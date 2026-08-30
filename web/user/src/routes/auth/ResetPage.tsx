/**
 * `/auth/reset?token=` —— P1，无替代。page-inventory §3.1 #4、§3.2.1。
 *
 * 唯一的特殊要求：**token 过期时一键重新发送，不要求用户回到上一页**。
 * 用户是从邮件点进来的，把他推回 `/auth/forgot` 等于让他重走一遍已经走过的路。
 *
 * ── 接线后的三条实现纪律 ─────────────────────────────────────────────
 *
 * 1. **401 不走全站那套「登录已过期」处置。** 这一页的 401 是
 *    `AUTH_TOKEN_INVALID`「重置链接无效或已过期」，与会话毫无关系 ——
 *    用户本来就没登录。`api.ts` 的 `handleAuthFailure` 在没有本地 token 时会直接返回
 *    （第 3 条 early return），所以不会误清会话；这一页要做的是把它渲染成
 *    「链接失效了，在这里再要一封」，而不是「请重新登录」。
 *
 *    后端把「不存在 / 已用过 / 已过期 / user_id 缺失」**全部**回同一个 401
 *    （`ResetPassword` 里那句「在 SQL 层已经不可区分，这是好事」），
 *    所以前端也不许编出「这个链接已经用过了」这种更具体的话 —— 我们区分不了。
 *
 * 2. **成功后不自动登录。** 后端 `ResetPassword` 撤销了该用户的**全部**会话，
 *    本来也没给我们任何 token。让用户用新密码登一次，是确认他真的记住了新密码 ——
 *    否则最典型的场景是：改完密码、关掉页面、第二天发现自己又进不去了。
 *
 * 3. **重发那封信走的是 `/auth/password/forgot`，所以「一律成功」那条防枚举规则同样适用。**
 *    实现只有一处（`shared.tsx` 的 `requestPasswordReset`），两页共用。
 *
 * ⚠️ `POST /auth/password/reset` **没有幂等键**（api-contract §9.1 的表里没有它）。
 * 它天然接近幂等 —— 令牌用一次就核销，重发只会拿到 401 —— 但「重发两次会不会
 * 把密码改成两遍」这件事上，真正挡住的是单飞与令牌核销，不是一个我们发过去也没人看的
 * `Idempotency-Key` 头。
 *
 * 这一页已接线，所以不再读 `?state=` 调试开关（README §7 代价 3）。
 */
import { useState, type FormEvent } from 'react';
import { Link, useSearchParams } from 'react-router';
import { Button, Card } from '@babelplus/shared/ui';
import { unwrapEmpty, type ApiError } from '@babelplus/shared/api';
import { AuthCard, Field, FormError } from '../../components/AuthForm.tsx';
import { api } from '../../lib/api.ts';
import {
  MailboxWhitelistGuide,
  PASSWORD_MAX_LENGTH,
  PASSWORD_MIN_LENGTH,
  PasswordStrengthBar,
  RESET_TOKEN_TTL_TEXT,
  asApiError,
  emailLooksValid,
  fallbackErrorCopy,
  passwordProblem,
  passwordProblemText,
  requestPasswordReset,
  resetMailErrorCopy,
  useCountdown,
  type ErrorCopy,
} from './shared.tsx';

export default function ResetPage() {
  const [params] = useSearchParams();
  const token = params.get('token');

  const [password, setPassword] = useState('');
  const [confirmPassword, setConfirmPassword] = useState('');
  // 二次确认。**不用弹窗**：可访问的确认对话框（焦点管理 + 键盘 + 屏幕阅读器）
  // 在本仓还不存在（web/README §7 代价 5），而一个做不对焦点的弹窗
  // 对键盘与读屏用户就是一道死路。行内展开的确认块没有这个问题。
  const [confirming, setConfirming] = useState(false);
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<ApiError | null>(null);
  const [done, setDone] = useState(false);
  const cooldown = useCountdown();

  const pwProblem = password === '' ? null : passwordProblem(password);
  const mismatch = confirmPassword !== '' && confirmPassword !== password;
  const ready = passwordProblem(password) === null && confirmPassword === password;

  async function onSubmit(event: FormEvent<HTMLFormElement>): Promise<void> {
    event.preventDefault();
    if (pending || cooldown.seconds !== null) return;
    // 两次不一致时**一个请求都不发**：这次往返注定失败，而失败的代价是
    // 用户以为自己改成功了（后端只收一个 password 字段，它认不出「两次不一致」）。
    if (!ready || token === null) return;
    if (!confirming) {
      setConfirming(true);
      return;
    }
    setPending(true);
    setError(null);
    try {
      await resetPassword(token, password);
      setDone(true);
    } catch (cause) {
      const apiError = asApiError(cause, '密码没能重置');
      setError(apiError);
      cooldown.start(apiError.retryAfterSeconds);
      setConfirming(false);
    } finally {
      setPending(false);
    }
  }

  if (done) return <DonePanel />;

  // token 从一开始就不在，或者后端明确说它无效 —— 两种都落到同一屏，
  // 只是开头那句话不同（一个是链接被截断，一个是链接过期）。
  if (token === null) return <ExpiredPanel hasToken={false} />;
  if (error?.code === 'AUTH_TOKEN_INVALID') return <ExpiredPanel hasToken />;

  return (
    <AuthCard
      title="设置新密码"
      description="设置完成后，其它设备上的登录会话会全部失效。"
      footer={
        <Link to="/auth/login" className="text-accent hover:underline">
          回到登录
        </Link>
      }
    >
      <form className="space-y-4" onSubmit={(event) => void onSubmit(event)}>
        <div>
          <Field
            label="新密码"
            name="password"
            type="password"
            autoComplete="new-password"
            required
            value={password}
            onChange={(event) => {
              setPassword(event.target.value);
              // 改了内容就要重新确认一次 —— 上一次确认针对的是上一个口令。
              setConfirming(false);
            }}
            hint={`${PASSWORD_MIN_LENGTH}–${PASSWORD_MAX_LENGTH} 个字符。我们只强制长度，不要求「必须含大小写数字符号」。`}
          />
          <PasswordStrengthBar password={password} />
          {pwProblem ? (
            <p className="mt-1.5 text-xs text-danger">{passwordProblemText(pwProblem)}</p>
          ) : null}
        </div>

        <div>
          <Field
            label="再输一次"
            name="password_confirm"
            type="password"
            autoComplete="new-password"
            required
            value={confirmPassword}
            onChange={(event) => {
              setConfirmPassword(event.target.value);
              setConfirming(false);
            }}
          />
          {mismatch ? <p className="mt-1.5 text-xs text-danger">两次输入不一样。</p> : null}
        </div>

        {error ? (
          <FormError>
            <span className="font-medium">{resetErrorCopy(error, cooldown.seconds).title}</span>
            <br />
            {resetErrorCopy(error, cooldown.seconds).description}
            {error.requestId ? (
              <>
                <br />
                <span className="font-mono text-xs">请求号 {error.requestId}</span>
              </>
            ) : null}
          </FormError>
        ) : null}

        {/* 二次确认：说清楚这一步的**副作用**，而不是问一句「确定吗」。
            全部会话失效在这里是有意的安全动作（找回密码的典型场景就是「号可能已经被别人拿到了」），
            但对一个只是忘了密码的人来说，「手机上的客户端要重新登录」是个真实的意外。 */}
        {confirming ? (
          <div className="rounded-lg border border-warn/30 bg-warn/10 p-3 text-sm leading-relaxed text-warn">
            <p className="font-medium">确认一下：这会让所有设备退出登录</p>
            <p className="mt-1">
              包括你自己的手机和电脑，也包括<strong className="font-medium">可能不是你本人</strong>的那些。
              改完之后，所有设备都要用新密码重新登录一次。订阅链接不受影响，已连上的节点也不会断。
            </p>
          </div>
        ) : null}

        <Button
          tone={confirming ? 'danger' : 'primary'}
          className="w-full"
          type="submit"
          disabled={pending || cooldown.seconds !== null || !ready}
        >
          {pending
            ? '正在设置…'
            : cooldown.seconds !== null
              ? `${cooldown.seconds} 秒后可再试`
              : confirming
                ? '确认，设置新密码'
                : '设置新密码'}
        </Button>

        {confirming && !pending ? (
          <Button className="w-full" onClick={() => setConfirming(false)}>
            先不改
          </Button>
        ) : null}
      </form>
    </AuthCard>
  );
}

/** 204，没有响应体。 */
function resetPassword(token: string, password: string): Promise<void> {
  return unwrapEmpty(api().POST('/api/v1/auth/password/reset', { body: { token, password } }));
}

/* ─────────────────────────── 失效 / 成功 ─────────────────────────── */

/**
 * 链接失效（或压根没带 token）。
 *
 * 关键：**在这一页就能重发**，不把用户踢回 `/auth/forgot`。
 * 他刚从邮件点进来，推回去等于让他把已经走过的一段再走一遍 ——
 * 而这一段的每一步（想起邮箱、去收件箱、翻垃圾箱）都是真实的流失点。
 */
function ExpiredPanel({ hasToken }: { hasToken: boolean }) {
  return (
    <Card>
      <h1 className="text-lg font-semibold text-fg">
        {hasToken ? '这个链接已经失效了' : '链接不完整'}
      </h1>
      <p className="mt-1.5 text-sm leading-relaxed text-fg-muted">
        {hasToken
          ? `重置链接只有 ${RESET_TOKEN_TTL_TEXT}有效，用过一次也会立刻作废。不用回上一页重来 —— 在下面直接再要一封。`
          : '地址里少了 token 参数。多半是邮件客户端把链接截断了 —— 复制完整链接再打开一次，或者在下面直接再要一封。'}
      </p>

      <div className="mt-4">
        <ResendForm />
      </div>
    </Card>
  );
}

/**
 * 就地重发。它自己一套三态，与主表单完全独立
 * （主表单此刻已经不在屏幕上，但「独立」这条纪律不因为看不见就放松：
 * 将来把两者放在同一屏时，这里不需要改）。
 */
function ResendForm() {
  const [email, setEmail] = useState('');
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<ApiError | null>(null);
  const [sent, setSent] = useState(false);
  const cooldown = useCountdown();

  async function onSubmit(event: FormEvent<HTMLFormElement>): Promise<void> {
    event.preventDefault();
    if (pending || cooldown.seconds !== null) return;
    if (!emailLooksValid(email.trim())) return;
    setPending(true);
    setError(null);
    try {
      // 与 `/auth/forgot` 同一个实现，所以「不泄漏邮箱是否存在」这条规则自动成立。
      await requestPasswordReset(email.trim());
      setSent(true);
    } catch (cause) {
      const apiError = asApiError(cause, '重置邮件没能发出');
      setError(apiError);
      cooldown.start(apiError.retryAfterSeconds);
    } finally {
      setPending(false);
    }
  }

  if (sent) {
    return (
      <div className="space-y-3">
        <p className="text-sm leading-relaxed text-fg-muted">
          如果这个邮箱在我们这里有账号，新的重置链接已经发出了，{RESET_TOKEN_TTL_TEXT}内有效。
        </p>
        <MailboxWhitelistGuide defaultOpen summary="没收到？先去垃圾箱看一眼，再把发信域名加白名单" />
        <Button className="w-full" onClick={() => setSent(false)}>
          换一个邮箱再发
        </Button>
      </div>
    );
  }

  return (
    <form className="space-y-3" onSubmit={(event) => void onSubmit(event)}>
      <Field
        label="邮箱"
        name="email"
        type="email"
        autoComplete="email"
        inputMode="email"
        required
        value={email}
        onChange={(event) => setEmail(event.target.value)}
      />

      {error ? (
        <FormError>
          <span className="font-medium">{resetMailErrorCopy(error, cooldown.seconds).title}</span>
          <br />
          {resetMailErrorCopy(error, cooldown.seconds).description}
          {error.requestId ? (
            <>
              <br />
              <span className="font-mono text-xs">请求号 {error.requestId}</span>
            </>
          ) : null}
        </FormError>
      ) : null}

      <Button
        tone="primary"
        className="w-full"
        type="submit"
        disabled={pending || cooldown.seconds !== null || !emailLooksValid(email.trim())}
      >
        {pending
          ? '正在发送…'
          : cooldown.seconds !== null
            ? `${cooldown.seconds} 秒后可再试`
            : '重新发一封'}
      </Button>

      <p className="text-center text-sm">
        <Link to="/auth/login" className="text-accent hover:underline">
          回到登录
        </Link>
      </p>
    </form>
  );
}

/** 成功。**这里没有「进入面板」按钮，是刻意的** —— 见文件头第 2 条。 */
function DonePanel() {
  return (
    <Card>
      <h1 className="text-lg font-semibold text-fg">密码已经改好了</h1>
      <p className="mt-1.5 text-sm leading-relaxed text-fg-muted">
        所有设备上的登录都已经失效 —— 包括可能不是你本人的那些。
        现在用新密码登一次，也顺便确认你确实记住了它。
      </p>
      <p className="mt-3 text-sm leading-relaxed text-fg-muted">
        订阅链接没有变，已经连上的客户端不受影响，不用重新导入。
      </p>
      <div className="mt-5">
        <Link
          to="/auth/login"
          className="inline-flex min-h-11 w-full items-center justify-center rounded-lg bg-accent px-4 text-sm font-medium text-accent-fg transition-colors hover:bg-accent-strong"
        >
          用新密码登录
        </Link>
      </div>
    </Card>
  );
}

/* ─────────────────────────── 错误文案 ─────────────────────────── */

/**
 * `ErrorCode` → 文案。**这一页唯一按 code 分支的地方**。
 *
 * `AUTH_TOKEN_INVALID` 不在这张表里是有意的：它有自己的一整屏（`ExpiredPanel`），
 * 在这里再写一条文案会让两处措辞迟早对不上。
 */
function resetErrorCopy(error: ApiError, seconds: number | null): ErrorCopy {
  switch (error.code) {
    case 'VALIDATION_FAILED':
      // 后端这个端点只有一条 422：口令长度。details 里是 password_length_out_of_range。
      return { title: '密码长度不合要求', description: error.message };
    case 'QUOTA_RATE_LIMITED':
      return {
        title: '太频繁了',
        description:
          seconds === null ? '稍后再试一次。' : `${seconds} 秒后可以再试。`,
      };
    default:
      return fallbackErrorCopy(error);
  }
}

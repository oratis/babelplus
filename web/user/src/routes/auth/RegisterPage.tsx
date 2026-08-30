/**
 * `/auth/register` —— P1。page-inventory §3.1 #2、§3.2.1；user-journey §3。
 *
 * 与竞品的关键差异：**邀请码必填，不是可选**。
 * 邀请码是注册的第一道闸（也是唯一一道），所以它排在最前面 ——
 * 用户在填完邮箱密码之后才发现自己没有码，是最差的顺序。
 *
 * 两种失败必须**分开说**（§3.2.1）：
 *   邀请码无效 vs 已用尽 → 文案不同，后者要给「找邀请你的人再要一个」
 *   邮箱已注册 → 引导到登录，不要当成错误报
 *
 * ── 接线后的三条实现纪律 ─────────────────────────────────────────────
 *
 * 1. **顺序是 verify → 发码 → 注册，不能反。** `handler/auth.go` 的 `RegisterAccount`
 *    先验邀请码、再验邮箱验证码、最后才建账号并核销码 —— 理由是邀请码是稀缺资源，
 *    先核销后失败等于每输错一次验证码就烧掉一个码。前端这一页的两步式表单是同一条顺序的
 *    用户侧投影：失焦先 verify 一次，是为了不让人填完整张表才发现码不对。
 *
 * 2. **422 的真正原因在 `details[].reason` 里，不在 `message` 里。**
 *    后端对「邀请码无效」「邀请码已用尽」「验证码过期」「验证码错太多次」全部回
 *    422 `VALIDATION_FAILED`，彼此只靠 `details` 区分（`s.unprocessable(...)` +
 *    `detail(field, reason)`）。只看 `error.code` 就把四种情况写成同一句话，
 *    §3.2.1 那条「两种失败必须分开说」当场作废，而页面不会报任何错。
 *    契约允许读 `details`，禁止的是匹配 `message`（api-contract §2.3）。
 *
 * 3. **409 `STATE_CONFLICT`（邮箱已注册）不是错误，是一条岔路。**
 *    渲染成红色错误框，用户会以为自己填错了什么并反复重试；正确的动作是去登录或找回密码。
 *
 * ⚠️ **这两个写端点都没有幂等键**：api-contract §9.1 的幂等总表里，
 * `/auth/email-code` 明写「无 —— 靠限流而非幂等」，`/auth/register` 根本不在表里，
 * 服务端不认 `Idempotency-Key`。发一个过去只会让代码看起来比实际安全。
 * 所以这里只有**单飞**（`pending` 挡住重复点击）与限流倒计时；
 * 「超时后重发」这个缺口留在原处，不假装它不存在 —— 好在注册的重发是安全的：
 * 邮箱唯一索引会让第二次落到 409，而不是建出两个账号。
 *
 * 这一页已接线，所以**不再读 `?state=` 调试开关**（README §7 代价 3）。
 */
import { useEffect, useState, type FormEvent } from 'react';
import { Link, useNavigate } from 'react-router';
import { Button, Card } from '@babelplus/shared/ui';
import { unwrap, unwrapEmpty, type ApiError, type components } from '@babelplus/shared/api';
import { AuthCard, AuthTodo, Field, FormError } from '../../components/AuthForm.tsx';
import { api } from '../../lib/api.ts';
import { useAuth } from '../../lib/auth.tsx';
import { session } from '../../lib/session.ts';
import {
  EMAIL_CODE_MIN_GAP_SECONDS,
  MailboxWhitelistGuide,
  PASSWORD_MAX_LENGTH,
  PASSWORD_MIN_LENGTH,
  PasswordStrengthBar,
  asApiError,
  detailReason,
  emailLooksValid,
  fallbackErrorCopy,
  normalizeInviteCode,
  passwordProblem,
  passwordProblemText,
  plausibleInviteCode,
  useCountdown,
  type ErrorCopy,
} from './shared.tsx';

type InviteVerifyResult = components['schemas']['InviteVerifyResult'];

/**
 * 验证码位数。事实源：`handler/auth.go` 的 `emailCodeDigits = 6`。
 * 写死一个前端常量是安全的（后端生成的就是 6 位），但**它是一份拷贝** ——
 * 后端改位数时这里必须跟着改，否则用户会被自己这边的表单挡住。
 */
const EMAIL_CODE_LENGTH = 6;

/** 验证码有效期，只用于文案。事实源：`handler/auth.go` 的 `emailCodeTTL = 10 * time.Minute`。 */
const EMAIL_CODE_TTL_TEXT = '10 分钟';

/**
 * 两步式：填表 → 填验证码 → 成功页。
 *
 * 为什么把验证码做成同一路由内的第二步而不是独立路由：中途刷新或后退时，
 * 一个独立路由会把邮箱与密码丢掉（它们不该进 URL，也不该进 storage），
 * 用户要从头再填一遍，而这时他手里已经有一封验证码邮件了。
 */
type Step = 'form' | 'code' | 'done';

/** 邀请码失焦预检的独立三态（外加两个「后端明确说了不行」的终态）。 */
type InviteCheck =
  | { readonly state: 'idle' }
  | { readonly state: 'checking' }
  | { readonly state: 'ok' }
  | { readonly state: 'invalid' }
  | { readonly state: 'exhausted' }
  | { readonly state: 'error'; readonly error: ApiError };

export default function RegisterPage() {
  const navigate = useNavigate();
  // 注册成功后后端**直接返回会话**（openapi：201「注册成功，直接返回会话」），
  // 所以这里要把它接进登录态，否则成功页上那个「进入面板」按钮会被守卫弹回登录页。
  const { reload } = useAuth();

  const [step, setStep] = useState<Step>('form');
  const [inviteCode, setInviteCode] = useState('');
  const [email, setEmail] = useState('');
  const [password, setPassword] = useState('');
  const [emailCode, setEmailCode] = useState('');

  // 邀请码预检：**自己一套状态**，与两个写请求互不干扰。
  const [invite, setInvite] = useState<InviteCheck>({ state: 'idle' });
  const [checkedCode, setCheckedCode] = useState<string | null>(null);

  // 发码与注册各自一套 pending / error / 倒计时（硬规矩 3：三态各请求独立）。
  const [sending, setSending] = useState(false);
  const [sendError, setSendError] = useState<ApiError | null>(null);
  const sendCooldown = useCountdown();

  const [submitting, setSubmitting] = useState(false);
  const [submitError, setSubmitError] = useState<ApiError | null>(null);
  const submitCooldown = useCountdown();

  // user-journey §3：验证码页上「60 秒没收到？」的白名单引导要**自动展开**。
  // 一进这一步就展开等于喊狼来了（邮件通常几秒就到），60 秒之后才是真的可能出事。
  const [guideOpen, setGuideOpen] = useState(false);
  useEffect(() => {
    if (step !== 'code') return;
    const timer = window.setTimeout(() => setGuideOpen(true), EMAIL_CODE_MIN_GAP_SECONDS * 1000);
    return () => window.clearTimeout(timer);
  }, [step]);

  const normalizedInvite = normalizeInviteCode(inviteCode);
  const pwProblem = password === '' ? null : passwordProblem(password);

  /**
   * 邀请码失焦预检。
   *
   * 长度不在 4–32 之间就**不发请求** —— 后端 `plausibleInviteCode` 对这种输入根本不查库，
   * 发过去只是白白消耗那条 per-IP 30/min 的限额（`bucketInviteIPMinute`）。
   */
  async function checkInvite(): Promise<void> {
    const code = normalizedInvite;
    if (code === checkedCode) return;
    if (!plausibleInviteCode(code)) {
      setInvite({ state: 'idle' });
      setCheckedCode(null);
      return;
    }
    setCheckedCode(code);
    setInvite({ state: 'checking' });
    try {
      const result = await verifyInviteCode(code);
      // 契约的三态（`InviteVerifyResult.state`）原样接住：
      // **「无效」与「已用尽」是两件事**，合并成一句「邀请码不可用」就是 §3.2.1 点名要防的那种写法。
      setInvite(
        result.state === 'ok'
          ? { state: 'ok' }
          : result.state === 'exhausted'
            ? { state: 'exhausted' }
            : { state: 'invalid' },
      );
    } catch (cause) {
      setInvite({ state: 'error', error: asApiError(cause, '邀请码校验失败') });
    }
  }

  /** 第一步：发验证码。**这一步不建账号，也不核销邀请码。** */
  async function onSendCode(event: FormEvent<HTMLFormElement>): Promise<void> {
    event.preventDefault();
    if (sending || sendCooldown.seconds !== null) return;
    if (!canSend()) return;
    setSending(true);
    setSendError(null);
    try {
      await sendEmailCode(email.trim());
      setStep('code');
      // 契约写死的「同一邮箱两次间隔 ≥ 60 s」，本地起表省掉一次注定 429 的往返。
      // 这不是在编秒数 —— 编的是 429 的剩余秒数，那个只能读 `Retry-After`。
      sendCooldown.start(EMAIL_CODE_MIN_GAP_SECONDS);
    } catch (cause) {
      const error = asApiError(cause, '验证码没能发出');
      setSendError(error);
      sendCooldown.start(error.retryAfterSeconds);
    } finally {
      setSending(false);
    }
  }

  /** 验证码页上的重发。与第一步同一个端点、同一套限流。 */
  async function onResend(): Promise<void> {
    if (sending || sendCooldown.seconds !== null) return;
    setSending(true);
    setSendError(null);
    try {
      await sendEmailCode(email.trim());
      sendCooldown.start(EMAIL_CODE_MIN_GAP_SECONDS);
    } catch (cause) {
      const error = asApiError(cause, '验证码没能重发');
      setSendError(error);
      sendCooldown.start(error.retryAfterSeconds);
    } finally {
      setSending(false);
    }
  }

  /**
   * 第二步：真正建账号。
   *
   * 「二次确认」在这条链路上就是**验证码本身** —— 用户必须去邮箱取一次码才能走到这里，
   * 这比一个「确定要注册吗？」的弹窗强得多（后者只是多一次点击，挡不住任何误操作）。
   */
  async function onRegister(event: FormEvent<HTMLFormElement>): Promise<void> {
    event.preventDefault();
    if (submitting || submitCooldown.seconds !== null) return;
    if (emailCode.trim().length !== EMAIL_CODE_LENGTH) return;
    setSubmitting(true);
    setSubmitError(null);
    try {
      const tokens = await registerAccount({
        invite_code: normalizedInvite,
        email: email.trim(),
        password,
        email_code: emailCode.trim(),
      });
      // 后端两个字段是同一个值（auth.go 的 sessionTokens），存 access_token —— 与 `signIn` 一致。
      session().setToken(tokens.access_token);
      setStep('done');
      // 拉一次 `/me` 把登录态从 anonymous 推到 authenticated。
      // 失败也不回滚：token 已经是有效的，守卫那边会给一个可重试的错误态，
      // 而把成功的注册显示成失败是更坏的结果。
      await reload();
    } catch (cause) {
      const error = asApiError(cause, '注册没能完成');
      setSubmitError(error);
      submitCooldown.start(error.retryAfterSeconds);
    } finally {
      setSubmitting(false);
    }
  }

  if (step === 'done') return <DonePanel onEnter={() => navigate('/dashboard', { replace: true })} />;

  if (step === 'code') {
    return (
      <AuthCard
        title="填入验证码"
        description={
          <>
            验证码已经发到 <span className="font-medium text-fg">{email.trim()}</span>，
            {EMAIL_CODE_TTL_TEXT}内有效。填对之后我们才会真正建账号并核销邀请码。
          </>
        }
        footer={
          <button
            type="button"
            className="text-accent hover:underline"
            onClick={() => {
              setStep('form');
              setEmailCode('');
              setSubmitError(null);
            }}
          >
            填错邮箱了？回上一步改
          </button>
        }
      >
        <form className="space-y-4" onSubmit={(event) => void onRegister(event)}>
          <Field
            label={`${EMAIL_CODE_LENGTH} 位验证码`}
            name="email_code"
            inputMode="numeric"
            autoComplete="one-time-code"
            maxLength={EMAIL_CODE_LENGTH}
            required
            value={emailCode}
            onChange={(event) => setEmailCode(event.target.value.replace(/\D/g, ''))}
          />

          {/* 🔴 常驻（60 秒后自动展开）。邮件是 ADR 0002 裁决的唯一失联恢复通道，
              收不到验证码的用户就是封锁当天必然失联的那批人 —— 这一块不是装饰。 */}
          <MailboxWhitelistGuide defaultOpen={guideOpen} />

          {sendError ? <ErrorBlock error={sendError} seconds={sendCooldown.seconds} /> : null}
          {submitError?.code === 'STATE_CONFLICT' ? (
            <EmailTakenNotice email={email.trim()} />
          ) : submitError ? (
            <ErrorBlock error={submitError} seconds={submitCooldown.seconds} />
          ) : null}

          <Button
            tone="primary"
            className="w-full"
            type="submit"
            disabled={
              submitting ||
              submitCooldown.seconds !== null ||
              emailCode.trim().length !== EMAIL_CODE_LENGTH
            }
          >
            {submitting
              ? '正在注册…'
              : submitCooldown.seconds !== null
                ? `${submitCooldown.seconds} 秒后可再试`
                : '完成注册'}
          </Button>

          <Button
            className="w-full"
            onClick={() => void onResend()}
            disabled={sending || sendCooldown.seconds !== null}
          >
            {sending
              ? '正在发送…'
              : sendCooldown.seconds !== null
                ? `${sendCooldown.seconds} 秒后可重发`
                : '重新发一封'}
          </Button>
        </form>
      </AuthCard>
    );
  }

  return (
    <AuthCard
      title="注册"
      description="babel.plus 是邀请制的。没有邀请码就注册不了 —— 这是刻意的。"
      footer={
        <span>
          已经有账号了？{' '}
          <Link to="/auth/login" className="text-accent hover:underline">
            去登录
          </Link>
        </span>
      }
    >
      <form className="space-y-4" onSubmit={(event) => void onSendCode(event)}>
        {/* 邀请码放第一位：它是唯一一道闸，填不出来就没必要继续往下填。 */}
        <Field
          label="邀请码（必填）"
          name="invite_code"
          autoComplete="off"
          placeholder="向邀请你的人索取"
          required
          maxLength={64}
          value={inviteCode}
          onChange={(event) => setInviteCode(event.target.value)}
          onBlur={() => void checkInvite()}
          hint="失焦时会先校验一次，避免你填完整张表才发现码不对。"
        />
        <InviteHint check={invite} />

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
        <div>
          <Field
            label="密码"
            name="password"
            type="password"
            autoComplete="new-password"
            required
            value={password}
            onChange={(event) => setPassword(event.target.value)}
            hint="找回密码依赖邮件送达，所以邮箱要选一个你确实能收信的。"
          />
          {/* 纯前端估算，不调用任何第三方服务（口令不出浏览器）。
              强度只是提示，硬门槛只有长度 —— 后端 `validPassword` 就是这么判的。 */}
          <PasswordStrengthBar password={password} />
          {pwProblem ? (
            <p className="mt-1.5 text-xs text-danger">{passwordProblemText(pwProblem)}</p>
          ) : null}
        </div>

        {/* TODO(P1): 条款勾选 —— 服务条款 / 隐私 / 退款三份法务页。
            ⚠️ 退款政策目前未定（pricing-and-plans §7），page-inventory §7 把它列为**上线前置条件**，
            不是待办事项。勾选框链到一份不存在的页面比没有勾选框更糟。 */}

        {sendError ? <ErrorBlock error={sendError} seconds={sendCooldown.seconds} /> : null}

        <Button tone="primary" className="w-full" type="submit" disabled={!canSend()}>
          {sending
            ? '正在发送…'
            : sendCooldown.seconds !== null
              ? `${sendCooldown.seconds} 秒后可再试`
              : '下一步：发送邮箱验证码'}
        </Button>
      </form>

      <AuthTodo>
        还差一件事没做：<strong className="font-medium">条款勾选</strong>。
        它卡在退款政策未定上（pricing-and-plans §7），而 page-inventory §7 把退款政策列为
        <strong className="font-medium">上线前置条件</strong> —— 勾选框链到一份不存在的法务页，
        比暂时没有勾选框更糟。
      </AuthTodo>
    </AuthCard>
  );

  /**
   * 能不能发码。
   *
   * 🔴 `invite.state === 'error'` **不拦**：预检打不通（网络、限流、501）不等于码不对，
   * 拦住它等于让一次预检失败把整条注册链路堵死，而后端注册时本来就会再验一次。
   * 拦的只有后端**明确说过不行**的那两种。
   */
  function canSend(): boolean {
    if (sending || sendCooldown.seconds !== null) return false;
    if (!plausibleInviteCode(normalizedInvite)) return false;
    if (invite.state === 'invalid' || invite.state === 'exhausted') return false;
    if (!emailLooksValid(email.trim())) return false;
    return passwordProblem(password) === null;
  }
}

/* ─────────────────────────── 请求 ─────────────────────────── */

function verifyInviteCode(code: string): Promise<InviteVerifyResult> {
  return unwrap(api().GET('/api/v1/invite/verify', { params: { query: { code } } }));
}

/** 204，没有响应体。`scene` 是契约里的枚举值，与 DB 的 purpose 命名**不同**（后端负责翻译）。 */
function sendEmailCode(email: string): Promise<void> {
  return unwrapEmpty(api().POST('/api/v1/auth/email-code', { body: { email, scene: 'register' } }));
}

function registerAccount(body: components['schemas']['RegisterRequest']) {
  return unwrap(api().POST('/api/v1/auth/register', { body }));
}

/* ─────────────────────────── 展示 ─────────────────────────── */

/** 邀请码预检的四种结果。**「无效」与「已用尽」的下一步动作完全不同**，所以文案也完全不同。 */
function InviteHint({ check }: { check: InviteCheck }) {
  switch (check.state) {
    case 'idle':
      return null;
    case 'checking':
      return <p className="text-xs text-fg-muted">正在校验邀请码…</p>;
    case 'ok':
      return <p className="text-xs text-ok">这个邀请码可以用。</p>;
    case 'invalid':
      return (
        <p className="text-xs text-danger">
          这个邀请码无效 —— 不存在、已被吊销，或者已经过期。
          码是全大写的，且不含 <span className="font-mono">0 O 1 I l</span> 这类易混字符，
          先对着来源核一遍有没有抄错。
        </p>
      );
    case 'exhausted':
      return (
        <p className="text-xs text-warn">
          这个码已经被用掉了。邀请码是一次性的 ——
          <strong className="font-medium">找邀请你的人再要一个新的</strong>，他在面板的「邀请」页可以再生成。
        </p>
      );
    case 'error': {
      const copy = fallbackErrorCopy(check.error);
      return (
        <p className="text-xs text-fg-muted">
          没能提前校验这个码（{copy.title}）。可以直接继续 —— 提交时后端还会再验一次。
        </p>
      );
    }
  }
}

/**
 * 邮箱已注册（409 `STATE_CONFLICT`）。
 * **不用 `FormError` 的红框**：这不是「你填错了」，是「你已经有账号了」，
 * 用户要的是一条去登录的路，不是一次重试。
 */
function EmailTakenNotice({ email }: { email: string }) {
  return (
    <div role="alert" className="rounded-lg border border-accent/30 bg-accent/5 px-3 py-2 text-sm">
      <p className="font-medium text-fg">这个邮箱已经注册过了</p>
      <p className="mt-1 leading-relaxed text-fg-muted">
        <span className="font-mono">{email}</span> 在我们这里已经有账号。
        邀请码没有被消耗掉，留着给别人用。
      </p>
      <p className="mt-2 flex flex-wrap gap-x-4 gap-y-1">
        <Link to="/auth/login" className="text-accent hover:underline">
          去登录
        </Link>
        <Link to="/auth/forgot" className="text-accent hover:underline">
          忘记密码了
        </Link>
      </p>
    </div>
  );
}

function ErrorBlock({ error, seconds }: { error: ApiError; seconds: number | null }) {
  const copy = registerErrorCopy(error, seconds);
  return (
    <FormError>
      <span className="font-medium">{copy.title}</span>
      <br />
      {copy.description}
      {error.requestId ? (
        <>
          <br />
          <span className="font-mono text-xs">请求号 {error.requestId}</span>
        </>
      ) : null}
    </FormError>
  );
}

/**
 * 注册成功页。
 *
 * 🔴 白名单引导在这里是**硬要求**（roadmap §5.2 2.C 第 3 条），不是收尾的客套话：
 * ADR 0002 裁决邮件是唯一的失联恢复通道，而用户此刻刚刚证明了自己能收到我们的信 ——
 * 这是让他做一次「加白名单」的最好时机，往后所有的到期提醒、域名广播、重置链接都靠它。
 */
function DonePanel({ onEnter }: { onEnter: () => void }) {
  return (
    <Card>
      <h1 className="text-lg font-semibold text-fg">注册完成</h1>
      <p className="mt-1.5 text-sm leading-relaxed text-fg-muted">
        账号已经建好，邀请码也核销了。你已经是登录状态，直接进面板就行。
      </p>

      <div className="mt-4">
        <MailboxWhitelistGuide
          defaultOpen
          summary="先花 30 秒把发信域名加进白名单（很重要）"
        />
      </div>

      <p className="mt-4 text-sm leading-relaxed text-fg-muted">
        为什么现在就做：面板域名万一被墙，
        <strong className="font-medium text-fg">邮件是我们唯一能联系上你的路</strong>
        —— 新域名、到期提醒、重置链接都走它。等到收不到信的那天再补就来不及了。
      </p>

      <div className="mt-5">
        <Button tone="primary" className="w-full" onClick={onEnter}>
          进入面板
        </Button>
      </div>
    </Card>
  );
}

/* ─────────────────────────── 错误文案 ─────────────────────────── */

/**
 * `ErrorCode`（必要时再看 `details[].reason`）→ 文案。
 * **这一页唯一按 code 分支的地方**，别处不许再写第二处。
 *
 * 分支顺序是 `code` → `details.reason` → `kind` 兜底。
 * 绝不看 `message`：api-contract §2.3 明令禁止，且后端随时会改措辞。
 */
function registerErrorCopy(error: ApiError, seconds: number | null): ErrorCopy {
  switch (error.code) {
    case 'VALIDATION_FAILED': {
      // 422 是这一页最主要的失败形态，四类原因全靠 details 区分。
      const invite = detailReason(error, 'invite_code');
      if (invite === 'invite_code_exhausted') {
        return {
          title: '这个邀请码已经被用掉了',
          description: '邀请码是一次性的。找邀请你的人再要一个新的 —— 他在「邀请」页可以再生成。',
        };
      }
      if (invite === 'invite_code_invalid') {
        return {
          title: '邀请码无效',
          description: '这个码不存在、已被吊销或已过期。码是全大写且不含 0 O 1 I l，先核一遍有没有抄错。',
        };
      }

      const code = detailReason(error, 'email_code');
      if (code === 'email_code_expired') {
        return {
          title: '验证码已经过期',
          description: `验证码只有 ${EMAIL_CODE_TTL_TEXT}有效。点下面的「重新发一封」再要一个。`,
        };
      }
      if (code === 'email_code_attempts_exceeded') {
        return {
          title: '这个验证码错太多次了',
          description: '出于安全，它已经作废。重新发一封，用新的码再试。',
        };
      }
      if (code === 'email_code_invalid' || code === 'email_code_required') {
        return {
          title: '验证码不正确',
          description: '对着最新的一封邮件再核一遍 —— 如果你连着要过好几次码，只有最后一封有效。',
        };
      }

      if (detailReason(error, 'password') === 'password_length_out_of_range') {
        return {
          title: '密码长度不合要求',
          description: `密码需要 ${PASSWORD_MIN_LENGTH}–${PASSWORD_MAX_LENGTH} 个字符（按字符数算，一个中文或一个 emoji 都是 1 个）。`,
        };
      }
      if (detailReason(error, 'email') === 'email_invalid') {
        return { title: '邮箱格式不对', description: '检查一下有没有多余的空格，或者 @ 后面少了点。' };
      }
      return { title: '填写有误', description: error.message };
    }

    case 'STATE_CONFLICT':
      // 正常情况下走不到这里（409 有自己的一块 UI），留着是为了兜住别的调用点。
      return { title: '这个邮箱已经注册过了', description: '直接去登录，或者用「忘记密码」找回。' };

    case 'QUOTA_RATE_LIMITED':
      return {
        title: '太频繁了',
        description:
          seconds === null
            ? '同一个邮箱一小时最多要 3 次验证码，两次之间还要隔 60 秒。稍后再试。'
            : `同一个邮箱一小时最多要 3 次验证码，两次之间还要隔 60 秒。${seconds} 秒后可以再试。`,
      };

    default:
      return fallbackErrorCopy(error);
  }
}

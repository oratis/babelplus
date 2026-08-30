/**
 * `/auth/forgot` —— P1，无替代。page-inventory §3.1 #3、§3.2.1。
 *
 * 两条硬规则：
 *  1. **无论邮箱是否存在都返回同样的成功文案**（防枚举）。这条要前后端一起守，
 *     前端不能因为「后端返回了 404」就显示「该邮箱未注册」。
 *  2. 成功页必须写「**请检查垃圾箱**，并把发信域名加入白名单」。
 *     找回密码的成功率 = 邮件送达率，而 ADR 0002 §5 已把邮件从「配置项」升级为「核心基础设施」。
 *
 * ── 接线后必须说清楚的一件事：「一律成功」到底一律到哪里 ──────────────────
 *
 * 后端 `ForgotPassword` 的形状是：**邮箱不存在照样返回 204**、**per email 超限也返回 204**，
 * 只有 **per IP 超限**才 429（api-contract §10.1 明写这个不对称，理由是 per email 的 429
 * 会把「这个邮箱最近请求过重置」泄漏出去）。
 *
 * 所以这一页的分支不是按「成功 / 失败」，而是按**这个失败会不会泄漏邮箱是否存在**：
 *
 *   · 204 / 404 / 409 / 422 → **一律显示同一个成功页**。
 *     这几种都可能因「这个邮箱在不在」而产生差异，所以必须抹平。
 *   · 429 `QUOTA_RATE_LIMITED` → 倒计时。它是 per IP 的，与邮箱是否存在无关，
 *     显示出来不泄漏任何东西；而假装成功会让用户一直等一封根本没发的信。
 *   · 5xx / 网络不可达 / 501 → **如实报错**。
 *     🔴 这一条是对空壳里那句「无论后端返回什么」的收窄，理由要写下来：
 *     防枚举要防的是「存在与不存在两条路径可被区分」，而 5xx 与网络故障对两种邮箱
 *     同样会发生 —— 把它们显示成「邮件已发出」拿不到任何防枚举收益，
 *     只会让一个正在找回账号的人白等一封永远不会到的邮件。
 *     而这一页的成功率就是失联恢复的成功率（ADR 0002）。
 *
 * ⚠️ 这个端点**没有幂等键**（api-contract §9.1 的表里没有它），服务端不认
 * `Idempotency-Key`。防重复靠单飞 + 限流，不发一个假的头过去装作安全。
 * 也**不加二次确认弹窗**：这是失联恢复入口，给正在着急的人多一次点击是负收益，
 * 而重复发信的实际代价由 per email 3/h 挡着。
 *
 * 这一页已接线，所以不再读 `?state=` 调试开关（README §7 代价 3）。
 */
import { useState, type FormEvent } from 'react';
import { Link } from 'react-router';
import { Button, Card } from '@babelplus/shared/ui';
import { type ApiError } from '@babelplus/shared/api';
import { AuthCard, Field, FormError } from '../../components/AuthForm.tsx';
import {
  MailboxWhitelistGuide,
  RESET_TOKEN_TTL_TEXT,
  asApiError,
  emailLooksValid,
  requestPasswordReset,
  resetMailErrorCopy,
  useCountdown,
} from './shared.tsx';

export default function ForgotPage() {
  const [email, setEmail] = useState('');
  const [sent, setSent] = useState<string | null>(null);
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<ApiError | null>(null);
  const cooldown = useCountdown();

  async function onSubmit(event: FormEvent<HTMLFormElement>): Promise<void> {
    event.preventDefault();
    // 单飞：这个端点没有幂等键，重复提交就是重复消耗 SES 配额。
    if (pending || cooldown.seconds !== null) return;
    if (!emailLooksValid(email.trim())) return;
    setPending(true);
    setError(null);
    try {
      await requestPasswordReset(email.trim());
      setSent(email.trim());
    } catch (cause) {
      const apiError = asApiError(cause, '重置邮件没能发出');
      setError(apiError);
      // 429 的秒数只能来自 `Retry-After`（CORS 已 expose）。读不到就不显示倒计时。
      cooldown.start(apiError.retryAfterSeconds);
    } finally {
      setPending(false);
    }
  }

  if (sent !== null) {
    return (
      <SentPanel
        email={sent}
        onRetryAnotherEmail={() => {
          setSent(null);
          setError(null);
        }}
      />
    );
  }

  return (
    <AuthCard
      title="找回密码"
      description="我们会给这个邮箱发一封重置链接。"
      footer={
        <Link to="/auth/login" className="text-accent hover:underline">
          回到登录
        </Link>
      }
    >
      <form className="space-y-4" onSubmit={(event) => void onSubmit(event)}>
        <Field
          label="邮箱"
          name="email"
          type="email"
          autoComplete="email"
          inputMode="email"
          required
          value={email}
          onChange={(event) => setEmail(event.target.value)}
          hint="填注册时用的那个。我们不会告诉你它在不在我们这里 —— 那等于替别人查账号。"
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
              : '发送重置链接'}
        </Button>
      </form>
    </AuthCard>
  );
}

/**
 * 「已发送」页 —— **这一页真正重要的那一屏**。
 *
 * 它同时是防枚举的落点（不存在的邮箱看到的是一模一样的这一屏）
 * 和送达率的落点（白名单引导）。两件事都不能省。
 */
function SentPanel({
  email,
  onRetryAnotherEmail,
}: {
  email: string;
  onRetryAnotherEmail: () => void;
}) {
  return (
    <Card>
      <h1 className="text-lg font-semibold text-fg">邮件已发出</h1>
      <p className="mt-1.5 text-sm leading-relaxed text-fg-muted">
        如果 <span className="font-mono text-fg">{email}</span> 在我们这里有账号，
        重置链接已经在路上了。链接 {RESET_TOKEN_TTL_TEXT}内有效。
      </p>

      {/* 这一块不是客套话，是这一页的主要内容：送达率决定了整个流程的成功率。
          QQ / 163 / 126 经常把新发信域名判为垃圾，而 QQ 的白名单优先级高于黑名单与反垃圾规则。 */}
      <div className="mt-4">
        <MailboxWhitelistGuide defaultOpen summary="没收到？先去垃圾箱看一眼，再把发信域名加白名单" />
      </div>

      <p className="mt-4 text-sm leading-relaxed text-fg-muted">
        还是收不到，就换一个邮箱重试，或者直接联系邀请你的人。
      </p>

      <div className="mt-4 flex flex-wrap items-center gap-x-4 gap-y-2">
        <Button onClick={onRetryAnotherEmail}>换一个邮箱</Button>
        <Link to="/auth/login" className="text-sm text-accent hover:underline">
          回到登录
        </Link>
      </div>
    </Card>
  );
}

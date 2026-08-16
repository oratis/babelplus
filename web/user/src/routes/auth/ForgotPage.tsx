/**
 * `/auth/forgot` —— P1，无替代。page-inventory §3.1 #3、§3.2.1。
 *
 * 两条硬规则：
 *  1. **无论邮箱是否存在都返回同样的成功文案**（防枚举）。这条要前后端一起守，
 *     前端不能因为「后端返回了 404」就显示「该邮箱未注册」。
 *  2. 成功页必须写「**请检查垃圾箱**，并把发信域名加入白名单」。
 *     找回密码的成功率 = 邮件送达率，而 ADR 0002 §5 已把邮件从「配置项」升级为「核心基础设施」。
 */
import { Link } from 'react-router';
import { Button, Card } from '@babelplus/shared/ui';
import { AuthCard, AuthTodo, Field } from '../../components/AuthForm.tsx';
import { useShellState } from '../../lib/shell.ts';

export default function ForgotPage() {
  const { state } = useShellState();

  // `?state=ready` 之外用 `?state=empty` 预览「已发送」页 —— 它是这一页真正重要的那一屏。
  if (state === 'empty') return <SentPanel />;

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
      <Field label="邮箱" name="email" type="email" autoComplete="email" inputMode="email" />

      {/* TODO(P1): 提交 → `forgotPassword`。
          🔴 **无论后端返回什么，前端一律显示同一个成功页** —— 包括 404 / 422。
          唯一例外是 429（限流），那时显示倒计时。
          这一页会消耗邮件配额，而邮件是核心基础设施，所以它和登录页一样需要严格限流。 */}
      <Button tone="primary" className="w-full" disabled>
        发送重置链接
      </Button>

      <AuthTodo>
        尚未接线。契约：<code className="font-mono">forgotPassword</code>。
        <br />
        用 <code className="font-mono">?state=empty</code> 预览「已发送」页。
      </AuthTodo>
    </AuthCard>
  );
}

function SentPanel() {
  return (
    <Card>
      <h1 className="text-lg font-semibold text-fg">邮件已发出</h1>
      <p className="mt-1.5 text-sm leading-relaxed text-fg-muted">
        如果这个邮箱在我们这里有账号，重置链接已经在路上了。链接 30 分钟内有效。
      </p>

      {/* 这一块不是客套话，是这一页的主要内容：送达率决定了整个流程的成功率。 */}
      <div className="mt-4 rounded-lg border border-warn/30 bg-warn/10 p-3 text-sm leading-relaxed text-warn">
        <p className="font-medium">没收到？先去垃圾箱看一眼。</p>
        <p className="mt-1">
          QQ / 163 / 126 邮箱经常把新发信域名判为垃圾。把我们的发信域名加入白名单可以一劳永逸 ——
          QQ 邮箱的白名单优先级高于黑名单与反垃圾规则。
        </p>
        {/* TODO(P1): 这里链到教程站的「把发信域名加白名单」一篇（tutorials-spec）。
            链接地址从运行时配置的 docsUrl 拼，不硬编码。 */}
      </div>

      <p className="mt-4 text-sm text-fg-muted">
        还是收不到，就换一个邮箱重试，或者直接联系邀请你的人。
      </p>

      <div className="mt-4">
        <Link to="/auth/login" className="text-sm text-accent hover:underline">
          回到登录
        </Link>
      </div>
    </Card>
  );
}

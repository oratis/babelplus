/**
 * `/auth/login` —— P1，无替代。page-inventory §3.1 #1、§3.2.1。
 *
 * 三条错态规则（§3.2.1 表格），**都不能省**：
 *   401 → 「邮箱或密码不正确」，**不区分是哪个错**（防账号枚举）
 *   429 → 显示解锁倒计时，而不是「请稍后再试」这种没信息量的话
 *   网络不可达 → §2.2 的备用域名块（页脚已常驻，这里再给一次就近的）
 */
import { Link } from 'react-router';
import { Button, ErrorState, MirrorDomainList } from '@babelplus/shared/ui';
import { AuthCard, AuthTodo, Field } from '../../components/AuthForm.tsx';
import { useShellState } from '../../lib/shell.ts';

export default function LoginPage() {
  const { state, errorKind } = useShellState();

  return (
    <div className="space-y-4">
      <AuthCard
        title="登录"
        description="用注册时的邮箱登录。"
        footer={
          <div className="flex flex-wrap items-center justify-between gap-x-4 gap-y-2">
            <Link to="/auth/forgot" className="text-accent hover:underline">
              忘记密码
            </Link>
            <Link to="/auth/register" className="text-accent hover:underline">
              用邀请码注册
            </Link>
          </div>
        }
      >
        <Field label="邮箱" name="email" type="email" autoComplete="username" inputMode="email" placeholder="you@example.com" />
        <Field label="密码" name="password" type="password" autoComplete="current-password" />

        <label className="flex min-h-11 items-center gap-2 text-sm text-fg-muted">
          <input type="checkbox" name="remember" className="size-4 rounded border-line" />
          记住我
        </label>

        {/* TODO(P1): 提交 → `login`（POST /api/v1/auth/login）。
            401 一律显示「邮箱或密码不正确」，服务端也必须对两种情况返回同一个 code；
            429 从响应里取解锁时间做倒计时；
            成功后写 access token（sessionStorage）并按 returnTo 跳转，
            returnTo 必须校验为站内相对路径 —— 否则就是开放重定向。 */}
        <Button tone="primary" className="w-full" disabled>
          登录
        </Button>

        <AuthTodo>
          尚未接线。契约：<code className="font-mono">login</code>、
          <code className="font-mono">refreshToken</code>。
          <br />
          这一页<strong className="font-medium">没有人机验证组件，且这是设计结论不是遗漏</strong> ——
          Turnstile 大陆不可用、reCAPTCHA 依赖 google.com、hCaptcha 无实测数据（ADR 0003 §3.2、
          page-inventory §5.3）。P1 靠「IP + 账号」双维度速率限制 + 指数退避。
        </AuthTodo>
      </AuthCard>

      {state === 'error' ? (
        <ErrorState
          kind={errorKind}
          {...(errorKind === 'client'
            ? { title: '邮箱或密码不正确', description: '两者中有一个不对。为了防止账号被逐个试探，我们不说是哪一个。' }
            : {})}
        />
      ) : null}

      {/* 登录页打不开的人是最需要备用域名的人，而他们此刻还没登录，
          面板里其他任何地方都到不了 —— 所以这里额外给一次，不只靠页脚。 */}
      <MirrorDomainList />
    </div>
  );
}

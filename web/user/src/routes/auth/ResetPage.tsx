/**
 * `/auth/reset?token=` —— P1，无替代。page-inventory §3.1 #4、§3.2.1。
 *
 * 唯一的特殊要求：**token 过期时一键重新发送，不要求用户回到上一页**。
 * 用户是从邮件点进来的，把他推回 `/auth/forgot` 等于让他重走一遍已经走过的路。
 */
import { useSearchParams } from 'react-router';
import { Button, Card } from '@babelplus/shared/ui';
import { AuthCard, AuthTodo, Field } from '../../components/AuthForm.tsx';
import { useShellState } from '../../lib/shell.ts';

export default function ResetPage() {
  const [params] = useSearchParams();
  const token = params.get('token');
  const { state } = useShellState();

  if (state === 'error' || (token === null && state === 'ready')) {
    return <ExpiredPanel hasToken={token !== null} />;
  }

  return (
    <AuthCard title="设置新密码" description="设置完成后，其它设备上的登录会话会全部失效。">
      <Field label="新密码" name="password" type="password" autoComplete="new-password" />
      <Field label="再输一次" name="password_confirm" type="password" autoComplete="new-password" />

      {/* TODO(P1): 提交 → `resetPassword`（token 从 query 取）。
          成功后不要自动登录 —— 让用户用新密码登一次，确认自己确实记住了。 */}
      <Button tone="primary" className="w-full" disabled>
        设置新密码
      </Button>

      <AuthTodo>
        尚未接线。契约：<code className="font-mono">resetPassword</code>。
        <br />
        用 <code className="font-mono">?state=error</code> 预览 token 过期页。
      </AuthTodo>
    </AuthCard>
  );
}

function ExpiredPanel({ hasToken }: { hasToken: boolean }) {
  return (
    <Card>
      <h1 className="text-lg font-semibold text-fg">{hasToken ? '这个链接已经失效了' : '链接不完整'}</h1>
      <p className="mt-1.5 text-sm leading-relaxed text-fg-muted">
        {hasToken
          ? '重置链接只有 30 分钟有效期。不用回上一页重来 —— 在下面直接再要一封。'
          : '地址里少了 token 参数。多半是邮件客户端把链接截断了，复制完整链接再打开一次。'}
      </p>

      {/* 关键：在**这一页**就能重发，不把用户踢回 /auth/forgot。 */}
      <div className="mt-4 space-y-3">
        <Field label="邮箱" name="email" type="email" autoComplete="email" inputMode="email" />
        {/* TODO(P1): 重发 → `forgotPassword`。同样遵守「不泄漏邮箱是否存在」。 */}
        <Button tone="primary" className="w-full" disabled>
          重新发一封
        </Button>
      </div>
    </Card>
  );
}

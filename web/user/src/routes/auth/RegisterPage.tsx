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
 */
import { Link } from 'react-router';
import { Button } from '@babelplus/shared/ui';
import { AuthCard, AuthTodo, Field } from '../../components/AuthForm.tsx';

export default function RegisterPage() {
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
      {/* 邀请码放第一位：它是唯一一道闸，填不出来就没必要继续往下填。 */}
      <Field
        label="邀请码（必填）"
        name="invite_code"
        autoComplete="off"
        placeholder="向邀请你的人索取"
        hint="失焦时会先校验一次，避免你填完整张表才发现码不对。"
      />
      <Field label="邮箱" name="email" type="email" autoComplete="email" inputMode="email" />
      <Field
        label="密码"
        name="password"
        type="password"
        autoComplete="new-password"
        hint="找回密码依赖邮件送达，所以邮箱要选一个你确实能收信的。"
      />

      {/* TODO(P1): 密码强度条（纯前端估算，不调用任何第三方服务）。 */}
      {/* TODO(P1): 条款勾选 —— 服务条款 / 隐私 / 退款三份法务页。
          ⚠️ 退款政策目前未定（pricing-and-plans §7），page-inventory §7 把它列为**上线前置条件**，
          不是待办事项。勾选框链到一份不存在的页面比没有勾选框更糟。 */}

      {/* TODO(P1): 提交 → `sendEmailCode` → 6 位验证码页 → `registerAccount`。
          user-journey §3：**验证码通过后才真正建账号并核销邀请码**，顺序不能反。
          验证码页必须常驻「60 秒没收到？三步把发信域名加白名单」的折叠引导 ——
          QQ 邮箱白名单优先级高于黑名单与反垃圾规则，这是零成本高回报的一步。 */}
      <Button tone="primary" className="w-full" disabled>
        下一步：发送邮箱验证码
      </Button>

      <AuthTodo>
        尚未接线。契约：<code className="font-mono">verifyInviteCode</code>、
        <code className="font-mono">sendEmailCode</code>、<code className="font-mono">registerAccount</code>。
        <br />
        还缺：6 位验证码输入页（同一路由内的第二步）、密码强度条、条款勾选。
      </AuthTodo>
    </AuthCard>
  );
}

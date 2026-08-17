/**
 * `/profile/2fa` —— P3。page-inventory §3.1 #20、§3.2.9。
 * 管理员 TOTP 是 P1 强制（§4.1 三道闸之一），用户侧是**可选项**。
 */
import { Button, Card, CardTitle, EmptyState, Icon, LinkButton } from './_imports.ts';
import { LayoutSlot, RouteScaffold } from '../components/RouteScaffold.tsx';

export default function ProfileTwoFactorPage() {
  return (
    <RouteScaffold
      title="两步验证"
      description="用 TOTP 应用生成的 6 位码作为第二道验证。"
      priority="P3"
      endpoints={['enrollUserTotp', 'verifyUserTotp', 'disableUserTotp']}
      todo={
        <>
          二维码<strong className="font-medium text-fg">必须本地生成</strong>，
          绝不调用任何在线二维码服务 —— 那等于把 TOTP 密钥发给第三方，
          而且是一个大陆可达性未知的第三方主机名（ADR 0003 §5）。
        </>
      }
      empty={
        <EmptyState
          title="还没有开启两步验证"
          description="开启后，登录时除了密码还要输入验证器里的 6 位码。恢复码只会显示一次，务必抄下来。"
          action={
            <Button tone="primary" disabled>
              开始设置
            </Button>
          }
          secondary="用户侧是可选的。管理员账号则是强制开启、不接受关闭。"
        />
      }
    >
      <div className="space-y-4">
        <Card>
          <CardTitle hint="TODO(P3): enrollUserTotp / verifyUserTotp">绑定</CardTitle>
          <LayoutSlot
            label="二维码（本地渲染）+ 手动输入的 secret + 验证 6 位码"
            hint="secret 要给出可复制的明文形式 —— 有人的手机没法扫自己屏幕上的码。"
          />
        </Card>

        <Card>
          <CardTitle hint="只显示一次">恢复码</CardTitle>
          <LayoutSlot
            label="10 个一次性恢复码 + 「我已保存」确认"
            hint="没有恢复码的 2FA 会把丢手机的用户永久锁在门外，而找回流程要走人工，成本高得多。"
          />
          <LinkButton className="mt-3" href="/profile">
            回到账号设置 <Icon.ArrowRight size={14} />
          </LinkButton>
        </Card>
      </div>
    </RouteScaffold>
  );
}

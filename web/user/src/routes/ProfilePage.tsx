/**
 * `/profile` —— P1，但**大幅瘦身**。page-inventory §3.1 #12、§3.2.9。
 * 只剩三块：改密码、通知开关、Telegram 绑定（P3）。
 * 钱包搬去 `/wallet`，重置订阅搬去 `/subscribe`。
 *
 * 🔴 两条不能省的：
 *  1. **Telegram 绑定必须写明大陆不可达。** OONI 实测 `api.telegram.org` 异常 48 / 正常 0，
 *     Telegram 整体 anomaly 12,215 / ok 253 ≈ 98%。
 *     **不能让用户误以为绑了就能收到通知。**
 *  2. **「服务不可用」类通知不受开关控制**（user-journey §1 裁决 4）——
 *     邮件是唯一失联恢复通道，生命线不能被用户关掉。开关旁边要写清楚这一点。
 */
import { Link } from 'react-router';
import { Button, Card, CardTitle, EmptyState, Icon } from './_imports.ts';
import { LayoutSlot, RouteScaffold } from '../components/RouteScaffold.tsx';

export default function ProfilePage() {
  return (
    <RouteScaffold
      title="账号"
      description="密码、通知、绑定。其余的搬到了各自的页面。"
      priority="P1"
      endpoints={['getCurrentUser', 'changePassword', 'getNotificationPrefs', 'updateNotificationPrefs']}
      todo={
        <>
          改密码成功后<strong className="font-medium text-fg">其余会话全部失效</strong>（契约 204 的描述），
          所以要提前告诉用户「其它设备需要重新登录」，而不是让他事后发现。
        </>
      }
      empty={
        <EmptyState
          title="读不到账号信息"
          description="重新登录一次通常就好了。"
          action={
            <Button tone="primary" disabled>
              重新登录
            </Button>
          }
        />
      }
    >
      <div className="space-y-4">
        <Card>
          <CardTitle hint="TODO(P1): changePassword">改密码</CardTitle>
          <LayoutSlot label="旧密码 · 新密码 · 确认" hint="需要旧密码。成功后其余会话失效，提交前要明说。" />
        </Card>

        <Card>
          <CardTitle hint="TODO(P1): getNotificationPrefs / updateNotificationPrefs">通知</CardTitle>
          <LayoutSlot label="到期提醒 · 流量提醒 · 工单回复，三个独立开关" />
          <p className="mt-3 rounded-lg border border-line bg-surface-alt p-3 text-sm leading-relaxed text-fg-muted">
            服务不可用、域名变更这类通知<strong className="font-medium text-fg">不受这些开关控制</strong>。
            邮件是我们和你之间唯一还能用的通道，这条生命线不提供关闭选项。
          </p>
        </Card>

        <Card>
          <CardTitle hint="P3">Telegram 绑定</CardTitle>
          {/* 🔴 这段警告不是可选的。 */}
          <div className="rounded-lg border border-danger/30 bg-danger/10 p-3 text-sm leading-relaxed text-danger">
            <p className="font-medium">Telegram 在中国大陆基本不可用。</p>
            <p className="mt-1">
              实测数据里 Telegram 的异常率约 98%。绑定了也大概率收不到通知 ——
              如果你人在大陆，请把邮箱当成唯一的通知渠道。
            </p>
          </div>
          <Button className="mt-3" disabled>
            绑定 Telegram（P3）
          </Button>
        </Card>

        <Card>
          <CardTitle hint="P3">两步验证</CardTitle>
          <p className="text-sm text-fg-muted">
            用户侧 TOTP 是可选项（管理员侧是强制的）。
          </p>
          <Link to="/profile/2fa" className="mt-3 inline-flex items-center gap-1 text-sm text-accent hover:underline">
            去设置 <Icon.ArrowRight size={14} />
          </Link>
        </Card>
      </div>
    </RouteScaffold>
  );
}

/**
 * 模块 9 · 邀请与返佣 `/admin/invites` —— P1 / M3。
 * 邀请制注册 → **发码是 P1 能力**，没有它连第一个用户都进不来。
 */
import { Card, CardTitle, EmptyState, Button } from './_imports.ts';
import { LayoutSlot, ModuleScaffold } from '../components/ModuleScaffold.tsx';

export default function InvitesPage() {
  return (
    <ModuleScaffold
      title="邀请与返佣"
      description="种子码批量生成、邀请关系树、佣金记录。"
      priority="P1"
      mobile="M3"
      endpoints={['listAdminInvites', 'createAdminInvite', 'adjustAdminCommission']}
      danger={['D11']}
      todo={
        <>
          种子码可配置 1–N 次并带总量上限；用户码<strong className="font-medium text-fg">恒为一次性</strong>。
          两者在列表里要能一眼分辨 —— 一个能用 50 次的码和一个只能用 1 次的码，
          泄漏后的后果差 50 倍。
        </>
      }
      empty={
        <EmptyState
          title="还没有邀请码"
          description="冷启动要先在这里批量发一组种子码。用户自助生成的码是常态增长路径，那是 P2。"
          action={
            <Button tone="primary" disabled>
              批量生成种子码
            </Button>
          }
        />
      }
    >
      <div className="space-y-4">
        <Card>
          <CardTitle hint="TODO(P1): listAdminInvites">邀请码</CardTitle>
          <LayoutSlot label="码 · 类型（种子 / 用户）· 可用次数 / 已用 · 有效期 · 归属 · 状态" />
        </Card>
        <Card>
          <CardTitle>邀请关系与佣金</CardTitle>
          <LayoutSlot
            label="关系树 · 佣金记录（确认中 / 已获得）"
            hint="两段式是退款冷静期的防套利设计。手工调整佣金是 D11，走确认 + 必填原因 + 审计。"
          />
        </Card>
      </div>
    </ModuleScaffold>
  );
}

/** 模块 16 · 系统配置 `/admin/settings` —— P2 / M3。KV 表 + 热生效。 */
import { Card, CardTitle, EmptyState, Icon, LinkButton } from './_imports.ts';
import { LayoutSlot, ModuleScaffold } from '../components/ModuleScaffold.tsx';

export default function SettingsPage() {
  return (
    <ModuleScaffold
      title="系统配置"
      description="注册开关、邀请策略、SLA、通知开关。改动全局立即生效。"
      priority="P2"
      mobile="M3"
      endpoints={['getAdminSettings', 'updateAdminSettings']}
      danger={['D13']}
      todo={
        <>
          保存前<strong className="font-medium text-fg">展示 diff</strong>（D13 的硬要求）。
          KV 配置最容易出的事故是「改了一个不知道会影响什么的键」，
          diff 至少能让人看见自己到底动了几个键。
        </>
      }
      empty={
        <EmptyState
          title="配置表是空的"
          description="所有键都在用代码里的默认值。这在首次部署时是正常的。"
          action={
            <LinkButton tone="primary" href="/admin">
              回到看板 <Icon.ArrowRight size={14} />
            </LinkButton>
          }
        />
      }
    >
      <Card>
        <CardTitle hint="TODO(P2): getAdminSettings">配置项</CardTitle>
        <LayoutSlot
          label="按分组折叠的 KV 编辑器 + 每项的默认值与当前值对照"
          hint="显示「当前值 = 默认值」还是「被覆盖过」很有用 —— 排障时最常问的是「这个值是谁改的」。"
        />
      </Card>
    </ModuleScaffold>
  );
}

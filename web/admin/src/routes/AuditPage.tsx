/**
 * 模块 10 · 审计日志 `/admin/audit` —— P1 / M3。
 *
 * 🔴 **append-only，无删除入口，无编辑入口。** 后台前端不提供，API 也不提供。
 * 「一个能被清理的审计日志等于没有审计日志。」
 *
 * 所以这一页是全后台唯一一个**没有任何写操作**的模块。
 * 如果将来有人在这里加了一个「清理 90 天前的日志」按钮，那就是这条纪律被破坏的时刻 ——
 * 留存策略应该在数据库侧做，且要有独立于后台的审批。
 */
import { Card, CardTitle, EmptyState, Icon, LinkButton } from './_imports.ts';
import { LayoutSlot, ModuleScaffold } from '../components/ModuleScaffold.tsx';

export default function AuditPage() {
  return (
    <ModuleScaffold
      title="审计日志"
      description="全部管理操作的流水。只读，没有删除入口，也没有编辑入口。"
      priority="P1"
      mobile="M3"
      endpoints={['listAdminAuditLog']}
      todo={
        <>
          筛选要支持「按管理员」「按动作」「按目标对象」三个维度 ——
          复盘时问的通常是「谁动过这个用户」，而不是「昨天都发生了什么」。
        </>
      }
      empty={
        <EmptyState
          title="还没有任何管理操作"
          description="全新部署时这是对的。一旦有人改过任何东西，这里就不会再是空的。"
          action={
            <LinkButton tone="primary" href="/admin">
              回到看板 <Icon.ArrowRight size={14} />
            </LinkButton>
          }
        />
      }
    >
      <div className="space-y-4">
        <Card>
          <CardTitle hint="TODO(P1): listAdminAuditLog">操作流水</CardTitle>
          <LayoutSlot
            label="时间 · 管理员 · IP · 动作 · 目标 · 改前值 / 改后值 · 原因"
            hint="改前值 / 改后值是这张表的价值所在。只记「谁改了什么字段」而不记值，复盘时等于没记。"
          />
        </Card>

        <Card>
          <p className="text-sm leading-relaxed text-fg-muted">
            这一页<strong className="font-medium text-fg">刻意没有任何按钮</strong>。
            没有删除、没有导出、没有批量操作。
            如果有一天这里出现了写操作，请先回去读一遍 page-inventory §4.4 的末尾那句话。
          </p>
        </Card>
      </div>
    </ModuleScaffold>
  );
}

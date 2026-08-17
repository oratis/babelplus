/** 模块 8 · 工单处理 `/admin/tickets` —— P1 / **M2**（手机上要能回工单）。 */
import { Card, CardTitle, EmptyState, Icon, LinkButton } from './_imports.ts';
import { LayoutSlot, ModuleScaffold } from '../components/ModuleScaffold.tsx';

export default function TicketsPage() {
  return (
    <ModuleScaffold
      title="工单"
      description="按状态 + 优先级 + SLA 排序的队列。手机上要能完成回复。"
      priority="P1"
      mobile="M2"
      endpoints={['listAdminTickets']}
      todo={
        <>
          默认排序是 <strong className="font-medium text-fg">SLA 剩余时间升序</strong>，不是创建时间倒序 ——
          队列的意义在于告诉你「先处理哪一个」，按时间排等于没排。
        </>
      }
      empty={
        <EmptyState
          title="队列是空的"
          description="没有待处理的工单。如果这和你的预期不符，先确认工单入口在用户面板上是可达的。"
          action={
            <LinkButton tone="primary" href="/admin">
              回到看板 <Icon.ArrowRight size={14} />
            </LinkButton>
          }
        />
      }
    >
      <Card>
        <CardTitle hint="TODO(P1): listAdminTickets">队列</CardTitle>
        <LayoutSlot
          label="public_id · 用户 · 分类 · 优先级 · 状态 · SLA 剩余 · 最后回复"
          hint="7 列，<768px 卡片化（M2）。分类是用户在建单时选的，它同时决定了当时弹给用户看的是哪一篇排障文档。"
        />
      </Card>
    </ModuleScaffold>
  );
}

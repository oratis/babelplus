/** 模块 3 · 订单管理 `/admin/orders` —— P1 / **M2**（涉资金，手机上要能查单）。 */
import { Card, CardTitle, EmptyState, Icon, LinkButton } from './_imports.ts';
import { LayoutSlot, ModuleScaffold } from '../components/ModuleScaffold.tsx';

export default function OrdersPage() {
  return (
    <ModuleScaffold
      title="订单"
      description="涉资金。查单、对账、处理 underpaid 都从这里开始。"
      priority="P1"
      mobile="M2"
      endpoints={['listAdminOrders']}
      todo={
        <>
          M2：<strong className="font-medium text-fg">6 列表格在 &lt;768px 必须卡片化</strong>。
          用户在电话里念订单号的时候，运维多半正拿着手机。
        </>
      }
      empty={
        <EmptyState
          title="还没有订单"
          description="第一笔订单出现后，这里会同时显示待确认与已完成的。"
          action={
            <LinkButton tone="primary" href="/admin/plans">
              先确认套餐已上架 <Icon.ArrowRight size={14} />
            </LinkButton>
          }
        />
      }
    >
      <Card>
        <CardTitle hint="TODO(P1): listAdminOrders">订单列表</CardTitle>
        <LayoutSlot
          label="trade_no · 用户 · 类型 · 金额 · 状态 · 渠道 · 时间"
          hint="状态筛选里 underpaid 必须是一个独立的快捷入口 —— 它是「金额唯一性匹配」这个设计的必然产物，会持续出现。"
        />
      </Card>
    </ModuleScaffold>
  );
}

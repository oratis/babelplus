/** 模块 4 · 套餐管理 `/admin/plans` —— P1 / M3。 */
import { Card, CardTitle, EmptyState, Button } from './_imports.ts';
import { LayoutSlot, ModuleScaffold } from '../components/ModuleScaffold.tsx';

export default function PlansPage() {
  return (
    <ModuleScaffold
      title="套餐"
      description="价格、流量、设备数、上下架。"
      priority="P1"
      mobile="M3"
      endpoints={['listAdminPlans', 'createAdminPlan', 'updateAdminPlan', 'deleteAdminPlan']}
      danger={['D8']}
      todo={
        <>
          🔴 <strong className="font-medium text-fg">已生成订单的价格快照不可变。</strong>
          改价只影响之后的新订单；待支付订单必须按下单时锁定的价格结算，
          否则用户会在收银台上看到金额突然变了 —— 而 USDT 支付靠金额唯一性匹配，
          金额一变，已经付出去的那笔就对不上任何订单了。
        </>
      }
      empty={
        <EmptyState
          title="还没有套餐"
          description="至少要有一个上架的套餐，用户面板的 /plan 才不是空的。"
          action={
            <Button tone="primary" disabled>
              新建套餐
            </Button>
          }
        />
      }
    >
      <Card>
        <CardTitle hint="TODO(P1): listAdminPlans">套餐列表</CardTitle>
        <LayoutSlot
          label="名称 · 类型（周期 / 流量包）· 各周期价格 · 流量 · 设备数 · 上下架"
          hint="设备数是我们的差异化杠杆（2/5/10），也是工单发生器。改这一列之前先看 page-inventory §7 代价 3。"
        />
      </Card>
    </ModuleScaffold>
  );
}

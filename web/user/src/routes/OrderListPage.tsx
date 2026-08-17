/**
 * `/order` —— P1。page-inventory §3.1 #8、§3.2.5。
 * 移动端必须卡片化：订单是 §2.3 点名的三个「重灾区」之一。
 */
import { Card, CardTitle, EmptyState, Icon, LinkButton } from './_imports.ts';
import { LayoutSlot, RouteScaffold } from '../components/RouteScaffold.tsx';

export default function OrderListPage() {
  return (
    <RouteScaffold
      title="订单"
      description="新购、续费、升级的记录都在这里。"
      priority="P1"
      endpoints={['listOrders', 'cancelOrder']}
      todo={
        <>
          列表有 5 列（订单号 / 类型 / 金额 / 状态 / 时间），
          <strong className="font-medium text-fg">&lt;768px 必须转卡片列表</strong>，
          不允许横向滚动表格（§2.3 M1 硬规则，订单是点名的重灾区之一）。
        </>
      }
      empty={
        <EmptyState
          title="还没有订单"
          description="下单后这里会出现记录，包括正在等链上确认的那些。"
          action={
            <LinkButton tone="primary" href="/plan">
              去看套餐 <Icon.ArrowRight size={14} />
            </LinkButton>
          }
        />
      }
    >
      <Card>
        <CardTitle hint="TODO(P1): listOrders（游标分页）">订单列表</CardTitle>
        <LayoutSlot
          label="订单号 · 类型（新购/续费/升级）· 金额 · 状态 · 时间 · 操作"
          hint="状态里 underpaid 要单独可见 —— 少付一定会发生（金额唯一性匹配的必然结果）。"
        />
        {/* TODO(P1): 游标分页。契约用 `?limit=20&cursor=`，`meta.has_more` 判断有没有下一页。
            用户面**不返回 total**，所以不要做「共 N 条 / 第 x 页」的分页器，只能做「加载更多」。 */}
      </Card>
    </RouteScaffold>
  );
}

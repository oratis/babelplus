/**
 * 模块 14 · 支付与对账 `/admin/payments` —— P2 / M3。
 *
 * `underpaid` 列表是这一页的主角，不是附属功能：
 * 我们用「小地址池 + 金额唯一性」匹配订单，**少付一定会发生**，这是设计的必然产物而不是异常。
 * 有一个专门的队列去处理它，比每次从订单列表里翻要现实得多。
 */
import { Card, CardTitle, EmptyState, Icon, LinkButton } from './_imports.ts';
import { LayoutSlot, ModuleScaffold } from '../components/ModuleScaffold.tsx';

export default function PaymentsPage() {
  return (
    <ModuleScaffold
      title="支付与对账"
      description="通道状态、地址池、待确认、underpaid 队列。"
      priority="P2"
      mobile="M3"
      endpoints={['listAdminPayments', 'updateAdminPayment', 'listAdminUnderpaidPayments']}
      danger={['D13']}
      todo={
        <>
          金额一律用 <code className="font-mono">1e-6 USDT</code> 的整数比较，
          <strong className="font-medium text-fg">不许在任何环节转成浮点</strong> ——
          「金额唯一性匹配」这个机制的整个前提就是金额精确可比。
        </>
      }
      empty={
        <EmptyState
          title="没有待处理的支付"
          description="没有待确认的交易，也没有 underpaid。"
          action={
            <LinkButton tone="primary" href="/admin/orders">
              去看订单 <Icon.ArrowRight size={14} />
            </LinkButton>
          }
        />
      }
    >
      <div className="space-y-4">
        <Card>
          <CardTitle hint="这一页的主角">underpaid 队列</CardTitle>
          <LayoutSlot
            label="订单 · 应付 · 实收 · 差额 · 时间 · 处理动作"
            hint="处理动作至少要有：按实收金额部分开通 / 要求补款 / 全额退回。三选一，不要只留「手工标记已支付」这一条路（那是 D6）。"
          />
        </Card>
        <Card>
          <CardTitle>通道与地址池</CardTitle>
          <LayoutSlot label="通道开关 · 地址池余额 · 链上查单入口" />
        </Card>
      </div>
    </ModuleScaffold>
  );
}

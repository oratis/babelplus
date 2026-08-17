/**
 * 模块 3 · 订单详情 `/admin/orders/:trade_no` —— P1 / M2。
 *
 * 🔴 **D6「手工标记订单已支付」是全系统最大的内部欺诈面。**
 * 它需要：🔒 输入确认串 + 必填原因 + 审计 + **独立权限位（默认不授予）**。
 * 独立权限位这一条不能因为「现在只有一个管理员」而省略 ——
 * page-inventory §8 明写「**D6 那个权限位必须从第一天就存在**」。
 */
import { useParams } from 'react-router';
import { Card, CardTitle, EmptyState, Icon, LinkButton } from './_imports.ts';
import { LayoutSlot, ModuleScaffold } from '../components/ModuleScaffold.tsx';

export default function OrderDetailPage() {
  const { trade_no: tradeNo } = useParams();

  return (
    <ModuleScaffold
      title="订单详情"
      description={
        <>
          <code className="font-mono text-fg">{tradeNo ?? '—'}</code>
        </>
      }
      priority="P1"
      mobile="M2"
      endpoints={['getAdminOrder', 'markAdminOrderPaid', 'refundAdminOrder']}
      danger={['D6', 'D7']}
      todo={
        <>
          先看「支付回调原文 + 链上 txid」再决定要不要手工标记 ——
          <strong className="font-medium text-fg">回调不可信</strong>，
          能查链就不要凭回调下结论（NewAPI 的易支付回调漏洞是先例）。
        </>
      }
      empty={
        <EmptyState
          title="找不到这个订单"
          description="订单号可能不对。"
          action={
            <LinkButton tone="primary" href="/admin/orders">
              回到订单列表 <Icon.ArrowRight size={14} />
            </LinkButton>
          }
        />
      }
    >
      <div className="space-y-4">
        <Card>
          <CardTitle hint="TODO(P1): getAdminOrder">明细</CardTitle>
          <LayoutSlot label="金额构成 · 折抵 · 优惠码 · 状态机历史" />
        </Card>
        <Card>
          <CardTitle>支付证据</CardTitle>
          <LayoutSlot
            label="支付回调原文（原样展示，不加工）· 链上 txid · 实收金额"
            hint="原样展示很重要：加工过的展示会把「回调声称的金额」和「链上实际到账」混成一个数字，而这两者不一致正是要查的东西。"
          />
        </Card>
      </div>
    </ModuleScaffold>
  );
}

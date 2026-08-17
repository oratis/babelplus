/**
 * `/order/:trade_no` —— P1，**无替代（付款中断 = 丢单）**。page-inventory §3.1 #9、§3.2.5。
 *
 * 🔴 **收银台必须独立成页，不能做成弹窗**（竞品是弹窗）。
 * 理由不是审美：链上确认有分钟级延迟，用户**必须能关掉页面再回来**，
 * 而弹窗的状态活不过一次刷新。这条决定了它是一条路由而不是一个组件。
 *
 * USDT 收银台的五个元素（§3.2.5 表格）一个都不能少，尤其是最后两个：
 *  - `underpaid` 的显式界面：「已收到 X，还差 Y」，不是笼统的「支付失败」
 *  - 「我已付款，帮我查一下」按钮：**回调不可信**（NewAPI 的易支付回调漏洞是先例），
 *    这个按钮是用户侧的最后防线
 */
import { useParams } from 'react-router';
import {
  Badge,
  Button,
  Card,
  CardTitle,
  EmptyState,
  ErrorState,
  Icon,
  LinkButton,
} from './_imports.ts';
import { LayoutSlot, RouteScaffold } from '../components/RouteScaffold.tsx';

export default function OrderDetailPage() {
  const { trade_no: tradeNo } = useParams();

  return (
    <RouteScaffold
      title="订单详情"
      description={
        <>
          订单号 <code className="font-mono text-fg">{tradeNo ?? '—'}</code>
        </>
      }
      priority="P1"
      endpoints={['getOrder', 'payOrder', 'getOrderPayment', 'recheckOrderPayment', 'cancelOrder']}
      todo={
        <>
          轮询 <code className="font-mono">getOrderPayment</code> 时必须
          <strong className="font-medium text-fg">带指数退避并在页面隐藏时暂停</strong>；
          链上确认是分钟级的，固定 1 秒轮询会在移动网络下白白烧用户的流量和电。
        </>
      }
      empty={
        <EmptyState
          title="找不到这个订单"
          description="订单号可能输错了，或者这个订单不属于当前账号。"
          action={
            <LinkButton tone="primary" href="/order">
              回到订单列表 <Icon.ArrowRight size={14} />
            </LinkButton>
          }
        />
      }
      error={
        <ErrorState
          kind="server"
          title="读不到这个订单"
          description="如果你刚刚已经付过款，别重复付。用下面的「我已付款，帮我查一下」触发一次主动查单。"
        />
      }
    >
      <div className="space-y-4">
        <Card>
          <CardTitle hint="TODO(P1): getOrder">金额构成</CardTitle>
          <LayoutSlot
            label="套餐 · 周期 · 折抵金额 · 优惠码 · 应付"
            hint="金额单位是「分」的 int64（契约约定），前端任何地方都不得用浮点算钱。"
          />
        </Card>

        <Card>
          <CardTitle hint="TODO(P1): payOrder / getOrderPayment">USDT 收银台</CardTitle>

          <div className="space-y-3">
            <LayoutSlot
              label="收款地址 + 二维码 + 精确到分位的唯一金额"
              hint="我们用「小地址池 + 金额唯一性」匹配订单，不做一单一地址。所以金额必须一分不差 —— 页面上要把这句话说出来。"
            />

            <div className="flex flex-wrap items-center gap-2">
              <Badge tone="warn">汇率锁定倒计时</Badge>
              <Badge>TRC20</Badge>
              <Badge>ERC20</Badge>
              <Badge>BEP20</Badge>
            </div>
            <p className="text-sm text-fg-muted">
              从交易所提币请选 <strong className="font-medium text-fg">TRC20</strong> ——
              手上的 U 基本都在 TRC20，且交易所代付能量，你不用自己准备。
            </p>

            {/* underpaid 的显式界面。少付一定会发生，笼统地说「支付失败」会直接变成工单。 */}
            <div className="rounded-lg border border-warn/30 bg-warn/10 p-3 text-sm text-warn">
              <p className="font-medium">underpaid 状态长这样：</p>
              <p className="mt-1">「已收到 X USDT，还差 Y USDT」+ 补款地址与金额。不要显示成「支付失败」。</p>
            </div>

            <LayoutSlot
              label="「已付款，等待确认」轮询区"
              hint="进度条 + 预期确认时间 + 「可以关闭此页，到账后会发邮件」。这句话是这一页存在的理由之一。"
            />

            {/* 🔴 回调不可信。这个按钮是用户侧的最后防线。 */}
            <Button tone="primary" disabled>
              我已付款，帮我查一下
            </Button>
            <p className="text-xs leading-relaxed text-fg-subtle">
              触发 <code className="font-mono">recheckOrderPayment</code> 主动查链，不依赖支付回调。
              回调可能丢、可能被伪造（NewAPI 的易支付回调漏洞是先例），所以这条路必须一直存在。
            </p>
          </div>
        </Card>
      </div>
    </RouteScaffold>
  );
}

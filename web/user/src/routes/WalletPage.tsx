/**
 * `/wallet` —— P2。page-inventory §3.1 #14、§3.2.9。从竞品的 profile 里拆出来。
 *
 * 🔴 「余额仅可消费，不可提现」是**资金合规底线**（product-brief §6），
 * 必须**常驻**而不是藏在条款里。放在页面顶部，不折叠。
 */
import { Card, CardTitle, EmptyState, Icon, LinkButton } from './_imports.ts';
import { LayoutSlot, RouteScaffold } from '../components/RouteScaffold.tsx';

export default function WalletPage() {
  return (
    <RouteScaffold
      title="钱包"
      description="余额和流水。"
      priority="P2"
      endpoints={['getWallet', 'listWalletTransactions']}
      todo={<>金额单位是「分」的 int64，展示走 <code className="font-mono">formatCny</code>，不许用浮点。</>}
      empty={
        <EmptyState
          title="还没有余额记录"
          description="邀请返佣划转、订单折抵都会在这里留下流水。"
          action={
            <LinkButton tone="primary" href="/invite">
              看看邀请返佣 <Icon.ArrowRight size={14} />
            </LinkButton>
          }
        />
      }
    >
      <div className="space-y-4">
        {/* 常驻声明，不折叠、不藏。 */}
        <Card>
          <p className="text-base font-medium text-fg">余额只能用于消费，不能提现。</p>
          <p className="mt-1.5 text-sm leading-relaxed text-fg-muted">
            充值和返佣划入的余额只能用来买套餐或流量包，不支持退回到原支付方式，也不支持转出。
          </p>
        </Card>

        <Card>
          <CardTitle hint="TODO(P2): getWallet">余额</CardTitle>
          <LayoutSlot label="可用余额 · 最近一次变动" />
        </Card>

        <Card>
          <CardTitle hint="TODO(P2): listWalletTransactions">流水</CardTitle>
          <LayoutSlot
            label="类型（充值 / 消费 / 佣金划入）· 金额 · 时间 · 关联订单"
            hint="4 列，<768px 卡片化。"
          />
        </Card>
      </div>
    </RouteScaffold>
  );
}

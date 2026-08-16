/**
 * `/plan` —— P1。page-inventory §3.1 #7、§3.2.4。
 *
 * 相对竞品的四处改动，每一处都有理由（§3.2.4 表格）：
 *  + 加长周期梯度折扣（竞品长周期零折扣，用户没有理由预付；折扣同时摊薄 USDT 归集成本）
 *  + 设备数加粗做主卖点（竞品三档统一为 3，未作杠杆）
 *  − 删除「峰值带宽 500/1000 Mbps」分档（真实瓶颈在境内纵深，这个数字是营销噪音）
 *  + 加「不承诺流媒体解锁」（GCP IP 段普遍被封，做不到就不能写）
 *
 * ⚠️ 价格数字一律不写死在前端。pricing-and-plans §7 的定价还是 P0 阻塞项。
 */
import {
  Card,
  CardTitle,
  EmptyState,
  ErrorState,
  Icon,
  LinkButton,
  runtimeConfig,
} from './_imports.ts';
import { LayoutSlot, RouteScaffold } from '../components/RouteScaffold.tsx';

export default function PlanPage() {
  const cfg = runtimeConfig();

  return (
    <RouteScaffold
      title="套餐"
      description="按周期买，或者只买一个流量包。"
      priority="P1"
      endpoints={['listPlans', 'createOrder', 'verifyCoupon']}
      todo={
        <>
          价格数字<strong className="font-medium text-fg">全部来自 API</strong>，前端不缓存、不硬编码 ——
          pricing-and-plans §7 的定价仍是 P0 阻塞项，任何写死的数字都会变成错的数字。
        </>
      }
      empty={
        <EmptyState
          title="暂时没有开放的套餐"
          description="现在没有可以下单的套餐。如果你正等着续费，提个工单我们直接处理。"
          action={
            <LinkButton tone="primary" href="/ticket">
              提交工单 <Icon.ArrowRight size={14} />
            </LinkButton>
          }
        />
      }
      error={
        <ErrorState
          kind="server"
          title="读不到套餐列表"
          description="公开站上有一份静态的价格页，API 挂了它仍然可读。"
          extra={
            cfg.docsUrl ? (
              <LinkButton href={cfg.docsUrl} external>
                看静态价格页 <Icon.External size={14} />
              </LinkButton>
            ) : null
          }
        />
      }
    >
      <div className="space-y-4">
        <Card>
          <CardTitle hint="照抄竞品的信息架构，这部分它做对了">按周期 / 按流量包</CardTitle>
          <LayoutSlot
            label="两个 tab + 套餐卡网格"
            hint="卡片字段：名称 · 流量/30 天 · 设备数（加粗，这是我们的差异化杠杆）· 价格 · 周期折扣角标。"
          />
        </Card>

        <Card>
          <CardTitle hint="竞品季付 = 3×月付、半年付 = 6×月付，长周期零折扣">周期选择</CardTitle>
          <LayoutSlot
            label="月 / 季 / 半年 / 年 —— 折扣率从 API 读"
            hint="梯度折扣同时解决两件事：给用户预付的理由，以及摊薄 USDT 归集成本（一笔 TRC20 归集对月付订单是两位数百分比的侵蚀）。"
          />
        </Card>

        {/* 🔴 诚实声明。§3.2.4 要求「与卖点同等字号，不放在折叠区」。
            GCP IP 段普遍被主流流媒体平台封禁，这是结构性劣势，只能诚实标注。 */}
        <Card>
          <p className="text-base font-medium text-fg">我们不承诺流媒体解锁。</p>
          <p className="mt-1.5 text-sm leading-relaxed text-fg-muted">
            出口是 GCP 的 IP 段，主流流媒体平台普遍会拦。如果你的主要用途是看剧，这个服务大概率不适合你 ——
            现在说清楚，比你付完钱再发现好。
          </p>
        </Card>

        {/* TODO(P1): 「删除峰值带宽分档」这条是**不做**的事，代码里不应出现带宽字段的展示位。
            如果 API 将来加了这个字段，前端也不显示 —— 跨境链路的真实瓶颈在中国境内纵深，
            这个数字对用户没有意义。 */}
      </div>
    </RouteScaffold>
  );
}

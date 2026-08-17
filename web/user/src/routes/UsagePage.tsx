/**
 * `/usage` —— P2。page-inventory §3.1 #13、§3.2.7。竞品完全没有流量趋势图，这是最便宜的差异化。
 *
 * 为什么是 P2 而不是 P1：P1 上线那天 `stat_user` 里一行数据都没有，图表页是空的。
 * 先上 dashboard 的总量进度条，积累 30 天数据后再上曲线页。
 *
 * ⚠️ 「按节点分布」需要一张现有数据模型里**没有**的表：`stat_user_server(user_id, server_id, date, u, d)`。
 * 量级约 3 万行/月（100 用户 × 10 节点 × 30 天），对 PostgreSQL 是噪音级。
 * 但它是净增加，不能等写前端时才发现没数据。
 */
import { Card, CardTitle, EmptyState, ErrorState, Icon, LinkButton } from './_imports.ts';
import { LayoutSlot, RouteScaffold } from '../components/RouteScaffold.tsx';

export default function UsagePage() {
  return (
    <RouteScaffold
      title="用量"
      description="流量花在哪、还够用多久。"
      priority="P2"
      endpoints={['getUserUsage', 'getUserSubscription', 'listSubscriptionFetchLog']}
      todo={
        <>
          图表<strong className="font-medium text-fg">不要从 0 动画增长到实际值</strong> ——
          在慢网络下会被误读成数据错误（§3.2.7 加载态）。渲染即最终值。
        </>
      }
      empty={
        // §3.2.7 原话：「这是最需要认真做的空态」。
        <EmptyState
          title="用满一天后这里会出现流量曲线"
          description="日曲线按天聚合，所以新账号至少要过一天才有第一个点。现在能看的是实时累计值。"
          action={
            <LinkButton tone="primary" href="/dashboard">
              先看当前用量 <Icon.ArrowRight size={14} />
            </LinkButton>
          }
          secondary="不显示空白图表 —— 一张全是零的柱状图看起来像坏了。"
        />
      }
      error={
        <ErrorState
          kind="server"
          title="聚合数据读不出来"
          description="曲线暂时不可用。概览页上的总量进度条走的是另一个数据源，通常更可靠。"
          extra={<LinkButton href="/dashboard">去看总量</LinkButton>}
        />
      }
    >
      <div className="space-y-4">
        <Card>
          <CardTitle hint="TODO(P2): getUserUsage?range=30d">日流量</CardTitle>
          <LayoutSlot label="近 30 天柱状图，上传 / 下载分色" hint="数据源 stat_user 日聚合。" />
        </Card>

        <Card>
          <CardTitle hint="TODO(P2): getUserSubscription">周期进度</CardTitle>
          <LayoutSlot
            label="已用 / 配额 · 距重置 N 天 · 按当前速率的耗尽预测"
            hint="重置日 = 订单日，这里要再说一次（tutorials-spec §5 记录这是高频误解）。"
          />
        </Card>

        <Card>
          <CardTitle hint="⚠️ 需要新表 stat_user_server">按节点分布</CardTitle>
          <LayoutSlot
            label="环图 + 列表"
            hint="现有聚合口径只有 stat_user（用户维度）与 stat_server（节点维度），没有 user × server 交叉维度。这张图上线前必须先加表。"
          />
        </Card>

        <Card>
          <CardTitle hint="TODO(P2): listSubscriptionFetchLog">订阅拉取审计</CardTitle>
          <LayoutSlot
            label="最近 10 次：时间 / IP 归属地 / UA"
            hint="展示给用户的边际成本为零（表本来就要建），价值很高 —— 用户自己就能发现订阅被白嫖。"
          />
        </Card>
      </div>
    </RouteScaffold>
  );
}

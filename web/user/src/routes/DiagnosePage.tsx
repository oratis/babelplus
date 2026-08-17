/**
 * `/diagnose` —— P2。page-inventory §3.1 #18、§3.2.8。
 *
 * 它和 `check.*` 的分工必须先说清楚，否则会做重：
 *   `/diagnose`（面板域名，要登录）回答「**我的账号**有没有问题」
 *   `check.*`（公开域名，免登录）回答「**我的网络**有没有走代理、走的是谁」
 * 用户连不上时通常打不开面板 —— 所以 `check.*` 是排障主入口，这一页是它的补充。
 *
 * 🔴 一条容易做错的三态规则：某项检查**本身失败**时显示灰色「检测不可用」，
 * **不能显示成红色失败** —— 检测失败和检测到失败是两回事。
 */
import { Card, CardTitle, EmptyState, Icon, LinkButton, Badge, runtimeConfig, Button } from './_imports.ts';
import { LayoutSlot, RouteScaffold } from '../components/RouteScaffold.tsx';

const CHECKS: ReadonlyArray<{ name: string; basis: string; action: string }> = [
  { name: '账号状态', basis: 'banned = false', action: '→ 提工单' },
  { name: '订阅有效期', basis: 'expired_at 未过', action: '→ /plan 续费' },
  { name: '流量余额', basis: 'u + d < transfer_enable', action: '→ 买流量包' },
  { name: '设备数', basis: '在线数 ≤ device_limit', action: '→ /subscribe 踢下线' },
  { name: '订阅可拉取', basis: '服务端自测：拉一次自己的订阅，看 HTTP 状态与字节数', action: '→ 订阅类排障文档' },
  { name: '最近拉取记录', basis: '有无 / 时间 / IP', action: '→ 陌生 IP 就引导重置 token' },
  { name: '节点健康', basis: '各节点最后上报时间', action: '→ 节点侧异常时显示「已知问题」' },
];

export default function DiagnosePage() {
  const cfg = runtimeConfig();

  return (
    <RouteScaffold
      title="自助诊断"
      description="先看看是不是账号这边的问题。前四项在客户端里一个都看不到 —— 这一页的价值就在这里。"
      priority="P2"
      endpoints={['getUserDiagnose']}
      todo={
        <>
          结果要<strong className="font-medium text-fg">逐项流式出</strong>，不等全部完成（各项互相独立）。
          某项检查本身失败时显示灰色「检测不可用」，
          <strong className="font-medium text-fg">不能显示成红色失败</strong>。
        </>
      }
      empty={
        <EmptyState
          title="还没跑过检查"
          description="点一下，我们会逐项检查你的账号状态。全程只读，不会改动任何东西。"
          action={
            <Button tone="primary" disabled>
              开始检查
            </Button>
          }
        />
      }
    >
      <div className="space-y-4">
        {/* 先把 check.* 推出去：用户连不上时打不开这一页，那才是主入口。 */}
        <Card>
          <p className="text-sm leading-relaxed text-fg-muted">
            如果你现在<strong className="font-medium text-fg">完全连不上</strong>，
            要看的是免登录的网络诊断页，而不是这里 —— 这一页只能告诉你账号有没有问题。
          </p>
          <div className="mt-3">
            {cfg.checkUrl ? (
              <LinkButton href={cfg.checkUrl} external>
                打开网络诊断 <Icon.External size={14} />
              </LinkButton>
            ) : (
              <Button disabled title="checkUrl 未配置">
                打开网络诊断（未配置）
              </Button>
            )}
          </div>
        </Card>

        <Card>
          <CardTitle hint="TODO(P2): getUserDiagnose">检查项</CardTitle>
          <ul className="divide-y divide-line">
            {CHECKS.map((c) => (
              <li key={c.name} className="flex flex-wrap items-baseline gap-x-3 gap-y-1 py-2.5">
                <Badge>待检测</Badge>
                <span className="text-sm font-medium text-fg">{c.name}</span>
                <span className="text-xs text-fg-muted">{c.basis}</span>
                <span className="ml-auto text-xs text-fg-subtle">{c.action}</span>
              </li>
            ))}
          </ul>
          <p className="mt-3 text-xs leading-relaxed text-fg-subtle">
            每项一行，红 / 黄 / 绿三态 + 一句人话解释 + 一个动作按钮。灰色第四态专门留给「检测不可用」。
          </p>
        </Card>

        <Card>
          <CardTitle hint="提工单时自动填入 tickets.context">诊断码</CardTitle>
          <LayoutSlot
            label="一键生成，包含上述全部结果"
            hint="用户不用自己描述环境，客服也不用来回问。这是把诊断页接到工单系统上的那根线。"
          />
        </Card>
      </div>
    </RouteScaffold>
  );
}

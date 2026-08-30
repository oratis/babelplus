/**
 * 模块 1 · 运营看板 `/admin` —— P1 / M3。page-inventory §4.2 / §4.3。
 *
 * 🔴 **这一页最要紧的一条不是「显示五个数字」，是「一个数字取不到时另外四个照常显示」。**
 *
 * 服务端 `loadAdminDashboard`（`handler/admin_catalog.go`）把五组查询**并发**跑，
 * 并发度钉在 2（ADR 0005：每实例连接池 max=2），**任一组失败只是让那一组的字段缺席**
 * （契约里 `AdminDashboard` 的字段全是可选的），整页仍然回 200。
 * 前端必须对得上这个语义：
 *
 *  - 缺字段 → 渲染 `—`，并说出「这一格没取到」；
 *  - **`0` 照常渲染成 `0`**。把缺字段渲染成 0 的后果是：数据库挂了半天，
 *    而看板一直平静地显示「今日收入 ¥0.00」，没有任何人会去查。
 *
 * 两者的区分由 `ops-common.tsx` 的 `MISSING` / `formatCount` 与
 * `formatCny` / `formatBytes`（它们对 `undefined` 也返回 `—`）统一保证。
 *
 * # 版面为什么与脚手架不同
 *
 * 脚手架把「节点异常」放在最后并注明「最该被一眼看到的一块」。既然如此就放到最上面 ——
 * 节点失联的用户感知是「突然连不上」，而这时候用户多半也打不开面板来告诉我们。
 *
 * # 契约里没有、因而这一页显示不了的两格（登记，不是遗漏）
 *
 * `AdminDashboard` 只有九个字段：在线数、节点数、今日流量、今日/本月收入、待回工单、欠费订单。
 * **没有「新注册人数」，也没有「今日订单笔数」**（只有金额）。
 * 编两个数出来比不显示更糟，所以页面底部把这两格作为**缺口**列出来。
 */
import { useMemo } from 'react';
import type { ReactNode } from 'react';
import { PageHeader } from '@babelplus/shared/ui';
import type { components } from '@babelplus/shared/api';
import { unwrap } from '@babelplus/shared/api';
import { Badge, Button, Card, CardTitle, Icon, LinkButton, formatBytes, formatCny } from './_imports.ts';
import {
  CaveatNotice,
  ListSkeleton,
  MISSING,
  QueryErrorState,
  formatCount,
  useOpsQuery,
} from './ops-common.tsx';
import { api } from '../lib/api.ts';

type AdminDashboard = components['schemas']['AdminDashboard'];

function loadDashboard(): Promise<AdminDashboard> {
  return unwrap(api().GET('/api/v1/admin/dashboard'));
}

/**
 * 服务端那五个 goroutine 的**逐组**映射。
 *
 * 为什么按「组」而不是按「字段」判缺失：五组各自成败，**一组内的字段要么全有要么全无**
 * （`loadAdminDashboard` 里 `if xxxOK { … }` 一次填完一组）。
 * 按组判能说出「节点那一格没取到」，按字段判只能说「有几个字段是空的」——
 * 前者能直接对上服务端日志里的 `cell=nodes`，后者对不上任何东西。
 */
const CELL_GROUPS = [
  { cell: 'online_users', label: '在线用户', probe: 'online_users' },
  { cell: 'nodes', label: '节点', probe: 'active_nodes' },
  { cell: 'traffic_today', label: '今日流量', probe: 'today_upload_bytes' },
  { cell: 'revenue', label: '收入', probe: 'today_revenue_amount' },
  { cell: 'queues', label: '工单与欠费订单', probe: 'pending_tickets' },
] as const satisfies ReadonlyArray<{ cell: string; label: string; probe: keyof AdminDashboard }>;

/** 「这一格没有值」。`undefined`（字段缺席）与 `null` 一视同仁，**`0` 不算**。 */
function absent(v: number | null | undefined): boolean {
  return v === undefined || v === null;
}

function missingCells(data: AdminDashboard): string[] {
  return CELL_GROUPS.filter((g) => absent(data[g.probe])).map((g) => g.label);
}

export default function DashboardPage() {
  const query = useOpsQuery<AdminDashboard>(loadDashboard, [], '看板取数失败');
  const data = query.data;

  const missing = useMemo(() => (data ? missingCells(data) : []), [data]);

  /**
   * 「今天还没有数据」。判据是**每一组都取到了、且全部为零** ——
   * 缺字段不能算零（见文件头）。
   *
   * ⚠️ 这里刻意**不用 `EmptyState` 替换整页**：看板的空态与列表的空态不是一回事。
   * 列表为空时那一页没别的可看，看板为空时「节点有几台、在线几人」仍然是要看的事实。
   * 把五张卡片换成一句「暂无数据」等于在最需要看节点的那一天把节点数藏起来。
   */
  const allZero =
    data !== null &&
    missing.length === 0 &&
    [
      data.online_users,
      data.active_nodes,
      data.total_nodes,
      data.today_upload_bytes,
      data.today_download_bytes,
      data.today_revenue_amount,
      data.month_revenue_amount,
      data.pending_tickets,
      data.underpaid_orders,
    ].every((v) => v === 0);

  const offlineNodes =
    data?.total_nodes !== undefined && data.active_nodes !== undefined
      ? data.total_nodes - data.active_nodes
      : null;

  return (
    <>
      <PageHeader
        title="运营看板"
        description="今天发生了什么，有没有需要马上处理的。"
        meta={
          <>
            <Badge tone="info">P1</Badge>
            <Badge tone="neutral">M3 · 桌面优先，手机上可读即可</Badge>
          </>
        }
        actions={
          <Button onClick={query.reload} disabled={query.state === 'loading'}>
            重新取数
          </Button>
        }
      />

      {query.state === 'loading' ? <ListSkeleton rows={5} /> : null}

      {query.state === 'error' && query.error ? (
        <QueryErrorState
          error={query.error}
          what="运营看板"
          why={
            <>
              看板的五组数字挂在 <code className="font-mono">GET /admin/dashboard</code> 上。
            </>
          }
          onRetry={query.reload}
        />
      ) : null}

      {query.state === 'ready' && data ? (
        <div className="space-y-4">
          {missing.length > 0 ? (
            <CaveatNotice>
              这次取数有 <strong className="font-medium text-fg">{missing.length}</strong> 组没回来：
              {missing.join('、')}。它们那几格显示的是「{MISSING}」，
              <strong className="font-medium text-fg">不是 0</strong>。
              五组数字在服务端是并发取的，一组失败不影响其余几组 ——
              所以其它格子里的数字仍然是刚取到的，可以照常用。
              失败原因只在服务端日志里（<code className="font-mono">bp_admin_dashboard_cell_failed</code>，
              带 <code className="font-mono">cell=</code> 标明是哪一组）。
              <span className="mt-2 block">
                <Button onClick={query.reload}>只重试一次看看</Button>
              </span>
            </CaveatNotice>
          ) : null}

          {allZero ? (
            <CaveatNotice>
              五组数字都取到了，而且全是零。新部署的第一天这是正常的；
              如果不是第一天，先去<strong className="font-medium text-fg">检查节点有没有在上报</strong> ——
              节点不上报时流量与在线数会一起归零，而这两个数看起来和「今天没人用」一模一样。
              <span className="mt-2 block">
                <LinkButton tone="primary" href="/admin/nodes">
                  去看节点 <Icon.ArrowRight size={14} />
                </LinkButton>
              </span>
            </CaveatNotice>
          ) : null}

          {/* 🔴 放在最上面：节点失联时用户多半也打不开面板来告诉我们，
              这一格是唯一会主动说话的地方。 */}
          <Card>
            <CardTitle hint={<ModuleLink to="/admin/nodes">节点管理</ModuleLink>}>节点</CardTitle>
            <div className="grid gap-4 sm:grid-cols-3">
              <Metric
                label="在线（2 分钟内有上报）"
                value={formatCount(data.active_nodes)}
                missing={absent(data.active_nodes)}
                hint="服务端口径是 alive_nodes（近 2 分钟推送过），不是「已启用」——「现在有几台能用」才是打开看板时要问的。"
              />
              <Metric
                label="登记在册"
                value={formatCount(data.total_nodes)}
                missing={absent(data.total_nodes)}
              />
              <Metric
                label="失联"
                value={offlineNodes === null ? MISSING : formatCount(offlineNodes)}
                missing={offlineNodes === null}
                tone={offlineNodes !== null && offlineNodes > 0 ? 'danger' : 'normal'}
                hint={
                  offlineNodes !== null && offlineNodes > 0
                    ? '这些节点上的用户此刻多半正在「突然连不上」，而他们也打不开面板来报障。'
                    : '登记在册减去仍在上报的。'
                }
              />
            </div>
          </Card>

          <div className="grid gap-4 sm:grid-cols-2">
            <Card>
              <CardTitle hint={<ModuleLink to="/admin/tickets">工单处理</ModuleLink>}>待回工单</CardTitle>
              <Metric
                label="待处理"
                value={formatCount(data.pending_tickets)}
                missing={absent(data.pending_tickets)}
                tone={
                  data.pending_tickets !== undefined && data.pending_tickets > 0 ? 'warn' : 'normal'
                }
                hint="⚠️ 契约的看板只给一个总数，没有「按 SLA 剩余时间排序的前 5 条」。要按 SLA 排队得去工单模块。"
              />
            </Card>

            <Card>
              <CardTitle hint={<ModuleLink to="/admin/orders">订单管理</ModuleLink>}>欠费 / 少付订单</CardTitle>
              <Metric
                label="underpaid"
                value={formatCount(data.underpaid_orders)}
                missing={absent(data.underpaid_orders)}
                tone={
                  data.underpaid_orders !== undefined && data.underpaid_orders > 0 ? 'warn' : 'normal'
                }
                hint="链上支付金额不足的订单。它们既没开通也没退款，卡在中间，需要人工判。"
              />
            </Card>

            <Card>
              <CardTitle>收入</CardTitle>
              <div className="grid gap-4 sm:grid-cols-2">
                <Metric
                  label="今日"
                  value={formatCny(data.today_revenue_amount)}
                  missing={absent(data.today_revenue_amount)}
                />
                <Metric
                  label="本月"
                  value={formatCny(data.month_revenue_amount)}
                  missing={absent(data.month_revenue_amount)}
                />
              </div>
              <p className="mt-3 text-xs leading-relaxed text-fg-subtle">
                金额在契约里是<strong className="font-medium text-fg-muted">分</strong>的 int64，
                展示统一走 <code className="font-mono">formatCny</code>，不做浮点除法。
              </p>
            </Card>

            <Card>
              <CardTitle hint={<ModuleLink to="/admin/stats">流量统计</ModuleLink>}>今日流量</CardTitle>
              <div className="grid gap-4 sm:grid-cols-2">
                <Metric
                  label="上传"
                  value={formatBytes(data.today_upload_bytes)}
                  missing={absent(data.today_upload_bytes)}
                />
                <Metric
                  label="下载"
                  value={formatBytes(data.today_download_bytes)}
                  missing={absent(data.today_download_bytes)}
                />
              </div>
              <p className="mt-3 text-xs leading-relaxed text-fg-subtle">
                出口成本估算<strong className="font-medium text-fg-muted">不在这一页</strong>：
                它要乘一个会变的单价（网络层级 / 区域 / 用量档位），
                单价的输入在流量统计页。把一个写死的单价放在看板上，
                等于让每天都看的那个数字每天都错一点。
              </p>
            </Card>

            <Card className="sm:col-span-2">
              <CardTitle>在线用户</CardTitle>
              <Metric
                label="当前在线"
                value={formatCount(data.online_users)}
                missing={absent(data.online_users)}
                hint="来自 UNLOGGED 表（服务崩溃后会被自动 TRUNCATE）。所以它变成 0 也可能只是刚重启过，别拿它单独下结论。"
              />
            </Card>
          </div>

          <Card>
            <CardTitle>这一页还缺的两格（登记，不是遗漏）</CardTitle>
            <ul className="space-y-1.5 text-sm leading-relaxed text-fg-muted">
              <li>
                · <strong className="font-medium text-fg">新注册人数</strong> ——
                契约的 <code className="font-mono">AdminDashboard</code> 没有这个字段，
                服务端也没有对应的查询。要它得先加字段。
              </li>
              <li>
                · <strong className="font-medium text-fg">今日订单笔数</strong> ——
                只有金额（<code className="font-mono">today_revenue_amount</code>）没有笔数。
                「今天 3 笔共 ¥600」与「今天 60 笔共 ¥600」是两件完全不同的事，
                而现在这一页只能说出后半句。
              </li>
              <li>
                · <strong className="font-medium text-fg">按 SLA 排序的前 5 条工单</strong> ——
                看板只给总数。排队要去工单模块。
              </li>
            </ul>
          </Card>
        </div>
      ) : null}
    </>
  );
}

/* ────────────────────────────── 版面零件 ────────────────────────────── */

function ModuleLink({ to, children }: { to: string; children: ReactNode }) {
  return (
    <a href={to} className="text-accent hover:underline">
      {children} →
    </a>
  );
}

/**
 * 一格数字。
 *
 * `missing` 为真时除了显示 `—` 还要**说一句**：一个孤零零的破折号看起来像「没有」，
 * 而它的真实含义是「没取到」。这两者在看板上是相反的结论。
 */
function Metric({
  label,
  value,
  missing = false,
  tone = 'normal',
  hint,
}: {
  label: string;
  value: string;
  missing?: boolean;
  tone?: 'normal' | 'warn' | 'danger';
  hint?: ReactNode;
}) {
  const color =
    missing ? 'text-fg-subtle' : tone === 'danger' ? 'text-danger' : tone === 'warn' ? 'text-warn' : 'text-fg';
  return (
    <div className="min-w-0">
      <p className="text-xs text-fg-muted">{label}</p>
      <p className={`mt-0.5 text-2xl font-semibold tabular-nums ${color}`}>{value}</p>
      {missing ? (
        <p className="mt-1 text-xs leading-relaxed text-warn">
          这一格没取到（不是 0）。服务端并发取数时这一组失败了。
        </p>
      ) : hint ? (
        <p className="mt-1 text-xs leading-relaxed text-fg-subtle">{hint}</p>
      ) : null}
    </div>
  );
}

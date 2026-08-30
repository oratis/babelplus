/**
 * 模块 5 · 节点管理 `/admin/nodes` —— P1 / **M2**。
 * M2 的理由很具体：**手机上要能紧急停用节点**。节点出事的时候人不一定在电脑前。
 *
 * 接线的四个 operation：`listAdminNodes` / `createAdminNode` /
 * `enableAdminNode` / `disableAdminNode`（删除在详情页，见 `NodeDetailPage.tsx`）。
 *
 * # 这一页上三处「本来该有、但契约给不了」的东西
 *
 * 产品文档（page-inventory §4.3）要求列表有七列：名称、地区、协议、**在线人数**、
 * 最后上报、**今日流量**、健康。契约的 `AdminNode` 里**没有后两者**：
 *
 *  1. **在线人数**：整个 openapi 里唯一的 online 计数是 `AdminDashboard.online_users`，
 *     那是全站总数，不是每节点。服务端确实有这个数（`AdminGetNodeForDangerOp`
 *     的 `reported_online_users` / `observed_online_users`），但它只在
 *     **删除节点的 422 消息里**露出来，没有任何读端点返回它。
 *  2. **今日流量**：`getAdminStats?scope=server` 能给按节点聚合的字节数，但那是模块 11
 *     的端点，且本页的 `ModuleScaffold` 没有声明它。这一列留空并说明去哪看，
 *     比在节点页里偷偷多打一个别的模块的端点要诚实。
 *
 * 🔴 两处都用 `<Unknown>` 占位而不是显示 0。显示 0 的后果是「我们不知道这台机器上有多少人」
 * 被渲染成「这台机器上没有人」—— 而后者正是让人放心按下停用的那个数字。
 *
 * # 筛选为什么是前端的
 *
 * `listAdminNodes` 的 query 只有 `limit` / `cursor` / `count`，**没有任何筛选参数**。
 * 所以这里的筛选只能在**已经加载进来的那些行**里做，界面必须把这句话说出来 ——
 * 否则「筛出 0 条」会被读成「没有这样的节点」，而真相可能是它在第 2 页。
 */
import { useId, useMemo, useState, type ReactNode } from 'react';
import { Link } from 'react-router';
import { ApiError, unwrap } from '@babelplus/shared/api';
import { PageHeader } from '@babelplus/shared/ui';
import {
  Badge,
  Button,
  Card,
  CardTitle,
  EmptyState,
  Icon,
  LoadingState,
  cx,
  formatDateTime,
} from './_imports.ts';
import { DangerousAction } from '../components/DangerousAction.tsx';
import { api } from '../lib/api.ts';
import {
  ContractGapNotice,
  DangerSummary,
  FormAlert,
  NODE_PAGE_SIZE,
  NODE_PROTOCOLS,
  NodeEnabledBadge,
  NodeHealthBadge,
  QueryErrorState,
  SelectField,
  TextField,
  Unknown,
  asApiError,
  listAdminNodesPage,
  nodeHealth,
  useApiQuery,
  useMorePages,
  type AdminNode,
  type NodeHealth,
} from './node-common.tsx';

export default function NodesPage() {
  // 第一页带 `count=true`：后台要分页器，而契约允许管理面返总数（用户面永不返）。
  // 后续页不再带，理由见 `listAdminNodesPage` 的注释。
  const first = useApiQuery(() => listAdminNodesPage(null, true), [], '节点列表加载失败');
  const more = useMorePages(first.data);

  /**
   * 写操作的结果就地覆盖，**不重拉整张列表**。
   *
   * 为什么是一张覆盖表而不是改数据源：分页后的行躺在 `useMorePages` 内部，
   * 页面拿不到它的 setter。用覆盖表能对**任意一页**上的行生效，
   * 而重拉会把操作者眼前的表打回骨架屏 —— 他刚在一台出事的机器上点了停用，
   * 表突然空了，最自然的反应是再点一次。
   */
  const [overrides, setOverrides] = useState<Readonly<Record<number, AdminNode>>>({});
  const [created, setCreated] = useState<readonly AdminNode[]>([]);
  const [rowError, setRowError] = useState<{ id: number; error: ApiError } | null>(null);
  const [pendingEnable, setPendingEnable] = useState<number | null>(null);
  const [creating, setCreating] = useState(false);

  function applyWrite(node: AdminNode): void {
    setOverrides((prev) => ({ ...prev, [node.id]: node }));
  }

  const rows: readonly AdminNode[] = useMemo(
    () => [...created, ...more.items].map((n) => overrides[n.id] ?? n),
    [created, more.items, overrides],
  );

  /* ── 筛选（只作用于已加载的行，见文件头） ───────────────────────── */
  const [keyword, setKeyword] = useState('');
  const [enabledFilter, setEnabledFilter] = useState<'all' | 'on' | 'off'>('all');
  const [healthFilter, setHealthFilter] = useState<'all' | NodeHealth>('all');
  const filtered = useMemo(
    () => rows.filter((n) => matchesFilters(n, keyword, enabledFilter, healthFilter)),
    [rows, keyword, enabledFilter, healthFilter],
  );
  const filtering = keyword.trim() !== '' || enabledFilter !== 'all' || healthFilter !== 'all';

  async function enableNode(node: AdminNode): Promise<void> {
    setPendingEnable(node.id);
    setRowError(null);
    try {
      // 启用**不是** D4：它不会让任何人掉线。给它套一层确认串只会训练操作者
      // 把确认框当成一道随手点掉的手续，而 D4 恰恰指望那一道能让人停一秒。
      const updated = await unwrap(
        api().POST('/api/v1/admin/nodes/{id}/enable', { params: { path: { id: node.id } } }),
      );
      applyWrite(updated);
    } catch (cause) {
      setRowError({ id: node.id, error: asApiError(cause, '启用节点失败') });
    } finally {
      setPendingEnable(null);
    }
  }

  const total = more.meta?.total;

  return (
    <>
      <PageHeader
        title="节点"
        description="线路的启停、健康与今日流量。手机上要能完成紧急停用。"
        meta={
          <>
            <Badge tone="info">P1</Badge>
            <Badge tone="warn">M2 · 手机上核心操作必须能完成，表格卡片化降级</Badge>
            {typeof total === 'number' ? <Badge>共 {total} 台</Badge> : null}
          </>
        }
      />

      <DangerSummary codes={['D4']} />

      <div className="space-y-4">
        <CreateNodeCard
          busy={creating}
          onCreate={async (body) => {
            setCreating(true);
            try {
              const node = await unwrap(api().POST('/api/v1/admin/nodes', { body }));
              // 新建的节点排在最前面：刚建好的那台正是接下来要给它发密钥的那台。
              setCreated((prev) => [node, ...prev]);
            } finally {
              setCreating(false);
            }
          }}
        />

        {first.state === 'loading' ? <LoadingState /> : null}

        {first.state === 'error' && first.error ? (
          <QueryErrorState error={first.error} what="节点列表" onRetry={first.reload} />
        ) : null}

        {first.state === 'ready' && rows.length === 0 ? (
          <EmptyState
            title="还没有节点"
            description="没有节点，订阅拉下来是空列表，用户连不上任何东西。"
            action={
              <a href="#new-node" className="text-sm font-medium text-accent underline underline-offset-4">
                新建第一台节点
              </a>
            }
          />
        ) : null}

        {first.state === 'ready' && rows.length > 0 ? (
          <Card>
            <CardTitle
              hint={
                // 「已加载」与「共」分开说：两个数不一样时，操作者才知道自己看的是不是全部。
                `已加载 ${rows.length} 台${typeof total === 'number' ? ` / 共 ${total} 台` : ''}` +
                (filtering ? `，筛选后 ${filtered.length} 台` : '')
              }
            >
              节点列表
            </CardTitle>

            <FilterBar
              keyword={keyword}
              onKeyword={setKeyword}
              enabled={enabledFilter}
              onEnabled={setEnabledFilter}
              health={healthFilter}
              onHealth={setHealthFilter}
            />

            {filtered.length === 0 ? (
              <p className="mt-4 rounded-lg border border-dashed border-line p-4 text-sm leading-relaxed text-fg-muted">
                已加载的 {rows.length} 台里没有符合条件的。
                {more.canLoadMore ? (
                  <>
                    {' '}
                    <strong className="font-medium text-fg">还有没加载的页</strong> ——
                    这个端点不支持服务端筛选，筛的只是眼前这些行，所以「筛不到」不等于「没有」。
                  </>
                ) : null}
              </p>
            ) : (
              <ul className="mt-4 space-y-3">
                {filtered.map((node) => (
                  <li key={node.id} className="rounded-xl border border-line bg-surface-alt/40 p-4">
                    <NodeRow
                      node={node}
                      pendingEnable={pendingEnable === node.id}
                      error={rowError?.id === node.id ? rowError.error : null}
                      onEnable={() => void enableNode(node)}
                      onDisabled={applyWrite}
                    />
                  </li>
                ))}
              </ul>
            )}

            {/* 「加载更多」而不是无限滚动：这一页每一行都挂着一个能让人掉线的按钮，
                滚过头再往回找是实打实的成本，而「加载更多」是可以停下来的。 */}
            {more.canLoadMore ? (
              <div className="mt-4 flex flex-wrap items-center gap-3">
                <Button onClick={() => void more.loadMore()} disabled={more.pending}>
                  {more.pending ? '加载中…' : `加载更多（每页 ${NODE_PAGE_SIZE} 条）`}
                </Button>
                {more.error ? (
                  <span className="text-sm text-danger">{more.error.message}</span>
                ) : null}
              </div>
            ) : (
              <p className="mt-4 text-xs text-fg-subtle">已经到底了。</p>
            )}
          </Card>
        ) : null}
      </div>
    </>
  );
}

/* ────────────────────────────── 一行 ────────────────────────────── */

function NodeRow({
  node,
  pendingEnable,
  error,
  onEnable,
  onDisabled,
}: {
  node: AdminNode;
  pendingEnable: boolean;
  error: ApiError | null;
  onEnable: () => void;
  onDisabled: (node: AdminNode) => void;
}) {
  const health = nodeHealth(node);

  return (
    <>
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="min-w-0">
          <Link
            to={`/admin/nodes/${node.id}`}
            className="text-base font-semibold text-fg underline-offset-4 hover:underline"
          >
            {node.name}
          </Link>
          <p className="mt-0.5 font-mono text-xs text-fg-subtle">
            #{node.id} · {node.host ?? '—'}
            {node.port ? `:${node.port}` : ''}
          </p>
        </div>
        <div className="flex shrink-0 flex-wrap items-center gap-2">
          <NodeEnabledBadge enabled={node.enabled} />
          <NodeHealthBadge health={health} />
        </div>
      </div>

      {/* 七列在 <768px 上卡片化（M2）：同一份 DOM 用栅格降级，
          **不写两套 DOM** —— 两套迟早会有一套漏改，而漏掉的通常是手机那套。 */}
      <dl className="mt-3 grid grid-cols-2 gap-x-4 gap-y-2 text-sm sm:grid-cols-3 lg:grid-cols-6">
        <Cell label="地区">{node.region || <Unknown title="这台节点没有填地区" />}</Cell>
        <Cell label="协议">
          <span className="font-mono text-xs">{node.type}</span>
        </Cell>
        <Cell label="在线人数">
          <Unknown title="契约的 AdminNode 里没有每节点在线人数，没有任何读端点返回它" />
        </Cell>
        <Cell label="最后上报">
          {node.last_push_at ? (
            <span className={cx(health === 'stale' && 'text-warn')}>{formatDateTime(node.last_push_at)}</span>
          ) : (
            <Unknown title="从没上报过" />
          )}
        </Cell>
        <Cell label="今日流量">
          <Unknown title="按节点聚合的流量在 getAdminStats?scope=server，属于模块 11" />
        </Cell>
        <Cell label="分组">
          {node.group_ids && node.group_ids.length > 0 ? (
            <span className="font-mono text-xs">{node.group_ids.join(', ')}</span>
          ) : (
            // 「不属于任何分组」不是「没填」：没有分组的节点对**所有**用户不可见。
            <span className="text-warn" title="不属于任何分组的节点对所有用户不可见">
              无分组
            </span>
          )}
        </Cell>
      </dl>

      <div className="mt-3">
        {node.enabled ? (
          <DangerousAction
            code="D4"
            title="停用这个节点"
            submitLabel="确认停用"
            confirmation={node.name}
            context={<DisableContext node={node} />}
            onSubmit={async () => {
              // 🔴 收上来的 confirmation **发不出去** —— 契约给 disable 没有请求体。
              //    这里不假装它去过服务端，界面上也照实说（见 DisableContext）。
              const updated = await unwrap(
                api().POST('/api/v1/admin/nodes/{id}/disable', { params: { path: { id: node.id } } }),
              );
              onDisabled(updated);
            }}
          />
        ) : (
          <Button onClick={onEnable} disabled={pendingEnable}>
            {pendingEnable ? '启用中…' : '启用'}
          </Button>
        )}
        {error ? (
          <div className="mt-2">
            <FormAlert>{error.message}</FormAlert>
          </div>
        ) : null}
      </div>
    </>
  );
}

/**
 * 停用确认框里的事实块。
 *
 * 登记表（D4）写着「确认框内必须显示当前在线人数」，而契约给不了这个数（见文件头）。
 * **缺口写在操作者眼前**，不是藏进代码注释：他至少知道「我现在是在没有这个数的情况下按下去的」。
 */
function DisableContext({ node }: { node: AdminNode }) {
  return (
    <div className="space-y-2">
      <p>
        停用会让 <strong className="font-semibold">这台机器上的所有在线用户在 60 秒内掉线</strong>
        （节点每 60 秒拉一次 /config，拿到 enabled=false 就停止服务）。用户不会收到任何提示，
        他们看到的是「连不上了」。
      </p>
      <p className="text-fg-muted">
        节点 <code className="font-mono text-fg">{node.name}</code>（#{node.id} ·{' '}
        {node.host ?? '—'}
        {node.port ? `:${node.port}` : ''}）
        {node.last_push_at ? `，最后上报 ${formatDateTime(node.last_push_at)}` : '，从没上报过'}。
      </p>
      <ContractGapNotice>
        <strong className="font-medium text-fg">当前在线人数这里显示不出来。</strong>
        契约的 <code className="font-mono">AdminNode</code> 没有这个字段，也没有别的读端点返回它 ——
        服务端确实知道（删除节点时那条 422 消息里会报出「上报在线 N 人、观测到 M 人」），
        但停用这条路径上拿不到。补法是给 <code className="font-mono">AdminNode</code>{' '}
        加两个在线人数字段。
      </ContractGapNotice>
      <ContractGapNotice>
        <strong className="font-medium text-fg">这一层确认串只在前端。</strong>
        契约给 <code className="font-mono">disableAdminNode</code>{' '}
        没有请求体，所以你打进去的节点名与理由 <strong className="font-medium text-fg">都发不到服务端</strong>，审计里的 reason 会是空的
        （服务端刻意不编一句「管理员操作」填进去）。它买的是「让你停一秒看清楚要停哪台」，
        对一个直接 curl 的人是零。删除节点那条（`deleteAdminNode`）是服务端真比对的。
      </ContractGapNotice>
    </div>
  );
}

function Cell({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="min-w-0">
      <dt className="text-xs text-fg-subtle">{label}</dt>
      <dd className="mt-0.5 truncate text-fg">{children}</dd>
    </div>
  );
}

/* ────────────────────────────── 筛选 ────────────────────────────── */

function matchesFilters(
  node: AdminNode,
  keyword: string,
  enabled: 'all' | 'on' | 'off',
  health: 'all' | NodeHealth,
): boolean {
  if (enabled === 'on' && !node.enabled) return false;
  if (enabled === 'off' && node.enabled) return false;
  if (health !== 'all' && nodeHealth(node) !== health) return false;

  const k = keyword.trim().toLowerCase();
  if (k === '') return true;
  // host 也进匹配范围：出故障时手里那串通常是 IP，不是节点名。
  return [node.name, node.region, node.host, node.type, String(node.id)]
    .filter((v): v is string => typeof v === 'string')
    .some((v) => v.toLowerCase().includes(k));
}

function FilterBar({
  keyword,
  onKeyword,
  enabled,
  onEnabled,
  health,
  onHealth,
}: {
  keyword: string;
  onKeyword: (v: string) => void;
  enabled: 'all' | 'on' | 'off';
  onEnabled: (v: 'all' | 'on' | 'off') => void;
  health: 'all' | NodeHealth;
  onHealth: (v: 'all' | NodeHealth) => void;
}) {
  const id = useId();
  return (
    <div className="mt-4">
      <div className="grid gap-3 sm:grid-cols-3">
        <TextField
          id={`${id}-keyword`}
          label="关键字"
          value={keyword}
          onChange={onKeyword}
          placeholder="名称 / 地区 / IP / 协议 / id"
        />
        <SelectField
          id={`${id}-enabled`}
          label="启停"
          value={enabled}
          onChange={(v) => onEnabled(v as 'all' | 'on' | 'off')}
          options={[
            { value: 'all', label: '全部' },
            { value: 'on', label: '启用中' },
            { value: 'off', label: '停用中' },
          ]}
        />
        <SelectField
          id={`${id}-health`}
          label="健康"
          value={health}
          onChange={(v) => onHealth(v as 'all' | NodeHealth)}
          options={[
            { value: 'all', label: '全部' },
            { value: 'fresh', label: '在上报' },
            { value: 'stale', label: '超过 5 分钟没上报' },
            { value: 'never', label: '从没上报过' },
          ]}
        />
      </div>
      <p className="mt-2 text-xs leading-relaxed text-fg-subtle">
        筛选只作用于<strong className="font-medium text-fg-muted">已经加载进来的行</strong> ——
        <code className="font-mono"> listAdminNodes </code>
        的 query 里只有 limit / cursor / count，没有任何筛选参数。要筛全量得先把页翻完。
      </p>
    </div>
  );
}

/* ────────────────────────────── 新建 ────────────────────────────── */

interface NodeUpsertBody {
  name: string;
  type: string;
  host: string;
  port: number;
  region: string;
  reason: string;
}

/**
 * 新建节点。
 *
 * **刻意不套 `<DangerousAction>`**：D4 / D9 管的是「改一台正在跑的机器」，
 * 而新建出来的节点服务端默认 `enabled = false`、也还没有任何密钥 ——
 * 它掉不了任何人的线。把它做成危险操作会稀释危险操作这个信号本身。
 *
 * 但 `reason` 是契约的必填字段（`AdminNodeUpsert.reason`，服务端 ≥ 8 码位），
 * 所以这里照样要收，并且照样进审计日志。
 */
function CreateNodeCard({
  busy,
  onCreate,
}: {
  busy: boolean;
  onCreate: (body: NodeUpsertBody) => Promise<void>;
}) {
  const id = useId();
  const [open, setOpen] = useState(false);
  const [name, setName] = useState('');
  const [type, setType] = useState(NODE_PROTOCOLS[0]?.value ?? '');
  const [host, setHost] = useState('');
  const [port, setPort] = useState('443');
  const [region, setRegion] = useState('');
  const [reason, setReason] = useState('');
  const [error, setError] = useState<ApiError | null>(null);

  const portNumber = Number.parseInt(port.trim(), 10);
  const portOk = Number.isInteger(portNumber) && portNumber >= 1 && portNumber <= 65535;
  const reasonRunes = [...reason.trim()].length;
  const ready = name.trim() !== '' && host.trim() !== '' && portOk && reasonRunes >= 8;

  async function submit(): Promise<void> {
    setError(null);
    try {
      await onCreate({
        name: name.trim(),
        type,
        host: host.trim(),
        port: portNumber,
        region: region.trim(),
        reason: reason.trim(),
      });
      setOpen(false);
      setName('');
      setHost('');
      setRegion('');
      setReason('');
    } catch (cause) {
      setError(asApiError(cause, '新建节点失败'));
    }
  }

  if (!open) {
    return (
      <div id="new-node">
        <Button tone="primary" onClick={() => setOpen(true)}>
          新建节点 <Icon.ArrowRight size={14} />
        </Button>
      </div>
    );
  }

  return (
    <Card>
      <CardTitle hint="createAdminNode">新建节点</CardTitle>
      <p className="mt-1 text-sm leading-relaxed text-fg-muted">
        新建出来的节点<strong className="font-medium text-fg">默认是停用的</strong>，且还没有密钥。
        它拉不到配置、上报不了流量，也不会进任何人的订阅 —— 下一步是去
        <Link to="/admin/node-keys" className="mx-1 text-accent underline underline-offset-4">
          节点密钥
        </Link>
        给它签一把。
      </p>

      <div className="mt-4 grid gap-4 sm:grid-cols-2">
        <TextField id={`${id}-name`} label="名称" value={name} onChange={setName} placeholder="东京 01" />
        <SelectField
          id={`${id}-type`}
          label="协议"
          value={type}
          onChange={setType}
          options={NODE_PROTOCOLS}
          hint="服务端只认这四个值。填 vless 这类折叠名会被 422 退回：它对应两个不同的协议。"
        />
        <TextField id={`${id}-host`} label="Host" value={host} onChange={setHost} mono placeholder="203.0.113.10" />
        <TextField
          id={`${id}-port`}
          label="端口"
          value={port}
          onChange={setPort}
          mono
          inputMode="numeric"
          hint={portOk ? '1–65535' : <span className="text-danger">必须是 1–65535 之间的整数。</span>}
        />
        <TextField id={`${id}-region`} label="地区" value={region} onChange={setRegion} placeholder="日本" />
      </div>

      <div className="mt-4">
        <label htmlFor={`${id}-reason`} className="mb-1.5 block text-sm font-medium text-fg">
          操作原因（必填）
        </label>
        <textarea
          id={`${id}-reason`}
          name="reason"
          rows={2}
          value={reason}
          onChange={(event) => setReason(event.target.value)}
          className="w-full rounded-lg border border-line bg-surface px-3 py-2.5 text-base leading-relaxed text-fg focus-visible:border-accent focus-visible:outline-2 focus-visible:outline-offset-1 focus-visible:outline-accent"
        />
        <p className="mt-1.5 text-xs leading-relaxed text-fg-muted">
          至少 8 个字（当前 {reasonRunes}）。它会原样进审计日志 ——
          写「扩容」不如写「东京线路晚高峰丢包，加一台分流」。
        </p>
      </div>

      {error ? (
        <div className="mt-4">
          <FormAlert>
            {error.message}
            {error.requestId ? (
              <span className="mt-1 block font-mono text-xs opacity-80">请求号 {error.requestId}</span>
            ) : null}
          </FormAlert>
        </div>
      ) : null}

      <div className="mt-4 flex flex-wrap gap-2">
        <Button tone="primary" disabled={!ready || busy} onClick={() => void submit()}>
          {busy ? '创建中…' : '创建'}
        </Button>
        <Button tone="ghost" disabled={busy} onClick={() => setOpen(false)}>
          取消
        </Button>
      </div>
    </Card>
  );
}

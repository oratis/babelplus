/**
 * 模块 6 · 节点密钥 `/admin/node-keys` —— P1 / M3。安全加固第 1 条的落地页。
 *
 * 这个模块存在的理由：Xboard / SSPanel 系用**全局共享 token**，
 * 持 token 者可冒充任意节点。我们改成每节点独立密钥，`node_id` 从密钥推导，
 * 请求里带的 `node_id` 一律忽略 —— 这是**根治点，不是缓解**。
 *
 * 🔴 D5 的 UI 层硬约束：**禁止一步完成轮换。**
 * 强制两步：先发新密钥 → 确认节点已用新密钥上报 → 再撤旧的。
 * 一步完成的话，节点会在下一次轮询时失联，而它失联之后你就没法再让它换密钥了。
 *
 * # 三屏怎么落到界面上
 *
 * ① 签发（`createAdminNodeKey`）→ ② 等待（轮询 `listAdminNodeKeys` 看 `last_used_at`）
 * → ③ 吊销（`revokeAdminNodeKey`）。三块是**并列摆着的三张卡**而不是一个向导：
 * 轮换会横跨几分钟甚至跨人交接，一个有「下一步」状态的向导在刷新页面后就没了，
 * 而三张卡的状态**全部从服务端数据算出来**（`keyUsedSinceIssue` / `hasWitnessFor`），
 * 谁在什么时候打开这一页，看到的都是同一个真相。
 *
 * 🔴 第 ② 步的判据是 `last_used_at > created_at`，**不是 `last_used_at != null`**。
 * 后者会把一把「很久以前用过、节点早换走了」的密钥当成见证 ——
 * 拿它去吊销另一把，正好制造出「节点失联」。判据实现在 `node-common.tsx`，
 * 与服务端的 `used_since_issue` 逐字同口径。
 *
 * # 前端的第 ③ 步闸门不是安全边界
 *
 * `hasWitnessFor` 只用来决定按钮亮不亮。真正的拒绝在数据库那条 UPDATE 的 `EXISTS` 里，
 * 服务端**仍然可能回 409**（轮换期节点每 60 秒改一次 `last_used_at`，
 * 另一个管理员可能同时在吊销另一把）。那个 409 是**正确的**，界面原样显示它，
 * 不当成 bug 去重试。
 */
import { useEffect, useId, useMemo, useRef, useState, type ReactNode } from 'react';
import { Link, useSearchParams } from 'react-router';
import { ApiError, unwrap, unwrapEmpty, unwrapWithMeta } from '@babelplus/shared/api';
import { PageHeader } from '@babelplus/shared/ui';
import {
  Badge,
  Button,
  Card,
  CardTitle,
  EmptyState,
  LoadingState,
  cx,
  formatDateTime,
} from './_imports.ts';
import { DangerousAction } from '../components/DangerousAction.tsx';
import { api } from '../lib/api.ts';
import {
  ContractGapNotice,
  CopyButton,
  DangerSummary,
  NODE_KEY_MAX_ACTIVE,
  NODE_SCOPES,
  QueryErrorState,
  SelectField,
  TextField,
  activeKeys,
  asApiError,
  hasWitnessFor,
  keyIsActive,
  keyUsedSinceIssue,
  listAdminNodesPage,
  useApiQuery,
  useMorePages,
  type AdminNode,
  type NodeKey,
  type NodeScope,
} from './node-common.tsx';

/** 等待节点换用新密钥时的轮询间隔。节点侧是 60 秒轮询，比它快一档才能看见变化。 */
const POLL_INTERVAL_MS = 15_000;

function listNodeKeys(nodeId: number): Promise<NodeKey[]> {
  return unwrapWithMeta(
    api().GET('/api/v1/admin/nodes/{id}/keys', { params: { path: { id: nodeId } } }),
  ).then((envelope) => envelope.data);
}

export default function NodeKeysPage() {
  const [params, setParams] = useSearchParams();
  const raw = params.get('node') ?? '';
  // 选中的节点放在 query string 里而不是组件 state 里：轮换要跨几分钟、
  // 常常要把链接发给另一个人接手，而一个「刷新就回到没选」的页面在这个场景下很贵。
  const nodeId = /^\d+$/.test(raw) ? Number(raw) : null;

  const nodesFirst = useApiQuery(() => listAdminNodesPage(null, true), [], '节点列表加载失败');
  const nodes = useMorePages(nodesFirst.data);

  const keys = useApiQuery<NodeKey[] | null>(
    () => (nodeId === null ? Promise.resolve(null) : listNodeKeys(nodeId)),
    [nodeId],
    '密钥列表加载失败',
  );

  const [issued, setIssued] = useState<{ key: NodeKey; secret: string } | null>(null);
  const [pollError, setPollError] = useState<ApiError | null>(null);
  const [refreshedAt, setRefreshedAt] = useState<number | null>(null);

  const list = keys.data ?? [];
  const active = useMemo(() => activeKeys(list), [list]);
  // 「等着看它被用上」的那些密钥：有效、但签发之后还没被节点用过。
  const awaiting = useMemo(() => active.filter((k) => !keyUsedSinceIssue(k)), [active]);
  const waiting = awaiting.length > 0;

  /**
   * 刷新密钥列表。**用 `patch` 而不是 `reload`** ——
   * `reload` 会把三态打回 loading，于是每 15 秒整页闪一次骨架屏，
   * 而这一页恰恰要求人盯着「最后使用」那一列看它什么时候变。
   */
  async function refreshKeys(): Promise<void> {
    if (nodeId === null) return;
    try {
      const fresh = await listNodeKeys(nodeId);
      keys.patch(() => fresh);
      setRefreshedAt(Date.now());
      setPollError(null);
    } catch (cause) {
      // 轮询失败不许打断页面：眼前这份数据仍然是刚才那一刻的真相。
      setPollError(asApiError(cause, '刷新密钥列表失败'));
    }
  }

  const refreshRef = useRef(refreshKeys);
  refreshRef.current = refreshKeys;

  useEffect(() => {
    // 只在**真的在等**的时候轮询。一直轮询是在 db-f1-micro 上白付钱，
    // 而且会让「这一页在动」变成常态，从而失去「有东西变了」这个信号。
    if (nodeId === null || !waiting) return undefined;
    const timer = setInterval(() => void refreshRef.current(), POLL_INTERVAL_MS);
    return () => clearInterval(timer);
  }, [nodeId, waiting]);

  const selected = nodes.items.find((n) => n.id === nodeId) ?? null;

  return (
    <>
      <PageHeader
        title="节点密钥"
        description="每节点一把独立密钥。DB 只存 sha256，签发时的明文只出现一次。"
        meta={
          <>
            <Badge tone="info">P1</Badge>
            <Badge>M3 · 桌面优先，手机上可读即可</Badge>
          </>
        }
      />

      <DangerSummary codes={['D5']} />

      <div className="space-y-4">
        <NodePicker
          nodeId={nodeId}
          nodes={nodes.items}
          loading={nodesFirst.state === 'loading'}
          error={nodesFirst.state === 'error' ? nodesFirst.error : null}
          onRetry={nodesFirst.reload}
          canLoadMore={nodes.canLoadMore}
          loadingMore={nodes.pending}
          onLoadMore={() => void nodes.loadMore()}
          onSelect={(id) => {
            setIssued(null);
            setPollError(null);
            setParams(id === null ? {} : { node: String(id) }, { replace: true });
          }}
        />

        {nodeId === null ? (
          <EmptyState
            title="先选一台节点"
            description="密钥是挂在节点上的：契约的 listAdminNodeKeys 是 /admin/nodes/{id}/keys，没有跨节点的全量密钥列表。"
            action={
              <Link to="/admin/nodes" className="text-sm font-medium text-accent underline underline-offset-4">
                去节点列表看看有哪些机器
              </Link>
            }
          />
        ) : null}

        {nodeId !== null && keys.state === 'loading' ? <LoadingState /> : null}

        {nodeId !== null && keys.state === 'error' && keys.error ? (
          <QueryErrorState error={keys.error} what="密钥列表" onRetry={keys.reload} />
        ) : null}

        {nodeId !== null && keys.state === 'ready' ? (
          <>
            {issued ? <IssuedSecret issued={issued} onDismiss={() => setIssued(null)} /> : null}

            <IssueCard
              nodeId={nodeId}
              nodeName={selected?.name ?? `#${nodeId}`}
              activeCount={active.length}
              onIssued={(created) => {
                setIssued(created);
                // 新签的这把立刻进列表：第 ② 步要盯的就是它的「最后使用」。
                keys.patch((prev) => [
                  created.key,
                  ...(prev ?? []).filter((k) => k.id !== created.key.id),
                ]);
              }}
            />

            <WaitCard
              awaiting={awaiting}
              refreshedAt={refreshedAt}
              pollError={pollError}
              onRefresh={() => void refreshKeys()}
            />

            <KeyListCard
              nodeId={nodeId}
              nodeName={selected?.name ?? `#${nodeId}`}
              keys={list}
              onRevoked={() => void refreshKeys()}
            />
          </>
        ) : null}

        <QueryTokenCard />
      </div>
    </>
  );
}

/* ────────────────────────────── 节点选择器 ────────────────────────────── */

function NodePicker({
  nodeId,
  nodes,
  loading,
  error,
  onRetry,
  canLoadMore,
  loadingMore,
  onLoadMore,
  onSelect,
}: {
  nodeId: number | null;
  nodes: readonly AdminNode[];
  loading: boolean;
  error: ApiError | null;
  onRetry: () => void;
  canLoadMore: boolean;
  loadingMore: boolean;
  onLoadMore: () => void;
  onSelect: (id: number | null) => void;
}) {
  const id = useId();
  const known = nodes.some((n) => n.id === nodeId);

  return (
    <Card>
      <CardTitle hint="listAdminNodes">选择节点</CardTitle>
      {loading ? <p className="mt-3 text-sm text-fg-muted">正在加载节点列表…</p> : null}
      {error ? (
        <div className="mt-3">
          <QueryErrorState error={error} what="节点列表" onRetry={onRetry} />
        </div>
      ) : null}
      {!loading && !error ? (
        <div className="mt-4 space-y-3">
          <SelectField
            id={`${id}-node`}
            label="节点"
            value={nodeId === null ? '' : String(nodeId)}
            onChange={(v) => onSelect(v === '' ? null : Number(v))}
            options={[
              { value: '', label: '— 请选择 —' },
              // 地址栏里带了一个还没加载到的 id 时，把它作为一项补进来，
              // 否则 <select> 会显示成「请选择」而下面却在展示那个节点的密钥。
              ...(nodeId !== null && !known
                ? [{ value: String(nodeId), label: `#${nodeId}（不在已加载的这几页里）` }]
                : []),
              ...nodes.map((n) => ({
                value: String(n.id),
                label: `${n.name} · #${n.id}${n.enabled ? '' : '（停用中）'}`,
              })),
            ]}
          />
          <div className="flex flex-wrap items-center gap-3">
            {canLoadMore ? (
              <Button onClick={onLoadMore} disabled={loadingMore}>
                {loadingMore ? '加载中…' : '加载更多节点'}
              </Button>
            ) : null}
            <p className="text-xs leading-relaxed text-fg-subtle">
              下拉里只有<strong className="font-medium text-fg-muted">已经加载的那几页</strong>。
              节点多的时候先把页翻完，或者直接改地址栏的 <code className="font-mono">?node=</code>。
            </p>
          </div>
        </div>
      ) : null}
    </Card>
  );
}

/* ────────────────────────── 明文（只出现这一次） ────────────────────────── */

/**
 * 🔴 这一块是整个后台唯一会显示密钥明文的地方，而且**只有这一次**。
 *
 * `secret_hash` 在 DB 里是 SHA-256，没有任何端点能把明文再取出来；
 * 关掉这一块之后它就永远消失了，只能重新签一把（然后又欠一次第 ③ 步）。
 * 所以：说清楚、给复制按钮、并且**要求手动确认关闭**，
 * 不做「3 秒后自动隐藏」这种会把密钥吃掉的贴心设计。
 */
function IssuedSecret({
  issued,
  onDismiss,
}: {
  issued: { key: NodeKey; secret: string };
  onDismiss: () => void;
}) {
  return (
    <Card className="border-l-4 border-l-warn">
      <h3 className="text-base font-semibold text-fg">
        新密钥已签发 —— 现在就复制，关掉就再也看不到
      </h3>
      <p className="mt-1.5 text-sm leading-relaxed text-fg-muted">
        DB 里只有它的 sha256。<strong className="font-medium text-fg">没有任何端点能再取出这串明文</strong>，
        关掉这一块之后只能重新签一把（而那会让你多欠一次第 ③ 步的吊销）。
      </p>
      <div className="mt-3 rounded-lg border border-line bg-surface-alt p-3">
        <code
          data-testid="issued-secret"
          className="block break-all font-mono text-sm text-fg select-all"
        >
          {issued.secret}
        </code>
      </div>
      <div className="mt-3 flex flex-wrap items-center gap-3">
        <CopyButton value={issued.secret} label="复制密钥" />
        <Button tone="ghost" onClick={onDismiss}>
          我已经复制好了，关掉
        </Button>
      </div>
      <p className="mt-3 text-xs leading-relaxed text-fg-subtle">
        指纹 <code className="font-mono text-fg">{issued.key.key_id}</code> ·{' '}
        {issued.key.name} · scope {issued.key.scopes.join(' ')}
        {issued.key.expires_at ? ` · ${formatDateTime(issued.key.expires_at)} 过期` : ' · 不过期'}
      </p>
      <p className="mt-2 text-xs leading-relaxed text-fg-subtle">
        把它配到节点上之后，回到下面第 ② 步等它出现在「最后使用」里。
        <strong className="font-medium text-fg-muted">在那之前不要吊销任何旧密钥。</strong>
      </p>
    </Card>
  );
}

/* ────────────────────────── 第 ① 步：签发 ────────────────────────── */

/** 有效期只给几个档，不给自由的日期输入框。理由见 `EXPIRY_OPTIONS` 上方注释。 */
const EXPIRY_OPTIONS: ReadonlyArray<{ value: string; label: string; days: number | null }> = [
  { value: 'never', label: '不过期（默认）', days: null },
  { value: '90', label: '90 天后过期', days: 90 },
  { value: '180', label: '180 天后过期', days: 180 },
  { value: '365', label: '365 天后过期', days: 365 },
];

function IssueCard({
  nodeId,
  nodeName,
  activeCount,
  onIssued,
}: {
  nodeId: number;
  nodeName: string;
  activeCount: number;
  onIssued: (created: { key: NodeKey; secret: string }) => void;
}) {
  const fieldId = useId();
  const [name, setName] = useState(() => defaultKeyName());
  const [expiry, setExpiry] = useState('never');
  const [scopes, setScopes] = useState<readonly NodeScope[]>(() =>
    NODE_SCOPES.filter((s) => s.byDefault).map((s) => s.value),
  );

  const nameRunes = [...name.trim()].length;
  const full = activeCount >= NODE_KEY_MAX_ACTIVE;

  const blocked = full
    ? `这个节点已经有 ${activeCount} 把同时有效的密钥（上限 ${NODE_KEY_MAX_ACTIVE}）。轮换期有两把是正常的，出现第三把说明上一次轮换没做完 —— 先去下面第 ③ 步把旧的吊销掉。`
    : nameRunes === 0
      ? '先给这把密钥起个名字（会进审计与密钥列表，如「2026-08 轮换」）。'
      : nameRunes > 64
        ? '名字最长 64 个字。'
        : scopes.length === 0
          ? // 服务端对空数组直接 422：一把零 scope 的密钥能过鉴权，但每个端点都回 403，
            // 看起来像被封禁而不是像配置错误。
            '至少要选一个 scope。一把零 scope 的密钥在所有节点端点上都会 403。'
          : null;

  return (
    // 锚点挂在外层：`Card` 只接受 children / className / as，没有 id。
    <div id="issue-key">
      <Card>
        <CardTitle hint="D5 第 1 步 · createAdminNodeKey">① 签发新密钥</CardTitle>
        <p className="mt-1 text-sm leading-relaxed text-fg-muted">
          给 <strong className="font-medium text-fg">{nodeName}</strong> 签一把新密钥。
          一个节点<strong className="font-medium text-fg">可以同时持有多把有效密钥</strong> ——
          这正是两步轮换能安全进行的前提：新旧并行一段时间，等确认节点换过去了再撤旧的。
        </p>

        <div className="mt-4 grid gap-4 sm:grid-cols-2">
          <TextField
            id={`${fieldId}-name`}
            label="密钥名"
            value={name}
            onChange={setName}
            placeholder="2026-08 轮换"
            hint={`人写的备注，1–64 字（当前 ${nameRunes}）。事后要靠它回答「这把是谁在什么时候发的」。`}
          />
          <SelectField
            id={`${fieldId}-expiry`}
            label="有效期"
            value={expiry}
            onChange={setExpiry}
            options={EXPIRY_OPTIONS.map((o) => ({ value: o.value, label: o.label }))}
            hint={
              // 不给自由日期输入：一个手打的时间戳在本地时区与 UTC 之间是掷硬币，
              // 而签出一把「昨天就过期」的密钥会被服务端 422 退回，
              // 签出一把「一小时后过期」的则会在轮换做到一半时把节点打死。
              '只给固定档位。手打时间戳在本地时区与 UTC 之间容易差 8 小时，而一把过早过期的密钥会让轮换卡死。'
            }
          />
        </div>

        <fieldset className="mt-4">
          <legend className="mb-2 text-sm font-medium text-fg">scope（精确匹配，非前缀）</legend>
          <div className="grid gap-2 sm:grid-cols-2">
            {NODE_SCOPES.map((s) => (
              <label
                key={s.value}
                className="flex items-start gap-2 rounded-lg border border-line p-2.5 text-sm"
              >
                <input
                  type="checkbox"
                  checked={scopes.includes(s.value)}
                  onChange={(event) =>
                    setScopes((prev) =>
                      event.target.checked ? [...prev, s.value] : prev.filter((v) => v !== s.value),
                    )
                  }
                  className="mt-1 size-4 shrink-0"
                />
                <span className="min-w-0">
                  <span className="block font-mono text-xs text-fg">{s.value}</span>
                  <span className="block text-xs text-fg-muted">
                    {s.label}
                    {s.byDefault ? '' : '（默认不给）'}
                  </span>
                </span>
              </label>
            ))}
          </div>
          <p className="mt-2 text-xs leading-relaxed text-fg-muted">
            少给一个 scope，节点就在对应端点上拿 403。
            <code className="font-mono"> node:status:write </code>
            不在缺省五个里 —— 不给它，节点详情页的负载卡就永远是空的。
          </p>
        </fieldset>

        <div className="mt-4">
          <DangerousAction
            code="D5"
            title="签发新密钥（第 ① 步）"
            submitLabel="签发"
            disabled={blocked !== null}
            disabledReason={blocked}
            context={
              <div className="space-y-2">
                <p>
                  这一步<strong className="font-semibold">不会影响任何正在连接的用户</strong>：
                  旧密钥继续有效，节点也还在用它。危险的是第 ③ 步。
                </p>
                <p className="text-fg-muted">
                  签完之后明文<strong className="font-medium text-fg">只显示这一次</strong>。
                  当前这个节点有 {activeCount} 把有效密钥（上限 {NODE_KEY_MAX_ACTIVE}）。
                </p>
              </div>
            }
            onSubmit={async (values) => {
              const days = EXPIRY_OPTIONS.find((o) => o.value === expiry)?.days ?? null;
              const created = await unwrap(
                api().POST('/api/v1/admin/nodes/{id}/keys', {
                  params: {
                    path: { id: nodeId },
                    // L3 的码走**请求头**，不是 body（§6.2）。
                    header: { 'X-TOTP-Code': values.totp ?? '' },
                  },
                  body: {
                    name: name.trim(),
                    scopes: [...scopes],
                    ...(days === null
                      ? {}
                      : { expires_at: new Date(Date.now() + days * 86_400_000).toISOString() }),
                  },
                }),
              );
              onIssued({ key: created.key, secret: created.secret });
            }}
            onDone={() => setName(defaultKeyName())}
          />
        </div>
      </Card>
    </div>
  );
}

/** 默认密钥名用当月：轮换基本按月做，而空着的名字最后都会变成「新密钥」。 */
function defaultKeyName(now = new Date()): string {
  const y = now.getFullYear();
  const m = String(now.getMonth() + 1).padStart(2, '0');
  return `${y}-${m} 轮换`;
}

/* ────────────────────────── 第 ② 步：等待 ────────────────────────── */

function WaitCard({
  awaiting,
  refreshedAt,
  pollError,
  onRefresh,
}: {
  awaiting: readonly NodeKey[];
  refreshedAt: number | null;
  pollError: ApiError | null;
  onRefresh: () => void;
}) {
  return (
    <Card className={awaiting.length > 0 ? 'border-l-4 border-l-warn' : undefined}>
      <CardTitle hint="D5 第 2 步 · 判据是 last_used_at > created_at">
        ② 等这个节点用新密钥上报
      </CardTitle>

      {awaiting.length === 0 ? (
        <p className="mt-3 text-sm leading-relaxed text-fg-muted">
          没有在等的新密钥 —— 现在有效的密钥要么已经被节点用过（可以作为吊销别的密钥时的见证），
          要么这个节点还没签过密钥。
        </p>
      ) : (
        <>
          <p className="mt-3 text-sm leading-relaxed text-fg">
            有 {awaiting.length} 把密钥<strong className="font-semibold">签发之后还没被这个节点用过</strong>。
            把它配到节点上，然后等最多 60 秒（节点的轮询周期）。
            这一页每 {POLL_INTERVAL_MS / 1000} 秒自己刷一次。
          </p>
          <ul className="mt-3 space-y-2">
            {awaiting.map((k) => (
              <li key={k.id} className="rounded-lg border border-line bg-surface-alt/50 p-3 text-sm">
                <div className="flex flex-wrap items-baseline gap-2">
                  <code className="font-mono text-fg">{k.key_id}</code>
                  <span className="text-fg-muted">{k.name}</span>
                  <Badge tone="warn">还没被用过</Badge>
                </div>
                <p className="mt-1 text-xs text-fg-subtle">
                  签发于 {formatDateTime(k.created_at)}
                  {k.last_used_at
                    ? ` · 最后使用 ${formatDateTime(k.last_used_at)}（早于签发时刻，不算数）`
                    : ' · 从没被使用过'}
                </p>
              </li>
            ))}
          </ul>
          <p className="mt-3 text-xs leading-relaxed text-fg-muted">
            🔴 判据是<strong className="font-medium text-fg"> 最后使用晚于签发时刻</strong>，
            不是「有最后使用时间」。一把很久以前用过、节点早就换走了的密钥不能当见证 ——
            拿它去吊销另一把，正好制造出「节点失联」。
          </p>
        </>
      )}

      <div className="mt-4 flex flex-wrap items-center gap-3">
        <Button onClick={onRefresh}>立刻刷新</Button>
        {refreshedAt !== null ? (
          <span className="text-xs text-fg-subtle">
            上次刷新 {new Date(refreshedAt).toLocaleTimeString('zh-CN')}
          </span>
        ) : null}
        {pollError ? (
          <span className="text-xs text-warn">
            上次刷新失败（{pollError.message}）—— 眼前这份数据是更早那一刻的。
          </span>
        ) : null}
      </div>
    </Card>
  );
}

/* ────────────────────────── 第 ③ 步：列表 + 吊销 ────────────────────────── */

function KeyListCard({
  nodeId,
  nodeName,
  keys,
  onRevoked,
}: {
  nodeId: number;
  nodeName: string;
  keys: readonly NodeKey[];
  onRevoked: () => void;
}) {
  if (keys.length === 0) {
    return (
      <EmptyState
        title="这个节点还没签发过密钥"
        description="节点没有密钥就拉不到配置，也上报不了流量。新建节点后第一件事就是给它发一把 —— 用上面的第 ① 步。"
        action={
          <a href="#issue-key" className="text-sm font-medium text-accent underline underline-offset-4">
            回到上面签发第一把
          </a>
        }
      />
    );
  }

  return (
    <Card>
      <CardTitle hint="D5 第 3 步 · revokeAdminNodeKey（服务端强制两步）">
        ③ {nodeName} 的密钥（{keys.length}）
      </CardTitle>

      <ul className="mt-4 space-y-3">
        {keys.map((key) => (
          <li key={key.id} className="rounded-xl border border-line bg-surface-alt/40 p-4">
            <KeyRow
              nodeId={nodeId}
              nodeName={nodeName}
              keyItem={key}
              witnessed={hasWitnessFor(keys, key)}
              onRevoked={onRevoked}
            />
          </li>
        ))}
      </ul>

      <ContractGapNotice>
        契约里没有跨节点的密钥总表（<code className="font-mono">listAdminNodeKeys</code> 挂在
        <code className="font-mono"> /admin/nodes/&#123;id&#125;/keys</code> 上），
        所以「哪些节点还没轮换过」这个问题在这一页上答不了，只能一台一台看。
      </ContractGapNotice>
    </Card>
  );
}

function KeyRow({
  nodeId,
  nodeName,
  keyItem,
  witnessed,
  onRevoked,
}: {
  nodeId: number;
  nodeName: string;
  keyItem: NodeKey;
  witnessed: boolean;
  onRevoked: () => void;
}) {
  const alive = keyIsActive(keyItem);
  const used = keyUsedSinceIssue(keyItem);

  return (
    <>
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="min-w-0">
          <code className="font-mono text-base font-semibold text-fg">{keyItem.key_id}</code>
          <p className="mt-0.5 text-sm text-fg-muted">{keyItem.name}</p>
        </div>
        <div className="flex shrink-0 flex-wrap items-center gap-2">
          <KeyStatusBadge keyItem={keyItem} />
          {alive ? (
            used ? (
              <Badge tone="ok">节点在用它</Badge>
            ) : (
              <Badge tone="warn">签发后还没被用过</Badge>
            )
          ) : null}
        </div>
      </div>

      <dl className="mt-3 grid grid-cols-2 gap-x-4 gap-y-2 text-sm sm:grid-cols-4">
        <Cell label="签发">{formatDateTime(keyItem.created_at)}</Cell>
        <Cell label="最后使用">
          {keyItem.last_used_at ? (
            <span className={cx(!used && 'text-warn')}>{formatDateTime(keyItem.last_used_at)}</span>
          ) : (
            <span className="text-fg-subtle">—</span>
          )}
        </Cell>
        <Cell label="过期">
          {keyItem.expires_at ? formatDateTime(keyItem.expires_at) : <span className="text-fg-subtle">不过期</span>}
        </Cell>
        <Cell label="吊销">
          {keyItem.revoked_at ? formatDateTime(keyItem.revoked_at) : <span className="text-fg-subtle">—</span>}
        </Cell>
      </dl>

      <p className="mt-2 flex flex-wrap gap-1.5">
        {keyItem.scopes.map((s) => (
          <span key={s} className="rounded bg-surface px-1.5 py-0.5 font-mono text-xs text-fg-muted">
            {s}
          </span>
        ))}
      </p>

      {alive ? (
        <div className="mt-3">
          <DangerousAction
            code="D5"
            title={`吊销 ${keyItem.key_id}（第 ③ 步）`}
            submitLabel="确认吊销"
            disabled={!witnessed}
            disabledReason={
              <>
                这个节点上<strong className="font-medium text-fg">没有另一把「签发后被用过」的有效密钥</strong>
                来接手。现在吊销它，节点在下一次轮询时就会失联，
                而失联之后你没法再让它换密钥 —— 只能上机器手动改。
                服务端也会拒绝（409 <code className="font-mono">STATE_CONFLICT</code>）：
                先做第 ① 步签一把新的，等第 ② 步看到它被用上，再回来。
              </>
            }
            context={
              <div className="space-y-2">
                <p>
                  吊销 <code className="font-mono">{keyItem.key_id}</code>（{nodeName} · #{nodeId}）。
                  这把密钥立刻失效，用它鉴权的请求会拿到 401。
                </p>
                <p className="text-fg-muted">
                  当前有另一把有效且<strong className="font-medium text-fg">签发后被节点用过</strong>的密钥可以接手，
                  所以这一步是安全的。
                </p>
                <p className="text-xs leading-relaxed text-fg-subtle">
                  服务端仍然可能回 409：轮换期节点每 60 秒改一次「最后使用」，
                  另一个管理员也可能同时在吊销另一把。
                  <strong className="font-medium text-fg-muted">那个 409 是对的</strong> ——
                  它是数据库刚刚拒绝了一次会让节点失联的操作，不要重试，先刷新看看现在还有几把有效密钥。
                </p>
              </div>
            }
            onSubmit={async (values) => {
              await unwrapEmpty(
                api().DELETE('/api/v1/admin/node-keys/{key_id}', {
                  params: {
                    path: { key_id: keyItem.key_id },
                    header: { 'X-TOTP-Code': values.totp ?? '' },
                  },
                }),
              );
            }}
            onDone={onRevoked}
          />
        </div>
      ) : null}
    </>
  );
}

function KeyStatusBadge({ keyItem }: { keyItem: NodeKey }) {
  // 吊销与过期分开：前者是有人做过一个动作（审计里查得到是谁），
  // 后者是时间到了。合成一个「失效」会让前一种情况没法被追问。
  if (keyItem.revoked_at) return <Badge tone="neutral">已吊销</Badge>;
  if (!keyIsActive(keyItem)) return <Badge tone="warn">已过期</Badge>;
  return <Badge tone="ok">有效</Badge>;
}

function Cell({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="min-w-0">
      <dt className="text-xs text-fg-subtle">{label}</dt>
      <dd className="mt-0.5 truncate text-fg">{children}</dd>
    </div>
  );
}

/* ────────────────────────── query 形态的 token ────────────────────────── */

/**
 * 这张卡原本写的是「过渡态，全量切换前必须关闭」。
 * **那个说法已经被实测推翻**，但原文保留在这里 —— 它记录了我们当初的判断，
 * 而删掉它会让「我们曾经以为这是暂时的」这件事消失。
 */
function QueryTokenCard() {
  return (
    <Card>
      <CardTitle>query 形态的 token</CardTitle>

      <div className="mt-3 rounded-lg border border-warn/30 bg-warn/10 p-3 text-sm leading-relaxed text-warn">
        <p className="font-medium">原文（2026-08 之前的判断）：这是有期限的过渡态，不是目标态。</p>
        <p className="mt-1">
          v2node 很可能把 token 挂在 query string 上而不发 Authorization 头。
          过渡期允许，但三条约束：query 形态也必须是每节点独立密钥；每次经 query 认证写一条
          结构化日志（带 key_id），使其可见可计数；
          <strong className="font-semibold">全量切换前必须关闭</strong>。
        </p>
      </div>

      <div className="mt-3 rounded-lg border border-line bg-surface-alt p-3 text-sm leading-relaxed text-fg">
        <p className="font-medium">
          🔴 2026-08-17 读过 v2node 源码之后，上面第三条不成立了。
        </p>
        <p className="mt-1 text-fg-muted">
          v2node 用 <code className="font-mono">SetQueryParams(&#123;node_type, node_id, token&#125;)</code> 鉴权，
          <strong className="font-medium text-fg">全仓没有任何一处为鉴权设置 Authorization 头，也没有开关可切换</strong>
          （证据：<code className="font-mono">docs/evidence/v2node-contract-20260817</code>，
          实现见 <code className="font-mono">api/internal/middleware/node.go</code>）。
          所以 query token 不是「等实测确认后关掉」的过渡态，而是<strong className="font-medium text-fg">当前唯一可行的形态</strong>：
          退出它需要给 v2node 提 PR 或自己 fork。
          <code className="font-mono"> AllowQueryToken </code>的默认值 true 是强制的。
        </p>
        <p className="mt-1 text-fg-muted">
          前两条约束仍然成立，而且已经落地：query 形态<strong className="font-medium text-fg">也是</strong>每节点独立密钥
          （<code className="font-mono">node_id</code> 从密钥推导，请求里带的一律忽略），
          每次经 query 认证都写一条带{' '}
          <code className="font-mono">key_prefix</code> 的结构化日志。
        </p>
      </div>

      <ContractGapNotice>
        <strong className="font-medium text-fg">「最近 24 小时经 query 认证的次数」这一页给不出来。</strong>
        那条日志是 Cloud Logging 里的结构化日志（<code className="font-mono">「节点使用 query string 凭据」</code>，
        monitoring 侧按它建了 log-based metric），
        <strong className="font-medium text-fg">契约里没有任何端点返回它</strong> ——
        审计端点记的是管理员动作，不是节点鉴权。要看数就去 Cloud Logging 按那条 metric 查。
        补法是加一个只读的运维指标端点。
      </ContractGapNotice>
    </Card>
  );
}

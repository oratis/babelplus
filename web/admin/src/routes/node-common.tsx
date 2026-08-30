/**
 * 后台「节点与密钥」三页（`/admin/nodes`、`/admin/nodes/:id`、`/admin/node-keys`）共用的东西。
 *
 * 为什么单开一个文件，而不是塞进 `@babelplus/shared/src/ui` 或 `components/`：
 * 那两处是全站公共资产，多个人同时接线会撞在同一个文件上；而这里的每一样都带着
 * **节点域特有的产品约束**（page-inventory §4.3 模块 5/6、api-contract §6.1），
 * 拿到订单页或用户页上既用不着也会误导。
 *
 * 🔴 **不跨包 import 用户面板。** `web/user/src/routes/ticket-common.tsx` 里有形态几乎相同的
 * `useApiQuery` / `QueryErrorState` / 文案表 —— 这里是**照着它重写一份**，不是引用它。
 * 两个包各自独立构建、独立部署在不同主域名上（page-inventory §4.1 第一道闸），
 * 一个 import 就把两套故障域焊在一起了。重复的代价是两处要各改一次；
 * 共享的代价是用户面板的一次改动能改掉后台的行为 —— 后者贵得多。
 */
import {
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
  type ReactNode,
} from 'react';
import { ApiError, unwrapWithMeta, type Meta, type components } from '@babelplus/shared/api';
import { Badge, Card, ErrorState, cx } from '@babelplus/shared/ui';
import { api } from '../lib/api.ts';
import { dangerOps } from '../lib/danger.ts';

/* ────────────────────────── 契约类型 ────────────────────────── */

export type AdminNode = components['schemas']['AdminNode'];
export type AdminNodeUpsert = components['schemas']['AdminNodeUpsert'];
export type NodeKey = components['schemas']['NodeKey'];
export type NodeScope = components['schemas']['NodeScope'];

/**
 * `servers.protocol` 的四个值（migration 0001 的 `server_protocol` 枚举）。
 *
 * ⚠️ 契约里 `AdminNodeUpsert.type` 只是 `string`，枚举没有进 openapi，
 * 所以这张表是**手抄**的第二份来源，改枚举时这里不会自动跟着变。
 * 抄它仍然比留一个自由文本框好：服务端对不认识的 type 回 422，
 * 而 422 的 message 里那句「不接受 vless 这类折叠名」说明这正是人会打错的地方
 * （`validateNodeUpsert` 的 `parseNodeProtocol` 分支）。
 */
export const NODE_PROTOCOLS: ReadonlyArray<{ readonly value: string; readonly label: string }> = [
  { value: 'vless_reality', label: 'vless_reality · REALITY 主力（TCP:443）' },
  { value: 'hysteria2', label: 'hysteria2 · 加速通路（UDP:443）' },
  { value: 'shadowsocks2022', label: 'shadowsocks2022 · 兜底' },
  { value: 'vless_xhttp_cdn', label: 'vless_xhttp_cdn · 应急（默认关闭）' },
];

/** 签发密钥时可选的六个 scope（契约 `NodeScope`，**精确匹配非前缀**）。 */
export const NODE_SCOPES: ReadonlyArray<{
  readonly value: NodeScope;
  readonly label: string;
  /** 是否在缺省五个里。`node:status:write` 不在，见 `nodeKeyDefaultScopes` 的注释。 */
  readonly byDefault: boolean;
}> = [
  { value: 'node:config:read', label: '读配置（拉 /config）', byDefault: true },
  { value: 'node:users:read', label: '读用户列表', byDefault: true },
  { value: 'node:traffic:write', label: '上报流量', byDefault: true },
  { value: 'node:alive:write', label: '上报在线', byDefault: true },
  { value: 'node:alive:read', label: '读在线态', byDefault: true },
  { value: 'node:status:write', label: '上报负载（cpu / 内存 / 磁盘）', byDefault: false },
];

/* ────────────────────────── 请求的三态 ────────────────────────── */

export type QueryState = 'loading' | 'ready' | 'error';

export interface ApiQuery<T> {
  readonly state: QueryState;
  readonly data: T | null;
  readonly error: ApiError | null;
  /** 重新发一次。错误态的「重试」按钮用。 */
  reload(): void;
  /**
   * 就地改已加载的数据。
   *
   * 存在的理由是三态纪律：启用/停用成功后**不能**把整张列表打回 loading 重拉 ——
   * 操作者刚在一台出事的机器上点了「停用」，眼前的表突然变成骨架屏，
   * 会让人以为操作没生效而再点一次。写操作拿到服务端返回的实体后直接补进来。
   */
  patch(update: (previous: T) => T): void;
}

/**
 * 一个请求 = 一套三态。**刻意不引缓存层** —— `shared/api/queries.ts` 的文件头写着
 * 缓存与状态管理的选型还没裁决，在这里引一个等于替以后的人做决定。
 *
 * `run` 不要求调用方 memo：用 ref 取最新的那一份，重跑与否只由 `deps` 决定。
 * 要求 memo 的话，每个调用点都得包一层 `useCallback`，而漏包的表现是**死循环请求**。
 */
export function useApiQuery<T>(
  run: () => Promise<T>,
  deps: readonly unknown[],
  fallbackMessage = '加载失败',
): ApiQuery<T> {
  const [nonce, setNonce] = useState(0);
  const [state, setState] = useState<QueryState>('loading');
  const [data, setData] = useState<T | null>(null);
  const [error, setError] = useState<ApiError | null>(null);

  const runRef = useRef(run);
  runRef.current = run;

  useEffect(() => {
    let alive = true;
    setState('loading');
    setError(null);
    void runRef
      .current()
      .then((value) => {
        if (!alive) return;
        setData(value);
        setState('ready');
      })
      .catch((cause: unknown) => {
        if (!alive) return;
        // 迟到的响应不许覆盖新一轮的状态，所以先判 alive 再 set。
        setError(asApiError(cause, fallbackMessage));
        setState('error');
      });
    return () => {
      alive = false;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [...deps, nonce]);

  const reload = useCallback(() => setNonce((n) => n + 1), []);
  const patch = useCallback(
    (update: (previous: T) => T) => setData((prev) => (prev === null ? prev : update(prev))),
    [],
  );

  return { state, data, error, reload, patch };
}

/** 任何 catch 到的东西 → `ApiError`。`status = 0` 会被归一成 `kind = 'offline'`。 */
export function asApiError(cause: unknown, fallbackMessage: string): ApiError {
  if (cause instanceof ApiError) return cause;
  return new ApiError({ status: 0, code: 'UNKNOWN', message: fallbackMessage, cause });
}

/* ────────────────────────── 分页 ────────────────────────── */

/**
 * 一页多少条。**不做无限滚动** ——「加载更多」是可以停下来的，滚动不是；
 * 而这一页上每一行都挂着一个能让人掉线的按钮，翻过头再往回找是实打实的成本。
 */
export const NODE_PAGE_SIZE = 50;

export interface NodePage {
  readonly items: readonly AdminNode[];
  readonly meta: Meta;
}

/**
 * 拉一页节点。
 *
 * ⚠️ **`count` 只在第一页传。** 管理面允许返总数（`?count=true` → `meta.total`，
 * 契约原话「仅管理面提供」），但它是一次实打实的 `COUNT(*)`，
 * 在 `db-f1-micro`（0.6 GiB RAM）上不能让每次翻页都付。
 * 这也是管理面与用户面的口径差别：用户面**永不**返 total。
 */
export function listAdminNodesPage(cursor: string | null, count: boolean): Promise<NodePage> {
  const query = {
    limit: NODE_PAGE_SIZE,
    ...(cursor === null ? {} : { cursor }),
    ...(count ? { count: true } : {}),
  };
  return unwrapWithMeta(api().GET('/api/v1/admin/nodes', { params: { query } })).then((envelope) => ({
    items: envelope.data,
    meta: envelope.meta,
  }));
}

/**
 * 「加载更多」的状态机。三页里有两处要翻页（节点列表、密钥页的节点选择器），
 * 各写一遍必然会有一处把判据写成「这一页返回的条数等于 limit」——
 * 那在总数整除时会判出一页空数据，而空页在前端长得像加载失败。
 *
 * 🔴 唯一判据是 `meta.next_cursor`。
 */
export function useMorePages(first: NodePage | null): {
  readonly items: readonly AdminNode[];
  readonly meta: Meta | null;
  readonly pending: boolean;
  readonly error: ApiError | null;
  readonly canLoadMore: boolean;
  loadMore(): Promise<void>;
  reset(): void;
} {
  const [more, setMore] = useState<readonly AdminNode[]>([]);
  const [moreMeta, setMoreMeta] = useState<Meta | null>(null);
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<ApiError | null>(null);

  const meta = moreMeta ?? first?.meta ?? null;
  const items = useMemo(
    () => (first === null ? [] : [...first.items, ...more]),
    [first, more],
  );

  const reset = useCallback(() => {
    setMore([]);
    setMoreMeta(null);
    setError(null);
  }, []);

  const cursor = meta?.next_cursor ?? null;

  const loadMore = useCallback(async (): Promise<void> => {
    if (pending || !cursor) return;
    setPending(true);
    setError(null);
    try {
      const page = await listAdminNodesPage(cursor, false);
      setMore((prev) => [...prev, ...page.items]);
      setMoreMeta(page.meta);
    } catch (cause) {
      setError(asApiError(cause, '没能加载更多节点'));
    } finally {
      setPending(false);
    }
  }, [cursor, pending]);

  return { items, meta, pending, error, canLoadMore: Boolean(cursor), loadMore, reset };
}

/* ────────────────────────── 错误分支 ────────────────────────── */

/**
 * 501 的判据。
 *
 * `NOT_IMPLEMENTED` **不在 openapi 的 `ErrorCode` enum 里**（它由错误映射层直接写出去），
 * 所以只能按字符串比。按状态码判也行，但那会把将来任何一个真的 501 也算进来 ——
 * 「端点没写」和「端点写了但依赖不可用」对操作者是两句不同的话。
 *
 * ⚠️ 节点域的十个 operation **目前全部已实现**，所以这条分支在这三页上正常永远不会命中。
 * 留着不是防御性编程：它是回滚保险 —— 后端若因故把某一条摘回 501，
 * 这三页会说「还没上线」而不是把它渲染成一次故障，从而不产生一轮无用的排查。
 */
export const NOT_IMPLEMENTED_CODE = 'NOT_IMPLEMENTED';

export function isNotImplemented(error: ApiError | null | undefined): boolean {
  return error?.code === NOT_IMPLEMENTED_CODE;
}

/**
 * **读**请求失败时的 `ErrorCode` → 文案。
 *
 * 与 `components/DangerousAction.tsx` 的 `dangerErrorCopy` 分开，不是重复：
 * 那一份写给**提交**（「不是你的填写有问题」「等验证器跳到下一个码」），
 * 拿到一次列表加载失败上说这些话是答非所问。同一个码在两个语境下确实该说两句不同的话。
 *
 * 🔴 按 `code` 分支，不按 HTTP 状态码分支（api-contract §2.3：禁止匹配 `message` 做分支）。
 */
export function nodeQueryErrorCopy(
  error: ApiError,
  what: string,
): { title: string; description: string } {
  switch (error.code) {
    case NOT_IMPLEMENTED_CODE:
      return {
        title: '这一部分还没上线',
        description: `${what}的后端还没实现。不是你的操作有问题，重试也不会有变化。`,
      };
    case 'RESOURCE_NOT_FOUND':
      return {
        title: '找不到这个对象',
        description: '它可能刚被别人删掉了，或者地址栏里的 id 不对。回列表页重新进一次。',
      };
    case 'AUTH_PERMISSION_DENIED':
      return {
        title: '这个管理员账号读不了这里',
        description:
          '身份已经通过 IAP，缺的是角色或权限位。重新登录、换浏览器都不会改变这个结果 —— 需要有人给你开。',
      };
    case 'VALIDATION_FAILED':
    case 'VALIDATION_MALFORMED_BODY':
      return { title: '服务端退回了这次查询', description: fieldReasons(error) ?? error.message };
    case 'QUOTA_RATE_LIMITED':
      return {
        title: '请求太频繁',
        description:
          error.retryAfterSeconds === undefined
            ? '稍后再试。'
            : `${error.retryAfterSeconds} 秒后可以再试。`,
      };
    default:
      break;
  }
  switch (error.kind) {
    case 'offline':
      return {
        title: '请求没到达服务端',
        description:
          '后台不做备用域名故障转移（多一个入口就是多一个要防护的入口），所以这里只能重试。' +
          '若你在大陆，先确认自己的出网路径还活着 —— IAP 要求 Google 身份。',
      };
    case 'server':
      return { title: '服务端出错了', description: '不是你的操作有问题。稍后再试，并把请求号一起报出来。' };
    case 'unauthorized':
      return {
        title: '准入状态变了',
        description: '页面顶部的横幅会说明是平台层（IAP）还是应用层拒绝 —— 两者的处置完全不同。',
      };
    default:
      return { title: `${what}没能加载`, description: error.message };
  }
}

function fieldReasons(error: ApiError): string | null {
  const details = error.details;
  if (!details || details.length === 0) return null;
  return details.map((d) => `${d.field}：${d.reason}`).join('；');
}

/**
 * 501 专用的提示块。
 *
 * **刻意不用 `ErrorState`**：501 的 `kind` 是 `server`，而 `ErrorState` 在 server 态下会说
 * 「我们这边出了问题」并把人推去状态页 —— 状态页上一切正常，看的人只会更困惑。
 * 「还没做」不是故障。
 */
export function NotImplementedNotice({
  what,
  requestId,
}: {
  what: string;
  requestId?: string | undefined;
}) {
  return (
    <Card className="border-dashed">
      <h3 className="text-base font-semibold text-fg">尚未开放</h3>
      <p className="mt-1.5 text-sm leading-relaxed text-fg-muted">
        {what}的后端还没实现。不是你的操作有问题，重试也不会有变化。
      </p>
      {requestId ? <p className="mt-3 font-mono text-xs text-fg-subtle">请求号 {requestId}</p> : null}
    </Card>
  );
}

/** 读请求失败时的整块错误态。501 走上面那块，其余走全站统一的 `ErrorState`。 */
export function QueryErrorState({
  error,
  what,
  onRetry,
}: {
  error: ApiError;
  what: string;
  onRetry?: () => void;
}) {
  if (isNotImplemented(error)) {
    return <NotImplementedNotice what={what} requestId={error.requestId} />;
  }
  const copy = nodeQueryErrorCopy(error, what);
  return (
    <ErrorState
      kind={error.kind}
      title={copy.title}
      description={copy.description}
      requestId={error.requestId}
      onRetry={onRetry}
    />
  );
}

/** 写操作（非危险操作那条路径）失败时的行内错误位。 */
export function FormAlert({ children }: { children: ReactNode }) {
  return (
    <div role="alert" className="rounded-lg border border-danger/30 bg-danger/10 px-3 py-2 text-sm text-danger">
      {children}
    </div>
  );
}

/* ────────────────────────── 节点的可判定事实 ────────────────────────── */

/**
 * 「最后上报」多久算掉队。
 *
 * 判据的基准是**节点侧的 60 秒轮询**（system-design §8：配置下发是 60s 轮询）。
 * 取 5 倍（300 秒）而不是 1–2 倍：一次网络抖动或一次 Cloud Run 冷启动就能吃掉两个周期，
 * 按 2 倍标黄会让这一列在正常日子里也是黄的 —— 而一个长期发黄的告警等于没有告警。
 */
export const NODE_PUSH_INTERVAL_SECONDS = 60;
export const NODE_STALE_AFTER_SECONDS = NODE_PUSH_INTERVAL_SECONDS * 5;

export type NodeHealth = 'never' | 'stale' | 'fresh';

/**
 * 由 `last_push_at` 判健康。
 *
 * 🔴 **从没上报过（`never`）与上报超时（`stale`）必须分开。**
 * 前者多半是「密钥还没发」或「机器还没起」，后者是「它本来在跑，现在联系不上」——
 * 处置完全不同，混成一个「异常」会让人在一台从来没配过的新机器上排查网络。
 */
export function nodeHealth(node: AdminNode, now = Date.now()): NodeHealth {
  if (!node.last_push_at) return 'never';
  const at = Date.parse(node.last_push_at);
  if (Number.isNaN(at)) return 'never';
  return now - at > NODE_STALE_AFTER_SECONDS * 1000 ? 'stale' : 'fresh';
}

export function NodeHealthBadge({ health }: { health: NodeHealth }) {
  switch (health) {
    case 'fresh':
      return <Badge tone="ok">在上报</Badge>;
    case 'stale':
      // 「超过 5 分钟没上报」而不是「异常」：说出判据，看的人才知道这是不是误报。
      return <Badge tone="warn">超过 5 分钟没上报</Badge>;
    default:
      return <Badge tone="neutral">从没上报过</Badge>;
  }
}

export function NodeEnabledBadge({ enabled }: { enabled: boolean }) {
  // 「停用中」而不是「已关闭」：停用是一个持续的状态，且它意味着这台机器上没有人。
  return enabled ? <Badge tone="ok">启用中</Badge> : <Badge tone="neutral">停用中</Badge>;
}

/* ────────────────────────── 密钥的可判定事实 ────────────────────────── */

/**
 * 这把密钥现在有效吗。
 *
 * **与服务端 `AdminListNodeKeys` 里那个计算列 `active` 逐字同口径**：
 * `revoked_at IS NULL AND (expires_at IS NULL OR expires_at > now())`。
 * 只看 `revoked_at` 不够：过期与吊销在契约里是两个字段，判定必须两个都看，
 * 否则一把过期密钥会在界面上显示成「有效」，而它已经不能给节点鉴权了。
 */
export function keyIsActive(key: NodeKey, now = Date.now()): boolean {
  if (key.revoked_at) return false;
  if (!key.expires_at) return true;
  const exp = Date.parse(key.expires_at);
  return Number.isNaN(exp) ? true : exp > now;
}

/**
 * 这把密钥**在签发之后**被节点真的用过吗 —— D5 第 2 步的唯一判据。
 *
 * 🔴 与服务端的 `used_since_issue` 同口径：`last_used_at > issued_at`
 * （契约把 `issued_at` 映射成了 `created_at`，见 `nodeKeyView`）。
 * **只看 `last_used_at IS NOT NULL` 不够严**：那把密钥可能是很久以前用过、
 * 节点后来早就换走了 —— 拿它当见证去吊销另一把，正好制造出「节点失联」。
 */
export function keyUsedSinceIssue(key: NodeKey): boolean {
  if (!key.last_used_at) return false;
  const used = Date.parse(key.last_used_at);
  const issued = Date.parse(key.created_at);
  if (Number.isNaN(used) || Number.isNaN(issued)) return false;
  return used > issued;
}

/**
 * 吊销 `target` 时，这个节点上有没有「见证密钥」。
 *
 * 与服务端 `AdminGetNodeKeyByPrefix` 的 `witness_count` 子查询同口径：
 * 存在另一把（`id <> target.id`）**有效**且**签发后被用过**的密钥。
 *
 * 🔴 **这个函数不是闸，是望远镜。** 真正的拒绝在数据库那条 UPDATE 的 `EXISTS` 里
 * （`AdminRevokeNodeKeyTwoStep`），前端算这一份只是为了把「现在能不能进第 3 步」
 * 显示给操作者看。两处判断之间必然有窗口 —— 轮换期节点每 60 秒改一次 `last_used_at`，
 * 另一个管理员可能同时在吊销另一把 —— 所以即使这里说「可以」，服务端仍可能回 409，
 * 而那个 409 是**正确的**。界面必须原样显示它，不能把它当成 bug 去重试。
 */
export function hasWitnessFor(keys: readonly NodeKey[], target: NodeKey, now = Date.now()): boolean {
  return keys.some((k) => k.id !== target.id && keyIsActive(k, now) && keyUsedSinceIssue(k));
}

export function activeKeys(keys: readonly NodeKey[], now = Date.now()): readonly NodeKey[] {
  return keys.filter((k) => keyIsActive(k, now));
}

/** data-model §8.3 的应用层规则：同一节点**同时有效 ≤ 2 把**。签发第 3 把服务端回 422。 */
export const NODE_KEY_MAX_ACTIVE = 2;

/* ────────────────────────── 版面零件 ────────────────────────── */

/**
 * 模块顶部的危险操作清单。
 *
 * 从 `ModuleScaffold` 里搬过来的 —— 那个脚手架整体已经不适用于接完线的页面
 *（它会印一条「这一页还没接线」的 `NotWiredNotice`），但**这一块要留**：
 * 它是 page-inventory §4.4 那张表在页面上的露出，
 * 让「这一页上有什么会让人掉线的按钮」在点开任何东西之前就已经写在屏幕上。
 */
export function DangerSummary({ codes }: { codes: readonly string[] }) {
  const ops = dangerOps(codes);
  if (ops.length === 0) return null;
  return (
    <ul className="mb-5 space-y-2">
      {ops.map((op) => (
        <li key={op.code} className="rounded-lg border border-danger/30 bg-danger/5 p-3 text-xs leading-relaxed">
          <div className="flex flex-wrap items-baseline gap-2">
            <span className="rounded bg-danger/15 px-1.5 py-0.5 font-mono font-semibold text-danger">
              {op.code}
            </span>
            <span className="font-medium text-fg">{op.title}</span>
          </div>
          <p className="mt-1 text-fg-muted">危害：{op.harm}</p>
          <p className="mt-1 flex flex-wrap gap-x-3 gap-y-1 text-fg-subtle">
            <span>审计（改前值 / 改后值）</span>
            {op.reason ? <span>必填原因</span> : null}
            {op.confirmString ? <span>🔒 输入{op.confirmString}</span> : null}
            {op.notify ? <span>📧 通知受影响用户</span> : null}
            {op.separatePerm ? <span>独立权限位（默认不授予）</span> : null}
          </p>
          {op.extra ? <p className="mt-1 text-fg-muted">额外：{op.extra}</p> : null}
        </li>
      ))}
    </ul>
  );
}

/**
 * 「契约里没有这个字段」的显式声明。
 *
 * 存在的理由与 `NotWiredNotice` 相同，但说的是另一件事：不是「还没接」，
 * 而是**接不了** —— 冻结的契约里没有这个字段。写成组件而不是删掉那一列，
 * 是因为 page-inventory §4.3 明写了这一列该有；悄悄不显示会让缺口从评审视野里消失，
 * 而下一个人只会以为「产品文档写多了」。
 */
export function ContractGapNotice({ children }: { children: ReactNode }) {
  return (
    <div className="rounded-lg border border-dashed border-warn/40 bg-warn/5 p-3 text-xs leading-relaxed text-fg-muted">
      {children}
    </div>
  );
}

/** 缺字段时的占位。**不显示 0，也不显示「正常」** —— 见 `adminNodeView` 里同一条理由。 */
export function Unknown({ title }: { title: string }) {
  return (
    <span className="text-fg-subtle" title={title}>
      —
    </span>
  );
}

/* ────────────────────────── 表单原语 ────────────────────────── */

// 16px 起步（`text-base`）：iOS Safari 在 <16px 的输入框获得焦点时会自动放大页面，
// 放大后 375px 布局就出现横向滚动。节点两页是 **M2**（手机上要能完成紧急停用），
// 所以这一条在这里不是可选项。
const CONTROL =
  'w-full rounded-lg border border-line bg-surface px-3 text-base text-fg ' +
  'placeholder:text-fg-subtle focus-visible:border-accent focus-visible:outline-2 ' +
  'focus-visible:outline-offset-1 focus-visible:outline-accent disabled:opacity-50';

export function Field({
  id,
  label,
  hint,
  children,
}: {
  id: string;
  label: string;
  hint?: ReactNode;
  children: ReactNode;
}) {
  return (
    <div>
      <label htmlFor={id} className="mb-1.5 block text-sm font-medium text-fg">
        {label}
      </label>
      {children}
      {hint ? <p className="mt-1.5 text-xs leading-relaxed text-fg-muted">{hint}</p> : null}
    </div>
  );
}

export function TextField({
  id,
  label,
  value,
  onChange,
  hint,
  placeholder,
  disabled,
  mono,
  inputMode,
}: {
  id: string;
  label: string;
  value: string;
  onChange: (value: string) => void;
  hint?: ReactNode;
  placeholder?: string;
  disabled?: boolean;
  mono?: boolean;
  inputMode?: 'numeric' | 'text';
}) {
  return (
    <Field id={id} label={label} hint={hint}>
      <input
        id={id}
        name={id}
        value={value}
        placeholder={placeholder}
        disabled={disabled}
        inputMode={inputMode}
        autoComplete="off"
        spellCheck={false}
        onChange={(event) => onChange(event.target.value)}
        className={cx(CONTROL, 'min-h-11', mono ? 'font-mono' : null)}
      />
    </Field>
  );
}

export function SelectField({
  id,
  label,
  value,
  onChange,
  options,
  hint,
  disabled,
}: {
  id: string;
  label: string;
  value: string;
  onChange: (value: string) => void;
  options: ReadonlyArray<{ readonly value: string; readonly label: string }>;
  hint?: ReactNode;
  disabled?: boolean;
}) {
  return (
    <Field id={id} label={label} hint={hint}>
      <select
        id={id}
        name={id}
        value={value}
        disabled={disabled}
        onChange={(event) => onChange(event.target.value)}
        className={cx(CONTROL, 'min-h-11')}
      >
        {options.map((o) => (
          <option key={o.value} value={o.value}>
            {o.label}
          </option>
        ))}
      </select>
    </Field>
  );
}

/**
 * 复制按钮。
 *
 * `navigator.clipboard` 在非安全上下文与部分浏览器里**不存在** ——
 * 直接调用会抛 TypeError，按钮看起来像「点了没反应」。
 * 在密钥页这条失败路径格外贵：明文只出现一次，一次静默的复制失败 = 一把发不出去的密钥。
 * 所以失败时明说「请手动选中」，而不是什么都不做。
 */
export function CopyButton({ value, label }: { value: string; label: string }) {
  const [state, setState] = useState<'idle' | 'ok' | 'manual'>('idle');

  async function copy(): Promise<void> {
    try {
      await navigator.clipboard.writeText(value);
      setState('ok');
    } catch {
      setState('manual');
    }
  }

  return (
    <span className="inline-flex flex-wrap items-center gap-1.5">
      <button
        type="button"
        onClick={() => void copy()}
        className="inline-flex min-h-9 items-center gap-1.5 rounded-lg border border-line bg-surface px-3 text-xs font-medium text-fg hover:bg-surface-alt"
      >
        {label}
      </button>
      {state === 'ok' ? <span className="text-xs text-ok">已复制</span> : null}
      {state === 'manual' ? (
        <span className="text-xs text-warn">这个浏览器不让自动复制，请手动选中上面那串</span>
      ) : null}
    </span>
  );
}

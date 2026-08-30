/**
 * 模块 8 · 工单处理 `/admin/tickets` —— P1 / **M2**（手机上要能完成回复）。
 *
 * 这一页顺带是模块 8 两页共用的**词表**所在地（状态 / 分类 / 等级怎么说、三态 hook、
 * 读路径的 `ErrorCode` 文案）。为什么不另开一个 `ticket-common.tsx`：
 * 只有两页共用，而 `catalog-common` / `node-common` / `ops-common` 各自服务四五页才让
 * 一个额外文件划算；队列是模块 8 的入口，词表放在入口、由详情页 import，方向与阅读顺序一致。
 *
 * ⚠️ **不跨包 import 用户面板**的 `user/src/routes/ticket-common.tsx`。那边有形状几乎相同的
 * `useApiQuery` 与状态词表，但两个 SPA 是两套故障域（`lib/api.ts` 文件头），
 * 一个 import 就把它们焊在一起了。更要紧的是**词不一样**：同一个 `pending`，
 * 用户面说「待你补充」，后台必须说「待用户补充」—— 合并的第一天就得加一个参数来分叉。
 *
 * # 🔴 这一页做不到的三件事，全是契约缺口，不是没做完
 *
 * 1. **从队列点进会话** —— `Ticket` 只有 `public_id`（'BP-7K2M9Q'），
 *    而 `GET /admin/tickets/{id}` 的路径参数是**数字主键**（`IdPath: number`，
 *    服务端 `AdminGetTicketDetail(ctx, ticketID int64)`）。列表里没有那个数字，
 *    于是**每一行都不能是链接**。页面给了一个「按数字 id 打开」的入口顶着，
 *    并把缺口写在明处 —— 把 `public_id` 塞进那个路径会得到一个请求校验层的 400，
 *    而那看起来像「这个工单不存在」。
 * 2. **按 SLA 剩余排序** —— 契约里没有 SLA 字段，端点也没有排序参数
 *    （服务端固定按 `created_at DESC` 分页）。后端另有一条按优先级排的工作台查询
 *    `ListTicketQueue`，但**没有对应的 HTTP 端点**（`admin_catalog.go` 的注释写明了
 *    这是两条查询、两条都要）。这一页因此只能对**已加载的那几页**做客户端排序，
 *    并且必须说出来 —— 一个只排了三页的队列如果自称「按 SLA 排序」，
 *    看它的人会以为第一行就是最该处理的那一张。
 * 3. **看是谁提的单** —— 列表行里没有任何用户字段（`user_email` 只在详情里）。
 *
 * 这三条都用页面上的说明块顶着，不是靠这段注释：注释只有改代码的人会读。
 */
import { useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from 'react';
import { useNavigate } from 'react-router';
import { ApiError, unwrapWithMeta, type Meta, type components } from '@babelplus/shared/api';
import { PageHeader } from '@babelplus/shared/ui';
import {
  Badge,
  Button,
  Card,
  CardTitle,
  EmptyState,
  ErrorState,
  Icon,
  LinkButton,
  Skeleton,
  cx,
  formatDateTime,
} from './_imports.ts';
import { api } from '../lib/api.ts';

/* ────────────────────────── 契约类型（以 schema.d.ts 为准） ────────────────────────── */

export type AdminTicket = components['schemas']['Ticket'];
export type TicketStatus = components['schemas']['TicketStatus'];
export type TicketCategory = components['schemas']['TicketCategory'];

/* ────────────────────────────── 请求三态 ────────────────────────────── */

export type QueryState = 'loading' | 'ready' | 'error';

export interface TicketQuery<T> {
  readonly state: QueryState;
  readonly data: T | null;
  readonly error: ApiError | null;
  /** 重新发一次。错误态的「重试」与写操作之后的刷新都用它。 */
  reload(): void;
  /**
   * 就地改已加载的数据，**不重新发请求**。
   *
   * 存在的理由是三态纪律：回复成功后不能把整段会话打回骨架屏 ——
   * 客服刚打完一段字，眼前的内容突然消失，第一反应是「是不是没发出去」，
   * 然后再发一遍。工单里发两遍同样的话，用户看到的是我们乱了。
   */
  patch(update: (previous: T) => T): void;
}

/**
 * 一个请求 = 一套三态。**刻意不引缓存层** —— `shared/api/queries.ts` 的文件头写明
 * 缓存与状态管理的选型还没裁决，现在装一个等于替以后的人做决定。
 *
 * `run` 不要求调用方 memo：用 ref 取最新的那一份，重跑与否**只由 `deps` 决定**。
 * 要求 memo 的话每个调用点都得包一层 `useCallback`，而漏包的表现是**死循环请求** ——
 * 在后台意味着一页打开就开始刷服务端，而 `db-f1-micro` 的连接池每实例只有 2 条。
 */
export function useTicketQuery<T>(
  run: () => Promise<T>,
  deps: readonly unknown[],
  fallbackMessage = '加载失败',
): TicketQuery<T> {
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
        // 迟到的响应不许覆盖新一轮的状态：先判 alive 再 set。
        if (!alive) return;
        setData(value);
        setState('ready');
      })
      .catch((cause: unknown) => {
        if (!alive) return;
        setData(null);
        setError(asTicketError(cause, fallbackMessage));
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
export function asTicketError(cause: unknown, fallbackMessage: string): ApiError {
  if (cause instanceof ApiError) return cause;
  return new ApiError({ status: 0, code: 'UNKNOWN', message: fallbackMessage, cause });
}

/* ────────────────────────────── 词表 ────────────────────────────── */

/**
 * 四个状态。
 *
 * 🔴 说法与用户面板**故意不同**：客服要知道的是**球在谁那边**。
 * `pending` 在用户那边是「待你补充」，在这里必须是「待用户补充」——
 * 两边都写「待补充」的话，客服会把一张正在等用户回话的单当成自己欠着的。
 *
 * ⚠️ `replied` **不是一个可以设置的状态**：库里没有这个值，它由
 * 「客服最后回复时间晚于用户最后回复时间」算出来（`adminTicketStatusToDB` 对它回 422）。
 * 详情页的状态下拉里因此没有它，见 `SETTABLE_STATUSES`。
 */
const STATUS_META: Record<TicketStatus, { label: string; tone: 'neutral' | 'ok' | 'warn' | 'info' }> = {
  open: { label: '待客服回复', tone: 'warn' },
  pending: { label: '待用户补充', tone: 'info' },
  replied: { label: '客服已回复', tone: 'ok' },
  closed: { label: '已关闭', tone: 'neutral' },
};

export function statusLabel(status: TicketStatus): string {
  return STATUS_META[status]?.label ?? status;
}

export function StatusBadge({ status }: { status: TicketStatus }) {
  const meta = STATUS_META[status] ?? { label: status, tone: 'neutral' as const };
  return <Badge tone={meta.tone}>{meta.label}</Badge>;
}

/** 契约给的四个分类。它同时决定了建单时弹给用户看的是哪一篇排障文档。 */
const CATEGORY_LABEL: Record<TicketCategory, string> = {
  subscription: '订阅问题',
  'node-down': '连不上 / 速度',
  billing: '支付与账单',
  account: '账号本身',
};

export function categoryLabel(category: TicketCategory): string {
  return CATEGORY_LABEL[category] ?? category;
}

/**
 * 等级 ↔ 优先级。1 低 / 2 普通 / 3 高 / 4 紧急，与服务端 `ticketPriorityFromLevel` 一一对应
 * （超出这四个值服务端回 422，所以下拉框里也只有这四个）。
 */
export const TICKET_LEVELS: ReadonlyArray<{ readonly value: 1 | 2 | 3 | 4; readonly label: string }> = [
  { value: 1, label: '低' },
  { value: 2, label: '普通' },
  { value: 3, label: '高' },
  { value: 4, label: '紧急' },
];

export function levelLabel(level: number | undefined): string {
  if (level === undefined) return '—';
  return TICKET_LEVELS.find((l) => l.value === level)?.label ?? String(level);
}

export function LevelBadge({ level }: { level: number | undefined }) {
  if (level === undefined) return <span className="text-fg-subtle">—</span>;
  const tone = level >= 4 ? 'danger' : level === 3 ? 'warn' : 'neutral';
  return <Badge tone={tone}>{levelLabel(level)}</Badge>;
}

/** 详情页能写回去的三个状态。`replied` 不在其中，理由见 `STATUS_META`。 */
export const SETTABLE_STATUSES: readonly TicketStatus[] = ['open', 'pending', 'closed'];

/* ────────────────────────── 「等了多久」 ────────────────────────── */

/**
 * 最后一次有人说话到现在有多久。
 *
 * 🔴 **这不是 SLA 剩余时间**，页面上也不许这么标。SLA 的口径（首次响应几小时、
 * 按等级分档）存在系统配置里（page-inventory §4.3「系统配置 → SLA」），
 * 而这个端点既不返回配置也不返回 `first_response_at`。
 * 能算的只有「静默了多久」，那是一个**相关但不相等**的量：
 * 一张刚被客服回过的单静默 3 天不违约，一张没人碰过的单静默 3 天早就违约了。
 * 把它标成「SLA 剩余」会让人以为红色就是要违约了。
 */
export function silenceMs(ticket: AdminTicket, now: number): number {
  const iso = ticket.last_reply_at ?? ticket.created_at;
  const t = new Date(iso).getTime();
  if (Number.isNaN(t)) return 0;
  return Math.max(0, now - t);
}

export function formatDuration(ms: number): string {
  const minutes = Math.floor(ms / 60_000);
  if (minutes < 1) return '刚刚';
  if (minutes < 60) return `${minutes} 分钟`;
  const hours = Math.floor(minutes / 60);
  if (hours < 48) return `${hours} 小时`;
  return `${Math.floor(hours / 24)} 天`;
}

/* ────────────────────────────── 错误分支 ────────────────────────────── */

/**
 * 501 的判据。`NOT_IMPLEMENTED` **不在 openapi 的 `ErrorCode` enum 里**
 * （由 `main.go` 的 `responseErrorHandler` 直接写出去），所以只能按字符串比。
 * **不按状态码判**：「这一条还没写」与「这一条今天坏了」对运维是两句不同的话。
 *
 * ⚠️ 工单四个 operation 目前**全部已实现**，这条分支正常不会命中。
 * 留着是回滚保险：后端若把某一条摘回 501，这两页会说「还没上线」而不是渲染成一次故障。
 */
export const NOT_IMPLEMENTED_CODE = 'NOT_IMPLEMENTED';

export function isNotImplemented(error: ApiError | null | undefined): boolean {
  return error?.code === NOT_IMPLEMENTED_CODE;
}

export interface TicketErrorCopy {
  readonly title: string;
  readonly description: string;
}

/**
 * **读**路径的 `ErrorCode` → 文案。按 `code` 分支，不按 HTTP 状态码分支（api-contract §2.3）。
 *
 * 与 `DangerousAction` 的 `dangerErrorCopy` 分开不是重复：那一份写给**提交**
 * （「不是你的填写有问题」），拿到一次列表加载失败上说等于答非所问。
 */
export function ticketErrorCopy(error: ApiError, what: string): TicketErrorCopy {
  switch (error.code) {
    case NOT_IMPLEMENTED_CODE:
      return { title: '尚未开放', description: `${what}的后端还没实现。重试不会有变化。` };
    case 'AUTH_PERMISSION_DENIED':
      return {
        title: '这个账号看不了工单',
        description:
          '身份是通过的（IAP 认下了你），缺的是角色 —— 工单读写由角色决定（owner / admin / support）。重新登录不会有帮助。',
      };
    case 'RESOURCE_NOT_FOUND':
      return { title: '找不到这个工单', description: '它可能已经被合并或删除，也可能这个数字 id 根本不存在。' };
    case 'VALIDATION_FAILED':
    case 'VALIDATION_MALFORMED_BODY':
      return { title: '服务端退回了这次请求', description: fieldReasons(error) ?? error.message };
    case 'QUOTA_RATE_LIMITED':
      return {
        title: '操作太频繁',
        description:
          error.retryAfterSeconds === undefined ? '稍后再试。' : `${error.retryAfterSeconds} 秒后可以再试。`,
      };
    default:
      break;
  }
  switch (error.kind) {
    case 'offline':
      // 🔴 后台站在 IAP 后面，跨域时 IAP 的拒绝在 JS 里与网络不可达**无法区分**
      //    （`shared/api/client.ts` 的 `detectEdgeRejection` 写明了这条限制）。
      //    断言成「网络不好」的后果是运维去查网络，而真正该做的是重新过一次 IAP。
      return {
        title: '请求没能到达服务端',
        description:
          '可能是网络不通，也可能是 IAP 会话过期后把请求挡在了应用之前（跨域时前端分不出这两者）。先在新标签页打开一次后台确认 IAP 还认你。',
      };
    case 'server':
      return { title: '服务端出错了', description: '不是你的操作有问题。稍后再试，并把请求号一起报出来。' };
    default:
      return { title: `${what}没能加载`, description: error.message };
  }
}

function fieldReasons(error: ApiError): string | null {
  const details = error.details;
  if (!details || details.length === 0) return null;
  return details.map((d) => `${d.field}：${d.reason}`).join('；');
}

/** 读失败时的整块错误态。501 用虚线说明块，其余走全站统一的 `ErrorState`。 */
export function TicketQueryErrorState({
  error,
  what,
  why,
  onRetry,
}: {
  error: ApiError;
  what: string;
  /** 501 时说明卡在哪。「尚未开放」不说清缺什么，读的人只会每周来点一次看看好了没。 */
  why?: ReactNode;
  onRetry?: () => void;
}) {
  if (isNotImplemented(error)) {
    return (
      <Card className="border-dashed">
        <h3 className="text-base font-semibold text-fg">尚未开放</h3>
        <p className="mt-1.5 text-sm leading-relaxed text-fg-muted">
          {what}的后端还没实现（服务端明确回 <code className="font-mono">501 NOT_IMPLEMENTED</code>）。
          这不是故障，重试也不会有变化。
        </p>
        {why ? (
          <div className="mt-3 rounded-lg border border-line bg-surface-alt p-3 text-sm leading-relaxed text-fg-muted">
            {why}
          </div>
        ) : null}
        {error.requestId ? <p className="mt-3 font-mono text-xs text-fg-subtle">请求号 {error.requestId}</p> : null}
      </Card>
    );
  }
  const copy = ticketErrorCopy(error, what);
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

/** 说明块。这几页有好几处**必须**声明「你看到的不是全部事实」。 */
export function CaveatNotice({ children, testId }: { children: ReactNode; testId?: string }) {
  return (
    <div
      data-testid={testId}
      className="rounded-lg border border-warn/30 bg-warn/5 p-3 text-xs leading-relaxed text-fg-muted"
    >
      {children}
    </div>
  );
}

export const CONTROL =
  'w-full rounded-lg border border-line bg-surface px-3 text-base text-fg ' +
  'placeholder:text-fg-subtle focus-visible:border-accent focus-visible:outline-2 ' +
  'focus-visible:outline-offset-1 focus-visible:outline-accent disabled:opacity-50';
// 16px 起步（`text-base`）：iOS Safari 在 <16px 的输入框获得焦点时会自动放大页面。
// 工单是 M2 —— 手机上要能完成回复，所以这里的每一个控件都按手机来。

/* ────────────────────────────── 取数 ────────────────────────────── */

/**
 * 一页多少条。**不做无限滚动**：「加载更多」是可以停下来的，滚动不是，
 * 而客户端筛选与排序都只作用于已加载的部分 —— 滚动会让「已加载多少」变成一个没人知道的数。
 */
export const TICKET_PAGE_SIZE = 50;

export interface TicketPage {
  readonly items: readonly AdminTicket[];
  readonly meta: Meta;
}

/**
 * 拉一页工单。
 *
 * ⚠️ **`count` 只在第一页传。** 管理面允许返总数（`?count=true` → `meta.total`，
 * 契约原话「仅管理面提供」，用户面永不返），但它是一次实打实的 `COUNT(*)`，
 * 在 `db-f1-micro`（0.6 GiB RAM）上不能让每次翻页都付。
 */
export function listAdminTicketsPage(cursor: string | null, count: boolean): Promise<TicketPage> {
  const query = {
    limit: TICKET_PAGE_SIZE,
    ...(cursor === null ? {} : { cursor }),
    ...(count ? { count: true } : {}),
  };
  return unwrapWithMeta(api().GET('/api/v1/admin/tickets', { params: { query } })).then((envelope) => ({
    items: envelope.data,
    meta: envelope.meta,
  }));
}

/* ────────────────────────────── 页面 ────────────────────────────── */

type StatusFilter = TicketStatus | 'all';
type CategoryFilter = TicketCategory | 'all';
type LevelFilter = '1' | '2' | '3' | '4' | 'all';
type SortMode = 'server' | 'waiting';

export default function TicketsPage() {
  const first = useTicketQuery(() => listAdminTicketsPage(null, true), [], '没能加载工单队列');

  // 「加载更多」拿到的后续页。第一页重拉时要一起清掉，否则会出现两份第一页。
  const [more, setMore] = useState<readonly AdminTicket[]>([]);
  const [moreMeta, setMoreMeta] = useState<Meta | null>(null);
  const [morePending, setMorePending] = useState(false);
  const [moreError, setMoreError] = useState<ApiError | null>(null);

  const [status, setStatus] = useState<StatusFilter>('all');
  const [category, setCategory] = useState<CategoryFilter>('all');
  const [level, setLevel] = useState<LevelFilter>('all');
  const [keyword, setKeyword] = useState('');
  const [sort, setSort] = useState<SortMode>('server');

  const meta = moreMeta ?? first.data?.meta ?? null;
  const cursor = meta?.next_cursor ?? null;

  const loaded = useMemo(
    () => (first.data === null ? [] : [...first.data.items, ...more]),
    [first.data, more],
  );

  const reload = useCallback(() => {
    setMore([]);
    setMoreMeta(null);
    setMoreError(null);
    first.reload();
  }, [first]);

  const loadMore = useCallback(async () => {
    if (morePending || cursor === null) return;
    setMorePending(true);
    setMoreError(null);
    try {
      const page = await listAdminTicketsPage(cursor, false);
      setMore((prev) => [...prev, ...page.items]);
      setMoreMeta(page.meta);
    } catch (cause) {
      setMoreError(asTicketError(cause, '没能加载更多工单'));
    } finally {
      setMorePending(false);
    }
  }, [cursor, morePending]);

  // 「静默多久」是相对当前时刻算的。取一次时间给整轮渲染用，
  // 免得同一屏里的两行按两个不同的 now 算出不一致的结果。
  const now = Date.now();

  const shown = useMemo(() => {
    const kw = keyword.trim().toLowerCase();
    const filtered = loaded.filter((t) => {
      if (status !== 'all' && t.status !== status) return false;
      if (category !== 'all' && t.category !== category) return false;
      if (level !== 'all' && String(t.level ?? '') !== level) return false;
      if (kw !== '' && !`${t.public_id} ${t.subject}`.toLowerCase().includes(kw)) return false;
      return true;
    });
    if (sort === 'server') return filtered;
    // 等最久的排前面。已关闭的沉底：它们不需要人处理，混在前面只会挡住要处理的。
    return [...filtered].sort((a, b) => {
      const closed = Number(a.status === 'closed') - Number(b.status === 'closed');
      if (closed !== 0) return closed;
      return silenceMs(b, now) - silenceMs(a, now);
    });
  }, [loaded, status, category, level, keyword, sort, now]);

  const filtering = status !== 'all' || category !== 'all' || level !== 'all' || keyword.trim() !== '';

  return (
    <>
      <PageHeader
        title="工单"
        description="按状态 / 分类 / 等级筛选的队列。手机上要能完成回复（M2）。"
        meta={
          <>
            <Badge tone="info">P1</Badge>
            <Badge tone="warn">M2 · 手机上核心操作必须能完成</Badge>
            {meta?.total === undefined ? null : <Badge>全库 {meta.total.toLocaleString('zh-CN')} 张</Badge>}
          </>
        }
      />

      <div className="mb-5 space-y-3">
        <OpenByIdCard />
        <QueueCaveat loadedCount={loaded.length} total={meta?.total} />
      </div>

      <Card className="mb-4">
        <CardTitle hint="筛选与排序都只作用于已加载的部分">筛选</CardTitle>
        <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-5">
          <Field id="ticket-filter-keyword" label="工单号 / 主题">
            <input
              id="ticket-filter-keyword"
              value={keyword}
              placeholder="BP-7K2M9Q"
              autoComplete="off"
              spellCheck={false}
              onChange={(e) => setKeyword(e.target.value)}
              className={cx(CONTROL, 'min-h-11')}
            />
          </Field>
          <Field id="ticket-filter-status" label="状态">
            <select
              id="ticket-filter-status"
              value={status}
              onChange={(e) => setStatus(e.target.value as StatusFilter)}
              className={cx(CONTROL, 'min-h-11')}
            >
              <option value="all">全部状态</option>
              {(Object.keys(STATUS_META) as TicketStatus[]).map((s) => (
                <option key={s} value={s}>
                  {statusLabel(s)}
                </option>
              ))}
            </select>
          </Field>
          <Field id="ticket-filter-category" label="分类">
            <select
              id="ticket-filter-category"
              value={category}
              onChange={(e) => setCategory(e.target.value as CategoryFilter)}
              className={cx(CONTROL, 'min-h-11')}
            >
              <option value="all">全部分类</option>
              {(Object.keys(CATEGORY_LABEL) as TicketCategory[]).map((c) => (
                <option key={c} value={c}>
                  {categoryLabel(c)}
                </option>
              ))}
            </select>
          </Field>
          <Field id="ticket-filter-level" label="等级">
            <select
              id="ticket-filter-level"
              value={level}
              onChange={(e) => setLevel(e.target.value as LevelFilter)}
              className={cx(CONTROL, 'min-h-11')}
            >
              <option value="all">全部等级</option>
              {TICKET_LEVELS.map((l) => (
                <option key={l.value} value={String(l.value)}>
                  {l.label}
                </option>
              ))}
            </select>
          </Field>
          <Field
            id="ticket-sort"
            label="排序"
            hint="服务端只按创建时间倒序返回，没有 SLA 排序参数。"
          >
            <select
              id="ticket-sort"
              value={sort}
              onChange={(e) => setSort(e.target.value as SortMode)}
              className={cx(CONTROL, 'min-h-11')}
            >
              <option value="server">创建时间（新的在前）</option>
              <option value="waiting">静默最久的在前</option>
            </select>
          </Field>
        </div>
      </Card>

      {first.state === 'loading' ? (
        <div className="space-y-2" data-testid="queue-skeleton">
          {Array.from({ length: 6 }, (_, i) => (
            <Skeleton key={i} className="h-16 w-full" />
          ))}
        </div>
      ) : first.state === 'error' && first.error !== null ? (
        <TicketQueryErrorState
          error={first.error}
          what="工单队列"
          why={
            <>
              <code className="font-mono">listAdminTickets</code> 被摘回了 501。
              这一条本来是实现了的，出现这个提示说明后端把它退了回去 —— 找后端确认，别当成故障排查。
            </>
          }
          onRetry={reload}
        />
      ) : loaded.length === 0 ? (
        <EmptyState
          title="队列是空的"
          description="全库一张工单都没有。如果这和你的预期不符，先确认工单入口在用户面板上是可达的 —— 用户提不了单，队列当然是空的。"
          action={
            <LinkButton tone="primary" href="/admin">
              回到看板 <Icon.ArrowRight size={14} />
            </LinkButton>
          }
        />
      ) : shown.length === 0 ? (
        // 🔴 「筛掉了」与「没有」必须是两句话。已加载 120 条里筛不出东西，
        //    不代表全库没有 —— 后者要靠继续加载或者一个服务端筛选参数（契约没有）。
        <Card>
          <p className="text-sm font-medium text-fg">当前筛选条件在已加载的 {loaded.length} 条里没有命中</p>
          <p className="mt-1.5 text-sm leading-relaxed text-fg-muted">
            这<strong className="font-medium text-fg">不等于</strong>全库没有符合条件的工单：筛选是在浏览器里做的，契约没有给这个端点任何筛选参数。
            {cursor === null ? '（已经加载到最后一页了。）' : '先「加载更多」再看，或者清掉筛选条件。'}
          </p>
          <div className="mt-3 flex flex-wrap gap-2">
            <Button
              onClick={() => {
                setStatus('all');
                setCategory('all');
                setLevel('all');
                setKeyword('');
              }}
            >
              清空筛选
            </Button>
            {cursor === null ? null : (
              <Button tone="primary" disabled={morePending} onClick={() => void loadMore()}>
                {morePending ? '加载中…' : '加载更多'}
              </Button>
            )}
          </div>
        </Card>
      ) : (
        <Card>
          <CardTitle
            hint={
              filtering
                ? `已加载 ${loaded.length} 条，筛出 ${shown.length} 条`
                : `已加载 ${loaded.length} 条`
            }
          >
            队列
          </CardTitle>

          <div className="hidden grid-cols-[9rem_minmax(0,1fr)_7rem_5rem_7rem_9rem_6rem] gap-3 border-b border-line pb-2 text-xs font-medium text-fg-muted md:grid">
            <span>工单号</span>
            <span>主题</span>
            <span>分类</span>
            <span>等级</span>
            <span>状态</span>
            <span>最后回复</span>
            <span>静默</span>
          </div>

          <ul className="divide-y divide-line">
            {shown.map((t) => (
              <TicketRow key={t.public_id} ticket={t} now={now} />
            ))}
          </ul>

          <div className="mt-4 flex flex-wrap items-center gap-3">
            {cursor === null ? (
              <p className="text-xs text-fg-subtle">已经到最后一页了。</p>
            ) : (
              <Button disabled={morePending} onClick={() => void loadMore()}>
                {morePending ? '加载中…' : '加载更多'}
              </Button>
            )}
            {meta?.total === undefined ? null : (
              <p className="text-xs text-fg-subtle">
                已加载 {loaded.length} / 全库 {meta.total.toLocaleString('zh-CN')} 条
              </p>
            )}
          </div>

          {moreError === null ? null : (
            <div className="mt-3">
              <TicketQueryErrorState error={moreError} what="下一页工单" onRetry={() => void loadMore()} />
            </div>
          )}
        </Card>
      )}
    </>
  );
}

/** 一行工单。**不是链接** —— 理由见文件头第 1 条。 */
function TicketRow({ ticket, now }: { ticket: AdminTicket; now: number }) {
  return (
    <li
      data-testid="ticket-row"
      className="grid gap-x-3 gap-y-1 py-3 text-sm md:grid-cols-[9rem_minmax(0,1fr)_7rem_5rem_7rem_9rem_6rem] md:items-center"
    >
      <span className="font-mono text-xs text-fg">{ticket.public_id}</span>
      <span className="min-w-0 truncate font-medium text-fg" title={ticket.subject}>
        {ticket.subject}
      </span>
      <span className="text-fg-muted">
        <span className="md:hidden">分类：</span>
        {categoryLabel(ticket.category)}
      </span>
      <span>
        <span className="text-fg-muted md:hidden">等级：</span>
        <LevelBadge level={ticket.level} />
      </span>
      <span>
        <StatusBadge status={ticket.status} />
      </span>
      <span className="text-xs text-fg-muted">
        <span className="md:hidden">最后回复：</span>
        {ticket.last_reply_at === undefined ? '还没有人回复' : formatDateTime(ticket.last_reply_at)}
      </span>
      <span className="text-xs text-fg-muted">
        <span className="md:hidden">静默：</span>
        {ticket.status === 'closed' ? '—' : formatDuration(silenceMs(ticket, now))}
      </span>
    </li>
  );
}

/**
 * 「按数字 id 打开会话」。
 *
 * 🔴 这个输入框存在的唯一原因是一个契约缺口（文件头第 1 条）：队列里没有数字主键，
 * 而详情页只认数字主键。它是一根拐杖，不是一个功能 —— 所以旁边那句话必须说清楚
 * 为什么要手输，否则下一个人会以为这是设计。
 */
function OpenByIdCard() {
  const navigate = useNavigate();
  const [value, setValue] = useState('');
  const trimmed = value.trim();
  const valid = /^[0-9]+$/.test(trimmed) && Number(trimmed) > 0;

  return (
    <Card className="border-dashed">
      <div className="flex flex-col gap-3 sm:flex-row sm:items-end">
        <div className="min-w-0 flex-1">
          <label htmlFor="ticket-open-id" className="mb-1.5 block text-sm font-medium text-fg">
            按数字 id 打开会话
          </label>
          <input
            id="ticket-open-id"
            value={value}
            inputMode="numeric"
            placeholder="例如 1024"
            autoComplete="off"
            onChange={(e) => setValue(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === 'Enter' && valid) navigate(`/admin/tickets/${trimmed}`);
            }}
            className={cx(CONTROL, 'min-h-11 font-mono')}
          />
        </div>
        <Button
          tone="primary"
          disabled={!valid}
          aria-disabled={!valid}
          onClick={() => {
            if (valid) navigate(`/admin/tickets/${trimmed}`);
          }}
        >
          打开
        </Button>
      </div>
      <p className="mt-2 text-xs leading-relaxed text-fg-muted">
        下面每一行<strong className="font-medium text-fg">不是链接</strong>：列表返回的是对外工单号（<code className="font-mono">BP-…</code>），
        而会话端点按**数字主键**定位（<code className="font-mono">GET /admin/tickets/{'{id}'}</code>，
        <code className="font-mono">IdPath: number</code>）。两者在契约里没有换算入口 ——
        把工单号填进这个框会被服务端的请求校验挡下来，那看起来像「工单不存在」，其实是缺口。
      </p>
    </Card>
  );
}

/** 队列自己的三条限制。写在页面上而不是注释里 —— 注释只有改代码的人会读。 */
function QueueCaveat({ loadedCount, total }: { loadedCount: number; total: number | undefined }) {
  return (
    <CaveatNotice testId="queue-caveat">
      <p className="font-medium text-fg">这个队列现在只能做到这个程度：</p>
      <ul className="mt-1.5 list-disc space-y-1 pl-4">
        <li>
          <strong className="font-medium text-fg">排序不是 SLA 排序。</strong>
          契约里没有 SLA 字段，端点也没有排序参数，服务端固定按创建时间倒序返回。
          「静默最久的在前」是**在浏览器里**对已加载的 {loadedCount} 条重排
          {total === undefined ? '' : `（全库 ${total.toLocaleString('zh-CN')} 条）`}
          ，不是「全库最该先处理的那一张」。
        </li>
        <li>
          <strong className="font-medium text-fg">「静默」不是「SLA 剩余」。</strong>
          它是最后一次有人说话到现在的时长。一张刚被回过的单静默三天不违约，一张没人碰过的早就违约了。
        </li>
        <li>
          <strong className="font-medium text-fg">看不到是谁提的单。</strong>
          列表行里没有用户字段（<code className="font-mono">user_email</code> 只在会话详情里返回）。
        </li>
      </ul>
    </CaveatNotice>
  );
}

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
    <div className="min-w-0">
      <label htmlFor={id} className="mb-1.5 block text-sm font-medium text-fg">
        {label}
      </label>
      {children}
      {hint ? <p className="mt-1.5 text-xs leading-relaxed text-fg-muted">{hint}</p> : null}
    </div>
  );
}

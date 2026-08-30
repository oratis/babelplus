/**
 * 后台「订单与支付」三页（订单列表 · 订单详情 · 支付与对账）共用的零件。
 *
 * 为什么单开一个文件：后台 17 个模块正在**并行**接线，`shared/ui` 与 `components/`
 * 是所有人都要改的目录，动它必然撞车；而这里的每一样都带着**这三页特有的**
 * 产品约束（金额一律整数、订单状态是契约外的 14 个值、退款明细只在 422 里），
 * 拿到别的模块上既用不着也会误导。同一条路用户面走过（`user/src/routes/ticket-common.tsx`），
 * 后台的另外三组也各有一份（`catalog-common` / `node-common` / `ops-common`）。
 *
 * ⚠️ **不跨包 import 用户面板，也不 import 兄弟组的 `*-common`。**
 * 两个 SPA 是两套故障域（`lib/api.ts` 文件头）；而兄弟组的 common 与这里是
 * 同时在写的东西，互相 import 等于把三组人的进度焊在一起。
 *
 * 🔴 **本文件不做任何浮点运算。** 金额有三种单位：分（`int64`）、`1e-6 USDT`、字节。
 * 「小地址池 + 金额唯一性」这个匹配机制的**整个前提**就是金额精确可比 ——
 * 一次 `parseFloat` 就足以让「应付 5.8423 USDT」变成「5.842299999999999」，
 * 而它错得不响：对账页上两个数看起来一样，只是永远匹配不上。
 */
import { useCallback, useEffect, useId, useRef, useState, type ReactNode } from 'react';
import {
  ApiError,
  unwrap,
  unwrapWithMeta,
  type Meta,
  type components,
} from '@babelplus/shared/api';
import { PageHeader } from '@babelplus/shared/ui';
import {
  Badge,
  Button,
  Card,
  ErrorState,
  LoadingState,
  SkeletonCard,
  cx,
  formatCny,
  formatUsdt,
} from './_imports.ts';
import { api } from '../lib/api.ts';
import { asApiError } from '../lib/auth.tsx';
import { dangerOps } from '../lib/danger.ts';

/* ────────────────────────── 契约类型（以 schema.d.ts 为准） ────────────────────────── */

export type AdminOrder = components['schemas']['AdminOrder'];
export type AdminPayment = components['schemas']['AdminPayment'];
export type PaymentState = components['schemas']['PaymentState'];
export type { Meta };

/** 一页列表 + 它的 `meta`（游标与 `total` 都在里面）。 */
export interface Page<T> {
  readonly data: readonly T[];
  readonly meta: Meta;
}

/* ────────────────────────────── 请求三态 ────────────────────────────── */

export type QueryState = 'loading' | 'ready' | 'error';

export interface ApiQuery<T> {
  readonly state: QueryState;
  readonly data: T | null;
  readonly error: ApiError | null;
  /** 重新发一次。错误态的「重试」与写操作成功后的刷新都用它。 */
  reload(): void;
}

/**
 * 一个请求 = 一套三态。刻意不引 react-query：`shared/api/client.ts` 的文件头写明
 * 缓存与状态管理的选型**还没裁决**，现在装一个等于替以后的人做决定。
 *
 * `run` 不要求调用方 memo：用 ref 取最新的那一份，重跑与否**只由 `deps` 决定**。
 * 要求 memo 的话每个调用点都得包一层 `useCallback`，而漏包的表现是**死循环请求** ——
 * 在后台这意味着一页打开就开始刷服务端，而这个库（db-f1-micro）的连接池只有 2 条。
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
        // 迟到的响应不许覆盖新一轮的状态，所以先判 alive 再 set。
        if (!alive) return;
        setData(value);
        setState('ready');
      })
      .catch((cause: unknown) => {
        if (!alive) return;
        setError(asApiError(cause, fallbackMessage));
        setState('error');
      });
    return () => {
      alive = false;
    };
  }, [...deps, nonce]);

  const reload = useCallback(() => setNonce((n) => n + 1), []);

  return { state, data, error, reload };
}

/* ────────────────────────── 错误分支（按 code，不按状态码） ────────────────────────── */

/**
 * 501 的判据。
 *
 * `NOT_IMPLEMENTED` **不在 openapi 的 `ErrorCode` enum 里**（错误映射层直接写出去的），
 * 所以只能按字符串比。按状态码判也行，但那会把将来任何一个真的 501 也算进来 ——
 * 而「端点还没写」与「端点写了但依赖挂了」对操作者是两句不同的话。
 */
export const NOT_IMPLEMENTED_CODE = 'NOT_IMPLEMENTED';

export function isNotImplemented(error: ApiError | null | undefined): boolean {
  return error?.code === NOT_IMPLEMENTED_CODE;
}

/**
 * `ErrorCode` → 文案。**这三页唯一的读侧文案表**（api-contract §2.3：
 * 禁止匹配 `message` 做分支）。写侧（危险操作提交失败）用
 * `DangerousAction` 的 `dangerErrorCopy` —— 两张表**有意分开**：
 * 读到 403 是「你看不了这一块」，写到 403 是「你改不了，去找人开权限位」。
 */
export function orderErrorCopy(
  error: ApiError,
  options: { fallbackTitle?: string } = {},
): { title: string; description: string } {
  switch (error.code) {
    case NOT_IMPLEMENTED_CODE:
      return { title: '这一块后端还没上线', description: '不是你的操作有问题，重试也不会有变化。' };
    case 'AUTH_PERMISSION_DENIED':
      return {
        title: '当前管理员账号看不到这一块',
        description: '身份是通过的，缺的是角色或权限位。重新登录不会有帮助。',
      };
    case 'RESOURCE_NOT_FOUND':
      return { title: '找不到这条记录', description: '订单号可能不对，或它刚被别人改过。' };
    case 'VALIDATION_FAILED':
    case 'VALIDATION_MALFORMED_BODY':
      return { title: '服务端退回了这次请求', description: fieldReasons(error) ?? error.message };
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
        title: '连不上后台 API',
        description:
          '后台不做备用域名故障转移（多一个入口就是多一个要防护的入口）。' +
          '若你在大陆境内，先确认自己的出网路径 —— IAP 要求的 Google 身份本身就要能出去。',
      };
    case 'server':
      return { title: '服务端出错了', description: '不是你的操作有问题。稍后再试，并把请求号一起报出来。' };
    case 'unauthorized':
      return { title: '需要重新准入', description: '会话状态已经变了，刷新页面会重新走一次准入探测。' };
    default:
      return { title: options.fallbackTitle ?? '加载失败', description: error.message };
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
 * **刻意不用 `ErrorState`**：501 的 `kind` 是 `server`，而 `ErrorState` 在 server 态下
 * 会说「我们这边出了问题」并把人推去状态页 —— 状态页上一切正常，看的人只会更困惑。
 * 「还没做」不是故障。
 */
export function NotImplementedNotice({
  what,
  why,
  requestId,
}: {
  what: string;
  /** 为什么还没做。**必须给** —— 「尚未开放」四个字会让人以为再等等就有了。 */
  why: ReactNode;
  requestId?: string | undefined;
}) {
  return (
    <Card className="border-dashed">
      <h3 className="text-base font-semibold text-fg">{what}尚未开放</h3>
      <p className="mt-1.5 text-sm leading-relaxed text-fg-muted">{why}</p>
      {requestId ? <p className="mt-3 font-mono text-xs text-fg-subtle">请求号 {requestId}</p> : null}
    </Card>
  );
}

/** 读请求失败时的整块错误态。501 走上面那块，其余走全站统一的 `ErrorState`。 */
export function QueryErrorState({
  error,
  what,
  notImplementedWhy,
  onRetry,
}: {
  error: ApiError;
  what: string;
  notImplementedWhy?: ReactNode;
  onRetry?: () => void;
}) {
  if (isNotImplemented(error)) {
    return (
      <NotImplementedNotice
        what={what}
        why={notImplementedWhy ?? '后端这一条还没实现。不是你的操作有问题，重试也不会有变化。'}
        requestId={error.requestId}
      />
    );
  }
  const copy = orderErrorCopy(error, { fallbackTitle: `${what}没能加载` });
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

/** 列表区的骨架屏。三页统一，免得每页各挑一个行数。 */
export function ListLoading() {
  return (
    <LoadingState>
      <SkeletonCard lines={5} />
    </LoadingState>
  );
}

/* ────────────────────────────── 游标分页 ────────────────────────────── */

/** 一页多少条。 */
export const PAGE_SIZE = 20;

export interface CursorPager {
  /** 当前页的起始游标。第一页是 `null`（不传 `cursor` 参数）。 */
  readonly cursor: string | null;
  /** 第几页，从 1 起。**只用于显示**，不参与请求。 */
  readonly pageNumber: number;
  readonly atFirstPage: boolean;
  next(cursor: string): void;
  prev(): void;
  /** 改筛选条件时回到第一页。旧游标在新条件下解出的是一段无意义的位置。 */
  reset(): void;
}

/**
 * 游标分页器。
 *
 * 🔴 **游标分页没有「跳到第 7 页」。** 游标是「从这一行之后再取 N 条」，不是偏移量。
 * 所以这里只有上一页 / 下一页，且「上一页」靠**记住来路**（压栈）实现 ——
 * 契约里没有反向游标。做成 `?page=7` 需要 OFFSET，而 OFFSET 在这个库上
 * 恰恰是我们要避开的东西。缺口是真的，形态是有意的。
 */
export function useCursorPager(): CursorPager {
  // 栈里存的是**每一页的起始游标**，栈底恒为 null（第一页）。
  const [stack, setStack] = useState<readonly (string | null)[]>([null]);
  const cursor = stack.length > 0 ? (stack[stack.length - 1] ?? null) : null;

  const next = useCallback((c: string) => setStack((s) => [...s, c]), []);
  const prev = useCallback(() => setStack((s) => (s.length > 1 ? s.slice(0, -1) : s)), []);
  const reset = useCallback(() => setStack([null]), []);

  return { cursor, pageNumber: stack.length, atFirstPage: stack.length === 1, next, prev, reset };
}

/**
 * 分页器。
 *
 * ⚠️ **管理面可以返总数，用户面不行**（`Meta.total`：仅管理面 `?count=true`）。
 * 但 `COUNT(*)` 在 db-f1-micro 上是实打实的开销，所以**只在第一页要一次**，
 * 后续页沿用第一次拿到的那个数（见 `useRememberedTotal`）。
 * 翻页期间有人下了新单时总数会短暂偏旧 —— 比每页都付一次 COUNT 划算，
 * 且「共 87 条」在后台的用途是估量级，不是对账。
 */
export function Pager({
  meta,
  pager,
  total,
  busy,
}: {
  meta: Meta | null;
  pager: CursorPager;
  total: number | null;
  busy?: boolean;
}) {
  const nextCursor = meta?.next_cursor ?? null;
  const hasMore = meta?.has_more === true && nextCursor !== null;

  return (
    <div className="mt-4 flex flex-wrap items-center justify-between gap-3 text-sm text-fg-muted">
      <span>
        第 {pager.pageNumber} 页
        {total === null ? null : <> · 共 {total.toLocaleString('zh-CN')} 条</>}
      </span>
      <span className="flex gap-2">
        <Button onClick={pager.prev} disabled={pager.atFirstPage || busy === true}>
          上一页
        </Button>
        <Button
          onClick={() => {
            // 🔴 判据是 `next_cursor` 而不是「这一页够不够 20 条」：
            //    最后一页恰好 20 条时，按条数判会给出一个点不动的「下一页」。
            if (nextCursor !== null) pager.next(nextCursor);
          }}
          disabled={!hasMore || busy === true}
        >
          下一页
        </Button>
      </span>
    </div>
  );
}

/**
 * 记住第一页那次 `count=true` 的总数。翻到第二页后 `meta.total` 就没有了
 * （我们不再要），但分页器还要显示它。回到第一页会重新取一次。
 */
export function useRememberedTotal(meta: Meta | null | undefined): number | null {
  const [total, setTotal] = useState<number | null>(null);
  const seen = meta?.total;
  useEffect(() => {
    if (typeof seen === 'number') setTotal(seen);
  }, [seen]);
  return total;
}

/* ────────────────────────────── 表格 ────────────────────────────── */

/**
 * 表格外壳。**横向滚动必须落在这一层**，不能让 body 横滚。
 * 订单列表是 M2（手机上要能查单），所以表格在 <768px 由调用方换成卡片，
 * 这一层只保证「万一没换，也不会出现全页横滚」。
 */
export function DataTable({ head, children }: { head: readonly string[]; children: ReactNode }) {
  return (
    <div className="-mx-4 overflow-x-auto sm:mx-0">
      <table className="w-full min-w-[44rem] border-collapse text-sm">
        <thead>
          <tr className="border-b border-line text-left text-xs font-medium text-fg-muted">
            {head.map((h) => (
              <th key={h} scope="col" className="px-3 py-2 whitespace-nowrap">
                {h}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>{children}</tbody>
      </table>
    </div>
  );
}

export function Td({ children, className }: { children: ReactNode; className?: string }) {
  return <td className={cx('px-3 py-2 align-top', className)}>{children}</td>;
}

export function Tr({ children }: { children: ReactNode }) {
  return <tr className="border-b border-line/60 last:border-0">{children}</tr>;
}

/** 一行「标签 · 值」。详情页的只读区与手机端卡片共用同一个形状。 */
export function Row({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="flex flex-wrap items-baseline justify-between gap-x-4 gap-y-0.5 py-1.5">
      <span className="text-xs text-fg-muted">{label}</span>
      <span className="text-sm text-fg">{children}</span>
    </div>
  );
}

export const MISSING = '—';

/* ────────────────────────────── 表单原语 ────────────────────────────── */

const CONTROL =
  'w-full rounded-lg border border-line bg-surface px-3 text-base text-fg ' +
  'placeholder:text-fg-subtle focus-visible:border-accent focus-visible:outline-2 ' +
  'focus-visible:outline-offset-1 focus-visible:outline-accent disabled:opacity-50';
// 16px 起步（`text-base`）：iOS Safari 在 <16px 的输入框获得焦点时会自动放大页面。
// 订单是 M2 —— 用户在电话里念订单号的时候，运维多半正拿着手机。

export function FieldShell({
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
  label,
  value,
  onChange,
  hint,
  placeholder,
  mono,
  inputMode,
}: {
  label: string;
  value: string;
  onChange: (next: string) => void;
  hint?: ReactNode;
  placeholder?: string;
  mono?: boolean;
  inputMode?: 'text' | 'numeric' | 'url';
}) {
  const id = useId();
  return (
    <FieldShell id={id} label={label} hint={hint}>
      <input
        id={id}
        value={value}
        placeholder={placeholder}
        inputMode={inputMode}
        autoComplete="off"
        spellCheck={false}
        onChange={(event) => onChange(event.target.value)}
        className={cx(CONTROL, 'min-h-11', mono === true && 'font-mono')}
      />
    </FieldShell>
  );
}

export function SelectField<T extends string>({
  label,
  value,
  options,
  onChange,
  hint,
}: {
  label: string;
  value: T;
  options: ReadonlyArray<{ readonly value: T; readonly label: string }>;
  onChange: (next: T) => void;
  hint?: ReactNode;
}) {
  const id = useId();
  return (
    <FieldShell id={id} label={label} hint={hint}>
      <select
        id={id}
        value={value}
        onChange={(event) => onChange(event.target.value as T)}
        className={cx(CONTROL, 'min-h-11')}
      >
        {options.map((o) => (
          <option key={o.value} value={o.value}>
            {o.label}
          </option>
        ))}
      </select>
    </FieldShell>
  );
}

/* ────────────────────────── 金额：三种单位，一律整数 ────────────────────────── */

/**
 * 「分」的整数解析。**不用 `parseFloat`，也不接受小数点** ——
 * 调用方要的是 `int64` 分，而「12.5」这种输入的正确处置是**拒绝**，
 * 不是悄悄变成 12 分或 1250 分（这两种猜法都出现过，且都不报错）。
 *
 * 返回 `null` = 不是一个合法的正整数分值。
 */
export function parseCents(raw: string): number | null {
  const s = raw.trim();
  if (s === '') return null;
  if (!/^[0-9]+$/.test(s)) return null;
  const n = Number(s);
  if (!Number.isSafeInteger(n) || n <= 0) return null;
  return n;
}

/** 1e-6 USDT → 展示串。四位小数是收银台的口径（末位是订单识别码）。 */
export function usdt6Text(amount: number | null | undefined): string {
  if (amount === null || amount === undefined) return MISSING;
  return formatUsdt(amount, 4);
}

/** 分 → 人民币展示。`undefined` 与 0 要分开：0 是「这一项确实是零」。 */
export function cnyText(cents: number | null | undefined): string {
  if (cents === null || cents === undefined) return MISSING;
  return formatCny(cents);
}

/* ────────────────────────── 订单状态：契约外的 14 个值 ────────────────────────── */

/**
 * 🔴 **管理面的 `status` 会出现契约 enum 之外的值，这是服务端刻意的偏离。**
 *
 * 契约的 `OrderStatus` 只有 6 个（且含库里根本不存在的 `processing`），
 * 库里有 14 个。用户面把 14 压成 6 是对的；管理面**不能**压 ——
 * 后台是全系统唯一能看见 `refunding` / `chargeback*` 的地方，
 * 压扁会让后台**看不见拒付**，而拒付是要在 120 天窗口内申辩的东西。
 * （`api/internal/handler/admin_orders.go` 的 `adminOrderStatusView` 逐字写了这条。）
 *
 * 所以这里按 `string` 处理，且**认不出来的值原样显示**，不显示成「未知」——
 * 服务端将来加一个状态时，后台该看见的是那个新状态的名字，不是一个盖住它的占位符。
 */
export const ORDER_STATUS_LABEL: Readonly<Record<string, string>> = {
  pending: '待支付',
  paying: '支付中',
  underpaid: '少付',
  paid: '已支付',
  completed: '已完成',
  cancelled: '已取消',
  expired: '已过期',
  failed: '失败',
  refunding: '退款中',
  refunded: '已退款',
  partially_refunded: '部分退款',
  chargeback: '拒付申诉中',
  chargeback_won: '拒付申诉胜诉',
  chargeback_lost: '拒付败诉',
  // 契约里有、库里没有的那一个。真出现了要能看见，别让它变成一个空格。
  processing: '处理中',
};

export function orderStatusLabel(status: string): string {
  return ORDER_STATUS_LABEL[status] ?? status;
}

/** 需要人工介入的状态。列表上要能一眼扫出来。 */
const ORDER_STATUS_TONE: Readonly<Record<string, 'neutral' | 'ok' | 'warn' | 'danger' | 'info'>> = {
  pending: 'neutral',
  paying: 'info',
  underpaid: 'warn',
  paid: 'ok',
  completed: 'ok',
  cancelled: 'neutral',
  expired: 'neutral',
  failed: 'danger',
  refunding: 'warn',
  refunded: 'neutral',
  partially_refunded: 'warn',
  chargeback: 'danger',
  chargeback_won: 'ok',
  chargeback_lost: 'danger',
  processing: 'info',
};

export function OrderStatusBadge({ status }: { status: string }) {
  return <Badge tone={ORDER_STATUS_TONE[status] ?? 'neutral'}>{orderStatusLabel(status)}</Badge>;
}

/** 库里 6 个订单类型，契约只写了 4 个。同上：认不出的原样显示。 */
export const ORDER_TYPE_LABEL: Readonly<Record<string, string>> = {
  new: '新购',
  renew: '续费',
  upgrade: '升级',
  traffic_pack: '流量包',
  reset_pack: '流量重置包',
  wallet_topup: '钱包充值',
};

export function orderTypeLabel(type: string): string {
  return ORDER_TYPE_LABEL[type] ?? type;
}

/**
 * 服务端认为「可退」的三个状态（ADR 0013 §3.2 / `classifyRefund`）。
 *
 * ⚠️ 前端拿它**只用来把 D7 按钮预先变灰**，不是判据 —— 判据在服务端。
 * 而且只对**认得出来**的状态变灰：一个我们不认识的新状态要放行到服务端去判，
 * 否则「服务端加了一个可退状态」的现象会是「后台永远退不了这类单」。
 */
export const REFUNDABLE_ORDER_STATUSES: readonly string[] = ['paid', 'completed', 'partially_refunded'];

export function knownNonRefundable(status: string): boolean {
  return ORDER_STATUS_LABEL[status] !== undefined && !REFUNDABLE_ORDER_STATUSES.includes(status);
}

/* ────────────────────────── 支付状态 ────────────────────────── */

export const PAYMENT_STATE_LABEL: Readonly<Record<string, string>> = {
  waiting: '等待到账',
  confirming: '确认中',
  underpaid: '少付',
  paid: '已到账',
  expired: '已过期',
};

const PAYMENT_STATE_TONE: Readonly<Record<string, 'neutral' | 'ok' | 'warn' | 'danger' | 'info'>> = {
  waiting: 'neutral',
  confirming: 'info',
  underpaid: 'warn',
  paid: 'ok',
  expired: 'neutral',
};

export function PaymentStateBadge({ state }: { state: string }) {
  return (
    <Badge tone={PAYMENT_STATE_TONE[state] ?? 'neutral'}>
      {PAYMENT_STATE_LABEL[state] ?? state}
    </Badge>
  );
}

/** 契约的 `PaymentState` 五个值，D13 的下拉用它。 */
export const PAYMENT_STATES: readonly PaymentState[] = [
  'waiting',
  'confirming',
  'underpaid',
  'paid',
  'expired',
];

/* ────────────────────────── 退款扣减明细（只在 422 里） ────────────────────────── */

/**
 * 🔴 **退款的分段扣减明细在冻结契约下只有一个出口：被拒时的 `422.details`。**
 *
 * 服务端的 `GetRefundBasis`（`WITH RECURSIVE` 窗口链）算出 `V_window` /
 * `consumed_time` / `consumed_data` / `refund_B` 四行，而成功响应的 schema 是
 * `AdminOrder` —— 上面没有任何地方能放它们。所以明细走两条路：
 * ① 被拒时进 422 的 `details`（操作者当场看得到算式）；
 * ② 成功时进 `audit_logs` 的 `after` 快照（`/admin/audit` 能读回来）。
 * （`admin_orders.go` 的 `refundBreakdownDetails` 逐字写了这条，缺口也登记在那里：
 * 契约需要一个退款预览端点，或在响应上加一个 breakdown 对象。）
 *
 * 这个函数把那批 `details` 认出来，好让页面把它渲染成一张**表**而不是一行
 * 用分号串起来的长句 —— 「不是只给一个总数」这条要求指的就是这张表。
 */
export const REFUND_BREAKDOWN_FIELDS: readonly string[] = [
  'v_window',
  'consumed_time',
  'consumed_data',
  'refund_b',
  'already_refunded',
  'rule',
];

export const REFUND_BREAKDOWN_LABEL: Readonly<Record<string, string>> = {
  v_window: '本次订阅期内实付合计',
  consumed_time: '已服务时间折算（扣减）',
  consumed_data: '已消耗套餐流量折算（扣减）',
  refund_b: '常规退款额',
  already_refunded: '此前已退到余额',
  rule: '本次适用档位',
};

export interface RefundBreakdownRow {
  readonly field: string;
  readonly label: string;
  readonly reason: string;
}

/** 从一个 `ApiError` 里抽出退款扣减明细。抽不到返回空数组（不是所有 422 都带它）。 */
export function refundBreakdown(error: ApiError | null | undefined): readonly RefundBreakdownRow[] {
  const details = error?.details;
  if (!details || details.length === 0) return [];
  return details
    .filter((d) => REFUND_BREAKDOWN_FIELDS.includes(d.field))
    .map((d) => ({
      field: d.field,
      label: REFUND_BREAKDOWN_LABEL[d.field] ?? d.field,
      reason: d.reason,
    }));
}

/**
 * 明细表。
 *
 * 金额在 `reason` 里是服务端拼好的中文句子（含「分」的整数），**原样显示** ——
 * 在前端把它拆出来重新格式化，等于给同一个数字造第二个来源，
 * 而这两个来源第一次不一致的时候，没有人会知道该信哪个。
 */
export function RefundBreakdownTable({ rows }: { rows: readonly RefundBreakdownRow[] }) {
  if (rows.length === 0) return null;
  return (
    <div className="mt-3 rounded-lg border border-line bg-surface-alt p-3">
      <p className="text-xs font-medium text-fg">按 ADR 0013 §3.2 算出的扣减明细</p>
      <dl className="mt-2 space-y-1.5">
        {rows.map((r) => (
          <div key={r.field} className="flex flex-col gap-0.5 sm:flex-row sm:gap-3">
            <dt className="shrink-0 text-xs text-fg-muted sm:w-44">{r.label}</dt>
            <dd className="text-xs leading-relaxed text-fg">{r.reason}</dd>
          </div>
        ))}
      </dl>
      <p className="mt-2 text-xs leading-relaxed text-fg-subtle">
        这份明细是<strong className="font-medium text-fg">服务端拒绝这次退款时</strong>顺带给出的。契约上没有退款预览端点，
        成功的那一次明细只进审计日志（<code className="font-mono">/admin/audit</code> 的
        <code className="font-mono"> after</code> 快照）。缺口已登记。
      </p>
    </div>
  );
}

/* ────────────────────────── 页头与危险操作登记 ────────────────────────── */

/**
 * 已接线页面的页头。
 *
 * 🔴 **接好线的页面不能再用 `ModuleScaffold`** —— 那个外壳里有一块
 * 「尚未接线」和一个假三态切换器，页面真的接上 API 之后这两样都在**说假话**。
 * 保留的是它真正有价值的两样：优先级 / 移动端档位徽标，与危险操作登记表。
 */
export function ModuleHeader({
  title,
  description,
  priority,
  mobile,
  actions,
}: {
  title: string;
  description: ReactNode;
  priority: 'P1' | 'P2' | 'P3';
  mobile: 'M2' | 'M3';
  actions?: ReactNode;
}) {
  return (
    <PageHeader
      title={title}
      description={description}
      actions={actions}
      meta={
        <>
          <Badge tone={priority === 'P1' ? 'info' : 'neutral'}>{priority}</Badge>
          <Badge tone={mobile === 'M2' ? 'warn' : 'neutral'}>
            {mobile === 'M2' ? 'M2 · 手机上核心操作必须能完成' : 'M3 · 桌面优先，手机上可读即可'}
          </Badge>
        </>
      }
    />
  );
}

/**
 * 本页涉及的危险操作登记（page-inventory §4.4 的逐字誊本，取自 `lib/danger.ts`）。
 *
 * 为什么接完线还要留着：这张表是**操作者按下按钮之前**唯一能读到的
 * 「这一条为什么危险」。`DangerousAction` 展开后也会显示同样的话，
 * 但那时人已经决定要做了 —— 决定之前看到和决定之后看到，不是同一件事。
 */
export function DangerOpsNote({ codes }: { codes: readonly string[] }) {
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
 * 「这一块契约上没有」的说明块。
 *
 * 与 `NotImplementedNotice` **不是一回事**，所以长得不一样：
 * 501 是「后端还没写，写了就有」，这里是「契约里根本没有这个字段/端点，
 * 要改 openapi 才会有」。混成一句「暂不支持」会让人一直等一个不会来的版本。
 */
export function ContractGapNotice({ title, children }: { title: string; children: ReactNode }) {
  return (
    <div className="rounded-lg border border-dashed border-line bg-surface-alt/60 p-3 text-xs leading-relaxed text-fg-muted">
      <span className="font-medium text-fg">{title}</span>
      <div className="mt-1 space-y-1">{children}</div>
    </div>
  );
}

/* ────────────────────────────── 端点调用 ────────────────────────────── */

/**
 * 订单列表。
 *
 * `count` 只在第一页传 —— `COUNT(*)` 在 db-f1-micro 上是实打实的开销
 * （契约里 `CountQuery` 的注释逐字写了这条）。
 */
export function listAdminOrders(params: {
  cursor: string | null;
  q: string;
  count: boolean;
}): Promise<Page<AdminOrder>> {
  const query: { limit: number; cursor?: string; count?: boolean; q?: string } = { limit: PAGE_SIZE };
  if (params.cursor !== null) query.cursor = params.cursor;
  if (params.count) query.count = true;
  const q = params.q.trim();
  if (q !== '') query.q = q;
  return unwrapWithMeta(api().GET('/api/v1/admin/orders', { params: { query } }));
}

export function getAdminOrder(tradeNo: string): Promise<AdminOrder> {
  return unwrap(api().GET('/api/v1/admin/orders/{trade_no}', { params: { path: { trade_no: tradeNo } } }));
}

/**
 * D6 · 手工标记订单已支付。
 *
 * 🔴 TOTP 走**请求头** `X-TOTP-Code`，不是 body。放进 body 的现象是
 * 服务端读不到它 → 恒定 403 `AUTH_TOTP_REQUIRED`，而操作者会以为自己的验证器坏了。
 */
export function markAdminOrderPaid(
  tradeNo: string,
  body: { confirmation: string; reason: string; evidence_url: string },
  totp: string,
): Promise<AdminOrder> {
  return unwrap(
    api().POST('/api/v1/admin/orders/{trade_no}/mark-paid', {
      params: { path: { trade_no: tradeNo }, header: { 'X-TOTP-Code': totp } },
      body,
    }),
  );
}

/**
 * D7 · 退款。
 *
 * `amount` 省略 = 全额（服务端按 ADR 0013 §3.2 算出的**本次可退上限**），
 * 给了就必须落在 `(0, MaxAmount]`。**不是「按管理员说的退」** ——
 * 那样 D7 就退化成一个可以填任意金额的转账按钮。
 */
export function refundAdminOrder(
  tradeNo: string,
  body: { reason: string; amount?: number },
): Promise<AdminOrder> {
  return unwrap(
    api().POST('/api/v1/admin/orders/{trade_no}/refund', {
      params: { path: { trade_no: tradeNo } },
      body,
    }),
  );
}

export function listAdminPayments(params: { cursor: string | null; count: boolean }): Promise<Page<AdminPayment>> {
  const query: { limit: number; cursor?: string; count?: boolean } = { limit: PAGE_SIZE };
  if (params.cursor !== null) query.cursor = params.cursor;
  if (params.count) query.count = true;
  return unwrapWithMeta(api().GET('/api/v1/admin/payments', { params: { query } }));
}

export function listAdminUnderpaidPayments(params: {
  cursor: string | null;
  count: boolean;
}): Promise<Page<AdminPayment>> {
  const query: { limit: number; cursor?: string; count?: boolean } = { limit: PAGE_SIZE };
  if (params.cursor !== null) query.cursor = params.cursor;
  if (params.count) query.count = true;
  return unwrapWithMeta(api().GET('/api/v1/admin/payments/underpaid', { params: { query } }));
}

/** D13 · 改支付流水状态。`note` 在库里**无处可存**，只进审计（见调用点的说明）。 */
export function updateAdminPayment(
  id: number,
  body: { reason: string; state: PaymentState; note?: string },
): Promise<AdminPayment> {
  return unwrap(api().PATCH('/api/v1/admin/payments/{id}', { params: { path: { id } }, body }));
}

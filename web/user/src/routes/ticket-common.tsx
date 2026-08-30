/**
 * 工单两页（`/ticket`、`/ticket/:public_id`）共用的东西。
 *
 * 为什么单开一个文件而不是塞进 `@babelplus/shared/src/ui`：那里是全站公共资产，
 * 多个人同时接线会撞在同一个文件上；而这里的每一样都带着**工单特有的产品约束**
 * （page-inventory §3.2.6），拿到别的页面上既用不着也会误导。
 * 这个文件只被 `TicketListPage.tsx` 与 `TicketDetailPage.tsx` 引用。
 */
import { useCallback, useEffect, useRef, useState, type ReactNode } from 'react';
import { ApiError, type components } from '@babelplus/shared/api';
import { Badge, Card, ErrorState, cx, runtimeConfig } from './_imports.ts';

export type TicketCategory = components['schemas']['TicketCategory'];
export type TicketStatus = components['schemas']['TicketStatus'];
export type Ticket = components['schemas']['Ticket'];
export type TicketMessage = components['schemas']['TicketMessage'];
export type TicketDetail = components['schemas']['TicketDetail'];
export type TicketClientContext = components['schemas']['TicketClientContext'];

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
   * 存在的理由是三态纪律：回复成功后**不能**把整段会话打回 loading 重拉 ——
   * 用户刚打完字，眼前的内容突然变成骨架屏，会让人以为自己的回复没发出去。
   * 写操作拿到服务端返回的实体后直接补进来，读请求的状态一动不动。
   */
  patch(update: (previous: T) => T): void;
}

/**
 * 一个请求 = 一套三态。**刻意不做全局缓存层** ——
 * `shared/api/queries.ts` 的文件头写了缓存与状态管理的选型还没裁决，
 * 在这里引一个等于替以后的人做决定。
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

/* ────────────────────────── 分类与状态 ────────────────────────── */

/**
 * 四个分类（page-inventory §3.2.6：**必选**，没有「其他」）。
 *
 * `docsSection` 只写**排障区里的哪一片**，不拼具体文章的 URL ——
 * tutorials-spec.md 只定了信息架构，没有定任何 slug，编一个出来就是把用户导到 404。
 * TODO(P1)：文档站落地后，这里换成 `${docsUrl}/troubleshoot/<slug>` 的深链。
 */
export const TICKET_CATEGORIES: ReadonlyArray<{
  readonly value: TicketCategory;
  readonly label: string;
  readonly symptom: string;
  readonly docsSection: string;
}> = [
  {
    value: 'subscription',
    label: '订阅问题',
    symptom: '客户端拉不到节点列表、订阅更新失败、导入后是空的',
    docsSection: '排障 · 订阅类（3 篇）',
  },
  {
    value: 'node-down',
    label: '连不上 / 速度',
    symptom: '全部节点超时、连上就断、能连但很慢',
    docsSection: '排障 · 连接类（4 篇）与速度类（2 篇）',
  },
  {
    value: 'billing',
    label: '支付与账单',
    symptom: '付了款没到账、订单状态不对、要发票',
    docsSection: '账户与账单 · 支付方式与开票',
  },
  {
    value: 'account',
    label: '账号本身',
    symptom: '登录不上、订阅链接疑似泄漏、要改邮箱',
    docsSection: '账户与账单 · 订阅泄漏了怎么办',
  },
];

export function categoryLabel(category: TicketCategory): string {
  return TICKET_CATEGORIES.find((c) => c.value === category)?.label ?? category;
}

const STATUS_META: Record<TicketStatus, { label: string; tone: 'neutral' | 'ok' | 'warn' | 'info' }> = {
  // 「待客服回复」而不是「打开」：`open` 是数据库里的词，用户要知道的是**球在谁那边**。
  open: { label: '待客服回复', tone: 'info' },
  pending: { label: '待你补充', tone: 'warn' },
  replied: { label: '客服已回复', tone: 'ok' },
  closed: { label: '已关闭', tone: 'neutral' },
};

export function StatusBadge({ status }: { status: TicketStatus }) {
  const meta = STATUS_META[status] ?? { label: status, tone: 'neutral' as const };
  return <Badge tone={meta.tone}>{meta.label}</Badge>;
}

export function statusLabel(status: TicketStatus): string {
  return STATUS_META[status]?.label ?? status;
}

/* ────────────────────────── 错误分支 ────────────────────────── */

/**
 * 501 的判据。
 *
 * 后端现在 128 个 operation 里只实现了 18 个，其余全部落在
 * `api/cmd/server/main.go` 的 `responseErrorHandler` 上，回 **501 + `NOT_IMPLEMENTED`**。
 * 注意 `NOT_IMPLEMENTED` **不在 openapi 的 `ErrorCode` enum 里**（它是错误映射层直接写出去的），
 * 所以只能按字符串比。按状态码判也行，但那会把将来任何一个真的 501 也算进来 ——
 * 而「端点没写」和「端点写了但依赖没实现」对用户是两句不同的话。
 */
export const NOT_IMPLEMENTED_CODE = 'NOT_IMPLEMENTED';

export function isNotImplemented(error: ApiError | null | undefined): boolean {
  return error?.code === NOT_IMPLEMENTED_CODE;
}

/**
 * `ErrorCode` → 文案。**工单两页唯一按 code 分支的地方**，页面里不许再写第二处
 * （api-contract §2.3：禁止匹配 `message` 做分支；README §8 记着「前端没有按 code 分支的文案表」）。
 *
 * `retrySeconds` 由调用方从 `error.retryAfterSeconds` 起算并每秒递减 ——
 * 读不到这个头就传 `null`，**绝不自己编一个秒数**。
 */
export function ticketErrorCopy(
  error: ApiError,
  options: { fallbackTitle?: string; retrySeconds?: number | null } = {},
): { title: string; description: string } {
  const retrySeconds = options.retrySeconds ?? null;
  switch (error.code) {
    case NOT_IMPLEMENTED_CODE:
      return {
        title: '该功能尚未开放',
        description: '工单系统的这一部分后端还没上线。不是你的操作有问题，重试也不会有变化。',
      };
    case 'RESOURCE_NOT_FOUND':
      return {
        title: '找不到这个工单',
        description: '工单号可能不对，或者它不属于当前账号。',
      };
    case 'STATE_CONFLICT':
      return {
        title: '工单状态已经变了',
        description: '这张工单可能已经被关闭。刷新一下看看最新的会话再决定要不要继续。',
      };
    case 'VALIDATION_FAILED':
    case 'VALIDATION_MALFORMED_BODY':
      return {
        title: '填写有误',
        description: fieldReasons(error) ?? error.message,
      };
    case 'QUOTA_RATE_LIMITED':
      return {
        title: '提交太频繁',
        description:
          retrySeconds === null
            ? '短时间内提交了太多次，稍后再试。'
            : `短时间内提交了太多次，${retrySeconds} 秒后可以再试。`,
      };
    case 'AUTH_PERMISSION_DENIED':
      return {
        title: '这个账号已被封禁',
        description: '会话是有效的，但账号不可用。重新登录不会有帮助，请通过邮件联系我们。',
      };
    default:
      break;
  }
  // 没有可依的 code（网络层失败 / 非信封响应）时按五类归一走。
  switch (error.kind) {
    case 'offline':
      return { title: '连不上面板', description: '当前网络到面板的连接失败。可以试试页脚的备用域名。' };
    case 'server':
      return { title: '我们这边出了问题', description: '不是你的账号或网络的问题，稍后再试一次。' };
    case 'unauthorized':
      return { title: '需要重新登录', description: '登录状态已过期，重新登录后回到这一页继续。' };
    default:
      return { title: options.fallbackTitle ?? '操作没能完成', description: error.message };
  }
}

/** `VALIDATION_FAILED` 的字段级明细。契约保证有 `details` 时逐字段列出。 */
function fieldReasons(error: ApiError): string | null {
  const details = error.details;
  if (!details || details.length === 0) return null;
  return details.map((d) => `${d.field}：${d.reason}`).join('；');
}

/**
 * 501 专用的提示块。
 *
 * **刻意不用 `ErrorState`**：501 的 `kind` 是 `server`，而 `ErrorState` 在 server 态下会说
 * 「我们这边出了问题」并把人推去状态页 —— 状态页上一切正常，用户只会更困惑。
 * 「还没做」不是故障，红色警告框在这里是误报。
 */
export function NotImplementedNotice({ what, requestId }: { what: string; requestId?: string | undefined }) {
  const cfg = runtimeConfig();
  return (
    <Card className="border-dashed">
      <h3 className="text-base font-semibold text-fg">该功能尚未开放</h3>
      <p className="mt-1.5 text-sm leading-relaxed text-fg-muted">
        {what}的后端还没上线。不是你的操作有问题，重试也不会有变化。
        {cfg.supportEmail ? (
          <>
            {' '}
            现在需要人工处理的话，直接发邮件到 <span className="font-mono text-fg">{cfg.supportEmail}</span>
            {' '}—— 邮件是唯一不依赖面板的通道（ADR 0002）。
          </>
        ) : null}
      </p>
      {requestId ? <p className="mt-3 font-mono text-xs text-fg-subtle">请求号 {requestId}</p> : null}
    </Card>
  );
}

/**
 * 读请求失败时的整块错误态。501 走上面那块，其余走全站统一的 `ErrorState`。
 *
 * 401 / 403 / 网络不可达三类的处置是全站统一的（RouteScaffold 的注释里写了理由），
 * 所以这里只覆盖标题与说明，不改 `ErrorState` 自己那套动作按钮与备用域名列表。
 */
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
  const copy = ticketErrorCopy(error, { fallbackTitle: `${what}没能加载` });
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

/* ────────────────────────── 表单原语 ────────────────────────── */

/**
 * 表单级错误位。`role="alert"` 是必须的：提交失败时焦点还在按钮上，
 * 不播报的话屏幕阅读器用户只会觉得「点了没反应」。
 */
export function FormAlert({ children }: { children: ReactNode }) {
  return (
    <div role="alert" className="rounded-lg border border-danger/30 bg-danger/10 px-3 py-2 text-sm text-danger">
      {children}
    </div>
  );
}

const CONTROL_BASE =
  'w-full rounded-lg border border-line bg-surface px-3 text-base text-fg ' +
  'placeholder:text-fg-subtle focus-visible:border-accent focus-visible:outline-2 ' +
  'focus-visible:outline-offset-1 focus-visible:outline-accent disabled:opacity-50';

// 16px 起步（`text-base`）：iOS Safari 在 <16px 的输入框获得焦点时会自动放大页面，
// 放大后 375px 布局就出现横向滚动，直接违反 M1 移动优先。理由同 AuthForm.tsx。

export function TextField({
  label,
  name,
  value,
  onChange,
  hint,
  ...rest
}: {
  label: string;
  name: string;
  value: string;
  onChange: (value: string) => void;
  hint?: ReactNode;
  maxLength?: number;
  required?: boolean;
  disabled?: boolean;
  placeholder?: string;
}) {
  const id = `tk-${name}`;
  return (
    <div>
      <label htmlFor={id} className="mb-1.5 block text-sm font-medium text-fg">
        {label}
      </label>
      <input
        id={id}
        name={name}
        value={value}
        onChange={(event) => onChange(event.target.value)}
        className={cx(CONTROL_BASE, 'min-h-11')}
        {...rest}
      />
      {hint ? <p className="mt-1.5 text-xs leading-relaxed text-fg-muted">{hint}</p> : null}
    </div>
  );
}

export function TextArea({
  label,
  name,
  value,
  onChange,
  hint,
  rows = 6,
  ...rest
}: {
  label: string;
  name: string;
  value: string;
  onChange: (value: string) => void;
  hint?: ReactNode;
  rows?: number;
  maxLength?: number;
  required?: boolean;
  disabled?: boolean;
  placeholder?: string;
}) {
  const id = `tk-${name}`;
  return (
    <div>
      <label htmlFor={id} className="mb-1.5 block text-sm font-medium text-fg">
        {label}
      </label>
      <textarea
        id={id}
        name={name}
        rows={rows}
        value={value}
        onChange={(event) => onChange(event.target.value)}
        className={cx(CONTROL_BASE, 'py-2.5 leading-relaxed')}
        {...rest}
      />
      {hint ? <p className="mt-1.5 text-xs leading-relaxed text-fg-muted">{hint}</p> : null}
    </div>
  );
}

/* ────────────────────────── 429 倒计时 ────────────────────────── */

/**
 * 解锁倒计时。秒数只能来自 `Retry-After`（CORS 里已 expose），
 * 读不到就返回 `null` 并**不显示倒计时** —— 编一个秒数会在用户眼皮底下走错。
 */
export function useRetryCountdown(): {
  seconds: number | null;
  start: (seconds: number | undefined) => void;
  clear: () => void;
} {
  const [seconds, setSeconds] = useState<number | null>(null);

  useEffect(() => {
    if (seconds === null || seconds <= 0) return;
    const timer = window.setInterval(() => {
      setSeconds((prev) => (prev === null || prev <= 1 ? null : prev - 1));
    }, 1000);
    return () => window.clearInterval(timer);
  }, [seconds]);

  const start = useCallback((next: number | undefined) => {
    setSeconds(next === undefined || next <= 0 ? null : next);
  }, []);
  const clear = useCallback(() => setSeconds(null), []);

  return { seconds, start, clear };
}

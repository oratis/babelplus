/**
 * `/plan`、`/order`、`/order/:trade_no` 三页共用的**带 UI 的**件：
 * `ErrorCode` 文案表、二次确认、幂等键、复制、倒计时。
 * 纯函数（金额 / 周期 / 状态映射）在同目录的 `format.ts`。
 *
 * 为什么不放进 `web/shared/src/ui`：那里是三个前端共用的地基，多个人同时接线会撞在同一个文件上；
 * 而这里的每一样都带着**支付路径特有的产品约束**（ADR 0012、page-inventory §3.2.5），
 * 拿到别的页面上既用不着也会误导。这个文件只被上面那三页引用。
 *
 * 通用件（`useApiQuery` / `NotImplementedNotice` / `FormAlert` / `TextField`）直接复用
 * `../ticket-common.tsx`，不在这里重抄一份 —— 抄一份的代价是两处三态实现慢慢漂移。
 */
import { useCallback, useEffect, useRef, useState, type ReactNode } from 'react';
import { ApiError } from '@babelplus/shared/api';
import { Button, Card, ErrorState, Icon } from '../_imports.ts';
import { NotImplementedNotice, isNotImplemented } from '../ticket-common.tsx';

/* ────────────────────────── ErrorCode → 文案 ────────────────────────── */

/**
 * 支付路径唯一按 `code` 分支的地方，三页共用。**页面里不许再写第二处。**
 *
 * 🔴 按 `ErrorCode` 分支而**不是**按 HTTP 状态码 —— 这条在这条路径上比在别处更要命：
 *  - 401 上挂着两个含义完全相反的码：`AUTH_TOKEN_INVALID`（会话过期，重登有用）与
 *    `AUTH_PERMISSION_DENIED`（账号被封，重登只会再被拒一次，`middleware/user.go` 点名了这个来回）；
 *  - 409 上挂着三个码：`STATE_CONFLICT`（订单状态变了）、`STATE_IDEMPOTENCY_MISMATCH`
 *    （同一个幂等键换了载荷）、`PAYMENT_ORDER_EXPIRED`（订单过期）。
 *    三者对用户的下一步动作分别是「刷新」「重新下单」「重新下单但别再付一次」，
 *    按 409 一句「操作冲突」打发掉，用户最可能做的事是**再付一次**。
 *
 * `retrySeconds` 由调用方从 `error.retryAfterSeconds` 起算并每秒递减，读不到就传 `null`，
 * **绝不自己编一个秒数**（编出来的倒计时会在用户眼皮底下走错）。
 */
export function billingErrorCopy(
  error: ApiError,
  options: { fallbackTitle?: string; retrySeconds?: number | null } = {},
): { title: string; description: string } {
  const retrySeconds = options.retrySeconds ?? null;
  switch (error.code) {
    case 'NOT_IMPLEMENTED':
      return {
        title: '该功能尚未开放',
        description: '这一段后端还没上线。不是你的操作有问题，重试也不会有变化。',
      };
    case 'RESOURCE_NOT_FOUND':
      return {
        title: '找不到这个订单',
        description: '订单号可能不对，或者它不属于当前账号。',
      };
    case 'STATE_CONFLICT':
      return {
        title: '订单状态已经变了',
        description:
          '这张订单可能已经被取消、已经支付，或者上一次提交还在处理中。刷新看看最新状态再决定下一步，不要重复付款。',
      };
    case 'STATE_IDEMPOTENCY_MISMATCH':
      return {
        title: '这次提交和上一次不一样',
        description:
          '同一个幂等键只能对应同一份内容。你可能在提交的过程中改了套餐、周期或优惠码 —— 重新点一次下单会用一把新的键。',
      };
    case 'PAYMENT_ORDER_EXPIRED':
      return {
        title: '这张订单已经过期',
        description:
          '付款窗口过了，这张单作废。但收款地址永远认账 —— 如果你已经转出去了，钱会自动记到你的账户余额上，可以用来重新下单。',
      };
    case 'PAYMENT_UNDERPAID':
      // 契约明写 `PAYMENT_UNDERPAID` 走 200 不走错误通道（`getOrderPayment` 的描述）。
      // 真在错误通道上收到它，说明后端换了口径 —— 那也不能显示成「支付失败」。
      return {
        title: '这笔付款金额不足',
        description: '钱已经收到了，只是还差一点。请看下面收银台里的「已收到 / 还差」两个数。',
      };
    case 'VALIDATION_FAILED':
    case 'VALIDATION_MALFORMED_BODY':
      return {
        title: '这一单不能这样下',
        description: fieldReasons(error) ?? error.message,
      };
    case 'QUOTA_RATE_LIMITED':
      return {
        title: '操作太频繁',
        description:
          retrySeconds === null
            ? '短时间内提交了太多次，稍后再试。你的订单没有任何变化。'
            : `短时间内提交了太多次，${retrySeconds} 秒后可以再试。你的订单没有任何变化。`,
      };
    case 'AUTH_PERMISSION_DENIED':
      return {
        title: '这个账号已被封禁',
        description: '会话是有效的，但账号不可用。重新登录不会有帮助，请通过邮件联系我们。',
      };
    case 'INTERNAL_DEPENDENCY_DOWN':
      return {
        title: '这一步依赖的外部服务暂时不可用',
        description:
          '可能是收款地址暂时开不出来，或者链上查询打不通。你的订单还在，也没有产生任何扣款，稍后再试一次。',
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
 * 读请求失败时的整块错误态。501 走 `NotImplementedNotice`（中性提示，不是红色故障框），
 * 其余走全站统一的 `ErrorState`。
 *
 * **不复用 `ticket-common` 的 `QueryErrorState`**：它内部调的是 `ticketErrorCopy`，
 * `RESOURCE_NOT_FOUND` 会说成「找不到这个工单」—— 在订单页上就是一句假话。
 * 501 的判据（`isNotImplemented`）与提示块（`NotImplementedNotice`）本身是通用的，那两个直接复用。
 */
export function BillingErrorState({
  error,
  what,
  onRetry,
  extra,
}: {
  error: ApiError;
  what: string;
  onRetry?: () => void;
  extra?: ReactNode;
}) {
  if (isNotImplemented(error)) {
    return <NotImplementedNotice what={what} requestId={error.requestId} />;
  }
  const copy = billingErrorCopy(error, { fallbackTitle: `${what}没能加载` });
  return (
    <ErrorState
      kind={error.kind}
      title={copy.title}
      description={copy.description}
      requestId={error.requestId}
      onRetry={onRetry}
      extra={extra}
    />
  );
}

/* ────────────────────────── 幂等键 ────────────────────────── */

/**
 * 一把跟着**载荷**走的幂等键（api-contract §9：`createOrder` / `payOrder` 都要 `Idempotency-Key`）。
 *
 * 两条相反的错误都要避开，所以它既不是「每次点击新生成」也不是「整页一把」：
 *  - 每次点击都换一把 → 网络超时后重试会**真的下出第二张单**，而幂等键的全部意义就是挡住这个；
 *  - 整页一把且载荷变了还用它 → 服务端回 409 `STATE_IDEMPOTENCY_MISMATCH`
 *    （`beginOrderIdempotency`：同键不同载荷必须拒绝），用户看到的是「莫名其妙下不了单」。
 *
 * 所以：**载荷不变时键不变，载荷一变就换新的一把。**`signature` 由调用方拼成一个字符串，
 * 它必须覆盖请求体里每一个会被服务端算进指纹的字段。
 *
 * `crypto.randomUUID` 在**非安全上下文**（http 的镜像域名、部分内嵌浏览器）下不存在 ——
 * 而备用域名恰恰可能是这种环境。退化实现只要满足服务端的形态要求
 * （`ErrIdempotencyKeyMalformed`：长度 8–128 的可见 ASCII）即可，它不需要密码学强度：
 * 这把键是**幂等标识**不是凭据，服务端还按 `(key, user_id)` 隔离。
 */
export function useIdempotencyKey(signature: string): string {
  const ref = useRef<{ signature: string; key: string } | null>(null);
  if (ref.current === null || ref.current.signature !== signature) {
    ref.current = { signature, key: newIdempotencyKey() };
  }
  return ref.current.key;
}

export function newIdempotencyKey(): string {
  const uuid = globalThis.crypto?.randomUUID;
  if (typeof uuid === 'function') return globalThis.crypto.randomUUID();
  let out = '';
  for (let i = 0; i < 32; i += 1) {
    out += Math.floor(Math.random() * 36).toString(36);
  }
  return `bp-${Date.now().toString(36)}-${out}`;
}

/* ────────────────────────── 二次确认 ────────────────────────── */

/**
 * 写操作的二次确认（api-contract §9 对写操作的要求）。
 *
 * `consequences` 是**必填数组**不是可选说明：只写「确定吗？」的确认框只是把误触从一次变成两次，
 * 并不让用户知道自己在做什么。
 *
 * **不用 `<dialog>` / 不做模态**：roadmap §5.2 明记组件库未选型、
 * 「后台 16 条危险操作需要的确认对话框（焦点管理 + 键盘 + 屏幕阅读器）现在不存在」。
 * 一个焦点管理做错的模态框对键盘与读屏用户是**比没有确认更糟**的东西，
 * 而就地展开的确认块不需要焦点陷阱就能用。
 */
export function ConfirmPanel({
  open,
  title,
  consequences,
  confirmLabel,
  tone = 'primary',
  pending = false,
  error = null,
  onCancel,
  onConfirm,
}: {
  open: boolean;
  title: string;
  consequences: readonly ReactNode[];
  confirmLabel: string;
  tone?: 'primary' | 'danger';
  pending?: boolean;
  /** 确认后失败的话，错误留在框里显示，**不关框** —— 关掉了用户就不知道自己那一下有没有生效。 */
  error?: ReactNode;
  onCancel: () => void;
  onConfirm: () => void;
}) {
  if (!open) return null;
  return (
    <div
      role="group"
      aria-label={title}
      className="mt-3 rounded-xl border-2 border-accent/40 bg-accent/5 p-4"
    >
      <h3 className="text-base font-semibold text-fg">{title}</h3>
      <ul className="mt-2 space-y-1 text-sm leading-relaxed text-fg-muted">
        {consequences.map((line, i) => (
          <li key={i} className="flex gap-2">
            <span aria-hidden="true" className="text-accent">
              ·
            </span>
            <span>{line}</span>
          </li>
        ))}
      </ul>

      {error ? (
        <div role="alert" className="mt-3 rounded-lg bg-danger/10 px-3 py-2 text-sm text-danger">
          {error}
        </div>
      ) : null}

      <div className="mt-4 flex flex-wrap gap-2">
        <Button tone={tone} disabled={pending} onClick={onConfirm}>
          {pending ? '正在提交…' : confirmLabel}
        </Button>
        <Button disabled={pending} onClick={onCancel}>
          再想想
        </Button>
      </div>
    </div>
  );
}

/* ────────────────────────── 复制 ────────────────────────── */

export type CopyState = 'idle' | 'ok' | 'failed';

/**
 * 复制收款地址 / 金额。
 *
 * `navigator.clipboard` 在**非安全上下文**下不存在（同上，备用域名可能就是这种环境）。
 * 失败必须是一个**可见的状态**：这一页上要复制的是收款地址，
 * 「以为复制成功了、其实剪贴板里是上一次的内容」在这里等于把钱转到别的地方。
 */
export function useClipboard(): { state: CopyState; copy: (text: string) => void } {
  const [state, setState] = useState<CopyState>('idle');

  useEffect(() => {
    if (state === 'idle') return;
    const timer = window.setTimeout(() => setState('idle'), 3000);
    return () => window.clearTimeout(timer);
  }, [state]);

  const copy = useCallback((text: string) => {
    const clipboard = navigator.clipboard as Clipboard | undefined;
    if (!clipboard || typeof clipboard.writeText !== 'function') {
      setState('failed');
      return;
    }
    void clipboard.writeText(text).then(
      () => setState('ok'),
      () => setState('failed'),
    );
  }, []);

  return { state, copy };
}

/** 一行「值 + 复制按钮」。地址与金额都要它 —— 手抄一个 34 位的 TRON 地址是不可接受的交互。 */
export function CopyRow({
  label,
  value,
  hint,
}: {
  label: string;
  value: string;
  hint?: ReactNode;
}) {
  const clipboard = useClipboard();
  return (
    <div>
      <div className="flex flex-wrap items-baseline justify-between gap-x-3 gap-y-1">
        <span className="text-sm text-fg-muted">{label}</span>
        <Button
          className="min-h-9 px-3 text-xs"
          onClick={() => clipboard.copy(value)}
          aria-label={`复制${label}`}
        >
          <Icon.Copy size={14} />
          {clipboard.state === 'ok' ? '已复制' : clipboard.state === 'failed' ? '复制失败' : '复制'}
        </Button>
      </div>
      {/* `select-all` + 换行显示：复制失败时用户还能手动全选。 */}
      <p className="mt-1 break-all font-mono text-base text-fg select-all">{value}</p>
      {clipboard.state === 'failed' ? (
        <p className="mt-1 text-xs text-warn">
          这个浏览器不让我们写剪贴板（多半是 http 的备用域名）。上面这一行可以长按 / 双击全选后手动复制。
        </p>
      ) : null}
      {hint ? <p className="mt-1.5 text-xs leading-relaxed text-fg-muted">{hint}</p> : null}
    </div>
  );
}

/* ────────────────────────── 倒计时 ────────────────────────── */

/**
 * 到某个时刻为止还剩多少秒。汇率锁定倒计时（`quote_expires_at`）与订单过期（`expires_at`）都用它。
 *
 * 🔴 秒数只从**服务端给的时刻**算，不从「进页面时是 30 分钟」倒着数：
 * 用户关掉页面十分钟后再回来，倒着数会显示成还剩 30 分钟 ——
 * 而「可以关掉页面再回来」正是这一页存在的理由（page-inventory §3.2.5）。
 *
 * 解析不出来（缺字段 / 不是时间）返回 `null`，此时调用方**不显示倒计时**，不编一个数出来。
 */
export function useSecondsUntil(iso: string | null | undefined): number | null {
  const target = iso ? Date.parse(iso) : Number.NaN;
  const valid = Number.isFinite(target);
  const [seconds, setSeconds] = useState<number | null>(() =>
    valid ? Math.max(0, Math.round((target - Date.now()) / 1000)) : null,
  );

  useEffect(() => {
    if (!valid) {
      setSeconds(null);
      return;
    }
    const tick = () => setSeconds(Math.max(0, Math.round((target - Date.now()) / 1000)));
    tick();
    const timer = window.setInterval(tick, 1000);
    return () => window.clearInterval(timer);
  }, [target, valid]);

  return seconds;
}

/** 秒 → 「12 分 34 秒」。倒计时只用在 30 分钟量级上，不做小时档。 */
export function formatDuration(seconds: number): string {
  const s = Math.max(0, Math.trunc(seconds));
  const m = Math.floor(s / 60);
  return m > 0 ? `${m} 分 ${String(s % 60).padStart(2, '0')} 秒` : `${s} 秒`;
}

/* ────────────────────────── 小件 ────────────────────────── */

/** 金额明细的一行。`—` 由调用方决定，这里不猜。 */
export function AmountRow({
  label,
  value,
  hint,
  strong = false,
}: {
  label: string;
  value: ReactNode;
  hint?: ReactNode;
  strong?: boolean;
}) {
  return (
    <div className="flex flex-wrap items-baseline justify-between gap-x-3 gap-y-0.5 py-1.5">
      <dt className={strong ? 'text-sm font-medium text-fg' : 'text-sm text-fg-muted'}>
        {label}
        {hint ? <span className="ml-2 text-xs text-fg-subtle">{hint}</span> : null}
      </dt>
      <dd className={strong ? 'text-lg font-semibold text-fg' : 'text-sm text-fg'}>{value}</dd>
    </div>
  );
}

/** 卡片内的小号提示块（既不是错误也不是空态时用）。 */
export function NoticeBlock({ tone, children }: { tone: 'info' | 'warn'; children: ReactNode }) {
  const cls =
    tone === 'warn'
      ? 'border-warn/30 bg-warn/10 text-warn'
      : 'border-accent/30 bg-accent/5 text-fg-muted';
  return <div className={`rounded-lg border p-3 text-sm leading-relaxed ${cls}`}>{children}</div>;
}

/** 空壳卡片，加载态用。**不用 spinner** —— §2.2：跨境往返数百毫秒到数秒，spinner 会被读成「卡死」。 */
export function BlockSkeleton({ rows = 3 }: { rows?: number }) {
  return (
    <Card>
      <div className="space-y-2" aria-busy="true" aria-live="polite">
        {Array.from({ length: rows }, (_, i) => (
          <div
            key={i}
            className={`h-4 animate-pulse rounded-md bg-skeleton ${i === rows - 1 ? 'w-2/5' : 'w-full'}`}
            aria-hidden="true"
          />
        ))}
      </div>
    </Card>
  );
}

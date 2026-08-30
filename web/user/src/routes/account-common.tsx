/**
 * 「账户 / 用量 / 钱包 / 邀请」四类页面共用的东西。
 *
 * 为什么单开一个文件而不是塞进 `@babelplus/shared/src/ui`：理由与 `ticket-common.tsx`
 * 那句一模一样 —— 那里是全站公共资产，多个人同时接线会撞在同一个文件上。
 * 而这里的每一样都带着**钱与账号特有的产品约束**（ADR 0013、product-brief §6、
 * 定价修订 C6），拿到别的页面上既用不着也会误导。
 *
 * 引用者只有五个：ProfilePage / ProfileTwoFactorPage / UsagePage / WalletPage / InvitePage。
 *
 * 🔴 **通用件不在这里重造。** `useApiQuery` / `QueryErrorState` / `FormAlert` /
 * `TextField` / `useRetryCountdown` / `asApiError` 全部从 `ticket-common.tsx` 直接 import ——
 * 它们与工单没有任何耦合（文件名只是历史），复制一份出来的唯一后果是
 * 「501 的分支以后要改两处，而第二处会被忘掉」。
 */
import { useState, type ReactNode } from 'react';
import type { ApiError, components } from '@babelplus/shared/api';
import { Button, Card, SkeletonCard, cx, formatCny } from './_imports.ts';
import { QueryErrorState, isNotImplemented, type ApiQuery } from './ticket-common.tsx';

export type Wallet = components['schemas']['Wallet'];
export type WalletTransaction = components['schemas']['WalletTransaction'];
export type InviteCode = components['schemas']['InviteCode'];
export type Commission = components['schemas']['Commission'];
export type NotificationPrefs = components['schemas']['NotificationPrefs'];
export type UsageSeries = components['schemas']['UsageSeries'];
export type UsagePoint = components['schemas']['UsagePoint'];
export type SubscriptionFetchLogEntry = components['schemas']['SubscriptionFetchLogEntry'];
export type SubscriptionSummary = components['schemas']['SubscriptionSummary'];
export type CurrentUser = components['schemas']['CurrentUser'];

/* ────────────────────── 一个请求 = 一套三态 ────────────────────── */

/**
 * 把「加载 / 错误 / 就绪」三条分支收成一个组件。
 *
 * 存在的理由是**纪律，不是省字数**：这五页里最多的一页有四个互相独立的请求，
 * 手写三条 `if` × 4 的写法下，「顺手把两个请求合并成一个 loading」是最容易发生的退化，
 * 而它的后果正是 page-inventory §2.2 禁止的那种整页 loading ——
 * 一个次要区块（比如订阅拉取审计）挂掉会把主区块（用量曲线）一起吞掉。
 * 每个 `<QuerySection>` 只认自己那一个 `query`，结构上就无法合并。
 *
 * 501 的呈现不在这里分支：`QueryErrorState` 内部已经先判 `isNotImplemented`
 * 再决定渲染「该功能尚未开放」还是普通错误态。
 */
export function QuerySection<T>({
  query,
  what,
  skeleton,
  children,
}: {
  query: ApiQuery<T>;
  /** 出错时说「什么」没能加载。会被拼进标题，用名词短语，如「余额」。 */
  what: string;
  skeleton?: ReactNode;
  children: (data: T) => ReactNode;
}) {
  if (query.state === 'loading') return <>{skeleton ?? <SkeletonCard lines={3} />}</>;
  if (query.state === 'error' && query.error) {
    return <QueryErrorState error={query.error} what={what} onRetry={query.reload} />;
  }
  if (query.data === null) return null;
  return <>{children(query.data)}</>;
}

/* ────────────────────────── 二次确认 ────────────────────────── */

/**
 * 两段式确认按钮：第一次点击**只切换到确认态**，第二次才真的发请求。
 *
 * 为什么不用 `window.confirm`：jsdom 里它是个 no-op，线上它是个不可样式化的模态框，
 * 且移动端浏览器允许用户勾选「不再显示」—— 一个可以被永久关掉的确认，等于没有确认。
 *
 * ⚠️ **这不是幂等。** api-contract §9.1 的幂等总表里只有下单与支付两条，
 * `createInviteCode` / `transferCommission` / `changePassword` **都不在表里**，
 * 服务端不认 `Idempotency-Key`（生成类型里这三个端点的 `header` 是 `never`）。
 * 所以这里能提供的只有「挡住重复点击」这一半：`pending` 期间按钮禁用。
 * 「请求超时后重发导致做了两次」这个缺口是真实存在的，登记在交付说明里，不假装它不存在。
 */
export function ConfirmAction({
  label,
  confirmLabel,
  question,
  tone = 'primary',
  pending = false,
  disabled = false,
  onConfirm,
}: {
  label: string;
  /** 确认态的按钮文案。要**重复一遍将要发生的事**，不能只写「确定」。 */
  confirmLabel: string;
  /** 确认态旁边的一句话，说清代价与是否可撤销。 */
  question: ReactNode;
  tone?: 'primary' | 'danger';
  pending?: boolean;
  disabled?: boolean;
  onConfirm: () => void;
}) {
  const [armed, setArmed] = useState(false);

  if (!armed) {
    return (
      <Button tone={tone} disabled={disabled || pending} onClick={() => setArmed(true)}>
        {label}
      </Button>
    );
  }

  return (
    <div className="flex w-full flex-col gap-2 rounded-lg border border-line bg-surface-alt p-3">
      <p className="text-sm leading-relaxed text-fg">{question}</p>
      <div className="flex flex-wrap gap-2">
        <Button
          tone={tone}
          disabled={disabled || pending}
          onClick={() => {
            setArmed(false);
            onConfirm();
          }}
        >
          {pending ? '正在处理…' : confirmLabel}
        </Button>
        <Button onClick={() => setArmed(false)} disabled={pending}>
          取消
        </Button>
      </div>
    </div>
  );
}

/* ────────────────────────── 金额与钱的话术 ────────────────────────── */

/**
 * 一行金额。`formatCny` 走整数除模，**不许在任何地方用浮点算钱**（api-contract §2.6）。
 *
 * `hint` 不是装饰位：钱包页的每一个数字都需要一句「这笔钱能干什么」，
 * 否则用户会自己脑补一个（而他脑补的那个多半是「能提现」）。
 */
export function MoneyRow({
  label,
  cents,
  hint,
  emphasis = false,
}: {
  label: ReactNode;
  cents: number;
  hint?: ReactNode;
  emphasis?: boolean;
}) {
  return (
    <div className="flex flex-col gap-0.5 border-b border-line py-2.5 last:border-b-0">
      <div className="flex flex-wrap items-baseline justify-between gap-x-3">
        <span className="text-sm text-fg-muted">{label}</span>
        <span
          className={cx(
            'font-mono tabular-nums',
            emphasis ? 'text-xl font-semibold text-fg' : 'text-base text-fg',
          )}
        >
          {formatCny(cents)}
        </span>
      </div>
      {hint ? <p className="text-xs leading-relaxed text-fg-subtle">{hint}</p> : null}
    </div>
  );
}

/**
 * 🔴 **资金合规底线的那句话**（product-brief §6：「不做提现 —— 钱包余额仅可消费」）。
 *
 * 常驻在 `/wallet` 顶部，且在 `/invite` 的划转入口旁边再说一次。
 * 两处**用同一个常量**，不是为了少打字 —— 而是这句话一旦在两处漂移，
 * 就会出现「钱包页说不可提现、邀请页说划转到余额」这种让人以为划转出去的钱另有出路的组合。
 */
export const NO_WITHDRAW_NOTICE =
  '余额只能用于购买套餐或流量包，不支持退回原支付方式，也不支持转出。系统里没有提现这个动作。';

/**
 * 🔴 **返佣是一次性定额，不是按订单金额的比例**（定价修订 C6，已落进 pricing-and-plans §5）。
 *
 * 被邀请用户完成**首个**周期订单后，邀请人拿 `10% × 该档月付标价`，
 * **与该订单实际周期无关，每位被邀请用户只发一次**。
 *
 * 为什么这条必须写成常量并在测试里钉死：原口径「按订单金额 10%」把 24 格里的 **4 格**
 * 打穿 1.20× 毛利地板（最差 1.1474×）。把页面文案写成「订单金额的 10%」不会有任何报错 ——
 * 它只会让用户按错误的预期算收益，然后在年付订单上发现少了一大截。
 * 后端已经按这个口径发钱（`handler/order.go` 的 C6 计提、`wallet_test.go` 的 720/1590/3580）。
 */
export const COMMISSION_TIERS: ReadonlyArray<{ readonly plan: string; readonly cents: number }> = [
  { plan: '轻量', cents: 720 },
  { plan: '标准', cents: 1590 },
  { plan: '重度', cents: 3580 },
];

/** 返佣口径的一句话说明。**不许改写成比例形态**（见 `COMMISSION_TIERS`）。 */
export const COMMISSION_RULE_TEXT =
  '返佣是一次性定额，按被邀请人首单所在档位的月付标价 10% 计，与他买的周期长短无关，每人只发一次。';

/* ────────────────────────── 订阅资格 ────────────────────────── */

/**
 * 「这个账号现在有没有有效订阅」。
 *
 * `/invite` 的生成按钮挂在这上面而不是「已登录」上（user-journey §3.1）——
 * 否则邀请制会退化成链式开放注册：任何人注册一个空账号就能继续发码。
 *
 * ⚠️ **判据比 dashboard 的 `hasSubscription` 严**：那里回答的是「要不要显示订阅卡」，
 * 这里回答的是「有没有在付费」。所以**过期的订阅不算**：`expired_at` 已过 = 没有有效订阅。
 * `expired_at` 缺失在契约里明确表示**不限时套餐**（不是「读不到」），算有效。
 *
 * 🔴 **这道闸只在前端。** 后端 `CreateInviteCode` 当前只校验未核销码的名额（≤ 3），
 * **不校验订阅**（`handler/wallet.go`，那里一行订阅查询都没有）——
 * 也就是说直接 `curl` 的人绕得过去。前端的 gate 是产品表达，不是安全边界，
 * 这个缺口登记在交付说明里。
 */
export function hasActiveSubscription(user: CurrentUser | null): boolean {
  const summary = user?.subscription;
  if (!summary) return false;
  const bought = Boolean(summary.plan_name) || summary.total_bytes > 0;
  if (!bought) return false;
  if (!summary.expired_at) return true; // 契约：null = 不限时套餐
  const expiry = Date.parse(summary.expired_at);
  // 解不出来的日期**当作有效**：把一个正在付费的用户挡在门外，比多发一个码糟得多。
  return Number.isNaN(expiry) || expiry > Date.now();
}

/* ────────────────────────── 写操作的错误呈现 ────────────────────────── */

/**
 * 表单 / 按钮旁边那一小块错误提示（读请求走 `QueryErrorState`，那是整块的）。
 *
 * `role="alert"` 的理由同 `ticket-common.tsx` 的 `FormAlert`：
 * 提交失败时焦点还在按钮上，不播报的话屏幕阅读器用户只会觉得「点了没反应」。
 */
export function WriteError({ title, description }: { title: string; description: ReactNode }) {
  return (
    <div
      role="alert"
      className="rounded-lg border border-danger/30 bg-danger/10 px-3 py-2 text-sm leading-relaxed text-danger"
    >
      <span className="font-medium">{title}</span>
      {description ? <span className="ml-1 opacity-90">{description}</span> : null}
    </div>
  );
}

/** 写操作成功后的确认位。同样要 `role="status"`，理由与上面对称。 */
export function WriteOk({ children }: { children: ReactNode }) {
  return (
    <div
      role="status"
      className="rounded-lg border border-ok/30 bg-ok/10 px-3 py-2 text-sm leading-relaxed text-ok"
    >
      {children}
    </div>
  );
}

/**
 * 「该功能尚未开放」的**卡片**形态（`ticket-common.tsx` 的 `NotImplementedNotice`
 * 是它的整块形态，那个带 supportEmail 与请求号，用在读请求失败时）。
 *
 * 这一个用在**写操作**旁边：501 不是故障，红色 `WriteError` 在这里是误报 ——
 * 用户没做错任何事，重试也不会有变化。
 */
export function PendingFeatureNotice({
  what,
  requestId,
  children,
}: {
  what: string;
  requestId?: string | undefined;
  children?: ReactNode;
}) {
  return (
    <Card className="border-dashed" as="div">
      <h3 className="text-base font-semibold text-fg">该功能尚未开放</h3>
      <p className="mt-1.5 text-sm leading-relaxed text-fg-muted">
        {what}的后端还没上线（当前返回 501）。不是你的操作有问题，重试也不会有变化。
      </p>
      {children ? <div className="mt-2 text-sm leading-relaxed text-fg-muted">{children}</div> : null}
      {requestId ? <p className="mt-3 font-mono text-xs text-fg-subtle">请求号 {requestId}</p> : null}
    </Card>
  );
}

/**
 * 写操作错误 → 文案的**共同前半段**。
 *
 * 只处理「在这四页里含义完全相同」的那几个 code；返回 `null` 表示
 * 「这一页得自己说」，调用方接着走自己的分支。**刻意不做成一张大全表** ——
 * Dashboard 的注释里写了理由：合成一张表的那一刻，各页的文案就会开始互相迁就。
 *
 * 🔴 **按 `ErrorCode` 分支，不按 HTTP 状态码。** 这里最典型的反例是
 * `changePassword`：原密码错误时后端回的是 **401 + `AUTH_INVALID_CREDENTIALS`**
 * （`handler/auth.go`），按状态码分支会把它显示成「登录已过期，请重新登录」，
 * 而用户的登录状态好得很。
 */
export function commonWriteErrorCopy(
  error: ApiError,
  options: { retrySeconds?: number | null } = {},
): { title: string; description: string } | null {
  const retrySeconds = options.retrySeconds ?? null;
  switch (error.code) {
    case 'QUOTA_RATE_LIMITED':
      // ⚠️ 只有**带 `Retry-After`** 的 `QUOTA_RATE_LIMITED` 才是限流。
      // 同一个 code 在 `createInviteCode` 的 403 上表示「名额用完」，那是另一回事，
      // 由 InvitePage 自己接住。判据取 `Retry-After` 而不是状态码，
      // 是因为 api-contract §2.7 写明「429 与 503 **必带** Retry-After」——
      // 没有这个头就不是 429。
      if (error.retryAfterSeconds === undefined && retrySeconds === null) return null;
      return {
        title: '操作太频繁',
        description:
          retrySeconds === null
            ? '短时间内提交了太多次，稍后再试。'
            : `短时间内提交了太多次，${retrySeconds} 秒后可以再试。`,
      };
    case 'INTERNAL_DEPENDENCY_DOWN':
      // 503。**必须与 500 分开说**：500 是偶发故障（值得重试），
      // 503 是「这个功能现在整个不可用」（重试多少次都一样）。
      return {
        title: '暂时不可用，请稍后再试',
        description:
          retrySeconds === null
            ? '这个功能依赖的服务当前不可用。不是你的账号或网络的问题，稍后再来一次。'
            : `这个功能依赖的服务当前不可用，建议 ${retrySeconds} 秒后再试。不是你的账号或网络的问题。`,
      };
    case 'AUTH_PERMISSION_DENIED':
      return {
        title: '这个账号已被封禁',
        description: '会话是有效的，但账号不可用。重新登录不会有帮助，请通过邮件联系我们。',
      };
    case 'VALIDATION_FAILED':
    case 'VALIDATION_MALFORMED_BODY':
      return { title: '填写有误', description: fieldReasons(error) ?? error.message };
    default:
      return null;
  }
}

/** `VALIDATION_FAILED` 的字段级明细。契约保证有 `details` 时逐字段列出。 */
export function fieldReasons(error: ApiError): string | null {
  const details = error.details;
  if (!details || details.length === 0) return null;
  return details.map((d) => `${d.field}：${d.reason}`).join('；');
}

/** 兜底：没有可依的 code 时按五类归一说话。**永远不要把 `error.message` 当分支依据。** */
export function fallbackWriteErrorCopy(
  error: ApiError,
  fallbackTitle: string,
): { title: string; description: string } {
  switch (error.kind) {
    case 'offline':
      return {
        title: '连不上面板',
        description: '当前网络到面板的连接失败。可以试试页脚的备用域名，或稍后再来。',
      };
    case 'server':
      return { title: '我们这边出了问题', description: '不是你的账号或网络的问题，稍后再试一次。' };
    case 'unauthorized':
      return { title: '需要重新登录', description: '登录状态已过期，重新登录后回到这一页继续。' };
    default:
      return { title: fallbackTitle, description: error.message };
  }
}

/** 写操作的 501 判据。转发 `ticket-common` 的实现，页面不必再 import 两个文件。 */
export { isNotImplemented };

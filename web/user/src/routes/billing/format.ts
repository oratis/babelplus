/**
 * `/plan`、`/order`、`/order/:trade_no` 三页共用的**纯函数**：金额、汇率、周期、状态映射。
 *
 * 为什么放在这里而不是 `web/shared`：这三页是同一条支付路径上的三段，
 * 而 `shared` 是三个前端共用的地基。只有一条路径用得到的东西塞进地基，
 * 以后没人敢动它 —— 而这条路径上的口径（尤其是 ADR 0012 推翻的那几条）还会再变。
 *
 * 🔴 这里一分钱都不过浮点。契约（`openapi.yaml` 的 info.description）约定
 * 金额是「分」的 int64、USDT 是 1e-6 的 int64、汇率是基数 1e4 的定点整数。
 * 把它们除成 float 再格式化，误差会以「用户照显示金额转账 → underpaid」的形式出现，
 * 而 underpaid 是这条路径上最贵的一种 bug（要么人工补单，要么用户开工单）。
 */
import type { components } from '@babelplus/shared/api';

export type Period = components['schemas']['PlanPrice']['period'];
export type PlanPrice = components['schemas']['PlanPrice'];
export type Plan = components['schemas']['Plan'];
export type Order = components['schemas']['Order'];
export type PaymentCheckout = components['schemas']['PaymentCheckout'];
export type PaymentState = components['schemas']['PaymentState'];
export type CouponVerifyResult = components['schemas']['CouponVerifyResult'];
export type CreateOrderRequest = components['schemas']['CreateOrderRequest'];

/** 展示顺序 = 周期从短到长。用户从便宜的往贵的看，不是从枚举定义顺序看。 */
export const PERIOD_ORDER: readonly Period[] = [
  'monthly',
  'quarterly',
  'half_yearly',
  'yearly',
  'two_yearly',
  'three_yearly',
  'onetime',
];

export const PERIOD_LABEL: Readonly<Record<Period, string>> = {
  monthly: '月付',
  quarterly: '季付',
  half_yearly: '半年付',
  yearly: '年付',
  two_yearly: '两年付',
  three_yearly: '三年付',
  onetime: '一次性',
};

/**
 * 各周期折合多少个月。`onetime` 是流量包，**没有「折合月数」这回事** —— 所以是 `null`
 * 而不是 1。写成 1 的话流量包会算出一个毫无意义的「折扣」角标。
 */
const PERIOD_MONTHS: Readonly<Record<Period, number | null>> = {
  monthly: 1,
  quarterly: 3,
  half_yearly: 6,
  yearly: 12,
  two_yearly: 24,
  three_yearly: 36,
  onetime: null,
};

export function periodLabel(period: string | undefined): string {
  if (!period) return '—';
  return PERIOD_LABEL[period as Period] ?? period;
}

/**
 * 周期折扣角标：**拿 API 给的价格现算，不写死**。
 *
 * page-inventory §3.2.4 写着「季 9 折 / 半年 85 折 / 年 75 折」，但那是**产品意图不是事实源**：
 * pricing-and-plans §7 的定价仍是 P0 阻塞项。把 75 写进前端，运营改价的那天它就是错的，
 * 而且是「页面上写着 75 折、结算按新价扣」这种最难解释的错。
 *
 * 返回 `null` = 算不出来（没有月付基准 / 是流量包 / 并不比月付便宜），此时**不显示角标**。
 * 除法在这里除的是**比率不是钱**，所以允许走浮点；结果四舍五入成整数「折」。
 */
export function discountPercent(prices: readonly PlanPrice[], period: Period): number | null {
  const months = PERIOD_MONTHS[period];
  if (months === null || months <= 1) return null;
  const monthly = prices.find((p) => p.period === 'monthly')?.amount;
  const target = prices.find((p) => p.period === period)?.amount;
  if (monthly === undefined || target === undefined || monthly <= 0) return null;
  const baseline = monthly * months;
  if (target >= baseline) return null;
  return Math.round((target * 100) / baseline);
}

/** 这个套餐在所有周期里最狠的一档折扣，用于套餐卡上的角标。 */
export function bestDiscountPercent(prices: readonly PlanPrice[]): number | null {
  let best: number | null = null;
  for (const price of prices) {
    const pct = discountPercent(prices, price.period);
    if (pct !== null && (best === null || pct < best)) best = pct;
  }
  return best;
}

/** 入门价（最便宜的那一档），套餐卡上显示「¥X 起」。 */
export function cheapestPrice(prices: readonly PlanPrice[]): PlanPrice | null {
  let best: PlanPrice | null = null;
  for (const price of prices) {
    if (best === null || price.amount < best.amount) best = price;
  }
  return best;
}

export function priceOf(prices: readonly PlanPrice[], period: Period | null): PlanPrice | null {
  if (period === null) return null;
  return prices.find((p) => p.period === period) ?? null;
}

/**
 * 1e-6 USDT 的整数 → 展示串（**不带单位**，因为它同时用于「复制」按钮的内容）。
 *
 * **一次都不四舍五入**：去掉多余的 0，但至少保留两位小数。
 * 为什么不用 `@babelplus/shared` 的 `formatUsdt`：它默认输出 6 位小数，
 * 而 ADR 0012 §1 把报价取整到 **0.01 USDT**，`55.600000` 这种写法会让用户以为尾数有意义 ——
 * 「尾数有意义」正是这份裁决推翻掉的旧方案（「小地址池 + 金额尾数唯一性匹配」）。
 *
 * 反过来，**链上实收可能不是整分**（`received_usdt6`），这时保留全部 6 位而不是抹成两位：
 * 「已经收到多少」是一个事实，四舍五入过的事实会让用户拿着页面数字和链上浏览器对不上。
 */
export function formatUsdtAmount(amountUsdt6: number | null | undefined): string {
  if (amountUsdt6 === null || amountUsdt6 === undefined || !Number.isFinite(amountUsdt6)) return '—';
  const negative = amountUsdt6 < 0;
  const abs = Math.abs(Math.trunc(amountUsdt6));
  const whole = Math.floor(abs / 1_000_000);
  let frac = String(abs % 1_000_000).padStart(6, '0');
  while (frac.length > 2 && frac.endsWith('0')) frac = frac.slice(0, -1);
  // 千分位分隔符**不能加**：这个串是给用户复制粘贴进交易所提币框的。
  return `${negative ? '-' : ''}${whole}.${frac}`;
}

/**
 * 锁定汇率（定点整数，基数 1e4）→ 展示串。同样走整数除模，不碰浮点。
 * ⚠️ `cny_per_usdt_e4: 71930` 在 api-contract 里只是**文档示例**，不是基准汇率（ADR 0012 §15.2）。
 */
export function formatRateE4(rateE4: number | null | undefined): string {
  if (rateE4 === null || rateE4 === undefined || !Number.isFinite(rateE4)) return '—';
  const negative = rateE4 < 0;
  const abs = Math.abs(Math.trunc(rateE4));
  let frac = String(abs % 10_000).padStart(4, '0');
  while (frac.length > 2 && frac.endsWith('0')) frac = frac.slice(0, -1);
  return `${negative ? '-' : ''}${Math.floor(abs / 10_000)}.${frac}`;
}

/* ───────────────────────────── 状态映射 ───────────────────────────── */

export type Tone = 'neutral' | 'ok' | 'warn' | 'danger' | 'info';

export interface StatusMeta {
  readonly label: string;
  readonly tone: Tone;
}

/**
 * 订单状态 → 展示。🔴 **键的类型是 `string` 而不是生成的 `OrderStatus`，这是有意的。**
 *
 * 契约的 `OrderStatus` 只有 6 个值（`pending` `processing` `completed` `cancelled` `expired` `refunded`），
 * 而 `api/db/migrations/0001_enum_types.up.sql` 的 `order_status` 冻结为 **14 个值**
 * （ADR 0012 §7.1 逐个数过），其中 `paying` / `underpaid` / `paid` 恰好是收银台路径上最常见的三个。
 *
 * 用生成类型写 exhaustive 的 switch，这三个会掉进 default 显示成「未知状态」——
 * 而 `underpaid` 正是 page-inventory §3.2.5 点名**必须单独可见**的那一个。
 * 契约与 DDL 谁对齐谁是另一回事（已在 notes 里登记），但前端在两者对齐之前**不能瞎**。
 */
const ORDER_STATUS_META: Readonly<Record<string, StatusMeta>> = {
  pending: { label: '待支付', tone: 'warn' },
  paying: { label: '等待到账', tone: 'warn' },
  underpaid: { label: '金额不足', tone: 'danger' },
  paid: { label: '已支付', tone: 'ok' },
  processing: { label: '处理中', tone: 'info' },
  completed: { label: '已完成', tone: 'ok' },
  cancelled: { label: '已取消', tone: 'neutral' },
  expired: { label: '已过期', tone: 'neutral' },
  failed: { label: '失败', tone: 'danger' },
  refunding: { label: '退款中', tone: 'info' },
  refunded: { label: '已退款', tone: 'neutral' },
  partially_refunded: { label: '部分退款', tone: 'neutral' },
  chargeback: { label: '争议中', tone: 'danger' },
  chargeback_won: { label: '争议已胜', tone: 'ok' },
  chargeback_lost: { label: '争议已败', tone: 'danger' },
};

/**
 * 认不出来的状态**原样显示这个码**，不显示「未知」。
 * 用户报障时贴一个原始码，我们能直接 grep；贴「未知」则什么线索都没有。
 */
export function orderStatusMeta(status: string): StatusMeta {
  return ORDER_STATUS_META[status] ?? { label: status, tone: 'neutral' };
}

const ORDER_TYPE_LABEL: Readonly<Record<string, string>> = {
  new: '新购',
  renew: '续费',
  upgrade: '升级',
  traffic_pack: '流量包',
};

export function orderTypeLabel(type: string): string {
  return ORDER_TYPE_LABEL[type] ?? type;
}

/** 只有 `pending` 可取消（契约 `cancelOrder`：「仅 `pending` 可取消」）。 */
export function isCancellable(order: Order): boolean {
  return order.status === 'pending';
}

/**
 * 还需要继续轮询的收银台状态。
 * `paid` / `expired` 是终态 —— 对终态继续轮询是白烧用户的移动流量和电。
 */
const POLLABLE_PAYMENT_STATES: ReadonlySet<string> = new Set<string>(['waiting', 'confirming', 'underpaid']);

/**
 * 要不要发下一次轮询。抽成纯函数是为了**可测** ——
 * 「页面隐藏时暂停」这条规则藏在 `useEffect` 里就只能靠人眼守，
 * 而它坏掉的表现（用户切到后台，页面在背景里每分钟打一次 API）没有人会报 bug。
 */
export function shouldPoll(state: string | undefined, documentHidden: boolean): boolean {
  if (documentHidden) return false;
  if (state === undefined) return false;
  return POLLABLE_PAYMENT_STATES.has(state);
}

/** 轮询起步间隔。链上确认是**分钟级**的，1 秒轮询在移动网络下毫无收益。 */
export const POLL_BASE_MS = 5_000;
/** 退避上限。ADR 0012 §10.1 的活跃档扫链本身就是 60 秒一次，比它更密没有意义。 */
export const POLL_MAX_MS = 60_000;

/** 指数退避的下一档。抽成纯函数同上：可测，且退避规则只有这一处实现。 */
export function nextPollDelayMs(current: number): number {
  return Math.min(current * 2, POLL_MAX_MS);
}

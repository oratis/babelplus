/**
 * `/order/:trade_no` —— P1，**无替代（付款中断 = 丢单）**。page-inventory §3.1 #9、§3.2.5。
 *
 * 🔴 **收银台必须独立成页，不能做成弹窗**（竞品是弹窗）。
 * 理由不是审美：链上确认有分钟级延迟，用户**必须能关掉页面再回来**，
 * 而弹窗的状态活不过一次刷新。这条决定了它是一条路由而不是一个组件。
 * 落到实现上就是两件事：倒计时只从服务端给的 `quote_expires_at` 算（不从「进页面时是 30 分钟」
 * 倒着数），以及轮询在页面隐藏时暂停、回来时继续。
 *
 * 🔴 **支付形态以 ADR 0012 为准：一单一址、永不复用、归属只看地址不看金额。**
 * page-inventory §3.2.5 那一格写的「小地址池 + 金额唯一性 / 精确到分位的唯一金额」
 * 与 §7 卡点 5 的旧文案**已被 ADR 0012 §5.4 整节删除**，理由是它在最常发生的那条路径上
 * 恰好失效：交易所提币费从转出额里扣，实收必然落在所有槽位之外。
 * 所以这一页**一个字都不许**出现「尾数 / 识别码 / 精确到小数点后四位」——
 * 报价已经取整到 0.01 USDT，那套话术现在是**错的**，不只是过时。
 * 收银台里那段解释文字直接渲染 API 的 `PaymentCheckout.note`（后端 `paymentCheckoutNote`
 * 已是新文案），前端不自己写一份 —— 两处文案迟早漂移，而漂移的那一份会教用户填错金额。
 * `OrderDetailPage.test.tsx` 对这一条做了负向断言。
 *
 * 两个读请求，**各自一套三态**（§2.2）：
 *  ① `getOrder`        —— 金额构成。404 走「找不到这个订单」的空态，不是错误态。
 *  ② `getOrderPayment` —— 收银台。轮询用 `patch` 就地更新，**不把已经显示的收银台打回骨架**：
 *     用户正盯着地址准备转账，页面突然变成骨架屏会让人怀疑地址变了。
 * 两个写请求各自持有 pending/error：`payOrder`（幂等键 + 二次确认）与 `cancelOrder`（二次确认）。
 *
 * 🔴 「我已付款，帮我查一下」**永远可见**（ADR 0012 §10.4 原话：「按钮必须在页面上永远可见，
 * 不能只在检测到异常时才出现 —— 因为『检测到异常』这件事本身就是我们做不到才需要这个按钮」）。
 * 所以它挂在两个读请求的三态**之外**：收银台读不出来的时候，它恰恰是用户仅剩的动作。
 */
import { useCallback, useEffect, useRef, useState } from 'react';
import { Link, useParams } from 'react-router';
import {
  Badge,
  Button,
  Card,
  CardTitle,
  EmptyState,
  Icon,
  LinkButton,
  formatCny,
  formatDateTime,
} from './_imports.ts';
import { unwrap, type ApiError } from '@babelplus/shared/api';
import { api } from '../lib/api.ts';
import { FormAlert, asApiError, useApiQuery, type ApiQuery } from './ticket-common.tsx';
import {
  POLL_BASE_MS,
  formatRateE4,
  formatUsdtAmount,
  isCancellable,
  nextPollDelayMs,
  orderStatusMeta,
  orderTypeLabel,
  periodLabel,
  shouldPoll,
  type Order,
  type PaymentCheckout,
} from './billing/format.ts';
import {
  AmountRow,
  BillingErrorState,
  BlockSkeleton,
  ConfirmPanel,
  CopyRow,
  NoticeBlock,
  billingErrorCopy,
  formatDuration,
  useIdempotencyKey,
  useSecondsUntil,
} from './billing/checkout.tsx';

export default function OrderDetailPage() {
  const { trade_no: tradeNoParam } = useParams();
  const tradeNo = tradeNoParam ?? '';

  const order = useApiQuery(() => getOrder(tradeNo), [tradeNo], '订单加载失败');
  const payment = useApiQuery(() => getPayment(tradeNo), [tradeNo], '收银台状态加载失败');

  // 订单一旦真的付掉，金额卡上的状态标签就该跟上。**只在这一次跃迁时重拉**，
  // 不做定时重拉：金额构成在订单创建之后不会再变，反复拉只是白烧跨境往返。
  const paid = payment.data?.state === 'paid';
  const reloadOrder = order.reload;
  useEffect(() => {
    if (paid) reloadOrder();
  }, [paid, reloadOrder]);

  const orderNotFound = order.error?.code === 'RESOURCE_NOT_FOUND';

  return (
    <>
      <header className="mb-5 sm:mb-6">
        <h1 className="text-xl font-semibold tracking-tight text-fg sm:text-2xl">订单详情</h1>
        <p className="mt-1 flex flex-wrap items-baseline gap-x-2 gap-y-1 text-sm text-fg-muted">
          订单号 <code className="font-mono text-fg">{tradeNo || '—'}</code>
          {order.data ? <Badge tone={orderStatusMeta(order.data.status).tone}>{orderStatusMeta(order.data.status).label}</Badge> : null}
        </p>
      </header>

      {orderNotFound ? (
        <EmptyState
          title="找不到这个订单"
          description="订单号可能输错了，或者这个订单不属于当前账号。"
          action={
            <LinkButton tone="primary" href="/order">
              回到订单列表 <Icon.ArrowRight size={14} />
            </LinkButton>
          }
        />
      ) : (
        <div className="space-y-4">
          <AmountSection query={order} />
          <CheckoutSection tradeNo={tradeNo} order={order.data} payment={payment} />
          <RecheckSection tradeNo={tradeNo} payment={payment} />
          {order.data ? (
            <CancelOrderBlock
              order={order.data}
              // 取消成功后**就地改**这一份订单，不 reload —— reload 会把金额卡打回骨架屏，
              // 而用户刚点完取消，最需要的是立刻看到「已取消」。
              onCancelled={(next) => order.patch(() => next)}
            />
          ) : null}
        </div>
      )}
    </>
  );
}

/* ─────────────────────── 金额构成（读请求 ①） ─────────────────────── */

/**
 * 🔴 金额单位是「分」的 int64（契约 `Order` 的注释：**所有 `*_amount` 单位都是分**），
 * 一律走 `formatCny` 的整数除模，**前端任何地方都不得用浮点算钱**。
 *
 * 折抵 / 优惠 / 余额三行**只在服务端真的给了值的时候才出现**：
 * 它们是可选字段，缺失表示「这一单没有这一项」而不是「0 元」，
 * 而一行「优惠 −¥0.00」会让用户以为自己的优惠码被吃掉了。
 */
function AmountSection({ query }: { query: ApiQuery<Order> }) {
  if (query.state === 'loading') return <BlockSkeleton rows={4} />;
  if (query.state === 'error' && query.error) {
    return <BillingErrorState error={query.error} what="订单信息" onRetry={query.reload} />;
  }
  const order = query.data;
  if (!order) return null;

  return (
    <Card>
      <CardTitle hint={`${orderTypeLabel(order.type)}${order.period ? ` · ${periodLabel(order.period)}` : ''}`}>
        金额构成
      </CardTitle>

      <dl className="divide-y divide-line">
        <AmountRow label={order.plan_name ?? '套餐'} value={formatCny(order.total_amount)} />
        {order.discount_amount ? (
          <AmountRow label="优惠码抵扣" value={`−${formatCny(order.discount_amount)}`} />
        ) : null}
        {order.surplus_amount ? (
          // ⚠️ 折抵**算法**在 api-contract §14 还没裁决（按剩余天数还是剩余流量未定），
          //    但呈现口径是定的（user-journey：原套餐剩余价值 / 新套餐价 / 实付三行）。
          //    这里只如实显示服务端算出来的那个数，不在前端复算。
          <AmountRow label="原套餐剩余价值折抵" value={`−${formatCny(order.surplus_amount)}`} />
        ) : null}
        {order.balance_amount ? (
          <AmountRow label="账户余额抵扣" value={`−${formatCny(order.balance_amount)}`} />
        ) : null}
        <AmountRow label="实付" value={formatCny(order.payable_amount)} strong />
      </dl>

      <div className="mt-3 space-y-1 text-xs text-fg-subtle">
        <p>下单时间 {formatDateTime(order.created_at)}</p>
        {order.rate_locked_at ? <p>汇率锁定于 {formatDateTime(order.rate_locked_at)}</p> : null}
        {order.paid_at ? <p>付款到账 {formatDateTime(order.paid_at)}</p> : null}
      </div>
    </Card>
  );
}

/* ─────────────────────── 收银台（读请求 ② + 写请求 payOrder） ─────────────────────── */

function CheckoutSection({
  tradeNo,
  order,
  payment,
}: {
  tradeNo: string;
  order: Order | null;
  payment: ApiQuery<PaymentCheckout>;
}) {
  const pollError = useCheckoutPolling(tradeNo, payment);

  if (payment.state === 'loading') return <BlockSkeleton rows={5} />;
  if (payment.state === 'error' && payment.error) {
    return <BillingErrorState error={payment.error} what="收银台" onRetry={payment.reload} />;
  }

  const checkout = payment.data;
  if (!checkout) return null;

  // 还没分配收款地址 = 还没发起支付。`payOrder` 才会分配地址（一单一址，ADR 0012 §5.1），
  // 所以「有没有 address」就是「这一单进没进收银台」的判据。
  const started = Boolean(checkout.address);

  return (
    <Card>
      <CardTitle hint={started ? 'getOrderPayment（轮询）' : 'payOrder'}>付款</CardTitle>

      {started ? (
        <PaidWithAddress checkout={checkout} pollError={pollError} />
      ) : (
        <StartPayment tradeNo={tradeNo} order={order} checkout={checkout} payment={payment} />
      )}
    </Card>
  );
}

/**
 * 还没发起支付：选支付方式 → 二次确认 → `payOrder`。
 *
 * 两种方式的形态完全不同，所以分开说明而不是并成一句「选择支付方式」：
 * 余额是**立刻扣、立刻开通**（`payWithBalance` 在一个事务里走完分录 + 扣缓存 + 标记已付），
 * USDT 是**开一个收款地址等你转账**。混为一谈的话，点了「余额」的人不知道钱已经扣了。
 */
function StartPayment({
  tradeNo,
  order,
  checkout,
  payment,
}: {
  tradeNo: string;
  order: Order | null;
  checkout: PaymentCheckout;
  payment: ApiQuery<PaymentCheckout>;
}) {
  const [method, setMethod] = useState<'usdt_trc20' | 'balance'>('usdt_trc20');
  const [confirming, setConfirming] = useState(false);
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<ApiError | null>(null);

  // 幂等键跟着载荷走：`payOrder` 的载荷只有 `method`，所以换一种支付方式就换一把键
  // （同键不同载荷 → 409 `STATE_IDEMPOTENCY_MISMATCH`）。
  const idempotencyKey = useIdempotencyKey(`${tradeNo}|${method}`);

  // 终态订单不该再有「去付款」。`waiting` 且没地址才是「还没开始」。
  if (checkout.state === 'expired') {
    return <ExpiredNotice />;
  }
  if (checkout.state === 'paid') {
    return <PaidNotice />;
  }

  async function onPay(): Promise<void> {
    if (pending) return;
    setPending(true);
    setError(null);
    try {
      const next = await payOrder(tradeNo, method, idempotencyKey);
      // 就地更新，不 reload —— reload 会把收银台打回骨架，而此刻页面上刚出现的是收款地址。
      payment.patch(() => next);
      setConfirming(false);
    } catch (cause) {
      setError(asApiError(cause, '发起支付失败'));
    } finally {
      setPending(false);
    }
  }

  const copy = error === null ? null : billingErrorCopy(error, { fallbackTitle: '发起支付没能完成' });
  const payable = order?.payable_amount;

  return (
    <div className="space-y-3">
      <p className="text-sm text-fg-muted">
        这张订单还没发起支付{payable === undefined ? '' : `，应付 ${formatCny(payable)}`}。
      </p>

      <fieldset className="space-y-2">
        <legend className="mb-1 text-sm font-medium text-fg">怎么付</legend>
        <MethodChoice
          value="usdt_trc20"
          current={method}
          onSelect={setMethod}
          title="USDT（TRC20）"
          description="我们会给这张订单开一个专属收款地址。地址只服务这一张订单、永不复用，转过来的钱一定认得出是你的。"
        />
        <MethodChoice
          value="balance"
          current={method}
          onSelect={setMethod}
          title="账户余额"
          description="立刻从余额里扣掉并开通，不需要再转账。余额不够会被拒绝，不会扣一半。"
        />
      </fieldset>

      <Button tone="primary" disabled={pending || confirming} onClick={() => setConfirming(true)}>
        <Icon.Coin size={14} /> 去付款
      </Button>

      <ConfirmPanel
        open={confirming}
        title={method === 'balance' ? '用余额付掉这张订单' : '开一个 USDT 收款地址'}
        consequences={
          method === 'balance'
            ? [
                <>
                  余额会
                  <strong className="font-medium text-fg">立刻扣掉</strong>
                  并开通订阅，这一步不可撤销。
                </>,
                '余额不足会被直接拒绝，不会扣掉一部分。',
              ]
            : [
                <>
                  会给这张订单分配一个
                  <strong className="font-medium text-fg">专属</strong>
                  的 TRC20 收款地址，分配之后这张单就不能取消了。
                </>,
                <>
                  这个地址<strong className="font-medium text-fg">永远认账</strong>：
                  无论多久之后到账、无论金额多少，都会自动记到这张订单或你的账户余额上。
                </>,
                <>
                  重复点击不会开出第二个地址：这次提交带幂等键{' '}
                  <code className="font-mono text-xs">{idempotencyKey.slice(0, 8)}…</code>。
                </>,
              ]
        }
        confirmLabel={method === 'balance' ? '确认用余额支付' : '确认，给我一个收款地址'}
        pending={pending}
        error={
          copy === null ? null : (
            <>
              <span className="font-medium">{copy.title}</span>
              <br />
              {copy.description}
              {error?.requestId ? (
                <>
                  <br />
                  <span className="font-mono text-xs">请求号 {error.requestId}</span>
                </>
              ) : null}
            </>
          )
        }
        onCancel={() => setConfirming(false)}
        onConfirm={() => void onPay()}
      />
    </div>
  );
}

function MethodChoice({
  value,
  current,
  onSelect,
  title,
  description,
}: {
  value: 'usdt_trc20' | 'balance';
  current: string;
  onSelect: (value: 'usdt_trc20' | 'balance') => void;
  title: string;
  description: string;
}) {
  return (
    <label className="flex items-start gap-2 rounded-lg border border-line p-3 text-sm">
      <input
        type="radio"
        name="pay-method"
        className="mt-1"
        value={value}
        checked={current === value}
        onChange={() => onSelect(value)}
      />
      <span>
        <span className="font-medium text-fg">{title}</span>
        <br />
        <span className="text-xs leading-relaxed text-fg-muted">{description}</span>
      </span>
    </label>
  );
}

/* ─────────────────────── 已经有收款地址的收银台 ─────────────────────── */

function PaidWithAddress({
  checkout,
  pollError,
}: {
  checkout: PaymentCheckout;
  pollError: ApiError | null;
}) {
  const secondsLeft = useSecondsUntil(checkout.quote_expires_at);
  const amount = checkout.amount_display ?? formatUsdtAmount(checkout.amount_usdt6);

  return (
    <div className="space-y-4">
      {/* 链。**只有 TRC20** —— 契约的 `PaymentCheckout.chain` 枚举只有这一个值，
          后端也只分配 TRON 地址（ADR 0012 §1：主通道是 USDT-TRC20）。
          roadmap §5.2 写的「链选择（TRC20 / ERC20 / BEP20）」在后端还不存在，
          所以这里**不摆一排假的链标签** —— 摆出来的后果是有人按 ERC20 转账，
          而那笔钱打到的是另一条链上的另一个地址，我们收不到也退不了。 */}
      <div className="flex flex-wrap items-center gap-2">
        <Badge tone="info">{checkout.chain ?? 'TRC20'}</Badge>
        <PaymentStateBadge state={checkout.state} />
        {secondsLeft === null ? null : secondsLeft > 0 ? (
          <Badge tone="warn">汇率锁定还剩 {formatDuration(secondsLeft)}</Badge>
        ) : (
          <Badge tone="neutral">汇率锁定已过期</Badge>
        )}
      </div>

      <CopyRow
        label="收款地址"
        value={checkout.address ?? ''}
        hint="这是一个 TRON（TRC20）地址，只服务这一张订单。用别的链转过来的钱不会到这里。"
      />

      <CopyRow
        label="需要到账的金额"
        value={amount}
        hint={
          checkout.cny_per_usdt_e4 === undefined ? undefined : (
            <>
              按锁定汇率 1 USDT = ¥{formatRateE4(checkout.cny_per_usdt_e4)} 折算。
              {checkout.confirmations_required === undefined
                ? null
                : ` 链上确认约需 ${checkout.confirmations_required} 个区块。`}
            </>
          )
        }
      />

      {/* 🔴 这段解释直接来自 API 的 `note`，前端不自己写一份。
          它现在解释的是**提币手续费**（手续费从你填的金额里扣，不是另外加收），
          而不是旧方案里那个「四位小数尾数」—— 那套话术已随 ADR 0012 §5.4 一起删除。
          在这里硬编码一份文案，就是给未来的漂移开一个口子。 */}
      {checkout.note ? <NoticeBlock tone="info">{checkout.note}</NoticeBlock> : null}

      <StateDetail checkout={checkout} secondsLeft={secondsLeft} />

      {pollError ? (
        // 轮询失败**不清掉已经显示的地址与金额** —— 用户正要照着它转账。
        <FormAlert>
          <span className="font-medium">
            {billingErrorCopy(pollError, { fallbackTitle: '状态刷新失败' }).title}
          </span>
          <br />
          上面显示的是最后一次成功读到的状态，收款地址与金额没有变。下面的「我已付款，帮我查一下」可以手动再查一次。
        </FormAlert>
      ) : null}
    </div>
  );
}

function PaymentStateBadge({ state }: { state: PaymentCheckout['state'] }) {
  switch (state) {
    case 'waiting':
      return <Badge tone="warn">等待转账</Badge>;
    case 'confirming':
      return <Badge tone="info">已到账，等链上确认</Badge>;
    case 'underpaid':
      return <Badge tone="danger">金额还差一点</Badge>;
    case 'paid':
      return <Badge tone="ok">已付款</Badge>;
    case 'expired':
      return <Badge tone="neutral">已过期</Badge>;
    default:
      return <Badge>{state}</Badge>;
  }
}

/** 五个 `PaymentState` 各自的正文。**每一个都有话说** —— 没有「其他」这一档。 */
function StateDetail({
  checkout,
  secondsLeft,
}: {
  checkout: PaymentCheckout;
  secondsLeft: number | null;
}) {
  switch (checkout.state) {
    case 'waiting':
      return (
        <div className="space-y-2">
          <p className="text-sm text-fg-muted">
            还没看到这个地址上的转账。
            {secondsLeft !== null && secondsLeft > 0
              ? `本单在 ${formatDuration(secondsLeft)} 后作废。`
              : null}
          </p>
          <CloseablePageNote />
          <ExpiryPromise />
        </div>
      );

    case 'confirming':
      return (
        <div className="space-y-2">
          <NoticeBlock tone="info">
            <span className="font-medium text-fg">钱已经到了，正在等链上确认。</span>
            {checkout.received_usdt6 === undefined
              ? null
              : ` 已收到 ${formatUsdtAmount(checkout.received_usdt6)} USDT。`}
            {' '}TRON 上通常是分钟级，不需要你再做任何事。
          </NoticeBlock>
          <CloseablePageNote />
        </div>
      );

    case 'underpaid':
      // 🔴 §3.2.5 点名：**不能显示成「支付失败」**。钱收到了，只是还差一点。
      //    少付的头号成因是交易所提币手续费从转出额里扣（ADR 0012 §6）——
      //    这不是用户填错了，是这条路径的固有形态。
      return (
        <div className="space-y-2">
          <div className="rounded-lg border border-warn/30 bg-warn/10 p-3 text-sm text-warn">
            {/* 措辞上**连「失败」两个字都不出现**：否定式（「这不是支付失败」）仍然会先把
                「失败」塞进用户眼里，而这一刻他要知道的第一件事是钱没丢。 */}
            <p className="font-medium">钱已经收到了，只是金额还差一点。</p>
            <dl className="mt-2 space-y-0.5">
              <div className="flex justify-between gap-3">
                <dt>已收到</dt>
                <dd className="font-mono">{formatUsdtAmount(checkout.received_usdt6)} USDT</dd>
              </div>
              <div className="flex justify-between gap-3">
                <dt>还差</dt>
                <dd className="font-mono">{formatUsdtAmount(checkout.shortfall_usdt6)} USDT</dd>
              </div>
            </dl>
          </div>
          {/* ⚠️ ADR 0012 §6.1 把少付分三档处理（≤2 USDT 自动写销 / 2–5 人工 / >5 提示补足），
              但**两个阈值只在服务端的 `settings` 里，契约一个字段都没下发**（`PaymentCheckout`
              只有 `shortfall_usdt6`）。所以前端分不出「你什么都不用做」和「请补足」——
              在这里按 2.0 / 5.0 写死判断就是把一个可配置的运营参数复制进前端，
              运营改一次阈值它就开始骗人。下面这段话对三档都成立，已登记在 notes 里。 */}
          <p className="text-sm leading-relaxed text-fg-muted">
            差额很小的时候我们会直接给你开通，不用你再转一次；差额大到值得补的时候，
            请把差额<strong className="font-medium text-fg">打到上面同一个地址</strong>
            —— 从交易所提币记得把手续费加进提币金额里（手续费是从你填的数里扣的）。
            不确定属于哪种就点下面的「我已付款，帮我查一下」，或者提个工单，我们直接处理。
          </p>
        </div>
      );

    case 'paid':
      return <PaidNotice />;

    case 'expired':
      return <ExpiredNotice />;

    default:
      return null;
  }
}

function PaidNotice() {
  return (
    <NoticeBlock tone="info">
      <span className="font-medium text-fg">这张订单已经付清。</span> 订阅会在开通后出现在
      <Link to="/subscribe" className="mx-1 text-accent hover:underline">
        订阅页
      </Link>
      上。如果过了几分钟还没有，提个工单，别重复付款。
    </NoticeBlock>
  );
}

/**
 * 过期。ADR 0012 §11.2 要求这段话**写进收银台**，而且要在倒计时归零**之前**就说
 * （user-journey §7 把「倒计时归零后才转账」判定为最不可挽回的一类失败，
 * 而 30 分钟对一个第一次用交易所提币的人很紧 —— 把兜底提前告诉用户，
 * 比把兜底做好更能减少那一刻的恐惧）。所以它同时出现在 `waiting` 与 `expired` 两档里。
 */
function ExpiredNotice() {
  return (
    <NoticeBlock tone="warn">
      <span className="font-medium">这张订单的付款窗口已经过去，本单作废。</span>
      <br />
      但这个收款地址<strong className="font-medium">永远认账</strong>：过期后它仍然被继续监听（至少 7 天，
      更晚到账的由每日对账兜住），到账的钱会自动
      <strong className="font-medium">入你的账户余额</strong>、
      <strong className="font-medium">不会直接开通订阅</strong>，你可以用余额重新下一单。
      <br />
      <Link to="/plan" className="text-accent hover:underline">
        重新下单
      </Link>
    </NoticeBlock>
  );
}

function ExpiryPromise() {
  return (
    <p className="text-xs leading-relaxed text-fg-subtle">
      万一倒计时结束之后你的转账才到：本单会作废，但这个地址仍然认账 ——
      钱会自动入你的账户余额，可以用来重新下单 —— <strong className="font-medium text-fg">钱不会进黑洞</strong>。
    </p>
  );
}

/**
 * 「可以关掉这一页」。
 *
 * ⚠️ page-inventory §3.2.5 的原话是「可以关闭此页，**到账后会发邮件**」，
 * 这里**故意只说前半句**：邮件通道一行都没接通（roadmap §5.2 的 2.C：ESP 未选型、
 * `email_log.status` 恒为 `queued`）。承诺一封发不出去的邮件，比不承诺糟得多 ——
 * 用户会关掉页面去等一封永远不来的信。
 * TODO(P1)：ESP 接通后把后半句加回来（这是 page-inventory 的硬要求，不是可选文案）。
 */
function CloseablePageNote() {
  return (
    <p className="text-sm leading-relaxed text-fg-muted">
      <strong className="font-medium text-fg">可以关掉这一页。</strong>{' '}
      付款不依赖这个页面开着 —— 转完账随时回到订单页，这里会显示最新状态。
    </p>
  );
}

/* ─────────────────────── 主动查单（写请求，永远可见） ─────────────────────── */

/**
 * 「我已付款，帮我查一下」—— page-inventory 称它为「用户侧的最后防线」。
 *
 * 🔴 **回调不可信。** 回调可能丢、可能被伪造（pricing-and-plans §4.1 记录了 NewAPI 的
 * 易支付回调漏洞先例），所以这条主动查链的路必须一直存在。后端 `recheckOrderPayment`
 * 与链上扫描走**同一段** `processDeposit`，权威金额只来自链上。
 *
 * 冷却窗口内后端**不回 429 而是回上一次的结果 + 200**（ADR 0012 §10.4：
 * 「给一个害怕的人回 429，是这个按钮所有可能行为里最差的一种」）。
 * 所以这里不需要给它做客户端节流 —— 做了反而会把一个后端已经处理好的场景
 * 变成「按钮点不动」。仍然保留 429 的分支：那是后端换口径时的兜底，不是预期路径。
 */
function RecheckSection({ tradeNo, payment }: { tradeNo: string; payment: ApiQuery<PaymentCheckout> }) {
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<ApiError | null>(null);
  const [checkedAt, setCheckedAt] = useState<string | null>(null);

  async function onRecheck(): Promise<void> {
    if (pending) return;
    setPending(true);
    setError(null);
    try {
      const next = await recheckPayment(tradeNo);
      payment.patch(() => next);
      // 收银台整段读失败（payment.data 为 null）时 patch 是 no-op —— 那种情况下
      // 至少把这次查询的时刻显示出来，让用户知道按钮确实起作用了。
      setCheckedAt(new Date().toISOString());
    } catch (cause) {
      setError(asApiError(cause, '查单失败'));
    } finally {
      setPending(false);
    }
  }

  const copy = error === null ? null : billingErrorCopy(error, { fallbackTitle: '查单没能完成' });

  return (
    <Card>
      <CardTitle hint="recheckOrderPayment">已经付了，但页面还没变？</CardTitle>
      <p className="mb-3 text-sm leading-relaxed text-fg-muted">
        点这个按钮我们会直接去链上查这张订单的收款地址，
        <strong className="font-medium text-fg">不依赖任何支付回调</strong>。
        回调可能丢、也可能被伪造，所以这条路一直留着。
      </p>
      <Button tone="primary" disabled={pending} onClick={() => void onRecheck()}>
        {pending ? '正在查链上…' : '我已付款，帮我查一下'}
      </Button>
      {checkedAt ? (
        <p className="mt-2 text-xs text-fg-subtle">最近一次查询：{formatDateTime(checkedAt)}</p>
      ) : null}
      {copy === null ? null : (
        <div className="mt-3">
          <FormAlert>
            <span className="font-medium">{copy.title}</span>
            <br />
            {copy.description}
            {error?.requestId ? (
              <>
                <br />
                <span className="font-mono text-xs">请求号 {error.requestId}</span>
              </>
            ) : null}
          </FormAlert>
        </div>
      )}
    </Card>
  );
}

/* ─────────────────────── 取消（写请求） ─────────────────────── */

function CancelOrderBlock({ order, onCancelled }: { order: Order; onCancelled: (next: Order) => void }) {
  const [confirming, setConfirming] = useState(false);
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<ApiError | null>(null);

  if (!isCancellable(order)) return null;

  async function onCancel(): Promise<void> {
    if (pending) return;
    setPending(true);
    setError(null);
    try {
      onCancelled(await cancelOrder(order.trade_no));
      setConfirming(false);
    } catch (cause) {
      setError(asApiError(cause, '取消失败'));
    } finally {
      setPending(false);
    }
  }

  const copy = error === null ? null : billingErrorCopy(error, { fallbackTitle: '取消没能完成' });

  return (
    <Card>
      <CardTitle hint="cancelOrder">不想买了</CardTitle>
      <p className="mb-3 text-sm text-fg-muted">
        只有<strong className="font-medium text-fg">还没发起支付</strong>的订单可以取消 ——
        一旦分配了收款地址，这张单就得走完（那个地址已经归它了）。
      </p>
      <Button disabled={pending || confirming} onClick={() => setConfirming(true)}>
        取消这张订单
      </Button>
      <ConfirmPanel
        open={confirming}
        title={`取消订单 ${order.trade_no}`}
        tone="danger"
        consequences={[
          '取消后这张单作废，不能恢复；想买同样的东西要重新下一单。',
          <>
            如果你已经转出去了 USDT，
            <strong className="font-medium text-fg">不要取消</strong>
            —— 先点上面的「我已付款，帮我查一下」。
          </>,
        ]}
        confirmLabel="确认取消这张订单"
        pending={pending}
        error={
          copy === null ? null : (
            <>
              <span className="font-medium">{copy.title}</span>
              <br />
              {copy.description}
            </>
          )
        }
        onCancel={() => setConfirming(false)}
        onConfirm={() => void onCancel()}
      />
    </Card>
  );
}

/* ─────────────────────── 轮询 ─────────────────────── */

/**
 * 收银台轮询。三条规则各自有代价，所以都不是可选的：
 *
 *  1. **页面隐藏时暂停**（`document.hidden` + `visibilitychange`）。用户切到交易所 App 去转账，
 *     这一页在后台每隔几秒打一次 API 是白烧移动流量和电，而那正是他最需要电的时候。
 *  2. **指数退避**（`POLL_BASE_MS` → `POLL_MAX_MS`）。链上确认是**分钟级**的，固定 1 秒轮询
 *     一次收益都没有；状态一变就退回起步档，因为状态变了之后下一次变化通常很快。
 *  3. **只轮询非终态**（`shouldPoll`）。`paid` / `expired` 之后再轮询是纯粹的浪费。
 *
 * 判据本身是 `format.ts` 里的两个纯函数，**在那里可以直接单测** ——
 * 藏在这个 effect 里就只能靠人眼守，而它坏掉的表现（用户切到后台、页面在背景里一直打 API）
 * 没有任何人会报 bug。
 *
 * 更新走 `payment.patch` 而不是 `reload`：`reload` 会把收银台打回骨架屏，
 * 而此刻屏幕上是用户正要照着转账的收款地址。
 */
function useCheckoutPolling(tradeNo: string, payment: ApiQuery<PaymentCheckout>): ApiError | null {
  const state = payment.data?.state;
  const patch = payment.patch;
  // 还没分配收款地址 = 还没发起支付，链上不可能有任何变化。
  // 这一档不加的话，一个躺在「待支付」上的订单页会每分钟白打一次 API ——
  // `state` 此时正是 `waiting`，而 `waiting` 恰恰是需要轮询的那一档。
  const started = Boolean(payment.data?.address);

  const [hidden, setHidden] = useState<boolean>(() => document.hidden);
  const [round, setRound] = useState(0);
  const [pollError, setPollError] = useState<ApiError | null>(null);
  const delayRef = useRef(POLL_BASE_MS);

  useEffect(() => {
    const onVisibility = () => setHidden(document.hidden);
    document.addEventListener('visibilitychange', onVisibility);
    return () => document.removeEventListener('visibilitychange', onVisibility);
  }, []);

  // 状态一变就把退避退回起步档。
  useEffect(() => {
    delayRef.current = POLL_BASE_MS;
  }, [state]);

  const tick = useCallback(() => {
    void getPayment(tradeNo).then(
      (next) => {
        setPollError(null);
        patch(() => next);
        setRound((n) => n + 1);
      },
      (cause: unknown) => {
        // 轮询失败**不清空已有数据**，只记一个旁注。
        setPollError(asApiError(cause, '状态刷新失败'));
        setRound((n) => n + 1);
      },
    );
  }, [tradeNo, patch]);

  useEffect(() => {
    if (!started || !shouldPoll(state, hidden)) return;
    const delay = delayRef.current;
    const timer = window.setTimeout(() => {
      delayRef.current = nextPollDelayMs(delay);
      tick();
    }, delay);
    return () => window.clearTimeout(timer);
    // `round` 在依赖里：每完成一轮就重新排下一轮。
  }, [started, state, hidden, round, tick]);

  return pollError;
}

/* ─────────────────────── 端点 ─────────────────────── */

function getOrder(tradeNo: string): Promise<Order> {
  return unwrap(api().GET('/api/v1/orders/{trade_no}', { params: { path: { trade_no: tradeNo } } }));
}

function getPayment(tradeNo: string): Promise<PaymentCheckout> {
  return unwrap(
    api().GET('/api/v1/orders/{trade_no}/payment', { params: { path: { trade_no: tradeNo } } }),
  );
}

/** `Idempotency-Key` 是契约必填的请求头（`payOrder` 的 `parameters.header`）。 */
function payOrder(
  tradeNo: string,
  method: 'usdt_trc20' | 'balance',
  idempotencyKey: string,
): Promise<PaymentCheckout> {
  return unwrap(
    api().POST('/api/v1/orders/{trade_no}/pay', {
      params: { path: { trade_no: tradeNo }, header: { 'Idempotency-Key': idempotencyKey } },
      body: { method },
    }),
  );
}

function recheckPayment(tradeNo: string): Promise<PaymentCheckout> {
  return unwrap(
    api().POST('/api/v1/orders/{trade_no}/recheck', { params: { path: { trade_no: tradeNo } } }),
  );
}

function cancelOrder(tradeNo: string): Promise<Order> {
  return unwrap(
    api().POST('/api/v1/orders/{trade_no}/cancel', { params: { path: { trade_no: tradeNo } } }),
  );
}

/**
 * `/plan` —— P1。page-inventory §3.1 #7、§3.2.4。
 *
 * 相对竞品的四处改动，每一处都有理由（§3.2.4 表格）：
 *  + 加长周期梯度折扣（竞品长周期零折扣，用户没有理由预付；折扣同时摊薄 USDT 归集成本）
 *  + 设备数加粗做主卖点（竞品三档统一为 3，未作杠杆）
 *  − 删除「峰值带宽 500/1000 Mbps」分档（真实瓶颈在境内纵深，这个数字是营销噪音）
 *  + 加「不承诺流媒体解锁」（GCP IP 段普遍被封，做不到就不能写）
 *
 * ⚠️ 价格数字一律不写死在前端。pricing-and-plans §7 的定价还是 P0 阻塞项。
 * 这条在这一页有一个具体的落点：**折扣角标是拿 API 给的价格现算的**（`bestDiscountPercent`），
 * 不是 §3.2.4 表里那三个数（季 9 折 / 半年 85 折 / 年 75 折）。那三个是**产品意图不是事实源** ——
 * 写进代码的那一刻，运营改一次价它们就成了「页面写着 9 折、结算按新价扣」这种最难解释的错。
 * `PlanPage.test.tsx` 钉死了这一条。
 *
 * 三个请求，**各自一套三态**（§2.2）：
 *  ① `listPlans`   —— 整页的主体
 *  ② `verifyCoupon` —— 用户点「校验」才发，失败不影响套餐列表，也不挡下单
 *  ③ `createOrder`  —— 写操作：二次确认 + 幂等键（api-contract §9）
 * 合成一个 loading 的话，优惠码校验失败会把已经选好的套餐一起吞掉。
 */
import { useMemo, useState } from 'react';
import { useNavigate } from 'react-router';
import {
  Badge,
  Button,
  Card,
  CardTitle,
  EmptyState,
  Icon,
  LinkButton,
  LoadingState,
  SkeletonCard,
  cx,
  formatBytes,
  formatCny,
} from './_imports.ts';
import { unwrap, type ApiError } from '@babelplus/shared/api';
import { api } from '../lib/api.ts';
import {
  FormAlert,
  TextField,
  asApiError,
  useApiQuery,
  useRetryCountdown,
} from './ticket-common.tsx';
import {
  PERIOD_ORDER,
  bestDiscountPercent,
  cheapestPrice,
  discountPercent,
  periodLabel,
  priceOf,
  type CouponVerifyResult,
  type Order,
  type Period,
  type Plan,
  type PlanPrice,
} from './billing/format.ts';
import {
  BillingErrorState,
  ConfirmPanel,
  billingErrorCopy,
  useIdempotencyKey,
} from './billing/checkout.tsx';

type PlanKind = Plan['type'];

/** 下单页要花余额，所以余额必须现取，不能用开机时的 CurrentUser 快照。 */
const loadWalletBalance = (): Promise<{ balance_amount: number }> =>
  unwrap(api().GET('/api/v1/user/wallet'));

const loadPlans = (): Promise<Plan[]> => unwrap(api().GET('/api/v1/plans'));

export default function PlanPage() {
  const plans = useApiQuery(loadPlans, [], '套餐加载失败');

  return (
    <>
      <header className="mb-5 sm:mb-6">
        <h1 className="text-xl font-semibold tracking-tight text-fg sm:text-2xl">套餐</h1>
        <p className="mt-1 max-w-2xl text-sm text-fg-muted">按周期买，或者只买一个流量包。</p>
      </header>

      <div className="space-y-4">
        {/* 🔴 诚实声明。§3.2.4 要求「与卖点同等字号，不放在折叠区」。
            GCP IP 段普遍被主流流媒体平台封禁，这是结构性劣势，只能诚实标注。
            它是**静态**的，所以放在任何请求的三态之外 —— 套餐读不出来的时候，
            这句话反而更该被看见（用户此刻正在决定要不要买）。 */}
        <Card>
          <p className="text-base font-medium text-fg">我们不承诺流媒体解锁。</p>
          <p className="mt-1.5 text-sm leading-relaxed text-fg-muted">
            出口是 GCP 的 IP 段，主流流媒体平台普遍会拦。如果你的主要用途是看剧，这个服务大概率不适合你 ——
            现在说清楚，比你付完钱再发现好。
          </p>
        </Card>

        <PlanSection query={plans} />
      </div>
    </>
  );
}

/* ─────────────────────────── 套餐列表（请求 ①） ─────────────────────────── */

function PlanSection({ query }: { query: ReturnType<typeof useApiQuery<Plan[]>> }) {
  const [kind, setKind] = useState<PlanKind>('period');
  const [selectedId, setSelectedId] = useState<number | null>(null);

  if (query.state === 'loading') {
    return (
      <LoadingState slowHint>
        <SkeletonCard lines={5} />
      </LoadingState>
    );
  }

  if (query.state === 'error' && query.error) {
    // TODO(P1): §3.2.4 的错态要求引导到公开站 `/pricing`（静态副本，API 挂了仍可读）。
    // **现在没有那个页面** —— 落地页那套前端目录都还不存在（roadmap §5.2、web/README §8），
    // runtime-config 里也没有 pricingUrl。编一个 URL 出来会把用户导到 404，
    // 所以这里退到「提工单」，等落地页落地后再换成静态价格页的深链。
    return (
      <BillingErrorState
        error={query.error}
        what="套餐列表"
        onRetry={query.reload}
        extra={<LinkButton href="/ticket">提交工单</LinkButton>}
      />
    );
  }

  const all = query.data ?? [];

  // §3.2.4 的空态：**不是「暂无数据」**，要给下一步动作。
  if (all.length === 0) {
    return (
      <EmptyState
        title="暂时没有开放的套餐"
        description="现在没有可以下单的套餐。如果你正等着续费，提个工单我们直接处理。"
        action={
          <LinkButton tone="primary" href="/ticket">
            提交工单 <Icon.ArrowRight size={14} />
          </LinkButton>
        }
      />
    );
  }

  const visible = all.filter((p) => p.type === kind);
  const selected = all.find((p) => p.id === selectedId) ?? null;

  return (
    <>
      <Card>
        <CardTitle hint="照抄竞品的信息架构，这部分它做对了">按周期 / 按流量包</CardTitle>

        {/* 两个 tab。用 role=tablist 而不是两个按钮：读屏用户需要知道这是一组互斥选择。 */}
        <div role="tablist" aria-label="套餐类型" className="mb-4 flex gap-2">
          <KindTab current={kind} value="period" onSelect={setKind}>
            按周期
          </KindTab>
          <KindTab current={kind} value="traffic_pack" onSelect={setKind}>
            按流量包
          </KindTab>
        </div>

        {visible.length === 0 ? (
          <p className="text-sm text-fg-muted">
            {kind === 'period' ? '现在没有开放的周期套餐。' : '现在没有开放的流量包。'}
            换一个 tab 看看。
          </p>
        ) : (
          <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
            {visible.map((plan) => (
              <PlanCard
                key={plan.id}
                plan={plan}
                selected={plan.id === selectedId}
                onSelect={() => setSelectedId(plan.id === selectedId ? null : plan.id)}
              />
            ))}
          </div>
        )}
      </Card>

      {selected ? <OrderComposer key={selected.id} plan={selected} /> : null}
    </>
  );
}

function KindTab({
  current,
  value,
  onSelect,
  children,
}: {
  current: PlanKind;
  value: PlanKind;
  onSelect: (kind: PlanKind) => void;
  children: string;
}) {
  const active = current === value;
  return (
    <button
      type="button"
      role="tab"
      aria-selected={active}
      onClick={() => onSelect(value)}
      className={cx(
        'min-h-11 rounded-lg border px-4 text-sm font-medium transition-colors',
        active ? 'border-accent bg-accent/10 text-accent' : 'border-line text-fg-muted hover:bg-surface-alt',
      )}
    >
      {children}
    </button>
  );
}

/**
 * 套餐卡。字段顺序照 §3.2.4：名称 · 流量 · **设备数（加粗）** · 价格 · 折扣角标。
 *
 * 🔴 这里**没有**「峰值带宽」的展示位，而且将来 API 加了这个字段也不显示（§3.2.4 的第三处改动）。
 * 跨境链路的真实瓶颈在中国境内纵深（SIGMETRICS 2020：71% 的瓶颈跳在境内），
 * 一个 500/1000 Mbps 的数字对用户没有意义 —— 它只会成为一条我们兑现不了的暗示。
 * `Plan.speed_limit_mbps` 因此被刻意忽略：这是**决定**，不是漏写。
 */
function PlanCard({
  plan,
  selected,
  onSelect,
}: {
  plan: Plan;
  selected: boolean;
  onSelect: () => void;
}) {
  const from = cheapestPrice(plan.prices);
  const discount = bestDiscountPercent(plan.prices);

  return (
    <button
      type="button"
      aria-pressed={selected}
      onClick={onSelect}
      className={cx(
        'flex flex-col gap-2 rounded-xl border p-4 text-left transition-colors',
        selected ? 'border-accent bg-accent/5' : 'border-line hover:bg-surface-alt',
      )}
    >
      <div className="flex flex-wrap items-baseline justify-between gap-x-2 gap-y-1">
        <span className="text-base font-semibold text-fg">{plan.name}</span>
        {/* 角标只在**算得出来**的时候出现：`discountPercent` 拿 API 的价格现算，
            算不出（没有月付基准 / 并不比月付便宜）就不显示，不写死任何折扣数字。 */}
        {discount === null ? null : <Badge tone="ok">最低 {discount / 10} 折</Badge>}
      </div>

      <dl className="space-y-1 text-sm">
        <div className="flex justify-between gap-2">
          <dt className="text-fg-muted">流量</dt>
          <dd className="text-fg">{formatBytes(plan.transfer_enable_bytes)}</dd>
        </div>
        <div className="flex justify-between gap-2">
          {/* 设备数加粗 —— §3.2.4 唯一点名要加粗的字段，它是我们相对竞品的差异化杠杆。 */}
          <dt className="text-fg-muted">同时在线设备</dt>
          <dd className="font-semibold text-fg">
            {plan.device_limit > 0 ? `${plan.device_limit} 台` : '不限'}
          </dd>
        </div>
        <div className="flex justify-between gap-2">
          <dt className="text-fg-muted">价格</dt>
          <dd className="text-fg">
            {from === null ? '—' : `${formatCny(from.amount)} 起`}
          </dd>
        </div>
      </dl>

      {plan.description ? (
        <p className="text-xs leading-relaxed text-fg-muted">{plan.description}</p>
      ) : null}

      <span className="mt-1 text-xs font-medium text-accent">
        {selected ? '已选中，下面配置这一单' : '选它'}
      </span>
    </button>
  );
}

/* ───────────────────── 周期 / 优惠码 / 余额 + 下单（请求 ②③） ───────────────────── */

/**
 * 一单的配置区。
 *
 * 🔴 **这里一分钱都不自己算。** 优惠抵扣来自 `verifyCoupon` 的 `discount_amount`，
 * 升级折抵与余额抵扣由服务端在 `createOrder` 里算（算法本身在 api-contract §14 还没裁决）。
 * 前端把「原价」与「已知的抵扣」如实列出来，并明说**最终应付以订单页为准** ——
 * 在这里先算一个数出来，等于用一个我们算不准的数字去和收银台上那个真数字打架。
 */
function OrderComposer({ plan }: { plan: Plan }) {
  const navigate = useNavigate();

  /**
   * 可选周期。**滤掉 `onetime`**（周期套餐上）：ADR 0013 §2.2 裁决 P1 阶段不售不限时套餐，
   * 后端对 `period=onetime` 的周期单直接 422「暂不销售不限时套餐」。
   * 把一个必然被拒的选项摆出来，用户会以为是自己填错了。
   * 流量包相反 —— 它只有一次性价格，那一档就是它唯一的周期。
   */
  const periods = useMemo(() => selectablePeriods(plan), [plan]);
  const [period, setPeriod] = useState<Period | null>(() => periods[0]?.period ?? null);

  const [couponInput, setCouponInput] = useState('');
  const [coupon, setCoupon] = useState<CouponVerifyResult | null>(null);
  const [couponPending, setCouponPending] = useState(false);
  const [couponError, setCouponError] = useState<ApiError | null>(null);

  const [useBalance, setUseBalance] = useState(false);
  const [confirming, setConfirming] = useState(false);
  const [orderPending, setOrderPending] = useState(false);
  const [orderError, setOrderError] = useState<ApiError | null>(null);
  const countdown = useRetryCountdown();

  const price = priceOf(plan.prices, period);
  // 只有**校验通过**的码才会被提交。校验没过还提交上去，服务端会以 422 拒绝，
  // 而用户看到的是「刚才明明说可以用」——user-journey 点名的那种背叛感。
  const appliedCoupon = coupon !== null && coupon.valid ? coupon.code : null;
  // 🔴 **余额不能读开机时的 CurrentUser 快照。** `user` 由 auth bootstrap 填一次，
  //    之后只在登录 / 手动 reload 时更新 —— 而这一页恰恰是花余额的地方。
  //    用户在本次会话里充过值、转过佣金、或用余额付过另一张单之后，
  //    这里读到的仍是进站那一刻的数字：勾选框会按一个过期的值决定能不能勾，
  //    展示的「当前余额」也是错的。改成进这一页时真去取一次（与 WalletPage 同源）。
  const walletQuery = useApiQuery(loadWalletBalance, [], '余额加载失败');
  const balance = walletQuery.data?.balance_amount ?? 0;

  /**
   * 幂等键跟着**载荷**走。载荷的每一个字段都进签名 —— 漏掉一个，
   * 改了那个字段再提交就会撞上 409 `STATE_IDEMPOTENCY_MISMATCH`。
   */
  const idempotencyKey = useIdempotencyKey(
    `${plan.id}|${period ?? ''}|${appliedCoupon ?? ''}|${useBalance ? '1' : '0'}`,
  );

  function resetCoupon() {
    setCoupon(null);
    setCouponError(null);
  }

  async function onVerifyCoupon(): Promise<void> {
    const code = couponInput.trim();
    if (code === '' || couponPending || period === null) return;
    setCouponPending(true);
    setCouponError(null);
    setCoupon(null);
    try {
      setCoupon(await verifyCoupon({ code, planId: plan.id, period }));
    } catch (cause) {
      setCouponError(asApiError(cause, '优惠码校验失败'));
    } finally {
      setCouponPending(false);
    }
  }

  async function onCreateOrder(): Promise<void> {
    // 单飞：挡住重复点击。真正挡住「超时后重发下出两张单」的是下面那个 `Idempotency-Key`。
    if (orderPending || countdown.seconds !== null || period === null) return;
    setOrderPending(true);
    setOrderError(null);
    try {
      const order = await createOrder({
        idempotencyKey,
        planId: plan.id,
        period,
        couponCode: appliedCoupon,
        useBalance,
      });
      // 下单成功直接进收银台。**这里不弹任何「下单成功」的提示** ——
      // 用户要的下一件事是付款，而收银台页自己会说清楚状态。
      navigate(`/order/${encodeURIComponent(order.trade_no)}`);
    } catch (cause) {
      const apiError = asApiError(cause, '下单失败');
      setOrderError(apiError);
      countdown.start(apiError.retryAfterSeconds);
      setOrderPending(false);
      // 确认框**不关**：关掉了用户就不知道自己那一下到底有没有生效，
      // 而在支付路径上「不知道有没有生效」的下一个动作通常是再点一次。
    }
  }

  const orderCopy =
    orderError === null
      ? null
      : billingErrorCopy(orderError, { fallbackTitle: '下单没能完成', retrySeconds: countdown.seconds });

  return (
    <Card>
      <CardTitle hint={`createOrder · ${plan.name}`}>配置这一单</CardTitle>

      <div className="space-y-4">
        {/* ① 周期。竞品季付 = 3×月付、半年付 = 6×月付，长周期零折扣；
            我们的梯度折扣**从 API 读**，不写死 —— 折扣同时解决两件事：
            给用户预付的理由，以及摊薄 USDT 归集成本。 */}
        {plan.type === 'period' ? (
          <fieldset>
            <legend className="mb-1.5 text-sm font-medium text-fg">付费周期</legend>
            {periods.length === 0 ? (
              <p className="text-sm text-warn">这个套餐现在没有可下单的周期，换一个套餐看看。</p>
            ) : (
              <div className="flex flex-wrap gap-2">
                {periods.map((p) => (
                  <PeriodChoice
                    key={p.period}
                    price={p}
                    prices={plan.prices}
                    active={p.period === period}
                    onSelect={() => {
                      setPeriod(p.period);
                      // 换周期后旧的校验结果作废：优惠码的适用范围是按周期算的
                      // （`evaluateCoupon` 的 `PeriodOutOfScope`），留着上一档的结论就是在骗人。
                      resetCoupon();
                      setConfirming(false);
                    }}
                  />
                ))}
              </div>
            )}
          </fieldset>
        ) : (
          <p className="text-sm text-fg-muted">
            流量包是一次性购买：{price === null ? '—' : formatCny(price.amount)}，
            不改变你的到期时间。
          </p>
        )}

        {/* ② 优惠码。**独立三态**：校验失败不影响下单按钮（不带码照样能下）。 */}
        <div>
          <TextField
            label="优惠码（可选）"
            name="coupon"
            value={couponInput}
            disabled={orderPending}
            onChange={(value) => {
              setCouponInput(value);
              resetCoupon();
            }}
            placeholder="有就填，没有就跳过"
            hint={
              <>
                校验只是查一下能不能用，<strong className="font-medium text-fg">不会</strong>核销这张券。
              </>
            }
          />
          <div className="mt-2 flex flex-wrap items-center gap-2">
            <Button
              className="min-h-9 px-3 text-xs"
              disabled={couponInput.trim() === '' || couponPending || period === null}
              onClick={() => void onVerifyCoupon()}
            >
              {couponPending ? '正在校验…' : '校验优惠码'}
            </Button>
            {coupon !== null ? <CouponVerdict result={coupon} /> : null}
          </div>
          {couponError ? (
            <div className="mt-2">
              <FormAlert>
                <span className="font-medium">
                  {billingErrorCopy(couponError, { fallbackTitle: '优惠码没能校验' }).title}
                </span>
                <br />
                {billingErrorCopy(couponError, { fallbackTitle: '优惠码没能校验' }).description}
                <br />
                <span className="text-xs">校验失败不挡下单 —— 不带优惠码直接下单也可以。</span>
              </FormAlert>
            </div>
          ) : null}
        </div>

        {/* ③ 余额抵扣。余额**仅可消费不可提现**（product-brief §6），所以这里只有「用不用」。 */}
        <label className="flex items-start gap-2 text-sm text-fg">
          <input
            type="checkbox"
            className="mt-1"
            checked={useBalance}
            disabled={orderPending || balance <= 0}
            onChange={(event) => {
              setUseBalance(event.target.checked);
              setConfirming(false);
            }}
          />
          <span>
            用账户余额抵扣（当前余额 <span className="font-mono">{formatCny(balance)}</span>）
            <br />
            <span className="text-xs text-fg-muted">
              {balance > 0
                ? '抵多少由服务端算，抵不完的部分用 USDT 付。'
                : '余额为 0，这一项现在没有意义。'}
            </span>
          </span>
        </label>

        {/* 金额预览。🔴 只列**已知的**数，不做加减法算「应付」——
            升级折抵的算法在 api-contract §14 还没裁决，前端算出来的任何一个「应付」
            都可能和订单页那个真数字打架，而用户会相信先看到的那个。 */}
        <dl className="rounded-lg border border-line bg-surface-alt/60 p-3 text-sm">
          <div className="flex justify-between gap-2 py-0.5">
            <dt className="text-fg-muted">
              {plan.name} · {period === null ? '—' : periodLabel(period)}
            </dt>
            <dd className="text-fg">{price === null ? '—' : formatCny(price.amount)}</dd>
          </div>
          {coupon !== null && coupon.valid && coupon.discount_amount !== undefined ? (
            <div className="flex justify-between gap-2 py-0.5">
              <dt className="text-fg-muted">优惠码 {coupon.code}</dt>
              <dd className="text-ok">−{formatCny(coupon.discount_amount)}</dd>
            </div>
          ) : null}
          <p className="mt-2 text-xs leading-relaxed text-fg-subtle">
            实付金额由服务端计算（余额抵扣、升级折抵都在下单那一步生效），
            <strong className="font-medium text-fg">以订单页显示的为准</strong>。
          </p>
        </dl>

        {/* ④ 二次确认（api-contract §9）。一次点击不下单。 */}
        <div>
          <Button
            tone="primary"
            disabled={period === null || price === null || orderPending || confirming}
            onClick={() => setConfirming(true)}
          >
            <Icon.Package size={14} /> 去下单
          </Button>

          <ConfirmPanel
            open={confirming}
            title={`确认下单：${plan.name} · ${period === null ? '' : periodLabel(period)}`}
            consequences={[
              <>
                这一步只创建一张<strong className="font-medium text-fg">待支付</strong>的订单，
                不会扣任何钱。付款在订单页完成。
              </>,
              <>
                标价 {price === null ? '—' : formatCny(price.amount)}
                {appliedCoupon ? `，已应用优惠码 ${appliedCoupon}` : ''}
                {useBalance ? '，并用余额抵扣' : ''}
                ；<strong className="font-medium text-fg">实付以订单页为准</strong>。
              </>,
              <>
                重复点击不会下出第二张单：这次提交带幂等键{' '}
                <code className="font-mono text-xs">{idempotencyKey.slice(0, 8)}…</code>
                ，服务端对同一把键的同一份内容只执行一次。
              </>,
            ]}
            confirmLabel={countdown.seconds === null ? '确认下单' : `${countdown.seconds} 秒后可再试`}
            pending={orderPending}
            error={
              orderCopy === null ? null : (
                <>
                  <span className="font-medium">{orderCopy.title}</span>
                  <br />
                  {orderCopy.description}
                  {orderError?.requestId ? (
                    <>
                      <br />
                      <span className="font-mono text-xs">请求号 {orderError.requestId}</span>
                    </>
                  ) : null}
                </>
              )
            }
            onCancel={() => setConfirming(false)}
            onConfirm={() => void onCreateOrder()}
          />
        </div>
      </div>
    </Card>
  );
}

function PeriodChoice({
  price,
  prices,
  active,
  onSelect,
}: {
  price: PlanPrice;
  prices: readonly PlanPrice[];
  active: boolean;
  onSelect: () => void;
}) {
  // 现算，不写死。算不出来就不显示角标 —— 一个错的折扣数字比没有折扣数字糟得多。
  const pct = discountPercent(prices, price.period);
  return (
    <button
      type="button"
      aria-pressed={active}
      onClick={onSelect}
      className={cx(
        'min-h-11 rounded-lg border px-3 text-sm transition-colors',
        active ? 'border-accent bg-accent/10 text-accent' : 'border-line text-fg hover:bg-surface-alt',
      )}
    >
      <span className="font-medium">{periodLabel(price.period)}</span>
      <span className="ml-2">{formatCny(price.amount)}</span>
      {pct === null ? null : <span className="ml-2 text-xs text-ok">{pct / 10} 折</span>}
    </button>
  );
}

/**
 * 优惠码的判定结果。
 * 🔴 `valid = false` 时**一律显示服务端给的 `reason`**，不自己归纳 ——
 * `evaluateCoupon` 的判定顺序（券本身 → 这个人 → 这个订单）是产品裁决过的，
 * 前端换一句笼统的「优惠码不可用」会把「你不是新用户」和「这张券过期了」混成一件事。
 */
function CouponVerdict({ result }: { result: CouponVerifyResult }) {
  if (!result.valid) {
    return <Badge tone="warn">{result.reason ?? '这张优惠码现在不能用'}</Badge>;
  }
  return (
    <Badge tone="ok">
      可用
      {result.discount_amount === undefined ? '' : ` · 抵 ${formatCny(result.discount_amount)}`}
    </Badge>
  );
}

/* ───────────────────────────── 数据 ───────────────────────────── */

/**
 * 这个套餐可以下单的周期，按「从短到长」排。
 *
 * 周期套餐滤掉 `onetime`（理由见 `OrderComposer` 里的注释）；流量包只有 `onetime`，原样保留。
 * 契约的 `PlanPrice.period` 枚举里有 `two_yearly` / `three_yearly`，而 `plans` 表根本没有这两列
 * （ADR 0013 §4.7 登记的契约/DB 不一致之一）—— 所以这里按**返回了什么就显示什么**做，
 * 不按枚举去反推该有哪些档。
 */
function selectablePeriods(plan: Plan): PlanPrice[] {
  const usable = plan.prices.filter((p) => (plan.type === 'period' ? p.period !== 'onetime' : true));
  return [...usable].sort(
    (a, b) => PERIOD_ORDER.indexOf(a.period) - PERIOD_ORDER.indexOf(b.period),
  );
}

function verifyCoupon(input: {
  code: string;
  planId: number;
  period: Period;
}): Promise<CouponVerifyResult> {
  return unwrap(
    api().POST('/api/v1/coupons/verify', {
      body: { code: input.code, plan_id: input.planId, period: input.period },
    }),
  );
}

/**
 * 下单。**`Idempotency-Key` 是契约必填的请求头**（`createOrder` 的 `parameters.header`），
 * 不是可选的保险 —— 缺了它服务端直接 422「缺少 Idempotency-Key 请求头」。
 */
function createOrder(input: {
  idempotencyKey: string;
  planId: number;
  period: Period;
  couponCode: string | null;
  useBalance: boolean;
}): Promise<Order> {
  const body = {
    plan_id: input.planId,
    period: input.period,
    use_balance: input.useBalance,
    ...(input.couponCode === null ? {} : { coupon_code: input.couponCode }),
  };
  return unwrap(
    api().POST('/api/v1/orders', {
      params: { header: { 'Idempotency-Key': input.idempotencyKey } },
      body,
    }),
  );
}

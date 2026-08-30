/**
 * `/order` —— P1。page-inventory §3.1 #8、§3.2.5。
 * 移动端必须卡片化：订单是 §2.3 点名的三个「重灾区」之一。
 *
 * 两条这一页最容易被顺手改错的规则，测试各钉了一条：
 *
 *  ① **游标分页，不做「共 N 条 / 第 x 页」的页码器。** 用户面**永不返回 `total`**
 *     （api-contract §2.4、`ListOrders` 的注释：COUNT(*) 在 db-f1-micro 上是实打实的开销）。
 *     「还有没有下一页」的唯一判据是 `meta.has_more` + `meta.next_cursor`，
 *     **不是**「这一页返回的条数等于 limit」—— 总数正好整除时后者会判出一页空数据，
 *     而空页在前端长得像加载失败。
 *
 *  ② **`underpaid` 在这一页看不到，这不是偷懒。** 契约的 `OrderStatus` 只有 6 个值，
 *     后端的 `orderStatusView` 把 DB 的 `paying` / `underpaid` / `paid` 三个状态**并成了
 *     `processing`**（它自己的注释写着「并档是有损的」，并指明精细状态由 `PaymentCheckout.state`
 *     在详情页给出）。所以列表上的每一行 `processing` 都必须把用户送进详情页，
 *     并且**不能**说「处理中，无需操作」—— 那正好把需要补款的那一单说成不用管。
 */
import { useState } from 'react';
import { Link } from 'react-router';
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
  formatCny,
  formatDateTime,
} from './_imports.ts';
import { unwrap, unwrapWithMeta, type ApiError, type Meta } from '@babelplus/shared/api';
import { api } from '../lib/api.ts';
import { FormAlert, asApiError, useApiQuery } from './ticket-common.tsx';
import { isCancellable, orderStatusMeta, orderTypeLabel, periodLabel, type Order } from './billing/format.ts';
import { BillingErrorState, ConfirmPanel, billingErrorCopy } from './billing/checkout.tsx';

/** 一页多少条。**不做无限滚动** —— 「加载更多」是可以停下来的，滚动不是。 */
const PAGE_SIZE = 20;

interface OrderPage {
  readonly items: readonly Order[];
  readonly meta: Meta;
}

function listOrdersPage(cursor: string | null): Promise<OrderPage> {
  const query = cursor === null ? { limit: PAGE_SIZE } : { limit: PAGE_SIZE, cursor };
  return unwrapWithMeta(api().GET('/api/v1/orders', { params: { query } })).then((envelope) => ({
    items: envelope.data,
    meta: envelope.meta,
  }));
}

const loadFirstPage = (): Promise<OrderPage> => listOrdersPage(null);

export default function OrderListPage() {
  const orders = useApiQuery(loadFirstPage, [], '订单列表加载失败');

  return (
    <>
      <header className="mb-5 flex flex-col gap-3 sm:mb-6 sm:flex-row sm:items-start sm:justify-between">
        <div className="min-w-0">
          <h1 className="text-xl font-semibold tracking-tight text-fg sm:text-2xl">订单</h1>
          <p className="mt-1 max-w-2xl text-sm text-fg-muted">新购、续费、升级的记录都在这里。</p>
        </div>
        <div className="flex shrink-0 flex-wrap gap-2">
          <LinkButton tone="primary" href="/plan">
            <Icon.Package size={14} /> 看套餐
          </LinkButton>
        </div>
      </header>

      <OrderListSection query={orders} />
    </>
  );
}

function OrderListSection({ query }: { query: ReturnType<typeof useApiQuery<OrderPage>> }) {
  // 「加载更多」拿到的后续页**单独存**，不塞回第一页的 query 里 ——
  // 重试第一页时这些应该一起作废，而它们确实会随 query 重建而清掉。
  const [more, setMore] = useState<readonly Order[]>([]);
  const [moreMeta, setMoreMeta] = useState<Meta | null>(null);
  const [morePending, setMorePending] = useState(false);
  const [moreError, setMoreError] = useState<ApiError | null>(null);

  if (query.state === 'loading') {
    return (
      <LoadingState>
        <SkeletonCard lines={5} />
      </LoadingState>
    );
  }

  if (query.state === 'error' && query.error) {
    return <BillingErrorState error={query.error} what="订单列表" onRetry={query.reload} />;
  }

  const first = query.data;
  if (!first) return null;
  const items = [...first.items, ...more];
  const meta = moreMeta ?? first.meta;

  if (items.length === 0) {
    return (
      <EmptyState
        title="还没有订单"
        description="下单后这里会出现记录，包括正在等链上确认的那些。"
        action={
          <LinkButton tone="primary" href="/plan">
            去看套餐 <Icon.ArrowRight size={14} />
          </LinkButton>
        }
      />
    );
  }

  async function loadMore(): Promise<void> {
    if (morePending) return;
    // 🔴 判据是 `next_cursor`，不是「条数够不够一页」。见文件头 ①。
    const cursor = meta.next_cursor;
    if (!cursor) return;
    setMorePending(true);
    setMoreError(null);
    try {
      const page = await listOrdersPage(cursor);
      setMore((prev) => [...prev, ...page.items]);
      setMoreMeta(page.meta);
    } catch (cause) {
      setMoreError(asApiError(cause, '没能加载更多'));
    } finally {
      setMorePending(false);
    }
  }

  return (
    <Card>
      {/* hint 里**不写「共 N 条」** —— 用户面没有 total，写出来的任何一个总数都是编的。
          「已加载 N 条」是事实：它说的是这个页面现在手上有多少条。 */}
      <CardTitle hint={`已加载 ${items.length} 条`}>订单列表</CardTitle>

      {/* 5 列表头只在 ≥640px 出现；<640px 整行堆叠成卡片（§2.3 M1 硬规则：
          订单是点名的重灾区之一，**不允许横向滚动表格**）。 */}
      <div
        className="hidden grid-cols-6 gap-x-3 border-b border-line pb-2 text-xs font-medium text-fg-muted sm:grid"
        aria-hidden="true"
      >
        <span className="col-span-2">订单号</span>
        <span>类型</span>
        <span>金额</span>
        <span>状态</span>
        <span>时间</span>
      </div>

      <ul className="divide-y divide-line">
        {items.map((order) => (
          <OrderRow
            key={order.trade_no}
            order={order}
            onCancelled={(next) => {
              // 取消成功后**就地改这一行**，不把整个列表打回 loading ——
              // 用户刚点完取消，眼前的列表突然变成骨架屏，会让人以为自己那一下没生效。
              // 两处都改：这一行可能来自第一页，也可能来自「加载更多」拿到的后续页。
              // 找不到的那一边是 no-op，不需要先判断它在哪。
              query.patch((page) => ({
                ...page,
                items: page.items.map((o) => (o.trade_no === next.trade_no ? next : o)),
              }));
              setMore((prev) => prev.map((o) => (o.trade_no === next.trade_no ? next : o)));
            }}
          />
        ))}
      </ul>

      {moreError ? (
        <div className="mt-3">
          {/* 分页失败**不清空已经加载出来的部分** —— 用户已经在看的东西不该因为下一页失败而消失。 */}
          <FormAlert>
            <span className="font-medium">
              {billingErrorCopy(moreError, { fallbackTitle: '没能加载更多' }).title}
            </span>
            <br />
            {billingErrorCopy(moreError, { fallbackTitle: '没能加载更多' }).description}
          </FormAlert>
        </div>
      ) : null}

      {meta.has_more && meta.next_cursor ? (
        <div className="mt-3">
          <Button onClick={() => void loadMore()} disabled={morePending}>
            {morePending ? '正在加载…' : '加载更多'}
          </Button>
        </div>
      ) : null}
    </Card>
  );
}

/* ───────────────────────────── 一行订单 ───────────────────────────── */

function OrderRow({ order, onCancelled }: { order: Order; onCancelled: (next: Order) => void }) {
  const [confirming, setConfirming] = useState(false);
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<ApiError | null>(null);

  const status = orderStatusMeta(order.status);
  const detailHref = `/order/${encodeURIComponent(order.trade_no)}`;

  async function onCancel(): Promise<void> {
    // 单飞挡住重复点击。**这就是取消这一步全部的「幂等」** ——
    // 契约给 `cancelOrder` 没有定义 `Idempotency-Key`（`operations.cancelOrder.parameters` 里没有这个头），
    // 发一个过去只会让代码看起来比实际更安全。好在重复取消是收敛的：
    // 第二次会拿到 409 `STATE_CONFLICT`（`CancelUserPendingOrder` 只认 pending），不会取消错东西。
    if (pending) return;
    setPending(true);
    setError(null);
    try {
      const next = await cancelOrder(order.trade_no);
      setConfirming(false);
      onCancelled(next);
    } catch (cause) {
      setError(asApiError(cause, '取消失败'));
    } finally {
      setPending(false);
    }
  }

  const copy = error === null ? null : billingErrorCopy(error, { fallbackTitle: '取消没能完成' });

  return (
    <li className="py-3">
      <div className="grid gap-1 text-sm sm:grid-cols-6 sm:items-center sm:gap-x-3">
        <Link
          to={detailHref}
          className="font-mono text-xs text-accent hover:underline sm:col-span-2"
        >
          {order.trade_no}
        </Link>
        <span className="text-xs text-fg-muted">
          {orderTypeLabel(order.type)}
          {order.period ? ` · ${periodLabel(order.period)}` : ''}
        </span>
        {/* 金额是「分」的 int64，走 `formatCny` 的整数除模 —— **任何地方都不得用浮点算钱**。 */}
        <span className="font-medium text-fg">{formatCny(order.payable_amount)}</span>
        <span>
          <Badge tone={status.tone}>{status.label}</Badge>
        </span>
        <span className="text-xs text-fg-muted">{formatDateTime(order.created_at)}</span>
      </div>

      <div className="mt-2 flex flex-wrap items-center gap-2">
        <Link to={detailHref} className="text-xs text-accent hover:underline">
          {order.status === 'pending' ? '去付款' : '查看详情'}
        </Link>
        {isCancellable(order) ? (
          <Button
            className="min-h-9 px-3 text-xs"
            disabled={pending || confirming}
            onClick={() => setConfirming(true)}
          >
            取消订单
          </Button>
        ) : null}
        {/* 🔴 见文件头 ②：`processing` 盖住了 `underpaid`，所以这一行**必须**把人送进详情页，
            而且这句话不能写成「无需操作」。 */}
        {order.status === 'processing' ? (
          <span className="text-xs text-fg-muted">
            钱在路上。到账、还是少付了需要补款，只有详情页看得到。
          </span>
        ) : null}
      </div>

      <ConfirmPanel
        open={confirming}
        title={`取消订单 ${order.trade_no}`}
        tone="danger"
        consequences={[
          '取消后这张单作废，不能恢复；想买同样的东西要重新下一单。',
          <>
            只有<strong className="font-medium text-fg">还没发起支付</strong>的订单可以取消。
            如果你已经转出去了 USDT，
            <strong className="font-medium text-fg">不要取消</strong>
            —— 先去详情页点「我已付款，帮我查一下」。
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
        onConfirm={() => void onCancel()}
      />
    </li>
  );
}

function cancelOrder(tradeNo: string): Promise<Order> {
  return unwrap(
    api().POST('/api/v1/orders/{trade_no}/cancel', { params: { path: { trade_no: tradeNo } } }),
  );
}

/**
 * 模块 3 · 订单管理 `/admin/orders` —— P1 / **M2**（涉资金，手机上要能查单）。
 *
 * 端点：`listAdminOrders`（GET /api/v1/admin/orders）。
 *
 * 这一页本身**没有写操作** —— D6 / D7 都在详情页上。列表要做对的只有三件事：
 * 查得到、看得懂状态、以及**把少付这条路指对**。
 *
 * 🔴 **列表上没有「状态筛选」，这是一处如实呈现的契约缺口，不是遗漏。**
 * `listAdminOrders` 的 query 只有 `limit` / `cursor` / `count` / `q`（去 schema.d.ts 核），
 * 服务端没有 `status` 参数。可以在前端对**已加载的这一页**做筛选，但那会造出一个
 * 更坏的形态：运维选了「少付」、第一页 20 行里恰好没有，于是他得到一个
 * 「当前没有少付订单」的结论 —— 而少付队列里可能有三十条。
 * 少付有它自己的**服务端队列**（`listAdminUnderpaidPayments`，在 `/admin/payments`），
 * 页头那个按钮就是脚手架注释里要的「独立快捷入口」，且它是真的。
 */
import { useState } from 'react';
import { Link } from 'react-router';
import { Button, Card, CardTitle, EmptyState, Icon, LinkButton, formatDateTime } from './_imports.ts';
import {
  ContractGapNotice,
  DataTable,
  ListLoading,
  MISSING,
  ModuleHeader,
  OrderStatusBadge,
  Pager,
  QueryErrorState,
  Td,
  TextField,
  Tr,
  cnyText,
  listAdminOrders,
  orderTypeLabel,
  useApiQuery,
  useCursorPager,
  useRememberedTotal,
  type AdminOrder,
} from './order-common.tsx';

export default function OrdersPage() {
  // `search` 是输入框里正在打的字，`q` 是**已提交**的那一次。
  // 分开是因为搜索走服务端全表扫（两个 ILIKE，见 admin_ops.sql 的登记）——
  // 边打边搜等于每敲一个字符就让 db-f1-micro 全表扫一遍 orders + users。
  const [search, setSearch] = useState('');
  const [q, setQ] = useState('');
  const pager = useCursorPager();

  const query = useApiQuery(
    () => listAdminOrders({ cursor: pager.cursor, q, count: pager.atFirstPage }),
    [pager.cursor, q],
    '订单列表加载失败',
  );
  const total = useRememberedTotal(query.data?.meta);

  function submitSearch() {
    // 改条件必须回到第一页：旧游标是「从那一行之后」，在新条件下解出的是一段
    // 无意义的位置 —— 现象是「搜出来的第一页少了几条」，且不报错。
    pager.reset();
    setQ(search);
  }

  return (
    <>
      <ModuleHeader
        title="订单"
        description="涉资金。查单、对账、处理 underpaid 都从这里开始。"
        priority="P1"
        mobile="M2"
        actions={
          <LinkButton href="/admin/payments">
            少付队列 <Icon.ArrowRight size={14} />
          </LinkButton>
        }
      />

      <Card>
        <CardTitle hint="搜索走服务端，同时匹配订单号与用户邮箱">订单列表</CardTitle>

        <form
          className="mb-4 flex flex-col gap-3 sm:flex-row sm:items-end"
          onSubmit={(event) => {
            event.preventDefault();
            submitSearch();
          }}
        >
          <div className="flex-1">
            <TextField
              label="搜索"
              value={search}
              onChange={setSearch}
              placeholder="订单号或用户邮箱"
              hint={
                <>
                  服务端按 <code className="font-mono">ILIKE %关键词%</code> 同时搜
                  <strong className="font-medium text-fg"> 订单号</strong> 与
                  <strong className="font-medium text-fg"> 用户邮箱</strong>。
                  输入 <code className="font-mono">%</code> 或 <code className="font-mono">_</code>{' '}
                  只会当成普通字符（服务端做了转义），不会变成「返回全部」。
                </>
              }
            />
          </div>
          <div className="flex gap-2">
            <Button type="submit" tone="primary">
              搜索
            </Button>
            <Button
              type="button"
              disabled={search === '' && q === ''}
              onClick={() => {
                setSearch('');
                pager.reset();
                setQ('');
              }}
            >
              清空
            </Button>
          </div>
        </form>

        {query.state === 'loading' ? <ListLoading /> : null}

        {query.state === 'error' && query.error !== null ? (
          <QueryErrorState error={query.error} what="订单列表" onRetry={query.reload} />
        ) : null}

        {query.state === 'ready' && query.data !== null ? (
          query.data.data.length === 0 ? (
            <EmptyState
              title={q === '' ? '还没有订单' : '没有匹配的订单'}
              description={
                q === ''
                  ? '第一笔订单出现后，这里会同时显示待确认与已完成的。'
                  : '搜索同时匹配订单号与用户邮箱，两者都要逐字包含关键词。'
              }
              action={
                q === '' ? (
                  <LinkButton tone="primary" href="/admin/plans">
                    先确认套餐已上架 <Icon.ArrowRight size={14} />
                  </LinkButton>
                ) : (
                  <Button
                    tone="primary"
                    onClick={() => {
                      setSearch('');
                      pager.reset();
                      setQ('');
                    }}
                  >
                    清空搜索
                  </Button>
                )
              }
            />
          ) : (
            <>
              {/* M2：<768px 卡片化。六列表格在手机上会横滚，而横滚着念订单号
                  正是这一页最常见的现场 —— 用户在电话那头，运维拿着手机。 */}
              <ul className="space-y-3 sm:hidden">
                {query.data.data.map((row) => (
                  <OrderCard key={row.order.trade_no} row={row} />
                ))}
              </ul>

              <div className="hidden sm:block">
                <DataTable head={['订单号', '用户', '类型', '实付 / 原价', '状态', '下单时间']}>
                  {query.data.data.map((row) => (
                    <OrderRow key={row.order.trade_no} row={row} />
                  ))}
                </DataTable>
              </div>

              <Pager meta={query.data.meta} pager={pager} total={total} />
            </>
          )
        ) : null}

        <div className="mt-4 space-y-2">
          <ContractGapNotice title="列表上没有「渠道」这一列">
            <p>
              脚手架里写的六列含「渠道」，但契约的 <code className="font-mono">Order</code>{' '}
              上没有支付渠道字段（库里有 <code className="font-mono">orders.gateway</code>，
              序列化时被丢掉了）。宁可少一列，也不放一列恒为「—」的东西 ——
              那会让人以为这些订单真的没有渠道。补法是改 openapi，不是在这里编。
            </p>
          </ContractGapNotice>
          <ContractGapNotice title="没有按状态筛选">
            <p>
              <code className="font-mono">listAdminOrders</code> 的 query 只有{' '}
              <code className="font-mono">limit / cursor / count / q</code>。在前端只筛「当前这一页」
              会让「第一页没有少付」被读成「没有少付」，所以这里不做。
              少付有服务端的常驻队列，在 <Link className="underline" to="/admin/payments">支付与对账</Link>。
            </p>
          </ContractGapNotice>
        </div>
      </Card>
    </>
  );
}

/** 桌面端的一行。订单号是主键也是唯一的入口，所以整个单号就是链接。 */
function OrderRow({ row }: { row: AdminOrder }) {
  const o = row.order;
  return (
    <Tr>
      <Td>
        <Link className="font-mono text-sm text-accent underline-offset-2 hover:underline" to={detailHref(o.trade_no)}>
          {o.trade_no}
        </Link>
      </Td>
      <Td>
        <span className="break-all text-fg">{row.user_email}</span>
      </Td>
      <Td className="whitespace-nowrap">
        {orderTypeLabel(o.type)}
        {o.plan_name ? <span className="ml-1 text-xs text-fg-muted">{o.plan_name}</span> : null}
      </Td>
      <Td className="whitespace-nowrap">
        <span className="font-medium text-fg">{cnyText(o.payable_amount)}</span>
        {o.total_amount !== o.payable_amount ? (
          <span className="ml-1 text-xs text-fg-muted line-through">{cnyText(o.total_amount)}</span>
        ) : null}
      </Td>
      <Td className="whitespace-nowrap">
        <OrderStatusBadge status={String(o.status)} />
      </Td>
      <Td className="whitespace-nowrap text-fg-muted">{formatDateTime(o.created_at)}</Td>
    </Tr>
  );
}

/** 手机端的一张卡。字段顺序与表格一致，免得两块屏幕上读到的是两个不同的东西。 */
function OrderCard({ row }: { row: AdminOrder }) {
  const o = row.order;
  return (
    <li className="rounded-lg border border-line p-3">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <Link className="font-mono text-sm text-accent underline-offset-2 hover:underline" to={detailHref(o.trade_no)}>
          {o.trade_no}
        </Link>
        <OrderStatusBadge status={String(o.status)} />
      </div>
      <p className="mt-1.5 break-all text-sm text-fg">{row.user_email}</p>
      <p className="mt-1 text-sm text-fg-muted">
        {orderTypeLabel(o.type)}
        {o.plan_name ? ` · ${o.plan_name}` : ''} · <span className="font-medium text-fg">{cnyText(o.payable_amount)}</span>
        {o.total_amount !== o.payable_amount ? ` （原价 ${cnyText(o.total_amount)}）` : ''}
      </p>
      <p className="mt-1 text-xs text-fg-subtle">{formatDateTime(o.created_at) || MISSING}</p>
    </li>
  );
}

/**
 * 详情页的地址。**订单号必须 encode** —— 它由服务端生成（`BP` + 时间戳 + 随机），
 * 现在不含需要转义的字符，但一个「现在不会出问题」的拼接会在编号规则变一次之后
 * 变成一条打不开的链接，而且没有人会想到去看这一行。
 */
function detailHref(tradeNo: string): string {
  return `/admin/orders/${encodeURIComponent(tradeNo)}`;
}

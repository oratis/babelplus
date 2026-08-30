/**
 * 模块 14 · 支付与对账 `/admin/payments` —— P2 / M3。
 *
 * 端点：`listAdminUnderpaidPayments` · `listAdminPayments` · `updateAdminPayment`（D13）。
 *
 * `underpaid` 列表是这一页的**主角**，不是附属功能：
 * 我们用「小地址池 + 金额唯一性」匹配订单，**少付一定会发生**
 * （提币手续费从转出额扣是头号成因），这是设计的必然产物而不是异常。
 * 所以它排在最上面，且它的**正常状态是空的** —— 空不是「功能没做」。
 *
 * 🔴 **金额一律用 `1e-6 USDT` 的整数比较，任何环节都不转浮点。**
 * 这一页显示的三个数（应付 / 实收 / 差额）全部由服务端算好，前端只负责格式化。
 * 在这里做一次减法都是多余的：差额是服务端按**地址口径**聚合出来的，
 * 而不是「这一行的金额减去应付」。
 *
 * 🔴 **被 AML 拉黑的到账要留在列表里。** 服务端的「累计实收」刻意
 * **不认**拉黑的钱（入账路径不认，对账面也不能替它认），于是会出现
 * 「有一笔钱到了，但这张单还差全款」这种看起来矛盾的行 —— 那正是要给人看的东西。
 * 把它过滤掉等于让最需要人处理的那一批从对账页上消失。
 */
import { useState } from 'react';
import { Link } from 'react-router';
import { Badge, Button, Card, CardTitle, EmptyState, Icon, LinkButton, formatDateTime } from './_imports.ts';
import { DangerousAction } from '../components/DangerousAction.tsx';
import {
  ContractGapNotice,
  DangerOpsNote,
  DataTable,
  ListLoading,
  MISSING,
  ModuleHeader,
  PAYMENT_STATES,
  PAYMENT_STATE_LABEL,
  Pager,
  PaymentStateBadge,
  QueryErrorState,
  Td,
  TextField,
  Tr,
  listAdminPayments,
  listAdminUnderpaidPayments,
  updateAdminPayment,
  useApiQuery,
  useCursorPager,
  useRememberedTotal,
  usdt6Text,
  type AdminPayment,
  type PaymentState,
} from './order-common.tsx';

export default function PaymentsPage() {
  return (
    <>
      <ModuleHeader
        title="支付与对账"
        description="少付队列、支付流水、通道与地址池。"
        priority="P2"
        mobile="M3"
        actions={
          <LinkButton href="/admin/orders">
            去看订单 <Icon.ArrowRight size={14} />
          </LinkButton>
        }
      />

      <DangerOpsNote codes={['D13']} />

      <div className="space-y-4">
        <UnderpaidQueue />
        <PaymentLedger />
        <ChannelsAndPool />
      </div>
    </>
  );
}

/* ────────────────────────── 少付队列（这一页的主角） ────────────────────────── */

function UnderpaidQueue() {
  const pager = useCursorPager();
  const query = useApiQuery(
    () => listAdminUnderpaidPayments({ cursor: pager.cursor, count: pager.atFirstPage }),
    [pager.cursor],
    '少付队列加载失败',
  );
  const total = useRememberedTotal(query.data?.meta);

  return (
    <Card>
      <CardTitle hint="listAdminUnderpaidPayments · 常驻对账入口">underpaid 队列</CardTitle>

      <p className="mb-3 text-sm leading-relaxed text-fg-muted">
        判据是<strong className="font-medium text-fg">订单口径</strong>（这张单的累计实收 &lt; 应收，
        且订单仍未终结），不是「这一笔流水被标成了 underpaid」。补足到账之后没有任何机制
        回头去改第一笔的状态，按状态过滤的队列<strong className="font-medium text-fg">永远清不空</strong>。
      </p>

      {query.state === 'loading' ? <ListLoading /> : null}

      {query.state === 'error' && query.error !== null ? (
        <QueryErrorState error={query.error} what="少付队列" onRetry={query.reload} />
      ) : null}

      {query.state === 'ready' && query.data !== null ? (
        query.data.data.length === 0 ? (
          <EmptyState
            title="没有少付的订单"
            description="这是这个队列的正常状态。它是常驻的对账入口，不是异常处理页 —— 空着说明每一笔到账都对上了。"
            action={
              <LinkButton href="/admin/orders">
                去订单列表 <Icon.ArrowRight size={14} />
              </LinkButton>
            }
          />
        ) : (
          <>
            <DataTable head={['订单号', '应付', '累计实收', '还差', '这一笔', '到账时间']}>
              {query.data.data.map((row) => (
                <UnderpaidRow key={row.id} row={row} />
              ))}
            </DataTable>
            <Pager meta={query.data.meta} pager={pager} total={total} />
          </>
        )
      ) : null}

      <div className="mt-4 space-y-2">
        <div className="rounded-lg border border-line bg-surface-alt p-3 text-xs leading-relaxed text-fg-muted">
          <p className="font-medium text-fg">「累计实收」不含被 AML 拉黑的到账。</p>
          <p className="mt-1">
            服务端在算这个数时排除了 <code className="font-mono">aml_verdict = &apos;blacklisted&apos;</code> 的流水 ——
            入账路径不认这笔钱，对账面也不能替它认。所以这里会出现
            <strong className="font-medium text-fg">「有一笔钱到了，但这张单还差全款」</strong>
            这种看起来矛盾的行。那不是显示错误，是需要人去处理的那一类。
          </p>
        </div>
        <ContractGapNotice title="处理动作在这一页做不完">
          <p>
            少付的三条出路里，只有一条现在有端点：
          </p>
          <ul className="list-disc space-y-1 pl-4">
            <li>
              <strong className="font-medium text-fg">全额退回</strong> —— 走 D7，在订单详情页。
              退款一律进不可提现余额。
            </li>
            <li>
              <strong className="font-medium text-fg">按实收金额部分开通</strong> —— 契约里没有这个端点。
              现在只能用 D1（改配额 / 到期）或 D10（调整余额）在用户详情页手工做，
              两者都会各自留下审计。
            </li>
            <li>
              <strong className="font-medium text-fg">要求用户补款</strong> —— 契约里没有「催补款」端点，
              邮件模板那一组目前是 501（<code className="font-mono">mail_templates</code> 表不存在）。
              现在只能人工联系。
            </li>
          </ul>
          <p>
            🔴 <strong className="font-medium text-fg">这三条都不是「手工标记已支付」。</strong>
            D6 只用于「钱确实到了、只是回调丢了」，且必须带链上 txid ——
            拿它来处理少付等于把差额白送出去。
          </p>
        </ContractGapNotice>
      </div>
    </Card>
  );
}

function UnderpaidRow({ row }: { row: AdminPayment }) {
  // 🔴 只做一次相等比较，不做减法：差额由服务端按**地址口径**算好，
  //    这一行自己的金额与它不是同一个量。
  const nothingCounted = row.received_usdt6 === 0;
  return (
    <Tr>
      <Td>
        {row.trade_no ? (
          <Link
            className="font-mono text-sm text-accent underline-offset-2 hover:underline"
            to={`/admin/orders/${encodeURIComponent(row.trade_no)}`}
          >
            {row.trade_no}
          </Link>
        ) : (
          MISSING
        )}
      </Td>
      <Td className="whitespace-nowrap font-mono">{usdt6Text(row.expected_usdt6)}</Td>
      <Td className="whitespace-nowrap font-mono">
        {usdt6Text(row.received_usdt6)}
        {nothingCounted ? (
          <span className="ml-1 align-middle">
            <Badge tone="danger">一分没计入</Badge>
          </span>
        ) : null}
      </Td>
      <Td className="whitespace-nowrap font-mono text-warn">{usdt6Text(row.shortfall_usdt6)}</Td>
      <Td className="whitespace-nowrap">
        <PaymentStateBadge state={String(row.state)} />
        {row.txid ? (
          <span className="ml-1 font-mono text-xs text-fg-subtle" title={row.txid}>
            {row.txid.slice(0, 8)}…
          </span>
        ) : null}
      </Td>
      <Td className="whitespace-nowrap text-fg-muted">{formatDateTime(row.created_at)}</Td>
    </Tr>
  );
}

/* ────────────────────────── 支付流水 + D13 ────────────────────────── */

function PaymentLedger() {
  const pager = useCursorPager();
  const query = useApiQuery(
    () => listAdminPayments({ cursor: pager.cursor, count: pager.atFirstPage }),
    [pager.cursor],
    '支付流水加载失败',
  );
  const total = useRememberedTotal(query.data?.meta);
  const [editing, setEditing] = useState<AdminPayment | null>(null);

  return (
    <Card>
      <CardTitle hint="listAdminPayments">支付流水</CardTitle>

      {query.state === 'loading' ? <ListLoading /> : null}

      {query.state === 'error' && query.error !== null ? (
        <QueryErrorState error={query.error} what="支付流水" onRetry={query.reload} />
      ) : null}

      {query.state === 'ready' && query.data !== null ? (
        query.data.data.length === 0 ? (
          <EmptyState
            title="还没有支付流水"
            description="第一笔链上到账或网关回调进来后，这里会有记录。"
            action={
              <LinkButton href="/admin/orders">
                去订单列表 <Icon.ArrowRight size={14} />
              </LinkButton>
            }
          />
        ) : (
          <>
            <DataTable head={['流水', '订单号', '状态', '应付 / 实收', 'txid', '到账时间', '']}>
              {query.data.data.map((row) => (
                <Tr key={row.id}>
                  <Td>
                    <span className="font-mono text-xs text-fg-muted">
                      {row.provider} #{row.id}
                    </span>
                    <span className="mt-0.5 block break-all font-mono text-xs text-fg-subtle">
                      {row.external_id}
                    </span>
                  </Td>
                  <Td>
                    {row.trade_no ? (
                      <Link
                        className="font-mono text-sm text-accent underline-offset-2 hover:underline"
                        to={`/admin/orders/${encodeURIComponent(row.trade_no)}`}
                      >
                        {row.trade_no}
                      </Link>
                    ) : (
                      <span className="text-warn" title="打到我们地址但归属不到订单的钱，是另一个人工队列">
                        未归属
                      </span>
                    )}
                  </Td>
                  <Td className="whitespace-nowrap">
                    <PaymentStateBadge state={String(row.state)} />
                  </Td>
                  <Td className="whitespace-nowrap font-mono text-xs">
                    {usdt6Text(row.expected_usdt6)}
                    <span className="mx-1 text-fg-subtle">/</span>
                    {usdt6Text(row.received_usdt6)}
                  </Td>
                  <Td>
                    {row.txid ? (
                      <span className="font-mono text-xs" title={row.txid}>
                        {row.txid.slice(0, 10)}…
                      </span>
                    ) : (
                      MISSING
                    )}
                  </Td>
                  <Td className="whitespace-nowrap text-fg-muted">{formatDateTime(row.created_at)}</Td>
                  <Td>
                    <Button
                      onClick={() => setEditing((current) => (current?.id === row.id ? null : row))}
                    >
                      {editing?.id === row.id ? '收起' : '改状态'}
                    </Button>
                  </Td>
                </Tr>
              ))}
            </DataTable>
            <Pager meta={query.data.meta} pager={pager} total={total} />
          </>
        )
      ) : null}

      {editing !== null ? (
        <div className="mt-4">
          <PaymentStateEdit
            payment={editing}
            onDone={() => {
              setEditing(null);
              query.reload();
            }}
          />
        </div>
      ) : null}

      <div className="mt-4">
        <ContractGapNotice title="「未归属」是另一个队列，不在这一页">
          <p>
            打到我们地址、但归属不到任何订单的钱（<code className="font-mono">order_id IS NULL</code>）
            会在这张流水表里显示成「未归属」，但它<strong className="font-medium text-fg">不在少付队列里</strong>：
            两者的人工动作完全不同 —— 少付是「联系用户补差价或写销」，未归属是「这笔钱是谁的」。
            契约里目前没有单独的未归属队列端点。
          </p>
        </ContractGapNotice>
      </div>
    </Card>
  );
}

/**
 * D13 · 改支付流水状态。
 *
 * 🔴 **这个操作不推进订单，也不开通任何权益。** 改成 `paid` 之后订单仍然停在原状态，
 * 而操作者会以为「我把它标成 paid 了，用户应该能用了」，然后等一个永远不会发生的开通。
 * 服务端在这条路径上打了一条 ERROR 日志专门盯这件事，界面这一侧必须把话说在前面。
 *
 * ⚠️ **`requireReason` 是显式传的，不是登记表给的。**
 * `lib/danger.ts` 的 D13 行没有 `reason: true`（它誊的是 page-inventory §4.4，
 * 那张表的 D13 只写了「展示 diff」），而契约与服务端都要求 L2 必填原因
 * （`guardAdminReason`，≥ 8 个码位）。不传的话按钮会在没填原因时就亮，
 * 提交后被 422 退回 —— 而那时操作者看到的是一句「服务端退回了这次提交」。
 * 覆盖是**加**不是减（见 `DangerousAction` 的 `stepUpRequired` 同款纪律）。
 * TODO(P1)：把 `reason: true` 补进登记表的 D13 行，然后删掉这个覆盖。
 */
function PaymentStateEdit({ payment, onDone }: { payment: AdminPayment; onDone: () => void }) {
  const before = String(payment.state);
  const [next, setNext] = useState<PaymentState>(payment.state);
  const [note, setNote] = useState('');

  const unchanged = next === payment.state;

  return (
    <DangerousAction
      code="D13"
      title={`改流水 #${payment.id} 的状态`}
      submitLabel="保存新状态"
      requireReason
      /* 不传 permissionName：服务端在这条端点上**没有**权限位守卫（只有 L2 必填原因）。
         编一个名字出来的代价是，将来真的 403 时，界面会指着一个不存在的开关让人去开。 */
      disabled={unchanged}
      disabledReason={
        <>
          新状态与当前状态相同（<code className="font-mono">{before}</code>），没有要改的东西。
          先在下面选一个不同的状态。
        </>
      }
      context={
        <div className="space-y-3">
          <div>
            <p>
              流水 <code className="font-mono">{payment.provider} #{payment.id}</code>
              {payment.trade_no ? (
                <>
                  ，订单 <code className="font-mono">{payment.trade_no}</code>
                </>
              ) : (
                <>，<strong className="font-semibold">未归属到任何订单</strong></>
              )}
              {payment.txid ? (
                <>
                  ，txid <code className="font-mono">{payment.txid}</code>
                </>
              ) : null}
              。
            </p>
            {/* D13 的登记要求是「展示 diff」。这一条能改的只有 state 一个字段，
                所以 diff 就是这一行 —— 但它必须真的显示出来，而不是让人自己记住原值。 */}
            <p className="mt-2 font-mono text-sm">
              state: <span className="text-fg-muted">{before}</span>
              <span className="mx-2">→</span>
              <span className={unchanged ? 'text-fg-muted' : 'font-semibold text-danger'}>{next}</span>
            </p>
          </div>

          <div className="rounded-lg border border-danger/30 bg-danger/5 p-3 text-xs leading-relaxed text-fg-muted">
            <p className="font-medium text-fg">改这一行不会推进订单，也不会开通任何权益。</p>
            <p className="mt-1">
              把它改成 <code className="font-mono">paid</code> 之后，订单仍然停在原来的状态，
              用户那边什么都不会发生。要补单请走
              <strong className="font-medium text-fg"> D6（手工标记订单已支付）</strong>
              ，在订单详情页 —— 而 D6 要求链上 txid，且默认对所有管理员关闭。
            </p>
            <p className="mt-1">
              <code className="font-mono">payments</code> 表上没有 <code className="font-mono">updated_at</code>，
              所以这次改动<strong className="font-medium text-fg">在这一行里不留任何痕迹</strong>；
              审计日志是唯一的记录。
            </p>
          </div>

          <div className="grid gap-3 sm:grid-cols-2">
            <div>
              <label className="mb-1.5 block text-sm font-medium text-fg" htmlFor={`state-${payment.id}`}>
                新状态
              </label>
              <select
                id={`state-${payment.id}`}
                value={next}
                onChange={(event) => setNext(event.target.value as PaymentState)}
                className="min-h-11 w-full rounded-lg border border-line bg-surface px-3 text-base text-fg focus-visible:border-accent focus-visible:outline-2 focus-visible:outline-offset-1 focus-visible:outline-accent"
              >
                {PAYMENT_STATES.map((s) => (
                  <option key={s} value={s}>
                    {PAYMENT_STATE_LABEL[s] ?? s}（{s}）
                  </option>
                ))}
              </select>
            </div>
            <TextField
              label="备注（可选）"
              value={note}
              onChange={setNote}
              hint={
                <>
                  ⚠️ <strong className="font-medium text-fg">备注在库里无处可存</strong> ——
                  <code className="font-mono">payments</code> 没有备注列，它只会进审计日志的
                  <code className="font-mono"> after </code>快照。下一个打开这一页的人看不到它。
                </>
              }
            />
          </div>
        </div>
      }
      onSubmit={async (values) => {
        const trimmedNote = note.trim();
        await updateAdminPayment(payment.id, {
          reason: values.reason ?? '',
          state: next,
          ...(trimmedNote === '' ? {} : { note: trimmedNote }),
        });
      }}
      onDone={onDone}
    />
  );
}

/* ────────────────────────── 通道与地址池 ────────────────────────── */

function ChannelsAndPool() {
  return (
    <Card>
      <CardTitle>通道与地址池</CardTitle>
      <ContractGapNotice title="通道开关与地址池余额不在这个模块的端点上">
        <p>
          page-inventory §4.3 把「通道开关、地址池」列在这一页的「能改」里，
          但契约的支付端点只有三条（少付队列、流水列表、改流水状态）——
          通道开关与地址池是 KV 配置，落在
          <Link className="underline" to="/admin/settings"> 系统配置 </Link>
          那一组端点上，同属 D13。
        </p>
        <p>
          地址池<strong className="font-medium text-fg">余额</strong>与「链上查单」在契约里
          没有任何端点：余额要读链，而本仓的纪律是代码里不出现第三方 endpoint
          （链上查询走服务端注入的 scanner）。现在只能上区块浏览器自己查。
        </p>
      </ContractGapNotice>
    </Card>
  );
}

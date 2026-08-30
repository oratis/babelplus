/**
 * 模块 3 · 订单详情 `/admin/orders/:trade_no` —— P1 / M2。
 *
 * 端点：`getAdminOrder` · `markAdminOrderPaid`（D6）· `refundAdminOrder`（D7）。
 *
 * 页面分成**只读区**与**操作区**两块，中间隔着一条线，顺序不能反：
 * 「先看清楚这张单是什么，再决定要不要动它」是这一页唯一的产品要求。
 *
 * 🔴 **D6「手工标记订单已支付」是全系统最大的内部欺诈面。**
 * 四层强制（api-contract §6.2）全部在服务端：L1 确认串 = 订单所属用户的邮箱、
 * L2 原因 ≥ 8 字、L3 当次 TOTP、L4 独立权限位 `perm_mark_order_paid`。
 * 前端这一侧**只负责把这四样收齐交上去**，一个都不模拟。
 *
 * 🔴 **D6 默认对所有管理员关闭，而且是两道锁。**
 * 第一道是权限位本身（`perm_mark_order_paid` 默认 false，要 DBA 改库才能开）；
 * 第二道是 ADR 0012 §16.3 的带外留痕通道 —— 在它被端到端验证通过之前，
 * 即使有人为了测试把权限位打开了，服务端仍然拒绝。
 * 这两件事会被**不同的人在不同的时间**打开，所以界面上要分开讲：
 * 一个灰按钮说不清「这是没授权」还是「这个功能坏了」。
 *
 * 🔴 **退款一律进不可提现余额**（ADR 0013 §3.1）。金额不是操作者说了算：
 * 服务端按「已消费时间 + 已消费流量」扣减后算出本次可退上限，填的数只能落在
 * `(0, 上限]` 之内。而那份扣减明细在冻结契约下**只有被拒时才拿得到**（422 details）——
 * 见 `order-common.tsx` 的 `refundBreakdown`，缺口登记在那里。
 */
import { useState } from 'react';
import { Link, useParams } from 'react-router';
import { ApiError } from '@babelplus/shared/api';
import { Card, CardTitle, EmptyState, Icon, LinkButton, formatDateTime } from './_imports.ts';
import { DangerousAction } from '../components/DangerousAction.tsx';
import { asApiError } from '../lib/auth.tsx';
import {
  ContractGapNotice,
  DangerOpsNote,
  ListLoading,
  MISSING,
  ModuleHeader,
  OrderStatusBadge,
  QueryErrorState,
  RefundBreakdownTable,
  Row,
  TextField,
  cnyText,
  getAdminOrder,
  knownNonRefundable,
  orderStatusLabel,
  orderTypeLabel,
  parseCents,
  refundAdminOrder,
  refundBreakdown,
  markAdminOrderPaid,
  useApiQuery,
  type AdminOrder,
} from './order-common.tsx';

export default function OrderDetailPage() {
  const { trade_no: tradeNoParam } = useParams();
  const tradeNo = tradeNoParam ?? '';

  const query = useApiQuery(() => getAdminOrder(tradeNo), [tradeNo], '订单详情加载失败');

  return (
    <>
      <ModuleHeader
        title="订单详情"
        description={<code className="font-mono text-fg">{tradeNo || MISSING}</code>}
        priority="P1"
        mobile="M2"
        actions={
          <LinkButton href="/admin/orders">
            回到订单列表 <Icon.ArrowRight size={14} />
          </LinkButton>
        }
      />

      <DangerOpsNote codes={['D6', 'D7']} />

      {query.state === 'loading' ? <ListLoading /> : null}

      {query.state === 'error' && query.error !== null ? (
        query.error.code === 'RESOURCE_NOT_FOUND' ? (
          <EmptyState
            title="找不到这个订单"
            description="订单号可能不对。它是区分大小写的，而且不含空格 —— 从工单里复制时容易多带一个。"
            action={
              <LinkButton tone="primary" href="/admin/orders">
                回到订单列表 <Icon.ArrowRight size={14} />
              </LinkButton>
            }
          />
        ) : (
          <QueryErrorState error={query.error} what="订单详情" onRetry={query.reload} />
        )
      ) : null}

      {query.state === 'ready' && query.data !== null ? (
        <OrderDetail order={query.data} tradeNo={tradeNo} onChanged={query.reload} />
      ) : null}
    </>
  );
}

function OrderDetail({
  order,
  tradeNo,
  onChanged,
}: {
  order: AdminOrder;
  tradeNo: string;
  onChanged: () => void;
}) {
  const o = order.order;
  const status = String(o.status);

  return (
    <div className="space-y-4">
      {/* ───────────────── 只读区 ───────────────── */}
      <Card>
        <CardTitle hint="getAdminOrder">明细</CardTitle>
        <dl className="divide-y divide-line/60">
          <Row label="订单号">
            <code className="font-mono">{o.trade_no}</code>
          </Row>
          <Row label="用户">
            <Link className="text-accent underline-offset-2 hover:underline" to={`/admin/users/${order.user_id}`}>
              {order.user_email}
            </Link>
          </Row>
          <Row label="状态">
            <OrderStatusBadge status={status} />
          </Row>
          <Row label="类型">{orderTypeLabel(o.type)}</Row>
          <Row label="套餐 / 周期">
            {o.plan_name ?? MISSING}
            {o.period ? ` · ${o.period}` : ''}
          </Row>
          <Row label="下单时间">{formatDateTime(o.created_at)}</Row>
          <Row label="订单过期">{formatDateTime(o.expires_at)}</Row>
          <Row label="支付时间">{formatDateTime(o.paid_at)}</Row>
          <Row label="汇率锁定于">{formatDateTime(o.rate_locked_at)}</Row>
        </dl>
        <p className="mt-3 text-xs leading-relaxed text-fg-subtle">
          「订单过期」之后收款地址仍会
          <strong className="font-medium text-fg">继续监听 ≥ 24 小时</strong>
          ，期间到账入余额、不直接开通。所以「已过期」不等于「这笔钱不会来了」。
        </p>
      </Card>

      <Card>
        <CardTitle hint="单位一律是分，整数">金额构成</CardTitle>
        <dl className="divide-y divide-line/60">
          <Row label="原价">{cnyText(o.total_amount)}</Row>
          <Row label="优惠码减免">{cnyText(o.discount_amount)}</Row>
          <Row label="升级折抵">{cnyText(o.surplus_amount)}</Row>
          <Row label="余额抵扣">{cnyText(o.balance_amount)}</Row>
          <Row label="实付">
            <strong className="font-semibold text-fg">{cnyText(o.payable_amount)}</strong>
          </Row>
        </dl>
        <div className="mt-3 space-y-2">
          <ContractGapNotice title="「升级折抵」的算式尚未裁决">
            <p>
              ADR 0013 只定了「升级按剩余天数折抵」这一种主路径，
              <strong className="font-medium text-fg">降档、周期内多次升档、加油包余量三种情形都还没有算式</strong>
              （pricing §3.5.10）。这里显示的是服务端存下来的数，不是这一页算出来的 —— 对不上时去查那张单的来源。
            </p>
          </ContractGapNotice>
          <ContractGapNotice title="没有「优惠码」与「状态机历史」">
            <p>
              契约的 <code className="font-mono">Order</code> 上没有优惠码字段（库里是{' '}
              <code className="font-mono">orders.coupon_id</code>），也没有任何端点能读{' '}
              <code className="font-mono">order_transitions</code>。脚手架里写的「状态机历史」
              因此在这一页做不出来 —— 状态<strong className="font-medium text-fg">变更</strong>的痕迹目前只能去
              <Link className="underline" to="/admin/audit"> 审计日志 </Link>
              里按订单号找。
            </p>
          </ContractGapNotice>
        </div>
      </Card>

      <Card>
        <CardTitle>支付证据</CardTitle>
        <ContractGapNotice title="回调原文与链上 txid 不在这个端点上">
          <p>
            <code className="font-mono">AdminOrder</code> 只有订单本身，没有支付回调原文、
            没有 <code className="font-mono">txid</code>、也没有实收金额。这些在{' '}
            <code className="font-mono">payments</code> 那一侧，而{' '}
            <code className="font-mono">listAdminPayments</code> 没有按订单号过滤的参数 ——
            所以现在只能去 <Link className="underline" to="/admin/payments">支付与对账</Link>{' '}
            翻，或者直接上链查。
          </p>
          <p>
            🔴 <strong className="font-medium text-fg">回调不可信，能查链就不要凭回调下结论。</strong>
            易支付一类网关的回调伪造是有先例的；而「回调声称的金额」与「链上实际到账」
            不一致，恰恰是要查的那件事。加工过的展示会把这两个数混成一个，
            所以这一页宁可什么都不显示，也不显示一个来源不明的「已收金额」。
          </p>
        </ContractGapNotice>
      </Card>

      {/* ───────────────── 操作区 ───────────────── */}
      <div className="border-t border-line pt-4">
        <h2 className="text-base font-semibold text-fg">危险操作</h2>
        <p className="mt-1 text-sm leading-relaxed text-fg-muted">
          下面两条都会<strong className="font-medium text-fg">立刻</strong>动钱或动权益，且都写进不可删除的审计日志。
          先把上面的只读区看完再展开。
        </p>
      </div>

      <MarkPaidAction order={order} tradeNo={tradeNo} onDone={onChanged} />
      <RefundAction order={order} tradeNo={tradeNo} status={status} onDone={onChanged} />
    </div>
  );
}

/* ────────────────────────── D6 · 手工标记已支付 ────────────────────────── */

/**
 * 🔴 D6。四层全开，外加一个**业务必填字段** `evidence_url`。
 *
 * `evidence_url` 不是四层之一，但它的强制性一样硬：ADR 0012 §16.1 要求 D6 必须携带
 * 真实 txid，而冻结的 `MarkPaidRequest` 没有 txid 字段，所以服务端**从这个 URL 里解**。
 * 解不出就 422 —— 因为入账幂等键只能来自链上（`external_id = txid:log_index`），
 * 用录入动作的 id 造一个键根本不幂等：点两次 = 两次入账、两次开通。
 *
 * 前端在这里只做一件事：**空的时候不让提交**（省一次注定失败的往返）。
 * **不做格式校验** —— 服务端认得四种形态（tronscan 的 hash 路由、带 `:log_index`、
 * 带 `?log_index=`、以及裸哈希），前端写一条更严的正则就会挡住服务端本会接受的输入，
 * 而那时操作者只会觉得「这个框坏了」。填了但看着不像哈希时，只**提示**，不拦。
 */
function MarkPaidAction({
  order,
  tradeNo,
  onDone,
}: {
  order: AdminOrder;
  tradeNo: string;
  onDone: () => void;
}) {
  const [evidenceUrl, setEvidenceUrl] = useState('');
  const trimmed = evidenceUrl.trim();
  // 只是「看起来有没有 64 位十六进制」，用于提示。服务端的判据比这条宽，也比这条准。
  const looksLikeTxid = /[0-9a-fA-F]{64}/.test(trimmed);

  return (
    <div>
      <DangerousAction
        code="D6"
        title="手工标记这张订单为已支付"
        submitLabel="标记为已支付"
        confirmation={order.user_email}
        permissionName="admin.order.mark_paid"
        disabled={trimmed === ''}
        disabledReason={
          <>
            还需要一个<strong className="font-medium text-fg">链上交易证据 URL</strong>
            （展开后在下面填）。服务端要从它解出 txid 当作入账幂等键，
            解不出来会直接退回 —— 没有 txid 的手工入账请改走「调整余额」（D10，在用户详情页）。
          </>
        }
        context={
          <div className="space-y-3">
            <div>
              <p>
                这张单：<code className="font-mono">{order.order.trade_no}</code>，实付{' '}
                <strong className="font-semibold">{cnyText(order.order.payable_amount)}</strong>，
                当前状态 <strong className="font-semibold">{orderStatusLabel(String(order.order.status))}</strong>。
              </p>
              <p className="mt-1">
                确认串是<strong className="font-semibold">订单所属用户的邮箱</strong>：
                <code className="font-mono"> {order.user_email}</code>。
                服务端会自己把它查出来再比对，这里显示只是让你不用去别处翻。
              </p>
              {/* 🔴 登记表与契约在这一条上不一致，而不一致的那一半正好显示在输入框的标签上。 */}
              <p className="mt-1 text-warn">
                ⚠️ 下面确认串输入框的标签写的是「订单号」——
                那是 <code className="font-mono">lib/danger.ts</code> 誊 page-inventory §4.4 时留下的旧值。
                <strong className="font-medium text-fg">契约与服务端要的是上面这个邮箱</strong>
                （<code className="font-mono">MarkPaidRequest.confirmation</code>）。以输入框下面的提示为准。
              </p>
            </div>

            <div className="rounded-lg border border-danger/30 bg-danger/5 p-3 text-xs leading-relaxed text-fg-muted">
              <p className="font-medium text-fg">这一条默认对所有管理员关闭，而且是两道锁。</p>
              <p className="mt-1">
                <strong className="font-medium text-fg">锁一：权限位。</strong>{' '}
                <code className="font-mono">perm_mark_order_paid</code> 在库里默认 false，
                <strong className="font-medium text-fg">对每一个管理员都是</strong>，
                而且当前 API 契约里没有对应的枚举值 —— 也就是说
                <strong className="font-medium text-fg">在「管理员账号」页面上找不到这个开关</strong>，
                只能由 DBA 直接改库置位。找不到不是页面漏了。
              </p>
              <p className="mt-1">
                <strong className="font-medium text-fg">锁二：带外留痕通道。</strong>{' '}
                ADR 0012 §16.3 要求 D6 的每一次执行都同步送进一条<strong className="font-medium text-fg">不在我们自己 Postgres 里</strong>的
                记录通道；在它被端到端验证通过之前，服务端一律拒绝 —— 即使权限位被打开了。
                这两把锁会被不同的人在不同的时间打开，所以你可能会先拿到权限、再被第二把锁挡住。
              </p>
            </div>

            <TextField
              label="链上交易证据 URL（必填）"
              value={evidenceUrl}
              onChange={setEvidenceUrl}
              mono
              inputMode="url"
              placeholder="https://tronscan.org/#/transaction/<64 位交易哈希>"
              hint={
                <>
                  服务端从这里解出 <code className="font-mono">txid</code>，用作入账幂等键
                  <code className="font-mono"> txid:log_index</code>。裸哈希也认；
                  一笔交易里我们的转账不是第一个事件时，写成{' '}
                  <code className="font-mono">&lt;txid&gt;:&lt;log_index&gt;</code>{' '}
                  —— 不写会默认按 0 算，而那会与扫链编出不同的键，
                  导致同一笔钱被入账两次。
                  {trimmed !== '' && !looksLikeTxid ? (
                    <span className="mt-1 block text-warn">
                      这串里没看到 64 位十六进制哈希。服务端的判据比这条提示准，
                      所以按钮仍然是亮的 —— 但多半会被退回。
                    </span>
                  ) : null}
                </>
              }
            />
          </div>
        }
        onSubmit={async (values) => {
          // 四层的参数原样交给服务端：confirmation / reason 进 body，totp 进请求头。
          await markAdminOrderPaid(
            tradeNo,
            {
              confirmation: values.confirmation ?? '',
              reason: values.reason ?? '',
              evidence_url: trimmed,
            },
            values.totp ?? '',
          );
          setEvidenceUrl('');
        }}
        onDone={onDone}
      />
    </div>
  );
}

/* ────────────────────────── D7 · 退款 ────────────────────────── */

/**
 * D7。L2 必填原因 + 📧 通知受影响用户（通知由服务端发，不是这里的一个复选框）。
 *
 * 🔴 **退款一律进不可提现余额，且金额由服务端算。**
 * 前端能填的只有一个可选的 `amount`（分），且它只能**小于等于**服务端算出来的上限。
 * 留空 = 按上限全额退。把它做成一个自由填写的金额框而不说明这一点，
 * 会让人以为 D7 是一个转账按钮。
 *
 * 扣减明细的唯一出口是 422 的 `details`，所以这里把提交失败的 `ApiError` 留了一份下来，
 * 好把明细渲染成一张**表**（要求是「把扣减明细算给操作者看，不是只给一个总数」）。
 * 组件自己也会显示一条表单级错误 —— 两者不重复：那一条说的是「这次为什么没成」，
 * 这张表说的是「按什么算式算出来的」。
 */
function RefundAction({
  order,
  tradeNo,
  status,
  onDone,
}: {
  order: AdminOrder;
  tradeNo: string;
  status: string;
  onDone: () => void;
}) {
  const [amountText, setAmountText] = useState('');
  const [lastError, setLastError] = useState<ApiError | null>(null);

  const amount = parseCents(amountText);
  const amountInvalid = amountText.trim() !== '' && amount === null;
  // 只对**认得出来**的不可退状态变灰。服务端将来加一个可退状态时，
  // 不认识的值要放行到服务端去判 —— 否则现象是「后台永远退不了这类单」。
  const blockedByStatus = knownNonRefundable(status);
  const breakdown = refundBreakdown(lastError);

  return (
    <div>
      <DangerousAction
        code="D7"
        title="退款 / 作废这张订单"
        submitLabel="提交退款"
        permissionName="admin_users.perm_refund"
        disabled={blockedByStatus || amountInvalid}
        disabledReason={
          blockedByStatus ? (
            <>
              这张单当前是<strong className="font-medium text-fg">{orderStatusLabel(status)}</strong>，
              没有可退的款项。服务端只接受 已支付 / 已完成 / 部分退款 三种状态。
            </>
          ) : (
            <>退款金额只能填<strong className="font-medium text-fg">正整数的「分」</strong>，或者留空表示全额。</>
          )
        }
        context={
          <div className="space-y-3">
            <div>
              <p>
                这张单：<code className="font-mono">{order.order.trade_no}</code>，实付{' '}
                <strong className="font-semibold">{cnyText(order.order.payable_amount)}</strong>，
                用户 <code className="font-mono">{order.user_email}</code>，
                当前状态 <strong className="font-semibold">{orderStatusLabel(status)}</strong>。
              </p>
            </div>

            <div className="rounded-lg border border-line bg-surface p-3 text-xs leading-relaxed text-fg-muted">
              <p className="font-medium text-fg">退款去哪、退多少，都不是这一页决定的。</p>
              <ul className="mt-1 list-disc space-y-1 pl-4">
                <li>
                  <strong className="font-medium text-fg">一律进不可提现余额</strong>（ADR 0013 §3.1）。
                  没有现金流出，用户拿到的是只能消费的余额。
                </li>
                <li>
                  金额按<strong className="font-medium text-fg">「已消费时间 + 已消费流量」扣减</strong>后算出
                  本次可退上限（§3.2 的窗口链），并已减去此前退到余额的部分。
                  你填的数只能落在 <code className="font-mono">(0, 上限]</code> 之内，留空即按上限退。
                </li>
                <li>
                  <strong className="font-medium text-fg">冷静期全额退款一个账号一生只有一次</strong>，
                  由数据库唯一索引强制。重复退会被退回 409，且那时<strong className="font-medium text-fg">还没有任何钱被动过</strong>
                  （服务端先插退款记录再动钱，就是为了这一点）。
                </li>
                <li>
                  退款会<strong className="font-medium text-fg">立即终止订阅</strong>并按比例
                  <strong className="font-medium text-fg">追回邀请人佣金</strong>（不论佣金是什么状态）。
                  加油包配额不动。
                </li>
                <li>
                  服务端把这一条挂在<strong className="font-medium text-fg">独立权限位</strong>
                  <code className="font-mono"> admin_users.perm_refund </code>上
                  （page-inventory §4.4 的 D7 行没有这一列，服务端加严了）。
                  而这个位在当前 API 契约的权限枚举里
                  <strong className="font-medium text-fg">没有对应值</strong> ——
                  在「管理员账号」页面上找不到它，只能由 DBA 直接置位。找不到不是页面漏了。
                </li>
              </ul>
            </div>

            <TextField
              label="退款金额（分，可留空）"
              value={amountText}
              onChange={setAmountText}
              mono
              inputMode="numeric"
              placeholder="留空 = 按服务端算出的上限全额退"
              hint={
                <>
                  只接受<strong className="font-medium text-fg">正整数的「分」</strong>，
                  不接受小数点 —— 「12.5」这种输入的正确处置是拒绝，不是猜成 12 分或 1250 分。
                  {amount !== null ? (
                    <span className="ml-1 text-fg">当前 = {cnyText(amount)}。</span>
                  ) : null}
                  {amountInvalid ? (
                    <span className="mt-1 block text-danger">这不是一个合法的正整数分值。</span>
                  ) : null}
                </>
              }
            />
          </div>
        }
        onSubmit={async (values) => {
          setLastError(null);
          try {
            await refundAdminOrder(tradeNo, {
              reason: values.reason ?? '',
              ...(amount !== null ? { amount } : {}),
            });
            setAmountText('');
          } catch (cause) {
            // 留一份下来渲染扣减明细，然后**原样抛回去** —— 表单级错误由组件显示，
            // 吞掉的话按钮会看起来像成功了。
            const error = asApiError(cause, '退款没能完成');
            setLastError(error);
            throw error;
          }
        }}
        onDone={onDone}
      />

      <RefundBreakdownTable rows={breakdown} />

      {lastError !== null && breakdown.length === 0 ? (
        <p className="mt-2 text-xs leading-relaxed text-fg-subtle">
          这次退回没有带扣减明细。明细只在服务端<strong className="font-medium text-fg">算完之后</strong>被拒时才有
          （比如金额超过上限、或算出来已经没有可退额）；卡在权限位或必填原因上时不会有。
        </p>
      ) : null}
    </div>
  );
}

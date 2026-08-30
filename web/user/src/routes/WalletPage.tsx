/**
 * `/wallet` —— P2。page-inventory §3.1 #14、§3.2.9。从竞品的 profile 里拆出来。
 *
 * 🔴 「余额仅可消费，不可提现」是**资金合规底线**（product-brief §6），
 * 必须**常驻**而不是藏在条款里。放在页面顶部，不折叠。
 *
 * 🔴 **三个金额分开显示，一个都不许合并成「总余额」。**
 * `getWallet` 返回三个数：`balance_amount`（余额）、`commission_pending_amount`
 * （确认中的佣金）、`commission_available_amount`（可划转的佣金）。
 * 把它们加起来显示成一个数字是这一页最容易犯、也最贵的错：
 * 确认中的佣金**还不是你的钱**（退款冷静期内可能被作废），
 * 而可划转的佣金**还没进余额**（要点一次划转）。合成一个数之后，
 * 「我明明有 ¥50 为什么下单时只抵了 ¥20」就成了必然发生的工单。
 * WalletPage.test.tsx 钉死了这一条：断言合计值的字符串**不出现在页面上**。
 *
 * 🔴 **「可提现」这一栏的值是一个字面量 0，不是从响应里读的。**
 * `handler/wallet.go` 的 `walletView` 逐字写着：`balance_amount` 取的是
 * `non_withdrawable_amount`，可提现余额恒为 0 且**不加进来**；
 * 契约的 `Wallet` 只有一个 `balance_amount` 字段，装不下两个数，而 openapi 已冻结。
 * 也就是说：**响应里的 `balance_amount` 本身就是「不可提现余额」**，
 * 页面要做的是把这个含义显示出来（ADR 0013 ① 裁决「退款一律退到不可提现的钱包余额」，
 * 那条裁决要防的误解正是「退款进余额 ⇒ 那部分应该能提出来」）。
 * ⚠️ 后端有一条 `walletAnomalies` 告警守着「哪天 withdrawable ≠ 0 却没人回来改前端」，
 * 但那是日志，用户看不到 —— 所以这一页的写法必须与后端的裁决**逐字对齐**，
 * 交付说明里也登记了这处契约限制。
 *
 * 金额单位是「分」的 int64，展示一律走 `formatCny`（整数除模），**不许用浮点**（api-contract §2.6）。
 *
 * 余额与流水是**两个独立请求**，各持一套三态：流水翻不出来不该让余额消失
 * （用户来这一页多半就是想看一眼余额还剩多少）。
 */
import { useState } from 'react';
import {
  Badge,
  Button,
  Card,
  CardTitle,
  EmptyState,
  Icon,
  LinkButton,
  cx,
  formatCny,
  formatDateTime,
} from './_imports.ts';
import { unwrapWithMeta, type ApiError, type Meta } from '@babelplus/shared/api';
import { api } from '../lib/api.ts';
import { asApiError, useApiQuery } from './ticket-common.tsx';
import {
  MoneyRow,
  NO_WITHDRAW_NOTICE,
  QuerySection,
  WriteError,
  commonWriteErrorCopy,
  fallbackWriteErrorCopy,
  type Wallet,
  type WalletTransaction,
} from './account-common.tsx';

/** 一页多少条流水。不做无限滚动 —— 「加载更多」是可停下来的，滚动不是。 */
const PAGE_SIZE = 20;

interface TransactionPage {
  readonly items: readonly WalletTransaction[];
  readonly meta: Meta;
}

function loadWallet(): Promise<Wallet> {
  return unwrapWithMeta(api().GET('/api/v1/user/wallet')).then((envelope) => envelope.data);
}

function loadTransactions(cursor: string | null): Promise<TransactionPage> {
  const query = cursor === null ? { limit: PAGE_SIZE } : { limit: PAGE_SIZE, cursor };
  return unwrapWithMeta(api().GET('/api/v1/user/wallet/transactions', { params: { query } })).then(
    (envelope) => ({ items: envelope.data, meta: envelope.meta }),
  );
}

const loadFirstPage = (): Promise<TransactionPage> => loadTransactions(null);

export default function WalletPage() {
  const wallet = useApiQuery(loadWallet, [], '余额加载失败');
  const transactions = useApiQuery(loadFirstPage, [], '流水加载失败');

  return (
    <>
      <header className="mb-5 sm:mb-6">
        <h1 className="text-xl font-semibold tracking-tight text-fg sm:text-2xl">钱包</h1>
        <p className="mt-1 max-w-2xl text-sm text-fg-muted">余额和流水。</p>
      </header>

      <div className="space-y-4">
        {/* 常驻声明，不折叠、不藏。 */}
        <Card>
          <p className="text-base font-medium text-fg">余额只能用于消费，不能提现。</p>
          <p className="mt-1.5 text-sm leading-relaxed text-fg-muted">{NO_WITHDRAW_NOTICE}</p>
        </Card>

        <Card>
          <CardTitle hint="三个数分开算，不是一个总额">余额</CardTitle>
          <QuerySection query={wallet} what="余额">
            {(data) => <WalletAmounts wallet={data} />}
          </QuerySection>
        </Card>

        <Card>
          <CardTitle hint="每一笔都是一条账本分录的投影">流水</CardTitle>
          <QuerySection query={transactions} what="流水">
            {(first) => <TransactionList first={first} />}
          </QuerySection>
        </Card>
      </div>
    </>
  );
}

/* ─────────────────────────── 余额 ─────────────────────────── */

/**
 * 🔴 四行，**四个独立的数**，中间没有任何加法。
 *
 * 顺序是有意的：先说「能花的」（用户最关心），再说「不能提的」（合规底线，
 * 且它和上一行是同一笔钱的两种说法 —— 所以必须挨着），
 * 最后是两段式佣金（它们还不在余额里）。
 */
function WalletAmounts({ wallet }: { wallet: Wallet }) {
  return (
    <>
      <dl>
        <MoneyRow
          label="可用于消费的余额"
          cents={wallet.balance_amount}
          hint="下单时可以直接抵扣。买套餐、买流量包都能用。"
          emphasis
        />
        {/* 🔴 字面量 0，不是从响应里读的 —— 见文件头。写成一个常量而不是省略这一行：
            省略它，「哪一部分能提」这个问题就再也没有地方可以问了，
            而那正是 ADR 0013 那条裁决要防的误解本身。 */}
        <MoneyRow
          label="可提现余额"
          cents={0}
          hint="恒为 0。系统里没有提现这个动作，上面那笔余额（含退款退回的部分）全部只能消费。"
        />
        <MoneyRow
          label="确认中的佣金"
          cents={wallet.commission_pending_amount}
          hint="还在退款冷静期内，暂时不能划转。冷静期过后会变成「可划转」。"
        />
        <MoneyRow
          label="可划转的佣金"
          cents={wallet.commission_available_amount}
          hint="还没进余额。要在邀请页点一次「划转到余额」才会变成上面那笔可消费余额。"
        />
      </dl>

      <div className="mt-4 flex flex-wrap items-center gap-2">
        <LinkButton href="/invite">
          <Icon.Gift size={14} /> 去邀请页划转佣金
        </LinkButton>
        <LinkButton href="/plan">
          <Icon.Package size={14} /> 用余额买套餐
        </LinkButton>
      </div>

      {/* 明确写出「为什么不给一个总数」。不写的话，下一个改这一页的人
          很可能会好心地加一行「合计」，而那一行会把上面四行的区别全部抹平。 */}
      <p className="mt-3 text-xs leading-relaxed text-fg-subtle">
        这四个数<strong className="font-medium text-fg">不相加</strong>：确认中的佣金可能因退款被作废，
        可划转的佣金要手动划转之后才进余额。给一个「总额」会让你按一个还不存在的数字去下单。
      </p>
    </>
  );
}

/* ─────────────────────────── 流水 ─────────────────────────── */

const TX_META: Record<
  WalletTransaction['type'],
  { label: string; tone: 'neutral' | 'ok' | 'warn' | 'info'; note?: string }
> = {
  recharge: { label: '充值', tone: 'ok' },
  consume: { label: '消费', tone: 'neutral' },
  // ADR 0013 ①：退款**一律**退到不可提现的余额。这句话挂在退款那一行上，
  // 因为「我的退款去哪了」正是这一行会被追问的问题。
  refund: { label: '退款入账', tone: 'info', note: '退款一律退到余额，只能用于消费。' },
  commission_transfer: { label: '佣金划入', tone: 'ok' },
  admin_adjust: { label: '人工调整', tone: 'warn' },
  expired_order_credit: {
    label: '过期订单入账',
    tone: 'info',
    // user-journey §5 的兜底：倒计时归零后才到账的链上转账，钱入余额而不是直接开通。
    note: '订单超时后才到账的付款，钱记到了余额里，没有丢。',
  },
};

function TransactionList({ first }: { first: TransactionPage }) {
  // 「加载更多」拿到的后续页**单独存**，不塞回第一页的 query 里 ——
  // 重试第一页时这些应该一起作废，而它们确实会随 query 重建而清掉。
  const [more, setMore] = useState<readonly WalletTransaction[]>([]);
  const [moreMeta, setMoreMeta] = useState<Meta | null>(null);
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<ApiError | null>(null);

  const items = [...first.items, ...more];
  const meta = moreMeta ?? first.meta;

  if (items.length === 0) {
    return (
      <EmptyState
        title="还没有余额记录"
        description="邀请返佣划转、订单折抵、退款入账都会在这里留下流水。"
        action={
          <LinkButton tone="primary" href="/invite">
            看看邀请返佣 <Icon.ArrowRight size={14} />
          </LinkButton>
        }
      />
    );
  }

  async function loadMore(): Promise<void> {
    if (pending) return;
    const cursor = meta.next_cursor;
    if (!cursor) return;
    setPending(true);
    setError(null);
    try {
      const page = await loadTransactions(cursor);
      setMore((prev) => [...prev, ...page.items]);
      setMoreMeta(page.meta);
    } catch (cause) {
      setError(asApiError(cause, '没能加载更多'));
    } finally {
      setPending(false);
    }
  }

  const moreCopy = error
    ? (commonWriteErrorCopy(error) ?? fallbackWriteErrorCopy(error, '没能加载更多'))
    : null;

  return (
    <>
      {/* 4 列，<768px 卡片化（一列堆叠）—— sm 以下 `grid-cols-1`，sm 起 4 列。 */}
      <div
        className="hidden grid-cols-4 gap-x-3 border-b border-line pb-2 text-xs font-medium text-fg-muted sm:grid"
        aria-hidden="true"
      >
        <span>类型</span>
        <span className="text-right">金额</span>
        <span className="text-right">变动后余额</span>
        <span className="text-right">时间</span>
      </div>

      <ul className="divide-y divide-line">
        {items.map((tx) => (
          <TransactionRow key={tx.id} tx={tx} />
        ))}
      </ul>

      {moreCopy ? (
        <div className="mt-3">
          {/* 分页失败**不清空已经加载出来的部分** —— 用户已经在看的东西不该因为下一页失败而消失。 */}
          <WriteError title={moreCopy.title} description={moreCopy.description} />
        </div>
      ) : null}

      {meta.has_more && meta.next_cursor ? (
        <div className="mt-3">
          <Button onClick={() => void loadMore()} disabled={pending}>
            {pending ? '正在加载…' : '加载更多'}
          </Button>
        </div>
      ) : null}
    </>
  );
}

function TransactionRow({ tx }: { tx: WalletTransaction }) {
  const meta = TX_META[tx.type] ?? { label: tx.type, tone: 'neutral' as const };
  // 方向按**符号**判，不按类型判：`handler/wallet.go` 在 `ref_type` 映射不确定时
  // 会「按符号给方向」并记一条 WARN —— 也就是说类型标签可能不精确，但符号一定对。
  const incoming = tx.amount > 0;

  return (
    <li className="grid gap-1 py-3 text-sm sm:grid-cols-4 sm:items-baseline sm:gap-x-3">
      <span className="flex flex-wrap items-center gap-2">
        <Badge tone={meta.tone}>{meta.label}</Badge>
        {tx.note ? <span className="text-xs text-fg-muted">{tx.note}</span> : null}
      </span>
      <span
        className={cx(
          'font-mono tabular-nums sm:text-right',
          incoming ? 'text-ok' : 'text-fg',
        )}
      >
        {incoming ? '+' : ''}
        {formatCny(tx.amount)}
      </span>
      <span className="font-mono tabular-nums text-fg-muted sm:text-right">
        {formatCny(tx.balance_after)}
      </span>
      <span className="text-xs text-fg-subtle sm:text-right">{formatDateTime(tx.created_at)}</span>
      {meta.note ? (
        <span className="text-xs leading-relaxed text-fg-subtle sm:col-span-4">{meta.note}</span>
      ) : null}
    </li>
  );
}

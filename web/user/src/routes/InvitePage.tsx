/**
 * `/invite` —— P2。page-inventory §3.1 #15、§3.2.9。
 *
 * 两段式佣金（`确认中` / `已获得`）照抄竞品 —— 退款冷静期防套利，这个设计是对的。
 *
 * 三条约束来自 user-journey §3.1：
 *  - 用户码**恒为一次性**（多次可用的码贴到论坛就等于开放注册）——
 *    后端把它写死在 SQL 里（`max_uses = 1`），前端只负责说清楚
 *  - 生成资格挂在「**有有效订阅**」上，不是「有账号」（否则邀请制退化成链式开放注册）
 *  - 每用户同时持有的未核销码上限 3 个（后端 `inviteCodeQuota = 3`，闸门在 INSERT 的 WHERE 里）
 *
 * 🔴 **返佣是一次性定额，不是按订单金额的 10%**（定价修订 C6，已落进 pricing §5）。
 * 文案口径集中在 `account-common.tsx` 的 `COMMISSION_TIERS` / `COMMISSION_RULE_TEXT`，
 * 这一页只引用、不复述。写成「订单金额的 10%」不会有任何报错 ——
 * 它只会让用户按错误的预期算收益，然后在年付订单上发现少了一大截。
 * InvitePage.test.tsx 钉死了这一条（正面断言定额、反面断言不出现比例说法）。
 *
 * 🔴 **`transferCommission` 的 503 必须与 500 分开说。**
 * 缺 `expense:commission` 科目时后端返 **503 + `INTERNAL_DEPENDENCY_DOWN` + `Retry-After`**
 * （`handler/wallet.go` 里那个刻意的契约偏差：openapi 只声明了 401/409/422/500）。
 * 500 是「偶发故障，值得重试」，503 是「这个功能现在整个不可用」——
 * 把 503 说成 500，用户会对着一个**永远不会成功**的按钮反复点。
 *
 * 四个区块各持一套三态：邀请码列表、佣金合计（走 `getWallet`）、佣金明细、生成表单。
 * 佣金合计**不从明细列表累加**：明细是分页的，加第一页得到的是一个偏小的数，
 * 而这个数会被用户拿去和钱包页对账。合计的唯一权威来源是 `getWallet`
 * （它在 SQL 里按 `status` 聚合了全部行）。
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
  formatCny,
  formatDateTime,
} from './_imports.ts';
import { unwrap, unwrapWithMeta, type ApiError, type Meta } from '@babelplus/shared/api';
import { useAuth } from '../lib/auth.tsx';
import { api } from '../lib/api.ts';
import { asApiError, useApiQuery, useRetryCountdown, type ApiQuery } from './ticket-common.tsx';
import {
  COMMISSION_RULE_TEXT,
  COMMISSION_TIERS,
  ConfirmAction,
  MoneyRow,
  NO_WITHDRAW_NOTICE,
  QuerySection,
  WriteError,
  WriteOk,
  commonWriteErrorCopy,
  fallbackWriteErrorCopy,
  fieldReasons,
  hasActiveSubscription,
  isNotImplemented,
  type Commission,
  type InviteCode,
  type Wallet,
} from './account-common.tsx';

/** 后端的 `inviteCodeQuota`。前端只用它写文案，**闸门在服务端**（INSERT 的 WHERE 里）。 */
const UNUSED_CODE_QUOTA = 3;

/** 佣金明细一页多少条。 */
const COMMISSION_PAGE_SIZE = 20;

const loadCodes = (): Promise<InviteCode[]> => unwrap(api().GET('/api/v1/user/invite/codes'));
const loadWallet = (): Promise<Wallet> => unwrap(api().GET('/api/v1/user/wallet'));

interface CommissionPage {
  readonly items: readonly Commission[];
  readonly meta: Meta;
}

function loadCommissions(cursor: string | null): Promise<CommissionPage> {
  const query = cursor === null ? { limit: COMMISSION_PAGE_SIZE } : { limit: COMMISSION_PAGE_SIZE, cursor };
  return unwrapWithMeta(api().GET('/api/v1/user/commissions', { params: { query } })).then(
    (envelope) => ({ items: envelope.data, meta: envelope.meta }),
  );
}

const loadFirstCommissionPage = (): Promise<CommissionPage> => loadCommissions(null);

export default function InvitePage() {
  const codes = useApiQuery(loadCodes, [], '邀请码加载失败');
  const wallet = useApiQuery(loadWallet, [], '佣金合计加载失败');
  const commissions = useApiQuery(loadFirstCommissionPage, [], '佣金记录加载失败');

  return (
    <>
      <header className="mb-5 sm:mb-6">
        <h1 className="text-xl font-semibold tracking-tight text-fg sm:text-2xl">邀请</h1>
        <p className="mt-1 max-w-2xl text-sm text-fg-muted">
          生成邀请码，被邀请人消费后你拿佣金。
        </p>
      </header>

      <div className="space-y-4">
        <CommissionRuleCard />

        <Card>
          <CardTitle hint={`同时最多持有 ${UNUSED_CODE_QUOTA} 个未核销的码`}>我的邀请码</CardTitle>
          <QuerySection query={codes} what="邀请码">
            {(list) => <InviteCodeSection codes={codes} list={list} />}
          </QuerySection>
        </Card>

        <Card>
          <CardTitle hint="两段式：确认中 / 可划转">佣金</CardTitle>
          <QuerySection query={wallet} what="佣金合计">
            {(data) => <CommissionBalance wallet={wallet} data={data} onTransferred={commissions.reload} />}
          </QuerySection>
        </Card>

        <Card>
          <CardTitle hint="每位被邀请人只发一次">佣金明细</CardTitle>
          <QuerySection query={commissions} what="佣金记录">
            {(first) => <CommissionList first={first} />}
          </QuerySection>
        </Card>
      </div>
    </>
  );
}

/* ─────────────────────────── 返佣口径 ─────────────────────────── */

/**
 * 🔴 返佣口径的唯一展示位。**不许改写成「订单金额的 10%」。**
 *
 * 原口径（按订单金额 10%）把 24 格里的 4 格打穿 1.20× 毛利地板（最差 1.1474×），
 * C6 因此把它改成一次性定额。三个数字与后端的计提逐字一致（720 / 1590 / 3580 分）。
 */
function CommissionRuleCard() {
  return (
    <Card>
      <CardTitle hint="定价修订 C6">佣金怎么算</CardTitle>
      <p className="text-sm leading-relaxed text-fg">{COMMISSION_RULE_TEXT}</p>
      <ul className="mt-3 flex flex-wrap gap-2">
        {COMMISSION_TIERS.map((tier) => (
          <li
            key={tier.plan}
            className="flex items-baseline gap-2 rounded-lg border border-line bg-surface-alt px-3 py-1.5 text-sm"
          >
            <span className="text-fg-muted">{tier.plan}</span>
            <span className="font-mono tabular-nums font-medium text-fg">{formatCny(tier.cents)}</span>
          </li>
        ))}
      </ul>
      <p className="mt-3 text-xs leading-relaxed text-fg-subtle">
        举例：被邀请人买了标准档的<strong className="font-medium text-fg">年付</strong>，
        你拿到的仍然是 {formatCny(1590)}（标准档月付标价的 10%），
        不会因为他买得久而变多。{NO_WITHDRAW_NOTICE}
      </p>
    </Card>
  );
}

/* ─────────────────────────── 邀请码 ─────────────────────────── */

const CODE_STATUS: Record<InviteCode['status'], { label: string; tone: 'ok' | 'neutral' | 'warn' }> = {
  ok: { label: '未使用', tone: 'ok' },
  // 「已核销」而不是「已用尽」：用户码恒为一次性，用尽 = 有人用它注册了。
  exhausted: { label: '已核销', tone: 'neutral' },
  // `disabled` 在后端同时覆盖「已吊销」与「已过期」两种情况（`inviteCodeView`），
  // 前端分不出来，所以说一个对两种情况都成立的词。
  disabled: { label: '已失效', tone: 'warn' },
};

function InviteCodeSection({ codes, list }: { codes: ApiQuery<InviteCode[]>; list: InviteCode[] }) {
  const { user } = useAuth();
  const eligible = hasActiveSubscription(user);
  const unusedCount = list.filter((c) => c.status === 'ok').length;

  const [pending, setPending] = useState(false);
  const [error, setError] = useState<ApiError | null>(null);
  const [created, setCreated] = useState<InviteCode | null>(null);
  const countdown = useRetryCountdown();

  async function generate(): Promise<void> {
    // 单飞。**这就是这个写端点当前全部的「幂等」** —— api-contract §9.1 的幂等总表里
    // 没有 `POST /api/v1/user/invite/codes`，服务端不认 `Idempotency-Key`
    // （生成类型里这个 operation 的 `header` 是 `never`）。
    // 「超时后重发多生成一个码」这个缺口留在交付说明里，不假装它不存在 ——
    // 好在它的后果有界：名额上限 3 由服务端在 INSERT 的 WHERE 里守着。
    if (pending || countdown.seconds !== null) return;
    setPending(true);
    setError(null);
    try {
      const code = await unwrap(api().POST('/api/v1/user/invite/codes'));
      setCreated(code);
      // 列表就地补一条，**不把整块打回 loading** ——
      // 用户刚点完生成，眼前的列表突然变成骨架屏会让人以为没生成成功。
      codes.patch((prev) => [code, ...prev]);
    } catch (cause) {
      const apiError = asApiError(cause, '生成失败');
      setError(apiError);
      countdown.start(apiError.retryAfterSeconds);
    } finally {
      setPending(false);
    }
  }

  const copy = error ? inviteCreateErrorCopy(error, countdown.seconds) : null;

  return (
    <>
      {list.length === 0 ? (
        <EmptyState
          title="还没有邀请码"
          description="每个码只能用一次。被邀请人下单后，你会拿到一笔一次性佣金。"
          action={
            <GenerateButton
              eligible={eligible}
              unusedCount={unusedCount}
              pending={pending}
              blocked={countdown.seconds !== null}
              onGenerate={() => void generate()}
            />
          }
          secondary="需要有效订阅才能生成 —— 这是邀请制不退化成开放注册的那道闸。"
        />
      ) : (
        <>
          <ul className="divide-y divide-line">
            {list.map((code) => (
              <InviteCodeRow key={code.id} code={code} />
            ))}
          </ul>
          <div className="mt-4">
            <GenerateButton
              eligible={eligible}
              unusedCount={unusedCount}
              pending={pending}
              blocked={countdown.seconds !== null}
              onGenerate={() => void generate()}
            />
          </div>
        </>
      )}

      {created ? (
        <div className="mt-3">
          <WriteOk>
            已生成 <span className="font-mono">{created.code}</span>。
            这个码<strong className="font-medium">只能用一次</strong>，发给一个人就好。
          </WriteOk>
        </div>
      ) : null}

      {copy ? (
        <div className="mt-3">
          {copy.pending ? (
            <div className="rounded-lg border border-dashed border-line bg-surface-alt/60 px-3 py-2 text-sm leading-relaxed text-fg-muted">
              <span className="font-medium text-fg">{copy.title}</span> {copy.description}
            </div>
          ) : (
            <WriteError title={copy.title} description={copy.description} />
          )}
        </div>
      ) : null}

      {countdown.seconds !== null ? (
        <p className="mt-2 text-sm text-fg-muted">{countdown.seconds} 秒后可再试</p>
      ) : null}
    </>
  );
}

/**
 * 生成按钮。**可用性挂在「当前有有效订阅」上，不是「已登录」**（user-journey §3.1）。
 *
 * 不可用时按钮**不消失**，而是禁用 + 说明原因并给出下一步（去买套餐）——
 * 一个消失的按钮让用户以为功能不存在，一个禁用的按钮告诉他差什么。
 *
 * ⚠️ 这道闸只在前端，后端 `CreateInviteCode` 不校验订阅（只校验名额）。见 `hasActiveSubscription`。
 */
function GenerateButton({
  eligible,
  unusedCount,
  pending,
  blocked,
  onGenerate,
}: {
  eligible: boolean;
  unusedCount: number;
  pending: boolean;
  blocked: boolean;
  onGenerate: () => void;
}) {
  if (!eligible) {
    return (
      <div className="flex flex-col items-center gap-2 sm:flex-row">
        <Button tone="primary" disabled title="需要有效订阅">
          生成邀请码
        </Button>
        <span className="text-xs leading-relaxed text-fg-muted">
          需要有一份生效中的订阅才能生成 —— 否则邀请制会退化成人人可发的开放注册。
        </span>
        <LinkButton href="/plan">
          去看套餐 <Icon.ArrowRight size={14} />
        </LinkButton>
      </div>
    );
  }

  const quotaFull = unusedCount >= UNUSED_CODE_QUOTA;
  if (quotaFull) {
    return (
      <div className="flex flex-col items-start gap-2 sm:flex-row sm:items-center">
        <Button tone="primary" disabled title="未核销的码已达上限">
          生成邀请码
        </Button>
        <span className="text-xs leading-relaxed text-fg-muted">
          你还有 {unusedCount} 个没被用掉的码，同时最多持有 {UNUSED_CODE_QUOTA} 个。
          等其中一个被用掉之后就能再生成。
        </span>
      </div>
    );
  }

  // 🔴 二次确认。生成本身不花钱，但**码发出去就收不回来**（契约里没有吊销端点），
  // 且它占用 3 个名额里的一个。所以给一次「确认」而不是直接发请求。
  return (
    <ConfirmAction
      label="生成邀请码"
      confirmLabel="确认生成一个"
      pending={pending}
      disabled={blocked}
      question={
        <>
          生成后<strong className="font-medium">无法撤销</strong>，
          而且它只能用一次 —— 发给一个人就用掉了。当前还能再生成{' '}
          {UNUSED_CODE_QUOTA - unusedCount} 个。
        </>
      }
      onConfirm={onGenerate}
    />
  );
}

function InviteCodeRow({ code }: { code: InviteCode }) {
  const meta = CODE_STATUS[code.status] ?? { label: code.status, tone: 'neutral' as const };
  // `invite_url` 只对还能用的码有值（后端 `inviteCodeView`：给一条已失效的码配可点链接，
  // 用户会把它发出去，然后对方在注册页拿到「邀请码无效」）。缺 `siteUrl` 时也不自己拼。
  const shareUrl = code.invite_url;

  return (
    <li className="grid gap-1.5 py-3 text-sm sm:grid-cols-[1fr_auto_auto] sm:items-center sm:gap-x-3">
      <span className="flex flex-wrap items-center gap-2">
        <code className="font-mono text-base text-fg">{code.code}</code>
        <Badge tone={meta.tone}>{meta.label}</Badge>
      </span>
      <span className="text-xs text-fg-subtle">{formatDateTime(code.created_at)}</span>
      <span className="flex flex-wrap gap-2">
        <CopyButton value={code.code} label="复制码" />
        {shareUrl ? <CopyButton value={shareUrl} label="复制邀请链接" /> : null}
      </span>
      {shareUrl ? (
        <span className="break-all font-mono text-xs text-fg-subtle sm:col-span-3">{shareUrl}</span>
      ) : code.status === 'ok' ? (
        <span className="text-xs text-fg-subtle sm:col-span-3">
          服务端没有给出邀请链接（`invite_base_url` 未配置），把上面的码直接发给对方即可。
        </span>
      ) : null}
    </li>
  );
}

/**
 * 复制按钮。
 *
 * `navigator.clipboard` 在非安全上下文（http）与部分国产浏览器里**不存在** ——
 * 直接调用会抛 TypeError 并让整个按钮看起来「点了没反应」。
 * 所以失败时退回到「已选中，请手动复制」而不是静默失败。
 */
function CopyButton({ value, label }: { value: string; label: string }) {
  const [copied, setCopied] = useState<'idle' | 'ok' | 'manual'>('idle');

  async function copy(): Promise<void> {
    try {
      await navigator.clipboard.writeText(value);
      setCopied('ok');
    } catch {
      setCopied('manual');
    }
  }

  return (
    <span className="inline-flex items-center gap-1.5">
      <Button className="min-h-9 px-3 text-xs" onClick={() => void copy()}>
        <Icon.Copy size={13} /> {label}
      </Button>
      {copied === 'ok' ? <span className="text-xs text-ok">已复制</span> : null}
      {copied === 'manual' ? (
        <span className="text-xs text-warn">这个浏览器不让自动复制，请手动选中上面的文字</span>
      ) : null}
    </span>
  );
}

/**
 * 生成邀请码的 `ErrorCode` → 文案。
 *
 * ⚠️ **`QUOTA_RATE_LIMITED` 在这个端点上有两个含义**，必须分开：
 *  - 403 + 这个 code = **名额用完**（后端 `CreateInviteCode` 拿它当「未核销码 ≥ 3」的错误码）；
 *  - 429 + 这个 code = 限流。
 * 判据取 **`Retry-After` 在不在**，不取状态码 —— api-contract §2.7 写明
 * 「429 与 503 **必带** Retry-After」，所以没有这个头就不是限流。
 * （这是本仓「按 code 分支不按状态码分支」这条规则的一个边界情况：
 *  code 撞车时，用**另一个契约保证的信号**去分，而不是回头去看状态码。）
 */
function inviteCreateErrorCopy(
  error: ApiError,
  retrySeconds: number | null,
): { title: string; description: string; pending: boolean } {
  if (isNotImplemented(error)) {
    return {
      pending: true,
      title: '该功能尚未开放',
      description: '生成邀请码的接口还没上线。不是你的操作有问题，重试也不会有变化。',
    };
  }
  if (error.code === 'QUOTA_RATE_LIMITED' && error.retryAfterSeconds === undefined && retrySeconds === null) {
    return {
      pending: false,
      title: '未核销的邀请码已达上限',
      description: `同时最多持有 ${UNUSED_CODE_QUOTA} 个没被用掉的码。等其中一个被用掉之后再来生成。`,
    };
  }
  const shared = commonWriteErrorCopy(error, { retrySeconds });
  if (shared) return { ...shared, pending: false };
  return { ...fallbackWriteErrorCopy(error, '邀请码没能生成'), pending: false };
}

/* ─────────────────────────── 佣金合计与划转 ─────────────────────────── */

function CommissionBalance({
  wallet,
  data,
  onTransferred,
}: {
  wallet: ApiQuery<Wallet>;
  data: Wallet;
  onTransferred: () => void;
}) {
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<ApiError | null>(null);
  const [done, setDone] = useState<number | null>(null);
  const countdown = useRetryCountdown();

  const available = data.commission_available_amount;

  async function transfer(): Promise<void> {
    if (pending || countdown.seconds !== null || available <= 0) return;
    setPending(true);
    setError(null);
    setDone(null);
    try {
      // 🔴 **金额只能是「全部可划转」，不给自由输入框。**
      // 后端 `TransferCommission` 里的 `pickCommissionsForAmount` 要求
      // `amount` 等于「按确认时间从旧到新取前 k 条」的**前缀和**：
      // 一条 ¥15.90 的佣金要么整条划转，要么原封不动（`commissions.status` 是整行状态，
      // 没有 `amount_transferred` 这样的列）。给一个自由金额框，用户输入的绝大多数值
      // 都会撞 422，而他无从知道该输什么。
      // 「全部」永远是一个合法的前缀和（就是最后一个），所以这是唯一不会让用户猜的形态。
      const updated = await unwrap(
        api().POST('/api/v1/user/commissions/transfer', { body: { amount: available } }),
      );
      // 响应体就是划转后的 Wallet（同一事务里读的快照）——
      // 直接用它覆盖，**不重新发一次 getWallet**：那两次之间的并发消费
      // 会让用户看到一个「划转之后反而变少了」的数字。
      wallet.patch(() => updated);
      setDone(available);
      // 明细里那几条的状态变了（confirmed → settled），所以那一块要重拉。
      onTransferred();
    } catch (cause) {
      const apiError = asApiError(cause, '划转失败');
      setError(apiError);
      countdown.start(apiError.retryAfterSeconds);
    } finally {
      setPending(false);
    }
  }

  const copy = error ? transferErrorCopy(error, countdown.seconds) : null;

  return (
    <>
      <dl>
        {/* 两段式。**两个数分开显示，不相加** —— 确认中的那部分还可能因退款被作废。 */}
        <MoneyRow
          label="确认中"
          cents={data.commission_pending_amount}
          hint="被邀请人下单了，但还在退款冷静期内。冷静期过后会变成「可划转」。"
        />
        <MoneyRow
          label="可划转"
          cents={available}
          hint="已经确认归你了，但还没进余额 —— 要点一次下面的按钮。"
          emphasis
        />
      </dl>

      <div className="mt-4">
        {available > 0 ? (
          // 🔴 二次确认：这是一笔**不可逆**的资金动作。
          <ConfirmAction
            label={`把 ${formatCny(available)} 划转到余额`}
            confirmLabel={`确认划转 ${formatCny(available)}`}
            pending={pending}
            disabled={countdown.seconds !== null}
            question={
              <>
                划转之后这笔钱就变成<strong className="font-medium">只能消费的余额</strong>，
                无法退回佣金、也不能提现。{NO_WITHDRAW_NOTICE}
              </>
            }
            onConfirm={() => void transfer()}
          />
        ) : (
          <p className="text-sm text-fg-muted">
            现在没有可划转的佣金。被邀请人下单并过了冷静期之后，这里会出现可划转的金额。
          </p>
        )}
      </div>

      {done !== null ? (
        <div className="mt-3">
          <WriteOk>
            已把 {formatCny(done)} 划转到余额。
            <a href="/wallet" className="ml-1 underline">
              去钱包看看
            </a>
          </WriteOk>
        </div>
      ) : null}

      {copy ? (
        <div className="mt-3">
          {copy.pending ? (
            <div className="rounded-lg border border-dashed border-line bg-surface-alt/60 px-3 py-2 text-sm leading-relaxed text-fg-muted">
              <span className="font-medium text-fg">{copy.title}</span> {copy.description}
            </div>
          ) : (
            <WriteError title={copy.title} description={copy.description} />
          )}
        </div>
      ) : null}

      {countdown.seconds !== null ? (
        <p className="mt-2 text-sm text-fg-muted">{countdown.seconds} 秒后可再试</p>
      ) : null}
    </>
  );
}

/**
 * 划转的 `ErrorCode` → 文案。
 *
 * 🔴 `INTERNAL_DEPENDENCY_DOWN`（503）由 `commonWriteErrorCopy` 统一接住，
 * 说的是「暂时不可用，请稍后再试」；它**必须**与 `INTERNAL_ERROR`（500，
 * 走兜底的「我们这边出了问题」）区分开：503 时重试多少次都一样，
 * 后端那边缺的是一支还没跑的 migration。
 */
function transferErrorCopy(
  error: ApiError,
  retrySeconds: number | null,
): { title: string; description: string; pending: boolean } {
  if (isNotImplemented(error)) {
    return {
      pending: true,
      title: '该功能尚未开放',
      description: '佣金划转的接口还没上线。你的佣金没有变化。',
    };
  }
  if (error.code === 'STATE_CONFLICT') {
    return {
      pending: false,
      title: '佣金状态刚刚变了',
      description: '可能是另一个标签页同时划转了，或者有一笔佣金刚被确认。刷新一下再试，钱一分没少。',
    };
  }
  if (error.code === 'VALIDATION_FAILED' || error.code === 'VALIDATION_MALFORMED_BODY') {
    // 后端在金额对不上前缀和时会把「可接受的金额」放进 `details`。
    // 这一页只发「全部」所以正常撞不到，但真撞到了要把服务端算出来的那几个数原样给用户看。
    return {
      pending: false,
      title: '这笔金额没法整条划转',
      description: fieldReasons(error) ?? error.message,
    };
  }
  const shared = commonWriteErrorCopy(error, { retrySeconds });
  if (shared) return { ...shared, pending: false };
  return { ...fallbackWriteErrorCopy(error, '佣金没能划转'), pending: false };
}

/* ─────────────────────────── 佣金明细 ─────────────────────────── */

const COMMISSION_STATUS: Record<
  Commission['status'],
  { label: string; tone: 'neutral' | 'ok' | 'info' }
> = {
  pending: { label: '确认中', tone: 'info' },
  confirmed: { label: '可划转', tone: 'ok' },
  settled: { label: '已划转', tone: 'neutral' },
};

function CommissionList({ first }: { first: CommissionPage }) {
  const [more, setMore] = useState<readonly Commission[]>([]);
  const [moreMeta, setMoreMeta] = useState<Meta | null>(null);
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<ApiError | null>(null);

  const items = [...first.items, ...more];
  const meta = moreMeta ?? first.meta;

  if (items.length === 0) {
    return (
      <EmptyState
        title="还没有佣金记录"
        description="被邀请人用你的码注册并完成首单之后，这里会出现一条一次性的定额佣金。"
        action={
          <LinkButton tone="primary" href="/wallet">
            看看钱包余额 <Icon.ArrowRight size={14} />
          </LinkButton>
        }
        secondary="每位被邀请人只发一次，与他买的周期长短无关。"
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
      const page = await loadCommissions(cursor);
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
      <ul className="divide-y divide-line">
        {items.map((item) => {
          const status = COMMISSION_STATUS[item.status] ?? { label: item.status, tone: 'neutral' as const };
          return (
            <li key={item.id} className="grid gap-1 py-3 text-sm sm:grid-cols-4 sm:items-baseline sm:gap-x-3">
              <span>
                <Badge tone={status.tone}>{status.label}</Badge>
              </span>
              <span className="font-mono tabular-nums text-fg sm:text-right">{formatCny(item.amount)}</span>
              <span className="font-mono text-xs text-fg-subtle sm:text-right">
                {item.order_trade_no ?? '—'}
              </span>
              <span className="text-xs text-fg-subtle sm:text-right">
                {formatDateTime(item.confirmed_at ?? item.created_at)}
              </span>
            </li>
          );
        })}
      </ul>

      {moreCopy ? (
        <div className="mt-3">
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

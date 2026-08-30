/**
 * 模块 9 · 邀请与返佣 `/admin/invites` —— P1 / M3。危险操作 **D11**（手工调整佣金）。
 *
 * 端点：`listAdminInvites` · `createAdminInvite` · `adjustAdminCommission`。
 *
 * 邀请制注册 → **发码是 P1 能力**，没有它连第一个用户都进不来。冷启动的第一步就在这一页。
 *
 * # 这一页的两条硬事实
 *
 * 1. 🔴 **`POST /admin/invites` 只能造管理员种子码。** 服务端把 `owner_user_id` 恒置为 NULL
 *    （`invite_codes_user_single_use` 这条 CHECK 只在 owner 非 NULL 时强制一次性），
 *    所以这里造出来的码可以设 `max_uses > 1`，而**用户自己生成的码恒为一次性**。
 *    换句话说：这个端点造不出「替某个用户批量发码」的东西，那是另一件事。
 *
 * 2. 🔴 **一个能用 50 次的码，泄漏后的后果比一次性码差 50 倍。** 所以「可用次数」这一列
 *    在列表里带警示色，生成表单里也把这句话摆在输入框旁边 —— 冷启动时批量发一组
 *    `use_limit=50` 的码很省事，而它们只要有一个被贴到论坛上，注册闸门就等于开着。
 *
 * # 契约缺口（列表上表达不出来的东西，不编造）
 *
 *  - 契约的 `InviteCode` 只有 `id / code / status / invite_url / used_count / use_limit /
 *    created_at`。**没有 `owner`、没有 `expires_at`、没有「种子码 / 用户码」的类型字段。**
 *    空壳里那句「两者在列表里要能一眼分辨」因此**做不到**：`use_limit > 1` 必然是种子码，
 *    但 `use_limit = 1` 两者都可能。这里只显示**能证明的那一半**（次数），
 *    不去按次数猜类型 —— 猜错的现象是「列表说这是种子码，其实是某个用户的码」。
 *  - **邀请关系树没有端点**（`listAdminInvites` 只给码，不给谁邀请了谁）。
 *  - **佣金记录列表没有端点**：契约里只有 `POST /admin/commissions/{id}/adjust`，
 *    没有 `GET /admin/commissions`。所以调整佣金只能**手输佣金记录 ID**，
 *    ID 从审计日志 / 工单 / 库里来。这不是好用的形态，但它是真话；
 *    做一个假的列表（比如按用户猜）会更糟。
 *  - `AdminInviteCreateRequest` 没有 `expires_at` → 生成的码**永不过期**。
 *    凭空给一个有效期等于让管理员发出去的邀请在某天突然失效，而他没同意过任何期限。
 */
import { useCallback, useState, type ReactNode } from 'react';
import { ApiError, unwrap, unwrapWithMeta } from '@babelplus/shared/api';
import {
  Badge,
  Button,
  Card,
  CardTitle,
  EmptyState,
  formatCny,
  formatDateTime,
} from './_imports.ts';
import { api } from '../lib/api.ts';
import { DangerousAction, dangerErrorCopy } from '../components/DangerousAction.tsx';
import {
  DangerOpsNote,
  DataTable,
  IntField,
  ListLoading,
  ModuleHeader,
  PAGE_SIZE,
  Pager,
  QueryErrorState,
  TextAreaField,
  Td,
  Tr,
  centsPreview,
  parseInteger,
  useApiQuery,
  useCursorPager,
  useRememberedTotal,
  type Commission,
  type InviteCode,
} from './catalog-common.tsx';

/** 服务端 `adminInviteMaxCount`，与契约 `AdminInviteCreateRequest.count` 的 maximum 同值。 */
export const INVITE_MAX_COUNT = 500;

type StatusFilter = 'all' | 'ok' | 'exhausted' | 'disabled';

const STATUS_LABEL: Readonly<Record<string, string>> = {
  ok: '可用',
  exhausted: '已用尽',
  disabled: '已撤销',
};

export default function InvitesPage() {
  const pager = useCursorPager();
  const page = useApiQuery(
    () =>
      unwrapWithMeta(
        api().GET('/api/v1/admin/invites', {
          params: {
            query: {
              limit: PAGE_SIZE,
              ...(pager.cursor === null ? {} : { cursor: pager.cursor }),
              // 总数只在第一页要一次（COUNT(*) 在 db-f1-micro 上是实打实的开销）。
              ...(pager.cursor === null ? { count: true } : {}),
            },
          },
        }),
      ),
    [pager.cursor],
    '邀请码列表加载失败',
  );
  const total = useRememberedTotal(page.data?.meta);
  const [status, setStatus] = useState<StatusFilter>('all');
  const [generatorOpen, setGeneratorOpen] = useState(false);

  const reload = page.reload;
  const afterGenerate = useCallback(() => {
    reload();
  }, [reload]);

  const items = page.data?.data ?? [];
  const shown = status === 'all' ? items : items.filter((c) => c.status === status);

  return (
    <>
      <ModuleHeader
        title="邀请与返佣"
        description="种子码批量生成、邀请码状态、手工调整佣金。"
        priority="P1"
        mobile="M3"
        actions={
          <Button tone="primary" onClick={() => setGeneratorOpen((v) => !v)}>
            {generatorOpen ? '收起生成器' : '批量生成种子码'}
          </Button>
        }
      />

      <DangerOpsNote codes={['D11']} />

      {generatorOpen ? (
        <div className="mb-5">
          <SeedCodeGenerator onCreated={afterGenerate} />
        </div>
      ) : null}

      <div className="space-y-5">
        <Card>
          <CardTitle hint="listAdminInvites">邀请码</CardTitle>

          <p className="mb-3 rounded-lg border border-line bg-surface-alt p-3 text-xs leading-relaxed text-fg-muted">
            ⚠️ 契约的 <code className="font-mono">InviteCode</code> 里
            <strong className="font-medium text-fg">没有归属人、也没有有效期</strong>，
            所以这张表分不出「管理员种子码」与「用户自己生成的码」——
            <code className="font-mono">use_limit &gt; 1</code> 必然是种子码，但{' '}
            <code className="font-mono">use_limit = 1</code> 两者都可能。
            这里只显示能证明的那一半（可用次数），不按次数去猜类型。
          </p>

          {page.state === 'loading' ? <ListLoading /> : null}

          {page.state === 'error' && page.error !== null ? (
            <QueryErrorState error={page.error} what="邀请码列表" onRetry={page.reload} />
          ) : null}

          {page.state === 'ready' && items.length === 0 && pager.atFirstPage ? (
            <EmptyState
              title="还没有邀请码"
              description="冷启动要先在这里批量发一组种子码。用户自助生成的码是常态增长路径，那是 P2。"
              action={
                <Button tone="primary" onClick={() => setGeneratorOpen(true)}>
                  批量生成种子码
                </Button>
              }
            />
          ) : null}

          {page.state === 'ready' && items.length > 0 ? (
            <>
              <div className="mb-3 flex flex-wrap items-center gap-2">
                {(
                  [
                    ['all', '全部'],
                    ['ok', '可用'],
                    ['exhausted', '已用尽'],
                    ['disabled', '已撤销'],
                  ] as const
                ).map(([value, label]) => (
                  <Button
                    key={value}
                    tone={status === value ? 'primary' : 'default'}
                    onClick={() => setStatus(value)}
                  >
                    {label}
                  </Button>
                ))}
                {/* 契约的 listAdminInvites 没有筛选参数，这个筛选只作用在当前这一页上。
                    做成服务端筛选的样子会让人以为「筛不出来 = 不存在」。 */}
                <span className="text-xs text-fg-subtle">
                  只过滤当前这一页（契约没有服务端筛选参数）
                </span>
              </div>

              <DataTable head={['码', '可用次数 / 已用', '状态', '创建时间', '邀请链接']}>
                {shown.map((code) => (
                  <Tr key={code.id}>
                    <Td>
                      <span className="font-mono font-medium text-fg">{code.code}</span>
                      <span className="mt-0.5 block font-mono text-xs text-fg-subtle">
                        #{code.id}
                      </span>
                    </Td>
                    <Td className="whitespace-nowrap">
                      <UseLimitCell code={code} />
                    </Td>
                    <Td>
                      <Badge
                        tone={
                          code.status === 'ok'
                            ? 'ok'
                            : code.status === 'exhausted'
                              ? 'neutral'
                              : 'warn'
                        }
                      >
                        {STATUS_LABEL[code.status] ?? code.status}
                      </Badge>
                    </Td>
                    <Td className="whitespace-nowrap text-xs">{formatDateTime(code.created_at)}</Td>
                    <Td className="text-xs break-all">
                      {code.invite_url === undefined ? (
                        <span className="text-fg-subtle">—</span>
                      ) : (
                        <span className="font-mono">{code.invite_url}</span>
                      )}
                    </Td>
                  </Tr>
                ))}
              </DataTable>

              <Pager
                meta={page.data?.meta ?? null}
                pager={pager}
                total={total}
                busy={page.state !== 'ready'}
              />
            </>
          ) : null}
        </Card>

        <CommissionAdjustCard />
      </div>
    </>
  );
}

/**
 * 可用次数格。
 *
 * 🔴 `use_limit > 1` 要显眼：一个能用 50 次的码和一个只能用 1 次的码，
 * 泄漏后的后果差 50 倍，而它们在一张等宽表格里长得一模一样。
 */
function UseLimitCell({ code }: { code: InviteCode }) {
  const limit = code.use_limit;
  const used = code.used_count ?? 0;
  if (limit === undefined) {
    // 契约注释说「0 = 不限」，但库里 `max_uses >= 1` 是 CHECK，所以这个分支
    // 在本系统里不该出现。真出现了就如实说不知道，不要显示成「不限」。
    return <span className="font-mono text-fg-muted">? / {used}</span>;
  }
  return (
    <span className="flex items-baseline gap-2">
      <span className="font-mono">
        {limit} / {used}
      </span>
      {limit > 1 ? <Badge tone="warn">可用 {limit} 次</Badge> : null}
    </span>
  );
}

/* ────────────────────────── 批量生成种子码 ────────────────────────── */

interface GeneratorResult {
  readonly requested: number;
  readonly codes: readonly InviteCode[];
}

/**
 * 批量生成种子码。
 *
 * **不是 D 项**（§4.4 的 16 条里没有「生成邀请码」），所以这里不套 `DangerousAction` ——
 * 套一个登记表里没有的编号会被那个组件渲染成「装配错误」，那是对的行为。
 * 但它仍然是一次性造出一批注册凭据，所以警示文案不能少。
 */
function SeedCodeGenerator({ onCreated }: { onCreated: () => void }) {
  const [count, setCount] = useState('10');
  const [useLimit, setUseLimit] = useState('1');
  const [note, setNote] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<ApiError | null>(null);
  const [result, setResult] = useState<GeneratorResult | null>(null);

  const countN = parseInteger(count);
  const limitN = parseInteger(useLimit);
  const problem =
    countN === null || countN < 1 || countN > INVITE_MAX_COUNT
      ? `生成数量要填 1–${INVITE_MAX_COUNT} 之间的整数。`
      : limitN === null || limitN < 1
        ? '每个码的可用次数至少是 1（本系统没有不限次的邀请码，max_uses ≥ 1 是数据库约束）。'
        : null;

  async function submit() {
    if (problem !== null || countN === null || limitN === null) return;
    setBusy(true);
    setError(null);
    try {
      const codes = await unwrap(
        api().POST('/api/v1/admin/invites', {
          body: {
            count: countN,
            use_limit: limitN,
            ...(note.trim() === '' ? {} : { note: note.trim() }),
          },
        }),
      );
      setResult({ requested: countN, codes });
      onCreated();
    } catch (cause) {
      setError(
        cause instanceof ApiError
          ? cause
          : new ApiError({ status: 0, code: 'UNKNOWN', message: '生成没能完成', cause }),
      );
    } finally {
      setBusy(false);
    }
  }

  return (
    <Card className="border-l-4 border-l-accent">
      <CardTitle hint="createAdminInvite">批量生成种子码</CardTitle>

      <p className="mb-4 rounded-lg border border-warn/30 bg-warn/5 p-3 text-xs leading-relaxed text-fg-muted">
        这个端点造出来的都是<strong className="font-medium text-fg">管理员种子码</strong>
        （服务端把归属人固定为空），所以它们可以设成多次可用 —— 用户自己生成的码恒为一次性。
        <span className="mt-1 block">
          🔴 <strong className="font-medium text-fg">一个能用 50 次的码，泄漏后的后果差 50 倍。</strong>
          冷启动图省事发一批高次数的码，只要有一个被贴到论坛上，注册闸门就等于开着。
        </span>
        <span className="mt-1 block">
          ⚠️ 生成的码<strong className="font-medium text-fg">永不过期</strong>：契约里没有有效期字段。
          要停掉一个码只能去撤销它。
        </span>
      </p>

      <div className="space-y-4">
        <div className="grid gap-4 sm:grid-cols-2">
          <IntField
            label="生成数量"
            value={count}
            onChange={setCount}
            placeholder="10"
            suffix={`最多 ${INVITE_MAX_COUNT}`}
          />
          <IntField
            label="每个码可用次数"
            value={useLimit}
            onChange={setUseLimit}
            suffix={limitN !== null && limitN > 1 ? `⚠️ 可用 ${limitN} 次` : '一次性'}
            hint="至少 1。填 1 就是一次性码，与用户自己生成的码同形态。"
          />
        </div>

        <TextAreaField
          label="备注（可选）"
          value={note}
          onChange={setNote}
          rows={2}
          hint="只存在库里，不进审计的 reason（这个端点契约里没有 reason 字段）。写清楚这批码发给谁。"
        />

        {problem !== null ? (
          <p className="rounded-lg border border-line bg-surface-alt px-3 py-2 text-sm text-fg-muted">
            还不能提交：{problem}
          </p>
        ) : null}

        {error !== null ? <WriteError error={error} /> : null}

        <Button tone="primary" disabled={problem !== null || busy} onClick={() => void submit()}>
          {busy ? '生成中…' : '生成'}
        </Button>

        {result !== null ? <GeneratorOutput result={result} /> : null}
      </div>
    </Card>
  );
}

function GeneratorOutput({ result }: { result: GeneratorResult }) {
  const short = result.requested - result.codes.length;
  return (
    <div className="rounded-lg border border-line bg-surface-alt p-3">
      <p className="text-sm font-medium text-fg">
        已生成 {result.codes.length} 个码
        {short > 0 ? (
          <span className="text-warn">
            （比申请的少 {short} 个 —— 服务端如实上报的就是这个数，不要按申请数去发）
          </span>
        ) : null}
      </p>
      {/* 一个可全选复制的纯文本块。码本身也在下面的列表里，这里只是省一次翻页。 */}
      <pre className="mt-2 max-h-64 overflow-auto rounded-lg border border-line bg-surface p-2 font-mono text-xs leading-relaxed text-fg">
        {result.codes.map((c) => c.invite_url ?? c.code).join('\n')}
      </pre>
    </div>
  );
}

/* ────────────────────────── D11 · 手工调整佣金 ────────────────────────── */

/**
 * 调整佣金（`adjustAdminCommission`，**D11**）。
 *
 * 🔴 **只能调「确认中」与「已确认」两态。** `transferred` 意味着这笔钱**已经变成用户余额**了
 * （账本上有对应分录），事后改佣金金额不会动分录，于是佣金表与账本对不上
 * 而**两边都不会报错**。正确做法是写一条冲正分录。`voided` 是退款套利被作废的，
 * 复活它需要先解释当初为什么作废 —— 同样不该由一个 +/- 数字完成。
 * 服务端会用 `STATE_CONFLICT` 挡住这两种，本页把这句话提前说出来。
 *
 * ⚠️ **契约没有佣金列表端点**，所以记录 ID 只能手输。这不好用，但它是真话。
 */
function CommissionAdjustCard() {
  const [id, setId] = useState('');
  const [amount, setAmount] = useState('');
  const [done, setDone] = useState<Commission | null>(null);

  const idN = parseInteger(id);
  const amountN = parseInteger(amount);
  const problem =
    idN === null || idN <= 0
      ? '要先填一个佣金记录 ID（正整数）。'
      : amountN === null
        ? '调整额要填一个整数（单位是分，可以是负数）。'
        : amountN === 0
          ? '调整额不能是 0 —— 那只会往审计表里写一条什么都没发生的记录。'
          : null;

  return (
    <Card>
      <CardTitle hint="adjustAdminCommission（D11）">邀请关系与佣金</CardTitle>

      <div className="mb-4 rounded-lg border border-line bg-surface-alt p-3 text-xs leading-relaxed text-fg-muted">
        <p>
          <strong className="font-medium text-fg">这一块只做得出「调整」，做不出「查看」。</strong>
          契约里只有 <code className="font-mono">POST /admin/commissions/{'{id}'}/adjust</code>，
          <strong className="font-medium text-fg">没有佣金列表端点，也没有邀请关系树端点</strong>。
          所以记录 ID 只能从审计日志 / 工单 / 库里拿到后手输。
        </p>
        <p className="mt-1">
          佣金的「确认中 → 已确认」两段式是<strong className="font-medium text-fg">退款冷静期的防套利设计</strong>
          ：先邀请、再退款、佣金已到手，这条路要被堵住。
        </p>
      </div>

      <div className="space-y-4">
        <div className="grid gap-4 sm:grid-cols-2">
          <IntField
            label="佣金记录 ID"
            value={id}
            onChange={(v) => {
              setId(v);
              setDone(null);
            }}
            placeholder="1234"
            hint="commissions.id。填错会得到 404，不会误伤别人。"
          />
          <IntField
            label="调整额（分，可为负）"
            value={amount}
            onChange={(v) => {
              setAmount(v);
              setDone(null);
            }}
            placeholder="-1590"
            suffix={centsPreview(amount)}
            hint="这是**增量**不是新值：填 -1590 表示在现有金额上减 ¥15.90。"
          />
        </div>

        {problem !== null ? (
          <p className="rounded-lg border border-line bg-surface-alt px-3 py-2 text-sm text-fg-muted">
            还不能提交：{problem}
          </p>
        ) : null}

        <DangerousAction
          code="D11"
          title="调整这笔佣金"
          submitLabel="确认调整"
          // D11 在登记表里就是「必填原因」，这里不用覆盖 —— 组件会自己读 DANGER.D11.reason。
          disabled={problem !== null}
          disabledReason={problem}
          context={
            <>
              <p className="font-medium text-fg">
                佣金记录 #{id.trim() || '—'} 的金额{' '}
                {amountN === null ? '—' : amountN > 0 ? `增加 ${formatCny(amountN)}` : `减少 ${formatCny(-amountN)}`}
              </p>
              <ul className="mt-1 list-disc space-y-1 pl-5 text-sm leading-relaxed text-fg-muted">
                <li>
                  这是<strong className="font-medium text-fg">增量</strong>，不是把金额设成这个数。
                </li>
                <li>
                  只有「确认中 / 已确认」的佣金能这样调。
                  <strong className="font-medium text-fg">已划转的不行</strong> ——
                  那笔钱已经变成用户余额、账本上有分录了，直接改金额会让佣金表与账本对不上
                  而两边都不报错。要改它得写一条冲正分录。
                </li>
                <li>
                  <strong className="font-medium text-fg">已作废的也不行</strong>
                  （那是退款套利被作废的），复活它需要先解释当初为什么作废。
                </li>
                <li>调整后的金额不能是负数，服务端会挡住调过头的负向调整。</li>
              </ul>
            </>
          }
          onSubmit={async (values) => {
            if (problem !== null || idN === null || amountN === null) return;
            const updated = await unwrap(
              api().POST('/api/v1/admin/commissions/{id}/adjust', {
                params: { path: { id: idN } },
                body: { amount: amountN, reason: values.reason ?? '' },
              }),
            );
            setDone(updated);
          }}
        />

        {done !== null ? (
          <div className="rounded-lg border border-ok/30 bg-ok/5 p-3 text-sm">
            <p className="font-medium text-fg">调整完成</p>
            <dl className="mt-1 grid gap-x-6 gap-y-1 sm:grid-cols-2">
              <Row label="记录 ID">#{done.id}</Row>
              <Row label="调整后金额">{formatCny(done.amount)}</Row>
              <Row label="状态">{done.status}</Row>
              <Row label="关联订单">{done.order_trade_no ?? '—'}</Row>
            </dl>
          </div>
        ) : null}
      </div>
    </Card>
  );
}

function Row({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div>
      <dt className="text-xs text-fg-muted">{label}</dt>
      <dd className="mt-0.5 font-mono text-fg">{children}</dd>
    </div>
  );
}

/**
 * 写操作失败的提示。复用 `dangerErrorCopy`（写侧那张表）——
 * 这里各写一份的话，同一个 `AUTH_PERMISSION_DENIED` 会在这一页和确认面板里说两句不同的话。
 */
function WriteError({ error }: { error: ApiError }) {
  const copy = dangerErrorCopy(error);
  return (
    <div
      role="alert"
      className="rounded-lg border border-danger/30 bg-danger/10 px-3 py-2 text-sm text-danger"
    >
      <p className="font-medium">{copy.title}</p>
      <p className="mt-0.5 leading-relaxed">{copy.description}</p>
      {error.requestId ? (
        <p className="mt-1 font-mono text-xs opacity-80">请求号 {error.requestId}</p>
      ) : null}
    </div>
  );
}

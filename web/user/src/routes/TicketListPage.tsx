/**
 * `/ticket` —— P1，无替代。page-inventory §3.1 #10、§3.2.6。
 *
 * 空态**不是**「暂无工单」，而是排障决策树的入口 —— 这是把 12+ 篇排障文档
 * 接到工单入口上的唯一位置。文档写了没人看，等于没写。
 *
 * 建单表单的三步是**产品硬要求**（§3.2.6），不是交互装饰：
 *   ① 分类必选（subscription / node-down / billing / account，没有「其他」）
 *   ② 选定分类后**先**给出对应的排障文档入口
 *   ③ 再给一个「我已经看过了，还是要提单」的按钮才展开正文
 * 把 ② ③ 顺手删掉，整条「文档 → 工单」的引流就断了，而断了之后**没有任何报错**
 * —— 只会表现为工单量慢慢涨回去。测试里钉了这一条。
 *
 * 🔴 `context` 的归属：**服务端在建单时重新采集并覆盖**，客户端提交的那份只会被存进
 * `context.client_reported`（openapi 的 `/api/v1/tickets` post 描述、`TicketClientContext` 注释）。
 * 所以这一页**不许**把自己拼的快照说成「已记录的诊断上下文」——
 * 它只是「你的浏览器现在看到的状态」，可能已经过时，也可能被改过。
 *
 * 这一页已接线，所以不再读 `?state=` 调试开关（README §7 代价 3）。
 */
import { useMemo, useState, type FormEvent } from 'react';
import { Link, useNavigate, useSearchParams } from 'react-router';
import {
  Button,
  Card,
  CardTitle,
  EmptyState,
  Icon,
  LinkButton,
  LoadingState,
  SkeletonCard,
} from './_imports.ts';
import { formatDateTime, runtimeConfig } from './_imports.ts';
import { unwrapWithMeta, type ApiError, type Meta } from '@babelplus/shared/api';
import { useAuth } from '../lib/auth.tsx';
import { api } from '../lib/api.ts';
import {
  FormAlert,
  // 501 的呈现不在这里做分支：QueryErrorState 内部已经先判 isNotImplemented
  // 再决定渲染 NotImplementedNotice 还是普通错误态。这一页只需要把 error 交给它。
  QueryErrorState,
  StatusBadge,
  TICKET_CATEGORIES,
  TextArea,
  TextField,
  asApiError,
  categoryLabel,
  ticketErrorCopy,
  useApiQuery,
  useRetryCountdown,
  type Ticket,
  type TicketCategory,
  type TicketClientContext,
} from './ticket-common.tsx';

/** 一页多少条。列表页不做无限滚动 —— 「加载更多」是可停下来的，滚动不是。 */
const PAGE_SIZE = 20;

/** 正文与主题的长度上限。契约没给，这里只是防止把一整个日志文件贴成主题。 */
const SUBJECT_MAX = 120;
const MESSAGE_MAX = 4000;

export default function TicketListPage() {
  const cfg = runtimeConfig();
  const [params] = useSearchParams();

  // 来源参数（tutorials-spec §4.1 的排障决策树叶子节点会带着它跳过来）。
  // 有 `from` 就直接把建单表单展开：从决策树点过来的人，意图已经很明确了，
  // 再让他找一遍「新建工单」按钮是白白多一步。
  const origin = useMemo(() => readTicketOrigin(params), [params]);
  const [composing, setComposing] = useState(origin !== null);

  const tickets = useApiQuery(() => listTicketsPage(null), [], '工单列表加载失败');

  return (
    <>
      <header className="mb-5 flex flex-col gap-3 sm:mb-6 sm:flex-row sm:items-start sm:justify-between">
        <div className="min-w-0">
          <h1 className="text-xl font-semibold tracking-tight text-fg sm:text-2xl">工单</h1>
          <p className="mt-1 max-w-2xl text-sm text-fg-muted">自己解决不了的，交给我们。</p>
        </div>
        <div className="flex shrink-0 flex-wrap gap-2">
          {cfg.docsUrl ? (
            <LinkButton href={cfg.docsUrl} external>
              排障文档 <Icon.External size={14} />
            </LinkButton>
          ) : null}
          <Button tone={composing ? 'default' : 'primary'} onClick={() => setComposing((v) => !v)}>
            {composing ? '收起' : '新建工单'}
          </Button>
        </div>
      </header>

      {composing ? (
        <div className="mb-5">
          <NewTicketForm origin={origin} onCreated={() => setComposing(false)} />
        </div>
      ) : null}

      <TicketListSection query={tickets} onCompose={() => setComposing(true)} />
    </>
  );
}

/* ───────────────────────────── 列表 ───────────────────────────── */

interface TicketPage {
  readonly items: readonly Ticket[];
  readonly meta: Meta;
}

function listTicketsPage(cursor: string | null): Promise<TicketPage> {
  const query = cursor === null ? { limit: PAGE_SIZE } : { limit: PAGE_SIZE, cursor };
  return unwrapWithMeta(api().GET('/api/v1/tickets', { params: { query } })).then((envelope) => ({
    items: envelope.data,
    meta: envelope.meta,
  }));
}

/**
 * 列表区。**自己的一套三态**，与建单表单互不影响 ——
 * 建单失败不该让已经加载出来的列表消失，列表加载失败也不该挡住建单
 * （连不上列表的时候恰恰是最想提单的时候）。
 */
function TicketListSection({ query, onCompose }: { query: ReturnType<typeof useApiQuery<TicketPage>>; onCompose: () => void }) {
  const cfg = runtimeConfig();

  // 「加载更多」拿到的后续页。**单独存**，不塞回第一页的 query 里 ——
  // 重试第一页时这些应该一起作废，而它们确实会随 query 重建而清掉。
  const [more, setMore] = useState<readonly Ticket[]>([]);
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
    return <QueryErrorState error={query.error} what="工单列表" onRetry={query.reload} />;
  }

  const first = query.data;
  if (!first) return null;
  const items = [...first.items, ...more];
  const meta = moreMeta ?? first.meta;

  // §3.2.6：空态是决策树入口，不是「暂无工单」。
  // 这一段是整个页面**最不该被简化**的部分：把它换成一句「暂无工单」，
  // 12+ 篇排障文档就与用户彻底断开了。
  if (items.length === 0) {
    return (
      <EmptyState
        title="大部分问题在这里能自己解决"
        description="连不上、订阅拉不到、流量对不上 —— 这三类占了绝大多数。排障文档按现象分类，照着走一遍通常几分钟就好了。"
        action={
          cfg.docsUrl ? (
            <LinkButton tone="primary" href={cfg.docsUrl} external>
              打开排障决策树 <Icon.External size={14} />
            </LinkButton>
          ) : (
            <Button tone="primary" disabled title="docsUrl 未配置">
              打开排障决策树
            </Button>
          )
        }
        secondary={
          <>
            还是不行？{' '}
            <button type="button" className="text-accent hover:underline" onClick={onCompose}>
              新建工单
            </button>{' '}
            —— 建单时会自动带上你的账号快照，不用自己描述环境。
          </>
        }
      />
    );
  }

  async function loadMore(): Promise<void> {
    if (morePending) return;
    const cursor = meta.next_cursor;
    if (!cursor) return;
    setMorePending(true);
    setMoreError(null);
    try {
      const page = await listTicketsPage(cursor);
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
      <CardTitle hint={`${items.length} 张`}>工单列表</CardTitle>

      {/* 6 列，<768px 卡片化（一列堆叠）。列表用 public_id 而不是自增 id ——
          自增 id 会泄漏工单总量。 */}
      <div
        className="hidden grid-cols-7 gap-x-3 border-b border-line pb-2 text-xs font-medium text-fg-muted sm:grid"
        aria-hidden="true"
      >
        <span>工单号</span>
        <span className="col-span-2">主题</span>
        <span>分类</span>
        <span>优先级</span>
        <span>状态</span>
        <span>最后回复</span>
      </div>

      <ul className="divide-y divide-line">
        {items.map((ticket) => (
          <li key={ticket.public_id}>
            <Link
              to={`/ticket/${encodeURIComponent(ticket.public_id)}`}
              className="grid gap-1 py-3 text-sm hover:bg-surface-alt/60 sm:grid-cols-7 sm:items-center sm:gap-x-3"
            >
              <span className="font-mono text-xs text-fg-muted">{ticket.public_id}</span>
              <span className="font-medium text-fg sm:col-span-2">{ticket.subject}</span>
              <span className="text-xs text-fg-muted">{categoryLabel(ticket.category)}</span>
              <span className="text-xs text-fg-muted">{levelLabel(ticket.level)}</span>
              <span>
                <StatusBadge status={ticket.status} />
              </span>
              <span className="text-xs text-fg-muted">
                {formatDateTime(ticket.last_reply_at ?? ticket.updated_at ?? ticket.created_at)}
              </span>
            </Link>
          </li>
        ))}
      </ul>

      {moreError ? (
        <div className="mt-3">
          {/* 分页失败**不清空已经加载出来的部分** —— 用户已经在看的东西不该因为下一页失败而消失。 */}
          <FormAlert>
            {ticketErrorCopy(moreError, { fallbackTitle: '没能加载更多' }).title}
            {' · '}
            {ticketErrorCopy(moreError, { fallbackTitle: '没能加载更多' }).description}
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

/**
 * 优先级。契约里 `level` 是个 int32，**没有定义数字到档位的对照**，
 * 所以这里不编「高 / 中 / 低」，原样显示。编一张对照表出来，
 * 等于替产品裁决了优先级体系，而那还没裁决（AGENTS.md §3：不确定就不编）。
 */
function levelLabel(level: number | undefined): string {
  return level === undefined ? '—' : `L${level}`;
}

/* ─────────────────────────── 新建工单 ─────────────────────────── */

type ComposeStep = 'category' | 'docs' | 'form';

function NewTicketForm({ origin, onCreated }: { origin: TicketOrigin | null; onCreated: () => void }) {
  const cfg = runtimeConfig();
  const navigate = useNavigate();
  const { user } = useAuth();

  const [category, setCategory] = useState<TicketCategory | ''>(origin?.category ?? '');
  const [acknowledged, setAcknowledged] = useState(false);
  const [subject, setSubject] = useState('');
  const [message, setMessage] = useState(() => (origin ? originPreamble(origin) : ''));
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<ApiError | null>(null);
  const countdown = useRetryCountdown();

  const selected = TICKET_CATEGORIES.find((c) => c.value === category) ?? null;
  const step: ComposeStep = selected === null ? 'category' : acknowledged ? 'form' : 'docs';

  async function onSubmit(event: FormEvent<HTMLFormElement>): Promise<void> {
    event.preventDefault();
    // 单飞：**这就是这三个写端点当前全部的「幂等」**。
    // api-contract §9.1 的幂等总表里没有 `POST /api/v1/tickets`，服务端不认 `Idempotency-Key`，
    // 发一个过去只会让代码看起来比实际更安全。所以这里老老实实只挡住重复点击，
    // 「超时后重发建出两张单」这个缺口留在 still_shell 里，不假装它不存在。
    if (pending || countdown.seconds !== null) return;
    if (selected === null || !acknowledged) return;
    setPending(true);
    setError(null);
    try {
      const created = await createTicket({
        category: selected.value,
        subject: subject.trim(),
        message: message.trim(),
        context: clientReportedContext(user),
      });
      onCreated();
      navigate(`/ticket/${encodeURIComponent(created.public_id)}`);
    } catch (cause) {
      const apiError = asApiError(cause, '建单失败');
      setError(apiError);
      countdown.start(apiError.retryAfterSeconds);
      setPending(false);
      // 正文一个字都不动。用户可能已经打了五分钟，清空重来是最招骂的实现（§3.2.6 错态）。
    }
  }

  return (
    <Card>
      <CardTitle hint="createTicket">新建工单</CardTitle>

      <form className="space-y-4" onSubmit={(event) => void onSubmit(event)}>
        {/* ① 分类必选。用原生 select：移动端有系统级的选择器，比自绘下拉可用得多。 */}
        <div>
          <label htmlFor="tk-category" className="mb-1.5 block text-sm font-medium text-fg">
            这是哪一类问题
          </label>
          <select
            id="tk-category"
            name="category"
            required
            value={category}
            disabled={pending}
            onChange={(event) => {
              setCategory(event.target.value as TicketCategory | '');
              // 换分类要重新看一次对应的文档 —— 上一类的「我看过了」对这一类不作数。
              setAcknowledged(false);
            }}
            className="min-h-11 w-full rounded-lg border border-line bg-surface px-3 text-base text-fg focus-visible:border-accent focus-visible:outline-2 focus-visible:outline-offset-1 focus-visible:outline-accent disabled:opacity-50"
          >
            <option value="">请选择</option>
            {TICKET_CATEGORIES.map((c) => (
              <option key={c.value} value={c.value}>
                {c.label} —— {c.symptom}
              </option>
            ))}
          </select>
        </div>

        {/* ② 选定分类后**先**弹排障文档。这一步是 §3.2.6 的硬要求，不是可选的礼貌提示。 */}
        {selected && !acknowledged ? (
          <div className="rounded-lg border border-accent/30 bg-accent/5 p-3">
            <p className="text-sm font-medium text-fg">先看一眼这一类的排障文档</p>
            <p className="mt-1 text-sm leading-relaxed text-fg-muted">
              「{selected.symptom}」这类问题，大多数在 <strong className="font-medium text-fg">{selected.docsSection}</strong>{' '}
              里有现成的解法，照着走一遍通常几分钟就好了。
            </p>
            <div className="mt-3 flex flex-wrap gap-2">
              {cfg.docsUrl ? (
                <LinkButton tone="primary" href={cfg.docsUrl} external>
                  打开排障文档 <Icon.External size={14} />
                </LinkButton>
              ) : (
                <Button tone="primary" disabled title="docsUrl 未配置">
                  打开排障文档（未配置）
                </Button>
              )}
              <Button onClick={() => setAcknowledged(true)}>我已经看过了，还是要提单</Button>
            </div>
          </div>
        ) : null}

        {/* ③ 看过文档之后才展开正文。 */}
        {step === 'form' ? (
          <>
            <TextField
              label="一句话说明"
              name="subject"
              required
              maxLength={SUBJECT_MAX}
              disabled={pending}
              placeholder="例如：iPhone 上订阅更新一直失败"
              value={subject}
              onChange={setSubject}
            />
            <TextArea
              label="具体情况"
              name="message"
              required
              rows={8}
              maxLength={MESSAGE_MAX}
              disabled={pending}
              placeholder="什么设备、什么客户端、什么时候开始的、已经试过什么。"
              value={message}
              onChange={setMessage}
              hint="贴上客户端里的原始报错比描述「连不上」有用得多。"
            />

            <ClientContextNotice user={user} />

            {error ? (
              <FormAlert>
                <span className="font-medium">
                  {ticketErrorCopy(error, { fallbackTitle: '建单没能完成', retrySeconds: countdown.seconds }).title}
                </span>
                <br />
                {ticketErrorCopy(error, { fallbackTitle: '建单没能完成', retrySeconds: countdown.seconds }).description}
                {error.requestId ? (
                  <>
                    <br />
                    <span className="font-mono text-xs">请求号 {error.requestId}</span>
                  </>
                ) : null}
              </FormAlert>
            ) : null}

            <div className="flex flex-wrap items-center gap-2">
              <Button
                tone="primary"
                type="submit"
                disabled={pending || countdown.seconds !== null || subject.trim() === '' || message.trim() === ''}
              >
                {pending
                  ? '正在提交…'
                  : countdown.seconds !== null
                    ? `${countdown.seconds} 秒后可再试`
                    : '提交工单'}
              </Button>
              <Button
                tone="ghost"
                disabled={pending}
                onClick={() => setAcknowledged(false)}
              >
                换一类
              </Button>
            </div>
          </>
        ) : null}
      </form>
    </Card>
  );
}

/**
 * 客户端自述的诊断快照说明。
 *
 * 措辞是有意的：**「你的浏览器现在看到的」而不是「已记录的诊断上下文」**。
 * 服务端建单时会重新采集一份并覆盖，客户端这份只落在 `context.client_reported`
 * （openapi `/api/v1/tickets` post 描述）。说成「已记录」会让用户和客服都以为
 * 工单里那份就是这里显示的这份，而两者可能不一样 —— 排障时按错的那份找问题，
 * 比没有这份快照更糟。
 */
function ClientContextNotice({ user }: { user: ReturnType<typeof useAuth>['user'] }) {
  const sub = user?.subscription;
  return (
    <div className="rounded-lg border border-dashed border-line bg-surface-alt/60 p-3 text-xs leading-relaxed text-fg-muted">
      <p>
        <span className="font-medium text-fg">提交时会带上你的账号快照</span>
        （套餐、流量、设备数、到期时间），你不用自己描述环境。
      </p>
      <p className="mt-1">
        这是<strong className="font-medium text-fg">你的浏览器现在看到的状态</strong>；
        工单里最终记录的那份由服务端在建单时重新采集，以服务端那份为准。
      </p>
      {sub ? (
        <p className="mt-1.5 font-mono text-fg-subtle">
          {sub.plan_name ?? '（无套餐）'} · 设备 {sub.device_count}/{sub.device_limit}
        </p>
      ) : null}
    </div>
  );
}

/**
 * 拼客户端自述的 `context`。**不含 `last_sub_fetch_*` 与 `last_active_node`** ——
 * `CurrentUser` 里没有这几个字段，编不出来的就不编（AGENTS.md §3）。
 * 服务端本来就会重新采集完整的一份，这里少几项不影响工单质量。
 */
function clientReportedContext(user: ReturnType<typeof useAuth>['user']): TicketClientContext | undefined {
  const sub = user?.subscription;
  if (!sub) return undefined;
  const context: TicketClientContext = {
    used_bytes: sub.upload_bytes + sub.download_bytes,
    total_bytes: sub.total_bytes,
    device_count: sub.device_count,
    device_limit: sub.device_limit,
  };
  if (sub.plan_name !== undefined) context.plan_name = sub.plan_name;
  if (sub.expired_at !== undefined) context.expired_at = sub.expired_at;
  return context;
}

function createTicket(body: {
  category: TicketCategory;
  subject: string;
  message: string;
  context: TicketClientContext | undefined;
}): Promise<Ticket> {
  const payload =
    body.context === undefined
      ? { category: body.category, subject: body.subject, message: body.message }
      : { category: body.category, subject: body.subject, message: body.message, context: body.context };
  return unwrapWithMeta(api().POST('/api/v1/tickets', { body: payload })).then((envelope) => envelope.data);
}

/* ───────────────────── 来源参数（决策树叶子节点） ───────────────────── */

export interface TicketOrigin {
  readonly from: string;
  readonly code: string | null;
  readonly category: TicketCategory | null;
}

/**
 * 读 `?from=diagnose&code=xxx` 这类来源参数（tutorials-spec §4.1 的决策树叶子节点、
 * 以及 `/diagnose` 的诊断码都会带着它跳过来）。
 *
 * 这些值**是用户可控输入**，所以：
 *  - `category` 必须在枚举里，否则丢弃 —— 不能让 URL 决定一个契约外的分类值；
 *  - `from` / `code` 只当**正文里的一行文本**用，长度截断、控制字符剔除。
 *    它们不参与任何跳转，也不拼进任何 URL，所以不存在开放重定向面。
 */
function readTicketOrigin(params: URLSearchParams): TicketOrigin | null {
  const from = sanitizeOriginValue(params.get('from'));
  if (from === null) return null;
  const rawCategory = params.get('category');
  const category = TICKET_CATEGORIES.some((c) => c.value === rawCategory)
    ? (rawCategory as TicketCategory)
    : null;
  return { from, code: sanitizeOriginValue(params.get('code')), category };
}

const ORIGIN_VALUE_MAX = 64;

function sanitizeOriginValue(raw: string | null): string | null {
  if (raw === null) return null;
  let out = '';
  for (const ch of raw.slice(0, ORIGIN_VALUE_MAX)) {
    const point = ch.codePointAt(0) ?? 0;
    // C0/C1 控制字符（含换行）会把这一行拆成好几行，进而伪装成用户自己写的内容。
    if (point <= 0x1f || point === 0x7f || (point >= 0x80 && point <= 0x9f)) continue;
    out += ch;
  }
  out = out.trim();
  return out.length > 0 ? out : null;
}

/**
 * 来源信息写进正文开头，而**不是**塞进某个隐藏字段。
 * 客服看到的第一屏就该有「他从哪一步走过来的」，否则第一句话必然是「你从哪进来的」。
 */
function originPreamble(origin: TicketOrigin): string {
  const parts = [`来源：${origin.from}`];
  if (origin.code) parts.push(`诊断码：${origin.code}`);
  return `${parts.join('　')}\n\n（下面请补充：什么设备、什么客户端、什么时候开始的）\n`;
}

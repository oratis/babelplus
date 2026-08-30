/**
 * `/ticket/:public_id` —— P1，无替代。page-inventory §3.1 #11、§3.2.6。
 *
 * 🔴 安全提示（§3.2.6 末尾）：`ticket_messages.is_internal` 是整个系统最容易出安全事故的一列。
 * **用户侧查询必须走固定带 `is_internal = false` 的视图或方法，不接受调用方传参决定。**
 * 前端这边的对应纪律：**永远不要渲染任何标着 internal 的消息，即使 API 误发了**
 * —— 客户端做一次兜底过滤，成本是一行，代价是零。见 `visibleMessages`。
 *
 * 附件上传失败时**保留已输入的正文，绝不清空**（§3.2.6 错态）。
 * 契约里回复端点只收 `{ message }`，**没有附件通道**，所以这一版没有上传；
 * 但「失败不清空正文」这条对**所有**提交失败都成立，`ReplyForm` 里落实了。
 *
 * 三态纪律：这一页只有 `getTicket` 一个**读**请求，两个写请求（回复、关单）
 * 各自持有自己的 pending / error，**不把整页打回 loading** ——
 * 用户刚打完字，眼前的会话突然变成骨架屏，会让人以为回复没发出去。
 * 回复成功后用 `query.patch` 就地追加，读请求的状态一动不动。
 *
 * 错态一律按 **`ErrorCode`** 分支，不按 HTTP 状态码（api-contract §2.3）。
 * 文案表只有一处：`ticket-common.tsx` 的 `ticketErrorCopy`，这一页不许再写第二处。
 *
 * 这一页已接线，所以不再读 `?state=` 调试开关（README §7 代价 3）。
 */
import { useState, type FormEvent } from 'react';
import { Link, useParams } from 'react-router';
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
  formatDateTime,
} from './_imports.ts';
import { unwrap, type ApiError } from '@babelplus/shared/api';
import { api } from '../lib/api.ts';
import {
  FormAlert,
  // 501 的呈现不在这里做分支：QueryErrorState 内部已经先判 isNotImplemented
  // 再决定渲染 NotImplementedNotice 还是普通错误态。这一页只需要把 error 交给它。
  QueryErrorState,
  StatusBadge,
  TextArea,
  asApiError,
  categoryLabel,
  ticketErrorCopy,
  useApiQuery,
  useRetryCountdown,
  type ApiQuery,
  type Ticket,
  type TicketDetail,
  type TicketMessage,
} from './ticket-common.tsx';

/**
 * 正文上限取**服务端那个数**（`handler/ticket.go` 的 `ticketMessageMaxRunes = 20000`）。
 * 前端自己设一个更小的值，会造成「服务端允许、前端不让发」，而用户看不到任何理由。
 *
 * ⚠️ `maxLength` 数的是 UTF-16 码元，服务端数的是 rune ——
 * 带 emoji 时前端会比服务端更严一点。方向是安全的（不会发出去被拒），
 * 且 422 `VALIDATION_FAILED` 的分支照样在，不靠这个上限兜底。
 */
const MESSAGE_MAX = 20_000;

/** 回复框的 DOM id。`TextArea` 用的是 `tk-${name}`，空态里的「去写回复」要靠它聚焦。 */
const REPLY_FIELD_ID = 'tk-reply';

export default function TicketDetailPage() {
  const { public_id: rawPublicId } = useParams();
  const publicId = (rawPublicId ?? '').trim();

  const detail = useApiQuery(() => getTicketDetail(publicId), [publicId], '工单加载失败');
  const ticket = detail.data?.ticket ?? null;

  return (
    <>
      <header className="mb-5 sm:mb-6">
        <Link to="/ticket" className="inline-flex items-center gap-1 text-sm text-accent hover:underline">
          <Icon.ArrowRight size={14} className="rotate-180" /> 工单列表
        </Link>
        <div className="mt-2 flex flex-col gap-2 sm:flex-row sm:items-start sm:justify-between">
          <div className="min-w-0">
            {/* 标题优先用主题（用户认得的是自己写的那句话），拿不到时退回「工单会话」。 */}
            <h1 className="text-xl font-semibold tracking-tight text-fg sm:text-2xl">
              {ticket?.subject ?? '工单会话'}
            </h1>
            <p className="mt-1 text-sm text-fg-muted">
              工单号 <code className="font-mono text-fg">{publicId || '—'}</code>
            </p>
          </div>
          {/* 状态徽章只在拿到数据后出现：加载中显示一个假状态比不显示更糟。 */}
          {ticket ? <StatusBadge status={ticket.status} /> : null}
        </div>
      </header>

      <TicketConversation query={detail} publicId={publicId} />
    </>
  );
}

/* ───────────────────────────── 端点 ───────────────────────────── */

function getTicketDetail(publicId: string): Promise<TicketDetail> {
  return unwrap(
    api().GET('/api/v1/tickets/{public_id}', { params: { path: { public_id: publicId } } }),
  );
}

function postTicketMessage(publicId: string, message: string): Promise<TicketMessage> {
  return unwrap(
    api().POST('/api/v1/tickets/{public_id}/messages', {
      params: { path: { public_id: publicId } },
      body: { message },
    }),
  );
}

function postCloseTicket(publicId: string): Promise<Ticket> {
  return unwrap(
    api().POST('/api/v1/tickets/{public_id}/close', { params: { path: { public_id: publicId } } }),
  );
}

/* ───────────────────────────── 会话 ───────────────────────────── */

function TicketConversation({ query, publicId }: { query: ApiQuery<TicketDetail>; publicId: string }) {
  if (query.state === 'loading') {
    return (
      <LoadingState>
        <SkeletonCard lines={6} />
      </LoadingState>
    );
  }

  if (query.state === 'error' && query.error) {
    // 404 在这一页**不是故障，是一个空结果**：单号打错、或者这张单不属于当前账号
    // （后端对两者返回同一个 404，理由是 public_id 可枚举 —— 区分开等于确认单号存在）。
    // 走 ErrorState 会给出「重试 / 看状态页」两个都没用的动作，还会让人以为服务挂了。
    if (query.error.code === 'RESOURCE_NOT_FOUND') return <TicketNotFound />;
    return <QueryErrorState error={query.error} what="工单会话" onRetry={query.reload} />;
  }

  const data = query.data;
  if (!data) return null;

  const ticket = data.ticket;
  const messages = visibleMessages(data.messages);
  const closed = ticket.status === 'closed';

  return (
    <div className="space-y-4">
      <TicketFactsCard ticket={ticket} onClosed={(next) => query.patch((prev) => ({ ...prev, ticket: next }))} />

      <Card>
        <CardTitle hint={`${messages.length} 条`}>会话</CardTitle>
        {messages.length === 0 ? (
          // 正常流程下建单会同时写下第一条消息，所以这个空态几乎不会出现 ——
          // 但「几乎不会」不是「不会」，而空白的一张卡片会让用户以为内容没加载出来。
          <EmptyState
            title="这张工单还没有任何消息"
            description={
              closed
                ? '它在任何人回复之前就已经关闭了。如果问题还在，新建一张工单会更快。'
                : '把遇到的问题写在下面，客服会看到。'
            }
            action={
              closed ? (
                <LinkButton tone="primary" href={newTicketHref(ticket)}>
                  新建工单 <Icon.ArrowRight size={14} />
                </LinkButton>
              ) : (
                <Button tone="primary" onClick={() => focusReplyField()}>
                  去写第一条
                </Button>
              )
            }
          />
        ) : (
          <ul className="space-y-3">
            {messages.map((message) => (
              <MessageBubble key={message.id} message={message} />
            ))}
          </ul>
        )}
      </Card>

      <ContextCard />

      {closed ? (
        <ClosedNotice ticket={ticket} />
      ) : (
        <ReplyForm
          publicId={publicId}
          onSent={(created) => query.patch((prev) => ({ ...prev, messages: [...prev.messages, created] }))}
          onStale={query.reload}
        />
      )}
    </div>
  );
}

/**
 * 🔴 客户端兜底过滤内部备注。
 *
 * 契约里的 `TicketMessage` **根本没有 `is_internal` 字段**（openapi 刻意让它不可表达），
 * 后端也只走 `ticket_messages_public` 视图 —— 也就是说这一行按理永远不会过滤掉任何东西。
 * 保留它的理由：**类型不是运行时保证**。哪天有人给用户面换了一条查询、或者视图被改了，
 * 类型系统一个字都不会说，而渲染出一条内部备注就是一次安全事故。
 *
 * 判据用「真值」而不是 `=== true`：`"true"` / `1` 这类形状也一律丢掉。
 * 误丢一条正常消息，比漏出一条内部备注轻得多 —— 这个不对称是刻意的。
 */
function visibleMessages(messages: readonly TicketMessage[]): TicketMessage[] {
  return messages.filter((message) => !(message as { is_internal?: unknown }).is_internal);
}

function MessageBubble({ message }: { message: TicketMessage }) {
  const fromUser = message.author === 'user';
  return (
    <li className={cx('flex', fromUser ? 'justify-end' : 'justify-start')}>
      <div
        className={cx(
          'max-w-[85%] rounded-lg border px-3 py-2',
          fromUser ? 'border-accent/30 bg-accent/5' : 'border-line bg-surface-alt/60',
        )}
      >
        <div className="mb-1 flex flex-wrap items-baseline gap-x-2 text-xs text-fg-muted">
          <span className="font-medium text-fg">{fromUser ? '你' : '客服'}</span>
          <time dateTime={message.created_at}>{formatDateTime(message.created_at)}</time>
        </div>
        {/* `whitespace-pre-wrap`：工单正文里贴的是客户端原始报错与日志，
            把换行折掉之后那些内容就没法读了。
            正文一律当**纯文本**渲染（React 默认转义），绝不 dangerouslySetInnerHTML ——
            用户与客服两边的正文都是不可信输入。 */}
        <p className="whitespace-pre-wrap break-words text-sm leading-relaxed text-fg">{message.body}</p>
      </div>
    </li>
  );
}

function TicketNotFound() {
  return (
    <EmptyState
      title="找不到这个工单"
      description="工单号可能不对，或者它不属于当前账号。"
      action={
        <LinkButton tone="primary" href="/ticket">
          回到工单列表 <Icon.ArrowRight size={14} />
        </LinkButton>
      }
    />
  );
}

/* ─────────────────────── 工单事实 + 关单 ─────────────────────── */

function TicketFactsCard({ ticket, onClosed }: { ticket: Ticket; onClosed: (next: Ticket) => void }) {
  return (
    <Card>
      <CardTitle hint={<StatusBadge status={ticket.status} />}>工单</CardTitle>
      <dl className="grid gap-x-6 gap-y-2 text-sm sm:grid-cols-2">
        <div className="flex justify-between gap-3 sm:block">
          <dt className="text-fg-muted">分类</dt>
          <dd className="text-fg sm:mt-0.5">{categoryLabel(ticket.category)}</dd>
        </div>
        <div className="flex justify-between gap-3 sm:block">
          <dt className="text-fg-muted">优先级</dt>
          {/* 契约里 `level` 是个 int32，**没有定义数字到档位的对照** ——
              编一张「高 / 中 / 低」出来等于替产品裁决优先级体系。理由同列表页。 */}
          <dd className="text-fg sm:mt-0.5">{ticket.level === undefined ? '—' : `L${ticket.level}`}</dd>
        </div>
        <div className="flex justify-between gap-3 sm:block">
          <dt className="text-fg-muted">创建</dt>
          <dd className="text-fg sm:mt-0.5">{formatDateTime(ticket.created_at)}</dd>
        </div>
        <div className="flex justify-between gap-3 sm:block">
          <dt className="text-fg-muted">最后回复</dt>
          <dd className="text-fg sm:mt-0.5">
            {formatDateTime(ticket.last_reply_at ?? ticket.updated_at ?? ticket.created_at)}
          </dd>
        </div>
      </dl>

      {ticket.status === 'closed' ? null : (
        <div className="mt-4 border-t border-line pt-3">
          <CloseTicketControl publicId={ticket.public_id} onClosed={onClosed} />
        </div>
      )}
    </Card>
  );
}

/**
 * 关单。**二次确认**（api-contract §9 那条纪律的 UI 面）：关单是用户能对自己的工单
 * 做的唯一破坏性动作 —— 关掉之后回复端点直接 409，问题没解决的话只能重开一张。
 *
 * 确认做成**页内展开**而不是 `window.confirm`：后者阻塞主线程、不可样式化、
 * 在移动端浏览器里位置不可控，且屏幕阅读器的播报顺序与页面完全脱节。
 *
 * 幂等：§9.1 的幂等总表里**没有** `POST /api/v1/tickets/{id}/close`，
 * 服务端不认 `Idempotency-Key`，发一个过去只会让代码看起来比实际更安全。
 * 这里只有单飞（`pending` 挡住重复点击）。
 * 好在关单本身是**收敛的**：重复关一次拿到的是 409「该工单已经关闭」，
 * 不是第二次副作用（`handler/ticket.go` 的 `AlreadyClosed` 分支）——
 * 所以缺幂等键在这个动作上的代价是「多一句错误提示」，而不是重复动作。
 */
function CloseTicketControl({ publicId, onClosed }: { publicId: string; onClosed: (next: Ticket) => void }) {
  const [confirming, setConfirming] = useState(false);
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<ApiError | null>(null);
  const countdown = useRetryCountdown();

  async function close(): Promise<void> {
    if (pending || countdown.seconds !== null) return;
    setPending(true);
    setError(null);
    try {
      const next = await postCloseTicket(publicId);
      // 服务端在同一个事务里回了一份完整的 Ticket，直接用它 ——
      // 自己把 status 改成 'closed' 是在猜，而客服可能同时改过别的字段。
      onClosed(next);
      setConfirming(false);
    } catch (cause) {
      const apiError = asApiError(cause, '关单失败');
      setError(apiError);
      countdown.start(apiError.retryAfterSeconds);
    } finally {
      setPending(false);
    }
  }

  if (!confirming) {
    return (
      <div className="flex flex-wrap items-center gap-3">
        <Button onClick={() => setConfirming(true)}>关闭工单</Button>
        <span className="text-xs text-fg-muted">问题已经解决了的话可以关掉它。</span>
      </div>
    );
  }

  return (
    <div className="rounded-lg border border-warn/30 bg-warn/5 p-3">
      <p className="text-sm font-medium text-fg">确定要关闭这张工单吗</p>
      <p className="mt-1 text-sm leading-relaxed text-fg-muted">
        关闭之后<strong className="font-medium text-fg">不能再回复</strong>；问题如果还在，需要新建一张工单。
      </p>

      {error ? (
        <div className="mt-3">
          <FormAlert>
            <span className="font-medium">
              {ticketErrorCopy(error, { fallbackTitle: '关单没能完成', retrySeconds: countdown.seconds }).title}
            </span>
            <br />
            {ticketErrorCopy(error, { fallbackTitle: '关单没能完成', retrySeconds: countdown.seconds }).description}
            {error.requestId ? (
              <>
                <br />
                <span className="font-mono text-xs">请求号 {error.requestId}</span>
              </>
            ) : null}
          </FormAlert>
        </div>
      ) : null}

      <div className="mt-3 flex flex-wrap gap-2">
        <Button tone="danger" onClick={() => void close()} disabled={pending || countdown.seconds !== null}>
          {pending ? '正在关闭…' : countdown.seconds !== null ? `${countdown.seconds} 秒后可再试` : '确认关闭'}
        </Button>
        <Button tone="ghost" onClick={() => setConfirming(false)} disabled={pending}>
          取消
        </Button>
      </div>
    </div>
  );
}

/* ───────────────────────────── 回复 ───────────────────────────── */

function ReplyForm({
  publicId,
  onSent,
  onStale,
}: {
  publicId: string;
  onSent: (created: TicketMessage) => void;
  /** 服务端说状态已经变了（409）时，用它重拉一次会话。 */
  onStale: () => void;
}) {
  const [message, setMessage] = useState('');
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<ApiError | null>(null);
  const countdown = useRetryCountdown();

  async function onSubmit(event: FormEvent<HTMLFormElement>): Promise<void> {
    event.preventDefault();
    // 单飞：**这就是这个写端点当前全部的「幂等」**。
    // api-contract §9.1 的幂等总表里没有 `POST /api/v1/tickets/{id}/messages`，
    // 服务端不认 `Idempotency-Key`。这里老老实实只挡住重复点击，
    // 「超时后重发发出两条回复」这个缺口留在交付说明里，不假装它不存在。
    if (pending || countdown.seconds !== null) return;
    const body = message.trim();
    if (body === '') return;

    setPending(true);
    setError(null);
    try {
      const created = await postTicketMessage(publicId, body);
      onSent(created);
      // 只有**成功**才清空。清空的是已经安全落库的那份。
      setMessage('');
    } catch (cause) {
      const apiError = asApiError(cause, '回复失败');
      setError(apiError);
      countdown.start(apiError.retryAfterSeconds);
      // 🔴 正文一个字都不动（§3.2.6 错态）。用户可能已经打了五分钟，
      // 一次提交失败就清空重来是最招骂的实现。
    } finally {
      setPending(false);
    }
  }

  const copy =
    error === null
      ? null
      : ticketErrorCopy(error, { fallbackTitle: '回复没能发出去', retrySeconds: countdown.seconds });

  return (
    <Card>
      <CardTitle hint="createTicketMessage">回复</CardTitle>
      <form className="space-y-4" onSubmit={(event) => void onSubmit(event)}>
        <TextArea
          label="补充说明"
          name="reply"
          required
          rows={6}
          maxLength={MESSAGE_MAX}
          disabled={pending}
          placeholder="客服问到的信息、你试过的步骤、客户端里的原始报错。"
          value={message}
          onChange={setMessage}
          hint="贴上客户端里的原始报错比描述「还是不行」有用得多。"
        />

        {copy ? (
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
            {/* 409 的处置是「去看最新的会话」，不是「再点一次」——
                客服可能已经关了这张单，重试一百次都是同一个 409。 */}
            {error?.code === 'STATE_CONFLICT' ? (
              <div className="mt-2">
                <Button onClick={onStale}>刷新会话</Button>
              </div>
            ) : null}
          </FormAlert>
        ) : null}

        <div className="flex flex-wrap items-center gap-2">
          <Button
            tone="primary"
            type="submit"
            disabled={pending || countdown.seconds !== null || message.trim() === ''}
          >
            {pending ? '正在发送…' : countdown.seconds !== null ? `${countdown.seconds} 秒后可再试` : '发送回复'}
          </Button>
        </div>

        {/* TODO(P1)：附件上传。契约里 `CreateTicketMessageRequest` 只有 `message` 一个字段，
            也没有任何上传端点 —— 现在放一个上传按钮上去只能是假的。
            要做需要先在 openapi 里开出通道（本轮不改生成物与契约）。 */}
      </form>
    </Card>
  );
}

function ClosedNotice({ ticket }: { ticket: Ticket }) {
  return (
    <Card className="border-dashed">
      <h3 className="text-base font-semibold text-fg">这张工单已经关闭</h3>
      <p className="mt-1.5 text-sm leading-relaxed text-fg-muted">
        已关闭的工单<strong className="font-medium text-fg">不再接收回复</strong>
        （服务端会直接拒绝）。问题如果还在，新建一张工单并附上这个单号{' '}
        <code className="font-mono text-fg">{ticket.public_id}</code>，客服能顺着找到这次的会话。
      </p>
      <div className="mt-3">
        {/* 带上来源与分类：新单的表单会预选同一个分类，并把来源写进正文开头
            （`readTicketOrigin` / `originPreamble`，两页共用同一套来源参数）。 */}
        <LinkButton tone="primary" href={newTicketHref(ticket)}>
          新建工单 <Icon.ArrowRight size={14} />
        </LinkButton>
      </div>
    </Card>
  );
}

/**
 * 建单入口的来源参数。`from` 是**给客服看的一行人话**，不参与任何跳转；
 * `category` 必须是契约里的枚举值（列表页会再校验一次，非法值直接丢弃）。
 */
function newTicketHref(ticket: Ticket): string {
  const params = new URLSearchParams({
    from: `已关闭的工单 ${ticket.public_id}`,
    category: ticket.category,
  });
  return `/ticket?${params.toString()}`;
}

/* ─────────────────────────── 诊断上下文 ─────────────────────────── */

/**
 * 诊断上下文快照。
 *
 * 🔴 **没有接线，也接不了**：用户面的 `TicketDetail`（openapi `schema.d.ts`）只有
 * `ticket` 与 `messages` 两个字段，**没有 `context`** —— 带 `context` 的是后台的
 * `AdminTicketDetail`。也就是说这份快照存在、客服看得见，但用户面的契约里没有出口。
 *
 * 那为什么还留这张卡：§3.2.6 的产品约束是「工单记录的是**报障当时的事实**，
 * 用户事后续费或换节点不应改变这份快照」。这句话本身要让用户看见 ——
 * 否则客服引用一个与用户当前状态对不上的数字时，用户只会觉得客服看错了。
 *
 * TODO(P1)：要真的展示，得先在 openapi 给用户面的 `TicketDetail` 开一个
 * **脱敏后的** `context` 出口（服务端那份含 IP / UA，不能原样回给用户面）。
 * 本轮不改 `openapi/` 与生成物，所以只陈述事实，不编数字。
 */
function ContextCard() {
  return (
    <Card>
      <CardTitle hint="建单时抓的快照，事后不随账号变化而变">诊断上下文</CardTitle>
      <p className="text-sm leading-relaxed text-fg-muted">
        建单时我们记下了你当时的套餐、流量、设备数与最近一次订阅拉取记录，
        <strong className="font-medium text-fg">客服看到的是那一刻的事实</strong>
        —— 你之后续费或者换了节点，都不会改动它。所以客服引用的数字和你现在看到的不一样是正常的。
      </p>
      <p className="mt-2 flex flex-wrap items-center gap-2 text-xs text-fg-subtle">
        <Badge>暂不在本页展示</Badge>
        这份快照里含 IP 与 UA，用户面的契约还没有开出脱敏后的出口。
      </p>
    </Card>
  );
}

/** 空态里的「去写第一条」：把焦点直接放进回复框，省掉一次找输入框的滚动。 */
function focusReplyField(): void {
  const field = document.getElementById(REPLY_FIELD_ID);
  if (field instanceof HTMLTextAreaElement) field.focus();
}

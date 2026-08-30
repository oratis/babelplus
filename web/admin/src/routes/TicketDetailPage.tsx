/**
 * 模块 8 · 工单会话 `/admin/tickets/:id` —— P1 / M2。
 *
 * 🔴 `ticket_messages.is_internal` 是整个系统最容易出安全事故的一列。
 * 后台这边的对应纪律：**内部备注与对用户可见的回复必须在视觉上截然不同**，
 * 不能只差一个小标签。误把内部备注当回复发出去，是这个模块最可能出的事故。
 *
 * 落到代码上是三条，缺一条这层防护就形同虚设：
 *
 *  1. **两个物理上分开的表单**，不是一个开关。开关会被误触，两个各自带正文的表单不会 ——
 *     写在「内部备注」框里的字，在「回复用户」的按钮上根本不存在。
 *  2. **每个表单在按钮正上方常驻一句目标声明**（「这条用户会看到」/「用户看不到」）。
 *     不是提交后的二次确认弹窗：M2 要求手机上能完成回复，多一次点击会被养成肌肉记忆地点掉，
 *     而常驻的那句话每次都在眼睛落向按钮的路径上。
 *  3. **已发出的三种消息三种视觉**（用户 / 客服 / 内部备注），扫读时也不会看错。
 *
 * ⚠️ 路径参数是**数字主键**（`IdPath: number`），不是列表里那个 `BP-…` 工单号 ——
 * 两者在契约里没有换算入口，缺口写在队列页上。这里对非数字的 `:id` **不发请求**，
 * 直接说清楚，免得把一个必然的 400 显示成「这个工单不存在」。
 */
import { useCallback, useMemo, useState, type ReactNode } from 'react';
import { useParams } from 'react-router';
import { ApiError, unwrap, type components } from '@babelplus/shared/api';
import { PageHeader } from '@babelplus/shared/ui';
import { Badge, Button, Card, CardTitle, Icon, LinkButton, Skeleton, cx, formatDateTime } from './_imports.ts';
import { api } from '../lib/api.ts';
import {
  CONTROL,
  Field,
  LevelBadge,
  SETTABLE_STATUSES,
  StatusBadge,
  TICKET_LEVELS,
  TicketQueryErrorState,
  asTicketError,
  categoryLabel,
  formatDuration,
  isNotImplemented,
  levelLabel,
  silenceMs,
  statusLabel,
  useTicketQuery,
  type AdminTicket,
  type TicketStatus,
} from './TicketsPage.tsx';

type AdminTicketDetail = components['schemas']['AdminTicketDetail'];
type AdminTicketMessage = components['schemas']['AdminTicketMessage'];

/** 回复正文上限。与服务端 `ticketMessageMaxRunes`（ticket.go）同一个数，数的是**码位**。 */
const MESSAGE_MAX_RUNES = 20_000;

function runeCount(raw: string): number {
  return [...raw.trim()].length;
}

/* ────────────────────────────── 取数与写入 ────────────────────────────── */

function getAdminTicket(id: number): Promise<AdminTicketDetail> {
  return unwrap(api().GET('/api/v1/admin/tickets/{id}', { params: { path: { id } } }));
}

function postTicketMessage(id: number, message: string, isInternal: boolean): Promise<AdminTicketMessage> {
  return unwrap(
    api().POST('/api/v1/admin/tickets/{id}/messages', {
      params: { path: { id } },
      body: { message, is_internal: isInternal },
    }),
  );
}

function patchTicket(id: number, body: { status?: TicketStatus; level?: number }): Promise<AdminTicket> {
  return unwrap(api().PATCH('/api/v1/admin/tickets/{id}', { params: { path: { id } }, body }));
}

/* ────────────────────────────── 页面 ────────────────────────────── */

export default function TicketDetailPage() {
  const { id: raw } = useParams();
  const id = parseTicketId(raw);

  if (id === null) {
    return <BadIdNotice raw={raw} />;
  }
  return <TicketDetail id={id} />;
}

/** `:id` 必须是正整数。`BP-7K2M9Q` 会被服务端的请求校验层挡下来，那不是「工单不存在」。 */
function parseTicketId(raw: string | undefined): number | null {
  if (raw === undefined || !/^[0-9]+$/.test(raw)) return null;
  const n = Number(raw);
  return Number.isSafeInteger(n) && n > 0 ? n : null;
}

function BadIdNotice({ raw }: { raw: string | undefined }) {
  return (
    <>
      <PageHeader title="工单会话" description="这个地址里的 id 不是会话端点认的那种 id。" />
      <Card className="border-l-4 border-l-warn">
        <p className="text-sm leading-relaxed text-fg">
          地址里的 <code className="font-mono">{raw ?? '（空）'}</code> 不是数字主键。
          会话端点 <code className="font-mono">GET /admin/tickets/{'{id}'}</code> 的路径参数是
          <code className="font-mono"> IdPath: number</code>，而队列列表返回的是对外工单号
          （<code className="font-mono">BP-…</code>）—— 契约里没有从工单号换到数字主键的入口，
          这是一处已登记的缺口。
        </p>
        <p className="mt-2 text-sm leading-relaxed text-fg-muted">
          这里<strong className="font-medium text-fg">没有发请求</strong>：发出去会拿到一个请求校验层的 400，
          而那看起来像「这个工单不存在」——两者的处置完全不同。
        </p>
        <div className="mt-3">
          <LinkButton tone="primary" href="/admin/tickets">
            回到队列 <Icon.ArrowRight size={14} />
          </LinkButton>
        </div>
      </Card>
    </>
  );
}

function TicketDetail({ id }: { id: number }) {
  const query = useTicketQuery(() => getAdminTicket(id), [id], '没能加载工单会话');
  const { data, patch } = query;

  /** 新消息就地追加，**不重拉整段会话**（三态纪律，见 `TicketQuery.patch` 的注释）。 */
  const appendMessage = useCallback(
    (message: AdminTicketMessage) => {
      patch((prev) => ({ ...prev, messages: [...prev.messages, message] }));
    },
    [patch],
  );

  /**
   * 状态 / 等级改完之后就地更新。
   *
   * 🔴 **只取 status / level / updated_at / last_reply_at，不整体替换 `ticket`。**
   * `PATCH /admin/tickets/{id}` 的 RETURNING 里没有 `category_slug`
   * （`AdminUpdateTicket` JOIN 不到分类表），服务端只能给一个 `account` 并记一条 WARN。
   * 整体替换的现象是：把一张「连不上 / 速度」的单改个等级，分类就无声无息变成了「账号本身」，
   * 而页面上没有任何东西表示这是个假值。
   */
  const applyPatched = useCallback(
    (updated: AdminTicket) => {
      patch((prev) => ({
        ...prev,
        ticket: {
          ...prev.ticket,
          status: updated.status,
          ...(updated.level === undefined ? {} : { level: updated.level }),
          ...(updated.updated_at === undefined ? {} : { updated_at: updated.updated_at }),
          ...(updated.last_reply_at === undefined ? {} : { last_reply_at: updated.last_reply_at }),
        },
      }));
    },
    [patch],
  );

  if (query.state === 'loading') {
    return (
      <>
        <PageHeader title="工单会话" description="加载中…" />
        <div className="space-y-3" data-testid="ticket-skeleton">
          <Skeleton className="h-24 w-full" />
          <Skeleton className="h-48 w-full" />
        </div>
      </>
    );
  }

  if (query.state === 'error' && query.error !== null) {
    return (
      <>
        <PageHeader title="工单会话" description={<code className="font-mono text-fg">#{id}</code>} />
        <TicketQueryErrorState
          error={query.error}
          what="工单会话"
          why={
            <>
              <code className="font-mono">getAdminTicket</code> 被摘回了 501。这一条本来是实现了的 ——
              找后端确认，不要当成故障排查。
            </>
          }
          onRetry={query.reload}
        />
      </>
    );
  }

  if (data === null) return null;

  const t = data.ticket;
  const now = Date.now();

  return (
    <>
      <PageHeader
        title={t.subject}
        description={
          <>
            工单 <code className="font-mono text-fg">{t.public_id}</code> · 数字 id{' '}
            <code className="font-mono text-fg">{id}</code>
          </>
        }
        meta={
          <>
            <StatusBadge status={t.status} />
            <LevelBadge level={t.level} />
            <Badge>{categoryLabel(t.category)}</Badge>
          </>
        }
        actions={
          <LinkButton href="/admin/tickets">
            <Icon.ArrowRight size={14} /> 回到队列
          </LinkButton>
        }
      />

      <div className="space-y-4">
        {/* ───────── 只读区 ───────── */}
        <Card>
          <CardTitle hint="只读">概览</CardTitle>
          <dl className="grid gap-x-6 gap-y-3 sm:grid-cols-2 lg:grid-cols-3">
            <Facts label="提单用户">
              <a
                className="font-mono text-sm text-accent underline underline-offset-2"
                href={`/admin/users/${data.user_id}`}
              >
                {data.user_email}
              </a>
            </Facts>
            <Facts label="分类">{categoryLabel(t.category)}</Facts>
            <Facts label="状态">{statusLabel(t.status)}</Facts>
            <Facts label="等级">{levelLabel(t.level)}</Facts>
            <Facts label="建单时间">{formatDateTime(t.created_at)}</Facts>
            <Facts label="最后回复">
              {t.last_reply_at === undefined ? '还没有人回复' : formatDateTime(t.last_reply_at)}
            </Facts>
            <Facts label="静默时长" hint="最后一次有人说话到现在，不是 SLA 剩余时间">
              {t.status === 'closed' ? '—（已关闭）' : formatDuration(silenceMs(t, now))}
            </Facts>
          </dl>
        </Card>

        <Conversation messages={data.messages} />

        <DiagnosticContext context={data.context} />

        {/* ───────── 操作区。与只读区之间**隔一条明显的界线**：
            上面是「发生过什么」，下面是「我要做什么」。 ───────── */}
        <div className="border-t border-line pt-4">
          <h2 className="mb-3 text-sm font-semibold tracking-wide text-fg-muted">处理</h2>
          <div className="space-y-4">
            <ReplyForm id={id} onSent={appendMessage} />
            <InternalNoteForm id={id} onSent={appendMessage} />
            <StatusForm id={id} ticket={t} onPatched={applyPatched} />
          </div>
        </div>
      </div>
    </>
  );
}

function Facts({ label, hint, children }: { label: string; hint?: string; children: ReactNode }) {
  return (
    <div className="min-w-0">
      <dt className="text-xs text-fg-muted">{label}</dt>
      <dd className="mt-0.5 truncate text-sm text-fg">{children}</dd>
      {hint ? <p className="mt-0.5 text-xs text-fg-subtle">{hint}</p> : null}
    </div>
  );
}

/* ────────────────────────────── 会话 ────────────────────────────── */

/**
 * 三种消息三种视觉。
 *
 * 🔴 内部备注**整块换底色 + 左侧竖条 + 明确文字标注**，不是「客服回复 + 一个小标签」。
 * 这里的判据是扫读：一屏十几条消息里，如果内部备注和对外回复只差一个 12px 的标签，
 * 那么在「刚才那条到底发出去没有」这个问题上，人会看错。
 */
function Conversation({ messages }: { messages: readonly AdminTicketMessage[] }) {
  return (
    <Card>
      <CardTitle hint={`${messages.length} 条（含内部备注）`}>会话</CardTitle>
      {messages.length === 0 ? (
        <p className="text-sm text-fg-muted">这张单还没有任何消息。</p>
      ) : (
        <ol className="space-y-3">
          {messages.map((m) => (
            <MessageBubble key={m.id} message={m} />
          ))}
        </ol>
      )}
    </Card>
  );
}

function MessageBubble({ message }: { message: AdminTicketMessage }) {
  const internal = message.is_internal;
  const fromUser = message.author === 'user';

  return (
    <li
      data-testid={internal ? 'message-internal' : fromUser ? 'message-user' : 'message-staff'}
      className={cx(
        'rounded-lg border p-3',
        internal
          ? 'border-warn/40 border-l-4 border-l-warn bg-warn/10'
          : fromUser
            ? 'border-line bg-surface-alt'
            : 'border-accent/30 border-l-4 border-l-accent bg-accent/5',
      )}
    >
      <div className="mb-1 flex flex-wrap items-baseline gap-x-2 gap-y-1">
        <span className="text-sm font-semibold text-fg">
          {internal ? '内部备注' : fromUser ? '用户' : '客服回复'}
        </span>
        {internal ? (
          <span className="rounded bg-warn/20 px-1.5 py-0.5 text-xs font-semibold text-warn">
            用户看不到这一条
          </span>
        ) : fromUser ? null : (
          <span className="rounded bg-accent/15 px-1.5 py-0.5 text-xs font-medium text-accent">用户能看到</span>
        )}
        <span className="ml-auto text-xs text-fg-subtle">{formatDateTime(message.created_at)}</span>
      </div>
      {/* `whitespace-pre-wrap`：正文按 markdown 存（`body_format = 'markdown'`），
          但这里**不渲染 markdown** —— 渲染 HTML 意味着把用户写的东西当代码执行，
          而工单正文正是外部输入。保留换行已经够读了。 */}
      <p className="whitespace-pre-wrap break-words text-sm leading-relaxed text-fg">{message.body}</p>
    </li>
  );
}

/**
 * 建单时的诊断快照。
 *
 * 这是**报障当时的事实**：用户事后续了费、换了节点，这份快照也不该跟着变，
 * 否则复盘时看到的是一个从未存在过的状态。所以它是只读的 JSON，不做任何「实时化」。
 */
function DiagnosticContext({ context }: { context: Record<string, unknown> | undefined }) {
  const text = useMemo(() => {
    if (context === undefined) return null;
    try {
      return JSON.stringify(context, null, 2);
    } catch {
      return '（这条快照无法序列化）';
    }
  }, [context]);

  return (
    <Card>
      <CardTitle hint="建单当时采集，之后不再变化">诊断上下文</CardTitle>
      {text === null ? (
        <p className="text-sm leading-relaxed text-fg-muted">
          这张单没有诊断快照（<code className="font-mono">context</code> 是可选字段）。
          老工单、或从别的渠道转进来的单会是这样 —— 不是加载失败。
        </p>
      ) : (
        <pre className="max-h-80 overflow-auto rounded-lg border border-line bg-surface-alt p-3 font-mono text-xs leading-relaxed text-fg">
          {text}
        </pre>
      )}
    </Card>
  );
}

/* ────────────────────────────── 写路径 ────────────────────────────── */

/**
 * **写**路径的 `ErrorCode` → 文案。与读路径分开：同一个 403，读到是「你看不了」，
 * 写到是「你改不了，去找人开权限」。
 *
 * 🔴 `offline` 这一条是这里最要紧的：一次回复没拿到响应**不等于**没发出去。
 * 直接重发的后果是用户收到两条一模一样的回复，而那看起来像我们这边乱了。
 */
function writeErrorCopy(error: ApiError, action: string): { title: string; description: string } {
  if (isNotImplemented(error)) {
    return { title: '这个操作还没上线', description: '后端这一条还没实现。重试不会有变化。' };
  }
  switch (error.code) {
    case 'AUTH_PERMISSION_DENIED':
      return {
        title: '这个账号不能' + action,
        description: '工单读写由角色决定（owner / admin / support）。身份是通过的，缺的是角色 —— 重新登录没有帮助。',
      };
    case 'RESOURCE_NOT_FOUND':
      return { title: '找不到这个工单', description: '它可能刚被删掉了。回到队列刷新一下。' };
    case 'VALIDATION_FAILED':
    case 'VALIDATION_MALFORMED_BODY':
      return { title: '服务端退回了这次提交', description: fieldReasons(error) ?? error.message };
    case 'QUOTA_RATE_LIMITED':
      return {
        title: '操作太频繁',
        description:
          error.retryAfterSeconds === undefined ? '稍后再试。' : `${error.retryAfterSeconds} 秒后可以再试。`,
      };
    default:
      break;
  }
  switch (error.kind) {
    case 'offline':
      return {
        title: '请求没能到达服务端',
        description: `这次${action}可能已经生效了（响应也可能只是在回程丢了）。刷新页面看一眼再决定，不要直接重试。`,
      };
    case 'server':
      return { title: '服务端出错了', description: '不是你填的内容有问题。稍后再试，并把请求号一起报出来。' };
    default:
      return { title: `没能${action}`, description: error.message };
  }
}

function fieldReasons(error: ApiError): string | null {
  const details = error.details;
  if (!details || details.length === 0) return null;
  return details.map((d) => `${d.field}：${d.reason}`).join('；');
}

function FormAlert({ error, action }: { error: ApiError; action: string }) {
  const copy = writeErrorCopy(error, action);
  return (
    <div role="alert" className="rounded-lg border border-danger/30 bg-danger/10 px-3 py-2 text-sm text-danger">
      <p className="font-medium">{copy.title}</p>
      <p className="mt-0.5 leading-relaxed">{copy.description}</p>
      {error.requestId ? <p className="mt-1 font-mono text-xs opacity-80">请求号 {error.requestId}</p> : null}
    </div>
  );
}

/** 两个回复表单共用的正文框。**状态各自独立** —— 这正是「两个入口」的实现方式。 */
function MessageComposer({
  id,
  label,
  value,
  onChange,
  disabled,
}: {
  id: string;
  label: string;
  value: string;
  onChange: (next: string) => void;
  disabled: boolean;
}) {
  const n = runeCount(value);
  return (
    <Field
      id={id}
      label={label}
      hint={
        <>
          {n} / {MESSAGE_MAX_RUNES} 字
          {n > MESSAGE_MAX_RUNES ? <span className="text-danger">（超出上限，服务端会退回）</span> : null}
        </>
      }
    >
      <textarea
        id={id}
        rows={4}
        value={value}
        disabled={disabled}
        onChange={(e) => onChange(e.target.value)}
        className={cx(CONTROL, 'py-2.5 leading-relaxed')}
      />
    </Field>
  );
}

/** 对用户可见的回复。**独立表单、独立正文、独立按钮。** */
function ReplyForm({ id, onSent }: { id: number; onSent: (message: AdminTicketMessage) => void }) {
  const [value, setValue] = useState('');
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<ApiError | null>(null);

  const n = runeCount(value);
  const blocked = pending || n === 0 || n > MESSAGE_MAX_RUNES;

  async function submit() {
    if (blocked) return;
    setPending(true);
    setError(null);
    try {
      onSent(await postTicketMessage(id, value.trim(), false));
      setValue('');
    } catch (cause) {
      setError(asTicketError(cause, '回复没能发出去'));
    } finally {
      setPending(false);
    }
  }

  return (
    <Card className="border-l-4 border-l-accent">
      <CardTitle hint="用户会在面板上看到，并收到邮件提醒">回复用户</CardTitle>
      <div className="space-y-3">
        <MessageComposer
          id="ticket-reply-body"
          label="回复内容"
          value={value}
          onChange={setValue}
          disabled={pending}
        />
        {error === null ? null : <FormAlert error={error} action="回复" />}
        {/* 目标声明。常驻在按钮正上方 —— 视线落到按钮之前必然经过它。 */}
        <p className="rounded-lg border border-accent/30 bg-accent/10 px-3 py-2 text-sm font-medium text-accent">
          这条<strong className="font-semibold">用户会看到</strong>。发出后无法撤回。
        </p>
        <Button tone="primary" disabled={blocked} aria-disabled={blocked} onClick={() => void submit()}>
          {pending ? '发送中…' : '发送给用户'}
        </Button>
      </div>
    </Card>
  );
}

/** 内部备注。视觉上与上面那个表单**不像同一个东西**，这是刻意的。 */
function InternalNoteForm({ id, onSent }: { id: number; onSent: (message: AdminTicketMessage) => void }) {
  const [value, setValue] = useState('');
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<ApiError | null>(null);

  const n = runeCount(value);
  const blocked = pending || n === 0 || n > MESSAGE_MAX_RUNES;

  async function submit() {
    if (blocked) return;
    setPending(true);
    setError(null);
    try {
      onSent(await postTicketMessage(id, value.trim(), true));
      setValue('');
    } catch (cause) {
      setError(asTicketError(cause, '备注没能保存'));
    } finally {
      setPending(false);
    }
  }

  return (
    <Card className="border-l-4 border-l-warn bg-warn/5">
      <CardTitle hint="只在后台可见；用户面的类型上根本没有这个字段">内部备注</CardTitle>
      <div className="space-y-3">
        <MessageComposer
          id="ticket-note-body"
          label="备注内容"
          value={value}
          onChange={setValue}
          disabled={pending}
        />
        {error === null ? null : <FormAlert error={error} action="保存备注" />}
        <p className="rounded-lg border border-warn/40 bg-warn/15 px-3 py-2 text-sm font-medium text-warn">
          这条<strong className="font-semibold">用户看不到</strong>，也不会触发邮件。
          它仍然会推进工单的最后活动时间。
        </p>
        <Button disabled={blocked} aria-disabled={blocked} onClick={() => void submit()}>
          {pending ? '保存中…' : '保存内部备注'}
        </Button>
      </div>
    </Card>
  );
}

/**
 * 状态与等级。一次 PATCH 同时改两样（服务端就是一条 UPDATE：
 * 写成两次的话，中间那一刻的状态在库里真的存在过，事后看审计会以为有人改了两次）。
 */
function StatusForm({
  id,
  ticket,
  onPatched,
}: {
  id: number;
  ticket: AdminTicket;
  onPatched: (updated: AdminTicket) => void;
}) {
  const currentStatus = ticket.status;
  const currentLevel = ticket.level ?? 2;
  const [status, setStatus] = useState<TicketStatus>(
    // `replied` 是算出来的、写不回去的状态，下拉里没有它 —— 落到 open 上做起点。
    SETTABLE_STATUSES.includes(currentStatus) ? currentStatus : 'open',
  );
  const [level, setLevel] = useState<number>(currentLevel);
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<ApiError | null>(null);
  const [saved, setSaved] = useState(false);

  const statusChanged = status !== currentStatus;
  const levelChanged = level !== currentLevel;
  const nothingChanged = !statusChanged && !levelChanged;

  async function submit() {
    if (pending || nothingChanged) return;
    setPending(true);
    setError(null);
    setSaved(false);
    try {
      const updated = await patchTicket(id, {
        ...(statusChanged ? { status } : {}),
        ...(levelChanged ? { level } : {}),
      });
      onPatched(updated);
      setSaved(true);
    } catch (cause) {
      setError(asTicketError(cause, '没能保存'));
    } finally {
      setPending(false);
    }
  }

  return (
    <Card>
      <CardTitle hint="改动会进审计日志（改前值 / 改后值）">状态与等级</CardTitle>
      <div className="grid gap-3 sm:grid-cols-2">
        <Field
          id="ticket-status"
          label="状态"
          hint={
            <>
              下拉里没有「客服已回复」：库里没有这个状态值，它由「客服最后回复晚于用户最后回复」
              <strong className="font-medium text-fg">算出来</strong>
              （服务端对它回 422）。要标记已处理，用「待用户补充」或「已关闭」。
            </>
          }
        >
          <select
            id="ticket-status"
            value={status}
            disabled={pending}
            onChange={(e) => setStatus(e.target.value as TicketStatus)}
            className={cx(CONTROL, 'min-h-11')}
          >
            {SETTABLE_STATUSES.map((s) => (
              <option key={s} value={s}>
                {statusLabel(s)}
              </option>
            ))}
          </select>
        </Field>
        <Field id="ticket-level" label="等级" hint="1 低 / 2 普通 / 3 高 / 4 紧急，其它值服务端会退回。">
          <select
            id="ticket-level"
            value={String(level)}
            disabled={pending}
            onChange={(e) => setLevel(Number(e.target.value))}
            className={cx(CONTROL, 'min-h-11')}
          >
            {TICKET_LEVELS.map((l) => (
              <option key={l.value} value={String(l.value)}>
                {l.label}
              </option>
            ))}
          </select>
        </Field>
      </div>

      {error === null ? null : (
        <div className="mt-3">
          <FormAlert error={error} action="保存状态" />
        </div>
      )}
      {saved && error === null ? (
        <p className="mt-3 rounded-lg border border-ok/30 bg-ok/10 px-3 py-2 text-sm text-ok" role="status">
          已保存。
        </p>
      ) : null}

      <div className="mt-3 flex flex-wrap items-center gap-3">
        <Button tone="primary" disabled={pending || nothingChanged} aria-disabled={pending || nothingChanged} onClick={() => void submit()}>
          {pending ? '保存中…' : '保存'}
        </Button>
        {nothingChanged ? (
          // 空 PATCH 服务端会 422（「至少要改一个字段」），且它只会写一条 before == after 的审计。
          // 审计表是 append-only 的，别往里塞没有信息的行。
          <p className="text-xs text-fg-subtle">还没有改动。</p>
        ) : null}
      </div>
    </Card>
  );
}

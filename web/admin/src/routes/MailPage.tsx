/**
 * 模块 15 · 邮件与送达 `/admin/mail` —— P2 / M3。
 *
 * 邮件不是「一个通知渠道」，是**唯一的失联恢复通道**（ADR 0002 §1）。
 * 所以这一页的送达统计不是运营指标，是**基础设施健康度**。
 *
 * 🔴 D11b 群发：退信率 ≥ 5% 进入服务商审查、≥ 10% 可能暂停发信。
 * 一次群发翻车 = 失联恢复通道被自己搞坏。
 *
 * # 这一页最容易犯的错，以及代码里对应的防线
 *
 * **把「采集断了」渲染成「指标很好」。** `email_log.delivered_at` 现在**没有任何写入方**
 * （全仓只有读它的地方；写它需要 ESP 的投递回调，而 ESP 一行没接 —— `defaultMailSender`
 * 是 `unconfiguredMailSender`）。于是「已确认送达」恒为 0。
 * 如果照着 `送达数 / 发信数` 算，这一页会安静地显示 **0%**，
 * 而 0% 与「没有回执可算」是两件完全不同的事：前者是灾难，后者是仪表盘没接上。
 * 更坏的方向同理 —— 一个空的分母配上一句「一切正常」。
 * 所以：**整份样本里没有任何一行带送达回执时，送达率显示「尚无数据」，不显示 0%**
 * （见 `deliveryRate`）。有回执之后 0% 才是一个真实且该报警的值。
 *
 * # 三处「做不到」，都用页面上的说明块顶着，不是靠这段注释
 *
 * 1. **模板两个端点是 501**：`mail_templates` 表在 18 支迁移里不存在。所以这里
 *    **不做编辑器** —— 做一个能填的框等于制造「填了没反应」。
 * 2. **群发只有模板键驱动的那一半**：`email_log` 没有正文列，临时写的正文没地方存，
 *    `ClaimQueuedMail` 也取不到，服务端对自定义正文回 501。
 * 3. **收件人数算不出来**：命中数由服务端在事务里算（`AdminCountBroadcastAudience`），
 *    契约里既没有预览端点也没有 `confirmation` 字段。前端能给的只有一个**上界**，
 *    而上界必须被标成上界。
 */
import { useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from 'react';
import { ApiError, unwrap, unwrapWithMeta, type Meta, type components } from '@babelplus/shared/api';
import { PageHeader } from '@babelplus/shared/ui';
import { Badge, Button, Card, CardTitle, ErrorState, Skeleton, cx, formatDateTime } from './_imports.ts';
import { api } from '../lib/api.ts';
import { DangerousAction, type DangerousActionValues } from '../components/DangerousAction.tsx';

/* ────────────────────────── 契约类型（以 schema.d.ts 为准） ────────────────────────── */

type MailTemplate = components['schemas']['MailTemplate'];
type MailLogEntry = components['schemas']['MailLogEntry'];
type MailBroadcastRequest = components['schemas']['MailBroadcastRequest'];
type Audience = MailBroadcastRequest['audience'];
type Plan = components['schemas']['Plan'];

/* ────────────────────────────── 请求三态 ────────────────────────────── */

type QueryState = 'loading' | 'ready' | 'error';

interface MailQuery<T> {
  readonly state: QueryState;
  readonly data: T | null;
  readonly error: ApiError | null;
  reload(): void;
}

/**
 * 一个请求 = 一套三态。
 *
 * ⚠️ 与 `TicketsPage.tsx` 里的那份是**刻意的重复**，不是漏抽。这一页与工单页唯一的共同点
 * 是「都要发请求」；而两边的错误文案、空态、缺口说明没有一句话是一样的。
 * 抽成一份共享 hook 的下一步一定是往里加参数来分叉，那时它就成了一个谁都不敢改的公共文件。
 * （`catalog-common` / `node-common` / `ops-common` 各自也有一份，同一条理由。）
 */
function useMailQuery<T>(
  run: () => Promise<T>,
  deps: readonly unknown[],
  fallbackMessage = '加载失败',
): MailQuery<T> {
  const [nonce, setNonce] = useState(0);
  const [state, setState] = useState<QueryState>('loading');
  const [data, setData] = useState<T | null>(null);
  const [error, setError] = useState<ApiError | null>(null);

  const runRef = useRef(run);
  runRef.current = run;

  useEffect(() => {
    let alive = true;
    setState('loading');
    setError(null);
    void runRef
      .current()
      .then((value) => {
        if (!alive) return;
        setData(value);
        setState('ready');
      })
      .catch((cause: unknown) => {
        if (!alive) return;
        setData(null);
        setError(asApiError(cause, fallbackMessage));
        setState('error');
      });
    return () => {
      alive = false;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [...deps, nonce]);

  const reload = useCallback(() => setNonce((n) => n + 1), []);
  return { state, data, error, reload };
}

function asApiError(cause: unknown, fallbackMessage: string): ApiError {
  if (cause instanceof ApiError) return cause;
  return new ApiError({ status: 0, code: 'UNKNOWN', message: fallbackMessage, cause });
}

/** 501 的判据：`NOT_IMPLEMENTED` 不在 openapi 的 `ErrorCode` enum 里，只能按字符串比。 */
const NOT_IMPLEMENTED_CODE = 'NOT_IMPLEMENTED';

function isNotImplemented(error: ApiError | null | undefined): boolean {
  return error?.code === NOT_IMPLEMENTED_CODE;
}

/** 读路径的 `ErrorCode` → 文案。按 code 分支，不按 HTTP 状态码分支（api-contract §2.3）。 */
function mailErrorCopy(error: ApiError, what: string): { title: string; description: string } {
  switch (error.code) {
    case 'AUTH_PERMISSION_DENIED':
      return {
        title: '这个账号看不了这一块',
        description: '身份是通过的（IAP 认下了你），缺的是角色或权限位。重新登录不会有帮助。',
      };
    case 'VALIDATION_FAILED':
    case 'VALIDATION_MALFORMED_BODY':
      return { title: '查询条件不合法', description: fieldReasons(error) ?? error.message };
    case 'QUOTA_RATE_LIMITED':
      return {
        title: '请求太频繁',
        description:
          error.retryAfterSeconds === undefined ? '稍后再试。' : `${error.retryAfterSeconds} 秒后可以再试。`,
      };
    default:
      break;
  }
  switch (error.kind) {
    case 'offline':
      // 后台站在 IAP 后面，跨域时 IAP 的拒绝与网络不可达在 JS 里分不出来
      // （`shared/api/client.ts` 的 `detectEdgeRejection` 写明了这条限制）。
      return {
        title: '请求没能到达服务端',
        description:
          '可能是网络不通，也可能是 IAP 会话过期后把请求挡在了应用之前（跨域时前端分不出这两者）。先在新标签页打开一次后台确认 IAP 还认你。',
      };
    case 'server':
      return { title: '服务端出错了', description: '不是你的操作有问题。稍后再试，并把请求号一起报出来。' };
    default:
      return { title: `${what}没能加载`, description: error.message };
  }
}

function fieldReasons(error: ApiError): string | null {
  const details = error.details;
  if (!details || details.length === 0) return null;
  return details.map((d) => `${d.field}：${d.reason}`).join('；');
}

/**
 * 501 的说明块。
 *
 * 🔴 `why` 是**必填**。一句光秃秃的「尚未开放」会让人以为是排期问题，
 * 于是每周来点一次看看好了没有。这一页的两条 501 卡的是**表不存在**，
 * 说清楚卡在哪，读的人才知道该去推动什么。
 *
 * 刻意不用 `ErrorState`：501 归一后的 `kind` 是 `server`，那个组件会说「我们这边出了问题」
 * 并把人推去状态页 —— 状态页上一切正常，运维只会更困惑。
 */
function NotImplementedNotice({ what, why, requestId }: { what: string; why: ReactNode; requestId?: string }) {
  return (
    <div data-testid="not-implemented" className="rounded-xl border border-dashed border-line bg-surface-alt/40 p-4">
      <h3 className="text-base font-semibold text-fg">尚未开放</h3>
      <p className="mt-1.5 text-sm leading-relaxed text-fg-muted">
        {what}的后端还没实现（服务端明确回 <code className="font-mono">501 NOT_IMPLEMENTED</code>）。
        这不是故障，重试也不会有变化。
      </p>
      <div className="mt-3 rounded-lg border border-line bg-surface p-3 text-sm leading-relaxed text-fg-muted">
        {why}
      </div>
      {requestId ? <p className="mt-3 font-mono text-xs text-fg-subtle">请求号 {requestId}</p> : null}
    </div>
  );
}

function MailQueryError({ error, what, onRetry }: { error: ApiError; what: string; onRetry?: () => void }) {
  const copy = mailErrorCopy(error, what);
  return (
    <ErrorState
      kind={error.kind}
      title={copy.title}
      description={copy.description}
      requestId={error.requestId}
      onRetry={onRetry}
    />
  );
}

function Caveat({ children, testId }: { children: ReactNode; testId?: string }) {
  return (
    <div
      data-testid={testId}
      className="rounded-lg border border-warn/30 bg-warn/5 p-3 text-xs leading-relaxed text-fg-muted"
    >
      {children}
    </div>
  );
}

const CONTROL =
  'w-full rounded-lg border border-line bg-surface px-3 text-base text-fg ' +
  'placeholder:text-fg-subtle focus-visible:border-accent focus-visible:outline-2 ' +
  'focus-visible:outline-offset-1 focus-visible:outline-accent disabled:opacity-50';

function Field({
  id,
  label,
  hint,
  children,
}: {
  id: string;
  label: string;
  hint?: ReactNode;
  children: ReactNode;
}) {
  return (
    <div className="min-w-0">
      <label htmlFor={id} className="mb-1.5 block text-sm font-medium text-fg">
        {label}
      </label>
      {children}
      {hint ? <p className="mt-1.5 text-xs leading-relaxed text-fg-muted">{hint}</p> : null}
    </div>
  );
}

/* ────────────────────────────── 取数 ────────────────────────────── */

const LOG_PAGE_SIZE = 100;
// 按域名分组的送达率是从**已加载的样本**里算的，样本越小越容易得出一个漂亮但没意义的比值。
// 100 是「一次请求能拿到的最大有效样本」与「db-f1-micro 上一次顺序扫的代价」之间的折中：
// 不带域名过滤时这张表**没有可用索引**（只有 (to_domain, template, created_at DESC)），
// 是一次顺序扫 + 排序，登记在此，别当成走了索引。

interface LogPage {
  readonly items: readonly MailLogEntry[];
  readonly meta: Meta;
}

function listMailLogs(cursor: string | null, domain: string, count: boolean): Promise<LogPage> {
  const query = {
    limit: LOG_PAGE_SIZE,
    ...(cursor === null ? {} : { cursor }),
    ...(count ? { count: true } : {}),
    ...(domain === '' ? {} : { recipient_domain: domain }),
  };
  return unwrapWithMeta(api().GET('/api/v1/admin/mail/logs', { params: { query } })).then((envelope) => ({
    items: envelope.data,
    meta: envelope.meta,
  }));
}

function listMailTemplates(): Promise<MailTemplate[]> {
  return unwrap(api().GET('/api/v1/admin/mail/templates'));
}

function listPlans(): Promise<Plan[]> {
  return unwrap(api().GET('/api/v1/admin/plans'));
}

/** 全库未注销用户数。**只当上界用**，理由见 `BroadcastFacts`。 */
function countUsers(): Promise<number | undefined> {
  return unwrapWithMeta(api().GET('/api/v1/admin/users', { params: { query: { limit: 1, count: true } } })).then(
    (envelope) => envelope.meta.total,
  );
}

/* ────────────────────────────── 页面 ────────────────────────────── */

export default function MailPage() {
  const [domain, setDomain] = useState('');
  const [appliedDomain, setAppliedDomain] = useState('');

  const logs = useMailQuery(
    () => listMailLogs(null, appliedDomain, true),
    [appliedDomain],
    '没能加载邮件日志',
  );

  const [more, setMore] = useState<readonly MailLogEntry[]>([]);
  const [moreMeta, setMoreMeta] = useState<Meta | null>(null);
  const [morePending, setMorePending] = useState(false);
  const [moreError, setMoreError] = useState<ApiError | null>(null);

  // 换筛选条件 = 换样本。后续页必须一起清掉，否则统计里会混进上一个域名的行。
  useEffect(() => {
    setMore([]);
    setMoreMeta(null);
    setMoreError(null);
  }, [appliedDomain]);

  const rows = useMemo(
    () => (logs.data === null ? [] : [...logs.data.items, ...more]),
    [logs.data, more],
  );
  const meta = moreMeta ?? logs.data?.meta ?? null;
  const cursor = meta?.next_cursor ?? null;

  const loadMore = useCallback(async () => {
    if (morePending || cursor === null) return;
    setMorePending(true);
    setMoreError(null);
    try {
      const page = await listMailLogs(cursor, appliedDomain, false);
      setMore((prev) => [...prev, ...page.items]);
      setMoreMeta(page.meta);
    } catch (cause) {
      setMoreError(asApiError(cause, '没能加载更多日志'));
    } finally {
      setMorePending(false);
    }
  }, [appliedDomain, cursor, morePending]);

  const reloadLogs = useCallback(() => {
    setMore([]);
    setMoreMeta(null);
    setMoreError(null);
    logs.reload();
  }, [logs]);

  return (
    <>
      <PageHeader
        title="邮件与送达"
        description="模板、发送日志、按收件域名的送达统计、群发。这一页看的是生命线的健康度，不是运营指标。"
        meta={
          <>
            <Badge>P2</Badge>
            <Badge>M3 · 桌面优先</Badge>
            <Badge tone="danger">D11b 群发</Badge>
          </>
        }
      />

      <div className="space-y-4">
        <EspNotice rows={rows} ready={logs.state === 'ready'} />
        <TemplatesCard />
        <DeliveryCard
          rows={rows}
          state={logs.state}
          error={logs.error}
          total={meta?.total}
          canLoadMore={cursor !== null}
          morePending={morePending}
          moreError={moreError}
          domain={domain}
          appliedDomain={appliedDomain}
          onDomainChange={setDomain}
          onApplyDomain={() => setAppliedDomain(domain.trim().toLowerCase())}
          onLoadMore={() => void loadMore()}
          onRetry={reloadLogs}
        />
        <BroadcastCard onSent={reloadLogs} />
      </div>
    </>
  );
}

/**
 * 「有没有真的接上一家 ESP」。
 *
 * 数据驱动而不是写死一句话：`email_log.esp` 存的是**发信当时准备用谁发**
 * （`MailSender.Name()`，未配置时是字面量 `unconfigured`）。
 * 全样本都是 `unconfigured` 就意味着这些信一封都没真的发出去 ——
 * 这句话必须出现在送达统计**上面**，否则下面那张表会被当成「发得挺好」。
 * 写死的话，将来真接上了 ESP 它还会继续喊，然后被所有人忽略。
 */
function EspNotice({ rows, ready }: { rows: readonly MailLogEntry[]; ready: boolean }) {
  if (!ready || rows.length === 0) return null;
  const espValues = new Set(rows.map((r) => r.esp ?? 'unconfigured'));
  const allUnconfigured = espValues.size === 1 && espValues.has('unconfigured');
  if (!allUnconfigured) return null;

  return (
    <Caveat testId="esp-unconfigured">
      <p className="text-sm font-semibold text-fg">还没有接任何一家 ESP。</p>
      <p className="mt-1">
        已加载的 {rows.length} 条日志里，<code className="font-mono">esp</code> 全部是{' '}
        <code className="font-mono">unconfigured</code> —— 这是「发信当时准备用谁发」这一列的未配置取值。
        也就是说这些信都只是<strong className="font-medium text-fg">入了队</strong>，投递任务（<code className="font-mono">mail-send</code>）真正发信时会失败。
        在接上 ESP 之前，下面的任何送达数字都不是「用户收没收到」的答案。
      </p>
    </Caveat>
  );
}

/* ────────────────────────────── 模板 ────────────────────────────── */

function TemplatesCard() {
  const query = useMailQuery(() => listMailTemplates(), [], '没能加载邮件模板');

  return (
    <Card>
      <CardTitle hint="listAdminMailTemplates · updateAdminMailTemplate">模板</CardTitle>

      {query.state === 'loading' ? (
        <Skeleton className="h-20 w-full" />
      ) : query.state === 'error' && query.error !== null ? (
        isNotImplemented(query.error) ? (
          <NotImplementedNotice
            what="邮件模板"
            requestId={query.error.requestId}
            why={
              <>
                <p>
                  缺的是一张表：<code className="font-mono">mail_templates</code> 在 18 支迁移里<strong className="font-medium text-fg">不存在</strong>
                  （<code className="font-mono">CREATE TABLE</code> 全仓核过一遍）。契约的{' '}
                  <code className="font-mono">MailTemplate {'{ id, key, subject, body, enabled }'}</code>{' '}
                  没有落点 —— <code className="font-mono">email_log.template</code> 只是一个<strong className="font-medium text-fg">模板键的字符串快照</strong>
                  （<code className="font-mono">verify_code</code> /{' '}
                  <code className="font-mono">domain_broadcast</code> /{' '}
                  <code className="font-mono">expire_remind</code>），不是模板正文的存储。
                </p>
                <p className="mt-2">
                  所以这里<strong className="font-medium text-fg">连编辑器都不做</strong>：
                  <code className="font-mono">updateAdminMailTemplate</code> 同样是 501，
                  做一个能填能点的框只会制造「填了没反应」，而那比一句「还没开放」更难排查。
                </p>
                <p className="mt-2">
                  补法有裁决过的方向：<strong className="font-medium text-fg">不要</strong>把模板塞进 <code className="font-mono">settings</code> 的 JSONB
                  再假装它是表 —— <code className="font-mono">MailTemplatePatch</code> 要求改前后像进审计，
                  而 JSONB 里的部分更新拿不到干净的字段级快照。
                </p>
              </>
            }
          />
        ) : (
          <MailQueryError error={query.error} what="邮件模板" onRetry={query.reload} />
        )
      ) : query.data === null || query.data.length === 0 ? (
        <p className="text-sm text-fg-muted">服务端返回了 0 个模板。</p>
      ) : (
        <>
          <ul className="divide-y divide-line">
            {query.data.map((t) => (
              <li key={t.id} className="grid gap-1 py-2 text-sm md:grid-cols-[10rem_minmax(0,1fr)_5rem]">
                <code className="font-mono text-xs text-fg">{t.key}</code>
                <span className="min-w-0 truncate text-fg">{t.subject}</span>
                <span className="text-xs text-fg-muted">{t.enabled === false ? '已停用' : '启用中'}</span>
              </li>
            ))}
          </ul>
          <p className="mt-3 text-xs leading-relaxed text-fg-muted">
            只读。编辑走 <code className="font-mono">updateAdminMailTemplate</code>，那一条还是 501。
          </p>
        </>
      )}
    </Card>
  );
}

/* ────────────────────────────── 送达 ────────────────────────────── */

interface DomainStat {
  readonly domain: string;
  readonly sent: number;
  readonly delivered: number;
  readonly failed: number;
}

/** 按收件域名分组。ADR 0002 §7 要的就是这张表：总体 95% 会掩盖「QQ 邮箱 40%」。 */
function groupByDomain(rows: readonly MailLogEntry[]): DomainStat[] {
  const map = new Map<string, { sent: number; delivered: number; failed: number }>();
  for (const r of rows) {
    const key = r.recipient_domain === '' ? '（空）' : r.recipient_domain;
    const cur = map.get(key) ?? { sent: 0, delivered: 0, failed: 0 };
    cur.sent += 1;
    if (r.delivered_at !== undefined) cur.delivered += 1;
    if (r.bounce_code !== undefined && r.bounce_code !== '') cur.failed += 1;
    map.set(key, cur);
  }
  return [...map.entries()]
    .map(([domain, v]) => ({ domain, ...v }))
    .sort((a, b) => b.sent - a.sent || a.domain.localeCompare(b.domain));
}

/**
 * 送达率。
 *
 * 🔴 `hasSignal = false`（整份样本里一行送达回执都没有）时返回 `null`，调用方渲染「尚无数据」。
 * **不返回 0**：`delivered_at` 目前没有任何写入方，0% 在这里的含义是「没接上回执」而不是
 * 「一封都没送到」。把前者显示成后者，是这一页最坏的失败模式的另一半 ——
 * 有人会照着这个 0% 去换 ESP，而问题根本不在 ESP。
 */
function deliveryRate(stat: DomainStat, hasSignal: boolean): number | null {
  if (!hasSignal || stat.sent === 0) return null;
  return stat.delivered / stat.sent;
}

function pct(value: number): string {
  return `${(value * 100).toFixed(1)}%`;
}

function DeliveryCard({
  rows,
  state,
  error,
  total,
  canLoadMore,
  morePending,
  moreError,
  domain,
  appliedDomain,
  onDomainChange,
  onApplyDomain,
  onLoadMore,
  onRetry,
}: {
  rows: readonly MailLogEntry[];
  state: QueryState;
  error: ApiError | null;
  total: number | undefined;
  canLoadMore: boolean;
  morePending: boolean;
  moreError: ApiError | null;
  domain: string;
  appliedDomain: string;
  onDomainChange: (next: string) => void;
  onApplyDomain: () => void;
  onLoadMore: () => void;
  onRetry: () => void;
}) {
  const stats = useMemo(() => groupByDomain(rows), [rows]);
  // 「有没有回执这回事」是**整份样本**的性质，不是单个域名的：
  // 逐域名判会让一个恰好有一条回执的域名显示 100%，而它旁边的域名显示「尚无数据」。
  const hasDeliverySignal = rows.some((r) => r.delivered_at !== undefined);

  return (
    <Card>
      <CardTitle hint="基础设施健康度，不是运营指标">送达</CardTitle>

      <div className="mb-4 flex flex-col gap-3 sm:flex-row sm:items-end">
        <div className="min-w-0 flex-1">
          <Field
            id="mail-domain"
            label="按收件域名过滤"
            hint="服务端过滤（列表与总数都跟着变）。留空看全部。大小写不敏感：入库时已经 lower 过。"
          >
            <input
              id="mail-domain"
              value={domain}
              placeholder="qq.com"
              autoComplete="off"
              spellCheck={false}
              onChange={(e) => onDomainChange(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === 'Enter') onApplyDomain();
              }}
              className={cx(CONTROL, 'min-h-11 font-mono')}
            />
          </Field>
        </div>
        <Button onClick={onApplyDomain}>应用</Button>
      </div>

      {state === 'loading' ? (
        <div className="space-y-2" data-testid="logs-skeleton">
          {Array.from({ length: 4 }, (_, i) => (
            <Skeleton key={i} className="h-10 w-full" />
          ))}
        </div>
      ) : state === 'error' && error !== null ? (
        <MailQueryError error={error} what="邮件日志" onRetry={onRetry} />
      ) : rows.length === 0 ? (
        <div>
          <p className="text-sm font-medium text-fg">
            {appliedDomain === '' ? '还没有发过任何邮件。' : `没有发往 ${appliedDomain} 的邮件记录。`}
          </p>
          <p className="mt-1.5 text-sm leading-relaxed text-fg-muted">
            注册验证码是第一个会用到邮件的流程，它同时也是送达率的免费持续采样 ——
            这里空着通常意味着还没有人走完注册，而不是统计坏了。
          </p>
        </div>
      ) : (
        <>
          <div className="space-y-3">
            <Caveat testId="delivery-caveat">
              <p className="font-medium text-fg">这张表的口径，读之前必须知道：</p>
              <ul className="mt-1.5 list-disc space-y-1 pl-4">
                <li>
                  <strong className="font-medium text-fg">样本是已加载的 {rows.length} 条日志</strong>
                  {total === undefined ? '' : `（符合条件的共 ${total.toLocaleString('zh-CN')} 条）`}
                  ，不是全库聚合 —— 契约里没有聚合端点，这个比值是浏览器按分页拿到的样本算的。
                  样本太小时它没有意义。
                </li>
                <li>
                  {hasDeliverySignal ? (
                    <>
                      <strong className="font-medium text-fg">送达以回执为准。</strong>
                      有回执的行才计入「已确认送达」，没有回执不等于没送到。
                    </>
                  ) : (
                    <>
                      <strong className="font-medium text-fg">
                        送达率现在是「尚无数据」，不是 0%。
                      </strong>{' '}
                      样本里没有任何一行带送达回执（<code className="font-mono">delivered_at</code>），
                      而这一列**没有任何写入方** —— 它要靠 ESP 的投递回调，那部分一行没接。
                      所以这里显示 <code className="font-mono">—</code>：把「采集断了」写成 0% 会让人去查 ESP，
                      写成「一切正常」则更糟。
                    </>
                  )}
                </li>
                <li>
                  <strong className="font-medium text-fg">「失败」不是「退信」。</strong>
                  这一列数的是 <code className="font-mono">bounce_code</code> 非空的行，
                  而现在唯一会写它的是本地发信失败（<code className="font-mono">MarkMailSendFailed</code>）。
                  D11b 说的「退信率 ≥ 5% 进入审查、≥ 10% 可能暂停发信」是 **ESP 侧**的硬退比例，
                  <strong className="font-medium text-fg">我们现在测不到它</strong>。
                </li>
              </ul>
            </Caveat>

            <div className="overflow-x-auto">
              <table className="w-full min-w-[36rem] text-sm">
                <thead>
                  <tr className="border-b border-line text-left text-xs text-fg-muted">
                    <th className="py-2 font-medium">收件域名</th>
                    <th className="py-2 font-medium">样本</th>
                    <th className="py-2 font-medium">已确认送达</th>
                    <th className="py-2 font-medium">送达率</th>
                    <th className="py-2 font-medium">发信失败</th>
                  </tr>
                </thead>
                <tbody className="divide-y divide-line">
                  {stats.map((s) => {
                    const rate = deliveryRate(s, hasDeliverySignal);
                    return (
                      <tr key={s.domain} data-testid="domain-stat">
                        <td className="py-2 font-mono text-xs text-fg">{s.domain}</td>
                        <td className="py-2 text-fg-muted">{s.sent}</td>
                        <td className="py-2 text-fg-muted">{s.delivered}</td>
                        <td className="py-2">
                          {rate === null ? (
                            <span className="text-fg-subtle" title="没有任何送达回执可算">
                              尚无数据
                            </span>
                          ) : (
                            <span className={rate < 0.9 ? 'font-medium text-danger' : 'text-fg'}>{pct(rate)}</span>
                          )}
                        </td>
                        <td className="py-2 text-fg-muted">{s.failed}</td>
                      </tr>
                    );
                  })}
                </tbody>
              </table>
            </div>
          </div>

          <details className="mt-4">
            <summary className="cursor-pointer text-sm font-medium text-fg-muted">
              明细（{rows.length} 条）
            </summary>
            <div className="mt-2 max-h-96 overflow-auto">
              <table className="w-full min-w-[40rem] text-xs">
                <thead>
                  <tr className="border-b border-line text-left text-fg-muted">
                    <th className="py-1.5 font-medium">收件域名</th>
                    <th className="py-1.5 font-medium">模板</th>
                    <th className="py-1.5 font-medium">ESP</th>
                    <th className="py-1.5 font-medium">发出</th>
                    <th className="py-1.5 font-medium">送达回执</th>
                    <th className="py-1.5 font-medium">失败码</th>
                  </tr>
                </thead>
                <tbody className="divide-y divide-line">
                  {rows.map((r) => (
                    <tr key={r.id} data-testid="log-row">
                      <td className="py-1.5 font-mono text-fg">{r.recipient_domain}</td>
                      <td className="py-1.5 font-mono text-fg-muted">{r.template_key ?? '—'}</td>
                      <td className="py-1.5 font-mono text-fg-muted">{r.esp ?? '—'}</td>
                      {/* `sent_at` 在服务端会回落到 `created_at`（'queued' 的信这一列是 NULL），
                          所以这一格的含义是「入队或发出的时刻」，不能读成「一定发出去了」。 */}
                      <td className="py-1.5 text-fg-muted">{formatDateTime(r.sent_at)}</td>
                      <td className="py-1.5 text-fg-muted">
                        {r.delivered_at === undefined ? '—' : formatDateTime(r.delivered_at)}
                      </td>
                      <td className="py-1.5 font-mono text-fg-muted">{r.bounce_code ?? '—'}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          </details>

          <div className="mt-4 flex flex-wrap items-center gap-3">
            {canLoadMore ? (
              <Button disabled={morePending} onClick={onLoadMore}>
                {morePending ? '加载中…' : '加载更多'}
              </Button>
            ) : (
              <p className="text-xs text-fg-subtle">已经到最后一页了。</p>
            )}
            {total === undefined ? null : (
              <p className="text-xs text-fg-subtle">
                已加载 {rows.length} / 共 {total.toLocaleString('zh-CN')} 条
              </p>
            )}
          </div>

          {moreError === null ? null : (
            <div className="mt-3">
              <MailQueryError error={moreError} what="下一页日志" onRetry={onLoadMore} />
            </div>
          )}
        </>
      )}
    </Card>
  );
}

/* ────────────────────────────── 群发（D11b） ────────────────────────────── */

/**
 * 🔴 **唯一允许群发的模板键**，与服务端的白名单（`broadcastTemplates`）一致。
 *
 * 白名单不是黑名单，方向是刻意的：将来新增模板键时默认**发不出去**，需要有人显式加进来。
 * 这个方向的失败是「发不出去」，反过来是「发错了」。
 *
 * 另外几个键被挡住的理由各不相同，写在这里是因为总有人会问「为什么不能群发到期提醒」：
 *  · `verify_code` / `password_reset` —— 群发等于给全站用户各发一封凭据邮件，
 *    而投递任务会真的去渲染并发出。那是一次自制的钓鱼活动。
 *  · `expire_remind` / `traffic_remind` —— 幂等键是 `(user_id, template, 当天)`，
 *    手工群发一次会吃掉当天的提醒配额，于是真正该收到到期提醒的人当天收不到了。
 */
const BROADCAST_TEMPLATE = 'domain_broadcast';

/** 主题上限，与服务端 `broadcastSubjectMaxRunes` 同一个数。数的是码位。 */
const SUBJECT_MAX_RUNES = 200;

/** 群发限流，契约 summary 逐字写着「限流 2/h」。 */
const BROADCAST_PER_HOUR = 2;

/** `expiring_soon` 的窗口天数。契约没有这个参数，由服务端钉死，所以这里只能誊一份。 */
const EXPIRING_WITHIN_DAYS = 7;

const AUDIENCES: ReadonlyArray<{ readonly value: Audience; readonly label: string; readonly desc: string }> = [
  { value: 'all', label: '全部用户', desc: '所有未注销的用户' },
  { value: 'active', label: '有效订阅', desc: '有套餐、未封禁、且未过期' },
  { value: 'expired', label: '已过期', desc: '到期时间已经过去的用户' },
  {
    value: 'expiring_soon',
    label: '即将到期',
    desc: `${EXPIRING_WITHIN_DAYS} 天内到期（窗口由服务端钉死，契约里没有这个参数）`,
  },
  { value: 'by_plan', label: '按套餐', desc: '当前正挂在选中套餐上的用户' },
];

function audienceLabel(a: Audience): string {
  return AUDIENCES.find((x) => x.value === a)?.label ?? a;
}

function subjectRunes(raw: string): number {
  return [...raw.trim()].length;
}

function BroadcastCard({ onSent }: { onSent: () => void }) {
  const [subject, setSubject] = useState('');
  const [audience, setAudience] = useState<Audience>('all');
  const [planIds, setPlanIds] = useState<readonly number[]>([]);
  const [queued, setQueued] = useState<number | null>(null);

  const needPlans = audience === 'by_plan';
  const plans = useMailQuery<Plan[] | null>(
    () => (needPlans ? listPlans() : Promise.resolve(null)),
    [needPlans],
    '没能加载套餐列表',
  );

  const subjectLen = subjectRunes(subject);
  const subjectBad = subjectLen === 0 || subjectLen > SUBJECT_MAX_RUNES;
  const planBad = needPlans && planIds.length === 0;
  const blocked = subjectBad || planBad;

  async function send(values: DangerousActionValues): Promise<void> {
    const body: MailBroadcastRequest = {
      subject: subject.trim(),
      // 🔴 `body` 在契约里叫「正文」，但服务端把它当**模板键**读：
      //    不在白名单里的值一律 501（`email_log` 没有正文列，投递任务也取不到正文）。
      //    所以这里送出去的是一个键，不是一段话 —— 上面的下拉框就是这个事实的界面形态。
      body: BROADCAST_TEMPLATE,
      audience,
      ...(needPlans ? { plan_ids: [...planIds] } : {}),
      // requireReason 打开了，所以这里必然有值；服务端要求 ≥ 8 码位。
      reason: values.reason ?? '',
    };
    const result = await unwrap(api().POST('/api/v1/admin/mail/broadcast', { body }));
    setQueued(result.queued);
  }

  return (
    <Card>
      <CardTitle hint="D11b · 不可撤回">群发</CardTitle>

      <div className="space-y-4">
        <Caveat testId="broadcast-half">
          <p className="font-medium text-fg">这里只有「模板键驱动」的那一半群发。</p>
          <p className="mt-1">
            <strong className="font-medium text-fg">写一封临时正文发出去做不到</strong>：
            <code className="font-mono">email_log</code> 没有正文列，投递任务的取件查询也取不到正文，
            服务端对自定义正文明确回 <code className="font-mono">501</code>。
            退化成「把正文丢掉、按某个默认模板发」是最坏的选择 ——
            管理员写了一封信、系统发出去的是另一封，而两边都显示成功。所以这里连正文框都不给。
          </p>
        </Caveat>

        <div className="grid gap-3 sm:grid-cols-2">
          <Field
            id="broadcast-template"
            label="模板"
            hint="白名单里目前只有这一个。别的键会被服务端回 501，理由见代码注释（群发验证码 = 自制钓鱼；群发到期提醒会吃掉当天的幂等配额）。"
          >
            <select id="broadcast-template" value={BROADCAST_TEMPLATE} disabled className={cx(CONTROL, 'min-h-11')}>
              <option value={BROADCAST_TEMPLATE}>域名广播（domain_broadcast）</option>
            </select>
          </Field>

          <Field
            id="broadcast-audience"
            label="收件范围"
            hint={AUDIENCES.find((a) => a.value === audience)?.desc}
          >
            <select
              id="broadcast-audience"
              value={audience}
              onChange={(e) => {
                setAudience(e.target.value as Audience);
                setQueued(null);
              }}
              className={cx(CONTROL, 'min-h-11')}
            >
              {AUDIENCES.map((a) => (
                <option key={a.value} value={a.value}>
                  {a.label}
                </option>
              ))}
            </select>
          </Field>
        </div>

        <Field
          id="broadcast-subject"
          label="邮件主题"
          hint={
            <>
              {subjectLen} / {SUBJECT_MAX_RUNES} 字
              {subjectLen > SUBJECT_MAX_RUNES ? (
                <span className="text-danger">（超出上限，服务端会退回）</span>
              ) : null}
              。它会进每一条收件记录，一次一千人的群发就是一千份。
            </>
          }
        >
          <input
            id="broadcast-subject"
            value={subject}
            autoComplete="off"
            onChange={(e) => {
              setSubject(e.target.value);
              setQueued(null);
            }}
            className={cx(CONTROL, 'min-h-11')}
          />
        </Field>

        {needPlans ? <PlanPicker query={plans} selected={planIds} onChange={setPlanIds} /> : null}

        {queued === null ? null : (
          <div role="status" className="rounded-lg border border-ok/30 bg-ok/10 px-3 py-2 text-sm text-ok">
            <p className="font-medium">已入队 {queued.toLocaleString('zh-CN')} 封。</p>
            <p className="mt-0.5 leading-relaxed">
              入队<strong className="font-semibold">不等于</strong>送达：真正发信在{' '}
              <code className="font-mono">mail-send</code> 任务里，成败要回到上面的日志里看。
              这个数字是入队语句的实际行数，可能与确认时的预估差几封（中间隔着你点确认的几秒）。
            </p>
          </div>
        )}

        <DangerousAction
          code="D11b"
          title="群发邮件"
          submitLabel="确认群发"
          // 🔴 登记表（page-inventory §4.4）里 D11b 那一行**没有**「必填原因」这一列，
          //    但契约的 `MailBroadcastRequest.reason` 是 required，服务端还要求 ≥ 8 码位。
          //    不显式打开的话，表单不会收 reason，服务端必然 422 —— 而那个 422 说的是
          //    「原因太短」，操作者会去找一个页面上根本不存在的输入框。
          requireReason
          disabled={blocked}
          disabledReason={
            subjectLen === 0
              ? '还没有填邮件主题。'
              : subjectLen > SUBJECT_MAX_RUNES
                ? `主题超过 ${SUBJECT_MAX_RUNES} 字，服务端会退回。`
                : '选了「按套餐」就必须至少选中一个套餐 —— 一个都不选时服务端命中 0 人并退回 422。'
          }
          context={<BroadcastFacts audience={audience} planCount={planIds.length} />}
          onSubmit={send}
          onDone={onSent}
        />
      </div>
    </Card>
  );
}

function PlanPicker({
  query,
  selected,
  onChange,
}: {
  query: MailQuery<Plan[] | null>;
  selected: readonly number[];
  onChange: (next: readonly number[]) => void;
}) {
  if (query.state === 'loading') return <Skeleton className="h-16 w-full" />;
  if (query.state === 'error' && query.error !== null) {
    return <MailQueryError error={query.error} what="套餐列表" onRetry={query.reload} />;
  }
  const plans = query.data ?? [];
  if (plans.length === 0) {
    return <p className="text-sm text-fg-muted">一个套餐都没有，「按套餐」这个范围现在选不出人。</p>;
  }
  return (
    <fieldset>
      <legend className="mb-1.5 text-sm font-medium text-fg">套餐（可多选）</legend>
      <div className="flex flex-wrap gap-2">
        {plans.map((p) => {
          const checked = selected.includes(p.id);
          return (
            <label
              key={p.id}
              className={cx(
                'inline-flex min-h-11 cursor-pointer items-center gap-2 rounded-lg border px-3 text-sm',
                checked ? 'border-accent bg-accent/10 text-accent' : 'border-line bg-surface text-fg',
              )}
            >
              <input
                type="checkbox"
                checked={checked}
                onChange={() =>
                  onChange(checked ? selected.filter((id) => id !== p.id) : [...selected, p.id])
                }
              />
              {p.name}
            </label>
          );
        })}
      </div>
    </fieldset>
  );
}

/**
 * 确认框里必须让操作者看见的事实（D11b 的登记要求原文是「确认框显示收件人数」）。
 *
 * 🔴 **收件人数在这里是拿不到的，所以这里不给一个数字冒充它。**
 * 命中数由服务端在事务里用 `AdminCountBroadcastAudience` 算，与入队语句的 WHERE 逐字同形；
 * 契约既没有预览端点，也没有 `confirmation` 字段能把那个数字拿回来跟人对质。
 * 能给的只有一个**上界**（全库未注销用户数），而上界必须被标成上界 ——
 * 在一个写着「收件人数」的位置放一个更大的数，比不放更危险。
 */
function BroadcastFacts({ audience, planCount }: { audience: Audience; planCount: number }) {
  const users = useMailQuery(() => countUsers(), [], '没能取到用户总数');

  return (
    <div className="space-y-2 text-sm leading-relaxed">
      <p>
        收件范围：<strong className="font-semibold text-fg">{audienceLabel(audience)}</strong>
        {audience === 'by_plan' ? `（已选 ${planCount} 个套餐）` : null}。
      </p>

      <p data-testid="recipient-upper-bound">
        <strong className="font-semibold text-fg">这一步算不出确切收件人数。</strong>{' '}
        命中数由服务端在事务里算（与入队用的是同一条 WHERE），契约里没有预览端点。
        可以作为量级参考的只有上界：全库未注销用户{' '}
        {users.state === 'loading' ? (
          <span className="text-fg-subtle">读取中…</span>
        ) : users.data === undefined || users.data === null ? (
          <span className="text-fg-subtle">取不到（这不影响群发，只是没有量级参考）</span>
        ) : (
          <strong className="font-mono font-semibold text-fg">{users.data.toLocaleString('zh-CN')}</strong>
        )}{' '}
        人。<strong className="font-semibold text-fg">这不是本次的收件人数</strong>，
        它没有算上收件范围，也没有排除已经硬退过的地址。
      </p>

      <ul className="list-disc space-y-1 pl-4 text-fg-muted">
        <li>
          命中 0 人时服务端<strong className="font-medium text-fg">回 422 而不是「发了 0 封」</strong> ——
          筛选没选中任何人正是这个数字要防的意外。
        </li>
        <li>
          已注销用户与<strong className="font-medium text-fg">已硬退过的地址会被排除</strong>：
          往它们发信是保证退信的，而退信率是发信资格的生死线。
        </li>
        <li>
          频率上限 <strong className="font-medium text-fg">{BROADCAST_PER_HOUR} 次/小时</strong>，
          超了会拿到 429。
        </li>
        <li>
          ⚠️ 登记表要求的「<strong className="font-medium text-fg">强制先发测试件</strong>」
          <strong className="font-medium text-fg">没有实现</strong>：
          <code className="font-mono">MailBroadcastRequest</code>{' '}
          里没有任何字段能表达「这是一次测试件」或「我已经发过测试件了」。
          在补上之前，这一条只能靠人自己遵守 —— 先给自己发一封，确认渲染没问题再群发。
        </li>
        <li>
          ⚠️ 这个数据模型<strong className="font-medium text-fg">无法尊重运营邮件的退订意愿</strong>：
          <code className="font-mono">users</code>{' '}
          只有到期与流量两个通知开关，没有「全部通知」总开关，也没有{' '}
          <code className="font-mono">notify_broadcast</code>。
        </li>
      </ul>
    </div>
  );
}

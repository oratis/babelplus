/**
 * 模块 10 · 审计日志 `/admin/audit` —— P1 / M3。
 *
 * 🔴 **append-only，无删除入口，无编辑入口。** 后台前端不提供，API 也不提供。
 * 「一个能被清理的审计日志等于没有审计日志。」
 *
 * 所以这一页是全后台唯一一个**没有任何写操作**的模块。
 * 如果将来有人在这里加了一个「清理 90 天前的日志」按钮，那就是这条纪律被破坏的时刻 ——
 * 留存策略应该在数据库侧做，且要有独立于后台的审批。
 *
 * # 🔴 `?action=` 是包含匹配，不是等值
 *
 * 库里的 `action` **带 D 编号前缀**（`D6.order.mark_paid`），而契约的参数说明举的例子
 * 不带（`order.mark_paid`）。服务端 `auditActionFilter` 因此把它翻成
 * `ILIKE '%…%'`。前端要做的事只有一件：**把用户填的串原样发出去**。
 *
 * 反过来的错误写法是「前端自己按 `entry.action === filter` 再过滤一遍」——
 * 那样一条都不会命中，**而且不报错**：页面显示「审计日志是空的」，
 * 而这块屏幕正是有人会拿来证明「没人动过」的那块。
 * 这条由 `AuditPage.test.tsx` 的第一个用例钉住。
 *
 * # 服务端没有的三个筛选维度
 *
 * 契约给 `listAdminAuditLog` 的查询参数只有 `limit / cursor / count / action / target_type`。
 * **没有 `admin_id`、没有 `target_id`、没有时间范围。**
 * 而复盘时最常问的恰恰是「谁动过这个用户」。
 * 这一页的做法是：能交给服务端的（action / target_type）交给服务端，
 * 剩下三个做成**只作用于当前这一页**的细筛，并在每个输入框旁边写明这一点。
 *
 * 不写明的话会造出一个比没有筛选更坏的东西：一个看起来在全库检索、
 * 实际只在 50 条里找的框 —— 它给出的「没有记录」是一句假话。
 */
import { useMemo, useState } from 'react';
import { PageHeader } from '@babelplus/shared/ui';
import type { Meta, components } from '@babelplus/shared/api';
import { unwrapWithMeta } from '@babelplus/shared/api';
import { Badge, Button, Card, CardTitle, EmptyState, Icon, LinkButton, formatDateTime } from './_imports.ts';
import {
  CaveatNotice,
  FilterText,
  JsonBlock,
  ListSkeleton,
  MISSING,
  QueryErrorState,
  useOpsQuery,
} from './ops-common.tsx';
import { api } from '../lib/api.ts';

type AuditLogEntry = components['schemas']['AuditLogEntry'];

/** 一页 50 条。审计条目带两份 JSON 快照，再多一页就不是「翻」而是「等」了。 */
const PAGE_SIZE = 50;

/** 服务端能过滤的两个维度。改这个对象要同时改 `useOpsQuery` 的 deps。 */
interface ServerFilters {
  readonly action: string;
  readonly targetType: string;
}

/** 只在当前这一页里生效的三个维度。见文件头。 */
interface LocalFilters {
  readonly adminId: string;
  readonly targetId: string;
  readonly from: string;
  readonly to: string;
}

const EMPTY_SERVER: ServerFilters = { action: '', targetType: '' };
const EMPTY_LOCAL: LocalFilters = { adminId: '', targetId: '', from: '', to: '' };

interface AuditResult {
  readonly rows: AuditLogEntry[];
  readonly meta: Meta;
}

function loadAudit(filters: ServerFilters, cursor: string | undefined): Promise<AuditResult> {
  const query: {
    limit: number;
    count: boolean;
    cursor?: string;
    action?: string;
    target_type?: string;
  } = { limit: PAGE_SIZE, count: true };
  // ⚠️ 管理面**可以**要总数（`?count=true` → `meta.total`），用户面永远不给。
  //    后台要分页器，而总数在审计表上是一次会随时间线性变慢的 COUNT(*) ——
  //    服务端已登记撤回条件（p95 > 500ms 就改成只给「有没有下一页」）。
  if (cursor) query.cursor = cursor;
  // 🔴 原样发出去。**不 trim 成空就不发**以外的加工一律不做（服务端自己会 TrimSpace）。
  if (filters.action.trim()) query.action = filters.action.trim();
  if (filters.targetType.trim()) query.target_type = filters.targetType.trim();
  return unwrapWithMeta(api().GET('/api/v1/admin/audit', { params: { query } })).then((env) => ({
    rows: env.data,
    meta: env.meta,
  }));
}

export default function AuditPage() {
  /** 输入框里的值（随手打字），与真正发出去的值分开 —— 每敲一个字符发一次请求在 f1-micro 上不可接受。 */
  const [draft, setDraft] = useState<ServerFilters>(EMPTY_SERVER);
  const [applied, setApplied] = useState<ServerFilters>(EMPTY_SERVER);
  const [local, setLocal] = useState<LocalFilters>(EMPTY_LOCAL);
  /** 游标栈。栈顶是当前页的游标；空栈 = 第一页。存栈才能「上一页」——游标分页没有反向游标。 */
  const [cursors, setCursors] = useState<string[]>([]);

  const cursor = cursors[cursors.length - 1];
  const query = useOpsQuery<AuditResult>(
    () => loadAudit(applied, cursor),
    [applied.action, applied.targetType, cursor],
    '审计日志加载失败',
  );

  const rows = query.data?.rows ?? [];
  const meta = query.data?.meta;
  const shown = useMemo(() => rows.filter((r) => matchesLocal(r, local)), [rows, local]);
  const localActive = local.adminId !== '' || local.targetId !== '' || local.from !== '' || local.to !== '';
  const serverActive = applied.action !== '' || applied.targetType !== '';

  function applyFilters() {
    setApplied(draft);
    setCursors([]); // 换了筛选条件，游标就没有意义了 —— 它是上一次查询里的位置。
  }

  function resetAll() {
    setDraft(EMPTY_SERVER);
    setApplied(EMPTY_SERVER);
    setLocal(EMPTY_LOCAL);
    setCursors([]);
  }

  return (
    <>
      <PageHeader
        title="审计日志"
        description="全部管理操作的流水。只读，没有删除入口，也没有编辑入口。"
        meta={
          <>
            <Badge tone="info">P1</Badge>
            <Badge tone="neutral">M3 · 桌面优先，手机上可读即可</Badge>
            <Badge tone="ok">只读</Badge>
          </>
        }
      />

      <div className="space-y-4">
        <Card>
          <CardTitle hint="上面两个走服务端，下面四个只筛当前这一页">检索</CardTitle>

          <form
            onSubmit={(e) => {
              e.preventDefault();
              applyFilters();
            }}
          >
            <div className="grid gap-4 sm:grid-cols-2">
              <FilterText
                id="audit-action"
                label="动作（服务端 · 包含匹配）"
                value={draft.action}
                onChange={(action) => setDraft((d) => ({ ...d, action }))}
                placeholder="order.mark_paid"
                hint={
                  <>
                    库里的动作<strong className="font-medium text-fg">带 D 编号前缀</strong>
                    （如 <code className="font-mono">D6.order.mark_paid</code>）。这里是包含匹配：
                    填 <code className="font-mono">order.mark_paid</code> 能命中，
                    填 <code className="font-mono">D6.</code> 能筛出这一类危险操作的全部记录。
                  </>
                }
              />
              <FilterText
                id="audit-target-type"
                label="目标类型（服务端 · 等值）"
                value={draft.targetType}
                onChange={(targetType) => setDraft((d) => ({ ...d, targetType }))}
                placeholder="order / user / server / plan …"
                hint="这一个是等值匹配，不是包含匹配 —— 大小写和单复数都要对得上。"
              />
            </div>

            <div className="mt-4 flex flex-wrap gap-2">
              <Button tone="primary" type="submit">
                查询
              </Button>
              <Button onClick={resetAll} disabled={!serverActive && !localActive}>
                清空全部条件
              </Button>
            </div>
          </form>

          <div className="mt-5 border-t border-line pt-4">
            <p className="mb-3 text-xs leading-relaxed text-warn">
              ⚠️ 下面四个是<strong className="font-medium text-fg">本页细筛</strong>：
              契约里没有按操作者 / 目标 id / 时间检索的参数，所以它们只能在
              <strong className="font-medium text-fg">当前这一页已经取回来的 {rows.length} 条</strong>里找。
              这里的「没有记录」<strong className="font-medium text-fg">不等于</strong>全库没有记录 ——
              要缩小到全库范围，请先用上面两个服务端条件。
            </p>
            <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-4">
              <FilterText
                id="audit-admin-id"
                label="操作者 admin_id"
                value={local.adminId}
                onChange={(adminId) => setLocal((l) => ({ ...l, adminId }))}
                placeholder="7"
              />
              <FilterText
                id="audit-target-id"
                label="目标 id"
                value={local.targetId}
                onChange={(targetId) => setLocal((l) => ({ ...l, targetId }))}
                placeholder="20260816T7K2M9Q4"
              />
              <FilterText
                id="audit-from"
                label="起始日（含）"
                type="date"
                value={local.from}
                onChange={(from) => setLocal((l) => ({ ...l, from }))}
              />
              <FilterText
                id="audit-to"
                label="结束日（含）"
                type="date"
                value={local.to}
                onChange={(to) => setLocal((l) => ({ ...l, to }))}
              />
            </div>
          </div>
        </Card>

        {query.state === 'loading' ? <ListSkeleton rows={8} /> : null}

        {query.state === 'error' && query.error ? (
          <QueryErrorState
            error={query.error}
            what="审计日志"
            why={
              <>
                审计流水挂在 <code className="font-mono">GET /admin/audit</code> 上。
              </>
            }
            onRetry={query.reload}
          />
        ) : null}

        {query.state === 'ready' && rows.length === 0 ? (
          serverActive ? (
            <EmptyState
              title="这个条件下没有记录"
              description="动作是包含匹配、目标类型是等值匹配。先把条件放宽一点再试 —— 特别是目标类型，它拼错时一条都不会命中。"
              action={<Button tone="primary" onClick={resetAll}>清空条件</Button>}
            />
          ) : (
            <EmptyState
              title="还没有任何管理操作"
              description="全新部署时这是对的。一旦有人改过任何东西，这里就不会再是空的 —— 而且改不掉。"
              action={
                <LinkButton tone="primary" href="/admin">
                  回到看板 <Icon.ArrowRight size={14} />
                </LinkButton>
              }
            />
          )
        ) : null}

        {query.state === 'ready' && rows.length > 0 ? (
          <>
            <div className="flex flex-wrap items-baseline justify-between gap-2 text-sm text-fg-muted">
              <span>
                本页 {rows.length} 条
                {localActive ? (
                  <>
                    ，本页细筛命中 <strong className="font-medium text-fg">{shown.length}</strong> 条
                  </>
                ) : null}
                {meta?.total !== undefined ? (
                  <>
                    {' '}
                    · 当前服务端条件共 <strong className="font-medium text-fg">{meta.total.toLocaleString('zh-CN')}</strong> 条
                  </>
                ) : null}
              </span>
              <span className="text-xs text-fg-subtle">第 {cursors.length + 1} 页</span>
            </div>

            {localActive && shown.length === 0 ? (
              <CaveatNotice>
                本页这 {rows.length} 条里没有符合细筛条件的。
                <strong className="font-medium text-fg">这不代表全库没有</strong> ——
                翻到下一页再看，或者改用上面的服务端条件。
              </CaveatNotice>
            ) : null}

            <div className="space-y-2">
              {shown.map((entry) => (
                <AuditRow key={entry.id} entry={entry} />
              ))}
            </div>

            <div className="flex flex-wrap items-center gap-2">
              <Button
                onClick={() => setCursors((s) => s.slice(0, -1))}
                disabled={cursors.length === 0 || query.state !== 'ready'}
              >
                上一页
              </Button>
              <Button
                onClick={() => {
                  const next = meta?.next_cursor;
                  if (next) setCursors((s) => [...s, next]);
                }}
                disabled={!meta?.next_cursor || query.state !== 'ready'}
              >
                下一页
              </Button>
              {!meta?.next_cursor ? <span className="text-xs text-fg-subtle">已经是最后一页。</span> : null}
            </div>
          </>
        ) : null}

        <Card>
          <CardTitle>这张表说不出口的三件事</CardTitle>
          <ul className="space-y-2 text-sm leading-relaxed text-fg-muted">
            <li>
              · <strong className="font-medium text-fg">请求号是空的。</strong>
              契约把 <code className="font-mono">request_id</code> 列为必填，而
              <code className="font-mono"> audit_logs </code>根本没有这一列，服务端只能填空串。
              它本该是「把一条审计接回访问日志 / trace」的唯一钥匙 —— 现在那条线断着。
            </li>
            <li>
              · <strong className="font-medium text-fg">操作者只有 id，没有邮箱。</strong>
              库里存着 <code className="font-mono">admin_email_snapshot</code>，
              但契约的 <code className="font-mono">AuditLogEntry</code> 上没有字段可放。
              <code className="font-mono"> admin_id = 0 </code>
              说明那条记录指认不到人（历史上真删过管理员）。
            </li>
            <li>
              · <strong className="font-medium text-fg">按操作者 / 目标 id / 时间检索没有服务端参数。</strong>
              上面那四个框只筛当前这一页。补它需要给端点加查询参数。
            </li>
          </ul>
        </Card>

        <Card>
          <p className="text-sm leading-relaxed text-fg-muted">
            这一页<strong className="font-medium text-fg">刻意没有任何写操作</strong>。
            没有删除、没有编辑、没有批量操作，也<strong className="font-medium text-fg">没有导出</strong>
            （导出会把审计流水搬到一个不再被审计的地方）。
            如果有一天这里出现了写操作，请先回去读一遍 page-inventory §4.4 的末尾那句话。
          </p>
        </Card>
      </div>
    </>
  );
}

/* ────────────────────────────── 本页细筛 ────────────────────────────── */

/**
 * 只作用于「已经取回来的这一页」。
 *
 * 时间用**本地日**比较（操作者眼里的日期就是屏幕上显示的那个），
 * 而不是 UTC 日 —— 屏幕上写着 08-31 却筛不出来，没有人会认为那是时区问题。
 */
function matchesLocal(entry: AuditLogEntry, f: LocalFilters): boolean {
  if (f.adminId.trim() && String(entry.admin_id) !== f.adminId.trim()) return false;
  if (f.targetId.trim() && !entry.target_id.includes(f.targetId.trim())) return false;
  if (f.from || f.to) {
    const day = localDay(entry.created_at);
    if (day === null) return false;
    if (f.from && day < f.from) return false;
    if (f.to && day > f.to) return false;
  }
  return true;
}

function localDay(iso: string): string | null {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return null;
  const y = d.getFullYear();
  const m = String(d.getMonth() + 1).padStart(2, '0');
  const day = String(d.getDate()).padStart(2, '0');
  return `${y}-${m}-${day}`;
}

/* ────────────────────────────── 一条记录 ────────────────────────────── */

/** `D6.order.mark_paid` → `['D6', 'order.mark_paid']`；不带前缀时第一项为 null。 */
function splitAction(action: string): [string | null, string] {
  const m = /^(D\d+b?)\.(.*)$/.exec(action);
  if (!m) return [null, action];
  return [m[1] ?? null, m[2] ?? action];
}

function AuditRow({ entry }: { entry: AuditLogEntry }) {
  const [code, rest] = splitAction(entry.action);
  const hasSnapshot = entry.before !== undefined || entry.after !== undefined;

  return (
    <Card as="article" className="p-3 sm:p-4">
      <div className="flex flex-wrap items-baseline gap-x-3 gap-y-1">
        {code ? (
          <span className="rounded bg-danger/15 px-1.5 py-0.5 font-mono text-xs font-semibold text-danger">
            {code}
          </span>
        ) : null}
        <span className="font-mono text-sm font-medium text-fg">{rest}</span>
        <span className="text-xs text-fg-muted">
          {entry.target_type}
          <span className="font-mono"> #{entry.target_id}</span>
        </span>
        <span className="ml-auto text-xs text-fg-subtle">{formatDateTime(entry.created_at)}</span>
      </div>

      <p className="mt-1.5 flex flex-wrap gap-x-3 gap-y-1 text-xs text-fg-muted">
        <span>
          操作者{' '}
          {entry.admin_id === 0 ? (
            <span className="text-warn">指认不到人（管理员被硬删过）</span>
          ) : (
            <code className="font-mono">admin #{entry.admin_id}</code>
          )}
        </span>
        <span>
          IP <code className="font-mono">{entry.ip || MISSING}</code>
        </span>
        {entry.user_agent ? <span className="truncate max-w-md">UA {entry.user_agent}</span> : null}
      </p>

      {entry.reason ? (
        <p className="mt-2 rounded-lg border border-line bg-surface-alt px-3 py-2 text-sm leading-relaxed text-fg">
          原因：{entry.reason}
        </p>
      ) : null}

      {hasSnapshot ? (
        <details className="mt-2">
          <summary className="cursor-pointer text-xs text-accent">改前值 / 改后值</summary>
          {/* 存的是**变更字段的完整快照**，不是 diff —— diff 需要靠对面的数据重建，
              而对面的数据可能已经被改过三次了。 */}
          <div className="mt-2 grid gap-3 sm:grid-cols-2">
            <JsonBlock label="改前" value={entry.before} />
            <JsonBlock label="改后" value={entry.after} />
          </div>
        </details>
      ) : (
        <p className="mt-2 text-xs text-fg-subtle">这一条没有快照（新建 / 删除类操作只有一侧）。</p>
      )}
    </Card>
  );
}

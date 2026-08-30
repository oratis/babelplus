/**
 * 模块 7 · 流量统计 `/admin/stats` —— P1 / M3。**成本核算的唯一依据。**
 *
 * 只有日 / 月聚合，没有明细（落明细是这个业务的性能命门，system-design §6.3）。
 * 所以这一页能回答「这个月出口花了多少钱」，回答不了「某用户 14:32 用了多少」。
 *
 * # 三处必须写在代码里的口径
 *
 * 1. 🔴 **`record_at` 是「上海当天 00:00」那个 UTC 时刻**（服务端 `catalogRecordAt`），
 *    例如上海 2026-08-31 那一天，`record_at = 2026-08-30T16:00:00Z`。
 *    **不能用浏览器本地时区去渲染它** —— 在 UTC 机器上会显示成 08-30，
 *    整张报表的每一行都会错一天，而它看起来完全正常。所以这一页统一走
 *    `formatStatDate`（钉死 `Asia/Shanghai`），不用 `formatDateTime`。
 *
 * 2. 🔴 **`?to=` 不能填当天的 23:59:59Z。** 服务端把 `to` 这个**时刻**换算成上海的**日期**
 *    （`catalogStatDate`）。23:59:59Z 是上海次日 07:59，于是查询会**多算一天**。
 *    这里用当天 12:00Z（上海 20:00），换算回来仍是当天。
 *
 * 3. ⚠️ **参数不合法时服务端回的是 500 + `VALIDATION_FAILED`**（契约给这个端点只声明了
 *    403/500，没有 422，`statsBadParam` 的注释写了缘由）。所以错误处理必须**按 code 分支**：
 *    按状态码分支会把「时间跨度填太长」显示成「服务端炸了」，操作者会去看状态页而不是改输入。
 *
 * # 出口单价为什么是一个输入框而不是常量
 *
 * 脚手架的 TODO(P1) 逐字写着：单价**必须是可配置项而不是常量**，
 * 它随网络层级、区域、用量档位变化，写死在前端的那天起这一页给出的成本就是错的。
 * 现状是 **`settings` 表里还没有任何种子键**（迁移 0011 只建表不塞值），
 * 也就没有「出口单价」这个键可读 —— 所以现在只能在这一页手填，且**不预填任何数字**：
 * page-inventory §4.3 记的 ¥1.65/GB 是 **GCP Premium** 的口径，
 * 而 ADR 0008 之后新建机锁的是 **Standard**，两者单价不同。
 * 预填一个 8 成场合下是错的数字，比留空更糟 —— 留空至少不会被当成结论抄走。
 * TODO(P1)：`settings` 里加一个出口单价键（按网络层级分档），这一页改成读它、并允许临时覆盖。
 */
import { useMemo, useState } from 'react';
import type { ReactNode } from 'react';
import { PageHeader } from '@babelplus/shared/ui';
import type { components } from '@babelplus/shared/api';
import { toApiError, unwrapWithMeta } from '@babelplus/shared/api';
import type { Meta } from '@babelplus/shared/api';
import { Badge, Card, CardTitle, EmptyState, Icon, LinkButton, formatBytes, formatCny } from './_imports.ts';
import { DangerousAction, type DangerousActionValues } from '../components/DangerousAction.tsx';
import {
  CaveatNotice,
  FilterSelect,
  FilterText,
  ListSkeleton,
  MISSING,
  QueryErrorState,
  useOpsQuery,
} from './ops-common.tsx';
import { api } from '../lib/api.ts';

type StatBucket = components['schemas']['StatBucket'];
type Scope = 'global' | 'user' | 'server';

const SCOPES: ReadonlyArray<{ value: Scope; label: string }> = [
  { value: 'global', label: '全局（每天一行）' },
  { value: 'user', label: '按用户' },
  { value: 'server', label: '按节点' },
];

/** 1 GB = 1024³ B。与 `formatBytes` 同口径（它也是二进制），不然表上的数与成本对不上。 */
const BYTES_PER_GB = 1024 ** 3;

/**
 * 日期输入 → 契约要的 RFC3339 UTC 时刻。见文件头第 2 条。
 * `edge='end'` 用 12:00Z 而不是 23:59:59Z，否则会多算一天。
 */
function toRfc3339(day: string, edge: 'start' | 'end'): string | null {
  if (!/^\d{4}-\d{2}-\d{2}$/.test(day)) return null;
  return `${day}T${edge === 'start' ? '00:00:00' : '12:00:00'}Z`;
}

/** `record_at` → 上海自然日。**不用浏览器时区**，见文件头第 1 条。 */
function formatStatDate(iso: string | null | undefined): string {
  if (!iso) return MISSING;
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return MISSING;
  return new Intl.DateTimeFormat('zh-CN', {
    timeZone: 'Asia/Shanghai',
    year: 'numeric',
    month: '2-digit',
    day: '2-digit',
  }).format(d);
}

interface StatsResult {
  readonly rows: StatBucket[];
  readonly meta: Meta;
}

function loadStats(scope: Scope, from: string, to: string): Promise<StatsResult> {
  const query: { scope: Scope; from?: string; to?: string } = { scope };
  const fromAt = toRfc3339(from, 'start');
  const toAt = toRfc3339(to, 'end');
  if (fromAt) query.from = fromAt;
  if (toAt) query.to = toAt;
  return unwrapWithMeta(api().GET('/api/v1/admin/stats', { params: { query } })).then((env) => ({
    rows: env.data,
    meta: env.meta,
  }));
}

/**
 * D14 的导出。**响应是 `text/csv` 不是信封**，所以不能走 `unwrap` ——
 * 用 `parseAs: 'text'` 拿原文，失败时仍由 `toApiError` 归一（openapi-fetch 对错误响应
 * 总是先试 JSON.parse，所以 403/429/500 的信封照样解得出来）。
 */
async function exportStatsCsv(scope: Scope): Promise<string> {
  const result = await api().GET('/api/v1/admin/stats/export', {
    params: { query: { scope } },
    parseAs: 'text',
  });
  if (result.error !== undefined || result.data === undefined) {
    throw toApiError(result.response, result.error);
  }
  return String(result.data);
}

/** 把 CSV 交给浏览器保存。**不经服务端二次跳转** —— 请求带着 IAP 会话与 Authorization 头，普通链接拿不到。 */
function saveCsv(text: string, filename: string): void {
  const url = URL.createObjectURL(new Blob([text], { type: 'text/csv;charset=utf-8' }));
  const a = document.createElement('a');
  a.href = url;
  a.download = filename;
  document.body.appendChild(a);
  a.click();
  a.remove();
  URL.revokeObjectURL(url);
}

export default function StatsPage() {
  const [scope, setScope] = useState<Scope>('global');
  const [from, setFrom] = useState('');
  const [to, setTo] = useState('');
  /** 元 / GB。字符串而不是 number：受控数字输入在清空时会变成 NaN，而 NaN 会把成本列变成「¥NaN」。 */
  const [priceYuan, setPriceYuan] = useState('');
  const [exported, setExported] = useState<{ scope: Scope; rows: number } | null>(null);

  const query = useOpsQuery<StatsResult>(() => loadStats(scope, from, to), [scope, from, to], '统计取数失败');
  const rows = query.data?.rows ?? [];
  const truncated = query.data?.meta.has_more === true;

  const price = Number.parseFloat(priceYuan);
  const hasPrice = Number.isFinite(price) && price >= 0;
  /** 字节 → 成本（分）。整数分，避免把浮点误差带进金额展示。 */
  const costCents = (bytes: number): number | null =>
    hasPrice ? Math.round((bytes / BYTES_PER_GB) * price * 100) : null;

  const totals = useMemo(() => {
    let up = 0;
    let down = 0;
    for (const r of rows) {
      up += r.upload_bytes;
      down += r.download_bytes;
    }
    return { up, down, total: up + down };
  }, [rows]);

  /** 按对象聚合（scope=user / server 时一个对象会有多天多行）。按总量倒序 —— 排行表的用途是「谁最费」。 */
  const ranked = useMemo(() => {
    if (scope === 'global') return [];
    const acc = new Map<number, { id: number; up: number; down: number; days: number }>();
    for (const r of rows) {
      const id = scope === 'user' ? r.user_id : r.server_id;
      if (id === undefined) continue;
      const cur = acc.get(id) ?? { id, up: 0, down: 0, days: 0 };
      cur.up += r.upload_bytes;
      cur.down += r.download_bytes;
      cur.days += 1;
      acc.set(id, cur);
    }
    return [...acc.values()].sort((a, b) => b.up + b.down - (a.up + a.down));
  }, [rows, scope]);

  return (
    <>
      <PageHeader
        title="流量统计"
        description="日 / 月聚合，以及按当前单价折算的出口成本。"
        meta={
          <>
            <Badge tone="info">P1</Badge>
            <Badge tone="neutral">M3 · 桌面优先，手机上可读即可</Badge>
            <Badge tone="danger">D14 导出 CSV</Badge>
          </>
        }
      />

      <div className="space-y-4">
        <Card>
          <CardTitle hint="留空 = 服务端默认的最近 30 天">查询条件</CardTitle>
          <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-4">
            <FilterSelect
              id="stats-scope"
              label="聚合维度"
              value={scope}
              options={SCOPES}
              onChange={(next) => setScope(next)}
            />
            <FilterText
              id="stats-from"
              label="起始日（含）"
              type="date"
              value={from}
              onChange={setFrom}
              hint="按上海时区的自然日算。"
            />
            <FilterText
              id="stats-to"
              label="结束日（含）"
              type="date"
              value={to}
              onChange={setTo}
              hint="跨度上限 366 天（这个端点没有分页参数，更长只会被截断）。"
            />
            <FilterText
              id="stats-price"
              label="出口单价（元 / GB）"
              type="number"
              value={priceYuan}
              onChange={setPriceYuan}
              placeholder="例如 1.65"
              hint="刻意不预填：¥1.65/GB 是 GCP Premium 的口径，ADR 0008 之后新建机是 Standard，单价不同。"
            />
          </div>
        </Card>

        {query.state === 'loading' ? <ListSkeleton rows={8} /> : null}

        {query.state === 'error' && query.error ? (
          <QueryErrorState
            error={query.error}
            what="流量统计"
            why={
              <>
                统计挂在 <code className="font-mono">GET /admin/stats</code> 上。
              </>
            }
            onRetry={query.reload}
          />
        ) : null}

        {query.state === 'ready' && rows.length === 0 ? (
          <EmptyState
            title="这个条件下没有聚合数据"
            description="节点上报后按天聚合，所以新部署的第一天是空的；也可能只是这段时间真的没有流量。"
            action={
              <LinkButton tone="primary" href="/admin/nodes">
                看看节点有没有在上报 <Icon.ArrowRight size={14} />
              </LinkButton>
            }
          />
        ) : null}

        {query.state === 'ready' && rows.length > 0 ? (
          <>
            {truncated ? (
              <CaveatNotice>
                🔴 <strong className="font-medium text-fg">结果已被截断。</strong>
                服务端一次最多返回 5000 行（这个端点没有分页参数），
                <code className="font-mono"> meta.has_more </code>为真说明还有没取回来的数据 ——
                <strong className="font-medium text-fg">下面这张表不是完整数据，不要拿它做成本核算</strong>。
                把时间窗缩短，或换成「全局」维度（全局每天只有一行，不会触顶）。
              </CaveatNotice>
            ) : null}

            <Card>
              <CardTitle
                hint={`${rows.length.toLocaleString('zh-CN')} 行 · 合计 ${formatBytes(totals.total)}`}
              >
                {scope === 'global' ? '每日流量' : scope === 'user' ? '按用户' : '按节点'}
              </CardTitle>

              <div className="overflow-x-auto">
                {scope === 'global' ? (
                  <table className="w-full min-w-[36rem] text-sm">
                    <thead>
                      <tr className="border-b border-line text-left text-xs text-fg-muted">
                        <Th>日期（上海）</Th>
                        <Th align="right">上传</Th>
                        <Th align="right">下载</Th>
                        <Th align="right">合计</Th>
                        <Th align="right">出口成本</Th>
                      </tr>
                    </thead>
                    <tbody>
                      {rows.map((r) => (
                        <tr key={r.record_at} className="border-b border-line/60">
                          <Td>{formatStatDate(r.record_at)}</Td>
                          <Td align="right">{formatBytes(r.upload_bytes)}</Td>
                          <Td align="right">{formatBytes(r.download_bytes)}</Td>
                          <Td align="right">{formatBytes(r.upload_bytes + r.download_bytes)}</Td>
                          <Td align="right">
                            {formatCost(costCents(r.upload_bytes + r.download_bytes))}
                          </Td>
                        </tr>
                      ))}
                    </tbody>
                    <tfoot>
                      <tr className="font-medium">
                        <Td>合计</Td>
                        <Td align="right">{formatBytes(totals.up)}</Td>
                        <Td align="right">{formatBytes(totals.down)}</Td>
                        <Td align="right">{formatBytes(totals.total)}</Td>
                        <Td align="right">{formatCost(costCents(totals.total))}</Td>
                      </tr>
                    </tfoot>
                  </table>
                ) : (
                  <table className="w-full min-w-[36rem] text-sm">
                    <thead>
                      <tr className="border-b border-line text-left text-xs text-fg-muted">
                        <Th>{scope === 'user' ? '用户' : '节点'}</Th>
                        <Th align="right">有数据的天数</Th>
                        <Th align="right">上传</Th>
                        <Th align="right">下载</Th>
                        <Th align="right">合计</Th>
                        <Th align="right">出口成本</Th>
                      </tr>
                    </thead>
                    <tbody>
                      {ranked.map((r) => (
                        <tr key={r.id} className="border-b border-line/60">
                          <Td>
                            <a
                              href={scope === 'user' ? `/admin/users/${r.id}` : `/admin/nodes/${r.id}`}
                              className="font-mono text-accent hover:underline"
                            >
                              #{r.id}
                            </a>
                          </Td>
                          <Td align="right">{r.days}</Td>
                          <Td align="right">{formatBytes(r.up)}</Td>
                          <Td align="right">{formatBytes(r.down)}</Td>
                          <Td align="right">{formatBytes(r.up + r.down)}</Td>
                          <Td align="right">{formatCost(costCents(r.up + r.down))}</Td>
                        </tr>
                      ))}
                    </tbody>
                    <tfoot>
                      <tr className="font-medium">
                        <Td>合计</Td>
                        <Td align="right">{ranked.length} 个对象</Td>
                        <Td align="right">{formatBytes(totals.up)}</Td>
                        <Td align="right">{formatBytes(totals.down)}</Td>
                        <Td align="right">{formatBytes(totals.total)}</Td>
                        <Td align="right">{formatCost(costCents(totals.total))}</Td>
                      </tr>
                    </tfoot>
                  </table>
                )}
              </div>

              {!hasPrice ? (
                <p className="mt-3 text-xs leading-relaxed text-fg-muted">
                  成本这一列现在是「{MISSING}」，因为还没填单价。
                  <strong className="font-medium text-fg">这不是缺陷，是刻意的</strong> ——
                  见页面下方「成本口径」。
                </p>
              ) : null}
            </Card>

            <Card>
              <CardTitle>成本口径</CardTitle>
              <p className="text-sm leading-relaxed text-fg-muted">
                成本 = 合计流量 ÷ 1024³ × 单价。单价是<strong className="font-medium text-fg">你在上面填的那个数</strong>，
                <strong className="font-medium text-fg">不会被保存</strong>，也不来自系统配置 ——
                <code className="font-mono"> settings </code>表里目前没有出口单价这个键（迁移 0011 只建表没塞种子值）。
              </p>
              <p className="mt-2 text-sm leading-relaxed text-fg-muted">
                历史月份应当按<strong className="font-medium text-fg">当时的单价</strong>核算，
                而这一页只有一个当前单价 —— 跨越过资费变更的时间窗，这里给出的数字是错的。
                这条缺口要靠「单价进配置表并带生效时间」补，不是靠在这里多填一个框。
              </p>
            </Card>
          </>
        ) : null}

        <Card>
          <CardTitle>这一页查不到、也不打算查的东西</CardTitle>
          <ul className="space-y-1.5 text-sm leading-relaxed text-fg-muted">
            <li>
              · <strong className="font-medium text-fg">明细流水</strong> ——
              只存日 / 月聚合（system-design §6.3：落明细必炸）。所以「某用户 14:32 用了多少」查不到。
            </li>
            <li>
              · <strong className="font-medium text-fg">用户 × 节点的交叉维度</strong> ——
              现有聚合只有「按用户」与「按节点」两张，没有交叉表。
              用户面板 <code className="font-mono">/usage</code> 的「按节点分布」卡在同一处。
            </li>
            <li>
              · <strong className="font-medium text-fg">访问了哪些网站</strong> ——
              不落目的地址日志。后台<strong className="font-medium text-fg">不存在</strong>这个查询入口，这是刻意的。
            </li>
          </ul>
        </Card>

        {/* D14。放在页面底部而不是页头：导出是「看完之后偶尔做一次」的动作，
            放在页头会让它与「换个筛选条件」处在同一个视觉层级上。 */}
        <Card>
          <CardTitle hint="D14 · 独立权限位">导出 CSV</CardTitle>
          <DangerousAction
            code="D14"
            title="导出流量统计 CSV"
            submitLabel={`导出（${scope}）`}
            permissionName="admin.user.export"
            context={
              <>
                <p>
                  导出的是<strong className="font-medium">服务端自钉的最近 90 天</strong>，
                  <strong className="font-medium">不是你上面选的时间窗</strong> ——
                  这个端点的参数只有 <code className="font-mono">scope</code>，
                  无界导出会让独立权限位形同虚设。当前维度：<code className="font-mono">{scope}</code>。
                </p>
                <p className="mt-2">
                  列固定为 <code className="font-mono">record_at, scope, user_id, server_id, upload_bytes, download_bytes</code>。
                  <strong className="font-medium">scope=user 时含 user_id</strong>，这份文件落到本机之后就不在审计范围内了。
                </p>
                <p className="mt-2">每个管理员每小时最多 5 次。这次导出会写进审计：谁、何时、哪些字段、多少行。</p>
              </>
            }
            onSubmit={async (_values: DangerousActionValues) => {
              const csv = await exportStatsCsv(scope);
              const stamp = new Date().toISOString().slice(0, 10);
              saveCsv(csv, `bp-stats-${scope}-${stamp}.csv`);
              // 行数 = 总行数 - 表头。让操作者看见的数与审计里记的是同一个。
              const lines = csv.split('\n').filter((l) => l.trim().length > 0).length;
              setExported({ scope, rows: Math.max(0, lines - 1) });
            }}
          />
          {exported ? (
            <p className="mt-3 rounded-lg border border-line bg-surface-alt px-3 py-2 text-sm text-fg-muted" role="status">
              已导出 <code className="font-mono">{exported.scope}</code> 维度的{' '}
              <strong className="font-medium text-fg">{exported.rows.toLocaleString('zh-CN')}</strong> 行。
              这次导出已经进审计日志了。
            </p>
          ) : null}
        </Card>
      </div>
    </>
  );
}

/* ────────────────────────────── 版面零件 ────────────────────────────── */

function formatCost(cents: number | null): string {
  return cents === null ? MISSING : formatCny(cents);
}

function Th({ children, align = 'left' }: { children: ReactNode; align?: 'left' | 'right' }) {
  return <th className={`py-2 pr-3 font-medium ${align === 'right' ? 'text-right' : ''}`}>{children}</th>;
}

function Td({ children, align = 'left' }: { children: ReactNode; align?: 'left' | 'right' }) {
  return (
    <td className={`py-2 pr-3 tabular-nums ${align === 'right' ? 'text-right' : ''}`}>{children}</td>
  );
}

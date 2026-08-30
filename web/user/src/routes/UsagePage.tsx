/**
 * `/usage` —— P2。page-inventory §3.1 #13、§3.2.7。竞品完全没有流量趋势图，这是最便宜的差异化。
 *
 * 三个**互相独立**的请求，各持一套三态（§2.2）：
 *   ① `getUserUsage`（日曲线）② `getUserSubscription`（周期进度）③ `listSubscriptionFetchLog`（拉取审计）
 * 任何一个挂掉都不许把另外两个一起吞掉。尤其是 ③：它是次要区块，
 * 但它挂掉时用户最需要看到的恰恰是 ① 和 ②。
 *
 * 🔴 **空态的判据是「全是 0」，不是「数组为空」。**
 * `handler/usersub.go` 的 `buildUsageSeries` 会把整个窗口**补齐成 days 个点**
 * （`for i := days-1; i >= 0; i--`，缺的那天填 0），所以新账号拿到的是
 * **30 个零点**而不是空数组。写成 `points.length === 0` 的话，新用户看到的是
 * 一张全是零的柱状图 —— 而 §3.2.7 逐字禁止这件事：「不显示空白图表 ——
 * 一张全是零的柱状图看起来像坏了」。UsagePage.test.tsx 钉死了这一条。
 *
 * 🔴 **柱子不要从 0 动画增长到实际值**（§3.2.7 加载态）——
 * 在慢网络下会被误读成数据错误。所以柱子上**没有任何 transition / animation 类**，
 * 高度是渲染时就写死的最终值。别顺手加一个 `transition-all` 上去。
 *
 * 单位一律走 `formatBytes`（全站唯一实现，与客户端显示同口径），
 * **这一页不做任何自己的字节换算** —— api-contract §2.6：流量是整数字节，
 * 任何自制的 /1000 或 /1024 都会让面板和客户端对不上数，而那是最难解释的一类工单。
 */
import { useState } from 'react';
import {
  Badge,
  Card,
  CardTitle,
  EmptyState,
  Icon,
  LinkButton,
  NotWiredNotice,
  Skeleton,
  cx,
  daysUntil,
  formatBytes,
  formatDate,
  formatDateTime,
  percent,
} from './_imports.ts';
import { unwrap, unwrapWithMeta, type UserSubscription } from '@babelplus/shared/api';
import { api } from '../lib/api.ts';
import { useApiQuery } from './ticket-common.tsx';
import {
  QuerySection,
  type SubscriptionFetchLogEntry,
  type SubscriptionSummary,
  type UsagePoint,
  type UsageSeries,
} from './account-common.tsx';

type Range = '7d' | '30d' | '90d';

const RANGES: ReadonlyArray<{ readonly value: Range; readonly label: string }> = [
  { value: '7d', label: '近 7 天' },
  { value: '30d', label: '近 30 天' },
  { value: '90d', label: '近 90 天' },
];

/** §3.2.7：审计区块只看「最近 10 次」。翻页在这一页没有价值 —— 要查历史去 `/subscribe`。 */
const FETCH_LOG_LIMIT = 10;

const loadSubscription = (): Promise<UserSubscription> => unwrap(api().GET('/api/v1/user/subscription'));
const loadFetchLog = (): Promise<SubscriptionFetchLogEntry[]> =>
  unwrapWithMeta(
    api().GET('/api/v1/user/subscription/fetch-log', {
      params: { query: { limit: FETCH_LOG_LIMIT } },
    }),
  ).then((envelope) => envelope.data);

export default function UsagePage() {
  const [range, setRange] = useState<Range>('30d');

  // `range` 进依赖数组：换窗口 = 重新发一次请求，且这一次的三态与上一次完全隔离
  // （`useApiQuery` 的 effect 里有 `alive` 闸，旧窗口的迟到响应不会覆盖新窗口）。
  const usage = useApiQuery<UsageSeries>(
    () => unwrap(api().GET('/api/v1/user/usage', { params: { query: { range } } })),
    [range],
    '用量曲线加载失败',
  );
  const subscription = useApiQuery(loadSubscription, [], '订阅信息加载失败');
  const fetchLog = useApiQuery(loadFetchLog, [], '拉取审计加载失败');

  return (
    <>
      <header className="mb-5 sm:mb-6">
        <h1 className="text-xl font-semibold tracking-tight text-fg sm:text-2xl">用量</h1>
        <p className="mt-1 max-w-2xl text-sm text-fg-muted">流量花在哪、还够用多久。</p>
      </header>

      <div className="space-y-4">
        <Card>
          <CardTitle
            hint={
              // CardTitle 把 hint 放进一个 <p> 里，所以这里只能用行内元素 —— 嵌 <div> 是非法 HTML。
              <span className="inline-flex flex-wrap gap-1">
                {RANGES.map((r) => (
                  <button
                    key={r.value}
                    type="button"
                    onClick={() => setRange(r.value)}
                    aria-pressed={range === r.value}
                    className={cx(
                      'rounded-md border px-2 py-1 text-xs',
                      range === r.value
                        ? 'border-accent/40 bg-accent/10 text-accent'
                        : 'border-line text-fg-muted hover:bg-surface-alt',
                    )}
                  >
                    {r.label}
                  </button>
                ))}
              </span>
            }
          >
            日流量
          </CardTitle>
          <QuerySection
            query={usage}
            what="流量曲线"
            skeleton={<UsageChartSkeleton />}
          >
            {(series) => <UsageChart series={series} />}
          </QuerySection>
        </Card>

        <Card>
          <CardTitle hint="来自订阅摘要，与曲线是两个数据源">周期进度</CardTitle>
          <QuerySection query={subscription} what="周期进度">
            {(data) => <CycleProgress summary={data.summary} />}
          </QuerySection>
        </Card>

        {/* ⚠️ 「按节点分布」**做不了**，且原因不在前端。
            现有聚合口径只有 `stat_user`（用户维度）与 `stat_server`（节点维度），
            **没有 (user × server) 交叉维度**；契约里也没有对应的 operation。
            要做它必须先加 `stat_user_server(user_id, server_id, date, u, d)`
            （量级约 3 万行/月，对 PostgreSQL 是噪音级）。
            这条 TODO **没有兑现，所以保留** —— 用 NotWiredNotice 而不是画一个假图。 */}
        <Card>
          <CardTitle hint="⚠️ 需要新表 stat_user_server">按节点分布</CardTitle>
          <NotWiredNotice>
            这张图需要一张现有数据模型里<strong className="font-medium text-fg">没有</strong>的表：
            <code className="font-mono text-fg">stat_user_server(user_id, server_id, date, u, d)</code>
            。现有聚合只有用户维度与节点维度，没有两者的交叉维度，契约里也还没有对应的端点。
            <span className="mt-1 block">TODO(P2)：先加表与端点，再接这一块。在那之前不画占位图表。</span>
          </NotWiredNotice>
        </Card>

        <Card>
          <CardTitle hint={`最近 ${FETCH_LOG_LIMIT} 次`}>订阅拉取审计</CardTitle>
          <QuerySection query={fetchLog} what="拉取审计">
            {(entries) => <FetchLogList entries={entries} />}
          </QuerySection>
        </Card>
      </div>
    </>
  );
}

/* ─────────────────────────── 日流量柱状图 ─────────────────────────── */

function UsageChartSkeleton() {
  return (
    <div className="flex h-40 items-end gap-1" aria-hidden="true">
      {Array.from({ length: 24 }, (_, i) => (
        <Skeleton key={i} className={cx('flex-1', i % 3 === 0 ? 'h-1/3' : i % 3 === 1 ? 'h-2/3' : 'h-1/2')} />
      ))}
    </div>
  );
}

function UsageChart({ series }: { series: UsageSeries }) {
  const total = series.total_upload_bytes + series.total_download_bytes;

  // 🔴 空态的判据。见文件头：后端把窗口补齐成 N 个零点，所以数组永远非空。
  if (total === 0) {
    return (
      <EmptyState
        title="用满一天后这里会出现流量曲线"
        description="日曲线按天聚合，所以新账号至少要过一天才有第一个点。现在能看的是实时累计值。"
        action={
          <LinkButton tone="primary" href="/dashboard">
            先看当前用量 <Icon.ArrowRight size={14} />
          </LinkButton>
        }
        secondary="不显示空白图表 —— 一张全是零的柱状图看起来像坏了。"
      />
    );
  }

  // 纵轴刻度取窗口内单日最大值。**不取全局固定值**：固定刻度会让轻用户的柱子
  // 全部贴着底边，看起来像「没有数据」而不是「用得少」。
  const peak = series.points.reduce(
    (max, p) => Math.max(max, p.upload_bytes + p.download_bytes),
    0,
  );

  return (
    <div>
      <dl className="mb-3 flex flex-wrap gap-x-6 gap-y-1 text-sm">
        <div className="flex items-baseline gap-2">
          <dt className="text-fg-muted">合计</dt>
          <dd className="font-mono tabular-nums text-fg">{formatBytes(total)}</dd>
        </div>
        <div className="flex items-baseline gap-2">
          <dt className="flex items-center gap-1.5 text-fg-muted">
            <span className="inline-block size-2.5 rounded-sm bg-accent" aria-hidden="true" />
            下载
          </dt>
          <dd className="font-mono tabular-nums text-fg">{formatBytes(series.total_download_bytes)}</dd>
        </div>
        <div className="flex items-baseline gap-2">
          <dt className="flex items-center gap-1.5 text-fg-muted">
            <span className="inline-block size-2.5 rounded-sm bg-ok" aria-hidden="true" />
            上传
          </dt>
          <dd className="font-mono tabular-nums text-fg">{formatBytes(series.total_upload_bytes)}</dd>
        </div>
      </dl>

      {/* 每根柱子是一个 `<li>`，`title` 给鼠标，`aria-label` 给屏幕阅读器 ——
          纯 CSS 的柱状图不需要图表库，也不需要 canvas（canvas 对读屏是完全不可见的）。 */}
      <ul className="flex h-40 items-end gap-px" role="list">
        {series.points.map((point) => (
          <UsageBar key={point.date} point={point} peak={peak} />
        ))}
      </ul>

      <div className="mt-1.5 flex justify-between text-xs text-fg-subtle">
        <span>{formatDate(series.points[0]?.date)}</span>
        <span>{formatDate(series.points[series.points.length - 1]?.date)}</span>
      </div>
    </div>
  );
}

/**
 * 一根柱子。上传叠在下载上面，**分色**（§3.2.7）。
 *
 * 🔴 高度用内联 `style` 直接给最终百分比，且样式类里**一个 transition / animate 都没有**。
 * 加一个 `transition-[height]` 上去，柱子就会从 0 长到实际值 ——
 * 在慢网络下这看起来像「数据在变」，用户会截图问「到底哪个数是对的」。
 */
function UsageBar({ point, peak }: { point: UsagePoint; peak: number }) {
  const dayTotal = point.upload_bytes + point.download_bytes;
  const heightPercent = peak > 0 ? (dayTotal / peak) * 100 : 0;
  const uploadShare = dayTotal > 0 ? (point.upload_bytes / dayTotal) * 100 : 0;

  return (
    <li
      className="flex h-full flex-1 items-end"
      title={`${point.date}：下载 ${formatBytes(point.download_bytes)} · 上传 ${formatBytes(point.upload_bytes)}`}
    >
      <div
        className="flex w-full flex-col justify-end overflow-hidden rounded-t-sm bg-accent"
        style={{ height: `${heightPercent}%` }}
        aria-label={`${point.date} 共 ${formatBytes(dayTotal)}`}
      >
        <div className="w-full bg-ok" style={{ height: `${uploadShare}%` }} aria-hidden="true" />
      </div>
    </li>
  );
}

/* ─────────────────────────── 周期进度 ─────────────────────────── */

function CycleProgress({ summary }: { summary: SubscriptionSummary }) {
  const used = summary.upload_bytes + summary.download_bytes;
  const hasQuota = summary.total_bytes > 0;
  const usedPercent = percent(used, summary.total_bytes);
  const resetInDays = daysUntil(summary.reset_at);
  const forecast = exhaustionForecast(summary, resetInDays);

  return (
    <div className="space-y-3 text-sm">
      <div>
        <div className="flex flex-wrap items-baseline justify-between gap-x-3">
          <span className="text-fg-muted">本周期已用</span>
          <span className="font-mono tabular-nums text-fg">
            {formatBytes(used)}
            {hasQuota ? ` / ${formatBytes(summary.total_bytes)}` : ''}
          </span>
        </div>
        <div className="mt-1.5">
          {hasQuota ? (
            <div
              className="h-2 w-full overflow-hidden rounded-full bg-surface-alt"
              role="progressbar"
              aria-label="本周期流量使用进度"
              aria-valuemin={0}
              aria-valuemax={100}
              aria-valuenow={Math.round(usedPercent)}
            >
              {/* 同样不加过渡动画，理由见 UsageBar。 */}
              <div
                className={cx('h-full rounded-full', usedPercent >= 90 ? 'bg-warn' : 'bg-accent')}
                style={{ width: `${usedPercent}%` }}
              />
            </div>
          ) : (
            // `total_bytes = 0` 到底是「不限流量」还是「还没配额度」，契约没写明。
            // 猜错任何一边都会让用户按错误的预期用流量，所以只陈述事实。
            <p className="text-xs text-fg-subtle">这个套餐没有给出流量上限。</p>
          )}
        </div>
      </div>

      {/* 🔴 「重置日 = 订单日」在这一页**再说一次**（tutorials-spec §5 记录这是高频误解：
          用户默认以为流量在每月 1 号重置，于是月底用光后等到 1 号，发现没恢复，然后开工单）。
          与 dashboard 上那句是**刻意重复**，不是漏删 —— 看到这一页的人未必看过那一页。
          且 `reset_at` 缺失时照样要说：被误解的是规则本身，不是某个具体日期。 */}
      <div className="rounded-lg border border-line bg-surface-alt p-3">
        <p className="text-fg">
          <strong className="font-medium">重置日 = 你的下单日，不是每月 1 号。</strong>{' '}
          <span className="text-fg-muted">
            {summary.reset_at
              ? `下一次重置：${formatDate(summary.reset_at)}${
                  resetInDays === null ? '' : `（还剩 ${Math.max(resetInDays, 0)} 天）`
                }。`
              : '具体日期以你的订单为准。'}
          </span>
        </p>
      </div>

      {forecast ? (
        <div className="flex flex-wrap items-center gap-2">
          <Badge tone={forecast.tone}>{forecast.badge}</Badge>
          <span className="text-fg-muted">{forecast.description}</span>
        </div>
      ) : null}
    </div>
  );
}

/**
 * 「按当前速率还够用多久」（§3.2.7 要求的耗尽预测）。
 *
 * ⚠️ 预测**只在能算的时候给**，算不出来就返回 `null` 而不是显示一个「—」或者猜一个数：
 * 一个错的耗尽预测比没有预测糟得多 —— 用户会照着它安排一个月的用量。
 *
 * 算不出来的三种情况都直接放弃：没有配额上限、没有重置日（不知道周期起点）、
 * 或者周期才刚开始（已过天数 < 1，速率的分母太小，一天的异常会被放大成整月的结论）。
 */
function exhaustionForecast(
  summary: SubscriptionSummary,
  resetInDays: number | null,
): { badge: string; description: string; tone: 'ok' | 'warn' | 'danger' } | null {
  const used = summary.upload_bytes + summary.download_bytes;
  if (summary.total_bytes <= 0) return null;
  if (used >= summary.total_bytes) {
    return { badge: '流量已用完', description: '可以买流量包，或等下一个周期重置。', tone: 'danger' };
  }
  if (resetInDays === null) return null;

  // 周期长度取 30 天（三档套餐的配额口径都是「每 30 天」，见 pricing §1.1）。
  // 已过天数 = 30 − 距重置天数，夹在 [0, 30]。
  const elapsedDays = Math.min(30, Math.max(0, 30 - resetInDays));
  if (elapsedDays < 1) return null;

  const perDay = used / elapsedDays;
  if (perDay <= 0) return null;
  const daysLeft = Math.floor((summary.total_bytes - used) / perDay);

  if (resetInDays <= daysLeft) {
    return {
      badge: '够用到重置',
      description: `按最近 ${elapsedDays} 天的平均速率（约 ${formatBytes(perDay)}/天），这个周期用不完。`,
      tone: 'ok',
    };
  }
  return {
    badge: `约 ${daysLeft} 天后用完`,
    description: `按最近 ${elapsedDays} 天的平均速率（约 ${formatBytes(perDay)}/天）估算，会早于重置日（还有 ${resetInDays} 天）用完。`,
    tone: 'warn',
  };
}

/* ─────────────────────────── 订阅拉取审计 ─────────────────────────── */

/**
 * 「谁在拉我的订阅」。
 *
 * 展示给用户的边际成本为零（这张表本来就要建），价值很高 ——
 * 用户自己就能发现订阅被白嫖，然后去 `/subscribe` 自助重置，不用开工单。
 * 所以空态的动作按钮指向 `/subscribe` 而不是「知道了」。
 */
function FetchLogList({ entries }: { entries: readonly SubscriptionFetchLogEntry[] }) {
  if (entries.length === 0) {
    return (
      <EmptyState
        title="还没有人拉取过你的订阅"
        description="把订阅链接导进客户端之后，每一次拉取都会记在这里：时间、IP、客户端标识。"
        action={
          <LinkButton tone="primary" href="/subscribe">
            去拿订阅链接 <Icon.ArrowRight size={14} />
          </LinkButton>
        }
        secondary="出现你不认识的 IP 时，说明链接可能泄漏了 —— 在订阅页可以一键重置。"
      />
    );
  }

  return (
    <>
      <ul className="divide-y divide-line">
        {entries.map((entry) => (
          <li key={entry.id} className="grid gap-0.5 py-2.5 text-sm sm:grid-cols-[auto_1fr_auto] sm:items-baseline sm:gap-x-3">
            <span className="text-fg-muted">{formatDateTime(entry.request_at)}</span>
            <span className="font-mono text-xs text-fg">{entry.request_ip}</span>
            {/* 第三列是**客户端标识**（UA），不是 IP 归属地。
                §3.2.7 写的是「IP 归属地」，但契约里没有这个字段
                （`SubscriptionFetchLogEntry` 只有 ip / ua / sub_token_*），
                而自己拼一个第三方归属地查询要么把用户 IP 发给第三方、要么编一个不准的结果 ——
                两条都比「只显示 IP」糟。所以 IP 原样显示，这里显示 UA。 */}
            <span className="truncate text-xs text-fg-subtle" title={entry.user_agent ?? undefined}>
              {entry.user_agent ?? '未知客户端'}
              {entry.sub_token_name ? `（${entry.sub_token_name}）` : ''}
            </span>
          </li>
        ))}
      </ul>
      <p className="mt-3 text-xs leading-relaxed text-fg-subtle">
        看到不认识的 IP？订阅链接等同于凭据，
        <a href="/subscribe" className="text-accent hover:underline">
          去订阅页
        </a>
        一键重置就会让旧链接立刻失效。
      </p>
    </>
  );
}

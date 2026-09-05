/**
 * `/dashboard` —— P1。page-inventory §3.1 #5、§3.2.2。
 *
 * 结构照抄竞品（公告轮播 + 订阅卡 + 四快捷入口，这个结构是对的），内容全换。
 *
 * 三条硬要求，接线后各自落在哪：
 *  1. 订阅卡与公告是**两个独立请求，任一失败不影响另一个** —— 所以它们各自持有自己的三态：
 *     `<SubscriptionSection />` 与 `<NoticeSection />` 各调一次 `useSection`。
 *     **不许合并成 `Promise.all` 或整页一个 loading**：那样公告挂掉会把订阅卡一起吞掉，
 *     而这一页正是用户判断「服务还在不在」的第一落点。DashboardPage.test.tsx 钉死了这条。
 *  2. 「重置日 = 订单日」必须明示（tutorials-spec §5：这是高频误解）—— 见 `ResetDayNote`。
 *     **`reset_at` 缺失时也照样说这句话**：被误解的是规则本身，不是某个具体日期。
 *  3. 订阅卡 5xx 时说「**你已连接的设备不受影响**」，不能只说「加载失败」——
 *     控制面故障不得被用户理解为数据面故障（system-design §1）。
 *
 * 错态一律按 **`ErrorCode`** 分支，不按 HTTP 状态码（api-contract §2.3 明令禁止匹配 message，
 * 而按状态码分支是同一类错误的另一种形态）。特别是 **501 `NOT_IMPLEMENTED`**：
 * 它按状态码归一成 `server`(5xx)，走 5xx 文案就会告诉用户「我们这边出了问题、去看状态页」——
 * 那是假的，后端只是还没实现。所以它有独立的一条分支。
 *
 * `getCurrentUser` 这一页**不自己发**：`AuthProvider` 启动时已经取过 `/api/v1/user/me`，
 * 布局层用它显示身份（layouts/AppLayout.tsx）。这里重复发一次只会在最慢的链路上多一次往返。
 */
import { useCallback, useEffect, useState, type ReactNode } from 'react';
import { Link } from 'react-router';
import {
  Badge,
  Button,
  Card,
  CardTitle,
  EmptyState,
  ErrorState,
  Icon,
  LinkButton,
  LoadingState,
  Meter,
  Mono,
  SkeletonCard,
  Stat,
  cx,
  daysUntil,
  formatBytes,
  formatDate,
  percent,
  runtimeConfig,
} from './_imports.ts';
import {
  ApiError,
  getSubscription,
  listNotices,
  type Notice,
  type UserSubscription,
} from '@babelplus/shared/api';
import { api } from '../lib/api.ts';

type SubscriptionSummary = UserSubscription['summary'];

/** §3.2.2：公告轮播取最近 **3** 条（竞品轮播 4 条且没有列表页）。 */
const TOP_NOTICE_LIMIT = 3;

/**
 * 两个 loader 定义在模块级而不是组件里，是为了让它们的**函数标识稳定** ——
 * 放进组件体内每次渲染都是一个新函数，`useSection` 的依赖数组就会每渲染一次重发一次请求。
 */
const loadSubscription = (): Promise<UserSubscription> => getSubscription(api());
const loadTopNotices = (): Promise<{ data: Notice[] }> =>
  listNotices(api(), { limit: TOP_NOTICE_LIMIT });

export default function DashboardPage() {
  const cfg = runtimeConfig();

  return (
    <>
      <header className="mb-5 sm:mb-6">
        <h1 className="text-xl font-semibold tracking-tight text-fg sm:text-2xl">概览</h1>
        <p className="mt-1 max-w-2xl text-sm text-fg-muted">
          订阅还剩多少、有没有新公告、下一步该点哪里。
        </p>
      </header>

      <div className="space-y-4">
        {/* TODO(P1): 首连未完成的账号，首屏应该是**接入向导**而不是这张仪表盘
            （user-journey §1 裁决 3：四个平级快捷入口对新用户是四个岔路）。
            判据是「订阅拉取审计表里有没有记录」，不是「有没有付款」。
            现在没做，因为判据要的是 `getSubscriptionFetchLog`（/subscribe 那一页的端点），
            且 §1 裁决 1（首次付款是否挪出首连路径）本身还没裁决。 */}

        <SubscriptionSection />
        <NoticeSection />

        {/* 快捷入口是**静态的，永远渲染** —— 上面两个请求全挂的时候，
            它恰恰是用户仅剩的出路（教程站在站外，面板挂了它还在）。
            把它放进任何一个请求的就绪态里都会在最需要它的时刻消失。 */}
        <Card>
          <CardTitle hint="结构照抄竞品，四个目标全改">快捷入口</CardTitle>
          <div className="grid grid-cols-2 gap-2 sm:grid-cols-4">
            {/* 教程指向**站外**独立域名：面板被封时教程还在（§3.3）。 */}
            {cfg.docsUrl ? (
              <LinkButton href={cfg.docsUrl} external className="flex-col gap-1 py-3 text-xs">
                <Icon.External size={16} />
                查看教程
              </LinkButton>
            ) : (
              <Button className="flex-col gap-1 py-3 text-xs" disabled title="docsUrl 未配置">
                <Icon.External size={16} />
                查看教程
              </Button>
            )}
            <LinkButton href="/subscribe" className="flex-col gap-1 py-3 text-xs">
              <Icon.Link size={16} />
              一键订阅
            </LinkButton>
            <LinkButton href="/plan" className="flex-col gap-1 py-3 text-xs">
              <Icon.Package size={16} />
              续费
            </LinkButton>
            {/* 「遇到问题」指向诊断页而不是工单 —— 先自助，再开单。 */}
            <LinkButton href="/diagnose" className="flex-col gap-1 py-3 text-xs">
              <Icon.Stethoscope size={16} />
              遇到问题
            </LinkButton>
          </div>
        </Card>

        {/* 失联提示条由 SiteFooter 常驻提供（ADR 0003 §5），这里不重复渲染。 */}
      </div>
    </>
  );
}

/* ─────────────────────────── 订阅卡（独立请求 ①） ─────────────────────────── */

function SubscriptionSection() {
  const { state, reload } = useSection(loadSubscription);

  if (state.status === 'loading') {
    // 慢提示条只挂在订阅卡上：同屏两条「跨境链路较慢」是噪音，而订阅卡是这一页的主体。
    return (
      <LoadingState slowHint>
        <SkeletonCard lines={4} />
      </LoadingState>
    );
  }

  if (state.status === 'error') {
    // 契约没给这个端点定义 404，但后端真回 RESOURCE_NOT_FOUND 时，
    // 它的含义是「这个账号没有订阅」而不是「出错了」—— 按空态处理，不要显示成故障。
    if (state.error.code === 'RESOURCE_NOT_FOUND') return <NoSubscriptionEmpty />;

    const copy = subscriptionErrorCopy(state.error);
    if (copy.pending) {
      return (
        <PendingNotice
          title="该功能尚未开放"
          description={copy.description}
          requestId={state.error.requestId}
        />
      );
    }
    return (
      <ErrorState
        kind={state.error.kind}
        title={copy.title}
        description={copy.description}
        requestId={state.error.requestId}
        onRetry={reload}
      />
    );
  }

  const summary = state.data.summary;
  if (!hasSubscription(summary)) return <NoSubscriptionEmpty />;
  return <SubscriptionCard summary={summary} />;
}

/**
 * 「有没有订阅」的判据。
 *
 * 契约给 `getUserSubscription` 只定义了 200 / 401 / 500，**没有 404** ——
 * 也就是说没买过套餐的账号拿到的是一个 200 加一份空摘要，而不是一个错误。
 * 所以判据只能落在字段上，且**三个字段全空才算没有订阅**：
 * 只看 `plan_name` 的话，存量订阅在后端一时没回套餐名时会被误判成空态，
 * 于是给一个正在用的用户显示「选一个套餐开始」—— 比显示「暂无订阅」还糟。
 */
function hasSubscription(summary: SubscriptionSummary): boolean {
  return Boolean(summary.plan_name) || summary.total_bytes > 0 || Boolean(summary.expired_at);
}

/** §3.2.2 原话：「这是最重要的空态」。**不显示「暂无订阅」。** */
function NoSubscriptionEmpty() {
  const cfg = runtimeConfig();
  return (
    <EmptyState
      title="选一个套餐开始"
      description="你的账号还没有可用的订阅。选好套餐、付款完成后，这里会出现订阅链接和流量进度。"
      action={
        <LinkButton tone="primary" href="/plan">
          看看套餐 <Icon.ArrowRight size={14} />
        </LinkButton>
      }
      secondary={
        <>
          不确定选哪个？先看
          {cfg.docsUrl ? (
            <a href={cfg.docsUrl} className="mx-1 text-accent hover:underline" rel="noreferrer noopener">
              用量说明
            </a>
          ) : (
            <span className="mx-1">用量说明</span>
          )}
          ，流量比设备数更容易估错。
        </>
      }
    />
  );
}

function SubscriptionCard({ summary }: { summary: SubscriptionSummary }) {
  const used = summary.upload_bytes + summary.download_bytes;
  const hasQuota = summary.total_bytes > 0;
  const usedPercent = percent(used, summary.total_bytes);
  const remainingDays = daysUntil(summary.expired_at);
  // 🔴 过期判定必须比时刻，不能比取整后的天数。daysUntil 走 Math.ceil，
  //    刚过期 0–24 小时的订阅算出来是 -0，而 `-0 < 0` 是 **false** ——
  //    于是一个已经失效的订阅会显示成「生效中 / 还剩 0 天」，
  //    「续费」也不会被置成主按钮。这恰好是用户最需要看到「已到期」的那一天。
  const expiredAtMs = summary.expired_at ? Date.parse(summary.expired_at) : null;
  const expired = expiredAtMs !== null && !Number.isNaN(expiredAtMs) && expiredAtMs <= Date.now();
  const exhausted = hasQuota && used >= summary.total_bytes;
  // user-journey §4.4 最后一行：到期 / 流量耗尽时「续费」置为主按钮。
  const renewIsPrimary = expired || exhausted;
  const atDeviceLimit = summary.device_limit > 0 && summary.device_count >= summary.device_limit;
  const remainingBytes = hasQuota ? Math.max(0, summary.total_bytes - used) : null;

  return (
    <Card>
      <CardTitle
        hint={
          expired ? (
            <Badge tone="danger">已到期</Badge>
          ) : exhausted ? (
            <Badge tone="warn">流量已用完</Badge>
          ) : (
            <Badge tone="ok">生效中</Badge>
          )
        }
      >
        当前订阅
      </CardTitle>

      <p className="text-lg font-semibold tracking-tight text-fg">{summary.plan_name || '未命名套餐'}</p>

      {/* 三个大数字：剩余流量 / 剩余天数 / 设备。数字是这一页的主视觉（design-system.md §3.3）。 */}
      <div className="mt-4 grid grid-cols-2 gap-x-4 gap-y-5 sm:grid-cols-3">
        <Stat
          label="Remaining · 剩余流量"
          value={remainingBytes === null ? '—' : formatBytes(remainingBytes, 1)}
          tone={exhausted ? 'danger' : usedPercent >= 90 ? 'warn' : undefined}
          hint={hasQuota ? `已用 ${formatBytes(used)} / 总 ${formatBytes(summary.total_bytes)}` : '这个套餐没有给出流量上限。'}
        />
        <Stat
          label="Days · 剩余天数"
          value={summary.expired_at ? Math.abs(remainingDays ?? 0) : '∞'}
          unit={summary.expired_at ? (expired ? '天前到期' : '天') : undefined}
          tone={expired ? 'danger' : undefined}
          hint={summary.expired_at ? `到期 ${formatDate(summary.expired_at)}` : '不限时套餐，不会到期'}
        />
        <Stat
          label="Devices · 设备"
          value={summary.device_count}
          unit={`/ ${summary.device_limit || '—'}`}
          hint={
            <span className="flex flex-wrap items-center gap-2">
              {/* §3.2.3 第 3 条：达到上限**不显示红色报错**，这是升档转化位不是错误提示。 */}
              {atDeviceLimit ? <Badge tone="info">已达上限，可升级套餐或踢掉一个</Badge> : null}
              <Link to="/subscribe" className="text-accent hover:underline">
                管理设备
              </Link>
            </span>
          }
        />
      </div>

      {/* 进度表紧跟大数字；`total_bytes = 0` 到底是「不限流量」还是「还没配额度」契约没写明，
          猜错任何一边都会让用户按错误的预期用流量，所以那种情况只在上面的 hint 里陈述事实。 */}
      {hasQuota ? (
        <div className="mt-4 flex items-center gap-3">
          <Meter percent={usedPercent} label="流量使用进度" className="flex-1" />
          <Mono className="text-[11px] text-fg-subtle">{Math.round(usedPercent)}%</Mono>
        </div>
      ) : null}

      <dl className="mt-4 space-y-3 text-sm">
        <ResetDayNote resetAt={summary.reset_at} />
      </dl>

      <div className="mt-5 flex flex-wrap gap-2">
        <LinkButton tone={renewIsPrimary ? 'default' : 'primary'} href="/subscribe">
          <Icon.Link size={14} /> 订阅链接
        </LinkButton>
        <LinkButton tone={renewIsPrimary ? 'primary' : 'default'} href="/plan">
          <Icon.Package size={14} /> 续费
        </LinkButton>
      </div>
    </Card>
  );
}

/**
 * 硬要求 ②：「重置日 = 订单日」必须明示。
 *
 * tutorials-spec §5 把它列为**高频误解** —— 用户默认以为流量在每月 1 号重置，
 * 于是月底把流量用光后等到 1 号，发现还是没恢复，然后开工单。
 * 所以这句话**与日期是否可得无关**，`reset_at` 缺失时照样要说，
 * 只是把「下一次是哪天」降级成「以订单为准」。
 */
function ResetDayNote({ resetAt }: { resetAt: string | undefined }) {
  return (
    <div>
      <dt className="text-fg-muted">流量重置</dt>
      <dd className="mt-0.5 text-fg">
        <span className="font-medium">重置日 = 你的下单日，不是每月 1 号。</span>{' '}
        <span className="text-fg-muted">
          {resetAt ? `下一次重置：${formatDate(resetAt)}。` : '具体日期以你的订单为准。'}
        </span>
      </dd>
    </div>
  );
}

/**
 * 订阅卡的 `ErrorCode` → 文案。**订阅卡里唯一按 code 分支的地方。**
 *
 * 硬要求 ③ 在 `server` 那一支：5xx 必须说「你已连接的设备不受影响」，
 * 不能只说「加载失败」。这句话不是安慰，是事实 —— 面板（控制面）与节点（数据面）
 * 是两套东西，把前者的故障说成后者会让用户以为服务没了并去申请退款。
 */
function subscriptionErrorCopy(error: ApiError): SectionErrorCopy {
  switch (error.code) {
    case 'NOT_IMPLEMENTED':
      return {
        pending: true,
        description:
          '后端的订阅接口还没实现（当前返回 501）。这只是面板少了一块显示，不影响你已经在用的订阅。',
      };
    case 'QUOTA_RATE_LIMITED':
      return {
        pending: false,
        title: '刷得有点快',
        description: error.retryAfterSeconds
          ? `请求太频繁了，${error.retryAfterSeconds} 秒后再试。你的订阅没有任何变化。`
          : '请求太频繁了，稍后再试。你的订阅没有任何变化。',
      };
    default:
      break;
  }
  switch (error.kind) {
    case 'server':
      return {
        pending: false,
        title: '暂时读不到订阅状态',
        description: '订阅本身没有变化，你已连接的设备不受影响 —— 这只是面板读不到数据。',
      };
    case 'offline':
      return {
        pending: false,
        title: '连不上面板',
        description: '订阅本身没有变化，你已连接的设备不受影响 —— 这只是面板连不上。',
      };
    default:
      // 401 / 403 / 4xx 的处置是全站统一的，用 ERROR_COPY 的默认文案，这一页不自己发挥。
      return { pending: false };
  }
}

/* ─────────────────────────── 公告（独立请求 ②） ─────────────────────────── */

function NoticeSection() {
  const { state, reload } = useSection(loadTopNotices);

  if (state.status === 'loading') {
    return <SkeletonCard lines={2} />;
  }

  if (state.status === 'error') {
    // 公告兼作**域名广播位**（§3.2.9），所以它的失败**不能静默吞掉** ——
    // 「这里什么都没有」与「这里有一条域名变更但你没看到」对用户是天壤之别。
    const copy = noticeErrorCopy(state.error);
    if (copy.pending) {
      return (
        <PendingNotice
          title="该功能尚未开放"
          description={copy.description}
          requestId={state.error.requestId}
        />
      );
    }
    return (
      <ErrorState
        kind={state.error.kind}
        title={copy.title}
        description={copy.description}
        requestId={state.error.requestId}
        onRetry={reload}
        extra={<LinkButton href="/notice">去公告页看看</LinkButton>}
      />
    );
  }

  const notices = sortNotices(state.data.data);

  // §3.2.2：**无公告时整块隐藏，不占位。** 这是 §2.2「空态必须给出下一步动作」的一条
  // 明文例外 —— 公告为空是常态，给它一个空态卡片等于每天占着首屏最贵的位置说「没事发生」。
  if (notices.length === 0) return null;

  return (
    <Card>
      <CardTitle hint={`最近 ${notices.length} 条`}>公告</CardTitle>
      <ul className="space-y-2">
        {notices.map((notice) => (
          <li key={notice.id}>
            <Link
              to="/notice"
              className={cx(
                'flex flex-wrap items-baseline gap-x-2 gap-y-1 rounded-lg border px-3 py-2 text-sm hover:bg-surface-alt',
                notice.pinned ? 'border-accent/40 bg-accent/5' : 'border-line',
              )}
            >
              {notice.pinned ? <Badge tone="info">置顶</Badge> : null}
              <span className="min-w-0 flex-1 truncate text-fg">{notice.title}</span>
              <span className="text-xs text-fg-subtle">{formatDate(notice.published_at)}</span>
            </Link>
          </li>
        ))}
      </ul>
      <div className="mt-3">
        <Link to="/notice" className="text-sm text-accent hover:underline">
          查看全部公告
        </Link>
      </div>
    </Card>
  );
}

/**
 * 公告块的 `ErrorCode` → 文案。**与订阅卡的那张表分开写，不合并** ——
 * 两者说的不是一件事：订阅卡失败要安抚「服务还在」，公告失败要提醒「你可能漏看了域名变更」。
 * 合成一张表的那一刻，两页的文案就会开始互相迁就。
 */
function noticeErrorCopy(error: ApiError): SectionErrorCopy {
  if (error.code === 'NOT_IMPLEMENTED') {
    return {
      pending: true,
      description:
        '后端的公告接口还没实现（当前返回 501）。在它开放之前，域名变更这类通知只会通过邮件发出 —— 请留意收件箱与垃圾箱。',
    };
  }
  return {
    pending: false,
    title: '公告没能加载',
    description:
      '公告里可能有**域名变更**这类你必须看到的通知，所以这次失败没有被忽略。稍后重试一次，或直接打开公告页。',
  };
}

/* ─────────────────────────── 公用小件 ─────────────────────────── */

interface SectionErrorCopy {
  /** `true` = 后端还没实现。用中性提示而不是红色错误：那不是故障，用户也无事可做。 */
  pending: boolean;
  /** 留空表示用 `ERROR_COPY` 的全站默认文案。 */
  title?: string;
  description?: ReactNode;
}

/**
 * 「该功能尚未开放」。
 *
 * 后端当前对多数端点回 **501 `NOT_IMPLEMENTED`**（api/cmd/server/main.go）。
 * 501 归一成 `kind = 'server'`，若不单独分一支，用户会看到「我们这边出了问题、去看状态页」——
 * 状态页上什么都不会有，因为根本没有故障。**没做完就说没做完。**
 */
function PendingNotice({
  title,
  description,
  requestId,
}: {
  title: string;
  description: ReactNode;
  requestId?: string | undefined;
}) {
  return (
    <Card className="border-dashed">
      <h3 className="text-base font-semibold text-fg">{title}</h3>
      <p className="mt-1 text-sm leading-relaxed text-fg-muted">{description}</p>
      {requestId ? (
        <p className="mt-2 font-mono text-xs text-fg-subtle">请求号 {requestId}</p>
      ) : null}
    </Card>
  );
}

/** 置顶优先，其次按发布时间倒序。日期解不出来时当 0，**不让一条坏数据把整个列表顺序搅乱**。 */
function sortNotices(list: readonly Notice[]): Notice[] {
  return [...list].sort((a, b) => {
    if (Boolean(a.pinned) !== Boolean(b.pinned)) return a.pinned ? -1 : 1;
    return publishedTime(b) - publishedTime(a);
  });
}

function publishedTime(notice: Notice): number {
  const t = Date.parse(notice.published_at);
  return Number.isNaN(t) ? 0 : t;
}

type SectionState<T> =
  | { status: 'loading' }
  | { status: 'ready'; data: T }
  | { status: 'error'; error: ApiError };

/**
 * 一个区块的三态 + 重试。
 *
 * **刻意不抽到 shared/**：请求缓存与状态管理的选型还没裁决（page-inventory §8），
 * 现在抽一个「全站统一的 useQuery」等于替以后的人做了那个决定。
 * 这里只要三个字段，二十行就够，等选型定了再统一换掉。
 *
 * `load` 必须是**标识稳定**的函数（本文件里都定义在模块级）——
 * 传一个渲染时新建的闭包进来会让 effect 每渲染一次重发一次请求。
 */
function useSection<T>(load: () => Promise<T>): {
  state: SectionState<T>;
  reload: () => void;
} {
  const [state, setState] = useState<SectionState<T>>({ status: 'loading' });
  const [attempt, setAttempt] = useState(0);

  useEffect(() => {
    // 重试或卸载后，旧请求的迟到响应不许再覆盖新状态。
    let cancelled = false;
    setState({ status: 'loading' });
    load().then(
      (data) => {
        if (!cancelled) setState({ status: 'ready', data });
      },
      (cause: unknown) => {
        if (!cancelled) setState({ status: 'error', error: asApiError(cause) });
      },
    );
    return () => {
      cancelled = true;
    };
  }, [load, attempt]);

  const reload = useCallback(() => setAttempt((n) => n + 1), []);
  return { state, reload };
}

/** 非 `ApiError` 的意外（组件里抛的 TypeError 之类）也要有 `kind`，否则错误态无从渲染。 */
function asApiError(cause: unknown): ApiError {
  if (cause instanceof ApiError) return cause;
  return new ApiError({ status: 0, code: 'UNKNOWN', message: '请求失败', cause });
}

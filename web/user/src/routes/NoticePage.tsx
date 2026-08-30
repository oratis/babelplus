/**
 * `/notice` —— P2，但**它本身是恢复路径**。page-inventory §3.1 #17、§3.2.9。
 *
 * 存在的真正理由：公告兼作**域名广播位**。
 * 竞品的置顶公告第一条就是「官网域名（防失联）」，2023-06-03 发布并长期置顶。
 * dashboard 只轮播 3 条，历史公告必须可查 —— 否则去年那条域名公告就找不回来了。
 *
 * 因为它是恢复路径，所以这一页比别的页更该：体积小、依赖少、失败时仍然显示备用域名列表。
 *
 * 接线后落在代码里的两条硬要求：
 *  1. 🔴 **置顶独立于分页**。置顶区渲染的是**已加载的全部页**里 `pinned` 为真的那些，
 *     不是「第一页里置顶的那些」。翻到第二页才出现的那条域名公告照样排在最前 ——
 *     它是这一页存在的理由，沉到底部等于没有。NoticePage.test.tsx 钉死了这一条。
 *  2. 🔴 **失败不能静默吞掉**。「这里什么都没有」与「这里有一条域名变更但你没看到」
 *     对用户是天壤之别，所以错误态必须点名说出「你可能漏看了域名变更」，
 *     并且备用域名列表在任何一态下都渲染（正文里那一份，不只靠页脚）。
 *
 * 三态**按 `ErrorCode` 分支**，不按 HTTP 状态码：501 与 500 归一后 `kind` 都是 `server`，
 * 但一个是「还没做」、一个是「炸了」，对用户是两句不同的话。
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
  MirrorDomainList,
  cx,
} from './_imports.ts';
import { formatDate } from './_imports.ts';
import { listNotices, type ApiError, type Meta, type Notice } from '@babelplus/shared/api';
import { api } from '../lib/api.ts';
import {
  ListSkeleton,
  PendingNotice,
  QueryError,
  fallbackErrorCopy,
  isNotImplemented,
  toApiErrorLike,
  useResource,
  type ErrorCopy,
} from './subscribe/_shared.tsx';

/**
 * 一页多少条。公告是低频内容，20 条通常就是全部历史 ——
 * 不做无限滚动：这一页的读者往往是在找**一年前那条域名公告**，
 * 「加载更多」是可以停下来数一数的，滚动不是。
 */
const PAGE_SIZE = 20;

interface NoticePage {
  readonly items: readonly Notice[];
  readonly meta: Meta;
}

function loadNotices(cursor: string | null): Promise<NoticePage> {
  const query = cursor === null ? { limit: PAGE_SIZE } : { limit: PAGE_SIZE, cursor };
  return listNotices(api(), query).then((envelope) => ({ items: envelope.data, meta: envelope.meta }));
}

export default function NoticePage() {
  const first = useResource(() => loadNotices(null));

  // 后续页单独存。第一页重拉时它们必须一起作废 —— 否则「重试」之后
  // 屏幕上会是「新的第一页 + 旧的第二页」，而那两份数据之间没有任何一致性保证。
  const [more, setMore] = useState<readonly Notice[]>([]);
  const [moreMeta, setMoreMeta] = useState<Meta | null>(null);
  const [morePending, setMorePending] = useState(false);
  const [moreError, setMoreError] = useState<ApiError | null>(null);

  function resetPaging(): void {
    setMore([]);
    setMoreMeta(null);
    setMoreError(null);
  }

  function retry(): void {
    resetPaging();
    first.reload();
  }

  async function loadMore(cursor: string): Promise<void> {
    if (morePending) return;
    setMorePending(true);
    setMoreError(null);
    try {
      const page = await loadNotices(cursor);
      // 去重按 id：游标分页在边界上重复一条是可能的，而重复的 key 在 React 里是一个
      // 不报错、只表现为「这条公告闪了一下」的缺陷。
      setMore((prev) => {
        const seen = new Set(prev.map((n) => n.id));
        return [...prev, ...page.items.filter((n) => !seen.has(n.id))];
      });
      setMoreMeta(page.meta);
    } catch (cause) {
      setMoreError(toApiErrorLike(cause, '加载更多公告失败'));
    } finally {
      setMorePending(false);
    }
  }

  return (
    <>
      <header className="mb-5 sm:mb-6">
        <h1 className="text-xl font-semibold tracking-tight text-fg sm:text-2xl">公告</h1>
        <p className="mt-1 max-w-2xl text-sm text-fg-muted">
          服务变更、维护窗口、域名更新都发在这里。
        </p>
      </header>

      <div className="space-y-4">
        {first.state.status === 'loading' ? (
          <Card>
            <CardTitle>全部公告</CardTitle>
            <ListSkeleton rows={4} />
          </Card>
        ) : null}

        {first.state.status === 'error' ? <NoticeError error={first.state.error} onRetry={retry} /> : null}

        {first.state.status === 'ready' ? (
          <NoticeList
            first={first.state.data}
            more={more}
            meta={moreMeta ?? first.state.data.meta}
            morePending={morePending}
            moreError={moreError}
            onLoadMore={(cursor) => void loadMore(cursor)}
          />
        ) : null}

        {/* 这一页是恢复路径，所以正文里也放一次备用域名，不只靠页脚 ——
            而且它在**三态之外**渲染：公告读不到的时候，恰恰是最需要备用域名的时候。 */}
        <MirrorDomainList />
      </div>
    </>
  );
}

/* ───────────────────────────── 列表 ───────────────────────────── */

function NoticeList({
  first,
  more,
  meta,
  morePending,
  moreError,
  onLoadMore,
}: {
  first: NoticePage;
  more: readonly Notice[];
  meta: Meta;
  morePending: boolean;
  moreError: ApiError | null;
  onLoadMore: (cursor: string) => void;
}) {
  /**
   * 展开状态。Set 里存的是**被用户翻转过**的 id，不是「展开的 id」——
   * 因为默认值本身跟着 `pinned` 走（见 `isOpen`），存「展开的」就没法表达
   * 「这条置顶的我收起来了」。
   */
  const [toggled, setToggled] = useState<ReadonlySet<number>>(() => new Set<number>());

  const all = dedupe([...first.items, ...more]);
  const pinned = all.filter((n) => n.pinned).sort(byPublishedDesc);
  const rest = all.filter((n) => !n.pinned).sort(byPublishedDesc);

  if (all.length === 0) {
    return (
      <EmptyState
        title="还没有公告"
        description="有服务变更或域名更新时会发在这里，同时也会给你发邮件。"
        action={
          <LinkButton tone="primary" href="/dashboard">
            回到概览 <Icon.ArrowRight size={14} />
          </LinkButton>
        }
      />
    );
  }

  /** 置顶默认展开，其余默认收起；`toggled` 表示「与默认相反」。 */
  function isOpen(notice: Notice): boolean {
    return Boolean(notice.pinned) !== toggled.has(notice.id);
  }

  function toggle(id: number): void {
    setToggled((prev) => {
      const next = new Set(prev);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });
  }

  // 两个字段都要满足才给「加载更多」：契约说无更多数据时 `next_cursor` 为 null、
  // `has_more` 为 false，但只看其中一个的写法会在另一个先出问题时把用户卡在一个死按钮上。
  const nextCursor = meta.has_more === true && meta.next_cursor ? meta.next_cursor : null;
  const moreCopy = moreError === null ? null : noticeErrorCopy(moreError);

  return (
    <>
      {/* 🔴 置顶区独立成块，且**永远在最前** —— 它装的是「已加载的全部页」里的置顶公告，
          不是第一页的。域名广播那条不会因为它在第三页而沉到底部。 */}
      {pinned.length > 0 ? (
        <Card>
          <CardTitle hint="始终置顶，不随翻页下沉">置顶</CardTitle>
          <ul className="space-y-2">
            {pinned.map((notice) => (
              <NoticeItem
                key={notice.id}
                notice={notice}
                open={isOpen(notice)}
                onToggle={() => toggle(notice.id)}
              />
            ))}
          </ul>
        </Card>
      ) : null}

      <Card>
        <CardTitle hint={`共 ${rest.length} 条`}>全部公告</CardTitle>

        {rest.length === 0 ? (
          <p className="text-sm text-fg-muted">除了上面的置顶公告，没有别的了。</p>
        ) : (
          <ul className="space-y-2">
            {rest.map((notice) => (
              <NoticeItem
                key={notice.id}
                notice={notice}
                open={isOpen(notice)}
                onToggle={() => toggle(notice.id)}
              />
            ))}
          </ul>
        )}

        {/* 翻页失败也不静默：这一次翻页可能正是在往回找那条域名公告。 */}
        {moreCopy ? (
          <p role="alert" className="mt-3 rounded-lg border border-warn/30 bg-warn/10 px-3 py-2 text-sm text-warn">
            {moreCopy.title}：{moreCopy.description}
          </p>
        ) : null}

        {nextCursor ? (
          <div className="mt-3">
            <Button onClick={() => onLoadMore(nextCursor)} disabled={morePending}>
              {morePending ? '正在加载…' : '加载更早的公告'}
            </Button>
          </div>
        ) : null}
      </Card>
    </>
  );
}

function NoticeItem({
  notice,
  open,
  onToggle,
}: {
  notice: Notice;
  open: boolean;
  onToggle: () => void;
}) {
  const bodyId = `notice-body-${notice.id}`;
  return (
    <li
      className={cx(
        'rounded-lg border',
        // 置顶的视觉区分度是产品要求：「域名变更」和「本周维护」不是一个重要级别。
        // 用 `pinned` 而不是标题关键词判断 —— 关键词匹配会把「域名备案说明」这类误判成广播，
        // 而 `pinned` 是运营**显式**给出的信号。
        notice.pinned ? 'border-accent/40 bg-accent/5' : 'border-line',
      )}
    >
      <button
        type="button"
        onClick={onToggle}
        aria-expanded={open}
        aria-controls={bodyId}
        className="flex w-full flex-wrap items-baseline gap-x-2 gap-y-1 px-3 py-2.5 text-left"
      >
        {notice.pinned ? <Badge tone="info">置顶</Badge> : null}
        <span className="min-w-0 flex-1 text-sm font-medium text-fg">{notice.title}</span>
        <span className="text-xs text-fg-subtle">{formatDate(notice.published_at)}</span>
        <span className="text-xs text-accent">{open ? '收起' : '展开'}</span>
      </button>
      {open ? (
        // `whitespace-pre-wrap`：公告正文是纯文本，后台写的换行必须保留。
        // **不解析 Markdown / HTML** —— 公告是后台可写内容，渲染成 HTML 就是一个存储型 XSS。
        <div id={bodyId} className="border-t border-line px-3 py-2.5 text-sm leading-relaxed whitespace-pre-wrap text-fg-muted">
          {notice.content}
        </div>
      ) : null}
    </li>
  );
}

/* ───────────────────────────── 错误 ───────────────────────────── */

function NoticeError({ error, onRetry }: { error: ApiError; onRetry: () => void }) {
  if (isNotImplemented(error)) {
    return (
      <PendingNotice what="公告" requestId={error.requestId}>
        在它开放之前，<strong className="font-medium text-fg">域名变更这类通知只会通过邮件发出</strong>
        —— 请留意收件箱与垃圾箱。下面的备用域名列表不依赖这个接口，仍然可用。
      </PendingNotice>
    );
  }
  return (
    <QueryError error={error} what="公告" copy={noticeErrorCopy(error)} onRetry={onRetry} />
  );
}

/**
 * 公告的 `ErrorCode` → 文案。**这一页唯一按 code 分支的地方。**
 *
 * 与 dashboard 上那张公告文案表**刻意分开写**：那里公告只是首屏的一块，
 * 失败时把人引到这一页就够了；而这里已经是终点，失败必须把「你可能漏看了什么」说完整。
 * 合成一张表的那一刻，两边的文案就会开始互相迁就。
 */
function noticeErrorCopy(error: ApiError): ErrorCopy {
  if (error.code === 'NOT_IMPLEMENTED') return fallbackErrorCopy(error);
  const base = fallbackErrorCopy(error);
  switch (error.kind) {
    case 'server':
    case 'offline':
      return {
        title: '公告没能加载',
        description:
          '公告里可能有域名变更这类你必须看到的通知，所以这次失败没有被当作「暂无数据」忽略掉。稍后重试一次；下面的备用域名列表不依赖这个接口，现在就能用。',
      };
    default:
      return base;
  }
}

/* ───────────────────────────── 小工具 ───────────────────────────── */

function dedupe(list: readonly Notice[]): Notice[] {
  const seen = new Set<number>();
  const out: Notice[] = [];
  for (const notice of list) {
    if (seen.has(notice.id)) continue;
    seen.add(notice.id);
    out.push(notice);
  }
  return out;
}

/** 日期解不出来时当 0，**不让一条坏数据把整个列表顺序搅乱**。 */
function byPublishedDesc(a: Notice, b: Notice): number {
  return publishedTime(b) - publishedTime(a);
}

function publishedTime(notice: Notice): number {
  const t = Date.parse(notice.published_at);
  return Number.isNaN(t) ? 0 : t;
}

/**
 * 公告页的接线测试。
 *
 * 每个用例为什么必须存在：
 *
 *  1. 🔴 **置顶独立于分页** —— 这一页最容易被改错的一条，而且改错之后**毫无症状**。
 *     「把置顶排前面」最自然的写法是对**当前这一页**排序（`[...page].sort(pinnedFirst)`），
 *     单页时它和正确实现的表现一模一样，测试也照样绿。等公告攒到第二页，
 *     那条 2023 年发布、长期置顶的**域名广播**就沉到「加载更早」后面去了 ——
 *     而它正是这一页存在的全部理由（§3.2.9：竞品置顶第一条即「官网域名（防失联）」）。
 *     这个用例让第二页里的置顶公告必须出现在**所有非置顶之前**。
 *
 *  2. 🔴 **失败不许静默** —— 公告兼作域名广播位，
 *     「这里什么都没有」与「这里有一条域名变更但你没看到」对用户是天壤之别。
 *     所以错误态必须点名说出「可能有域名变更」，而不是退化成一个空列表；
 *     同时备用域名列表**在错误态下照样渲染** —— 读不到公告的时刻恰恰是最需要它的时刻。
 *
 *  3. **501 说「该功能尚未开放」**，并且要说清后果：在它开放之前，
 *     域名变更这类通知只会走邮件。501 归一成 5xx，只按状态码分支会说成服务故障。
 *
 *  4. **空态给下一步动作**（§2.2），不是「暂无公告」。
 *
 *  5. **置顶默认展开、其余默认收起** —— 域名广播那条必须一眼能读到正文，
 *     否则「可查」等于「要多点一下才可查」。判据用的是 `pinned` 而不是标题关键词：
 *     关键词匹配会把「域名备案说明」这类误判成广播。
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { MemoryRouter } from 'react-router';
import { resetRuntimeConfig } from '@babelplus/shared';
import { resetApiClientForTests } from '../lib/api.ts';
import { resetSessionForTests } from '../lib/session.ts';
import NoticePage from './NoticePage.tsx';

const REQUEST_ID = '01K2NOTICENOTICENOTICENOTI';

function jsonResponse(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

function errorResponse(status: number, code: string, message: string): Response {
  return jsonResponse(status, { error: { code, message }, meta: { request_id: REQUEST_ID } });
}

function noticesResponse(data: unknown[], meta: Record<string, unknown> = {}): Response {
  return jsonResponse(200, { data, meta: { request_id: REQUEST_ID, ...meta } });
}

const RECENT = [
  { id: 11, title: '本周维护窗口', content: '周四凌晨 03:00–04:00 会有短暂中断。', published_at: '2026-08-20T00:00:00Z' },
  { id: 12, title: '新增日本线路', content: '东京 02 已上线。', published_at: '2026-08-10T00:00:00Z' },
];

/** 长期置顶的那条域名广播 —— 在真实数据里它**很旧**，所以它总是落在后面的页里。 */
const DOMAIN_BROADCAST = {
  id: 1,
  title: '官网域名（防失联）',
  content: '主域名被封时改用镜像域名，列表见本页底部。',
  pinned: true,
  published_at: '2023-06-03T00:00:00Z',
};

/** 按调用顺序依次返回给定的响应。用来模拟「第一页 → 第二页」。 */
function stubNoticePages(pages: Array<() => Response>): { calls: string[] } {
  const calls: string[] = [];
  let index = 0;
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: string | URL | Request) => {
      const url = new URL(String(input));
      if (url.pathname !== '/api/v1/notices') throw new Error(`未预期的请求：${url.pathname}`);
      calls.push(url.search);
      const page = pages[Math.min(index, pages.length - 1)];
      index += 1;
      if (!page) throw new Error('没有更多预设响应');
      return page();
    }),
  );
  return { calls };
}

function renderNotices() {
  return render(
    <MemoryRouter initialEntries={['/notice']}>
      <NoticePage />
    </MemoryRouter>,
  );
}

beforeEach(() => {
  resetRuntimeConfig();
  resetSessionForTests();
  resetApiClientForTests();
  window.sessionStorage.clear();
  delete window.__BP_RUNTIME_CONFIG__;
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  delete window.__BP_RUNTIME_CONFIG__;
});

describe('NoticePage', () => {
  it('成功：按发布时间倒序列出，置顶单独成块', async () => {
    stubNoticePages([() => noticesResponse([...RECENT, DOMAIN_BROADCAST], { has_more: false })]);
    renderNotices();

    await waitFor(() => expect(screen.getByText('官网域名（防失联）')).toBeTruthy());
    // 「置顶」出现两次：置顶区的标题，以及那条公告自己的徽章。
    expect(screen.getAllByText('置顶').length).toBeGreaterThanOrEqual(2);
    expect(screen.getByText('本周维护窗口')).toBeTruthy();
    expect(screen.getByText('共 2 条')).toBeTruthy();
  });

  it('🔴 第二页里的置顶公告仍然排在所有非置顶之前，不会因为翻页而沉底', async () => {
    stubNoticePages([
      // 第一页：只有近期的普通公告，**没有**置顶。
      () => noticesResponse(RECENT, { has_more: true, next_cursor: 'cursor-page-2' }),
      // 第二页：更早的历史里才出现那条长期置顶的域名广播。
      () => noticesResponse([DOMAIN_BROADCAST], { has_more: false }),
    ]);
    const { container } = renderNotices();

    await waitFor(() => expect(screen.getByText('本周维护窗口')).toBeTruthy());
    // 第一页里没有置顶公告，所以此刻置顶区不存在。
    expect(screen.queryByText('官网域名（防失联）')).toBeNull();

    fireEvent.click(screen.getByText('加载更早的公告'));
    await waitFor(() => expect(screen.getByText('官网域名（防失联）')).toBeTruthy());

    const text = container.textContent ?? '';
    const pinnedAt = text.indexOf('官网域名（防失联）');
    const firstPlainAt = text.indexOf('本周维护窗口');
    expect(pinnedAt).toBeGreaterThanOrEqual(0);
    // 它是第二页才拿到的，却必须排在第一页那些普通公告的**前面**。
    expect(pinnedAt).toBeLessThan(firstPlainAt);
    expect(screen.getByText('始终置顶，不随翻页下沉')).toBeTruthy();
  });

  it('置顶默认展开正文；普通公告默认收起，点开才显示', async () => {
    stubNoticePages([() => noticesResponse([...RECENT, DOMAIN_BROADCAST], { has_more: false })]);
    renderNotices();

    await waitFor(() => expect(screen.getByText('官网域名（防失联）')).toBeTruthy());
    // 置顶的正文一进来就能读到 —— 域名广播不该需要多点一下。
    expect(screen.getByText(/主域名被封时改用镜像域名/)).toBeTruthy();
    // 普通公告的正文不在 DOM 里（不是「在 DOM 里但隐藏」）。
    expect(screen.queryByText(/周四凌晨 03:00–04:00/)).toBeNull();

    fireEvent.click(screen.getByText('本周维护窗口'));
    expect(screen.getByText(/周四凌晨 03:00–04:00/)).toBeTruthy();
  });

  it('空：给「回到概览」，不是「暂无公告」了事', async () => {
    stubNoticePages([() => noticesResponse([], { has_more: false })]);
    renderNotices();

    await waitFor(() => expect(screen.getByText('还没有公告')).toBeTruthy());
    expect(screen.getByText(/回到概览/)).toBeTruthy();
    expect(screen.getByText(/同时也会给你发邮件/)).toBeTruthy();
  });

  it('🔴 500：说明「可能有域名变更」，且备用域名列表照样渲染', async () => {
    window.__BP_RUNTIME_CONFIG__ = {
      mirrorDomains: [{ label: '镜像 1', url: 'https://mirror-1.example' }],
    };
    stubNoticePages([() => errorResponse(500, 'INTERNAL_ERROR', '内部错误')]);
    renderNotices();

    await waitFor(() => expect(screen.getByText('公告没能加载')).toBeTruthy());
    // 失败没有被当成「暂无数据」吞掉：必须点名说出用户可能漏看了什么。
    expect(screen.getByText(/公告里可能有域名变更这类你必须看到的通知/)).toBeTruthy();
    // 恢复路径：读不到公告的时刻恰恰是最需要备用域名的时刻。
    expect(screen.getByText('https://mirror-1.example')).toBeTruthy();
  });

  it('501 NOT_IMPLEMENTED：说「尚未开放」，并说清此期间域名变更只走邮件', async () => {
    stubNoticePages([() => errorResponse(501, 'NOT_IMPLEMENTED', '尚未实现')]);
    const { container } = renderNotices();

    await waitFor(() => expect(screen.getByText('该功能尚未开放')).toBeTruthy());
    expect(screen.getByText(/域名变更这类通知只会通过邮件发出/)).toBeTruthy();
    const text = container.textContent ?? '';
    expect(text).not.toContain('我们这边出了问题');
    expect(text).not.toContain('查看状态页');
  });

  it('403 AUTH_PERMISSION_DENIED（封禁）：说封禁，不说「没有访问权限」', async () => {
    stubNoticePages([() => errorResponse(403, 'AUTH_PERMISSION_DENIED', '账号已被封禁')]);
    const { container } = renderNotices();

    await waitFor(() => expect(screen.getByText('这个账号已被封禁')).toBeTruthy());
    expect(container.textContent ?? '').not.toContain('没有访问权限');
  });
});

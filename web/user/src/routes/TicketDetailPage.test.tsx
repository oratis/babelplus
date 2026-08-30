/**
 * 工单详情页的接线测试。
 *
 * 每个用例为什么必须存在：
 *
 *  1. **`is_internal` 兜底过滤**（`过滤掉标着 internal 的消息`）——
 *     这是这一页唯一一条「删掉之后什么都不会报错，但那是一次安全事故」的规则。
 *     契约里的 `TicketMessage` 根本没有这个字段，所以任何人打开这个文件都会觉得
 *     那行 `.filter(...)` 是多余的，顺手删掉最自然不过。这个用例让那次删除在 CI 里红掉。
 *
 *  2. **失败不清空草稿**（`回复失败时正文一个字都不动`）——
 *     §3.2.6 的错态原话。「提交失败后重置表单」是各种表单库的默认行为，
 *     也是最容易被「顺手规范化」出来的一次重构。用户打了五分钟的字没了，
 *     不会有人报 bug，只会不再提工单。
 *
 *  3. **按 `ErrorCode` 分支，不按 HTTP 状态码** —— 仓库点名的事故来源。
 *     这里钉三条：404 `RESOURCE_NOT_FOUND` 是「找不到这个工单」而不是故障态；
 *     409 `STATE_CONFLICT` 要说「状态已经变了」并给刷新，而不是让人重试；
 *     501 `NOT_IMPLEMENTED` 要说「尚未开放」而不是「我们这边出了问题、去看状态页」
 *     （501 按状态码归一成 5xx，只按状态码分支必然说错）。
 *
 *  4. **关单要二次确认** —— 关单是用户能对自己工单做的唯一破坏性动作，
 *     关掉之后回复端点直接 409。第一次点击就发请求的实现看不出任何问题，直到有人误点。
 *
 *  5. **回复成功不重拉整个会话** —— 三态纪律：写操作成功后把整段会话打回 loading，
 *     用户刚打完字眼前变成骨架屏，会以为回复没发出去。用例断言 GET 只发了一次。
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { MemoryRouter, Route, Routes } from 'react-router';
import { resetRuntimeConfig } from '@babelplus/shared';
import { resetApiClientForTests } from '../lib/api.ts';
import { resetSessionForTests } from '../lib/session.ts';
import TicketDetailPage from './TicketDetailPage.tsx';

const REQUEST_ID = '01K2TICKETTICKETTICKETTICK';

function jsonResponse(status: number, body: unknown, headers: Record<string, string> = {}): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json', ...headers },
  });
}

function errorResponse(status: number, code: string, message: string, headers: Record<string, string> = {}): Response {
  return jsonResponse(status, { error: { code, message }, meta: { request_id: REQUEST_ID } }, headers);
}

const TICKET = {
  public_id: 'T-1',
  subject: 'iPhone 上订阅更新一直失败',
  category: 'subscription',
  status: 'replied',
  level: 2,
  created_at: '2026-08-20T02:00:00Z',
  updated_at: '2026-08-21T03:00:00Z',
  last_reply_at: '2026-08-21T03:00:00Z',
};

const USER_MESSAGE = {
  id: 1,
  author: 'user',
  body: '客户端里报的是 subscription download failed',
  created_at: '2026-08-20T02:00:00Z',
};

const STAFF_MESSAGE = {
  id: 2,
  author: 'staff',
  body: '请确认订阅域名有没有被规则里的代理拦下。',
  created_at: '2026-08-21T03:00:00Z',
};

interface Stubs {
  /** `GET /api/v1/tickets/T-1` */
  detail?: () => Response;
  /** `POST /api/v1/tickets/T-1/messages` */
  reply?: () => Response;
  /** `POST /api/v1/tickets/T-1/close` */
  close?: () => Response;
}

/** 记录每条路径各被打了几次，用来断言「成功回复之后没有重拉会话」。 */
let calls: Array<{ method: string; path: string }> = [];

function stubApi(stubs: Stubs): void {
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: string | URL | Request, init?: RequestInit) => {
      const path = new URL(String(input)).pathname;
      const method = (init?.method ?? 'GET').toUpperCase();
      calls.push({ method, path });

      if (method === 'GET' && path === '/api/v1/tickets/T-1') {
        return (stubs.detail ?? (() => detailResponse()))();
      }
      if (method === 'POST' && path === '/api/v1/tickets/T-1/messages') {
        if (!stubs.reply) throw new Error('用例没有为回复端点准备替身');
        return stubs.reply();
      }
      if (method === 'POST' && path === '/api/v1/tickets/T-1/close') {
        if (!stubs.close) throw new Error('用例没有为关单端点准备替身');
        return stubs.close();
      }
      throw new Error(`未预期的请求：${method} ${path}`);
    }),
  );
}

function detailResponse(messages: unknown[] = [USER_MESSAGE, STAFF_MESSAGE], ticket: unknown = TICKET): Response {
  return jsonResponse(200, { data: { ticket, messages }, meta: { request_id: REQUEST_ID } });
}

function renderDetail() {
  return render(
    <MemoryRouter initialEntries={['/ticket/T-1']}>
      <Routes>
        <Route path="/ticket/:public_id" element={<TicketDetailPage />} />
        <Route path="/ticket" element={<div data-testid="list">工单列表</div>} />
      </Routes>
    </MemoryRouter>,
  );
}

/** 等会话加载完（主题出现即为就绪）。 */
async function waitForThread(): Promise<void> {
  await waitFor(() => expect(screen.getByText(TICKET.subject)).toBeTruthy());
}

function typeReply(text: string): void {
  fireEvent.change(screen.getByLabelText('补充说明'), { target: { value: text } });
}

beforeEach(() => {
  calls = [];
  resetRuntimeConfig();
  resetSessionForTests();
  resetApiClientForTests();
  window.sessionStorage.clear();
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe('TicketDetailPage · 会话', () => {
  it('成功 → 消息按作者分列渲染，状态用「球在谁那边」的说法', async () => {
    stubApi({});
    renderDetail();
    await waitForThread();

    expect(screen.getByText(USER_MESSAGE.body)).toBeTruthy();
    expect(screen.getByText(STAFF_MESSAGE.body)).toBeTruthy();
    // `replied` 的展示词是「客服已回复」——「replied」是数据库里的词，用户要知道的是球在谁那边。
    expect(screen.getAllByText('客服已回复').length).toBeGreaterThan(0);
    expect(screen.getByText('订阅问题')).toBeTruthy();
  });

  it('🔴 API 误发了 is_internal 的消息 → **一个字都不渲染**', async () => {
    // 契约里 TicketMessage 没有 is_internal，后端也只走公共视图 ——
    // 也就是说这个响应在今天是不可能出现的。用例模拟的是**将来某次改动之后**：
    // 换一条查询、改一个视图，类型系统一个字都不会说，而内部备注会直接出现在用户眼前。
    const internal = {
      id: 3,
      author: 'staff',
      body: '内部备注：这个账号疑似共享，先别退款',
      created_at: '2026-08-21T04:00:00Z',
      is_internal: true,
    };
    stubApi({ detail: () => detailResponse([USER_MESSAGE, internal, STAFF_MESSAGE]) });
    renderDetail();
    await waitForThread();

    expect(screen.queryByText(internal.body)).toBeNull();
    expect(screen.getByText(STAFF_MESSAGE.body)).toBeTruthy();
    // 计数也要跟着过滤后的条数走，否则用户会发现「说是 3 条只显示 2 条」。
    expect(screen.getByText('2 条')).toBeTruthy();
  });

  it('空态 → 不是一张空白卡片，给一个「去写第一条」', async () => {
    stubApi({ detail: () => detailResponse([]) });
    renderDetail();
    await waitForThread();

    expect(screen.getByText('这张工单还没有任何消息')).toBeTruthy();
    expect(screen.getByRole('button', { name: '去写第一条' })).toBeTruthy();
  });

  it('404 RESOURCE_NOT_FOUND → 「找不到这个工单」，**不显示成故障**', async () => {
    stubApi({ detail: () => errorResponse(404, 'RESOURCE_NOT_FOUND', '工单不存在') });
    renderDetail();

    await waitFor(() => expect(screen.getByText('找不到这个工单')).toBeTruthy());
    // 故障态才有「重试」与「查看状态页」；单号打错时这两个动作都是白点。
    expect(screen.queryByRole('button', { name: '重试' })).toBeNull();
    expect(screen.getByRole('link', { name: /回到工单列表/ })).toBeTruthy();
  });

  it('501 NOT_IMPLEMENTED → 「该功能尚未开放」，不是「我们这边出了问题」', async () => {
    // 501 按状态码归一成 5xx。只按状态码分支的实现会把「后端还没写」说成服务故障，
    // 并把用户推去一个一切正常的状态页。
    stubApi({ detail: () => errorResponse(501, 'NOT_IMPLEMENTED', '尚未实现') });
    renderDetail();

    await waitFor(() => expect(screen.getByText('该功能尚未开放')).toBeTruthy());
    expect(screen.queryByText('我们这边出了问题')).toBeNull();
  });

  it('5xx → 走统一错误态并能重试', async () => {
    let attempts = 0;
    stubApi({
      detail: () => {
        attempts += 1;
        return attempts === 1
          ? errorResponse(500, 'INTERNAL_ERROR', '内部错误')
          : detailResponse();
      },
    });
    renderDetail();

    await waitFor(() => expect(screen.getByText('我们这边出了问题')).toBeTruthy());
    fireEvent.click(screen.getByRole('button', { name: '重试' }));
    await waitForThread();
  });
});

describe('TicketDetailPage · 回复', () => {
  it('成功 → 追加到会话末尾、清空草稿，且**不重拉整个会话**', async () => {
    const created = { id: 9, author: 'user', body: '换了网络还是不行', created_at: '2026-08-22T01:00:00Z' };
    stubApi({ reply: () => jsonResponse(201, { data: created, meta: { request_id: REQUEST_ID } }) });
    renderDetail();
    await waitForThread();

    typeReply(created.body);
    fireEvent.click(screen.getByRole('button', { name: '发送回复' }));

    await waitFor(() => expect(screen.getByText(created.body)).toBeTruthy());
    expect((screen.getByLabelText('补充说明') as HTMLTextAreaElement).value).toBe('');
    // GET 只发过一次：成功之后是就地补一条，不是把整段会话打回 loading 重拉。
    expect(calls.filter((c) => c.method === 'GET').length).toBe(1);
  });

  it('🔴 失败 → 正文一个字都不动，会话也还在', async () => {
    const draft = '客服问的日志我贴在这里：subscription download failed，试过换网络';
    stubApi({ reply: () => errorResponse(500, 'INTERNAL_ERROR', '写入失败') });
    renderDetail();
    await waitForThread();

    typeReply(draft);
    fireEvent.click(screen.getByRole('button', { name: '发送回复' }));

    await waitFor(() => expect(screen.getByRole('alert')).toBeTruthy());
    expect((screen.getByLabelText('补充说明') as HTMLTextAreaElement).value).toBe(draft);
    expect(screen.getByText(USER_MESSAGE.body)).toBeTruthy();
  });

  it('409 STATE_CONFLICT → 说「状态已经变了」并给刷新，不是让人再点一次', async () => {
    stubApi({ reply: () => errorResponse(409, 'STATE_CONFLICT', '该工单已关闭，无法继续回复') });
    renderDetail();
    await waitForThread();

    typeReply('还有一个问题');
    fireEvent.click(screen.getByRole('button', { name: '发送回复' }));

    await waitFor(() => expect(screen.getByText('工单状态已经变了')).toBeTruthy());
    expect(screen.getByRole('button', { name: '刷新会话' })).toBeTruthy();
    // 409 不是「填写有误」——按状态码分支（4xx → 校验失败）会说成这句。
    expect(screen.queryByText('填写有误')).toBeNull();
  });

  it('422 VALIDATION_FAILED → 逐字段说明，不吞成「未知错误」', async () => {
    stubApi({
      reply: () =>
        jsonResponse(422, {
          error: {
            code: 'VALIDATION_FAILED',
            message: '正文长度必须在 1–20000 个字符之间',
            details: [{ field: 'message', reason: '长度不合法' }],
          },
          meta: { request_id: REQUEST_ID },
        }),
    });
    renderDetail();
    await waitForThread();

    typeReply('太长了');
    fireEvent.click(screen.getByRole('button', { name: '发送回复' }));

    await waitFor(() => expect(screen.getByText('填写有误')).toBeTruthy());
    expect(screen.getByRole('alert').textContent).toContain('message：长度不合法');
  });

  it('429 → 倒计时秒数只能来自 Retry-After', async () => {
    stubApi({ reply: () => errorResponse(429, 'QUOTA_RATE_LIMITED', '太频繁', { 'Retry-After': '30' }) });
    renderDetail();
    await waitForThread();

    typeReply('再问一次');
    fireEvent.click(screen.getByRole('button', { name: '发送回复' }));

    await waitFor(() => expect(screen.getByRole('button', { name: '30 秒后可再试' })).toBeTruthy());
    expect(screen.getByRole('alert').textContent).toContain('30 秒后可以再试');
  });

  it('已关闭的工单 → 没有回复框，给的是新建工单的入口', async () => {
    stubApi({ detail: () => detailResponse([USER_MESSAGE], { ...TICKET, status: 'closed' }) });
    renderDetail();
    await waitForThread();

    expect(screen.queryByLabelText('补充说明')).toBeNull();
    expect(screen.getByText('这张工单已经关闭')).toBeTruthy();
    // 新建入口带上来源与分类，新单的表单会预选同一类并把来源写进正文。
    const href = screen.getByRole('link', { name: /新建工单/ }).getAttribute('href') ?? '';
    // 用 URLSearchParams 读，而不是对着字符串做 contains ——
    // 列表页的 `readTicketOrigin` 拿到的就是这一份解码结果（空格在查询串里是 `+`）。
    const params = new URLSearchParams(href.slice(href.indexOf('?') + 1));
    expect(params.get('category')).toBe('subscription');
    expect(params.get('from')).toBe('已关闭的工单 T-1');
  });
});

describe('TicketDetailPage · 关单', () => {
  it('🔴 第一次点击只展开确认，**不发请求**；确认之后才关', async () => {
    stubApi({
      close: () =>
        jsonResponse(200, { data: { ...TICKET, status: 'closed' }, meta: { request_id: REQUEST_ID } }),
    });
    renderDetail();
    await waitForThread();

    fireEvent.click(screen.getByRole('button', { name: '关闭工单' }));
    expect(calls.filter((c) => c.path.endsWith('/close')).length).toBe(0);
    expect(screen.getByText('确定要关闭这张工单吗')).toBeTruthy();

    fireEvent.click(screen.getByRole('button', { name: '确认关闭' }));

    // 关单成功后就地换状态：徽章变「已关闭」，回复框消失。
    await waitFor(() => expect(screen.queryByLabelText('补充说明')).toBeNull());
    expect(screen.getAllByText('已关闭').length).toBeGreaterThan(0);
    expect(calls.filter((c) => c.path.endsWith('/close')).length).toBe(1);
    // 同样不重拉会话。
    expect(calls.filter((c) => c.method === 'GET').length).toBe(1);
  });

  it('取消确认 → 什么都没发生', async () => {
    stubApi({});
    renderDetail();
    await waitForThread();

    fireEvent.click(screen.getByRole('button', { name: '关闭工单' }));
    fireEvent.click(screen.getByRole('button', { name: '取消' }));

    expect(screen.queryByText('确定要关闭这张工单吗')).toBeNull();
    expect(calls.filter((c) => c.path.endsWith('/close')).length).toBe(0);
    expect(screen.getByLabelText('补充说明')).toBeTruthy();
  });

  it('409（已经关过了）→ 说状态变了，不是「关不掉」', async () => {
    stubApi({ close: () => errorResponse(409, 'STATE_CONFLICT', '该工单已经关闭') });
    renderDetail();
    await waitForThread();

    fireEvent.click(screen.getByRole('button', { name: '关闭工单' }));
    fireEvent.click(screen.getByRole('button', { name: '确认关闭' }));

    await waitFor(() => expect(screen.getByText('工单状态已经变了')).toBeTruthy());
  });
});

// @vitest-environment jsdom

/**
 * 工单会话的测试。
 *
 * 半数用例只为一件事服务：**内部备注不许被当成对用户的回复发出去**。
 * `ticket_messages.is_internal` 是全系统最容易出安全事故的一列，而这类事故不会有人报 bug ——
 * 用户收到了一句「这个先放着，等节点商回复」，最多是觉得我们不专业，我们这边什么都不会知道。
 * 所以这里钉的是**结构**（两个表单、两份正文、两个按钮），不是文案。
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { MemoryRouter, Route, Routes } from 'react-router';
import { resetRuntimeConfig } from '@babelplus/shared';
import { resetAdminApiForTests } from '../lib/api.ts';
import TicketDetailPage from './TicketDetailPage.tsx';

const REQUEST_ID = '01K2TICKETDETAILDETAIL00';

const DETAIL = {
  ticket: {
    public_id: 'BP-7K2M9Q',
    subject: '订阅拉不到节点',
    category: 'node-down',
    status: 'open',
    level: 3,
    created_at: '2026-08-20T02:00:00Z',
    last_reply_at: '2026-08-25T02:00:00Z',
  },
  messages: [
    { id: 1, author: 'user', body: '全部节点都连不上', is_internal: false, created_at: '2026-08-20T02:00:00Z' },
    { id: 2, author: 'staff', body: '我们在查，请稍等', is_internal: false, created_at: '2026-08-21T02:00:00Z' },
    { id: 3, author: 'staff', body: '节点商还没回复', is_internal: true, created_at: '2026-08-22T02:00:00Z' },
  ],
  user_id: 42,
  user_email: 'someone@qq.com',
  context: { client_reported: { ua: 'Clash/1.2', last_fetch_at: '2026-08-20T01:59:00Z' } },
};

interface Call {
  readonly url: string;
  readonly method: string;
  readonly body: unknown;
}

/** 按「方法 + 路径」分发的 fetch 替身。返回值里带一份调用记录，用来断言真正发出去了什么。 */
function stubFetch(handler: (url: URL, method: string, body: unknown) => Response) {
  const calls: Call[] = [];
  const spy = vi.fn(async (input: unknown, init?: RequestInit) => {
    const url = new URL(String(input));
    const method = (init?.method ?? 'GET').toUpperCase();
    let body: unknown;
    if (init?.body !== undefined && init.body !== null) {
      const raw =
        typeof init.body === 'string' ? init.body : new TextDecoder().decode(init.body as ArrayBuffer);
      body = raw === '' ? undefined : JSON.parse(raw);
    }
    calls.push({ url: url.pathname, method, body });
    return handler(url, method, body);
  });
  vi.stubGlobal('fetch', spy);
  return { spy, calls };
}

function ok(data: unknown, status = 200): Response {
  return new Response(JSON.stringify({ data, meta: { request_id: REQUEST_ID } }), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

function errorEnvelope(status: number, code: string, message: string): Response {
  return new Response(JSON.stringify({ error: { code, message }, meta: { request_id: REQUEST_ID } }), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

function renderDetail(id = '1024') {
  return render(
    <MemoryRouter initialEntries={[`/admin/tickets/${id}`]}>
      <Routes>
        <Route path="/admin/tickets/:id" element={<TicketDetailPage />} />
      </Routes>
    </MemoryRouter>,
  );
}

beforeEach(() => {
  resetRuntimeConfig();
  resetAdminApiForTests();
  window.sessionStorage.clear();
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe('工单会话 · 路径参数', () => {
  it('地址里是工单号而不是数字主键时**不发请求**，并说清楚这是契约缺口', async () => {
    const { spy } = stubFetch(() => ok(DETAIL));
    renderDetail('BP-7K2M9Q');

    expect(await screen.findByText(/不是数字主键/)).toBeTruthy();
    // 🔴 发出去只会拿到一个请求校验层的 400，而那看起来像「这个工单不存在」。
    expect(spy).not.toHaveBeenCalled();
  });

  it('404 说的是「找不到这个工单」，不是一句泛泛的加载失败', async () => {
    stubFetch(() => errorEnvelope(404, 'RESOURCE_NOT_FOUND', '工单不存在'));
    renderDetail();

    expect(await screen.findByText('找不到这个工单')).toBeTruthy();
  });

  it('501 → 「尚未开放」，不当成故障', async () => {
    stubFetch(() => errorEnvelope(501, 'NOT_IMPLEMENTED', '尚未实现'));
    renderDetail();

    expect(await screen.findByText('尚未开放')).toBeTruthy();
  });
});

describe('工单会话 · 三种消息三种视觉', () => {
  it('用户消息 / 客服回复 / 内部备注各有各的块，内部备注明说用户看不到', async () => {
    stubFetch(() => ok(DETAIL));
    renderDetail();

    await screen.findByText('全部节点都连不上');
    expect(screen.getAllByTestId('message-user').length).toBe(1);
    expect(screen.getAllByTestId('message-staff').length).toBe(1);
    const internal = screen.getAllByTestId('message-internal');
    expect(internal.length).toBe(1);
    // 只差一个小标签是不够的：这里要求块级的文字标注。
    expect(internal[0]?.textContent).toContain('用户看不到这一条');
    expect(internal[0]?.textContent).toContain('节点商还没回复');
  });

  it('诊断上下文原样只读展示（建单当时的事实，不做任何实时化）', async () => {
    stubFetch(() => ok(DETAIL));
    renderDetail();

    await screen.findByText('诊断上下文');
    expect(screen.getByText(/client_reported/)).toBeTruthy();
  });
});

describe('工单会话 · 两个物理上分开的回复入口', () => {
  it('在内部备注里打字，「发送给用户」仍然点不动 —— 两份正文各自独立', async () => {
    stubFetch(() => ok(DETAIL));
    renderDetail();

    await screen.findByText('全部节点都连不上');
    fireEvent.change(screen.getByLabelText('备注内容'), { target: { value: '等节点商回复' } });

    const reply = screen.getByRole('button', { name: '发送给用户' }) as HTMLButtonElement;
    expect(reply.disabled).toBe(true);
    fireEvent.click(reply);
    expect((screen.getByRole('button', { name: '保存内部备注' }) as HTMLButtonElement).disabled).toBe(false);
  });

  it('回复用户 → is_internal: false；内部备注 → is_internal: true', async () => {
    const { calls } = stubFetch((_url, method) =>
      method === 'POST'
        ? ok({ id: 9, author: 'staff', body: '已修好', is_internal: false, created_at: '2026-08-26T02:00:00Z' }, 201)
        : ok(DETAIL),
    );
    renderDetail();

    await screen.findByText('全部节点都连不上');

    fireEvent.change(screen.getByLabelText('回复内容'), { target: { value: '  已经修好了  ' } });
    fireEvent.click(screen.getByRole('button', { name: '发送给用户' }));
    await waitFor(() => expect(calls.filter((c) => c.method === 'POST').length).toBe(1));

    fireEvent.change(screen.getByLabelText('备注内容'), { target: { value: '节点商换了 IP' } });
    fireEvent.click(screen.getByRole('button', { name: '保存内部备注' }));
    await waitFor(() => expect(calls.filter((c) => c.method === 'POST').length).toBe(2));

    const posts = calls.filter((c) => c.method === 'POST');
    expect(posts[0]?.url).toBe('/api/v1/admin/tickets/1024/messages');
    expect(posts[0]?.body).toEqual({ message: '已经修好了', is_internal: false });
    expect(posts[1]?.body).toEqual({ message: '节点商换了 IP', is_internal: true });
  });

  it('两个按钮上方各自常驻一句目标声明（发送前必须看见球会滚到哪边）', async () => {
    stubFetch(() => ok(DETAIL));
    renderDetail();

    await screen.findByText('全部节点都连不上');
    // 精确串：内部备注气泡里的「用户看不到这一条」是另一处（那是已发出的消息，不是目标声明）。
    expect(screen.getByText('用户会看到')).toBeTruthy();
    expect(screen.getByText('用户看不到')).toBeTruthy();
    expect(screen.getByText(/发出后无法撤回/)).toBeTruthy();
  });

  it('空正文不许提交（服务端也会退回，但没必要为此走一趟）', async () => {
    const { calls } = stubFetch(() => ok(DETAIL));
    renderDetail();

    await screen.findByText('全部节点都连不上');
    const reply = screen.getByRole('button', { name: '发送给用户' }) as HTMLButtonElement;
    expect(reply.disabled).toBe(true);

    // 只打空白也不算：服务端 TrimSpace 之后判空，这里同口径。
    fireEvent.change(screen.getByLabelText('回复内容'), { target: { value: '   ' } });
    expect((screen.getByRole('button', { name: '发送给用户' }) as HTMLButtonElement).disabled).toBe(true);
    fireEvent.click(screen.getByRole('button', { name: '发送给用户' }));
    expect(calls.filter((c) => c.method === 'POST').length).toBe(0);
  });
});

describe('工单会话 · 状态与等级', () => {
  it('下拉里没有「客服已回复」：它是算出来的，写回去会被服务端 422', async () => {
    stubFetch(() => ok(DETAIL));
    renderDetail();

    await screen.findByText('全部节点都连不上');
    const select = screen.getByLabelText('状态') as HTMLSelectElement;
    const options = [...select.options].map((o) => o.textContent);
    expect(options).toEqual(['待客服回复', '待用户补充', '已关闭']);
    expect(options).not.toContain('客服已回复');
  });

  it('没有改动时不许提交 —— 空 PATCH 只会往 append-only 的审计表里塞一条 before == after', async () => {
    const { calls } = stubFetch(() => ok(DETAIL));
    renderDetail();

    await screen.findByText('全部节点都连不上');
    const save = screen.getByRole('button', { name: '保存' }) as HTMLButtonElement;
    expect(save.disabled).toBe(true);
    fireEvent.click(save);
    expect(calls.filter((c) => c.method === 'PATCH').length).toBe(0);
  });

  it('只改了等级就只发 level，且 PATCH 回来的假分类不许覆盖页面上的真分类', async () => {
    const { calls } = stubFetch((_url, method) =>
      method === 'PATCH'
        ? ok({
            public_id: 'BP-7K2M9Q',
            subject: '订阅拉不到节点',
            // 🔴 服务端这里给的是 `account`：`AdminUpdateTicket` 的 RETURNING 里没有
            //    category_slug，只能给一个默认值并记 WARN。整体替换的话，
            //    一张「连不上 / 速度」的单会在改完等级后无声无息变成「账号本身」。
            category: 'account',
            status: 'open',
            level: 4,
            created_at: '2026-08-20T02:00:00Z',
          })
        : ok(DETAIL),
    );
    renderDetail();

    await screen.findByText('全部节点都连不上');
    fireEvent.change(screen.getByLabelText('等级'), { target: { value: '4' } });
    fireEvent.click(screen.getByRole('button', { name: '保存' }));

    await screen.findByText('已保存。');
    const patch = calls.find((c) => c.method === 'PATCH');
    expect(patch?.url).toBe('/api/v1/admin/tickets/1024');
    expect(patch?.body).toEqual({ level: 4 });

    // 分类仍然是真的那个。
    expect(screen.getAllByText('连不上 / 速度').length).toBeGreaterThan(0);
    expect(screen.queryByText('账号本身')).toBeNull();
  });
});

/**
 * `/order` 的接线测试。
 *
 * **这一页最容易被顺手改错的那条规则**，第 4、5 个用例正面钉住它：
 * 「还有没有下一页」的唯一判据是 `meta.has_more` + `meta.next_cursor`，
 * **不是**「这一页返回的条数等于 limit」。后者是所有人写分页时的第一反应，
 * 而它在总数正好整除时会判出一页空数据 —— 空页在前端长得像加载失败，
 * 用户看到的是「点了『加载更多』什么都没有」。
 * 同一条规则的另一半：用户面**永不返回 `total`**（api-contract §2.4），
 * 所以页面上不许出现「共 N 条 / 第 x 页」这种只能靠编才写得出来的数字。
 *
 * 第 7 个用例钉的是另一件反直觉的事：契约的 `OrderStatus` 只有 6 个值，
 * 后端把 DB 的 `paying` / `underpaid` / `paid` 三个状态**并成了 `processing`**
 * （`orderStatusView` 的注释：「并档是有损的」）。所以列表页**看不到 underpaid**，
 * 每一行 `processing` 都必须把用户送进详情页，而且不能说成「无需操作」——
 * 那正好把需要补款的那一单说成不用管。
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { MemoryRouter, Route, Routes } from 'react-router';
import { resetRuntimeConfig } from '@babelplus/shared';
import { AuthProvider } from '../lib/auth.tsx';
import { resetApiClientForTests } from '../lib/api.ts';
import { ACCESS_TOKEN_KEY, resetSessionForTests } from '../lib/session.ts';
import OrderListPage from './OrderListPage.tsx';

interface Call {
  path: string;
  search: string;
  method: string;
}

const calls: Call[] = [];

function jsonResponse(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

const META = { request_id: '01K2ORDERORDERORDERORDEROR' };

const CURRENT_USER = {
  data: {
    id: 1,
    email: 'user@example.com',
    banned: false,
    created_at: '2026-08-23T00:00:00Z',
    balance_amount: 0,
    subscription: {
      total_bytes: 0,
      upload_bytes: 0,
      download_bytes: 0,
      device_limit: 0,
      device_count: 0,
    },
  },
  meta: META,
};

function order(overrides: Record<string, unknown> = {}) {
  return {
    trade_no: 'BP20260830000001',
    type: 'new',
    status: 'pending',
    currency: 'CNY',
    plan_name: '标准',
    period: 'monthly',
    total_amount: 3000,
    payable_amount: 3000,
    created_at: '2026-08-30T02:00:00Z',
    ...overrides,
  };
}

/** 正好一页（PAGE_SIZE = 20）—— 「条数 == limit」那种写法恰好在这里判错。 */
function exactlyOnePage() {
  return Array.from({ length: 20 }, (_, i) =>
    order({ trade_no: `BP2026083000${String(i).padStart(4, '0')}` }),
  );
}

function renderOrderList() {
  return render(
    <MemoryRouter initialEntries={['/order']}>
      <AuthProvider>
        <Routes>
          <Route path="/order" element={<OrderListPage />} />
        </Routes>
      </AuthProvider>
    </MemoryRouter>,
  );
}

function stubApi(routes: Record<string, (call: Call) => Response>) {
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: string | URL | Request, init?: RequestInit) => {
      const url = new URL(String(input));
      const call: Call = {
        path: url.pathname,
        search: url.search,
        method: (init?.method ?? 'GET').toUpperCase(),
      };
      calls.push(call);
      if (call.path === '/api/v1/user/me') return jsonResponse(200, CURRENT_USER);
      const handler = routes[`${call.method} ${call.path}`] ?? routes[call.path];
      if (!handler) throw new Error(`未预期的请求：${call.method} ${call.path}`);
      return handler(call);
    }),
  );
}

function ordersOk(items: unknown[], meta: Record<string, unknown> = {}) {
  return () => jsonResponse(200, { data: items, meta: { ...META, ...meta } });
}

beforeEach(() => {
  calls.length = 0;
  resetRuntimeConfig();
  resetSessionForTests();
  resetApiClientForTests();
  window.sessionStorage.clear();
  window.sessionStorage.setItem(ACCESS_TOKEN_KEY, 'token-alive');
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe('OrderListPage', () => {
  it('成功：订单号 / 类型 / 金额 / 状态 / 时间五列都来自 API', async () => {
    stubApi({ '/api/v1/orders': ordersOk([order({ payable_amount: 8900 })], { has_more: false }) });
    renderOrderList();

    await waitFor(() => expect(screen.getByText('BP20260830000001')).toBeTruthy());
    // 金额是「分」的 int64，走整数除模 —— 8900 分 = ¥89.00，不是 8900 或 89。
    expect(screen.getByText('¥89.00')).toBeTruthy();
    expect(screen.getByText('新购 · 月付')).toBeTruthy();
    expect(screen.getByText('待支付')).toBeTruthy();
  });

  it('空态：不是「暂无数据」，给下一步动作', async () => {
    stubApi({ '/api/v1/orders': ordersOk([], { has_more: false }) });
    renderOrderList();

    await waitFor(() => expect(screen.getByText('还没有订单')).toBeTruthy());
    expect(screen.getByRole('link', { name: /去看套餐/ })).toBeTruthy();
  });

  it('501 NOT_IMPLEMENTED → 「该功能尚未开放」，不是「我们这边出了问题」', async () => {
    stubApi({
      '/api/v1/orders': () =>
        jsonResponse(501, { error: { code: 'NOT_IMPLEMENTED', message: '尚未实现' }, meta: META }),
    });
    renderOrderList();

    await waitFor(() => expect(screen.getByText('该功能尚未开放')).toBeTruthy());
    expect(screen.queryByText('我们这边出了问题')).toBeNull();
  });

  it('🔴 正好一页且 has_more=false → 没有「加载更多」，也没有「共 N 条」', async () => {
    stubApi({ '/api/v1/orders': ordersOk(exactlyOnePage(), { has_more: false }) });
    const { container } = renderOrderList();

    await waitFor(() => expect(screen.getByText('BP20260830000000')).toBeTruthy());
    // 「返回条数 == limit 就还有下一页」的写法会在这里长出一个按钮，然后点出一页空数据。
    expect(screen.queryByRole('button', { name: '加载更多' })).toBeNull();
    // 用户面没有 total，任何「共 N 条 / 第 x 页」都是编的。
    expect(container.textContent).not.toMatch(/共\s*\d+\s*条/);
    expect(container.textContent).not.toMatch(/第\s*\d+\s*页/);
  });

  it('🔴 has_more=true 时按 next_cursor 翻页，且已加载的部分不被清空', async () => {
    let page = 0;
    stubApi({
      '/api/v1/orders': () => {
        page += 1;
        return page === 1
          ? jsonResponse(200, {
              data: [order({ trade_no: 'BP-FIRST' })],
              meta: { ...META, has_more: true, next_cursor: 'CURSOR-1' },
            })
          : jsonResponse(200, {
              data: [order({ trade_no: 'BP-SECOND' })],
              meta: { ...META, has_more: false },
            });
      },
    });
    renderOrderList();

    await waitFor(() => expect(screen.getByText('BP-FIRST')).toBeTruthy());
    fireEvent.click(screen.getByRole('button', { name: '加载更多' }));

    await waitFor(() => expect(screen.getByText('BP-SECOND')).toBeTruthy());
    // 第一页仍在屏幕上 —— 翻页不是替换。
    expect(screen.getByText('BP-FIRST')).toBeTruthy();
    // 第二次请求必须真的带上游标，不是「limit + offset」也不是重发第一页。
    const listCalls = calls.filter((c) => c.path === '/api/v1/orders');
    expect(listCalls).toHaveLength(2);
    expect(listCalls[1]?.search).toContain('cursor=CURSOR-1');
    // 没有下一页了 → 按钮消失。
    expect(screen.queryByRole('button', { name: '加载更多' })).toBeNull();
  });

  it('取消要二次确认；成功后就地改这一行，不重新拉整张列表', async () => {
    stubApi({
      'GET /api/v1/orders': ordersOk([order()], { has_more: false }),
      'POST /api/v1/orders/BP20260830000001/cancel': () =>
        jsonResponse(200, { data: order({ status: 'cancelled' }), meta: META }),
    });
    renderOrderList();

    await waitFor(() => expect(screen.getByText('待支付')).toBeTruthy());
    fireEvent.click(screen.getByRole('button', { name: '取消订单' }));

    // 确认框出现，但还没发任何 POST。
    expect(screen.getByRole('button', { name: '确认取消这张订单' })).toBeTruthy();
    expect(calls.filter((c) => c.method === 'POST')).toHaveLength(0);

    fireEvent.click(screen.getByRole('button', { name: '确认取消这张订单' }));

    await waitFor(() => expect(screen.getByText('已取消')).toBeTruthy());
    // 列表**只拉过一次** —— 取消成功后把整页打回骨架屏，会让用户以为自己那一下没生效。
    expect(calls.filter((c) => c.path === '/api/v1/orders')).toHaveLength(1);
  });

  it('取消撞上 409 STATE_CONFLICT → 说「状态已经变了」，并明说别重复付款', async () => {
    stubApi({
      'GET /api/v1/orders': ordersOk([order()], { has_more: false }),
      'POST /api/v1/orders/BP20260830000001/cancel': () =>
        jsonResponse(409, {
          error: { code: 'STATE_CONFLICT', message: '只有待支付的订单可以取消' },
          meta: META,
        }),
    });
    renderOrderList();

    await waitFor(() => expect(screen.getByText('待支付')).toBeTruthy());
    fireEvent.click(screen.getByRole('button', { name: '取消订单' }));
    fireEvent.click(screen.getByRole('button', { name: '确认取消这张订单' }));

    // 409 上挂着三个不同的码，按状态码分支会把它们说成同一句「操作冲突」，
    // 而用户在支付路径上对「操作冲突」最可能的反应是再付一次。
    await waitFor(() => expect(screen.getByText('订单状态已经变了')).toBeTruthy());
    expect(screen.getByRole('alert').textContent).toContain('不要重复付款');
  });

  it('🔴 processing 的行把人送进详情页，且不说「无需操作」（underpaid 藏在这一档里）', async () => {
    stubApi({ '/api/v1/orders': ordersOk([order({ status: 'processing' })], { has_more: false }) });
    const { container } = renderOrderList();

    await waitFor(() => expect(screen.getByText('处理中')).toBeTruthy());
    expect(screen.getByRole('link', { name: '查看详情' })).toBeTruthy();
    expect(container.textContent).toContain('只有详情页看得到');
    expect(container.textContent).not.toContain('无需操作');
    // 契约里根本没有 underpaid 这个订单状态，所以这一页不该假装看得见它。
    expect(container.textContent).not.toContain('金额不足');
  });
});

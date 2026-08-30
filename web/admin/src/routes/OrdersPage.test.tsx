// @vitest-environment jsdom
//
// 需要 DOM：这一支验的是「列表渲染成了什么」。包级默认环境是 node（`lib/iap.test.ts`
// 测纯函数），所以在文件级单独提高，而不是把整个包扛上 jsdom 的启动开销。

/**
 * `/admin/orders` 的接线测试。
 *
 * 钉住的是四件**改错了不会有人报 bug** 的事：
 *
 *  1. **契约外的订单状态要原样透出。** 服务端刻意让管理面直出库里的 14 个值
 *     （`adminOrderStatusView`），而生成的 TS 联合类型只有 6 个。
 *     前端若按联合类型写一个 `switch` 再 `default: '未知'`，后台就**看不见拒付**了 ——
 *     而拒付是要在 120 天窗口内申辩的东西。
 *  2. **「有没有下一页」只看 `meta.has_more` + `next_cursor`**，不看「这页够不够 20 条」。
 *     后者在总数正好整除时会给出一个点不动的「下一页」。
 *  3. **`count=true` 只在第一页要**。`COUNT(*)` 在 db-f1-micro 上是实打实的开销。
 *  4. **501 不是故障**。它要显示成「还没上线」，不是红色的「我们这边出了问题」。
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { MemoryRouter, Route, Routes } from 'react-router';
import { resetRuntimeConfig } from '@babelplus/shared';
import { resetAdminApiForTests } from '../lib/api.ts';
import OrdersPage from './OrdersPage.tsx';

const META = { request_id: '01K2ORDERSORDERSORDERSOR' };

interface Call {
  path: string;
  search: string;
  method: string;
}

const calls: Call[] = [];

function json(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });
}

function errorEnvelope(status: number, code: string, message: string): Response {
  return json(status, { error: { code, message }, meta: META });
}

/** 每次调用都造一个**新的** `Response`：body 是一次性的流，重复用会在第二次读时炸。 */
function stubFetch(handler: (call: Call) => Response) {
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const raw = typeof input === 'string' ? input : input instanceof URL ? input.href : input.url;
      const url = new URL(raw, 'http://localhost');
      const call: Call = { path: url.pathname, search: url.search, method: init?.method ?? 'GET' };
      calls.push(call);
      return handler(call);
    }),
  );
}

function order(patch: { order?: Record<string, unknown>; user_email?: string; user_id?: number } = {}) {
  return {
    order: {
      trade_no: 'BP20260830000001',
      type: 'new',
      status: 'pending',
      currency: 'CNY',
      plan_name: '标准',
      period: 'monthly',
      total_amount: 3000,
      payable_amount: 3000,
      created_at: '2026-08-30T02:00:00Z',
      ...(patch.order ?? {}),
    },
    user_id: patch.user_id ?? 7,
    user_email: patch.user_email ?? 'user@example.com',
  };
}

function page(data: unknown[], meta: Record<string, unknown> = {}) {
  return { data, meta: { ...META, has_more: false, ...meta } };
}

function renderPage() {
  return render(
    <MemoryRouter initialEntries={['/admin/orders']}>
      <Routes>
        <Route path="/admin/orders" element={<OrdersPage />} />
      </Routes>
    </MemoryRouter>,
  );
}

beforeEach(() => {
  calls.length = 0;
  resetRuntimeConfig();
  resetAdminApiForTests();
  window.sessionStorage.clear();
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe('OrdersPage', () => {
  it('渲染订单行：单号、邮箱、类型、实付、状态、时间', async () => {
    stubFetch(() => json(200, page([order()])));
    renderPage();

    expect(await screen.findAllByText('BP20260830000001')).not.toHaveLength(0);
    expect(screen.getAllByText('user@example.com').length).toBeGreaterThan(0);
    expect(screen.getAllByText('待支付').length).toBeGreaterThan(0);
    expect(screen.getAllByText('¥30.00').length).toBeGreaterThan(0);
  });

  it('🔴 契约 enum 之外的订单状态要**看得见**：拒付与少付都不能被压扁', async () => {
    stubFetch(() =>
      json(
        200,
        page([
          order({ order: { trade_no: 'BP1', status: 'chargeback_lost' } }),
          order({ order: { trade_no: 'BP2', status: 'underpaid' } }),
          order({ order: { trade_no: 'BP3', status: 'refunding' } }),
        ]),
      ),
    );
    renderPage();

    expect(await screen.findAllByText('拒付败诉')).not.toHaveLength(0);
    expect(screen.getAllByText('少付').length).toBeGreaterThan(0);
    expect(screen.getAllByText('退款中').length).toBeGreaterThan(0);
  });

  it('认不出来的状态**原样显示**，不显示成「未知」', async () => {
    // 服务端将来加一个状态时，后台该看见的是那个新状态的名字，不是一个盖住它的占位符。
    stubFetch(() => json(200, page([order({ order: { trade_no: 'BP9', status: 'brand_new_state' } })])));
    renderPage();

    expect(await screen.findAllByText('brand_new_state')).not.toHaveLength(0);
    expect(screen.queryByText('未知')).toBeNull();
  });

  it('空态：还没有订单', async () => {
    stubFetch(() => json(200, page([])));
    renderPage();

    expect(await screen.findByText('还没有订单')).toBeTruthy();
  });

  it('501 显示成「尚未开放」，而不是红色的服务端故障', async () => {
    stubFetch(() => errorEnvelope(501, 'NOT_IMPLEMENTED', '尚未实现'));
    renderPage();

    expect(await screen.findByText(/尚未开放/)).toBeTruthy();
    expect(screen.queryByText(/我们这边/)).toBeNull();
  });

  it('403 AUTH_PERMISSION_DENIED：说清是权限位而不是登录过期', async () => {
    stubFetch(() => errorEnvelope(403, 'AUTH_PERMISSION_DENIED', '没有权限'));
    renderPage();

    expect(await screen.findByText('当前管理员账号看不到这一块')).toBeTruthy();
    expect(screen.getByText(/重新登录不会有帮助/)).toBeTruthy();
  });

  it('🔴 正好一页（20 条）但 has_more=false 时，「下一页」是禁用的', async () => {
    const rows = Array.from({ length: 20 }, (_, i) =>
      order({ order: { trade_no: `BP2026083000${String(i).padStart(4, '0')}` } }),
    );
    stubFetch(() => json(200, page(rows, { has_more: false })));
    renderPage();

    const next = await screen.findByRole('button', { name: '下一页' });
    expect((next as HTMLButtonElement).disabled).toBe(true);
  });

  it('count=true 只在第一页要；翻页后不再要', async () => {
    stubFetch(() => json(200, page([order()], { has_more: true, next_cursor: 'CURSOR2' })));
    renderPage();

    await screen.findAllByText('BP20260830000001');
    const first = calls.find((c) => c.path === '/api/v1/admin/orders');
    expect(first?.search).toContain('count=true');

    fireEvent.click(screen.getByRole('button', { name: '下一页' }));

    await waitFor(() => {
      expect(calls.filter((c) => c.path === '/api/v1/admin/orders').length).toBeGreaterThan(1);
    });
    const second = calls.filter((c) => c.path === '/api/v1/admin/orders').at(-1);
    expect(second?.search).toContain('cursor=CURSOR2');
    expect(second?.search).not.toContain('count=true');
  });

  it('搜索提交后 q 进 query，且回到第一页（重新要 count）', async () => {
    stubFetch(() => json(200, page([order()], { has_more: true, next_cursor: 'CURSOR2' })));
    renderPage();

    await screen.findAllByText('BP20260830000001');
    fireEvent.click(screen.getByRole('button', { name: '下一页' }));
    await waitFor(() => expect(calls.length).toBeGreaterThan(1));

    fireEvent.change(screen.getByLabelText('搜索'), { target: { value: 'a@b.com' } });
    fireEvent.click(screen.getByRole('button', { name: '搜索' }));

    await waitFor(() => {
      const last = calls.filter((c) => c.path === '/api/v1/admin/orders').at(-1);
      expect(last?.search).toContain('q=a%40b.com');
    });
    const last = calls.filter((c) => c.path === '/api/v1/admin/orders').at(-1);
    // 回到第一页：不带游标，且重新要一次总数。
    expect(last?.search).not.toContain('cursor=');
    expect(last?.search).toContain('count=true');
  });

  it('边打字不发请求 —— 搜索走服务端全表扫，必须显式提交', async () => {
    stubFetch(() => json(200, page([order()])));
    renderPage();

    await screen.findAllByText('BP20260830000001');
    const before = calls.length;
    fireEvent.change(screen.getByLabelText('搜索'), { target: { value: 'abc' } });
    expect(calls.length).toBe(before);
  });
});

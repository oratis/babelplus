// @vitest-environment jsdom

/**
 * 运营看板的接线测试。
 *
 * 🔴 **这个文件存在的首要理由是第二、第三个用例：`—` 与 `0` 是两件事。**
 * 服务端 `loadAdminDashboard` 把五组查询并发跑，任一组失败时那一组的字段**缺席**
 * （整页仍然 200）。把缺席渲染成 0 的后果是：数据库挂了半天，
 * 而看板一直平静地显示「今日收入 ¥0.00」—— 没有报错、没有红色、没有人会去查。
 *
 * ⚠️ 这些用例里的 `—` 断言看的是**那句解释**（「这一格没取到（不是 0）」）而不是破折号本身：
 * 一个孤零零的破折号在页面上会有很多个，而它的含义正是这一页最容易被读错的东西。
 *
 * 测试装配件（`stubFetch` 那一段）在这五个文件里各有一份。
 * 不抽公共文件是**任务边界**所致（后台 17 个模块正在并行接线，公共目录动一下就撞车）；
 * 等 17 个模块都接完，应该像用户面板的 `account-test-utils.tsx` 那样收成一份。
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { cleanup, render, screen, waitFor } from '@testing-library/react';
import { resetRuntimeConfig } from '@babelplus/shared';
import { resetAdminApiForTests } from '../lib/api.ts';
import DashboardPage from './DashboardPage.tsx';

const DASHBOARD = '/api/v1/admin/dashboard';

interface Seen {
  readonly method: string;
  readonly url: URL;
  readonly body: string | null;
}

type StubHandler = (req: Seen) => Response;

function jsonResponse(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

function ok(data: unknown): unknown {
  return { data, meta: { request_id: '01K2DASHDASHDASHDASHDASHDA' } };
}

/** 失败信封。**始终带 code** —— 页面一律按 code 分支，不按状态码。 */
function fail(status: number, code: string, message = '出错了'): Response {
  return jsonResponse(status, {
    error: { code, message },
    meta: { request_id: '01K2ERRERRERRERRERRERRERRE' },
  });
}

function stubFetch(routes: Record<string, StubHandler>): Seen[] {
  const seen: Seen[] = [];
  const spy = vi.fn(async (input: string | URL | Request, init?: RequestInit) => {
    const url = new URL(String(input instanceof Request ? input.url : input));
    const method = (init?.method ?? (input instanceof Request ? input.method : 'GET')).toUpperCase();
    const route = routes[url.pathname];
    if (route === undefined) throw new Error(`未预期的请求：${method} ${url.pathname}`);
    const raw = init?.body;
    const body =
      typeof raw === 'string'
        ? raw
        : raw instanceof ArrayBuffer
          ? new TextDecoder().decode(raw)
          : null;
    const req: Seen = { method, url, body };
    seen.push(req);
    return route(req);
  });
  vi.stubGlobal('fetch', spy);
  return seen;
}

/** 五组齐全的一份数据。数值刻意两两不同，免得断言撞上别的格子。 */
const FULL = {
  online_users: 128,
  active_nodes: 11,
  total_nodes: 13,
  today_upload_bytes: 1024 ** 3,
  today_download_bytes: 3 * 1024 ** 3,
  today_revenue_amount: 123_456,
  month_revenue_amount: 7_890_123,
  pending_tickets: 7,
  underpaid_orders: 5,
};

beforeEach(() => {
  resetRuntimeConfig();
  resetAdminApiForTests();
  window.sessionStorage.clear();
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe('运营看板', () => {
  it('五组都取到了 → 五组数字都渲染出来', async () => {
    stubFetch({ [DASHBOARD]: () => jsonResponse(200, ok(FULL)) });
    render(<DashboardPage />);

    expect(await screen.findByText('128')).toBeTruthy(); // 在线
    expect(screen.getByText('11')).toBeTruthy(); // 在线节点
    expect(screen.getByText('13')).toBeTruthy(); // 登记在册
    expect(screen.getByText('2')).toBeTruthy(); // 失联 = 13 - 11
    expect(screen.getByText('7')).toBeTruthy(); // 待回工单
    expect(screen.getByText('5')).toBeTruthy(); // 欠费订单
    expect(screen.getByText('¥1,234.56')).toBeTruthy();
    expect(screen.getByText('¥78,901.23')).toBeTruthy();
    expect(screen.getByText('1.00 GB')).toBeTruthy();
    expect(screen.getByText('3.00 GB')).toBeTruthy();

    // 一组都没缺 → 不该出现任何「这一格没取到」。
    expect(screen.queryAllByText(/这一格没取到/)).toHaveLength(0);
  });

  it('🔴 一组取不到 → 那两格是「—」并说明原因，其余四组照常显示', async () => {
    // 服务端的 revenue 那个 goroutine 失败：两个金额字段一起缺席，其余字段完整。
    const partial: Record<string, number> = { ...FULL };
    delete partial['today_revenue_amount'];
    delete partial['month_revenue_amount'];
    stubFetch({ [DASHBOARD]: () => jsonResponse(200, ok(partial)) });
    render(<DashboardPage />);

    // 缺席的是「收入」这一组的两个字段。
    expect(await screen.findAllByText(/这一格没取到/)).toHaveLength(2);
    // 说清是哪一组没回来 —— 「有一组失败了」而不说是哪一组，等于没说。
    expect(screen.getByText(/这次取数有/).textContent).toContain('收入');

    // 🔴 其余四组必须照常显示 —— 一格失败不许把整页拖下水。
    expect(screen.getByText('128')).toBeTruthy();
    expect(screen.getByText('1.00 GB')).toBeTruthy();
    expect(screen.getByText('7')).toBeTruthy();
    expect(screen.getByText('11')).toBeTruthy();

    // 而且绝不能渲染成 ¥0.00。
    expect(screen.queryByText('¥0.00')).toBeNull();
  });

  it('🔴 `0` 不是 `—`：收入为 0 时显示 ¥0.00，且不说「没取到」', async () => {
    stubFetch({
      [DASHBOARD]: () =>
        jsonResponse(200, ok({ ...FULL, today_revenue_amount: 0, month_revenue_amount: 0 })),
    });
    render(<DashboardPage />);

    expect(await screen.findAllByText('¥0.00')).toHaveLength(2);
    expect(screen.queryAllByText(/这一格没取到/)).toHaveLength(0);
  });

  it('五组齐全且全为零 → 说一句「去看节点」，但不把卡片藏起来', async () => {
    const zeros = Object.fromEntries(Object.keys(FULL).map((k) => [k, 0]));
    stubFetch({ [DASHBOARD]: () => jsonResponse(200, ok(zeros)) });
    render(<DashboardPage />);

    expect(await screen.findByText(/五组数字都取到了，而且全是零/)).toBeTruthy();
    // 空态不许替换整页：节点那一格仍然要在（那才是这一天要看的东西）。
    expect(screen.getByText('登记在册')).toBeTruthy();
  });

  it('整页 500 → 错误态，重试会再发一次请求', async () => {
    const seen = stubFetch({ [DASHBOARD]: () => fail(500, 'INTERNAL_ERROR', '读取看板失败') });
    render(<DashboardPage />);

    expect(await screen.findByText('服务端出错了')).toBeTruthy();
    expect(seen).toHaveLength(1);

    screen.getByRole('button', { name: '重试' }).click();
    await waitFor(() => expect(seen.length).toBe(2));
  });

  it('501 → 「尚未开放」，不是「服务端出错了」（按 code 分支，不按状态码）', async () => {
    stubFetch({ [DASHBOARD]: () => fail(501, 'NOT_IMPLEMENTED', '尚未实现') });
    render(<DashboardPage />);

    expect(await screen.findByText('尚未开放')).toBeTruthy();
    expect(screen.queryByText('服务端出错了')).toBeNull();
  });

  it('权限不足 → 说「缺的是角色或权限位」，不劝人重新登录', async () => {
    stubFetch({ [DASHBOARD]: () => fail(403, 'AUTH_PERMISSION_DENIED', '无权访问') });
    render(<DashboardPage />);

    expect(await screen.findByText('这个账号看不了这一块')).toBeTruthy();
    expect(screen.getByText(/重新登录不会有帮助/)).toBeTruthy();
  });
});

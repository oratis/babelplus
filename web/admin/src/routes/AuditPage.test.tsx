// @vitest-environment jsdom

/**
 * 审计日志的接线测试。
 *
 * 🔴 **第一个用例是这个文件存在的理由：`?action=` 是包含匹配。**
 * 库里的动作带 D 编号前缀（`D6.order.mark_paid`），契约的举例不带。
 * 如果前端在服务端过滤之外再按等值筛一遍，结果是**一条都不命中且不报错** ——
 * 页面显示「审计日志是空的」，而这块屏幕正是有人会拿来证明「没人动过」的那块。
 * 所以这里既断言「发出去的参数是原样的」，也断言「带前缀的那一行渲染出来了」。
 *
 * 第二组用例守另一件容易失真的事：**本页细筛不是全库检索**。
 * 契约没有按操作者 / 目标 id / 时间检索的参数，这四个框只作用于当前这一页 ——
 * 它们不许发请求，而且找不到时必须说「本页没有」而不是「没有」。
 *
 * 最后一个用例守这一页的纪律：**没有任何写操作入口**。
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { resetRuntimeConfig } from '@babelplus/shared';
import { resetAdminApiForTests } from '../lib/api.ts';
import AuditPage from './AuditPage.tsx';

const AUDIT = '/api/v1/admin/audit';

interface Seen {
  readonly method: string;
  readonly url: URL;
}

type StubHandler = (req: Seen) => Response;

function jsonResponse(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

function page(
  data: unknown,
  meta: { has_more?: boolean; next_cursor?: string; total?: number } = {},
): unknown {
  return { data, meta: { request_id: '01K2AUDITAUDITAUDITAUDITAU', ...meta } };
}

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
    const req: Seen = { method, url };
    seen.push(req);
    return route(req);
  });
  vi.stubGlobal('fetch', spy);
  return seen;
}

/**
 * 已经渲染出来的审计条目（每条是一个 `<article>`）。
 *
 * 不用 `getByText('order.mark_paid')`：同样的串也出现在筛选框的说明文字里
 * （「填 order.mark_paid 能命中」），按文本查会撞上它。
 * 按结构查还有一个好处 —— 它同时断言了「这一条真的被渲染成了一条记录」。
 */
function auditRows(): string[] {
  return [...document.querySelectorAll('article')].map((el) => el.textContent ?? '');
}

function last(seen: Seen[]): Seen {
  const item = seen[seen.length - 1];
  if (!item) throw new Error('一次请求都没发出去');
  return item;
}

/** 🔴 库里的形态：动作带 D 编号前缀。契约的举例不带，以库为准。 */
const MARK_PAID = {
  id: 91,
  admin_id: 7,
  request_id: '',
  ip: '203.0.113.9',
  user_agent: 'Mozilla/5.0',
  action: 'D6.order.mark_paid',
  target_type: 'order',
  target_id: '20260816T7K2M9Q4',
  before: { status: 'pending' },
  after: { status: 'paid' },
  reason: '链上 txid 7f3a… 已确认到账，网关回调丢失',
  created_at: '2026-08-16T10:00:00Z',
};

const BAN_USER = {
  id: 90,
  admin_id: 3,
  request_id: '',
  ip: '203.0.113.4',
  action: 'D2.user.ban',
  target_type: 'user',
  target_id: '4242',
  reason: '批量注册滥用',
  created_at: '2026-08-15T09:00:00Z',
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

describe('审计日志 · 服务端筛选', () => {
  it('🔴 动作是包含匹配：原样发参数，带 D 前缀的记录照样渲染', async () => {
    const seen = stubFetch({ [AUDIT]: () => jsonResponse(200, page([MARK_PAID])) });
    render(<AuditPage />);
    await waitFor(() => expect(seen.length).toBe(1));

    fireEvent.change(screen.getByLabelText('动作（服务端 · 包含匹配）'), {
      target: { value: 'order.mark_paid' },
    });
    fireEvent.click(screen.getByRole('button', { name: '查询' }));

    await waitFor(() => expect(seen.length).toBe(2));
    // 参数原样发出去（服务端翻成 ILIKE '%…%'），前端不加工。
    expect(last(seen).url.searchParams.get('action')).toBe('order.mark_paid');

    // 🔴 关键：库里的 `D6.order.mark_paid` 与用户填的 `order.mark_paid` 不相等，
    //    但它必须显示出来。前端再按等值筛一遍的话，这里会是空的。
    await waitFor(() => expect(auditRows()).toHaveLength(1));
    expect(auditRows()[0]).toContain('order.mark_paid');
    expect(screen.getByText('D6')).toBeTruthy();
  });

  it('分页走游标，并且换筛选条件时把游标丢掉', async () => {
    const seen = stubFetch({
      [AUDIT]: () => jsonResponse(200, page([MARK_PAID], { next_cursor: 'CUR2', total: 1234 })),
    });
    render(<AuditPage />);
    await waitFor(() => expect(seen.length).toBe(1));

    // 管理面可以要总数（用户面永远不给）。
    expect(last(seen).url.searchParams.get('count')).toBe('true');
    expect(await screen.findByText('1,234')).toBeTruthy();

    fireEvent.click(screen.getByRole('button', { name: '下一页' }));
    await waitFor(() => expect(seen.length).toBe(2));
    expect(last(seen).url.searchParams.get('cursor')).toBe('CUR2');

    // 换条件 → 游标必须清掉：它是上一次查询里的位置，带过去只会翻到错的地方。
    fireEvent.change(screen.getByLabelText('目标类型（服务端 · 等值）'), {
      target: { value: 'order' },
    });
    fireEvent.click(screen.getByRole('button', { name: '查询' }));
    await waitFor(() => expect(seen.length).toBe(3));
    expect(last(seen).url.searchParams.get('cursor')).toBeNull();
    expect(last(seen).url.searchParams.get('target_type')).toBe('order');
  });

  it('没有任何记录 → 空态说「一旦有人改过就不会再空」', async () => {
    stubFetch({ [AUDIT]: () => jsonResponse(200, page([])) });
    render(<AuditPage />);

    expect(await screen.findByText('还没有任何管理操作')).toBeTruthy();
  });

  it('筛出来是空的 → 说的是「这个条件下没有记录」，不是「还没有任何管理操作」', async () => {
    const seen = stubFetch({
      [AUDIT]: (req) =>
        jsonResponse(200, page(req.url.searchParams.get('action') ? [] : [MARK_PAID])),
    });
    render(<AuditPage />);
    await waitFor(() => expect(seen.length).toBe(1));

    fireEvent.change(screen.getByLabelText('动作（服务端 · 包含匹配）'), {
      target: { value: 'nothing.matches' },
    });
    fireEvent.click(screen.getByRole('button', { name: '查询' }));

    expect(await screen.findByText('这个条件下没有记录')).toBeTruthy();
    expect(screen.queryByText('还没有任何管理操作')).toBeNull();
  });

  it('500 → 错误态；501 → 「尚未开放」（按 code 分支）', async () => {
    const seen = stubFetch({ [AUDIT]: () => fail(500, 'INTERNAL_ERROR', '读取审计日志失败') });
    const view = render(<AuditPage />);
    expect(await screen.findByText('服务端出错了')).toBeTruthy();
    await waitFor(() => expect(seen.length).toBe(1));
    view.unmount();

    stubFetch({ [AUDIT]: () => fail(501, 'NOT_IMPLEMENTED', '尚未实现') });
    render(<AuditPage />);
    expect(await screen.findByText('尚未开放')).toBeTruthy();
  });
});

describe('审计日志 · 本页细筛', () => {
  it('细筛不发请求，而且没命中时说的是「本页没有」', async () => {
    const seen = stubFetch({ [AUDIT]: () => jsonResponse(200, page([MARK_PAID, BAN_USER])) });
    render(<AuditPage />);
    await waitFor(() => expect(auditRows()).toHaveLength(2));

    fireEvent.change(screen.getByLabelText('操作者 admin_id'), { target: { value: '7' } });

    // 🔴 一次请求都不许多发：这四个框在契约里没有对应的服务端参数。
    expect(seen).toHaveLength(1);
    expect(auditRows()).toHaveLength(1);
    expect(auditRows()[0]).toContain('order.mark_paid');
    expect(screen.getByText(/本页细筛命中/)).toBeTruthy();

    fireEvent.change(screen.getByLabelText('操作者 admin_id'), { target: { value: '999' } });
    expect(seen).toHaveLength(1);
    // 「本页这 2 条里没有」而不是「没有」—— 后者是一句假话。
    expect(screen.getByText(/本页这 2 条里没有符合细筛条件的/)).toBeTruthy();
    expect(screen.getByText(/这不代表全库没有/)).toBeTruthy();
  });

  it('目标 id 细筛是包含匹配（复盘时常常只记得后几位）', async () => {
    stubFetch({ [AUDIT]: () => jsonResponse(200, page([MARK_PAID, BAN_USER])) });
    render(<AuditPage />);
    await waitFor(() => expect(auditRows()).toHaveLength(2));

    fireEvent.change(screen.getByLabelText('目标 id'), { target: { value: '7K2M9Q4' } });

    expect(auditRows()).toHaveLength(1);
    expect(auditRows()[0]).toContain('order.mark_paid');
  });
});

describe('审计日志 · 只读纪律', () => {
  it('整页没有任何删除 / 编辑 / 导出入口', async () => {
    const { container } = (() => {
      stubFetch({ [AUDIT]: () => jsonResponse(200, page([MARK_PAID])) });
      return render(<AuditPage />);
    })();
    await waitFor(() => expect(auditRows()).toHaveLength(1));

    const labels = [...container.querySelectorAll('button')].map((b) => b.textContent ?? '');
    // 🔴 结构性断言：一个能被清理的审计日志等于没有审计日志。
    //    有人在这里加按钮的那一天，这一条会红。
    for (const forbidden of ['删除', '编辑', '清理', '导出']) {
      expect(labels.some((l) => l.includes(forbidden))).toBe(false);
    }
    expect(screen.getByText(/刻意没有任何写操作/)).toBeTruthy();
  });

  it('请求号是空串这件事要写在页面上（它本该是接回访问日志的钥匙）', async () => {
    stubFetch({ [AUDIT]: () => jsonResponse(200, page([MARK_PAID])) });
    render(<AuditPage />);

    expect(await screen.findByText(/请求号是空的/)).toBeTruthy();
  });
});

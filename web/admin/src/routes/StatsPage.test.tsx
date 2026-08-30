// @vitest-environment jsdom

/**
 * 流量统计的接线测试。
 *
 * 三条**会静默出错**的口径，各有一个用例钉住：
 *
 *  1. 🔴 **`record_at` 必须按上海时区渲染。** 它是「上海当天 00:00」的 UTC 时刻，
 *     用浏览器本地时区渲染会让整张报表每一行都错一天，而它看起来完全正常。
 *  2. 🔴 **`?to=` 不能是 23:59:59Z。** 服务端把这个时刻换算成上海的日期，
 *     23:59:59Z 是上海次日 07:59 —— 查询会多算一天。
 *  3. 🔴 **参数不合法回的是 500 + `VALIDATION_FAILED`**（契约给这个端点没有 422）。
 *     按状态码分支会把「时间跨度填太长」说成「服务端炸了」，
 *     于是操作者去看状态页，而该做的是改输入框。
 *
 * 另外钉住 D14 的两件事：请求参数只有 `scope`（窗口由服务端自钉 90 天），
 * 以及权限位被拒时说的是「缺权限位」而不是「重新登录」。
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { resetRuntimeConfig } from '@babelplus/shared';
import { resetAdminApiForTests } from '../lib/api.ts';
import StatsPage from './StatsPage.tsx';

const STATS = '/api/v1/admin/stats';
const EXPORT = '/api/v1/admin/stats/export';

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

function page(data: unknown, meta: { has_more?: boolean } = {}): unknown {
  return { data, meta: { request_id: '01K2STATSTATSTATSTATSTATST', ...meta } };
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
    const req: Seen = { method, url, body: typeof init?.body === 'string' ? init.body : null };
    seen.push(req);
    return route(req);
  });
  vi.stubGlobal('fetch', spy);
  return seen;
}

function last(seen: Seen[]): Seen {
  const item = seen[seen.length - 1];
  if (!item) throw new Error('一次请求都没发出去');
  return item;
}

const GB = 1024 ** 3;

/** 上海 2026-08-31 那一天 = `2026-08-30T16:00:00Z`（服务端 `catalogRecordAt` 的口径）。 */
const SHANGHAI_AUG31 = '2026-08-30T16:00:00Z';

beforeEach(() => {
  resetRuntimeConfig();
  resetAdminApiForTests();
  window.sessionStorage.clear();
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe('流量统计 · 读路径', () => {
  it('🔴 record_at 按上海时区渲染 —— 用本地时区会整张表错一天', async () => {
    stubFetch({
      [STATS]: () =>
        jsonResponse(
          200,
          page([{ record_at: SHANGHAI_AUG31, upload_bytes: GB, download_bytes: 3 * GB }]),
        ),
    });
    render(<StatsPage />);

    // 这个时刻在 UTC 机器上是 08-30，在上海是 08-31。页面必须说 08-31。
    expect(await screen.findByText('2026/08/31')).toBeTruthy();
    expect(screen.queryByText('2026/08/30')).toBeNull();
  });

  it('默认按全局维度取数，不传时间参数（服务端自己给最近 30 天）', async () => {
    const seen = stubFetch({ [STATS]: () => jsonResponse(200, page([])) });
    render(<StatsPage />);

    await waitFor(() => expect(seen.length).toBe(1));
    expect(last(seen).url.searchParams.get('scope')).toBe('global');
    expect(last(seen).url.searchParams.get('from')).toBeNull();
    expect(last(seen).url.searchParams.get('to')).toBeNull();
  });

  it('🔴 结束日发的是当天 12:00Z，不是 23:59:59Z（后者会多算一天）', async () => {
    const seen = stubFetch({ [STATS]: () => jsonResponse(200, page([])) });
    render(<StatsPage />);
    await waitFor(() => expect(seen.length).toBe(1));

    fireEvent.change(screen.getByLabelText('结束日（含）'), { target: { value: '2026-08-31' } });

    await waitFor(() => expect(seen.length).toBe(2));
    expect(last(seen).url.searchParams.get('to')).toBe('2026-08-31T12:00:00Z');
  });

  it('按用户维度：同一个用户的多天会被聚合成一行', async () => {
    const seen = stubFetch({
      [STATS]: () =>
        jsonResponse(
          200,
          page([
            { record_at: SHANGHAI_AUG31, user_id: 5, upload_bytes: GB, download_bytes: GB },
            { record_at: '2026-08-29T16:00:00Z', user_id: 5, upload_bytes: GB, download_bytes: GB },
            { record_at: SHANGHAI_AUG31, user_id: 9, upload_bytes: GB, download_bytes: 0 },
          ]),
        ),
    });
    render(<StatsPage />);
    await waitFor(() => expect(seen.length).toBe(1));

    fireEvent.change(screen.getByLabelText('聚合维度'), { target: { value: 'user' } });
    await waitFor(() => expect(last(seen).url.searchParams.get('scope')).toBe('user'));

    // #5 两天合计 4 GB，#9 一天 1 GB。
    expect(await screen.findByText('#5')).toBeTruthy();
    expect(screen.getByText('#9')).toBeTruthy();
    expect(screen.getByText('4.00 GB')).toBeTruthy();
  });

  it('填了单价才有成本列；不填时是「—」并说明为什么刻意不预填', async () => {
    stubFetch({
      [STATS]: () =>
        jsonResponse(
          200,
          page([{ record_at: SHANGHAI_AUG31, upload_bytes: GB, download_bytes: GB }]),
        ),
    });
    render(<StatsPage />);
    await screen.findByText('2026/08/31');

    expect(screen.getByText(/成本这一列现在是/)).toBeTruthy();

    fireEvent.change(screen.getByLabelText('出口单价（元 / GB）'), { target: { value: '1.65' } });

    // 2 GB × ¥1.65 = ¥3.30（行内一次、合计一次）。
    expect(await screen.findAllByText('¥3.30')).toHaveLength(2);
  });

  it('结果被截断（meta.has_more）→ 明说「这不是完整数据」', async () => {
    stubFetch({
      [STATS]: () =>
        jsonResponse(
          200,
          page([{ record_at: SHANGHAI_AUG31, upload_bytes: 1, download_bytes: 1 }], {
            has_more: true,
          }),
        ),
    });
    render(<StatsPage />);

    expect(await screen.findByText(/结果已被截断/)).toBeTruthy();
    expect(screen.getByText(/不要拿它做成本核算/)).toBeTruthy();
  });

  it('没有数据 → 空态给出「去看节点」这个下一步', async () => {
    stubFetch({ [STATS]: () => jsonResponse(200, page([])) });
    render(<StatsPage />);

    expect(await screen.findByText('这个条件下没有聚合数据')).toBeTruthy();
    expect(screen.getByRole('link', { name: /看看节点有没有在上报/ })).toBeTruthy();
  });

  it('🔴 500 + VALIDATION_FAILED → 说「查询条件不合法」，不说「服务端出错了」', async () => {
    stubFetch({
      [STATS]: () => fail(500, 'VALIDATION_FAILED', '时间跨度最多 366 天'),
    });
    render(<StatsPage />);

    expect(await screen.findByText('查询条件不合法')).toBeTruthy();
    expect(screen.getByText('时间跨度最多 366 天')).toBeTruthy();
    expect(screen.queryByText('服务端出错了')).toBeNull();
  });

  it('501 → 「尚未开放」', async () => {
    stubFetch({ [STATS]: () => fail(501, 'NOT_IMPLEMENTED', '尚未实现') });
    render(<StatsPage />);

    expect(await screen.findByText('尚未开放')).toBeTruthy();
  });
});

describe('流量统计 · D14 导出', () => {
  const CSV = 'record_at,scope,user_id,server_id,upload_bytes,download_bytes\n2026-08-31,global,,,1,2\n';

  function stubDownload(): { urls: string[]; clicks: number } {
    const state = { urls: [] as string[], clicks: 0 };
    // jsdom 没有 createObjectURL；`a.click()` 会触发一条「navigation 未实现」的噪声。
    // 两个都替掉，断言落在「有没有把文件交出去」上。
    vi.stubGlobal('URL', Object.assign(URL, {
      createObjectURL: vi.fn(() => 'blob:stats.csv'),
      revokeObjectURL: vi.fn((u: string) => state.urls.push(u)),
    }));
    vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(function click(this: HTMLAnchorElement) {
      state.clicks += 1;
    });
    return state;
  }

  it('参数只有 scope（窗口由服务端自钉 90 天），导出后说清导了多少行', async () => {
    const download = stubDownload();
    const seen = stubFetch({
      [STATS]: () => jsonResponse(200, page([])),
      [EXPORT]: () => new Response(CSV, { status: 200, headers: { 'Content-Type': 'text/csv' } }),
    });
    render(<StatsPage />);
    await waitFor(() => expect(seen.length).toBe(1));

    fireEvent.click(screen.getByRole('button', { name: '导出流量统计 CSV' }));
    // 确认面板里必须写着「不是你上面选的时间窗」——这一条最容易被误解成「导出我筛的这段」。
    expect(screen.getByText(/不是你上面选的时间窗/)).toBeTruthy();

    fireEvent.click(screen.getByRole('button', { name: '导出（global）' }));

    await waitFor(() => expect(seen.length).toBe(2));
    expect(last(seen).url.pathname).toBe(EXPORT);
    expect(last(seen).url.searchParams.get('scope')).toBe('global');
    expect([...last(seen).url.searchParams.keys()]).toEqual(['scope']);

    expect(await screen.findByText(/已导出/)).toBeTruthy();
    expect(screen.getByText('1')).toBeTruthy(); // 行数 = 2 行 - 表头
    expect(download.clicks).toBe(1);
  });

  it('没有权限位 → 说缺的是权限位，不劝人重新登录', async () => {
    stubDownload();
    const seen = stubFetch({
      [STATS]: () => jsonResponse(200, page([])),
      [EXPORT]: () => fail(403, 'AUTH_PERMISSION_DENIED', '导出需要 admin.user.export 权限位'),
    });
    render(<StatsPage />);
    await waitFor(() => expect(seen.length).toBe(1));

    fireEvent.click(screen.getByRole('button', { name: '导出流量统计 CSV' }));
    fireEvent.click(screen.getByRole('button', { name: '导出（global）' }));

    expect(await screen.findByText('这个账号不能执行这一条')).toBeTruthy();
    expect(screen.getByText(/重试与重新登录都不会有帮助/)).toBeTruthy();
  });

  it('导出被限流 → 把 Retry-After 说出来', async () => {
    stubDownload();
    const seen = stubFetch({
      [STATS]: () => jsonResponse(200, page([])),
      [EXPORT]: () =>
        new Response(
          JSON.stringify({
            error: { code: 'QUOTA_RATE_LIMITED', message: '导出过于频繁' },
            meta: { request_id: '01K2ERRERRERRERRERRERRERRE' },
          }),
          { status: 429, headers: { 'Content-Type': 'application/json', 'Retry-After': '600' } },
        ),
    });
    render(<StatsPage />);
    await waitFor(() => expect(seen.length).toBe(1));

    fireEvent.click(screen.getByRole('button', { name: '导出流量统计 CSV' }));
    fireEvent.click(screen.getByRole('button', { name: '导出（global）' }));

    expect(await screen.findByText('操作太频繁')).toBeTruthy();
    expect(screen.getByText('600 秒后可以再试。')).toBeTruthy();
  });
});

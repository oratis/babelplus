/**
 * 「账户 / 用量 / 钱包 / 邀请」五页测试共用的装配件。
 *
 * 为什么抽出来：这五个测试文件的**前 40 行**本来会一模一样（重置四个单例、
 * 塞一枚 token 让 `AuthProvider` 走到 authenticated、按 path 分发 `fetch` 替身）。
 * 复制五份的代价不是行数，是**它们会各自漂移** —— 比如某一份忘了
 * `resetApiClientForTests()`，那一份就会拿到上一个用例留下的客户端单例，
 * 表现为「单独跑绿、一起跑红」，而这种失败最难查。
 *
 * 🔴 **不引 MSW**（照 `LoginPage.test.tsx` / `App.routes.test.tsx` 的做法）：
 * 这里要验的东西里有一半在**传输层**（信封拆封、`Retry-After` 解析、
 * 401 后的静默 refresh 与重放、501 的 code 判别）。MSW 拦在更外层没问题，
 * 但多引一个依赖去做 `vi.stubGlobal('fetch', …)` 五行就能做的事，
 * 换来的是「测试用的网络栈」与「线上的网络栈」两条不同的路径。
 */
import type { ReactNode } from 'react';
import { render } from '@testing-library/react';
import { MemoryRouter } from 'react-router';
import { vi } from 'vitest';
import { resetRuntimeConfig } from '@babelplus/shared';
import { AuthProvider } from '../lib/auth.tsx';
import { resetApiClientForTests } from '../lib/api.ts';
import { ACCESS_TOKEN_KEY, resetSessionForTests } from '../lib/session.ts';

/** 成功信封 `{data, meta}`。 */
export function jsonResponse(
  status: number,
  body: unknown,
  headers: Record<string, string> = {},
): Response {
  return new Response(status === 204 ? null : JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json', ...headers },
  });
}

/** 成功体。`meta.request_id` 是 ULID 形态，随手编一个够用。 */
export function ok(data: unknown, requestId = '01K2OKOKOKOKOKOKOKOKOKOKOK'): unknown {
  return { data, meta: { request_id: requestId, has_more: false } };
}

/** 带分页游标的成功体。`has_more` / `next_cursor` 落在 `meta` 上（契约 §2.4）。 */
export function okPage(
  data: unknown,
  meta: { has_more?: boolean; next_cursor?: string } = {},
): unknown {
  return { data, meta: { request_id: '01K2PAGEPAGEPAGEPAGEPAGEPA', ...meta } };
}

/** 失败信封。**始终带 `code`** —— 页面一律按 code 分支，不按状态码。 */
export function fail(
  status: number,
  code: string,
  message = '出错了',
  extra: { details?: Array<{ field: string; reason: string }>; headers?: Record<string, string> } = {},
): Response {
  const body: Record<string, unknown> = { code, message };
  if (extra.details) body['details'] = extra.details;
  return jsonResponse(
    status,
    { error: body, meta: { request_id: '01K2ERRERRERRERRERRERRERRE' } },
    extra.headers ?? {},
  );
}

/**
 * 501 `NOT_IMPLEMENTED`。
 *
 * ⚠️ `NOT_IMPLEMENTED` **不在 openapi 的 `ErrorCode` enum 里** —— 它是
 * `cmd/server/main.go` 的错误映射层直接写出去的，所以只能按字符串比。
 * 用例里必须用这个 helper 而不是随手写一个别的 code，
 * 否则测的就不是真实后端会发出来的那个东西。
 */
export function notImplemented(): Response {
  return fail(501, 'NOT_IMPLEMENTED', '尚未实现');
}

/** 有订阅的当前用户（`/invite` 的生成闸门要它）。 */
export const CURRENT_USER = {
  id: 1,
  email: 'user@example.com',
  banned: false,
  created_at: '2026-06-01T00:00:00Z',
  balance_amount: 0,
  subscription: {
    plan_name: '标准',
    total_bytes: 107_374_182_400,
    upload_bytes: 0,
    download_bytes: 0,
    device_limit: 3,
    device_count: 1,
    expired_at: '2099-01-01T00:00:00Z',
    reset_at: '2099-01-01T00:00:00Z',
  },
};

/** 没有订阅的当前用户。 */
export const CURRENT_USER_NO_SUB = {
  id: 2,
  email: 'nosub@example.com',
  banned: false,
  created_at: '2026-06-01T00:00:00Z',
  balance_amount: 0,
  subscription: {
    total_bytes: 0,
    upload_bytes: 0,
    download_bytes: 0,
    device_limit: 0,
    device_count: 0,
  },
};

/**
 * 一条路由的应答函数。**导出**是因为各页的测试都要写
 * `Record<string, StubHandler>` 这样的表 —— 写成 `() => Response` 的话，
 * 想按 `method` 分岔的 handler 会被 TS 拒掉（多参数不能赋给零参数签名）。
 */
export type StubHandler = (request: {
  method: string;
  url: URL;
  body: string | null;
}) => Response | Promise<Response>;

/**
 * 按 pathname 分发的 `fetch` 替身。
 *
 * **未登记的路径直接抛**，不静默返回 200：一个页面偷偷多发了一个请求
 * （比如有人把「用 AuthProvider 已有的 user」改成「自己再拉一次 /me」）
 * 应该让用例炸掉，而不是悄悄放过。
 */
export function stubFetch(routes: Record<string, StubHandler | Response>): ReturnType<typeof vi.fn> {
  const spy = vi.fn(async (input: string | URL | Request, init?: RequestInit) => {
    const url = new URL(String(input instanceof Request ? input.url : input));
    const method = (init?.method ?? (input instanceof Request ? input.method : 'GET')).toUpperCase();
    const route = routes[url.pathname];
    if (route === undefined) throw new Error(`未预期的请求：${method} ${url.pathname}`);
    if (typeof route !== 'function') return route.clone();
    // 传输层把 body 读成 `ArrayBuffer` 再逐次 `slice`（为了能重发），
    // 所以这里拿到的**不是字符串** —— 想断言请求体就得先解回来。
    const raw = init?.body;
    const body =
      typeof raw === 'string'
        ? raw
        : raw instanceof ArrayBuffer
          ? new TextDecoder().decode(raw)
          : null;
    return route({ method, url, body });
  });
  vi.stubGlobal('fetch', spy);
  return spy;
}

/** `/api/v1/user/me` 的标准应答。每个用例都要它（`AuthProvider` 启动时会拉一次）。 */
export function meRoute(user: unknown = CURRENT_USER): StubHandler {
  return () => jsonResponse(200, ok(user));
}

/**
 * 渲染一个已登录的页面。
 *
 * 先往 `sessionStorage` 里塞一枚 token：`AuthProvider` 的初值是同步读 token 的，
 * 没有它会**立刻**判成 anonymous，页面根本不渲染。
 */
export function renderSignedIn(ui: ReactNode) {
  window.sessionStorage.setItem(ACCESS_TOKEN_KEY, 'test-token');
  return render(
    <MemoryRouter>
      <AuthProvider>{ui}</AuthProvider>
    </MemoryRouter>,
  );
}

/** 每个用例前把四个单例都清干净。漏一个就会出现「单独跑绿、一起跑红」。 */
export function resetAll(): void {
  resetRuntimeConfig();
  resetSessionForTests();
  resetApiClientForTests();
  window.sessionStorage.clear();
}

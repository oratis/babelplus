/**
 * API 客户端的第一批测试。
 *
 * README §8 点名了第一条该补的：**「POST 不故障转移」**。
 * 它不是一条性能规则而是一条安全规则 —— 非幂等请求换个域名重发一次，
 * 「下单」「标记已支付」就变成了两笔。它同时是一条**很容易被后来者好心改错**的规则：
 * 「为什么只有 GET 能重试？把 POST 也加上不是更稳吗」听起来完全合理。
 * 所以这里把方法矩阵写成表，改错了 CI 会直接指出改错了哪一个方法。
 */
import { describe, expect, it, vi } from 'vitest';
import {
  ApiError,
  IAP_GENERATED_HEADER,
  createTransport,
  networkError,
  toApiError,
  unwrap,
  type FetchResult,
  type Meta,
} from './client.ts';
import type { ErrorKind } from '../src/lib/error-kind.ts';
import { createSessionManager, memorySessionStore } from './session.ts';

const PRIMARY = 'https://api.example';
const FALLBACK = 'https://api-mirror.example';

/** 记录每次尝试打到了哪个 URL。 */
function recordingFetch(handler: (url: string, init: RequestInit) => Promise<Response>) {
  const urls: string[] = [];
  const impl = (async (input: string | URL | Request, init?: RequestInit) => {
    const url = String(input);
    urls.push(url);
    return handler(url, init ?? {});
  }) as unknown as typeof fetch;
  return { impl, urls };
}

function jsonResponse(status: number, body: unknown, headers: Record<string, string> = {}): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json', ...headers },
  });
}

function envelope(code: string, message = '出错了', requestId = '01K2TESTTESTTESTTESTTESTTE') {
  return { error: { code, message }, meta: { request_id: requestId } };
}

/* ───────────────────────── ① 故障转移的方法矩阵 ───────────────────────── */

describe('故障转移：只有幂等方法才换备用域名', () => {
  const UNSAFE: string[] = ['POST', 'PUT', 'PATCH', 'DELETE'];
  const SAFE: string[] = ['GET', 'HEAD', 'OPTIONS'];

  it.each(UNSAFE)('%s 连不上时**只试主域名一次**，绝不重发到备用域名', async (method) => {
    const { impl, urls } = recordingFetch(async () => {
      throw new TypeError('fetch failed');
    });
    const transport = createTransport({
      baseUrl: PRIMARY,
      fallbackBaseUrls: [FALLBACK],
      timeoutMs: 100,
      fetchImpl: impl,
    });

    const request = new Request(`${PRIMARY}/api/v1/orders`, { method, body: '{"plan_id":1}' });
    await expect(transport(request)).rejects.toBeInstanceOf(ApiError);

    expect(urls).toEqual([`${PRIMARY}/api/v1/orders`]);
  });

  it.each(SAFE)('%s 连不上时按顺序重试备用域名', async (method) => {
    const { impl, urls } = recordingFetch(async (url) => {
      if (url.startsWith(PRIMARY)) throw new TypeError('fetch failed');
      return jsonResponse(200, { data: { ok: true }, meta: {} });
    });
    const transport = createTransport({
      baseUrl: PRIMARY,
      fallbackBaseUrls: [FALLBACK],
      timeoutMs: 100,
      fetchImpl: impl,
    });

    const response = await transport(new Request(`${PRIMARY}/api/v1/plans`, { method }));

    expect(response.status).toBe(200);
    expect(urls).toEqual([`${PRIMARY}/api/v1/plans`, `${FALLBACK}/api/v1/plans`]);
  });

  it('全部域名都连不上时，错误信息说清楚试了几个', async () => {
    const { impl } = recordingFetch(async () => {
      throw new TypeError('fetch failed');
    });
    const transport = createTransport({
      baseUrl: PRIMARY,
      fallbackBaseUrls: [FALLBACK, 'https://api-3.example'],
      timeoutMs: 100,
      fetchImpl: impl,
    });

    await expect(transport(new Request(`${PRIMARY}/api/v1/plans`))).rejects.toThrow(
      '主域名与 2 个备用域名都连不上',
    );
  });

  it('用户主动取消不触发故障转移', async () => {
    const controller = new AbortController();
    const { impl, urls } = recordingFetch(async () => {
      throw new DOMException('aborted', 'AbortError');
    });
    const transport = createTransport({
      baseUrl: PRIMARY,
      fallbackBaseUrls: [FALLBACK],
      timeoutMs: 100,
      fetchImpl: impl,
    });

    const request = new Request(`${PRIMARY}/api/v1/plans`, { signal: controller.signal });
    controller.abort();
    await expect(transport(request)).rejects.toBeInstanceOf(ApiError);

    expect(urls).toHaveLength(1);
  });
});

/* ───────────────────────────── ② 超时 ───────────────────────────── */

describe('超时', () => {
  /** 永不 settle，只在 signal 中断时失败 —— 这就是「服务端不回包」在客户端的样子。 */
  const hangingFetch = (async (_input: unknown, init?: RequestInit) =>
    new Promise<Response>((_resolve, reject) => {
      init?.signal?.addEventListener('abort', () =>
        reject(new DOMException('timed out', 'TimeoutError')),
      );
    })) as unknown as typeof fetch;

  it('单次尝试超时后按幂等规则继续，最终抛 offline 且信息里带超时值', async () => {
    const transport = createTransport({
      baseUrl: PRIMARY,
      fallbackBaseUrls: [FALLBACK],
      timeoutMs: 20,
      fetchImpl: hangingFetch,
    });

    const error = await transport(new Request(`${PRIMARY}/api/v1/plans`)).catch((e: unknown) => e);

    expect(error).toBeInstanceOf(ApiError);
    expect((error as ApiError).kind).toBe('offline');
    expect((error as ApiError).status).toBe(0);
    expect((error as ApiError).message).toContain('20ms');
  });

  it('POST 超时也只吃一次超时，不会把等待时间翻倍', async () => {
    const { impl, urls } = recordingFetch(
      async (_url, init) =>
        new Promise<Response>((_resolve, reject) => {
          init.signal?.addEventListener('abort', () => reject(new DOMException('t', 'TimeoutError')));
        }),
    );
    const transport = createTransport({
      baseUrl: PRIMARY,
      fallbackBaseUrls: [FALLBACK],
      timeoutMs: 20,
      fetchImpl: impl,
    });

    await expect(
      transport(new Request(`${PRIMARY}/api/v1/orders`, { method: 'POST', body: '{}' })),
    ).rejects.toBeInstanceOf(ApiError);
    expect(urls).toHaveLength(1);
  });
});

/* ─────────────────────────── ③ 五类错误归一 ─────────────────────────── */

describe('五类错误归一（page-inventory §2.2）', () => {
  const KIND_CASES: Array<[number, string, ErrorKind]> = [
    [401, 'AUTH_TOKEN_INVALID', 'unauthorized'],
    [403, 'AUTH_PERMISSION_DENIED', 'forbidden'],
    [404, 'RESOURCE_NOT_FOUND', 'client'],
    [422, 'VALIDATION_FAILED', 'client'],
    [429, 'QUOTA_RATE_LIMITED', 'client'],
    [500, 'INTERNAL_ERROR', 'server'],
    [503, 'INTERNAL_DEPENDENCY_DOWN', 'server'],
  ];

  it.each(KIND_CASES)('HTTP %i → kind=%s', (status, code, kind) => {
    const body = envelope(code);
    const error = toApiError(jsonResponse(status, body), body);

    expect(error.kind).toBe(kind);
    expect(error.code).toBe(code);
    expect(error.status).toBe(status);
    // 用户报障时贴的就是这个串，它必须一路带到 UI 上。
    expect(error.requestId).toBe('01K2TESTTESTTESTTESTTESTTE');
  });

  it('网络层失败 → offline，status 归零', () => {
    const error = networkError('连不上 API');
    expect(error.kind).toBe('offline');
    expect(error.status).toBe(0);
    expect(error.code).toBe('NETWORK');
  });

  it('信封里没有 request_id 时退回响应头', () => {
    const response = jsonResponse(500, { error: { code: 'INTERNAL_ERROR', message: 'x' } }, {
      'X-Request-Id': '01HEADERHEADERHEADERHEADER',
    });
    const error = toApiError(response, { error: { code: 'INTERNAL_ERROR', message: 'x' } });
    expect(error.requestId).toBe('01HEADERHEADERHEADERHEADER');
  });

  it('429 的 Retry-After 解析成秒；解析不了就是 undefined，**绝不猜一个值**', () => {
    const body = envelope('QUOTA_RATE_LIMITED');
    expect(toApiError(jsonResponse(429, body, { 'Retry-After': '42' }), body).retryAfterSeconds).toBe(42);
    expect(
      toApiError(jsonResponse(429, body, { 'Retry-After': 'Wed, 21 Oct 2026 07:28:00 GMT' }), body)
        .retryAfterSeconds,
    ).toBeUndefined();
    expect(toApiError(jsonResponse(429, body), body).retryAfterSeconds).toBeUndefined();
  });

  it('unwrap 成功时脱掉两层信封', async () => {
    const call: Promise<FetchResult<{ data: { id: number }; meta: Meta }>> = Promise.resolve({
      data: { data: { id: 7 }, meta: {} as Meta },
      response: jsonResponse(200, {}),
    });
    await expect(unwrap(call)).resolves.toEqual({ id: 7 });
  });
});

/* ──────────────────── ④ 平台层拒绝 vs 应用层拒绝 ──────────────────── */

describe('平台层（IAP）拒绝的判别', () => {
  it('带 IAP 标记头 → iap-header（即使响应体看起来像我们的信封）', () => {
    const body = envelope('AUTH_TOKEN_INVALID');
    const error = toApiError(jsonResponse(401, body, { [IAP_GENERATED_HEADER]: 'true' }), body);
    expect(error.edge?.signal).toBe('iap-header');
  });

  it('响应体是 HTML → non-json-body', () => {
    const response = new Response('<html>Sign in with Google</html>', {
      status: 401,
      headers: { 'Content-Type': 'text/html; charset=utf-8' },
    });
    const error = toApiError(response, '<html>Sign in with Google</html>');
    expect(error.edge?.signal).toBe('non-json-body');
    expect(error.edge?.contentType).toContain('text/html');
  });

  it('是 JSON 但没有 error.code → not-envelope', () => {
    const body = { message: 'Unauthorized' };
    const error = toApiError(jsonResponse(403, body), body);
    expect(error.edge?.signal).toBe('not-envelope');
  });

  it('我们自己的 401 信封 → 不是平台层拒绝', () => {
    const body = envelope('AUTH_TOKEN_INVALID');
    expect(toApiError(jsonResponse(401, body), body).edge).toBeNull();
  });

  it('非 401/403 一律不判平台层（500 的 HTML 是我们的网关，不是 IAP）', () => {
    const response = new Response('<html>502</html>', {
      status: 500,
      headers: { 'Content-Type': 'text/html' },
    });
    expect(toApiError(response, '<html>502</html>').edge).toBeNull();
  });
});

/* ─────────────────────── ⑤ 401 静默 refresh + 重放 ─────────────────────── */

describe('401 → 单飞 refresh → 重放一次', () => {
  /** 只认新 token 的服务端。旧 token 一律 401 `AUTH_TOKEN_INVALID`（后端的真实行为）。 */
  function tokenGatedFetch(validToken: string) {
    return recordingFetch(async (_url, init) => {
      const auth = new Headers(init.headers).get('Authorization');
      if (auth === `Bearer ${validToken}`) return jsonResponse(200, { data: { ok: true }, meta: {} });
      return jsonResponse(401, envelope('AUTH_TOKEN_INVALID', '会话无效或已过期'));
    });
  }

  function transportWith(
    impl: typeof fetch,
    refreshAccessToken: (stale: string | null) => Promise<string | null>,
  ) {
    return createTransport({
      baseUrl: PRIMARY,
      fallbackBaseUrls: [],
      timeoutMs: 500,
      fetchImpl: impl,
      refreshAccessToken,
    });
  }

  function authed(url: string, token: string, init: RequestInit = {}): Request {
    return new Request(url, { ...init, headers: { Authorization: `Bearer ${token}` } });
  }

  it('并发 6 个 401 只触发一次 refresh，全部重放成功', async () => {
    const { impl } = tokenGatedFetch('new');
    const refreshCalls = vi.fn(async () => 'new');
    const manager = createSessionManager({ store: memorySessionStore('old'), refresh: refreshCalls });
    const transport = transportWith(impl, (stale) => manager.ensureFreshToken(stale));

    const responses = await Promise.all(
      Array.from({ length: 6 }, () => transport(authed(`${PRIMARY}/api/v1/user/me`, 'old'))),
    );

    expect(responses.map((r) => r.status)).toEqual(Array.from({ length: 6 }, () => 200));
    expect(refreshCalls).toHaveBeenCalledTimes(1);
    expect(manager.getToken()).toBe('new');
  });

  it('重放用的是新 token，且只重放一次', async () => {
    const { impl, urls } = tokenGatedFetch('new');
    const transport = transportWith(impl, async () => 'new');

    const response = await transport(authed(`${PRIMARY}/api/v1/user/me`, 'old'));

    expect(response.status).toBe(200);
    expect(urls).toHaveLength(2); // 原请求 + 一次重放，没有第三次
  });

  it('POST 也会被重放 —— 401 由鉴权中间件在 handler 之前返回，服务端没有产生副作用', async () => {
    const { impl, urls } = tokenGatedFetch('new');
    const bodies: string[] = [];
    const wrapped = (async (input: string | URL | Request, init?: RequestInit) => {
      if (init?.body) bodies.push(new TextDecoder().decode(init.body as ArrayBuffer));
      return impl(input as string, init);
    }) as unknown as typeof fetch;
    const transport = transportWith(wrapped, async () => 'new');

    const response = await transport(
      authed(`${PRIMARY}/api/v1/orders`, 'old', { method: 'POST', body: '{"plan_id":1}' }),
    );

    expect(response.status).toBe(200);
    expect(urls).toHaveLength(2);
    // 重放必须带上同一份请求体：body 是一次性的流，读过一次就没了。
    expect(bodies).toEqual(['{"plan_id":1}', '{"plan_id":1}']);
  });

  it('refresh 被拒 → 不重放，401 原样冒泡', async () => {
    const { impl, urls } = tokenGatedFetch('new');
    const refresh = vi.fn(async () => null);
    const manager = createSessionManager({ store: memorySessionStore('old'), refresh });
    const transport = transportWith(impl, (stale) => manager.ensureFreshToken(stale));

    const response = await transport(authed(`${PRIMARY}/api/v1/user/me`, 'old'));

    expect(response.status).toBe(401);
    expect(urls).toHaveLength(1);
    expect(manager.getToken()).toBeNull();
  });

  it('登录接口的 401（AUTH_INVALID_CREDENTIALS）不触发 refresh', async () => {
    const { impl } = recordingFetch(async () =>
      jsonResponse(401, envelope('AUTH_INVALID_CREDENTIALS', '邮箱或密码不正确')),
    );
    const refresh = vi.fn(async () => 'new');
    const transport = transportWith(impl, refresh);

    const response = await transport(new Request(`${PRIMARY}/api/v1/auth/login`, { method: 'POST', body: '{}' }));

    expect(response.status).toBe(401);
    expect(refresh).not.toHaveBeenCalled();
  });

  it('封禁（AUTH_PERMISSION_DENIED）不触发 refresh —— 换个 token 也换不掉封禁', async () => {
    const { impl } = recordingFetch(async () => jsonResponse(401, envelope('AUTH_PERMISSION_DENIED')));
    const refresh = vi.fn(async () => 'new');
    const transport = transportWith(impl, refresh);

    await transport(authed(`${PRIMARY}/api/v1/user/me`, 'old'));
    expect(refresh).not.toHaveBeenCalled();
  });

  it('refresh 端点自己 401 时不再 refresh —— 否则是无限递归', async () => {
    const { impl } = recordingFetch(async () => jsonResponse(401, envelope('AUTH_TOKEN_INVALID')));
    const refresh = vi.fn(async () => 'new');
    const transport = transportWith(impl, refresh);

    await transport(new Request(`${PRIMARY}/api/v1/auth/refresh`, { method: 'POST', body: '{}' }));
    expect(refresh).not.toHaveBeenCalled();
  });

  it('平台层的 401（HTML）不触发 refresh —— 换 token 换不回一个 IAP 会话', async () => {
    const { impl } = recordingFetch(
      async () => new Response('<html>login</html>', { status: 401, headers: { 'Content-Type': 'text/html' } }),
    );
    const refresh = vi.fn(async () => 'new');
    const transport = transportWith(impl, refresh);

    await transport(authed(`${PRIMARY}/api/v1/admin/users`, 'old'));
    expect(refresh).not.toHaveBeenCalled();
  });
});

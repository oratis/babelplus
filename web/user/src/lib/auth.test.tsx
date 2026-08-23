/**
 * 路由守卫的测试。
 *
 * 这里钉住的是**三态**而不是布尔值那一版的行为差异，两条都写成了独立用例：
 *  - 「确定未登录」→ 立刻跳登录，且带上校验过的 returnTo；
 *  - 「还没确定」→ **不许跳**。布尔值那一版初值只能取 false，
 *    表现为每次刷新页面先闪一下登录页；用户正在填的表单会因此消失。
 *
 * 走的是真实链路：`AuthProvider` → `api()` → 传输层 → 被替换掉的全局 `fetch`。
 * 不 mock `getCurrentUser`，因为「401 之后到底会不会跳登录」这个问题的答案
 * 一半在客户端的 refresh/重放逻辑里，mock 掉就测不到了。
 */
import { StrictMode } from 'react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { act, cleanup, render, screen, waitFor } from '@testing-library/react';
import { MemoryRouter, Route, Routes, useLocation } from 'react-router';
import { resetRuntimeConfig } from '@babelplus/shared';
import { AuthProvider, RequireAuth } from './auth.tsx';
import { resetApiClientForTests } from './api.ts';
import { ACCESS_TOKEN_KEY, resetSessionForTests } from './session.ts';

const PROTECTED_TEXT = '受保护内容';

/** 登录页替身：把当前地址原样打出来，好断言 returnTo。 */
function LoginProbe() {
  const location = useLocation();
  return <div data-testid="login">{`${location.pathname}${location.search}`}</div>;
}

function renderApp(initialEntry: string) {
  return render(
    <StrictMode>
      <MemoryRouter initialEntries={[initialEntry]}>
        <AuthProvider>
          <Routes>
            <Route path="/auth/login" element={<LoginProbe />} />
            <Route element={<RequireAuth />}>
              <Route path="/dashboard" element={<div>{PROTECTED_TEXT}</div>} />
              <Route path="/order/:trade_no" element={<div>{PROTECTED_TEXT}</div>} />
            </Route>
          </Routes>
        </AuthProvider>
      </MemoryRouter>
    </StrictMode>,
  );
}

function jsonResponse(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

const CURRENT_USER = {
  data: {
    id: 1,
    email: 'user@example.com',
    banned: false,
    created_at: '2026-08-23T00:00:00Z',
    balance_amount: 0,
    subscription: { total_bytes: 0, upload_bytes: 0, download_bytes: 0, device_limit: 0, device_count: 0 },
  },
  meta: { request_id: '01K2MEMEMEMEMEMEMEMEMEMEME' },
};

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((res) => {
    resolve = res;
  });
  return { promise, resolve };
}

beforeEach(() => {
  resetRuntimeConfig();
  resetSessionForTests();
  resetApiClientForTests();
  window.sessionStorage.clear();
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe('RequireAuth', () => {
  it('确定未登录 → 立刻跳登录并带上 returnTo，受保护内容一次都没渲染过', async () => {
    const fetchSpy = vi.fn();
    vi.stubGlobal('fetch', fetchSpy);

    renderApp('/dashboard');

    expect(screen.getByTestId('login').textContent).toBe(
      `/auth/login?returnTo=${encodeURIComponent('/dashboard')}`,
    );
    expect(screen.queryByText(PROTECTED_TEXT)).toBeNull();
    // 没有 token 时**不该发任何请求** —— 拿 401 来确认「我没登录」是一次白白的跨境往返。
    expect(fetchSpy).not.toHaveBeenCalled();
  });

  it('returnTo 保留查询串（用户回来时还在同一个标签页上）', () => {
    vi.stubGlobal('fetch', vi.fn());
    renderApp('/order/ORD-1?tab=pay');

    expect(screen.getByTestId('login').textContent).toBe(
      `/auth/login?returnTo=${encodeURIComponent('/order/ORD-1?tab=pay')}`,
    );
  });

  it('还没确定登录态时**不跳登录**，渲染 role="status" 的占位；确认后才放行', async () => {
    window.sessionStorage.setItem(ACCESS_TOKEN_KEY, 'token-alive');
    const gate = deferred<Response>();
    vi.stubGlobal('fetch', vi.fn(() => gate.promise));

    renderApp('/dashboard');

    // 这一屏可能停留数秒（跨境往返），但绝不能是登录页。
    expect(screen.getByRole('status')).toBeTruthy();
    expect(screen.queryByTestId('login')).toBeNull();
    expect(screen.queryByText(PROTECTED_TEXT)).toBeNull();

    await act(async () => {
      gate.resolve(jsonResponse(200, CURRENT_USER));
    });

    await waitFor(() => expect(screen.getByText(PROTECTED_TEXT)).toBeTruthy());
    expect(screen.queryByTestId('login')).toBeNull();
  });

  it('/me 回 401 → 试一次 refresh，失败后清会话并跳登录带 returnTo', async () => {
    window.sessionStorage.setItem(ACCESS_TOKEN_KEY, 'token-dead');
    const calls: string[] = [];
    vi.stubGlobal(
      'fetch',
      vi.fn(async (input: string | URL | Request) => {
        calls.push(new URL(String(input)).pathname);
        return jsonResponse(401, {
          error: { code: 'AUTH_TOKEN_INVALID', message: '会话无效或已过期' },
          meta: { request_id: '01K2DEADDEADDEADDEADDEADDE' },
        });
      }),
    );

    renderApp('/dashboard');

    await waitFor(() => expect(screen.getByTestId('login')).toBeTruthy());
    expect(screen.getByTestId('login').textContent).toBe(
      `/auth/login?returnTo=${encodeURIComponent('/dashboard')}`,
    );
    expect(window.sessionStorage.getItem(ACCESS_TOKEN_KEY)).toBeNull();
    // 一次 /me + 恰好一次 refresh。refresh 发第二次就意味着单飞破了。
    expect(calls.filter((p) => p === '/api/v1/auth/refresh')).toHaveLength(1);
  });

  it('确认登录态时网络不可达 → 显示可重试的错误态，**绝不跳登录**', async () => {
    window.sessionStorage.setItem(ACCESS_TOKEN_KEY, 'token-alive');
    vi.stubGlobal(
      'fetch',
      vi.fn(async () => {
        throw new TypeError('fetch failed');
      }),
    );

    renderApp('/dashboard');

    await waitFor(() => expect(screen.getByText('没能确认你的登录状态')).toBeTruthy());
    expect(screen.queryByTestId('login')).toBeNull();
    // 会话没有被清掉 —— 跨境抖一下不等于被登出。
    expect(window.sessionStorage.getItem(ACCESS_TOKEN_KEY)).toBe('token-alive');
  });
});

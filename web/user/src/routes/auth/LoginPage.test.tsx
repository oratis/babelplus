/**
 * 登录页的接线测试。
 *
 * 重点不在「表单能不能提交」，在**按 `ErrorCode` 分支**这件事上：
 * 后端对被封禁的账号返回的是 **401 + `AUTH_PERMISSION_DENIED`**（不是 403 ——
 * 契约没给这个端点定义 403，见 `handler/auth.go` 的 Login）。
 * 按 HTTP 状态码分支的写法会把封禁显示成「邮箱或密码不正确」，
 * 用户于是反复重试并开工单 —— `middleware/user.go` 的注释里点名了这条来回。
 * 这个用例存在的意义就是让那种「顺手改成按状态码判断」的重构在 CI 里红掉。
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { MemoryRouter, Route, Routes, useLocation } from 'react-router';
import { resetRuntimeConfig } from '@babelplus/shared';
import { AuthProvider } from '../../lib/auth.tsx';
import { resetApiClientForTests } from '../../lib/api.ts';
import { ACCESS_TOKEN_KEY, resetSessionForTests } from '../../lib/session.ts';
import LoginPage from './LoginPage.tsx';

function LandingProbe() {
  const location = useLocation();
  return <div data-testid="landed">{`${location.pathname}${location.search}`}</div>;
}

function renderLogin(search = '') {
  return render(
    <MemoryRouter initialEntries={[`/auth/login${search}`]}>
      <AuthProvider>
        <Routes>
          <Route path="/auth/login" element={<LoginPage />} />
          <Route path="/dashboard" element={<LandingProbe />} />
          <Route path="/order/:trade_no" element={<LandingProbe />} />
        </Routes>
      </AuthProvider>
    </MemoryRouter>,
  );
}

function jsonResponse(status: number, body: unknown, headers: Record<string, string> = {}): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json', ...headers },
  });
}

const SESSION_TOKENS = {
  data: { access_token: 'fresh', refresh_token: 'fresh', token_type: 'Bearer', expires_in: 2592000 },
  meta: { request_id: '01K2LOGINLOGINLOGINLOGINLO' },
};

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

function submitLogin(container: HTMLElement) {
  fireEvent.change(screen.getByLabelText('邮箱'), { target: { value: 'user@example.com' } });
  fireEvent.change(screen.getByLabelText('密码'), { target: { value: 'hunter2hunter2' } });
  const form = container.querySelector('form');
  if (!form) throw new Error('登录表单不存在');
  fireEvent.submit(form);
}

/** 登录接口返回指定响应，`/user/me` 一律成功。 */
function stubLogin(loginResponse: () => Response) {
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: string | URL | Request) => {
      const path = new URL(String(input)).pathname;
      if (path === '/api/v1/auth/login') return loginResponse();
      if (path === '/api/v1/user/me') return jsonResponse(200, CURRENT_USER);
      throw new Error(`未预期的请求：${path}`);
    }),
  );
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

describe('LoginPage', () => {
  it('登录成功 → 存下 token → 跳回校验过的 returnTo', async () => {
    stubLogin(() => jsonResponse(200, SESSION_TOKENS));
    const { container } = renderLogin(`?returnTo=${encodeURIComponent('/order/ORD-1?tab=pay')}`);

    submitLogin(container);

    await waitFor(() => expect(screen.getByTestId('landed')).toBeTruthy());
    expect(screen.getByTestId('landed').textContent).toBe('/order/ORD-1?tab=pay');
    expect(window.sessionStorage.getItem(ACCESS_TOKEN_KEY)).toBe('fresh');
  });

  it('returnTo 指向外站时落到 /dashboard，**不跳外站**', async () => {
    stubLogin(() => jsonResponse(200, SESSION_TOKENS));
    const { container } = renderLogin(`?returnTo=${encodeURIComponent('https://evil.example/steal')}`);

    submitLogin(container);

    await waitFor(() => expect(screen.getByTestId('landed')).toBeTruthy());
    expect(screen.getByTestId('landed').textContent).toBe('/dashboard');
  });

  it('401 AUTH_INVALID_CREDENTIALS → 统一文案，不说是哪一个错', async () => {
    stubLogin(() =>
      jsonResponse(401, {
        error: { code: 'AUTH_INVALID_CREDENTIALS', message: '邮箱或密码不正确' },
        meta: { request_id: '01K2BADBADBADBADBADBADBADB' },
      }),
    );
    const { container } = renderLogin();

    submitLogin(container);

    await waitFor(() => expect(screen.getByText('邮箱或密码不正确')).toBeTruthy());
    expect(screen.getByRole('alert').textContent).toContain('我们不说是哪一个');
    expect(window.sessionStorage.getItem(ACCESS_TOKEN_KEY)).toBeNull();
  });

  it('401 AUTH_PERMISSION_DENIED（封禁）→ 说封禁，**不说密码不对**', async () => {
    stubLogin(() =>
      jsonResponse(401, {
        error: { code: 'AUTH_PERMISSION_DENIED', message: '账号已被封禁' },
        meta: { request_id: '01K2BANBANBANBANBANBANBANB' },
      }),
    );
    const { container } = renderLogin();

    submitLogin(container);

    await waitFor(() => expect(screen.getByText('这个账号已被封禁')).toBeTruthy());
    expect(screen.getByRole('alert').textContent).not.toContain('邮箱或密码不正确');
  });

  it('429 → 用 Retry-After 做倒计时，读不到就不显示倒计时', async () => {
    stubLogin(() =>
      jsonResponse(
        429,
        { error: { code: 'QUOTA_RATE_LIMITED', message: '太频繁' }, meta: { request_id: '01K2RL' } },
        { 'Retry-After': '30' },
      ),
    );
    const { container } = renderLogin();

    submitLogin(container);

    await waitFor(() => expect(screen.getByText('30 秒后可再试')).toBeTruthy());
    expect(screen.getByRole('alert').textContent).toContain('30 秒后可以再试');
  });
});

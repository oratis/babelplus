/**
 * 守卫覆盖率：**对真实的 `App` 路由表逐条核对**。
 *
 * 为什么要有这一支，而不是靠 `lib/auth.test.tsx` 就够了：那一支渲染的是一棵**测试自建的**
 * 路由树（只有 `/dashboard` 与 `/order/:trade_no` 两条）。它验证的是 `RequireAuth` 这个组件
 * 的行为，验证不了 `App.tsx` 里那张表**有没有把每一条路由都放进守卫底下**。
 * 有人把某条路由挪到 `<Route element={<RequireAuth />}>` 外面的时候，那一支照样全绿。
 *
 * 而「漏掉一条路由」恰恰是这类缺陷的典型形态，且**不会有人报 bug**——
 * 表现只是「这一页不用登录也能看」，没有报错、没有白屏，谁都不会注意到。
 * 所以这里把清单钉死：受保护的一条不许漏，免登录的四页一条不许多。
 */
import { StrictMode } from 'react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { cleanup, render } from '@testing-library/react';
import { MemoryRouter } from 'react-router';
import { resetRuntimeConfig } from '@babelplus/shared';
import { App } from './App.tsx';
import { resetApiClientForTests } from './lib/api.ts';
import { resetSessionForTests } from './lib/session.ts';

/**
 * page-inventory §3.1 里登录后才能看的 16 条，外加两条**容易被忘掉**的：
 * 通配路由（未知地址不该把 404 页面漏给未登录访客）与 `/`（它重定向到 `/dashboard`）。
 */
const PROTECTED_PATHS = [
  '/dashboard',
  '/subscribe',
  '/subscribe/tokens',
  '/plan',
  '/order',
  '/order/ORD-1',
  '/ticket',
  '/ticket/T-1',
  '/profile',
  '/profile/2fa',
  '/usage',
  '/wallet',
  '/invite',
  '/node',
  '/notice',
  '/diagnose',
  '/some-unknown-path', // 通配路由
  '/', // 重定向到 /dashboard，到那里被守卫接住
];

/** 认证四页：给未登录用户看的，**必须**留在守卫外面，否则是重定向环。 */
const PUBLIC_PATHS = ['/auth/login', '/auth/register', '/auth/forgot', '/auth/reset'];

beforeEach(() => {
  resetRuntimeConfig();
  resetSessionForTests();
  resetApiClientForTests();
  window.sessionStorage.clear();
  // 没有 token 时守卫不该发任何请求。真发了，这个替身会让用例炸掉而不是悄悄放过。
  vi.stubGlobal(
    'fetch',
    vi.fn(async () => {
      throw new Error('未登录时不该发出任何请求');
    }),
  );
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

function renderAt(path: string): string {
  const { container } = render(
    <StrictMode>
      <MemoryRouter initialEntries={[path]}>
        <App />
      </MemoryRouter>
    </StrictMode>,
  );
  return container.textContent ?? '';
}

/** 登录页的判据：同时出现「邮箱」与「密码」两个字段标签。 */
function looksLikeLoginPage(text: string): boolean {
  return text.includes('邮箱') && text.includes('密码');
}

describe('App 路由表的守卫覆盖', () => {
  it.each(PROTECTED_PATHS)('未登录访问 %s → 落到登录页，页面内容一个字都不渲染', (path) => {
    const text = renderAt(path);
    expect(looksLikeLoginPage(text), `${path} 没有被守卫拦住，渲染出的是：${text.slice(0, 120)}`).toBe(
      true,
    );
  });

  it.each(PUBLIC_PATHS)('未登录访问 %s → 直达，不被守卫拦', (path) => {
    const text = renderAt(path);
    expect(text.length).toBeGreaterThan(0);
    // 认证页要是进了守卫，未登录访问会被跳回 /auth/login，形成环。
    expect(text).not.toContain('正在确认登录状态');
  });

  it('登录页本身可直达，且不因为守卫而变成重定向环', () => {
    expect(looksLikeLoginPage(renderAt('/auth/login'))).toBe(true);
  });
});

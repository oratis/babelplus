// @vitest-environment jsdom

/**
 * 守卫覆盖率：**对真实的 `App` 路由表逐条核对**。
 *
 * 为什么要有这一支，而不是靠 `lib/auth.test.tsx` 就够了：那一支渲染的是一棵**测试自建的**
 * 路由树（只有两条）。它验证的是 `RequireAdmin` 这个组件的行为，验证不了 `App.tsx` 里那张表
 * **有没有把每一条路由都放进守卫底下**。有人把某条路由挪到
 * `<Route element={<RequireAdmin />}>` 外面的时候，那一支照样全绿。
 *
 * 而「漏掉一条路由」恰恰是这类缺陷的典型形态，且**不会有人报 bug** ——
 * 表现只是「这一页不用准入也能打开」，没有报错、没有白屏。
 * 在后台它比在用户面板更糟：漏出去的那一页会照着契约把模块名、端点名、
 * 危险操作编号一起印在屏幕上（`ModuleScaffold` 就是这么渲染的），
 * 等于给一个还没被 IAP 认下的人发了一份系统结构说明。
 *
 * 所以这里把清单钉死：受保护的一条不许漏，免守卫的**只有** `/admin/login` 一页。
 */
import { StrictMode } from 'react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { cleanup, render, screen } from '@testing-library/react';
import { MemoryRouter } from 'react-router';
import { resetRuntimeConfig } from '@babelplus/shared';
import { App } from './App.tsx';
import { resetAdminApiForTests } from './lib/api.ts';
import { reportAdminAuthFailure } from './lib/iap.ts';

/**
 * page-inventory §4.2 的 17 个模块（含五条详情页），外加两条**容易被忘掉**的：
 * 通配路由（未准入的人不该看到 404 页面，那一页也带着后台的版式与导航）
 * 与 `/`（它重定向到 `/admin`，到那里被守卫接住）。
 */
const GUARDED_PATHS = [
  '/admin',
  '/admin/users',
  '/admin/users/42',
  '/admin/orders',
  '/admin/orders/20260816T7K2M9Q4',
  '/admin/plans',
  '/admin/nodes',
  '/admin/nodes/7',
  '/admin/node-keys',
  '/admin/stats',
  '/admin/tickets',
  '/admin/tickets/9',
  '/admin/invites',
  '/admin/audit',
  '/admin/admins',
  '/admin/notices',
  '/admin/coupons',
  '/admin/payments',
  '/admin/mail',
  '/admin/settings',
  '/admin/domains',
  '/admin/some-unknown-path', // 通配路由
  '/some-unknown-path', // 通配路由（`/admin` 前缀之外）
  '/', // 重定向到 /admin，到那里被守卫接住
];

/** 唯一一页在守卫外面。它进了守卫就是死循环：未准入时它自己也会被守卫接管。 */
const UNGUARDED_PATH = '/admin/login';

/** 管理面被拒时服务端返回的形状（`middleware/admin.go` 的 `adminDenied`：一律 403，不区分原因）。 */
function adminDenied(): Response {
  return new Response(
    JSON.stringify({
      error: { code: 'AUTH_PERMISSION_DENIED', message: '无权访问管理面' },
      meta: { request_id: '01K2ROUTESROUTESROUTESRO' },
    }),
    { status: 403, headers: { 'Content-Type': 'application/json' } },
  );
}

function adminAdmitted(): Response {
  return new Response(
    JSON.stringify({ data: [], meta: { request_id: '01K2ROUTESROUTESROUTESRO' } }),
    { status: 200, headers: { 'Content-Type': 'application/json' } },
  );
}

function renderAt(path: string) {
  return render(
    <StrictMode>
      <MemoryRouter initialEntries={[path]}>
        <App />
      </MemoryRouter>
    </StrictMode>,
  );
}

beforeEach(() => {
  resetRuntimeConfig();
  resetAdminApiForTests();
  reportAdminAuthFailure(null);
  window.sessionStorage.clear();
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe('App 路由表的守卫覆盖', () => {
  it.each(GUARDED_PATHS)('未准入访问 %s → 被守卫接住，模块内容一个字都不渲染', async (path) => {
    vi.stubGlobal('fetch', vi.fn(async () => adminDenied()));
    const { container } = renderAt(path);

    // 守卫的说明块（`AdmissionNotice`）出现。
    // ⚠️ 用 findAll 而不是 find：同一句话在页面上会出现两次 —— 一次在守卫的说明块里，
    // 一次在常驻的 `<AuthFailureBanner />` 上（它挂在 `<Routes>` 外面）。
    // 用 findBy 的话，断言会在「只渲染出其中一处」的那一瞬间偶然通过，
    // 于是这一支会变成一个时序敏感的随机测试。
    expect((await screen.findAllByText(/身份通过了，但这个操作被拒绝/)).length).toBeGreaterThan(0);

    // 🔴 结构性断言：`AdminLayout` 是守卫的**子**路由，它一旦渲染就说明守卫没接住。
    // 只断文案的话，某一页自己恰好也印着同样几个字时就会漏掉。
    expect(
      container.querySelector('nav'),
      `${path} 渲染出了后台导航，说明它没有被 RequireAdmin 包住`,
    ).toBeNull();
  });

  it(`未准入访问 ${UNGUARDED_PATH} → 直达，不被守卫接管`, async () => {
    vi.stubGlobal('fetch', vi.fn(async () => adminDenied()));
    renderAt(UNGUARDED_PATH);

    // 这一页自己的内容必须在（它被守卫接管的话，就只剩说明块了）。
    expect(await screen.findByText('管理员登录')).toBeTruthy();
    // 而且它自己也会把准入结论显示出来 —— 那是这一页现在的主要作用。
    expect((await screen.findAllByText(/身份通过了，但这个操作被拒绝/)).length).toBeGreaterThan(0);
  });

  it('准入通过 → 守卫放行，后台版式与模块内容都渲染出来（守卫不是一堵墙）', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => adminAdmitted()));
    const { container } = renderAt('/admin');

    // 「运营看板」在侧边导航与页头上各出现一次，所以是 findAll。
    expect((await screen.findAllByText('运营看板')).length).toBeGreaterThan(0);
    expect(container.querySelector('nav')).not.toBeNull();
  });

  it('准入通过后停在登录页没有意义 → 回到 /admin', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => adminAdmitted()));
    const { container } = renderAt(UNGUARDED_PATH);

    expect((await screen.findAllByText('运营看板')).length).toBeGreaterThan(0);
    expect(container.querySelector('nav')).not.toBeNull();
  });
});

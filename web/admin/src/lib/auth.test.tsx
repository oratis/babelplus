// @vitest-environment jsdom
//
// 这一支需要 DOM：它验的是「守卫渲染了什么」，而「有没有渲染出受保护内容」
// 只有真的把组件挂起来才能回答。仓库级的 vitest 配置是 `node`（`lib/iap.test.ts`
// 测的是纯函数，不需要 DOM），所以这里用文件级 docblock 单独提高环境，
// 而不是把整个包的默认环境改掉 —— 那会让那一支明明不需要 DOM 的测试也扛上 jsdom 的启动开销。

/**
 * 后台准入守卫的测试。
 *
 * 走的是**真实链路**：`AdminAuthProvider` → `api()` → 传输层 → 被替换掉的全局 `fetch`。
 * 不 mock 探测函数，因为这里最要紧的几条结论（IAP 拒绝与应用层拒绝的分流、
 * 401 会不会先去 refresh）一半在客户端里，mock 掉就测不到了。
 *
 * 钉住的是四条**处置**，它们两两不同，写错任何一条都不会有人报 bug：
 *
 *  1. 平台层（IAP）拒绝 → **不跳登录页**，提示重新走 Google 登录；
 *  2. 应用层 403（不是管理员）→ **不跳登录页**，说明重登没用；
 *  3. 网络不可达 / 5xx → **不跳、不清、不下结论**，留在「还不知道」；
 *  4. 应用层 401 → 这才跳 `/admin/login`，且带上校验过的 returnTo。
 *
 * 其中 1、2、3 全都是「看起来该跳登录页但绝不能跳」的情形 —— 这正是本文件存在的理由。
 */
import { StrictMode } from 'react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { cleanup, render, screen, waitFor } from '@testing-library/react';
import { MemoryRouter, Route, Routes, useLocation } from 'react-router';
import { resetRuntimeConfig } from '@babelplus/shared';
import { IAP_GENERATED_HEADER } from '@babelplus/shared/api';
import { AdminAuthProvider, RequireAdmin } from './auth.tsx';
import { resetAdminApiForTests } from './api.ts';
import { reportAdminAuthFailure } from './iap.ts';
import { NavigationBridge } from '../components/NavigationBridge.tsx';

const PROTECTED_TEXT = '受保护的后台内容';

/** 登录页替身：把当前地址原样打出来，好断言 returnTo。 */
function LoginProbe() {
  const location = useLocation();
  return <div data-testid="login">{`${location.pathname}${location.search}`}</div>;
}

function renderApp(initialEntry: string) {
  return render(
    <StrictMode>
      <MemoryRouter initialEntries={[initialEntry]}>
        <AdminAuthProvider>
          {/* 真实 App 里也挂着它：没有它，API 层的 401 跳转会退化成整页跳转，
              在 jsdom 里表现为一条 "Not implemented: navigation" 噪声。 */}
          <NavigationBridge />
          <Routes>
            <Route path="/admin/login" element={<LoginProbe />} />
            <Route element={<RequireAdmin />}>
              <Route path="/admin" element={<div>{PROTECTED_TEXT}</div>} />
              <Route path="/admin/orders/:trade_no" element={<div>{PROTECTED_TEXT}</div>} />
            </Route>
          </Routes>
        </AdminAuthProvider>
      </MemoryRouter>
    </StrictMode>,
  );
}

/**
 * 每次调用都造一个**新的** `Response`：body 是一次性的流，
 * 而 StrictMode 下 effect 会跑两遍，同一个对象复用会在第二次读时炸掉。
 */
function respondWith(make: () => Response) {
  const spy = vi.fn(async () => make());
  vi.stubGlobal('fetch', spy);
  return spy;
}

function envelope(status: number, code: string, message: string): Response {
  return new Response(
    JSON.stringify({ error: { code, message }, meta: { request_id: '01K2AUDITAUDITAUDITAUDIT' } }),
    { status, headers: { 'Content-Type': 'application/json' } },
  );
}

function okAuditPage(): Response {
  return new Response(JSON.stringify({ data: [], meta: { request_id: '01K2AUDITAUDITAUDITAUDIT' } }), {
    status: 200,
    headers: { 'Content-Type': 'application/json' },
  });
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

describe('RequireAdmin', () => {
  it('探测成功 → 放行，受保护内容渲染出来', async () => {
    respondWith(okAuditPage);
    renderApp('/admin');

    expect(await screen.findByText(PROTECTED_TEXT)).toBeTruthy();
  });

  it('探测还没回来时**不跳转**，渲染 role="status" 的占位', async () => {
    // 永远不 resolve：模拟一次很慢的跨境往返。
    vi.stubGlobal('fetch', vi.fn(() => new Promise<Response>(() => {})));
    renderApp('/admin');

    expect(screen.getByRole('status')).toBeTruthy();
    expect(screen.queryByTestId('login')).toBeNull();
    expect(screen.queryByText(PROTECTED_TEXT)).toBeNull();
  });

  it('IAP（平台层）拒绝 → 绝不跳登录页，且提示去重新走 Google 登录', async () => {
    // IAP 自己生成的响应：带它的标记头、body 是 HTML 而不是我们的信封。
    respondWith(
      () =>
        new Response('<html>Sign in with Google</html>', {
          status: 401,
          headers: {
            'Content-Type': 'text/html',
            // 用常量而不是字面量：头名将来若改（它是「需实测」的，取自 GCP 文档而非抓包），
            // 这一支要跟着一起变，而不是继续绿着测一个已经不存在的头。
            [IAP_GENERATED_HEADER]: 'true',
          },
        }),
    );
    renderApp('/admin');

    expect(await screen.findByText(/被平台层挡下了/)).toBeTruthy();
    // 🔴 这三条是这个用例的全部意义：跳登录页在这里毫无用处，因为请求根本没到我们的服务。
    expect(screen.queryByTestId('login')).toBeNull();
    expect(screen.queryByText(PROTECTED_TEXT)).toBeNull();
    expect(screen.getByText(/重新走 Google 登录/)).toBeTruthy();
  });

  it('应用层 403（不是管理员）→ 不跳登录页，并说明重登不会有帮助', async () => {
    respondWith(() => envelope(403, 'AUTH_PERMISSION_DENIED', '无权访问管理面'));
    renderApp('/admin');

    expect(await screen.findByText(/身份通过了，但这个操作被拒绝/)).toBeTruthy();
    expect(screen.getByText(/重新登录、换浏览器、清缓存都不会改变这个结果/)).toBeTruthy();
    expect(screen.queryByTestId('login')).toBeNull();
    expect(screen.queryByText(PROTECTED_TEXT)).toBeNull();
  });

  it('请求没走到服务端 → 留在「还不知道」，两种可能都说出来，且不跳转', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(async () => {
        throw new TypeError('Failed to fetch');
      }),
    );
    renderApp('/admin');

    expect(await screen.findByText(/请求没能到达后台/)).toBeTruthy();
    // 大陆场景下这两件事会同时发生，断言其中一种就是在猜。
    expect(screen.getByText(/IAP 会话已过期/)).toBeTruthy();
    expect(screen.queryByTestId('login')).toBeNull();
    expect(screen.queryByText(PROTECTED_TEXT)).toBeNull();
  });

  it('5xx → 可重试的错误态，**不当成被拒**，也不跳转', async () => {
    respondWith(() => envelope(500, 'INTERNAL_ERROR', '读取审计日志失败'));
    renderApp('/admin');

    expect(await screen.findByText('没能确认你的后台准入状态')).toBeTruthy();
    expect(screen.getByRole('button', { name: '重试' })).toBeTruthy();
    expect(screen.queryByTestId('login')).toBeNull();
    expect(screen.queryByText(PROTECTED_TEXT)).toBeNull();
  });

  it('应用层 401 → 这一种才跳 /admin/login，且带上校验过的 returnTo', async () => {
    // 用 AUTH_INVALID_CREDENTIALS 而不是 AUTH_TOKEN_INVALID：后者会先触发一次静默 refresh，
    // 那条路径有它自己的测试（shared/api/client.test.ts），不该混进来。
    respondWith(() => envelope(401, 'AUTH_INVALID_CREDENTIALS', '会话无效'));
    renderApp('/admin/orders/ORD-1?tab=pay');

    await waitFor(() => expect(screen.getByTestId('login')).toBeTruthy());
    expect(screen.getByTestId('login').textContent).toBe(
      `/admin/login?returnTo=${encodeURIComponent('/admin/orders/ORD-1?tab=pay')}`,
    );
    expect(screen.queryByText(PROTECTED_TEXT)).toBeNull();
  });

  it('探测成功会撤掉之前那条鉴权失败横幅（否则运维会盯着一个已经好了的红条继续折腾）', async () => {
    respondWith(okAuditPage);
    reportAdminAuthFailure({
      kind: 'edge',
      title: '旧的失败',
      description: '',
      evidence: '',
      signOutLocally: false,
      requestId: undefined,
    });

    renderApp('/admin');
    await screen.findByText(PROTECTED_TEXT);

    const { getAdminAuthFailure } = await import('./iap.ts');
    expect(getAdminAuthFailure()).toBeNull();
  });
});

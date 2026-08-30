/**
 * 找回密码页的接线测试。
 *
 * 这一页只有一条真正要紧的规则，而它**恰好是最容易被顺手改掉的那种**：
 *
 * 🔴 **无论邮箱在不在我们这里，用户看到的必须是同一屏**（page-inventory §3.2.1 防枚举）。
 *
 * 它会被改掉的路径非常具体：某天有人为了「体验更好」加一句
 * 「该邮箱未注册，检查一下有没有打错」—— 这句话看起来是帮忙，实际上是把
 * `/auth/forgot` 变成一个**免登录的账号存在性查询接口**：拿一份邮箱列表打一遍，
 * 就知道谁是我们的用户。而我们的用户是「翻墙的人」，这份名单本身就是危险品。
 * 后端为此把「邮箱不存在」「per email 超限」全都做成 204
 * （`handler/auth.go` 的 `ForgotPassword`），前端**不许**在自己这一侧把它拆穿。
 *
 * 所以下面的用例分两组：
 *   · 会泄漏「邮箱在不在」的失败（404 / 422 / 409）→ **必须**显示成功页；
 *   · 不会泄漏的失败（429 per IP、5xx、离线、501）→ **必须**照实说。
 *     后者是对空壳里那句「无论后端返回什么」的收窄，理由是：假装成功拿不到任何
 *     防枚举收益，只会让一个正在找回账号的人白等一封永远不会到的信，
 *     而这一页的成功率就是失联恢复的成功率（ADR 0002）。
 *
 * 另外钉住成功页上的白名单引导 —— roadmap §5.2 2.C 第 3 条的硬要求。
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { MemoryRouter, Route, Routes } from 'react-router';
import { resetRuntimeConfig } from '@babelplus/shared';
import { resetApiClientForTests } from '../../lib/api.ts';
import { resetSessionForTests } from '../../lib/session.ts';
import ForgotPage from './ForgotPage.tsx';

const FORGOT = '/api/v1/auth/password/forgot';

function jsonResponse(status: number, body: unknown, headers: Record<string, string> = {}): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json', ...headers },
  });
}

function errorBody(code: string, message: string) {
  return { error: { code, message }, meta: { request_id: '01K2FORGOTFORGOTFORGOTFORG' } };
}

/** `/auth/password/forgot` 返回指定响应；返回值是调用流水。 */
function stubForgot(response: () => Response): string[] {
  const calls: string[] = [];
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: string | URL | Request) => {
      const path = new URL(String(input)).pathname;
      calls.push(path);
      if (path === FORGOT) return response();
      throw new Error(`未预期的请求：${path}`);
    }),
  );
  return calls;
}

function renderForgot() {
  return render(
    <MemoryRouter initialEntries={['/auth/forgot']}>
      <Routes>
        <Route path="/auth/forgot" element={<ForgotPage />} />
        <Route path="/auth/login" element={<div data-testid="landed">login</div>} />
      </Routes>
    </MemoryRouter>,
  );
}

function submitEmail(container: HTMLElement, email = 'someone@example.com') {
  fireEvent.change(screen.getByLabelText('邮箱'), { target: { value: email } });
  const form = container.querySelector('form');
  if (!form) throw new Error('表单不存在');
  fireEvent.submit(form);
}

/** 成功页的判据：三句话缺一不可 —— 已发出、有效期、白名单引导。 */
function looksLikeSentPanel(text: string): boolean {
  return text.includes('邮件已发出') && text.includes('30 分钟') && text.includes('设置域名白名单');
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

describe('ForgotPage', () => {
  it('204 → 成功页，且成功页上必须有白名单引导（roadmap §5.2 2.C 第 3 条）', async () => {
    const calls = stubForgot(() => new Response(null, { status: 204 }));
    const { container } = renderForgot();

    submitEmail(container);

    await waitFor(() => expect(screen.getByText('邮件已发出')).toBeTruthy());
    expect(looksLikeSentPanel(container.textContent ?? '')).toBe(true);
    expect(calls).toEqual([FORGOT]);
    // 邮箱回显出来，用户能一眼看出自己有没有打错。
    expect(container.textContent).toContain('someone@example.com');
  });

  it('🔴 404 RESOURCE_NOT_FOUND → **一模一样的成功页**，绝不说「该邮箱未注册」', async () => {
    stubForgot(() => jsonResponse(404, errorBody('RESOURCE_NOT_FOUND', '用户不存在')));
    const { container } = renderForgot();

    submitEmail(container, 'nobody@example.com');

    await waitFor(() => expect(screen.getByText('邮件已发出')).toBeTruthy());
    expect(looksLikeSentPanel(container.textContent ?? '')).toBe(true);
    // 这几个词里的任何一个出现在这一屏上，`/auth/forgot` 就是一个账号存在性查询接口。
    expect(container.textContent).not.toContain('未注册');
    expect(container.textContent).not.toContain('不存在');
    expect(screen.queryByRole('alert')).toBeNull();
  });

  it('🔴 422 VALIDATION_FAILED → 同样是成功页（后端可能在校验阶段就分了岔）', async () => {
    stubForgot(() => jsonResponse(422, errorBody('VALIDATION_FAILED', '邮箱格式不正确')));
    const { container } = renderForgot();

    submitEmail(container);

    await waitFor(() => expect(screen.getByText('邮件已发出')).toBeTruthy());
    expect(screen.queryByRole('alert')).toBeNull();
  });

  it('429（per IP）→ 倒计时，**不显示成功页** —— 信根本没发出去', async () => {
    stubForgot(() =>
      jsonResponse(429, errorBody('QUOTA_RATE_LIMITED', '操作过于频繁'), { 'Retry-After': '42' }),
    );
    const { container } = renderForgot();

    submitEmail(container);

    await waitFor(() => expect(screen.getByText('42 秒后可再试')).toBeTruthy());
    expect(screen.getByRole('alert').textContent).toContain('42 秒后可以再试');
    // per IP 的限流与「这个邮箱在不在」无关，说出来不泄漏任何东西；
    // 而假装成功会让用户一直等一封没发出去的信。
    expect(container.textContent).not.toContain('邮件已发出');
  });

  it('5xx → 如实报错，不假装已发出', async () => {
    stubForgot(() => jsonResponse(500, errorBody('INTERNAL_ERROR', '内部错误')));
    const { container } = renderForgot();

    submitEmail(container);

    await waitFor(() => expect(screen.getByText('我们这边出了问题')).toBeTruthy());
    expect(container.textContent).not.toContain('邮件已发出');
    expect(screen.getByRole('alert').textContent).toContain('请求号');
  });

  it('501 → 「该功能尚未开放」，不是「未知错误」', async () => {
    stubForgot(() => jsonResponse(501, errorBody('NOT_IMPLEMENTED', '尚未实现')));
    const { container } = renderForgot();

    submitEmail(container);

    await waitFor(() => expect(screen.getByText('该功能尚未开放')).toBeTruthy());
    expect(container.textContent).toContain('重试没有用');
    expect(container.textContent).not.toContain('邮件已发出');
  });

  it('邮箱一眼就不合法时不发请求 —— 这一页对 422 也显示成功页，发出去就再没有反馈了', () => {
    const calls = stubForgot(() => new Response(null, { status: 204 }));
    const { container } = renderForgot();

    submitEmail(container, 'not-an-email');

    expect(calls).toHaveLength(0);
    expect(container.textContent).not.toContain('邮件已发出');
  });
});

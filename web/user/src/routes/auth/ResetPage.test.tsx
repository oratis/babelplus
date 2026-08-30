/**
 * 重置密码页的接线测试。
 *
 * 这一页有两条规则，改错了都**不会有任何报错**，只会安静地把人卡住或坑掉：
 *
 * 🔴 **1. 链接失效时在本页重发，不把用户踢回 `/auth/forgot`**（page-inventory §3.2.1）。
 *    用户是从邮件点进来的 —— 想起邮箱、翻收件箱、翻垃圾箱，这一段他已经走过一遍了。
 *    推回上一页等于让他重走，而这一段的每一步都是真实的流失点。
 *    「顺手改成 `<Navigate to="/auth/forgot" />`」是个看起来更简洁、体验更差的重构，
 *    这一支就是为了让它红掉。
 *    ⚠️ 附带一条：后端把「不存在 / 已用过 / 已过期」**全部**回同一个 401
 *    （`handler/auth.go` 的 `ResetPassword`：「在 SQL 层已经不可区分，这是好事」），
 *    所以前端也不许编出「这个链接已经用过了」这种我们其实区分不了的话。
 *
 * 🔴 **2. 成功后不自动登录。** 后端撤销了该用户的**全部**会话，也没给我们任何 token。
 *    让用户用新密码登一次，是确认他真的记住了它 —— 否则最典型的场景是
 *    「改完密码、关掉页面、第二天又进不去了」。用例里直接断言 sessionStorage 是空的。
 *
 * 另外钉住：写操作的二次确认（第一次点击只展开确认，不发请求）、
 * 两次输入不一致时一个请求都不发、以及 501 / 429 两条通用分支。
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { MemoryRouter, Route, Routes } from 'react-router';
import { resetRuntimeConfig } from '@babelplus/shared';
import { resetApiClientForTests } from '../../lib/api.ts';
import { ACCESS_TOKEN_KEY, resetSessionForTests } from '../../lib/session.ts';
import ResetPage from './ResetPage.tsx';

const RESET = '/api/v1/auth/password/reset';
const FORGOT = '/api/v1/auth/password/forgot';

const TOKEN = 'Zm9vYmFyLXJlc2V0LXRva2VuLTMyLWJ5dGVz';
const NEW_PASSWORD = 'hunter2hunter2';

function jsonResponse(status: number, body: unknown, headers: Record<string, string> = {}): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json', ...headers },
  });
}

function errorBody(code: string, message: string) {
  return { error: { code, message }, meta: { request_id: '01K2RESETRESETRESETRESETRE' } };
}

interface Call {
  readonly path: string;
  readonly body: unknown;
}

function stubApi(routes: Record<string, () => Response>): Call[] {
  const calls: Call[] = [];
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: string | URL | Request, init?: RequestInit) => {
      const path = new URL(String(input)).pathname;
      let body: unknown = undefined;
      if (typeof init?.body === 'string') body = JSON.parse(init.body);
      else if (init?.body instanceof ArrayBuffer) body = JSON.parse(new TextDecoder().decode(init.body));
      calls.push({ path, body });
      const handler = routes[path];
      if (!handler) throw new Error(`未预期的请求：${path}`);
      return handler();
    }),
  );
  return calls;
}

/** `/auth/forgot` 挂一个探针路由：跳过去了就说明第 1 条被破坏了。 */
function renderReset(search = `?token=${TOKEN}`) {
  return render(
    <MemoryRouter initialEntries={[`/auth/reset${search}`]}>
      <Routes>
        <Route path="/auth/reset" element={<ResetPage />} />
        <Route path="/auth/forgot" element={<div data-testid="landed">forgot</div>} />
        <Route path="/auth/login" element={<div data-testid="landed">login</div>} />
      </Routes>
    </MemoryRouter>,
  );
}

function fillPasswords(password = NEW_PASSWORD, confirm = password) {
  fireEvent.change(screen.getByLabelText('新密码'), { target: { value: password } });
  fireEvent.change(screen.getByLabelText('再输一次'), { target: { value: confirm } });
}

function submitForm(container: HTMLElement) {
  const form = container.querySelector('form');
  if (!form) throw new Error('表单不存在');
  fireEvent.submit(form);
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

describe('ResetPage · 设置新密码', () => {
  it('🔴 第一次提交只展开二次确认，**不发请求**；确认之后才真的改', async () => {
    const calls = stubApi({ [RESET]: () => new Response(null, { status: 204 }) });
    const { container } = renderReset();

    fillPasswords();
    submitForm(container);

    // 副作用要说清楚：全部设备退出登录，这对「只是忘了密码」的人是个真实的意外。
    await waitFor(() => expect(screen.getByText('确认一下：这会让所有设备退出登录')).toBeTruthy());
    expect(calls).toHaveLength(0);

    submitForm(container);
    await waitFor(() => expect(screen.getByText('密码已经改好了')).toBeTruthy());
    expect(calls.map((c) => c.path)).toEqual([RESET]);
    expect(calls[0]?.body).toEqual({ token: TOKEN, password: NEW_PASSWORD });
  });

  it('🔴 成功之后**不自动登录** —— 一个 token 都不许写进会话', async () => {
    stubApi({ [RESET]: () => new Response(null, { status: 204 }) });
    const { container } = renderReset();

    fillPasswords();
    submitForm(container);
    await waitFor(() => expect(screen.getByText('确认一下：这会让所有设备退出登录')).toBeTruthy());
    submitForm(container);

    await waitFor(() => expect(screen.getByText('密码已经改好了')).toBeTruthy());
    expect(window.sessionStorage.getItem(ACCESS_TOKEN_KEY)).toBeNull();
    // 用户要做的下一件事只有一件：用新密码登一次。
    expect(screen.getByRole('link', { name: '用新密码登录' })).toBeTruthy();
  });

  it('两次输入不一致 → 一个请求都不发（后端只收一个 password 字段，它认不出这种错）', () => {
    const calls = stubApi({ [RESET]: () => new Response(null, { status: 204 }) });
    const { container } = renderReset();

    fillPasswords(NEW_PASSWORD, 'hunter2hunter3');
    submitForm(container);

    expect(calls).toHaveLength(0);
    expect(screen.getByText('两次输入不一样。')).toBeTruthy();
  });

  it('密码太短 → 不发请求，并按后端的口径说明（只强制长度，8–128）', () => {
    const calls = stubApi({ [RESET]: () => new Response(null, { status: 204 }) });
    const { container } = renderReset();

    fillPasswords('short');
    submitForm(container);

    expect(calls).toHaveLength(0);
    expect(screen.getByText('密码至少 8 个字符。')).toBeTruthy();
  });

  it('429 → 倒计时秒数来自 Retry-After', async () => {
    stubApi({
      [RESET]: () =>
        jsonResponse(429, errorBody('QUOTA_RATE_LIMITED', '太频繁'), { 'Retry-After': '25' }),
    });
    const { container } = renderReset();

    fillPasswords();
    submitForm(container);
    await waitFor(() => expect(screen.getByText('确认一下：这会让所有设备退出登录')).toBeTruthy());
    submitForm(container);

    await waitFor(() => expect(screen.getByText('25 秒后可再试')).toBeTruthy());
    expect(screen.getByRole('alert').textContent).toContain('25 秒后可以再试');
  });

  it('501 → 「该功能尚未开放」，不是「未知错误」', async () => {
    stubApi({ [RESET]: () => jsonResponse(501, errorBody('NOT_IMPLEMENTED', '尚未实现')) });
    const { container } = renderReset();

    fillPasswords();
    submitForm(container);
    await waitFor(() => expect(screen.getByText('确认一下：这会让所有设备退出登录')).toBeTruthy());
    submitForm(container);

    await waitFor(() => expect(screen.getByText('该功能尚未开放')).toBeTruthy());
    expect(container.textContent).toContain('重试没有用');
  });
});

describe('ResetPage · 链接失效', () => {
  it('🔴 401 → 在**本页**给重发表单，不跳回 /auth/forgot', async () => {
    stubApi({ [RESET]: () => jsonResponse(401, errorBody('AUTH_TOKEN_INVALID', '重置链接无效或已过期')) });
    const { container } = renderReset();

    fillPasswords();
    submitForm(container);
    await waitFor(() => expect(screen.getByText('确认一下：这会让所有设备退出登录')).toBeTruthy());
    submitForm(container);

    await waitFor(() => expect(screen.getByText('这个链接已经失效了')).toBeTruthy());
    // 没跳走：重发的输入框就在这一屏上。
    expect(screen.queryByTestId('landed')).toBeNull();
    expect(screen.getByLabelText('邮箱')).toBeTruthy();
    expect(screen.getByRole('button', { name: '重新发一封' })).toBeTruthy();
    // 后端区分不了「用过」和「过期」，前端也不许编。
    expect(container.textContent).not.toContain('已经用过');
    expect(container.textContent).not.toContain('请重新登录');
  });

  it('地址里没有 token → 「链接不完整」，同样在本页给重发表单', () => {
    stubApi({});
    renderReset('');

    expect(screen.getByText('链接不完整')).toBeTruthy();
    expect(screen.getByLabelText('邮箱')).toBeTruthy();
    expect(screen.queryByTestId('landed')).toBeNull();
  });

  it('本页重发走的是 forgot，所以 404 也显示「已发出」（防枚举规则跨页一致）', async () => {
    const calls = stubApi({
      [FORGOT]: () => jsonResponse(404, errorBody('RESOURCE_NOT_FOUND', '用户不存在')),
    });
    const { container } = renderReset('');

    fireEvent.change(screen.getByLabelText('邮箱'), { target: { value: 'nobody@example.com' } });
    submitForm(container);

    await waitFor(() => expect(screen.getByText(/新的重置链接已经发出了/)).toBeTruthy());
    expect(calls.map((c) => c.path)).toEqual([FORGOT]);
    expect(container.textContent).not.toContain('未注册');
    expect(container.textContent).not.toContain('不存在');
    // 重发成功页同样带白名单引导 —— 收不到信才是这条链路真正的失败模式。
    expect(container.textContent).toContain('设置域名白名单');
  });

  it('本页重发 429 → 倒计时，不假装已发出', async () => {
    stubApi({
      [FORGOT]: () =>
        jsonResponse(429, errorBody('QUOTA_RATE_LIMITED', '操作过于频繁'), { 'Retry-After': '30' }),
    });
    const { container } = renderReset('');

    fireEvent.change(screen.getByLabelText('邮箱'), { target: { value: 'someone@example.com' } });
    submitForm(container);

    await waitFor(() => expect(screen.getByText('30 秒后可再试')).toBeTruthy());
    expect(container.textContent).not.toContain('新的重置链接已经发出了');
  });
});

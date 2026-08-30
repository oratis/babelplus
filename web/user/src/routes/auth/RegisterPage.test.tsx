/**
 * 注册页的接线测试。
 *
 * 每一条用例都对着一条**改错了不会报错、只会安静地变糟**的规则：
 *
 *  1. **邀请码「无效」与「已用尽」必须分开说**（page-inventory §3.2.1）。
 *     两者的用户动作完全不同：一个是「你抄错了，再核一遍」，另一个是
 *     「这个码没了，找邀请你的人再要一个」。合并成一句「邀请码不可用」之后，
 *     页面照常渲染、控制台一片安静，只有被邀请的人卡在门口不知道下一步该干什么。
 *
 *  2. **422 的真正原因在 `details[].reason` 里，不在 `message` 里。**
 *     后端对邀请码无效 / 已用尽 / 验证码过期 / 验证码错太多次**全部**回
 *     422 `VALIDATION_FAILED`（`handler/auth.go` 的 `s.unprocessable(...)`）。
 *     谁把分支简化成「看 code 就够了」，第 1 条当场作废 —— 这一支就是为了让那种简化红掉。
 *
 *  3. **邮箱已注册（409 `STATE_CONFLICT`）不是错误，是一条岔路。**
 *     渲染成红色错误框，用户会以为自己填错了什么并反复重试；他真正需要的是去登录。
 *
 *  4. **顺序是 verify → 发码 → 注册。** 后端刻意「验证码通过后才建账号并核销邀请码」
 *     （user-journey §3），因为邀请码是稀缺资源，先核销后失败等于每输错一次码就烧掉一个。
 *     顺序钉在这里，免得有人为了「少一次往返」把 verify 挪到提交之后。
 *
 *  5. **注册成功页必须有白名单引导**（roadmap §5.2 2.C 第 3 条，硬要求不是装饰）。
 *     邮件是 ADR 0002 裁决的唯一失联恢复通道，而用户此刻刚证明了自己能收到我们的信 ——
 *     这是让他加白名单的最好时机。删掉它没有任何症状，代价要到封锁那天才显形。
 *
 *  6. 429 的秒数只能来自 `Retry-After`，501 要说「尚未开放」而不是「未知错误」。
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { MemoryRouter, Route, Routes } from 'react-router';
import { resetRuntimeConfig } from '@babelplus/shared';
import { AuthProvider } from '../../lib/auth.tsx';
import { resetApiClientForTests } from '../../lib/api.ts';
import { ACCESS_TOKEN_KEY, resetSessionForTests } from '../../lib/session.ts';
import RegisterPage from './RegisterPage.tsx';

const VERIFY = '/api/v1/invite/verify';
const EMAIL_CODE = '/api/v1/auth/email-code';
const REGISTER = '/api/v1/auth/register';
const ME = '/api/v1/user/me';

function jsonResponse(status: number, body: unknown, headers: Record<string, string> = {}): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json', ...headers },
  });
}

function noContent(): Response {
  return new Response(null, { status: 204 });
}

function errorBody(code: string, message: string, details?: Array<{ field: string; reason: string }>) {
  return {
    error: details ? { code, message, details } : { code, message },
    meta: { request_id: '01K2REGREGREGREGREGREGREGR' },
  };
}

const SESSION_TOKENS = {
  data: { access_token: 'fresh', refresh_token: 'fresh', token_type: 'Bearer', expires_in: 2592000 },
  meta: { request_id: '01K2REGISTERREGISTERREGIST' },
};

const CURRENT_USER = {
  data: {
    id: 7,
    email: 'newcomer@example.com',
    banned: false,
    created_at: '2026-08-30T00:00:00Z',
    balance_amount: 0,
    subscription: { total_bytes: 0, upload_bytes: 0, download_bytes: 0, device_limit: 0, device_count: 0 },
  },
  meta: { request_id: '01K2MEMEMEMEMEMEMEMEMEMEME' },
};

interface Call {
  readonly path: string;
  readonly body: unknown;
}

/** 按路径分发的 fetch 替身。**返回调用流水**，顺序类断言直接读它。 */
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

function renderRegister() {
  return render(
    <MemoryRouter initialEntries={['/auth/register']}>
      <AuthProvider>
        <Routes>
          <Route path="/auth/register" element={<RegisterPage />} />
          <Route path="/dashboard" element={<div data-testid="landed">dashboard</div>} />
          <Route path="/auth/login" element={<div data-testid="landed">login</div>} />
        </Routes>
      </AuthProvider>
    </MemoryRouter>,
  );
}

function submit(container: HTMLElement) {
  const form = container.querySelector('form');
  if (!form) throw new Error('表单不存在');
  fireEvent.submit(form);
}

/** 填第一步并失焦触发邀请码预检。 */
function fillForm(inviteCode = 'BPINVITE01') {
  fireEvent.change(screen.getByLabelText('邀请码（必填）'), { target: { value: inviteCode } });
  fireEvent.blur(screen.getByLabelText('邀请码（必填）'));
  fireEvent.change(screen.getByLabelText('邮箱'), { target: { value: 'newcomer@example.com' } });
  fireEvent.change(screen.getByLabelText('密码'), { target: { value: 'hunter2hunter2' } });
}

/** 走到第二步（验证码页）。 */
async function goToCodeStep(container: HTMLElement) {
  fillForm();
  await waitFor(() => expect(screen.getByText('这个邀请码可以用。')).toBeTruthy());
  submit(container);
  await waitFor(() => expect(screen.getByLabelText('6 位验证码')).toBeTruthy());
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

describe('RegisterPage', () => {
  it('邀请码太短时**一个请求都不发** —— 与后端 plausibleInviteCode 同一条闸', () => {
    const calls = stubApi({});
    renderRegister();

    fireEvent.change(screen.getByLabelText('邀请码（必填）'), { target: { value: 'AB' } });
    fireEvent.blur(screen.getByLabelText('邀请码（必填）'));

    // 后端对 4–32 之外的码根本不查库，发过去只是白白消耗 per-IP 30/min 的限额。
    expect(calls).toHaveLength(0);
    expect(screen.queryByText('正在校验邀请码…')).toBeNull();
  });

  it('邀请码已用尽 → 说「再要一个新的」，**不说「无效」**', async () => {
    stubApi({
      [VERIFY]: () =>
        jsonResponse(200, { data: { valid: false, state: 'exhausted' }, meta: { request_id: '01K2X' } }),
    });
    const { container } = renderRegister();

    fillForm();

    await waitFor(() => expect(screen.getByText(/这个码已经被用掉了/)).toBeTruthy());
    expect(container.textContent).toContain('找邀请你的人再要一个新的');
    // 「已用尽」被写成「无效」的话，用户会去核对一个根本没抄错的码。
    expect(container.textContent).not.toContain('这个邀请码无效');
  });

  it('邀请码无效 → 说「无效」，且拦住下一步（不浪费一次发码配额）', async () => {
    const calls = stubApi({
      [VERIFY]: () =>
        jsonResponse(200, { data: { valid: false, state: 'invalid' }, meta: { request_id: '01K2Y' } }),
    });
    const { container } = renderRegister();

    fillForm();
    await waitFor(() => expect(screen.getByText(/这个邀请码无效/)).toBeTruthy());

    submit(container);
    // 只有那一次 verify，没有发码请求 —— 后端明确说了不行的码不该再往下走。
    expect(calls.map((c) => c.path)).toEqual([VERIFY]);
  });

  it('预检打不通（501）时**不拦**提交 —— 预检失败不等于码不对', async () => {
    const calls = stubApi({
      [VERIFY]: () => jsonResponse(501, errorBody('NOT_IMPLEMENTED', '尚未实现')),
      [EMAIL_CODE]: () => noContent(),
    });
    const { container } = renderRegister();

    fillForm();
    await waitFor(() => expect(screen.getByText(/没能提前校验这个码/)).toBeTruthy());
    expect(container.textContent).toContain('该功能尚未开放');

    submit(container);
    await waitFor(() => expect(screen.getByLabelText('6 位验证码')).toBeTruthy());
    expect(calls.map((c) => c.path)).toEqual([VERIFY, EMAIL_CODE]);
  });

  it('全链路成功：verify → 发码 → 注册，顺序不能反；成功页必须有白名单引导', async () => {
    const calls = stubApi({
      [VERIFY]: () =>
        jsonResponse(200, { data: { valid: true, state: 'ok' }, meta: { request_id: '01K2Z' } }),
      [EMAIL_CODE]: () => noContent(),
      [REGISTER]: () => jsonResponse(201, SESSION_TOKENS),
      [ME]: () => jsonResponse(200, CURRENT_USER),
    });
    const { container } = renderRegister();

    await goToCodeStep(container);
    fireEvent.change(screen.getByLabelText('6 位验证码'), { target: { value: '123456' } });
    submit(container);

    await waitFor(() => expect(screen.getByText('注册完成')).toBeTruthy());

    // ① 顺序：验证码通过后才建账号并核销邀请码。
    expect(calls.map((c) => c.path)).toEqual([VERIFY, EMAIL_CODE, REGISTER, ME]);
    // ② 发码用的 scene 是契约里的 register（DB 那边叫别的名字，翻译是后端的事）。
    expect(calls[1]?.body).toEqual({ email: 'newcomer@example.com', scene: 'register' });
    // ③ 邀请码归一化成大写再发 —— 与 handler/auth.go 的 normalizeInviteCode 对齐。
    expect(calls[2]?.body).toMatchObject({ invite_code: 'BPINVITE01', email_code: '123456' });
    // ④ 201 直接带回会话，接住它，否则成功页那个「进入面板」会被守卫弹回登录页。
    expect(window.sessionStorage.getItem(ACCESS_TOKEN_KEY)).toBe('fresh');
    // ⑤ 🔴 白名单引导（roadmap §5.2 2.C 第 3 条）。
    expect(container.textContent).toContain('设置域名白名单');
    expect(container.textContent).toContain('反垃圾');
  });

  it('邮箱已注册（409 STATE_CONFLICT）→ 引导去登录，**不当成填写错误报**', async () => {
    stubApi({
      [VERIFY]: () =>
        jsonResponse(200, { data: { valid: true, state: 'ok' }, meta: { request_id: '01K2Z' } }),
      [EMAIL_CODE]: () => noContent(),
      [REGISTER]: () => jsonResponse(409, errorBody('STATE_CONFLICT', '该邮箱已注册')),
    });
    const { container } = renderRegister();

    await goToCodeStep(container);
    fireEvent.change(screen.getByLabelText('6 位验证码'), { target: { value: '123456' } });
    submit(container);

    await waitFor(() => expect(screen.getByText('这个邮箱已经注册过了')).toBeTruthy());
    expect(screen.getByRole('link', { name: '去登录' })).toBeTruthy();
    expect(screen.getByRole('link', { name: '忘记密码了' })).toBeTruthy();
    // 「填写有误」会让人回去改邮箱密码 —— 而他要做的是去登录。
    expect(container.textContent).not.toContain('填写有误');
  });

  it('422 的原因读 details[].reason —— 邀请码在最后一步被用尽也要说清楚', async () => {
    stubApi({
      [VERIFY]: () =>
        jsonResponse(200, { data: { valid: true, state: 'ok' }, meta: { request_id: '01K2Z' } }),
      [EMAIL_CODE]: () => noContent(),
      // 预检通过、提交时被别人抢先用掉：后端回 422 + invite_code_exhausted。
      // code 只有一个 VALIDATION_FAILED，区分只在 details 里。
      [REGISTER]: () =>
        jsonResponse(
          422,
          errorBody('VALIDATION_FAILED', '邀请码已被使用', [
            { field: 'invite_code', reason: 'invite_code_exhausted' },
          ]),
        ),
    });
    const { container } = renderRegister();

    await goToCodeStep(container);
    fireEvent.change(screen.getByLabelText('6 位验证码'), { target: { value: '123456' } });
    submit(container);

    await waitFor(() => expect(screen.getByText('这个邀请码已经被用掉了')).toBeTruthy());
    expect(container.textContent).toContain('找邀请你的人再要一个新的');
    expect(container.textContent).not.toContain('填写有误');
  });

  it('422 email_code_expired → 说「过期了，重新发一封」，不与「码不对」混为一谈', async () => {
    stubApi({
      [VERIFY]: () =>
        jsonResponse(200, { data: { valid: true, state: 'ok' }, meta: { request_id: '01K2Z' } }),
      [EMAIL_CODE]: () => noContent(),
      [REGISTER]: () =>
        jsonResponse(
          422,
          errorBody('VALIDATION_FAILED', '验证码已过期，请重新获取', [
            { field: 'email_code', reason: 'email_code_expired' },
          ]),
        ),
    });
    const { container } = renderRegister();

    await goToCodeStep(container);
    fireEvent.change(screen.getByLabelText('6 位验证码'), { target: { value: '123456' } });
    submit(container);

    await waitFor(() => expect(screen.getByText('验证码已经过期')).toBeTruthy());
    expect(container.textContent).toContain('重新发一封');
  });

  it('发码 429 → 倒计时秒数来自 Retry-After，且不进入验证码步骤', async () => {
    stubApi({
      [VERIFY]: () =>
        jsonResponse(200, { data: { valid: true, state: 'ok' }, meta: { request_id: '01K2Z' } }),
      [EMAIL_CODE]: () =>
        jsonResponse(429, errorBody('QUOTA_RATE_LIMITED', '获取验证码过于频繁'), {
          'Retry-After': '42',
        }),
    });
    const { container } = renderRegister();

    fillForm();
    await waitFor(() => expect(screen.getByText('这个邀请码可以用。')).toBeTruthy());
    submit(container);

    await waitFor(() => expect(screen.getByText('42 秒后可再试')).toBeTruthy());
    expect(screen.getByRole('alert').textContent).toContain('42 秒后可以再试');
    // 码没发出去就跳到验证码页，用户会对着一封不存在的邮件干等。
    expect(screen.queryByLabelText('6 位验证码')).toBeNull();
  });

  it('注册端点 501 → 「该功能尚未开放」，不是「未知错误」', async () => {
    stubApi({
      [VERIFY]: () =>
        jsonResponse(200, { data: { valid: true, state: 'ok' }, meta: { request_id: '01K2Z' } }),
      [EMAIL_CODE]: () => noContent(),
      [REGISTER]: () => jsonResponse(501, errorBody('NOT_IMPLEMENTED', '尚未实现')),
    });
    const { container } = renderRegister();

    await goToCodeStep(container);
    fireEvent.change(screen.getByLabelText('6 位验证码'), { target: { value: '123456' } });
    submit(container);

    await waitFor(() => expect(screen.getByText('该功能尚未开放')).toBeTruthy());
    expect(container.textContent).toContain('重试没有用');
    expect(window.sessionStorage.getItem(ACCESS_TOKEN_KEY)).toBeNull();
  });
});

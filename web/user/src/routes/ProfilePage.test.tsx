/**
 * `/profile` 的接线测试。
 *
 * 🔴 **这个文件存在的首要理由是第三个用例。**
 * 后端在**原密码不正确**时返回的是 **401 + `AUTH_INVALID_CREDENTIALS`**
 * （`handler/auth.go` 的 `ChangePassword`），而 401 在前端归一成 `kind = 'unauthorized'`，
 * 兜底文案是「需要重新登录，登录状态已过期」。也就是说：
 * **任何一次「顺手改成按 HTTP 状态码分支」的重构，都会把「你打错了原密码」
 * 显示成「你被登出了」** —— 用户会去重新登录一次，回来再输错一次，然后开工单。
 * 这条退化不会有任何报错、不会白屏，只会表现为工单里多一类「密码改不了」。
 * 用例同时断言 **token 还在 sessionStorage 里**：`lib/api.ts` 的 `handleAuthFailure`
 * 为这个 code 留了 early return，那条 early return 一旦被删掉，会话会被误清。
 *
 * 其余用例各守一条：
 *  - 通知偏好的三态与 501（这一页有独立请求，不能被改密码表单的状态吞掉）；
 *  - `service_broadcast` 必须渲染成**不可关闭**且带服务端给的 `reason`
 *    —— 生命线不能被用户关掉（user-journey §1 裁决 4），
 *    而「前端把它藏起来」是不够的：藏起来之后没人会记得它为什么不能改；
 *  - 二次确认（「其它设备会退出登录」那个勾选框）没勾上时按钮必须是禁用的。
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, screen, waitFor } from '@testing-library/react';
import { ACCESS_TOKEN_KEY } from '../lib/session.ts';
import ProfilePage from './ProfilePage.tsx';
import {
  fail,
  jsonResponse,
  meRoute,
  notImplemented,
  ok,
  renderSignedIn,
  resetAll,
  stubFetch,
} from './account-test-utils.tsx';

const PREFS = {
  expire_remind: true,
  traffic_remind: false,
  service_broadcast: {
    value: true,
    locked: true,
    reason: '服务通告包含域名变更等无法通过其他渠道送达的信息，因此不提供关闭选项。',
  },
};

const PREFS_PATH = '/api/v1/user/notification-prefs';
const PASSWORD_PATH = '/api/v1/user/password';

/** 填完三个密码框并勾上二次确认。返回 `<form>`，调用方自己决定提不提交。 */
function fillPasswordForm(container: HTMLElement, oldPassword = 'oldpassword'): HTMLFormElement {
  fireEvent.change(screen.getByLabelText('原密码'), { target: { value: oldPassword } });
  fireEvent.change(screen.getByLabelText('新密码'), { target: { value: 'brandnewpassword' } });
  fireEvent.change(screen.getByLabelText('确认新密码'), { target: { value: 'brandnewpassword' } });
  const form = container.querySelector('form');
  if (!form) throw new Error('改密码表单不存在');
  return form;
}

function acknowledge(): void {
  fireEvent.click(screen.getByRole('checkbox', { name: /其它设备/ }));
}

beforeEach(resetAll);
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe('ProfilePage', () => {
  it('成功：通知偏好加载出来，两个开关按服务端的值渲染', async () => {
    stubFetch({
      '/api/v1/user/me': meRoute(),
      [PREFS_PATH]: () => jsonResponse(200, ok(PREFS)),
    });
    renderSignedIn(<ProfilePage />);

    const expire = await screen.findByRole('checkbox', { name: /到期提醒/ });
    expect((expire as HTMLInputElement).checked).toBe(true);
    const traffic = screen.getByRole('checkbox', { name: /流量提醒/ });
    expect((traffic as HTMLInputElement).checked).toBe(false);
  });

  it('🔴 service_broadcast 渲染成不可关闭，并把服务端给的原因显示出来', async () => {
    stubFetch({
      '/api/v1/user/me': meRoute(),
      [PREFS_PATH]: () => jsonResponse(200, ok(PREFS)),
    });
    renderSignedIn(<ProfilePage />);

    const locked = await screen.findByRole('checkbox', { name: '服务通告（不可关闭）' });
    expect((locked as HTMLInputElement).checked).toBe(true);
    expect((locked as HTMLInputElement).disabled).toBe(true);
    // 原因来自服务端的 `LockedBoolean.reason`，**前端不自己编一个**。
    expect(screen.getByText(PREFS.service_broadcast.reason)).toBeTruthy();
    // 并且这条生命线的说明必须常驻，不只是挂在开关上。
    expect(screen.getByText(/不受这些开关控制/)).toBeTruthy();
  });

  it('🔴 401 AUTH_INVALID_CREDENTIALS（原密码错）→ 说原密码不对，**不说要重新登录**，且会话不被清掉', async () => {
    stubFetch({
      '/api/v1/user/me': meRoute(),
      [PREFS_PATH]: () => jsonResponse(200, ok(PREFS)),
      [PASSWORD_PATH]: () => fail(401, 'AUTH_INVALID_CREDENTIALS', '原密码不正确'),
    });
    const { container } = renderSignedIn(<ProfilePage />);
    await screen.findByRole('checkbox', { name: /到期提醒/ });

    const form = fillPasswordForm(container);
    acknowledge();
    fireEvent.submit(form);

    await waitFor(() => expect(screen.getByRole('alert')).toBeTruthy());
    const alert = screen.getByRole('alert');
    expect(alert.textContent).toContain('原密码不正确');
    // 这两条是这个用例的全部意义：401 不等于「会话没了」。
    expect(alert.textContent).not.toContain('需要重新登录');
    expect(alert.textContent).not.toContain('登录状态已过期');
    expect(window.sessionStorage.getItem(ACCESS_TOKEN_KEY)).toBe('test-token');
  });

  it('改密码成功（204）→ 提前说过的「其它设备要重新登录」在成功后再确认一次，输入框清空', async () => {
    const seen: Array<string | null> = [];
    stubFetch({
      '/api/v1/user/me': meRoute(),
      [PREFS_PATH]: () => jsonResponse(200, ok(PREFS)),
      [PASSWORD_PATH]: ({ body }) => {
        seen.push(body);
        return jsonResponse(204, null);
      },
    });
    const { container } = renderSignedIn(<ProfilePage />);
    await screen.findByRole('checkbox', { name: /到期提醒/ });

    const form = fillPasswordForm(container);
    acknowledge();
    fireEvent.submit(form);

    await waitFor(() => expect(screen.getByRole('status')).toBeTruthy());
    expect(screen.getByRole('status').textContent).toContain('其它设备已被登出');
    // 契约的字段名是 `old_password` / `new_password`，不是 camelCase。
    expect(seen[0]).toContain('"old_password"');
    expect(seen[0]).toContain('"new_password"');
    // 明文密码不留在 DOM 里。
    expect((screen.getByLabelText('原密码') as HTMLInputElement).value).toBe('');
  });

  it('二次确认没勾上时，提交按钮是禁用的', async () => {
    stubFetch({
      '/api/v1/user/me': meRoute(),
      [PREFS_PATH]: () => jsonResponse(200, ok(PREFS)),
      // 没登记 PASSWORD_PATH：真发出去了这个替身会抛，用例会红。
    });
    const { container } = renderSignedIn(<ProfilePage />);
    await screen.findByRole('checkbox', { name: /到期提醒/ });

    fillPasswordForm(container);
    const submit = screen.getByRole('button', { name: '修改密码' });
    expect((submit as HTMLButtonElement).disabled).toBe(true);

    acknowledge();
    expect((screen.getByRole('button', { name: '修改密码' }) as HTMLButtonElement).disabled).toBe(false);
  });

  it('通知偏好 501 → 「该功能尚未开放」，而不是「我们这边出了问题」', async () => {
    stubFetch({
      '/api/v1/user/me': meRoute(),
      [PREFS_PATH]: () => notImplemented(),
    });
    renderSignedIn(<ProfilePage />);

    await waitFor(() => expect(screen.getByText('该功能尚未开放')).toBeTruthy());
    // 501 归一成 kind='server'，走 5xx 文案就会把人推去状态页 —— 那上面什么都没有。
    expect(screen.queryByText(/查看状态页/)).toBeNull();
    // 而改密码表单**照样在**：一个区块的三态不许吞掉另一个区块。
    expect(screen.getByLabelText('原密码')).toBeTruthy();
  });

  it('通知偏好 5xx → 显示可重试的错误态，改密码表单不受影响', async () => {
    stubFetch({
      '/api/v1/user/me': meRoute(),
      [PREFS_PATH]: () => fail(500, 'INTERNAL_ERROR', '服务器开小差'),
    });
    renderSignedIn(<ProfilePage />);

    await waitFor(() => expect(screen.getByRole('button', { name: '重试' })).toBeTruthy());
    expect(screen.getByLabelText('原密码')).toBeTruthy();
  });

  it('单个开关保存失败时，只报错，不把开关的显示值改掉', async () => {
    stubFetch({
      '/api/v1/user/me': meRoute(),
      [PREFS_PATH]: ({ method }) =>
        method === 'PUT'
          ? fail(500, 'INTERNAL_ERROR', '存不进去')
          : jsonResponse(200, ok(PREFS)),
    });
    renderSignedIn(<ProfilePage />);

    const traffic = await screen.findByRole('checkbox', { name: /流量提醒/ });
    expect((traffic as HTMLInputElement).checked).toBe(false);
    fireEvent.click(traffic);

    await waitFor(() => expect(screen.getByRole('alert')).toBeTruthy());
    expect(screen.getByRole('alert').textContent).toContain('开关仍是原来的值');
    // 没有乐观更新，所以失败后不需要「拨回来」—— 值从头到尾没动过。
    expect((screen.getByRole('checkbox', { name: /流量提醒/ }) as HTMLInputElement).checked).toBe(false);
  });
});

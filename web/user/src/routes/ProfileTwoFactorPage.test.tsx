/**
 * `/profile/2fa` 的接线测试。
 *
 * 这一页的产品要求是「**显示尚未开放**」，不是「做一个看起来能用的绑定界面」——
 * 三个端点（`enrollUserTotp` / `verifyUserTotp` / `disableUserTotp`）是**刻意的 501**，
 * 数据库里连存密钥的列都没有（`db/queries/account.sql` 的 TOTP 一节明写「本节没有任何查询」）。
 * 所以这里钉的三条都是「不要假装它能用」：
 *
 *  1. **挂载时一个请求都不发。** `enrollUserTotp` 是 `POST` —— 后端真做出来之后，
 *     自动探测会让「每打开一次这一页就重新生成一次 secret」，把用户已经绑好的验证器作废。
 *     这条退化没有任何报错，只会表现为「验证器突然对不上了」。
 *  2. **501 说「尚未开放」，不说「我们这边出了问题」。** 501 归一成 `kind = 'server'`，
 *     不单独分一支就会把人推去状态页 —— 而状态页上什么都不会有，因为根本没有故障。
 *  3. 🔴 **探测意外成功时，secret 一个字都不许出现在页面上。**
 *     二维码要求本地生成（绝不调在线二维码服务 —— 那等于把 TOTP 密钥发给第三方），
 *     而仓库里还没有本地渲染件，也就是说绑定流程根本走不完。
 *     此时把 secret 印出来，用户既完成不了绑定，密钥还白白暴露了一次。
 *     这一条是最容易被「顺手补全」的：下一个人看到响应里有 `secret`，
 *     很自然会想「显示出来让用户手动输入嘛」。
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, screen, waitFor } from '@testing-library/react';
import ProfileTwoFactorPage from './ProfileTwoFactorPage.tsx';
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

const ENROLL_PATH = '/api/v1/user/2fa/enroll';
const SECRET = 'JBSWY3DPEHPK3PXP';

beforeEach(resetAll);
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe('ProfileTwoFactorPage', () => {
  it('首屏就说「尚未开放」，并且**没有对 2FA 端点发过任何请求**', async () => {
    const spy = stubFetch({
      '/api/v1/user/me': meRoute(),
      // 刻意不登记 ENROLL_PATH：挂载时真发出去了，替身会抛，用例红。
    });
    renderSignedIn(<ProfileTwoFactorPage />);

    await screen.findByText('该功能尚未开放');
    const paths = spy.mock.calls.map((call) => new URL(String(call[0])).pathname);
    expect(paths).not.toContain(ENROLL_PATH);
    // 「开始设置」这种让人以为能用的按钮不该存在。
    expect(screen.queryByRole('button', { name: '开始设置' })).toBeNull();
  });

  it('主动检查 → 501 → 「尚未开放」，且不推去状态页', async () => {
    stubFetch({
      '/api/v1/user/me': meRoute(),
      [ENROLL_PATH]: () => notImplemented(),
    });
    renderSignedIn(<ProfileTwoFactorPage />);

    fireEvent.click(await screen.findByRole('button', { name: '检查是否已开放' }));

    await waitFor(() => expect(screen.getByText(/服务端回答：尚未开放/)).toBeTruthy());
    expect(screen.queryByText(/查看状态页/)).toBeNull();
    expect(screen.queryByText(/我们这边出了问题/)).toBeNull();
  });

  it('🔴 探测意外成功 → secret **不出现在页面上**，只说前端流程没做完', async () => {
    stubFetch({
      '/api/v1/user/me': meRoute(),
      [ENROLL_PATH]: () =>
        jsonResponse(
          200,
          ok({ secret: SECRET, otpauth_url: `otpauth://totp/babelplus?secret=${SECRET}` }),
        ),
    });
    const { container } = renderSignedIn(<ProfileTwoFactorPage />);

    fireEvent.click(await screen.findByRole('button', { name: '检查是否已开放' }));

    await waitFor(() =>
      expect(screen.getByText(/后端已经开放了，但这一页的绑定流程还没做完/)).toBeTruthy(),
    );
    // 密钥与 otpauth URL 一个字都不许落到 DOM 上。
    expect(container.textContent).not.toContain(SECRET);
    expect(container.textContent).not.toContain('otpauth://');
    // 并且要说清楚差的是**本地**二维码渲染，不是「随便找个二维码服务」。
    expect(screen.getByText(/本地二维码渲染/)).toBeTruthy();
  });

  it('探测撞上 5xx → 按错误码说话，不显示成「尚未开放」', async () => {
    stubFetch({
      '/api/v1/user/me': meRoute(),
      [ENROLL_PATH]: () => fail(500, 'INTERNAL_ERROR', '崩了'),
    });
    renderSignedIn(<ProfileTwoFactorPage />);

    fireEvent.click(await screen.findByRole('button', { name: '检查是否已开放' }));

    await waitFor(() => expect(screen.getByRole('alert')).toBeTruthy());
    expect(screen.getByRole('alert').textContent).toContain('我们这边出了问题');
    expect(screen.queryByText(/服务端回答：尚未开放/)).toBeNull();
  });

  it('封禁账号（401 AUTH_PERMISSION_DENIED）→ 说封禁，不说「登录过期」', async () => {
    stubFetch({
      '/api/v1/user/me': meRoute(),
      [ENROLL_PATH]: () => fail(401, 'AUTH_PERMISSION_DENIED', '账号已被封禁'),
    });
    renderSignedIn(<ProfileTwoFactorPage />);

    fireEvent.click(await screen.findByRole('button', { name: '检查是否已开放' }));

    await waitFor(() => expect(screen.getByRole('alert')).toBeTruthy());
    expect(screen.getByRole('alert').textContent).toContain('这个账号已被封禁');
    expect(screen.getByRole('alert').textContent).not.toContain('登录状态已过期');
  });
});

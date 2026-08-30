/**
 * `/invite` 的接线测试。
 *
 * 🔴 **首要理由是第一个用例：返佣是一次性定额，不是「订单金额的 10%」。**
 * 定价修订 C6 推翻了原来的按单计法 —— 原口径把 24 格里的 **4 格**打穿 1.20× 毛利地板
 * （最差 1.1474×）。而「10%」这个数字在旧文档（page-inventory §3.2.9 的表里还写着
 * 「**佣金 10%**」）与所有人的记忆里都还在，所以把文案改回比例形态是**极可能发生**的。
 * 改回去不会有任何报错：用户只是按错误的预期算收益，然后在年付订单上发现少了一大截。
 * 用例因此是双向的：正面断言三个定额出现，反面断言「订单金额」这类比例说法不出现。
 *
 * 🔴 第二条是 `transferCommission` 的 **503 vs 500**。
 * 缺 `expense:commission` 科目时后端返 503 + `INTERNAL_DEPENDENCY_DOWN`
 * （`handler/wallet.go` 里一处刻意的契约偏差，openapi 只声明了 401/409/422/500）。
 * 500 是「偶发故障，重试可能好」，503 是「这个功能整个不可用，重试多少次都一样」——
 * 后端那边缺的是一支**还没跑的 migration**。混成一句话，用户会对着一个
 * 永远不会成功的按钮反复点，然后开工单。
 *
 * 第三条是 `QUOTA_RATE_LIMITED` 的**一码两义**：403 时它是「名额用完」，
 * 429 时它是限流。判据取 `Retry-After` 在不在（api-contract §2.7：429 与 503 必带），
 * 而不是回头去看状态码。
 *
 * 其余用例守：生成资格挂在「有有效订阅」上、二次确认、空态、501。
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, screen, waitFor } from '@testing-library/react';
import InvitePage from './InvitePage.tsx';
import {
  CURRENT_USER_NO_SUB,
  fail,
  jsonResponse,
  meRoute,
  notImplemented,
  ok,
  okPage,
  renderSignedIn,
  resetAll,
  stubFetch,
  type StubHandler,
} from './account-test-utils.tsx';

const CODES_PATH = '/api/v1/user/invite/codes';
const WALLET_PATH = '/api/v1/user/wallet';
const COMMISSIONS_PATH = '/api/v1/user/commissions';
const TRANSFER_PATH = '/api/v1/user/commissions/transfer';

const CODES = [
  {
    id: 1,
    code: 'BPINVITE01',
    status: 'ok',
    invite_url: 'https://panel.example/register?invite=BPINVITE01',
    used_count: 0,
    use_limit: 1,
    created_at: '2026-08-20T00:00:00Z',
  },
  {
    id: 2,
    code: 'BPINVITE02',
    status: 'exhausted',
    used_count: 1,
    use_limit: 1,
    created_at: '2026-08-10T00:00:00Z',
  },
];

const WALLET = {
  balance_amount: 0,
  commission_pending_amount: 720,
  commission_available_amount: 1590,
};

const COMMISSIONS = [
  {
    id: 11,
    order_trade_no: 'BP20260830ABC',
    amount: 1590,
    status: 'confirmed',
    created_at: '2026-08-20T00:00:00Z',
    confirmed_at: '2026-08-27T00:00:00Z',
  },
];

function baseRoutes(overrides: Record<string, StubHandler> = {}) {
  return stubFetch({
    '/api/v1/user/me': meRoute(),
    [CODES_PATH]: () => jsonResponse(200, ok(CODES)),
    [WALLET_PATH]: () => jsonResponse(200, ok(WALLET)),
    [COMMISSIONS_PATH]: () => jsonResponse(200, okPage(COMMISSIONS)),
    ...overrides,
  });
}

/** 走完两段式确认：先点主按钮，再点确认按钮。 */
async function confirmClick(label: RegExp, confirmLabel: RegExp): Promise<void> {
  fireEvent.click(await screen.findByRole('button', { name: label }));
  fireEvent.click(await screen.findByRole('button', { name: confirmLabel }));
}

beforeEach(resetAll);
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe('InvitePage', () => {
  it('🔴 返佣写成**一次性定额**（¥7.20 / ¥15.90 / ¥35.80），不是「订单金额的 10%」', async () => {
    baseRoutes();
    const { container } = renderSignedIn(<InvitePage />);

    // 三个定额都要出现。用 getAllByText：¥7.20 / ¥15.90 同时是「确认中 / 可划转」
    // 那两行的金额，这是**刻意的重合**（fixture 用的就是真实档位金额），不是重复渲染。
    await waitFor(() => expect(screen.getAllByText('¥7.20').length).toBeGreaterThan(0));
    expect(screen.getAllByText('¥15.90').length).toBeGreaterThan(0);
    expect(screen.getByText('¥35.80')).toBeTruthy();
    // 口径这句话必须在。
    expect(screen.getByText(/一次性定额/)).toBeTruthy();
    expect(screen.getByText(/与他买的周期长短无关/)).toBeTruthy();

    // 🔴 反面：任何「按订单金额算比例」的说法都不许出现。
    const text = container.textContent ?? '';
    expect(text).not.toContain('订单金额的 10%');
    expect(text).not.toContain('订单金额 10%');
    expect(text).not.toMatch(/订单金额的?\s*10\s*%/);
  });

  it('🔴 划转撞上 503 INTERNAL_DEPENDENCY_DOWN → 说「暂时不可用」，**不说「我们这边出了问题」**', async () => {
    baseRoutes({
      [TRANSFER_PATH]: () =>
        fail(503, 'INTERNAL_DEPENDENCY_DOWN', '佣金划转暂不可用，请稍后再试', {
          headers: { 'Retry-After': '300' },
        }),
    });
    renderSignedIn(<InvitePage />);

    await confirmClick(/把 ¥15\.90 划转到余额/, /确认划转/);

    await waitFor(() => expect(screen.getByRole('alert')).toBeTruthy());
    const alert = screen.getByRole('alert');
    expect(alert.textContent).toContain('暂时不可用，请稍后再试');
    // 500 的文案不许出现在 503 上 —— 两者对用户的含义完全不同。
    expect(alert.textContent).not.toContain('我们这边出了问题');
    // Retry-After 必须被读出来（api-contract §2.7：503 必带），并且不许自己编秒数。
    expect(alert.textContent).toContain('300 秒');
  });

  it('划转撞上 500 → 走「我们这边出了问题」，与 503 分得开', async () => {
    baseRoutes({ [TRANSFER_PATH]: () => fail(500, 'INTERNAL_ERROR', '崩了') });
    renderSignedIn(<InvitePage />);

    await confirmClick(/把 ¥15\.90 划转到余额/, /确认划转/);

    await waitFor(() => expect(screen.getByRole('alert')).toBeTruthy());
    expect(screen.getByRole('alert').textContent).toContain('我们这边出了问题');
    expect(screen.getByRole('alert').textContent).not.toContain('暂时不可用');
  });

  it('划转成功 → 用响应里的 Wallet 就地覆盖，**不重新拉一次 getWallet**', async () => {
    let walletCalls = 0;
    let sentBody: string | null = null;
    baseRoutes({
      [WALLET_PATH]: () => {
        walletCalls += 1;
        return jsonResponse(200, ok(WALLET));
      },
      // 请求体从 handler 里直接拿（`stubFetch` 已经把 ArrayBuffer 解回字符串），
      // 确认发出去的是「全部可划转」而不是某个自由金额。
      [TRANSFER_PATH]: ({ body }) => {
        sentBody = body;
        return jsonResponse(
          200,
          ok({ ...WALLET, commission_available_amount: 0, balance_amount: 1590 }),
        );
      },
    });

    renderSignedIn(<InvitePage />);
    await confirmClick(/把 ¥15\.90 划转到余额/, /确认划转/);

    await waitFor(() => expect(screen.getByRole('status')).toBeTruthy());
    expect(screen.getByRole('status').textContent).toContain('已把 ¥15.90 划转到余额');
    // 金额只能是「全部可划转」：后端要求 amount 等于一个前缀和，自由金额会撞 422。
    expect(sentBody).toContain('"amount":1590');
    // 划转前拉过一次，之后**不再拉** —— 重拉的两次之间有并发消费时，
    // 用户会看到「划转之后余额反而变少了」。
    expect(walletCalls).toBe(1);
  });

  it('🔴 没有有效订阅 → 生成按钮禁用并说明原因，一个请求都不发', async () => {
    // 这一条不能用 baseRoutes：闸门的输入是 `/me` 里的订阅摘要，得换一个没有订阅的账号。
    stubFetch({
      '/api/v1/user/me': meRoute(CURRENT_USER_NO_SUB),
      // 只登记 GET 的那一条路由：`createInviteCode` 走的是同一个 path 的 POST，
      // 而这个 handler 对 POST 也会返 200 —— 所以下面还要断言确认框没出现过。
      [CODES_PATH]: () => jsonResponse(200, ok([])),
      [WALLET_PATH]: () => jsonResponse(200, ok(WALLET)),
      [COMMISSIONS_PATH]: () => jsonResponse(200, okPage([])),
    });

    renderSignedIn(<InvitePage />);

    const button = await screen.findByRole('button', { name: '生成邀请码' });
    expect((button as HTMLButtonElement).disabled).toBe(true);
    expect(screen.getByText(/需要有一份生效中的订阅/)).toBeTruthy();
    fireEvent.click(button);
    // 禁用按钮点了也不该发请求；顺便确认没有跳过闸门的路径。
    expect(screen.queryByRole('button', { name: /确认生成/ })).toBeNull();
  });

  it('生成邀请码要走二次确认：第一次点击**不发请求**', async () => {
    let created = 0;
    baseRoutes({
      [CODES_PATH]: ({ method }) => {
        if (method === 'POST') {
          created += 1;
          return jsonResponse(201, ok({ ...CODES[0], id: 9, code: 'BPINVITE09' }));
        }
        return jsonResponse(200, ok(CODES));
      },
    });
    renderSignedIn(<InvitePage />);

    fireEvent.click(await screen.findByRole('button', { name: '生成邀请码' }));
    expect(created).toBe(0);
    expect(screen.getByText(/无法撤销/)).toBeTruthy();

    fireEvent.click(screen.getByRole('button', { name: '确认生成一个' }));
    await waitFor(() => expect(created).toBe(1));
    expect(screen.getByRole('status').textContent).toContain('BPINVITE09');
  });

  it('🔴 403 QUOTA_RATE_LIMITED（无 Retry-After）→ 说「名额用完」，不说「太频繁」', async () => {
    baseRoutes({
      [CODES_PATH]: ({ method }) =>
        method === 'POST'
          ? fail(403, 'QUOTA_RATE_LIMITED', '未使用的邀请码最多 3 条')
          : jsonResponse(200, ok(CODES)),
    });
    renderSignedIn(<InvitePage />);

    await confirmClick(/^生成邀请码$/, /确认生成一个/);

    await waitFor(() => expect(screen.getByRole('alert')).toBeTruthy());
    expect(screen.getByRole('alert').textContent).toContain('未核销的邀请码已达上限');
    expect(screen.getByRole('alert').textContent).not.toContain('太频繁');
  });

  it('🔴 429 QUOTA_RATE_LIMITED（带 Retry-After）→ 说限流并倒计时，与「名额用完」分得开', async () => {
    baseRoutes({
      [CODES_PATH]: ({ method }) =>
        method === 'POST'
          ? fail(429, 'QUOTA_RATE_LIMITED', '太频繁', { headers: { 'Retry-After': '45' } })
          : jsonResponse(200, ok(CODES)),
    });
    renderSignedIn(<InvitePage />);

    await confirmClick(/^生成邀请码$/, /确认生成一个/);

    await waitFor(() => expect(screen.getByRole('alert')).toBeTruthy());
    expect(screen.getByRole('alert').textContent).toContain('45 秒后可以再试');
    expect(screen.getByRole('alert').textContent).not.toContain('名额');
    expect(screen.getByText('45 秒后可再试')).toBeTruthy();
  });

  it('没有邀请码 → 空态给下一步动作，而佣金那一块照常渲染', async () => {
    baseRoutes({ [CODES_PATH]: () => jsonResponse(200, ok([])) });
    renderSignedIn(<InvitePage />);

    await waitFor(() => expect(screen.getByText('还没有邀请码')).toBeTruthy());
    expect(screen.getByText(/这是邀请制不退化成开放注册的那道闸/)).toBeTruthy();
    expect(screen.getAllByText('¥7.20').length).toBeGreaterThan(0);
  });

  it('邀请码列表 501 → 「该功能尚未开放」，佣金那一块不受影响', async () => {
    baseRoutes({ [CODES_PATH]: () => notImplemented() });
    renderSignedIn(<InvitePage />);

    await waitFor(() => expect(screen.getByText('该功能尚未开放')).toBeTruthy());
    expect(screen.queryByText(/查看状态页/)).toBeNull();
    expect(screen.getAllByText('可划转').length).toBeGreaterThan(0);
  });

  it('佣金明细为空 → 空态说清「每人只发一次」', async () => {
    baseRoutes({ [COMMISSIONS_PATH]: () => jsonResponse(200, okPage([])) });
    renderSignedIn(<InvitePage />);

    await waitFor(() => expect(screen.getByText('还没有佣金记录')).toBeTruthy());
    expect(screen.getAllByText(/每位被邀请人只发一次/).length).toBeGreaterThan(0);
  });
});

/**
 * `/order/:trade_no`（USDT 收银台）的接线测试。
 *
 * **这一页最容易被顺手改错的那条规则**，第 2 个用例做的是**负向断言**：
 * page-inventory §3.2.5 与 user-journey §7 卡点 5 里那套「精确到分位的唯一金额 /
 * 四位小数尾数是订单识别码」的话术，**已被 ADR 0012 §5.4 整节删除** ——
 * 归属改成一单一址、只看地址不看金额，报价取整到 0.01 USDT。
 * 照旧文案填写金额对交易所提币是**反的**（提币费从填写金额里扣），按它填必然 underpaid。
 * 而这两份文档至今还写着旧话术，任何人照文档「补全」这一页都会把它写回去，
 * 且不会有任何东西报错 —— 只会表现为 underpaid 工单变多。所以这条规则只能靠断言守。
 * 同一条规则的正面：解释文字直接渲染 API 的 `PaymentCheckout.note`，前端不自己写一份。
 *
 * 其余用例覆盖：两个读请求的三态**互相独立**（收银台 501 不许把金额构成一起吞掉）、
 * `underpaid` 的显式界面（不是「支付失败」）、404 走空态、
 * 「我已付款帮我查一下」**永远可见**（连收银台整段读失败时也在），以及
 * `payOrder` 的二次确认 + 幂等键。最后一组是轮询三条规则的纯函数断言。
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { MemoryRouter, Route, Routes } from 'react-router';
import { resetRuntimeConfig } from '@babelplus/shared';
import { AuthProvider } from '../lib/auth.tsx';
import { resetApiClientForTests } from '../lib/api.ts';
import { ACCESS_TOKEN_KEY, resetSessionForTests } from '../lib/session.ts';
import OrderDetailPage from './OrderDetailPage.tsx';
import { POLL_BASE_MS, POLL_MAX_MS, nextPollDelayMs, shouldPoll } from './billing/format.ts';

interface Call {
  path: string;
  method: string;
  headers: Headers;
  body: string;
}

const calls: Call[] = [];
const TRADE_NO = 'BP20260830000001';

function jsonResponse(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

const META = { request_id: '01K2PAYPAYPAYPAYPAYPAYPAYP' };

const CURRENT_USER = {
  data: {
    id: 1,
    email: 'user@example.com',
    banned: false,
    created_at: '2026-08-23T00:00:00Z',
    balance_amount: 0,
    subscription: {
      total_bytes: 0,
      upload_bytes: 0,
      download_bytes: 0,
      device_limit: 0,
      device_count: 0,
    },
  },
  meta: META,
};

const ORDER = {
  trade_no: TRADE_NO,
  type: 'new',
  status: 'processing',
  currency: 'CNY',
  plan_name: '标准',
  period: 'monthly',
  total_amount: 40_000,
  discount_amount: 2000,
  payable_amount: 38_000,
  created_at: '2026-08-30T02:00:00Z',
};

/**
 * 后端 `paymentCheckoutNote` 的实际文案（ADR 0012 §6.4）。
 * 测试里原样带上它，是为了断言页面**渲染的是 API 给的这一份**，不是前端自己写的一份。
 */
const NOTE =
  '若你从交易所提币，请在提币金额里填「上面这个数 + 你的提币手续费」——' +
  '手续费是从你填的金额里扣的，不是另外加收。这个地址永远认账。';

function checkout(overrides: Record<string, unknown> = {}) {
  return {
    trade_no: TRADE_NO,
    chain: 'TRC20',
    address: 'TQ5oo1BpJ7cQ4kZ3nR9uVwXyZaBcDeFgHi',
    amount_usdt6: 55_600_000,
    amount_display: '55.60',
    cny_per_usdt_e4: 71_500,
    quote_expires_at: new Date(Date.now() + 20 * 60_000).toISOString(),
    confirmations_required: 19,
    received_usdt6: 0,
    state: 'waiting',
    note: NOTE,
    ...overrides,
  };
}

function renderDetail() {
  return render(
    <MemoryRouter initialEntries={[`/order/${TRADE_NO}`]}>
      <AuthProvider>
        <Routes>
          <Route path="/order/:trade_no" element={<OrderDetailPage />} />
        </Routes>
      </AuthProvider>
    </MemoryRouter>,
  );
}

function stubApi(routes: Record<string, (call: Call) => Response>) {
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: string | URL | Request, init?: RequestInit) => {
      const url = new URL(String(input));
      const call: Call = {
        path: url.pathname,
        method: (init?.method ?? 'GET').toUpperCase(),
        headers: new Headers(init?.headers as HeadersInit | undefined),
        body:
          init?.body instanceof ArrayBuffer ? new TextDecoder().decode(init.body) : String(init?.body ?? ''),
      };
      calls.push(call);
      if (call.path === '/api/v1/user/me') return jsonResponse(200, CURRENT_USER);
      const handler = routes[`${call.method} ${call.path}`] ?? routes[call.path];
      if (!handler) throw new Error(`未预期的请求：${call.method} ${call.path}`);
      return handler(call);
    }),
  );
}

const ok = (data: unknown) => () => jsonResponse(200, { data, meta: META });

beforeEach(() => {
  calls.length = 0;
  resetRuntimeConfig();
  resetSessionForTests();
  resetApiClientForTests();
  window.sessionStorage.clear();
  window.sessionStorage.setItem(ACCESS_TOKEN_KEY, 'token-alive');
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe('OrderDetailPage', () => {
  it('成功：金额构成与收银台各自渲染，数字全部来自 API', async () => {
    stubApi({
      [`/api/v1/orders/${TRADE_NO}`]: ok(ORDER),
      [`/api/v1/orders/${TRADE_NO}/payment`]: ok(checkout()),
    });
    renderDetail();

    // 金额是「分」的 int64 —— 40000 分 = ¥400.00。
    await waitFor(() => expect(screen.getByText('¥400.00')).toBeTruthy());
    expect(screen.getByText('−¥20.00')).toBeTruthy();
    expect(screen.getByText('¥380.00')).toBeTruthy();

    // 收款地址与到账金额原样来自 API（`amount_display` 已经是取整到 0.01 USDT 的两位小数串）。
    expect(screen.getByText('TQ5oo1BpJ7cQ4kZ3nR9uVwXyZaBcDeFgHi')).toBeTruthy();
    expect(screen.getByText('55.60')).toBeTruthy();
    expect(screen.getByText('TRC20')).toBeTruthy();
  });

  it('🔴 收银台文案来自 API 的 note，且不出现被 ADR 0012 §5.4 删掉的「尾数 / 识别码」话术', async () => {
    stubApi({
      [`/api/v1/orders/${TRADE_NO}`]: ok(ORDER),
      [`/api/v1/orders/${TRADE_NO}/payment`]: ok(checkout()),
    });
    const { container } = renderDetail();

    await waitFor(() => expect(screen.getByText(NOTE)).toBeTruthy());

    // 旧方案（小地址池 + 金额尾数唯一性匹配）的每一个词。照文档「补全」这一页就会写回来。
    for (const dead of ['尾数', '识别码', '四位小数', '小数点后四位', '一分不差']) {
      expect(container.textContent).not.toContain(dead);
    }
    // 新方案最该被用户看见的那句（ADR 0012 §11.2）反过来必须在。
    expect(container.textContent).toContain('永远认账');
  });

  it('🔴 underpaid 显示「已收到 X / 还差 Y」，绝不显示成「支付失败」', async () => {
    stubApi({
      [`/api/v1/orders/${TRADE_NO}`]: ok(ORDER),
      [`/api/v1/orders/${TRADE_NO}/payment`]: ok(
        checkout({ state: 'underpaid', received_usdt6: 54_100_000, shortfall_usdt6: 1_500_000 }),
      ),
    });
    const { container } = renderDetail();

    await waitFor(() => expect(screen.getByText('已收到')).toBeTruthy());
    expect(screen.getByText('54.10 USDT')).toBeTruthy();
    expect(screen.getByText('还差')).toBeTruthy();
    expect(screen.getByText('1.50 USDT')).toBeTruthy();

    // 少付的头号成因是交易所提币费从转出额里扣 —— 那不是「失败」，钱已经在我们手上了。
    expect(container.textContent).not.toContain('支付失败');

    // ⚠️ 三档阈值（≤2 / 2–5 / >5 USDT）只在服务端 settings 里，契约一个字段都没下发。
    //    所以页面**不许**出现写死的阈值数字 —— 运营改一次阈值它就开始骗人。
    expect(container.textContent).not.toMatch(/2(\.0)?\s*USDT\s*以内/);
  });

  it('🔴 收银台 501 不影响金额构成（两个读请求各自一套三态）', async () => {
    stubApi({
      [`/api/v1/orders/${TRADE_NO}`]: ok(ORDER),
      [`/api/v1/orders/${TRADE_NO}/payment`]: () =>
        jsonResponse(501, { error: { code: 'NOT_IMPLEMENTED', message: '尚未实现' }, meta: META }),
    });
    renderDetail();

    await waitFor(() => expect(screen.getByText('该功能尚未开放')).toBeTruthy());
    // 金额构成照常显示 —— 整页一个 loading / 一个 error 的写法会把它一起吞掉。
    expect(screen.getByText('¥380.00')).toBeTruthy();
    expect(screen.queryByText('我们这边出了问题')).toBeNull();
  });

  it('订单 404 → 「找不到这个订单」的空态，不是红色错误框', async () => {
    stubApi({
      [`/api/v1/orders/${TRADE_NO}`]: () =>
        jsonResponse(404, { error: { code: 'RESOURCE_NOT_FOUND', message: '订单不存在' }, meta: META }),
      [`/api/v1/orders/${TRADE_NO}/payment`]: () =>
        jsonResponse(404, { error: { code: 'RESOURCE_NOT_FOUND', message: '订单不存在' }, meta: META }),
    });
    renderDetail();

    await waitFor(() => expect(screen.getByText('找不到这个订单')).toBeTruthy());
    expect(screen.getByRole('link', { name: /回到订单列表/ })).toBeTruthy();
  });

  it('🔴 「我已付款，帮我查一下」在收银台整段读失败时**依然在**，并真的打 recheck', async () => {
    stubApi({
      [`/api/v1/orders/${TRADE_NO}`]: ok(ORDER),
      [`GET /api/v1/orders/${TRADE_NO}/payment`]: () =>
        jsonResponse(500, { error: { code: 'INTERNAL_ERROR', message: '服务器开小差' }, meta: META }),
      [`POST /api/v1/orders/${TRADE_NO}/recheck`]: ok(checkout({ state: 'paid' })),
    });
    renderDetail();

    // ADR 0012 §10.4：「按钮必须在页面上永远可见，不能只在检测到异常时才出现 ——
    // 因为『检测到异常』这件事本身就是我们做不到才需要这个按钮」。
    const button = await screen.findByRole('button', { name: '我已付款，帮我查一下' });
    fireEvent.click(button);

    await waitFor(() =>
      expect(
        calls.some((c) => c.method === 'POST' && c.path.endsWith('/recheck')),
      ).toBe(true),
    );
  });

  it('还没发起支付时：payOrder 要二次确认，且带 Idempotency-Key', async () => {
    stubApi({
      [`/api/v1/orders/${TRADE_NO}`]: ok({ ...ORDER, status: 'pending' }),
      [`GET /api/v1/orders/${TRADE_NO}/payment`]: ok({
        trade_no: TRADE_NO,
        state: 'waiting',
        received_usdt6: 0,
      }),
      [`POST /api/v1/orders/${TRADE_NO}/pay`]: ok(checkout()),
    });
    renderDetail();

    fireEvent.click(await screen.findByRole('button', { name: /去付款/ }));

    // 确认框出现，但还没发 POST。
    expect(screen.getByRole('button', { name: '确认，给我一个收款地址' })).toBeTruthy();
    expect(calls.filter((c) => c.method === 'POST')).toHaveLength(0);

    fireEvent.click(screen.getByRole('button', { name: '确认，给我一个收款地址' }));

    await waitFor(() => expect(screen.getByText('TQ5oo1BpJ7cQ4kZ3nR9uVwXyZaBcDeFgHi')).toBeTruthy());
    const pay = calls.find((c) => c.method === 'POST' && c.path.endsWith('/pay'));
    expect(pay?.headers.get('Idempotency-Key')).toBeTruthy();
    expect(JSON.parse(pay?.body ?? '{}')).toMatchObject({ method: 'usdt_trc20' });
  });
});

/**
 * 轮询的三条规则。
 *
 * 直接测纯函数而不是去操纵定时器：这三条坏掉的表现（用户切到交易所 App 转账，
 * 这一页在后台每隔几秒打一次 API）**没有任何人会报 bug**，
 * 而假定时器 + React 的组合本身就够脆，用它来守一条永远不会有人报的规则不划算。
 * 判据被抽成纯函数正是为了能在这里被直接钉住。
 */
describe('收银台轮询的判据', () => {
  it('页面隐藏时不轮询（用户正切到交易所 App 转账，那是他最需要电的时候）', () => {
    expect(shouldPoll('waiting', true)).toBe(false);
    expect(shouldPoll('confirming', true)).toBe(false);
  });

  it('只轮询非终态：paid / expired 之后再轮询是纯粹的浪费', () => {
    expect(shouldPoll('waiting', false)).toBe(true);
    expect(shouldPoll('confirming', false)).toBe(true);
    expect(shouldPoll('underpaid', false)).toBe(true);
    expect(shouldPoll('paid', false)).toBe(false);
    expect(shouldPoll('expired', false)).toBe(false);
    expect(shouldPoll(undefined, false)).toBe(false);
  });

  it('指数退避且封顶：链上确认是分钟级的，1 秒轮询一次收益都没有', () => {
    expect(POLL_BASE_MS).toBeGreaterThanOrEqual(5000);
    expect(nextPollDelayMs(POLL_BASE_MS)).toBe(POLL_BASE_MS * 2);
    expect(nextPollDelayMs(POLL_MAX_MS)).toBe(POLL_MAX_MS);
    expect(nextPollDelayMs(POLL_MAX_MS * 4)).toBe(POLL_MAX_MS);
  });
});

/**
 * `/plan` 的接线测试。
 *
 * **这一页最容易被顺手改错的那条规则**，第 2 与第 3 个用例正面钉住它：
 * page-inventory §3.2.4 的表格里写着「月 / 季（**9 折**）/ 半年（**85 折**）/ 年（**75 折**）」，
 * 那是**产品意图不是事实源** —— pricing-and-plans §7 的定价至今是 P0 阻塞项。
 * 把这三个数写进折扣角标，运营改一次价它们就变成「页面写着 9 折、结算按新价扣」，
 * 而这种错**不会报任何错**：它只表现为客服开始收到「你们的折扣是假的」。
 * 所以：折扣角标必须由 API 返回的价格现算，价格里没有折扣时**一个「折」字都不许出现**。
 *
 * 其余用例覆盖：成功、空态、501、写操作的二次确认 + 幂等键、以及按 `ErrorCode`（不是状态码）
 * 分支 —— 后端对被封禁账号返回的是 **401 + `AUTH_PERMISSION_DENIED`**，
 * 按状态码分支会把封禁显示成「登录过期」，用户于是反复重登并开工单。
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { MemoryRouter, Route, Routes, useLocation } from 'react-router';
import { resetRuntimeConfig } from '@babelplus/shared';
import { AuthProvider } from '../lib/auth.tsx';
import { resetApiClientForTests } from '../lib/api.ts';
import { ACCESS_TOKEN_KEY, resetSessionForTests } from '../lib/session.ts';
import PlanPage from './PlanPage.tsx';

interface Call {
  path: string;
  method: string;
  headers: Headers;
  body: string;
}

const calls: Call[] = [];

function jsonResponse(status: number, body: unknown, headers: Record<string, string> = {}): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json', ...headers },
  });
}

const META = { request_id: '01K2PLANPLANPLANPLANPLANPL' };

const CURRENT_USER = {
  data: {
    id: 1,
    email: 'user@example.com',
    banned: false,
    created_at: '2026-08-23T00:00:00Z',
    balance_amount: 12_345,
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

/**
 * 一个**长周期完全没有折扣**的套餐：季付正好 = 3×月付、年付正好 = 12×月付。
 * 竞品就是这样定价的（competitor-conyss §3.3 实测订单流水），所以这不是一个假想输入。
 */
const PLAN_NO_DISCOUNT = {
  id: 1,
  name: '标准',
  type: 'period',
  description: '2 台设备',
  currency: 'CNY',
  transfer_enable_bytes: 107_374_182_400,
  device_limit: 2,
  prices: [
    { period: 'monthly', amount: 3000 },
    { period: 'quarterly', amount: 9000 },
    { period: 'yearly', amount: 36_000 },
  ],
};

/** 同一个套餐，但季付真的打了 9 折（8100 / 9000）。用来证明角标不是被写死成「不显示」。 */
const PLAN_WITH_DISCOUNT = {
  ...PLAN_NO_DISCOUNT,
  prices: [
    { period: 'monthly', amount: 3000 },
    { period: 'quarterly', amount: 8100 },
  ],
};

const PLAN_PACK = {
  id: 2,
  name: '50G 流量包',
  type: 'traffic_pack',
  currency: 'CNY',
  transfer_enable_bytes: 53_687_091_200,
  device_limit: 2,
  prices: [{ period: 'onetime', amount: 1200 }],
};

function LandingProbe() {
  const location = useLocation();
  return <div data-testid="landed">{location.pathname}</div>;
}

function renderPlanPage() {
  return render(
    <MemoryRouter initialEntries={['/plan']}>
      <AuthProvider>
        <Routes>
          <Route path="/plan" element={<PlanPage />} />
          <Route path="/order/:trade_no" element={<LandingProbe />} />
        </Routes>
      </AuthProvider>
    </MemoryRouter>,
  );
}

/** `/api/v1/plans` 与写端点各自可控；`/user/me` 一律成功（登录态不是这一页要测的东西）。 */
function stubApi(routes: Record<string, (call: Call) => Response>) {
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: string | URL | Request, init?: RequestInit) => {
      const path = new URL(String(input)).pathname;
      const call: Call = {
        path,
        method: (init?.method ?? 'GET').toUpperCase(),
        headers: new Headers(init?.headers as HeadersInit | undefined),
        body:
          init?.body instanceof ArrayBuffer ? new TextDecoder().decode(init.body) : String(init?.body ?? ''),
      };
      calls.push(call);
      if (path === '/api/v1/user/me') return jsonResponse(200, CURRENT_USER);
      const handler = routes[path];
      if (!handler) throw new Error(`未预期的请求：${call.method} ${path}`);
      return handler(call);
    }),
  );
}

function plansOk(plans: unknown[]) {
  return () => jsonResponse(200, { data: plans, meta: META });
}

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

describe('PlanPage', () => {
  it('成功：套餐卡的每一个数字都来自 API（价格、流量、设备数）', async () => {
    stubApi({ '/api/v1/plans': plansOk([PLAN_NO_DISCOUNT, PLAN_PACK]) });
    renderPlanPage();

    await waitFor(() => expect(screen.getByText('标准')).toBeTruthy());
    // 「¥30.00 起」= API 里最便宜的那一档，不是任何写死的入门价。
    expect(screen.getByText('¥30.00 起')).toBeTruthy();
    expect(screen.getByText('100.00 GB')).toBeTruthy();
    // 设备数是 §3.2.4 唯一点名要加粗的字段（我们相对竞品的差异化杠杆）。
    expect(screen.getByText('2 台')).toBeTruthy();

    // 诚实声明与卖点同等字号、不放折叠区，且**不依赖任何请求**。
    expect(screen.getByText('我们不承诺流媒体解锁。')).toBeTruthy();
  });

  it('🔴 长周期没有折扣时，页面上一个「折」字都不出现（不许写死 9 折 / 85 折 / 75 折）', async () => {
    stubApi({ '/api/v1/plans': plansOk([PLAN_NO_DISCOUNT]) });
    const { container } = renderPlanPage();

    await waitFor(() => expect(screen.getByText('标准')).toBeTruthy());
    fireEvent.click(screen.getByRole('button', { name: /标准/ }));
    await waitFor(() => expect(screen.getByRole('button', { name: /季付/ })).toBeTruthy());

    // 三个周期的价格照 API 显示……
    expect(screen.getByRole('button', { name: /季付.*¥90\.00/ })).toBeTruthy();
    expect(screen.getByRole('button', { name: /年付.*¥360\.00/ })).toBeTruthy();
    // ……但一个折扣角标都没有。写死 page-inventory 表格里那三个数的话，这两行会红。
    // 用「数字 + 折」而不是光一个「折」字：页面上还有「升级折抵」这种正当用法。
    expect(container.textContent).not.toMatch(/\d+(\.\d+)?\s*折/);
    for (const hardcoded of ['9 折', '8.5 折', '7.5 折']) {
      expect(container.textContent).not.toContain(hardcoded);
    }
  });

  it('🔴 API 的价格里真有折扣时，角标按 API 现算出来（8100/9000 → 9 折）', async () => {
    stubApi({ '/api/v1/plans': plansOk([PLAN_WITH_DISCOUNT]) });
    renderPlanPage();

    await waitFor(() => expect(screen.getByText('标准')).toBeTruthy());
    expect(screen.getByText('最低 9 折')).toBeTruthy();
  });

  it('空态：不是「暂无数据」，给下一步动作', async () => {
    stubApi({ '/api/v1/plans': plansOk([]) });
    renderPlanPage();

    await waitFor(() => expect(screen.getByText('暂时没有开放的套餐')).toBeTruthy());
    expect(screen.getByRole('link', { name: /提交工单/ })).toBeTruthy();
  });

  it('501 NOT_IMPLEMENTED → 「该功能尚未开放」，不是「我们这边出了问题」', async () => {
    stubApi({
      '/api/v1/plans': () =>
        jsonResponse(501, {
          error: { code: 'NOT_IMPLEMENTED', message: '尚未实现' },
          meta: META,
        }),
    });
    renderPlanPage();

    await waitFor(() => expect(screen.getByText('该功能尚未开放')).toBeTruthy());
    // 501 归一成 kind='server'，按 kind 走就会说「我们这边出了问题、去看状态页」——
    // 而状态页上什么都不会有，因为根本没有故障。
    expect(screen.queryByText('我们这边出了问题')).toBeNull();
  });

  it('下单要二次确认：只点「去下单」不发任何 POST；确认后带 Idempotency-Key', async () => {
    stubApi({
      '/api/v1/plans': plansOk([PLAN_NO_DISCOUNT]),
      '/api/v1/orders': () =>
        jsonResponse(201, {
          data: {
            trade_no: 'BP20260830000001',
            type: 'new',
            status: 'pending',
            currency: 'CNY',
            total_amount: 3000,
            payable_amount: 3000,
            created_at: '2026-08-30T00:00:00Z',
          },
          meta: META,
        }),
    });
    renderPlanPage();

    await waitFor(() => expect(screen.getByText('标准')).toBeTruthy());
    fireEvent.click(screen.getByRole('button', { name: /标准/ }));
    fireEvent.click(await screen.findByRole('button', { name: /去下单/ }));

    // 确认框已经出现，但**一个 POST 都还没发**。
    expect(screen.getByRole('button', { name: '确认下单' })).toBeTruthy();
    expect(calls.filter((c) => c.path === '/api/v1/orders')).toHaveLength(0);

    fireEvent.click(screen.getByRole('button', { name: '确认下单' }));

    await waitFor(() => expect(screen.getByTestId('landed')).toBeTruthy());
    expect(screen.getByTestId('landed').textContent).toBe('/order/BP20260830000001');

    const post = calls.find((c) => c.path === '/api/v1/orders' && c.method === 'POST');
    expect(post).toBeTruthy();
    // 幂等键是契约必填的头（`createOrder` 的 parameters.header），不是可选的保险。
    expect(post?.headers.get('Idempotency-Key')).toBeTruthy();
    expect(JSON.parse(post?.body ?? '{}')).toMatchObject({
      plan_id: 1,
      period: 'monthly',
      use_balance: false,
    });
  });

  it('下单失败后重试，用的还是同一把幂等键（换一把就等于允许重复下单）', async () => {
    let attempt = 0;
    stubApi({
      '/api/v1/plans': plansOk([PLAN_NO_DISCOUNT]),
      '/api/v1/orders': () => {
        attempt += 1;
        if (attempt === 1) {
          return jsonResponse(500, {
            error: { code: 'INTERNAL_ERROR', message: '服务器开小差' },
            meta: META,
          });
        }
        return jsonResponse(201, {
          data: {
            trade_no: 'BP20260830000002',
            type: 'new',
            status: 'pending',
            currency: 'CNY',
            total_amount: 3000,
            payable_amount: 3000,
            created_at: '2026-08-30T00:00:00Z',
          },
          meta: META,
        });
      },
    });
    renderPlanPage();

    await waitFor(() => expect(screen.getByText('标准')).toBeTruthy());
    fireEvent.click(screen.getByRole('button', { name: /标准/ }));
    fireEvent.click(await screen.findByRole('button', { name: /去下单/ }));
    fireEvent.click(screen.getByRole('button', { name: '确认下单' }));

    // 失败后确认框**不关**：关掉了用户就不知道自己那一下有没有生效。
    await waitFor(() => expect(screen.getByText('我们这边出了问题')).toBeTruthy());
    fireEvent.click(screen.getByRole('button', { name: '确认下单' }));
    await waitFor(() => expect(screen.getByTestId('landed')).toBeTruthy());

    const posts = calls.filter((c) => c.path === '/api/v1/orders' && c.method === 'POST');
    expect(posts).toHaveLength(2);
    expect(posts[0]?.headers.get('Idempotency-Key')).toBe(posts[1]?.headers.get('Idempotency-Key'));
  });

  it('401 AUTH_PERMISSION_DENIED（封禁）→ 说封禁，不说「登录已过期」', async () => {
    stubApi({
      '/api/v1/plans': plansOk([PLAN_NO_DISCOUNT]),
      '/api/v1/orders': () =>
        jsonResponse(401, {
          error: { code: 'AUTH_PERMISSION_DENIED', message: '账号已被封禁' },
          meta: META,
        }),
    });
    renderPlanPage();

    await waitFor(() => expect(screen.getByText('标准')).toBeTruthy());
    fireEvent.click(screen.getByRole('button', { name: /标准/ }));
    fireEvent.click(await screen.findByRole('button', { name: /去下单/ }));
    fireEvent.click(screen.getByRole('button', { name: '确认下单' }));

    await waitFor(() => expect(screen.getByText('这个账号已被封禁')).toBeTruthy());
    expect(screen.getByRole('alert').textContent).not.toContain('登录状态已过期');
  });

  it('优惠码不可用 → 原样显示服务端给的原因，且不挡下单', async () => {
    stubApi({
      '/api/v1/plans': plansOk([PLAN_NO_DISCOUNT]),
      '/api/v1/coupons/verify': () =>
        jsonResponse(200, {
          data: { code: 'FIRST50', valid: false, reason: '该优惠码仅限首次下单使用' },
          meta: META,
        }),
    });
    renderPlanPage();

    await waitFor(() => expect(screen.getByText('标准')).toBeTruthy());
    fireEvent.click(screen.getByRole('button', { name: /标准/ }));

    fireEvent.change(await screen.findByLabelText('优惠码（可选）'), {
      target: { value: 'FIRST50' },
    });
    fireEvent.click(screen.getByRole('button', { name: '校验优惠码' }));

    // 服务端的判定顺序（券本身 → 这个人 → 这个订单）是裁决过的产品行为，
    // 前端归纳成一句「优惠码不可用」会把「你不是新用户」和「这张券过期了」混成一件事。
    await waitFor(() => expect(screen.getByText('该优惠码仅限首次下单使用')).toBeTruthy());
    // 校验失败不影响下单按钮 —— 不带码照样能下。
    expect(screen.getByRole('button', { name: /去下单/ })).toHaveProperty('disabled', false);
  });
});

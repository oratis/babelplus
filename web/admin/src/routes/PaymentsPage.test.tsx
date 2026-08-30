// @vitest-environment jsdom

/**
 * `/admin/payments` 的接线测试。
 *
 * 这一页有一条**反直觉**的规则，第 2 个用例正面钉住它：
 * 服务端算「累计实收」时**排除**被 AML 拉黑的到账（入账路径不认这笔钱，
 * 对账面也不能替它认）。于是队列里会出现「有一笔钱到了，但这张单还差全款」
 * 这种看起来矛盾的行 —— 那不是显示错误，正是最需要人处理的那一类。
 * 前端顺手把它「修正」掉（过滤、或者自己拿这一行的金额去补上实收）
 * 会让它从对账页上消失，而且不会有任何报错。
 *
 * 另一条：D13 改支付流水状态**不会推进订单、也不会开通权益**。
 * 服务端为此专门打了一条 ERROR 日志；界面这一侧必须把话说在前面，
 * 否则操作者会标完 `paid` 然后等一个永远不会发生的开通。
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { MemoryRouter, Route, Routes } from 'react-router';
import { resetRuntimeConfig } from '@babelplus/shared';
import { resetAdminApiForTests } from '../lib/api.ts';
import PaymentsPage from './PaymentsPage.tsx';

const META = { request_id: '01K2PAYPAYPAYPAYPAYPAYPA' };
const UNDERPAID_PATH = '/api/v1/admin/payments/underpaid';

interface Call {
  path: string;
  method: string;
  body: string | null;
}

const calls: Call[] = [];

function json(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });
}

function errorEnvelope(status: number, code: string, message: string): Response {
  return json(status, { error: { code, message }, meta: META });
}

/** 见 `OrderDetailPage.test.tsx` 的同名函数：传输层把 body 读成过 ArrayBuffer。 */
function decodeBody(body: BodyInit | null | undefined): string | null {
  if (body === null || body === undefined) return null;
  if (typeof body === 'string') return body;
  if (body instanceof ArrayBuffer) return new TextDecoder().decode(body);
  if (ArrayBuffer.isView(body)) return new TextDecoder().decode(body as Uint8Array);
  return null;
}

function stubFetch(handler: (call: Call) => Response) {
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const raw = typeof input === 'string' ? input : input instanceof URL ? input.href : input.url;
      const url = new URL(raw, 'http://localhost');
      const call: Call = {
        path: url.pathname,
        method: init?.method ?? 'GET',
        body: decodeBody(init?.body),
      };
      calls.push(call);
      return handler(call);
    }),
  );
}

function payment(patch: Record<string, unknown> = {}) {
  return {
    id: 1,
    provider: 'tron',
    external_id: 'abc:0',
    trade_no: 'BP20260830000001',
    state: 'underpaid',
    expected_usdt6: 5_842_300,
    received_usdt6: 5_000_000,
    shortfall_usdt6: 842_300,
    txid: 'f'.repeat(64),
    created_at: '2026-08-30T02:00:00Z',
    ...patch,
  };
}

function page(data: unknown[], meta: Record<string, unknown> = {}) {
  return { data, meta: { ...META, has_more: false, ...meta } };
}

/** 两条列表各发一次请求，所以 stub 必须按路径分流。 */
function routes(underpaid: () => Response, payments: () => Response) {
  return (call: Call) => (call.path === UNDERPAID_PATH ? underpaid() : payments());
}

function renderPage() {
  return render(
    <MemoryRouter initialEntries={['/admin/payments']}>
      <Routes>
        <Route path="/admin/payments" element={<PaymentsPage />} />
      </Routes>
    </MemoryRouter>,
  );
}

beforeEach(() => {
  calls.length = 0;
  resetRuntimeConfig();
  resetAdminApiForTests();
  window.sessionStorage.clear();
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe('少付队列', () => {
  it('渲染应付 / 累计实收 / 还差三个数，单位是 USDT 且不经过浮点', async () => {
    stubFetch(routes(() => json(200, page([payment()])), () => json(200, page([]))));
    renderPage();

    // 5842300 * 1e-6 = 5.8423；末位是订单识别码，四位小数必须原样出现。
    expect(await screen.findByText('5.8423 USDT')).toBeTruthy();
    expect(screen.getByText('5.0000 USDT')).toBeTruthy();
    expect(screen.getByText('0.8423 USDT')).toBeTruthy();
  });

  it('🔴 被拉黑的到账留在列表里，并标出「一分没计入」', async () => {
    // 这一行确实有一笔到账（它就在 payments 表里），但服务端的累计实收是 0 ——
    // 因为算实收时排除了 aml_verdict = blacklisted 的流水。
    stubFetch(
      routes(
        () => json(200, page([payment({ received_usdt6: 0, shortfall_usdt6: 5_842_300 })])),
        () => json(200, page([])),
      ),
    );
    renderPage();

    expect(await screen.findByText('一分没计入')).toBeTruthy();
    // 行本身没有被过滤掉：订单号还在。
    expect(screen.getAllByText('BP20260830000001').length).toBeGreaterThan(0);
    expect(screen.getByText(/不含被 AML 拉黑的到账/)).toBeTruthy();
  });

  it('空态说明这是队列的**正常**状态，不是功能没做', async () => {
    stubFetch(routes(() => json(200, page([])), () => json(200, page([]))));
    renderPage();

    expect(await screen.findByText('没有少付的订单')).toBeTruthy();
    expect(screen.getByText(/常驻的对账入口/)).toBeTruthy();
  });

  it('501 显示成「尚未开放」而不是红色故障', async () => {
    stubFetch(
      routes(() => errorEnvelope(501, 'NOT_IMPLEMENTED', '尚未实现'), () => json(200, page([]))),
    );
    renderPage();

    expect(await screen.findByText(/少付队列尚未开放/)).toBeTruthy();
  });

  it('三条出路里只有「全额退回」有端点，界面上如实说出来', async () => {
    stubFetch(routes(() => json(200, page([])), () => json(200, page([]))));
    renderPage();

    await screen.findByText('没有少付的订单');
    expect(screen.getByText(/按实收金额部分开通/)).toBeTruthy();
    expect(screen.getByText(/要求用户补款/)).toBeTruthy();
    expect(screen.getByText(/这三条都不是「手工标记已支付」/)).toBeTruthy();
  });
});

describe('支付流水', () => {
  it('未归属的钱显示成「未归属」而不是空白', async () => {
    stubFetch(
      routes(
        () => json(200, page([])),
        () => json(200, page([payment({ id: 9, trade_no: undefined, state: 'paid' })])),
      ),
    );
    renderPage();

    expect(await screen.findByText('未归属')).toBeTruthy();
  });
});

describe('D13 · 改支付流水状态', () => {
  async function openEditor() {
    stubFetch((call) => {
      if (call.path === UNDERPAID_PATH) return json(200, page([]));
      if (call.method === 'PATCH') return json(200, { data: payment({ state: 'paid' }), meta: META });
      return json(200, page([payment({ id: 5, state: 'confirming' })]));
    });
    renderPage();
    fireEvent.click(await screen.findByRole('button', { name: '改状态' }));
    fireEvent.click(await screen.findByRole('button', { name: '改流水 #5 的状态' }));
  }

  it('把「这一步不会推进订单、也不会开通权益」写在面板里', async () => {
    await openEditor();

    expect(screen.getByText(/改这一行不会推进订单，也不会开通任何权益/)).toBeTruthy();
    expect(screen.getByText(/要补单请走/)).toBeTruthy();
  });

  it('展示 diff：state 的改前值 → 改后值', async () => {
    await openEditor();

    fireEvent.change(screen.getByLabelText('新状态'), { target: { value: 'paid' } });
    // 改前值必须**仍然看得见** —— D13 的登记要求是「展示 diff」，不是「显示新值」。
    // 只显示新值的话，操作者无从判断自己是不是正在把一个已经对的状态改坏。
    const diff = screen.getByText(/^state:/);
    expect(diff.textContent).toContain('confirming');
    expect(diff.textContent).toContain('paid');
    expect(diff.textContent).toContain('→');
  });

  it('🔴 参数没收齐时不许提交：状态没变、或原因不足 8 码位，按钮都点不动', async () => {
    await openEditor();

    const submit = () => screen.getByRole('button', { name: '保存新状态' }) as HTMLButtonElement;

    // ① 状态没改：没有要改的东西。
    expect(submit().disabled).toBe(true);
    expect(screen.getByTestId('danger-blocked-hint').textContent).toContain('新状态与当前状态相同');

    // ② 改了状态，但原因还没填够（L2 由服务端强制，这里只是省一次往返）。
    fireEvent.change(screen.getByLabelText('新状态'), { target: { value: 'paid' } });
    expect(submit().disabled).toBe(true);
    fireEvent.change(screen.getByLabelText('操作原因（必填）'), { target: { value: '改一下' } });
    expect(submit().disabled).toBe(true);

    // ③ 收齐了才亮。
    fireEvent.change(screen.getByLabelText('操作原因（必填）'), {
      target: { value: '链上确认数已足够，网关状态回写失败，人工对齐' },
    });
    expect(submit().disabled).toBe(false);
  });

  it('提交时 PATCH 到这一条流水，body 带 reason 与 state', async () => {
    await openEditor();

    fireEvent.change(screen.getByLabelText('新状态'), { target: { value: 'paid' } });
    fireEvent.change(screen.getByLabelText('操作原因（必填）'), {
      target: { value: '链上确认数已足够，网关状态回写失败，人工对齐' },
    });
    fireEvent.click(screen.getByRole('button', { name: '保存新状态' }));

    await waitFor(() => expect(calls.some((c) => c.method === 'PATCH')).toBe(true));
    const patch = calls.find((c) => c.method === 'PATCH');
    expect(patch?.path).toBe('/api/v1/admin/payments/5');
    const body = JSON.parse(patch?.body ?? '{}') as Record<string, unknown>;
    expect(body.state).toBe('paid');
    expect(body.reason).toBe('链上确认数已足够，网关状态回写失败，人工对齐');
    // 没填备注时不发 note —— 空串会在审计的 after 快照里留下一个假的「填过备注」。
    expect('note' in body).toBe(false);
  });

  it('备注要说清它在库里无处可存，只进审计', async () => {
    await openEditor();

    expect(screen.getByText(/备注在库里无处可存/)).toBeTruthy();
  });
});

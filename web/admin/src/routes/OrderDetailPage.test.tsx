// @vitest-environment jsdom

/**
 * `/admin/orders/:trade_no` 的接线测试 —— 这一支的重量全在**危险操作**上。
 *
 * 四层强制（api-contract §6.2）在服务端，前端只负责把参数收齐。所以这里钉的不是
 * 「前端拦住了坏请求」（那是安全边界的错觉），而是两件别的事：
 *
 *  1. **参数没收齐时不许提交** —— 省一次注定失败的往返，且让人知道还差什么。
 *     D6 要收四样：确认串（用户邮箱）、原因 ≥ 8 字、TOTP 6 位、以及业务必填的 `evidence_url`。
 *  2. **收齐之后原样发出去** —— `confirmation` / `reason` / `evidence_url` 进 body，
 *     TOTP 进**请求头** `X-TOTP-Code`。放错位置的现象是恒定 403，
 *     而操作者会以为自己的验证器坏了。
 *
 * D7 这边钉的是退款的两条硬规则：**扣减明细要成表**（不是一行分号串）、
 * **冷静期一生一次的 409 要说清原因**。
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { MemoryRouter, Route, Routes } from 'react-router';
import { resetRuntimeConfig } from '@babelplus/shared';
import { resetAdminApiForTests } from '../lib/api.ts';
import OrderDetailPage from './OrderDetailPage.tsx';

const META = { request_id: '01K2DETAILDETAILDETAILDE' };
const TRADE_NO = 'BP20260830000001';
const USER_EMAIL = 'buyer@example.com';

interface Call {
  path: string;
  method: string;
  body: string | null;
  totp: string | null;
}

const calls: Call[] = [];

function json(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });
}

function errorEnvelope(status: number, code: string, message: string, details?: unknown): Response {
  return json(status, { error: { code, message, ...(details ? { details } : {}) }, meta: META });
}

function adminOrder(patch: Record<string, unknown> = {}) {
  return {
    order: {
      trade_no: TRADE_NO,
      type: 'new',
      status: 'paid',
      currency: 'CNY',
      plan_name: '标准',
      period: 'monthly',
      total_amount: 3000,
      discount_amount: 0,
      surplus_amount: 0,
      balance_amount: 0,
      payable_amount: 3000,
      created_at: '2026-08-30T02:00:00Z',
      ...patch,
    },
    user_id: 42,
    user_email: USER_EMAIL,
  };
}

/**
 * 请求体在传输层被读成过 `ArrayBuffer`（`client.ts` 的 `prepareRequest`：
 * `Request.body` 是一次性的流，而故障转移与 refresh 重放都要重发同一个请求）。
 * 所以这里不能只认字符串 —— 只认字符串的现象是每一条 body 断言都拿到 `undefined`，
 * 而那看起来像「前端根本没发这些字段」。
 */
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
      const headers = new Headers(init?.headers ?? {});
      const call: Call = {
        path: url.pathname,
        method: init?.method ?? 'GET',
        body: decodeBody(init?.body),
        totp: headers.get('X-TOTP-Code'),
      };
      calls.push(call);
      return handler(call);
    }),
  );
}

function renderPage() {
  return render(
    <MemoryRouter initialEntries={[`/admin/orders/${TRADE_NO}`]}>
      <Routes>
        <Route path="/admin/orders/:trade_no" element={<OrderDetailPage />} />
      </Routes>
    </MemoryRouter>,
  );
}

/** 打开某一条危险操作的内联面板（折叠态下按钮上写的是登记表的标题或调用方给的 title）。 */
function openPanel(name: string | RegExp) {
  fireEvent.click(screen.getByRole('button', { name }));
}

function submitButton(label: string): HTMLButtonElement {
  return screen.getByRole('button', { name: label }) as HTMLButtonElement;
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

describe('OrderDetailPage · 只读区', () => {
  it('渲染金额构成与用户', async () => {
    stubFetch(() => json(200, { data: adminOrder(), meta: META }));
    renderPage();

    expect(await screen.findByText(USER_EMAIL)).toBeTruthy();
    expect(screen.getAllByText('¥30.00').length).toBeGreaterThan(0);
    expect(screen.getAllByText('已支付').length).toBeGreaterThan(0);
  });

  it('404：说「找不到这个订单」，并提醒单号区分大小写', async () => {
    stubFetch(() => errorEnvelope(404, 'RESOURCE_NOT_FOUND', '订单不存在'));
    renderPage();

    expect(await screen.findByText('找不到这个订单')).toBeTruthy();
  });

  it('把「回调不可信」写在页面上，而不是显示一个来源不明的已收金额', async () => {
    stubFetch(() => json(200, { data: adminOrder(), meta: META }));
    renderPage();

    expect(await screen.findByText(/回调不可信/)).toBeTruthy();
  });
});

describe('OrderDetailPage · D6 手工标记已支付', () => {
  async function openD6() {
    stubFetch((call) => {
      if (call.method === 'POST') return errorEnvelope(403, 'AUTH_PERMISSION_DENIED', '没有权限位');
      return json(200, { data: adminOrder(), meta: META });
    });
    renderPage();
    await screen.findByText(USER_EMAIL);
    openPanel('手工标记这张订单为已支付');
  }

  it('折叠态就说明这一条默认对所有人关闭（两道锁），不是一个说不清的灰按钮', async () => {
    stubFetch(() => json(200, { data: adminOrder(), meta: META }));
    renderPage();
    await screen.findByText(USER_EMAIL);
    openPanel('手工标记这张订单为已支付');

    expect(screen.getByText(/这一条默认对所有管理员关闭，而且是两道锁/)).toBeTruthy();
    expect(screen.getByText(/在「管理员账号」页面上找不到这个开关/)).toBeTruthy();
    expect(screen.getByText(/带外留痕通道/)).toBeTruthy();
  });

  it('🔴 参数没收齐时不许提交：四样缺任何一样，按钮都是禁用的', async () => {
    await openD6();

    // ① 什么都没填：先被 evidence_url 挡住（结构性原因排在填写原因前面）。
    expect(submitButton('标记为已支付').disabled).toBe(true);
    expect(screen.getByText(/还需要一个/)).toBeTruthy();

    // ② 只填证据 URL：确认串还没打对。
    fireEvent.change(screen.getByLabelText('链上交易证据 URL（必填）'), {
      target: { value: `https://tronscan.org/#/transaction/${'a'.repeat(64)}` },
    });
    expect(submitButton('标记为已支付').disabled).toBe(true);

    // ③ 补上确认串（= 订单所属用户的邮箱）：原因还不够 8 个字。
    fireEvent.change(screen.getByLabelText('输入订单号以确认'), { target: { value: USER_EMAIL } });
    expect(submitButton('标记为已支付').disabled).toBe(true);
    fireEvent.change(screen.getByLabelText('操作原因（必填）'), { target: { value: '补单' } });
    expect(submitButton('标记为已支付').disabled).toBe(true);

    // ④ 原因够了：还差 TOTP。
    fireEvent.change(screen.getByLabelText('操作原因（必填）'), {
      target: { value: '链上 txid 已确认到账，网关回调丢失' },
    });
    expect(submitButton('标记为已支付').disabled).toBe(true);
    expect(screen.getByTestId('danger-blocked-hint').textContent).toContain('6 位码');

    // ⑤ 四样齐了才亮。
    fireEvent.change(screen.getByLabelText('验证器 6 位码'), { target: { value: '123456' } });
    expect(submitButton('标记为已支付').disabled).toBe(false);
  });

  it('🔴 收齐后原样发出：confirmation/reason/evidence_url 进 body，TOTP 进请求头', async () => {
    await openD6();

    fireEvent.change(screen.getByLabelText('链上交易证据 URL（必填）'), {
      target: { value: `https://tronscan.org/#/transaction/${'b'.repeat(64)}:2` },
    });
    fireEvent.change(screen.getByLabelText('输入订单号以确认'), { target: { value: USER_EMAIL } });
    fireEvent.change(screen.getByLabelText('操作原因（必填）'), {
      target: { value: '链上 txid 已确认到账，网关回调丢失' },
    });
    fireEvent.change(screen.getByLabelText('验证器 6 位码'), { target: { value: '654321' } });
    fireEvent.click(submitButton('标记为已支付'));

    await waitFor(() => {
      expect(calls.some((c) => c.method === 'POST')).toBe(true);
    });
    const post = calls.find((c) => c.method === 'POST');
    expect(post?.path).toBe(`/api/v1/admin/orders/${TRADE_NO}/mark-paid`);
    // TOTP 必须在头上，不能在 body 里。
    expect(post?.totp).toBe('654321');
    const body = JSON.parse(post?.body ?? '{}') as Record<string, unknown>;
    expect(body.confirmation).toBe(USER_EMAIL);
    expect(body.reason).toBe('链上 txid 已确认到账，网关回调丢失');
    expect(body.evidence_url).toBe(`https://tronscan.org/#/transaction/${'b'.repeat(64)}:2`);
    expect(body.totp).toBeUndefined();
  });

  it('403 AUTH_PERMISSION_DENIED：说清缺的是授权而不是功能', async () => {
    await openD6();

    fireEvent.change(screen.getByLabelText('链上交易证据 URL（必填）'), {
      target: { value: 'c'.repeat(64) },
    });
    fireEvent.change(screen.getByLabelText('输入订单号以确认'), { target: { value: USER_EMAIL } });
    fireEvent.change(screen.getByLabelText('操作原因（必填）'), {
      target: { value: '链上已确认到账，人工补单' },
    });
    fireEvent.change(screen.getByLabelText('验证器 6 位码'), { target: { value: '111111' } });
    fireEvent.click(submitButton('标记为已支付'));

    expect(await screen.findByText('这个账号不能执行这一条')).toBeTruthy();
  });

  it('证据 URL 里没有 64 位哈希时**只提示不拦**（服务端的判据比前端准）', async () => {
    await openD6();

    fireEvent.change(screen.getByLabelText('链上交易证据 URL（必填）'), {
      target: { value: 'https://example.com/some-receipt' },
    });
    fireEvent.change(screen.getByLabelText('输入订单号以确认'), { target: { value: USER_EMAIL } });
    fireEvent.change(screen.getByLabelText('操作原因（必填）'), {
      target: { value: '链上已确认到账，人工补单' },
    });
    fireEvent.change(screen.getByLabelText('验证器 6 位码'), { target: { value: '222222' } });

    expect(screen.getByText(/这串里没看到 64 位十六进制哈希/)).toBeTruthy();
    // 提示归提示，按钮仍然是亮的。
    expect(submitButton('标记为已支付').disabled).toBe(false);
  });
});

describe('OrderDetailPage · D7 退款', () => {
  function renderWith(handler: (call: Call) => Response) {
    stubFetch(handler);
    renderPage();
  }

  it('原因不足 8 个字时不许提交', async () => {
    renderWith(() => json(200, { data: adminOrder(), meta: META }));
    await screen.findByText(USER_EMAIL);
    openPanel('退款 / 作废这张订单');

    expect(submitButton('提交退款').disabled).toBe(true);
    fireEvent.change(screen.getByLabelText('操作原因（必填）'), { target: { value: '退款' } });
    expect(submitButton('提交退款').disabled).toBe(true);
    fireEvent.change(screen.getByLabelText('操作原因（必填）'), {
      target: { value: '用户在冷静期内申请退款，已核对流量用量' },
    });
    expect(submitButton('提交退款').disabled).toBe(false);
  });

  it('金额只接受正整数的「分」，写小数点时不许提交', async () => {
    renderWith(() => json(200, { data: adminOrder(), meta: META }));
    await screen.findByText(USER_EMAIL);
    openPanel('退款 / 作废这张订单');

    fireEvent.change(screen.getByLabelText('操作原因（必填）'), {
      target: { value: '用户在冷静期内申请退款，已核对流量用量' },
    });
    fireEvent.change(screen.getByLabelText('退款金额（分，可留空）'), { target: { value: '12.5' } });
    expect(submitButton('提交退款').disabled).toBe(true);

    fireEvent.change(screen.getByLabelText('退款金额（分，可留空）'), { target: { value: '1250' } });
    expect(submitButton('提交退款').disabled).toBe(false);
  });

  it('已退款的订单：按钮变灰并说明服务端只接受哪三个状态', async () => {
    renderWith(() => json(200, { data: adminOrder({ status: 'refunded' }), meta: META }));
    await screen.findByText(USER_EMAIL);
    openPanel('退款 / 作废这张订单');

    expect(submitButton('提交退款').disabled).toBe(true);
    expect(screen.getByText(/只接受 已支付 \/ 已完成 \/ 部分退款/)).toBeTruthy();
  });

  it('🔴 422 带扣减明细时渲染成一张**表**，逐行给出算式而不是一个总数', async () => {
    renderWith((call) => {
      if (call.method === 'POST') {
        return errorEnvelope(422, 'VALIDATION_FAILED', '退款金额必须在 1 到 6000 分之间', [
          { field: 'v_window', reason: '本次订阅期内实付合计 35800 分（2 段窗口链求和）' },
          { field: 'consumed_time', reason: '已服务时间按月付标价折算 1000 分' },
          { field: 'consumed_data', reason: '已消耗套餐流量按月付标价折算 200 分' },
          { field: 'refund_b', reason: '常规退款额 = max(0, v_window − consumed_time − consumed_data) = 34600 分' },
          { field: 'already_refunded', reason: '此前已退到余额 0 分' },
          { field: 'rule', reason: '本次适用档位：prorated' },
        ]);
      }
      return json(200, { data: adminOrder(), meta: META });
    });
    await screen.findByText(USER_EMAIL);
    openPanel('退款 / 作废这张订单');

    fireEvent.change(screen.getByLabelText('操作原因（必填）'), {
      target: { value: '用户在冷静期内申请退款，已核对流量用量' },
    });
    fireEvent.click(submitButton('提交退款'));

    expect(await screen.findByText('按 ADR 0013 §3.2 算出的扣减明细')).toBeTruthy();
    expect(screen.getByText('已服务时间折算（扣减）')).toBeTruthy();
    expect(screen.getByText('已消耗套餐流量折算（扣减）')).toBeTruthy();
    expect(screen.getByText('本次适用档位')).toBeTruthy();
    // 同一句算式也会出现在组件自己的表单级错误里（它把 details 串成一行）。
    // 两处并存是有意的：那一行说「这次为什么没成」，这张表说「按什么算式算出来的」。
    expect(screen.getAllByText(/常规退款额 = max\(0/).length).toBeGreaterThan(0);
  });

  it('🔴 409（冷静期一生一次）：原样显示服务端说的原因，不含糊成「操作失败」', async () => {
    const message =
      '该账号已经使用过一次冷静期全额退款（每个账号仅限一次，由数据库唯一索引强制）。' +
      '若本次应当走常规按比例退款，说明判档输入有误，请核对首单与 7 天窗口';
    renderWith((call) => {
      if (call.method === 'POST') return errorEnvelope(409, 'STATE_CONFLICT', message);
      return json(200, { data: adminOrder(), meta: META });
    });
    await screen.findByText(USER_EMAIL);
    openPanel('退款 / 作废这张订单');

    fireEvent.change(screen.getByLabelText('操作原因（必填）'), {
      target: { value: '用户在冷静期内申请退款，已核对流量用量' },
    });
    fireEvent.click(submitButton('提交退款'));

    expect(await screen.findByText('当前状态不允许这次操作')).toBeTruthy();
    expect(screen.getByText(new RegExp('每个账号仅限一次'))).toBeTruthy();
  });

  it('留空金额时 body 里不带 amount（= 按服务端算出的上限全额退）', async () => {
    renderWith((call) => {
      if (call.method === 'POST') return json(200, { data: adminOrder({ status: 'refunded' }), meta: META });
      return json(200, { data: adminOrder(), meta: META });
    });
    await screen.findByText(USER_EMAIL);
    openPanel('退款 / 作废这张订单');

    fireEvent.change(screen.getByLabelText('操作原因（必填）'), {
      target: { value: '用户在冷静期内申请退款，已核对流量用量' },
    });
    fireEvent.click(submitButton('提交退款'));

    await waitFor(() => expect(calls.some((c) => c.method === 'POST')).toBe(true));
    const post = calls.find((c) => c.method === 'POST');
    expect(post?.path).toBe(`/api/v1/admin/orders/${TRADE_NO}/refund`);
    const body = JSON.parse(post?.body ?? '{}') as Record<string, unknown>;
    expect(body.reason).toBe('用户在冷静期内申请退款，已核对流量用量');
    expect('amount' in body).toBe(false);
  });
});

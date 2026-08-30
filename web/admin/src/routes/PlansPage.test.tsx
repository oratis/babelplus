// @vitest-environment jsdom
//
// 这一支需要 DOM：验的是「列表渲染成什么、按钮点不点得动、点下去有没有真的发请求」，
// 而这几条只有把组件挂起来才能回答。包级默认环境是 `node`（`lib/iap.test.ts` 测纯函数），
// 所以用文件级 docblock 单独提高环境。

/**
 * 套餐管理页（模块 4，D8）的测试。
 *
 * ⚠️ **这些用例证明的不是安全性。** §6.2 的四层全部在服务端强制；
 * 这里钉住的只有两件事：**参数没收齐时前端不会把请求发出去**，
 * 以及**收齐之后发出去的那份 body 是对的**（尤其是 `type` —— 它决定了 `plans.kind`）。
 *
 * 走的是真实链路：组件 → `api()` → 传输层 → 被替换掉的全局 `fetch`。
 * 不 mock `api()`，因为「query 参数拼对了没有」「信封拆对了没有」都在那条链路里。
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { resetRuntimeConfig } from '@babelplus/shared';
import PlansPage, { buildPlanDraft } from './PlansPage.tsx';
import { resetAdminApiForTests } from '../lib/api.ts';
import { reportAdminAuthFailure } from '../lib/iap.ts';
import type { Plan } from './catalog-common.tsx';

const REQ = '01K2PLANPLANPLANPLANPLAN';
const GIB = 1024 * 1024 * 1024;

const STANDARD: Plan = {
  id: 2,
  name: '标准版',
  type: 'period',
  currency: 'CNY',
  prices: [
    { period: 'monthly', amount: 7200 },
    { period: 'quarterly', amount: 20400 },
  ],
  transfer_enable_bytes: 200 * GIB,
  device_limit: 5,
  speed_limit_mbps: 0,
  visible: true,
  sort: 10,
};

const PACK: Plan = {
  id: 7,
  name: '加油包 50G',
  type: 'traffic_pack',
  prices: [{ period: 'onetime', amount: 1500 }],
  transfer_enable_bytes: 50 * GIB,
  // 0 = 不限（契约用 0 表达「不限」）。显示成「0 台」是这一列最典型的错法。
  device_limit: 0,
  visible: false,
  sort: 99,
};

/* ────────────────────────── fetch 替身 ────────────────────────── */

interface Call {
  readonly method: string;
  readonly path: string;
  readonly query: URLSearchParams;
  readonly body: unknown;
}

let calls: Call[] = [];

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

function okList(data: readonly Plan[]): Response {
  return json({ data, meta: { request_id: REQ } });
}

function errorEnvelope(status: number, code: string, message: string): Response {
  return json({ error: { code, message }, meta: { request_id: REQ } }, status);
}

/**
 * 每次调用都造一个**新的** `Response`：body 是一次性的流，
 * 而客户端在 401/403 上会 `clone()` 再读，复用同一个对象会在第二次读时炸掉。
 */
function stubFetch(handler: (call: Call) => Response) {
  const spy = vi.fn(async (input: unknown, init?: RequestInit) => {
    const url = new URL(String(input));
    const method = (init?.method ?? 'GET').toUpperCase();
    let body: unknown;
    const raw = init?.body;
    if (raw !== undefined && raw !== null) {
      const text =
        typeof raw === 'string' ? raw : new TextDecoder().decode(raw as ArrayBuffer);
      try {
        body = JSON.parse(text);
      } catch {
        body = text;
      }
    }
    const call: Call = { method, path: url.pathname, query: url.searchParams, body };
    calls.push(call);
    return handler(call);
  });
  vi.stubGlobal('fetch', spy);
  return spy;
}

beforeEach(() => {
  calls = [];
  resetRuntimeConfig();
  resetAdminApiForTests();
  reportAdminAuthFailure(null);
  window.sessionStorage.clear();
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

/* ────────────────────────── 纯函数：表单校验 ────────────────────────── */

interface DraftInput {
  name: string;
  type: 'period' | 'traffic_pack' | null;
  description: string;
  prices: Record<'monthly' | 'quarterly' | 'half_yearly' | 'yearly' | 'onetime', string>;
  transferGib: string;
  deviceLimit: string;
  speedLimit: string;
  visible: boolean;
  sort: string;
}

function form(overrides: Partial<DraftInput> = {}): DraftInput {
  return {
    name: '标准版',
    type: 'period',
    description: '',
    prices: { monthly: '7200', quarterly: '', half_yearly: '', yearly: '', onetime: '' },
    transferGib: '200',
    deviceLimit: '5',
    speedLimit: '0',
    visible: true,
    sort: '0',
    ...overrides,
  };
}

describe('buildPlanDraft', () => {
  it('齐了就放行，并把 GB 换算成字节', () => {
    const draft = buildPlanDraft(form());
    expect(draft.ok).toBe(true);
    if (!draft.ok) return;
    expect(draft.value.transfer_enable_bytes).toBe(200 * GIB);
    expect(draft.value.type).toBe('period');
    expect(draft.value.prices).toEqual([{ period: 'monthly', amount: 7200 }]);
  });

  it('🔴 没选类型 → 挡住（plans.kind 是 NOT NULL 且没有 DEFAULT）', () => {
    const draft = buildPlanDraft(form({ type: null }));
    expect(draft.ok).toBe(false);
    if (draft.ok) return;
    expect(draft.problem).toContain('类型');
  });

  it('周期套餐没有月付价 → 挡住（退款按月单价折算）', () => {
    const draft = buildPlanDraft(
      form({ prices: { monthly: '', quarterly: '20400', half_yearly: '', yearly: '', onetime: '' } }),
    );
    expect(draft.ok).toBe(false);
    if (draft.ok) return;
    expect(draft.problem).toContain('月付');
  });

  it('一个周期价格都没填 → 挡住（那是一个买不了的套餐）', () => {
    const draft = buildPlanDraft(
      form({
        type: 'traffic_pack',
        prices: { monthly: '', quarterly: '', half_yearly: '', yearly: '', onetime: '' },
      }),
    );
    expect(draft.ok).toBe(false);
  });

  it('价格不是整数 → 挡住（单位是分，7200 而不是 72.00）', () => {
    const draft = buildPlanDraft(
      form({ prices: { monthly: '72.00', quarterly: '', half_yearly: '', yearly: '', onetime: '' } }),
    );
    expect(draft.ok).toBe(false);
  });

  it('加油包只填一次性价格是合法的', () => {
    const draft = buildPlanDraft(
      form({
        type: 'traffic_pack',
        prices: { monthly: '', quarterly: '', half_yearly: '', yearly: '', onetime: '1500' },
      }),
    );
    expect(draft.ok).toBe(true);
  });
});

/* ────────────────────────── 列表三态 ────────────────────────── */

describe('套餐列表', () => {
  it('渲染出名称、各周期价格、类型与设备数', async () => {
    stubFetch(() => okList([STANDARD, PACK]));
    render(<PlansPage />);

    expect(await screen.findByText('标准版')).toBeTruthy();
    // 价格来自 API，前端不硬编码 —— 这里断言的是「分 → 元」的换算，不是某个具体定价。
    expect(screen.getByText('¥72.00')).toBeTruthy();
    expect(screen.getByText('¥204.00')).toBeTruthy();
    // 徽标文本在 <td> 与 <span> 上各匹配一次（祖先也算），所以用 getAllByText。
    expect(screen.getAllByText('加油包').length).toBeGreaterThan(0);
    expect(screen.getByText('5 台')).toBeTruthy();
    // 🔴 device_limit = 0 是「不限」，不是「0 台」。
    expect(screen.getByText('不限')).toBeTruthy();
  });

  it('列表为空 → 空态给出下一步动作', async () => {
    stubFetch(() => okList([]));
    render(<PlansPage />);

    expect(await screen.findByText('还没有套餐')).toBeTruthy();
    expect(screen.getAllByRole('button', { name: '新建套餐' }).length).toBeGreaterThan(0);
  });

  it('501 → 说「还没上线」，不摆一个红色故障框', async () => {
    // NOT_IMPLEMENTED 不在契约的 ErrorCode enum 里（错误映射层直接写出去的），
    // 所以判据是字符串比对而不是状态码 —— 将来一个真的 501 是另一回事。
    stubFetch(() => errorEnvelope(501, 'NOT_IMPLEMENTED', '尚未实现'));
    render(<PlansPage />);

    expect(await screen.findByText('套餐列表尚未开放')).toBeTruthy();
    expect(screen.queryByRole('button', { name: '重试' })).toBeNull();
  });

  it('403 AUTH_PERMISSION_DENIED → 说明重新登录没有用', async () => {
    stubFetch(() => errorEnvelope(403, 'AUTH_PERMISSION_DENIED', '角色不足'));
    render(<PlansPage />);

    expect(await screen.findByText('当前管理员账号看不到这一块')).toBeTruthy();
    expect(screen.getByText(/重新登录不会有帮助/)).toBeTruthy();
  });

  it('500 → 走全站统一的错误态，带请求号与重试', async () => {
    stubFetch(() => errorEnvelope(500, 'INTERNAL_ERROR', '出错了'));
    render(<PlansPage />);

    expect(await screen.findByRole('button', { name: '重试' })).toBeTruthy();
    expect(screen.getAllByText(/请求号/).length).toBeGreaterThan(0);
  });
});

/* ────────────────────────── D8：参数没收齐不许提交 ────────────────────────── */

describe('D8 · 新建套餐', () => {
  async function openEditor() {
    stubFetch((call) => {
      if (call.method === 'GET') return okList([STANDARD]);
      return json({ data: { ...STANDARD, id: 99 }, meta: { request_id: REQ } }, 201);
    });
    render(<PlansPage />);
    await screen.findByText('标准版');
    fireEvent.click(screen.getByRole('button', { name: '新建套餐' }));
    // 折叠状态下的按钮 → 展开确认面板。
    fireEvent.click(screen.getByRole('button', { name: '创建套餐' }));
  }

  function writes() {
    return calls.filter((c) => c.method !== 'GET');
  }

  it('🔴 没选类型 / 没填原因时，提交按钮点不动，也不会发出任何写请求', async () => {
    await openEditor();

    const submit = screen.getByRole('button', { name: '确认创建' });
    expect(submit.getAttribute('aria-disabled')).toBe('true');

    fireEvent.click(submit);
    expect(writes()).toHaveLength(0);

    // 把名字填上之后，挡住它的就只剩「没选类型」这一条 —— 这才是本用例要钉的那条。
    fireEvent.change(screen.getByLabelText('套餐名'), { target: { value: '加油包 50G' } });
    // 表单里说一次、确认面板的「为什么点不动」再说一次 —— 两处都该有。
    expect(screen.getAllByText(/还没选套餐类型/).length).toBe(2);
    fireEvent.click(screen.getByRole('button', { name: '确认创建' }));
    expect(writes()).toHaveLength(0);
  });

  it('业务字段填齐但原因不足 8 码位 → 仍然点不动', async () => {
    await openEditor();

    fireEvent.click(screen.getByRole('radio', { name: /周期套餐（cycle）/ }));
    fireEvent.change(screen.getByLabelText('套餐名'), { target: { value: '标准版' } });
    fireEvent.change(screen.getByLabelText('月付'), { target: { value: '7200' } });
    fireEvent.change(screen.getByLabelText('流量额度（GB）'), { target: { value: '200' } });
    fireEvent.change(screen.getByLabelText('设备数'), { target: { value: '5' } });
    fireEvent.change(screen.getByLabelText('操作原因（必填）'), { target: { value: '改价' } });

    const submit = screen.getByRole('button', { name: '确认创建' });
    expect(submit.getAttribute('aria-disabled')).toBe('true');
    fireEvent.click(submit);
    expect(writes()).toHaveLength(0);
  });

  it('全部收齐 → POST 一次，body 里带上 type（它决定 plans.kind）与分为单位的价格', async () => {
    await openEditor();

    fireEvent.click(screen.getByRole('radio', { name: /加油包（pack）/ }));
    fireEvent.change(screen.getByLabelText('套餐名'), { target: { value: '加油包 50G' } });
    fireEvent.change(screen.getByLabelText('一次性'), { target: { value: '1500' } });
    fireEvent.change(screen.getByLabelText('流量额度（GB）'), { target: { value: '50' } });
    fireEvent.change(screen.getByLabelText('设备数'), { target: { value: '0' } });
    fireEvent.change(screen.getByLabelText('操作原因（必填）'), {
      target: { value: '新开加油包，运营活动用' },
    });

    const submit = screen.getByRole('button', { name: '确认创建' });
    expect(submit.getAttribute('aria-disabled')).toBe('false');
    fireEvent.click(submit);

    await waitFor(() => expect(writes()).toHaveLength(1));
    const post = writes()[0]!;
    expect(post.method).toBe('POST');
    expect(post.path).toBe('/api/v1/admin/plans');
    const body = post.body as Record<string, unknown>;
    // 🔴 漏了 type 会让加油包被静默写成周期套餐（后端 NOT NULL 无默认值）。
    expect(body['type']).toBe('traffic_pack');
    expect(body['prices']).toEqual([{ period: 'onetime', amount: 1500 }]);
    expect(body['transfer_enable_bytes']).toBe(50 * GIB);
    expect(body['reason']).toBe('新开加油包，运营活动用');
  });

  it('确认面板里必须出现「改套餐只影响新订单」这句话', async () => {
    await openEditor();
    expect(screen.getByText('改套餐只影响新订单。')).toBeTruthy();
  });
});

describe('D8 · 下架套餐', () => {
  it('下架走 DELETE，且明说这一条不会写下原因', async () => {
    stubFetch((call) => {
      if (call.method === 'GET') return okList([STANDARD]);
      return new Response(null, { status: 204, headers: { 'X-Request-Id': REQ } });
    });
    render(<PlansPage />);
    await screen.findByText('标准版');

    fireEvent.click(screen.getByRole('button', { name: '编辑 / 下架' }));
    fireEvent.click(screen.getByRole('button', { name: '下架这个套餐' }));

    // D8 在登记表里既不要确认串也不要原因，DELETE 端点也没有请求体 ——
    // 所以这里**不该**出现一个收原因的框。
    expect(screen.queryByLabelText('操作原因（必填）')).toBeNull();
    expect(screen.getByText(/不会写下操作原因/)).toBeTruthy();

    fireEvent.click(screen.getByRole('button', { name: '确认下架' }));
    await waitFor(() => {
      const del = calls.filter((c) => c.method === 'DELETE');
      expect(del).toHaveLength(1);
      expect(del[0]!.path).toBe('/api/v1/admin/plans/2');
    });
  });

  it('服务端 409（还有未结算订单）→ 原样显示服务端那句话', async () => {
    stubFetch((call) => {
      if (call.method === 'GET') return okList([STANDARD]);
      return errorEnvelope(409, 'STATE_CONFLICT', '该套餐还有 3 张未结算的订单');
    });
    render(<PlansPage />);
    await screen.findByText('标准版');

    fireEvent.click(screen.getByRole('button', { name: '编辑 / 下架' }));
    fireEvent.click(screen.getByRole('button', { name: '下架这个套餐' }));
    fireEvent.click(screen.getByRole('button', { name: '确认下架' }));

    expect(await screen.findByText('该套餐还有 3 张未结算的订单')).toBeTruthy();
  });
});

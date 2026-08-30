// @vitest-environment jsdom
//
// 需要 DOM 的理由同 PlansPage.test.tsx：判据是纯函数，但「按钮变没变灰、
// 点下去有没有真的发请求」只有把组件挂起来才能回答。

/**
 * 优惠码页（模块 13，D8）的测试。
 *
 * 这一页最要紧的一条是**量纲**：`value` 在 `fixed` 下是分、在 `percent` 下是百分点，
 * 同一个数字在两种类型下差一个量级，而且**两边都不会报错**。
 * 所以类型没选时按钮必须点不动 —— 这是下面几条用例的重点。
 *
 * ⚠️ 这些用例证明的不是安全性。§6.2 的四层全部在服务端强制。
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { resetRuntimeConfig } from '@babelplus/shared';
import CouponsPage, {
  buildCouponDraft,
  discountText,
  isoToLocalInput,
  localInputToIso,
} from './CouponsPage.tsx';
import { resetAdminApiForTests } from '../lib/api.ts';
import { reportAdminAuthFailure } from '../lib/iap.ts';
import type { Coupon, Plan } from './catalog-common.tsx';

const REQ = '01K2COUPONCOUPONCOUPONCO';

const FIXED: Coupon = {
  id: 11,
  code: 'NEWYEAR',
  type: 'fixed',
  value: 2000,
  enabled: true,
  use_limit: 100,
  used_count: 7,
  started_at: '2026-01-01T00:00:00Z',
  ended_at: '2026-02-01T00:00:00Z',
  plan_ids: [2],
};

/** 🔴 与上面**同一个 value 数字**在 percent 下是 20%，不是 ¥20。 */
const PERCENT_UNLIMITED: Coupon = {
  id: 12,
  code: 'OPENFOREVER',
  type: 'percent',
  value: 20,
  enabled: false,
  used_count: 3,
  plan_ids: [],
};

const PLAN: Plan = {
  id: 2,
  name: '标准版',
  type: 'period',
  prices: [{ period: 'monthly', amount: 7200 }],
  transfer_enable_bytes: 200 * 1024 ** 3,
  device_limit: 5,
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

function errorEnvelope(status: number, code: string, message: string): Response {
  return json({ error: { code, message }, meta: { request_id: REQ } }, status);
}

function stubFetch(handler: (call: Call) => Response) {
  const spy = vi.fn(async (input: unknown, init?: RequestInit) => {
    const url = new URL(String(input));
    const method = (init?.method ?? 'GET').toUpperCase();
    let body: unknown;
    const raw = init?.body;
    if (raw !== undefined && raw !== null) {
      const text = typeof raw === 'string' ? raw : new TextDecoder().decode(raw as ArrayBuffer);
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

/** 两条 GET：优惠码列表 + 套餐列表（后者只为把 plan_ids 显示成名字）。 */
function stubBoth(coupons: readonly Coupon[], meta: Record<string, unknown> = {}) {
  return stubFetch((call) => {
    if (call.path === '/api/v1/admin/plans') return json({ data: [PLAN], meta: { request_id: REQ } });
    if (call.path === '/api/v1/admin/coupons' && call.method === 'GET') {
      return json({ data: coupons, meta: { request_id: REQ, ...meta } });
    }
    if (call.method === 'DELETE') return new Response(null, { status: 204 });
    return json({ data: coupons[0] ?? FIXED, meta: { request_id: REQ } }, 201);
  });
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

/* ────────────────────────── 纯函数 ────────────────────────── */

interface DraftInput {
  code: string;
  type: 'fixed' | 'percent' | null;
  value: string;
  enabled: boolean;
  useLimit: string;
  startedAt: string;
  endedAt: string;
  planIds: readonly number[];
}

function form(overrides: Partial<DraftInput> = {}): DraftInput {
  return {
    code: 'NEWYEAR',
    type: 'fixed',
    value: '2000',
    enabled: true,
    useLimit: '100',
    startedAt: '',
    endedAt: '',
    planIds: [],
    ...overrides,
  };
}

describe('buildCouponDraft', () => {
  it('齐了就放行', () => {
    const draft = buildCouponDraft(form());
    expect(draft.ok).toBe(true);
    if (!draft.ok) return;
    expect(draft.value.value).toBe(2000);
    expect(draft.value.use_limit).toBe(100);
  });

  it('🔴 没选类型 → 挡住（优惠额的量纲跟着类型走）', () => {
    const draft = buildCouponDraft(form({ type: null }));
    expect(draft.ok).toBe(false);
    if (draft.ok) return;
    expect(draft.problem).toContain('类型');
  });

  it('百分比超过 100 个百分点 → 挡住（折扣超过原价，订单金额会变成负数）', () => {
    expect(buildCouponDraft(form({ type: 'percent', value: '101' })).ok).toBe(false);
    expect(buildCouponDraft(form({ type: 'percent', value: '100' })).ok).toBe(true);
  });

  it('优惠额 ≤ 0 → 挡住（想停用请取消勾选「启用」）', () => {
    expect(buildCouponDraft(form({ value: '0' })).ok).toBe(false);
    expect(buildCouponDraft(form({ value: '-1' })).ok).toBe(false);
  });

  it('总量上限填了但小于 1 → 挡住；留空 = 不限（字段整个不发出去）', () => {
    expect(buildCouponDraft(form({ useLimit: '0' })).ok).toBe(false);
    const unlimited = buildCouponDraft(form({ useLimit: '' }));
    expect(unlimited.ok).toBe(true);
    if (!unlimited.ok) return;
    expect('use_limit' in unlimited.value).toBe(false);
  });

  it('结束时间不晚于开始时间 → 挡住', () => {
    const bad = buildCouponDraft(
      form({ startedAt: '2026-03-01T10:00', endedAt: '2026-03-01T09:00' }),
    );
    expect(bad.ok).toBe(false);
  });
});

describe('日期换算', () => {
  it('本地时间串 ↔ RFC3339 往返不丢时刻', () => {
    const local = '2026-03-01T10:30';
    const iso = localInputToIso(local);
    expect(typeof iso).toBe('string');
    expect(isoToLocalInput(iso as string)).toBe(local);
  });

  it('空 = 没填（undefined），看不懂 = null（要挡住提交）', () => {
    expect(localInputToIso('   ')).toBeUndefined();
    expect(localInputToIso('明天')).toBeNull();
  });
});

describe('discountText', () => {
  it('🔴 同一个数字在两种类型下必须显示成不同的东西', () => {
    expect(discountText({ type: 'fixed', value: 2000 })).toBe('减 ¥20.00');
    expect(discountText({ type: 'percent', value: 2000 })).toBe('减 2000%');
  });
});

/* ────────────────────────── 列表三态 ────────────────────────── */

describe('优惠码列表', () => {
  it('渲染出码、带量纲的折扣、适用套餐名与总量', async () => {
    stubBoth([FIXED, PERCENT_UNLIMITED]);
    render(<CouponsPage />);

    expect(await screen.findByText('NEWYEAR')).toBeTruthy();
    expect(screen.getByText('减 ¥20.00')).toBeTruthy();
    expect(screen.getByText('减 20%')).toBeTruthy();
    expect(screen.getByText('标准版')).toBeTruthy();
    expect(screen.getByText('100 / 7')).toBeTruthy();
    // 不限总量的券要显眼：它是「一张对外公开的无限折扣」。
    expect(screen.getByText('不限 / 3')).toBeTruthy();
  });

  it('第一页带 count=true 与 limit；翻页时带 cursor 且不再要总数', async () => {
    stubBoth([FIXED], { has_more: true, next_cursor: 'CURSOR2', total: 87 });
    render(<CouponsPage />);
    await screen.findByText('NEWYEAR');

    const first = calls.find((c) => c.path === '/api/v1/admin/coupons')!;
    expect(first.query.get('limit')).toBe('20');
    expect(first.query.get('count')).toBe('true');
    // 总数由 useRememberedTotal 在 effect 里落下来，比列表晚一拍 —— 要等。
    expect(await screen.findByText(/共 87 条/)).toBeTruthy();

    fireEvent.click(screen.getByRole('button', { name: '下一页' }));
    await waitFor(() => {
      const pages = calls.filter((c) => c.path === '/api/v1/admin/coupons');
      expect(pages.length).toBe(2);
      expect(pages[1]!.query.get('cursor')).toBe('CURSOR2');
      // COUNT(*) 在 db-f1-micro 上是实打实的开销，只在第一页付一次。
      expect(pages[1]!.query.get('count')).toBeNull();
    });
  });

  it('列表为空 → 空态给出下一步动作', async () => {
    stubBoth([]);
    render(<CouponsPage />);
    expect(await screen.findByText('还没有优惠码')).toBeTruthy();
  });

  it('501 → 说「还没上线」而不是摆一个故障框', async () => {
    stubFetch((call) => {
      if (call.path === '/api/v1/admin/plans') return json({ data: [], meta: { request_id: REQ } });
      return errorEnvelope(501, 'NOT_IMPLEMENTED', '尚未实现');
    });
    render(<CouponsPage />);
    expect(await screen.findByText('优惠码列表尚未开放')).toBeTruthy();
  });

  it('403 AUTH_PERMISSION_DENIED → 说明重新登录没有用', async () => {
    stubFetch((call) => {
      if (call.path === '/api/v1/admin/plans') return json({ data: [], meta: { request_id: REQ } });
      return errorEnvelope(403, 'AUTH_PERMISSION_DENIED', '角色不足');
    });
    render(<CouponsPage />);
    expect(await screen.findByText('当前管理员账号看不到这一块')).toBeTruthy();
  });
});

/* ────────────────────────── D8：参数没收齐不许提交 ────────────────────────── */

describe('D8 · 新建优惠码', () => {
  async function openEditor() {
    stubBoth([FIXED]);
    render(<CouponsPage />);
    await screen.findByText('NEWYEAR');
    fireEvent.click(screen.getByRole('button', { name: '新建优惠码' }));
    fireEvent.click(screen.getByRole('button', { name: '创建优惠码' }));
  }

  function writes() {
    return calls.filter((c) => c.method !== 'GET');
  }

  it('🔴 没选类型时点不动，也不会发出任何写请求', async () => {
    await openEditor();

    fireEvent.change(screen.getByLabelText('优惠码'), { target: { value: 'SPRING' } });
    fireEvent.change(screen.getByLabelText('优惠额（单位取决于类型，先选类型）'), {
      target: { value: '2000' },
    });
    fireEvent.change(screen.getByLabelText('操作原因（必填）'), {
      target: { value: '春季活动，运营已批' },
    });

    const submit = screen.getByRole('button', { name: '确认创建' });
    expect(submit.getAttribute('aria-disabled')).toBe('true');
    fireEvent.click(submit);
    expect(writes()).toHaveLength(0);
  });

  it('原因不足 8 码位 → 点不动', async () => {
    await openEditor();

    fireEvent.change(screen.getByLabelText('优惠码'), { target: { value: 'SPRING' } });
    fireEvent.click(screen.getByRole('radio', { name: /固定金额（fixed）/ }));
    fireEvent.change(screen.getByLabelText('优惠额（分）'), { target: { value: '2000' } });
    fireEvent.change(screen.getByLabelText('操作原因（必填）'), { target: { value: '活动' } });

    expect(
      screen.getByRole('button', { name: '确认创建' }).getAttribute('aria-disabled'),
    ).toBe('true');
    fireEvent.click(screen.getByRole('button', { name: '确认创建' }));
    expect(writes()).toHaveLength(0);
  });

  it('全部收齐 → POST 一次，body 里的 value 是按类型的量纲', async () => {
    await openEditor();

    fireEvent.change(screen.getByLabelText('优惠码'), { target: { value: 'SPRING' } });
    fireEvent.click(screen.getByRole('radio', { name: /百分比（percent）/ }));
    fireEvent.change(screen.getByLabelText('优惠额（百分点，1–100）'), { target: { value: '20' } });
    fireEvent.change(screen.getByLabelText('总量上限（留空 = 不限）'), { target: { value: '50' } });
    fireEvent.change(screen.getByLabelText('操作原因（必填）'), {
      target: { value: '春季活动，运营已批' },
    });

    fireEvent.click(screen.getByRole('button', { name: '确认创建' }));

    await waitFor(() => expect(writes()).toHaveLength(1));
    const post = writes()[0]!;
    expect(post.method).toBe('POST');
    expect(post.path).toBe('/api/v1/admin/coupons');
    const body = post.body as Record<string, unknown>;
    expect(body['type']).toBe('percent');
    // 🔴 20 个百分点。若这里写成 2000（当成分），券会变成 2000% 并被服务端拒。
    expect(body['value']).toBe(20);
    expect(body['use_limit']).toBe(50);
    expect(body['reason']).toBe('春季活动，运营已批');
  });

  it('留空总量上限时，界面明说这是一张无限折扣券', async () => {
    await openEditor();
    fireEvent.change(screen.getByLabelText('总量上限（留空 = 不限）'), { target: { value: '' } });
    expect(screen.getByText(/可以被无限次使用/)).toBeTruthy();
  });
});

describe('D8 · 删除优惠码', () => {
  it('不收原因（DELETE 端点没有请求体），确认后发 DELETE', async () => {
    stubBoth([FIXED]);
    render(<CouponsPage />);
    await screen.findByText('NEWYEAR');

    fireEvent.click(screen.getByRole('button', { name: '编辑 / 删除' }));
    fireEvent.click(screen.getByRole('button', { name: '删除这张优惠码' }));

    expect(screen.queryByLabelText('操作原因（必填）')).toBeNull();
    fireEvent.click(screen.getByRole('button', { name: '确认删除' }));

    await waitFor(() => {
      const del = calls.filter((c) => c.method === 'DELETE');
      expect(del).toHaveLength(1);
      expect(del[0]!.path).toBe('/api/v1/admin/coupons/11');
    });
  });
});

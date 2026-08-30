// @vitest-environment jsdom
//
// 需要 DOM 的理由同另外三页：判据是纯的，但「按钮变没变灰、点下去有没有真的发请求」
// 只有把组件挂起来才能回答。

/**
 * 邀请与返佣页（模块 9，D11）的测试。
 *
 * 钉住三件事：
 *  1. **可用次数要看得见** —— 一个能用 50 次的码，泄漏后的后果比一次性码差 50 倍，
 *     而它们在一张等宽表格里长得一模一样。
 *  2. 批量生成的入参没收齐时不发请求（数量 1–500、每码次数 ≥ 1）。
 *  3. **D11 调佣金**：记录 ID / 调整额 / 必填原因三者缺一，按钮就点不动。
 *
 * ⚠️ 这些用例证明的不是安全性。§6.2 的四层全部在服务端强制。
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { resetRuntimeConfig } from '@babelplus/shared';
import InvitesPage, { INVITE_MAX_COUNT } from './InvitesPage.tsx';
import { resetAdminApiForTests } from '../lib/api.ts';
import { reportAdminAuthFailure } from '../lib/iap.ts';
import type { InviteCode } from './catalog-common.tsx';

const REQ = '01K2INVITEINVITEINVITEIN';

const SEED: InviteCode = {
  id: 21,
  code: 'SEEDAAAA',
  status: 'ok',
  invite_url: 'https://example.test/register?invite=SEEDAAAA',
  use_limit: 50,
  used_count: 3,
  created_at: '2026-08-01T00:00:00Z',
};

const SINGLE: InviteCode = {
  id: 22,
  code: 'ONCEBBBB',
  status: 'exhausted',
  use_limit: 1,
  used_count: 1,
  created_at: '2026-08-02T00:00:00Z',
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

function stubList(codes: readonly InviteCode[], meta: Record<string, unknown> = {}) {
  return stubFetch((call) => {
    if (call.method === 'GET') return json({ data: codes, meta: { request_id: REQ, ...meta } });
    return json({ data: [], meta: { request_id: REQ } }, 201);
  });
}

function writes() {
  return calls.filter((c) => c.method !== 'GET');
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

/* ────────────────────────── 列表三态 ────────────────────────── */

describe('邀请码列表', () => {
  it('多次可用的码要显眼（泄漏后果差一个量级）', async () => {
    stubList([SEED, SINGLE]);
    render(<InvitesPage />);

    expect(await screen.findByText('SEEDAAAA')).toBeTruthy();
    expect(screen.getByText('50 / 3')).toBeTruthy();
    expect(screen.getByText('可用 50 次')).toBeTruthy();
    // 一次性码不该有这个警示徽标，否则它就不再是警示。
    expect(screen.queryByText('可用 1 次')).toBeNull();
    // 「已用尽」既是状态徽标也是筛选按钮的字，所以用 getAllByText。
    expect(screen.getAllByText('已用尽').length).toBeGreaterThan(0);
  });

  it('明说契约里没有归属人与有效期，不去按次数猜「种子码 / 用户码」', async () => {
    stubList([SEED]);
    render(<InvitesPage />);
    await screen.findByText('SEEDAAAA');
    expect(screen.getByText(/没有归属人、也没有有效期/)).toBeTruthy();
  });

  it('第一页带 count=true，翻页时带 cursor 且不再要总数', async () => {
    stubList([SEED], { has_more: true, next_cursor: 'CUR2', total: 120 });
    render(<InvitesPage />);
    await screen.findByText('SEEDAAAA');

    const first = calls.find((c) => c.path === '/api/v1/admin/invites')!;
    expect(first.query.get('count')).toBe('true');
    // 总数由 useRememberedTotal 在 effect 里落下来，比列表晚一拍 —— 要等。
    expect(await screen.findByText(/共 120 条/)).toBeTruthy();

    fireEvent.click(screen.getByRole('button', { name: '下一页' }));
    await waitFor(() => {
      const pages = calls.filter((c) => c.path === '/api/v1/admin/invites');
      expect(pages.length).toBe(2);
      expect(pages[1]!.query.get('cursor')).toBe('CUR2');
      expect(pages[1]!.query.get('count')).toBeNull();
    });
  });

  it('列表为空 → 空态给出下一步动作（冷启动的第一步）', async () => {
    stubList([]);
    render(<InvitesPage />);
    expect(await screen.findByText('还没有邀请码')).toBeTruthy();
  });

  it('501 → 说「还没上线」', async () => {
    stubList([]);
    stubFetch(() => errorEnvelope(501, 'NOT_IMPLEMENTED', '尚未实现'));
    render(<InvitesPage />);
    expect(await screen.findByText('邀请码列表尚未开放')).toBeTruthy();
  });

  it('403 AUTH_PERMISSION_DENIED → 说明重新登录没有用', async () => {
    stubFetch(() => errorEnvelope(403, 'AUTH_PERMISSION_DENIED', '角色不足'));
    render(<InvitesPage />);
    expect(await screen.findByText('当前管理员账号看不到这一块')).toBeTruthy();
  });
});

/* ────────────────────────── 批量生成种子码 ────────────────────────── */

describe('批量生成种子码', () => {
  async function openGenerator() {
    stubFetch((call) => {
      if (call.method === 'GET') return json({ data: [SEED], meta: { request_id: REQ } });
      return json(
        {
          data: [
            { id: 31, code: 'NEWAAAAA', status: 'ok', use_limit: 1, used_count: 0, created_at: '2026-08-30T00:00:00Z' },
            { id: 32, code: 'NEWBBBBB', status: 'ok', use_limit: 1, used_count: 0, created_at: '2026-08-30T00:00:00Z' },
          ],
          meta: { request_id: REQ },
        },
        201,
      );
    });
    render(<InvitesPage />);
    await screen.findByText('SEEDAAAA');
    fireEvent.click(screen.getByRole('button', { name: '批量生成种子码' }));
  }

  function generateButton(): HTMLButtonElement {
    return screen.getByRole('button', { name: '生成' }) as HTMLButtonElement;
  }

  it('数量超过上限 → 点不动，也不发请求', async () => {
    await openGenerator();

    fireEvent.change(screen.getByLabelText('生成数量'), {
      target: { value: String(INVITE_MAX_COUNT + 1) },
    });
    expect(generateButton().disabled).toBe(true);
    fireEvent.click(generateButton());
    expect(writes()).toHaveLength(0);
  });

  it('每码可用次数填 0 → 点不动（本系统没有不限次的邀请码）', async () => {
    await openGenerator();

    fireEvent.change(screen.getByLabelText('每个码可用次数'), { target: { value: '0' } });
    expect(generateButton().disabled).toBe(true);
    fireEvent.click(generateButton());
    expect(writes()).toHaveLength(0);
    expect(screen.getByText(/至少是 1/)).toBeTruthy();
  });

  it('填对了 → POST 一次，并把真正生成出来的码显示出来', async () => {
    await openGenerator();

    fireEvent.change(screen.getByLabelText('生成数量'), { target: { value: '2' } });
    fireEvent.change(screen.getByLabelText('每个码可用次数'), { target: { value: '1' } });
    fireEvent.change(screen.getByLabelText('备注（可选）'), { target: { value: '冷启动第一批' } });

    fireEvent.click(generateButton());

    await waitFor(() => expect(writes()).toHaveLength(1));
    const post = writes()[0]!;
    expect(post.path).toBe('/api/v1/admin/invites');
    const body = post.body as Record<string, unknown>;
    expect(body['count']).toBe(2);
    expect(body['use_limit']).toBe(1);
    expect(body['note']).toBe('冷启动第一批');

    expect(await screen.findByText(/已生成 2 个码/)).toBeTruthy();
    expect(screen.getByText(/NEWAAAAA/)).toBeTruthy();
  });

  it('服务端凑不齐时如实上报，界面提示不要按申请数去发', async () => {
    stubFetch((call) => {
      if (call.method === 'GET') return json({ data: [SEED], meta: { request_id: REQ } });
      return json(
        {
          data: [
            { id: 33, code: 'ONLYONE1', status: 'ok', use_limit: 1, used_count: 0, created_at: '2026-08-30T00:00:00Z' },
          ],
          meta: { request_id: REQ },
        },
        201,
      );
    });
    render(<InvitesPage />);
    await screen.findByText('SEEDAAAA');
    fireEvent.click(screen.getByRole('button', { name: '批量生成种子码' }));

    fireEvent.change(screen.getByLabelText('生成数量'), { target: { value: '3' } });
    fireEvent.click(screen.getByRole('button', { name: '生成' }));

    expect(await screen.findByText(/比申请的少 2 个/)).toBeTruthy();
  });

  it('写请求失败 → 按 ErrorCode 分支给出文案，不是「操作失败」四个字', async () => {
    stubFetch((call) => {
      if (call.method === 'GET') return json({ data: [SEED], meta: { request_id: REQ } });
      return errorEnvelope(403, 'AUTH_PERMISSION_DENIED', '当前角色不能生成邀请码');
    });
    render(<InvitesPage />);
    await screen.findByText('SEEDAAAA');
    fireEvent.click(screen.getByRole('button', { name: '批量生成种子码' }));
    fireEvent.click(screen.getByRole('button', { name: '生成' }));

    expect(await screen.findByText('这个账号不能执行这一条')).toBeTruthy();
  });
});

/* ────────────────────────── D11：调整佣金 ────────────────────────── */

describe('D11 · 调整佣金', () => {
  async function openAdjust() {
    stubFetch((call) => {
      if (call.method === 'GET') return json({ data: [SEED], meta: { request_id: REQ } });
      return json(
        {
          data: {
            id: 1234,
            amount: 1200,
            status: 'pending',
            created_at: '2026-08-01T00:00:00Z',
            order_trade_no: '20260801T7K2M9Q4',
          },
          meta: { request_id: REQ },
        },
        200,
      );
    });
    render(<InvitesPage />);
    await screen.findByText('SEEDAAAA');
    fireEvent.click(screen.getByRole('button', { name: '调整这笔佣金' }));
  }

  function submitButton() {
    return screen.getByRole('button', { name: '确认调整' });
  }

  it('🔴 ID / 调整额 / 原因缺任何一个都点不动，也不发请求', async () => {
    await openAdjust();

    expect(submitButton().getAttribute('aria-disabled')).toBe('true');
    fireEvent.click(submitButton());
    expect(writes()).toHaveLength(0);

    // 只填 ID：还缺调整额。
    fireEvent.change(screen.getByLabelText('佣金记录 ID'), { target: { value: '1234' } });
    expect(submitButton().getAttribute('aria-disabled')).toBe('true');

    // 调整额填 0：不是一个操作，只会往 append-only 的审计表里写一条什么都没发生的记录。
    fireEvent.change(screen.getByLabelText('调整额（分，可为负）'), { target: { value: '0' } });
    expect(submitButton().getAttribute('aria-disabled')).toBe('true');
    expect(screen.getAllByText(/调整额不能是 0/).length).toBeGreaterThan(0);

    // 业务字段齐了，但原因不足 8 码位 —— D11 在登记表里就是「必填原因」。
    fireEvent.change(screen.getByLabelText('调整额（分，可为负）'), { target: { value: '-1590' } });
    fireEvent.change(screen.getByLabelText('操作原因（必填）'), { target: { value: '调整' } });
    expect(submitButton().getAttribute('aria-disabled')).toBe('true');
    fireEvent.click(submitButton());
    expect(writes()).toHaveLength(0);
  });

  it('全部收齐 → POST 到 /commissions/{id}/adjust，body 带增量与原因', async () => {
    await openAdjust();

    fireEvent.change(screen.getByLabelText('佣金记录 ID'), { target: { value: '1234' } });
    fireEvent.change(screen.getByLabelText('调整额（分，可为负）'), { target: { value: '-1590' } });
    fireEvent.change(screen.getByLabelText('操作原因（必填）'), {
      target: { value: '邀请人退款套利，冲回这笔佣金' },
    });

    expect(submitButton().getAttribute('aria-disabled')).toBe('false');
    fireEvent.click(submitButton());

    await waitFor(() => expect(writes()).toHaveLength(1));
    const post = writes()[0]!;
    expect(post.method).toBe('POST');
    expect(post.path).toBe('/api/v1/admin/commissions/1234/adjust');
    const body = post.body as Record<string, unknown>;
    // 🔴 这是**增量**不是新值。
    expect(body['amount']).toBe(-1590);
    expect(body['reason']).toBe('邀请人退款套利，冲回这笔佣金');

    expect(await screen.findByText('调整完成')).toBeTruthy();
  });

  it('已划转的佣金被服务端拒（STATE_CONFLICT）→ 原样显示服务端那句话', async () => {
    stubFetch((call) => {
      if (call.method === 'GET') return json({ data: [SEED], meta: { request_id: REQ } });
      return errorEnvelope(422, 'STATE_CONFLICT', '这笔佣金的状态是 "transferred"，不能直接改金额');
    });
    render(<InvitesPage />);
    await screen.findByText('SEEDAAAA');
    fireEvent.click(screen.getByRole('button', { name: '调整这笔佣金' }));

    fireEvent.change(screen.getByLabelText('佣金记录 ID'), { target: { value: '1234' } });
    fireEvent.change(screen.getByLabelText('调整额（分，可为负）'), { target: { value: '-1590' } });
    fireEvent.change(screen.getByLabelText('操作原因（必填）'), {
      target: { value: '邀请人退款套利，冲回这笔佣金' },
    });
    fireEvent.click(screen.getByRole('button', { name: '确认调整' }));

    expect(
      await screen.findByText('这笔佣金的状态是 "transferred"，不能直接改金额'),
    ).toBeTruthy();
  });

  it('界面明说契约里没有佣金列表端点（ID 只能手输）', async () => {
    stubList([SEED]);
    render(<InvitesPage />);
    await screen.findByText('SEEDAAAA');
    expect(screen.getByText(/没有佣金列表端点，也没有邀请关系树端点/)).toBeTruthy();
  });
});

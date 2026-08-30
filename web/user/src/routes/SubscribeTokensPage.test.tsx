/**
 * 多 token 页的接线测试。
 *
 * 每个用例为什么必须存在：
 *
 *  1. 🔴 **明文只出现一次** —— 这一页的全部安全价值就在这一条上，而破坏它的改法
 *     既自然又无声：「用户不小心关掉了怎么办？存一份到 localStorage 回显吧」。
 *     DB 里只有 `token_hash` 与 `token_prefix`（`usersub.go`），明文除了那一次 201 响应
 *     哪儿都没有；前端一旦把它存下来，「服务端不留明文」这件事就白做了，
 *     而且不会有任何报错。这个用例钉三件事：关掉之后页面上再也没有它、
 *     刷新出来的列表里只有 `masked`、以及**任何 storage 里都不出现明文**。
 *
 *  2. 🔴 **「撤销全部」必须勾选后才可点** —— 它作废用户所有设备的订阅链接，
 *     是这一页唯一不可撤销的操作。少了 `requireAck`，它就退化成一个普通的两次点击，
 *     而两次点击挡不住任何误触。用例同时断言**未确认之前一个请求都没发出去**。
 *
 *  3. **403 `QUOTA_DEVICE_LIMIT` 在这个端点上说的是「链接条数」不是「设备数」** ——
 *     后端复用了这个码（`usersub.go`：上限 10 条）。照全站「设备数」的文案渲染，
 *     会把用户指到订阅页去踢设备，而在那里踢多少台都不会让他能新建链接。
 *     这是「按 code 分支」的一个反直觉边角：**同一个码在不同端点上含义不同。**
 *
 *  4. **501 说「该功能尚未开放」** —— 501 归一成 5xx，只按状态码分支会说成服务故障。
 *
 *  5. **空态给下一步动作**（§2.2），并且不是「暂无数据」。
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { MemoryRouter } from 'react-router';
import { resetRuntimeConfig } from '@babelplus/shared';
import { resetApiClientForTests } from '../lib/api.ts';
import { resetSessionForTests } from '../lib/session.ts';
import SubscribeTokensPage from './SubscribeTokensPage.tsx';

const REQUEST_ID = '01K2TOKENTOKENTOKENTOKENTO';

/** 这一串是**明文** —— 用例全程盯着它，它只许在创建响应之后出现那一次。 */
const PLAINTEXT = 'PLAINTEXT-TOKEN-ONLY-ONCE-abcdef';

function jsonResponse(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

function errorResponse(status: number, code: string, message: string): Response {
  return jsonResponse(status, { error: { code, message }, meta: { request_id: REQUEST_ID } });
}

const TOKENS = [
  {
    id: 1,
    name: 'iPhone',
    masked: 'a1b2c3d4…',
    created_at: '2026-08-01T00:00:00Z',
    last_used_at: '2026-08-28T10:00:00Z',
  },
  { id: 2, name: '公司电脑', masked: 'e5f6a7b8…', created_at: '2026-08-05T00:00:00Z' },
  {
    id: 3,
    name: '旧手机',
    masked: 'c9d0e1f2…',
    created_at: '2026-07-01T00:00:00Z',
    revoked_at: '2026-08-20T00:00:00Z',
  },
];

interface Call {
  method: string;
  path: string;
  body: string | null;
}

interface Handlers {
  list?: () => Response;
  create?: () => Response;
  revoke?: () => Response;
  revokeAll?: () => Response;
}

function stubTokens(handlers: Handlers): { calls: Call[] } {
  const calls: Call[] = [];
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: string | URL | Request, init?: RequestInit) => {
      const url = new URL(String(input));
      const method = (init?.method ?? 'GET').toUpperCase();
      const body = typeof init?.body === 'string' ? init.body : null;
      calls.push({ method, path: url.pathname, body });

      if (url.pathname === '/api/v1/user/subscription/tokens' && method === 'GET') {
        return (handlers.list ?? (() => jsonResponse(200, { data: TOKENS, meta: { request_id: REQUEST_ID } })))();
      }
      if (url.pathname === '/api/v1/user/subscription/tokens' && method === 'POST') {
        if (!handlers.create) throw new Error('这个用例不该发出创建请求');
        return handlers.create();
      }
      if (/^\/api\/v1\/user\/subscription\/tokens\/\d+$/.test(url.pathname) && method === 'DELETE') {
        if (!handlers.revoke) throw new Error('这个用例不该发出吊销请求');
        return handlers.revoke();
      }
      if (url.pathname === '/api/v1/user/subscription/revoke-all') {
        if (!handlers.revokeAll) throw new Error('这个用例不该发出全撤请求');
        return handlers.revokeAll();
      }
      throw new Error(`未预期的请求：${method} ${url.pathname}`);
    }),
  );
  return { calls };
}

function renderTokens() {
  return render(
    <MemoryRouter initialEntries={['/subscribe/tokens']}>
      <SubscribeTokensPage />
    </MemoryRouter>,
  );
}

/** 走一遍「起名 → 新建」。 */
function submitCreate(container: HTMLElement, name: string): void {
  fireEvent.change(screen.getByLabelText('备注名'), { target: { value: name } });
  const form = container.querySelector('form');
  if (!form) throw new Error('新建表单不存在');
  fireEvent.submit(form);
}

beforeEach(() => {
  resetRuntimeConfig();
  resetSessionForTests();
  resetApiClientForTests();
  window.sessionStorage.clear();
  window.localStorage.clear();
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe('SubscribeTokensPage', () => {
  it('成功：列表只显示打码后的 token，并区分有效与已失效', async () => {
    stubTokens({});
    const { container } = renderTokens();

    await waitFor(() => expect(screen.getByText('iPhone')).toBeTruthy());
    expect(screen.getByText('a1b2c3d4…')).toBeTruthy();
    expect(screen.getByText('2 条有效 · 共 3 条')).toBeTruthy();
    expect(screen.getByText('已失效')).toBeTruthy();
    // 「从未被使用过」不是装饰：它说明那台设备根本没配对成功，与「配好了连不上」的下一步不同。
    expect(screen.getAllByText(/从未被使用过/).length).toBeGreaterThanOrEqual(1);
    // 已失效的那一条不给「撤销」按钮 —— 撤一个已经撤过的只会拿到 404。
    expect(screen.getAllByText('撤销这一条')).toHaveLength(2);
    expect(container.textContent ?? '').not.toContain(PLAINTEXT);
  });

  it('🔴 新建的明文只出现一次：关掉之后页面和 storage 里都不再有它', async () => {
    stubTokens({
      // 创建后列表重拉：新的一条在列表里**只有 masked**，明文不会再回来。
      list: () =>
        jsonResponse(200, {
          data: [...TOKENS, { id: 4, name: '平板', masked: 'PLAINTE…', created_at: '2026-08-29T00:00:00Z' }],
          meta: { request_id: REQUEST_ID },
        }),
      create: () =>
        jsonResponse(201, {
          data: {
            id: 4,
            name: '平板',
            token: PLAINTEXT,
            subscribe_url: `https://api.example.test/s/${PLAINTEXT}`,
            created_at: '2026-08-29T00:00:00Z',
          },
          meta: { request_id: REQUEST_ID },
        }),
    });
    const { container } = renderTokens();

    await waitFor(() => expect(screen.getByText('iPhone')).toBeTruthy());
    submitCreate(container, '平板');

    await waitFor(() => expect(screen.getByText(/只显示这一次/)).toBeTruthy());
    expect(container.textContent ?? '').toContain(PLAINTEXT);

    // 明文**从来没有**被写进任何一处存储 —— 存了它，服务端「只留哈希」就白做了。
    expect(JSON.stringify(window.localStorage)).not.toContain(PLAINTEXT);
    expect(JSON.stringify(window.sessionStorage)).not.toContain(PLAINTEXT);

    fireEvent.click(screen.getByText('我已保存，关掉'));

    await waitFor(() => expect(screen.queryByText(/只显示这一次/)).toBeNull());
    const after = container.textContent ?? '';
    expect(after).not.toContain(PLAINTEXT);
    // 但那一条本身还在列表里，只是从此只有打码形态。
    expect(screen.getByText('平板')).toBeTruthy();
    expect(screen.getByText('PLAINTE…')).toBeTruthy();
  });

  it('🔴 「撤销全部」必须先勾选「我明白后果」，勾选前一个请求都不发', async () => {
    const { calls } = stubTokens({
      revokeAll: () =>
        jsonResponse(200, {
          data: { revoked: 2, sub_revoked_at: '2026-08-29T12:00:00Z' },
          meta: { request_id: REQUEST_ID },
        }),
    });
    renderTokens();

    await waitFor(() => expect(screen.getByText('iPhone')).toBeTruthy());
    fireEvent.click(screen.getByText('撤销全部（等同订阅页的「重置订阅」）'));

    // 确认框出现，后果逐条列出，但按钮此刻是禁用的。
    const confirm = screen.getByRole('dialog');
    expect(confirm.textContent).toContain('每一台设备都必须重新导入新链接');
    const confirmButton = screen.getByText('全部撤销') as HTMLButtonElement;
    expect(confirmButton.disabled).toBe(true);
    fireEvent.click(confirmButton);
    expect(calls.filter((c) => c.path === '/api/v1/user/subscription/revoke-all')).toHaveLength(0);

    fireEvent.click(screen.getByLabelText('我明白上面的后果'));
    expect((screen.getByText('全部撤销') as HTMLButtonElement).disabled).toBe(false);
    fireEvent.click(screen.getByText('全部撤销'));

    await waitFor(() =>
      expect(calls.filter((c) => c.path === '/api/v1/user/subscription/revoke-all')).toHaveLength(1),
    );
  });

  it('撤销单条：确认后才发 DELETE，且确认框说明只影响那一台设备', async () => {
    const { calls } = stubTokens({ revoke: () => new Response(null, { status: 204 }) });
    renderTokens();

    await waitFor(() => expect(screen.getByText('iPhone')).toBeTruthy());
    const revokeButtons = screen.getAllByText('撤销这一条');
    const first = revokeButtons[0];
    if (!first) throw new Error('列表里应该有可撤销的 token');
    fireEvent.click(first);

    expect(screen.getByRole('dialog').textContent).toContain('其它设备不受影响');
    expect(calls.filter((c) => c.method === 'DELETE')).toHaveLength(0);

    fireEvent.click(screen.getByText('确认撤销'));
    await waitFor(() => expect(calls.filter((c) => c.method === 'DELETE')).toHaveLength(1));
    expect(calls.find((c) => c.method === 'DELETE')?.path).toBe('/api/v1/user/subscription/tokens/1');
  });

  it('403 QUOTA_DEVICE_LIMIT：说的是链接条数到顶，不是设备数到顶', async () => {
    stubTokens({
      create: () =>
        errorResponse(403, 'QUOTA_DEVICE_LIMIT', '订阅链接数量已达上限（10 条）。请先撤销不再使用的链接'),
    });
    const { container } = renderTokens();

    await waitFor(() => expect(screen.getByText('iPhone')).toBeTruthy());
    submitCreate(container, '平板');

    await waitFor(() => expect(screen.getByText('订阅链接的条数到上限了')).toBeTruthy());
    const alert = screen.getByRole('alert').textContent ?? '';
    expect(alert).toContain('订阅链接数量已达上限（10 条）');
    // 不许把用户指到「踢设备」上去 —— 在这个端点上踢多少台设备都不会放开这条限制。
    expect(alert).not.toContain('踢');
    expect(alert).not.toContain('在线设备');
  });

  it('空：给「回到订阅页」，不是「暂无数据」', async () => {
    stubTokens({ list: () => jsonResponse(200, { data: [], meta: { request_id: REQUEST_ID } }) });
    renderTokens();

    await waitFor(() => expect(screen.getByText('还没有单独签发过链接')).toBeTruthy());
    expect(screen.getByText(/回到订阅页/)).toBeTruthy();
  });

  it('501 NOT_IMPLEMENTED：说「该功能尚未开放」，不推状态页', async () => {
    stubTokens({ list: () => errorResponse(501, 'NOT_IMPLEMENTED', '尚未实现') });
    const { container } = renderTokens();

    await waitFor(() => expect(screen.getByText('该功能尚未开放')).toBeTruthy());
    const text = container.textContent ?? '';
    expect(text).not.toContain('我们这边出了问题');
    expect(text).not.toContain('查看状态页');
  });
});

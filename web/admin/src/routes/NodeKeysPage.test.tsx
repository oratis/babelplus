// @vitest-environment jsdom

/**
 * 模块 6 · 节点密钥的接线测试。
 *
 * 这一页只有一条真正要命的规则，其余用例都是在保护它：
 *
 * 🔴 **界面必须把轮换做成两步，且第 ③ 步在没有见证密钥时点不动。**
 *
 * 「见证」的判据是 `last_used_at > created_at`（签发之后被用过），
 * **不是 `last_used_at != null`**。两者的差别只有在一种情况下才显出来：
 * 一把很久以前用过、节点早就换走了的密钥。按后一种判据，它会被当成见证，
 * 于是界面允许你吊销另一把 —— 而那正好制造出「节点失联」，
 * 且失联之后你没法再让它换密钥，只能上机器手动改。
 * 下面那条 `last_used_at 早于 created_at` 的用例是唯一会拦住这次退化的东西。
 *
 * 其余几条：
 *  · 明文只显示一次，且必须说「关掉就再也看不到」—— DB 里只有 sha256，没有任何端点能再取出来。
 *  · 签发与吊销都要 TOTP（§6.2 L3 + 契约的 `X-TOTP-Code` 必填头），没填时一个请求都不发。
 *  · 服务端的 409 原样显示 —— 那个 409 是**对的**，不是 bug，不该被改写成「稍后重试」。
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { MemoryRouter } from 'react-router';
import { resetRuntimeConfig } from '@babelplus/shared';
import { resetAdminApiForTests } from '../lib/api.ts';
import NodeKeysPage from './NodeKeysPage.tsx';

const REQUEST_ID = '01K2NODEKEYSNODEKEYSNODEKE';
const TOTP = '481920';

function jsonResponse(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

function errorResponse(status: number, code: string, message: string): Response {
  return jsonResponse(status, { error: { code, message }, meta: { request_id: REQUEST_ID } });
}

function dataResponse(data: unknown, status = 200): Response {
  return jsonResponse(status, { data, meta: { request_id: REQUEST_ID } });
}

const NODE = {
  id: 1,
  name: '东京 01',
  type: 'vless_reality',
  host: '203.0.113.10',
  port: 443,
  region: '日本',
  enabled: true,
  group_ids: [1],
};

const minutesAgo = (n: number) => new Date(Date.now() - n * 60_000).toISOString();
const daysAgo = (n: number) => new Date(Date.now() - n * 86_400_000).toISOString();

const DEFAULT_SCOPES = [
  'node:config:read',
  'node:users:read',
  'node:traffic:write',
  'node:alive:write',
  'node:alive:read',
];

function key(overrides: Record<string, unknown> = {}) {
  return {
    id: 1,
    key_id: 'aaa111',
    name: '2026-01 首发',
    scopes: DEFAULT_SCOPES,
    created_at: daysAgo(200),
    last_used_at: minutesAgo(1),
    ...overrides,
  };
}

/** 轮换做到一半：旧密钥在用，新密钥刚签发、节点还没换过去。第 ③ 步必须点不动。 */
const MID_ROTATION = [
  key(),
  key({ id: 2, key_id: 'bbb222', name: '2026-08 轮换', created_at: minutesAgo(2), last_used_at: undefined }),
];

/** 节点已经换到新密钥上了：新密钥的 last_used_at 晚于它自己的 created_at。第 ③ 步放行。 */
const READY_TO_REVOKE = [
  key({ last_used_at: daysAgo(1) }),
  key({
    id: 2,
    key_id: 'bbb222',
    name: '2026-08 轮换',
    created_at: minutesAgo(10),
    last_used_at: minutesAgo(1),
  }),
];

interface Call {
  method: string;
  path: string;
  body: unknown;
  totp: string | null;
}

/** 见 `NodesPage.test.tsx` 里同名函数的注释：空 body 的请求到这里是空 ArrayBuffer。 */
function decodeBody(raw: unknown): unknown {
  const text =
    typeof raw === 'string' ? raw : raw instanceof ArrayBuffer ? new TextDecoder().decode(raw) : '';
  if (text.trim() === '') return null;
  try {
    return JSON.parse(text) as unknown;
  } catch {
    return text;
  }
}

function stubApi(handler: (call: Call) => Response): Call[] {
  const calls: Call[] = [];
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: string | URL | Request, init?: RequestInit) => {
      const url = new URL(String(input));
      const call: Call = {
        method: (init?.method ?? 'GET').toUpperCase(),
        path: url.pathname,
        body: decodeBody(init?.body),
        // L3 的码走请求头，不是 body。这一列存在就是为了钉住这一点。
        totp: new Headers(init?.headers ?? {}).get('X-TOTP-Code'),
      };
      calls.push(call);
      return handler(call);
    }),
  );
  return calls;
}

/** 默认桩：节点列表 + 指定的密钥列表，其余请求交给 `extra`。 */
function stubKeys(keys: unknown[], extra?: (call: Call) => Response | null): Call[] {
  return stubApi((call) => {
    if (call.path === '/api/v1/admin/nodes') {
      return jsonResponse(200, {
        data: [NODE],
        meta: { request_id: REQUEST_ID, next_cursor: null, has_more: false, total: 1 },
      });
    }
    if (call.path === '/api/v1/admin/nodes/1/keys' && call.method === 'GET') {
      return jsonResponse(200, { data: keys, meta: { request_id: REQUEST_ID } });
    }
    return extra?.(call) ?? errorResponse(404, 'RESOURCE_NOT_FOUND', `未预期的请求 ${call.path}`);
  });
}

function renderPage(search = '?node=1') {
  return render(
    <MemoryRouter initialEntries={[`/admin/node-keys${search}`]}>
      <NodeKeysPage />
    </MemoryRouter>,
  );
}

function blockedHints(): string[] {
  return screen.queryAllByTestId('danger-blocked-hint').map((e) => e.textContent ?? '');
}

beforeEach(() => {
  resetRuntimeConfig();
  resetAdminApiForTests();
  window.sessionStorage.clear();
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe('选节点', () => {
  it('没选节点时不去拉密钥，并说清楚密钥是挂在节点上的', async () => {
    const calls = stubKeys([]);
    renderPage('');

    expect(await screen.findByText('先选一台节点')).toBeTruthy();
    expect(calls.some((c) => c.path.includes('/keys'))).toBe(false);
  });

  it('地址栏里带了 ?node= 就直接拉那台机器的密钥', async () => {
    const calls = stubKeys(MID_ROTATION);
    renderPage();

    await screen.findByText('aaa111');
    expect(calls.some((c) => c.path === '/api/v1/admin/nodes/1/keys')).toBe(true);
  });
});

describe('密钥列表', () => {
  it('渲染指纹、备注、scope、签发与最后使用', async () => {
    stubKeys(MID_ROTATION);
    renderPage();

    await screen.findByText('aaa111');
    // bbb222 出现两次：第 ② 步的「在等它」列表里一次，第 ③ 步的密钥列表里一次。
    // 这不是重复渲染，是同一把密钥在两块里各有各的意思。
    expect(screen.getAllByText('bbb222').length).toBe(2);
    expect(screen.getByText('2026-01 首发')).toBeTruthy();
    expect(screen.getAllByText('node:config:read').length).toBeGreaterThan(0);
    // 一个节点可以同时持有多把有效密钥 —— 这正是两步轮换成立的前提。
    expect(screen.getAllByText('有效').length).toBe(2);
  });

  it('吊销与过期分开显示 —— 前者是有人做过一个动作，后者是时间到了', async () => {
    stubKeys([
      key({ id: 3, key_id: 'ccc333', revoked_at: daysAgo(1) }),
      key({ id: 4, key_id: 'ddd444', expires_at: daysAgo(1) }),
    ]);
    renderPage();

    await screen.findByText('ccc333');
    expect(screen.getByText('已吊销')).toBeTruthy();
    expect(screen.getByText('已过期')).toBeTruthy();
  });

  it('一把密钥都没有时，空态说的是「下一步去签一把」', async () => {
    stubKeys([]);
    renderPage();

    expect(await screen.findByText('这个节点还没签发过密钥')).toBeTruthy();
  });

  it('501 说「尚未开放」', async () => {
    stubApi((call) => {
      if (call.path === '/api/v1/admin/nodes') {
        return jsonResponse(200, { data: [NODE], meta: { request_id: REQUEST_ID } });
      }
      return errorResponse(501, 'NOT_IMPLEMENTED', '尚未实现');
    });
    renderPage();

    expect(await screen.findByText('尚未开放')).toBeTruthy();
  });
});

describe('D5 第 ① 步 · 签发', () => {
  it('没输 TOTP 时不许提交，一个请求都不发', async () => {
    // 只放一把密钥：MID_ROTATION 有两把有效的，那时挡住提交的是「已达上限」
    // 而不是「缺 TOTP」—— 门禁按结构性原因优先，这个用例要测的是后者。
    const calls = stubKeys([key()]);
    renderPage();
    await screen.findByText('aaa111');

    fireEvent.click(screen.getByRole('button', { name: '签发新密钥（第 ① 步）' }));
    const submit = screen.getByRole('button', { name: '签发' });
    expect(submit.hasAttribute('disabled')).toBe(true);
    fireEvent.click(submit);

    expect(calls.some((c) => c.method === 'POST')).toBe(false);
    expect(blockedHints().some((t) => t.includes('6 位码'))).toBe(true);
  });

  it('TOTP 走请求头 X-TOTP-Code，不进 body', async () => {
    const calls = stubKeys([], (call) => {
      if (call.method === 'POST') {
        return dataResponse(
          { key: key({ id: 5, key_id: 'eee555', created_at: new Date().toISOString(), last_used_at: undefined }), secret: 'bpn_eee555_S3CR3T' },
          201,
        );
      }
      return null;
    });
    renderPage();
    await screen.findByText('这个节点还没签发过密钥');

    fireEvent.click(screen.getByRole('button', { name: '签发新密钥（第 ① 步）' }));
    fireEvent.change(screen.getByLabelText('密钥名'), { target: { value: '2026-08 轮换' } });
    fireEvent.change(screen.getByLabelText('验证器 6 位码'), { target: { value: TOTP } });
    fireEvent.click(screen.getByRole('button', { name: '签发' }));

    await waitFor(() => expect(calls.some((c) => c.method === 'POST')).toBe(true));
    const post = calls.find((c) => c.method === 'POST');
    expect(post?.path).toBe('/api/v1/admin/nodes/1/keys');
    expect(post?.totp).toBe(TOTP);
    const body = post?.body as Record<string, unknown>;
    expect(body.name).toBe('2026-08 轮换');
    expect(body.scopes).toEqual(DEFAULT_SCOPES);
    // 默认不给 node:status:write，也不带 expires_at（默认不过期）。
    expect(body.scopes).not.toContain('node:status:write');
    expect('expires_at' in body).toBe(false);
    expect('totp' in body).toBe(false);
  });

  it('🔴 明文只显示这一次，并且必须说「关掉就再也看不到」', async () => {
    stubKeys([], (call) =>
      call.method === 'POST'
        ? dataResponse(
            { key: key({ id: 5, key_id: 'eee555', created_at: new Date().toISOString(), last_used_at: undefined }), secret: 'bpn_eee555_S3CR3T' },
            201,
          )
        : null,
    );
    renderPage();
    await screen.findByText('这个节点还没签发过密钥');

    fireEvent.click(screen.getByRole('button', { name: '签发新密钥（第 ① 步）' }));
    fireEvent.change(screen.getByLabelText('验证器 6 位码'), { target: { value: TOTP } });
    fireEvent.click(screen.getByRole('button', { name: '签发' }));

    const secret = await screen.findByTestId('issued-secret');
    expect(secret.textContent).toBe('bpn_eee555_S3CR3T');
    expect(screen.getByText(/现在就复制，关掉就再也看不到/)).toBeTruthy();
    expect(screen.getByText(/没有任何端点能再取出这串明文/)).toBeTruthy();

    // 关掉之后就真的没了 —— 不做「N 秒后自动隐藏」，那会把密钥吃掉。
    fireEvent.click(screen.getByRole('button', { name: '我已经复制好了，关掉' }));
    expect(screen.queryByTestId('issued-secret')).toBeNull();
  });

  it('已经有两把有效密钥时不许再签 —— 第三把说明上一次轮换没做完', async () => {
    const calls = stubKeys(MID_ROTATION);
    renderPage();
    await screen.findByText('aaa111');

    fireEvent.click(screen.getByRole('button', { name: '签发新密钥（第 ① 步）' }));
    expect(screen.getByRole('button', { name: '签发' }).hasAttribute('disabled')).toBe(true);
    expect(blockedHints().some((t) => t.includes('上一次轮换没做完'))).toBe(true);
    fireEvent.click(screen.getByRole('button', { name: '签发' }));
    expect(calls.some((c) => c.method === 'POST')).toBe(false);
  });

  it('一个 scope 都不选时不许提交 —— 零 scope 的密钥在所有端点上都会 403', async () => {
    const calls = stubKeys([]);
    renderPage();
    await screen.findByText('这个节点还没签发过密钥');

    for (const scope of DEFAULT_SCOPES) {
      fireEvent.click(screen.getByRole('checkbox', { name: new RegExp(scope) }));
    }
    fireEvent.click(screen.getByRole('button', { name: '签发新密钥（第 ① 步）' }));
    fireEvent.change(screen.getByLabelText('验证器 6 位码'), { target: { value: TOTP } });

    expect(screen.getByRole('button', { name: '签发' }).hasAttribute('disabled')).toBe(true);
    fireEvent.click(screen.getByRole('button', { name: '签发' }));
    expect(calls.some((c) => c.method === 'POST')).toBe(false);
  });
});

describe('D5 第 ② 步 · 等节点用上新密钥', () => {
  it('新密钥还没被用过时，明确说它在等，并给出判据', async () => {
    stubKeys(MID_ROTATION);
    renderPage();
    await screen.findByText('aaa111');

    expect(screen.getByText(/有 1 把密钥/)).toBeTruthy();
    expect(screen.getAllByText('签发后还没被用过').length).toBeGreaterThan(0);
    expect(screen.getByText(/最后使用晚于签发时刻/)).toBeTruthy();
  });

  it('🔴 last_used_at 早于 created_at 的密钥**不算**被用过', async () => {
    // 这一把在 200 天前被用过，但它是 2 分钟前才签发的 —— 时间戳对不上，
    // 说明那次使用属于另一段历史。按 `last_used_at != null` 判会把它当成见证，
    // 而拿它去吊销另一把正好制造出「节点失联」。
    stubKeys([key({ id: 2, key_id: 'bbb222', created_at: minutesAgo(2), last_used_at: daysAgo(200) })]);
    renderPage();

    await screen.findAllByText('bbb222');
    expect(screen.getAllByText('签发后还没被用过').length).toBeGreaterThan(0);
    expect(screen.queryByText('节点在用它')).toBeNull();
    expect(screen.getByText(/早于签发时刻，不算数/)).toBeTruthy();
  });

  it('都被用过时说「没有在等的新密钥」', async () => {
    stubKeys(READY_TO_REVOKE);
    renderPage();

    await screen.findByText('aaa111');
    expect(screen.getByText(/没有在等的新密钥/)).toBeTruthy();
  });

  it('「立刻刷新」重拉密钥列表，但不把页面打回骨架屏', async () => {
    const calls = stubKeys(MID_ROTATION);
    renderPage();
    await screen.findByText('aaa111');

    fireEvent.click(screen.getByRole('button', { name: '立刻刷新' }));
    await waitFor(() =>
      expect(calls.filter((c) => c.path === '/api/v1/admin/nodes/1/keys').length).toBe(2),
    );
    // 刷新期间列表还在，没有变成 loading。
    expect(screen.getByText('aaa111')).toBeTruthy();
  });
});

describe('D5 第 ③ 步 · 吊销', () => {
  function revokeButton(keyId: string) {
    return screen.getByRole('button', { name: `吊销 ${keyId}（第 ③ 步）` });
  }

  it('🔴 没有见证密钥时吊销点不动，且理由要说清楚会失联 + 服务端也会 409', async () => {
    const calls = stubKeys(MID_ROTATION);
    renderPage();
    await screen.findByText('aaa111');

    // 轮换做到一半：新密钥还没被用过，所以两把都不能吊销。
    fireEvent.click(revokeButton('aaa111'));
    expect(blockedHints().some((t) => t.includes('没有另一把'))).toBe(true);
    expect(blockedHints().some((t) => t.includes('STATE_CONFLICT'))).toBe(true);
    expect(calls.some((c) => c.method === 'DELETE')).toBe(false);
  });

  it('🔴 只有一把密钥时也点不动 —— 吊销它等于让这台机器失联', async () => {
    stubKeys([key()]);
    renderPage();
    await screen.findByText('aaa111');

    fireEvent.click(revokeButton('aaa111'));
    // 面板会展开（组件刻意不把折叠按钮也变灰：藏起来的按钮无法被投诉），
    // 但提交键是灰的，而且理由说的是「没有另一把能接手的密钥」。
    expect(screen.getByRole('button', { name: '确认吊销' }).hasAttribute('disabled')).toBe(true);
    expect(blockedHints().some((t) => t.includes('没有另一把'))).toBe(true);
  });

  it('有见证之后仍然要 TOTP，没填不发请求', async () => {
    const calls = stubKeys(READY_TO_REVOKE);
    renderPage();
    await screen.findByText('aaa111');

    fireEvent.click(revokeButton('aaa111'));
    const submit = screen.getByRole('button', { name: '确认吊销' });
    expect(submit.hasAttribute('disabled')).toBe(true);
    fireEvent.click(submit);
    expect(calls.some((c) => c.method === 'DELETE')).toBe(false);
  });

  it('见证 + TOTP 齐了才发 DELETE /admin/node-keys/{key_id}', async () => {
    const calls = stubKeys(READY_TO_REVOKE, (call) =>
      call.method === 'DELETE' ? new Response(null, { status: 204 }) : null,
    );
    renderPage();
    await screen.findByText('aaa111');

    fireEvent.click(revokeButton('aaa111'));
    fireEvent.change(screen.getByLabelText('验证器 6 位码'), { target: { value: TOTP } });
    fireEvent.click(screen.getByRole('button', { name: '确认吊销' }));

    await waitFor(() => expect(calls.some((c) => c.method === 'DELETE')).toBe(true));
    const del = calls.find((c) => c.method === 'DELETE');
    // 路径参数是 key_id（base32 六字符短标识），不是数据库主键 id。
    expect(del?.path).toBe('/api/v1/admin/node-keys/aaa111');
    expect(del?.totp).toBe(TOTP);
  });

  it('🔴 服务端的 409 原样显示 —— 那个 409 是对的，不是 bug', async () => {
    stubKeys(READY_TO_REVOKE, (call) =>
      call.method === 'DELETE'
        ? errorResponse(
            409,
            'STATE_CONFLICT',
            '新密钥尚未被节点使用过，现在吊销旧密钥会导致节点失联',
          )
        : null,
    );
    renderPage();
    await screen.findByText('aaa111');

    fireEvent.click(revokeButton('aaa111'));
    fireEvent.change(screen.getByLabelText('验证器 6 位码'), { target: { value: TOTP } });
    fireEvent.click(screen.getByRole('button', { name: '确认吊销' }));

    // 服务端那句话比任何前端兜底文案都具体，所以原样显示，不改写成「稍后重试」。
    expect(
      await screen.findByText('新密钥尚未被节点使用过，现在吊销旧密钥会导致节点失联'),
    ).toBeTruthy();
    expect(screen.getByText('当前状态不允许这次操作')).toBeTruthy();
  });

  it('已吊销的密钥不再显示吊销按钮', async () => {
    stubKeys([key({ id: 3, key_id: 'ccc333', revoked_at: daysAgo(1) })]);
    renderPage();

    await screen.findByText('ccc333');
    expect(screen.queryByRole('button', { name: /吊销 ccc333/ })).toBeNull();
  });
});

describe('query 形态的 token', () => {
  it('保留原文，同时写明它已被 2026-08-17 的实测推翻', async () => {
    stubKeys([]);
    renderPage();
    await screen.findByText('这个节点还没签发过密钥');

    const card = screen.getByText('query 形态的 token').closest('div') as HTMLElement;
    // 原文是资产：它记录了我们当初的判断，删掉会让「我们曾经以为这是暂时的」这件事消失。
    expect(within(card).getByText(/全量切换前必须关闭/)).toBeTruthy();
    expect(within(card).getByText(/没有开关可切换/)).toBeTruthy();
    // 「最近 24 小时经 query 认证的次数」没有端点，缺口要写在界面上。
    expect(within(card).getByText(/契约里没有任何端点返回它/)).toBeTruthy();
  });
});

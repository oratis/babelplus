// @vitest-environment jsdom

/**
 * 模块 5 · 节点详情的接线测试。
 *
 * 每个用例为什么必须存在：
 *
 *  1. 🔴 **没有 `load_status` 时不许画一份全 0 的负载** —— 服务端刻意在没上报过时
 *     不给这个对象（`adminNodeView` 的注释：全 0 在后台看起来是「这台机器很空闲」，
 *     恰恰是把「我们不知道」渲染成了最让人放心的那个样子）。前端补 0 会把服务端
 *     那一处刻意的克制原样抹掉，而且不会有任何东西报错。
 *
 *  2. 🔴 **PATCH 只发改动过的字段** —— 契约的 `AdminNodeUpsert` 是一张整表，
 *     而服务端对没提供的字段回填当前值。把整张表发回去在单人操作下没有区别，
 *     在两个人同时开着这一页时会**静默覆盖**别人刚改的字段（PATCH 没有并发保护）。
 *     这个用例是唯一会拦住「顺手把整个 form 发出去」那次提交的东西。
 *
 *  3. **D4 删除要求确认串 + 原因，缺一不可，且缺的时候一个请求都不发**。
 *
 *  4. **id 不是数字时不发请求** —— `Number.parseInt('12abc')` 会给出 12，
 *     而这一页上有删除按钮：拿一个猜出来的 id 去查，查到的是另一台机器。
 *
 *  5. **404 不是「加载失败」** —— 它要给一条回列表的路，不是一个重试按钮。
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { MemoryRouter, Route, Routes } from 'react-router';
import { resetRuntimeConfig } from '@babelplus/shared';
import { resetAdminApiForTests } from '../lib/api.ts';
import NodeDetailPage from './NodeDetailPage.tsx';

const REQUEST_ID = '01K2NODEDETAILNODEDETAILNO';

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

function node(overrides: Record<string, unknown> = {}) {
  return {
    id: 1,
    name: '东京 01',
    type: 'vless_reality',
    host: '203.0.113.10',
    port: 443,
    region: '日本',
    enabled: true,
    group_ids: [1, 2],
    config_rev: 7,
    user_rev: 3,
    last_push_at: new Date(Date.now() - 60_000).toISOString(),
    ...overrides,
  };
}

interface Call {
  method: string;
  path: string;
  body: unknown;
}

/** 见 `NodesPage.test.tsx` 里同名函数的注释：空 body 的 POST 到这里是空 ArrayBuffer。 */
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
      };
      calls.push(call);
      return handler(call);
    }),
  );
  return calls;
}

function renderPage(id = '1') {
  return render(
    <MemoryRouter initialEntries={[`/admin/nodes/${id}`]}>
      <Routes>
        <Route path="/admin/nodes/:id" element={<NodeDetailPage />} />
      </Routes>
    </MemoryRouter>,
  );
}

/** 只读区那张卡。节点名在这一页上会出现好几次（标题、确认面板），所以断言要圈定范围。 */
function basicsCard(): HTMLElement {
  return screen.getByText('基础').closest('div') as HTMLElement;
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

describe('只读区', () => {
  it('渲染连接信息与 ETag 版本号', async () => {
    stubApi(() => dataResponse(node()));
    renderPage();

    await screen.findByText('基础');
    const card = basicsCard();
    expect(within(card).getByText('203.0.113.10')).toBeTruthy();
    expect(within(card).getByText('443')).toBeTruthy();
    expect(within(card).getByText('日本')).toBeTruthy();
    expect(within(card).getByText('1, 2')).toBeTruthy();
    expect(within(card).getByText('7')).toBeTruthy();
  });

  it('config_rev 缺失时说「缺失」而不是 0 —— null 的意思是这台机器的 ETag 从此不工作', async () => {
    stubApi(() => dataResponse(node({ config_rev: undefined, user_rev: undefined })));
    renderPage();

    await screen.findByText('基础');
    expect(within(basicsCard()).getAllByText('缺失').length).toBe(2);
    expect(within(basicsCard()).queryByText('0')).toBeNull();
  });

  it('🔴 没有 load_status 时不画全 0 的负载，而是说清楚两种可能', async () => {
    stubApi(() => dataResponse(node({ load_status: undefined, last_status_at: undefined })));
    renderPage();

    expect(await screen.findByText(/没有负载快照/)).toBeTruthy();
    // 全 0 在后台看起来是「这台机器很空闲」—— 那是把「我们不知道」渲染成了最让人放心的样子。
    expect(screen.queryByText('0.0%')).toBeNull();
    expect(screen.getByText(/这里不显示 0/)).toBeTruthy();
  });

  it('有 load_status 时渲染四项占用', async () => {
    stubApi(() =>
      dataResponse(
        node({
          last_status_at: new Date().toISOString(),
          load_status: {
            cpu: 42.5,
            mem: { total: 2_000_000_000, used: 1_000_000_000 },
            swap: { total: 0, used: 0 },
            disk: { total: 40_000_000_000, used: 38_000_000_000 },
          },
        }),
      ),
    );
    renderPage();

    expect(await screen.findByText('42.5%')).toBeTruthy();
    expect(screen.getByText(/50%/)).toBeTruthy();
    // swap 的 total 是 0：算不出占比就说算不出，不显示 NaN% 也不显示 0%。
    expect(screen.getByTitle('上报的 total 是 0，算不出占比')).toBeTruthy();
  });

  it('把「协议参数编辑器接不了」写在界面上，而不是悄悄不显示', async () => {
    stubApi(() => dataResponse(node()));
    renderPage();

    expect(await screen.findByText(/协议参数 JSON 编辑器不在这里，也接不上/)).toBeTruthy();
    expect(screen.getByText(/没有任何协议参数字段/)).toBeTruthy();
  });
});

describe('取不到节点时', () => {
  it('id 不是数字 → 一个请求都不发', async () => {
    const calls = stubApi(() => dataResponse(node()));
    renderPage('12abc');

    expect(await screen.findByText('这不是一个节点 id')).toBeTruthy();
    // `Number.parseInt('12abc')` 会给出 12，而这一页上有删除按钮。
    expect(calls.length).toBe(0);
  });

  it('404 给的是回列表的路，不是重试按钮', async () => {
    stubApi(() => errorResponse(404, 'RESOURCE_NOT_FOUND', '节点不存在'));
    renderPage();

    expect(await screen.findByText('找不到这个节点')).toBeTruthy();
    expect(screen.getAllByText(/回到节点列表/).length).toBeGreaterThan(0);
    expect(screen.queryByRole('button', { name: '重试' })).toBeNull();
  });

  it('501 说「尚未开放」', async () => {
    stubApi(() => errorResponse(501, 'NOT_IMPLEMENTED', '尚未实现'));
    renderPage();

    expect(await screen.findByText('尚未开放')).toBeTruthy();
    expect(screen.queryByText('找不到这个节点')).toBeNull();
  });
});

describe('D9 · 改连接信息', () => {
  async function open() {
    const calls = stubApi((call) => {
      if (call.method === 'GET') return dataResponse(node());
      if (call.method === 'PATCH') return dataResponse(node({ port: 8443 }));
      return errorResponse(404, 'RESOURCE_NOT_FOUND', `未预期的请求 ${call.path}`);
    });
    renderPage();
    await screen.findByText('基础');
    return calls;
  }

  it('没有改动时按钮点不动，也不发请求', async () => {
    const calls = await open();
    fireEvent.click(screen.getByRole('button', { name: '保存连接信息' }));

    const submit = screen.getByRole('button', { name: '确认保存' });
    expect(submit.hasAttribute('disabled')).toBe(true);
    fireEvent.click(submit);
    expect(calls.some((c) => c.method === 'PATCH')).toBe(false);
    expect(screen.getByTestId('danger-blocked-hint').textContent).toContain('还没有任何改动');
  });

  it('改了字段之后把 diff 摆在确认按钮上方 —— D9 的危害是「改错了不报错」', async () => {
    await open();
    fireEvent.change(screen.getByLabelText('端口'), { target: { value: '8443' } });
    fireEvent.click(screen.getByRole('button', { name: '保存连接信息' }));

    // 圈定 diff 那一块再断言：443 在只读区也有一份，全页 getByText 会命中两个。
    const diff = screen.getByText('这次会改这 1 处：').closest('div') as HTMLElement;
    expect(within(diff).getByText('443')).toBeTruthy();
    expect(within(diff).getByText('8443')).toBeTruthy();
    expect(within(diff).getByText('端口：')).toBeTruthy();
  });

  it('原因不足 8 字时不许提交', async () => {
    const calls = await open();
    fireEvent.change(screen.getByLabelText('端口'), { target: { value: '8443' } });
    fireEvent.click(screen.getByRole('button', { name: '保存连接信息' }));
    fireEvent.change(screen.getByLabelText('操作原因（必填）'), { target: { value: '改端口' } });

    expect(screen.getByRole('button', { name: '确认保存' }).hasAttribute('disabled')).toBe(true);
    fireEvent.click(screen.getByRole('button', { name: '确认保存' }));
    expect(calls.some((c) => c.method === 'PATCH')).toBe(false);
  });

  it('🔴 只发改动过的字段 —— 整张表发回去会静默覆盖别人刚改的字段', async () => {
    const calls = await open();
    fireEvent.change(screen.getByLabelText('端口'), { target: { value: '8443' } });
    fireEvent.click(screen.getByRole('button', { name: '保存连接信息' }));
    fireEvent.change(screen.getByLabelText('操作原因（必填）'), {
      target: { value: '443 被干扰，换到 8443 观察一周' },
    });
    fireEvent.click(screen.getByRole('button', { name: '确认保存' }));

    await waitFor(() => expect(calls.some((c) => c.method === 'PATCH')).toBe(true));
    const patch = calls.find((c) => c.method === 'PATCH');
    expect(patch?.path).toBe('/api/v1/admin/nodes/1');
    const body = patch?.body as Record<string, unknown>;
    // name / type 是契约的必填字段，一定在；改过的 port 在；没改的 host / region / group_ids 不在。
    expect(body.name).toBe('东京 01');
    expect(body.type).toBe('vless_reality');
    expect(body.port).toBe(8443);
    expect(body.reason).toBe('443 被干扰，换到 8443 观察一周');
    expect('host' in body).toBe(false);
    expect('region' in body).toBe(false);
    expect('group_ids' in body).toBe(false);
  });

  it('🔴 永远不发 enabled —— PATCH 带上它等于绕过 D4 把机器停掉', async () => {
    const calls = await open();
    fireEvent.change(screen.getByLabelText('名称'), { target: { value: '东京 02' } });
    fireEvent.click(screen.getByRole('button', { name: '保存连接信息' }));
    fireEvent.change(screen.getByLabelText('操作原因（必填）'), {
      target: { value: '按新命名规则改名，与机房编号对齐' },
    });
    fireEvent.click(screen.getByRole('button', { name: '确认保存' }));

    await waitFor(() => expect(calls.some((c) => c.method === 'PATCH')).toBe(true));
    const body = calls.find((c) => c.method === 'PATCH')?.body as Record<string, unknown>;
    expect('enabled' in body).toBe(false);
  });

  it('分组填了非数字时不许提交', async () => {
    const calls = await open();
    fireEvent.change(screen.getByLabelText('分组 id'), { target: { value: '1, abc' } });
    fireEvent.click(screen.getByRole('button', { name: '保存连接信息' }));

    // CI 上（GH Actions 33603696664）这一条曾在点击后立刻断言时读到「还没有任何改动」——
    // 面板展开与表单派生的 blocked 文案不在同一次提交里落地，本地跑不出来。等它稳定下来再断言。
    await waitFor(() =>
      expect(screen.getByTestId('danger-blocked-hint').textContent).toContain('分组只能填正整数'),
    );
    fireEvent.click(screen.getByRole('button', { name: '确认保存' }));
    expect(calls.some((c) => c.method === 'PATCH')).toBe(false);
  });
});

describe('D4 · 停用与删除', () => {
  async function open() {
    const calls = stubApi((call) => {
      if (call.method === 'GET') return dataResponse(node());
      if (call.path === '/api/v1/admin/nodes/1/disable') return dataResponse(node({ enabled: false }));
      if (call.method === 'DELETE') return new Response(null, { status: 204 });
      return errorResponse(404, 'RESOURCE_NOT_FOUND', `未预期的请求 ${call.path}`);
    });
    renderPage();
    await screen.findByText('基础');
    return calls;
  }

  it('详情页上有停用，不是只有删除 —— 停用可逆，删除不可逆', async () => {
    await open();
    expect(screen.getByRole('button', { name: '停用这个节点' })).toBeTruthy();
    expect(screen.getByRole('button', { name: '删除这个节点' })).toBeTruthy();
  });

  it('删除：确认串与原因缺一不可，缺的时候一个请求都不发', async () => {
    const calls = await open();
    fireEvent.click(screen.getByRole('button', { name: '删除这个节点' }));

    const submit = screen.getByRole('button', { name: '确认删除' });
    expect(submit.hasAttribute('disabled')).toBe(true);
    fireEvent.click(submit);
    expect(calls.some((c) => c.method === 'DELETE')).toBe(false);

    // 只填确认串还不够。
    fireEvent.change(screen.getByLabelText('输入节点名以确认'), { target: { value: '东京 01' } });
    expect(screen.getByRole('button', { name: '确认删除' }).hasAttribute('disabled')).toBe(true);
    // 页面上此刻有两条 blocked-hint：这一条，以及折叠着的 D9「还没有任何改动」。
    // 取全部再找，比猜 DOM 结构稳。
    const hints = () => screen.getAllByTestId('danger-blocked-hint').map((e) => e.textContent ?? '');
    expect(hints().some((t) => t.includes('至少 8 个字'))).toBe(true);

    // 只填原因也不够（先把确认串改错）。
    fireEvent.change(screen.getByLabelText('输入节点名以确认'), { target: { value: '东京' } });
    fireEvent.change(screen.getByLabelText('操作原因（必填）'), {
      target: { value: '机器退租，已确认上面没有活跃用户' },
    });
    expect(screen.getByRole('button', { name: '确认删除' }).hasAttribute('disabled')).toBe(true);
    fireEvent.click(screen.getByRole('button', { name: '确认删除' }));
    expect(calls.some((c) => c.method === 'DELETE')).toBe(false);
  });

  it('两样都填齐才发 DELETE，且 body 里两个字段都在', async () => {
    const calls = await open();
    fireEvent.click(screen.getByRole('button', { name: '删除这个节点' }));
    fireEvent.change(screen.getByLabelText('输入节点名以确认'), { target: { value: '东京 01' } });
    fireEvent.change(screen.getByLabelText('操作原因（必填）'), {
      target: { value: '机器退租，已确认上面没有活跃用户' },
    });
    fireEvent.click(screen.getByRole('button', { name: '确认删除' }));

    await waitFor(() => expect(calls.some((c) => c.method === 'DELETE')).toBe(true));
    const del = calls.find((c) => c.method === 'DELETE');
    expect(del?.path).toBe('/api/v1/admin/nodes/1');
    expect(del?.body).toEqual({
      confirmation: '东京 01',
      reason: '机器退租，已确认上面没有活跃用户',
    });
  });

  it('删除的确认框要说清楚危害、软删除没有恢复入口，以及在线人数为什么显示不出来', async () => {
    await open();
    fireEvent.click(screen.getByRole('button', { name: '删除这个节点' }));

    expect(screen.getByText(/连带删掉它的全部密钥/)).toBeTruthy();
    expect(screen.getByText(/后台没有恢复入口/)).toBeTruthy();
    // D4 登记表要求「确认框内必须显示当前在线人数」，契约给不了 —— 缺口写在操作者眼前。
    expect(screen.getByText(/当前在线人数这里显示不出来/)).toBeTruthy();
  });

  it('停用成功后就地变成「启用」，整页不重拉', async () => {
    const calls = await open();
    fireEvent.click(screen.getByRole('button', { name: '停用这个节点' }));
    fireEvent.change(screen.getByLabelText('输入节点名以确认'), { target: { value: '东京 01' } });
    fireEvent.click(screen.getByRole('button', { name: '确认停用' }));

    expect(await screen.findByRole('button', { name: '启用' })).toBeTruthy();
    expect(calls.filter((c) => c.method === 'GET').length).toBe(1);
  });
});

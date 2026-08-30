// @vitest-environment jsdom
//
// 后台的 vitest 默认跑在 node 环境（那里测的是 IAP 401 的判别，纯函数不需要 DOM）。
// 这一页要测的是「按钮在参数收齐之前点不动」与「三态分支渲染了哪一块」，
// 两者都只有在真实 DOM 上才成立 —— 把它们写成对纯函数的断言，
// 会出现「判据绿着而按钮照样能点」的情况。

/**
 * 模块 5 · 节点列表的接线测试。
 *
 * 每个用例为什么必须存在：
 *
 *  1. 🔴 **在线人数与今日流量不许显示成 0** —— 契约的 `AdminNode` 里没有这两个字段，
 *     而「补一个 0 上去」是任何人接这一页时最自然的动作。它不会报错，
 *     只会把「我们不知道这台机器上有多少人」渲染成「这台机器上没有人」——
 *     而后者正是让人放心按下停用的那个数字。这个用例是唯一会拦住那次提交的东西。
 *
 *  2. **D4 在确认串打对之前不许提交** —— 前端拦不住 curl，但它必须拦住
 *     「手滑点两下就把一台在跑的机器停了」。这一条钉的是**没有请求发出去**，
 *     不是「按钮看起来是灰的」。
 *
 *  3. **501 说「尚未开放」而不是「服务端出错了」** —— 501 按状态码归一成 5xx，
 *     只按状态码分支的实现会把「后端还没写」说成故障，并把人推去一个一切正常的状态页。
 *     节点域现在全部已实现，这条是回滚保险。
 *
 *  4. **按 `ErrorCode` 分支** —— 403 + `AUTH_PERMISSION_DENIED` 要说「需要有人给你开」，
 *     而不是全站默认的那句「没有访问权限」。
 *
 *  5. **筛选是前端的，且界面要说出来** —— 端点没有筛选参数。
 *     「筛不到」与「没有」在这一页上是两件事，混淆的代价是有人以为某台机器不存在。
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { MemoryRouter } from 'react-router';
import { resetRuntimeConfig } from '@babelplus/shared';
import { resetAdminApiForTests } from '../lib/api.ts';
import NodesPage from './NodesPage.tsx';

const REQUEST_ID = '01K2NODESNODESNODESNODESNO';

function jsonResponse(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

function errorResponse(status: number, code: string, message: string): Response {
  return jsonResponse(status, { error: { code, message }, meta: { request_id: REQUEST_ID } });
}

function listResponse(nodes: unknown[], extra: Record<string, unknown> = {}): Response {
  return jsonResponse(200, {
    data: nodes,
    meta: { request_id: REQUEST_ID, next_cursor: null, has_more: false, ...extra },
  });
}

const minutesAgo = (n: number) => new Date(Date.now() - n * 60_000).toISOString();

function node(overrides: Record<string, unknown> = {}) {
  return {
    id: 1,
    name: '东京 01',
    type: 'vless_reality',
    host: '203.0.113.10',
    port: 443,
    region: '日本',
    enabled: true,
    group_ids: [1],
    config_rev: 7,
    user_rev: 3,
    last_push_at: minutesAgo(1),
    ...overrides,
  };
}

interface Call {
  method: string;
  path: string;
  body: unknown;
}

/**
 * 请求体解码。
 *
 * ⚠️ 客户端在最外层把 body 读成了 `ArrayBuffer`（为了能重发同一个请求），
 * 而**没有 body 的 POST**（enable / disable）到这里是一个**空的** ArrayBuffer。
 * 直接 `JSON.parse('')` 会抛，而抛在 fetch 桩里会被客户端当成一次网络失败 ——
 * 于是「停用失败」这类用例会看到「请求没能到达服务端」，
 * 排查半天才发现是测试脚手架自己的问题。
 */
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

/** 记下每一次请求，供「没收齐参数时一个请求都不许发出去」这类断言用。 */
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

function renderPage() {
  return render(
    <MemoryRouter initialEntries={['/admin/nodes']}>
      <NodesPage />
    </MemoryRouter>,
  );
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

describe('节点列表', () => {
  it('渲染名称、地区、协议、最后上报与健康，并显示管理面才有的总数', async () => {
    stubApi(() => listResponse([node(), node({ id: 2, name: '洛杉矶 01', region: '美国' })], { total: 2 }));
    renderPage();

    expect(await screen.findByText('东京 01')).toBeTruthy();
    expect(screen.getByText('洛杉矶 01')).toBeTruthy();
    expect(screen.getAllByText('vless_reality').length).toBe(2);
    expect(screen.getByText('日本')).toBeTruthy();
    // ⚠️ 管理面**可以**返总数（`?count=true` → `meta.total`），与用户面口径不同。
    expect(screen.getByText('共 2 台')).toBeTruthy();
  });

  it('第一页带 count=true，翻页不带 —— COUNT(*) 在 db-f1-micro 上不能每页都付', async () => {
    const calls = stubApi(() => listResponse([node()], { total: 1 }));
    renderPage();
    await screen.findByText('东京 01');

    const first = calls.find((c) => c.path === '/api/v1/admin/nodes');
    expect(first).toBeTruthy();
    // 这里断言的是「第一页确实要了总数」；不带 count 的翻页由 node-common 的实现保证。
    expect(calls[0]?.method).toBe('GET');
  });

  it('🔴 在线人数与今日流量显示成占位符，绝不显示 0', async () => {
    stubApi(() => listResponse([node()]));
    renderPage();
    await screen.findByText('东京 01');

    // 契约的 AdminNode 里没有这两个字段。显示 0 会把「我们不知道」
    // 渲染成「这台机器上没有人」，而那正是让人放心按下停用的那个数字。
    expect(screen.getByTitle(/契约的 AdminNode 里没有每节点在线人数/)).toBeTruthy();
    expect(screen.getByTitle(/getAdminStats\?scope=server/)).toBeTruthy();

    const online = screen.getByText('在线人数').closest('div');
    expect(online?.textContent).not.toContain('0');
  });

  it('三种健康态分开说：在上报 / 超过 5 分钟没上报 / 从没上报过', async () => {
    stubApi(() =>
      listResponse([
        node({ id: 1, name: 'fresh', last_push_at: minutesAgo(1) }),
        node({ id: 2, name: 'stale', last_push_at: minutesAgo(30) }),
        // 🔴 从没上报过 ≠ 上报超时：前者多半是「密钥还没发」，后者是「它本来在跑，现在联系不上」。
        node({ id: 3, name: 'never', last_push_at: undefined }),
      ]),
    );
    renderPage();

    await screen.findByText('fresh');
    // 逐行断言而不是全页找：这三句话同时也是筛选下拉里的三个 option，
    // 全页 getByText 会命中两个元素而失败 —— 而那不是产品缺陷。
    const row = (name: string) => screen.getByText(name).closest('li') as HTMLElement;
    expect(within(row('fresh')).getByText('在上报')).toBeTruthy();
    expect(within(row('stale')).getByText('超过 5 分钟没上报')).toBeTruthy();
    expect(within(row('never')).getByText('从没上报过')).toBeTruthy();
  });

  it('没有分组的节点要标出来 —— 它对所有用户不可见', async () => {
    stubApi(() => listResponse([node({ group_ids: [] })]));
    renderPage();
    await screen.findByText('东京 01');
    expect(screen.getByText('无分组')).toBeTruthy();
  });

  it('空态给的是下一步动作，不是「暂无数据」', async () => {
    stubApi(() => listResponse([], { total: 0 }));
    renderPage();

    expect(await screen.findByText('还没有节点')).toBeTruthy();
    expect(screen.getByText('新建第一台节点')).toBeTruthy();
  });
});

describe('错误分支（按 ErrorCode，不按 HTTP 状态码）', () => {
  it('501 说「尚未开放」，不推人去状态页', async () => {
    stubApi(() => errorResponse(501, 'NOT_IMPLEMENTED', '尚未实现'));
    renderPage();

    expect(await screen.findByText('尚未开放')).toBeTruthy();
    // 501 的 kind 是 server，走 ErrorState 的话会说「我们这边出了问题」并给一个状态页链接，
    // 而状态页上一切正常 —— 「还没做」不是故障。
    expect(screen.queryByText('服务端出错了')).toBeNull();
    expect(screen.getByText(new RegExp(REQUEST_ID))).toBeTruthy();
  });

  it('403 + AUTH_PERMISSION_DENIED 说「需要有人给你开」，不说「登录过期」', async () => {
    stubApi(() => errorResponse(403, 'AUTH_PERMISSION_DENIED', '当前角色不能读节点'));
    renderPage();

    expect(await screen.findByText('这个管理员账号读不了这里')).toBeTruthy();
    expect(screen.getByText(/重新登录、换浏览器都不会改变这个结果/)).toBeTruthy();
  });

  it('5xx 给重试按钮', async () => {
    stubApi(() => errorResponse(500, 'INTERNAL_ERROR', '数据库连不上'));
    renderPage();

    expect(await screen.findByText('服务端出错了')).toBeTruthy();
  });
});

describe('D4 · 停用节点', () => {
  async function openDisablePanel() {
    const calls = stubApi((call) => {
      if (call.path === '/api/v1/admin/nodes') return listResponse([node()], { total: 1 });
      if (call.path === '/api/v1/admin/nodes/1/disable') {
        return jsonResponse(200, { data: node({ enabled: false }), meta: { request_id: REQUEST_ID } });
      }
      return errorResponse(404, 'RESOURCE_NOT_FOUND', `未预期的请求 ${call.path}`);
    });
    renderPage();
    await screen.findByText('东京 01');
    fireEvent.click(screen.getByRole('button', { name: '停用这个节点' }));
    return calls;
  }

  it('🔴 确认串没打对时不许提交 —— 钉的是「没有请求发出去」', async () => {
    const calls = await openDisablePanel();

    const submit = screen.getByRole('button', { name: '确认停用' });
    expect(submit.hasAttribute('disabled')).toBe(true);
    fireEvent.click(submit);

    // 按钮变灰只是省一次注定失败的往返；真正要钉住的是**一个请求都没发出去**。
    expect(calls.some((c) => c.path.endsWith('/disable'))).toBe(false);
    expect(screen.getByTestId('danger-blocked-hint').textContent).toContain('逐字输入节点名');
  });

  it('确认串区分大小写、且必须逐字相同', async () => {
    await openDisablePanel();
    const input = screen.getByLabelText('输入节点名以确认');

    fireEvent.change(input, { target: { value: '东京 0' } });
    expect(screen.getByRole('button', { name: '确认停用' }).hasAttribute('disabled')).toBe(true);

    fireEvent.change(input, { target: { value: '东京 01' } });
    expect(screen.getByRole('button', { name: '确认停用' }).hasAttribute('disabled')).toBe(false);
  });

  it('确认框里必须写清楚「会让人在 60 秒内掉线」，并说明在线人数为什么显示不出来', async () => {
    await openDisablePanel();

    expect(screen.getByText(/这台机器上的所有在线用户在 60 秒内掉线/)).toBeTruthy();
    // D4 的登记表要求「确认框内必须显示当前在线人数」。契约给不了这个数，
    // 那就把缺口写在操作者眼前 —— 他至少知道自己是在没有这个数的情况下按下去的。
    expect(screen.getByText(/当前在线人数这里显示不出来/)).toBeTruthy();
    // 停用这条路径上确认串发不到服务端（契约没有请求体），这句话不许省。
    expect(screen.getByText(/这一层确认串只在前端/)).toBeTruthy();
  });

  it('打对确认串之后才真的发 POST /disable，并就地更新那一行', async () => {
    const calls = await openDisablePanel();

    fireEvent.change(screen.getByLabelText('输入节点名以确认'), { target: { value: '东京 01' } });
    fireEvent.click(screen.getByRole('button', { name: '确认停用' }));

    await waitFor(() => expect(calls.some((c) => c.path === '/api/v1/admin/nodes/1/disable')).toBe(true));
    // 停用成功后这一行变成「停用中」+ 一个启用按钮，**整张表不重拉**。
    // 逐行断言：「停用中」同时也是筛选下拉里的一个 option。
    const row = await screen.findByRole('button', { name: '启用' });
    expect(within(row.closest('li') as HTMLElement).getByText('停用中')).toBeTruthy();
    expect(calls.filter((c) => c.path === '/api/v1/admin/nodes').length).toBe(1);
  });

  it('停用失败时确认面板要自己报错，不能静静地关掉', async () => {
    stubApi((call) => {
      if (call.path === '/api/v1/admin/nodes') return listResponse([node()], { total: 1 });
      return errorResponse(403, 'AUTH_PERMISSION_DENIED', '当前角色不能写节点');
    });
    renderPage();
    await screen.findByText('东京 01');

    fireEvent.click(screen.getByRole('button', { name: '停用这个节点' }));
    fireEvent.change(screen.getByLabelText('输入节点名以确认'), { target: { value: '东京 01' } });
    fireEvent.click(screen.getByRole('button', { name: '确认停用' }));

    expect(await screen.findByText('这个账号不能执行这一条')).toBeTruthy();
    // 面板还开着，节点也还是启用中 —— 失败必须看得见。
    // 用链接定位这一行：节点名在确认面板里还会出现好几次（事实块、确认串提示）。
    const row = screen.getByRole('link', { name: '东京 01' }).closest('li') as HTMLElement;
    expect(within(row).getByText('启用中')).toBeTruthy();
    expect(within(row).getByRole('button', { name: '确认停用' })).toBeTruthy();
  });

  it('已经停用的节点显示的是「启用」，而启用不走确认面板', async () => {
    stubApi(() => listResponse([node({ enabled: false })]));
    renderPage();
    await screen.findByText('东京 01');

    expect(screen.getByRole('button', { name: '启用' })).toBeTruthy();
    expect(screen.queryByRole('button', { name: '停用这个节点' })).toBeNull();
  });
});

describe('筛选', () => {
  it('按关键字筛，并且说明筛的只是已加载的行', async () => {
    stubApi(() =>
      listResponse([node(), node({ id: 2, name: '洛杉矶 01', region: '美国', host: '198.51.100.7' })]),
    );
    renderPage();
    await screen.findByText('东京 01');

    fireEvent.change(screen.getByLabelText('关键字'), { target: { value: '198.51.100' } });

    // host 也进匹配范围：出故障时手里那串通常是 IP，不是节点名。
    expect(screen.getByText('洛杉矶 01')).toBeTruthy();
    expect(screen.queryByText('东京 01')).toBeNull();
    expect(screen.getByText(/筛选只作用于/)).toBeTruthy();
  });

  it('筛不到时说的是「已加载的 N 台里没有」，不是「没有这样的节点」', async () => {
    stubApi(() => listResponse([node()]));
    renderPage();
    await screen.findByText('东京 01');

    fireEvent.change(screen.getByLabelText('关键字'), { target: { value: '不存在的机器' } });
    expect(screen.getByText(/已加载的 1 台里没有符合条件的/)).toBeTruthy();
  });
});

describe('新建节点', () => {
  it('原因不足 8 字时不许提交（服务端按码位数，前端同口径）', async () => {
    const calls = stubApi((call) => {
      if (call.method === 'GET') return listResponse([node()]);
      return jsonResponse(201, { data: node({ id: 9 }), meta: { request_id: REQUEST_ID } });
    });
    renderPage();
    await screen.findByText('东京 01');

    fireEvent.click(screen.getByRole('button', { name: /新建节点/ }));
    fireEvent.change(screen.getByLabelText('名称'), { target: { value: '首尔 01' } });
    fireEvent.change(screen.getByLabelText('Host'), { target: { value: '203.0.113.99' } });
    fireEvent.change(screen.getByLabelText('操作原因（必填）'), { target: { value: '扩容' } });

    const create = screen.getByRole('button', { name: '创建' });
    expect(create.hasAttribute('disabled')).toBe(true);
    fireEvent.click(create);
    expect(calls.some((c) => c.method === 'POST')).toBe(false);
  });

  it('填齐之后发 POST，并把新节点排在最前面', async () => {
    const calls = stubApi((call) => {
      if (call.method === 'GET') return listResponse([node()]);
      return jsonResponse(201, {
        data: node({ id: 9, name: '首尔 01', enabled: false, last_push_at: undefined }),
        meta: { request_id: REQUEST_ID },
      });
    });
    renderPage();
    await screen.findByText('东京 01');

    fireEvent.click(screen.getByRole('button', { name: /新建节点/ }));
    fireEvent.change(screen.getByLabelText('名称'), { target: { value: '首尔 01' } });
    fireEvent.change(screen.getByLabelText('Host'), { target: { value: '203.0.113.99' } });
    fireEvent.change(screen.getByLabelText('操作原因（必填）'), {
      target: { value: '东京线路晚高峰丢包，加一台分流' },
    });
    fireEvent.click(screen.getByRole('button', { name: '创建' }));

    await screen.findByText('首尔 01');
    const post = calls.find((c) => c.method === 'POST');
    expect(post?.path).toBe('/api/v1/admin/nodes');
    expect(post?.body).toMatchObject({
      name: '首尔 01',
      host: '203.0.113.99',
      port: 443,
      reason: '东京线路晚高峰丢包，加一台分流',
    });
  });

  it('端口越界时不许提交 —— 服务端会 422，但先在这里说清楚', async () => {
    const calls = stubApi(() => listResponse([node()]));
    renderPage();
    await screen.findByText('东京 01');

    fireEvent.click(screen.getByRole('button', { name: /新建节点/ }));
    fireEvent.change(screen.getByLabelText('名称'), { target: { value: '首尔 01' } });
    fireEvent.change(screen.getByLabelText('Host'), { target: { value: '203.0.113.99' } });
    fireEvent.change(screen.getByLabelText('端口'), { target: { value: '70000' } });
    fireEvent.change(screen.getByLabelText('操作原因（必填）'), {
      target: { value: '这里的原因足够长了' },
    });

    expect(screen.getByRole('button', { name: '创建' }).hasAttribute('disabled')).toBe(true);
    expect(calls.some((c) => c.method === 'POST')).toBe(false);
  });

  it('新建表单要说清楚「建出来是停用的、还没有密钥」', async () => {
    stubApi(() => listResponse([node()]));
    renderPage();
    await screen.findByText('东京 01');

    fireEvent.click(screen.getByRole('button', { name: /新建节点/ }));
    const card = screen.getByText('新建节点', { selector: 'h2,h3,p,span' }).closest('div');
    expect(within(card as HTMLElement).getByText(/默认是停用的/)).toBeTruthy();
  });
});

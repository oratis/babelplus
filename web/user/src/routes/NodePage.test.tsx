/**
 * 节点页的接线测试。
 *
 * 每个用例为什么必须存在：
 *
 *  1. 🔴 **不显示倍率** —— 这一页唯一一条会被「顺手补全」改错的规则。
 *     契约的 `UserNode` 里有 `multiplier_e9`，`@babelplus/shared` 里有现成的
 *     `formatMultiplier`，两样东西摆在眼前，加一列倍率是任何人打开这个文件后
 *     最自然的一个「补完」动作 —— 而 product-brief §6 定的是**不引入倍率**。
 *     加上去不会有任何报错，只会让用户开始比较哪条线路「更划算」，
 *     并在我们某天真的不引入倍率时觉得被骗了。这个用例是唯一会拦住那次提交的东西。
 *
 *  2. **空态不是「暂无数据」** —— §2.2：空态必须给出下一步动作。
 *     没有可用节点几乎总是我们这边的问题，所以要先替用户排除「是不是我账号坏了」。
 *
 *  3. **501 说「该功能尚未开放」** —— 501 按状态码归一成 5xx，
 *     只按状态码分支的实现会把「后端还没写」说成服务故障，并把人推去一个一切正常的状态页。
 *     这条是全仓点名的事故来源，每一页都要各自钉一次。
 *
 *  4. **5xx 必须说「已连接的设备不受影响」** —— 面板是控制面、节点是数据面。
 *     在**节点页**上把前者的故障说成后者，用户会以为线路全没了然后去申请退款
 *     （system-design §1 的控制面/数据面分离在 UI 上的落点）。
 *
 *  5. **按 `ErrorCode` 分支，不按 HTTP 状态码** —— 403 + `AUTH_PERMISSION_DENIED`
 *     是被封禁的账号（`middleware/user.go`），只按状态码分支会显示成
 *     全站默认的「没有访问权限」，而那句话会让用户去提工单问「为什么没权限」。
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { cleanup, render, screen, waitFor } from '@testing-library/react';
import { MemoryRouter } from 'react-router';
import { resetRuntimeConfig } from '@babelplus/shared';
import { resetApiClientForTests } from '../lib/api.ts';
import { resetSessionForTests } from '../lib/session.ts';
import NodePage from './NodePage.tsx';

const REQUEST_ID = '01K2NODENODENODENODENODENO';

function jsonResponse(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

function errorResponse(status: number, code: string, message: string): Response {
  return jsonResponse(status, { error: { code, message }, meta: { request_id: REQUEST_ID } });
}

function nodesResponse(nodes: unknown[]): Response {
  return jsonResponse(200, { data: nodes, meta: { request_id: REQUEST_ID } });
}

/**
 * 带倍率的节点。**倍率字段是刻意填上的** —— 它必须存在于响应里，
 * 用例才能证明「页面拿到了倍率但没有显示它」，而不是「响应里恰好没有」。
 */
const NODES = [
  { id: 1, name: '东京 01', region: '日本', type: 'vless', status: 'online', multiplier_e9: 2_000_000_000 },
  { id: 2, name: '洛杉矶 01', region: '美国', type: 'hysteria2', status: 'degraded', multiplier_e9: 1_000_000_000 },
  { id: 3, name: '新加坡 01', region: '新加坡', type: 'vless', status: 'offline' },
];

function stubNodes(response: () => Response): void {
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: string | URL | Request) => {
      const path = new URL(String(input)).pathname;
      if (path !== '/api/v1/user/nodes') throw new Error(`未预期的请求：${path}`);
      return response();
    }),
  );
}

function renderNodes() {
  return render(
    <MemoryRouter initialEntries={['/node']}>
      <NodePage />
    </MemoryRouter>,
  );
}

beforeEach(() => {
  resetRuntimeConfig();
  resetSessionForTests();
  resetApiClientForTests();
  window.sessionStorage.clear();
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe('NodePage', () => {
  it('成功：列出名称、地区与在线状态', async () => {
    stubNodes(() => nodesResponse(NODES));
    renderNodes();

    await waitFor(() => expect(screen.getByText('东京 01')).toBeTruthy());
    expect(screen.getByText('日本')).toBeTruthy();
    expect(screen.getByText('在线')).toBeTruthy();
    expect(screen.getByText('不稳定')).toBeTruthy();
    expect(screen.getByText('离线')).toBeTruthy();
    expect(screen.getByText('共 3 条线路 · 1 条在线')).toBeTruthy();
  });

  it('🔴 倍率既不显示成一列，也不以任何形态出现在页面上', async () => {
    stubNodes(() => nodesResponse(NODES));
    const { container } = renderNodes();

    await waitFor(() => expect(screen.getByText('东京 01')).toBeTruthy());
    const text = container.textContent ?? '';

    // 「倍率」这一列不存在 —— 不是「都显示 1x」，是根本没有这一列。
    expect(text).not.toContain('倍率');
    // 原始值、常见的渲染形态（2x / ×2 / 2.0）一个都不许出现。
    expect(text).not.toContain('2000000000');
    expect(text).not.toMatch(/[x×]\s*[12](\.0)?\b/i);
    expect(text).not.toMatch(/[12](\.0)?\s*[x×]/i);
    // 协议名同理：契约里有 `type`，但用户看了只会误判「哪个协议更快」。
    expect(text).not.toContain('vless');
    expect(text).not.toContain('hysteria2');
  });

  it('空：给「跑一遍诊断」而不是「暂无数据」，并说明这多半不是账号的问题', async () => {
    stubNodes(() => nodesResponse([]));
    renderNodes();

    await waitFor(() => expect(screen.getByText('当前没有可用节点')).toBeTruthy());
    expect(screen.getByText('跑一遍诊断')).toBeTruthy();
    expect(screen.getByText(/这通常是我们这边的问题，不是你的账号/)).toBeTruthy();
  });

  it('501 NOT_IMPLEMENTED：说「该功能尚未开放」，不说服务故障、不推状态页', async () => {
    stubNodes(() => errorResponse(501, 'NOT_IMPLEMENTED', '尚未实现'));
    const { container } = renderNodes();

    await waitFor(() => expect(screen.getByText('该功能尚未开放')).toBeTruthy());
    const text = container.textContent ?? '';
    expect(text).not.toContain('我们这边出了问题');
    expect(text).not.toContain('查看状态页');
    expect(text).toContain(REQUEST_ID);
  });

  it('500：说「已连接的设备不受影响」，不能只说加载失败', async () => {
    stubNodes(() => errorResponse(500, 'INTERNAL_ERROR', '内部错误'));
    renderNodes();

    await waitFor(() => expect(screen.getByText('读不到节点列表')).toBeTruthy());
    // 页面自己那句（`ErrorState` 的全站兜底里也有一句类似的，所以这里挑本页特有的措辞）。
    expect(screen.getByText(/线路本身没有变化，已连接的设备不受影响/)).toBeTruthy();
    // 501 与 500 的 kind 都是 server，所以这里反过来钉一次：500 **不该**被说成「尚未开放」。
    expect(screen.queryByText('该功能尚未开放')).toBeNull();
  });

  it('403 AUTH_PERMISSION_DENIED（封禁）：说封禁，不说「没有访问权限」', async () => {
    stubNodes(() => errorResponse(403, 'AUTH_PERMISSION_DENIED', '账号已被封禁'));
    const { container } = renderNodes();

    await waitFor(() => expect(screen.getByText('这个账号已被封禁')).toBeTruthy());
    expect(container.textContent ?? '').not.toContain('没有访问权限');
  });
});

/**
 * 订阅页的接线测试。本项目最重的一页，所以钉得最细。
 *
 * 每个用例为什么必须存在：
 *
 *  1. 🔴 **订阅接口失败时从本地缓存回显链接** —— §3.2.3 错态的原话，
 *     也是这一页最贵的一条：用户来这一页十有八九就是为了复制那条链接，
 *     读不到就直接给一个错误框是最糟的实现。同一个用例还钉住**三态互相独立**：
 *     订阅挂了，设备列表照样要显示出来。把三个请求合成一个整页 loading 是最省事的写法，
 *     它的代价恰恰是「任意一个 5xx 都会把订阅链接一起藏掉」。
 *
 *  2. 🔴 **`device_limit = 0` 是「不限设备」不是「0 台」** —— 契约的 int32 装不下「不限」，
 *     后端只能填 0（`usersub.go` 的 `subscriptionSummaryView` 注释）。
 *     照字面渲染成「0 台」会告诉一个不限设备的用户「你一台都不能连」，
 *     并且会把他判成「已达上限」。这是一条**只在契约注释里存在**的规则，
 *     没有类型能拦住它，只有这个用例能。
 *
 *  3. 🔴 **达到上限用信息色，不是红色报错**（§3.2.3 第 3 条）——
 *     那是升档转化位不是错误提示。所以断言的是**颜色与语义**（accent / 无 `role="alert"`），
 *     不只是文案：把它改成 `danger` 时文案往往一个字都不用动。
 *
 *  4. 🔴 **设备数文案不许出现 ADR 0015 §6.2 点名禁止的三句** ——
 *     设备数是软限制（节点拿不到 alivelist 时静默失败开放，§6.1），
 *     我们**证明不了**它执行过，所以只能承诺容量。「严格限制 / 超出将被封禁 /
 *     系统会自动踢下线」是最顺手也最容易被加回来的三句话。
 *
 *  5. 🔴 **「重置订阅」必须勾选确认，且后果说得准** —— 它只作废订阅链接，
 *     不断开已有连接（`revokeAllSubscriptionTokens` 只写 `users.sub_revoked_at`）。
 *     写成「所有设备立刻掉线」会让用户以为按下去就断网，发现设备还在跑之后
 *     认定「没生效」并反复点。用例同时钉住：**勾选之前一个请求都不发**。
 *
 *  6. **「全部下线」必须显示生效延迟** —— 契约明写响应必须带
 *     `effective_within_seconds`，理由是配置下发为 60 秒轮询；
 *     不显示它，用户会连点五次然后开工单（user-journey §12.2）。
 *
 *  7. **两个空态含义不同，要分开**：「还没有设备连接过」（客户端没连上）
 *     与「还没有客户端拉过订阅」（客户端根本没配对）——下一步完全不同。
 *
 *  8. **501 说「该功能尚未开放」**，且不影响同页其它区块。
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { MemoryRouter } from 'react-router';
import { resetRuntimeConfig } from '@babelplus/shared';
import { AuthProvider } from '../lib/auth.tsx';
import { resetApiClientForTests } from '../lib/api.ts';
import { ACCESS_TOKEN_KEY, resetSessionForTests } from '../lib/session.ts';
import SubscribePage from './SubscribePage.tsx';

const REQUEST_ID = '01K2SUBSUBSUBSUBSUBSUBSUBS';

/** 链接里的 token 部分。用例全程盯着它：打码状态下它一个字符都不许完整出现。 */
const TOKEN = 'TOKENSECRETVALUE0123456789';
const SUB_URL = `https://api.example.test/s/${TOKEN}`;

function jsonResponse(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

function errorResponse(status: number, code: string, message: string): Response {
  return jsonResponse(status, { error: { code, message }, meta: { request_id: REQUEST_ID } });
}

const CURRENT_USER = {
  data: {
    id: 42,
    email: 'user@example.com',
    banned: false,
    created_at: '2026-08-01T00:00:00Z',
    balance_amount: 0,
  },
  meta: { request_id: REQUEST_ID },
};

function summary(overrides: Record<string, unknown> = {}) {
  return {
    plan_name: '标准 · 月付',
    upload_bytes: 1_000_000,
    download_bytes: 2_000_000,
    total_bytes: 100_000_000_000,
    expired_at: '2026-12-01T00:00:00Z',
    device_count: 2,
    device_limit: 5,
    ...overrides,
  };
}

function subscriptionResponse(overrides: Record<string, unknown> = {}, urls?: Record<string, unknown>) {
  return jsonResponse(200, {
    data: {
      urls: urls ?? {
        short: SUB_URL,
        long: `https://api.example.test/api/v1/client/subscribe?token=${TOKEN}`,
        clash: `https://api.example.test/api/v1/client/subscribe?token=${TOKEN}&flag=clash`,
      },
      summary: summary(overrides),
    },
    meta: { request_id: REQUEST_ID },
  });
}

const DEVICES = [
  { id: 1, ip: '203.0.113.7', node_name: '东京 01', last_seen_at: '2026-08-29T11:00:00Z' },
  { id: 2, ip: '198.51.100.22', last_seen_at: '2026-08-29T10:30:00Z', first_seen_at: '2026-08-28T10:00:00Z' },
];

const FETCH_LOG = [
  {
    id: 1,
    request_at: '2026-08-29T11:00:00Z',
    request_ip: '203.0.113.7',
    user_agent: 'Karing/1.2 sing-box',
    format: 'clash',
  },
];

interface Call {
  method: string;
  path: string;
}

interface Stubs {
  subscription?: () => Response;
  devices?: () => Response;
  fetchLog?: () => Response;
  kick?: () => Response;
  kickAll?: () => Response;
  revokeAll?: () => Response;
}

function stubSubscribe(stubs: Stubs): { calls: Call[] } {
  const calls: Call[] = [];
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: string | URL | Request, init?: RequestInit) => {
      const url = new URL(String(input));
      const method = (init?.method ?? 'GET').toUpperCase();
      calls.push({ method, path: url.pathname });

      switch (url.pathname) {
        case '/api/v1/user/me':
          return jsonResponse(200, CURRENT_USER);
        case '/api/v1/user/subscription':
          return (stubs.subscription ?? (() => subscriptionResponse()))();
        case '/api/v1/user/subscription/fetch-log':
          return (stubs.fetchLog ??
            (() => jsonResponse(200, { data: FETCH_LOG, meta: { request_id: REQUEST_ID } })))();
        case '/api/v1/user/subscription/revoke-all':
          if (!stubs.revokeAll) throw new Error('这个用例不该发出全撤请求');
          return stubs.revokeAll();
        case '/api/v1/user/devices':
          if (method === 'DELETE') {
            if (!stubs.kickAll) throw new Error('这个用例不该发出全部下线请求');
            return stubs.kickAll();
          }
          return (stubs.devices ?? (() => jsonResponse(200, { data: DEVICES, meta: { request_id: REQUEST_ID } })))();
        default:
          break;
      }
      if (/^\/api\/v1\/user\/devices\/\d+$/.test(url.pathname) && method === 'DELETE') {
        if (!stubs.kick) throw new Error('这个用例不该发出踢下线请求');
        return stubs.kick();
      }
      throw new Error(`未预期的请求：${method} ${url.pathname}`);
    }),
  );
  return { calls };
}

function renderSubscribe() {
  return render(
    <MemoryRouter initialEntries={['/subscribe']}>
      <AuthProvider>
        <SubscribePage />
      </AuthProvider>
    </MemoryRouter>,
  );
}

function subscribeUrlText(): string {
  return screen.getByTestId('subscribe-url').textContent ?? '';
}

beforeEach(() => {
  resetRuntimeConfig();
  resetSessionForTests();
  resetApiClientForTests();
  window.sessionStorage.clear();
  window.localStorage.clear();
  // 有 token 才会去取 /me，页面才拿得到用户 id（缓存按用户隔离要用它）。
  window.sessionStorage.setItem(ACCESS_TOKEN_KEY, 'test-token');
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe('SubscribePage', () => {
  it('成功：链接默认打码但域名可见，点「显示明文」才露出 token', async () => {
    stubSubscribe({});
    renderSubscribe();

    await waitFor(() => expect(screen.getByTestId('subscribe-url')).toBeTruthy());
    const masked = subscribeUrlText();
    // 打的是 token，不是域名 —— 失联轮换域名时，「我在用哪个镜像」是第一个要问的问题。
    expect(masked).toContain('api.example.test');
    expect(masked).not.toContain(TOKEN);

    fireEvent.click(screen.getByText('显示明文'));
    expect(subscribeUrlText()).toBe(SUB_URL);

    // 设备与拉取记录各自渲染出来了。
    // 用 198.51.100.22 作判据：203.0.113.7 在设备行与拉取记录里各出现一次。
    expect(screen.getByText('198.51.100.22')).toBeTruthy();
    expect(screen.getByText('2 / 5')).toBeTruthy();
    expect(screen.getByText('Karing/1.2 sing-box')).toBeTruthy();
  });

  it('只渲染后端真的给了的格式，不自己拼 ?flag=singbox_profile', async () => {
    stubSubscribe({});
    const { container } = renderSubscribe();

    await waitFor(() => expect(screen.getByTestId('subscribe-url')).toBeTruthy());
    expect(screen.getByText('Clash YAML')).toBeTruthy();
    // 响应里没有 singbox / base64，页面就不该凭空长出这两个按钮。
    expect(screen.queryByText('sing-box 节点清单')).toBeNull();
    expect(screen.queryByText('base64 分享链接')).toBeNull();
    // 🔴 完整配置的闸门没开：这条 flag 一个字符都不许出现在页面上（ADR 0015 裁决 ②）。
    expect(container.textContent ?? '').not.toContain('singbox_profile');
  });

  it('🔴 订阅接口 5xx：从本地缓存回显链接并标注可能过期，设备列表照常显示', async () => {
    // 第一趟：读成功，把链接写进缓存。
    stubSubscribe({});
    renderSubscribe();
    await waitFor(() => expect(screen.getByTestId('subscribe-url')).toBeTruthy());
    cleanup();
    vi.unstubAllGlobals();

    // 第二趟：订阅接口挂了，但设备与拉取记录都正常。
    stubSubscribe({ subscription: () => errorResponse(500, 'INTERNAL_ERROR', '内部错误') });
    renderSubscribe();

    await waitFor(() => expect(screen.getByTestId('subscribe-url')).toBeTruthy());
    expect(screen.getByText(/以下是这台设备上次读到的链接，可能已经过期/)).toBeTruthy();
    expect(subscribeUrlText()).toContain('api.example.test');

    // 🔴 三态独立：订阅那一个请求挂了，设备列表**照样**要在。
    expect(screen.getByText('198.51.100.22')).toBeTruthy();
    expect(screen.getAllByText('203.0.113.7').length).toBeGreaterThanOrEqual(1);
  });

  it('订阅接口 5xx 且没有缓存：如实说读不到，并说明已连接的设备不受影响', async () => {
    stubSubscribe({ subscription: () => errorResponse(500, 'INTERNAL_ERROR', '内部错误') });
    renderSubscribe();

    await waitFor(() => expect(screen.getByText('暂时读不到订阅链接')).toBeTruthy());
    expect(screen.getByText(/订阅本身没有变化，已连接的设备不受影响/)).toBeTruthy();
    // 设备区不受影响。
    expect(screen.getByText('198.51.100.22')).toBeTruthy();
  });

  it('501 NOT_IMPLEMENTED：说「该功能尚未开放」，同页其它区块照常', async () => {
    stubSubscribe({ subscription: () => errorResponse(501, 'NOT_IMPLEMENTED', '尚未实现') });
    const { container } = renderSubscribe();

    await waitFor(() => expect(screen.getByText('该功能尚未开放')).toBeTruthy());
    expect(container.textContent ?? '').not.toContain('我们这边出了问题');
    expect(screen.getByText('198.51.100.22')).toBeTruthy();
  });

  it('🔴 device_limit = 0 渲染成「不限」，且不判成已达上限', async () => {
    stubSubscribe({ subscription: () => subscriptionResponse({ device_limit: 0 }) });
    const { container } = renderSubscribe();

    await waitFor(() => expect(screen.getByText('2 / 不限')).toBeTruthy());
    expect(container.textContent ?? '').not.toContain('2 / 0');
    expect(screen.queryByTestId('device-limit-notice')).toBeNull();
  });

  it('🔴 达到上限：信息色的升档引导，不是红色报错', async () => {
    stubSubscribe({ subscription: () => subscriptionResponse({ device_limit: 2 }) });
    renderSubscribe();

    await waitFor(() => expect(screen.getByTestId('device-limit-notice')).toBeTruthy());
    const notice = screen.getByTestId('device-limit-notice');
    expect(notice.className).toContain('accent');
    // 改成 danger 时文案往往一个字都不用动，所以这里断言的是颜色与语义本身。
    expect(notice.className).not.toContain('danger');
    expect(notice.getAttribute('role')).toBeNull();
    expect(notice.textContent).toContain('升级套餐');
  });

  it('🔴 设备数文案是容量承诺：ADR 0015 §6.2 禁止的说法一句都不出现', async () => {
    stubSubscribe({});
    const { container } = renderSubscribe();

    await waitFor(() => expect(screen.getByText('198.51.100.22')).toBeTruthy());
    const text = container.textContent ?? '';

    expect(text).toContain('这里数的是出口 IP，不是设备。');
    expect(text).toContain('同一个路由器后面的多台设备算作 1 台');
    // 我们证明不了它执行过（拿不到 alivelist 时静默失败开放），所以不许承诺执行。
    expect(text).not.toContain('严格限制');
    expect(text).not.toContain('超出将被封禁');
    expect(text).not.toContain('系统会自动踢下线');
    // 反过来也不许自曝实现缺陷 —— 那是在教人钻空子。
    expect(text).not.toContain('可能不生效');
  });

  it('全部下线：确认后发 DELETE，并把生效延迟（60 秒）显示出来', async () => {
    const { calls } = stubSubscribe({
      kickAll: () =>
        jsonResponse(200, {
          data: { removed: 2, effective_within_seconds: 60 },
          meta: { request_id: REQUEST_ID },
        }),
    });
    renderSubscribe();

    await waitFor(() => expect(screen.getByText('198.51.100.22')).toBeTruthy());
    fireEvent.click(screen.getByText('全部下线'));

    expect(screen.getByRole('dialog').textContent).toContain('订阅链接不受影响');
    expect(calls.filter((c) => c.method === 'DELETE')).toHaveLength(0);

    fireEvent.click(screen.getByText('确认全部下线'));
    // 用「已请求下线 N 个连接」限定：口径说明里也有一句「最长 60 秒生效」。
    await waitFor(() => expect(screen.getByText(/已请求下线 2 个连接，最长 60 秒生效/)).toBeTruthy());
    expect(calls.filter((c) => c.method === 'DELETE' && c.path === '/api/v1/user/devices')).toHaveLength(1);
  });

  it('🔴 重置订阅：必须勾选后才可点，且后果不说成「立刻掉线」', async () => {
    const { calls } = stubSubscribe({
      revokeAll: () =>
        jsonResponse(200, {
          data: { revoked: 1, sub_revoked_at: '2026-08-29T12:00:00Z' },
          meta: { request_id: REQUEST_ID },
        }),
      // 重置之后后端不再有有效 token，`urls.short` 变成空串（`subscriptionURLsFor`）。
      subscription: () => subscriptionResponse({}, { short: '', long: '' }),
    });
    renderSubscribe();

    await waitFor(() => expect(screen.getByText('重置订阅')).toBeTruthy());
    fireEvent.click(screen.getByText('重置订阅'));

    const dialog = screen.getByRole('dialog');
    expect(dialog.textContent).toContain('当前所有订阅链接立即失效');
    // 真正让设备下线的是「全部下线」，这里必须把两件事分清楚。
    expect(dialog.textContent).toContain('已经建立的连接不会因此断开');
    expect(dialog.textContent).not.toContain('立刻掉线');

    const confirmButton = screen.getByText('确认重置订阅') as HTMLButtonElement;
    expect(confirmButton.disabled).toBe(true);
    fireEvent.click(confirmButton);
    expect(calls.filter((c) => c.path === '/api/v1/user/subscription/revoke-all')).toHaveLength(0);

    fireEvent.click(screen.getByLabelText('我明白上面的后果'));
    fireEvent.click(screen.getByText('确认重置订阅'));

    await waitFor(() =>
      expect(calls.filter((c) => c.path === '/api/v1/user/subscription/revoke-all')).toHaveLength(1),
    );
    // 撤销后没有可用链接是**预期结果**，要说清楚并给出下一步，而不是显示成故障。
    await waitFor(() => expect(screen.getByText('现在没有可用的订阅链接')).toBeTruthy());
    expect(screen.getByText(/新建一条订阅链接/)).toBeTruthy();
    // 缓存里那条已经必然 404 的链接也被清掉了。
    expect(JSON.stringify(window.sessionStorage)).not.toContain(TOKEN);
  });

  it('两个空态含义不同，分开显示', async () => {
    stubSubscribe({
      devices: () => jsonResponse(200, { data: [], meta: { request_id: REQUEST_ID } }),
      fetchLog: () => jsonResponse(200, { data: [], meta: { request_id: REQUEST_ID } }),
    });
    renderSubscribe();

    await waitFor(() => expect(screen.getByText('还没有设备连接过')).toBeTruthy());
    expect(screen.getByText(/还没有任何客户端用这条链接拉过配置/)).toBeTruthy();
  });

  it('403 AUTH_PERMISSION_DENIED（封禁）：说封禁，不说「没有访问权限」', async () => {
    stubSubscribe({
      subscription: () => errorResponse(403, 'AUTH_PERMISSION_DENIED', '账号已被封禁'),
    });
    const { container } = renderSubscribe();

    await waitFor(() => expect(screen.getByText('这个账号已被封禁')).toBeTruthy());
    expect(container.textContent ?? '').not.toContain('没有访问权限');
  });
});

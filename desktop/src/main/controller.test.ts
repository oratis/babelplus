/**
 * 状态机的用例。fetch 用 `vi.stubGlobal` 的假服务端，内核用假 spawn，
 * Electron 的两处（applyProxy / clearProxy）是注入的回调 —— 所以整条链在 Node 里跑得完。
 * 每条对应 controller.ts 文件头列的一条产品规则。
 */
import { mkdtemp, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { Api } from './api.ts';
import { Controller } from './controller.ts';
import { Core, type SpawnedProcess } from './core.ts';
import { Store } from './store.ts';

const API = 'https://api.example.invalid';
const SUB_URL = 'https://api.example.invalid/s/tok?flag=singbox';
const GB = 1024 ** 3;

function subscriptionBody(nodes = 2): string {
  const tags = ['HK-1 · REALITY', 'HK-1 · HY2'].slice(0, nodes);
  return JSON.stringify({
    log: { level: 'warn' },
    outbounds: [
      { type: 'selector', tag: 'babel.plus', outbounds: tags, default: tags[0] },
      ...tags.map((t) => ({ type: 'vless', tag: t, server: '203.0.113.10', server_port: 443, uuid: 'u' })),
    ],
    route: { final: 'babel.plus' },
  });
}

interface Server {
  summary: { upload_bytes: number; download_bytes: number; total_bytes: number; expired_at: string | null; device_count: number; device_limit: number };
  subNodes: number;
  subStatus: number;
  calls: string[];
}

function fakeServer(): Server {
  const s: Server = {
    summary: { upload_bytes: 1 * GB, download_bytes: 11 * GB, total_bytes: 20 * GB, expired_at: '2026-10-01T00:00:00Z', device_count: 1, device_limit: 3 },
    subNodes: 2,
    subStatus: 200,
    calls: [],
  };
  const envelope = (data: unknown) =>
    new Response(JSON.stringify({ data, meta: { request_id: '01J' } }), {
      status: 200,
      headers: { 'Content-Type': 'application/json' },
    });
  vi.stubGlobal('fetch', async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = new URL(String(input));
    s.calls.push(`${init?.method ?? 'GET'} ${url.pathname}`);
    if (url.pathname === '/api/v1/auth/login') {
      const body = JSON.parse(String(init?.body)) as { password: string };
      if (body.password !== 'correct') {
        return new Response(JSON.stringify({ error: { code: 'AUTH_INVALID_CREDENTIALS', message: '邮箱或密码不正确' } }), {
          status: 401,
          headers: { 'Content-Type': 'application/json' },
        });
      }
      return envelope({ access_token: 'tok-1', refresh_token: 'tok-1', token_type: 'Bearer', expires_in: 900 });
    }
    if (url.pathname === '/api/v1/auth/logout') return new Response(null, { status: 204 });
    if (url.pathname === '/api/v1/user/subscription') {
      return envelope({ urls: { short: 'https://x', long: 'https://y', singbox: SUB_URL }, summary: s.summary });
    }
    if (url.pathname === '/s/tok') {
      if (s.subStatus !== 200) return new Response('nope', { status: s.subStatus });
      return new Response(subscriptionBody(s.subNodes), { status: 200 });
    }
    return new Response(JSON.stringify({ error: { code: 'RESOURCE_NOT_FOUND', message: 'no' } }), { status: 404, headers: { 'Content-Type': 'application/json' } });
  });
  return s;
}

interface Harness {
  controller: Controller;
  store: Store;
  proxied: number[];
  cleared: number;
  runProcs: { kill(): void; exit(code: number | null): void }[];
}

async function harness(dir: string, opts: { checkExit?: number; portOpens?: boolean } = {}): Promise<Harness> {
  const store = new Store(dir);
  await store.load();
  const api = new Api({
    baseUrls: [API],
    getToken: () => store.current.token,
    setToken: (token) => {
      void store.update({ token });
    },
  });
  const runProcs: Harness['runProcs'] = [];
  const spawnImpl = (_bin: string, args: readonly string[]): SpawnedProcess => {
    let exitCb: ((c: number | null, s: string | null) => void) | null = null;
    let stderrCb: ((l: string) => void) | null = null;
    const p = {
      pid: 1,
      kill: () => exitCb?.(0, 'SIGTERM'),
      onExit: (cb: (c: number | null, s: string | null) => void) => {
        exitCb = cb;
      },
      onStderr: (cb: (l: string) => void) => {
        stderrCb = cb;
      },
    };
    if (args[0] === 'check') {
      queueMicrotask(() => {
        if (opts.checkExit) stderrCb?.('FATAL: bad config');
        exitCb?.(opts.checkExit ?? 0, null);
      });
    } else {
      runProcs.push({ kill: p.kill, exit: (code) => exitCb?.(code, null) });
    }
    return p;
  };
  const core = new Core({
    binary: 'sing-box',
    spawnImpl,
    waitImpl: async () => opts.portOpens !== false,
    workDir: dir,
    maxRestarts: 1,
  });
  const proxied: number[] = [];
  let cleared = 0;
  const controller = new Controller({
    api,
    store,
    core,
    applyProxy: async (port) => {
      proxied.push(port);
    },
    clearProxy: async () => {
      cleared += 1;
    },
    controlPlaneHosts: ['api.example.invalid'],
  });
  // 内核事件接回状态机（真实接线在 main/index.ts 里做同样的事）。
  (core as unknown as { opts: { onEvent?: (e: unknown) => void } }).opts.onEvent = (e) =>
    controller.handleCoreEvent(e as never);
  return {
    controller,
    store,
    proxied,
    get cleared() {
      return cleared;
    },
    runProcs,
  } as Harness;
}

let dir: string;
let server: Server;
beforeEach(async () => {
  dir = await mkdtemp(join(tmpdir(), 'bp-ctrl-'));
  server = fakeServer();
});
afterEach(async () => {
  vi.unstubAllGlobals();
  await rm(dir, { recursive: true, force: true });
});

describe('登录与订阅', () => {
  it('登录后拿到配额与订阅链接，token 落盘', async () => {
    const h = await harness(dir);
    await h.controller.signIn('a@example.invalid', 'correct');
    expect(h.controller.signedIn).toBe(true);
    expect(h.controller.subscriptionSummary?.total_bytes).toBe(20 * GB);
    expect(h.store.current.token).toBe('tok-1');
    expect(server.calls).toContain('GET /api/v1/user/subscription');
  });

  it('密码错就是密码错，不落 token', async () => {
    const h = await harness(dir);
    await expect(h.controller.signIn('a@example.invalid', 'wrong')).rejects.toMatchObject({ code: 'AUTH_INVALID_CREDENTIALS' });
    expect(h.controller.signedIn).toBe(false);
  });
});

describe('connect', () => {
  it('顺路径：起内核 → 端口通 → 才设代理，状态 on', async () => {
    const h = await harness(dir);
    await h.controller.signIn('a@example.invalid', 'correct');
    await h.controller.connect();
    expect(h.controller.connection.status).toBe('on');
    expect(h.controller.connection.outbound).toBe('babel.plus');
    expect(h.proxied).toHaveLength(1);
    expect(h.proxied[0]).toBe(h.controller.connection.port);
    expect(h.store.current.onboarded).toBe(true);
  });

  it('🔴 sing-box check 不过 → failed 且**一次代理都没设**', async () => {
    const h = await harness(dir, { checkExit: 1 });
    await h.controller.signIn('a@example.invalid', 'correct');
    await h.controller.connect();
    expect(h.controller.connection).toMatchObject({ status: 'failed', lastError: { reason: 'config-rejected' } });
    expect(h.proxied).toHaveLength(0);
  });

  it('端口没开 → failed，不设代理', async () => {
    const h = await harness(dir, { portOpens: false });
    await h.controller.signIn('a@example.invalid', 'correct');
    await h.controller.connect();
    expect(h.controller.connection).toMatchObject({ status: 'failed', lastError: { reason: 'port-unavailable' } });
    expect(h.proxied).toHaveLength(0);
  });

  it('订阅里没有节点 = 账号状态，说人话而不是「解析失败」', async () => {
    server.subNodes = 0;
    const h = await harness(dir);
    await h.controller.signIn('a@example.invalid', 'correct');
    await h.controller.connect();
    expect(h.controller.connection).toMatchObject({ status: 'failed', lastError: { reason: 'subscription-empty' } });
    expect(h.controller.connection.lastError?.detail).toMatch(/到期|被封|配额/);
  });

  it('配额已用尽时**不连**，直接说明原因', async () => {
    server.summary = { ...server.summary, download_bytes: 19 * GB };
    const h = await harness(dir);
    await h.controller.signIn('a@example.invalid', 'correct');
    await h.controller.connect();
    expect(h.controller.connection.status).toBe('failed');
    expect(h.controller.connection.lastError?.detail).toContain('用尽');
    expect(h.proxied).toHaveLength(0);
  });

  it('未登录不能连', async () => {
    const h = await harness(dir);
    await h.controller.connect();
    expect(h.controller.connection).toMatchObject({ status: 'failed', lastError: { reason: 'not-signed-in' } });
  });
});

describe('崩溃与断开', () => {
  it('🔴 内核崩了进 degraded，**代理不撤** —— 撤掉就是静默直连', async () => {
    const h = await harness(dir);
    await h.controller.signIn('a@example.invalid', 'correct');
    await h.controller.connect();
    const clearedBefore = h.cleared;
    h.runProcs.at(-1)?.exit(1);
    await new Promise((r) => setTimeout(r, 10));
    expect(h.controller.connection.status).toBe('degraded');
    expect(h.controller.connection.lastError?.reason).toBe('core-crashed');
    expect(h.cleared).toBe(clearedBefore);
  });

  it('用户主动断开才撤代理', async () => {
    const h = await harness(dir);
    await h.controller.signIn('a@example.invalid', 'correct');
    await h.controller.connect();
    await h.controller.disconnect();
    expect(h.cleared).toBeGreaterThan(0);
    expect(h.controller.connection.status).toBe('off');
  });
});

describe('tick 与偏好', () => {
  it('配额在运行中用尽 → 主动断开并说明', async () => {
    const h = await harness(dir);
    await h.controller.signIn('a@example.invalid', 'correct');
    await h.controller.connect();
    server.summary = { ...server.summary, download_bytes: 19 * GB };
    await h.controller.tick();
    expect(h.controller.connection.status).toBe('failed');
    expect(h.cleared).toBeGreaterThan(0);
  });

  it('改路由偏好会重建配置并重连（sing-box 不支持热改路由）', async () => {
    const h = await harness(dir);
    await h.controller.signIn('a@example.invalid', 'correct');
    await h.controller.connect();
    const before = h.proxied.length;
    await h.controller.setPrefs({ mode: 'everything' });
    expect(h.proxied.length).toBe(before + 1);
    expect(h.controller.prefs.mode).toBe('everything');
  });

  it('接受「这个站点被屏蔽」提示 → 主机进 alwaysProxy 并规整', async () => {
    const h = await harness(dir);
    await h.controller.signIn('a@example.invalid', 'correct');
    await h.controller.routeHost('https://WWW.Example.invalid/x');
    expect(h.controller.prefs.alwaysProxy).toEqual(['www.example.invalid']);
  });

  it('不影响路由的偏好（开机自启）不重连', async () => {
    const h = await harness(dir);
    await h.controller.signIn('a@example.invalid', 'correct');
    await h.controller.connect();
    const before = h.proxied.length;
    await h.controller.setPrefs({ launchAtStart: true });
    expect(h.proxied.length).toBe(before);
  });
});

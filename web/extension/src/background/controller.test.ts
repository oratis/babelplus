/**
 * 状态机的用例。storage / proxy / badge / alarms 全部是内存实现，网络用 `vi.stubGlobal('fetch')` 的假服务端。
 * 每条用例对应 controller.ts 头部列出的一条产品规则。
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { Snapshot, ProxyConfig, SubscriptionSummary } from '../shared/types.ts';
import { ALARM_CONFIG, ALARM_REFRESH, Controller, type ControllerDeps } from './controller.ts';
import { memoryProxyPort, type MemoryProxyPort } from './proxy.ts';
import { KEY, memoryStorage } from './storage.ts';

const GB = 1024 ** 3;
const API = 'https://api.example.invalid';

function envelope(data: unknown, status = 200): Response {
  return new Response(JSON.stringify({ data, meta: { request_id: '01J' } }), {
    status,
    headers: { 'Content-Type': 'application/json', 'X-Request-Id': '01J' },
  });
}
function errorEnvelope(status: number, code: string, message = code): Response {
  return new Response(JSON.stringify({ error: { code, message }, meta: { request_id: '01J' } }), {
    status,
    headers: { 'Content-Type': 'application/json', 'X-Request-Id': '01J' },
  });
}

function config(over: Partial<ProxyConfig> = {}): ProxyConfig {
  return {
    endpoints: [
      { id: 1, host: 'a.example.invalid', port: 443, scheme: 'https', region: 'HK', label: 'Hong Kong A', auth: { username: 'ua', password: 'pa' }, probe_url: 'https://probe.example.invalid/ip?ep=1' },
      { id: 2, host: 'b.example.invalid', port: 443, scheme: 'https', region: 'HK', label: 'Hong Kong B', auth: { username: 'ub', password: 'pb' }, probe_url: 'https://probe.example.invalid/ip?ep=2' },
      { id: 3, host: 'j.example.invalid', port: 443, scheme: 'https', region: 'JP', label: 'Tokyo', auth: { username: 'uj', password: 'pj' }, probe_url: 'https://probe.example.invalid/ip?ep=3' },
    ],
    rules: { direct_suffixes: ['cn'], proxy_suffixes: [] },
    rules_rev: 7,
    expires_in: 1800,
    control_plane: { api_base_urls: [API, 'https://api2.example.invalid'], web_base_url: 'https://web.example.invalid', backup_page_url: 'https://backup.example.invalid/' },
    ...over,
  };
}

function summary(over: Partial<SubscriptionSummary> = {}): SubscriptionSummary {
  return { plan_name: 'Standard', upload_bytes: 1 * GB, download_bytes: 11 * GB, total_bytes: 20 * GB, expired_at: '2026-10-01T00:00:00Z', device_count: 1, device_limit: 3, ...over };
}

interface FakeServer {
  readonly calls: string[];
  configResponse: () => Response;
  subscription: SubscriptionSummary;
  /** ep → 延迟 ms；-1 = 失败 */
  probeLatency: Record<string, number>;
  clock: number;
}

function fakeServer(): FakeServer {
  const server: FakeServer = {
    calls: [],
    configResponse: () => envelope(config()),
    subscription: summary(),
    probeLatency: { '1': 40, '2': 20, '3': -1 },
    clock: Date.parse('2026-09-02T10:00:00Z'),
  };
  vi.stubGlobal('fetch', async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = new URL(typeof input === 'string' ? input : input instanceof URL ? input.toString() : input.url);
    const method = init?.method ?? 'GET';
    server.calls.push(`${method} ${url.pathname}`);
    if (url.hostname === 'probe.example.invalid') {
      const ep = url.searchParams.get('ep') ?? '';
      const latency = server.probeLatency[ep] ?? -1;
      if (latency < 0) throw new TypeError('Failed to fetch');
      server.clock += latency;
      return new Response(JSON.stringify({ ip: `203.0.113.${ep}` }), { headers: { 'Content-Type': 'application/json' } });
    }
    if (url.pathname === '/api/v1/auth/login') {
      const raw = init?.body;
      const text = typeof raw === 'string' ? raw : new TextDecoder().decode(raw as ArrayBuffer);
      const body = JSON.parse(text) as { email: string; password: string };
      if (body.password !== 'correct') return errorEnvelope(401, 'AUTH_INVALID_CREDENTIALS', '邮箱或密码不正确');
      return envelope({ access_token: 'tok-1', refresh_token: 'tok-1', token_type: 'Bearer', expires_in: 900 });
    }
    if (url.pathname === '/api/v1/auth/logout') return new Response(null, { status: 204 });
    const auth = new Headers(init?.headers).get('Authorization');
    if (auth !== 'Bearer tok-1') return errorEnvelope(401, 'AUTH_TOKEN_INVALID');
    if (url.pathname === '/api/v1/user/subscription') return envelope({ urls: { short: 'https://x', long: 'https://y' }, summary: server.subscription });
    if (url.pathname === '/api/v1/user/proxy-config') return server.configResponse();
    return errorEnvelope(404, 'RESOURCE_NOT_FOUND');
  });
  return server;
}

interface Harness {
  readonly controller: Controller;
  readonly proxy: MemoryProxyPort;
  readonly local: ReturnType<typeof memoryStorage>;
  readonly session: ReturnType<typeof memoryStorage>;
  readonly badge: { text: string; color: string };
  readonly alarms: Map<string, { periodInMinutes?: number; delayInMinutes?: number }>;
  readonly broadcasts: Snapshot[];
  readonly opened: string[];
}

function harness(server: FakeServer, over: Partial<{ local: ReturnType<typeof memoryStorage>; session: ReturnType<typeof memoryStorage>; proxy: MemoryProxyPort }> = {}): Harness {
  const local = over.local ?? memoryStorage();
  const session = over.session ?? memoryStorage();
  const proxy = over.proxy ?? memoryProxyPort();
  const badge = { text: '', color: '' };
  const alarms = new Map<string, { periodInMinutes?: number; delayInMinutes?: number }>();
  const broadcasts: Snapshot[] = [];
  const opened: string[] = [];
  const deps: ControllerDeps = {
    local,
    session,
    proxy,
    badge: {
      async set(text, color) {
        badge.text = text;
        badge.color = color;
      },
    },
    alarms: {
      async create(name, info) {
        alarms.set(name, info);
      },
      async clear(name) {
        alarms.delete(name);
      },
    },
    env: { version: '0.1.0-test', apiBaseUrls: [API], webUrl: '', backupPageUrl: '', helpUrl: 'https://help.example.invalid', onboardingUrl: 'chrome-extension://x/onboarding.html' },
    now: () => (server.clock += 1),
    broadcast: (s) => broadcasts.push(s),
    openUrl: async (url) => {
      opened.push(url);
    },
    openOptions: async () => {
      opened.push('options');
    },
    uiLanguage: () => 'en-US',
    userAgent: 'test',
    probeTimeoutMs: 1000,
  };
  return { controller: new Controller(deps), proxy, local, session, badge, alarms, broadcasts, opened };
}

async function signedIn(server: FakeServer): Promise<Harness> {
  const h = harness(server);
  const res = await h.controller.handle({ type: 'sign-in', email: 'a@example.invalid', password: 'correct' });
  expect(res.ok).toBe(true);
  return h;
}

let server: FakeServer;
beforeEach(() => {
  server = fakeServer();
});
afterEach(() => {
  vi.unstubAllGlobals();
});

describe('登录 / 登出', () => {
  it('登录后拉到配额与配置，凭据进 session 存储，5 分钟刷新闹钟建立', async () => {
    const h = await signedIn(server);
    const s = h.controller.snapshot();
    expect(s.signedIn).toBe(true);
    expect(s.subscription?.total_bytes).toBe(20 * GB);
    expect(s.regions.map((r) => r.code).sort()).toEqual(['HK', 'JP']);
    expect(s.links.webUrl).toBe('https://web.example.invalid');
    expect(await h.session.get(KEY.credentials)).toEqual([
      { host: 'a.example.invalid', port: 443, username: 'ua', password: 'pa' },
      { host: 'b.example.invalid', port: 443, username: 'ub', password: 'pb' },
      { host: 'j.example.invalid', port: 443, username: 'uj', password: 'pj' },
    ]);
    expect(h.alarms.get(ALARM_REFRESH)).toEqual({ periodInMinutes: 5 });
    expect(h.alarms.get(ALARM_CONFIG)?.delayInMinutes).toBe(29);
    expect(await h.local.get(KEY.token)).toBe('tok-1');
  });

  it('密码错：ok=false 且 code 是契约里的 AUTH_INVALID_CREDENTIALS，不登录', async () => {
    const h = harness(server);
    const res = await h.controller.handle({ type: 'sign-in', email: 'a@example.invalid', password: 'wrong' });
    expect(res).toMatchObject({ ok: false, error: { code: 'AUTH_INVALID_CREDENTIALS' } });
    expect(h.controller.snapshot().signedIn).toBe(false);
  });

  it('没有配置 API 地址的构建：登录直接报 NOT_CONFIGURED，而不是请求一个编出来的域名', async () => {
    const h = harness(server);
    // 覆盖 env：重建一个没有 base 的 controller
    const local = memoryStorage();
    const c = new Controller({
      local,
      session: memoryStorage(),
      proxy: memoryProxyPort(),
      badge: { set: async () => undefined },
      alarms: { create: async () => undefined, clear: async () => undefined },
      env: { version: 't', apiBaseUrls: [], webUrl: '', backupPageUrl: '', helpUrl: '', onboardingUrl: '' },
      now: () => Date.now(),
      broadcast: () => undefined,
      openUrl: async () => undefined,
      openOptions: async () => undefined,
      uiLanguage: () => 'en',
      userAgent: 't',
    });
    const res = await c.handle({ type: 'sign-in', email: 'a@example.invalid', password: 'correct' });
    expect(res).toMatchObject({ ok: false, error: { code: 'NOT_CONFIGURED' } });
    expect(server.calls).toEqual([]);
    void h;
  });

  it('登出：清代理、清 token、清凭据、清闹钟', async () => {
    const h = await signedIn(server);
    await h.controller.handle({ type: 'connect', region: null });
    expect(h.proxy.current).not.toBeNull();
    const res = await h.controller.handle({ type: 'sign-out' });
    expect(res.ok).toBe(true);
    expect(h.proxy.current).toBeNull();
    expect(await h.local.get(KEY.token)).toBeUndefined();
    expect(await h.session.get(KEY.credentials)).toBeUndefined();
    expect(h.alarms.size).toBe(0);
    expect(h.controller.snapshot()).toMatchObject({ signedIn: false, subscription: null, connection: { status: 'off' } });
    expect(server.calls).toContain('POST /api/v1/auth/logout');
  });
});

describe('连接', () => {
  it('探测后按延迟排序设 PAC；判据是经代理取到 probe_url，出口 IP 来自最快那台', async () => {
    const h = await signedIn(server);
    const res = await h.controller.handle({ type: 'connect', region: 'HK' });
    expect(res.ok).toBe(true);
    const s = h.controller.snapshot();
    expect(s.connection).toMatchObject({ status: 'on', region: 'HK', exitIp: '203.0.113.2', usedAtConnect: 12 * GB });
    expect(h.proxy.current).toContain('"HTTPS b.example.invalid:443; HTTPS a.example.invalid:443"');
    expect(h.proxy.current).not.toMatch(/HTTPS [^"]*; *DIRECT/);
    // 控制面（含运行时下发的镜像与面板）在 PAC 里直连
    expect(h.proxy.current).toContain('"api.example.invalid"');
    expect(h.proxy.current).toContain('"api2.example.invalid"');
    expect(h.proxy.current).toContain('"web.example.invalid"');
    expect(h.badge).toEqual({ text: 'ON', color: '#1B7355' });
    // 探测期间广播过「连接中」
    expect(h.broadcasts.some((b) => b.connection.status === 'connecting')).toBe(true);
    // 只探测了 HK 的两台
    expect(s.probes.map((p) => p.endpointId)).toEqual([1, 2]);
  });

  it('全部端点失败：清掉代理设置 + no-route，不许留着指向死端点的 PAC', async () => {
    server.probeLatency = { '1': -1, '2': -1, '3': -1 };
    const h = await signedIn(server);
    await h.controller.handle({ type: 'connect', region: null });
    expect(h.controller.snapshot().connection).toMatchObject({ status: 'no-route', reason: 'all-endpoints-failed', failedEndpoints: 3 });
    expect(h.proxy.current).toBeNull();
    expect(h.proxy.history.at(-1)).toEqual({ op: 'clear' });
    expect(h.badge.text).toBe('!');
  });

  it('服务端 501（E0/E1 未完成）：no-route 且原因是 config-unavailable，lastError 指名 NOT_IMPLEMENTED', async () => {
    server.configResponse = () => errorEnvelope(501, 'INTERNAL_ERROR', 'not implemented');
    const h = await signedIn(server);
    await h.controller.handle({ type: 'connect', region: null });
    expect(h.controller.snapshot()).toMatchObject({ connection: { status: 'no-route', reason: 'config-unavailable' }, lastError: { code: 'NOT_IMPLEMENTED' } });
    expect(h.proxy.current).toBeNull();
  });

  it('服务端给空端点列表：no-endpoints（那是运营状态，不是失败）', async () => {
    server.configResponse = () => envelope(config({ endpoints: [] }));
    const h = await signedIn(server);
    await h.controller.handle({ type: 'connect', region: null });
    expect(h.controller.snapshot().connection).toMatchObject({ status: 'no-route', reason: 'no-endpoints' });
  });

  it('别的扩展占着代理设置：不假装连上', async () => {
    const proxy = memoryProxyPort('controlled_by_other_extensions');
    const h = harness(server, { proxy });
    await h.controller.handle({ type: 'sign-in', email: 'a@example.invalid', password: 'correct' });
    await h.controller.handle({ type: 'connect', region: null });
    expect(h.controller.snapshot().connection).toMatchObject({ status: 'no-route', reason: 'proxy-not-controllable' });
    expect(proxy.history.filter((x) => x.op === 'set')).toHaveLength(0);
  });

  it('未登录不能连接', async () => {
    const h = harness(server);
    const res = await h.controller.handle({ type: 'connect', region: null });
    expect(res.ok).toBe(false);
    expect(h.proxy.current).toBeNull();
  });

  it('断开：清代理、状态 off、保留上次成功时间', async () => {
    const h = await signedIn(server);
    await h.controller.handle({ type: 'connect', region: 'HK' });
    await h.controller.handle({ type: 'disconnect' });
    const c = h.controller.snapshot().connection;
    expect(c.status).toBe('off');
    expect(c.lastSuccessAt).not.toBeNull();
    expect(h.proxy.current).toBeNull();
    expect(h.badge.text).toBe('');
  });
});

describe('配额驱动的断开与偏好', () => {
  it('刷新闹钟发现配额用尽 → 主动断开（服务端 17 s 内已切断，留着代理只会让每个请求 407 打转）', async () => {
    const h = await signedIn(server);
    await h.controller.handle({ type: 'connect', region: 'HK' });
    server.subscription = summary({ download_bytes: 19 * GB });
    await h.controller.handleAlarm(ALARM_REFRESH);
    expect(h.controller.snapshot().connection.status).toBe('off');
    expect(h.proxy.current).toBeNull();
  });

  it('改「一律直连」列表时已连接 → 用同一顺序重设 PAC，且新主机在 NEVER 里', async () => {
    const h = await signedIn(server);
    await h.controller.handle({ type: 'connect', region: 'HK' });
    const before = h.proxy.history.length;
    await h.controller.handle({ type: 'set-prefs', prefs: { neverProxy: ['https://bank.example.invalid/x'] } });
    expect(h.proxy.history.length).toBe(before + 1);
    expect(h.proxy.current).toContain('var NEVER = ["bank.example.invalid"]');
    expect(h.proxy.current).toContain('"HTTPS b.example.invalid:443; HTTPS a.example.invalid:443"');
    expect(h.controller.snapshot().prefs.neverProxy).toEqual(['bank.example.invalid']);
  });

  it('配置闹钟：凭据轮换后重拉，已连接时按 id 对回新端点重设 PAC', async () => {
    const h = await signedIn(server);
    await h.controller.handle({ type: 'connect', region: 'HK' });
    server.configResponse = () =>
      envelope(config({ endpoints: config().endpoints.filter((e) => e.id !== 1).map((e) => ({ ...e, auth: { username: 'new', password: 'new' } })) }));
    await h.controller.handleAlarm(ALARM_CONFIG);
    expect(h.proxy.current).toContain('"HTTPS b.example.invalid:443"');
    expect(h.proxy.current).not.toContain('a.example.invalid:443');
    expect(await h.session.get(KEY.credentials)).toEqual([
      { host: 'b.example.invalid', port: 443, username: 'new', password: 'new' },
      { host: 'j.example.invalid', port: 443, username: 'new', password: 'new' },
    ]);
  });
});

describe('service worker 重启', () => {
  it('记录里是 on 但代理设置已经不是我们的 → 回到 off，不假装', async () => {
    const h = await signedIn(server);
    await h.controller.handle({ type: 'connect', region: 'HK' });
    const proxy = memoryProxyPort('controllable_by_this_extension'); // 用户在别处清掉了
    const again = harness(server, { local: h.local, session: h.session, proxy });
    const s = (await again.controller.handle({ type: 'snapshot' })) as { ok: true; snapshot: Snapshot };
    expect(s.snapshot.connection.status).toBe('off');
    expect(s.snapshot.signedIn).toBe(true);
  });

  it('在「连接中」被杀：拉起后清掉代理并回到 off', async () => {
    const local = memoryStorage({ [KEY.connection]: { status: 'connecting', region: null, exitIp: null, connectedAt: null, usedAtConnect: null, lastSuccessAt: null, reason: null, failedEndpoints: 0 }, [KEY.token]: 'tok-1' });
    const proxy = memoryProxyPort();
    await proxy.setPac('var PROXY = "HTTPS dead:443";');
    const h = harness(server, { local, proxy });
    const s = (await h.controller.handle({ type: 'snapshot' })) as { ok: true; snapshot: Snapshot };
    expect(s.snapshot.connection.status).toBe('off');
    expect(proxy.current).toBeNull();
  });

  it('凭据被端点持续拒绝 → no-route(auth-rejected) 且清代理', async () => {
    const h = await signedIn(server);
    await h.controller.handle({ type: 'connect', region: 'HK' });
    await h.controller.onAuthRejected();
    expect(h.controller.snapshot().connection).toMatchObject({ status: 'no-route', reason: 'auth-rejected' });
    expect(h.proxy.current).toBeNull();
  });
});

describe('诊断与导航', () => {
  it('诊断报告不含 token、密码、端点主机名、邮箱', async () => {
    const h = await signedIn(server);
    await h.controller.handle({ type: 'connect', region: 'HK' });
    const res = await h.controller.handle({ type: 'diagnostics' });
    expect(res.ok).toBe(true);
    const text = res.ok ? (res.text ?? '') : '';
    expect(text).toContain('"product": "babel.plus-extension"');
    expect(text).toContain('"state": "on"');
    for (const secret of ['tok-1', 'pa', 'ua', 'a.example.invalid', 'b.example.invalid', 'a@example.invalid']) {
      expect(text, secret).not.toContain(`"${secret}"`);
    }
    expect(text).not.toMatch(/password/i);
  });

  it('Top up 跳到运行时下发的面板 /plan；备用页与帮助各走各的', async () => {
    const h = await signedIn(server);
    await h.controller.handle({ type: 'open', target: 'topup' });
    await h.controller.handle({ type: 'open', target: 'backup' });
    await h.controller.handle({ type: 'open', target: 'help' });
    await h.controller.handle({ type: 'open', target: 'options' });
    expect(h.opened).toEqual(['https://web.example.invalid/plan', 'https://backup.example.invalid/', 'https://help.example.invalid', 'options']);
  });
});

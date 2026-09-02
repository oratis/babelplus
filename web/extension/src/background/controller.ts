/**
 * 扩展的状态机。service worker 里唯一持有状态的对象；popup / options / onboarding 都是它的薄壳。
 *
 * 为什么把 chrome 的东西全部抽成 `ControllerDeps`：MV3 的 service worker 随时会被杀掉再拉起，
 * 状态必须每次从 storage 重建 —— 这条路径只有在「storage 是内存实现」的测试里才能被反复走到。
 * 真机上它是靠不住的：你不知道 Chrome 什么时候回收 SW。
 *
 * 几条落在这里的产品规则（都有测试）：
 *  - 连接的判据是**经代理取到 probe_url**，不是「proxy settings set 成功」。set 成功只说明 Chrome 收下了脚本。
 *  - 全部端点失败 → 清掉代理设置 + 进 no-route。**不许留着一份指向死端点的 PAC**：
 *    那样用户所有请求都在超时，而 popup 说的是「连接中」。
 *  - 配额用尽 / 到期 → 主动断开。服务端 17 s 内已经切断（三态计时实测），
 *    留着代理只会让每个请求变成 407 循环。
 *  - 另一个扩展占着代理设置 → 不假装连上（`levelOfControl`）。
 */
import { unwrap, type ApiError } from '@babelplus/shared/api';
import { buildDiagnostics, diagnosticsText } from '../shared/diagnostics.ts';
import { quotaView } from '../shared/quota.ts';
import { buildPac, hostsOfUrls, normalizeHostList } from '../shared/pac.ts';
import { effectiveRules } from '../shared/rules.ts';
import type { Request, Response } from '../shared/messages.ts';
import {
  DEFAULT_PREFS,
  OFFLINE_CONNECTION,
  type Connection,
  type Links,
  type NoRouteReason,
  type Prefs,
  type ProbeResult,
  type ProxyConfig,
  type ProxyEndpoint,
  type RegionOption,
  type Snapshot,
  type SubscriptionSummary,
} from '../shared/types.ts';
import { createExtensionApi, mirroredSessionStore, NotConfiguredError, type ExtensionApi } from './api.ts';
import type { ProxyCredential } from './auth.ts';
import { orderByProbe, probeEndpoints } from './probe.ts';
import type { ProxyPort } from './proxy.ts';
import { KEY, type KeyValue } from './storage.ts';

export interface BuildEnv {
  readonly version: string;
  readonly apiBaseUrls: readonly string[];
  readonly webUrl: string;
  readonly backupPageUrl: string;
  readonly helpUrl: string;
  readonly onboardingUrl: string;
}

export interface Badge {
  set(text: string, color: string): Promise<void>;
}

export interface Alarms {
  create(name: string, info: { periodInMinutes?: number; delayInMinutes?: number }): Promise<void>;
  clear(name: string): Promise<void>;
}

export interface ControllerDeps {
  readonly local: KeyValue;
  readonly session: KeyValue;
  readonly proxy: ProxyPort;
  readonly badge: Badge;
  readonly alarms: Alarms;
  readonly env: BuildEnv;
  readonly now: () => number;
  readonly broadcast: (snapshot: Snapshot) => void;
  readonly openUrl: (url: string) => Promise<void>;
  readonly openOptions: () => Promise<void>;
  readonly uiLanguage: () => string;
  readonly userAgent: string;
  readonly probeTimeoutMs?: number;
  readonly requestTimeoutMs?: number;
}

export const ALARM_REFRESH = 'bp-refresh';
export const ALARM_CONFIG = 'bp-config';
/** 配额每 5 分钟拉一次（spec §3.6）。 */
export const REFRESH_PERIOD_MINUTES = 5;

interface StoredConfig {
  readonly config: ProxyConfig;
  readonly fetchedAt: string;
  readonly expiresAt: string;
}

interface LastError {
  readonly code: string;
  readonly message: string;
}

const BADGE_ON = '#1B7355';
const BADGE_WAIT = '#1B4D8F';
const BADGE_BAD = '#A8352C';

function iso(ms: number): string {
  return new Date(ms).toISOString();
}

function errorOf(cause: unknown): LastError {
  if (cause instanceof NotConfiguredError) return { code: cause.code, message: cause.message };
  const e = cause as Partial<ApiError> | undefined;
  if (e && typeof e === 'object' && typeof e.code === 'string') {
    const status = typeof e.status === 'number' ? e.status : 0;
    if (status === 501) {
      return { code: 'NOT_IMPLEMENTED', message: 'The service has not enabled the browser extension yet (HTTP 501).' };
    }
    if (status === 429) return { code: 'QUOTA_RATE_LIMITED', message: e.message ?? 'Rate limited' };
    return { code: e.code, message: e.message ?? e.code };
  }
  if (cause instanceof Error) return { code: cause.name || 'ERROR', message: cause.message };
  return { code: 'UNKNOWN', message: String(cause) };
}

export class Controller {
  private readonly deps: ControllerDeps;
  private readonly api: ExtensionApi;
  private readonly token: ReturnType<typeof mirroredSessionStore>;
  private readonly ready: Promise<void>;

  private prefs: Prefs = DEFAULT_PREFS;
  private connection: Connection = OFFLINE_CONNECTION;
  private activeEndpoints: ProxyEndpoint[] = [];
  private subscription: SubscriptionSummary | null = null;
  private subscriptionAt: string | null = null;
  private stored: StoredConfig | null = null;
  private probes: ProbeResult[] = [];
  private lastError: LastError | null = null;
  private links: Links;
  private connectAbort: AbortController | null = null;

  constructor(deps: ControllerDeps) {
    this.deps = deps;
    this.links = { webUrl: deps.env.webUrl, backupPageUrl: deps.env.backupPageUrl, helpUrl: deps.env.helpUrl };
    this.token = mirroredSessionStore(deps.local, KEY.token);
    this.api = createExtensionApi(deps.env.apiBaseUrls, {
      store: this.token.store,
      timeoutMs: deps.requestTimeoutMs ?? 15_000,
      onAuthFailure: (error) => this.onAuthFailure(error),
    });
    this.ready = this.load();
  }

  /* ───────────────────────── 装载 / 持久化 ───────────────────────── */

  private async load(): Promise<void> {
    const d = this.deps;
    await this.token.load();
    this.prefs = { ...DEFAULT_PREFS, ...((await d.local.get<Prefs>(KEY.prefs)) ?? {}) };
    this.connection = (await d.local.get<Connection>(KEY.connection)) ?? OFFLINE_CONNECTION;
    this.activeEndpoints = (await d.local.get<ProxyEndpoint[]>(`${KEY.connection}.endpoints`)) ?? [];
    this.subscription = (await d.local.get<SubscriptionSummary>(KEY.subscription)) ?? null;
    this.subscriptionAt = (await d.local.get<string>(KEY.subscriptionAt)) ?? null;
    this.stored = (await d.local.get<StoredConfig>(KEY.config)) ?? null;
    this.lastError = (await d.local.get<LastError>(KEY.lastError)) ?? null;
    this.links = (await d.local.get<Links>(KEY.links)) ?? this.links;
    const bases = (await d.local.get<string[]>(KEY.apiBaseUrls)) ?? [];
    if (bases.length > 0) this.api.setBases(bases);

    // SW 在「连接中」被杀：探测没做完，代理可能停在某一台探测端点上。回到 off，且清掉设置。
    if (this.connection.status === 'connecting') {
      await d.proxy.clear();
      this.connection = OFFLINE_CONNECTION;
    }
    // 记录里是 on 但代理设置已经不是我们的（用户在别处清了、别的扩展抢了）：不假装。
    if (this.connection.status === 'on' && (await d.proxy.levelOfControl()) !== 'controlled_by_this_extension') {
      this.connection = { ...OFFLINE_CONNECTION, lastSuccessAt: this.connection.lastSuccessAt };
    }
    await this.persist();
    await this.updateBadge();
  }

  private async persist(): Promise<void> {
    const d = this.deps;
    await d.local.set(KEY.prefs, this.prefs);
    await d.local.set(KEY.connection, this.connection);
    await d.local.set(`${KEY.connection}.endpoints`, this.activeEndpoints);
    if (this.subscription) await d.local.set(KEY.subscription, this.subscription);
    else await d.local.remove(KEY.subscription);
    if (this.subscriptionAt) await d.local.set(KEY.subscriptionAt, this.subscriptionAt);
    else await d.local.remove(KEY.subscriptionAt);
    if (this.stored) await d.local.set(KEY.config, this.stored);
    else await d.local.remove(KEY.config);
    if (this.lastError) await d.local.set(KEY.lastError, this.lastError);
    else await d.local.remove(KEY.lastError);
    await d.local.set(KEY.links, this.links);
    await d.local.set(KEY.apiBaseUrls, [...this.api.bases()]);
  }

  private async emit(): Promise<void> {
    await this.persist();
    await this.updateBadge();
    this.deps.broadcast(this.snapshot());
  }

  private async updateBadge(): Promise<void> {
    const s = this.connection.status;
    if (s === 'on') await this.deps.badge.set('ON', BADGE_ON);
    else if (s === 'connecting') await this.deps.badge.set('…', BADGE_WAIT);
    else if (s === 'no-route') await this.deps.badge.set('!', BADGE_BAD);
    else await this.deps.badge.set('', BADGE_WAIT);
  }

  /* ───────────────────────── 快照 ───────────────────────── */

  private signedIn(): boolean {
    return this.token.store.read() !== null;
  }

  private regions(): RegionOption[] {
    const byRegion = new Map<string, RegionOption>();
    const latency = new Map<number, number | null>(this.probes.map((p) => [p.endpointId, p.ok ? p.latencyMs : null]));
    for (const ep of this.stored?.config.endpoints ?? []) {
      const cur = byRegion.get(ep.region);
      const l = latency.get(ep.id) ?? null;
      const best = cur ? (cur.latencyMs === null ? l : l === null ? cur.latencyMs : Math.min(cur.latencyMs, l)) : l;
      byRegion.set(ep.region, {
        code: ep.region,
        label: cur?.label ?? ep.label ?? ep.region,
        latencyMs: best,
        endpointCount: (cur?.endpointCount ?? 0) + 1,
      });
    }
    return [...byRegion.values()];
  }

  snapshot(): Snapshot {
    return {
      version: this.deps.env.version,
      signedIn: this.signedIn(),
      subscription: this.subscription,
      subscriptionFetchedAt: this.subscriptionAt,
      connection: this.connection,
      probes: this.probes,
      regions: this.regions(),
      prefs: this.prefs,
      links: this.links,
      configFetchedAt: this.stored?.fetchedAt ?? null,
      rulesRev: this.stored?.config.rules_rev ?? null,
      lastError: this.lastError,
    };
  }

  /* ───────────────────────── 入口 ───────────────────────── */

  async handle(req: Request): Promise<Response> {
    await this.ready;
    try {
      switch (req.type) {
        case 'snapshot':
          return { ok: true, snapshot: this.snapshot() };
        case 'sign-in':
          await this.signIn(req.email, req.password);
          break;
        case 'sign-out':
          await this.signOut();
          break;
        case 'connect':
          await this.connect(req.region ?? this.prefs.region);
          break;
        case 'disconnect':
          await this.disconnect();
          break;
        case 'cancel':
          this.connectAbort?.abort();
          await this.disconnect();
          break;
        case 'refresh':
          await this.refreshAll();
          break;
        case 'set-prefs':
          await this.setPrefs(req.prefs);
          break;
        case 'diagnostics':
          return { ok: true, snapshot: this.snapshot(), text: this.diagnostics() };
        case 'open':
          await this.open(req.target);
          break;
      }
      return { ok: true, snapshot: this.snapshot() };
    } catch (cause) {
      const error = errorOf(cause);
      this.lastError = error;
      await this.emit();
      return { ok: false, error };
    }
  }

  async handleAlarm(name: string): Promise<void> {
    await this.ready;
    if (!this.signedIn()) return;
    if (name === ALARM_REFRESH) {
      await this.refreshSubscription();
      const q = quotaView(this.subscription, this.deps.now());
      if (this.connection.status === 'on' && q && (q.exhausted || q.expired)) await this.disconnect();
      await this.emit();
    } else if (name === ALARM_CONFIG) {
      const okConfig = await this.refreshConfig();
      if (okConfig && this.connection.status === 'on') await this.reapplyPac();
      await this.emit();
    }
  }

  async onStartup(): Promise<void> {
    await this.ready;
    if (this.prefs.autoConnect && this.signedIn()) await this.connect(this.prefs.region);
  }

  /** 给 onAuthRequired 用：当前配置里全部端点的凭据。 */
  async getCredentials(): Promise<readonly ProxyCredential[]> {
    await this.ready;
    return (await this.deps.session.get<ProxyCredential[]>(KEY.credentials)) ?? [];
  }

  /** 端点持续拒绝我们的凭据（onAuthRequired 第三次质询）。 */
  async onAuthRejected(): Promise<void> {
    await this.ready;
    if (this.connection.status !== 'on') return;
    await this.deps.proxy.clear();
    this.connection = { ...OFFLINE_CONNECTION, status: 'no-route', reason: 'auth-rejected', lastSuccessAt: this.connection.lastSuccessAt };
    await this.emit();
  }

  noteProxyError(details: { readonly fatal: boolean; readonly error: string }): void {
    if (!details.fatal) return;
    this.lastError = { code: 'PROXY_ERROR', message: details.error };
    void this.emit();
  }

  /* ───────────────────────── 会话 ───────────────────────── */

  private async signIn(email: string, password: string): Promise<void> {
    const tokens = await unwrap(this.api.client().POST('/api/v1/auth/login', { body: { email, password } }));
    this.api.session.setToken(tokens.access_token);
    this.lastError = null;
    await this.deps.alarms.create(ALARM_REFRESH, { periodInMinutes: REFRESH_PERIOD_MINUTES });
    await this.refreshAll();
  }

  private async signOut(): Promise<void> {
    this.connectAbort?.abort();
    try {
      if (this.signedIn()) await this.api.client().POST('/api/v1/auth/logout');
    } catch {
      /* 服务端撤销失败不影响本地登出。 */
    }
    await this.deps.proxy.clear();
    this.api.session.signOut('user');
    await this.deps.session.remove(KEY.credentials);
    await this.deps.alarms.clear(ALARM_REFRESH);
    await this.deps.alarms.clear(ALARM_CONFIG);
    this.subscription = null;
    this.subscriptionAt = null;
    this.stored = null;
    this.probes = [];
    this.activeEndpoints = [];
    this.lastError = null;
    this.connection = OFFLINE_CONNECTION;
    await this.emit();
  }

  private onAuthFailure(error: ApiError): void {
    // 与用户面板 lib/api.ts 同一套判断：403 是封禁不是会话过期；登录接口的 401 是密码错。
    if (error.status !== 401) return;
    if (error.code === 'AUTH_INVALID_CREDENTIALS') return;
    if (!this.signedIn()) return;
    this.api.session.signOut('rejected');
    this.lastError = { code: 'SESSION_EXPIRED', message: 'Your session has expired. Sign in again.' };
    void (async () => {
      await this.deps.proxy.clear();
      await this.deps.session.remove(KEY.credentials);
      this.connection = OFFLINE_CONNECTION;
      await this.emit();
    })();
  }

  /* ───────────────────────── 数据 ───────────────────────── */

  private async refreshAll(): Promise<void> {
    await this.refreshSubscription();
    await this.refreshConfig();
    await this.emit();
  }

  private async refreshSubscription(): Promise<boolean> {
    try {
      const sub = await unwrap(this.api.client().GET('/api/v1/user/subscription'));
      this.subscription = sub.summary;
      this.subscriptionAt = iso(this.deps.now());
      return true;
    } catch (cause) {
      this.lastError = errorOf(cause);
      return false;
    }
  }

  private async refreshConfig(): Promise<boolean> {
    try {
      const config = await unwrap(this.api.client().GET('/api/v1/user/proxy-config'));
      const now = this.deps.now();
      this.stored = { config, fetchedAt: iso(now), expiresAt: iso(now + Math.max(60, config.expires_in) * 1000) };
      await this.deps.session.set(
        KEY.credentials,
        config.endpoints.map((e) => ({ host: e.host, port: e.port, username: e.auth.username, password: e.auth.password })),
      );
      const cp = config.control_plane;
      if (cp.api_base_urls.length > 0) this.api.setBases(cp.api_base_urls);
      this.links = {
        webUrl: cp.web_base_url ?? this.links.webUrl,
        backupPageUrl: cp.backup_page_url ?? this.links.backupPageUrl,
        helpUrl: this.links.helpUrl,
      };
      // 到期前 1 分钟重拉；expires_in 很短时也至少隔 1 分钟，避免自己把服务端打成 429。
      const minutes = Math.max(1, Math.floor(Math.max(60, config.expires_in) / 60) - 1);
      await this.deps.alarms.create(ALARM_CONFIG, { delayInMinutes: minutes });
      return true;
    } catch (cause) {
      this.lastError = errorOf(cause);
      return false;
    }
  }

  private configIsFresh(): boolean {
    return this.stored !== null && Date.parse(this.stored.expiresAt) > this.deps.now();
  }

  /* ───────────────────────── 连接 ───────────────────────── */

  private controlPlaneHosts(): string[] {
    return hostsOfUrls([...this.api.bases(), this.links.webUrl, this.links.backupPageUrl].filter((u) => u.length > 0));
  }

  private pacFor(endpoints: readonly ProxyEndpoint[]): string {
    return buildPac({
      endpoints: endpoints.map((e) => ({ host: e.host, port: e.port })),
      rules: effectiveRules(this.stored?.config.rules),
      mode: this.prefs.mode,
      alwaysProxy: this.prefs.alwaysProxy,
      neverProxy: this.prefs.neverProxy,
      controlPlaneHosts: this.controlPlaneHosts(),
    });
  }

  private async fail(reason: NoRouteReason, failed = 0): Promise<void> {
    await this.deps.proxy.clear();
    this.activeEndpoints = [];
    this.connection = {
      ...OFFLINE_CONNECTION,
      status: 'no-route',
      reason,
      failedEndpoints: failed,
      lastSuccessAt: this.connection.lastSuccessAt,
    };
    await this.emit();
  }

  private async connect(region: string | null): Promise<void> {
    if (!this.signedIn()) throw Object.assign(new Error('Sign in first.'), { name: 'NOT_SIGNED_IN' });
    this.connectAbort?.abort();
    const abort = new AbortController();
    this.connectAbort = abort;

    this.prefs = { ...this.prefs, region };
    this.probes = [];
    this.connection = { ...OFFLINE_CONNECTION, status: 'connecting', region, lastSuccessAt: this.connection.lastSuccessAt };
    await this.emit();

    if (!this.configIsFresh()) {
      const ok = await this.refreshConfig();
      if (!ok || !this.stored) {
        await this.fail('config-unavailable');
        return;
      }
    }
    const level = await this.deps.proxy.levelOfControl();
    if (level === 'not_controllable' || level === 'controlled_by_other_extensions') {
      await this.fail('proxy-not-controllable');
      return;
    }
    const all = this.stored?.config.endpoints ?? [];
    const candidates = region && all.some((e) => e.region === region) ? all.filter((e) => e.region === region) : all;
    if (candidates.length === 0) {
      await this.fail('no-endpoints');
      return;
    }

    const probes = await probeEndpoints(candidates, {
      proxy: this.deps.proxy,
      fetchImpl: (input, init) => fetch(input, init),
      timeoutMs: this.deps.probeTimeoutMs ?? 5_000,
      controlPlaneHosts: this.controlPlaneHosts(),
      now: this.deps.now,
      signal: abort.signal,
      onProgress: (results) => {
        this.probes = [...results];
        this.deps.broadcast(this.snapshot());
      },
    });
    if (abort.signal.aborted) return; // cancel 已经处理了状态
    this.probes = probes;

    const ordered = orderByProbe(candidates, probes);
    if (ordered.length === 0) {
      await this.fail('all-endpoints-failed', probes.filter((p) => !p.ok).length);
      return;
    }
    await this.deps.proxy.setPac(this.pacFor(ordered));
    const best = ordered[0];
    const bestProbe = probes.find((p) => p.endpointId === best?.id);
    const q = quotaView(this.subscription, this.deps.now());
    const now = iso(this.deps.now());
    this.activeEndpoints = ordered;
    this.connection = {
      status: 'on',
      region: best?.region ?? region,
      exitIp: bestProbe?.exitIp ?? null,
      connectedAt: now,
      usedAtConnect: q?.usedBytes ?? null,
      lastSuccessAt: now,
      reason: null,
      failedEndpoints: probes.filter((p) => !p.ok).length,
    };
    this.lastError = null;
    this.connectAbort = null;
    await this.emit();
  }

  private async reapplyPac(): Promise<void> {
    if (this.activeEndpoints.length === 0) return;
    // 凭据 / 端点轮换后按 id 对回新配置里的端点；已经不存在的端点从候选串里去掉。
    const fresh = new Map((this.stored?.config.endpoints ?? []).map((e) => [e.id, e]));
    const kept = this.activeEndpoints.map((e) => fresh.get(e.id)).filter((e): e is ProxyEndpoint => e !== undefined);
    if (kept.length === 0) {
      await this.fail('no-endpoints');
      return;
    }
    this.activeEndpoints = kept;
    await this.deps.proxy.setPac(this.pacFor(kept));
  }

  private async disconnect(): Promise<void> {
    this.connectAbort?.abort();
    this.connectAbort = null;
    await this.deps.proxy.clear();
    this.activeEndpoints = [];
    this.connection = { ...OFFLINE_CONNECTION, lastSuccessAt: this.connection.lastSuccessAt };
    await this.emit();
  }

  /* ───────────────────────── 偏好 / 导航 / 诊断 ───────────────────────── */

  private async setPrefs(patch: Partial<Prefs>): Promise<void> {
    const next: Prefs = {
      mode: patch.mode ?? this.prefs.mode,
      alwaysProxy: patch.alwaysProxy ? normalizeHostList(patch.alwaysProxy) : this.prefs.alwaysProxy,
      neverProxy: patch.neverProxy ? normalizeHostList(patch.neverProxy) : this.prefs.neverProxy,
      autoConnect: patch.autoConnect ?? this.prefs.autoConnect,
      region: patch.region === undefined ? this.prefs.region : patch.region,
    };
    const routingChanged =
      next.mode !== this.prefs.mode ||
      next.alwaysProxy.join() !== this.prefs.alwaysProxy.join() ||
      next.neverProxy.join() !== this.prefs.neverProxy.join();
    this.prefs = next;
    if (routingChanged && this.connection.status === 'on') await this.reapplyPac();
    await this.emit();
  }

  private async open(target: 'topup' | 'renew' | 'help' | 'backup' | 'options' | 'onboarding'): Promise<void> {
    const d = this.deps;
    if (target === 'options') {
      await d.openOptions();
      return;
    }
    if (target === 'onboarding') {
      await d.openUrl(d.env.onboardingUrl);
      return;
    }
    const url =
      target === 'help' ? this.links.helpUrl : target === 'backup' ? this.links.backupPageUrl : this.links.webUrl;
    if (!url) throw Object.assign(new Error('This build has no address configured for that page.'), { name: 'NOT_CONFIGURED' });
    await d.openUrl(target === 'topup' || target === 'renew' ? new URL('/plan', url).toString() : url);
  }

  private diagnostics(): string {
    return diagnosticsText(
      buildDiagnostics(this.snapshot(), {
        uiLanguage: this.deps.uiLanguage(),
        userAgent: this.deps.userAgent,
        now: iso(this.deps.now()),
      }),
    );
  }
}

/** 构建期兜底配置（vite-env.d.ts）。都可为空。 */
export function envFromImportMeta(version: string, onboardingUrl: string): BuildEnv {
  const env = import.meta.env;
  const primary = env.VITE_BP_API_BASE_URL?.trim() ?? '';
  const fallbacks = (env.VITE_BP_API_FALLBACK_URLS ?? '')
    .split(',')
    .map((s) => s.trim())
    .filter((s) => s.length > 0);
  return {
    version,
    apiBaseUrls: primary ? [primary, ...fallbacks] : fallbacks,
    webUrl: env.VITE_BP_WEB_URL?.trim() ?? '',
    backupPageUrl: env.VITE_BP_BACKUP_PAGE_URL?.trim() ?? '',
    helpUrl: env.VITE_BP_HELP_URL?.trim() ?? '',
    onboardingUrl,
  };
}

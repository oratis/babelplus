/**
 * 浏览器的状态机：登录 → 拉订阅 → 组配置 → check → 起内核 → 接 session 代理。
 *
 * Electron 的东西全部经 `ControllerDeps` 注入（`applyProxy` / `clearProxy` / `now` / `onChange`），
 * 所以整套流程在 Node 里可测，不需要开窗口。
 *
 * 落在这里的产品规则，每条都有用例：
 *  - **连接的判据是内核起来且端口可连**，不是「setProxy 调过了」。
 *  - **配额用尽 / 到期 → 主动断开**（与扩展同一条纪律：服务端 17 s 内已切断，
 *    留着代理只会让每个请求在死路上超时）。
 *  - **内核崩了不撤代理**（`degraded`）：撤掉就是静默直连。
 *  - **订阅为空 = 账号状态**（到期 / 被封 / 配额耗尽时服务端下发空列表），
 *    界面必须说人话，而不是「解析失败」。
 */
import { buildConfig } from './config.ts';
import { Core, type CoreEvent } from './core.ts';
import { pickLoopbackPort } from './ports.ts';
import { parseSubscription, SubscriptionError, type ParsedSubscription } from './subscription.ts';
import { quotaView } from './quota.ts';
import { normalizeHostList } from './routing.ts';
import { Api, ApiError } from './api.ts';
import { Store } from './store.ts';
import {
  OFFLINE,
  type ConnectionState,
  type FailureReason,
  type Prefs,
  type RegionOption,
  type SubscriptionSummary,
} from '../shared/types.ts';

export interface ControllerDeps {
  readonly api: Api;
  readonly store: Store;
  readonly core: Core;
  /** 把 Chromium 的代理指到本机端口。**只有这一处**碰 Electron 的 session。 */
  readonly applyProxy: (port: number) => Promise<void>;
  /** 撤掉代理设置。只在**用户主动断开**与登出时调用 —— 内核崩溃时绝不调。 */
  readonly clearProxy: () => Promise<void>;
  readonly controlPlaneHosts: readonly string[];
  readonly now?: () => number;
  readonly onChange?: () => void;
}

export class Controller {
  private readonly d: ControllerDeps;
  private conn: ConnectionState = OFFLINE;
  private sub: ParsedSubscription | null = null;
  private summary: SubscriptionSummary | null = null;
  private summaryAt: string | null = null;
  private subUrl: string | null = null;

  constructor(deps: ControllerDeps) {
    this.d = deps;
  }

  private get now(): number {
    return (this.d.now ?? Date.now)();
  }

  private set(next: Partial<ConnectionState>): void {
    this.conn = { ...this.conn, ...next };
    this.d.onChange?.();
  }

  get connection(): ConnectionState {
    return this.conn;
  }

  get subscriptionSummary(): SubscriptionSummary | null {
    return this.summary;
  }

  get subscriptionFetchedAt(): string | null {
    return this.summaryAt;
  }

  get regions(): readonly RegionOption[] {
    return this.sub?.regions ?? [];
  }

  get signedIn(): boolean {
    return this.d.store.current.token !== null;
  }

  get prefs(): Prefs {
    return this.d.store.current.prefs;
  }

  /** 内核事件 → 连接状态。装在构造之后由调用方接上（`core.onEvent`）。 */
  handleCoreEvent(e: CoreEvent): void {
    switch (e.type) {
      case 'started':
        this.set({ status: 'on', port: e.port, lastError: null });
        break;
      case 'exited':
        // 🔴 不撤代理。见文件头。
        if (this.conn.status === 'on') {
          this.set({
            status: 'degraded',
            lastError: { reason: 'core-crashed', detail: `内核退出（code=${e.code} signal=${e.signal}）` },
          });
        }
        break;
      case 'restarting':
        this.set({ restarts: e.attempt });
        break;
      case 'failed':
        this.set({ status: 'failed', lastError: { reason: 'core-crashed', detail: e.detail } });
        break;
      case 'stderr':
        break;
    }
  }

  async signIn(email: string, password: string): Promise<void> {
    await this.d.api.login(email, password);
    // token 由 Api 的 setToken 回调写进 store（同步触发、异步落盘），这里只等它落定。
    await this.d.store.flush();
    await this.refreshSubscription();
  }

  async signOut(): Promise<void> {
    await this.disconnect();
    await this.d.api.logout().catch(() => undefined);
    await this.d.store.update({ token: null, onboarded: this.d.store.current.onboarded });
    this.sub = null;
    this.summary = null;
    this.summaryAt = null;
    this.subUrl = null;
    this.d.onChange?.();
  }

  /** 拉会员中心的订阅摘要与订阅 URL。失败不抛，记进 lastError。 */
  async refreshSubscription(): Promise<boolean> {
    try {
      const s = await this.d.api.getSubscription();
      this.summary = s.summary;
      this.summaryAt = new Date(this.now).toISOString();
      this.subUrl = s.urls.singbox ?? null;
      if (!this.subUrl) {
        this.set({ lastError: { reason: 'no-subscription', detail: '会员中心没有下发 sing-box 订阅链接' } });
        this.d.onChange?.();
        return false;
      }
      this.d.onChange?.();
      return true;
    } catch (cause) {
      this.set({ lastError: { reason: reasonOf(cause), detail: detailOf(cause) } });
      return false;
    }
  }

  /** 拉订阅正文并解析。 */
  private async loadSubscription(): Promise<ParsedSubscription | null> {
    if (!this.subUrl && !(await this.refreshSubscription())) return null;
    const url = this.subUrl;
    if (!url) return null;
    try {
      const body = await this.d.api.fetchSubscriptionBody(url);
      this.sub = parseSubscription(body);
      return this.sub;
    } catch (cause) {
      const reason: FailureReason =
        cause instanceof SubscriptionError
          ? cause.reason === 'empty'
            ? 'subscription-empty'
            : 'no-subscription'
          : reasonOf(cause);
      this.set({ status: 'failed', lastError: { reason, detail: detailOf(cause) } });
      return null;
    }
  }

  /**
   * 连接。`outbound` 为 undefined 时用偏好里的；显式传 null = 用订阅的选择器。
   * 全程任一步失败都进 `failed` 并**不设代理**。
   */
  async connect(outbound?: string | null): Promise<void> {
    if (!this.signedIn) {
      this.set({ status: 'failed', lastError: { reason: 'not-signed-in', detail: '先登录' } });
      return;
    }
    this.set({ status: 'starting', lastError: null, restarts: 0 });

    const chosen = outbound === undefined ? this.prefs.outbound : outbound;
    if (chosen !== this.prefs.outbound) {
      await this.d.store.update({ prefs: { ...this.prefs, outbound: chosen } });
    }

    const sub = await this.loadSubscription();
    if (!sub) return;

    const quota = quotaView(this.summary, this.now);
    if (quota?.expired || quota?.exhausted) {
      this.set({
        status: 'failed',
        lastError: {
          reason: 'subscription-empty',
          detail: quota.expired ? '通行证已到期' : '通行证流量已用尽',
        },
      });
      return;
    }

    let port: number;
    try {
      port = await pickLoopbackPort();
    } catch (cause) {
      this.set({ status: 'failed', lastError: { reason: 'port-unavailable', detail: detailOf(cause) } });
      return;
    }

    const { config, proxyTag } = buildConfig({
      subscription: sub,
      port,
      outbound: chosen,
      rules: {
        mode: this.prefs.mode,
        alwaysProxy: this.prefs.alwaysProxy,
        neverProxy: this.prefs.neverProxy,
        controlPlaneHosts: this.d.controlPlaneHosts,
      },
    });

    try {
      await this.d.core.start(config, port);
    } catch (cause) {
      const reason = (cause as { reason?: FailureReason }).reason ?? 'core-missing';
      this.set({ status: 'failed', port: null, lastError: { reason, detail: detailOf(cause) } });
      return;
    }

    // 只有内核真的在听了才接代理。顺序反过来的话，中间那一小段时间里
    // Chromium 会把请求发给一个还没起来的端口 —— 表现是「刚连上就打不开网页」。
    await this.d.applyProxy(port);
    this.set({
      status: 'on',
      port,
      outbound: proxyTag,
      startedAt: new Date(this.now).toISOString(),
      lastError: null,
    });
    await this.d.store.update({ lastConnectedAt: new Date(this.now).toISOString(), onboarded: true });
  }

  async disconnect(): Promise<void> {
    await this.d.core.stop();
    await this.d.clearProxy();
    this.conn = { ...OFFLINE, lastError: null };
    this.d.onChange?.();
  }

  /** 定时刷新配额；用尽 / 到期时主动断开。 */
  async tick(): Promise<void> {
    if (!this.signedIn) return;
    await this.refreshSubscription();
    const quota = quotaView(this.summary, this.now);
    if (this.conn.status === 'on' && quota && (quota.exhausted || quota.expired)) {
      await this.disconnect();
      this.set({
        status: 'failed',
        lastError: {
          reason: 'subscription-empty',
          detail: quota.expired ? '通行证已到期，已断开' : '通行证流量已用尽，已断开',
        },
      });
    }
  }

  /** 改偏好。影响路由的改动在已连接时会**重建配置并重启内核**（sing-box 不支持热改路由）。 */
  async setPrefs(patch: Partial<Prefs>): Promise<void> {
    const before = this.prefs;
    const next: Prefs = {
      mode: patch.mode ?? before.mode,
      alwaysProxy: patch.alwaysProxy ? normalizeHostList(patch.alwaysProxy) : before.alwaysProxy,
      neverProxy: patch.neverProxy ? normalizeHostList(patch.neverProxy) : before.neverProxy,
      outbound: patch.outbound === undefined ? before.outbound : patch.outbound,
      launchAtStart: patch.launchAtStart ?? before.launchAtStart,
    };
    await this.d.store.update({ prefs: next });
    const routingChanged =
      next.mode !== before.mode ||
      next.alwaysProxy.join() !== before.alwaysProxy.join() ||
      next.neverProxy.join() !== before.neverProxy.join() ||
      next.outbound !== before.outbound;
    if (routingChanged && (this.conn.status === 'on' || this.conn.status === 'degraded')) {
      await this.d.core.stop();
      await this.connect(next.outbound);
    } else {
      this.d.onChange?.();
    }
  }

  /** 「这个站点在这里被屏蔽」提示条的处置：加进 alwaysProxy 并重连。 */
  async routeHost(host: string): Promise<void> {
    const list = normalizeHostList([...this.prefs.alwaysProxy, host]);
    await this.setPrefs({ alwaysProxy: list });
  }
}

function reasonOf(cause: unknown): FailureReason {
  if (cause instanceof ApiError) {
    if (cause.status === 0) return 'network';
    if (cause.status === 401) return 'not-signed-in';
    return 'no-subscription';
  }
  return 'network';
}

function detailOf(cause: unknown): string {
  return cause instanceof Error ? cause.message : String(cause);
}

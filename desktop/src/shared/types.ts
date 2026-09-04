/**
 * 浏览器（`bp-browser`）的共享类型。
 *
 * 🔴 与扩展最大的结构性差别写在这里，读代码前先知道：
 * **浏览器不需要新的服务端端点。** 它随包 sing-box，直接消费会员中心已有的订阅
 * （`GET /api/v1/user/subscription` → `urls.singbox`），走的是现有 REALITY / Hysteria2 节点。
 * 扩展那条 HTTPS 入站（roadmap B66 / E0）与本进程无关。
 *
 * 订阅产出**没有 `inbounds`**（roadmap B45 登记的欠缺）。对浏览器反而正好：
 * 我们自己注入一个只监听 127.0.0.1 的 mixed 入站，这就是 B45 注释里说的
 * 「让客户端自带模板」那条路。生成的完整配置在启动前由**随包的 sing-box 自己 `check`** 一遍
 * （`core.ts`），check 不过就不设代理 —— 失败要响，不静默直连。
 */

/** 出口地区（从订阅里的节点名/标签推出来，仅用于显示与选择）。 */
export interface RegionOption {
  readonly tag: string;
  readonly label: string;
}

/** 会员中心的配额摘要，字段名与契约 `SubscriptionSummary` 一致。 */
export interface SubscriptionSummary {
  readonly plan_name?: string;
  readonly upload_bytes: number;
  readonly download_bytes: number;
  readonly total_bytes: number;
  readonly expired_at?: string | null;
  readonly reset_at?: string | null;
  readonly device_count: number;
  readonly device_limit: number;
}

/** 路由模式。与扩展同名同义（spec §3.4：默认走代理，只有规则表里的才直连）。 */
export type RoutingMode = 'smart' | 'everything';

export interface Prefs {
  readonly mode: RoutingMode;
  /** 一律走代理的主机（接受「这个站点被屏蔽」提示条时自动加进来）。 */
  readonly alwaysProxy: readonly string[];
  /** 一律直连的主机（银行、政务站常拒绝境外地址）。 */
  readonly neverProxy: readonly string[];
  /** 上次选的出口节点 tag；null = 用订阅里的默认选择器。 */
  readonly outbound: string | null;
  readonly launchAtStart: boolean;
}

export const DEFAULT_PREFS: Prefs = {
  mode: 'smart',
  alwaysProxy: [],
  neverProxy: [],
  outbound: null,
  launchAtStart: false,
};

/**
 * 连接状态。`degraded` 是浏览器特有的一态：内核死了但代理设置**故意不撤** ——
 * 撤掉等于静默直连，而那正是 spec §3.3 规则 1 禁止的事。此时页面会连接失败，界面必须明说原因。
 */
export type ConnectionStatus = 'off' | 'starting' | 'on' | 'degraded' | 'failed';

/** 失败原因。每一种在界面上的说法不同，糊成一句「连不上」会让用户在错的地方排障。 */
export type FailureReason =
  | 'not-signed-in'
  | 'no-subscription'
  | 'subscription-empty'
  | 'config-rejected'
  | 'core-missing'
  | 'core-crashed'
  | 'port-unavailable'
  | 'network';

export interface ConnectionState {
  readonly status: ConnectionStatus;
  /** 本机 mixed 入站端口；未启动为 null。 */
  readonly port: number | null;
  readonly outbound: string | null;
  readonly startedAt: string | null;
  readonly lastError: { readonly reason: FailureReason; readonly detail: string } | null;
  /** 内核重启次数（本次会话内），界面在 > 0 时提示。 */
  readonly restarts: number;
}

export const OFFLINE: ConnectionState = {
  status: 'off',
  port: null,
  outbound: null,
  startedAt: null,
  lastError: null,
  restarts: 0,
};

/** 每个标签页的路由归属。`bytes` 来自 CDP `Network.loadingFinished` 的 `encodedDataLength`。 */
export type TabRoute = 'proxy' | 'direct' | 'failed';

export interface TabState {
  readonly id: number;
  readonly title: string;
  readonly url: string;
  readonly route: TabRoute;
  readonly bytes: number;
  readonly loading: boolean;
  /** 直连且加载失败、且该主机在「被屏蔽」清单里时，提示条要显示的主机名。 */
  readonly blockedHost: string | null;
}

/** 渲染进程看到的全部状态。主进程是唯一持有者。 */
export interface Snapshot {
  readonly version: string;
  readonly signedIn: boolean;
  readonly connection: ConnectionState;
  readonly subscription: SubscriptionSummary | null;
  readonly subscriptionFetchedAt: string | null;
  readonly regions: readonly RegionOption[];
  readonly prefs: Prefs;
  readonly tabs: readonly TabState[];
  readonly activeTabId: number | null;
  readonly links: { readonly webUrl: string; readonly helpUrl: string };
  /** 首次运行没走完时为 true，界面显示三步引导而不是浏览器。 */
  readonly onboarding: boolean;
}

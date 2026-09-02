/**
 * 扩展内部的类型。**契约类型一律从生成物取**（`@babelplus/shared/api` 的 `components`），
 * 这里只定义扩展自己的状态形状 —— 它们不经过网络，不需要契约。
 */
import type { components } from '@babelplus/shared/api';

export type ProxyConfig = components['schemas']['ProxyConfig'];
export type ProxyEndpoint = components['schemas']['ProxyEndpoint'];
export type ProxyRules = components['schemas']['ProxyRules'];
export type ProxyControlPlane = components['schemas']['ProxyControlPlane'];
export type SubscriptionSummary = components['schemas']['SubscriptionSummary'];

/**
 * 路由模式（options 页的三段开关里的前两段）。
 * - `smart`：只有规则表里的中国站点直连，其余走代理（默认；方向对「来华外国人」是反过来的，spec §3.4）
 * - `everything`：全部走代理，只留本地 / 私有地址与控制面直连
 *
 * mockup 里的第三段「Off」**不是一种路由模式**，它就是断开：一份「全部直连」的 PAC 会违反
 * 「不静默直连」这条规则（spec §3.3 规则 1），所以不存在 `off` 模式，options 页的 Off 直接调 disconnect。
 */
export type RoutingMode = 'smart' | 'everything';

export interface Prefs {
  readonly mode: RoutingMode;
  /** 用户自己加的「一律走代理」主机（options 页一行一个；接受 `blocked here` 提示时也会加进来）。 */
  readonly alwaysProxy: readonly string[];
  /** 用户自己加的「一律直连」主机（银行、政务站常拒绝境外地址）。 */
  readonly neverProxy: readonly string[];
  /** 浏览器启动时自动按上次地区连接。 */
  readonly autoConnect: boolean;
  /** 上次选的出口地区代码；`null` = 让扩展挑最快的。 */
  readonly region: string | null;
}

export const DEFAULT_PREFS: Prefs = {
  mode: 'smart',
  alwaysProxy: [],
  neverProxy: [],
  autoConnect: false,
  region: null,
};

/** 一次端点探测的结果。`latencyMs === null` 且 `ok` 表示「没有 probe_url，未测」。 */
export interface ProbeResult {
  readonly endpointId: number;
  readonly region: string;
  readonly label: string;
  readonly ok: boolean;
  readonly latencyMs: number | null;
  readonly exitIp: string | null;
  readonly error: string | null;
}

export type ConnectionStatus = 'off' | 'connecting' | 'on' | 'no-route';

/**
 * 「全部端点不可达」态的成因。每一种在 popup 上的说法不同，糊成一句「连不上」会让用户在错的地方排障：
 * - `no-endpoints`：服务端给了空列表（当前没有可用入站）
 * - `all-endpoints-failed`：有端点，逐个探测全部失败
 * - `auth-rejected`：端点持续拒绝我们的凭据（多半是订阅被重置，需重新登录拉新凭据）
 * - `config-unavailable`：拉不到 `/user/proxy-config`（含服务端 501：E0/E1 未完成）
 * - `proxy-not-controllable`：另一个扩展或策略占着代理设置，本扩展改不了
 */
export type NoRouteReason =
  | 'no-endpoints'
  | 'all-endpoints-failed'
  | 'auth-rejected'
  | 'config-unavailable'
  | 'proxy-not-controllable';

export interface Connection {
  readonly status: ConnectionStatus;
  readonly region: string | null;
  readonly exitIp: string | null;
  /** RFC3339。 */
  readonly connectedAt: string | null;
  /** 连接时服务端报告的已用字节，会话用量 = 最新已用 − 它（只用于显示，不用于计费判定，spec §3.6）。 */
  readonly usedAtConnect: number | null;
  readonly lastSuccessAt: string | null;
  readonly reason: NoRouteReason | null;
  /** 有关系时才有意义：不可达态里失败了多少个端点。 */
  readonly failedEndpoints: number;
}

export const OFFLINE_CONNECTION: Connection = {
  status: 'off',
  region: null,
  exitIp: null,
  connectedAt: null,
  usedAtConnect: null,
  lastSuccessAt: null,
  reason: null,
  failedEndpoints: 0,
};

export interface RegionOption {
  readonly code: string;
  readonly label: string;
  /** 该地区最快端点的延迟；未测为 `null`。 */
  readonly latencyMs: number | null;
  readonly endpointCount: number;
}

export interface Links {
  readonly webUrl: string;
  readonly backupPageUrl: string;
  readonly helpUrl: string;
}

/**
 * popup / options / onboarding 看到的全部状态。由 service worker 维护，页面只读；
 * 页面要改什么都经消息发回 service worker（`messages.ts`）。
 */
export interface Snapshot {
  readonly version: string;
  readonly signedIn: boolean;
  readonly subscription: SubscriptionSummary | null;
  readonly subscriptionFetchedAt: string | null;
  readonly connection: Connection;
  readonly probes: readonly ProbeResult[];
  readonly regions: readonly RegionOption[];
  readonly prefs: Prefs;
  readonly links: Links;
  readonly configFetchedAt: string | null;
  readonly rulesRev: number | null;
  /** 最近一次操作的错误（登录失败、拉配置失败），给 popup 显示一次。 */
  readonly lastError: { readonly code: string; readonly message: string } | null;
}

/**
 * PAC 生成。这是 spec §3.3 那一处「白拿」的落点：返回值是一个**有序候选串**
 * `HTTPS a:443; HTTPS b:443`，前一台连不上时 Chrome 的网络栈自己降到下一台 ——
 * ADR 0010 的域名池故障转移在这里不需要我们写一行代码。
 *
 * 三条不可协商的规则（spec §3.3）：
 *  1. 🔴 **末位不放 `DIRECT`。** 全部端点挂掉时连接必须失败并告警，不能静默直连让用户以为自己被保护着。
 *     `buildPac` 对空端点列表直接抛错而不是生成一个「只有 DIRECT」的脚本，同理。
 *  2. 端点与规则来自服务端（`/user/proxy-config`），本文件不含任何域名。
 *  3. 顺序由调用方给（探测延迟排序 / 服务端打乱后的顺序），本文件不排序。
 *
 * PAC 脚本本身只用 ES5：它跑在 Chrome 的 PAC 解析器里，不是页面里。
 * 主机名匹配用 `dnsDomainIs(host, ".suffix")`，它是后缀匹配且不触发 DNS；
 * `isInNet` 只对 IPv4 字面量调用 —— 对主机名调用它会先做一次 DNS 解析，而那正是要避免的。
 */
import type { ProxyRules } from './types.ts';

export interface PacEndpoint {
  readonly host: string;
  readonly port: number;
}

export interface PacInput {
  /** 已排好序的端点。空数组抛错。 */
  readonly endpoints: readonly PacEndpoint[];
  readonly rules: ProxyRules;
  readonly mode: 'smart' | 'everything';
  /** 用户自定义「一律走代理」，优先级最高（仅次于本地地址与控制面）。 */
  readonly alwaysProxy: readonly string[];
  /** 用户自定义「一律直连」。 */
  readonly neverProxy: readonly string[];
  /** 控制面主机（API / 面板域名池），一律直连：控制面故障不得升级为数据面故障，反之亦然。 */
  readonly controlPlaneHosts: readonly string[];
}

const HOST_RE = /^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?(\.[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)*$/;

/**
 * 把 options 页 textarea 的文本（或服务端列表）规整成主机名列表：
 * 去协议、去路径、去端口、小写、去前导点、去重、丢掉非法项。**不做 DNS，不做通配。**
 */
export function normalizeHostList(input: string | readonly string[]): string[] {
  const parts = typeof input === 'string' ? input.split(/[\s,;]+/) : input;
  const out: string[] = [];
  for (const raw of parts) {
    let h = raw.trim().toLowerCase();
    if (!h) continue;
    h = h.replace(/^[a-z][a-z0-9+.-]*:\/\//, '');
    h = h.replace(/[/?#].*$/, '');
    h = h.replace(/:\d+$/, '');
    h = h.replace(/^\*?\.+/, '');
    if (!HOST_RE.test(h)) continue;
    if (!out.includes(h)) out.push(h);
  }
  return out;
}

/** 控制面 URL → 主机名。非法 URL 丢掉，不猜。 */
export function hostsOfUrls(urls: readonly string[]): string[] {
  const out: string[] = [];
  for (const u of urls) {
    try {
      const host = new URL(u).hostname.toLowerCase();
      if (host && !out.includes(host)) out.push(host);
    } catch {
      /* 非法 URL：跳过 */
    }
  }
  return out;
}

function proxyString(endpoints: readonly PacEndpoint[]): string {
  return endpoints.map((e) => `HTTPS ${e.host}:${e.port}`).join('; ');
}

function jsArray(list: readonly string[]): string {
  return JSON.stringify(normalizeHostList(list));
}

export function buildPac(input: PacInput): string {
  if (input.endpoints.length === 0) {
    throw new Error('PAC 需要至少一个端点：没有端点时不设代理，而不是生成一份只剩 DIRECT 的脚本');
  }
  for (const e of input.endpoints) {
    if (!HOST_RE.test(e.host.toLowerCase()) && !/^\d{1,3}(\.\d{1,3}){3}$/.test(e.host)) {
      throw new Error(`端点主机名不合法：${e.host}`);
    }
    if (!Number.isInteger(e.port) || e.port <= 0 || e.port > 65535) {
      throw new Error(`端点端口不合法：${e.host}:${e.port}`);
    }
  }
  const proxy = JSON.stringify(proxyString(input.endpoints));
  const smart = input.mode === 'smart';
  const directSuffixes = smart ? jsArray(input.rules.direct_suffixes) : '[]';
  const proxySuffixes = smart ? jsArray(input.rules.proxy_suffixes ?? []) : '[]';

  // ES5 only. 顺序：本地/私有 → 控制面 → 用户直连表 → 用户代理表 → 服务端代理例外 → 服务端直连表 → 默认代理。
  return [
    'var PROXY = ' + proxy + ';',
    'var CONTROL_PLANE = ' + jsArray(input.controlPlaneHosts) + ';',
    'var NEVER = ' + jsArray(input.neverProxy) + ';',
    'var ALWAYS = ' + jsArray(input.alwaysProxy) + ';',
    'var PROXY_SUFFIXES = ' + proxySuffixes + ';',
    'var DIRECT_SUFFIXES = ' + directSuffixes + ';',
    'function inList(host, list) {',
    '  for (var i = 0; i < list.length; i++) {',
    '    if (host === list[i] || dnsDomainIs(host, "." + list[i])) return true;',
    '  }',
    '  return false;',
    '}',
    'function isPrivateV4(host) {',
    '  return isInNet(host, "10.0.0.0", "255.0.0.0") ||',
    '    isInNet(host, "172.16.0.0", "255.240.0.0") ||',
    '    isInNet(host, "192.168.0.0", "255.255.0.0") ||',
    '    isInNet(host, "127.0.0.0", "255.0.0.0") ||',
    '    isInNet(host, "169.254.0.0", "255.255.0.0") ||',
    '    isInNet(host, "100.64.0.0", "255.192.0.0");',
    '}',
    'function FindProxyForURL(url, host) {',
    '  host = host.toLowerCase();',
    '  if (host === "localhost" || isPlainHostName(host) || shExpMatch(host, "*.local") || shExpMatch(host, "*.localhost")) return "DIRECT";',
    '  if (/^\\d{1,3}(\\.\\d{1,3}){3}$/.test(host)) { if (isPrivateV4(host)) return "DIRECT"; }',
    '  else if (host.indexOf(":") !== -1) { if (host === "::1" || shExpMatch(host, "fe80:*") || shExpMatch(host, "fc*") || shExpMatch(host, "fd*")) return "DIRECT"; }',
    '  if (inList(host, CONTROL_PLANE)) return "DIRECT";',
    '  if (inList(host, NEVER)) return "DIRECT";',
    '  if (inList(host, ALWAYS)) return PROXY;',
    '  if (inList(host, PROXY_SUFFIXES)) return PROXY;',
    '  if (inList(host, DIRECT_SUFFIXES)) return "DIRECT";',
    '  return PROXY;',
    '}',
    '',
  ].join('\n');
}

/** 只给一台端点的 PAC，探测时用：探测某一台时不能让候选串把失败悄悄转到下一台。 */
export function buildSingleEndpointPac(endpoint: PacEndpoint, controlPlaneHosts: readonly string[]): string {
  return buildPac({
    endpoints: [endpoint],
    rules: { direct_suffixes: [] },
    mode: 'everything',
    alwaysProxy: [],
    neverProxy: [],
    controlPlaneHosts,
  });
}

/**
 * 路由规则：**一处生成，两处使用**。
 *
 * 这是本文件存在的全部理由 —— 同一张规则表既要变成 sing-box 的 `route.rules`，
 * 又要回答界面上「这个标签页走的是代理还是直连」。两处各写一遍必然会漂移，
 * 而漂移的现象是「角标说直连、实际走了代理」这种没人能查的错。
 *
 * 方向与扩展一致（spec §3.4）：**默认走代理**，只有规则表里的才直连 ——
 * 用户是在中国的外国人，他打不开的东西比打得开的多。
 *
 * 🔴 浏览器**不用** Chromium 的 `proxyBypassRules` 做分流：整份流量交给本机 mixed 入站，
 * 由 sing-box 按下面这张表分。Chromium 侧只 bypass 回环地址（否则连不上自己的入站）。
 * 这样「谁在分流」只有一个答案，角标才可能是真的。
 */

/** 直连的域名后缀兜底表。服务端订阅里没有 route.rules（B45），所以这张表在客户端。 */
export const CN_DIRECT_SUFFIXES: readonly string[] = [
  'cn',
  'taobao.com',
  'tmall.com',
  'alipay.com',
  'alicdn.com',
  'jd.com',
  'qq.com',
  'wechat.com',
  'baidu.com',
  'bdstatic.com',
  'amap.com',
  'autonavi.com',
  'meituan.com',
  'dianping.com',
  'bilibili.com',
  'zhihu.com',
  'douyin.com',
  'unionpay.com',
  'cmbchina.com',
  'icbc.com.cn',
  'ccb.com',
  'boc.cn',
];

/**
 * 「在中国打不开」的清单。**只用于一件事**：一个直连的页面加载失败时，判断要不要弹
 * 「这个站点在这里被屏蔽，要不要走 babel.plus」提示条（spec §4.3 第 3 条）。
 * 它不参与分流 —— 这些域名本来就默认走代理。
 */
export const BLOCKED_SUFFIXES: readonly string[] = [
  'google.com',
  'youtube.com',
  'gmail.com',
  'facebook.com',
  'instagram.com',
  'whatsapp.com',
  'x.com',
  'twitter.com',
  'wikipedia.org',
  'github.io',
  'notion.so',
  'slack.com',
  'figma.com',
  'openai.com',
  'chatgpt.com',
  'anthropic.com',
  'claude.ai',
];

const HOST_RE = /^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?(\.[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)*$/;

/** 文本/列表 → 规整主机名：去协议、路径、端口、前导点，小写去重，丢掉非法项（**不猜**）。 */
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

/** URL → 主机名（小写）。取不到返回 null，不抛。 */
export function hostOf(url: string): string | null {
  try {
    const h = new URL(url).hostname.toLowerCase();
    return h.length > 0 ? h : null;
  } catch {
    return null;
  }
}

function matches(host: string, suffixes: readonly string[]): boolean {
  return suffixes.some((s) => host === s || host.endsWith(`.${s}`));
}

export interface RuleInput {
  readonly mode: 'smart' | 'everything';
  readonly alwaysProxy: readonly string[];
  readonly neverProxy: readonly string[];
  /** 控制面主机（API / 面板）。一律直连：控制面故障不得升级为数据面故障，反之亦然。 */
  readonly controlPlaneHosts: readonly string[];
}

/** 一个主机的判定结果。`reason` 只为让界面能解释「为什么这一页是直连」。 */
export type Decision = { readonly route: 'proxy' | 'direct'; readonly reason: string };

/**
 * 判定顺序 = 生成 sing-box 规则的顺序。改这里必须同时改 `singboxRules`，
 * 两者的一致性由 `routing.test.ts` 逐条对拍。
 */
export function decide(host: string, input: RuleInput): Decision {
  const h = host.toLowerCase();
  if (h === 'localhost' || h.endsWith('.localhost') || h.endsWith('.local') || !h.includes('.')) {
    return { route: 'direct', reason: 'local' };
  }
  if (matches(h, normalizeHostList(input.controlPlaneHosts))) return { route: 'direct', reason: 'control-plane' };
  if (matches(h, normalizeHostList(input.neverProxy))) return { route: 'direct', reason: 'never-list' };
  if (matches(h, normalizeHostList(input.alwaysProxy))) return { route: 'proxy', reason: 'always-list' };
  if (input.mode === 'everything') return { route: 'proxy', reason: 'mode-everything' };
  if (matches(h, CN_DIRECT_SUFFIXES)) return { route: 'direct', reason: 'cn-direct' };
  return { route: 'proxy', reason: 'default-proxy' };
}

/** 这个主机是否在「在中国打不开」的清单里（只用于提示条）。 */
export function looksBlocked(host: string): boolean {
  return matches(host.toLowerCase(), BLOCKED_SUFFIXES);
}

/** sing-box `route.rules` 的一条。字段名照 sing-box 1.14 的 schema。 */
export interface SingboxRule {
  readonly ip_is_private?: boolean;
  readonly domain_suffix?: string[];
  readonly domain?: string[];
  readonly outbound: string;
}

/**
 * 生成 sing-box 的 `route.rules`。
 *
 * `directTag` 是我们自己加的 direct 出站的 tag；`proxyTag` 是订阅里的选择器（或用户选的节点）。
 * **没有 final 规则** —— `final` 由 `config.ts` 写成 `proxyTag`，也就是「默认走代理」。
 */
export function singboxRules(input: RuleInput, directTag: string, proxyTag: string): SingboxRule[] {
  const rules: SingboxRule[] = [
    // 回环与私有网段直连。放第一条：本机入站自己也要能连出去。
    { ip_is_private: true, outbound: directTag },
    { domain_suffix: ['localhost', 'local'], outbound: directTag },
  ];
  const control = normalizeHostList(input.controlPlaneHosts);
  if (control.length > 0) rules.push({ domain_suffix: [...control], outbound: directTag });
  const never = normalizeHostList(input.neverProxy);
  if (never.length > 0) rules.push({ domain_suffix: [...never], outbound: directTag });
  const always = normalizeHostList(input.alwaysProxy);
  if (always.length > 0) rules.push({ domain_suffix: [...always], outbound: proxyTag });
  if (input.mode === 'smart') {
    rules.push({ domain_suffix: [...CN_DIRECT_SUFFIXES], outbound: directTag });
  }
  return rules;
}

/**
 * 界面文案。英文优先（商业前提），简体中文第二语言（spec §3.1）。
 *
 * 不用 `chrome.i18n.getMessage` 做界面文案：它只能读 `_locales`，改一个词就要改两份 JSON 且拿不到类型；
 * `_locales` 只留给 manifest 的名字与简介（那两处只能走它）。
 *
 * 🔴 文案纪律（go-to-market-plan 裁决 6）：**一律自称 privacy / access，绝不出现 unblock、解锁、
 * 流媒体品牌、eSIM、电信字样。** 这一条决定支付通道把我们放进「受限-可审批」还是「禁止」。
 */

const en = {
  brand: 'babel.plus',
  status_signed_out: 'Signed out',
  status_off: 'Off',
  status_connecting: 'Connecting',
  status_on: 'Connected',
  status_low: 'Running low',
  status_exhausted: 'Used up',
  status_expired: 'Expired',
  status_no_route: 'No route',

  email: 'Email',
  password: 'Password',
  sign_in: 'Sign in',
  signing_in: 'Signing in…',
  get_a_pass: 'Get a pass →',
  help: 'Help',
  sign_out: 'Sign out',
  top_up: 'Top up',
  renew: 'Renew',
  buy_more: 'Buy more data',
  connect: 'Connect',
  disconnect: 'Disconnect',
  cancel: 'Cancel',
  retry: 'Retry',
  stay_connected: 'Stay connected',
  open_backup: 'Open backup address page',
  copy_diagnostics: 'Copy diagnostics',
  copied: 'Copied',
  options: 'Options',
  not_configured: 'Not configured',

  days_left: '{n} days left',
  day_left: '1 day left',
  no_expiry: 'No expiry',
  unlimited: 'Unlimited',
  gb_of: '{used} / {total} GB',
  your_exit_ip: 'Your exit IP',
  this_session: 'This session',
  region: 'Region',
  fastest: 'Fastest available',
  untested: 'untested',
  waiting: '— waiting',

  testing_endpoints: 'Testing {n} endpoints — picking the fastest one.',
  low_banner: '{left} GB left.',
  low_banner_tail: 'Top up before it runs out; the connection stays up until then.',
  exhausted_banner: 'Your pass is used up.',
  exhausted_banner_tail: 'Traffic is no longer routed.',
  expired_banner: 'Your pass ended on {date}.',
  expired_unused: '{unused} GB went unused.',
  carry_over_rule: 'Carry-over only works if you renew before a pass ends.',
  no_route_banner: "Can't reach any server.",
  no_route_all_failed: 'All {n} endpoints failed. You are not being routed — nothing silently fell back to a direct connection.',
  no_route_no_endpoints: 'The service has no endpoints available for your account right now. You are not being routed.',
  no_route_auth: 'The servers rejected your credentials. Sign out and sign in again to refresh them.',
  no_route_config: "Couldn't fetch your proxy configuration from the service. You are not being routed.",
  no_route_not_controllable: 'Another extension or a system policy controls proxy settings. Disable it to use babel.plus.',
  last_success: 'Last success',
  quota_hint: 'Quota is reported by the service every few minutes; the number here may lag by a moment.',

  signin_failed: 'Sign-in failed',
  invalid_credentials: 'Email or password is incorrect.',
  rate_limited: 'Too many attempts. Wait a minute and try again.',
  network_error: "Can't reach the service. Check your connection or try the backup address page.",
  unknown_error: 'Something went wrong.',
} as const;

export type MessageKey = keyof typeof en;

const zh: Record<MessageKey, string> = {
  brand: 'babel.plus',
  status_signed_out: '未登录',
  status_off: '已关闭',
  status_connecting: '连接中',
  status_on: '已连接',
  status_low: '流量将尽',
  status_exhausted: '已用尽',
  status_expired: '已过期',
  status_no_route: '无可用线路',

  email: '邮箱',
  password: '密码',
  sign_in: '登录',
  signing_in: '登录中…',
  get_a_pass: '获取通行证 →',
  help: '帮助',
  sign_out: '退出登录',
  top_up: '充值',
  renew: '续费',
  buy_more: '购买流量',
  connect: '连接',
  disconnect: '断开',
  cancel: '取消',
  retry: '重试',
  stay_connected: '保持连接',
  open_backup: '打开备用地址页',
  copy_diagnostics: '复制诊断信息',
  copied: '已复制',
  options: '选项',
  not_configured: '未配置',

  days_left: '剩 {n} 天',
  day_left: '剩 1 天',
  no_expiry: '不限时',
  unlimited: '不限量',
  gb_of: '{used} / {total} GB',
  your_exit_ip: '你的出口 IP',
  this_session: '本次会话',
  region: '出口地区',
  fastest: '自动选最快',
  untested: '未测',
  waiting: '— 等待中',

  testing_endpoints: '正在测试 {n} 个线路，选最快的一条。',
  low_banner: '还剩 {left} GB。',
  low_banner_tail: '用尽前先充值；在那之前连接不会断。',
  exhausted_banner: '通行证流量已用尽。',
  exhausted_banner_tail: '流量已不再转发。',
  expired_banner: '通行证已于 {date} 到期。',
  expired_unused: '有 {unused} GB 未使用。',
  carry_over_rule: '只有在到期前续费，剩余流量才能顺延。',
  no_route_banner: '连不上任何服务器。',
  no_route_all_failed: '全部 {n} 条线路失败。你的流量没有被转发 —— 没有任何东西悄悄改成了直连。',
  no_route_no_endpoints: '当前没有可用于你账号的线路。你的流量没有被转发。',
  no_route_auth: '服务器拒绝了你的凭据。请退出并重新登录以刷新凭据。',
  no_route_config: '无法从服务端获取代理配置。你的流量没有被转发。',
  no_route_not_controllable: '另一个扩展或系统策略占用了代理设置，关掉它才能使用 babel.plus。',
  last_success: '上次成功',
  quota_hint: '配额由服务端每几分钟上报一次，这里的数字可能略有延迟。',

  signin_failed: '登录失败',
  invalid_credentials: '邮箱或密码不正确。',
  rate_limited: '尝试过于频繁，请一分钟后再试。',
  network_error: '连不上服务端。检查网络，或打开备用地址页。',
  unknown_error: '出了点问题。',
};

export type Language = 'en' | 'zh';

export function pickLanguage(uiLanguage: string | undefined): Language {
  return (uiLanguage ?? '').toLowerCase().startsWith('zh') ? 'zh' : 'en';
}

export function translator(language: Language): (key: MessageKey, vars?: Record<string, string | number>) => string {
  const dict: Record<MessageKey, string> = language === 'zh' ? zh : en;
  return (key, vars) => {
    let text: string = dict[key];
    if (vars) {
      for (const [k, v] of Object.entries(vars)) text = text.replaceAll(`{${k}}`, String(v));
    }
    return text;
  };
}

/** 页面里的默认实例：按浏览器 UI 语言选；测试环境（没有 `chrome`）退到英文。 */
export function detectLanguage(): Language {
  try {
    return pickLanguage(chrome.i18n.getUILanguage());
  } catch {
    return 'en';
  }
}

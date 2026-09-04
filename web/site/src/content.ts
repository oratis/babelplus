/**
 * 官网的全部文案与可售状态。**内容与渲染分开**，是为了让两件事可测：
 *
 *  1. **文案红线**（`content.test.ts`）：go-to-market 裁决 6 —— 一律自称 consumer privacy，
 *     绝不出现 unblock / 解锁 / 流媒体品牌 / eSIM / 电信字样。那一个编辑决定决定我们落在
 *     支付通道的「受限-可审批」还是「禁止」，比选哪家支付商更重要。
 *  2. 🔴 **不在官网暴露订阅配置**（2026-09-04 用户裁决）：软件是服务主体，
 *     代理配置是**会员中心里的服务**。所以这份文案里不出现 Clash / sing-box / YAML /
 *     订阅链接 / 节点 / 协议名 —— 一个字都不出现，由测试钉住。
 *
 * 🔴 **可售状态是 `availability` 里的三个显式布尔，不是文案里的形容词。**
 * 两个客户端都还不能分发（浏览器没签名、扩展没上架），站点必须说这件事，
 * 而不是摆一个点了没反应的下载按钮。上架 / 出包的那天改这三个值。
 */

export interface Availability {
  /** 桌面浏览器有没有可下载的签名包（desktop/README §6：签名与公证都还没有）。 */
  readonly browser: boolean;
  /** 扩展有没有上架（web/extension/store/README：E1 之前不提交）。 */
  readonly extension: boolean;
  /** 会员中心能不能自助买通行证（roadmap F8 站内收款闭环）。 */
  readonly checkout: boolean;
}

export const AVAILABILITY: Availability = {
  browser: false,
  extension: false,
  checkout: false,
};

export interface Plan {
  readonly name: string;
  readonly price: string;
  readonly data: string;
  readonly days: string;
  readonly note: string;
}

/**
 * 通行证。数字来自 go-to-market §3.2（**该文档状态是「提案，未批准」**），
 * 是目前唯一存在的一版定价。站点上不放结账按钮 —— 购买在会员中心完成（§F8 未闭环时会说明）。
 */
export const PLANS: readonly Plan[] = [
  { name: 'Trial', price: '$2.50', data: '3 GB', days: '7 days', note: 'One per account. See what it is like before a longer trip.' },
  { name: 'Short trip', price: '$4.50', data: '10 GB', days: '14 days', note: 'A conference, a two-week visit.' },
  { name: 'Standard', price: '$8.90', data: '20 GB', days: '30 days', note: 'The one most people take for a month of work.' },
  { name: 'Resident', price: '$18.90', data: '50 GB', days: '30 days', note: 'Long stays, study terms, an office month.' },
];

export interface Faq {
  readonly q: string;
  readonly a: string;
}

export const CONTENT = {
  brand: 'babel.plus',
  tagline: 'Your laptop keeps working when you land.',
  /** 首屏第二句。**说清楚边界**：只接管这一个浏览器，不是整台电脑。 */
  lede:
    'A private browser and a browser extension for people who bring a laptop to China. ' +
    'They route that browser through your babel.plus pass. Nothing else on your computer is touched.',

  primaryCtaSignedOut: 'Sign in to your account',
  secondaryCta: 'How it works',

  apps: [
    {
      key: 'browser' as const,
      title: 'The browser',
      for: 'For the evening you land with a clean laptop.',
      body:
        'A browser that arrives ready. Enter the email you bought your pass with and it connects — no store account, ' +
        'no configuration file, no separate app to install first. Every tab tells you whether it went through us or ' +
        'went direct, and a page that fails says so instead of quietly falling back.',
      platforms: 'macOS (Apple silicon and Intel) · Windows 10 and 11',
    },
    {
      key: 'extension' as const,
      title: 'The extension',
      for: 'For the Chrome or Edge you already use.',
      body:
        'One button in the toolbar. It routes that browser and shows what is left on your pass. ' +
        'It never reads the pages you visit — there is no content script and nothing is logged.',
      platforms: 'Chrome · Edge',
    },
  ],

  /** 🔴 这一节是**刻意放在首屏之后第二位**的：先说做不到什么，再谈价格。 */
  limits: {
    title: 'What it does not do',
    items: [
      'It routes one browser. Chat clients, editors, terminals and everything else on your computer are unaffected.',
      'It is not free, and there is no free trial. The cheapest way to find out whether it suits you is a 7-day pass.',
      'We do not promise that any network works at any moment. When a server cannot be reached the app says so, ' +
        'with the reason, instead of pretending to be connected.',
      'We do not keep a record of the pages you visit. Our servers count bytes for billing; that is all.',
    ],
  },

  how: {
    title: 'How it works',
    steps: [
      { t: 'Get a pass', d: 'Create an account and choose how much data and how many days you need.' },
      { t: 'Open the app', d: 'Sign in with the same email. The app picks the fastest server on your pass.' },
      { t: 'Use the browser', d: 'Sites that work locally stay local and fast. Everything else goes through us.' },
    ],
  },

  plansTitle: 'Passes',
  plansNote:
    'A pass is data plus days. Data does not roll over after a pass ends, and unused data carries over only if you renew before it does. ' +
    'Passes are bought and managed in your account.',

  faqTitle: 'Questions people actually ask',
  faq: [
    {
      q: 'What happens when my data runs out?',
      a: 'The app disconnects and tells you. It does not slow you down quietly — being throttled without being told is worse than being stopped.',
    },
    {
      q: 'Can I use it on more than one computer?',
      a: 'Yes. Your account has a device allowance and the app shows what is in use.',
    },
    {
      q: 'Do you keep logs?',
      a: 'We count bytes per account, because that is what a pass is. We do not record the addresses you visit.',
    },
    {
      q: 'What if it stops working while I am there?',
      a: 'The apps carry a list of backup addresses and tell you when none of them can be reached. Support is by email; the address is in your account.',
    },
  ] as readonly Faq[],

  footer: {
    signIn: 'Account',
    help: 'Help',
    status: 'Status',
    legal: 'Terms · Privacy',
  },
} as const;

/** 会员中心与帮助站的地址。构建期注入，缺省为空 —— 空则渲染成不可点，**不编域名**。 */
export interface SiteLinks {
  readonly account: string;
  readonly help: string;
  readonly status: string;
}

export function linksFromEnv(env: Record<string, string | undefined>): SiteLinks {
  return {
    account: env['VITE_BP_ACCOUNT_URL']?.trim() ?? '',
    help: env['VITE_BP_HELP_URL']?.trim() ?? '',
    status: env['VITE_BP_STATUS_URL']?.trim() ?? '',
  };
}

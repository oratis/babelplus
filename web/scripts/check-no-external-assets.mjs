/**
 * 「不引用任何第三方资源」的自动检查。
 *
 * 为什么要有这个脚本：ADR 0003 §5 的「字体、图标等一切外部资源自托管」是一条
 * **很容易在某次提交里被悄悄破坏**的纪律 —— 复制一段示例代码、装一个带 CDN 引用的组件库、
 * 图省事写一个 `<img src="https://…">`，都会引入一个我们控制不了、可达性未知的主机名。
 * 人工评审抓不住这类改动，所以让 CI 抓。
 *
 * 检查的是**构建产物**（dist），不是源码 —— 源码里没有不代表打包后没有。
 *
 * 三层检查，严格程度递减：
 *   ① 点名的高风险主机名，出现在任何位置即失败（含 JS 字符串里的 fetch 目标）
 *   ② HTML 的 src/href 与 CSS 的 url() —— 这些是**确定会被取**的资源，绝对 URL 一律失败
 *   ③ 其余位置出现的绝对 URL，必须在 ALLOWED_HOSTS 里带理由登记，否则失败
 *
 * 第 ③ 层看起来啰嗦，但它是这个脚本唯一能长期有效的原因：
 * 白名单里的每一条都必须写清楚「为什么它不会被取」，
 * 而写不出理由的那一条，通常就是真的会被取的那一条。
 *
 * 用法：`pnpm -r build && pnpm lint:no-external`
 */
import { readdir, readFile } from 'node:fs/promises';
import { existsSync } from 'node:fs';
import { join, extname } from 'node:path';
import { fileURLToPath } from 'node:url';

const WEB_ROOT = fileURLToPath(new URL('..', import.meta.url));
const TARGETS = ['user/dist', 'admin/dist', 'extension/dist'];
const SCANNED_EXT = new Set(['.html', '.js', '.css', '.mjs']);

/** ① 点名的高风险主机名。命中即失败，附理由 —— 报错时给理由比只说「不允许」更可能被正确处理。 */
const FORBIDDEN_HOSTS = [
  [
    'fonts.googleapis.com',
    'Google Fonts。理由不是「被封」（OONI 显示约 90% 可达），而是消除不可控的第三方主机名（ADR 0003 §5）',
  ],
  ['fonts.gstatic.com', '同 fonts.googleapis.com'],
  [
    'challenges.cloudflare.com',
    'Cloudflare Turnstile —— 中国大陆不可用，不在 China Network 可用产品清单里（ADR 0003 §3.2）',
  ],
  ['google.com/recaptcha', 'Google reCAPTCHA —— google.com 在大陆完全封锁'],
  ['hcaptcha.com', 'hCaptcha —— 其 CDN 主机名在大陆的可达性无任何 OONI 数据，不可假设'],
  ['cloudflare-dns.com', 'Cloudflare DoH —— 41,969/41,969 dnscheck 失败，大陆完全不可用'],
  ['one.one.one.one', 'Cloudflare DoH —— OONI 115/115 全异常'],
  ['dns.google', 'Google DoH —— google.com 在大陆完全封锁'],
  ['cdn.jsdelivr.net', '第三方 CDN'],
  ['unpkg.com', '第三方 CDN'],
  ['cdnjs.cloudflare.com', '第三方 CDN'],
  ['ajax.googleapis.com', '第三方 CDN'],
  [
    'algolia.net',
    'Algolia —— 免费档托管只在 US/UK/EU West，每次敲键都跨太平洋（ADR 0003 §3.4）。问题不是「被封」是「太远」',
  ],
  ['api.telegram.org', 'Telegram —— 大陆异常率约 98%（ADR 0002 §3.1）'],
];

/**
 * ③ 允许出现在产物里的主机名。**每一条都必须写明为什么它不会被取。**
 * 加一条之前先确认：它真的只是一段文本，而不是某个运行时会去请求的地址。
 */
const ALLOWED_HOSTS = [
  ['www.w3.org', 'SVG / XML 命名空间标识符。浏览器不会去取命名空间 URI'],
  ['react.dev', 'React 运行时报错信息里的文档链接，纯字符串'],
  ['reactrouter.com', 'react-router 报错信息里的文档链接，纯字符串'],
  ['tailwindcss.com', 'Tailwind 生成的注释 / 报错文本'],
  ['github.com', '依赖包报错信息里的 issue 链接，纯字符串'],
  ['localhost', 'Vite 开发期提示文本；生产构建里不会被请求'],
  ['example.com', 'RFC 2606 保留域名，用于配置文件注释中的示例'],
  ['example.invalid', 'RFC 2606 保留后缀，永不解析，用于 UI 占位'],
  // 扩展 onboarding 第三步的四个方块：**用户点击才打开**的 target=_blank 外链（spec §3.5 的验证页），
  // 扩展自己不请求它们；它们是「你现在能打开什么」的证据，不是资源。
  // 🔴 撤销条件：若将来扩展自己去 fetch 这些站点做连通性探测，这四条必须先删掉再讨论 ——
  //    探测目标必须是我们自己的 probe_url（openapi ProxyEndpoint.probe_url），不能是第三方站点。
  ['www.google.com', '扩展 onboarding 的用户点击外链，不请求'],
  ['www.youtube.com', '同上'],
  ['web.whatsapp.com', '同上'],
  ['chatgpt.com', '同上'],
  [
    'tronscan.org',
    // 出现在两处，都不是取资源：
    //  ① 后台 D6「手工标记订单已支付」的证据链接输入框 placeholder —— 它是给操作者看的
    //     形态示例（服务端认四种形态），前端不解析、不请求、也不做格式校验。
    //  ② web/shared/api/schema.d.ts —— 生成物，来自已冻结的 openapi 里同一段 description。
    // 🔴 撤销条件：哪天前端真的要去链上浏览器查一笔交易（例如收银台自动核对 txid），
    //    这条白名单必须**先删掉**再讨论 —— 那时它就成了一个真实的外部依赖，
    //    而外部依赖正是 ADR 0003 §5 要在墙内可用性上避免的东西。
    'D6 证据链接的 placeholder 示例文本与 openapi description 的生成物，前端不请求它',
  ],
];

/** ② 确定会被取的资源位置。 */
const FETCH_VECTORS = [
  [/<[^>]+\ssrc\s*=\s*["']https?:\/\/[^"']+["']/gi, 'HTML src'],
  [/<[^>]+\shref\s*=\s*["']https?:\/\/[^"']+["']/gi, 'HTML href'],
  [/url\(\s*["']?https?:\/\/[^)"']+/gi, 'CSS url()'],
  [/@import\s+(?:url\()?\s*["']https?:\/\//gi, 'CSS @import'],
];

const ABSOLUTE_URL = /\bhttps?:\/\/[a-z0-9.-]+(?::\d+)?(?:\/[^\s"'`)<>\\]*)?/gi;

async function* walk(dir) {
  for (const entry of await readdir(dir, { withFileTypes: true })) {
    const full = join(dir, entry.name);
    if (entry.isDirectory()) yield* walk(full);
    else yield full;
  }
}

function hostOf(url) {
  return url.replace(/^https?:\/\//i, '').split(/[/:?#]/)[0];
}

const errors = [];
let scanned = 0;
const unknownHosts = new Map();

for (const target of TARGETS) {
  const dir = join(WEB_ROOT, target);
  if (!existsSync(dir)) {
    errors.push(`${target} 不存在。先跑 pnpm -r build。`);
    continue;
  }

  for await (const file of walk(dir)) {
    if (!SCANNED_EXT.has(extname(file))) continue;
    scanned += 1;
    const text = await readFile(file, 'utf8');
    const rel = file.slice(WEB_ROOT.length);

    // ① 高风险主机名
    for (const [host, why] of FORBIDDEN_HOSTS) {
      if (text.includes(host)) errors.push(`${rel} 引用了 ${host}\n    ${why}`);
    }

    // ② 确定会被取的位置
    if (extname(file) === '.html' || extname(file) === '.css') {
      for (const [re, label] of FETCH_VECTORS) {
        for (const m of text.matchAll(re)) {
          errors.push(`${rel} 的 ${label} 指向了外部地址：${m[0].slice(0, 120)}`);
        }
      }
    }

    // ③ 其余绝对 URL
    for (const m of text.matchAll(ABSOLUTE_URL)) {
      const host = hostOf(m[0]);
      if (ALLOWED_HOSTS.some(([allowed]) => host === allowed || host.endsWith(`.${allowed}`))) continue;
      unknownHosts.set(host, (unknownHosts.get(host) ?? 0) + 1);
    }
  }
}

if (unknownHosts.size > 0) {
  errors.push(
    '产物里出现了未登记的外部主机名：\n' +
      [...unknownHosts]
        .sort((a, b) => b[1] - a[1])
        .map(([host, n]) => `    ${host}  ×${n}`)
        .join('\n') +
      '\n    一切外部资源必须自托管（ADR 0003 §5）。如果它只是一段不会被请求的文本，' +
      '\n    把它加进本脚本的 ALLOWED_HOSTS 并写清楚理由。',
  );
}

if (errors.length > 0) {
  console.error('✗ 外部资源检查未通过：\n');
  for (const e of errors) console.error(`  - ${e}`);
  console.error(`\n扫描了 ${scanned} 个文件。`);
  process.exit(1);
}

console.log(`✓ 无第三方资源引用（扫描 ${scanned} 个文件，${TARGETS.length} 个产物目录）。`);

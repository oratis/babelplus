/**
 * 把 `content.ts` 渲染成 HTML 片段。**在构建期跑**（见 vite.config.ts 的插件），
 * 所以产出的 `index.html` 是**零 JavaScript 的静态页**。
 *
 * 为什么不在浏览器里渲染：这一页要在「人已经在中国、还没有可用代理」的那一刻打开。
 * 那一刻多一个可能失败的东西（一个 JS 包）就多一分打不开的概率，
 * 而它恰好是用户唯一能找到「怎么办」的地方。ADR 0003 §4 记着大陆跨境链路每天有
 * 5 小时以上低于 1 Mbps —— 体积与依赖在这里是可达性问题，不是性能偏好。
 *
 * ── 版式（2026-09-04 按用户指定改版）────────────────────────────────
 * 形态照 openai.com/index/* 那一类**发布页**：eyebrow → 超大紧字距标题 → 灰色 dek →
 * 主视觉 → 窄栏长文，留白分隔章节而不是卡片网格，药丸按钮，近黑配纯白、几乎不用彩色。
 * ⚠️ 原页面当天打不开（Cloudflare 人机验证 + WebFetch 403），所以这是按该类页面的
 * 通用形态实现的，不是逐像素复刻 —— 这一点写在这里，免得后人以为对过稿。
 * 🔴 两处**不能照抄**：字体只能用系统栈（ADR 0003 §5 禁一切外部资源，CI 会扫产物），
 * 主视觉必须是内联 SVG（同上，不能引图片）。
 */
import { AVAILABILITY, CONTENT, PLANS, type Availability, type SiteLinks } from './content.ts';

export function esc(s: string): string {
  return s.replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' })[c] ?? c);
}

/** 有地址就是链接，没有就是一段不可点的文字 —— **不编域名**（AGENTS.md §3）。 */
function link(href: string, text: string, cls = ''): string {
  const c = cls ? ` class="${cls}"` : '';
  return href ? `<a${c} href="${esc(href)}">${esc(text)}</a>` : `<span${c} aria-disabled="true">${esc(text)}</span>`;
}

/**
 * 一个客户端的可下载状态。
 * 🔴 没有可分发的包时**说出来**，而不是摆一个点了没反应的按钮 ——
 * 后者是这类站点最常见的谎，且用户会在最需要信任的那一刻发现它。
 */
function appCta(available: boolean, kind: 'browser' | 'extension'): string {
  if (available) {
    return kind === 'browser'
      ? '<a class="pill pill--solid" href="/download">Download</a>'
      : '<a class="pill pill--solid" href="/extension">Add to your browser</a>';
  }
  const what = kind === 'browser' ? 'The desktop app is not released yet.' : 'The extension is not in the stores yet.';
  return `<p class="pending">${esc(what)} It is being tested; nothing to download today.</p>`;
}

/**
 * 主视觉：内联 SVG，画的是这个产品的实际形状 ——
 * 一个浏览器窗口，标签页上带着「这一页走了代理 / 直连」的角标，右上角是配额胶囊。
 * 不用装饰性抽象图：这一页要回答的第一个问题是「它到底是个什么东西」。
 */
function heroFigure(): string {
  return `
  <figure class="figure">
    <svg class="figure__art" viewBox="0 0 1200 620" role="img" aria-labelledby="figure-t" preserveAspectRatio="xMidYMid meet">
      <title id="figure-t">A browser window with a per-tab routing badge and a data allowance in the toolbar</title>
      <defs>
        <clipPath id="win"><rect x="60" y="60" width="1080" height="500" rx="16" /></clipPath>
      </defs>
      <rect x="60" y="60" width="1080" height="500" rx="16" class="a-win" />
      <rect x="60" y="60" width="1080" height="52" class="a-chrome" clip-path="url(#win)" />
      <!-- 标签：走代理 / 直连 / 本页有资源失败 -->
      <g class="a-tab"><rect x="80" y="74" width="230" height="38" rx="8" class="a-tab-on" />
        <rect x="94" y="84" width="18" height="18" rx="5" class="a-badge-proxy" />
        <rect x="122" y="88" width="150" height="10" rx="5" class="a-text-strong" /></g>
      <g class="a-tab"><rect x="316" y="78" width="200" height="34" rx="8" class="a-tab-off" />
        <rect x="330" y="86" width="18" height="18" rx="5" class="a-badge-direct" />
        <rect x="358" y="90" width="120" height="9" rx="5" class="a-text" /></g>
      <g class="a-tab"><rect x="522" y="78" width="200" height="34" rx="8" class="a-tab-off" />
        <rect x="536" y="86" width="18" height="18" rx="5" class="a-badge-warn" />
        <rect x="564" y="90" width="120" height="9" rx="5" class="a-text" /></g>
      <!-- 地址栏与配额胶囊 -->
      <rect x="60" y="112" width="1080" height="56" class="a-bar" clip-path="url(#win)" />
      <rect x="84" y="128" width="740" height="24" rx="12" class="a-omni" />
      <rect x="100" y="135" width="10" height="10" rx="3" class="a-badge-proxy" />
      <rect x="122" y="136" width="260" height="8" rx="4" class="a-text" />
      <rect x="856" y="126" width="260" height="28" rx="14" class="a-pill" />
      <rect x="876" y="136" width="26" height="8" rx="4" class="a-text-strong" />
      <rect x="914" y="136" width="1" height="9" class="a-rule" />
      <rect x="926" y="136" width="120" height="8" rx="4" class="a-text" />
      <!-- 页面内容骨架 -->
      <rect x="112" y="212" width="420" height="26" rx="6" class="a-text-strong" />
      <rect x="112" y="258" width="700" height="12" rx="6" class="a-text" />
      <rect x="112" y="284" width="640" height="12" rx="6" class="a-text" />
      <rect x="112" y="310" width="520" height="12" rx="6" class="a-text" />
      <g class="a-cards">
        <rect x="112" y="360" width="220" height="130" rx="12" class="a-card" />
        <rect x="352" y="360" width="220" height="130" rx="12" class="a-card" />
        <rect x="592" y="360" width="220" height="130" rx="12" class="a-card" />
        <rect x="832" y="360" width="236" height="130" rx="12" class="a-card" />
      </g>
    </svg>
    <figcaption>Every tab says whether it went through babel.plus or went direct. The toolbar shows what is left on your pass.</figcaption>
  </figure>`;
}

/** 品牌图形：经线球，与两个客户端的角标同源（index.html 的 favicon 是同一张图）。 */
function brandGlyph(): string {
  return `<svg viewBox="0 0 32 32" fill="none" stroke="currentColor" aria-hidden="true"><circle cx="16" cy="16" r="11" stroke-width="3"/><ellipse cx="16" cy="16" rx="4.6" ry="11" stroke-width="2"/><path d="M3 16h26" stroke-width="2.4" stroke-linecap="round"/></svg>`;
}

export interface RenderInput {
  readonly links: SiteLinks;
  readonly availability?: Availability;
}

export function renderPage(input: RenderInput): string {
  const a = input.availability ?? AVAILABILITY;
  const { links } = input;
  const c = CONTENT;

  const apps = c.apps
    .map(
      (app) => `
        <div class="app">
          <h3>${esc(app.title)}</h3>
          <p class="app__for">${esc(app.for)}</p>
          <p>${esc(app.body)}</p>
          <p class="meta">${esc(app.platforms)}</p>
          ${appCta(app.key === 'browser' ? a.browser : a.extension, app.key)}
        </div>`,
    )
    .join('');

  const limits = c.limits.items.map((i) => `<li>${esc(i)}</li>`).join('');

  const steps = c.how.steps
    .map(
      (s, i) => `
        <li>
          <span class="step__n">${i + 1}</span>
          <div><b>${esc(s.t)}</b><p>${esc(s.d)}</p></div>
        </li>`,
    )
    .join('');

  const plans = PLANS.map(
    (p) => `
      <tr>
        <th scope="row">${esc(p.name)}</th>
        <td class="num">${esc(p.price)}</td>
        <td class="num">${esc(p.data)}</td>
        <td class="num">${esc(p.days)}</td>
        <td class="plan__note">${esc(p.note)}</td>
      </tr>`,
  ).join('');

  // 结账没开通时必须明说 —— 「价目表在这里但现在买不了」是一句诚实的话，
  // 而一个跳到半路的购买按钮不是。
  const plansCta = a.checkout
    ? `<p class="cta-row">${link(links.account, 'Get a pass', 'pill pill--solid')}</p>`
    : `<p class="pending">Self-service purchase is not open yet. Accounts are by invitation while we finish it.</p>`;

  // 读数条：两个客户端各自的平台清单，原样取自 content.ts（不新增文案）。
  const readout = c.apps.map((app) => `<li>${esc(app.platforms)}</li>`).join('');

  const faq = c.faq
    .map((f) => `<div class="qa"><h3>${esc(f.q)}</h3><p>${esc(f.a)}</p></div>`)
    .join('');

  return `
  <nav class="topbar">
    <div class="topbar__in">
      <a class="brand" href="/"><span class="brand__mark" aria-hidden="true">${brandGlyph()}</span><span>${esc(c.brand)}</span><span class="brand__sub"><i class="led" aria-hidden="true"></i>console</span></a>
      <div class="topbar__links">
        ${link(links.help, c.footer.help, 'topbar__link')}
        ${link(links.account, c.footer.signIn, 'pill pill--ghost')}
      </div>
    </div>
  </nav>

  <article>
    <div class="hero-wrap">
    <header class="lede-block">
      <p class="eyebrow">Product</p>
      <h1>${esc(c.tagline)}</h1>
      <p class="dek">${esc(c.lede)}</p>
      <p class="cta-row">
        ${link(links.account, c.primaryCtaSignedOut, 'pill pill--solid')}
        <a class="pill pill--ghost" href="#how">${esc(c.secondaryCta)}</a>
      </p>
      <ul class="readout" aria-label="Platforms">${readout}</ul>
    </header>
    </div>

    ${heroFigure()}

    <section id="apps" aria-labelledby="apps-h">
      <h2 id="apps-h">Two ways to use it</h2>
      <div class="apps">${apps}</div>
    </section>

    <section id="limits" aria-labelledby="limits-h">
      <h2 id="limits-h">${esc(c.limits.title)}</h2>
      <ul class="limits">${limits}</ul>
    </section>

    <section id="how" aria-labelledby="how-h">
      <h2 id="how-h">${esc(c.how.title)}</h2>
      <ol class="steps">${steps}</ol>
    </section>

    <section id="plans" aria-labelledby="plans-h">
      <h2 id="plans-h">${esc(c.plansTitle)}</h2>
      <div class="tablewrap">
        <table class="plans">
          <thead><tr><th scope="col">Pass</th><th scope="col">Price</th><th scope="col">Data</th><th scope="col">Days</th><th scope="col">Who it is for</th></tr></thead>
          <tbody>${plans}</tbody>
        </table>
      </div>
      <p class="note">${esc(c.plansNote)}</p>
      ${plansCta}
    </section>

    <section id="faq" aria-labelledby="faq-h">
      <h2 id="faq-h">${esc(c.faqTitle)}</h2>
      <div class="qas">${faq}</div>
    </section>
  </article>

  <footer>
    <div class="footer__in">
      <div class="brand"><span class="brand__mark" aria-hidden="true">${brandGlyph()}</span><span>${esc(c.brand)}</span></div>
      <nav class="footer__nav">
        ${link(links.account, c.footer.signIn)}
        ${link(links.help, c.footer.help)}
        ${link(links.status, c.footer.status)}
      </nav>
    </div>
  </footer>`;
}

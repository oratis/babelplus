/**
 * 把 `content.ts` 渲染成 HTML 片段。**在构建期跑**（见 vite.config.ts 的插件），
 * 所以产出的 `index.html` 是**零 JavaScript 的静态页**。
 *
 * 为什么不在浏览器里渲染：这一页要在「人已经在中国、还没有可用代理」的那一刻打开。
 * 那一刻多一个可能失败的东西（一个 JS 包）就多一分打不开的概率，
 * 而它恰好是用户唯一能找到「怎么办」的地方。ADR 0003 §4 记着大陆跨境链路每天有
 * 5 小时以上低于 1 Mbps —— 体积与依赖在这里是可达性问题，不是性能偏好。
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
      ? '<a class="btn btn--go" href="/download">Download</a>'
      : '<a class="btn btn--go" href="/extension">Add to your browser</a>';
  }
  const what = kind === 'browser' ? 'The desktop app is not released yet.' : 'The extension is not in the stores yet.';
  return `<p class="pending"><span class="dot"></span>${esc(what)} It is being tested; nothing to download today.</p>`;
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
        <article class="app">
          <h3>${esc(app.title)}</h3>
          <p class="app__for">${esc(app.for)}</p>
          <p>${esc(app.body)}</p>
          <p class="app__plat mono">${esc(app.platforms)}</p>
          ${appCta(app.key === 'browser' ? a.browser : a.extension, app.key)}
        </article>`,
    )
    .join('');

  const limits = c.limits.items.map((i) => `<li>${esc(i)}</li>`).join('');

  const steps = c.how.steps
    .map(
      (s, i) => `
        <li class="step">
          <span class="step__n mono">${i + 1}</span>
          <div><b>${esc(s.t)}</b><p>${esc(s.d)}</p></div>
        </li>`,
    )
    .join('');

  const plans = PLANS.map(
    (p) => `
      <article class="plan">
        <h3>${esc(p.name)}</h3>
        <p class="plan__price mono">${esc(p.price)}</p>
        <p class="plan__spec mono">${esc(p.data)} · ${esc(p.days)}</p>
        <p class="plan__note">${esc(p.note)}</p>
      </article>`,
  ).join('');

  // 结账没开通时必须明说 —— 「价目表在这里但现在买不了」是一句诚实的话，
  // 而一个跳到半路的购买按钮不是。
  const plansCta = a.checkout
    ? `<p class="plans__cta">${link(links.account, 'Get a pass', 'btn btn--go')}</p>`
    : `<p class="pending"><span class="dot"></span>Self-service purchase is not open yet. Accounts are by invitation while we finish it.</p>`;

  const faq = c.faq
    .map((f) => `<div class="faq__i"><h3>${esc(f.q)}</h3><p>${esc(f.a)}</p></div>`)
    .join('');

  return `
  <header class="hero">
    <div class="wrap">
      <div class="brand"><i class="glyph" aria-hidden="true"></i><span>${esc(c.brand)}</span></div>
      <h1>${esc(c.tagline)}</h1>
      <p class="lede">${esc(c.lede)}</p>
      <p class="hero__cta">
        ${link(links.account, c.primaryCtaSignedOut, 'btn btn--go')}
        <a class="btn btn--ghost" href="#how">${esc(c.secondaryCta)}</a>
      </p>
    </div>
  </header>

  <main>
    <section class="wrap" id="apps" aria-labelledby="apps-h">
      <h2 id="apps-h">Two ways to use it</h2>
      <div class="apps">${apps}</div>
    </section>

    <section class="wrap band" id="limits" aria-labelledby="limits-h">
      <h2 id="limits-h">${esc(c.limits.title)}</h2>
      <ul class="limits">${limits}</ul>
    </section>

    <section class="wrap" id="how" aria-labelledby="how-h">
      <h2 id="how-h">${esc(c.how.title)}</h2>
      <ol class="steps">${steps}</ol>
    </section>

    <section class="wrap" id="plans" aria-labelledby="plans-h">
      <h2 id="plans-h">${esc(c.plansTitle)}</h2>
      <div class="plans">${plans}</div>
      <p class="plans__note">${esc(c.plansNote)}</p>
      ${plansCta}
    </section>

    <section class="wrap band" id="faq" aria-labelledby="faq-h">
      <h2 id="faq-h">${esc(c.faqTitle)}</h2>
      <div class="faq">${faq}</div>
    </section>
  </main>

  <footer class="wrap">
    <div class="brand"><i class="glyph" aria-hidden="true"></i><span>${esc(c.brand)}</span></div>
    <nav class="foot__nav">
      ${link(links.account, c.footer.signIn)}
      ${link(links.help, c.footer.help)}
      ${link(links.status, c.footer.status)}
    </nav>
  </footer>`;
}

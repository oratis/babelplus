/**
 * 浏览器 chrome 的渲染层。**没有框架**：这一层只有一个 `render(snapshot)`，
 * 引一个框架换来的是几十 KB 与一套我们不控制的更新时机，而这里的更新时机
 * 恰恰要与主进程的状态推送严格对齐。
 *
 * 它拿不到 `ipcRenderer`，只能用 preload 暴露的窄接口（`window.bp`）。
 */
import { bytesShort, gb, quotaView } from '../main/quota.ts';
import type { Snapshot } from '../shared/types.ts';

interface Bp {
  snapshot(): Promise<Snapshot>;
  signIn(email: string, password: string): Promise<Snapshot>;
  signOut(): Promise<Snapshot>;
  connect(outbound?: string | null): Promise<Snapshot>;
  disconnect(): Promise<Snapshot>;
  setPrefs(patch: unknown): Promise<Snapshot>;
  routeHost(host: string): Promise<Snapshot>;
  tab(action: string, id?: number, url?: string): Promise<Snapshot>;
  openExternal(url: string): Promise<Snapshot>;
  overlay(open: boolean): Promise<Snapshot>;
  diagnostics(): Promise<string>;
  onSnapshot(cb: (s: Snapshot) => void): void;
  onNotice(cb: (n: { kind: string }) => void): void;
}
declare global {
  interface Window {
    bp: Bp;
  }
}

const $ = <T extends HTMLElement>(id: string): T => document.getElementById(id) as T;

const el = {
  tabstrip: $<HTMLDivElement>('tabstrip'),
  omni: $<HTMLInputElement>('omni'),
  omnibadge: $<HTMLSpanElement>('omnibadge'),
  pill: $<HTMLButtonElement>('pill'),
  pillgeo: $<HTMLSpanElement>('pillgeo'),
  pillquota: $<HTMLSpanElement>('pillquota'),
  panel: $<HTMLDivElement>('panel'),
  quotaval: $<HTMLSpanElement>('quotaval'),
  quotadays: $<HTMLSpanElement>('quotadays'),
  quotafill: $<HTMLDivElement>('quotafill'),
  region: $<HTMLSelectElement>('region'),
  mode: $<HTMLSelectElement>('mode'),
  tabbytes: $<HTMLSpanElement>('tabbytes'),
  exittag: $<HTMLSpanElement>('exittag'),
  connect: $<HTMLButtonElement>('connect'),
  topup: $<HTMLButtonElement>('topup'),
  signout: $<HTMLButtonElement>('signout'),
  blockbar: $<HTMLDivElement>('blockbar'),
  blockhost: $<HTMLElement>('blockhost'),
  blockroute: $<HTMLButtonElement>('blockroute'),
  blockdismiss: $<HTMLButtonElement>('blockdismiss'),
  banner: $<HTMLDivElement>('banner'),
  onboarding: $<HTMLDivElement>('onboarding'),
  obform: $<HTMLFormElement>('obform'),
  obemail: $<HTMLInputElement>('obemail'),
  obpassword: $<HTMLInputElement>('obpassword'),
  oberror: $<HTMLParagraphElement>('oberror'),
  obsubmit: $<HTMLButtonElement>('obsubmit'),
  obdone: $<HTMLDivElement>('obdone'),
  obdonep: $<HTMLParagraphElement>('obdonep'),
  obh: $<HTMLHeadingElement>('obh'),
  obp: $<HTMLParagraphElement>('obp'),
  obring: $<HTMLDivElement>('obring'),
  obstart: $<HTMLButtonElement>('obstart'),
  settings: $<HTMLDivElement>('settings'),
  settingsOpen: $<HTMLButtonElement>('settings-open'),
  settingsClose: $<HTMLButtonElement>('settings-close'),
  setMode: $<HTMLSelectElement>('set-mode'),
  setRegion: $<HTMLSelectElement>('set-region'),
  setAlways: $<HTMLTextAreaElement>('set-always'),
  setNever: $<HTMLTextAreaElement>('set-never'),
  saveAlways: $<HTMLButtonElement>('save-always'),
  saveNever: $<HTMLButtonElement>('save-never'),
  setLaunch: $<HTMLButtonElement>('set-launch'),
  setVersion: $<HTMLSpanElement>('set-version'),
  copyDiag: $<HTMLButtonElement>('copy-diag'),
  setSignout: $<HTMLButtonElement>('set-signout'),
  back: $<HTMLButtonElement>('back'),
  forward: $<HTMLButtonElement>('forward'),
  reload: $<HTMLButtonElement>('reload'),
};

let snap: Snapshot | null = null;
let omniFocused = false;
let dismissedBlock: string | null = null;

const BADGE: Record<string, string> = { proxy: '↗', direct: '·', failed: '⚠' };

/**
 * 连接状态 → 横幅文案。**每一种状态说的是不同的话** ——
 * 尤其 `degraded`：内核挂了但代理**故意**没撤，所以这里必须明说「流量没有改走直连」，
 * 否则用户会以为「反正还能上网」，而实际上他什么都打不开。
 */
function bannerFor(s: Snapshot): { html: string; retry: boolean } | null {
  const c = s.connection;
  if (c.status === 'degraded') {
    return {
      html: '<b>The tunnel stopped.</b> Nothing switched to a direct connection — pages will fail until it is back.',
      retry: true,
    };
  }
  if (c.status === 'failed') {
    const detail = c.lastError?.detail ?? '';
    const head: Record<string, string> = {
      'not-signed-in': 'Sign in to connect.',
      'no-subscription': "Couldn't read your pass from your account.",
      'subscription-empty': 'Your pass has no servers right now.',
      'config-rejected': 'The bundled core rejected the generated configuration.',
      'core-missing': 'The bundled core is missing.',
      'core-crashed': 'The bundled core keeps stopping.',
      'port-unavailable': "The local port didn't open.",
      network: "Can't reach the service.",
    };
    const reason = c.lastError?.reason ?? 'network';
    return { html: `<b>${head[reason] ?? 'Not connected.'}</b> ${escapeHtml(detail)}`, retry: true };
  }
  return null;
}

function escapeHtml(s: string): string {
  return s.replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' })[c] ?? c);
}

function render(s: Snapshot): void {
  snap = s;

  // ---- onboarding ----
  const needsOnboarding = s.onboarding || !s.signedIn;
  el.onboarding.hidden = !needsOnboarding;
  if (needsOnboarding) {
    const connected = s.connection.status === 'on';
    el.obform.hidden = s.signedIn;
    el.obdone.hidden = !s.signedIn;
    if (s.signedIn) {
      el.obh.textContent = connected ? 'Ready' : 'Connect to get started';
      el.obp.textContent = connected
        ? 'Your traffic now leaves through babel.plus. Only this browser is affected.'
        : 'We pick the fastest server on your pass. If none can be reached we say so here.';
      el.obring.textContent = connected ? '✓' : '…';
      el.obdonep.textContent = connected ? `Exit: ${s.connection.outbound ?? '—'}` : '';
      el.obstart.textContent = connected ? 'Start browsing' : 'Connect';
    }
  }

  // ---- tabs ----
  el.tabstrip.replaceChildren();
  for (const t of s.tabs) {
    const btn = document.createElement('button');
    btn.className = `tab${t.id === s.activeTabId ? ' tab--on' : ''}`;
    btn.onclick = () => void window.bp.tab('select', t.id);
    const badge = document.createElement('span');
    badge.className = `rt rt--${t.route}`;
    badge.textContent = BADGE[t.route] ?? '·';
    badge.title = t.route === 'proxy' ? 'Routed through babel.plus' : t.route === 'direct' ? 'Direct' : 'This page failed to load';
    const txt = document.createElement('span');
    txt.className = 'tab__txt';
    txt.textContent = t.loading ? 'Loading…' : t.title || t.url;
    const x = document.createElement('button');
    x.className = 'tab__x';
    x.textContent = '✕';
    x.setAttribute('aria-label', 'Close tab');
    x.onclick = (e) => {
      e.stopPropagation();
      void window.bp.tab('close', t.id);
    };
    btn.append(badge, txt, x);
    el.tabstrip.append(btn);
  }
  const plus = document.createElement('button');
  plus.className = 'newtab';
  plus.textContent = '+';
  plus.setAttribute('aria-label', 'New tab');
  plus.onclick = () => void window.bp.tab('create');
  el.tabstrip.append(plus);

  const active = s.tabs.find((t) => t.id === s.activeTabId) ?? null;
  if (active && !omniFocused) el.omni.value = active.url;
  el.omnibadge.textContent = active ? (BADGE[active.route] ?? '·') : '·';

  // ---- pill ----
  const q = quotaView(s.subscription);
  el.pillgeo.textContent = s.connection.outbound ?? (s.signedIn ? 'Off' : '—');
  el.pillquota.textContent = !s.signedIn
    ? 'not signed in'
    : q
      ? q.hasQuota
        ? `${gb(q.usedBytes)} / ${gb(q.totalBytes)} GB`
        : `${gb(q.usedBytes)} GB`
      : '—';
  el.pill.className = `pill${s.connection.status === 'on' ? ' pill--on' : ''}${
    s.connection.status === 'degraded' || s.connection.status === 'failed' ? ' pill--bad' : ''
  }`;

  // ---- panel ----
  el.quotaval.textContent = q ? (q.hasQuota ? `${gb(q.usedBytes)} / ${gb(q.totalBytes)} GB` : `${gb(q.usedBytes)} GB`) : '—';
  el.quotadays.textContent = q ? (q.daysLeft === null ? 'no expiry' : `${Math.max(0, q.daysLeft)} days left`) : '—';
  el.quotafill.style.width = q ? `${Math.round(q.usedFraction * 100)}%` : '0';
  el.quotafill.className = `fill${q?.exhausted ? ' fill--bad' : q?.low ? ' fill--warn' : ''}`;
  el.tabbytes.textContent = active ? bytesShort(active.bytes) : '—';
  el.exittag.textContent = s.connection.outbound ?? '—';
  el.connect.textContent = s.connection.status === 'on' ? 'Disconnect' : s.connection.status === 'starting' ? 'Connecting…' : 'Connect';
  el.connect.className = `btn ${s.connection.status === 'on' ? 'btn--stop' : 'btn--go'}`;
  el.connect.disabled = s.connection.status === 'starting';

  const regions = [{ tag: '', label: 'Automatic' }, ...s.regions];
  el.region.replaceChildren(
    ...regions.map((r) => {
      const o = document.createElement('option');
      o.value = r.tag;
      o.textContent = r.label;
      o.selected = (s.prefs.outbound ?? '') === r.tag;
      return o;
    }),
  );
  el.mode.value = s.prefs.mode;

  // ---- settings ----
  // 两个 textarea **只在没获得焦点时**跟随快照重绘：用户正在敲的内容不能被一次
  // 后台推送冲掉（连接状态变化每几秒就会推一次）。
  if (document.activeElement !== el.setAlways) el.setAlways.value = s.prefs.alwaysProxy.join('\n');
  if (document.activeElement !== el.setNever) el.setNever.value = s.prefs.neverProxy.join('\n');
  el.setMode.value = s.prefs.mode;
  el.setLaunch.setAttribute('aria-checked', String(s.prefs.launchAtStart));
  el.setVersion.textContent = s.version;
  el.setRegion.replaceChildren(
    ...regions.map((r) => {
      const o = document.createElement('option');
      o.value = r.tag;
      o.textContent = r.label;
      o.selected = (s.prefs.outbound ?? '') === r.tag;
      return o;
    }),
  );

  // ---- blocked bar ----
  const blocked = active?.blockedHost ?? null;
  const showBlocked = blocked !== null && blocked !== dismissedBlock;
  el.blockbar.hidden = !showBlocked;
  if (showBlocked && blocked) el.blockhost.textContent = blocked;

  // ---- banner ----
  const banner = bannerFor(s);
  el.banner.hidden = banner === null;
  if (banner) {
    el.banner.innerHTML = banner.html;
    if (banner.retry) {
      const b = document.createElement('button');
      b.textContent = 'Retry';
      b.onclick = () => void window.bp.connect();
      el.banner.append(b);
    }
  }
}

/** 地址栏输入 → URL。像域名的当域名，其余当搜索词。 */
function toUrl(input: string): string {
  const raw = input.trim();
  if (!raw) return 'about:blank';
  if (/^[a-z][a-z0-9+.-]*:\/\//i.test(raw)) return raw;
  if (/^[a-z0-9-]+(\.[a-z0-9-]+)+(:\d+)?(\/.*)?$/i.test(raw) || raw === 'localhost') return `https://${raw}`;
  return `https://duckduckgo.com/?q=${encodeURIComponent(raw)}`;
}

/** 按钮上闪一下结果再变回去。比一个消失不了的 toast 省事，也不会挡住下面的内容。 */
function flash(btn: HTMLButtonElement, text: string): void {
  const before = btn.textContent;
  btn.textContent = text;
  setTimeout(() => {
    btn.textContent = before;
  }, 1400);
}

function wire(): void {
  el.omni.addEventListener('focus', () => {
    omniFocused = true;
    el.omni.select();
  });
  el.omni.addEventListener('blur', () => {
    omniFocused = false;
  });
  el.omni.addEventListener('keydown', (e) => {
    if (e.key !== 'Enter' || !snap?.activeTabId) return;
    el.omni.blur();
    void window.bp.tab('navigate', snap.activeTabId, toUrl(el.omni.value));
  });
  el.back.onclick = () => snap?.activeTabId && void window.bp.tab('back', snap.activeTabId);
  el.forward.onclick = () => snap?.activeTabId && void window.bp.tab('forward', snap.activeTabId);
  el.reload.onclick = () => snap?.activeTabId && void window.bp.tab('reload', snap.activeTabId);

  el.pill.onclick = () => {
    const open = el.panel.hidden;
    el.panel.hidden = !open;
    el.pill.setAttribute('aria-expanded', String(open));
  };
  el.connect.onclick = () => {
    if (!snap) return;
    void (snap.connection.status === 'on' ? window.bp.disconnect() : window.bp.connect());
  };
  el.topup.onclick = () => snap && void window.bp.openExternal(`${snap.links.webUrl}/plan`);
  el.signout.onclick = () => void window.bp.signOut();
  el.region.onchange = () => void window.bp.connect(el.region.value || null);
  el.mode.onchange = () => void window.bp.setPrefs({ mode: el.mode.value });

  el.blockroute.onclick = () => {
    const host = el.blockhost.textContent;
    if (host) void window.bp.routeHost(host);
  };
  el.blockdismiss.onclick = () => {
    dismissedBlock = el.blockhost.textContent;
    el.blockbar.hidden = true;
  };

  el.obform.onsubmit = async (e) => {
    e.preventDefault();
    el.oberror.hidden = true;
    el.obsubmit.disabled = true;
    el.obsubmit.textContent = 'Signing in…';
    try {
      await window.bp.signIn(el.obemail.value.trim(), el.obpassword.value);
      el.obpassword.value = '';
    } catch (cause) {
      el.oberror.hidden = false;
      el.oberror.textContent = cause instanceof Error ? cause.message : String(cause);
    } finally {
      el.obsubmit.disabled = false;
      el.obsubmit.textContent = 'Sign in';
    }
  };
  el.obstart.onclick = async () => {
    if (snap?.connection.status === 'on') {
      el.onboarding.hidden = true;
      await window.bp.tab('create');
    } else {
      await window.bp.connect();
    }
  };

  const openSettings = async (open: boolean) => {
    el.settings.hidden = !open;
    el.panel.hidden = true;
    // 主进程要把标签页视图藏起来，否则这一层只会被裁到 chrome 那 96px（tabs.setOverlay）。
    await window.bp.overlay(open);
  };
  el.settingsOpen.onclick = () => void openSettings(true);
  el.settingsClose.onclick = () => void openSettings(false);
  document.addEventListener('keydown', (e) => {
    if (e.key === 'Escape' && !el.settings.hidden) void openSettings(false);
  });

  el.setMode.onchange = () => void window.bp.setPrefs({ mode: el.setMode.value });
  el.setRegion.onchange = () => void window.bp.connect(el.setRegion.value || null);
  el.saveAlways.onclick = async () => {
    await window.bp.setPrefs({ alwaysProxy: el.setAlways.value.split(/\r?\n/) });
    flash(el.saveAlways, 'Saved');
  };
  el.saveNever.onclick = async () => {
    await window.bp.setPrefs({ neverProxy: el.setNever.value.split(/\r?\n/) });
    flash(el.saveNever, 'Saved');
  };
  el.setLaunch.onclick = () =>
    void window.bp.setPrefs({ launchAtStart: el.setLaunch.getAttribute('aria-checked') !== 'true' });
  el.setSignout.onclick = async () => {
    await window.bp.signOut();
    await openSettings(false);
  };
  el.copyDiag.onclick = async () => {
    const text = await window.bp.diagnostics();
    try {
      await navigator.clipboard.writeText(text);
      flash(el.copyDiag, 'Copied');
    } catch {
      flash(el.copyDiag, 'Copy failed');
    }
  };

  window.bp.onSnapshot(render);
  window.bp.onNotice((n) => {
    if (n.kind === 'download-blocked') {
      el.banner.hidden = false;
      el.banner.textContent = 'Downloads are turned off in this version.';
    }
  });
}

wire();
void window.bp.snapshot().then(render);

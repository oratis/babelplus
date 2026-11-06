"use strict";
(() => {
  // src/main/quota.ts
  var LOW_FRACTION = 0.1;
  var DAY_MS = 864e5;
  function quotaView(summary, now = Date.now()) {
    if (!summary) return null;
    const usedBytes = Math.max(0, summary.upload_bytes + summary.download_bytes);
    const totalBytes = Math.max(0, summary.total_bytes);
    const hasQuota = totalBytes > 0;
    const remainingBytes = hasQuota ? Math.max(0, totalBytes - usedBytes) : 0;
    const expiredAt = summary.expired_at ?? null;
    const expiredAtMs = expiredAt ? Date.parse(expiredAt) : Number.NaN;
    const hasExpiry = Number.isFinite(expiredAtMs);
    const exhausted = hasQuota && usedBytes >= totalBytes;
    return {
      hasQuota,
      usedBytes,
      totalBytes,
      remainingBytes,
      usedFraction: hasQuota ? Math.min(1, usedBytes / totalBytes) : 0,
      expiredAt,
      daysLeft: hasExpiry ? Math.floor((expiredAtMs - now) / DAY_MS) : null,
      expired: hasExpiry && expiredAtMs <= now,
      exhausted,
      low: hasQuota && !exhausted && remainingBytes / totalBytes < LOW_FRACTION,
      planName: summary.plan_name ?? null
    };
  }
  function gb(bytes) {
    if (!Number.isFinite(bytes) || bytes < 0) return "\u2014";
    return (bytes / 1024 ** 3).toFixed(1);
  }
  function bytesShort(bytes) {
    if (!Number.isFinite(bytes) || bytes < 0) return "\u2014";
    if (bytes >= 1024 ** 3) return `${(bytes / 1024 ** 3).toFixed(2)} GB`;
    if (bytes >= 1024 ** 2) return `${Math.round(bytes / 1024 ** 2)} MB`;
    if (bytes >= 1024) return `${Math.round(bytes / 1024)} KB`;
    return `${Math.round(bytes)} B`;
  }

  // src/renderer/index.ts
  var $ = (id) => document.getElementById(id);
  var el = {
    tabstrip: $("tabstrip"),
    omni: $("omni"),
    omnibadge: $("omnibadge"),
    pill: $("pill"),
    pillgeo: $("pillgeo"),
    pillquota: $("pillquota"),
    panel: $("panel"),
    quotaval: $("quotaval"),
    quotadays: $("quotadays"),
    quotafill: $("quotafill"),
    region: $("region"),
    mode: $("mode"),
    tabbytes: $("tabbytes"),
    exittag: $("exittag"),
    connect: $("connect"),
    topup: $("topup"),
    signout: $("signout"),
    blockbar: $("blockbar"),
    blockhost: $("blockhost"),
    blockroute: $("blockroute"),
    blockdismiss: $("blockdismiss"),
    banner: $("banner"),
    onboarding: $("onboarding"),
    obform: $("obform"),
    obemail: $("obemail"),
    obpassword: $("obpassword"),
    oberror: $("oberror"),
    obsubmit: $("obsubmit"),
    obdone: $("obdone"),
    obdonep: $("obdonep"),
    obh: $("obh"),
    obp: $("obp"),
    obring: $("obring"),
    obstart: $("obstart"),
    settings: $("settings"),
    settingsOpen: $("settings-open"),
    settingsClose: $("settings-close"),
    setMode: $("set-mode"),
    setRegion: $("set-region"),
    setAlways: $("set-always"),
    setNever: $("set-never"),
    saveAlways: $("save-always"),
    saveNever: $("save-never"),
    setLaunch: $("set-launch"),
    setVersion: $("set-version"),
    copyDiag: $("copy-diag"),
    setSignout: $("set-signout"),
    back: $("back"),
    forward: $("forward"),
    reload: $("reload")
  };
  var snap = null;
  var omniFocused = false;
  var dismissedBlock = null;
  var BADGE = { proxy: "\u2197", direct: "\xB7", failed: "\u26A0" };
  function bannerFor(s) {
    const c = s.connection;
    if (c.status === "degraded") {
      return {
        html: "<b>The tunnel stopped.</b> Nothing switched to a direct connection \u2014 pages will fail until it is back.",
        retry: true
      };
    }
    if (c.status === "failed") {
      const detail = c.lastError?.detail ?? "";
      const head = {
        "not-signed-in": "Sign in to connect.",
        "no-subscription": "Couldn't read your pass from your account.",
        "subscription-empty": "Your pass has no servers right now.",
        "config-rejected": "The bundled core rejected the generated configuration.",
        "core-missing": "The bundled core is missing.",
        "core-crashed": "The bundled core keeps stopping.",
        "port-unavailable": "The local port didn't open.",
        network: "Can't reach the service."
      };
      const reason = c.lastError?.reason ?? "network";
      return { html: `<b>${head[reason] ?? "Not connected."}</b> ${escapeHtml(detail)}`, retry: true };
    }
    return null;
  }
  function escapeHtml(s) {
    return s.replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[c] ?? c);
  }
  function render(s) {
    snap = s;
    const needsOnboarding = s.onboarding || !s.signedIn;
    el.onboarding.hidden = !needsOnboarding;
    if (needsOnboarding) {
      const connected = s.connection.status === "on";
      el.obform.hidden = s.signedIn;
      el.obdone.hidden = !s.signedIn;
      if (s.signedIn) {
        el.obh.textContent = connected ? "Ready" : "Connect to get started";
        el.obp.textContent = connected ? "Your traffic now leaves through babel.plus. Only this browser is affected." : "We pick the fastest server on your pass. If none can be reached we say so here.";
        el.obring.textContent = connected ? "\u2713" : "\u2026";
        el.obdonep.textContent = connected ? `Exit: ${s.connection.outbound ?? "\u2014"}` : "";
        el.obstart.textContent = connected ? "Start browsing" : "Connect";
      }
    }
    el.tabstrip.replaceChildren();
    for (const t of s.tabs) {
      const btn = document.createElement("button");
      btn.className = `tab${t.id === s.activeTabId ? " tab--on" : ""}`;
      btn.onclick = () => void window.bp.tab("select", t.id);
      const badge = document.createElement("span");
      badge.className = `rt rt--${t.route}`;
      badge.textContent = BADGE[t.route] ?? "\xB7";
      badge.title = t.route === "proxy" ? "Routed through babel.plus" : t.route === "direct" ? "Direct" : "This page failed to load";
      const txt = document.createElement("span");
      txt.className = "tab__txt";
      txt.textContent = t.loading ? "Loading\u2026" : t.title || t.url;
      const x = document.createElement("button");
      x.className = "tab__x";
      x.textContent = "\u2715";
      x.setAttribute("aria-label", "Close tab");
      x.onclick = (e) => {
        e.stopPropagation();
        void window.bp.tab("close", t.id);
      };
      btn.append(badge, txt, x);
      el.tabstrip.append(btn);
    }
    const plus = document.createElement("button");
    plus.className = "newtab";
    plus.textContent = "+";
    plus.setAttribute("aria-label", "New tab");
    plus.onclick = () => void window.bp.tab("create");
    el.tabstrip.append(plus);
    const active = s.tabs.find((t) => t.id === s.activeTabId) ?? null;
    if (active && !omniFocused) el.omni.value = active.url;
    el.omnibadge.textContent = active ? BADGE[active.route] ?? "\xB7" : "\xB7";
    const q = quotaView(s.subscription);
    el.pillgeo.textContent = s.connection.outbound ?? (s.signedIn ? "Off" : "\u2014");
    el.pillquota.textContent = !s.signedIn ? "not signed in" : q ? q.hasQuota ? `${gb(q.usedBytes)} / ${gb(q.totalBytes)} GB` : `${gb(q.usedBytes)} GB` : "\u2014";
    el.pill.className = `pill${s.connection.status === "on" ? " pill--on" : ""}${s.connection.status === "degraded" || s.connection.status === "failed" ? " pill--bad" : ""}`;
    el.quotaval.textContent = q ? q.hasQuota ? `${gb(q.usedBytes)} / ${gb(q.totalBytes)} GB` : `${gb(q.usedBytes)} GB` : "\u2014";
    el.quotadays.textContent = q ? q.daysLeft === null ? "no expiry" : `${Math.max(0, q.daysLeft)} days left` : "\u2014";
    el.quotafill.style.width = q ? `${Math.round(q.usedFraction * 100)}%` : "0";
    el.quotafill.className = `fill${q?.exhausted ? " fill--bad" : q?.low ? " fill--warn" : ""}`;
    el.tabbytes.textContent = active ? bytesShort(active.bytes) : "\u2014";
    el.exittag.textContent = s.connection.outbound ?? "\u2014";
    el.connect.textContent = s.connection.status === "on" ? "Disconnect" : s.connection.status === "starting" ? "Connecting\u2026" : "Connect";
    el.connect.className = `btn ${s.connection.status === "on" ? "btn--stop" : "btn--go"}`;
    el.connect.disabled = s.connection.status === "starting";
    const regions = [{ tag: "", label: "Automatic" }, ...s.regions];
    el.region.replaceChildren(
      ...regions.map((r) => {
        const o = document.createElement("option");
        o.value = r.tag;
        o.textContent = r.label;
        o.selected = (s.prefs.outbound ?? "") === r.tag;
        return o;
      })
    );
    el.mode.value = s.prefs.mode;
    if (document.activeElement !== el.setAlways) el.setAlways.value = s.prefs.alwaysProxy.join("\n");
    if (document.activeElement !== el.setNever) el.setNever.value = s.prefs.neverProxy.join("\n");
    el.setMode.value = s.prefs.mode;
    el.setLaunch.setAttribute("aria-checked", String(s.prefs.launchAtStart));
    el.setVersion.textContent = s.version;
    el.setRegion.replaceChildren(
      ...regions.map((r) => {
        const o = document.createElement("option");
        o.value = r.tag;
        o.textContent = r.label;
        o.selected = (s.prefs.outbound ?? "") === r.tag;
        return o;
      })
    );
    const blocked = active?.blockedHost ?? null;
    const showBlocked = blocked !== null && blocked !== dismissedBlock;
    el.blockbar.hidden = !showBlocked;
    if (showBlocked && blocked) el.blockhost.textContent = blocked;
    const banner = bannerFor(s);
    el.banner.hidden = banner === null;
    if (banner) {
      el.banner.innerHTML = banner.html;
      if (banner.retry) {
        const b = document.createElement("button");
        b.textContent = "Retry";
        b.onclick = () => void window.bp.connect();
        el.banner.append(b);
      }
    }
  }
  function toUrl(input) {
    const raw = input.trim();
    if (!raw) return "about:blank";
    if (/^[a-z][a-z0-9+.-]*:\/\//i.test(raw)) return raw;
    if (/^[a-z0-9-]+(\.[a-z0-9-]+)+(:\d+)?(\/.*)?$/i.test(raw) || raw === "localhost") return `https://${raw}`;
    return `https://duckduckgo.com/?q=${encodeURIComponent(raw)}`;
  }
  function flash(btn, text) {
    const before = btn.textContent;
    btn.textContent = text;
    setTimeout(() => {
      btn.textContent = before;
    }, 1400);
  }
  function wire() {
    el.omni.addEventListener("focus", () => {
      omniFocused = true;
      el.omni.select();
    });
    el.omni.addEventListener("blur", () => {
      omniFocused = false;
    });
    el.omni.addEventListener("keydown", (e) => {
      if (e.key !== "Enter" || !snap?.activeTabId) return;
      el.omni.blur();
      void window.bp.tab("navigate", snap.activeTabId, toUrl(el.omni.value));
    });
    el.back.onclick = () => snap?.activeTabId && void window.bp.tab("back", snap.activeTabId);
    el.forward.onclick = () => snap?.activeTabId && void window.bp.tab("forward", snap.activeTabId);
    el.reload.onclick = () => snap?.activeTabId && void window.bp.tab("reload", snap.activeTabId);
    el.pill.onclick = () => {
      const open = el.panel.hidden;
      el.panel.hidden = !open;
      el.pill.setAttribute("aria-expanded", String(open));
    };
    el.connect.onclick = () => {
      if (!snap) return;
      void (snap.connection.status === "on" ? window.bp.disconnect() : window.bp.connect());
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
      el.obsubmit.textContent = "Signing in\u2026";
      try {
        await window.bp.signIn(el.obemail.value.trim(), el.obpassword.value);
        el.obpassword.value = "";
      } catch (cause) {
        el.oberror.hidden = false;
        el.oberror.textContent = cause instanceof Error ? cause.message : String(cause);
      } finally {
        el.obsubmit.disabled = false;
        el.obsubmit.textContent = "Sign in";
      }
    };
    el.obstart.onclick = async () => {
      if (snap?.connection.status === "on") {
        el.onboarding.hidden = true;
        await window.bp.tab("create");
      } else {
        await window.bp.connect();
      }
    };
    const openSettings = async (open) => {
      el.settings.hidden = !open;
      el.panel.hidden = true;
      await window.bp.overlay(open);
    };
    el.settingsOpen.onclick = () => void openSettings(true);
    el.settingsClose.onclick = () => void openSettings(false);
    document.addEventListener("keydown", (e) => {
      if (e.key === "Escape" && !el.settings.hidden) void openSettings(false);
    });
    el.setMode.onchange = () => void window.bp.setPrefs({ mode: el.setMode.value });
    el.setRegion.onchange = () => void window.bp.connect(el.setRegion.value || null);
    el.saveAlways.onclick = async () => {
      await window.bp.setPrefs({ alwaysProxy: el.setAlways.value.split(/\r?\n/) });
      flash(el.saveAlways, "Saved");
    };
    el.saveNever.onclick = async () => {
      await window.bp.setPrefs({ neverProxy: el.setNever.value.split(/\r?\n/) });
      flash(el.saveNever, "Saved");
    };
    el.setLaunch.onclick = () => void window.bp.setPrefs({ launchAtStart: el.setLaunch.getAttribute("aria-checked") !== "true" });
    el.setSignout.onclick = async () => {
      await window.bp.signOut();
      await openSettings(false);
    };
    el.copyDiag.onclick = async () => {
      const text = await window.bp.diagnostics();
      try {
        await navigator.clipboard.writeText(text);
        flash(el.copyDiag, "Copied");
      } catch {
        flash(el.copyDiag, "Copy failed");
      }
    };
    window.bp.onSnapshot(render);
    window.bp.onNotice((n) => {
      if (n.kind === "download-blocked") {
        el.banner.hidden = false;
        el.banner.textContent = "Downloads are turned off in this version.";
      }
    });
  }
  wire();
  void window.bp.snapshot().then(render);
})();

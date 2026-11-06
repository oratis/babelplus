"use strict";
var __create = Object.create;
var __defProp = Object.defineProperty;
var __getOwnPropDesc = Object.getOwnPropertyDescriptor;
var __getOwnPropNames = Object.getOwnPropertyNames;
var __getProtoOf = Object.getPrototypeOf;
var __hasOwnProp = Object.prototype.hasOwnProperty;
var __copyProps = (to, from, except, desc) => {
  if (from && typeof from === "object" || typeof from === "function") {
    for (let key of __getOwnPropNames(from))
      if (!__hasOwnProp.call(to, key) && key !== except)
        __defProp(to, key, { get: () => from[key], enumerable: !(desc = __getOwnPropDesc(from, key)) || desc.enumerable });
  }
  return to;
};
var __toESM = (mod, isNodeMode, target) => (target = mod != null ? __create(__getProtoOf(mod)) : {}, __copyProps(
  // If the importer is in node compatibility mode or this is not an ESM
  // file that has been converted to a CommonJS file using a Babel-
  // compatible transform (i.e. "__esModule" has not been set), then set
  // "default" to the CommonJS "module.exports" for node compatibility.
  isNodeMode || !mod || !mod.__esModule ? __defProp(target, "default", { value: mod, enumerable: true }) : target,
  mod
));

// src/main/index.ts
var import_electron2 = require("electron");
var import_node_path3 = require("node:path");
var import_node_fs = require("node:fs");

// src/main/api.ts
var ApiError = class extends Error {
  status;
  code;
  constructor(status, code, message) {
    super(message);
    this.name = "ApiError";
    this.status = status;
    this.code = code;
  }
};
var IDEMPOTENT = /* @__PURE__ */ new Set(["GET", "HEAD"]);
var Api = class {
  o;
  constructor(options) {
    this.o = options;
  }
  get bases() {
    return this.o.baseUrls.filter((b) => b.length > 0);
  }
  async raw(path, init) {
    const doFetch = this.o.fetchImpl ?? fetch;
    const bases = IDEMPOTENT.has(init.method) ? this.bases : this.bases.slice(0, 1);
    if (bases.length === 0) throw new ApiError(0, "NOT_CONFIGURED", "\u8FD9\u4E2A\u6784\u5EFA\u91CC\u6CA1\u6709\u914D\u7F6E\u670D\u52A1\u5730\u5740");
    let last;
    for (const base of bases) {
      try {
        return await doFetch(new URL(path, base).toString(), {
          ...init,
          signal: AbortSignal.timeout(this.o.timeoutMs ?? 15e3)
        });
      } catch (cause) {
        last = cause;
      }
    }
    throw new ApiError(0, "NETWORK", `\u8FDE\u4E0D\u4E0A\u670D\u52A1\uFF08\u8BD5\u8FC7 ${bases.length} \u4E2A\u5730\u5740\uFF09\uFF1A${String(last)}`);
  }
  /** 带鉴权的请求；401 时静默 refresh 一次再重放一次。 */
  async call(path, method, body) {
    const send = async () => {
      const headers = { Accept: "application/json" };
      const token = this.o.getToken();
      if (token) headers["Authorization"] = `Bearer ${token}`;
      if (body !== void 0) headers["Content-Type"] = "application/json";
      return this.raw(path, {
        method,
        headers,
        ...body === void 0 ? {} : { body: JSON.stringify(body) }
      });
    };
    let res = await send();
    if (res.status === 401 && this.o.getToken() && !path.endsWith("/auth/refresh")) {
      const refreshed = await this.refresh();
      if (refreshed) res = await send();
    }
    return this.unwrap(res);
  }
  async unwrap(res) {
    if (res.status === 204) return void 0;
    let parsed = null;
    const text = await res.text();
    try {
      parsed = text ? JSON.parse(text) : null;
    } catch {
      parsed = null;
    }
    if (!res.ok) {
      const code = parsed?.error?.code ?? (res.status === 501 ? "NOT_IMPLEMENTED" : "UNKNOWN");
      const message = parsed?.error?.message ?? `\u8BF7\u6C42\u5931\u8D25\uFF08HTTP ${res.status}\uFF09`;
      throw new ApiError(res.status, code, message);
    }
    if (parsed && "data" in parsed && parsed.data !== void 0) return parsed.data;
    throw new ApiError(res.status, "MALFORMED", "\u54CD\u5E94\u4E0D\u662F\u7EDF\u4E00\u4FE1\u5C01");
  }
  async refresh() {
    const stale = this.o.getToken();
    if (!stale) return false;
    try {
      const res = await this.raw("/api/v1/auth/refresh", {
        method: "POST",
        headers: { "Content-Type": "application/json", Accept: "application/json" },
        body: JSON.stringify({ refresh_token: stale })
      });
      if (!res.ok) {
        this.o.setToken(null);
        return false;
      }
      const parsed = await res.json();
      const next = parsed.data?.access_token;
      if (typeof next !== "string" || next.length === 0) {
        this.o.setToken(null);
        return false;
      }
      this.o.setToken(next);
      return true;
    } catch {
      return false;
    }
  }
  async login(email, password) {
    const tokens = await this.call("/api/v1/auth/login", "POST", { email, password });
    this.o.setToken(tokens.access_token);
  }
  async logout() {
    try {
      await this.call("/api/v1/auth/logout", "POST");
    } finally {
      this.o.setToken(null);
    }
  }
  getSubscription() {
    return this.call("/api/v1/user/subscription", "GET");
  }
  /**
   * 拉订阅正文。**这条是订阅面**：裸响应、不套信封、按 UA / flag 分格式，
   * 所以不能走 `unwrap`。URL 直接来自会员中心下发的 `urls.singbox`，我们不自己拼 token。
   */
  async fetchSubscriptionBody(url) {
    const doFetch = this.o.fetchImpl ?? fetch;
    const res = await doFetch(url, {
      method: "GET",
      headers: { Accept: "application/json" },
      signal: AbortSignal.timeout(this.o.timeoutMs ?? 15e3)
    });
    if (!res.ok) {
      throw new ApiError(res.status, res.status === 404 ? "SUB_NOT_FOUND" : "UNKNOWN", `\u8BA2\u9605\u62C9\u53D6\u5931\u8D25\uFF08HTTP ${res.status}\uFF09`);
    }
    return res.text();
  }
};

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

// src/main/diagnostics.ts
function buildDiagnostics(input) {
  const q = quotaView(input.subscription, Date.parse(input.now) || Date.now());
  const count = (r) => input.tabs.filter((t) => t.route === r).length;
  return {
    product: "babel.plus-browser",
    version: input.version,
    platform: input.platform,
    generatedAt: input.now,
    signedIn: input.signedIn,
    connection: {
      status: input.connection.status,
      // 出口是节点的**展示名**（例 `HK-1 · REALITY`），不是主机名 —— 排障要它，且它不泄露地址。
      outbound: input.connection.outbound,
      startedAt: input.connection.startedAt,
      restarts: input.connection.restarts,
      lastError: input.connection.lastError ? { reason: input.connection.lastError.reason, detail: input.connection.lastError.detail } : null
    },
    core: { bundled: input.corePath !== null, port: input.connection.port === null ? "none" : "assigned" },
    quota: q ? {
      usedBytes: q.usedBytes,
      totalBytes: q.totalBytes,
      expiredAt: q.expiredAt,
      low: q.low,
      exhausted: q.exhausted,
      expired: q.expired,
      fetchedAt: input.subscriptionFetchedAt
    } : null,
    prefs: {
      mode: input.prefs.mode,
      // **只报个数不报内容**：这两个列表是用户访问过什么的强指纹。
      alwaysProxyCount: input.prefs.alwaysProxy.length,
      neverProxyCount: input.prefs.neverProxy.length,
      launchAtStart: input.prefs.launchAtStart,
      outboundPinned: input.prefs.outbound !== null
    },
    tabs: { total: input.tabs.length, proxy: count("proxy"), direct: count("direct"), failed: count("failed") }
  };
}
function diagnosticsText(report) {
  return `${JSON.stringify(report, null, 2)}
`;
}

// src/main/routing.ts
var CN_DIRECT_SUFFIXES = [
  "cn",
  "taobao.com",
  "tmall.com",
  "alipay.com",
  "alicdn.com",
  "jd.com",
  "qq.com",
  "wechat.com",
  "baidu.com",
  "bdstatic.com",
  "amap.com",
  "autonavi.com",
  "meituan.com",
  "dianping.com",
  "bilibili.com",
  "zhihu.com",
  "douyin.com",
  "unionpay.com",
  "cmbchina.com",
  "icbc.com.cn",
  "ccb.com",
  "boc.cn"
];
var BLOCKED_SUFFIXES = [
  "google.com",
  "youtube.com",
  "gmail.com",
  "facebook.com",
  "instagram.com",
  "whatsapp.com",
  "x.com",
  "twitter.com",
  "wikipedia.org",
  "github.io",
  "notion.so",
  "slack.com",
  "figma.com",
  "openai.com",
  "chatgpt.com",
  "anthropic.com",
  "claude.ai"
];
var HOST_RE = /^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?(\.[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)*$/;
function normalizeHostList(input) {
  const parts = typeof input === "string" ? input.split(/[\s,;]+/) : input;
  const out = [];
  for (const raw of parts) {
    let h = raw.trim().toLowerCase();
    if (!h) continue;
    h = h.replace(/^[a-z][a-z0-9+.-]*:\/\//, "");
    h = h.replace(/[/?#].*$/, "");
    h = h.replace(/:\d+$/, "");
    h = h.replace(/^\*?\.+/, "");
    if (!HOST_RE.test(h)) continue;
    if (!out.includes(h)) out.push(h);
  }
  return out;
}
function hostOf(url) {
  try {
    const h = new URL(url).hostname.toLowerCase();
    return h.length > 0 ? h : null;
  } catch {
    return null;
  }
}
function matches(host, suffixes) {
  return suffixes.some((s) => host === s || host.endsWith(`.${s}`));
}
function decide(host, input) {
  const h = host.toLowerCase();
  if (h === "localhost" || h.endsWith(".localhost") || h.endsWith(".local") || !h.includes(".")) {
    return { route: "direct", reason: "local" };
  }
  if (matches(h, normalizeHostList(input.controlPlaneHosts))) return { route: "direct", reason: "control-plane" };
  if (matches(h, normalizeHostList(input.neverProxy))) return { route: "direct", reason: "never-list" };
  if (matches(h, normalizeHostList(input.alwaysProxy))) return { route: "proxy", reason: "always-list" };
  if (input.mode === "everything") return { route: "proxy", reason: "mode-everything" };
  if (matches(h, CN_DIRECT_SUFFIXES)) return { route: "direct", reason: "cn-direct" };
  return { route: "proxy", reason: "default-proxy" };
}
function looksBlocked(host) {
  return matches(host.toLowerCase(), BLOCKED_SUFFIXES);
}
function singboxRules(input, directTag, proxyTag) {
  const rules2 = [
    // 回环与私有网段直连。放第一条：本机入站自己也要能连出去。
    { ip_is_private: true, outbound: directTag },
    { domain_suffix: ["localhost", "local"], outbound: directTag }
  ];
  const control = normalizeHostList(input.controlPlaneHosts);
  if (control.length > 0) rules2.push({ domain_suffix: [...control], outbound: directTag });
  const never = normalizeHostList(input.neverProxy);
  if (never.length > 0) rules2.push({ domain_suffix: [...never], outbound: directTag });
  const always = normalizeHostList(input.alwaysProxy);
  if (always.length > 0) rules2.push({ domain_suffix: [...always], outbound: proxyTag });
  if (input.mode === "smart") {
    rules2.push({ domain_suffix: [...CN_DIRECT_SUFFIXES], outbound: directTag });
  }
  return rules2;
}

// src/main/config.ts
var DIRECT_TAG = "bp-direct";
var INBOUND_TAG = "bp-mixed-in";
function buildConfig(input) {
  const { subscription, port } = input;
  if (!Number.isInteger(port) || port <= 0 || port > 65535) {
    throw new Error(`\u5165\u7AD9\u7AEF\u53E3\u4E0D\u5408\u6CD5\uFF1A${port}`);
  }
  const tags = new Set(
    subscription.outbounds.map((o) => o["tag"]).filter((t) => typeof t === "string")
  );
  const proxyTag = input.outbound && tags.has(input.outbound) ? input.outbound : subscription.selectorTag;
  const config = {
    log: { level: input.logLevel ?? "warn", timestamp: true },
    inbounds: [
      {
        type: "mixed",
        tag: INBOUND_TAG,
        // 🔴 只监听回环。监听 0.0.0.0 会让同一网络里的任何人白嫖这个用户的配额，
        // 而酒店 / 咖啡馆 WiFi 正是本产品的主场景。
        listen: "127.0.0.1",
        listen_port: port
        // 本机入站不设认证：Chrome 对 SOCKS5 不支持认证（spec §1 边界 ②），
        // 而端口只对回环开放，认证在这里买不到任何东西。
      }
    ],
    outbounds: [...subscription.outbounds, { type: "direct", tag: DIRECT_TAG }],
    route: {
      rules: singboxRules(input.rules, DIRECT_TAG, proxyTag),
      // final 指向代理 = 默认走代理（spec §3.4）。
      final: proxyTag,
      auto_detect_interface: true
    }
  };
  return { config, proxyTag };
}
function serializeConfig(config) {
  return `${JSON.stringify(config, null, 2)}
`;
}

// src/main/core.ts
var import_node_child_process = require("node:child_process");
var import_promises = require("node:fs/promises");
var import_node_os = require("node:os");
var import_node_path = require("node:path");

// src/main/ports.ts
var import_node_net = require("node:net");
async function pickLoopbackPort() {
  return new Promise((resolve, reject) => {
    const srv = (0, import_node_net.createServer)();
    srv.unref();
    srv.on("error", reject);
    srv.listen(0, "127.0.0.1", () => {
      const addr = srv.address();
      if (addr === null || typeof addr === "string") {
        srv.close(() => reject(new Error("\u62FF\u4E0D\u5230\u4E34\u65F6\u7AEF\u53E3")));
        return;
      }
      const { port } = addr;
      srv.close(() => resolve(port));
    });
  });
}
async function waitForPort(port, timeoutMs, stepMs = 100) {
  const deadline = Date.now() + timeoutMs;
  const { connect } = await import("node:net");
  while (Date.now() < deadline) {
    const ok = await new Promise((resolve) => {
      const sock = connect({ port, host: "127.0.0.1" });
      const done = (v) => {
        sock.destroy();
        resolve(v);
      };
      sock.once("connect", () => done(true));
      sock.once("error", () => done(false));
      sock.setTimeout(Math.min(stepMs * 5, 1e3), () => done(false));
    });
    if (ok) return true;
    await new Promise((r) => setTimeout(r, stepMs));
  }
  return false;
}

// src/main/core.ts
function defaultSpawn(bin, args) {
  const child = (0, import_node_child_process.spawn)(bin, [...args], { stdio: ["ignore", "ignore", "pipe"] });
  return {
    pid: child.pid,
    kill: () => {
      child.kill("SIGTERM");
    },
    onExit: (cb) => child.on("exit", cb),
    onStderr: (cb) => {
      child.stderr?.setEncoding("utf8");
      child.stderr?.on("data", (chunk) => {
        for (const line of chunk.split("\n")) if (line.trim()) cb(line.trim());
      });
    }
  };
}
async function checkConfig(binary, configPath, spawnImpl) {
  const impl = spawnImpl ?? defaultSpawn;
  return new Promise((resolve) => {
    let stderr = "";
    const p = impl(binary, ["check", "-c", configPath]);
    p.onStderr((line) => {
      stderr += `${line}
`;
    });
    p.onExit((code) => resolve({ ok: code === 0, detail: stderr.trim() }));
  });
}
var Core = class {
  opts;
  proc = null;
  dir = null;
  configPath = null;
  restarts = 0;
  stopping = false;
  port_ = null;
  constructor(opts) {
    this.opts = opts;
  }
  get port() {
    return this.port_;
  }
  get restartCount() {
    return this.restarts;
  }
  get running() {
    return this.proc !== null;
  }
  emit(e) {
    this.opts.onEvent?.(e);
  }
  /**
   * 写配置 → check → 起进程 → 等端口。任一步失败都抛，且**不会留下半个跑着的进程**。
   * 抛出的 Error 上带 `reason`，调用方据此决定界面上说什么。
   */
  async start(config, port) {
    this.stopping = false;
    this.restarts = 0;
    this.dir = this.opts.workDir ?? await (0, import_promises.mkdtemp)((0, import_node_path.join)((0, import_node_os.tmpdir)(), "bp-browser-"));
    this.configPath = (0, import_node_path.join)(this.dir, "sing-box.json");
    await (0, import_promises.writeFile)(this.configPath, serializeConfig(config), { mode: 384 });
    const check = await checkConfig(this.opts.binary, this.configPath, this.opts.spawnImpl);
    if (!check.ok) {
      const err = new Error(check.detail || "sing-box check \u5931\u8D25");
      err.reason = "config-rejected";
      throw err;
    }
    this.port_ = port;
    await this.launch();
  }
  async launch() {
    const impl = this.opts.spawnImpl ?? defaultSpawn;
    const wait = this.opts.waitImpl ?? waitForPort;
    const p = impl(this.opts.binary, ["run", "-c", this.configPath ?? ""]);
    this.proc = p;
    p.onStderr((line) => this.emit({ type: "stderr", line }));
    p.onExit((code, signal) => {
      this.proc = null;
      this.emit({ type: "exited", code, signal });
      if (!this.stopping) void this.onUnexpectedExit();
    });
    const ok = await wait(this.port_ ?? 0, this.opts.startTimeoutMs ?? 8e3);
    if (!ok) {
      this.stopping = true;
      p.kill();
      this.proc = null;
      const err = new Error(`sing-box \u8D77\u6765\u4E86\u4F46 127.0.0.1:${this.port_} \u6CA1\u6709\u5F00\u59CB\u63A5\u53D7\u8FDE\u63A5`);
      err.reason = "port-unavailable";
      throw err;
    }
    this.emit({ type: "started", port: this.port_ ?? 0 });
  }
  /** 非预期退出：退避重启，到上限后 failed。**期间不撤代理**（见文件头第 2 条）。 */
  async onUnexpectedExit() {
    const max = this.opts.maxRestarts ?? 5;
    if (this.restarts >= max) {
      this.emit({ type: "failed", detail: `\u5185\u6838\u8FDE\u7EED\u9000\u51FA ${this.restarts} \u6B21\uFF0C\u5DF2\u505C\u6B62\u91CD\u542F` });
      return;
    }
    this.restarts += 1;
    const delayMs = Math.min(500 * 2 ** (this.restarts - 1), 8e3);
    this.emit({ type: "restarting", attempt: this.restarts, delayMs });
    await new Promise((r) => setTimeout(r, delayMs));
    if (this.stopping) return;
    try {
      await this.launch();
    } catch (cause) {
      this.emit({ type: "failed", detail: cause instanceof Error ? cause.message : String(cause) });
    }
  }
  /** 停止并清理临时目录（配置里有节点凭据，不能留在盘上）。 */
  async stop() {
    this.stopping = true;
    this.proc?.kill();
    this.proc = null;
    this.port_ = null;
    if (this.dir && !this.opts.workDir) {
      await (0, import_promises.rm)(this.dir, { recursive: true, force: true }).catch(() => void 0);
    }
    this.dir = null;
    this.configPath = null;
  }
};

// src/main/subscription.ts
var SubscriptionError = class extends Error {
  reason;
  constructor(reason, message) {
    super(message);
    this.name = "SubscriptionError";
    this.reason = reason;
  }
};
function isRecord(v) {
  return typeof v === "object" && v !== null && !Array.isArray(v);
}
function labelOf(tag) {
  return tag.trim() || tag;
}
function parseSubscription(raw) {
  let doc;
  try {
    doc = JSON.parse(raw);
  } catch (cause) {
    throw new SubscriptionError("malformed", `\u8BA2\u9605\u4E0D\u662F\u5408\u6CD5 JSON\uFF1A${cause instanceof Error ? cause.message : cause}`);
  }
  if (!isRecord(doc)) throw new SubscriptionError("malformed", "\u8BA2\u9605\u9876\u5C42\u4E0D\u662F\u5BF9\u8C61");
  const outbounds = doc["outbounds"];
  if (!Array.isArray(outbounds) || outbounds.length === 0) {
    throw new SubscriptionError("malformed", "\u8BA2\u9605\u91CC\u6CA1\u6709 outbounds");
  }
  const records = outbounds.filter(isRecord);
  if (records.length !== outbounds.length) throw new SubscriptionError("malformed", "outbounds \u91CC\u6709\u975E\u5BF9\u8C61\u5143\u7D20");
  const selector = records.find((o) => o["type"] === "selector");
  const nodes = records.filter((o) => o["type"] !== "selector");
  if (nodes.length === 0) {
    throw new SubscriptionError("empty", "\u8BA2\u9605\u91CC\u6CA1\u6709\u53EF\u7528\u8282\u70B9\uFF08\u8D26\u53F7\u53EF\u80FD\u5DF2\u5230\u671F\u3001\u88AB\u5C01\u6216\u914D\u989D\u8017\u5C3D\uFF09");
  }
  let selectorTag;
  let members;
  if (selector && typeof selector["tag"] === "string") {
    selectorTag = selector["tag"];
    const m = selector["outbounds"];
    members = Array.isArray(m) ? m.filter((x) => typeof x === "string") : [];
  } else {
    const first = nodes[0]?.["tag"];
    if (typeof first !== "string") throw new SubscriptionError("malformed", "\u8282\u70B9\u6CA1\u6709 tag");
    selectorTag = first;
    members = [first];
  }
  const nodeTags = new Set(nodes.map((n) => n["tag"]).filter((t) => typeof t === "string"));
  const regionTags = members.filter((t) => nodeTags.has(t));
  const regions = (regionTags.length > 0 ? regionTags : [...nodeTags]).map((tag) => ({
    tag,
    label: labelOf(tag)
  }));
  return { outbounds: records, selectorTag, regions };
}

// src/main/store.ts
var import_promises2 = require("node:fs/promises");
var import_node_path2 = require("node:path");

// src/shared/types.ts
var DEFAULT_PREFS = {
  mode: "smart",
  alwaysProxy: [],
  neverProxy: [],
  outbound: null,
  launchAtStart: false
};
var OFFLINE = {
  status: "off",
  port: null,
  outbound: null,
  startedAt: null,
  lastError: null,
  restarts: 0
};

// src/main/store.ts
var EMPTY_STATE = {
  token: null,
  prefs: DEFAULT_PREFS,
  lastConnectedAt: null,
  onboarded: false
};
function coercePrefs(v) {
  if (typeof v !== "object" || v === null) return DEFAULT_PREFS;
  const r = v;
  const list = (x) => Array.isArray(x) ? x.filter((s) => typeof s === "string") : [];
  return {
    mode: r["mode"] === "everything" ? "everything" : "smart",
    alwaysProxy: list(r["alwaysProxy"]),
    neverProxy: list(r["neverProxy"]),
    outbound: typeof r["outbound"] === "string" ? r["outbound"] : null,
    launchAtStart: r["launchAtStart"] === true
  };
}
var Store = class {
  file;
  state = EMPTY_STATE;
  /**
   * 🔴 写入必须串行。两处会同时写：`Api` 的 `setToken` 回调（登录成功那一刻，同步触发）
   * 与调用方自己的 `update`。并发时两次写用同一个临时文件名 —— 先完成的那次 rename 把它移走，
   * 后一次 rename 就 ENOENT，**而那次丢掉的正好可能是 token**。
   * 由这条队列 + 每次唯一的临时名一起挡住（`store.test.ts` 有回归用例）。
   */
  queue = Promise.resolve();
  seq = 0;
  constructor(dir, name = "state.json") {
    this.file = (0, import_node_path2.join)(dir, name);
  }
  get path() {
    return this.file;
  }
  async load() {
    try {
      const raw = await (0, import_promises2.readFile)(this.file, "utf8");
      const parsed = JSON.parse(raw);
      this.state = {
        token: typeof parsed["token"] === "string" ? parsed["token"] : null,
        prefs: coercePrefs(parsed["prefs"]),
        lastConnectedAt: typeof parsed["lastConnectedAt"] === "string" ? parsed["lastConnectedAt"] : null,
        onboarded: parsed["onboarded"] === true
      };
    } catch {
      this.state = EMPTY_STATE;
    }
    return this.state;
  }
  get current() {
    return this.state;
  }
  async update(patch) {
    this.state = { ...this.state, ...patch };
    const snapshot2 = this.state;
    this.seq += 1;
    const tmp = `${this.file}.${process.pid}.${this.seq}.tmp`;
    const write = this.queue.then(async () => {
      await (0, import_promises2.mkdir)((0, import_node_path2.dirname)(this.file), { recursive: true });
      await (0, import_promises2.writeFile)(tmp, `${JSON.stringify(snapshot2, null, 2)}
`, { mode: 384 });
      await (0, import_promises2.rename)(tmp, this.file);
    });
    this.queue = write.catch(() => void 0);
    await write;
    return this.state;
  }
  /** 等所有排队中的写落盘。登录之后必须调一次 —— 否则「刚登录就退出」会丢掉 token。 */
  async flush() {
    await this.queue;
  }
};

// src/main/controller.ts
var Controller = class {
  d;
  conn = OFFLINE;
  sub = null;
  summary = null;
  summaryAt = null;
  subUrl = null;
  constructor(deps) {
    this.d = deps;
  }
  get now() {
    return (this.d.now ?? Date.now)();
  }
  set(next) {
    this.conn = { ...this.conn, ...next };
    this.d.onChange?.();
  }
  get connection() {
    return this.conn;
  }
  get subscriptionSummary() {
    return this.summary;
  }
  get subscriptionFetchedAt() {
    return this.summaryAt;
  }
  get regions() {
    return this.sub?.regions ?? [];
  }
  get signedIn() {
    return this.d.store.current.token !== null;
  }
  get prefs() {
    return this.d.store.current.prefs;
  }
  /** 内核事件 → 连接状态。装在构造之后由调用方接上（`core.onEvent`）。 */
  handleCoreEvent(e) {
    switch (e.type) {
      case "started":
        this.set({ status: "on", port: e.port, lastError: null });
        break;
      case "exited":
        if (this.conn.status === "on") {
          this.set({
            status: "degraded",
            lastError: { reason: "core-crashed", detail: `\u5185\u6838\u9000\u51FA\uFF08code=${e.code} signal=${e.signal}\uFF09` }
          });
        }
        break;
      case "restarting":
        this.set({ restarts: e.attempt });
        break;
      case "failed":
        this.set({ status: "failed", lastError: { reason: "core-crashed", detail: e.detail } });
        break;
      case "stderr":
        break;
    }
  }
  async signIn(email, password) {
    await this.d.api.login(email, password);
    await this.d.store.flush();
    await this.refreshSubscription();
  }
  async signOut() {
    await this.disconnect();
    await this.d.api.logout().catch(() => void 0);
    await this.d.store.update({ token: null, onboarded: this.d.store.current.onboarded });
    this.sub = null;
    this.summary = null;
    this.summaryAt = null;
    this.subUrl = null;
    this.d.onChange?.();
  }
  /** 拉会员中心的订阅摘要与订阅 URL。失败不抛，记进 lastError。 */
  async refreshSubscription() {
    try {
      const s = await this.d.api.getSubscription();
      this.summary = s.summary;
      this.summaryAt = new Date(this.now).toISOString();
      this.subUrl = s.urls.singbox ?? null;
      if (!this.subUrl) {
        this.set({ lastError: { reason: "no-subscription", detail: "\u4F1A\u5458\u4E2D\u5FC3\u6CA1\u6709\u4E0B\u53D1 sing-box \u8BA2\u9605\u94FE\u63A5" } });
        this.d.onChange?.();
        return false;
      }
      this.d.onChange?.();
      return true;
    } catch (cause) {
      this.set({ lastError: { reason: reasonOf(cause), detail: detailOf(cause) } });
      return false;
    }
  }
  /** 拉订阅正文并解析。 */
  async loadSubscription() {
    if (!this.subUrl && !await this.refreshSubscription()) return null;
    const url = this.subUrl;
    if (!url) return null;
    try {
      const body = await this.d.api.fetchSubscriptionBody(url);
      this.sub = parseSubscription(body);
      return this.sub;
    } catch (cause) {
      const reason = cause instanceof SubscriptionError ? cause.reason === "empty" ? "subscription-empty" : "no-subscription" : reasonOf(cause);
      this.set({ status: "failed", lastError: { reason, detail: detailOf(cause) } });
      return null;
    }
  }
  /**
   * 连接。`outbound` 为 undefined 时用偏好里的；显式传 null = 用订阅的选择器。
   * 全程任一步失败都进 `failed` 并**不设代理**。
   */
  async connect(outbound) {
    if (!this.signedIn) {
      this.set({ status: "failed", lastError: { reason: "not-signed-in", detail: "\u5148\u767B\u5F55" } });
      return;
    }
    this.set({ status: "starting", lastError: null, restarts: 0 });
    const chosen = outbound === void 0 ? this.prefs.outbound : outbound;
    if (chosen !== this.prefs.outbound) {
      await this.d.store.update({ prefs: { ...this.prefs, outbound: chosen } });
    }
    const sub = await this.loadSubscription();
    if (!sub) return;
    const quota = quotaView(this.summary, this.now);
    if (quota?.expired || quota?.exhausted) {
      this.set({
        status: "failed",
        lastError: {
          reason: "subscription-empty",
          detail: quota.expired ? "\u901A\u884C\u8BC1\u5DF2\u5230\u671F" : "\u901A\u884C\u8BC1\u6D41\u91CF\u5DF2\u7528\u5C3D"
        }
      });
      return;
    }
    let port;
    try {
      port = await pickLoopbackPort();
    } catch (cause) {
      this.set({ status: "failed", lastError: { reason: "port-unavailable", detail: detailOf(cause) } });
      return;
    }
    const { config, proxyTag } = buildConfig({
      subscription: sub,
      port,
      outbound: chosen,
      rules: {
        mode: this.prefs.mode,
        alwaysProxy: this.prefs.alwaysProxy,
        neverProxy: this.prefs.neverProxy,
        controlPlaneHosts: this.d.controlPlaneHosts
      }
    });
    try {
      await this.d.core.start(config, port);
    } catch (cause) {
      const reason = cause.reason ?? "core-missing";
      this.set({ status: "failed", port: null, lastError: { reason, detail: detailOf(cause) } });
      return;
    }
    await this.d.applyProxy(port);
    this.set({
      status: "on",
      port,
      outbound: proxyTag,
      startedAt: new Date(this.now).toISOString(),
      lastError: null
    });
    await this.d.store.update({ lastConnectedAt: new Date(this.now).toISOString(), onboarded: true });
  }
  async disconnect() {
    await this.d.core.stop();
    await this.d.clearProxy();
    this.conn = { ...OFFLINE, lastError: null };
    this.d.onChange?.();
  }
  /** 定时刷新配额；用尽 / 到期时主动断开。 */
  async tick() {
    if (!this.signedIn) return;
    await this.refreshSubscription();
    const quota = quotaView(this.summary, this.now);
    if (this.conn.status === "on" && quota && (quota.exhausted || quota.expired)) {
      await this.disconnect();
      this.set({
        status: "failed",
        lastError: {
          reason: "subscription-empty",
          detail: quota.expired ? "\u901A\u884C\u8BC1\u5DF2\u5230\u671F\uFF0C\u5DF2\u65AD\u5F00" : "\u901A\u884C\u8BC1\u6D41\u91CF\u5DF2\u7528\u5C3D\uFF0C\u5DF2\u65AD\u5F00"
        }
      });
    }
  }
  /** 改偏好。影响路由的改动在已连接时会**重建配置并重启内核**（sing-box 不支持热改路由）。 */
  async setPrefs(patch) {
    const before = this.prefs;
    const next = {
      mode: patch.mode ?? before.mode,
      alwaysProxy: patch.alwaysProxy ? normalizeHostList(patch.alwaysProxy) : before.alwaysProxy,
      neverProxy: patch.neverProxy ? normalizeHostList(patch.neverProxy) : before.neverProxy,
      outbound: patch.outbound === void 0 ? before.outbound : patch.outbound,
      launchAtStart: patch.launchAtStart ?? before.launchAtStart
    };
    await this.d.store.update({ prefs: next });
    const routingChanged = next.mode !== before.mode || next.alwaysProxy.join() !== before.alwaysProxy.join() || next.neverProxy.join() !== before.neverProxy.join() || next.outbound !== before.outbound;
    if (routingChanged && (this.conn.status === "on" || this.conn.status === "degraded")) {
      await this.d.core.stop();
      await this.connect(next.outbound);
    } else {
      this.d.onChange?.();
    }
  }
  /** 「这个站点在这里被屏蔽」提示条的处置：加进 alwaysProxy 并重连。 */
  async routeHost(host) {
    const list = normalizeHostList([...this.prefs.alwaysProxy, host]);
    await this.setPrefs({ alwaysProxy: list });
  }
};
function reasonOf(cause) {
  if (cause instanceof ApiError) {
    if (cause.status === 0) return "network";
    if (cause.status === 401) return "not-signed-in";
    return "no-subscription";
  }
  return "network";
}
function detailOf(cause) {
  return cause instanceof Error ? cause.message : String(cause);
}

// src/main/tabs.ts
var import_electron = require("electron");
var Tabs = class {
  d;
  tabs = [];
  active = null;
  seq = 0;
  overlay = false;
  constructor(deps) {
    this.d = deps;
  }
  get state() {
    return {
      tabs: this.tabs.map((t) => ({
        id: t.id,
        title: t.title,
        url: t.url,
        route: t.route,
        bytes: t.bytes,
        loading: t.loading,
        blockedHost: t.blockedHost
      })),
      activeTabId: this.active
    };
  }
  routeOf(url) {
    const host = hostOf(url);
    if (!host) return "direct";
    return decide(host, this.d.rules()).route;
  }
  create(url) {
    this.seq += 1;
    const id = this.seq;
    const view = new import_electron.WebContentsView({
      webPreferences: {
        // 页面是任意第三方内容：三道隔离一个都不能少。
        contextIsolation: true,
        nodeIntegration: false,
        sandbox: true,
        // 每个标签页共用默认 session —— 代理设置是 session 级的，
        // 单独 partition 会让「设了代理但这个标签页没走」这种最难查的事成为可能。
        webviewTag: false
      }
    });
    const target = url ?? this.d.newTabUrl;
    const tab = {
      id,
      view,
      title: target,
      url: target,
      route: this.routeOf(target),
      bytes: 0,
      loading: true,
      blockedHost: null,
      metered: false
    };
    this.tabs.push(tab);
    this.wire(tab);
    this.d.window.contentView.addChildView(view);
    void view.webContents.loadURL(target);
    this.select(id);
    return id;
  }
  wire(tab) {
    const wc = tab.view.webContents;
    wc.on("page-title-updated", (_e, title) => {
      tab.title = title;
      this.d.onChange();
    });
    wc.on("did-start-loading", () => {
      tab.loading = true;
      tab.blockedHost = null;
      this.d.onChange();
    });
    wc.on("did-stop-loading", () => {
      tab.loading = false;
      this.d.onChange();
    });
    wc.on("did-navigate", (_e, url) => {
      tab.url = url;
      tab.route = this.routeOf(url);
      tab.bytes = 0;
      this.d.onChange();
    });
    wc.on("did-fail-load", (_e, errorCode, errorDescription, validatedURL, isMainFrame) => {
      if (!isMainFrame) return;
      if (errorCode === -3) return;
      tab.route = "failed";
      const host = hostOf(validatedURL);
      const wasDirect = host ? decide(host, this.d.rules()).route === "direct" : false;
      tab.blockedHost = host && wasDirect && looksBlocked(host) ? host : null;
      tab.title = errorDescription || tab.title;
      this.d.onChange();
    });
    wc.setWindowOpenHandler(({ url }) => {
      this.create(url);
      return { action: "deny" };
    });
    wc.on("devtools-opened", () => this.detachForDevTools(tab));
    if (this.d.attachMetering !== false) this.attachMetering(tab);
  }
  /** CDP 计量。失败不影响浏览 —— 拿不到字节数只是少一个数字。 */
  attachMetering(tab) {
    const wc = tab.view.webContents;
    try {
      wc.debugger.attach("1.3");
      tab.metered = true;
    } catch {
      return;
    }
    wc.debugger.on("message", (_e, method, params) => {
      if (method !== "Network.loadingFinished") return;
      const len = params.encodedDataLength;
      if (typeof len === "number" && Number.isFinite(len) && len > 0) {
        tab.bytes += len;
        this.d.onChange();
      }
    });
    wc.debugger.sendCommand("Network.enable").catch(() => void 0);
  }
  detachForDevTools(tab) {
    if (!tab.metered) return;
    try {
      tab.view.webContents.debugger.detach();
    } catch {
    }
    tab.metered = false;
    this.d.onChange();
  }
  /**
   * 覆盖层（设置页）开关。
   * 🔴 必须有这个东西：`WebContentsView` 是**盖在** chrome 页面之上的原生视图，
   * chrome 里的任何全屏覆盖层都只会被裁到上面那 96 px。所以打开设置时先把标签页视图藏起来。
   */
  setOverlay(on) {
    this.overlay = on;
    for (const t of this.tabs) t.view.setVisible(!on && t.id === this.active);
  }
  select(id) {
    const tab = this.tabs.find((t) => t.id === id);
    if (!tab) return;
    this.active = id;
    for (const t of this.tabs) t.view.setVisible(!this.overlay && t.id === id);
    this.layout();
    this.d.onChange();
  }
  close(id) {
    const idx = this.tabs.findIndex((t) => t.id === id);
    if (idx < 0) return;
    const [tab] = this.tabs.splice(idx, 1);
    if (!tab) return;
    if (tab.metered) {
      try {
        tab.view.webContents.debugger.detach();
      } catch {
      }
    }
    this.d.window.contentView.removeChildView(tab.view);
    tab.view.webContents.close();
    if (this.active === id) {
      const next = this.tabs[Math.min(idx, this.tabs.length - 1)];
      this.active = next?.id ?? null;
      if (next) this.select(next.id);
    }
    this.d.onChange();
  }
  navigate(id, url) {
    const tab = this.tabs.find((t) => t.id === id);
    if (!tab) return;
    tab.blockedHost = null;
    void tab.view.webContents.loadURL(url);
  }
  reload(id) {
    this.tabs.find((t) => t.id === id)?.view.webContents.reload();
  }
  goBack(id) {
    const wc = this.tabs.find((t) => t.id === id)?.view.webContents;
    if (wc?.navigationHistory.canGoBack()) wc.navigationHistory.goBack();
  }
  goForward(id) {
    const wc = this.tabs.find((t) => t.id === id)?.view.webContents;
    if (wc?.navigationHistory.canGoForward()) wc.navigationHistory.goForward();
  }
  /** 路由规则变了（改偏好、接受提示条）之后重算所有角标。 */
  recomputeRoutes() {
    for (const t of this.tabs) if (t.route !== "failed") t.route = this.routeOf(t.url);
    this.d.onChange();
  }
  /** 窗口大小变化时重排当前标签页的视图。 */
  layout() {
    const bounds = this.d.window.getContentBounds();
    for (const t of this.tabs) {
      if (t.id !== this.active) continue;
      t.view.setBounds({
        x: 0,
        y: this.d.chromeHeight,
        width: bounds.width,
        height: Math.max(0, bounds.height - this.d.chromeHeight)
      });
    }
  }
  get count() {
    return this.tabs.length;
  }
};

// src/main/index.ts
var API_BASE_URLS = (process.env["BP_API_BASE_URLS"] ?? "https://api.babel.plus").split(",").map((s) => s.trim()).filter(Boolean);
var WEB_URL = process.env["BP_WEB_URL"] ?? "https://web.babel.plus";
var HELP_URL = process.env["BP_HELP_URL"] ?? "https://babel.plus/help";
var NEW_TAB_URL = process.env["BP_NEW_TAB_URL"] ?? "https://www.google.com/";
var CHROME_HEIGHT = 96;
function coreBinary() {
  const key = `${process.platform}-${process.arch}`;
  const name = process.platform === "win32" ? "sing-box.exe" : "sing-box";
  const candidates = [
    // 打包后：electron-builder 的 extraResources 把 vendor 放进 resources/
    (0, import_node_path3.join)(process.resourcesPath ?? "", "vendor", key, name),
    // 开发期：desktop/vendor/<key>/<bin>
    (0, import_node_path3.join)(import_electron2.app.getAppPath(), "vendor", key, name)
  ];
  return candidates.find((p) => p && (0, import_node_fs.existsSync)(p)) ?? null;
}
var win = null;
var tabs = null;
var controller = null;
var refreshTimer = null;
var store = new Store(import_electron2.app.getPath("userData"));
function rules() {
  const prefs = controller?.prefs ?? store.current.prefs;
  return {
    mode: prefs.mode,
    alwaysProxy: prefs.alwaysProxy,
    neverProxy: prefs.neverProxy,
    controlPlaneHosts: [...API_BASE_URLS, WEB_URL, HELP_URL].map((u) => hostOf(u) ?? "").filter((h) => h.length > 0)
  };
}
function snapshot() {
  const t = tabs?.state ?? { tabs: [], activeTabId: null };
  return {
    version: import_electron2.app.getVersion(),
    signedIn: controller?.signedIn ?? false,
    connection: controller?.connection ?? {
      status: "off",
      port: null,
      outbound: null,
      startedAt: null,
      lastError: null,
      restarts: 0
    },
    subscription: controller?.subscriptionSummary ?? null,
    subscriptionFetchedAt: controller?.subscriptionFetchedAt ?? null,
    regions: controller?.regions ?? [],
    prefs: controller?.prefs ?? store.current.prefs,
    tabs: t.tabs,
    activeTabId: t.activeTabId,
    links: { webUrl: WEB_URL, helpUrl: HELP_URL },
    onboarding: !store.current.onboarded
  };
}
function push() {
  win?.webContents.send("bp:snapshot", snapshot());
}
async function applyProxy(port) {
  await import_electron2.session.defaultSession.setProxy({
    proxyRules: `socks5://127.0.0.1:${port}`,
    // 只 bypass 回环：其余一律进入站，由 sing-box 分流（见文件头第 1 条）。
    proxyBypassRules: "<-loopback>"
  });
  await import_electron2.session.defaultSession.closeAllConnections();
  tabs?.recomputeRoutes();
}
async function clearProxy() {
  await import_electron2.session.defaultSession.setProxy({ mode: "direct" });
  await import_electron2.session.defaultSession.closeAllConnections();
  tabs?.recomputeRoutes();
}
function createWindow() {
  win = new import_electron2.BrowserWindow({
    width: 1180,
    height: 780,
    minWidth: 720,
    minHeight: 480,
    title: "babel.plus",
    backgroundColor: "#E9EBEE",
    webPreferences: {
      preload: (0, import_node_path3.join)(__dirname, "preload.cjs"),
      contextIsolation: true,
      nodeIntegration: false,
      sandbox: true
    }
  });
  void win.loadFile((0, import_node_path3.join)(__dirname, "index.html"));
  tabs = new Tabs({
    window: win,
    chromeHeight: CHROME_HEIGHT,
    rules,
    onChange: push,
    newTabUrl: NEW_TAB_URL
  });
  win.on("resize", () => tabs?.layout());
  win.on("closed", () => {
    win = null;
    tabs = null;
  });
}
function wireIpc() {
  const handlers = {
    "bp:snapshot": () => snapshot(),
    "bp:sign-in": async (_e, payload) => {
      const { email, password } = payload;
      await controller?.signIn(email, password);
      return snapshot();
    },
    "bp:sign-out": async () => {
      await controller?.signOut();
      return snapshot();
    },
    "bp:connect": async (_e, payload) => {
      const { outbound } = payload ?? {};
      await controller?.connect(outbound === void 0 ? void 0 : outbound);
      return snapshot();
    },
    "bp:disconnect": async () => {
      await controller?.disconnect();
      return snapshot();
    },
    "bp:set-prefs": async (_e, payload) => {
      await controller?.setPrefs(payload);
      tabs?.recomputeRoutes();
      return snapshot();
    },
    "bp:route-host": async (_e, payload) => {
      const { host } = payload;
      await controller?.routeHost(host);
      tabs?.recomputeRoutes();
      return snapshot();
    },
    "bp:tab": (_e, payload) => {
      const p = payload;
      switch (p.action) {
        case "create":
          tabs?.create(p.url);
          break;
        case "select":
          if (p.id !== void 0) tabs?.select(p.id);
          break;
        case "close":
          if (p.id !== void 0) tabs?.close(p.id);
          break;
        case "navigate":
          if (p.id !== void 0 && p.url) tabs?.navigate(p.id, p.url);
          break;
        case "reload":
          if (p.id !== void 0) tabs?.reload(p.id);
          break;
        case "back":
          if (p.id !== void 0) tabs?.goBack(p.id);
          break;
        case "forward":
          if (p.id !== void 0) tabs?.goForward(p.id);
          break;
      }
      return snapshot();
    },
    "bp:overlay": (_e, payload) => {
      const { open } = payload;
      tabs?.setOverlay(open === true);
      return snapshot();
    },
    "bp:diagnostics": () => {
      const s = snapshot();
      return diagnosticsText(
        buildDiagnostics({
          version: s.version,
          platform: `${process.platform}-${process.arch}`,
          signedIn: s.signedIn,
          connection: s.connection,
          subscription: s.subscription,
          subscriptionFetchedAt: s.subscriptionFetchedAt,
          prefs: s.prefs,
          tabs: s.tabs,
          corePath: coreBinary(),
          now: (/* @__PURE__ */ new Date()).toISOString()
        })
      );
    },
    "bp:open-external": async (_e, payload) => {
      const { url } = payload;
      const allowed = [WEB_URL, HELP_URL].map((u) => hostOf(u));
      const host = hostOf(url);
      if (host && allowed.includes(host)) await import_electron2.shell.openExternal(url);
      return snapshot();
    }
  };
  for (const [channel, fn] of Object.entries(handlers)) {
    import_electron2.ipcMain.handle(channel, fn);
  }
}
import_electron2.app.whenReady().then(async () => {
  await store.load();
  const api = new Api({
    baseUrls: API_BASE_URLS,
    getToken: () => store.current.token,
    setToken: (token) => {
      void store.update({ token });
    }
  });
  const bin = coreBinary();
  const core = new Core({
    binary: bin ?? "sing-box",
    onEvent: (e) => {
      controller?.handleCoreEvent(e);
      push();
    }
  });
  controller = new Controller({
    api,
    store,
    core,
    applyProxy,
    clearProxy,
    controlPlaneHosts: rules().controlPlaneHosts,
    onChange: push
  });
  wireIpc();
  createWindow();
  if (process.env["BP_SMOKE"] === "1") {
    win?.webContents.once("did-finish-load", async () => {
      try {
        const probe = await win?.webContents.executeJavaScript(
          `JSON.stringify({
             bridge: typeof window.bp === 'object',
             onboarding: !document.getElementById('onboarding').hidden,
             signInButton: !!document.getElementById('obsubmit'),
             pill: document.getElementById('pillquota').textContent,
             styled: getComputedStyle(document.getElementById('chrome')).height,
             settings: ['set-mode','set-region','set-always','set-never','set-launch','copy-diag','set-signout']
               .every(function (id) { return !!document.getElementById(id); }),
             settingsHidden: document.getElementById('settings').hidden,
           })`
        );
        process.stdout.write(`BP_SMOKE ${probe}
`);
        const r = JSON.parse(String(probe));
        const ok = r["bridge"] === true && r["signInButton"] === true && r["styled"] === "96px" && r["settings"] === true && r["settingsHidden"] === true;
        import_electron2.app.exit(ok ? 0 : 1);
      } catch (cause) {
        process.stdout.write(`BP_SMOKE failed: ${String(cause)}
`);
        import_electron2.app.exit(1);
      }
    });
  }
  if (!bin) {
    controller.handleCoreEvent({
      type: "failed",
      detail: "\u627E\u4E0D\u5230\u968F\u5305\u5185\u6838 sing-box\u3002\u5F00\u53D1\u673A\u4E0A\u8DD1\u4E00\u6B21 `pnpm core`\u3002"
    });
  }
  import_electron2.session.defaultSession.on("will-download", (event) => {
    event.preventDefault();
    win?.webContents.send("bp:notice", { kind: "download-blocked" });
  });
  if (store.current.token && store.current.prefs.launchAtStart) {
    void controller.connect();
  } else if (store.current.token) {
    void controller.refreshSubscription();
  }
  refreshTimer = setInterval(() => void controller?.tick(), 5 * 6e4);
  import_electron2.app.on("activate", () => {
    if (import_electron2.BrowserWindow.getAllWindows().length === 0) createWindow();
  });
});
import_electron2.app.on("window-all-closed", () => {
  if (process.platform !== "darwin") import_electron2.app.quit();
});
import_electron2.app.on("before-quit", () => {
  if (refreshTimer) clearInterval(refreshTimer);
  void controller?.disconnect();
});

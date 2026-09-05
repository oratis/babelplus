// fleet-sub · 自用机队（vpn-*）的边缘：订阅下发 / 巡检 ingest / 飞书日报。
//
// 事实源：docs/04-ops/personal-fleet-runbook.md §2.3（接口）、§3.2（五组判据）、§3.4（卡片格式）
//         docs/05-adr/0017-personal-fleet-in-repo.md §5 / §6
//         api/internal/handler/subscription.go:774（响应头那一组，照抄，含小写）
//
// KV 布局（一个命名空间，前缀分域）：
//   sub/<file>              已渲染好的订阅产物（四种 + cn-cidr 两种）。内容对所有设备相同，只存一份。
//   tok/dev/<token>  = <device>   设备 token → 设备名。吊销 = 删这个 key。
//   tok/node/<token> = <host>     节点 token → 主机名。只允许 POST /ingest 与 GET /fleet。
//   fleet                   fleet.json 的非机密副本（publish-subscription.sh 发布）。
//   hc/<host>/latest        节点最近一次巡检 JSON；hc/<host>/<YYYY-MM-DD> 为 daily 快照（40 天过期）。
//   usage/<host>/<YYYY-MM>  月度出网累计（由节点上报的 tx_bytes 差分而来）。
//   report/last             最近一次日报（卡片 + 发送结果），排障用。
//
// 🔴 本 Worker 里没有任何凭据的组装逻辑：产物在本机渲染（gen-subscription.py），这里只有已经组装好的字节。
// 🔴 未知 token / 未知文件一律同一个 404，响应体不含任何信息（枚举者要的就是差异）。

const FILES = {
  "mihomo-provider.yaml": "text/yaml; charset=utf-8",
  "clash.yaml": "text/yaml; charset=utf-8",
  "singbox.json": "application/json; charset=utf-8",
  "base64.txt": "text/plain; charset=utf-8",
  "cn-cidr.txt": "text/plain; charset=utf-8",
  "cn-cidr.json": "application/json; charset=utf-8",
};
const GIB = 1024 ** 3;

export default {
  async fetch(request, env, ctx) {
    const url = new URL(request.url);
    const parts = url.pathname.split("/").filter(Boolean);
    try {
      if (parts[0] === "p" && parts.length === 3 && request.method === "GET") {
        return await serveSubscription(env, parts[1], parts[2], url.origin);
      }
      if (parts[0] === "ingest" && parts.length === 2 && request.method === "POST") {
        return await ingest(env, parts[1], request);
      }
      if (parts[0] === "fleet" && parts.length === 1 && request.method === "GET") {
        const who = await authNodeOrAdmin(env, request);
        if (!who) return notFound();
        const fleet = await env.KV.get("fleet");
        return fleet ? json(JSON.parse(fleet)) : notFound();
      }
      if (parts[0] === "admin") {
        if (!(await isAdmin(env, request))) return notFound();
        if (parts[1] === "report" && parts.length === 2 && request.method === "GET") {
          return json(await buildReport(env));
        }
        if (parts[1] === "report" && parts[2] === "send" && request.method === "POST") {
          return json(await runReport(env, { send: true }));
        }
        if (parts[1] === "hc" && request.method === "GET") return json(await dumpPrefix(env, "hc/"));
        if (parts[1] === "usage" && request.method === "GET") return json(await dumpPrefix(env, "usage/"));
        if (parts[1] === "last" && request.method === "GET") {
          const last = await env.KV.get("report/last");
          return last ? json(JSON.parse(last)) : notFound();
        }
      }
      return notFound();
    } catch (e) {
      // 不把异常细节回给客户端；observability 里能看到。
      console.error("fleet-sub error", e && e.stack ? e.stack : String(e));
      return new Response("error\n", { status: 500, headers: { "cache-control": "no-store" } });
    }
  },

  async scheduled(event, env, ctx) {
    ctx.waitUntil(runReport(env, { send: true, cron: event.cron }));
  },
};

// ───────────────────────── 订阅下发 ─────────────────────────

async function serveSubscription(env, token, file, origin) {
  const ctype = FILES[file];
  if (!ctype) return notFound();
  const device = await env.KV.get("tok/dev/" + token);
  if (!device) return notFound();
  let body = await env.KV.get("sub/" + file, { type: "text" });
  if (body === null) return notFound();
  // clash.yaml / singbox.json 里的订阅 URL 是模板（gen-subscription.py 写成 __SUB_BASE__/p/__TOKEN__/…），
  // 按请求替换 —— 于是四种产物对所有设备内容相同、KV 只存一份，吊销只删 token。
  if (file === "clash.yaml" || file === "singbox.json") {
    body = body.replaceAll("__SUB_BASE__", origin).replaceAll("__TOKEN__", token);
  }

  const usage = await fleetUsage(env);
  const headers = new Headers();
  headers.set("content-type", ctype);
  // 🔴 与 Clash / sing-box / Shadowrocket 生态的硬接口，格式不能自创（api-contract §4.4）：
  //    `upload={u}; download={d}; total={transfer_enable}; expire={unix}`，分号 + 一个空格，十进制整数。
  //    download 填机队本月已用字节、total 填软阈值、expire 填月末 —— 这条流量条就是 $500 闸在每台设备上的投影。
  headers.set(
    "subscription-userinfo",
    `upload=0; download=${usage.mtdBytes}; total=${usage.softBytes}; expire=${usage.monthEndUnix}`,
  );
  headers.set("profile-update-interval", String(env.PROFILE_UPDATE_INTERVAL_H || "24"));
  headers.set("content-disposition", `attachment; filename*=UTF-8''${encodeURIComponent(env.FLEET_NAME || "vpn-fleet")}`);
  headers.set("cache-control", "no-store");
  // Workers 运行时按 Fetch 规范以小写序列化头名，与 subscription.go 绕开 Header.Set 的写法效果一致。
  return new Response(body, { status: 200, headers });
}

// ───────────────────────── 巡检 ingest ─────────────────────────

async function ingest(env, token, request) {
  const host = await env.KV.get("tok/node/" + token);
  if (!host) return notFound();
  const text = await request.text();
  if (text.length > 65536) return new Response("too large\n", { status: 413 });
  let hc;
  try {
    hc = JSON.parse(text);
  } catch {
    return new Response("bad json\n", { status: 400 });
  }
  if (!hc || hc.node !== host) return new Response("node mismatch\n", { status: 400 });
  const now = new Date();
  hc.received_at = now.toISOString();
  await env.KV.put(`hc/${host}/latest`, JSON.stringify(hc));
  if (hc.mode === "daily") {
    await env.KV.put(`hc/${host}/${now.toISOString().slice(0, 10)}`, JSON.stringify(hc), {
      expirationTtl: 40 * 86400,
    });
  }
  if (typeof hc.tx_bytes === "number" && hc.boot_id) {
    await updateUsage(env, host, hc.tx_bytes, hc.boot_id, now);
  }
  return new Response(null, { status: 204 });
}

// updateUsage · 节点只报「自开机以来的累计 tx_bytes」，月度累计在这里差分。
// 重启（boot_id 变）或计数器回绕时，把本次计数当作从零起的增量 —— 会多算最多一次样本间隔的量，
// 宁可多算不可少算：这是成本闸，漏算的方向才危险。
async function updateUsage(env, host, counter, bootId, now) {
  const month = now.toISOString().slice(0, 7);
  const key = `usage/${host}/${month}`;
  let doc = await getJSON(env, key);
  if (!doc) {
    const prev = new Date(Date.UTC(now.getUTCFullYear(), now.getUTCMonth() - 1, 1)).toISOString().slice(0, 7);
    const prevDoc = await getJSON(env, `usage/${host}/${prev}`);
    doc = { host, month, mtd: 0, samples: 0, last: prevDoc ? prevDoc.last : null };
  }
  let delta = 0;
  if (doc.last && doc.last.boot_id === bootId && counter >= doc.last.counter) {
    delta = counter - doc.last.counter;
  } else if (doc.last) {
    delta = counter;
  }
  doc.mtd += delta;
  doc.samples += 1;
  doc.last = { counter, boot_id: bootId, ts: now.toISOString() };
  await env.KV.put(key, JSON.stringify(doc));
}

async function fleetUsage(env) {
  const now = new Date();
  const month = now.toISOString().slice(0, 7);
  const list = await env.KV.list({ prefix: "usage/" });
  let mtdBytes = 0;
  const perHost = {};
  for (const k of list.keys) {
    if (!k.name.endsWith("/" + month)) continue;
    const doc = await getJSON(env, k.name);
    if (doc) {
      mtdBytes += doc.mtd || 0;
      perHost[doc.host] = doc.mtd || 0;
    }
  }
  const softBytes = Math.round(Number(env.SOFT_GIB || 3000) * GIB);
  const hardBytes = Math.round(Number(env.HARD_GIB || 3800) * GIB);
  const monthEnd = new Date(Date.UTC(now.getUTCFullYear(), now.getUTCMonth() + 1, 1)) - 1;
  const daysInMonth = new Date(Date.UTC(now.getUTCFullYear(), now.getUTCMonth() + 1, 0)).getUTCDate();
  const dayOfMonth = now.getUTCDate() + now.getUTCHours() / 24;
  return {
    month,
    mtdBytes,
    perHost,
    softBytes,
    hardBytes,
    monthEndUnix: Math.floor(monthEnd / 1000),
    projectedBytes: dayOfMonth > 0 ? Math.round((mtdBytes / dayOfMonth) * daysInMonth) : 0,
  };
}

// ───────────────────────── 日报 ─────────────────────────

// buildReport · 五组判据（runbook §3.2）→ 每节点一行 + 用量段 + 待办段。
// 「未上报」必须是一行正向可读的内容，不是省略（ADR 0017 §6）。
async function buildReport(env) {
  const now = new Date();
  const tzH = Number(env.REPORT_TZ_OFFSET_H || 8);
  const local = new Date(now.getTime() + tzH * 3600 * 1000);
  const dateStr = local.toISOString().slice(0, 10);
  const absentAfterMs = Number(env.ABSENT_AFTER_H || 26) * 3600 * 1000;

  const fleetRaw = await env.KV.get("fleet");
  const fleet = fleetRaw ? JSON.parse(fleetRaw) : { nodes: [], services: {} };
  const nodes = (fleet.nodes || []).filter((n) => (n.status || "running") !== "planned");
  const planned = (fleet.nodes || []).filter((n) => (n.status || "running") === "planned").map((n) => n.host);
  const svcMap = fleet.services || {};

  const latest = {};
  for (const n of nodes) latest[n.host] = await getJSON(env, `hc/${n.host}/latest`);

  const lines = [];
  const todo = [];
  let worst = 0; // 0 green, 1 orange, 2 red
  const bump = (lvl) => { if (lvl > worst) worst = lvl; };

  for (const n of nodes) {
    const hc = latest[n.host];
    const ageMs = hc ? now - new Date(hc.received_at || hc.ts) : Infinity;
    if (!hc || !(ageMs < absentAfterMs)) {
      const last = hc ? fmtLocal(new Date(hc.received_at || hc.ts), tzH) : "从未";
      lines.push(`⚫ **${n.host}**  —— 未上报（最后一次 ${last}）`);
      bump(2);
      continue;
    }
    const expected = [...new Set((n.paths || []).map((p) => svcMap[p.kind]).filter(Boolean))];
    const active = expected.filter((s) => hc.services && hc.services[s] === "active");
    const procOk = active.length === expected.length;

    // B 组：封锁取证 —— ①进程活着 ②443 零公网 established ③数小时零日志 ④邻居回打能握手
    const peerOks = [];
    for (const other of nodes) {
      if (other.host === n.host) continue;
      const o = latest[other.host];
      const pr = o && o.peers && o.peers[n.host];
      if (pr && typeof pr.ok === "boolean") peerOks.push(pr.ok);
    }
    const peerText = peerOks.length ? (peerOks.every(Boolean) ? "邻居回打 OK" : peerOks.some(Boolean) ? "邻居回打 部分失败" : "邻居回打 失败") : "邻居回打 无数据";
    const logAgeH = hc.log_age_s && typeof hc.log_age_s.xray === "number" ? hc.log_age_s.xray / 3600 : null;
    const suspectBlock = procOk && hc.est443_public === 0 && logAgeH !== null && logAgeH >= 3 && peerOks.length && peerOks.every(Boolean);

    let mark = "🟢";
    const notes = [];
    if (!procOk) { mark = "🔴"; bump(2); notes.push(`缺 ${expected.filter((s) => !active.includes(s)).join("/")}`); }
    if (suspectBlock) { mark = "🔴"; bump(2); notes.push("疑似 IP 封锁，见 runbook-node-health §3"); }
    else if (procOk && hc.est443_public === 0 && logAgeH !== null && logAgeH >= 3) { if (mark === "🟢") mark = "🟡"; bump(1); notes.push("443 零连接且 ≥3h 零日志（邻居数据不足，未判封锁）"); }
    if (peerOks.length && !peerOks.every(Boolean) && !suspectBlock) { if (mark === "🟢") mark = "🟡"; bump(1); }

    // D 组：证书
    const certParts = [];
    for (const [name, c] of Object.entries(hc.cert || {})) {
      if (typeof c.days_left === "number") {
        certParts.push(`cert ${c.days_left}d`);
        if (c.days_left < 21) { if (mark === "🟢") mark = "🟡"; bump(1); todo.push(`${n.host} 的 ${name} 证书 ${c.days_left} 天后到期`); }
      }
    }
    // E 组：主机
    const h = hc.host || {};
    if (h.reboot_required) todo.push(`${n.host} 有内核更新待重启`);
    if (h.tcp_cc && h.tcp_cc !== "bbr") { if (mark === "🟢") mark = "🟡"; bump(1); todo.push(`${n.host} 拥塞控制漂移为 ${h.tcp_cc}（期望 bbr）`); }
    if (h.qdisc && h.qdisc !== "fq") { if (mark === "🟢") mark = "🟡"; bump(1); todo.push(`${n.host} qdisc 漂移为 ${h.qdisc}（期望 fq）`); }
    if (typeof h.disk_pct === "number" && h.disk_pct >= 85) { if (mark === "🟢") mark = "🟡"; bump(1); todo.push(`${n.host} 系统盘 ${h.disk_pct}%`); }
    if (typeof h.mem_pct === "number" && h.mem_pct >= 90) { if (mark === "🟢") mark = "🟡"; bump(1); todo.push(`${n.host} 内存 ${h.mem_pct}%`); }
    if (h.oom_recent) { if (mark === "🟢") mark = "🟡"; bump(1); todo.push(`${n.host} 24h 内 OOM ${h.oom_recent} 次`); }

    const parts = [
      `进程 ${active.length}/${expected.length}`,
      `443 est ${typeof hc.est443_public === "number" ? hc.est443_public : "?"}`,
      peerText,
      ...certParts,
      typeof h.cpu_pct === "number" ? `CPU ${h.cpu_pct}%` : null,
    ].filter(Boolean);
    lines.push(`${mark} **${n.host}**  ${parts.join(" · ")}${notes.length ? `  ← ${notes.join("；")}` : ""}`);
  }

  // C 组：用量与预算 —— 这是 $500 上限的唯一执行点（GCP budget 只告警不停机）
  const u = await fleetUsage(env);
  const soft = u.softBytes, hard = u.hardBytes;
  const pctSoft = soft ? Math.round((u.mtdBytes / soft) * 100) : 0;
  let usageMark = "";
  if (u.projectedBytes >= hard || u.mtdBytes >= hard) { usageMark = " 🔴"; bump(2); }
  else if (u.projectedBytes >= soft || u.mtdBytes >= soft) { usageMark = " 🟡"; bump(1); }
  const perHost = Object.entries(u.perHost).sort((a, b) => b[1] - a[1]).map(([h, b]) => `${h} ${gib(b)} GiB`).join(" · ");
  const usageLines = [
    `**${gib(u.mtdBytes)} / ${gib(soft)} GiB**（软阈值 ${pctSoft}%）  月末外推 ${gib(u.projectedBytes)} GiB${usageMark}`,
    perHost ? `  ${perHost}` : "  （尚无节点用量样本）",
  ];
  if (u.projectedBytes >= hard) usageLines.push("  🔴 按当前速率月末将越过硬阈值：切低价区域 / 降级机型 / 暂停某节点（runbook §1.6）");

  if (planned.length) todo.push(`fleet.json 里仍标 planned：${planned.join(" / ")}`);

  const template = worst === 2 ? "red" : worst === 1 ? "orange" : "green";
  const card = {
    config: { wide_screen_mode: true },
    header: { template, title: { tag: "plain_text", content: `🐕 机队日报 · ${dateStr}` } },
    elements: [
      { tag: "div", text: { tag: "lark_md", content: lines.length ? lines.join("\n") : "（fleet 未发布：跑 publish-subscription.sh）" } },
      { tag: "hr" },
      { tag: "div", text: { tag: "lark_md", content: `**本月用量（${u.month}）**\n${usageLines.join("\n")}` } },
      { tag: "hr" },
      { tag: "div", text: { tag: "lark_md", content: `**待办**\n${todo.length ? todo.map((t) => `· ${t}`).join("\n") : "· 无"}` } },
      { tag: "note", elements: [{ tag: "plain_text", content: `生成于 ${fmtLocal(now, tzH)} · fleet-sub worker · 节点每小时 :30 UTC 上报，23:30 为 daily` }] },
    ],
  };
  return { date: dateStr, template, lines, usage: u, todo, card };
}

async function runReport(env, opts = {}) {
  const report = await buildReport(env);
  const result = { ts: new Date().toISOString(), cron: opts.cron || null, template: report.template, sent: false };
  if (opts.send) {
    if (!env.FEISHU_WEBHOOK_URL) {
      result.error = "FEISHU_WEBHOOK_URL 未配置（wrangler secret put）";
    } else {
      const r = await sendFeishuCard(env, report.card);
      result.sent = r.ok;
      result.status = r.status;
      result.body = r.body;
    }
  }
  await env.KV.put("report/last", JSON.stringify({ ...result, card: report.card }));
  return result;
}

// sendFeishuCard · 飞书自定义机器人 webhook（open.feishu.cn add-custom-bot，2026-09-05 核实）：
// 不需要 app_id / app_secret / tenant_access_token；签名 = base64(HMAC-SHA256(key = `${timestamp}\n${secret}`, msg = ""))。
// 限 100 次/分钟、请求体 ≤ 20 KB。
async function sendFeishuCard(env, card) {
  const payload = { msg_type: "interactive", card };
  if (env.FEISHU_WEBHOOK_SECRET) {
    const timestamp = String(Math.floor(Date.now() / 1000));
    const keyData = new TextEncoder().encode(`${timestamp}\n${env.FEISHU_WEBHOOK_SECRET}`);
    const key = await crypto.subtle.importKey("raw", keyData, { name: "HMAC", hash: "SHA-256" }, false, ["sign"]);
    const sig = await crypto.subtle.sign("HMAC", key, new Uint8Array(0));
    payload.timestamp = timestamp;
    payload.sign = btoa(String.fromCharCode(...new Uint8Array(sig)));
  }
  const r = await fetch(env.FEISHU_WEBHOOK_URL, {
    method: "POST",
    headers: { "content-type": "application/json; charset=utf-8" },
    body: JSON.stringify(payload),
  });
  const body = (await r.text()).slice(0, 300);
  let ok = r.ok;
  try { const j = JSON.parse(body); if (j && typeof j.code === "number") ok = j.code === 0; } catch {}
  return { ok, status: r.status, body };
}

// ───────────────────────── 小工具 ─────────────────────────

async function authNodeOrAdmin(env, request) {
  const tok = bearer(request);
  if (!tok) return null;
  if (env.ADMIN_TOKEN && tok === env.ADMIN_TOKEN) return "admin";
  const host = await env.KV.get("tok/node/" + tok);
  return host || null;
}
async function isAdmin(env, request) {
  const tok = bearer(request);
  return Boolean(env.ADMIN_TOKEN && tok && tok === env.ADMIN_TOKEN);
}
function bearer(request) {
  const a = request.headers.get("authorization") || "";
  return a.startsWith("Bearer ") ? a.slice(7).trim() : null;
}
async function getJSON(env, key) {
  const v = await env.KV.get(key);
  if (!v) return null;
  try { return JSON.parse(v); } catch { return null; }
}
async function dumpPrefix(env, prefix) {
  const list = await env.KV.list({ prefix });
  const out = {};
  for (const k of list.keys) out[k.name] = await getJSON(env, k.name);
  return out;
}
function gib(bytes) {
  return (bytes / GIB).toFixed(bytes >= 100 * GIB ? 0 : 1).replace(/\.0$/, "");
}
function fmtLocal(d, tzH) {
  const l = new Date(d.getTime() + tzH * 3600 * 1000);
  return l.toISOString().slice(0, 16).replace("T", " ");
}
function json(obj, status = 200) {
  return new Response(JSON.stringify(obj, null, 2) + "\n", {
    status,
    headers: { "content-type": "application/json; charset=utf-8", "cache-control": "no-store" },
  });
}
// notFound · 全部 404 分支的唯一出口。响应体刻意不含任何信息（同 subscription.go notFoundResponse）。
function notFound() {
  return new Response("not found\n", { status: 404, headers: { "cache-control": "no-store" } });
}

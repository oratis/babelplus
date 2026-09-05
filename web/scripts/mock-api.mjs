#!/usr/bin/env node
/**
 * mock-api.mjs —— **仅供本机看界面用**的假后端 + 反向代理。
 *
 * 为什么要有它：用户面板与后台的每一页都是「先探测 / 先取数，再渲染」，没有 API 就只有登录页与
 * 「请求没能到达后台」两种画面 —— 评审界面与做视觉改版时看不到任何真实页面。
 * 本机跑真后端要 Docker + Postgres + 迁移（local-development.md §3），只为看一眼版式太重。
 *
 * 它做两件事：
 *   1. `/api/*` 按 `openapi/openapi.yaml` 的**信封与字段名**回一份写死的样例数据（数字是编的，
 *      **只用于看版式**，不要拿它当任何事实）；没写到的端点回 501 `NOT_IMPLEMENTED`，
 *      正好是生产里那些没实现端点的真实形态，前端对 501 的呈现也能一并看到。
 *   2. 其余请求原样转发给 Vite dev server（同源，于是 `runtime-config.js` 的 `apiBaseUrl: ''` 不用改）。
 *      ⚠️ 不转发 WebSocket：HMR 连不上，改代码要手动刷新。
 *
 * 用法：
 *   node scripts/mock-api.mjs --mode user  --port 5178 --upstream http://localhost:5177
 *   node scripts/mock-api.mjs --mode admin --port 5179 --upstream http://localhost:5174
 * 然后打开 http://localhost:5178 —— 用户面板任意邮箱 + 任意密码即可登录。
 *
 * 🔴 不进任何构建产物，不被任何 package.json 脚本引用；CI 不跑它。
 */
import http from 'node:http';

const args = Object.fromEntries(
  process.argv.slice(2).reduce((acc, a, i, arr) => {
    if (a.startsWith('--')) acc.push([a.slice(2), arr[i + 1] && !arr[i + 1].startsWith('--') ? arr[i + 1] : 'true']);
    return acc;
  }, []),
);
const MODE = args.mode ?? 'user';
const PORT = Number(args.port ?? (MODE === 'admin' ? 5179 : 5178));
const UPSTREAM = new URL(args.upstream ?? (MODE === 'admin' ? 'http://localhost:5174' : 'http://localhost:5177'));

const rid = () => '01J' + Math.random().toString(36).slice(2, 12).toUpperCase().padEnd(10, '0') + 'MOCK';
const now = Date.now();
const iso = (offsetMs) => new Date(now + offsetMs).toISOString();
const GiB = 1024 ** 3;
const H = 3600 * 1000;
const D = 24 * H;

const SUMMARY = {
  plan_name: '标准通行证 · 20 GB / 30 天',
  upload_bytes: Math.round(0.9 * GiB),
  download_bytes: Math.round(7.4 * GiB),
  total_bytes: 20 * GiB,
  expired_at: iso(17 * D),
  reset_at: iso(17 * D),
  device_count: 2,
  device_limit: 5,
};
const ME = {
  id: 1024,
  email: 'traveler@example.com',
  banned: false,
  balance_amount: 1250,
  commission_balance_amount: 0,
  totp_enabled: false,
  created_at: iso(-40 * D),
  subscription: SUMMARY,
};
const NOTICES = [
  { id: 3, title: '9 月 12 日 02:00–02:30 东京节点维护', content: '维护期间东京线路会中断约 10 分钟，客户端会自动切到备用线路。', pinned: true, published_at: iso(-1 * D) },
  { id: 2, title: '新增新加坡出口', content: '订阅已自动包含新加坡节点，无需重新导入。', pinned: false, published_at: iso(-3 * D) },
  { id: 1, title: '关于流量重置日', content: '重置日等于你的订单日，不是每月 1 号。', pinned: false, published_at: iso(-12 * D) },
];
const DEVICES = [
  { id: 1, ip: '111.199.185.207', node_id: 2, node_name: '东京 · HY2', first_seen_at: iso(-6 * H), last_seen_at: iso(-3 * 60000) },
  { id: 2, ip: '117.129.39.124', node_id: 1, node_name: '香港 · REALITY', first_seen_at: iso(-2 * D), last_seen_at: iso(-42 * 60000) },
];
const FETCH_LOG = Array.from({ length: 6 }, (_, i) => ({
  id: 100 - i,
  request_at: iso(-(i * 5 + 1) * H),
  request_ip: i % 2 ? '117.129.39.124' : '111.199.185.207',
  user_agent: i % 3 === 0 ? 'clash-verge/v2.3.1 mihomo' : i % 3 === 1 ? 'Shadowrocket/2.2.60' : 'sing-box 1.12.0',
  sub_token_id: 1,
  sub_token_name: 'mac',
  format: ['clash', 'base64', 'singbox'][i % 3],
}));
const PLANS = [
  { id: 1, name: '体验', type: 'period', description: '每个账号一次。', currency: 'CNY', prices: [{ period: 'onetime', amount: 1800 }], transfer_enable_bytes: 3 * GiB, device_limit: 2, visible: true, sort: 1 },
  { id: 2, name: '短途', type: 'period', description: '一场会议、两周探亲。', currency: 'CNY', prices: [{ period: 'onetime', amount: 3200 }], transfer_enable_bytes: 10 * GiB, device_limit: 3, visible: true, sort: 2 },
  { id: 3, name: '标准', type: 'period', description: '多数人一个月工作用的那一档。', currency: 'CNY', prices: [{ period: 'monthly', amount: 6400 }], transfer_enable_bytes: 20 * GiB, device_limit: 5, visible: true, sort: 3 },
  { id: 4, name: '常驻', type: 'period', description: '长住、学期、一个月的办公。', currency: 'CNY', prices: [{ period: 'monthly', amount: 13600 }], transfer_enable_bytes: 50 * GiB, device_limit: 10, visible: true, sort: 4 },
  { id: 5, name: '流量包 10 GB', type: 'traffic_pack', description: '当月有效，不改到期日。', currency: 'CNY', prices: [{ period: 'onetime', amount: 2400 }], transfer_enable_bytes: 10 * GiB, device_limit: 0, visible: true, sort: 9 },
];
const ORDERS = [
  { trade_no: '20260905A7K2Q', type: 'renew', status: 'completed', plan_id: 3, plan_name: '标准', period: 'monthly', currency: 'CNY', total_amount: 6400, discount_amount: 0, surplus_amount: 0, balance_amount: 0, payable_amount: 6400, created_at: iso(-13 * D), paid_at: iso(-13 * D + 6 * 60000) },
  { trade_no: '20260808C1M9X', type: 'traffic_pack', status: 'completed', plan_id: 5, plan_name: '流量包 10 GB', period: 'onetime', currency: 'CNY', total_amount: 2400, discount_amount: 400, surplus_amount: 0, balance_amount: 0, payable_amount: 2000, created_at: iso(-28 * D), paid_at: iso(-28 * D + 3 * 60000) },
  { trade_no: '20260901P0ZZ4', type: 'new', status: 'pending', plan_id: 4, plan_name: '常驻', period: 'monthly', currency: 'CNY', total_amount: 13600, discount_amount: 0, surplus_amount: 0, balance_amount: 1250, payable_amount: 12350, created_at: iso(-2 * H), expires_at: iso(22 * H) },
];
const TICKETS = [
  { public_id: 'T-8F3K2', subject: '东京线路晚高峰很慢', category: 'connectivity', status: 'replied', level: 1, created_at: iso(-3 * D), updated_at: iso(-1 * D), last_reply_at: iso(-1 * D) },
  { public_id: 'T-2Q9ZM', subject: '发票怎么开', category: 'billing', status: 'closed', level: 0, created_at: iso(-20 * D), updated_at: iso(-19 * D), last_reply_at: iso(-19 * D) },
];
const USAGE = (range) => {
  const n = range === '7d' ? 7 : range === '90d' ? 90 : 30;
  const points = Array.from({ length: n }, (_, i) => {
    const day = n - 1 - i;
    const w = 0.15 + 0.85 * Math.abs(Math.sin(i * 0.7)) * (i % 7 === 5 ? 0.3 : 1);
    return { date: new Date(now - day * D).toISOString().slice(0, 10), upload_bytes: Math.round(w * 0.12 * GiB), download_bytes: Math.round(w * 0.9 * GiB) };
  });
  return { range, points, total_upload_bytes: points.reduce((s, p) => s + p.upload_bytes, 0), total_download_bytes: points.reduce((s, p) => s + p.download_bytes, 0) };
};
const NODES = [
  { id: 1, name: '香港 · REALITY', region: 'HK', type: 'vless', status: 'online', load_pct: 23 },
  { id: 2, name: '东京 · HY2', region: 'JP', type: 'hysteria2', status: 'online', load_pct: 61 },
  { id: 3, name: '新加坡 · REALITY', region: 'SG', type: 'vless', status: 'degraded', load_pct: 88 },
];
const ADMIN_USERS = Array.from({ length: 12 }, (_, i) => ({
  id: 1000 + i,
  email: `user${i + 1}@example.com`,
  banned: i === 7,
  balance_amount: i * 340,
  plan_name: ['标准', '常驻', '短途', '体验'][i % 4],
  expired_at: iso((i * 3 - 5) * D),
  upload_bytes: Math.round((i * 0.13) * GiB),
  download_bytes: Math.round((i * 1.7 + 0.4) * GiB),
  transfer_enable_bytes: [20, 50, 10, 3][i % 4] * GiB,
  device_limit: [5, 10, 3, 2][i % 4],
  group_id: 1,
  created_at: iso(-(i * 9 + 2) * D),
}));
const ADMIN_NODES = [
  { id: 1, name: 'bp-node-hk1', type: 'vless', host: '35.215.158.52', port: 443, region: 'asia-east2', enabled: true, group_ids: [1], config_rev: 14, user_rev: 302, last_push_at: iso(-50000), last_status_at: iso(-30000), load_status: { cpu: 0.21, mem: 0.38, uptime: 1234567, online: 17 } },
  { id: 2, name: 'bp-node-jp1', type: 'hysteria2', host: '34.104.192.233', port: 443, region: 'asia-northeast1', enabled: true, group_ids: [1], config_rev: 14, user_rev: 302, last_push_at: iso(-70000), last_status_at: iso(-40000), load_status: { cpu: 0.64, mem: 0.52, uptime: 987654, online: 41 } },
  { id: 3, name: 'bp-node-sg1', type: 'vless', host: '34.2.143.75', port: 443, region: 'asia-southeast1', enabled: false, group_ids: [1], config_rev: 13, user_rev: 298, last_push_at: iso(-5 * H), last_status_at: iso(-4 * H) },
];
const ADMIN_ORDERS = ORDERS.map((o, i) => ({ order: o, user_id: 1000 + i, user_email: `user${i + 1}@example.com` }));
const AUDIT = Array.from({ length: 8 }, (_, i) => ({
  id: 500 - i,
  admin_id: 1,
  request_id: rid(),
  ip: '203.0.113.' + (10 + i),
  user_agent: 'Mozilla/5.0',
  action: ['node.disable', 'user.ban', 'order.mark_paid', 'plan.update', 'settings.update'][i % 5],
  target_type: ['node', 'user', 'order', 'plan', 'settings'][i % 5],
  target_id: String(1000 + i),
  before: { enabled: true },
  after: { enabled: false },
  reason: i % 2 ? '例行维护' : undefined,
  created_at: iso(-(i * 7 + 1) * H),
}));
const DASHBOARD = { online_users: 58, active_nodes: 2, total_nodes: 3, today_upload_bytes: Math.round(9.2 * GiB), today_download_bytes: Math.round(71.5 * GiB), today_revenue_amount: 38400, month_revenue_amount: 412800, pending_tickets: 3, underpaid_orders: 1 };

const list = (data, extra = {}) => ({ data, meta: { request_id: rid(), has_more: false, total: data.length, ...extra } });
const one = (data) => ({ data, meta: { request_id: rid() } });
const err = (status, code, message) => [status, { error: { code, message }, meta: { request_id: rid() } }];

/** 路径 → [status, body]。查不到回 501，形态与生产的 `responseErrorHandler` 一致。 */
function route(method, url) {
  const p = url.pathname;
  const q = url.searchParams;
  const m = (re) => p.match(re);
  if (method === 'OPTIONS') return [204, null];

  // ── 认证 ──
  if (p === '/api/v1/auth/login' && method === 'POST') return [200, one({ access_token: 'mock-access-token', refresh_token: 'mock-access-token', token_type: 'Bearer', expires_in: 900 })];
  if (p === '/api/v1/auth/refresh' && method === 'POST') return [200, one({ access_token: 'mock-access-token-2', refresh_token: 'mock-access-token-2', token_type: 'Bearer', expires_in: 900 })];
  if (p === '/api/v1/auth/logout') return [204, null];

  // ── 用户面 ──
  if (p === '/api/v1/user/me') return [200, one(ME)];
  if (p === '/api/v1/user/subscription' && method === 'GET') return [200, one({ urls: { short: 'https://sub.example.net/s/9k2Qx7', long: 'https://sub.example.net/api/v1/client/subscribe?token=9k2Qx7mockmockmock', clash: 'https://sub.example.net/s/9k2Qx7?flag=clash', singbox: 'https://sub.example.net/s/9k2Qx7?flag=singbox', base64: 'https://sub.example.net/s/9k2Qx7?flag=base64' }, summary: SUMMARY })];
  if (p === '/api/v1/user/subscription/tokens' && method === 'GET') return [200, list([{ id: 1, name: 'mac', created_at: iso(-30 * D), masked: 'sub_9k2Q…7mZ', last_used_at: iso(-3 * 60000) }, { id: 2, name: 'iphone', created_at: iso(-30 * D), masked: 'sub_Ab4R…1qP', last_used_at: iso(-2 * H) }])];
  if (p === '/api/v1/user/subscription/fetch-log') return [200, list(FETCH_LOG.slice(0, Number(q.get('limit') ?? 10)))];
  if (p === '/api/v1/user/devices' && method === 'GET') return [200, list(DEVICES)];
  if (m(/^\/api\/v1\/user\/devices\/\d+$/) && method === 'DELETE') return [200, one({ removed: 1, effective_within_seconds: 60 })];
  if (p === '/api/v1/user/usage') return [200, one(USAGE(q.get('range') ?? '30d'))];
  if (p === '/api/v1/user/nodes') return [200, list(NODES)];
  if (p === '/api/v1/user/wallet') return [200, one({ balance_amount: 1250, commission_pending_amount: 0, commission_available_amount: 800 })];
  if (p === '/api/v1/user/wallet/transactions') return [200, list([{ id: 1, type: 'order_payment', amount: -2000, balance_after: 1250, created_at: iso(-28 * D), note: '订单 20260808C1M9X' }, { id: 2, type: 'commission_transfer', amount: 3250, balance_after: 3250, created_at: iso(-31 * D) }])];
  if (p === '/api/v1/user/invite/codes') return [200, list([{ id: 1, code: 'BPX-7Q2K', status: 'active', created_at: iso(-30 * D), max_uses: 5, used: 2 }])];
  if (p === '/api/v1/user/commissions') return [200, list([{ id: 1, amount: 800, status: 'available', created_at: iso(-9 * D), from_user_id: 1003 }])];
  if (p === '/api/v1/user/diagnose') return [200, one({ checks: [
    { key: 'subscription', status: 'ok', title: '订阅有效', detail: '17 天后到期，剩余 11.7 GB。' },
    { key: 'devices', status: 'ok', title: '设备 2 / 5', detail: '最近 3 分钟有设备在线。' },
    { key: 'fetch', status: 'ok', title: '客户端 1 小时前拉取过订阅', detail: 'clash-verge/v2.3.1' },
    { key: 'traffic', status: 'warn', title: '最近 30 分钟没有流量', detail: '如果你正在用，说明连接可能没走通。' },
  ], data_delay_note: '以上数据最多延迟 60 秒。' })];
  if (p === '/api/v1/plans') return [200, list(PLANS)];
  if (p === '/api/v1/orders' && method === 'GET') return [200, list(ORDERS)];
  if (m(/^\/api\/v1\/orders\/[A-Z0-9]+$/) && method === 'GET') return [200, one(ORDERS.find((o) => p.endsWith(o.trade_no)) ?? ORDERS[0])];
  if (p === '/api/v1/tickets' && method === 'GET') return [200, list(TICKETS)];
  if (m(/^\/api\/v1\/tickets\/T-[A-Z0-9]+$/) && method === 'GET') return [200, one({ ...TICKETS[0], messages: [
    { id: 1, author: 'user', body: '晚上 9 点到 11 点东京线路只有 200 KB/s，其他时间正常。', created_at: iso(-3 * D) },
    { id: 2, author: 'staff', body: '收到。晚高峰跨境拥塞是常态，建议把默认组切到 HY2（UDP）通路；我们也在观察东京出口。', created_at: iso(-1 * D) },
  ] })];
  if (p === '/api/v1/notices') return [200, list(NOTICES.slice(0, Number(q.get('limit') ?? 20)))];

  // ── 管理面 ──
  if (p === '/api/v1/admin/audit') return [200, list(AUDIT.slice(0, Number(q.get('limit') ?? 20)))];
  if (p === '/api/v1/admin/dashboard') return [200, one(DASHBOARD)];
  if (p === '/api/v1/admin/users' && method === 'GET') return [200, list(ADMIN_USERS, { total: 128, has_more: true, next_cursor: 'c2' })];
  if (m(/^\/api\/v1\/admin\/users\/\d+$/) && method === 'GET') return [200, one(ADMIN_USERS[0])];
  if (p === '/api/v1/admin/nodes' && method === 'GET') return [200, list(ADMIN_NODES)];
  if (m(/^\/api\/v1\/admin\/nodes\/\d+$/) && method === 'GET') return [200, one(ADMIN_NODES[0])];
  if (m(/^\/api\/v1\/admin\/nodes\/\d+\/keys$/) && method === 'GET') return [200, list([{ id: 1, node_id: 1, prefix: 'nk_7Q2K', created_at: iso(-30 * D), last_used_at: iso(-30000) }])];
  if (p === '/api/v1/admin/orders' && method === 'GET') return [200, list(ADMIN_ORDERS)];
  if (p === '/api/v1/admin/plans' && method === 'GET') return [200, list(PLANS)];
  if (p === '/api/v1/admin/tickets' && method === 'GET') return [200, list(TICKETS.map((t, i) => ({ ...t, user_id: 1000 + i, user_email: `user${i + 1}@example.com` })))];
  if (p === '/api/v1/admin/admins' && method === 'GET') return [200, list([{ id: 1, email: 'ops@example.com', role: 'owner', totp_enabled: true, created_at: iso(-90 * D), last_login_at: iso(-2 * H) }])];
  if (p === '/api/v1/admin/notices' && method === 'GET') return [200, list(NOTICES)];
  if (p === '/api/v1/admin/stats') return [200, one({ range: '30d', points: USAGE('30d').points, total_upload_bytes: 0, total_download_bytes: 0, by_node: ADMIN_NODES.map((n, i) => ({ node_id: n.id, node_name: n.name, download_bytes: Math.round((30 - i * 9) * GiB), upload_bytes: Math.round((3 - i) * GiB) })) })];
  if (p === '/api/v1/admin/invites') return [200, list([{ id: 1, code: 'BPX-7Q2K', status: 'active', created_at: iso(-30 * D), owner_user_id: 1000, max_uses: 5, used: 2 }])];
  if (p === '/api/v1/admin/settings' && method === 'GET') return [200, one({ site_name: 'babel.plus', support_email: 'help@example.com', invite_required: true, device_limit_default: 5, updated_at: iso(-2 * D) })];

  return err(501, 'NOT_IMPLEMENTED', `mock-api: ${method} ${p} 没有样例数据（生产里这条也可能就是 501）`);
}

const server = http.createServer((req, res) => {
  const url = new URL(req.url ?? '/', `http://${req.headers.host}`);
  if (url.pathname.startsWith('/api/')) {
    const [status, body] = route(req.method ?? 'GET', url);
    const reqId = body?.meta?.request_id ?? rid();
    res.writeHead(status, {
      'content-type': 'application/json; charset=utf-8',
      'x-request-id': reqId,
      'cache-control': 'no-store',
    });
    res.end(body === null ? '' : JSON.stringify(body));
    // eslint-disable-next-line no-console
    console.log(`${status} ${req.method} ${url.pathname}${url.search}`);
    return;
  }
  // 其余原样转发到 Vite
  const proxied = http.request(
    { hostname: UPSTREAM.hostname, port: UPSTREAM.port, path: req.url, method: req.method, headers: { ...req.headers, host: UPSTREAM.host } },
    (up) => {
      res.writeHead(up.statusCode ?? 502, up.headers);
      up.pipe(res);
    },
  );
  proxied.on('error', (e) => {
    res.writeHead(502, { 'content-type': 'text/plain; charset=utf-8' });
    res.end(`upstream ${UPSTREAM.origin} 不可达：${e.message}\n先启动 Vite dev server。`);
  });
  req.pipe(proxied);
});

server.listen(PORT, '127.0.0.1', () => {
  // eslint-disable-next-line no-console
  console.log(`mock-api (${MODE}) → http://localhost:${PORT}  ⇢  ${UPSTREAM.origin}`);
});

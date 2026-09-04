# web/extension —— 浏览器扩展（MV3，Chrome / Edge）：popup 八个状态、PAC 候选串、代理凭据回填都已实现并有测试；唯一的服务端端点仍是 501，所以它能装、能登录、能显示配额，但还不能转发一个字节

> 日期：2026-09-02 · 性质：**机制说明** · 状态：**执行中**（2026-09-02）—— 扩展侧代码完成；服务端 `GET /api/v1/user/proxy-config` 返回 `501`，直到 [client-products-spec §6.1](../../docs/03-product/client-products-spec.md) 的 **E0（HTTPS 入站计量验证）与 E1（服务端）** 完成
> 事实基线：`openapi/openapi.yaml` 的 `getUserProxyConfig`（本 PR 新增，形状冻结）；`api/internal/handler/unimplemented_test.go` 钉着它为什么是 501
> 关联：[client-products-spec.md](../../docs/03-product/client-products-spec.md)（产品形态与八个状态）、
> [client-products-mockup.html](../../docs/03-product/client-products-mockup.html)（界面稿）、
> [acquisition-channels §5](../../docs/01-research/acquisition-channels.md)（技术边界的出处）、
> [store/](store/)（商店提交材料）、[ADR 0003 §5](../../docs/05-adr/0003-web-hosting-and-reachability.md)（不引外部资源）
> 读者：接手扩展的人，与要把它提交到商店的人。**先读 §1 的三条边界与 §6 的门。**

---

## 1 · 三条边界决定了它长什么样

| # | 边界 | 后果 |
|---|---|---|
| ① | `chrome.proxy` 只认 `http / https / quic / socks4 / socks5` | 扩展说不了 REALITY；上游是**独立的 HTTPS 代理入站**（服务端 E1 要建），与 REALITY 节点不是同一条传输 |
| ② | Chrome 对 SOCKS5 不支持认证 | 只能 HTTPS 代理 + Basic，凭据经 `webRequest.onAuthRequired` 回填 —— 这要求 `host_permissions: ["<all_urls>"]`（Chrome 文档：webRequest 只暴露扩展有 host 权限的请求）。spec §3.2 已订正 |
| ③ | PAC 返回值可以是有序候选串 | `HTTPS a:443; HTTPS b:443` —— 前一台失败 Chrome 自动降到下一台，ADR 0010 的域名池故障转移一行代码都不用写。**末位永远不放 `DIRECT`** |

## 2 · 目录

```
public/            manifest.json · icons/ · _locales/     → 原样落到 dist 根
popup.html / options.html / onboarding.html              → 三个页面入口
src/shared/        pac.ts（PAC 生成）· state.ts（八个状态）· quota.ts（四个阈值）· i18n.ts · diagnostics.ts · messages.ts
src/background/    index.ts（SW 入口，监听器全部顶层同步注册）· controller.ts（状态机）· auth.ts（onAuthRequired）· probe.ts（逐台探测）· proxy.ts · storage.ts · api.ts
src/popup/ src/options/ src/onboarding/                   → React 页面；src/ui/ 是共用组件与 useSnapshot
scripts/gen-icons.mjs  图标的**源**（纯 Node 画 PNG，不放来历不明的二进制）
scripts/package.mjs    dist → 商店 zip，打包前三条只读检查
store/                 商店文案、权限说明、隐私政策、提交清单
```

只有 service worker 持有状态；三个页面都是「发一条消息、拿一份 `Snapshot`」的薄壳（`src/shared/messages.ts`）。

## 3 · 怎么跑

```bash
cd web && pnpm install
pnpm --filter @babelplus/extension test        # 64 个用例：PAC 规则、八个状态、认证回填、探测、状态机、诊断脱敏
pnpm --filter @babelplus/extension build       # tsc + vite → dist/
pnpm --filter @babelplus/extension package     # dist → babelplus-extension-<version>.zip
```

装进浏览器：`chrome://extensions` → 开发者模式 → 「加载已解压的扩展程序」→ 选 `web/extension/dist`。Edge 同样（`edge://extensions`）。

构建期变量（都可为空；空则 popup 显示「未配置」，不编域名）：

| 变量 | 用途 |
|---|---|
| `VITE_BP_API_BASE_URL` / `VITE_BP_API_FALLBACK_URLS` | 第一次登录前的 API 域名池兜底；登录后以 `/user/proxy-config` 的 `control_plane.api_base_urls` 为准 |
| `VITE_BP_WEB_URL` | `Top up` / `Renew` 跳的用户面板（`/plan`）；同样会被运行时下发覆盖 |
| `VITE_BP_BACKUP_PAGE_URL` | 「全部端点不可达」态的备用域名页 |
| `VITE_BP_HELP_URL` | 页脚 Help |

## 4 · 它做了什么（对应 spec 的条目）

- **八个状态**（spec §3.5）：`src/shared/state.ts` 的优先级 + `Popup.tsx` 逐个渲染，`Popup.test.tsx` 逐个断言主按钮。
- **PAC 候选串**（§3.3）：`buildPac` —— 本地 / 私有地址 → 控制面 → 用户直连表 → 用户代理表 → 服务端代理例外 → 服务端直连表 → 默认代理；空端点列表直接抛错，不生成「只剩 DIRECT」的脚本；`mandatory: true` 让脚本本身出错时也不退回直连。
- **分流方向**（§3.4）：默认走代理，只有直连表里的才直连；服务端没下发时用 `rules.ts` 的最小兜底表。
- **凭据回填**（§3.2）：`decideAuth` 只回应 `isProxy` 且 challenger 在凭据表里的质询；同一请求最多两次，第三次取消并进 `auth-rejected`。
- **探测**（§3.5 状态 3）：逐台设「只有这一台」的 PAC 取 `probe_url`，记延迟与出口 IP；全部失败 → **清掉代理设置**再进不可达态（不许留着指向死端点的 PAC）。
- **配额四阈值**（§3.6 / §5）：每 5 分钟 `alarms` 拉一次；用尽 / 到期时**主动断开**（服务端 17 s 内已切断，留着代理只会让每个请求 407 打转）。会话用量 = 服务端最新已用 − 连接时已用，只用于显示。
- **诊断报告**（§3.5 options）：不含 token、密码、端点主机名、邮箱、页面 URL（`diagnostics.test.ts`）。
- **控制面直连**：PAC 里 API / 面板 / 备用页主机一律 `DIRECT` —— 控制面故障不得升级为数据面故障，反之亦然。
- **不做**：mockup 里的「Share anonymous connection reports」开关（v1 没有上报通道，一个什么都不做的开关是撒谎）；per-tab 归属（MV3 非阻塞 `webRequest` 拿不到实际字节数，spec §9 代价 5 已改写）。

## 5 · 与规格不同的地方（都写回了 spec）

| spec 原文 | 实现 | 为什么 |
|---|---|---|
| `host_permissions: []` | `["<all_urls>"]` | Chrome 文档：webRequest 只对扩展有 host 权限的 URL 派发事件；代理质询挂在被代理的目标 URL 上 |
| `GET /api/v1/client/proxy-config` | `GET /api/v1/user/proxy-config` | 需要用户会话、统一信封、会改版 —— 都是 `user` 面的性质；`client` 面是不改版的订阅硬接口 |
| options 三段「Smart / Everything / Off」 | Off = 断开 | 「全部直连」的 PAC 违反「不静默直连」；不存在 `off` 路由模式 |

## 6 · 🔴 门：它现在为什么还不能转发流量

`GET /api/v1/user/proxy-config` 在服务端是 **501**，扩展把它显示为「全部端点不可达 · Couldn't fetch your proxy configuration (HTTP 501)」。打开这道门要按顺序做：

1. 🔴 **E0 已于 2026-09-04 执行，判定「不通过」**：100 MiB 经 HTTPS 入站走完，`stat_user_server` **一个字节都没变**；同一节点同一时间窗的 REALITY 正对照 20 MiB 正常入账（+0.21%）。**所以这道门现在不是「未核实」，是「已知不通」** —— 证据 [e0-metering-20260904](../../docs/evidence/e0-metering-20260904/)。
   根因：写计量表的只有 v2node 经 `/api/v2/server/push`，而 `server_id` 由节点密钥推导；Caddy 是另一个进程，没有密钥、不认识我们的用户。「让 v2node 自己起 http inbound」也已排除（协议白名单里没有 http）。
   **出路三条，都要写代码、都要一次裁决**：给 HTTPS 入站建独立上报通路 / 改 v2node / 放弃扩展这条传输。在裁决之前，下面第 2–4 步全部不排期。
2. **E1（1 周）**：凭据派生（`HMAC(token, node_id)` 前 16 字节是提案，未做安全评审）、`probe_url` 的服务（返回 `{ "ip": … }`，且它的主机必须被 PAC 判为走代理）、`getUserProxyConfig` 的真实现、`probe_resistance` 的回落站点。实现那一刻把它从 `unimplemented_test.go` 的表里删掉。
3. **E5（与 E1 并行，2 周）**：1–2 个测试域名对照 REALITY 跑两周，出一份 HTTPS 入站在大陆的存活率证据。没有它，扩展的 SLO 无法承诺（ADR 0014 要按传输拆两条）。
4. **E4（1–2 周）**：商店提交，见 [store/README.md](store/README.md)。前置是 CWS 开发者账号（$5）与 Edge 合作伙伴中心账号；截图要在真机上装好扩展后截（本仓库里没有）。

## 7 · 代价

- **第二条传输 = 运维面翻倍。** HTTPS 入站的封锁面比 REALITY 大且无 padding；被封时扩展用户整体失联而 REALITY 用户不受影响。告警、SLO、状态页都要按传输拆。
- **`<all_urls>` 比第一版规格宽。** 买到的只有「看见代理质询」这一件事，但商店审核与隐私政策要为它负责；VPN 类扩展普遍申请它，被拒率无数据。
- **探测是串行的**，最坏 N × 5 s；popup 逐行显示进度是对冲，不是消除。
- **会话用量是服务端 5 分钟粒度的差值**，不是实时字节；文案里写的是 "since you connected"，不写 "live"。
- **没有真机验证。** 全部 64 个用例跑在 Node / jsdom 里，`chrome.*` 是内存实现；`chrome.proxy.settings.set` 在真实 Chrome 里的时序、`onAuthRequired` 在 PAC 降级到第二台时是否再次质询、Edge 的 `webRequestAuthProvider` 行为 —— **三条都需实测**，E1 之前测不了（没有入站可连）。

## 8 · 这次没有解决的

- [ ] 🔴 E0：HTTPS 入站的每用户计量能否进 UniProxy 上报路径 —— 未核实（§6 第 1 条）。
- [ ] E1 全部（§6 第 2 条）；`getUserProxyConfig` 仍 501。
- [ ] 真机三条需实测（§7 最后一条）。
- [ ] 商店截图（1280×800 ≥ 1 张）与宣传图；需要真机。
- [ ] Edge Add-ons 对 VPN 类扩展的审核宽严无数据。
- [ ] 「blocked here」提示条（spec §4.3 第 3 条）是浏览器的功能，扩展没有 per-tab 信号，v1 不做。
- [ ] 中文界面只有词典（`i18n.ts`），未经母语校对；商店文案只有英文。

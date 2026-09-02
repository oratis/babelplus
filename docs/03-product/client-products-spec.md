# 客户端产品形态：扩展用 PAC 把「域名池故障转移」白拿，浏览器只做「零配置 + 按站点可见」两件 Chrome 做不到的事；两者共用一套凭据，但走两条互不重叠的传输

> 日期：2026-09-02 · 性质：**设计方案** · 状态：**提案，未批准**（2026-09-02）
> 事实基线：master `671355cfb8d`；技术边界引自 [acquisition-channels.md](../01-research/acquisition-channels.md) §5–§6
> （`chrome.proxy` scheme 枚举、Chrome 对 SOCKS5 不支持认证、MV3 `webRequestAuthProvider`、
> Electron `session.setProxy`、SmartScreen 2026-05 新规、Xray 二进制 Defender 误报）；
> 商业前提引自 [go-to-market-plan.md](go-to-market-plan.md)（海外销售、非中国公民、不做身份验证）
> 关联：[ADR 0015](../05-adr/0015-client-strategy.md)（**本文不推翻它** —— 0015 管的是第三方客户端首推，
> 本文管的是我们自研的两个客户端，两者并存）；[ADR 0010](../05-adr/0010-domain-strategy.md)（域名池）、
> [ADR 0004](../05-adr/0004-transport-hardening.md)（传输层）、[tutorials-spec.md](tutorials-spec.md)、
> [page-inventory.md](page-inventory.md)
> 读者：要把这两个东西做出来的人。**先读 §1 的三条边界，它们决定了后面每一个界面长什么样。**

---

## 1 · 三条不可协商的技术边界（先读这个）

后面所有产品设计都是这三条的推论，不是审美选择。

| # | 边界 | 出处 | 产品后果 |
|---|---|---|---|
| **①** | **扩展说不了 VLESS / REALITY。** `chrome.proxy` 的 scheme 只有 `http / https / quic / socks4 / socks5` | [chrome.proxy 文档](https://developer.chrome.com/docs/extensions/reference/api/proxy) | 扩展必须有一条**独立的 HTTPS 代理入站**（F9）。它与浏览器走的 REALITY **不是同一条传输**，封锁面、存活期、SLO 全部要分开算 |
| **②** | **Chrome 对 SOCKS5 不支持任何认证** | [Chromium net/docs/proxy.md](https://chromium.googlesource.com/chromium/src/+/HEAD/net/docs/proxy.md) | 扩展的上游只能是 HTTP(S) 代理 + Basic 认证（`onAuthRequired`）。**本机 SOCKS 端口不需要认证**，所以浏览器那条路反而简单 |
| **③** | **浏览器可以说 REALITY** —— Electron 的 `session.setProxy` 指向本机 sing-box 的 SOCKS 端口即可 | [Electron session](https://www.electronjs.org/docs/latest/api/session) | 浏览器**完全复用现有节点**，服务端零改动。这是它相对扩展唯一的结构性优势 |

**一句话**：扩展换来的是「装一下就能用」，代价是多一条更脆弱的传输；浏览器换来的是「复用最硬的传输」，代价是一个要签名、要跟版、会被杀软误报的二进制。

---

## 2 · 两个产品的分工

|  | **扩展** | **浏览器** |
|---|---|---|
| 一句话 | 已经在用 Chrome/Edge 的人，装一个按钮 | 不想学任何东西的人，下一个东西打开就是通的 |
| 传输 | HTTPS 代理入站（新建） | VLESS-REALITY（现有节点） |
| 覆盖 | 只有浏览器流量，且只有这个浏览器 | 只有浏览器流量，且只有这个浏览器 |
| 安装门槛 | 商店两下点击 | 下载 80–200 MB + 过 SmartScreen / Gatekeeper |
| 分发 | Chrome Web Store / Edge Add-ons（**是一个被搜索的货架**） | 官网下载（**不是货架，没有自然流量**） |
| 获客价值 | **有**：商店搜索是入口 | **无**：它只服务已经决定用我们的人 |
| 工期 | 4–6 周 | 8–10 周 |
| 持续成本 | 商店审核往返 | 签名证书 + 每 8 周跟 Chromium 版 + 杀软误报处理 |

> 🔴 **诚实标注**：[go-to-market-plan.md](go-to-market-plan.md) §4.2 的裁决是「浏览器不做」。
> 本文按用户明确要求把它的完整形态设计出来，**并不撤回那条裁决的理由** ——
> 它们记在 §9 代价第 1 条。要做，就要知道在买什么、花多少。

---

## 3 · 扩展：完整产品形态

### 3.1 身份

| 项 | 值 |
|---|---|
| 商店名 | `babel.plus — Access for China Travel` |
| 简介 | 一句话，英文，不出现 VPN 一词的营销用法（避开 Featured 排除条款的同时不影响可上架性） |
| 图标 | 单色地球 + 一条穿过的线；连接态在图标上加角标（`chrome.action.setBadgeText`） |
| 语言 | **英文优先**（商业前提），中文为第二语言 |

### 3.2 权限清单（逐条写给审核看）

```json
"permissions": ["proxy", "storage", "alarms", "webRequest", "webRequestAuthProvider"],
"host_permissions": []
```

- `proxy` —— 设置代理，产品的全部功能
- `webRequest` + `webRequestAuthProvider` —— **仅**用于 `onAuthRequired` 回填代理凭据（MV3 必需，Chrome 108+）
- `storage` —— 存会话与规则
- `alarms` —— 定时刷新配额与端点列表
- 🔴 **不申请 `<all_urls>`、不注入 content script、不读页面内容。** Urban VPN 采集 AI 对话、FreeVPN.One 偷截图两个案例都是从这里开始的；商店审核也从这里看。

### 3.3 连接机制：用 PAC 白拿域名池故障转移

**不用 `fixed_servers`，用 `pac_script` 内联字符串。** 理由是 PAC 的返回值可以是一个**有序候选串**：

```javascript
function FindProxyForURL(url, host) {
  if (isDirect(host)) return "DIRECT";
  return "HTTPS a.example:443; HTTPS b.example:443; HTTPS c.example:443; DIRECT";
}
```

Chrome 的网络栈在前一个代理连接失败时会**自动降到下一个**。也就是说：

> **[ADR 0010](../05-adr/0010-domain-strategy.md) 的域名池，在扩展里不需要我们写一行故障转移代码 —— 它是 PAC 的既有语义。**

这是本设计里唯一一处「白拿」，值得单独写下来。三条配套规则：

1. **末位必须是 `DIRECT` 还是不是？** —— **不是**。末位放 `DIRECT` 意味着所有端点都挂掉时**静默直连**，用户以为自己被保护着。改为末位仍是代理，全部失败则连接失败，扩展弹出「所有线路不可达 + 备用域名页链接」。**故障要响，不要静默降级。**
2. PAC 字符串由后端下发（§3.7 的 `GetBrowserProxyConfig`），扩展只做缓存与定时刷新，**不在扩展里硬编码任何域名**。
3. 端点顺序由后端按用户地区打乱后下发，避免所有用户压同一台。

### 3.4 分流规则（对「来华外国人」是反过来的）

面向中国大陆用户的机场规则是「国内直连、国外走代理」。**我们的用户是在中国的外国人，方向相同但清单不同**：

| 类别 | 动作 | 例 |
|---|---|---|
| 被墙的境外服务 | **走代理** | Google 全家、YouTube、WhatsApp、Slack、Notion、X、Instagram、AI 服务 |
| 中国境内站点 | **直连** | `.cn`、淘宝、微信网页、高德、12306、银行 |
| 本地/私有地址 | **直连** | RFC1918、`localhost`、`*.local` |
| 其余 | **走代理**（默认代理，不是默认直连） | — |

> 默认走代理是刻意的：用户是外国人，他打不开的东西比打得开的多；默认直连会让「第一次点开 Google 还是白屏」，
> 而那是 [user-journey](user-journey.md) §4 定义的最贵的一次失败。
> 代价是流量成本上升，**由 §3.6 的配额条实时可见来对冲**。

规则表由后端下发 + 本地可编辑（`Always proxy` / `Always direct` 两个列表）。

### 3.5 界面：一个 popup、一个 options、一个 onboarding

**Popup（360 × 内容自适应）自上而下五块**：

1. **主开关** —— 一个大按钮，四态：`Off` / `Connecting` / `On` / `Error`
2. **出口地区** —— 下拉，显示 `Hong Kong · 28 ms`（延迟来自扩展自测，不是后端声称）
3. **配额条** —— `12.4 / 20 GB · 18 days left`，进度条；> 90% 变橙，用尽变红
4. **本次会话用量** —— `Session 240 MB`，让用户建立「什么动作费流量」的直觉
5. **底部三个链接** —— `Top up` / `Help` / `Sign out`

**八个状态，每个都必须有设计**（这是本节最重要的部分，多数扩展只做前三个）：

| 状态 | 用户看到 | 主按钮 |
|---|---|---|
| 未登录 | 邮箱 + 密码 + `Get a pass →` | Sign in |
| 已登录 · 关闭 | 配额条 + 地区 | Connect |
| 连接中 | 转圈 + `Testing 3 endpoints…` | 可取消 |
| 已连接 | 绿点 + 出口 IP 与地区 + 会话用量 | Disconnect |
| **配额将尽（< 10%）** | 橙条 + `You have 1.8 GB left` | Top up（主按钮变它） |
| **配额用尽** | 红条 + `Your pass is used up` | Buy more；开关禁用 |
| **已过期** | `Your 30-day pass ended on Sep 2` | Renew |
| **全部端点不可达** | `Can't reach any server` + **备用域名页链接** + `Copy diagnostics` | Retry |

**Options 页**：路由模式（Smart / Everything / Off）、两个自定义列表、开机自动连接、诊断报告一键复制（含端点探测结果、扩展版本、最后一次配置刷新时间 —— 直接进工单，兑现 [tutorials-spec](tutorials-spec.md) 的排障定位）。

**Onboarding**：安装后自动打开一页，三步 —— 登录 → 选地区 → 点连接 → 打开一个验证页显示「你现在的出口 IP 是 X」。验证页是 [user-journey](user-journey.md) §4 已有的设计，扩展直接复用。

### 3.6 配额与计费的可见性

扩展每 5 分钟（`alarms`）拉一次 `GetUserUsage`，本地按会话累计做插值显示。
🔴 **本地累计只用于显示，永远不用于计费判定** —— 计费口径唯一来源是节点上报（[data-model §9](../02-architecture/data-model.md)）。
两者出现 > 15% 偏差时在诊断报告里标出来，那是 pricing §3.5.9 条件 1（计量系数 `k` 双侧偏离）的一个免费探针。

### 3.7 服务端需要新增的东西

| 项 | 内容 |
|---|---|
| **入站** | 每个节点加一个 HTTPS 代理入站（Caddy `forwardproxy` + `probe_resistance` 回落真站，或 Xray `http` inbound + TLS + accounts） |
| **凭据** | Basic 的 `user:pass` 由订阅 token 派生（`HMAC(token, node_id)` 取前 16 字节），**不新建一套密码**；重置订阅即全部失效 |
| **端点** | `GET /api/v1/client/proxy-config` → `{ pac: "<string>", endpoints: [...], rules_rev, expires_in }`。128 个 operation 里没有它，需一次契约修订 |
| 🔴 **计量** | HTTPS 入站的每用户字节能否进现有 UniProxy 上报路径 —— **待核实**。若不能，需要一条独立上报通路写进 `stat_user_server`。**这是整个扩展方案的第一个技术门槛，先验证它再写界面** |

### 3.8 上架

| 商店 | 优先级 | 说明 |
|---|---|---|
| Chrome Web Store | **主**（海外用户为主，CWS 是他们的默认货架） | $5 一次性；审核 24–72h；VPN 类可上架但**不会被 Featured**；每账号默认 2 个扩展槽位 |
| Edge Add-ons | 同步上架 | 审核 ≤ 7 个工作日；**大陆可直接访问** —— 对已在中国的用户是唯一能一键安装的官方入口 |
| 官网 zip | 兜底 | 开发者模式加载，写进教程 |

> 商业前提改为海外销售后，**CWS 与 Edge 的优先级相对上一版对调了**：
> 之前 Edge 是主（因为它是墙内唯一可达的货架），现在 CWS 是主（因为买家在海外买）。
> Edge 仍然要上，因为**买家买完之后人会到中国** —— 那时他要装第二台设备就只剩 Edge 这条路。

---

## 4 · 浏览器：完整产品形态

### 4.1 它必须回答的问题：为什么不用 Chrome + 扩展

如果答案只是「一样但打包好了」，那它不值 8–10 周。**只有两件事是 Chrome + 扩展做不到的**，产品必须围绕这两件：

| # | 只有浏览器能做 | 为什么扩展做不到 |
|---|---|---|
| **①** | **零配置**：下载 → 输入一个 pass code → 直接能用。没有商店、没有登录 Google 账号、没有权限弹窗、没有「先要能上网才能装扩展」的鸡生蛋 | 装扩展本身需要先能访问商店。**人已经在中国时，Chrome Web Store 打不开** —— 这就是鸡生蛋，而浏览器是唯一解 |
| **②** | **按站点可见**：每个标签页显示这一页走的是代理还是直连、用了多少流量、慢在哪 | 扩展拿不到 per-tab 的网络归属（不申请 `<all_urls>` 的前提下） |

**鸡生蛋那条是决定性的**：一个已经落地中国、手上只有一台干净笔记本的人，他能做的第一件事是从一个还没被封的域名下载一个 exe。这正是浏览器存在的理由，也说明**它的下载页必须在域名池里、必须极小、必须无 JS**。

### 4.2 技术栈

| 层 | 选型 | 理由 |
|---|---|---|
| 壳 | **Electron**（非 Tauri） | Tauri 在 macOS 走 WKWebView，[Mysk 2026-08](https://mysk.blog/2026/08/04/webkit-proxy-icloud-private-relay-ip-leak/) 报告 WebKit 代理有 DNS prefetch / WebTransport / WebAuthn 三类真实 IP 泄漏。**代理产品不能接受泄漏** |
| 内核 | **sing-box**，随包，由我们自己从源码编译 | 复用 REALITY；自编译是为了签名与降低杀软误报 |
| 接线 | `session.setProxy({ proxyRules: 'socks5://127.0.0.1:<port>', proxyBypassRules })` + `closeAllConnections()` | 本机 SOCKS 不需要认证，绕开边界 ② |
| 端口 | 随机高位端口，只监听 `127.0.0.1`，启动时写入配置 | 不占固定端口、不被别的程序连上 |
| 平台 | v1：Windows x64、macOS arm64 + x64。Linux 与 ARM Windows 延后 | 目标用户是商旅笔记本 |

### 4.3 界面

**整体是 Chrome 的样子**（用户不需要学），只加三处：

1. **地址栏右侧的「地球胶囊」** —— 常驻。显示 `HK · 12.4/20 GB`。点开是一个面板：地区切换、配额条、到期日、`Top up`、`Diagnostics`。这是扩展 popup 的同一套信息架构，刻意保持一致。
2. **每个标签页标题左侧的路由角标** —— 三态：`↗` 走代理 / `·` 直连 / `⚠` 本页有资源加载失败。悬停显示这一页各走了多少字节。
3. **「这个站点在中国被屏蔽」提示条** —— 当一个直连的页面加载失败且它在规则表的「被墙」清单里时，顶部出现一条：`This site is blocked here. Route it through babel.plus?` 一键把该域名加入代理清单并重载。
   > 这一条是整个浏览器最有产品价值的地方：它把「我不知道为什么打不开」这个**最贵的困惑**变成一次点击。

**首次运行三步**：
1. 一个输入框：`Enter your pass code`（或 `Sign in with email`）
2. 自动探测端点、选最快的、显示 `Ready · Hong Kong · 28 ms`
3. 直接落到一个验证页：`You're connected. Your exit IP is X.` + 四个大图标（Google / YouTube / WhatsApp / ChatGPT）点了就打开

**设置页**（一页到底，不做多级）：出口地区、路由模式与两个清单、开机启动、更新通道、导出诊断、退出登录。

### 4.4 v1 明确不做

- ❌ 扩展支持（Chrome 扩展生态整套不带）
- ❌ 账号同步、密码管理器、书签云同步
- ❌ 多 profile、隐私窗口以外的隔离
- ❌ 移动端（另立项，见 §7）
- ❌ 把系统全局流量接管（那是 VPN 客户端，不是浏览器，且直接踩 §9 代价 2）

### 4.5 更新、签名与误报

| 项 | 方案 | 成本 |
|---|---|---|
| 自动更新 | `electron-updater`，更新源放在**域名池里的一个域名**上，且要有第二个源 | 更新源被封 = 用户永远停在旧版，这是本产品最隐蔽的失效模式 |
| macOS | Developer ID 签名 + 公证 | $99/年 |
| Windows | **Azure Artifact Signing**（EV 已不再绕过 SmartScreen，2026-05 官方口径） | $9.99/月 + 身份验证。🔴 **中国主体能否通过身份验证：待核实** |
| 杀软误报 | sing-box 自编译、所有 PE 用同一证书签、每次发版向 Microsoft 提交样本、安装目录加白说明 | Xray-core 官方 release 被 Defender 标 `Trojan:Script/Wacatac.B!ml` 且上游 `not planned` |
| Chromium 跟版 | Electron 每约 8 周一个大版本，跟版是**例行工作不是项目** | 每次约 1 人日 + 回归 |

---

## 5 · 两者共用的东西

| 共用件 | 说明 |
|---|---|
| **账号与凭据** | 同一套 `Login` / 订阅 token；扩展派生 Basic 凭据，浏览器派生 REALITY 参数 |
| **配额与到期** | 同一个 `GetUserUsage`，同一套四阈值文案（正常 / <10% / 用尽 / 过期） |
| **规则表** | 同一份 `rules_rev`，两端都从 `/client/proxy-config` 拿 |
| **域名池** | 同一份端点列表；扩展靠 PAC 候选串，浏览器靠 sing-box 的 `urltest` |
| **诊断报告** | 同一个 JSON 结构，直接贴进工单 |
| **验证页** | 同一个「你的出口 IP 是 X」页面 |

> 结论：**先做扩展，浏览器复用它 70% 的后端与文案**。反过来做要多花约 3 周。

---

## 6 · 执行方案

### 6.1 扩展（4–6 周）

| 里程碑 | 内容 | 完成判据 |
|---|---|---|
| **E0 · 门槛验证（3 天）** | 在 `bp-node-hk1` 上起一个 HTTPS 代理入站；**先回答 §3.7 的计量问题** | 一个真人用 curl 走该入站产生 100 MB，`stat_user_server` 里能查到这 100 MB。**查不到就停，先解决计量** |
| **E1 · 服务端（1 周）** | 凭据派生、`/client/proxy-config`、契约修订、`probe_resistance` 回落 | 无凭据访问该域名返回一个正常网站；带凭据能代理 |
| **E2 · 扩展内核（1 周）** | MV3 骨架、PAC 生成与注入、`onAuthRequired`、端点探测与排序 | 装上后能连、断网时按候选串降级、全部失败时报错不静默直连 |
| **E3 · 界面（1 周）** | 八个状态、options、onboarding、诊断导出 | 八个状态逐个截图存进 evidence |
| **E4 · 上架（1–2 周）** | 图标与商店素材、隐私政策、权限说明、CWS + Edge 提交 | 两家都过审 |
| **E5 · 存活性实测（与 E2 并行，2 周）** | 1–2 个测试域名对照 REALITY 跑两周 | 出一份 evidence：HTTPS 入站在大陆的可用率与被封时间 |

### 6.2 浏览器（8–10 周，且必须在扩展之后）

| 里程碑 | 内容 | 完成判据 |
|---|---|---|
| **B1 · 壳与内核（2 周）** | Electron 骨架、随包 sing-box、`setProxy` 接线、进程生命周期与崩溃重启 | 打开就能上 Google；杀掉 sing-box 进程能自愈 |
| **B2 · 界面（3 周）** | 地球胶囊、per-tab 角标、被墙提示条、首次运行、设置页 | §4.3 三处特性逐个可演示 |
| **B3 · 分发（2 周）** | 签名、公证、`electron-updater`、双更新源、下载页（< 20 KB 无 JS，进域名池） | 在一台干净的 Windows 与 macOS 上从零安装无警告 |
| **B4 · 灰度（1 周）** | 5 人用 7 天 | 无崩溃、无误报、更新通道验证过一次 |

### 6.3 排期与前置

```
E0 ──► E1 ──► E2 ──► E3 ──► E4          （扩展可售）
       └────► E5（并行两周）
                              └──► B1 ──► B2 ──► B3 ──► B4   （浏览器灰度）
```

**硬前置（三条都不是本文能解决的）**：P1 出口标准 8/8（现 3.5/8）、ESP 接通、官网收款闭环。
没有第三条，两个客户端都只能展示配额、不能卖东西。

---

## 7 · 移动端（登记，不排期）

Android：`androidx.webkit.ProxyController.setProxyOverride()` 可让 App 内 WebView 走本机 sing-box，**不需要 VpnService 授权弹窗**；分发只能官网 APK。
iOS：iOS 17+ 的 `WKWebsiteDataStore.proxyConfigurations` 可给 WKWebView 设代理，不需要 NetworkExtension；但**中国区 App Store 上架不可能**，且要自行封堵 Mysk 报告的三类泄漏。
两者都复用同一套后端，工期各 4–8 / 6–10 周。**本文只登记，不排期。**

---

## 8 · 度量

| 指标 | 目标 | 怎么测 |
|---|---|---|
| 扩展：装上到首次连接成功的中位耗时 | ≤ 3 分钟 | 扩展本地埋点，随诊断上报 |
| 扩展：首次连接成功率 | ≥ 90% | 同上（分母是完成登录的人） |
| 商店：安装 → 注册转化 | **基线待测**，无设定值 | 商店后台安装数 vs `acq_channel = extension` 注册数 |
| 「全部端点不可达」出现率 | < 1% 会话 | 诊断上报 |
| 浏览器：崩溃率 | < 0.5% 会话 | Electron crashReporter，自建 sink |
| 本地累计与节点计量偏差 | < 15% | §3.6 |

---

## 9 · 代价

> ⚠️ 这一选择的代价，必须留在记录里而不是被措辞掩盖：
>
> 1. **本文把 [go-to-market-plan §4.2](go-to-market-plan.md)「浏览器不做」的三条理由暂时搁置，但没有反驳它们。** 三条都还在：Electron 每 8 周跟版是永久性人力占用；「自带节点的浏览器」在中国法域里是最直接的物证（海外销售降低了但没有消除这一点，因为**使用地仍在中国**）；签名与误报是持续现金与工时成本。**做它 = 用一个 1–2 人团队每年约 6–10 人周的固定维护，换 §4.1 那两件事。** 值不值取决于「鸡生蛋」那条在真实用户里有多常见 —— 而那个数字现在是零。
> 2. **扩展引入第二条传输，运维面翻倍。** HTTPS 入站的封锁面比 REALITY 大且无 padding；被封时扩展用户整体失联而浏览器用户不受影响。[ADR 0014](../05-adr/0014-slo-and-oncall.md) 的可用率 SLO 必须按传输拆成两条，告警、值班与状态页都要跟着拆。
> 3. **默认走代理（§3.4）是主动选择的成本上升。** 它把「第一次点开还是白屏」换成流量账单变高，量化不了幅度（无用量数据）。配额条实时可见是对冲，不是抵消。
> 4. **PAC 末位不放 `DIRECT` = 故意让失败更响。** 代价是所有端点抖动时用户直接断网，而不是降级到直连凑合用。这是刻意的：静默直连意味着用户以为自己被保护着而实际没有，那比断网严重。
> 5. **不申请 `<all_urls>` = 扩展永远做不到 per-tab 归属。** 这正是浏览器 §4.1 第 ② 条的由来 —— 一个我们主动制造出来的差异化。若将来为了做 per-tab 而去申请该权限，商店审核难度与用户信任成本会同时上升。
> 6. **两个客户端都只覆盖浏览器流量。** 桌面 App（Cursor、Slack 客户端、终端里的 `pip`）一律不走。目标用户里的开发者会在第一天发现这一点。这不是缺陷是范围，但必须写进商品页，否则就是虚假宣传。
> 7. **Windows 签名依赖一个中国主体可能过不了的身份验证**（§4.5，待核实）。若过不了，Windows 版要么无签名发布（SmartScreen 拦截，转化率影响未知），要么找一个可签的主体，而后者又回到 [go-to-market-plan](go-to-market-plan.md) §1 第 0 条的身份问题。

---

## 10 · 这次没有解决的

- [ ] 🔴 **HTTPS 入站的每用户计量能否进 UniProxy 上报路径 —— 未核实。** 这是 E0 的全部内容，也是整个扩展方案的第一个可能致命点：不能计量就不能扣配额，扩展流量成为无界泄漏。
- [ ] **HTTPS 代理在大陆的存活期 —— 未实测**（E5）。没有它，扩展的 SLO 无法承诺。
- [ ] **`probe_resistance` 的回落站点用哪个域名、放什么内容 —— 未定。** naiveproxy issue #97 记录过参考配置本身可被探测。
- [ ] **凭据派生方案未做安全评审**：`HMAC(token, node_id)` 前 16 字节是提案，轮换与吊销的时序（订阅重置到入站生效之间的窗口）没算。
- [ ] **Electron 版本与 Chromium 安全更新的滞后上界未定** —— 用户拿它当浏览器用，滞后多久算不可接受，需要一条可判定的规则。
- [ ] **浏览器的崩溃与诊断上报 sink 未选型**，且它必须能从中国境内送达（否则收不到最需要的那批报告）。
- [ ] **per-tab 字节归属在 Electron 里的具体实现路径未验证**（`webRequest` + `webContents` 能否稳定拿到，需要 spike）。
- [ ] **商店素材、隐私政策、权限说明的英文文案未写**，且隐私政策必须与「不注入 content script、不采集浏览数据」的实现逐条对上。
- [ ] **`/client/proxy-config` 的契约未写**，`openapi.yaml` 需要一次修订与冻结流程。
- [ ] **移动端未排期**（§7）。

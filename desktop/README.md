# desktop/ —— babel.plus 浏览器：Electron 外壳 + 随包 sing-box，直接吃会员中心的订阅；能跑、51 个用例、生成的配置由真二进制校验过，**但还打不出可分发的包**

> 日期：2026-09-04 · 性质：**机制说明** · 状态：**执行中**（2026-09-04）——
> 本机可 `pnpm start` 起来并连上；**未签名、未公证、未在 Windows 上跑过**，所以还不能分发。
> 事实基线：`api/internal/subgen/singbox.go`（订阅产出形状，2026-09-02 读）；sing-box **v1.14.0**（`scripts/fetch-core.mjs` 钉版本与校验和）；Electron 44.2.0
> 关联：[client-products-spec §4](../docs/03-product/client-products-spec.md)（产品形态 B1–B4）、
> [roadmap §5.2 2.E](../docs/00-overview/roadmap.md)（排期与门槛）、
> [web/extension/](../web/extension/README.md)（另一个客户端，走的是另一条传输）
> 读者：接手浏览器的人。**先读 §1 的那一条结构性差别，它解释了为什么这个东西不需要等 E0。**

---

## 1 · 它与扩展的结构性差别：不需要新的服务端端点

| | 扩展 | **浏览器（本目录）** |
|---|---|---|
| 传输 | HTTPS 代理入站（**还不存在**，roadmap B66 / E0） | **VLESS-REALITY / Hysteria2 —— 现有节点** |
| 服务端改动 | 需要 `getUserProxyConfig`（现在是 501） | **零** |
| 拿配置的方式 | 新端点 | 会员中心已有的订阅：`GET /api/v1/user/subscription` → `urls.singbox` |
| 卡在什么上 | E0 计量验证（真机） | 签名证书与真机回归（下方 §6） |

`chrome.proxy` 说不了 REALITY（spec §1 边界 ①），所以扩展必须另起一条 HTTPS 入站；
而 Electron 可以把 `session.setProxy` 指向本机的 sing-box（边界 ③）。**这就是浏览器能先做的原因。**

订阅产出**没有 `inbounds` 也没有 `route.rules`**（roadmap B45 登记的欠缺）。对浏览器反而正好：
B45 的注释说「要么真机验证 tun，要么让客户端自带模板」—— 我们走后者，而且要的不是 tun
（接管整机流量是 spec §4.4 明确不做的事），是一个**只监听 `127.0.0.1` 的 mixed 入站**。

## 2 · 目录

```
src/main/       routing.ts（一张表两处用）· config.ts（组装完整配置）· subscription.ts（解析订阅）
                core.ts（sing-box 监护：check → spawn → 等端口 → 退避重启）· controller.ts（状态机）
                api.ts（会员中心客户端，契约类型从 web/shared/api/schema.d.ts type-only 引用）
                store.ts（token 与偏好，写入串行化）· tabs.ts（WebContentsView + per-tab 归属与字节）
                ports.ts · quota.ts · index.ts（Electron 接线，薄）
src/preload/    窄接口 window.bp（渲染层拿不到 ipcRenderer）
src/renderer/   chrome 界面（无框架，经典脚本）：标签条 · 地址栏 · 地球胶囊 · 被屏蔽提示条 · 首次运行
scripts/        fetch-core.mjs（取内核，钉校验和）· build.mjs（esbuild ×3）
vendor/         随包内核，**不入库**
```

## 3 · 怎么跑

```bash
cd desktop && pnpm install && pnpm core && pnpm start
```

`pnpm core` 取 sing-box 到 `vendor/<platform>-<arch>/`（钉版本 + 校验和）。
构建期变量都可为空（空则界面显示未配置，不编域名）：`BP_API_BASE_URLS`、`BP_WEB_URL`、`BP_HELP_URL`、`BP_NEW_TAB_URL`。

## 4 · 落在代码里的产品规则（每条都有用例）

- **一张表两处用**（`routing.ts`）：同一份规则既生成 sing-box 的 `route.rules`，又回答「这个标签页走代理还是直连」。两处各写一遍必然漂移，而漂移的现象是「角标说直连、实际走了代理」——没人查得动。`routing.test.ts` 逐个主机对拍。
- **配置由随包二进制自己 `check`**，check 不过就不启动、更不设代理。schema 是上游的自由，我们不假装知道每个版本长什么样。
- **内核崩了不撤代理**（`degraded` 态）：撤掉就是静默直连，用户会以为自己被保护着。界面此时必须明说「没有任何东西改走直连」。
- **只监听回环**：入站绑 `0.0.0.0` 会让同一个 WiFi 里的人白嫖配额，而酒店 / 咖啡馆 WiFi 正是本产品的主场景。
- **配额用尽 / 到期 → 主动断开**（与扩展同一条纪律）。
- **连接的判据是端口真的可连**，不是「setProxy 调过了」。
- **下载功能关闭**（v1，spec §4.4）：一个代理浏览器不该顺手变成下载器，`will-download` 直接取消并提示。
- **外链只放行我们自己的两个域名**：把任意 URL 交给系统浏览器，等于把用户从被保护的窗口里推出去而他不会注意到。

## 5 · 验证到什么程度（2026-09-04 实跑）

| 层 | 手段 | 结果 |
|---|---|---|
| 纯逻辑 | `pnpm test`，51 个用例 | ✅ 全绿 |
| **配置 schema** | `config.singbox.test.ts` 拿 **vendor 里的真 sing-box v1.14.0** 对生成的配置跑 `check`，并另跑一条「故意写坏必须不过」 | ✅ 通过（这是本目录最值钱的一条） |
| Electron 外壳 | `BP_SMOKE=1 pnpm start`：窗口加载后到渲染层里读真实节点再退出 | ✅ `{"bridge":true,"onboarding":true,"signInButton":true,"pill":"not signed in","styled":"96px"}` —— preload 桥、渲染、CSS 都生效 |
| **端到端连上真节点** | 需要一个真账号（登录 → 订阅 → 起内核 → 出口 IP） | 🔴 **没做**：本机没有可用账号凭据 |
| Windows / Intel Mac | — | 🔴 **一次都没跑过** |

## 6 · 代价与还欠着的（分发之前必须解决）

> 🔴 **这一节是「为什么现在还不能给用户」的完整清单，不要只读第一条。**

1. **签名与公证都没有。** macOS 需要 Developer ID + 公证（$99/年）；Windows 需要 Azure Artifact Signing（EV 证书 2026-05 起已不再绕过 SmartScreen）。两者都要一个法律实体 —— 与 App Store 的 D-U-N-S 是同一件事（go-to-market §1 第 0 条）。`electron-builder.yml` 已写好，缺的是证书。
2. **随包内核是官方 release 二进制，校验和是 TOFU。** sing-box 的 release **没有发布 checksums 文件**（2026-09-04 实查），所以 `fetch-core.mjs` 里的三个 sha256 是本仓下载后自己算的：能挡住以后被换掉，挡不住第一次就被换过。spec §4.5 要求**自编译**，那既是供应链也是杀软误报（Xray-core 被 Defender 标 Wacatac 的先例）。**分发前必须改。**
3. **token 落盘**（`store.ts`）。不落盘就没有「开机自动连接」，而落地当晚打开笔记本就要能用是第一场景。代价：拿到这台机器的人可以拿走会话。只做了 0600 与「只存会话不存密码」。
4. **per-tab 字节数占用调试器**。这是扩展做不到、浏览器能做的那件事（CDP `Network.loadingFinished`），代价是用户自己开 DevTools 时我们要让出，此后该标签页的字节数停在原地。
5. **没有端到端连上真节点**（§5）。在那之前，「能连」这三个字只有单元测试与 schema 校验背书。
6. **只有英文界面**。扩展有中英两套词典，这里只有英文 —— 海外优先，中文待补。
7. **没有自动更新**。spec §4.5 要求 `electron-updater` + 双更新源（都在域名池里），本轮未做：更新源被封 = 用户永远停在旧版，是这个产品最隐蔽的失效模式。
8. **每 8 周跟 Electron 大版本**是永久性人力占用（spec §9 代价 1），排期不消除它。

## 7 · 这次没有解决的

- [ ] 🔴 端到端实测：真账号 → 连上 → 出口 IP 正确 → 关掉内核看是否**没有**静默直连
- [ ] 🔴 签名 / 公证 / 自编译内核（B3 的全部内容）
- [ ] `electron-updater` 与双更新源、下载页（< 20 KB 无 JS，进域名池）
- [ ] Windows 与 Intel Mac 上跑一次
- [ ] 中文界面
- [ ] 崩溃与诊断上报 sink（且必须能从中国境内送达）
- [ ] 书签 / 历史 / 下载 —— v1 都不做，但「不做」要写进产品页，否则是虚假宣传

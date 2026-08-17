# babel.plus 术语表：一份索引，不是一份事实来源

> 日期：2026-08-16 · 性质：**机制说明** · 状态：**草稿**（v1，2026-08-16）
> 事实基线：`docs/` 全库 34 份 `.md`、17,446 行的高频专有名词扫描；
> 每条的权威出处写在最后一列，本表不新增任何裁决
> 关联：[product-brief.md](product-brief.md)、[docs/README.md](../README.md) §5 写作风格
> 读者：任何第一次读到某个词、想知道「这词在**我们这里**是什么意思」的人。

---

## 1 · 这份表怎么用（三条硬规矩）

**结论：本表的每一行都是对某份权威文档的有损压缩。最后一列不是装饰，是唯一正确的用法。**

| # | 规矩 | 为什么 |
|---|---|---|
| 1 | **冲突时以最后一列指向的文档为准，不以本表为准。** 表名与列名一律以 [data-model.md](../02-architecture/data-model.md) 为准；节点契约以 [api-contract.md](../02-architecture/api-contract.md) 为准 | 本表的「一句话解释」删掉了原文的限定条件与证据等级。拿它当结论用，会得到一个精度更低的版本 |
| 2 | **已裁定不用的术语仍然留在表里，并注明为什么不用** | [docs/README.md](../README.md) §4：「一条裁决被推翻时，它的理由不会自动消失。」删掉 Brutal 这一行，半年后必然有人重新提议用 Brutal |
| 3 | **本表内的版本号与价格是 2026-08-16 快照。** 凡涉及被钉死的版本（Xray v26.3.27、sing-box v1.13.18），以 [node-provisioning.md](../04-ops/node-provisioning.md) 为准 | 版本号是本表腐烂最快的部分，而 Xray 那一条是硬约束不是参考值 |

事实层级标记沿用全仓约定：**待核实** = 有来源但来源不够硬（社区单一来源 / 二手）；
**需实测** = 我们自己没测过。未标注的条目均可在最后一列的文档里找到已核实的出处。

---

## 2 · 协议与传输

| 术语 | 英文 / 全称 | 一句话解释（对本项目意味着什么） | 在本项目中的位置 |
|---|---|---|---|
| **VLESS** | V2Ray Less | 无内建加密的轻量代理协议，加密全交给外层 TLS/REALITY。我们所有 TCP 主力节点都是它——选它不是因为快，是因为它**没有** VMess 那层冗余的自有加密与认证头（外层已有 TLS） | [system-design §3.1](../02-architecture/system-design.md)、[protocol-and-infra §1.2](../01-research/protocol-and-infra.md) |
| **XTLS-Vision** | `flow: xtls-rprx-vision` | VLESS 的一种 flow：对内层 TLS 数据直通并加入内层握手随机填充，削弱 TLS-in-TLS 长度特征。我们默认下发。⚠️ Xray 的 `xtls-rprx-vision` **接管** UDP:443，而 mihomo 的 `xtls-*` 行为等价于 Xray 的 `xtls-*-udp443`（**不接管**） | [protocol-and-infra §1.2 / §5.4.3](../01-research/protocol-and-infra.md) |
| **REALITY** | — | 借用他人网站 TLS 握手的传输层伪装：服务端不持自己的证书，客户端认证失败就把连接原样代理到真实目标站。**我们的默认主力协议**，走 TCP:443 与海量正常 HTTPS 同形。选它是因为**下限稳**（实测单流仅 269 KB/s，是 Hysteria2 的 1/6），不是因为快 | [system-design §3.1](../02-architecture/system-design.md)、[ADR 0004](../05-adr/0004-transport-hardening.md) |
| **target / dest** | REALITY 回落目标 | REALITY 伪装成的那个真实站点。选择标准：支持 TLS 1.3 + HTTP/2、无跳转、境外、非自家域名、**且在中国可正常访问**（否则伪装本身就露馅）。Xray 新版把 `dest` 改名为 `target`，旧名仍作**静默别名**被接受——写错不报错，只是行为不符预期 | [protocol-and-infra §5.3 / §5.4.6](../01-research/protocol-and-infra.md) |
| **REALITY 密钥三件套** | PrivateKey / Password (PublicKey) / Hash32 | `xray x25519` 输出三行：`PrivateKey:` 进面板，`Password (PublicKey):` 才是给客户端的，`Hash32:` 是 VLESS Encryption 用的**与 REALITY 无关**。`publicKey`→`password` 是安全性改名不是美学改名——持有该字段即可探测 REALITY 服务器，所以它是**客户端持有的秘密** | [node-provisioning](../04-ops/node-provisioning.md)、[protocol-and-infra §5.3](../01-research/protocol-and-infra.md) |
| **Hysteria2 / HY2** | — | 基于 QUIC 的 UDP 代理，我们的**加速通路**（UDP:443）。实测单流 ~1700 KB/s = SS-2022 的 4.6×、REALITY 的 6.3×，但方差极大——UDP QoS 降级逐运营商逐时段波动。所以它是加速不是默认 | [system-design §3.1](../02-architecture/system-design.md)、[ADR 0004 §3.2](../05-adr/0004-transport-hardening.md) |
| **Brutal** | TCP-Brutal / Hysteria Brutal CC | Hysteria 自带的固定速率拥塞控制，**丢包时反而提速**。⛔ **我们不用**：FOCI 2025 对 10,080 条流用两级阈值分类器，以 **100% 准确率**区分 loss-based 与 non-loss-based CCA。放弃 55% 吞吐（1700 → 1094 KB/s）换不携带一个已被证明 100% 可分的行为特征 | [ADR 0004 §3.1](../05-adr/0004-transport-hardening.md) |
| **BBR** | Bottleneck Bandwidth and RTT | 标准拥塞控制，丢包时向下收敛。我们的 Hysteria2 默认用它。**具体落点**：面板节点记录的 `up_mbps` / `down_mbps` 必须留空或 0——HY2 只在带宽被显式指定时才启用 Brutal，留空即回落 BBR | [ADR 0004 §1](../05-adr/0004-transport-hardening.md)、[node-provisioning](../04-ops/node-provisioning.md) |
| **salamander** | HY2 obfs | HY2 的混淆层，把每个包打乱成看不出模式的随机字节，使流量**不产生可解析的 QUIC Initial**——因此不落入 USENIX Sec'25 的 QUIC SNI 审查机制。钉 sing-box 1.13 稳定版时只能下发 `salamander`（`gecko` 是 1.14 开发线） | [protocol-and-infra §1.2 / §5.4.2](../01-research/protocol-and-infra.md) |
| **端口跳跃** | port hopping | HY2 在一段 UDP 端口范围内轮换端口。⛔ **我们不开**：社区实测（`apernet/hysteria` #1380）无帮助，开一万个 UDP 端口是净增攻击面换零收益 | [ADR 0004 §3.2](../05-adr/0004-transport-hardening.md)、[node-provisioning](../04-ops/node-provisioning.md) |
| **TUIC v5** | — | QUIC 代理协议，0-RTT Full Cone UDP。⛔ **已裁定排除**：走标准 QUIC + 标准 TLS，SNI 可被 GFW 从 QUIC Initial 解出，且无 masquerade；上游 `EAimTY/tuic` 2025-05-15 后停滞。相对 Hysteria2 在抗审查上全面处于劣势 | [protocol-and-infra §1.2](../01-research/protocol-and-infra.md)、[system-design §3.1](../02-architecture/system-design.md) |
| **ShadowTLS v3** | — | 握手阶段把流量转给一个**真实的** TLS 服务器，握手结束后才接到隐藏后端（通常是 SS-2022）。与 REALITY 同生态位。**在已有 REALITY 的前提下边际价值有限**，列为备选而非并行主力 | [protocol-and-infra §1.2](../01-research/protocol-and-infra.md) |
| **Shadowsocks-2022** | SIP022 / AEAD-2022 | 用 BLAKE3 派生子密钥的新一代 SS，要求定长 PSK、带抗重放。我们的**兜底通路**（高位端口，实测 370 KB/s）。⚠️ 不可作主力：线上流量仍是**高熵随机字节流**，正中 IMC'20 的「按首包长度与熵值筛选」 | [system-design §3.1](../02-architecture/system-design.md)、[protocol-and-infra §1.2](../01-research/protocol-and-infra.md) |
| **AnyTLS** | — | 专门针对 TLS-in-TLS 论文设计填充方案 + 会话池的 TLS 隧道。**观察名单，不是首发**：比 REALITY 年轻得多、实网对抗数据不足、且需要真实域名 + 真实证书（REALITY 可以白嫖别人的）。注意 mihomo 明确声明不支持且永不支持 AnyTLS + REALITY 组合 | [protocol-and-infra §1.2](../01-research/protocol-and-infra.md)、[system-design §3.1](../02-architecture/system-design.md) |
| **WireGuard / WARP** | — | UDP-only 的现代 VPN 协议，握手初始包定长、消息类型字节偏移固定，是教科书级易指纹协议——设计目标里**完全没有抗审查**。⛔ 中国侧第一跳绝不用。它有价值的位置是**出口侧**（GCE → WARP → 互联网）拿干净出口 IP，属可选增值不是核心链路 | [protocol-and-infra §1.2](../01-research/protocol-and-infra.md) |
| **VMess** | — | V2Ray 时代的原生协议。⛔ **已裁定排除**：比 VLESS 每连接多一层冗余的自有加密与认证头（外层已有 TLS），且 WS+TLS 组合的 TLS-in-TLS 特征完全暴露。同样走 CDN 时 `VLESS+WS+TLS` 严格优于它 | [protocol-and-infra §1.2](../01-research/protocol-and-infra.md) |
| **Trojan-Go** | — | ⛔ **已裁定排除**：`p4gefau1t/trojan-go` 最后一个 release 是 **v0.10.6（2021-09-14）**，五年无发布 = 任何新检测手法都不会有对应缓解。⚠️ 注意区分实现与协议：**Trojan 协议**本身在 Xray-core / sing-box / mihomo 中均有活跃实现，只是相对 VLESS 无优势 | [protocol-and-infra §1.2](../01-research/protocol-and-infra.md) |
| **XHTTP** | — | Xray 的 CDN 前置传输，工作在普通 HTTP 请求/响应之上，不需要 CDN 支持 WS 或 gRPC，且上下行可分离。我们的 **❹ 应急通路**（VLESS + XHTTP over CF CDN），**默认关闭**，仅节点 IP 被封且尚未完成 IP 轮换时下发启用，且必须落在隔离的一次性 CF 账号下 | [system-design §3.1](../02-architecture/system-design.md)、[ADR 0001](../05-adr/0001-cloudflare-tos-risk.md) |
| **mux / 多路复用** | multiplexing | 把多条应用流塞进一条代理连接。**TCP 路径启用、UDP 路径不启用**：USENIX Sec'24 证明每条代理连接哪怕只承载 2 条应用流，TLS-in-TLS 检测 TPR 就下降 **超过 70%**；代价是队头阻塞损害吞吐（Proxy_Skill 已实测）。⚠️ mux 与 XTLS-Vision 能否共存**未核实** | [ADR 0004 §3.3](../05-adr/0004-transport-hardening.md)、[node-provisioning](../04-ops/node-provisioning.md) |
| **ECH** | Encrypted Client Hello | 把 SNI 加密进 ClientHello。FOCI 2025 原文：GFW **不封 ECH 本身**，但封了取 ECHConfig 所需的加密 DNS。破解口是所有 Cloudflare 免费计划 zone 共用同一份 ECH config，可从未被污染的域名取来复用。列为应急通路的加固手段，**待验证** | [ADR 0004 §3.8](../05-adr/0004-transport-hardening.md) |
| **SNI** | Server Name Indication | TLS ClientHello 里的**明文**域名字段，GFW 最主要的封锁抓手。我们的具体用法：HY2 订阅里节点地址填 IP、`sni` 填证书域名，客户端不做 DNS 解析——**域名只存在于证书里** | [node-provisioning](../04-ops/node-provisioning.md)、[ADR 0003 §3.2](../05-adr/0003-web-hosting-and-reachability.md) |
| **ALPN** | Application-Layer Protocol Negotiation | TLS 握手中协商上层协议（`h2` / `h3` / `http/1.1`）。HY2 订阅必须写 `alpn: [h3]`；gRPC over CDN 也要求源站通告 ALPN | [protocol-and-infra §5.4.2](../01-research/protocol-and-infra.md) |
| **QUIC** | — | UDP 之上的传输协议，HTTP/3 的底座、Hysteria2 与 TUIC 的基础。对我们是双刃：原生多路复用 + 抗丢包，但自 **2024-04-07** 起 GFW 大规模解密 QUIC Initial 读 SNI，命中后丢弃该**三元组**（源 IP、目的 IP、目的端口）的全部后续 UDP 包并**持续 180 秒**；换源端口无效，换目的端口有效 | [ADR 0004 §3.2](../05-adr/0004-transport-hardening.md)、[runbook §2](../04-ops/runbook-node-health.md) |

---

## 3 · 客户端与内核

| 术语 | 英文 / 全称 | 一句话解释（对本项目意味着什么） | 在本项目中的位置 |
|---|---|---|---|
| **Clash** | — | ⛔ **这个词不能再用。** 原版 `Dreamacro/clash` 仓库已返回 HTTP 404、仓库不存在。**所有对外文档不得再出现 "Clash" 这一称呼**，统一写 **mihomo / Clash.Meta 内核** | [protocol-and-infra §1.4](../01-research/protocol-and-infra.md) |
| **mihomo** | 原 Clash.Meta | 原版 Clash 的**唯一继承者**（v1.19.30，2026-08-16 当天仍有提交，是全调研中更新最勤的项目）。它是 Clash Verge Rev 等主流客户端的内核——所以我们的**服务端 Xray 版本直接决定了一大批用户能否连上** | [protocol-and-infra §1.4 / §5.3.1](../01-research/protocol-and-infra.md) |
| **Clash Verge Rev** | — | Windows / macOS 首推客户端（GPL-3.0，Tauri，内置 mihomo 内核）。两个必须知道的行为：它匹配**任何以 `subscription-userinfo` 结尾**的响应头；且校验 YAML 必须含 `proxies` 或 `proxy-providers`，否则**拒绝整份配置** | [tutorials-spec §3](../03-product/tutorials-spec.md)、[protocol-and-infra §5.1](../01-research/protocol-and-infra.md) |
| **sing-box** | — | 全平台核心（v1.13.18），移动端发行名 SFI / SFA / SFM / SFT。我们订阅的第二种输出格式。⚠️ 它的 Remote Profile 是**一份完整配置**而不是节点列表，且内核里**没有任何流量配额响应头的定义**——流量额度展示无法通过它传达，必须在面板侧解决 | [protocol-and-infra §5.1](../01-research/protocol-and-infra.md)、[api-contract §5](../02-architecture/api-contract.md) |
| **Xray-core** | — | REALITY 的原生实现，也是 v2node **vendor 的依赖**（我们不单独装 Xray）。🔴 **必须钉死 v26.3.27**（当前最新非预发布版）——v26.7.11+ 会让 mihomo 系客户端连不上 REALITY 且 mihomo 明确不修；且 v26.4.x–v26.7.28 全是 **prerelease**，「取最新 release」的自动化会踩坑 | [protocol-and-infra §5.3.1](../01-research/protocol-and-infra.md)、[node-provisioning](../04-ops/node-provisioning.md) |
| **v2rayN** | — | Windows / Linux GUI（7.24.4，113,954★，约每周发版），Windows 备选。⚠️ `vmess://` 的 base64-JSON 形式就是**它的约定**，没有任何官方规范 | [tutorials-spec §3](../03-product/tutorials-spec.md)、[protocol-and-infra §5.2](../01-research/protocol-and-infra.md) |
| **v2rayNG** | — | Android 备选客户端。订阅按 base64 格式下发 | [tutorials-spec §3](../03-product/tutorials-spec.md)、[api-contract §5](../02-architecture/api-contract.md) |
| **Shadowrocket** | — | iOS 付费客户端。⚠️ **无任何可达的官方文档**，关于它支持哪些参数的一切说法都应视为未经证实、必须真机实测。且需外区 Apple ID——[user-journey](../03-product/user-journey.md) 因此主张改推 Karing，**两文分歧未裁决** | [tutorials-spec §3](../03-product/tutorials-spec.md)、[user-journey §4.3](../03-product/user-journey.md) |
| **Stash** | — | iOS 付费客户端，走 Clash 规则生态（mihomo 系），列为 iOS 备选 | [tutorials-spec §3](../03-product/tutorials-spec.md) |
| **Karing** | — | 基于 `KaringX/sing-box` **fork** 的免费全平台客户端。⚠️ **不能假定它与上游 sing-box 协议对等**（README 不枚举协议），整行支持矩阵标 ⚠️ 需实测。它的产品价值是免费——在首连路径上省掉「注册外区 Apple ID → 充值 → 购买」整个子旅程 | [protocol-and-infra §1.4](../01-research/protocol-and-infra.md)、[user-journey §4.3](../03-product/user-journey.md) |
| **Hiddify** | Hiddify-app | 全平台 GUI（v4.1.1），Android 备选。README 只列 Vless / Vmess / Reality / TUIC / Hysteria / WireGuard / SSH，**未列** Trojan / AnyTLS / ShadowTLS | [protocol-and-infra §1.4](../01-research/protocol-and-infra.md) |
| **NekoBox** | NekoBox for Android | Android 备选，已停滞（1.4.2 / 2026-02-09）。🔴 **必须转达给用户的警告**：其 README 声明 Google Play 版本自 2024 年 5 月起**由第三方控制且不是开源版本**，不应引导用户从 Play 商店安装 | [protocol-and-infra §1.4](../01-research/protocol-and-infra.md) |
| **NekoRay / Throne** | — | NekoRay 桌面版**已归档死亡**（`archived: true`，末次 push 2024-12-12），继任者是 `throneproj/Throne`。**任何仍推荐 NekoRay 的文档都是过时的** | [protocol-and-infra §1.4](../01-research/protocol-and-infra.md) |
| **TUN 模式** | — | 客户端建虚拟网卡接管全系统流量。教程必须讲（Windows/macOS 需装系统服务），但 🔴 **开 TUN/fake-ip 后 `ping` / `dig` / `nslookup` / `curl --interface` 的结果全部不可信**——排障文档里这些命令**一次都不要出现**，探活必须走内核 API（mihomo delay 接口） | [tutorials-spec §4.4](../03-product/tutorials-spec.md)、[reference-repos §1.5](../01-research/reference-repos.md)、[node-provisioning](../04-ops/node-provisioning.md) |
| **fake-ip** | — | DNS 劫持模式：给域名返回一个假 IP，再由内核按域名分流。它是「DNS 泄漏」排障的核心概念，也是上一行那些命令不可信的直接成因。⚠️ 它的 DNS 劫持在**直连模式下依然可能生效** | [tutorials-spec §4.4](../03-product/tutorials-spec.md)、[node-provisioning](../04-ops/node-provisioning.md) |
| **分流规则** | routing rules | 决定哪些流量走代理、哪些直连。我们**第一阶段不在节点侧下发 `routes`**，分流全在客户端配置里。产品含义：国内直连 + 流媒体直连是**最划算的一条降成本手段**（出口流量是唯一真实变动成本） | [api-contract §4](../02-architecture/api-contract.md)、[pricing-and-plans §2.2](../03-product/pricing-and-plans.md) |
| **url-test / fallback** | 策略组类型 | `url-test` 按延迟自动选，`fallback` 按人工排序 + 失效自动跳过。🔴 **我们的默认组必须是 `fallback`**：实测各健康节点延迟同在 100–250 ms 噪声带内而吞吐差 4–5 倍，`url-test` 会**稳定选错** | [system-design §3.1](../02-architecture/system-design.md)、[api-contract §5](../02-architecture/api-contract.md) |

---

## 4 · 面板与节点端

| 术语 | 英文 / 全称 | 一句话解释（对本项目意味着什么） | 在本项目中的位置 |
|---|---|---|---|
| **v2board** | — | 上一代中文机场面板（Laravel）。**已停更三年**，但它定义的 `/api/v1/server/UniProxy/*` 契约与 `v2_` 前缀表结构是整个生态的事实标准——我们兼容的是它的孙辈契约，不是它的代码 | [panels-and-market §1](../01-research/panels-and-market.md) |
| **Xboard** | — | v2board 的重构 fork，保留 `v2_` 表结构与 UniProxy 契约。🔴 **我们不 fork 它，但照抄它的数据模型与节点契约**——这样节点端可直接用 v2node。照抄时必须加固三处（见 §8「三处加固」），且 MySQL→PostgreSQL 有陷阱（`utf8mb4_unicode_ci` 不区分大小写，直接照抄会让同一邮箱注册两次） | [panels-and-market §1](../01-research/panels-and-market.md)、[data-model.md](../02-architecture/data-model.md) |
| **SSPanel-UIM** | — | 另一支 PHP 面板（MIT，10,411★）。五个候选面板里**只有它具备真正的用户计费与工单**。我们从它借了一样东西：**把订阅 token 拆成独立表**，而不是像 v2board 那样挂在 `users.token` 上 | [panels-and-market §1 / §2.2](../01-research/panels-and-market.md) |
| **Marzban** | — | Python / FastAPI 面板。⚠️ `master` HEAD 停在 2025-01-09，`Marzban-node` 停在 2025-03-22，**维护风险高**。我们借了它的订阅吊销语义：比较 token 内嵌的签发时间与 `users.sub_revoked_at`，**不换标识符即可一键全撤** | [panels-and-market §1](../01-research/panels-and-market.md)、[data-model.md](../02-architecture/data-model.md) |
| **Remnawave** | — | 更现代的面板。节点鉴权最强（mTLS TLS 1.3 + 面板签发的非对称 JWT 双层）。我们借了它的**订阅拉取审计表**；倍率的定点整数基数 `1e9` 也抄自它 | [panels-and-market §1 / §2.2](../01-research/panels-and-market.md)、[api-contract §11](../02-architecture/api-contract.md) |
| **3x-ui** | — | Go / Gin 的面板（44,735★，非常活跃）。它的 **per-node bearer token（SHA-256 存储）+ scope 路由白名单**正是我们节点鉴权的原型。⚠️ 它没有独立节点 agent，所谓「node」就是另一台完整的 3x-ui 面板 | [panels-and-market §1 / §2.2](../01-research/panels-and-market.md) |
| **XrayR** | — | 上一代节点 agent。⛔ **已废弃**：仓库源码被清空、描述改为「项目已废弃」，最后 release v0.9.4（2024-07-21）。但它留下两笔遗产：ETag 缓存实现是我们 304 设计的参考；而它用 resty `SetQueryParams` 把 token 全局挂在 query string 上的做法，正是我们要改掉的病灶 | [panels-and-market §2.3](../01-research/panels-and-market.md)、[api-contract §3](../02-architecture/api-contract.md) |
| **V2bX** | — | XrayR 的继任者之一（MPL-2.0）。⛔ 仓库已 `archived`（2025-12-02） | [panels-and-market §1](../01-research/panels-and-market.md) |
| **soga** | — | 闭源二进制节点 agent，按 USDT 收费 + 授权码绑定域名。⛔ **明确排除**：把闭源商业组件放进关键路径是不可接受的供应链风险 | [panels-and-market §2.4](../01-research/panels-and-market.md) |
| **v2node** | `wyx2685/v2node` | ✅ **我们选定的节点端软件**（MPL-2.0，Go，「改版 xray-core」，260★，2026-07-13 仍活跃），XrayR/V2bX 的事实继任者。🔴 三件事必须起真实容器实测：① 是否发 `If-None-Match`（不发则整套 ETag 一行不生效）② 能否配 `Authorization` 头 ③ 收到 401/403 是否**清空本地用户列表**（若清空，一次密钥配置失误 = 全体瞬时掉线） | [system-design §3.2](../02-architecture/system-design.md)、[api-contract §3](../02-architecture/api-contract.md)、[ADR 0006 §11.4](../05-adr/0006-api-stack.md) |
| **UniProxy 契约** | `/api/v1/server/UniProxy/*` | 节点 ↔ 面板的五个端点：`config` / `user` / `push` / `alive` / `alivelist`。对我们是**冻结契约**——不改版、不改路径、且**禁止使用统一响应信封**（v2node 用 Go 结构体直接反序列化，包一层 `{"data":…}` 立刻不兼容）。三条冻结不变量：路径、凭据传输位置、响应顶层形状 | [api-contract §3 / §12](../02-architecture/api-contract.md)、[panels-and-market §2.3](../01-research/panels-and-market.md) |
| **订阅** | subscription | 一个 URL，客户端定期拉取得到节点列表。产品上它还有第二重身份：**唯一在邮箱收不到、TG 连不上、主站被封时仍能触达用户的通道**——到期/超额时返回「空节点列表 + 一个名字即公告的伪节点」就是在用这个性质 | [user-journey §9](../03-product/user-journey.md)、[api-contract §5](../02-architecture/api-contract.md) |
| **`subscription-userinfo`** | 订阅响应头 | `upload={u}; download={d}; total={x}; expire={ts}`，**分号 + 一个空格**分隔，值全为十进制整数；流量单位字节、`expire` 单位 Unix 秒。机场生态事实标准，**非任何官方规范**。⚠️ 订阅响应**不设 ETag**——304 会让客户端一直显示旧流量条，而流量条是用户判断剩余额度的唯一入口 | [api-contract §5](../02-architecture/api-contract.md)、[protocol-and-infra §5.1](../01-research/protocol-and-infra.md) |
| **subconverter** | — | 第三方订阅格式转换服务（`tindy2013/subconverter` 是事实标准后端）。⛔ **绝不依赖任何第三方实例**——用户的 UUID、HY2 密码、REALITY 公钥会**明文经过它**。我们自建订阅生成器，顺带绕开上游缺 `singbox` target 的问题 | [protocol-and-infra §5.1](../01-research/protocol-and-infra.md) |
| **ETag / 304** | — | 节点 60 秒轮询的省钱机制。必须**版本号驱动**（一次主键查 `config_rev` / `user_rev`）而不是哈希响应体，且 `/config` 与 `/user` 用**两个不同的 ETag**。经济学：10 节点 = 1,728,000 请求/月 = Cloud Run 免费额度的 **86%**，20 节点超出 173%——**ETag 不是优化，是让这笔账算得平的前提** | [ADR 0006 §11](../05-adr/0006-api-stack.md)、[api-contract §3](../02-architecture/api-contract.md) |
| **alivelist / 设备数** | — | 多节点共享设备数限制的**唯一**机制：节点先向面板拉全网计数，再本地决策是否放行。🔴 **口径是 IP 不是设备**——同一台手机切 Wi-Fi/蜂窝占两个名额，设备数 = 2 的档位下一人一机一电脑就已超限。⚠️ v2node 拿到计数后是拒新还是踢旧**待核实** | [api-contract §3](../02-architecture/api-contract.md)、[page-inventory](../03-product/page-inventory.md)、[data-model.md](../02-architecture/data-model.md) |

---

## 5 · 审查与对抗

| 术语 | 英文 / 全称 | 一句话解释（对本项目意味着什么） | 在本项目中的位置 |
|---|---|---|---|
| **GFW** | Great Firewall | 中国的国家级审查系统。对我们的操作性认知只有一条：**封锁状态是时变的、地域差异化的、且运营商差异化的**，任何「某协议在中国能用/不能用」的断言，其有效期通常以周计。所以选型强调**协议栈的可切换性**而不是押注单一协议 | [protocol-and-infra §1.1](../01-research/protocol-and-infra.md) |
| **主动探测** | active probing | GFW 对疑似代理服务器主动发起连接、观察响应。IMC'20 证实：按**首包长度与熵值**筛出疑似 Shadowsocks，再发**七种不同类型**的探测验证。这是「服务器必须对非法客户端表现得像一个真实服务」这条设计原则的来源——REALITY 回落、Trojan `fallback`、HY2 masquerade 都在解同一道题 | [protocol-and-infra §1.1](../01-research/protocol-and-infra.md) |
| **残留封锁** | residual censorship | 触发一次后，即使停止发送特征流量，该连接/三元组仍在一段时间内被持续封锁。QUIC 是 **180 秒**。**排障判据**：等 3 分钟自动恢复 = 残留封锁，不是节点故障 | [runbook §2](../04-ops/runbook-node-health.md)、[ADR 0004 §3.2](../05-adr/0004-transport-hardening.md) |
| **SNI 封锁** | — | 按 ClientHello 里的明文 SNI 触发 RST 注入。ADR 0003 §3.2 实测到教科书式的一例：**IP 没问题，被封的是名字**。这也是 `pages.dev` / `workers.dev` 呈现「大部分被封、少数完全正常」双峰分布的成因 | [ADR 0003 §3.2](../05-adr/0003-web-hosting-and-reachability.md) |
| **DNS 投毒** | DNS poisoning | 解析返回伪造 IP。ADR 0002 原始测量：Discord 在国内 DNS 返回伪造的 `210.56.51.192`，控制组返回真实 Cloudflare IP。⚠️ **但 Telegram 不是这个成因**——`api.telegram.org` 的 DNS 解析成功且一致，封在别处 | [ADR 0002 §2](../05-adr/0002-notification-channels.md) |
| **RST 注入** | — | 在途注入 TCP RST 强制断连。🔴 **必须与「单向丢包」区分开**：GTS 证书触发的封锁是「在服务端 Certificate 消息之后**单向丢包**，不是 RST 注入」——排障时极易被误判成网络抖动 | [ADR 0004 §3.4](../05-adr/0004-transport-hardening.md)、[deploy.md](../04-ops/deploy.md) |
| **TLS-in-TLS 指纹** | — | 隧道内再跑 HTTPS 必然产生嵌套握手，内层包长与时序会在外层加密流里留下可识别印记。🔴 **它打击的不是某个协议，而是「代理」这个行为本身**。在 FPR < 0.6% 下 23 种混淆代理配置的 TPR 全部 > 70%（`vless-over-TLS` 0.748）。这是我们启用 mux 的唯一理由 | [ADR 0004 §3.3](../05-adr/0004-transport-hardening.md)、[protocol-and-infra §1.1](../01-research/protocol-and-infra.md) |
| **全加密流量检测** | fully encrypted traffic detection | 按首包**高熵 + 长度**筛选没有明文特征的流量。裸 SS-2022 正中此路——这就是它只能作兜底不能作主力的全部原因 | [protocol-and-infra §1.2](../01-research/protocol-and-infra.md)、[system-design §3.1](../02-architecture/system-design.md) |
| **IP 级封锁** | — | 直接对出口 IP 丢包或 RST，最钝但最有效。**判定必须同时满足三条独立证据**：① 服务端进程 active、端口正常 bind ② 443 上零 established 且数小时零日志 ③ 从境外回打可完成 TCP 握手。缺一条就不能判 | [runbook §3](../04-ops/runbook-node-health.md) |
| **域名池** | — | 一组可互换的镜像域名，按「域名一定会被封」设计。我们有**三套独立域名池**（Web 面板 / API / 教程站），且**必须是独立主域名不能是同一域名的子域**——GFW 的封锁粒度常在主域名级 | [ADR 0003 §1](../05-adr/0003-web-hosting-and-reachability.md)、[system-design §4.1](../02-architecture/system-design.md)、[deploy.md §11](../04-ops/deploy.md) |
| **优选 IP** | — | 批量测速 Cloudflare IP 段、挑低延迟低丢包的固定使用。本质是**对抗 anycast 的选路决策**，典型的「能用但脆弱」——IP 池会变、测速结果会过期，需要持续自动化重测。对我们只在 ❹ 应急通路里才有意义 | [protocol-and-infra §4.3](../01-research/protocol-and-infra.md) |
| **机场** | — | 中文圈对订阅制代理服务的通称。⚠️ **我们不是机场**：product-brief §6 明确不做公开注册的 to-C 生意。但它的市场惯例（¥0.10–0.25/GB、倍率、设备数档位）仍是用户的心理锚点——定价必须知道自己贵在哪，而不是假装这个锚点不存在 | [product-brief §6](product-brief.md)、[panels-and-market §4.1](../01-research/panels-and-market.md) |
| **Turnstile** | Cloudflare Turnstile | 人机验证服务。⛔ **出局**：不在 Cloudflare China Network 的可用产品清单里，大陆不可用。reCAPTCHA 同样出局（google.com 大陆封锁），hCaptcha 的大陆可达性**无任何测量数据、需实测**。P1 的决定是**不上任何 captcha**，改用「邀请码 + 邮箱验证码 + 双维度速率限制」 | [page-inventory §5.3](../03-product/page-inventory.md)、[user-journey](../03-product/user-journey.md) |

---

## 6 · 网络与路由

> ⚠️ **本节的第一条实用结论**：GCP 上买不到任何一条中国精品线路，这是结构性的不是配置问题。
> 实查 RIPEstat BGP（2026-08-16），Google **AS15169** 的 **335 个观测邻居中没有任何一个中国大陆运营商 ASN**，
> 与 AS4809（CN2）也无邻接。因此下面这些线路名对我们的价值只有两个：读懂竞品文案、读懂 traceroute。

| 术语 | 英文 / 全称 | 一句话解释（对本项目意味着什么） | 在本项目中的位置 |
|---|---|---|---|
| **163 / ChinaNet** | AS4134 | 中国电信的**消费级**国际出口，带宽被严重超售。每晚约 **19:00–24:00 CST** 进入拥塞窗口，表现为**丢包率飙升与 RTT 抖动激增**，而不是平均延迟上升——这是我们晚高峰实测必须测丢包不能只测延迟的原因 | [protocol-and-infra §4.1](../01-research/protocol-and-infra.md) |
| **CN2 GT** | China Telecom Next Carrier Network · Global Transit | 电信精品线路的**较低**一档 | [protocol-and-infra §4.1](../01-research/protocol-and-infra.md) |
| **CN2 GIA** | Global Internet Access · **AS4809** | 电信精品线路的**最高**等级，独立的高优先级承载网，拥塞窗口内劣化远小于 163。⛔ **GCP 上买不到**（见本节开头） | [protocol-and-infra §4.1](../01-research/protocol-and-infra.md)、[ADR 0004 §3.6](../05-adr/0004-transport-hardening.md) |
| **AS4837** | 联通 169 | 中国联通的普通国际出口 | [protocol-and-infra §4.1](../01-research/protocol-and-infra.md) |
| **AS9929** | CUII | 中国联通的精品线路，质量优于 AS4837 | [protocol-and-infra §4.1](../01-research/protocol-and-infra.md) |
| **AS9808** | — | 中国移动的普通国际出口 | [protocol-and-infra §4.1](../01-research/protocol-and-infra.md) |
| **AS58453** | CMI · China Mobile International | 中国移动的国际精品线路 | [protocol-and-infra §4.1](../01-research/protocol-and-infra.md) |
| **CTGNet** | China Telecom Global（通称对应 **AS23764**，**待核实**） | 电信国际业务的新一代品牌，市场上常与 CN2 GIA 并列宣传。⚠️ **本项目文档从未调研过它**，此处按行业通称收录，AS 号与线路归属**待核实**。对我们无操作影响（同样买不到） | 无（本表首次收录） |
| **CMIN2** | China Mobile International Next-generation（**待核实**） | 移动国际的新一代精品网，市场上作为 AS58453 的升级线路宣传。⚠️ **本项目文档从未调研过它**，全部内容**待核实** | 无（本表首次收录） |
| **三网** | 电信 / 联通 / 移动 | 中国三大运营商。我们所有路由验收与实测都必须**三网各测**——[node-provisioning](../04-ops/node-provisioning.md) 的判据 J4 就是「三网中位 RTT 极差 > 60 ms → 警告」，因为一条对电信好的线路可能对移动绕美 | [node-provisioning](../04-ops/node-provisioning.md)、[evidence/README.md](../evidence/README.md) |
| **晚高峰** | — | 约 **19:00–24:00 北京时间**。所有吞吐与丢包实测**必须含一次晚高峰采样**，否则数据不可用于选型：非高峰的差异会被拥塞完全淹没 | [node-provisioning](../04-ops/node-provisioning.md)、[protocol-and-infra §4.1](../01-research/protocol-and-infra.md) |
| **BGP** | Border Gateway Protocol | 决定流量走哪条路的域间路由协议。对我们的实际含义只有一句：🔴 **入向路径由中国运营商的 BGP 决策，我们完全无法控制**——这正是中国移动「绕美」现象的成因。Premium 给运营商更多入口，但**强迫不了它选好的那个** | [ADR 0004 §3.7](../05-adr/0004-transport-hardening.md) |
| **anycast** | — | 同一个 IP 在多地宣告，由 BGP 就近选路。Premium Tier 支持全球 anycast IP，**Standard 不支持**（这排除了任何多区域 anycast 设计）。CF 的 anycast 从中国出发经常选路不理想——「优选 IP」实践就是由此而来 | [protocol-and-infra §3.5 / §4.3](../01-research/protocol-and-infra.md) |
| **Premium / Standard 网络层级** | Network Service Tiers | GCP 出网的两档。**Premium**：走 Google 骨干、在离目的地最近的 PoP 出网，到中国 **$0.23/GiB 且无免费额度**。**Standard**：在源区域附近就交给公网，**$0.11/GiB + 每区域每月前 200 GiB 免费**。🔴 **我们用 Premium，唯一决定性理由是 Standard 不支持 IPv6**——ADR 0004 自认这是全裁决里**论据最弱**的一条，且它直接推高定价下限 | [ADR 0004 §3.7](../05-adr/0004-transport-hardening.md)、[protocol-and-infra §3.5](../01-research/protocol-and-infra.md) |
| **PoP** | Point of Presence | 网络接入点。两个具体后果：① Cloudflare Free/Pro/Business 的 zone **在中国大陆没有任何 PoP**，中国用户的请求必然出境；② Premium Tier 的真实价值是**入向宣告范围更广**（给中国运营商更多落地选择），不是「更快」 | [protocol-and-infra §2.7](../01-research/protocol-and-infra.md)、[ADR 0004 §3.7](../05-adr/0004-transport-hardening.md) |
| **IP 段是一等变量** | — | 不是俗语，是裁决。同一 `asia-east2` 区域内 zone `-b` 绕道美国而 `-a`/`-c` 直连；`35.220.x` 对移动直连约 50 ms 而 `34.92.x` 绕东京约 110 ms（社区来源，**待核实**）。所以开机后必须**逐 IP 实测三网路由，不合格立即释放重开**，且换 IP 是常规操作不是异常处置 | [ADR 0004 §3.5](../05-adr/0004-transport-hardening.md)、[node-provisioning](../04-ops/node-provisioning.md) |
| **ICP 备案 / ICP 许可证** | — | 见 §9 成对术语。一句话结论：**两者我们都基本不可能取得**，因此 Cloudflare China Network 这条官方路径对我们关闭。但 **ICP 备案不影响邮件投递**——不要让这件事影响架构决策 | [admin-support-docs §6](../01-research/admin-support-docs.md)、[ADR 0003 §1](../05-adr/0003-web-hosting-and-reachability.md) |

---

## 7 · 商业与计费

| 术语 | 英文 / 全称 | 一句话解释（对本项目意味着什么） | 在本项目中的位置 |
|---|---|---|---|
| **倍率** | rate / multiplier | 实际用量 × 倍率 = 扣配额。市场惯例 0.1x–3x（也见过 20x）。⛔ **我们第一阶段不引入**——竞品 58 个节点全部 1x，倍率会让用户算不清账、徒增客诉。**数据层后果**：`servers` 不建 `rate` 列、`stat_user_server` 不按倍率分桶。将来引入需要 ADR + schema 重建 + 一条「生效日之前的历史按 1x 解释」的口径声明 | [product-brief §6](product-brief.md)、[data-model.md](../02-architecture/data-model.md)、[panels-and-market §4.1](../01-research/panels-and-market.md) |
| **流量重置** | `reset_traffic_method` | 周期性把已用流量清零。六种模式的枚举，默认 `monthly_on_order_day`（竞品实测「重置日 = 订单日」）。🔴 两条硬要求：**重置必须 bump `user_rev`**（超额用户重新可用 = 改变了节点可见用户集合）；**置零与推进 `next_reset_at` 必须在同一条 UPDATE 里**，拆成两条则 Scheduler 重试会把用户流量二次清零 | [data-model.md](../02-architecture/data-model.md)、[deploy.md §8.2](../04-ops/deploy.md) |
| **流量包** | data pack | 额外购买的一次性配额，**不改到期日、不改重置日**，只叠加配额。⚠️ **已知缺口**：当前 DDL 没有独立配额列，`transfer_enable` 单列在重置时会被套餐值整个覆盖，需拆成 `transfer_enable_plan` + `transfer_enable_pack`，但依赖一条尚未裁决的产品规则 | [data-model.md](../02-architecture/data-model.md)、[user-journey §10.1](../03-product/user-journey.md) |
| **不限时套餐** | onetime | `expired_at IS NULL`。可用性判定的那条 SQL 天然支撑它。⚠️ **订阅头 `expire` 该输出什么未裁决**：Xboard 输出空值，部分客户端可能渲染成 1970 已过期；提案是输出 `4102444800`（2100-01-01），**是提案不是裁决，需实测** | [data-model.md](../02-architecture/data-model.md)、[api-contract §5](../02-architecture/api-contract.md) |
| **升级折抵** | proration / `surplus_amount` | 中途换套餐时把旧套餐的剩余价值抵扣新单。**字段有（`surplus_amount` + `surplus_order_ids`）但算法未设计。** 已定的只有呈现口径：结算页必须显示「原套餐剩余价值 / 新套餐价 / 实付」三行，且「剩余价值」旁必须有可展开的一句话算法说明 | [data-model.md](../02-architecture/data-model.md)、[user-journey §10](../03-product/user-journey.md) |
| **订单状态机** | — | `pending → paying → paid → …`，另有 `underpaid` / `expired`。🔴 三条必须写进代码而不是文档的约束：① **回调不可信**，收到回调必须反向查单（先例：NewAPI 的易支付回调漏洞直接信任状态参数、完全跳过验签）② 幂等键是 `(provider, external_id)` 上的**唯一索引**，不是应用层 `SELECT ... IF NOT EXISTS` ③ 开通是**一个事务**：写配额 + 写到期 + 改状态 + bump `user_rev` | [api-contract §6](../02-architecture/api-contract.md)、[payments §4](../01-research/payments.md) |
| **underpaid** | — | 加密通道**特有**的订单状态：收到了钱但不够。头号成因是「提币手续费从转出额扣，导致实收少」。必须有**显式界面**显示「已收到 X，还差 Y」（带 `shortfall_usdt6`），不能笼统报「支付失败」 | [payments §4](../01-research/payments.md)、[api-contract §6](../02-architecture/api-contract.md)、[page-inventory](../03-product/page-inventory.md) |
| **双录记账** | 复式记账 / double-entry ledger | 每笔记账拆成借贷两条，硬约束 `∀ entry: SUM(lines.amount) = 0`。🔴 **余额（wallet）一旦引入就必须用它，否则一定对不平。** `ledger_lines.amount` 用**有符号整数最小货币单位**（人民币分 / 1e-6 USDT）。三张表 `ledger_accounts` / `ledger_entries` / `ledger_lines` 一律 append-only，用 `REVOKE UPDATE,DELETE,TRUNCATE` 强制 | [payments §4.12](../01-research/payments.md)、[data-model.md](../02-architecture/data-model.md) |
| **易支付 / EPay** | — | 国内第三方聚合支付的**事实接口标准**（MD5 签名，权威实现是 v2board 的 `EPay.php`）。我们的**备用通道且风险极高**：其接入协议 3.2.9 点名 VPN，FAQ 明写「风险极高的类型：…VPN…」且「情节严重者直接上报片区网警」。🔴 **不是灰色地带，是连灰产平台自己都写明拒收的类目** | [pricing-and-plans §4.0](../03-product/pricing-and-plans.md)、[payments §3.2](../01-research/payments.md) |
| **码商** | — | 行业通称：向聚合支付/四方平台提供个人收款码（支付宝/微信）跑量的中间人，是易支付类通道的实际资金入口。⚠️ **本项目文档从未调研过码商层**（payments.md 只调研到易支付平台层），此处**待核实**。既然易支付本身已定为「备用且风险极高」，再往下追的边际价值有限 | 无（本表首次收录），上游见 [payments §3](../01-research/payments.md) |
| **USDT TRC20 / ERC20 / BEP20** | — | **同一种稳定币在三条链上的三个版本，地址格式不同、互转即永久丢失**（用户旅程七个卡点之一）。默认预选 **TRC20**：用户从交易所提币时由交易所代付能量，那笔 $2.13–4.31 的归集成本不落在用户头上。TRC20 是 **6 位小数**，所以金额唯一性匹配的 `+0.0001 USDT` 递增正好等于 `+100 usdt6` | [payments §2](../01-research/payments.md)、[user-journey §5](../03-product/user-journey.md)、[api-contract §6](../02-architecture/api-contract.md) |
| **黑 U** | 污染 USDT | 来自电诈、洗钱链路的 USDT。收到会导致后续出金被冻结——所以**入账前的链上风险筛查不是可选项**，或者只收托管网关的钱 | [payments §2](../01-research/payments.md)、[pricing-and-plans §4.3](../03-product/pricing-and-plans.md) |
| **MoR** | Merchant of Record | 代收税的销售主体（Paddle / Polar / Creem / LemonSqueezy）。**Paddle 是唯一在公开政策里给 VPN/Proxy 留了 "Restricted Category + 强化尽调" 通道的**，值得申请但**不可放在关键路径**：过审 ≠ 安全，条款均保留单方面终止权（FastSpring 可扣留余额 180 天–1 年，Creem 可冻结调查 90 天） | [payments §1](../01-research/payments.md)、[pricing-and-plans §4](../03-product/pricing-and-plans.md) |
| **钱包余额** | — | 🔴 **仅可消费，不可提现。** 这既是资金合规底线，也是「过期订单的收款地址继续监听 ≥ 24 小时、到账入账为余额而不是直接开通」这个兜底能够成立的前提——不做这一条，用户第一次付款的钱就真的进黑洞 | [product-brief §6](product-brief.md)、[user-journey §5](../03-product/user-journey.md) |
| **邀请码** | — | 我们**唯一**的注册入口（不开放公开注册）。两条规则：用户码**恒为一次性核销**（多次可用等于开放注册）；生成资格挂在「有有效订阅」而非「有账号」上，否则邀请制会退化为链式开放注册 | [product-brief §4](product-brief.md)、[user-journey §2](../03-product/user-journey.md) |

---

## 8 · 本项目内部约定

| 术语 | 英文 / 全称 | 一句话解释（对本项目意味着什么） | 在本项目中的位置 |
|---|---|---|---|
| **`bp-` 前缀** | — | 所有新建 GCP 资源的强制前缀（`bp-node-*` / `bp-api` / `bp-web` / `bp-docs`）。理由是我们与 `vpn-us` / `vpn-jp` 和三个 Cloud Run 服务**共享项目 `oratis-491316`**，前缀是「不影响已部署服务」的第一道隔离。附加要求：所有 `bp-` 资源创建时必须打 label `app=babel-plus`，否则项目级 budget 会混进别人的支出 | [as-built-gcp §8](../02-architecture/as-built-gcp.md)、[ADR 0007](../05-adr/0007-node-migration.md)、[monitoring.md §9](../04-ops/monitoring.md) |
| **`bp-node` 标签** | network tag | 新节点唯一的网络标签，四条 `bp-*` 防火墙规则全绑它。🔴 两个优先级不等式必须同时成立：`bp-public-ssh-deny`(1000) < `default-allow-ssh`(65534)，且 `bp-iap-ssh-allow`(900) < `bp-public-ssh-deny`(1000)——搞反了节点就是一台登不进去的砖 | [ADR 0007 §6.1](../05-adr/0007-node-migration.md)、[node-provisioning](../04-ops/node-provisioning.md) |
| **控制面** | control plane | 面板、订阅 API、计费、节点上报。跑在 Cloud Run + 静态托管上。🔴 **控制面故障绝不能升级为数据面故障**——面板挂了、API 域名被封，用户都应该照常上网（节点用最后一次成功的配置继续服务） | [system-design §1 / §5.3](../02-architecture/system-design.md) |
| **数据面** | data plane | 中转流量本身，跑在 GCE 节点上，**不经 Cloudflare**（既是 ToS 合规也是性能：CDN 路径受单流 TCP 拥塞控制约束，实测只有 Hysteria2 的 1/5 左右） | [system-design §2](../02-architecture/system-design.md)、[ADR 0001](../05-adr/0001-cloudflare-tos-risk.md) |
| **恢复面** | recovery plane | 域名发现与失联广播——前两个面都挂了也要救得回来的那一层。载体是**邮件 + 节点名 + 静态域名池**。裁决：一切「服务不可用」类通知**不受用户通知开关控制**（到期/流量提醒可以关，生命线不能关） | [system-design §1](../02-architecture/system-design.md)、[ADR 0002](../05-adr/0002-notification-channels.md)、[user-journey §11](../03-product/user-journey.md) |
| **`user_rev` / `config_rev`** | — | `servers` 表上的两个 bigint，是 ETag 的唯一来源。**四条 bump 规则**：① 改变节点可见用户集合的写操作必须 bump ② 流量累加**不得** bump ③ `u+d` 跨过 `transfer_enable` 那一次**必须** bump（否则配额耗尽 = 免费无限上网）④ 到期不产生任何写操作，**必须由定时任务** bump。⚠️ 实现上必须是 `rev = rev + 1`，绝不能是 `rev = <计算值>` | [ADR 0006 §11.2](../05-adr/0006-api-stack.md)、[api-contract §3](../02-architecture/api-contract.md)、[data-model.md](../02-architecture/data-model.md) |
| **三处加固** | — | 照抄 Xboard 时**必须**改掉的三个病灶：① 全节点共用明文 `server_token` 走 query string → **每节点独立密钥 + scope 白名单 + DB 存哈希 + 可在线轮换吊销** ② 订阅 token 明文永久 `char(32)` → **独立 token 表 + 内嵌签发时间 + `sub_revoked_at` 一键全撤 + 每次拉取写审计** ③ 后台靠路径混淆无 2FA → **独立域名 + IP 白名单/IAP + 强制 TOTP** | [api-contract §3 / §7](../02-architecture/api-contract.md)、[data-model.md](../02-architecture/data-model.md)、[panels-and-market §2.2](../01-research/panels-and-market.md) |
| **危险操作 D1–D16** | — | 后台 16 条必须写审计日志（含改前/改后值）的操作。其中 **D6「手工标记订单已支付」是全系统最大的内部欺诈面**，必须有独立权限位且默认不授予——**这个权限位必须从第一天就存在，即使团队只有一个角色**。四层强制机制：确认串 / 必填 reason / TOTP step-up / 独立权限位，**且必须在 API 层不能只在前端** | [page-inventory §4](../03-product/page-inventory.md)、[api-contract §7](../02-architecture/api-contract.md) |
| **As-Built** | — | 文档状态之一：**只写已经存在的东西**，规划中的能力一律写进 ADR 或设计稿。`02-architecture/as-built-*.md` 专用。硬规矩来自 docs/README §2.2：始终区分「设计目标 / 当前实现 / 测试结果」三层 | [docs/README §2.2](../README.md)、[as-built-gcp.md](../02-architecture/as-built-gcp.md) |
| **代价尾节** | `## N · 代价` | 每份裁决与计划**强制**的倒数第二节，用 `> ⚠️` 引用块，量化带数字，并写明**什么情况下这个取舍不再成立**。docs/README 称它是「整套约定里最有价值的一条，不允许省略」 | [docs/README §4.1](../README.md) |
| **「这次没有解决的」** | `## N+1` | 最后一节，`- [ ]` 清单，每条说清楚**为什么不在本次范围内**。它与代价尾节成对出现，缺一即视为文档不合格 | [docs/README §4.1](../README.md) |
| **待核实 / 需实测** | — | 事实层级标记。**待核实** = 有来源但来源不够硬（社区单一来源 / 二手 / 旧年份博客）；**需实测** = 我们自己没测过。配套原则：**不确定就说不确定，编造一个数字比留空危害大得多** | [docs/README §5](../README.md) |
| **`evidence/`** | 证据平面 | 实测原始数据目录（`<topic>-YYYYMMDD/`），与正文分离：结论写正文，原始数据留这里。每个证据目录必须有 README 写清楚「**这些证据证明什么、不证明什么**」，且**失败样本要保留**不要只留成功的 | [evidence/README.md](../evidence/README.md) |
| **P0–P4** | — | 阶段划分：P0 调研与设计（当前）→ P1 内核可用（一个人能用）→ P2 产品闭环（一群人能用）→ P3 可运营 → P4 加固 | [product-brief §9](product-brief.md) |

---

## 9 · 容易混淆的成对术语

**这一节比前面七节更值得读。** 下面每一对里，混淆的后果都不是「说错话」，而是做错事。

| A | B | 差在哪 | 混淆的后果 |
|---|---|---|---|
| **CN2 GT** | **CN2 GIA** | 同为电信精品，GT（Global Transit）是**较低**一档，GIA（Global Internet Access，**AS4809**）是**最高**等级、独立高优先级承载网 | 读竞品文案时会把「CN2」当成一个东西。⚠️ 但对我们**两者都买不到**——GCP 与 AS4809 无邻接，区分它们只在读 traceroute 时有用 |
| **ICP 备案** | **ICP 许可证** | MIIT 签发的**两类不同文件**：非经营性网站的**备案**（`京ICP备04123456号`，纯信息性、不涉及直接销售）；面向售卖商品/服务站点的**经营性 ICP 许可证**（`京ICP证123456号`）。移动应用 ICP 备案自 2023-09-01 起亦为必需 | 以为「随便备个案」就能接 Cloudflare China Network。我们**卖服务**，需要的是许可证那一档——而这类业务在境内不具备合法经营基础，**两者都取不到** |
| **备案（形式登记）** | **许可（实质审批）** | 备案是把信息报上去登记；许可是主管部门实质审查后发牌照。⚠️ 另有一层最常被误用：**ICP 备案与邮件投递无关**——三个独立一手来源一致（阿里云 ICP FAQ、网易企业邮、腾讯云 SES：解析指向境外服务器无需备案，仅发信用途的域名不强制备案） | 把「我们没备案」当成邮件送达率低的解释，然后去改一个不是原因的东西。**不要让这件事影响架构决策** |
| **Cloudflare 全球网络** | **Cloudflare China Network** | 全球网络的 Free/Pro/Business zone 在**中国大陆没有任何 PoP**，中国用户必然出境到境外节点；China Network 是 **Enterprise 专属订阅**，与**京东云**合营，要求每个 apex 域名持 ICP、过内容审核、强制 IPv6，且 **Pages 与 Turnstile 都不在其可用产品清单里** | 看到 `pages.dev` 有时能打开就以为「Cloudflare 在中国有节点」。那**不是** China Network。直接后果：Turnstile 出局，人机验证方案要重做 |
| **mihomo** | **Clash Premium** | mihomo（原 Clash.Meta）是原版 Clash 的**唯一继承者**，2026-08 仍在日更；Clash Premium 是原版 Clash 的闭源增强版（TUN 等特性），随原版一同消失。原版仓库 `Dreamacro/clash` 现返回 **404**。⚠️ Premium 的具体停更时点本项目未调研，**待核实** | 教程里写「装 Clash Premium 开 TUN」= 引导用户去下载一个不存在的东西。**对外文案统一写 mihomo / Clash.Meta 内核** |
| **`publicKey`** | **`password`**（REALITY） | 同一个 x25519 公钥的新旧字段名。改名是**安全性改名不是美学改名**：持有它即可探测 REALITY 服务器，所以它是**客户端持有的秘密**。⚠️ 旧名仍作静默别名被接受 | 按旧教程配置**不会报错**，只会行为不符预期——这正是危险之处。同类静默别名还有 `clients`→`users`、`dest`→`target` |
| **`client-fingerprint`** | **`fingerprint`**（mihomo） | 前者是 **uTLS 指纹**（`chrome`/`firefox`/`safari`/`iOS`/…，注意 `iOS` 的大小写）；后者是**证书 SHA-256 pin**。完全不同的两个东西 | 写错会导致**难以排查的连接失败**——不是配置报错，是连不上 |
| **`/config` 的 ETag** | **`/user` 的 ETag** | 必须是**两个独立的 ETag**（`W/"3-c17"` 与 `W/"3-u482"`） | 用同一个 ETag：改一次节点配置会让用户列表也失效，反之亦然。每次改配置都触发一次全量用户下发 |
| **控制面故障** | **数据面故障** | 面板打不开 / API 域名被封 ≠ 用户断网。节点在拉取失败时用最后一次成功的配置继续服务 | 用户在一个**其实完好**的系统里做灾难恢复。所以失联排障树的**第一个分支**必须是「你现在还能上网吗？能 = 直接用现有代理打开面板」——这是最常命中却最易被忽略的一支 |
| **订阅 token / 节点密钥** | **用户密码** | 前两者是**我们签发的 256 bit CSPRNG 高熵随机串**，无字典攻击面，用 **SHA-256** + 常数时间比对即可；用户密码是低熵人类输入，**必须 argon2id** | 给节点密钥上 argon2id：每 60 秒 × 节点数 × 5 端点付一次内存密集哈希，Cloud Run 单实例并发 80 × 64 MiB = 5 GiB 超出实例内存。⚠️ 这条取舍在密钥改为人工可设时**立刻失效**——所以 API 不提供「自定义密钥」入口 |
| **Cloud Tasks** | **Pub/Sub** | 分工定死：**Cloud Tasks 管流量入账，Pub/Sub 管告警** | 混用会让「队列积压」这条告警**自己走在积压的队列里** |

---

## 10 · 代价

> ⚠️ 这一选择的代价，必须留在记录里而不是被措辞掩盖：
>
> 1. **维护成本可量化，而且没有任何机制保证它被付。** 本表 **115 个词条 + 11 组易混淆对照**，
>    事实来源散布在 **34 份文档、17,446 行**里。每次 ADR 推翻旧裁决，本表至少要改数条——
>    举例：若 [ADR 0004 §3.7](../05-adr/0004-transport-hardening.md)（自认论据最弱）被实测推翻，
>    §6 的「Premium / Standard」行、§6 的开篇结论、§7 的定价相关表述都要同步改。
>    🔴 **术语表是全仓最容易腐烂的文档，因为没有人会因为它过时而收到报错。**
>    唯一的缓解是规矩 1（以最后一列为准），它把腐烂的后果从「读者拿到错结论」降级为「读者多点一次链接」。
> 2. **「一句话解释」是有损压缩，丢掉的往往正是限定条件。** 例如 mux 那一行删掉了原文最重要的框定
>    （USENIX Sec'24 是研究者**扮演审查者**在美国 ISP 上做的实验，**不是** GFW 已部署行为的观测）。
>    读者若拿本表当结论用，会把「可以被检测」读成「正在被检测」——
>    而 ADR 0004 的整份代价尾节就是在处理这个区别。
> 3. **保留已裁定不用的术语，让表长了约 10%**（VMess / Trojan-Go / TUIC v5 / XrayR / V2bX / soga /
>    Brutal / 端口跳跃 / NekoRay / Clash / Turnstile / WireGuard 第一跳，共 12 条 = 全表 10.4%）。
>    删掉它们能让表更短更好读；不删的理由是
>    [docs/README §4](../README.md)：**一条裁决被推翻时，它的理由不会自动消失。**
>    **取舍失效条件**：若某条排除项在两年内从未被人重新提起，它可以移进一份 `glossary-archive.md`。
> 4. **本表引用的版本号与价格全部是 2026-08-16 快照**（Xray v26.3.27、sing-box v1.13.18、
>    mihomo v1.19.30、Hysteria app/v2.12.1、$0.23/GiB、$0.11/GiB）。
>    版本号腐烂最快，其中 **Xray 那一条尤其危险**——它不是参考值而是硬约束（钉死 v26.3.27，
>    升到 v26.7.11+ 会让 mihomo 系客户端集体连不上）。
>    **取舍失效条件**：任一被钉死的版本发生变更而本表未同步，本表就从索引变成误导源。
> 5. **双语命名让部分术语有两个可搜索形态**（复式记账 / double-entry ledger、
>    多路复用 / mux、残留封锁 / residual censorship）。
>    这是 [docs/README §5](../README.md)「中文散文 + 英文标识符」规矩的直接后果，
>    代价是 `grep` 一个词可能漏掉另一半。本表第二列的存在就是为了让两种写法都能命中。
> 6. **本表不覆盖对外文案。** 项目内部说「机场」「代理」没问题，
>    但对用户能说什么、不能说什么（不得出现 "Clash"、不得承诺流媒体解锁）是另一套约束，
>    见 §11 第 7 条。**在那份文档写出来之前，本表被误用于对外文案是一个真实风险。**

## 11 · 这次没有解决的

- [ ] **CTGNet / CMIN2 / AS23764 的 AS 号与线路归属未核实。**
      本项目 34 份文档里从未出现过这三个词，本表按行业通称收录并全部标 **待核实**。
      不在本次范围，是因为 [ADR 0004 §3.6](../05-adr/0004-transport-hardening.md) 已证明
      GCP 上买不到任何一条中国精品线路——核实它们**不改变任何裁决**，只影响读竞品文案的精度。
- [ ] **码商的运作方式与风险敞口未调研。**
      [payments.md](../01-research/payments.md) 只调研到易支付**平台层**，没有再往下追一层。
      不在本次范围，是因为易支付已被裁定为「备用且风险极高」，
      再往下追的边际价值低于其他 P0 阻塞项（见 [evidence/README.md](../evidence/README.md)）。
- [ ] **Clash Premium 的停更时点与它的 TUN 实现差异未核实。**
      我们只核实了「`Dreamacro/clash` 返回 404、仓库不存在」这一条。
      这条缺口对教程写作有实际影响（要不要提这个名字、怎么解释它消失了）。
- [ ] **mihomo 的 `xtls-*` flow 等价于 Xray 的 `xtls-*-udp443`（不接管 UDP 443）
      这条差异对订阅生成器意味着什么，未评估。** 它可能意味着 mihomo 用户与 sing-box 用户
      拿到的 UDP 行为不一致，属于 [api-contract §5](../02-architecture/api-contract.md)
      「UA 分发表需实测」的一部分，不是术语问题，故不在本次范围。
- [ ] **没有机制保证新文档引入新术语时回填本表。**
      可行的做法是在 [docs/README §5](../README.md) 加一条写作规矩（「引入新专有名词时同步登记 glossary」），
      但那是对全仓写作约定的修改，超出一份术语表的权限，应由一次 ADR 或 README 修订裁决。
- [ ] **反向索引没做**（现在只有「术语 → 文档」，没有「文档 → 它引入了哪些新术语」）。
      要做需要在每份文档里加一节，34 份文档的改动成本高于收益。
- [ ] **面向用户的对外文案术语表未写。** 它的读者与本表完全不同（用户，不是项目内部），
      且已知至少有两条硬约束：对外不得出现 "Clash"、不得承诺流媒体解锁。
      应当独立成篇（建议落在 [03-product/](../03-product/)），不是本表的扩写。
- [ ] **本表没有英文版。** 若将来要把 `openapi/uniproxy-v1.yaml` 或
      [api-contract.md](../02-architecture/api-contract.md) 给第三方看，
      术语的英文对照需要独立维护——第二列现在只写了通行英文名，不构成对照表。

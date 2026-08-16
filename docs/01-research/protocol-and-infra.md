# babel.plus 协议与基础设施技术调研

> 文档状态：初版调研（Research Draft）
> 撰写日期：2026-08-16
> 适用架构：中国大陆客户端 → Cloudflare 边缘 → Google Cloud（project `oratis-491316`）→ 全球互联网
> 阅读对象：babel.plus 架构决策者

## 0. 阅读须知：证据分级

本文所有结论按以下三级标注，请勿混用：

| 标记 | 含义 |
| --- | --- |
| **[已核实]** | 从官方文档、学术论文、或可程序化查询的接口（如 GitHub API）直接取得，正文给出链接 |
| **[社区共识]** | 来自 net4people/bbs、GitHub Discussions、机场运营者社群的普遍说法，无官方背书，可能过时 |
| **[需实测/待核实]** | 无法从可信来源确认的数字或行为，必须在自建环境实测后回填 |

对抗审查领域的一个基本事实是：**封锁状态是时变的、地域差异化的、且运营商差异化的**。任何"某协议在中国能用/不能用"的断言，其有效期通常以周计。因此本文的选型建议强调**协议栈的可切换性**，而不是押注单一协议。

另需明确一点：本文讨论的 Cloudflare 前置方案存在**服务条款风险**，见 §2.6。这不是技术风险而是商业风险，架构决策必须把它当作一等约束。

---

## 1. 协议调研

### 1.1 对抗模型：先搞清楚 GFW 在检测什么

在逐个协议评估之前，需要先明确 2025-2026 年的实际对抗面。这决定了各协议的评分权重。

**（1）主动探测（Active Probing）** — GFW 会对疑似代理服务器主动发起连接，观察其响应行为。经典研究 *How China Detects and Blocks Shadowsocks*（IMC 2020）证实：GFW 用**首包的长度与熵值**筛选疑似 Shadowsocks 流量，再发送**七种不同类型的主动探测**去验证猜测 **[已核实]**。这是"服务器必须对非法客户端表现得像一个真实服务"这一设计原则的直接来源，也是 REALITY、Trojan `fallback`、ShadowTLS 的共同思路。

**（2）TLS-in-TLS 指纹** — 这是当前最严重的结构性威胁。Xue 等人在 *Fingerprinting Obfuscated Proxy Traffic with Encapsulated TLS Handshakes*（USENIX Security 2024）中提出了一个**协议无关**的检测方法：任何代理／隧道行为都必然产生嵌套的协议栈，当用户在隧道内访问 HTTPS 站点时，内层 TLS 握手的**包长序列与时序特征**会在外层加密流中留下可识别的印记。作者构建了基于相似度的分类器，并**在一个服务百万级用户的中型 ISP 内实网部署**，证明即使加了随机填充和多层封装，混淆代理流量仍可被可靠检出且附带损害很小 **[已核实]**。

这一点的含义非常重要：**它打击的不是某个协议，而是"代理"这个行为本身**。XTLS-Vision、AnyTLS 的填充方案都是针对它的缓解措施，而非根治。

**（3）QUIC/UDP 上的 SNI 审查** — Zohaib 等人在 *Exposing and Circumventing SNI-based QUIC Censorship of the Great Firewall of China*（USENIX Security 2025）中证实：自 **2024 年 4 月 7 日**起，GFW 开始按域名封锁 QUIC 连接；GFW 会**大规模解密 QUIC Initial 包**、应用启发式过滤规则，并使用一份**独立于其他审查机制的封锁清单**（每周平均封锁 43.8K 个 FQDN，历史累计 58,207 个域名）**[已核实]**。

论文同时给出了一个关键弱点：**截至 2025 年 1 月，GFW 不会重组跨多个 UDP 数据报或多个 QUIC CRYPTO 帧分片的 TLS ClientHello** —— 把 SNI 拆分即可绕过 **[已核实]**。作者已与 Firefox、quic-go 及主要 QUIC 类翻墙工具的社区合作集成该规避策略。

注意其适用边界：**该机制以能解出明文 SNI 为前提**。Hysteria2 开启 salamander obfs 后整个包被打乱、不存在可解析的 QUIC Initial，因此不受此机制直接影响；而 TUIC v5 走标准 QUIC + TLS，是直接暴露在该机制下的。

**（4）UDP QoS 限速** — 与上述"精确封锁"不同，这是一种**粗粒度降级**。社区长期报告国际出口 UDP 在晚高峰被显著限速。关于这究竟是审查机构的蓄意 QoS 还是纯粹的链路拥塞，**社区两种说法都有，缺乏可引用的实证研究** **[需实测/待核实]**。但无论成因如何，**运营层面的结论是一致的：不能把 UDP 类协议作为唯一通路**。

**（5）IP 封锁** — 最钝但最有效的手段。一旦出口 IP 被识别为代理，直接丢包或 RST。这是选择 CDN 前置（IP 池巨大且与正常业务共享，封锁附带损害极高）的核心动机。

### 1.2 逐协议评估

#### VLESS + XTLS-Vision + REALITY

**机制。** REALITY 的核心是**借用他人网站的 TLS 握手**。服务端不持有自己的证书，而是在客户端认证失败时，把连接**原样代理到真实目标站点（`dest`）**，让探测者看到该站点完整、真实、证书链有效的 TLS 握手。官方 README 的表述是它能"消除服务端 TLS 指纹特征"同时保留前向保密，并且"无需购买域名、无需配置 TLS 服务端"**[已核实]**。这使得主动探测（对抗面 1）基本失效——探针看到的就是一个真实的大站。

XTLS-Vision（`flow: xtls-rprx-vision`）则针对对抗面 2：它对内层 TLS 数据做直通处理并加入**内层握手随机填充**，削弱 TLS-in-TLS 的长度特征。Xray 官方文档明确 `xtls-rprx-vision` 会**接管发往 UDP 443 的流量**，而 `xtls-rprx-vision-udp443` 则不接管 **[已核实]**。

**新进展。** Xray-core 在 2025 年 8 月合入了 **VLESS Encryption**（PR #5067），提供基于 **ML-KEM-768 + X25519** 的后量子 1-RTT PFS 与抗重放 0-RTT AEAD 加密层，配置串形如 `mlkem768x25519plus.native.1rtt.<padding>.<key>` **[已核实]**。这解决的是"REALITY 私钥若泄露则历史流量可解"以及未来量子威胁下的前向保密问题，与抗审查是正交的两件事。

**版本。** Xray-core 最新发布 **v26.3.27（2026-03-27）** **[已核实，GitHub API]**。项目已切换为日历版本号。

**CDN。** **不可用。** REALITY 要求客户端与你的服务器建立**直接 TCP 连接**并完成一个由你控制的定制握手；CDN 会终止 TLS，REALITY 的整个信任模型随之崩塌。这是 REALITY 的根本性限制，不是配置问题。

**封锁现状。** 截至 2026 年，VLESS+Vision+REALITY 仍是中国大陆最主流、最稳定的直连方案 **[社区共识]**。但需注意一个警讯信号：net4people/bbs issue #546 报告**俄罗斯**部分家宽 ISP 部署了基于 TLS 连接行为的管控，导致 VLESS 及其多数变体失效 **[已核实该 issue 存在]**。俄罗斯常被视为审查技术的先行试验场，此类方法迁移到中国的可能性需要持续监控。

#### VMess + WebSocket + TLS

**机制。** VMess 是 V2Ray 时代的原生协议，现代实现仅保留 AEAD 变体（Xray 中 `alterId` 必须为 0，旧的 MD5-AEAD 模式已移除）**[已核实]**。WS+TLS 组合的价值不在协议本身，而在于 **WebSocket 是标准 HTTP Upgrade，可以穿过任何支持 WS 的 CDN**。

**评估。** 相比 VLESS，VMess 每个连接多一层自有加密与认证头，纯属冗余开销——外层已有 TLS。抗检测能力上，WS+TLS 的 TLS-in-TLS 特征**完全暴露**，没有任何 Vision 式的缓解。

**结论。** **作为主力协议已被淘汰**，唯一保留理由是老旧客户端兼容 **[社区共识]**。同样是走 CDN，`VLESS + WS + TLS` 严格优于 `VMess + WS + TLS`；而 `VLESS + XHTTP` 又优于 WS（见 §2.4）。

#### Trojan-Go

**机制。** Trojan 协议本身思路优雅：客户端在 TLS 内发送一个密码哈希，**密码错误时服务端把连接透明转交给一个真实的 Web 服务**（fallback），从而抗主动探测。

**维护状态（关键发现）。** `p4gefau1t/trojan-go` 的**最后一个 release 是 v0.10.6，发布于 2021-09-14**；仓库最后一次 push 为 2024-07-14 **[已核实，GitHub API]**。原版 `trojan-gfw/trojan` 最后 push 为 2024-08-21 **[已核实]**。

**结论。** **Trojan-Go 这个实现事实上已停止维护，不应用于新部署。** 五年无发布意味着任何新出现的检测手法都不会有对应缓解，且存在未修补安全问题的风险。

需要区分实现与协议：**Trojan 协议**本身在 Xray-core、sing-box、mihomo 中均有活跃维护的实现，作为兼容性选项保留是合理的；但它相对 VLESS 没有任何优势，且不支持 XTLS-Vision 式的 TLS-in-TLS 缓解。

#### Hysteria2

**机制。** 基于 QUIC 的 TCP & UDP 代理。核心特性 **[已核实，官方文档]**：

- **Brutal 拥塞控制**：固定速率模型，**不响应丢包**。这是它在高丢包链路上表现优异的原因，也是它对邻居流量不友好的原因。
- **Salamander 混淆**：官方文档称其"把每个包打乱成看不出模式的随机字节"，使流量不可识别为 QUIC/HTTP3。另有实验性的 Gecko 变体增加握手包分片。
- **Masquerade**：未认证连接可伪装为真实 Web 服务器，支持三种模式——反代另一个网站（`proxy`）、提供静态文件目录（`file`）、返回自定义字符串（`string`）。这是它的抗主动探测机制。
- **端口跳跃（Port Hopping）**：服务端可监听 UDP 端口范围并自动配置 nftables；客户端按 `hopInterval` 轮换，默认固定 30 秒间隔，也可配 `minHopInterval`/`maxHopInterval` 随机化。这是对抗"单端口被封"的有效手段。
- **UDP 支持**：原生支持，`disableUDP` 可关闭。会话默认 60 秒无活动超时。
- `ignoreClientBandwidth`：服务端忽略客户端上报带宽，由管理员统一控制——**多租户运营必开**，否则客户端可以自行声明任意带宽。

**CDN。** **官方明确不支持。** Hysteria 文档专门有一页回答这个问题，原话是这个问题的"简短明确的答案是'不行'。它就是不会工作"。给出三条理由：认证后连接切换到 CDN 不支持的自定义协议；多数 CDN 不支持到源站的 HTTP/3；即使技术上打通，客户端也是在和 CDN 的 QUIC 实现对话而非 Hysteria 优化过的实现，速度优势荡然无存 **[已核实]**。

**版本。** 最新 **app/v2.12.1（2026-08-09）**，维护活跃 **[已核实，GitHub API]**。

**封锁现状。** 这是全文分歧最大的一项。搜索结果中存在若干 SEO 内容农场声称"2026 年 2 月中国电信部署了增强 QUIC 分类，无混淆的 Hysteria2 可在约 30 秒内被识别"——**这类具体数字无任何可信来源支撑，本文不予采信** **[需实测/待核实]**。

可以谨慎陈述的是：**开启 salamander obfs 后，Hysteria2 不产生可解析的 QUIC Initial 包，因此不落入 USENIX Sec'25 论文所述的 SNI-based QUIC 审查机制**。其真实风险来自更钝的 **UDP QoS 降级**，表现为"能连上但晚高峰速度崩塌"，且**因地区、运营商而剧烈波动** **[社区共识]**。

**运营结论：Hysteria2 是极佳的加速通道，但绝不能是唯一通道。**

#### TUIC v5

**机制 [已核实，SPEC.md]：** 通过在客户端与服务端间同步 UDP session ID 实现 **0-RTT 全锥型（Full Cone）UDP 转发**；UDP 包可走 QUIC 单向流或 QUIC datagram 两种模式（可靠性 vs 延迟的取舍）；认证使用 **TLS Keying Material Exporter** 从密码派生 token，以客户端 UUID 为 label；TCP 与 UDP 会话复用同一条 QUIC 连接。

**维护状态（关键发现）。** 上游 `EAimTY/tuic` **最后一次 push 为 2025-05-15** **[已核实，GitHub API]**，已基本停滞。实际可用的是 **sing-box 内置的 TUIC 实现**，随 sing-box 活跃维护。

**关键劣势。** TUIC 走**标准 QUIC + 标准 TLS**，**ClientHello 中的 SNI 是可被 GFW 解出的**，因此**直接暴露在 USENIX Sec'25 所述的 QUIC SNI 审查机制之下**。它也**没有 Hysteria2 那样的内建 obfs 与 masquerade**。

**结论。** 相对 Hysteria2，TUIC v5 在抗审查上**全面处于劣势**，在中国场景下没有选择它而非 Hysteria2 的理由。其 Full Cone NAT 特性对 P2P/游戏场景有价值，属于小众需求。

#### ShadowTLS v3

**机制。** 中继在握手阶段把流量转发给一个**真实的 TLS 服务器**，握手结束后才把客户端接到隐藏的后端代理（通常是 SS-2022）。这样中间设备看到的是一次**货真价实的、与真实大站的 TLS 握手**（包括真实证书链）。

**v3 相对 v2/v1 的改进 [已核实，sing-box 文档]：** 用**多用户独立密码**取代 v2 的单一密码；新增 `strict_mode`（v3 专有的安全选项）；新增 `wildcard_sni`（`off`/`authed`/`all` 三档，按 SNI 自动重写目的地）；支持按 server name 分别配置 handshake。

**为什么有 v3。** Gaukas Wang 等人的 *Chasing Shadows: A security analysis of the ShadowTLS proxy*（FOCI 2023）对 v1/v2 做了安全分析并发现问题，v3 是针对性的回应 **[已核实]**。

**维护状态。** `ihciah/shadow-tls` 最后 push 为 **2025-04-25** **[已核实，GitHub API]**，上游节奏放缓；sing-box 侧实现活跃。

**CDN。** 不可用（同 REALITY，需要直连）。

**定位。** 与 REALITY 是**同一生态位的竞品**，都解决"让探测者看到真实站点"。REALITY 的优势是单进程、配置简单、生态更大；ShadowTLS 的优势是握手确实来自真实服务器（而非本机代理），理论上更难被区分。**在已有 REALITY 的前提下，ShadowTLS 的边际价值有限**，可作为备选而非并行主力。

#### Shadowsocks-2022（SIP022 / AEAD-2022）

**机制 [已核实，SIP022 规范]：** 使用 **BLAKE3 的 key derivation 模式**派生子密钥，取代已过时的 HKDF_SHA1；`2022-blake3-aes-128-gcm` 与 `2022-blake3-aes-256-gcm` 为**必须实现**的方法；请求与响应流中都加入了**独立 header chunk** 以增强安全性与**抗重放**，并使用**滑动窗口过滤器**做重放保护；**要求用户直接提供密码学安全的定长 PSK**，实现**禁止**使用旧的 `EVP_BytesToKey` 或任何从口令派生密钥的方式。

**评估。** SIP022 修好了老 AEAD 版本的密码学问题，但它**没有也不打算解决流量形态问题**——SS-2022 的线上流量仍然是**高熵的随机字节流**，恰好命中 IMC'20 论文所述"按首包长度与熵值筛选"的检测路径。

**结论。** **裸 SS-2022 不适合作为中国大陆的入口协议。** 它的正确用法是作为**内层载荷**，外面套 ShadowTLS v3。这个组合（`ShadowTLS v3 + SS-2022`）在社区中是成熟方案。

#### AnyTLS

**机制。** 一个 TLS 形态的隧道，核心创新是**填充方案（padding scheme）**——通过可配置的分片/填充策略主动改造包长分布，专门针对 §1.1 的 TLS-in-TLS 长度指纹。同时引入**会话池（session pool）**复用连接，避免每次新建连接都做完整 TLS 握手（握手频率本身也是一个指纹）。

**支持情况 [已核实]：** sing-box 是首个稳定实现（inbound + outbound + 客户端会话池）；mihomo 双向完整支持，字段为 kebab-case，额外提供 uTLS 指纹与 ECH 选项。**mihomo 明确表示不支持也不会支持 AnyTLS + REALITY 组合**，若需隐藏 SNI 建议改用 ECH 或与 ShadowTLS / RestTLS / JLS 组合。协议 v2 于 2025 年 4 月引入，改进了卡死隧道检测与超时行为。v2rayN 自 v7.14.3+ 支持 AnyTLS URI 与订阅。

**维护状态。** `anytls/anytls-go` 最后 push **2026-08-03**，活跃 **[已核实，GitHub API]**。

**评估。** AnyTLS 是**针对 2024 年那篇 TLS-in-TLS 论文的直接工程回应**，思路正确。但它比 REALITY 年轻得多，实网对抗数据不足，且需要一个**真实域名 + 真实证书**（不像 REALITY 可以白嫖别人的）。**定位为有前景的第二备选**，不宜作为首发主力。

#### WireGuard / Cloudflare WARP

**机制。** WireGuard 是 UDP-only、极简、定长头部的现代 VPN 协议。它的设计目标里**完全没有抗审查**这一条。

**问题。** 握手初始包有**固定长度与固定的消息类型字节偏移**，是教科书级的易指纹协议。这在 2022 年就已是社区共识（WireGuard 官方邮件列表中关于中国封锁与 `swgp-go` 用户态混淆代理的讨论）**[已核实该讨论存在]**。

搜索中出现的"当前检测率 100%"之类表述来自 SEO 内容站，**无来源，不予采信** **[需实测/待核实]**。可靠的说法是：**裸 WireGuard 在中国大陆不可作为跨境通路依赖** **[社区共识]**。

**WARP 现状。** Cloudflare 社区中有多个 2025 年的报告：WARP / Zero Trust 在中国无法连接（API 与 DNS 可通但 WARP 隧道建不起来）；Cloudflare 于 2025 年 8 月调查过中国区 WARP 连通性问题并于 8 月 22 日修复。此外 **1.1.1.1 自 2023-10-01 起在中国被封**（net4people/bbs #295）**[已核实该 issue 存在]**。社区亦报告 WARP 的新协议 MASQUE 早已被 GFW 封锁 **[社区共识]**。

**正确定位。** WireGuard/WARP **不应出现在中国侧的第一跳**。它有价值的位置是**出口侧**：GCP 实例 → WARP → 互联网，用于获得干净出口 IP、规避部分服务的云 IP 风控（如流媒体解锁）。这是一个**可选的增值功能，不是核心链路**。

### 1.3 协议对比总表

| 协议 | 抗主动探测 | 抗 TLS-in-TLS 指纹 | 传输 | UDP | 走 Cloudflare CDN | 延迟/吞吐特性 | 中国封锁现状（2025-26） | 维护状态 |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| **VLESS + Vision + REALITY** | 极强（回落真实站点） | 中-强（内层握手随机填充） | TCP/TLS | 是（Vision 接管 UDP:443） | **否** | 低延迟，Splice 零拷贝，吞吐好 | 主流可用 [社区共识] | Xray v26.3.27 (2026-03) 活跃 |
| **VLESS/VMess + WS + TLS** | 弱（依赖回落配置） | **弱（完全暴露）** | TCP/TLS/HTTP | 需 Mux 模拟 | **是** | WS 帧开销 + CDN 跳数，延迟较高 | 可用但特征明显 [社区共识] | 活跃（VMess 已不推荐） |
| **VLESS + XHTTP** | 中（普通 HTTP 形态） | 中（分片打散） | TCP/HTTP2/3 | 需 Mux 模拟 | **是（推荐）** | 上下行可分离，效率优于 WS | 可用 [社区共识] | 活跃 |
| **Trojan-Go** | 强（fallback 真实 Web） | 弱 | TCP/TLS | 是 | 是（WS 模式） | 与 VLESS+TLS 相当 | — | **停滞：末次 release 2021-09** |
| **Hysteria2** | 强（masquerade 三模式） | N/A（非 TLS 内嵌） | QUIC/UDP | **原生，强** | **否（官方明确）** | **最优**：Brutal 抗丢包，高丢包链路碾压 TCP | 波动大，UDP QoS 降级 [社区共识] | v2.12.1 (2026-08) 活跃 |
| **TUIC v5** | 弱（无 masquerade） | N/A | QUIC/UDP | **原生，Full Cone** | **否** | 好，0-RTT，但不及 Hysteria2 抗丢包 | **SNI 暴露于 QUIC 审查** [已核实机制] | 上游停滞 2025-05；sing-box 内置活跃 |
| **ShadowTLS v3** | 极强（真实服务器握手） | 中（取决于内层） | TCP/TLS | 取决于内层 | **否** | 良好，握手多一跳 | 可用 [社区共识] | 上游放缓 2025-04 |
| **Shadowsocks-2022** | **弱（高熵首包易筛）** | N/A | TCP/UDP | 是 | **否** | 开销最低，吞吐好 | **裸用不推荐** [已核实检测机制] | 活跃 |
| **AnyTLS** | 中（需真实证书） | **强（填充方案专门针对）** | TCP/TLS | 是 | 理论可（未验证） | 会话池减少握手开销 | 数据不足 [需实测] | v2 (2025-04)，活跃 |
| **WireGuard / WARP** | **无** | N/A | UDP | 原生 | **否** | 极低开销，内核态最快 | **不可依赖** [社区共识] | 活跃（但非抗审查设计） |

### 1.4 客户端支持矩阵

版本信息均通过 GitHub API 于 2026-08-16 查询 **[已核实]**：

| 项目 | 最新版本 | 发布日期 | 平台 |
| --- | --- | --- | --- |
| sing-box | v1.13.18 | 2026-08-09 | 全平台核心 + SFI/SFA/SFM/SFT |
| mihomo (Clash.Meta) | v1.19.30 | 2026-08-16 | 全平台核心 |
| Xray-core | v26.3.27 | 2026-03-27 | 全平台核心 |
| Hysteria | app/v2.12.1 | 2026-08-09 | 服务端 + 客户端 |
| v2rayN | 7.24.4 | 2026-07-30 | Windows/Linux GUI |
| Karing | 活跃（末次提交 2026-08-12） | — | iOS/Android/桌面 |
| Hiddify-app | v4.1.1 | 2026-03-05 | 全平台 GUI |
| NekoBox for Android | 末次提交 2026-02-09 | — | Android |

协议 × 客户端支持矩阵。**✅ = 已从该项目自身文档或源码确认；➖ = 不支持；⚠️ = 无法确认，须实测**：

| | VLESS+REALITY+Vision | VMess+WS+TLS | XHTTP | Trojan | Hysteria2 | TUIC v5 | ShadowTLS v3 | SS-2022 | AnyTLS | WireGuard |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| **sing-box 1.13** | ✅ | ✅ | ➖ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅（移至 `endpoints`） |
| **Xray-core v26** | ✅（原生） | ✅ | ✅（原生） | ✅ | ✅ **新增原生客户端** | ➖ | ➖ | ✅ | ➖ | ✅（outbound） |
| **mihomo v1.19** | ✅ | ✅ | ⚠️ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅（**不支持 +REALITY**） | ✅ |
| **Hiddify-app** | ✅ | ✅ | ⚠️ | ⚠️ | ✅ | ✅ | ⚠️ | ⚠️ | ⚠️ | ✅ |
| **Karing** | ⚠️ | ⚠️ | ⚠️ | ⚠️ | ⚠️ | ⚠️ | ⚠️ | ⚠️ | ⚠️ | ⚠️ |
| **Shadowrocket** | ⚠️ | ⚠️ | ⚠️ | ⚠️ | ⚠️ | ⚠️ | ⚠️ | ⚠️ | ⚠️ | ⚠️ |
| **v2rayN 7.24** | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ⚠️ | ✅ | ✅ | ✅ |
| **NekoBox (Android)** | ✅ | ✅ | ⚠️ | ✅ | ✅ | ✅ | ✅ | ⚠️ | ✅ | ✅ |

矩阵依据与保留意见：

- **sing-box / Xray / mihomo / v2rayN 的列由文档目录结构或源码枚举核实**（例如 v2rayN 的 `EConfigType.cs` 中 `Anytls=11` 是一等公民类型；其中**无 ShadowTLS 枚举**，故标 ⚠️）。
- **Xray v26 新增了原生 Hysteria 2 客户端**（`"protocol": "hysteria"` + `"settings": {"version": 2}`）**[已核实]**——这改变了"Xray 只能走 TCP 系"的旧认知。但文档同时警告：hysteria 协议自身无认证，与非 `hysteria` 传输搭配会导致 UDP 代理不可用。
- **Karing 使用的是 sing-box 的一个 fork**（`KaringX/sing-box`），README 不枚举协议。**不能假定它与上游 sing-box 协议对等**，故整行 ⚠️。
- **Shadowrocket 无任何可达的官方文档**。整行 ⚠️。关于它支持哪些参数的一切说法都应视为未经证实，**必须真机实测**。

**维护状态判断（直接影响客户端推荐策略）：**

- **原版 Clash 已彻底消失**：`Dreamacro/clash` 现返回 **HTTP 404，仓库已不存在** **[已核实]**。**mihomo 是唯一继承者**，且是本调研中更新最勤的项目（2026-08-16 当天仍有提交）。**所有对外文档不得再出现 "Clash" 这一称呼**，应统一为 **mihomo / Clash.Meta 内核**。
- **NekoRay（桌面版）已归档死亡**：`MatsuriDayo/nekoray` `archived: true`，末次 push 2024-12-12 **[已核实]**。继任者是 **`throneproj/Throne`**（v1.2.4，2026-08-08，活跃发布）。**任何仍推荐 NekoRay 的文档都是过时的。**
- **NekoBox for Android** 尚存但迟缓（1.4.2 / 2026-02-09，已停滞半年）**[已核实]**。另有一条必须转达给用户的警告：其 README 声明 **Google Play 版本自 2024 年 5 月起由第三方控制，且不是开源版本**。**不应引导用户从 Play 商店安装 NekoBox。**
- **v2rayN 活跃度极高**（113,954★，约每周发版，7.24.7 / 2026-08-15）**[已核实]**。
- **Hiddify-app** 仍活跃（末次 push 2026-08-10），但其 README 只列出 Vless / Vmess / Reality / TUIC / Hysteria / WireGuard / SSH，未列 Trojan / AnyTLS / ShadowTLS **[已核实]**。

**推荐主推组合**：iOS → Shadowrocket 或 Karing；Android → Karing 或 sing-box (SFA)；Windows → v2rayN 或 Clash Verge Rev；macOS → sing-box (SFM) 或 Karing。**桌面端不再推荐 NekoRay，改推 Throne 或 v2rayN。**

---

## 2. Cloudflare 作为前门

> 本节事实由独立核查流于 2026-08-16 从 Cloudflare 一手来源取得 **[已核实]**，标注例外者除外。

### 2.1 先说结论：一个被社区误传三年的 ToS 问题

几乎所有中文教程在讨论"Cloudflare 能不能跑代理"时都会引用 **ToS 第 2.8 条「Limitation on Serving Non-HTML Content」**。

**这条早在 2023-05-16 就被 Cloudflare 删除了** **[已核实]**（见 `blog.cloudflare.com/updated-tos`）。继续拿 2.8 说事的论证已经过时三年。残余的非 HTML 内容条款被移到了 Service-Specific Terms 的 "Content Delivery Network (Free, Pro, or Business)" 一节，且现在明确把 Developer Platform 列为可以正当承载大文件的付费服务。

**但这对我们是坏消息而不是好消息**，因为真正卡住我们的是另一条：

### 2.2 真正的红线：Self-Serve Subscription Agreement §2.2.1(j)

该条禁止：

> "use the Services to provide a virtual private network or other similar proxy services."
> —— Cloudflare Self-Serve Subscription Agreement §2.2.1(j)

这条的杀伤力体现在三点，每一点都必须被架构决策吸收：

1. **适用范围是 "the Services" 整体**，不是仅 CDN。**Workers、Pages 一并涵盖**。"我用 Worker 不用 CDN 所以没事"是错的。
2. **没有付费豁免通道。** 升级到 Pro / Business 不解除该限制。这与 2.8 时代"付费即可服务大文件"的结构完全不同。
3. **Cloudflare 保留不经通知直接停用的权利。**

另有两条相关：**§2.2.1(b)** 禁止对服务造成过度负担；**§2.2.1(c)** 禁止规避用量限制。

**因此：把 babel.plus 的中转数据面架在 Cloudflare 上，是明确违反其服务条款的行为。** 这不是灰色地带，不是"技术上没人管"，是白纸黑字。§7 的选型建议必须正面处理这个冲突，而不是绕过去。

### 2.3 Workers 的经济模型（为什么这个模式在钱上如此诱人）

| 项目 | Free | Paid |
| --- | --- | --- |
| 请求量 | 100,000 / 天（超出返回 **Error 1027**） | 含 10M / 月，超出 **$0.30 / 百万** |
| CPU 时间 | **10 ms / 次调用** | 含 30M CPU-ms / 月，超出 **$0.02 / 百万 CPU-ms** |
| 订阅底价 | $0 | **$5 / 月起** |
| Subrequests | 50 / 请求 | 更高 |
| **出网流量费** | **无** | **无** |
| 执行时长（wall clock） | **既不计费也不限制** | 同左 |

两条决定性细节 **[已核实]**：

1. **等待网络 I/O 不计入 CPU 时间。** 一条隧道 99.9% 的时间在等 socket，因此几乎不消耗 CPU 配额。
2. 定价页脚注明确：**一次 WebSocket Upgrade 计为 1 个请求；此后经由该 Worker 转发的消息不再计费为请求。**

**这两条加起来就是整个 edgetunnel 模式的经济基础**：一个用户一天开几十条隧道，跑几十 GB 流量，在计费上约等于几十个请求 + 极少 CPU-ms，**且带宽全免**。成本几乎为零——这也正是 Cloudflare 要用 §2.2.1(j) 堵死它的原因。**在商业上把成本优势建立在对方明令禁止的用法上，是不可持续的。**

### 2.4 技术硬墙：`connect()` 连不回 Cloudflare

Workers 的 TCP Sockets API（`cloudflare:sockets` 的 `connect()`）有几条硬限制 **[已核实]**：

- **"Outbound TCP sockets to Cloudflare IP ranges are blocked"** —— 出站 TCP **不能连到 Cloudflare 自己的 IP 段**。
- 试图连回发起该请求的 Worker 会得到 **"TCP Loop detected"** 错误。
- **端口 25 被封**（防垃圾邮件）。
- **没有入站 TCP**。
- `accept({ allowHalfOpen: true })` 是**专为代理场景提供**的。

**这解释了一个长期被误解的现象**：社区 edgetunnel 项目为什么普遍需要配置 `proxyIP` 或一个 SOCKS5 中继？因为当目标恰好在 Cloudflare IP 段内时 `connect()` 会被拒。**这是有文档记载的、蓄意的设计，不是 bug，不会被"修复"。**

**运行时稳定性**（对长连接隧道致命）：

- **128 MB 内存是按 isolate 计的，由该 isolate 上的并发请求共享** —— 这是真实的并发天花板，不是"每请求 128 MB"。
- **Workers 运行时每周更新数次，更新时在途请求只有 30 秒宽限期。** 意味着**长连隧道会被周期性掐断**。客户端**必须有重连逻辑**；任何"隧道能挂几小时"的预期都不成立。

### 2.5 边缘方案横向对比

| 方案 | 能承载什么 | 计划要求 | ToS 状态 | 对本项目可用性 |
| --- | --- | --- | --- | --- |
| **Workers / Pages（edgetunnel）** | WSS / HTTP，经 `connect()` 出站 TCP | Free 起 | **违反 §2.2.1(j)** | 技术可行、成本极低、**合规不可行** |
| **CDN 反代 + WS / XHTTP** | HTTP(S) 语义流量 | Free 起 | **违反 §2.2.1(j)** | 同上 |
| **gRPC over CDN** | gRPC 流 | **全计划含 Free** | 同上 | 需源站 :443 + TLS + HTTP/2 + ALPN 通告 + 代理主机名 + Full SSL；未在 zone 上开启则 **403**。WAF **只检查 header 不检查流内容** |
| **Cloudflare Tunnel (`cloudflared`)** | HTTP；任意 TCP 需 Access | Zero Trust Free（<50 用户），超出 **$7/用户/月** | **官方认可的产品** | **任意 TCP 要求每台客户端装 `cloudflared` 并过 Access/SSO 认证** —— 对未注册的普通订阅用户不是 drop-in 方案 |
| **Spectrum（通用 TCP/UDP）** | 裸 TCP/UDP | **Enterprise 专属，且是付费加购项** | 合规 | **不可行**（Pro/Business 只给 1 个 Minecraft + 1 个 SSH 应用，Business 多一个 RDP）。定价 **待核实** |

### 2.6 可代理端口

Cloudflare 代理（橙云）的端口列表 **确认未变** **[已核实]**：

| 协议 | 端口 |
| --- | --- |
| HTTP | 80, 8080, 8880, 2052, 2082, 2086, 2095 |
| HTTPS | **443, 2053, 2083, 2087, 2096, 8443** |

两条实务要点：

- **所有非标端口默认不缓存。** 对代理场景这反而是好事（不会被缓存层干扰），但意味着不能指望 CDN 缓存分担任何负载。
- **在启用了 China Network 的中国境内数据中心，只有 80/443 可用。** 非标端口在中国节点上不工作。

### 2.7 China Network：与我们无关

Cloudflare China Network 是 **Enterprise 专属订阅**，与合作伙伴 **京东云（JD Cloud）** 共同运营，要求**每个 apex 域名持有有效 ICP 备案**、通过内容审核、并强制启用 IPv6 **[已核实]**。

**结论：Free / Pro / Business 的 zone 在中国大陆没有任何 PoP。** 中国用户的请求必然出境到境外节点。

**具体落到哪些境外 PoP（香港 / 东京 / 大阪 / 首尔 / 新加坡 / 洛杉矶 / 圣何塞）—— 本次未取得可引用来源，标记为 待核实。** 社区普遍这么说，但社区说法与实测经常不符，且**因运营商、省份、时段而异**，必须自行用 `traceroute` + Cloudflare `/cdn-cgi/trace` 的 `colo=` 字段实测。

### 2.8 XHTTP：如果一定要走 CDN，它优于 WebSocket

Xray 的 **XHTTP** 是目前 CDN 前置的技术最优解 **[已核实，来自 Xray-core Discussion #4113]**：

- **不需要 CDN 专门支持 WebSocket 或 gRPC**，它就工作在普通 HTTP 请求/响应之上。
- **上下行分离**：上行与下行可以走**完全不同的 CDN、IP、甚至不同协议**。
- 模式：
  - `packet-up` —— 上行拆成多个 POST，下行流式；**兼容性最好**，能穿几乎所有 HTTP 中间件。
  - `stream-up` —— 全双工流式，**用 gRPC header 伪装，专门用于穿 Cloudflare 的 H2 流式**。
  - `stream-one` —— 单个 POST 双向流。
  - `auto`（默认）—— 按协议自动选（TLS H2 用 `stream-up`，REALITY 用 `stream-one`）。
- 官方建议在 Cloudflare 面板中**启用 gRPC 支持**；自建 Nginx 前置时应把 `proxy_pass` 换成 `grpc_pass` 以避免缓冲。

`xhttpSettings` 的完整字段名本次未能从官方页面取得，**待核实**（参见 `xtls.github.io/en/config/transports/xhttp.html`）。

---

## 3. Google Cloud 作为出口

> 本节价格于 **2026-08-16** 从 Google 官方定价页取得（定价表由 JS 按区域渲染，已实际操作区域选择器读取）**[已核实]**。Spot 分档另有说明。注意 Google 已将文档迁至 `docs.cloud.google.com`，定价页仍在 `cloud.google.com`。

### 3.1 本节最重要的结论：网络层级比机型更值钱

先给结论，因为它推翻了一个常见假设：**中转服务的成本由出网流量主导，而不是由计算实例主导。而出网单价可以通过切换网络层级砍掉一半以上。**

| 出网路径 | 到中国大陆单价 | 免费额度 |
| --- | --- | --- |
| **Premium Tier** | **$0.23 / GiB**（0–1 TiB 档） | 极少（见 §3.5） |
| **Standard Tier（asia-east1 / asia-northeast1）** | **$0.11 / GiB** | **每区域每月前 200 GiB 免费** |
| **Standard Tier（us-central1）** | **$0.085 / GiB** | **每区域每月前 200 GiB 免费** |

**切换网络层级带来的节省，超过任何计算侧优化能带来的节省。** 一台 e2-small 在 asia-east1 的月度计算成本约 $14.16，而 1 TiB 的中国方向出网在 Premium 下是约 $235、在 Standard 下是约 $90。**成本模型必须以出网为中心。**

### 3.2 Cloud Run：三条独立的否决理由

| 能力 | 结论 |
| --- | --- |
| WebSocket | **支持**，无需额外配置 |
| **请求超时** | **默认 300 秒（5 分钟），最大 3600 秒（60 分钟）**。WebSocket 按长时 HTTP 请求计，**受同一超时约束** |
| 会话亲和性 | **仅尽力而为**——"新的 WebSocket 请求仍可能因内建负载均衡连到不同实例" |
| **裸 TCP / UDP 互联网入站** | **不支持。** 入口只有 HTTPS 前端 |
| HTTP/2 / gRPC / gRPC 流式 | **全部支持**（HTTP/2 需 `--use-http2`，容器须讲 h2c） |
| 容器自行终止 TLS | **不允许**——TLS 由 Cloud Run 终止后以明文 HTTP/1 或 gRPC 转发给容器 |

**Cloud Run 不能作为 babel.plus 的数据面。** 三条独立的否决理由，任何一条单独成立即可：

1. **60 分钟是单连接硬上限。** 对透明 TCP 隧道是**致命**的。
2. **无裸 TCP/UDP 互联网入站** → **REALITY、Hysteria2、TUIC、ShadowTLS、SS-2022 全部无法部署**。只剩 VLESS/VMess + WS/XHTTP，等于**把协议选择权交给平台限制**，恰好排除了抗审查能力最强的那几个。而且**容器不能自己终止 TLS，REALITY 从原理上就不可能实现**。
3. **出网被锁死在 Premium Tier** —— 官方原文：出网互联网流量使用 Premium 网络服务层级。**这意味着 Cloud Run 无法使用 §3.1 中 Standard Tier 的 200 GiB 免费额度与 $0.11/GiB 单价，中国方向一律 $0.23/GiB，且只有每月 1 GiB 北美境内的免费额度。** 对流量密集型业务，**这是全平台最差的出网费率**。

> 一个值得记录的例外：**Cloud Run worker pools 配合 Direct VPC ingress 确实接受任意 TCP**，但**仅限 VPC 内私有 IP 可达，不是互联网入站**，对本项目无用。

**Cloud Run 的正确用途是控制面**：订阅分发 API、用户面板、计费、节点健康上报。这些是无状态短请求，正是它的强项，且流量极小，Premium 费率无所谓。

**Cloud Run 定价（Tier 1，含 us-central1 / asia-east1 / asia-northeast1，三者同价）** **[已核实]**：

| 计费模式 | 项目 | 单价 (USD) |
| --- | --- | --- |
| **实例计费**（原 "CPU always allocated"，`--no-cpu-throttling`） | vCPU-秒 | $0.000018 |
| | GiB-秒 | $0.000002 |
| | 请求数 | **不另收费** |
| **请求计费**（默认，`--cpu-throttling`） | vCPU-秒（活跃） | $0.000024 |
| | vCPU-秒（空闲/最小实例） | $0.0000025 |
| | GiB-秒（活跃） | $0.0000025 |
| | 每百万请求 | $0.40 |

免费额度（每结算账号每月）：实例计费 **240,000 vCPU-秒 + 450,000 GiB-秒**；请求计费 **180,000 vCPU-秒 + 360,000 GiB-秒 + 200 万请求**；**北美境内出网 1 GiB**。

### 3.3 GCE：唯一结构上正确的选择

**按需实例单价（每小时，USD）** **[已核实]**：

| 机型 | vCPU / 内存 | us-central1 | asia-east1 | asia-northeast1 |
| --- | --- | --- | --- | --- |
| e2-micro | 2（共享核）/ 1 GiB | $0.008376428 | $0.009698985 | $0.010745715 |
| e2-small | 2（共享核）/ 2 GiB | $0.016752855 | $0.019397970 | $0.021491430 |
| e2-medium | 2（共享核）/ 4 GiB | $0.033505710 | $0.038795940 | $0.042982860 |

按 730 小时/月折算 **[已核实]**：e2-micro 约 **$6.11 / $7.08 / $7.84**；e2-small 约 **$12.23 / $14.16 / $15.69**。

**Spot 实例**：⚠️ **共享核 E2（e2-micro/small/medium）的 Spot 价格 Google 未公开发布** —— Spot 定价页完全没有共享核章节。以下为**推算值，非核实值** **[需实测]**，方法是用同区域已核实的 e2-standard-2 折扣率外推：

| 区域 | e2-standard-2 按需 | e2-standard-2 Spot | 折扣率 |
| --- | --- | --- | --- |
| us-central1 | $0.06701142 | $0.040212 | 0.600（省 40.0%） |
| asia-east1 | $0.07759188 | $0.036596 | **0.472（省 52.8%）** |
| asia-northeast1 | $0.08596572 | $0.051584 | 0.600（省 40.0%） |

**推算**的 e2-small Spot 月成本：us-central1 约 $7.34、asia-east1 约 **$6.68**、asia-northeast1 约 $9.41。**注意 Spot 折扣率各区域不同，asia-east1 明显更划算。**

**e2-micro 免费层** **[已核实]**：每月 1 台**非抢占式** e2-micro，仅限 **us-west1（俄勒冈）/ us-central1（爱荷华）/ us-east1（南卡）**，含 30 GB-月标准持久盘。**Spot 实例不适用免费层额度。**

**免费层的出网额度是 1 GB，不是 200 GB**，且原文明确"从北美到所有区域目的地，**不含中国与澳大利亚**"。**对本项目的价值为零。**

### 3.4 GKE：明确的过度设计

**[已核实]**：集群管理费 **每集群每小时统一 $0.10**（约 **$73/月**），按秒计费，**"适用于所有 GKE 集群，无论运行模式、集群规模或拓扑"**——单区域、多区域、区域级、Autopilot 一视同仁。免费层为**每结算账号每月 $74.40 抵扣额**，相当于一个免费的 Autopilot 或单区域 Standard 集群；**仅适用于单区域与 Autopilot 集群，不能用于区域级集群费用或计算/网络 SKU，且不滚存**。

**结论：单节点中转场景下 GKE 是决定性的过度设计。** 即使免费额度覆盖了一个单区域集群的控制面，节点 VM 仍需全额付费，而你换来的是 Kubernetes 运维复杂度和"Service/Ingress 对裸 TCP/UDP 需额外配 LoadBalancer"的麻烦。一旦需要第二个集群或区域级集群，就是每月 ~$73 去调度一个 Pod。**在节点数进入两位数且需跨区域自动调度之前，不应引入 GKE。**

### 3.5 网络层级：Premium vs Standard（本节最关键的决策）

> ⚠️ **方法论说明**：Google 官方文档中**从未出现 "cold potato / hot potato" 字样**。这两个标签是业界俗称。以下行为描述引自官方文档原文，标签是本文的归纳。

**Premium Tier（默认）** **[已核实]**：
- 入站：流量在**尽可能靠近用户**的 Google PoP 进入 Google 网络；Premium IP 的下一跳在**全球网络**以等价 BGP metric 通告。
- 出站：**经 Google 网络传输，在离目的地最近的 PoP 出网**。官方称此路由方式"最小化拥塞并通过减少跳数最大化性能"。
- SLA **99.99%**；支持全球外部 IP（anycast）、全部负载均衡类型、Cloud VPN、外部 IPv6。

**Standard Tier** **[已核实]**：
- 入站/出站：在**靠近该 Standard IP 所属区域**的对等或中转网络进出。
- 官方对代价的表述非常直白：**"最近的对等 PoP 可能不在该区域所在国家，且最近对等 PoP 的选择并不针对性能做优化"**；它"只在 Google 数据中心连到对等 PoP 这一段利用 Google 网络的双重冗余"。
- SLA **99.9%**；**不支持全球外部 IP、外部 IPv6、Cloud VPN**。

**出网定价对比** **[已核实]**：

Premium Tier 按**目的地**计价（**与源区域基本无关**，已通过切换 us-central1 / asia-east1 验证两者中国行完全一致）。分档边界为 **1,024 GiB（1 TiB）** 与 **10,240 GiB（10 TiB）**：

| 目的地 | 0–1 TiB | 1–10 TiB | 10 TiB+ |
| --- | --- | --- | --- |
| **中国大陆（不含香港）** | **$0.23/GiB** | **$0.22/GiB** | **$0.20/GiB** |
| 北美 | $0.12 | $0.11 | $0.08（源为 asia-east1 时 $0.085） |
| 欧洲 | $0.12 | $0.11 | $0.085 |
| 亚洲（不含韩国、印尼） | $0.12 | $0.11 | $0.085 |
| 澳洲/印尼/韩国/南美/沙特 | $0.19 | $0.18 | $0.15 |
| **入站（ingress）** | **免费** | | |

⚠️ **中国与澳洲两行没有任何免费额度。**

Standard Tier 按**源区域**计价，**不区分目的地**（中国方向与美国方向同价）**[已核实]**：

| 源区域 | 0–200 GiB | 200 GiB–10 TiB | 10–150 TiB | 150 TiB+ |
| --- | --- | --- | --- | --- |
| us-central1 | **免费** | $0.085/GiB | $0.065/GiB | $0.045/GiB |
| asia-east1 | **免费** | $0.11/GiB | $0.075/GiB | $0.07/GiB |
| asia-northeast1 | **免费** | $0.11/GiB | $0.075/GiB | $0.07/GiB |

**200 GiB 免费额度是真实的，但它是 Standard Tier 专属，不是通用 GCP 额度** **[已核实]**。自 2023-10-01 起，**每个区域独立计算，每月 200 GB**。官方原文提到覆盖 28+ 区域、合计约 6 TB 的潜在免费流量。**多区域部署时，免费额度按区域数量线性叠加**——这对拓扑设计有直接影响。

⚠️ **不可叠加**：定价页明确"Always Free 用量限制不适用于 Standard Tier"。Standard 的 200 GiB 与 Premium 的 1 GiB 是两套独立机制。

**三种"免费出网"极易混淆，务必区分** **[已核实]**：

| 额度 | 适用范围 | 对本项目价值 |
| --- | --- | --- |
| **1 GB/月** | GCE 免费层，源北美，**排除中国与澳洲**，Premium | **零** |
| **1 GiB/月** | Cloud Run，北美境内，Premium | 零 |
| **200 GiB/月/区域** | **Standard Tier 互联网出网，不限目的地** | **这是唯一有意义的一个** |

**那么中国方向该选哪个层级？**

官方文档**没有任何中国相关的质量指引**，也无 GFW 交互的官方说法 **[待核实]**。基于机制的推理：

- 从 **asia-east1（台湾）/ asia-northeast1（东京）** 出发，**源区域本身就紧邻中国**，Premium 与 Standard 的骨干差异在地理上很小——是"Google 骨干走一小段"与"区域中转走一小段"的区别。**Standard 的质量牺牲有限，而单价省一半以上。**
- 从 **us-central1** 出发差异很大：Premium 用 Google 骨干承载跨太平洋段，Standard 在美国中西部就交给公网——**而跨太平洋恰恰是最需要好骨干的一段**。

**推荐：asia-east1 + Standard Tier。** 这是成本与质量的最优点：$0.11/GiB、200 GiB 免费、且对中国用户 RTT 低。**但仍须 A/B 实测确认** **[需实测]**——同区域起两台同配置实例分设 Premium/Standard，从中国三网各测 24 小时 RTT/丢包/晚高峰吞吐。

⚠️ **Standard Tier 的架构约束必须提前知晓**：**不支持全球 anycast IP、外部 IPv6、Cloud VPN**，只能用区域级 IPv4。对单纯的中转节点通常无妨，但**它排除了任何多区域 anycast 设计**。

### 3.6 Google Cloud 服务条款：与 Cloudflare 形成鲜明对比

**[已核实]**：Google Cloud Acceptable Use Policy（`cloud.google.com/terms/aup`）全文中**完全没有提及 proxy、VPN、匿名化服务、隧道或网络中继**。其禁止清单为：侵犯他人合法权利；从事/推广非法活动；用于非法、侵入性、侵权、诽谤或欺诈目的；分发病毒木马等破坏性内容；未授权访问或干扰服务；篡改/绕过服务的任何方面；发送垃圾邮件；测试或逆向服务以发现其限制或漏洞、**或规避其过滤能力**（该条限定为规避 **Google 自身**的过滤，非第三方国家级过滤）；以违反其他 Google 产品条款的方式访问该产品。

**这与 Cloudflare §2.2.1(j) 形成决定性对比：运行中转服务本身并不违反 GCP 的 AUP。** 合法性问题落在用户与运营者所在司法辖区，而非平台条款。

> 本文不提供法律意见，仅陈述该文件写了什么、没写什么。

**这一条与 §2.2 合并起来，直接决定了 §7 的架构：数据面放 GCP（条款上干净），控制面放 Cloudflare（正当用法），两者不混。**

### 3.7 在 GCE 上部署 sing-box / Xray

以下 `gcloud` 参数名均已核实 **[已核实]**。启动脚本**以 root 身份、每次开机都会运行**。

**第一步（最省钱的一步）：把项目默认网络层级切到 Standard，解锁每区域 200 GiB 免费出网。**

```bash
gcloud compute project-info update --default-network-tier STANDARD

# 保留 Standard 层级的静态区域 IP。
# 强烈建议：临时 IP 会在实例 stop/start 时变更，而节点 IP 变更 = 全部订阅失效。
gcloud compute addresses create babel-relay-tw-ip \
  --project=oratis-491316 \
  --region=asia-east1 \
  --network-tier=STANDARD
```

**第二步：防火墙。** 按网络 tag 限定作用范围。REALITY 走 TCP:443；Hysteria2 需开 UDP 端口范围以支持端口跳跃。

```bash
gcloud compute firewall-rules create babel-relay-ingress \
  --project=oratis-491316 \
  --action=ALLOW --direction=INGRESS --network=default --priority=1000 \
  --rules=tcp:443,udp:443,udp:20000-30000 \
  --target-tags=babel-relay --source-ranges=0.0.0.0/0
```

**第三步：创建实例。**

```bash
gcloud compute instances create babel-relay-tw-1 \
  --project=oratis-491316 \
  --zone=asia-east1-b \
  --machine-type=e2-small \
  --image-family=debian-12 --image-project=debian-cloud \
  --boot-disk-size=10GB --boot-disk-type=pd-standard \
  --tags=babel-relay \
  --network-tier=STANDARD \
  --address=babel-relay-tw-ip \
  --metadata-from-file=startup-script=./startup.sh
```

`startup.sh`：

```bash
#!/bin/bash
set -euxo pipefail

# 内核调优：BBR + 扩大 UDP 缓冲区（Hysteria2 的实际吞吐对此极敏感）
cat >/etc/sysctl.d/99-babel.conf <<'SYSCTL'
net.core.default_qdisc=fq
net.ipv4.tcp_congestion_control=bbr
net.core.rmem_max=16777216
net.core.wmem_max=16777216
SYSCTL
sysctl --system

# 安装 sing-box —— 版本必须钉死，切勿取 latest
SB_VER="1.13.18"
curl -fsSLo /tmp/sb.tar.gz \
  "https://github.com/SagerNet/sing-box/releases/download/v${SB_VER}/sing-box-${SB_VER}-linux-amd64.tar.gz"
tar -xzf /tmp/sb.tar.gz -C /tmp
install -m 755 "/tmp/sing-box-${SB_VER}-linux-amd64/sing-box" /usr/local/bin/sing-box

# 配置从 Secret Manager 拉取（实例需绑定有 secretAccessor 角色的服务账号）
mkdir -p /etc/sing-box
gcloud secrets versions access latest --secret=babel-relay-config \
  > /etc/sing-box/config.json
chmod 600 /etc/sing-box/config.json

cat >/etc/systemd/system/sing-box.service <<'UNIT'
[Unit]
Description=sing-box
After=network-online.target
Wants=network-online.target

[Service]
ExecStart=/usr/local/bin/sing-box run -c /etc/sing-box/config.json
Restart=on-failure
RestartSec=5
LimitNOFILE=1048576

[Install]
WantedBy=multi-user.target
UNIT
systemctl daemon-reload
systemctl enable --now sing-box
```

排障与验证：

```bash
gcloud compute instances get-serial-port-output babel-relay-tw-1 \
  --zone=asia-east1-b | grep startup-script
gcloud compute ssh babel-relay-tw-1 --zone=asia-east1-b \
  --command="systemctl status sing-box; ss -tulnp"
```

**三条必须遵守的要点：**

1. **版本钉死。** `latest` 会导致重建实例时行为漂移；对 Xray 而言更严重——参见 §5.3.1 的 mihomo 兼容性断裂（且 v26.4.x–v26.7.28 均以 **prerelease** 发布，"取最新 release"的自动化会踩坑）。
2. **密钥走 Secret Manager，绝不写进 metadata。** 实例 metadata 对项目内任何有读权限的主体可见；UUID、Hysteria2 密码、REALITY 私钥写进去等于泄露。
3. **静态 IP 必须预留。** 节点 IP 变更即全部已下发订阅失效——这是运营事故，不是小问题。

> **关于 Spot 实例的修正意见**：Google 已取消旧的 24 小时抢占上限，Spot 实例**无最长运行时间限制**，但**可随时被抢占、不受 SLA 覆盖、默认抢占通知为 0 秒**（120 秒通知选项尚在 Preview），且**价格每日可变**。asia-east1 的 Spot 折扣高达约 52.8%，诱惑不小。**但对承载在线用户的中转节点仍不推荐**：抢占 = 全员瞬间掉线 + 出口 IP 变更 + 订阅失效。若要用 Spot，必须配合"多节点 + 客户端自动切换 + 静态 IP 池"，其复杂度成本高于每月省下的几美元。**初期用按需实例。**

---

## 4. 到中国的延迟与路由

> 本节几乎全部内容属 **[社区共识]**。跨境路由质量**没有官方 SLA、没有权威公开测量数据**，且**逐运营商、逐省份、逐时段变化**。**所有结论都必须以自建探针的长期实测为准** **[需实测]**。

### 4.1 中国骨干网基础

中国三大运营商各有"普通"与"精品"两套国际出口：

| 运营商 | 普通线路 | 精品线路 | 说明 |
| --- | --- | --- | --- |
| 中国电信 | **163 / ChinaNet（AS4134）** | **CN2 GT**（Global Transit）< **CN2 GIA**（Global Internet Access, **AS4809**） | GIA 为最高等级 |
| 中国联通 | **169（AS4837）** | **CUII / AS9929** | 9929 优于 4837 |
| 中国移动 | **AS9808** | **CMI / AS58453** | CMI 为国际精品 |

**为什么 CN2 GIA 在晚高峰差异巨大**：163 是消费级国际出口，**带宽被严重超售**；每晚约 **19:00–24:00（北京时间）**进入拥塞窗口，表现为丢包率飙升与 RTT 抖动激增，而非平均延迟上升。CN2 GIA 是独立的高优先级承载网，拥塞窗口内劣化远小于 163。

**关键事实：GCP / AWS / Azure 的亚洲区域都没有 CN2 GIA 接入。** 公有云的中国方向流量通常经由 **PCCW、NTT、Telia、HE、以及电信 163** 等公开对等/中转到达。**这意味着"选 GCP 哪个区域"这个问题，其上限被公有云的对等策略锁死了**——不可能通过换区域获得 CN2 GIA 级别的体验。

**关于 GFW 是否对国际链路做蓄意 QoS 限速**：社区中"蓄意限速"与"纯粹超售拥塞"两种解释都很流行，**本次未找到可引用的实证研究** **[需实测/待核实]**。运营上无需区分——两种成因导出的应对措施相同。

### 4.2 GCP 区域选择

| 区域 | 物理位置 | 对中国用户的评价 |
| --- | --- | --- |
| `asia-east1` | 台湾彰化 | 地理最近之一，社区常用；**具体路由质量待实测** |
| `asia-east2` | 香港 | 延迟通常最低，**但香港出口是拥塞与封锁的重灾区**，且是 GFW 重点关照方向 |
| `asia-northeast1` | 东京 | 中国到日本海缆质量通常较稳；常被认为是较平衡的选择 |
| `asia-southeast1` | 新加坡 | 绕行距离长，延迟劣于台/日 |
| `us-west1` | 俄勒冈 | 延迟最差，**但 e2-micro 免费层在美区** |

（区域物理位置查 `cloud.google.com/about/locations`；上述对中国的质量评价**全部为 [社区共识]，需实测**。）

**一个反直觉但重要的点**：`asia-east2`（香港）**延迟低不等于体验好**。香港方向国际出口在晚高峰的拥塞程度往往超过日本方向，且香港 IP 段被 GFW 针对性处理的强度更高。**低 RTT 与高丢包同时存在时，实际体验由丢包主导**——这也正是 Hysteria2 的 Brutal 拥塞控制在中国场景有价值的原因（它不因丢包退让）。

**GCP IP 段是否被 GFW 整段封锁/降级**：社区长期有报告，但**无权威结论** **[待核实]**。运营上的应对是**准备可快速更换的 IP 池**，而不是赌某个 IP 长期存活。

### 4.3 Cloudflare + 中国的现实

- Free / Pro / Business **在中国大陆无 PoP**（见 §2.7，**已核实**），中国用户必然出境访问境外 Cloudflare 节点。
- **具体命中哪个 PoP —— 待核实。** 用 `curl https://<你的域名>/cdn-cgi/trace` 看 `colo=` 字段实测。
- **"优选 IP"实践** **[社区共识]**：Cloudflare 的 anycast 从中国出发时选路经常不理想（可能被导到远端 PoP），社区因此发展出批量测速 Cloudflare IP 段、挑选低延迟低丢包 IP 并固定使用的做法，常用工具为 `XIU2/CloudflareSpeedTest`。**这本质上是在对抗 anycast 的选路决策**，属于典型的"能用但脆弱"手段——IP 池会变、测速结果会过期，需要持续自动化重测。

### 4.4 拓扑权衡：这才是真正的决策

"中国客户端 → Cloudflare → GCP"这条路径的延迟由三段串联构成：

```
[中国客户端] --(a) 中国国际出口，晚高峰瓶颈--> [境外 CF PoP]
             --(b) CF 骨干 --> [CF 出口 PoP]
             --(c) CF → GCP --> [GCP 实例] --> [目标站点]
```

**(a) 是瓶颈，(b)(c) 是额外增加的固定开销。** CDN 前置**不能改善 (a)**——它改善的是**抗封锁性**（CF 的 IP 池与正常业务共享，整段封锁的附带损害极高），代价是**多两跳、更高抖动、更高 P99**。

三种主流拓扑的取舍：

| 拓扑 | 抗封锁 | 延迟 | 成本 | 说明 |
| --- | --- | --- | --- | --- |
| **直连**（REALITY / Hysteria2 打 GCP IP） | 低（IP 一旦被标记即失效） | **最好** | 低 | 体验上限最高；需 IP 轮换预案 |
| **CDN 前置**（CF → GCP） | **高** | 差（多跳 + 抖动） | 极低（CF 不收带宽费） | **但违反 CF ToS** |
| **中转**（中国侧 CN2 GIA 落地机 → GCP） | 中 | **最好且晚高峰稳定** | **高**（GIA 带宽昂贵） | 机场市场的主流付费方案；本项目未评估其合规与采购路径 |

**核心洞察：CDN 前置和直连解决的不是同一个问题。** 直连优化体验，CDN 前置优化生存性。**它们应该同时存在于产品中，作为不同优先级的通路，而不是二选一。**

---

## 5. 节点与订阅格式

### 5.1 事实标准概览

| 格式 | 载体 | 消费方 | 备注 |
| --- | --- | --- | --- |
| **Base64 订阅** | 换行分隔的 URI 列表整体 base64 | 几乎所有客户端 | 最通用的最小公分母 |
| **Clash / mihomo YAML** | `proxies` / `proxy-groups` / `rules` | mihomo 系客户端 | 表达力最强，可下发路由规则 |
| **sing-box JSON** | 完整 config 或远程 profile | sing-box 系 | 表达力强，与 mihomo YAML 不兼容 |
| **Shadowrocket** | 接受 base64 订阅 | iOS | 有自有扩展参数 |

**订阅 HTTP 响应头**（机场生态事实标准，**非任何官方规范**，但 `subscription-userinfo` 的解析行为已从 mihomo 源码核实 **[已核实]**）：

```http
Subscription-Userinfo: upload=1638257504; download=13418441583; total=1073839341568; expire=1791390742
Profile-Update-Interval: 24
Content-Disposition: attachment; filename*=UTF-8''babel.plus
```

- `upload` / `download` / `total` 单位为**字节**；`expire` 为**秒级 Unix 时间戳**，`0` 或缺省表示永不过期。
- mihomo 的解析器（`adapter/provider/subscription_info.go`）按**分号分隔的 `key=value`** 解析，**键名大小写不敏感、空格被剥除**，值为 int64（浮点会被截断）**[已核实]**。
- Clash Verge Rev 匹配**任何以 `subscription-userinfo` 结尾**的响应头（前缀为空或以 `-` 结尾），因此 `x-amz-meta-subscription-userinfo` 也有效——**若从对象存储直接分发订阅，这一点很有用** **[已核实]**。
- ⚠️ **`Profile-Update-Interval` 的单位是「小时」**，不是分钟（Clash Verge Rev 源码内部乘 60 转为分钟）**[已核实]**。写 `24` 表示每日更新。**此头未在 mihomo 内核中出现，是 GUI 客户端约定**，其他客户端可能有不同解释 **[需实测]**。
- Clash Verge Rev 会校验 YAML 中必须含 `proxies` **或** `proxy-providers`，否则拒绝该配置 **[已核实]**。

**sing-box 的远程订阅是另一套机制** **[已核实]**：sing-box 原生支持 Remote Profile，导入 URL 格式为 `sing-box://import-remote-profile?url=<urlEncodedURL>#<urlEncodedName>`，默认 60 分钟自动更新，要求客户端实现 HTTP Basic 认证。**关键差异：sing-box 远程 profile 是一份完整的 sing-box JSON 配置，而不是节点列表**；内核中**没有 `proxy-providers` 的对应物，也没有任何流量配额响应头的定义**。因此**流量额度展示无法通过 sing-box 原生 profile 传达**，须在面板侧解决。

**subconverter 生态** **[已核实]**：

- `tindy2013/subconverter`（16,988★，末次 push 2026-07-09）是事实标准后端，接口 `/sub?target=<target>&url=<urlencoded>`。**但上游没有 `singbox` target**，且对 hy2 / tuic / anytls 支持滞后。
- 维护更积极的分支 `asdlokj1qpi233/subconverter` 补齐了 anytls / hy2 / vless 对 sing-box 与 Clash.Meta 的输出。
- `sub-store-org/Sub-Store`（10,289★，末次 push 2026-08-16）**不是 subconverter 的前端而是独立且更强的工具**，**原生支持 sing-box 输出目标**，并可聚合多订阅。
- `CareyWang/sub-web` 是 subconverter 的经典 Vue 前端。

> **架构建议（不变，且现在理由更强）**：**不要依赖任何第三方 subconverter 实例**——用户的节点凭据（UUID、REALITY 私钥对应的公钥、Hysteria2 密码）会明文经过它。babel.plus 应**自建订阅生成服务**，从内部节点清单直接渲染 base64 / mihomo YAML / sing-box JSON 三种目标。这既是安全要求，也是产品控制点，还顺带绕开了上游 subconverter 缺 sing-box target 的问题。

### 5.2 分享链接标准

VLESS / VMess 的分享链接格式来自 **Xray-core Discussion #716「VMessAEAD / VLESS 分享链接标准提案」** **[已核实]**（该文档 2026-03-27 仍在修订）。这是社区提案而非 RFC，但已是事实标准。

通用形状：`protocol://$(uuid)@remote-host:remote-port?<协议字段><传输字段><TLS字段>#$(描述文本)`

标准明文规定的三条规则 **[已核实]**：**字段顺序无关**；**禁止重复字段**；**所有值必须经 `encodeURIComponent` 转义**；**所有参数名与常量字符串大小写敏感**。

VLESS + REALITY 的 URI 骨架：

```
vless://<uuid>@<host>:<port>?type=raw&security=reality
  &sni=<serverName>&fp=chrome&pbk=<password/公钥>&sid=<shortId>&spx=<spiderX>
  &flow=xtls-rprx-vision#<节点名称>
```

关键参数映射 **[已核实]**：

| 参数 | 映射到 | 说明 |
| --- | --- | --- |
| `type` | `streamSettings.network` | `tcp`/`kcp`/`ws`/`http`/`grpc`/`httpupgrade`/`xhttp` |
| `security` | 传输安全 | `none`（默认）/`tls`/`reality` |
| `flow` | VLESS flow | `xtls-rprx-vision`（2026-01-18 加入标准） |
| `fp` | uTLS `fingerprint` | 默认 `chrome`；**用 REALITY 时不得省略** |
| `sni` | `serverName` | 默认取 `remote-host` |
| **`pbk`** | REALITY **`password`**（原 `publicKey`） | 必填且非空 |
| `sid` | REALITY `shortId` | 可为空 |
| `spx` | REALITY `spiderX` | 可为空 |
| `pqv` | REALITY `mldsa65Verify` | 2025-07-22 加入 |
| `ech` | `echConfigList` | 2025-07-26 加入 |
| `pcs` / `vcn` | `pinnedPeerCertSha256` / `verifyPeerCertByName` | 2026-01-18 加入，**取代 `allowInsecure`** |
| `alpn` | `alpn` | 逗号分隔，**不含空格** |
| `mode` / `extra` | XHTTP | `extra` 是 URL 编码的 JSON 串 |

标准明确指出的坑 **[已核实]**：

- **`alterId` / `aid` 不存在于本标准**——它只覆盖 VMessAEAD 与 VLESS。
- **`allowInsecure` 从未进入标准，且 Xray-core 已移除该配置项。**
- **并非每个 URL 都含 `?`**，解析器必须处理这一边界情况。

⚠️ **`vmess://` 的 base64-JSON 形式（`v/ps/add/port/id/aid/scy/net/type/host/path/tls/sni/alpn/fp`）没有任何官方规范** ——它源自 v2rayN 的约定，与上述标准中定义的"`vmess://` 为普通 URL"是**两套并存的东西**。**`tuic://` 则完全没有官方 URI 规范** **[已核实：不存在]**，各客户端行为自定。这两者在实现订阅生成器时必须按目标客户端分别适配，不能假定通用。

### 5.3 ⚠️ 重大变更：Xray 配置字段已改名

**这是本次调研中最容易踩坑的发现** **[已核实，来自 `xtls.github.io` 官方文档]**。Xray 近期版本对配置字段做了不兼容改名，**网上绝大多数教程仍在用旧名**：

| 位置 | 旧字段名 | **新字段名** |
| --- | --- | --- |
| `streamSettings` | `network` | **`method`** |
| `streamSettings.method` 取值 | `tcp`（+ `tcpSettings`） | **`raw`**（+ `rawSettings`） |
| `settings`（VLESS inbound） | `clients` | **`users`** |
| `realitySettings`（inbound） | `dest` | **`target`** |
| `realitySettings`（outbound） | `publicKey` | **`password`** |

旧名**仍作为别名被接受**（源码中有 `if c.Clients != nil { c.Users = c.Clients }`、`if c.Target != nil { c.Dest = c.Target }` 等兼容分支），因此旧配置不会立刻报错——**这恰恰是危险之处：错误是静默的**。

`streamSettings.method` 的合法取值为：`raw` / `xhttp` / `mkcp` / `grpc` / `websocket` / `httpupgrade` / `hysteria`。

**`publicKey` → `password` 改名的理由值得理解**：该字段确实是一个 x25519 公钥，但在 REALITY 的设计中它是**客户端持有的秘密**——叫它 "publicKey" 诱导用户随意分享，而**持有它即可探测 REALITY 服务器**。这是一个安全性改名，不是美学改名。

**另外两个已被移除/变更的点：**

- **`allowInsecure` 已从 Xray 配置中移除**，且它从未进入分享链接标准。替代品是 `pcs`（`pinnedPeerCertSha256`）与 `vcn`（`verifyPeerCertByName`）。大量面板仍在发 `allowInsecure`，应视为遗留字段。
- **`xray x25519` 的标准输出格式已变更**，现为三行：`PrivateKey:` / `Password (PublicKey):` / `Hash32:`（默认 base64 RawURL 无填充编码，`--std-encoding` 切标准 base64）。`Hash32` 是 BLAKE3-256 公钥摘要，**用于 VLESS Encryption 中继，与 REALITY 无关**，切勿误填。**任何抓取旧的 `Public key:` 字样的自动化脚本都已失效。**

### 5.3.1 ⚠️ 最高优先级运营约束：mihomo 已放弃与新版 Xray 的 REALITY 互通

mihomo 官方文档中有一句必须被产品决策吸收的声明 **[已核实]**：

> "Due to xray-core's deliberately incompatible behavior, we will not consider compatibility with xray v26.7.11+ versions."

即：**若服务端 Xray ≥ v26.7.11，mihomo 系客户端将无法连接 REALITY 节点**，且 mihomo 明确表示不会修复，建议改用 sing-box / mihomo 原生 listener / 旧版 Xray 或换协议。

**对 babel.plus 的直接影响**（mihomo 系覆盖 Clash Verge Rev、Stash 等大量用户）：

1. **服务端 Xray 必须钉死在 v26.3.27**（当前最新非预发布版），**绝不可自动升级**。注意 v26.4.x–v26.7.28 均以 **prerelease** 发布——任何"取 latest release"的自动化都可能踩进预发布版。
2. 或者**服务端改用 sing-box 承载 REALITY**，从根上绕开这个 Xray↔mihomo 的兼容性战场。**鉴于本项目已选 sing-box 为主要服务端（§3.5 启动脚本），这是更稳妥的路线。**
3. 无论选哪条，**发布前必须用真实 mihomo 客户端回归测试 REALITY 连通性**。

**REALITY 新增后量子服务端认证**：inbound 支持 `mldsa65Seed`，outbound 对应 `mldsa65Verify`，由 `xray mldsa65` 生成 **[已核实]**。这与 §1.2 的 VLESS Encryption（ML-KEM-768）是两个独立的后量子机制——前者认证服务端身份，后者保护会话密钥。

**密钥生成命令** **[已核实]**：

```bash
xray x25519                      # 生成 x25519 密钥对
xray x25519 -i "<私钥>"          # 从私钥反推公钥
xray mldsa65                     # 生成 ML-DSA-65 密钥对（后量子服务端认证）
openssl rand -hex 8              # 生成 shortId（1-8 字节的偶数长度 hex）

sing-box generate reality-keypair
sing-box generate rand 8 --hex
```

### 5.4 配置示例

> **以下片段按官方文档字段名撰写，但本次未逐条在真机启动验证。部署前必须对照所钉版本的官方文档校验，并实际启动确认 **[需实测]**。** sing-box 1.11 → 1.12 → 1.13 存在字段废弃与结构调整（例如 WireGuard 由 `outbounds` 迁移至 `endpoints`），跨版本复制配置极易出错。

#### 5.4.1 sing-box — VLESS + REALITY + XTLS-Vision（outbound）

```json
{
  "outbounds": [
    {
      "type": "vless",
      "tag": "babel-reality",
      "server": "203.0.113.10",
      "server_port": 443,
      "uuid": "00000000-0000-0000-0000-000000000000",
      "flow": "xtls-rprx-vision",
      "tls": {
        "enabled": true,
        "server_name": "www.cloudflare.com",
        "utls": {
          "enabled": true,
          "fingerprint": "chrome"
        },
        "reality": {
          "enabled": true,
          "public_key": "<REALITY_PUBLIC_KEY>",
          "short_id": "0123456789abcdef"
        }
      }
    }
  ]
}
```

#### 5.4.2 sing-box — Hysteria2（outbound）

```json
{
  "outbounds": [
    {
      "type": "hysteria2",
      "tag": "babel-hy2",
      "server": "203.0.113.10",
      "server_port": 443,
      "up_mbps": 100,
      "down_mbps": 500,
      "password": "<PASSWORD>",
      "obfs": {
        "type": "salamander",
        "password": "<OBFS_PASSWORD>"
      },
      "tls": {
        "enabled": true,
        "server_name": "babel.plus",
        "alpn": ["h3"]
      }
    }
  ]
}
```

要点 **[已核实，sing-box v1.13.18 文档]**：

- **`tls` 对 hysteria2 是必填项。**
- `up_mbps` / `down_mbps` **留空则改用 BBR 拥塞控制**，而非 Hysteria 自有的 Brutal。想要 Brutal 的抗丢包特性就**必须显式填写带宽**。
- ⚠️ **`obfs.type` 存在版本分歧**：v1.13.18 文档只承认 `salamander`；开发线（1.14）文档同时列出 `gecko`。**若钉在 1.13 稳定版，只能下发 `salamander`。**
- ⚠️ **互通陷阱（订阅生成器必须处理）**：官方 Hysteria2 有一种 **userpass** 认证方式，实质是把 `<username>:<password>` 整体当作密码；**sing-box 不提供这个别名**。因此把 `hysteria2://user:pass@host/` 转成 sing-box 配置时，**必须输出 `"password": "user:pass"`**。
- 端口跳跃：`server_ports: ["20000:30000"]` + `hop_interval`（均自 **1.11.0** 起），`server_ports` 与 `server_port` **互斥**。`hop_interval_max` 是 1.14+ 专有。
- 生产环境**不要**设 `tls.insecure: true`。它会让客户端接受任意证书，使 §1.1 的对抗模型形同虚设。

#### 5.4.3 mihomo / Clash.Meta — VLESS + REALITY + Vision

```yaml
proxies:
  - name: "babel-reality-tw"
    type: vless
    server: 203.0.113.10
    port: 443
    uuid: 00000000-0000-0000-0000-000000000000
    network: tcp
    udp: true
    tls: true
    flow: xtls-rprx-vision
    servername: www.cloudflare.com
    client-fingerprint: chrome
    reality-opts:
      public-key: "<REALITY_PUBLIC_KEY>"
      short-id: "0123456789abcdef"
```

⚠️ **两个极易混淆的字段** **[已核实]**：`client-fingerprint` 是 **uTLS 指纹**（取值 `chrome` / `firefox` / `safari` / `iOS` / `android` / `edge` / `360` / `qq` / `random`，注意 `iOS` 的大小写）；而 `fingerprint` 是**证书 SHA-256 pin**，是完全不同的东西。**写错会导致难以排查的连接失败。**

`reality-opts` 另支持 `support-x25519mlkem768: true` 启用后量子密钥交换。mihomo 的 `xtls-*` flow 行为**等价于 Xray 的 `xtls-*-udp443`**（即不接管 UDP 443）**[已核实]**。`udp: true` 必须显式写——mihomo 对多数协议默认 `udp: false`。

#### 5.4.4 mihomo / Clash.Meta — Hysteria2

```yaml
proxies:
  - name: "babel-hy2-tw"
    type: hysteria2
    server: 203.0.113.10
    port: 443
    password: "<PASSWORD>"
    up: "100 Mbps"
    down: "500 Mbps"
    obfs: salamander
    obfs-password: "<OBFS_PASSWORD>"
    sni: babel.plus
    skip-cert-verify: false
    alpn:
      - h3
```

#### 5.4.5 mihomo — AnyTLS（字段名已核实）

`mihomo` 的 AnyTLS 字段为：`name` / `type` / `server` / `port` / `password` / `client-fingerprint` / `udp` / `client-metadata` / `idle-session-check-interval` / `idle-session-timeout` / `min-idle-session` / `sni` / `alpn` / `skip-cert-verify`，并可与 `shadow-tls-opts` / `restls-opts` / `jls-opts` 组合 **[已核实]**。

**mihomo 明确声明不支持且永不支持 AnyTLS + REALITY**，需隐藏 SNI 请改用 ECH / ShadowTLS / RestTLS / JLS **[已核实]**。

#### 5.4.6 Xray-core — VLESS + REALITY 服务端 inbound

```json
{
  "inbounds": [
    {
      "listen": "0.0.0.0",
      "port": 443,
      "protocol": "vless",
      "settings": {
        "users": [
          {
            "id": "00000000-0000-0000-0000-000000000000",
            "flow": "xtls-rprx-vision",
            "email": "user@babel.plus"
          }
        ],
        "decryption": "none"
      },
      "streamSettings": {
        "method": "raw",
        "security": "reality",
        "realitySettings": {
          "show": false,
          "target": "www.cloudflare.com:443",
          "xver": 0,
          "serverNames": ["www.cloudflare.com"],
          "privateKey": "<REALITY_PRIVATE_KEY>",
          "shortIds": ["0123456789abcdef"]
        }
      }
    }
  ]
}
```

> 注意此处已使用新字段名 `users` / `method` / `raw` / `target`（见 §5.3）。**若所钉的 Xray 版本较旧，须改回 `clients` / `network` / `tcp` / `dest`** —— 这正是必须钉死版本并按版本校验配置的原因。
>
> **REALITY 只能与 `raw` / `xhttp` / `grpc` 三种传输组合，明确不支持 `websocket` / `httpupgrade` / `mkcp`** **[已核实]**。这再次印证 §1.2 的结论：REALITY 与 CDN 路径互斥。
>
> `shortIds` 为 8 字节即 16 位 hex，也可更短但**长度必须为偶数**（自动补零），**允许空字符串**。⚠️ sing-box 文档写作"zero to eight digits"，与 Xray 的"16 hex 字符"字面矛盾（sing-box 几乎肯定指字节数）。**跨内核互通时须实测** **[需实测]**。
>
> REALITY 的 `target` 站点选择标准：支持 **TLS 1.3 与 HTTP/2**、**无跳转**、**境外**、**非自家域名**、且**在中国可正常访问**（否则伪装本身就露馅）。可用 `xray tls ping <host>` 预检目标站证书。

---

## 6. 参考来源

### 学术研究（一手）

- Alice, Bob, Carol, et al. *How China Detects and Blocks Shadowsocks*. IMC 2020. https://gfw.report/publications/imc20/en/ ／ https://dl.acm.org/doi/10.1145/3419394.3423644
- Xue, Diwen; Kallitsis, Michalis; Houmansadr, Amir; Ensafi, Roya. *Fingerprinting Obfuscated Proxy Traffic with Encapsulated TLS Handshakes*. USENIX Security 2024. https://www.usenix.org/conference/usenixsecurity24/presentation/xue-fingerprinting
- Zohaib, et al. *Exposing and Circumventing SNI-based QUIC Censorship of the Great Firewall of China*. USENIX Security 2025. https://gfw.report/publications/usenixsecurity25/en/ ／ https://www.usenix.org/conference/usenixsecurity25/presentation/zohaib ／ 复现工件：https://github.com/gfw-report/usenixsecurity25-quic-sni
- Wang, Gaukas, et al. *Chasing Shadows: A security analysis of the ShadowTLS proxy*. FOCI 2023. https://www.petsymposium.org/foci/2023/foci-2023-0002.pdf

### 协议与实现（官方）

- XTLS/REALITY — https://github.com/XTLS/REALITY
- Xray-core Releases — https://github.com/XTLS/Xray-core/releases
- Xray VLESS 配置 — https://xtls.github.io/en/config/outbounds/vless.html
- Xray Transport 总览 — https://xtls.github.io/en/config/transport.html
- Xray REALITY 传输配置 — https://xtls.github.io/en/config/transports/reality.html
- Xray XHTTP 传输配置 — https://xtls.github.io/en/config/transports/xhttp.html
- XHTTP: Beyond REALITY（设计讨论）— https://github.com/XTLS/Xray-core/discussions/4113
- VLESS 后量子加密 PR #5067 — https://github.com/XTLS/Xray-core/pull/5067
- VMessAEAD / VLESS 分享链接标准提案 — https://github.com/XTLS/Xray-core/discussions/716
- Hysteria 2 — CDN 兼容性说明 — https://v2.hysteria.network/docs/misc/CDN/
- Hysteria 2 — 服务端完整配置 — https://v2.hysteria.network/docs/advanced/Full-Server-Config/
- Hysteria 2 — 客户端完整配置 — https://v2.hysteria.network/docs/advanced/Full-Client-Config/
- TUIC v5 协议规范 — https://github.com/EAimTY/tuic/blob/dev/SPEC.md
- sing-box — ShadowTLS inbound — https://sing-box.sagernet.org/configuration/inbound/shadowtls/
- sing-box — VLESS outbound — https://sing-box.sagernet.org/configuration/outbound/vless/
- sing-box — Hysteria2 outbound — https://sing-box.sagernet.org/configuration/outbound/hysteria2/
- mihomo — AnyTLS — https://wiki.metacubex.one/en/config/proxies/anytls/
- mihomo — VLESS — https://wiki.metacubex.one/config/proxies/vless/
- mihomo — Hysteria2 — https://wiki.metacubex.one/config/proxies/hysteria2/
- Shadowsocks SIP002（`ss://` URI 格式）— https://shadowsocks.org/doc/sip002.html
- Shadowsocks SIP022（AEAD-2022）— https://shadowsocks.org/doc/sip022.html
- Shadowsocks 2022 Edition 规范 — https://github.com/Shadowsocks-NET/shadowsocks-specs/blob/main/2022-1-shadowsocks-2022-edition.md
- Hysteria 2 URI Scheme（`hysteria2://`）— https://v2.hysteria.network/docs/developers/URI-Scheme/
- AnyTLS URI Scheme（`anytls://`）— https://github.com/anytls/anytls-go/blob/main/docs/uri_scheme.md
- Trojan-Go URL 规范（`trojan://` 向后兼容说明）— https://p4gefau1t.github.io/trojan-go/developer/url/
- sing-box 客户端通用规范（Remote Profile / 导入 URL）— https://github.com/SagerNet/sing-box/blob/v1.13.18/docs/clients/general.md
- sing-box 迁移指南与废弃清单 — https://sing-box.sagernet.org/migration/

### 订阅与客户端生态

- tindy2013/subconverter — https://github.com/tindy2013/subconverter
- asdlokj1qpi233/subconverter（支持 anytls/hy2/vless → sing-box 的维护分支）— https://github.com/asdlokj1qpi233/subconverter
- Sub-Store（原生支持 sing-box 输出目标）— https://github.com/sub-store-org/Sub-Store ／ Wiki: https://github.com/sub-store-org/Sub-Store/wiki
- CareyWang/sub-web — https://github.com/CareyWang/sub-web
- v2rayN（协议枚举来源 `ServiceLib/Enums/EConfigType.cs`）— https://github.com/2dust/v2rayN
- Clash Verge Rev（`subscription-userinfo` / `profile-update-interval` 解析来源 `src-tauri/src/config/prfitem.rs`）— https://github.com/clash-verge-rev/clash-verge-rev
- Throne（NekoRay 桌面版继任者）— https://github.com/throneproj/Throne
- MatsuriDayo/nekoray（**已归档**）— https://github.com/MatsuriDayo/nekoray
- NekoBox for Android（含 Play 商店版第三方控制警告）— https://github.com/MatsuriDayo/NekoBoxForAndroid
- Hiddify-app — https://github.com/hiddify/hiddify-app
- Karing（基于 sing-box fork）— https://github.com/KaringX/karing

### Cloudflare（官方）

- Self-Serve Subscription Agreement（§2.2.1(j) 所在）— https://www.cloudflare.com/terms/
- ToS 更新公告（2.8 条删除，2023-05-16）— https://blog.cloudflare.com/updated-tos/
- Workers 定价 — https://developers.cloudflare.com/workers/platform/pricing/
- Workers 限制 — https://developers.cloudflare.com/workers/platform/limits/
- Workers TCP Sockets API — https://developers.cloudflare.com/workers/runtime-apis/tcp-sockets/
- 可代理网络端口 — https://developers.cloudflare.com/fundamentals/reference/network-ports/
- gRPC 支持 — https://developers.cloudflare.com/network/grpc-connections/
- Cloudflare Tunnel — https://developers.cloudflare.com/cloudflare-one/connections/connect-networks/
- Spectrum — https://developers.cloudflare.com/spectrum/
- China Network — https://developers.cloudflare.com/china-network/

### Google Cloud（价格与限制于 2026-08-16 从以下页面核实）

> 注意：Google 已将文档迁至 `docs.cloud.google.com`（旧 `cloud.google.com/*/docs/*` 会 301 过去）；**定价页仍在 `cloud.google.com`**。定价表由 JS 按区域渲染，需操作区域选择器才能读到分区域数字。

- 免费层功能清单（e2-micro 区域、1 GB 出网）— https://docs.cloud.google.com/free/docs/free-cloud-features
- **VPC 网络（出网）定价** — https://cloud.google.com/vpc/network-pricing
- **网络服务层级定价** — https://cloud.google.com/network-tiers/pricing
- 网络服务层级概述（Premium/Standard 路由行为）— https://docs.cloud.google.com/network-tiers/docs/overview
- Standard Tier 200 GB 免费额度公告 — https://cloud.google.com/blog/products/networking/standard-tier-network-now-includes-200-gb-data-transfer-per-month
- 通用型机型定价（E2 共享核）— https://cloud.google.com/products/compute/pricing/general-purpose
- Spot VM 定价 — https://cloud.google.com/spot-vms/pricing
- Spot VM 说明（抢占通知、无最长运行时间、不适用免费层）— https://docs.cloud.google.com/compute/docs/instances/spot
- Cloud Run 定价 — https://cloud.google.com/run/pricing
- Cloud Run 请求超时 — https://docs.cloud.google.com/run/docs/configuring/request-timeout
- Cloud Run WebSocket — https://docs.cloud.google.com/run/docs/triggering/websockets
- Cloud Run gRPC — https://docs.cloud.google.com/run/docs/triggering/grpc
- Cloud Run HTTP/2 — https://docs.cloud.google.com/run/docs/configuring/http2
- Cloud Run 容器契约（TLS 终止、worker pools TCP）— https://docs.cloud.google.com/run/docs/container-contract
- Cloud Run 计费模式（实例计费 vs 请求计费）— https://docs.cloud.google.com/run/docs/configuring/billing-settings
- GKE 定价 — https://cloud.google.com/kubernetes-engine/pricing
- **Google Cloud 可接受使用政策（AUP）** — https://cloud.google.com/terms/aup
- 启动脚本（Linux）— https://docs.cloud.google.com/compute/docs/instances/startup-scripts/linux
- 防火墙规则 — https://docs.cloud.google.com/vpc/docs/using-firewalls
- 使用网络服务层级 — https://docs.cloud.google.com/network-tiers/docs/using-network-service-tiers
- 全球区域与位置 — https://cloud.google.com/about/locations

### 社区来源（**[社区共识]**，非权威）

- net4people/bbs #528 — 2025 年协议可用性求证 — https://github.com/net4people/bbs/issues/528
- net4people/bbs #546 — 俄罗斯 TLS 管控致 VLESS 变体失效 — https://github.com/net4people/bbs/issues/546
- net4people/bbs #295 — 1.1.1.1 在中国被封（2023-10-01 起）— https://github.com/net4people/bbs/issues/295
- WireGuard 邮件列表 — 中国封锁与 swgp-go 用户态混淆 — https://lists.zx2c4.com/pipermail/wireguard/2022-June/007638.html
- XIU2/CloudflareSpeedTest（优选 IP 工具）— https://github.com/XIU2/CloudflareSpeedTest
- tindy2013/subconverter — https://github.com/tindy2013/subconverter
- Sub-Store — https://github.com/sub-store-org/Sub-Store

---

## 选型建议

### 7.1 必须先解决的问题：Cloudflare ToS 冲突

技术选型无法绕开这一点，所以先讲它。

**事实陈述：把 babel.plus 的中转数据面放在 Cloudflare 上（无论是 Workers/edgetunnel、CDN 反代 WS/XHTTP、还是 gRPC over CDN），都明确违反 Cloudflare Self-Serve Subscription Agreement §2.2.1(j)** —— 该条禁止"使用本服务提供 VPN 或其他类似代理服务"。付费不解除该限制，且 Cloudflare 保留不经通知停用的权利。

**风险敞口的量化**（这是决策的关键——风险不止于"节点掉线"）：

- 处置是**账号级**的，不是单 zone 级。
- 同一 Cloudflare 账号下的**所有域名、DNS、邮件路由、Pages 站点、R2 存储**可能一并失效。
- **DNS 托管一旦被停，域名本身即刻不可解析** —— 这会同时打掉官网、支付回调、订阅接口、以及所有依赖域名的客户端配置。
- **不经通知**意味着没有迁移窗口。
- 恢复不确定，且**申诉不占理**。

换句话说：**把公司的主域名和中转数据面放在同一个 CF 账号下，等于把整个业务的存活押在一条你正在违反的条款上。**

### 7.2 推荐架构

**推荐方案：以直连为主数据面，Cloudflare 严格限制在控制面。**（即任务描述中的选项 (b)，并吸收 (c) 的应急设计）

理由不是保守，而是三条独立论证指向同一结论：

1. **合规**：控制面（官网、订阅 API、面板）是**完全正当**的 Cloudflare 用法，不触及 §2.2.1(j)。数据面不上 CF，就消除了账号级停用风险。
2. **性能**（§4.4）：CDN 前置**改善不了**中国国际出口这个真实瓶颈，却确定地增加两跳和抖动。**为一个不解决瓶颈的方案承担 ToS 风险，性价比是负的。**
3. **可靠性**（§2.4）：Workers 运行时每周更新数次、在途请求仅 30 秒宽限期，**长连隧道必然被周期性掐断**。这对付费产品是硬伤。

具体分工：

| 平面 | 承载 | 平台 |
| --- | --- | --- |
| **控制面** | 官网、用户面板、订阅生成 API、计费、节点健康上报 | **Cloudflare（Pages + Workers）+ Cloud Run**，完全合规 |
| **数据面** | 中转流量 | **GCE 实例直连，不经 Cloudflare** |

**并且：数据面与控制面使用两个完全隔离的 Cloudflare 账号与两个不同域名。** 即使将来因任何原因在 CF 上做了应急承载，爆炸半径也被限制在一个一次性账号内，主域名与支付链路不受牵连。

### 7.3 推荐协议栈

| 优先级 | 协议栈 | 承担角色 |
| --- | --- | --- |
| **主力** | **VLESS + XTLS-Vision + REALITY / TCP:443** | 默认通路。抗主动探测最强（回落真实站点）、生态最大、客户端覆盖最全、日常稳定性最好 |
| **加速备选** | **Hysteria2 + salamander obfs + 端口跳跃** | 晚高峰、高丢包链路、以及 TCP 通路被降级时切换。Brutal 拥塞控制不因丢包退让，是丢包场景的唯一解 |
| **应急兜底** | **VLESS + XHTTP over Cloudflare CDN**（`stream-up` 模式） | **默认关闭**，仅在直连 IP 被封且尚未完成 IP 轮换时由服务端下发启用。置于隔离的一次性 CF 账号 |
| **明确排除** | VMess（冗余且特征明显）、Trojan-Go（2021 年后无发布）、TUIC v5（SNI 暴露于 QUIC 审查且无 masquerade）、裸 SS-2022（高熵首包易筛）、裸 WireGuard（无抗审查设计） | — |
| **观察名单** | **AnyTLS** | 唯一直接针对 TLS-in-TLS 论文设计填充方案的协议。待实网数据积累后再考虑升格 |

**为什么是 REALITY 主 + Hysteria2 副，而不是反过来**：REALITY 走 TCP:443，与海量正常 HTTPS 同形，封锁它的附带损害极高，**下限稳**；Hysteria2 走 UDP，**上限高但方差大**（UDP QoS 降级，且逐运营商逐时段波动）。**产品应该把下限稳的放默认，把上限高的放可选**——反之会让大量用户在首次使用时就遇到不可用。

同时下发这两条并让客户端自动择优（mihomo 的 `url-test` / sing-box 的 `urltest`），是成本最低的可用性提升手段。

### 7.4 推荐 GCP 计算方案

**推荐：GCE `e2-small` 按需实例（非 Spot），部署于 `asia-east1`（台湾），并使用 Standard 网络层级。** 同时在 `asia-northeast1`（东京）起一台做 A/B。

| 选项 | 结论 |
| --- | --- |
| **GCE 按需 + Standard Tier** | **✅ 推荐。** 唯一支持裸 TCP/UDP 的选项，因而是唯一能跑 REALITY + Hysteria2 的选项；且 Standard 层级把中国方向出网从 $0.23/GiB 砍到 $0.11/GiB 并附带每区域 200 GiB 免费 |
| **GCE Spot** | ⚠️ 初期不用。asia-east1 折扣诱人（约 52.8%），但**抢占通知默认 0 秒**，回收 = 全员掉线 + IP 变更 + 订阅失效 |
| **GCE e2-micro 免费层** | ❌ 仅限美区（最差地理位置），且免费出网仅 **1 GB/月且明确排除中国**。**对本项目价值为零** |
| **Cloud Run** | ❌ 数据面三重否决：60 分钟连接硬上限、无裸 TCP/UDP 入站、出网锁死 Premium（$0.23/GiB）。**✅ 控制面首选** |
| **GKE** | ❌ 过度设计。$0.10/集群/小时（约 $73/月），单节点场景收益为零 |

**成本结构的核心认知：出网主导，且网络层级是最大杠杆。**

以 asia-east1 单节点为例（**计算为已核实值，出网为已核实单价** **[已核实]**）：

| 项 | Premium Tier | **Standard Tier** |
| --- | --- | --- |
| e2-small 计算（730h） | $14.16 | $14.16 |
| 前 200 GiB 出网 | $46.00 | **$0.00** |
| 之后每 TiB 出网 | ~$235 | **~$90** |

**切换网络层级带来的节省，超过任何机型优化。** 且 **200 GiB 免费额度按区域独立计算**——多区域部署时免费额度线性叠加，这应当直接影响拓扑设计（宁可多开几个小节点分摊，而非单点堆流量）。

**必须在实施前完成的实测项**：

1. **网络层级 A/B（最高优先级）**：Premium vs Standard 从 asia-east1 出发。这一项**不能靠推理决定**，且直接决定一半的出网成本。
2. **区域 A/B**：台湾 vs 东京，从中国电信/联通/移动各测 24h 的 RTT、丢包、晚高峰（19:00–24:00 CST）吞吐。
3. **协议实测**：REALITY 与 Hysteria2 在三网的真实可用性与晚高峰表现。
4. **共享核 Spot 实价核实**：本文的 e2-micro/e2-small Spot 价格为**推算值**（Google 未公开共享核 Spot 定价），须在 Console 定价计算器或实际账单中确认 **[需实测]**。
5. **mihomo × Xray REALITY 互通回归**（§5.3.1）——发版前的硬性门禁。

> **收尾提醒**：本文 **§3 的价格已核实（2026-08-16 快照，Spot 推算值除外）**；但 **§4 的全部路由质量判断仍为 [社区共识] / [需实测]**。跨境链路质量没有 SLA、没有权威公开数据，**在自建探针积累出真实数据之前，不得据本文对用户承诺任何延迟或速度指标**。

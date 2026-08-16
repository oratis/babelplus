# 0004 · 裁决：传输层按「特征混同」而非「性能最优」调参

> 日期：2026-08-16 · 性质：**架构裁决** · 状态：**设计稿 v1，待实施**（2026-08-16）
> 事实基线：USENIX Security '23/'24/'25、NDSS '25、FOCI 2025、IEEE S&P '25 论文原文；
> OONI 实测（`probe_cc=CN`，2025-01-01 → 2026-08-16）；APNIC RDAP / RIPEstat BGP 实查
> 证据口径：同行评审论文 = 高；OONI 大样本 = 高；net4people 社区报告（带日期）= 中；厂商营销 = 不采信
> 关联：[system-design.md](../02-architecture/system-design.md) §3、
> [0001](0001-cloudflare-tos-risk.md)、[0003](0003-web-hosting-and-reachability.md)
> ⚠️ 本裁决**修正**了 [system-design.md](../02-architecture/system-design.md) §3.3 的区域与网络层级选择，
> 并**部分推翻** [reference-repos.md](../01-research/reference-repos.md) §1.5 第 4 条关于 mux 的结论。

---

## 1 · 裁决

1. **Hysteria2 用 BBR，不用 Brutal** —— 尽管 Brutal 快 55%。
2. **启用多路复用（mux）** —— 尽管它损害吞吐。
3. **证书必须钉 Let's Encrypt，禁用 Google Trust Services。**
4. **节点区域改为 `asia-east2`（香港），网络层级用 Premium。**
5. **把「拿到哪个 IP」当作一等变量**，设计上必须支持释放与重新分配。
6. **QUIC/Hysteria2 的战略地位进一步下调** —— 它是加速通路，不是可依赖的主力。

贯穿这五条的是同一个原则：**在抗封锁场景里，可辨识度是比性能更重要的指标。**
每一条都是主动放弃性能来换取「看起来像正常流量」。

---

## 2 · 为什么要专门推翻性能导向

[reference-repos.md](../01-research/reference-repos.md) §1.5 记录的 Proxy_Skill 实测是**纯性能视角**的：
Brutal 比 BBR 快（1700 vs 1094 KB/s）、mux 有害（队头阻塞）。
这些测量本身没错，但它们是在**自用、无对抗**的前提下做的。

产品化之后前提变了：**我们的流量会被持续探测与分类。**
下面每一条证据都指向同一个方向 —— 那些让链路更快的选择，恰好也让它更容易被识别。

---

## 3 · 证据与逐条裁决

### 3.1 Brutal 拥塞控制有 100% 可分性的特征

> Wang, Xue, Kumar, Mishra, Anonymous, Ensafi,
> **"Is Custom Congestion Control a Bad Idea for Circumvention Tools?"**, **FOCI 2025(1)**
> `petsymposium.org/foci/2025/foci-2025-0001.pdf`

核心机制：**Hysteria 与 TCP-Brutal 在丢包时提高发送速率**，而 BBR 等标准算法向下收敛。
这是一个**行为层面的、与加密无关的**特征。

实验结果：对 10,080 条实验流，一个两级阈值分类器
**以 100% 准确率区分了 loss-based 与 non-loss-based 拥塞控制**，
随后捕获**全部**自定义 CCA 流，仅 16 个误报。

> ⚠️ **限制**：实验是实验室模拟，**不是** GFW 已部署的观测。
> 但社区侧有独立印证 —— `apernet/hysteria` issue #1301（2024-12-26）：
> 「BBR 比 Brutal 稳定」「**Brutal 的特征太明显了**」。
> 一篇论文与一条社区观察在互不知情的情况下得出同一结论，这个组合的说服力比任何一方单独都强。

**裁决：默认下发 BBR 模式。** 放弃 55% 的吞吐优势。
Brutal 仅在用户手动选择的「激进模式」中提供，并明确告知风险。

### 3.2 QUIC 的 SNI 已被大规模解密

> Zohaib, Zao, Sippe, Alaraj, Houmansadr, Durumeric, Wustrow,
> **"Exposing and Circumventing SNI-based QUIC Censorship of the Great Firewall of China"**,
> **USENIX Security 2025** · `gfw.report/publications/usenixsecurity25/`

关键事实：

- **2024-04-07 起，GFW 大规模解密 QUIC Initial 包以读取 SNI。**
- 触发后丢弃该 **3 元组（源 IP、目的 IP、目的端口）** 的全部后续 UDP 包，**持续 180 秒**。
- **换源端口无效**（仍被丢弃），换目的端口有效。
- **单个 QUIC Initial 即可触发** —— 不像 TLS-SNI 需要 SYN + PSH/ACK。
  这是**已知的首例 GFW 对 UDP 协议做丢包式残留封锁**，意味着具备在路径能力。
- QUIC 用的是**独立于 TLS/HTTP/DNS 的另一份封锁列表**。

社区侧的对应观察（`apernet/hysteria` issues #1267/#1301/#1380）：

- 2024-12-17：「所有 HY2 节点被统一 QoS」，且**端口跳跃没有帮助**。
- 2025-06-19：**企业静态 IP** 上任何 QUIC 每 3–16 分钟被切断，
  切到 TCP 再切回可恢复，**而普通 UDP 数据报双向通畅** ——
  说明是 **QUIC 协议识别**，不是通用 UDP 封锁。**家宽动态 IP 不受影响。**

**裁决：Hysteria2 保留为加速通路，但**
- 不作为默认，不作为唯一（已在 [system-design](../02-architecture/system-design.md) §3.1 裁定）；
- 端口跳跃**不作为主要防御手段**（社区实测无效）；
- 需要在客户端配置中保留一键切回 TCP 路径的能力（这是社区验证过的恢复手段）。

### 3.3 多路复用是对抗 TLS-in-TLS 指纹的唯一有效手段

> Xue, Kallitsis, Houmansadr, Ensafi,
> **"Fingerprinting Obfuscated Proxy Traffic with Encapsulated TLS Handshakes"**,
> **USENIX Security 2024** · `usenix.org/system/files/usenixsecurity24-xue-fingerprinting.pdf`

> ⚠️ **重要框定**：这是研究者**扮演审查者**在美国 ISP（Merit Network，>100 万用户）
> 的 50 Gbps 镜像流量上做的实验，**不是** GFW 已部署行为的观测报告。
> 不要把它描述成「GFW 正在这么做」。

在 **FPR < 0.6%**（对齐 GFW 自身的估计误报率）下，**全部 23 种混淆代理配置的 TPR 均超过 70%**：

| 配置 | TPR |
|---|---|
| Shadowsocks | 0.854 |
| shadowsocks-over-Cloak | 0.787 |
| vmess-over-websocket | 0.785 |
| vmess | 0.771 |
| vless-over-TLS | **0.748** |
| trojan-over-TLS | 0.737 |
| vmess-ws-tls | 0.688（无填充时 0.859）|

**对抗手段的效果排序**：

- 随机填充：**帮助有限**。
- 激进填充（XTLS-Vision、obfs4）：迫使审查者改用基于方向的模型，误报率升到 0.61% ——
  作者认为**在敏感时期审查者会容忍这个误报率**。
- **多路复用：唯一真正有效。每条代理连接哪怕只承载 2 条应用流，检测 TPR 就下降超过 70%。**
  （单条活跃流时无效。）

> **这一条直接与 Proxy_Skill 的实测冲突** —— 后者发现 mux 有害
> （多个逻辑流塞进同一条 TCP 连接，受同一单流上限约束，且引入队头阻塞）。
>
> **两者都对，只是优化目标不同。** 处置：
> - **TCP 路径（REALITY）启用 mux** —— 它本来就受单流拥塞控制约束，
>   mux 带来的额外吞吐损失有限，而抗指纹收益巨大。
> - **UDP 路径（Hysteria2）不启用 mux** —— QUIC 原生多路复用，且它的价值就是单流吞吐。
>
> ⚠️ **这个取舍建立在推理而非实测之上，需要用我们自己的数据验证。**

同一课题组的后续工作（Xue et al., **NDSS 2025**，`ndss-symposium.org/wp-content/uploads/2025-966-paper.pdf`）
发现代理会造成**传输层与应用层 RTT 的错位**，是一个**与协议无关**的特征 ——
意味着协议层的混淆无法完全消除代理的可辨识性。同样是censor-modelling，非观测。

### 3.4 🔴 Google Trust Services 证书会导致 IP 级丢包

`net4people/bbs` issue #381（2024-07-22，8 条回复，含抓包）：

使用 Google 新中间证书 **`WE1` / `WR2` / `WR3`**（替代 `GTS CA 1C3`/`1D4`/`1P5`）的站点，
在 **TLS 1.2 下 100% 被封**。报告者原文：

> "it is not related to the domain name, **it is the IP that is blocked**."

net4people 维护者的分析：

> "the connection is blocked some time **after the server's Certificate message** …
> The blocking looks like **packet dropping, not RST injection** … packets are dropped in only one direction."

TLS 1.3 起初不受影响（1.3 握手中证书是加密的），但 **2025-07-11 有报告称 1.2 与 1.3 出现同样问题**。

> 🔴 **这对本项目是直接命中的**：
> **Cloudflare Universal SSL 的两家 CA 之一就是 Google Trust Services。**
> 如果放任 Cloudflare 自选 CA，我们可能凭空继承这个失效模式 ——
> 而且它表现为**单向丢包**，排障时极难定位（会被误判成网络抖动）。

**裁决：所有面向中国用户的证书必须钉 Let's Encrypt，明确禁用 GTS。**
这一条要写进部署检查清单，并加入监控（定期核对线上证书链的签发者）。

### 3.5 GCP 的路由质量是 IP 段的属性，不是区域的属性

这是所有社区来源里**最一致**的一条发现：

- `34.x` 与 `35.x` 段路由行为不同；
- **同一个 `asia-east2` 区域内，zone `-b` 绕道美国，而 `-a` / `-c` 直连**
  （`shanyemangfu.com/route-of-gcp.html`，2019）；
- GCP HK `35.220.x` → 中国移动经北京→广东→香港直连约 50 ms；
  同一区域的 `34.92.x` → 移动经东京绕行约 110 ms（`blog.jsmsr.com`，约 2022）。

**裁决：把「拿到哪个 IP」当作一等变量。**
- 开机后**必须逐 IP 实测**三网路由，不合格就释放重开。
- 基础设施代码要支持**释放并重新分配地址**作为常规操作，不是异常处置。
- 这一条与 [runbook §3.1](../04-ops/runbook-node-health.md) 的换 IP 流程合并管理。

### 3.6 区域改为 `asia-east2`（香港）

物理下限（大圆距离 ×2 ÷ 200,000 km/s 计算所得，为**下限**非实测）：

| 出发地 | 香港 `asia-east2` | 台湾 `asia-east1` | 东京 `ane1` | 俄勒冈 `us-west1` |
|---|---|---|---|---|
| 北京 | 19.7 ms | 18.0 ms | 20.9 ms | 89.2 ms |
| 上海 | 12.3 ms | 8.0 ms | 17.5 ms | 94.0 ms |
| 深圳 | **0.3 ms** | 6.8 ms | 28.7 ms | 106.1 ms |
| 成都 | 13.7 ms | 17.8 ms | 33.4 ms | 103.9 ms |

香港是三大运营商国际互联最密集的落地点（HKIX、PCCW、HGC、CMI）。

> ⚠️ **但香港不能解决晚高峰问题** —— 拥塞在中国境内一侧的交接段。
> POMACS 2020 实测：**71% 的瓶颈跳位于中国境内纵深**，不在海缆、不在边界。
> 香港带来的是**最好的非高峰延迟**，不是对高峰劣化的免疫。

**同时必须知道的**：实查 RIPEstat BGP 邻接关系（2026-08-16），
**Google AS15169 的 335 个观测邻居中，没有任何一个中国大陆运营商 ASN**，
与 AS4809（CN2）也无邻接。中国方向流量走的是 PCCW / NTT / Arelion / HE / HGC 等中转。
**即：GCP 上买不到 CN2 GIA，这是结构性的，不是配置问题。**

### 3.7 网络层级改回 Premium —— 因为 Standard 没有 IPv6

[system-design](../02-architecture/system-design.md) §3.3 此前按成本裁定用 Standard
（$0.11/GiB vs $0.23，另有 200 GiB/区域/月免费额度）。**这个裁定需要修正**：

GCP 官方文档明确 **Standard Tier 不支持 IPv6**
（`docs.cloud.google.com/network-tiers/docs/overview`）。

而 IPv6 对本项目有两处具体价值：
1. 中国 IPv6 部署激进，且社区反复观察到 **IPv6 路径受干扰通常少于 IPv4**（**待核实**，无论文支撑）；
2. OONI 数据显示 Cloudflare 的 `2606:4700::/32` 从电信与联通均**完全可达**。

> **裁决：用 Premium。** 但要诚实记录：
> Premium 优化的是**从来不是瓶颈的那 80% 路径**（Google 骨干段），
> 两个层级都把包交到同一个拥塞的中国骨干上。
> **Premium 的真实价值是入向宣告范围更广（给中国运营商更多落地选择）+ IPv6 + 99.99% SLA，
> 不是「更快」。**
>
> 且入向路径由**中国运营商的 BGP 决策**，我们完全无法控制 ——
> 这正是中国移动「绕美」现象的成因。Premium 给运营商更多入口，但**强迫不了它选好的那个**。

### 3.8 ECH 在中国不是被封，是被 DNS 卡住

> Niere, Lange, Heitmann, Somorovsky, **"Encrypted Client Hello (ECH) in Censorship Circumvention"**,
> **FOCI 2025(2)** · `petsymposium.org/foci/2025/foci-2025-0016.pdf`

原文：

> "China. We did not encounter censorship of ClientHello messages that contain an ECH extension
> by the GFW. However, ECH is effectively censored in China through the censorship of
> unencrypted and encrypted DNS."

即 **GFW 不封 ECH 本身，但封了拿 ECHConfig 所需的加密 DNS**。
论文同时给出破解口：**NextDNS 的主机名解析到两个 IP，只有一个被黑洞**，用另一个即可。

社区侧另有更简单的做法（`net4people/bbs` #529，2025-09-23）：
**所有 Cloudflare 免费计划 zone 共用同一份 ECH config**，
因此可以从一个未被污染的 Cloudflare 域名取 ECHConfig 复用，不需要 DoH。
#543（2025-11-07）报告此法确实解决了一个被拦截的域名。

**裁决：ECH 列为应急通路的加固手段，配合固定的 ECHConfig 获取方案。标注为待验证。**

---

## 4 · 修正汇总

| 文档 | 原结论 | 本裁决 |
|---|---|---|
| [system-design](../02-architecture/system-design.md) §3.3 | `asia-east1` + Standard 层级 | **`asia-east2` + Premium 层级** |
| [reference-repos](../01-research/reference-repos.md) §1.5 第 4 条 | mux 有害 | **TCP 路径启用 mux**（抗指纹 > 吞吐）；UDP 路径不启用 |
| 隐含默认 | Hysteria2 用 Brutal（更快） | **用 BBR**（Brutal 有 100% 可分特征）|
| 未涉及 | — | **新增：证书必须钉 Let's Encrypt** |
| [pricing](../03-product/pricing-and-plans.md) §2.2 | Standard 层级是最大成本杠杆 | **仍然是最大杠杆，但因 IPv6 缺失而不采用** —— 成本结论需重算 |

---

## 5 · 代价

> ⚠️ 这一选择的代价，必须留在记录里而不是被措辞掩盖：
>
> 1. **主动放弃 55% 的单流吞吐**（Brutal → BBR：1700 → 1094 KB/s）。
>    换来的是不携带一个已被证明 100% 可分的行为特征。
>    **若后续实测表明 GFW 并未部署 CCA 分类，这个取舍就是纯亏 —— 需要复审。**
> 2. **TCP 路径启用 mux 会损害吞吐**（Proxy_Skill 已实测队头阻塞问题），
>    且**「mux 有效」这一条来自 censor-modelling 论文而非 GFW 观测**。
>    我们是在用确定的性能损失，换取对一个**尚未证实已部署**的攻击的防御。
> 3. **改用 Premium 层级，出口单价翻倍**（$0.11 → $0.23/GiB），
>    且**放弃 200 GiB/区域/月的免费额度** —— 这直接推高定价下限，
>    [pricing](../03-product/pricing-and-plans.md) §2 的全部核算需要重做。
>    换来的是 IPv6 能力，**而 IPv6 更不易被干扰这一点本身是待核实的社区观察。**
>    这是本裁决中论据最弱的一条。
> 4. **按 IP 段实测选路 = 每次开机都有额外的验收成本**，且可能反复释放重开。
> 5. 本裁决大量依赖 censor-modelling 论文（研究者扮演审查者），
>    **它们证明的是「可以被检测」，不是「正在被检测」。**
>    按最坏情况设计是稳妥的，但也意味着我们可能在为不存在的威胁付出性能代价。

## 6 · 这次没有解决的

- [ ] 🔴 §3.7 的 Premium/Standard 取舍**论据最弱**（依赖「IPv6 更不易被干扰」这一待核实的社区观察）。
      应当用实测决定，且这直接影响定价。
- [ ] mux 对 REALITY 路径的实际吞吐损失**未实测**。
- [ ] BBR vs Brutal 在三网晚高峰的真实差距**未实测**（1700/1094 KB/s 来自单一网络单次测量）。
- [ ] 证书链监控未设计（如何持续确认线上证书不是 GTS 签发）。
- [ ] ECHConfig 的获取与更新方案未设计。
- [ ] IP 段实测的自动化未设计（开机 → 三网探测 → 不合格自动释放重开）。
- [ ] 未评估「中转架构」（境内 VPS 转发到境外落地）—— 它的延迟最优但法律敞口最大，
      `net4people/bbs` #602（2026-04-04）记录境内 VPS 跑 mTLS 到 `.de` 域名约 5 分钟后被 RST。
- [ ] 河南省级审查（IEEE S&P '25：自 2023-08 起省内自建 TLS-SNI/HTTP-Host 审查，
      累计封锁 **420 万域名，超过 GFW 累计封锁量的 5 倍**）对我们的影响未评估。

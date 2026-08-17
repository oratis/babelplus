# 0001 · 裁决：Cloudflare 只承载控制面，不承载中转数据面

> 日期：2026-08-16 · 性质：**架构裁决** · 状态：**已批准，待实施**（2026-08-17 用户批准）
> 事实基线：2026-08-16 实际抓取 Cloudflare 官方条款与开发者文档原文
> 关联：[protocol-and-infra.md](../01-research/protocol-and-infra.md)、
> [product-brief.md](../00-overview/product-brief.md)、
> [reference-repos.md](../01-research/reference-repos.md) §1.7（Proxy_Skill 现有的 CF Tunnel 用法）
> 裁决人：用户（2026-08-17 批准，指示「所有决策按照推荐」）

---

## 1 · 裁决

**Cloudflare 用于承载 Web 面板、API、教程站与 DNS；不用于承载中转流量的数据面。
中转数据面走 GCP 直连（Hysteria2 主 / REALITY 备）。CF 隧道路径只作为「直连全断」时的应急通道，
且部署在与主账号完全隔离的独立 Cloudflare 账号下。**

---

## 2 · 为什么必须专门裁决这件事

需求原文是「通过 **GC+CF** 的流量中转服务」，即把 Cloudflare 放在数据路径上。
调研发现这与 Cloudflare 的服务条款直接冲突，**且社区流传的风险认知是错的**，
因此不能按默认理解推进。

### 2.1 社区共识已过时三年

几乎所有中文与英文社区讨论都在引用 Cloudflare ToS **第 2.8 条「禁止代理非 HTML 内容」**，
并围绕它设计规避方案（控制流量占比、只走小文件等）。

**这一条款已于 2023-05-16 被删除。**
来源：[Goodbye, section 2.8 and hello to Cloudflare's new terms of service](https://blog.cloudflare.com/updated-tos)。
实测核对 2025-09-12 版 [Self-Serve Subscription Agreement](https://www.cloudflare.com/terms/) 原文，
章节编号从 2.7 直接跳到第 3 节，**全文不含 "non-HTML" 字样**。

其继承条款移到了
[Service-Specific Terms](https://www.cloudflare.com/service-specific-terms-application-services/)
的 "Content Delivery Network (Free, Pro, or Business)" 段落，且**明确把 Developer Platform
（Workers/Pages）列为可以合法承载大文件的付费服务**。

> 也就是说：**「按流量类型/占比规避」这条路从根上就不成立** —— 它针对的条款已经不存在，
> 而真正生效的条款根本不看流量类型。

### 2.2 真正生效的是 §2.2.1(j)

[Self-Serve Subscription Agreement](https://www.cloudflare.com/terms/) §2.2.1 Restrictions 第 (j) 项，原文：

> "use the Services to provide a virtual private network or other similar proxy services."
> — Cloudflare Self-Serve Subscription Agreement §2.2.1(j)

前置语为 "Unless otherwise expressly permitted in writing by Cloudflare, you will not and you have no right to…"。

三个关键差别，决定了风险评估完全不同：

| 维度 | 旧的 §2.8（已废止） | 现行 §2.2.1(j) |
|---|---|---|
| 适用范围 | 仅 CDN | **"the Services" 全部服务**，含 Workers / Pages / CDN |
| 判定依据 | 流量的**内容类型与占比** | **用途本身** ——「提供 VPN 或类似代理服务」 |
| 规避空间 | 有（控制内容类型、买付费服务） | **无。没有付费豁免通道** |
| 我们的行为 | 可争辩 | **正面、明确、无争议地违反** |

相邻两条同样增加暴露面：
- **§2.2.1(b)** 禁止 "creating an undue burden on the Services or the networks"，
  并点名 "causing… traffic for your Cloudflare-proxied domain to be sent to an IP address
  that was not assigned by Cloudflare for the domain"。
- **§2.2.1(c)** 禁止规避服务用量限制与配额。

[Developer Platform 专项条款](https://www.cloudflare.com/service-specific-terms-developer-platform/)
（2026-06-02 版）另行保留了「**因怀疑违反协议即可限制或暂停访问，无需通知**」的权利，
并说明 `workers.dev` / `pages.dev` 子域名通常提前一周通知才更名，
但**若因违反条款则立即生效**。

### 2.3 技术上也有一堵硬墙

即便不谈条款，Workers 做数据面本身受官方明文限制
（[TCP Sockets API](https://developers.cloudflare.com/workers/runtime-apis/tcp-sockets/)）：

- **"Outbound TCP sockets to Cloudflare IP ranges are blocked."**
  —— 这正是所有 edgetunnel 类项目必须外挂一个 `proxyIP` / SOCKS5 中转跳板的原因。
  这是**刻意设计的限制，不是待修的 bug**，指望它以后放开是不合理的。
- 连回发起自身的 Worker 会得到 `TCP Loop detected`。
- **128 MB 内存是 per-isolate 而非 per-invocation**，多条并发隧道共享，这是真实的并发天花板。
- **运行时每周更新数次，在途请求只有 30 秒宽限期** —— 长连接会被周期性掐断。

### 2.4 但经济模型确实极具诱惑（这是要放弃的东西）

必须诚实记录我们放弃了什么。
据 [Workers Pricing](https://developers.cloudflare.com/workers/platform/pricing/)（2026-07-07 版）：

- **完全不收流量费**（"no charges for data transfer or bandwidth"）。
- WebSocket 的 `Upgrade` 记 1 次请求，**其后所有消息不计请求数**。
- 等待网络 I/O **不计入 CPU 时间**；时长既不计费也不限制。

即：一条 WSS 隧道的边际成本 ≈ 1 次请求 + 极少 CPU-ms，**流量免费**。
对比 GCP 出口 100GB/月 ≈ $23，这是数量级的成本差距。
**这就是为什么这套方案在社区如此流行 —— 它在经济上确实无可匹敌。**

---

## 3 · 风险的实际形态

违规的后果不是「这条隧道被断」，而是**账号级处置**：

1. Cloudflare 可**不经通知**暂停或限制访问。
2. 处置对象是**账号**，因此**同一 Cloudflare 账号下的其他域名与服务会一并受影响** ——
   包括我们打算放在 CF 上的 Web 面板、API、教程站、DNS。
3. **这构成一个单点故障，且是最糟糕的那种**：抗封锁架构的全部备份路径
   （备用域名、域名池、带外通知）如果都托管在同一个 CF 账号下，
   会在同一时刻一起消失。
4. 域名 DNS 若也在该账号，恢复需要转出 DNS，耗时以天计。

> ⚠️ 关键判断：**把中转数据面和抗封锁控制面放在同一个违反其 ToS 的服务商上，
> 是把「服务商处置」和「被墙」这两个独立风险耦合成了一个。**
> 这在架构上比 ToS 本身更不可接受。

---

## 4 · 候选方案

| 方案 | 数据面 | 控制面（Web/API/DNS） | ToS 风险 | 成本 | 单流性能 |
|---|---|---|---|---|---|
| **A. 全 CF 数据面** | CF Workers/Tunnel | 同一 CF 账号 | 高，且**耦合** | 流量免费 | 受 TCP 单流瓶颈约束 |
| **B. CF 数据面 + 隔离账号** | CF Workers（独立账号） | 另一 CF 账号 | 高，但**已解耦** | 流量免费 | 同上 |
| **C. GCP 直连为主，CF 应急**（提案） | GCP Hysteria2/REALITY | CF（主账号） | **控制面合规**；应急通道在隔离账号 | 出口按 GB 计费 | **最优（4.6×）** |
| **D. 完全不用 CF** | GCP 直连 | 自建/其他 CDN | 无 | 更高（需自建抗 DDoS） | 最优 |

### 4.1 为什么提案选 C

1. **性能上 CF 路径本来就不是最优解。**
   Proxy_Skill 的实测（[reference-repos.md](../01-research/reference-repos.md) §1.5）已经证明：
   跨境链路的瓶颈是**单条 TCP 流的拥塞控制**，不是带宽。
   VLESS+WS over CDN 仍然是 TCP，**逃不出这个瓶颈**。
   实测单流吞吐中位数：

   | 路径 | 协议 | 单流吞吐 |
   |---|---|---|
   | JP-HY2（Brutal） | Hysteria2 / QUIC UDP | **~1700 KB/s** |
   | JP-HY2（BBR） | Hysteria2 / QUIC UDP | 1094 KB/s |
   | JP-SS | SS-2022 / TCP | 370 KB/s |
   | JP-Reality | VLESS+REALITY / TCP | 269 KB/s |

   **也就是说：违反 ToS 换来的那条路径，性能只有合规路径的 1/5 左右。**
   这是本裁决最有力的论据 —— 我们放弃的不是「更好的方案」，而是「更便宜但更慢的方案」。

2. **CDN 路径的真实价值是抗封锁，不是性能。** 它的不可替代性在于
   「VM IP 被封时仍然可用」（cloudflared 是出站连接，不受入站 IP 封锁影响）。
   这个价值只在**故障态**兑现，因此把它降级为**应急通道**是恰当的定位，
   而不是让它承载日常流量、天天暴露在 ToS 风险下。

3. **应急通道放隔离账号，把两个风险解耦。**
   主账号（Web/API/DNS/教程站）做的是**完全合规**的事情：托管静态站点、反代一个 API。
   应急账号即使被封，损失仅限于应急通道本身。

4. **成本可承受。** 出口流量是唯一变动成本，且计费模型本来就要覆盖它
   （见 [product-brief.md](../00-overview/product-brief.md) §4.3）。
   「内部使用 + 邀请制」的规模下，这不是数量级问题。

---

## 5 · 落地约束（若本提案获批）

1. **两个 Cloudflare 账号，严格隔离**：
   - `主账号`：`babel.plus` 及 Web/API/教程站域名、DNS。**不承载任何中转流量。**
   - `应急账号`：仅用于 CF Tunnel / Worker 应急通道，用独立邮箱与独立域名，
     **不放任何有价值的资产**，视为随时可弃。
2. **域名注册商不能只用一家**，且**注册商账号与 CF 账号分离**，
   避免 CF 处置导致域名失控。
3. 应急通道**默认关闭**，仅在探测到直连全断时由客户端/订阅侧切换启用。
4. 现有 `vpn-us`/`vpn-jp` 上的 cloudflared 隧道（Proxy_Skill 遗留）
   **需要确认它挂在哪个 CF 账号下** —— 若与主账号同账号，属于当前就存在的暴露，需迁移。

---

## 6 · 代价

> ⚠️ 这一选择的代价，必须留在记录里而不是被措辞掩盖：
>
> 1. **放弃了流量免费。** CF Workers 路径流量零成本，GCP 出口约 $0.23/GB（待核实具体档位）。
>    按 100GB/月/人计，每用户每月约 $23 的直接成本。**这是本裁决最实在的代价，
>    它直接决定了套餐定价的下限。**
> 2. **放弃了「IP 被封仍可用」的常态化保障。** 降级为应急通道意味着：
>    直连被封时用户会先经历一段不可用，再切到应急路径，而不是无感切换。
>    需要用客户端侧的探测与自动切换把这段时间压到最短。
> 3. **应急通道仍然违反 ToS**，只是把爆炸半径限制住了。**这不是合规方案，是风险隔离方案。**
>    若要求完全合规，必须选方案 D，代价是失去 CF 的抗 DDoS 与全球边缘加速。
> 4. **本裁决建立在 Proxy_Skill 的单次实测之上**（4 轮交叉轮询，样本量有限，
>    仅覆盖 JP 节点）。若后续实测显示 CDN 路径吞吐并不劣于直连，
>    §4.1 的第 1 条论据失效，**本裁决需要重新审视**。

## 7 · 这次没有解决的

- [ ] 未确认现有 `vpn-us`/`vpn-jp` 的 cloudflared 隧道挂在哪个 CF 账号 —— 需要 CF 后台访问权限。
- [ ] GCP 出口流量的准确单价未核实（Premium vs Standard Tier、按目的地大洲分档），
      这直接影响定价，标 **待核实**。
- [ ] 未评估 CF 之外的 CDN/边缘替代（Fastly、Bunny、AWS CloudFront）的 ToS 立场。
- [ ] 未评估「自建多入口 IP + Anycast」替代 CF 的可行性与成本。
- [ ] 应急通道的**触发与切换机制**未设计（客户端探测？订阅侧下发？带外推送？）。
- [ ] 若用户决策为方案 A 或 B（接受 ToS 风险以换取流量成本），本文需整体重写为新的裁决，
      并按 [docs/README.md](../README.md) §4 的规矩交代旧理由的落点。

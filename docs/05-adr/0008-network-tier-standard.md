# 0008 · 裁决：网络层级改用 Standard，放弃为 IPv6 支付 Premium 溢价

> 日期：2026-08-17 · 性质：**架构裁决** · 状态：**设计稿 v1，待实施**（2026-08-17）
> **推翻 [0004 号 §3.7](0004-transport-hardening.md)**（该节自陈「论据最弱」，本裁决给出了推翻它的证据）
> 事实基线：Google Cloud Billing Catalog API 权威价目 + Google 官方文档 + OONI 原始测量
> 证据：[gcp-egress-pricing-20260817](../evidence/gcp-egress-pricing-20260817/)、
> [ipv6-censorship-20260817](../evidence/ipv6-censorship-20260817/)
> 关联：[0004](0004-transport-hardening.md)、[system-design §3.3](../02-architecture/system-design.md)、
> [pricing §2](../03-product/pricing-and-plans.md)、[roadmap B20](../00-overview/roadmap.md)

---

## 1 · 裁决

**节点使用 Standard 网络层级，不用 Premium。接受失去 IPv6 与 SLA 从 99.99% 降到 99.9%。**

---

## 2 · 被推翻的是什么

[ADR 0004 §3.7](0004-transport-hardening.md) 裁定「网络层级改回 Premium」，
理由只有一条：**Standard 不支持 IPv6，而 IPv6 路径在中国受干扰更少**。
原文自己标注这是「本裁决中论据最弱的一条」并列为「最应当优先用实测推翻的决定」。

本裁决就是那次实测。逐条交代：

| 0004 §3.7 的理由 | 本裁决下的落点 |
|---|---|
| ① Standard 不支持 IPv6 | **保留，且已由官方文档确认。** Google 网络层级文档明写 Standard 下「Regional external IPv6 addresses」与「Global external IPv4 and IPv6 addresses」均为 *Not supported* |
| ② IPv6 路径在中国受干扰更少（原文标 **待核实**） | 🔴 **不再适用 —— 已有直接反证。** 见 §3 |
| ③ Cloudflare 的 `2606:4700::/32` 从电信联通实测可达 | **保留，但不支持原结论。** 「IPv6 可达」与「IPv6 不被审查」是两件事：那次测量里可达的是**未被封锁的域名**，被封域名在 IPv6 上同样被 RST |
| ④ Premium 入向宣告范围更广，给中国运营商更多落地选择 | **保留，但价值存疑。** 0004 §3.7 自己也写了「入向路径由中国运营商的 BGP 决策，我们完全无法控制 —— Premium 给运营商更多入口，但强迫不了它选好的那个」 |
| ⑤ Premium SLA 99.99% vs Standard 99.9% | **行为变化，这是真实代价。** 见 §5 |

---

## 3 · 决定性证据：GFW 的 SNI 封锁在 IPv6 上一模一样

OONI 原始测量 `20260807061238.146747_CN_webconnectivity_d52aeb0d19592b62`，
**AS9808（中国移动），2026-08-07**，目标 `workers.dev`：

```
TCP 连接
  IPv4  104.18.12.15:443                success=True
  IPv6  [2606:4700::6812:d0f]:443       success=True     ← 两边都连得上

TLS 握手（SNI = workers.dev）
  IPv4  104.18.12.15:443                failure=connection_reset
  IPv6  [2606:4700::6812:d0f]:443       failure=connection_reset   ← 两边都被 RST
  IPv4  104.18.13.15:443                failure=connection_reset
  IPv6  [2606:4700::6812:c0f]:443       failure=connection_reset
```

`dns_consistency: consistent`（DNS 未被投毒），`blocking: http-failure`。

**这是一次受控对比**：同一个探针、同一时刻、同一域名、同一份 DNS 结果，
**唯一的变量是 IP 版本**。结论无可回避：

> **GFW 的 SNI 触发式 RST 注入在 IPv6 上与 IPv4 完全同等有效。**

而 SNI 封锁**恰好就是威胁我们的那个机制** —— 我们的 REALITY 走 TCP:443 + TLS，
面对的正是 SNI 检测。为一个在该机制上毫无保护作用的能力支付溢价，是不成立的。

> ⚠️ **本证据的边界**：它证明的是**SNI 触发式 RST** 在 IPv6 上同样生效，
> 单一 ASN、单次测量。它**不证明** IPv6 在其他机制上（IP 黑洞、QUIC 检测、
> DNS 投毒）也与 IPv4 等同 —— 那些仍是开放问题。
> 但对本项目而言，SNI 封锁是主要威胁，这一条足以支撑裁决。

---

## 4 · 成本差距（这是推翻它的另一半）

Cloud Billing Catalog API 权威价目（[证据](../evidence/gcp-egress-pricing-20260817/)）：

| | Standard | Premium |
|---|---|---|
| 计价维度 | **只按源区域**，与目的地无关 | 按「源区域 → 目的地」配对 |
| 香港/台湾/东京出口 | **$0.11/GiB** | **$0.23/GiB** |
| 免费额度 | **每源区域每月前 200 GiB $0** | **无** |

按每用户 100 GB/月：

| 规模 | Standard | Premium | 差额 |
|---|---|---|---|
| 1 用户 | **$0** | $23.00 | **$23.00** |
| 10 用户 | $8.80/人 | $23.00/人 | $14.20/人 |
| 50 用户 | $10.56/人 | $23.00/人 | $12.44/人 |

且 **中国是香港出口在 Premium 下最贵的目的地**（$0.23，欧洲仅 $0.12）。

> **合起来看：我们本来要为一个在主要威胁上无效的能力，支付每用户每月最多 $23。**
> 这是本项目至此发现的最大一笔无效支出。

---

## 5 · 代价

> ⚠️ 这一选择的代价，必须留在记录里而不是被措辞掩盖：
>
> 1. **彻底失去 IPv6 能力。** 不只是「少一条路径」——
>    中国的 IPv6 部署很激进，部分移动网络是 IPv6-only 或 IPv6 优先。
>    **这些用户可能根本连不上我们的节点。**
>    🔴 这是本裁决最大的风险，且**我们没有数据**知道有多少用户受影响。
>    缓解：REALITY / Hysteria2 / SS-2022 都走 IPv4 直连，
>    客户端在 IPv6-only 网络下需要 NAT64/DNS64 —— **需实测**这在中国移动网络下是否可用。
> 2. **SLA 从 99.99% 降到 99.9%**（月度不可用时间从 ~4.4 分钟放宽到 ~43 分钟）。
>    对本项目可接受：我们的可用性瓶颈是 GFW 而不是 Google 骨干，
>    99.9% 与 99.99% 的差别被跨境链路的抖动完全淹没。
> 3. **Standard 走公网而非 Google 骨干，延迟稳定性可能下降。**
>    Proxy_Skill 当初选 Premium 正是这个理由。
>    **本裁决没有测过这一项** —— 若 A/B 实测显示 Standard 的晚高峰表现显著更差，
>    本裁决需要重新评估。**但那时的论据将是「性能」，不再是「IPv6」。**
> 4. **Standard 还不支持 Cloud CDN、Cloud VPN 网关、全球负载均衡。**
>    当前架构都不需要，但**如果将来要上这些，得改回 Premium**。
> 5. Premium 是 **GCP 的默认值** —— 不显式指定就会用 Premium。
>    这意味着**任何忘记加参数的建机操作都会静默产生 2.09 倍的账单**。
>    必须在 `infra/node/create-node.sh` 里硬编码 `--network-tier=STANDARD` 并加断言。

## 6 · 这次没有解决的

- [ ] 🔴 **IPv6-only 用户的比例与影响未知。** 这是本裁决最大的风险敞口。
      需要在第一批用户里统计接入方式，或先做一次中国移动 IPv6-only 环境的连通性实测。
- [ ] **Standard vs Premium 的性能 A/B 仍未做**（roadmap B20 的性能侧）。
      本裁决只解决了「IPv6 这个理由不成立」，没有证明 Standard 的性能可接受。
- [ ] IPv6 在**其他封锁机制**（IP 黑洞、QUIC 检测、DNS 投毒）下是否与 IPv4 等同，未测。
- [ ] 200 GiB 免费额度是否与 GCP Always Free 层级叠加或互斥，未核实。
- [ ] 现有 `vpn-us` / `vpn-jp` 用的是哪个层级**仍未核实**（as-built-gcp §9 的遗留项），
      因此不知道 Proxy_Skill 的实测数据是在哪个层级下取得的 ——
      **这会影响 §5 代价第 3 条的判断基准**。

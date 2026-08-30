# IPv6 上的 SNI 封锁与 IPv4 完全同等有效 —— ADR 0008 推翻 0004 §3.7 的那条一手证据

> 日期：2026-08-17（**证据本体的采集时间是 2026-08-07**） · 性质：**证据型核查** ·
> 状态：**As-Built**（本目录只放原始测量，结论已写进 [ADR 0008 §3](../../05-adr/0008-network-tier-standard.md)）
> 事实基线：OONI Web Connectivity 单次测量 `20260807T061238Z_webconnectivity_CN_9808_n4_OnSgSYpYHxNvX99a`，
> 本目录 `ooni-workers-dev-AS9808-20260807.json`（18,252 字节，
> `sha256 = d3499fbe13241729cd134cb7d5bab888e1b362b66fe759b7c536f4bfaeb5be16`）
> 证据口径：一手（原始 measurement JSON 逐字段解包）= **高**；样本量 = **单 ASN 单次**，见 §4
> 读者：要复审 ADR 0008「放弃 IPv6」这一步的人；要设计封锁判据的人（[ADR 0011](../../05-adr/0011-domain-blackout-detection.md) 也以本文件为事实基线）
> 关联：[ADR 0008](../../05-adr/0008-network-tier-standard.md) §2/§3、
> [ADR 0011](../../05-adr/0011-domain-blackout-detection.md) §5.1、
> [gcp-egress-pricing-20260817 §9](../gcp-egress-pricing-20260817/)

---

## 1 · 这份证据回答的问题

ADR 0004 §3.7 当年选 Premium 网络层级，唯一站得住的理由是「Standard 不支持 IPv6，
而 **IPv6 路径在中国受干扰更少**」—— 原文自己标了 **待核实**。

本目录就是那次核实。**结论是反证：IPv6 一点也没少受干扰。**

## 2 · 采集条件

| 项 | 值 |
|---|---|
| 探针 ASN / 网络 | **AS9808（China Mobile）**，`probe_cc = CN` |
| 测量时间 | **2026-08-07 06:12:36 UTC** |
| 目标 | `https://workers.dev` |
| 工具 | `ooniprobe-react-os` 3.30.0-alpha / `ooniprobe-engine`，`data_format_version 0.2.0` |
| 整次测量耗时 | `test_runtime = 1.5206 s` |
| 解析器 | AS60068 Datacamp（**不是运营商 DNS**，见 §4 边界） |

## 3 · 逐字段解出来的三段（全部来自本目录的 JSON，无推断）

**① DNS 正常，A 与 AAAA 都拿到了 Cloudflare 的地址**

```
queries: workers.dev A    → 104.18.12.15, 104.18.13.15
         workers.dev AAAA → 2606:4700::6812:d0f, 2606:4700::6812:c0f
dns_experiment_failure = null
```

**② TCP 443 四个地址全部连得通 —— IPv6 与 IPv4 一样通**

```
tcp_connect  [2606:4700::6812:d0f]:443   success=true
             [2606:4700::6812:c0f]:443   success=true
             104.18.12.15:443            success=true
             104.18.13.15:443            success=true
```

**③ TLS 握手（SNI = `workers.dev`）四个地址全部被 RST —— IPv6 与 IPv4 一样被打**

```
tls_handshakes  [2606:4700::6812:d0f]:443  failure=connection_reset
                [2606:4700::6812:c0f]:443  failure=connection_reset
                104.18.12.15:443           failure=connection_reset
                104.18.13.15:443           failure=connection_reset

network_events  t=0.4771 connect [2606:4700::6812:d0f]:443  ok
                t=0.4936 read    [2606:4700::6812:d0f]:443  connection_reset   ← 首个 RST
```

顶层判定：`blocking = "http-failure"`、`accessible = false`、
`http_experiment_failure = "connection_reset"`，而 `control_failure = null`
（OONI 的境外 control 对同样四个地址握手全部成功）。

> **同一域名、同一时刻、同一台探针：v4 与 v6 的 TCP 都通、TLS 都被 RST。
> 差别是零。** 「IPv6 受干扰更少」这条 2026-08-16 写下时标着「待核实」的假设，
> 到此为止是**被证伪**而不是「仍待核实」。

## 4 · 这份证据证明什么、不证明什么

**证明**：
- **SNI 触发式 RST 注入在 IPv6 上与 IPv4 完全同等有效**（同一次测量内的对照，最强的一种）。
- 封锁点在 **TLS 阶段**不在 DNS、也不在 IP 层 —— 四个 IP 全都连得上，是握手被打断。
  这是 [ADR 0011 §5.1](../../05-adr/0011-domain-blackout-detection.md) 里「切主域名在 SNI 封锁下无意义」的依据。
- 首个 RST 在 `t=0.4936` 到达，即**单次失败在 0.5 秒量级**，不是「超时几十秒」的形态。

**不证明**：
- ❌ **不证明** IPv6 在**其他封锁机制**下与 IPv4 等同（IP 黑洞、QUIC 检测、DNS 投毒都没测）。
- ❌ **不证明**普遍性。**单 ASN（AS9808）、单次测量、单域名**。电信 / 联通没有样本。
- ❌ **不证明** `workers.dev` 之外的域名。它是 Cloudflare Workers 的共享域名，
  属于被重点盯的一类，未必代表自有域名的待遇。
- ❌ **不证明**运营商 DNS 的行为：本次 `resolver_asn = AS60068`（Datacamp，境外），
  **不是** China Mobile 自己的递归 —— 所以「DNS 正常」这一条只对这条解析路径成立。

## 5 · 复现

原始 measurement 可按 `report_id` 从 OONI 的 measurement API 取回：

```
report_id = 20260807T061238Z_webconnectivity_CN_9808_n4_OnSgSYpYHxNvX99a
input     = https://workers.dev
```

本目录的解包全部是对 `ooni-workers-dev-AS9808-20260807.json` 的直接读取，
字段路径为 `test_keys.queries` / `test_keys.tcp_connect` / `test_keys.tls_handshakes` /
`test_keys.network_events`，无二次加工。

## 6 · 代价

> ⚠️ 这一选择的代价，必须留在记录里而不是被措辞掩盖：
>
> 1. **用一次单 ASN 的测量关掉了一条架构选项。** ADR 0008 据此永久放弃 IPv6，
>    而本证据的样本量是 **1 个 ASN × 1 次 × 1 个域名**。
>    换来的是不用为一个「待核实的好处」持续付 2.09 倍的出口单价 ——
>    但如果将来在电信 / 联通上测出 IPv6 确有优势，**这一条就该重新评估**。
> 2. **本目录 2026-08-17 建立时没有写 README，一直到 2026-08-29 才补。**
>    期间它是 `docs/evidence/` 里唯一一个违反「每个证据目录必须有 README.md」的目录，
>    也因此在 [evidence/README.md](../README.md) 的「已完成」表里漏登记了 12 天，
>    而 `docs/README.md` 的证据目录计数也跟着少数了。
>    代价是：**两份已发布的 ADR（0008、0011）引用了一个在索引里不存在的目录。**

## 7 · 这次没有解决的

- [ ] **电信（AS4134/AS4809）与联通（AS4837）没有样本。** 结论目前只对中国移动成立。
- [ ] **只测了 `workers.dev` 一个域名。** 自有域名（尤其是新注册、无历史的域名）
      在 IPv6 上是否同样被 SNI 封锁，无数据。
- [ ] **QUIC / HTTP3 完全没测。** Hysteria2 走 UDP，而本测量是 TCP+TLS ——
      本证据对 [ADR 0004](../../05-adr/0004-transport-hardening.md) 的 HY2 侧**一句话都没说**。
- [ ] **IPv6-only 网络下的可用性没测。** [ADR 0008 §6](../../05-adr/0008-network-tier-standard.md)
      把「IPv6-only 用户的比例与影响未知」列为它最大的风险敞口，本目录不解决这一条。

# evidence · 证据平面

实测原始数据：测速输出、抓包、探活 JSON、截图、SHA256SUMS。
**与正文文档分离** —— 结论写进正文，原始数据留在这里。

## 约定

- 目录名：`<topic>-YYYYMMDD`，例 `hy2-throughput-20260816/`
- 每个证据目录**必须有 `README.md`**，写清楚：
  - 采集条件（时间、网络、运营商、工具版本）
  - **这些证据证明什么、不证明什么** ← 最重要的一节
- 二进制/大文件附 `SHA256SUMS.txt`
- **失败样本要保留**，不要只留成功的

## 已完成

| 证据目录 | 解决了什么 |
|---|---|
| [gcp-egress-pricing-20260817](gcp-egress-pricing-20260817/) | **B2** GCP 出口单价（Billing Catalog API 权威价目）。Standard $0.11/GiB + 200GiB/区域/月免费；Premium $0.23/GiB 无免费额度 |
| [cloudrun-healthz-intercept-20260817](cloudrun-healthz-intercept-20260817/) | **Cloud Run 的 Google Frontend 拦截 /healthz**，请求不进容器。探活路径改为 /-/healthz |
| [v2node-contract-20260817](v2node-contract-20260817/) | **B6** 鉴权形态、**B18** 两个字段语义、**B16** 设备计数口径、ADR 0006 的 ETag 前提。全部靠读源码解决，无需真实节点 |
| [network-tier-implementation-20260820](network-tier-implementation-20260820/) | **ADR 0008 落地**：既有 vpn-us/vpn-jp 全为 PREMIUM（关闭 0008 §6 遗留项）；Standard 在 asia-east2 实测可用；IPv6 只支持 PREMIUM 是 API 硬约束 |

> 这两次的共同经验：**大量标着「需实测」的条目其实是「没读源码/没查 API」。**
> 在租机器测之前，先穷尽「读开源代码」与「查厂商 API」这两条零成本路径。

## 当前待采集（全部为 P0 阻塞项）

- [x] ~~`egress-cost-*` — GCP 出口单价~~ **✅ 已完成** → [gcp-egress-pricing-20260817](gcp-egress-pricing-20260817/)
      （单价已定。**实际账单核对已于 2026-08-20 补做**：`vpn-us` + `vpn-jp` 在
      2026-06-28 → 08-20 实际发生 **2,927 GiB / $294.12 = $0.1005/GiB** ——
      这是 **Premium 层**下跨两区域、混了 Internet Data Transfer 与 Carrier Peering
      两类 SKU 的加权单价，**不对应目录里任何单独一档**。
      结论写在 [pricing §2](../03-product/pricing-and-plans.md) 与
      [as-built-gcp §10.3](../02-architecture/as-built-gcp.md)。
      ⚠️ 两笔欠账：**SKU 级拆分未做**；BigQuery 导出的**原始数据没有落进本目录**，
      按本文的约定应当补一个 `egress-billing-20260820/`。）
- [ ] `protocol-throughput-*` — REALITY vs Hysteria2，电信/联通/移动 × 晚高峰
- [ ] `region-ab-*` — asia-east1 vs asia-northeast1
- [ ] 🔴 `nettier-ab-*` — Standard vs Premium 网络层级。
      **2026-08-20 起这一项的性质变了**：实查确认线上一直跑在 **Premium**
      （[as-built-gcp §10.4](../02-architecture/as-built-gcp.md)），
      [ADR 0008](../05-adr/0008-network-tier-standard.md) 从未实施 ——
      所以这不再是「选型调研」，而是**一个已经在花钱的现状要不要改**的取舍：
      成本可能降（幅度未知，需先做 SKU 级拆分），代价是 Standard 的回程路径质量
      对代理类产品直接影响体感。**这项实测是那个决定的前置条件。**
- [ ] `email-deliverability-*` — QQ/163/126/Sina 送达率
- [ ] `domain-reachability-*` — 候选托管平台与域名的三网可达性（连续一周，覆盖晚高峰）

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
| [v2node-contract-20260817](v2node-contract-20260817/) | **B6** 鉴权形态、**B18** 两个字段语义、**B16** 设备计数口径、ADR 0006 的 ETag 前提。全部靠读源码解决，无需真实节点 |

> 这两次的共同经验：**大量标着「需实测」的条目其实是「没读源码/没查 API」。**
> 在租机器测之前，先穷尽「读开源代码」与「查厂商 API」这两条零成本路径。

## 当前待采集（全部为 P0 阻塞项）

- [x] ~~`egress-cost-*` — GCP 出口单价~~ **✅ 已完成** → [gcp-egress-pricing-20260817](gcp-egress-pricing-20260817/)
      （单价已定。**实际账单核对已于 2026-08-20 补做**：`vpn-us` + `vpn-jp` 在
      2026-06-28 → 08-20 实际发生 **2,927 GiB / $294.12 = $0.1005/GiB**，与目录价 $0.11/GiB 吻合。
      结论写在 [pricing §2](../03-product/pricing-and-plans.md) 与
      [as-built-gcp §10.3](../02-architecture/as-built-gcp.md)。
      ⚠️ 这次的 BigQuery 导出**原始数据没有落进本目录** —— 按本文的约定这是一笔欠账，
      应当补一个 `egress-billing-20260820/`。）
- [ ] `protocol-throughput-*` — REALITY vs Hysteria2，电信/联通/移动 × 晚高峰
- [ ] `region-ab-*` — asia-east1 vs asia-northeast1
- [ ] `nettier-ab-*` — Standard vs Premium 网络层级
- [ ] `email-deliverability-*` — QQ/163/126/Sina 送达率
- [ ] `domain-reachability-*` — 候选托管平台与域名的三网可达性（连续一周，覆盖晚高峰）

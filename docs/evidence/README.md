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
| [egress-billing-20260820](egress-billing-20260820/) | **出口账单的 SKU 级拆分**（pricing §7 点名的定价前置）。到中国大陆的 SKU 用量为 0，中国方向实走 Carrier Peering $0.080–0.085/GiB；全部 gross 被账单账户级推广抵扣吸收，本项目现金支出约 $6 |
| [v2node-401-behavior-20260821](v2node-401-behavior-20260821/) | **B7** 关闭：401/403 **不会**清空用户列表（三重保护），但会让**重启**失败且不自愈；`alivelist` 对 ≥399 静默返空 map。仍是读源码解决 |
| [gcp-inventory-20260821](gcp-inventory-20260821/) | **B9**（run.app 证书签发者是 GTS）、**B12**（Cloud SQL 四细节里的三项 + 三条新发现）、**B32**（预算建得了，缺的是口径）；生产冒烟 6 条；监控现状：log-based metrics 曾经一条都没有 |

> **五次下来同一条经验，越来越硬：大量标着「需实测」的条目其实是「没读源码 / 没查 API / 没跑一条 gcloud」。**
> 在租机器测之前，先穷尽「读开源代码」「查厂商 API」「查自己账上的实况」这三条零成本路径。
> B7 曾被登记为「必须起真实容器」，最后是 20 分钟的源码阅读关掉的；
> B9 是一条 `openssl`，B12/B32 各是一条 `gcloud describe`。

## 当前待采集（全部为 P0 阻塞项）

- [x] ~~`egress-cost-*` — GCP 出口单价~~ **✅ 已完成** → [gcp-egress-pricing-20260817](gcp-egress-pricing-20260817/)
      （单价已定；实际账单核对于 2026-08-20 补做，结论写在 [pricing §2](../03-product/pricing-and-plans.md) 与
      [as-built-gcp §10.3](../02-architecture/as-built-gcp.md)。
      **2026-08-21：本条登记的两笔欠账已还清** → [egress-billing-20260820](egress-billing-20260820/)
      —— SKU 级拆分做完了，BigQuery 原始数据与全部查询也落进了目录。
      ⚠️ 同时修正两处口径：完整到 08-20 的实际值是 **3,399.0 GiB / $332.91**
      （§10.3 的 2,927 / $294.12 是跑数当天的部分日快照，作为 As-Built 快照不回改）；
      且**这是 gross，现金支出约 $6**，其余被推广抵扣吸收。）
- [ ] `protocol-throughput-*` — REALITY vs Hysteria2，电信/联通/移动 × 晚高峰
- [ ] `region-ab-*` — **asia-east2**（香港，ADR 0004 §3.5 裁定的主力）vs asia-northeast1
      （2026-08-21 改正区域名，原写 asia-east1 —— 即 roadmap B40）
- [ ] 🔴 `nettier-ab-*` — Standard vs Premium 网络层级。
      **2026-08-20 起这一项的性质变了**：实查确认线上一直跑在 **Premium**
      （[as-built-gcp §10.4](../02-architecture/as-built-gcp.md)），
      [ADR 0008](../05-adr/0008-network-tier-standard.md) 从未实施 ——
      所以这不再是「选型调研」，而是**一个已经在花钱的现状要不要改**的取舍：
      成本可能降，代价是 Standard 的回程路径质量对代理类产品直接影响体感。
      **2026-08-21 更新：它点名的「需先做 SKU 级拆分」已经做完**
      → [egress-billing-20260820 §2.1](egress-billing-20260820/)。
      拆开之后省钱空间**比原来估的小**：层级敏感的只有 Internet DTO 那 1,483.7 GiB
      （43.6%），另外 54.6% 走 Carrier Peering 是另一套 SKU；
      ADR 0008 引用的 2.09× 是目录价之比（$0.23/$0.11），而实收从来不是 $0.23。
      **这项性能实测仍然是那个决定的前置条件，而且现在收益上界更低了，
      即「先测性能再决定」的理由更强。**
- [ ] `email-deliverability-*` — QQ/163/126/Sina 送达率
- [ ] `domain-reachability-*` — 候选托管平台与域名的三网可达性（连续一周，覆盖晚高峰）

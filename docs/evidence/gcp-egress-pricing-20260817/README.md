# GCP 出口计价实测 · 2026-08-17

> 日期：2026-08-17 · 性质：**证据型核查** · 状态：**已完成**
> 事实基线：Google **Cloud Billing Catalog API** 权威价目，非文档抓取、非二手转述
> 采集方式：`GET https://cloudbilling.googleapis.com/v1/services/6F81-5844-456A/skus`
> （`6F81-5844-456A` = Compute Engine 服务 ID），以 `gcloud auth print-access-token` 鉴权，
> 全量翻页取得 30,000 个 SKU，筛出 `StandardInternetEgress` 与 `PremiumInternetEgress` 共 **677** 个
> 关联：[roadmap B2/B20](../../00-overview/roadmap.md)、[ADR 0004 §3.7](../../05-adr/0004-transport-hardening.md)、
> [pricing-and-plans §2](../../03-product/pricing-and-plans.md)

---

## 1 · 这些证据证明什么、不证明什么

**证明**：Premium 与 Standard 两个网络层级到中国大陆的**官方标价**，以及两者的**计价模型差异**。
数据直接来自 Google 的计费目录 API，是签合同时实际生效的那份价目。

**不证明**：
- 不证明**实际账单**。Free Tier 抵扣、承诺使用折扣、促销额度都不在 SKU 价目里。
- 不证明**性能**。价格差不等于质量差，Premium/Standard 的实际路由质量仍 **需实测**。
- 不证明**我们会用掉多少流量**。单价确定了，总成本仍取决于用量分布。

---

## 2 · 决定性发现：两个层级的计价模型根本不同

这是本次调研最重要的一点，此前所有文档都没写对。

| | **Standard Tier** | **Premium Tier** |
|---|---|---|
| 计价维度 | **只按源区域**，与目的地无关 | **按「源区域 → 目的地」配对** |
| 到中国大陆是否有专门 SKU | **没有**（不区分目的地） | **有**，且是单独定价 |
| 免费额度 | **每源区域每月前 200 GiB $0.0000** | **无。从第 1 字节起计费** |

也就是说：**Standard 根本不认「中国」这个目的地，Premium 认，而且专门加价。**

---

## 3 · 实测价目

### 3.1 Standard Tier（按源区域，目的地无关）

| 源区域 | 0–200 GiB | 200 GiB–10 TiB | >10 TiB |
|---|---|---|---|
| **Hong Kong**（`asia-east2`） | **$0.0000** | **$0.1100** | $0.0750 |
| **Taiwan**（`asia-east1`） | **$0.0000** | **$0.1100** | $0.0750 |
| **Tokyo**（`asia-northeast1`） | **$0.0000** | **$0.1100** | $0.0750 |
| Singapore / Osaka / Jakarta / Mumbai / Delhi / Bangkok | $0.0000 | $0.1100 | $0.0750 |
| Seoul | $0.0000 | $0.1190 | $0.1090 |
| Sydney / Melbourne / Sao Paulo / Santiago | $0.0000 | $0.1200 | $0.0850 |
| Oregon / Iowa / N. Virginia / 其余美欧 | $0.0000 | $0.0850 | $0.0650 |

### 3.2 Premium Tier 到中国大陆（按源区域→目的地）

**从任一源区域到 China，价格一致：**

| 用量档 | 单价 |
|---|---|
| 0–1 TiB | **$0.2300 / GiB** |
| 1–10 TiB | $0.2200 / GiB |
| >10 TiB | $0.2000 / GiB |

已核对的源区域包括 APAC、Japan、Hong Kong、Berlin、Delhi、Mexico、Phoenix、Salt Lake City 等，
**全部同价**。

### 3.3 「中国目的地溢价」的量化

同样从 **Hong Kong** 出发，Premium 到各目的地：

| 目的地 | 单价 | 相对中国 |
|---|---|---|
| Western Europe / Eastern Europe / EMEA | $0.1200 | 0.52× |
| Africa / Middle East | $0.1500 | 0.65× |
| Indonesia / South Korea | $0.1900 | 0.83× |
| **China** | **$0.2300** | **1.00×（最贵）** |

> **中国是香港出口在 Premium 层级下最贵的目的地**，比欧洲贵 **1.92 倍**。
> 这不是我们的猜测，是 Google 价目表的事实。

---

## 4 · 对本项目的成本含义

以 **asia-east2（香港）** 为源、每用户 100 GB/月计：

| 场景 | Standard | Premium |
|---|---|---|
| 单用户，总量 ≤ 200 GiB/月 | **$0**（免费额度内） | **$23.00** |
| 10 用户 = 1000 GiB/月 | 200 免费 + 800×$0.11 = **$88** → **$8.80/人** | 1000×$0.23 = **$230** → **$23.00/人** |
| 50 用户 = 5000 GiB/月 | 200 免费 + 4800×$0.11 = **$528** → **$10.56/人** | 5000×$0.23 = **$1150** → **$23.00/人** |

**Standard 在任何规模下都便宜 2.2–∞ 倍**，且规模越小优势越大（免费额度占比越高）。

> ⚠️ 注意 200 GiB 免费额度是**每源区域**的。多区域部署会有多份免费额度 ——
> 这对「多节点分散部署」的架构是额外的成本优势，此前未被计入。

---

## 5 · 对既有裁决的影响

| 文档 | 原结论 | 本次证据 |
|---|---|---|
| [ADR 0004 §3.7](../../05-adr/0004-transport-hardening.md) | 为 IPv6 支持改用 Premium，自陈「论据最弱」 | **代价被低估了。**实际是 2.09× 单价 + 完全失去 200 GiB/区域/月免费额度。小规模下差距是「$0 vs $23/人/月」 |
| [pricing §2](../../03-product/pricing-and-plans.md) | 「$0.11–0.23，待核实」 | **两个数字都对**，但此前不知道它们**属于不同计价模型**，也不知道 Standard 的免费额度是每区域的 |
| [roadmap B2](../../00-overview/roadmap.md) | 头号阻塞，标「需实测 + 需核实官方价目表」 | **✅ 已解决** |
| [roadmap B20](../../00-overview/roadmap.md) | Premium vs Standard A/B | **成本侧已定量**；**性能侧仍需实测**，但现在知道了要用多大的性能优势才值回 2.09× 的价差 |

---

## 6 · 复现方法

```bash
TOKEN=$(gcloud auth print-access-token)
curl -s -H "Authorization: Bearer $TOKEN" \
  "https://cloudbilling.googleapis.com/v1/services/6F81-5844-456A/skus?pageSize=5000" \
  | jq '.skus[] | select(.category.resourceGroup=="StandardInternetEgress" or
        .category.resourceGroup=="PremiumInternetEgress")'
```

需翻页（`nextPageToken`）取全量。本目录的 `skus-egress.json` 是 2026-08-17 的快照，
含 677 个出口 SKU 的完整原始记录（含 `skuId`、生效时间、分档价）。

> **价目会变。** 本快照的有效性以采集日为准；做正式成本核算前应重新拉取。

---

## 7 · 代价

> ⚠️ 1. 本次只解决了**单价**，没解决**用量**。成本 = 单价 × 用量，
> 而用量分布（人均实际跑多少 GB、峰谷比）**完全没有数据**，
> 需要跑满第一批用户才能建模。在此之前任何总成本预测都是猜的。
> 2. SKU 价目**不含** Free Tier 抵扣、承诺使用折扣与促销额度，
> 实际账单可能低于此。反过来说，**用它做定价下限是安全的**。

## 8 · 这次没有解决的

- [ ] Premium vs Standard 的**性能差异**未测 —— 成本侧已定量，性能侧仍是 [ADR 0004 §3.7](../../05-adr/0004-transport-hardening.md) 的空白。
- [ ] **IPv6 是否真的只有 Premium 支持**未在 API 层面核实（这是选 Premium 的唯一理由）。
- [ ] 共享核机型（e2-micro / e2-small）的 **Spot 价格**未查（本次只查了网络 SKU）。
- [ ] Cloud SQL、Cloud Run 的 SKU 未查，[ADR 0005](../../05-adr/0005-database-selection.md) 的 $9.53/月仍是文档值。
- [ ] 未验证 200 GiB 免费额度是否与 GCP Always Free 层级叠加或互斥。

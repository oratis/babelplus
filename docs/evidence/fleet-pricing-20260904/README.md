# 机队扩容价目实测 · 2026-09-04

> 日期：2026-09-04 · 性质：**证据型核查** · 状态：**已完成（本次采集）**
> 事实基线：Google **Cloud Billing Catalog API** 权威价目
> 采集方式：`GET https://cloudbilling.googleapis.com/v1/services/6F81-5844-456A/skus`
> （`6F81-5844-456A` = Compute Engine 服务 ID），以 `gcloud auth print-access-token`
> 鉴权（身份 `wangharp@gmail.com`），全量翻页取得 **32,771** 个 SKU
> 证据口径：目录 API = 高（是签约时生效的那份价目）；产品页条款 = 中；推算 = 标注
> 关联：[gcp-egress-pricing-20260817](../gcp-egress-pricing-20260817/)（同法采集的出口目录价，
> 本目录是它在**机型 / 盘 / 外网 IP** 三个维度上的补齐）、
> [egress-billing-20260820](../egress-billing-20260820/)（**实收**单价，与本目录的**目录**价必须分开读）、
> [ADR 0017](../../05-adr/0017-personal-fleet-in-repo.md)、
> [personal-fleet-runbook §2](../../04-ops/personal-fleet-runbook.md)

---

## 1 · 这些证据证明什么、不证明什么

**证明**：`e2-*` 机型、`pd-standard` 盘、外网 IPv4、Standard 层级出网、Carrier Peering 出网
在目标区域的**官方标价与分档结构**，包括免费额度是怎么编码进 SKU 的。

**不证明**：

1. **不证明实际账单。** 目录价 ≠ 实收。本项目已经有一次实证反例：
   [egress-billing-20260820 §2.1](../egress-billing-20260820/) 显示中国方向的字节
   **没有落在 `… to China` 那条 $0.23 的 SKU 上**，而是落在 Carrier Peering 的
   $0.080–0.085 上，窗口内混合实收 **$0.0979/GiB**。**做预算要用实收，做上界要用目录。**
2. **不证明外网 IP 到底按哪条 SKU 计。** 见 §4 —— 目录里有两条候选，
   「挂在运行中实例上的保留静态 IP 走哪条」从目录本身判不出来，**需用账单实数核实**。
3. **不证明免费层的条款文本。** §5 里「1 GiB 北美出网**不含中国大陆与澳大利亚**」这条
   来自 GCP 产品页，**本次没有用 API 核实条款文本**，证据等级为「官方文档 = 中」。
4. **不证明性能。** 价格差不等于质量差。Standard 与 Premium 的实际路由质量仍
   **需实测**（[ADR 0008](../../05-adr/0008-network-tier-standard.md) 的 `nettier-ab-*` 至今未做）。

---

## 2 · 四张表

完整输出见 [tables.md](tables.md)，由 [extract.py](extract.py) 从 `skus-compute.json` 生成。
下面只摘与机队扩容直接相关的行。

### 2.1 E2 整机月价（OnDemand，730 h，不含盘与出口）

| 区域 | `e2-micro` | `e2-small` | `e2-medium` | `e2-standard-2` |
|---|---|---|---|---|
| `us-west1`（俄勒冈） | $6.11 | $12.23 | **$24.46** | $48.92 |
| `us-central1`（爱荷华） | $6.11 | $12.23 | $24.46 | $48.92 |
| `us-east1`（南卡） | $6.11 | $12.23 | $24.46 | $48.92 |
| `asia-northeast1`（东京） | $7.84 | $15.69 | **$31.38** | $62.75 |
| `asia-southeast1`（新加坡） | $7.54 | $15.09 | **$30.17** | $60.35 |
| `asia-east2`（香港） | $8.56 | $17.11 | $34.22 | $68.45 |
| `asia-east1`（台湾） | $7.08 | $14.16 | $28.32 | $56.64 |

> 折算法：`E2 Instance Core` 与 `E2 Instance Ram` 两条 SKU 的时价，
> 按各机型的计费规格（`e2-micro` = 0.25 vCPU / 1 GiB，`e2-medium` = 1 vCPU / 4 GiB，
> 依此类推）加权 × 730 h。**共享核机型的「0.25 vCPU」是计费口径，不是性能保证** ——
> 性能侧的实测见 [as-built-personal-fleet §4.2](../../02-architecture/as-built-personal-fleet.md)。

### 2.2 Standard 层级出网（按源区域，**目的地无关**；单位 GiB）

| 源区域 | 0–200 | 200–10,240 | 10,240–153,600 | >153,600 |
|---|---|---|---|---|
| `us-west1` / `us-central1` / `us-east1` | **$0.000** | $0.085 | $0.065 | $0.045 |
| `asia-northeast1` / `asia-southeast1` / `asia-east2` / `asia-east1` | **$0.000** | $0.110 | $0.075 | $0.070 |

🔴 **第一档 200 GiB 是每源区域每月免费的，且它是按区域独立计的。**
这条直接影响拓扑决策：**多开一个区域 = 多一份 200 GiB 免费额度**。

### 2.3 Carrier Peering 出网（Premium 路由下中国方向的**实际**落点）

| SKU | 目录区域标签 | $/GiB | 免费额度 | 阶梯 |
|---|---|---|---|---|
| `… via Carrier Peering Network - Americas Based` | `us-central1,us-east1,us-west1` | **$0.080** | 无 | 无 |
| `… via Carrier Peering Network - APAC Based` | `asia-east1` | **$0.085** | 无 | 无 |
| `… via Carrier Peering Network - EMEA Based` | `europe-west1` | **$0.080** | 无 | 无 |

> `serviceRegions` 里只列一个代表区域，但 [egress-billing-20260820 §2](../egress-billing-20260820/)
> 实证 `asia-northeast1` 的流量确实计在 "APAC Based" 这条上 ——
> 所以它是**地理组**标签而非区域白名单，`asia-southeast1`（新加坡）同组。
> ⚠️ 这是**按一个已观测样本外推**，新加坡节点建成后的第一份账单要回来核对本条。

### 2.4 Premium 到中国大陆的 Internet DTO（对照用）

| 源 | 0–1 TiB | 1–10 TiB | >10 TiB |
|---|---|---|---|
| Japan / Singapore / Hong Kong / Los Angeles / Americas / APAC → China | **$0.23** | $0.22 | $0.20 |

**本 SKU 在 [egress-billing-20260820](../egress-billing-20260820/) 的 54 天窗口内用量为 0.00 GiB。**
它是上界，不是预算基准。

---

## 3 · 盘：免费额度直接写在 SKU 的第一档里

| SKU | 区域 | 分档 |
|---|---|---|
| `Storage PD Capacity` | `us-west1` / `us-central1` / `us-east1` | **0–30 GiB = $0.000**，>30 GiB = $0.040/GiB/月 |
| `Storage PD Capacity in Japan` | `asia-northeast1` | $0.052/GiB/月（**无免费档**） |
| `Storage PD Capacity in Singapore` | `asia-southeast1` | $0.044/GiB/月（**无免费档**） |

🔴 **这 30 GiB 是账户级的一份，不是每台一份。** 美国区域跑两台各挂 30 GB 系统盘，
只有 30 GiB 落在 $0 档，另 30 GiB 按 $0.040 计 = $1.20/月。

---

## 4 · 🔴 外网 IPv4：目录里有两条候选，判不出该走哪条

| SKU | 区域 | 单位 | 分档 |
|---|---|---|---|
| `External IP Charge on a Standard VM` | `global` | h | **0–720 h = $0.000**，>720 h = $0.005/h |
| `Static Ip Charge` | `us-central1` / `us-east1` / `us-west1` | h | 0–1 h = $0.000，>1 h = $0.010/h |
| `Static Ip Charge in Japan` | `asia-northeast1` | h | >1 h = $0.015/h |
| `Static Ip Charge in Singapore` | `asia-southeast1` | h | >1 h = $0.011/h |

`vpn-us-ip-v4` / `vpn-jp-ip` 都是**保留静态 IP 且挂在运行中的实例上**。
按名称推测应走第一条（720 h/月免费额度是账户级的，四个常驻 IP ≈ 2,920 h，
扣掉 720 h 后 2,200 h × $0.005 ≈ **$11/月**）；
按第二条算则是 4 × 730 × $0.010–0.015 ≈ **$29–44/月**。

**两者差 3–4 倍，且本次无法从目录判定。** 预算表按保守值 $11 记，
并把核实动作留成待办 —— 一条 BigQuery 就能定论：

```sql
SELECT sku.description, SUM(usage.amount) AS hours, SUM(cost) AS gross
FROM `loopback-500616.billing_export.gcp_billing_export_v1_0130C2_FA2146_786074`
WHERE project.id = 'oratis-491316'
  AND usage_start_time >= TIMESTAMP('2026-08-01')
  AND (sku.description LIKE '%Ip Charge%' OR sku.description LIKE '%External IP%')
GROUP BY 1 ORDER BY gross DESC
```

> ⚠️ **`VPN方案设计.md` §二 写的「静态 IP 挂载在运行中的 VM 上免费」在 2026-09-04 的目录里
> 已经不成立** —— 无论走哪条 SKU，超过免费档之后都是要钱的。原文那一行是过时事实。

---

## 5 · Always Free 与本机队的关系（"把 free tier 吃满"能省多少）

| 免费项 | 额度 | 证据来源 | 本机队怎么用 |
|---|---|---|---|
| `e2-micro` | **744 h/月，仅 `us-west1`/`us-central1`/`us-east1`，全账户合计 1 台份** | 官方产品页（中） | 1 台巡检聚合器 |
| `pd-standard` | 30 GiB/月（同三区域） | **SKU 第一档 = $0（高）** | 聚合器系统盘 |
| 北美出网 | 1 GiB/月，**不含中国大陆与澳大利亚** | 官方产品页（中，**本次未核实条款文本**） | 巡检 JSON（KB 级） |
| 外网 IPv4 | 720 h/月 | **SKU 第一档 = $0（高）** | 自动抵掉约 1 个 IP |
| Cloud Storage | 5 GiB + 5k A 类 / 50k B 类操作 | 官方产品页（中） | 订阅产物兜底落点 |
| Secret Manager | 6 个活跃版本 + 10k 访问/月 | 官方产品页（中） | 飞书 secret、节点 token |
| Cloud Logging | 50 GiB/项目/月 | 官方产品页（中） | 巡检日志 |
| Cloud Scheduler | 3 作业/账户/月 | 官方产品页（中） | 🔴 **已被 `bp-` 的 8 条作业占满**，第 4 条起 $0.10/作业/月 |

### 5.1 结论：免费层值约 $11/月，占 $500 预算的 2.2%

$6.11（`e2-micro`）+ $1.20（30 GiB 盘）+ 约 $3.60（720 h IP）≈ **$10.9/月**。

🔴 **而免费出网额度对本机队的价值是零** —— 1 GiB/月本身就不够，
而且**明确排除中国大陆**，也就是排除了这套机队 100% 的业务流量。

> **所以"把 free tier 吃满"是对的，但要知道它买不到什么：**
> 它买到的是一台不跑业务流量的运维机和一块盘。
> **决定 $500 够不够的，只有出口单价 × 用量。**

---

## 6 · 复现

```bash
cd docs/evidence/fleet-pricing-20260904
python3 fetch-skus.py            # 需 gcloud auth；写出 $SP/skus-compute.json
python3 extract.py skus-compute.json > tables.md
```

`skus-compute.json` 原始文件 **26 MB，未入库**（超出 evidence 目录的合理体积，
且可由上面两条命令在任何时刻重新取得）。`tables.md` 是它的确定性投影。

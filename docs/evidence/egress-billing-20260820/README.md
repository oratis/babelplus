# 出口账单 SKU 级拆分 · 2026-06-28 → 2026-08-20

> 日期：2026-08-21 · 性质：**证据型核查** · 状态：**已完成（本窗口）**
> 事实基线：BigQuery 账单导出 `loopback-500616.billing_export.gcp_billing_export_v1_0130C2_FA2146_786074`
> ——标准导出（非详细导出，因此**没有 `resource` 字段**，无法把流量归到具体实例）
> 关联：[gcp-egress-pricing-20260817](../gcp-egress-pricing-20260817/)（B2 目录价）、
> [as-built-gcp §10.3](../../02-architecture/as-built-gcp.md)、
> [pricing-and-plans §2 / §7](../../03-product/pricing-and-plans.md)、
> [ADR 0008](../../05-adr/0008-network-tier-standard.md)、
> [gcp-inventory-20260821](../gcp-inventory-20260821/)（同一批核查里的 gcloud 侧）

---

## 1 · 起因

[evidence/README.md](../README.md) 自记两笔欠账：
**「SKU 级拆分未做」**、**「BigQuery 导出的原始数据没有落进本目录」**。
[pricing §7](../../03-product/pricing-and-plans.md) 把 SKU 拆分列为**定价定稿最近的一个前置条件**。
本目录同时清掉这两笔。

## 2 · SKU 级拆分（原始数据：[sku-breakdown.csv](sku-breakdown.csv)，查询：[q1-sku-breakdown.sql](q1-sku-breakdown.sql)）

窗口 2026-06-28 00:00 UTC → 2026-08-21 00:00 UTC，项目 `oratis-491316`，
全部 `Network % Data Transfer Out %` SKU，共 23 条。**前四条占了 98.6% 的钱**：

| SKU | 源区域 | GiB | gross | $/GiB |
|---|---|---|---|---|
| Internet Data Transfer Out — Americas → Americas | `us-west1` | 842.11 | $100.81 | **0.1197** |
| **Data Transfer Out via Carrier Peering Network — Americas Based** | `us-west1` | 959.95 | $76.80 | **0.0800** |
| **Data Transfer Out via Carrier Peering Network — APAC Based** | `asia-northeast1` | 894.77 | $76.06 | **0.0850** |
| Internet Data Transfer Out — Tokyo → APAC（不含韩国/印尼） | `asia-northeast1` | 621.11 | $74.53 | **0.1200** |
| Internet DTO — Japan → Americas | `asia-northeast1` | 18.05 | $2.17 | 0.1200 |
| Inter-Region DTO — Japan → Americas | `asia-northeast1` | 25.18 | $2.01 | 0.0800 |
| Inter-Region DTO — Americas → Americas | `us-west1` | 21.05 | $0.40 | 0.0190 |
| 其余 16 条尾巴合计 | — | 17.1 | $0.16 | — |
| **合计（全部 23 条）** | — | **3,399.3** | **$332.94** | **0.0979** |

> 其中落在两台节点所在区域（`us-west1` + `asia-northeast1`）的是 **3,399.0 GiB / $332.91**；
> 差额来自 `us-central1` / `us-east1` / `europe-west4` 上不到 0.4 GiB 的零头。

### 2.1 三条结论

1. 🔴 **「到中国大陆」的 SKU 是 0.00 GiB / $0.00。**
   目录里确实存在 `Network Internet Data Transfer Out from Japan to China`（$0.197/GiB 档），
   本窗口内它的用量是**零**。中国方向的字节实际落在
   **Carrier Peering Network** 两条 SKU 上（合计 1,854.7 GiB / $152.86），实收 **$0.080–0.085/GiB**。
   > 这直接影响 [pricing](../../03-product/pricing-and-plans.md) 与
   > [审查 §4](../../00-overview/launch-readiness-review-20260821.md) 里
   > 「产品化后流量向大陆倾斜，只会往 $0.23 那一端走」这句判断 ——
   > **本窗口的实证与它相反**：倾斜方向上的实收单价是目录 Premium-to-China 价的约 1/3。
   > ⚠️ 但见 §5「不证明什么」第 2 条：Carrier Peering 归类由 Google 侧决定，不是我们能选的，
   > 也不保证未来不变。

2. **混合单价的构成已经拆开了**：**54.6% 的字节**（1,854.7 GiB / $152.86）走 Carrier Peering
   （$0.080 / $0.085），**43.6%**（1,483.7 GiB / $177.67）走 Internet DTO（$0.1197 / $0.1200），
   剩下 1.8%（60.9 GiB / $2.41）是跨区域与同区内传输。
   加权就是 $0.0979 —— 它**不对应目录里任何单独一档**这句话仍然成立，
   但现在知道它是**哪两档、按什么比例**加权出来的。

3. **Standard 层级能省多少，现在可以估了。** Internet DTO 那 1,483.7 GiB 是层级敏感的部分
   （Premium $0.12 → Standard 目录价 $0.11，且 Standard 每源区域每月前 200 GiB 免费）；
   Carrier Peering 那 1,854.7 GiB 走的是另一套 SKU。
   **切 Standard 的节省上限远低于 ADR 0008 引用的 2.09×** —— 2.09× 是目录价之比
   （$0.23 / $0.11），而实收从来不是 $0.23。

## 3 · 与 as-built §10.3 的差异：2,927 GiB / $294.12 是**跑数当天的部分日快照**

逐日累计（原始数据：[daily-egress.csv](daily-egress.csv)）：

| 截至 | 累计 GiB | 累计 gross |
|---|---|---|
| 2026-08-18 | 2,849.3 | $284.91 |
| **as-built §10.3 记录的值** | **2,927** | **$294.12** |
| 2026-08-19 | 3,087.1 | $305.65 |
| 2026-08-20 | **3,399.0** | **$332.91** |

§10.3 的数字落在 08-18 与 08-19 之间 —— 与「在 08-19/08-20 当天跑数、当日数据尚未导完」一致。
**§10.3 不需要改**（它是 As-Built 快照，标注了自己的时点）；
但**引用它做定价的人必须知道口径**：完整到 08-20 的数是 3,399.0 GiB / $332.91。

## 4 · 两个本次才看见的事实

### 4.1 🔴 出口用量在 2026-08-17 之后阶跃了约 4 倍，集中在日本节点

| 区间 | 日均 GiB | 其中 `asia-northeast1` |
|---|---|---|
| 2026-08-01 → 08-16 | 63.5 | 31.1 |
| 2026-08-17 → 08-20 | **256.2** | **203.5** |

按 08-17 → 08-20 的速率外推：**约 7,800 GiB/月 ≈ $764/月 gross**（按本窗口实收 $0.0979/GiB）。
[roadmap §11-R6](../../00-overview/roadmap.md) 的「固定成本粗估 $47–55/月」是**实例与数据库**的口径，
不含出口；把出口按当前速率算进去，量级差一个数量级。
> 本目录**不解释**这个阶跃的原因（标准导出没有 `resource` 字段，无法归到实例；
> 也没有节点侧的连接日志）。只登记事实。

### 4.2 🔴 这笔钱到目前为止**一分现金都没付** —— 全部由账单账户级的推广抵扣吸收

（原始数据：[credits.csv](credits.csv)、[billing-account-monthly.csv](billing-account-monthly.csv)、
[account-daily-burn.csv](account-daily-burn.csv)）

`oratis-491316` 在本窗口的 gross 被一笔 `PROMOTION` 抵扣（credit id `eddda554-…`）冲掉
**-$1,237.74**，净额约 **$6**。出口那四条 SKU 的 net 分别是 $0.17 / $0.15 / $0.04 / $0.14。

这笔抵扣是**账单账户 `0130C2-FA2146-786074` 级别的，与同账户其它项目共用**：

| 账期 | 全账户 gross | 其中 `oratis-491316` | PROMOTION 抵扣 |
|---|---|---|---|
| 2026-06 | $10,790.52 | $117.45 | -$5,794.30 |
| 2026-07 | $25,925.83 | $988.15 | -$25,893.81 |
| 2026-08（至 08-20） | $7,277.46 | $242.93 | -$7,253.33 |

同一个账单账户上还挂着一条运维自建的预算：
**`GFS Y1 cumulative drawdown (100k, to 2027-06-15)`**，自定义周期
2026-06-16 → 2027-06-15，额度 $100,000，口径 `EXCLUDE_ALL_CREDITS`。
按该预算的口径实算（[q5-gfs.sql](q5-gfs.sql)）：

| 项 | 值 |
|---|---|
| 2026-06-16 → 08-20 已消耗 gross | **$39,106.85 / $100,000（39.1%）** |
| 其中 `oratis-491316` | **$1,278.83（占 3.3%）** |
| 全周期日均 | $592.5/天（**被 7 月的 $25.9k 尖峰主导**） |
| 最近 14 天日均 | **$200.6/天** |
| 最近 7 天日均 | **$190.7/天** |
| 按最近 14 天速率外推剩余 $60,893 | **约 304 天 → 2027 年 6 月中旬**，与窗口终点基本重合 |

**含义（三条，方向不同，都要说）：**

1. 「上线前已经烧掉 $294」在**现金口径**上不成立 —— 烧的是抵扣额度，本项目本窗口现金支出约 $6。
2. **抵扣池按当前速率大致能撑满一年**，不是马上见底。所以「成本压力」不是上线的紧迫理由。
3. ⚠️ **但这个池子 babel.plus 只占 3.3%，控制权不在本项目手里。**
   2026-07 一个月就消耗了 $25.9k（≈ 全年额度的四分之一，绝大部分是同账户的 Vertex AI）。
   **同账户其它项目再来一次 7 月量级的月份，跑道就少掉三个月。**
   因此：定价基准应当继续用 **gross**（把抵扣当跑道，不当成本结构），
   而 [as-built §10.3](../../02-architecture/as-built-gcp.md) 与
   [pricing](../../03-product/pricing-and-plans.md) 用 gross 口径是对的，不需要改。

> 抵扣的**准确余额与到期日**无法从账单导出里查到（导出只有已消耗的 credit 行，没有余额）。
> 上表的「剩余」是用 $100,000 减去实算 gross 得到的**推算值**，前提是那条预算的额度与口径
> 确实对应这笔 GFS 抵扣。要确证需去 Billing 控制台看抵扣余额页。

## 5 · 这些证据证明什么、不证明什么

**证明：**
- 本窗口内 `oratis-491316` 的出口流量在 SKU 级别的完整构成、用量与 gross 金额。
- 「到中国大陆」的 Internet DTO SKU 在本窗口用量为零；对应字节走 Carrier Peering。
- 全部 gross 被账单账户级 PROMOTION 抵扣，本项目本窗口现金支出约 $6。
- as-built §10.3 的 2,927 GiB / $294.12 是部分日快照，完整值为 3,399.0 GiB / $332.91。

**不证明：**
1. **不证明流量属于哪台实例。** 标准账单导出没有 `resource` 字段，`system_labels` 里
   也没有网络类 SKU 的 `instance_name`（查询里试过，全为空）。
   「这是 `vpn-us` + `vpn-jp` 打的」是**按区域推断**的（两个区域里只有这两台机器在跑代理），
   不是账单直接给出的归属。
2. **不证明未来的单价。** Carrier Peering 的归类由 Google 侧的对端网络决定，不是可选参数；
   目的地运营商换了对端、或 Google 调整分类，单价就会变。本窗口的 $0.080–0.085 是**已发生**，
   不是**承诺**。
3. **不证明 Standard 会省多少。** §2.1 第 3 条只给了「省得比 2.09× 少」这个上界，
   没有给具体金额；且 Carrier Peering 部分在 Standard 下是什么 SKU、什么价，本窗口无数据。
   **`nettier-ab-*` 实测仍然是那个决定的前置条件。**
4. **不证明 08-17 的阶跃会持续。** 四天不是趋势。
5. **不证明抵扣的余额与到期日。** §4.2 的「剩余 $60,893 / 约 304 天」是
   「预算额度 − 实算 gross」的**推算**，不是查到的余额。见 §4.2 末尾的注。
6. **不证明本项目的现金成本会一直是零。** §4.2 第 3 条给的是相反方向的风险。

## 6 · 复现

六条查询都在本目录里，可直接 `bq --project_id=loopback-500616 query --use_legacy_sql=false < <file>`：

| 查询 | 产出 | 对应本文 |
|---|---|---|
| [q1-sku-breakdown.sql](q1-sku-breakdown.sql) | [sku-breakdown.csv](sku-breakdown.csv) | §2 |
| [q2-daily.sql](q2-daily.sql) | [daily-egress.csv](daily-egress.csv) | §3、§4.1 |
| [q3-credits.sql](q3-credits.sql) | [credits.csv](credits.csv) | §4.2 |
| [q4-account.sql](q4-account.sql) | [billing-account-monthly.csv](billing-account-monthly.csv) | §4.2 |
| [q5-gfs.sql](q5-gfs.sql) | （单行，见 §4.2 表） | §4.2 |
| [q6-burn.sql](q6-burn.sql) | [account-daily-burn.csv](account-daily-burn.csv) | §4.2 |
| [q7-project-daily.sql](q7-project-daily.sql) | [project-daily-gross.csv](project-daily-gross.csv) | 给项目级 budget 定额度，见 [gcp-inventory-20260821 §4](../gcp-inventory-20260821/) |

> 跑数身份：`wangharp@gmail.com`。账单导出在项目 `loopback-500616`，
> 与被计费的 `oratis-491316` **不是同一个项目** —— 查询要指定 `--project_id=loopback-500616`。

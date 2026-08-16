# babel.plus 文档体系

> 日期：2026-08-16 · 性质：**机制说明** · 状态：**执行中**（v1，2026-08-16）
> 事实基线：约定吸收自 `DiogenesModel/Diogenes`，取舍理由见
> [01-research/reference-repos.md](01-research/reference-repos.md) §3.2
> 读者：所有参与本项目的人与 Agent。**动笔写任何文档前先读本文。**

---

## 1 · 目录

| 目录 | 放什么 | 不放什么 |
|---|---|---|
| [`00-overview/`](00-overview/) | 产品定位、范围、路线图、术语表 | 具体技术方案 |
| [`01-research/`](01-research/) | 竞品、协议、市场、支付、参考项目调研 | 我们自己的决策 |
| [`02-architecture/`](02-architecture/) | 系统架构、数据模型、API 契约、**As-Built 现状** | 尚未裁决的方案（那属于 ADR） |
| [`03-product/`](03-product/) | 套餐设计、用户旅程、页面清单、用户教程规格 | 实现细节 |
| [`04-ops/`](04-ops/) | 部署手册、runbook、排障 SOP、监控告警 | 一次性的调研 |
| [`05-adr/`](05-adr/) | 架构裁决记录（一次一决策，写完不改） | 长期维护的活文档 |
| [`evidence/`](evidence/) | 实测原始数据：测速输出、抓包、探活 JSON、截图 | 结论（结论写进正文文档） |

**每个目录必须有 `README.md` 作为索引。** 这一条是对 Diogenes 的**修正**而非照抄 ——
其 `DataMiner/docs/` 累积到 255 篇后已难以导航，我们从第一天就避免。

---

## 2 · 文档头（强制）

**不使用 YAML frontmatter。** 元信息一律写在 H1 之后紧跟的引用块里：

```markdown
# ⟨编号 · ⟩⟨一句话结论式标题，不是话题名⟩

> 日期：YYYY-MM-DD · 性质：**⟨受控词表⟩** · 状态：⟨成熟度 + 日期⟩
> 事实基线：⟨commit / 实测环境 / 数据来源⟩
> 关联：[⟨文档⟩](…)、#issue、PR
> ⟨调研类追加⟩ 证据口径：一手实测=高；官方文档=中；社区单一来源=待核实
> ⟨手册类追加⟩ 读者：⟨谁会在什么场景下打开这份文档⟩
```

理由：元信息对人类直接可读、可 `grep`、不依赖任何渲染工具。

### 2.1 `性质：` — 受控词表（只能从这里选）

`架构裁决` · `调研` · `设计方案` · `排期计划` · `执行手册` ·
`证据型核查` · `机制说明` · `复盘` · `对外需求书`

### 2.2 `状态：` — 必须带日期

`草稿` · `设计稿 vN` · `设计冻结稿` · `待实施` · `执行中` ·
`As-Built` · `已归档` · `提案，未批准`

> **硬规矩：始终区分「设计目标 / 当前实现 / 测试结果」三层，不要混写。**
> `02-architecture/as-built-*.md` 只写**已经存在**的东西；规划中的能力写进 ADR 或设计稿。

---

## 3 · 文件命名

| 形态 | 用于 | 例 |
|---|---|---|
| `<slug>.md` | 长期维护的活文档 | `as-built-gcp.md`、`architecture.md` |
| `NNNN-<slug>.md` | ADR，四位序号，**唯一且只增不减** | `05-adr/0001-protocol-stack.md` |
| `rca-<slug>.md` | 故障复盘，独立命名空间 | `rca-jp-node-ip-blocked.md` |
| `evidence/<topic>-YYYYMMDD/` | 证据目录 | `evidence/hy2-throughput-20260816/` |

slug 一律**小写英文连字符**，即使正文是中文。文件名要能自解释，宁长勿短。

---

## 4 · ADR 规矩

1. **一份 ADR = 一个决策**，写完基本不改。
2. **推翻旧裁决时不删不改不加 DEPRECATED**，而是**写一份新 ADR**，
   在头部写 `**推翻 [NNNN 号 §x](NNNN-….md)**`，并用表格**逐条**交代
   旧裁决的每一条理由在新架构下的落点（不再适用 / 保留 / 行为变化）。
3. 被推翻的旧 ADR 保持原样，靠双向链接构成裁决谱系。

> **一条裁决被推翻时，它的理由不会自动消失。**

### 4.1 每份裁决与计划强制两个尾节

```markdown
## N · 代价
> ⚠️ 这一选择的代价，必须留在记录里而不是被措辞掩盖：
> 1. …（量化，带数字）
> 2. …（写明什么情况下这个取舍不再成立）

## N+1 · 这次没有解决的
- [ ] …（每条说清楚为什么不在本次范围内）
```

这是整套约定里**最有价值的一条**，不允许省略。

---

## 5 · 写作风格

1. **结论前置** — 第一节写裁决/结论，理由在后。RCA 的 H1 写**根因**不写现象。
2. **一切带数字与出处** — 不写「性能提升明显」，写
   「单流 4.6 倍，1094 → 1700 KB/s，同时段交叉轮询 4 轮」。
3. **显式区分事实层级** — 已落地 / 未落地；一手实测 / 官方文档 / 社区传闻。
   凡未经核实的，标 **待核实**；凡需实测的，标 **需实测**。
4. **不确定就说不确定** — 编造一个数字比留空危害大得多。
5. **中文散文 + 英文标识符** — 路径、字段名、命令、错误信息保持英文原样，不翻译。
6. **`·` 作为 H1 与引用块内的分隔符**，全仓一致。
7. **不重复生成长设计文档** — 更新既有活文档，而不是新写一份并列的。

---

## 6 · 索引

每个目录另有自己的 `README.md`，含更细的清单与待写项。

| 目录 | 文档 | 状态 |
|---|---|---|
| **00-overview** | [product-brief.md](00-overview/product-brief.md) — 产品定位与范围 | 设计稿 v1 |
| | [roadmap.md](00-overview/roadmap.md) — 路线图与排期 | 设计稿 v1 |
| | [glossary.md](00-overview/glossary.md) — 术语表 | 执行中 |
| **01-research** | [competitor-conyss.md](01-research/competitor-conyss.md) — 竞品一手走查 | 已完成 |
| | [reference-repos.md](01-research/reference-repos.md) — Proxy_Skill + Diogenes | 已完成 |
| | [protocol-and-infra.md](01-research/protocol-and-infra.md) — 协议与基础设施 | 已完成 |
| | [panels-and-market.md](01-research/panels-and-market.md) — 面板与市场 | 已完成 |
| | [payments.md](01-research/payments.md) — 支付与计费 | 已完成 |
| | [admin-support-docs.md](01-research/admin-support-docs.md) — 后台/工单/通知/文档站 | 已完成 |
| **02-architecture** | [as-built-gcp.md](02-architecture/as-built-gcp.md) — GCP 资产清点 | **As-Built**（2026-08-16） |
| | [system-design.md](02-architecture/system-design.md) — 系统架构 | 设计稿 v1，未实施 |
| | [data-model.md](02-architecture/data-model.md) — 完整数据模型 DDL | 设计稿 v1 |
| | [api-contract.md](02-architecture/api-contract.md) — 三套 API 契约 | 设计稿 v1 |
| **03-product** | [pricing-and-plans.md](03-product/pricing-and-plans.md) — 套餐与定价 | **草稿**，价格待实测 |
| | [tutorials-spec.md](03-product/tutorials-spec.md) — 教程体系规格 | 设计稿 v1 |
| | [user-journey.md](03-product/user-journey.md) — 用户旅程 | 设计稿 v1 |
| | [page-inventory.md](03-product/page-inventory.md) — 页面清单 | 设计稿 v1 |
| **04-ops** | [runbook-node-health.md](04-ops/runbook-node-health.md) — 节点健康与封锁取证 | 设计稿 v1 |
| | [node-provisioning.md](04-ops/node-provisioning.md) — 建机与装机 | 设计稿 v1 |
| | [deploy.md](04-ops/deploy.md) — API/Web 部署 | 设计稿 v1 |
| | [monitoring.md](04-ops/monitoring.md) — 监控与告警 | 设计稿 v1 |
| | [local-development.md](04-ops/local-development.md) — 本地开发（无需装 Go） | **As-Built** |
| **05-adr** | [0001](05-adr/0001-cloudflare-tos-risk.md) — CF 只承载控制面 | **提案，未批准** |
| | [0002](05-adr/0002-notification-channels.md) — 邮件是唯一失联恢复通道 | 设计稿 v1 |
| | [0003](05-adr/0003-web-hosting-and-reachability.md) — 托管按实测可达性选型 | 设计稿 v1 |
| | [0004](05-adr/0004-transport-hardening.md) — 传输层按特征混同调参 | 设计稿 v1 |
| | [0005](05-adr/0005-database-selection.md) — Cloud SQL Postgres 17 | 设计稿 v1 |
| | [0006](05-adr/0006-api-stack.md) — Go + OpenAPI spec-first | 设计稿 v1 |
| | [0007](05-adr/0007-node-migration.md) — 节点混合迁移 | 设计稿 v1 |
| | [0008](05-adr/0008-network-tier-standard.md) — 网络层级改 Standard（**推翻 0004 §3.7**） | 设计稿 v1 |
| **evidence** | *（空）* — 全部 P0 阻塞实测项见 [evidence/README.md](evidence/README.md) | 待采集 |

---

## 7 · 当前阻塞项（读到这里的人请先看这个）

按影响面排序。**前两条不解决，后面所有设计都是悬空的。**

| # | 阻塞项 | 卡住了什么 | 归属 |
|---|---|---|---|
| 1 | **[ADR 0001](05-adr/0001-cloudflare-tos-risk.md) 未获批准** | Cloudflare 数据面用不用，决定整个拓扑与成本模型 | **需用户决策** |
| 2 | **零实测数据** | 协议选型、区域选型、网络层级、定价、托管选型全部悬空 | 需搭测试环境 |
| 3 | ~~数据库选型未裁决~~ | **已解决** — [ADR 0005](05-adr/0005-database-selection.md) | ✅ |
| 4 | 支付通道未落实 | 收款闭环 | 需申请与尽调 |
| 5 | 邮件送达率未验证 | [ADR 0002](05-adr/0002-notification-channels.md) 的整个前提 | 需实测 |

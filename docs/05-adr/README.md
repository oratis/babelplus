# 05-adr · 架构裁决记录

**一份 ADR = 一个决策，写完基本不改。**

## 推翻旧裁决的规矩

不删、不改、不加 DEPRECATED 前缀。写一份**新** ADR，在头部标
`**推翻 [NNNN 号 §x](NNNN-….md)**`，并用表格**逐条**交代旧裁决的每一条理由
在新架构下的落点（不再适用 / 保留 / 行为变化）。

> **一条裁决被推翻时，它的理由不会自动消失。**

每份 ADR 强制两个尾节：**`代价`** 与 **`这次没有解决的`**。不允许省略。

| # | 裁决 | 状态 |
|---|---|---|
| [0001](0001-cloudflare-tos-risk.md) | Cloudflare 只承载控制面，不承载中转数据面 | **已批准**，待实施（2026-08-17） |
| [0002](0002-notification-channels.md) | 邮件是唯一的失联恢复通道，Telegram 只能做锦上添花 | 设计稿 v1，待实施 |
| [0003](0003-web-hosting-and-reachability.md) | 控制面托管按实测可达性选型，必须用自有域名 + 镜像 | 设计稿 v1，待实施 |
| [0004](0004-transport-hardening.md) | 传输层按「特征混同」而非「性能最优」调参 | 设计稿 v1，待实施 |
| [0005](0005-database-selection.md) | Cloud SQL Postgres 17 + Unix socket，在线态用 UNLOGGED 表不买 Redis | 设计稿 v1，待实施 |
| [0006](0006-api-stack.md) | Go + chi + pgx/sqlc + OpenAPI spec-first，理由是与节点端同语言生态 | 设计稿 v1，待实施 |
| [0007](0007-node-migration.md) | 混合迁移：新建 bp-node-hk1，vpn-us/vpn-jp 原封不动 | 设计稿 v1，待实施 |
| [0008](0008-network-tier-standard.md) | 网络层级改用 Standard，放弃为 IPv6 支付 Premium 溢价 —— **推翻 0004 §3.7** | **已实施（仅新节点）**，既有 `vpn-*` 核实为 Premium 且不迁 |

## 待写
- [ ] `0009` — 旧节点退役（需 bp 侧连续 30 天零回滚事件后才写）
      （编号原写 `0008`，与已发布的网络层级裁决重号，2026-08-21 改正）
- [ ] `0010` — 域名策略（system-design §2 与 §4.1 自相矛盾，P1 全线前置）
- [ ] `0011` — 域名失联的自动检测（七处登记的洞；在它之前「≤ 30 分钟恢复」零机制支撑）

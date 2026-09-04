# 02-architecture · 架构

**严格区分两类文档：**

- `as-built-*.md` — **只写已经存在的东西**，状态标 `As-Built` + 日期。
- `system-design.md` 等 — 设计目标，状态标 `设计稿 vN`。

混写这两者是本项目最容易犯的错误。

| 文档 | 性质 | 状态 |
|---|---|---|
| [as-built-gcp.md](as-built-gcp.md) | 证据型核查 | **As-Built**（§2–§6 为 2026-08-16 快照；§10 为 2026-08-20 复核） |
| [as-built-personal-fleet.md](as-built-personal-fleet.md) | 证据型核查 | **As-Built**（2026-09-04 实查快照，不回改）—— 自用机队 `vpn-*`：2 台机 / 7 条通路 / 🔴 443 入向挂在无 tag 规则上 |
| [system-design.md](system-design.md) | 设计方案 | 设计稿 v1（2026-08-16，未实施） |
| [data-model.md](data-model.md) | 设计方案 | 设计稿 v1（2026-08-16） |
| [api-contract.md](api-contract.md) | 设计方案 | 设计稿 v1（2026-08-16） |

> `openapi/openapi.yaml` 是 API 契约的**事实源**，本目录的 `api-contract.md` 是它的设计说明与背景。
> 两者不一致时以 `openapi/openapi.yaml` 为准。

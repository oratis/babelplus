# 04-ops · 运维

部署手册、runbook、排障 SOP、监控告警。**面向值班的人，出事时会被打开。**

写作要求：可直接照做，命令可复制，判据可验证。不要写「检查一下网络」这种话。

| 文档 | 性质 | 状态 |
|---|---|---|
| [runbook-node-health.md](runbook-node-health.md) | 执行手册 | 设计稿 v1（未经本项目实战验证） |
| [personal-fleet-runbook.md](personal-fleet-runbook.md) | 执行手册 | 设计稿 v1（2026-09-04）—— 自用机队的扩容 / 订阅热更新 / 每日巡检与飞书日报。**三节全部未在真机上执行过** |
| [node-provisioning.md](node-provisioning.md) | 执行手册 | 设计稿 v1（2026-08-16） |
| [deploy.md](deploy.md) | 执行手册 | **执行中**（2026-08-20 —— `bp-api` / `bp-db` 已上线；§5 示例命令与线上不一致，以 `infra/deploy/` 为准） |
| [monitoring.md](monitoring.md) | 执行手册 | 设计稿 v1（2026-08-16） |
| [local-development.md](local-development.md) | 执行手册 | **As-Built**（2026-08-16 实测通过） |

> 这些手册的可执行形式在 `infra/` 下。手册与脚本不一致时，**以脚本为准并回头修手册**。

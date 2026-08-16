# babel.plus

内部使用的流量中转服务 —— 让中国境内用户经由 Cloudflare 边缘 + Google Cloud 出口，
稳定访问全球网络与服务。配套完整的账户、订阅、计费、后台与工单体系。

> **状态：P0 调研与设计阶段。** 目前仓库中只有文档，尚无实现代码。
> 先读 [`docs/00-overview/product-brief.md`](docs/00-overview/product-brief.md)。

---

## 快速导航

| 我想… | 去这里 |
|---|---|
| 了解这个项目做什么、不做什么 | [产品定位与范围](docs/00-overview/product-brief.md) |
| 看竞品长什么样 | [Conyss 竞品调研](docs/01-research/competitor-conyss.md) |
| 了解协议选型与基础设施 | [协议与基础设施调研](docs/01-research/protocol-and-infra.md) |
| 知道 GCP 上现在有什么 | [GCP 资产清点（As-Built）](docs/02-architecture/as-built-gcp.md) |
| 写文档 | [文档体系约定](docs/README.md) ← **动笔前必读** |
| 查一条决策为什么这么定 | [架构裁决记录](docs/05-adr/) |

---

## 仓库结构

```
babel.plus/
├── docs/                  # 全部调研、设计、裁决、手册（详见 docs/README.md）
├── apps/
│   ├── api/               # API 服务（独立部署）
│   └── web/               # 用户面板 + 后台（独立部署）
├── infra/                 # IaC、节点 provisioning、部署脚本
└── AGENTS.md              # 给 Agent 的项目规则
```

> `apps/` 与 `infra/` 尚未创建 —— P1 阶段落地。

---

## 关键约束

1. **API 与 Web 分开部署。** 各自独立域名池，互为备份；避免一个域名被封导致全站失效。
2. **部署在 GCP 项目 `oratis-491316`，不得影响已有服务。**
   现有资产（`vpn-us` / `vpn-jp` 节点、3 个 Cloud Run 服务）清单见
   [as-built-gcp.md](docs/02-architecture/as-built-gcp.md)。
   新资源一律 `bp-` 前缀 + 独立网络标签。
3. **邀请制，不开放公开注册。**
4. **钱包余额仅可消费，不可提现。**
5. **不承诺流媒体解锁** —— GCP IP 段普遍被主流流媒体平台封禁，这是选择 GCP 出口的已知代价。

---

## 历史说明

本仓库此前存放的是一份 MediaWiki 1.45 的部署副本，已于 2026-08-16 清空。
如需找回，见提交 `89c9b6cb3c8` 及其之前的历史。

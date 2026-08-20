# babel.plus

内部使用的流量中转服务 —— 让中国境内用户经由 Cloudflare 边缘 + Google Cloud 出口，
稳定访问全球网络与服务。配套完整的账户、订阅、计费、后台与工单体系。

> **状态：P0 设计已完成，P1 脚手架已落地，GCP 控制面已部署，但产品尚未上线。**
> 契约（`openapi/`）、API 骨架（`api/`）、前端工作区（`web/`）、部署脚本（`infra/`）都已建起来，
> 但 128 个 operation 里仍有 122 个返回 `501`
> （[local-development.md §4](docs/04-ops/local-development.md)）。
> **GCP 上已经有 `bp-` 资源**：`bp-api`（Cloud Run，`us-central1`，2026-08-17 创建）、
> `bp-db`（Cloud SQL PostgreSQL 17，`db-f1-micro`）、`bp-api-sa` 与 4 个 `bp-` secret，
> 2026-08-20 `gcloud` 复核时都在运行 —— 清单与参数见
> [as-built-gcp.md §10](docs/02-architecture/as-built-gcp.md)。
> 先读 [`docs/00-overview/product-brief.md`](docs/00-overview/product-brief.md)，
> 再读 [`CONTRIBUTING.md`](CONTRIBUTING.md)。

> ⚠️ **产品还没上线，出口流量的钱已经在花。**
> 2026-06-28 → 08-20 的账单（BigQuery 导出 `loopback-500616.billing_export`，gross）：
> `vpn-us` + `vpn-jp` 两个出口节点的流量合计 **2,927 GiB / $294.12**，即 **$0.1005/GiB**，
> 与 Standard Tier 目录价 $0.11/GiB 吻合。
> 竞品零售约 $0.042/GB —— **按竞品价卖每 GB 净亏约 $0.06**。
> 单位经济见 [pricing-and-plans.md §2](docs/03-product/pricing-and-plans.md)。

---

## 快速导航

| 我想… | 去这里 |
|---|---|
| **把项目在本机跑起来** | [本地开发（无需装 Go）](docs/04-ops/local-development.md) |
| **提第一个 PR** | [CONTRIBUTING.md](CONTRIBUTING.md) ← **动手前必读** |
| 了解这个项目做什么、不做什么 | [产品定位与范围](docs/00-overview/product-brief.md) |
| 看竞品长什么样 | [Conyss 竞品调研](docs/01-research/competitor-conyss.md) |
| 了解协议选型与基础设施 | [协议与基础设施调研](docs/01-research/protocol-and-infra.md) |
| 知道 GCP 上现在有什么 | [GCP 资产清点（As-Built）](docs/02-architecture/as-built-gcp.md) |
| 写文档 | [文档体系约定](docs/README.md) ← **动笔前必读** |
| 查一条决策为什么这么定 | [架构裁决记录](docs/05-adr/) |

---

## 仓库结构

**单仓，分开部署**（[ADR 0006 §13](docs/05-adr/0006-api-stack.md)）。

```
babel.plus/
├── openapi/
│   └── openapi.yaml       # 🔴 全仓契约的唯一事实源，128 个 operation
├── api/                   # Go 服务 → Cloud Run (bp-api)
│   ├── cmd/server/        # 入口
│   ├── internal/gen/      # oapi-codegen 生成物，禁止手改
│   ├── internal/handler/  # 实现；unimplemented.gen.go 是生成的 501 兜底
│   ├── db/migrations/     # 12 组 up/down
│   ├── db/queries/        # sqlc 输入
│   ├── db/gen/            # sqlc 生成物，禁止手改
│   └── Makefile           # 全部目标走容器，本机不需要装 Go
├── web/                   # pnpm workspace → 静态托管 (bp-web)
│   ├── shared/            # 共享客户端与组件；shared/api/ 是 openapi-typescript 生成物
│   ├── user/              # 用户面板
│   └── admin/             # 后台
├── infra/                 # GCP 资源创建、节点 provisioning、部署脚本
├── docs/                  # 全部调研、设计、裁决、手册（详见 docs/README.md）
├── .github/workflows/     # ci.yml（PR/push）· deploy.yml（手动）
├── AGENTS.md              # 给 Agent 的项目规则
└── CONTRIBUTING.md        # 怎么跑、怎么提交、什么时候要写 ADR
```

单仓不等于单体部署：CI 与部署都按路径过滤，
`api/**` 与 `openapi/**` 走 API 侧，`web/**` 与 `openapi/**` 走 Web 侧。

> **四处生成物必须与源文件一起提交**，CI 用 `git diff --exit-code` 卡漂移：
> `api/internal/gen/`、`api/db/gen/`、`api/internal/handler/unimplemented.gen.go`、
> `web/shared/api/`。详见 [CONTRIBUTING.md](CONTRIBUTING.md)。

---

## 关键约束

1. **API 与 Web 分开部署。** 各自独立域名池，互为备份；避免一个域名被封导致全站失效。
2. **部署在 GCP 项目 `oratis-491316`，不得影响已有服务。**
   现有资产（`vpn-us` / `vpn-jp` 节点、3 个 Cloud Run 服务）清单见
   [as-built-gcp.md](docs/02-architecture/as-built-gcp.md)。
   新资源一律 `bp-` 前缀 + 独立网络标签 —— 已建成的
   `bp-api` / `bp-db` / `bp-api-sa` 都遵守了这一条（as-built §10）。
3. **邀请制，不开放公开注册。**
4. **钱包余额仅可消费，不可提现。**
5. **不承诺流媒体解锁** —— GCP IP 段普遍被主流流媒体平台封禁，这是选择 GCP 出口的已知代价。

---

## 历史说明

本仓库此前存放的是一份 MediaWiki 1.45 的部署副本，已于 2026-08-16 清空。
如需找回，见提交 `89c9b6cb3c8` 及其之前的历史。

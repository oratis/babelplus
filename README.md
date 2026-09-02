# babel.plus

内部使用的流量中转服务 —— 让中国境内用户经由 Cloudflare 边缘 + Google Cloud 出口，
稳定访问全球网络与服务。配套完整的账户、订阅、计费、后台与工单体系。

> **状态：控制面已上线（2026-08-31），第一台出口节点已端到端接通（2026-09-01），但产品还不能卖 —— 单节点、单协议、真实收款 0 笔。**
> 契约（`openapi/`）、API（`api/`）、前端工作区（`web/`）、部署脚本（`infra/`）都已建起来。
> **128 个 operation 里已实现 120 个，仍有 8 个返回 `501`**
> （2026-08-30 实数：`operations.txt` 与各非生成文件的 `func (s *Server) X` 取交集得 123，
> 减去 3 条**契约自己声明为 501** 的用户侧 TOTP = 120；
> 剩余 8 条逐条有阻塞原因，清单钉在 `api/internal/handler/unimplemented_test.go` 里 ——
> 5 条缺表（`domains` / `mail_templates`）、3 条契约声明未实现。
> 另有 2 条是「主路径已实现、保留一个分支 501」。
> 另见 [local-development.md §4](docs/04-ops/local-development.md)）。
>
> ✅ **2026-08-31：仓库口径与线上口径第一次对齐了。**
> 生产 `bp-api` 的 serving revision 是 `bp-api-87886e4`，就是 master。
> `bp-db` 的迁移版本 **13 → 19**（48 张基表）。用户面 11 条端点实测 200，
> 注册 → 登录 → 下单 → 取消整条链路在真库上跑通过。
> 此前这里长期写着「生产落后 master 14 个提交、实现数 18/128」—— 那句话现在不成立了。
>
> ✅ **2026-09-01：第一台出口节点 `bp-node-hk1` 接通。** 未手改的订阅在 mihomo 与 sing-box 各加载一次，
> 出口 IP 均为节点 IP；15 MB 下载入账误差 0.3%。P1 出口标准 **0/8 → 3.5/8**
> （证据 [node-bringup-20260901](docs/evidence/node-bringup-20260901/)）。
>
> ⚠️ **但「接通」仍然不等于「可以卖」，三件事照旧**：
> **只有一台节点、只有 REALITY 一条通路**（HY2 / SS-2022 未启用，任何一条出问题就是全线中断）；
> **真实收款 0 笔**（「下单 → 付款 → 自动开通」一次都没真跑过，运维账号的套餐是 SQL 开的）；
> **`deploy.yml` 仍然从未运行过**（2026-09-02 起 WIF 与两个仓库变量已配好，见 roadmap B47）。
>
> **GCP 上已经有 `bp-` 资源**：`bp-api`（Cloud Run，`us-central1`，2026-08-17 创建）、
> `bp-db`（Cloud SQL PostgreSQL 17，`db-f1-micro`）、`bp-api-sa` 与 4 个 `bp-` secret，
> 2026-08-20 `gcloud` 复核时都在运行 —— 清单与参数见
> [as-built-gcp.md §10](docs/02-architecture/as-built-gcp.md)。
> **2026-08-31 首次上线后新增**：`bp-web` 与 `bp-admin`（两个 SPA 的静态托管，Cloud Run）、
> `bp-migrate`（迁移 Job，实际上 2026-08-17 就建好了，此前文档一直记成「未建」）、
> **8 条 `bp-` Cloud Scheduler 作业**（内部定时面，OIDC 调 `/internal/tasks/*`，实测 200）。
> `bp-` 告警策略：2026-09-01 从 0 条到 3 条（手工），**2026-09-02 ADR 0014 批准后由 `setup-alerts.sh` 接管**
> （实建清单见 [first-deploy §4.5](docs/04-ops/first-deploy-20260831.md)）。
>
> ✅ 2026-08-31 起 `web.` / `admin.` / `api.babel.plus` 三个子域名经一个 GCLB 接入
> （[ADR 0016](docs/05-adr/0016-domain-babelplus.md)），`web.` / `api.` 钉 Let's Encrypt，
> `admin.` 走 IAP 保留 Google 托管证书。此前「两个 SPA 共享 `*.run.app`」的过渡形态已结束。
> 先读 [`docs/00-overview/product-brief.md`](docs/00-overview/product-brief.md)，
> 再读 [`CONTRIBUTING.md`](CONTRIBUTING.md)。

> ⚠️ **产品还没上线，出口流量的钱已经在花。**
> 2026-06-28 → 08-20 的账单（BigQuery 导出 `loopback-500616.billing_export`，gross）：
> `vpn-us` + `vpn-jp` 两个出口节点的流量合计 **2,927 GiB / $294.12**，即 **$0.1005/GiB** ——
> 这是 **Premium 网络层**下、跨两个区域、Internet Data Transfer 与 Carrier Peering
> 两类 SKU 的**混合**单价，不对应目录里任何单独一档。
> 竞品零售约 $0.042/GB —— **按竞品价卖每 GB 净亏约 $0.06**。
> [ADR 0008](docs/05-adr/0008-network-tier-standard.md) 裁决改用 Standard。
> **它的落地是半边的，两半都要说**：`infra/node/create-node.sh` 已硬编码
> `NETWORK_TIER="STANDARD"` 并在建完之后读回断言一次（PR #10，2026-08-21 合并），
> 所以**今后每一台新节点都是 Standard**；而 2026-08-20 实查的两台既有节点
> `vpn-us` / `vpn-jp` 与它们的两个静态 IP **仍全在 `PREMIUM` 层且裁决明定不迁**。
> 于是**现在在花钱的那部分流量，一分钱都还没省下来** —— 自有 Standard 节点 1 台（`bp-node-hk1`），
> 但它上面只有运维自己一个账号，真实流量仍全部走那两台 Premium 老节点。
> （此前本行写「该裁决至今未实施」，只说了后半边；ADR 0008 头部写「已实施（仅新节点）」，
> 只说了前半边。两种读法各对一半，这里合成一句。）
> 单位经济与这个待评估的成本杠杆见
> [pricing-and-plans.md §2](docs/03-product/pricing-and-plans.md)。

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
│   ├── db/migrations/     # 19 组 up/down（0001–0019，2026-08-30 实数），47 张表
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

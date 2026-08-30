# 首次上线记录 —— 2026-08-31

> 日期：2026-08-31 · 性质：**As-Built 记录**（发生了什么，不是计划）
> 状态：**已完成**（2026-08-31，控制面上线；数据面仍为 0 台节点）
> 事实基线：全部命令与响应均在 `oratis-491316` 上真实执行，本文只记**执行过的**动作与**观测到的**结果。
> 关联：[deploy.md](deploy.md)（部署手册）、[as-built-gcp.md](../02-architecture/as-built-gcp.md)、
> [roadmap](../00-overview/roadmap.md) B47 / B51 / B53 / B54
> 读者：值班运维、下一个接手部署的人。**要复现本次上线看 §2；要知道还欠着什么看 §5。**

---

## 1 · 一句话

`bp-api` 从 `618bf1c`（2026-08-23，实现数 18/128）推到 master `87886e4`，
`bp-db` 迁移 **13 → 19**，新建 `bp-web` / `bp-admin` 两个 SPA 托管服务与 8 条 Cloud Scheduler。
**这是本项目第一次「仓库口径 = 线上口径」。**

---

## 2 · 实际执行的顺序

| # | 动作 | 命令 | 结果 |
|---|---|---|---|
| 1 | 部署前隔离快照 | `verify-isolation.sh --out=<dir>` | 16/16 通过 |
| 2 | 迁移 | `infra/migrate/build-and-run.sh` | 13 → 19，48 张基表，exit 0 |
| 3 | 两个 SPA 构建镜像 | `gcloud builds submit`（nginx + dist） | SUCCESS |
| 4 | 部署 `bp-web` / `bp-admin` | `gcloud run deploy` | 各 1 个修订版，100% 流量 |
| 5 | 部署 `bp-api` 候选 | `deploy-api.sh --yes` | `bp-api-87886e4`，**0% 流量** |
| 6 | 候选验证 | 见 §3 | 全部符合预期 |
| 7 | 切流量 | `deploy-api.sh --no-build --tag=87886e4 --promote` | 100% |
| 8 | 定时面 | `setup-scheduler.sh --only=scheduler --apply --yes` | 8 条 Scheduler |
| 9 | 部署后隔离核对 | `verify-isolation.sh --baseline=<dir>` | 18/18 通过，非 `bp-` 资源逐字节未变 |

**迁移在切流量之前**：0016 会 `DROP COLUMN users.transfer_enable` 再以生成列加回，
旧修订版此刻仍在服务。本次的窗口安全是因为 `users` 表当时 **0 行** ——
**下一次不会有这个便利**，届时必须按 deploy.md §12.3 的 expand/contract 分两次发。

### 2.1 两处必须先修才能部署的缺陷（都是本次上线暴露的）

1. **`deploy-api.sh` 的 `--set-env-vars` 分隔符是 `@`，而服务账号 email 正中它。**
   加 `BP_INTERNAL_TASK_CALLERS` 后首次真部署即 `Bad syntax for dict arg`。
   失败发生在 gcloud 参数解析阶段，**没有产生修订版**。改用 `;;`（PR #21）。
2. **`orders.surplus_order_ids` 传显式 NULL，覆盖了列上的 `DEFAULT '{}'`。**
   现象是**新购与续费一律 500，升级单正常**。1,263 个进程内单测全部漏掉它 ——
   假的 `CreateOrder` 不执行 NOT NULL。**它是在真库上下第一单的那一刻暴露的**（PR #22）。

> 这两条合起来说明一件事：**「CI 全绿」与「能上线」之间隔着一次真实部署。**
> 本次上线本身就是发现它们的唯一手段。

---

## 3 · 实测结果（全部是观测，不是推断）

**鉴权边界**

| 探针 | 期望 | 实测 |
|---|---|---|
| `GET /-/healthz` | 200 `ok` | ✅ |
| `GET /api/v1/plans` 无凭据 | 401 | ✅ `AUTH_TOKEN_INVALID` |
| `GET /api/v1/admin/dashboard` | 403 | ✅ `AUTH_PERMISSION_DENIED` |
| 同上 + **伪造** `x-goog-iap-jwt-assertion` | 403 | ✅ **验签名，不信头的存在** |
| `POST /internal/tasks/*` 无凭据 | 403 | ✅ |
| 同上，Cloud Scheduler 带 OIDC | 200 | ✅ |
| CORS：白名单 Origin | 带 ACAO | ✅ |
| CORS：`evil.example.com` | **无** ACAO | ✅ |

**用户面全链路**（真实注册的 `demo@babel.plus`）

`/user/me` `/plans` `/user/subscription` `/user/wallet` `/user/nodes` `/orders`
`/notices` `/tickets` `/user/invite/codes` `/user/usage` `/user/devices` —— **11/11 返回 200**。

**余额抵扣（本轮修的最重一条）在真库上的验证**

```
下单前 wallet = 5000
下单（¥72 勾选余额）→ total=7200 balance_used=5000 payable=2200
下单后 wallet = 0        ← 修复前这里仍是 5000，同一笔余额可无限次重复抵扣
取消订单 → wallet = 5000  ← 锁定的余额原样退回
```

分录侧同时核对：`BALHOLD-*` 与 `BALREL-*` 各一条，
每条分录借贷合计为 0，`wallet_balances` 缓存与 `user_wallet_balance` 视图逐分一致。

**浏览器端**：登录 → 概览 → 套餐页渲染出三个套餐与正确价格（¥72 / ¥159 / ¥358）。
后台打开后显示准入状态页（403 分支），**没有跳登录页** —— 这正是 PR #19 改掉的那个行为。

---

## 4 · 上线后的资源清单（`bp-` 前缀）

| 资源 | 形态 | 备注 |
|---|---|---|
| `bp-api` | Cloud Run，修订版 `bp-api-87886e4` | `--max-instances=8` / `BP_DB_MAX_CONNS=2`（ADR 0005 §6.2 的连接数公式） |
| `bp-web` / `bp-admin` | Cloud Run，nginx 静态托管 | ⚠️ **过渡形态，非 ADR 0003 裁决结果**，见 §5 |
| `bp-db` | Cloud SQL PG17，迁移版本 19 | 48 张基表 |
| `bp-migrate` | Cloud Run Job | 2026-08-17 就已存在，此前文档一直记成「未建」 |
| Cloud Scheduler ×8 | `alive-gc` / `expire-check` / `order-timeout` / `chain-scan` / `traffic-reset` / `stat-rollup` ×2 / `remind-sweep` | 均走 OIDC，实测 200 |

---

## 代价

- **两个 SPA 共享 `*.run.app` 这一个可注册主域名。** ADR 0003 §3.2 明确要求用户面板与后台
  **不共享主域名**（封锁粒度常在主域名级别）。本次为了「先可用」欠下了这条约束。
- **静态托管走 Cloud Run + nginx**，比 CDN 多一跳、冷启动多几十毫秒，且**不是**裁决过的选型。
  `deploy-web.sh` 支持的三个目标（Cloudflare / GitHub / Netlify）本次一个都没用 ——
  它们都需要本机没有的凭据。
- **本次上线的可复现性是半边的**：`bp-api` / `bp-migrate` 走的是仓库里的脚本，
  `bp-web` / `bp-admin` 的 Dockerfile 与 nginx.conf **不在仓库里**（临时目录），
  ADR 0003 落定之前不把它们当成答案提交进来。**下一个人无法照着仓库重发这两个服务。**

## 这次没有解决的

- **出口节点仍是 0 台。** 用户现在能注册、能下单，但**买了也没有节点可连**，
  `/user/nodes` 返回空数组。P1 的八条出口标准依然 0/8。
- **管理面进不去。** 需要 GCLB + IAP + OAuth 品牌，而 OAuth 同意屏是控制台交互步骤。
  见 roadmap B51。
- **ESP 未接线。** 注册验证码写进了 `email_verifications`，但没有任何邮件真的发出去。
  本次的测试账号是靠直接往那张表里插一行已知验证码完成注册的 —— **真人做不到这件事**。
  也就是说，**现在这套东西还不能让任何一个真实用户自助注册**。
- **`bp-` 告警策略仍是 0 条。** 8 条 Scheduler 从此会跑，而「某条任务从此不再执行」
  在告警面上**完全静默**，最坏的那条（`expire-check`）的现象是「到期用户继续免费上网」。
- **roadmap B53 / B54 两条动钱的缺陷未修**，理由见 roadmap（都不是能顺手改一行的）。
- **`deploy.yml` 仍然从未运行过**（B47）。本次走的是本机脚本 + Cloud Build，
  没有可审计的 CI 部署记录。

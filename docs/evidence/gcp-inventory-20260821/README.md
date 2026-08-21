# GCP 实查一批 · 2026-08-21 —— 解开 B9 / B12 / B32，并对齐生产实况

> 日期：2026-08-21 · 性质：**证据型核查** · 状态：**已完成**
> 事实基线：以 `wangharp@gmail.com` 对 `oratis-491316` 与账单账户 `0130C2-FA2146-786074` 的
> `gcloud` / `openssl` / `curl` 实际输出，无推断
> 关联：[roadmap §9](../../00-overview/roadmap.md) 的 B9 / B12 / B32、
> [ADR 0005 §12](../../05-adr/0005-database-selection.md)、
> [deploy §15](../../04-ops/deploy.md)、[monitoring §3](../../04-ops/monitoring.md)、
> [launch-readiness-review-20260821](../../00-overview/launch-readiness-review-20260821.md)

---

## 1 · 这些证据证明什么、不证明什么

**证明**：2026-08-21 这一刻 `oratis-491316` 与其账单账户上**实际存在**的配置。
四条被 roadmap 标为「可直接做（分钟级）」的核实项由此关闭：B9、B12、B32，外加对
上线审查 §12 第 2 条「未核实生产 serving revision」的补做。

**不证明**：
- 不证明这些配置**将来**不变。全部是时点快照。
- §5 的冒烟只覆盖 6 条免鉴权路径，**不证明任何需要登录或节点密钥的路径正确**。
- §4 只证明预算**建得了**，不证明它会送达 —— 通知规则是默认 IAM 收件人，
  没有端到端触发过（[monitoring §10](../../04-ops/monitoring.md) 要求「每条告警被真实触发过」，本次没做）。

---

## 2 · B9 关闭：`*.a.run.app` 的签发者是 **GTS**，不是 Let's Encrypt

原始输出：[runapp-cert.txt](runapp-cert.txt)

```
0 s:CN=*.a.run.app
  i:C=US, O=Google Trust Services, CN=WR2
1 s:C=US, O=Google Trust Services, CN=WR2
  i:C=US, O=Google Trust Services LLC, CN=GTS Root R1
2 s:C=US, O=Google Trust Services LLC, CN=GTS Root R1
  i:C=BE, O=GlobalSign nv-sa, OU=Root CA, CN=GlobalSign Root CA
```

有效期 2026-07-20 → 2026-10-12（约 90 天，Google 自动轮换）。

**含义**：[deploy §15](../../04-ops/deploy.md) 与 roadmap B9 写的「**若是 GTS**，中国用户路径
必须过一个能钉 LE 的代理（CF 橙云 $0 或 GCLB 约 $18/月待核实）」这一分支**成立**。
`*.run.app` 直连不能作为面向中国用户的 API 入口形态。

> 注意 SAN 里同时有 `*.a.run.app` 与逐区域的 `*.<region>.run.app`、`*.<region>.mtls.run.app`，
> 也就是说换区域重新部署不会改变签发者结论。

---

## 3 · B12 关闭：`bp-db` 的四个配置细节

原始输出：[cloudsql-bp-db.txt](cloudsql-bp-db.txt)

| [ADR 0005 §12](../../05-adr/0005-database-selection.md) 的问题 | 实查值 |
|---|---|
| 存储下限（本文按 10 GB 计） | **10 GB PD_SSD** —— ADR 的成本基础成立 |
| 自动备份默认份数 | **保留 14 份**（`retentionUnit: COUNT`），`backupTier: STANDARD`，每日 10:00 起 |
| PITR 事务日志默认保留天数 | **7 天**，`pointInTimeRecoveryEnabled: true`，日志存 `CLOUD_STORAGE` |
| **删实例时自动备份是否一并删** | ⚠️ **仍未证实** —— `describe` 里没有这个字段，需查官方文档或做一次真实建删实验。**本次未做，B12 的第四问保持开放。** |

**顺带查到的三条本来没在问的**（都指向 §10.4「恢复方案的必要性」）：

1. 🔴 **`deletionProtection: false`** —— 实例现在可以被一条 `gcloud sql instances delete` 直接删掉，
   没有任何守卫。这比第四问更要紧：第四问是「删了之后备份还在不在」，
   而这一条是「多容易被删」。
2. **`storageAutoResize: true` 且 `storageAutoResizeLimit: 0`（= 无上限）** ——
   磁盘会随写入自动增长且不封顶，ADR 0005 按 10 GB 算的存储成本是下限不是上限。
3. **公网 IP `34.172.195.186` 存在，`sslMode: ALLOW_UNENCRYPTED_AND_ENCRYPTED`、`requireSsl: false`**，
   但 `authorizedNetworks` 为空 —— 所以网络层是关的（连不进来），
   生产走的是 Cloud Run 内建连接器（`run.googleapis.com/cloudsql-instances` 注解）。
   **不是漏洞，是一条待收紧项**：授权网络一旦被加，明文连接就是允许的。

---

## 4 · B32 结论要改：预算**建得了**，缺的是口径

原始输出：[billing-budgets.txt](billing-budgets.txt)

- 计费账号：**`0130C2-FA2146-786074`**（`gcloud billing projects describe` 直接给出，
  roadmap B32 写的「计费账号与 `BILLING_ACCOUNT_ID` 都没查」现在有答案了）。
- 当前身份**有** `billing.budgets.list` / `update` 权限 —— roadmap §12 与 monitoring 里
  「Cloud Billing budget 告警**现在建不了**」这条**不成立**，不需要申请权限。
- 账户上**本来就有**一条项目级预算 `VPN-oratis-491316 monthly budget`：$50/月，
  阈值 20% / 60% / 100% / 150%。

🔴 **但它的口径是 `INCLUDE_ALL_CREDITS`。** 项目的 gross 目前被
[egress-billing-20260820 §4.2](../egress-billing-20260820/) 记录的推广抵扣全额冲平，
净额约 $6/两个月 —— **这条预算在抵扣用完之前永远不会触发**。
账户上另外三条预算（`Monthly gross run-rate guardrail`、`telloria monthly cap`、
`Luddi BigQuery monthly guardrail`）用的都是 `EXCLUDE_ALL_CREDITS`，只有这一条是例外。

**2026-08-21 已改**（本文件记录的是改后状态）：

| 项 | 改前 | 改后 |
|---|---|---|
| 口径 | `INCLUDE_ALL_CREDITS` | **`EXCLUDE_ALL_CREDITS`** |
| 额度 | $50/月 | **$500/月** |
| 阈值 | 20 / 60 / 100 / 150%（current） | 同上 **+ forecasted 100%** |
| 名称 | `VPN-oratis-491316 monthly budget` | `VPN-oratis-491316 monthly budget (500, excl credits)` |

$500 的依据：项目 gross 日均近 7 天 $16.44（≈ $501/月）、近 14 天 $13.05（≈ $397/月）、
近 4 天 $24.36（≈ $741/月，出口阶跃之后）。
取 $500 是为了让 20/60/100 这个梯子在**当前速率下逐级点亮**而不是一上来就爆表；
出口若再翻一倍，150% 会在月中触发。**这个数字是可调的，改额度不需要改口径。**

> ⚠️ `notificationsRule` 是空的 —— 走的是默认 IAM 收件人（账单管理员邮箱），
> 没有接 Pub/Sub。[monitoring §4](../../04-ops/monitoring.md) 要求的双通道拓扑对预算告警**尚未落地**。

---

## 5 · 生产实况对齐

### 5.1 线上冒烟 6 条全过

原始输出：[prod-smoke.txt](prod-smoke.txt)。全部是只读 GET/OPTIONS。

| 检查 | 结果 |
|---|---|
| `/-/healthz` | 200 `ok` |
| `/healthz` | 404（Cloud Run 的 GFE 拦截，与 [cloudrun-healthz-intercept-20260817](../cloudrun-healthz-intercept-20260817/) 一致） |
| 订阅假 token | 404（不泄露存在性） |
| UniProxy 无 token | 401 |
| CORS 白名单内 | 回 `Access-Control-Allow-Origin` + `Allow-Credentials`，带 `Vary: Origin` |
| CORS 白名单外 | **不回** `Access-Control-Allow-Origin`，仍带 `Vary: Origin`（缓存安全） |

### 5.2 🔴 生产跑的 commit **不在任何分支上**

原始输出：[cloudrun-bp-api.txt](cloudrun-bp-api.txt)

线上 100% 流量在 `bp-api-2fbf49d`，镜像 tag `2fbf49d`
（`deploy-api.sh` 的 tag 默认取 `git rev-parse --short=7 HEAD`）。
该 commit 完整 sha 是 `2fbf49d3d2b6a1a3236e0598f54bf184b478514c` ——
**它在 GitHub 的对象库里存在，但不被任何分支引用**：`pr7/p1-core-and-deploy`
被 force-push 过，它是被冲掉的那一版。

把它取回来做两点 diff（`git diff 2fbf49d3d2b6 origin/pr/9`）：

```
 .github/workflows/deploy.yml      |  6 ++++-
 api/cmd/server/main.go            |  5 ++++
 infra/node/rotate-ip.sh           | 49 +++++++++++++++++++++++++++++++++++----
 infra/scripts/verify-isolation.sh |  4 +++-
 4 files changed, 58 insertions(+), 6 deletions(-)
```

`api/cmd/server/main.go` 的 5 行**全部是注释**。
→ **生产的 API 二进制与 PR #9 的 head 语义等价，合并 PR 栈不改变生产行为。**
这是上线审查 §12 第 2 条留下的问题，答案比它问的更具体。

> **教训（已回写 roadmap）**：`deploy-api.sh` 用短 sha 做 tag，
> 而短 sha 在分支被 force-push 之后会指向一个不可达的 commit —— 镜像与源码的对应关系
> 只在「分支没被改写」这个前提下成立。要么部署前禁止 force-push，
> 要么把完整 sha + 分支名写进镜像 label。

### 5.3 监控现状：**log-based metrics 一条都没有**

原始输出：[monitoring-inventory.txt](monitoring-inventory.txt)

| 项 | 2026-08-21 实查 |
|---|---|
| log-based metrics | **0 条**（[monitoring §3.2](../../04-ops/monitoring.md) 要求上线前建 10 条，且**不追溯**） |
| 告警策略 | 3 条，**全部属于 `lisa-cloud`**，`bp-*` 一条没有 |
| uptime check | 1 条 `lisa-cloud-health`，`bp-*` 没有 |
| Cloud Scheduler | 1 条 `lisa-autonomy-sweep`，`bp-*` 一条没有（[monitoring/deploy](../../04-ops/deploy.md) 要求 6 条） |
| Pub/Sub topic `bp-alerts` | ✅ 已存在 |
| 通知渠道 | `ops alerts (wangharp)` email，已启用；**没有 Pub/Sub 渠道** |

**`bp-api` 于 2026-08-17 首次部署，到本次实查已过 4 天，这 4 天的日志指标数据永久缺失。**

**2026-08-21 已建 7 条**（另 3 条卡在代码，见下）：

| 已建 | 过滤器来源 |
|---|---|
| `bp_api_5xx` / `bp_api_429` | monitoring §3.2 给的原文过滤器（平台请求日志 `httpRequest.status`） |
| `bp_uniproxy_auth_fail` | AccessLog 的 `path` + `status`（中间件本身不打日志） |
| `bp_subscribe_404` | `handler/subscription.go` 的显式日志行「订阅 token 无效，返回 404」 |
| `bp_admin_authz_fail` | admin 路径 401/403。**是 `bp_admin_totp_fail` 的占位** —— TOTP 未实现 |
| `bp_task_idem_skip` | `handler/node.go` 的「流量上报重复，已按幂等丢弃」。只覆盖 `/push`，不覆盖 `Idempotency-Key` 中间件（后者不打日志） |
| `bp_db_pool_wait` | 按 `jsonPayload.err` 文本匹配，**近似过滤器** |

| 仍缺 | 卡在什么 |
|---|---|
| `bp_mail_bounce` | ESP 未接通（`auth.go` 的 TODO(P1)），没有退信日志可匹配 |
| `bp_cert_issuer_bad` | monitoring §8 的每日证书核对作业不存在 |
| `bp_node_alive` | 需要应用主动写一行带 `node_id` 的结构化日志；现在没有。§5 的 metric-absence 告警依赖它 |

---

## 6 · 复现

四份原始输出各自把命令行写在文件第一行，可直接照抄重跑。
`bq` 相关的账单查询在 [egress-billing-20260820](../egress-billing-20260820/)。

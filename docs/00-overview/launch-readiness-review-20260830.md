# 上线审查：一天里 API 从 18/128 做到 120/128，而能上线的东西仍然一件都没多 —— 节点 0 台，生产跑的是 14 个提交之前的代码

> 日期：2026-08-30 · 性质：**证据型核查** · 状态：**As-Built**（2026-08-30 时点快照，不随后续进展回改）
> 事实基线：`claude/project-status-completion-9b129a` 分支 HEAD **`b6e7603e7f9`**（2026-08-30）；**0 个 open PR**
> 关联：[roadmap.md](roadmap.md)（B1–B52 阻塞项登记处，同日复核）、
> [launch-readiness-review-20260821.md](launch-readiness-review-20260821.md)（**As-Built，不回改**；§10 是对它的逐条对读）、
> [product-brief.md](product-brief.md)、[pricing-and-plans.md](../03-product/pricing-and-plans.md)、
> [docs/README.md §7](../README.md)
> 证据口径：本机命令实测 / `gcloud` 实查 / GitHub API 实况 = **高**；仓库文档转述 = 中；未复核的旧结论 = **不采用**
> 读者：决定「离上线还差什么」的人。审查目标：**上线并服务内部付费用户**。
> ⚠️ **本文与 [20260821 那份](launch-readiness-review-20260821.md) 并列存在，不替代它。**
> 那份是 2026-08-21 的时点快照且明写不回改；本文是 2026-08-30 的。两份对读见 §10。
>
> ---
>
> ⚠️ **同日重写说明（2026-08-30，第二次）。本文早些时候发布过一版，那一版现在是错的，必须说清楚错在哪。**
>
> 上一版的事实基线是 master `a4604c9396f`，它的 §2.1 写的是 **18 / 128**
> （在那个 commit 上重跑同一条统计确实是 18，本次已核）。
> 此后同一天内又落了 **8 个提交**（`01350425ef1` → `b6e7603e7f9`，`git rev-list --count` 实数），
> 实现数一路走到 **120 / 128**（中途：`6ed53d5a8bc` 27 → `92b65e0d5f9` 67 → `bdc4437d0fe` 120），
> 前端测试从 108 涨到 **623**。上一版 §2 / §3 / §7 的绝大多数数字因此失准。
> **本文是对同一份时点快照的更新，不是新写一份** —— 理由：它记的时点仍是 2026-08-30，
> 而 [docs/README §5](../README.md) 的「不回改」约束针对的是**已经过去的**时点
> （[20260821 那份](launch-readiness-review-20260821.md) 属此类），不是当天还在推进的当天。
>
> **本次相对上一版的结构变动，逐条交代（[docs/README §4](../README.md) 第 2 条要求）**：
> - **新增 §4「契约 vs schema 缺口」** —— 这是本轮最有价值的产出之一，上一版没有这一节。
> - **§4–§9 顺次后移一号**（单位经济 §4→§5、文档矛盾 §5→§6、决策 §6→§7、阻塞项 §7→§8、
>   关键路径 §8→§9、逐条对读 §9→§10、代价 §10→§11、这次没有解决的 §11→§12）。
>   **外部引用不受影响**：全仓对本文带节号的引用只有两处，
>   [ADR 0005](../05-adr/0005-database-selection.md) 引 §2、[ADR 0006](../05-adr/0006-api-stack.md) 引 §3，
>   而 §2「现状盘点」与 §3「阶段完成度」**编号与主题都没动**（2026-08-30 `grep` 实核）。
> - **上一版的判定被推翻了三条，逐条落点在 §10.1**：①「API 已实现 18/128」→ **120/128**；
>   ② §11 第 2 条「没有跑 gcloud，GCP 实况全部转述自 08-20」→ **本次实查了**；
>   ③ §11 第 3 条「生产 serving revision 与 HEAD 的对应关系未验」→ **本次验了，答案在 §1 第 1 条**。
> - **上一版的核心判定一个字没改**：P1 按出口标准仍是 **0/8**。见 §3.2。

---

## 1 · 结论

**这一天写出来的代码，比这个项目此前九天写的加起来还多。可上线性依然是零位移。**

一天里 10 个提交（`2c0c6b69bde` → `b6e7603e7f9`，全部 2026-08-30）做完了：
API 从 18/128 到 **120/128**、sqlc 查询从 194 条到 **343 条**、
Go 测试从 195 个顶层函数到 **573 个**、前端测试从 108 个用例到 **623 个**、
管理面 23 页里 **21 页**接上真实 API。

**而下面这五个数字，一个都没变**：

| | 值 | 本次实查命令 |
|---|---|---|
| 在跑的自有节点 | **0 台** | `gcloud compute instances list --project=oratis-491316` → 只有 `vpn-us` / `vpn-jp` |
| 真实链上收款 | **0 笔** | 支付链路无任何生产调用；`bp-api` 上连支付所需的配置都没有 |
| `bp-` 告警策略 | **0 条** | `gcloud alpha monitoring policies list` → 3 条，全部属 `lisa-cloud` |
| `bp-` Cloud Scheduler 作业 | **0 条** | `gcloud scheduler jobs list`（us-central1 / asia-east2 / us-west1 三处）→ 只有 `lisa-autonomy-sweep` |
| `deploy.yml` 执行次数 | **0 次** | `gh run list --limit 200` → 35 次 run 全是 `ci` |

**三个最紧迫的发现：**

1. 🔴 **生产上跑的不是今天写的东西，而是 14 个提交之前的代码 —— 这条欠了九天的账，本次终于验了。**
   `gcloud run services describe bp-api` → serving revision **`bp-api-618bf1c`**，
   镜像 `bp-images/bp-api:618bf1c`。该 tag 对应 commit **`618bf1cc89b3`**（2026-08-23，PR #16 的合并提交），
   `git merge-base --is-ancestor 618bf1c HEAD` 为**真**，且它仍被 `master` 引用（`git branch --contains` 实查）。
   `git rev-list --count 618bf1c..HEAD` = **14**。
   **在那个 commit 上重跑本文 §2.1 的实现数统计，得到的是 18/128**（`git archive 618bf1c` 后逐条比对 `operations.txt`）。
   > **两句话必须一起说**：① [20260821 §12](launch-readiness-review-20260821.md) 与本文上一版都欠着的
   > 「serving revision 与 HEAD 的对应关系」**已经问清楚了，而且答案是好的** ——
   > 镜像可溯源、commit 仍被分支引用，B41 那种「对应关系断掉」的事故没有重演；
   > ② **但它的内容是「今天做的 120 个 operation，生产上一个都没有」。**
   > 本文全部的「已实现」都是**仓库口径**，不是**线上口径**。这两个口径这一天里第一次差到了 102 个 operation。

2. 🔴 **管理面在生产上是整体关闭的，而且它必须如此 —— 因为它根本没有登录端点。**
   `gcloud run services describe bp-api` 的环境变量清单里**没有 `BP_ADMIN_IAP_AUDIENCE`、
   没有 `BP_ADMIN_TOTP_ENC_KEY`、没有任何 `BP_INTERNAL_*`**（10 个环境变量全部列出，逐个核过）。
   按 `middleware/admin.go` 的 fail-closed 设计，未配置 audience 时 `AuthenticateAdmin` **整体拒绝**，
   所以 61 个 `/admin/*` + 9 个 `/internal/tasks/*` 在生产上一律 403。
   **这不是配置疏漏，是当前唯一正确的状态** —— 见下一条。

3. 🔴 **管理面的「登录」这一半在冻结契约里有声明、无实现，本轮才被发现。**
   45 条 `/api/v1/admin/*` 路径里**没有一条是 login / session / me**；
   `adminSession` 这个 security scheme 被 61 个 operation 引用，
   而 `middleware/admin.go` 里 `Authorization` 这个词出现 **0 次**。
   完整证据与另外五条同类缺口见 **§4** —— 那一节是本轮最有价值的产出之一。

---

## 2 · 现状盘点（当前实现，非设计目标）

### 2.1 代码 —— 每个数字都附产生它的命令

| 层 | 数字 | 命令（本次当场跑过） |
|---|---|---|
| OpenAPI 契约 | **7,065 行 / 128 个 operation** | `wc -l openapi/openapi.yaml`；`wc -l api/internal/handler/operations.txt` |
| **API 已实现** | 🟢 **120 / 128** | `operations.txt` 与各非生成文件的 `func (s *Server) X` 取交集得 123，**减去 3 条显式返 501 的**（见下）= 120 |
| **API 仍返 501** | **8 条**（另有 2 条是「主路径已实现、保留一个分支 501」） | `api/internal/handler/unimplemented_test.go` 的 `TestDeliberatelyUnimplemented` 表，逐条见 §2.3 |
| 管理面 | **56 / 61 已实现** | 61 取自 `authmap.go` 的 `adminOperations`（实数）；未实现的 5 条是 domains ×3 + mail templates ×2 |
| 内部定时任务面 | **9 / 9 已实现** | `authmap.go` 的 `internalTaskOperations`（实数） |
| 节点面 UniProxy | **6 / 6 已实现** | 含本轮补上的 `PushUniProxyStatus`（`handler/usersub.go:1413`）—— 上一版记的「五端点实现了四个」已不成立 |
| 迁移 | **19 组 up/down**（`0001`–`0019`） | `ls api/db/migrations/*.up.sql \| wc -l` |
| 表 | **47 张** | `44` 条 `CREATE TABLE` + **3 条 `CREATE UNLOGGED TABLE`**（`server_online_state` / `user_device_state` / `rate_limit`）。⚠️ **上一版的「44 张」是漏数**：那条 grep 的模式匹配不到 `CREATE UNLOGGED TABLE`，与 `make migrate-verify` 实跑报的 47 差了 3 —— 差值就是这三张。`0018`/`0019` 只 seed 科目与改一个索引，不建表 |
| sqlc 查询 | **343 条** | `grep -rhoE "^-- name: " api/db/queries/*.sql \| wc -l` = 343；`db/gen/*.sql.go` 的 SQL 常量数 = 343；`Querier` 接口方法数 = 343，**三处一致** |
| `make db-explain` | **343 条语句 / 172 条写语句** | `python3 api/scripts/db_explain.py \| head -2` → 「共 343 条语句，其中写语句 172 条」。⚠️ **本次只跑到抽取这一步，没有跑 EXPLAIN**（本机 Docker daemon 未运行），见 §11 代价 2 |
| Go 测试 | **37 个 `_test.go` / 573 个顶层 `Test` 函数** | `find api -name '*_test.go' \| wc -l`；`grep -rhoE '^func Test[A-Za-z0-9_]*\(' api --include='*_test.go' \| wc -l`。⚠️ **静态计数，不是一次绿灯**（本机无 Go 工具链），见 §11 代价 2 |
| Go 源码 | 非生成 **52,704 行** / 生成物 **46,509 行** | `find api -name '*.go' \| grep -v '/gen/' \| xargs cat \| wc -l`（与其反向） |
| **web 测试** | 🟢 **623 个用例 / 48 个文件，全绿** | `pnpm test`（本次真跑）：shared **67 / 3** + user **189 / 20** + admin **367 / 25** |
| web 路由 | user **22 条** + admin **24 条** = 46 | `grep -oE 'path="[^"]*"' web/{user,admin}/src/App.tsx \| wc -l` |
| web 页面组件 | user **21 个** `*Page.tsx` + admin **23 个** | `find web/{user,admin}/src/routes -name '*Page.tsx' \| grep -v test \| wc -l` |
| **web 未接线点** | **22 处 `TODO(P1)`，散在 16 个文件**（上一版：44 处 / 30 个文件） | `grep -rho 'TODO(P1)' web --include='*.ts*' \| wc -l` |
| api 代码内 TODO | P1 **19 条** / P2 **50 条**（上一版：5 / 27） | `grep -rho 'TODO(P[0-9])' api --include='*.go' --include='*.sql' \| sort \| uniq -c`。⚠️ **涨了不等于变差**：这一轮的实现把此前藏在「整个端点是 501」之下的缺口逐条挖出来登记了 |
| infra 脚本 | **18 支 / 10,705 行**（`infra/node/` 下 4 支 / 2,621 行） | `find infra -name '*.sh' \| wc -l`；`... \| xargs wc -l` |
| CI 作业 | `ci.yml` **9 个** · `deploy.yml` **6 个** | 从两份 workflow 的 `jobs:` 下解析。`ci`：`changes` `go` `contract-drift` `migrations` `openapi-lint` `web` `shellcheck` `docker-build` `ci-ok`；`deploy`：`plan` `isolation-before` `deploy-api` `deploy-web` `isolation-after` `mark-deployed` |
| evidence 目录 | **9 个** | `ls -d docs/evidence/*/ \| wc -l` |
| ADR | **14 份**（`0001`–`0008` + `0010`–`0015`，`0009` 是刻意空号） | `ls docs/05-adr/0*.md \| wc -l` |
| docs 文档 | **56 篇 `.md`** | `find docs -name '*.md' \| wc -l` |
| open PR | **0 个** | `gh pr list --state open` → `[]` |
| workflow run | **35 次，100% 是 `ci`** | `gh run list --limit 200 --json workflowName` → `Counter({'ci': 35})` |
| **deploy 运行次数** | 🔴 **0** | `gh run list --workflow=deploy.yml` → 空 |
| repo environment / variable / secret | 🔴 **0 / 0 / 0** | `gh api repos/oratis/babelplus/{environments,actions/variables,actions/secrets}` → `total_count` 全为 0 |

> ⚠️ **「120 / 128」这个数字的准确读法，三句话**：
> ① 它是**仓库口径**，生产上是 **18 / 128**（§1 第 1 条）；
> ② 它的分母是 operation，不是功能 —— 其中 2 条是「主路径实现、一个分支仍 501」；
> ③ 它**不声称这些实现是对的**。本文没有做代码审查，见 §12。

### 2.2 GCP 实况 —— 🟢 本次实查（这是相对上一版的一处证据升级）

**上一版把「未跑 `gcloud`」列为代价第 3 条与遗留第 2 条。本次跑了**，以下每一行都是 2026-08-30 只读实查：

| 查什么 | 结果 | 命令 |
|---|---|---|
| Compute 实例 | **`vpn-us`（us-west1-a，RUNNING）· `vpn-jp`（asia-northeast1-a，RUNNING）** —— 🔴 **`bp-node-*` 0 台** | `gcloud compute instances list --project=oratis-491316` |
| Cloud Run 服务 | `bp-api` · `anthropic-relay` · `lisa-cloud` · `lisa-web` —— 🔴 **`bp-web` 不存在** | `gcloud run services list` |
| `bp-api` serving revision | **`bp-api-618bf1c`**，镜像 `bp-images/bp-api:618bf1c` | `gcloud run services describe bp-api --region=us-central1` |
| `bp-api` 环境变量 | `BP_ENV` `BP_GCP_PROJECT_ID` `BP_DB_MAX_CONNS` `BP_LOG_LEVEL` `BP_TRUST_PROXY_HEADERS` `BP_ALLOWED_ORIGINS` `BP_DATABASE_URL` `BP_SUBSCRIPTION_TOKEN_PEPPER` `BP_NODE_KEY_PEPPER` `BP_SESSION_SIGNING_KEY` —— 🔴 **没有任何 `BP_ADMIN_*` / `BP_INTERNAL_*`** | 同上 |
| Cloud Scheduler | **只有 `lisa-autonomy-sweep`** —— 🔴 `bp-` 作业 **0 条**（三个 location 各查一次） | `gcloud scheduler jobs list --location={us-central1,asia-east2,us-west1}` |
| 告警策略 | **3 条，全部是 `lisa-cloud` 的** —— 🔴 `bp-` **0 条**（monitoring 要求 17 条） | `gcloud alpha monitoring policies list` |
| log-based metrics | **7 条，全部是 `bp_`**：`bp_admin_authz_fail` `bp_api_429` `bp_api_5xx` `bp_db_pool_wait` `bp_subscribe_404` `bp_task_idem_skip` `bp_uniproxy_auth_fail` —— 清单要 11 条，**仍差 4 条** | `gcloud logging metrics list` |
| uptime check | **1 条（`lisa-cloud-health`）** —— 🔴 `bp-` **0 条** | `gcloud monitoring uptime list-configs` |

> 🟢 **这次实查确认了上一版所有转述过来的判定，一条都没被推翻**（0 台节点、无 `bp-web`、0 条告警、7 条指标）。
> 转述是对的，但它**当时是十天前的实况加推理**；现在它是实测。**两者的证据强度不一样，这一节现在是高。**
> ⚠️ **本次仍未执行任何 `gcloud` 变更操作**，全部是 `list` / `describe`。

### 2.3 仍在返 501 的 8 条 —— 逐条清单与各自的阻塞原因

来源是 `api/internal/handler/unimplemented_test.go` 的运行期清单（它对真 `Server` 逐条调用并断言 `ErrNotImplemented`）。
**这个测试挡的不是「漏实现」，是反过来那种事故**：有人为了让页面别报 501 写一个「返回空列表」的假实现 ——
**空列表和未实现在界面上长得一模一样，在决策上完全相反。**

| # | operation | 阻塞原因（不是「没空写」） |
|---|---|---|
| 1 | `listAdminDomains` | **`domains` 表不存在**（19 支迁移逐条核过），且卡在**两份未批准的 ADR**（0010 §1.3+§8.1 的池划分、0011 §7.2 的字段形状）。**更要命的是字段模型是两套**：冻结契约的 `Domain` 是 `{id, hostname, role, enabled, reachable, last_checked_at}`，ADR 0011 §7.2 是 `{host, url, label, state, order, platform, registrable}` —— 后者多出的 `platform` / `registrable` 不是装饰，0011 §2.1/§6.1 要求故障转移**优先跳到不同平台、不同可注册域名**，没这两个字段做不到 |
| 2 | `createAdminDomain` | 同上。**不能拿 `settings` 的 JSONB 顶**：契约的路径参数是数字 id，而 JSONB 数组没有稳定 id —— 并发编辑会删错行**且不报错** |
| 3 | `deleteAdminDomain` | 同上 |
| 4 | `listAdminMailTemplates` | **`mail_templates` 表不存在。** `email_log.template` 只是模板键的字符串快照，不是正文存储 |
| 5 | `updateAdminMailTemplate` | 同上。同样不能塞进 `settings` 的 JSONB：`MailTemplatePatch` 要求改前/改后值进审计，而 JSONB 的部分更新拿不到干净的字段级快照 |
| 6 | `enrollUserTotp` | **契约里就声明 501**（`description` 原文「**P3，未实现。** 服务端返回 `501` 直到实现完成」），且 schema 无落点：`users` 上没有 `totp_secret_enc` / `totp_confirmed_at`，防重放也没有用户侧的表 |
| 7 | `verifyUserTotp` | 同上 |
| 8 | `disableUserTotp` | 同上。🔴 **它尤其不能退化成 204** —— 一个以为自己关掉了 2FA 的用户会在下次登录时被挡在门外，而他手上可能已经把 authenticator 里的条目删了 |

**另有 2 条是「主路径已实现、保留一个分支 501」**（与上表分开记，因为性质不同）：

- `broadcastAdminMail` 的**自定义正文**分支 —— `email_log` 有 `template` 键与 `subject`，**没有正文列**。模板键驱动的那一半可用。
- `sendEmailCode` 的 **`email_change`** 场景 —— 换绑邮箱要「已登录 + 目标邮箱未被占用」两个前提，而该端点在契约里是 `security: []`（免登录）。要支持它得先裁定「本端点是否接受可选鉴权」，**那是契约层面的决定**。

> **一句话概括这 8 条**：**没有一条卡在工时上。** 5 条卡在缺表（而缺表又卡在未批准的 ADR），3 条卡在契约自己声明的未实现。
> 这与「API 还有 8 个没做完」是完全不同的一句话。

---

## 3 · 阶段完成度 —— 两个数字，因为它们差得很远

**roadmap 自己的口径是「出口标准」**（§1 组织原则 2：「每个阶段的出口标准必须是一次可判定的观察，
不是一份文档」）。按那个口径算一次；再按「工作量」算一次；**两个都写，并说清楚差在哪**。

| 阶段 | **按出口标准（roadmap 口径）** | 按工作量 | 与本文上一版的差 | 差在哪 |
|---|---|---|---|---|
| **P0 调研与设计** | **3.5 / 7 = 50%** | ≈ 90% | 不变 | 见 §3.1 |
| **P1 内核可用** | 🔴 **0 / 8 = 0%** | ≈ **70%**（上一版 45%） | 工作量 +25，**出口标准 0 位移** | 见 §3.2 —— **八条出口标准全部要求一台在跑的节点，而节点数是 0** |
| **P2 产品闭环** | **0 / 8 = 0%** | ≈ **35%**（上一版 15%） | 工作量 +20 | 见 §3.3 |
| **P3 可运营** | **0 / 7 = 0%** | ≈ **25%**（上一版 5%） | 工作量 +20 | 后台 21 页接线 + 四层强制全栈落地，**但告警一条没建、演练零次** |
| **P4 加固** | **0 / 5 = 0%** | ≈ 5% | 不变 | 只有「生成物漂移」这一小块做了，IaC 未起步 |

> **为什么四个阶段的工作量都涨了、出口标准却一格都没动 —— 一句话：**
> **出口标准全是「被观察到」，而这一天做的全是「被写下来」。**
> 这不是批评，是这套口径的设计目的 —— roadmap §1 原则 2 就是为了让这个差值可见。
> ⚠️ **今天这个差值创了新高**：工作量在一天内 +25 / +20 / +20，出口标准 **0 / 0 / 0**。

### 3.1 P0 逐条（3.5 / 7）—— 相对上一版无变化

| # | 判据 | 判定 | 依据 |
|---|---|---|---|
| 1 | ADR 0001 不再是「提案，未批准」 | ✅ | `0001-cloudflare-tos-risk.md:3` = 「已批准，待实施（2026-08-17 用户批准）」 |
| 2 | evidence ≥ 5 个目录，每个带 README 且写明「证明什么 / 不证明什么」 | ✅ **9 个** | `ls -d docs/evidence/*/ \| wc -l` = 9 |
| 3 | pricing 里不再有留空的价格；§7 第一条被划掉 | ✅ | `grep -n "待定" docs/03-product/pricing-and-plans.md` **无命中**；§7 第一条已是 `- [x]` |
| 4 | docs/README §7 阻塞项表第 1、2 条被划掉 | 🔶 **0.5** | 第 1 条已划（ADR 0001）；**第 2 条「零实测数据」原封不动** |
| 5 | v2node 三项行为各有一条 evidence | 🔶 **0** | 三项**答案**都有（读源码），但落在 `v2node-contract-20260817` 与 `v2node-401-behavior-20260821`，**没有 `evidence/v2node-behavior-*`**；且判据要的「起容器」一次没做 |
| 6 | 三份缺失 ADR 各自存在，**且不是「提案，未批准」** | 🔴 **0 / 3** | 0010 与 0011 **存在但状态正是被排除的那个**；**节点密钥传输形式那一份连文件都不存在**（14 份里没有） |
| 7 | 至少一个域名已注册且 DNS 可控 | ✅ | `dig +short NS babel.plus` → `dns13/dns14.hichina.com`（2026-08-30 复核） |

> 🔴 **这一节这一天一个字都没变，而这恰恰是本文最想说清楚的一件事：`ADR 数量` 是工作量指标，`ADR 状态` 才是完成度指标。**
> 今天写了 10 个提交、25,000 行代码，P0 的出口标准 6 仍然是 0/3 —— 因为它要的是**批准**，而批准不是写出来的。

### 3.2 P1 逐条（0 / 8）—— 八条全部要求一台在跑的节点

| # | 判据（roadmap §4.3 原文缩写） | 判定 | 为什么 |
|---|---|---|---|
| 1 | `bp-node-hk1` 通过 J1–J6，含 ≥1 次晚高峰采样 | 🔴 | **没有 `bp-node-hk1`**（本次 `gcloud` 实查：实例只有 `vpn-us` / `vpn-jp`） |
| 2 | v2node 从 `bp-api` 拉到配置与用户表，180 秒窗口 `1×200 + 2×304` | 🔴 | 没有节点，也没有起过 v2node 容器。**注意：这一条现在只差一台机器了** —— 六个节点面 operation 已全部实现（含本轮补上的 `/status`） |
| 3 | 若第 2 条不成立，已切降级方案并把损失写进 evidence | 🔴 | 前提未发生 |
| 4 | 一条真实订阅链接在 Clash Verge Rev 与 sing-box **各人工加载一次成功** | 🔴 | 没有节点就没有真实订阅链接。B45（sing-box 缺 `inbounds`）与 B46（`GEOIP,CN` 拒载）都指向这一步会出事 |
| 5 | 连续 72 小时无中断；内存峰值 < 70%；流量差异 < 1% | 🔴 | 没有节点 |
| 6 | 封禁 / 配额耗尽 / 到期三态各手工触发一次并计时 | 🔴 | 没有节点。⚠️ **但这一条的软件侧障碍今天清掉了**：9 个 `RunXxxTask` 端点**全部实现**（上一版这里写的是「全部 501」），三态生效链路在代码里第一次是通的 |
| 7 | 一次节点密钥两步轮换，节点全程不失联 | 🔴 | 没有节点 |
| 8 | 每阶段前后跑 as-built §7 清点做 diff，`vpn-*` 与三个 Cloud Run 服务零变化 | 🔴 | 没有「阶段」可跑。（顺带：本次实查确认 `vpn-*` 两台与三个 Cloud Run 服务**确实零变化**） |

**按工作量看 P1 已经到 ≈70%**：120 个 operation 实现、47 张表在库、343 条查询、
六个节点面端点齐了、9 个定时任务端点齐了、节点脚本 2,621 行写完。
**差值 100% 落在「节点数 = 0」这一件事上** —— 八条判据没有一条能在没有节点时被观察到。

> **这就是「按出口标准算 0%、按工作量算 70%」的全部含义**：
> P1 的产出不是「造出了半个节点」，是「造好了建节点需要的一切，但一次都没执行」。
> `infra/node/` 下 **2,621 行**脚本全部带 dry-run。**写了脚本 ≠ 执行过。**
> ⚠️ 而且今天这个差值比昨天更刺眼：**软件侧的借口没有了。** 上一版还能说「三态依赖 9 个 501 的任务端点」，
> 今天那 9 条全实现了 —— **P1 现在只差一台机器。**

**API 侧仍欠的（不需要节点就能做）**：
ESP 发信未接通（`auth.go:1293` 的 `TODO(P1)`，`MailSender` 默认实现返回「未配置」，`email_log.status` 恒为 `queued`）；
链上扫描器同样是「未配置」的默认实现（ADR 0012 未批准，代码里没有任何第三方 endpoint 字面量）；
六条 Cloud Scheduler 一条没建；11 条 log-based metric 建了 7 条；
**生产 `bp-api` 上没有配 `BP_ADMIN_IAP_AUDIENCE`，管理面整体关闭**（§2.2）。

### 3.3 P2 / P3 / P4（出口标准各 0%）

- **P2 出口 8 条全 0**。第 2 条「≥1 笔真实链上收款完成对账」是硬判据 ——
  支付 operation 现在**全部实现了**（`CreateOrder` / `PayOrder` / `GetOrderPayment` /
  `RecheckOrderPayment` / `HandlePaymentNotify` 等 11 条），复式账在写入前按币种断言 `SUM=0`，
  `handlePaymentNotify` 的默认验签对**一切**回调返回 401 ——
  **但没有接任何网关、没有扫过一次链、没有收过一分钱**。
  关键路径 8 页里，`/auth/register` `/plan` `/order/:trade_no` `/subscribe` `/dashboard` `/ticket`
  **六页已接线**，仍缺的是**落地页**与 **`docs.*` 教程站**（第三套前端，目录都还不存在）。
  🔴 **邮件仍是这一层最安静的洞**：ESP 未选、发信一行没接通，
  而 ADR 0002 裁决邮件是**唯一**的失联恢复通道 ——
  **今天发生一次域名封锁，我们没有任何一条能通知到用户的路径。**
  注册 / 找回 / 重置三页前端是完整的，**而真人现在收不到那封信**。
- **P3 出口 7 条全 0**，但工作量从 5% 涨到 ≈35% 的那一段值得写清楚：
  **危险操作四层强制第一次是全栈的** —— L1 确认串在**服务端**比对、L2 reason ≥ 8（按**码位**不按 `String.length`）、
  L3 走 `mw.RequireStepUp`（含 `used_totp` 防重放）、L4 独立权限位；
  审计与业务同事务有真用例（真 `audit.InTx` + 真 sqlc 生成代码 + 假 `pgx.Tx`，断言审计写失败时 `Commit` 一次都没发生）。
  后台 23 页接线 21 页。
  🔴 **而出口标准要的是「17 条告警策略全部创建且至少做过一次端到端演练」——
  本次 `gcloud` 实查：`bp-` 告警策略 0 条，演练 0 次。** `setup-alerts.sh` 1,191 行 + `setup-metrics.sh` 951 行**从未执行**。
- **P4 出口 5 条全 0**：唯一做掉的是「`git diff --exit-code` 卡 OpenAPI 漂移」（CI `contract-drift` 作业），
  而那只是第 2 条判据的三分之一。
  **真实 Postgres 的集成测试与真实 v2node 容器的契约测试都不存在** ——
  **573 个 Go 测试全部是进程内单元测试**（假 `Querier` / `httptest`）。
  贴近真库的只有 CI `migrations` 作业里的两步（`db_explain.py` 的 343 条 `EXPLAIN`、回滚后写探针），
  **它们测的是 schema 与 SQL 而不是 handler，不构成集成测试。**

---

## 4 · 本轮查出的契约 vs schema 缺口 —— 六条，每条都不能靠写代码解决

**这一节是本轮最有价值的产出之一**（六条分别来自 `3e18dd8b269` / `92b65e0d5f9` /
`bdc4437d0fe` / `b6e7603e7f9` 四个提交）。它们的共同形状是：
**冻结契约与数据库 schema 各自成立，合起来不成立** —— 而这类缺口
**编译器抓不到、CI 抓不到、`contract-drift` 也抓不到**（生成物两边都对得上），
只有在有人真去实现那个 operation 的时候才会撞上。所以它们本轮才被发现，**而不是本轮才产生**。

**处置纪律统一是「登记，不擅自补」**：补契约要改冻结的 `openapi.yaml`，补 schema 要加迁移，
两者都超出「实现一个已在契约里的端点」的范围（[CONTRIBUTING §5](../../CONTRIBUTING.md)）。

### 4.1 🔴 管理面根本没有登录端点 —— `adminSession` 有声明、无实现

| 事实 | 命令 / 位置 |
|---|---|
| 45 条 `/api/v1/admin/*` 路径里，**没有一条是 login / session / me** | `grep -oE '^  /api/v1/admin/[^:]*' openapi/openapi.yaml \| grep -icE 'login\|session\|me$'` = **0**（总数 45） |
| `adminSession` 是被 61 个 operation 引用的 security scheme，定义在 `openapi.yaml:4379`（`http` / `bearer` / JWT） | `grep -c adminSession openapi/openapi.yaml` = **63**（1 条定义 + 61 条引用 + 1 行说明表） |
| `AuthenticateAdmin` **从不读 `Authorization`** | `grep -c Authorization api/internal/middleware/admin.go` = **0**。它验 `x-goog-iap-jwt-assertion`（`IAPAssertionHeader`）的签名，再用断言里的 email 查 `admin_users` |
| 它用的 `AdminRecord` **刻意不含 `password_hash`** | 注释原话：「管理面走 IAP，根本不该有任何代码路径能读到密码哈希」 |
| L3 的 TOTP **不是登录第二因子** | 是每个危险操作的 `X-TOTP-Code` step-up（`RequireStepUp`） |

**后果有三层，从轻到重**：

1. **前端手上一个字节的管理面凭据都没有**（凭据是浏览器里的 Google/IAP cookie），
   所以「有没有 token」这个问题在管理面**不存在**。
   本轮据此把 `web/admin` 的 `LoginPage` 从「登录表单」改成**准入状态页**
   （三个 input 受控但保持 disabled 并写清上面五条；页面主体跑一次准入探测，
   给出「重新走 Google 登录（整页重载）」这个真正有用的动作）。
   **原注释写的「管理面复用同一端点 `login`」是错的**：`login` 在 openapi 里 `tags: [user]` / `security: []`，
   handler 查的是 **users 表**，发的是管理面根本不读的用户面 token。
   接上去的结果是「提示登录成功 → 每一页照样 403」。
2. **守卫只能是准入探测，不能是本地会话。** 探 `GET /api/v1/admin/audit?limit=1`
   （最便宜、不挑角色、不挑权限位、且已实现），**结论不缓存** ——
   缓存的「你是管理员」在管理员刚被禁用那一刻正好是错的。
3. 🔴 **这条缺口决定了管理面的部署形态，因而直接改写 roadmap B19。**
   `parseAdminIAPAudience` 在启动期把 audience 钉死成 `/projects/<数字>/global/backendServices/<数字>`
   并**显式拒绝写成 Cloud Run 服务 URL**（`api/internal/config/admin_test.go` 逐条钉住）。
   **`backendServices` 这个形态只能来自一个外部 HTTPS 负载均衡器的后端服务** ——
   也就是说「`bp-admin` 是不是独立服务」这个问题，答案已经被代码钉了一半：
   **管理面必须坐在一个挂着 IAP 的 GCLB 后面**，而这套东西现在**一件都没建**。

### 4.2 🔴 两个直接动钱的权限位在 API 上看不见也授不了

| 侧 | 事实 |
|---|---|
| **schema** | `admin_users` 有 **4 个** `perm_` boolean 列（`0002_foundation.up.sql:62-65`）：`perm_mark_order_paid`（D6）、**`perm_refund`（D7）**、**`perm_adjust_balance`（D10）**、`perm_export_csv`（D14），另有 `role`（`owner`/`admin`/`support`） |
| **契约** | `AdminPermission` 枚举**只有 7 个值**：`admin.order.mark_paid` `admin.user.export` `admin.user.write` `admin.node.write` `admin.plan.write` `admin.ticket.write` `admin.settings.write` |
| **缺口** | 🔴 **`perm_refund` 与 `perm_adjust_balance` 在契约里没有任何对应枚举值** —— 两个直接动钱的权限位，在 API 上既**看不见**（`AdminAccount.permissions` 里不会出现）也**授不了**（`updateAdminPermissions` 无从表达） |

**本轮的处置**：写侧只接受那两个对得上的（`admin.order.mark_paid` / `admin.user.export`），
其余一律 **422 并说明「由角色决定」——绝不假装成功**。
**这是唯一诚实的选项**：静默忽略会让「我给他授了退款权限」与「他有退款权限」变成两件事，
而这个差值只会在有人退了一笔不该退的钱之后才被发现。

### 4.3 🔴 `createAdmin` 造出来的管理员登不进去

- `admin_users.totp_confirmed_at` 是 **NOT NULL**（`0002_foundation.up.sql:57`，与 `totp_secret_enc` 一起），
  注释原文：「强制 TOTP：两列都 NOT NULL，数据库层面不存在『没有 2FA 的管理员』」。
  **所以库里不存在「待绑 2FA」这个状态**，secret 在创建那一刻就得生成。
- 而 `createAdmin` 的 201 响应是 **`AdminAccount`**（`openapi.yaml` 实读），
  它有 `permissions` / `totp_enabled` / `created_at`，**没有 `TotpEnrollment`** ——
  **明文 secret 无处可去。**
- **正确的开户是两步**：`createAdmin` → **立刻** `resetAdminTotp`（后者的响应里才有绑定材料）。
  本轮的实现给 201 多带了一个 `X-Next-Step` 头指向 reset-totp，**但那是补丁不是修复**。

> 🔴 **不把这两步写进后台操作文档的话，现场唯一的「解法」是直接改库。**
> 而这是一张 append-only 审计覆盖不到的旁路。

### 4.4 🔴 ADR 0012 §5.3 的报价公式漏了汇率的 1e4 定点基数

`docs/05-adr/0012-payment-gateway.md:295` 原文：

```
amount_usdt6 = ceil( amount_due_cents × 1e6 × (1 + fx_buffer) / (cny_per_usdt_e4 × 100) )
```

**照它算 ¥100.00 @ 汇率 7.15、缓冲 1%**（`revenue:fx_buffer` 科目，ADR §15.2「明记不藏」）：

```
10000 × 1e6 × 1.01 / (71500 × 100) = 1412.6   →   0.0014 USDT
```

**小 10000 倍。** 分母里的 `cny_per_usdt_e4` 已经是 ×1e4 的定点整数，公式里没有抵掉这个基数。
正确量纲（`api/internal/handler/order.go:571` 的 `quoteUSDT6`，注释在 `:578` 逐条对齐四个基数）：

```
cents × 1e10 × (10000 + bps) / (e4 × 100 × 10000) = 14,126,000 (1e-6 USDT)   →   14.13 USDT
```

> **它是怎么被抓到的**：agent 第一版照抄 ADR，**测试当场炸出来**。
> **ADR 原文待修，本轮没动它** —— 它是已合并的裁决，改它要单独一次，
> 并按 [docs/README §4](../README.md) 逐条交代落点。**登记在此，见 roadmap 新增条目 B50。**

### 4.5 🔴 两张表不存在，5 个 operation 因此只能 501

`domains` 与 `mail_templates` 在 19 支迁移里**一次都没出现**
（`grep -rn 'CREATE TABLE' api/db/migrations/*.up.sql | grep -iE 'domain|mail_template'` → 空）。
逐条阻塞原因见 §2.3 第 1–5 行。**两张表都不是「忘了建」**：
`domains` 卡在两份未批准 ADR 且字段模型有两套；`mail_templates` 缺的是正文存储这个决定本身。

**顺带查到另外两张同类的表**（契约提到、库里不存在，本轮各自在注释里登记）：
`mail_queue`（`broadcastAdminMail` 的 202 响应描述写着「走 `mail_queue` + `/internal/tasks/mail-send`」）、
`traffic_batch`。

### 4.6 🔴 `commissions` 与佣金契约有两处不兼容

| # | 缺口 | 事实 |
|---|---|---|
| 1 | **没有 `amount_transferred` 列，而契约的划转金额是自由金额** | `commissions` 只有 `amount bigint NOT NULL CHECK (amount >= 0)`（`0007_ledger.up.sql:70`），一条佣金**要么整条 `transferred` 要么不动**；而 `CommissionTransferRequest` 只有一个 `amount`（`minimum: 1`）、**没有 id 列表**。本轮的实现只能取「按 `(confirmed_at, id)` 排序的前 k 条前缀和恰好等于 `amount`」这一个确定解释（`wallet.go` 的 `pickCommissionsForAmount`），**任何「凑出这个金额的任意子集」的实现都是在发明语义** —— 同一个金额可能有多个子集能凑出来，而选哪一个是用户看得见的差别 |
| 2 | **状态机差一格，而缺的那一格是「这笔钱没了」** | 契约 `Commission.status` 枚举是 `[pending, confirmed, settled]`；DB 的 CHECK 是 `('pending','confirmed','transferred','voided')`（`0007_ledger.up.sql:73`）。`transferred → settled` 是显然的，**`voided` 在契约里没有对应值**，而两个候选都是谎话：映射成 `settled` = 告诉用户「这笔已经到账了」而它永远不会到；映射成 `pending` = 让他一直等一笔永远不会来的钱。本轮选了伤害较小的 `pending`，并保证它**不进任何「可划转」合计** |

**openapi 自己在这一处标着「佣金结算状态机未设计」，即 roadmap B37。**
**裁决前不加列**：加了会让一条佣金同时处在两个状态。

---

## 5 · 单位经济 —— 相对上一版无变化

| 数字 | 值 | 出处 |
|---|---|---|
| 定价基准单价 `u` | **$0.11/GiB**（Standard 目录价） | Billing Catalog API；且它是本项目全生命周期唯一适用的档位 |
| 有效变动单价 `c = u × k` | **$0.121/GiB** | `k = 1.10` 🔴 **设定值，无实测依据** |
| 三档定价 | **¥72 / ¥159 / ¥358** | 30 / 100 / 250 GiB，2 / 5 / 10 设备（[pricing §3.1](../03-product/pricing-and-plans.md)） |
| 24 格覆盖倍数 | **1.2749× ~ 1.5616×** | 最薄格 = 重度·年付·USDT；含一次性返佣后 **1.2620×**，全 24 格无一破 1.20× 地板 |
| 满编成本上界 | **$412.24/月** gross | 8/12/5 拆分、2,690 GiB；落在线上 $500 预算的 **82.4%** |
| 满编收入 | **¥4,274 = $597.76/月** | 覆盖 **1.4500×** —— 这是**下界**（满额兑付 + 免费额度记 0） |
| 竞品零售 | **$0.04505/GiB** | 仅为 GCP Standard 全球最便宜源区域目录价（Oregon $0.085）的 **53.0%** —— **不是效率差距，是商业模式差距** |

🔴 **两个仍然悬空的参数**：`k = 1.10`（无出处、无实测，直接乘在变动成本上，要第一台节点跑满一个账期才能对账）与
`FX = 7.15`（二手反推，且 2026-08-23 的检索聚合显示现汇可能是 6.72–6.74，**我们可能在超收 6.1%**）。

> 🟢 **本轮给这一节添了一件小而实的东西**：报价公式的四个定点基数第一次被**测试钉住**了（§4.4）。
> 在此之前，`FX = 7.15` 这个悬空参数下面还垫着一个**算错 10000 倍**的公式 ——
> 也就是说这张表右下角那个「多收 6.1%」的担心，在代码里本来是「少收 99.99%」。

---

## 6 · 这一轮修掉的文档矛盾与实测缺陷

> 上一版这一节记的是 2026-08-21 → 08-30 那九天的文档失同步（12 处以上，已全部修完，**不在此重复**）。
> 本节只记这一天的 10 个提交里**新修掉的**。

| 类别 | 修了什么 | 为什么它此前抓不到 |
|---|---|---|
| 🔴 **生成列破坏付款链** | `0016` 把 `users.transfer_enable` 变成 GENERATED STORED 后仍有 **9 条**查询在写它（ADR 0013 §6.5 列了 8 条，`CreateRefund` 是超出清单的第 9 条） | `sqlc.yaml` 没有 `database:` 段，`sqlc generate` 与 `go build` **都 exit 0**。**第一次暴露是在用户付款成功、订单进 paid、开通权利那一刻返 500** |
| 🔴 **入账路径有两条，而 ADR 0012 §8.4 硬约束 1 要求只有一条** | 保留 `order.go` 的 `processDeposit`，`runChainScan` 改调它，删掉 `tasks.sql` 的 `RecordChainPayment` / `SetOrderPayFromAddress` | 被删的那条按 `order_id` 累计（§6.3 的口径是按 `to_address`），且 `order_id`/`user_id` 非空，**结构上无法记录 §8.4 分支 ②——钱打到我们地址但找不到订单**。而 ADR 0012 §1 把消除「钱进黑洞」列为整份裁决的核心 |
| 🔴 **少付对账队列会静默吃掉工单** | 三处 LATERAL 求和没有排除拉黑的钱，而入账判「付清没有」的 `SumAddressReceipts` 排除了 | 存在这样一种状态：一张单的缺口恰好被一笔 `aml_verdict='blacklisted'` 的到账「补上」—— 入账路径仍判 `underpaid`、订单卡住不动，而少付清单的谓词变成 false，**这张单从队列里消失**。要等用户投诉 |
| 🔴 **离职再入职的管理员登不进来** | `0019` 把 `admin_users_email_uk` 改成 `WHERE disabled_at IS NULL`（软停用后邮箱不再被永久占住），但 `LookupAdminByIAPEmail` 是不带条件的 `QueryRow`，**会静默取到先插入的停用行并 403**。改为 `ORDER BY (disabled_at IS NULL) DESC, id DESC LIMIT 1` | 跨文件、跨迁移与中间件两侧，**在真库上复现过**。刻意不用迁移注释建议的 `AND disabled_at IS NULL`：那样会让「已停用的管理员来敲门」与「这邮箱压根不是管理员」塌成同一条日志 |
| 🔴 **在线设备数偏大导致用户被误判超限** | `ListAliveDeviceCounts` 从 `count(*)` 改为 `count(DISTINCT device_ip)` | 表主键是 `(user_id, server_id, device_ip)`，同一 IP 连多个节点会被重复计数 → **用户明明只开 2 台却被判超限** |
| 🔴 **缺两个记账科目 = 用户点按钮就 500** | `0018` 补 `expense:commission`（`0015` 的 seed 来自 ADR 0012 §17.6(c)，只覆盖收款链路；佣金是 ADR 0013 §3.5 的事）、`0019` 补 `expense:admin_adjust`（管理员手工调余额**不属于任何一份已有裁决** —— 它是运维动作不是业务流程） | 缺它的现象不是启动失败，是**用户点「划转佣金」时 500** |
| **CI 对整类缺陷全盲** | 新增 `db_explain.py`（**343 条**语句逐条 `EXPLAIN (GENERIC_PLAN)`，**172 条写语句**）+ `ci_post_rollback_write.sql`（回滚后**先 INSERT 一行真实数据再打到那一行**并断言 `ROW_COUNT=1`） | `migrations` 作业此前只灌 up/down 再数对象个数，**一行数据都不写**。🔴 ADR 0013 §6.4 原提议的 `UPDATE … WHERE false` **抓不到它要抓的东西**：影响 0 行 ⇒ ROW 触发器不执行 ⇒ plpgsql 永不解析字段名 |
| **四支运维脚本的四个真缺陷** | `setup-wif.sh` 双引号串里一对未转义反引号（warn 一触发就会真的执行 `gcloud`）；`setup-metrics.sh` 的 `$*` 是函数参数（恒空）；`setup-scheduler.sh` 的 `--delete --dry-run` 实际是**一次创建的预演**；`print_summary` 在空数组上被 `set -u` 判成 unbound | 最后一条是 **bash 3.2 行为，而 3.2 正是 macOS 自带的那个** —— CI 的 ubuntu 是 bash 5 不报，**所以这类崩溃过得了 CI，只在真人跑的时候炸** |
| **前端的一处防枚举规则被收窄** | 「无论后端返回什么都显示成功页」→ 204/404/409/422 抹平成同一个成功页；429 给倒计时；**5xx / 离线 / 501 如实报错** | 防枚举要防的是「两条路径可被区分」，而 5xx 对两种邮箱同样会发生，**假装成功拿不到任何防枚举收益，只会让一个正在找回账号的人白等一封根本没发出去的信** |

---

## 7 · 需用户拍板的决策 —— 9 条，一条都没关掉

**这一天写了 25,000 行代码，下面 9 条没有一条因此被关掉。** 这是本文最该被记住的一张表。

| # | 决策 | 状态 |
|---|---|---|
| 1 | **域名策略** | 🔶 [ADR 0010](../05-adr/0010-domain-strategy.md)（**提案，未批准**）。缺的是 5 个镜像域名（**采购不可退，需用户本人付钱**） |
| 2 | **退款政策** | 🔶 [ADR 0013](../05-adr/0013-billing-and-refund-rules.md)（**提案，未批准**）。数据库侧已有 `refunds_cooling_off_once` 约束 —— **即数据库已按未获批准的裁决改了形状** |
| 3 | **支付网关 + AML** | 🔶 [ADR 0012](../05-adr/0012-payment-gateway.md)（**提案，未批准**）。⚠️ **今天新增一层紧迫**：11 条支付 operation 已经实现了，代码里坐着一条完整的收款链路，而**裁决没批、AML 完全未定**。它现在是「实现了但不许开」而不是「还没做」 |
| 4 | **`vpn-us` / `vpn-jp` 上是否有人在用** | 原封不动。**本次实查确认两台仍在 RUNNING 且仍是 PREMIUM 层的成本来源** |
| 5 | **升级折抵 + 流量包重置** | 🔶 [ADR 0013](../05-adr/0013-billing-and-refund-rules.md)。**降档 / 多次升档 / 加油包余量三种情形仍无算式**。⚠️ 本轮实现时**又发现一处 ADR 与实现的有意偏差**：地板断言的分子用 `gross − discount − accrual` 而不是 A7 写的 `amount_due`，否则**任何一次升级都必然破地板**（有用例证明 naive 那一半确实被拒） |
| 6 | **是否采购境内探测能力** | 原封不动。卡着 `nettier-ab-*`，而那是**定价定稿的第二个前置** |
| 7 | **iOS 首推客户端** | 🔶 [ADR 0015](../05-adr/0015-client-strategy.md)（**提案，未批准**）。⚠️ 本轮据此**刻意没做**客户端「一键导入」深链：tutorials-spec 把 scheme 逐客户端列为「待核实」，唯一已核实的 `sing-box://import-remote-profile` 要的是完整 profile，**那正是 ADR 0015 裁决 ② 闸住、尚未放量的东西** |
| 8 | **设备数限制的产品口径** | 🔶 [ADR 0015](../05-adr/0015-client-strategy.md)。且它是定价失效条件之一 |
| 9 | **SLO / on-call** | 🔶 [ADR 0014](../05-adr/0014-slo-and-oncall.md)（**提案，未批准**） |
| **10** | **`deploy.yml` 的四项 TODO 里有两项是裁决而不是配置** | `BP_WEB_DEPLOY_CMD` 等 ADR 0003 的托管选型；staging 用不用独立 GCP 项目也没裁决过。见 §8 的 B47 |
| **11**<br>🆕 | 🔴 **管理面的部署形态** | **本轮新提。** §4.1 已经把它从「要不要」变成「必须」：`parseAdminIAPAudience` 要求 `/projects/<数字>/global/backendServices/<数字>`，**这个形态只能来自一个挂 IAP 的外部 HTTPS 负载均衡器**。要拍的是：建不建这套 GCLB + IAP（约 $18/月起，与 B9 的代理选型是同一笔钱），以及 `bp-admin` 是否独立成第二个 Cloud Run 服务（会再吃一份 max-instances 与连接数预算，ADR 0005 §6.2 的公式要重算）。**在这一条落地之前，56 个已实现的管理面 operation 一个都进不去** |

---

## 8 · 阻塞项：3 条新登记（B50–B52）+ 5 条状态变化

**详见 [roadmap §9](roadmap.md) 的追加式更新。** 本节只给摘要。

**新登记（B50–B52，全部来自 §4）**：

| # | 一句话 | 为什么现在才登记 |
|---|---|---|
| **B50** | **ADR 0012 §5.3 的报价公式漏了汇率的 1e4 定点基数**，照它算 ¥100 @ 7.15 得 0.0014 USDT | 公式此前没有任何实现，也就没有任何东西会对它求值。第一次有人照它写代码，测试当场炸 |
| **B51** | **管理面没有登录端点**：`adminSession` 在冻结契约里有声明无实现，45 条 admin 路径里没有 login/session/me，`AuthenticateAdmin` 从不读 `Authorization` | 此前 61 个 admin operation 全是 501，前端也没有一页接线，**没有任何代码路径会去问「管理员怎么登录」** |
| **B52** | **`perm_refund`(D7) 与 `perm_adjust_balance`(D10) 在 `AdminPermission` 枚举里没有值** —— 两个直接动钱的权限位在 API 上看不见也授不了 | 同上。另附两条同源缺口：`createAdmin` 造出来的管理员登不进去（§4.3）、`commissions` 状态机差一格（§4.6） |

**状态变化的 5 条**：

| # | 变化 |
|---|---|
| **B48** 管理面与内部面鉴权 | 🔶 → 🔶 **但内容全变了**。接线**已提交**（`01350425ef1`），80 个新测试，`authwiring_test.go` 遍历全部 70 个 operationID 各跑 7/8 种伪造凭据。🔴 **仍不记 ✅**：生产 `bp-api` 上**没有 `BP_ADMIN_IAP_AUDIENCE`**（本次 `gcloud` 实查），管理面在线上整体关闭；且 §4.1 揭示登录这一半在契约里就不存在 |
| **B19** `bp-admin` 是否独立服务 | 🔴 → 🔶 **问题被 §4.1 改写了**：不再是「要不要独立」，而是「IAP audience 的形态要求一个 GCLB 后端服务，这套东西建不建」 |
| **B27** 前端框架 / 组件库 | 🔶 → 🔶 **前端侧的那一半做完了**：623 个用例、admin 23 页接线 21 页、`DangerousAction.tsx` 742 行收齐四层。🔴 **组件库仍未选型**：`DangerousAction` 是**行内块不是 modal**（按 `role="dialog"` / `aria-modal` / `<dialog` 三个模式各 grep 一次，三次都无命中），因为可访问的确认对话框在本仓不存在（web/README §7 代价 5） |
| **B37** 佣金状态机 / 群发筛选 | 🔴 → 🔶 **两半各有进展、各有硬缺口**。佣金：见 §4.6 两条不兼容。群发：`MailBroadcastRequest` 只有粗粒度 `audience` 枚举 + 可选 `plan_ids`，契约自己写着「收件人筛选表达式怎么表示是个独立的设计问题，尚未裁决」；**自定义正文那一半必须 501**（`email_log` 没有正文列） |
| **B44** PR 栈 13 条缺陷 | ✅ 保持。⚠️ **「已修」仍不等于「已验证」** —— `bp-node-*` 现有 0 台（本次实查） |

**B1–B52 总账**（详见 [roadmap §9](roadmap.md)）：**✅ 已解决 13 · 🔶 部分 20 · 🔴 开放 19。**
（算式：上一版 49 条 = ✅13 · 🔶18 · 🔴18；本轮 **B19 与 B37 由 🔴 转 🔶**，
**新增 B50 / B51 / B52 三条均记 🔴** —— 13 + 20 + 19 = 52。）

> 🔴 **B5 / B16 / B21 / B24 / B25 / B26 / B29 / B34 / B35 这九条仍然一律记 🔶 而不是 ✅，
> 只有一个理由：它们对应的 ADR 0011–0015 全部是「提案，未批准」。**
> 今天的 10 个提交没有改变这九条中的任何一条 —— **写了实现同样不等于批准了裁决。**
> ⚠️ 而且今天它变得更尖锐了：**代码已经按未批准的 ADR 0012 / 0013 实现完了**，
> 不只是 schema 按未获批准的裁决改了形状，现在**逻辑也是**。

---

## 9 · 通往首笔收款的关键路径（本次修订）

硬性时间窗（不可压缩）：域名可达性采样 1 周 · 节点单人验证 72 小时 · 灰度 7 天。

| 序 | 动作 | 链 | 依赖 | 与上一版的差别 |
|---|---|---|---|---|
| 0 | 汇率用权威源核实一次（Fed H.10 / 人民银行中间价） | 商业 | 无 —— **今天一次查询** | 不变。**但现在它下面那个公式是对的了**（§4.4） |
| 1 | **建 `bp-node-hk1`** | 技术 | 无（`babel.plus` 已可签 LE / DNS-01） | 🔴 **升为唯一的头号动作。** 上一版它排第 1 但和第 5「接线管理面鉴权」并列重要；**今天软件侧的理由全部消失了** —— 六个节点面端点齐了、9 个定时任务端点齐了、订阅下发实现了。**P1 现在只差一台机器** |
| 2 | 装机 9 步 → **72 小时单人验证** = P1 出口 | 技术 | 1 | 不变 |
| 3 | **部署一次** —— 让生产跑上 HEAD | 技术 | 无 | 🆕 **新增，而且它可能是全表最便宜的一步。** 生产现在跑 `618bf1c`（18/128），HEAD 是 120/128，差 14 个提交。⚠️ **走 `deploy.yml` 就要先填四项 TODO 并配 WIF**（B47），走 `deploy-api.sh` 则又一次绕开唯一被写下来的部署路径 |
| 4 | 接通 ESP 发信 | 商业 | ⏳ 用户选 ESP | **优先级再升**：注册 / 找回 / 重置三页前端**已经完整**，而真人收不到那封信 —— 这一页的成功率就是失联恢复的成功率 |
| 5 | 建 GCLB + IAP，配 `BP_ADMIN_IAP_AUDIENCE` 与 `BP_ADMIN_TOTP_ENC_KEY` | 技术 | ⏳ 决策 11（§7） | 🆕 **替换掉上一版的第 5 步「接线管理面鉴权」——那一步今天做完了。** 现在挡在后台前面的不是代码，是**一套没建的基础设施 + 一个没拍的板** |
| 6 | 支付网关落地 + 落地页与 `docs.*` 教程站 | 商业 | ⏳ 决策 3、P1 出口 | **收窄了**：关键路径 8 页里 6 页已接线，剩下的两个是第三套前端 |
| 7 | 3–5 人灰度 **7 天** | 汇合 | 2 + 6 | 不变 |
| 8 | **第一笔真实 USDT 收款「下单 → 到账 → 自动开通 → 对账」= 上线** | 汇合 | 全部 | 不变。⚠️ 提醒一条本轮登记的缺口：`paid → completed` 的权益开通**未接**，`markOrderPaid` 现在**响亮地**停在 paid（每次打一条 `metric=bp_order_paid_not_provisioned` 的 ERROR）—— 缺一条「首次开通的 covers_from/covers_to/reset_at」查询，而 `reset_at` 的口径唯一实现在 `stats.sql` 的 `AdvanceUserResetCycle` 里，在 Go 侧再抄一份就是本仓反复警告的漂移 |

> **本次最重要的路径变化**：**技术链上第 1 步的最后一个软件借口消失了。**
> 上一版说「第 1 步的前置从『等用户注册域名』变成『无』」；这一版说：
> **前置是无，配套代码也齐了，剩下的只是有没有人去敲那条 `gcloud compute instances create`。**

---

## 10 · 逐条对读

### 10.1 与本文上一版（同日，基线 `a4604c9396f`）的对读

> 上一版被本版整体替换（理由与结构变动见文档头的「同日重写说明」）。**下表逐条交代它的判定在本版的落点。**

| 上一版的说法 | 本版的实况 | 性质 |
|---|---|---|
| §2.1「API 已实现 **18 / 128**，仍返 501 **110**」 | **120 / 128；仍 501 8 条**（另 2 条分支级） | **推翻**（同日 6 个提交） |
| §2.1「迁移 17 组 / **44 张表**」 | **19 组 / 47 张表** —— 且 44 那个数**当时就是错的**：grep 模式漏了 3 条 `CREATE UNLOGGED TABLE` | **推翻 + 纠错** |
| §2.1「sqlc **194 条**」 | **343 条**（三处口径一致） | 推翻 |
| §2.1「Go 测试 20 文件 / 195 函数」 | **37 文件 / 573 函数** | 推翻 |
| §2.1「web 测试 **108 个用例 / 7 个文件**」 | **623 个用例 / 48 个文件**（本次真跑） | 推翻 |
| §2.1「web 未接线 **44 处 TODO(P1) / 30 个文件**」 | **22 处 / 16 个文件** | 推翻 |
| §2.1「CI 作业 9 个」 | `ci.yml` **9 个**不变；**补记 `deploy.yml` 的 6 个**（上一版漏了这一半） | 补全 |
| §2.2「GCP 实况全部转述自 2026-08-20，未跑 `gcloud`」 | 🟢 **本次实查，全部转述结论均被确认**，无一被推翻 | **证据升级** |
| §3「P1 按工作量 ≈45%」 | **≈70%** | 推翻（口径不变，分子变了） |
| §3.2「三态依赖 9 个 `RunXxxTask`，它们全部 501」 | **9 条全部实现** | 推翻 |
| §3「P1 按出口标准 **0 / 8**」 | **0 / 8**，八条逐条判定一字未改 | **不变 —— 这是本文的核心判定** |
| §7 B48「接线已在未提交的工作树里」 | **已提交**（`01350425ef1`），并补了 80 个测试 | 落地 |
| §10 代价 2「没跑 `go test`」 | **仍然没跑**（本机无 Go 工具链），573 这个数同样是静态计数 | 不变，见 §11 代价 2 |
| §10 代价 3 / §11 第 2 条「没跑 `gcloud`」 | ✅ **已解决**，见 §2.2 | 关闭 |
| §11 第 3 条「生产 serving revision 与 HEAD 的对应关系未验」 | ✅ **已解决**：`bp-api-618bf1c` = commit `618bf1cc89b3`，是 HEAD 的祖先、仍被 master 引用，**落后 14 个提交** | 关闭，见 §1 第 1 条 |
| §11 第 4 条「没有做代码审查」 | **仍然没有** | 不变，见 §12 |

### 10.2 与 [20260821 那份](launch-readiness-review-20260821.md) 的对读

> 那份是 As-Built、不回改。本节只做对读，**不修改它的任何一个字**。

| 20260821 的说法 | 2026-08-30 的实况 | 性质 |
|---|---|---|
| §1「生产与 master 漂移」是头号发现 | ✅ **PR 全部合并，0 个 open PR**；🔴 **但一个新的漂移出现了，形状相反**：不是「生产跑着未合并的分支」，是**生产跑着 14 个提交之前的 master**（§1 第 1 条） | 旧问题解决 / **新问题** |
| §1 / §2.2 / §3「域名 **0 个注册**」 | 🔴 **前提是错的**（`dig` 实查，2026-08-30 复核） | 纠错 |
| §2.1「API 18/128，155 顶层测试」 | **120/128；573 个测试函数** | 大幅真进展 |
| §2.1「12 组迁移、41 张表、189 条 sqlc 查询」 | **19 组 / 47 张表 / 343 条查询** | 真进展 |
| §2.1「Web 双 SPA 空壳，无登录守卫，**测试 0**」 | **623 个用例全绿**；user **20 条业务路由全部接线**（另两条 `path=` 是 `/` 重定向与 `*` 的静态 404）、admin **23 页接线 21 页** | 大幅真进展 |
| §2.1「infra 11 个脚本 5,571 行」 | **18 支 10,705 行** | 工作量增加，**执行次数仍是 0** |
| §2.1「CI 8 作业全绿；`deploy.yml` 从未执行」 | **9 作业**；`deploy.yml` **仍是 0 次**，仓库 0 environment/variable/secret | **认知更清楚，没改善** |
| §2.2「不存在任何 `bp-node-*` / `bp-web`」 | 🔴 **本次 `gcloud` 实查确认：仍然一个都没有** | 不变 |
| §3「P0 ≈ 80%」 | **按出口标准 3.5/7 = 50%**；按工作量 ≈ 90% | 口径更严 |
| §3「P1 ≈ 40%」 | **按出口标准 0/8 = 0%**；按工作量 ≈ 70% | 口径更严 |
| §3「P2 ≈ 5%」 | 出口标准 0/8；工作量 ≈ 35% | 真进展 |
| §3「P3/P4 = 0%」 | 出口标准仍 0；P3 工作量 ≈ 25%（四层强制全栈落地） | 半进展 |
| §4「这笔账目前算不出来」 | ✅ **算得出来了**，且报价公式的定点基数第一次被测试钉住 | 实质进展 |
| §5 第 4 条「起真实 v2node 容器验 401/403」 | 🔶 **答案有了（读源码），容器仍未起** | 半 |
| §5 第 6 条「抓客户端 UA」 | 🔴 **仍未做**（B17，七条「成本以分钟计」里唯一零进展的那条） | 不变 |
| §8「已解决 8 · 部分 4 · 开放 28」（B1–B40） | **B1–B52：已解决 13 · 部分 21 · 开放 18** | 条目数 +12 |
| §11 代价 1「本文是时点快照，发布即开始过时」 | **应验两次** —— 第二次是**在同一天内**（本文上一版发布后几小时就失准了） | — |

**九天加一天的净变化，一句话：**
**软件从「骨架」变成了「几乎完整」，而可上线性仍然是零位移**
（节点 0 台、收款 0 笔、告警 0 条、部署 0 次、生产落后 14 个提交）——
**现在阻塞上线的，一件都不是代码。**

---

## 11 · 代价

> ⚠️ 这一选择的代价，必须留在记录里而不是被措辞掩盖：
>
> 1. **本文是时点快照，发布即开始过时 —— 而这一条今天已经应验过一次了。**
>    本文上一版在同一天内就因为 6 个提交而失准（§10.1 整张表）。
>    事实基线钉在 `b6e7603e7f9`。**下一次它失准可能同样只需要几小时。**
>    **本文此后不再回改** —— 后续进展应更新 [roadmap.md](roadmap.md) 与各活文档。
>    🔴 **这条代价指向一个真实的方法问题**：在一天能落 10 个提交的节奏下，
>    「时点快照」这种文体的半衰期比写它的时间还短。
>    **应当考虑的替代形式**：把 §2 的数字表换成一个由 CI 生成的产物，让它不需要人来维护。本文没有做这件事。
> 2. 🔴 **本次没有跑 `go test`，也没有跑 `make db-explain` / `make migrate-verify`。**
>    本机 `which go` 无结果，`psql` 无结果，**Docker daemon 未运行**（`docker version` 报
>    `Cannot connect to the Docker daemon`），而这三个目标全都要求其中之一。
>    **具体到每个数字**：
>    - 「573 个顶层 `Test` 函数」是 `grep` 的静态计数，**不是一次绿灯**。
>      如果这 573 个里有失败的，本文不知道，而本文的其它判断（比如「API 侧 120 个 operation 可用」）部分依赖它们是绿的。
>    - 「343 条语句 / 172 条写语句」是 `python3 api/scripts/db_explain.py` 的**抽取**结果（这一步不需要数据库），
>      **`EXPLAIN` 本身没有对真库跑过**。最后一次真跑的记录在 commit `bdc4437d0fe` 的信息里。
>    - 「47 张表」是对 19 支迁移的静态计数（44 条 `CREATE TABLE` + 3 条 `CREATE UNLOGGED TABLE`），
>      **不是 `make migrate-verify` 的输出**。最后一次真跑的记录在 commit `3e18dd8b269`（「up 后 47 表，down 后残留 0」），
>      而 `0018`/`0019` 不建表，所以两者应当一致 —— **「应当」是推理，不是观测。**
>    - **web 侧的 623 是真跑出来的**（`pnpm test` 全绿）。
>    **两类数字的证据强度不一样，不要并列引用。**
> 3. **把完成度按出口标准算，代价是数字看起来与工作量脱节，且脱节在今天创了新高。**
>    P1 工作量一天内 45% → 70%，出口标准 0/8 → 0/8。
>    一个「干了一整天、进度条纹丝不动」的表在压力下极易被改回宽口径，
>    而 [roadmap §12 代价 6](roadmap.md) 已经预言过：
>    「在只有出口标准没有回退规则的排期里，压力下的默认行为是**降低出口标准**」。
>    **本文能提供的唯一防护是把两个数字并列写出来**，让「换尺子」这个动作可见。
> 4. **§10 的逐条对读把三份审查压成两张表，丢失了各自的语境。**
>    表里每行都指了出处，但读表的人不会去翻。
>    **已知的具体损失**：20260821 §3 那句「P1 ≈ 40%」在它的原文里是有解释的，
>    到了表里被压成「40% → 0%」两个数字，看起来像项目倒退。
> 5. **§4 这一节把六条缺口写成了「发现」，而它们同时也是一份未完成工作的清单。**
>    把缺口写清楚会让人产生「这件事已经处理了」的错觉 —— 事实是**六条一条都没修**，
>    只是从「不知道」变成了「知道且登记了」。B50–B52 三条新阻塞项就是这个转化的代价：
>    **阻塞项总数从 49 涨到 52，而这是好事** —— 但读总账的人看到的是数字变大。
> 6. **本文用了 roadmap 自己的出口标准做尺子，而没有质疑那套标准。**
>    已知一处张力：P1 出口标准 4（「订阅链接在两个客户端各人工加载一次成功」）
>    是**不可自动化**的判据，而 B45 / B46 两条都指向它会失败 ——
>    也就是说 P1 可能存在一个「其余七条都过、第 4 条过不了」的状态，
>    而 roadmap **没有为任何阶段定义「失败」**（§12 代价 6 自陈）。本文同样没有。

## 12 · 这次没有解决的

- [ ] 🔴 **没有跑 `go test ./...`、`make db-explain`、`make migrate-verify`。**
      本机无 Go 工具链、无 `psql`、Docker daemon 未运行。
      **下一次审查前应当先解决这一条** —— 573 / 343 / 47 这三个数字的证据强度
      比 web 侧的 623 低一整档，而本文多处判断依赖 Go 侧测试是绿的。
      ⚠️ 与上一版的同一条相比，**范围扩大了**：上一版只缺 Go 工具链，本次连 Docker daemon 都没起。
- [ ] 🔴 **§4 的六条缺口一条都没修**，全部只做了登记与（在能做的地方）**诚实降级**
      （权限位写侧 422 而不假装成功、`voided` 不谎称已结算、`DisableUserTotp` 不退化成 204）。
      不在本次范围是因为**每一条的修法都要么改冻结契约、要么加迁移、要么等一份 ADR 被批准** ——
      三者都超出「实现一个已在契约里的端点」的边界。
- [ ] 🔴 **ADR 0012 §5.3 的公式没有改。** 实现是对的，ADR 原文仍然是错的（§4.4）。
      改它要按 [docs/README §4](../README.md) 逐条交代落点，且它是已合并的裁决 ——
      **属独立一次修订**。在那之前，**任何照 ADR 原文写第二份实现的人会再踩一次同一个坑**。
- [ ] 🔴 **没有做代码审查。** 本文只核对「实现了什么、测了什么、跑没跑过」，
      **没有逐行看这 10 个提交的正确性** —— 而这一轮落了约 25,000 行非生成代码。
      ⚠️ 这一点比上一版更值得警惕：上一版指出 `a4604c9396f` 的提交信息自己写着上一个提交把主干弄坏了；
      **本轮的六个提交里，`3e18dd8b269` 与 `bdc4437d0fe` 又各自修掉了前一轮留下的静默缺陷。**
      这个规律连续三轮成立，**而本文仍然不是那道网。**
- [ ] **生产没有部署。** §9 第 3 步。本文查清了生产落后 14 个提交，**没有部署**，
      因为走 `deploy.yml` 要先填四项 TODO 并配 WIF（B47），而那两项里有一项需要裁决。
- [ ] **`user-journey.md` 的价格占位没有填。** pricing 的价格已定案，
      而 user-journey §16 仍写「所有价格数字留空」。填数字要连带核对旅程各步的金额示例与文案，属独立一次修订。
- [ ] **P1 出口标准 4 的「可能过不了」没有对应的阶段门。**
      见 §11 代价 6。roadmap 没有为任何阶段定义「失败」，本文没有补上这个空缺。
- [ ] **`web/README.md` 的章节顺序违反 [docs/README §4.1](../README.md)，只登记未修。**
      它是 `§7 代价 → §8 这次没有解决的 → §9 测试`，而规矩要求前两者物理最后。
      不修是因为改序要重编号，而 §7/§8/§9 在本文与 [roadmap](roadmap.md) 里都被按号引用。
      已在 `web/README.md §8` 就地登记。
- [ ] **没有评估「内部用户手工收款」这条捷径。**
      与 [20260821 §12](launch-readiness-review-20260821.md) 第 4 条同一条欠账。
      ⚠️ **今天它比昨天更有吸引力**：D6「手工标记订单已支付」的四层强制**已经全栈实现并有测试**，
      而支付网关仍卡在未批准的 ADR 0012 上。
      若内部用户少到可以手工收款，§9 的第 6 步可以大幅降级 —— 但那要求先回答决策 3，本文不代替该裁决。

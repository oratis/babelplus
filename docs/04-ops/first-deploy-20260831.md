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

## 4.1 · 当天晚些时候：域名接入与两处纪律事故（2026-08-31 追记）

同日在 §4 之上又做了一轮，**本节只记执行过的动作与观测到的结果**。
裁决依据：[ADR 0016](../05-adr/0016-domain-babelplus.md)（域名统一 `babel.plus`，当日批准）。

### 新增资源

| 资源 | 形态 | 备注 |
|---|---|---|
| `bp-admin-lb-ip` | 全局静态 IP `34.117.101.225` | 三个子域共用一个 LB |
| `bp-admin-lb` | 全局外部 HTTPS LB（URL map） | 三条 host 规则：`admin.` / `web.` / `api.` |
| `bp-admin-neg` / `bp-api-admin-neg` / `bp-web-neg` / `bp-api-public-neg` | serverless NEG（us-central1） | ⚠️ 后端服务**不能带 `--protocol`**：serverless NEG 会拒绝 port-name（实测踩到，删了重建） |
| `bp-admin-backend` / `bp-api-admin-backend` | 后端服务，**IAP=enabled** | `admin.` 的默认路由与 `/api/*` |
| `bp-web-backend` / `bp-api-public-backend` | 后端服务，无 IAP | `web.` / `api.` |
| `bp-admin-config-bucket` / `bp-web-config-bucket` | backend bucket（GCS） | 只托管一个 `/runtime-config.js`，理由见下 |
| `bp-admin-cert` / `bp-public-cert` | Google 托管证书 | `admin.` / `web.`+`api.`，均 ACTIVE |
| `bp-admin-totp-enc-key` | Secret Manager | `openssl rand -base64 32`，已授权 `bp-api-sa` |
| `bp-scheduler-task-failed` | 告警策略（**`bp-` 的第一条**） | `job_id=~"^bp-"` 非 2xx 单次即告警，走既有 email 渠道 |
| `admin_users` 首行 | id=1，`owner`，四个权限位全开 | 一次性引导程序（跑完即删，不进仓库），解 B52 的引导死结 |

### 🔴 两处必须留在记录里的事故

**① `runtime-config.js` 指向 `run.app` 绝对地址，经 LB 访问会绕过 IAP。**
两个 SPA 容器里那份配置是 §2 上线时写死的 `apiBaseUrl: 'https://bp-api-…run.app'`。
从 `admin.babel.plus` 打开时，浏览器会直连 run.app —— **请求不带 IAP 断言，全部 403**，
而现象是「后台能打开、每个接口都 403」，指向的是鉴权配置而不是这一行。
处置：LB 上给 `/runtime-config.js` 加一条 backend bucket 路径规则，
`apiBaseUrl` 留空 = 同源（`web/admin/src/lib/api.ts:40` 的 `|| window.location.origin`）。
**用户面同理**，只是它指向 `https://api.babel.plus` 且保留 run.app 作 `apiFallbackBaseUrls`。

**② 生产流量被钉在旧修订版上，导致「验证通过」是假绿。**
`gcloud run services update` 改环境变量会**建新修订版**，而流量此前被
`--to-revisions=bp-api-87886e4=100` 钉死 —— 新修订版建了三个都是 0% 流量。
于是「admin 无凭据 403」这条验证**看起来通过、实际来自没配 audience 的旧版本**：
它 403 的原因是「压根没配所以全拒」，而不是「配了且验签失败」。两者现象一样，结论完全相反。
判据：`gcloud run services describe --format='value(status.traffic)'`，不要只看 `describe` 的 spec。
⚠️ 同一个坑还有第二半：`candidate` **标签也不会自动跟着最新修订版走**，
本次实测标签仍指向上一个候选版，对着 `candidate---` 域名做的验证同样验错了对象。

### 本次上线暴露的第三条（已修）

**`deploy-api.sh` 会静默删掉线上已配的四个环境变量。**
`--set-env-vars` 是全量替换，而内部面/管理面那四项走「没 export 就不进列表」的路径 ——
合起来的后果不是部署失败，是**功能静默消失**：内部面两项没了 → 8 条 Scheduler 全部 403
（`expire-check` 停跑 = 到期用户继续免费上网，无人察觉）；管理面两项没了 → 后台整体拒绝。
本次候选版实际已经丢了这四项 + `BP_ALLOWED_ORIGINS` 的四个 run.app 源，**切流量前发现**。
处置见 [PR #28](https://github.com/oratis/babelplus/pull/28)：部署前读线上实况，
只在「线上有、这次没传」时拦截并给出处置。这是 [deploy.md](deploy.md) 地雷 4 的可执行形态。

### 实测结果（观测，不是推断）

```
DoH 解析（dns.google）  web./api./admin.babel.plus → 34.117.101.225   三个全对
api.babel.plus/-/healthz                            ok
api.babel.plus/api/v1/admin/dashboard               403（无凭据）
bp-api（生产，修订版 bp-api-00009-7dn）
  /-/healthz                                        ok
  admin 无凭据 / 伪造 IAP 断言                        403 / 403
  CORS：web./admin.babel.plus 与 4 个 run.app 源      逐个回显
  CORS：evil.example.com                            0 命中
Scheduler bp-expire-check 手工触发                   200（内部面未被切流量弄坏）
verify-isolation.sh 部署前后                         18 项通过 / 0 失败
```

⚠️ **管理面仍进不去**：IAP 的 `oauth2ClientId` 为空（502 `Empty Google Account OAuth client ID(s)`）。
IAP OAuth Admin API **已于 2026-03-19 永久关停**，且本项目不属于组织 —— gcloud 与 API 都没有路径，
**只能控制台**：配 Google Auth Platform 品牌（External / Testing）后，在 IAP 页面把两个后端服务
的开关关掉再打开，由控制台挂上 Google 托管的 OAuth 客户端。

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

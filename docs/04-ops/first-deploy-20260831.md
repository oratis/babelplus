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

## 4.2 · ESP 接通：第一封真实送达的邮件（2026-08-31 追记）

选型 **Resend**（用户裁决），发信域 **`mail.babel.plus`**，区域 `ap-northeast-1`。
ADR 0002 §7 要求「以国内邮箱实测送达率为唯一选型依据」——**本次没有做那个比较**，
是用户直接指定的。那份实测仍然欠着（B22），只是现在有了产生数据的手段：
`email_log` 从此按 `esp` 分组记录 `status` / `provider_msg_id` / `bounce_code`。

**接线形态**：走 Resend 的 **SMTP** 接口而不是它的 HTTP SDK ——
PR #26 的实现因此开箱即用，且「换一家 ESP 测送达率」仍然只是一次配置变更。

| 变量 | 值 |
|---|---|
| `BP_SMTP_HOST` | `smtp.resend.com` |
| `BP_SMTP_PORT` | `465`（隐式 TLS） |
| `BP_SMTP_USERNAME` | `resend`（Resend 的 SMTP 用户名是固定字符串） |
| `BP_SMTP_PASSWORD` | Secret Manager `bp-resend-api-key`（**即 Resend API key 本身**） |
| `BP_MAIL_FROM` | `babel.plus <no-reply@mail.babel.plus>` |
| `BP_MAIL_ESP` | `resend` |

**DNS（阿里云，三条，均已生效并验证通过）**：
`resend._domainkey.mail` TXT（DKIM）· `rsend.mail` CNAME · `send.mail` CNAME。
域名状态经 `POST /domains/{id}/verify` 后转 **verified**。

### 实测（观测，不是推断）

```
POST /api/v1/auth/email-code {scene:"register"}          204
Resend 侧 last_event                                      delivered
  from "babel.plus" <no-reply@mail.babel.plus>
  subject 【babel.plus】邮箱验证码
email_log 第 2 行  esp=resend  status=sent  provider_msg_id=有  sent_at=有
email_log 第 1 行  esp=unwired status=queued provider_msg_id=无 sent_at=无   ← 上线当天那封，对照组
verify-isolation.sh 前后                                  18 项通过 / 0 失败
```

两行的差别正是「接线前 vs 接线后」的全部形态差异。

⚠️ **切流量前先验候选版能起来**：`config.Load` 对这六项是**整组校验**，
半配会让容器**拒绝启动**——现象是新修订版起不来而不是发不出信。本次按两段式做了这一步。

---

## 4.3 · GTS → Let's Encrypt：B9 的剩余部分已解决（2026-09-01）

`web.` / `api.babel.plus` 的证书从 **Google 托管（GTS 签发）换成 Let's Encrypt**。
这不是洁癖：GTS 证书在中国触发 **IP 级单向丢包**（ADR 0004 §3.4，2026-08-21 实测），
失效形态是「慢」而不是「断」，在我们当前的可观测性水平上根本发现不了。
ADR 0016 §3.1 把它列为「域名裁决改不掉的硬约束」，roadmap **B9 的剩余部分**即此。

`admin.babel.plus` **刻意保留 Google 托管证书**：它走 IAP，管理员本来就要先过
`accounts.google.com`（ADR 0003 §2.1 基线 95% 异常），GTS 对它不构成额外损失，
而托管证书会自动续期 —— 少一处要维护的东西。

### 🔴 两个把路堵死、必须记下来的实测

**① GCS 硬拒 `.well-known/acme-challenge/` 这个对象路径。**

```
HTTPError 400: ACME HTTP challenges are not supported.
```

Google 主动封堵，防的是「谁都能拿桶托管一个挑战响应去劫持域名」。
所以「backend bucket + HTTP-01」这条最直觉的路**直接不通**。
处置：对象存 `acme/<token>`，由 URL map 的 `routeAction.urlRewrite.pathPrefixRewrite: /acme/`
把 `/.well-known/acme-challenge/` 前缀重写过去。**两边必须一起改** ——
只改一边的现象是 LE 拿到 404、报 `unauthorized`。

**② `google/cloud-sdk` 镜像在 arm64 上跑 amd64 模拟时，容器内的 gcloud 自身不可用。**
只读挂 `~/.config/gcloud` 报 `Read-only file system: credentials.db`；
复制成可写之后 gcloud 仍报诊断异常、上传静默失败。
而 certbot 又必须在容器里（本机没装）。
处置：**容器只跑 certbot，挑战上传由宿主机做**，两者靠共享目录接力
（容器写 `pending/<token>`，宿主机 watcher 轮询上传）。
⚠️ 附带一条老坑：本机 Docker 是 **colima**，bind mount **只在 `/Users/...` 下有效** ——
挂 `/private/tmp/...` 会得到**空目录**（不是报错，是静默为空），工作目录因此固定在 `$HOME` 下。

### 新增资源与脚本

| 资源 | 用途 |
|---|---|
| `bp-acme-challenge`（GCS）+ `bp-acme-bucket`（backend bucket） | 托管 ACME 挑战响应，对象路径 `acme/<token>` |
| `bp-acme-http-lb` + `bp-acme-http-proxy` + `bp-acme-http-fr`（80 端口） | HTTP-01 的入口；**非挑战路径一律 301 到 HTTPS** |
| `bp-public-le-20260901`（自管证书） | 替换掉 Google 托管的 `bp-public-cert` |
| `bp-acme-certbot-state`（Secret Manager） | certbot 账号状态，续期不必重新注册 |
| `infra/scripts/renew-le-cert.sh` | 续期脚本，三模式：`--check` / `--dry-run` / `--apply` |

### 实测

```
签发者  web./api.babel.plus   C=US, O=Let's Encrypt, CN=YE1     （到期 2026-11-29）
       admin.babel.plus      O=Google Trust Services, CN=WR3   （刻意保留）
仓库自己的 check-cert-issuer.sh --domains=web,api                2 项通过 / 0 失败
80 端口                       301 → https://web.babel.plus:443/
web / api / admin             200 / ok / 302（IAP 正常拦截）
verify-isolation.sh                                             16 项通过 / 0 失败
```

⚠️ **90 天后要续**（2026-11-29 到期）。忘了续的兜底见 §4.4 的 `bp-api-healthz-down`：
uptime check 会校验 TLS，证书过期即告警。

---

## 4.4 · 告警：从 0 条到 3 条（2026-09-01）

| 策略 | 抓什么 | 备注 |
|---|---|---|
| `bp-scheduler-task-failed` | `bp-*` 的 Scheduler 任一非 2xx，单次即告警 | 最坏那条 `expire-check` 停跑 = 到期用户继续免费上网，此前**完全静默** |
| `bp-api-healthz-down` | `api.babel.plus/-/healthz` 连续探测失败 | uptime check **校验 TLS**，所以它同时兜住「证书过期」 |
| `bp-cert-issuer-bad` | 签发者不再是 Let's Encrypt（P0） | 指标 `bp_cert_issuer_bad` 已按 `check-cert-issuer.sh` 的契约建好（**B42 三条建不了的指标解决一条**） |

🔴 **`bp-cert-issuer-bad` 目前收不到任何信号**，这一条必须写清楚：
它依赖 `check-cert-issuer.sh` 每日写日志，而**那个每日作业还没有挂**。
ADR 0014 §14 要求这类检测**带外**运行（「检测『我们的前置基础设施是否被替换』
不应依赖那套基础设施」），而我们没有带外机器 —— 这条仍然欠着。
**在它挂上之前，「签发者被换成 GTS」这类故障只能靠人跑 `renew-le-cert.sh --check` 发现。**
（`bp-api-healthz-down` 抓不到它：GTS 证书是受信任的，TLS 校验照样通过。）

⚠️ ADR 0014 批准并跑 `setup-alerts.sh --apply` 时，**须先删掉这三条手工策略**，
否则同一事件会告警两次。

---

### 管理面准入：当天稍晚已打通（含一条走了弯路的记录）

✅ **`https://admin.babel.plus` 已可登录并操作**，`/api/v1/admin/dashboard` 实测 **200 带真实数据**
（IAP 断言经 LB 传到 `bp-api`，`AuthenticateAdmin` 验签通过并查到 `admin_users` 的 owner 记录）。

🔴 **走了弯路的那一段值得逐条留下，它推翻了两个看起来合理的做法**：

1. **IAP 的 `oauth2ClientId` 为空时报 502 `Empty Google Account OAuth client ID(s)`**，
   而 `gcloud compute backend-services update --iap=enabled` **不会**挂上任何 OAuth 客户端。
2. **控制台里把 IAP 开关关掉再打开也不会**（本次实测，两种方式各试一次，客户端 id 始终为空）。
   ⚠️ 控制台 IAP 列表页此时显示 **Status: Ok**，与真实可用性不符 —— **别拿那一列当判据**。
3. **`gcloud alpha iap oauth-brands` 这条路已经不存在**：命令自己警告
   「IAP OAuth Admin APIs 于 2026-03-19 永久关停」，且对本项目直接报
   `INVALID_ARGUMENT: Project must belong to an organization`。

**真正可行的做法是手工建一个 Web 类型的 OAuth 客户端再显式配给 IAP**（四步）：

```bash
# ① 控制台 → Google Auth Platform → Clients → Create client → Web application（名字 bp-admin-iap）
#    创建后弹窗里的 client secret **只显示这一次**，立刻存好。
# ② 存进 Secret Manager（本次已存：bp-admin-iap-oauth-client-id / bp-admin-iap-oauth-secret）
# ③ 配给两个后端服务
CID=$(gcloud secrets versions access latest --secret=bp-admin-iap-oauth-client-id --project=oratis-491316)
CSEC=$(gcloud secrets versions access latest --secret=bp-admin-iap-oauth-secret   --project=oratis-491316)
for b in bp-admin-backend bp-api-admin-backend; do
  gcloud compute backend-services update "$b" --global --project=oratis-491316 \
    --iap="enabled,oauth2-client-id=${CID},oauth2-client-secret=${CSEC}"
done
# ④ 回控制台给该客户端加 Authorized redirect URI（缺它登录回调会失败）：
#    https://iap.googleapis.com/v1/oauth/clientIds/<CLIENT_ID>:handleRedirect
```

⚠️ **③ 之后到边缘真正生效之间有几分钟传播延迟**，期间仍报同一个 502 ——
不要因为「配完立刻还是 502」就回头改配置，先等一轮再判。

**登录路径实测**（Google 账号选择 → 同意页只请求 `email` 一项 → 落到 `/admin` 看板）。
边界同时复测：`api.babel.plus` 上的 admin 端点 **403**（那条路径不经 IAP，靠应用层 fail-closed），
经 LB 带伪造 `x-goog-iap-jwt-assertion` 头 **302**（IAP 在应用之前就拦掉了）。

---

## 4.5 · ADR 0014 批准落地 + 第二条通路（2026-09-02 追记）

> 证据目录 [evidence/adr0014-alerts-hy2-20260902](../evidence/adr0014-alerts-hy2-20260902/)，命令与原始输出都在那里。本节只给结论。

### 告警：从「3 条手工」到「12 条脚本接管」

用户 2026-09-02 批准 ADR 0014。当天 `setup-alerts.sh --apply` 建成 **12 条**（B1–B9 + 追加的 B11–B13），
2026-09-01 手工建的三条删除、由脚本接管；JSON 入库 `infra/alerts/`。**未建**：A1 / A3（带外，VPS 未采购）、
A2（email#2 与推送未建）、B10（指标不存在）。**通道仍只有 email#1**。**零演练。**
脚本在真实 GCP 上撞到四处（`$VAR（` 切词、过滤器不加引号会误建重复渠道、API 不支持 `COMPARISON_GE`、
uptime check 的 `check_id` 带随机后缀），全部修在脚本里。

### 日志指标：8 → 13

`bp_node_alive`（带 `node_id` 标签，B1 的信号源；node_id=1 的序列已存在 = armed）、`bp_mail_bounce`、
`bp_ratelimit_degraded`、`bp_cert_expiring_soon`、`bp_cert_check_failed`。

### 证书核对：每日作业挂上了（B57 前半、B58）

Cloud Run Job `bp-cert-issuer-check`（镜像 `bp-images/bp-cert-issuer-check:b6bd9ad`）+ Scheduler
`bp-cert-issuer-check-daily`（每日 04:40 CST）。首跑：`web.` / `api.` 都是 Let's Encrypt，2/2 通过。
到期前 14 天由 B13 告警。⚠️ 它是**带内**的，与 ADR 0014 §10.2 有意偏离；续签本身仍是人跑 `renew-le-cert.sh --apply`。

### `deploy.yml` 的前置：WIF 与两个仓库变量（B47 的前半）

`bp-github-pool` / `bp-github-oidc`（condition 限定 `oratis/babelplus`），`GCP_WIF_PROVIDER` / `GCP_DEPLOY_SA` 已设，
`bp-deploy-sa` 补了 `bp-api` 的服务级 `run.developer`。**工作流仍然一次都没跑过。**

### 阿里云 DNS：AK/SK 入 Secret Manager，脚本改 `dns_ali`（B60 的根因其实早已解掉）

`bp-aliyun-dns-ali-key` / `bp-aliyun-dns-ali-secret`。实查发现 `bp-node-hk1` 上 `hk1.babel.plus` 的 LE 证书
**2026-09-01 就已经用 `dns_ali` 签出来了**（到期 2026-11-30，acme.sh cron 已挂），只是 `setup-node.sh` 与文档还写着 `dns_cf`。

### Hysteria2：证书在盘上，离「能连」还隔着三条契约缺陷

三次加节点、三次撞坑、三次 `bp-api` 部署（`28991eb` → `7579163` → `a747ebf`）：
`protocol` 要写 `hysteria2` 不是 `hysteria`；`tls: 1` 必须显式下发；obfs 密码键名是 `obfs_password` 不是 Xboard 的 `obfs-password`。
每一条的失败形态都是 v2node **整个进程退出码 0**（同机 REALITY 陪葬，`Restart=on-failure` 不触发），第三条连日志都没有。
两次 REALITY 中断共约 6 分钟。**终态：HY2 从本机容器与原生客户端实测可用，出口 IP 正确，5 MB 3–4.8 MB/s。**

### 三态计时与 72 h 窗口

封禁 **38 s** · 配额耗尽 **17 s**（真实下载撞线，经 `/push`）· 到期 **3 min 51 s**（含等 expire-check 刻度 3 min 19 s）——
三条都在阈值内；恢复各 52–55 s。前提是先撞上 **B63**：节点上只剩一个用户时 v2node 把空列表当「没变化」，谁都踢不掉，
建了哨兵用户 `drill-sentinel@babel.plus` 才测得出来。**72 h 窗口起点 2026-09-02T07:05:22Z**。
P1 出口标准：**3.5/8 → 6/8**（剩：第 1 条路由判据重定、第 5 条 72 h 未到、第 7 条密钥轮换需在后台登录后做）。

## 4.6 · 官网 `bp-site` 上线（2026-09-04 追记）

> 用户当日指示：扩展与浏览器是服务主体，**做出官网并上线 babel.plus**，
> 订阅配置只作为会员中心里的服务、不在官网暴露。本节只记**已经存在**的东西。

### 建了什么

```
镜像   us-central1-docker.pkg.dev/oratis-491316/bp-images/bp-site:f6afde85441
       （infra/site/Dockerfile —— 🔴 与 bp-web/bp-admin 不同，这份**在仓库里**）
服务   bp-site（Cloud Run us-central1，nginx:1.29-alpine，SA bp-site-sa 零角色，max-instances=4）
接线   bp-site-neg（serverless NEG）→ bp-site-backend（EXTERNAL_MANAGED）
       → url-map bp-admin-lb 新增主机规则 ['babel.plus', 'www.babel.plus'] → site-paths
验收   Cloud Run 直连 200 / 8,046 B；`/-/healthz` 200
       经 LB 带 Host: babel.plus 与 www.babel.plus 均 **200**
       既有三个子域回归：web. 200 · api. `/-/healthz` 200 · admin. 302（IAP）· 证书仍是 LE
```

### 三个只有真做一次才会撞上的坑（都已修进脚本）

| # | 现象 | 根因 | 修法 |
|---|---|---|---|
| 1 | `gcloud builds submit --tag` 报 "Dockerfile required" | 该命令要求**上下文根目录**有名为 `Dockerfile` 的文件，而我们的在 `infra/site/` | 构建上下文里放一份同名副本 |
| 2 | `add-path-matcher` 报错，且**指向 url-map 里别的后端**（bp-admin-backend），看起来像别人的问题 | 新建的 `bp-site-backend` 默认是 `EXTERNAL`（经典 LB），而这套转发规则是 `EXTERNAL_MANAGED` | 建后端时显式 `--load-balancing-scheme=EXTERNAL_MANAGED`；已建错的删了重建 |
| 3 | 脚本报「url-map 已有主机规则」而实际没加 | 判断用了 `grep babel.plus` —— 既有的 `web./api./admin.babel.plus` 都含这个子串 | 改成逐条**精确**比对 |

另外**又一次撞上 `/healthz` 被 Google Frontend 拦截**（证据 [cloudrun-healthz-intercept-20260817](../evidence/cloudrun-healthz-intercept-20260817/)，2026-08-17 已记）：
容器本地 `/healthz` 返 200，经 Cloud Run 是 GFE 的 404。nginx 的健康路径因此改为 `/-/healthz`，与 `bp-api` 一致。
**同一个坑在 17 天里被踩了两次**，说明它值得写在新服务的模板里，而不是只留在证据目录。

### 🔴 还没上线的那一半：DNS 与证书

`babel.plus` 与 `www.babel.plus` 的 A 记录**当前指向 `76.76.21.21`（Vercel）**，
上面跑着一个**在线的、与本项目无关的产品**（`<title>Babel Plus | A LLM Game Platform</title>`，HTTP 200）。
把 apex 改指到本项目的负载均衡器**会让那个站点下线**，因此本次**没有改 DNS**，
等用户裁决（三个选项记在 roadmap 2.A 的对应条目）。

连带结果：**证书也没签**。`renew-le-cert.sh` 的 `DOMAINS` 已加上 apex 与 www（四个名字同一张证书），
但 http-01 要求域名先解析到负载均衡器 —— 顺序是 **DNS → 证书**，跳不过去。
在此之前站点只能经 `--resolve` 或 Cloud Run 直连访问。

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
- ~~**管理面进不去。**~~ ✅ **当天稍晚已解决**（§4.1 末节：GCLB + IAP + 手工建的 OAuth 客户端，
  `admin.babel.plus` 实测可登录、`/admin/dashboard` 200）。roadmap **B51 可关闭**。
- ~~**ESP 未接线**~~ ✅ **已接通并实测送达**（同日，见 §4.2）。
  **「真人无法自助注册」这条从此不成立** —— 验证码邮件真的会到收件箱。
- ~~**`bp-` 告警策略仍是 0 条。**~~ 🔶 **当天已建第一条**（`bp-scheduler-task-failed`，§4.1）——
  8 条 Scheduler 任一非 2xx 单次即告警，最坏那条（`expire-check` 停跑 = 到期用户继续免费上网）
  从此有信号。⚠️ 仍只有这一条：ADR 0014 未批准，`setup-alerts.sh --apply` 不许跑；
  批准后须**先删掉这条手工策略**再由脚本统一接管，否则同一事件会告警两次。
- ~~**roadmap B53 / B54 两条动钱的缺陷未修**~~ ✅ **已修并已上线**（PR #25，当天合入并随
  修订版 `bp-api-00009-7dn` 部署）。
- **`deploy.yml` 仍然从未运行过**（B47）。本次走的是本机脚本 + Cloud Build，
  没有可审计的 CI 部署记录。

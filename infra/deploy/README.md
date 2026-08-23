# 部署脚本 · 每次部署以隔离快照开始、以修订版回滚收尾

> 日期：2026-08-17 · 性质：**执行手册**（脚本的使用说明与偏差登记）
> 状态：**执行中**（2026-08-20 —— `oratis-491316` 上**已经有** `bp-api` / `bp-db` /
> `bp-api-sa` / 4 个 `bp-` secret，且线上参数与 `setup-infra.sh`、`deploy-api.sh` 逐项一致；
> 但本次核实只看到 GCP 上的结果状态、没有取到执行日志，**不断言资源就是这两个脚本建的**。
> `deploy-web.sh` / `rollback.sh` / `../node/` 侧仍无线上痕迹。见 §7 第 1 条与
> [docs/02-architecture/as-built-gcp.md §10](../../docs/02-architecture/as-built-gcp.md)）
> 事实基线：[docs/04-ops/deploy.md](../../docs/04-ops/deploy.md)（部署手册，935 行）、
> [docs/02-architecture/as-built-gcp.md](../../docs/02-architecture/as-built-gcp.md)（2026-08-16 `gcloud` 实测）、
> [docs/05-adr/0005-database-selection.md](../../docs/05-adr/0005-database-selection.md)（数据库参数）、
> [openapi/openapi.yaml](../../openapi/openapi.yaml)（`/internal/tasks/*` 的**契约事实源**）、
> [api/internal/config/config.go](../../api/internal/config/config.go)（**环境变量名的唯一事实源**）、
> [web/shared/src/lib/runtime-config.ts](../../web/shared/src/lib/runtime-config.ts)（**前端运行时配置的字段定义**）
> 证据口径：脚本逻辑 = 已在假 `gcloud` 下逐分支验证；`gcloud` 参数与字段路径 = **待核实**（未真实执行）
> 读者：值班运维。**要发布时看 §3；要知道脚本为什么与 deploy.md 不一样看 §4；出事时跳到 §3.4。**
> 关联：[../scripts/](../scripts/)（清点与隔离校验）、
> [docs/04-ops/monitoring.md](../../docs/04-ops/monitoring.md)（部署前必须先建好日志指标）

---

## 1 · 八个脚本，各管一件事

| 脚本 | 做什么 | 幂等 | 危险度 |
|---|---|---|---|
| [`setup-infra.sh`](setup-infra.sh) | 一次性资源：API、Artifact Registry、SA 与 IAM、Secret、Cloud SQL、Scheduler、Tasks、Pub/Sub | ✅ 反复跑无副作用 | 中（建 Cloud SQL = 持续支出，有二次确认） |
| [`deploy-api.sh`](deploy-api.sh) | 构建镜像 → 推 AR → `gcloud run deploy` | ⚠️ 同一 commit **重复构建**会因修订版重名失败（**特性不是 bug**）。`--promote` 到一个**已存在**的修订版则只切流量，不重建（2026-08-23 修，见 §3.2 注） | 中（默认 0% 流量；`--promote` 才切） |
| [`deploy-web.sh`](deploy-web.sh) | 两个 SPA 的静态发布：`web/user`→`bp-web`、`web/admin`→`bp-admin`，**独立主域名** | ✅ | 低（不碰 GCP） |
| [`rollback.sh`](rollback.sh) | Cloud Run 修订版流量切换 | ✅ | 中（写操作，有二次确认） |
| [`../scripts/inventory.sh`](../scripts/inventory.sh) | 把 as-built §7 的清点固化成可 diff 的快照 | ✅ | **零**（纯只读） |
| [`../scripts/verify-isolation.sh`](../scripts/verify-isolation.sh) | 确认现有服务未受影响；**有差异非零退出** | ✅ | **零**（纯只读） |
| [`../scripts/image-provenance.sh`](../scripts/image-provenance.sh) | 反查一个修订版跑的是哪个**完整 sha**，并判断该 commit 是否还被分支引用（B41，2026-08-23 加） | ✅ | **零**（纯只读；`--pull` 会往本机拉镜像） |
| [`../scripts/check-cert-issuer.sh`](../scripts/check-cert-issuer.sh) | 每日核对证书签发者，不符就写 `bp_cert_issuer_bad` 要的那条日志（B42，2026-08-23 加） | ✅ | **低**（只读 TLS 握手 + 写一条日志，**不建任何资源**） |

八个脚本共同遵守四条：

1. `set -euo pipefail`；每个都有 `--help`、`--dry-run`。
2. **开头校验 `PROJECT_ID` 必须是 `oratis-491316`**，且每个被创建/修改的资源名必须带 `bp-`（或 `bp_`）前缀。
   写别的项目直接拒绝运行 —— 本仓库的全部资产清点与 IAM 设计只对这一个项目成立。
3. 所有 `gcloud` 显式带 `--project`，**不依赖 `gcloud config set project`**。
   deploy.md §2 的原话：「`gcloud config set project` 打错项目是本文最现实的事故源」。
4. 危险操作要求**手工键入一个特定字符串**确认（不是 y/N —— y/N 是肌肉记忆，键入 `create-bp-db` 不是）。

> 守卫代码在八个脚本里各复制了一份，这是**故意的**：每个脚本都必须能单独 `scp` 出去跑，
> 且单独具备「打错项目就拒绝」的能力。抽成公共库会让守卫的存在取决于另一个文件有没有被一起拷走。
> 代价见 §6 第 1 条。

---

## 2 · 前置条件

| 需要 | 用在哪 | 没有会怎样 |
|---|---|---|
| `gcloud`（已登录且有项目权限） | 全部 GCP 脚本 | 直接报缺命令 |
| `jq` | `rollback.sh` / `inventory.sh` / `verify-isolation.sh` / `image-provenance.sh` | 直接报缺命令 |
| `openssl` | `setup-infra.sh` 生成密钥与密码；`check-cert-issuer.sh` 取证书 | 直接报缺命令 |
| `docker`（能跑 `--platform=linux/amd64`） | `deploy-api.sh` 构建 | 只能 `--no-build` 部署已有镜像 |
| `pnpm` | `deploy-web.sh` | 只能 `--dry-run` |
| `gcloud auth configure-docker us-central1-docker.pkg.dev` | 推镜像 | 推送 `denied`。**脚本不替你改本机 docker 配置**，只提示 |

环境变量（凭据**只从环境读**，脚本不写、不打印、不落盘任何 token）：

```
CLOUDFLARE_API_TOKEN   deploy-web.sh --target=cloudflare
NETLIFY_AUTH_TOKEN     deploy-web.sh --target=netlify
BP_WEB_DOMAINS         用户面板域名池，逗号分隔
BP_ADMIN_DOMAINS       后台域名池，逗号分隔（必须与上面**不共享可注册主域名**）
BP_API_DOMAINS         注入前端的 API 域名池
```

> `deploy-api.sh` 的 `docker build` 会显式清空 `HTTP_PROXY` / `HTTPS_PROXY` 等 build-arg。
> 这不是洁癖：Docker Desktop 往构建容器注入的代理端口可能与宿主机实际端口不一致，
> 不清空会让 `go mod download` 在构建容器里失败。`api/Dockerfile` 把它们声明成 `ARG` 就是为了这个。

---

## 3 · 顺序

### 3.1 首次搭建（从零到 `bp-api` 上线）

```bash
# 0. 部署前基线 —— 这一步不允许跳过
./infra/scripts/verify-isolation.sh --out=snapshots/before

# 1. 先看清楚要做什么（不发任何写操作）
./infra/deploy/setup-infra.sh --dry-run

# 2. 建一次性资源（apis → registry → iam → secrets → sql → pubsub → tasks）
./infra/deploy/setup-infra.sh

# 3. ⚠️ 在这里插一步：monitoring.md §3 的 log-based metrics 必须**现在**建好。
#    自定义日志指标**不追溯** —— 事后补建拿不到首次部署当天的数据。
#    这一步本目录没有脚本，见 §7 第 3 条。

# 4. 首次部署（默认起不接流量的候选版）
./infra/deploy/deploy-api.sh
curl -sS https://candidate---<服务主机名>/healthz     # 验证候选
./infra/deploy/deploy-api.sh --no-build --tag=<sha> --promote

# 5. 补上依赖 bp-api 存在的部分（Scheduler / Pub-Sub push 订阅 / 服务级 IAM）
./infra/deploy/setup-infra.sh --step=postdeploy

# 6. 部署后核对
./infra/scripts/verify-isolation.sh --baseline=snapshots/before
```

### 3.2 日常发布 `bp-api`

```bash
./infra/scripts/verify-isolation.sh --out=snapshots/before
./infra/deploy/deploy-api.sh                              # 0% 流量
curl -sS https://candidate---<服务主机名>/healthz
./infra/deploy/deploy-api.sh --no-build --tag=<sha> --promote
./infra/scripts/verify-isolation.sh --baseline=snapshots/before
```

🔴 **不做灰度。** 节点每 60 秒轮询一次，10% 的流量切分意味着同一个节点在相邻两次轮询里
可能拿到两个不同版本的响应；若这次发布改了 UniProxy 响应体或 ETag 计算方式，
灰度会造成节点反复失效缓存。所以两个脚本都**只提供 0% 与 100%**。

> **2026-08-23 修的两件（都是上面这四行命令自己踩的）：**
>
> 1. **第 4 步以前会撞上 §1 表里那条「修订版重名失败」。** 第 2 步已经用
>    `--revision-suffix=<sha>` 把 `bp-api-<sha>` 建出来了，第 4 步再发一次
>    `gcloud run deploy` 会带着同一个 suffix 去建同名修订版。现在 `--promote` 先做一次
>    只读的 `gcloud run revisions describe`：**修订版已存在就只 `update-traffic` 切流量**
>    （与 [`rollback.sh`](rollback.sh) 和 CI 的 `--to-tags=candidate=100` 同一形态），
>    不存在才走 `run deploy`。**待核实**：未在真实 `gcloud` 上跑过。
> 2. **「工作树脏时拒绝」以前也拦 `--no-build`。** 第 4 步不构建任何东西，工作区脏不脏
>    与将被部署的那个镜像的内容无关；而值班在做第 4 步时工作区**通常**是不干净的
>    （正在改、正在查）。现在这道门只在真的要构建时才关。
>    连带修掉的还有：脏树 + `--no-build` 时脚本会自作主张把 tag 换成 `<短sha>-dirty`，
>    去指一个多半根本不存在的镜像 —— 即脚本自己建议的那条出路会把人送进 image not found。

### 3.3 发布前端

```bash
BP_WEB_DOMAINS=... BP_ADMIN_DOMAINS=... BP_API_DOMAINS=... \
  ./infra/deploy/deploy-web.sh --app=web --target=all

BP_WEB_DOMAINS=... BP_ADMIN_DOMAINS=... BP_API_DOMAINS=... \
  ./infra/deploy/deploy-web.sh --app=admin --target=cloudflare   # 会要求键入 publish-admin
```

一次构建，发布到池内**全部**镜像 —— 页脚要列全所有镜像域名，
所以新增一个域名要重新发布同池全部镜像（deploy.md §11.3 第 7 步）。

**域名池不烧进 bundle。** 脚本在构建之后生成 `dist/runtime-config.js`
（字段按 [`web/shared/src/lib/runtime-config.ts`](../../web/shared/src/lib/runtime-config.ts)），
并往 `dist/_headers` 追加 `/runtime-config.js → Cache-Control: no-cache`。
于是**只改域名池时不需要重新构建**，重跑一次脚本即可 —— 这正是 ADR 0003 §5
「一键新增镜像域名」在恢复速度上要的东西。
（源码里的 `web/user/public/runtime-config.js` 是**故意留空的模板**，脚本不改它。）

发布后脚本会逐域名校验证书签发者：**签发者含 `Google Trust Services` 直接失败**
（ADR 0004 §3.4：GTS 证书在中国触发 IP 级单向丢包，现象酷似网络抖动）。

> ⚠️ GitHub Pages **不读 `_headers`** —— 那一侧没有 `runtime-config.js` 不缓存的等价物。
> 用它当镜像时，改域名池的生效时间取决于用户浏览器的缓存，这是它比另外两家差的一点。

### 3.4 出事了

```bash
./infra/deploy/rollback.sh --list
./infra/deploy/rollback.sh --to=bp-api-<旧sha>
./infra/scripts/verify-isolation.sh --baseline=snapshots/before
```

🔴 **代码秒级可回滚，schema 不能。** 一次发布只做 expand（加列/加表/加可空字段），
contract（删列/改类型）放下一次 —— 这是「旧代码能在新 schema 上跑」的唯一保证。

### 3.5 线上跑的到底是哪份源码（roadmap B41 的处置，2026-08-23）

**问题不是假想的，它已经发生过一次。** 生产 `bp-api-2fbf49d` 的 tag 取自
`git rev-parse --short=7 HEAD`，而那个 commit（`2fbf49d3d2b6…`）**不被任何分支引用** ——
`pr7/p1-core-and-deploy` 被 force-push 改写，它成了孤儿。
于是「线上跑的是哪份源码」只能靠去代码托管方的对象库按完整 sha 捞才答得出来
（[evidence/gcp-inventory-20260821 §5.2](../../docs/evidence/gcp-inventory-20260821/)）。
**答不出来 = 无法回滚到已知 good，也无法审计。**

处置分三层，`deploy-api.sh` 与 `image-provenance.sh` 各占一半：

| 层 | 放什么 | 谁写 | 谁读 |
|---|---|---|---|
| 镜像 label（**权威**） | `org.opencontainers.image.revision`（完整 40 位 sha）·`.version`（tag）·`.created`（构建时间）·`plus.babel.git.branch`·`plus.babel.git.dirty`·`plus.babel.build.by` | `deploy-api.sh` 的两条构建路径**同源**（`label_pairs`），加 `.github/workflows/deploy.yml` 这**第三条**（`plus.babel.build.by=github-actions`） | `docker inspect` / `image-provenance.sh` |
| 修订版 label（快捷） | `bp-git-sha` = 完整 sha、`bp-git-dirty` | `gcloud run deploy --update-labels` | 一条 `gcloud run revisions describe` |
| 镜像 tag | 短 sha（可能带 `-dirty`） | 同上 | 人眼。**它不再是唯一线索** |

**一条命令查出线上跑的完整 sha：**

```bash
./infra/scripts/image-provenance.sh                       # 当前接 100% 流量的修订版
./infra/scripts/image-provenance.sh --revision=bp-api-2fbf49d
./infra/scripts/image-provenance.sh --image=<镜像引用> --pull   # 绕开 Cloud Run，拉镜像读 label
```

它会顺带回答**那个 commit 现在还在不在**：本地仓库里找不到、或者找得到但**不被任何分支引用**，
都会红着退出（退出码 1）并给出处置 —— 先 `git tag deployed/<短sha> <完整sha>` 把它钉住，
别让 GC 收走，再去查是哪个分支被 force-push 了。

不带任何 label 的老镜像（2026-08-23 之前构建的全部镜像）在它这里会明确报「答不出来」，
**这正是 B41 的原始状态本身**，不是脚本坏了。

> ⚠️ 三条构建路径写同一组 label key，而这三处**没有任何机制保证同步**（与 §6 第 1 条同一类债）：
> `deploy-api.sh` 的 `LABEL_*` 常量、它生成的 Cloud Build 配置模板（这两处之间有断言互锁）、
> 以及 `.github/workflows/deploy.yml` 里那六行 `--label`（**这一处没有互锁**）。
> 读的那一侧 `image-provenance.sh` 还有第四份。改 key 要改四处。

> ⚠️ **工作树脏时默认拒绝构建**，`--allow-dirty` 才放行。
> **这道门只在真的要构建时才关** —— `--no-build`（两段式发布的第 4 步、回补一次失败的
> 部署）不构建任何东西，不受它约束；那条路径上的 `bp-git-dirty` 从 tag 反推
> （`<短sha>` = 干净构建，`<短sha>-dirty` = 脏构建，其它 tag 一律不写 label）。
> 理由：label 只能记下 HEAD 的 sha，而真正被构建进镜像的是**工作区** ——
> 两者不一致时 label 会变成一句**可信但错误**的话，而这比没有 label 更糟：
> 没有 label 时人会去查，有一个错的 label 时人会直接信。
> 放行时 tag 会变成 `<短sha>-dirty`（不换 tag 的话，这次构建会把同一短 sha 下那份
> 干净镜像顶掉，被顶掉的那份就只剩 digest 可寻 —— 又回到「答不出来」）。

### 3.6 每日证书签发者核对（roadmap B42 的一条，2026-08-23）

```bash
# 平时（域名还没注册，清单为空 → 打提示后以 0 退出）
./infra/scripts/check-cert-issuer.sh --dry-run

# 域名接进来之后（定时作业里这么调）
BP_WEB_DOMAINS=... BP_ADMIN_DOMAINS=... BP_API_DOMAINS=... \
  ./infra/scripts/check-cert-issuer.sh --require-targets
```

判定按 [monitoring.md §8](../../docs/04-ops/monitoring.md)：**只校验 issuer 的 `O`，不校验 `CN`**。
不符时写一条结构化日志到 `logName=projects/oratis-491316/logs/bp-cert-issuer-check`，
那是 log-based metric `bp_cert_issuer_bad`（告警第 15 条，**P0**）的**唯一**信号源。

> ⚠️ `*.a.run.app` 的签发者**本来就是 GTS**（2026-08-21 实测），所以 run.app 主机名
> **不属于**目标清单 —— 放进去只会得到一条永远为红的告警，而长期为红的告警等于没有告警。
> 目标清单只放三套域名池里我们自己钉了 LE 的域名。

---

## 4 · 与 `deploy.md` 的已知差异（**每一条都要回写文档**）

deploy.md 写于 2026-08-16，早于 `openapi/openapi.yaml` 与 `api/` 骨架。凡冲突，
**契约与代码优先**（它们是能被 CI 卡住的事实，文档不是）。差异共八条：

| # | deploy.md 怎么写的 | 脚本怎么做的 | 为什么 |
|---|---|---|---|
| 1 | Scheduler 打 `/internal/tasks/expire-sweep`，10 分钟 | `expire-check`，**5 分钟** | 契约里没有 `expire-sweep` 这个路径；频率写在契约的 summary 里 |
| 2 | `/internal/tasks/rollup?grain=day` | `stat-rollup`，**契约里没有 `grain` 参数** | 建了 hourly + daily 两条任务打同一路径 |
| 3 | 六条定时任务 | **八条**（多了 `chain-scan`、`remind-sweep`） | 契约里这两个也标了 Scheduler 驱动 |
| 4 | 环境变量 `DB_HOST` / `DB_NAME` / `DB_USER` + secret `DB_PASSWORD` | **整串 DSN 进 secret `bp-database-url`** → `BP_DATABASE_URL` | `config.go` 只认 `BP_DATABASE_URL` 一个连接串；拆成四项要改代码 |
| 5 | 项目级 `roles/artifactregistry.writer` / `roles/run.developer` / `roles/cloudtasks.enqueuer` | **仓库级 / 服务级 / 队列级** | 与 deploy.md §1 第 2 条（逐 secret 授权）完全同构：项目级会一并覆盖 `cloud-run-source-deploy` 与现有三个 Cloud Run 服务 |
| 6 | 7 个 secret | **只建 4 个** | `bp-mail-api-key` / `-backup` / `bp-payment-webhook-secret` 的 ESP 与支付商都未选型，建占位值会让「secret 存在」失去信号价值 |
| 7 | 一个队列 `bp-traffic-ingest` | **两个**（多了 `bp-mail-send`） | 契约里 `mail-send` 也是 Cloud Tasks 驱动 |
| 8 | 镜像 `FROM gcr.io/distroless/static-debian12`，build-arg `GIT_SHA` | `api/Dockerfile` 用 `FROM scratch`，build-arg `VERSION` | 这台开发机拉不到 `gcr.io`（TLS 握手超时）；`scratch` 同时把镜像做到 16.4 MB |

另有两条**不是本目录能改的冲突**，登记在这里等人订正：

- `api/.env.example` 的注释里写实例名 `bp-pg`、库名 `babelplus`、用户 `bp-api`；
  而 deploy.md §3.4 与 ADR 0005 §10.2 写的是 `bp-db` / `bp` / `bp_app`。
  **脚本按后者做**（它们是裁决，`.env.example` 的那行是注释）。**待核实**并订正 `.env.example`。
- deploy.md §8.1 把 OIDC audience 写成硬编码的 `https://bp-api-2360090741.us-central1.run.app`；
  脚本改为运行时 `gcloud run services describe` 取。结论相同，来源不同 —— 不写死才不会在
  项目号变化时静默失配。

---

## 5 · 凭据

| 凭据 | 在哪生成 | 存在哪 | 会不会落盘 |
|---|---|---|---|
| `bp_app` 的密码 | `setup-infra.sh` 里 `openssl rand -hex 24` | 只作为 DSN 的一部分进 `bp-database-url` | **否** |
| 三个 pepper / 签名密钥 | `openssl rand -base64 48` | 逐个 Secret Manager 条目 | **否**（`openssl` 直接管道进 `--data-file=-`） |
| CF / Netlify token | 运维自己准备 | 环境变量 | **否**（脚本只判断非空，不打印） |

三条设计约束：

1. **逐 secret 授权，绝不在项目级授 `roles/secretmanager.secretAccessor`。**
   项目级会把 `anthropic-api-key` 与 `relay-token`（属于**现有服务**）一并交给 `bp-api-sa`。
2. **`--dry-run` 的输出会遮蔽 `--password=`。** 预演输出会进终端回滚缓冲、会被贴进工单、
   会被 CI 记进日志 —— 一个「只是预演」的命令把明文密码写进日志比真的执行还糟，因为没人会去清理预演输出。
3. 🔴 **`bp-sub-token-pepper` 与 `bp-node-token-pepper` 不可轮换**（轮换 = 全体订阅 / 全体节点密钥失效）。
   `setup-infra.sh` 对已存在的 secret **一律跳过不覆盖**，就是为了让「顺手重跑一次初始化脚本」
   不会变成一次全员失效事故。

---

## 6 · 代价

> ⚠️ 这一选择的代价，必须留在记录里而不是被措辞掩盖：
>
> 1. **守卫代码在八个脚本里各复制了一份，会漂移。** 换来的是每个脚本可以单独拷出去执行
>    且单独具备「打错项目就拒绝」的能力。哪天要改守卫，就是改六处 —— 且没有任何机制提醒你改全。
> 2. **完全没有 IaC。** 这些脚本是命令式的：没有状态文件、没有 drift 检测、
>    没有「谁在什么时候改了什么基础设施」的 code review 入口。
>    `setup-infra.sh` 的幂等靠「先探测再创建」，它能防重复创建，**防不了手工在控制台改过之后的漂移**。
>    这是 P1 阶段主动接受的技术债，理由与 deploy.md §15 第 1 条相同（Cloudflare 侧资产未清点完，
>    导入一份不完整的 state 比没有 state 更危险）。
> 3. **`verify-isolation.sh` 是事后发现，不是事前阻止。** 一条打错名字的
>    `gcloud compute firewall-rules delete`，它能告诉你出事了，**但拦不住你**。
>    真正的机制隔离要独立 GCP 项目 + 共享 VPC，那是另一次裁决（as-built §8）。
> 4. **as-built 的实测值被写死在 `verify-isolation.sh` 里。** 好处是第一次跑就有判定力，
>    不需要「先有 before 快照」这个前提；代价是现实合法变化时（比如某天真的要给 `vpn-jp` 换 IP）
>    要同时改脚本常量与 as-built 文档，而**没有任何机制保证两处同步**。
> 5. **`deploy-web.sh` 的「两池不共享主域名」检查用「域名最后两段」近似可注册主域名。**
>    对 `.co.uk` / `.com.cn` 这类二级后缀会**误判成相同**。这是刻意选的偏保守方向
>    （宁可误报也不漏报），但它意味着用这类后缀时脚本会挡住合法的域名组合。
> 6. **`rollback.sh --previous` 的推断口径是「最近创建的、当前不接流量的修订版」**，
>    这不等于「上一个曾经接过流量的修订版」。中间存在候选修订版时它会挑到候选版 ——
>    所以脚本把推断结果打出来并仍然要求手工键入修订版名确认。**应急时最容易被跳过的恰恰是这一步。**
> 7. **`bp_app` 的密码经 `--password=` 传给 gcloud，在那几秒内对同机其它进程的 `ps` 可见。**
>    gcloud 没有提供从 stdin 读密码的非交互形式（`--prompt-for-password` 是交互式的，
>    用管道喂它的行为 **待核实**）。缓解只有「只在运维自己的机器上跑」。它不落盘、不进 shell history。
> 8. **`--max-instances=8` 被写死在 `deploy-api.sh` 里，而它属于数据库不属于 Cloud Run。**
>    改它必须同时改 `BP_DB_MAX_CONNS` 并重算 ADR 0005 §6.2 的公式。
>    脚本用注释挡住了随手改，但**没有机制**能阻止两个值被改成不匹配的一对。

## 7 · 这次没有解决的

- [ ] **`deploy-web.sh` / `rollback.sh` 仍未在 `oratis-491316` 上真实执行过**，
      它们的 `gcloud` 参数名、子命令名与 `--format=json` 字段路径仍全部 **待核实**。
      `setup-infra.sh` / `deploy-api.sh` 这一侧则有了线上结果可对照：2026-08-20 复核，
      `bp-api` 的 `--set-env-vars` / `--set-secrets` / `--max-instances=8` / `--cpu-boost` /
      运行时 SA / Cloud SQL 连接**与脚本逐项一致**，4 个 secret 名也与 `setup-infra.sh` 一致
      （[docs/02-architecture/as-built-gcp.md §10](../../docs/02-architecture/as-built-gcp.md)）——
      但**只看到结果状态、没有执行日志**，因此不断言资源就是脚本建的，本文状态记为「执行中」而不是「As-Built」。
- [ ] 🔴 **线上 `bp-api` 带着一个仓库里根本不存在的环境变量 `BP_ALLOWED_ORIGINS`**
      （值 `https://web.babel.plus,https://admin.babel.plus`，2026-08-20 核实）。
      `api/internal/config/config.go`、本目录的 `deploy-api.sh`、`.github/workflows/deploy.yml`
      三处都没有它，`api/` 下也没有任何 CORS 中间件会读它。
      而 `--set-env-vars` 是**全量替换**语义（`deploy-api.sh` 里那条注释与 deploy.md §7 末尾都写明了）——
      **照现在的 `deploy-api.sh` 再部署一次，会静默删掉线上这一项。**
      重跑之前必须先定它的去留：要么补进 `config.go` 与脚本，要么确认无用后从线上删掉。
- [ ] **Cloud Run Job `bp-migrate` 没有脚本。** migration 怎么在生产上跑仍未定
      （deploy.md §15：迁移工具 `golang-migrate` / `atlas` / `tern` 未选）。
      `api/db/migrations/` 有 12 组可回滚的 migration，但目前只有 `make migrate-up` 这条本地路径。
- [ ] 🔴 **Cloud Run Job `bp-db-dump` 没有脚本，所以每周跨区 `pg_dump` 现在没有生效。**
      ADR 0005 §11 第 5 条明说：它是「一次打错名字的 `gcloud sql instances delete`」这条风险的**唯一对冲**。
      `setup-infra.sh --step=postdeploy` 会检测该 Job 是否存在，不存在就跳过并告警。
- [ ] **monitoring.md §3 的 log-based metrics、§4 的通知渠道、§5 的 17 条告警策略都没有脚本。**
      而 §3.1 要求它们必须在 `bp-api` 首次部署**之前**建好（日志指标不追溯）。
      这是目前部署流程里最大的一处「文档要求存在、可执行形式不存在」。
      2026-08-23 补上的只是其中**一条指标的信号源**（`check-cert-issuer.sh` →
      `bp_cert_issuer_bad`）：**指标本身仍要人工建一次**（命令在该脚本 `--help` 末尾），
      而且把这个脚本挂成每日作业的那一步（Cloud Scheduler / cron / CI 定时任务，三选一）
      **也还没有裁决、没有脚本**。现在它只是一条能手工跑的命令。
- [ ] **`/internal/tasks/alert-relay` 不在 `openapi/openapi.yaml` 里**（契约只到 `remind-sweep`），
      但 `setup-infra.sh` 会为它建 Pub/Sub push 订阅。订阅建好后会持续 404 直到中继实现。
- [ ] **Artifact Registry 的清理策略没配。** `set-cleanup-policies` 的子命令名与 JSON schema
      在 gcloud 版本间变动过（deploy.md §3.2 标 待核实），且它是唯一能**删镜像**的配置项，
      必须人工带 `--dry-run` 看清楚再落。
- [ ] **`deploy-web.sh` 从没在真实的 `web/` 上跑过一次完整构建。**
      它按 `web/user` + `web/admin` + `web/shared` 的 pnpm workspace 布局写，
      `--dry-run` 验证过命令与生成的 `runtime-config.js` 形状，但 `pnpm --filter … build`
      本身**未实测**（前端仍在开发中）。
- [ ] **`bp-docs`（教程站）完全没有脚本** —— 而 page-inventory 把它定为整个自助排障体系的单点，
      且它必须在用户连不上代理时可达。第三套域名池、第三份发布路径，一样都还没有。
- [ ] **域名一个都还没注册**，所以证书签发者校验**仍然跑不了** —— 2026-08-23 之后
      「可执行形式」有了两个（`deploy-web.sh` 发布后的即时确认、`check-cert-issuer.sh`
      的每日核对），但两个都需要一个真实存在的域名才能产生判定。
      在第一个域名接入之前，「钉 Let's Encrypt」这条承诺**依然没有任何东西在生效**，
      `bp_cert_issuer_bad` 也不会有任何信号。**接入第一个域名时必须回来把它填进目标清单。**
- [ ] **Cloud Build 触发器没有脚本**（deploy.md §4.2 路径 B），因为代码仓库托管在哪未定。
      现在只有本地构建这一条路，意味着**发布能力绑在某一台开发机上**。
- [ ] **没有自动化冒烟测试，没有基于错误率的自动回滚。**
      `deploy-api.sh` 的两段式发布把「验证候选」这一步留给了人和一条 `curl`。
- [ ] **`verify-isolation.sh` 覆盖不到 Cloudflare 侧。** as-built §9 记录 CF 的 Tunnel / DNS zone /
      Workers 至今未清点 —— 「不影响已部署服务」这条承诺在 CF 那一侧目前**没有任何可执行形式**。

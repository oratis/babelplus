# 本地开发环境（无需在本机装 Go）

> 日期：2026-08-16（2026-08-17 补 web 侧并全量复跑） · 性质：**执行手册**
> 状态：**As-Built**（2026-08-17 端到端复验通过）
> 事实基线：本文每条命令都在这台开发机上实际跑通过，不是照着文档抄的
> 读者：接手 `api/` 或 `web/` 的开发者。第一次拉仓库时从 §1 开始。
> 关联：[deploy.md](deploy.md)、[ADR 0006](../05-adr/0006-api-stack.md)、
> [`web/README.md`](../../web/README.md)、[`.github/workflows/ci.yml`](../../.github/workflows/ci.yml)

---

## 1 · 前提

**`api/` 侧只需要 Docker。不需要在本机装 Go、sqlc、oapi-codegen、psql** —— 全部走容器。

**`web/` 侧反过来：不走容器，直接用宿主机的 Node + pnpm。**
原因是 §2.2 那个代理坑只影响容器，宿主机的网络是好的；
把前端也塞进容器只会白白继承一个已知的坑，还多一层文件监听的延迟。

已验证的版本（2026-08-17 实测）：

| 工具 | 版本 | 谁用 |
|---|---|---|
| Docker | 28.1.1 | `api/`、`infra/` 的 shellcheck、本地 Postgres |
| Node | v24.12 | `web/` |
| pnpm | 10.33.0 | `web/`（`web/package.json` 里 `packageManager` 钉的就是这个） |

> CI 用的是 Node 24 / pnpm 10（`ci.yml` 的 `NODE_VERSION` / `PNPM_VERSION`），与上表一致。

```bash
cd api && make help    # 列出所有可用目标
cd web && cat package.json   # web 侧没有 Makefile，脚本都在 package.json
```

---

## 2 · 🔴 两个环境坑（不先处理会卡住）

这两条是在这台机器上实测踩到的。换机器不一定复现，但症状很有迷惑性，先记下来。

### 2.1 Docker Hub 拉不到 golang 镜像

症状：

```
failed to authorize: failed to fetch anonymous token:
Get "https://auth.docker.io/token?...": EOF
```

绕过（用 Google 的 Docker Hub 镜像）：

```bash
docker pull mirror.gcr.io/library/golang:1.25-alpine
docker tag  mirror.gcr.io/library/golang:1.25-alpine golang:1.25-alpine
```

> `gcr.io` 本身从这台机器**不可达**（TLS 握手超时），所以 `api/Dockerfile`
> 的运行阶段用 `FROM scratch` 而不是 distroless —— 见 Dockerfile 里的说明。

**⚠️ 重打 tag 只救得了 `docker run`，救不了 `docker build`（2026-08-17 实测）。**
`docker run golang:1.25-alpine …` 命中本地缓存直接跑；但 `docker build` 解析
`FROM golang:1.25-alpine` 时仍会去 registry 核对 digest，于是卡在

```
#5 [build 1/6] FROM docker.io/library/golang:1.25-alpine@sha256:3eb6c2b3…
#5 resolve docker.io/library/golang:1.25-alpine@sha256:3eb6c2b3…
```

一直不动（`DOCKER_BUILDKIT=0` 退回旧 builder 也一样）。
本机要验证 `api/Dockerfile` 能构建，把 `FROM` 临时指到镜像源：

```bash
sed 's|^FROM golang:1.25-alpine AS build|FROM mirror.gcr.io/library/golang:1.25-alpine AS build|' \
  api/Dockerfile > /tmp/Dockerfile.mirror
docker build -f /tmp/Dockerfile.mirror \
  --build-arg HTTP_PROXY= --build-arg HTTPS_PROXY= \
  --build-arg http_proxy= --build-arg https_proxy= \
  --build-arg GOPROXY='https://goproxy.cn,direct' \
  --build-arg VERSION=local -t bp-api:local api/
```

**不要把这个 `sed` 的结果提交进仓库** —— 它是本机网络的临时绕过，
`api/Dockerfile` 里必须保持 `FROM golang:1.25-alpine`（CI 的 runner 直连 Docker Hub，没有这个问题）。

### 2.2 Docker Desktop 往容器注入的代理端口是错的

症状：容器内 `go mod download` 失败，报

```
proxyconnect tcp: dial tcp 192.168.65.254:7890: connect: connection refused
```

原因：Docker Desktop 按自己的设置注入 `HTTP_PROXY`，而宿主机实际代理在**另一个端口**。

解法：**清空容器内的代理变量**，容器可以直连。`Makefile` 与
`make docker-build` 都已经这么做了，直接用 make 目标即可，不要自己敲 `docker run`。

---

## 3 · 常用流程

### 3.1 编译与测试

```bash
cd api
make build        # 编译
make test         # 单测
make check        # fmt-check + vet + build + test，CI 跑的就是这个
```

### 3.2 起本地数据库并灌表

```bash
make pg-up          # postgres:17，映射到 127.0.0.1:55433
make migrate-up     # 依序执行全部 up migration
make pg-down        # 用完拆掉
```

一键验证 migration 可正向可回滚（起库 → 灌 up → 跑 down → 确认归零 → 拆库）：

```bash
make migrate-verify
```

实测结果：**43 张表、118 个索引、4 个视图；回滚后残留 0 张表。**

### 3.3 改了规格或 SQL 之后

**改了 `openapi/openapi.yaml`，两侧都要重新生成 —— 只跑 `make generate` 会漏掉前端。**

```bash
cd api && make generate     # = gen-api + gen-db + gen-stubs + fmt
cd web && pnpm run gen:api  # openapi-typescript → shared/api/schema.d.ts
```

**五处生成物都要提交进仓库**，CI 的 `contract-drift` 作业逐一比对
（重新生成后有任何差异就红）：

| 生成物 | 来源 | 工具 | 重新生成 |
|---|---|---|---|
| `api/internal/gen/api.gen.go` | `openapi/openapi.yaml` | oapi-codegen v2.4.1 | `cd api && make gen-api` |
| `api/db/gen/*.go` | `api/db/migrations/` + `api/db/queries/` | sqlc 1.31.1 | `cd api && make gen-db` |
| `api/internal/handler/unimplemented.gen.go` | 上面生成的接口 | `api/scripts/gen_stubs.py` | `cd api && make gen-stubs` |
| `api/internal/handler/operations.txt` | 同上（同一次生成） | `api/scripts/gen_stubs.py` | 同上 |
| `web/shared/api/schema.d.ts` | `openapi/openapi.yaml` | openapi-typescript 7.13.0 | `cd web && pnpm run gen:api` |

> **`make generate` 末尾那步 `fmt` 不是可有可无的。**
> `gen_stubs.py` 的原始输出比提交版本多一行空行，靠 `gofmt -w .` 收掉。
> 手动分步跑生成器而漏掉 `fmt`，会看到一行凭空冒出来的假漂移。
>
> `web` 侧的 `pnpm run gen:api:check` 是「生成 + `git diff --exit-code`」的合体，
> 提交前想自查一下用它。**幂等性已实测**：连跑两次 `schema.d.ts` 的 sha256 不变。

### 3.4 端到端冒烟

```bash
docker network create bp-smoke
docker run --rm -d --name bp-dev-pg --network bp-smoke \
  -e POSTGRES_PASSWORD=devpass -e POSTGRES_DB=babelplus postgres:17
# 等就绪后灌 migration（见 §3.2），然后：
cd api && make docker-build
docker run --rm -d --name bp-api-smoke --network bp-smoke -p 18080:8080 \
  -e BP_ENV=dev \
  -e BP_DATABASE_URL='postgres://postgres:devpass@bp-dev-pg:5432/babelplus?sslmode=disable' \
  -e BP_SUBSCRIPTION_TOKEN_PEPPER=dev-only-pepper-sub-00000000 \
  -e BP_NODE_KEY_PEPPER=dev-only-pepper-node-0000000 \
  -e BP_SESSION_SIGNING_KEY=dev-only-session-key-000000 \
  -e BP_GCP_PROJECT_ID=oratis-491316 \
  bp-api:dev
```

实测应得到的结果（**这是回归基线，改动后应保持一致**）：

| 请求 | 期望 |
|---|---|
| `GET /healthz` | `200` `ok` |
| `GET /api/v1/plans`（未实现） | `501` `NOT_IMPLEMENTED` |
| `GET UniProxy/config` 无密钥 | `401` `NODE_KEY_MISSING` |
| `GET UniProxy/config` 错密钥 | `401` `NODE_KEY_INVALID` |
| `GET UniProxy/config` 短密钥 | `401`（**不查库**直接拒） |
| 不带任何环境变量启动 | 非 0 退出，一次性列出全部 6 个缺失变量 |

> **501 与 500 的区分是刻意的**：脚手架阶段有 120+ 个端点未实现，
> 若它们都算 500，5xx 告警会长期为红，真正的故障反而被淹没。
> [monitoring.md](monitoring.md) 的告警规则按「5xx 排除 501」建。

### 3.5 web 侧：起前端

`web/` 是一个 pnpm workspace，三个包：`shared`（共享客户端与组件）、
`user`（用户面板 → `bp-web`）、`admin`（后台 → `bp-admin`）。
**`user` 与 `admin` 的构建产物完全分开**，各自独立域名池，这是设计约束不是巧合。

```bash
cd web
pnpm install          # 首次；CI 用的是 pnpm install --frozen-lockfile
pnpm dev:user         # 用户面板 → http://localhost:5173
pnpm dev:admin        # 后台     → http://localhost:5174
```

两个端口都写死在各自的 `vite.config.ts` 里（不是自动分配），可以同时起。

提交前跑一遍 CI 会跑的四件事：

```bash
cd web
pnpm -r build                       # tsc --noEmit + vite build（三个包）
pnpm -r typecheck                   # strict + noUncheckedIndexedAccess
pnpm run lint:no-external           # 🔴 见下
pnpm run gen:api:check              # 契约没漂移
```

> 🔴 **`lint:no-external` 是可达性防线不是代码风格。**
> 面板不得引用任何第三方主机（字体、CDN、图标、统计），
> 在中国那会让整页卡住等超时（ADR 0003）。脚本做三层检查，
> 反向验证过：往 `dist/index.html` 塞一个 Google Fonts 的 `<link>` 会让它 exit 1。

**三态调试开关**（脚手架期临时能力，接线时要删）：任何页面地址加
`?state=loading`、`?state=empty`、`?state=error&error=offline`
可以直接看到该页的加载 / 空 / 错三种形态，不需要真的把后端弄坏。

### 3.6 🔴 web 与 api 现在**还不能**在本地互通

**这不是配置问题，是两侧都还缺东西。** 2026-08-17 实测确认：

| 走法 | 现状 | 为什么不通 |
|---|---|---|
| 同源（`apiBaseUrl` 留空，落到 `window.location.origin`） | ❌ | 前端请求 `http://localhost:5173/api/v1/…`，而两个 `vite.config.ts` 的 `server` 段**都没有 `proxy` 配置**，vite 直接 404 |
| 跨源（`apiBaseUrl` 指到 `http://localhost:18080`） | ❌ | `api/` 侧**没有任何 CORS 中间件**（`grep -ri cors api/cmd api/internal` 为空），浏览器预检就被拦下 |

两条路都堵着，所以**现在的前端页面全是静态骨架，没有一个真的调过本地 API**。

**不要随手加一个 vite dev proxy 把它糊过去。** 生产形态是
「API 与 Web 各用独立主域名池」（README §关键约束 1），**本来就是跨源的**；
dev 阶段用同源代理绕过去，只会把「API 缺 CORS」这件事藏到上线那天才爆。
正确顺序是先裁决允许哪些 Origin（域名池是动态的，这不是一行 `AllowAll` 能了事的），
在 `api/` 侧实现 CORS 中间件，前端再用 `runtime-config.js` 把 `apiBaseUrl`
指到本地 API —— 那条路径和生产完全同构。已登记在 §6。

> 在此之前，本地改前端就只看 UI；要验证契约层，靠 `pnpm -r typecheck`
> （`shared/api/queries.ts` 里那 7 个真正接线的只读查询，作用就是让生成的类型
> 每次 typecheck 都被真的走一遍）。

---

## 4 · 当前实现状态

| 部分 | 状态 |
|---|---|
| 数据库 schema（43 表 / 118 索引 / 4 视图） | ✅ 可正向可回滚，实测通过 |
| sqlc 查询层（6 个领域，1511 行查询） | ✅ 生成通过 |
| OpenAPI 契约（128 operation） | ✅ 生成 Go 接口通过 |
| 配置 fail-closed | ✅ 实测通过 |
| 节点鉴权（每节点密钥 + 哈希存储 + scope） | ✅ 实测通过 |
| ETag 弱比较 | ✅ 单测覆盖 13 个用例 |
| UniProxy `/config` `/user` 的 ETag 协商 | ✅ 已实现（响应体组装仍是 TODO） |
| 其余 122 个 operation | ⬜ 返回 501 |
| 用户面 / 管理面鉴权中间件 | ⬜ **未实现**（因端点全 501，当前无鉴权缺口） |
| web workspace（shared / user / admin） | ✅ 构建、类型检查、外链检查全通过 |
| web 路由骨架（用户 20 条 + 后台 21 条） | ✅ 每页有布局与三态占位，业务逻辑全是 `TODO(P1)` |
| web TS 客户端（从契约生成，128 operation） | ✅ 生成物已提交，幂等已实测 |
| web ↔ api 本地互通 | ⬜ **不通**（无 dev proxy 且 API 无 CORS，见 §3.6） |
| web 登录态与路由守卫 | ⬜ **未实现**，所有页面可直达（`web/README.md` §8 已标红） |
| 后台危险操作确认组件 `DangerAction` | ⬜ **未实现**，16 条 D 项目前只有清单展示 |
| CI（9 个作业）与 deploy（6 个作业） | ✅ 已建；本机可复跑的作业均已等价验证 |
| GCP 上的 `bp-` 资源 | ⬜ **一个都还没建**，`infra/` 的脚本从未真实执行过 |

**实现新端点前必须先补对应的鉴权中间件** —— 见
`cmd/server/main.go` 的 `nodeScopeMiddleware` 注释。

---

## 5 · 代价

> ⚠️ 1. **`make` 目标全部依赖 Docker**，容器启动开销让单次 `make test`
> 比本机跑慢几秒。换来的是不要求任何人在本机装 Go 工具链，且 CI 与本地环境一致。
> 2. **`FROM scratch` 的镜像无法 `docker exec` 进去调试。**
> 这是为了体积（16.4 MB）与冷启动做的取舍，排障靠日志与修订版回滚。
> **若将来确实需要登机调试，改回 distroless debug 变体即可，但要重新解决 gcr.io 可达性。**
> 3. §2 的两个绕过是**这台机器**的环境事实，写死在 Makefile 里
> （`mirror.gcr.io` 与清空代理）。换到网络正常的机器上这些绕过是无害的冗余，
> 但**如果有人在别的网络环境下遇到新的坑，应该回来更新本节**。
> 4. **两套工具链、两种心智**：`api/` 一切走容器且入口是 `make`，
> `web/` 一切走宿主机且入口是 `pnpm`。没有一条命令能把两边一起跑起来，
> 「改了契约要记得两边都重新生成」（§3.3）只能靠 CI 的 `contract-drift` 兜底，
> 本地没有任何东西提醒你。统一成一个入口是可以做的，但那要求前端也接受容器里的网络坑。
> 5. **§2.1 的 `docker build` 绕过要改 `FROM` 行，这是一个容易误提交的动作。**
> 用 `sed` 生成到 `/tmp` 而不是原地改，就是为了让「误提交」需要额外一步刻意操作。
> 6. **本文的「实测通过」有边界**：本机 Docker Hub 不可达，
> 所以 `docker build` 是把基础镜像换成同内容的 `mirror.gcr.io` tag 后验证的；
> `sqlc/sqlc:1.31.1` 也是从 `mirror.gcr.io` 拉下来再打上该 tag 的。
> 镜像**内容**一致，但「CI 那条 `docker build api/` 原样命令在直连环境下成功」
> 这件事本机证明不了 —— **需实测**（第一次 CI 跑绿就算证明）。

## 6 · 这次没有解决的

已解决的划掉，保留原文以便对照 —— 划掉的那几条说明这份手册在往前走。

- [x] ~~未验证 `sqlc generate` 的输出与提交版本一致（CI 的 `git diff --exit-code` 尚未建）。~~
      ✅ 2026-08-17 解决：`ci.yml` 的 `contract-drift` 作业已建，五处生成物逐一比对；
      本机也把整条链（oapi-codegen v2.4.1 → sqlc 1.31.1 → `gen_stubs.py` → `gofmt -w .`）
      重跑了一遍，`api/` 侧生成物与提交版本**逐字节一致**。
- [x] ~~web 侧的本地开发流程未写（`web/` 尚未搭建）。~~
      ✅ 2026-08-17 解决：见 §1、§3.3、§3.5、§3.6。

仍然没有解决：

- [ ] 没有 `docker compose` 一键起全栈 —— 目前要手敲 network + 两个容器。
      加上 web 之后这件事更值得做了：现在要开三个终端（pg、api、vite）。
- [ ] 没有集成测试（跑真实 Postgres 的 handler 测试）。当前只有 httpx 的单测。
      **web 侧则是一行测试都没有**，包括 `shared/api/client.ts` 里
      「POST 不做故障转移」那条会被后来者好心改错的规则。
- [ ] `make migrate-*` 直接 `psql` 灌文件，**没有用 golang-migrate**，
      因此没有版本表、不能增量迁移、不防重复执行。生产部署必须换成真正的迁移工具。
- [ ] 🔴 **web 与 api 在本地互不可达**（§3.6）。缺的是 `api/` 侧的 CORS 中间件，
      而它卡在一个未决问题上：**允许哪些 Origin？** 面板域名池是会动态增删的
      （ADR 0003 §5「一键新增镜像域名」），所以白名单不能写死在代码里，
      要么读运行时配置、要么由部署时注入。这是一次真正的裁决，不是补一行中间件。
- [ ] `?state=loading|empty|error` 这个三态开关**会进生产构建**（`web/README.md` §7 代价 3）。
      它不泄漏数据，但接线时必须删掉 `resolveShellState` 对查询参数的读取。
- [ ] **`infra/` 下的脚本一行真实变更都没跑过。** shellcheck 零告警、`bash -n`、
      `--dry-run` 三件事都不能证明 `gcloud` 的参数组合是对的，
      所有 `gcloud` 子命令名与 `--format=json` 字段路径仍标 **待核实**。
- [ ] **`deploy.yml` 从未真实运行过。** WIF、Cloud Run 部署、隔离核对全是纸面的；
      `vars.GCP_WIF_PROVIDER` / `GCP_DEPLOY_SA` / `BP_WEB_DEPLOY_CMD` / `BP_MIGRATE_JOB`
      四个仓库变量都还没配。
- [ ] `AGENTS.md` §1/§2 仍写着「仓库中只有文档，没有实现代码」，**已经过时**，
      需要有人回去改（本次未动，不在分配范围内）。

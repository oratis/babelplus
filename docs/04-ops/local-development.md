# 本地开发环境（无需在本机装 Go）

> 日期：2026-08-16（2026-08-17 补 web 侧并全量复跑；2026-08-17 补 P1 内核落地后的联调与冒烟基线；
> 2026-08-20 按线上实况修 §4 的 GCP 一行）
> 性质：**执行手册**
> 状态：**As-Built**（2026-08-17 端到端复验通过，39 条冒烟断言全绿）
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

**基线（2026-08-17 实测）**：`make check` 全绿，155 个顶层用例 + 98 个子用例，0 失败。

竞态检测不在 `make check` 里，因为 `-race` 需要 cgo，而 `golang:1.25-alpine`
默认没有 C 工具链（直接跑会报 `-race requires cgo`）。要跑的话：

```bash
make GO=go test   # 本机装了 Go 的话最简单；否则在容器里先装 gcc：
# docker run ... golang:1.25-alpine sh -c 'apk add --no-cache gcc musl-dev && CGO_ENABLED=1 go test -race ./...'
```

2026-08-17 用上面第二条跑过一次，全部包通过（订阅面有异步 `TouchSubscriptionToken`
的 goroutine，值得偶尔跑一次）。

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

实测结果：**44 张表、120 个索引、4 个视图；回滚后残留 0 张表。**（2026-08-23 复测，含新增的 `rate_limit`；此前是 43 表 / 118 索引。）

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

**冒烟需要种子数据。** 空库跑不出有意义的结果：注册要求 `server_groups` 里至少有一行
（`users.group_id` 是 NOT NULL 外键）与一个有效邀请码，节点面要求一台 server 加一把密钥。
migration 里**没有**任何种子数据，这是刻意的（种子数据属于运营，不属于 schema），
所以下面第 2 步不能跳。

**第 1 步：起库灌表**（见 §3.2）

```bash
cd api && make pg-up && sleep 5 && make migrate-up
```

**第 2 步：灌种子数据**

节点密钥的哈希算法是 `sha256(BP_NODE_KEY_PEPPER + 明文密钥)`，
订阅 token 同理但用 `BP_SUBSCRIPTION_TOKEN_PEPPER`。
**pepper 拼在前面**（`middleware.HashSessionToken` / `AuthenticateNode` 都是这个顺序），
顺序反了的现象是「密钥看着完全正确但一直 401」。

```bash
NODE_PEPPER='dev-only-pepper-node-0000000'
NODE_TOKEN='bpk_smoke_0123456789abcdefghij'      # 形态要求：24–128 位 [A-Za-z0-9_-]
HASH=$(printf '%s' "${NODE_PEPPER}${NODE_TOKEN}" | shasum -a 256 | cut -d' ' -f1)

docker exec -i bp-dev-pg psql -v ON_ERROR_STOP=1 -U postgres -d babelplus <<SQL
INSERT INTO server_groups (code, name) VALUES ('basic', '基础组');
INSERT INTO servers (code, name, protocol, host, port, server_port, region, protocol_settings)
VALUES ('bp-node-smoke', '冒烟节点', 'vless_reality', '203.0.113.10', 443, 443, 'asia-east2',
  '{"listen_ip":"0.0.0.0","server_name":"www.microsoft.com","reality_private_key":"PRIVKEY_CANARY",
    "reality_short_id":"a1b2c3d4","reality_dest":"www.microsoft.com:443","reality_public_key":"PUBKEY_SMOKE"}'::jsonb);
INSERT INTO server_group_map (server_id, group_id)
  SELECT s.id, g.id FROM servers s, server_groups g WHERE s.code='bp-node-smoke' AND g.code='basic';
INSERT INTO node_rev (server_id) SELECT id FROM servers WHERE code='bp-node-smoke';
INSERT INTO server_keys (server_id, key_prefix, key_hash, scopes)
  SELECT id, 'bpk_smoke', decode('${HASH}','hex'),
    ARRAY['node:config:read','node:users:read','node:traffic:write',
          'node:alive:write','node:alive:read','node:status:write']
  FROM servers WHERE code='bp-node-smoke';
INSERT INTO invite_codes (code, max_uses, note) VALUES ('SMOKE2026', 5, '冒烟测试用');
SQL
```

**第 3 步：起 API**

用 `go run` 而不是 `make docker-build`：改一行代码就重建一次 16 MB 镜像太慢，
而且冒烟阶段要频繁看日志。`host.docker.internal` 让容器连回宿主机上 `make pg-up` 发布的 55433 端口，
不用另建 docker network。

```bash
R=$(cd .. && pwd)   # 仓库根
docker run -d --name bp-smoke-api \
  -v "$R":/w -v "$R/.gocache":/root/.cache/go-build -v "$R/.gomodcache":/go/pkg/mod \
  -w /w/api -p 18080:8080 \
  -e HTTP_PROXY= -e HTTPS_PROXY= -e http_proxy= -e https_proxy= -e NO_PROXY='*' \
  -e GOPROXY='https://goproxy.cn,https://proxy.golang.org,direct' -e GOFLAGS=-mod=mod \
  -e BP_ENV=dev \
  -e BP_DATABASE_URL='postgres://postgres:devpass@host.docker.internal:55433/babelplus?sslmode=disable' \
  -e BP_SUBSCRIPTION_TOKEN_PEPPER=dev-only-pepper-sub-00000000 \
  -e BP_NODE_KEY_PEPPER="$NODE_PEPPER" \
  -e BP_SESSION_SIGNING_KEY=dev-only-session-key-000000 \
  -e BP_GCP_PROJECT_ID=oratis-491316 \
  -e BP_ALLOWED_ORIGINS='http://localhost:5173,http://localhost:5174' \
  golang:1.25-alpine sh -c 'go run ./cmd/server'
docker logs -f bp-smoke-api      # 等到「监听中」
```

> **注册验证码怎么拿**：ESP 尚未接入（ADR 0002 定的 SES 还没接），
> `BP_ENV=dev` 时验证码明文打在日志里的 `secret` 字段：
> `docker logs bp-smoke-api | grep '仅 dev' | tail -1`。
> staging/prod **不打** —— 打出来等于把验证码写进 Cloud Logging，而日志读取权限比数据库宽得多。

#### 冒烟基线（2026-08-17 实测，39 条断言全绿）

**这是回归基线，改动后应保持一致。** 期望值里的错误码全部是 `openapi.yaml`
的 `ErrorCode` enum 成员 —— 前端按 `code` 分支，enum 外的值会全部落到兜底分支。

| 请求 | 期望 |
|---|---|
| `GET /-/healthz` | `200` `ok` |
| **订阅面** | |
| `GET /s/abc`（过短） | `404`，**不查库**直接拒 |
| `GET /s/<含非法字符>` | `404`，同上 |
| `GET /s/<合法形态但不存在>` | `404`，与上面两条**响应体逐字节一致** |
| `GET /s/<有效 token>`（clash UA） | `200` `text/yaml`，头部 `subscription-userinfo` 全小写 |
| 订阅正文 | 不含 `protocol_settings` 里的 REALITY 私钥，不含 token 明文 |
| **节点面** | |
| `GET UniProxy/user` 无密钥 | `401` `NODE_KEY_INVALID` |
| `GET UniProxy/user` 错密钥 | `401` `NODE_KEY_INVALID` |
| `GET UniProxy/user` 短密钥 | `401`（**不查库**直接拒） |
| `GET UniProxy/user` 有效密钥、无可用用户 | `200` `{"users":[]}` —— **是 `[]` 不是 `null`** |
| `GET UniProxy/user` 带 `If-None-Match` | `304`，空响应体 |
| `GET UniProxy/user` scope 不含 `node:users:read` | `403` `NODE_SCOPE_DENIED` |
| `GET UniProxy/user` 节点 `enabled=false` | `403` `AUTH_PERMISSION_DENIED` |
| `GET UniProxy/config` 有效密钥 | `200`，裸 JSON 无信封 |
| **CORS** | |
| `OPTIONS /api/v1/plans` + `Origin: http://localhost:5173` | `204` + 回显该 Origin + `Vary: Origin` |
| `OPTIONS` + `Origin: https://evil-babel.plus` | `204`，**不带任何 CORS 头** |
| `OPTIONS` + `Origin: http://localhost:5173.attacker.com` | 同上，不回显（后缀绕过不成立） |
| 任意响应（**含无 Origin 的**） | 都带 `Vary: Origin` |
| **用户面鉴权** | |
| `GET /api/v1/user/me` 无凭据 | `401` `AUTH_TOKEN_INVALID` |
| `GET /api/v1/user/me` 伪造 token | `401`，与上一条**不可区分** |
| `GET /api/v1/admin/users`（管理面） | `501`（鉴权未实现 → 中间件 fail-closed，见 §4 脚注） |
| `POST /internal/tasks/traffic-reset` | `501`，同上 |
| **账户全链路** | |
| `POST /auth/email-code` | `204`（已注册/未注册都是 204，且**都记账**） |
| `POST /auth/register` | `201` + `SessionTokens` |
| `POST /auth/login` | `200` + `SessionTokens` |
| `GET /api/v1/user/me` 带登录 token | `200`，返回本人邮箱 |
| `POST /auth/login` 口令错误 | `401` |
| `POST /auth/login` 邮箱不存在 | `401`，与口令错误**同码同耗时** |
| `POST /auth/logout` | `204`，之后原 token 立刻 `401` |
| **日志脱敏** | |
| 访问日志 | 不含订阅 token 明文（路径打码成 `/s/xxxxxxxx…`）、不含节点密钥（query 从不入日志） |
| **启动** | |
| 不带任何环境变量启动 | 非 0 退出，一次性列出全部 6 个缺失变量 |
| `BP_ENV=staging` 且缺 `BP_ALLOWED_ORIGINS` | 拒绝启动（dev 有默认值 5173/5174） |

> **501 与 500 的区分是刻意的**：当前有 110 个端点未实现，
> 若它们都算 500，5xx 告警会长期为红，真正的故障反而被淹没。
> [monitoring.md](monitoring.md) 的告警规则按「5xx 排除 501」建。

> **怎么证明「不查库」**：给本地 Postgres 开 `ALTER SYSTEM SET log_statement='all'` +
> `SELECT pg_reload_conf()`，然后打一批非法形态的 token，
> `docker logs bp-dev-pg | grep -c subscription_tokens` 应保持不变。
> 2026-08-17 实测：6 次非法形态请求之后计数为 0 增量。
> 光看 404 状态码证明不了这件事 —— 查了库再返回 404 也是 404。

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

### 3.6 web ↔ api 本地联调（跨源，与生产同构）

**2026-08-17：这条路通了。** 原先卡在「`api/` 侧没有 CORS 中间件」，现已实现
（`api/internal/middleware/cors.go`），白名单来自 `BP_ALLOWED_ORIGINS`，
dev 缺省就是 `http://localhost:5173,http://localhost:5174`。

**仍然不要加 vite dev proxy。** 生产形态是「API 与 Web 各用独立主域名池」
（README §关键约束 1），**本来就是跨源的**；用同源代理绕过去只会把跨源问题藏到上线那天。
下面这条路径与生产完全同构。

**第 1 步：起 API**（见 §3.4 第 3 步）。确认启动日志里
`"allowed_origins":["http://localhost:5173","http://localhost:5174"]`。
显式传 `BP_ALLOWED_ORIGINS` 也可以，但**白名单是精确字符串相等匹配**：
`http://localhost:5173/`（多一个尾斜杠）、`localhost:5173`（缺协议）都会被 config 层拒绝启动，
而不是静默不匹配 —— 后者的现象与「压根没配」一模一样。

**第 2 步：把 `apiBaseUrl` 指到本地 API**

```bash
# 用户面板；后台同理改 web/admin/public/runtime-config.js
sed -i '' "s#apiBaseUrl: ''#apiBaseUrl: 'http://localhost:18080'#" web/user/public/runtime-config.js
cd web && pnpm dev:user      # → http://localhost:5173
```

> ⚠️ `runtime-config.js` 是**提交进仓库**的文件（部署时覆盖它就能换域名，不重新构建）。
> 本地改完**别提交**：`git checkout web/user/public/runtime-config.js` 还原。
> CI 的 `contract-drift` 作业不看它，唯一的防线是你自己的 `git status`。

**第 3 步：验证**

浏览器 devtools 的 Network 里应当看到：`/api/v1/user/me` 先一条 `OPTIONS`（204），
再一条真实请求（未登录时 401 `AUTH_TOKEN_INVALID`）。
**看到 401 就是通了** —— 那是应用层的正确回答；被 CORS 拦下的表现是
`TypeError: Failed to fetch`，请求在 Network 里显示为 `(failed)` 且没有响应头。

2026-08-17 实测（真实浏览器，不是 curl）：

| Origin | `GET /-/healthz`（简单请求） | `GET /user/me` 带 `Authorization`（触发预检） | `POST /auth/login`（预检 + JSON） |
|---|---|---|---|
| `http://localhost:5173`（白名单内） | `200 ok` | `401 AUTH_TOKEN_INVALID` | `401` |
| `http://localhost:5175`（白名单外） | `TypeError: Failed to fetch` | 同左（预检就被拦） | 同左 |

白名单外的那一行是**反向验证**：不做这一步的话，「CORS 生效了」与
「CORS 中间件写成了 `AllowAll`」在正向测试里长得一模一样。

> 契约层的静态验证仍然靠 `pnpm -r typecheck`
> （`shared/api/queries.ts` 里那 7 个真正接线的只读查询，作用就是让生成的类型
> 每次 typecheck 都被真的走一遍）。**web 侧一行运行时测试都还没有**，见 §6。

---

## 4 · 当前实现状态

截至 2026-08-17（P1 内核落地后复验）。

| 部分 | 状态 |
|---|---|
| 数据库 schema（44 表 / 120 索引 / 4 视图） | ✅ 可正向可回滚，2026-08-23 实测：建表 44、回滚后残留 0 表 / 0 视图 / 0 枚举 |
| sqlc 查询层（6 个领域） | ✅ 生成通过；`sqlc generate` 与提交版本一致 |
| OpenAPI 契约（128 operation） | ✅ `make gen-api` + `gen-stubs` + `gofmt` 后零漂移 |
| 配置 fail-closed | ✅ 实测通过（含 `BP_ALLOWED_ORIGINS` 的格式校验） |
| **已实现的 operation：18 / 128** | ✅ 见下表 |
| **仍返回 501 的 operation：110 / 128** | ⬜ |
| 节点鉴权（每节点密钥 + sha256 存储 + scope 白名单） | ✅ 实测通过（401/403 分流、短密钥不查库） |
| **用户会话鉴权** | ✅ 已实现**且已挂载**（`cmd/server/authmap.go`） |
| 管理面 / 内部任务鉴权 | ⬜ **未实现**；中间件对这 70 个 operation 一律 `501` **fail-closed** |
| ETag 弱比较 | ✅ 单测覆盖；`/user` `/config` 的 304 协商实测通过 |
| UniProxy 五端点（config / user / push / alive / alivelist） | ✅ 已实现（`/status` 仍是 501） |
| 账户体系十端点（注册 / 登录 / 改密 / 找回 …） | ✅ 已实现；argon2id + 不可枚举登录实测通过 |
| 订阅下发（`/s/{token}` 与 `/client/subscribe`） | ✅ 三格式渲染 + 审计写入实测通过 |
| CORS | ✅ 已实现并挂载；真实浏览器正反向验证通过（§3.6） |
| 日志脱敏（订阅 token 不进访问日志） | ✅ 已实现（`middleware.RedactPath`），有回归测试 |
| 幂等骨架 | ✅ 三态 `BeginIdempotent` 已实现；⚠️ **清理定时任务尚未挂**（§6） |
| Go 全套（build / vet / test / gofmt / race） | ✅ 全绿：195 个顶层用例 + 133 个子用例，0 失败（2026-08-23 复测） |
| web workspace（shared / user / admin） | ✅ 构建、类型检查、外链检查全通过 |
| web 路由骨架（用户 20 条 + 后台 21 条） | ✅ 每页有布局与三态占位，业务逻辑全是 `TODO(P1)` |
| web TS 客户端（从契约生成，128 operation） | ✅ 生成物已提交，幂等已实测 |
| web ↔ api 本地互通 | ✅ **通了**（跨源 + CORS，与生产同构，见 §3.6） |
| web 登录态与路由守卫 | ⬜ **未实现**，所有页面可直达（`web/README.md` §8 已标红） |
| web 页面真的调过本地 API | ⬜ **还没有** —— §3.6 验证的是浏览器层的 CORS，不是页面业务接线 |
| 后台危险操作确认组件 `DangerAction` | ⬜ **未实现**，16 条 D 项目前只有清单展示 |
| 登录 / 邀请码校验的限流（`rate_limit` 表） | ✅ **已实现**（migration 0013 + `internal/ratelimit`）：`login` / `email-code` / `forgot` / `invite/verify` 四个端点，超限 429 + `Retry-After`。⚠️ 契约要的**指数退避未做**，且限流器是**失败开放**的（§6） |
| 邮件真正发送（SES） | ⬜ **未接**，`email_log.status` 恒为 `queued` |
| CI（9 个作业）与 deploy（6 个作业） | ✅ 已建；本机可复跑的作业均已等价验证 |
| GCP 上的 `bp-` 资源 | ✅ **`bp-api` / `bp-db` / `bp-api-sa` / 4 个 `bp-` secret 已建并在计费**（2026-08-20 复核，见 [as-built-gcp §10](../02-architecture/as-built-gcp.md)）；`bp-web`、`bp-migrate`、Scheduler / Tasks / Pub/Sub 与 `infra/node/` 侧仍无 |

**已实现的 18 个 operation**：`GetHealthz`；节点面 `GetUniProxyConfig` `GetUniProxyUsers`
`GetUniProxyAliveList` `PushUniProxyTraffic` `PushUniProxyAlive`；订阅面 `GetShortSubscription`
`GetClientSubscription`；账户面 `RegisterAccount` `SendEmailCode` `Login` `RefreshToken` `Logout`
`ForgotPassword` `ResetPassword` `VerifyInviteCode` `GetCurrentUser` `ChangePassword`。

#### 🔴 鉴权装配：新增端点必读

事实源是 **`api/cmd/server/authmap.go`** 的五张表，128 个 operation 各归其一：

| 表 | 条数 | 中间件行为 |
|---|---|---|
| `handler.PublicOperations` | 11 | 放行（凭据是订阅 token / 验证码 / 网关签名，由 handler 自己校验） |
| `nodeOperationScopes` | 6 | `AuthenticateNode` + scope 白名单 |
| `userSessionOperations` | 41 | `AuthenticateUser` |
| `adminOperations` | 61 | ⬜ 鉴权未实现 → **一律 501**（fail-closed） |
| `internalTaskOperations` | 9 | 同上 |

**为什么 admin/internal 是 501 而不是放行**：放行的写法下，任何人实现一个 admin handler
的那一刻就上线了一个无鉴权的管理端点，而代码 diff 里看不出任何异常。
现在的行为与它们当前的 handler **逐字节一致**（都是 501），代价为零。
实现管理面时把那个分支换成真正的 adminSession 中间件，**不要只改 handler**。

`TestOperationAuthCoverage` 用反射列出 `StrictServerInterface` 的全部方法，
强制「五张表互不相交且并集等于全集」。**它挡住的是一类运行时完全静默的错误** ——
上一版把 `PushUniProxyStatus` 写成了不存在的 `GetUniProxyStatus`，
于是那个 operation 落到「未分类」分支被原样放行、不做任何鉴权。
当时无害（handler 是 501），但实现 `/status` 的那一刻它就是一个无鉴权的写端点。

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
- [x] ~~🔴 **web 与 api 在本地互不可达**（§3.6）。缺的是 `api/` 侧的 CORS 中间件，
      而它卡在一个未决问题上：**允许哪些 Origin？**~~
      ✅ 2026-08-17 解决。裁决是**运行时配置 + 精确字符串相等匹配**：
      白名单来自 `BP_ALLOWED_ORIGINS`，代码里**没有任何模式匹配的路径**，
      所以 `evil-babel.plus` / `localhost:5173.attacker.com` 这两类经典绕过在结构上不成立
      （已在真实浏览器里正反向验证，见 §3.6）。域名池动态增删靠改环境变量 + 重新部署，
      与 `runtime-config.js` 的思路一致。永不输出 `*`；`Vary: Origin` 无条件加。

仍然没有解决：

- [ ] 没有 `docker compose` 一键起全栈 —— 目前要手敲 network + 两个容器。
      加上 web 之后这件事更值得做了：现在要开三个终端（pg、api、vite）。
      §3.4 的三步 + §3.6 的两步是目前唯一的路。
- [ ] **没有集成测试**（跑真实 Postgres 的 handler 测试）。当前全部是纯单测 +
      一次性的手工冒烟：§3.4 那 39 条断言是**人跑的，没有进 CI**，
      种子数据也是手敲 SQL。这意味着「订阅 404 不查库」「空列表是 `[]` 不是 `null`」
      这类只在真库上才成立的性质，下一次改动时没有任何东西会提醒你它坏了。
      **web 侧则是一行测试都没有**，包括 `shared/api/client.ts` 里
      「POST 不做故障转移」那条会被后来者好心改错的规则。
- [ ] `make migrate-*` 直接 `psql` 灌文件，**没有用 golang-migrate**，
      因此没有版本表、不能增量迁移、不防重复执行。生产部署必须换成真正的迁移工具。
- [ ] **migration 里没有任何种子数据**，而注册链路硬依赖 `server_groups` 至少一行
      （`users.group_id` 是 NOT NULL 外键）。空库上注册会 500 而不是给出可操作的错误。
      §3.4 第 2 步是人工补位，不是长久之计 —— 需要一份 seed migration 或部署步骤。
- [ ] 🔴 **`idempotency_keys` 的清理定时任务没有挂。** `CleanupExpiredIdempotencyKeys`
      当前没有任何调用点。不挂的后果不是「表变大」而是**下单与流量入账开始失败**：
      `ClaimIdempotencyKey` 的 `ON CONFLICT` 认主键（不看过期），
      而 `GetIdempotencyKey` 带 `expires_at > now()` 过滤，
      于是过期未清理的行会永久卡住同名键。每次非空 `/push` 都写一行（10 节点 ≈ 1.4 万行/天）。
      按 system-design §4 应该走 Cloud Scheduler，而**进程内不能加 ticker**
      （Cloud Run 缩到 0，加 ticker 等于必须常开 min-instances，成本模型立刻不成立）。
- [x] ~~**登录与邀请码校验没有限流**~~ —— 2026-08-23 落地：migration `0013_rate_limit`
      （`UNLOGGED` 表，ADR 0005 §8 的同一条裁决）+ `internal/ratelimit` 包，
      接在 `/auth/login`（per IP + per email 各 5/min、10/h）、`/auth/email-code`
      与 `/auth/password/forgot`（per IP 10/h 的门口计数）、`/invite/verify`（per IP 30/min）上。
      **仍然欠着的三件事**，都不是「小尾巴」：
      - 契约要求的**指数退避 / 解锁倒计时未实现**（当前是固定窗口）。
        做它之前要先裁定「锁定的是 IP 还是账号」—— 锁定账号可以被用来定向拒绝某个用户登录。
      - 限流器**失败开放**：数据库不可用时放行。理由写在 `internal/ratelimit` 的包注释里，
        但它意味着 `bp_ratelimit_degraded` 这条日志指标不建出来，限流失效就是**静默**的。
      - `rate_limit` 是 `UNLOGGED` 表，**Cloud SQL 崩溃或计划内维护重启会清空全部计数**。
        窗口最长 1 小时（由 CHECK 强制），所以损失有界，但这是一次有意的取舍不是顺带的好处。
- [ ] `?state=loading|empty|error` 这个三态开关**会进生产构建**（`web/README.md` §7 代价 3）。
      它不泄漏数据，但接线时必须删掉 `resolveShellState` 对查询参数的读取。
- [ ] **`infra/` 下的脚本，只有 `deploy/` 侧有线上结果可对照。**
      2026-08-20 复核：`bp-api` / `bp-db` / `bp-api-sa` / 4 个 secret 已经存在，
      且线上参数与 `setup-infra.sh`、`deploy-api.sh` 逐项一致（as-built §10）——
      但**没有取到执行日志**，不能断言资源就是这两个脚本建的。
      `deploy-web.sh`、`rollback.sh` 与 `infra/node/` 的四个脚本**仍无任何线上痕迹**，
      它们的 `gcloud` 子命令名与 `--format=json` 字段路径仍标 **待核实**。
- [ ] **`deploy.yml` 从未真实运行过。** WIF、Cloud Run 部署、隔离核对全是纸面的；
      `vars.GCP_WIF_PROVIDER` / `GCP_DEPLOY_SA` / `BP_WEB_DEPLOY_CMD` / `BP_MIGRATE_JOB`
      四个仓库变量都还没配。
- [ ] 🔴 **`db/queries/stats.sql` 的 `BulkAddUserTraffic` 是坏的**（已在真库复现，不是理论风险）：
      两个 `bigint` 算术表达式没写 `::bigint`，sqlc 推成 `int4`，生成的 Row 字段是 `int32`。
      用户本周期累计超过 2 GiB 时 pgx 在 **Scan 阶段**报错、事务回滚、整批流量丢失。
      最小套餐也是几十 GB，上线第一天就会踩到。当前由 `servers.sql` 的 `AddNodeTrafficBatch`
      （同语义、显式 `::bigint`）顶住，但那是**两份同义 SQL 并存**。
      修法：给原查询加 `::bigint` → `make gen-db` → 删掉副本、调用方切回去。
      **同类风险值得全仓扫一遍**：任何 `:many/:one` 里 bigint 算术没显式转型的地方都可能被推成 int32。
- [ ] **`api/.env.example` 里没有 `BP_ALLOWED_ORIGINS`。** dev 有默认值不受影响，
      staging/prod 缺失即拒绝启动（错误消息自带格式说明与示例，不会让人卡住），
      但样例文件应当补上。同时缺的还有 `BP_WEB_BASE_URL`：
      订阅响应里的 `profile-web-page-url` 与伪节点名里的 Web 域名目前借用
      `BP_ALLOWED_ORIGINS` 的第一项，等于把「放行某个 Origin」和「告诉用户去哪续费」绑死了，
      而 ADR 0002 的失联恢复会轮换 Web 域名。
- [ ] **订阅的 404 写不进 `subscription_fetch_log`**：该表 `user_id` 是 NOT NULL，
      而走到 404 恰恰意味着不知道 user_id。后果是「有人拿着已吊销的 token 一直在刷」
      在审计表里完全看不见，只有应用日志里一条 INFO。
      修法需要 DDL 改动（user_id 可空，或另开一张匿名拉取表）。
      注意 `db/queries/subscriptions.sql` 的注释写着「每次拉取都写一条，包括 304 与 404」——
      那句话与当前 DDL 自相矛盾，需要一并裁决。
- [ ] `AGENTS.md` §1/§2 仍写着「仓库中只有文档，没有实现代码」，**已经过时**，
      需要有人回去改（本次未动，不在分配范围内）。

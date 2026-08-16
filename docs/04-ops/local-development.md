# 本地开发环境（无需在本机装 Go）

> 日期：2026-08-16 · 性质：**执行手册** · 状态：**As-Built**（2026-08-16 实测通过）
> 事实基线：本文每条命令都在这台开发机上实际跑通过，不是照着文档抄的
> 读者：接手 `api/` 的开发者。第一次拉仓库时从 §1 开始。
> 关联：[deploy.md](deploy.md)、[ADR 0006](../05-adr/0006-api-stack.md)

---

## 1 · 前提

只需要 **Docker**。**不需要在本机装 Go、sqlc、oapi-codegen、psql** —— 全部走容器。

已验证的版本：Docker 28.1.1、Node 24.12（web 用）、pnpm 10.33。

```bash
cd api && make help    # 列出所有可用目标
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

```bash
make generate     # = gen-api + gen-db + gen-stubs + fmt
```

三个生成物**都要提交进仓库**，CI 用 `git diff --exit-code` 卡漂移：

| 生成物 | 来源 | 工具 |
|---|---|---|
| `api/internal/gen/api.gen.go` | `openapi/openapi.yaml` | oapi-codegen |
| `api/db/gen/*.go` | `db/migrations/` + `db/queries/` | sqlc |
| `api/internal/handler/unimplemented.gen.go` | 上面生成的接口 | `scripts/gen_stubs.py` |

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

## 6 · 这次没有解决的

- [ ] 没有 `docker compose` 一键起全栈 —— 目前要手敲 network + 两个容器。
- [ ] 没有集成测试（跑真实 Postgres 的 handler 测试）。当前只有 httpx 的单测。
- [ ] `make migrate-*` 直接 `psql` 灌文件，**没有用 golang-migrate**，
      因此没有版本表、不能增量迁移、不防重复执行。生产部署必须换成真正的迁移工具。
- [ ] 未验证 `sqlc generate` 的输出与提交版本一致（CI 的 `git diff --exit-code` 尚未建）。
- [ ] web 侧的本地开发流程未写（`web/` 尚未搭建）。

# bp-api 开发命令
#
# ⚠️ 本机可能没有装 Go。所有目标默认**在容器里跑**，不要求本机工具链。
# 装了 Go 的机器可以 `make GO=go build` 直接用本机 Go。
#
# 两个环境坑（在这台开发机上实测过，换机器可能不同）：
#  1. Docker Hub 拉不到 golang 镜像时用 mirror.gcr.io：
#       docker pull mirror.gcr.io/library/golang:1.25-alpine
#       docker tag  mirror.gcr.io/library/golang:1.25-alpine golang:1.25-alpine
#  2. Docker Desktop 会往容器注入代理变量，且端口可能与宿主机实际代理不一致，
#     导致容器内 go mod 下载失败。所以下面显式**清空**代理变量。

GO_IMAGE  ?= golang:1.25-alpine
SQLC_IMAGE ?= sqlc/sqlc:latest
REPO_ROOT  := $(shell cd .. && pwd)

# 清空代理 + 指定 GOPROXY + 挂载缓存目录（避免每次重新下载依赖）
DOCKER_GO = docker run --rm \
	-v "$(REPO_ROOT)":/w \
	-v "$(REPO_ROOT)/.gocache":/root/.cache/go-build \
	-v "$(REPO_ROOT)/.gomodcache":/go/pkg/mod \
	-w /w/api \
	-e HTTP_PROXY= -e HTTPS_PROXY= -e http_proxy= -e https_proxy= -e NO_PROXY='*' -e no_proxy='*' \
	-e GOPROXY='https://goproxy.cn,https://proxy.golang.org,direct' \
	-e GOFLAGS=-mod=mod \
	$(GO_IMAGE) sh -c

.PHONY: help
help: ## 显示本文件的所有目标
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-14s\033[0m %s\n", $$1, $$2}'

.PHONY: build
build: ## 编译
	$(DOCKER_GO) 'go build ./...'

.PHONY: vet
vet: ## go vet
	$(DOCKER_GO) 'go vet ./...'

.PHONY: fmt
fmt: ## 格式化
	$(DOCKER_GO) 'gofmt -w .'

.PHONY: fmt-check
fmt-check: ## 检查格式（CI 用，有未格式化文件则失败）
	$(DOCKER_GO) 'test -z "$$(gofmt -l .)" || { echo "以下文件未格式化："; gofmt -l .; exit 1; }'

.PHONY: test
test: ## 跑单测
	$(DOCKER_GO) 'go test ./...'

.PHONY: tidy
tidy: ## 整理依赖
	$(DOCKER_GO) 'go mod tidy'

.PHONY: generate
generate: gen-api gen-db gen-stubs fmt ## 重新生成全部生成物

.PHONY: gen-api
gen-api: ## 从 openapi.yaml 生成 Go 服务端接口
	$(DOCKER_GO) 'apk add --no-cache git >/dev/null && \
		go run github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@v2.4.1 \
		  -config oapi-codegen.yaml ../openapi/openapi.yaml'

.PHONY: gen-db
gen-db: ## 从 migrations + queries 生成 sqlc 代码
	docker run --rm -v "$(CURDIR)":/src -w /src $(SQLC_IMAGE) generate

.PHONY: gen-stubs
gen-stubs: ## 从生成的接口重建 128 个 501 stub
	python3 scripts/gen_stubs.py

.PHONY: check
check: fmt-check vet build test ## CI 的完整检查

.PHONY: pg-up
pg-up: ## 起本地 Postgres（端口 55433）
	docker run --rm -d --name bp-dev-pg \
		-e POSTGRES_PASSWORD=devpass -e POSTGRES_DB=babelplus \
		-p 55433:5432 postgres:17

.PHONY: pg-down
pg-down: ## 停本地 Postgres
	-docker rm -f bp-dev-pg

.PHONY: migrate-up
migrate-up: ## 对本地 Postgres 依序执行 up migration
	@for f in $$(ls db/migrations/*.up.sql | sort); do \
		echo "  → $$(basename $$f)"; \
		docker exec -i bp-dev-pg psql -v ON_ERROR_STOP=1 -q -U postgres -d babelplus < "$$f" || exit 1; \
	done

.PHONY: migrate-down
migrate-down: ## 逆序回滚全部 migration
	@for f in $$(ls db/migrations/*.down.sql | sort -r); do \
		echo "  → $$(basename $$f)"; \
		docker exec -i bp-dev-pg psql -v ON_ERROR_STOP=1 -q -U postgres -d babelplus < "$$f" || exit 1; \
	done

# 语义校验：对 db/gen/*.sql.go 里每一条真正会发到 Postgres 的 SQL 跑一次 EXPLAIN。
#
# 为什么需要它：sqlc.yaml 没有 `database:` 段，内置引擎只做语法与列名解析，
# 所以「往 GENERATED ALWAYS 列上写值」这种错误 sqlc generate 与 go build 都是 exit 0
# （ADR 0013 §6.3 实测），要到线上第一笔付款开通权利时才炸成 500。
# CI 的 migrations 作业跑的就是这一条；本目标是它的本地复现入口。
#
# ⚠️ EXPLAIN 不执行语句，抓不到 NOT NULL / CHECK / 外键这类运行期约束。
#    「0016.down 之后真的写一行」那个探针（scripts/ci_post_rollback_write.sql）只在 CI 跑，
#    因为它必须夹在 0016.down 与 0012.down 之间的那个瞬间。
.PHONY: db-explain
db-explain: ## 起库 → 灌 up → 对每条生成的 SQL 跑 EXPLAIN → 拆库
	@$(MAKE) pg-up
	@sleep 4
	@$(MAKE) migrate-up
	@python3 scripts/db_explain.py > /tmp/bp_db_explain.sql
	@docker exec -i bp-dev-pg psql -v ON_ERROR_STOP=1 -U postgres -d babelplus < /tmp/bp_db_explain.sql
	@$(MAKE) pg-down

.PHONY: migrate-verify
migrate-verify: pg-up ## 起库 → 灌全部 up → 全部 down → 确认归零 → 拆库
	@sleep 4
	@$(MAKE) migrate-up
	@echo "建表数: $$(docker exec -i bp-dev-pg psql -qtA -U postgres -d babelplus -c \
		"select count(*) from information_schema.tables where table_schema='public' and table_type='BASE TABLE';")"
	@$(MAKE) migrate-down
	@echo "回滚后残留: $$(docker exec -i bp-dev-pg psql -qtA -U postgres -d babelplus -c \
		"select count(*) from information_schema.tables where table_schema='public' and table_type='BASE TABLE';")"
	@$(MAKE) pg-down

.PHONY: docker-build
docker-build: ## 构建部署镜像
	# 显式清空代理 build-arg：Docker Desktop 注入的代理端口可能与宿主机不一致，
	# 不清空会让 go mod download 在构建容器里失败。
	docker build \
		--build-arg HTTP_PROXY= --build-arg HTTPS_PROXY= \
		--build-arg http_proxy= --build-arg https_proxy= \
		--build-arg GOPROXY='https://goproxy.cn,https://proxy.golang.org,direct' \
		-t bp-api:dev .

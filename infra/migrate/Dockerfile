# bp-migrate —— 数据库迁移镜像，作为 **Cloud Run Job** 运行
#
# 为什么是独立 Job 而不是 bp-api 启动时跑（deploy.md §6.3）：
#   bp-api 的 --max-instances=8，启动时跑迁移意味着 8 个实例并发跑同一份迁移。
#   即使有 advisory lock 也只是让 7 个实例白等，冷启动被拖长，且失败语义混乱。
#
# 为什么自己构建而不是用 migrate/migrate 官方镜像：
#   本机拉不到它 —— mirror.gcr.io 只镜像 Docker Hub 的 library/* 官方镜像，
#   `migrate/migrate` 是用户仓库不在其中，而 Docker Hub 直连在这台机器上失败
#   （auth.docker.io 返回 EOF，见 local-development.md §2.1）。
#   自己 go install 反而更干净：版本钉死、无额外注册表依赖。

FROM golang:1.25-alpine AS build

# Docker Desktop 注入的代理端口可能与宿主机不一致，构建方需显式清空（见 build-and-run.sh）。
ARG HTTP_PROXY
ARG HTTPS_PROXY
ARG http_proxy
ARG https_proxy
ARG GOPROXY=https://proxy.golang.org,direct
ENV GOPROXY=${GOPROXY}

# 版本钉死。升级前先读 golang-migrate 的 CHANGELOG —— 它改过 dirty 状态的语义。
ARG MIGRATE_VERSION=v4.18.1

# postgres 驱动 + file 源，其余 tag 一概不带（少一个 driver 少一份攻击面）。
RUN CGO_ENABLED=0 go install -tags 'postgres,file' -trimpath -ldflags="-s -w" \
      github.com/golang-migrate/migrate/v4/cmd/migrate@${MIGRATE_VERSION}

# ─────────────────────────────────────────────────────────────
# 运行阶段
#
# ⚠️ 这里**复用 golang:1.25-alpine 而不是 alpine:3.21**，是一个有意识的取舍：
#   本机 `docker build` 解析 FROM 时会去 registry 核对 digest，而 Docker Hub
#   在这台机器上不可达（local-development.md §2.1）。golang:1.25-alpine 已经
#   在本地并带着 digest，alpine:3.21 没有 —— 引入它会让构建直接卡死。
#
#   代价：镜像约 250 MB 而不是 ~30 MB。对一个**部署时才跑一次**的迁移 Job
#   这个体积无所谓（它不在任何热路径上，也不影响 bp-api 的冷启动）。
#   TODO(P2): CI 的 runner 直连 Docker Hub 没有这个限制，
#             可以在 CI 里改回 alpine:3.21 把体积降下来。
FROM golang:1.25-alpine

# psql 只用于迁移后的兜底自检（确认基表真的建出来了）。
RUN apk add --no-cache postgresql17-client ca-certificates \
 && adduser -D -u 65532 bp

COPY --from=build /go/bin/migrate /usr/local/bin/migrate
COPY migrations/ /migrations/
COPY entrypoint.sh /usr/local/bin/entrypoint.sh

RUN chmod +x /usr/local/bin/entrypoint.sh

USER 65532:65532

ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]

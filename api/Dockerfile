# bp-api 部署镜像
#
# 目标：**冷启动尽量快**。Cloud Run 用 min-instances=0（ADR 0005 的成本前提），
# 每次缩容后的第一个请求都要付冷启动代价，镜像体积直接影响它。
#
# 运行阶段用 `FROM scratch` 而不是 distroless / alpine：
#   - 纯静态 Go 二进制本来就不需要任何基础镜像
#   - 体积与攻击面都到最小，冷启动最快
#   - **消除了一个外部依赖** —— 这台开发机拉不到 gcr.io（TLS 握手超时），
#     用 distroless 会让构建在这里直接卡住
# 代价：容器里没有 shell，无法 exec 进去调试。
#   这与 runbook 的定位一致 —— 排障靠结构化日志与 Cloud Run 的修订版回滚，不靠登机。
#   另外 scratch 没有 /etc/passwd，所以 USER 只能用数字 UID。

# ---------- 构建阶段 ----------
FROM golang:1.25-alpine AS build

# Docker Desktop 会往构建容器注入代理变量，且注入的端口可能与宿主机实际代理不一致，
# 导致 go mod download 失败。声明成 ARG 让构建方可以显式清空：
#   docker build --build-arg HTTP_PROXY= --build-arg HTTPS_PROXY= ...
# （make docker-build 已经这么做了）
ARG HTTP_PROXY
ARG HTTPS_PROXY
ARG http_proxy
ARG https_proxy
ARG GOPROXY=https://proxy.golang.org,direct
ENV GOPROXY=${GOPROXY}

WORKDIR /src

# 先只拷依赖清单，让 go mod download 这层能被缓存 —— 改业务代码不会重新下载依赖。
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# CGO_ENABLED=0 产出纯静态二进制，才能跑在 scratch 上。
# -trimpath 去掉构建机绝对路径（可复现构建，也不泄漏本地目录结构）。
# -ldflags "-s -w" 去掉符号表与调试信息，显著减小体积。
ARG VERSION=dev
RUN CGO_ENABLED=0 GOOS=linux go build \
      -trimpath \
      -ldflags="-s -w -X main.version=${VERSION}" \
      -o /out/bp-api \
      ./cmd/server

# ---------- 运行阶段 ----------
FROM scratch

# CA 根证书：出站调 GCP API、支付网关、邮件服务商都需要校验 TLS。
# scratch 里什么都没有，必须从构建阶段拷过来。
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt

# 非 root 运行。scratch 没有 /etc/passwd，所以只能用数字 UID ——
# 65532 是 distroless 的 nonroot 惯例值，沿用它便于将来切回 distroless。
USER 65532:65532

COPY --from=build /out/bp-api /bp-api

# Cloud Run 通过 PORT 环境变量指定端口，EXPOSE 只是文档作用。
EXPOSE 8080

ENTRYPOINT ["/bp-api"]

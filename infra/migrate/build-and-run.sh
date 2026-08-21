#!/usr/bin/env bash
# 构建 bp-migrate 镜像、推到 Artifact Registry、部署成 Cloud Run Job 并执行。
#
# 事实源：docs/04-ops/deploy.md §6.3（迁移是独立 Job，不在 bp-api 启动时跑）
#        docs/05-adr/0005-database-selection.md（Cloud SQL bp-db，走内建连接器 Unix socket）
#
# 用法：
#   ./infra/migrate/build-and-run.sh            # 构建 + 推 + 部署 Job + 执行 up
#   ./infra/migrate/build-and-run.sh --dry-run
#   ./infra/migrate/build-and-run.sh --step=build|push|deploy|run
#   ./infra/migrate/build-and-run.sh --local   # 本机 docker 构建（默认走 Cloud Build）
#   BP_MIGRATE_CMD=version ./infra/migrate/build-and-run.sh --step=run

set -euo pipefail

readonly EXPECTED_PROJECT_ID="oratis-491316"
readonly REGION="us-central1"
readonly AR_REPO="bp-images"
readonly IMAGE_NAME="bp-migrate"
readonly JOB_NAME="bp-migrate"
readonly SA_API="bp-api-sa"
readonly SQL_INSTANCE="bp-db"
readonly SECRET_DB_URL="bp-database-url"

PROJECT_ID="${PROJECT_ID:-$EXPECTED_PROJECT_ID}"
DRY_RUN=0
STEP="all"
# 默认走 Cloud Build（在 Google 的 amd64 机器上构建），理由见 step_build。
USE_CLOUD_BUILD=1
# 镜像 tag 用 git SHA 而不是时间戳。
#
# 时间戳在每次脚本启动时重算，于是 `--step=build` 与 `--step=push` 分开跑会得到
# **两个不同的 tag**，push 必然报 `tag does not exist`（2026-08-17 实测踩到）。
# git SHA 天然稳定，且能把线上镜像直接追溯到某个提交 —— 排障时这一点比时间戳有用得多。
# 工作区脏时加 -dirty 后缀，避免「这个镜像对应哪份代码」变成悬案。
_git_sha="$(git rev-parse --short HEAD 2>/dev/null || echo nogit)"
if ! git diff --quiet 2>/dev/null || ! git diff --cached --quiet 2>/dev/null; then
  _git_sha="${_git_sha}-dirty"
fi
TAG="${TAG:-$_git_sha}"

die() { printf '\033[31m✗ %s\033[0m\n' "$*" >&2; exit 1; }
log() { printf '%s\n' "$*"; }
step() { printf '\n\033[1m▸ %s\033[0m\n' "$*"; }

run() {
  if [ "$DRY_RUN" -eq 1 ]; then
    printf '  [dry-run]'; printf ' %q' "$@"; printf '\n'
    return 0
  fi
  "$@"
}

usage() {
  sed -n '2,12p' "$0" | sed 's/^# \{0,1\}//'
  exit 0
}

for a in "$@"; do
  case "$a" in
    --dry-run) DRY_RUN=1 ;;
    --local)   USE_CLOUD_BUILD=0 ;;
    --step=*)  STEP="${a#--step=}" ;;
    --project=*) PROJECT_ID="${a#--project=}" ;;
    --tag=*)   TAG="${a#--tag=}" ;;
    -h|--help) usage ;;
    *) die "未知参数：$a" ;;
  esac
done

# 与其余 infra 脚本同一条纪律：项目 ID 写错会把资源建到别人家。
[ "$PROJECT_ID" = "$EXPECTED_PROJECT_ID" ] \
  || die "PROJECT_ID 必须是 $EXPECTED_PROJECT_ID，当前是 $PROJECT_ID"

readonly IMAGE="${REGION}-docker.pkg.dev/${PROJECT_ID}/${AR_REPO}/${IMAGE_NAME}"
REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
readonly REPO_ROOT
readonly CTX="${REPO_ROOT}/infra/migrate"

step_build() {
  step "1/4 构建镜像 ${IMAGE_NAME}:${TAG}"

  # migrations 不在 infra/migrate 下，构建上下文拿不到 —— 先同步过来。
  # 用 rsync 语义的 cp：每次全量覆盖，避免删掉的 migration 残留在镜像里。
  run rm -rf "${CTX}/migrations"
  run mkdir -p "${CTX}/migrations"
  run cp "${REPO_ROOT}/api/db/migrations/"*.sql "${CTX}/migrations/"
  local n
  n="$(find "${REPO_ROOT}/api/db/migrations" -maxdepth 1 -name '*.sql' | wc -l | tr -d ' ')"
  log "  · 已同步 ${n} 个迁移文件"

  # ── 为什么默认走 Cloud Build 而不是本机 docker build ──
  #
  # 本机构建在这台开发机上有两个叠加的坑，2026-08-17 逐个踩过：
  #  1. 本机是 Apple Silicon（arm64），Cloud Run 只接受 amd64/linux，
  #     不加 --platform 会被拒绝：manifest type '…oci.image.index.v1+json'
  #     must support amd64/linux。
  #  2. 加了 --platform 反而更糟：跨平台会强制去 registry 拉对应架构的基础镜像，
  #     而 Docker Hub 在这台机器上不可达（auth.docker.io 返回 EOF）。
  #     试过 docker pull --platform=linux/amd64 mirror.gcr.io/library/golang:1.25-alpine，
  #     在 Docker Desktop 上仍然落回 arm64（inspect 报 Architecture=arm64）。
  #
  # Cloud Build 一次解决两个：它跑在 amd64 上，且 Google 的构建机直连 Docker Hub。
  # 代价是每次要上传构建上下文，以及 Cloud Build 用量（免费额度 120 分钟/天，远用不完）。
  # --local 保留：网络正常且架构匹配的机器上本机构建更快。
  if [ "$USE_CLOUD_BUILD" -eq 1 ]; then
    run gcloud builds submit "$CTX" \
      --project="$PROJECT_ID" \
      --tag="${IMAGE}:${TAG}" \
      --timeout=15m
    run gcloud artifacts docker tags add \
      "${IMAGE}:${TAG}" "${IMAGE}:latest" --project="$PROJECT_ID" --quiet
    return 0
  fi

  # 代理必须显式清空：Docker Desktop 注入的端口与宿主机不一致（local-development.md §2.2）
  # --platform=linux/amd64 不是可选的（deploy-api.sh 同款注释，这里踩过一次才补上）：
  # 本机是 Apple Silicon，不写这个会构建出 arm64 镜像，Cloud Run 直接拒绝部署 ——
  #   Container manifest type 'application/vnd.oci.image.index.v1+json'
  #   must support amd64/linux
  # --provenance=false 是配套的：buildx 默认会额外产出 attestation，
  # 把单平台镜像包成 OCI index，Cloud Run 同样不接受。
  run docker build \
    --platform=linux/amd64 \
    --provenance=false \
    --build-arg HTTP_PROXY= --build-arg HTTPS_PROXY= \
    --build-arg http_proxy= --build-arg https_proxy= \
    --build-arg GOPROXY='https://goproxy.cn,https://proxy.golang.org,direct' \
    -t "${IMAGE}:${TAG}" -t "${IMAGE}:latest" \
    "$CTX"
}

step_push() {
  step "2/4 推送到 Artifact Registry"
  if [ "$USE_CLOUD_BUILD" -eq 1 ]; then
    log "  · Cloud Build 已直接推送，跳过"
    return 0
  fi
  run gcloud auth configure-docker "${REGION}-docker.pkg.dev" --quiet
  run docker push "${IMAGE}:${TAG}"
  run docker push "${IMAGE}:latest"
}

step_deploy() {
  step "3/4 部署 Cloud Run Job"
  local verb=create
  if gcloud run jobs describe "$JOB_NAME" --project="$PROJECT_ID" --region="$REGION" >/dev/null 2>&1; then
    verb=update
    log "  · Job 已存在，改为 update"
  fi

  # --max-retries=0 是硬要求（deploy.md §6.3）：
  # 重试一个改到一半的 schema 比直接失败更糟。
  run gcloud run jobs "$verb" "$JOB_NAME" \
    --project="$PROJECT_ID" --region="$REGION" \
    --image="${IMAGE}:${TAG}" \
    --service-account="${SA_API}@${PROJECT_ID}.iam.gserviceaccount.com" \
    --set-cloudsql-instances="${PROJECT_ID}:${REGION}:${SQL_INSTANCE}" \
    --set-secrets="BP_DATABASE_URL=${SECRET_DB_URL}:latest" \
    --max-retries=0 \
    --task-timeout=10m \
    --memory=512Mi --cpu=1
}

step_run() {
  step "4/4 执行迁移"
  # ⚠️ 两处 bash 陷阱，2026-08-17 实测踩到：
  #  1. `set -u` 下展开**空数组** "${extra[@]}" 会报 unbound variable
  #     （macOS 自带的 bash 3.2 尤其严格）。必须用 "${extra[@]+"${extra[@]}"}"。
  #  2. `[ -n ... ] && arr+=(...)` 在条件为假时整行返回非 0，
  #     若它恰好是函数最后一条语句，`set -e` 会让函数以失败告终。改用 if 更稳。
  local extra=()
  if [ -n "${BP_MIGRATE_CMD:-}" ]; then
    extra+=(--update-env-vars="BP_MIGRATE_CMD=${BP_MIGRATE_CMD}")
  fi
  if [ -n "${BP_MIGRATE_ARG:-}" ]; then
    extra+=(--update-env-vars="BP_MIGRATE_ARG=${BP_MIGRATE_ARG}")
  fi

  run gcloud run jobs execute "$JOB_NAME" \
    --project="$PROJECT_ID" --region="$REGION" \
    --wait "${extra[@]+"${extra[@]}"}"

  log ""
  log "  查看日志："
  log "    gcloud run jobs executions list --job=$JOB_NAME --region=$REGION --project=$PROJECT_ID"
}

log "项目 : $PROJECT_ID"
log "镜像 : ${IMAGE}:${TAG}"
log "步骤 : $STEP"
[ "$DRY_RUN" -eq 1 ] && log "模式 : DRY-RUN"

case "$STEP" in
  all)    step_build; step_push; step_deploy; step_run ;;
  build)  step_build ;;
  push)   step_push ;;
  deploy) step_deploy ;;
  run)    step_run ;;
  *) die "未知步骤：$STEP（可选 all build push deploy run）" ;;
esac

step "完成"

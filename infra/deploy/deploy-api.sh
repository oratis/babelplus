#!/usr/bin/env bash
#
# deploy-api.sh —— 构建 bp-api 镜像并部署到 Cloud Run
#
# 事实源：
#   docs/04-ops/deploy.md §4（镜像）· §5（run deploy 逐参数）· §6（数据库连接）· §7（密钥）
#   docs/05-adr/0005-database-selection.md §6.2（连接数硬公式）· §10.3
#   api/internal/config/config.go（**环境变量名的唯一事实源**）
#   api/Dockerfile（构建参数 VERSION / 代理 ARG）
#
# 默认行为是**不接流量的候选修订版**（--no-traffic --tag=candidate）。
# 切流量要显式 --promote，且会要求二次确认。
#
# 🔴 本脚本刻意**没有**灰度选项。deploy.md §12.1：节点每 60 秒轮询一次，
#    10% 的流量切分意味着同一个节点在相邻两次轮询里可能拿到两个不同版本的响应，
#    改动 UniProxy 响应体或 ETag 计算方式时会造成节点反复失效缓存。
#    一律 0% → 验证 → 100%。

set -euo pipefail

# ───────────────────────── 防呆常量 ─────────────────────────

readonly EXPECTED_PROJECT_ID="oratis-491316"
readonly REGION="us-central1"
readonly SERVICE="bp-api"
readonly AR_REPO="bp-images"
readonly AR_HOST="us-central1-docker.pkg.dev"
readonly SA_API="bp-api-sa"
readonly SQL_INSTANCE="bp-db"

# Secret 名 → 环境变量名。**必须与 setup-infra.sh 的 SECRET_* 和
# api/internal/config/config.go 的 required 表三方一致。**
readonly SECRET_MOUNTS="\
BP_DATABASE_URL=bp-database-url:latest,\
BP_SUBSCRIPTION_TOKEN_PEPPER=bp-sub-token-pepper:latest,\
BP_NODE_KEY_PEPPER=bp-node-token-pepper:latest,\
BP_SESSION_SIGNING_KEY=bp-jwt-signing-key:latest"

# ───────────────────────── 运行时开关 ─────────────────────────

PROJECT_ID="${PROJECT_ID:-$EXPECTED_PROJECT_ID}"
DRY_RUN=0
ASSUME_YES=0
DO_BUILD=1
DO_PROMOTE=0
ALLOW_DIRTY=0
TAG=""

# ───────────────────────── 通用工具（与 infra/ 下其它脚本刻意保持重复，见 setup-infra.sh 的说明）─────────────────────────

log()  { printf '%s\n' "$*" >&2; }
step() { printf '\n\033[1m▸ %s\033[0m\n' "$*" >&2; }
ok()   { printf '  ✓ %s\n' "$*" >&2; }
skip() { printf '  · %s\n' "$*" >&2; }
warn() { printf '  ⚠ %s\n' "$*" >&2; }
die()  { printf '\n\033[31m✗ %s\033[0m\n' "$*" >&2; exit 1; }

# qq 打印一个参数：只在需要时加单引号。
# 不用 printf '%q' —— 它会把中文转成八进制转义（\346\216\247…），而 dry-run 的输出是给人读的。
qq() {
  case "$1" in
    ''|*[!A-Za-z0-9_@%+=:,./~-]*)
      printf "'%s'" "$(printf '%s' "$1" | sed "s/'/'\\\\''/g")" ;;
    *) printf '%s' "$1" ;;
  esac
}


usage() {
  cat <<'EOF'
用法: deploy-api.sh [选项]

构建 bp-api 镜像 → 推 Artifact Registry → 部署到 Cloud Run。
默认部署成**不接流量**的候选修订版（--tag=candidate），验证通过后再 --promote。

选项:
  --tag=<sha>     镜像 tag。默认取 git rev-parse --short=7 HEAD
  --no-build      跳过构建，直接部署已在仓库里的该 tag（回补一次失败的部署时用）
  --promote       部署后把 100% 流量切到新修订版。会要求二次确认
  --allow-dirty   允许工作区有未提交改动时构建。⚠️ 会让 tag 与 commit 不对应
  --project=<id>  GCP 项目 ID。**必须是 oratis-491316**
  --dry-run       只打印将要执行的命令，不做任何写操作
  --yes           跳过交互确认
  -h, --help      显示本帮助

两段式发布（推荐）:
  ./infra/scripts/verify-isolation.sh                    # 1. 部署前基线
  ./infra/deploy/deploy-api.sh                           # 2. 起候选，不接流量
  curl -sS https://candidate---<服务URL>/healthz          # 3. 验证候选
  ./infra/deploy/deploy-api.sh --no-build --promote      # 4. 切 100%
  ./infra/scripts/verify-isolation.sh                    # 5. 部署后核对

回滚:
  ./infra/deploy/rollback.sh --list
  ./infra/deploy/rollback.sh --to=<修订版名>
EOF
}

run() {
  if [ "$DRY_RUN" -eq 1 ]; then
    local _a
    printf '  [dry-run] ' >&2
    for _a in "$@"; do qq "$_a" >&2; printf ' ' >&2; done
    printf '\n' >&2
    return 0
  fi
  "$@"
}

confirm() {
  local prompt="$1" expect="$2" answer=""
  if [ "$DRY_RUN" -eq 1 ]; then
    skip "[dry-run] 跳过确认：$prompt"
    return 0
  fi
  if [ "$ASSUME_YES" -eq 1 ]; then
    warn "--yes 已跳过确认：$prompt"
    return 0
  fi
  printf '\n%s\n请输入 %s 确认（其它任何输入都会中止）：' "$prompt" "$expect" >&2
  read -r answer
  if [ "$answer" != "$expect" ]; then
    die "确认失败，已中止。"
  fi
}

guard_project() {
  if [ "$PROJECT_ID" != "$EXPECTED_PROJECT_ID" ]; then
    die "PROJECT_ID 必须是 $EXPECTED_PROJECT_ID，当前是 \"$PROJECT_ID\"。"
  fi
}

guard_bp_prefix() {
  local name
  for name in "$@"; do
    case "$name" in
      bp-*|bp_*) : ;;
      *) die "资源名 \"$name\" 不带 bp- 前缀，违反 as-built §2.1 第 1 条。" ;;
    esac
  done
}

need_cmd() {
  command -v "$1" >/dev/null 2>&1 || die "缺少命令：$1"
}

# ───────────────────────── 前置检查 ─────────────────────────
#
# fail-closed：任何一项缺失都在**构建之前**报错。
# bp-api 自己的 config 包也是 fail-closed（缺环境变量拒绝启动），
# 但那时镜像已经推上去、修订版已经建出来了 —— 在这里挡住更便宜。

preflight() {
  step "前置检查"

  if ! gcloud iam service-accounts describe "${SA_API}@${PROJECT_ID}.iam.gserviceaccount.com" \
         --project="$PROJECT_ID" >/dev/null 2>&1; then
    die "服务账号 ${SA_API} 不存在。先跑 ./infra/deploy/setup-infra.sh --step=iam。
     🔴 绝不要退而求其次用 Compute 默认 SA（as-built §5：权限过大且被现有工作负载共用）。"
  fi
  ok "服务账号 $SA_API 存在"

  if ! gcloud sql instances describe "$SQL_INSTANCE" --project="$PROJECT_ID" >/dev/null 2>&1; then
    die "Cloud SQL 实例 ${SQL_INSTANCE} 不存在。先跑 ./infra/deploy/setup-infra.sh --step=sql。"
  fi
  ok "Cloud SQL 实例 $SQL_INSTANCE 存在"

  if ! gcloud artifacts repositories describe "$AR_REPO" \
         --project="$PROJECT_ID" --location="$REGION" >/dev/null 2>&1; then
    die "Artifact Registry 仓库 ${AR_REPO} 不存在。先跑 ./infra/deploy/setup-infra.sh --step=registry。
     🔴 不要改用 cloud-run-source-deploy —— 那是现有三个服务的镜像仓库（deploy.md §1 第 1 条）。"
  fi
  ok "Artifact Registry 仓库 $AR_REPO 存在"

  # 四个 secret 缺一个，实例就会因为 config 的 fail-closed 起不来。
  local pair name
  while IFS= read -r pair; do
    [ -n "$pair" ] || continue
    name="${pair#*=}"      # bp-xxx:latest
    name="${name%%:*}"     # bp-xxx
    guard_bp_prefix "$name"
    if ! gcloud secrets describe "$name" --project="$PROJECT_ID" >/dev/null 2>&1; then
      die "secret ${name} 不存在。先跑 ./infra/deploy/setup-infra.sh --step=secrets（以及 --step=sql）。"
    fi
  done <<EOF
$(printf '%s' "$SECRET_MOUNTS" | tr ',' '\n')
EOF
  ok "4 个 secret 均存在"
}

# ───────────────────────── 构建 ─────────────────────────

resolve_tag() {
  if [ -n "$TAG" ]; then
    return 0
  fi
  need_cmd git
  if ! git -C "$ROOT" rev-parse --git-dir >/dev/null 2>&1; then
    die "不在 git 仓库里且没给 --tag=<sha>，无法确定镜像 tag。"
  fi
  TAG="$(git -C "$ROOT" rev-parse --short=7 HEAD)"

  if [ -n "$(git -C "$ROOT" status --porcelain)" ] && [ "$ALLOW_DIRTY" -eq 0 ]; then
    die "工作区有未提交改动，而 --revision-suffix 会把修订版命名成 ${SERVICE}-${TAG}。
     这会让「线上跑的是哪个 commit」这个问题**答不出来** —— 而排障时它是第一个要问的。
     先提交，或显式加 --allow-dirty。"
  fi
}

build_and_push() {
  step "构建镜像 $IMAGE"
  need_cmd docker

  # --platform=linux/amd64 不是可选的：在 Apple Silicon 上不写这个会推上去一个
  # arm64 镜像，Cloud Run 拉起来直接 exec format error（deploy.md §4.2）。
  #
  # 代理三连：Docker Desktop 会往构建容器注入代理变量，且注入的端口可能与宿主机
  # 实际代理不一致，导致 go mod download 在构建容器里失败。api/Dockerfile 把它们
  # 声明成 ARG 就是为了让这里能显式清空（api/Makefile 的 docker-build 同样处理）。
  run docker build \
    --platform=linux/amd64 \
    --build-arg "VERSION=${TAG}" \
    --build-arg HTTP_PROXY= --build-arg HTTPS_PROXY= \
    --build-arg http_proxy= --build-arg https_proxy= \
    --build-arg 'GOPROXY=https://goproxy.cn,https://proxy.golang.org,direct' \
    -t "$IMAGE" \
    "$ROOT/api"
  ok "镜像已构建"

  # 推送需要 docker 认得 Artifact Registry 的凭据助手。
  # 本脚本**不替你改 $HOME/.docker/config.json** —— 那是本机配置，应当由你自己决定。
  if ! grep -q "$AR_HOST" "${HOME}/.docker/config.json" 2>/dev/null; then
    warn "${HOME}/.docker/config.json 里没看到 ${AR_HOST} 的凭据助手。
     若推送报 denied，先手工跑一次（它会修改你本机的 docker 配置）：
       gcloud auth configure-docker ${AR_HOST}"
  fi

  run docker push "$IMAGE"
  ok "镜像已推送"
}

# ───────────────────────── 部署 ─────────────────────────

deploy() {
  step "部署 $SERVICE（修订版 ${SERVICE}-${TAG}）"

  local conn="${PROJECT_ID}:${REGION}:${SQL_INSTANCE}"

  # 环境变量。⚠️ --set-env-vars 与 --set-secrets 都是**全量替换**语义
  # （deploy.md §7 末尾）：漏写一项就是静默删除一项，所以这里每次列全。
  # 只想改一项时用 --update-env-vars / --update-secrets，不要改这里。
  #
  # PORT 不在列表里 —— Cloud Run 自己注入，手工设会冲突。
  # BP_ALLOWED_ORIGINS：CORS 白名单，**prod 下 config 是 fail-closed 的**——
  # 不设这一项，容器会拒绝启动（config.parseAllowedOrigins）。
  # 2026-08-17 首次部署前发现漏了它：部署会「成功」，但修订版永远起不来。
  #
  # 值取自 ALLOWED_ORIGINS 环境变量，默认是 system-design §4.1 规划的 Web 域名。
  # ⚠️ 这些域名**目前还不存在**（web 尚未部署），所以现在填的是「将来会是什么」而不是
  #    「现在是什么」。真实域名池定下来后必须回来改 —— 域名池是会轮换的，
  #    每加一个镜像域名都要同步进这里，否则那个域名下的面板调不通 API。
  local origins="${ALLOWED_ORIGINS:-https://web.babel.plus,https://admin.babel.plus}"

  # ⚠️ 用 gcloud 的**自定义分隔符**语法 `^@^k=v@k=v`，不能用默认的逗号。
  # BP_ALLOWED_ORIGINS 的值本身就含逗号（多个 Origin），用默认分隔符会被切错：
  #   ERROR: argument --set-env-vars: Bad syntax for dict arg: [https://admin.babel.plus]
  # 2026-08-17 首次部署实测踩到。选 @ 是因为它不会出现在 URL 与我们的任何值里。
  local env_vars="^@^\
BP_ENV=prod@\
BP_GCP_PROJECT_ID=${PROJECT_ID}@\
BP_DB_MAX_CONNS=2@\
BP_LOG_LEVEL=info@\
BP_TRUST_PROXY_HEADERS=true@\
BP_ALLOWED_ORIGINS=${origins}"

  local -a args=(
    gcloud run deploy "$SERVICE"
    --project="$PROJECT_ID"
    --region="$REGION"
    --image="$IMAGE"
    --revision-suffix="$TAG"

    # 运行时身份。🔴 绝不用 Compute 默认 SA（as-built §5）。
    --service-account="${SA_API}@${PROJECT_ID}.iam.gserviceaccount.com"

    # 🔴 Cloud SQL 走 Cloud Run **内建连接器**注入的 Unix socket：
    #    连接自动加密、走 Google 内部网络、不需要给数据库开任何 authorized network、
    #    **不碰 default 网络**（default 正是 vpn-us / vpn-jp 所在的网络）、成本 $0。
    #    对应地，下面**没有** --vpc-connector / --network / --subnet，且必须继续没有。
    --add-cloudsql-instances="$conn"

    # 鉴权在应用层（节点面 / 用户面 / 客户端面 / 管理面 / 内部面五条互不共享的中间件链）。
    # 节点、代理客户端、Cloud Tasks 都是**公网**调用方，IAM 级鉴权在这里不适用。
    --allow-unauthenticated
    --ingress=all

    # 🔴 --max-instances=8 是本命令里唯一一个**属于数据库而不属于 Cloud Run** 的参数。
    #    硬公式（ADR 0005 §6.2）：max_instances × pool_max + 运维预留 ≤ max_connections − 3
    #    代入 db-f1-micro：8 × 2 + 6 = 22 ≤ 25 − 3 ✓
    #    改它必须同时改 BP_DB_MAX_CONNS，并重算这条公式。
    #    缺省更糟：Cloud Run 默认上限 100 × 2 = 200 连接 → FATAL: sorry, too many clients already。
    --max-instances=8

    # min-instances=0：节点每 60 秒轮询本身就是保温器（2 节点 = 每 7.5 秒一个请求），
    # 而 min-instances=1 = 2,628,000 vCPU-s/月，是免费额度的 14.6 倍。
    # 复审触发条件：startup_latencies P95 > 3 秒且冷启动落在用户请求上（deploy.md §5.2）。
    --min-instances=0

    # 并发 80 是 Cloud Run 默认值，沿用。我们的请求以「一次主键查 + 返 304」为主。
    # ⚠️ 调**低**它等于变相调**高**实例数，会直接撞上面那条连接数天花板。
    --concurrency=80

    # 1 vCPU 是能拿到 request-based 计费的最小完整核；512Mi 对「Go 静态二进制 + 2 连接池」很宽裕。
    --cpu=1
    --memory=512Mi

    # 必须 ≥ 最慢的 /internal/tasks/*。不要拉到 3600 —— 实例总数只有 8，
    # 一个卡住的请求就吃掉 12.5% 的容量。真有超过 120 秒的聚合就拆成 Cloud Run Job。
    --timeout=120

    # 启动期给额外 CPU 缩短冷启动。计费细节 **待核实**，先开着等基线数据。
    --cpu-boost

    --set-env-vars="$env_vars"
    --set-secrets="$SECRET_MOUNTS"
  )

  # 🔴 下列参数**没有出现在上面，且必须继续没有**（deploy.md §1）：
  #    --source            会自动复用 cloud-run-source-deploy（现有三个服务的镜像仓库）
  #    --vpc-connector / --network / --subnet   会碰 default 网络（vpn-us / vpn-jp 所在）
  #    --no-cpu-throttling 把计费从 request-based 切到 instance-based，成本模型当场作废

  if [ "$DO_PROMOTE" -eq 1 ]; then
    confirm "将把 100% 流量切到新修订版 ${SERVICE}-${TAG}。
  当前修订版会立刻停止接收新请求。
  ⚠️ 若本次发布改了 DB schema，请记住：**代码能秒级回滚，schema 不能**（deploy.md §12.3）。" "promote"
    ok "流量：新修订版 100%"
  else
    args+=(--no-traffic --tag=candidate)
    ok "流量：0%（候选修订版，tag=candidate）"
  fi

  run "${args[@]}"

  step "部署完成"
  local url
  url="$(gcloud run services describe "$SERVICE" --project="$PROJECT_ID" --region="$REGION" \
          --format='value(status.url)' 2>/dev/null || true)"
  if [ -n "$url" ]; then
    log "  服务 URL      : $url"
    if [ "$DO_PROMOTE" -eq 1 ]; then
      log "  验证          : curl -sS ${url}/healthz   # 确认 revision 是 ${TAG}"
      log "  就绪（查库）  : curl -sS ${url}/readyz"
    else
      # 候选修订版的可访问地址是 https://<tag>---<服务主机名>
      log "  候选 URL      : https://candidate---${url#https://}"
      log "  验证后切流量  : ./infra/deploy/deploy-api.sh --no-build --tag=${TAG} --promote"
    fi
  fi
  log ""
  log "  ⚠️ 现在跑一次 ./infra/scripts/verify-isolation.sh —— 这是「不影响已部署服务」这条承诺的可执行形式。"
  log "  ⚠️ /healthz **不查数据库**（控制面故障不得升级为数据面故障）；要确认库连得上看 /readyz。"
}

# ───────────────────────── 主流程 ─────────────────────────

main() {
  local arg
  for arg in "$@"; do
    case "$arg" in
      --tag=*)     TAG="${arg#*=}" ;;
      --no-build)  DO_BUILD=0 ;;
      --promote)   DO_PROMOTE=1 ;;
      --allow-dirty) ALLOW_DIRTY=1 ;;
      --project=*) PROJECT_ID="${arg#*=}" ;;
      --dry-run)   DRY_RUN=1 ;;
      --yes)       ASSUME_YES=1 ;;
      -h|--help)   usage; exit 0 ;;
      *)           usage >&2; die "未知参数：$arg" ;;
    esac
  done

  guard_project
  guard_bp_prefix "$SERVICE" "$AR_REPO" "$SA_API" "$SQL_INSTANCE"
  need_cmd gcloud

  ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
  resolve_tag
  IMAGE="${AR_HOST}/${PROJECT_ID}/${AR_REPO}/${SERVICE}:${TAG}"

  log "项目   : $PROJECT_ID"
  log "服务   : $SERVICE @ $REGION"
  log "镜像   : $IMAGE"
  if [ "$DRY_RUN" -eq 1 ]; then
    log "模式   : DRY-RUN（只打印写操作）"
  fi

  preflight
  if [ "$DO_BUILD" -eq 1 ]; then
    build_and_push
  else
    skip "--no-build：跳过构建，直接部署 $IMAGE"
  fi
  deploy
}

ROOT=""
IMAGE=""

main "$@"

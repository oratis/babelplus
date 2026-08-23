#!/usr/bin/env bash
#
# setup-infra.sh —— babel.plus 控制面在 GCP 上的**一次性**资源创建
#
# 事实源：
#   docs/04-ops/deploy.md §3（初始化）· §7（密钥）· §8（Scheduler）· §9（Cloud Tasks）
#   docs/05-adr/0005-database-selection.md §10（Cloud SQL 参数，一字不改）
#   docs/02-architecture/as-built-gcp.md §2.1（隔离承诺）· §5（不复用 Compute 默认 SA）· §6（未启用的 API）
#   openapi/openapi.yaml（/internal/tasks/* 的**路径与频率以契约为准**，见下方 SCHEDULER 表的注释）
#   docs/04-ops/monitoring.md §4（告警通道：Pub/Sub + email 双通道）
#
# 三条贯穿全文的纪律：
#   1. 只**新增** bp- 前缀资源。本脚本没有任何 delete / update 既有资源的调用。
#   2. 每条 gcloud 都显式写 --project，不依赖 `gcloud config set project`
#      （deploy.md §2：「打错项目是本文最现实的事故源」）。
#   3. 幂等。每一步先探测再创建，已存在就跳过，可以反复跑。
#
# 凭据处理：密码与 pepper 由 openssl 现场生成，**只存在于内存变量与管道里**，
# 通过 `--data-file=-` 直接喂给 Secret Manager，不落任何文件、不写 shell history。
# 唯一的残留见 §step_sql 里对 `gcloud sql users create --password` 的注释。

set -euo pipefail

# ───────────────────────── 防呆常量（不接受命令行覆盖以外的任何来源）─────────────────────────

readonly EXPECTED_PROJECT_ID="oratis-491316"
readonly REGION="us-central1"

# 资源名。全部 bp- 前缀（as-built §2.1 第 1 条）。
readonly AR_REPO="bp-images"           # 独立仓库，**不混用** cloud-run-source-deploy（deploy.md §1 第 1 条）
readonly SA_API="bp-api-sa"            # Cloud Run 运行时身份
readonly SA_DEPLOY="bp-deploy-sa"      # CI / 构建身份
readonly SA_TASKS="bp-tasks-sa"        # Scheduler / Tasks / Pub-Sub push 的 OIDC 主体
readonly SQL_INSTANCE="bp-db"
readonly SQL_DATABASE="bp"
readonly SQL_USER="bp_app"             # Postgres 角色名用下划线：带连字符的角色名在每条 SQL 里都要加引号
readonly RUN_SERVICE="bp-api"
readonly QUEUE_TRAFFIC="bp-traffic-ingest"
readonly QUEUE_MAIL="bp-mail-send"
readonly PUBSUB_TOPIC="bp-alerts"
readonly PUBSUB_SUB="bp-alerts-relay"
readonly DUMP_JOB="bp-db-dump"

# Secret 清单：只创建**已经有消费者**的四个。
# 名字右边是 api/internal/config/config.go 里对应的环境变量 —— 这张对应关系
# 必须与 deploy-api.sh 的 --set-secrets 完全一致，改一处就要改两处。
#   bp-database-url        → BP_DATABASE_URL              （含密码，故整串进 Secret Manager）
#   bp-sub-token-pepper    → BP_SUBSCRIPTION_TOKEN_PEPPER
#   bp-node-token-pepper   → BP_NODE_KEY_PEPPER
#   bp-jwt-signing-key     → BP_SESSION_SIGNING_KEY
# deploy.md §7.1 还列了 bp-mail-api-key / bp-mail-api-key-backup / bp-payment-webhook-secret，
# 本脚本**不建**：ESP（ADR 0002 §7）与支付商都未选型，建一个占位值的 secret
# 只会让「secret 存在」这件事失去信号价值，将来还得先删再建。

readonly SECRET_DB_URL="bp-database-url"
readonly SECRET_SUB_PEPPER="bp-sub-token-pepper"
readonly SECRET_NODE_PEPPER="bp-node-token-pepper"
readonly SECRET_SESSION_KEY="bp-jwt-signing-key"

# 需要启用的 API。as-built §6 记录 run/artifactregistry/cloudbuild/secretmanager/iam/
# logging/monitoring/pubsub 已启用，下面三个**未启用**，必须显式开。
# dns 与 redis 保持未启用（DNS 在 Cloudflare、缓存用 Postgres UNLOGGED 表，ADR 0005 §8）。
REQUIRED_APIS=(
  sqladmin.googleapis.com
  cloudscheduler.googleapis.com
  cloudtasks.googleapis.com
)

# ───────────────────────── 运行时开关 ─────────────────────────

PROJECT_ID="${PROJECT_ID:-$EXPECTED_PROJECT_ID}"
DRY_RUN=0
ASSUME_YES=0
STEP="all"

# ───────────────────────── 通用工具 ─────────────────────────
#
# 下面这段守卫在 infra/ 的每个脚本里都重复了一遍，这是**故意的**：
# 每个脚本都必须能单独 scp 出去执行，且单独具备「打错项目就拒绝运行」的能力。
# 抽成公共库会让守卫的存在依赖于另一个文件是否被一起拷走。

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
用法: setup-infra.sh [选项]

一次性创建 babel.plus 控制面的 GCP 资源。只新增 bp- 前缀资源，幂等，可反复跑。

选项:
  --step=<名称>   只跑一步。可选：apis registry iam secrets sql pubsub tasks postdeploy
                  默认 all（按上面的顺序全跑一遍）
  --project=<id>  GCP 项目 ID。**必须是 oratis-491316**，写别的会直接拒绝
  --dry-run       只打印将要执行的命令，不做任何写操作（只读探测仍会真的发生）
  --yes           跳过交互确认。⚠️ 只在你确认过 --dry-run 输出之后用
  -h, --help      显示本帮助

步骤说明:
  apis        启用 sqladmin / cloudscheduler / cloudtasks（as-built §6：这三个当前未启用）
  registry    创建 Artifact Registry 仓库 bp-images（不混用 cloud-run-source-deploy）
  iam         创建 bp-api-sa / bp-deploy-sa / bp-tasks-sa 与最小权限绑定
  secrets     生成并写入 4 个 bp- 前缀 secret（值只经内存与管道，不落盘）
  sql         创建 Cloud SQL 实例 bp-db、数据库 bp、用户 bp_app，并合成 DSN 写入 secret
  pubsub      创建告警 topic bp-alerts
  tasks       创建 Cloud Tasks 队列 bp-traffic-ingest / bp-mail-send
  postdeploy  ⚠️ **必须在 deploy-api.sh 首次部署之后跑**：
              Scheduler 任务、Pub/Sub push 订阅、以及依赖 bp-api 存在的 IAM 绑定

典型顺序:
  ./infra/scripts/verify-isolation.sh                 # 部署前基线
  ./infra/deploy/setup-infra.sh --dry-run             # 先看一遍要做什么
  ./infra/deploy/setup-infra.sh                       # 建到 tasks 为止
  ./infra/deploy/deploy-api.sh --promote              # 首次部署 bp-api
  ./infra/deploy/setup-infra.sh --step=postdeploy     # 补上依赖 bp-api 的部分
  ./infra/scripts/verify-isolation.sh                 # 部署后核对
EOF
}

# run 是全部**写操作**的唯一入口。--dry-run 时只打印不执行。
#
# 🔴 打印时必须遮蔽 --password=：dry-run 的输出会进终端回滚缓冲、会被复制进工单、
#    会被 `script`/CI 记进日志。一个「只是预演」的命令把明文密码写进日志，
#    比真的执行还糟糕 —— 因为没人会去清理预演的输出。
run() {
  if [ "$DRY_RUN" -eq 1 ]; then
    local a
    printf '  [dry-run] ' >&2
    for a in "$@"; do
      case "$a" in
        --password=*) printf '%s ' '--password=***(已遮蔽)' >&2 ;;
        *)            qq "$a" >&2; printf ' ' >&2 ;;
      esac
    done
    printf '\n' >&2
    return 0
  fi
  "$@"
}

# confirm 要求手工键入一个特定字符串。用 read -r 且不回显任何凭据。
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
    die "确认失败，已中止。未做任何修改。"
  fi
}

# guard_project 是本脚本最重要的一行防呆。
guard_project() {
  if [ "$PROJECT_ID" != "$EXPECTED_PROJECT_ID" ]; then
    die "PROJECT_ID 必须是 $EXPECTED_PROJECT_ID，当前是 \"$PROJECT_ID\"。
     本仓库的全部资产清点、隔离承诺与 IAM 设计都只对这一个项目成立
     （docs/02-architecture/as-built-gcp.md）。要部署到别的项目请先写一份新的 as-built。"
  fi
}

# guard_bp_prefix 保证脚本里出现的每一个被创建资源名都带 bp 前缀。
# 允许 bp- 与 bp_ 两种：GCP 资源用 bp-，Postgres 角色用 bp_（见 SQL_USER 的注释）。
guard_bp_prefix() {
  local name
  for name in "$@"; do
    case "$name" in
      bp-*|bp_*) : ;;
      *) die "资源名 \"$name\" 不带 bp- 前缀，违反 as-built §2.1 第 1 条的命名前缀隔离。" ;;
    esac
  done
}

need_cmd() {
  command -v "$1" >/dev/null 2>&1 || die "缺少命令：$1"
}

# ───────────────────────── 步骤 ─────────────────────────

step_apis() {
  step "1/8 启用 API"
  local enabled api
  enabled="$(gcloud services list --enabled --project="$PROJECT_ID" --format='value(config.name)' 2>/dev/null || true)"
  for api in "${REQUIRED_APIS[@]}"; do
    if printf '%s\n' "$enabled" | grep -qx "$api"; then
      skip "$api 已启用"
      continue
    fi
    # 启用 API 本身不改动任何现有资源（ADR 0005 §10.1），但 sqladmin 会解锁一类
    # 会产生持续月度支出的资源，所以仍然确认一次。
    local note=""
    if [ "$api" = "sqladmin.googleapis.com" ]; then
      note="
  ⚠️ 这一个会解锁 Cloud SQL —— 后续 --step=sql 建实例时开始产生约 \$9.53/月 的支出。"
    fi
    confirm "将启用 ${api}（新增一个 API，不改动任何现有资源）${note}" "enable"
    run gcloud services enable "$api" --project="$PROJECT_ID"
    ok "$api 已启用"
  done
  skip "dns.googleapis.com 与 redis.googleapis.com 保持未启用（ADR 0005 §8 / §10.1）"
}

step_registry() {
  # 前置依赖：本步要给 bp-deploy-sa 授仓库级 writer，所以它必须已经存在。
  # 单独跑 --step=registry 而没先跑 --step=iam 时，这里给出可操作的报错，
  # 而不是让 gcloud 抛一句 INVALID_ARGUMENT 就完了。
  if [ "$DRY_RUN" -eq 0 ] && ! gcloud iam service-accounts describe \
      "${SA_DEPLOY}@${PROJECT_ID}.iam.gserviceaccount.com" \
      --project="$PROJECT_ID" >/dev/null 2>&1; then
    die "服务账号 ${SA_DEPLOY} 不存在。registry 步骤依赖它 —— 先跑：
     ./infra/deploy/setup-infra.sh --step=iam"
  fi

  step "3/8 Artifact Registry"
  guard_bp_prefix "$AR_REPO"
  if gcloud artifacts repositories describe "$AR_REPO" \
       --project="$PROJECT_ID" --location="$REGION" >/dev/null 2>&1; then
    skip "仓库 $AR_REPO 已存在"
  else
    run gcloud artifacts repositories create "$AR_REPO" \
      --project="$PROJECT_ID" \
      --repository-format=docker \
      --location="$REGION" \
      --description="babel.plus 控制面镜像（勿与 cloud-run-source-deploy 混用）"
    ok "仓库 $AR_REPO 已创建"
  fi

  # 🔴 分仓不是洁癖，是**清理策略的爆炸半径**：清理策略按仓库配置，
  #    而 cloud-run-source-deploy 里放的是 anthropic-relay / lisa-cloud / lisa-web 的镜像
  #    （as-built §4）。一次配错的清理策略会删掉别人的镜像。
  warn "清理策略（deploy.md §3.2 的 ar-cleanup.json）本脚本**不配**：
     set-cleanup-policies 的子命令名与 JSON schema 在 gcloud 版本间变动过（deploy.md 标 待核实），
     且它是唯一能删镜像的配置项，必须人工带 --dry-run 跑一次看清楚再落。"

  # AR 的写权限**只授到这一个仓库**，不在项目级授 roles/artifactregistry.writer。
  # 理由与 deploy.md §1 第 2 条（逐 secret 授权）完全同构：项目级会一并覆盖
  # cloud-run-source-deploy，等于把现有服务的镜像仓库交给 babel.plus 的 CI。
  # 这是本脚本对 deploy.md §3.3 的一处**收紧**，差异见 infra/deploy/README.md。
  run gcloud artifacts repositories add-iam-policy-binding "$AR_REPO" \
    --project="$PROJECT_ID" --location="$REGION" \
    --member="serviceAccount:${SA_DEPLOY}@${PROJECT_ID}.iam.gserviceaccount.com" \
    --role=roles/artifactregistry.writer
  ok "$SA_DEPLOY 已获得 $AR_REPO 的 writer（仓库级，非项目级）"
}

step_iam() {
  step "2/8 服务账号与 IAM"
  guard_bp_prefix "$SA_API" "$SA_DEPLOY" "$SA_TASKS"

  local sa
  for sa in "$SA_API" "$SA_DEPLOY" "$SA_TASKS"; do
    if gcloud iam service-accounts describe "${sa}@${PROJECT_ID}.iam.gserviceaccount.com" \
         --project="$PROJECT_ID" >/dev/null 2>&1; then
      skip "服务账号 $sa 已存在"
    else
      run gcloud iam service-accounts create "$sa" \
        --project="$PROJECT_ID" --display-name="babel.plus $sa"
      ok "服务账号 $sa 已创建"
    fi
  done

  # 🔴 绝不复用 2360090741-compute@developer.gserviceaccount.com（as-built §5）：
  #    它被现有工作负载共用且权限过大，用它跑 bp-api 等于把 babel.plus 的爆炸半径接到 lisa-* 上。

  # roles/cloudsql.client 在 GCP 里**只能项目级授**（没有实例级的等价角色）。
  # 它的权限是「可以经连接器连 Cloud SQL」，不含读数据的权限，项目内也没有别的 SQL 实例，
  # 所以项目级在这里可接受 —— 这是本脚本唯一一处项目级绑定。
  run gcloud projects add-iam-policy-binding "$PROJECT_ID" \
    --member="serviceAccount:${SA_API}@${PROJECT_ID}.iam.gserviceaccount.com" \
    --role=roles/cloudsql.client \
    --condition=None
  ok "$SA_API ← roles/cloudsql.client（项目级，见上方注释）"

  # 部署身份要能「以 bp-api-sa 的身份部署」，但不该能读生产 secret。
  run gcloud iam service-accounts add-iam-policy-binding \
    "${SA_API}@${PROJECT_ID}.iam.gserviceaccount.com" \
    --project="$PROJECT_ID" \
    --member="serviceAccount:${SA_DEPLOY}@${PROJECT_ID}.iam.gserviceaccount.com" \
    --role=roles/iam.serviceAccountUser
  ok "$SA_DEPLOY ← 对 $SA_API 的 serviceAccountUser"

  # Pub/Sub push 订阅要用 bp-tasks-sa 签 OIDC token，Pub/Sub 的服务代理必须能
  # 为它签 token。服务代理邮箱形如 service-<项目号>@gcp-sa-pubsub.iam.gserviceaccount.com。
  local project_number
  project_number="$(gcloud projects describe "$PROJECT_ID" --format='value(projectNumber)' 2>/dev/null || true)"
  if [ -z "$project_number" ]; then
    warn "取不到项目号，跳过 Pub/Sub 服务代理的 tokenCreator 绑定（--dry-run 或未鉴权时正常）"
  else
    run gcloud iam service-accounts add-iam-policy-binding \
      "${SA_TASKS}@${PROJECT_ID}.iam.gserviceaccount.com" \
      --project="$PROJECT_ID" \
      --member="serviceAccount:service-${project_number}@gcp-sa-pubsub.iam.gserviceaccount.com" \
      --role=roles/iam.serviceAccountTokenCreator
    ok "Pub/Sub 服务代理 ← 对 $SA_TASKS 的 serviceAccountTokenCreator"
  fi

  skip "roles/run.invoker 与 roles/run.developer 依赖 bp-api 存在，放在 --step=postdeploy"
}

# new_secret <secret 名> —— 值从 stdin 读，直接进 Secret Manager，不落盘。
new_secret() {
  local name="$1"
  guard_bp_prefix "$name"
  if gcloud secrets describe "$name" --project="$PROJECT_ID" >/dev/null 2>&1; then
    # 已存在就**不覆盖**。轮换是另一件事（有各自的双密钥并存流程，deploy.md §7.1），
    # 不能由一个「初始化脚本」顺手完成。
    skip "secret $name 已存在（不覆盖；轮换请走 deploy.md §7.1 的流程）"
    cat >/dev/null   # 把上游管道里的值吞掉，避免 SIGPIPE
    return 0
  fi
  if [ "$DRY_RUN" -eq 1 ]; then
    cat >/dev/null
    printf '  [dry-run] gcloud secrets create %s --data-file=- (值经管道，不落盘)\n' "$name" >&2
    printf '  [dry-run] gcloud secrets add-iam-policy-binding %s (accessor → %s)\n' "$name" "$SA_API" >&2
    return 0
  fi

  # --replication-policy=user-managed --locations=us-central1：与 bp-api / bp-db 同区
  # （ADR 0005 §9 的数据驻留结论）。
  gcloud secrets create "$name" --project="$PROJECT_ID" \
    --replication-policy=user-managed --locations="$REGION" --data-file=-

  # 🔴 **逐 secret 授权，绝不在项目级授 roles/secretmanager.secretAccessor**（deploy.md §1 第 2 条）：
  #    项目级会把 anthropic-api-key 与 relay-token（as-built §5，属于**现有服务**）
  #    一并交给 bp-api-sa。这是软隔离下最容易犯、后果最严重的一个 IAM 错误。
  gcloud secrets add-iam-policy-binding "$name" --project="$PROJECT_ID" \
    --member="serviceAccount:${SA_API}@${PROJECT_ID}.iam.gserviceaccount.com" \
    --role=roles/secretmanager.secretAccessor >/dev/null
  ok "secret $name 已创建，并逐 secret 授权给 $SA_API"
}

step_secrets() {
  step "4/8 Secret Manager（3 个随机密钥；DSN 在 sql 步骤里生成）"
  need_cmd openssl

  # 值只经内存与管道。openssl 的输出直接进 new_secret 的 stdin。
  openssl rand -base64 48 | tr -d '\n' | new_secret "$SECRET_SUB_PEPPER"
  openssl rand -base64 48 | tr -d '\n' | new_secret "$SECRET_NODE_PEPPER"
  openssl rand -base64 48 | tr -d '\n' | new_secret "$SECRET_SESSION_KEY"

  # 🔴 bp-sub-token-pepper 与 bp-node-token-pepper **不可轮换**（deploy.md §7.1）：
  #    轮换 = 全体订阅 / 全体节点密钥失效。要撤销单个用户走 sub_revoked_at，
  #    要撤销单个节点走 DB 里的节点密钥两步轮换。生成一次就再也不要动它们。
  skip "bp-mail-api-key / bp-mail-api-key-backup / bp-payment-webhook-secret 未创建：ESP 与支付商都未选型"
}

step_sql() {
  step "5/8 Cloud SQL"
  guard_bp_prefix "$SQL_INSTANCE" "$SQL_USER"
  need_cmd openssl

  if gcloud sql instances describe "$SQL_INSTANCE" --project="$PROJECT_ID" >/dev/null 2>&1; then
    skip "实例 $SQL_INSTANCE 已存在"
  else
    confirm "将创建 Cloud SQL 实例 $SQL_INSTANCE。
  这是本脚本唯一一个**产生持续月度支出**的资源：约 \$9.53/月
  （ADR 0005 §1：\$7.665 实例 + \$1.70 存储 + 约 \$0.16 备份），
  且 db-f1-micro **不在 Cloud SQL SLA 覆盖范围内**（ADR 0005 §11 第 1 条）。
  创建耗时通常数分钟。" "create-bp-db"

    # 🔴 --edition=ENTERPRISE 不能省（ADR 0005 §10.2）：PostgreSQL 16+ 缺省是
    #    Enterprise Plus，而 Enterprise Plus **不支持 shared-core 机型**，
    #    命令会带着一个语焉不详的报错直接失败。
    # ⚠️ 保留公网 IP 但**不配置任何 authorized network**：访问只能经
    #    Cloud SQL 连接器 + IAM，公网无法直连（ADR 0005 §10.2）。
    run gcloud sql instances create "$SQL_INSTANCE" \
      --project="$PROJECT_ID" \
      --database-version=POSTGRES_17 \
      --edition=ENTERPRISE \
      --tier=db-f1-micro \
      --region="$REGION" \
      --storage-type=SSD --storage-size=10GB --storage-auto-increase \
      --backup --backup-start-time=10:00 \
      --enable-point-in-time-recovery \
      --retained-backups-count=14 \
      --retained-transaction-log-days=7 \
      --database-flags=autovacuum_vacuum_cost_delay=2
    ok "实例 $SQL_INSTANCE 已创建"
  fi

  if gcloud sql databases describe "$SQL_DATABASE" \
       --instance="$SQL_INSTANCE" --project="$PROJECT_ID" >/dev/null 2>&1; then
    skip "数据库 $SQL_DATABASE 已存在"
  else
    run gcloud sql databases create "$SQL_DATABASE" \
      --instance="$SQL_INSTANCE" --project="$PROJECT_ID"
    ok "数据库 $SQL_DATABASE 已创建"
  fi

  # 用户与 DSN 是一件事：DSN 里含密码，所以「建用户」与「写 DSN secret」必须同一次完成。
  # 若 secret 已存在就整段跳过 —— 我们无法从 secret 之外的任何地方重建密码，
  # 重跑一次「建用户」会把已部署实例手里的密码作废。
  if gcloud secrets describe "$SECRET_DB_URL" --project="$PROJECT_ID" >/dev/null 2>&1; then
    skip "secret $SECRET_DB_URL 已存在 → 跳过建用户（重建会作废在跑实例手里的密码）"
    return 0
  fi

  local pw dsn conn
  # 用 hex 而不是 base64：base64 的 / + = 在 DSN 里要百分号转义，
  # 一个转义错误会变成「连不上但报错像是密码错」的排障噩梦。48 hex = 192 bit 熵，够了。
  pw="$(openssl rand -hex 24)"
  conn="${PROJECT_ID}:${REGION}:${SQL_INSTANCE}"
  # DSN 形式见 deploy.md §6.1：host 是**目录路径**不是主机名；不需要 sslmode
  # （连接器本身走 Google 内部网络且已加密）。
  dsn="postgres://${SQL_USER}:${pw}@/${SQL_DATABASE}?host=/cloudsql/${conn}"

  if gcloud sql users describe "$SQL_USER" \
       --instance="$SQL_INSTANCE" --project="$PROJECT_ID" >/dev/null 2>&1; then
    warn "用户 $SQL_USER 已存在但 $SECRET_DB_URL 不存在 —— 将重设密码并写入新 secret。
     ⚠️ 已在跑的实例会在下次重启时才拿到新密码，期间会连不上库。"
    confirm "确认重设 $SQL_USER 的密码？" "reset-password"
    # ⚠️ 密码经 argv 传递，在这条命令存续的几秒内对同机其它进程的 `ps` 可见。
    #    gcloud 没有提供从 stdin 读密码的非交互形式（--prompt-for-password 是交互式的，
    #    用管道喂它的行为 **待核实**）。这是本脚本唯一一处凭据残留，
    #    对策是只在运维自己的机器上跑本脚本。它**不落盘**，也不进 shell history
    #    （值来自变量而不是字面量）。
    run gcloud sql users set-password "$SQL_USER" \
      --instance="$SQL_INSTANCE" --project="$PROJECT_ID" --password="$pw"
  else
    run gcloud sql users create "$SQL_USER" \
      --instance="$SQL_INSTANCE" --project="$PROJECT_ID" --password="$pw"
    ok "用户 $SQL_USER 已创建"
  fi

  printf '%s' "$dsn" | new_secret "$SECRET_DB_URL"
  # 明确擦掉内存里的副本。bash 没有安全擦除，unset 只是不让它继续出现在后续的
  # 展开与 trap 里 —— 聊胜于无，写出来是为了让「凭据的生命周期」在代码里可见。
  unset pw dsn conn
}

step_pubsub() {
  step "6/8 Pub/Sub 告警通道"
  guard_bp_prefix "$PUBSUB_TOPIC"
  if gcloud pubsub topics describe "$PUBSUB_TOPIC" --project="$PROJECT_ID" >/dev/null 2>&1; then
    skip "topic $PUBSUB_TOPIC 已存在"
  else
    run gcloud pubsub topics create "$PUBSUB_TOPIC" --project="$PROJECT_ID"
    ok "topic $PUBSUB_TOPIC 已创建"
  fi

  # 🔴 monitoring.md §4：Pub/Sub 中继跑在 bp-api 上，这是**自我引用** ——
  #    bp-api 挂了中继也发不出去，而那恰恰是最需要告警的时刻。
  #    所以 email 通道不是可选项，且收件邮箱**不能是 @babel.plus**。
  warn '告警通道（notification channel）与告警策略本脚本不建：
     gcloud alpha monitoring channels create 需要一个真实的运维邮箱，属于人工输入；
     且 monitoring.md §3.1 要求全部 log-based metrics 在 bp-api **首次部署之前**建好
     （自定义日志指标不追溯）。见 docs/04-ops/monitoring.md §3–§4。'
  skip "push 订阅 $PUBSUB_SUB 依赖 bp-api 存在，放在 --step=postdeploy"
}

# step_logging 建一条日志排除过滤器，挡住订阅 token 进 Cloud Logging。
#
# 🔴 **问题不在应用侧。** 应用自己的访问日志早就走 middleware.RedactPath 打过码了
#    （`/s/abcdefgh…`）。漏的是 **Cloud Run 的平台请求日志** ——
#    每个请求 Cloud Run 都会往 `run.googleapis.com/requests` 写一条，
#    其中 `httpRequest.requestUrl` 是**完整 URL**：
#      · `GET /s/{token}`            → 完整订阅 token 在路径里
#      · `GET /api/v1/client/subscribe?token=…` → 完整 token 在 query 里
#      · 节点面 `?token=…`           → 节点密钥明文（v2node 只发 query token）
#    这一条应用侧改不了，只能在日志侧排除。
#
#    后果正是 RedactPath 注释里点名要防的那件事：Cloud Logging 的 _Default bucket 里
#    躺着一份**可直接使用**的订阅 token / 节点密钥明文清单，而数据库里存的是 sha256。
#    `roles/logging.viewer` 的授予面（值班、排障、日志导出、sink 到 BigQuery）
#    远宽于数据库权限 —— 等于加密存储被平台日志整体绕过。
#
# ⚠️ **代价（必须知道再执行）：** Cloud Logging 只能整条排除，不能改写字段。
#    所以这条过滤器会让这些请求在**平台请求日志**里彻底消失 —— 延迟、
#    responseSize、GFE 侧状态码都不再有。
#    可接受的理由：应用自己的 AccessLog 仍然记录同样这些请求（路径已打码，
#    含 status / duration_ms / bytes / request_id），排障信息不丢，丢的是平台侧的那份副本。
#    ⚠️ 但 `bp_api_5xx` / `bp_api_429` 两条日志指标走的**正是平台请求日志**
#    （monitoring.md §3.2 给的过滤器是 `httpRequest.status>=500`）——
#    订阅与节点面路径上的 5xx/429 因此不会被这两条指标计入。
#    要么接受（这两条路径的错误率另有 bp_subscribe_404 / bp_uniproxy_auth_fail 覆盖），
#    要么把那两条指标改成走应用日志的 jsonPayload.status。**本脚本不替你做这个取舍。**
step_logging() {
  step "日志排除：不让订阅 token / 节点密钥进 Cloud Logging"

  local sink=_Default
  local excl=bp-redact-credential-urls
  local filter
  # 只排 Cloud Run 的平台请求日志，且只排凭据在 URL 里的那几条路径。
  # 应用自己写的 stdout 日志（logName 是 …/stdout）不受影响。
  filter='logName:"run.googleapis.com%2Frequests"
AND resource.labels.service_name="'"$RUN_SERVICE"'"
AND (httpRequest.requestUrl:"/s/" OR httpRequest.requestUrl:"token=")'

  if gcloud logging sinks describe "$sink" --project="$PROJECT_ID"        --format='value(exclusions[].name)' 2>/dev/null | tr ';' '\n' | grep -qx "$excl"; then
    skip "排除过滤器 $excl 已存在"
    return 0
  fi

  warn "即将给 _Default 日志接收器加一条排除过滤器 —— 读一遍上面那段「代价」再继续。"
  confirm "确认要建 $excl 吗？" "$excl"
  run gcloud logging sinks update "$sink" --project="$PROJECT_ID" \
      --add-exclusion="name=$excl,filter=$filter"
  ok "排除过滤器 $excl 已创建"
}

step_tasks() {
  step "7/8 Cloud Tasks 队列"
  guard_bp_prefix "$QUEUE_TRAFFIC" "$QUEUE_MAIL"

  # --max-concurrent-dispatches=4 / --max-dispatches-per-second=10 直接来自 ADR 0005 §6.2 第 2 条。
  # 🔴 这些任务打的是**同一个** bp-api 服务，消耗的是同一份 --max-instances=8 预算。
  #    不限并发的话，一批任务同时投递会瞬间把实例拉满，把用户请求挤成 429。
  if gcloud tasks queues describe "$QUEUE_TRAFFIC" \
       --project="$PROJECT_ID" --location="$REGION" >/dev/null 2>&1; then
    skip "队列 $QUEUE_TRAFFIC 已存在"
  else
    run gcloud tasks queues create "$QUEUE_TRAFFIC" \
      --project="$PROJECT_ID" --location="$REGION" \
      --max-concurrent-dispatches=4 \
      --max-dispatches-per-second=10 \
      --max-attempts=10 \
      --min-backoff=5s --max-backoff=300s --max-doublings=4
    ok "队列 $QUEUE_TRAFFIC 已创建"
  fi

  # 发信队列：openapi 的 /internal/tasks/mail-send 标注为 Cloud Tasks 驱动，
  # 所以它需要自己的队列。并发给得更低 —— ESP 侧通常有自己的速率限制，
  # 而发信失败的重试比流量入账的重试廉价得多。**这两个数字是设定值，无实测依据。**
  if gcloud tasks queues describe "$QUEUE_MAIL" \
       --project="$PROJECT_ID" --location="$REGION" >/dev/null 2>&1; then
    skip "队列 $QUEUE_MAIL 已存在"
  else
    run gcloud tasks queues create "$QUEUE_MAIL" \
      --project="$PROJECT_ID" --location="$REGION" \
      --max-concurrent-dispatches=2 \
      --max-dispatches-per-second=5 \
      --max-attempts=5 \
      --min-backoff=10s --max-backoff=600s --max-doublings=3
    ok "队列 $QUEUE_MAIL 已创建"
  fi

  # 入队权限授到队列级，不在项目级授 roles/cloudtasks.enqueuer（同「逐 secret 授权」的道理）。
  local q
  for q in "$QUEUE_TRAFFIC" "$QUEUE_MAIL"; do
    run gcloud tasks queues add-iam-policy-binding "$q" \
      --project="$PROJECT_ID" --location="$REGION" \
      --member="serviceAccount:${SA_API}@${PROJECT_ID}.iam.gserviceaccount.com" \
      --role=roles/cloudtasks.enqueuer
  done
  ok "$SA_API ← 两个队列的 enqueuer（队列级，非项目级）"
}

# api_url 取 bp-api 的 run.app URL。取不到返回空串。
api_url() {
  gcloud run services describe "$RUN_SERVICE" \
    --project="$PROJECT_ID" --region="$REGION" \
    --format='value(status.url)' 2>/dev/null || true
}

# mkjob <名字> <cron> <路径> <说明>
mkjob() {
  local name="$1" cron="$2" path="$3" desc="$4"
  guard_bp_prefix "$name"
  local verb="create"
  if gcloud scheduler jobs describe "$name" \
       --project="$PROJECT_ID" --location="$REGION" >/dev/null 2>&1; then
    verb="update"
  fi
  # 🔴 audience 一律用 run.app URL，不用镜像域名（deploy.md §8.1）：
  #    域名池会轮换（ADR 0003 §5）而 OIDC 的 aud 是硬校验；
  #    更重要的是**故障隔离** —— 公开域名被封时，重置 / 到期 / 聚合 / 入账全部照常运行。
  run gcloud scheduler jobs "$verb" http "$name" \
    --project="$PROJECT_ID" --location="$REGION" \
    --schedule="$cron" --time-zone="Asia/Shanghai" \
    --uri="${AUD}${path}" --http-method=POST \
    --oidc-service-account-email="${SA_TASKS}@${PROJECT_ID}.iam.gserviceaccount.com" \
    --oidc-token-audience="$AUD" \
    --attempt-deadline=120s \
    --max-retry-attempts=3 --min-backoff=10s --max-backoff=120s \
    --description="$desc"
  ok "scheduler $name ($verb) $cron → $path"
}

step_postdeploy() {
  step "8/8 依赖 bp-api 已部署的部分"

  AUD="$(api_url)"
  if [ -z "$AUD" ]; then
    if [ "$DRY_RUN" -eq 1 ]; then
      AUD="https://${RUN_SERVICE}-<项目号>.${REGION}.run.app"
      warn "[dry-run] 取不到 bp-api 的 URL，用占位符 $AUD 继续打印命令"
    else
      die "$RUN_SERVICE 尚未部署，取不到 run.app URL。
     请先跑 ./infra/deploy/deploy-api.sh --promote，再回来跑 --step=postdeploy。"
    fi
  fi
  log "  bp-api URL / OIDC audience = $AUD"

  # ── 8.1 依赖服务存在的 IAM 绑定 ──
  # run.invoker 授在**服务上**而不是项目级：项目级会让 bp-tasks-sa 同时能调
  # anthropic-relay / lisa-cloud / lisa-web（as-built §4）。
  run gcloud run services add-iam-policy-binding "$RUN_SERVICE" \
    --project="$PROJECT_ID" --region="$REGION" \
    --member="serviceAccount:${SA_TASKS}@${PROJECT_ID}.iam.gserviceaccount.com" \
    --role=roles/run.invoker
  ok "$SA_TASKS ← bp-api 的 run.invoker（服务级）"

  # 同理，部署权限授在服务上而不是项目级 —— 项目级 roles/run.developer
  # 等于让 babel.plus 的 CI 能改现有三个服务。这是对 deploy.md §3.3 的收紧。
  run gcloud run services add-iam-policy-binding "$RUN_SERVICE" \
    --project="$PROJECT_ID" --region="$REGION" \
    --member="serviceAccount:${SA_DEPLOY}@${PROJECT_ID}.iam.gserviceaccount.com" \
    --role=roles/run.developer
  ok "$SA_DEPLOY ← bp-api 的 run.developer（服务级）"

  # ── 8.2 Scheduler 任务 ──
  #
  # 🔴 路径与频率以 **openapi/openapi.yaml 为准**（全仓事实源），不是 deploy.md §8.2。
  #    两处已知差异，见 infra/deploy/README.md §4：
  #      · deploy.md 的 /internal/tasks/expire-sweep → 契约是 expire-check，且频率 10 分钟 → 5 分钟
  #      · deploy.md 的 /internal/tasks/rollup?grain=day → 契约是 stat-rollup，**契约里没有 grain 参数**
  #
  # 🔴 到期扫描（expire-check）与流量重置（traffic-reset）**必须 bump user_rev**，
  #    统计聚合（stat-rollup）**绝不能 bump** —— 这是 handler 侧的要求（ADR 0006 §11.2），
  #    写在这里是因为改 cron 频率的人往往就是需要知道这条的人。
  #
  # 🔴 traffic-reset 的置零与推进 next_reset_at 必须在**同一条 UPDATE 里**完成。
  #    拆成两条语句时，一次重投会把用户流量二次清零 —— 这是全部定时任务里最危险的一条。
  mkjob bp-alive-gc       "*/5 * * * *" /internal/tasks/alive-gc \
    "在线态清理：DELETE user_alive WHERE seen_at < now()-5min（天然幂等）"
  mkjob bp-expire-check   "*/5 * * * *" /internal/tasks/expire-check \
    "到期扫描：必须 bump user_rev，否则到期用户永远不从节点列表消失"
  mkjob bp-order-timeout  "* * * * *"   /internal/tasks/order-timeout \
    "订单超时：置 expired，但收款地址继续监听 >=24h，到账入余额"
  mkjob bp-chain-scan     "* * * * *"   /internal/tasks/chain-scan \
    "链上扫描：以链上/网关查单为权威，回调不可信"
  mkjob bp-traffic-reset  "7 * * * *"   /internal/tasks/traffic-reset \
    "流量周期重置：置零与推进 next_reset_at 必须同一条 UPDATE；必须 bump user_rev"
  mkjob bp-stat-rollup-hourly "25 * * * *" /internal/tasks/stat-rollup \
    "统计聚合（小时）：ON CONFLICT DO UPDATE 覆盖不累加；绝不 bump user_rev"
  mkjob bp-stat-rollup-daily  "20 1 * * *" /internal/tasks/stat-rollup \
    "统计聚合（日）：口径统一按 Asia/Shanghai"
  mkjob bp-remind-sweep       "0 10 * * *" /internal/tasks/remind-sweep \
    "到期/流量提醒扫描：幂等键 (user_id, remind_kind, day)"

  # 每周 pg_dump（ADR 0005 §10.4）：它是 Cloud Run **Job** 不是 HTTP 端点。
  # Job 不存在就跳过 —— 我们不为一个不存在的目标建定时任务。
  if gcloud run jobs describe "$DUMP_JOB" --project="$PROJECT_ID" --region="$REGION" >/dev/null 2>&1; then
    local dump_uri="https://${REGION}-run.googleapis.com/apis/run.googleapis.com/v1/namespaces/${PROJECT_ID}/jobs/${DUMP_JOB}:run"
    local verb="create"
    if gcloud scheduler jobs describe bp-db-dump-weekly \
         --project="$PROJECT_ID" --location="$REGION" >/dev/null 2>&1; then
      verb="update"
    fi
    # 触发 Cloud Run Job 用 **OAuth** 不是 OIDC —— 调用的是 Google API 而不是我们自己的端点。
    # 这条 URI 的形式 deploy.md §8.3 标了 **待核实**（Jobs Admin API 路径在版本间调整过）。
    run gcloud scheduler jobs "$verb" http bp-db-dump-weekly \
      --project="$PROJECT_ID" --location="$REGION" \
      --schedule="0 3 * * 0" --time-zone="Asia/Shanghai" \
      --uri="$dump_uri" --http-method=POST \
      --oauth-service-account-email="${SA_TASKS}@${PROJECT_ID}.iam.gserviceaccount.com" \
      --description="每周 pg_dump -Fc 到跨区 GCS（ADR 0005 §10.4：自动备份防不了删错实例）"
    ok "scheduler bp-db-dump-weekly ($verb)"
  else
    warn "Cloud Run Job $DUMP_JOB 不存在 → 跳过 bp-db-dump-weekly。
     ⚠️ ADR 0005 §10.4 的「跨区 pg_dump」是「删错实例」这条风险的**唯一对冲**，
     它现在没有生效。见 infra/deploy/README.md 的『这次没有解决的』。"
  fi

  # ── 8.3 Pub/Sub push 订阅 ──
  guard_bp_prefix "$PUBSUB_SUB"
  if gcloud pubsub subscriptions describe "$PUBSUB_SUB" --project="$PROJECT_ID" >/dev/null 2>&1; then
    skip "订阅 $PUBSUB_SUB 已存在"
  else
    run gcloud pubsub subscriptions create "$PUBSUB_SUB" \
      --project="$PROJECT_ID" \
      --topic="$PUBSUB_TOPIC" \
      --push-endpoint="${AUD}/internal/tasks/alert-relay" \
      --push-auth-service-account="${SA_TASKS}@${PROJECT_ID}.iam.gserviceaccount.com" \
      --push-auth-token-audience="$AUD" \
      --ack-deadline=60 \
      --message-retention-duration=7d \
      --min-retry-delay=10s --max-retry-delay=600s
    ok "订阅 $PUBSUB_SUB 已创建 → ${AUD}/internal/tasks/alert-relay"
  fi
  warn "/internal/tasks/alert-relay **不在 openapi/openapi.yaml 里**（契约只到 remind-sweep）。
     订阅建好了但端点还不存在 —— push 会持续 404 直到 monitoring.md §4 的中继实现。
     这是已知缺口，不是脚本 bug。"
}

# ───────────────────────── 主流程 ─────────────────────────

main() {
  local arg
  for arg in "$@"; do
    case "$arg" in
      --step=*)    STEP="${arg#*=}" ;;
      --project=*) PROJECT_ID="${arg#*=}" ;;
      --dry-run)   DRY_RUN=1 ;;
      --yes)       ASSUME_YES=1 ;;
      -h|--help)   usage; exit 0 ;;
      *)           usage >&2; die "未知参数：$arg" ;;
    esac
  done

  guard_project
  need_cmd gcloud

  log "项目 : $PROJECT_ID"
  log "区域 : $REGION"
  log "步骤 : $STEP"
  if [ "$DRY_RUN" -eq 1 ]; then
    log "模式 : DRY-RUN（只打印写操作，只读探测仍会真的发生）"
  fi

  case "$STEP" in
    all)
      # ⚠️ iam 必须排在 registry 之前：registry 那步要给 bp-deploy-sa 授仓库级 writer，
      #    而这个服务账号是 iam 那步创建的。2026-08-17 实测踩到 ——
      #    原顺序下 --step=all 会在第 2 步以
      #    `INVALID_ARGUMENT: Service account bp-deploy-sa@… does not exist` 中断。
      step_apis; step_iam; step_registry; step_secrets; step_sql; step_pubsub; step_tasks
      step "完成（不含 postdeploy）"
      log "  下一步：./infra/deploy/deploy-api.sh --promote"
      log "  然后  ：./infra/deploy/setup-infra.sh --step=postdeploy"
      log "  最后  ：./infra/scripts/verify-isolation.sh"
      ;;
    apis)       step_apis ;;
    registry)   step_registry ;;
    iam)        step_iam ;;
    secrets)    step_secrets ;;
    sql)        step_sql ;;
    pubsub)     step_pubsub ;;
    tasks)      step_tasks ;;
    # 刻意**不进 all**：它有一个需要人读完再决定的取舍（见 step_logging 的注释），
    # 不该在一次「把基础设施拉起来」里被顺手执行掉。
    logging)    step_logging ;;
    postdeploy) step_postdeploy ;;
    *)          usage >&2; die "未知步骤：$STEP" ;;
  esac
}

# AUD 在 step_postdeploy 里赋值，mkjob 读它。声明在这里是为了让 shellcheck 看得见。
AUD=""

main "$@"

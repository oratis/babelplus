#!/usr/bin/env bash
#
# image-provenance.sh —— 「线上这个修订版跑的到底是哪份源码」，一条命令回答
#
# 事实源：
#   docs/00-overview/roadmap.md B41（问题本身）
#   docs/evidence/gcp-inventory-20260821/README.md §5.2（它已经发生过的那一次）
#   infra/deploy/deploy-api.sh 的 LABEL_* 常量（label key 的另一半，两处必须一致）
#   docs/04-ops/deploy.md §4.1（镜像与 tag 的约定）
#
# 🔴 这个脚本存在的理由：2026-08-21 排查时，生产 bp-api-2fbf49d 的短 sha
#    对应的 commit 不被任何分支引用（分支被 force-push 改写过），
#    「线上跑的是哪份源码」只能靠去 GitHub 对象库按完整 sha 捞才答得出来。
#    deploy-api.sh 现在把完整 sha 写进镜像 label —— **但一个没人读得出来的 label 不算修好**，
#    所以有了这一侧。
#
# 本脚本**只做只读调用**。没有任何 create / update / delete。
# 唯一可能有副作用的是 --pull（往本机 docker 拉一个镜像），默认关闭。

set -euo pipefail

readonly EXPECTED_PROJECT_ID="oratis-491316"
readonly REGION="us-central1"
readonly SERVICE_DEFAULT="bp-api"

# ⚠️ 与 infra/deploy/deploy-api.sh 顶部的 LABEL_* 常量**必须一致**（那边写，这边读）。
# 两处刻意重复，理由与六个脚本各自复制守卫代码相同：每个脚本要能单独拷出去跑。
readonly LABEL_SHA="org.opencontainers.image.revision"
readonly LABEL_VERSION="org.opencontainers.image.version"
readonly LABEL_CREATED="org.opencontainers.image.created"
readonly LABEL_BRANCH="plus.babel.git.branch"
readonly LABEL_DIRTY="plus.babel.git.dirty"
readonly LABEL_BUILDER="plus.babel.build.by"

# Cloud Run 修订版上的快捷 label（deploy-api.sh 的 --update-labels 写的）。
readonly RUN_LABEL_SHA="bp-git-sha"
readonly RUN_LABEL_DIRTY="bp-git-dirty"

# ───────────────────────── 运行时开关 ─────────────────────────

PROJECT_ID="${PROJECT_ID:-$EXPECTED_PROJECT_ID}"
SERVICE="$SERVICE_DEFAULT"
REVISION=""
IMAGE=""
DRY_RUN=0
ALLOW_PULL=0
FOUND_SHA=""
FOUND_FROM=""
# 修订版 label 与镜像 label 打架时置 1。这是一次**事故**，不能以 0 退出 ——
# 否则挂在定时作业里的调用会把「两处来源记录互相矛盾」当成一次成功。
LABEL_CONFLICT=0

# ───────────────────────── 通用工具（与 infra/ 下其它脚本刻意保持重复）─────────────────────────

log()  { printf '%s\n' "$*" >&2; }
step() { printf '\n\033[1m▸ %s\033[0m\n' "$*" >&2; }
pass() { printf '  \033[32m✓\033[0m %s\n' "$*" >&2; }
fail() { printf '  \033[31m✗ %s\033[0m\n' "$*" >&2; }
warn() { printf '  \033[33m⚠\033[0m %s\n' "$*" >&2; }
note() { printf '  · %s\n' "$*" >&2; }
die()  { printf '\n\033[31m✗ %s\033[0m\n' "$*" >&2; exit 2; }

need_cmd() {
  command -v "$1" >/dev/null 2>&1 || die "缺少命令：$1"
}

usage() {
  cat <<'EOF'
用法: image-provenance.sh [选项]

回答「线上这个修订版跑的是哪份源码」，并当场判断那个 commit 是否还被某个分支引用 ——
后者正是 roadmap B41 出事的判据（force-push 之后短 sha 指向孤儿 commit）。

**纯只读**（--pull 除外，它会往本机 docker 拉一个镜像）。

选项:
  --service=<名>    Cloud Run 服务名，默认 bp-api。查当前接 100% 流量的修订版
  --revision=<名>   直接指定修订版，跳过流量查询
  --image=<引用>    直接查一个镜像引用，完全不碰 Cloud Run
  --pull            允许 docker pull + docker inspect 读镜像 label（最可靠，但要拉镜像）
  --project=<id>    GCP 项目 ID。必须是 oratis-491316
  --dry-run         只打印将要执行的只读命令
  -h, --help        显示本帮助

退出码:
  0  答得出完整 sha，且该 commit 仍被某个分支引用
  1  答不出来，或答得出但该 commit 已成孤儿（= B41 那种状态，要立刻处理）
  2  用法或环境错误

来源优先级（越靠前越省事）:
  1. 修订版 label bp-git-sha           一条 gcloud，不用拉镜像
  2. 镜像 label org.opencontainers.image.revision   经 Artifact Registry 元数据读
  3. 同上，但经 docker pull + docker inspect（--pull）
EOF
}

guard_project() {
  if [ "$PROJECT_ID" != "$EXPECTED_PROJECT_ID" ]; then
    die "PROJECT_ID 必须是 $EXPECTED_PROJECT_ID，当前是 \"$PROJECT_ID\"。"
  fi
}

# gread <说明> <gcloud 参数...> —— 只读调用，打印 stdout。dry-run 下只打印命令。
gread() {
  local what="$1"; shift
  if [ "$DRY_RUN" -eq 1 ]; then
    printf '  [dry-run] %s\n' "$*" >&2
    printf ''
    return 0
  fi
  if ! "$@" 2>/dev/null; then
    warn "取不到${what}（gcloud 调用失败）"
    printf ''
    return 1
  fi
}

# json_label <JSON> <key> —— 在任意深度的对象里找一个 label key。
#
# ⚠️ **待核实**：`gcloud artifacts docker images describe` 的输出里到底带不带镜像
#    config 的 labels，各 gcloud 版本不一致（本次没有真跑过 gcloud）。所以这里用
#    「任意深度找 key」而不是写死字段路径 —— 找不到就退回 --pull 那条路，
#    而不是把「读不到」当成「没有 label」。
json_label() {
  printf '%s' "$1" | jq -r --arg k "$2" '[.. | objects | select(has($k)) | .[$k]] | .[0] // empty' 2>/dev/null || true
}

# ───────────────────────── 解析：修订版 → 镜像 ─────────────────────────

resolve_revision() {
  if [ -n "$REVISION" ]; then
    note "指定修订版：$REVISION"
    return 0
  fi
  step "1 · 当前接 100% 流量的修订版（服务 $SERVICE）"
  local json
  json="$(gread "服务 $SERVICE" gcloud run services describe "$SERVICE" \
            --project="$PROJECT_ID" --region="$REGION" --format=json)" || true
  if [ -z "$json" ]; then
    if [ "$DRY_RUN" -eq 1 ]; then
      REVISION="<dry-run 下未查询>"
      return 0
    fi
    fail "读不到服务 $SERVICE。它可能还没部署，或者当前身份没有 run.services.get 权限。"
    return 1
  fi
  # 只认 percent==100 的那一条：本项目不做灰度（deploy.md §12.1），
  # 出现流量切分本身就是异常，宁可在这里答不出来也不要挑一个看着像的。
  REVISION="$(printf '%s' "$json" | jq -r '[.status.traffic[]? | select(.percent == 100) | .revisionName] | .[0] // empty')"
  if [ -z "$REVISION" ]; then
    fail "没有任何修订版接 100% 流量 —— 流量被切分了？本项目不做灰度，这本身就要查。
     当前流量分配：$(printf '%s' "$json" | jq -c '[.status.traffic[]? | {rev: .revisionName, pct: .percent, tag: .tag}]')"
    return 1
  fi
  pass "$REVISION"
}

read_revision_label() {
  step "2 · 修订版 label（最省事的一条路）"
  local json sha dirty
  json="$(gread "修订版 $REVISION" gcloud run revisions describe "$REVISION" \
            --project="$PROJECT_ID" --region="$REGION" --format=json)" || true
  if [ -z "$json" ]; then
    warn "读不到修订版详情，跳到镜像 label"
    return 1
  fi

  # ⚠️ 字段路径 **待核实**（gcloud 未真跑过）：修订版走 Knative 形态时是
  #    .spec.containers[0].image，新版 API 可能不同。取不到就报出来，不要静默当成空。
  IMAGE="$(printf '%s' "$json" | jq -r '.spec.containers[0].image // .spec.template.spec.containers[0].image // empty')"
  if [ -n "$IMAGE" ]; then
    note "镜像：$IMAGE"
  else
    warn "从修订版里取不到镜像引用（gcloud 的字段路径 **待核实**）"
  fi

  sha="$(printf '%s' "$json" | jq -r --arg k "$RUN_LABEL_SHA" '.metadata.labels[$k] // empty')"
  dirty="$(printf '%s' "$json" | jq -r --arg k "$RUN_LABEL_DIRTY" '.metadata.labels[$k] // empty')"
  if [ -z "$sha" ]; then
    warn "修订版上没有 ${RUN_LABEL_SHA} label。
     两种可能：① 它是 deploy-api.sh 加这个 label **之前**部署的（2026-08-23 以前的全部修订版）；
     ② --update-labels 没有传播到修订版（该行为 **待核实**）。两种情况都走镜像 label。"
    return 1
  fi
  FOUND_SHA="$sha"
  FOUND_FROM="修订版 label ${RUN_LABEL_SHA}"
  pass "${RUN_LABEL_SHA} = ${sha}  ${RUN_LABEL_DIRTY}=${dirty:-?}"
}

read_image_labels() {
  step "3 · 镜像 label（权威记录）"
  if [ -z "$IMAGE" ]; then
    warn "不知道镜像引用，这一步做不了。用 --image=<引用> 直接指定。"
    return 1
  fi

  local json sha
  json="$(gread "镜像 $IMAGE 的元数据" gcloud artifacts docker images describe "$IMAGE" \
            --project="$PROJECT_ID" --format=json)" || true
  if [ -n "$json" ]; then
    sha="$(json_label "$json" "$LABEL_SHA")"
    if [ -n "$sha" ]; then
      pass "${LABEL_SHA} = ${sha}"
      note "tag=$(json_label "$json" "$LABEL_VERSION")  分支=$(json_label "$json" "$LABEL_BRANCH")"
      note "构建时间=$(json_label "$json" "$LABEL_CREATED")  dirty=$(json_label "$json" "$LABEL_DIRTY")  由=$(json_label "$json" "$LABEL_BUILDER")"
      if [ -z "$FOUND_SHA" ]; then
        FOUND_SHA="$sha"
        FOUND_FROM="镜像 label ${LABEL_SHA}"
      elif [ "$FOUND_SHA" != "$sha" ]; then
        # 修订版 label 与镜像 label 打架 = 有人手工改过其中一个，或者部署与构建不是同一次。
        fail "🔴 修订版 label（$FOUND_SHA）与镜像 label（$sha）**不一致**。
     以镜像 label 为准（它与二进制同生共死），并把这次不一致当成事故查。"
        FOUND_SHA="$sha"
        FOUND_FROM="镜像 label ${LABEL_SHA}（与修订版 label 冲突）"
        LABEL_CONFLICT=1
      fi
      return 0
    fi
    warn "Artifact Registry 的元数据里没找到 ${LABEL_SHA}。
     可能是 gcloud 这个版本不返回镜像 config 的 labels（**待核实**），也可能这个镜像
     确实是 2026-08-23 加 label 之前构建的。"
  fi

  if [ "$ALLOW_PULL" -eq 0 ]; then
    note "要更可靠地读，加 --pull（会往本机 docker 拉这个镜像）。"
    return 1
  fi

  need_cmd docker
  step "3b · docker pull + inspect"
  if [ "$DRY_RUN" -eq 1 ]; then
    printf '  [dry-run] docker pull %s\n' "$IMAGE" >&2
    printf '  [dry-run] docker inspect --format {{json .Config.Labels}} %s\n' "$IMAGE" >&2
    return 1
  fi
  if ! docker pull "$IMAGE" >/dev/null 2>&1; then
    warn "docker pull 失败（没登录 Artifact Registry？先跑 gcloud auth configure-docker）"
    return 1
  fi
  local labels
  labels="$(docker inspect --format '{{json .Config.Labels}}' "$IMAGE" 2>/dev/null || printf '{}')"
  sha="$(printf '%s' "$labels" | jq -r --arg k "$LABEL_SHA" '.[$k] // empty')"
  if [ -z "$sha" ]; then
    fail "镜像里没有 ${LABEL_SHA} label。
     这就是 B41 的原始状态：镜像与源码的对应关系**只剩 tag 一条线索**，
     而 tag 是短 sha，分支被 force-push 之后它可能谁也不指。
     现在只能：按 tag 去 git 里找同名短 sha，找不到就去代码托管方的对象库捞。"
    return 1
  fi
  FOUND_SHA="$sha"
  FOUND_FROM="docker inspect 的镜像 label"
  pass "${LABEL_SHA} = ${sha}"
  note "$(printf '%s' "$labels" | jq -c 'with_entries(select(.key | startswith("plus.babel") or startswith("org.opencontainers")))')"
}

# ───────────────────────── 拿到 sha 之后：它还在不在 ─────────────────────────

verify_against_git() {
  step "4 · 这个 commit 在仓库里还找得到吗"
  local root
  root="$(cd "$(dirname "$0")/../.." && pwd)"
  if ! git -C "$root" rev-parse --git-dir >/dev/null 2>&1; then
    warn "$root 不是 git 仓库，跳过核对"
    return 1
  fi

  if ! git -C "$root" cat-file -e "${FOUND_SHA}^{commit}" 2>/dev/null; then
    fail "🔴 本地仓库里**没有** commit ${FOUND_SHA}。
     先 git fetch --all 再试一次。仍然没有，就是它从没被推上去，或者已被垃圾回收 ——
     那么线上跑的源码在这个仓库里**不存在**，只有镜像本身还留着二进制。"
    return 1
  fi
  pass "commit 存在：$(git -C "$root" log -1 --format='%h %ad %s' --date=short "$FOUND_SHA")"

  local branches
  # 行首的 * 是当前分支，+ 是「在另一个 worktree 里被检出」—— 两个都要剥掉。
  branches="$(git -C "$root" branch -a --contains "$FOUND_SHA" 2>/dev/null | sed 's/^[*+ ]*//' | paste -sd' ' - || printf '')"
  if [ -z "$branches" ]; then
    fail "🔴 **没有任何分支引用这个 commit** —— 这正是 B41 那次的状态。
     它现在只能靠完整 sha 取回（这份 label 就是它唯一的出处）。
     处置：给它打一个 tag 钉住，别让 GC 收走：
       git tag deployed/${FOUND_SHA:0:7} ${FOUND_SHA} && git push origin deployed/${FOUND_SHA:0:7}
     然后查清楚哪个分支被 force-push 改写了。"
    return 1
  fi
  pass "被这些 ref 引用：$branches"
}

# ───────────────────────── 主流程 ─────────────────────────

main() {
  local arg
  for arg in "$@"; do
    case "$arg" in
      --service=*)  SERVICE="${arg#*=}" ;;
      --revision=*) REVISION="${arg#*=}" ;;
      --image=*)    IMAGE="${arg#*=}" ;;
      --pull)       ALLOW_PULL=1 ;;
      --project=*)  PROJECT_ID="${arg#*=}" ;;
      --dry-run)    DRY_RUN=1 ;;
      -h|--help)    usage; exit 0 ;;
      *)            usage >&2; die "未知参数：$arg" ;;
    esac
  done

  guard_project
  need_cmd jq
  need_cmd git

  log "项目 : $PROJECT_ID"
  if [ "$DRY_RUN" -eq 1 ]; then
    log "模式 : DRY-RUN（连只读调用都不发，下面的判定没有意义）"
  fi

  if [ -n "$IMAGE" ] && [ -z "$REVISION" ]; then
    log "镜像 : $IMAGE（跳过 Cloud Run）"
    read_image_labels || true
  else
    need_cmd gcloud
    resolve_revision || true
    if [ -n "$REVISION" ]; then
      read_revision_label || true
    fi
    read_image_labels || true
  fi

  step "结果"
  if [ -z "$FOUND_SHA" ]; then
    log "  🔴 **答不出来**：没能拿到完整 sha。"
    log "     这就是 roadmap B41 描述的状态本身 —— 「线上跑的是哪份源码」不可回答，"
    log "     于是无法回滚到已知 good，也无法审计。"
    log "     还能试：--pull（拉镜像读 label）、--image=<引用>（跳过 Cloud Run 那几跳）。"
    exit 1
  fi
  log "  完整 sha : $FOUND_SHA"
  log "  来源     : $FOUND_FROM"
  if verify_against_git && [ "$LABEL_CONFLICT" -eq 0 ]; then
    log ""
    log "  ✅ 线上跑的源码可定位，且仍被分支引用。"
    exit 0
  fi
  if [ "$LABEL_CONFLICT" -eq 1 ]; then
    log ""
    log "  🔴 sha 答得出来，但**两处来源记录互相矛盾** —— 以 1 退出。"
    log "     矛盾本身要当事故查：修订版 label 与镜像 label 只可能由同一次部署写出，"
    log "     不一致说明有人手工改过其中一个，或者部署与构建不是同一次。"
  fi
  exit 1
}

main "$@"

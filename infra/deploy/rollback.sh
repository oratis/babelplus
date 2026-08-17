#!/usr/bin/env bash
#
# rollback.sh —— bp-api 的 Cloud Run 修订版流量回滚
#
# 事实源：docs/04-ops/deploy.md §12（回滚）· §12.4（回滚后必做）
#
# 这是我们最强的一张牌：**代码回滚是秒级的，只是把流量指回旧修订版，不重新构建、不重新部署。**
# 也正因如此，必须把它旁边那条不对称写在最显眼的地方：
#
#   🔴 代码能回滚，**数据库 schema 不能**。
#      PITR 一定会新建实例（ADR 0005 §10.4 引官方原文），因此恢复流程一定包含
#      「改连接串 + 重新部署 bp-api」，耗时小时级或不可能。
#      发布纪律由此倒推：一次发布只做 expand（加列/加表/加可空字段），
#      contract（删列/改类型）放下一次 —— 这样旧代码才能在新 schema 上跑。
#
# 🔴 本脚本刻意**不提供灰度**。deploy.md §12.1：节点每 60 秒轮询一次，
#    10% 的流量切分意味着同一个节点在相邻两次轮询里可能拿到两个不同版本的响应。
#    凡是改动 /api/v1/server/UniProxy/* 的发布，一律 0% → 验证 → 100%。

set -euo pipefail

# ───────────────────────── 防呆常量 ─────────────────────────

readonly EXPECTED_PROJECT_ID="oratis-491316"
readonly REGION="us-central1"
readonly DEFAULT_SERVICE="bp-api"

# ───────────────────────── 运行时开关 ─────────────────────────

PROJECT_ID="${PROJECT_ID:-$EXPECTED_PROJECT_ID}"
SERVICE="$DEFAULT_SERVICE"
DRY_RUN=0
ASSUME_YES=0
ACTION=""
TARGET_REVISION=""
TARGET_TAG=""

# ───────────────────────── 通用工具（与 infra/ 下其它脚本刻意保持重复）─────────────────────────

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
用法: rollback.sh <动作> [选项]

Cloud Run 修订版流量回滚。秒级，不重新构建，不重新部署。

动作（三选一）:
  --list              列出全部修订版、创建时间、当前流量占比与镜像 tag
  --to=<修订版名>      把 100% 流量切到指定修订版
  --to-tag=<tag>      把 100% 流量切到指定 tag（部署脚本默认打的是 candidate）
  --previous          切到「最近创建的、当前不接流量的」那个修订版

选项:
  --service=<名字>    默认 bp-api。必须带 bp- 前缀
  --project=<id>      GCP 项目 ID。必须是 oratis-491316
  --dry-run           只打印将要执行的命令
  --yes               跳过交互确认。⚠️ 回滚是应急动作，通常**不该**跳过确认
  -h, --help          显示本帮助

例:
  ./infra/deploy/rollback.sh --list
  ./infra/deploy/rollback.sh --to=bp-api-a1b2c3d
  ./infra/deploy/rollback.sh --to-tag=candidate
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
    die "确认失败，已中止。流量未改动。"
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
      *) die "资源名 \"$name\" 不带 bp- 前缀。
     🔴 这条守卫在回滚脚本里比在别处更重要：update-traffic 是**写操作**，
        打错服务名就是在改 anthropic-relay / lisa-cloud / lisa-web 的流量分配
        （as-built §4，与本项目无关的现有服务）。" ;;
    esac
  done
}

need_cmd() {
  command -v "$1" >/dev/null 2>&1 || die "缺少命令：$1"
}

# ───────────────────────── 查询（只读）─────────────────────────

service_json() {
  gcloud run services describe "$SERVICE" \
    --project="$PROJECT_ID" --region="$REGION" --format=json
}

revisions_json() {
  gcloud run revisions list --service="$SERVICE" \
    --project="$PROJECT_ID" --region="$REGION" --format=json
}

# serving_revision 返回当前占比最高的修订版名。没有就返回空串。
serving_revision() {
  service_json 2>/dev/null \
    | jq -r '[.status.traffic[]? | select((.percent // 0) > 0)]
             | sort_by(-(.percent // 0)) | (.[0].revisionName // empty)'
}

do_list() {
  step "$SERVICE 的修订版（新 → 旧）"
  local traffic
  traffic="$(service_json | jq -c '[.status.traffic[]? | {rev: .revisionName, pct: (.percent // 0), tag: (.tag // "")}]')"
  revisions_json | jq -r --argjson t "$traffic" '
    sort_by(.metadata.creationTimestamp) | reverse | .[]
    | .metadata.name as $n
    | ($t | map(select(.rev == $n)) | (.[0].pct // 0)) as $pct
    | ($t | map(select(.rev == $n and .tag != "")) | map(.tag) | join(",")) as $tags
    | [ $n,
        (.metadata.creationTimestamp // "?"),
        ($pct | tostring) + "%",
        (if $tags == "" then "-" else $tags end),
        (.spec.containers[0].image // "?" | split(":") | last)
      ] | @tsv' \
    | awk -F'\t' 'BEGIN{printf "  %-28s %-22s %6s %-12s %s\n","修订版","创建时间","流量","tag","镜像tag"}
                  {printf "  %-28s %-22s %6s %-12s %s\n",$1,$2,$3,$4,$5}'
}

# ───────────────────────── 切流量（写）─────────────────────────

switch_to_revision() {
  local rev="$1" current=""
  guard_bp_prefix "$rev"

  # 目标必须真的存在。切到一个不存在的修订版会让 gcloud 报一个不太好读的错，
  # 而在应急场景下「错误信息好不好读」是有代价的。
  if ! gcloud run revisions describe "$rev" \
        --project="$PROJECT_ID" --region="$REGION" >/dev/null 2>&1; then
    die "修订版 $rev 不存在。先跑 ./infra/deploy/rollback.sh --list 看有哪些。"
  fi

  current="$(serving_revision || true)"
  log "  当前接流量 : ${current:-（未知）}"
  log "  回滚目标   : $rev"

  confirm "将把 ${SERVICE} 的 **100% 流量**切到 ${rev}。
  ⚠️ 这只回滚代码。若上一次发布跑过 migration，schema 仍是新的 ——
     expand-only 纪律保证旧代码能在新 schema 上跑；如果这次发布违反了那条纪律，
     回滚会让旧代码撞上它不认识的 schema，**这时回滚比不回滚更糟**（deploy.md §12.3）。" "$rev"

  run gcloud run services update-traffic "$SERVICE" \
    --project="$PROJECT_ID" --region="$REGION" \
    --to-revisions="${rev}=100"
  ok "流量已切到 $rev"
  post_rollback_checklist
}

switch_to_tag() {
  local tag="$1"
  confirm "将把 ${SERVICE} 的 **100% 流量**切到 tag=${tag} 指向的修订版。" "$tag"
  run gcloud run services update-traffic "$SERVICE" \
    --project="$PROJECT_ID" --region="$REGION" \
    --to-tags="${tag}=100"
  ok "流量已切到 tag=$tag"
  post_rollback_checklist
}

switch_to_previous() {
  local current prev
  current="$(serving_revision || true)"
  if [ -z "$current" ]; then
    die "取不到当前接流量的修订版，无法推断「上一个」。请用 --list 后显式 --to=<修订版名>。"
  fi
  prev="$(revisions_json | jq -r --arg cur "$current" '
    sort_by(.metadata.creationTimestamp) | reverse
    | map(.metadata.name) | map(select(. != $cur)) | (.[0] // empty)')"
  if [ -z "$prev" ]; then
    die "除了 $current 之外没有别的修订版可回。"
  fi
  # ⚠️ 「最近创建的、不是当前这个」不等于「上一个**曾经接过流量**的」——
  #    如果中间有过若干个 --no-traffic 的候选修订版，它会挑到候选版而不是上一个生产版。
  #    所以这里把推断结果打出来，并且仍然要求手工键入修订版名确认。
  warn "--previous 的推断口径是「最近创建的、当前不接流量的修订版」= $prev
     若中间存在未切流量的候选修订版，这个推断会挑到候选版。**看清楚再确认。**"
  switch_to_revision "$prev"
}

post_rollback_checklist() {
  step "回滚后必做（deploy.md §12.4）"
  log "  1. 跑一次 ./infra/scripts/verify-isolation.sh —— 确认现有服务未被波及。"
  log "  2. curl <服务URL>/healthz，确认 revision 字段确实回到了目标 commit。"
  log "  3. curl <服务URL>/readyz，确认数据库连得上。"
  log "  4. 确认 user_rev / config_rev **没有倒退**。"
  log "     版本号是数据不是代码，回滚代码不会把它退回去 —— 这没关系，单调递增多走几步是安全的；"
  log "     🔴 **倒退才是危险的**：节点会以为自己的缓存仍然有效。"
  log "     所以 user_rev 的实现必须是 rev = rev + 1，绝不能是 rev = <某个计算值>。"
  log "  5. 若本次回滚跨越了一次 migration，去 deploy.md §12.3 对照 expand/contract 纪律。"
}

# ───────────────────────── 主流程 ─────────────────────────

main() {
  local arg
  for arg in "$@"; do
    case "$arg" in
      --list)       ACTION="list" ;;
      --to=*)       ACTION="to";       TARGET_REVISION="${arg#*=}" ;;
      --to-tag=*)   ACTION="to-tag";   TARGET_TAG="${arg#*=}" ;;
      --previous)   ACTION="previous" ;;
      --service=*)  SERVICE="${arg#*=}" ;;
      --project=*)  PROJECT_ID="${arg#*=}" ;;
      --dry-run)    DRY_RUN=1 ;;
      --yes)        ASSUME_YES=1 ;;
      -h|--help)    usage; exit 0 ;;
      *)            usage >&2; die "未知参数：$arg" ;;
    esac
  done

  guard_project
  guard_bp_prefix "$SERVICE"
  need_cmd gcloud
  need_cmd jq

  log "项目 : $PROJECT_ID"
  log "服务 : $SERVICE @ $REGION"

  case "$ACTION" in
    list)     do_list ;;
    to)       switch_to_revision "$TARGET_REVISION" ;;
    to-tag)   switch_to_tag "$TARGET_TAG" ;;
    previous) switch_to_previous ;;
    "")       usage >&2; die "必须指定一个动作：--list / --to= / --to-tag= / --previous" ;;
    *)        die "内部错误：未知动作 $ACTION" ;;
  esac
}

main "$@"

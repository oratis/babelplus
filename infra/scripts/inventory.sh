#!/usr/bin/env bash
#
# inventory.sh —— 把 as-built-gcp.md §7 的清点命令固化成**可 diff 的快照**
#
# 事实源：docs/02-architecture/as-built-gcp.md §7（清点命令）· §2–§6（期望值）
#        docs/04-ops/deploy.md §2（部署前后各跑一次）
#
# 产出两种东西，用途不同：
#   *.json      —— jq -S 规范化过的原始输出。给机器 diff 用（verify-isolation.sh --baseline）
#   summary.txt —— 一行一个资源的稳定文本。给人 diff 用，也是提交进 git 当基线的那份
#
# 本脚本**只做只读调用**。它没有任何 create / update / delete。
#
# ⚠️ gcloud --format=json 的字段路径**随版本变化**（deploy.md §2 已标 待核实）：
#    run services list 是 Knative 风格的 .metadata.name，其余是 .name。
#    下面的 jq 表达式一律写成 (.metadata.name // .name) 这种容错形式，
#    但**第一次跑完必须人工看一眼 summary.txt 是不是真的填上了名字**，
#    全是 "?" 说明字段路径又变了。

set -euo pipefail

readonly EXPECTED_PROJECT_ID="oratis-491316"
readonly REGION="us-central1"

PROJECT_ID="${PROJECT_ID:-$EXPECTED_PROJECT_ID}"
OUT_DIR=""
DRY_RUN=0
QUIET=0

log()  { if [ "$QUIET" -eq 0 ]; then printf '%s\n' "$*" >&2; fi; }
step() { if [ "$QUIET" -eq 0 ]; then printf '\n\033[1m▸ %s\033[0m\n' "$*" >&2; fi; }
ok()   { if [ "$QUIET" -eq 0 ]; then printf '  ✓ %s\n' "$*" >&2; fi; }
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
用法: inventory.sh [选项]

跑 as-built-gcp.md §7 的清点命令，产出可 diff 的快照。**全部只读。**

选项:
  --out=<目录>    快照写到哪里。默认 ./inventory/<UTC 时间戳>
  --project=<id>  GCP 项目 ID。必须是 oratis-491316
  --dry-run       只打印将要执行的 gcloud 读命令，不真的调用
  --quiet         只在出错时输出（给 verify-isolation.sh 内部调用用）
  -h, --help      显示本帮助

产出:
  <目录>/*.json        jq -S 规范化的原始输出
  <目录>/summary.txt   一行一个资源的稳定文本（人读 + git diff 用）

典型用法:
  ./infra/scripts/inventory.sh --out=snapshots/before
  # …部署…
  ./infra/scripts/inventory.sh --out=snapshots/after
  diff -u snapshots/before/summary.txt snapshots/after/summary.txt

  # 只关心「现有服务有没有被动过」用这个，它带判定逻辑：
  ./infra/scripts/verify-isolation.sh --baseline=snapshots/before
EOF
}

guard_project() {
  if [ "$PROJECT_ID" != "$EXPECTED_PROJECT_ID" ]; then
    die "PROJECT_ID 必须是 $EXPECTED_PROJECT_ID，当前是 \"$PROJECT_ID\"。
     本仓库的全部资产清点与隔离承诺只对这一个项目成立（as-built-gcp.md）。"
  fi
}

need_cmd() {
  command -v "$1" >/dev/null 2>&1 || die "缺少命令：$1"
}

# snap <文件名> <gcloud 参数...>
# 失败不中止整个快照 —— 少一类资源的权限不该让整份清点作废，但要在 summary 里留痕。
snap() {
  local name="$1"; shift
  if [ "$DRY_RUN" -eq 1 ]; then
    local _a
    printf '  [dry-run] ' >&2
    for _a in "$@"; do qq "$_a" >&2; printf ' ' >&2; done
    printf -- '--format=json > %s/%s.json\n' "$OUT_DIR" "$name" >&2
    return 0
  fi
  local raw
  if ! raw="$("$@" --format=json 2>"${OUT_DIR}/${name}.err")"; then
    warn "${name}: gcloud 调用失败，详见 ${OUT_DIR}/${name}.err"
    printf '[]' > "${OUT_DIR}/${name}.json"
    printf 'FAILED\n' > "${OUT_DIR}/${name}.status"
    return 0
  fi
  rm -f "${OUT_DIR}/${name}.err"
  printf '%s' "$raw" | jq -S . > "${OUT_DIR}/${name}.json"
  printf 'OK\n' > "${OUT_DIR}/${name}.status"
  ok "$name"
}

# sum_lines <文件名> <jq 表达式> —— 表达式对每个元素产出一行字符串
sum_lines() {
  local name="$1" expr="$2"
  if [ "$DRY_RUN" -eq 1 ]; then
    return 0
  fi
  if [ ! -f "${OUT_DIR}/${name}.json" ]; then
    return 0
  fi
  {
    printf '### %s\n' "$name"
    jq -r "[.[]? | ${expr}] | sort | .[]" "${OUT_DIR}/${name}.json" 2>/dev/null || printf '(解析失败：字段路径可能已变，见本文件头部注释)\n'
    printf '\n'
  } >> "${OUT_DIR}/summary.txt"
}

collect() {
  step "清点 $PROJECT_ID"

  # ── as-built §7 的八条，一条不少、顺序不变 ──
  snap instances  gcloud compute instances list      --project="$PROJECT_ID"
  snap addresses  gcloud compute addresses list      --project="$PROJECT_ID"
  snap firewall   gcloud compute firewall-rules list --project="$PROJECT_ID"
  snap run        gcloud run services list           --project="$PROJECT_ID" --region="$REGION"
  snap secrets    gcloud secrets list                --project="$PROJECT_ID"
  snap artifacts  gcloud artifacts repositories list --project="$PROJECT_ID"
  snap sa         gcloud iam service-accounts list   --project="$PROJECT_ID"
  snap apis       gcloud services list --enabled     --project="$PROJECT_ID"

  # ── as-built §7 未覆盖的资源类型 ──
  # 不是遗漏：写 as-built 时（2026-08-16）项目里根本没有这些东西。
  # 它们现在全是 bp- 前缀的新资源，清点它们是为了让 babel.plus 自己也有 as-built。
  snap sql        gcloud sql instances list          --project="$PROJECT_ID"
  snap scheduler  gcloud scheduler jobs list         --project="$PROJECT_ID" --location="$REGION"
  snap queues     gcloud tasks queues list           --project="$PROJECT_ID" --location="$REGION"
  snap topics     gcloud pubsub topics list          --project="$PROJECT_ID"
  snap subs       gcloud pubsub subscriptions list   --project="$PROJECT_ID"
}

summarize() {
  if [ "$DRY_RUN" -eq 1 ]; then
    return 0
  fi
  {
    printf '# babel.plus GCP 清点快照\n'
    printf '# 项目: %s\n' "$PROJECT_ID"
    printf '# 时间: %s (UTC)\n' "$(date -u '+%Y-%m-%dT%H:%M:%SZ')"
    printf '# 来源: infra/scripts/inventory.sh（as-built-gcp.md §7）\n'
    printf '# 说明: 本文件是为 diff 设计的 —— 每行一个资源，行内按固定顺序列关键字段，整体排序。\n'
    printf '\n'
  } > "${OUT_DIR}/summary.txt"

  # 实例：名字 / 可用区 / 机型 / 外网 IP / 状态。
  # 外网 IP 必须进 summary —— as-built §2 记录 vpn-us 的静态 IP 已经轮换到第四代，
  # IP 变化在这个项目里是**已经反复发生过的事实**，不是理论风险。
  sum_lines instances '"\(.name)\t\(.zone // "?" | split("/") | last)\t\(.machineType // "?" | split("/") | last)\t\(.networkInterfaces[0].accessConfigs[0].natIP // "-")\t\(.status // "?")"'
  sum_lines addresses '"\(.name)\t\(.address // "?")\t\(.region // "?" | split("/") | last)\t\(.status // "?")"'
  # 防火墙：as-built §3 记录了 10 条规则，其中三条**没有 target tag** 因而对
  # default 网络里所有实例生效。所以 targetTags 必须出现在 summary 里。
  sum_lines firewall  '"\(.name)\t\(.direction // "?")\t\((.sourceRanges // []) | join(","))\t\((.targetTags // ["(无)"]) | join(","))\t\((.allowed // []) | map("\(.IPProtocol):\((.ports // ["all"]) | join(","))") | join(" "))"'
  sum_lines run       '"\(.metadata.name // .name // "?")\t\(.status.url // "?")\t\(.status.latestReadyRevisionName // "?")"'
  sum_lines secrets   '"\(.name // "?" | split("/") | last)\t\(.createTime // "?")"'
  sum_lines artifacts '"\(.name // "?" | split("/") | last)\t\(.format // "?")\t\(.sizeBytes // "?")"'
  sum_lines sa        '"\(.email // "?")\t\(.displayName // "-")"'
  sum_lines apis      '"\(.config.name // .name // "?")"'
  sum_lines sql       '"\(.name // "?")\t\(.databaseVersion // "?")\t\(.settings.tier // "?")\t\(.region // "?")\t\(.state // "?")"'
  sum_lines scheduler '"\(.name // "?" | split("/") | last)\t\(.schedule // "?")\t\(.state // "?")\t\(.httpTarget.uri // "-")"'
  sum_lines queues    '"\(.name // "?" | split("/") | last)\t\(.state // "?")"'
  sum_lines topics    '"\(.name // "?" | split("/") | last)"'
  sum_lines subs      '"\(.name // "?" | split("/") | last)\t\(.pushConfig.pushEndpoint // "(pull)")"'
}

main() {
  local arg
  for arg in "$@"; do
    case "$arg" in
      --out=*)     OUT_DIR="${arg#*=}" ;;
      --project=*) PROJECT_ID="${arg#*=}" ;;
      --dry-run)   DRY_RUN=1 ;;
      --quiet)     QUIET=1 ;;
      -h|--help)   usage; exit 0 ;;
      *)           usage >&2; die "未知参数：$arg" ;;
    esac
  done

  guard_project
  need_cmd gcloud
  need_cmd jq

  if [ -z "$OUT_DIR" ]; then
    OUT_DIR="./inventory/$(date -u '+%Y%m%dT%H%M%SZ')"
  fi
  if [ "$DRY_RUN" -eq 0 ]; then
    mkdir -p "$OUT_DIR"
  fi

  log "项目 : $PROJECT_ID"
  log "输出 : $OUT_DIR"
  if [ "$DRY_RUN" -eq 1 ]; then
    log "模式 : DRY-RUN（连只读调用都不发）"
  fi

  collect
  summarize

  if [ "$DRY_RUN" -eq 0 ]; then
    step "完成"
    log "  $OUT_DIR/summary.txt"
    log "  比对：diff -u <旧>/summary.txt $OUT_DIR/summary.txt"
    log "  判定：./infra/scripts/verify-isolation.sh --baseline=<旧>"
  fi
}

main "$@"

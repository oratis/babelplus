#!/usr/bin/env bash
#
# check-cert-issuer.sh —— 每日证书签发者核对，log-based metric `bp_cert_issuer_bad` 的信号源
#
# 事实源：
#   docs/04-ops/monitoring.md §8（核对机制、判定表、「只校验 O 不校验 CN」）
#                            · §3.1（日志指标的标签基数规矩）· §3.2（十条指标）
#                            · §5 第 15 条告警（bp_cert_issuer_bad > 0 单次即告警，P0）
#   docs/04-ops/deploy.md §11.2（钉 Let's Encrypt 与同一条 openssl 命令）
#   docs/05-adr/0004-transport-hardening.md §3.4（GTS 证书在中国触发 IP 级单向丢包）
#   infra/deploy/deploy-web.sh 的 check_cert_issuer（发布后的即时确认，与本脚本刻意重复）
#
# 🔴 这是 roadmap **B42** 三条建不了的指标之一：`bp_cert_issuer_bad` 建不了，
#    不是因为过滤器写不出来，而是因为**没有任何东西在写那条日志**。本脚本就是那个东西。
#
# 本脚本**不创建任何 GCP 资源**。它只做两件事：只读的 TLS 握手，以及
# 在判定不合格时写一条结构化日志（`gcloud logging write`，写日志不是建资源）。
# 指标本身要人工建一次，命令见 --help 末尾。
#
# ⚠️ 两条它**回答不了**的问题，别指望它（monitoring §8 末尾已记）：
#   1. 它从我们这一侧发起握手，看到的是我们发出去的证书。ADR 0004 §3.4 记录的失效是
#      **中国到我们的路径上的单向丢包** —— 「CA 变了」能发现，「中国用户连不上」发现不了。
#   2. `*.a.run.app` 的签发者**本来就是 GTS**（2026-08-21 实测，evidence/gcp-inventory-20260821 §2），
#      所以 run.app 主机名**不属于**本脚本的目标清单 —— 把它放进来只会得到一条永远为红的告警，
#      而长期为红的告警等于没有告警。目标清单只放三套域名池里**我们自己钉了 LE** 的域名。

set -euo pipefail

# ───────────────────────── 判定常量 ─────────────────────────
#
# 与 infra/deploy/deploy-web.sh 的同名常量刻意重复（六个脚本各自复制守卫代码的同一条理由：
# 每个脚本要能单独 scp 出去跑）。改一处要改两处，没有机制提醒 —— 代价记在 deploy/README.md §6。

# 只校验 O，不校验 CN：LE 会轮换中间证书（R10/R11/E5/E6…），钉 CN 会造成周期性误报，
# 而误报会让人关掉这条告警，最后等于没有它（monitoring §8）。
readonly CERT_ISSUER_REQUIRED="Let's Encrypt"
# 🔴 GTS 证书在中国触发 IP 级单向丢包（ADR 0004 §3.4，net4people/bbs #381：
#    抓包证据是证书消息之后单向丢包，不是 RST 注入）。现象酷似网络抖动，极难定位。
readonly CERT_ISSUER_FORBIDDEN="Google Trust Services"

# 已知的 GTS 中间证书 CN（monitoring §8 / deploy.md §11.2 的判定表原文）。
# 它是 O 解析失败时的兜底判据，不是主判据。
readonly FORBIDDEN_CNS="WE1 WR2 WR3 GTS CA 1C3 GTS CA 1D4 GTS CA 1P5"

# 剩余有效期告警线（monitoring §8：< 14 天）。
readonly EXPIRY_WARN_DAYS=14

# ───────────────────────── 日志常量 ─────────────────────────
#
# logName 与 event 名是 `bp_cert_issuer_bad` 过滤器的**唯一契约**。
# 改这两个常量 = 改指标过滤器，而**日志指标不追溯**（monitoring §3.1 第 1 条）：
# 改完之后到重建指标之间的那段时间，signal 是静默丢失的。所以别改。
readonly LOG_NAME="bp-cert-issuer-check"
readonly EVENT_BAD="cert_issuer_bad"          # 签发者与期望不符 → bp_cert_issuer_bad
readonly EVENT_EXPIRING="cert_expiring_soon"  # 剩余有效期 < EXPIRY_WARN_DAYS
readonly EVENT_UNREACHABLE="cert_check_failed"  # 握手失败 / 取不到证书

readonly EXPECTED_PROJECT_ID="oratis-491316"

# ───────────────────────── 运行时开关 ─────────────────────────

PROJECT_ID="${PROJECT_ID:-$EXPECTED_PROJECT_ID}"
DOMAINS=""
CONFIG_FILE=""
EXPECT_O="$CERT_ISSUER_REQUIRED"
DRY_RUN=0
LOG_ENABLED=1
REQUIRE_TARGETS=0
PASS_N=0
FAIL_N=0
WARN_N=0

# ───────────────────────── 通用工具（与 infra/ 下其它脚本刻意保持重复）─────────────────────────

log()  { printf '%s\n' "$*" >&2; }
step() { printf '\n\033[1m▸ %s\033[0m\n' "$*" >&2; }
pass() { printf '  \033[32m✓\033[0m %s\n' "$*" >&2; PASS_N=$((PASS_N + 1)); }
fail() { printf '  \033[31m✗ %s\033[0m\n' "$*" >&2; FAIL_N=$((FAIL_N + 1)); }
warn() { printf '  \033[33m⚠\033[0m %s\n' "$*" >&2; WARN_N=$((WARN_N + 1)); }
note() { printf '  · %s\n' "$*" >&2; }
die()  { printf '\n\033[31m✗ %s\033[0m\n' "$*" >&2; exit 2; }

# qq 打印一个参数：只在需要时加单引号。
# 不用 printf '%q' —— 它会把中文转成八进制转义（\346\216\247…），而 dry-run 的输出是给人读的。
qq() {
  case "$1" in
    ''|*[!A-Za-z0-9_@%+=:,./~-]*)
      printf "'%s'" "$(printf '%s' "$1" | sed "s/'/'\\\\''/g")" ;;
    *) printf '%s' "$1" ;;
  esac
}

need_cmd() {
  command -v "$1" >/dev/null 2>&1 || die "缺少命令：$1"
}

usage() {
  cat <<'EOF'
用法: check-cert-issuer.sh [选项]

对一组域名做 TLS 握手，核对证书签发者的 O 是否仍是期望值，不符就写一条结构化日志 ——
log-based metric bp_cert_issuer_bad 的信号源（monitoring.md §8、告警第 15 条 P0）。

**只读 + 写日志。不创建任何 GCP 资源。**

目标清单（三选一，从上到下优先）:
  --domains=a.com,b.com   直接给
  --config=<文件>         每行一个域名；# 开头与空行忽略
  环境变量                 BP_WEB_DOMAINS + BP_ADMIN_DOMAINS + BP_API_DOMAINS 三池取并集

  ⚠️ 清单为空是**当前的正常状态** —— 域名一个都还没注册（deploy/README.md §7）。
     此时脚本打一条提示并以 0 退出。接进定时作业之后请加 --require-targets，
     让「清单空了」变成一次响亮的失败而不是一次安静的空跑。

选项:
  --expect-o=<O>       期望的签发者 O。默认 "Let's Encrypt"
  --require-targets    目标清单为空时以 2 退出（给定时作业用）
  --no-log             只在终端判定，不往 Cloud Logging 写（本机临时核对时用）
  --project=<id>       GCP 项目 ID。必须是 oratis-491316
  --dry-run            只打印将要执行的写操作（写日志），TLS 握手照做
  -h, --help           显示本帮助

退出码:
  0  全部通过（或清单为空且未加 --require-targets）
  1  有域名不合格 / 取不到证书 —— 立即按 ADR 0004 §3.4 处置
  2  用法或环境错误

指标要人工建一次（本脚本刻意不建任何 GCP 资源）:
  gcloud logging metrics create bp_cert_issuer_bad \
    --project=oratis-491316 \
    --description='证书签发者与期望不符（check-cert-issuer.sh 写的日志）' \
    --log-filter='logName="projects/oratis-491316/logs/bp-cert-issuer-check"
                  AND jsonPayload.event="cert_issuer_bad"'

  🔴 日志指标**不追溯**（monitoring §3.1）：先建指标，再挂定时作业，顺序反了就丢数据。
EOF
}

guard_project() {
  if [ "$PROJECT_ID" != "$EXPECTED_PROJECT_ID" ]; then
    die "PROJECT_ID 必须是 $EXPECTED_PROJECT_ID，当前是 \"$PROJECT_ID\"。"
  fi
}

# ───────────────────────── 目标清单 ─────────────────────────

split_commas() {
  printf '%s' "$1" | tr ',' '\n' | sed 's/^[[:space:]]*//; s/[[:space:]]*$//' | grep -v '^$' || true
}

# collect_targets 打印去重后的目标域名，每行一个。
collect_targets() {
  if [ -n "$DOMAINS" ]; then
    split_commas "$DOMAINS" | sort -u
    return 0
  fi
  if [ -n "$CONFIG_FILE" ]; then
    if [ ! -f "$CONFIG_FILE" ]; then
      die "配置文件不存在：$CONFIG_FILE"
    fi
    sed 's/#.*//; s/^[[:space:]]*//; s/[[:space:]]*$//' "$CONFIG_FILE" | grep -v '^$' | sort -u || true
    return 0
  fi
  {
    split_commas "${BP_WEB_DOMAINS:-}"
    split_commas "${BP_ADMIN_DOMAINS:-}"
    split_commas "${BP_API_DOMAINS:-}"
  } | sort -u
}

# ───────────────────────── 结构化日志 ─────────────────────────

# json_escape 转义一个 JSON 字符串字面量的内容（不含两侧引号）。
# 控制字符直接删掉而不是转义 —— 它们只可能来自 openssl 的输出，没有保留价值，
# 而漏转义一个控制字符会让整条 payload 变成非法 JSON，那才是真的丢信号。
json_escape() {
  printf '%s' "$1" | tr -d '[:cntrl:]' | sed -e 's/\\/\\\\/g' -e 's/"/\\"/g'
}

# write_log <severity> <event> <domain> <reason> <message> <k=v>...
#
# 字段名对齐 api/cmd/server/main.go 的 newLogger：Cloud Logging 用 `severity`
# 与 `message`，其余业务字段平铺在 jsonPayload 下。
#
# 🔴 payload 里**不放**任何高基数字段（monitoring §3.1 第 2 条）。`reason` 是有界枚举
#    （forbidden_issuer / unexpected_issuer / no_issuer / handshake_failed / expiring_soon），
#    `domain` 的基数受三套域名池上限约束，且**不要**给它建 label extractor。
write_log() {
  local severity="$1" event="$2" domain="$3" reason="$4" message="$5"
  shift 5

  local payload
  payload="$(printf '{"event":"%s","message":"%s","domain":"%s","reason":"%s","expected_o":"%s","checked_by":"check-cert-issuer.sh"' \
    "$(json_escape "$event")" "$(json_escape "$message")" \
    "$(json_escape "$domain")" "$(json_escape "$reason")" \
    "$(json_escape "$EXPECT_O")")"

  local kv key val
  for kv in "$@"; do
    key="${kv%%=*}"
    val="${kv#*=}"
    payload="${payload},\"$(json_escape "$key")\":\"$(json_escape "$val")\""
  done
  payload="${payload}}"

  if [ "$LOG_ENABLED" -eq 0 ]; then
    note "[--no-log] 未写 Cloud Logging：$payload"
    return 0
  fi

  local -a cmd=(
    gcloud logging write "$LOG_NAME" "$payload"
    --project="$PROJECT_ID"
    --payload-type=json
    --severity="$severity"
  )

  if [ "$DRY_RUN" -eq 1 ]; then
    local _a
    printf '  [dry-run] ' >&2
    for _a in "${cmd[@]}"; do qq "$_a" >&2; printf ' ' >&2; done
    printf '\n' >&2
    return 0
  fi

  # 写日志失败**不能**吞掉：这条日志就是告警的唯一信号源，写不进去 = 这次检查等于没做。
  if ! "${cmd[@]}" >/dev/null; then
    fail "gcloud logging write 失败（logName=${LOG_NAME}）——
     判定结果没有进 Cloud Logging，bp_cert_issuer_bad 这次拿不到信号。"
    return 1
  fi
  note "已写入 logName=projects/${PROJECT_ID}/logs/${LOG_NAME}  severity=${severity}  event=${event}"
}

# ───────────────────────── 证书读取与解析 ─────────────────────────

# fetch_pem <域名> —— 打印证书 PEM；取不到就打印空。
fetch_pem() {
  local domain="$1"
  # 与 deploy.md §11.2 / monitoring §8 给值班的那条命令同构。
  # -verify_return_error 刻意**不加**：链验证失败也要能拿到叶证书来判断签发者是谁 ——
  # 「证书验不过」本身就是要报的东西，不是跳过检查的理由。
  echo | openssl s_client -servername "$domain" -connect "${domain}:443" 2>/dev/null \
    | openssl x509 2>/dev/null || true
}

# issuer_field <PEM> <字段名> —— 从 issuer DN 里取一个字段（organizationName / commonName）。
# 用 -nameopt multiline 是因为单行形式在 OpenSSL 3.x（逗号分隔）与 LibreSSL（斜杠分隔）
# 之间不一样，而 multiline 两边一致（2026-08-23 在 OpenSSL 3.6.3 与 LibreSSL 3.3.6 上实测）。
issuer_field() {
  printf '%s\n' "$1" | openssl x509 -noout -issuer -nameopt multiline 2>/dev/null \
    | awk -v k="$2" '
        $0 ~ "^[[:space:]]*" k "[[:space:]]*=" {
          sub("^[[:space:]]*" k "[[:space:]]*=[[:space:]]*", "")
          print
          exit
        }' || true
}

# cn_forbidden <CN> —— CN 是否在已知的 GTS 中间证书清单里。
cn_forbidden() {
  local cn="$1" bad
  [ -n "$cn" ] || return 1
  for bad in $FORBIDDEN_CNS; do
    if [ "$cn" = "$bad" ]; then
      return 0
    fi
  done
  # 「GTS CA 1C3」这类带空格的 CN 在上面的按词循环里会被拆开，单独再比一次整串。
  case " $FORBIDDEN_CNS " in
    *" $cn "*) return 0 ;;
  esac
  return 1
}

# ───────────────────────── 逐域名核对 ─────────────────────────

check_domain() {
  local domain="$1"
  local pem raw_issuer issuer_o issuer_cn not_after

  pem="$(fetch_pem "$domain")"
  if [ -z "$pem" ]; then
    fail "$domain 取不到证书（域名没解析 / 证书没签发 / 本机出网被拦 / 对端不接 443）
     取不到 = 判定不了 = **当作失败**（与 verify-isolation.sh 同一条口径）。"
    write_log WARNING "$EVENT_UNREACHABLE" "$domain" handshake_failed \
      "TLS 握手失败或取不到证书" || true
    return 1
  fi

  raw_issuer="$(printf '%s\n' "$pem" | openssl x509 -noout -issuer 2>/dev/null || true)"
  issuer_o="$(issuer_field "$pem" organizationName)"
  issuer_cn="$(issuer_field "$pem" commonName)"
  not_after="$(printf '%s\n' "$pem" | openssl x509 -noout -enddate 2>/dev/null | sed 's/^notAfter=//' || true)"

  local rc=0

  if [ -z "$issuer_o" ]; then
    # O 解析不出来时不静默通过：退回到整串 issuer 的子串匹配（与 deploy-web.sh 同法），
    # 仍判不了就当作不合格。
    case "$raw_issuer" in
      *"$CERT_ISSUER_FORBIDDEN"*)
        fail "$domain issuer = ${raw_issuer:-<空>} —— 含 ${CERT_ISSUER_FORBIDDEN}"
        write_log ERROR "$EVENT_BAD" "$domain" forbidden_issuer \
          "证书签发者是被禁止的 CA" "issuer=$raw_issuer" "not_after=$not_after" || true
        return 1
        ;;
      *"$EXPECT_O"*)
        pass "$domain issuer = $raw_issuer（O 未解析出，按整串匹配通过）"
        ;;
      *)
        fail "$domain 的 issuer O 解析不出来，整串是 ${raw_issuer:-<空>}"
        write_log WARNING "$EVENT_BAD" "$domain" no_issuer \
          "证书 issuer 的 O 解析不出来" "issuer=$raw_issuer" "not_after=$not_after" || true
        return 1
        ;;
    esac
  elif [ "$issuer_o" = "$EXPECT_O" ]; then
    # CN 只在这里做一次兜底：O 对但 CN 落在 GTS 清单里，说明有一侧在撒谎，要人看。
    # 前提是期望值本身不是 GTS —— 用 --expect-o 显式期望 GTS 时（比如临时核对 run.app），
    # 再拿 GTS 的中间证书 CN 判失败就是自相矛盾的误报。
    if [ "$EXPECT_O" != "$CERT_ISSUER_FORBIDDEN" ] && cn_forbidden "$issuer_cn"; then
      fail "$domain issuer O=${issuer_o} 但 CN=${issuer_cn} 属于 ${CERT_ISSUER_FORBIDDEN} 的中间证书"
      write_log ERROR "$EVENT_BAD" "$domain" forbidden_issuer \
        "签发者 O 与 CN 自相矛盾" "issuer_o=$issuer_o" "issuer_cn=$issuer_cn" "not_after=$not_after" || true
      rc=1
    else
      pass "$domain issuer O=${issuer_o} CN=${issuer_cn:-<无>}"
    fi
  else
    case "$issuer_o" in
      *"$CERT_ISSUER_FORBIDDEN"*)
        fail "🔴 $domain 的证书由 ${CERT_ISSUER_FORBIDDEN} 签发（CN=${issuer_cn:-?}）
     ADR 0004 §3.4：GTS 证书在中国触发 IP 级单向丢包，现象酷似网络抖动。
     处置：Cloudflare 后台 SSL/TLS → Edge Certificates → Universal SSL 的
     Certificate Authority 显式选 Let's Encrypt，等重新签发（可能数小时）后复核。"
        write_log ERROR "$EVENT_BAD" "$domain" forbidden_issuer \
          "证书签发者是被禁止的 CA" "issuer_o=$issuer_o" "issuer_cn=$issuer_cn" "not_after=$not_after" || true
        rc=1
        ;;
      *)
        # 既不是期望值也不是已知的坏值。**照样记 bp_cert_issuer_bad** ——
        # 这条指标要回答的是「签发者变了没有」，一个没见过的 CA 同样是变了，
        # 而且它比 GTS 更值得看一眼（可能是签发链被换掉）。severity 用 WARNING 区分。
        fail "$domain issuer O=${issuer_o}（期望 ${EXPECT_O}），CN=${issuer_cn:-<无>} —— 需人工判断"
        write_log WARNING "$EVENT_BAD" "$domain" unexpected_issuer \
          "证书签发者不是期望的 CA" "issuer_o=$issuer_o" "issuer_cn=$issuer_cn" "not_after=$not_after" || true
        rc=1
        ;;
    esac
  fi

  # 剩余有效期。用 -checkend 而不是解析 notAfter 再算天数：
  # BSD date 与 GNU date 的时间解析参数不兼容，-checkend 两边都有。
  if ! printf '%s\n' "$pem" | openssl x509 -noout -checkend $((EXPIRY_WARN_DAYS * 86400)) >/dev/null 2>&1; then
    warn "$domain 的证书将在 ${EXPIRY_WARN_DAYS} 天内过期（notAfter=${not_after:-?}）"
    write_log WARNING "$EVENT_EXPIRING" "$domain" expiring_soon \
      "证书剩余有效期不足 ${EXPIRY_WARN_DAYS} 天" "not_after=$not_after" || true
    # 刻意**不**计入 FAIL：过期预警是「该去续了」，不是「现在坏了」，
    # 把它算成失败会让定时作业在续签窗口里天天报红。
  fi

  return "$rc"
}

# ───────────────────────── 主流程 ─────────────────────────

main() {
  local arg
  for arg in "$@"; do
    case "$arg" in
      --domains=*)      DOMAINS="${arg#*=}" ;;
      --config=*)       CONFIG_FILE="${arg#*=}" ;;
      --expect-o=*)     EXPECT_O="${arg#*=}" ;;
      --require-targets) REQUIRE_TARGETS=1 ;;
      --no-log)         LOG_ENABLED=0 ;;
      --project=*)      PROJECT_ID="${arg#*=}" ;;
      --dry-run)        DRY_RUN=1 ;;
      -h|--help)        usage; exit 0 ;;
      *)                usage >&2; die "未知参数：$arg" ;;
    esac
  done

  guard_project
  need_cmd openssl

  local targets
  targets="$(collect_targets)"

  log "项目 : $PROJECT_ID"
  log "期望 : issuer O = $EXPECT_O"
  if [ "$DRY_RUN" -eq 1 ]; then
    log "模式 : DRY-RUN（TLS 握手照做，只有写日志被拦下）"
  fi

  if [ -z "$targets" ]; then
    step "目标清单为空"
    log "  · 没有可核对的域名。三套域名池（BP_WEB_DOMAINS / BP_ADMIN_DOMAINS / BP_API_DOMAINS）"
    log "    都没设，也没给 --domains= / --config=。"
    log "  · 这是**当前的正常状态** —— 域名一个都还没注册（infra/deploy/README.md §7）。"
    log "  ⚠️ 于是「钉 Let's Encrypt」这条承诺目前没有任何可执行形式在生效，"
    log "     bp_cert_issuer_bad 也不会有任何信号。第一个域名接入后必须回来把它填进清单。"
    if [ "$REQUIRE_TARGETS" -eq 1 ]; then
      die "--require-targets：目标清单为空，按失败处理。"
    fi
    exit 0
  fi

  if [ "$LOG_ENABLED" -eq 1 ]; then
    # 早失败：判定不合格时写不出日志，等于这次检查没有做。
    need_cmd gcloud
  fi

  step "逐域名核对签发者"
  local d
  while IFS= read -r d; do
    [ -n "$d" ] || continue
    check_domain "$d" || true
  done <<EOF
$targets
EOF

  step "结果"
  log "  通过 $PASS_N 项 / 失败 $FAIL_N 项 / 提醒 $WARN_N 项"
  if [ "$FAIL_N" -ne 0 ]; then
    log ""
    log "  🔴 有域名不合格。处置见 docs/04-ops/deploy.md §11.2 与 ADR 0004 §3.4。"
    log "     告警侧：bp_cert_issuer_bad 已拿到信号（monitoring §5 第 15 条，P0，单次即告警）。"
    exit 1
  fi
  log "  ✅ 全部域名的签发者仍是 ${EXPECT_O}。"
  exit 0
}

main "$@"

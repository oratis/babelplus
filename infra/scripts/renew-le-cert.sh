#!/usr/bin/env bash
#
# renew-le-cert.sh —— 给 web./api.babel.plus 续签 Let's Encrypt 证书并换到负载均衡器上
#
# 事实源：docs/04-ops/deploy.md §11.2（判定口径：**只校验 issuer 的 O，不校验 CN**）
#         docs/05-adr/0016-domain-babelplus.md §3.1（面向中国的入口必须钉 LE）
#         docs/05-adr/0004-transport-hardening.md §3.4（GTS 在中国触发 IP 级单向丢包）
#
# 🔴 为什么必须是 LE 而不是 Google 托管证书：
#    Google 托管证书一律由 GTS 签发，而 GTS 证书在中国触发 **IP 级单向丢包**
#    （2026-08-21 实测，evidence/gcp-inventory-20260821 §2）。失效形态是「慢」而不是「断」，
#    在我们当前的可观测性水平上发现不了 —— 这正是它危险的地方。
#    admin.babel.plus **刻意不在本脚本范围内**：它走 IAP，管理员本来就要先过
#    accounts.google.com（ADR 0003 §2.1 基线：95% 异常），GTS 对它不构成额外损失，
#    而 Google 托管证书会自动续期，少一处要维护的东西。
#
# ─────────────────── HTTP-01 为什么绕了一圈 ───────────────────
#
# 🔴 **GCS 硬拒 `.well-known/acme-challenge/` 这个对象路径**：
#      HTTPError 400: ACME HTTP challenges are not supported.
#    这是 Google 主动封堵，防的是「谁都能拿桶托管一个挑战响应去劫持域名」。
#    2026-09-01 实测踩到。
#    处置：对象存在 `acme/<token>`，由 URL map（bp-acme-http-lb）的
#    `urlRewrite.pathPrefixRewrite: /acme/` 把 `/.well-known/acme-challenge/` 前缀重写过去。
#    **两边必须一起改** —— 只改一边的现象是 LE 拿到 404，报「unauthorized」。
#
# 🔴 **挑战上传由宿主机做，certbot 在容器里跑**，两者靠共享目录接力。
#    原因很具体：本机没装 certbot（要容器），而 google/cloud-sdk 镜像在 arm64 上
#    跑 amd64 模拟时**容器内的 gcloud 自身不可用**（credentials.db 写入失败）。
#    2026-09-01 实测：只读挂 ~/.config/gcloud 会 "Read-only file system"，
#    复制成可写之后 gcloud 仍报诊断异常。所以让容器只做 certbot 该做的事。
#
# ⚠️ 挂载路径必须在 `/Users/...` 下：本机 Docker 是 colima，
#    `/private/tmp/...` 下的 bind mount 会得到**空目录**（不是报错，是静默为空）。
#
# 用法：
#   ./infra/scripts/renew-le-cert.sh --dry-run     # 只打印将要执行的动作
#   ./infra/scripts/renew-le-cert.sh --apply       # 真的续签并切换
#   ./infra/scripts/renew-le-cert.sh --check       # 只查当前签发者与剩余天数
#
# 续期时机：LE 证书 90 天有效期，剩余 < 30 天时才真的续（certbot 自己会判）。
#           `--apply` 在没到期时是安全的空跑。

set -euo pipefail

readonly EXPECTED_PROJECT_ID="oratis-491316"
readonly LB_IP="34.117.101.225"
readonly DOMAINS="web.babel.plus,api.babel.plus"
readonly CERT_NAME="bp-public"
readonly ACME_BUCKET="bp-acme-challenge"
readonly HTTPS_PROXY_NAME="bp-admin-https-proxy"
readonly ADMIN_CERT="bp-admin-cert"
readonly ACCOUNT_EMAIL="wangharp@gmail.com"
readonly STATE_SECRET="bp-acme-certbot-state"
readonly SDK_IMAGE="google/cloud-sdk:alpine"
# 工作目录必须在 $HOME 下（colima 的 bind mount 限制，见文件头）。
readonly WORK="${HOME}/.bp-acme-work"

PROJECT_ID="${PROJECT_ID:-$EXPECTED_PROJECT_ID}"
MODE="check"
WATCHER_PID=""

log()  { printf '%s\n' "$*" >&2; }
step() { printf '\n\033[1m▸ %s\033[0m\n' "$*" >&2; }
ok()   { printf '  ✓ %s\n' "$*" >&2; }
warn() { printf '  ⚠ %s\n' "$*" >&2; }
die()  { printf '\n\033[31m✗ %s\033[0m\n' "$*" >&2; exit 1; }

usage() {
  cat <<'EOF'
用法: renew-le-cert.sh [--check | --dry-run | --apply]

  --check    （默认）只查 web./api.babel.plus 当前的签发者与剩余天数
  --dry-run  打印将要执行的动作，不做任何写操作
  --apply    真的续签并把新证书换到负载均衡器上

续期后不需要改任何别的东西：证书资源名带日期后缀，脚本自己换 target-https-proxy，
admin.babel.plus 用的 Google 托管证书不受影响。
EOF
}

while [ $# -gt 0 ]; do
  case "$1" in
    --check)   MODE="check";   shift ;;
    --dry-run) MODE="dry-run"; shift ;;
    --apply)   MODE="apply";   shift ;;
    -h|--help) usage; exit 0 ;;
    *) die "未知参数：$1（--help 看用法）" ;;
  esac
done

[ "$PROJECT_ID" = "$EXPECTED_PROJECT_ID" ] || die "PROJECT_ID 必须是 $EXPECTED_PROJECT_ID"

# ───────────────────────── 只读检查 ─────────────────────────

check_issuers() {
  step "当前签发者与有效期"
  local d bad=0
  for d in ${DOMAINS//,/ }; do
    local txt issuer enddate days
    txt=$(echo | openssl s_client -servername "$d" -connect "${LB_IP}:443" 2>/dev/null \
          | openssl x509 -noout -issuer -enddate 2>/dev/null || true)
    if [ -z "$txt" ]; then
      warn "$d 取不到证书"; bad=1; continue
    fi
    issuer=$(printf '%s' "$txt" | sed -n 's/^issuer=//p')
    enddate=$(printf '%s' "$txt" | sed -n 's/^notAfter=//p')
    # 只校验 O，不校验 CN —— LE 会轮换中间证书（实测见过 YE1/YR1/YR2），钉 CN 会误报。
    case "$issuer" in
      *"O = Let's Encrypt"*|*"O=Let's Encrypt"*) ;;
      *) warn "$d 签发者不是 Let's Encrypt：$issuer"; bad=1 ;;
    esac
    days=$(( ( $(date -j -f "%b %e %T %Y %Z" "$enddate" +%s 2>/dev/null \
                 || date -d "$enddate" +%s 2>/dev/null || echo 0) - $(date +%s) ) / 86400 ))
    ok "$d · $issuer · 剩余 ${days} 天（到期 $enddate）"
    [ "$days" -lt 21 ] && warn "$d 剩余不足 21 天，应当尽快 --apply"
  done
  return $bad
}

# ───────────────────────── 续签 ─────────────────────────

start_watcher() {
  # 宿主机侧：把容器写进 pending/ 的挑战响应传到 GCS（理由见文件头）。
  mkdir -p "${WORK}/work/pending"
  (
    for _ in $(seq 1 300); do
      for f in "${WORK}/work/pending"/*; do
        [ -e "$f" ] || continue
        gcloud storage cp --cache-control=no-cache "$f" \
          "gs://${ACME_BUCKET}/acme/$(basename "$f")" --project="$PROJECT_ID" \
          >>"${WORK}/watcher.log" 2>&1 && rm -f "$f"
      done
      sleep 2
    done
  ) &
  WATCHER_PID=$!
  ok "挑战上传 watcher 已启动（pid $WATCHER_PID）"
}

stop_watcher() {
  [ -n "$WATCHER_PID" ] && kill "$WATCHER_PID" 2>/dev/null || true
  gcloud storage rm -r "gs://${ACME_BUCKET}/acme/**" --project="$PROJECT_ID" >/dev/null 2>&1 || true
}

write_hooks() {
  mkdir -p "${WORK}/hooks"
  cat > "${WORK}/hooks/auth.sh" <<'HOOK'
#!/bin/sh
set -eu
echo "$CERTBOT_VALIDATION" > "/work/pending/${CERTBOT_TOKEN}"
i=0
while [ "$i" -lt 40 ]; do
  got=$(wget -qO- --timeout=8 "http://34.117.101.225/.well-known/acme-challenge/${CERTBOT_TOKEN}" \
        --header="Host: ${CERTBOT_DOMAIN}" 2>/dev/null || true)
  [ "$got" = "$CERTBOT_VALIDATION" ] && exit 0
  i=$((i+1)); sleep 3
done
echo "挑战文件 120 秒内未变为可读：${CERTBOT_DOMAIN}/${CERTBOT_TOKEN}" >&2
exit 1
HOOK
  cat > "${WORK}/hooks/cleanup.sh" <<'HOOK'
#!/bin/sh
exit 0
HOOK
  chmod +x "${WORK}/hooks"/*.sh
}

restore_state() {
  mkdir -p "${WORK}/letsencrypt"
  if [ -d "${WORK}/letsencrypt/accounts" ]; then
    ok "本机已有 certbot 账号状态，跳过恢复"
    return
  fi
  gcloud secrets versions access latest --secret="$STATE_SECRET" --project="$PROJECT_ID" \
    > "${WORK}/state.tgz" 2>/dev/null || die "取不到 ACME 账号状态（secret ${STATE_SECRET}）"
  tar xzf "${WORK}/state.tgz" -C "${WORK}/letsencrypt"
  rm -f "${WORK}/state.tgz"
  ok "已从 Secret Manager 恢复 certbot 账号状态"
}

do_renew() {
  step "续签"
  restore_state
  write_hooks
  start_watcher
  trap stop_watcher EXIT

  docker run --rm \
    -v "${WORK}/hooks":/hooks -v "${WORK}/letsencrypt":/etc/letsencrypt -v "${WORK}/work":/work \
    -e HTTP_PROXY= -e HTTPS_PROXY= -e NO_PROXY='*' \
    "$SDK_IMAGE" sh -c "
      apk add --no-cache certbot >/dev/null 2>&1
      certbot certonly --manual --preferred-challenges http --agree-tos --no-eff-email \
        --email ${ACCOUNT_EMAIL} \
        --manual-auth-hook /hooks/auth.sh --manual-cleanup-hook /hooks/cleanup.sh \
        -d ${DOMAINS//,/ -d } \
        --cert-name ${CERT_NAME} --keep-until-expiring --non-interactive
    " || die "certbot 失败（挑战路径不通时先跑 --check 看 80 端口与 URL map 重写）"

  stop_watcher; trap - EXIT

  # 从容器里把证书拷出来（live/ 是符号链接，要 -L；archive/ 是 0700，宿主机读不到）
  mkdir -p "${WORK}/out"
  docker run --rm -v "${WORK}/letsencrypt":/etc/letsencrypt -v "${WORK}/out":/out \
    "$SDK_IMAGE" sh -c "cp -L /etc/letsencrypt/live/${CERT_NAME}/fullchain.pem \
      /etc/letsencrypt/live/${CERT_NAME}/privkey.pem /out/ && chmod 644 /out/*.pem" \
    >/dev/null 2>&1 || die "从容器取证书失败"

  local new_name
  new_name="bp-public-le-$(date -u +%Y%m%d%H%M)"
  step "上传新证书并切换：$new_name"
  gcloud compute ssl-certificates create "$new_name" \
    --certificate="${WORK}/out/fullchain.pem" --private-key="${WORK}/out/privkey.pem" \
    --global --project="$PROJECT_ID" >/dev/null || die "上传证书失败"
  ok "已上传 $new_name"

  gcloud compute target-https-proxies update "$HTTPS_PROXY_NAME" \
    --ssl-certificates="${ADMIN_CERT},${new_name}" \
    --global --project="$PROJECT_ID" >/dev/null || die "切换证书失败"
  ok "已切到 $HTTPS_PROXY_NAME"

  # 账号状态可能有更新（regr/renewal 配置），回写 secret。
  docker run --rm -v "${WORK}/letsencrypt":/le -v "${WORK}/out":/out "$SDK_IMAGE" \
    sh -c 'cd /le && tar czf /out/certbot-state.tgz accounts renewal renewal-hooks 2>/dev/null; chmod 644 /out/certbot-state.tgz' \
    >/dev/null 2>&1 || true
  if [ -f "${WORK}/out/certbot-state.tgz" ]; then
    gcloud secrets versions add "$STATE_SECRET" --data-file="${WORK}/out/certbot-state.tgz" \
      --project="$PROJECT_ID" >/dev/null 2>&1 && ok "certbot 账号状态已回写 Secret Manager"
  fi

  step "验证（边缘传播通常 1–5 分钟）"
  local tries=0
  while [ "$tries" -lt 30 ]; do
    if check_issuers >/dev/null 2>&1; then ok "新证书已在边缘生效"; break; fi
    tries=$((tries + 1))
    sleep 15
  done
  check_issuers || warn "边缘可能还在传播，几分钟后再跑一次 --check"

  # 旧证书**不在这里删**：删早了会在传播窗口里让正在握手的连接拿不到证书。
  step "遗留清理（人工，确认新证书生效之后再做）"
  log "  gcloud compute ssl-certificates list --global --project=${PROJECT_ID} --filter='name~bp-public'"
  log "  gcloud compute ssl-certificates delete <旧的那个> --global --project=${PROJECT_ID}"
}

main() {
  case "$MODE" in
    check) check_issuers ;;
    dry-run)
      step "dry-run：将要执行的动作"
      log "  1. 从 Secret Manager 恢复 certbot 账号状态（${STATE_SECRET}）"
      log "  2. 启动宿主机挑战上传 watcher → gs://${ACME_BUCKET}/acme/"
      log "  3. 容器内跑 certbot（HTTP-01，域名：${DOMAINS}）"
      log "  4. gcloud compute ssl-certificates create bp-public-le-<时间戳>"
      log "  5. gcloud compute target-https-proxies update ${HTTPS_PROXY_NAME}"
      log "  6. 回写账号状态 + 验证签发者"
      check_issuers
      ;;
    apply) do_renew ;;
  esac
}

main

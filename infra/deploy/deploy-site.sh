#!/usr/bin/env bash
#
# deploy-site.sh —— 官网 babel.plus（Cloud Run 服务 bp-site）的发布与接线
#
# 事实源：
#   web/site/（Vite 静态站，产出零 JavaScript）
#   infra/site/Dockerfile · nginx.conf（**在仓库里**，与 bp-web/bp-admin 的欠账相反）
#   docs/04-ops/first-deploy-20260831.md §4.1（GCLB 的现状：一个 IP、一个 url-map、四个后端）
#   docs/05-adr/0004-transport-hardening.md §3.4（面向中国的入口**必须钉 LE**，禁 GTS）
#
# 🔴 这个脚本**不签发证书**。apex 的证书由 `infra/scripts/renew-le-cert.sh` 一并签
#    （它的 DOMAINS 已包含 babel.plus 与 www.babel.plus），理由是那条 ACME 链路有三个
#    非显然的坑（GCS 硬拒 .well-known 路径、容器内 gcloud 不可用、colima 的 bind mount
#    静默为空），不值得在这里复制第二份。**顺序是：DNS → 本脚本 → renew-le-cert.sh。**
#
# 🔴 也**不改 DNS**。`babel.plus` 的 NS 在阿里云（ADR 0016），改解析要 AK/SK，
#    而那对密钥的用途只有一个（DNS-01 与解析），不该被一个发布脚本顺手拿到。
#    脚本会打印该建的记录并**自己核对解析是否已生效**，不生效就拒绝往下走。
#
# 用法：
#   ./infra/deploy/deploy-site.sh --dry-run        # 默认：只打印将要执行的动作
#   ./infra/deploy/deploy-site.sh --apply          # 真的建资源
#   ./infra/deploy/deploy-site.sh --apply --skip-dns-check   # 解析还没生效时先发服务（HTTPS 会先不可用）

set -euo pipefail

readonly EXPECTED_PROJECT_ID="oratis-491316"
readonly REGION="us-central1"
readonly SERVICE="bp-site"
readonly NEG="bp-site-neg"
readonly BACKEND="bp-site-backend"
readonly URL_MAP="bp-admin-lb"
readonly PATH_MATCHER="site-paths"
readonly AR_HOST="us-central1-docker.pkg.dev"
readonly AR_REPO="bp-images"
readonly LB_IP="34.117.101.225"
readonly APEX="babel.plus"
readonly WWW="www.babel.plus"
readonly RUN_SA="bp-site-sa"

# 站点的构建期变量。空值就是空值 —— 空则渲染成不可点，不编域名（AGENTS.md §3）。
readonly ACCOUNT_URL="${BP_ACCOUNT_URL:-https://web.babel.plus}"
readonly HELP_URL="${BP_HELP_URL:-}"
readonly STATUS_URL="${BP_STATUS_URL:-}"

PROJECT_ID="${PROJECT_ID:-$EXPECTED_PROJECT_ID}"
DRY_RUN=1
SKIP_DNS_CHECK=0

log()  { printf '%s\n' "$*" >&2; }
step() { printf '\n\033[1m▸ %s\033[0m\n' "$*" >&2; }
ok()   { printf '  ✓ %s\n' "$*" >&2; }
skip() { printf '  · %s\n' "$*" >&2; }
warn() { printf '  ⚠ %s\n' "$*" >&2; }
die()  { printf '\n\033[31m✗ %s\033[0m\n' "$*" >&2; exit 1; }

run() {
  if [ "$DRY_RUN" = 1 ]; then
    printf '  [dry-run] %s\n' "$*" >&2
  else
    "$@"
  fi
}

usage() {
  cat <<'EOF'
用法: deploy-site.sh [--apply|--dry-run] [--skip-dns-check]

发布官网 babel.plus：构建镜像 → Cloud Run bp-site → serverless NEG → 后端服务 →
在既有 url-map（bp-admin-lb）上加 babel.plus / www.babel.plus 两条主机规则。

前置（脚本会检查，不会替你做）：
  1. DNS：babel.plus 与 www.babel.plus 的 A 记录指向 34.117.101.225（阿里云控制台或 aliyun CLI）
  2. 证书：跑完本脚本之后跑 infra/scripts/renew-le-cert.sh --apply
     （它的 DOMAINS 已含 apex 与 www；**没有它 HTTPS 会用错证书而告警**）

环境变量：
  BP_ACCOUNT_URL   会员中心地址，默认 https://web.babel.plus
  BP_HELP_URL      帮助站；未建时留空（页面渲染成不可点，不编域名）
  BP_STATUS_URL    状态页；同上
EOF
}

while [ $# -gt 0 ]; do
  case "$1" in
    --apply) DRY_RUN=0 ;;
    --dry-run) DRY_RUN=1 ;;
    --skip-dns-check) SKIP_DNS_CHECK=1 ;;
    -h|--help) usage; exit 0 ;;
    *) die "未知参数：$1（--help 看用法）" ;;
  esac
  shift
done

# ───────────────────────── 防呆 ─────────────────────────

guard_project() {
  [ "$PROJECT_ID" = "$EXPECTED_PROJECT_ID" ] ||
    die "PROJECT_ID 必须是 ${EXPECTED_PROJECT_ID}，当前是 \"$PROJECT_ID\"。"
  local active
  active="$(gcloud config get-value project 2>/dev/null || true)"
  if [ "$active" != "$EXPECTED_PROJECT_ID" ]; then
    # 这个 GCP 项目是共享的（AGENTS.md §4：lisa-* 与 vpn-* 不要碰）。
    # 不 set 别人的活动项目，只要求每条命令都显式带 --project。
    warn "gcloud 活动项目是 \"${active:-<未设置>}\"，脚本会对每条命令显式带 --project=${PROJECT_ID}"
  fi
}

check_dns() {
  step "核对 DNS（apex 必须已经指向负载均衡器，否则证书签不出来）"
  local bad=0 got
  for d in "$APEX" "$WWW"; do
    got="$(dig +short A "$d" 2>/dev/null | head -n 1 || true)"
    if [ "$got" = "$LB_IP" ]; then
      ok "$d → $got"
    else
      warn "$d → ${got:-<无记录>}（期望 $LB_IP）"
      bad=1
    fi
  done
  if [ "$bad" = 1 ]; then
    log ""
    log "  在阿里云为 babel.plus 建两条记录（控制台或 aliyun CLI）："
    log "    A  @    $LB_IP"
    log "    A  www  $LB_IP"
    log ""
    [ "$SKIP_DNS_CHECK" = 1 ] || die "DNS 未就绪。--skip-dns-check 可先发服务，但 HTTPS 在证书签出来之前不可用。"
    warn "已按 --skip-dns-check 继续"
  fi
}

# ───────────────────────── 步骤 ─────────────────────────

build_image() {
  step "构建镜像"
  local sha ref ctx
  sha="$(git rev-parse --short=11 HEAD)"
  ref="${AR_HOST}/${PROJECT_ID}/${AR_REPO}/${SERVICE}:${sha}"
  # 🔴 构建上下文裁到只含 web/ 与 infra/site/：仓库根带着 node_modules 与 .gomodcache，
  #    整个上传既慢又没必要（同 infra/jobs/cert-issuer-check/build.sh 的做法）。
  ctx="$(mktemp -d "${TMPDIR:-/tmp}/bp-site-ctx.XXXXXX")"
  trap 'rm -rf "$ctx"' RETURN
  if [ "$DRY_RUN" = 0 ]; then
    mkdir -p "$ctx/web" "$ctx/infra/site"
    # rsync 排除 node_modules / dist：镜像里会重新装、重新构建。
    rsync -a --exclude node_modules --exclude dist --exclude 'vendor' web/ "$ctx/web/"
    cp infra/site/Dockerfile infra/site/nginx.conf "$ctx/infra/site/"
    # 站点的构建期变量经 .env.production 进 Vite（vite.config.ts 用 loadEnv 读）。
    {
      printf 'VITE_BP_ACCOUNT_URL=%s\n' "$ACCOUNT_URL"
      printf 'VITE_BP_HELP_URL=%s\n' "$HELP_URL"
      printf 'VITE_BP_STATUS_URL=%s\n' "$STATUS_URL"
    } > "$ctx/web/site/.env.production"
    ( cd "$ctx" && gcloud builds submit . --project="$PROJECT_ID" --tag="$ref" --timeout=15m >&2 )
  else
    log "  [dry-run] rsync web/ → 临时上下文；gcloud builds submit --tag=$ref"
  fi
  printf '%s' "$ref"
}

deploy_run() {
  local ref="$1"
  step "部署 Cloud Run $SERVICE"
  # 零角色服务账号：站点不访问任何 GCP 资源（AGENTS.md §4 禁止复用 Compute 默认 SA）。
  if ! gcloud iam service-accounts describe "${RUN_SA}@${PROJECT_ID}.iam.gserviceaccount.com" \
        --project="$PROJECT_ID" >/dev/null 2>&1; then
    run gcloud iam service-accounts create "$RUN_SA" --project="$PROJECT_ID" \
      --display-name="babel.plus 官网（无任何角色）"
  else
    skip "服务账号已存在：$RUN_SA"
  fi
  run gcloud run deploy "$SERVICE" \
    --project="$PROJECT_ID" --region="$REGION" \
    --image="$ref" \
    --service-account="${RUN_SA}@${PROJECT_ID}.iam.gserviceaccount.com" \
    --allow-unauthenticated \
    --port=8080 --cpu=1 --memory=256Mi --min-instances=0 --max-instances=4 \
    --ingress=all --quiet
}

wire_lb() {
  step "接进负载均衡器（NEG → 后端 → 主机规则）"
  if ! gcloud compute network-endpoint-groups describe "$NEG" --project="$PROJECT_ID" --region="$REGION" >/dev/null 2>&1; then
    run gcloud compute network-endpoint-groups create "$NEG" \
      --project="$PROJECT_ID" --region="$REGION" \
      --network-endpoint-type=serverless --cloud-run-service="$SERVICE"
  else
    skip "NEG 已存在：$NEG"
  fi

  if ! gcloud compute backend-services describe "$BACKEND" --project="$PROJECT_ID" --global >/dev/null 2>&1; then
    # 🔴 serverless NEG 的后端服务**不能带 --protocol**（first-deploy §4.1 实测踩到，删了重建）。
    run gcloud compute backend-services create "$BACKEND" --project="$PROJECT_ID" --global
    run gcloud compute backend-services add-backend "$BACKEND" \
      --project="$PROJECT_ID" --global \
      --network-endpoint-group="$NEG" --network-endpoint-group-region="$REGION"
  else
    skip "后端服务已存在：$BACKEND"
  fi

  # 主机规则：apex 与 www 都指向站点。
  if gcloud compute url-maps describe "$URL_MAP" --project="$PROJECT_ID" \
       --format='value(hostRules[].hosts)' 2>/dev/null | grep -q "$APEX"; then
    skip "url-map 已有 $APEX 的主机规则"
  else
    run gcloud compute url-maps add-path-matcher "$URL_MAP" \
      --project="$PROJECT_ID" \
      --path-matcher-name="$PATH_MATCHER" \
      --default-service="$BACKEND" \
      --new-hosts="${APEX},${WWW}"
  fi
}

verify() {
  step "验收"
  if [ "$DRY_RUN" = 1 ]; then
    skip "dry-run 跳过验收"
    return
  fi
  local url
  url="$(gcloud run services describe "$SERVICE" --project="$PROJECT_ID" --region="$REGION" \
        --format='value(status.url)')"
  ok "Cloud Run 直连：$url"
  if curl -fsS -o /dev/null -w '  ✓ %{http_code} %{size_download} bytes\n' "$url/" 2>/dev/null; then
    :
  else
    warn "Cloud Run 直连取不到首页"
  fi
  # 经负载均衡器（证书没签出来之前 HTTPS 会告警，所以这里用 --resolve 走 IP + -k 只验通路）
  if curl -fsSk -o /dev/null -w '  ✓ 经 LB：%{http_code}\n' --resolve "${APEX}:443:${LB_IP}" "https://${APEX}/" 2>/dev/null; then
    :
  else
    warn "经 LB 取不到（证书或主机规则还没生效；证书见 renew-le-cert.sh）"
  fi
  log ""
  log "  下一步：./infra/scripts/renew-le-cert.sh --apply   # 给 apex 与 www 签 LE 证书"
}

main() {
  command -v gcloud >/dev/null || die "缺 gcloud"
  command -v dig >/dev/null || die "缺 dig"
  guard_project
  log "项目 : ${PROJECT_ID}"
  log "模式 : $([ "$DRY_RUN" = 1 ] && echo 'DRY-RUN（不发任何写操作）' || echo 'APPLY —— 会真的改 GCP')"
  log "站点变量 : account=${ACCOUNT_URL} help=${HELP_URL:-<空>} status=${STATUS_URL:-<空>}"
  check_dns
  local ref
  ref="$(build_image)"
  deploy_run "$ref"
  wire_lb
  verify
}

main "$@"

#!/usr/bin/env bash
#
# deploy-spa.sh —— 用户面板 bp-web / 后台 bp-admin 的**现行**发布路径：Cloud Build 出镜像 → Cloud Run 换修订版
#
# 事实源：
#   docs/04-ops/first-deploy-20260831.md §4（两个 SPA 实际跑在 Cloud Run + nginx 上，经 GCLB bp-admin-lb
#   的 web. / admin. 主机规则；`/runtime-config.js` 由 backend bucket 接管）
#   infra/web/Dockerfile · nginx.conf（2026-09-05 进仓库，关掉 first-deploy 「无法照着仓库重发」那条欠账）
#   infra/deploy/deploy-site.sh（同一套写法：裁剪上下文、显式判构建失败、只读验收）
#
# 与 deploy-web.sh 的关系：那份脚本发布到 Cloudflare / GitHub / Netlify，是 ADR 0003 的**目标形态**，
# 至今一个目标都没用上（凭据与裁决都还没有）。本脚本是**今天真在跑**的形态。两份并存，
# 哪天 ADR 0003 落定就删本脚本。
#
# 🔴 不碰 LB：NEG / 后端 / 主机规则 / backend bucket 早已存在（first-deploy §4.1），本脚本只换 Cloud Run 修订版。
# 🔴 不改服务账号、不改并发与实例上限：照 `gcloud run services describe` 读到的现值写死在下面，
#    改这些属于另一次裁决（AGENTS.md §4 对默认 Compute SA 的红线在 first-deploy 里登记为欠账，本脚本不顺手改）。
#
# 用法：
#   ./infra/deploy/deploy-spa.sh --app=web   --dry-run     # 默认 dry-run
#   ./infra/deploy/deploy-spa.sh --app=admin --apply
#
# 部署前后各跑一次 infra/scripts/verify-isolation.sh（deploy.md §2，不允许跳过）—— 本脚本不替你跑。

set -euo pipefail

readonly EXPECTED_PROJECT_ID="oratis-491316"
readonly REGION="us-central1"
readonly AR_HOST="us-central1-docker.pkg.dev"
readonly AR_REPO="bp-images"

PROJECT_ID="${PROJECT_ID:-$EXPECTED_PROJECT_ID}"
DRY_RUN=1
APP=""

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
用法: deploy-spa.sh --app=web|admin [--apply|--dry-run]

  --app=web     用户面板：包 @babelplus/user → Cloud Run 服务 bp-web（经 LB 的 web.babel.plus）
  --app=admin   后台：    包 @babelplus/admin → Cloud Run 服务 bp-admin（经 LB 的 admin.babel.plus，IAP）
  --apply       真的构建并部署；默认 dry-run 只打印
EOF
}

while [ $# -gt 0 ]; do
  case "$1" in
    --app=*) APP="${1#*=}" ;;
    --apply) DRY_RUN=0 ;;
    --dry-run) DRY_RUN=1 ;;
    -h|--help) usage; exit 0 ;;
    *) die "未知参数：$1（--help 看用法）" ;;
  esac
  shift
done

case "$APP" in
  web)   PKG="user";  SERVICE="bp-web";   HOST="web.babel.plus";   CONCURRENCY=200 ;;
  admin) PKG="admin"; SERVICE="bp-admin"; HOST="admin.babel.plus"; CONCURRENCY=200 ;;
  *) usage >&2; die "--app 必须是 web 或 admin" ;;
esac

guard_project() {
  [ "$PROJECT_ID" = "$EXPECTED_PROJECT_ID" ] || die "PROJECT_ID 必须是 ${EXPECTED_PROJECT_ID}，当前是 \"$PROJECT_ID\"。"
  local active
  active="$(gcloud config get-value project 2>/dev/null || true)"
  [ "$active" = "$EXPECTED_PROJECT_ID" ] || warn "gcloud 活动项目是 \"${active:-<未设置>}\"，脚本对每条命令显式带 --project=${PROJECT_ID}"
  gcloud run services describe "$SERVICE" --project="$PROJECT_ID" --region="$REGION" --format='value(metadata.name)' >/dev/null 2>&1 \
    || die "Cloud Run 服务 $SERVICE 不存在。本脚本只换修订版，不建服务、不接 LB（那是 first-deploy §4 的一次性动作）。"
}

# 现值：只读出来给人看，部署命令里不带 --service-account（沿用服务现有的 SA）。
show_current() {
  step "现状 $SERVICE"
  gcloud run services describe "$SERVICE" --project="$PROJECT_ID" --region="$REGION" \
    --format='value(status.latestReadyRevisionName,spec.template.spec.containers[0].image,spec.template.spec.serviceAccountName)' 2>/dev/null \
    | tr '\t' '\n' | sed 's/^/  · /' >&2
}

build_image() {
  step "构建镜像（Cloud Build）"
  local sha ref ctx
  sha="$(git rev-parse --short=11 HEAD)"
  ref="${AR_HOST}/${PROJECT_ID}/${AR_REPO}/${SERVICE}:${sha}"
  ctx="$(mktemp -d "${TMPDIR:-/tmp}/bp-${SERVICE}-ctx.XXXXXX")"
  trap 'rm -rf "$ctx"' RETURN
  if [ "$DRY_RUN" = 0 ]; then
    mkdir -p "$ctx/web" "$ctx/infra/web"
    rsync -a --exclude node_modules --exclude dist web/ "$ctx/web/"
    cp infra/web/Dockerfile infra/web/nginx.conf infra/web/headers.conf "$ctx/infra/web/"
    # `gcloud builds submit --tag` 要求上下文根有 Dockerfile（deploy-site.sh 同款坑）。
    cp infra/web/Dockerfile "$ctx/Dockerfile"
    # 构建参数走 cloudbuild.yaml（--tag 形态不接受 --build-arg）。
    cat > "$ctx/cloudbuild.yaml" <<YAML
steps:
  - name: gcr.io/cloud-builders/docker
    args: ['build', '--build-arg', 'APP=${PKG}', '-t', '${ref}', '.']
images: ['${ref}']
timeout: 900s
YAML
    if ! ( cd "$ctx" && gcloud builds submit . --project="$PROJECT_ID" --config=cloudbuild.yaml --timeout=15m >&2 ); then
      die "镜像构建失败，未部署任何东西"
    fi
  else
    log "  [dry-run] rsync web/ + infra/web/ → 临时上下文；docker build --build-arg APP=${PKG} -t ${ref}"
  fi
  printf '%s' "$ref"
}

deploy_run() {
  local ref="$1"
  step "部署 Cloud Run $SERVICE（沿用现有服务账号与 ingress，只换镜像）"
  run gcloud run deploy "$SERVICE" \
    --project="$PROJECT_ID" --region="$REGION" \
    --image="$ref" \
    --allow-unauthenticated \
    --port=8080 --cpu=1 --memory=256Mi --min-instances=0 --max-instances=4 \
    --concurrency="$CONCURRENCY" --ingress=all --quiet
}

verify() {
  step "验收（只读）"
  if [ "$DRY_RUN" = 1 ]; then skip "dry-run 跳过验收"; return 0; fi
  local url rev code
  rev="$(gcloud run services describe "$SERVICE" --project="$PROJECT_ID" --region="$REGION" --format='value(status.latestReadyRevisionName)')"
  url="$(gcloud run services describe "$SERVICE" --project="$PROJECT_ID" --region="$REGION" --format='value(status.url)')"
  ok "最新修订版：$rev"
  code="$(curl -sS -m 30 -o /dev/null -w '%{http_code}' "${url}/-/healthz" || echo 000)"
  if [ "$code" = "200" ]; then ok "run.app /-/healthz → 200"; else warn "run.app /-/healthz → $code"; fi
  code="$(curl -sS -m 30 -o /dev/null -w '%{http_code}' "${url}/" || echo 000)"
  if [ "$code" = "200" ]; then ok "run.app / → 200"; else warn "run.app / → $code"; fi
  # 经 LB：web. 应当 200；admin. 应当被 IAP 挡成 302（能挡说明路由与 IAP 都在）。
  code="$(curl -sS -m 40 -o /dev/null -w '%{http_code}' "https://${HOST}/" || echo 000)"
  ok "https://${HOST}/ → $code（web 期望 200，admin 期望 302 IAP）"
}

guard_project
show_current
ref="$(build_image)"
deploy_run "$ref"
verify
step "下一步"
log "  · 部署后再跑一次 infra/scripts/verify-isolation.sh --baseline=<部署前快照目录>"
log "  · 回滚：gcloud run services update-traffic $SERVICE --project=$PROJECT_ID --region=$REGION --to-revisions=<上一修订版>=100"

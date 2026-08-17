#!/usr/bin/env bash
#
# deploy-web.sh —— 两个 SPA 的静态发布：用户面板 bp-web 与后台 bp-admin
#
# 事实源：
#   docs/04-ops/deploy.md §10（构建产物只有一份，发布到 N 个镜像）· §11（域名与证书）
#   docs/05-adr/0003-web-hosting-and-reachability.md §3.2 · §3.5 · §5
#   docs/03-product/page-inventory.md §1（三套前端：互不共享域名、鉴权、故障域）
#   web/（pnpm workspace：web/user = bp-web，web/admin = bp-admin，web/shared = 公用）
#   web/shared/src/lib/runtime-config.ts（**运行时配置的字段定义，本脚本按它生成**）
#
# 🔴 三条硬约束，脚本各自有可执行形式：
#
#   1. **用户面板与后台是两个独立主域名，不是同一域名的两个子域。**
#      GFW 的封锁粒度常在主域名级别（deploy.md §10.4），共享主域名等于把后台的可达性
#      绑在面板上。脚本在发布前检查两个池的**可注册主域名**不相交，相交就拒绝发布。
#
#   2. **构建一份 dist/，原封不动发布到池内每一个镜像。**
#      页脚常驻全部镜像域名，所以新增一个域名要重新发布池内全部镜像（deploy.md §11.3 第 7 步）。
#
#   3. **域名池不烧进 bundle，写进 dist/runtime-config.js。**
#      web/shared/src/lib/runtime-config.ts 的原话：备用域名列表恰恰是域名被封时唯一还能用的
#      东西，如果它是构建期常量，新增一个镜像就要重新构建 + 重新部署三套前端。
#      本脚本因此**在构建之后覆盖 dist/runtime-config.js**，不改源码里那份空模板。
#
# 凭据：CLOUDFLARE_API_TOKEN / NETLIFY_AUTH_TOKEN 只从环境变量读，
#      脚本不写、不打印、不落盘任何 token。

set -euo pipefail

# ───────────────────────── 防呆常量 ─────────────────────────

readonly EXPECTED_PROJECT_ID="oratis-491316"

# 允许的证书签发者。只校验 O 不校验 CN —— LE 会轮换中间证书（R10/R11/E5/E6…），
# 钉 CN 会造成误报（deploy.md §11.2 的判定表）。
readonly CERT_ISSUER_REQUIRED="Let's Encrypt"
# 🔴 GTS 证书在中国触发 **IP 级单向丢包**（ADR 0004 §3.4，net4people/bbs #381：
#    抓包证据是证书消息之后单向丢包，不是 RST 注入）。现象酷似网络抖动，极难定位。
readonly CERT_ISSUER_FORBIDDEN="Google Trust Services"

# ───────────────────────── 运行时开关 ─────────────────────────

PROJECT_ID="${PROJECT_ID:-$EXPECTED_PROJECT_ID}"
DRY_RUN=0
ASSUME_YES=0
APP=""
TARGET="cloudflare"
DOMAINS=""
API_POOL="${BP_API_DOMAINS:-}"
SKIP_CERT_CHECK=0

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
用法: deploy-web.sh --app=web|admin [选项]

构建一份 dist/，写入运行时配置，发布到该应用域名池里的**全部**镜像。

必填:
  --app=web       用户面板 bp-web（web/user，包名 @babelplus/user）
  --app=admin     后台   bp-admin（web/admin，包名 @babelplus/admin）

选项:
  --target=<平台>   cloudflare（默认）| github | netlify | all
  --domains=a,b,c   本次要发布的镜像域名列表（**只写主机名，不带协议**）。
                    默认读环境变量 BP_WEB_DOMAINS / BP_ADMIN_DOMAINS
  --api-pool=a,b    API 域名池。第一个是主，其余进 apiFallbackBaseUrls。默认读 BP_API_DOMAINS
  --skip-cert-check 跳过发布后的证书签发者校验。⚠️ 只在域名刚建、证书还没签发时用
  --project=<id>    GCP 项目 ID（本脚本不碰 GCP，仅用于统一防呆）。必须是 oratis-491316
  --dry-run         只打印将要执行的命令
  --yes             跳过交互确认
  -h, --help        显示本帮助

环境变量（凭据只从这里读，脚本不落盘）:
  CLOUDFLARE_API_TOKEN   wrangler 用
  NETLIFY_AUTH_TOKEN     netlify 用
  BP_WEB_DOMAINS         用户面板域名池，逗号分隔
  BP_ADMIN_DOMAINS       后台域名池，逗号分隔（必须与上面**不共享可注册主域名**）
  BP_API_DOMAINS         API 域名池，逗号分隔
  BP_DOCS_URL            教程站 URL（独立主域名，免登录）
  BP_STATUS_URL          状态页 URL
  BP_CHECK_URL           免登录诊断页 URL
  BP_SUPPORT_EMAIL       失联恢复用的支持邮箱（ADR 0002：邮件是唯一那条）

例:
  BP_WEB_DOMAINS=panel.example.com,panel.example.net \
  BP_ADMIN_DOMAINS=ops.example.org \
  BP_API_DOMAINS=api.example.com \
  ./infra/deploy/deploy-web.sh --app=web --target=all --dry-run
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

# split_commas 把逗号分隔串变成一行一个，供 while read 消费。
# ⚠️ 结尾那个换行不是多余的：`printf '%s'`（无换行）+ `while read` 会**静默丢掉最后一项** ——
#    read 读到没有换行的 EOF 时虽然赋了值但返回非零，循环体不执行。
#    单元素列表会整个变成空，而这类脚本里「空 = 检查通过」，是最坏的失败方向。
split_commas() {
  printf '%s\n' "$1" | tr ',' '\n' | sed '/^[[:space:]]*$/d'
}

# guard_hostname 只放行主机名字符。
# 这些值会被写进生成的 runtime-config.js —— 不校验就等于把一个字符串拼进 JS 源码。
guard_hostname() {
  case "$1" in
    ''|*[!A-Za-z0-9.-]*)
      die "域名 \"$1\" 含非法字符。只接受主机名（字母、数字、点、连字符），**不要带 https:// 或路径**。" ;;
  esac
}

# guard_js_literal 校验会被写进生成的 JS 单引号字符串里的值。
# 单引号 / 反斜杠 / 换行都会让生成的 runtime-config.js 语法错误 —— 而它是 index.html 里的
# 同步 script，语法错误的后果是**整个页面白屏**，且恰好白在「用户连不上、最需要看备用域名」的时刻。
guard_js_literal() {
  local name="$1" value="$2"
  case "$value" in
    *\'*|*\\*|*'
'*) die "${name} 的值含单引号 / 反斜杠 / 换行，无法安全写进 runtime-config.js：${value}" ;;
  esac
}

# registrable 取「可注册主域名」的粗略近似：最后两段。
# ⚠️ 对 .co.uk / .com.cn 这类二级后缀会误判成相同 —— 那正好是**偏保守**的方向
#    （宁可误报「两个池共享主域名」也不要漏报），所以不引入 PSL 依赖。
registrable() {
  printf '%s' "$1" | awk -F. '{ if (NF<2) print $0; else printf "%s.%s\n", $(NF-1), $NF }'
}

# ───────────────────────── 域名池校验 ─────────────────────────

guard_pools_disjoint() {
  local web_pool admin_pool d r web_roots
  web_pool="${BP_WEB_DOMAINS:-}"
  admin_pool="${BP_ADMIN_DOMAINS:-}"
  if [ -z "$web_pool" ] || [ -z "$admin_pool" ]; then
    warn "BP_WEB_DOMAINS 或 BP_ADMIN_DOMAINS 未设 → 跳过「两池不共享主域名」检查。
     ⚠️ 这条检查是 page-inventory §1「互不共享域名」的可执行形式，请尽快把两个池都配上。"
    return 0
  fi

  web_roots="$(split_commas "$web_pool" | while IFS= read -r d; do registrable "$d"; done | sort -u)"
  while IFS= read -r d; do
    [ -n "$d" ] || continue
    r="$(registrable "$d")"
    if printf '%s\n' "$web_roots" | grep -qx "$r"; then
      die "后台域名 ${d} 与用户面板池共享可注册主域名 ${r}。
     page-inventory §1 与 deploy.md §10.4 要求两者是**独立主域名**：
     GFW 的封锁粒度常在主域名级别，共享主域名等于把后台的可达性绑在面板上。"
    fi
  done <<EOF
$(split_commas "$admin_pool")
EOF
  ok "用户面板池与后台池不共享可注册主域名"
}

# ───────────────────────── 证书签发者校验 ─────────────────────────
#
# 这一条不是一次性检查，它必须变成每日任务（monitoring.md §5 第 15 条告警，P0）。
# 这里做的是**发布后的即时确认**，不替代那条告警。

check_cert_issuer() {
  local domain="$1" issuer=""
  need_cmd openssl
  issuer="$(echo | openssl s_client -servername "$domain" -connect "${domain}:443" 2>/dev/null \
            | openssl x509 -noout -issuer 2>/dev/null || true)"
  if [ -z "$issuer" ]; then
    warn "$domain 取不到证书（域名还没解析 / 证书还没签发 / 本机出网被拦）"
    return 1
  fi
  case "$issuer" in
    *"$CERT_ISSUER_FORBIDDEN"*)
      log "  ✗ $domain issuer = $issuer"
      die "🔴 ${domain} 的证书由 ${CERT_ISSUER_FORBIDDEN} 签发。
     ADR 0004 §3.4：GTS 证书在中国触发 **IP 级单向丢包**，现象酷似网络抖动，排障时极难定位。
     处置：Cloudflare 后台 SSL/TLS → Edge Certificates → Universal SSL 的
     Certificate Authority 显式选 Let's Encrypt，等重新签发（可能数小时）后再发布。"
      ;;
    *"$CERT_ISSUER_REQUIRED"*)
      ok "$domain issuer = $issuer"
      return 0
      ;;
    *)
      warn "$domain issuer = $issuer —— 既不是 ${CERT_ISSUER_REQUIRED} 也不是已知的 ${CERT_ISSUER_FORBIDDEN}。
     只校验 O 不校验 CN 是刻意的（LE 会轮换中间证书）。这个 issuer 需要人工判断。"
      return 1
      ;;
  esac
}

# ───────────────────────── 构建 ─────────────────────────

build() {
  step "构建 $PROJECT_NAME（$PKG_NAME）"
  if [ ! -d "$SRC_DIR" ]; then
    die "源码目录不存在：$SRC_DIR
     期望的布局是 pnpm workspace：web/user（bp-web）· web/admin（bp-admin）· web/shared（公用）。"
  fi
  need_cmd pnpm

  # 依赖装在宿主机上（宿主机代理是好的），不进容器。
  # 从 workspace 根装：web/user 依赖 workspace:* 的 @babelplus/shared，只在子包里装装不全。
  run pnpm --dir "$WS_DIR" install --frozen-lockfile

  # 构建期只注入「兜底值」。真正生效的是构建后写的 dist/runtime-config.js
  # （web/shared/src/lib/runtime-config.ts 的合并顺序：BASELINE ← 构建期 env ← window.__BP_RUNTIME_CONFIG__）。
  # ⚠️ VITE_ 前缀的变量会被打进 bundle，**任何凭据都不能放这里**（web/user/src/vite-env.d.ts 已写明）。
  if [ "$DRY_RUN" -eq 1 ]; then
    log "  [dry-run] VITE_API_BASE_URL=… pnpm --dir $WS_DIR --filter $PKG_NAME build"
  else
    VITE_API_BASE_URL="$API_PRIMARY_URL" \
    VITE_DOCS_URL="${BP_DOCS_URL:-}" \
    VITE_STATUS_URL="${BP_STATUS_URL:-}" \
    VITE_CHECK_URL="${BP_CHECK_URL:-}" \
    VITE_SUPPORT_EMAIL="${BP_SUPPORT_EMAIL:-}" \
    pnpm --dir "$WS_DIR" --filter "$PKG_NAME" build
  fi

  if [ "$DRY_RUN" -eq 0 ] && [ ! -d "$DIST_DIR" ]; then
    die "构建没有产出 $DIST_DIR"
  fi
  ok "构建产物：$DIST_DIR"
}

# write_runtime_config 覆盖 dist/runtime-config.js。
#
# 为什么覆盖 dist 而不是改源码里的 public/runtime-config.js：
# 源码那份是**故意留空的模板**（「编不出来的东西就不编，填上假域名会把失联的用户导向一个
# 不存在的地址」）。发布是唯一知道真实域名池的时刻，所以值在这里注入，源码保持干净。
write_runtime_config() {
  step "写运行时配置"
  local f="${DIST_DIR}/runtime-config.js" d i=0 mirrors="" fallbacks="" body

  guard_js_literal BP_DOCS_URL     "${BP_DOCS_URL:-}"
  guard_js_literal BP_STATUS_URL   "${BP_STATUS_URL:-}"
  guard_js_literal BP_CHECK_URL    "${BP_CHECK_URL:-}"
  guard_js_literal BP_SUPPORT_EMAIL "${BP_SUPPORT_EMAIL:-}"

  while IFS= read -r d; do
    [ -n "$d" ] || continue
    guard_hostname "$d"
    if [ "$i" -eq 0 ]; then
      mirrors="{ label: '主站', url: 'https://${d}', note: '优先尝试' }"
    else
      mirrors="${mirrors},
    { label: '镜像 ${i}', url: 'https://${d}' }"
    fi
    i=$((i + 1))
  done <<EOF
$(split_commas "$DOMAINS")
EOF

  i=0
  while IFS= read -r d; do
    [ -n "$d" ] || continue
    guard_hostname "$d"
    if [ "$i" -gt 0 ]; then
      if [ -n "$fallbacks" ]; then fallbacks="${fallbacks}, "; fi
      fallbacks="${fallbacks}'https://${d}'"
    fi
    i=$((i + 1))
  done <<EOF
$(split_commas "$API_POOL")
EOF

  body="$(cat <<EOF
/* 由 infra/deploy/deploy-web.sh 在发布时生成。**不要手工改这里，改域名池后重新发布。**
 *
 * 必须以 Cache-Control: no-cache 下发 —— 改了域名池但用户拿旧缓存 = 白改。
 * 同目录的 _headers 已经声明了这条（Cloudflare Pages / Netlify 都读它）。
 */
window.__BP_RUNTIME_CONFIG__ = {
  apiBaseUrl: '${API_PRIMARY_URL}',
  apiFallbackBaseUrls: [${fallbacks}],
  mirrorDomains: [
    ${mirrors}
  ],
  docsUrl: '${BP_DOCS_URL:-}',
  statusUrl: '${BP_STATUS_URL:-}',
  checkUrl: '${BP_CHECK_URL:-}',
  supportEmail: '${BP_SUPPORT_EMAIL:-}',
  requestTimeoutMs: 15000,
  slowHintMs: 3000,
  configuredAt: '$(date -u '+%Y-%m-%dT%H:%M:%SZ')',
};
EOF
)"

  if [ "$DRY_RUN" -eq 1 ]; then
    log "  [dry-run] 写 $f："
    printf '%s\n' "$body" | sed 's/^/    | /' >&2
  else
    printf '%s\n' "$body" > "$f"
    ok "已写 $f"
  fi

  # _headers：Cloudflare Pages 与 Netlify 用同一种格式。
  # 只对 runtime-config.js 关缓存，其余带 hash 的静态资源仍然可以长缓存。
  local h="${DIST_DIR}/_headers"
  if [ "$DRY_RUN" -eq 1 ]; then
    log "  [dry-run] 追加 $h：/runtime-config.js → Cache-Control: no-cache"
  else
    {
      printf '\n/runtime-config.js\n'
      printf '  Cache-Control: no-cache\n'
    } >> "$h"
    ok "已声明 /runtime-config.js 不缓存"
  fi

  if [ -z "$mirrors" ]; then
    warn "镜像域名列表为空 —— 页脚会显示「备用域名尚未配置」。
     product-brief §8 承诺「域名失联恢复 ≤ 30 分钟」，而那条承诺的前提就是这个列表是满的。"
  fi
}

# ───────────────────────── 发布 ─────────────────────────

publish_cloudflare() {
  need_cmd npx
  if [ -z "${CLOUDFLARE_API_TOKEN:-}" ]; then
    die "CLOUDFLARE_API_TOKEN 未设。export 它再跑（脚本不读、不写任何凭据文件）。"
  fi
  # 平台子域名（*.pages.dev）**明确不用**：ADR 0003 §3.5 —— 自有域名是我们
  # 能控制、能轮换的主机名，平台子域名不是；且 *.pages.dev 已被 SNI 级封锁。
  run npx wrangler pages deploy "$DIST_DIR" --project-name="$PROJECT_NAME"
  ok "已发布到 Cloudflare Pages 项目 $PROJECT_NAME"
}

publish_netlify() {
  need_cmd npx
  if [ -z "${NETLIFY_AUTH_TOKEN:-}" ]; then
    die "NETLIFY_AUTH_TOKEN 未设。export 它再跑。"
  fi
  run npx netlify deploy --prod --dir="$DIST_DIR"
  ok "已发布到 Netlify"
}

publish_github() {
  # GitHub Pages 走「push 到 gh-pages 分支 + CNAME 文件」，需要仓库上下文。
  # ⚠️ ADR 0003 §6 第 4 条：GitHub 自身在中国的异常率 37%（高于 github.io 的 8.9%），
  #    且历史上有 2013 年 DNS 劫持、2015 年「大炮」DDoS。它今天可用，但**不是一个
  #    中立的、无政治风险的选择**。
  warn "GitHub Pages 发布本脚本不自动化：它依赖仓库托管在哪，而这一项 deploy.md §15 记为未定。
     手工步骤：把 ${DIST_DIR} 的内容 push 到 gh-pages 分支，并在根目录放 CNAME 文件（内容是镜像域名）。
     ⚠️ GitHub Pages **不读 _headers** —— runtime-config.js 的 no-cache 在那边没有等价物。"
}

publish() {
  step "发布 $PROJECT_NAME → $TARGET"
  case "$TARGET" in
    cloudflare) publish_cloudflare ;;
    netlify)    publish_netlify ;;
    github)     publish_github ;;
    all)
      # 镜像应当**跨平台**分布而不是同平台多域名（ADR 0003 §3.2 修订结论：
      # 封锁是 SNI 级的，平台不是单点，域名才是）。
      publish_cloudflare
      publish_netlify
      publish_github
      ;;
    *) die "未知发布目标：$TARGET（可选 cloudflare / netlify / github / all）" ;;
  esac
}

verify_domains() {
  step "发布后校验：证书签发者"
  if [ "$SKIP_CERT_CHECK" -eq 1 ]; then
    skip "--skip-cert-check：跳过"
    return 0
  fi
  if [ "$DRY_RUN" -eq 1 ]; then
    skip "[dry-run]：跳过（这一步是只读的网络探测，但没必要在 dry-run 里做）"
    return 0
  fi
  local d failed=0
  while IFS= read -r d; do
    [ -n "$d" ] || continue
    if ! check_cert_issuer "$d"; then
      failed=1
    fi
  done <<EOF
$(split_commas "$DOMAINS")
EOF
  if [ "$failed" -ne 0 ]; then
    warn "有域名的证书签发者未确认。这不阻断发布，但**必须**在 monitoring.md §8 的
     每日证书检查里跟进 —— 第 15 条告警（证书签发者变更）是 P0。"
  fi
}

# ───────────────────────── 主流程 ─────────────────────────

main() {
  local arg
  for arg in "$@"; do
    case "$arg" in
      --app=*)         APP="${arg#*=}" ;;
      --target=*)      TARGET="${arg#*=}" ;;
      --domains=*)     DOMAINS="${arg#*=}" ;;
      --api-pool=*)    API_POOL="${arg#*=}" ;;
      --skip-cert-check) SKIP_CERT_CHECK=1 ;;
      --project=*)     PROJECT_ID="${arg#*=}" ;;
      --dry-run)       DRY_RUN=1 ;;
      --yes)           ASSUME_YES=1 ;;
      -h|--help)       usage; exit 0 ;;
      *)               usage >&2; die "未知参数：$arg" ;;
    esac
  done

  guard_project
  ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
  WS_DIR="${ROOT}/web"

  case "$APP" in
    web)
      PROJECT_NAME="bp-web"
      PKG_NAME="@babelplus/user"
      SRC_DIR="${WS_DIR}/user"
      if [ -z "$DOMAINS" ]; then DOMAINS="${BP_WEB_DOMAINS:-}"; fi
      ;;
    admin)
      PROJECT_NAME="bp-admin"
      PKG_NAME="@babelplus/admin"
      SRC_DIR="${WS_DIR}/admin"
      if [ -z "$DOMAINS" ]; then DOMAINS="${BP_ADMIN_DOMAINS:-}"; fi
      ;;
    "")
      usage >&2; die "必须指定 --app=web 或 --app=admin。" ;;
    *)
      usage >&2; die "未知应用：$APP（可选 web / admin）" ;;
  esac
  DIST_DIR="${SRC_DIR}/dist"
  guard_bp_prefix "$PROJECT_NAME"

  if [ -z "$DOMAINS" ]; then
    die "没有域名池。用 --domains=a,b 或设 BP_WEB_DOMAINS / BP_ADMIN_DOMAINS。
     ⚠️ product-brief §8 承诺「域名失联恢复 ≤ 30 分钟」，唯一能让它成立的做法是
     池里始终有**已注册、已配好证书、已在运行时配置里但未启用**的备件（deploy.md §11.3）。"
  fi

  # API 主域名：池里的第一个。空池时留空 —— runtime-config 里空串表示「与页面同源」，
  # 而那对我们是错的（API 与 Web 必须不同源，system-design §4.1），所以要显式告警。
  API_PRIMARY="$(split_commas "$API_POOL" | head -n 1)"
  if [ -n "$API_PRIMARY" ]; then
    guard_hostname "$API_PRIMARY"
    API_PRIMARY_URL="https://${API_PRIMARY}"
  else
    API_PRIMARY_URL=""
    warn "API 域名池为空 → apiBaseUrl 留空 = 前端会当作「与页面同源」。
     而 system-design §4.1 要求 API 与 Web **不同源**。上线前必须配上 BP_API_DOMAINS。"
  fi

  log "应用   : $PROJECT_NAME（$PKG_NAME）"
  log "源码   : $SRC_DIR"
  log "平台   : $TARGET"
  log "镜像池 : $DOMAINS"
  log "API 池 : ${API_POOL:-（空）}"
  if [ "$DRY_RUN" -eq 1 ]; then
    log "模式   : DRY-RUN（只打印写操作）"
  fi

  guard_pools_disjoint

  if [ "$APP" = "admin" ]; then
    # 后台是唯一一个「能改钱、改配额、改节点可用性」的界面（page-inventory §4.4：
    # 16 条必须二次确认 + 审计日志的操作）。把它发到公网静态托管上，
    # **静态托管本身不提供任何访问控制**。
    confirm "即将把**后台 SPA** 发布到公网静态托管。
  🔴 静态托管不提供 IP 白名单，也不提供 TOTP。page-inventory §1 要求的
     「IP 白名单/IAP + 强制 TOTP」必须由 Cloudflare Access（或等价物）与 bp-api 侧共同实现，
     发布这一步**不会**替你做。请确认这两层已经就位。
  🔴 同时确认后台域名与用户面板域名是两个**独立主域名**（上面的检查只在两个池都配了时才生效）。" "publish-admin"
  fi

  build
  write_runtime_config
  publish
  verify_domains

  step "完成"
  log "  ⚠️ 回滚 = 拿上一个 commit 重新构建重新发布，且**必须发布到池内全部镜像**"
  log "     （只回滚一个镜像会让用户在不同域名上看到不同版本，而用户在故障时恰恰会挨个域名试）。"
  log "  ⚠️ 只新增/下线镜像域名时**不需要重新构建** —— 改域名池后重跑本脚本即可，"
  log "     值走 dist/runtime-config.js，不在 bundle 里。"
  log "  ⚠️ 新增一个镜像域名的完整流程是九步不是一步，见 deploy.md §11.3。"
}

ROOT=""
WS_DIR=""
PROJECT_NAME=""
PKG_NAME=""
SRC_DIR=""
DIST_DIR=""
API_PRIMARY=""
API_PRIMARY_URL=""

main "$@"

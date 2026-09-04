#!/usr/bin/env bash
#
# setup-https-inbound.sh —— 在一台已经装好的 bp-node-* 上起一个 HTTPS 代理入站（Caddy forwardproxy）
#
# 🔴 **这个脚本存在的唯一目的是回答一个问题，不是上线一个功能**：
#    [client-products-spec §6.1](../../docs/03-product/client-products-spec.md) 的 **E0** ——
#    「HTTPS 入站的每用户字节能否进 UniProxy 的上报路径」。roadmap **B66**。
#    **查不到就停**：不能计量就不能扣配额，扩展流量会成为无界泄漏。
#    判定方法与验收口径写在 docs/04-ops/e0-metering-verification.md，**先读它再跑这个**。
#
# 🔴 **跑之前必须确认 P1 出口标准 5 的 72 小时观察窗已经到点**（roadmap §4.3）。
#    这个脚本会在节点上装一个新进程并占用一个端口；观察窗期间动那台机器
#    等于用一天换重跑 72 小时。脚本自己拦不住这件事 —— 它不知道窗口什么时候开始的，
#    所以这一条只能靠人。
#
# 为什么是 Caddy 而不是 Xray 的 http inbound：
#   · v2node v0.4.3 的 GetNodeInfo 只认 vmess/trojan/hysteria2/tuic/anytls/vless/shadowsocks，
#     **没有普通 http**（2026-09-02 实测过它对未知 protocol 的反应：整个进程退出码 0）。
#     所以 HTTPS 代理只能是**另一个进程**，而这正是 E0 那个问题的由来：
#     另一个进程的字节数不在 v2node 的账里。
#   · Caddy 的 forwardproxy 带 probe_resistance：没有凭据时**回落成一个正常网站**，
#     而不是回 407 —— 后者等于对着主动探测举手（spec §3.7）。
#
# 用法（在**节点上**跑，或用 setup-node.sh 同样的「变量走 stdin、不进 argv」的方式喂进去）：
#   ./setup-https-inbound.sh --dry-run
#   BP_PROXY_PORT=8443 BP_PROXY_USER=… BP_PROXY_PASS=… BP_CERT_DOMAIN=… ./setup-https-inbound.sh --apply
#
# 必需环境变量（fail-closed，缺一即退出）：
#   BP_PROXY_PORT    入站端口。**不要用 443** —— 那是 REALITY 与 HY2 的，撞了就是全线中断
#   BP_PROXY_USER / BP_PROXY_PASS   Basic 凭据。E0 阶段是**一对手工凭据**；
#                    正式方案是由订阅 token 派生（spec §3.7，未做安全评审）
#   BP_CERT_DOMAIN   证书域名。复用 setup-node.sh 已经签好的那张（/run/credentials 下）
#   BP_PROBE_TARGET  probe_resistance 的回落站点（无凭据访问时看到的东西）
#
# 这个脚本**不碰 v2node、不碰 443、不改防火墙**。防火墙那一步刻意留给人：
# 开一个新端口是一次爆炸半径判断，不该被一个装机脚本顺手做掉。

set -euo pipefail

readonly CADDY_VERSION="2.10.0"
readonly UNIT="bp-httpsproxy"
readonly ETC="/etc/bp"
readonly CADDYFILE="${ETC}/Caddyfile"
readonly BIN="/usr/local/bin/bp-caddy"

DRY_RUN=1

log()  { printf '%s\n' "$*" >&2; }
step() { printf '\n\033[1m▸ %s\033[0m\n' "$*" >&2; }
ok()   { printf '  ✓ %s\n' "$*" >&2; }
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
  sed -n '2,45p' "$0" | sed 's/^# \{0,1\}//'
}

while [ $# -gt 0 ]; do
  case "$1" in
    --apply) DRY_RUN=0 ;;
    --dry-run) DRY_RUN=1 ;;
    -h|--help) usage; exit 0 ;;
    *) die "未知参数：$1" ;;
  esac
  shift
done

preflight() {
  step "前置检查"
  local missing=""
  for v in BP_PROXY_PORT BP_PROXY_USER BP_PROXY_PASS BP_CERT_DOMAIN BP_PROBE_TARGET; do
    [ -n "${!v:-}" ] || missing="${missing} $v"
  done
  [ -z "$missing" ] || die "缺环境变量：${missing# }"

  # 🔴 443 是 REALITY 与 Hysteria2 的。撞上去就是全线中断，而现象是「今天所有人都连不上」。
  [ "$BP_PROXY_PORT" != "443" ] || die "BP_PROXY_PORT 不能是 443 —— 那是 REALITY / HY2 的端口"
  case "$BP_PROXY_PORT" in
    ''|*[!0-9]*) die "BP_PROXY_PORT 必须是数字" ;;
  esac

  if [ "$DRY_RUN" = 0 ]; then
    command -v systemctl >/dev/null || die "这个脚本要在节点上跑（找不到 systemctl）"
    ss -lntp 2>/dev/null | grep -q ":${BP_PROXY_PORT} " && die "端口 ${BP_PROXY_PORT} 已被占用"
  fi
  ok "变量齐全，端口 ${BP_PROXY_PORT} 可用"
}

install_caddy() {
  step "装 Caddy（带 forwardproxy 插件）"
  # 🔴 官方发行版**不含** forwardproxy，必须用 xcaddy 构建或取一个带插件的构建。
  #    这里刻意不自动下载一个「带插件的第三方构建」—— 那是把整条流量交给一个来历不明的二进制。
  #    E0 阶段用 xcaddy 在节点上现构建：慢，但可复现且来源清楚。
  run bash -c "command -v go >/dev/null || (apt-get update -qq && apt-get install -y -qq golang-go)"
  run bash -c "go install github.com/caddyserver/xcaddy/cmd/xcaddy@latest"
  # 波浪号在引号里不展开（shellcheck SC2088）—— 用 $HOME，否则会去找一个字面量叫 "~" 的目录。
  run bash -c "\"\$HOME/go/bin/xcaddy\" build v${CADDY_VERSION} --with github.com/caddyserver/forwardproxy@caddy2 --output ${BIN}"
  run chmod 755 "$BIN"
  ok "$BIN"
}

write_config() {
  step "写配置"
  # probe_resistance：无凭据时**回落成 probe_target 那个站点**，不回 407。
  # 407 等于对主动探测举手说「这里有个代理」。
  run mkdir -p "$ETC"
  if [ "$DRY_RUN" = 1 ]; then
    log "  [dry-run] 写 ${CADDYFILE}（凭据经环境变量注入，不落进 argv）"
  else
    umask 077
    cat > "$CADDYFILE" <<EOF
{
  order forward_proxy before file_server
  auto_https off
}

:${BP_PROXY_PORT} {
  tls /run/credentials/bp-node.service/fullchain.pem /run/credentials/bp-node.service/privkey.pem

  forward_proxy {
    basic_auth ${BP_PROXY_USER} ${BP_PROXY_PASS}
    hide_ip
    hide_via
    probe_resistance ${BP_PROBE_TARGET}
  }

  # 没有走代理的普通请求（含主动探测）看到的东西。
  respond "OK" 200
}
EOF
    chmod 600 "$CADDYFILE"
  fi
  ok "$CADDYFILE"
}

write_unit() {
  step "写 systemd 单元"
  if [ "$DRY_RUN" = 1 ]; then
    log "  [dry-run] 写 /etc/systemd/system/${UNIT}.service"
  else
    cat > "/etc/systemd/system/${UNIT}.service" <<EOF
[Unit]
Description=babel.plus HTTPS forward proxy (E0 metering probe)
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
# 证书用 LoadCredential 挂进来，与 bp-node.service 同一套路径 —— 私钥不给固定 uid。
LoadCredential=fullchain.pem:/etc/bp/certs/fullchain.pem
LoadCredential=privkey.pem:/etc/bp/certs/privkey.pem
ExecStart=${BIN} run --config ${CADDYFILE} --adapter caddyfile
Restart=on-failure
RestartSec=3
DynamicUser=yes
ProtectSystem=strict
ProtectHome=yes
PrivateTmp=yes
NoNewPrivileges=yes
AmbientCapabilities=CAP_NET_BIND_SERVICE

[Install]
WantedBy=multi-user.target
EOF
  fi
  run systemctl daemon-reload
  run systemctl enable --now "${UNIT}.service"
  ok "${UNIT}.service"
}

verify() {
  step "自检（只证明入站起来了，**不证明能计量** —— 那是 E0 runbook 的事）"
  if [ "$DRY_RUN" = 1 ]; then
    log "  [dry-run] 跳过"
    return
  fi
  systemctl is-active --quiet "${UNIT}.service" || die "${UNIT} 没起来：journalctl -u ${UNIT} -n 50"
  ok "进程在跑"
  # 无凭据：应当看到回落内容，**不是** 407。
  local code
  code="$(curl -sk -o /dev/null -w '%{http_code}' "https://127.0.0.1:${BP_PROXY_PORT}/" || true)"
  [ "$code" = "407" ] && die "无凭据访问返回 407 —— probe_resistance 没生效，这台机器对主动探测是举手的"
  ok "无凭据访问返回 ${code}（不是 407）"
  log ""
  log "  下一步：按 docs/04-ops/e0-metering-verification.md 跑计量验证。"
  log "  🔴 在那份文档给出「查得到」之前，**不要**把这条入站写进任何下发路径。"
}

main() {
  log "模式 : $([ "$DRY_RUN" = 1 ] && echo 'DRY-RUN' || echo 'APPLY —— 会改这台节点')"
  preflight
  install_caddy
  write_config
  write_unit
  verify
}

main "$@"

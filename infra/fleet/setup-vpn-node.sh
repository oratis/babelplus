#!/usr/bin/env bash
# =============================================================================
# setup-vpn-node.sh · 自用机队新节点的装机（在节点上以 root 跑；凭据走 stdin 环境变量）
#
# 事实源：oratis/Proxy_Skill setup-server.sh（xray REALITY + sysctl 调优 + unattended-upgrades + SSH 加固，
#         2026-07-27 在 vpn-us / vpn-jp 上实际跑过的那份）
#         vpn-jp 上 2026-09-05 只读实查的 /etc/hysteria/config.yaml 结构（salamander 混淆、bing 伪装、
#         自签证书 CN=www.bing.com、hysteria-server.service User=hysteria）
#         docs/04-ops/personal-fleet-runbook.md §2.2（默认不启用 Brutal：服务端不写 bandwidth）
#
# 装两条通路（fleet.json 里 vpn-sg 的 paths）：
#   1. xray      VLESS + REALITY + XTLS-Vision，tcp:443
#   2. hysteria2 QUIC，udp:443，salamander 混淆，自签证书（客户端 skip-cert-verify）
#   可选：SS-2022（给 SS_PASSWORD 才装）、cloudflared（给 CF_TUNNEL_TOKEN 才装）
#
# 本机用法：
#   set -a; source infra/fleet/.secrets.env; set +a
#   {
#     for v in SG_REALITY_UUID SG_REALITY_PRIVATE SG_REALITY_SHORTID SG_REALITY_SNI SG_HY2_PASSWORD SG_HY2_OBFS_PASSWORD SG_HY2_SNI; do
#       printf 'export %s=%q\n' "${v#SG_}" "${!v:?缺少 $v}"
#     done
#     cat infra/fleet/setup-vpn-node.sh
#   } | gcloud compute ssh vpn-sg --project=oratis-491316 --zone=asia-southeast1-a --tunnel-through-iap --command='sudo bash -s'
#
# 幂等：重复跑会覆盖配置并重启服务，不会重复装包。只读自检在最后。
# =============================================================================
set -euo pipefail
export PATH="/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin:$PATH"
export DEBIAN_FRONTEND=noninteractive

: "${REALITY_UUID:?}" "${REALITY_PRIVATE:?}" "${REALITY_SHORTID:?}" "${REALITY_SNI:?}"
: "${HY2_PASSWORD:?}" "${HY2_OBFS_PASSWORD:?}" "${HY2_SNI:=www.bing.com}"
SS_PORT="${SS_PORT:-48882}"; SS_PASSWORD="${SS_PASSWORD:-}"
CF_TUNNEL_TOKEN="${CF_TUNNEL_TOKEN:-}"; CDN_UUID="${CDN_UUID:-}"; CDN_WS_PATH="${CDN_WS_PATH:-}"; CDN_WS_PORT="${CDN_WS_PORT:-8080}"
XRAY_VER="${XRAY_VER:-}"   # 空 = 官方安装脚本的最新版；钉版本就写 v25.x.y

say() { printf '\n=== %s\n' "$*"; }

say "[1/7] sysctl（与 Proxy_Skill 99-proxy-network.conf 一致：BBR + fq + 16 MiB 缓冲 + MTU 探测 + TFO）"
tee /etc/sysctl.d/99-proxy-network.conf >/dev/null <<'SYSCTL'
net.core.default_qdisc=fq
net.ipv4.tcp_congestion_control=bbr
net.ipv4.tcp_mtu_probing=1
net.ipv4.tcp_fastopen=3
net.ipv4.tcp_slow_start_after_idle=0
net.core.rmem_max=16777216
net.core.wmem_max=16777216
net.core.netdev_max_backlog=4096
net.ipv4.tcp_rmem=4096 131072 16777216
net.ipv4.tcp_wmem=4096 65536 16777216
SYSCTL
sysctl --system >/dev/null
sysctl -n net.ipv4.tcp_congestion_control net.core.default_qdisc

say "[2/7] 基础包"
apt-get update -qq
apt-get install -y -qq curl jq openssl unattended-upgrades ca-certificates >/dev/null
dpkg-reconfigure -f noninteractive unattended-upgrades

say "[3/7] xray（官方安装脚本）+ REALITY 配置"
if ! command -v xray >/dev/null; then
  if [ -n "$XRAY_VER" ]; then
    bash -c "$(curl -fsSL https://github.com/XTLS/Xray-install/raw/main/install-release.sh)" @ install --version "$XRAY_VER"
  else
    bash -c "$(curl -fsSL https://github.com/XTLS/Xray-install/raw/main/install-release.sh)" @ install
  fi
fi
xray version | head -1
mkdir -p /usr/local/etc/xray
CDN_INBOUND=""
if [ -n "$CDN_UUID" ] && [ -n "$CDN_WS_PATH" ]; then
  CDN_INBOUND=",
    { \"listen\": \"127.0.0.1\", \"port\": ${CDN_WS_PORT}, \"protocol\": \"vless\",
      \"settings\": { \"clients\": [ { \"id\": \"${CDN_UUID}\" } ], \"decryption\": \"none\" },
      \"streamSettings\": { \"network\": \"ws\", \"wsSettings\": { \"path\": \"${CDN_WS_PATH}\" } } }"
fi
cat > /usr/local/etc/xray/config.json <<JSON
{
  "log": { "loglevel": "warning" },
  "inbounds": [
    {
      "listen": "0.0.0.0", "port": 443, "protocol": "vless",
      "settings": { "clients": [ { "id": "${REALITY_UUID}", "flow": "xtls-rprx-vision" } ], "decryption": "none" },
      "streamSettings": {
        "network": "tcp", "security": "reality",
        "realitySettings": {
          "dest": "${REALITY_SNI}:443", "serverNames": [ "${REALITY_SNI}" ],
          "privateKey": "${REALITY_PRIVATE}", "shortIds": [ "${REALITY_SHORTID}" ]
        }
      }
    }${CDN_INBOUND}
  ],
  "outbounds": [ { "protocol": "freedom" }, { "protocol": "blackhole", "tag": "block" } ]
}
JSON
id xray >/dev/null 2>&1 || useradd --system --home-dir /var/lib/xray --create-home --shell /usr/sbin/nologin xray
chown xray:xray /usr/local/etc/xray/config.json && chmod 600 /usr/local/etc/xray/config.json
mkdir -p /etc/systemd/system/xray.service.d
printf '[Service]\nUser=xray\nGroup=xray\n' > /etc/systemd/system/xray.service.d/20-user.conf
xray run -test -config /usr/local/etc/xray/config.json >/dev/null
systemctl daemon-reload && systemctl enable --now xray >/dev/null && systemctl restart xray

say "[4/7] hysteria2（官方安装脚本）+ 自签证书 + salamander"
if ! command -v hysteria >/dev/null; then
  bash <(curl -fsSL https://get.hy2.sh/) >/dev/null
fi
hysteria version | head -2
mkdir -p /etc/hysteria
id hysteria >/dev/null 2>&1 || useradd --system --home-dir /var/lib/hysteria --create-home --shell /usr/sbin/nologin hysteria
if [ ! -s /etc/hysteria/server.key ]; then
  openssl req -x509 -nodes -newkey ec -pkeyopt ec_paramgen_curve:prime256v1 -days 3650 \
    -keyout /etc/hysteria/server.key -out /etc/hysteria/server.crt -subj "/CN=${HY2_SNI}" >/dev/null 2>&1
fi
cat > /etc/hysteria/config.yaml <<YAML
listen: :443

tls:
  cert: /etc/hysteria/server.crt
  key: /etc/hysteria/server.key

auth:
  type: password
  password: ${HY2_PASSWORD}

obfs:
  type: salamander
  salamander:
    password: ${HY2_OBFS_PASSWORD}

quic:
  initStreamReceiveWindow: 16777216
  maxStreamReceiveWindow: 16777216
  initConnReceiveWindow: 33554432
  maxConnReceiveWindow: 33554432

# 刻意不写 bandwidth：写了 = 服务端 Brutal（runbook §2.2 约束 1）。客户端要 Brutal 用 --brutal 下发 up/down。

masquerade:
  type: proxy
  proxy:
    url: https://${HY2_SNI}/
    rewriteHost: true
YAML
chown -R hysteria:hysteria /etc/hysteria && chmod 600 /etc/hysteria/config.yaml /etc/hysteria/server.key && chmod 644 /etc/hysteria/server.crt
systemctl daemon-reload && systemctl enable --now hysteria-server >/dev/null && systemctl restart hysteria-server

say "[5/7] SS-2022（可选）"
if [ -n "$SS_PASSWORD" ]; then
  if ! command -v ssserver >/dev/null; then
    SS_VER="v1.22.0"; ARCH="$(uname -m)"
    case "$ARCH" in x86_64) T="x86_64-unknown-linux-gnu" ;; aarch64) T="aarch64-unknown-linux-gnu" ;; *) echo "arch $ARCH?"; exit 1 ;; esac
    curl -fsSL -o /tmp/ss.tar.xz "https://github.com/shadowsocks/shadowsocks-rust/releases/download/${SS_VER}/shadowsocks-${SS_VER}.${T}.tar.xz"
    (cd /tmp && tar -xf ss.tar.xz && install -m 0755 ssserver /usr/local/bin/ssserver)
  fi
  mkdir -p /etc/shadowsocks
  printf '{ "server": "0.0.0.0", "server_port": %s, "method": "2022-blake3-aes-128-gcm", "password": "%s", "mode": "tcp_and_udp", "fast_open": false }\n' "$SS_PORT" "$SS_PASSWORD" > /etc/shadowsocks/config.json
  chmod 600 /etc/shadowsocks/config.json
  cat > /etc/systemd/system/ssserver.service <<'UNIT'
[Unit]
Description=shadowsocks-rust server
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
LoadCredential=config:/etc/shadowsocks/config.json
ExecStart=/usr/local/bin/ssserver -c %d/config
Restart=on-failure
RestartSec=5
LimitNOFILE=1048576
NoNewPrivileges=true
ProtectSystem=strict
ProtectHome=true
PrivateTmp=true
DynamicUser=true

[Install]
WantedBy=multi-user.target
UNIT
  systemctl daemon-reload && systemctl enable --now ssserver >/dev/null && systemctl restart ssserver
else
  echo "SS_PASSWORD 为空，跳过 SS-2022（fleet.json 里 vpn-sg 也没有 SS 通路）"
fi

say "[6/7] cloudflared（可选）"
if [ -n "$CF_TUNNEL_TOKEN" ]; then
  if ! command -v cloudflared >/dev/null; then
    case "$(uname -m)" in x86_64) D="cloudflared-linux-amd64.deb" ;; aarch64) D="cloudflared-linux-arm64.deb" ;; esac
    curl -fsSL -o /tmp/cloudflared.deb "https://github.com/cloudflare/cloudflared/releases/latest/download/${D}" && dpkg -i /tmp/cloudflared.deb >/dev/null
  fi
  cloudflared service install "$CF_TUNNEL_TOKEN" || true
  systemctl enable --now cloudflared >/dev/null
else
  echo "CF_TUNNEL_TOKEN 为空，跳过 cloudflared"
fi

say "[7/7] SSH 加固 + 自检"
sed -i -e 's/^#*PasswordAuthentication.*/PasswordAuthentication no/' -e 's/^#*PermitRootLogin.*/PermitRootLogin no/' \
       -e 's/^#*KbdInteractiveAuthentication.*/KbdInteractiveAuthentication no/' /etc/ssh/sshd_config
systemctl reload ssh || systemctl reload sshd || true
for s in xray hysteria-server ssserver cloudflared; do printf '%s=%s ' "$s" "$(systemctl is-active "$s" 2>/dev/null || echo n/a)"; done; echo
ss -tulnp | awk 'NR>1{print $1, $5, $7}' | grep -E ':443 |:'"$SS_PORT"' ' | sed -E 's/users:\(\("([^"]+)".*/\1/' | sort -u
if [ -f /var/run/reboot-required ]; then echo "⚠️ 有内核更新待重启（装完本脚本可以马上 reboot 一次）"; fi
echo "done."

#!/usr/bin/env bash
# =============================================================================
# healthcheck-install.sh · 在本机执行：把 healthcheck.sh 装到一台自用节点上（IAP SSH + stdin）
#
# 事实源：docs/04-ops/personal-fleet-runbook.md §3.5；infra/node/README.md §3（stdin 灌法、fail-closed）
#
# 用法：
#   set -a; source infra/fleet/.secrets.env; set +a
#   infra/fleet/healthcheck-install.sh vpn-jp            # 装 + 立即跑一次 + 打印结果
#   infra/fleet/healthcheck-install.sh vpn-jp --dry-run  # 只打印将要送到节点的安装脚本（token 打码）
#
# 需要 .secrets.env 里有：FLEET_INGEST_URL、NODE_TOKEN_<HOST 大写、连字符换下划线>
# zone 从 infra/fleet/fleet.json 取。凭据经 stdin 进入远端 `sudo bash -s`，不进命令行、不进 history。
#
# 节点上落三个文件 + 一对 unit：
#   /usr/local/sbin/fleet-healthcheck      本体（healthcheck.sh 原样）
#   /etc/fleet/healthcheck.env             非机密配置（EnvironmentFile）
#   /etc/fleet/node-token                  节点 token，root:root 600，经 LoadCredential 注入，不进 unit
#   fleet-healthcheck.service / .timer     每小时 :30 UTC 跑一次（23:30 那次为 daily），Persistent
# =============================================================================
set -euo pipefail

HERE="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
FLEET_JSON="$HERE/fleet.json"
PROJECT="$(jq -r '.project' "$FLEET_JSON")"

NODE="${1:?用法: healthcheck-install.sh <vpn-host> [--dry-run]}"
DRY=0; [ "${2:-}" = "--dry-run" ] && DRY=1

ZONE="$(jq -r --arg h "$NODE" '.nodes[] | select(.host==$h) | .zone' "$FLEET_JSON")"
[ -n "$ZONE" ] && [ "$ZONE" != "null" ] || { printf 'fleet.json 里没有 %s\n' "$NODE" >&2; exit 2; }
SS_PORT="$(jq -r --arg h "$NODE" '[.nodes[] | select(.host==$h) | .paths[] | select(.kind=="shadowsocks2022") | .port] | .[0] // 48882' "$FLEET_JSON")"

TOKEN_VAR="NODE_TOKEN_$(printf '%s' "$NODE" | tr '[:lower:]-' '[:upper:]_')"
# ${!v:?} 是 fail-closed：缺任何一个变量在连上机器之前就退出，不生成半成品配置。
for v in FLEET_INGEST_URL "$TOKEN_VAR"; do
  : "${!v:?缺少环境变量 $v（set -a; source infra/fleet/.secrets.env; set +a）}"
done
TOKEN="${!TOKEN_VAR}"
HC_B64="$(base64 < "$HERE/healthcheck.sh" | tr -d '\n')"

# 远端安装脚本。变量用 printf %q 注入，含特殊字符的值不会被 shell 二次解释。
remote_script() {
  printf 'export NODE=%q INGEST=%q TOKEN=%q SS_PORT=%q HC_B64=%q\n' \
    "$NODE" "$FLEET_INGEST_URL" "$1" "$SS_PORT" "$HC_B64"
  cat <<'REMOTE'
set -euo pipefail
umask 077
install -d -m 0755 /etc/fleet /var/lib/fleet
printf '%s' "$HC_B64" | base64 -d > /usr/local/sbin/fleet-healthcheck.tmp
install -m 0755 /usr/local/sbin/fleet-healthcheck.tmp /usr/local/sbin/fleet-healthcheck
rm -f /usr/local/sbin/fleet-healthcheck.tmp
printf '%s\n' "$TOKEN" > /etc/fleet/node-token; chmod 0600 /etc/fleet/node-token; chown root:root /etc/fleet/node-token
cat > /etc/fleet/healthcheck.env <<ENV
FLEET_INGEST_URL=$INGEST
FLEET_NODE_NAME=$NODE
FLEET_SS_PORT=$SS_PORT
ENV
chmod 0644 /etc/fleet/healthcheck.env
cat > /etc/systemd/system/fleet-healthcheck.service <<'UNIT'
[Unit]
Description=fleet healthcheck (personal fleet, ADR 0017 / runbook §3)
After=network-online.target
Wants=network-online.target

[Service]
Type=oneshot
EnvironmentFile=/etc/fleet/healthcheck.env
LoadCredential=token:/etc/fleet/node-token
ExecStart=/usr/local/sbin/fleet-healthcheck
Nice=10
UNIT
cat > /etc/systemd/system/fleet-healthcheck.timer <<'UNIT'
[Unit]
Description=fleet healthcheck hourly at :30 UTC (23:30 = daily)

[Timer]
OnCalendar=*-*-* *:30:00 UTC
RandomizedDelaySec=120
Persistent=true

[Install]
WantedBy=timers.target
UNIT
systemctl daemon-reload
systemctl enable --now fleet-healthcheck.timer >/dev/null
systemctl start fleet-healthcheck.service
echo "--- timer"; systemctl list-timers fleet-healthcheck.timer --no-pager | head -3
echo "--- last run"; journalctl -u fleet-healthcheck.service -n 3 --no-pager -o cat
echo "--- latest.json (摘要)"; python3 -c '
import json; d=json.load(open("/var/lib/fleet/latest.json"))
print({k:d[k] for k in ("node","ts","mode","est443_public","tx_bytes")}); print("services:",d["services"]); print("peers:",d["peers"]); print("host:",d["host"])'
REMOTE
}

if [ "$DRY" = 1 ]; then
  remote_script "<token>" | sed 's/HC_B64=[^ ]*/HC_B64=<base64 of healthcheck.sh>/'
  exit 0
fi

printf '▸ 安装到 %s（%s / %s）…\n' "$NODE" "$PROJECT" "$ZONE"
remote_script "$TOKEN" | gcloud compute ssh "$NODE" --project="$PROJECT" --zone="$ZONE" \
  --tunnel-through-iap --quiet --command="sudo bash -s" 2>&1 | grep -v -E '^(please see|WARNING:|$|To increase the performance)'

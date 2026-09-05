#!/usr/bin/env bash
# =============================================================================
# healthcheck.sh · 自用机队节点侧巡检（在节点上以 root 由 systemd timer 运行）
#
# 事实源：docs/04-ops/personal-fleet-runbook.md §3.2（五组判据）、§3.5（装法）
#         docs/04-ops/runbook-node-health.md §3（IP 封锁三判据；本脚本只能测到 ①② + 对邻居的 ④）
#
# 五组：A 进程与端口 · B 封锁取证（①②③ 本机 + ④ 对邻居回打）· C 用量（本机 tx_bytes 累计，
#       Worker 差分成月度）· D 证书到期 · E 主机健康（CPU/内存/盘/OOM/待重启/BBR+fq 漂移）
#
# 只做三件事：采集 → 写 /var/lib/fleet/latest.json → POST 到 Worker 的 /ingest/<token>。
# 节点上**没有任何飞书凭据**；它需要的只是「把 JSON 交出去」的能力（ADR 0017 §6）。
#
# 🔴 不用 ping / dig / nc / curl 判连通性（runbook §3.2）。这里跑在节点上、没有 TUN，
#    对邻居的 TCP 握手用 bash 的 /dev/tcp + timeout，只证明「路径通 + 443 有人听」。
#
# 输入（systemd EnvironmentFile=/etc/fleet/healthcheck.env + LoadCredential=token）：
#   FLEET_INGEST_URL   Worker 基址，如 https://fleet-sub.<sub>.workers.dev 或 https://<订阅域名>
#   FLEET_NODE_NAME    本机在 fleet.json 里的 host
#   FLEET_IFACE        出网网卡（默认取第一块非 lo）
#   FLEET_SS_PORT      SS 端口（默认 48882）
#   FLEET_PEERS        兜底的对端表 "vpn-us=8.231.52.43,vpn-jp=…"（正常从 Worker GET /fleet 取）
#   $CREDENTIALS_DIRECTORY/token   节点 token（LoadCredential 注入，不进 unit、不进环境变量）
#
# 用法：fleet-healthcheck [--daily] [--dry-run]     （--dry-run 只打印 JSON，不 POST）
# =============================================================================
set -uo pipefail
export PATH="/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"

MODE="hourly"; DRY=0
for a in "$@"; do
  case "$a" in
    --daily)   MODE="daily" ;;
    --dry-run) DRY=1 ;;
    *) printf 'unknown arg: %s\n' "$a" >&2; exit 2 ;;
  esac
done
# 23:xx UTC 那一次自动升格为 daily（fleet.json .report.node_timer_utc）
if [ "$(date -u +%H)" = "23" ]; then MODE="daily"; fi

NODE="${FLEET_NODE_NAME:-$(hostname)}"
BASE="${FLEET_INGEST_URL:-}"
SS_PORT="${FLEET_SS_PORT:-48882}"
IFACE="${FLEET_IFACE:-}"
if [ -z "$IFACE" ]; then
  for d in /sys/class/net/*; do n="$(basename "$d")"; [ "$n" = "lo" ] && continue; IFACE="$n"; break; done
fi
TOKEN=""
if [ -n "${CREDENTIALS_DIRECTORY:-}" ] && [ -f "$CREDENTIALS_DIRECTORY/token" ]; then
  TOKEN="$(tr -d '\n' < "$CREDENTIALS_DIRECTORY/token")"
elif [ -f /etc/fleet/node-token ]; then
  TOKEN="$(tr -d '\n' < /etc/fleet/node-token)"
fi

TS="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
BOOT_ID="$(cat /proc/sys/kernel/random/boot_id 2>/dev/null || echo unknown)"
UPTIME_S="$(cut -d. -f1 /proc/uptime 2>/dev/null || echo 0)"

# ── A · 进程与端口 ──
svc_state() { systemctl is-active "$1" 2>/dev/null || true; }
S_XRAY="$(svc_state xray)"; S_SS="$(svc_state ssserver)"; S_HY="$(svc_state hysteria-server)"; S_CF="$(svc_state cloudflared)"
listen_tcp() { ss -Hltn "sport = :$1" 2>/dev/null | grep -q . && echo true || echo false; }
listen_udp() { ss -Hlun "sport = :$1" 2>/dev/null | grep -q . && echo true || echo false; }
L_TCP443="$(listen_tcp 443)"; L_UDP443="$(listen_udp 443)"; L_SS_TCP="$(listen_tcp "$SS_PORT")"; L_SS_UDP="$(listen_udp "$SS_PORT")"

# ── B · 封锁取证 ②：443 上来自公网的 established 数（排除私网 / 环回 / 链路本地）──
EST443="$(ss -Htn state established '( sport = :443 )' 2>/dev/null \
  | awk '{print $4}' | sed -E 's/^\[?([^]]*)\]?:[0-9]+$/\1/' \
  | grep -Evc '^(10\.|127\.|192\.168\.|172\.(1[6-9]|2[0-9]|3[01])\.|169\.254\.|::1$|fe80:|fc|fd)' || true)"
EST443="${EST443:-0}"

# ── B ③：服务日志最后一条的时间戳（秒龄）──
log_age() {
  local ts
  ts="$(journalctl -u "$1" -n1 -o short-iso --no-pager 2>/dev/null | tail -1 | awk '{print $1}')"
  if [ -n "$ts" ] && date -d "$ts" +%s >/dev/null 2>&1; then
    echo $(( $(date +%s) - $(date -d "$ts" +%s) ))
  else
    echo null
  fi
}
LOG_XRAY="$(log_age xray)"; LOG_HY="$(log_age hysteria-server)"; LOG_SS="$(log_age ssserver)"

# ── B ④：对邻居 443 做 TCP 握手（从境外回打）。对端表优先从 Worker 取，兜底用 FLEET_PEERS ──
PEERS_JSON="null"
peer_list=""
if [ -n "$BASE" ] && [ -n "$TOKEN" ]; then
  peer_list="$(curl -sS -m 15 -H "Authorization: Bearer $TOKEN" "$BASE/fleet" 2>/dev/null \
    | ME="$NODE" python3 -c '
import json,sys,os
me=os.environ.get("ME","")
try:
    d=json.load(sys.stdin)
except Exception:
    sys.exit(0)
for n in d.get("nodes",[]):
    if n.get("host")==me or (n.get("status") or "running")=="planned" or not n.get("ip"): continue
    print("%s=%s"%(n["host"],n["ip"]))' 2>/dev/null | tr '\n' ',' | sed 's/,$//')"
fi
[ -z "$peer_list" ] && peer_list="${FLEET_PEERS:-}"
if [ -n "$peer_list" ]; then
  PEERS_JSON="{"
  first=1
  IFS=',' read -r -a arr <<< "$peer_list"
  for kv in "${arr[@]}"; do
    h="${kv%%=*}"; ip="${kv#*=}"
    if [ -z "$h" ] || [ -z "$ip" ]; then continue; fi
    t0="$(date +%s%N)"
    if timeout 5 bash -c "exec 3<>/dev/tcp/$ip/443" 2>/dev/null; then ok=true; else ok=false; fi
    t1="$(date +%s%N)"
    ms=$(( (t1 - t0) / 1000000 ))
    [ "$first" = 1 ] || PEERS_JSON="$PEERS_JSON,"
    first=0
    PEERS_JSON="$PEERS_JSON\"$h\":{\"ip\":\"$ip\",\"ok\":$ok,\"tcp443_ms\":$ms}"
  done
  PEERS_JSON="$PEERS_JSON}"
fi

# ── C · 用量：本机累计 tx/rx（Worker 按 boot_id 差分成月度）──
TX="$(cat "/sys/class/net/$IFACE/statistics/tx_bytes" 2>/dev/null || echo 0)"
RX="$(cat "/sys/class/net/$IFACE/statistics/rx_bytes" 2>/dev/null || echo 0)"

# ── D · 证书 ──
CERT_JSON="{"
first=1
for c in /etc/hysteria/server.crt /etc/hysteria/cert.pem /etc/letsencrypt/live/*/fullchain.pem; do
  [ -f "$c" ] || continue
  end="$(openssl x509 -in "$c" -noout -enddate 2>/dev/null | cut -d= -f2)"
  [ -n "$end" ] || continue
  days=$(( ( $(date -d "$end" +%s) - $(date +%s) ) / 86400 ))
  name="hysteria"; case "$c" in /etc/letsencrypt/*) name="letsencrypt" ;; esac
  [ "$first" = 1 ] || CERT_JSON="$CERT_JSON,"
  first=0
  CERT_JSON="$CERT_JSON\"$name\":{\"path\":\"$c\",\"days_left\":$days}"
done
CERT_JSON="$CERT_JSON}"

# ── E · 主机健康 ──
read -r _ u1 n1 s1 i1 _ < /proc/stat; sleep 1; read -r _ u2 n2 s2 i2 _ < /proc/stat
busy=$(( (u2+n2+s2) - (u1+n1+s1) )); total=$(( busy + (i2 - i1) ))
CPU_PCT=$(( total > 0 ? busy * 100 / total : 0 ))
MEM_PCT="$(awk '/MemTotal/{t=$2} /MemAvailable/{a=$2} END{ if(t>0) printf "%d", (t-a)*100/t; else print 0}' /proc/meminfo)"
SWAP_PCT="$(awk '/SwapTotal/{t=$2} /SwapFree/{f=$2} END{ if(t>0) printf "%d", (t-f)*100/t; else print 0}' /proc/meminfo)"
DISK_PCT="$(df -P / 2>/dev/null | awk 'NR==2{gsub("%","",$5); print $5}')"
LOAD1="$(cut -d' ' -f1 /proc/loadavg 2>/dev/null || echo 0)"
OOM="$(journalctl -k --since -24h --no-pager 2>/dev/null | grep -c -i 'out of memory' || true)"
REBOOT=false; [ -f /var/run/reboot-required ] && REBOOT=true
TCP_CC="$(sysctl -n net.ipv4.tcp_congestion_control 2>/dev/null || echo unknown)"
QDISC="$(sysctl -n net.core.default_qdisc 2>/dev/null || echo unknown)"
KERNEL="$(uname -r)"

# ── 组 JSON（python 负责转义与类型，避免手写 JSON 的引号坑）──
JSON="$(NODE="$NODE" TS="$TS" MODE="$MODE" BOOT_ID="$BOOT_ID" UPTIME_S="$UPTIME_S" \
  S_XRAY="$S_XRAY" S_SS="$S_SS" S_HY="$S_HY" S_CF="$S_CF" \
  L_TCP443="$L_TCP443" L_UDP443="$L_UDP443" L_SS_TCP="$L_SS_TCP" L_SS_UDP="$L_SS_UDP" \
  EST443="$EST443" LOG_XRAY="$LOG_XRAY" LOG_HY="$LOG_HY" LOG_SS="$LOG_SS" PEERS_JSON="$PEERS_JSON" \
  TX="$TX" RX="$RX" IFACE="$IFACE" CERT_JSON="$CERT_JSON" \
  CPU_PCT="$CPU_PCT" MEM_PCT="$MEM_PCT" SWAP_PCT="$SWAP_PCT" DISK_PCT="${DISK_PCT:-0}" LOAD1="$LOAD1" OOM="$OOM" \
  REBOOT="$REBOOT" TCP_CC="$TCP_CC" QDISC="$QDISC" KERNEL="$KERNEL" \
  python3 - <<'PY'
import json, os
E = os.environ
def num(v):
    try: return int(v)
    except Exception:
        try: return float(v)
        except Exception: return None
def jl(v):
    try: return json.loads(v)
    except Exception: return None
def nn(v): return None if v == "null" else num(v)
doc = {
  "version": 1, "node": E["NODE"], "ts": E["TS"], "mode": E["MODE"],
  "boot_id": E["BOOT_ID"], "uptime_s": num(E["UPTIME_S"]),
  "services": {"xray": E["S_XRAY"] or "unknown", "ssserver": E["S_SS"] or "unknown",
               "hysteria-server": E["S_HY"] or "unknown", "cloudflared": E["S_CF"] or "unknown"},
  "listen": {"tcp443": E["L_TCP443"] == "true", "udp443": E["L_UDP443"] == "true",
             "ss_tcp": E["L_SS_TCP"] == "true", "ss_udp": E["L_SS_UDP"] == "true"},
  "est443_public": num(E["EST443"]),
  "log_age_s": {"xray": nn(E["LOG_XRAY"]), "hysteria-server": nn(E["LOG_HY"]), "ssserver": nn(E["LOG_SS"])},
  "peers": jl(E["PEERS_JSON"]),
  "iface": E["IFACE"], "tx_bytes": num(E["TX"]), "rx_bytes": num(E["RX"]),
  "cert": jl(E["CERT_JSON"]) or {},
  "host": {"cpu_pct": num(E["CPU_PCT"]), "mem_pct": num(E["MEM_PCT"]), "swap_pct": num(E["SWAP_PCT"]),
           "disk_pct": num(E["DISK_PCT"]), "load1": num(E["LOAD1"]), "oom_recent": num(E["OOM"]),
           "reboot_required": E["REBOOT"] == "true", "tcp_cc": E["TCP_CC"], "qdisc": E["QDISC"], "kernel": E["KERNEL"]},
}
print(json.dumps(doc, ensure_ascii=False))
PY
)"

mkdir -p /var/lib/fleet
printf '%s\n' "$JSON" > /var/lib/fleet/latest.json.tmp && mv /var/lib/fleet/latest.json.tmp /var/lib/fleet/latest.json

if [ "$DRY" = 1 ]; then
  printf '%s\n' "$JSON"
  exit 0
fi
if [ -z "$BASE" ] || [ -z "$TOKEN" ]; then
  printf 'healthcheck: 未配置 FLEET_INGEST_URL 或 token，只落盘不上报（%s）\n' "$MODE" >&2
  exit 0
fi
code="$(curl -sS -m 20 -o /dev/null -w '%{http_code}' -X POST "$BASE/ingest/$TOKEN" \
  -H 'Content-Type: application/json' -H "Authorization: Bearer $TOKEN" --data-binary "$JSON" 2>/dev/null || echo 000)"
if [ "$code" = "204" ]; then
  printf 'healthcheck: %s 上报成功（%s）\n' "$NODE" "$MODE"
else
  printf 'healthcheck: 上报失败 HTTP %s（已落盘 /var/lib/fleet/latest.json）\n' "$code" >&2
  exit 1
fi

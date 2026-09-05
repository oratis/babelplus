#!/usr/bin/env bash
# =============================================================================
# publish-subscription.sh · 把 gen-subscription.py 的产物、设备/节点 token、fleet 副本 PUT 进 Cloudflare KV
#
# 事实源：docs/04-ops/personal-fleet-runbook.md §2.3 / §2.4 / §2.6；infra/fleet/worker/src/index.js（KV 布局）
#
# 用法（本机）：
#   set -a; source infra/fleet/.secrets.env; set +a
#   infra/fleet/publish-subscription.sh                  # 渲染 + 发布四种产物 + token + fleet
#   infra/fleet/publish-subscription.sh --no-gen         # 只发布 out/ 里已有的产物
#   infra/fleet/publish-subscription.sh --refresh-cn-cidr   # 顺手更新自托管的 CN CIDR 列表（需本机能出网）
#   infra/fleet/publish-subscription.sh --revoke iphone  # 吊销一台设备（删它的 token；产物不动）
#   infra/fleet/publish-subscription.sh --list           # 看 KV 里现在有什么
#   infra/fleet/publish-subscription.sh --dry-run        # 只打印将要执行的 wrangler 命令
#
# 换 IP 的新流程（runbook §2.6）：改 fleet.json 一行 → 本脚本 → 完。客户端在 ≤ provider interval 内拿到。
# wrangler 走 OAuth 登录（wrangler whoami），命名空间 id 在 fleet.json .subscription.kv_namespace_id。
# =============================================================================
set -euo pipefail

HERE="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
FLEET_JSON="$HERE/fleet.json"
OUT="$HERE/out"
WORKER_DIR="$HERE/worker"
NS="$(jq -r '.subscription.kv_namespace_id' "$FLEET_JSON")"
ACCT="$(jq -r '.subscription.cloudflare_account_id' "$FLEET_JSON")"
export CLOUDFLARE_ACCOUNT_ID="$ACCT"

CN_CIDR_URL="${CN_CIDR_URL:-https://raw.githubusercontent.com/17mon/china_ip_list/master/china_ip_list.txt}"

GEN=1; DRY=0; REFRESH_CN=0; REVOKE=""; LIST=0
GEN_ARGS=()
while [ $# -gt 0 ]; do
  case "$1" in
    --no-gen) GEN=0 ;;
    --dry-run) DRY=1 ;;
    --refresh-cn-cidr) REFRESH_CN=1 ;;
    --revoke) REVOKE="${2:?--revoke 需要设备 id}"; shift ;;
    --list) LIST=1 ;;
    --brutal|--geoip|--sub-domain|--rules-from) GEN_ARGS+=("$1" "${2:?$1 需要值}"); shift ;;
    -h|--help) sed -n '2,20p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) printf '未知参数：%s\n' "$1" >&2; exit 2 ;;
  esac
  shift
done

die() { printf '\033[31m✗ %s\033[0m\n' "$*" >&2; exit 1; }
say() { printf '▸ %s\n' "$*"; }
kv() {
  if [ "$DRY" = 1 ]; then printf '  [dry-run] wrangler kv %s\n' "$*"; return 0; fi
  (cd "$WORKER_DIR" && wrangler kv "$@" --namespace-id "$NS" --remote 2>&1 | grep -v -E '^\s*$|⛅|─' ) || true
}
command -v wrangler >/dev/null || die "缺少 wrangler"
command -v jq >/dev/null || die "缺少 jq"
if [ -z "$NS" ] || [ "$NS" = "null" ]; then die "fleet.json 缺 .subscription.kv_namespace_id"; fi

if [ "$LIST" = 1 ]; then
  kv key list
  exit 0
fi

if [ -n "$REVOKE" ]; then
  var="DEVICE_TOKEN_$(printf '%s' "$REVOKE" | tr '[:lower:]-' '[:upper:]_')"
  : "${!var:?缺少 $var（set -a; source infra/fleet/.secrets.env; set +a）}"
  say "吊销设备 $REVOKE（删 tok/dev/<token>；产物不动，其余设备不受影响）"
  kv key delete "tok/dev/${!var}"
  say "完成。该设备的订阅 URL 从此 404。要恢复：换一个新 token 写进 .secrets.env 再 publish。"
  exit 0
fi

# ── 1 · 渲染 ──
if [ "$GEN" = 1 ]; then
  say "渲染产物（gen-subscription.py ${GEN_ARGS[*]+${GEN_ARGS[*]}}）"
  # ${arr[@]+"${arr[@]}"}：空数组在 macOS 自带的 bash 3.2 下会触发 set -u 的 unbound variable
  python3 "$HERE/gen-subscription.py" --fleet "$FLEET_JSON" --secrets "$HERE/.secrets.env" --out "$OUT" ${GEN_ARGS[@]+"${GEN_ARGS[@]}"}
fi
for f in mihomo-provider.yaml clash.yaml singbox.json base64.txt; do
  [ -s "$OUT/$f" ] || die "缺产物 $OUT/$f（先跑 gen-subscription.py）"
done

# ── 2 · CN CIDR（自托管，B46）──
if [ "$REFRESH_CN" = 1 ] || [ ! -s "$OUT/cn-cidr.txt" ]; then
  say "拉取 CN CIDR 列表：$CN_CIDR_URL"
  if [ "$DRY" = 1 ]; then
    printf '  [dry-run] curl %s → %s/cn-cidr.txt\n' "$CN_CIDR_URL" "$OUT"
  else
    curl -fsSL -m 60 "$CN_CIDR_URL" -o "$OUT/cn-cidr.txt.tmp" || die "拉不到 CN CIDR（本机没代理？用 CN_CIDR_URL 指别的源）"
    n="$(grep -c -E '^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+/[0-9]+$' "$OUT/cn-cidr.txt.tmp" || true)"
    [ "${n:-0}" -gt 1000 ] || die "CN CIDR 列表只有 $n 行，不像是对的"
    grep -E '^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+/[0-9]+$' "$OUT/cn-cidr.txt.tmp" > "$OUT/cn-cidr.txt"
    rm -f "$OUT/cn-cidr.txt.tmp"
    # sing-box 的 source 格式 rule-set
    python3 - "$OUT" <<'PY'
import json, sys, pathlib
out = pathlib.Path(sys.argv[1])
cidrs = [l.strip() for l in (out / "cn-cidr.txt").read_text().splitlines() if l.strip()]
(out / "cn-cidr.json").write_text(json.dumps({"version": 1, "rules": [{"ip_cidr": cidrs}]}) + "\n")
print(f"  cn-cidr: {len(cidrs)} 条 → cn-cidr.txt / cn-cidr.json")
PY
  fi
fi

# ── 3 · 产物进 KV ──
say "发布产物 → KV $NS"
for f in mihomo-provider.yaml clash.yaml singbox.json base64.txt cn-cidr.txt cn-cidr.json; do
  [ -s "$OUT/$f" ] || { printf '  跳过 %s（无文件）\n' "$f"; continue; }
  kv key put "sub/$f" --path "$OUT/$f"
  printf '  sub/%s\n' "$f"
done

# ── 4 · token 与 fleet 副本（bulk）──
say "发布设备 token / 节点 token / fleet 副本"
BULK="$(mktemp "${TMPDIR:-/tmp}/fleet-bulk.XXXXXX")"
python3 - "$FLEET_JSON" "$BULK" <<'PY'
import json, os, sys
fleet = json.load(open(sys.argv[1], encoding="utf-8"))
items = []
missing = []
for d in fleet["devices"]:
    var = "DEVICE_TOKEN_" + d["id"].upper().replace("-", "_")
    tok = os.environ.get(var, "")
    if not tok: missing.append(var); continue
    items.append({"key": f"tok/dev/{tok}", "value": d["id"]})
for n in fleet["nodes"]:
    var = "NODE_TOKEN_" + n["host"].upper().replace("-", "_")
    tok = os.environ.get(var, "")
    if not tok: missing.append(var); continue
    items.append({"key": f"tok/node/{tok}", "value": n["host"]})
if missing:
    sys.exit("缺少环境变量: " + ", ".join(missing) + "（set -a; source infra/fleet/.secrets.env; set +a）")
pub = {k: v for k, v in fleet.items() if not k.startswith("_")}
items.append({"key": "fleet", "value": json.dumps(pub, ensure_ascii=False)})
json.dump(items, open(sys.argv[2], "w"), ensure_ascii=False)
print(f"  {len(items)-1} 个 token + fleet 副本（{len(fleet['nodes'])} 节点）")
PY
kv bulk put "$BULK"
rm -f "$BULK"

say "完成。验证（把 <token> 换成 .secrets.env 里某台设备的）："
base="$(jq -r '.subscription.hostname // empty' "$FLEET_JSON")"
[ -n "$base" ] && base="https://$base" || base="${FLEET_INGEST_URL:-https://fleet-sub.<sub>.workers.dev}"
printf '  curl -sSI %s/p/<token>/mihomo-provider.yaml | grep -i -E "subscription-userinfo|profile-update"\n' "$base"

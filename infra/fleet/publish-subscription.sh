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
#   infra/fleet/publish-subscription.sh --refresh-cn-cidr   # 顺手更新自托管的 CN CIDR 列表（IPv4 + IPv6，需本机能出网）
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
# 17mon 只有 IPv4。CN 的 IPv6 取 APNIC delegated 统计里 apnic|CN|ipv6|…|allocated/assigned 的行，Python ipaddress 合并。
# 少了这部分，国内 v6 目标（电信 240e、联通 2408、阿里 DNS 2400:3200…）漏过 RULE-SET,cn-cidr 落到 MATCH 走代理，
# 而节点没有 IPv6，全部失败（2026-11-18 排查：vpn-jp 一天三千多次，微信 / 阿里 DNS 先失败再回落 v4）。
CN_CIDR6_URL="${CN_CIDR6_URL:-https://ftp.apnic.net/stats/apnic/delegated-apnic-latest}"
# APNIC 没登记在 CN、但国内客户端拿来访问国内服务的 IPv6 段，空格分隔，与上面一起合并（设成空串 = 不加）：
#   240d:c000::/24  腾讯微信 / QQ（APNIC 登记在 SG）
CN_CIDR6_EXTRA="${CN_CIDR6_EXTRA-240d:c000::/24}"
# 只认这两种形状的行，其余当垃圾丢掉；各自条数低于下限就拒绝发布（之后 Python 再逐条 ipaddress 解析一遍）
CIDR4_RE='^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+/[0-9]+$'
CIDR6_RE='^[0-9a-f]{0,4}(:[0-9a-f]{0,4}){2,7}/[0-9]+$'

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
for f in mihomo-provider.yaml iphone-shadowrocket.yaml clash.yaml singbox.json base64.txt; do
  [ -s "$OUT/$f" ] || die "缺产物 $OUT/$f（先跑 gen-subscription.py）"
done

# ── 2 · CN CIDR（自托管，B46）：IPv4（17mon）在前 + IPv6（APNIC CN ∪ CN_CIDR6_EXTRA，合并）在后 ──
# mihomo 的 behavior: ipcidr 与 sing-box 的 ip_cidr 都接受 v4 / v6 混排，一份列表两种格式共用。
# out/cn-cidr.txt 里一条 IPv6 都没有 = 旧版脚本生成的：原样发布会把 KV 里的 IPv6 冲掉，所以当作必须刷新；
# 刷新失败就停在这里，不发布任何东西。
CN_STALE=0
if [ -s "$OUT/cn-cidr.txt" ] && ! grep -q ':' "$OUT/cn-cidr.txt"; then CN_STALE=1; fi
if [ "$REFRESH_CN" = 1 ] || [ ! -s "$OUT/cn-cidr.txt" ] || [ "$CN_STALE" = 1 ]; then
  if [ "$CN_STALE" = 1 ]; then say "out/cn-cidr.txt 没有 IPv6（旧版脚本生成），强制刷新，否则发布会冲掉 KV 里的 IPv6"; fi
  say "拉取 CN CIDR 列表：IPv4 $CN_CIDR_URL ；IPv6 $CN_CIDR6_URL"
  if [ "$DRY" = 1 ]; then
    printf '  [dry-run] curl %s → 只留 IPv4 行（须 > 1000 条）\n' "$CN_CIDR_URL"
    printf '  [dry-run] curl %s → apnic|CN|ipv6|…|allocated/assigned + 额外段 [%s] → ipaddress 合并 → 只留 IPv6 行（须 > 1000 条）\n' "$CN_CIDR6_URL" "$CN_CIDR6_EXTRA"
    printf '  [dry-run] 逐条 ipaddress 校验 → %s/cn-cidr.txt（IPv4 在前、IPv6 在后）/ cn-cidr.json（sing-box source）\n' "$OUT"
  else
    raw4="$OUT/cn-cidr.v4.tmp"; raw6="$OUT/cn-cidr.apnic.tmp"; merged6="$OUT/cn-cidr.v6.tmp"
    trap 'rm -f "$raw4" "$raw6" "$merged6" "$OUT/cn-cidr.txt.tmp" "$OUT/cn-cidr.json.tmp"' EXIT
    curl -fsSL -m 60 "$CN_CIDR_URL" -o "$raw4" || die "拉不到 CN CIDR（本机没代理？用 CN_CIDR_URL 指别的源）"
    n4="$(grep -c -E "$CIDR4_RE" "$raw4" || true)"
    [ "${n4:-0}" -gt 1000 ] || die "CN IPv4 列表只有 $n4 行，不像是对的"
    curl -fsSL -m 120 "$CN_CIDR6_URL" -o "$raw6" || die "拉不到 APNIC delegated（用 CN_CIDR6_URL 指镜像）"
    # IPv6Network() 默认 strict：起始地址带主机位、前缀长度越界都直接抛错 → 整个刷新失败，不会带着坏数据往下走
    python3 - "$raw6" "$CN_CIDR6_EXTRA" > "$merged6" <<'PY' || die "APNIC delegated 解析失败（见上方 Python 报错）"
import ipaddress, sys
src, extra = sys.argv[1], sys.argv[2].split()
nets = []
with open(src, encoding="utf-8", errors="replace") as fh:
    for line in fh:
        # registry|cc|type|start|value|date|status[|opaque-id]；ipv6 行的 value 是前缀长度
        f = line.strip().split("|")
        if len(f) >= 7 and f[0] == "apnic" and f[1] == "CN" and f[2] == "ipv6" and f[6] in ("allocated", "assigned"):
            nets.append(ipaddress.IPv6Network(f"{f[3]}/{f[4]}"))
n_apnic = len(nets)
nets += [ipaddress.IPv6Network(x) for x in extra]
merged = list(ipaddress.collapse_addresses(nets))
for n in merged:
    print(n)
print(f"  IPv6：APNIC CN {n_apnic} 段 + 额外 {len(extra)} 段 → 合并后 {len(merged)} 条", file=sys.stderr)
PY
    n6="$(grep -c -E "$CIDR6_RE" "$merged6" || true)"
    [ "${n6:-0}" -gt 1000 ] || die "CN IPv6 列表只有 $n6 行，不像是对的"
    { grep -E "$CIDR4_RE" "$raw4"; grep -E "$CIDR6_RE" "$merged6"; } > "$OUT/cn-cidr.txt.tmp"
    rm -f "$raw4" "$raw6" "$merged6"
    # 逐条 ipaddress 解析（正则放过的 999.1.1.1/40 这类在这里拒掉），再生成 sing-box 的 source 格式 rule-set；
    # 两个文件都校验通过才替换 out/ 里的旧文件
    python3 - "$OUT" <<'PY' || die "cn-cidr 校验失败，out/ 里的旧列表未动"
import ipaddress, json, sys, pathlib
out = pathlib.Path(sys.argv[1])
cidrs = [l.strip() for l in (out / "cn-cidr.txt.tmp").read_text().splitlines() if l.strip()]
bad, fam = [], {4: 0, 6: 0}
for c in cidrs:
    try:
        fam[ipaddress.ip_network(c, strict=False).version] += 1
    except ValueError:
        bad.append(c)
if bad:
    sys.exit(f"  {len(bad)} 行不是合法 CIDR，例如：{', '.join(bad[:5])}")
(out / "cn-cidr.json.tmp").write_text(json.dumps({"version": 1, "rules": [{"ip_cidr": cidrs}]}) + "\n")
print(f"  cn-cidr: {len(cidrs)} 条（IPv4 {fam[4]} + IPv6 {fam[6]}）→ cn-cidr.txt / cn-cidr.json")
PY
    mv "$OUT/cn-cidr.txt.tmp" "$OUT/cn-cidr.txt"
    mv "$OUT/cn-cidr.json.tmp" "$OUT/cn-cidr.json"
  fi
fi

# ── 3 · 产物进 KV ──
say "发布产物 → KV $NS"
for f in mihomo-provider.yaml iphone-shadowrocket.yaml clash.yaml singbox.json base64.txt cn-cidr.txt cn-cidr.json; do
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

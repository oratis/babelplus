#!/usr/bin/env bash
#
# verify-isolation.sh —— 「不影响已经部署的服务」这条承诺的**可执行形式**
#
# 事实源：docs/02-architecture/as-built-gcp.md §2（vpn-* 节点）· §3（防火墙规则）
#        · §4（三个 Cloud Run 服务与 cloud-run-source-deploy）· §5（secret 与 SA）· §2.1（隔离承诺）
#        infra/fleet/fleet.json（**vpn 侧的全部期望：节点 / 保留 IP / 非 bp- 防火墙清单 / SSH 压制链 / 跨机队 deny**）
#        docs/05-adr/0017-personal-fleet-in-repo.md §3（反向断言的裁决）
#        docs/04-ops/deploy.md §2（部署前后各跑一次，不允许跳过）· §13（部署后核对清单）
#
# **部署前后各跑一次。有任何差异一律非零退出。**
#
# 两层判定，各自能独立成立：
#
#   第一层 · 硬期望（不需要任何基线文件）
#     vpn 侧的期望从 infra/fleet/fleet.json 读（2026-09-05 起，roadmap B69）；
#     lisa-* / Cloud Run / secret / SA 那部分仍写死在本脚本里（它们不属于机队清单）。
#     它能回答「现在这一刻，现有资产是不是还是清单里那个样子」。
#     好处是**第一次跑就有判定力** —— 不需要「先有 before 快照」这个前提。
#
#   第二层 · 基线 diff（--baseline=<目录>）
#     把本次观测到的全部**非 bp- 资源**写成一份规范化文本，与部署前那份逐字节比。
#     它能抓住硬期望覆盖不到的东西：secret 版本数、修订版名、SA 列表变化……
#
# 🔴 为什么期望改成读 fleet.json 而不是继续写死（2026-09-05）：
#    2026-09-04 21:15 CST 用户给自用队加了一条 vpn-deny-from-bp，本脚本当晚就红了
#    （非 bp- 规则 11 ≠ 硬期望 10），而 deploy.yml 的两个部署作业都 needs: isolation-before。
#    写死的清单让「现实合理地变了」和「有人把事情做坏了」在脚本眼里长得一模一样，
#    于是每次现实变化都会诱使人把脚本改宽松 —— 那一刻隔离就名存实亡。
#    清单进 fleet.json（入库、受版本控制、CI 可跑），改期望就是一次可评审的 diff。
#
# 🔴 这个脚本是**事后发现**不是**事前阻止**（as-built §8 的代价第 2 条）：
#    一条打错名字的 gcloud compute firewall-rules delete，它能告诉你出事了，但拦不住你。
#    真正的机制隔离要独立 GCP 项目 + 共享 VPC，那是另一次裁决。
#    唯一一条 GCP 强制的隔离是 vpn-deny-from-bp（bp-node → vpn-node 内网全拒），本脚本正向守它。
#
# 本脚本**只做只读调用**。没有任何 create / update / delete。

set -euo pipefail

readonly EXPECTED_PROJECT_ID="oratis-491316"
readonly REGION="us-central1"

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
FLEET_JSON="${FLEET_JSON:-$ROOT/infra/fleet/fleet.json}"

# ───────────────────────── 写死的那部分期望（非机队资产）─────────────────────────
#
# 改这些常量之前先问一句：**是现实变了，还是我们把事情做坏了？**
# 只有前者才应该改这里，并且必须同步更新 docs/02-architecture/as-built-gcp.md。

# as-built §4 的三个现有 Cloud Run 服务。名字|URL。
EXPECT_RUN=(
  # ⚠️ 2026-08-17 实测修正：Cloud Run 的默认 URL 形式已从
  #    `<svc>-<项目号>.<region>.run.app` 改为 `<svc>-<哈希>-<区域缩写>.a.run.app`。
  #    as-built §4 最初记的是前者（从截断的列表里抄的），与线上不符，
  #    导致本检查开工第一天就三条全红。下面是实测值。
  "anthropic-relay|https://anthropic-relay-cko3zfff5a-uc.a.run.app"
  "lisa-cloud|https://lisa-cloud-cko3zfff5a-uc.a.run.app"
  "lisa-web|https://lisa-web-cko3zfff5a-uc.a.run.app"
)

# as-built §5 的现有 secret 与服务账号。
EXPECT_SECRETS=(anthropic-api-key relay-token)
EXPECT_SA=(
  "2360090741-compute@developer.gserviceaccount.com"
  "vertex-express@oratis-491316.iam.gserviceaccount.com"
  "cuddler-play-billing@oratis-491316.iam.gserviceaccount.com"
)

readonly EXPECT_AR_REPO="cloud-run-source-deploy"

# ───────────────────────── 运行时开关 ─────────────────────────

PROJECT_ID="${PROJECT_ID:-$EXPECTED_PROJECT_ID}"
BASELINE=""
OUT_DIR=""
DRY_RUN=0
KEEP_OUT=0
PASS_N=0
FAIL_N=0
TMP_DIR=""

# 从 fleet.json 装进来的期望（load_fleet 填）
FLEET_VPN_TAG=""
FLEET_BP_TAG=""
FLEET_VPN_PREFIX=""
FLEET_BP_PREFIX=""
FLEET_SSH_IAP=""
FLEET_SSH_DENY=""
FLEET_SSH_DEFAULT=""
FLEET_SSH_TAG=""
FLEET_XDENY_NAME=""
FLEET_XDENY_SRC=""
FLEET_XDENY_DST=""
FLEET_XDENY_OUTRANK=""

# ───────────────────────── 通用工具 ─────────────────────────

log()  { printf '%s\n' "$*" >&2; }
step() { printf '\n\033[1m▸ %s\033[0m\n' "$*" >&2; }
die()  { printf '\n\033[31m✗ %s\033[0m\n' "$*" >&2; exit 2; }

# qq 打印一个参数：只在需要时加单引号。
# 不用 printf '%q' —— 它会把中文转成八进制转义（\346\216\247…），而 dry-run 的输出是给人读的。
qq() {
  case "$1" in
    ''|*[!A-Za-z0-9_@%+=:,./~-]*)
      printf "'%s'" "$(printf '%s' "$1" | sed "s/'/'\\\\''/g")" ;;
    *) printf '%s' "$1" ;;
  esac
}


pass() { printf '  \033[32m✓\033[0m %s\n' "$*" >&2; PASS_N=$((PASS_N + 1)); }
fail() { printf '  \033[31m✗ %s\033[0m\n' "$*" >&2; FAIL_N=$((FAIL_N + 1)); }
warn() { printf '  \033[33m⚠\033[0m %s\n' "$*" >&2; }

usage() {
  cat <<'EOF'
用法: verify-isolation.sh [选项]

确认 vpn-* 自用机队 / anthropic-relay / lisa-cloud / lisa-web 以及它们周边的
现有资产**未被 babel.plus 的部署影响**，且两支机队没有交叉。**部署前后各跑一次。**

vpn 侧的期望（节点、保留 IP、非 bp- 防火墙清单、SSH 压制链、跨机队 deny）读自
infra/fleet/fleet.json（可用 FLEET_JSON=<路径> 覆盖）。**现实合理地变了就改清单并提交，
不要改脚本、更不要把判定改宽松。**

退出码:
  0  全部通过
  1  有差异 —— 立即停止部署 / 立即排查
  2  用法或环境错误（缺 gcloud / jq、项目 ID 不对、fleet.json 不合法……）

选项:
  --baseline=<目录>  与之前一次运行留下的 isolation.txt 逐字节比对
  --out=<目录>       本次观测写到哪里（默认写临时目录，退出时删）
  --keep             保留临时目录（配合默认 --out 用）
  --project=<id>     GCP 项目 ID。必须是 oratis-491316
  --dry-run          只打印将要执行的只读命令，不真的调用
  -h, --help         显示本帮助

典型用法（部署前后各一次）:
  ./infra/scripts/verify-isolation.sh --out=snapshots/before   # 部署前
  # …部署…
  ./infra/scripts/verify-isolation.sh --baseline=snapshots/before

判定口径:
  不是「diff 为空」—— 新增 bp- 资源本来就会让 diff 非空。
  而是「**排除 bp- 前缀之后的部分必须逐字节相同**」，本脚本产出的 isolation.txt
  按构造就只含非 bp- 资源，所以可以直接 diff。
EOF
}

guard_project() {
  if [ "$PROJECT_ID" != "$EXPECTED_PROJECT_ID" ]; then
    die "PROJECT_ID 必须是 $EXPECTED_PROJECT_ID，当前是 \"$PROJECT_ID\"。
     本脚本里写死的全部期望值都来自 as-built-gcp.md 对这一个项目的实测，换项目就毫无意义。"
  fi
}

need_cmd() {
  command -v "$1" >/dev/null 2>&1 || die "缺少命令：$1"
}

# 由 main 里的 trap cleanup EXIT 调用，shellcheck 看不出间接调用。
# 两个码都要留：0.9.0（CI 的 ubuntu-24.04 预装版）报 SC2317，SC2329 是 0.10.0 才引入的。
# shellcheck disable=SC2317,SC2329
cleanup() {
  if [ "$KEEP_OUT" -eq 0 ] && [ -n "$TMP_DIR" ] && [ -d "$TMP_DIR" ]; then
    rm -rf "$TMP_DIR"
  fi
}

# fetch <名字> <gcloud 参数...> —— 只读，结果落到 $OUT_DIR/<名字>.json
fetch() {
  local name="$1"; shift
  if [ "$DRY_RUN" -eq 1 ]; then
    local _a
    printf '  [dry-run] ' >&2
    for _a in "$@"; do qq "$_a" >&2; printf ' ' >&2; done
    printf -- '--format=json\n' >&2
    printf '[]' > "${OUT_DIR}/${name}.json"
    return 0
  fi
  if ! "$@" --format=json > "${OUT_DIR}/${name}.raw" 2>"${OUT_DIR}/${name}.err"; then
    fail "取不到 ${name}（gcloud 调用失败，见 ${OUT_DIR}/${name}.err）。
     取不到 = 判定不了 = **当作失败**。隔离检查不允许「查不到就算通过」。"
    printf '[]' > "${OUT_DIR}/${name}.json"
    return 1
  fi

  # ⚠️ gcloud 会把噪音写进 **stdout** 且仍然 exit 0，于是 --format=json 的输出不是合法 JSON。
  # 2026-08-17 实测：`gcloud artifacts repositories list` 在 Python 3.9 的环境下，
  # stdout 第一行是 `An error occurred: module 'importlib.metadata' has no attribute
  # 'packages_distributions'`，紧跟才是 `[`。退出码 0，所以上面那个 if 检测不到。
  #
  # 这会让隔离检查**静默误报**（JSON 解析不出来 → 判定为「资源不存在」），
  # 而一个天天报红的检查等于没有检查。所以在这里统一剥掉 JSON 之前的所有内容。
  # 只认第一个 `[` 或 `{` 起头 —— gcloud 的 --format=json 输出必定以其中之一开始。
  awk 'p{print;next} /^[[{]/{p=1;print}' "${OUT_DIR}/${name}.raw" > "${OUT_DIR}/${name}.json"

  if ! jq -e 'type' "${OUT_DIR}/${name}.json" >/dev/null 2>&1; then
    fail "取到的 ${name} 不是合法 JSON（原始输出见 ${OUT_DIR}/${name}.raw）。
     同样按「取不到 = 判定不了 = 失败」处理。"
    printf '[]' > "${OUT_DIR}/${name}.json"
    return 1
  fi

  rm -f "${OUT_DIR}/${name}.err" "${OUT_DIR}/${name}.raw"
  return 0
}

jqf()  { jq -r "$2" "${OUT_DIR}/${1}.json" 2>/dev/null || true; }
jqfl() { jq -r "$1" "$FLEET_JSON" 2>/dev/null || true; }

# ───────────────────────── 机队清单 ─────────────────────────

load_fleet() {
  [ -f "$FLEET_JSON" ] || die "找不到机队清单：$FLEET_JSON
     vpn 侧的全部期望都来自它（roadmap B69）。它在仓库里（infra/fleet/fleet.json），
     不在就是 checkout 不完整，或者 FLEET_JSON 指错了。"
  jq -e '.nodes and .isolation.expected_firewall_non_bp and .isolation.ssh_chain and .isolation.cross_fleet_deny' \
      "$FLEET_JSON" >/dev/null 2>&1 \
    || die "$FLEET_JSON 不是合法的机队清单（缺 .nodes / .isolation.expected_firewall_non_bp / .isolation.ssh_chain / .isolation.cross_fleet_deny）"

  FLEET_VPN_TAG="$(jqfl '.isolation.vpn_tag // "vpn-node"')"
  FLEET_BP_TAG="$(jqfl '.isolation.bp_tag // "bp-node"')"
  FLEET_VPN_PREFIX="$(jqfl '.isolation.vpn_prefix // "vpn-"')"
  FLEET_BP_PREFIX="$(jqfl '.isolation.bp_prefix // "bp-"')"
  FLEET_SSH_IAP="$(jqfl '.isolation.ssh_chain.iap_allow')"
  FLEET_SSH_DENY="$(jqfl '.isolation.ssh_chain.public_deny')"
  FLEET_SSH_DEFAULT="$(jqfl '.isolation.ssh_chain.default_allow')"
  FLEET_SSH_TAG="$(jqfl '.isolation.ssh_chain.tag')"
  FLEET_XDENY_NAME="$(jqfl '.isolation.cross_fleet_deny.name')"
  FLEET_XDENY_SRC="$(jqfl '.isolation.cross_fleet_deny.source_tag')"
  FLEET_XDENY_DST="$(jqfl '.isolation.cross_fleet_deny.target_tag')"
  FLEET_XDENY_OUTRANK="$(jqfl '.isolation.cross_fleet_deny.must_outrank')"

  log "清单 : $FLEET_JSON（updated=$(jqfl '.updated // "?"')，$(jqfl '.nodes | length') 个节点，$(jqfl '.isolation.expected_firewall_non_bp | length') 条非 bp- 规则）"
}

# ───────────────────────── 第一层：硬期望 ─────────────────────────

check_instances() {
  step "1 · 自用代理节点（fleet.json .nodes；as-built-personal-fleet §2）"
  local i n name status want actual
  n="$(jqfl '.nodes | length')"
  i=0
  while [ "$i" -lt "${n:-0}" ]; do
    name="$(jqfl ".nodes[$i].host")"
    status="$(jqfl ".nodes[$i].status // \"running\"")"
    actual="$(jqf instances "
      [.[]? | select(.name == \"$name\")
       | \"\(.name)|\(.zone // \"?\" | split(\"/\") | last)|\(.machineType // \"?\" | split(\"/\") | last)|\(.networkInterfaces[0].accessConfigs[0].natIP // \"-\")|\(.status // \"?\")|dp=\(.deletionProtection // false | tostring)|sa=\(.serviceAccounts[0].email // \"-\")|tags=\((.tags.items // []) | sort | join(\",\"))\"] | .[0] // \"(不存在)\"")"
    if [ "$status" = "planned" ]; then
      if [ "$actual" = "(不存在)" ]; then
        pass "$name  尚未建（fleet.json 标 planned，符合）"
      else
        fail "$name 在 fleet.json 里标 planned，但 GCP 上已经存在：$actual
     🔴 要么有人绕过流程建了机器，要么建完忘了把 fleet.json 改成 running。两种都要人看。"
      fi
    else
      want="$(jqfl ".nodes[$i] | \"\(.host)|\(.zone)|\(.machine_type)|\(.ip)|RUNNING|dp=\(.deletion_protection // false | tostring)|sa=\(.service_account // \"-\")|tags=\((.tags // []) | sort | join(\",\"))\"")"
      if [ "$actual" = "$want" ]; then
        pass "$name  $actual"
      else
        fail "$name 与 fleet.json 不符
     期望: $want
     实际: $actual
     是现实合理地变了（升机型 / 换 SA / 换 IP）？→ 改 infra/fleet/fleet.json 并提交，不要改脚本。"
      fi
    fi
    i=$((i + 1))
  done
}

check_addresses() {
  step "2 · 保留静态外部 IP（fleet.json .nodes[].address_name）"
  # vpn-us-ip-v4 的 -v4 后缀记录着「美国节点 IP 已被封锁并更换过三次」。
  # 这些地址一旦变化，意味着有人动了现役节点的入口。
  local list want name actual
  list="$(jqfl '.nodes[] | select((.status // "running") != "planned") | select(.address_name != null) | "\(.address_name)|\(.ip)|IN_USE"')"
  while IFS= read -r want; do
    [ -n "$want" ] || continue
    name="${want%%|*}"
    actual="$(jqf addresses "
      [.[]? | select(.name == \"$name\")
       | \"\(.name)|\(.address // \"?\")|\(.status // \"?\")\"] | .[0] // \"(不存在)\"")"
    if [ "$actual" = "$want" ]; then
      pass "$name  $actual"
    else
      fail "$name 与 fleet.json 不符
     期望: $want
     实际: $actual"
    fi
  done <<EOF
$list
EOF
}

check_firewall() {
  local n
  n="$(jqfl '.isolation.expected_firewall_non_bp | length')"
  step "3 · 防火墙规则（fleet.json：${n} 条非 bp- 规则，一条不增不减）"
  local actual expected
  # 只看非 bp- 规则。新增 bp- 规则是允许的（as-built §2.1 第 3 条），不算差异。
  actual="$(jqf firewall "[.[]? | .name | select(startswith(\"$FLEET_BP_PREFIX\") | not)] | sort | join(\" \")")"
  expected="$(jqfl '.isolation.expected_firewall_non_bp | sort | join(" ")')"
  if [ "$actual" = "$expected" ]; then
    pass "${n} 条非 bp- 规则与 fleet.json 完全一致"
  else
    fail "非 bp- 防火墙规则集合发生变化
     期望: $expected
     实际: $actual
     🔴 现有节点靠 ${FLEET_SSH_DENY} 压制 ${FLEET_SSH_DEFAULT}(0.0.0.0/0:22)。
        删掉那条 deny 会让 vpn-* 立刻裸奔 22 端口。
     是现实合理地变了？→ 改 fleet.json 的 expected_firewall_non_bp 并在 _history 里记一笔。"
  fi

  # disabled 状态：清单里点名 disabled 的必须 disabled，其余必须 enabled。
  # allow-*-443 被 disable 的现象与 IP 级封锁完全一样（runbook-node-health §3），
  # 名字集合比对看不出来，所以单独看这个字段。
  local want_dis is_dis rule
  while IFS= read -r rule; do
    [ -n "$rule" ] || continue
    want_dis="$(jqfl "[.isolation.expected_disabled // [] | .[] | select(. == \"$rule\")] | length")"
    is_dis="$(jqf firewall "[.[]? | select(.name == \"$rule\") | ((.disabled // false) | tostring)] | .[0] // \"?\"")"
    if [ "$DRY_RUN" -eq 1 ]; then
      break
    fi
    if [ "${want_dis:-0}" -ge 1 ]; then
      [ "$is_dis" = "true" ] || fail "$rule 应当是 disabled（fleet.json expected_disabled），实际 disabled=$is_dis"
    else
      [ "$is_dis" = "false" ] || fail "$rule 被 disable 了（实际 disabled=$is_dis）。名字还在，作用没了。"
    fi
  done <<EOF
$(jqfl '.isolation.expected_firewall_non_bp[]')
EOF

  check_ssh_posture
}

# check_ssh_posture 核对 SSH 压制链**还在生效**，而不只是「那条规则还叫这个名字」。
#
# 规则可以在名字不变的前提下被 disable、被改 priority、被改 denied 端口 ——
# 三者都不会被上面的名字集合比对或第二层基线 diff 抓到（那两处都不采集这些字段）。
# 本函数是唯一看这三个字段的地方。
#
# 判定用的是**相对顺序**而不是写死数字：GCP 按 priority 升序求值，
# 数字大小本身不重要，重要的是 iap-allow < public-deny < default-allow-ssh。
check_ssh_posture() {
  local deny_disabled deny_prio deny_ports deny_tags iap_prio default_prio

  # ⚠️ 必须先 tostring 再走 `//`：jq 的 `//` 把 **false 也当成空值**，
  # 所以 `[false] | .[0] // "(不存在)"` 会返回 "(不存在)" —— 一条正常启用的规则
  # 会被误判成「读不到」，进而误报暴露。这类误报比漏报更伤：它会训练人忽略这个检查。
  deny_disabled="$(jqf firewall "[.[]? | select(.name == \"$FLEET_SSH_DENY\") | ((.disabled // false) | tostring)] | .[0] // \"(不存在)\"")"
  deny_prio="$(jqf firewall "[.[]? | select(.name == \"$FLEET_SSH_DENY\") | (.priority // 65535)] | .[0] // \"\"")"
  deny_ports="$(jqf firewall "[.[]? | select(.name == \"$FLEET_SSH_DENY\") | ((.denied // []) | map(\"\(.IPProtocol):\((.ports // [\"all\"]) | join(\",\"))\") | sort | join(\" \"))] | .[0] // \"\"")"
  deny_tags="$(jqf firewall "[.[]? | select(.name == \"$FLEET_SSH_DENY\") | ((.targetTags // []) | sort | join(\",\"))] | .[0] // \"\"")"
  iap_prio="$(jqf firewall "[.[]? | select(.name == \"$FLEET_SSH_IAP\") | (.priority // 65535)] | .[0] // \"\"")"
  default_prio="$(jqf firewall "[.[]? | select(.name == \"$FLEET_SSH_DEFAULT\") | (.priority // 65535)] | .[0] // \"\"")"

  # dry-run 下 fetch 写的是空数组，所有字段都取不到 —— 那不是「压制链坏了」，
  # 只是没有数据，所以不能判 fail。
  if [ "$DRY_RUN" -eq 1 ]; then
    warn "[dry-run] 跳过 SSH 压制链姿态核对"
    return 0
  fi

  if [ "$deny_disabled" != "false" ]; then
    fail "$FLEET_SSH_DENY 的 disabled = $deny_disabled（期望 false）
     🔴 被 disable 的 deny 规则名字还在，集合比对与基线 diff 都看不出来，
        但 vpn-* 的 22 端口此刻对 0.0.0.0/0 敞开。"
    return 0
  fi

  case "$deny_ports" in
    *tcp:22*) : ;;
    *)
      fail "$FLEET_SSH_DENY 不再 deny tcp:22（实际: ${deny_ports:-<空>}）
     🔴 同上：名字没变，压制没了。"
      return 0
      ;;
  esac

  case ",$deny_tags," in
    *,"$FLEET_SSH_TAG",*) : ;;
    *)
      fail "$FLEET_SSH_DENY 的 target tag 不含 $FLEET_SSH_TAG（实际: ${deny_tags:-<无>}）
     🔴 没有这个 tag，这条 deny 就落不到 vpn-* 上。"
      return 0
      ;;
  esac

  if [ -z "$deny_prio" ] || [ -z "$default_prio" ] || [ -z "$iap_prio" ]; then
    fail "SSH 相关规则的 priority 读不全（deny=${deny_prio:-?} iap=${iap_prio:-?} default=${default_prio:-?}）"
    return 0
  fi

  # GCP 按 priority 升序求值，先命中先生效。
  if [ "$deny_prio" -ge "$default_prio" ]; then
    fail "$FLEET_SSH_DENY 的 priority=$deny_prio 不再优先于 $FLEET_SSH_DEFAULT 的 $default_prio
     🔴 求值顺序反了：0.0.0.0/0 的 allow 会先命中，deny 永远轮不到。"
    return 0
  fi
  if [ "$iap_prio" -ge "$deny_prio" ]; then
    fail "$FLEET_SSH_IAP 的 priority=$iap_prio 不再优先于 $FLEET_SSH_DENY 的 $deny_prio
     ⚠️ 这个方向的错误不会造成暴露，但会把**自己**也挡在外面：
        IAP 隧道进不去，节点就只能靠串口控制台救。"
    return 0
  fi

  pass "SSH 压制链在位  ${FLEET_SSH_IAP}(${iap_prio}) < ${FLEET_SSH_DENY}(${deny_prio}, ${deny_ports}) < ${FLEET_SSH_DEFAULT}(${default_prio})"
}

# check_cross_fleet · 两支机队不交叉（ADR 0017 §3 的两条扩展 + 一条正向断言）。
#
# 反向断言：bp-* 资源不得带 vpn-node 标签，vpn-* 资源不得带 bp-node 标签，
#           任何一条防火墙规则的 target tag 不得同时命中两支机队。
#           现状 2026-09-04 实查满足，但此前**没有任何东西在守它**。
# 正向断言：vpn-deny-from-bp（bp-node → vpn-node 内网全拒）必须存在、enabled、
#           且优先级压得住 default-allow-internal。它是两支机队之间唯一一条 GCP 强制的隔离。
check_cross_fleet() {
  step "3b · 两支机队不交叉（ADR 0017 §3 反向断言 + ${FLEET_XDENY_NAME} 正向断言）"
  if [ "$DRY_RUN" -eq 1 ]; then
    warn "[dry-run] 跳过交叉核对"
    return 0
  fi
  local bad

  bad="$(jqf instances "[.[]? | select((.name | startswith(\"$FLEET_BP_PREFIX\")) and (((.tags.items // []) | index(\"$FLEET_VPN_TAG\")) != null)) | .name] | join(\" \")")"
  if [ -z "$bad" ]; then
    pass "没有 ${FLEET_BP_PREFIX}* 实例带 ${FLEET_VPN_TAG} 标签"
  else
    fail "${FLEET_BP_PREFIX}* 实例带了 ${FLEET_VPN_TAG} 标签：$bad
     🔴 这台商用节点此刻能命中自用队的全部防火墙规则（含 SSH 压制链之外的所有 allow）。"
  fi

  bad="$(jqf instances "[.[]? | select((.name | startswith(\"$FLEET_VPN_PREFIX\")) and (((.tags.items // []) | index(\"$FLEET_BP_TAG\")) != null)) | .name] | join(\" \")")"
  if [ -z "$bad" ]; then
    pass "没有 ${FLEET_VPN_PREFIX}* 实例带 ${FLEET_BP_TAG} 标签"
  else
    fail "${FLEET_VPN_PREFIX}* 实例带了 ${FLEET_BP_TAG} 标签：$bad
     🔴 付费用户流量的规则此刻落在用户自用机器上（AGENTS.md §4 红线）。"
  fi

  bad="$(jqf firewall "[.[]? | select((((.targetTags // []) | index(\"$FLEET_VPN_TAG\")) != null) and (((.targetTags // []) | index(\"$FLEET_BP_TAG\")) != null)) | .name] | join(\" \")")"
  if [ -z "$bad" ]; then
    pass "没有一条规则的 target tag 同时命中两支机队"
  else
    fail "这些规则同时绑了 ${FLEET_VPN_TAG} 与 ${FLEET_BP_TAG}：$bad"
  fi

  bad="$(jqf firewall "[.[]? | select((.name | startswith(\"$FLEET_BP_PREFIX\")) and (((.targetTags // []) | index(\"$FLEET_VPN_TAG\")) != null)) | .name] | join(\" \")")"
  if [ -z "$bad" ]; then
    pass "没有 ${FLEET_BP_PREFIX}* 规则指向 ${FLEET_VPN_TAG}"
  else
    fail "${FLEET_BP_PREFIX}* 规则指向了 ${FLEET_VPN_TAG}：$bad"
  fi

  bad="$(jqf firewall "[.[]? | select((.name | startswith(\"$FLEET_VPN_PREFIX\")) and (((.targetTags // []) | index(\"$FLEET_BP_TAG\")) != null)) | .name] | join(\" \")")"
  if [ -z "$bad" ]; then
    pass "没有 ${FLEET_VPN_PREFIX}* 规则指向 ${FLEET_BP_TAG}"
  else
    fail "${FLEET_VPN_PREFIX}* 规则指向了 ${FLEET_BP_TAG}：$bad"
  fi

  # 正向：vpn-deny-from-bp
  local x_disabled x_prio x_src x_dst x_proto o_prio
  x_disabled="$(jqf firewall "[.[]? | select(.name == \"$FLEET_XDENY_NAME\") | ((.disabled // false) | tostring)] | .[0] // \"(不存在)\"")"
  x_prio="$(jqf firewall "[.[]? | select(.name == \"$FLEET_XDENY_NAME\") | (.priority // 65535)] | .[0] // \"\"")"
  x_src="$(jqf firewall "[.[]? | select(.name == \"$FLEET_XDENY_NAME\") | ((.sourceTags // []) | sort | join(\",\"))] | .[0] // \"\"")"
  x_dst="$(jqf firewall "[.[]? | select(.name == \"$FLEET_XDENY_NAME\") | ((.targetTags // []) | sort | join(\",\"))] | .[0] // \"\"")"
  x_proto="$(jqf firewall "[.[]? | select(.name == \"$FLEET_XDENY_NAME\") | ((.denied // []) | map(.IPProtocol) | sort | join(\",\"))] | .[0] // \"\"")"
  o_prio="$(jqf firewall "[.[]? | select(.name == \"$FLEET_XDENY_OUTRANK\") | (.priority // 65535)] | .[0] // \"65534\"")"

  if [ "$x_disabled" = "(不存在)" ]; then
    fail "${FLEET_XDENY_NAME} 不存在。
     🔴 它是 bp-node → vpn-node 内网横向的唯一一道闸（default-allow-internal 放通 10.128.0.0/9 全部端口）。"
    return 0
  fi
  if [ "$x_disabled" != "false" ]; then
    fail "${FLEET_XDENY_NAME} 被 disable 了（disabled=$x_disabled）。"
    return 0
  fi
  case ",$x_src," in *,"$FLEET_XDENY_SRC",*) : ;; *) fail "${FLEET_XDENY_NAME} 的 source tag 不含 ${FLEET_XDENY_SRC}（实际 ${x_src:-<无>}）"; return 0 ;; esac
  case ",$x_dst," in *,"$FLEET_XDENY_DST",*) : ;; *) fail "${FLEET_XDENY_NAME} 的 target tag 不含 ${FLEET_XDENY_DST}（实际 ${x_dst:-<无>}）"; return 0 ;; esac
  case ",$x_proto," in *,all,*) : ;; *) fail "${FLEET_XDENY_NAME} 不再 deny all（实际 ${x_proto:-<无>}）"; return 0 ;; esac
  if [ -z "$x_prio" ] || [ "$x_prio" -ge "${o_prio:-65534}" ]; then
    fail "${FLEET_XDENY_NAME} 的 priority=${x_prio:-?} 压不住 ${FLEET_XDENY_OUTRANK}(${o_prio})。
     🔴 求值顺序反了：内网 allow 先命中，deny 永远轮不到。"
    return 0
  fi
  pass "${FLEET_XDENY_NAME} 在位  ${FLEET_XDENY_SRC} → ${FLEET_XDENY_DST} DENY ${x_proto}，priority ${x_prio} < ${FLEET_XDENY_OUTRANK}(${o_prio})"
}

check_run_services() {
  step "4 · 现有 Cloud Run 服务（as-built §4）"
  local want name url actual
  for want in "${EXPECT_RUN[@]}"; do
    name="${want%%|*}"
    url="${want#*|}"
    actual="$(jqf run "
      [.[]? | select((.metadata.name // .name) == \"$name\")
       | (.status.url // \"?\")] | .[0] // \"(不存在)\"")"
    if [ "$actual" = "$url" ]; then
      pass "$name  $actual"
    else
      fail "$name 的 URL 与 as-built 不符
     期望: $url
     实际: $actual"
    fi
  done
  # 「lastDeployed 不变」这一条硬期望做不到：as-built §4 只记了日期（2026-07-02 等），
  # 而 gcloud 里对应的字段路径 **待核实**。它由第二层的基线 diff 覆盖 ——
  # isolation.txt 里记了 latestReadyRevisionName，重新部署一定会让它变。
}

check_secrets() {
  step "5 · 现有 Secret（as-built §5）"
  local name found
  for name in "${EXPECT_SECRETS[@]}"; do
    found="$(jqf secrets "[.[]? | (.name // \"\" | split(\"/\") | last) | select(. == \"$name\")] | length")"
    if [ "${found:-0}" -ge 1 ]; then
      pass "$name 存在"
    else
      fail "$name 不存在或读不到。
     🔴 它属于现有服务。babel.plus 从不需要碰它 ——
        逐 secret 授权（deploy.md §1 第 2 条）的全部意义就是让 bp-api-sa 连看都看不到它。"
    fi
  done
}

check_artifacts() {
  step "6 · Artifact Registry（as-built §4）"
  local found
  found="$(jqf artifacts "[.[]? | (.name // \"\" | split(\"/\") | last) | select(. == \"$EXPECT_AR_REPO\")] | length")"
  if [ "${found:-0}" -ge 1 ]; then
    pass "$EXPECT_AR_REPO 存在"
  else
    fail "$EXPECT_AR_REPO 不存在。它是现有三个服务的镜像所在地，删了等于让它们无法重新部署。"
  fi
  if [ "$DRY_RUN" -eq 0 ]; then
    local count
    # 同 fetch()：gcloud 可能把噪音写进 stdout 且仍 exit 0，所以这里也要剥掉 JSON 前缀。
    count="$(gcloud artifacts docker images list \
      "${REGION}-docker.pkg.dev/${PROJECT_ID}/${EXPECT_AR_REPO}" \
      --project="$PROJECT_ID" --format=json 2>/dev/null \
      | awk 'p{print;next} /^[[{]/{p=1;print}' \
      | jq 'length' 2>/dev/null || printf '')"
    if [ -n "$count" ]; then
      printf '%s\n' "$count" > "${OUT_DIR}/ar-image-count.txt"
      log "  · $EXPECT_AR_REPO 当前镜像数 = $count（判定见「镜像数不减少」一节）"
    else
      warn "取不到 $EXPECT_AR_REPO 的镜像数（权限或 API 变化）。「镜像数不减少」这一条本次**未验证**。"
    fi
  fi
}

check_service_accounts() {
  step "7 · 现有服务账号（as-built §5）"
  local email found
  for email in "${EXPECT_SA[@]}"; do
    found="$(jqf sa "[.[]? | select(.email == \"$email\")] | length")"
    if [ "${found:-0}" -ge 1 ]; then
      pass "$email"
    else
      fail "$email 不存在或读不到"
    fi
  done
  # 反向检查：bp-api 绝不能跑在 Compute 默认 SA 上。这一条不在 as-built 的清单里，
  # 但它是「爆炸半径不接到 lisa-* 上」的直接体现（as-built §5 / deploy.md §3.3）。
  if [ "$DRY_RUN" -eq 0 ]; then
    local bp_sa
    bp_sa="$(gcloud run services describe bp-api --project="$PROJECT_ID" --region="$REGION" \
      --format='value(spec.template.spec.serviceAccountName)' 2>/dev/null || printf '')"
    if [ -z "$bp_sa" ]; then
      log "  · bp-api 尚未部署，跳过「运行时身份不是 Compute 默认 SA」检查"
    elif [ "$bp_sa" = "bp-api-sa@${PROJECT_ID}.iam.gserviceaccount.com" ]; then
      pass "bp-api 跑在 bp-api-sa 上"
    else
      fail "bp-api 的运行时身份是 $bp_sa，不是 bp-api-sa@${PROJECT_ID}.iam.gserviceaccount.com
     🔴 用 Compute 默认 SA 跑 bp-api 等于把 babel.plus 的爆炸半径接到现有工作负载上。"
    fi
  fi
}

# ───────────────────────── 第二层：基线 diff ─────────────────────────

write_isolation_txt() {
  local f="${OUT_DIR}/isolation.txt"
  {
    printf '# babel.plus 隔离基线（**只含非 bp- 资源**，故可以直接 diff）\n'
    printf '# 项目: %s\n' "$PROJECT_ID"
    printf '# 来源: infra/scripts/verify-isolation.sh\n'
    printf '# 刻意不含时间戳 —— 带时间戳的文件没法 diff。\n'
    printf '\n'
  } > "$f"

  {
    jqf instances '[.[]? | select(.name | startswith("bp-") | not)
      | "instance \(.name) zone=\(.zone // "?" | split("/") | last) machine=\(.machineType // "?" | split("/") | last) ip=\(.networkInterfaces[0].accessConfigs[0].natIP // "-") status=\(.status // "?") sa=\(.serviceAccounts[0].email // "-") tags=\((.tags.items // ["-"]) | sort | join(","))"] | sort | .[]'
    jqf addresses '[.[]? | select(.name | startswith("bp-") | not)
      | "address \(.name) addr=\(.address // "?") status=\(.status // "?")"] | sort | .[]'
    jqf firewall '[.[]? | select(.name | startswith("bp-") | not)
      | "firewall \(.name) prio=\(.priority // "?") disabled=\(.disabled // false) dir=\(.direction // "?") src=\((.sourceRanges // []) | sort | join(",")) srctags=\((.sourceTags // ["-"]) | sort | join(",")) tags=\((.targetTags // ["-"]) | sort | join(",")) allow=\((.allowed // []) | map("\(.IPProtocol):\((.ports // ["all"]) | join(","))") | sort | join(" ")) deny=\((.denied // []) | map("\(.IPProtocol):\((.ports // ["all"]) | join(","))") | sort | join(" "))"] | sort | .[]'
    jqf run '[.[]? | select((.metadata.name // .name // "") | startswith("bp-") | not)
      | "run \(.metadata.name // .name // "?") url=\(.status.url // "?") rev=\(.status.latestReadyRevisionName // "?")"] | sort | .[]'
    jqf secrets '[.[]? | (.name // "" | split("/") | last) | select(startswith("bp-") | not)
      | "secret \(.)"] | sort | .[]'
    # shellcheck disable=SC2016  # $n 是 jq 的变量，不是 shell 的
    jqf artifacts '[.[]? | (.name // "" | split("/") | last) as $n | select($n | startswith("bp-") | not)
      | "artifact \($n) format=\(.format // "?")"] | sort | .[]'
    jqf sa '[.[]? | select((.email // "") | startswith("bp-") | not)
      | "sa \(.email // "?")"] | sort | .[]'
  } >> "$f"

  # secret 的版本数：as-built §2.1 要求「版本数不变」，但 as-built 没记下基线数字，
  # 所以它只能靠 before/after 比对，不能写死。逐个查，只查非 bp- 的那两个。
  if [ "$DRY_RUN" -eq 0 ]; then
    local name n
    for name in "${EXPECT_SECRETS[@]}"; do
      n="$(gcloud secrets versions list "$name" --project="$PROJECT_ID" --format=json 2>/dev/null \
           | jq 'length' 2>/dev/null || printf '?')"
      printf 'secret-versions %s count=%s\n' "$name" "$n" >> "$f"
    done
  fi
}

compare_baseline() {
  step "8 · 与基线逐字节比对：$BASELINE"
  local base="${BASELINE%/}/isolation.txt"
  if [ ! -f "$base" ]; then
    fail "基线文件不存在：$base
     先在部署前跑一次 ./infra/scripts/verify-isolation.sh --out=$BASELINE"
    return 0
  fi
  if diff -u "$base" "${OUT_DIR}/isolation.txt"; then
    pass "非 bp- 资源与基线逐字节相同"
  else
    fail "🔴 **现有资源被改动了。** 上面就是 diff。立即停止部署并排查。
     判定口径提醒：新增 bp- 资源不会出现在这份文件里（按构造排除），
     所以任何一行差异都意味着**非本项目的资源发生了变化**。"
  fi

  # 镜像数只允许不减少（as-built §2.1）：现有服务重新部署会让它增加，那不是我们的事。
  local before_n after_n
  before_n="$(cat "${BASELINE%/}/ar-image-count.txt" 2>/dev/null || printf '')"
  after_n="$(cat "${OUT_DIR}/ar-image-count.txt" 2>/dev/null || printf '')"
  if [ -n "$before_n" ] && [ -n "$after_n" ]; then
    if [ "$after_n" -lt "$before_n" ]; then
      fail "$EXPECT_AR_REPO 的镜像数从 $before_n 降到 $after_n。
     🔴 那是 anthropic-relay / lisa-cloud / lisa-web 的镜像仓库。
        babel.plus 的镜像在 bp-images 里，我们没有任何理由让这个数字变小。"
    else
      pass "$EXPECT_AR_REPO 镜像数 $before_n → $after_n（未减少）"
    fi
  else
    warn "缺少镜像数记录，「镜像数不减少」这一条本次未比对"
  fi
}

# ───────────────────────── 主流程 ─────────────────────────

main() {
  local arg
  for arg in "$@"; do
    case "$arg" in
      --baseline=*) BASELINE="${arg#*=}" ;;
      --out=*)      OUT_DIR="${arg#*=}" ;;
      --keep)       KEEP_OUT=1 ;;
      --project=*)  PROJECT_ID="${arg#*=}" ;;
      --dry-run)    DRY_RUN=1 ;;
      -h|--help)    usage; exit 0 ;;
      *)            usage >&2; die "未知参数：$arg" ;;
    esac
  done

  guard_project
  need_cmd gcloud
  need_cmd jq
  load_fleet

  if [ -z "$OUT_DIR" ]; then
    TMP_DIR="$(mktemp -d "${TMPDIR:-/tmp}/bp-isolation.XXXXXX")"
    OUT_DIR="$TMP_DIR"
  else
    KEEP_OUT=1
  fi
  mkdir -p "$OUT_DIR"
  trap cleanup EXIT

  log "项目 : $PROJECT_ID"
  log "输出 : $OUT_DIR"
  if [ -n "$BASELINE" ]; then
    log "基线 : $BASELINE"
  fi
  if [ "$DRY_RUN" -eq 1 ]; then
    log "模式 : DRY-RUN（连只读调用都不发，判定结果无意义）"
  fi

  # 只读抓取。任何一次失败都记 FAIL —— 「查不到」不等于「没问题」。
  fetch instances gcloud compute instances list      --project="$PROJECT_ID" || true
  fetch addresses gcloud compute addresses list      --project="$PROJECT_ID" || true
  fetch firewall  gcloud compute firewall-rules list --project="$PROJECT_ID" || true
  fetch run       gcloud run services list           --project="$PROJECT_ID" --region="$REGION" || true
  fetch secrets   gcloud secrets list                --project="$PROJECT_ID" || true
  fetch artifacts gcloud artifacts repositories list --project="$PROJECT_ID" || true
  fetch sa        gcloud iam service-accounts list   --project="$PROJECT_ID" || true

  check_instances
  check_addresses
  check_firewall
  check_cross_fleet
  check_run_services
  check_secrets
  check_artifacts
  check_service_accounts

  write_isolation_txt
  if [ -n "$BASELINE" ]; then
    compare_baseline
  else
    step "8 · 基线 diff"
    log "  · 未给 --baseline=<目录>，只跑了硬期望。"
    log "    部署前请先留一份：./infra/scripts/verify-isolation.sh --out=snapshots/before"
  fi

  step "结果"
  log "  通过 $PASS_N 项 / 失败 $FAIL_N 项"
  log "  本次观测：${OUT_DIR}/isolation.txt"
  if [ "$FAIL_N" -ne 0 ]; then
    log ""
    log "  🔴 有差异。**停止部署。**"
    log "     人工复核清单（as-built §2.1 + ADR 0017 §3，一条都不能少）："
    log "       · fleet.json 里每台 running 节点的可用区、机型、外网 IP、状态、删除保护、服务账号、标签"
    log "       · 每台 planned 节点此刻不存在"
    log "       · 每个保留 IP 均 IN_USE 且地址不变"
    log "       · 非 bp- 防火墙规则与 fleet.json 清单一条不增不减，disabled 状态相符"
    log "       · SSH 压制链与 vpn-deny-from-bp 在位；两支机队的标签与规则不交叉"
    log "       · anthropic-relay / lisa-cloud / lisa-web 的 URL 与最后部署时间"
    log "       · anthropic-api-key / relay-token 存在且版本数不变"
    log "       · cloud-run-source-deploy 存在且镜像数不减少"
    log "     现实合理地变了？→ 改 infra/fleet/fleet.json 并提交，不要改脚本、不要改宽松。"
    exit 1
  fi
  log "  ✅ 现有资产未受影响，两支机队未交叉。"
  exit 0
}

main "$@"

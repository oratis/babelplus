#!/usr/bin/env bash
# =============================================================================
# create-vpn-node.sh · 建自用机队（vpn-*）节点的 GCP 侧资源
#
#   防火墙核对 → 服务账号核对 → 静态 IP 预筛 → 创建实例 → 建机即刻验收
#
# 事实源：docs/04-ops/personal-fleet-runbook.md §1.2–§1.4（本脚本是 §1.4 的可执行形式）
#         docs/05-adr/0017-personal-fleet-in-repo.md §1（两支机队的边界表）、§3（隔离怎么被强制）
#         docs/02-architecture/as-built-personal-fleet.md §3（自用队防火墙现状，2026-09-04 实查）
#         infra/fleet/README.md §1（两支机队不要混）、§5 代价第 1 条（为什么复制而不抽库）
#         infra/node/create-node.sh（商用队的同类脚本；本脚本复制它的守卫，改掉它的前缀）
#
# -----------------------------------------------------------------------------
# 🔴 为什么是一个单独的文件，而不是给 create-node.sh 加几个参数
# -----------------------------------------------------------------------------
# create-node.sh 把 bp- 前缀、bp-node 标签、STANDARD 层级**硬编码**进了脚本，
# 而且那三处硬编码各有各的理由（隔离承诺 / ADR 0007 §6 / ADR 0008 §5.5 明令不给开关）。
# 给它加 --fleet=vpn 这种开关，等于把「打错一个字就把付费用户的流量落到自用机器上」
# 这件事从「不可能」变成「一个参数的距离」—— 而两支机队同在 oratis-491316 一个 project、
# 同一个 VPC，隔离靠的只有命名前缀、网络标签和一个只读脚本，没有任何一层是 GCP 强制的
# （infra/fleet/README.md §1，2026-09-04）。
#
# 所以 ADR 0017 的裁决是「同仓不同队」，infra/fleet/README.md §5 代价第 1 条据此选择了
# **复制守卫逻辑而不是抽公共库**：理由与 infra/node/README.md §8 代价第 2 条相同
# （setup-*.sh 从 stdin 灌进 sudo bash -s，没有兄弟文件可以 source，必须自包含），
# 代价是**改守卫逻辑要改两个目录，而它们会悄悄分叉**。本脚本就是那份复制品。
#
# -----------------------------------------------------------------------------
# 🔴 隔离规则（ADR 0017 §1 表 + §3，2026-09-04）
# -----------------------------------------------------------------------------
#   自用队：vpn-* 前缀 / vpn-node 标签 / vpn-iap-ssh-allow(500) < vpn-public-ssh-deny(600)
#   商用队：bp-*  前缀 / bp-node  标签 / bp-iap-ssh-allow(900)  < bp-public-ssh-deny(1000)
#
#   本脚本只在 vpn-* 命名空间里**新建**资源；对 bp-* 一个字都不碰，对既有的 vpn-us / vpn-jp
#   及其地址、既有的每一条防火墙规则、三个 Cloud Run 服务一律拒绝（assert_target_safe）。
#   既有规则只做**核对**，不 update（本脚本没有 --fix-firewall；属性不符就打印手工命令并退出）。
#   服务账号 vpn-node-sa 只做**核对**，不 create、不改 IAM。
#
# -----------------------------------------------------------------------------
# 与 create-node.sh 的差异清单（改守卫逻辑时两边都要看这张表）
# -----------------------------------------------------------------------------
#   · --network-tier PREMIUM|STANDARD **必填、无默认值**。自用队要跑 A/B
#     （vpn-sg 用 Standard 与 vpn-jp 的 Premium 构成同期对照，ADR 0017 §4.3、runbook §1.1），
#     而 bp 那边是 ADR 0008 §5.5 明令硬编码 STANDARD 的。这里没有"安全默认值"可选：
#     默认 PREMIUM 会静默产生 2.09 倍账单，默认 STANDARD 会静默毁掉对照 —— 所以必须显式说。
#   · --node / --region / --zone 同样**无默认值**：自用队的节点分布在四个区域，任何默认值
#     都只对其中一台成立。
#   · 没有 --ipv6。（i）它要求修改共享 default 子网的 stack-type，而那个子网是两支机队共用的，
#     改它属于 ADR 0017 §3 的边界之外；（ii）参数名在本项目从未实测（node-provisioning §3.6
#     标「待核实」）；（iii）现役两台自用机都是 IPv4-only，fleet.json 也没有 IPv6 字段。
#     要开 IPv6 是另一次裁决，不是本脚本的一个开关。
#   · 没有 --fix-firewall。四条 vpn-* 规则 2026-09-05 已全部存在，本脚本只核对。
#   · 没有 --no-proxy-ports（runbook §1.4 给 vpn-ops 用的那个）。443/48882 入向由
#     vpn-node 标签决定（三条 allow 规则都绑这个标签），一台带标签的机器**必然**开这些端口；
#     真要关，需要第二个标签（例如 vpn-ops-node）连同它自己的 SSH 压制链和
#     verify-isolation.sh 的反向断言 —— 那是改隔离边界，不是加参数。裁决前用本脚本建的
#     vpn-ops 会在防火墙层开着 443/48882（主机上没进程监听，仅是暴露面）。
#   · SS 端口：VPN_SS_PORT=48882 **允许**（allow-ss-48882 本来就绑 vpn-node，只核对不新建），
#     与 bp 那边"不得为 48882"正好相反 —— 因为那条规则本来就是自用队的。
#   · 机型默认 e2-small；盘 pd-standard 30GB（与 vpn-us / vpn-jp 一致，2026-09-05 实查）；
#     SA 挂 vpn-node-sa（只有 logging.logWriter + monitoring.metricWriter），
#     scopes 只给 logging.write + monitoring.write，不给 cloud-platform；
#     标签 owner=personal,fleet=vpn,role=proxy。
#   · 网段先验（rank_ip）只在 asia-east2 有依据（ADR 0004 §3.5 的两条社区来源）；
#     其他区域一律等权，选第一个，靠实测。
#   · 证据目录默认在仓库内 docs/evidence/fleet-node-provision-<node>-<日期>/，
#     CI 的 check-evidence-index.sh 会要求它有 README.md 且进索引表。
#
# -----------------------------------------------------------------------------
# 代价
# -----------------------------------------------------------------------------
#   1. 这是 create-node.sh 的复制品，约 80% 的行是一样的。守卫逻辑改一处必须改两处，
#      而没有任何 CI 在比对两份。分叉是必然的，只能靠这张差异清单让分叉被看见。
#   2. 与 create-node.sh 一样，这是「脚本化的手敲」，不是 IaC：没有状态、不可 diff。
#      fw_ensure 比 bp 版多核对了 disabled / 来源 / 端口，但仍然只是「这一刻长这样」。
#   3. --dry-run 需要有效的 gcloud 凭据，权限不足时会静默降级成「查不到 → 当作不存在」。
#
# 用法见 --help；一切改动都支持 --dry-run。
# =============================================================================
set -euo pipefail

# ---------------------------------------------------------------------------
# 0 · 默认值（node / region / zone / network-tier 故意没有默认值）
# ---------------------------------------------------------------------------
PROJECT="${VPN_PROJECT:-oratis-491316}"
NODE="${VPN_NODE:-}"
REGION="${VPN_REGION:-}"
ZONE="${VPN_ZONE:-}"
MACHINE_TYPE="${VPN_MACHINE_TYPE:-e2-small}"
BOOT_DISK_SIZE="${VPN_BOOT_DISK_SIZE:-30GB}"     # 与 vpn-us / vpn-jp 一致（2026-09-05 实查 pd-standard 30）
BOOT_DISK_TYPE="pd-standard"
IP_CANDIDATES="${VPN_IP_CANDIDATES:-5}"
NODE_TAG="vpn-node"
SA_NAME="vpn-node-sa"
SA_EMAIL="${SA_NAME}@${PROJECT}.iam.gserviceaccount.com"
SA_EXPECT_ROLES="roles/logging.logWriter roles/monitoring.metricWriter"

# 网络层级。**必填，只从命令行给**，不设环境变量：环境变量会让「忘了设」有一个静默的落点。
NETWORK_TIER=""

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"

DRY_RUN=0
ASSUME_YES=0
AUTO_PICK=0
ONLY_STAGES=""
PICK_ADDRESS=""
EVIDENCE_DIR=""

# ---------------------------------------------------------------------------
# 1 · 输出与守卫
# ---------------------------------------------------------------------------
log()  { printf '%s\n' "$*"; }
step() { printf '\n=== %s\n' "$*"; }
ok()   { printf '  [ok]   %s\n' "$*"; }
warn() { printf '  [warn] %s\n' "$*" >&2; }
die()  { printf '\n[FATAL] %s\n' "$*" >&2; exit 1; }

# 绝不允许本脚本以「新建目标」的身份触碰的既有资源：
#   · 现役自用节点与它们的静态 IP（personal-vpn 指令，2026-09-04）
#   · 三个 Cloud Run 服务（as-built-gcp §4）
#   · 2026-09-05 实查的全部 17 条防火墙规则（含当日新建的 vpn-allow-* 与 vpn-deny-from-bp）
PROTECTED_NAMES="vpn-us vpn-jp vpn-us-ip-v4 vpn-jp-ip \
anthropic-relay lisa-cloud lisa-web \
vpn-iap-ssh-allow vpn-public-ssh-deny vpn-deny-from-bp \
vpn-allow-reality-443 vpn-allow-hy2-udp443 \
bp-iap-ssh-allow bp-public-ssh-deny bp-allow-reality-443 bp-allow-hy2-udp443 \
allow-hysteria-udp443 allow-xray-443 allow-ss-48882 allow-iap-ssh \
default-allow-icmp default-allow-internal default-allow-rdp default-allow-ssh"

# assert_target_safe · 目标必须是 vpn- 前缀的新资源，且不在保护名单里，且不是 bp-*。
# 这一条挡的是「参数敲错一个字就把现役节点改掉」或「把自用资源建进商用队的命名空间」。
assert_target_safe() {
    _ats_name="$1"
    for _ats_p in $PROTECTED_NAMES; do
        if [ "$_ats_name" = "$_ats_p" ]; then
            die "拒绝操作受保护资源 '$_ats_name'。它属于现役系统（vpn-us / vpn-jp 及其地址 /
      Cloud Run / 既有防火墙规则），本仓库的操作红线是「不修改、不删除、不改名」。"
        fi
    done
    case "$_ats_name" in
        bp-*|bp_*)
            die "拒绝操作 '$_ats_name'：bp-* 是商用队（babel.plus）的命名空间，本脚本一律不碰。
      ADR 0017 §1：两支机队共享设计与工具，不共享任何一份 GCP 资源。" ;;
        vpn-*) : ;;
        *)
            die "节点名 '$_ats_name' 不是 vpn- 前缀。ADR 0017 §1 的边界表要求自用队资源一律 vpn- 前缀，
      verify-isolation.sh 与 fleet.json 都按这个前缀识别机队。" ;;
    esac
}

usage() {
    cat <<'USAGE'
用法：create-vpn-node.sh --node NAME --region R --zone Z --network-tier PREMIUM|STANDARD [选项]

  建自用机队（vpn-*）节点的 GCP 侧资源。是 infra/node/create-node.sh 的 vpn- 变体：
  同一套守卫，不同的前缀与标签集，且**必须显式选择网络层级**（自用队要跑 A/B）。
  **绝不触碰 vpn-us / vpn-jp、任何 bp-* 资源、既有防火墙规则与三个 Cloud Run 服务。**

必填
  --node NAME           节点名，必须 vpn- 前缀且不是 vpn-us / vpn-jp（或 $VPN_NODE）
  --region REGION       例 asia-southeast1（或 $VPN_REGION）
  --zone ZONE           例 asia-southeast1-a。🔴 拒绝 -b（或 $VPN_ZONE）
  --network-tier TIER   PREMIUM | STANDARD。无默认值 —— 见脚本头部"差异清单"

选项
  --project ID          GCP 项目（默认 oratis-491316，或 $VPN_PROJECT）
  --machine-type TYPE   默认 e2-small（或 $VPN_MACHINE_TYPE；runbook §1.1 给 vpn-sg 的是 e2-medium）
  --candidates N        预留几个候选静态 IP 做网段预筛（默认 5）
  --address NAME        跳过预筛，直接用这个已存在的保留地址名（层级必须与 --network-tier 一致）
  --auto-pick           按网段启发式自动选一个候选（不加则交互式选）
  --only STAGE          只跑某一段，可重复：firewall|sa|address|instance|verify
  --evidence-dir DIR    清点快照落点（默认 <仓库根>/docs/evidence/fleet-node-provision-<node>-<date>/）
  --dry-run             只打印将要执行的变更命令，不做任何改动（只读查询照常执行）
  --yes                 跳过交互确认（CI 用；危险操作仍会打印全部细节）
  -h, --help            本帮助

环境变量
  VPN_SS_PORT           设了才核对/建 SS-2022 的防火墙规则。
                        48882 **允许**（allow-ss-48882 本来就绑 vpn-node，只核对不新建）；
                        其他端口建 vpn-allow-ss-<port>。

顺序（不要跳步，理由见 docs/04-ops/personal-fleet-runbook.md §1 与 infra/node/README.md §7）
  firewall → sa → address → instance → verify

例（runbook §1.4）
  ./create-vpn-node.sh --node vpn-sg --region asia-southeast1 --zone asia-southeast1-a \
      --machine-type e2-medium --network-tier STANDARD --dry-run
USAGE
}

# ---------------------------------------------------------------------------
# 2 · dry-run 包装
# ---------------------------------------------------------------------------
# run · 有副作用的命令走这里；dry-run 下只打印。
# 只读查询（describe / list / get-iam-policy）**不**走 run —— dry-run 下照常执行，
# 这样预演也能看到真实状态。
run() {
    if [ "$DRY_RUN" = 1 ]; then
        _run_out=""
        for _run_a in "$@"; do
            _run_out="$_run_out $(printf '%q' "$_run_a")"
        done
        printf '  [dry-run]%s\n' "$_run_out"
        return 0
    fi
    "$@"
}

# confirm_typed · 危险操作的二次确认：必须原样敲出目标名字，回车不算数。
confirm_typed() {
    _ct_expect="$1"
    _ct_what="$2"
    if [ "$ASSUME_YES" = 1 ]; then
        warn "--yes 已跳过确认：$_ct_what"
        return 0
    fi
    if [ ! -t 0 ]; then
        die "需要交互确认但 stdin 不是终端：$_ct_what（非交互场景请显式加 --yes）"
    fi
    printf '\n  ⚠️  %s\n  确认请原样输入 %s ：' "$_ct_what" "$_ct_expect"
    _ct_ans=""
    read -r _ct_ans || true
    [ "$_ct_ans" = "$_ct_expect" ] || die "确认串不匹配，已中止。"
}

want_stage() {
    [ -z "$ONLY_STAGES" ] && return 0
    case " $ONLY_STAGES " in *" $1 "*) return 0 ;; *) return 1 ;; esac
}

# ---------------------------------------------------------------------------
# 3 · 参数解析
# ---------------------------------------------------------------------------
while [ $# -gt 0 ]; do
    case "$1" in
        --project)      PROJECT="${2:?--project 需要值}"; shift 2 ;;
        --node)         NODE="${2:?--node 需要值}"; shift 2 ;;
        --region)       REGION="${2:?--region 需要值}"; shift 2 ;;
        --zone)         ZONE="${2:?--zone 需要值}"; shift 2 ;;
        --machine-type) MACHINE_TYPE="${2:?--machine-type 需要值}"; shift 2 ;;
        --network-tier) NETWORK_TIER="${2:?--network-tier 需要值}"; shift 2 ;;
        --candidates)   IP_CANDIDATES="${2:?--candidates 需要值}"; shift 2 ;;
        --address)      PICK_ADDRESS="${2:?--address 需要值}"; shift 2 ;;
        --evidence-dir) EVIDENCE_DIR="${2:?--evidence-dir 需要值}"; shift 2 ;;
        --only)         ONLY_STAGES="$ONLY_STAGES ${2:?--only 需要值}"; shift 2 ;;
        --auto-pick)    AUTO_PICK=1; shift ;;
        --dry-run)      DRY_RUN=1; shift ;;
        --yes|-y)       ASSUME_YES=1; shift ;;
        -h|--help)      usage; exit 0 ;;
        *)              usage >&2; die "未知参数：$1" ;;
    esac
done

[ -n "$NODE" ]   || { usage >&2; die "--node 是必填项（自用队没有「默认那一台」）。"; }
[ -n "$REGION" ] || { usage >&2; die "--region 是必填项。"; }
[ -n "$ZONE" ]   || { usage >&2; die "--zone 是必填项。"; }

assert_target_safe "$NODE"

# SA 邮箱跟着 --project 走（默认值在解析前就拼好了，这里重拼一次）。
SA_EMAIL="${SA_NAME}@${PROJECT}.iam.gserviceaccount.com"

# 网络层级：必填、只认两个值。理由见脚本头部"差异清单"第一条 —— 这里没有安全默认值。
NETWORK_TIER="$(printf '%s' "$NETWORK_TIER" | tr '[:lower:]' '[:upper:]')"
case "$NETWORK_TIER" in
    PREMIUM|STANDARD) : ;;
    '')
        die "--network-tier 是必填项，取值 PREMIUM 或 STANDARD。
      自用队要跑层级 A/B（ADR 0017 §4.3：vpn-sg 用 Standard 对照 vpn-jp 的 Premium），
      所以这里不给默认值：默认 PREMIUM 会静默产生 2.09 倍出网账单（ADR 0008 §5.5），
      默认 STANDARD 会静默毁掉对照。你必须说出来。" ;;
    *)
        die "--network-tier 的值 '$NETWORK_TIER' 不合法，只能是 PREMIUM 或 STANDARD。" ;;
esac

# --only 的取值必须真实存在。打错一个字就「什么都没跑却退出码 0」是最坏的失败形式。
ALL_STAGES="firewall sa address instance verify"
for _os in $ONLY_STAGES; do
    case " $ALL_STAGES " in
        *" $_os "*) : ;;
        *) die "--only 的值 '$_os' 不是有效阶段。可选：$ALL_STAGES" ;;
    esac
done

IPNAME="${NODE}-ip"
[ -z "$EVIDENCE_DIR" ] && EVIDENCE_DIR="${REPO_ROOT}/docs/evidence/fleet-node-provision-${NODE}-$(date +%Y%m%d)"

# ---------------------------------------------------------------------------
# 4 · 前置检查
# ---------------------------------------------------------------------------
preflight() {
    step "前置检查"
    command -v gcloud >/dev/null 2>&1 || die "找不到 gcloud。"

    case "$ZONE" in
        *-b)
            die "zone '$ZONE' 以 -b 结尾。ADR 0004 §3.5：asia-east2-b 社区实测绕道美国（2019，待核实），
      本项目已裁定避开；runbook §1.4 把这条守卫原样套用到自用队。请用 -a 或 -c。" ;;
        "$REGION"-*) : ;;
        *) die "zone '$ZONE' 与 region '$REGION' 不匹配。" ;;
    esac

    if [ -n "${VPN_SS_PORT:-}" ]; then
        case "$VPN_SS_PORT" in
            ''|*[!0-9]*) die "VPN_SS_PORT 必须是纯数字，当前值不合法。" ;;
        esac
        if [ "$VPN_SS_PORT" -lt 1024 ] || [ "$VPN_SS_PORT" -gt 65535 ]; then
            die "VPN_SS_PORT 超出 1024-65535。"
        fi
        if [ "$VPN_SS_PORT" = "48882" ]; then
            ok "SS-2022 端口 48882 —— 既有 allow-ss-48882 已绑 vpn-node，只核对不新建"
        else
            ok "SS-2022 端口 ${VPN_SS_PORT} → 将核对/建 vpn-allow-ss-${VPN_SS_PORT}"
        fi
    else
        log "  VPN_SS_PORT 未设置 → 不建 SS 防火墙规则（allow-ss-48882 对 vpn-node 本来就生效）"
    fi

    _pf_acct="$(gcloud config get-value account 2>/dev/null || true)"
    log "  gcloud 身份：${_pf_acct:-<未登录>}"
    log "  项目 / 区域 / 可用区：$PROJECT / $REGION / $ZONE"
    log "  节点 / 标签 / 机型：$NODE / $NODE_TAG / $MACHINE_TYPE"
    log "  盘：$BOOT_DISK_TYPE $BOOT_DISK_SIZE（与 vpn-us / vpn-jp 一致）"
    log "  服务账号：$SA_EMAIL（只核对，不建、不改 IAM）"
    log "  🔶 网络层级：$NETWORK_TIER（显式选择；自用队在跑 A/B，见 ADR 0017 §4.3）"
    case "$NETWORK_TIER" in
        PREMIUM)  log "     Premium 到中国 \$0.23/GiB 且无免费额度（evidence/gcp-egress-pricing-20260817）" ;;
        STANDARD) log "     Standard \$0.11/GiB，每区每月前 200 GiB 免费；无 IPv6（API 硬约束）" ;;
    esac
    log "  证据目录：$EVIDENCE_DIR"
    [ "$DRY_RUN" = 1 ] && warn "DRY-RUN：不会做任何改动"
    return 0
}

# snapshot · as-built §7 的清点命令。变更前后各跑一次做 diff，
# 这是「不影响已部署服务」这条约束的唯一可验证形式（ADR 0007 §9.2；ADR 0017 §3 同理）。
snapshot() {
    _sn_when="$1"
    _sn_out="${EVIDENCE_DIR}/inventory-${_sn_when}.txt"
    step "清点快照（${_sn_when}）→ ${_sn_out}"
    if [ "$DRY_RUN" = 1 ]; then
        log "  [dry-run] 跳过快照落盘（清点本身是只读的，但写文件属于改动）"
        return 0
    fi
    mkdir -p "$EVIDENCE_DIR"
    {
        printf '# as-built §7 清点 · %s · %s\n\n' "$_sn_when" "$(date -u '+%Y-%m-%dT%H:%M:%SZ')"
        for _sn_cmd in "compute instances list" "compute addresses list" \
                       "compute firewall-rules list" "run services list" \
                       "secrets list" "iam service-accounts list"; do
            printf '\n$ gcloud %s --project=%s\n' "$_sn_cmd" "$PROJECT"
            # shellcheck disable=SC2086  # 故意按空格拆成子命令；每个 _sn_cmd 都是上面写死的字面量
            gcloud $_sn_cmd --project="$PROJECT" 2>&1 || printf '(命令失败)\n'
        done
    } >"$_sn_out"
    ok "已写入 $_sn_out"
    log "  变更后请自行 diff：diff -u ${EVIDENCE_DIR}/inventory-before.txt ${EVIDENCE_DIR}/inventory-after.txt"
}

# ---------------------------------------------------------------------------
# 5 · 防火墙
# ---------------------------------------------------------------------------
fw_field() {
    gcloud compute firewall-rules describe "$1" --project="$PROJECT" \
        --format="value($2)" 2>/dev/null || true
}

# fw_json · 一次 describe 拿整条规则的 JSON；**读不到就 die**，不静默变成空串。
# 2026-09-05 第一次真实执行时 fw_field 对 vpn-public-ssh-deny 的 denied 读回空串（同一条命令手工跑是 tcp:22），
# 脚本据此判「属性不符」拒绝建机 —— 一个读不到的字段被当成了「字段为空」。这是错误的默认方向：
# 建机脚本对现役规则的核对，「查不到」必须等于「不能继续」，不能等于「不符」，更不能等于「符合」。
fw_json() {
    _fj_out="$(gcloud compute firewall-rules describe "$1" --project="$PROJECT" --format=json 2>&1)" \
        || die "读不到防火墙规则 $1（gcloud describe 失败，可能是网络）：$(printf '%s' "$_fj_out" | tail -c 300)"
    printf '%s' "$_fj_out" | awk 'p{print;next} /^[[{]/{p=1;print}'
}
fw_get() { printf '%s' "$1" | jq -r "$2"; }
# 规则串与 gcloud 的 firewall_rule() 投影同格式：proto:p1,p2；无端口时只有 proto（如 all）
FW_RULES_ALLOW='[.allowed[]? | if ((.ports // []) | length) > 0 then "\(.IPProtocol):\(.ports | join(","))" else .IPProtocol end] | join(",")'
FW_RULES_DENY='[.denied[]? | if ((.ports // []) | length) > 0 then "\(.IPProtocol):\(.ports | join(","))" else .IPProtocol end] | join(",")'

fw_exists() {
    gcloud compute firewall-rules describe "$1" --project="$PROJECT" \
        --format="value(name)" >/dev/null 2>&1
}

# audit_untagged · 显式点名那些「无 target tag、会作用到新 VM」的既有规则。
# 我们不修改它们（那属于「影响已部署服务」），只把风险写在屏幕上。
audit_untagged() {
    step "审计：default 网络里无 target tag 的规则（它们会作用到新 VM）"
    log "  规则名                    优先级  作用"
    for _au_r in default-allow-ssh allow-xray-443 allow-hysteria-udp443 allow-iap-ssh default-allow-rdp; do
        _au_p="$(fw_field "$_au_r" priority)"
        _au_t="$(fw_field "$_au_r" "targetTags.list()")"
        if [ -z "$_au_p" ]; then
            log "  ${_au_r}: 不存在（或无读权限）"
        elif [ -z "$_au_t" ]; then
            log "  ${_au_r}  ${_au_p}  **无 tag → 对本网络所有实例生效**"
        else
            log "  ${_au_r}  ${_au_p}  tag=${_au_t}"
        fi
    done
    log ""
    log "  🔴 as-built-personal-fleet §3.2：现役 vpn-us / vpn-jp 的 443 入向**就挂在**无 tag 的"
    log "     allow-xray-443 / allow-hysteria-udp443 上。vpn-allow-reality-443 / vpn-allow-hy2-udp443"
    log "     （2026-09-05 建）是让自用队对将来那次「补 target tag 收敛」免疫的冗余路径。"
    log "     22 端口靠 vpn-public-ssh-deny(600) 压制 default-allow-ssh(65534)，新 VM 必须带 $NODE_TAG。"
}

# fw_ensure · 幂等核对/建规则。
#   $1 名字  $2 优先级  $3 action(ALLOW|DENY)  $4 rules  $5 source-ranges  $6 描述
#   $7 缺失策略：create（缺了就建，纯增量）| protected（缺了就 die —— 那是现役节点的姿态漂移，
#      属于事故响应，不是建机脚本该顺手修的）
# 已存在则核对 priority / disabled / target tag / 来源 / 动作与端口；任何一项不符 → 打印手工命令并 die。
# **永远不 update 既有规则**（与 create-node.sh 的 --fix-firewall 不同）。
fw_ensure() {
    _fe_name="$1"; _fe_prio="$2"; _fe_action="$3"
    _fe_rules="$4"; _fe_src="$5"; _fe_desc="$6"; _fe_policy="${7:-create}"

    if fw_exists "$_fe_name"; then
        _fe_json="$(fw_json "$_fe_name")"
        _fe_have_prio="$(fw_get "$_fe_json" '.priority // ""')"
        _fe_have_tags="$(fw_get "$_fe_json" '(.targetTags // []) | join(",")')"
        _fe_have_dis="$(fw_get "$_fe_json" '(.disabled // false) | tostring')"
        _fe_have_src="$(fw_get "$_fe_json" '(.sourceRanges // []) | join(",")')"
        case "$_fe_action" in
            ALLOW) _fe_have_rules="$(fw_get "$_fe_json" "$FW_RULES_ALLOW")" ;;
            DENY)  _fe_have_rules="$(fw_get "$_fe_json" "$FW_RULES_DENY")" ;;
            *)     die "fw_ensure：action 只能是 ALLOW 或 DENY（收到 '$_fe_action'）" ;;
        esac

        _fe_bad=""
        [ "$_fe_have_prio" = "$_fe_prio" ]   || _fe_bad="$_fe_bad priority=${_fe_have_prio}(期望 ${_fe_prio})"
        [ "$_fe_have_dis" = "false" ]        || _fe_bad="$_fe_bad disabled=${_fe_have_dis:-?}(期望 false)"
        printf '%s' "$_fe_have_tags" | grep -q "$NODE_TAG" \
            || _fe_bad="$_fe_bad targetTags=${_fe_have_tags:-<无>}(期望含 ${NODE_TAG})"
        [ "$_fe_have_src" = "$_fe_src" ]     || _fe_bad="$_fe_bad sourceRanges=${_fe_have_src:-<无>}(期望 ${_fe_src})"
        [ "$_fe_have_rules" = "$_fe_rules" ] || _fe_bad="$_fe_bad ${_fe_action}=${_fe_have_rules:-<无>}(期望 ${_fe_rules})"

        if [ -z "$_fe_bad" ]; then
            ok "$_fe_name 已存在且属性相符（priority=$_fe_have_prio $_fe_action=$_fe_have_rules src=$_fe_have_src tags=$_fe_have_tags）"
            return 0
        fi
        die "$_fe_name 已存在但属性不符：${_fe_bad}
      本脚本**不会 update 既有规则**（它可能正在为 vpn-us / vpn-jp 服务）。
      先弄清是现实变了还是有人改坏了；确认要改再手工执行：
        gcloud compute firewall-rules update $_fe_name --project=$PROJECT \\
          --priority=$_fe_prio --target-tags=$NODE_TAG --source-ranges=$_fe_src --rules=$_fe_rules --no-disabled"
    fi

    if [ "$_fe_policy" = "protected" ]; then
        die "$_fe_name 不存在。它是现役自用节点在依赖的规则（2026-09-05 实查存在），
      缺了意味着 vpn-us / vpn-jp 的姿态已经漂移 —— 那是事故，按 runbook-node-health 处理，
      不由建机脚本顺手补。确认要重建再手工执行：
        gcloud compute firewall-rules create $_fe_name --project=$PROJECT \\
          --network=default --direction=INGRESS --priority=$_fe_prio --action=$_fe_action \\
          --rules=$_fe_rules --source-ranges=$_fe_src --target-tags=$NODE_TAG"
    fi

    run gcloud compute firewall-rules create "$_fe_name" --project="$PROJECT" \
        --network=default --direction=INGRESS --priority="$_fe_prio" \
        --action="$_fe_action" --rules="$_fe_rules" --source-ranges="$_fe_src" \
        --target-tags="$NODE_TAG" \
        --description="$_fe_desc"
    ok "已建 $_fe_name（priority=$_fe_prio, target-tags=$NODE_TAG）"
}

stage_firewall() {
    step "阶段 firewall · 核对 vpn-* 规则（2026-09-05 起四条全部已存在；只核对，不 update）"
    audit_untagged

    # ①② SSH 压制链 —— 现役 vpn-us / vpn-jp 正在依赖，缺了就是事故（protected）。
    fw_ensure vpn-iap-ssh-allow 500 ALLOW tcp:22 35.235.240.0/20 \
        "IAP TCP forwarding only. MUST outrank vpn-public-ssh-deny." protected
    fw_ensure vpn-public-ssh-deny 600 DENY tcp:22 0.0.0.0/0 \
        "Suppress default-allow-ssh for vpn nodes." protected
    # ③④ 明知冗余仍要有：切断对两条无 tag 规则的隐式依赖（runbook §1.2，纯增量）。
    fw_ensure vpn-allow-reality-443 1000 ALLOW tcp:443 0.0.0.0/0 \
        "REALITY / VLESS-XTLS-Vision. Redundant with untagged allow-xray-443 on purpose." create
    fw_ensure vpn-allow-hy2-udp443 1000 ALLOW udp:443 0.0.0.0/0 \
        "Hysteria2 / QUIC. Redundant with untagged allow-hysteria-udp443 on purpose." create

    if [ -n "${VPN_SS_PORT:-}" ]; then
        if [ "$VPN_SS_PORT" = "48882" ]; then
            # 既有规则本来就绑 vpn-node；只核对，不新建（新建一条同名前缀的会是纯噪音）。
            fw_ensure allow-ss-48882 1000 ALLOW "tcp:48882,udp:48882" 0.0.0.0/0 \
                "(existing) SS-2022 for vpn nodes." protected
        else
            fw_ensure "vpn-allow-ss-${VPN_SS_PORT}" 1000 ALLOW \
                "tcp:${VPN_SS_PORT},udp:${VPN_SS_PORT}" 0.0.0.0/0 \
                "SS-2022 on a non-default port for vpn nodes." create
        fi
    fi
}

# assert_ssh_posture · 🔴 建实例之前的硬闸。不通过就拒绝建机。
# 核的是 ADR 0017 §1 表里自用队那条链，外加跨机队的 vpn-deny-from-bp。
assert_ssh_posture() {
    step "🔴 硬闸：SSH 姿态 + 跨机队隔离核对（不通过则拒绝创建实例）"
    if [ "$DRY_RUN" = 1 ] && ! fw_exists vpn-public-ssh-deny; then
        warn "dry-run 且规则尚不存在，跳过硬闸核对（真实执行时这里会拦住）"
        return 0
    fi

    _ap_deny_prio="$(fw_field vpn-public-ssh-deny priority)"
    _ap_deny_tags="$(fw_field vpn-public-ssh-deny "targetTags.list()")"
    _ap_iap_prio="$(fw_field vpn-iap-ssh-allow priority)"
    _ap_iap_tags="$(fw_field vpn-iap-ssh-allow "targetTags.list()")"
    _ap_def_prio="$(fw_field default-allow-ssh priority)"

    [ -n "$_ap_deny_prio" ] || die "vpn-public-ssh-deny 不存在。没有它，新 VM 的 22 端口会被
      无 target tag 的 default-allow-ssh 对 0.0.0.0/0 放通。先跑 --only firewall。"
    [ -n "$_ap_iap_prio" ] || die "vpn-iap-ssh-allow 不存在。只有 deny 没有 allow，
      IAP 隧道会被自己的 deny 挡住，节点将成为无法登录的砖。"

    printf '%s' "$_ap_deny_tags" | grep -q "$NODE_TAG" \
        || die "vpn-public-ssh-deny 的 target tags 是 '${_ap_deny_tags:-<无>}'，不含 $NODE_TAG。
      规则不绑 tag 或绑错 tag = 规则白写。"
    printf '%s' "$_ap_iap_tags" | grep -q "$NODE_TAG" \
        || die "vpn-iap-ssh-allow 的 target tags 是 '${_ap_iap_tags:-<无>}'，不含 $NODE_TAG。"

    # 不等式 1：deny 必须比 default-allow-ssh 优先（数字更小）。
    if [ -n "$_ap_def_prio" ]; then
        [ "$_ap_deny_prio" -lt "$_ap_def_prio" ] \
            || die "不等式 1 不成立：vpn-public-ssh-deny($_ap_deny_prio) 必须 <
      default-allow-ssh($_ap_def_prio)。数字越小优先级越高，否则公网 22 依然放通。"
        ok "不等式 1：deny($_ap_deny_prio) < default-allow-ssh($_ap_def_prio)"
    else
        warn "查不到 default-allow-ssh（可能已被删或无读权限）。风险变小，但仍按 65534 核对。"
        [ "$_ap_deny_prio" -lt 65534 ] || die "不等式 1 不成立（按默认 65534 核对）。"
    fi

    # 不等式 2：IAP allow 必须比 deny 优先，否则节点变砖。
    [ "$_ap_iap_prio" -lt "$_ap_deny_prio" ] \
        || die "不等式 2 不成立：vpn-iap-ssh-allow($_ap_iap_prio) 必须 <
      vpn-public-ssh-deny($_ap_deny_prio)。否则 IAP 隧道也被挡，GCE 没有能救 SSH 的带外 console。"
    ok "不等式 2：iap-allow($_ap_iap_prio) < deny($_ap_deny_prio)"
    ok "SSH 姿态核对通过 —— 新 VM 打上 $NODE_TAG 后，22 端口只对 IAP 网段开放"

    # 跨机队隔离：vpn-deny-from-bp（700，DENY all，source-tags=bp-node → target-tags=vpn-node）。
    # default-allow-internal(65534) 放通 10.128.0.0/9 全端口 —— 同一个 VPC 里 bp 节点默认能
    # 直连自用节点的内网口。这条 deny 是 ADR 0017 §3「不共享流量」在 VPC 内的唯一执行点，
    # 它必须存在、启用、且优先级数字 < default-allow-internal。
    fw_exists vpn-deny-from-bp || die "vpn-deny-from-bp 不存在（2026-09-05 实查存在）。没有它，bp 节点经
      default-allow-internal 能直连本节点的内网口 —— 违反 ADR 0017 §3。这是姿态漂移，先查原因。"
    _ap_x_json="$(fw_json vpn-deny-from-bp)"
    _ap_x_prio="$(fw_get "$_ap_x_json" '.priority // ""')"
    _ap_x_dis="$(fw_get "$_ap_x_json" '(.disabled // false) | tostring')"
    _ap_x_stags="$(fw_get "$_ap_x_json" '(.sourceTags // []) | join(",")')"
    _ap_x_ttags="$(fw_get "$_ap_x_json" '(.targetTags // []) | join(",")')"
    _ap_x_denied="$(fw_get "$_ap_x_json" "$FW_RULES_DENY")"
    _ap_int_prio="$(fw_field default-allow-internal priority)"
    [ "$_ap_x_dis" = "false" ] || die "vpn-deny-from-bp 处于 disabled=${_ap_x_dis:-?}，规则形同不存在。"
    printf '%s' "$_ap_x_stags" | grep -q "bp-node" \
        || die "vpn-deny-from-bp 的 source tags 是 '${_ap_x_stags:-<无>}'，不含 bp-node。"
    printf '%s' "$_ap_x_ttags" | grep -q "$NODE_TAG" \
        || die "vpn-deny-from-bp 的 target tags 是 '${_ap_x_ttags:-<无>}'，不含 $NODE_TAG。"
    [ "$_ap_x_prio" -lt "${_ap_int_prio:-65534}" ] \
        || die "不等式 3 不成立：vpn-deny-from-bp($_ap_x_prio) 必须 < default-allow-internal(${_ap_int_prio:-65534})。"
    ok "不等式 3：deny-from-bp($_ap_x_prio) < default-allow-internal(${_ap_int_prio:-65534})"
    if [ "$_ap_x_denied" = "all" ]; then
        ok "跨机队隔离：bp-node → $NODE_TAG 全协议 DENY（vpn-deny-from-bp）"
    else
        warn "vpn-deny-from-bp 拒绝的是 '${_ap_x_denied:-<空>}' 而不是 all —— 隔离不完整，请人工核实。"
    fi
}

# ---------------------------------------------------------------------------
# 6 · 服务账号（只核对，不建、不改 IAM）
# ---------------------------------------------------------------------------
stage_sa() {
    step "阶段 sa · 核对 ${SA_NAME}（应恰好持有 logWriter + metricWriter 两个角色）"
    if ! gcloud iam service-accounts describe "$SA_EMAIL" --project="$PROJECT" \
            --format="value(email)" >/dev/null 2>&1; then
        die "$SA_EMAIL 不存在（2026-09-05 实查存在）。本脚本**不建 SA、不改 IAM**。
      要重建请手工执行，然后再跑本阶段：
        gcloud iam service-accounts create $SA_NAME --project=$PROJECT \\
          --display-name='personal fleet node runtime'
        gcloud projects add-iam-policy-binding $PROJECT \\
          --member=serviceAccount:$SA_EMAIL --role=roles/logging.logWriter
        gcloud projects add-iam-policy-binding $PROJECT \\
          --member=serviceAccount:$SA_EMAIL --role=roles/monitoring.metricWriter"
    fi
    ok "$SA_EMAIL 存在"
    _sa_dis="$(gcloud iam service-accounts describe "$SA_EMAIL" --project="$PROJECT" \
        --format="value(disabled)" 2>/dev/null || true)"
    [ "$_sa_dis" = "True" ] && warn "$SA_NAME 处于 disabled 状态，挂上去的实例拿不到任何凭据。"

    # 核实角色集合**恰好**是那两个。默认 Compute SA 在多数项目带 Editor ——
    # 一台被攻陷的节点若持有它，可以删掉 vpn-us / vpn-jp 与三个 Cloud Run 服务。
    # 多一个角色是暴露面，少一个角色是日志/指标发不出去 —— 两种都只报不改。
    _sa_roles="$(gcloud projects get-iam-policy "$PROJECT" \
        --flatten="bindings[].members" \
        --filter="bindings.members:serviceAccount:${SA_EMAIL}" \
        --format="value(bindings.role)" 2>/dev/null || true)"
    _sa_extra=""; _sa_missing=""
    for _sa_r in $_sa_roles; do
        case " $SA_EXPECT_ROLES " in
            *" $_sa_r "*) : ;;
            *) _sa_extra="$_sa_extra $_sa_r" ;;
        esac
    done
    for _sa_r in $SA_EXPECT_ROLES; do
        case " $(printf '%s' "$_sa_roles" | tr '\n' ' ') " in
            *" $_sa_r "*) : ;;
            *) _sa_missing="$_sa_missing $_sa_r" ;;
        esac
    done
    if [ -z "$_sa_extra" ] && [ -z "$_sa_missing" ]; then
        ok "${SA_NAME} 的项目角色恰好是：$SA_EXPECT_ROLES"
    else
        [ -n "$_sa_extra" ]   && warn "${SA_NAME} 持有设计之外的角色（请人工核实，本脚本不改 IAM）：$_sa_extra"
        [ -n "$_sa_missing" ] && warn "${SA_NAME} 缺少设计要求的角色（日志/指标会发不出去）：$_sa_missing"
    fi
    return 0
}

# ---------------------------------------------------------------------------
# 7 · 静态 IP：预留候选 + 网段预筛
# ---------------------------------------------------------------------------
# rank_ip · 网段启发式打分，数字越小越优先。
# 🔴 依据是两条 2019/2022 年的社区单一来源（ADR 0004 §3.5，**待核实**），而且它们说的
#    **只是 asia-east2**：35.220.x → 移动直连约 50 ms；34.92.x → 移动经东京绕行约 110 ms。
#    自用队的节点不在香港（新加坡 / 爱荷华 / 俄勒冈 / 东京），那两条先验对它们**没有任何依据**，
#    所以其他区域一律等权（都是 40）—— 候选按序号选第一个，不假装知道哪个网段更好。
# **这只是先验，不是验收。** 真正的判定靠三网路由实测（infra/node/verify-route.sh 的方法）。
rank_ip() {
    if [ "$REGION" != "asia-east2" ]; then
        echo 40
        return 0
    fi
    case "$1" in
        35.220.*) echo 0  ;;
        34.92.*)  echo 90 ;;
        35.*)     echo 20 ;;
        34.*)     echo 50 ;;
        *)        echo 40 ;;
    esac
}

addr_value() {
    gcloud compute addresses describe "$1" --project="$PROJECT" --region="$REGION" \
        --format="value(address)" 2>/dev/null || true
}

addr_tier() {
    gcloud compute addresses describe "$1" --project="$PROJECT" --region="$REGION" \
        --format="value(networkTier)" 2>/dev/null || true
}

# assert_addr_tier · 保留地址的层级必须与 --network-tier 一致。层级跟着地址走，
# 一个 PREMIUM 地址挂到「想要 STANDARD」的实例上，结果就是账单 2.09 倍且脚本全绿。
assert_addr_tier() {
    _aat_t="$(addr_tier "$1")"
    [ -n "$_aat_t" ] || return 0     # 不存在（dry-run 尚未建）→ 交给调用方处理
    [ "$_aat_t" = "$NETWORK_TIER" ] \
        || die "保留地址 $1 的层级是 $_aat_t，与 --network-tier $NETWORK_TIER 不一致。
      层级不能原地改；要么换 --network-tier，要么删掉这个地址重新预留（删除不可逆，先想清楚）。"
}

stage_address() {
    step "阶段 address · 预留 ${IP_CANDIDATES} 个候选静态 IP（$NETWORK_TIER）并做网段预筛"
    if [ -n "$PICK_ADDRESS" ]; then
        assert_target_safe "$PICK_ADDRESS"
        _sa_ip="$(addr_value "$PICK_ADDRESS")"
        [ -n "$_sa_ip" ] || [ "$DRY_RUN" = 1 ] || die "保留地址 $PICK_ADDRESS 不存在。"
        assert_addr_tier "$PICK_ADDRESS"
        ok "使用指定地址 $PICK_ADDRESS (${_sa_ip:-<dry-run>}, $NETWORK_TIER)"
        SELECTED_ADDRESS="$PICK_ADDRESS"
        return 0
    fi

    log "  ⚠️ 闲置保留地址是计费的（2024-02-01 起 GCP 对全部外部 IPv4 计费，"
    log "     闲置约 \$0.010/hr、在用约 \$0.005/hr，**待核实**；evidence/fleet-pricing-20260904 §4"
    log "     指出目录里有两条候选 SKU 且差 3–4 倍）。用完立刻删多余的。"
    if [ "$REGION" != "asia-east2" ]; then
        log "  ⚠️ $REGION 没有网段先验（rank_ip 只对 asia-east2 有依据），候选等权、选第一个。"
    fi

    _sa_i=1
    while [ "$_sa_i" -le "$IP_CANDIDATES" ]; do
        _sa_n="${IPNAME}-cand${_sa_i}"
        if [ -n "$(addr_value "$_sa_n")" ]; then
            assert_addr_tier "$_sa_n"
            ok "$_sa_n 已存在，复用"
        else
            run gcloud compute addresses create "$_sa_n" --project="$PROJECT" \
                --region="$REGION" --network-tier="$NETWORK_TIER"
        fi
        _sa_i=$((_sa_i + 1))
    done

    log ""
    log "  候选地址（rank 越小越优先，仅先验）："
    _sa_best=""; _sa_best_rank=999; _sa_best_ip=""
    _sa_i=1
    while [ "$_sa_i" -le "$IP_CANDIDATES" ]; do
        _sa_n="${IPNAME}-cand${_sa_i}"
        _sa_ip="$(addr_value "$_sa_n")"
        if [ -z "$_sa_ip" ]; then
            log "    $_sa_n  <dry-run，尚未创建>"
        else
            _sa_r="$(rank_ip "$_sa_ip")"
            log "    $_sa_n  $_sa_ip  rank=$_sa_r"
            if [ "$_sa_r" -lt "$_sa_best_rank" ]; then
                _sa_best_rank="$_sa_r"; _sa_best="$_sa_n"; _sa_best_ip="$_sa_ip"
            fi
        fi
        _sa_i=$((_sa_i + 1))
    done

    if [ -z "$_sa_best" ]; then
        warn "没有可选候选（dry-run 或创建失败）。后续用 --address 显式指定。"
        SELECTED_ADDRESS=""
        return 0
    fi

    if [ "$AUTO_PICK" = 1 ]; then
        SELECTED_ADDRESS="$_sa_best"
        ok "自动选中 $SELECTED_ADDRESS ($_sa_best_ip, rank=$_sa_best_rank)"
    elif [ "$ASSUME_YES" = 1 ] || [ ! -t 0 ]; then
        SELECTED_ADDRESS="$_sa_best"
        warn "非交互模式，按启发式选中 $SELECTED_ADDRESS ($_sa_best_ip)"
    else
        printf '\n  输入要保留的候选名（回车 = %s）：' "$_sa_best"
        _sa_ans=""
        read -r _sa_ans || true
        SELECTED_ADDRESS="${_sa_ans:-$_sa_best}"
        [ -n "$(addr_value "$SELECTED_ADDRESS")" ] || die "$SELECTED_ADDRESS 不是有效的保留地址。"
    fi

    # 释放其余候选。释放是单向门 —— GCP 不保证能拿回同一个地址，所以要二次确认。
    _sa_drop=""
    _sa_i=1
    while [ "$_sa_i" -le "$IP_CANDIDATES" ]; do
        _sa_n="${IPNAME}-cand${_sa_i}"
        if [ "$_sa_n" != "$SELECTED_ADDRESS" ] && [ -n "$(addr_value "$_sa_n")" ]; then
            _sa_drop="$_sa_drop $_sa_n"
        fi
        _sa_i=$((_sa_i + 1))
    done
    if [ -n "$_sa_drop" ]; then
        confirm_typed "$NODE" "将释放这些未选中的候选地址（不可逆）：$_sa_drop"
        for _sa_n in $_sa_drop; do
            assert_target_safe "$_sa_n"
            run gcloud compute addresses delete "$_sa_n" --project="$PROJECT" \
                --region="$REGION" --quiet
        done
    fi
}

# ---------------------------------------------------------------------------
# 8 · 创建实例
# ---------------------------------------------------------------------------
stage_instance() {
    step "阶段 instance · 创建 $NODE"
    assert_ssh_posture     # 🔴 硬闸在这里，绝不能挪到建机之后

    if gcloud compute instances describe "$NODE" --project="$PROJECT" --zone="$ZONE" \
            --format="value(name)" >/dev/null 2>&1; then
        ok "$NODE 已存在，跳过创建"
        return 0
    fi

    _si_ip=""
    if [ -n "${SELECTED_ADDRESS:-}" ]; then
        assert_addr_tier "$SELECTED_ADDRESS"
        _si_ip="$(addr_value "$SELECTED_ADDRESS")"
    fi
    if [ -z "$_si_ip" ] && [ "$DRY_RUN" != 1 ]; then
        die "没有可用的静态 IP。先跑 --only address，或用 --address <name> 指定。"
    fi

    log "  ⚠️ 开 OS Login 之前必须先把权限授给自己，顺序反了就得从 Console 救："
    log "     gcloud projects add-iam-policy-binding $PROJECT \\"
    log "       --member=\"user:<你的邮箱>\" --role=\"roles/compute.osAdminLogin\""
    log "     gcloud projects add-iam-policy-binding $PROJECT \\"
    log "       --member=\"user:<你的邮箱>\" --role=\"roles/iap.tunnelResourceAccessor\""

    confirm_typed "$NODE" "将创建实例 $NODE（$MACHINE_TYPE / $ZONE / $NETWORK_TIER / IP ${_si_ip:-<dry-run>}）"

    set -- \
        compute instances create "$NODE" \
        --project="$PROJECT" --zone="$ZONE" \
        --machine-type="$MACHINE_TYPE" \
        --image-family=debian-12 --image-project=debian-cloud \
        --boot-disk-size="$BOOT_DISK_SIZE" --boot-disk-type="$BOOT_DISK_TYPE" \
        --boot-disk-device-name="$NODE" \
        --network=default --network-tier="$NETWORK_TIER" \
        --tags="$NODE_TAG" \
        --service-account="$SA_EMAIL" \
        --scopes=https://www.googleapis.com/auth/logging.write,https://www.googleapis.com/auth/monitoring.write \
        --shielded-secure-boot --shielded-vtpm --shielded-integrity-monitoring \
        --metadata=block-project-ssh-keys=TRUE,enable-oslogin=TRUE \
        --labels=owner=personal,fleet=vpn,role=proxy \
        --deletion-protection
    # ↑ --scopes 只给 logging.write + monitoring.write，与 SA 的两个角色一一对应：
    #   scopes 是权限上界，角色是权限来源，两边都收窄，任何一边被人放宽另一边仍兜着。
    #   不用 --metadata-from-file=startup-script=：startup script 以 root 每次开机运行，
    #   且凭据必须落进 metadata，而 metadata 对项目内任何有读权限的主体可见。
    #   装机走 infra/fleet/setup-vpn-node.sh 的 SSH + stdin。
    [ -n "$_si_ip" ] && set -- "$@" --address="$_si_ip"
    run gcloud "$@"
    ok "实例已创建"
}

# ---------------------------------------------------------------------------
# 9 · 建机即刻验收
# ---------------------------------------------------------------------------
stage_verify() {
    step "阶段 verify · 建机即刻验收（装机之前就要过）"
    if ! gcloud compute instances describe "$NODE" --project="$PROJECT" --zone="$ZONE" \
            --format="value(name)" >/dev/null 2>&1; then
        if [ "$DRY_RUN" = 1 ]; then
            warn "实例尚不存在（dry-run），跳过验收。真实执行后再跑：--only verify"
            return 0
        fi
        die "实例 $NODE 不存在，无法验收。"
    fi

    log "  ① 实例网络标签（含 $NODE_TAG，且**不含** bp-node —— ADR 0017 §3 反向断言）"
    _sv_tags="$(gcloud compute instances describe "$NODE" --project="$PROJECT" \
        --zone="$ZONE" --format="value(tags.items)" 2>/dev/null || true)"
    if printf '%s' "$_sv_tags" | grep -q "$NODE_TAG"; then
        ok "tags = ${_sv_tags}（含 $NODE_TAG）"
    else
        warn "tags = '${_sv_tags:-<空>}' —— 🔴 不含 $NODE_TAG！22 端口正在裸奔。"
        warn "  立即修：gcloud compute instances add-tags $NODE --project=$PROJECT \\"
        warn "            --zone=$ZONE --tags=$NODE_TAG"
    fi
    if printf '%s' "$_sv_tags" | grep -q "bp-node"; then
        warn "🔴 tags 含 bp-node —— 自用节点带了商用队标签，付费用户的规则会命中这台机器。"
        warn "  立即修：gcloud compute instances remove-tags $NODE --project=$PROJECT \\"
        warn "            --zone=$ZONE --tags=bp-node"
    else
        ok "不含 bp-node"
    fi

    log "  ②′ 网络层级必须是你选的 $NETWORK_TIER（这里是选择，不是 ADR 0008 的硬约束）"
    # 为什么还要读回来：层级跟着保留地址走，复用了一个早先建的另一层级地址（--address 传进来的
    # 那种）、有人手工改过 access-config、gcloud 改了默认值 —— 三种情况的共同症状都是
    # 「脚本全绿、层级不是你以为的那个」。对 A/B 来说，这等于对照组静默变成了同一组。
    _sv_tier="$(gcloud compute instances describe "$NODE" --project="$PROJECT" --zone="$ZONE" \
        --format="value(networkInterfaces[0].accessConfigs[0].networkTier)" 2>/dev/null || true)"
    if [ "$_sv_tier" = "$NETWORK_TIER" ]; then
        ok "networkTier = $_sv_tier"
    else
        warn "networkTier = '${_sv_tier:-<空>}'，期望 $NETWORK_TIER —— 🔴 A/B 对照已失真，账单口径也不是你以为的。"
        warn "  Premium 到中国 \$0.23/GiB 无免费额度；Standard \$0.11/GiB 且每区每月前 200 GiB 免费，"
        warn "  差价 2.09 倍（evidence/gcp-egress-pricing-20260817）。"
        warn "  处置：层级不能原地改，必须换一个 $NETWORK_TIER 的保留地址再切 access-config。"
        warn "  把实际层级如实写进 fleet.json 的 network_tier，不要写你想要的那个。"
    fi

    log "  ②-b 服务账号"
    _sv_sa="$(gcloud compute instances describe "$NODE" --project="$PROJECT" --zone="$ZONE" \
        --format="value(serviceAccounts[0].email)" 2>/dev/null || true)"
    if [ "$_sv_sa" = "$SA_EMAIL" ]; then
        ok "serviceAccount = $_sv_sa"
    else
        warn "serviceAccount = '${_sv_sa:-<空>}'，期望 $SA_EMAIL（默认 Compute SA 在多数项目带 Editor）"
    fi

    log "  ② 生效中的防火墙（以 GCP 侧为准，不要只信本地测试）"
    _sv_eff="$(gcloud compute instances network-interfaces get-effective-firewalls "$NODE" \
        --project="$PROJECT" --zone="$ZONE" --format=json 2>/dev/null || true)"
    for _sv_r in vpn-iap-ssh-allow vpn-public-ssh-deny vpn-allow-reality-443 vpn-allow-hy2-udp443 \
                 allow-ss-48882 vpn-deny-from-bp; do
        if printf '%s' "$_sv_eff" | grep -q "\"$_sv_r\""; then
            ok "  生效：$_sv_r"
        else
            warn "  未在生效列表中看到：$_sv_r"
        fi
    done
    for _sv_r in bp-iap-ssh-allow bp-public-ssh-deny bp-allow-reality-443 bp-allow-hy2-udp443; do
        if printf '%s' "$_sv_eff" | grep -q "\"$_sv_r\""; then
            warn "  🔴 商用队的规则在本节点上生效：$_sv_r —— 标签串了，回到 ① 修。"
        fi
    done

    log "  ③ IAP SSH 应当可通"
    log "     gcloud compute ssh $NODE --project=$PROJECT --zone=$ZONE \\"
    log "       --tunnel-through-iap --command='echo IAP-OK'"

    _sv_ip="$(gcloud compute instances describe "$NODE" --project="$PROJECT" --zone="$ZONE" \
        --format="value(networkInterfaces[0].accessConfigs[0].natIP)" 2>/dev/null || true)"
    log ""
    log "  ④ 🔴 公网 22 必须被拒 —— **从一台境外机器上测，且那台机器不能开着代理客户端**"
    log "     （本机若开着 TUN/fake-ip，测出来的东西没有任何意义，见 infra/node/verify-route.sh 开头；"
    log "      也不要从 vpn-us / vpn-jp 上测 —— 它们带 vpn-node 标签，走的是 VPC 内网路径）"
    log "     预期：连接超时（GCP 的 deny 是丢包不是 RST）"
    log "     timeout 8 bash -c \"cat < /dev/null > /dev/tcp/${_sv_ip:-<IP>}/22\" \\"
    log "       && echo '🔴 22 裸奔' || echo '22 已压制 ✅'"
    log ""
    log "  节点外网 IP：${_sv_ip:-<未知>}"
}

# ---------------------------------------------------------------------------
# main
# ---------------------------------------------------------------------------
main() {
    preflight
    want_stage firewall && snapshot before
    want_stage firewall && stage_firewall
    want_stage sa       && stage_sa
    want_stage address  && stage_address
    want_stage instance && stage_instance
    want_stage verify   && stage_verify
    want_stage verify   && snapshot after

    step "下一步"
    log "  1. 把节点写进 infra/fleet/fleet.json（host / zone / region / machine_type /"
    log "     network_tier=$NETWORK_TIER / ip / address_name；status 从 planned 改掉），然后："
    log "       ./infra/scripts/verify-isolation.sh --project=$PROJECT   —— 必须绿"
    log "     它的 vpn 侧期望**从 fleet.json 读**（2026-09-05 起，ADR 0017 §3 / runbook §1.3）："
    log "     清单没改就先跑，它会因为「多了一台机器」变红 —— 那是正确的红，改清单，不要改它。"
    log "  2. 装机：./infra/fleet/setup-vpn-node.sh --help（凭据走 stdin，不进命令行、不进 history）"
    log "  3. 巡检：./infra/fleet/healthcheck-install.sh $NODE（本机执行，IAP SSH + stdin 把"
    log "     healthcheck.sh 与 systemd timer 装上去；规格见 runbook §3.2 / §3.5）"
    log "  4. 🔴 路由验收：infra/node/verify-route.sh 拒绝 vpn-* 目标（它守的是商用队），"
    log "     按 docs/evidence/node-route-methodology-20260901 的方法手工采样，结果入证据目录。"
    log "  5. 清点 diff：diff -u ${EVIDENCE_DIR}/inventory-before.txt \\"
    log "                       ${EVIDENCE_DIR}/inventory-after.txt"
    log "     期望：vpn-us / vpn-jp / vpn-*-ip / 全部 bp-* / 三个 Cloud Run 服务**零变化**"
    log "  6. 证据目录 ${EVIDENCE_DIR}"
    log "     必须补 README.md（证明什么 / 不证明什么），并在 docs/evidence/README.md 的"
    log "     「已完成」表里加一行 —— CI 的 infra/scripts/check-evidence-index.sh 三条判据全部致命。"
}

main

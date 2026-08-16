#!/usr/bin/env bash
# =============================================================================
# create-node.sh · 建 bp 节点的 GCP 侧资源
#
#   防火墙先行 → 服务账号 → 静态 IP 预筛 → 创建实例 → 建机即刻验收
#
# 事实源：docs/04-ops/node-provisioning.md §3（本脚本是它的可执行形式）
#         docs/05-adr/0007-node-migration.md §6（防火墙与标签策略）
#         docs/02-architecture/as-built-gcp.md §3（三条现存防火墙风险）
#
# -----------------------------------------------------------------------------
# 🔴 本脚本存在的首要理由：网络标签
# -----------------------------------------------------------------------------
# oratis-491316 的 default 网络里有三条**没有 target tag** 的规则，它们对该网络中
# 的每一台实例生效（as-built §3 实测）：
#
#   allow-xray-443        0.0.0.0/0 → tcp:443    无 tag  → 新 VM 自动放通 443
#   allow-hysteria-udp443 0.0.0.0/0 → udp:443    无 tag  → 新 VM 自动放通 443
#   default-allow-ssh     0.0.0.0/0 → tcp:22     无 tag  → 新 VM 的 22 对全网放通
#
# 压制 default-allow-ssh 的 vpn-public-ssh-deny **只绑 vpn-node 标签**。
# 也就是说：**一台不带任何能命中 SSH deny 的标签的新 VM，22 端口对全网裸奔。**
# （缓和事实：Debian 官方镜像默认 PasswordAuthentication no —— 但那是「碰巧安全」，
#   不是「设计安全」，不构成依赖它的理由。）
#
# 本脚本的处置，三步，缺一不可：
#   1. 建 bp-public-ssh-deny（DENY tcp:22，0.0.0.0/0，**绑 bp-node**），
#      优先级数字必须 < default-allow-ssh 的 65534；
#   2. 建 bp-iap-ssh-allow（ALLOW tcp:22，35.235.240.0/20，**绑 bp-node**），
#      优先级数字必须 < bp-public-ssh-deny，否则连 IAP 也被自己挡住 ——
#      GCE 没有带外 console，那会让节点变成一块无法登录的砖；
#   3. **创建实例之前**硬性核对上述两个不等式与 target tag（assert_ssh_posture），
#      不通过就拒绝建机。实例从 RUNNING 到规则生效之间的窗口不允许存在。
#
# 而 bp-allow-reality-443 / bp-allow-hy2-udp443 是**明知冗余仍要建**的：
# as-built §3 的处置建议本身就写着要给那两条无 tag 规则补 target tag 做收敛。
# 一旦有人执行了那个（正确的）收敛动作，bp 节点会毫无预警地瞬时失去 443 入向，
# 而现象（443 无响应 / 零入站连接 / 进程 active）与 IP 级封锁的三条取证特征完全吻合，
# 排障会走到「释放 IP 重开机器」上去。用 40 秒建两条冗余规则，
# 换掉一个能造成半小时误诊的跨系统耦合。
#
# -----------------------------------------------------------------------------
# 用法见 --help；一切改动都支持 --dry-run。
# =============================================================================
set -euo pipefail

# ---------------------------------------------------------------------------
# 0 · 默认值（全部可被环境变量或命令行覆盖）
# ---------------------------------------------------------------------------
PROJECT="${BP_PROJECT:-oratis-491316}"
NODE="${BP_NODE:-bp-node-hk1}"
REGION="${BP_REGION:-asia-east2}"          # 香港。ADR 0004 §3.6
ZONE="${BP_ZONE:-asia-east2-a}"            # 🔴 必须避开 -b（社区实测绕道美国，待核实）
MACHINE_TYPE="${BP_MACHINE_TYPE:-e2-small}"
BOOT_DISK_SIZE="${BP_BOOT_DISK_SIZE:-20GB}"
IP_CANDIDATES="${BP_IP_CANDIDATES:-5}"
NODE_TAG="bp-node"
SA_NAME="bp-node-sa"

DRY_RUN=0
ASSUME_YES=0
AUTO_PICK=0
ENABLE_IPV6=0
ALLOW_SUBNET_MUTATION=0
FIX_FIREWALL=0
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

# 绝不允许本脚本触碰的既有资源（AGENTS.md §4 操作红线 + as-built §2/§3/§4）
PROTECTED_NAMES="vpn-us vpn-jp vpn-us-ip-v4 vpn-jp-ip \
anthropic-relay lisa-cloud lisa-web \
allow-hysteria-udp443 allow-xray-443 allow-ss-48882 allow-iap-ssh \
vpn-iap-ssh-allow vpn-public-ssh-deny \
default-allow-icmp default-allow-internal default-allow-rdp default-allow-ssh"

# assert_target_safe · 目标必须是 bp- 前缀的新资源，且不在保护名单里。
# 这一条挡的是「参数敲错一个字就把现役节点改掉」。
assert_target_safe() {
    _ats_name="$1"
    for _ats_p in $PROTECTED_NAMES; do
        if [ "$_ats_name" = "$_ats_p" ]; then
            die "拒绝操作受保护资源 '$_ats_name'。它属于现役系统（vpn-* 节点 / Cloud Run / 既有防火墙规则），
      本仓库的操作红线是「不修改、不删除、不改名」。"
        fi
    done
    case "$_ats_name" in
        vpn-*|vpn_*)
            die "拒绝操作 '$_ats_name'：vpn-* 命名空间属于现役代理节点，本脚本一律不碰。" ;;
        bp-*) : ;;
        *)
            die "节点名 '$_ats_name' 不是 bp- 前缀。as-built §2.1 的隔离承诺要求新建资源一律 bp- 前缀。" ;;
    esac
}

usage() {
    cat <<'USAGE'
用法：create-node.sh [选项]

  建 bp 节点的 GCP 侧资源。默认目标 bp-node-hk1 / asia-east2-a / e2-small / Premium。
  **绝不触碰 vpn-us / vpn-jp 与三个 Cloud Run 服务**（脚本开头有硬性守卫）。

选项
  --project ID          GCP 项目（默认 oratis-491316，或 $BP_PROJECT）
  --node NAME           节点名，必须 bp- 前缀（默认 bp-node-hk1，或 $BP_NODE）
  --region REGION       默认 asia-east2（香港，ADR 0004 §3.6）
  --zone ZONE           默认 asia-east2-a。🔴 拒绝 -b（社区实测绕道美国，待核实）
  --machine-type TYPE   默认 e2-small（ADR 0007 §4.4；机型是可逆决策）
  --candidates N        预留几个候选静态 IP 做网段预筛（默认 5）
  --address NAME        跳过预筛，直接用这个已存在的保留地址名
  --auto-pick           按网段启发式自动选一个候选（不加则交互式选）
  --ipv6                同时配 IPv6（需配合 --allow-subnet-mutation）
  --allow-subnet-mutation
                        允许修改共享项目的 default 子网（开 IPv6 必需，爆炸半径见 §3.6）
  --fix-firewall        已存在但属性不符的 bp-* 规则，允许原地 update（默认只报不改）
  --only STAGE          只跑某一段，可重复：firewall|sa|address|instance|verify
  --evidence-dir DIR    清点快照落点（默认 ./evidence/node-provision-<node>-<date>/）
  --dry-run             只打印将要执行的变更命令，不做任何改动（只读查询照常执行）
  --yes                 跳过交互确认（CI 用；危险操作仍会打印全部细节）
  -h, --help            本帮助

环境变量
  BP_SS_PORT            设了才建 SS-2022 的防火墙规则。**不得为 48882**
                        （那是 vpn-node 的端口，复用会把 bp 节点与 vpn-* 关联起来）

顺序（不要跳步，理由见 docs/04-ops/node-provisioning.md §1）
  firewall → sa → address → instance → verify
USAGE
}

# ---------------------------------------------------------------------------
# 2 · dry-run 包装
# ---------------------------------------------------------------------------
# run · 有副作用的命令走这里；dry-run 下只打印。
# 只读查询（describe / list）**不**走 run —— dry-run 下照常执行，这样预演也能看到真实状态。
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
        --candidates)   IP_CANDIDATES="${2:?--candidates 需要值}"; shift 2 ;;
        --address)      PICK_ADDRESS="${2:?--address 需要值}"; shift 2 ;;
        --evidence-dir) EVIDENCE_DIR="${2:?--evidence-dir 需要值}"; shift 2 ;;
        --only)         ONLY_STAGES="$ONLY_STAGES ${2:?--only 需要值}"; shift 2 ;;
        --auto-pick)    AUTO_PICK=1; shift ;;
        --ipv6)         ENABLE_IPV6=1; shift ;;
        --allow-subnet-mutation) ALLOW_SUBNET_MUTATION=1; shift ;;
        --fix-firewall) FIX_FIREWALL=1; shift ;;
        --dry-run)      DRY_RUN=1; shift ;;
        --yes|-y)       ASSUME_YES=1; shift ;;
        -h|--help)      usage; exit 0 ;;
        *)              usage >&2; die "未知参数：$1" ;;
    esac
done

assert_target_safe "$NODE"

# --only 的取值必须真实存在。打错一个字就「什么都没跑却退出码 0」是最坏的失败形式。
ALL_STAGES="firewall sa address instance verify"
for _os in $ONLY_STAGES; do
    case " $ALL_STAGES " in
        *" $_os "*) : ;;
        *) die "--only 的值 '$_os' 不是有效阶段。可选：$ALL_STAGES" ;;
    esac
done

IPNAME="${NODE}-ip"
[ -z "$EVIDENCE_DIR" ] && EVIDENCE_DIR="./evidence/node-provision-${NODE}-$(date +%Y%m%d)"

# ---------------------------------------------------------------------------
# 4 · 前置检查
# ---------------------------------------------------------------------------
preflight() {
    step "前置检查"
    command -v gcloud >/dev/null 2>&1 || die "找不到 gcloud。"

    case "$ZONE" in
        *-b)
            die "zone '$ZONE' 以 -b 结尾。ADR 0004 §3.5：asia-east2-b 社区实测绕道美国（2019，待核实），
      本项目已裁定避开。请用 -a 或 -c。" ;;
        "$REGION"-*) : ;;
        *) die "zone '$ZONE' 与 region '$REGION' 不匹配。" ;;
    esac

    if [ -n "${BP_SS_PORT:-}" ]; then
        case "$BP_SS_PORT" in
            ''|*[!0-9]*) die "BP_SS_PORT 必须是纯数字，当前值不合法。" ;;
        esac
        if [ "$BP_SS_PORT" -lt 1024 ] || [ "$BP_SS_PORT" -gt 65535 ]; then
            die "BP_SS_PORT 超出 1024-65535。"
        fi
        if [ "$BP_SS_PORT" = "48882" ]; then
            die "BP_SS_PORT 不得为 48882 —— 那是 vpn-node 的 SS 端口
      （node-provisioning §4.5）。复用它会把 bp 节点与 vpn-* 关联起来，
      且 allow-ss-48882 绑的是 vpn-node 标签，对 bp 节点根本不生效。"
        fi
        ok "SS-2022 端口 ${BP_SS_PORT}（非 48882）"
    else
        log "  BP_SS_PORT 未设置 → 不建 SS 防火墙规则（SS-2022 是兜底通路，可后补）"
    fi

    _pf_acct="$(gcloud config get-value account 2>/dev/null || true)"
    log "  gcloud 身份：${_pf_acct:-<未登录>}"
    log "  项目 / 区域 / 可用区：$PROJECT / $REGION / $ZONE"
    log "  节点 / 标签 / 机型：$NODE / $NODE_TAG / $MACHINE_TYPE"
    [ "$DRY_RUN" = 1 ] && warn "DRY-RUN：不会做任何改动"
    return 0
}

# snapshot · as-built §7 的清点命令。变更前后各跑一次做 diff，
# 这是「不影响已部署服务」这条约束的唯一可验证形式（ADR 0007 §9.2）。
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
            # shellcheck disable=SC2086
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

fw_exists() {
    gcloud compute firewall-rules describe "$1" --project="$PROJECT" \
        --format="value(name)" >/dev/null 2>&1
}

# audit_untagged · 显式点名那些「无 target tag、会作用到新 VM」的既有规则。
# 我们不修改它们（那属于「影响已部署服务」，需授权且要走维护窗口），只把风险写在屏幕上。
audit_untagged() {
    step "审计：default 网络里无 target tag 的规则（它们会作用到新 VM）"
    log "  规则名                    优先级  作用"
    for _au_r in default-allow-ssh allow-xray-443 allow-hysteria-udp443 default-allow-rdp; do
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
    log "  🔴 结论：default-allow-ssh 无 tag 且放通 0.0.0.0/0:22，压制它的 vpn-public-ssh-deny"
    log "     只绑 vpn-node。新 VM 必须自带能命中 bp-public-ssh-deny 的 bp-node 标签，"
    log "     否则 22 端口对全网裸奔。本脚本在建实例前会硬性核对（assert_ssh_posture）。"
}

# fw_ensure · 幂等建规则。已存在则核对关键属性；不符只报不改，除非 --fix-firewall。
#   $1 名字  $2 优先级  $3 action(ALLOW|DENY)  $4 rules  $5 source-ranges  $6 描述
fw_ensure() {
    _fe_name="$1"; _fe_prio="$2"; _fe_action="$3"
    _fe_rules="$4"; _fe_src="$5"; _fe_desc="$6"

    if fw_exists "$_fe_name"; then
        _fe_have_prio="$(fw_field "$_fe_name" priority)"
        _fe_have_tags="$(fw_field "$_fe_name" "targetTags.list()")"
        if [ "$_fe_have_prio" = "$_fe_prio" ] && \
           printf '%s' "$_fe_have_tags" | grep -q "$NODE_TAG"; then
            ok "$_fe_name 已存在且属性相符（priority=$_fe_have_prio tags=$_fe_have_tags）"
            return 0
        fi
        warn "$_fe_name 已存在但属性不符：priority=$_fe_have_prio tags=${_fe_have_tags:-<无>}"
        warn "  期望：priority=$_fe_prio tags 含 $NODE_TAG"
        if [ "$FIX_FIREWALL" != 1 ]; then
            warn "  未加 --fix-firewall，本脚本不擅自 update。手工修：
        gcloud compute firewall-rules update $_fe_name --project=$PROJECT \\
          --priority=$_fe_prio --target-tags=$NODE_TAG"
            return 0
        fi
        run gcloud compute firewall-rules update "$_fe_name" --project="$PROJECT" \
            --priority="$_fe_prio" --target-tags="$NODE_TAG"
        return 0
    fi

    run gcloud compute firewall-rules create "$_fe_name" --project="$PROJECT" \
        --network=default --direction=INGRESS --priority="$_fe_prio" \
        --action="$_fe_action" --rules="$_fe_rules" --source-ranges="$_fe_src" \
        --target-tags="$NODE_TAG" \
        --description="$_fe_desc"
    ok "已建 $_fe_name（priority=$_fe_prio, target-tags=$NODE_TAG）"
}

stage_firewall() {
    step "阶段 firewall · 建 bp-* 规则（必须在建实例之前）"
    audit_untagged

    # ① IAP SSH 放通 —— 优先级数字必须最小，否则自己被自己的 deny 挡在门外。
    fw_ensure bp-iap-ssh-allow 900 ALLOW tcp:22 35.235.240.0/20 \
        "IAP TCP forwarding only. MUST outrank bp-public-ssh-deny."
    # ② 压制 default-allow-ssh（0.0.0.0/0 → tcp:22，无 tag，priority 65534）。
    fw_ensure bp-public-ssh-deny 1000 DENY tcp:22 0.0.0.0/0 \
        "Suppress default-allow-ssh for bp nodes."
    # ③④ 明知冗余仍要建：切断对两条无 tag 规则的隐式依赖（ADR 0007 §6.3）。
    fw_ensure bp-allow-reality-443 1000 ALLOW tcp:443 0.0.0.0/0 \
        "REALITY / VLESS-XTLS-Vision. Redundant with untagged allow-xray-443 on purpose."
    fw_ensure bp-allow-hy2-udp443 1000 ALLOW udp:443 0.0.0.0/0 \
        "Hysteria2 / QUIC. Redundant with untagged allow-hysteria-udp443 on purpose."

    if [ -n "${BP_SS_PORT:-}" ]; then
        fw_ensure "bp-allow-ss-${BP_SS_PORT}" 1000 ALLOW \
            "tcp:${BP_SS_PORT},udp:${BP_SS_PORT}" 0.0.0.0/0 \
            "SS-2022 fallback. Port intentionally different from legacy 48882."
    fi

    if [ "$ENABLE_IPV6" = 1 ]; then
        # 开了 IPv6 就必须补 IPv6 的 443 规则 —— IPv4 的 0.0.0.0/0 不覆盖 IPv6。
        # ⚠️ 反过来：**不要**给 22 加任何 ::/0 的 allow。既有规则全是 IPv4-only，
        #    IPv6 入向默认隐式拒绝，这是白送的一层保护，别自己拆掉。
        fw_ensure bp-allow-reality-443-v6 1000 ALLOW tcp:443 ::/0 \
            "REALITY over IPv6. NOTE: no IPv6 rule for tcp:22 by design."
        fw_ensure bp-allow-hy2-udp443-v6 1000 ALLOW udp:443 ::/0 \
            "Hysteria2 over IPv6. NOTE: no IPv6 rule for tcp:22 by design."
    fi
}

# assert_ssh_posture · 🔴 建实例之前的硬闸。不通过就拒绝建机。
assert_ssh_posture() {
    step "🔴 硬闸：SSH 姿态核对（不通过则拒绝创建实例）"
    if [ "$DRY_RUN" = 1 ] && ! fw_exists bp-public-ssh-deny; then
        warn "dry-run 且规则尚不存在，跳过硬闸核对（真实执行时这里会拦住）"
        return 0
    fi

    _ap_deny_prio="$(fw_field bp-public-ssh-deny priority)"
    _ap_deny_tags="$(fw_field bp-public-ssh-deny "targetTags.list()")"
    _ap_iap_prio="$(fw_field bp-iap-ssh-allow priority)"
    _ap_iap_tags="$(fw_field bp-iap-ssh-allow "targetTags.list()")"
    _ap_def_prio="$(fw_field default-allow-ssh priority)"

    [ -n "$_ap_deny_prio" ] || die "bp-public-ssh-deny 不存在。没有它，新 VM 的 22 端口会被
      无 target tag 的 default-allow-ssh 对 0.0.0.0/0 放通。先跑 --only firewall。"
    [ -n "$_ap_iap_prio" ] || die "bp-iap-ssh-allow 不存在。只建 deny 不建 allow，
      IAP 隧道会被自己的 deny 挡住，节点将成为无法登录的砖。"

    printf '%s' "$_ap_deny_tags" | grep -q "$NODE_TAG" \
        || die "bp-public-ssh-deny 的 target tags 是 '${_ap_deny_tags:-<无>}'，不含 $NODE_TAG。
      规则不绑 tag 或绑错 tag = 规则白写。"
    printf '%s' "$_ap_iap_tags" | grep -q "$NODE_TAG" \
        || die "bp-iap-ssh-allow 的 target tags 是 '${_ap_iap_tags:-<无>}'，不含 $NODE_TAG。"

    # 不等式 1：deny 必须比 default-allow-ssh 优先（数字更小）。
    if [ -n "$_ap_def_prio" ]; then
        [ "$_ap_deny_prio" -lt "$_ap_def_prio" ] \
            || die "不等式 1 不成立：bp-public-ssh-deny($_ap_deny_prio) 必须 <
      default-allow-ssh($_ap_def_prio)。数字越小优先级越高，否则公网 22 依然放通。"
        ok "不等式 1：deny($_ap_deny_prio) < default-allow-ssh($_ap_def_prio)"
    else
        warn "查不到 default-allow-ssh（可能已被删或无读权限）。风险变小，但仍按 65534 核对。"
        [ "$_ap_deny_prio" -lt 65534 ] || die "不等式 1 不成立（按默认 65534 核对）。"
    fi

    # 不等式 2：IAP allow 必须比 deny 优先，否则节点变砖。
    [ "$_ap_iap_prio" -lt "$_ap_deny_prio" ] \
        || die "不等式 2 不成立：bp-iap-ssh-allow($_ap_iap_prio) 必须 <
      bp-public-ssh-deny($_ap_deny_prio)。否则 IAP 隧道也被挡，GCE 没有能救 SSH 的带外 console。"
    ok "不等式 2：iap-allow($_ap_iap_prio) < deny($_ap_deny_prio)"
    ok "SSH 姿态核对通过 —— 新 VM 打上 $NODE_TAG 后，22 端口只对 IAP 网段开放"
}

# ---------------------------------------------------------------------------
# 6 · 服务账号
# ---------------------------------------------------------------------------
stage_sa() {
    step "阶段 sa · 最小权限服务账号 ${SA_NAME}（故意零角色）"
    _sa_email="${SA_NAME}@${PROJECT}.iam.gserviceaccount.com"
    _sa_present=1
    if gcloud iam service-accounts describe "$_sa_email" --project="$PROJECT" \
            --format="value(email)" >/dev/null 2>&1; then
        ok "$_sa_email 已存在"
    else
        _sa_present=0
        run gcloud iam service-accounts create "$SA_NAME" --project="$PROJECT" \
            --display-name="babel.plus node runtime" \
            --description="Attached to bp-node-*. Intentionally holds ZERO project roles."
    fi

    if [ "$_sa_present" = 0 ]; then
        log "  （SA 刚建或 dry-run 未建，角色核对留到下次跑本阶段时做）"
        return 0
    fi

    # 核实「零角色」这句话是真的。默认 Compute SA 在多数项目带 Editor ——
    # 一台被攻陷的节点若持有它，可以删掉 vpn-us / vpn-jp 与三个 Cloud Run 服务。
    _sa_roles="$(gcloud projects get-iam-policy "$PROJECT" \
        --flatten="bindings[].members" \
        --filter="bindings.members:serviceAccount:${_sa_email}" \
        --format="value(bindings.role)" 2>/dev/null || true)"
    if [ -n "$_sa_roles" ]; then
        warn "${SA_NAME} 持有以下项目角色，与「零角色」设计不符，请人工核实："
        printf '%s\n' "$_sa_roles" | sed 's/^/      /'
    else
        ok "${SA_NAME} 无任何项目级角色绑定（符合设计）"
    fi
}

# ---------------------------------------------------------------------------
# 7 · 静态 IP：预留候选 + 网段预筛
# ---------------------------------------------------------------------------
# rank_ip · 网段启发式打分，数字越小越优先。
# 🔴 依据是两条 2019/2022 年的社区单一来源（ADR 0004 §3.5，**待核实**）：
#    35.220.x → 移动直连约 50 ms；34.92.x → 移动经东京绕行约 110 ms。
# **这只是先验，不是验收。** 真正的判定在 verify-route.sh（三网路由实测）。
# 网段好但实测不合格照样释放；网段一般但实测合格照样留用。
rank_ip() {
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

stage_address() {
    step "阶段 address · 预留 ${IP_CANDIDATES} 个候选静态 IP 并做网段预筛"
    if [ -n "$PICK_ADDRESS" ]; then
        assert_target_safe "$PICK_ADDRESS"
        _sa_ip="$(addr_value "$PICK_ADDRESS")"
        [ -n "$_sa_ip" ] || [ "$DRY_RUN" = 1 ] || die "保留地址 $PICK_ADDRESS 不存在。"
        ok "使用指定地址 $PICK_ADDRESS (${_sa_ip:-<dry-run>})"
        SELECTED_ADDRESS="$PICK_ADDRESS"
        return 0
    fi

    log "  ⚠️ 闲置保留地址是计费的（2024-02-01 起 GCP 对全部外部 IPv4 计费，"
    log "     闲置约 \$0.010/hr、在用约 \$0.005/hr，**待核实**）。用完立刻删多余的。"

    _sa_i=1
    while [ "$_sa_i" -le "$IP_CANDIDATES" ]; do
        _sa_n="${IPNAME}-cand${_sa_i}"
        if [ -n "$(addr_value "$_sa_n")" ]; then
            ok "$_sa_n 已存在，复用"
        else
            run gcloud compute addresses create "$_sa_n" --project="$PROJECT" \
                --region="$REGION" --network-tier=PREMIUM
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
# 8 · IPv6 子网（可选，会改共享项目的 default 子网）
# ---------------------------------------------------------------------------
stage_ipv6_subnet() {
    [ "$ENABLE_IPV6" = 1 ] || return 0
    step "IPv6 · 给 ${REGION} 的 default 子网开 IPV4_IPV6"
    log "  ⚠️ 这一步修改的是**共享项目的 default 子网**。"
    log "     前置：该区域当前没有任何实例（爆炸半径为零）。先自检："
    _iv_n="$(gcloud compute instances list --project="$PROJECT" \
        --filter="zone~${REGION}" --format="value(name)" 2>/dev/null | wc -l | tr -d ' ')"
    log "     ${REGION} 现有实例数：${_iv_n}"
    if [ "$ALLOW_SUBNET_MUTATION" != 1 ]; then
        die "--ipv6 需要同时加 --allow-subnet-mutation 才会执行子网变更。"
    fi
    if [ "${_iv_n:-0}" != "0" ]; then
        confirm_typed "$NODE" "${REGION} 已有 ${_iv_n} 台实例，子网变更不再是零爆炸半径"
    fi
    # ⚠️ 参数名 **待核实** —— 未在本项目实测过（node-provisioning §3.6）。
    run gcloud compute networks subnets update default --project="$PROJECT" \
        --region="$REGION" --stack-type=IPV4_IPV6 --ipv6-access-type=EXTERNAL
}

# ---------------------------------------------------------------------------
# 9 · 创建实例
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

    confirm_typed "$NODE" "将创建实例 $NODE（$MACHINE_TYPE / $ZONE / IP ${_si_ip:-<dry-run>}）"

    set -- \
        compute instances create "$NODE" \
        --project="$PROJECT" --zone="$ZONE" \
        --machine-type="$MACHINE_TYPE" \
        --image-family=debian-12 --image-project=debian-cloud \
        --boot-disk-size="$BOOT_DISK_SIZE" --boot-disk-type=pd-balanced \
        --boot-disk-device-name="$NODE" \
        --network=default --network-tier=PREMIUM \
        --tags="$NODE_TAG" \
        --service-account="${SA_NAME}@${PROJECT}.iam.gserviceaccount.com" \
        --scopes=https://www.googleapis.com/auth/cloud-platform \
        --shielded-secure-boot --shielded-vtpm --shielded-integrity-monitoring \
        --metadata=block-project-ssh-keys=TRUE,enable-oslogin=TRUE \
        --labels=owner=babelplus,role=node,region=hk \
        --deletion-protection
    # ↑ --scopes 是权限上界，不是权限来源：bp-node-sa 零角色 ⇒ 实际权限等于零。
    #   不用 --metadata-from-file=startup-script=：startup script 以 root 每次开机运行，
    #   且凭据必须落进 metadata，而 metadata 对项目内任何有读权限的主体可见。
    #   装机走 setup-node.sh 的 SSH + stdin（node-provisioning §2.2）。
    [ -n "$_si_ip" ] && set -- "$@" --address="$_si_ip"
    if [ "$ENABLE_IPV6" = 1 ]; then
        # stack-type 能否事后变更 **待核实** → 保守做法是建机时就带上。
        set -- "$@" --stack-type=IPV4_IPV6 --ipv6-network-tier=PREMIUM
    fi
    run gcloud "$@"
    ok "实例已创建"
}

# ---------------------------------------------------------------------------
# 10 · 建机即刻验收
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

    log "  ① 实例网络标签"
    _sv_tags="$(gcloud compute instances describe "$NODE" --project="$PROJECT" \
        --zone="$ZONE" --format="value(tags.items)" 2>/dev/null || true)"
    if printf '%s' "$_sv_tags" | grep -q "$NODE_TAG"; then
        ok "tags = ${_sv_tags}（含 $NODE_TAG）"
    else
        warn "tags = '${_sv_tags:-<空>}' —— 🔴 不含 $NODE_TAG！22 端口正在裸奔。"
        warn "  立即修：gcloud compute instances add-tags $NODE --project=$PROJECT \\"
        warn "            --zone=$ZONE --tags=$NODE_TAG"
    fi

    log "  ② 生效中的防火墙（以 GCP 侧为准，不要只信本地测试）"
    _sv_eff="$(gcloud compute instances network-interfaces get-effective-firewalls "$NODE" \
        --project="$PROJECT" --zone="$ZONE" --format=json 2>/dev/null || true)"
    for _sv_r in bp-iap-ssh-allow bp-public-ssh-deny bp-allow-reality-443 bp-allow-hy2-udp443; do
        if printf '%s' "$_sv_eff" | grep -q "\"$_sv_r\""; then
            ok "  生效：$_sv_r"
        else
            warn "  未在生效列表中看到：$_sv_r"
        fi
    done

    log "  ③ IAP SSH 应当可通"
    log "     gcloud compute ssh $NODE --project=$PROJECT --zone=$ZONE \\"
    log "       --tunnel-through-iap --command='echo IAP-OK'"

    _sv_ip="$(gcloud compute instances describe "$NODE" --project="$PROJECT" --zone="$ZONE" \
        --format="value(networkInterfaces[0].accessConfigs[0].natIP)" 2>/dev/null || true)"
    log ""
    log "  ④ 🔴 公网 22 必须被拒 —— **从一台境外机器上测，且那台机器不能开着代理客户端**"
    log "     （本机若开着 TUN/fake-ip，测出来的东西没有任何意义，见 verify-route.sh 开头）"
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
    want_stage address  && stage_ipv6_subnet
    want_stage address  && stage_address
    want_stage instance && stage_instance
    want_stage verify   && stage_verify
    want_stage verify   && snapshot after

    step "下一步"
    log "  1. 装机：见 setup-node.sh --help（凭据走 stdin，不进命令行、不进 history）"
    log "  2. 接面板：node-provisioning §6"
    log "  3. 🔴 IP 路由验收：./verify-route.sh --node $NODE  —— 这是唯一不可跳过的闸"
    log "  4. 清点 diff：diff -u ${EVIDENCE_DIR}/inventory-before.txt \\"
    log "                       ${EVIDENCE_DIR}/inventory-after.txt"
    log "     期望：vpn-us / vpn-jp / vpn-*-ip / 三个 Cloud Run 服务**零变化**"
}

main

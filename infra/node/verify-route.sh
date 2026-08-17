#!/usr/bin/env bash
# =============================================================================
# verify-route.sh · 🔴 IP 路由验收（建机七段里唯一不可跳过的那一段）
#
# 事实源：docs/04-ops/node-provisioning.md §5（J1–J6 判据与采样要求）
#         docs/05-adr/0004-transport-hardening.md §3.5（「拿到哪个 IP」是一等变量）
#         docs/04-ops/runbook-node-health.md §0（不要用系统网络工具判断连通性）
#
# 为什么这一段不能跳：其余六段做错了都能重来（机型可改、装机脚本幂等可重跑、
# 面板记录可改），而**把一个绕道美国的 IP 推给用户，代价是用户第一次连接
# 就判定这个服务不行**。建机阶段换 IP 代价为零，上线之后代价是全员重导订阅。
#
# =============================================================================
# 🔴🔴 先说不能怎么测 —— 这一段比脚本本身重要
# =============================================================================
# **本机开着 TUN / fake-ip 时，ping / dig / nslookup / nc / curl --interface
#   的结果全部被客户端劫持，不能作为任何判断依据。**
#
# Proxy_Skill 记录过一次对照实验：连 baidu.com 的**正对照也失败** ——
# 说明失败来自本机劫持，不是链路问题。也就是说：
#
#   ✗ `dig @8.8.8.8 example.com`      → fake-ip 会返回 198.18.x.x 之类的假地址
#   ✗ `nc -vz <节点IP> 443`           → TUN 把这条 TCP 连接送进了代理，测的是代理不是节点
#   ✗ `curl -v https://<节点IP>`      → 同上，且 curl --interface 也逃不掉 TUN 路由
#   ✗ `ping <节点IP>`                 → ICMP 在部分 TUN 实现下同样被接管
#
# **正确做法有三条，按可信度排序：**
#   ① 在**节点自己**身上测出向路径（本脚本做的就是这个）—— 机器是我们的，没有 TUN。
#   ② 用一台**从未装过代理客户端**的机器测入向。
#   ③ 需要「经由节点的连通性/延迟」这类数据时，走**客户端内核的 API**
#      （mihomo 的 delay 接口；Clash Verge 的 unix socket 在 /tmp/verge/verge-mihomo.sock），
#      **不要用系统命令**。
#
# 退出代理客户端时要**退出进程**，不是切「直连」模式 ——
# fake-ip 的 DNS 劫持在直连模式下依然可能生效。
#
# 本脚本据此做了一个硬性选择：**所有测量都在节点上跑，本机只负责编排与判定。**
# 并且在本机检测到 TUN 迹象时会明确警告（不阻断，因为本机不参与测量）。
#
# =============================================================================
# 三个数据源，各测半张图（我们目前没有探针机）
# =============================================================================
#   A. 节点本机 mtr 打中国三网目标 → 测到**出向**（HK → CN），测不到入向。强度高但只有一半。
#   B. 国内公开多点测速站          → 唯一能拿到**入向**数据的来源，口径与时段不可控。
#   C. 运维自己的国内机器          → 真实用户视角，样本 = 1。
#
# ⚠️ **A 与 B 测的不是同一条路径。** 中国方向的非对称路由是常态：
#    入向路径由中国运营商的 BGP 决策决定，我们完全无法控制 ——
#    这正是「移动绕美」现象的成因。
#    **只跑 A 就宣布验收通过是错的。** A 好看而 B 难看完全可能发生，
#    而用户体验由 B 决定。所以本脚本自动化的只有 A，
#    结尾会强制列出 B/C 的人工清单，并且**在 B/C 未完成前拒绝输出「验收通过」**。
# =============================================================================
set -euo pipefail

PROJECT="${BP_PROJECT:-oratis-491316}"
NODE="${BP_NODE:-bp-node-hk1}"
ZONE="${BP_ZONE:-asia-east2-a}"
COUNT="${BP_PROBE_COUNT:-100}"
EVIDENCE_DIR=""
DRY_RUN=0
IS_PEAK=0
MANUAL_DONE=0
SKIP_INSTALL=0

# 探测目标。⚠️ 这三个是**占位示例**（分别取自电信 / 联通 / 移动的公共递归 DNS 段），
# 归属与可达性 **待核实** —— 第一次执行时按实际选定的探测点替换，
# 并把最终列表回写进 docs/04-ops/node-provisioning.md §5.2。
TARGETS_DEFAULT="202.96.209.133:电信 202.106.196.115:联通 211.136.112.50:移动"
TARGETS="${BP_PROBE_TARGETS:-$TARGETS_DEFAULT}"

# ---- 判定阈值。**全部是「设定值」，不来自任何测量。** ----------------------
# 第一批节点跑完必须整体重标定（node-provisioning §10）。
J1_JUMP_MS="${BP_J1_JUMP_MS:-80}"      # 相邻跳 RTT 跃升超过它 → 疑似跨洋绕行
J2_RTT_MS="${BP_J2_RTT_MS:-120}"       # 非高峰中位 RTT 上限。参考：香港物理下限
                                       #   深圳 0.3 / 上海 12.3 / 北京 19.7 ms，
                                       #   实测正常值应落在 30–80 ms
J3_LOSS_PCT="${BP_J3_LOSS_PCT:-5}"     # 非高峰丢包率上限
J4_SPREAD_MS="${BP_J4_SPREAD_MS:-60}"  # 三网中位 RTT 极差，超了只警告不否决
J5_PEAK_RATIO="${BP_J5_PEAK_RATIO:-3}" # 晚高峰相对非高峰的劣化倍数，超了只警告

log()  { printf '%s\n' "$*"; }
step() { printf '\n=== %s\n' "$*"; }
ok()   { printf '  [PASS] %s\n' "$*"; }
warn() { printf '  [warn] %s\n' "$*" >&2; }
die()  { printf '\n[FATAL] %s\n' "$*" >&2; exit 1; }

HARD_FAILS=0      # 测量类硬判据（J1/J2/J3/J6）—— 触发它才谈得上「换 IP」
SOFT_WARNS=0      # J4/J5 之类只警告不否决的
MANUAL_FAIL=0     # 数据源 B/C 未完成 —— 换 IP 救不了，只能去补采样
fail() { printf '  [FAIL] %s\n' "$*" >&2; HARD_FAILS=$((HARD_FAILS + 1)); }
soft() { printf '  [WARN] %s\n' "$*" >&2; SOFT_WARNS=$((SOFT_WARNS + 1)); }

assert_target_safe() {
    case "$1" in
        vpn-us|vpn-jp|vpn-*|vpn_*)
            die "拒绝把 '$1' 当作测量目标：vpn-* 是现役代理节点。
      本脚本会在目标机器上 apt-get install mtr-tiny 并跑 100 包探测 ——
      那是对已部署服务的改动与打扰。" ;;
        bp-*) : ;;
        *) die "节点名 '$1' 不是 bp- 前缀。" ;;
    esac
}

usage() {
    cat <<'USAGE'
用法：verify-route.sh [选项]

  🔴 IP 路由验收。**所有测量在节点上跑**（本机开着 TUN/fake-ip 时任何本地探测
  都不可信，理由见脚本头部）。本机只负责编排、解析与判定。

选项
  --node NAME        节点名，必须 bp- 前缀（默认 bp-node-hk1）
  --project ID       默认 oratis-491316
  --zone ZONE        默认 asia-east2-a
  --count N          每个目标发多少包（默认 100）
  --targets "IP:名 IP:名 ..."
                     覆盖探测目标（默认三条**占位示例**，归属待核实）
  --peak             标记本次为**晚高峰采样**（19:00–24:00 CST）。J5 需要它
  --manual-done      声明数据源 B（国内公开测速站）与 C（自己的国内机器）已完成，
                     且证据已入 evidence/。不加这个，脚本**拒绝**输出「验收通过」
  --evidence-dir DIR 默认 ./evidence/node-route-<node>-<YYYYMMDD>/
  --skip-install     不在节点上装 mtr-tiny（已装过时用）
  --dry-run          只打印将要执行的命令
  -h, --help         本帮助

退出码
  0 = 自动判据（J1/J2/J3/J6）全过 且 --manual-done 已给
  1 = 有硬判据不通过，或 B/C 未完成
USAGE
}

while [ $# -gt 0 ]; do
    case "$1" in
        --node)         NODE="${2:?--node 需要值}"; shift 2 ;;
        --project)      PROJECT="${2:?--project 需要值}"; shift 2 ;;
        --zone)         ZONE="${2:?--zone 需要值}"; shift 2 ;;
        --count)        COUNT="${2:?--count 需要值}"; shift 2 ;;
        --targets)      TARGETS="${2:?--targets 需要值}"; shift 2 ;;
        --evidence-dir) EVIDENCE_DIR="${2:?--evidence-dir 需要值}"; shift 2 ;;
        --peak)         IS_PEAK=1; shift ;;
        --manual-done)  MANUAL_DONE=1; shift ;;
        --skip-install) SKIP_INSTALL=1; shift ;;
        --dry-run)      DRY_RUN=1; shift ;;
        -h|--help)      usage; exit 0 ;;
        *)              usage >&2; die "未知参数：$1" ;;
    esac
done

assert_target_safe "$NODE"
[ -z "$EVIDENCE_DIR" ] && EVIDENCE_DIR="./evidence/node-route-${NODE}-$(date +%Y%m%d)"
LEDGER="${EVIDENCE_DIR}/samples.tsv"

# ---------------------------------------------------------------------------
# 本机 TUN / fake-ip 迹象检测
# ---------------------------------------------------------------------------
# 本机不参与测量，所以这里**不阻断**，只是提醒：如果你正打算手工用 ping/nc
# 复核脚本的结论，先看这一段。
warn_local_tun() {
    step "本机环境提醒（本机不参与测量，仅提示）"
    _lt_hit=0
    if command -v ifconfig >/dev/null 2>&1; then
        if ifconfig 2>/dev/null | grep -qE '^(utun|tun|tap)[0-9]'; then
            _lt_hit=1
        fi
    elif command -v ip >/dev/null 2>&1; then
        if ip -o link show 2>/dev/null | grep -qE ': (utun|tun|tap)[0-9]'; then
            _lt_hit=1
        fi
    fi
    if pgrep -f 'mihomo|clash|sing-box|verge' >/dev/null 2>&1; then
        _lt_hit=1
    fi
    if [ -S /tmp/verge/verge-mihomo.sock ]; then
        _lt_hit=1
    fi
    if [ "$_lt_hit" = 1 ]; then
        warn "检测到 TUN 接口或代理客户端进程。"
        warn "  → 本机的 ping / dig / nc / curl **全部不可信**（连 baidu.com 的正对照也会失败）。"
        warn "  → 本脚本的所有测量都在节点上跑，不受影响；但**你自己手工复核时会踩坑**。"
        warn "  → 要手工复核，请先**退出客户端进程**（不是切「直连」模式 ——"
        warn "     fake-ip 的 DNS 劫持在直连模式下依然可能生效），或换一台没装过客户端的机器。"
    else
        ok "未检测到明显的 TUN / 客户端迹象（不代表一定干净）"
    fi
}

# ---------------------------------------------------------------------------
# 在节点上执行命令
# ---------------------------------------------------------------------------
node_exec() {
    if [ "$DRY_RUN" = 1 ]; then
        printf '  [dry-run] gcloud compute ssh %s --tunnel-through-iap --command=%s\n' \
            "$NODE" "$(printf '%q' "$1")"
        return 0
    fi
    gcloud compute ssh "$NODE" --project="$PROJECT" --zone="$ZONE" \
        --tunnel-through-iap --quiet --command="$1"
}

# ---------------------------------------------------------------------------
# 浮点比较（bash 不会算小数，一律交给 awk）
# ---------------------------------------------------------------------------
fgt() { awk -v a="$1" -v b="$2" 'BEGIN { exit !(a > b) }'; }

# ---------------------------------------------------------------------------
# 解析 mtr -r 报告
# ---------------------------------------------------------------------------
# mtr -r 的行形如：
#   1.|-- 10.128.0.1        0.0%   100    0.3   0.4   0.3   1.2   0.1
# 字段：$1=序号 $2=主机 $3=Loss% $4=Snt $5=Last $6=Avg $7=Best $8=Wrst $9=StDev
#
# ⚠️ **mtr 报的是算术平均（Avg），不是中位数。** 而 §5.3 的 J2 阈值写的是「中位 RTT」。
#    所以本脚本的 J2 **不用 mtr 的 Avg**，改用 ping 的逐包 time= 值自己排序取中位。
#    mtr 在这里只负责 J1（路径）与 J3（丢包）。
#    这个偏差要回写进 node-provisioning §5.2。

# j1_check · 跨洋绕行判定。两条启发式：
#   ① 相邻跳的 Avg 跃升 > J1_JUMP_MS  → **硬判据**（这是 §5.3 J1 的原文口径之一）
#   ② 路径中出现境外远端 PoP 标识      → **需人工确认**（rDNS 命名不可靠，机场码会误报）
j1_check() {
    _j1_file="$1"
    _j1_name="$2"
    _j1_jump="$(awk -v lim="$J1_JUMP_MS" '
        $1 ~ /^[0-9]+\.\|--/ {
            avg = $6 + 0
            if (prev != "" && avg - prev > lim) {
                printf "hop%s(%s) %.1fms -> hop%s(%s) %.1fms\n", pidx, phost, prev, $1, $2, avg
            }
            prev = avg; phost = $2; pidx = $1
        }' "$_j1_file")"
    if [ -n "$_j1_jump" ]; then
        fail "J1 [$_j1_name] 相邻跳 RTT 跃升 > ${J1_JUMP_MS} ms —— 疑似跨洋绕行，**硬否决，立即换 IP**"
        printf '%s\n' "$_j1_jump" | sed 's/^/         /'
    else
        ok "J1 [$_j1_name] 无 > ${J1_JUMP_MS} ms 的相邻跳跃升"
    fi

    # 境外 PoP 机场码启发式。**待核实**：rDNS 命名不统一，这条只用来提示人去读路径，
    # 不作为硬判据 —— 误报（比如域名里恰好含 "ams"）比漏报更常见。
    _j1_pop="$(grep -iE \
        '[.-](lax|sjc|sfo|sea|pdx|iad|ewr|ord|dfw|atl|mia|nrt|hnd|kix|tyo|osa|sin|syd|fra|ams|lhr|cdg|mad)[0-9]*[.-]' \
        "$_j1_file" || true)"
    if [ -n "$_j1_pop" ]; then
        soft "J1 [$_j1_name] 路径里出现疑似境外 PoP 标识（启发式，**需人工确认**）："
        printf '%s\n' "$_j1_pop" | sed 's/^/         /'
    fi
}

# parse_loss · 取 mtr 报告末跳的 Loss%（纯数字，不打印任何别的东西）
parse_loss() {
    awk '$1 ~ /^[0-9]+\.\|--/ { l = $3 } END { gsub("%", "", l); print l + 0 }' "$1"
}

j3_check() {
    _j3_loss="$1"
    _j3_name="$2"
    if fgt "$_j3_loss" "$J3_LOSS_PCT"; then
        fail "J3 [$_j3_name] 末跳丢包 ${_j3_loss}% > ${J3_LOSS_PCT}% —— 硬否决"
    else
        ok "J3 [$_j3_name] 末跳丢包 ${_j3_loss}%"
    fi
}

# median_of_ping · 从 ping 原始输出算真中位数。
# 用 sort -n + 取中间行，避免依赖 gawk 的 asort（mawk / BSD awk 都没有）。
median_of_ping() {
    _mp_file="$1"
    _mp_tmp="$(mktemp)"
    grep -o 'time=[0-9.]*' "$_mp_file" 2>/dev/null | sed 's/time=//' | sort -n >"$_mp_tmp" || true
    _mp_n="$(wc -l <"$_mp_tmp" | tr -d ' ')"
    if [ "${_mp_n:-0}" -lt 1 ]; then
        rm -f "$_mp_tmp"
        printf ''
        return 0
    fi
    _mp_mid=$(((_mp_n + 1) / 2))
    sed -n "${_mp_mid}p" "$_mp_tmp"
    rm -f "$_mp_tmp"
}

# ---------------------------------------------------------------------------
# 主测量
# ---------------------------------------------------------------------------
measure() {
    step "数据源 A · 在 ${NODE} 上打中国三网（出向路径，测不到入向）"
    log "  采样时刻：$(date '+%Y-%m-%d %H:%M:%S %Z')  ·  CST $(TZ=Asia/Shanghai date '+%H:%M')"
    log "  标记：$([ "$IS_PEAK" = 1 ] && printf '晚高峰' || printf '非高峰')"
    log "  每目标 ${COUNT} 包；mtr 与 ping 各一轮，预计耗时数分钟。"

    if [ "$DRY_RUN" != 1 ]; then
        mkdir -p "$EVIDENCE_DIR"
        [ -f "$LEDGER" ] || printf 'iso_time\tpeak\ttarget\toperator\tmedian_ms\tloss_pct\n' >"$LEDGER"
    fi

    if [ "$SKIP_INSTALL" != 1 ]; then
        node_exec 'command -v mtr >/dev/null 2>&1 || sudo apt-get install -y -qq mtr-tiny' >/dev/null 2>&1 \
            || warn "在节点上装 mtr-tiny 失败（可能已装或无网）。加 --skip-install 可跳过这一步。"
    fi

    MEDIANS=""
    for _m_t in $TARGETS; do
        _m_ip="${_m_t%%:*}"
        _m_op="${_m_t##*:}"
        [ "$_m_op" = "$_m_ip" ] && _m_op="unknown"
        _m_tag="${_m_op}-${_m_ip}-$(date +%H%M)$([ "$IS_PEAK" = 1 ] && printf '-peak' || printf '')"
        _m_mtr="${EVIDENCE_DIR}/mtr-${_m_tag}.txt"
        _m_png="${EVIDENCE_DIR}/ping-${_m_tag}.txt"

        step "目标 ${_m_ip}（${_m_op}）"

        # mtr：路径与丢包。不加 -n，保留 rDNS 名字供 J1 的 PoP 启发式使用。
        if [ "$DRY_RUN" = 1 ]; then
            node_exec "mtr -4 -r -c ${COUNT} ${_m_ip}"
            node_exec "ping -4 -n -c ${COUNT} -i 0.3 -W 2 ${_m_ip}"
            continue
        fi
        node_exec "mtr -4 -r -c ${COUNT} ${_m_ip}" >"$_m_mtr" 2>&1 || true
        node_exec "ping -4 -n -c ${COUNT} -i 0.3 -W 2 ${_m_ip}" >"$_m_png" 2>&1 || true
        log "  原始输出：$_m_mtr"
        log "            $_m_png"
        sed 's/^/    /' "$_m_mtr"

        j1_check "$_m_mtr" "$_m_op"
        _m_loss="$(parse_loss "$_m_mtr")"
        j3_check "$_m_loss" "$_m_op"

        _m_med="$(median_of_ping "$_m_png")"
        if [ -z "$_m_med" ]; then
            fail "J2 [$_m_op] 拿不到任何 ping 样本 —— 目标可能不响应 ICMP，换探测点并回写文档"
            _m_med="NA"
        elif fgt "$_m_med" "$J2_RTT_MS"; then
            fail "J2 [$_m_op] 中位 RTT ${_m_med} ms > ${J2_RTT_MS} ms —— 硬否决"
        else
            ok "J2 [$_m_op] 中位 RTT ${_m_med} ms（正常应落在 30–80 ms）"
        fi

        printf '%s\t%s\t%s\t%s\t%s\t%s\n' \
            "$(date -u '+%Y-%m-%dT%H:%M:%SZ')" "$IS_PEAK" "$_m_ip" "$_m_op" "$_m_med" "${_m_loss:-NA}" \
            >>"$LEDGER"
        [ "$_m_med" = "NA" ] || MEDIANS="$MEDIANS $_m_med"
    done
}

# J4 · 三网中位 RTT 极差（警告，不否决）
j4_check() {
    [ -n "${MEDIANS:-}" ] || return 0
    _j4_min=""; _j4_max=""
    for _j4_v in $MEDIANS; do
        [ -z "$_j4_min" ] && _j4_min="$_j4_v" && _j4_max="$_j4_v"
        fgt "$_j4_min" "$_j4_v" && _j4_min="$_j4_v"
        fgt "$_j4_v" "$_j4_max" && _j4_max="$_j4_v"
    done
    _j4_sp="$(awk -v a="$_j4_max" -v b="$_j4_min" 'BEGIN { printf "%.1f", a - b }')"
    if fgt "$_j4_sp" "$J4_SPREAD_MS"; then
        soft "J4 三网中位 RTT 极差 ${_j4_sp} ms > ${J4_SPREAD_MS} ms —— 警告不否决，记入 evidence 并进 A/B 观察名单"
    else
        ok "J4 三网中位 RTT 极差 ${_j4_sp} ms"
    fi
}

# J5 · 晚高峰相对非高峰的劣化（警告，不否决）
# 需要台账里同时有 peak=1 与 peak=0 的样本，所以第一次跑必然「数据不足」。
j5_check() {
    [ -f "$LEDGER" ] || return 0
    _j5_off="$(awk -F'\t' 'NR>1 && $2=="0" && $5!="NA" { s += $5; n++ } END { if (n) printf "%.1f", s/n }' "$LEDGER")"
    _j5_pk="$(awk -F'\t'  'NR>1 && $2=="1" && $5!="NA" { s += $5; n++ } END { if (n) printf "%.1f", s/n }' "$LEDGER")"
    if [ -z "$_j5_off" ] || [ -z "$_j5_pk" ]; then
        soft "J5 数据不足：台账里缺 $([ -z "$_j5_off" ] && printf '非高峰' || printf '晚高峰')样本。
         §5.3 要求**必须包含一次晚高峰（19:00–24:00 CST）采样** ——
         在那个时段带 --peak 再跑一次。"
        return 0
    fi
    _j5_r="$(awk -v p="$_j5_pk" -v o="$_j5_off" 'BEGIN { if (o > 0) printf "%.2f", p / o; else print "NA" }')"
    log "  J5 非高峰均值 ${_j5_off} ms / 晚高峰均值 ${_j5_pk} ms → 比值 ${_j5_r}×"
    if [ "$_j5_r" != "NA" ] && fgt "$_j5_r" "$J5_PEAK_RATIO"; then
        soft "J5 晚高峰劣化 ${_j5_r}× > ${J5_PEAK_RATIO}× —— 警告不否决。
         这是**链路属性不是 IP 属性，换 IP 通常救不了**：POMACS 2020 实测
         71% 的瓶颈跳位于中国境内纵深，香港带来的是最好的非高峰延迟，
         不是对高峰劣化的免疫。"
    else
        ok "J5 晚高峰劣化 ${_j5_r}×"
    fi
}

# J6 · 证书链签发者必须是 Let's Encrypt
j6_check() {
    step "J6 · 证书链签发者"
    if [ "$DRY_RUN" = 1 ]; then
        node_exec "openssl x509 -in /etc/bp/certs/fullchain.pem -noout -issuer"
        return 0
    fi
    _j6="$(node_exec 'sudo openssl x509 -in /etc/bp/certs/fullchain.pem -noout -issuer -enddate' 2>/dev/null || true)"
    if [ -z "$_j6" ]; then
        soft "J6 读不到 /etc/bp/certs/fullchain.pem（装机 step cert 还没跑？）"
        return 0
    fi
    printf '%s\n' "$_j6" | sed 's/^/    /'
    if printf '%s' "$_j6" | grep -qi "let's encrypt"; then
        ok "J6 证书由 Let's Encrypt 签发"
    else
        fail "J6 🔴 证书不是 Let's Encrypt。若是 Google Trust Services（WE1/WR2/WR3），
         失效模式是**中国方向 IP 级单向丢包**，不是握手失败 ——
         「能握手」不能证明证书没问题。回装机 step cert 重签。"
    fi
}

# ---------------------------------------------------------------------------
# 验收产出
# ---------------------------------------------------------------------------
write_readme() {
    _wr="${EVIDENCE_DIR}/README.md"
    [ "$DRY_RUN" = 1 ] && return 0
    _wr_ip="$(gcloud compute instances describe "$NODE" --project="$PROJECT" --zone="$ZONE" \
        --format='value(networkInterfaces[0].accessConfigs[0].natIP)' 2>/dev/null || echo '未知')"
    cat >"$_wr" <<README
# 路由验收证据 · ${NODE}

> 日期：$(date '+%Y-%m-%d') · 性质：**证据型核查** · 状态：**自动部分已跑**（$(date '+%Y-%m-%d')）
> 关联：docs/04-ops/node-provisioning.md §5、docs/05-adr/0004-transport-hardening.md §3.5

节点外网 IP：\`${_wr_ip}\`

## 证明什么

- 数据源 A（节点本机 mtr / ping 打中国三网）的**出向**路径与延迟、丢包。
- J1（跨洋绕行）、J2（中位 RTT）、J3（丢包）、J6（证书签发者）的自动判定结果。
- 全部原始输出（**含不合格样本**）都在本目录，未做筛选。

## 不证明什么

- **不证明入向路径没问题。** 中国方向非对称路由是常态，入向由中国运营商的
  BGP 决策决定，我们完全无法控制 —— 这正是「移动绕美」现象的成因。
  入向只能靠数据源 B（国内公开多点测速站）与 C（运维自己的国内机器）。
- **不证明用户体验没问题。** 采样点数远少于 §5.3 要求的「三网各 ≥ 5 个探测点
  （华北/华东/华南/西南/华中 各 ≥ 1）」。
- **J1–J6 六条判据全部是「设定值」**，不来自任何测量。第一批节点跑完必须整体重标定。
- J2 的口径有一处已知偏差：mtr 报的是**算术平均**不是中位数，
  所以本脚本改用 ping 的逐包值自算中位数。这一点要回写进 §5.2。

## 还要人工补的

- [ ] 数据源 B：国内公开多点 ping / traceroute 的截图或导出，标明采集时刻与工具
- [ ] 数据源 C：运维自己的国内机器实测（**测之前必须退出代理客户端进程**）
- [ ] 一次晚高峰（19:00–24:00 CST）采样：\`verify-route.sh --peak\`
- [ ] 一句话结论：\`IP ${_wr_ip} 于 <时刻> 在 J1–J6 全部通过 / J4 警告\`
- [ ] **换过几次 IP、每次为什么不合格** —— 这是评估「同区域 IP 段差异是否真实存在」
      的唯一数据来源，也是将来复审那条代价的依据

## 采样台账

见 \`samples.tsv\`（iso_time / peak / target / operator / median_ms / loss_pct）。
README
    ok "已写入 $_wr"
}

manual_gate() {
    step "数据源 B / C · 人工部分（脚本自动不了，也不该假装自动了）"
    log "  B. 国内公开多点测速站（itdog.cn / ping.pe 之类，**当前可用性待核实**）"
    log "     —— 这是**唯一能拿到入向数据的来源**。导出或截图，标明采集时刻与工具。"
    log "  C. 运维自己的国内机器：真实用户视角，样本 = 1。"
    log "     🔴 测之前**退出代理客户端进程**（不是切「直连」模式）。"
    log ""
    log "  采样要求（§5.3）：三网各 ≥ 5 个探测点（华北/华东/华南/西南/华中 各 ≥ 1），"
    log "  且**必须包含一次晚高峰（19:00–24:00 CST）采样**。"
    if [ "$MANUAL_DONE" = 1 ]; then
        ok "已声明 --manual-done：B/C 完成且证据已入 ${EVIDENCE_DIR}/"
        return 0
    fi
    MANUAL_FAIL=1
    printf '  [FAIL] %s\n' "B/C 未完成（没加 --manual-done）。**只跑 A 就宣布验收通过是错的** ——
       A 好看而 B 难看完全可能发生，而用户体验由 B 决定。" >&2
}

verdict() {
    step "结论"
    log "  测量硬判据失败：${HARD_FAILS}   软警告：${SOFT_WARNS}   B/C 未完成：${MANUAL_FAIL}"
    log "  证据目录：${EVIDENCE_DIR}"
    if [ "$HARD_FAILS" -gt 0 ]; then
        log ""
        log "  🔴 **IP 不合格。** 处置：换 IP 重测（约 1 分钟一轮，不重建实例）："
        log "     ./rotate-ip.sh --node ${NODE} --reason '<哪条判据不过>'"
        log "  换完回到本脚本重跑。**失败样本要留在 evidence/ 里，不要只留合格的那个** ——"
        log "  「换过几次、每次为什么不合格」是评估同区域 IP 段差异是否真实存在的唯一数据来源。"
        return 1
    fi
    if [ "$MANUAL_FAIL" = 1 ]; then
        log ""
        log "  ⚠️ 自动判据（J1/J2/J3/J6）全过，但**验收尚未完成**：数据源 B/C 还没做。"
        log "  🔴 换 IP 救不了这个 —— 缺的是入向数据与真实用户视角，去补采样，"
        log "     补完带 --manual-done 重跑本脚本。"
        return 1
    fi
    log ""
    log "  ✅ 自动判据全过，且 B/C 已声明完成。"
    log "  请把一句话结论写进 ${EVIDENCE_DIR}/README.md：「IP <地址> 于 <时刻> 在 J1–J6 全部通过」。"
    return 0
}

main() {
    warn_local_tun
    log ""
    log "  ⚠️ 阈值全部是「设定值」，不来自任何测量（§5.3）："
    log "     J1 相邻跳跃升 > ${J1_JUMP_MS} ms 硬否决 · J2 中位 RTT > ${J2_RTT_MS} ms 硬否决"
    log "     J3 丢包 > ${J3_LOSS_PCT}% 硬否决 · J4 极差 > ${J4_SPREAD_MS} ms 警告"
    log "     J5 高峰劣化 > ${J5_PEAK_RATIO}× 警告 · J6 证书必须是 Let's Encrypt"
    log "     第一批节点跑完必须整体重标定。"

    measure
    j4_check
    j5_check
    j6_check
    write_readme
    manual_gate
    verdict
}

main

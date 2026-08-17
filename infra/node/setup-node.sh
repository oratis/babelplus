#!/usr/bin/env bash
# =============================================================================
# setup-node.sh · bp 节点装机（在**节点上**以 root 运行，幂等，分步）
#
# 事实源：docs/04-ops/node-provisioning.md §4（本脚本是它 9 步的可执行形式）
#         docs/05-adr/0004-transport-hardening.md §3.1/§3.3/§3.4（BBR / mux / LE 钉扎）
#         docs/05-adr/0007-node-migration.md §4.2（内存预算）、§7.2（不装 cloudflared）
#
# -----------------------------------------------------------------------------
# 怎么跑：凭据走 stdin，**不进命令行、不进 shell history、不进实例 metadata**
# -----------------------------------------------------------------------------
#   set -a; source ~/.secrets/bp-node-hk1.env; set +a       # 该文件 chmod 600 且已 gitignore
#   {
#     for v in BP_PANEL_URL BP_NODE_ID BP_NODE_TOKEN BP_CERT_DOMAIN \
#              BP_ACME_EMAIL BP_SS_PORT CF_Token CF_Account_ID BP_V2NODE_VERSION; do
#       printf 'export %s=%q\n' "$v" "${!v:?缺少环境变量 $v}"
#     done
#     cat ./setup-node.sh
#   } | gcloud compute ssh bp-node-hk1 --project=oratis-491316 --zone=asia-east2-a \
#         --tunnel-through-iap --command="sudo bash -s -- --all"
#
#   ${!v:?} 是 fail-closed：缺任何一个变量在连上机器之前就退出，不生成半成品配置。
#   printf %q 保证含特殊字符的密码不会被 shell 二次解释。
#   ⚠️ 不要把凭据写进实例 metadata —— metadata 对项目内任何有读权限的主体可见，
#      而 oratis-491316 是共享项目（as-built §8 的软隔离取舍）。
#
# -----------------------------------------------------------------------------
# 🔴 版本地雷（每次建机与每次升级都要重读一遍）
# -----------------------------------------------------------------------------
# ① mihomo 已**放弃**与 Xray ≥ v26.7.11 的 REALITY 兼容。官方原话：
#      "Due to xray-core's deliberately incompatible behavior, we will not consider
#       compatibility with xray v26.7.11+ versions."
#    **mihomo 是 Clash Verge Rev 的内核** —— 服务端 xray-core 版本直接决定一大批
#    客户端能否连上 REALITY，且 mihomo 明确表示不会修。
#    本项目不单独装 Xray：v2node 是「改版 xray-core」，xray-core 是它 vendor 的依赖。
#    所以真实形态是：**v2node 的某次版本升级可能在无任何提示的情况下把 vendored
#    xray-core 带过 v26.7.11，于是所有 Clash Verge Rev 用户在下次节点重启后集体失联。**
#    → 处置：BP_V2NODE_VERSION 必须钉死（绝不用 latest）；升级前先查 go.mod 里
#      xtls/xray-core 的版本；升级前用真实 mihomo 客户端回归测一次 REALITY 连通性。
#    另注：Xray v26.4.x–v26.7.28 **均以 prerelease 发布**，任何「取 latest release」
#    的自动化都会踩坑；当前最新非预发布版是 v26.3.27（2026-03-27）。
#
# ② Xray 配置字段已改名，且**保留静默别名** —— 写错不报错，只是行为不符预期。
#    这是本项目最容易产生「查不出来的 bug」的一处（源码里有
#    `if c.Clients != nil { c.Users = c.Clients }` 这样的兼容分支）。
#      streamSettings.network            → streamSettings.method
#      method 取值 tcp (+tcpSettings)    → raw (+rawSettings)
#      settings.clients (VLESS inbound)  → settings.users
#      realitySettings.dest  (inbound)   → realitySettings.target
#      realitySettings.publicKey (outbound) → realitySettings.password
#    → 处置：见 step 9 的 check_field_renames()，任何配置里出现旧名一律视为缺陷。
#    补一句：publicKey → password 不是美学改名。该字段确实是 x25519 公钥，
#    但在 REALITY 的设计里它是**客户端持有的秘密，持有它即可探测 REALITY 服务器**。
#    叫它 publicKey 会诱导用户随手分享。
#
# ③ 其余三条同样造成「查不出来的失败」：
#    - mihomo 的 client-fingerprint（uTLS 指纹，取值 chrome/iOS 等，**大小写敏感**）
#      与 fingerprint（**证书 SHA-256 pin**）是完全不同的东西；
#    - sing-box 的 obfs `gecko` 只在开发线 1.14 文档里存在，1.13.18 只认 `salamander`；
#    - Hysteria2 的 userpass 认证在 sing-box 里没有别名，必须输出 "password": "user:pass"。
#
# -----------------------------------------------------------------------------
# 用法见 --help。一切改动都支持 --dry-run。
# =============================================================================
set -euo pipefail
umask 077

# ---------------------------------------------------------------------------
# 0 · 步骤表（编号沿用 node-provisioning §4 的 [N/9]，不重排）
# ---------------------------------------------------------------------------
# 1 sysctl     网络栈调优（BBR / fq / mtu_probing / fastopen / 缓冲区）
# 2 baseline   系统基线（apt / 时间同步 / journald 上限 / 可选 swap）
# 3 cert       证书：acme.sh + **显式钉 Let's Encrypt** + DNS-01
#              （排在 v2node 之前，是因为 step 6 的 LoadCredential 要求证书文件先存在）
# 4 v2node     安装 v2node（版本钉死 + sha256 记录 + 面板坐标配置）
# 5 transport  三通路策略：REALITY / Hysteria2(**BBR** 不是 Brutal) / SS-2022 + **TCP 路径 mux**
# 6 systemd    单元与硬化（LoadCredential + DynamicUser + ProtectSystem=strict + NoNewPrivileges）
# 7 upgrades   unattended-upgrades（**Automatic-Reboot false**）
# 8 ssh        SSH 加固（drop-in + sshd -t + reload）
# 9 selfcheck  自检（含字段改名 grep 与 BBR 生效核对）
STEPS="sysctl baseline cert v2node transport systemd upgrades ssh selfcheck"

BP_ETC="${BP_ETC:-/etc/bp}"
BP_CERT_DIR="${BP_ETC}/certs"
BP_UNIT="bp-node.service"

DRY_RUN=0
ASSUME_YES=0
RUN_STEPS=""
ENABLE_SWAP="${BP_ENABLE_SWAP:-1}"
NEED_RESTART=0
FILE_CHANGED=0
FAILED_CHECKS=0

# ---------------------------------------------------------------------------
# 1 · 输出、守卫、dry-run
# ---------------------------------------------------------------------------
log()  { printf '%s\n' "$*"; }
step() { printf '\n=== [%s] %s\n' "$1" "$2"; }
ok()   { printf '  [ok]   %s\n' "$*"; }
warn() { printf '  [warn] %s\n' "$*" >&2; }
bad()  { printf '  [FAIL] %s\n' "$*" >&2; FAILED_CHECKS=$((FAILED_CHECKS + 1)); }
die()  { printf '\n[FATAL] %s\n' "$*" >&2; exit 1; }

# redact · dry-run 打印命令时抹掉凭据值。
# 凭据永远不作为命令行参数传给任何程序（jq 走 env.*，acme.sh 走导出的环境变量），
# 这个函数是最后一道保险，防止将来有人加了带凭据的命令还开着 --dry-run 贴日志。
REDACTED_MARK='***REDACTED***'
redact() {
    _rd_s="$1"
    for _rd_n in BP_NODE_TOKEN BP_SS_PSK BP_HY2_OBFS_PASSWORD CF_Token CF_Account_ID; do
        _rd_v="${!_rd_n:-}"
        if [ -n "$_rd_v" ]; then
            _rd_s="${_rd_s//"$_rd_v"/"$REDACTED_MARK"}"
        fi
    done
    printf '%s' "$_rd_s"
}

run() {
    if [ "$DRY_RUN" = 1 ]; then
        _run_out=""
        for _run_a in "$@"; do
            _run_out="$_run_out $(printf '%q' "$_run_a")"
        done
        printf '  [dry-run]%s\n' "$(redact "$_run_out")"
        return 0
    fi
    "$@"
}

confirm_typed() {
    _ct_expect="$1"
    _ct_what="$2"
    if [ "$ASSUME_YES" = 1 ]; then
        warn "--yes 已跳过确认：$_ct_what"
        return 0
    fi
    if [ ! -t 0 ]; then
        # 装机默认从 stdin 灌进来，stdin 就不是终端 —— 所以这里必须显式指路，
        # 不能默默放行（默默放行等于没有二次确认）。
        die "需要交互确认但 stdin 不是终端：$_ct_what
      装机脚本本来就走 stdin，因此这类操作请显式加 --yes 表示你已经读过后果。"
    fi
    printf '\n  ⚠️  %s\n  确认请原样输入 %s ：' "$_ct_what" "$_ct_expect"
    _ct_ans=""
    read -r _ct_ans </dev/tty || true
    [ "$_ct_ans" = "$_ct_expect" ] || die "确认串不匹配，已中止。"
}

# write_file · 幂等写文件。内容从 stdin 读；无变化则不写、不触发重启。
# 结果写进全局 FILE_CHANGED（0=无变化 1=已变更），返回值恒为 0。
write_file() {
    _wf_path="$1"
    _wf_mode="$2"
    FILE_CHANGED=0
    _wf_tmp="$(mktemp)"
    cat >"$_wf_tmp"
    if [ -f "$_wf_path" ] && cmp -s "$_wf_tmp" "$_wf_path"; then
        rm -f "$_wf_tmp"
        ok "未变更：$_wf_path"
        return 0
    fi
    if [ "$DRY_RUN" = 1 ]; then
        log "  [dry-run] 将写入 $_wf_path (mode $_wf_mode)："
        if [ -f "$_wf_path" ]; then
            diff -u "$_wf_path" "$_wf_tmp" | sed 's/^/      /' || true
        else
            sed 's/^/      /' "$_wf_tmp"
        fi
        rm -f "$_wf_tmp"
        FILE_CHANGED=1
        return 0
    fi
    install -D -m "$_wf_mode" "$_wf_tmp" "$_wf_path"
    rm -f "$_wf_tmp"
    FILE_CHANGED=1
    ok "已写入：$_wf_path"
}

usage() {
    cat <<'USAGE'
用法：setup-node.sh [--all | --step NAME ... | --from NAME] [选项]

  在 **bp 节点上以 root** 运行的幂等装机脚本。每一步都可以单独重跑，结果相同。
  开头有硬性守卫：主机名是 vpn-us / vpn-jp / vpn-* 时**立即退出**。

步骤（顺序即依赖顺序）
  sysctl baseline cert v2node transport systemd upgrades ssh selfcheck

选项
  --all             跑全部步骤（默认；等价于不加任何步骤参数）
  --step NAME       只跑指定步骤，可重复
  --from NAME       从指定步骤开始跑到最后
  --list            列出步骤后退出
  --no-swap         不建 swapfile（默认建 1 GB，见下方「未经裁决的提案」）
  --dry-run         只打印将做的改动，不落任何一个字节
  --yes             跳过交互确认
  -h, --help        本帮助

必需环境变量（fail-closed，缺一即退出；按所选步骤检查）
  cert       BP_CERT_DOMAIN BP_ACME_EMAIL CF_Token CF_Account_ID
  v2node     BP_PANEL_URL BP_NODE_ID BP_NODE_TOKEN BP_V2NODE_VERSION
  transport  BP_SS_PORT
  selfcheck  BP_SS_PORT

可选环境变量
  BP_REALITY_TARGET   REALITY 的 target 站点域名；设了就在本机实测一次可达性与 TLS1.3
  BP_ACME_NO_PERSIST  =1 则签发后从 acme.sh 的 account.conf 抹掉 CF token
                      （代价：自动续期会失效，见 README 代价）
  BP_ENABLE_SWAP      =0 等价于 --no-swap

🔴 **不需要**传给节点的（传了只会白白扩大凭据暴露面）
  BP_SS_PSK / BP_HY2_OBFS_PASSWORD / REALITY 的 privateKey / shortId
  —— 按 node-provisioning §4.4，这些协议参数**全部由面板经
     GET /api/v1/server/UniProxy/config 下发**，节点本地配置只有面板坐标与密钥。
     「配置 REALITY / HY2 / SS」这件事有八成发生在面板里，不在这台机器上。
USAGE
}

# ---------------------------------------------------------------------------
# 2 · 参数解析
# ---------------------------------------------------------------------------
while [ $# -gt 0 ]; do
    case "$1" in
        --all)      RUN_STEPS="$STEPS"; shift ;;
        --step)     RUN_STEPS="$RUN_STEPS ${2:?--step 需要值}"; shift 2 ;;
        --from)
            _from="${2:?--from 需要值}"
            _acc=""; _hit=0
            for _s in $STEPS; do
                [ "$_s" = "$_from" ] && _hit=1
                [ "$_hit" = 1 ] && _acc="$_acc $_s"
            done
            [ "$_hit" = 1 ] || die "--from 的步骤名不存在：$_from"
            RUN_STEPS="$RUN_STEPS $_acc"
            shift 2 ;;
        --list)     printf '%s\n' "$STEPS" | tr ' ' '\n'; exit 0 ;;
        --no-swap)  ENABLE_SWAP=0; shift ;;
        --dry-run)  DRY_RUN=1; shift ;;
        --yes|-y)   ASSUME_YES=1; shift ;;
        -h|--help)  usage; exit 0 ;;
        *)          usage >&2; die "未知参数：$1" ;;
    esac
done
[ -n "$RUN_STEPS" ] || RUN_STEPS="$STEPS"

# --step 的取值必须真实存在：打错一个字就「什么都没装却退出码 0」是最坏的失败形式。
for _rs in $RUN_STEPS; do
    case " $STEPS " in
        *" $_rs "*) : ;;
        *) die "--step 的值 '$_rs' 不是有效步骤。可选：$STEPS" ;;
    esac
done

want() {
    case " $RUN_STEPS " in *" $1 "*) return 0 ;; *) return 1 ;; esac
}

# ---------------------------------------------------------------------------
# 3 · 守卫：绝不能在 vpn-us / vpn-jp 上运行
# ---------------------------------------------------------------------------
# 这台脚本从 stdin 灌进 `sudo bash -s`，一旦 gcloud compute ssh 的目标名敲错，
# 它就会在现役节点上跑 —— 而它会改 sysctl / sshd / systemd。
# 所以守卫放在最前面，且既查主机名也查 GCE metadata 的 instance name。
assert_not_legacy_host() {
    _al_h="$(hostname -s 2>/dev/null || hostname 2>/dev/null || echo unknown)"
    _al_m=""
    if command -v curl >/dev/null 2>&1; then
        # --noproxy '*' 是必需的：不能让任何继承来的代理变量把 metadata 请求带走。
        _al_m="$(curl -fsS --noproxy '*' --max-time 2 \
            -H 'Metadata-Flavor: Google' \
            'http://169.254.169.254/computeMetadata/v1/instance/name' 2>/dev/null || true)"
    fi
    for _al_n in "$_al_h" "$_al_m"; do
        [ -n "$_al_n" ] || continue
        case "$_al_n" in
            vpn-us|vpn-jp|vpn-*|vpn_*)
                die "🔴 检测到当前主机是 '$_al_n' —— 这是现役代理节点（as-built §2）。
      本脚本会改 sysctl / sshd / systemd，在 vpn-* 上运行等于改动已部署服务。
      立即中止。请核对 gcloud compute ssh 的目标名。" ;;
        esac
    done
    case "$_al_h" in
        bp-*) ok "主机名 $_al_h（bp- 前缀）" ;;
        *)    warn "主机名 '$_al_h' 不是 bp- 前缀。不阻断，但请确认这台机器是你要装的那台。" ;;
    esac
    [ -n "$_al_m" ] && ok "GCE instance name = $_al_m"
    return 0
}

# require_env · fail-closed 的环境变量检查。**在任何变更之前**一次性检查完
# 本次要跑的所有步骤所需的变量，避免跑到第 5 步才发现缺变量、留下半成品配置。
env_for_step() {
    case "$1" in
        cert)      printf 'BP_CERT_DOMAIN BP_ACME_EMAIL CF_Token CF_Account_ID' ;;
        v2node)    printf 'BP_PANEL_URL BP_NODE_ID BP_NODE_TOKEN BP_V2NODE_VERSION' ;;
        transport) printf 'BP_SS_PORT' ;;
        selfcheck) printf 'BP_SS_PORT' ;;
        *)         printf '' ;;
    esac
}

preflight_env() {
    _pe_missing=""
    for _pe_s in $RUN_STEPS; do
        for _pe_v in $(env_for_step "$_pe_s"); do
            if [ -z "${!_pe_v:-}" ]; then
                case " $_pe_missing " in
                    *" $_pe_v "*) : ;;
                    *) _pe_missing="$_pe_missing $_pe_v" ;;
                esac
            fi
        done
    done
    [ -z "$_pe_missing" ] || die "缺少环境变量（fail-closed，不生成半成品配置）：$_pe_missing
      传法见脚本头部注释：凭据走 stdin 的 export 语句，不进命令行。"

    # 反向提醒：这三个变量传到节点上是**多余的暴露面**。
    for _pe_v in BP_SS_PSK BP_HY2_OBFS_PASSWORD BP_REALITY_PRIVATE_KEY; do
        if [ -n "${!_pe_v:-}" ]; then
            warn "$_pe_v 已传入，但节点侧用不到它 —— 协议参数由面板下发（§4.4）。
      建议从 .env 的传递列表里去掉，减少凭据暴露面。"
        fi
    done

    if [ -n "${BP_SS_PORT:-}" ]; then
        case "$BP_SS_PORT" in
            ''|*[!0-9]*) die "BP_SS_PORT 必须是纯数字。" ;;
        esac
        [ "$BP_SS_PORT" != "48882" ] || die "BP_SS_PORT 不得为 48882（那是 vpn-node 的端口，§4.5）。"
    fi
    if [ -n "${BP_V2NODE_VERSION:-}" ]; then
        case "$BP_V2NODE_VERSION" in
            latest|LATEST|"")
                die "BP_V2NODE_VERSION 不得为 latest。版本必须钉死 —— 见脚本头部版本地雷 ①。" ;;
        esac
    fi
    return 0
}

# ---------------------------------------------------------------------------
# step 1/9 · sysctl 调优
# ---------------------------------------------------------------------------
do_sysctl() {
    step 1/9 "sysctl 网络栈调优"
    # 逐字复用 Proxy_Skill 一手实测过的那份，一个字不改。
    #
    #   default_qdisc=fq + tcp_congestion_control=bbr
    #     —— 这是节点侧 BBR 的落点之一。注意源注释的限定：它**保留 GCE 现役的多队列
    #        qdisc**，这个 default 只作用于新建的单队列接口。
    #   tcp_mtu_probing=1
    #     —— 恢复 path-MTU 黑洞。跨境链路上 PMTU 黑洞表现为「握手成功但传大包卡死」。
    #   tcp_fastopen=3
    #     —— 客户端与服务端两侧都开。
    #   tcp_slow_start_after_idle=0
    #     —— 长连接空闲后不回到慢启动。代理连接空闲再唤醒是常态。
    #   rmem_max / wmem_max = 16 MB
    #     —— 这两条**同时服务 TCP 与 UDP**，是 Hysteria2 吞吐的前提。缓冲区不足时
    #        quic-go 会在启动日志里打印一条 receive buffer 警告（措辞**待核实**）——
    #        看到它就说明这份 sysctl 没生效，不要放过。
    #
    # ⚠️ 已经实测过：这套调优正确且无害，**但不要期待吞吐提升**。
    #    跨境单流的瓶颈是拥塞控制，不是缓冲区。写这一句是为了防止有人把它当性能手段反复加码。
    write_file /etc/sysctl.d/99-bp-network.conf 0644 <<'SYSCTL'
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
    if [ "$FILE_CHANGED" = 1 ]; then
        run sysctl --system
    fi
    if [ "$DRY_RUN" != 1 ]; then
        _ds_cc="$(sysctl -n net.ipv4.tcp_congestion_control 2>/dev/null || echo '?')"
        _ds_qd="$(sysctl -n net.core.default_qdisc 2>/dev/null || echo '?')"
        if [ "$_ds_cc" = "bbr" ]; then
            ok "tcp_congestion_control=bbr"
        else
            bad "tcp_congestion_control=$_ds_cc（期望 bbr）"
        fi
        if [ "$_ds_qd" = "fq" ]; then
            ok "default_qdisc=fq"
        else
            bad "default_qdisc=$_ds_qd（期望 fq）"
        fi
    fi
}

# ---------------------------------------------------------------------------
# step 2/9 · 系统基线
# ---------------------------------------------------------------------------
do_baseline() {
    step 2/9 "系统基线（包 / 时间 / 日志上限 / swap）"
    export DEBIAN_FRONTEND=noninteractive
    run apt-get update -qq
    run apt-get install -y --no-install-recommends \
        curl ca-certificates unzip jq socat mtr-tiny iproute2 openssl

    # 时间偏差是隐蔽的握手失败成因：多数协议对时间敏感。
    if [ "$DRY_RUN" != 1 ]; then
        if timedatectl show -p NTPSynchronized --value 2>/dev/null | grep -qx yes; then
            ok "时间已同步"
        else
            bad "🔴 时间未同步（NTPSynchronized != yes）—— 会造成难以定位的握手失败"
        fi
    fi

    # 20 GB 盘不能被 journald 吃光：pd-balanced 上 journald 写日志能把队列打满。
    write_file /etc/systemd/journald.conf.d/99-bp.conf 0644 <<'JOURNALD'
[Journal]
SystemMaxUse=200M
MaxRetentionSec=14day
JOURNALD
    [ "$FILE_CHANGED" = 1 ] && run systemctl restart systemd-journald

    run install -d -m 700 "$BP_ETC"
    run install -d -m 700 "$BP_CERT_DIR"

    # ---- swap：**未经任何 ADR 裁决的提案**，默认开，30 天后按实测决定去留 ----
    # 理由：内存耗尽在 Linux 上不是「变慢」，是 OOM killer 挑一个进程杀掉 ——
    #   现象（端口不再响应、日志戛然而止）与 IP 被封高度相似，会把排障指向封锁取证
    #   流程，浪费掉最宝贵的半小时。swap 把「瞬时全员掉线」换成「可观测的劣化」。
    # 代价：pd 上的 swap 极慢，用户侧表现为「时快时慢」，**比干脆掉线更难定位**，
    #   且 20 GB 盘要让出 1 GB。
    # 撤销条件写死：**30 天内 swap 使用始终为 0 就撤掉**，不要留着当护身符。
    if [ "$ENABLE_SWAP" = 1 ]; then
        if [ -f /swapfile ]; then
            ok "/swapfile 已存在"
        else
            run fallocate -l 1G /swapfile
            run chmod 600 /swapfile
            run mkswap /swapfile
            run swapon /swapfile
            if ! grep -q '^/swapfile' /etc/fstab 2>/dev/null; then
                run sh -c 'printf "/swapfile none swap sw 0 0\n" >> /etc/fstab'
            fi
        fi
        write_file /etc/sysctl.d/99-bp-swap.conf 0644 <<'SWAPCTL'
vm.swappiness=10
SWAPCTL
        [ "$FILE_CHANGED" = 1 ] && run sysctl --system
    else
        log "  --no-swap：跳过 swapfile"
    fi

    # 🔴 不装 cloudflared，且绝不复制旧节点的 tunnel token（token 即凭据）。
    #    应急 CDN 通道是「默认关闭」的第四优先级，不属于 P1；
    #    且现有隧道的 Cloudflare 账号归属至今未确认（ADR 0007 §7.2）。
    if [ "$DRY_RUN" != 1 ] && command -v cloudflared >/dev/null 2>&1; then
        bad "🔴 本机装了 cloudflared —— ADR 0007 §7.2 明确第一阶段不装。请人工核实来源。"
    fi
}

# ---------------------------------------------------------------------------
# step 3/9 · 证书：钉 Let's Encrypt，禁 GTS
# ---------------------------------------------------------------------------
# 谁需要真证书（教程里常被搞混）：
#   VLESS + XTLS-Vision + REALITY  ❌ 不需要 —— REALITY 借用 target 站点的真实 TLS
#                                     握手，服务端不持有自己的证书
#   Hysteria2                      ✅ 需要 —— tls 是必填项；生产环境**不得** insecure: true
#   SS-2022                        ❌ 不需要 —— 非 TLS
# 所以 ADR 0004 §3.4「必须钉 LE、禁用 GTS」在节点上的落点**只有 Hysteria2 一处**。
#
# 🔴 GTS 的失效模式是**单向丢包**不是握手失败（net4people #381：
#    "it is the IP that is blocked"、"packet dropping, not RST injection"）——
#    所以「能握手」不能证明证书没问题，**必须直接看 issuer**。
do_cert() {
    step 3/9 "证书：acme.sh + 显式钉 Let's Encrypt + DNS-01"
    # sudo 是否重置 HOME 取决于发行版的 sudoers 配置（env_reset / always_set_home），
    # 而 acme.sh 的安装位置完全由 $HOME 决定 —— 依赖它就是把「证书装在哪」交给运气。
    # 这里显式钉死，重跑时才能找到上次装的那一份，幂等才成立。
    export HOME="${BP_ACME_BASE:-/root}"
    _dc_home="${HOME}/.acme.sh"
    _dc_acme="${_dc_home}/acme.sh"

    if [ ! -x "$_dc_acme" ]; then
        # acme.sh 的安装脚本来自网络。这是一条供应链依赖，记在 README 代价里。
        run sh -c "curl -fsSL https://get.acme.sh | sh -s email=$(printf '%q' "$BP_ACME_EMAIL")"
    else
        ok "acme.sh 已安装：$_dc_acme"
    fi

    # 🔴 --server letsencrypt 必须**显式**写。acme.sh 的默认 CA 在版本之间变过，
    #    依赖默认值就是把 ADR 0004 §3.4 的裁决交给上游决定。
    run "$_dc_acme" --set-default-ca --server letsencrypt

    # DNS-01，且**不给这个域名建 A 记录**：订阅里节点地址填 IP、sni 填证书域名，
    # 客户端不做 DNS 解析，域名只存在于证书里。这样它既不需要解析、
    # 也不会因为被封而影响连接。
    # CF_Token / CF_Account_ID 由环境变量传入，**不出现在命令行**（acme.sh 直接读 env）。
    if [ "$DRY_RUN" = 1 ]; then
        log "  [dry-run] $_dc_acme --issue --dns dns_cf -d <BP_CERT_DOMAIN> --keylength ec-256"
    elif [ -d "${_dc_home}/${BP_CERT_DOMAIN}_ecc" ]; then
        ok "证书目录已存在，跳过签发（续期由 acme.sh 的 cron 负责）"
    else
        "$_dc_acme" --issue --dns dns_cf -d "$BP_CERT_DOMAIN" --keylength ec-256
    fi

    run "$_dc_acme" --install-cert -d "$BP_CERT_DOMAIN" --ecc \
        --fullchain-file "${BP_CERT_DIR}/fullchain.pem" \
        --key-file       "${BP_CERT_DIR}/privkey.pem" \
        --reloadcmd      "systemctl restart ${BP_UNIT}"

    # ⚠️ acme.sh 会把 CF_Token 存进 ~/.acme.sh/account.conf 以便续期 —— 这是
    #    「凭据不落盘」的一个真实例外，代价写在 README。BP_ACME_NO_PERSIST=1 可抹掉，
    #    但那样**自动续期会失效**，续期时必须重新注入 token。
    if [ "${BP_ACME_NO_PERSIST:-0}" = "1" ] && [ -f "${_dc_home}/account.conf" ]; then
        run sed -i '/^SAVED_CF_Token=/d;/^SAVED_CF_Account_ID=/d' "${_dc_home}/account.conf"
        warn "已从 account.conf 抹掉 CF token —— **自动续期从此失效**，续期需手工注入。"
    fi
    run chmod 700 "$_dc_home" || true

    check_cert_issuer
}

check_cert_issuer() {
    _ci_f="${BP_CERT_DIR}/fullchain.pem"
    if [ "$DRY_RUN" = 1 ] || [ ! -f "$_ci_f" ]; then
        log "  （dry-run 或证书尚不存在，跳过 issuer 核对）"
        return 0
    fi
    _ci_issuer="$(openssl x509 -in "$_ci_f" -noout -issuer 2>/dev/null || echo '')"
    _ci_end="$(openssl x509 -in "$_ci_f" -noout -enddate 2>/dev/null || echo '')"
    log "  issuer : $_ci_issuer"
    log "  expires: $_ci_end"
    if printf '%s' "$_ci_issuer" | grep -qi "let's encrypt"; then
        ok "证书由 Let's Encrypt 签发（J6 通过）"
    else
        bad "🔴 证书不是 Let's Encrypt 签发。若是 Google Trust Services（WE1/WR2/WR3），
      失效模式是**中国方向的 IP 级单向丢包**，排障时会被误判成网络抖动。
      回到 step cert 重签，并核对 --set-default-ca --server letsencrypt 是否生效。"
    fi
}

# ---------------------------------------------------------------------------
# step 4/9 · v2node
# ---------------------------------------------------------------------------
do_v2node() {
    step 4/9 "安装 v2node（版本钉死 ${BP_V2NODE_VERSION}）"
    # 选 v2node（wyx2685/v2node，MPL-2.0）而非 XrayR（已废弃、源码被删）、
    # V2bX（已归档）、soga（闭源 + USDT 授权 + 绑定域名）。
    _v4_arch="$(uname -m)"
    case "$_v4_arch" in
        x86_64|amd64)   _v4_t="64" ;;
        aarch64|arm64)  _v4_t="arm64-v8a" ;;   # arm64 只为本机 --dry-run 演练；GCE Debian 报 aarch64
        *)              die "未知架构 $_v4_arch，无法选择 v2node 发行包。" ;;
    esac
    _v4_url="https://github.com/wyx2685/v2node/releases/download/${BP_V2NODE_VERSION}/v2node-linux-${_v4_t}.zip"

    if [ "$DRY_RUN" = 1 ]; then
        log "  [dry-run] curl -fsSLo /tmp/v2node.zip $_v4_url"
        log "  [dry-run] unzip → install -m 755 /usr/local/bin/v2node"
    else
        _v4_have=""
        [ -x /usr/local/bin/v2node ] && _v4_have="$(cat "${BP_ETC}/v2node.version" 2>/dev/null || true)"
        if [ "$_v4_have" = "$BP_V2NODE_VERSION" ]; then
            ok "v2node ${BP_V2NODE_VERSION} 已安装，跳过下载"
        else
            curl -fsSLo /tmp/v2node.zip "$_v4_url"
            # sha256 记录下来，下次升级要对比 —— 这是「版本钉死」唯一可验证的形式。
            sha256sum /tmp/v2node.zip | tee "${BP_ETC}/v2node.sha256"
            rm -rf /tmp/v2node && mkdir -p /tmp/v2node
            unzip -o /tmp/v2node.zip -d /tmp/v2node >/dev/null
            install -m 755 /tmp/v2node/v2node /usr/local/bin/v2node
            printf '%s\n' "$BP_V2NODE_VERSION" >"${BP_ETC}/v2node.version"
            rm -rf /tmp/v2node /tmp/v2node.zip
            ok "已安装 v2node ${BP_V2NODE_VERSION}"
            NEED_RESTART=1
        fi
    fi

    # 节点本地配置**只有面板坐标，没有协议参数** —— UniProxy 架构最容易被误解的一点。
    # 字段名 **待核实**：必须以所钉 tag 的 config.json.example 为准。
    # NodeID 用数字还是字符串同样 **待核实**，这里按数字下发。
    # 用 jq 的 env.* 读环境变量：凭据**不进 argv**，因此不会出现在节点的 ps 输出里。
    if [ "$DRY_RUN" = 1 ]; then
        log "  [dry-run] 生成 ${BP_ETC}/v2node.json（ApiHost/ApiKey/NodeID/NodeType/Timeout）"
    else
        command -v jq >/dev/null 2>&1 || die "缺 jq（step baseline 会装）。"
        _v4_tmp="$(mktemp)"
        jq -n '{
          Nodes: [{
            ApiHost:  env.BP_PANEL_URL,
            ApiKey:   env.BP_NODE_TOKEN,
            NodeID:   (env.BP_NODE_ID | tonumber),
            NodeType: "v2node",
            Timeout:  30
          }]
        }' >"$_v4_tmp"
        if [ -f "${BP_ETC}/v2node.json" ] && cmp -s "$_v4_tmp" "${BP_ETC}/v2node.json"; then
            ok "未变更：${BP_ETC}/v2node.json"
        else
            install -D -m 600 "$_v4_tmp" "${BP_ETC}/v2node.json"
            ok "已写入：${BP_ETC}/v2node.json（0600，含明文节点密钥）"
            NEED_RESTART=1
        fi
        rm -f "$_v4_tmp"
    fi
}

# ---------------------------------------------------------------------------
# step 5/9 · 三条通路的策略
# ---------------------------------------------------------------------------
# 🔴 读这一段之前先接受一个事实：**这台机器上几乎没有协议参数可配。**
#    REALITY 的 privateKey / serverName / target / shortId、Hysteria2 的
#    obfs / obfs-password / up_mbps / down_mbps、SS 的 cipher / server_key，
#    全部由面板经 GET /api/v1/server/UniProxy/config 下发。
#    节点侧能写错的只有面板地址与密钥（step 4 已处理）。
#
# 那么本步骤做三件事：
#   ① 把**意图**落成一份不含凭据的 transport-policy.json，让下一个人知道该配什么；
#   ② 把 mux 策略写清楚（它是订阅生成器的事，不是装机的事，但必须有人记着）；
#   ③ 在节点上实测 REALITY 的 target 站点是否合格（这是 §4.5 要求「在节点上实测一次」的）。
do_transport() {
    step 5/9 "三通路策略：REALITY / Hysteria2(BBR) / SS-2022 + TCP 路径 mux"

    write_file "${BP_ETC}/transport-policy.json" 0644 <<'POLICY'
{
  "_comment": "意图记录，不含任何凭据。真正生效的协议参数由面板经 UniProxy/config 下发。",
  "_source": "docs/04-ops/node-provisioning.md §4.5 + docs/05-adr/0004-transport-hardening.md",

  "reality": {
    "_path": "TCP 443 · VLESS + XTLS-Vision + REALITY · 默认主力",
    "mux": "enabled",
    "_mux_why": "ADR 0004 §3.3：USENIX Security 2024 实测，每条代理连接哪怕只承载 2 条应用流，TLS-in-TLS 指纹检测 TPR 就下降超过 70%；单条活跃流时无效。TCP 路径本来就受单流拥塞控制约束，mux 的额外吞吐损失有限，抗指纹收益巨大。",
    "_mux_who": "多路复用由**客户端发起**、服务端只是接受 —— 所以这条是订阅生成器的事，装机侧无开关。",
    "_mux_unresolved": "🔴 mux 与 XTLS-Vision 能否共存**未核实**。若两者互斥，ADR 0004 §3.3 与 system-design §3.1 必须放弃一个。判定方法：用真实 mihomo 与 sing-box 各加载一次订阅。",
    "_field_names": "生成器一律用新字段名：users / target / password / method / raw。旧名 clients / dest / publicKey / network:tcp 仍作为静默别名被接受，写错不报错。",
    "_private_key": "面板 tls_settings.private_key 填 `xray x25519` 输出的 **PrivateKey:** 行。Password (PublicKey): 是给客户端的；Hash32: 是 VLESS Encryption 用的，与 REALITY 无关，误填即不通。",
    "_target_site": "必须支持 TLS 1.3 + HTTP/2、无跳转、境外、非自家域名、在中国可正常访问，且从本节点可达且低延迟 —— 否则回落超时反而暴露。"
  },

  "hysteria2": {
    "_path": "UDP 443 · Hysteria2 + salamander obfs · 加速通路，不是主力",
    "mux": "disabled",
    "_mux_why": "QUIC 原生多路复用，且 HY2 的价值就是单流吞吐。ADR 0004 §3.3 明确 UDP 路径不启用 mux。",
    "congestion_control": "BBR",
    "up_mbps": null,
    "down_mbps": null,
    "_bbr_why": "🔴 这就是「用 BBR 不用 Brutal」的落点：Hysteria2 只有在带宽被**显式指定**时才用 Brutal，留空即回落到 BBR。放弃的是 55% 吞吐（1700 → 1094 KB/s），换掉的是一个 100% 可分的行为特征（FOCI 2025：10,080 条流，两级阈值分类器 100% 区分 loss-based 与 non-loss-based 拥塞控制）。",
    "_ignore_client_bandwidth": "服务端有一个能强制 BBR 的硬开关 ignoreClientBandwidth（字段名待核实），**我们刻意不开** —— ADR 0004 §3.1 允许用户手动选择「激进模式」。拥塞控制的决定权在客户端配置里，服务端只负责默认不下发带宽字段。这条要同步给订阅生成器，否则默认值会在订阅侧被写回去。",
    "obfs": "salamander",
    "_obfs_note": "sing-box 1.13 只认 salamander；gecko 仅开发线 1.14 文档有，下发它 = 客户端加载失败。",
    "port_hopping": "disabled",
    "_port_hopping_why": "社区实测端口跳跃没有帮助（apernet/hysteria #1267/#1380），开一万个 UDP 端口是净增攻击面换零收益。"
  },

  "shadowsocks2022": {
    "_path": "兜底通路，端口从 BP_SS_PORT 来",
    "cipher": "2022-blake3-aes-128-gcm",
    "_psk": "openssl rand -base64 16（16 字节），在面板侧生成与保管，**不下发到装机脚本**。",
    "_port_note": "端口不得复用旧节点的 48882 —— 那会把 bp 节点与 vpn-* 关联起来，且 allow-ss-48882 绑的是 vpn-node 标签，对 bp 节点根本不生效。"
  },

  "not_installed": {
    "cloudflared": "ADR 0007 §7.2：第一阶段一律不装，且绝不复制旧节点的 tunnel token（token 即凭据，账号归属至今未确认）。"
  }
}
POLICY

    # ③ REALITY target 站点的节点侧实测（§4.5 要求「在节点上实测一次」）。
    #    注意这里 curl 是**正当**的：节点上没有 TUN / fake-ip 客户端，
    #    而且测的是「本节点 → target 站点」这条出向路径，不是中国方向的连通性。
    if [ -n "${BP_REALITY_TARGET:-}" ] && [ "$DRY_RUN" != 1 ]; then
        log "  实测 REALITY target 站点：${BP_REALITY_TARGET}"
        _dt_v="$(curl -sS -o /dev/null -w '%{http_code} %{http_version} %{time_total}' \
            --max-time 8 "https://${BP_REALITY_TARGET}/" 2>/dev/null || echo 'ERR')"
        log "    http_code/http_version/time_total = $_dt_v"
        case "$_dt_v" in
            200\ 2*) ok "200 且 HTTP/2 —— 无跳转、H2 可用" ;;
            30*)     bad "target 站点有跳转，不合格（回落会暴露）" ;;
            ERR*)    bad "从本节点访问 target 站点失败 —— 回落超时反而暴露，换一个站点" ;;
            *)       warn "返回 '$_dt_v'，请人工判断是否满足「无跳转 + HTTP/2」" ;;
        esac
        if openssl s_client -connect "${BP_REALITY_TARGET}:443" -servername "$BP_REALITY_TARGET" \
                -tls1_3 </dev/null 2>/dev/null | grep -q 'TLSv1.3'; then
            ok "target 站点支持 TLS 1.3"
        else
            bad "target 站点 TLS 1.3 握手失败 —— REALITY 要求 target 支持 TLS 1.3"
        fi
    elif [ -z "${BP_REALITY_TARGET:-}" ]; then
        log "  BP_REALITY_TARGET 未设置 → 跳过 target 站点实测（**需实测**，别忘了补）"
    fi
}

# ---------------------------------------------------------------------------
# step 6/9 · systemd 硬化
# ---------------------------------------------------------------------------
do_systemd() {
    step 6/9 "systemd 单元与硬化"
    # LoadCredential + DynamicUser=true + ProtectSystem=strict + NoNewPrivileges=true
    # 是 Proxy_Skill 已经在跑的组合：凭据以只读方式出现在 %d/，
    # 进程没有固定 uid、看不到 /etc 的其余部分。
    #
    # AmbientCapabilities=CAP_NET_BIND_SERVICE 是**新增的必需项**：
    # Proxy_Skill 的 ssserver 监听高位端口不需要它，我们监听 443 需要。
    # 漏了它的症状是启动即 permission denied。
    #
    # MemoryHigh / MemoryMax 是**未经 ADR 裁决的提案**（Debian 12 = cgroup v2）：
    # 把「内核挑一个进程杀」换成「这个 cgroup 自己先被限住」。
    # 数字取自 e2-small 2 GB − 系统 150–250 MB ≈ 留给代理进程 1.4 GB。
    # **这两个数字是设定值，第一台节点跑完必须按实测重标定。**
    write_file "/etc/systemd/system/${BP_UNIT}" 0644 <<'UNIT'
[Unit]
Description=babel.plus node agent (v2node)
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=/usr/local/bin/v2node --config %d/node.json
LoadCredential=node.json:/etc/bp/v2node.json
LoadCredential=fullchain.pem:/etc/bp/certs/fullchain.pem
LoadCredential=privkey.pem:/etc/bp/certs/privkey.pem
DynamicUser=true
ProtectSystem=strict
ProtectHome=true
PrivateTmp=true
NoNewPrivileges=true
RestrictAddressFamilies=AF_INET AF_INET6 AF_NETLINK AF_UNIX
# ↓ DynamicUser 下没有 root，绑 443 必须显式给这一条，否则启动即失败
AmbientCapabilities=CAP_NET_BIND_SERVICE
CapabilityBoundingSet=CAP_NET_BIND_SERVICE
LimitNOFILE=1048576
Restart=on-failure
RestartSec=5
MemoryHigh=1400M
MemoryMax=1600M

[Install]
WantedBy=multi-user.target
UNIT
    if [ "$FILE_CHANGED" = 1 ] || [ "$NEED_RESTART" = 1 ]; then
        run systemctl daemon-reload
        run systemctl enable "$BP_UNIT"
        run systemctl restart "$BP_UNIT"
    else
        run systemctl enable "$BP_UNIT"
        ok "单元未变更，不重启（不打断在线用户）"
    fi
}

# ---------------------------------------------------------------------------
# step 7/9 · unattended-upgrades
# ---------------------------------------------------------------------------
do_upgrades() {
    step 7/9 "unattended-upgrades"
    export DEBIAN_FRONTEND=noninteractive
    run apt-get install -y unattended-upgrades
    # ⚠️ Automatic-Reboot 必须是 false。自动重启会在无人值守时让全体用户掉线，
    #    而面向用户的回滚做不到（客户端订阅刷新频率由用户决定）—— 掉线期间没有任何补救手段。
    #    内核更新放到维护窗口手动重启。
    #    代价：内核 CVE 的修复延迟，需要一条「待重启节点」的巡检项（当前不存在）。
    #    代理二进制不走 apt，所以 unattended-upgrades **不会**动 v2node 的版本钉死。
    write_file /etc/apt/apt.conf.d/99-bp-unattended 0644 <<'UNATT'
Unattended-Upgrade::Automatic-Reboot "false";
Unattended-Upgrade::Remove-Unused-Kernel-Packages "true";
UNATT
    run systemctl enable --now unattended-upgrades
    if [ "$DRY_RUN" != 1 ] && [ -f /var/run/reboot-required ]; then
        warn "/var/run/reboot-required 存在 —— 有内核更新在等重启。请排进维护窗口。"
    fi
}

# ---------------------------------------------------------------------------
# step 8/9 · SSH 加固
# ---------------------------------------------------------------------------
do_ssh() {
    step 8/9 "SSH 加固（drop-in，不动主配置文件）"
    # 用 drop-in 而不是 sed 改主文件 —— 这是「幂等」的具体含义。
    # Debian 12 的 sshd_config 顶部有 Include /etc/ssh/sshd_config.d/*.conf。
    # KbdInteractiveAuthentication 是 ChallengeResponseAuthentication 在 OpenSSH 8.7+
    # 的新名（Debian 12 = OpenSSH 9.2；旧名是否仍作为别名被接受 **待核实**）。
    write_file /etc/ssh/sshd_config.d/99-bp-hardening.conf 0644 <<'SSHD'
PasswordAuthentication no
PermitRootLogin no
KbdInteractiveAuthentication no
PermitEmptyPasswords no
X11Forwarding no
ClientAliveInterval 120
SSHD
    if [ "$DRY_RUN" = 1 ]; then
        log "  [dry-run] sshd -t && systemctl reload ssh"
        return 0
    fi
    # 先 sshd -t 再 reload，且用 reload 不用 restart —— 现有会话不断。
    # 管理通道只走 IAP：公网 22 已被 bp-public-ssh-deny 压制。
    if sshd -t; then
        ok "sshd 配置语法通过"
        systemctl reload ssh
    else
        die "🔴 sshd 配置语法错误，拒绝 reload。已写入的 drop-in 需要人工修，
      当前会话还活着 —— 不要断开它，否则可能再也登不上来。"
    fi
}

# ---------------------------------------------------------------------------
# step 9/9 · 自检
# ---------------------------------------------------------------------------
# check_field_renames · 版本地雷 ② 的自动化防线。
# 在节点上能扫到的只有本地文件；面板下发的配置**在哪落地待核实**，
# 所以扫不到不算通过，只算「无法判定」。
check_field_renames() {
    _cf_hits=0
    for _cf_d in "$BP_ETC" /usr/local/etc/v2node /etc/v2node; do
        [ -d "$_cf_d" ] || continue
        for _cf_pat in '"clients"' '"dest"' '"publicKey"' '"network"[[:space:]]*:[[:space:]]*"tcp"'; do
            if grep -rlE "$_cf_pat" "$_cf_d" 2>/dev/null | grep -q .; then
                bad "在 $_cf_d 下发现旧字段 $_cf_pat —— 旧名是**静默别名**，
      写错不报错、只是行为不符预期。一律视为缺陷，改用新名
      （users / target / password / \"method\": \"raw\"）。"
                _cf_hits=$((_cf_hits + 1))
            fi
        done
    done
    [ "$_cf_hits" = 0 ] && ok "本地配置中未发现已改名的旧字段"
    log "  ⚠️ 面板下发的配置在节点上的落点**待核实** —— 上面这条扫描"
    log "     只覆盖本地文件，不能证明面板下发的那份用的是新字段名。"
    log "     权威做法是在生成器侧加 CI grep + 起真实 v2node 容器做契约测试。"
}

do_selfcheck() {
    step 9/9 "自检"
    if [ "$DRY_RUN" = 1 ]; then
        log "  [dry-run] 跳过自检（自检全部是只读命令，真实执行时才有意义）"
        return 0
    fi

    if systemctl is-active --quiet "$BP_UNIT"; then
        ok "$BP_UNIT is-active"
    else
        bad "$BP_UNIT 未运行。看日志：journalctl -u $BP_UNIT -n 100 --no-pager
      常见成因：漏了 AmbientCapabilities=CAP_NET_BIND_SERVICE（绑 443 失败）、
      面板地址/密钥写错、证书文件不存在导致 LoadCredential 失败。"
    fi

    log "  监听端口（期望 tcp:443 与 udp:443 各一条，监听 0.0.0.0 / [::]）："
    _sc_l443="$(ss -tulnp 2>/dev/null | grep -E ':443[[:space:]]' || true)"
    if [ -n "$_sc_l443" ]; then
        printf '%s\n' "$_sc_l443" | sed 's/^/    /'
    else
        bad "443 上没有任何监听 —— REALITY 与 Hysteria2 都不可用"
    fi
    if [ -n "${BP_SS_PORT:-}" ]; then
        log "  SS-2022 端口 ${BP_SS_PORT}："
        _sc_lss="$(ss -tulnp 2>/dev/null | grep -E ":${BP_SS_PORT}[[:space:]]" || true)"
        if [ -n "$_sc_lss" ]; then
            printf '%s\n' "$_sc_lss" | sed 's/^/    /'
        else
            warn "${BP_SS_PORT} 上没有监听（SS 是兜底通路，面板侧未启用时属正常）"
        fi
    fi

    check_cert_issuer
    check_field_renames

    _sc_cc="$(sysctl -n net.ipv4.tcp_congestion_control 2>/dev/null || echo '?')"
    if [ "$_sc_cc" = "bbr" ]; then
        ok "BBR 已生效"
    else
        bad "🔴 BBR 未生效（当前 $_sc_cc）——
      sysctl 没生效时 Hysteria2 会用内核默认的 cubic，
      而 ADR 0004 §3.1 的整条裁决（BBR 不 Brutal）建立在 BBR 可用的前提上。"
    fi

    if journalctl -k 2>/dev/null | grep -qi 'out of memory'; then
        bad "🔴 内核日志里有 out of memory —— 已经发生过 OOM。
      注意：OOM 的现象（端口不再响应、日志戛然而止）与 IP 被封高度相似，
      别把它误诊成封锁。"
    else
        ok "内核日志无 OOM 记录"
    fi

    log ""
    log "  内存 / CPU 基线（🔴 **把这几行抄进 evidence/** ——"
    log "  ADR 0007 §4.2 的整套内存论证都是模型推算，这是它的第一个真实数字）："
    free -m | sed 's/^/    /'
    printf '    nproc=%s\n' "$(nproc)"
    uptime | sed 's/^/    /'
    if swapon --show 2>/dev/null | grep -q .; then
        swapon --show | sed 's/^/    /'
    fi

    log ""
    log "  接面板之后还要人工验的三件事（节点侧看不出来，node-provisioning §6.3）："
    log "    journalctl -u $BP_UNIT -f | grep -Ei 'config|user|push|alive|304|401'"
    log "    ① 每 60 秒各一次 GET .../UniProxy/config 与 /user"
    log "    ② 每 60 秒一次 POST .../push，body 形如 {\"1\":[u,d]}（原始字节，倍率面板结算）"
    log "    ③ 🔴 ETag：180 秒内应出现 1×200 + 2×304。若三次全是 200 且没有"
    log "       If-None-Match 头，说明 v2node 不发条件请求 —— 整套 ETag 设计一行都不生效。"
}

# ---------------------------------------------------------------------------
# main
# ---------------------------------------------------------------------------
main() {
    printf '=== bp 节点装机 · 步骤：%s\n' "$RUN_STEPS"
    [ "$DRY_RUN" = 1 ] && warn "DRY-RUN：不落任何一个字节"

    assert_not_legacy_host

    if [ "$(id -u)" != "0" ] && [ "$DRY_RUN" != 1 ]; then
        die "本脚本必须以 root 运行（装机方式见脚本头部：… --command=\"sudo bash -s -- --all\"）。"
    fi
    preflight_env

    want sysctl    && do_sysctl
    want baseline  && do_baseline
    want cert      && do_cert
    want v2node    && do_v2node
    want transport && do_transport
    want systemd   && do_systemd
    want upgrades  && do_upgrades
    want ssh       && do_ssh
    want selfcheck && do_selfcheck

    printf '\n=== 完成\n'
    # 只跑了 v2node 这一步时，改了配置却没人重启服务 —— 这种「装了但没生效」
    # 是最难发现的一类失败，所以显式点名。
    if [ "$NEED_RESTART" = 1 ] && ! want systemd; then
        warn "v2node 二进制或配置已变更，但本次没有跑 systemd 步骤 —— 服务仍在用旧配置。
      生效：sudo systemctl restart ${BP_UNIT}   （或重跑 --step systemd）"
    fi
    if [ "$FAILED_CHECKS" -gt 0 ]; then
        printf '  🔴 %d 项检查未通过 —— 逐条看上面的 [FAIL]，修完重跑本脚本（幂等）。\n' "$FAILED_CHECKS"
        printf '  在它们清零之前，**不要**把这台节点接给用户。\n'
        exit 1
    fi
    printf '  全部检查通过。下一步：接面板（§6）→ 🔴 IP 路由验收（verify-route.sh）。\n'
    printf '  🔴 路由验收是唯一不可跳过的闸：把一个绕道美国的 IP 推给用户，\n'
    printf '     代价是用户第一次连接就判定这个服务不行。\n'
}

main

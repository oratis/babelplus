#!/usr/bin/env bash
# =============================================================================
# rotate-ip.sh · 换节点外网 IP（约 1 分钟一轮，**不重建实例**）
#
# 事实源：docs/04-ops/node-provisioning.md §5.4
#         docs/04-ops/runbook-node-health.md §3.1–3.2（判定被封后的处置 + 换完要做的）
#         docs/05-adr/0007-node-migration.md §5.2（IP 是一等资产，释放是单向门）
#
# -----------------------------------------------------------------------------
# 三件必须先知道的事
# -----------------------------------------------------------------------------
# ① **不需要重建实例、不需要重装、甚至不需要重启进程。**
#    外部 IP 在 GCE 上是到内网 IP 的 1:1 NAT，服务监听 0.0.0.0。
#    （ADR 0007 §9.1 阶段 2 写的「释放 IP → 重新预留 → **重开**」，那个「重开」
#      指的是重新预留地址，不是重建 VM。这个措辞坑过人，这里写死。）
#
# ② **防火墙按 network tag（bp-node）匹配，换 IP 无需改任何规则。**
#    这是 create-node.sh 给所有 bp-* 规则绑 target tag 换来的直接好处：
#    IP 可以随便换，安全姿态不动。
#    → 换完**不要**去动防火墙。真要动，先回头读 create-node.sh 顶部那段。
#
# ③ **顺序不能反。** 地址处于 IN_USE 时删不掉，必须先摘 access-config。
#    而摘掉 access-config 的那一刻起，到新地址挂上为止，节点**没有公网 IP**：
#    全部用户掉线，且 IAP（走内网）仍然可用 —— 别把「SSH 还通」当成「服务还在」。
#
# -----------------------------------------------------------------------------
# 🔴 释放静态 IP 是**单向门**
# -----------------------------------------------------------------------------
# GCP 不保证能拿回同一个地址。一个当前活着、路由已被日常使用验证过的 IP
# 本身就是有价值的资产（vpn-us-ip-v4 的 -v4 后缀 = 美国节点 IP 已被封换过三次）。
# 所以本脚本默认**保留旧地址**（--release-old 才删），并且拒绝碰任何 vpn-* 地址。
#
# 用法见 --help。一切改动都支持 --dry-run。
# =============================================================================
set -euo pipefail

PROJECT="${BP_PROJECT:-oratis-491316}"
NODE="${BP_NODE:-bp-node-hk1}"
REGION="${BP_REGION:-asia-east2}"
ZONE="${BP_ZONE:-asia-east2-a}"
NODE_TAG="bp-node"

DRY_RUN=0
ASSUME_YES=0
RELEASE_OLD=0
NEW_ADDRESS=""
REASON=""

log()  { printf '%s\n' "$*"; }
step() { printf '\n=== %s\n' "$*"; }
ok()   { printf '  [ok]   %s\n' "$*"; }
warn() { printf '  [warn] %s\n' "$*" >&2; }
die()  { printf '\n[FATAL] %s\n' "$*" >&2; exit 1; }

PROTECTED_NAMES="vpn-us vpn-jp vpn-us-ip-v4 vpn-jp-ip anthropic-relay lisa-cloud lisa-web"

assert_target_safe() {
    _ats_name="$1"
    for _ats_p in $PROTECTED_NAMES; do
        if [ "$_ats_name" = "$_ats_p" ]; then
            die "拒绝操作 '$_ats_name'。它是现役资源：
      vpn-us / vpn-jp 是在跑的代理节点，vpn-us-ip-v4 / vpn-jp-ip 是它们的静态 IP
      （ADR 0007 裁决第 3 条：两个旧 IP **一律不释放、不迁移、不复用**；
        vpn-jp-ip 若确实从未被封，它是本项目最有价值的单个 IP 资产）。"
        fi
    done
    case "$_ats_name" in
        vpn-*|vpn_*) die "拒绝操作 '$_ats_name'：vpn-* 命名空间属于现役系统。" ;;
        bp-*) : ;;
        *) die "'$_ats_name' 不是 bp- 前缀，本脚本只处理 bp 资源。" ;;
    esac
}

usage() {
    cat <<'USAGE'
用法：rotate-ip.sh [选项]

  给 bp 节点换一个外网 IP。不重建实例、不重装、不重启进程、**不动防火墙**。
  典型场景：① 路由验收（verify-route.sh）不合格；② 判定 IP 被封（runbook §3）。

选项
  --node NAME       节点名，必须 bp- 前缀（默认 bp-node-hk1）
  --project ID      默认 oratis-491316
  --region REGION   默认 asia-east2
  --zone ZONE       默认 asia-east2-a
  --new-address NAME
                    用这个**已存在**的保留地址；不给则自动预留一个新的
  --release-old     换完后释放旧地址。🔴 释放是单向门，默认**不**释放
  --reason "文字"   写进轮换台账的原因（例：J1 跨洋绕行 / 判定 IP 被封）
  --dry-run         只打印将执行的命令
  --yes             跳过二次确认
  -h, --help        本帮助

判定 IP 被封的三条独立证据（**满足全部三条**才算，runbook §3）
  ① 服务端进程 active、端口正常 bind
  ② 443 上没有任何来自公网的 established 连接，且数小时零日志
  ③ 同端口从境外回打可完成 TCP 握手，而同协议同配置的另一区域节点完全正常
  ⚠️ 只满足 ①② 时，第一嫌疑是 **OOM**（现象高度相似）或
     **有人给 allow-xray-443 补了 target tag**（bp 节点会瞬时失去 443 入向）。
     换 IP 治不了这两种，先排除再来。
USAGE
}

while [ $# -gt 0 ]; do
    case "$1" in
        --node)         NODE="${2:?--node 需要值}"; shift 2 ;;
        --project)      PROJECT="${2:?--project 需要值}"; shift 2 ;;
        --region)       REGION="${2:?--region 需要值}"; shift 2 ;;
        --zone)         ZONE="${2:?--zone 需要值}"; shift 2 ;;
        --new-address)  NEW_ADDRESS="${2:?--new-address 需要值}"; shift 2 ;;
        --reason)       REASON="${2:?--reason 需要值}"; shift 2 ;;
        --release-old)  RELEASE_OLD=1; shift ;;
        --dry-run)      DRY_RUN=1; shift ;;
        --yes|-y)       ASSUME_YES=1; shift ;;
        -h|--help)      usage; exit 0 ;;
        *)              usage >&2; die "未知参数：$1" ;;
    esac
done

assert_target_safe "$NODE"
[ -n "$NEW_ADDRESS" ] && assert_target_safe "$NEW_ADDRESS"

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

confirm_typed() {
    _ct_expect="$1"
    _ct_what="$2"
    if [ "$ASSUME_YES" = 1 ]; then
        warn "--yes 已跳过确认：$_ct_what"
        return 0
    fi
    [ -t 0 ] || die "需要交互确认但 stdin 不是终端：$_ct_what（非交互请显式 --yes）"
    printf '\n  ⚠️  %s\n  确认请原样输入 %s ：' "$_ct_what" "$_ct_expect"
    _ct_ans=""
    read -r _ct_ans || true
    [ "$_ct_ans" = "$_ct_expect" ] || die "确认串不匹配，已中止。"
}

# ---------------------------------------------------------------------------
# 现状
# ---------------------------------------------------------------------------
step "现状"
command -v gcloud >/dev/null 2>&1 || die "找不到 gcloud。"

INSTANCE_EXISTS=1
INFO="$(gcloud compute instances describe "$NODE" --project="$PROJECT" --zone="$ZONE" \
    --format='value(networkInterfaces[0].accessConfigs[0].natIP,networkInterfaces[0].accessConfigs[0].name,tags.items)' \
    2>/dev/null)" || INSTANCE_EXISTS=0

OLD_IP=""; AC_NAME=""; TAGS=""
if [ "$INSTANCE_EXISTS" = 1 ]; then
    OLD_IP="$(printf '%s' "$INFO" | awk -F'\t' '{ print $1 }')"
    AC_NAME="$(printf '%s' "$INFO" | awk -F'\t' '{ print $2 }')"
    TAGS="$(printf '%s' "$INFO" | awk -F'\t' '{ print $3 }')"
fi
[ -n "$AC_NAME" ] || AC_NAME="external-nat"   # GCE 默认名，describe 拿不到时的兜底

log "  节点        ：$NODE ($ZONE)"
log "  当前外网 IP ：${OLD_IP:-<无>}"
log "  access-config：$AC_NAME"
log "  网络标签    ：${TAGS:-<无>}"

if [ "$INSTANCE_EXISTS" = 0 ]; then
    if [ "$DRY_RUN" = 1 ]; then
        warn "查不到实例 $NODE（不存在 / 无权限 / 未登录）。dry-run 继续，只演示流程。"
    else
        die "查不到实例 $NODE。核对 --node / --zone / --project，或先跑 create-node.sh。"
    fi
fi

# 换 IP 之所以不用改防火墙，前提是标签还在。标签掉了才是真正要救的东西。
if printf '%s' "$TAGS" | grep -q "$NODE_TAG"; then
    ok "标签含 $NODE_TAG —— 防火墙按 tag 匹配，本次换 IP 无需改任何规则"
elif [ "$INSTANCE_EXISTS" = 0 ] && [ "$DRY_RUN" = 1 ]; then
    warn "（实例不存在，跳过标签核对）"
else
    die "🔴 实例标签不含 $NODE_TAG。换 IP 之前先把标签补回来，否则新 IP 上：
      · 22 端口会被无 target tag 的 default-allow-ssh 对 0.0.0.0/0 放通；
      · bp-allow-reality-443 / bp-allow-hy2-udp443 不生效。
      修：gcloud compute instances add-tags $NODE --project=$PROJECT --zone=$ZONE --tags=$NODE_TAG"
fi

# 找出旧地址的保留名（用于可选的释放）
OLD_ADDR_NAME=""
if [ -n "$OLD_IP" ]; then
    OLD_ADDR_NAME="$(gcloud compute addresses list --project="$PROJECT" \
        --filter="region:($REGION) AND address=$OLD_IP" \
        --format='value(name)' 2>/dev/null | head -1 || true)"
    log "  旧地址保留名：${OLD_ADDR_NAME:-<非保留地址，可能是临时 IP>}"
fi

# ---------------------------------------------------------------------------
# 预留新地址
# ---------------------------------------------------------------------------
step "预留新地址"
if [ -z "$NEW_ADDRESS" ]; then
    NEW_ADDRESS="${NODE}-ip-$(date +%Y%m%d%H%M)"
    assert_target_safe "$NEW_ADDRESS"
    run gcloud compute addresses create "$NEW_ADDRESS" --project="$PROJECT" \
        --region="$REGION" --network-tier=PREMIUM
else
    ok "使用已存在的保留地址 $NEW_ADDRESS"
fi

NEW_IP="$(gcloud compute addresses describe "$NEW_ADDRESS" --project="$PROJECT" \
    --region="$REGION" --format='value(address)' 2>/dev/null || true)"
if [ -z "$NEW_IP" ]; then
    [ "$DRY_RUN" = 1 ] || die "拿不到 $NEW_ADDRESS 的地址值。"
    NEW_IP="<dry-run>"
fi
log "  新地址：$NEW_ADDRESS = $NEW_IP"

# 网段启发式提示。**只是先验，不是验收** —— 真正的判定在 verify-route.sh。
case "$NEW_IP" in
    35.220.*) ok  "落在 35.220.x（社区实测对移动直连约 50 ms，2022，**待核实**）" ;;
    34.92.*)  warn "落在 34.92.x（社区实测经东京绕行约 110 ms，2022，**待核实**）——
      建议直接换一个候选，不要浪费一轮 5 分钟的验收。" ;;
    *)        log  "  网段 ${NEW_IP%.*}.x 无先验记录，一切以 verify-route.sh 的实测为准。" ;;
esac

# ---------------------------------------------------------------------------
# 切换
# ---------------------------------------------------------------------------
step "切换（顺序不能反：先摘 access-config，再挂新地址）"
log "  ⚠️ 从摘掉 access-config 到新地址挂上为止，节点**没有公网 IP，全部用户掉线**。"
log "     IAP 走内网仍然可用 —— 别把「SSH 还通」当成「服务还在」。"
confirm_typed "$NODE" "将把 $NODE 的外网 IP 从 ${OLD_IP:-<无>} 换成 $NEW_IP"

run gcloud compute instances delete-access-config "$NODE" \
    --project="$PROJECT" --zone="$ZONE" \
    --access-config-name="$AC_NAME" --network-interface=nic0

run gcloud compute instances add-access-config "$NODE" \
    --project="$PROJECT" --zone="$ZONE" \
    --access-config-name="$AC_NAME" --address="$NEW_IP" \
    --network-tier=PREMIUM --network-interface=nic0

ok "已切换到 $NEW_IP"

# ---------------------------------------------------------------------------
# 旧地址
# ---------------------------------------------------------------------------
step "旧地址处置"
if [ -z "$OLD_ADDR_NAME" ]; then
    log "  旧 IP 不是保留地址（或查不到名字），无需释放。"
elif [ "$RELEASE_OLD" != 1 ]; then
    log "  保留 $OLD_ADDR_NAME（${OLD_IP}）。**释放是单向门，GCP 不保证能拿回同一个地址。**"
    log "  确认新 IP 验收通过、且不再需要回退之后，再手动释放："
    log "    gcloud compute addresses delete $OLD_ADDR_NAME --project=$PROJECT --region=$REGION"
    log "  ⚠️ 闲置保留地址是计费的（约 \$0.010/hr，**待核实**），别忘了。"
else
    assert_target_safe "$OLD_ADDR_NAME"
    confirm_typed "$OLD_ADDR_NAME" "将**永久释放**保留地址 $OLD_ADDR_NAME（$OLD_IP）。单向门，不可撤销。"
    run gcloud compute addresses delete "$OLD_ADDR_NAME" \
        --project="$PROJECT" --region="$REGION" --quiet
    ok "已释放 $OLD_ADDR_NAME"
fi

# ---------------------------------------------------------------------------
# 换完的检查清单
# ---------------------------------------------------------------------------
step "🔴 换完的检查清单（逐条勾，勾完才算换完）"
cat <<CHECKLIST

  节点侧（IP 是 1:1 NAT，服务监听 0.0.0.0，理论上什么都不用做 —— 但要验证）
  - [ ] 进程仍在跑，**没有**因为换 IP 重启过
        gcloud compute ssh $NODE --project=$PROJECT --zone=$ZONE --tunnel-through-iap \\
          --command='systemctl is-active bp-node.service; systemctl show -p ActiveEnterTimestamp bp-node.service'
  - [ ] 443 tcp/udp 仍在监听 0.0.0.0 / [::]
        ... --command='ss -tulnp | grep -E ":443[[:space:]]"'
  - [ ] 面板轮询未中断（60 秒一次 config / user，push 有数据入账）
        ... --command='journalctl -u bp-node.service -n 50 --no-pager | grep -Ei "config|user|push|alive"'

  防火墙（**不需要改任何规则**，只需确认 tag 仍然生效）
  - [ ] gcloud compute instances network-interfaces get-effective-firewalls $NODE \\
          --project=$PROJECT --zone=$ZONE
        期望能看到 bp-iap-ssh-allow / bp-public-ssh-deny / bp-allow-reality-443 / bp-allow-hy2-udp443
  - [ ] 公网 22 仍然被拒（**从境外机器测，且那台机器不能开着代理客户端**）
        timeout 8 bash -c "cat < /dev/null > /dev/tcp/${NEW_IP}/22" && echo '🔴 22 裸奔' || echo '22 已压制 ✅'

  🔴 路由验收（换 IP 的**全部意义**就在这里，不做等于白换）
  - [ ] ./verify-route.sh --node $NODE
  - [ ] 含一次晚高峰（19:00–24:00 CST）采样：./verify-route.sh --node $NODE --peak
  - [ ] **失败样本要留在 evidence/ 里**，不要只留合格的那个 ——
        「换过几次、每次为什么不合格」是评估「同区域 IP 段差异是否真实存在」的唯一数据来源

  面向用户（**只有节点已经在服务用户时才需要**，runbook §3.2）
  - [ ] 触发订阅重新生成（所有用户下次拉取即获得新 IP）
  - [ ] 邮件广播通知 —— 唯一能触达大陆用户的通道（Telegram 在大陆被封）
  - [ ] 更新节点名中的域名广播位
  - [ ] 记入 IP 轮换台账：第几代 / 换出原因 / 上一个 IP 的存活时长
        本次原因：${REASON:-<未填，请补 --reason>}
        上一个 IP：${OLD_IP:-<无>}   新 IP：${NEW_IP}
        **存活时长是评估协议抗封锁能力的唯一真实指标。**

  ⚠️ 换 IP 只是治标。新 IP 同样会被再次探测封锁（vpn-us-ip-v4 的 -v4 后缀 =
     美国节点已换过三次）。真正的抗封锁手段是协议层（REALITY 伪装、
     Hysteria2 混淆）与应急 CDN 通道。

  ⚠️ 建机阶段换 IP 代价为零；**上线之后换 IP 的代价是全员重导订阅** ——
     这就是为什么路由验收必须在接用户之前做完。

CHECKLIST

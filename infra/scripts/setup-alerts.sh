#!/usr/bin/env bash
#
# setup-alerts.sh —— 按 ADR 0014 的 A/B/C 三级，在 oratis-491316 上建通知渠道与告警策略
#
# ✅ **ADR 0014 已于 2026-09-02 获用户批准**（此前是「提案，未批准」，2026-08-23）。
#    本脚本按批准后的裁决实现，`--apply` 从这一天起是允许的。
#    2026-09-02 首轮实施的实况（哪些建了、哪些仍被门挡着、与 ADR 的三处有意偏离）
#    记在 docs/04-ops/first-deploy-20260831.md §4.5 与 ADR 0014 文档头的批准记录里。
#    ⚠️ 提案里那三处「待核实 / 需实测」（A2 的 combiner:AND 混用两种条件类型、
#       B10 的同比能否用单条件 MQL 表达、renotifyInterval 与 notificationRateLimit 并存）
#       **批准并没有让它们变成已核实** —— A2 与 B10 仍然被门挡着，理由见 gate_reason。
#      先建后改的代价不是改一次配置，是「清单里有一条永不触发的策略」——
#      ADR §8.4 第 2 条：**那比不存在更危险**，它让半年后的自己以为这里有覆盖。
#
# 事实源（引用一律带日期，理由见 ADR 0014 §17 代价第 9 条：一手实查全是某个时点的快照）：
#   docs/05-adr/0014-slo-and-oncall.md（2026-08-23 裁决，2026-09-02 批准）
#     §8.1 分级第一原则与三档定义 · §8.3 通道矩阵 · §8.4 静默纪律
#     §9.1 A 级 3 条（其中 2 条不在 GCP）· §9.2 B 级表 · §9.3 C 级（不建策略）
#     §10.1/§10.2 缺的指标怎么补 · §14 对 monitoring.md 的四处修正 · §15 实施顺序
#   docs/04-ops/monitoring.md（2026-08-16 定稿，§3.2 含 2026-08-23 增补）
#     §3.1 标签基数规矩 · §3.2 十条日志指标与实况 · §4 通道拓扑与建渠道的命令
#     §5 告警清单与阈值 · §5.1 metric absence 的两个坑 · §5.2 documentation 要写什么
#   docs/evidence/gcp-inventory-20260821/（2026-08-21 实查）
#     §5.2/§5.3：bp-alerts topic 已存在；通知渠道只有一条 email；bp-* 告警策略 0 条；
#     uptime check 只有 lisa-cloud-health；7 条日志指标已建、3 条建不了
#   docs/04-ops/runbook-node-health.md §3/§5（收到告警之后干什么，documentation 里指向它）
#
# 🔴 这个脚本**建得了的比 ADR 要求的少**，而且少得不是一星半点。结尾的覆盖率对照表是本脚本
#    最重要的输出，不是装饰：ADR 要求 14 条（+批准记录追加的 B11–B13），2026-09-02 默认门槛下能建 12 条。
#    差额逐条带原因。**不要把「脚本跑完了」当成「告警建好了」。**
#
# 🔴 另一条更容易被忘的：**建成 != armed。** metric-absence 型告警要求那条 time series
#    曾经有过数据。monitoring §5.1 原话：一个从未上报过的新节点「它在监控眼里根本不存在」。
#    所以每次新节点首次上报之后，必须人工确认 series 已出现 —— 脚本会在结尾再喊一次。
#
# 本脚本默认 dry-run，且 dry-run 模式下**不发任何触达 GCP 的 gcloud 调用**（连只读的都不发）。
# 唯一的例外是 guard_project 里那条 `gcloud config get-value project` ——
# 它读的是本机 gcloud 配置文件，不联网、不需要凭据、也不碰项目里的任何东西。
# 理由：这个 GCP 项目是共享的（AGENTS.md §4：lisa-cloud / lisa-web 不要碰），
# 而 dry-run 的用途是「看看它打算干什么」，不该以任何形式依赖对生产项目的访问权。

set -euo pipefail

# ───────────────────────── 项目守卫常量 ─────────────────────────
#
# 🔴 硬编码，不接受来自环境的覆盖之外的任何形式。项目跑错的代价不是「白跑一次」——
#    这个项目里住着 vpn-us / vpn-jp 两台在役代理节点和三个 lisa-* Cloud Run 服务，
#    而告警策略是**按 displayName 前缀**删的（--delete）。跑错项目 = 删别人的告警。

readonly EXPECTED_PROJECT_ID="oratis-491316"
# 项目编号（monitoring §4 的 Monitoring 服务代理 SA 里要用；2026-08-21 实查值）。
readonly EXPECTED_PROJECT_NUMBER="2360090741"

readonly REGION="us-central1"
readonly RUN_SERVICE="bp-api"
# uptime check 的 check_id **不是** displayName：GCP 给它追加了一段随机后缀。
# 2026-09-02 实查（gcloud monitoring uptime list-configs）。写成 bp-api-healthz 的过滤器永远匹配不到任何序列。
readonly UPTIME_CHECK_ID="bp-api-healthz-1bMHnj3kS4M"
readonly SQL_INSTANCE="bp-db"
readonly PUBSUB_TOPIC="bp-alerts"

# 通知渠道的显示名（monitoring §4 给的命名）。
readonly CH_NAME_PUBSUB="bp-alerts-pubsub"
readonly CH_NAME_EMAIL2="bp-alerts-email-a-only"

# 文档链接的根。documentation.content 会随告警一起送到值班手机上，
# 相对路径在那里没用（monitoring §5.2：这是最便宜的一次 runbook 投递）。
readonly DOC_BASE="https://github.com/oratis/babelplus/blob/master/docs"

# 危险操作的确认串。不读一遍提示就打不出来，比再加一个 --i-know 开关可靠。
# （2026-09-02 之前这个串是 apply-unapproved-adr-0014，把「ADR 未批准」折在串里；批准后改掉。）
readonly CONFIRM_APPLY="apply-adr-0014"
readonly CONFIRM_DELETE="delete-bp-alert-policies"

# ───────────────────────── 运行时开关 ─────────────────────────

PROJECT_ID="${PROJECT_ID:-$EXPECTED_PROJECT_ID}"
ACTION="plan"              # plan | create | delete
DRY_RUN=1
EXPLICIT_DRY_RUN=0
EMAIL1_ADDR=""             # email#1：B 级唯一通道。留空 = 从项目里已有的 email 渠道里认
EMAIL2_ADDR=""             # email#2：只接 A 级的语义隔离箱（ADR §12.2）。留空 = 不建
EMIT_DIR=""                # 策略 JSON 落盘目录（本地文件，不碰 GCP）
INCLUDE_BLOCKED=""         # 逗号分隔的 id，操作者自称门已解除
SKIP_CHANNELS=0

CREATED_N=0
SKIPPED_N=0
FAILED_N=0

# 通知渠道的资源名，resolve_channels 填。dry-run 下是占位符。
CH_EMAIL1=""
CH_EMAIL2=""
CH_PUSH=""
CH_PUBSUB=""

# ───────────────────────── 通用工具（与 infra/ 下其它脚本刻意保持重复）─────────────────────────
#
# 每个脚本自带一份守卫与打印工具，是因为它们要能被单独 scp 出去跑
# （deploy/README.md §6 已记这份重复的代价：改一处要改六处，没有机制提醒）。

log()  { printf '%s\n' "$*" >&2; }
step() { printf '\n\033[1m▸ %s\033[0m\n' "$*" >&2; }
ok()   { printf '  \033[32m✓\033[0m %s\n' "$*" >&2; }
warn() { printf '  \033[33m⚠\033[0m %s\n' "$*" >&2; }
skip() { printf '  \033[90m·\033[0m %s\n' "$*" >&2; }
fail() { printf '  \033[31m✗ %s\033[0m\n' "$*" >&2; FAILED_N=$((FAILED_N + 1)); }
die()  { printf '\n\033[31m✗ %s\033[0m\n' "$*" >&2; exit 2; }

# qq 打印一个参数：只在需要时加单引号。
# 不用 printf '%q' —— 它会把中文转成八进制转义，而 dry-run 的输出是给人读的。
qq() {
  case "$1" in
    ''|*[!A-Za-z0-9_@%+=:,./~-]*)
      printf "'%s'" "$(printf '%s' "$1" | sed "s/'/'\\\\''/g")" ;;
    *) printf '%s' "$1" ;;
  esac
}

# run 执行一条会改动 GCP 的命令；dry-run 下只打印。
run() {
  if [ "$DRY_RUN" -eq 1 ]; then
    local a
    printf '  [dry-run] ' >&2
    for a in "$@"; do qq "$a" >&2; printf ' ' >&2; done
    printf '\n' >&2
    return 0
  fi
  "$@"
}

need_cmd() {
  command -v "$1" >/dev/null 2>&1 || die "缺少命令：$1"
}

# confirm_typed · 危险操作的二次确认：必须原样敲出确认串，回车不算数。
# 刻意**没有** --yes 之类的旁路开关：本脚本唯一的两个危险动作各自只会被人手动跑几次，
# 给它们留一个自动化入口，收益是省几秒钟，代价是有一天它会出现在某个 CI 里。
confirm_typed() {
  local what="$1" expect="$2" answer=""
  # 非交互旁路：把确认串放进 BP_ALERTS_CONFIRM。它**不是** --yes —— 调用方仍然必须知道并写出那个串，
  # 只是省掉终端。2026-09-02 加：操作者在没有 tty 的会话里跑，而 gcloud 在伪终端下行为不同
  # （渠道查询解析失败、误建重复渠道），所以不能用 pty 冒充终端。
  if [ ! -t 0 ] && [ "${BP_ALERTS_CONFIRM:-}" = "$expect" ]; then
    warn "BP_ALERTS_CONFIRM 已匹配，跳过终端确认：$what"
    return 0
  fi
  if [ ! -t 0 ]; then
    die "需要交互确认但 stdin 不是终端：$what
     本脚本刻意不提供 --yes 旁路。请在终端里跑，或把确认串放进 BP_ALERTS_CONFIRM。"
  fi
  printf '\n  ⚠️  %s\n  确认请原样输入 %s ：' "$what" "$expect" >&2
  read -r answer || true
  [ "$answer" = "$expect" ] || die "确认串不匹配，已中止。未做任何改动。"
}

# guard_project 是本脚本最重要的两行防呆，两层都要过：
#   ① --project / $PROJECT_ID 必须是 oratis-491316；
#   ② **当前 gcloud 的活动项目**也必须是它。
# 为什么第 ② 层不能省，哪怕每条命令都显式带了 --project：
#   带 --project 只保证「这个脚本」打对了地方，保证不了「你人在哪」。
#   一个把活动项目切在别处的会话，意味着操作者的心智模型和脚本不一致 ——
#   而这个脚本会打印一堆命令让人复制粘贴（dry-run 的全部用途就是这个），
#   粘出去的那几条不会自动带上 --project。roadmap R7 的爆炸半径就在这儿。
guard_project() {
  if [ "$PROJECT_ID" != "$EXPECTED_PROJECT_ID" ]; then
    die "PROJECT_ID 必须是 ${EXPECTED_PROJECT_ID}，当前是 \"$PROJECT_ID\"。
     本仓库的资产清点、隔离承诺与告警命名前缀都只对这一个项目成立
     （docs/02-architecture/as-built-gcp.md）。"
  fi

  need_cmd gcloud
  local active
  active="$(gcloud config get-value project 2>/dev/null || true)"
  if [ "$active" != "$EXPECTED_PROJECT_ID" ]; then
    die "当前 gcloud 活动项目是 \"${active:-<未设置>}\"，不是 ${EXPECTED_PROJECT_ID}。
     这个 GCP 项目是共享的（AGENTS.md §4：lisa-cloud / lisa-web 不要碰），
     跑错项目会动到别人的资源。先切过去再来：
       gcloud config set project $EXPECTED_PROJECT_ID
     或只给本次进程改：
       CLOUDSDK_CORE_PROJECT=$EXPECTED_PROJECT_ID $0 ..."
  fi
}

# guard_bp_prefix 保证每一个被创建 / 删除的资源名都带 bp- 前缀。
# 删除路径上它是**最后一道**闸：项目里有三条 lisa-* 策略指向同一个收件箱（ADR §12.1），
# 一个写错的过滤器会把它们一起删掉，而那是别人的项目。
guard_bp_prefix() {
  local name
  for name in "$@"; do
    case "$name" in
      bp-*) : ;;
      *) die "资源名 \"$name\" 不带 bp- 前缀，拒绝操作。" ;;
    esac
  done
}

# ───────────────────────── 策略登记表 ─────────────────────────
#
# 记录格式：id|级别|策略名|门状态(ready/blocked/oob)|一句话说明
#
#   ready   —— 前置条件今天就满足，--apply 会建
#   blocked —— ADR 或实况写死了前置条件，默认不建（--include-blocked=<id> 可强开）
#   oob     —— out-of-band，**跑在 GCP 之外**，本脚本根本建不了（见 §9.1）
#
# 🔴 命名统一 bp-<级别>-<名字>，级别用小写 a/b。C 级不出现在这里 ——
#    ADR §9.3 的裁决是 C 级**不建策略**，只进看板与月巡检。
#    C 级不是「被砍掉的告警」，它是一条零成本车道；把它建成策略就是把它变回了叫醒权。
POLICIES=(
  "A1|a|bp-a-node-tcp443-oob|oob|全部 bp-node-* 的境外 TCP 443 握手失败（Uptime Kuma）"
  "A2|a|bp-a-cp-down-and-node-absent|blocked|控制面不可达 且 有节点掉队（复合 AND）"
  "A3|a|bp-a-cert-issuer-oob|oob|证书签发者不再是 Let's Encrypt（带外 VPS cron）"
  "B1|b|bp-b-node-heartbeat-absent|ready|节点心跳缺失（按 node_id 分组的 absence）"
  "B2|b|bp-b-healthz-unreachable|ready|/-/healthz 连续 2 个周期不可达且 >=2 个探测区域"
  "B3|b|bp-b-api-5xx|ready|API 5xx >=5 / 10 分钟"
  "B4|b|bp-b-uniproxy-auth-fail|ready|节点认证失败 >=5 / 5 分钟"
  "B5|b|bp-b-db-pool-wait|ready|DB 连接池等待 >0 / 5 分钟"
  "B6|b|bp-b-db-backends|ready|DB 连接数 >=18 / 10 分钟"
  "B7|b|bp-b-api-instances-cap|ready|API 活跃实例数 >7 / 5 分钟"
  "B8|b|bp-b-subscribe-404|ready|订阅 404 >=20 / 5 分钟"
  "B9|b|bp-b-db-disk|ready|DB 磁盘使用率 >=80%"
  "B10|b|bp-b-carrying-collapse|blocked|有效承载同比塌陷 >80%"
  # ── 2026-09-02 追加的三条（ADR 0014 批准记录里登记为 B11–B13）──
  # B11 就是 ADR §8.1 说「B 级 11 条」却只列出 10 条的那一条：它在 2026-09-01 被手工建出来时还没有名字。
  "B11|b|bp-b-scheduler-task-failed|ready|任一 bp-* Cloud Scheduler 作业单次非 2xx（最坏是 expire-check 停跑 = 到期用户继续免费上网）"
  # B12 是 ADR §9.1 A3 的**带内替身**：A3 要求带外 VPS cron，VPS 未采购，先让信号存在。故障域与 A3 不同，所以记 B 级不记 A 级。
  "B12|b|bp-b-cert-issuer-bad|ready|证书签发者不再是 Let's Encrypt（Cloud Run Job 每日核对，带内）"
  # B13 是 monitoring §8 自己登记「三个 event 里只有一个规划了指标」的那两个：到期临近与握手失败。
  "B13|b|bp-b-cert-expiring-or-unreachable|ready|证书剩余 < 14 天，或对域名握手失败 / 取不到证书"
)

# policy_field <id> <1..5> —— 取登记表的第 n 个字段。
policy_field() {
  local rec id="$1" n="$2"
  for rec in "${POLICIES[@]}"; do
    case "$rec" in
      "$id"\|*) printf '%s' "$rec" | cut -d'|' -f"$n"; return 0 ;;
    esac
  done
  return 1
}

# gate_reason <id> —— 为什么它今天建不了。**每一条都要能追到一份文档的一节**，
# 「以后再说」不是理由。这些字符串会原样进覆盖率对照表。
gate_reason() {
  case "$1" in
    A1) cat <<'EOF'
跑在 GCP 之外：第三方 VPS 上的 Uptime Kuma（ADR §9.1 A1、§6.4）。
  · 为什么不能建在 GCP 里：ADR §8.1 分级第一原则 —— A 级的唯一来源不能跑在被监控的那套
    基础设施里。GCP 整体故障时，建在 GCP 上的 A1 会和被监控对象一起挂。
  · 缺什么：一台第三方 VPS（约 $5/月），**未采购**。这是钱和采购决策，脚本变不出来。
EOF
      ;;
    A3) cat <<'EOF'
跑在 GCP 之外：同一台 VPS 上的 cron + openssl + msmtp 直接发信（ADR §9.1 A3、§10.2）。
  · 为什么不能建在 GCP 里：证书签发者变成 GTS 意味着我们自己的前置基础设施可能已经不可信，
    用那套基础设施去检测它自己是又一次自我引用（ADR §10.2）。
  · 缺什么：① 同一台未采购的 VPS；② 一个 msmtp 能用的发信账号；
    ③ 🔴 **域名归属未回答**（ADR §1.3）：babel.plus 的 RDAP 注册人隐藏，
       证明不了它归谁。若答案是「不是我们的」，这条检查今天根本没有对象，A 级实际只有 2 条。
  · 信号源侧已经有了：infra/scripts/check-cert-issuer.sh（PR #16）。缺的是「把它挂到哪台机器上」。
EOF
      ;;
    A2) cat <<'EOF'
四个前置条件里还剩一个（ADR §9.1、§15.1、§15.2；2026-09-02 复核）：
  ① ~~日志指标 bp_node_alive 尚未创建~~ ✅ 2026-09-02 已建（setup-metrics.sh --apply --create-only），带 node_id 标签。
  ② ~~D0 第 3 条：v2node 零流量分钟是否仍发 /push 未实测~~ ✅ 2026-09-02 实测：**不发**
     （24 h 日志里 /push 只在有流量的那个小时出现 8 次，其余全为 0），
     但 bp_node_alive 记在**任一** UniProxy 端点鉴权通过之后，/user 每分钟都在轮询，
     所以心跳不依赖 /push —— absence 型策略可以建，误报前提不成立。
  ③ ~~uptime check bp-api-healthz 不存在~~ ✅ 2026-09-01 已建（check_id 见 UPTIME_CHECK_ID）。
  ④ 🔴 A 级四通道未齐：email#2 与原生推送**未建、更未演练**。ADR §15.2 阶段 1 的门是
     「演练送达记录进 ledger，否则 A 级通道不算存在」（§11.1 规矩一）。
     给 --email2=<地址> 并演练送达之后，用 --include-blocked=A2 强开。
EOF
      ;;
    B1) cat <<'EOF'
（2026-09-02 起不再被门挡着：A2 的 ① 与 ② 已解除，见 A2。）
  · 附带一条 ADR §9.2 B1 表注的纪律，建的时候别忘：**不分组的那条心跳策略不建**，
    它的建立条件写死为「节点数 >= 2」—— 节点数 = 1 时它与分组版条件恒等（§2.1 攻击 2b）。
  · 🔴 建成 != armed：metric absence 要求该 node_id 的序列曾有数据。2026-09-02 实查
    node_id=1 每分钟一条，序列已存在。**每台新节点上线后都要回来确认一次。**
EOF
      ;;
    B2) cat <<'EOF'
（2026-09-02 起不再被门挡着。）两个前置的现状：
  ① ~~uptime check bp-api-healthz 不存在~~ ✅ 2026-09-01 已建，check_id 带随机后缀（UPTIME_CHECK_ID）。
  ② ~~matcher 写的是 "ok":true~~ ✅ monitoring §6.2 / deploy.md 2026-08-30 已订正为纯文本 ok。
  · 2026-09-01 手工建过一条同题的 bp-api-healthz-down（fraction_true < 1 持续 10 min），
    2026-09-02 由本条接管后删除 —— 同一事件不能有两条策略响。
EOF
      ;;
    B3) cat <<'EOF'
（2026-09-02 起不再被门挡着。）门曾是 roadmap B41（ADR §9.2 B3、§5.3 第 1 位、§15.2 阶段 3 之后）：
  · 「合并」那一半 2026-08-23 完成；「已部署」那一半 2026-08-25 完成 —— 生产自此跑的都是
    deploy-api.sh 带 label 构建的镜像（image-provenance.sh 反查通过），2026-09-02 在线的是 bp-api-f76487f。
  · 指标 bp_api_5xx 2026-08-21 已建。
EOF
      ;;
    B10) cat <<'EOF'
三个前置（ADR §9.2 B10、§3.3、§18）：
  ① 指标 bp_node_active_users 不存在 —— 它要 api/ 侧**新写一行日志**，
     且 ADR §10.1 写死：**不能与 bp_node_alive 挤同一行**。api/ 侧未落地。
  ② 14 天基线未采（ADR §18：需要真实用户，而 ESP 未接通、注册链路走不通）。
  ③ 🔴 单条件 MQL 能否表达「相对过去 14 天同星期几 x 小时的中位数」**待核实 + 需实测**
     （ADR §18 第 2 条，B10 唯一的技术风险）。本脚本 --emit-json 给出的是
     **7 日同比近似**，不是 ADR 要求的 14 天同槽位中位数 —— 两者不是一回事，别当成实现。
EOF
      ;;
    *) printf '（无）\n' ;;
  esac
}

# policy_doc <id> —— documentation.content。它随告警送到值班手机上，
# monitoring §5.2 的原话：凌晨三点没人会去翻文档目录。所以这里写「这意味着什么 + 去看哪一节」，
# 不写「发生了一个错误」。
policy_doc() {
  case "$1" in
    A2) printf '%s' "A 级。含义很具体：控制面挂了，**并且**至少一个节点已经不在上报。此时若重启该节点，v2node 的 Controller.Start() 会在 GetUserList 出错时拒绝启动，用户会掉线且不自愈。🔴 **控制面故障期间禁止重启任何节点、禁止任何节点侧变更**（ADR 0014 §6.2）。处置：${DOC_BASE}/04-ops/runbook-node-health.md §2 §4；口径：${DOC_BASE}/05-adr/0014-slo-and-oncall.md §9.1" ;;
    B1) printf '%s' "B 级，工作时间处理（19:00 之后顺延次日 10:00）。某个 node_id 连续 5 分钟没有心跳。⚠️ 它可能是误报：v2node 在零流量分钟是否仍发 /push 未实测（ADR 0014 §2.1 攻击 2a），这正是它是 B 级而不是 A 级的原因。先看 CP 的 SLI 再动节点。处置：${DOC_BASE}/04-ops/runbook-node-health.md §2 §3" ;;
    B2) printf '%s' "B 级。/-/healthz 在 >=2 个探测区域连续失败。要求多区域是为了排除单区域抖动。响应体判据是定值而不是 200（被劫持的 DNS、中间设备、平台默认页都可能回 200）。处置：${DOC_BASE}/04-ops/runbook-node-health.md §4；冷启动与回滚：${DOC_BASE}/04-ops/deploy.md §12.1" ;;
    B3) printf '%s' "B 级。10 分钟内 5xx >= 5 次。稳态 5xx 应为 0。刻意用绝对计数而不是比值：按 ADR 0014 §3.1 的请求量算术，1% 比值只等于 1.5 次/10 分钟，一次瞬时抖动就触发。处置：${DOC_BASE}/04-ops/runbook-node-health.md §2；回滚：${DOC_BASE}/04-ops/deploy.md §12.1" ;;
    B4) printf '%s' "B 级。节点 Bearer 校验失败 5 分钟内 >= 5 次。正常态严格为 0（密钥要么对要么错，两步轮换期间不产生 401）。两种成因：密钥轮换没做完两步，或有人在试。一个密钥失效的节点每分钟约 4.5 次请求全 401，必然触发 —— 这正是想要的。处置：${DOC_BASE}/04-ops/runbook-node-health.md §2" ;;
    B5) printf '%s' "B 级兼升配触发器。出现连接池等待意味着真撞了天花板：BP_DB_MAX_CONNS=2 x max-instances=8 + 6 预留 = 22 <= 25-3。稳态为 0。⚠️ 过滤器按 jsonPayload.err 文本近似匹配，**只会漏报不会误报** —— 能确认，不能排除。处置与升配：${DOC_BASE}/05-adr/0005-database-selection.md §6.3" ;;
    B6) printf '%s' "B 级兼升配触发器。num_backends >= 18 = 22 个可用连接的 80%（ADR 0005 §6.3）。稳态 1-3 个实例 x 2 连接 = 2-6 个 backend，距阈值 3 倍以上。处置与升配：${DOC_BASE}/05-adr/0005-database-selection.md §6.3" ;;
    B7) printf '%s' "B 级。活跃实例数 > 7（max-instances=8）。🔴 **此时 request_count 可能是平的甚至在下降，不要以为没事** —— 它不统计被拒绝的请求（monitoring §2.1）。这条是那个盲区的唯一替代信号。concurrency=80，跑满 8 个实例需要 640 并发，几十人规模下只可能来自重试风暴或崩溃循环。处置：${DOC_BASE}/04-ops/monitoring.md §2.1 与 ${DOC_BASE}/05-adr/0005-database-selection.md §6.3" ;;
    B8) printf '%s' "B 级。5 分钟内订阅 404 >= 20 次。订阅 token 是唯一对外可观测的攻击面（ADR 0006 §10.2 规定 404 不泄露存在性）。单个持有失效 token 的客户端最坏 5 次/5 分钟，20 需要至少 4 个独立失效客户端或一次 token 扫描。处置：${DOC_BASE}/04-ops/runbook-node-health.md §5" ;;
    B9) printf '%s' "B 级慢变量，24 小时 autoClose。10 GB SSD 使用率 >= 80%。几十用户的数据量在可预见期内不该接近它，触发即意味着有东西在意料之外地长（日志表、审计表、备份留在实例里）。处置：${DOC_BASE}/05-adr/0005-database-selection.md §6.3" ;;
    B11) printf '%s' "B 级。某条 bp-* Cloud Scheduler 作业单次非 2xx。最坏的一条是 expire-check 停跑 = 到期用户继续免费上网；其次 traffic-reset 停跑 = 周期不重置。先看 Cloud Scheduler 的最近执行状态与 bp-api 的 /internal/tasks/* 日志（403 = OIDC audience 或 caller 白名单被改；5xx = 任务体自己炸了）。处置：${DOC_BASE}/04-ops/deploy.md §8" ;;
    B12) printf '%s' "B 级（ADR 0014 A3 的带内替身，VPS 采购后应迁为带外 A 级）。web./api.babel.plus 的证书签发者不再是 Let's Encrypt。为什么要紧：GTS 证书在中国触发 IP 级单向丢包（ADR 0004 §3.4），失效形态是慢不是断。两种来路：① LE 证书过期没续 → ./infra/scripts/renew-le-cert.sh --apply；② 有人把 target-https-proxy 换回了 Google 托管证书 → 查 bp-admin-https-proxy 的 sslCertificates。先跑 renew-le-cert.sh --check。处置：${DOC_BASE}/04-ops/deploy.md §11.2" ;;
    B13) printf '%s' "B 级。证书剩余有效期 < 14 天（cert_expiring_soon），或对某个域名 TLS 握手失败 / 取不到证书（cert_check_failed）。前者是续签窗口：在到期前跑 ./infra/scripts/renew-le-cert.sh --apply（LE 90 天，剩 < 30 天 certbot 才真续）。后者可能是域名整个连不上 —— 先从别的网络 curl 一次再判断。处置：${DOC_BASE}/04-ops/monitoring.md §8" ;;
    B10) printf '%s' "B 级。有效承载相对基线塌陷。🔴 **收到之后第一件事是先看控制面的 SLI**：两个信号都走控制面，控制面挂了它们都会塌（ADR 0014 §3.3 的 2x2 右下角）。若控制面正常而这条塌了，那一格是「节点 IP 被封 / 协议被识别」—— 本项目频率最高的那类事故，也是所有其它自动信号都看不见的那一类。处置：${DOC_BASE}/04-ops/runbook-node-health.md §3" ;;
    *) printf '%s' "见 ${DOC_BASE}/05-adr/0014-slo-and-oncall.md" ;;
  esac
}

# ───────────────────────── 通知渠道 ─────────────────────────

# channels_json <级别> —— 该级别要挂的通道，JSON 数组的**内容**（不含方括号）。
#
# 🔴 这里与任务书里那句「每条告警同时挂两个通道」不一致，是**故意的**：
#    「两条通道都挂在每一条策略上，不做分级」是 monitoring.md §4 的旧裁决，
#    ADR 0014 §14 第一行明确把它改掉了 —— A 级四通道 / B 级单通道 / C 级不发。
#    理由是 §8.2：双通道到同一个人 = 噪音翻倍、冗余为零，而且 Pub/Sub 那条根本没通
#    （push 端点不在 openapi 契约里，持续 404）。本脚本按 ADR 走。
channels_json() {
  local out=""
  case "$1" in
    a)
      out="\"${CH_EMAIL1}\", \"${CH_EMAIL2}\", \"${CH_PUSH}\", \"${CH_PUBSUB}\""
      ;;
    b)
      out="\"${CH_EMAIL1}\""
      ;;
    *) die "未知级别：$1" ;;
  esac
  printf '%s' "$out"
}

# 🔴 过滤器里 type 与值都必须**加引号**：`type=email` 会被 gcloud 判成「右侧是字段名 email」而报
#    INVALID_ARGUMENT，脚本随后走「没找到 → 新建」分支，每跑一次多建一对重复渠道
#    （2026-09-02 实测，连建了三对，已手工删掉）。
# resolve_channels 解析（必要时创建）通知渠道。dry-run 下不发任何 gcloud，用占位符。
resolve_channels() {
  local p="projects/${PROJECT_ID}/notificationChannels"

  if [ "$DRY_RUN" -eq 1 ]; then
    CH_EMAIL1="${p}/<EMAIL1_ID·apply 时只读查询解析>"
    CH_EMAIL2="${p}/<EMAIL2_ID·未建>"
    CH_PUSH="${p}/<PUSH_ID·未建>"
    CH_PUBSUB="${p}/<PUBSUB_ID·apply 时创建>"
    skip "dry-run 不发任何 gcloud（含只读），通道 ID 用占位符"
    return 0
  fi

  # email#1：B 级的唯一通道，也是今天唯一在通的通道（ADR §12.1，2026-08-23 实查）。
  local filter='type="email"'
  if [ -n "$EMAIL1_ADDR" ]; then
    filter="type=\"email\" AND labels.email_address=\"${EMAIL1_ADDR}\""
  fi
  local found
  found="$(gcloud alpha monitoring channels list \
    --project="$PROJECT_ID" --filter="$filter" \
    --format='value(name)' 2>/dev/null | head -n 1 || true)"
  if [ -n "$found" ]; then
    CH_EMAIL1="$found"
    ok "email#1 = $CH_EMAIL1"
  elif [ -n "$EMAIL1_ADDR" ]; then
    run gcloud alpha monitoring channels create \
      --project="$PROJECT_ID" \
      --display-name="bp-alerts-email" \
      --type=email \
      --channel-labels="email_address=${EMAIL1_ADDR}"
    CH_EMAIL1="$(gcloud alpha monitoring channels list \
      --project="$PROJECT_ID" --filter="type=\"email\" AND labels.email_address=\"${EMAIL1_ADDR}\"" \
      --format='value(name)' 2>/dev/null | head -n 1 || true)"
    ok "email#1 已创建 = ${CH_EMAIL1:-<解析失败>}"
  else
    die "项目里没有 email 通知渠道，也没给 --email1=<地址>。
     B 级只有这一条通道，没有它建出来的策略是哑的。
     ⚠️ 收件邮箱不能是 @babel.plus（monitoring §4）：我们自己的发信域名一出问题，
        告警会跟着一起消失。"
  fi

  # email#2：只接 A 级的语义隔离箱（ADR §12.2）。地址是一个决策，不是一个默认值。
  if [ -n "$EMAIL2_ADDR" ]; then
    found="$(gcloud alpha monitoring channels list \
      --project="$PROJECT_ID" --filter="type=\"email\" AND labels.email_address=\"${EMAIL2_ADDR}\"" \
      --format='value(name)' 2>/dev/null | head -n 1 || true)"
    if [ -n "$found" ]; then
      CH_EMAIL2="$found"
      ok "email#2 = $CH_EMAIL2"
    else
      run gcloud alpha monitoring channels create \
        --project="$PROJECT_ID" \
        --display-name="$CH_NAME_EMAIL2" \
        --type=email \
        --channel-labels="email_address=${EMAIL2_ADDR}"
      CH_EMAIL2="$(gcloud alpha monitoring channels list \
        --project="$PROJECT_ID" --filter="type=\"email\" AND labels.email_address=\"${EMAIL2_ADDR}\"" \
        --format='value(name)' 2>/dev/null | head -n 1 || true)"
      ok "email#2 已创建 = ${CH_EMAIL2:-<解析失败>}"
    fi
    warn "🔴 email#2 的纪律没有任何技术手段保证，只能靠人（ADR §17 代价第 5 条）：
     **它只接 A 级。任何非 A 级的东西进了这个箱子，A 级就作废**，
     而且不会有任何信号提示这件事发生了。"
  else
    CH_EMAIL2="projects/${PROJECT_ID}/notificationChannels/<EMAIL2_ID·未建>"
    warn "email#2 未建（没给 --email2=<地址>）。A 级四通道不齐 → A 级策略保持 blocked。"
  fi

  # 推送通道：GCP 侧只能是 webhook，指向那台 VPS 上的 Bark / ServerChan 中转。
  CH_PUSH="projects/${PROJECT_ID}/notificationChannels/<PUSH_ID·未建>"
  warn "原生推送通道未建：它要先有那台未采购的 VPS 做发送端，
     且大陆到达率与勿扰穿透**需实测**（ADR §12.2）。
     ⚠️ GCP 侧只能用 webhook 类型接过去，而 Google 说明 webhook 与 Slack / PagerDuty /
        移动应用**共用同一内部服务、是同一个故障域**（runbook §5）——
        所以推送不能算「第四个独立故障域」，它的独立性只在最后一跳（VPS -> APNs）。"

  # Pub/Sub 通道：topic 2026-08-21 实查已存在，渠道没建。
  found="$(gcloud alpha monitoring channels list \
    --project="$PROJECT_ID" \
    --filter="type=\"pubsub\" AND labels.topic=\"projects/${PROJECT_ID}/topics/${PUBSUB_TOPIC}\"" \
    --format='value(name)' 2>/dev/null | head -n 1 || true)"
  if [ -n "$found" ]; then
    CH_PUBSUB="$found"
    skip "Pub/Sub 通道已存在 = $CH_PUBSUB"
  else
    guard_bp_prefix "$PUBSUB_TOPIC"
    if ! gcloud pubsub topics describe "$PUBSUB_TOPIC" --project="$PROJECT_ID" >/dev/null 2>&1; then
      fail "topic $PUBSUB_TOPIC 不存在（2026-08-21 实查说它在）。
     先跑 infra/deploy/setup-infra.sh --step=pubsub，再回来。"
      return 1
    fi
    # Monitoring 的服务代理要能往这个 topic 发布，否则渠道建得出来但一条都发不进去。
    # SA 域名字符串 monitoring §4 标了 待核实 —— 若这条 IAM 绑定失败，先去控制台确认 SA 名。
    run gcloud pubsub topics add-iam-policy-binding "$PUBSUB_TOPIC" \
      --project="$PROJECT_ID" \
      --member="serviceAccount:service-${EXPECTED_PROJECT_NUMBER}@gcp-sa-monitoring-notification.iam.gserviceaccount.com" \
      --role=roles/pubsub.publisher
    run gcloud alpha monitoring channels create \
      --project="$PROJECT_ID" \
      --display-name="$CH_NAME_PUBSUB" \
      --type=pubsub \
      --channel-labels="topic=projects/${PROJECT_ID}/topics/${PUBSUB_TOPIC}"
    CH_PUBSUB="$(gcloud alpha monitoring channels list \
      --project="$PROJECT_ID" \
      --filter="type=\"pubsub\" AND labels.topic=\"projects/${PROJECT_ID}/topics/${PUBSUB_TOPIC}\"" \
      --format='value(name)' 2>/dev/null | head -n 1 || true)"
    ok "Pub/Sub 通道已创建 = ${CH_PUBSUB:-<解析失败>}"
  fi
  warn "Pub/Sub 现在**不算送达通道，只算事后取证**（ADR §8.3）：
     push 订阅 bp-alerts-relay 指向的 /internal/tasks/alert-relay 不在 openapi 契约里，
     会持续 404。消息仍在（保留 7 天），复盘时用：
       gcloud pubsub subscriptions pull bp-alerts-archive --project=${PROJECT_ID} --limit=50 --auto-ack=false
     （那条 archive 拉取订阅本身也还没建 —— ADR §8.3 要求另建，setup-infra.sh 只建了 relay。）"

  return 0
}

# ───────────────────────── 策略 JSON ─────────────────────────
#
# 全部策略 JSON 都能用 --emit-json=<目录> 落盘并入库，理由与 ADR 0006 §13「生成物入库」一致：
# **code review 时能看见告警配置的变化本身**（monitoring §5.2；ADR §18 最后一条欠账）。

emit_policy_json() {
  local id="$1"
  local name tier chans doc
  name="$(policy_field "$id" 3)"
  tier="$(policy_field "$id" 2)"
  chans="$(channels_json "$tier")"
  doc="$(policy_doc "$id")"

  # A 级配 renotifyInterval 30 分钟（ADR §6.3：GCP 侧管 30 分钟起的持续提醒，
  # 0-30 分钟那一段由 Kuma 的 resendInterval 补）。**刻意不配 notificationRateLimit** ——
  # 两者并存时的行为未知（ADR §6.3 标 需实测），限流可能吃掉提醒。
  local strategy_a
  strategy_a="\"alertStrategy\": {
    \"autoClose\": \"1800s\",
    \"notificationChannelStrategy\": [
      { \"notificationChannelNames\": [ ${chans} ], \"renotifyInterval\": \"1800s\" }
    ]
  }"

  case "$id" in
    A2)
      # 🔴 combiner 必须是 AND，**绝不能是 AND_WITH_MATCHING_RESOURCE**（ADR §9.1 表注）：
      #    条件 1 落在 uptime_url 上、条件 2 落在 cloud_run_revision 上，永不可能是同一个
      #    resource —— 用后者会得到一条永不触发、但在清单里看起来存在的策略。
      #    「混用 conditionThreshold 与 conditionAbsent 在 AND 下是否真的按预期工作」
      #    证据等级只有「中」，**仍需一次真实创建坐实**（ADR §11.2 的 A2 演练就是为此设计）。
      cat <<EOF
{
  "displayName": "${name}",
  "combiner": "AND",
  "conditions": [
    {
      "displayName": "uptime check failing in >=2 probe regions",
      "conditionThreshold": {
        "filter": "metric.type=\"monitoring.googleapis.com/uptime_check/check_passed\" AND resource.type=\"uptime_url\" AND metric.label.check_id=\"${UPTIME_CHECK_ID}\"",
        "aggregations": [
          {
            "alignmentPeriod": "60s",
            "perSeriesAligner": "ALIGN_NEXT_OLDER",
            "crossSeriesReducer": "REDUCE_COUNT_FALSE",
            "groupByFields": ["resource.label.host"]
          }
        ],
        "comparison": "COMPARISON_GT",
        "thresholdValue": 1,
        "duration": "120s",
        "trigger": { "count": 1 }
      }
    },
    {
      "displayName": "bp_node_alive absent per node_id",
      "conditionAbsent": {
        "filter": "resource.type=\"cloud_run_revision\" AND metric.type=\"logging.googleapis.com/user/bp_node_alive\"",
        "aggregations": [
          {
            "alignmentPeriod": "60s",
            "perSeriesAligner": "ALIGN_COUNT",
            "crossSeriesReducer": "REDUCE_SUM",
            "groupByFields": ["metric.label.node_id"]
          }
        ],
        "duration": "300s",
        "trigger": { "count": 1 }
      }
    }
  ],
  "notificationChannels": [ ${chans} ],
  ${strategy_a},
  "documentation": { "content": "${doc}", "mimeType": "text/markdown" }
}
EOF
      ;;
    B1)
      # groupByFields 必须按 node_id 分组（monitoring §5.1 坑二）：
      # 不分组的话「8 个节点里挂了 1 个」不会触发缺失，因为总数仍 >0。
      cat <<EOF
{
  "displayName": "${name}",
  "combiner": "OR",
  "conditions": [
    {
      "displayName": "no bp_node_alive for 300s (per node_id)",
      "conditionAbsent": {
        "filter": "resource.type=\"cloud_run_revision\" AND metric.type=\"logging.googleapis.com/user/bp_node_alive\"",
        "aggregations": [
          {
            "alignmentPeriod": "60s",
            "perSeriesAligner": "ALIGN_COUNT",
            "crossSeriesReducer": "REDUCE_SUM",
            "groupByFields": ["metric.label.node_id"]
          }
        ],
        "duration": "300s",
        "trigger": { "count": 1 }
      }
    }
  ],
  "notificationChannels": [ ${chans} ],
  "alertStrategy": { "autoClose": "1800s" },
  "documentation": { "content": "${doc}", "mimeType": "text/markdown" }
}
EOF
      ;;
    B2)
      cat <<EOF
{
  "displayName": "${name}",
  "combiner": "OR",
  "conditions": [
    {
      "displayName": "healthz uptime check failing in >=2 regions for 120s",
      "conditionThreshold": {
        "filter": "metric.type=\"monitoring.googleapis.com/uptime_check/check_passed\" AND resource.type=\"uptime_url\" AND metric.label.check_id=\"${UPTIME_CHECK_ID}\"",
        "aggregations": [
          {
            "alignmentPeriod": "60s",
            "perSeriesAligner": "ALIGN_NEXT_OLDER",
            "crossSeriesReducer": "REDUCE_COUNT_FALSE",
            "groupByFields": ["resource.label.host"]
          }
        ],
        "comparison": "COMPARISON_GT",
        "thresholdValue": 1,
        "duration": "120s",
        "trigger": { "count": 1 }
      }
    }
  ],
  "notificationChannels": [ ${chans} ],
  "alertStrategy": { "autoClose": "1800s" },
  "documentation": { "content": "${doc}", "mimeType": "text/markdown" }
}
EOF
      ;;
    B3)
      emit_count_policy "$name" "logging.googleapis.com/user/bp_api_5xx" \
        "api 5xx >= 5 in 10m" "600s" "COMPARISON_GT" "4" "1800s" "$chans" "$doc"
      ;;
    B4)
      emit_count_policy "$name" "logging.googleapis.com/user/bp_uniproxy_auth_fail" \
        "uniproxy auth fail >= 5 in 5m" "300s" "COMPARISON_GT" "4" "1800s" "$chans" "$doc"
      ;;
    B5)
      emit_count_policy "$name" "logging.googleapis.com/user/bp_db_pool_wait" \
        "db pool wait > 0 in 5m" "300s" "COMPARISON_GT" "0" "1800s" "$chans" "$doc"
      ;;
    B8)
      emit_count_policy "$name" "logging.googleapis.com/user/bp_subscribe_404" \
        "subscribe 404 >= 20 in 5m" "300s" "COMPARISON_GT" "19" "1800s" "$chans" "$doc"
      ;;
    B6)
      cat <<EOF
{
  "displayName": "${name}",
  "combiner": "OR",
  "conditions": [
    {
      "displayName": "num_backends >= 18 for 10m",
      "conditionThreshold": {
        "filter": "metric.type=\"cloudsql.googleapis.com/database/postgresql/num_backends\" AND resource.type=\"cloudsql_database\" AND resource.labels.database_id=\"${PROJECT_ID}:${SQL_INSTANCE}\"",
        "aggregations": [
          { "alignmentPeriod": "60s", "perSeriesAligner": "ALIGN_MAX", "crossSeriesReducer": "REDUCE_MAX" }
        ],
        "comparison": "COMPARISON_GT",
        "thresholdValue": 17,
        "duration": "600s",
        "trigger": { "count": 1 }
      }
    }
  ],
  "notificationChannels": [ ${chans} ],
  "alertStrategy": { "autoClose": "86400s" },
  "documentation": { "content": "${doc}", "mimeType": "text/markdown" }
}
EOF
      ;;
    B7)
      # state="active" 必须显式带上：只看总和会把保温的 idle 实例算进来（monitoring §2.2）。
      cat <<EOF
{
  "displayName": "${name}",
  "combiner": "OR",
  "conditions": [
    {
      "displayName": "active instances > 7 for 5m",
      "conditionThreshold": {
        "filter": "metric.type=\"run.googleapis.com/container/instance_count\" AND resource.type=\"cloud_run_revision\" AND resource.labels.service_name=\"${RUN_SERVICE}\" AND resource.labels.location=\"${REGION}\" AND metric.labels.state=\"active\"",
        "aggregations": [
          { "alignmentPeriod": "60s", "perSeriesAligner": "ALIGN_MAX", "crossSeriesReducer": "REDUCE_SUM" }
        ],
        "comparison": "COMPARISON_GT",
        "thresholdValue": 7,
        "duration": "300s",
        "trigger": { "count": 1 }
      }
    }
  ],
  "notificationChannels": [ ${chans} ],
  "alertStrategy": { "autoClose": "1800s" },
  "documentation": { "content": "${doc}", "mimeType": "text/markdown" }
}
EOF
      ;;
    B9)
      cat <<EOF
{
  "displayName": "${name}",
  "combiner": "OR",
  "conditions": [
    {
      "displayName": "disk utilization >= 80% for 30m",
      "conditionThreshold": {
        "filter": "metric.type=\"cloudsql.googleapis.com/database/disk/utilization\" AND resource.type=\"cloudsql_database\" AND resource.labels.database_id=\"${PROJECT_ID}:${SQL_INSTANCE}\"",
        "aggregations": [
          { "alignmentPeriod": "300s", "perSeriesAligner": "ALIGN_MEAN", "crossSeriesReducer": "REDUCE_MAX" }
        ],
        "comparison": "COMPARISON_GT",
        "thresholdValue": 0.799,
        "duration": "1800s",
        "trigger": { "count": 1 }
      }
    }
  ],
  "notificationChannels": [ ${chans} ],
  "alertStrategy": { "autoClose": "86400s" },
  "documentation": { "content": "${doc}", "mimeType": "text/markdown" }
}
EOF
      ;;
    B10)
      # 🔴 这段 MQL 是**近似**，不是 ADR §9.2 B10 要求的那个判据：
      #    ADR 要的是「相对过去 14 天同（星期几 x 小时）中位数下降 >80%」，
      #    下面写的是 7 日同比（time_shift 7d）。MQL 能不能表达 14 天同槽位中位数
      #    ADR §18 自己标着 待核实 + 需实测 —— 所以这里不假装做到了。
      #    conditionMonitoringQueryLanguage **必须是唯一条件**，B10 恰好只有一个条件。
      cat <<EOF
{
  "displayName": "${name}",
  "combiner": "OR",
  "conditions": [
    {
      "displayName": "active users collapsed >80% vs 7d ago (APPROXIMATION, see ADR 0014 §18)",
      "conditionMonitoringQueryLanguage": {
        "query": "{ fetch cloud_run_revision::logging.googleapis.com/user/bp_node_active_users | align delta(30m) | every 30m | group_by [], [v: sum(value.bp_node_active_users)] ; fetch cloud_run_revision::logging.googleapis.com/user/bp_node_active_users | align delta(30m) | every 30m | group_by [], [v: sum(value.bp_node_active_users)] | time_shift 7d } | join | value [ratio: val(0) / val(1)] | condition ratio < 0.2",
        "duration": "1800s",
        "trigger": { "count": 1 }
      }
    }
  ],
  "notificationChannels": [ ${chans} ],
  "alertStrategy": { "autoClose": "1800s" },
  "documentation": { "content": "${doc}", "mimeType": "text/markdown" }
}
EOF
      ;;
    B11)
      # 与 2026-09-01 手工建的 bp-scheduler-task-failed 逐字段同形（conditionMatchedLog + 5 分钟通知限流），
      # 2026-09-02 由本脚本接管：先删手工那条再建这条，避免同一事件响两次。
      cat <<EOF
{
  "displayName": "${name}",
  "combiner": "OR",
  "conditions": [
    {
      "displayName": "bp-* scheduler job non-2xx",
      "conditionMatchedLog": {
        "filter": "resource.type=\"cloud_scheduler_job\" AND resource.labels.job_id=~\"^bp-\" AND severity>=ERROR"
      }
    }
  ],
  "notificationChannels": [ ${chans} ],
  "alertStrategy": { "autoClose": "1800s", "notificationRateLimit": { "period": "300s" } },
  "documentation": { "content": "${doc}", "mimeType": "text/markdown" }
}
EOF
      ;;
    B12)
      emit_global_count_policy "$name" "logging.googleapis.com/user/bp_cert_issuer_bad" \
        "cert issuer bad > 0 in 5m" "300s" "COMPARISON_GT" "0" "86400s" "$chans" "$doc"
      ;;
    B13)
      # 两条指标 OR 在一起：任一 > 0 即响。24 h autoClose —— 它是慢变量，每天只核对一次。
      cat <<EOF
{
  "displayName": "${name}",
  "combiner": "OR",
  "conditions": [
    {
      "displayName": "cert expiring soon (< 14d) > 0 in 5m",
      "conditionThreshold": {
        "filter": "metric.type=\"logging.googleapis.com/user/bp_cert_expiring_soon\" AND resource.type=\"global\"",
        "aggregations": [ { "alignmentPeriod": "300s", "perSeriesAligner": "ALIGN_SUM", "crossSeriesReducer": "REDUCE_SUM" } ],
        "comparison": "COMPARISON_GT",
        "thresholdValue": 0,
        "duration": "0s",
        "trigger": { "count": 1 }
      }
    },
    {
      "displayName": "cert check failed (handshake / no cert) > 0 in 5m",
      "conditionThreshold": {
        "filter": "metric.type=\"logging.googleapis.com/user/bp_cert_check_failed\" AND resource.type=\"global\"",
        "aggregations": [ { "alignmentPeriod": "300s", "perSeriesAligner": "ALIGN_SUM", "crossSeriesReducer": "REDUCE_SUM" } ],
        "comparison": "COMPARISON_GT",
        "thresholdValue": 0,
        "duration": "0s",
        "trigger": { "count": 1 }
      }
    }
  ],
  "notificationChannels": [ ${chans} ],
  "alertStrategy": { "autoClose": "86400s" },
  "documentation": { "content": "${doc}", "mimeType": "text/markdown" }
}
EOF
      ;;
    *)
      die "策略 $id 没有 JSON 定义（A1 / A3 跑在 GCP 之外，本来就不该走到这里）"
      ;;
  esac
}

# 🔴 GCP 的 conditionThreshold **只支持 COMPARISON_GT / COMPARISON_LT**（2026-09-02 实测：
#    COMPARISON_GE 被 INVALID_ARGUMENT 拒绝，"only COMPARISON_LT and COMPARISON_GT are supported at present"）。
#    所以 ADR 里写的「>= N」一律落成「> N-1」（计数是整数，两者等价）；磁盘使用率 >= 80% 落成 > 0.799。
#    条件的 displayName 仍按 ADR 的写法保留「>=」，读告警的人看到的是裁决口径。
# emit_global_count_policy —— 与 emit_count_policy 同形，但资源类型是 global：
# check-cert-issuer.sh 用 gcloud logging write 写的日志不属于任何 Cloud Run 修订版，
# 它的日志指标序列挂在 resource.type="global" 上。套 emit_count_policy 的过滤器会永远匹配不到。
emit_global_count_policy() {
  local name="$1" mtype="$2" cname="$3" window="$4" cmp="$5" thr="$6" ac="$7" chans="$8" doc="$9"
  cat <<EOF
{
  "displayName": "${name}",
  "combiner": "OR",
  "conditions": [
    {
      "displayName": "${cname}",
      "conditionThreshold": {
        "filter": "metric.type=\"${mtype}\" AND resource.type=\"global\"",
        "aggregations": [
          { "alignmentPeriod": "${window}", "perSeriesAligner": "ALIGN_SUM", "crossSeriesReducer": "REDUCE_SUM" }
        ],
        "comparison": "${cmp}",
        "thresholdValue": ${thr},
        "duration": "0s",
        "trigger": { "count": 1 }
      }
    }
  ],
  "notificationChannels": [ ${chans} ],
  "alertStrategy": { "autoClose": "${ac}" },
  "documentation": { "content": "${doc}", "mimeType": "text/markdown" }
}
EOF
}

# emit_count_policy <名> <指标type> <条件名> <对齐窗口> <比较> <阈值> <autoClose> <通道> <文档>
#
# 日志指标都是 DELTA 计数器：把对齐窗口设成告警窗口本身 + ALIGN_SUM，duration 用 0s。
# 换一种写法（60s 对齐 + duration=600s）语义是「连续 10 分钟每分钟都超阈值」，完全不是一回事。
emit_count_policy() {
  local name="$1" mtype="$2" cname="$3" window="$4" cmp="$5" thr="$6" ac="$7" chans="$8" doc="$9"
  cat <<EOF
{
  "displayName": "${name}",
  "combiner": "OR",
  "conditions": [
    {
      "displayName": "${cname}",
      "conditionThreshold": {
        "filter": "metric.type=\"${mtype}\" AND resource.type=\"cloud_run_revision\" AND resource.labels.service_name=\"${RUN_SERVICE}\"",
        "aggregations": [
          { "alignmentPeriod": "${window}", "perSeriesAligner": "ALIGN_SUM", "crossSeriesReducer": "REDUCE_SUM" }
        ],
        "comparison": "${cmp}",
        "thresholdValue": ${thr},
        "duration": "0s",
        "trigger": { "count": 1 }
      }
    }
  ],
  "notificationChannels": [ ${chans} ],
  "alertStrategy": { "autoClose": "${ac}" },
  "documentation": { "content": "${doc}", "mimeType": "text/markdown" }
}
EOF
}

# ───────────────────────── 创建 ─────────────────────────

# is_included <id> —— 操作者是否用 --include-blocked 强开了这一条。
is_included() {
  case ",${INCLUDE_BLOCKED}," in
    *",$1,"*) return 0 ;;
    *) return 1 ;;
  esac
}

# 记录覆盖率对照表的行，结尾统一打印。
COVERAGE=()

create_policies() {
  step "按 A/B/C 分级创建告警策略"

  local rec id tier name gate desc
  for rec in "${POLICIES[@]}"; do
    id="$(printf '%s' "$rec" | cut -d'|' -f1)"
    tier="$(printf '%s' "$rec" | cut -d'|' -f2)"
    name="$(printf '%s' "$rec" | cut -d'|' -f3)"
    gate="$(printf '%s' "$rec" | cut -d'|' -f4)"
    desc="$(printf '%s' "$rec" | cut -d'|' -f5)"

    guard_bp_prefix "$name"

    if [ "$gate" = "oob" ]; then
      SKIPPED_N=$((SKIPPED_N + 1))
      COVERAGE+=("${id}|${name}|跳过·跑在 GCP 之外")
      continue
    fi

    if [ "$gate" = "blocked" ] && ! is_included "$id"; then
      skip "${id} ${name} —— 前置未满足，不建（--include-blocked=${id} 可强开）"
      SKIPPED_N=$((SKIPPED_N + 1))
      COVERAGE+=("${id}|${name}|跳过·前置未满足")
      continue
    fi

    if [ "$gate" = "blocked" ]; then
      warn "🔴 ${id} 被 --include-blocked 强开。你正在断言下面这些门已经解除："
      gate_reason "$id" | sed 's/^/       /' >&2
    fi

    local json_file
    json_file="$(mktemp -t "bp-alert-${id}.XXXXXX")"
    emit_policy_json "$id" >"$json_file"

    if [ -n "$EMIT_DIR" ]; then
      mkdir -p "$EMIT_DIR"
      cp "$json_file" "${EMIT_DIR}/${name}.json"
      ok "JSON 已落盘 ${EMIT_DIR}/${name}.json"
    fi

    printf '  %s %s（%s 级）%s\n' "$id" "$name" "$tier" "$desc" >&2
    if [ "$DRY_RUN" -eq 1 ]; then
      printf '  [dry-run] gcloud alpha monitoring policies create --project=%s --policy-from-file=%s\n' \
        "$PROJECT_ID" "${EMIT_DIR:-<临时文件>}/${name}.json" >&2
      SKIPPED_N=$((SKIPPED_N + 1))
      COVERAGE+=("${id}|${name}|dry-run·未创建")
      rm -f "$json_file"
      continue
    fi

    # 幂等：同名策略已存在就不重复建。GCP 允许同名策略并存，重复跑会得到两条同名的，
    # 而删除是按 displayName 前缀匹配的 —— 重复不会立刻出事，但会让清单读不懂。
    local exists
    exists="$(gcloud alpha monitoring policies list \
      --project="$PROJECT_ID" --filter="displayName=\"${name}\"" \
      --format='value(name)' 2>/dev/null | head -n 1 || true)"
    if [ -n "$exists" ]; then
      skip "${name} 已存在（${exists}），跳过"
      SKIPPED_N=$((SKIPPED_N + 1))
      COVERAGE+=("${id}|${name}|已存在")
      rm -f "$json_file"
      continue
    fi

    if gcloud alpha monitoring policies create \
        --project="$PROJECT_ID" --policy-from-file="$json_file" >/dev/null; then
      ok "${name} 已创建"
      CREATED_N=$((CREATED_N + 1))
      COVERAGE+=("${id}|${name}|已创建")
    else
      fail "${name} 创建失败 —— 上面那条 gcloud 的报错就是原因，别忽略它。
     ⚠️ 一条建失败的策略与一条不存在的策略在清单里长得一样（ADR §8.4 第 2 条）。"
      COVERAGE+=("${id}|${name}|创建失败")
    fi
    rm -f "$json_file"
  done
}

# ───────────────────────── 删除 ─────────────────────────

delete_policies() {
  step "删除 bp-* 告警策略"

  log "  为什么是删除而不是静默（ADR §8.4 第 3 条）："
  log "    删除是可见的（alerts/ 目录入库，code review 看得见）；静默是不可见的。"
  log "    一条被永久静默的策略在清单里仍然显示「存在」，**这比不存在更危险**。"

  local list
  list="$(gcloud alpha monitoring policies list \
    --project="$PROJECT_ID" \
    --format='value(name,displayName)' 2>/dev/null || true)"

  if [ -z "$list" ]; then
    skip "项目里一条告警策略都没有（或读不到）。"
    return 0
  fi

  local res dn n=0
  while IFS="$(printf '\t')" read -r res dn; do
    [ -n "$res" ] || continue
    # 🔴 双重过滤：list 的 --filter 可能因为版本差异不按预期工作，
    #    所以在循环里再判一次前缀。项目里那三条 lisa-* 策略不是我们的
    #    （AGENTS.md §4 红线），删掉它们等于打断别人的告警。
    case "$dn" in
      bp-*) : ;;
      *) skip "跳过非 bp-* 策略：${dn}"; continue ;;
    esac
    guard_bp_prefix "$dn"
    n=$((n + 1))
    if [ "$DRY_RUN" -eq 1 ]; then
      printf '  [dry-run] gcloud alpha monitoring policies delete %s --project=%s --quiet   # %s\n' \
        "$res" "$PROJECT_ID" "$dn" >&2
      continue
    fi
    if gcloud alpha monitoring policies delete "$res" --project="$PROJECT_ID" --quiet >/dev/null; then
      ok "已删除 ${dn}"
    else
      fail "删除 ${dn} 失败"
    fi
  done <<EOF
$list
EOF

  log ""
  log "  共 ${n} 条 bp-* 策略。"
  warn "**通知渠道一律不删。** email#1 同时是三条 lisa-* 策略的收件通道（ADR §12.1，
     2026-08-23 实查），删掉它会打断一个与本项目无关的项目的告警。
     要删渠道请人工确认它没有别的引用之后再单独删。"
}

# ───────────────────────── 输出：建不了的、以及必须人工做的 ─────────────────────────

print_out_of_band() {
  step "A 级里跑在 GCP 之外的 2 条 —— 本脚本建不了，也不假装建了"

  log "  ADR §8.1 的分级第一原则：**这条告警在我们的基础设施整体失效时还工作吗？**"
  log "  不工作的，不能是 A 级的唯一来源。所以 A 级 3 条里有 2 条**故意**不在 GCP。"
  log ""

  local rec id name
  for rec in "${POLICIES[@]}"; do
    case "$rec" in
      *"|oob|"*) : ;;
      *) continue ;;
    esac
    id="$(printf '%s' "$rec" | cut -d'|' -f1)"
    name="$(printf '%s' "$rec" | cut -d'|' -f3)"
    printf '\n  \033[1m%s · %s\033[0m\n' "$id" "$name" >&2
    printf '  %s\n' "$(policy_field "$id" 5)" >&2
    gate_reason "$id" | sed 's/^/    /' >&2
  done

  cat >&2 <<EOF

  ── 采购那台 VPS（约 \$5/月）之后怎么建 ────────────────────────────────
  ⚠️ 下面是**给人执行的清单**，不是脚本能代劳的部分：它要 SSH 到一台不属于本项目的机器。

  A1（Uptime Kuma 的 TCP 443 探测）
    1. docker run -d --restart=always -p 3001:3001 -v uptime-kuma:/app/data \\
         --name uptime-kuma louislam/uptime-kuma:1        # tag 钉大版本，不要 latest
    2. 给每个 bp-node-* 建一个 TCP Port monitor，端口 443，
       **周期 60 秒**（从 monitoring §7 的 300 秒改上来，A 级判据需要），
       Retries = 3（连续 3 次失败 = 3 分钟才报）。
    3. resendInterval 设成每 5 次检查重发一次 = 每 5 分钟 —— 它补的是 0-30 分钟那一段，
       GCP 的 renotifyInterval 下限是 30 分钟，用 30 分钟去催一个 30 分钟的承诺等于没催。
       ⚠️ Kuma 的 resendInterval 字段名与单位来自软件 UI，**待核实**。
    4. 只有**全部节点同时失败**才当 A 级（单节点失败是 B 级的事）。
    5. 再加一条 dead man's switch：VPS 上 cron 每 5 分钟 curl 一次 healthchecks.io ——
       一台死掉的 Kuma 不会记录自己的死亡，而盲区分钟的记账需要一个外部权威源（ADR §12.4）。

  A3（证书签发者核对）
    1. 把本仓库的 infra/scripts/check-cert-issuer.sh scp 到同一台 VPS。
    2. cron 每日跑一次，加 --require-targets（清单空了要变成响亮的失败而不是安静的空跑）。
    3. 🔴 但它在那台机器上**不能走 gcloud logging write** —— 走了就又是自我引用。
       改成失败时用 msmtp 直接发信到 email#1 + email#2 + 推送通道。
       （--no-log 只关掉写日志，判定与退出码照旧；发信那一段要自己包一层。）
    4. 判定规则一字不改地照抄 monitoring §8：只校验 O 不校验 CN
       —— LE 会轮换中间证书（R10/R11/E5/E6），钉 CN 会周期性误报，而误报会让人关掉这条告警。
    5. 纪律：**任何域名上线后 24 小时内必须加进这个 cron 的清单**，否则它静默地只覆盖一半。

  ── 演练之前，这两条都不算存在（ADR §11.1 规矩一）──────────────────
  建策略的最后一步不是 create 成功，是 docs/04-ops/alert-drill-ledger.md 里多了一行送达记录。
  monitoring §14 末条原话：一条从未被真正触发过的告警链路，应当默认视为不工作。
EOF
}

print_armed_reminder() {
  step "🔴 metric-absence 型告警：建成 != armed"

  cat >&2 <<EOF
  本脚本里两条 absence 型告警（A2 的条件 2、B1）都按 groupByFields=["metric.label.node_id"] 分组。
  两条纪律，缺一条这两个策略就是哑的：

  ① **为什么必须按 node_id 分组**（monitoring §5.1 坑二）：
     不分组的话，「8 个节点里挂了 1 个」不会触发缺失 —— 总数仍然 >0。
     node_id 是本项目唯一被允许的实体标签，因为节点数有硬上限（<=10，monitoring §3.1）。
     ⚠️ 失效条件：节点数一旦不再有上限，这条允许要立刻撤回（ADR §16 第 7 条）。

  ② 🔴 **新节点首次上报之后，必须人工确认 time series 已经出现，这条告警才算 armed。**
     metric absence 要求该 time series **曾经有过数据**。monitoring §5.1 原话：
     一个从未上报过的新节点不会触发这条告警 ——「它在监控眼里根本不存在」。

     确认命令（只读）：
       gcloud logging read 'jsonPayload.message="bp_node_alive"' \\
         --project=${PROJECT_ID} --freshness=10m --limit=5 \\
         --format='table(timestamp, jsonPayload.node_id)'
     然后在 Metrics Explorer 上确认该 node_id 的 series 真的出现了。

     ADR §14 把这一步写成 ADR 0007 §9.1 **阶段 2 的验收标准**而不是一条纪律 ——
     写成纪律只是一句话，写成验收标准它才是一个门。

  ③ 顺序纪律（ADR §10.1）：日志指标**不追溯**。节点建成之后、开始服务之前，
     必须先部署带心跳日志的 bp-api → 确认指标有数据 → 再建 A2/B1 → 再人工确认 armed。
     2026-08-17 -> 08-21 已经因为这条顺序错过一次，4 天数据永久缺失（roadmap B42）。
EOF
}

print_coverage() {
  step "覆盖率对照：ADR 0014 要求多少，本脚本能建多少"

  local rec status line id name
  local created=0 not_created=0

  printf '\n  %-5s %-32s %s\n' "ID" "策略名" "结果" >&2
  printf '  %s\n' "----- -------------------------------- --------------------" >&2
  for line in "${COVERAGE[@]}"; do
    id="$(printf '%s' "$line" | cut -d'|' -f1)"
    name="$(printf '%s' "$line" | cut -d'|' -f2)"
    status="$(printf '%s' "$line" | cut -d'|' -f3)"
    printf '  %-5s %-32s %s\n' "$id" "$name" "$status" >&2
    case "$status" in
      已创建|已存在) created=$((created + 1)) ;;
      *) not_created=$((not_created + 1)) ;;
    esac
  done

  cat >&2 <<EOF

  ── 数字 ────────────────────────────────────────────────────────────
  ADR 0014 §8.1 要求          ：**14 条**（A 级 3 + B 级 11）
  ADR 0014 §9 逐条列出        ：**13 条**（A1-A3 + B1-B10）+ 2026-09-02 批准记录追加 **B11-B13**
  ⚠️ 这里有一处 **ADR 自身的内部计数不一致**，不是本脚本的取舍：
     §8.1/§8.2/§9.2 的标题都写「B 级 11 条」，而 §9.2 的表只列出 B1-B10 共 10 条；
     §13.1 的成本表又按「11 条 GCP 策略（A2 + B1-B10）= 12 个指标引用」算钱。
     后者与逐条列出的 13 条自洽。**本脚本按逐条列出的实现，并把这处差异登记在这里** ——
     差的那 1 条 B 级没有名字、没有指标、没有阈值，凭空建不出来。

  跑在 GCP 之外，脚本建不了：**2 条**（A1 Kuma / A3 证书 cron）→ 需要一台未采购的 VPS
  GCP 侧可建上限              ：**14 条**（A2 + B1-B13）
  本次实际创建                ：**${created} 条**
  未创建                      ：**${not_created} 条**（逐条原因见上表与下面）

  ── 差额的原因，逐条 ────────────────────────────────────────────────
EOF

  for rec in "${POLICIES[@]}"; do
    case "$rec" in
      *"|ready|"*) continue ;;
    esac
    id="$(printf '%s' "$rec" | cut -d'|' -f1)"
    name="$(printf '%s' "$rec" | cut -d'|' -f3)"
    printf '\n  \033[1m%s · %s\033[0m\n' "$id" "$name" >&2
    gate_reason "$id" | sed 's/^/    /' >&2
  done

  cat >&2 <<'EOF'

  ── C 级：0 条策略，这是裁决不是遗漏（ADR §9.3）────────────────────
  bp_node_id_mismatch / bp_api_429 / bp_task_idem_skip / bp_admin_authz_fail /
  request_latencies P95 / startup_latencies P95 / cloudtasks queue depth /
  database memory utilization / bp_sub_fetch —— 全部只进看板与每月一次批处理巡检。
  C 级不是被砍掉的告警，它是一条**零成本车道**：在不分级的旧框架下，一个新信号想被记录，
  唯一的代价形式就是「获得凌晨叫醒权」，所以只能不断说不。
  ⚠️ 其中 bp_admin_authz_fail 有一个写死的失效条件：后台上线且有真实登录之后，
     必须重新定阈值（建议 >=5/10 min）并升为 B 级。

  ── 别把「脚本跑完了」当成「告警建好了」──────────────────────────
  ADR §11.1 规矩一：一条告警在被人为触发过、并且送达被记录之前，**不算存在**。
  每建一条策略，当天完成演练并记 docs/04-ops/alert-drill-ledger.md，不要攒着一起做（§15.2）。
  ledger 里有两个字段最容易被跳过，而它们是全链路上最贵的两个：
  「手机响铃」要与「送达」分开列；「勿扰模式」必须为开至少一次 ——
  一次在勿扰模式下演练成功的告警，才算真的能在凌晨三点叫醒人。
EOF
}

# ───────────────────────── 用法 ─────────────────────────

usage() {
  cat <<'EOF'
用法: setup-alerts.sh [选项]

按 ADR 0014（**已批准**，2026-09-02）的 A/B/C 三级，在 oratis-491316 上建通知渠道与告警策略。

   dry-run 是安全的：它不发任何触达 GCP 的调用（连只读的都不发）；
   唯一例外是读本机 gcloud 配置的 `gcloud config get-value project`。

模式（默认 dry-run）:
  --dry-run          只打印将要执行的命令与策略清单。**默认**
  --apply            真的创建通知渠道与告警策略（需手打确认串）
  --delete           删除项目里全部 bp-* 告警策略（需手打确认串；**不删通知渠道**）

选项:
  --email1=<地址>    email#1（B 级唯一通道）。不给就从项目里已有的 email 渠道里认第一条
  --email2=<地址>    email#2（只接 A 级的语义隔离箱，ADR §12.2）。不给就不建
  --emit-json=<目录> 把每条策略的 JSON 落盘（本地文件，不碰 GCP）。
                     monitoring §5.2 要求生成物入库：code review 时能看见告警配置的变化本身
  --include-blocked=<id,id>
                     强开被门挡住的策略（如 B3）。**你是在断言那些前置已经解除** ——
                     脚本会把它断言掉的每一条门原样打印出来
  --skip-channels    跳过通知渠道那一步（渠道已经建好、只想补策略时用）
  --project=<id>     GCP 项目 ID。必须是 oratis-491316
  -h, --help         显示本帮助

退出码:
  0  成功（dry-run 永远走这一支，除非用法错）
  1  有策略创建 / 删除失败
  2  用法或环境错误（含项目不匹配）

例:
  ./setup-alerts.sh                                  # 看它打算干什么
  ./setup-alerts.sh --emit-json=infra/alerts         # 生成 JSON 入库，仍然不碰 GCP
  ./setup-alerts.sh --apply --email2=xxx@qq.com      # 批准之后才跑
  ./setup-alerts.sh --delete                         # 先看它打算删什么（默认 dry-run）
  ./setup-alerts.sh --delete --apply                 # 真的删
EOF
}

# ───────────────────────── 主流程 ─────────────────────────

main() {
  local arg want_apply=0 want_delete=0
  for arg in "$@"; do
    case "$arg" in
      --dry-run)            EXPLICIT_DRY_RUN=1 ;;
      --apply)              want_apply=1 ;;
      --delete)             want_delete=1 ;;
      --email1=*)           EMAIL1_ADDR="${arg#*=}" ;;
      --email2=*)           EMAIL2_ADDR="${arg#*=}" ;;
      --emit-json=*)        EMIT_DIR="${arg#*=}" ;;
      --include-blocked=*)  INCLUDE_BLOCKED="${arg#*=}" ;;
      --skip-channels)      SKIP_CHANNELS=1 ;;
      --project=*)          PROJECT_ID="${arg#*=}" ;;
      -h|--help)            usage; exit 0 ;;
      *)                    usage >&2; die "未知参数：$arg" ;;
    esac
  done

  # --delete --apply = 真的删；--delete 单独给 = 删的 dry-run。
  # 默认 dry-run 这条纪律对删除同样成立，而且在删除上它更要紧。
  if [ "$want_delete" -eq 1 ]; then
    ACTION="delete"
    if [ "$want_apply" -eq 1 ] && [ "$EXPLICIT_DRY_RUN" -eq 0 ]; then
      DRY_RUN=0
    fi
  elif [ "$want_apply" -eq 1 ]; then
    ACTION="create"
    if [ "$EXPLICIT_DRY_RUN" -eq 1 ]; then
      die "--apply 与 --dry-run 同时给了。想看计划就别给 --apply（dry-run 本来就是默认）。"
    fi
    DRY_RUN=0
  fi

  guard_project

  log "项目 : ${PROJECT_ID}（gcloud 活动项目已核对一致）"
  log "动作 : $ACTION"
  if [ "$DRY_RUN" -eq 1 ]; then
    log "模式 : DRY-RUN（不发任何 gcloud，含只读）"
  else
    log "模式 : \033[31mAPPLY —— 会真的改动 GCP\033[0m"
  fi
  log ""
  log "ADR 0014：已批准（2026-09-02）。A2 / B10 仍被门挡着，理由见结尾对照表。"

  if [ "$ACTION" = "delete" ]; then
    if [ "$DRY_RUN" -eq 0 ]; then
      confirm_typed "将删除 ${PROJECT_ID} 里**全部 bp-* 告警策略**。
      三条 lisa-* 策略不属于本项目，脚本会逐条跳过（AGENTS.md §4 红线）。
      通知渠道一律不删。" "$CONFIRM_DELETE"
    fi
    delete_policies
    [ "$FAILED_N" -eq 0 ] || exit 1
    exit 0
  fi

  if [ "$ACTION" = "create" ]; then
    confirm_typed "将在 ${PROJECT_ID} 上创建通知渠道与告警策略（ADR 0014 已批准，2026-09-02）。
      仍有两处「待核实 / 需实测」直接决定策略长什么样（A2 的 AND 混用两种条件类型、
      B10 的同比 MQL），它们默认被门挡着；--include-blocked 强开前先读 gate_reason。" "$CONFIRM_APPLY"
  fi

  if [ "$SKIP_CHANNELS" -eq 1 ]; then
    step "通知渠道（--skip-channels：跳过）"
    CH_EMAIL1="projects/${PROJECT_ID}/notificationChannels/<EMAIL1_ID>"
    CH_EMAIL2="projects/${PROJECT_ID}/notificationChannels/<EMAIL2_ID>"
    CH_PUSH="projects/${PROJECT_ID}/notificationChannels/<PUSH_ID>"
    CH_PUBSUB="projects/${PROJECT_ID}/notificationChannels/<PUBSUB_ID>"
    warn "策略 JSON 里的通道会是占位符，直接 apply 会被 API 拒。只在生成 JSON 时用这个开关。"
  else
    step "通知渠道"
    log "  ADR §8.3 的通道矩阵（**不是 monitoring §4 的「每条都挂两条」**，§14 已改）："
    log "    A 级 -> email#1 + email#2（只接 A 级）+ 原生推送 + Pub/Sub（事后取证）"
    log "    B 级 -> email#1 单通道"
    log "    C 级 -> 不发"
    resolve_channels || true
  fi

  create_policies
  print_out_of_band
  print_armed_reminder
  print_coverage

  step "结果"
  log "  创建 ${CREATED_N} 条 / 跳过 ${SKIPPED_N} 条 / 失败 ${FAILED_N} 条"
  if [ "$DRY_RUN" -eq 1 ]; then
    log "  （DRY-RUN：什么都没改。要真的建，先让 ADR 0014 被批准，再跑 --apply。）"
  fi
  [ "$FAILED_N" -eq 0 ] || exit 1
  exit 0
}

main "$@"

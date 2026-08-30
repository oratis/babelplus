#!/usr/bin/env bash
#
# setup-scheduler.sh —— 定时面的**唯一可执行形式**：八条 Cloud Scheduler + 两条 Cloud Tasks 队列
#                       + `check-cert-issuer.sh` 的每日调度（roadmap B42 欠的那一半）
#
# 事实源（凡冲突，**契约与代码优先**，文档次之 —— 契约能被 CI 卡住，文档不能）：
#   openapi/openapi.yaml `/internal/tasks/*`（**路径与方法的唯一事实源**，9 个 Run*Task）
#   docs/02-architecture/api-contract.md §7（内部面：Google OIDC，校验 aud / iss / email）
#   docs/04-ops/monitoring.md §5 第 11/12 条（队列积压、Scheduler 失败告警）· §8（证书核对）
#   docs/00-overview/roadmap.md §4.2「六条 Cloud Scheduler + 一条 Cloud Tasks 入账队列」
#                               · §4.3 出口标准第 6 条（封禁/配额耗尽/到期的生效时间）
#   docs/05-adr/0005-database-selection.md §6.2（连接数硬公式 —— 队列并发的推导在这里）
#   docs/04-ops/deploy.md §8.1（audience 一律用 run.app URL）
#
# ─────────────────────────────────────────────────────────────────────────────
# 一 · 为什么是**八条**而不是 roadmap 写的「六条」
# ─────────────────────────────────────────────────────────────────────────────
#
# roadmap §4.2 与 deploy.md §8.2 都写「六条」，那两处写于 openapi 契约定稿之前。
# 契约里标了 `Scheduler` 驱动的端点有 7 个，其中 `stat-rollup` 要 hourly + daily 两条
# （契约里**没有** `grain` 参数，所以不能像 deploy.md 那样靠 query 区分，只能建两条打同一路径）：
#
#     8 = 7 个 Scheduler 驱动的端点 + stat-rollup 多出来的那一条
#
# 另外 2 个端点（`traffic-batch` / `mail-send`）是 **Cloud Tasks 驱动，不建 Scheduler**：
#   · traffic-batch 由 `/push` 在服务端入队（幂等键 batch_id 也是服务端生成的）；
#   · mail-send 由业务按需入队。
#   给它们建定时任务是错的 —— 定时器不知道该带什么 body，打过去只会得到一堆 4xx。
# 这就是 9 个 `Run*Task` 只对应 8 条 Scheduler 的原因，不是漏了两个。
#
# ⚠️ **与 infra/deploy/setup-infra.sh 的重叠是已知债**：`--step=tasks` 建同两个队列，
#    `--step=postdeploy` 建同八条任务，参数刻意逐字相同（两边都是 create-or-update，
#    先跑哪个都收敛到同一状态）。改一处必须改两处，**没有任何机制提醒你改全**
#    —— 与 infra/deploy/README.md §6 第 1 条是同一类债。
#    本脚本存在的理由是 setup-infra 的 postdeploy 把 IAM、Pub/Sub 订阅、备份作业捆在一起，
#    「只想核对/重建定时面」时没有粒度；而且它没有 --delete，也没有覆盖 B42 的证书调度。
#
# ─────────────────────────────────────────────────────────────────────────────
# 二 · OIDC 的 audience 必须是 `*.run.app`，不是镜像域名 —— 这是踩过的坑
# ─────────────────────────────────────────────────────────────────────────────
#
# `--oidc-token-audience` 是**签发时写死进 token 的字符串**，校验方拿它和自己的期望值做等值比较。
# 校验发生在两处，两处期望的都是 Cloud Run 服务自身的 `*.run.app` URL：
#   1. Cloud Run 的 IAM 层（`roles/run.invoker` + OIDC）；
#   2. 我们自己的 handler（api-contract §7：校验 `aud` = 本服务 URL、`iss`、`email`）。
#
# 把 audience 写成镜像域名 / 自定义域名会坏在三处：
#   1. **前面可能挂着 CDN 或反代**（ADR 0003 的域名池就是这么用的）。请求到 Cloud Run 时
#      Host 已经被代理改写，而 token 里的 aud 还是签发时那个字符串 —— 不一致 → 403。
#      现象是「定时任务全部 403，但用浏览器访问同一个域名一切正常」，极难定位。
#   2. **域名池会轮换**（ADR 0003 §5）。aud 跟着域名走 = 每换一次域名要重建全部
#      Scheduler / Tasks 配置，而漏掉一条的后果是那一条静默停摆。
#   3. **故障隔离**（deploy.md §8.1）：打 run.app 直连，意味着公开域名被封时，
#      入账 / 重置 / 到期 / 聚合全部照常运行。这是 system-design §5.3
#      「控制面故障不得升级为数据面故障」在控制面内部的同一条原则。
#
# 所以本脚本**运行时**从 `gcloud run services describe` 取 URL（不写死项目号 ——
# deploy.md §8.1 里那个硬编码的 `bp-api-2360090741...` 在项目号变化时会静默失配），
# 并断言它形如 `https://*.run.app`，不是就直接退出。
#
# ─────────────────────────────────────────────────────────────────────────────
# 三 · cron 频率 ⇄ P1 出口标准第 6 条（roadmap §4.3）
# ─────────────────────────────────────────────────────────────────────────────
#
# 出口标准第 6 条：封禁 / 配额耗尽 / 到期 三个状态各手工触发一次，
# 节点侧生效时间分别 ≤ 60 s / ≤ 60 s / ≈ 6 分钟。**扫描周期直接决定它能不能满足**：
#
#   封禁     ≤ 60 s : 封禁是**写操作**，handler 当场 bump `user_rev`（ADR 0006 §11.2）。
#                     上界 = 节点轮询周期 = 60 s。
#                     ⇒ **这一条不经过任何定时任务，改本文件的 cron 不会影响它。**
#   配额耗尽 ≤ 60 s : 节点每 60 s 一次 `/push` → 服务端入 bp-traffic-ingest 队列 →
#                     traffic-batch 在**跨越 transfer_enable 阈值的那一次**bump `user_rev`。
#                     上界 = 入队到派发的延迟 + 下一次轮询。
#                     ⇒ **队列一旦积压，这一条立刻被击穿**，而积压是可观测的
#                       （monitoring §5 第 11 条：queue/depth > 100 告警）。
#                       派发速率的余量：8 节点 × 1 次/分 = 0.13 次/秒，
#                       而 --max-dispatches-per-second=10 ⇒ 约 75 倍余量。
#   到期     ≈ 6 min: 🔴 到期是**时间驱动**的状态变化，**没有任何写操作会触发 bump**
#                     （api-contract §7 / ADR 0006 §11.2 第 4 条，ETag 设计里最容易漏的一条）。
#                     只能靠 expire-check 扫出来。
#                     上界 = 扫描周期 + 轮询周期 = 5 min + 60 s ≈ 6 min。
#
#                     ⇒ **expire-check 的 cron 必须是 `*/5`。**
#                       deploy.md §8.2 写的 `*/10` 会给出 10 min + 60 s ≈ 11 min，
#                       **直接违反出口标准第 6 条**，而且是静默违反 ——
#                       没有任何告警会因为「扫描周期太长」而响。
#                     ⇒ 反过来也别随手调快：每 1 分钟扫一次，代价是每天多 1152 次
#                       全表扫描（P1 规模下无所谓），收益是把 6 分钟压到 2 分钟；
#                       契约把 6 分钟的经济代价算过了（≈ 612 MB / ¥0.06–0.15 每人次），
#                       所以**这是一次已经拍过板的取舍，不要在这里重开**。
#
# ─────────────────────────────────────────────────────────────────────────────
# 四 · 队列并发上限的算术（ADR 0005 §6.2，不是拍脑袋）
# ─────────────────────────────────────────────────────────────────────────────
#
#   硬公式：max_instances × pool_max + 运维预留 ≤ max_connections − superuser_reserved
#   代入  ：      8       ×     2    +    6      ≤        25       −        3        = 22  ✓
#           占用 16，余量 6（留给手工 psql、迁移工具、监控采集）。
#
#   🔴 两个队列的并发**不是额外的实例预算，它们和用户请求共用同一份 --max-instances=8**。
#      bp-traffic-ingest max-concurrent-dispatches = 4
#    + bp-mail-send      max-concurrent-dispatches = 2
#    = 最多 6 个并发的内部请求。
#
#      Cloud Run 侧 --concurrency=80，所以这 6 个并发在理论上 1 个实例就吃得下；
#      最坏情况（冷启动把它们打散到不同实例）= 6 实例 × 2 连接 = 12 连接，
#      仍在 16 的预算内，且还给用户面留了 2 个实例。
#
#      把 max-concurrent-dispatches 调到 8 以上 = 最坏情况下 8 个实例全被内部任务占满，
#      用户请求只能排队，然后 429 —— 而 monitoring §5 第 4 条把**任何** 429 都当异常。
#      换句话说：调大这个数字，代价会以「用户被拒绝」的形式出现，不是「任务慢一点」。
#
#      重试上限（--max-attempts）为什么两个不一样：
#        · traffic-ingest = 10：丢一批流量 = 少收一次钱且对不上账（出口标准第 5 条要求差异 < 1%），
#          而 handler 有 batch_id 抢占式幂等，重投是安全的 —— 值得多试几次。
#        · mail-send      = 5 ：ESP 侧通常自带速率限制，硬重试只会把自己打进对方的黑名单；
#          且发信失败还有下一次 remind-sweep 兜底。**这两个数字是设定值，无实测依据。**
#
# ─────────────────────────────────────────────────────────────────────────────
# 五 · 纪律（与 infra/ 下其它脚本逐条一致）
# ─────────────────────────────────────────────────────────────────────────────
#   1. set -euo pipefail
#   2. 开头断言项目 = oratis-491316，不一致直接退出（项目共享，跑错会动到别人的资源）
#   3. 每条 gcloud 显式 --project，不依赖 `gcloud config set project`
#   4. 危险操作要手打确认串；**默认 --dry-run**，写操作要显式 --apply
#
#   守卫代码在每个脚本里各复制一份，是**故意的**：每个脚本要能单独 scp 出去跑，
#   且单独具备「打错项目就拒绝」的能力。代价见 infra/deploy/README.md §6 第 1 条。

set -euo pipefail

# ───────────────────────── 防呆常量 ─────────────────────────

readonly EXPECTED_PROJECT_ID="oratis-491316"
readonly REGION="us-central1"
# 统计口径的「一天」统一按 Asia/Shanghai（deploy.md §8.2 第 3 条：口径只定这一次）。
readonly TIME_ZONE="Asia/Shanghai"

readonly RUN_SERVICE="bp-api"
readonly SA_TASKS="bp-tasks-sa"          # Scheduler / Tasks / Pub-Sub push 的 OIDC 主体
readonly QUEUE_TRAFFIC="bp-traffic-ingest"
readonly QUEUE_MAIL="bp-mail-send"

# B42：check-cert-issuer.sh 的每日调度。形态选择与取舍见 step_cert() 的注释。
readonly CERT_JOB="bp-cert-issuer-check"          # Cloud Run **Job**（不是 HTTP 端点）
readonly CERT_SCHED="bp-cert-issuer-check-daily"  # 触发它的 Scheduler 任务

# 危险操作的确认串。**不是 y/N** —— y/N 是肌肉记忆，手打这些字符串不是。
readonly CONFIRM_APPLY="setup-scheduler"
readonly CONFIRM_DELETE="delete-bp-scheduler"
readonly CONFIRM_DELETE_QUEUES="delete-bp-queues-7d"

# ───────────────────────── 八条 Scheduler 任务 ─────────────────────────
#
# 格式：名字|cron|路径|说明
#
# 🔴 路径与频率**以 openapi/openapi.yaml 为准**。已知与 deploy.md §8.2 的四处差异：
#      · expire-sweep → 契约里叫 expire-check，且 10 分钟 → **5 分钟**（见上文「三」）
#      · rollup?grain=day → 契约里叫 stat-rollup 且**没有 grain 参数** ⇒ 建两条打同一路径
#      · order-timeout 2 分钟 → 契约 summary 写的是 1 分钟
#      · 契约里还多了 chain-scan 与 remind-sweep（deploy.md 的六条表里没有）
#
# 🔴 谁必须 bump user_rev（ADR 0006 §11.2，写在这里是因为**改 cron 的人往往正是需要知道这条的人**）：
#      expire-check  必须 bump —— 不 bump 的话到期用户永远不从节点列表消失（= 免费无限上网）
#      traffic-reset 必须 bump —— 重置让超额用户重新可用，改变了可见用户集合
#      stat-rollup   **绝不能** bump —— 它只读不改可见集合，bump 会让全部节点无谓失效缓存
#
# 🔴 traffic-reset 的「置零」与「推进 next_reset_at」必须在**同一条 UPDATE 里**完成。
#    拆成两条语句时，一次重投会把用户流量**二次清零** —— 这是全部定时任务里最危险的一条。
SCHEDULER_JOBS=(
  "bp-alive-gc|*/5 * * * *|/internal/tasks/alive-gc|在线态清理：DELETE user_alive WHERE seen_at < now()-5min（天然幂等）"
  "bp-expire-check|*/5 * * * *|/internal/tasks/expire-check|到期扫描：**必须 5 分钟**，出口标准第 6 条的 6 分钟 = 5min 扫描 + 60s 轮询；必须 bump user_rev"
  "bp-order-timeout|* * * * *|/internal/tasks/order-timeout|订单超时：置 expired，但收款地址继续监听 >=24h，到账入余额"
  "bp-chain-scan|* * * * *|/internal/tasks/chain-scan|链上扫描：幂等键 (chain,txid,log_index)；回调不可信，以链上/网关查单为权威"
  "bp-traffic-reset|7 * * * *|/internal/tasks/traffic-reset|流量周期重置：置零与推进 next_reset_at 必须同一条 UPDATE；必须 bump user_rev"
  "bp-stat-rollup-hourly|25 * * * *|/internal/tasks/stat-rollup|统计聚合（小时）：ON CONFLICT DO UPDATE 覆盖不累加；绝不 bump user_rev"
  "bp-stat-rollup-daily|20 1 * * *|/internal/tasks/stat-rollup|统计聚合（日）：口径统一按 Asia/Shanghai"
  "bp-remind-sweep|0 10 * * *|/internal/tasks/remind-sweep|到期/流量提醒扫描：幂等键 (user_id, remind_kind, day)"
)

# 契约里另外两个端点：Cloud Tasks 驱动，**故意不建 Scheduler**（理由见文件头「一」）。
# 只用于结尾清单里把「为什么只有 8 条」说清楚，避免下一个人以为漏了。
TASKS_ONLY_ENDPOINTS=(
  "/internal/tasks/traffic-batch|${QUEUE_TRAFFIC}|由 /push 在服务端入队，body 带服务端生成的 batch_id"
  "/internal/tasks/mail-send|${QUEUE_MAIL}|由业务按需入队，body 带 mail_queue.id"
)

# ───────────────────────── 运行时开关 ─────────────────────────

PROJECT_ID="${PROJECT_ID:-$EXPECTED_PROJECT_ID}"
MODE="dry-run"          # dry-run（默认）| apply | delete
ONLY="all"              # all | queues | scheduler | cert
ASSUME_YES=0
INCLUDE_QUEUES=0        # --delete 时是否连队列一起删（见 step_delete 的 7 天墓碑警告）
AUDIENCE=""             # --audience= 覆盖；默认运行时从 gcloud 取
CERT_IMAGE=""           # --cert-image=<镜像引用>：有它才创建 Cloud Run Job bp-cert-issuer-check
FAIL_N=0

# 结尾清单用。两个数组分别是「本次做了什么」与「人工还要做什么」。
SUMMARY_DONE=()
SUMMARY_TODO=()

# ───────────────────────── 通用工具（与 infra/ 下其它脚本刻意保持重复）─────────────────────────

log()  { printf '%s\n' "$*" >&2; }
step() { printf '\n\033[1m▸ %s\033[0m\n' "$*" >&2; }
ok()   { printf '  \033[32m✓\033[0m %s\n' "$*" >&2; }
skip() { printf '  · %s\n' "$*" >&2; }
warn() { printf '  \033[33m⚠\033[0m %s\n' "$*" >&2; }
fail() { printf '  \033[31m✗ %s\033[0m\n' "$*" >&2; FAIL_N=$((FAIL_N + 1)); }
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

# run · 有副作用的命令走这里；非 apply/delete 模式下只打印。
# 只读查询（describe / list）**不**走 run —— dry-run 下照常执行，
# 这样预演也能看到「哪些已存在、要 create 还是 update」的真实状态。
run() {
  if [ "$MODE" = "dry-run" ]; then
    local _a
    printf '  [dry-run] ' >&2
    for _a in "$@"; do qq "$_a" >&2; printf ' ' >&2; done
    printf '\n' >&2
    return 0
  fi
  "$@"
}

# confirm_typed · 危险操作的二次确认：必须原样敲出确认串，回车不算数。
confirm_typed() {
  local expect="$1" what="$2" answer=""
  if [ "$MODE" = "dry-run" ]; then
    skip "[dry-run] 跳过确认：$what"
    return 0
  fi
  if [ "$ASSUME_YES" -eq 1 ]; then
    warn "--yes 已跳过确认：$what"
    return 0
  fi
  if [ ! -t 0 ]; then
    die "需要交互确认但 stdin 不是终端：$what（非交互场景请显式加 --yes）"
  fi
  printf '\n  ⚠️  %s\n  确认请原样输入 %s ：' "$what" "$expect" >&2
  read -r answer || true
  [ "$answer" = "$expect" ] || die "确认串不匹配，已中止。"
}

guard_project() {
  if [ "$PROJECT_ID" != "$EXPECTED_PROJECT_ID" ]; then
    die "PROJECT_ID 必须是 $EXPECTED_PROJECT_ID，当前是 \"$PROJECT_ID\"。
     这个 GCP 项目是**共享**的：里面还住着 vpn-us / vpn-jp 与三个现有 Cloud Run 服务。
     跑错项目不是「什么都不会发生」，是「动到别人的资源」（roadmap R7 的爆炸半径）。"
  fi

  # 每条 gcloud 都显式带 --project，所以 `gcloud config` 的当前项目**不影响**本脚本的行为。
  # 这里只提示不拦截：拦截等于把「必须先 gcloud config set project」变成隐藏前置条件，
  # 而 deploy.md §2 的原话是「gcloud config set project 打错项目是本文最现实的事故源」——
  # 我们的做法是让它变得无关紧要，不是让它变成另一道闸。
  local active
  active="$(gcloud config get-value project 2>/dev/null || true)"
  if [ -n "$active" ] && [ "$active" != "$PROJECT_ID" ] && [ "$active" != "(unset)" ]; then
    warn "gcloud 当前项目是 $active，本脚本一律显式 --project=$PROJECT_ID（不受影响，仅提示）"
  fi
}

guard_bp_prefix() {
  local name
  for name in "$@"; do
    case "$name" in
      bp-*|bp_*) : ;;
      *) die "资源名 \"$name\" 不带 bp- 前缀，违反 as-built §2.1 第 1 条。" ;;
    esac
  done
}

need_cmd() {
  command -v "$1" >/dev/null 2>&1 || die "缺少命令：$1"
}

want() {
  [ "$ONLY" = "all" ] || [ "$ONLY" = "$1" ]
}

usage() {
  cat <<'EOF'
用法: setup-scheduler.sh [选项]

建/更新 babel.plus 的定时面：**八条 Cloud Scheduler + 两条 Cloud Tasks 队列**，
外加 check-cert-issuer.sh 的每日调度（roadmap B42）。

**默认 --dry-run。** 想真的改 GCP 必须显式 --apply，且要手打确认串 setup-scheduler。

选项:
  --dry-run          只打印将要执行的写操作（默认；只读探测照常执行）
  --apply            真的创建/更新。要求手打 setup-scheduler 确认
  --delete           删除本脚本建的 Scheduler 任务。要求手打 delete-bp-scheduler
  --include-queues   配合 --delete：连两个 Cloud Tasks 队列一起删。
                     ⚠️ 队列名删除后有 **7 天墓碑期**，期间无法用同名重建（GCP 行为）。
                     需要第二次确认 delete-bp-queues-7d
  --only=<步骤>      queues | scheduler | cert | all（默认 all）
  --audience=<url>   覆盖 OIDC audience。**必须形如 https://*.run.app**，
                     否则脚本拒绝继续（见 --help 末尾「为什么」）
  --cert-image=<ref> 有它才创建 Cloud Run Job bp-cert-issuer-check；不给就只打印缺什么
  --project=<id>     GCP 项目 ID。必须是 oratis-491316
  --yes              跳过交互确认（CI 用；危险操作仍会打印全部细节）
  -h, --help         显示本帮助

退出码:
  0  全部成功（或 dry-run 正常结束）
  1  有步骤失败
  2  用法或环境错误（缺 gcloud、项目 ID 不对、audience 不是 run.app……）

典型顺序:
  ./infra/deploy/deploy-api.sh --no-build --tag=<sha> --promote   # 先要有 bp-api，才有 audience
  ./infra/scripts/setup-scheduler.sh                              # 看一遍要做什么
  ./infra/scripts/setup-scheduler.sh --apply
  gcloud scheduler jobs list --project=oratis-491316 --location=us-central1   # 人工复核

为什么 audience 必须是 *.run.app（这是踩过的坑）:
  --oidc-token-audience 是签发时写死进 token 的字符串，校验方做等值比较，
  而校验方（Cloud Run 的 IAM 层 + 我们自己的 handler）期望的都是服务自身的 run.app URL。
  写成自定义域名时，前面挂的 CDN / 反代会改写 Host，token 里的 aud 却不会跟着变 —— 403。
  现象是「定时任务全部 403，浏览器访问同一域名一切正常」，极难定位。
  另外域名池会轮换（ADR 0003 §5），aud 绑域名 = 每换一次域名要重建全部定时配置。

它建了什么、没建什么:
  建   8 条 Scheduler：alive-gc / expire-check / order-timeout / chain-scan /
       traffic-reset / stat-rollup(hourly+daily) / remind-sweep
  建   2 个 Cloud Tasks 队列：bp-traffic-ingest / bp-mail-send
  不建 traffic-batch 与 mail-send 的 Scheduler —— 它们由服务端入队，
       定时器不知道该带什么 body，打过去只会得到一堆 4xx
  不建 bp-db-dump-weekly（属于 setup-infra.sh --step=postdeploy）
  不建 log-based metrics 与告警策略（monitoring §3/§5 至今没有脚本）
EOF
}

# gcloud_json <参数...> · 只读取 JSON。失败返回 1 并输出空。
#
# ⚠️ gcloud 会把噪音写进 **stdout** 且仍然 exit 0，于是 --format=json 的输出不是合法 JSON
#    （2026-08-17 实测：Python 3.9 环境下 `gcloud artifacts repositories list` 的 stdout
#    第一行是 importlib.metadata 的报错）。所以统一剥掉第一个 [ 或 { 之前的所有内容。
gcloud_json() {
  local out
  if ! out="$("$@" --format=json 2>/dev/null)"; then
    return 1
  fi
  printf '%s\n' "$out" | awk 'p{print;next} /^[[{]/{p=1;print}'
}

# ───────────────────────── 前置检查 ─────────────────────────

sched_exists() {
  gcloud scheduler jobs describe "$1" \
    --project="$PROJECT_ID" --location="$REGION" >/dev/null 2>&1
}

queue_exists() {
  gcloud tasks queues describe "$1" \
    --project="$PROJECT_ID" --location="$REGION" >/dev/null 2>&1
}

# resolve_audience · OIDC audience 的唯一来源。
#
# 不写死（deploy.md §8.1 里那个硬编码的 bp-api-2360090741... 在项目号变化时会静默失配），
# 运行时从 Cloud Run 取，然后**断言它是 run.app**。
resolve_audience() {
  if [ -z "$AUDIENCE" ]; then
    AUDIENCE="$(gcloud run services describe "$RUN_SERVICE" \
      --project="$PROJECT_ID" --region="$REGION" \
      --format='value(status.url)' 2>/dev/null || true)"
  fi

  if [ -z "$AUDIENCE" ]; then
    if [ "$MODE" = "dry-run" ]; then
      # 占位符也必须形如 run.app，否则下面的断言会把 dry-run 自己拦下来。
      AUDIENCE="https://${RUN_SERVICE}-PLACEHOLDER-uc.a.run.app"
      warn "[dry-run] 取不到 $RUN_SERVICE 的 URL，用占位符 $AUDIENCE 继续打印命令"
    else
      die "$RUN_SERVICE 尚未部署，取不到 run.app URL，因此**无法确定 OIDC audience**。
     先跑 ./infra/deploy/deploy-api.sh --promote，再回来跑本脚本。
     （不允许用猜的 URL 建任务：aud 错了的任务会 403，而 403 只在 Scheduler 的执行日志里可见 ——
       monitoring §5 第 12 条那条告警目前**还没有建**，所以它会静默失败。）"
    fi
  fi

  # 去掉可能的结尾斜杠：`https://x.run.app/` 与 `https://x.run.app` 在 aud 的等值比较里
  # 是**两个不同的字符串**，而 Cloud Run 期望的是后者。
  AUDIENCE="${AUDIENCE%/}"

  case "$AUDIENCE" in
    https://*.run.app) : ;;
    *)
      die "OIDC audience 必须形如 https://<服务>.<...>.run.app，当前是 \"$AUDIENCE\"。
     自定义域名 / 镜像域名**不能**当 audience：
       · 域名前面可能挂 CDN 或反代，到达 Cloud Run 时 Host 已被改写，
         而 token 里的 aud 是签发时固定的字符串 —— 不一致直接 403；
       · 域名池会轮换（ADR 0003 §5），aud 绑域名等于每换一次域名重建全部定时配置；
       · 打 run.app 直连还顺带做到故障隔离：公开域名被封时定时面照常运行。
     取正确值：gcloud run services describe $RUN_SERVICE --project=$PROJECT_ID --region=$REGION --format='value(status.url)'"
      ;;
  esac
}

preflight() {
  step "前置检查"
  log "  项目 : $PROJECT_ID"
  log "  区域 : $REGION"
  log "  模式 : $MODE（$( [ "$MODE" = "dry-run" ] && printf '不做任何写操作' || printf '**会真的改 GCP**' )）"
  log "  范围 : --only=$ONLY"

  local sa_email="${SA_TASKS}@${PROJECT_ID}.iam.gserviceaccount.com"
  if gcloud iam service-accounts describe "$sa_email" --project="$PROJECT_ID" >/dev/null 2>&1; then
    ok "服务账号 $SA_TASKS 存在"
  elif [ "$MODE" = "dry-run" ]; then
    warn "服务账号 $SA_TASKS 不存在（dry-run 继续）。先跑 ./infra/deploy/setup-infra.sh --step=iam"
  else
    die "服务账号 $SA_TASKS 不存在。先跑 ./infra/deploy/setup-infra.sh --step=iam。
     它是全部 Scheduler / Tasks 的 OIDC 主体，没有它建出来的任务一条都跑不通。"
  fi

  # 🔴 bp-tasks-sa 必须在 **bp-api 这个服务上**有 run.invoker。
  #    没有它，八条任务会全部 403 —— 而「Scheduler 任务失败」的告警（monitoring §5 第 12 条）
  #    至今没有建，所以那是一次**完全静默**的失败：面板一切正常，只是到期用户不再过期。
  #    这一条只检查不修复：授权属于 setup-infra.sh --step=postdeploy 的职责，
  #    在两个脚本里都写一遍 IAM 绑定只会制造第二处漂移源。
  local policy
  if policy="$(gcloud_json gcloud run services get-iam-policy "$RUN_SERVICE" \
                 --project="$PROJECT_ID" --region="$REGION")" && [ -n "$policy" ]; then
    if printf '%s' "$policy" \
         | jq -e --arg m "serviceAccount:${sa_email}" \
             '[.bindings[]? | select(.role == "roles/run.invoker") | .members[]?] | index($m)' \
             >/dev/null 2>&1; then
      ok "$SA_TASKS 在 $RUN_SERVICE 上有 roles/run.invoker"
    else
      warn "$SA_TASKS 在 $RUN_SERVICE 上**没有** roles/run.invoker。
     建出来的八条任务会全部 403，而且是静默失败（monitoring §5 第 12 条的告警还没建）。
     补：./infra/deploy/setup-infra.sh --step=postdeploy"
      SUMMARY_TODO+=("给 $SA_TASKS 授 $RUN_SERVICE 的 roles/run.invoker（setup-infra.sh --step=postdeploy）")
    fi
  else
    skip "取不到 $RUN_SERVICE 的 IAM 策略（未部署 / 无权限 / dry-run 未鉴权），跳过 run.invoker 核对"
  fi
}

# ───────────────────────── 步骤 1 · Cloud Tasks 队列 ─────────────────────────

step_queues() {
  step "1/3 Cloud Tasks 队列（入账 + 发信）"
  guard_bp_prefix "$QUEUE_TRAFFIC" "$QUEUE_MAIL"

  # 参数的推导见文件头「四」。这里只重复一句最要紧的：
  # 这些任务打的是**同一个** bp-api 服务，消耗的是同一份 --max-instances=8 预算。
  if queue_exists "$QUEUE_TRAFFIC"; then
    skip "队列 $QUEUE_TRAFFIC 已存在 —— 只核对不修改"
    log "    期望：max-concurrent-dispatches=4 max-dispatches-per-second=10 max-attempts=10"
    log "    实际：$(gcloud tasks queues describe "$QUEUE_TRAFFIC" --project="$PROJECT_ID" \
                    --location="$REGION" \
                    --format='value(rateLimits.maxConcurrentDispatches,rateLimits.maxDispatchesPerSecond,retryConfig.maxAttempts)' \
                    2>/dev/null || printf '(取不到)')"
  else
    run gcloud tasks queues create "$QUEUE_TRAFFIC" \
      --project="$PROJECT_ID" --location="$REGION" \
      --max-concurrent-dispatches=4 \
      --max-dispatches-per-second=10 \
      --max-attempts=10 \
      --min-backoff=5s --max-backoff=300s --max-doublings=4
    ok "队列 $QUEUE_TRAFFIC（并发 4 / 10 qps / 最多 10 次）"
    SUMMARY_DONE+=("Cloud Tasks 队列 $QUEUE_TRAFFIC —— traffic-batch 的入账通道")
  fi

  if queue_exists "$QUEUE_MAIL"; then
    skip "队列 $QUEUE_MAIL 已存在 —— 只核对不修改"
  else
    run gcloud tasks queues create "$QUEUE_MAIL" \
      --project="$PROJECT_ID" --location="$REGION" \
      --max-concurrent-dispatches=2 \
      --max-dispatches-per-second=5 \
      --max-attempts=5 \
      --min-backoff=10s --max-backoff=600s --max-doublings=3
    ok "队列 $QUEUE_MAIL（并发 2 / 5 qps / 最多 5 次）"
    SUMMARY_DONE+=("Cloud Tasks 队列 $QUEUE_MAIL —— mail-send 的发信通道")
  fi

  # 已存在的队列**故意不 update**：改并发是会立刻改变 bp-api 负载的操作
  # （ADR 0005 §6.2 的公式直接挂在这两个数字上），不该由一次「顺手重跑初始化脚本」触发。
  # 真要改：先算一遍上面那个公式，再手工 `gcloud tasks queues update`。
}

# ───────────────────────── 步骤 2 · 八条 Scheduler ─────────────────────────

# mkjob <名字> <cron> <路径> <说明>
mkjob() {
  local name="$1" cron="$2" path="$3" desc="$4"
  guard_bp_prefix "$name"

  local verb="create"
  if sched_exists "$name"; then
    verb="update"
  fi

  # --attempt-deadline=120s：单次执行的超时。
  # ⚠️ order-timeout / chain-scan 是每分钟一次，而超时是 120 秒 ⇒ **两次执行可能重叠**。
  #    这不是配置错误，是必须接受的现实（Scheduler 不保证不重叠），
  #    所以契约给每个 handler 都定了幂等键（api-contract §9.1），
  #    而 monitoring §3.2 的 bp_task_idem_skip 长期为 0 反而可疑。
  # --max-retry-attempts=3：Scheduler 自己的重试。重试是安全的，前提同上。
  run gcloud scheduler jobs "$verb" http "$name" \
    --project="$PROJECT_ID" --location="$REGION" \
    --schedule="$cron" --time-zone="$TIME_ZONE" \
    --uri="${AUDIENCE}${path}" --http-method=POST \
    --oidc-service-account-email="${SA_TASKS}@${PROJECT_ID}.iam.gserviceaccount.com" \
    --oidc-token-audience="$AUDIENCE" \
    --attempt-deadline=120s \
    --max-retry-attempts=3 --min-backoff=10s --max-backoff=120s \
    --description="$desc"
  ok "$name ($verb)  $cron  → $path"
  SUMMARY_DONE+=("Scheduler $name（$verb）$cron → $path")
}

step_scheduler() {
  step "2/3 Cloud Scheduler（八条）"
  log "  OIDC audience = $AUDIENCE"
  log "  OIDC 主体     = ${SA_TASKS}@${PROJECT_ID}.iam.gserviceaccount.com"
  log "  时区          = $TIME_ZONE（口径只定这一次，所有报表都用它）"

  local entry name cron path desc
  for entry in "${SCHEDULER_JOBS[@]}"; do
    name="${entry%%|*}"
    entry="${entry#*|}"
    cron="${entry%%|*}"
    entry="${entry#*|}"
    path="${entry%%|*}"
    desc="${entry#*|}"
    mkjob "$name" "$cron" "$path" "$desc"
  done

  step "2b · 契约里另外两个端点：**故意不建 Scheduler**"
  local e ep queue why
  for e in "${TASKS_ONLY_ENDPOINTS[@]}"; do
    ep="${e%%|*}"
    e="${e#*|}"
    queue="${e%%|*}"
    why="${e#*|}"
    skip "$ep → 队列 $queue（$why）"
  done
  log "    定时器不知道该带什么 body，打过去只会得到一堆 4xx —— 9 个 Run*Task 对 8 条 Scheduler"
  log "    不是漏了两个，是这两个按契约就该由服务端入队。"
}

# ───────────────────────── 步骤 3 · B42：证书核对的每日调度 ─────────────────────────
#
# ⚠️ check-cert-issuer.sh **不是 HTTP 端点**，Cloud Scheduler 打不了它。
#    monitoring §8 的原始设想是 `/internal/tasks/cert-check`，2026-08-23 落地时被否掉了
#    （契约里没有这个路径、故障域错误、脚本零依赖）。于是「每日的那个调度器」一直悬着。
#
# 四种可行形态，选第一种：
#
#   A. **Cloud Run Job + Scheduler（OAuth 触发）** ← 本脚本采用
#      ＋ 与已有的 bp-db-dump-weekly **同一形态**，值班只需要理解一种触发方式
#      ＋ 跑在 GCP 内，`gcloud logging write` 不需要额外凭据（用 Job 的运行时 SA）
#      ＋ **故障域与 bp-api 分开**：bp-api 挂了的时候证书核对仍然要能跑（monitoring §8 理由 2）
#      － 需要一个**装了 bash + openssl + gcloud 且带着这个脚本**的镜像。
#        本仓库现在没有这个镜像，也没有构建它的路径 —— 所以本步骤需要 --cert-image=<ref>，
#        没给就只打印缺什么，**不建半个东西**。
#
#   B. GitHub Actions 定时工作流
#      ＋ 零 GCP 资源，且从**外网**发起握手（更接近真实用户视角）
#      － cron 会被延迟甚至跳过（GH 官方明说高峰期可能不准），而这是一条 P0 告警的信号源
#      － 要给 CI 一份能 `gcloud logging write` 的 WIF 凭据；本仓库是 **public 仓库**，
#        扩大 CI 凭据面是一次独立裁决，不该由本脚本顺手做掉
#
#   C. 打到 bp-api 的 /internal/tasks/cert-check
#      － monitoring §8 已经否掉：契约里没有这个路径（要动契约 + 四处生成物 + handler），
#        且把监控挂在被监控对象上
#
#   D. 节点上的 cron
#      － 节点是**数据面**。控制面的核对跑在数据面上，等于让「节点被封」连带「证书核对停摆」
#
step_cert() {
  step "3/3 每日证书签发者核对（roadmap B42 欠的那一半）"

  # 目标清单为空时**什么都不建**。
  # check-cert-issuer.sh 接进定时作业要带 --require-targets（清单空 = 响亮失败），
  # 而域名一个都还没注册 ⇒ 建出来就是一条**每天都红**的作业。
  # monitoring §8 的原话：一条会规律性误报的 P0 最终会被人关掉。
  local domains="${BP_WEB_DOMAINS:-}${BP_ADMIN_DOMAINS:+,${BP_ADMIN_DOMAINS}}${BP_API_DOMAINS:+,${BP_API_DOMAINS}}"
  domains="${domains#,}"
  if [ -z "$domains" ]; then
    warn "BP_WEB_DOMAINS / BP_ADMIN_DOMAINS / BP_API_DOMAINS 三个都为空 → **不建**证书核对作业。
     理由不是「懒得建」：接进定时作业必须带 --require-targets（清单空 = 失败），
     而域名一个都还没注册 ⇒ 建出来就是一条每天都红的 P0，最后一定被人关掉。
     **注册第一个域名时回来跑一次本脚本 --only=cert。**"
    SUMMARY_TODO+=("注册域名后重跑 setup-scheduler.sh --only=cert --apply（B42 的每日调度）")
    return 0
  fi
  log "  目标域名：$domains"

  local job_ready=0
  if gcloud run jobs describe "$CERT_JOB" \
       --project="$PROJECT_ID" --region="$REGION" >/dev/null 2>&1; then
    skip "Cloud Run Job $CERT_JOB 已存在"
    job_ready=1
  elif [ -n "$CERT_IMAGE" ]; then
    guard_bp_prefix "$CERT_JOB"
    confirm_typed "$CONFIRM_APPLY" "将创建 Cloud Run Job $CERT_JOB（镜像 $CERT_IMAGE）"

    # 🔴 Job 的运行时身份要能写 Cloud Logging，否则 cert_issuer_bad 这条日志写不出去，
    #    而 bp_cert_issuer_bad 的**唯一**信号源就是它 —— 没有这个绑定，整条 P0 链路是死的。
    #    roles/logging.logWriter 在 GCP 里只能项目级授（没有更细的等价角色）。
    #    它是只写权限、且项目内没有别的东西依赖「谁能写日志」，所以项目级在这里可接受；
    #    这是本仓库第二处项目级绑定（第一处是 setup-infra.sh 的 roles/cloudsql.client）。
    run gcloud projects add-iam-policy-binding "$PROJECT_ID" \
      --member="serviceAccount:${SA_TASKS}@${PROJECT_ID}.iam.gserviceaccount.com" \
      --role=roles/logging.logWriter \
      --condition=None

    run gcloud run jobs create "$CERT_JOB" \
      --project="$PROJECT_ID" --region="$REGION" \
      --image="$CERT_IMAGE" \
      --service-account="${SA_TASKS}@${PROJECT_ID}.iam.gserviceaccount.com" \
      --max-retries=1 \
      --task-timeout=600s \
      --set-env-vars="BP_WEB_DOMAINS=${BP_WEB_DOMAINS:-},BP_ADMIN_DOMAINS=${BP_ADMIN_DOMAINS:-},BP_API_DOMAINS=${BP_API_DOMAINS:-}" \
      --command=/bin/bash \
      --args=/opt/bp/infra/scripts/check-cert-issuer.sh,--require-targets
    ok "Cloud Run Job $CERT_JOB 已创建（镜像 $CERT_IMAGE）"
    SUMMARY_DONE+=("Cloud Run Job $CERT_JOB —— 每日证书核对的执行体")
    job_ready=1
  else
    warn "Cloud Run Job $CERT_JOB 不存在，且没给 --cert-image=<镜像引用> → 跳过。
     **不建半个东西**：只建 Scheduler 而没有 Job，等于每天定时调用一个不存在的目标。
     缺的是一个装了 bash + openssl + gcloud、且把 infra/scripts/check-cert-issuer.sh
     放在 /opt/bp/infra/scripts/ 下的镜像。本仓库现在没有构建它的路径（见结尾清单）。"
    SUMMARY_TODO+=("构建 $CERT_JOB 的镜像并重跑 --only=cert --cert-image=<ref>（B42）")
  fi

  if [ "$job_ready" -ne 1 ]; then
    return 0
  fi

  guard_bp_prefix "$CERT_SCHED"
  local verb="create"
  if sched_exists "$CERT_SCHED"; then
    verb="update"
  fi

  # 触发 Cloud Run Job 用 **OAuth** 不是 OIDC —— 调用的是 Google 自己的 API
  # （run.googleapis.com）而不是我们的端点，所以不存在 audience 这回事。
  # ⚠️ 这条 URI 的形式在 deploy.md §8.3 标了 **待核实**（Jobs Admin API 路径在 gcloud 版本间调整过）。
  local cert_uri="https://${REGION}-run.googleapis.com/apis/run.googleapis.com/v1/namespaces/${PROJECT_ID}/jobs/${CERT_JOB}:run"

  # 04:40 而不是整点：整点是 ACME 续签与各家定时任务最拥挤的时刻，
  # 而这条作业的判据之一是「剩余有效期 < 14 天」—— 跟续签撞在同一分钟会造成偶发的自我误报。
  run gcloud scheduler jobs "$verb" http "$CERT_SCHED" \
    --project="$PROJECT_ID" --location="$REGION" \
    --schedule="40 4 * * *" --time-zone="$TIME_ZONE" \
    --uri="$cert_uri" --http-method=POST \
    --oauth-service-account-email="${SA_TASKS}@${PROJECT_ID}.iam.gserviceaccount.com" \
    --attempt-deadline=180s \
    --max-retry-attempts=1 \
    --description="每日证书签发者核对（monitoring §8）：bp_cert_issuer_bad 的唯一信号源，告警第 15 条 P0"
  ok "$CERT_SCHED ($verb)  40 4 * * *  → Cloud Run Job $CERT_JOB"
  SUMMARY_DONE+=("Scheduler $CERT_SCHED（$verb）每日 04:40 触发 $CERT_JOB")

  # 指标不追溯（monitoring §3.1 第 1 条）：作业挂上去**之前**必须先有指标，
  # 否则第一段时间的信号是永久丢失的（2026-08-17→08-21 已经因为这条丢过四天数据）。
  if gcloud logging metrics describe bp_cert_issuer_bad --project="$PROJECT_ID" >/dev/null 2>&1; then
    ok "log-based metric bp_cert_issuer_bad 已存在"
  else
    fail "log-based metric bp_cert_issuer_bad **不存在**，而作业已经/即将开始产生日志。
     🔴 日志指标不追溯：现在到建指标之间的信号是**永久丢失**的（2026-08-17→08-21 已经丢过四天）。
     立刻建：
       gcloud logging metrics create bp_cert_issuer_bad --project=$PROJECT_ID \\
         --description='证书签发者与期望不符（monitoring §8）' \\
         --log-filter='logName=\"projects/$PROJECT_ID/logs/bp-cert-issuer-check\" AND jsonPayload.event=\"cert_issuer_bad\"'"
    SUMMARY_TODO+=("建 log-based metric bp_cert_issuer_bad（不追溯，越早越好）")
  fi
}

# ───────────────────────── --delete ─────────────────────────

step_delete() {
  step "删除定时面"

  warn "🔴 删掉这些任务的后果，按严重度排：
     · expire-check 停 ⇒ 到期用户**永远不过期**（没有任何写操作会 bump user_rev），
       面板上一切正常，人只会在对账时才发现
     · traffic-reset 停 ⇒ 用户到了新周期流量不重置，客服量直接上来
     · order-timeout / chain-scan 停 ⇒ 订单永远 pending，链上到账无人认领
     · alive-gc 停 ⇒ 在线设备数只增不减，误判设备超限把正常用户挡在外面
     停摆是**静默**的：monitoring §5 第 12 条那条「Scheduler 任务失败」告警还没建，
     而「任务根本不存在」本来也不会产生任何失败事件。"

  confirm_typed "$CONFIRM_DELETE" "将删除本脚本建的全部 Cloud Scheduler 任务"

  local entry name
  for entry in "${SCHEDULER_JOBS[@]}"; do
    name="${entry%%|*}"
    guard_bp_prefix "$name"
    if sched_exists "$name"; then
      run gcloud scheduler jobs delete "$name" \
        --project="$PROJECT_ID" --location="$REGION" --quiet
      ok "已删除 $name"
      SUMMARY_DONE+=("删除 Scheduler $name")
    else
      skip "$name 不存在"
    fi
  done

  if sched_exists "$CERT_SCHED"; then
    guard_bp_prefix "$CERT_SCHED"
    run gcloud scheduler jobs delete "$CERT_SCHED" \
      --project="$PROJECT_ID" --location="$REGION" --quiet
    ok "已删除 $CERT_SCHED"
    SUMMARY_DONE+=("删除 Scheduler $CERT_SCHED")
  else
    skip "$CERT_SCHED 不存在"
  fi

  # Cloud Run Job bp-cert-issuer-check **故意不删**：它是一个构建产物，
  # 删了要重新构建镜像；而删 Scheduler 已经足以让它停跑。
  skip "Cloud Run Job $CERT_JOB 不在删除范围内（删调度就够了，删 Job 要重新构建镜像）"

  if [ "$INCLUDE_QUEUES" -ne 1 ]; then
    skip "两个 Cloud Tasks 队列保留（要删加 --include-queues，会再要一次确认）"
    return 0
  fi

  # 🔴 Cloud Tasks 队列名在删除后有 **7 天墓碑期**：期间用同名重建会被拒绝。
  #    也就是说这个操作在最坏情况下会让流量入账**停摆一周**，而不是「重跑脚本就好了」。
  #    这是本脚本里唯一一个「撤销成本以天计」的操作，所以单独一道确认。
  warn "🔴 Cloud Tasks 队列删除后，同名重建有 **7 天墓碑期**（GCP 行为）。
     期间 bp-traffic-ingest 建不回来 ⇒ 流量入账停摆 ⇒ 配额耗尽不生效（出口标准第 6 条第 2 项失效）
     ⇒ 超额用户可以继续免费上网，最长一周。**这不是重跑脚本能修的。**"
  confirm_typed "$CONFIRM_DELETE_QUEUES" "确认删除两个 Cloud Tasks 队列（7 天内无法同名重建）"

  local q
  for q in "$QUEUE_TRAFFIC" "$QUEUE_MAIL"; do
    guard_bp_prefix "$q"
    if queue_exists "$q"; then
      run gcloud tasks queues delete "$q" \
        --project="$PROJECT_ID" --location="$REGION" --quiet
      ok "已删除队列 $q"
      SUMMARY_DONE+=("删除 Cloud Tasks 队列 $q（7 天内无法同名重建）")
    else
      skip "队列 $q 不存在"
    fi
  done
}

# ───────────────────────── 结尾清单 ─────────────────────────

print_summary() {
  step "清单 · 建了什么"
  if [ "${#SUMMARY_DONE[@]}" -eq 0 ]; then
    log "  （没有任何变更）"
  else
    local item
    for item in "${SUMMARY_DONE[@]}"; do
      log "  ✓ $item"
    done
  fi
  if [ "$MODE" = "dry-run" ]; then
    log ""
    log "  ⚠️ 以上是 **dry-run**，一条都没有真的执行。加 --apply 才会动 GCP。"
  fi

  step "清单 · 下一步人工要做什么"
  local item
  for item in "${SUMMARY_TODO[@]}"; do
    log "  ☐ $item"
  done
  cat >&2 <<EOF
  ☐ 复核：gcloud scheduler jobs list --project=$PROJECT_ID --location=$REGION
          gcloud tasks queues list   --project=$PROJECT_ID --location=$REGION
  ☐ 手工验一次 P1 出口标准第 6 条（roadmap §4.3）：封禁 / 配额耗尽 / 到期 各触发一次，
    量节点侧生效时间，期望 ≤ 60 s / ≤ 60 s / ≈ 6 分钟。
    **这三个数字是本脚本 cron 的下游结果，不是本脚本能自证的。**
  ☐ 建 monitoring §5 第 12 条告警（Scheduler 任务失败 > 0，单次即告警）——
    没有它，「某条任务从此不再执行」在告警面上是**完全静默**的，
    而最坏的那条（expire-check）的现象是「到期用户继续免费上网、面板一切正常」。
  ☐ 建 monitoring §5 第 11 条告警（cloudtasks/queue/depth > 100）——
    队列积压会直接击穿出口标准第 6 条的「配额耗尽 ≤ 60 s」。
  ☐ monitoring §3.2 的十条 log-based metrics 仍未建全（B42）。**日志指标不追溯**。
  ☐ bp-db-dump-weekly 不归本脚本管，在 ./infra/deploy/setup-infra.sh --step=postdeploy。
  ☐ ⚠️ 本脚本与 setup-infra.sh --step=tasks/postdeploy **参数重复**（刻意逐字相同）。
    改任何一条 cron / 并发数，两处都要改，没有机制提醒你改全。
EOF
}

# ───────────────────────── 主流程 ─────────────────────────

main() {
  local arg
  for arg in "$@"; do
    case "$arg" in
      --dry-run)        MODE="dry-run" ;;
      --apply)          MODE="apply" ;;
      --delete)         MODE="delete" ;;
      --include-queues) INCLUDE_QUEUES=1 ;;
      --only=*)         ONLY="${arg#*=}" ;;
      --audience=*)     AUDIENCE="${arg#*=}" ;;
      --cert-image=*)   CERT_IMAGE="${arg#*=}" ;;
      --project=*)      PROJECT_ID="${arg#*=}" ;;
      --yes)            ASSUME_YES=1 ;;
      -h|--help)        usage; exit 0 ;;
      *)                usage >&2; die "未知参数：$arg" ;;
    esac
  done

  case "$ONLY" in
    all|queues|scheduler|cert) : ;;
    *) die "--only 只能是 all / queues / scheduler / cert，当前是 \"$ONLY\"" ;;
  esac

  guard_project
  need_cmd gcloud
  need_cmd jq

  if [ "$MODE" = "delete" ]; then
    step_delete
    print_summary
    [ "$FAIL_N" -eq 0 ] || exit 1
    exit 0
  fi

  preflight

  # 写操作的总闸：一次确认覆盖本次全部创建/更新。
  # 单条任务不再逐个问 —— 逐个问会训练人无脑敲确认串，那就退化成 y/N 了。
  if [ "$MODE" = "apply" ]; then
    confirm_typed "$CONFIRM_APPLY" \
      "将在 $PROJECT_ID / $REGION 建或更新：8 条 Scheduler + 2 个 Cloud Tasks 队列（范围 --only=$ONLY）"
  fi

  if want queues; then
    step_queues
  fi

  if want scheduler; then
    resolve_audience
    step_scheduler
  fi

  if want cert; then
    step_cert
  fi

  print_summary

  if [ "$FAIL_N" -ne 0 ]; then
    log ""
    log "  🔴 有 $FAIL_N 项失败。上面每一条都写了处置。"
    exit 1
  fi
  exit 0
}

main "$@"

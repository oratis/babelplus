#!/usr/bin/env bash
#
# setup-metrics.sh —— 把 monitoring.md §3.2 的全部 log-based metric 一次建全（幂等）
#
# 事实源：
#   docs/04-ops/monitoring.md §3.1（不追溯 / 标签基数两条硬规矩）· §3.2（十一条指标的过滤器与用途）
#                            · §5 第 1 条（metric absence 按 node_id 分组）· §5 第 15 条（P0）
#                            · §8（证书核对的日志契约）· §11（自定义指标按写入量计费）
#   docs/evidence/gcp-inventory-20260821/ §5.3（2026-08-21 实查：0 条 → 补建 7 条）
#   docs/00-overview/roadmap.md B42
#   api/internal/handler/nodealive.go（bp_node_alive 的日志文案与字段名，**已逐行核实**）
#   api/internal/ratelimit/ratelimit.go（bp_ratelimit_degraded 同上）
#   api/internal/middleware/common.go（AccessLog 的 message/path/status 字段名）
#   api/cmd/server/main.go 的 newLoggerTo（msg→message、level→severity 的重命名）
#   infra/scripts/check-cert-issuer.sh（bp_cert_issuer_bad 的 logName 与 event 契约）
#
# ───────────────────────────────────────────────────────────────────────────
# 这个脚本要还的是哪笔债
# ───────────────────────────────────────────────────────────────────────────
#
# 🔴 **log-based metric 不追溯** —— 它只统计**创建之后**写入的日志（monitoring §3.1 第 1 条）。
#    2026-08-17 `bp-api` 首次部署时这些指标一条都没建，2026-08-21 才补建 7 条，
#    **那 4 天的数据永久缺失**。补建拿不回来，事后也没有历史基线可比。
#
#    所以本脚本存在的理由不是「把命令抄成脚本比较方便」，而是：
#    **建指标这件事必须是可重放、可在几秒内跑完的一步**，否则它会像 08-17 那次一样
#    被排在「先把服务部署上去」后面，而排在后面的代价是不可弥补的。
#
# 🔴 **本脚本刻意建全部指标，不是只建缺的 4 条。** 幂等脚本必须能从零重放 ——
#    换项目、误删、或者哪天有人清理监控之后，跑一次就回到应有状态。
#    「只补缺的」那种脚本在最需要它的那一刻（全没了）恰好不能用。
#
# ───────────────────────────────────────────────────────────────────────────
# 🔴 monitoring.md §3.2 给的那条 --label-extractors 命令**跑不起来**
# ───────────────────────────────────────────────────────────────────────────
#
# 文档里写的是：
#
#     gcloud logging metrics create bp_node_alive --project=$P \
#       --description="…" --log-filter='…' \
#       --label-extractors='node_id=EXTRACT(jsonPayload.node_id)'
#
# 文档自己给这条标了「**待核实**」，核实结果是：**这个 flag 不存在。**
# `gcloud logging metrics create` 在 Google Cloud SDK 558.0.0 上只接受
# `--description` / `--log-filter` / `--bucket-name` / `--config-from-file` 四个
# （核实方式：读本机 SDK 源码 lib/surface/logging/metrics/create.py 的 Args()，
# 以及 api_lib/logging/util.py 的 CreateLogMetric —— 不是猜 CLI 的 --help 措辞）。
# 照抄文档那条命令会得到 `unrecognized arguments: --label-extractors` 而直接失败。
#
# 于是本脚本一律走 `--config-from-file`：把每条指标渲染成一份完整的 LogMetric YAML
# 再交给 gcloud。三个附带好处：
#   1. 带 label 的指标与不带的**走同一条代码路径**，不需要两套分支；
#   2. 渲染出来的 YAML 就是 REST API 的 LogMetric 资源本身，可以 --out= 留档、可以入库、
#      也可以在 gcloud 出问题时直接 curl；
#   3. `create` 与 `update` 都吃同一份文件，幂等重放不需要第二种拼命令的写法。
#
# ⚠️ 不这么做会怎样：`bp_node_alive` 会被建成一条**没有 node_id 标签**的指标。
#    它看起来是成功的（gcloud 退出 0、控制台里有这条指标、计数也在涨），
#    但 monitoring §5 第 1 条的 metric-absence 告警按 `groupByFields=[metric.label.node_id]`
#    分组 —— 没有这个标签，8 个节点挂掉 1 个时总数仍然 > 0，**告警永远不会响**。
#    这是本脚本里最容易「静默做错」的一处，所以它有独立的一段注释和一段核实记录。
#
# ───────────────────────────────────────────────────────────────────────────
# 诚实边界：本脚本**不能**声称指标「已经在采数」
# ───────────────────────────────────────────────────────────────────────────
#
# 指标建出来 ≠ 有日志匹配。一条过滤器谁都写得出来，但只有真的有代码/脚本在写那条日志、
# 而且写日志的前置条件成立（节点已接入、域名已注册、ESP 已接通、调度器已挂上），
# 它才会有非零的时间序列。本脚本结尾的对照表把这两件事**分成两列**，
# 就是为了不让「11 条全绿」被读成「11 条都在采数」。
#
# 想知道现在到底有没有日志匹配，加 `--probe`：它对每条过滤器做一次**只读**的
# `gcloud logging read --limit=1 --freshness=24h`，报告过去 24 小时有没有命中。
# 这是本脚本唯一能给出的、关于「在不在采数」的一手证据。
#
# ───────────────────────────────────────────────────────────────────────────
# 本脚本**不做**的事（别把它读成「监控做完了」）
# ───────────────────────────────────────────────────────────────────────────
#
#   · 不建任何**告警策略**。monitoring §5 的 17 条策略至今 0 条 —— 指标只是「有数」，
#     告警才是「有人会知道」。两者之间还差一整步，而那一步不在本脚本里。
#   · 不建 Pub/Sub **通知渠道**（§4：现在只有 email 一条，Pub/Sub 那半边欠着）。
#   · 不建 **uptime check**（§6）。
#   · 不删任何指标。删指标会丢历史时间序列且不可逆，那必须是人工决定。
#
set -euo pipefail

# ───────────────────────── 固定常量 ─────────────────────────

readonly EXPECTED_PROJECT_ID="oratis-491316"

# 所有 bp-api 应用日志与平台请求日志的公共前缀（monitoring §3.2 里的 $BASE 原文）。
readonly BASE_FILTER='resource.type="cloud_run_revision" AND resource.labels.service_name="bp-api"'

# check-cert-issuer.sh 的 logName。改它 = 改 bp_cert_issuer_bad 的过滤器，
# 而**指标不追溯**：改完到重建指标之间的信号是静默丢失的。两处必须一起改。
readonly CERT_LOG_NAME="bp-cert-issuer-check"

# --probe 的回看窗口。24h 的理由：v2node 每 60 秒轮询、证书核对每日一次，
# 24 小时能覆盖到所有周期性信号源，再长就只是让查询变慢。
readonly PROBE_FRESHNESS="24h"

# 危险操作的手打确认串。**故意分两个**：
#   · 只新建缺失指标 → CONFIRM_CREATE。这一步不会改动任何已有配置。
#   · 会改写已有指标的 filter/description → CONFIRM_OVERWRITE。
# 后者才是真正危险的那个：2026-08-21 补建的 7 条，其**逐字过滤器没有在仓库里留档**
# （evidence 只记了「来源」不是原文），本脚本的过滤器是按代码重新推出来的。
# 两者不一致时，改写等于用推断覆盖当时人工调过的实况。所以要敲一个不一样的串，
# 敲之前先看脚本打出来的逐条 diff。
readonly CONFIRM_CREATE="apply-metrics"
readonly CONFIRM_OVERWRITE="overwrite-filters"

# ───────────────────────── 指标清单 ─────────────────────────
#
# 顺序与 monitoring §3.2 的表格一致，方便逐行对照。
#
# ⚠️ `bp_admin_totp_fail` **不在这里**，在的是 `bp_admin_authz_fail`。
#    monitoring §3.2 登记的是「以 bp_admin_authz_fail 占位 —— TOTP 未实现」。
#    建一条名字叫 totp_fail、过滤器却是 admin 路径 401/403 的指标，会让将来读告警的人
#    以为「TOTP 校验失败了 3 次」，而实际可能只是有人拿过期 session 刷后台。
#    TOTP 落地后应当**新建** bp_admin_totp_fail 并收窄过滤器，而不是改这一条 ——
#    改这一条会让同名指标前后语义不同，历史数据从此不可比。
METRICS=(
  bp_api_5xx
  bp_api_429
  bp_uniproxy_auth_fail
  bp_subscribe_404
  bp_admin_authz_fail
  bp_task_idem_skip
  bp_db_pool_wait
  bp_mail_bounce
  bp_cert_issuer_bad
  bp_node_alive
  bp_ratelimit_degraded
)

# metric_desc <指标名> —— 指标描述。它会显示在 GCP 控制台里，
# 所以要写给「凌晨三点第一次看到这条指标的人」，不是写给自己。
metric_desc() {
  case "$1" in
    bp_api_5xx)
      printf '%s' "bp-api 5xx。错误率的分子（monitoring §5 第 3 条）。注意 request_count 在被打满时是平的，见 §2.1" ;;
    bp_api_429)
      printf '%s' "bp-api 429 被拒/限流。我们的规模下任何 429 都是异常（monitoring §5 第 4 条）" ;;
    bp_uniproxy_auth_fail)
      printf '%s' "节点 UniProxy 鉴权失败（401/403）。近似过滤器：走 AccessLog 的 path+status，鉴权中间件本身不打日志" ;;
    bp_subscribe_404)
      printf '%s' "订阅 token 无效返回 404。突增 = 有人在扫 token（monitoring §5 第 13 条）" ;;
    bp_admin_authz_fail)
      printf '%s' "后台鉴权失败（401/403）。这是 bp_admin_totp_fail 的占位：TOTP 尚未实现，落地后应新建本名指标并收窄" ;;
    bp_task_idem_skip)
      printf '%s' "流量上报被幂等丢弃。Cloud Tasks at-least-once 的可观测证据；长期为 0 反而可疑。只覆盖 /push" ;;
    bp_db_pool_wait)
      printf '%s' "pgxpool 取连接超时 / too many clients。ADR 0005 §6.3 的升配触发器之一。近似过滤器：按 err 文本匹配" ;;
    bp_mail_bounce)
      printf '%s' "ESP 退信回调。邮件是唯一失联恢复通道（ADR 0002）。⚠️ ESP 未接通，本指标在接通前恒为 0" ;;
    bp_cert_issuer_bad)
      printf '%s' "证书签发者与期望不符，由 infra/scripts/check-cert-issuer.sh 写日志。monitoring §5 第 15 条，P0" ;;
    bp_node_alive)
      printf '%s' "节点心跳，由 api/internal/handler/nodealive.go 主动写。带 node_id 标签，§5 第 1 条的 metric-absence 告警依赖它" ;;
    bp_ratelimit_degraded)
      printf '%s' "精确档限流器降级（DB 不可用 → 失败开放）。限流失效不产生任何 429，这条日志是它唯一的痕迹" ;;
    *) printf '' ;;
  esac
}

# metric_filter <指标名> —— 过滤器。
#
# 🔴 三条一起看才成立的依赖（monitoring §3.2 的红字，这里再钉一次）：
#   1. `jsonPayload.message` 这个字段名不是 slog 的默认值。slog 默认写 `msg`，
#      是 api/cmd/server/main.go 的 newLoggerTo 用 ReplaceAttr 改成 `message`
#      （`level`→`severity` 同理）。删掉那几行不会有任何编译或运行时报错，
#      但**下面每一条按 message 匹配的过滤器会同时停止匹配**，而且是静默的。
#      已由 api/cmd/server/logger_test.go 钉住。
#   2. 按 `message` 精确匹配日志**文案**的那几条（bp_subscribe_404 / bp_task_idem_skip）
#      是脆的：改一句中文措辞就静默失配。bp_node_alive 与 bp_ratelimit_degraded
#      刻意把文案写成指标名本身，正是为了不重蹈这一点。
#   3. `httpRequest.*` 来自 Cloud Run 的**平台请求日志**，不是我们写的；
#      `jsonPayload.*` 来自应用日志。两者是两条不同的日志流，不要混着写进同一个条件。
metric_filter() {
  case "$1" in
    # ── 平台请求日志（monitoring §3.2 给的原文过滤器）─────────────────
    bp_api_5xx)
      printf '%s' "${BASE_FILTER} AND httpRequest.status>=500" ;;
    bp_api_429)
      printf '%s' "${BASE_FILTER} AND httpRequest.status=429" ;;

    # ── AccessLog（middleware/common.go：message="http"，字段 path/status）──
    #
    # 节点面路径来自 api-contract 的冻结契约 /api/v1/server/UniProxy/*；
    # 401/403 两个码都要，middleware/node.go 两者都会回：
    #   401 NODE_KEY_INVALID（缺密钥 / 格式非法 / 无效或已吊销）
    #   403 AUTH_PERMISSION_DENIED（节点已停用）· 403 NODE_SCOPE_DENIED（密钥无权访问该端点）
    # 只抓 401 会漏掉「密钥轮换只做了一半」里最常见的那半（scope 没给全）。
    bp_uniproxy_auth_fail)
      printf '%s' "${BASE_FILTER} AND jsonPayload.message=\"http\" AND jsonPayload.path=~\"^/api/v1/server/UniProxy/\" AND (jsonPayload.status=401 OR jsonPayload.status=403)" ;;
    bp_admin_authz_fail)
      printf '%s' "${BASE_FILTER} AND jsonPayload.message=\"http\" AND jsonPayload.path=~\"^/api/v1/admin/\" AND (jsonPayload.status=401 OR jsonPayload.status=403)" ;;

    # ── 应用显式日志行（匹配文案，脆）───────────────────────────────
    # 文案取自 api/internal/handler/subscription.go 与 handler/node.go 的实际字符串。
    bp_subscribe_404)
      printf '%s' "${BASE_FILTER} AND jsonPayload.message=\"订阅 token 无效，返回 404\"" ;;
    bp_task_idem_skip)
      printf '%s' "${BASE_FILTER} AND jsonPayload.message=\"流量上报重复，已按幂等丢弃\"" ;;

    # ── 近似判据：按错误文本匹配 ───────────────────────────────────
    # monitoring §3.2 明说这条是「近似（按 jsonPayload.err 文本匹配，非结构化判据）」。
    # 两个子串分别对应两种成因：Postgres 侧连接数打满、pgxpool 侧取连接超时。
    # ⚠️ 它会连带抓到别处的 context deadline exceeded。宁可宽也不要漏 ——
    #    这条指标是 ADR 0005 §6.3 的升配触发器，漏报的代价是「撞了 25 连接天花板却不知道」。
    bp_db_pool_wait)
      printf '%s' "${BASE_FILTER} AND (jsonPayload.err:\"too many clients\" OR jsonPayload.err:\"timeout: context deadline exceeded\" OR jsonPayload.err:\"timeout: pool\")" ;;

    # ── 信号源尚不存在：先定契约，再等实现来对齐 ─────────────────────
    #
    # 🔴 ESP 未接通（api/internal/handler/auth.go 的 TODO(P1)），没有任何东西写这条日志。
    #    仍然要建，理由只有一条：**指标不追溯**。等 ESP 接通那天再建，
    #    「接通头几天退信率是多少」这个最该看的窗口就永远没有数据了 ——
    #    而 AWS SES 退信率 ≥5% 进审查、≥10% 可能暂停发信，恰恰是**接通初期**最容易踩的。
    #
    #    取舍：建一条恒为 0 的指标要付两笔代价 ——
    #      (a) 它会让「11 条都建好了」这句话产生误导（本脚本用结尾对照表 + 一行显式警告对冲）；
    #      (b) 自定义指标按写入量计费（monitoring §11），但零写入 = 零费用，这一笔实际是 0。
    #    对照收益（接通首日就有基线），值。
    #
    #    过滤器按 bp_node_alive / bp_ratelimit_degraded 的同一条约定写死：
    #    **日志文案就是指标名**。ESP 接通时写退信日志的那段代码只要照抄这条约定，
    #    指标不用改、也不会有「改了指标名之间那段时间信号丢失」的窗口。
    #    ⚠️ 刻意**不加** label extractor。§10.1 想要的「按收件域名分组」在这里做不了：
    #       收件域名不是有界枚举（§3.1 只批准 route_group / reason / node_id 三类），
    #       分域名统计走 user-journey 的 email_probe 表，不走指标标签。
    bp_mail_bounce)
      printf '%s' "${BASE_FILTER} AND jsonPayload.message=\"bp_mail_bounce\"" ;;

    # ── 脚本写的日志（不在 Cloud Run 里，所以不带 BASE_FILTER）──────────
    # 逐字取自 check-cert-issuer.sh 的 LOG_NAME 与 EVENT_BAD 常量。
    # 只喂 cert_issuer_bad 一个 event：cert_expiring_soon 走单独的（尚未规划的）指标，
    # 把续签窗口算成「签发者异常」会让这条 P0 在每次证书轮换前都响一次，
    # 而一条会规律性误报的 P0 最终会被人关掉（monitoring §8）。
    bp_cert_issuer_bad)
      printf '%s' "logName=\"projects/${PROJECT_ID}/logs/${CERT_LOG_NAME}\" AND jsonPayload.event=\"cert_issuer_bad\"" ;;

    # ── 文案即指标名（最稳的一类）──────────────────────────────────
    bp_node_alive)
      printf '%s' "${BASE_FILTER} AND jsonPayload.message=\"bp_node_alive\"" ;;
    bp_ratelimit_degraded)
      printf '%s' "${BASE_FILTER} AND jsonPayload.message=\"bp_ratelimit_degraded\"" ;;
    *) printf '' ;;
  esac
}

# metric_labels <指标名> —— 空格分隔的 `标签名=jsonPayload 路径`。空 = 这条指标不带标签。
#
# 🔴 只有 bp_node_alive 有标签，而且**必须**有。见文件头那一大段。
#
# 字段名不是猜的：api/internal/handler/nodealive.go 里写的是
#     s.logger.InfoContext(ctx, nodeAliveMessage, "node_id", strconv.FormatInt(auth.ServerID, 10), ...)
# slog 的 JSON handler 会把它平铺成 jsonPayload.node_id，值是**字符串**
# （FormatInt 而不是直接传 int64 —— log-based metric 的 label 本身就是字符串类型，
# 传数字要多依赖一次隐式转换，而那一步出错的现象正是「指标建出来了但没有标签」）。
#
# ⚠️ 同一条日志里还有 server_code。**不要**把它也做成标签：
#    monitoring §3.2 只批准了 node_id 一个实体标签（依据是节点数有硬上限 ≤10），
#    多一个维度就是 time series 数量翻倍，而它对告警没有任何增量作用
#    —— 值班要看「7 号是哪台」时去日志里看就行。
metric_labels() {
  case "$1" in
    bp_node_alive) printf '%s' "node_id=jsonPayload.node_id" ;;
    *)             printf '' ;;
  esac
}

metric_label_desc() {
  case "$1" in
    node_id) printf '%s' "节点 ID（字符串）。§5 第 1 条的 metric-absence 告警按它分组" ;;
    *)       printf '%s' "" ;;
  esac
}

# metric_signal <指标名> —— 这条指标**现在**有没有东西在写它的日志。
#
#   LIVE    有代码/脚本正在写，且前置条件已满足
#   APPROX  有信号，但过滤器是近似或占位 —— 抓到的不完全是指标名说的那件事
#   BLOCKED 写日志的代码已就绪，但前置条件未满足 → **现在恒为 0**
#   NONE    没有任何东西在写 → **恒为 0**
#
# BLOCKED 与 NONE 的区别值得留着：前者只差一个外部动作（建节点 / 注册域名 / 挂调度器），
# 后者还差一整块代码。把两者混成「没有信号」会让人低估已经做完的部分，
# 也会让人高估还剩的工作量。
metric_signal() {
  case "$1" in
    bp_api_5xx|bp_api_429|bp_subscribe_404|bp_ratelimit_degraded) printf 'LIVE' ;;
    bp_uniproxy_auth_fail|bp_admin_authz_fail|bp_task_idem_skip|bp_db_pool_wait) printf 'APPROX' ;;
    bp_cert_issuer_bad|bp_node_alive) printf 'BLOCKED' ;;
    bp_mail_bounce) printf 'NONE' ;;
    *) printf 'UNKNOWN' ;;
  esac
}

metric_signal_note() {
  case "$1" in
    bp_api_5xx)
      printf '%s' "Cloud Run 平台请求日志，bp-api 自 2026-08-17 起在跑" ;;
    bp_api_429)
      printf '%s' "同上。平台日志，不依赖我们写任何东西" ;;
    bp_uniproxy_auth_fail)
      printf '%s' "鉴权中间件不打日志，靠 AccessLog 的 path+status 反推" ;;
    bp_subscribe_404)
      printf '%s' "subscription.go 的显式日志行。⚠️ 匹配中文文案，改词即静默失配" ;;
    bp_admin_authz_fail)
      printf '%s' "占位：TOTP 未实现，抓的是 admin 路径的 401/403" ;;
    bp_task_idem_skip)
      printf '%s' "只覆盖 /push；httpx/idempotency.go 的 Idempotency-Key 路径不打日志" ;;
    bp_db_pool_wait)
      printf '%s' "按 err 文本匹配，非结构化判据；会连带抓到别处的 deadline exceeded" ;;
    bp_mail_bounce)
      printf '%s' "ESP 未接通（auth.go TODO(P1)）—— 接通前恒为 0" ;;
    bp_cert_issuer_bad)
      printf '%s' "check-cert-issuer.sh 已就绪，但域名池为空 + 每日调度器未挂 → 现在不会有日志" ;;
    bp_node_alive)
      printf '%s' "nodealive.go 已在写，但第一台 bp-node-* 尚未建成 → 现在不会有日志" ;;
    bp_ratelimit_degraded)
      printf '%s' "ratelimit.go 在写；只在限流器降级时出现，长期为 0 是正常的" ;;
    *) printf '' ;;
  esac
}

# ───────────────────────── 运行时开关 ─────────────────────────

PROJECT_ID="${PROJECT_ID:-$EXPECTED_PROJECT_ID}"
DRY_RUN=1          # 🔴 默认 dry-run。要真的动 GCP 必须显式 --apply。
CREATE_ONLY=0
DO_PROBE=0
ASSUME_YES=0
OUT_DIR=""
KEEP_OUT=0
WORK_DIR=""

N_CREATE=0
N_UPDATE=0
N_OK=0
N_ERROR=0

# ───────────────────────── 通用工具（与 infra/ 下其它脚本刻意保持重复）─────────────────────────
#
# 每个脚本要能单独 scp 出去跑，所以 log/step/qq/die 这几个在六个脚本里各有一份。
# 代价（改一处要改六处，没有机制提醒）记在 infra/deploy/README.md §6。

log()  { printf '%s\n' "$*" >&2; }
step() { printf '\n\033[1m▸ %s\033[0m\n' "$*" >&2; }
pass() { printf '  \033[32m✓\033[0m %s\n' "$*" >&2; }
fail() { printf '  \033[31m✗ %s\033[0m\n' "$*" >&2; }
warn() { printf '  \033[33m⚠\033[0m %s\n' "$*" >&2; }
note() { printf '  · %s\n' "$*" >&2; }
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

need_cmd() {
  command -v "$1" >/dev/null 2>&1 || die "缺少命令：$1"
}

# 由 main 里的 trap cleanup EXIT 调用，shellcheck 看不出间接调用。
# 两个码都要留：0.9.0（CI 的 ubuntu-24.04 预装版）报 SC2317，SC2329 是 0.10.0 才引入的。
# shellcheck disable=SC2317,SC2329
cleanup() {
  if [ "$KEEP_OUT" -eq 0 ] && [ -n "$WORK_DIR" ] && [ -d "$WORK_DIR" ]; then
    rm -rf "$WORK_DIR"
  fi
}

usage() {
  cat <<'EOF'
用法: setup-metrics.sh [选项]

幂等地建齐 docs/04-ops/monitoring.md §3.2 列的全部 log-based metric（11 条）。
已存在且定义一致 → 不动；存在但定义不同 → update；不存在 → create。

🔴 log-based metric **不追溯**：它只统计创建之后写入的日志。
   2026-08-17 首次部署时一条都没建，到 08-21 才补 7 条 —— 那 4 天数据永久缺失（roadmap B42）。
   所以这个脚本应当在**任何新环境部署 bp-api 之前**先跑一次。

选项:
  --dry-run        只打印将要执行的命令，不改任何东西（**默认**）
  --apply          真的建/改指标。需要手打确认串（除非 --yes）
  --create-only    只建缺失的；已存在但定义不同的一律不动，只报告 diff
  --probe          额外做一次**只读**探测：过去 24h 有没有日志匹配每条过滤器。
                   这是本脚本唯一能给出的「到底在不在采数」的一手证据。dry-run 下也会跑
  --out=<目录>     把生成的 LogMetric YAML 留在这里（默认写临时目录，退出时删）
  --yes            跳过手打确认串。给 CI 用，人工别用
  --project=<id>   GCP 项目 ID。必须是 oratis-491316
  -h, --help       显示本帮助

退出码:
  0  全部就绪（已 apply 成功，或 dry-run 下无待办）
  1  有待办未落实（dry-run/--create-only 下存在缺失或漂移），或 apply 过程中有失败
  2  用法或环境错误（缺 gcloud/jq、项目 ID 不对、当前 gcloud 项目不是 oratis-491316）

  ⚠️ 「dry-run 且有待办 → 退出 1」是**故意的**：这让本脚本可以直接当漂移检测用
     （`setup-metrics.sh >/dev/null || echo 有指标没建`）。它不代表脚本自己出错了。

典型用法:
  ./infra/scripts/setup-metrics.sh                      # 看看差什么（不改任何东西）
  ./infra/scripts/setup-metrics.sh --probe              # 再看看现在有没有日志真的在匹配
  ./infra/scripts/setup-metrics.sh --apply --create-only  # 只补缺的，不碰已有的
  ./infra/scripts/setup-metrics.sh --apply              # 全量对齐（会改写已有指标，需二次确认串）

本脚本**不建**告警策略、通知渠道、uptime check，也**不删**任何指标。
指标只是「有数」，告警才是「有人会知道」—— monitoring §5 的 17 条策略至今 0 条。
EOF
}

# ───────────────────────── 项目守卫 ─────────────────────────
#
# 🔴 oratis-491316 是**共享项目**：里面还住着 vpn-us / vpn-jp 两台现役代理节点
#    和 anthropic-relay / lisa-cloud / lisa-web 三个 Cloud Run 服务（as-built §2、§4）。
#    跑错项目的爆炸半径就是 roadmap R7 那一条。
#
# 两层都要查，因为它们防的不是同一件事：
#   1. PROJECT_ID != 期望值  → 防「有人给脚本传了别的项目」；
#   2. gcloud 当前项目 != 期望值 → 防「登录环境本身指向别处」。
#      本脚本每条 gcloud 都显式带 --project，所以第 2 条**技术上**不影响这些命令。
#      仍然拦，是因为：跑这个脚本的人下一步多半要手敲 gcloud 命令看结果，
#      而那些命令不会自带 --project。让不一致在这里响一次，比让它在下一条命令里静默生效好。
guard_project() {
  if [ "$PROJECT_ID" != "$EXPECTED_PROJECT_ID" ]; then
    die "PROJECT_ID 必须是 $EXPECTED_PROJECT_ID，当前是 \"$PROJECT_ID\"。
     本脚本建的指标名、过滤器、logName 全都写死了这一个项目，换项目毫无意义。"
  fi

  local active
  # tail -n 1：gcloud 偶尔把噪音写进 stdout 且仍然 exit 0（verify-isolation.sh 实测过），
  # 真正的值在最后一行。tr 去掉尾部换行与可能的空白。
  active="$(gcloud config get-value project 2>/dev/null | tail -n 1 | tr -d '[:space:]' || true)"
  case "$active" in
    '(unset)'|'') active="" ;;
  esac

  if [ "$active" != "$EXPECTED_PROJECT_ID" ]; then
    die "当前 gcloud 项目是 \"${active:-<未设置>}\"，不是 $EXPECTED_PROJECT_ID。
     $EXPECTED_PROJECT_ID 是共享项目（vpn-us / vpn-jp / lisa-* 都住在里面），
     在错误的上下文里操作监控配置的代价不是「脚本报错」，是「动到别人的资源」。
     改法二选一：
       gcloud config set project $EXPECTED_PROJECT_ID
       CLOUDSDK_CORE_PROJECT=$EXPECTED_PROJECT_ID $0 ..."
  fi
}

# confirm <提示> <期望串> —— 危险操作的手打确认。回车不算数，输错即中止。
confirm() {
  local prompt="$1" expect="$2" answer=""
  if [ "$DRY_RUN" -eq 1 ]; then
    note "[dry-run] 跳过确认：$prompt"
    return 0
  fi
  if [ "$ASSUME_YES" -eq 1 ]; then
    warn "--yes 已跳过确认：$prompt"
    return 0
  fi
  if [ ! -t 0 ]; then
    die "需要交互确认但 stdin 不是终端。非交互场景请显式加 --yes（并想清楚为什么）。"
  fi
  printf '\n%s\n请输入 %s 确认（其它任何输入都会中止）：' "$prompt" "$expect" >&2
  read -r answer || true
  if [ "$answer" != "$expect" ]; then
    die "确认串不匹配，已中止。"
  fi
}

# ───────────────────────── LogMetric YAML 渲染 ─────────────────────────

# yaml_squote —— 渲染成 YAML 单引号标量。单引号内只需把 ' 变成 ''，
# 其余字符（包括我们过滤器里大量的 "、>=、: 、中文）都原样安全。
# 不用双引号标量：那还要处理反斜杠转义，而过滤器里的正则前缀 ^ 与 \ 会踩到。
yaml_squote() {
  printf "'%s'" "$(printf '%s' "$1" | sed "s/'/''/g")"
}

# render_metric_yaml <指标名> <目标文件>
#
# 产出的就是 REST v2 的 LogMetric 资源本身：
# https://cloud.google.com/logging/docs/reference/v2/rest/v2/projects.metrics#LogMetric
#
# 字段名必须是 **camelCase 的 JSON 名**（metricDescriptor / labelExtractors / valueType），
# 因为 gcloud 是拿 apitools 的 encoding.DictToMessage 直接把这份 YAML 映射成 proto 消息的
# （核实过 api_lib/logging/util.py::CreateLogMetric）。写成 snake_case 会解析失败。
#
# `name:` 会被 gcloud 用位置参数覆盖（CreateLogMetric 里的 metric_msg.name = metric_name），
# 这里仍然写出来，是为了让这份文件单独看也是完整可用的资源定义。
render_metric_yaml() {
  local name="$1" file="$2"
  local labels key path
  labels="$(metric_labels "$name")"

  {
    printf '# 由 infra/scripts/setup-metrics.sh 生成 —— 勿手改，改脚本。\n'
    printf '# LogMetric: https://cloud.google.com/logging/docs/reference/v2/rest/v2/projects.metrics#LogMetric\n'
    printf 'name: %s\n' "$(yaml_squote "$name")"
    printf 'description: %s\n' "$(yaml_squote "$(metric_desc "$name")")"
    printf 'filter: %s\n' "$(yaml_squote "$(metric_filter "$name")")"

    if [ -n "$labels" ]; then
      # metricKind/valueType 显式写死成计数型（DELTA/INT64）。
      # 不靠 API 的默认值：一旦某天默认值变了，或者哪个字段没给全导致 labels 被忽略，
      # 现象是「指标建出来了但没有标签」—— 而那正是本脚本最要防的静默失败。
      printf 'metricDescriptor:\n'
      printf '  metricKind: DELTA\n'
      printf '  valueType: INT64\n'
      printf '  unit: %s\n' "'1'"
      printf '  labels:\n'
      while IFS= read -r kv; do
        [ -n "$kv" ] || continue
        key="${kv%%=*}"
        printf '    - key: %s\n' "$(yaml_squote "$key")"
        printf '      valueType: STRING\n'
        printf '      description: %s\n' "$(yaml_squote "$(metric_label_desc "$key")")"
      done <<EOF
$(printf '%s' "$labels" | tr ' ' '\n')
EOF
      printf 'labelExtractors:\n'
      while IFS= read -r kv; do
        [ -n "$kv" ] || continue
        key="${kv%%=*}"
        path="${kv#*=}"
        printf '  %s: %s\n' "$key" "$(yaml_squote "EXTRACT(${path})")"
      done <<EOF
$(printf '%s' "$labels" | tr ' ' '\n')
EOF
    fi
  } > "$file"
}

# want_label_extractors <指标名> —— 期望的 labelExtractors，规范化成 `k=v,k=v`（按 key 排序）。
want_label_extractors() {
  local labels kv key path out=""
  labels="$(metric_labels "$1")"
  [ -n "$labels" ] || { printf ''; return 0; }
  while IFS= read -r kv; do
    [ -n "$kv" ] || continue
    key="${kv%%=*}"
    path="${kv#*=}"
    if [ -n "$out" ]; then out="${out},"; fi
    out="${out}${key}=EXTRACT(${path})"
  done <<EOF
$(printf '%s' "$labels" | tr ' ' '\n')
EOF
  printf '%s' "$out"
}

# ───────────────────────── GCP 只读查询 ─────────────────────────

# describe_metric <指标名>
#   0 = 存在（JSON 打到 stdout）
#   1 = 不存在
#   2 = 查不了（权限 / 网络 / API 变化）—— 「查不到」不等于「没问题」，按失败处理
describe_metric() {
  local name="$1" out="" rc=0
  local errf="${WORK_DIR}/describe-${name}.err"

  out="$(gcloud logging metrics describe "$name" --project="$PROJECT_ID" --format=json 2>"$errf")" || rc=$?

  if [ "$rc" -eq 0 ]; then
    # gcloud 会把噪音写进 stdout 且仍然 exit 0（verify-isolation.sh 2026-08-17 实测：
    # Python 3.9 环境下 importlib.metadata 的报错就在 JSON 之前）。
    # 剥到第一个 { 或 [ 为止，否则下游 jq 会把「有噪音」误判成「指标不存在」。
    printf '%s' "$out" | awk 'p{print;next} /^[[{]/{p=1;print}'
    return 0
  fi
  if grep -qiE 'NOT_FOUND|was not found|does not exist|Could not fetch metric' "$errf"; then
    return 1
  fi
  return 2
}

# norm_ws —— 把空白规范化后再比。
# GCP 会对 filter 做一次自己的格式化（换行/缩进/多余空格），逐字节比会让每次都判成「有漂移」，
# 而一个每次都报红的检查等于没有检查。
norm_ws() {
  printf '%s' "$1" | tr '\n\t' '  ' | sed -e 's/  */ /g' -e 's/^ //' -e 's/ $//'
}

# probe_filter <过滤器> —— 过去 PROBE_FRESHNESS 内有没有日志命中。
#   HIT / EMPTY / ERR
# 只读。它回答的是「有没有信号」，与「指标存不存在」无关 —— 所以指标还没建也能问。
probe_filter() {
  local filter="$1" out="" rc=0
  out="$(gcloud logging read "$filter" --project="$PROJECT_ID" \
          --limit=1 --freshness="$PROBE_FRESHNESS" \
          --format='value(timestamp)' 2>/dev/null)" || rc=$?
  if [ "$rc" -ne 0 ]; then
    printf 'ERR'
    return 0
  fi
  # 只认长得像时间戳的行 —— 同样是为了不把 gcloud 的 stdout 噪音当成一条命中。
  if printf '%s\n' "$out" | grep -qE '^[0-9]{4}-[0-9]{2}-[0-9]{2}T'; then
    printf 'HIT'
  else
    printf 'EMPTY'
  fi
}

# ───────────────────────── 第一步：盘点与计划 ─────────────────────────
#
# 计划写到 ${WORK_DIR}/plan.tsv：`指标名 \t 动作 \t 说明`
# 动作 ∈ CREATE / UPDATE / OK / ERROR
plan_all() {
  step "1 · 盘点（只读 describe，dry-run 下也会真的查）"

  local name json rc
  local cur_filter cur_desc cur_labels
  local want_filter want_desc want_labels
  local diffs

  : > "${WORK_DIR}/plan.tsv"

  for name in "${METRICS[@]}"; do
    want_filter="$(norm_ws "$(metric_filter "$name")")"
    want_desc="$(metric_desc "$name")"
    want_labels="$(want_label_extractors "$name")"

    rc=0
    json="$(describe_metric "$name")" || rc=$?

    case "$rc" in
      1)
        printf '%s\tCREATE\t%s\n' "$name" "不存在" >> "${WORK_DIR}/plan.tsv"
        N_CREATE=$((N_CREATE + 1))
        warn "$name  不存在 → 将新建"
        ;;
      2)
        printf '%s\tERROR\t%s\n' "$name" "describe 调用失败" >> "${WORK_DIR}/plan.tsv"
        N_ERROR=$((N_ERROR + 1))
        fail "$name  describe 调用失败（见 ${WORK_DIR}/describe-${name}.err）
     查不到 = 判定不了 = 当作失败。不允许「查不到就算通过」。"
        ;;
      0)
        cur_filter="$(norm_ws "$(printf '%s' "$json" | jq -r '.filter // ""')")"
        cur_desc="$(printf '%s' "$json" | jq -r '.description // ""')"
        cur_labels="$(printf '%s' "$json" | jq -r '
          (.labelExtractors // {}) | to_entries | sort_by(.key)
          | map("\(.key)=\(.value)") | join(",")')"

        diffs=""
        [ "$cur_filter" = "$want_filter" ] || diffs="${diffs}filter "
        [ "$cur_desc" = "$want_desc" ]     || diffs="${diffs}description "
        [ "$cur_labels" = "$want_labels" ] || diffs="${diffs}labelExtractors "

        if [ -z "$diffs" ]; then
          printf '%s\tOK\t%s\n' "$name" "已存在且定义一致" >> "${WORK_DIR}/plan.tsv"
          N_OK=$((N_OK + 1))
          pass "$name  已存在且定义一致"
        else
          printf '%s\tUPDATE\t%s\n' "$name" "漂移：${diffs}" >> "${WORK_DIR}/plan.tsv"
          N_UPDATE=$((N_UPDATE + 1))
          warn "$name  已存在但定义不同（${diffs%% }）"
          if [ "$cur_filter" != "$want_filter" ]; then
            log "      现有 filter: ${cur_filter:-<空>}"
            log "      期望 filter: ${want_filter}"
          fi
          if [ "$cur_labels" != "$want_labels" ]; then
            log "      现有 labels: ${cur_labels:-<无>}"
            log "      期望 labels: ${want_labels:-<无>}"
            if [ "$name" = "bp_node_alive" ] && [ -z "$cur_labels" ]; then
              log "      🔴 没有 node_id 标签的 bp_node_alive 会让 §5 第 1 条 metric-absence 告警"
              log "         在**每一个**节点上都不 armed —— 挂了也不会响。这一条必须修。"
            fi
          fi
          if [ "$cur_desc" != "$want_desc" ]; then
            log "      现有 desc  : ${cur_desc:-<空>}"
          fi
        fi
        ;;
    esac
  done
}

# ───────────────────────── 第二步：执行 ─────────────────────────

# apply_one <指标名> <create|update>
apply_one() {
  local name="$1" verb="$2"
  local file="${WORK_DIR}/${name}.yaml"
  local -a cmd=(
    gcloud logging metrics "$verb" "$name"
    --project="$PROJECT_ID"
    --config-from-file="$file"
  )

  if [ "$DRY_RUN" -eq 1 ]; then
    local _a
    printf '  [dry-run] ' >&2
    for _a in "${cmd[@]}"; do qq "$_a" >&2; printf ' ' >&2; done
    printf '\n' >&2
    return 0
  fi

  # </dev/null 不能省：apply_all 是 `while read < plan.tsv` 驱动的，
  # 子命令若读走一口 stdin，剩下的指标会被**静默跳过**（跳过的那条不会有任何输出）。
  if ! "${cmd[@]}" >/dev/null </dev/null; then
    fail "$name  gcloud logging metrics $verb 失败"
    return 1
  fi
  pass "$name  已 $verb"
  return 0
}

apply_all() {
  step "2 · 执行"

  if [ "$N_CREATE" -eq 0 ] && [ "$N_UPDATE" -eq 0 ]; then
    note "没有待办：11 条指标都已存在且定义一致。"
    return 0
  fi

  if [ "$CREATE_ONLY" -eq 1 ] && [ "$N_UPDATE" -gt 0 ]; then
    warn "--create-only：${N_UPDATE} 条已存在但定义不同的指标**不会被改动**，只在上面报了 diff。"
    warn "   想对齐它们请去掉 --create-only 重跑（会要求敲 ${CONFIRM_OVERWRITE}）。"
  fi

  # 确认串按危险程度分两级：会改写已有配置的那一级用不同的串，
  # 因为 2026-08-21 那 7 条的逐字过滤器没有在仓库里留档，改写是用推断覆盖实况。
  local expect="$CONFIRM_CREATE"
  local prompt="将在 ${PROJECT_ID} 上**新建** ${N_CREATE} 条 log-based metric。
  新建不影响任何已有配置；但自定义指标按写入量计费（monitoring §11）。"
  if [ "$CREATE_ONLY" -eq 0 ] && [ "$N_UPDATE" -gt 0 ]; then
    expect="$CONFIRM_OVERWRITE"
    prompt="将在 ${PROJECT_ID} 上新建 ${N_CREATE} 条、**改写 ${N_UPDATE} 条已有**指标的定义。
  🔴 被改写的那几条里可能有 2026-08-21 人工补建的 —— 它们的逐字过滤器**没有在仓库里留档**，
     本脚本的过滤器是按当前代码重新推出来的。改写 = 用推断覆盖当时的实况。
     **先把上面每一条 diff 看完再敲这个串。**
  （只想补缺、不碰已有的：加 --create-only 重跑。）"
  fi
  confirm "$prompt" "$expect"

  local name action rest
  while IFS="$(printf '\t')" read -r name action rest; do
    [ -n "$name" ] || continue
    case "$action" in
      CREATE) apply_one "$name" create || N_ERROR=$((N_ERROR + 1)) ;;
      UPDATE)
        if [ "$CREATE_ONLY" -eq 1 ]; then
          note "$name  --create-only，跳过（保留现状）"
        else
          apply_one "$name" update || N_ERROR=$((N_ERROR + 1))
        fi
        ;;
      *) : ;;
    esac
  done < "${WORK_DIR}/plan.tsv"
  # rest 只是为了吃掉第三列，shellcheck 会说它没用到 —— 它确实没用到，这是故意的。
  : "${rest:-}"
}

# ───────────────────────── 第三步：对照表 ─────────────────────────
#
# 🔴 这是本脚本最有价值的输出，也是它唯一诚实的姿态：
#    **「指标建好了」与「指标在采数」是两件事**，所以它们是两列，不是一列。
report_table() {
  step "3 · 对照表：哪些指标现在有信号源、哪些还没有"

  local name action rest signal probe_state
  local hit=0 empty=0 err=0

  printf '\n' >&2
  # 列头刻意全 ASCII：printf 的 %-Ns 按字节对齐，中文列头会把整张表排歪。
  # 中文解释放在下面的图例里。
  printf '  %-24s %-8s %-8s %-7s %s\n' "METRIC" "STATE" "SIGNAL" "LOG24H" "NOTE" >&2
  printf '  %-24s %-8s %-8s %-7s %s\n' \
    "------------------------" "--------" "--------" "-------" "----------------------------------------" >&2

  while IFS="$(printf '\t')" read -r name action rest; do
    [ -n "$name" ] || continue
    signal="$(metric_signal "$name")"

    probe_state="-"
    if [ "$DO_PROBE" -eq 1 ]; then
      probe_state="$(probe_filter "$(metric_filter "$name")")"
      case "$probe_state" in
        HIT)   hit=$((hit + 1)) ;;
        EMPTY) empty=$((empty + 1)) ;;
        ERR)   err=$((err + 1)) ;;
      esac
    fi

    # dry-run 下 CREATE/UPDATE 是「将要做」，apply 后是「已做」。
    # 两者在表里用同一个词，因为读表的人关心的是「跑完之后应该是什么状态」，
    # 而 dry-run 的抬头已经说清楚了什么都没改。
    printf '  %-24s %-8s %-8s %-7s %s\n' \
      "$name" "$action" "$signal" "$probe_state" "$(metric_signal_note "$name")" >&2
  done < "${WORK_DIR}/plan.tsv"

  printf '\n' >&2
  log "  图例 · STATE（指标本身在 GCP 上的状态）"
  log "    CREATE  不存在 → 本次新建（dry-run 下是「将新建」）"
  log "    UPDATE  已存在但定义不同 → 本次改写（--create-only 下不动）"
  log "    OK      已存在且定义一致，没动它"
  log "    ERROR   查不到状态（权限/网络/API）—— 当作失败，不当作通过"
  log ""
  log "  图例 · SIGNAL（**有没有东西在写这条日志**，与指标存不存在无关）"
  log "    LIVE     有代码/脚本正在写，前置条件也满足"
  log "    APPROX   有信号，但过滤器是近似或占位 —— 抓到的不完全是指标名说的那件事"
  log "    BLOCKED  写日志的代码已就绪，但前置条件未满足 → **现在恒为 0**"
  log "    NONE     没有任何东西在写 → **恒为 0**"
  log ""
  if [ "$DO_PROBE" -eq 1 ]; then
    log "  图例 · LOG24H（过去 ${PROBE_FRESHNESS} 的只读探测实测）"
    log "    HIT ${hit} 条 / EMPTY ${empty} 条 / ERR ${err} 条"
    log "    ⚠️ EMPTY 不一定是坏事：bp_ratelimit_degraded 长期 EMPTY 恰恰说明限流器没降级过。"
    log "       要区分「没坏所以没信号」和「压根没人写所以没信号」，看 SIGNAL 那一列。"
  else
    log "  图例 · LOG24H：本次没探测（加 --probe 做一次只读的 logging read）"
  fi

  printf '\n' >&2
  log "  🔴 **指标建出来 ≠ 已经在采数。** 上表 STATE 全绿只说明「过滤器已经挂上去了」。"
  log "     真正在产生数据的，只有 SIGNAL=LIVE 且前置条件成立的那几条；"
  log "     BLOCKED 与 NONE 那几条现在的值就是 0，而 0 在图上和「一切正常」长得一模一样。"
}

# ───────────────────────── 缺口提醒 ─────────────────────────

report_gaps() {
  step "4 · 建完之后还欠着什么"

  # 要求 3：bp_mail_bounce 必须显式警告，不能混在表里让人自己发现。
  warn "bp_mail_bounce 建出来了，但它在 ESP 接通之前**恒为 0** ——"
  log "     ESP 未接通（api/internal/handler/auth.go 的 TODO(P1)），没有任何东西写退信日志。"
  log "     仍然现在就建，是因为**指标不追溯**：等接通那天再建，「接通头几天退信率多少」"
  log "     这个最该看的窗口就永远没有数据。而 SES 退信率 ≥5% 进审查、≥10% 可能暂停发信，"
  log "     恰恰是接通初期最容易踩的（邮件是 ADR 0002 裁定的唯一失联恢复通道）。"
  log "     ⚠️ 代价记在这里：monitoring §5 第 17 条「邮件退信率 ≥5%」这条告警，"
  log "        在 ESP 接通前**永远不会响，而且它不响与「一切正常」无法区分**。"
  log "     接通时写日志的那段代码请照抄本脚本的约定：日志文案就是指标名 bp_mail_bounce。"
  log "     这样指标不用改，也就没有「改名之间那段时间信号丢失」的窗口。"

  printf '\n' >&2
  warn "两条 BLOCKED 各差一个外部动作，不差代码："
  log "     · bp_cert_issuer_bad —— infra/scripts/check-cert-issuer.sh 在，"
  log "       每日调度也已经有脚本了（infra/scripts/setup-scheduler.sh --only=cert，形态已裁决："
  log "       Cloud Run Job + Scheduler）。但那个脚本**现在同样建不出来**，卡在两件事上："
  log "       (a) 三套域名池一个域名都还没注册 —— 清单为空的每日核对是一条天天报红的 P0；"
  log "       (b) 缺一个装了 bash + openssl + gcloud 且带着该脚本的镜像，本仓库没有构建它的路径。"
  log "       🔴 顺序不能反：**先建指标，再挂调度器**。反了就丢掉挂上去到建指标之间的信号。"
  log "     · bp_node_alive —— api/internal/handler/nodealive.go 在写，但第一台 bp-node-* 还没建成。"
  log "       🔴 monitoring §5.1 坑一：metric absence **需要那条 time series 曾经有过数据**。"
  log "          新节点首次上报成功后要人工确认该 node_id 的 time series 已出现，"
  log "          告警才算 armed（ADR 0007 的建节点流程里那一步）。"

  printf '\n' >&2
  warn "本脚本管不到、但少了它们指标就只是「有数没人看」的部分："
  log "     · monitoring §5 的 17 条**告警策略**：至今 0 条（evidence/gcp-inventory-20260821 §5.3）。"
  log "     · §4 的 **Pub/Sub 通知渠道**：只有 email 一条是通的，Pub/Sub 那半边欠着。"
  log "     · §6 的 **uptime check**：bp-* 一条没有。"
}

# ───────────────────────── 主流程 ─────────────────────────

main() {
  local arg
  for arg in "$@"; do
    case "$arg" in
      --dry-run)     DRY_RUN=1 ;;
      --apply)       DRY_RUN=0 ;;
      --create-only) CREATE_ONLY=1 ;;
      --probe)       DO_PROBE=1 ;;
      --yes)         ASSUME_YES=1 ;;
      --out=*)       OUT_DIR="${arg#*=}" ;;
      --project=*)   PROJECT_ID="${arg#*=}" ;;
      -h|--help)     usage; exit 0 ;;
      *)             usage >&2; die "未知参数：$arg" ;;
    esac
  done

  need_cmd gcloud
  need_cmd jq
  guard_project

  if [ -n "$OUT_DIR" ]; then
    mkdir -p "$OUT_DIR"
    WORK_DIR="$OUT_DIR"
    KEEP_OUT=1
  else
    WORK_DIR="$(mktemp -d "${TMPDIR:-/tmp}/bp-setup-metrics.XXXXXX")"
  fi
  trap cleanup EXIT

  log "项目 : $PROJECT_ID"
  log "指标 : ${#METRICS[@]} 条（monitoring.md §3.2 全量，不是只补缺的）"
  log "工作 : $WORK_DIR"
  if [ "$DRY_RUN" -eq 1 ]; then
    log "模式 : DRY-RUN（默认）—— 只读 describe 照做，任何 create/update 只打印不执行"
  else
    log "模式 : APPLY —— 会真的在 $PROJECT_ID 上建/改指标"
  fi
  log ""
  log "🔴 log-based metric **不追溯**：只统计创建之后写入的日志。"
  log "   2026-08-17 bp-api 首次部署时一条都没建，08-21 才补 7 条 —— 那 4 天数据永久缺失（B42）。"

  # 先把 11 份 YAML 渲染出来。dry-run 也渲染：这样 --out= 能拿到可 review 的产物，
  # 而「命令长什么样」远不如「真正会提交给 API 的资源长什么样」有信息量。
  local name
  for name in "${METRICS[@]}"; do
    render_metric_yaml "$name" "${WORK_DIR}/${name}.yaml"
  done

  plan_all
  apply_all
  report_table
  report_gaps

  step "结果"
  log "  新建 $N_CREATE 条 / 改写 $N_UPDATE 条 / 已一致 $N_OK 条 / 失败 $N_ERROR 条"
  if [ "$KEEP_OUT" -eq 1 ]; then
    log "  生成的 LogMetric YAML 留在：$WORK_DIR"
  fi

  if [ "$N_ERROR" -ne 0 ]; then
    log ""
    log "  🔴 有指标没能建成或状态查不到。**不要**据此认为监控已就位。"
    exit 1
  fi

  if [ "$DRY_RUN" -eq 1 ] && { [ "$N_CREATE" -gt 0 ] || [ "$N_UPDATE" -gt 0 ]; }; then
    log ""
    log "  ⚠️ dry-run：以上都没有执行。有 $((N_CREATE + N_UPDATE)) 条待办 → 退出码 1。"
    log "     真的要建：./infra/scripts/setup-metrics.sh --apply"
    exit 1
  fi

  if [ "$CREATE_ONLY" -eq 1 ] && [ "$N_UPDATE" -gt 0 ]; then
    log ""
    log "  ⚠️ --create-only：$N_UPDATE 条漂移未处理 → 退出码 1。"
    exit 1
  fi

  log ""
  log "  ✅ ${#METRICS[@]} 条指标在 GCP 上都已就位。"
  log "     ⚠️ 这句话**只说明过滤器挂上去了**，不说明它们在采数 —— 看上面第 3 节的 SIGNAL 列。"
  exit 0
}

main "$@"

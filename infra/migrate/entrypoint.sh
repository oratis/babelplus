#!/bin/sh
# bp-migrate 的入口 —— 作为 Cloud Run Job 一次性执行。
#
# 契约：
#   BP_DATABASE_URL  必需。Postgres DSN。Cloud Run Job 上走 Cloud SQL 连接器的 Unix socket。
#   BP_MIGRATE_CMD   可选。up（默认）| down | version | force。
#   BP_MIGRATE_ARG   可选。给 down / force 用的参数。
#
# 退出码：0 = 成功；非 0 = 失败。**Job 必须配 --max-retries=0**（deploy.md §6.3）——
# 重试一个改到一半的 schema 比直接失败更糟。

set -eu

log() { printf '%s %s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$*"; }
die() { log "ERROR: $*"; exit 1; }

[ -n "${BP_DATABASE_URL:-}" ] || die "缺少 BP_DATABASE_URL"

CMD="${BP_MIGRATE_CMD:-up}"
ARG="${BP_MIGRATE_ARG:-}"

# golang-migrate 的 postgres driver 自带 advisory lock（Lock/Unlock 走 pg_advisory_lock），
# 所以并发跑多个实例是安全的 —— 后来者会阻塞等待而不是并行改 schema。
# 这一条是**驱动提供的**，不是我们额外加的；升级 golang-migrate 时要复核它没变。
#
# ⚠️ 但 advisory lock 只防并发，不防「上一次跑到一半失败」。
# 那种情况会在 schema_migrations 表留下 dirty=true，migrate 会拒绝继续 —— 这是对的。
# 处理 dirty 必须人工判断到底应用了多少，然后 force 到正确版本，**不能盲目 force**。

log "迁移命令: $CMD ${ARG}"

# 先报当前版本，失败不致命（首次跑时 schema_migrations 还不存在）
CURRENT="$(migrate -path /migrations -database "$BP_DATABASE_URL" version 2>&1 || true)"
log "迁移前版本: ${CURRENT:-<无>}"

# 🔴 dirty 闸门**必须放行 force 与 version**，否则它挡掉的正是自己指定的那条恢复路径。
#
# 这里曾经是无条件 die（不看 $CMD）。后果：schema 一旦 dirty，
# 上面 die 消息里让运维跑的 `BP_MIGRATE_CMD=force BP_MIGRATE_ARG=<版本号>`
# 会被同一道闸门再挡一次，连 `version` 都查不了 ——
# 生产库卡在 dirty，唯一的出路是绕过 entrypoint（覆盖 Job 的 --command）
# 或直连 Cloud SQL 手改 schema_migrations，而那恰恰是「不能盲目 force」
# 这条纪律最不希望发生的操作方式。
#
# 「不能盲目 force」推出的是「dirty 下不许继续 up/down」，
# **推不出**「连显式带版本号的 force 也一起挡掉」——
# 显式 force + 下面 force 分支强制的 BP_MIGRATE_ARG 本身就是人工判断的结果。
case "$CURRENT" in
  *dirty*)
    case "$CMD" in
      force)
        log "⚠️ 数据库处于 dirty 状态（$CURRENT），本次是显式 force，放行。"
        ;;
      version)
        log "⚠️ 数据库处于 dirty 状态（$CURRENT）。只查版本，放行。"
        ;;
      *)
        die "数据库处于 dirty 状态（$CURRENT），拒绝执行 $CMD。
     这表示上一次迁移执行到一半失败了。**不要盲目 force。**
     处置：登库核对 schema 实际应用到哪一步，确认后再
       BP_MIGRATE_CMD=force BP_MIGRATE_ARG=<正确版本号> 跑一次，
     然后重新 up。force 与 version 这两条在 dirty 状态下是放行的，
     不需要绕过本脚本。详见 docs/04-ops/deploy.md §6.3。"
        ;;
    esac
    ;;
esac

case "$CMD" in
  up)
    migrate -path /migrations -database "$BP_DATABASE_URL" up
    ;;
  down)
    [ -n "$ARG" ] || die "down 必须显式给步数（BP_MIGRATE_ARG），不接受无参数全量回滚"
    migrate -path /migrations -database "$BP_DATABASE_URL" down "$ARG"
    ;;
  version)
    migrate -path /migrations -database "$BP_DATABASE_URL" version
    exit 0
    ;;
  force)
    [ -n "$ARG" ] || die "force 必须给目标版本号（BP_MIGRATE_ARG）"
    log "⚠️ force 到版本 $ARG —— 这会直接改写 schema_migrations，不执行任何 SQL"
    migrate -path /migrations -database "$BP_DATABASE_URL" force "$ARG"
    ;;
  *)
    die "未知的 BP_MIGRATE_CMD: $CMD（可选 up / down / version / force）"
    ;;
esac

AFTER="$(migrate -path /migrations -database "$BP_DATABASE_URL" version 2>&1 || true)"
log "迁移后版本: ${AFTER:-<无>}"

# 收尾自检：确认核心表在位。不是全量校验，只是「迁移声称成功但表没建出来」的兜底。
TABLES="$(psql "$BP_DATABASE_URL" -qtA -c \
  "select count(*) from information_schema.tables where table_schema='public' and table_type='BASE TABLE';" 2>/dev/null || echo '?')"
log "public schema 基表数: $TABLES"

case "$CMD" in
  up)
    case "$TABLES" in
      ''|'?'|0)
        die "迁移声称成功，但 public schema 里一张基表都没有。视为失败。"
        ;;
    esac
    ;;
esac

log "完成"

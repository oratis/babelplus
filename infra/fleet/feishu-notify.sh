#!/usr/bin/env bash
# =============================================================================
# feishu-notify.sh · 自用机队的飞书出口（应用「胖猫」）
#
# 事实源：docs/04-ops/personal-fleet-runbook.md §3.3
#         docs/05-adr/0017-personal-fleet-in-repo.md §6（凭据红线）
#
# -----------------------------------------------------------------------------
# 🔴 凭据红线：App Secret 只允许存在于
#   ① 本机 gitignored 的 infra/fleet/.secrets.env
#   ② vpn-ops 的 Secret Manager 引用
# **不下发到任何一台代理节点。** 代理节点是暴露面最大的资产，它们需要的只是
# 「把巡检 JSON 交出去」的能力，不需要「以胖猫身份发消息」的能力。
#
# 凭据只经环境变量传入，不落盘、不进命令行、不进 shell history。
# -----------------------------------------------------------------------------
#
# 用法：
#   ./feishu-notify.sh --bot-info                   机器人自检
#   ./feishu-notify.sh --list-chats                 列出机器人所在会话，拿 chat_id
#   ./feishu-notify.sh --whoami <邮箱|手机号>        反查 open_id（需 contact:user.id:readonly）
#   ./feishu-notify.sh --text "..."                 发纯文本
#   ./feishu-notify.sh --card <file.json>           发交互卡片（日报走这条）
#
# 收件人取自 FEISHU_RECEIVE_ID / FEISHU_RECEIVE_ID_TYPE，可用 --to / --to-type 覆盖。
# =============================================================================
set -euo pipefail

BASE="${FEISHU_BASE:-https://open.feishu.cn}"   # 与 open.larksuite.com 返回同一租户；规范用这个
APP_ID="${FEISHU_APP_ID:-}"
APP_SECRET="${FEISHU_APP_SECRET:-}"
TO="${FEISHU_RECEIVE_ID:-}"
TO_TYPE="${FEISHU_RECEIVE_ID_TYPE:-open_id}"    # open_id | user_id | union_id | email | chat_id

HERE="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
PYDIR="$HERE/_py"

die() { printf '%s\n' "$*" >&2; exit 1; }
usage() { sed -n '19,25p' "$0" | sed 's/^# \{0,1\}//'; exit 0; }

# tenant_access_token 有效期 7200s。每次调用重新取：脚本是短命进程，
# 把 token 缓存到磁盘等于让它落盘，违反上面的凭据红线。
token() {
  [ -n "$APP_ID" ]     || die "缺少 FEISHU_APP_ID（set -a; source infra/fleet/.secrets.env; set +a）"
  [ -n "$APP_SECRET" ] || die "缺少 FEISHU_APP_SECRET"
  curl -sS -m 20 -X POST "$BASE/open-apis/auth/v3/tenant_access_token/internal" \
      -H 'Content-Type: application/json; charset=utf-8' \
      -d "$(APP_ID="$APP_ID" APP_SECRET="$APP_SECRET" python3 "$PYDIR/authbody.py")" \
    | python3 "$PYDIR/token.py"
}

api_get() { curl -sS -m 20 "$BASE$1" -H "Authorization: Bearer $2"; }
pretty()  { python3 -m json.tool 2>/dev/null || cat; }

# content 必须是**字符串化的 JSON**（飞书契约如此，不是嵌套对象）——
# 写成对象不会报错，只会静默发不出去，属于最难排查的一类失败。
send() {
  local tok="$1" msg_type="$2" content="$3"
  [ -n "$TO" ] || die "缺少收件人：设 FEISHU_RECEIVE_ID 或传 --to。
  取 open_id：  $0 --whoami <邮箱|手机号>
  取 chat_id：  $0 --list-chats   （建一个只有你和胖猫的群，零额外权限）"
  curl -sS -m 30 -X POST "$BASE/open-apis/im/v1/messages?receive_id_type=$TO_TYPE" \
    -H "Authorization: Bearer $tok" \
    -H 'Content-Type: application/json; charset=utf-8' \
    -d "$(TO="$TO" MT="$msg_type" C="$content" python3 "$PYDIR/sendbody.py")" | pretty
}

MODE=""; ARG=""
while [ $# -gt 0 ]; do
  case "$1" in
    --list-chats|--bot-info) MODE="${1#--}";;
    --whoami|--text|--card)  MODE="${1#--}"; ARG="${2:?$1 需要一个参数}"; shift;;
    --to)                    TO="${2:?}"; shift;;
    --to-type)               TO_TYPE="${2:?}"; shift;;
    -h|--help)               usage;;
    *)                       die "未知参数：$1（--help 看用法）";;
  esac
  shift
done
[ -n "$MODE" ] || usage

TOK="$(token)"
case "$MODE" in
  bot-info)   api_get "/open-apis/bot/v3/info" "$TOK" | pretty;;
  list-chats) api_get "/open-apis/im/v1/chats?page_size=50" "$TOK" | python3 "$PYDIR/chats.py";;
  whoami)
    # 需应用具备 contact:user.id:readonly。没有这个权限就走 --list-chats 那条路。
    curl -sS -m 20 -X POST "$BASE/open-apis/contact/v3/users/batch_get_id?user_id_type=open_id" \
      -H "Authorization: Bearer $TOK" -H 'Content-Type: application/json; charset=utf-8' \
      -d "$(V="$ARG" python3 "$PYDIR/whoami_body.py")" | pretty;;
  text)  send "$TOK" text "$(C="$ARG" python3 "$PYDIR/textbody.py")";;
  card)  [ -f "$ARG" ] || die "找不到卡片文件：$ARG"
         send "$TOK" interactive "$(python3 "$PYDIR/cardbody.py" "$ARG")";;
esac

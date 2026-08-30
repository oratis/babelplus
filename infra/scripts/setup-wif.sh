#!/usr/bin/env bash
#
# setup-wif.sh —— 把 `.github/workflows/deploy.yml` 缺的那两个值**建出来并打印出来**
#
# 事实源：
#   .github/workflows/deploy.yml 顶部「为什么不用长期密钥 JSON」一节（本脚本是它的可执行形式）
#   docs/04-ops/deploy.md §1（五条禁令）· §3.3（IAM 授权范围）
#   docs/02-architecture/as-built-gcp.md §2.1（隔离承诺）· §4（三个现有 Cloud Run）· §5（现有 SA 与 secret）
#   infra/deploy/setup-infra.sh（bp-deploy-sa 及其三条部署权限的**所有者**，本脚本只核对不重复授）
#   infra/scripts/verify-isolation.sh（部署门禁；它要的只读权限由本脚本授，见 step_read_roles）
#   docs/00-overview/roadmap.md R7（共享 GCP 项目的爆炸半径 —— 本脚本每一条授权的取舍都来自它）
#
# 解决的问题：仓库现在 **0 个 environment / 0 个 variable / 0 个 secret**，
# `deploy.yml` 的 `vars.GCP_WIF_PROVIDER` 与 `vars.GCP_DEPLOY_SA` 都是空的，
# 于是 isolation-before 作业在第一步硬失败 —— 那正是设计意图（缺凭据不许静默跳过认证），
# 但「缺什么」一直没有可执行的补法。本脚本就是那个补法：
# 跑一次 → GCP 侧建好 → **结尾打印两个确切取值与两条 `gh variable set` 命令**，复制粘贴即可。
#
# ───────────────────────── 🔴 这个脚本存在的首要理由：attribute-condition ─────────────────────────
#
# Workload Identity Federation 最经典的一次配置事故是**建了 provider 却不加
# `--attribute-condition`**。GitHub 的 OIDC issuer 是**全球共用**的一个
# （https://token.actions.githubusercontent.com）—— 它给地球上每一个 GitHub Actions 作业
# 签发 token，包括任何陌生人 5 分钟前刚建的仓库。
#
# 不限定 `assertion.repository` 的后果不是「配置不严谨」，是：
#   **任何人在任何仓库里放一个 workflow，就能换到 bp-deploy-sa 的 GCP 短期凭据**，
#   而这个项目里住着 vpn-us / vpn-jp 与 anthropic-relay / lisa-cloud / lisa-web（as-built §2/§4）。
#
# 本脚本因此在**两个层次**上各限定一次，两层都不是可选的：
#   1. provider 的 `--attribute-condition`：assertion.repository == 'oratis/babelplus'
#      —— 换不到 STS token，在**联邦入口**就被拒。
#   2. SA 绑定的 principalSet 写到 `attribute.repository/oratis/babelplus`，**不是** `/*`
#      —— 即使有人日后把上面那条 condition 放宽了，池内的其它身份仍然冒充不了 bp-deploy-sa。
#   为什么要两层：这两处**没有任何机制保证同步**（与 infra/deploy/README.md §6 第 1 条同一类债）。
#   任何一层单独失守时，另一层仍然拦得住。
#
# ───────────────────────── 本脚本**默认 dry-run** ─────────────────────────
#
# 与 infra/ 下多数脚本相反（它们默认执行、`--dry-run` 才预演），本脚本**默认只打印**，
# 必须显式 `--apply` 才会发出写操作。理由就是上面那一条：一次配错的 WIF 不会报错、
# 不会变红、不会有任何现象 —— 它只是**安静地把生产项目的部署权限交给全世界**。
# 这类「错了也没有反馈」的操作，默认值必须站在安全那一侧。
#
# ───────────────────────── 排障备忘（写在这里免得有人靠删重建来试）─────────────────────────
#
# ⚠️ 删掉的 pool / provider 是**软删除**：名字在 30 天内不能重用（gcloud 提供 undelete）。
#    所以「删了重建」不是排障手段 —— 它会让你在 30 天里连原来的名字都用不了。
#    配错了就用 `providers update-oidc` 改（本脚本的 --apply 会走这条路，见 step_provider）。
#
# 本脚本只**新增** bp- 前缀资源与 IAM 绑定，没有任何 delete。

set -euo pipefail

# ───────────────────────── 防呆常量 ─────────────────────────

readonly EXPECTED_PROJECT_ID="oratis-491316"

# 项目编号来自 as-built §5 实测的 `2360090741-compute@developer.gserviceaccount.com`
# 与 deploy.md §8.1 里那个硬编码的 run.app 主机名。写在这里有两个用途：
#   1. 未鉴权时（比如只想看一眼 --dry-run 输出）也能把 provider 的**完整取值**打全；
#   2. 真跑时与线上取到的项目号**对拍**，不一致就退出 —— 那意味着 gcloud 指着别的项目。
readonly EXPECTED_PROJECT_NUMBER="2360090741"

readonly REGION="us-central1"

# 🔴 本脚本最重要的一个常量。它必须是**仓库全名**，大小写与 GitHub 上完全一致。
#    刻意不提供 --repo= 覆盖：一个能被命令行改掉的安全边界不是安全边界。
#    仓库真的搬家时，改这一行，然后重跑一次（provider 的 condition 会被 update 上去）。
readonly GITHUB_REPO="oratis/babelplus"

# GitHub Actions 的 OIDC issuer。**全球共用**，见文件头。
readonly GITHUB_ISSUER="https://token.actions.githubusercontent.com"

# 资源名一律 bp- 前缀（as-built §2.1 第 1 条的命名前缀隔离）。
readonly POOL="bp-github-pool"
readonly PROVIDER="bp-github-oidc"
readonly SA_DEPLOY="bp-deploy-sa"        # setup-infra.sh --step=iam 建的那个，本脚本不换新的
readonly SA_API="bp-api-sa"              # 只用来核对 serviceAccountUser 绑定
readonly AR_REPO="bp-images"             # 只用来核对仓库级 writer 绑定
readonly RUN_SERVICE="bp-api"            # 只用来核对服务级 run.developer 绑定

# 属性映射。google.subject 是必填项；attribute.repository 是 principalSet 的依据，
# **缺了它整条链就断**（绑定永远匹配不上，表现为 auth 步骤一句难懂的 403）。
# repository_owner / ref 不参与判定，只是为了让审计日志里能看出是哪个分支跑的。
readonly ATTR_MAPPING="google.subject=assertion.sub,attribute.repository=assertion.repository,attribute.repository_owner=assertion.repository_owner,attribute.ref=assertion.ref"

# 与文件头第 1 层对应。单引号是 CEL 字符串字面量的一部分，不是 shell 引号。
readonly ATTR_CONDITION="assertion.repository == '${GITHUB_REPO}'"

# 危险操作的确认串。不是 y/N —— y/N 是肌肉记忆，键入这一串不是（README §1 第 4 条）。
readonly CONFIRM_APPLY="create-bp-wif"
readonly CONFIRM_RECONDITION="overwrite-wif-condition"

# ───────────────────────── 运行时开关 ─────────────────────────

PROJECT_ID="${PROJECT_ID:-$EXPECTED_PROJECT_ID}"
DRY_RUN=1          # ← 默认 dry-run，见文件头
ASSUME_YES=0
PROBE=1            # 能否做只读探测（未鉴权时置 0）
PROJECT_NUMBER=""
FAIL_N=0
WARN_N=0

# ───────────────────────── 通用工具（与 infra/ 下其它脚本刻意保持重复）─────────────────────────
#
# 守卫代码在每个脚本里各复制一份是**故意的**：每个脚本都要能单独 scp 出去跑，
# 且单独具备「打错项目就拒绝」的能力。代价记在 infra/deploy/README.md §6 第 1 条。

log()  { printf '%s\n' "$*" >&2; }
step() { printf '\n\033[1m▸ %s\033[0m\n' "$*" >&2; }
ok()   { printf '  \033[32m✓\033[0m %s\n' "$*" >&2; }
skip() { printf '  · %s\n' "$*" >&2; }
warn() { printf '  \033[33m⚠\033[0m %s\n' "$*" >&2; WARN_N=$((WARN_N + 1)); }
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

# run 是全部**写操作**的唯一入口。本脚本的命令里没有任何凭据，故不需要遮蔽。
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

# confirm 要求手工键入一个特定字符串。
confirm() {
  local prompt="$1" expect="$2" answer=""
  if [ "$DRY_RUN" -eq 1 ]; then
    skip "[dry-run] 跳过确认：$prompt"
    return 0
  fi
  if [ "$ASSUME_YES" -eq 1 ]; then
    warn "--yes 已跳过确认：$prompt"
    return 0
  fi
  if [ ! -t 0 ]; then
    die "需要交互确认但 stdin 不是终端：$prompt
     非交互场景请显式加 --yes（并确保你已经读过 --dry-run 的输出）。"
  fi
  printf '\n%s\n请输入 %s 确认（其它任何输入都会中止）：' "$prompt" "$expect" >&2
  read -r answer
  if [ "$answer" != "$expect" ]; then
    die "确认失败，已中止。未做任何修改。"
  fi
}

need_cmd() {
  command -v "$1" >/dev/null 2>&1 || die "缺少命令：$1"
}

usage() {
  cat <<'EOF'
用法: setup-wif.sh [选项]

建好 GitHub Actions → GCP 的 Workload Identity Federation，并**打印出
.github/workflows/deploy.yml 要的两个仓库变量的确切取值**与现成的 gh 命令。

**默认 dry-run。** 想真的动 GCP 必须显式加 --apply。

它会做什么（每一步都幂等，已存在就跳过）:
  1. 启用 iamcredentials / sts / iam 三个 API（缺一个 WIF 就换不到凭据）
  2. 建 Workload Identity Pool  bp-github-pool
  3. 建 OIDC Provider          bp-github-oidc
     🔴 带 --attribute-condition 限定 assertion.repository == 'oratis/babelplus'
        不限定 = 任何仓库的任何 workflow 都能换到本项目的部署权限
  4. 确认部署用服务账号 bp-deploy-sa 存在（正主是 setup-infra.sh --step=iam）
  5. 授 roles/iam.workloadIdentityUser，principalSet **限定到本仓库**，不是 /*
  6. 授 verify-isolation.sh 这道部署门禁要的 5 个**只读**角色
  7. 核对（不重复授）setup-infra.sh 拥有的 3 条部署权限，缺了就告诉你补哪一条

它**不会**做什么:
  · 不建 WIF 之外的任何资源（Cloud Run / Cloud SQL / secret 一概不碰）
  · 不删任何东西
  · 不碰 GitHub 仓库设置 —— 那要你自己的凭据，脚本只把命令打出来给你贴

选项:
  --apply         真的执行写操作。不加就只打印（默认）
  --dry-run       显式声明只打印（默认行为，为肌肉记忆保留）
  --yes           跳过手工确认串。⚠️ 只在你读过 --dry-run 输出之后用
  --project=<id>  GCP 项目 ID。必须是 oratis-491316
  -h, --help      显示本帮助

退出码:
  0  全部就绪（或 dry-run 正常打印完）
  1  有检查不通过 —— 见输出里的 ✗
  2  用法或环境错误（缺 gcloud、项目不对、未鉴权却要 --apply……）

典型顺序:
  ./infra/scripts/setup-wif.sh                 # 先看清楚要做什么
  ./infra/scripts/setup-wif.sh --apply         # 真的建（要手工键入 create-bp-wif）
  # 然后把它最后打印的两条 gh variable set 贴到终端里跑
  gh variable list --repo oratis/babelplus     # 核对
EOF
}

# guard_project：命令行/环境给的项目 ID 必须是本仓库唯一认的那个。
guard_project() {
  if [ "$PROJECT_ID" != "$EXPECTED_PROJECT_ID" ]; then
    die "PROJECT_ID 必须是 $EXPECTED_PROJECT_ID，当前是 \"$PROJECT_ID\"。
     本仓库的全部资产清点、隔离承诺与 IAM 设计都只对这一个项目成立
     （docs/02-architecture/as-built-gcp.md）。"
  fi
}

# guard_active_project：**当前 gcloud 的活动项目**也必须一致。
#
# 本脚本每条 gcloud 都显式带 --project，照理说活动项目是什么都不影响结果 ——
# 但它影响的是**人**：活动项目指着别处时，操作者对「我现在在哪个项目里」的判断是错的，
# 而下一条手敲的命令多半不会带 --project。deploy.md §2 的原话：
# 「gcloud config set project 打错项目是本文最现实的事故源」。
# 这个项目是共享的（roadmap R7），跑错项目会动到 anthropic-relay / lisa-* / vpn-*。
guard_active_project() {
  local active
  active="$(gcloud config get-value project 2>/dev/null || true)"
  case "$active" in
    "$EXPECTED_PROJECT_ID") return 0 ;;
    ''|'(unset)')
      die "gcloud 当前没有设置活动项目。
     先执行：gcloud config set project ${EXPECTED_PROJECT_ID}
     （本脚本每条命令都带 --project，这道断言拦的是**下一条你手敲的命令**。）" ;;
    *)
      die "gcloud 活动项目是 \"$active\"，不是 ${EXPECTED_PROJECT_ID}。
     ${EXPECTED_PROJECT_ID} 是**共享项目**（roadmap R7）：里面还住着 vpn-us / vpn-jp
     与 anthropic-relay / lisa-cloud / lisa-web。跑错项目会动到不属于本项目的东西。
     先执行：gcloud config set project ${EXPECTED_PROJECT_ID}" ;;
  esac
}

# guard_bp_prefix：本脚本创建的每个资源名都必须带 bp- 前缀（as-built §2.1 第 1 条）。
guard_bp_prefix() {
  local name
  for name in "$@"; do
    case "$name" in
      bp-*) : ;;
      *) die "资源名 \"$name\" 不带 bp- 前缀，违反 as-built §2.1 第 1 条的命名前缀隔离。" ;;
    esac
  done
}

# check_auth：没有活动账号时全部只读探测都会失败。
# 那时把「探测不到」当成「不存在」会打印出一串误导性的创建命令，所以显式降级成「不探测」。
check_auth() {
  local acct
  acct="$(gcloud auth list --filter=status:ACTIVE --format='value(account)' 2>/dev/null || true)"
  if [ -n "$acct" ]; then
    log "账号 : $acct"
    return 0
  fi
  PROBE=0
  if [ "$DRY_RUN" -eq 0 ]; then
    die "gcloud 没有活动账号，--apply 无从谈起。先执行：gcloud auth login"
  fi
  log "账号 : （无）—— 只读探测全部跳过，下面打印的是**完整**流程；"
  log "        真跑时已经存在的步骤会被跳过，不会重复创建。"
}

# ───────────────────────── 项目编号 ─────────────────────────

resolve_project_number() {
  if [ "$PROBE" -eq 0 ]; then
    PROJECT_NUMBER="$EXPECTED_PROJECT_NUMBER"
    warn "未鉴权，项目编号用写死的 ${EXPECTED_PROJECT_NUMBER}（来源见文件头）。
     真跑时会与线上取到的值对拍。"
    return 0
  fi
  PROJECT_NUMBER="$(gcloud projects describe "$PROJECT_ID" \
    --format='value(projectNumber)' 2>/dev/null || true)"
  if [ -z "$PROJECT_NUMBER" ]; then
    PROJECT_NUMBER="$EXPECTED_PROJECT_NUMBER"
    warn "取不到项目编号（权限不足？），退回写死的 ${EXPECTED_PROJECT_NUMBER}。
     ⚠️ 若它与线上不符，本脚本最后打印的 GCP_WIF_PROVIDER 就是错的 —— 配上去会得到一句
        难懂的 403。真跑前请用 `gcloud projects describe ${PROJECT_ID}` 自行核对一次。"
    return 0
  fi
  if [ "$PROJECT_NUMBER" != "$EXPECTED_PROJECT_NUMBER" ]; then
    die "项目编号对不上：线上是 $PROJECT_NUMBER，本脚本写死的是 $EXPECTED_PROJECT_NUMBER。
     要么 gcloud 指着另一个项目（危险，立即停手核对），
     要么 as-built §5 的实测值过期了（那就先更新文档再改本脚本的常量）。"
  fi
  ok "项目编号 $PROJECT_NUMBER（与 as-built §5 一致）"
}

# 两个最终产物。放在函数里是因为它们依赖 PROJECT_NUMBER，而那是运行期才知道的。
wif_provider_value() {
  printf 'projects/%s/locations/global/workloadIdentityPools/%s/providers/%s' \
    "$PROJECT_NUMBER" "$POOL" "$PROVIDER"
}
deploy_sa_email() {
  printf '%s@%s.iam.gserviceaccount.com' "$SA_DEPLOY" "$PROJECT_ID"
}
# 🔴 principalSet 必须写到 attribute.repository/<owner>/<repo>。
#    写成 .../workloadIdentityPools/<pool>/* 是**池内任何身份都能冒充 bp-deploy-sa**。
principal_set() {
  printf 'principalSet://iam.googleapis.com/projects/%s/locations/global/workloadIdentityPools/%s/attribute.repository/%s' \
    "$PROJECT_NUMBER" "$POOL" "$GITHUB_REPO"
}

# ───────────────────────── IAM 策略核对 ─────────────────────────

# has_binding <role> <member> <取策略的 gcloud 命令...>
#   0 = 有；1 = 没有；2 = 读不到策略（权限不足 / 资源不存在 / 未鉴权）
#
# 用 --flatten + --format 而不是 jq：本脚本因此不依赖 jq。
# member 不放进 --filter 是刻意的 —— principalSet 里有 `:` 与 `/`，
# 塞进 filter 表达式要处理转义，转义写错的表现是「静默匹配不到」→ 误报缺权限。
has_binding() {
  local role="$1" member="$2"; shift 2
  local out
  if ! out="$("$@" --flatten='bindings[].members' \
        --filter="bindings.role=${role}" \
        --format='value(bindings.members)' 2>/dev/null)"; then
    return 2
  fi
  printf '%s\n' "$out" | grep -Fxq "$member"
}

# report_binding <说明> <role> <member> <取策略的 gcloud 命令...>
# 只核对不修改。用于 setup-infra.sh 拥有的那三条权限 —— 两个脚本都授同一条绑定
# 会变成第二处会漂移的真相（README §6 第 1 条那类债），所以这里只报缺失并给出补法。
report_binding() {
  local what="$1" role="$2" member="$3"; shift 3
  local rc=0
  has_binding "$role" "$member" "$@" || rc=$?
  case "$rc" in
    0) ok   "$what（$role 已在）" ;;
    1) fail "$what 缺失：$role
     它属于 infra/deploy/setup-infra.sh，本脚本不代授。补法见输出末尾。" ;;
    *) warn "$what 读不到 IAM 策略，本条**未核对**（未鉴权 / 权限不足 / 资源尚不存在）" ;;
  esac
}

# grant <说明> <role> <member> —— 项目级绑定。本脚本拥有的那几条走这里。
# --condition=None 不能省：不带它时 gcloud 会转成交互式提问，在 CI/非终端里会挂住。
grant_project_role() {
  local what="$1" role="$2" member="$3"
  local rc=0
  if [ "$PROBE" -eq 1 ]; then
    has_binding "$role" "$member" gcloud projects get-iam-policy "$PROJECT_ID" || rc=$?
    if [ "$rc" -eq 0 ]; then
      skip "$what（$role 已在，跳过）"
      return 0
    fi
  fi
  run gcloud projects add-iam-policy-binding "$PROJECT_ID" \
    --member="$member" \
    --role="$role" \
    --condition=None
  ok "$what ← $role"
}

# ───────────────────────── 步骤 ─────────────────────────

# 1/7 API
#
# 三个都缺一不可：
#   sts.googleapis.com          用 OIDC token 换联邦 token（WIF 的第一跳）
#   iamcredentials.googleapis.com  用联邦 token 换 bp-deploy-sa 的短期访问令牌（第二跳）
#   iam.googleapis.com          pool / provider 这两类资源本身归它管
# as-built §6 记录 iam 已启用，另两个**未记录** —— 所以照样探一次，不假设。
step_apis() {
  step "1/7 启用 WIF 需要的三个 API"
  local api
  for api in sts.googleapis.com iamcredentials.googleapis.com iam.googleapis.com; do
    if [ "$PROBE" -eq 1 ] && gcloud services list --enabled --project="$PROJECT_ID" \
         --filter="config.name=${api}" --format='value(config.name)' 2>/dev/null \
         | grep -Fxq "$api"; then
      skip "$api 已启用"
      continue
    fi
    run gcloud services enable "$api" --project="$PROJECT_ID"
    ok "$api 已启用"
  done
}

# 2/7 Pool
step_pool() {
  step "2/7 Workload Identity Pool"
  guard_bp_prefix "$POOL"
  if [ "$PROBE" -eq 1 ] && gcloud iam workload-identity-pools describe "$POOL" \
       --project="$PROJECT_ID" --location=global >/dev/null 2>&1; then
    skip "pool $POOL 已存在"
    return 0
  fi
  run gcloud iam workload-identity-pools create "$POOL" \
    --project="$PROJECT_ID" \
    --location=global \
    --display-name="babel.plus GitHub Actions" \
    --description="GitHub Actions（${GITHUB_REPO}）换取 GCP 短期凭据；仓库里不存长期密钥 JSON"
  ok "pool $POOL 已创建"
}

# 3/7 Provider —— 本脚本的核心
step_provider() {
  step "3/7 OIDC Provider（🔴 attribute-condition 就在这一步）"
  guard_bp_prefix "$PROVIDER"

  local exists=0 cur_cond="" cur_issuer="" cur_map=""
  if [ "$PROBE" -eq 1 ] && gcloud iam workload-identity-pools providers describe "$PROVIDER" \
       --project="$PROJECT_ID" --location=global --workload-identity-pool="$POOL" \
       --format='value(name)' >/dev/null 2>&1; then
    exists=1
    cur_cond="$(gcloud iam workload-identity-pools providers describe "$PROVIDER" \
      --project="$PROJECT_ID" --location=global --workload-identity-pool="$POOL" \
      --format='value(attributeCondition)' 2>/dev/null || true)"
    cur_issuer="$(gcloud iam workload-identity-pools providers describe "$PROVIDER" \
      --project="$PROJECT_ID" --location=global --workload-identity-pool="$POOL" \
      --format='value(oidc.issuerUri)' 2>/dev/null || true)"
    cur_map="$(gcloud iam workload-identity-pools providers describe "$PROVIDER" \
      --project="$PROJECT_ID" --location=global --workload-identity-pool="$POOL" \
      --format='value(attributeMapping)' 2>/dev/null || true)"
  fi

  if [ "$exists" -eq 0 ]; then
    # gcloud 对 GitHub 这类**公共 issuer** 会要求必须给 --attribute-condition
    # （不给会直接报错拒绝创建）。这条产品侧的强制很好，但它只保证「有条件」，
    # 不保证「条件写对了」—— 一条 `assertion.repository_owner == 'oratis'`
    # 同样能过它那一关，而那等于把权限交给该账号下的**任何**仓库，包括 fork 出来的。
    run gcloud iam workload-identity-pools providers create-oidc "$PROVIDER" \
      --project="$PROJECT_ID" \
      --location=global \
      --workload-identity-pool="$POOL" \
      --display-name="GitHub Actions ${GITHUB_REPO}" \
      --issuer-uri="$GITHUB_ISSUER" \
      --attribute-mapping="$ATTR_MAPPING" \
      --attribute-condition="$ATTR_CONDITION"
    ok "provider $PROVIDER 已创建，condition = $ATTR_CONDITION"
    return 0
  fi

  # 已存在：**逐项核对**，不合就报红并给出 update 路径。
  local drift=0
  if [ "$cur_issuer" = "$GITHUB_ISSUER" ]; then
    ok "issuer = $cur_issuer"
  else
    fail "issuer 不是 GitHub 的那个：实际 ${cur_issuer:-<空>}，期望 $GITHUB_ISSUER"
    drift=1
  fi

  case "$cur_map" in
    *attribute.repository=assertion.repository*)
      ok "attribute-mapping 含 attribute.repository" ;;
    *)
      fail "attribute-mapping 里没有 attribute.repository（实际：${cur_map:-<空>}）
     🔴 principalSet 绑定依赖这个属性。缺了它绑定永远匹配不上，
        表现是 auth 步骤一句难懂的 403，而不是「配置有误」。"
      drift=1 ;;
  esac

  if [ "$cur_cond" = "$ATTR_CONDITION" ]; then
    ok "attribute-condition = $cur_cond"
  else
    fail "🔴 **attribute-condition 与期望不符。**
     实际: ${cur_cond:-<空 —— 任何仓库都能换到本项目的部署权限>}
     期望: $ATTR_CONDITION
     在改对之前，**不要**把 GCP_WIF_PROVIDER 配进仓库变量。"
    drift=1
  fi

  if [ "$drift" -eq 0 ]; then
    skip "provider $PROVIDER 已存在且逐项一致"
    return 0
  fi

  # 覆盖一个已存在 provider 的判定条件，是本脚本唯一一处**改既有资源**的操作。
  # 它单独要一个确认串：上面那几条 ✗ 里，有的是「有人手工放宽过」，
  # 那种情况下应该先去搞清楚是谁放宽的、为什么，而不是让脚本一键盖回去。
  confirm "将用 update-oidc 覆盖 $PROVIDER 的 issuer / mapping / condition。
上面每一条 ✗ 都要先看懂再决定 —— 如果是有人手工放宽过，先查清原因。" "$CONFIRM_RECONDITION"
  run gcloud iam workload-identity-pools providers update-oidc "$PROVIDER" \
    --project="$PROJECT_ID" \
    --location=global \
    --workload-identity-pool="$POOL" \
    --issuer-uri="$GITHUB_ISSUER" \
    --attribute-mapping="$ATTR_MAPPING" \
    --attribute-condition="$ATTR_CONDITION"
  ok "provider $PROVIDER 已更新"
}

# 4/7 部署用服务账号
#
# 正主是 infra/deploy/setup-infra.sh --step=iam。这里只在它**不存在**时补建，
# 因为「先跑 setup-wif.sh」是一条完全合理的顺序，不该在这里硬失败。
step_sa() {
  step "4/7 部署用服务账号"
  guard_bp_prefix "$SA_DEPLOY"
  local email
  email="$(deploy_sa_email)"
  if [ "$PROBE" -eq 1 ] && gcloud iam service-accounts describe "$email" \
       --project="$PROJECT_ID" >/dev/null 2>&1; then
    skip "服务账号 $email 已存在（由 setup-infra.sh --step=iam 建）"
    return 0
  fi
  run gcloud iam service-accounts create "$SA_DEPLOY" \
    --project="$PROJECT_ID" \
    --display-name="babel.plus $SA_DEPLOY"
  ok "服务账号 $email 已创建"
  # 🔴 绝不复用 2360090741-compute@developer.gserviceaccount.com（as-built §5）：
  #    它被现有工作负载共用且权限过大，拿它当 CI 身份等于把 babel.plus 的爆炸半径接到 lisa-* 上。
}

# 5/7 WIF 绑定 —— 第二层限定
step_wif_binding() {
  step "5/7 让本仓库（且只有本仓库）能冒充 $SA_DEPLOY"
  local email member
  email="$(deploy_sa_email)"
  member="$(principal_set)"

  log "  member = $member"
  log "  🔴 结尾是 attribute.repository/${GITHUB_REPO}，不是 /* —— 见文件头第 2 层。"

  local rc=0
  if [ "$PROBE" -eq 1 ]; then
    has_binding roles/iam.workloadIdentityUser "$member" \
      gcloud iam service-accounts get-iam-policy "$email" --project="$PROJECT_ID" || rc=$?
    if [ "$rc" -eq 0 ]; then
      skip "roles/iam.workloadIdentityUser 已在"
      return 0
    fi
  fi
  # roles/iam.workloadIdentityUser 里带的是 iam.serviceAccounts.getAccessToken 一类权限，
  # 也就是 google-github-actions/auth 那一步真正要用的东西。没有它的现象是：
  # STS 那一跳成功、第二跳 403 —— 报错文本讲的是 token 而不是权限，很难一眼看懂。
  run gcloud iam service-accounts add-iam-policy-binding "$email" \
    --project="$PROJECT_ID" \
    --role=roles/iam.workloadIdentityUser \
    --member="$member"
  ok "$SA_DEPLOY ← roles/iam.workloadIdentityUser（仅 ${GITHUB_REPO}）"
}

# 6/7 部署门禁要的只读权限
#
# 这五条不是「顺手多给一点」。deploy.yml 的 isolation-before / isolation-after 两个作业
# 跑的是 infra/scripts/verify-isolation.sh，而那个脚本的判定口径是
# **「取不到 = 判定不了 = 当作失败」**。少一个 list 权限，隔离门禁就红，整条部署停住。
#
# 每一条都是 list/get 级别的**只读**角色，且都停在「元数据」这一层。
step_read_roles() {
  step "6/7 隔离门禁（verify-isolation.sh）要的只读权限"
  local member
  member="serviceAccount:$(deploy_sa_email)"

  # compute instances / addresses / firewall-rules list —— as-built §2 的 vpn-us / vpn-jp、
  # 两个保留 IP、10 条防火墙规则，全靠这三条 list 才能比对。
  # 其中防火墙那部分是**唯一**能发现「压制 default-allow-ssh 的 deny 规则被 disable 了」的地方。
  grant_project_role "compute 资源只读（实例 / 保留 IP / 防火墙规则）" \
    roles/compute.viewer "$member"

  # gcloud run services list —— 要比对的是 anthropic-relay / lisa-cloud / lisa-web
  # 三个**现有**服务的 URL 与修订版。setup-infra.sh 授的 run.developer 是**服务级、只在 bp-api 上**，
  # 列不出别人的服务，也就看不见「我们是不是碰了别人」。
  grant_project_role "Cloud Run 只读（列出现有三个服务）" \
    roles/run.viewer "$member"

  # gcloud secrets list + versions list —— 判定的是「anthropic-api-key / relay-token
  # 还在，且版本数没变」。要的只有**名字和版本号**。
  #
  # 🔴 这里授的是 viewer 不是 secretmanager.secretAccessor。deploy.md §1 第 2 条的禁令
  #    针对的正是后者：项目级 accessor 会让 CI 读到 anthropic-api-key / relay-token 的**值**，
  #    那是现有服务的凭据。viewer 看得到名字与版本数，看不到内容 —— 恰好是隔离检查要的那一层。
  grant_project_role "Secret Manager 只读元数据（**不是** secretAccessor）" \
    roles/secretmanager.viewer "$member"

  # artifacts repositories list + docker images list —— 「cloud-run-source-deploy 的镜像数
  # 不减少」这一条（as-built §2.1）。setup-infra.sh 授的 writer 是**仓库级、只在 bp-images 上**，
  # 读不到 cloud-run-source-deploy，而那个仓库正是要保护的对象。
  grant_project_role "Artifact Registry 只读（读得到 cloud-run-source-deploy 的镜像数）" \
    roles/artifactregistry.reader "$member"

  # gcloud iam service-accounts list —— as-built §5 的三个现有 SA 是否还在。
  #
  # ⚠️ 角色名 roles/iam.serviceAccountViewer **待核实**（本次未在真实 gcloud 上执行过）。
  #    若它在你的组织里不存在，用一个只含 iam.serviceAccounts.list / .get 的自定义角色顶上，
  #    **不要**退回 roles/viewer —— 项目级 viewer 会把现有服务的一切都读一遍，
  #    与本脚本每一条授权的取舍方向相反。
  grant_project_role "服务账号只读（列出 as-built §5 的三个现有 SA）" \
    roles/iam.serviceAccountViewer "$member"
}

# 7/7 部署本身要的权限 —— **只核对，不代授**
step_verify_deploy_roles() {
  step "7/7 核对 setup-infra.sh 拥有的三条部署权限（本脚本不代授）"
  local member email
  email="$(deploy_sa_email)"
  member="serviceAccount:${email}"

  if [ "$PROBE" -eq 0 ]; then
    warn "未鉴权，三条都跳过核对。真跑前请确认 setup-infra.sh 已经跑过。"
    return 0
  fi

  # 推镜像。仓库级，不是项目级 —— 项目级会一并覆盖 cloud-run-source-deploy（deploy.md §1 第 1 条）。
  report_binding "推镜像到 $AR_REPO" roles/artifactregistry.writer "$member" \
    gcloud artifacts repositories get-iam-policy "$AR_REPO" \
    --project="$PROJECT_ID" --location="$REGION"

  # 「以 bp-api-sa 的身份部署」。gcloud run deploy --service-account=bp-api-sa 要的就是它。
  # 注意它给的是「用这个 SA」，不是「变成这个 SA」—— CI 因此仍然读不到生产 secret。
  report_binding "以 $SA_API 身份部署" roles/iam.serviceAccountUser "$member" \
    gcloud iam service-accounts get-iam-policy \
    "${SA_API}@${PROJECT_ID}.iam.gserviceaccount.com" --project="$PROJECT_ID"

  # 建修订版 + 切流量。服务级，不是项目级 —— 项目级等于让 CI 能改现有三个服务。
  report_binding "部署 $RUN_SERVICE" roles/run.developer "$member" \
    gcloud run services get-iam-policy "$RUN_SERVICE" \
    --project="$PROJECT_ID" --region="$REGION"

  log ""
  log "  缺任何一条就跑（它是这三条的正主，幂等）："
  log "    ./infra/deploy/setup-infra.sh --step=iam"
  log "    ./infra/deploy/setup-infra.sh --step=registry"
  log "    ./infra/deploy/setup-infra.sh --step=postdeploy   # run.developer 在这一步"
  log ""
  log "  ⚠️ 服务级 run.developer 有一条真实的门槛：它只对**已存在**的服务成立。"
  log "     所以 CI 建不了新服务 —— 这正是 staging 那个 TODO 卡住的地方之一"
  log "     （deploy.yml 里 staging 的资源命名尚无裁决，见该文件的说明）。"
}

# ───────────────────────── 结果 ─────────────────────────

print_handoff() {
  local provider_value sa_value
  provider_value="$(wif_provider_value)"
  sa_value="$(deploy_sa_email)"

  step "填给 GitHub 的两个值（deploy.yml 顶部那四项 TODO 的前两项）"
  printf '\n' >&2
  printf '  vars.GCP_WIF_PROVIDER\n    %s\n\n' "$provider_value" >&2
  printf '  vars.GCP_DEPLOY_SA\n    %s\n\n' "$sa_value" >&2

  step "复制粘贴即可（要先 gh auth login，且对本仓库有 admin 权限）"
  printf '\n' >&2
  printf "  gh variable set GCP_WIF_PROVIDER --repo %s --body '%s'\n" \
    "$GITHUB_REPO" "$provider_value" >&2
  printf "  gh variable set GCP_DEPLOY_SA --repo %s --body '%s'\n" \
    "$GITHUB_REPO" "$sa_value" >&2
  printf '\n  # 核对（现在跑它应该是空的 —— 仓库当前 0 个 variable）\n' >&2
  printf '  gh variable list --repo %s\n' "$GITHUB_REPO" >&2
  printf '\n' >&2
  log "  说明："
  log "   · 这是**仓库级** variable，staging 与 prod 两个环境都读得到。"
  log "     若日后要给某个环境单独换一套，用 gh variable set --env <环境名>，它会覆盖仓库级。"
  log "   · 仓库现在 0 个 environment。deploy.yml 里的 environment: prod **不构成审批门禁** ——"
  log "     没有配保护规则的环境不会拦任何人。要审批就去仓库设置里给 prod 加 required reviewers。"
  log "   · 这两个值都**不是**凭据：provider 是资源路径，SA 是邮箱地址，公开也不构成风险。"
  log "     真正的边界是 attribute-condition 与 principalSet 那两层，它们在 GCP 侧。"
}

# ───────────────────────── 主流程 ─────────────────────────

main() {
  local arg
  for arg in "$@"; do
    case "$arg" in
      --apply)     DRY_RUN=0 ;;
      --dry-run)   DRY_RUN=1 ;;
      --yes)       ASSUME_YES=1 ;;
      --project=*) PROJECT_ID="${arg#*=}" ;;
      -h|--help)   usage; exit 0 ;;
      *)           usage >&2; die "未知参数：$arg" ;;
    esac
  done

  guard_project
  need_cmd gcloud
  guard_active_project

  log "项目 : $PROJECT_ID"
  log "仓库 : $GITHUB_REPO"
  log "条件 : $ATTR_CONDITION"
  if [ "$DRY_RUN" -eq 1 ]; then
    log "模式 : DRY-RUN（默认）—— 不发任何写操作。要真跑加 --apply"
  else
    log "模式 : \033[31mAPPLY\033[0m —— 会真的修改 GCP"
  fi
  check_auth
  resolve_project_number

  if [ "$DRY_RUN" -eq 0 ]; then
    log ""
    log "将要做的（每一步幂等，已存在就跳过）："
    log "  · 启用 sts / iamcredentials / iam 三个 API"
    log "  · 建 pool ${POOL} 与 provider ${PROVIDER}（condition 限定 ${GITHUB_REPO}）"
    log "  · 确认 $(deploy_sa_email) 存在"
    log "  · 授 roles/iam.workloadIdentityUser（principalSet 限定到 ${GITHUB_REPO}）"
    log "  · 授 5 个只读角色：compute.viewer / run.viewer / secretmanager.viewer /"
    log "    artifactregistry.reader / iam.serviceAccountViewer"
    log "  · 核对（不代授）setup-infra.sh 的三条部署权限"
    confirm "以上操作会修改共享项目 ${PROJECT_ID} 的 IAM。" "$CONFIRM_APPLY"
  fi

  step_apis
  step_pool
  step_provider
  step_sa
  step_wif_binding
  step_read_roles
  step_verify_deploy_roles
  print_handoff

  step "结果"
  log "  失败 $FAIL_N 项 / 提醒 $WARN_N 项"
  if [ "$DRY_RUN" -eq 1 ]; then
    log ""
    log "  ⚠️ 本次是 DRY-RUN，**GCP 上什么都没变**。上面打印的两个值在真跑之后才成立。"
    log "     真跑：./infra/scripts/setup-wif.sh --apply"
  fi
  if [ "$FAIL_N" -ne 0 ]; then
    log ""
    log "  🔴 有检查未通过（见上面的 ✗）。在改对之前不要配 GCP_WIF_PROVIDER。"
    exit 1
  fi
  exit 0
}

main "$@"

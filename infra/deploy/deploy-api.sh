#!/usr/bin/env bash
#
# deploy-api.sh —— 构建 bp-api 镜像并部署到 Cloud Run
#
# 事实源：
#   docs/04-ops/deploy.md §4（镜像）· §5（run deploy 逐参数）· §6（数据库连接）· §7（密钥）
#   docs/05-adr/0005-database-selection.md §6.2（连接数硬公式）· §10.3
#   api/internal/config/config.go（**环境变量名的唯一事实源**）
#   api/Dockerfile（构建参数 VERSION / 代理 ARG）
#
# 默认行为是**不接流量的候选修订版**（--no-traffic --tag=candidate）。
# 切流量要显式 --promote，且会要求二次确认。
#
# 🔴 本脚本刻意**没有**灰度选项。deploy.md §12.1：节点每 60 秒轮询一次，
#    10% 的流量切分意味着同一个节点在相邻两次轮询里可能拿到两个不同版本的响应，
#    改动 UniProxy 响应体或 ETag 计算方式时会造成节点反复失效缓存。
#    一律 0% → 验证 → 100%。
#
# 🔴 镜像 label 是「线上跑的到底是哪份源码」的**权威答案**（roadmap B41）。
#    这不是假想的风险，它已经发生过：生产 bp-api-2fbf49d 的 tag 来自
#    `git rev-parse --short=7 HEAD`，而那个 commit（2fbf49d3d2b6…）**不被任何分支引用**
#    —— pr7/p1-core-and-deploy 被 force-push 改写，它成了孤儿。
#    「线上跑的是哪份源码」当时只能靠去 GitHub 对象库捞完整 sha 才答得出来。
#    证据：docs/evidence/gcp-inventory-20260821/README.md §5.2。
#
#    所以本脚本把**完整 40 位 sha + 分支名 + 构建时间 + 工作树是否干净**写进镜像 label；
#    tag 仍是短 sha（人要读它），但**不再是唯一线索**。反查：infra/scripts/image-provenance.sh。

set -euo pipefail

# ───────────────────────── 防呆常量 ─────────────────────────

readonly EXPECTED_PROJECT_ID="oratis-491316"
readonly REGION="us-central1"
readonly SERVICE="bp-api"
readonly AR_REPO="bp-images"
readonly AR_HOST="us-central1-docker.pkg.dev"
readonly SA_API="bp-api-sa"
readonly SQL_INSTANCE="bp-db"

# ───────────────────────── 镜像来源 label（roadmap B41）─────────────────────────
#
# 前三个用 OCI 标准 key（org.opencontainers.image.*）—— docker inspect、crane 与各类
# 供应链工具都认得，不需要人先知道我们的私有约定；后三个是本项目自己的事实，
# 没有对应的 OCI key，走反向域名 plus.babel.*（域名是 babel.plus）。
#
# ⚠️ 这六个 key **同时**写在 infra/scripts/image-provenance.sh 里（反查那一侧）。
#    两处刻意重复，理由与六个脚本各自复制守卫代码相同：每个脚本要能单独拷出去跑。
#    改这里 = 改两处。
readonly LABEL_SHA="org.opencontainers.image.revision"      # 完整 40 位 sha
readonly LABEL_VERSION="org.opencontainers.image.version"   # 镜像 tag（短 sha，可能带 -dirty）
readonly LABEL_CREATED="org.opencontainers.image.created"   # 构建时间（RFC3339 UTC）
readonly LABEL_BRANCH="plus.babel.git.branch"               # 分支名；detached 时是 (detached)
readonly LABEL_DIRTY="plus.babel.git.dirty"                 # true / false
readonly LABEL_BUILDER="plus.babel.build.by"                # 哪条构建路径产的

# Secret 名 → 环境变量名。**必须与 setup-infra.sh 的 SECRET_* 和
# api/internal/config/config.go 的 required 表三方一致。**
readonly SECRET_MOUNTS="\
BP_DATABASE_URL=bp-database-url:latest,\
BP_SUBSCRIPTION_TOKEN_PEPPER=bp-sub-token-pepper:latest,\
BP_NODE_KEY_PEPPER=bp-node-token-pepper:latest,\
BP_SESSION_SIGNING_KEY=bp-jwt-signing-key:latest"

# ───────────────────────── 运行时开关 ─────────────────────────

PROJECT_ID="${PROJECT_ID:-$EXPECTED_PROJECT_ID}"
DRY_RUN=0
ASSUME_YES=0
DO_BUILD=1
DO_PROMOTE=0
# 默认用 Cloud Build 在 Google 的 amd64 机器上构建，理由见 build_and_push。
USE_CLOUD_BUILD=1
ALLOW_DIRTY=0
TAG=""

# 下面五个由 resolve_provenance() **一次性**填好，之后只读不改。
# 刻意不在构建过程中重新读 git：构建期间工作区若被改动，label 会与真正构建进去的内容不符，
# 而一句**可信但错误**的来源记录比没有记录更糟。
GIT_SHA=""          # 完整 40 位
GIT_BRANCH=""       # 分支名，或 (detached)
GIT_DIRTY=""        # true / false —— **工作区此刻**的事实
BUILD_TIME=""       # RFC3339 UTC
# IMAGE_DIRTY 是**将被部署的那个镜像**的事实。构建时它等于 GIT_DIRTY；
# --no-build 时工作区与镜像无关，只能从 tag 反推（<短sha> = 干净，<短sha>-dirty = 脏）。
IMAGE_DIRTY=""
STAMP_RUN_LABELS=1  # 是否把 sha 写进 Cloud Run 修订版 label（--no-build 换 tag 时会关掉）

# Cloud Build 的构建配置是运行时生成的临时文件，退出时删。
CB_CONFIG=""

# ───────────────────────── 通用工具（与 infra/ 下其它脚本刻意保持重复，见 setup-infra.sh 的说明）─────────────────────────

log()  { printf '%s\n' "$*" >&2; }
step() { printf '\n\033[1m▸ %s\033[0m\n' "$*" >&2; }
ok()   { printf '  ✓ %s\n' "$*" >&2; }
skip() { printf '  · %s\n' "$*" >&2; }
warn() { printf '  ⚠ %s\n' "$*" >&2; }
die()  { printf '\n\033[31m✗ %s\033[0m\n' "$*" >&2; exit 1; }

# qq 打印一个参数：只在需要时加单引号。
# 不用 printf '%q' —— 它会把中文转成八进制转义（\346\216\247…），而 dry-run 的输出是给人读的。
qq() {
  case "$1" in
    ''|*[!A-Za-z0-9_@%+=:,./~-]*)
      printf "'%s'" "$(printf '%s' "$1" | sed "s/'/'\\\\''/g")" ;;
    *) printf '%s' "$1" ;;
  esac
}


usage() {
  cat <<'EOF'
用法: deploy-api.sh [选项]

构建 bp-api 镜像 → 推 Artifact Registry → 部署到 Cloud Run。
默认部署成**不接流量**的候选修订版（--tag=candidate），验证通过后再 --promote。

选项:
  --tag=<sha>     镜像 tag。默认取 git rev-parse --short=7 HEAD
  --no-build      跳过构建，直接部署已在仓库里的该 tag（回补一次失败的部署时用）
  --promote       部署后把 100% 流量切到新修订版。会要求二次确认
  --allow-dirty   允许工作区有未提交改动时构建。tag 会变成 <短sha>-dirty，
                  且镜像 label 里 plus.babel.git.dirty=true（见下）
  --project=<id>  GCP 项目 ID。**必须是 oratis-491316**
  --dry-run       只打印将要执行的命令，不做任何写操作
  --yes           跳过交互确认
  -h, --help      显示本帮助

两段式发布（推荐）:
  ./infra/scripts/verify-isolation.sh                    # 1. 部署前基线
  ./infra/deploy/deploy-api.sh                           # 2. 起候选，不接流量
  curl -sS https://candidate---<服务URL>/-/healthz          # 3. 验证候选
  ./infra/deploy/deploy-api.sh --no-build --promote      # 4. 切 100%
  ./infra/scripts/verify-isolation.sh                    # 5. 部署后核对

回滚:
  ./infra/deploy/rollback.sh --list
  ./infra/deploy/rollback.sh --to=<修订版名>

镜像来源（roadmap B41 的处置）:
  每个镜像都带六个 label：完整 40 位 sha、tag、构建时间、分支名、工作树是否干净、构建路径。
  tag 只是给人读的短名，**不是**「线上跑的是哪份源码」的答案 ——
  短 sha 在分支被 force-push 之后会指向一个不被任何分支引用的孤儿 commit（已发生过）。

  反查线上修订版对应的完整 sha：
    ./infra/scripts/image-provenance.sh                 # 当前接 100% 流量的修订版
    ./infra/scripts/image-provenance.sh --revision=bp-api-<sha>
EOF
}

# 由 main 里的 trap cleanup EXIT 调用，shellcheck 看不出间接调用。
# 两个码都要留：0.9.0（CI 的 ubuntu-24.04 预装版）报 SC2317，SC2329 是 0.10.0 才引入的。
# shellcheck disable=SC2317,SC2329
cleanup() {
  if [ -n "$CB_CONFIG" ] && [ -f "$CB_CONFIG" ]; then
    rm -f "$CB_CONFIG"
  fi
}

run() {
  if [ "$DRY_RUN" -eq 1 ]; then
    local _a
    printf '  [dry-run] ' >&2
    for _a in "$@"; do qq "$_a" >&2; printf ' ' >&2; done
    printf '\n' >&2
    return 0
  fi
  "$@"
}

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
  printf '\n%s\n请输入 %s 确认（其它任何输入都会中止）：' "$prompt" "$expect" >&2
  read -r answer
  if [ "$answer" != "$expect" ]; then
    die "确认失败，已中止。"
  fi
}

guard_project() {
  if [ "$PROJECT_ID" != "$EXPECTED_PROJECT_ID" ]; then
    die "PROJECT_ID 必须是 $EXPECTED_PROJECT_ID，当前是 \"$PROJECT_ID\"。"
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

# ───────────────────────── 前置检查 ─────────────────────────
#
# fail-closed：任何一项缺失都在**构建之前**报错。
# bp-api 自己的 config 包也是 fail-closed（缺环境变量拒绝启动），
# 但那时镜像已经推上去、修订版已经建出来了 —— 在这里挡住更便宜。

preflight() {
  step "前置检查"

  if ! gcloud iam service-accounts describe "${SA_API}@${PROJECT_ID}.iam.gserviceaccount.com" \
         --project="$PROJECT_ID" >/dev/null 2>&1; then
    die "服务账号 ${SA_API} 不存在。先跑 ./infra/deploy/setup-infra.sh --step=iam。
     🔴 绝不要退而求其次用 Compute 默认 SA（as-built §5：权限过大且被现有工作负载共用）。"
  fi
  ok "服务账号 $SA_API 存在"

  if ! gcloud sql instances describe "$SQL_INSTANCE" --project="$PROJECT_ID" >/dev/null 2>&1; then
    die "Cloud SQL 实例 ${SQL_INSTANCE} 不存在。先跑 ./infra/deploy/setup-infra.sh --step=sql。"
  fi
  ok "Cloud SQL 实例 $SQL_INSTANCE 存在"

  if ! gcloud artifacts repositories describe "$AR_REPO" \
         --project="$PROJECT_ID" --location="$REGION" >/dev/null 2>&1; then
    die "Artifact Registry 仓库 ${AR_REPO} 不存在。先跑 ./infra/deploy/setup-infra.sh --step=registry。
     🔴 不要改用 cloud-run-source-deploy —— 那是现有三个服务的镜像仓库（deploy.md §1 第 1 条）。"
  fi
  ok "Artifact Registry 仓库 $AR_REPO 存在"

  # 四个 secret 缺一个，实例就会因为 config 的 fail-closed 起不来。
  local pair name
  while IFS= read -r pair; do
    [ -n "$pair" ] || continue
    name="${pair#*=}"      # bp-xxx:latest
    name="${name%%:*}"     # bp-xxx
    guard_bp_prefix "$name"
    if ! gcloud secrets describe "$name" --project="$PROJECT_ID" >/dev/null 2>&1; then
      die "secret ${name} 不存在。先跑 ./infra/deploy/setup-infra.sh --step=secrets（以及 --step=sql）。"
    fi
  done <<EOF
$(printf '%s' "$SECRET_MOUNTS" | tr ',' '\n')
EOF
  ok "4 个 secret 均存在"
}

# ───────────────────────── 构建 ─────────────────────────

# resolve_provenance 解析四件来源事实并定下 tag。**构建之前**跑，之后不再读 git。
#
# 🔴 与旧版 resolve_tag 的两处区别，都是 B41 的直接后果：
#   1. 即使显式给了 --tag=，也照样解析来源事实 —— tag 是名字，来源是事实，两者不是一回事。
#   2. dirty 构建**换一个 tag**（<短sha>-dirty），而不是沿用短 sha。
#
# 🔴 但这两条都**只在真的要构建时**才成立（DO_BUILD=1）。--no-build 那条路径上
#    没有任何东西被构建，工作区脏不脏与将被部署的那个镜像的内容**无关** ——
#    在那里拦一道等于把「两段式发布的第 4 步」和「回补一次失败的部署」都堵死，
#    而这两件事恰恰常常发生在工作区不干净的时候（正在改、正在查）。
#    2026-08-23 实测：旧版 resolve_tag 在给了 --tag= 时直接 return，不会拦；
#    早先的 resolve_provenance 去掉了那个 early return，于是
#    `deploy-api.sh --no-build --tag=<sha> --promote`（README §3.2 第 4 步）在脏树上必死。
resolve_provenance() {
  need_cmd git
  if ! git -C "$ROOT" rev-parse --git-dir >/dev/null 2>&1; then
    die "不在 git 仓库里，解析不出镜像来源（完整 sha / 分支 / 工作树状态）。
     没有这些 label 就不要构建 —— 那正是 B41 里「线上跑的是哪份源码答不出来」的起点。
     只想部署一个**已经存在**的镜像时用 --no-build --tag=<已有tag>。"
  fi

  GIT_SHA="$(git -C "$ROOT" rev-parse HEAD)"
  # detached HEAD 是常态（CI 的 checkout 就是），不是错误 —— 但要显式记下来，
  # 因为「分支名」在那种情况下是不存在的东西，不能编一个。
  GIT_BRANCH="$(git -C "$ROOT" symbolic-ref --quiet --short HEAD || printf '(detached)')"
  BUILD_TIME="$(date -u '+%Y-%m-%dT%H:%M:%SZ')"

  if [ -n "$(git -C "$ROOT" status --porcelain)" ]; then
    GIT_DIRTY=true
  else
    GIT_DIRTY=false
  fi

  local short="${GIT_SHA:0:7}"

  if [ "$DO_BUILD" -eq 1 ]; then
    # 构建时「镜像的 dirty」与「工作区的 dirty」是同一件事。
    IMAGE_DIRTY="$GIT_DIRTY"

    # 🔴 默认**拒绝**带脏工作区构建，而不是「标个 dirty 就放行」。
    #    理由：label 只能记下 HEAD 的 sha，但真正被构建进镜像的是**工作区**。
    #    工作区一脏，label 里那个 sha 就是一句可信但错误的话 —— 而这比没有 label 更糟：
    #    没有 label 时人会去查，有一个错的 label 时人会直接信。
    #    dirty=true 这个 label 的作用是**给已经明知故犯的那次留证据**，不是许可证。
    if [ "$GIT_DIRTY" = true ] && [ "$ALLOW_DIRTY" -eq 0 ]; then
      die "工作区有未提交改动，拒绝**构建**。
     镜像 label 会写 ${LABEL_SHA}=${GIT_SHA}，而那个 commit **不等于**将被构建进去的内容。
     修订版还会被命名成 ${SERVICE}-${TAG:-$short}，看起来与那个 commit 一一对应。
     先提交（推荐），或显式加 --allow-dirty ——
     那会把 tag 改成 ${short}-dirty 并在 label 里写 ${LABEL_DIRTY}=true。
     ⚠️ 只想部署一个**已经存在**的镜像（两段式发布的第 4 步、回补一次失败的部署）时
        用 --no-build —— 那条路径不构建任何东西，不受本检查约束。"
    fi

    if [ -z "$TAG" ]; then
      TAG="$short"
      if [ "$GIT_DIRTY" = true ]; then
        # dirty 构建**必须换个 tag**：否则它会在 Artifact Registry 里把同一个短 sha 下
        # 那份干净镜像顶掉（tag 会移动到新 digest），被顶掉的那份就只剩 digest 可寻 ——
        # 又回到 B41 那种「答不出来」。
        TAG="${short}-dirty"
        warn "--allow-dirty：tag 取 ${TAG}，label ${LABEL_DIRTY}=true。
     ⚠️ 这个镜像与仓库里任何一个 commit 都**不**对应，只能用于救火，不要 --promote 到生产。"
      fi
    fi
  else
    # ── --no-build：不构建，只部署一个已经在仓库里的镜像 ──
    #
    # 这里**不看工作区脏不脏**，理由见函数头。tag 也**不加 -dirty 后缀**：
    # 加了就会去指一个多半根本不存在的镜像（干净构建推的是 <短sha>，不是 <短sha>-dirty），
    # 于是脚本自己建议的那条出路会把人送进 "image not found"。
    [ -n "$TAG" ] || TAG="$short"

    # 修订版 label 要描述的是**那个镜像**，不是此刻的工作区。
    # 镜像与 commit 的对应关系只能从 tag 反推：<短sha> = 干净构建，<短sha>-dirty = 脏构建，
    # 其它 tag 一律推不出来 → 干脆不写（写了就是撒谎）。
    case "$TAG" in
      "$short")          IMAGE_DIRTY=false ;;
      "${short}-dirty")  IMAGE_DIRTY=true  ;;
      *)                 STAMP_RUN_LABELS=0 ;;
    esac
  fi
}

# label_pairs 打印本次构建要写进镜像的 label，每行一个 k=v。
# **两条构建路径都从这里取**，所以 docker 与 Cloud Build 产出的 label 不会漂移。
label_pairs() {
  printf '%s=%s\n' "$LABEL_SHA"     "$GIT_SHA"
  printf '%s=%s\n' "$LABEL_VERSION" "$TAG"
  printf '%s=%s\n' "$LABEL_CREATED" "$BUILD_TIME"
  printf '%s=%s\n' "$LABEL_BRANCH"  "$GIT_BRANCH"
  printf '%s=%s\n' "$LABEL_DIRTY"   "$GIT_DIRTY"
  printf '%s=%s\n' "$LABEL_BUILDER" "$1"
}

# assert_substitutable —— gcloud 的 --substitutions 是逗号分隔的 k=v 串。
# 值里出现逗号或换行时**拒绝构建**，而不是想办法转义：这里宁可挡住一次合法的构建，
# 也不要产出一个 label 内容被切坏的镜像 —— 那正是 B41 想根除的「可信但错误的来源记录」。
assert_substitutable() {
  case "$2" in
    *,*|*$'\n'*)
      die "$1 的值里有逗号或换行（$2），没法安全地经 gcloud --substitutions 传进 Cloud Build。
     改用 --local 走本机 docker 构建（那条路径的 --label 不受此限），或者把分支名改掉。" ;;
  esac
}

# write_cloudbuild_config 生成一份**不含任何插值**的 Cloud Build 配置，写到临时文件。
#
# 🔴 为什么不能继续用 `gcloud builds submit --tag=<镜像>`：那条捷径是 gcloud 自己拼出来的
#    固定构建，**没有任何形式能传 --label 或 --build-arg**。而 B41 要的四项来源事实
#    只能靠 label 落进镜像 —— 所以这里必须换成显式的构建配置。
#
# 配置里的动态值全部是 Cloud Build 的替换变量（${_XXX}），真实值走 --substitutions 传。
# 这样分支名之类的外部字符串永远不会进入 YAML 结构，也就不可能改变配置的形状。
#
# **待核实**：本配置尚未在 Cloud Build 上真跑过（本次任务不执行任何 gcloud 变更）。
write_cloudbuild_config() {
  CB_CONFIG="$(mktemp "${TMPDIR:-/tmp}/bp-cloudbuild.XXXXXX")"
  cat > "$CB_CONFIG" <<'EOF'
# 由 infra/deploy/deploy-api.sh 运行时生成，勿手工编辑（改脚本里的 write_cloudbuild_config）。
steps:
  - name: gcr.io/cloud-builders/docker
    args:
      - build
      - --build-arg=VERSION=${_VERSION}
      - --label=org.opencontainers.image.revision=${_GIT_SHA}
      - --label=org.opencontainers.image.version=${_VERSION}
      - --label=org.opencontainers.image.created=${_BUILD_TIME}
      - --label=plus.babel.git.branch=${_GIT_BRANCH}
      - --label=plus.babel.git.dirty=${_GIT_DIRTY}
      - --label=plus.babel.build.by=${_BUILD_BY}
      - --tag=${_IMAGE}
      - .
images:
  - ${_IMAGE}
EOF

  # 自检：配置模板里的 label key 必须与本脚本顶部的常量逐个对上。
  # 这两处必然重复（YAML 里不能插值，否则就不是「不含插值」了），所以用断言把它们锁在一起 ——
  # 漂移会在这里以一次响亮的失败暴露，而不是变成一个少了某个 label 的镜像。
  local key
  for key in "$LABEL_SHA" "$LABEL_VERSION" "$LABEL_CREATED" \
             "$LABEL_BRANCH" "$LABEL_DIRTY" "$LABEL_BUILDER"; do
    if ! grep -q -- "--label=${key}=" "$CB_CONFIG"; then
      die "Cloud Build 配置模板里缺 label ${key}。
     模板（write_cloudbuild_config）与脚本顶部的 LABEL_* 常量漂移了，两处都要改。"
    fi
  done

  if [ "$DRY_RUN" -eq 1 ]; then
    log "  [dry-run] 生成的 Cloud Build 配置（$CB_CONFIG）："
    sed 's/^/    | /' "$CB_CONFIG" >&2
  fi
}

build_and_push() {
  step "构建镜像 $IMAGE"

  # ── 默认走 Cloud Build，而不是本机 docker build ──
  #
  # 与 infra/migrate/build-and-run.sh 保持一致。本机构建在这台开发机上有三重障碍，
  # 2026-08-17 逐个踩过：
  #  1. 本机是 Apple Silicon（arm64），Cloud Run 只接受 amd64/linux；
  #  2. 加 --platform 会强制去 registry 拉对应架构的基础镜像，而 Docker Hub 在这台机器上
  #     不可达（auth.docker.io 返回 EOF），且 docker pull --platform 在 Docker Desktop 上
  #     实测仍落回 arm64；
  #  3. Docker daemon 本身未必在跑 —— 会话重启后就遇到过
  #     `Cannot connect to the Docker daemon`，部署直接中断。
  #
  # Cloud Build 一次解决三个：跑在 amd64、直连 Docker Hub、不依赖本机 daemon。
  # 代价是每次上传构建上下文（api/ 目录）与 Cloud Build 用量（免费额度 120 分钟/天）。
  # --local 保留给环境正常的机器。
  if [ "$USE_CLOUD_BUILD" -eq 1 ]; then
    need_cmd gcloud
    write_cloudbuild_config

    # 每个值都要过一遍逗号/换行检查：gcloud 的 --substitutions 用逗号分隔 k=v，
    # 值里带逗号会被切错 —— 而切错的结果是一个**内容错误的 label**，不是一次失败。
    local subs
    assert_substitutable "镜像引用"   "$IMAGE"
    assert_substitutable "tag"        "$TAG"
    assert_substitutable "完整 sha"   "$GIT_SHA"
    assert_substitutable "构建时间"   "$BUILD_TIME"
    assert_substitutable "分支名"     "$GIT_BRANCH"
    subs="_IMAGE=${IMAGE},_VERSION=${TAG},_GIT_SHA=${GIT_SHA}"
    subs="${subs},_BUILD_TIME=${BUILD_TIME},_GIT_BRANCH=${GIT_BRANCH}"
    subs="${subs},_GIT_DIRTY=${GIT_DIRTY},_BUILD_BY=deploy-api.sh/cloud-build"

    run gcloud builds submit "$ROOT/api" \
      --project="$PROJECT_ID" \
      --config="$CB_CONFIG" \
      --substitutions="$subs" \
      --timeout=15m
    ok "镜像已构建并推送（Cloud Build），label 见上面的配置"
    return 0
  fi

  need_cmd docker

  # --platform=linux/amd64 不是可选的：在 Apple Silicon 上不写这个会推上去一个
  # arm64 镜像，Cloud Run 拉起来直接 exec format error（deploy.md §4.2）。
  #
  # 代理三连：Docker Desktop 会往构建容器注入代理变量，且注入的端口可能与宿主机
  # 实际代理不一致，导致 go mod download 在构建容器里失败。api/Dockerfile 把它们
  # 声明成 ARG 就是为了让这里能显式清空（api/Makefile 的 docker-build 同样处理）。
  # label 与 Cloud Build 路径同源（label_pairs），两条路径产出的镜像带的 label 一致。
  local -a label_args=()
  local pair
  while IFS= read -r pair; do
    [ -n "$pair" ] || continue
    label_args+=(--label "$pair")
  done <<EOF
$(label_pairs "deploy-api.sh/local-docker")
EOF

  run docker build \
    --platform=linux/amd64 \
    --build-arg "VERSION=${TAG}" \
    --build-arg HTTP_PROXY= --build-arg HTTPS_PROXY= \
    --build-arg http_proxy= --build-arg https_proxy= \
    --build-arg 'GOPROXY=https://goproxy.cn,https://proxy.golang.org,direct' \
    "${label_args[@]}" \
    -t "$IMAGE" \
    "$ROOT/api"
  ok "镜像已构建"

  # 推送需要 docker 认得 Artifact Registry 的凭据助手。
  # 本脚本**不替你改 $HOME/.docker/config.json** —— 那是本机配置，应当由你自己决定。
  if ! grep -q "$AR_HOST" "${HOME}/.docker/config.json" 2>/dev/null; then
    warn "${HOME}/.docker/config.json 里没看到 ${AR_HOST} 的凭据助手。
     若推送报 denied，先手工跑一次（它会修改你本机的 docker 配置）：
       gcloud auth configure-docker ${AR_HOST}"
  fi

  run docker push "$IMAGE"
  ok "镜像已推送"
}

# ───────────────────────── 部署 ─────────────────────────

# promote_epilogue —— 切完流量之后要说的话。deploy() 的两条 promote 路径共用。
promote_epilogue() {
  step "部署完成"
  local url
  url="$(gcloud run services describe "$SERVICE" --project="$PROJECT_ID" --region="$REGION" \
          --format='value(status.url)' 2>/dev/null || true)"
  if [ -n "$url" ]; then
    log "  服务 URL      : $url"
    log "  验证          : curl -sS ${url}/-/healthz   # 确认 revision 是 ${TAG}"
    log "  就绪（查库）  : curl -sS ${url}/readyz"
  fi
  log ""
  log "  ⚠️ 现在跑一次 ./infra/scripts/verify-isolation.sh —— 这是「不影响已部署服务」这条承诺的可执行形式。"
  log "  ⚠️ /-/healthz **不查数据库**（控制面故障不得升级为数据面故障）；要确认库连得上看 /readyz。"
}

deploy() {
  step "部署 $SERVICE（修订版 ${SERVICE}-${TAG}）"

  local conn="${PROJECT_ID}:${REGION}:${SQL_INSTANCE}"

  # 环境变量。⚠️ --set-env-vars 与 --set-secrets 都是**全量替换**语义
  # （deploy.md §7 末尾）：漏写一项就是静默删除一项，所以这里每次列全。
  # 只想改一项时用 --update-env-vars / --update-secrets，不要改这里。
  #
  # PORT 不在列表里 —— Cloud Run 自己注入，手工设会冲突。
  # BP_ALLOWED_ORIGINS：CORS 白名单，**prod 下 config 是 fail-closed 的**——
  # 不设这一项，容器会拒绝启动（config.parseAllowedOrigins）。
  # 2026-08-17 首次部署前发现漏了它：部署会「成功」，但修订版永远起不来。
  #
  # 值取自 ALLOWED_ORIGINS 环境变量，默认是 system-design §4.1 规划的 Web 域名。
  # ⚠️ 这些域名**目前还不存在**（web 尚未部署），所以现在填的是「将来会是什么」而不是
  #    「现在是什么」。真实域名池定下来后必须回来改 —— 域名池是会轮换的，
  #    每加一个镜像域名都要同步进这里，否则那个域名下的面板调不通 API。
  local origins="${ALLOWED_ORIGINS:-https://web.babel.plus,https://admin.babel.plus}"

  # ⚠️ 用 gcloud 的**自定义分隔符**语法 `^;;^k=v;;k=v`，不能用默认的逗号。
  # BP_ALLOWED_ORIGINS 的值本身就含逗号（多个 Origin），用默认分隔符会被切错：
  #   ERROR: argument --set-env-vars: Bad syntax for dict arg: [https://admin.babel.plus]
  # 2026-08-17 首次部署实测踩到。
  #
  # 🔴 分隔符曾经是 `@`，理由写的是「它不会出现在 URL 与我们的任何值里」——
  #    那句话在 BP_INTERNAL_TASK_CALLERS 出现之前是对的，之后就不再成立了：
  #    它的值是**服务账号 email**，形如 bp-tasks-sa@oratis-491316.iam.gserviceaccount.com，
  #    正中分隔符。2026-08-30 实测踩到：
  #      ERROR: Bad syntax for dict arg: [oratis-491316.iam.gserviceaccount.com]
  #    失败发生在 gcloud 参数解析阶段，**没有产生任何修订版**，所以它只是一次响亮的失败。
  #    改用 `;;`：分号不出现在 email 的本地部分与域名里，也不出现在我们下发的任何 URL 里，
  #    而**两个连写**的分号进一步把「某个值里碰巧有一个分号」也排除掉了。
  #    ⚠️ 今后往这张表里加变量时，先问一句新值里可不可能出现 `;;`。
  local env_vars="^;;^\
BP_ENV=prod;;\
BP_GCP_PROJECT_ID=${PROJECT_ID};;\
BP_DB_MAX_CONNS=2;;\
BP_LOG_LEVEL=info;;\
BP_TRUST_PROXY_HEADERS=true;;\
BP_ALLOWED_ORIGINS=${origins}"

  # ---- 内部面（/internal/tasks/*，9 条 Cloud Scheduler 打进来的端点）----
  #
  # 两项**要么都给、要么都不给** —— config.Load 会在只给一项时直接拒绝启动。
  # 理由见 config.go：只配一项时内部面仍然整体拒绝（与没配无异），
  # 但配置者会以为自己已经打开了它，于是去查 Scheduler、查 IAM、查 OIDC，唯独不回来看环境变量。
  #
  # 不给默认值：内部面关闭是一个**安全的**默认，而一个猜出来的 audience 只会让
  # 「配了但永不匹配」冒充「没配」。留空时这两行不进 --set-env-vars，行为与从前逐字一致。
  if [ -n "${BP_INTERNAL_OIDC_AUDIENCE:-}" ] || [ -n "${BP_INTERNAL_TASK_CALLERS:-}" ]; then
    if [ -z "${BP_INTERNAL_OIDC_AUDIENCE:-}" ] || [ -z "${BP_INTERNAL_TASK_CALLERS:-}" ]; then
      die "BP_INTERNAL_OIDC_AUDIENCE 与 BP_INTERNAL_TASK_CALLERS 必须同时给或同时留空。
     只给一项时内部面仍然整体拒绝（与没配无异），但排查方向会被带偏 —— 见 config.go 的同名断言。"
    fi
    env_vars="${env_vars};;BP_INTERNAL_OIDC_AUDIENCE=${BP_INTERNAL_OIDC_AUDIENCE}"
    env_vars="${env_vars};;BP_INTERNAL_TASK_CALLERS=${BP_INTERNAL_TASK_CALLERS}"
  fi

  # ---- 管理面（/admin/*）----
  #
  # 同一条纪律的另一半。⚠️ BP_ADMIN_IAP_AUDIENCE 的取值**只能**来自一个挂了 IAP 的
  # GCLB 后端服务的数字 id，不是 URL、不是项目 ID —— 那套负载均衡器目前一件都没建
  # （roadmap B51），所以这两项现在必然留空，管理面在线上整体拒绝。**这是设计，不是故障。**
  if [ -n "${BP_ADMIN_IAP_AUDIENCE:-}" ] || [ -n "${BP_ADMIN_TOTP_ENC_KEY:-}" ]; then
    if [ -z "${BP_ADMIN_IAP_AUDIENCE:-}" ] || [ -z "${BP_ADMIN_TOTP_ENC_KEY:-}" ]; then
      die "BP_ADMIN_IAP_AUDIENCE 与 BP_ADMIN_TOTP_ENC_KEY 必须同时给或同时留空。
     只给 audience → 管理面能进但所有危险操作被拒；只给密钥 → 管理面整体进不去。两种现象都不指向配置本身。"
    fi
    env_vars="${env_vars};;BP_ADMIN_IAP_AUDIENCE=${BP_ADMIN_IAP_AUDIENCE}"
    env_vars="${env_vars};;BP_ADMIN_TOTP_ENC_KEY=${BP_ADMIN_TOTP_ENC_KEY}"
  fi

  local -a args=(
    gcloud run deploy "$SERVICE"
    --project="$PROJECT_ID"
    --region="$REGION"
    --image="$IMAGE"
    --revision-suffix="$TAG"

    # 运行时身份。🔴 绝不用 Compute 默认 SA（as-built §5）。
    --service-account="${SA_API}@${PROJECT_ID}.iam.gserviceaccount.com"

    # 🔴 Cloud SQL 走 Cloud Run **内建连接器**注入的 Unix socket：
    #    连接自动加密、走 Google 内部网络、不需要给数据库开任何 authorized network、
    #    **不碰 default 网络**（default 正是 vpn-us / vpn-jp 所在的网络）、成本 $0。
    #    对应地，下面**没有** --vpc-connector / --network / --subnet，且必须继续没有。
    --add-cloudsql-instances="$conn"

    # 鉴权在应用层（节点面 / 用户面 / 客户端面 / 管理面 / 内部面五条互不共享的中间件链）。
    # 节点、代理客户端、Cloud Tasks 都是**公网**调用方，IAM 级鉴权在这里不适用。
    --allow-unauthenticated
    --ingress=all

    # 🔴 --max-instances=8 是本命令里唯一一个**属于数据库而不属于 Cloud Run** 的参数。
    #    硬公式（ADR 0005 §6.2）：max_instances × pool_max + 运维预留 ≤ max_connections − 3
    #    代入 db-f1-micro：8 × 2 + 6 = 22 ≤ 25 − 3 ✓
    #    改它必须同时改 BP_DB_MAX_CONNS，并重算这条公式。
    #    缺省更糟：Cloud Run 默认上限 100 × 2 = 200 连接 → FATAL: sorry, too many clients already。
    --max-instances=8

    # min-instances=0：节点每 60 秒轮询本身就是保温器（2 节点 = 每 7.5 秒一个请求），
    # 而 min-instances=1 = 2,628,000 vCPU-s/月，是免费额度的 14.6 倍。
    # 复审触发条件：startup_latencies P95 > 3 秒且冷启动落在用户请求上（deploy.md §5.2）。
    --min-instances=0

    # 并发 80 是 Cloud Run 默认值，沿用。我们的请求以「一次主键查 + 返 304」为主。
    # ⚠️ 调**低**它等于变相调**高**实例数，会直接撞上面那条连接数天花板。
    --concurrency=80

    # 1 vCPU 是能拿到 request-based 计费的最小完整核；512Mi 对「Go 静态二进制 + 2 连接池」很宽裕。
    --cpu=1
    --memory=512Mi

    # 必须 ≥ 最慢的 /internal/tasks/*。不要拉到 3600 —— 实例总数只有 8，
    # 一个卡住的请求就吃掉 12.5% 的容量。真有超过 120 秒的聚合就拆成 Cloud Run Job。
    --timeout=120

    # 启动期给额外 CPU 缩短冷启动。计费细节 **待核实**，先开着等基线数据。
    --cpu-boost

    --set-env-vars="$env_vars"
    --set-secrets="$SECRET_MOUNTS"
  )

  # 修订版 label：让「这个修订版是哪个 commit」变成**一条 gcloud 就能查**的事实，
  # 不必先把镜像拉下来看 label。镜像 label 仍是权威记录，这里是快捷方式。
  #
  # ⚠️ Cloud Run 的 label 值只允许小写字母 / 数字 / - / _，最长 63 位 ——
  #    40 位 hex 合法，**分支名不合法**（含 /），所以分支只写在镜像 label 里，不写这里。
  # ⚠️ 用 --update-labels（合并）而不是 --labels（全量替换）：线上服务可能带着别人
  #    加的 label，全量替换会静默抹掉它们（与 --set-env-vars 同一类陷阱）。
  # **待核实**：--update-labels 是否会落到**新建的修订版**上（gcloud 文档未逐字复核）。
  #    即便不落，image-provenance.sh 会退回去读镜像 label，答案不会因此丢失。
  if [ "$STAMP_RUN_LABELS" -eq 1 ]; then
    args+=(--update-labels="bp-git-sha=${GIT_SHA},bp-git-dirty=${IMAGE_DIRTY}")
  else
    warn "--no-build 且 --tag=${TAG} 与当前 HEAD 无关：本次**不**往修订版写 bp-git-sha。
     写了就是撒谎 —— 那个镜像不是从当前工作树构建的。
     要知道它的来源：./infra/scripts/image-provenance.sh --image=${IMAGE}"
  fi

  # 🔴 下列参数**没有出现在上面，且必须继续没有**（deploy.md §1）：
  #    --source            会自动复用 cloud-run-source-deploy（现有三个服务的镜像仓库）
  #    --vpc-connector / --network / --subnet   会碰 default 网络（vpn-us / vpn-jp 所在）
  #    --no-cpu-throttling 把计费从 request-based 切到 instance-based，成本模型当场作废

  if [ "$DO_PROMOTE" -eq 1 ]; then
    confirm "将把 100% 流量切到修订版 ${SERVICE}-${TAG}。
  当前修订版会立刻停止接收新请求。
  ⚠️ 若本次发布改了 DB schema，请记住：**代码能秒级回滚，schema 不能**（deploy.md §12.3）。" "promote"

    # 🔴 两段式发布的第 4 步（README §3.2）是 `--no-build --tag=<sha> --promote`，
    #    而那个 sha 的修订版**在第 2 步就已经建出来了**。再发一次 `gcloud run deploy`
    #    会带着同一个 --revision-suffix 去建同名修订版 —— README §1 自己写着
    #    「同一 commit 重复部署会因修订版重名失败」。也就是说，文档里的发布流程
    #    第 4 步会撞上文档里第 1 节记的那条限制。
    #
    #    「把流量切到一个**已经存在**的修订版」本来就不该走 run deploy。
    #    rollback.sh 与 .github/workflows/deploy.yml 都已经用 update-traffic 了，
    #    这里对齐它们：修订版已存在 → update-traffic；不存在 → 照旧 run deploy。
    #    存在性检查是只读的，查不到就退回原路径，所以这条改动不会让任何原本能跑的情况变坏。
    #
    #    **待核实**：未在真实 gcloud 上跑过（本次任务不执行任何 gcloud 变更）。
    if gcloud run revisions describe "${SERVICE}-${TAG}" \
         --project="$PROJECT_ID" --region="$REGION" >/dev/null 2>&1; then
      ok "修订版 ${SERVICE}-${TAG} 已存在 —— 只切流量，不重建（避免修订版重名失败）"
      run gcloud run services update-traffic "$SERVICE" \
        --project="$PROJECT_ID" --region="$REGION" \
        --to-revisions="${SERVICE}-${TAG}=100"
      promote_epilogue
      return 0
    fi
    ok "流量：新修订版 100%（${SERVICE}-${TAG} 尚不存在，走 run deploy 建它）"
  elif gcloud run services describe "$SERVICE" \
         --project="$PROJECT_ID" --region="$REGION" >/dev/null 2>&1; then
    args+=(--no-traffic --tag=candidate)
    ok "流量：0%（候选修订版，tag=candidate）"
  else
    # ⚠️ **创建新服务时 gcloud 不接受 --no-traffic**：
    #   ERROR: --no-traffic not supported when creating a new service.
    # 2026-08-17 首次部署实测踩到。
    #
    # 这个限制是合理的：候选修订版策略的意义是「新版本先不接流量，
    # 验证通过再切」，而首次部署根本没有旧修订版可保护 ——
    # 不接流量就等于服务对外完全不可用，没有任何东西被保护。
    #
    # 所以首次部署直接接 100% 流量。**后续部署会自动走上面的候选分支**，
    # 因为那时 describe 就能查到服务了。
    warn "服务 ${SERVICE} 尚不存在 —— 这是首次部署，直接接 100% 流量。
     （--no-traffic 在创建新服务时不被支持，且首次部署没有旧修订版需要保护。
       下次部署会自动变回不接流量的候选修订版。）"
    ok "流量：新服务 100%"
  fi

  run "${args[@]}"

  step "部署完成"
  local url
  url="$(gcloud run services describe "$SERVICE" --project="$PROJECT_ID" --region="$REGION" \
          --format='value(status.url)' 2>/dev/null || true)"
  if [ -n "$url" ]; then
    log "  服务 URL      : $url"
    if [ "$DO_PROMOTE" -eq 1 ]; then
      log "  验证          : curl -sS ${url}/-/healthz   # 确认 revision 是 ${TAG}"
      log "  就绪（查库）  : curl -sS ${url}/readyz"
    else
      # 候选修订版的可访问地址是 https://<tag>---<服务主机名>
      log "  候选 URL      : https://candidate---${url#https://}"
      log "  验证后切流量  : ./infra/deploy/deploy-api.sh --no-build --tag=${TAG} --promote"
    fi
  fi
  log ""
  log "  ⚠️ 现在跑一次 ./infra/scripts/verify-isolation.sh —— 这是「不影响已部署服务」这条承诺的可执行形式。"
  log "  ⚠️ /-/healthz **不查数据库**（控制面故障不得升级为数据面故障）；要确认库连得上看 /readyz。"
}

# ───────────────────────── 主流程 ─────────────────────────

main() {
  local arg
  for arg in "$@"; do
    case "$arg" in
      --tag=*)     TAG="${arg#*=}" ;;
      --local)     USE_CLOUD_BUILD=0 ;;
      --no-build)  DO_BUILD=0 ;;
      --promote)   DO_PROMOTE=1 ;;
      --allow-dirty) ALLOW_DIRTY=1 ;;
      --project=*) PROJECT_ID="${arg#*=}" ;;
      --dry-run)   DRY_RUN=1 ;;
      --yes)       ASSUME_YES=1 ;;
      -h|--help)   usage; exit 0 ;;
      *)           usage >&2; die "未知参数：$arg" ;;
    esac
  done

  guard_project
  guard_bp_prefix "$SERVICE" "$AR_REPO" "$SA_API" "$SQL_INSTANCE"
  need_cmd gcloud

  ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
  trap cleanup EXIT
  resolve_provenance
  IMAGE="${AR_HOST}/${PROJECT_ID}/${AR_REPO}/${SERVICE}:${TAG}"

  log "项目   : $PROJECT_ID"
  log "服务   : $SERVICE @ $REGION"
  log "镜像   : $IMAGE"
  # 这四行就是 B41 的答案本身，所以它们**每次都打**，不藏在 --verbose 后面。
  log "commit : $GIT_SHA"
  log "分支   : $GIT_BRANCH"
  log "工作树 : dirty=$GIT_DIRTY"
  log "构建时 : $BUILD_TIME"
  if [ "$DO_BUILD" -eq 0 ]; then
    # --no-build 时上面四行描述的是**此刻的工作区**，不是将被部署的那个镜像。
    # 镜像的来源要去读它自己的 label（image-provenance.sh），别把这四行当成它的答案。
    log "         ↑ 这四行是**工作区**的事实；--no-build 不构建任何东西，"
    log "           将被部署的镜像 ${IMAGE:-<待定>} 的来源以它自己的 label 为准："
    log "           ./infra/scripts/image-provenance.sh --image=<镜像引用>"
  fi
  if [ "$DRY_RUN" -eq 1 ]; then
    log "模式   : DRY-RUN（只打印写操作）"
  fi

  preflight
  if [ "$DO_BUILD" -eq 1 ]; then
    build_and_push
  else
    skip "--no-build：跳过构建，直接部署 $IMAGE"
  fi
  deploy
}

ROOT=""
IMAGE=""

main "$@"

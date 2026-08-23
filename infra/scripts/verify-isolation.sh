#!/usr/bin/env bash
#
# verify-isolation.sh —— 「不影响已经部署的服务」这条承诺的**可执行形式**
#
# 事实源：docs/02-architecture/as-built-gcp.md §2（vpn-* 节点）· §3（10 条防火墙规则）
#        · §4（三个 Cloud Run 服务与 cloud-run-source-deploy）· §5（secret 与 SA）· §2.1（隔离承诺）
#        docs/04-ops/deploy.md §2（部署前后各跑一次，不允许跳过）· §13（部署后核对清单）
#
# **部署前后各跑一次。有任何差异一律非零退出。**
#
# 两层判定，各自能独立成立：
#
#   第一层 · 硬期望（不需要任何基线文件）
#     把 as-built 2026-08-16 的实测值写死在脚本里逐条比对。
#     它能回答「现在这一刻，现有资产是不是还是文档里那个样子」。
#     好处是**第一次跑就有判定力** —— 不需要「先有 before 快照」这个前提。
#
#   第二层 · 基线 diff（--baseline=<目录>）
#     把本次观测到的全部**非 bp- 资源**写成一份规范化文本，与部署前那份逐字节比。
#     它能抓住硬期望覆盖不到的东西：secret 版本数、修订版名、SA 列表变化……
#
# 🔴 这个脚本是**事后发现**不是**事前阻止**（as-built §8 的代价第 2 条）：
#    一条打错名字的 gcloud compute firewall-rules delete，它能告诉你出事了，但拦不住你。
#    真正的机制隔离要独立 GCP 项目 + 共享 VPC，那是另一次裁决。
#
# 本脚本**只做只读调用**。没有任何 create / update / delete。

set -euo pipefail

readonly EXPECTED_PROJECT_ID="oratis-491316"
readonly REGION="us-central1"

# ───────────────────────── as-built 2026-08-16 的实测值（硬期望）─────────────────────────
#
# 改这些常量之前先问一句：**是现实变了，还是我们把事情做坏了？**
# 只有前者才应该改这里，并且必须同步更新 docs/02-architecture/as-built-gcp.md。

readonly EXPECT_VPN_US="vpn-us|us-west1-a|e2-micro|8.231.52.43|RUNNING"
readonly EXPECT_VPN_JP="vpn-jp|asia-northeast1-a|e2-micro|34.104.192.233|RUNNING"

readonly EXPECT_ADDR_US="vpn-us-ip-v4|8.231.52.43|IN_USE"
readonly EXPECT_ADDR_JP="vpn-jp-ip|34.104.192.233|IN_USE"

# as-built §3 的 10 条防火墙规则，一条不增不减。
# 其中 allow-xray-443 / allow-hysteria-udp443 / default-allow-ssh **没有 target tag**，
# 对 default 网络里所有实例生效 —— 这是现存风险（非本项目引入），已记在 as-built §3。
EXPECT_FIREWALL=(
  allow-hysteria-udp443
  allow-iap-ssh
  allow-ss-48882
  allow-xray-443
  default-allow-icmp
  default-allow-internal
  default-allow-rdp
  default-allow-ssh
  vpn-iap-ssh-allow
  vpn-public-ssh-deny
)

# SSH 压制链的实测形态（2026-08-21 `gcloud compute firewall-rules list` 实查）。
#
# 🔴 为什么单独写这一组，而不是只比对规则名的集合：
#    上面的 EXPECT_FIREWALL 只比对**名字**，第二层基线 diff 记的也只有
#    direction / sourceRanges / targetTags / allowed —— 三者都不看
#    **priority / disabled / denied**。于是下面任意一种改动都能让两层判定同时绿灯通过：
#      · gcloud compute firewall-rules update vpn-public-ssh-deny --disabled
#      · 把 vpn-public-ssh-deny 的 priority 从 600 改到 65535（排到 default-allow-ssh 后面）
#      · 把它 denied 的端口从 tcp:22 改成别的
#    而这条 deny 是 vpn-us / vpn-jp 压制 default-allow-ssh(0.0.0.0/0:22) 的**唯一**手段，
#    上面三种改动的任意一种都会让两台现役代理节点的 22 端口立刻对全网放通。
#
# 判定用的是**相对顺序**而不是写死数字：GCP 按 priority 升序求值，
# 数字大小本身不重要，重要的是 iap-allow < public-deny < default-allow-ssh。
readonly EXPECT_SSH_DENY_RULE="vpn-public-ssh-deny"
readonly EXPECT_SSH_IAP_RULE="vpn-iap-ssh-allow"
readonly EXPECT_SSH_DEFAULT_RULE="default-allow-ssh"
readonly EXPECT_SSH_TAG="vpn-node"

# as-built §4 的三个现有 Cloud Run 服务。名字|URL。
EXPECT_RUN=(
  # ⚠️ 2026-08-17 实测修正：Cloud Run 的默认 URL 形式已从
  #    `<svc>-<项目号>.<region>.run.app` 改为 `<svc>-<哈希>-<区域缩写>.a.run.app`。
  #    as-built §4 最初记的是前者（从截断的列表里抄的），与线上不符，
  #    导致本检查开工第一天就三条全红。下面是实测值。
  "anthropic-relay|https://anthropic-relay-cko3zfff5a-uc.a.run.app"
  "lisa-cloud|https://lisa-cloud-cko3zfff5a-uc.a.run.app"
  "lisa-web|https://lisa-web-cko3zfff5a-uc.a.run.app"
)

# as-built §5 的现有 secret 与服务账号。
EXPECT_SECRETS=(anthropic-api-key relay-token)
EXPECT_SA=(
  "2360090741-compute@developer.gserviceaccount.com"
  "vertex-express@oratis-491316.iam.gserviceaccount.com"
  "cuddler-play-billing@oratis-491316.iam.gserviceaccount.com"
)

readonly EXPECT_AR_REPO="cloud-run-source-deploy"

# ───────────────────────── 运行时开关 ─────────────────────────

PROJECT_ID="${PROJECT_ID:-$EXPECTED_PROJECT_ID}"
BASELINE=""
OUT_DIR=""
DRY_RUN=0
KEEP_OUT=0
PASS_N=0
FAIL_N=0

# ───────────────────────── 通用工具 ─────────────────────────

log()  { printf '%s\n' "$*" >&2; }
step() { printf '\n\033[1m▸ %s\033[0m\n' "$*" >&2; }
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


pass() { printf '  \033[32m✓\033[0m %s\n' "$*" >&2; PASS_N=$((PASS_N + 1)); }
fail() { printf '  \033[31m✗ %s\033[0m\n' "$*" >&2; FAIL_N=$((FAIL_N + 1)); }
warn() { printf '  \033[33m⚠\033[0m %s\n' "$*" >&2; }

usage() {
  cat <<'EOF'
用法: verify-isolation.sh [选项]

确认 vpn-us / vpn-jp / anthropic-relay / lisa-cloud / lisa-web 以及它们周边的
现有资产**未被 babel.plus 的部署影响**。**部署前后各跑一次。**

退出码:
  0  全部通过
  1  有差异 —— 立即停止部署 / 立即排查
  2  用法或环境错误（缺 gcloud、项目 ID 不对……）

选项:
  --baseline=<目录>  与之前一次运行留下的 isolation.txt 逐字节比对
  --out=<目录>       本次观测写到哪里（默认写临时目录，退出时删）
  --keep             保留临时目录（配合默认 --out 用）
  --project=<id>     GCP 项目 ID。必须是 oratis-491316
  --dry-run          只打印将要执行的只读命令，不真的调用
  -h, --help         显示本帮助

典型用法（部署前后各一次）:
  ./infra/scripts/verify-isolation.sh --out=snapshots/before   # 部署前
  # …部署…
  ./infra/scripts/verify-isolation.sh --baseline=snapshots/before

判定口径:
  不是「diff 为空」—— 新增 bp- 资源本来就会让 diff 非空。
  而是「**排除 bp- 前缀之后的部分必须逐字节相同**」，本脚本产出的 isolation.txt
  按构造就只含非 bp- 资源，所以可以直接 diff。
EOF
}

guard_project() {
  if [ "$PROJECT_ID" != "$EXPECTED_PROJECT_ID" ]; then
    die "PROJECT_ID 必须是 $EXPECTED_PROJECT_ID，当前是 \"$PROJECT_ID\"。
     本脚本里写死的全部期望值都来自 as-built-gcp.md 对这一个项目的实测，换项目就毫无意义。"
  fi
}

need_cmd() {
  command -v "$1" >/dev/null 2>&1 || die "缺少命令：$1"
}

# 由 main 里的 trap cleanup EXIT 调用，shellcheck 看不出间接调用。
# 两个码都要留：0.9.0（CI 的 ubuntu-24.04 预装版）报 SC2317，SC2329 是 0.10.0 才引入的。
# shellcheck disable=SC2317,SC2329
cleanup() {
  if [ "$KEEP_OUT" -eq 0 ] && [ -n "$TMP_DIR" ] && [ -d "$TMP_DIR" ]; then
    rm -rf "$TMP_DIR"
  fi
}

# fetch <名字> <gcloud 参数...> —— 只读，结果落到 $OUT_DIR/<名字>.json
fetch() {
  local name="$1"; shift
  if [ "$DRY_RUN" -eq 1 ]; then
    local _a
    printf '  [dry-run] ' >&2
    for _a in "$@"; do qq "$_a" >&2; printf ' ' >&2; done
    printf -- '--format=json\n' >&2
    printf '[]' > "${OUT_DIR}/${name}.json"
    return 0
  fi
  if ! "$@" --format=json > "${OUT_DIR}/${name}.raw" 2>"${OUT_DIR}/${name}.err"; then
    fail "取不到 ${name}（gcloud 调用失败，见 ${OUT_DIR}/${name}.err）。
     取不到 = 判定不了 = **当作失败**。隔离检查不允许「查不到就算通过」。"
    printf '[]' > "${OUT_DIR}/${name}.json"
    return 1
  fi

  # ⚠️ gcloud 会把噪音写进 **stdout** 且仍然 exit 0，于是 --format=json 的输出不是合法 JSON。
  # 2026-08-17 实测：`gcloud artifacts repositories list` 在 Python 3.9 的环境下，
  # stdout 第一行是 `An error occurred: module 'importlib.metadata' has no attribute
  # 'packages_distributions'`，紧跟才是 `[`。退出码 0，所以上面那个 if 检测不到。
  #
  # 这会让隔离检查**静默误报**（JSON 解析不出来 → 判定为「资源不存在」），
  # 而一个天天报红的检查等于没有检查。所以在这里统一剥掉 JSON 之前的所有内容。
  # 只认第一个 `[` 或 `{` 起头 —— gcloud 的 --format=json 输出必定以其中之一开始。
  awk 'p{print;next} /^[[{]/{p=1;print}' "${OUT_DIR}/${name}.raw" > "${OUT_DIR}/${name}.json"

  if ! jq -e 'type' "${OUT_DIR}/${name}.json" >/dev/null 2>&1; then
    fail "取到的 ${name} 不是合法 JSON（原始输出见 ${OUT_DIR}/${name}.raw）。
     同样按「取不到 = 判定不了 = 失败」处理。"
    printf '[]' > "${OUT_DIR}/${name}.json"
    return 1
  fi

  rm -f "${OUT_DIR}/${name}.err" "${OUT_DIR}/${name}.raw"
  return 0
}

jqf() { jq -r "$2" "${OUT_DIR}/${1}.json" 2>/dev/null || true; }

# ───────────────────────── 第一层：硬期望 ─────────────────────────

check_instances() {
  step "1 · 现有代理节点（as-built §2）"
  local actual want name
  for want in "$EXPECT_VPN_US" "$EXPECT_VPN_JP"; do
    name="${want%%|*}"
    actual="$(jqf instances "
      [.[]? | select(.name == \"$name\")
       | \"\(.name)|\(.zone // \"?\" | split(\"/\") | last)|\(.machineType // \"?\" | split(\"/\") | last)|\(.networkInterfaces[0].accessConfigs[0].natIP // \"-\")|\(.status // \"?\")\"] | .[0] // \"(不存在)\"")"
    if [ "$actual" = "$want" ]; then
      pass "$name  $actual"
    else
      fail "$name 与 as-built 不符
     期望: $want
     实际: $actual"
    fi
  done
}

check_addresses() {
  step "2 · 保留静态外部 IP（as-built §2）"
  # vpn-us-ip-v4 的 -v4 后缀记录着「美国节点 IP 已被封锁并更换过三次」。
  # 这两个地址一旦变化，意味着有人动了现役节点的入口。
  local actual want name
  for want in "$EXPECT_ADDR_US" "$EXPECT_ADDR_JP"; do
    name="${want%%|*}"
    actual="$(jqf addresses "
      [.[]? | select(.name == \"$name\")
       | \"\(.name)|\(.address // \"?\")|\(.status // \"?\")\"] | .[0] // \"(不存在)\"")"
    if [ "$actual" = "$want" ]; then
      pass "$name  $actual"
    else
      fail "$name 与 as-built 不符
     期望: $want
     实际: $actual"
    fi
  done
}

check_firewall() {
  step "3 · 防火墙规则（as-built §3：10 条，一条不增不减）"
  local actual expected
  # 只看非 bp- 规则。新增 bp- 规则是允许的（as-built §2.1 第 3 条），不算差异。
  actual="$(jqf firewall '[.[]? | .name | select(startswith("bp-") | not)] | sort | join(" ")')"
  expected="$(printf '%s\n' "${EXPECT_FIREWALL[@]}" | sort | tr '\n' ' ' | sed 's/ $//')"
  if [ "$actual" = "$expected" ]; then
    pass "10 条非 bp- 规则与 as-built 完全一致"
  else
    fail "非 bp- 防火墙规则集合发生变化
     期望: $expected
     实际: $actual
     🔴 现有两台节点靠 vpn-public-ssh-deny 压制 default-allow-ssh(0.0.0.0/0:22)。
        删掉那条 deny 会让 vpn-us / vpn-jp 立刻裸奔 22 端口。"
  fi

  check_ssh_posture
}

# check_ssh_posture 核对 SSH 压制链**还在生效**，而不只是「那条规则还叫这个名字」。
#
# 规则可以在名字不变的前提下被 disable、被改 priority、被改 denied 端口 ——
# 三者都不会被上面的名字集合比对或第二层基线 diff 抓到（那两处都不采集这些字段）。
# 本函数是唯一看这三个字段的地方。
check_ssh_posture() {
  local deny_disabled deny_prio deny_ports deny_tags iap_prio default_prio

  # ⚠️ 必须先 tostring 再走 `//`：jq 的 `//` 把 **false 也当成空值**，
  # 所以 `[false] | .[0] // "(不存在)"` 会返回 "(不存在)" —— 一条正常启用的规则
  # 会被误判成「读不到」，进而误报暴露。这类误报比漏报更伤：它会训练人忽略这个检查。
  deny_disabled="$(jqf firewall "[.[]? | select(.name == \"$EXPECT_SSH_DENY_RULE\") | ((.disabled // false) | tostring)] | .[0] // \"(不存在)\"")"
  deny_prio="$(jqf firewall "[.[]? | select(.name == \"$EXPECT_SSH_DENY_RULE\") | (.priority // 65535)] | .[0] // \"\"")"
  deny_ports="$(jqf firewall "[.[]? | select(.name == \"$EXPECT_SSH_DENY_RULE\") | ((.denied // []) | map(\"\(.IPProtocol):\((.ports // [\"all\"]) | join(\",\"))\") | sort | join(\" \"))] | .[0] // \"\"")"
  deny_tags="$(jqf firewall "[.[]? | select(.name == \"$EXPECT_SSH_DENY_RULE\") | ((.targetTags // []) | sort | join(\",\"))] | .[0] // \"\"")"
  iap_prio="$(jqf firewall "[.[]? | select(.name == \"$EXPECT_SSH_IAP_RULE\") | (.priority // 65535)] | .[0] // \"\"")"
  default_prio="$(jqf firewall "[.[]? | select(.name == \"$EXPECT_SSH_DEFAULT_RULE\") | (.priority // 65535)] | .[0] // \"\"")"

  # dry-run 下 fetch 写的是空数组，所有字段都取不到 —— 那不是「压制链坏了」，
  # 只是没有数据，所以不能判 fail。
  if [ "$DRY_RUN" -eq 1 ]; then
    warn "[dry-run] 跳过 SSH 压制链姿态核对"
    return 0
  fi

  if [ "$deny_disabled" != "false" ]; then
    fail "$EXPECT_SSH_DENY_RULE 的 disabled = $deny_disabled（期望 false）
     🔴 被 disable 的 deny 规则名字还在，集合比对与基线 diff 都看不出来，
        但 vpn-us / vpn-jp 的 22 端口此刻对 0.0.0.0/0 敞开。"
    return 0
  fi

  case "$deny_ports" in
    *tcp:22*) : ;;
    *)
      fail "$EXPECT_SSH_DENY_RULE 不再 deny tcp:22（实际: ${deny_ports:-<空>}）
     🔴 同上：名字没变，压制没了。"
      return 0
      ;;
  esac

  case ",$deny_tags," in
    *,"$EXPECT_SSH_TAG",*) : ;;
    *)
      fail "$EXPECT_SSH_DENY_RULE 的 target tag 不含 $EXPECT_SSH_TAG（实际: ${deny_tags:-<无>}）
     🔴 没有这个 tag，这条 deny 就落不到 vpn-us / vpn-jp 上。"
      return 0
      ;;
  esac

  if [ -z "$deny_prio" ] || [ -z "$default_prio" ] || [ -z "$iap_prio" ]; then
    fail "SSH 相关规则的 priority 读不全（deny=${deny_prio:-?} iap=${iap_prio:-?} default=${default_prio:-?}）"
    return 0
  fi

  # GCP 按 priority 升序求值，先命中先生效。
  if [ "$deny_prio" -ge "$default_prio" ]; then
    fail "$EXPECT_SSH_DENY_RULE 的 priority=$deny_prio 不再优先于 $EXPECT_SSH_DEFAULT_RULE 的 $default_prio
     🔴 求值顺序反了：0.0.0.0/0 的 allow 会先命中，deny 永远轮不到。"
    return 0
  fi
  if [ "$iap_prio" -ge "$deny_prio" ]; then
    fail "$EXPECT_SSH_IAP_RULE 的 priority=$iap_prio 不再优先于 $EXPECT_SSH_DENY_RULE 的 $deny_prio
     ⚠️ 这个方向的错误不会造成暴露，但会把**自己**也挡在外面：
        IAP 隧道进不去，节点就只能靠串口控制台救。"
    return 0
  fi

  pass "SSH 压制链在位  ${EXPECT_SSH_IAP_RULE}(${iap_prio}) < ${EXPECT_SSH_DENY_RULE}(${deny_prio}, ${deny_ports}) < ${EXPECT_SSH_DEFAULT_RULE}(${default_prio})"
}

check_run_services() {
  step "4 · 现有 Cloud Run 服务（as-built §4）"
  local want name url actual
  for want in "${EXPECT_RUN[@]}"; do
    name="${want%%|*}"
    url="${want#*|}"
    actual="$(jqf run "
      [.[]? | select((.metadata.name // .name) == \"$name\")
       | (.status.url // \"?\")] | .[0] // \"(不存在)\"")"
    if [ "$actual" = "$url" ]; then
      pass "$name  $actual"
    else
      fail "$name 的 URL 与 as-built 不符
     期望: $url
     实际: $actual"
    fi
  done
  # 「lastDeployed 不变」这一条硬期望做不到：as-built §4 只记了日期（2026-07-02 等），
  # 而 gcloud 里对应的字段路径 **待核实**。它由第二层的基线 diff 覆盖 ——
  # isolation.txt 里记了 latestReadyRevisionName，重新部署一定会让它变。
}

check_secrets() {
  step "5 · 现有 Secret（as-built §5）"
  local name found
  for name in "${EXPECT_SECRETS[@]}"; do
    found="$(jqf secrets "[.[]? | (.name // \"\" | split(\"/\") | last) | select(. == \"$name\")] | length")"
    if [ "${found:-0}" -ge 1 ]; then
      pass "$name 存在"
    else
      fail "$name 不存在或读不到。
     🔴 它属于现有服务。babel.plus 从不需要碰它 ——
        逐 secret 授权（deploy.md §1 第 2 条）的全部意义就是让 bp-api-sa 连看都看不到它。"
    fi
  done
}

check_artifacts() {
  step "6 · Artifact Registry（as-built §4）"
  local found
  found="$(jqf artifacts "[.[]? | (.name // \"\" | split(\"/\") | last) | select(. == \"$EXPECT_AR_REPO\")] | length")"
  if [ "${found:-0}" -ge 1 ]; then
    pass "$EXPECT_AR_REPO 存在"
  else
    fail "$EXPECT_AR_REPO 不存在。它是现有三个服务的镜像所在地，删了等于让它们无法重新部署。"
  fi
  if [ "$DRY_RUN" -eq 0 ]; then
    local count
    # 同 fetch()：gcloud 可能把噪音写进 stdout 且仍 exit 0，所以这里也要剥掉 JSON 前缀。
    count="$(gcloud artifacts docker images list \
      "${REGION}-docker.pkg.dev/${PROJECT_ID}/${EXPECT_AR_REPO}" \
      --project="$PROJECT_ID" --format=json 2>/dev/null \
      | awk 'p{print;next} /^[[{]/{p=1;print}' \
      | jq 'length' 2>/dev/null || printf '')"
    if [ -n "$count" ]; then
      printf '%s\n' "$count" > "${OUT_DIR}/ar-image-count.txt"
      log "  · $EXPECT_AR_REPO 当前镜像数 = $count（判定见「镜像数不减少」一节）"
    else
      warn "取不到 $EXPECT_AR_REPO 的镜像数（权限或 API 变化）。「镜像数不减少」这一条本次**未验证**。"
    fi
  fi
}

check_service_accounts() {
  step "7 · 现有服务账号（as-built §5）"
  local email found
  for email in "${EXPECT_SA[@]}"; do
    found="$(jqf sa "[.[]? | select(.email == \"$email\")] | length")"
    if [ "${found:-0}" -ge 1 ]; then
      pass "$email"
    else
      fail "$email 不存在或读不到"
    fi
  done
  # 反向检查：bp-api 绝不能跑在 Compute 默认 SA 上。这一条不在 as-built 的清单里，
  # 但它是「爆炸半径不接到 lisa-* 上」的直接体现（as-built §5 / deploy.md §3.3）。
  if [ "$DRY_RUN" -eq 0 ]; then
    local bp_sa
    bp_sa="$(gcloud run services describe bp-api --project="$PROJECT_ID" --region="$REGION" \
      --format='value(spec.template.spec.serviceAccountName)' 2>/dev/null || printf '')"
    if [ -z "$bp_sa" ]; then
      log "  · bp-api 尚未部署，跳过「运行时身份不是 Compute 默认 SA」检查"
    elif [ "$bp_sa" = "bp-api-sa@${PROJECT_ID}.iam.gserviceaccount.com" ]; then
      pass "bp-api 跑在 bp-api-sa 上"
    else
      fail "bp-api 的运行时身份是 $bp_sa，不是 bp-api-sa@${PROJECT_ID}.iam.gserviceaccount.com
     🔴 用 Compute 默认 SA 跑 bp-api 等于把 babel.plus 的爆炸半径接到现有工作负载上。"
    fi
  fi
}

# ───────────────────────── 第二层：基线 diff ─────────────────────────

write_isolation_txt() {
  local f="${OUT_DIR}/isolation.txt"
  {
    printf '# babel.plus 隔离基线（**只含非 bp- 资源**，故可以直接 diff）\n'
    printf '# 项目: %s\n' "$PROJECT_ID"
    printf '# 来源: infra/scripts/verify-isolation.sh\n'
    printf '# 刻意不含时间戳 —— 带时间戳的文件没法 diff。\n'
    printf '\n'
  } > "$f"

  {
    jqf instances '[.[]? | select(.name | startswith("bp-") | not)
      | "instance \(.name) zone=\(.zone // "?" | split("/") | last) machine=\(.machineType // "?" | split("/") | last) ip=\(.networkInterfaces[0].accessConfigs[0].natIP // "-") status=\(.status // "?")"] | sort | .[]'
    jqf addresses '[.[]? | select(.name | startswith("bp-") | not)
      | "address \(.name) addr=\(.address // "?") status=\(.status // "?")"] | sort | .[]'
    jqf firewall '[.[]? | select(.name | startswith("bp-") | not)
      | "firewall \(.name) prio=\(.priority // "?") disabled=\(.disabled // false) dir=\(.direction // "?") src=\((.sourceRanges // []) | sort | join(",")) tags=\((.targetTags // ["-"]) | sort | join(",")) allow=\((.allowed // []) | map("\(.IPProtocol):\((.ports // ["all"]) | join(","))") | sort | join(" ")) deny=\((.denied // []) | map("\(.IPProtocol):\((.ports // ["all"]) | join(","))") | sort | join(" "))"] | sort | .[]'
    jqf run '[.[]? | select((.metadata.name // .name // "") | startswith("bp-") | not)
      | "run \(.metadata.name // .name // "?") url=\(.status.url // "?") rev=\(.status.latestReadyRevisionName // "?")"] | sort | .[]'
    jqf secrets '[.[]? | (.name // "" | split("/") | last) | select(startswith("bp-") | not)
      | "secret \(.)"] | sort | .[]'
    # shellcheck disable=SC2016  # $n 是 jq 的变量，不是 shell 的
    jqf artifacts '[.[]? | (.name // "" | split("/") | last) as $n | select($n | startswith("bp-") | not)
      | "artifact \($n) format=\(.format // "?")"] | sort | .[]'
    jqf sa '[.[]? | select((.email // "") | startswith("bp-") | not)
      | "sa \(.email // "?")"] | sort | .[]'
  } >> "$f"

  # secret 的版本数：as-built §2.1 要求「版本数不变」，但 as-built 没记下基线数字，
  # 所以它只能靠 before/after 比对，不能写死。逐个查，只查非 bp- 的那两个。
  if [ "$DRY_RUN" -eq 0 ]; then
    local name n
    for name in "${EXPECT_SECRETS[@]}"; do
      n="$(gcloud secrets versions list "$name" --project="$PROJECT_ID" --format=json 2>/dev/null \
           | jq 'length' 2>/dev/null || printf '?')"
      printf 'secret-versions %s count=%s\n' "$name" "$n" >> "$f"
    done
  fi
}

compare_baseline() {
  step "8 · 与基线逐字节比对：$BASELINE"
  local base="${BASELINE%/}/isolation.txt"
  if [ ! -f "$base" ]; then
    fail "基线文件不存在：$base
     先在部署前跑一次 ./infra/scripts/verify-isolation.sh --out=$BASELINE"
    return 0
  fi
  if diff -u "$base" "${OUT_DIR}/isolation.txt"; then
    pass "非 bp- 资源与基线逐字节相同"
  else
    fail "🔴 **现有资源被改动了。** 上面就是 diff。立即停止部署并排查。
     判定口径提醒：新增 bp- 资源不会出现在这份文件里（按构造排除），
     所以任何一行差异都意味着**非本项目的资源发生了变化**。"
  fi

  # 镜像数只允许不减少（as-built §2.1）：现有服务重新部署会让它增加，那不是我们的事。
  local before_n after_n
  before_n="$(cat "${BASELINE%/}/ar-image-count.txt" 2>/dev/null || printf '')"
  after_n="$(cat "${OUT_DIR}/ar-image-count.txt" 2>/dev/null || printf '')"
  if [ -n "$before_n" ] && [ -n "$after_n" ]; then
    if [ "$after_n" -lt "$before_n" ]; then
      fail "$EXPECT_AR_REPO 的镜像数从 $before_n 降到 $after_n。
     🔴 那是 anthropic-relay / lisa-cloud / lisa-web 的镜像仓库。
        babel.plus 的镜像在 bp-images 里，我们没有任何理由让这个数字变小。"
    else
      pass "$EXPECT_AR_REPO 镜像数 $before_n → $after_n（未减少）"
    fi
  else
    warn "缺少镜像数记录，「镜像数不减少」这一条本次未比对"
  fi
}

# ───────────────────────── 主流程 ─────────────────────────

main() {
  local arg
  for arg in "$@"; do
    case "$arg" in
      --baseline=*) BASELINE="${arg#*=}" ;;
      --out=*)      OUT_DIR="${arg#*=}" ;;
      --keep)       KEEP_OUT=1 ;;
      --project=*)  PROJECT_ID="${arg#*=}" ;;
      --dry-run)    DRY_RUN=1 ;;
      -h|--help)    usage; exit 0 ;;
      *)            usage >&2; die "未知参数：$arg" ;;
    esac
  done

  guard_project
  need_cmd gcloud
  need_cmd jq

  if [ -z "$OUT_DIR" ]; then
    TMP_DIR="$(mktemp -d "${TMPDIR:-/tmp}/bp-isolation.XXXXXX")"
    OUT_DIR="$TMP_DIR"
  else
    KEEP_OUT=1
  fi
  mkdir -p "$OUT_DIR"
  trap cleanup EXIT

  log "项目 : $PROJECT_ID"
  log "输出 : $OUT_DIR"
  if [ -n "$BASELINE" ]; then
    log "基线 : $BASELINE"
  fi
  if [ "$DRY_RUN" -eq 1 ]; then
    log "模式 : DRY-RUN（连只读调用都不发，判定结果无意义）"
  fi

  # 只读抓取。任何一次失败都记 FAIL —— 「查不到」不等于「没问题」。
  fetch instances gcloud compute instances list      --project="$PROJECT_ID" || true
  fetch addresses gcloud compute addresses list      --project="$PROJECT_ID" || true
  fetch firewall  gcloud compute firewall-rules list --project="$PROJECT_ID" || true
  fetch run       gcloud run services list           --project="$PROJECT_ID" --region="$REGION" || true
  fetch secrets   gcloud secrets list                --project="$PROJECT_ID" || true
  fetch artifacts gcloud artifacts repositories list --project="$PROJECT_ID" || true
  fetch sa        gcloud iam service-accounts list   --project="$PROJECT_ID" || true

  check_instances
  check_addresses
  check_firewall
  check_run_services
  check_secrets
  check_artifacts
  check_service_accounts

  write_isolation_txt
  if [ -n "$BASELINE" ]; then
    compare_baseline
  else
    step "8 · 基线 diff"
    log "  · 未给 --baseline=<目录>，只跑了硬期望。"
    log "    部署前请先留一份：./infra/scripts/verify-isolation.sh --out=snapshots/before"
  fi

  step "结果"
  log "  通过 $PASS_N 项 / 失败 $FAIL_N 项"
  log "  本次观测：${OUT_DIR}/isolation.txt"
  if [ "$FAIL_N" -ne 0 ]; then
    log ""
    log "  🔴 有差异。**停止部署。**"
    log "     人工复核清单（as-built §2.1，一条都不能少）："
    log "       · vpn-us / vpn-jp 两台实例的可用区、机型、外网 IP、状态"
    log "       · vpn-us-ip-v4 / vpn-jp-ip 两个保留 IP 均 IN_USE 且地址不变"
    log "       · 10 条防火墙规则一条不增不减不改"
    log "       · anthropic-relay / lisa-cloud / lisa-web 的 URL 与最后部署时间"
    log "       · anthropic-api-key / relay-token 存在且版本数不变"
    log "       · cloud-run-source-deploy 存在且镜像数不减少"
    exit 1
  fi
  log "  ✅ 现有资产未受影响。"
  exit 0
}

TMP_DIR=""

main "$@"

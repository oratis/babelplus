# 参考项目调研

> 日期：2026-08-16 · 性质：**参考仓库调研**（结论均出自实际 clone 后通读源文件，非二手推断）
> 调研对象：`oratis/Proxy_Skill`（技术方案与部署脚本）、`DiogenesModel/Diogenes`（文档体系）
> 访问方式：`gh` CLI 已认证，两个仓库**均为 private 且均可访问**，已浅克隆到 scratchpad 通读。
> 口径：本文只记录仓库中实际存在的内容；凡是我没在文件里读到的，不写。

---

## 一、oratis/Proxy_Skill

### 1.1 访问状态

**可访问。** `gh repo view oratis/Proxy_Skill` 成功返回，`gh repo clone` 完成。仓库为 private，
无 description，**单分支单提交**：

```
77f1bdd Add Japan region, Hysteria2, and resilient regional failover (#1)
```

### 1.2 仓库全貌（4 个文件，无子目录）

| 文件 | 体积 | 角色 |
|---|---|---|
| `VPN方案设计.md` | 22.9 KB | 唯一的设计文档，同时充当决策记录、实测报告与 runbook |
| `gen-clash.py` | 12.1 KB | 客户端配置生成器（Clash.Meta / Mihomo YAML） |
| `setup-server.sh` | 7.3 KB | 服务端一键全栈 provisioning |
| `optimize-network.sh` | 1.2 KB | 对已部署节点单独重放 sysctl 调优 |

`.gitignore` 明确了两类不入库产物：秘密文件 `.secrets.env` / `.secrets-*.env`，
以及生成物目录 `clash-configs/`（注释写明「contains real UUIDs / SS password / Reality keys」）。
`.gitattributes` 仅一行 `*.sh text eol=lf`。

> ⚠️ 本文**刻意不复制**源仓库中出现的具体静态 IP、Cloudflare Tunnel UUID 与主机名对应关系。
> 需要时直接查 `VPN方案设计.md` §9.1/§9.5。凭据本身两个仓库里都没有（在 gitignored 的 `.secrets.env`）。

### 1.3 这个仓库做什么

它不是一个 Agent Skill（没有 `SKILL.md`、没有 frontmatter），而是**一套「自用跨境代理节点」的
方案文档 + 部署脚本**。目标：在 GCP 上自建美国/日本出口节点，本机 Clash Verge 按规则分流，
主要服务场景是 **AI 站点访问**（`gen-clash.py` 的规则表里 `🤖 AI` 组是第一优先级分类，
覆盖 openai / anthropic / claude.ai / gemini / perplexity / x.ai / huggingface 等 20+ 域名）。

### 1.4 协议矩阵（当前部署 = 7 条路径 / 2 区域）

这是本仓库最有复用价值的部分。**同一台 VM 上并行跑三种入口**，两个区域共 7 条路径：

| 节点 | 协议 | 入口 | 定位 |
|---|---|---|---|
| `US-CDN` | VLESS + WS + TLS | Cloudflare anycast `:443` | 抗封锁主力；VM IP 被封不受影响 |
| `US-Reality` | VLESS + Reality (`xtls-rprx-vision`) | 静态 IP `:443`，伪装 `www.cloudflare.com` | 直连主力 |
| `US-SS` | Shadowsocks-2022 (`2022-blake3-aes-128-gcm`) | 静态 IP 高位端口，tcp+udp | 兜底 |
| `JP-HY2` | **Hysteria2 / QUIC over UDP** | 日本静态 IP UDP `:443` | **默认路径**，单流吞吐最高 |
| `JP-Reality` / `JP-SS` / `JP-CDN` | 同上美国三种 | 日本节点 | 日本区同构副本 |

关键点：Reality 用 TCP `:443`，Hysteria2 用 UDP `:443`，**两者不冲突**，同一端口号双协议共存。

### 1.5 实测结论（对 babel.plus 直接有用的工程事实）

`VPN方案设计.md` §十一 是一次完整的量化诊断，结论强度很高：

1. **瓶颈是单条 TCP 流的拥塞控制，不是带宽上限。** 经 JP-SS 下载 Cloudflare 测速文件：
   单流 132 KB/s，8 并发聚合 **1093 KB/s（8.3× 近线性）**。管道远未跑满。
2. **加大服务端缓冲区不解决这个问题。** 这次测量是在 sysctl 调优已生效之后跑的，
   原文直接写「那套调优正确且无害，但不要期待吞吐提升」。
3. **换节点无用**：JP 与 US 单流吞吐同在 150–310 KB/s 区间，差异只在延迟。
4. **mux / smux 有害**：多路复用把多个逻辑流塞进同一条 TCP 连接，受同一个单流上限约束，
   还引入队头阻塞。
5. **对症解法是换用自带拥塞控制的 UDP 协议。** 部署 Hysteria2 后同时段交叉轮询 4 轮，
   单流吞吐中位数：

   | 节点 | 协议 | 单流吞吐中位 |
   |---|---|---|
   | JP-HY2（Brutal 拥塞控制） | Hysteria2 / QUIC UDP | **~1700 KB/s** |
   | JP-HY2（BBR） | Hysteria2 / QUIC UDP | 1094 KB/s |
   | JP-SS | SS-2022 / TCP | 370 KB/s |
   | JP-Reality | VLESS+Reality / TCP | 269 KB/s |

   **单流 4.6 倍提升**；BBR 模式的 1094 KB/s 恰好等于此前 8 条 TCP 流才凑出的聚合值。

6. **`url-test` 按延迟选路会稳定选错。** 各健康节点延迟都落在同一噪声带（100–250ms），
   吞吐却差 4–5 倍。因此默认组改指向 `fallback` 类型的区域组（把最快的放首位、
   保留失效自动跳过），而不是 `url-test` 自动组。
7. **IP 级封锁的取证方法**（三条独立证据，可直接照搬为 babel.plus 的排障 SOP）：
   ① 服务端进程 `active`、端口正常 bind；② 服务端 443 上**没有任何来自公网的 established 连接**、
   数小时零日志；③ 同样端口从境外回打可完成 TCP 握手，而同协议同配置的另一区域节点完全正常。
8. **本机 Clash 开 TUN(fake-ip) 时，`dig` / `nc` / `curl --interface` 全部被劫持，不可作为连通性依据。**
   验证链路要用 mihomo API（unix socket `/tmp/verge/verge-mihomo.sock`）的 delay 接口。
   原文记录了对照实验：`--interface en0` 连 baidu 的正对照也失败。
9. 换 IP 属治标：新 IP 同样会被再次探测封锁。**真正的抗封锁路径是 CDN 与 Hysteria2。**

### 1.6 部署目标与 provisioning 逻辑

**目标平台：GCP Compute Engine**（项目 `oratis-491316`），非容器、非 K8s。

- 机型 `e2-micro` / Debian 12 / 30GB pd-standard，落在 GCP Free Tier 三区（`us-west1` / `us-central1` / `us-east1`）内。
- **Reserved Static External IP**：挂在运行中的 VM 上免费；被封后删除重新预留即可换 IP。
  已轮换到第四代（`vpn-us-ip-v4`）。
- 网络层级选 **Premium Tier**（Google 骨干），理由是延迟稳定性；成本差约 $14.5/月。
- 防火墙按 **tag** 匹配（`--tags=vpn-node`），换 IP 时无需改规则。
  规则：`allow-ss`、`allow-xray-443`、`allow-hysteria-udp443`，SSH 只放通 IAP 来源 `35.235.240.0/20`。
- SSH 管理通道：`gcloud compute ssh <vm> --tunnel-through-iap`（公网 22 已被 deny 规则压制）。

`setup-server.sh` 是**幂等的 8 步全栈脚本**，从本地把 secrets 以环境变量转发过去执行：

```
[1/8] sysctl 调优 → /etc/sysctl.d/99-proxy-network.conf
[2/8] 装 shadowsocks-rust（GitHub release 静态二进制，pin v1.22.0，按 uname -m 选 target）
[3/8] 写 /etc/shadowsocks/config.json（单密钥模式）+ systemd unit
[4/8] 装 xray（官方 install-release.sh）
[5/8] 写 xray config：Reality :443 + VLESS/WS 127.0.0.1:8080 双 inbound
[6/8] 装 cloudflared（token 模式，CF_TUNNEL_TOKEN 为空则跳过）
[7/8] unattended-upgrades
[8/8] SSH 加固（禁密码/禁 root/禁 challenge-response）
末尾：systemctl is-active + ss -tulnp 自检
```

调用方式（凭据只经环境变量，不落盘、不进命令历史文件）：

```bash
set -a; source .secrets.env; set +a
gcloud compute ssh vpn-us --project=oratis-491316 --zone=us-west1-a --tunnel-through-iap \
  --command="SS_PORT=$SS_PORT ... CF_TUNNEL_TOKEN=$CF_TUNNEL_TOKEN bash -s" < setup-server.sh
```

systemd 硬化写法值得抄：`ssserver.service` 用 `LoadCredential=config:/etc/shadowsocks/config.json`
+ `ExecStart=... -c %d/config` + `DynamicUser=true` + `ProtectSystem=strict` + `NoNewPrivileges=true`；
xray 则单独建 system user 并用 drop-in `/etc/systemd/system/xray.service.d/20-user.conf` 覆写 `User=`。

### 1.7 Cloudflare 具体做法

- **cloudflared token 模式**：`cloudflared service install <token>`，
  **ingress 规则托管在 Cloudflare Zero Trust 后台**（Networks → Tunnels），VM 上无本地配置文件。
  重建节点时只需要 token。
- 回源 `http://localhost:8080` → xray 的 VLESS+WS inbound（**仅监听 127.0.0.1**，不暴露公网）。
- DNS 由隧道自动接管（橙云代理）。日本隧道通过 Cloudflare API 创建，注册 4 条 QUIC 连接到 nrt01/nrt15/nrt16 边缘。
- cloudflared 是**出站**连接，所以换 VM 静态 IP 时 CDN 路径全程不受影响。
- 已知噪音：日志中大量 `context canceled` / `stream canceled by remote` ERR 是 WS 长连接正常断开，非故障。

### 1.8 sysctl 调优（两个脚本共用同一份，可直接复用）

```
net.core.default_qdisc=fq
net.ipv4.tcp_congestion_control=bbr
net.ipv4.tcp_mtu_probing=1          # 恢复 path-MTU 黑洞
net.ipv4.tcp_fastopen=3             # 双向 TFO
net.ipv4.tcp_slow_start_after_idle=0
net.core.rmem_max=16777216
net.core.wmem_max=16777216
net.core.netdev_max_backlog=4096
net.ipv4.tcp_rmem=4096 131072 16777216
net.ipv4.tcp_wmem=4096 65536 16777216
```

注释明确：保留 GCE 现役的多队列 qdisc，这个 default 只作用于新建的单队列接口。

### 1.9 配置生成器 `gen-clash.py` 的设计

纯 stdlib（`json / os / pathlib / sys`），**无模板引擎**——一个大 `TEMPLATE` 字符串 + `str.format(**env)`。

- 凭据来源优先级：`.secrets.env` → `.secrets-jp.env`（可选，同名键后者覆盖）→ **同名环境变量最高优先**。
- `REQUIRED` 列出 28 个必需变量，**缺任何一个在渲染前直接 `sys.exit`**（fail-closed，不生成半成品）。
- 内置 `ALIASES` 兼容历史拼写（`CDN_SERVER`←`CDN_ADDR`、`CDN_WS_PATH`←`CDN_WSPATH`、`JP_STATIC_IP`←`JP_IP`）。
  代码注释直接写明这次拼写不一致「导致生成器长期无法运行」——**把踩过的坑写在代码里**。
- 含特殊字符的密码用 `json.dumps()` 转成安全的 YAML 标量再插值。
- 输出 6 份设备配置：`mac / iphone / ipad / laptop / spare / windows`，每份内容相同、只有头部注释的设备名不同。

生成的 Clash 配置结构：`fake-ip` DNS（国内 DoH `223.5.5.5`/`1.12.12.12` + 境外 fallback + `geoip-code: CN` 过滤）、
7 个 proxies、8 个 proxy-groups（`🚀 Proxy` / `⚡ Auto` / `🇺🇸 United States` / `🇯🇵 Japan` / `🤖 AI` / `🌎 Global` / `🇨🇳 Direct-CN` / `🎯 Final`）、
分层 rules（私网 → AI → 日本地区解锁 → 流媒体走直连 → 常见西方站点 → CN 域名 → `GEOIP,CN` → `MATCH`）。

**规则表里两条带注释的判断**值得注意：① 日本组只列**具名站点**，不用 `DOMAIN-SUFFIX,jp` 兜底，
因为那会把大量从国内直连更快的 `.jp` 站点也绕道，且会抢在 `GEOIP,CN,DIRECT` 之前生效；
② 流媒体（Netflix / Hulu / Disney+ 等）**刻意走直连**，因为 GCP IP 段通常已被这些平台封禁。

### 1.10 没有的东西

明确说明：仓库里**没有** Agent Skill 定义（无 `SKILL.md` / 无 frontmatter）、没有 IaC
（无 Terraform / Pulumi / Ansible）、没有 CI、没有测试、没有 Cloudflare Workers 代码、
没有节点自动 provisioning 编排（开 VM 仍是文档里的手敲 `gcloud` 命令，只有装机之后是脚本化的）。
「node-provisioning logic」的边界就在这里：**装机自动化 = 有；建机与建隧道 = 手动 + 文档**。

---

## 二、DiogenesModel/Diogenes 文档体系

仓库为 private、可访问、`main` 分支、主语言 Rust、体积 ~486 MB。
**注意：根目录 `docs/` 不是主文档体系**，它只有 11 个跨目录技术底稿；
真正成体系的是 **`DataMiner/docs/`（255 个文件）**。下文两者都覆盖。

### 目录结构

```
Diogenes/
├── README.md                     # 仓库门面：一句话定位 + 目录表格 + 指向 ONBOARDING
├── ONBOARDING.md                 # 54 KB，14 个 H2 章节的「项目接手文档」= 全局入口
├── AGENTS.md                     # 根级 Agent 规则（45 行）
├── TODO-2026-07-28.md            # 日期戳的全局 TODO 快照
├── .CODEX/                       # 面向 Agent 会话的「项目长期记忆」
│   ├── README.md                 #   目录说明 + 5 条使用规则
│   ├── PROJECT.md                #   稳定的项目结构、架构边界、事实源
│   ├── WORKING_MEMORY.md         #   当前状态、风险、技术债
│   ├── COMMANDS.md               #   按模块整理的开发与验证命令
│   └── REVIEW.md                 #   一次性的代码库审阅结论（带日期）
├── .claude/skills/<name>/SKILL.md    # 技能事实源（YAML frontmatter: name / description）
├── .agents/skills/…                  # 上者的镜像，由 tools/sync-agent-skills.py 生成，CI 拦截漂移
├── docs/                         # 跨目录技术底稿（不属于某条产品线的通用参考）
│   ├── <slug>-YYYY-MM.md         #   带年月后缀的调研，如 llm-vendor-landscape-2026-08.md
│   ├── <slug>-YYYY-MM.html       #   同名 .html = 同一文档的渲染版，与 .md 并存
│   ├── coding-model-training-and-dataset-formats.md   # 无日期 = 长期参考书
│   └── superpowers/plans/
│       └── YYYY-MM-DD-issue-NNN-<slug>.md    # 实施计划，日期前缀 + issue 号
└── DataMiner/
    ├── README.md                 # 模块门面，同时是 docs/ 的索引（正文内联引用 docs/NN）
    ├── AGENTS.md                 # 目录级 Agent 规则（62 行，进入该目录自动叠加装载）
    ├── TODO.md / RELEASE-NOTES.md
    └── docs/                     # ← 主文档体系，255 文件
        ├── 00-overview.md        # 00 号即索引/总览（没有 docs/README.md）
        ├── 01-recording-pipeline.md … 140-pose-depth-index-…-decision.md
        ├── NN-<slug>/            # 资产重的文档升级为目录
        │   ├── README.md         #   正文
        │   ├── schema/*.schema.json + *.sample.jsonl
        │   └── assets/*.mp4 / *.png
        ├── architecture.md       # 无编号 = 长期维护的「当前实现」文档
        ├── consent-registry.md / release-runbook.md / windows-*.md
        ├── rca-<slug>.md         # 无编号，rca- 前缀，4 篇
        └── evidence/             # 证据平面，与正文文档分离（自身无 README）
            ├── <topic>-YYYY-MM-DD.md          # 单文件证据，日期用连字符
            ├── <topic>-YYYYMMDD/              # 如 depth-vflip-20260805
            └── issue-NNN[-slug]/              # 如 issue-609-runtime-gates
                ├── README.md     #   证据说明 + 表格索引（6 个子目录中只有 3 个有）
                ├── SHA256SUMS.txt
                └── *.json / *.png / *.jsonl / *.pdf
```

### 命名与状态约定

**1）没有 YAML frontmatter。** 实测：`DataMiner/docs` + `docs` 下 **186 个 `.md`，0 个以 `---` 开头**。
frontmatter 只出现在 `.claude/skills/*/SKILL.md`（字段仅 `name` / `description`）。
文档元信息一律用 **H1 之后紧跟的引用块（`>`）**承载。

**2）文件名三种形态，各有语义：**

| 形态 | 例子 | 含义 |
|---|---|---|
| `NN-<slug>.md` | `107-depth-into-ingestion-plane-decision.md` | 编号 = **写作顺序/时间序**，一次性产出，写完基本不改 |
| `<slug>.md`（无编号） | `architecture.md`、`release-runbook.md`、`consent-registry.md` | **长期维护**的活文档 |
| `rca-<slug>.md` | `rca-encoder-tail-drain-budget.md` | 故障复盘，独立命名空间不占编号 |

编号**不保证唯一**：存在 `40-d6-age-of-empires-4-profile-evidence.md`、`40-d6-bannerlord-admission-blocker.md`、
`40-d8-certified-roster-readiness.md` 三个同为 40 的文件——同一批次/同一天的并列产出共用编号，
再用 `d6`/`d7`/`d8` 这类**阶段码**做二级区分。可见编号是「哪一轮」而非唯一主键。

**3）slug 全小写连字符、英文，即使正文是中文。** 长 slug 被接受（如
`58-60fps-alignment-and-input-vocabulary-analysis-and-implementation.md`）——文件名要能自解释。

**4）没有「每个文件夹一个 README」的硬规矩，执行也不彻底：**
`DataMiner/docs/` 根下**没有** `README.md`，索引职责由 `00-overview.md` 和上级 `DataMiner/README.md` 承担。
实测覆盖率：**4 个 `NN-<slug>/` 目录 4/4 都有 `README.md`**；
但 **`evidence/` 的 6 个子目录只有 3 个有**（`depth-vflip-20260805`、`game-prereq-20260806`、
`issue-609-runtime-gates` 有；`depth-formal-20260804`、`issue-423`、`issue-457` 没有，
后两者用 `issue423-evidence.md` / `fallout4-…-2026-08-04.md` 这样的具名文件代替）。
`evidence/` 目录自身也没有 README。
即：README 在这里是「目录型文档的正文」而非「目录清单」，且**纯资产目录经常漏写**——
这是应当在 babel-relay 里补强的地方，不是照抄的地方。

**5）状态与性质是两个正交的标记，都写在头部引用块里：**

`性质：` 声明**这份文档是什么类型**（实测枚举，出现频次由高到低）：
`架构裁决`、`调研`、`证据型核查`、`证据型取证`、`设计方案`、`执行计划 + 裁决记录`、
`工具建设 + 执行手册`、`进度评估（证据型）`、`排期计划`、`机制说明`、`方法沉淀`、
`桌面调研`、`对外需求书`、`正方 / 反方 / 裁决`、`全目录调研`。

`状态：` 声明**成熟度**，是自由文本而非枚举，但反复出现的模式清晰：
`设计稿 v2（2026-07-22）`、`设计冻结稿 v1`、`设计定稿 v2，待实施`、`计划稿`、`提案，未批准实施`、
`As-Built，按 2026-07-23 当前代码整理`、`执行中`、`实施归档稿 v1.0`、`草稿,未发送`、
`代码侧已修，真机验证未做`、`审计报告，事实已核实`。

**关键约定：状态几乎总是带日期，且成熟度词汇明确区分「设计目标 / 当前实现 / 测试结果」**——
`DataMiner/AGENTS.md` 把这条写成了硬规矩：「不重复生成长设计文档，始终区分设计目标、当前实现和测试结果」。

**6）文档生命周期靠「新文档推翻旧文档 + 显式交代」，不靠状态位。**
`107` 号的头部直接写 `**推翻 [77 号 §7.1](77-…​.md)**`，正文 §2 标题就是「被推翻的是什么」，
并用表格**逐条**交代旧裁决的 4 条理由在新架构下各自的落点，附一句准则：

> 一条裁决被推翻时，它的理由不会自动消失。

`140` 号则反向标注先例：`先例：[107 号](…)（depth 进摄取面，2026-08-07）——本裁决是它的同形延伸`。
文件**不删除、不改名、不加 DEPRECATED 前缀**，靠双向链接构成裁决谱系。
（`SUPERSEDED.md` / `PREFLIGHT_ONLY.md` 这类标记文件确实存在，但在
`DataMiner/config/catalogs/dev/*/` 下标注配置条目状态，不是文档体系的一部分。）

**7）plan / research / architecture 的分离方式**：不靠目录，靠 `性质：` 标记 + 命名空间：

| 类别 | 落点 | 标记 |
|---|---|---|
| 架构现状 | `DataMiner/docs/architecture.md`（无编号，长期维护） | `状态：As-Built` |
| 架构决策（ADR） | `DataMiner/docs/NN-<slug>-decision.md` | `性质：**架构裁决**` + `裁决人：` |
| 调研 | 根 `docs/<slug>-YYYY-MM.md`；`DataMiner/docs/NN-…-survey/research.md` | `性质：**调研**` / `**桌面调研**` |
| 计划 | `NN-…-plan.md`；`docs/superpowers/plans/YYYY-MM-DD-…md` | `性质：**排期计划**` / `**实施计划**` |
| 手册 / runbook | `NN-…-runbook.md`、`release-runbook.md` | `性质：**工具建设 + 执行手册**` + `读者：` |
| 复盘 | `rca-<slug>.md` | 固定四段式（见模板） |
| 证据 | `docs/evidence/<topic>-<date>/` | 目录 + README + `SHA256SUMS.txt` |

### 文档模板

以下骨架从实际文件中还原，行内说明用 `⟨⟩` 包裹。

**模板 A · 编号设计/裁决文档（ADR）** — 出自 `107-depth-into-ingestion-plane-decision.md`

```markdown
# 107 · 裁决：⟨一句话结论，不是话题名⟩

> 日期：2026-08-07。裁决人：用户。性质：**架构裁决**，**推翻 [77 号 §7.1](77-….md)**。
> 关联：[101 §6.3](101-….md)（冲突记录）、#503（客户端上传链路认 depth）、
> [102](102-….md)（现行手工管线）。

---

## 1 · 裁决
⟨结论前置。加粗的一句话说清楚裁了什么，再补一段边界⟩

---

## 2 · 被推翻的是什么
⟨表格逐条列旧裁决的理由 → 新架构下的落点。区分「不再适用 / 保留 / 行为变化」⟩

| 77 号的理由 | 新架构下 |
|---|---|
| ① … | **不再适用**：… |
| ④ 生命周期不同 | **行为变化**：…。这是代价，不是等价搬迁 |

---

## 3 · ⟨机制落点：新的约束/不变量长什么样⟩
⟨用代码块写判定式或伪代码；解释「为什么是这个方向」而不只是「是什么」⟩

---

## 4 · ⟨查证时发现的相邻问题⟩
### 4.1 处置：⟨裁决 + 括注日期与裁决人⟩

> ⚠️ **这一选择的代价，必须留在记录里而不是被措辞掩盖：**
> 1. …
> 2. …
> ⟨代价段用引用块 + ⚠️ 显式标注，并写明「什么情况下这个取舍不再成立」⟩

## 5 · 已落地 / 未落地
**已落地（服务端，2026-08-07 已部署）** …
**已落地（客户端运行时代码，尚未发布到采集机）** …
**未落地（客户端发布与真实验收）**：…

---

## 6 · 这次裁决没有解决的
⟨显式列出遗留问题，每条说清楚为什么不在本次范围内⟩
```

**模板 B · RCA 复盘** — 出自 `rca-encoder-tail-drain-budget.md`（固定四段 + 两个附段）

```markdown
# RCA · ⟨根因的一句话，不是现象的一句话⟩

> 日期：2026-07-28 · 相关 issue：[#266](https://github.com/…/issues/266)
> 状态：**代码侧已修，真机验证未做**（⟨为什么没验：本机无 GPU…⟩）

## 现象
⟨表格：对象 | 结果 | 出处。用 ✅/❌ + 具体数字 + PR 号⟩
⟨末尾一句点出共同点⟩

## 根因
⟨表格：调用点 | 等待对象 | 是否符合预期⟩
⟨然后用散文解释机制链条，并引用相邻 RCA 文档⟩

### 更要命的一点：⟨次级发现独立成 H3⟩

## 修复
1. … 5.  ⟨编号列表，每条写清楚改了什么 + 为什么这么改 + 不变量是什么⟩

### ⟨YYYY-MM-DD 补充⟩
⟨后续发现追加在此，不重写上文⟩

### 代价
⟨量化：内存 158 MB → 224 MB（+66 MB）。目标机 GTX 1060 6GB 可接受⟩

## 未验证（必须真机做）
- **⟨假设⟩**：这是依据…推出的估计，没有实测。若…，新报错会直接给出…
- 本仓 CI 无 GPU，覆盖不到真实编码器行为。

## 相关
- [#232](…) 是**另一个**失败：⟨明确划清与相似问题的边界⟩
```

**模板 C · 证据目录 README** — 出自 `evidence/issue-609-runtime-gates/README.md`

```markdown
# Issue #609 ⟨主题⟩ evidence · 2026-08-10

⟨一段：这个目录里是什么、采集条件是什么、做了什么没做什么⟩
⟨一段：这些证据**证明什么、不证明什么**——原文：
 "`status=pass` proves only the exact exe/hash, … It is not permission to instrument
  or collect content and it is not Definition of Adapted."⟩

| Game | Report | Status | … | Report SHA-256 |
|---|---|---|---|---|
| … | [`xxx-20260810T063945Z.json`](xxx-…json) | pass | … | `6a17934e…` |

⟨末段：证据由哪个脚本产出（带仓内相对链接）、失败样本为何刻意保留、
  独立摘要在 SHA256SUMS.txt⟩
```

证据文件命名：`<slug>-<steam-appid>-<ISO8601 basic UTC 时间戳>.json`，
例 `aoe4-1466860-20260810T063945Z.json`。
证据目录名有两种并存形态：**主题型 `<topic>-YYYYMMDD`**（`depth-vflip-20260805`、
`game-prereq-20260806`、`depth-formal-20260804`）与 **issue 型 `issue-NNN[-slug]`**
（`issue-423`、`issue-457`、`issue-609-runtime-gates`，后者带 slug）。
单文件证据则直接平铺为 `evidence/<topic>-YYYY-MM-DD.md`
（如 `fallout4-steam-2026-07-28.md`）——注意此处日期用连字符，与目录名的紧凑格式不一致。

**模板 D · 实施计划** — 出自 `docs/superpowers/plans/2026-08-13-issue-147-bounded-safe-exit.md`
（全文仅 27 行，红/绿分段，TDD 顺序）

```markdown
# Issue #147 ⟨主题⟩ implementation plan

**Goal:** ⟨一句话，可验收⟩
**Scope:** ⟨列出触及的模块⟩。⟨显式写出不做什么：No installer/release restructuring.⟩

## Red tests
1. ⟨先写哪些会失败的测试⟩
4. Run the named tests through ⟨具体命令⟩ and retain the expected failures before production edits.

## Green implementation
1. … 4. ⟨最小实现步骤⟩

## Installer contract and verification
1. … 5. Obtain an independent P1/P2 review, then commit in Chinese, push,
   and open a ready PR with `Closes #147`.
```

**模板 E · 调研文档头** — 出自根 `docs/*.md`（信息密度极高的引用块）

```markdown
# ⟨主题⟩调研（2026-08-12）

> 日期：2026-08-12
> 仓库基线：`origin/main@627e701c`（本调研开始前已执行 `git pull --ff-only origin main`）
> 业务目标：⟨为什么做这件事⟩
> 性质：**桌面调研**（证据级别逐项标注），**所有配方须过 D7 真机验证后才准量产**
> 调研方式：3-agent web 核验（PCGW / 官方支持页 / 一手配置文件优先），来源 URL 存工作流工件 `wf_83d601da-0d9`
> 证据口径：官方页/一手配置文件/业务方反馈=高；社区多方一致=中；单一来源=待真机
> 前置阅读：[…](…)、[…](…)
```

置信度标记体系（`llm-vendor-*.md` 系列）：`✅ 官方页面抓取核实 ／ ◐ 多源交叉 ／ ⚠️ 单一二手源`。

### 写作风格（读完 5 篇全文后的归纳）

1. **结论前置**：`## 1 · 裁决` 永远是第一节，理由在后。RCA 的 H1 写根因不写现象。
2. **一切带数字与出处**：不写「性能提升明显」，写「单流 4.6 倍，1094 → 1700 KB/s，同时段交叉轮询 4 轮」。
3. **显式区分事实层级**：已落地 / 未落地、设计目标 / 当前实现 / 测试结果、
   「startup-stage facts, not representative-scene evidence」。
4. **代价必须落纸**：每篇裁决都有「代价」「这次没有解决的」段落，且用 `> ⚠️` 强调不可被措辞掩盖。
5. **反向链接密集**：头部引用块几乎总有 3–8 条 `关联：` / `先例：` / `前置阅读：`，用相对路径链到仓内文档、
   issue、PR、迁移文件、甚至具体源码符号（`ml/diogenes_wm/wm01/action_space.py`）。
6. **中文散文 + 英文标识符**：路径、字段名、命令、错误信息一律保持英文原样，不翻译。
7. **`·` 作为 H1 与引用块内的分隔符**（`# 107 · 裁决：…`、`> 日期：… · 性质：…`），全仓一致。
8. **章节号用 `## 1 · ` 或 `## 1. `**，编号文档倾向前者。

---

## 三、对 babel.plus 的可复用结论

### 3.1 来自 Proxy_Skill 的技术结论（可直接作为 babel-relay 的设计输入）

| # | 结论 | 对 babel.plus 的动作 |
|---|---|---|
| 1 | **单流 TCP 拥塞控制是跨境链路的真瓶颈**，加缓冲、换节点、上 mux 都无效 | 默认路径必须是 **Hysteria2 / QUIC（或 TUIC）**，TCP 协议只作兜底 |
| 2 | **一台机器并行三种入口**（CDN / Reality / SS）成本几乎为零，抗封锁收益极大 | 单节点默认部署多协议栈，而不是「一台机一个协议」 |
| 3 | **CDN 路径（cloudflared 出站 + 回源 127.0.0.1）不受 VM IP 封锁影响** | 把 Cloudflare Tunnel 作为**架构必选项**而非可选项；隧道 ingress 托管在 Zero Trust 后台，节点无状态 |
| 4 | **`url-test` 按延迟选路会稳定选错**（延迟同带宽差 5 倍） | 选路策略要按**吞吐**或用 `fallback` 显式排序，别用纯延迟自动组 |
| 5 | **静态 IP 会被逐个探测封锁，换 IP 只是治标** | IP 轮换要脚本化（防火墙按 tag 匹配即可零改动换 IP），但不能作为抗封锁主策略 |
| 6 | **IP 级封锁有可判定的三条取证证据** | 写进 babel-relay 的排障 runbook |
| 7 | **本机 TUN(fake-ip) 会劫持 `dig`/`nc`/`curl`** | 探活必须走内核 API（mihomo delay 接口），不能用系统网络工具 |
| 8 | GCP Free Tier `e2-micro` + Premium Tier 出口，100GB/月 ≈ $23 | 成本模型可直接复用；出口流量是唯一的实际成本项 |

**架构缺口**（Proxy_Skill 没有、babel.plus 若要产品化必须补的）：
IaC（仓库全是手敲 `gcloud` + shell）、多租户与用户密钥分发（当前退化成单密钥共享）、
节点健康监控与自动换 IP、订阅分发服务（当前是本地生成 YAML 手动导入 6 台设备）、
计费与配额、任何形式的测试与 CI。

**可直接搬运的代码资产**：`optimize-network.sh` 的 sysctl 全文；`setup-server.sh` 的 systemd
硬化写法（`LoadCredential` + `DynamicUser` + `ProtectSystem=strict`）；`gen-clash.py` 的
规则分层与 `🤖 AI` 域名表；`ALIASES` + `REQUIRED` fail-closed 的配置校验模式。

### 3.2 来自 Diogenes 的文档体系结论

已有的 `babel-relay/docs/` 骨架（`00-overview` / `01-research` / `02-architecture` /
`03-product` / `04-ops` / `05-adr`）是**按类型分目录**，Diogenes 是**按编号平铺 + 标记区分类型**。
两者不必二选一，建议这样吸收：

**照抄的（低成本、高收益）：**

1. **不用 YAML frontmatter，用 H1 后的引用块承载元信息**。字段固定为
   `日期 / 性质 / 状态 / 关联`，需要时加 `读者 / 前置阅读 / 事实基线 / 裁决人`。
   理由：186 篇文档零 frontmatter 仍然可检索、可 grep，而且元信息对人类可读、不需要工具渲染。
2. **`性质：` 用受控词表**，直接采用 Diogenes 的：`架构裁决 / 调研 / 设计方案 / 排期计划 /
   执行手册 / 证据型核查 / 机制说明 / 复盘`。
3. **`状态：` 必须带日期**，且用能区分成熟度的词：`草稿 / 设计稿 vN / 冻结稿 / 待实施 /
   执行中 / As-Built / 已归档`。
4. **ADR 用「新文档推翻旧文档 + 逐条交代旧理由的落点」**，不删不改不加 DEPRECATED。
   把那句准则写进 `05-adr/README.md`：**一条裁决被推翻时，它的理由不会自动消失。**
5. **每篇裁决/计划强制两个尾节**：`代价` 与 `这次没有解决的`。这是 Diogenes 文档最有价值的习惯。
6. **`evidence/` 独立平面**：证据（探测 JSON、抓包、测速原始输出、SHA256SUMS）与正文分离，
   目录名 `<topic>-YYYYMMDD`，内置 `README.md` 说明**证明什么、不证明什么**。
   babel-relay 会有大量链路实测数据，这个约定几乎是刚需。
7. **`rca-<slug>.md` 独立命名空间 + 固定四段式**（现象 / 根因 / 修复 / 未验证 + 相关）。
   Proxy_Skill §十一 那段实测其实就是一篇 RCA，只是没这么组织。
8. **`architecture.md` 标 `状态：As-Built`，只写已经存在的实现**，规划中的能力不写进去。
   Proxy_Skill 的 `VPN方案设计.md` §九「架构现状（实测核对）」正是在事后补这个分离——
   babel-relay 应该**从一开始就分开**：`02-architecture/as-built.md` 与设计稿分离。
9. **顶层 `ONBOARDING.md` + `README.md` 目录表 + Agent 规则文件（`AGENTS.md` / `CLAUDE.md`）三件套**，
   `AGENTS.md` 负责给 Agent 指路到不会自动装载的目录。

**要调整的（Diogenes 的做法在小项目上不划算）：**

- **不采用全局递增编号**。Diogenes 编号已到 140 且出现重号（三个 `40-`），
  babel-relay 用现成的类型目录 + `NN-` 二级编号更清晰（如 `05-adr/0003-<slug>.md`）。
- **每个目录补一个 `README.md` 作为索引**。Diogenes 靠 `00-overview.md` 和上级 README 内联引用，
  文档到 255 篇时已经很难导航（我这次调研就是先撞进只有 11 个文件的根 `docs/` 才找到主体系的）。
  这是应当避免而不是复制的。
- **不引入 `.CODEX/` 那套多文件 Agent 记忆**，项目规模不匹配；但
  `.CODEX/WORKING_MEMORY.md`（当前状态 / 风险 / 技术债）的概念值得保留为单文件。

**建议的 babel-relay 文档头（合成 Diogenes 模板 A/E）：**

```markdown
# ⟨编号 · ⟩⟨一句话结论式标题⟩

> 日期：YYYY-MM-DD · 性质：**⟨受控词表⟩** · 状态：⟨成熟度 + 日期⟩
> 事实基线：`⟨commit / 实测环境 / 数据来源⟩`
> 关联：[⟨文档⟩](…)、#issue、PR
> ⟨调研类追加⟩ 证据口径：一手实测=高；官方文档=中；社区来源=待验证
```

---

## 附：调研环境与可复现信息

两个仓库已浅克隆在 scratchpad，可直接复查：

- `…/scratchpad/Proxy_Skill`（完整克隆，`--depth=1`，4 个文件全部通读）
- `…/scratchpad/Diogenes`（`--filter=blob:none --sparse`，
  `git sparse-checkout set docs DataMiner/docs .claude/skills .CODEX`）

通读全文的 Diogenes 文档：`DataMiner/docs/107-depth-into-ingestion-plane-decision.md`、
`DataMiner/docs/rca-encoder-tail-drain-budget.md`、
`DataMiner/docs/evidence/issue-609-runtime-gates/README.md`、
`docs/superpowers/plans/2026-08-13-issue-147-bounded-safe-exit.md`、
`DataMiner/AGENTS.md`、`AGENTS.md`、`.CODEX/README.md`、`.CODEX/PROJECT.md`、`README.md`；
另扫描了 186 个 `.md` 的首行与全部 `状态：`/`性质：` 标记。

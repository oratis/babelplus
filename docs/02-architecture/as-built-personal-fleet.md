# 架构现状 · 自用机队（`vpn-*`）

> 日期：2026-09-04 · 性质：**证据型核查** · 状态：**As-Built**（2026-09-04 实查快照，不回改；**2026-09-05 的变化以增补节 §2.1 / §3.3 / §8 记录**，快照本文不改）
> 事实基线：`gcloud` 只读实查（身份 `wangharp@gmail.com`，project `oratis-491316`，2026-09-04）：
> `instances list` / `instances describe` / `addresses list` / `firewall-rules list`；
> 通路矩阵与实测数字来自 `oratis/Proxy_Skill` 的 `VPN方案设计.md` §九–§十一（2026-06-12 / 07-27 / 07-28 三轮）；
> 用量与 CPU 数据来自 2026-09-04 的 Monitoring API 只读查询
> 证据口径：`gcloud` 输出与 Monitoring API = 高；Proxy_Skill 的一手实测 = 高（但**单网络、单时段**）；
> 本文对未实测项一律标 **需实测**
> 关联：[as-built-gcp.md](as-built-gcp.md)（同项目的商用侧清点，§2 记的就是这两台）、
> [ADR 0017](../05-adr/0017-personal-fleet-in-repo.md)（本队进仓的裁决）、
> [personal-fleet-runbook.md](../04-ops/personal-fleet-runbook.md)（怎么运维它）、
> [reference-repos.md §1.5](../01-research/reference-repos.md)（这批实测对 babel.plus 的输入）

---

## 1 · 为什么这份文档要存在

`VPN方案设计.md` 是本项目**一半架构决策的上游事实源**——单流 TCP 拥塞瓶颈、
Hysteria2 的 4.6 倍提升、IP 级封锁的三条取证判据、TUN 劫持导致 `dig`/`nc` 不可信，
全部出自那份文件，且已被 [runbook-node-health](../04-ops/runbook-node-health.md)、
[system-design §3.1](system-design.md)、[ADR 0004](../05-adr/0004-transport-hardening.md) 逐条引用。

但那份文件**把三层东西写在一起**：§一–§八 是 2026-06 的原始方案，
§九–§十 是后来实际长成的样子，§十一 是两轮实测。
三层互相矛盾的地方不少（多用户 vs 单密钥、$0 静态 IP vs 现在要收费、
`us-west1` Free Tier vs 已经撞顶的 CPU），**读者无法分辨哪一层还成立**。

本文只写**当前实际存在**的东西。原始方案不复制，实测数字带日期与条件。

---

## 2 · 计算与网络资源（2026-09-04 `gcloud` 实查）

| 名称 | zone | 机型 | 系统盘 | 创建于 | 外网 IP | 层级 | 状态 |
|---|---|---|---|---|---|---|---|
| `vpn-us` | `us-west1-a` | `e2-micro` | 30 GB | 2026-04-22 | `8.231.52.43` | PREMIUM | RUNNING |
| `vpn-jp` | `asia-northeast1-a` | `e2-micro` | 30 GB | 2026-07-25 | `34.104.192.233` | PREMIUM | RUNNING |

保留静态 IP：

| 名称 | 地址 | 区域 | 层级 | 状态 |
|---|---|---|---|---|
| `vpn-us-ip-v4` | `8.231.52.43` | `us-west1` | PREMIUM | IN_USE |
| `vpn-jp-ip` | `34.104.192.233` | `asia-northeast1` | PREMIUM | IN_USE |

> `-v4` 后缀是**代际记号**：美国节点的 IP 已被封锁并更换过三次
> （`vpn-us-ip-v3` = `34.82.4.35` 于 2026-07-28 因被封释放）。
> **IP 级封锁在这条链路上是已经反复发生过的事实，不是理论风险。**

两台的共同配置（`instances describe`）：

| 项 | 值 | 备注 |
|---|---|---|
| 网络标签 | `vpn-node` | 两台一致 |
| 服务账号 | `2360090741-compute@developer.gserviceaccount.com` | 🔴 **默认 Compute SA，持有 `roles/editor`** |
| 删除保护 | **`false`** | 🔴 两台都没开 |
| 维护策略 | `MIGRATE` | |
| Shielded VM | vTPM ✅ / 完整性监控 ✅ / **Secure Boot：`vpn-us` ✅、`vpn-jp` ❌** | 两台不一致 |
| 快照 / 快照计划 | **零** | 🔴 机内配置无备份 |

---

## 2.1 · 增补 · 2026-09-04 21:14 之后的变化（2026-09-05 实查，不回改上表）

| 时间（CST） | 变化 | 来源 |
|---|---|---|
| 09-04 20:47 | `default` 子网 us-west1 / asia-northeast1 开 VPC Flow Logs（15 min / 采样 0.5） | 用户跑 `optimize-vpn.sh` p0 |
| 09-04 21:14 | 两台 `deletionProtection: true`；快照计划 `vpn-weekly-us` / `vpn-weekly-jp`；2 条 `vpn-*` 告警策略 | 同上 |
| 09-04 21:15 | +防火墙 `vpn-deny-from-bp`（prio 700，`bp-node` → `vpn-node` DENY all）；`default-allow-rdp` disabled | 用户跑 p1 |
| 09-05 05:40 | `vpn-us` stop → 摘默认 SA（空 SA）→ start | 用户跑 p2（只做了 `vpn-us`） |
| 09-05 08:5x | +`vpn-node-sa`（仅 `logging.logWriter` + `monitoring.metricWriter`）；+`vpn-allow-reality-443` / `vpn-allow-hy2-udp443`（B70） | 本仓 PR #46 后续 |
| 09-05 09:42 | `vpn-us`：e2-micro → **e2-small**，SA → `vpn-node-sa`（停机 50 s） | D4 / D6 |
| 09-05 09:44–15:17 | `vpn-jp`：stop → **序列中断，约 5.5 h 不可用** → SA → `vpn-node-sa` → start；内核更新随重启生效 | D6（事故记录见 runbook §6） |
| 09-05 15:33 | +`vpn-sg`（`asia-southeast1-a` / e2-small / STANDARD / `34.2.143.75` / `vpn-node-sa` / 删除保护 on） | `create-vpn-node.sh`，[evidence](../evidence/fleet-node-provision-vpn-sg-20260905/) |

三台的现状（2026-09-05 16:0x `gcloud` 实查 + IAP SSH 只读）：

| 名称 | zone | 机型 | 层级 | 外网 IP | SA | 服务 | 内核 |
|---|---|---|---|---|---|---|---|
| `vpn-us` | `us-west1-a` | **e2-small** | PREMIUM | `8.231.52.43` | `vpn-node-sa` | xray / ssserver / cloudflared active | 6.1.0-52 |
| `vpn-jp` | `asia-northeast1-a` | e2-micro | PREMIUM | `34.104.192.233` | `vpn-node-sa` | xray / ssserver / hysteria-server / cloudflared active | 6.1.0-52（本次重启后） |
| `vpn-sg` | `asia-southeast1-a` | **e2-small** | **STANDARD** | **`34.2.143.75`** | `vpn-node-sa` | xray（REALITY :443）/ hysteria-server（udp :443）active；无 SS、无 CDN | 6.1.0-52 |

`vpn-sg` 机内自检（`setup-vpn-node.sh` 尾部 + 只读复核）：Xray 26.3.27；`tcp_congestion_control=bbr` / `default_qdisc=fq`；
Hysteria2 自签证书 `CN=www.bing.com`，到期 2036-09-02；无 `reboot-required`。**机内配置本次是脚本写入的，不再是转述**。

⚠️ 上表 §2 里「服务账号 = 默认 Compute SA、删除保护 false、零快照」三条自 2026-09-04 21:14 起已不成立，保留原文是为了记录当时的实况。

## 3 · 🔴 防火墙：自用队的 443 入向挂在**没有 target tag** 的规则上

`firewall-rules list` 实查（2026-09-04），按优先级排：

| 规则 | 优先级 | 动作 | 端口 | 来源 | **target tag** |
|---|---|---|---|---|---|
| `vpn-iap-ssh-allow` | **500** | ALLOW | tcp:22 | `35.235.240.0/20` | `vpn-node` |
| `vpn-public-ssh-deny` | **600** | **DENY** | tcp:22 | `0.0.0.0/0` | `vpn-node` |
| `allow-ss-48882` | 1000 | ALLOW | tcp+udp:48882 | `0.0.0.0/0` | `vpn-node` |
| `allow-xray-443` | 1000 | ALLOW | tcp:443 | `0.0.0.0/0` | 🔴 **无** |
| `allow-hysteria-udp443` | 1000 | ALLOW | udp:443 | `0.0.0.0/0` | 🔴 **无** |
| `allow-iap-ssh` | 1000 | ALLOW | tcp:22 | `35.235.240.0/20` | 🔴 **无**（被 `vpn-*` 那两条覆盖，冗余） |
| `bp-iap-ssh-allow` | 900 | ALLOW | tcp:22 | `35.235.240.0/20` | `bp-node` |
| `bp-public-ssh-deny` | 1000 | DENY | tcp:22 | `0.0.0.0/0` | `bp-node` |
| `bp-allow-reality-443` | 1000 | ALLOW | tcp:443 | `0.0.0.0/0` | `bp-node` |
| `bp-allow-hy2-udp443` | 1000 | ALLOW | udp:443 | `0.0.0.0/0` | `bp-node` |
| `default-allow-ssh` | 65534 | ALLOW | tcp:22 | `0.0.0.0/0` | 无 |

### 3.1 SSH 姿态：正确

`vpn-iap-ssh-allow`(500) < `vpn-public-ssh-deny`(600) < `default-allow-ssh`(65534)。
两个不等式都成立 —— IAP 隧道通，公网 22 被压制。与 `bp-` 那一套同构（900 < 1000 < 65534）。

### 3.2 🔴 但 443 入向是一条**跨机队的隐式耦合**，而方向与 `bp-` 相反

[`infra/node/README.md` §4](../../infra/node/README.md) 与
[as-built-gcp §3](as-built-gcp.md) 都写过：`allow-xray-443` / `allow-hysteria-udp443`
**没有 target tag**，因此对 `default` 网络里每一台实例生效，
而 as-built §3 的处置建议本身就是「**给这两条补 target tag 做收敛**」。

`create-node.sh` 为此明知冗余仍建了 `bp-allow-reality-443` / `bp-allow-hy2-udp443`
两条带 `bp-node` 标签的规则，**所以商用队对那次收敛免疫**。

**自用队没有对应的两条。** 也就是说：

> **一旦有人执行了那个（正确的）安全收敛动作，`vpn-us` 与 `vpn-jp` 会毫无预警地
> 瞬时失去 443 入向 —— REALITY 与 Hysteria2 六条通路里的四条同时断。**
> 而现象（443 无响应、服务端零入站连接、进程 `active`）
> **与 [runbook-node-health §3](../04-ops/runbook-node-health.md) 的 IP 级封锁三判据完全吻合**，
> 排障会走到「释放 IP、重开机器」上去 —— 那是一条完全错误的路径。

**处置**：建 `vpn-allow-reality-443`（tcp:443）与 `vpn-allow-hy2-udp443`（udp:443），
绑 `--target-tags=vpn-node`，优先级 1000。与 `bp-` 那两条同法、同理由。
**这是纯增量、零影响的动作**（同端口同来源，只是多一条按标签命中的路径），
但它**不在本次范围内**——见 [runbook §1.2](../04-ops/personal-fleet-runbook.md)。

---

### 3.3 · 增补 · 2026-09-05 的规则集（13 条非 bp-）

在 §3 的 10 条之上：`vpn-deny-from-bp`（700，`bp-node` → `vpn-node` DENY all，2026-09-04 21:15）、
`vpn-allow-reality-443` / `vpn-allow-hy2-udp443`（1000，target `vpn-node`，2026-09-05，roadmap B70）；`default-allow-rdp` 已 disabled。
**§3.2 那条跨机队隐式耦合已由 B70 关掉**：自用队的 443 入向现在有自己的 tagged 路径。
期望清单进了 [`infra/fleet/fleet.json`](../../infra/fleet/fleet.json)，由 `verify-isolation.sh` 守（2026-09-05 24/24 绿）。

## 4 · 实测数字（各自带日期与条件，不要跨条件引用）

### 4.1 七条通路的延迟（2026-07-28 01:20 CST，客户端为北京联通的 Mac）

| 通路 | 协议 / 入口 | 延迟 | 状态 |
|---|---|---|---|
| JP-HY2 | Hysteria2 + salamander，UDP:443 | **100 ms** | ✅ 默认路径 |
| JP-Reality | VLESS + REALITY，TCP:443 | 95 ms | ✅ |
| JP-SS | SS-2022，48882 | 148 ms | ✅ |
| JP-CDN | VLESS+WS，Cloudflare Tunnel `jp.gptwiki.net` | 325 ms | ✅ 抗封锁保险，非日常 |
| US-Reality | VLESS + REALITY，TCP:443 | 253 ms | ✅ 换 IP 后恢复 |
| US-SS | SS-2022，48882 | 232 ms | ✅ 换 IP 后恢复 |
| US-CDN | VLESS+WS，Cloudflare Tunnel `cdn.gptwiki.net` | 279 ms | ✅ |

> **延迟不是选路依据。** 同一批测量里各健康节点的延迟全落在 95–325 ms 这一条噪声带内，
> 而吞吐差 4–5 倍 —— 所以 `url-test`（按延迟排序）会**稳定地选中慢的那条**。
> 默认组因此指向 `fallback` 类型的区域组而不是 `⚡ Auto`。

### 4.2 吞吐：瓶颈是单流拥塞控制，不是带宽（2026-07-27）

| 并发 | 经 JP-SS 下载 Cloudflare 测速文件 |
|---|---|
| 单流 | 132 KB/s |
| 8 并发 | **1,093 KB/s 聚合（8.3×，近线性）** |

**这次测量是在 sysctl 调优（`99-proxy-network.conf`，BBR / 16 MiB 缓冲 / MTU 探测 / TFO）
已经生效之后跑的。** 所以那套调优「正确且无害，但不要期待吞吐提升」。

换协议之后（同时段交叉轮询 4 轮，每轮三节点各下 5 MB，中位数）：

| 节点 | 协议 | 单流吞吐中位 |
|---|---|---|
| **JP-HY2（Brutal）** | Hysteria2 / QUIC UDP | **~1,700 KB/s** |
| JP-HY2（BBR） | Hysteria2 / QUIC UDP | 1,094 KB/s |
| JP-SS | SS-2022 / TCP | 370 KB/s |
| JP-Reality | VLESS+REALITY / TCP | 269 KB/s |

**单流 4.6 倍**；BBR 模式的 1,094 KB/s **恰好等于此前 8 条 TCP 流并发才凑出的聚合值**——
这是「瓶颈就是 TCP 单流拥塞控制」最直接的一条证据。

推论（三条，都已被 babel.plus 采纳）：
① 加大服务端缓冲区不解决；② 换节点无用（US 与 JP 单流同在 150–310 KB/s）；
③ **mux/smux 有害**（多个逻辑流塞进同一条 TCP 连接，受同一个单流上限约束，还引入队头阻塞）。

### 4.3 🔴 `vpn-us` 已撞 CPU 天花板（2026-09-04，Monitoring API）

`e2-micro` 的 `reserved_cores = 0.25`。取吞吐最高的 5 个小时：

| 出网吞吐 | 该小时 CPU 利用率 |
|---|---|
| 29.2 Mbps | 111% |
| 28.1 Mbps | 108% |
| 26.8 Mbps | 100% |
| 23.5 Mbps | 75% |
| 22.1 Mbps | 100% |

**上限约 29 Mbps，且已经在打满突发额度。**
`vpn-jp` 相反：40.8 Mbps 时 CPU 才 65% —— **不是 CPU 瓶颈**。

> ⚠️ **这条与 §4.2 的结论并不冲突，但很容易被读混：**
> §4.2 说的是**单条 TCP 流**的上限（拥塞控制），§4.3 说的是**整机聚合**的上限（CPU）。
> 升级机型能抬高后者，**对前者无效**。
> 因此「`vpn-us` 升到 `e2-medium` 会不会更快」取决于用户实际是单流还是多流负载，
> **需实测**（复审条件写在 [ADR 0017 §8 代价第 2 条](../05-adr/0017-personal-fleet-in-repo.md)）。

### 4.4 用量与成本（2026-09-04，14 天窗口）

| 项 | 值 |
|---|---|
| 出网合计 | `vpn-us` 624 GiB + `vpn-jp` 431 GiB = **1,055 GiB / 14 天** |
| 日均 | **75 GiB/日** |
| 折月 | 约 **2.3 TiB/月** |
| 按实收混合单价 $0.0979/GiB 估 | 约 **$221/月 gross** |

> ⚠️ **与 [egress-billing-20260820 §4.1](../evidence/egress-billing-20260820/) 的外推不一致，
> 而这个不一致本身是信息**：那份证据在 2026-08-17→08-20 的四天里测到日均 **256 GiB**
> （是本窗口的 3.4 倍），按那个速率外推是 $764/月。
> **两者都不是错的 —— 那四天的阶跃没有持续。**
> 但它证明了这条链路的用量**可以在几天内翻几倍且无人察觉**，
> 而当时没有任何机制会告诉任何人。这正是日报要解决的问题。

---

## 5 · 客户端侧的现状与它的痛点

`gen-clash.py` 从 `.secrets.env` + `.secrets-jp.env` 读凭据，
渲染 **6 份静态 YAML** 到 `clash-configs/`（mac / iphone / ipad / laptop / windows / spare），
每份包含全部七条通路 + 三个分组（`🇺🇸 United States` fallback、`🇯🇵 Japan` fallback、`⚡ Auto` url-test）。

**痛点，也就是本轮要解决的第 2 项：**

> **节点地址是烧进 YAML 里的常量。** 换一次 IP（已经发生过三次）意味着：
> 重跑生成器 → 把 6 份文件分别送到 6 台设备 → 每台手工重新导入 → 重载配置。
> 而**换 IP 恰好发生在链路已经不通的时候**，这时候把新配置送到手机上本身就是个问题。

其余三条现状记录：

- 凭据文件有过一次**变量名不一致**导致生成器长期无法运行的历史
  （`CDN_ADDR` / `CDN_WSPATH` / `JP_IP` vs 脚本期望的规范名），现由 `ALIASES` 表兼容。
  **新增变量一律用规范名。**
- SS-2022 是**单密钥**，全部设备共用一个密码 —— 与原方案 §4.4 设计的 EIH 多用户不同。
  单设备泄漏 = 六份配置全部作废。
- 两台节点的 `config.json` **属主/权限不同**（`vpn-jp` 是 `root:root 600`，
  `vpn-us` 是 `ssserver:ssserver 400`）。改文件后必须各自还原，
  **否则 systemd 起不来且不会自动重试**。

---

## 6 · 与商用队（`bp-*`）的对照

| 维度 | 自用队 `vpn-*` | 商用队 `bp-*` |
|---|---|---|
| 台数 | 2（`us-west1` / `asia-northeast1`） | 1（`bp-node-hk1`，`asia-east2-a`） |
| 机型 | `e2-micro` ×2 | `e2-small` |
| 网络层级 | **PREMIUM** | **STANDARD**（[ADR 0008](../05-adr/0008-network-tier-standard.md)） |
| 服务账号 | 默认 Compute SA（`roles/editor`） | `bp-node-sa`（**零角色**） |
| 删除保护 | ❌ | ✅ |
| 443 防火墙 | 🔴 靠无 tag 规则（§3.2） | ✅ 自带 `bp-` tagged 规则 |
| 服务端 | 手装 xray + ssserver + hysteria + cloudflared | v2node **v0.4.3 钉死** |
| 配置下发 | 本地生成器 → 静态文件 | 面板 `GET /api/v1/server/UniProxy/config` |
| 订阅 | 无（手工导入） | `bp-api` `/client/subscribe`，带 `profile-update-interval` |
| 计量 | 无 | `stat_user_server` + `/push` 上报 |
| 巡检 | **无** | 告警策略若干（多数未接信号源） |

> **两队各有对方缺的东西。** 自用队有七条实测可用的通路和三年积累的排障判据；
> 商用队有订阅下发、计量、告警策略与建机脚本的守卫逻辑。
> [ADR 0017](../05-adr/0017-personal-fleet-in-repo.md) 让两边共享**工具**，
> 本表则是共享之后应当互相补齐的清单。

---

## 7 · 本文没有覆盖的

- [x] ~~**机内实际配置未清点**~~ 2026-09-05 经 IAP SSH 只读实查三台（§2.1）：服务、监听端口、hysteria 配置结构（值打码）、证书到期、sysctl、`reboot-required`。`healthcheck.sh` 每小时把这些落进 KV。仍未清点：xray / ssserver 配置文件的完整内容、各服务版本号（除 `vpn-sg`）。
- [ ] ~~**机内实际配置未清点**~~（原文保留）：本次是纯 GCP 侧只读实查，**没有登录过任何一台机器**。
      `/etc/xray`、`/etc/hysteria`、`/etc/shadowsocks` 的实际内容、
      各服务的版本号、sysctl 的当前生效值，全部来自 `VPN方案设计.md` 的**转述**，
      **不是本次实测**。第一次跑 `healthcheck.sh` 时应当把这些落进 evidence。
- [ ] **Cloudflare 侧未清点**：两条隧道（`cdn.gptwiki.net` / `jp.gptwiki.net` +
      隧道 `vpn-jp-ws` = `54a649cb-30b0-4f0e-8276-94307a737af5`）的 ingress 规则
      托管在 Zero Trust 后台，**本仓库没有它们的副本**。
      这与 [roadmap B33](../00-overview/roadmap.md)（CF 账号资产未清点）是同一个缺口。
- [ ] **`vpn-jp` 的 Secure Boot 为什么关着，没有查**。两台不一致本身值得一个解释。
- [ ] **入向路由从未测过。** §4.1 的延迟是客户端侧观测，
      [`infra/node/README.md` §5](../../infra/node/README.md) 说得很清楚：
      **入向由中国运营商的 BGP 决策决定，而用户体验由入向决定。**
      自用队从来没做过数据源 B（国内多点测速站）。

## 8 · 增补 · 2026-09-05 之后仍未覆盖的

- [ ] `vpn-sg` 的入向路由与 Standard/Premium 对照未测（与 §7 末条同一个缺口，现在多了一台）。
- [ ] `vpn-sg` 无快照计划、无 SS / CDN 通路。
- [ ] `vpn-us` e2-small 的 4 轮交叉测未做（ADR 0017 §8 代价 2）。
- [ ] Cloudflare 侧（两条隧道 + 新增的 Worker `fleet-sub` / KV）仍未进 as-built-gcp 的清点。

# evidence · 证据平面

实测原始数据：测速输出、抓包、探活 JSON、截图、SHA256SUMS。
**与正文文档分离** —— 结论写进正文，原始数据留在这里。

## 约定

- 目录名：`<topic>-YYYYMMDD`，例 `hy2-throughput-20260816/`
- 每个证据目录**必须有 `README.md`**，写清楚：
  - 采集条件（时间、网络、运营商、工具版本）
  - **这些证据证明什么、不证明什么** ← 最重要的一节
- 二进制/大文件附 `SHA256SUMS.txt`
- **失败样本要保留**，不要只留成功的

## 已完成

| 证据目录 | 解决了什么 |
|---|---|
| [gcp-egress-pricing-20260817](gcp-egress-pricing-20260817/) | **B2** GCP 出口单价（Billing Catalog API 权威价目）。Standard $0.11/GiB + 200GiB/区域/月免费；Premium $0.23/GiB 无免费额度 |
| [ipv6-censorship-20260817](ipv6-censorship-20260817/) | **ADR 0008 §3 的决定性反证**：OONI 一手测量（AS9808 中国移动，2026-08-07，`workers.dev`）里 v4 与 v6 的 TCP 443 **都通**、TLS 握手 **都被 RST** —— 「IPv6 在中国受干扰更少」（ADR 0004 §3.7 的唯一支柱，原文自标待核实）**被证伪**。⚠️ **本行 2026-08-29 补登记**：目录 2026-08-17 就随 ADR 0008 进了仓库，但一直没有 README、也没进本表，见下方「登记欠账」一节 |
| [cloudrun-healthz-intercept-20260817](cloudrun-healthz-intercept-20260817/) | **Cloud Run 的 Google Frontend 拦截 /healthz**，请求不进容器。探活路径改为 /-/healthz |
| [v2node-contract-20260817](v2node-contract-20260817/) | **B6** 鉴权形态、**B18** 两个字段语义、**B16** 设备计数口径、ADR 0006 的 ETag 前提。全部靠读源码解决，无需真实节点 |
| [network-tier-implementation-20260820](network-tier-implementation-20260820/) | **ADR 0008 落地**：既有 vpn-us/vpn-jp 全为 PREMIUM（关闭 0008 §6 遗留项）；Standard 在 asia-east2 实测可用；IPv6 只支持 PREMIUM 是 API 硬约束 |
| [egress-billing-20260820](egress-billing-20260820/) | **出口账单的 SKU 级拆分**（pricing §7 点名的定价前置）。到中国大陆的 SKU 用量为 0，中国方向实走 Carrier Peering $0.080–0.085/GiB；全部 gross 被账单账户级推广抵扣吸收，本项目现金支出约 $6 |
| [v2node-401-behavior-20260821](v2node-401-behavior-20260821/) | **B7** 关闭：401/403 **不会**清空用户列表（三重保护），但会让**重启**失败且不自愈；`alivelist` 对 ≥399 静默返空 map。仍是读源码解决 |
| [gcp-inventory-20260821](gcp-inventory-20260821/) | **B9**（run.app 证书签发者是 GTS）、**B12**（Cloud SQL 四细节里的三项 + 三条新发现）、**B32**（预算建得了，缺的是口径）；生产冒烟 6 条；监控现状：log-based metrics 曾经一条都没有 |
| [client-config-validation-20260822](client-config-validation-20260822/) | 用容器里的**真实客户端**（mihomo v1.19.30 `-t` / sing-box v1.13.19 `check`）校验订阅产出。🔴 头号发现：`GEOIP,CN` 拿不到数据库时**整份配置被拒绝加载**，据此把它从下发规则里去掉（B46）。另确证 `sing-box check` 对缺 `inbounds` 的配置**通过**（B45 只能真机验），并测出 SS-2022 客户端密码必须是恰好 16 字节的 base64。⚠️ 目录随 PR #13 一起进 master |
| [node-provision-bp-node-hk1-20260901](node-provision-bp-node-hk1-20260901/) | **第一台自有节点建出来之后的清点**：建了哪些资源、有没有碰到别的东西（`verify-isolation.sh` 与 as-built §7 的清点命令实跑）。只回答「长什么样」，不回答「路由好不好」 |
| [node-route-methodology-20260901](node-route-methodology-20260901/) | 🔴 **推翻既有验收方法**：`verify-route.sh` 的 ICMP 判据（J1 跨洋绕行 / J2 中位 RTT / J3 丢包）**不可靠** —— 中国三网对 ICMP 的处理与 TCP 不同，据此把三条降级为参考值、另加 TCP 握手探测（B55）。同一份数据**证实**了 ADR 0008 的 Standard 选择 |
| [node-route-bp-node-hk1-20260901](node-route-bp-node-hk1-20260901/) | `35.215.158.52` 的路由采样原始输出（mtr / ping 打中国三网，**含不合格样本**）与 J1/J2/J3/J6 的自动判定 |
| [node-bringup-20260901](node-bringup-20260901/) | **第一条 REALITY 通路端到端接通**，以及**四条只有真机才会撞上的缺陷**；ADR 0004 §3.3 的 mux 裁决被本文推翻；B15 有了实测判定。计量口径 +0.3% 的基线也在这里 |
| [adr0014-alerts-hy2-20260902](adr0014-alerts-hy2-20260902/) | ADR 0014 落地 + **HY2 通路接通** + **又三条真机缺陷**（含 B63「节点上只剩一个用户时封禁永不生效」）。三态计时见 §6 |
| [e0-metering-20260904](e0-metering-20260904/) | 🔴 **E0 的答案是否定的（B66）**：100 MiB 经 HTTPS 代理入站走完，`stat_user_server` **一个字节都没变**；**同一节点同一时间窗**的 REALITY 正对照 20 MiB 正常入账（+0.21%）—— 所以不是「表没在动」，是那条路径**根本没有账**。扩展这条传输就此停在门口，`getUserProxyConfig` 保持 501。三条出路都要写代码、都要一次裁决 |

> **2026-08-29 登记欠账（自查）：本表此前只有 8 行，而 `docs/evidence/` 下有 9 个目录。**
> 漏掉的是 `ipv6-censorship-20260817/` —— 它 2026-08-17 随 ADR 0008 一起入库，
> 被 [ADR 0008](../05-adr/0008-network-tier-standard.md) 与
> [ADR 0011](../05-adr/0011-domain-blackout-detection.md) 当作事实基线引用，
> 却既没有本目录约定强制的 `README.md`，也没有出现在这张表里，
> 于是 [docs/README.md](../README.md) 的证据目录计数长期写成「6 个」（实际 9 个）。
> **两件事已于 2026-08-29 补上**：补写 `ipv6-censorship-20260817/README.md`、
> 把 `docs/README.md` 的计数改成 9 并逐个列名。
> 教训与本节下面那条经验同源：**约定里「每个目录必须有 README」这一条没有任何机制在执行**，
> 靠的是写目录的人自觉，而这次就漏了一个。

> **2026-09-04 再一次登记欠账（同一个洞，第二次掉进去）：本表此前有 9 行，而目录下有 14 个。**
> 漏的是 2026-09-01/02 那五个（`node-provision-bp-node-hk1` / `node-route-methodology` /
> `node-route-bp-node-hk1` / `node-bringup` / `adr0014-alerts-hy2`）——
> 它们**都有 README、都被 roadmap 正文当事实基线引用**，只是没进这张表，
> `docs/README.md` 的计数也仍停在「9 个」。本次连同 `e0-metering-20260904` 一起补齐到 **15 行**，
> 并把 `docs/README.md` 改成 15。
> 🔴 **两次的根因一样：这张表是手写的，没有任何东西在核对它与 `ls docs/evidence/*/` 是否一致。**
> 上一次（2026-08-29）写了教训但没做机制，于是六天后又漏了五个 —— **「写下教训」不是机制**。
> ✅ **这次补了机制**：[`infra/scripts/check-evidence-index.sh`](../../infra/scripts/check-evidence-index.sh)
> 在 CI 的「文档约定（证据目录）」作业里跑，三条判据全部致命 ——
> ①每个目录有 `README.md`；②每个目录在本表里被链接到；③本表链到的目录都存在（防改名死链）。
> 它**刻意不检查行数、顺序与每行写了什么** —— 那些是判断不是事实，一个会误报的检查很快就会被人绕过去。
> 所以「证明什么 / 不证明什么」那一条仍然只能靠评审的人看。

> **五次下来同一条经验，越来越硬：大量标着「需实测」的条目其实是「没读源码 / 没查 API / 没跑一条 gcloud」。**
> 在租机器测之前，先穷尽「读开源代码」「查厂商 API」「查自己账上的实况」这三条零成本路径。
> B7 曾被登记为「必须起真实容器」，最后是 20 分钟的源码阅读关掉的；
> B9 是一条 `openssl`，B12/B32 各是一条 `gcloud describe`。

## 当前待采集（全部为 P0 阻塞项）

- [x] ~~`egress-cost-*` — GCP 出口单价~~ **✅ 已完成** → [gcp-egress-pricing-20260817](gcp-egress-pricing-20260817/)
      （单价已定；实际账单核对于 2026-08-20 补做，结论写在 [pricing §2](../03-product/pricing-and-plans.md) 与
      [as-built-gcp §10.3](../02-architecture/as-built-gcp.md)。
      **2026-08-21：本条登记的两笔欠账已还清** → [egress-billing-20260820](egress-billing-20260820/)
      —— SKU 级拆分做完了，BigQuery 原始数据与全部查询也落进了目录。
      ⚠️ 同时修正两处口径：完整到 08-20 的实际值是 **3,399.0 GiB / $332.91**
      （§10.3 的 2,927 / $294.12 是跑数当天的部分日快照，作为 As-Built 快照不回改）；
      且**这是 gross，现金支出约 $6**，其余被推广抵扣吸收。）
- [ ] `protocol-throughput-*` — REALITY vs Hysteria2，电信/联通/移动 × 晚高峰
- [ ] `region-ab-*` — **asia-east2**（香港，ADR 0004 §3.5 裁定的主力）vs asia-northeast1
      （2026-08-21 改正区域名，原写 asia-east1 —— 即 roadmap B40）
- [ ] 🔴 `nettier-ab-*` — Standard vs Premium 网络层级。
      **2026-08-20 起这一项的性质变了**：实查确认线上一直跑在 **Premium**
      （[as-built-gcp §10.4](../02-architecture/as-built-gcp.md)），
      [ADR 0008](../05-adr/0008-network-tier-standard.md) 从未实施 ——
      所以这不再是「选型调研」，而是**一个已经在花钱的现状要不要改**的取舍：
      成本可能降，代价是 Standard 的回程路径质量对代理类产品直接影响体感。
      **2026-08-21 更新：它点名的「需先做 SKU 级拆分」已经做完**
      → [egress-billing-20260820 §2.1](egress-billing-20260820/)。
      拆开之后省钱空间**比原来估的小**：层级敏感的只有 Internet DTO 那 1,483.7 GiB
      （43.6%），另外 54.6% 走 Carrier Peering 是另一套 SKU；
      ADR 0008 引用的 2.09× 是目录价之比（$0.23/$0.11），而实收从来不是 $0.23。
      **这项性能实测仍然是那个决定的前置条件，而且现在收益上界更低了，
      即「先测性能再决定」的理由更强。**
      **2026-08-29 校正上面「ADR 0008 从未实施」那半句**（保留原文不删，它是 2026-08-20 当天的实况）：
      PR #10（2026-08-21 合并）把 0008 落到了**新节点**上 —— `infra/node/create-node.sh:71` 现在硬编码
      `NETWORK_TIER="STANDARD"`，`:712` 建完再读回断言一次（一手实查）。
      所以准确的表述是 **「新节点已 Standard，既有 `vpn-us` / `vpn-jp` 仍是 Premium 且不迁」**
      （见本表 [network-tier-implementation-20260820](network-tier-implementation-20260820/)
      与 [ADR 0008](../05-adr/0008-network-tier-standard.md) 头部的「已实施（仅新节点）」）。
      ⚠️ **这不改变本条待采集项的性质**：自有 Standard 节点目前是 **0 台**，
      在花钱的仍然是那两台 Premium 老节点，所以「Standard 的性能数据」依旧是零，
      「一个已经在花钱的现状要不要改」这个取舍也依旧原封不动。
- [ ] `email-deliverability-*` — QQ/163/126/Sina 送达率
- [ ] `domain-reachability-*` — 候选托管平台与域名的三网可达性（连续一周，覆盖晚高峰）

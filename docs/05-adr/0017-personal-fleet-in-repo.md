# 0017 · 裁决：自用机队以「同仓不同队」形态进入本仓库；共享设计与工具，不共享任何一份 GCP 资源

> 日期：2026-09-04 · 性质：**架构裁决** · 状态：**已批准（修订版）**（2026-09-05，用户裁决 D1–D7 全按评审推荐；修订内容与执行记录见 §10）
> 事实基线：`gcloud` 只读实查（`instances list` / `describe` / `addresses list` /
> `firewall-rules list`，身份 `wangharp@gmail.com`，2026-09-04）；
> 价目来自 [evidence/fleet-pricing-20260904](../evidence/fleet-pricing-20260904/)（Billing Catalog API）；
> 实收单价来自 [evidence/egress-billing-20260820](../evidence/egress-billing-20260820/)（BigQuery 账单导出）；
> 飞书应用凭据已实测可用（`auth/v3/tenant_access_token/internal` 返回 `code:0`，
> `bot/v3/info` 返回 `app_name: 胖狗`、`activate_status: 2`）
> 关联：[0002 §4](0002-notification-channels.md)（本文**补一格**：内部运维通道；用户面裁决**原样不动**）、
> [0007](0007-node-migration.md)（§4/§8 拿 `vpn-jp` 当回滚落点 —— **本文撤销那一条**）、
> [0008](0008-network-tier-standard.md)（Standard 层级；本文给它的 A/B 找到了执行载体）、
> [0015](0015-client-strategy.md)（客户端策略）、
> [as-built-personal-fleet.md](../02-architecture/as-built-personal-fleet.md)（自用队现状）、
> [personal-fleet-runbook.md](../04-ops/personal-fleet-runbook.md)（三项能力的执行手册）
> 用户指令基线：2026-09-04「`vpn-us` / `vpn-jp` 是私人自用节点，babel.plus 不得复用」；
> 同日「把 Proxy_Skill 的完整设计纳入 babel plus 项目」并选定**同仓不同队**

---

## 1 · 裁决

**一、`oratis/Proxy_Skill` 的设计、运维手册与工具链并入本仓库，但两支机队在 GCP 上保持物理隔离。**

「并入」的范围**只有三样**：文档、脚本、以及脚本读取的机队清单格式。
**不包括**：实例、静态 IP、防火墙规则、服务账号、订阅通路、计费归属、用户流量。

| | **自用队** | **商用队** |
|---|---|---|
| 命名前缀 | `vpn-*` | `bp-*` |
| 网络标签 | `vpn-node` | `bp-node` |
| SSH 压制链 | `vpn-iap-ssh-allow`(500) < `vpn-public-ssh-deny`(600) | `bp-iap-ssh-allow`(900) < `bp-public-ssh-deny`(1000) |
| 服务对象 | 用户本人与其设备 | babel.plus 的付费用户 |
| 面板 | **无**（订阅由本地生成器渲染 + 边缘下发） | v2node ← `bp-api` 的 UniProxy 五端点 |
| 订阅来源 | `infra/fleet/gen-subscription.py` | `api/internal/subgen` |
| 网络层级 | Premium（现状）→ 见 §5 的 A/B | Standard（[ADR 0008](0008-network-tier-standard.md)） |
| 谁付钱 | 用户本人 | 项目 |

**二、`vpn-jp` 不再是 babel.plus 的回滚落点。** [ADR 0007 §4 / §8](0007-node-migration.md)
把它定为「第一次节点切换的人工回滚落点」——**本文撤销这一条**。
回滚落点改为**第二台 `bp-node-*`**；在第二台建成之前，babel.plus 的节点切换**没有回滚落点**，
这是一个必须被看见的缺口，不是一个可以用自用机器填掉的坑。

**三、飞书（应用「胖狗」）成为自用队的内部运维通道，且只做内部运维。**
它填的是 [ADR 0002 §4](0002-notification-channels.md) 那张图里 `M4[内部运维告警]` 那一格
（原文写的是 "Slack / Pub-Sub → 值班"）。
**0002 关于用户面通知的裁决一个字都不改** —— 邮件仍是唯一的失联恢复通道。

**四、机队目标形态：3 台付费速度节点（us / jp / sg），月度成本上限 $500（credit 计入成本）。** ~~+ 1 台免费层运维节点~~
（2026-09-05 修订 D2：`vpn-ops` 推迟——Worker 承担聚合与日报，节点互探代替跨节点回打；复审条件见 runbook §4 代价 5。）
拓扑与预算见 §4，逐条执行见 [personal-fleet-runbook](../04-ops/personal-fleet-runbook.md)。

---

## 2 · 为什么这条值得单独裁决

因为**两条用户指令表面上互相矛盾，而按任一条的字面意思单独执行都会做错事**：

> 2026-09-04 (a)：「vpn-us / vpn-jp 是私人自用节点，babel.plus 不得复用。」
> 2026-09-04 (b)：「把 Proxy_Skill 的完整设计纳入 babel plus 项目。」

如果把 (b) 读成"合并基础设施"，就违反 (a)，后果是**付费用户的流量落到用户自用的机器上**——
而 `vpn-us` 已经实测撞到 CPU 天花板（[as-built-personal-fleet §4.2](../02-architecture/as-built-personal-fleet.md)），
抢的是他本人的带宽。

如果把 (b) 读成"什么都别做"，就等于拒绝了一件本来该做的事：
**Proxy_Skill 的实战结论已经是本项目一半架构决策的事实基线**
（[reference-repos §1.5](../01-research/reference-repos.md)、
[runbook-node-health §0/§3](../04-ops/runbook-node-health.md)、
[system-design §3.1](../02-architecture/system-design.md)），
而它本身**没有文档纪律、没有证据目录、没有 As-Built 与设计的分离**——
`VPN方案设计.md` 一份文件里同时躺着 2026-06 的原始方案、后来实际部署的形态、和两轮实测，
读者无法分辨哪一层还成立。**它是本项目最重要的上游事实源，却是纪律最差的一份文档。**

裁决把两条读法拆开：**知识与工具共享，资源与流量不共享。**

---

## 3 · 隔离怎么被强制，而不是靠自觉

已有的 `infra/scripts/verify-isolation.sh` 把两台自用机的 zone / 机型 / IP / RUNNING
与 10 条防火墙规则写死成硬期望。本裁决对它提两条**扩展要求**（实现列在 §9）：

1. **清单从硬编码改为读 `infra/fleet/fleet.json`**，否则每加一台自用节点，
   隔离脚本就会因为"多了一台没见过的机器"而误报，
   于是**必然会有人把它改成宽松匹配**——那一刻隔离就名存实亡。
2. **加一条反向断言**：`bp-*` 命名的资源不得带 `vpn-node` 标签，
   `vpn-*` 命名的资源不得带 `bp-node` 标签，且两组防火墙规则的 target tag 不得交叉。
   现状实查已满足（2026-09-04），但**没有任何东西在守它**。

另外三条边界，靠文件布局而不是靠约定：

- 自用队的订阅生成器是 `infra/fleet/gen-subscription.py`，**不 import `api/internal/subgen`**。
  两者渲染的是同一批协议，但商用侧的字段变更**不允许**静默改变用户本人的配置。
- 自用队的凭据文件 `infra/fleet/.secrets*.env` 进 `.gitignore`，
  与 `bp-*` 的 Secret Manager 条目**不共用任何一个名字**。
- 自用队**不接 v2node、不接面板、不装 cloudflared 的 babel.plus 隧道 token**。

---

## 4 · 机队目标形态与 $500 预算

### 4.1 拓扑

| 节点 | 区域 | 机型（2026-09-05 修订 D4） | 角色 | 免费层 |
|---|---|---|---|---|
| `vpn-us` | `us-west1`（俄勒冈） | **`e2-small`**（原案 `e2-medium`；先测再升，见 §8 代价 2） | 美国出口（AI 账号 IP 一致性） | 否（2026-09-05 起不再是 `e2-micro`） |
| `vpn-jp` | `asia-northeast1`（东京） | **`e2-micro` 不升**（as-built §4.3：40.8 Mbps 时 CPU 65%，不是 CPU 瓶颈） | 默认高吞吐路径（Hysteria2） | 否 |
| `vpn-sg` | `asia-southeast1`（新加坡） | **`e2-small`** 起步（原案 `e2-medium`） | **2026-09-05 已建**；东南亚出口 + **唯一一份** Standard 200 GiB 免费出网（us/jp 是 Premium，没有） | 否 |
| ~~`vpn-ops`~~ | ~~`us-central1`~~ | ~~`e2-micro`~~ | **推迟（D2）**：Worker 承担聚合与日报；只剩「BigQuery 拉 $ 实收」「订阅静态兜底」两件事时再建 | （Always Free 那台 `e2-micro` 暂不占用） |

**为什么运维节点单独一台、而且必须是那台免费的：**
Always Free 的 `e2-micro` 全账户只有一台份，而它 0.25 vCPU 的计费规格**跑不动代理流量**
（`vpn-us` 已实测撞顶，见 as-built §4.2）。把它用在**不承载业务流量**的角色上，
免费额度才真正被兑现；反过来，为了保住 $6.11/月而让美国出口继续跑在 `e2-micro` 上，
是用 1.2% 的预算去换掉一条主力通路的速度。

### 4.2 月度成本（目录价，730 h；credit 计入成本）

| 项 | 明细（2026-09-05 修订后的实际形态） | $/月 |
|---|---|---|
| 计算 | `e2-small` us-west1 12.23 + `e2-micro` 东京 7.84 + `e2-small` 新加坡 15.09 | **35.16** |
| 盘 | 30 GiB × 3 台：美国 30 GiB 落免费档 $0；东京 30 × 0.052 = 1.56；新加坡 30 × 0.044 = 1.32 | **2.88** |
| 外网 IPv4 | 3 个常驻 2,190 h − 720 h 免费 = 1,470 h × $0.005（口径已定，价目 §4.1） | **7.35** |
| **固定小计** | | **$45.39** |
| **出口预算余量** | $500 − $45 | **$454.61** |

> 原案（e2-medium ×3 + vpn-ops）固定小计 $101.09；修订后少 $55.70/月，全部换成出口余量。

出口余量换算成流量（两种口径都给，因为层级还没定）：

| 口径 | 单价依据 | 可承载 | 折日均 |
|---|---|---|---|
| 全部按 Premium 实收（保守上界） | 实收混合 **$0.0979/GiB**（[egress-billing §2](../evidence/egress-billing-20260820/)） | 4,644 GiB/月 | **153 GiB/日** |
| 实际混合（us/jp Premium + sg Standard） | sg 那一份**只有 200 GiB 免费**，之后 APAC $0.11；us/jp 按实收 $0.0979 | 约 4,700–4,800 GiB/月（取决于 sg 分到多少流量） | **≈ 155 GiB/日** |

> ⚠️ 2026-09-05 修正：原表按「3 个区域各 200 GiB 免费」算 Standard 行，与 §4.3 裁决（us/jp 保持 Premium）自相矛盾——
> 免费额度只有 `vpn-sg` 一份，原表多算了 400 GiB/月（≈ $44）。

**当前实测用量：75 GiB/日**（2026-09-04，14 天窗口 1,055 GiB，`vpn-us` 624 + `vpn-jp` 431）。

> **结论：目标形态在 $500 上限内有 79%–97% 的余量。**
> ⚠️ 但有一个必须记住的反例：[egress-billing §4.1](../evidence/egress-billing-20260820/)
> 记录过 2026-08-17→08-20 的四天里日均冲到 **256 GiB**，按那个速率外推是 **$764/月，直接爆表**。
> 那次阶跃至今**没有解释**（标准账单导出没有 `resource` 字段，归不到实例）。
> **所以 $500 不是靠算出来的余量守住的，是靠 §6 的日度用量闸守住的。**

### 4.3 网络层级：把它变成一次测量，而不是再一次拍板

[ADR 0008](0008-network-tier-standard.md) 为 `bp-node` 选了 Standard，
但它自己写着前置的 `nettier-ab-*` A/B **至今没做**；
Proxy_Skill 则选了 Premium，理由是"跨太平洋延迟稳定性"，同样**没有对照数据**。

按目录价算的分水岭（APAC 源区域，Premium 按实收 $0.0979、Standard 按 200 GiB 免费 + $0.11）：

```
0.0979·X = 0.11·(X − 200)   →   X = 1,818 GiB/月/区域
```

**低于每区域每月约 1.8 TiB，Standard 更省；高于则 Premium 更省。**
按 §4.2 的规划量（3 个区域分摊 4 TiB ≈ 每区域 1.3 TiB），**Standard 更省**——
但省下的钱换的是什么速度，**没有人测过**。

**裁决：`vpn-sg` 以 Standard 建，`vpn-jp` 保持 Premium 不动，构成同期对照。**（✅ 2026-09-05 `vpn-sg` 已按此建成：`34.2.143.75`，STANDARD；对照采样**未做**。）
这既是新节点的选型，也顺手把挂了一个多月的 `nettier-ab-*` 变成有载体的实验。
判据与采样窗口沿用 [`infra/node/verify-route.sh`](../../infra/node/verify-route.sh) 的 J1–J6，
含一次晚高峰（19:00–24:00 CST）。

> ⚠️ **这个对照是不干净的**：新加坡与东京既差层级也差地理位置。
> 它能回答的是"新加坡 Standard 这条通路好不好用"，**不能**回答"层级本身值不值"。
> 要回答后者，得在**同一区域**开两个 IP 各挂一个层级——那是 `nettier-ab-*` 的正题，本文不代做。

---

## 5 · 订阅热更新：裁决的是"哪一层是事实源"

用户要的是"用 Shadowrocket 或 Clash Verge 时不用重新加载配置就能更新连接地址"。

**裁决：机队清单（`fleet.json` + 凭据）是唯一事实源，客户端配置降级为对订阅 URL 的一次性引用。**
换 IP / 加节点 / 删节点，**只改服务端**。

但**必须诚实地说清楚三种客户端拿到的不是同一种东西**：

| 客户端 | 机制 | 换地址时用户要做什么 | 是否真的"不重载" |
|---|---|---|---|
| Clash Verge Rev / mihomo 系 | **`proxy-providers`**（`type: http` + `interval` + `health-check`），`proxy-groups` 用 `use:` 引用 | 什么都不做 | ✅ **真热更新**，内核后台拉取并就地替换，配置不重载 |
| sing-box 系 | 订阅（远程 profile）+ 客户端自身的自动更新 | 什么都不做 | 🔶 后台替换 profile，**是重载**，只是用户无感 |
| Shadowrocket | 订阅 URL + 「后台自动更新」 | 什么都不做 | 🔶 同上；**Shadowrocket 不支持 `proxy-providers`** |

> 🔴 **"不用重新加载配置"这个字面要求，只有 mihomo 系能满足。**
> 另外两种能做到的是"用户零操作"，不是"零重载"。
> 把这两件事说成一件，会在第一次故障排查时误导人——
> 因为**重载会断连接，而 provider 热更新不会**。

配套的三条硬要求：

1. **订阅下发必须带 `profile-update-interval` 与 `subscription-userinfo` 响应头。**
   商用侧 `api/internal/handler/subscription.go:774` 已经这么做了；自用侧照抄同一组头，
   连大小写都照抄（那份实现有一条注释专门解释为什么不能用 `http.Header.Set` 规范化）。
   ⚠️ **Shadowrocket 是否读 `profile-update-interval`：需实测。**
2. **订阅的第一位是公告伪节点**（`KindNotice`，商用侧 `subgen` 已有的设计），
   节点名里写当前订阅域名。这是订阅域名被封时唯一还能触达用户的通道
   —— 与 [ADR 0002 §4.1](0002-notification-channels.md) 的第 2 条恢复路径同源。
3. **订阅托管必须独立于节点。** 托管在任何一台代理节点上，
   会在"节点挂了"和"节点换了地址"这两种最需要它的时刻同时失效。
   落点：Cloudflare Worker `fleet-sub` + KV（✅ 2026-09-05 已部署）。~~自定义域走既有 `gptwiki.net` zone~~ → **2026-09-05 修订 D5：用独立 zone**——`cdn.gptwiki.net` / `jp.gptwiki.net` 两条抗封锁保险通路在同一个 zone，订阅与保险不能共用一个失效面；也不用 `babel.plus`。域名**待定**，定前只有 workers.dev（大陆不可达，只当发布验证）。`vpn-ops` 静态兜底随 D2 推迟。

---

## 6 · 每日巡检与飞书日报：它同时是成本闸

**裁决：巡检在每台节点上跑，汇总与发送在 Cloudflare Worker 上跑（2026-09-05 修订 D2，原案 `vpn-ops`），日报走飞书自定义机器人 Webhook 发到「只有用户 + 机器人」的群（2026-09-05 修订 D3，原案应用「胖狗」私聊）。**

不选"每台机器各发一条"的理由：4 台机器 = 每天 4 条卡片，
而**最重要的那条信息恰恰是"某台没发"**——把"缺席"变成一条正向可读的行，
比让人每天数卡片数量可靠得多。

日报必须包含的**五组**（逐项判据见 [runbook §4](../04-ops/personal-fleet-runbook.md)）：

1. **进程与端口**：各协议服务 `active`、端口 bind。
2. **IP 封锁三判据**（照搬 [runbook-node-health §3](../04-ops/runbook-node-health.md)）：
   进程活着 + 443 上零公网 established + 数小时零日志 → 疑似 IP 级封锁。
   ⚠️ 这三条**在节点自身上只能测到前两条**，第三条要跨节点互探，所以 `vpn-ops` 要对其余节点回打。
3. **月度用量与预算**：每节点月累计 GiB、占本月配额百分比、按当前速率的月末外推。
   **这是 $500 上限的唯一执行点**——GCP 的 budget 只会**告警**，不会**停机**。
4. **证书与到期**：Hysteria2 的自签证书、以及任何 LE 证书的剩余天数。
5. **待重启**：`/var/run/reboot-required`（`unattended-upgrades` 关了自动重启，
   [`infra/node/README.md` §9](../../infra/node/README.md) 自记这条巡检项"不存在"，本文补上）。

**凭据处置（本裁决的一条硬红线，2026-09-05 按 D3 修订）：**
日报通道改为自定义机器人 Webhook 之后，**运维链路上不再需要 App Secret**：Worker 只持有 webhook URL 与签名 secret
（`wrangler secret`），本机 gitignored `.secrets.env` 留一份；**任何一台代理节点上都没有任何飞书凭据**。
应用「胖狗」的 App Secret 只在本机 `.secrets.env`（备用通道），代理节点是暴露面最大的资产，
而它们需要的只是"把 JSON 交出去"的能力，不需要"以胖狗身份发消息"的能力。

> 🔴 **本次对话中 App Secret 以明文出现在聊天记录里。**
> 落库前后都不该有它，但**它已经离开过密码管理器**——
> 建议在飞书后台重置一次 App Secret，把新值直接写进 `.env` / Secret Manager，不要再经过任何对话。

---

## 7 · 被本文推翻或修正的既有内容

| 出处 | 原文 | 本文的处置 |
|---|---|---|
| [ADR 0007 §4 / §8](0007-node-migration.md) | `vpn-jp` 作为第一次节点切换的人工回滚落点 | **撤销**。改为第二台 `bp-node-*`；在它存在之前，**回滚落点为空**，缺口显式登记 |
| [ADR 0002 §4](0002-notification-channels.md) 图中 `M4` | 内部运维告警走 "Slack / Pub-Sub → 值班" | **补一格**：飞书私聊为自用队的日报通道。用户面通道**不动** |
| `VPN方案设计.md` §二 | 「静态外部 IP 附加在运行中 VM 上 = **$0**」 | **已过时**。目录里 in-use IPv4 超 720 h/月后 $0.005/h，见 [价目 §4](../evidence/fleet-pricing-20260904/) |
| `VPN方案设计.md` §三 | 「出站 100 GB Premium ≈ $23」（按 $0.23/GiB） | **与实收不符**。实测混合 $0.0979/GiB，中国方向落 Carrier Peering 而非 `to China` SKU |
| `VPN方案设计.md` §4.4 | SS-2022 EIH 多用户，1 iPSK + 5 uPSK | **从未按此部署**（§9.3 自陈是单密钥）。本文不恢复它，理由见 §8 代价第 4 条 |
| [`infra/node/README.md` §9](../../infra/node/README.md) | 「待重启节点的巡检项不存在」 | **由 §6 第 5 组补上**（自用队先落地，商用队照抄） |
| [ADR 0008](0008-network-tier-standard.md) | Standard，前置 A/B 未做 | 不推翻。§4.3 给 A/B 找到执行载体，并明说这个对照**不干净** |

---

## 8 · 代价

> ⚠️ 这一选择的代价，必须留在记录里而不是被措辞掩盖：
>
> 1. **"同仓不同队"把一条社会性约束伪装成了技术性约束。** 隔离现在靠的是命名前缀、
>    网络标签和一个只读脚本——**没有任何一层是 GCP 强制的**。两支机队同在
>    `oratis-491316` 一个 project、同一个 VPC、同一个计费账号下，
>    一条 `--target-tags` 打错的命令就能让付费用户的流量落到自用机器上，
>    而 `verify-isolation.sh` **要等到下一次有人跑它才会发现**。
>    真正的隔离是两个 GCP project，代价是跨项目的运维复杂度与一次迁移。**本文没有付这个代价。**
> 2. **`vpn-us` 升到 `e2-medium` 会永久失去 Always Free 的 `e2-micro` 额度**，
>    每月 $6.11。换来的是解掉一条实测的 29 Mbps 天花板。
>    **如果实测显示升级后吞吐没有改善**（即瓶颈不在 CPU 而在跨太平洋单流拥塞，
>    这正是 Proxy_Skill §11.3 已经证明过的一件事），**这 $6.11 就是白花的**，
>    应当降回 `e2-micro` 并把速度问题交还给协议层（Hysteria2）。
>    **复审条件写死：升级后同法交叉轮询 4 轮，若单流吞吐中位提升 < 20%，回退。**
> 3. **新增 `vpn-sg` 与 `vpn-ops` 把机队从 2 台变成 4 台，运维面翻倍。**
>    4 台机器 = 4 份 unattended-upgrades、4 份证书、4 个可能被封的 IP、
>    4 份需要人去重启的内核更新。**而运维仍然是一个人。**
>    日报是对这条代价的部分补偿，不是消除。
> 4. **不恢复 SS-2022 的 EIH 多用户，意味着单设备密钥泄漏时只能全队轮换。**
>    原方案 §4.4 设计过按设备分 uPSK，实际部署成了单密钥（§9.3）。
>    本文选择不修，因为 SS-2022 在当前七条通路里是**兜底路径**，
>    而把 EIH 补回来要改服务端配置、生成器和全部客户端配置。
>    **代价是：一台设备丢了，六份配置全部作废。**
> 5. **日报把成本闸建在"每天看一眼"上。** 它拦不住一次持续 6 小时的异常放量
>    ——[egress-billing §4.1](../evidence/egress-billing-20260820/) 那次 4 倍阶跃
>    如果发生在两次日报之间，$500 里会有相当一部分在无人知晓时被烧掉。
>    真正的硬闸是节点侧限速（`tc` / `wondershaper`）或自动停机，**本文没有做**，
>    因为两者都会在误判时切断用户本人的网络。
> 6. **飞书是一个企业租户，而这是私人流量。** 胖狗当前所在的 6 个群都是有其他人的工作群
>    （`产品小队` / `组织建设讨论` / `HakkoAI专项` / `产品+UI` / `龙虾养殖场` /
>    `Github权限和PR Review`，`tenant_key=1740dbc1c7459740`）。
>    本文把投递限定为私聊，但**应用凭据本身仍是企业租户的资产**——
>    租户管理员可以看到这个应用、可以停用它、审计日志里会有它的调用记录。
>    **在私人 VPN 的节点 IP 与流量数据上，这不是一个中立的选择。**
> 8. **（2026-09-05 追加）workers.dev 子域名叫 `oratisoratisoratisoratis`。** 交互式注册脚本把名字重复输入了四次；它只是 D5 定下独立域名前的临时地址，可在后台改。
> 9. **（2026-09-05 追加）`vpn-us` 升到 `e2-small` 而不是 `e2-medium`，代价是如果 `e2-small` 也顶了要再停一次机。** 换来的是每月少 $12.23、且不用为一个未证明的收益先付双倍。
> 10. **（2026-09-05 追加）改现役节点的停机序列没有看门狗。** `vpn-jp` 的 stop → 换 SA → start 在 stop 之后被 gcloud 连接中断打断，09:44–15:17 CST 无人重启，**约 5.5 h 不可用**。这是执行事故，不是设计代价，但它暴露的是设计缺口：序列应当是一个带重试/回滚的脚本。
> 7. **飞书对失联恢复毫无价值。** 与 [ADR 0002 §3.1](0002-notification-channels.md) 对
>    Telegram 的判定同构——飞书在中国大陆可达，所以它比 Telegram 好；
>    但**当代理全线中断时，用户本人的飞书客户端仍然是可达的**，这一点是它成立的全部理由。
>    **它不解决"节点全挂时怎么拿到新配置"** ——那仍然要靠订阅 URL 与公告伪节点。

---

## 9 · 这次没有解决的

- [x] ~~🔴 **本文是提案，未批准。**~~ ✅ 2026-09-05 用户按修订批准（D1）；同日建成 `vpn-sg`、升级 `vpn-us`、两台换 SA、Worker 上线（§10）。
- [x] ~~🔴 **`verify-isolation.sh` 的两条扩展（§3）没有实现**~~ ✅ 2026-09-05 已做：期望读 `infra/fleet/fleet.json`（入库，D7），反向断言 + 守 `vpn-deny-from-bp` 的正向断言；本地 23/23 绿。⚠️ 它在 2026-09-04 21:15 就已经红了（用户加了 `vpn-deny-from-bp`），本次先修它再建机。
- [x] ~~🔴 **外网 IPv4 的计费口径未定**~~ ✅ 2026-09-05 定论（[价目 §4.1](../evidence/fleet-pricing-20260904/)）：`External IP Charge on a Standard VM`，$11/月口径成立。
- [ ] **Carrier Peering 对新加坡是否同价，是按一个观测样本外推的。**
      `vpn-sg` 第一份账单出来要回头核对 [价目 §2.3](../evidence/fleet-pricing-20260904/)。
- [ ] **`e2-small` 能不能解掉 29 Mbps 天花板，没测过。**（2026-09-05 改为 e2-small 先行，D4）复审条件在代价第 2 条；4 轮交叉测**仍未做**，要在用户设备上跑。
- [ ] **Shadowrocket 是否读 `profile-update-interval` 响应头：需实测。**
      同样未测的还有它对 `subscription-userinfo` 的显示行为。
- [ ] **`proxy-providers` 的 `health-check` 在 Clash Verge Rev 当前版本里的确切键名：需实测。**
      与 [api-contract §4.5](../02-architecture/api-contract.md) 对 `smux` / `reality-opts`
      标「需实测」同理——**按文档实现，用真实客户端加载一次才算数**。
- [ ] **飞书 App Secret 的轮换没做**（§6 末尾）。它在 2026-09-04 对话中以明文出现过。D3 之后它不在运维链路上，但泄漏就是泄漏，仍要重置。
- [ ] ~~**用户本人的飞书 `open_id` 未取得**~~ → D3 之后不需要 `open_id`；**需要的是用户建「只有本人 + 自定义机器人」的群并取得 webhook URL / 签名 secret**（2026-09-05 仍未取得，日报发送路径未跑）。
- [ ] **节点侧限速 / 自动停机没做**（代价第 5 条）。日报只有观测，没有执行。
- [ ] **Oracle Cloud Always Free（4 OCPU / 24 GB Ampere A1 + 10 TB/月免费出网）未评估。**
      它是唯一能把出口成本结构整体改写的免费层，但会推翻 2026-08-25 的
      「全部用 GCP + Cloudflare」平台裁决，且 Oracle IP 段在代理场景下的封锁速度**待核实**。
      **要不要开这条口子是一次独立裁决，不在本文范围内。**
- [ ] **两支机队仍在同一个 GCP project**（代价第 1 条）。拆项目未评估。
- [x] ~~**`vpn-*` 两台仍挂默认 Compute SA，而该 SA 持有 `roles/editor`**~~ ✅ 2026-09-05（D6）三台全部挂 `vpn-node-sa`（只有 `logging.logWriter` + `monitoring.metricWriter`）。⚠️ `vpn-us` 在 05:40 由用户的 `optimize-vpn.sh` p2 先摘成空 SA，09:42 与升机型合并一次停机换成 `vpn-node-sa`；`vpn-jp` 的停机序列失控 5.5 h（代价 10）。
- [x] ~~**`vpn-*` 两台 `deletionProtection: false`、零快照、零快照计划**~~ ✅ 2026-09-04 21:14 用户跑 `optimize-vpn.sh` p0：删除保护 on、每周快照 `vpn-weekly-us/jp`、Flow Logs on、2 条告警。`vpn-sg` 建机即带删除保护；**它的快照计划未建**。

---

## 10 · 2026-09-05 修订与执行记录

用户裁决（D1–D7 全按评审推荐）：

| # | 裁决 | 落地 |
|---|---|---|
| D1 | 按修订批准本 ADR | 本节 |
| D2 | `vpn-ops` 推迟 | Worker 承担 ingest + cron 日报；节点互探；`fleet.json .deferred` 记录复审条件 |
| D3 | 飞书改自定义机器人 Webhook | Worker 直接 POST（签名）；**webhook 待用户建群取得** |
| D4 | `vpn-us` 先 `e2-small`、`vpn-jp` 不升、`vpn-sg` 从 `e2-small` 起步 | ✅ 09:42 `vpn-us` 升级；`vpn-sg` 15:33 建成 |
| D5 | 订阅用独立 zone | **域名待定**；Worker 暂在 workers.dev |
| D6 | 换 SA 与升机型合并一次停机；SA = `vpn-node-sa` | ✅ 三台全部 `vpn-node-sa`（`vpn-jp` 序列失控 5.5 h，代价 10） |
| D7 | `fleet.json` 入库 | ✅ 入库；`verify-isolation.sh` 读它；`fleet.example.json` 删除 |

当日执行（全部有 gcloud 只读实查或脚本输出背书）：B70 两条规则、`vpn-node-sa`、B69 隔离脚本 23/23 绿、Worker `fleet-sub` + KV、
订阅四产物 + 自托管 CN CIDR 发布并 `curl` 验头、healthcheck 三台装机并上报、`vpn-sg` 建机 + 装机、
B71 定论、出网归因证据、`vpn-jp` 内核更新随重启生效。**未做**：飞书首条真实消息、独立域名、真机客户端加载、4 轮交叉测、`vpn-sg` 路由验收、App Secret 重置。

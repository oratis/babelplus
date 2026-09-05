# 运维手册 · 自用机队的扩容、订阅热更新与每日巡检

> 日期：2026-09-04 · 性质：**执行手册** · 状态：**v2，三节均已在真机上执行**（2026-09-05；执行记录与偏差见 §6，
> 仍未做的见 §5。原 v1 设计稿 2026-09-04 写成时三节全部未执行）
> 事实基线：[as-built-personal-fleet.md](../02-architecture/as-built-personal-fleet.md)（现状）、
> [evidence/fleet-pricing-20260904](../evidence/fleet-pricing-20260904/)（目录价）、
> [evidence/egress-billing-20260820](../evidence/egress-billing-20260820/)（实收价）
> 关联：[ADR 0017](../05-adr/0017-personal-fleet-in-repo.md)（本文是它的可执行形式）、
> [runbook-node-health.md](runbook-node-health.md)（**排障归它管**，本文只管例行）、
> [`infra/node/README.md`](../../infra/node/README.md)（商用队的同类手册，本文大量复用它的守卫逻辑）、
> [`infra/fleet/README.md`](../../infra/fleet/README.md)（脚本使用说明）
> 读者：机队的运维者（当前是用户本人）。**三节互相独立，可以分别执行。**

---

## 0 · 三件事与它们的依赖顺序

```
§1 扩容 ──────────────────┐
   vpn-sg（新，Standard） │
   vpn-ops（新，免费层）  ├──▶ §3 每日巡检与飞书日报
   vpn-us 升机型          │      （聚合器跑在 vpn-ops 上）
                          │
§2 订阅热更新 ────────────┘
   （不依赖 §1，可以先做）
```

**§2 应当先做。** 理由：扩容会**改变节点地址集合**，
而在订阅通路建成之前，每加一台机器都意味着再手工分发一次 6 份配置。
先把事实源收敛到一处，扩容才是加一行 JSON 的事。

✅ [ADR 0017](../05-adr/0017-personal-fleet-in-repo.md) 已于 2026-09-05 按修订批准（D1–D7），§1 的改动同日执行。

---

## 1 · 扩容：从 2 台到 4 台

### 1.1 目标拓扑

| 节点 | 区域 | 机型（D4 修订） | 层级 | 角色 | 状态（2026-09-05） |
|---|---|---|---|---|---|
| `vpn-us` | `us-west1-a` | `e2-micro` → **`e2-small`**（顶了再议 `e2-medium`） | Premium | 美国出口 | ✅ 09:42 已升级（停机 50 s） |
| `vpn-jp` | `asia-northeast1-a` | **`e2-micro` 不升**（不是 CPU 瓶颈） | Premium | 默认高吞吐 | ✅ 只换 SA（序列失控 5.5 h，§6） |
| `vpn-sg` | `asia-southeast1-a` | **`e2-small`** 起步 | **Standard** | 东南亚出口 + A/B 对照 | ✅ 15:33 建成 `34.2.143.75`，15:4x 装机 |
| ~~`vpn-ops`~~ | ~~`us-central1-a`~~ | ~~`e2-micro`~~ | — | 巡检聚合 + 日报 → **Worker 承担** | **推迟（D2）** |

选型理由与月度成本见 [ADR 0017 §4](../05-adr/0017-personal-fleet-in-repo.md)。三条要点：

1. **Always Free 的 `e2-micro` 全账户只有一台份**，给了 `vpn-ops` 就没了。
   把它用在不跑业务流量的角色上，是唯一能真正兑现这份额度的用法
   （免费出网额度 1 GiB/月且**明确排除中国大陆**，对代理流量价值为零）。
2. **多开一个区域 = 多一份 200 GiB/月的 Standard 免费出网额度**（[价目 §2.2](../evidence/fleet-pricing-20260904/)）。
3. **`vpn-sg` 用 Standard、`vpn-jp` 保持 Premium**，构成同期对照
   —— 顺手给挂了一个多月的 `nettier-ab-*` 找到载体。⚠️ 这个对照**不干净**，见 ADR 0017 §4.3。

### 1.2 🔴 前置：先补两条防火墙规则，再碰任何实例

[as-built-personal-fleet §3.2](../02-architecture/as-built-personal-fleet.md) 的实查结论：
**自用队的 443 入向挂在 `allow-xray-443` / `allow-hysteria-udp443` 这两条没有 target tag
的规则上。** 一旦有人按 [as-built-gcp §3](../02-architecture/as-built-gcp.md) 的建议
给它们补上 target tag 做收敛，`vpn-us` / `vpn-jp` 会**瞬时失去 443 入向**，
而现象与 IP 级封锁的三条取证特征完全吻合。

```bash
P=oratis-491316
gcloud compute firewall-rules create vpn-allow-reality-443 --project=$P \
  --network=default --direction=INGRESS --action=ALLOW \
  --rules=tcp:443 --source-ranges=0.0.0.0/0 --target-tags=vpn-node --priority=1000
gcloud compute firewall-rules create vpn-allow-hy2-udp443 --project=$P \
  --network=default --direction=INGRESS --action=ALLOW \
  --rules=udp:443 --source-ranges=0.0.0.0/0 --target-tags=vpn-node --priority=1000
```

**纯增量、零影响**（同端口同来源，只是多一条按标签命中的路径），
和 `create-node.sh` 为 `bp-node` 建那两条冗余规则是同一个理由、同一个写法。

### 1.3 🔴 前置：扩展 `verify-isolation.sh`

现有脚本把**两台**自用机的 zone / 机型 / IP / RUNNING 写死成硬期望。
加第三、第四台会让它**误报**，而误报的必然结局是有人把它改宽松 —— 那一刻隔离就名存实亡。

两条扩展（[ADR 0017 §3](../05-adr/0017-personal-fleet-in-repo.md)）：

- 期望清单改从 `infra/fleet/fleet.json` 读，而不是硬编码。
- 加**反向断言**：`bp-*` 资源不得带 `vpn-node` 标签，`vpn-*` 资源不得带 `bp-node` 标签，
  两组防火墙规则的 target tag 不得交叉。

**这一条必须在建 `vpn-sg` 之前做完，不能并行。**

### 1.4 建机

`infra/node/create-node.sh` 的守卫逻辑（SSH 姿态硬闸、静态 IP 网段预筛、zone 避开 `-b`、
建机即刻验收）全部适用，但它**硬编码了 `bp-` 前缀、`bp-node` 标签与 `STANDARD` 层级**。
自用队用 `infra/fleet/create-vpn-node.sh`——它是同一套守卫、不同的前缀与标签集，
且**允许显式选择网络层级**（因为自用队要跑 A/B，而 `bp-` 那边是 ADR 0008 明令不给开关的）。

```bash
cd infra/fleet
./create-vpn-node.sh --node vpn-sg  --region asia-southeast1 --zone asia-southeast1-a \
    --machine-type e2-medium --network-tier STANDARD --dry-run
./create-vpn-node.sh --node vpn-ops --region us-central1 --zone us-central1-a \
    --machine-type e2-micro   --network-tier STANDARD --no-proxy-ports --dry-run
```

`--no-proxy-ports` 让 `vpn-ops` **不开 443/48882 入向** —— 它不承载代理流量，
开着只是白白扩大暴露面。

**建机检查清单**照抄 [`infra/node/README.md` §7](../../infra/node/README.md)，两处替换：
`bp-` → `vpn-`、`bp-node` → `vpn-node`。**两个不等式仍然要逐条核**：

- `vpn-public-ssh-deny`(600) < `default-allow-ssh`(65534)
- `vpn-iap-ssh-allow`(500) < `vpn-public-ssh-deny`(600)

### 1.5 升级 `vpn-us` / `vpn-jp` 的机型

```bash
P=oratis-491316
gcloud compute instances stop  vpn-us --project=$P --zone=us-west1-a
gcloud compute instances set-machine-type vpn-us --project=$P --zone=us-west1-a \
    --machine-type=e2-medium
gcloud compute instances start vpn-us --project=$P --zone=us-west1-a
```

⚠️ **要停机。** 保留静态 IP 不变（这正是当初预留它的理由），但**连接会全断**。

🔴 **先测再升。** [as-built §4.3](../02-architecture/as-built-personal-fleet.md) 证明
`vpn-us` 撞的是**整机 CPU** 天花板（29 Mbps / 111% CPU），
而 §4.2 证明**单条 TCP 流**的瓶颈在跨太平洋拥塞控制、与 CPU 无关。
**这两个上限是不同的东西**，升机型只抬高前者。
[ADR 0017 §8 代价第 2 条](../05-adr/0017-personal-fleet-in-repo.md) 写死了复审条件：

> 升级后同法交叉轮询 4 轮，若单流吞吐中位提升 < 20%，**回退到 `e2-micro`**。

### 1.6 成本闸：GCP 的 budget 只告警，不停机

项目上已有一条 $500/月、`EXCLUDE_ALL_CREDITS` 口径的预算（2026-08-21 建）。
**它只发邮件。** 真正的执行点是 §3 的日报里那一组用量行。

按 [ADR 0017 §4.2](../05-adr/0017-personal-fleet-in-repo.md) 的预算表，
出口余量 $398.91/月 → **按当前 14 天实测的 75 GiB/日，余量 79%–97%**。
配额取保守值：

| 阈值 | 月度 GiB | 日均 GiB | 日报里的表现 |
|---|---|---|---|
| 软阈值 | 3,000 | 100 | 🟡 提示，附各节点占比 |
| 硬阈值 | 3,800 | 127 | 🔴 告警 + 建议动作（切低价区域 / 降级机型 / 暂停某节点） |

⚠️ **两个阈值都拦不住一次持续几小时的异常放量。**
[egress-billing §4.1](../evidence/egress-billing-20260820/) 记录过日均 256 GiB
持续四天的阶跃且**至今没有解释**。日报是观测，不是执行 —— 见 §4 代价第 3 条。

---

## 2 · 订阅热更新：让换 IP 不再需要碰客户端

### 2.1 裁决要点：三种客户端拿到的不是同一种东西

用户要的是「用 Shadowrocket 或 Clash Verge 时**不用重新加载配置**就能更新连接地址」。
这个字面要求**只有 mihomo 系能满足**，必须说清楚：

| 客户端 | 机制 | 用户要做什么 | 真的不重载？ |
|---|---|---|---|
| **Clash Verge Rev / mihomo 系**（Stash、ClashX Meta） | **`proxy-providers`**：`type: http` + `interval`，`proxy-groups` 用 `use:` 引用 | 什么都不做 | ✅ **是**。内核后台拉取、就地替换节点集合，**配置不重载、已有连接不断** |
| **sing-box 系**（SFI / SFA / Karing / Hiddify） | 远程 profile + 客户端自动更新 | 什么都不做 | 🔶 **否**。后台替换整份 profile 并重载，只是用户无感 |
| **Shadowrocket** | 订阅 URL + 「后台自动更新」 | 什么都不做 | 🔶 **否**，同上。**Shadowrocket 不支持 `proxy-providers`** |

> 🔴 **把"用户零操作"和"零重载"说成一件事，会在第一次排障时误导人** ——
> 重载会断掉正在进行的连接，provider 热更新不会。
> 用户看到的现象不同：前者是"视频卡了一下"，后者是"什么都没发生"。

### 2.2 产物：一份清单，四种渲染

事实源是 `infra/fleet/fleet.json`（**非机密拓扑**：节点名、区域、IP、端口、协议、排序）
加上 gitignored 的 `.secrets*.env`（UUID / 密码 / PSK / REALITY 公钥 / short id）。

`infra/fleet/gen-subscription.py` 从这两样渲染出四份产物：

| 产物 | 给谁 | 说明 |
|---|---|---|
| `mihomo-provider.yaml` | Clash Verge Rev | **只有 `proxies:` 一个顶层键**，这是 provider 文件的格式 |
| `clash.yaml` | 首次导入 / 不想用 provider 的人 | 完整配置，含 DNS / 分组 / 规则；分组用 `use:` 指向 provider |
| `singbox.json` | sing-box 系 | ⚠️ 必须含 `inbounds` 与 `route.rules`，否则官方客户端"开关打开却不走流量"（[roadmap B45](../00-overview/roadmap.md)） |
| `base64.txt` | Shadowrocket / 兜底 | 分享链接逐行 base64 |

**三条从商用侧 `api/internal/subgen` 照抄的硬约束**（同样的坑不踩第二次）：

1. **Hysteria2 不下发 `up`/`down`** —— 声明带宽就是启用 Brutal，而 Brutal 有 100% 可分的特征
   （[ADR 0004](../05-adr/0004-transport-hardening.md) 裁决 1）。
   ⚠️ 与 `VPN方案设计.md` §11.4 建议的 `up: 20 Mbps / down: 60 Mbps` **相反**——
   那是为速度，这是为不被识别。**自用队按速度优先可以保留 Brutal，但要知道代价。**
2. **TCP 路径（REALITY）启用 mux，UDP 路径（Hysteria2）不启用。**
   ⚠️ 但 [`infra/node/README.md` §6.3](../../infra/node/README.md) 记着一条**未核实**：
   mux 与 XTLS-Vision 能否共存不明。而 §4.2 的实测又说 mux 对本链路有害。
   **自用队默认关 mux**，理由是实测优先于裁决。
3. **不下发 `GEOIP,CN`** —— mihomo 会为它去下 8.6 MB 的 MMDB，
   而需要它的人正是"人在大陆、还没有可用代理"的那一刻（[roadmap B46](../00-overview/roadmap.md)）。
   国内直连改用 `RULE-SET` 或 IP-CIDR 明细。

### 2.3 托管：必须独立于节点

🔴 **不能托管在任何一台代理节点上。** 那会让它在"节点挂了"和"节点换地址了"
这两种最需要它的时刻同时失效。

| 层 | 落点 | 免费额度 |
|---|---|---|
| 主 | **Cloudflare Worker `fleet-sub` + KV**（✅ 2026-09-05 已部署）。自定义域按 D5 用**独立 zone**（不挂 `gptwiki.net`：与 CDN 保险通路同失效面；不挂 `babel.plus`）——**域名待定**，定前只有 workers.dev（大陆不可达，只当发布验证） | Workers 100k 请求/日、KV 100k 读/日 |
| 兜底 | ~~`vpn-ops` 上的静态文件~~ 随 D2 推迟；目前**没有**第二份托管 | — |

Worker 只做三件事，**不持有任何节点凭据的生成逻辑**：

```
GET  /p/<device-token>/mihomo-provider.yaml   → KV 取已渲染好的产物 + 订阅响应头
GET  /p/<device-token>/clash.yaml             → 同上
GET  /p/<device-token>/singbox.json           → 同上
GET  /p/<device-token>/base64.txt             → 同上
POST /ingest/<node-token>                     → §3 的巡检上报，写 KV
cron                                          → §3 的日报
```

渲染在**本机**做（`gen-subscription.py`），`publish-subscription.sh` 把产物 PUT 进 KV。
**Worker 里没有明文凭据的组装逻辑，只有已经组装好的字节。**

**必带的响应头**（照抄商用侧 `api/internal/handler/subscription.go:774` 那一组，
包括**小写**这件事 —— 那份实现有一条注释专门解释为什么不能用 `http.Header.Set` 规范化）：

```
subscription-userinfo: upload=0; download=<本月已用字节>; total=<配额字节>; expire=<unix秒>
profile-update-interval: 24
profile-web-page-url: https://<订阅域名>/
content-disposition: attachment; filename*=UTF-8''<机队名>
```

⚠️ **Shadowrocket 是否读 `profile-update-interval`：需实测。**
mihomo 系读，Clash Verge Rev 会据此设置自动更新周期。

### 2.4 每台设备一个 token

**不是**为了计费，是为了**吊销**：一台设备丢了，作废它的 token 而不影响其余五台。
这正是 `VPN方案设计.md` §4.4 当年为 SS-2022 EIH 多用户写下的理由，
只是那套多用户从未真正部署（[as-built §5](../02-architecture/as-built-personal-fleet.md)）。
**在订阅层做吊销比在协议层做便宜得多**，而且对全部七条通路一次生效。

### 2.5 客户端配置：一次性，之后不再碰

**Clash Verge Rev**（真热更新）——把下面这份导入一次，之后**永远不用再动它**：

```yaml
proxy-providers:
  fleet:
    type: http
    url: "https://<订阅域名>/p/<device-token>/mihomo-provider.yaml"
    path: ./providers/fleet.yaml
    interval: 900              # 秒。15 分钟拉一次
    health-check:
      enable: true
      url: https://www.gstatic.com/generate_204
      interval: 300
      lazy: false              # 2026-09-05 文档核实：分组级 url/interval 不测 provider 节点，fallback 的健康信息只来自这里
      expected-status: 204

proxy-groups:
  - name: "🚀 Proxy"
    type: select
    proxies: ["🇯🇵 Japan", "🇺🇸 United States", "🇸🇬 Singapore", "⚡ Auto", DIRECT]
  # 分组级 url / interval 对 use: 导入的节点无效（wiki.metacubex.one proxy-groups，2026-09-05 抓取），故不写
  - name: "🇯🇵 Japan"
    type: fallback
    use: [fleet]
    filter: "^JP-"
  - name: "🇺🇸 United States"
    type: fallback
    use: [fleet]
    filter: "^US-"
  - name: "🇸🇬 Singapore"
    type: fallback
    use: [fleet]
    filter: "^SG-"
  - name: "⚡ Auto"
    type: url-test
    use: [fleet]
    filter: "^(JP|US|SG)-"
    tolerance: 80
```

> **`🚀 Proxy` 默认指 `🇯🇵 Japan` 而不是 `⚡ Auto`**：`url-test` 只按延迟排序，
> 而本链路上健康节点的延迟都落在同一噪声带（95–325 ms），吞吐却差 4–5 倍
> —— 按延迟选会**稳定地挑中慢的那条**（[as-built §4.1](../02-architecture/as-built-personal-fleet.md)）。
>
> ⚠️ **`filter` 是正则，匹配的是节点名。** 所以 `fleet.json` 里的节点命名前缀
> （`JP-` / `US-` / `SG-`）**是接口的一部分**，改名会让分组静默变空。

**Shadowrocket**：设置里填订阅 URL（用 `base64.txt` 那条），
打开「后台自动更新」。换 IP 时它会在后台拉取并替换 profile。

**⚠️ 需实测的从三条减到一条**（2026-09-05 用 wiki.metacubex.one 官方文档核实了 `health-check.lazy`（存在，默认 true）与 `filter`（只作用于 `use:` 导入）；
证据等级「官方文档 = 高」但**不替代真机加载一次**）：仍未核实的是 Shadowrocket 是否读 `profile-update-interval` 响应头。
🔴 **真机客户端一次都没加载**（Clash Verge Rev / Shadowrocket），这是 §5 的第一条。

### 2.6 换 IP 的新流程

```
旧：改 .secrets.env → 跑 gen-clash.py → 6 份 YAML 分发到 6 台设备 → 每台手工导入 → 重载
新：改 fleet.json（一行）→ gen-subscription.py → publish-subscription.sh → 完
```

客户端在 ≤ `interval`（15 分钟）内自动拿到新地址。
**同时**：`rotate-ip.sh` 的检查清单里那条「不会自动触发订阅重新生成」
（[`infra/node/README.md` §9](../../infra/node/README.md) 自记的欠账）在自用队这边被关掉了。

🔴 **但有一个环要闭上**：换 IP 时链路**已经不通**，客户端可能拉不到新订阅。
兜底是订阅里的**公告伪节点**（第一位，节点名写当前订阅域名），
以及 `vpn-ops` 上的静态副本 —— 它在另一个区域、另一个 IP。

---

## 3 · 每日巡检与飞书日报

### 3.1 拓扑

```
   vpn-us ─┐
   vpn-jp ─┼─ healthcheck.sh (systemd timer, 每日 + 每小时轻量)
   vpn-sg ─┘        │ JSON
                    ▼  POST /ingest/<node-token>
        Cloudflare Worker fleet-sub + KV ◀── daily-report.py --source nodes（兜底：IAP 逐台拉 latest.json）
                    │
                    │ cron 00:37 UTC（08:37 CST；避开整/半点；在节点 23:30 UTC daily 之后）
                    ▼
        飞书应用「胖猫」im/v1/messages（2026-09-05 用户指定；webhook 为备选；原案应用「胖狗」私聊）
                    │
                    ▼  只有用户 + 机器人的群
                  用户本人
```

**不选"每台机器各发一条"**：4 台 = 每天 4 张卡片，
而最重要的那条信息恰恰是**"某台没发"** —— 把缺席变成一行正向可读的文字，
比让人每天数卡片数量可靠得多。

**降级路径**：Worker 不可用时，本机 `daily-report.py --source nodes --send` 经 IAP 逐台拉 `/var/lib/fleet/latest.json`、本地渲染精简卡片、发 webhook。
⚠️ 精简版与 Worker 的渲染是两份代码（JS / Python），没有月度差分（那份状态只在 KV 里）。

### 3.2 巡检项（五组）

| 组 | 项 | 判据 | 数据源 |
|---|---|---|---|
| **A 进程与端口** | `xray` / `ssserver` / `hysteria-server` / `cloudflared` | `systemctl is-active` = active | 节点本地 |
| | tcp:443 / udp:443 / tcp+udp:`SS_PORT` 已 bind | `ss -tulnp` 命中 | 节点本地 |
| **B 封锁取证** | ① 进程 active（同 A） | — | 节点本地 |
| | ② 443 上**零**公网 established | `ss -tnp state established '( sport = :443 )'` 计数 | 节点本地 |
| | ③ 数小时零日志 | 服务日志最后一条的时间戳 | 节点本地 |
| | ④ **跨节点回打**：从别的节点对该节点 443 做 TCP 握手 | 握手成功 = 服务没坏，是路径问题 | `vpn-ops` |
| **C 用量与预算** | 本月累计出网 GiB / 各节点占比 | Monitoring `instance/network/sent_bytes_count` | `vpn-ops`（读 API） |
| | 按当前速率的月末外推 vs §1.6 两个阈值 | 软 3,000 / 硬 3,800 GiB | 计算 |
| **D 证书与到期** | Hysteria2 自签证书剩余天数；任何 LE 证书剩余天数 | < 21 天告警 | 节点本地 |
| **E 主机健康** | CPU / 内存 / swap / 盘 / load / OOM / 待重启 | `/var/run/reboot-required` 存在 = 待重启 | 节点本地 |
| | `net.ipv4.tcp_congestion_control` = `bbr`，`default_qdisc` = `fq` | 漂移即告警 | 节点本地 |

🔴 **B 组的第 ③ 条在节点自身上测不准，第 ④ 条节点自己完全测不了。**
[runbook-node-health §3](runbook-node-health.md) 的三条判据要"全部满足"才判 IP 级封锁，
而单机视角只能拿到前两条 —— 这正是需要 `vpn-ops` 做互探的原因。

🔴 **不要用 `ping` / `dig` / `nc` / `curl --interface` 判断连通性。**
本机开 TUN / fake-ip 时它们全部被劫持，Proxy_Skill 记录过**连 `baidu.com` 的正对照也失败**。
巡检全部在节点上跑（机器是我们的，没有 TUN），这一条是设计前提不是建议。

### 3.3 飞书接线（已实测的部分与未实测的部分分开写）

**应用**（2026-09-05 用户改为）：`胖猫`，App ID `cli_a94eb8811578dcd4`，`activate_status: 2`，bot `open_id: ou_8f9991…`；实测在 **16 个**有其他人的工作群里、没有只有用户本人的会话。原案 `胖狗`（`cli_a9439762a1789bc9`）留作备用。

✅ **已实测（2026-09-04，本机 curl）**：

| 调用 | 结果 |
|---|---|
| `POST /open-apis/auth/v3/tenant_access_token/internal` | `code:0`，`expire:7200` |
| `GET /open-apis/bot/v3/info` | `app_name: 胖狗`、`activate_status: 2`（已发布）、`open_id: ou_365d77fa…` |
| `GET /open-apis/im/v1/chats` | 返回 6 个群，`tenant_key: 1740dbc1c7459740` |

> `open.feishu.cn` 与 `open.larksuite.com` 返回**同一个租户**的同一份数据。
> 规范用 `open.feishu.cn`。

🔴 **未解决**：**用户本人的 `open_id` 还没拿到。**
`contact/v3/users/batch_get_id` 在采集会话中被权限策略拦下。两条路，任选：

```bash
# 路 A：用邮箱/手机号反查（需应用有 contact:user.id:readonly 权限）
infra/fleet/feishu-notify.sh --whoami <你的飞书邮箱或手机号>

# 路 B（零额外权限，推荐）：在飞书里建一个只有你和胖猫的群，然后
infra/fleet/feishu-notify.sh --list-chats     # 这条接口已实测可用
```

🔴 **凭据红线**：App Secret **只放 `vpn-ops` 的 Secret Manager 与本机 gitignored `.env`**，
**不下发到任何一台代理节点**。代理节点是暴露面最大的资产，
而它们需要的只是"把 JSON 交出去"，不需要"以胖狗身份发消息"。

🔴 **本次对话中 App Secret 以明文出现过。** 建议在飞书后台重置一次，
新值直接写进 `.env` / Secret Manager，不要再经过任何对话。

### 3.4 卡片格式

`msg_type: interactive`，header 的 `template` 按最坏一项取色：
`green`（全绿）/ `orange`（有 🟡）/ `red`（有 🔴 或有节点缺席）。

```
🐕 机队日报 · 2026-09-05

🔴 vpn-sg   进程 3/4  ·  443 established 0  ·  邻居回打 OK   ← 疑似 IP 封锁，见 runbook-node-health §3
🟢 vpn-jp   进程 4/4  ·  443 est 12  ·  cert 68d  ·  CPU 41%
🟢 vpn-us   进程 4/4  ·  443 est  6  ·  cert 68d  ·  CPU 74%
⚫ vpn-ops  —— 未上报（最后一次 2026-09-04 08:03）

── 本月用量 ─────────────────────
2,140 / 3,000 GiB（软阈值 71%）  月末外推 2,890 GiB 🟡
  vpn-jp 1,180 GiB · vpn-us 720 GiB · vpn-sg 240 GiB

── 待办 ─────────────────────────
· vpn-us 有内核更新待重启
```

**"未上报"必须是一行正向可读的内容，不是省略。**

### 3.5 装到节点上

```bash
# 在本机，凭据走 stdin（不进命令行、不进 history）
set -a; source infra/fleet/.secrets.env; set +a
{
  for v in FLEET_INGEST_URL FLEET_NODE_TOKEN FLEET_NODE_NAME; do
    printf 'export %s=%q\n' "$v" "${!v:?缺少环境变量 $v}"
  done
  cat infra/fleet/healthcheck.sh
} | gcloud compute ssh vpn-jp --project=oratis-491316 --zone=asia-northeast1-a \
      --tunnel-through-iap --command="sudo bash -s -- --install"
```

实际实现（2026-09-05）：本机跑 `infra/fleet/healthcheck-install.sh <host>`，它把 `healthcheck.sh` base64 后连同非机密 env 与 token 经 stdin 送到节点，落
`/usr/local/sbin/fleet-healthcheck`、`/etc/fleet/healthcheck.env`、`/etc/fleet/node-token`（600）、`fleet-healthcheck.service` + `.timer`
（**`OnCalendar=*-*-* *:30:00 UTC`，每小时一次，23:30 那次自动升格为 daily**——原稿 00:05 UTC 会让 00:00 UTC 的日报读到 23 h 前的数据）。
凭据经 `LoadCredential` 注入，**不写进 unit 文件**。B 组第 ④ 条改为**节点互探**（对端表从 Worker `GET /fleet` 取），C 组改为本机 `tx_bytes` 累计、Worker 差分成月度。

`${!v:?}` 是 **fail-closed**：缺任何一个变量在连上机器之前就退出，不生成半成品配置。
`printf %q` 保证含特殊字符的值不会被 shell 二次解释。**两条都照抄 `infra/node/README.md` §3。**

---

## 4 · 代价

> ⚠️ 这一选择的代价，必须留在记录里而不是被措辞掩盖：
>
> 1. **订阅 URL 成了新的单点。** 收敛事实源换来了换 IP 的便利，
>    代价是**多了一个可以被封的域名**。它被封时，客户端会停在最后一次成功拉取的节点集合上
>    —— 那正好是"地址已经变了但拉不到新的"的最坏组合。
>    公告伪节点与 `vpn-ops` 静态副本是缓解，不是消除。
> 2. **`proxy-providers` 把配置正确性的判定推迟到了运行时。**
>    静态 YAML 至少在导入那一刻会被内核校验一次；provider 拉到一份坏文件时，
>    mihomo 会**保留上一份可用的**并只在日志里留一行 —— 用户侧的现象是"节点没更新"，
>    而不是"报错"。**这类静默失败必须靠 §3 的日报来发现**，而日报每天只跑一次。
> 3. **日报是观测，不是执行。** $500 的闸建在"每天看一眼"上。
>    [egress-billing §4.1](../evidence/egress-billing-20260820/) 那次日均 256 GiB
>    持续四天的阶跃如果发生在两次日报之间，预算里会有相当一部分在无人知晓时被烧掉。
>    真正的硬闸是节点侧限速或自动停机，**两者都会在误判时切断用户本人的网络**，所以没做。
> 4. **机队从 2 台变 4 台，运维面翻倍，而运维仍然是一个人。**
>    4 份 unattended-upgrades、4 份证书、4 个可能被封的 IP、4 份需要人去重启的内核更新。
>    日报是部分补偿，不是消除。
> 5. **`vpn-ops` 用掉了 Always Free 唯一那台 `e2-micro`。**
>    从此**任何**新的美国轻量负载都要付钱。这份额度值 $6.11/月，
>    被换成了"有一台不承载业务流量的机器"—— 如果后来发现 Worker 完全够用，
>    这台机器就是纯亏。**复审条件：Worker 连续 60 天零不可用，则撤掉 `vpn-ops` 的聚合角色。**
> 6. **`e2-medium` 未必能解掉 `vpn-us` 的天花板。** §1.5 已写死复审条件。
>    最坏情况是停机一次、每月多付 $18.35，换来的提升低于噪声。
> 7. **飞书是企业租户的资产。** 胖狗当前在 6 个有其他人的工作群里，
>    租户管理员可以看到这个应用、停用它，审计日志里会有它的调用记录。
>    **在私人 VPN 的节点 IP 与流量数据上，这不是一个中立的选择。**

---

## 5 · 这次没有解决的（2026-09-05）

- [ ] 🔴 **飞书收件会话未定（D3，2026-09-05 改用应用「胖猫」）**：`FEISHU_APP_ID/SECRET` 已入 Worker secret；胖猫实测在 16 个有其他人的群里、没有只有用户本人的会话。需用户建一个只有本人 + 胖猫的群（`feishu-notify.sh --list-chats` 取 `chat_id`），或给飞书邮箱/手机号跑 `--whoami` 取 `open_id`；然后 `wrangler secret put FEISHU_RECEIVE_ID`（+ `FEISHU_RECEIVE_ID_TYPE`），`daily-report.py --send` 发第一条。**日报的发送路径一次都没跑。**
- [ ] 🔴 **订阅域名未定（D5）**：独立 zone。定了以后：`worker/wrangler.jsonc` 加 `routes` → `wrangler deploy` → `fleet.json .subscription.hostname` → `publish-subscription.sh`（公告伪节点名里写域名）→ `.secrets.env` 的 `FLEET_INGEST_URL` → 三台 `healthcheck-install.sh` 重装 env。
- [ ] 🔴 **真机客户端一次都没加载**：Clash Verge Rev 导入 `clash.yaml`（provider 热更新）、Shadowrocket 用 `base64.txt`。Shadowrocket 是否读 `profile-update-interval` 仍需实测。
- [ ] **`vpn-us` 升 `e2-small` 后的 4 轮交叉测未做**（ADR 0017 §8 代价 2）；要在用户设备上跑。
- [ ] **`vpn-sg` 的入向路由与 Standard/Premium 对照未做**；`verify-route.sh` 只认 `bp-*`。
- [ ] **`vpn-sg` 没有 SS-2022 与 CDN 通路**（fleet.json 只定义了 HY2 + REALITY），也没有快照计划。
- [ ] **飞书 App Secret 未重置。**
- [ ] **Flow Logs → BigQuery sink 未建；「$ 实收 MTD」没有落点**（Worker 无 GCP 身份，`vpn-ops` 推迟）。
- [ ] **节点退役 / 删除流程没写。**
- [ ] **停机序列没有看门狗脚本**（§6 第 2 条的教训）。

---

## 6 · 2026-09-05 执行记录（偏差逐条）

1. **顺序偏差**：先修 `verify-isolation.sh`（它在 09-04 21:15 已红）→ B70 两条规则 → `vpn-node-sa` → Worker + 订阅 → healthcheck → 停机窗口 → `vpn-sg`。§0 原图的「§2 应当先做」在实际执行里成立。
2. 🔴 **`vpn-jp` 停机序列失控**：09:44 CST `stop` 之后 gcloud 连接中断，`set-service-account` / `start` 没执行，**15:17 才重新启动，约 5.5 h 不可用**。教训：改现役节点的序列必须是一个带重试 / 回滚的脚本，不能逐条手敲 gcloud。
3. `vpn-us` 在 05:40 已被用户的 `optimize-vpn.sh` p2 摘成空 SA（as-built 未记）；09:42 与升 `e2-small` 合并一次停机（50 s）换成 `vpn-node-sa`。
4. `create-vpn-node.sh` 第一次真实执行被自己的读规则误判拦住（网络抖动 → `denied` 读成空串）；改成 JSON 读取 + 读不到就 die，第二次通过。
5. `publish-subscription.sh` 在 macOS 自带 bash 3.2 下因空数组触发 `set -u`；已改 `${arr[@]+"${arr[@]}"}`。
6. `JP_HY2_*` 凭据不在 Proxy_Skill 的 `.secrets-*.env` 里，只在生成产物里；从 `clash-configs/mac.yaml` 回填进 `infra/fleet/.secrets.env`。
7. workers.dev 子域名被交互脚本重复输入成 `oratisoratisoratisoratis`；可在后台改，D5 之后无关紧要。
8. 三台 healthcheck 首次上报全部成功，互探全通；`GET /admin/report` 渲染绿卡；月度用量从首个样本起差分（首样本 mtd=0）。

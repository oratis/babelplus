# 系统架构设计

> 日期：2026-08-16 · 性质：**设计方案** · 状态：**设计稿 v1**（2026-08-16，未实施）
> 事实基线：现有资产见 [as-built-gcp.md](as-built-gcp.md)；
> 面板选型依据 [panels-and-market.md](../01-research/panels-and-market.md) §6；
> 协议实测依据 [reference-repos.md](../01-research/reference-repos.md) §1.5
> 关联：[0001 CF ToS 裁决](../05-adr/0001-cloudflare-tos-risk.md)、
> [0002 通知通道裁决](../05-adr/0002-notification-channels.md)、
> [0010 域名策略](../05-adr/0010-domain-strategy.md)（消解 §2 与 §4.1 的矛盾，**提案，未批准**）、
> [0011 域名失联的发现与恢复](../05-adr/0011-domain-blackout-detection.md)（承接 §9 的自动检测缺口，**提案，未批准**）
> **2026-08-29 补登**：§2、§4.1、§9 各加一处落点（指向 ADR 0010 / 0011），拓扑图与正文裁决未改。
> ⚠️ **本文写的是设计目标，不是当前实现。** 当前实现见 `as-built-*.md`。

---

## 1 · 三条主线

整套系统由三个**故障域互相隔离**的部分构成。这个划分是全篇的骨架：

| 面 | 职责 | 挂了会怎样 | 部署 |
|---|---|---|---|
| **数据面** | 真正转发流量 | 用户上不了网 | GCE 节点（多协议） |
| **控制面** | 账户/订阅/计费/后台/工单 | 用户上得了网，但买不了、改不了 | Cloud Run（API）+ 静态站（Web） |
| **恢复面** | 域名发现、失联广播 | 前两者挂了也救不回来 | 邮件 + 节点名 + 静态域名池 |

> **关键约束：三者不得共享单点。**
> 特别地，控制面挂掉**不能**导致已连接用户断线 —— 节点必须能在面板不可达时
> 用最后一次拉取的配置继续服务（见 §5.3）。

---

## 2 · 全局拓扑

```mermaid
flowchart TB
    subgraph CN["🇨🇳 中国大陆用户"]
        C1[Clash Verge Rev<br/>sing-box / Karing]
    end

    subgraph EDGE["Cloudflare（仅控制面）"]
        W[web.babel.plus<br/>用户面板 SPA]
        D[docs.babel.plus<br/>教程站]
        DNS[DNS]
    end

    subgraph GCPRUN["GCP Cloud Run · us-central1"]
        API[bp-api<br/>用户/节点/管理 API]
        SUB[订阅下发]
        TASK[Cloud Tasks / Pub-Sub<br/>流量入账]
        SCHED[Cloud Scheduler<br/>重置/到期/聚合]
    end

    subgraph NODES["GCP Compute Engine · 出口节点"]
        N1["bp-node-jp<br/>Hysteria2 UDP:443 ★主<br/>VLESS-REALITY TCP:443<br/>SS-2022"]
        N2["bp-node-us<br/>同构"]
    end

    DB[(PostgreSQL)]
    INET([全球互联网])

    C1 -->|订阅拉取 HTTPS| W
    C1 -->|"❶ Hysteria2 / QUIC"| N1
    C1 -->|"❷ REALITY 兜底"| N2
    W --> API
    D -.静态.-> C1
    API --> DB
    SUB --> DB
    API --> TASK --> DB
    SCHED --> API
    N1 <-->|"UniProxy 60s 轮询<br/>ETag + 流量上报"| API
    N2 <--> API
    N1 --> INET
    N2 --> INET

    style N1 fill:#2d5016,color:#fff
    style API fill:#1a3d5c,color:#fff
```

**注意图中没有的东西**：Cloudflare **不在数据路径上**。
理由见 [ADR 0001](../05-adr/0001-cloudflare-tos-risk.md) —— 既是 ToS 合规考虑，
也因为 CDN 路径受单流 TCP 拥塞控制约束，实测吞吐只有 Hysteria2 的 1/5 左右。

> **2026-08-29 补登落点：本节与 §4.1 的域名矛盾已有裁决 —— [ADR 0010](../05-adr/0010-domain-strategy.md)（提案，未批准）。**
>
> 矛盾的原文是：本图把 `web.babel.plus` 与 `docs.babel.plus` 画成同一个可注册域名下的两个子域，
> 而 §4.1 下面一行写着「三者必须是独立域名，**不能是同一域名的不同子域**」。
> [page-inventory §8](../03-product/page-inventory.md) 第 1 条把它登记为「需要一份 ADR 来裁决域名策略」，
> **0010 就是那份**（0010 §11 落点表第 1 行「`page-inventory.md` §8 第 1 条 🔴 → 本文即是，可划掉」）。
>
> **0010 §1.4 的裁决：§4.1 胜出，但改写成 R1–R5 五条可机械校验的规则** ——
> 问题**不是「用了子域」，是「两个必须独立的故障域挂在同一个主域名下」**。
> 0010 §11 给本图的落点是**改写**：`app.<WEB 主域>` / `d.<DOCS 主域>` —— **子域保留，主域拆开**。
> ⚠️ **本图今天不改**：0010 未获批准，且它明写「具体域名不进这个公开仓库」（0010 §1.1、§2.2），
> 图里换上占位主域名只会制造第二处不一致。改图的前置是 0010 获批 + §1.2 的十件事跑完。
>
> **归属侧的事实已变（roadmap B4）**：`babel.plus` **是项目所有者自己的域名**（2026-08-25 用户确认，
> 0010 文档头「归属已答」）。因此 0010 §1.2 第 1 步降为「什么都不要动」——
> **生产 `BP_ALLOWED_ORIGINS` 指向正确，改它会打断生产 CORS**。
> 但归属不改变本条落点：`.plus` 的续费价是 `.org` 的 3.7 倍，0010 §1.5 裁定它**不进池**。

---

## 3 · 数据面：协议栈

### 3.1 单节点并跑多协议（抄 Proxy_Skill）

同一台 VM 上并行开三个入口，成本几乎为零，抗封锁收益极大。
Reality 用 TCP:443、Hysteria2 用 UDP:443，**端口号相同但不冲突**。

| 优先级 | 协议 | 端口 | 定位 | 实测单流吞吐 |
|---|---|---|---|---|
| ❶ 默认 | **VLESS + XTLS-Vision + REALITY** | TCP 443 | **默认通路（下限稳）** | 269 KB/s |
| ❷ 加速 | **Hysteria2 + salamander obfs + 端口跳跃** | UDP 443 | **高丢包/晚高峰切换（上限高）** | **~1700 KB/s** |
| ❸ | Shadowsocks-2022 (`2022-blake3-aes-128-gcm`) | 高位端口 | 兜底 | 370 KB/s |
| ❹ 应急 | VLESS + XHTTP over CF CDN | CF anycast 443 | **默认关闭**，仅节点 IP 被封时下发启用 | 受 TCP 瓶颈约束 |

明确排除：VMess（特征明显）、Trojan-Go（2021 后无发布）、TUIC v5（SNI 暴露于 QUIC 审查）、
裸 SS-2022 作主力（高熵首包易筛）、裸 WireGuard（无抗审查设计）。
观察名单：**AnyTLS**（唯一针对 TLS-in-TLS 论文设计填充方案的协议），待实网数据后再评估。

#### 为什么默认是 REALITY 而不是吞吐更高的 Hysteria2

**这里存在一处调研内部冲突，必须显式记录而不是悄悄取一边。**

| 立场 | 依据 | 强度 |
|---|---|---|
| **Hysteria2 应为默认** | Proxy_Skill 一手实测：单流 370 → ~1700 KB/s，**4.6×** | 一手实测，但**单一网络、单一时段、4 轮轮询、仅 JP 节点** |
| **REALITY 应为默认** | REALITY 走 TCP:443 与海量正常 HTTPS 同形，封锁它附带损害极高；Hysteria2 走 UDP，**逐运营商逐时段的 QoS 降级方差极大** | 机制论证 + 行业共识 |

**裁决：REALITY 做默认，Hysteria2 做可切换的加速通路。**

理由是**方差而非均值**：Hysteria2 的期望吞吐更高，但**最坏情况更差** ——
UDP 被运营商 QoS 降级时它几乎不可用，而 REALITY 的下限稳定得多。
产品的默认值应当优化**首次使用成功率**，不是优化峰值性能；
一个新用户第一次连不上就流失了，不会有机会体验 4.6 倍。

同时下发两条并让用户（或客户端）择优，是成本最低的做法。
**但选路不能按延迟自动做** —— 实测证明 `url-test` 会稳定选错：
各健康节点延迟同在 100–250ms 噪声带内，吞吐却差 4–5 倍。
因此默认组用 **`fallback` 类型**（人工排序 + 失效自动跳过），
并在教程里明确引导用户「慢的时候切到 HY2 组试试」。

> **待实测项**：REALITY 与 Hysteria2 在电信/联通/移动三网、
> 晚高峰（19:00–24:00 CST）的真实可用性与吞吐。
> **若实测显示 UDP QoS 并不严重，本裁决应反转。**

### 3.2 节点端软件

选 **v2node**（MPL-2.0，2026 年仍活跃）而非 XrayR（已废弃、源码被删）、
V2bX（已归档）或 soga（闭源 + 绑定域名）。
协议保持 **UniProxy v1 兼容**，这样节点端不用自己写。

> 🔴 **两个版本兼容性地雷，必须在选定版本时验证：**
>
> 1. **mihomo 已正式放弃与 Xray ≥ v26.7.11 的 REALITY 兼容性。**
>    而 mihomo 是 Clash Verge Rev 等主流客户端的内核 ——
>    这意味着**服务端 Xray 版本直接决定了一大批客户端能否连上**。
>    升级 Xray 前必须先验证客户端侧兼容矩阵。
> 2. **Xray 重命名了配置字段**：`clients`→`users`、`dest`→`target`、
>    `publicKey`→`password`，且**保留了静默别名**。
>    静默别名意味着**配置写错不会报错，只会行为不符预期** —— 生成器必须显式用新字段名。

### 3.3 节点资源规范

- 机型 **`e2-small`**（按需，**不用 Spot** —— 回收 = 全体用户掉线 + IP 变更 + 订阅失效）
- 区域：**`asia-east2`（香港）为主**，`asia-northeast1`（东京）做 A/B
  > 香港是三大运营商国际互联最密集的落地点。物理下限：深圳 0.3ms / 上海 12ms / 北京 20ms。
  > ⚠️ 但香港**不解决晚高峰** —— POMACS 2020 实测 **71% 的瓶颈跳在中国境内纵深**。
- Debian 12
- **网络层级：Standard**（$0.11/GiB + 每源区域每月 200 GiB 免费）。
  🔴 **必须显式指定 `--network-tier=STANDARD`** —— GCP 的默认值是 Premium，
  忘记加参数会静默产生 2.09 倍账单。
  > 该裁决见 [ADR 0008](../05-adr/0008-network-tier-standard.md)，
  > 它推翻了 [ADR 0004 §3.7](../05-adr/0004-transport-hardening.md) 的 Premium 选择 ——
  > 后者唯一的理由是 IPv6，而 OONI 受控对比证明 GFW 的 SNI 封锁在 IPv6 上同等有效。
  > **代价：彻底失去 IPv6，IPv6-only 网络的用户可能连不上（风险敞口未知）。**
- 🔴 **「拿到哪个 IP」是一等变量，不是实现细节。** 实测证据：同一 `asia-east2` 区域内
  zone `-b` 绕道美国而 `-a`/`-c` 直连；`35.220.x` 对移动直连约 50ms 而 `34.92.x` 绕东京约 110ms。
  **开机后必须逐 IP 实测三网路由，不合格立即释放重开。**
- ⚠️ **GCP 上买不到 CN2 GIA，这是结构性的。** 实查 RIPEstat BGP：
  Google AS15169 的 335 个观测邻居中**没有任何中国大陆运营商 ASN**，与 AS4809 也无邻接。
- ❌ **Cloud Run 不能做数据面**：不支持裸 TCP/UDP（REALITY 与 Hysteria2 都跑不了）、
  请求最长 60 分钟硬上限、出口锁定 Premium 层级。
- ❌ GKE 过度设计（$0.10/集群/小时，零收益）。
- ⚠️ GCP Free Tier 的 `e2-micro` 仅限美区，对中国用户是最差位置，**对本项目基本无效**。
- 命名 `bp-node-{region}{n}`，网络标签 `bp-node`
- 保留静态外部 IP，被封后删除重新预留即可换 IP
- SSH 仅走 IAP（`35.235.240.0/20`），公网 22 由 deny 规则压制
- sysctl 调优与 systemd 硬化写法直接复用 Proxy_Skill 的
  （`LoadCredential` + `DynamicUser=true` + `ProtectSystem=strict`）

> ⚠️ 现有防火墙规则 `allow-xray-443` / `allow-hysteria-udp443` **没有 target tag**，
> 对 default 网络所有实例生效。新建节点会自动继承，但也意味着
> **必须同时打上能继承 SSH deny 的标签**，否则 22 端口裸奔。
> 详见 [as-built-gcp.md](as-built-gcp.md) §3。

---

## 4 · 控制面：部署形态

**API 与 Web 分开部署**（硬要求）。

| 组件 | 形态 | 说明 |
|---|---|---|
| `bp-api` | Cloud Run（无状态） | 用户 API + 节点 UniProxy API + 管理 API。运行时选冷启动友好的（Go / Node），**避免 PHP-FPM** |
| `bp-web` | 静态 SPA | 用户面板 + 后台两个 SPA，只调 `bp-api` |
| `bp-docs` | 静态站 | 教程与排障，**必须中国大陆可达**（本身不能需要梯子才能打开） |
| 数据库 | PostgreSQL | ⚠️ `sqladmin` API 当前**未启用**，需显式开启；也可考虑 serverless Postgres，见 §9 |
| 流量入账 | **Cloud Tasks / Pub-Sub push → Cloud Run** | **不要常驻 worker**，这样彻底不需要 `min-instances` |
| 定时任务 | Cloud Scheduler → Cloud Run | 流量重置、到期处理、日/月聚合、订单超时取消 |
| 配置下发 | **60 秒轮询 + ETag，不做 WebSocket** | Cloud Run 上 WS 长连最长 60 分钟、会话亲和只是 best-effort、有连接的实例按 instance-based 计费 —— 成本与复杂度远超收益 |

### 4.1 域名策略（三套独立域名池）

| 用途 | 域名 | 被封时的影响 |
|---|---|---|
| Web 面板 | `web.babel.plus` + 备用池 | 用户看不到面板，但**已连接不受影响** |
| API | 独立域名 + 备用池 | 客户端拉不到新订阅，节点拉不到配置 |
| 教程站 | `docs.babel.plus` + 备用池 | 用户无法自助排障 |

**三者必须是独立域名**，不能是同一域名的不同子域 —— GFW 的封锁粒度常在**主域名**级别。
注册商也应分散，且**注册商账号与 Cloudflare 账号分离**。

> **2026-08-29 补登落点：[ADR 0010](../05-adr/0010-domain-strategy.md) §1.4（提案，未批准）。**
>
> ⚠️ **本节自己就是矛盾的一半**：上面这张表列的 `web.babel.plus` / `docs.babel.plus` 是子域，
> 紧跟的这句话又禁止子域。[ADR 0011](../05-adr/0011-domain-blackout-detection.md) §14
> 把它点名为「**同一节自相矛盾**」，根源是「把主机名当成了失效单位」。
>
> **0010 §1.4 的裁决：本节的方向对、字面太绝对，胜出但改写成五条规则**
> （0010 §11 落点表原文：「**保留并精确化**为 §1.4 的 R1–R5」）：
>
> - **R1** 跨故障域不共享**可注册域名**（registrable domain），不是不共享子域；
> - **R2** 同故障域内部**推荐**共用一个主域名（少一个域名、一份证书、一条监控）；
> - **R3** 同池的两个镜像必须是两个主域名，且不得只差 TLD、不得共享二级标签词根；
> - **R4** 同池的两个镜像不得同平台、不得同平台账号（这是 [deploy.md](../04-ops/deploy.md) §10.2 已有的硬约束升格）；
> - **R5** 恢复通道（MAIL + DOCS）与服务通道（WEB + API + CERT）不共享注册商。
>
> 支撑改写的证据是「两种封锁粒度都被观测到」（0010 §1.4 引 [ADR 0003](../05-adr/0003-web-hosting-and-reachability.md)
> §2.2/§2.3 的单主机名级与 §3.2 的整主域级），设计按更坏的那一种做。
> **池的划分也变了**：0010 §1.3 把「三套」改成**五个故障域池**（WEB / API / DOCS / CERT / MAIL）+ 暗池，
> `status.` / `check.` 并进 WEB 池。0011 §6.1 补一条判定口径：**同一可注册域名下的兄弟子域不计入备件数**。
> 🔴 **本节今天不改写**：0010 未获批准，域名尚未采购（roadmap B4「镜像域名池尚未采购」）。

---

## 5 · 核心契约

### 5.1 节点 ↔ 面板（UniProxy v1 兼容）

| 端点 | 方向 | 说明 |
|---|---|---|
| `GET /api/v1/server/UniProxy/config` | 节点拉 | 节点自身配置。**必须支持 ETag + 304** |
| `GET /api/v1/server/UniProxy/user` | 节点拉 | 该节点的用户列表与密钥。**必须支持 ETag + 304** |
| `POST /api/v1/server/UniProxy/push` | 节点推 | 上报流量：`{uid: [upload, download]}`，**原始字节** |
| `POST /api/v1/server/UniProxy/alive` | 节点推 | 在线设备数（用于设备数限制） |

> **ETag 是性能命门**：节点每 60 秒轮询，不做 ETag 会把 DB 打爆。

**必须相对 Xboard 加固的三处**（原设计的安全底子不够）：

| Xboard 现状 | babel.plus 做法 |
|---|---|
| 全节点共用一个明文 `server_token`，走 query string（会进 access log） | **每节点独立密钥 + scope 白名单**，走 `Authorization: Bearer`，DB 存哈希，支持在线轮换与吊销 |
| 订阅 token 明文永久 32 位，泄露只能手工重置 | 独立 token 表（多条、可命名、可单独吊销）+ 内嵌签发时间 + `sub_revoked_at` 一键全撤 + 每次拉取写审计表 |
| 后台路径靠 `hash('crc32b', app.key)` 混淆，无 2FA | 后台**独立域名 + IP 白名单/IAP + 强制 TOTP**，不靠路径混淆 |

### 5.2 客户端 ↔ 面板（订阅下发）

- 按 `User-Agent` 分发格式：**Clash/mihomo YAML** / **sing-box JSON** / **base64**。
  这是与客户端生态的**硬接口，不能自创格式**。
- 必须返回 `subscription-userinfo` 响应头（上传/下载/总量/到期），
  客户端靠它显示流量条。
- 每次拉取写审计表（`user_id, request_ip, user_agent, request_at`）——
  **这是唯一能识别"账号共享"的数据来源，成本极低。**

### 5.3 面板不可用时节点必须继续工作

节点在拉取失败时**使用最后一次成功的配置继续服务**，并本地缓冲流量数据，
待面板恢复后补报。**控制面故障绝不能升级为数据面故障。**

---

## 6 · 数据模型要点

完整 DDL 见 [panels-and-market.md](../01-research/panels-and-market.md)。此处只记**设计要点**：

1. **可用性判定一条 SQL 覆盖**：`u + d < transfer_enable` AND (`expired_at IS NULL` OR 未过期) AND NOT `banned`。
   `expired_at IS NULL` 天然支撑「不限时套餐」。
2. **倍率在面板侧结算，节点只报原始字节** —— 节点无状态，改倍率不用重启节点。
   （不过第一阶段**不引入倍率**，见 [product-brief.md](../00-overview/product-brief.md) §6。）
3. **流量不落明细流水**：只在用户表累加 + 按天/月聚合到 `stat_user` / `stat_server`。
   **这是本业务的性能命门**，落明细必炸。
4. **热写字段拆表**：`used_traffic` / `online_at` 从主表拆到 1:1 的 `user_traffic`，
   减少行锁竞争。
5. **金额一律 integer 存分**，绝不用 float。倍率若引入则用定点整数（基数 1e9）。
6. **订单状态机**：`待支付 → 开通中 → 已完成`，旁支 `已取消` / `已折抵`；
   订单类型 `新购 / 续费 / 升级`，升级带折抵（`surplus_amount` + `surplus_order_ids`）。
7. **`reset_traffic_method` 多种重置模式** —— 竞品实测「重置日 = 订单日」，不是每月 1 号。

---

## 7 · 关键流程

```mermaid
sequenceDiagram
    participant U as 用户
    participant W as bp-web
    participant A as bp-api
    participant N as bp-node
    participant DB as PostgreSQL

    U->>W: 邀请码注册 / 登录
    W->>A: POST /auth/register
    A->>DB: 建用户 + 签发订阅 token
    U->>W: 选套餐下单
    W->>A: POST /order
    A->>DB: 订单 pending
    Note over A: 支付回调（幂等 + 签名校验）
    A->>DB: 订单 completed，写入配额与到期
    U->>W: 复制订阅链接
    U->>A: GET /sub?token=... (客户端 UA)
    A->>DB: 校验 token + sub_revoked_at
    A-->>U: Clash YAML / sing-box JSON<br/>+ subscription-userinfo 头
    U->>N: Hysteria2 连接
    N->>A: GET UniProxy/user (ETag)
    A-->>N: 304 或用户列表
    N->>A: POST UniProxy/push 原始字节
    A->>DB: 入队 → 累加 u/d
```

---

## 8 · 代价

> ⚠️ 这一选择的代价，必须留在记录里而不是被措辞掩盖：
>
> 1. **自研而非 fork Xboard，意味着从零写账户/订单/工单/后台。**
>    换来的是与 Cloud Run 的运行时模型匹配（Xboard 默认镜像是 supervisor 管的多进程容器，
>    拆分部署仍需 4 个常驻服务 + Redis + **共享可写卷**，而 Cloud Run 没有跨服务共享卷）。
>    **这笔交易只有在我们确实需要 Cloud Run 的弹性与零运维时才划算 ——
>    如果最终发现要长期开 `min-instances`，那不如直接在 GCE 上跑 Xboard。**
> 2. **不做 WebSocket 配置下发，改 60 秒轮询**：配置变更最长有 60 秒延迟。
>    对本业务可接受，但「封禁一个滥用用户」也要等 60 秒才生效。
> 3. **数据面不走 Cloudflare**，放弃了流量免费与「IP 被封仍可用」的常态保障，
>    代价量化见 [ADR 0001](../05-adr/0001-cloudflare-tos-risk.md) §6。
> 4. **三套独立域名池 = 三倍的域名成本与运维复杂度**，且每套都要独立监控。

## 9 · 这次没有解决的

- [ ] **数据库选型未定。** Cloud SQL 需启用 `sqladmin` API 且最小实例约 $9–10/月起；
      Memorystore 约 $35/月对本项目不划算。候选还有 serverless Postgres（Neon/Supabase）
      或自建在 GCE 上。**这是下一个必须做的 ADR。**
- [ ] API 实现语言/框架未定（Go / Node / Python）。
- [ ] 缓存/在线态存储未定（Redis 太贵，可能用 Postgres + TTL 表或 Cloud Run 内存）。
- [ ] **教程站的中国大陆可达性需实测** —— 这是整个自助排障体系的单点。
- [ ] 节点自动 provisioning 与 IaC 未设计（Proxy_Skill 只有装机脚本，建机仍是手敲）。
- [ ] 节点健康探活的具体实现未定（不能用系统网络工具，需走内核 API）。
- [ ] 「域名被封」的自动检测与切换机制未设计。
      > **2026-08-29 补登落点：[ADR 0011](../05-adr/0011-domain-blackout-detection.md)（提案，未批准）。**
      > 它把本条拆成两半分开归属：**发现**交给客户端内核的直连探测腿（§3，境内信号），
      > **恢复**交给浏览器里的 `client.ts`（§8，秒级、不等判定），中心只做判决/补货/广播（§1）。
      > 「自动切主域名」被 §5.1 **显式否掉**（SNI 封锁下切主域名无意义 + 误报等于我们自己制造断网）。
      > 0011 文档头把本条列为它「合并解决 B5 的七处登记」之一。
      > 🔴 **本条不划掉**：0011 状态是**提案，未批准**（2026-08-23），且 §13.1 的阶段零尚未开始。
- [ ] 现有 `vpn-us`/`vpn-jp` 是**原地改造**还是**新建 `bp-node-*` 并行**未裁决。
- [ ] 支付通道未定，见 [payments.md](../01-research/payments.md)。

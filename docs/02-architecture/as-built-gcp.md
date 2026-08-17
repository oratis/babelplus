# 架构现状 · GCP 项目 oratis-491316 资产清点

> 日期：2026-08-16 · 性质：**证据型核查** · 状态：**As-Built**，按 2026-08-16 实际 `gcloud` 输出整理
> 事实基线：`gcloud` 以 `wangharp@gmail.com` 身份对 `oratis-491316`（OratisBase，项目号 `2360090741`）实测
> 证据口径：全部来自 `gcloud ... list` 实际输出，无推断
> 关联：[reference-repos.md](../01-research/reference-repos.md) §1.6（Proxy_Skill 的部署逻辑）

---

## 1 · 为什么先写这份

需求明确要求「使用 GC 项目 `oratis-491316` 进行服务部署（**不影响已经部署的服务**）」。
「不影响」是一条可验证的约束，因此必须先有一份**现状快照**作为基线。
本文只记录**当前实际存在**的资源，规划中的资源不写在这里。

**关键发现：`oratis-491316` 不是一个空项目 —— 它已经在跑两个跨境代理节点。**
这两个节点正是 `oratis/Proxy_Skill` 仓库部署的成果。也就是说，babel.plus 不是从零开始，
而是**在一套已验证可用的自用节点之上做产品化**。

---

## 2 · Compute Engine（现有代理节点，勿动）

| 名称 | 区域/可用区 | 机型 | 内网 IP | 外网 IP | 状态 |
|---|---|---|---|---|---|
| `vpn-us` | `us-west1-a` | `e2-micro` | `10.138.0.2` | `8.231.52.43` | RUNNING |
| `vpn-jp` | `asia-northeast1-a` | `e2-micro` | `10.146.0.2` | `34.104.192.233` | RUNNING |

保留静态外部 IP：

| 名称 | 地址 | 区域 | 状态 |
|---|---|---|---|
| `vpn-us-ip-v4` | `8.231.52.43` | `us-west1` | IN_USE |
| `vpn-jp-ip` | `34.104.192.233` | `asia-northeast1` | IN_USE |

`vpn-us-ip-v4` 的 `-v4` 后缀印证了 `VPN方案设计.md` 记录的**静态 IP 已轮换到第四代** —— 即
美国节点 IP 已被封锁并更换过三次。这是本项目最重要的**先验事实**：
IP 级封锁在这条链路上是**已经反复发生过的**，不是理论风险。

**成本注记**：`us-west1` 的 `e2-micro` 落在 GCP Free Tier 三区内（`us-west1`/`us-central1`/`us-east1`），
`asia-northeast1` 的 `vpn-jp` **不在免费额度内**，是真实付费实例。

### 2.1 隔离承诺

babel.plus 新建的所有资源必须满足：

1. **命名前缀隔离**：一律使用 `bp-` 前缀（`bp-node-*` / `bp-api` / `bp-web` / `bp-*`），
   不复用、不改名、不删除任何 `vpn-*` 资源。
2. **网络标签隔离**：新节点使用 `bp-node` 标签，不使用现有的 `vpn-node` 标签。
3. **防火墙规则隔离**：新增规则一律 `bp-` 前缀且**必须绑定 target tag**，
   不修改任何现有规则（原因见 §3 的风险项）。
4. **Cloud Run 服务隔离**：新服务名 `bp-api` / `bp-web`，与现有三个服务无重名。
5. 变更前后各跑一次本文档的清点命令（见 §7）做 diff 核对。

---

## 3 · 防火墙规则（default 网络）

| 名称 | 方向 | 来源 | 放通 | Target Tags |
|---|---|---|---|---|
| `allow-hysteria-udp443` | INGRESS | `0.0.0.0/0` | `udp:443` | **（无）** |
| `allow-xray-443` | INGRESS | `0.0.0.0/0` | `tcp:443` | **（无）** |
| `allow-ss-48882` | INGRESS | `0.0.0.0/0` | `tcp:48882,udp:48882` | `vpn-node` |
| `allow-iap-ssh` | INGRESS | `35.235.240.0/20` | `tcp:22` | （无） |
| `vpn-iap-ssh-allow` | INGRESS | `35.235.240.0/20` | `tcp:22` | `vpn-node` |
| `vpn-public-ssh-deny` | INGRESS | `0.0.0.0/0` | （deny） | `vpn-node` |
| `default-allow-icmp` | INGRESS | `0.0.0.0/0` | `icmp` | （无） |
| `default-allow-internal` | INGRESS | `10.128.0.0/9` | `tcp/udp:0-65535,icmp` | （无） |
| `default-allow-rdp` | INGRESS | `0.0.0.0/0` | `tcp:3389` | （无） |
| `default-allow-ssh` | INGRESS | `0.0.0.0/0` | `tcp:22` | （无） |

> ⚠️ **发现的三个安全风险（现存，非本项目引入）：**
>
> 1. **`allow-xray-443` 与 `allow-hysteria-udp443` 没有 target tag**，
>    因此对 `default` 网络中**所有**实例生效，包括未来新建的任何 VM。
>    新建 babel.plus 节点时会**自动继承**这两条放通 —— 这既是便利也是隐患：
>    任何在该网络里起的机器都会暴露 443。
> 2. **`default-allow-ssh` 对 `0.0.0.0/0` 放通 tcp:22**，且**无 target tag**。
>    `vpn-public-ssh-deny` 只对带 `vpn-node` 标签的机器压制了它 ——
>    所以现有两台节点是安全的，但**任何不带该标签的新 VM 都会裸奔 22 端口**。
> 3. `default-allow-rdp` 放通 `0.0.0.0/0:3389`，项目内无 Windows 实例，属无用暴露面。
>
> **处置建议（需用户决策，本次不擅自变更）**：
> - babel.plus 的新节点**必须**同时打 `vpn-node` 标签（继承 SSH deny）或新建等效的
>   `bp-public-ssh-deny` 规则，二选一，不能不做。
> - 建议单独提一次收敛：给 `allow-xray-443`/`allow-hysteria-udp443` 补 target tag，
>   删除 `default-allow-rdp`。**这会短暂影响现有节点，属于「影响已部署服务」，
>   须获得明确授权后在维护窗口执行。**

---

## 4 · Cloud Run（现有服务，勿动）

区域均为 `us-central1`：

| 服务 | URL | 最后部署 |
|---|---|---|
| `anthropic-relay` | `https://anthropic-relay-2360090741.us-central1.run.app` | 2026-07-02 |
| `lisa-cloud` | `https://lisa-cloud-2360090741.us-central1.run.app` | 2026-07-25 |
| `lisa-web` | `https://lisa-web-2360090741.us-central1.run.app` | 2026-07-14 |

Artifact Registry：`cloud-run-source-deploy`（DOCKER，`us-central1`，约 1375 MB）
—— 这是 Cloud Run 源码部署自动创建的仓库。babel.plus 建议**新建独立仓库 `bp-images`**，
避免镜像与现有服务混在同一仓库导致误清理。

---

## 5 · Secret Manager / IAM

现有 secret（**勿动**）：

| 名称 | 创建时间 |
|---|---|
| `anthropic-api-key` | 2026-07-02 |
| `relay-token` | 2026-07-02 |

现有服务账号：

| 邮箱 | 用途 |
|---|---|
| `2360090741-compute@developer.gserviceaccount.com` | Compute Engine 默认 SA |
| `vertex-express@oratis-491316.iam.gserviceaccount.com` | Vertex |
| `cuddler-play-billing@oratis-491316.iam.gserviceaccount.com` | Cuddler Play 计费 |

> babel.plus 应新建**最小权限**服务账号 `bp-api-sa` / `bp-node-sa`，
> **不复用 Compute 默认 SA**（默认 SA 权限过大且被现有工作负载共用）。
> 新 secret 一律 `bp-` 前缀。

---

## 6 · 已启用 / 未启用的 API

**已启用且与本项目相关**：`compute`、`run`、`artifactregistry`、`cloudbuild`、
`secretmanager`、`iam`、`iap`、`logging`、`monitoring`、`pubsub`、`cloudkms`、`cloudtrace`。

**未启用，需要时须显式开启**（这是架构选型的硬约束）：

| API | 影响 |
|---|---|
| `sqladmin.googleapis.com` | **Cloud SQL 不可用**。若选 Postgres 需先启用，且 Cloud SQL 最小实例约 $9–10/月起 |
| `dns.googleapis.com` | Cloud DNS 不可用。但我们的域名走 Cloudflare DNS，**不需要它** |
| `redis.googleapis.com` | Memorystore 不可用。最小实例约 $35/月，对本项目**成本不划算** |

> 选型含义：数据库与缓存**不应默认走 GCP 托管服务**。
> 详见 [02-architecture](.) 的选型裁决（待写），候选是
> Cloudflare D1/KV/Durable Objects、Neon/Supabase 等 serverless Postgres，或自建在 GCE 上。

---

## 7 · 清点命令（变更前后各跑一次做 diff）

```bash
P=oratis-491316
gcloud compute instances list --project=$P
gcloud compute addresses list --project=$P
gcloud compute firewall-rules list --project=$P
gcloud run services list --project=$P
gcloud secrets list --project=$P
gcloud artifacts repositories list --project=$P
gcloud iam service-accounts list --project=$P
gcloud services list --enabled --project=$P
```

---

## 8 · 代价

- 采用「命名前缀 + 独立标签」的软隔离，而不是**新建一个独立 GCP 项目**做硬隔离。
  代价是：配额、计费、IAM、VPC 全部与现有工作负载共享，
  一次误操作（例如删错防火墙规则）**可以影响到 `vpn-us`/`vpn-jp` 与三个 Cloud Run 服务**。
  这是为了满足「使用 `oratis-491316` 部署」这条明确要求而接受的取舍。
- 若后续需要真正的爆炸半径隔离，应改为独立项目 + 共享 VPC。**这个取舍在流量或营收规模上来后需要重新评估。**

## 9 · 这次没有解决的

- [ ] `vpn-us` / `vpn-jp` 上实际运行的服务版本、配置与健康状态**未登机核查**
      （需要 `gcloud compute ssh --tunnel-through-iap`，属侵入性操作，待授权）。
- [ ] 现有 Cloudflare 账号下的 Tunnel、DNS zone、Workers 资产**未清点**
      （需要 Cloudflare API token 或后台访问权限）。
- [ ] 计费账号与当前月度实际支出**未查**（`gcloud billing` 需要额外权限）。
- [ ] §3 的三条防火墙风险**仅记录未处置**，需用户决策。

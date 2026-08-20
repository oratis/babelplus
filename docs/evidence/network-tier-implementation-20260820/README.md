# 网络层级：ADR 0008 的落地与既有节点层级核实 · 2026-08-20

> 日期：2026-08-20 · 性质：**证据型核查 + 裁决落地** · 状态：**已完成**
> 事实基线：`gcloud` 对 `oratis-491316` 的实时查询 + gcloud 自带帮助文本 + 一次真实建删探针
> 关联：[ADR 0008](../../05-adr/0008-network-tier-standard.md)、
> [gcp-egress-pricing-20260817](../gcp-egress-pricing-20260817/)、
> [as-built-gcp §9](../../02-architecture/as-built-gcp.md)

---

## 1 · 起因

ADR 0008（2026-08-17）裁定「节点使用 Standard 网络层级」，并在 §5 代价第 5 条写明：

> Premium 是 **GCP 的默认值** —— 不显式指定就会用 Premium。
> 这意味着**任何忘记加参数的建机操作都会静默产生 2.09 倍的账单**。
> 必须在 `infra/node/create-node.sh` 里硬编码 `--network-tier=STANDARD` 并加断言。

**三天里这条要求一行都没有落地。** 而且不是「忘了写所以落到默认值」——
`create-node.sh` 与 `rotate-ip.sh` 里**显式硬编码了 `--network-tier=PREMIUM`** 共 7 处，
主动与自己的裁决相反。ADR 写完即失效，因为没有任何机制把它连到代码上。

## 2 · 核实一：既有节点全部是 PREMIUM

这同时关闭了 ADR 0008 §6 的最后一个遗留项（「现有 vpn-us / vpn-jp 用的是哪个层级仍未核实」）。

```
NAME    ZONE               NETWORK_TIER  NAT_IP
vpn-us  us-west1-a         PREMIUM       8.231.52.43
vpn-jp  asia-northeast1-a  PREMIUM       34.104.192.233

NAME          REGION           NETWORK_TIER  ADDRESS         STATUS
vpn-us-ip-v4  us-west1         PREMIUM       8.231.52.43     IN_USE
vpn-jp-ip     asia-northeast1  PREMIUM       34.104.192.233  IN_USE
```

原始输出：[instances-tier.txt](instances-tier.txt)、[addresses-tier.txt](addresses-tier.txt)

**推论（ADR 0008 §5 代价第 3 条的判断基准）：** Proxy_Skill 当年那批延迟实测
是在 **Premium** 下取得的。因此「Standard 的晚高峰表现如何」至今**没有任何数据**——
不是「有数据但不够」，是零。§5 第 3 条的风险敞口比原文写的更大。

## 3 · 核实二：Standard 在 asia-east2 可用

ADR 0004 §3.6 选定香港 `asia-east2`。Standard 是分区域可用的，**必须验而不能假定**。

真实建删探针（2026-08-20）：

```
$ gcloud compute addresses create bp-tier-probe-7992 \
    --project=oratis-491316 --region=asia-east2 --network-tier=STANDARD
Created [.../regions/asia-east2/addresses/bp-tier-probe-7992].

bp-tier-probe-7992	35.215.139.226	STANDARD	RESERVED

$ gcloud compute addresses delete bp-tier-probe-7992 ... --quiet
Deleted [...]
```

探针已删除，未留下计费资源。

## 4 · 核实三：IPv6 与 Standard 互斥是 API 硬约束

不是我们的策略选择。gcloud 自述（[gcloud-tier-constraints.txt](gcloud-tier-constraints.txt)）：

- `--network-tier` — `must be one of: PREMIUM, STANDARD. **The default value is PREMIUM.**`
  ← 坐实了 ADR §5.5 说的「不写就是 2.09 倍」。
- `--ipv6-network-tier` — `must be (**only one value is supported**): PREMIUM`
  ← 外部 IPv6 只有 Premium 一个取值。

所以 ADR 0008 §1 的「接受失去 IPv6」是**硬后果**，没有绕过的余地。

## 5 · 落地了什么

| 文件 | 改动 | 理由 |
|---|---|---|
| `create-node.sh` | 新增常量 `NETWORK_TIER="STANDARD"`，地址与建机两处引用它 | ADR §5.5 明文要求「硬编码」 |
| `create-node.sh` | `--ipv6` 与非 Premium 层级**互斥，直接 die** | 静默降级会让人以为拿到了双栈节点 |
| `create-node.sh` | verify 阶段新增 `②′ 网络层级断言`，读回 `accessConfigs[0].networkTier` 比对 | ADR §5.5 明文要求「加断言」 |
| `rotate-ip.sh` | 层级改为**从实例现状推导**，不写死 | 见下 |

**`rotate-ip.sh` 为什么不硬编码 STANDARD（这一条是本次最容易做错的地方）：**

保留地址的层级必须与网卡 access-config 的层级一致，否则 `add-access-config` 失败。
而该调用发生在**已经摘掉旧 access-config 之后** —— 失败点正好落在脚本自己标注的
「节点没有公网 IP，全部用户掉线」那个窗口里。若写死 STANDARD，本脚本在任何既有
Premium 节点（即 `vpn-us` / `vpn-jp`）上**必然**走进这条路径。

因此：`rotate-ip.sh` 保持层级不变，`create-node.sh` 决定新节点的层级。
换 IP 与换层级是两件事，捆在一起会让一次应急操作同时改变两个变量。

## 6 · 既有节点为什么不迁

**不迁 `vpn-us` / `vpn-jp`。** 层级不能原地修改，必须换一个 STANDARD 保留地址再切
access-config —— 也就是**必然换 IP**。这两台是已部署且在服务的机器，换 IP 会让
所有既有用户的配置立刻失效。这超出「实施 ADR 0008」的范围，属于独立决策。

ADR 0008 裁的是**新节点用什么层级**，本次落地的也正是这个。

## 7 · 代价

> 1. **本次没有省下一分钱。** 改的是「将来建的节点」，而目前 babel.plus
>    还没有自己的节点 —— 在途流量全部跑在 `vpn-us` / `vpn-jp` 上，仍是 Premium。
>    省钱要等到第一台 `bp-node-*` 建起来，或者既有节点迁移被单独批准。
> 2. **Standard 的性能仍然零数据。** §2 的推论说明连基准都不存在。
>    第一台 Standard 节点建起来之后必须立刻做一次晚高峰对比，
>    否则我们是在没有性能数据的前提下把全部新节点押在 Standard 上。
> 3. **断言是事后的。** 它在 verify 阶段发现层级不对，那时地址与实例都已经建好了。
>    改正的手段是 `rotate-ip.sh`（换 IP）。做不到「建之前就拦住」——
>    因为层级错误的来源之一正是 `--address` 传进来的既有 Premium 地址。

## 8 · 这次没有解决的

- [ ] 🔴 **既有 `vpn-us` / `vpn-jp` 的迁移未做**，也未排期。按 §6 它需要单独批准。
      在此之前，实际在跑的流量 100% 是 Premium 计价。
- [ ] **Standard vs Premium 的性能 A/B 仍未做**（ADR 0008 §6 原有项，未因本次落地而关闭）。
      §2 新增的信息是：连 Premium 侧的基准都只是 Proxy_Skill 的二手社区数据。
- [ ] `--address` 传入一个 PREMIUM 保留地址时，建机会失败还是静默成功？**未测。**
      §7 代价第 3 条假定它会被 verify 阶段的断言抓到，但没有实际制造过这个场景。
- [ ] IPv6-only 用户比例（ADR 0008 §6 原有项）依旧未知，且本次落地把「无 IPv6」
      从默认行为变成了**强制行为**（`--ipv6` 现在直接 die）—— 风险敞口没变，但更难被意外绕开。

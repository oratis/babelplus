# `vpn-sg` 建机清点 · 2026-09-05

> 日期：2026-09-05 · 性质：**证据型核查** · 状态：**已完成（本次采集）**
> 事实基线：`infra/fleet/create-vpn-node.sh` 第一次真实执行（2026-09-05 15:30–15:33 CST，身份 `wangharp@gmail.com`）
> 自动落下的建机前后清点快照（`gcloud compute instances/addresses/firewall-rules list`、`run services list`、
> `secrets list`、`iam service-accounts list`）；`--dry-run` 于同日先跑过一次全流程
> 证据口径：`gcloud` 输出 = 高
> 关联：[ADR 0017](../../05-adr/0017-personal-fleet-in-repo.md)（用户 2026-09-05 按修订批准；D4 起步机型 e2-small）、
> [personal-fleet-runbook §1.4](../../04-ops/personal-fleet-runbook.md)、
> [`infra/fleet/create-vpn-node.sh`](../../../infra/fleet/create-vpn-node.sh)

---

## 1 · 这些证据证明什么、不证明什么

**证明**：
1. **建机前后，除 `vpn-sg` 自己与它的保留地址之外，项目里没有任何资源发生变化**
   —— `diff -u inventory-before.txt inventory-after.txt` 只多两行（实例 `vpn-sg`、地址 `vpn-sg-ip-cand1`），
   `vpn-us` / `vpn-jp` / `bp-*` / 三个 Cloud Run 服务 / secret / SA 逐字节未变。这是 ADR 0017 §3「同仓不同队」在建机这个动作上的可验证形式。
2. 建机即刻验收全过：标签 `vpn-node`（且不含 `bp-node`）、`networkTier=STANDARD`、SA = `vpn-node-sa`、
   六条规则全部生效（`vpn-iap-ssh-allow` / `vpn-public-ssh-deny` / `vpn-allow-reality-443` / `vpn-allow-hy2-udp443` / `allow-ss-48882` / `vpn-deny-from-bp`）。
3. 三个不等式在建实例**之前**核过：`iap-allow(500) < deny(600) < default-allow-ssh(65534)`、`deny-from-bp(700) < default-allow-internal(65534)`。

**不证明**：
1. **不证明入向路由质量。** `infra/node/verify-route.sh` 只认 `bp-*` 目标；自用队的路由验收（含晚高峰）**一次都没做**。
2. **不证明 Standard 层级好不好用。** 这是 ADR 0017 §4.3 那个「不干净的对照」的一半；另一半（`vpn-jp` Premium）没有同法采样。
3. **不证明 IP 网段好坏。** `rank_ip` 只对 `asia-east2` 有社区先验，新加坡五个候选等权、选了第一个（`34.2.143.75`）。
4. **不证明机内配置。** 装机（`setup-vpn-node.sh`）在本快照之后；它的自检输出在 [as-built-personal-fleet §2.1](../../02-architecture/as-built-personal-fleet.md)。

## 2 · 结果

| 项 | 值 |
|---|---|
| 实例 | `vpn-sg` · `asia-southeast1-a` · `e2-small` · `pd-standard 30GB` · debian-12 · Shielded ×3 · 删除保护 on |
| 外网 IP | **`34.2.143.75`**（保留地址 `vpn-sg-ip-cand1`，STANDARD） |
| 内网 IP | `10.148.0.2` |
| 标签 / 标记 | `vpn-node` / `owner=personal,fleet=vpn,role=proxy` |
| 服务账号 | `vpn-node-sa@oratis-491316.iam.gserviceaccount.com`（`logging.write` + `monitoring.write` scopes） |
| 候选地址 | 预留 5 个（`34.2.143.75` / `35.213.153.51` / `35.213.157.234` / `35.213.182.192` / `35.213.161.71`），选 1 释放 4 |

## 3 · 这次没有解决的

- [ ] 保留地址名是 `vpn-sg-ip-cand1`（候选命名遗留），与 `vpn-us-ip-v4` / `vpn-jp-ip` 的命名不一致；改名要释放重建，不值得。
- [ ] 第一次真实执行时 `fw_ensure` 曾把网络抖动导致的空读判成「属性不符」而拒绝建机（15:2x CST）；当场改成 JSON 读取 + 读不到就 die，第二次执行通过。这条改动在脚本头部有记。
- [ ] 路由验收与 Standard/Premium 对照未做（§1 不证明 1、2）。

# 建机证据 · bp-node-hk1

> 日期：2026-09-01 · 性质：**证据型核查**（清点 diff，不是路由验收）
> 状态：**已完成**（建机部分；装机与验收都不在本目录）
> 事实基线：`infra/scripts/verify-isolation.sh` 与 as-built §7 清点命令在 `oratis-491316` 上实跑，
> 输出原样落盘，未做筛选。
> 关联：[node-provisioning.md](../../04-ops/node-provisioning.md) §5、
> [ADR 0007 §9](../../05-adr/0007-node-migration.md)（阶段 1–2）、
> [node-route-bp-node-hk1-20260901](../node-route-bp-node-hk1-20260901/)（路由采样）、
> [node-route-methodology-20260901](../node-route-methodology-20260901/)（判据被推翻的那一份）
> 读者：下一个建节点的人。**本目录只回答「建出来的东西长什么样、有没有碰到别的资源」。**

---

## 证明什么

- **建机前后的资源清点 diff**（`inventory-before.txt` / `inventory-after.txt`，
  时间戳 `2026-08-31T17:14:21Z` → `17:21:30Z`），逐项可比对。
- 新增的 `bp-` 资源恰好是四类，且**只有这四类**：
  | 资源 | 值 |
  |---|---|
  | 实例 | `bp-node-hk1` · asia-east2-a · **e2-small** · 内网 `10.170.0.2` · RUNNING |
  | 静态 IP | `bp-node-hk1-ip-cand1` = `35.215.140.154`（asia-east2） |
  | 防火墙 ×4 | `bp-allow-hy2-udp443`(udp:443/1000) · `bp-allow-reality-443`(tcp:443/1000) · `bp-iap-ssh-allow`(tcp:22/**900**) · `bp-public-ssh-deny`(deny tcp:22/**1000**) |
  | 服务账号 | `bp-node-sa@oratis-491316.iam.gserviceaccount.com`（描述 `babel.plus node runtime`） |
- **SSH 姿态的优先级关系成立**：`bp-iap-ssh-allow` 优先级 **900** 严格小于
  `bp-public-ssh-deny` 的 **1000**，即「IAP 段放行」压过「全网拒绝」。
  这是 ADR 0007 §9 阶段 1「防火墙先行」要的形态 —— 注意它与既有 `vpn-*` 的
  500/600 那一对是**两套独立规则**，`bp-` 这套只对 `bp-node` 标签生效。
- **非 `bp-` 资源逐字节未变**：diff 里除上述四类外无任何其他差异，
  `vpn-us` / `vpn-jp` 两台实例与它们的静态 IP、`lisa-*` 与 `anthropic-relay` 全部原样。
  这是 P1 出口标准 8 要求的那种「不影响已部署服务」的可验证形式。

## 不证明什么

- 🔴 **不证明节点能用。** 本目录只覆盖 ADR 0007 §9 的**阶段 1–2**（防火墙 + 建实例）。
  **装机 9 步一步都没做** —— sysctl / v2node / xray / hysteria2 / ss-2022 /
  acme.sh + LE / systemd 硬化 / unattended-upgrades / swap **全部未执行**。
  机器在跑，机器上什么都没有。见 roadmap §4.2 的 1.A。
- 🔴 **不证明路由验收通过。** 采样在
  [node-route-bp-node-hk1-20260901](../node-route-bp-node-hk1-20260901/)，
  而那批采样所依据的 J1–J3 判据已被
  [node-route-methodology-20260901](../node-route-methodology-20260901/) **实测推翻**
  （ICMP 打运营商 DNS 与真实路径质量无关）。**判据当前处于作废状态**，见 roadmap **B55**。
- **不证明 IP 是选出来的。** roadmap §4.2 要的是「预留 5 个看落段、留 1 删 4」，
  实际是**只留了 1 个**（`cand1`）；后来因误判换过一次，现网 IP 是 `35.215.158.52`
  而不是本目录 `inventory-after.txt` 里的 `35.215.140.154`。
  **两者不一致不是笔误** —— 清点发生在换 IP 之前，原样保留。
- **不证明网络层级符合原计划。** roadmap §4.2 阶段 2 原文写的是 **Premium**，
  实建为 **Standard**（照 [ADR 0008](../../05-adr/0008-network-tier-standard.md)）。
  该偏差由 `node-route-methodology-20260901` §2.3 的 A/B 事后支持（36.4 vs 36.2 ms 无可测差异），
  **但那是一次单目标单时段的握手延迟测量，不是吞吐**。
- **不证明 IPv6 已启用**（阶段 2 原文含 IPv6，本次清点无相关字段，未核实）。

## 还要人工补的

- [ ] 装机 9 步，然后才谈得上 P1 出口标准 2–7
- [ ] `verify-isolation.sh` 的 16/16 与 18/18 输出未落盘（本目录只有 as-built §7 清点那一半）
- [ ] IPv6 是否启用、能否事后启用（承 roadmap B20 的二次阻塞）

## 代价

- **清点是命令输出的原样粘贴，没有结构化。** 好处是不可能在转录时失真，
  代价是要靠 `diff` 读，且它不会随资源变化自动重跑 —— 下次建节点必须重新跑一遍，
  拿旧文件对比会得到错误结论。
- **本目录的 IP 与现网不一致**（见「不证明什么」第 3 条）。
  保留不一致比事后改成一致更有信息量：它记录了「换过一次 IP」这个事实本身。

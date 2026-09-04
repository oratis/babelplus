# E0 · HTTPS 代理入站不进计量：100 MiB 走完，`stat_user_server` 一个字节没变；同一时间窗的 REALITY 路径 20 MiB 正常入账

> 日期：2026-09-04 · 性质：**证据型核查**（结论是**否定的**）
> 状态：**已完成** —— 判定见 §1，正对照见 §4
> 事实基线：全部命令在 `bp-node-hk1`（经 IAP SSH）与 `bp-db`（cloud-sql-proxy + 容器 psql）上真实执行；输出逐字粘贴
> 关联：[client-products-spec §6.1 E0](../../03-product/client-products-spec.md)、[roadmap **B66**](../../00-overview/roadmap.md)、
> [e0-metering-verification.md](../../04-ops/e0-metering-verification.md)（本次执行的就是它）、
> [web/extension/README §6](../../../web/extension/README.md)（这道门挡着什么）
> 读者：下一个考虑扩展那条传输的人。**§1 与 §5 先读。**

---

## 1 · 一句话

**HTTPS 代理入站的字节数不进 `stat_user_server`，一个字节都不进。**
100 MiB 经该入站下载完成后等两个 `/push` 周期，计量表逐行未变；
而**同一台节点、同一个时间窗**里经现有 REALITY/Hysteria2 路径下载的 20 MiB，
2 分钟内就出现在表里。所以这不是「表没在动」，是**那条路径根本没有账**。

**E0 判定：不通过。** 按 spec §6.1 的规定，**停** ——
在计量问题解决之前，不把这条入站写进任何下发路径。

## 2 · 证明什么 / 不证明什么

**证明**：
- Caddy forwardproxy 起得来、能认证、能转发（100 MiB 实测走通）；
- 它的流量**不产生任何 `stat_user_server` 记录**；
- 计量管道本身当天是活的（§4 正对照）。

**不证明**：
- 不证明「无法计量」，只证明**当前形态下没有计量**。理论出路见 §5，都要写代码。
- 不证明大陆可达性、存活期、被封时间（那是 E5）。
- 不证明凭据方案 —— 本次用的是一对手工 Basic 凭据，与 spec §3.7 的「由订阅 token 派生」无关。

## 3 · 怎么做的

```
入站   Caddy v2.10.0 + forwardproxy（caddy2 分支）
       🔴 **在本机 Docker 里交叉编译 linux/amd64 后只拷一个二进制上去**，
          不在节点上装 Go、不在节点上构建 —— 那才是真正威胁 72 h 观察窗内存判据的东西。
          sha256 defd4f30e5752d8d82a9336a0f19b695db3753b319e027c56e6422becc89b74a（两端一致）
监听   127.0.0.1:8443（**只回环**，不碰防火墙、不碰 443、不碰 v2node）
客户端 节点本机 curl，经 CONNECT 隧道下载
用量   4 × 25 MiB = 104,857,600 bytes，10:29:57Z–10:29:58Z 完成
```

```
基线 10:28:09Z
  user=1 server=1 date=2026-09-01 u=5096  d=15053419 total=15058515
  user=1 server=1 date=2026-09-02 u=5545  d=5023456  total=5029001
  user=1 server=2 date=2026-09-02 u=16899 d=46149817 total=46166716

100 MiB 之后 + 等 156 秒（> 2 个 push 周期）10:32:43Z
  （三行**逐字节相同**，且没有 2026-09-04 的新行）
  diff → 完全没有变化
```

## 4 · 正对照：同一时间窗，现有路径正常入账

没有这一步，「没变化」也可以解释成「这张表最近就没动过」。所以紧接着用**现有的** REALITY/HY2
订阅（本机 mihomo v1.19.30 容器，配置在 `~/.bp-mihomo-test/native/`）下了 20 MiB：

```
出口 IP 35.215.158.52（正确）· http=200 · size=20,971,520 · 4.1 s
等 156 秒后：
  user=1 server=2 date=2026-09-04 u=3650 d=21012333 total=21015983   ← **新行**
```

计入 **21,015,983** 字节 vs 实下 20,971,520 字节 = **+0.21%**，与
[node-bringup-20260901](../node-bringup-20260901/) 记的 0.3% 同量级。

> **两个数字并排看**：同一节点、同一天、相隔几分钟 ——
> **HTTPS 入站 104,857,600 字节 → 记 0；REALITY 路径 20,971,520 字节 → 记 21,015,983。**

## 5 · 为什么是这个结果，以及出路有几条

`stat_user_server` 的写入方只有一个：v2node 经 `POST /api/v2/server/push` 上报，
而 `server_id` 是 `servers.id` 的外键、由**节点密钥**推导。Caddy 是**另一个进程**：
它既不是 `servers` 里的一行，也没有节点密钥，更不知道我们的 `users` 是谁 ——
它的 Basic 用户名是 `e0probe`，与 `users` 表无关。**所以它没有任何路径把账报上来。**

⚠️ 「让 v2node 自己起一个 http inbound」这条路**已被排除**：v2node v0.4.3 的 `GetNodeInfo`
只认 `vmess / trojan / hysteria2 / tuic / anytls / vless / shadowsocks`，
给它不认识的 protocol 会让**整个进程以退出码 0 退出**（2026-09-02 实测，同机 REALITY 陪葬）。

剩下的出路，三条，都要写代码，且都要一次新的裁决：

| # | 出路 | 代价 |
|---|---|---|
| 1 | **给 HTTPS 入站建一条独立上报通路**：`servers` 加一行（protocol 枚举要加值 = 改冻结契约 + 迁移）、发一把节点密钥给一个新的上报器（读 Caddy 访问日志或用它的 metrics），按 UniProxy 的形状 push | 新增一个与 UniProxy 并行的信任边界；Caddy 的日志里没有「我们的 user_id」，凭据到用户的映射要另做一张表 |
| 2 | **给 v2node 加一个 http inbound**（改上游或自维护分支） | 自维护一个数据面二进制，与「版本地雷 ①」（v0.4.5 断裂）叠加 |
| 3 | **放弃扩展这条传输** | 扩展说不了 REALITY（`chrome.proxy` 的 scheme 枚举），等于放弃扩展这个产品形态；go-to-market §4.5 的渠道第 4 位随之作废 |

**本文不裁决走哪条。** 但有一条现在就能说：出路 1 的工作量与风险，
与 spec §6.1 给 E1 估的「1 周」不是一个量级 —— 那个估算是在「计量白拿」的假设下做的。

## 6 · 顺带撞出的三条（都不在原计划里）

1. 🔴 **Caddy 会往节点的信任库里装一个自己的本地 CA**（`Caddy_Local_Authority_-_2026_ECC_Root_…`），
   即使配置里写了 `auto_https off`。本次已删除并 `update-ca-certificates --fresh`，
   核对后自定义 CA 数为 0。**任何在节点上跑 Caddy 的方案都要处理这一条。**
2. 🔴 **站点地址必须是端口形式（`:8443`），不能带主机名。** CONNECT 请求的 `Host` 是**目标站**
   （`speed.cloudflare.com:443`），匹配不上 `hk1.babel.plus:8443` 那样的站点块，
   于是整条链路空转：隧道回 200 但什么都不转发，客户端表现为内层 TLS
   `wrong version number`（curl 退出码 35）。**报错指向的是错的方向。**
   `infra/node/setup-https-inbound.sh` 本来就写的是端口形式，本条是给下一个改它的人。
3. ⚠️ **不配 `probe_resistance` 时，无凭据访问返回 407** —— 那正是 spec §3.7 禁止的
   「对主动探测举手」。本次配置为了隔离变量没开它，实测确认了 407 这个行为。
   脚本里的 `verify()` 正是检查这一条。

> **三条都已折进 [`infra/node/setup-https-inbound.sh`](../../../infra/node/setup-https-inbound.sh)**（2026-09-04）：
> 文件头记下经过；新增 `BP_CADDY_BIN=<已构建好的二进制>` 走「不在节点上装 Go」那条路；
> 装完主动删掉 Caddy 塞进信任库的本地 CA 并 `update-ca-certificates --fresh`；
> Caddyfile 里把「地址必须是端口形式」写在生成的配置旁边。
> 🔴 但**下一个人仍然会撞上第 2 条**：脚本只覆盖它自己生成的配置，手改配置时没有东西拦着。

## 7 · 对 72 小时观察窗的影响：无

本次刻意把节点上的动作压到最小（二进制在本机构建、只监听回环、不动防火墙与 v2node）。事后核对：

```
心跳（node_id=1，10:10–10:37Z）：28 条，最大间隔 61 s —— 未中断
内存采样峰值（整个窗口 2868 条样本）：24.9%（阈值 70%），峰值时刻恰在本次测试期间
bp-node.service：active
8443：已无监听；/usr/local/bin 下只剩 v2node；hosts 条目已删；信任库已复原
```

## 代价

- 在**唯一一台**生产节点上跑了一次实验。事后核对四项都干净（§7），但这件事本身是有风险的，
  只是这次风险被压到了「一个 42 MB 的静态二进制 + 一个回环监听」。
- 产生约 125 MiB 出口流量（100 MiB 实验 + 20 MiB 正对照），按 `c = $0.121/GiB` 约 **$0.015**。
- 正对照那 20 MiB **真的记在了运维账号 `demo@babel.plus` 头上**（server_id=2，2026-09-04 那一行）。
  它是一笔真实用量，不是模拟。
- 临时 Basic 凭据在节点上以 0600 存在过约 8 分钟（`/etc/bp/.e0pass`），已删除。

## 这次没有解决的

- [ ] 🔴 **走哪条出路（§5 的三选一）—— 需裁决。** 在裁决之前 `getUserProxyConfig` 保持 501。
- [ ] 出路 1 的形状：`servers.protocol` 枚举加值意味着改冻结契约；上报器读什么、
      凭据到 `user_id` 的映射放哪，都没设计。
- [ ] E5（大陆存活期）未做，且在 E0 有答案之前做它没有意义。
- [ ] `probe_resistance` 的回落站点用哪个域名、放什么内容 —— 仍未定。

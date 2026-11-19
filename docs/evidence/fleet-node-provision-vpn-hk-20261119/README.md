# `vpn-hk` 建机清点 · 2026-11-19

> 日期：2026-11-19 · 性质：**证据型核查** · 状态：**已完成（本次采集）**
> 事实基线：`infra/fleet/create-vpn-node.sh --address vpn-hk-ip` 真实执行（2026-11-19 13:26–13:28 CST，身份 `wangharp@gmail.com`），
> 同日先跑过一次 `--dry-run`；地址 `vpn-hk-ip` 在脚本之前手工预留（PREMIUM，`asia-east2`）
> 证据口径：`gcloud` 输出 = 高；路由数字 = 单次采样，见 §2.2 的时段
> 关联：[ADR 0017](../../05-adr/0017-personal-fleet-in-repo.md)、[personal-fleet-runbook §1.4](../../04-ops/personal-fleet-runbook.md)、
> [`infra/fleet/create-vpn-node.sh`](../../../infra/fleet/create-vpn-node.sh)、
> 排查记录：oratis/Proxy_Skill `ops/README.md`「2026-11-18 · 部分客户端连接缓慢」一节、探针 `ops/gcp/region-path-probe.sh`

---

## 0 · 为什么建它

慢的客户端全是**中国联通（AS4837）**。GCP 回给联通的流量无论从 `vpn-jp` / `vpn-us` / `vpn-sg` 出去，都被 Google 内部拉到美国、
交给 Lumen AS3356 / Telia AS1299 再进 4837 国际口，晚高峰约 50% 丢包（客户端侧 TCP 重传率 34–56%）。
`asia-east2` **Premium** 在香港直交 219.158.x；Standard 在香港同样绕美国（346 ms），所以层级必须是 PREMIUM。
电信从香港反而绕美国（174 ms vs 东京 57–67 ms），所以客户端按延迟选区，而不是把联通写死到香港。

## 1 · 这些证据证明什么、不证明什么

**证明**：
1. **建机前后，除 `vpn-hk` 与它的保留地址之外，项目里没有任何资源发生变化** ——
   `diff -u inventory-before.txt inventory-after.txt` 只有两处：多一行实例 `vpn-hk`，`vpn-hk-ip` 从 `RESERVED` 变 `IN_USE`。
2. 建机即刻验收全过：标签 `vpn-node`（且不含 `bp-node`）、`networkTier=PREMIUM`、SA = `vpn-node-sa`、
   六条规则全部生效（`vpn-iap-ssh-allow` / `vpn-public-ssh-deny` / `vpn-allow-reality-443` / `vpn-allow-hy2-udp443` / `allow-ss-48882` / `vpn-deny-from-bp`）。
   **没有新增防火墙规则**：443 入向走既有的 `vpn-node` 标签规则，`fleet.json` 的 `expected_firewall_non_bp` 不变。
3. 三个不等式在建实例**之前**核过：`iap-allow(500) < deny(600) < default-allow-ssh(65534)`、`deny-from-bp(700) < default-allow-internal(65534)`。
4. `infra/scripts/verify-isolation.sh`：`vpn-hk` 标 `planned` 时 27/0，改 `running` 后 **28/0**。

**不证明**：
1. **不证明晚高峰。** §2.2 是北京时间 11:19 与 13:33 的采样；晚高峰（20:00–24:00）复测在 Proxy_Skill `ops/README.md` 记录。
2. **不证明电信 / 移动走香港更好。** 只采到了联通客户端；电信的 174 ms 来自 11-19 02:22 UTC 的临时探针，不是 `vpn-hk` 本机。
3. **不证明吞吐。** 只有 ICMP RTT / 丢包与 traceroute；HY2 Brutal 下的单流吞吐要真机测。
4. **不证明 IP 网段好坏。** 只预留了一个地址（`34.96.243.207`）；`rank_ip` 的 asia-east2 先验（35.220.x 移动直连 / 34.92.x 移动绕东京）说的是移动，本次没测移动。

## 2 · 结果

### 2.1 实例与装机

| 项 | 值 |
|---|---|
| 实例 | `vpn-hk` · `asia-east2-a` · `e2-small` · `pd-standard 30GB` · debian-12（内核 6.1.0-56）· Shielded ×3 · 删除保护 on |
| 外网 IP | **`34.96.243.207`**（保留地址 `vpn-hk-ip`，**PREMIUM**） |
| 内网 IP | `10.170.0.6` |
| 标签 / 标记 | `vpn-node` / `owner=personal,fleet=vpn,role=proxy` |
| 服务账号 | `vpn-node-sa@oratis-491316.iam.gserviceaccount.com`（`logging.write` + `monitoring.write` scopes） |
| 通路 | `HK-Reality`：xray 26.11.9 VLESS + REALITY + Vision，tcp:443，dest `www.cloudflare.com` · `HK-HY2`：Hysteria2，udp:443，salamander，自签证书 CN=www.bing.com，bing 伪装 |
| HY2 带宽 | 服务端**不写** `bandwidth`（runbook §2.2 约束 1）；客户端声明 up/down 即启用 Brutal |
| 凭据 | **在节点上生成**（`xray x25519` / `xray uuid` / `openssl rand`），只落在 `/usr/local/etc/xray/config.json` 与 `/etc/hysteria/config.yaml`；不经本机、不进日志 |
| sysctl | `99-proxy-network.conf`：bbr + fq（自检回读确认） |
| 巡检 | `fleet-healthcheck.timer` 已装，首跑 **上报成功**（13:29 CST）；四个对端 tcp:443 全通：vpn-jp 57 ms、vpn-sg 42 ms、vpn-fengrongcheng-sg 44 ms、vpn-us 138 ms |
| 快照 | 资源策略 `vpn-weekly-hk`（每周日 18:00 UTC，保留 28 天，源盘删除后保留）已挂到盘 `vpn-hk`（与 `vpn-weekly-sg` 同参数；建于清点快照之后，所以不在 diff 里） |

### 2.2 路由（同一分钟、同一批客户端，ping 60 次 1/s + traceroute；IP 打码）

| 时间（UTC） | 出发点 | 联通北京 114.246.x.x | 进 4837 的位置 | vpn-jp 同一分钟 |
|---|---|---|---|---|
| 03:19 | asia-east2 Premium 临时探针（34.150.x，已删） | 50 ms，0% | 香港，第 3 跳 5.5 ms | 205 ms，17%（Google → 美国 → Lumen → 219.158.x） |
| 05:33 | **`vpn-hk` 本机（34.96.x）** | **54 ms，0%** | 香港，第 4 跳 31 ms，北京城域网第 9 跳 45 ms | 200 ms，20% |

另一个联通出口 111.194.x.x 不回 ICMP：03:19 从香港 traceroute 第 9 跳到北京城域网 51 ms；从 vpn-jp 止于 Lumen 第 4 跳 191 ms。

## 3 · 这次没有解决的

- [ ] 晚高峰复测（11-19 13:00 / 14:00 UTC 已排程，结果记在 Proxy_Skill `ops/README.md`）。
- [ ] 订阅：`gen-subscription.py` 的区域元组仍硬编码 `(JP|US|SG)`；HK 进订阅由另一个会话随整份订阅一起发布（香港出口不得进 `🤖 AI` / `⚡ Auto`：Anthropic / OpenAI / Gemini 不对香港 IP 服务）。
- [ ] `NODE_TOKEN_VPN_HK` 只在节点 `/etc/fleet/node-token` 与 KV `tok/node/*`；`.secrets.env` 不在本机，下次跑 `publish-subscription.sh` 前要把它补进去（脚本对缺失的节点 token 会直接退出）。
- [ ] p0 对齐：asia-east2 子网 flow logs、失联 / CPU 告警扩到 `vpn-hk`。

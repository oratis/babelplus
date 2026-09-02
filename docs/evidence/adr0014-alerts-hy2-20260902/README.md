# ADR 0014 批准落地、HY2 通路接通、以及又三条只有真机才会撞上的缺陷

> 日期：2026-09-02 · 性质：**证据型核查**（含推翻既有设计的部分）
> 状态：**已完成**（HY2 结果见 §5；三态计时见 §6）
> 事实基线：全部命令在本机（`wangharp@gmail.com` 身份的 `gcloud`）、生产 `bp-api`、
> `bp-node-hk1` 与 `bp-db`（cloud-sql-proxy + 容器 psql）上真实执行。日志逐字粘贴。
> 关联：[ADR 0014](../../05-adr/0014-slo-and-oncall.md)（本文是它批准记录的证据）、
> [roadmap §4.3 / §9](../../00-overview/roadmap.md)（P1 出口标准、B57/B58/B60/B62 的处置）、
> [node-bringup-20260901](../node-bringup-20260901/)（前一天的四条缺陷，本文接着记第五到第七条）、
> [first-deploy §4.5](../../04-ops/first-deploy-20260831.md)
> 读者：下一个接节点、或下一个动告警的人。**§3 那条「一个节点错、全部不起」先读。**

---

## 1 · 一句话

**批准一份裁决之后的第一天，产出的是七条新的实测事实和两次约 2–4 分钟的 REALITY 中断 ——
每一条都不能靠读代码发现，而其中三条直接改写了「什么算做完了」。**

## 2 · 证明什么

- ADR 0014 §18 第 1 条：v2node 在零流量分钟**不发** `/push`，但心跳不依赖它（§4.1）。
- `setup-alerts.sh` 在真实 GCP 上跑通需要修的四处（§4.2）。
- Hysteria2 通路从「签不出证书」到接通之间隔着的**三条契约缺陷**（§3）。
- v2node 对多节点配置的失败形态：一个节点错，**整个进程退出码 0 退出**（§3）。
- 从容器客户端验证节点的可复现方法，以及它踩过的三个坑（§5）。

## 3 · 不证明什么

- **不证明中国大陆可用。** 全部客户端测试从出口在 `34.104.192.233` 的开发机发起。
- **不证明 72 小时稳定。** 本文记录的是观察窗口的**起点**，不是终点。
- **不证明告警会叫醒人。** 12 条策略一条都没演练（ADR 0014 §11.1：未演练不算存在）。

---

## 4 · ADR 0014 批准落地

### 4.1 §18 第 1 条实测：零流量分钟不发 `/push`

```
gcloud logging read '... jsonPayload.path:"UniProxy/push"' --freshness=24h  → 按小时计数
   8 2026-09-01T06          ← 只有做吞吐测试的那个小时
（其余 23 个小时：0）
gcloud logging read '... jsonPayload.path:"UniProxy/user"' --freshness=24h  → 每小时 59 次
gcloud logging read '... jsonPayload.message="bp_node_alive"' --freshness=30m
   2026-09-02T04:43:34Z  node_id=1
   2026-09-02T04:42:34Z  node_id=1
   2026-09-02T04:41:33Z  node_id=1
```

结论：`/push` 只在有流量时出现；但 `bp_node_alive` 记在**任一** UniProxy 端点鉴权通过之后
（`handler/nodealive.go`），`/user` 每分钟轮询一次 → 心跳每分钟都在。
**absence 型策略（B1）的创建前置解除**，误报前提不成立。

### 4.2 `setup-alerts.sh --apply` 在真实 GCP 上撞到的四处

| # | 现象 | 根因 | 修法 |
|---|---|---|---|
| 1 | `PROJECT_ID�: unbound variable` | `$PROJECT_ID（` —— 变量名紧跟全角括号，bash 在某些 locale 下把首字节并入变量名 | 一律 `${VAR}` |
| 2 | 渠道查询「解析失败」，然后**每跑一次多建一对重复渠道**（连建了三对，已手工删） | `--filter="type=email AND …"`：gcloud 把右侧 `email` 当字段名，INVALID_ARGUMENT → 脚本走「没找到 → 新建」 | 过滤器里 type 与值都加引号 |
| 3 | 6 条策略 `INVALID_ARGUMENT: only COMPARISON_LT and COMPARISON_GT are supported` | ADR 写的是 `>= N`，API 不支持 GE | 计数改 `> N-1`，磁盘使用率改 `> 0.799` |
| 4 | B2 的过滤器永远匹配不到序列 | uptime check 的 `check_id` 带随机后缀（`bp-api-healthz-1bMHnj3kS4M`），不是 displayName | 常量 `UPTIME_CHECK_ID` |

另：脚本刻意不提供 `--yes`，而 gcloud 在伪终端下行为不同（第 2 条最初就是在 pty 里撞上的），
所以加了 `BP_ALERTS_CONFIRM=<确认串>` 这一条非交互旁路 —— 调用方仍必须写出那个串。

### 4.3 实建清单（2026-09-02 终态）

```
gcloud alpha monitoring policies list --filter='displayName~^bp-'
  bp-b-api-5xx                        B3
  bp-b-api-instances-cap              B7
  bp-b-cert-expiring-or-unreachable   B13（新增：cert_expiring_soon / cert_check_failed）
  bp-b-cert-issuer-bad                B12（ADR A3 的带内替身）
  bp-b-db-backends                    B6
  bp-b-db-disk                        B9
  bp-b-db-pool-wait                   B5
  bp-b-healthz-unreachable            B2
  bp-b-node-heartbeat-absent          B1（node_id=1 的序列已存在，armed）
  bp-b-scheduler-task-failed          B11（就是 ADR「B 级 11 条」里没名字的那一条）
  bp-b-subscribe-404                  B8
  bp-b-uniproxy-auth-fail             B4
未建：A1 / A3（带外，VPS 未采购）· A2（email#2 与推送未建，四通道不齐）· B10（bp_node_active_users 指标不存在 + MQL 未核实）
通道：email#1 = ops alerts (wangharp) · bp-alerts-pubsub（事后取证）· email#2 / 推送 未建
删除：2026-09-01 手工建的 bp-scheduler-task-failed / bp-api-healthz-down / bp-cert-issuer-bad（被 B11 / B2 / B12 接管）
日志指标：8 → 13（新增 bp_node_alive[带 node_id 标签] / bp_mail_bounce / bp_ratelimit_degraded / bp_cert_expiring_soon / bp_cert_check_failed）
```

JSON 入库：`infra/alerts/*.json`（`--emit-json` 产物，monitoring §5.2 的要求）。

### 4.4 证书核对的每日作业（B57 前半、B58）

```
镜像   us-central1-docker.pkg.dev/oratis-491316/bp-images/bp-cert-issuer-check:b6bd9ad
       （infra/jobs/cert-issuer-check/build.sh；google/cloud-sdk:558.0.0-alpine + bash + openssl）
Job    bp-cert-issuer-check（Cloud Run Job，SA bp-tasks-sa，BP_WEB_DOMAINS=web.babel.plus BP_API_DOMAINS=api.babel.plus）
       ⚠️ admin.babel.plus 刻意不在清单里：它走 IAP，保留 GTS 托管证书，放进去就是一条永远红的告警
调度   bp-cert-issuer-check-daily  40 4 * * *  Asia/Shanghai  → jobs:run（OAuth，bp-tasks-sa）
首跑   gcloud run jobs execute bp-cert-issuer-check --wait
         ✓ web.babel.plus issuer O=Let's Encrypt CN=YE1
         ✓ api.babel.plus issuer O=Let's Encrypt CN=YE1
         通过 2 项 / 失败 0 项 / 提醒 0 项
```

与 ADR 0014 §10.2 的偏离：这是**带内**（GCP 之内）。VPS 未采购之前，「有信号」胜过「无信号」；
告警因此记 B 级（B12/B13），不冒充 A 级。

### 4.5 顺手落下的另外三件

- **WIF**：`setup-wif.sh --apply --yes` → pool `bp-github-pool` / provider `bp-github-oidc`
  （condition `assertion.repository == 'oratis/babelplus'`），仓库变量 `GCP_WIF_PROVIDER` / `GCP_DEPLOY_SA` 已设；
  `bp-deploy-sa` 补了 `bp-api` 服务级 `roles/run.developer`。**`deploy.yml` 仍未跑过第一次**（B47 仍开）。
- **阿里云 DNS AK/SK 入 Secret Manager**：`bp-aliyun-dns-ali-key` / `bp-aliyun-dns-ali-secret`
  （从节点 `~/.acme.sh/account.conf` 经管道写入，值未经任何终端显示）。
- **`bp-node-hk1` 的证书早就签出来了**：`issuer=C=US, O=Let's Encrypt, CN=YE2`，`subject=CN=hk1.babel.plus`，
  `notAfter=Nov 30 07:03:09 2026 GMT`，acme.sh cron 已挂（`1 1,7,13,19 * * *`），`account.conf` 里是 `SAVED_Ali_*` ——
  也就是 2026-09-01 那轮文档写「签不出证书」（B60）时，证书其实已经在盘上了。

---

## 5 · Hysteria2：从「证书在盘上」到「客户端能连」之间的三条缺陷

前提：`servers` 表里 `bp-node-hk1-hy2`（id=2，`hysteria2`）2026-09-01 就已建好，`protocol_settings` 带
`cert_mode=file` 与 `/run/credentials/bp-node.service/{fullchain,privkey}.pem`；节点上证书是 LE 的；
`v2node.json` 只有节点 1。于是把节点 2 加进 `v2node.json` 重启 —— 三次，撞三条：

| # | 节点侧报错 | 根因（我们的 `/api/v2/server/config`） | 修 |
|---|---|---|---|
| 1 | `Get node info failed err=unsupport protocol: hysteria` | 下发 `protocol: "hysteria"` + `version: 2`（Xboard 老格式）；v2node v0.4.3 `GetNodeInfo` 只认 `hysteria2` | `protocol: "hysteria2"`，并下发 `tls_settings{cert_mode,cert_file,key_file,server_name}` |
| 2 | `failed to listen on 0.0.0.0:443 > transport/internet/hysteria: tls config is nil` | 没下发 `tls: 1`；v2node 只在 `Security==Tls` 时才用 `tls_settings` 构造监听 | `tls: 1` 无条件写死（HY2 的 TLS 不是可选项） |
| 3 | **无任何报错**；客户端 `context deadline exceeded`，回环也连不上 | 下发 `obfs-password`（Xboard 连字符），v2node 读 `obfs_password`（下划线）→ 拿到空串 → 「obfs 与密码必须成对」→ **静默不开混淆**，带 salamander 的客户端流量全被丢 | 契约键名改 `obfs_password`（订阅给客户端的仍是 Clash 的 `obfs-password`，两码事） |

第 3 条的诊断手法值得记住：**回环上把客户端的 obfs 去掉就通了**（5 MB 0.16 s）—— 服务端没有 obfs 是唯一解释。

三次都是同一个失败形态（§7）：v2node 一个节点配置错，**整个进程退出码 0**，同机 REALITY 陪葬，
`Restart=on-failure` 不触发。第三条更糟：它连退出都没有，节点侧零日志。

**每一条都对应一次 `bp-api` 部署**（`bp-api-28991eb` → `7579163` → `a747ebf`），都走 `deploy-api.sh`
候选 → 验证候选 URL 的 `/api/v2/server/config` → 切流量；第三条上线后用 `UPDATE node_rev SET config_rev=config_rev+1`
让节点热重载（61 s 后日志 `重启成功`），没有再重启进程。

### 5.1 实测结果

```
节点回环（mihomo linux-amd64 v1.19.30 → 127.0.0.1:443，sni hk1.babel.plus，salamander）
  出口 IP 35.215.158.52 · 5 MB 0.25 s（19.7 MB/s，本机回环无意义，只证明服务端配置对）
本机容器（docker mihomo v1.19.30 + curl 同网络命名空间 → 35.215.158.52:443/udp）
  出口 IP 35.215.158.52 · 5 MB ×3：3.18 / 3.84 / 4.79 MB/s
本机原生（mihomo darwin-arm64 v1.19.30）
  出口 IP 35.215.158.52 · 5 MB ×3：2.45 / 3.68 / 3.53 MB/s
同机同时段 REALITY（容器）：5 MB 3.13 MB/s
```

⚠️ 发起点出口在 `34.104.192.233`（境外），**不证明中国大陆的 UDP 路径**。

### 5.2 容器测试方法踩的三个坑（都写进 [local-development.md](../../04-ops/local-development.md) 之前先记这里）

1. **配置目录必须在 `$HOME` 下**：colima 对 `/private/tmp/…` 的 bind mount **静默为空**，mihomo 于是加载一份默认配置、
   `-t` 还会报「test is successful」、所有请求走 DIRECT。与 renew-le-cert.sh 头部记的是同一个坑，第三次踩。
2. **不要 `-p` 映射端口**，用 `docker run --network container:<mihomo 容器> curlimages/curl -x http://127.0.0.1:7890`：
   mihomo 默认只绑回环，映射进来的流量到不了它。
3. **不要自定义名为 `GLOBAL` 的 proxy-group**：它与内置组同名，`mode: global` 下全部走 DIRECT。
   用 `mode: rule` + `rules: [MATCH,<proxy>]`。

配置留在本机 `~/.bp-mihomo-test/`（含 uuid 与 obfs 密码，0600）。

## 6 · 三态生效计时（P1 出口标准 6）

### 6.0 先撞上的一条：只剩一个用户时，谁都踢不掉

第一次封禁 `demo@babel.plus`（当时节点上唯一的用户）：`bp-api` 在 3 s 后给了 200（`user_rev` 已 bump），
节点却**什么都没做**。v2node `node/task.go`：

```go
if len(newU) == 0 {
    log.WithField("tag", c.tag).Debug("User list no change")
    return nil
}
```

**空列表被当成「没变化」。** 这是 roadmap B7 记过的那条保护（防止面板故障时清空全员），
它的另一面是：**节点上只剩最后一个用户时，封禁 / 到期 / 配额耗尽对他永远不生效。** 登记为 **B63**。
处置：建了哨兵用户 `drill-sentinel@babel.plus`（id=2，与运维账号同组同套餐，`password_hash='!'` 不可登录，
通知全关，uuid 从未导出），让列表永不为空。**第二台节点、第二个真实用户上线前，它必须留着。**

### 6.1 结果（哨兵在位之后；时间全部 UTC，节点侧以 `journalctl -o short-iso-precise` 为准）

| 状态 | 触发（数据库写入时刻） | 节点侧生效（`1 user deleted`） | 耗时 | 阈值 | 恢复 → 重新下发 |
|---|---|---|---|---|---|
| 封禁 | `banned=true` 07:14:51.757 | 07:15:30.1（vless）/ 07:15:30.8（hy2） | **38–39 s** | ≤ 60 s ✅ | 07:15:36.3 → 07:16:30.9 / 31.6（**55 s**） |
| 配额耗尽 | 配额压到已用 + 3 MB，07:22:12.7；经 HY2 真实下载 6 MB，07:22:18.3 完成 | `/push` 07:22:26 → API「本批流量导致用户配额耗尽，已 bump」07:22:25.8 → 07:22:35.4 / 36.2 | **17–18 s**（自下载完成起） | ≤ 60 s ✅ | 07:22:44.8 → 07:23:36.2 / 36.9（**52 s**） |
| 到期 | `expired_at` = 07:16:43.2 | expire-check 07:20:02（5 分钟周期）→ 07:20:33.9 / 34.6 | **3 min 51 s** | ≈ 6 min（最坏）✅ | 07:20:43.3 → 07:21:34.6 / 35.4（**52 s**） |

三条都过。配额那条是**真实路径**（客户端下载 → 节点 `/push` 上报 → API 判定越线 → bump → 节点下一次 `/user` 拿到 200），
不是改库模拟。到期那条的 3 min 51 s 里 3 min 19 s 在等 Scheduler 的下一个 5 分钟刻度。

账号状态已全部还原（`banned=false`，`expired_at=2026-10-01`，配额 100 GiB）。演练产生的真实流量约 6 MB。

### 6.2 72 小时窗口（P1 出口标准 5）起点

**2026-09-02T07:05:22Z**（HY2 节点热重载成功、两条通路同时在跑的时刻；REALITY 自 06:56:11Z 的最后一次进程重启起连续）。
节点侧 `bp-mem-sample.timer` 每分钟写 `/var/log/bp-mem-sample.csv`（epoch, MemTotal_kB, MemAvailable_kB），
终点 **2026-09-05T07:05Z** 之后算峰值。`bp-b-node-heartbeat-absent` 已 armed，中断会有信号。

## 7 · 两次 REALITY 中断（都是本文作者造成的）

| 起 | 止 | 时长 | 原因 |
|---|---|---|---|
| 2026-09-02 04:57:40 UTC | 05:01:43 UTC | ≈ 4 min | 加第二个节点后 v2node 报 `unsupport protocol: hysteria`，整个进程退出码 0 退出 |
| 2026-09-02 06:41:35 UTC | 06:43:28 UTC | ≈ 2 min | 同上，报 `transport/internet/hysteria: tls config is nil` |

两次都是 `Restart=on-failure` **不触发**（退出码 0），靠人手工回滚到单节点配置恢复。
🔴 **规律**：v2node 的多节点配置是「一个错、全部不起」，且失败形态是**安静退出**。
加第二个节点之前，先用它的 token 手工 `GET /api/v2/server/config` 看 `protocol` 与 `tls`。

---

## 代价

- **两次 REALITY 中断**共约 6 分钟，都在只有运维自己一个账号的阶段。**72 小时观察窗口因此在 §5 之后才起算。**
- 本文作者在排查时把 `servers.protocol_settings` 整列打印过一次，**REALITY 私钥与 HY2 的 obfs 密码
  因此进入了本次会话的转录**。私钥泄露的后果是「持有者可探测并冒充 REALITY 服务端」；
  建议轮换（换一对 x25519、改库、bump `config_rev`，客户端会经订阅拿到新公钥）—— 登记为 roadmap B65。
- 证书核对作业**带内**跑，与 ADR 0014 §10.2 的裁决有意偏离，理由见 §4.4。
- 告警 12 条**零演练**。`alert-drill-ledger.md` 尚未创建。
- 部署了两次 `bp-api`（`bp-api-28991eb` → `bp-api-7579163`），都走 `deploy-api.sh` 两段式，
  `verify-isolation.sh` 部署前 16/16、部署后 18/18；**`deploy.yml` 仍然一次都没跑过。**

## 这次没有解决的

- [ ] 72 小时观察窗口（出口标准 5）：起点 2026-09-02T07:05:22Z（§6.2），终点未到
- [ ] 节点密钥两步轮换演练（出口标准 7）：需在 `admin.babel.plus` 以 `wangharp@gmail.com` 登录后做；
      本机 Chrome 登录的是另一个 Google 账号，IAP 未授权，本文作者未代点 OAuth 账号选择
- [ ] 12 条告警的演练与 `alert-drill-ledger.md`
- [ ] A2 / A1 / A3：email#2、推送通道、VPS —— 都需用户
- [ ] ADR 0014 §15.1 D0 第 2 条（GCP 账号 2FA 改本地 TOTP + 离线备用码）—— 需用户本人
- [ ] REALITY 私钥轮换（B65）
- [ ] 哨兵用户是绕过不是修复（B63）：v2node 的空列表短路要么上游改，要么 `/user` 永远至少回一个占位条目 —— 未裁决
- [ ] SS-2022 仍未启用（第三条通路，本轮没碰）
- [ ] `local-development.md` 未收录 §5.2 的容器测试方法
- [ ] `deploy.yml` 第一次真跑

# 第一台节点接通 · 四条只有真机才会撞上的缺陷，以及 B15 的实测判定

> 日期：2026-09-01 · 性质：**证据型核查**（推翻既有设计，不是补充数据）
> 状态：**已完成**（REALITY 通路端到端可用；HY2 与 SS-2022 未启用，理由见 §6）
> 事实基线：全部命令在 `bp-node-hk1`（asia-east2-a，`35.215.158.52`）与
> 生产 `bp-api` 上真实执行。日志逐字粘贴，未做筛选。
> 关联：[roadmap §4](../../00-overview/roadmap.md)（P1 出口标准）、
> [ADR 0004 §3.3](../../05-adr/0004-transport-hardening.md)（mux 裁决，**被本文推翻**）、
> [system-design §3.1](../../02-architecture/system-design.md)（XTLS-Vision）、
> [node-provisioning §4.5](../../04-ops/node-provisioning.md)（REALITY target 选型标准，**本文给它补了一条**）
> 读者：下一个接节点的人。**动手之前先读 §5 那条判据 —— 它是本次最贵的一条。**

---

## 1 · 一句话

**从「机器建好了」到「客户端真的能上网」之间隔着四条缺陷，
没有一条能靠读代码发现，每一条的失败形态都指向错误的方向。**

最终状态：未经任何手工修改的订阅，在 **mihomo（Clash Verge Rev 内核）**与
**sing-box** 上各加载一次，**出口 IP 均为 `35.215.158.52`**；
15 MB 下载的流量入账误差 **0.3%**。

---

## 2 · 证明什么

- v2node 与 `bp-api` 之间六个端点的实际契约（**其中一个与冻结契约不符**）。
- REALITY 在本项目参数下的可用性，以及 **target 站点的证书链大小是硬约束**。
- **mux 与 XTLS-Vision 互斥**（roadmap **B15**，登记 16 天后第一次有数据）。
- 流量入账、在线设备上报、ETag 条件请求、配置热重载四条链路在真机上成立。

## 3 · 不证明什么

- **不证明中国大陆可用。** 全部测试从一台**出口在 `34.104.192.233`（vpn-jp）**
  的开发机发起，不是境内探针。三网可达性、晚高峰吞吐、入向路由**全部零数据**。
- **不证明 Hysteria2 可用** —— 它根本没启用（§6）。
- **不证明 72 小时稳定** —— 观察窗口尚未开始。
- **不证明 `www.bing.com` 是最优 target** —— 只证明它在本节点上握手成功、
  无 xray 告警、`200 + HTTP/2 + 无跳转`、延迟 176 ms。**它在中国的可达性未实测。**

---

## 4 · 四条缺陷（按撞上的顺序）

### 4.1 冻结契约与 agent 在 config 端点上分叉

v2node 自 v0.4.0 起把**且仅把**配置端点迁到 `/api/v2/server/config`
（`api/v2board/node.go:122`），`/user` `/push` `/alive` `/alivelist` 四个仍在
`/api/v1/server/UniProxy/*`，且它**没有 `/status` 调用**。
冻结契约抄的是 v2board 1.7.4 / Xboard，六个端点里**五个对得上，一个对不上**。

```
level=error msg=Get node info failed err=decode node params error:
  invalid character 'p' after top-level value
```

🔴 **这条报错完全不指向路由**：v2node 拿到 Go 默认的 `404 page not found`，
把它当 JSON 解析 ——`404` 是合法的顶层数字，后面那个 `p` 才是报错点。

### 4.2 systemd 单元无条件挂载证书凭据

`setup-node.sh` 的 step 3 自己写明「REALITY ❌ 不需要证书、SS-2022 ❌ 不需要、
只有 HY2 ✅ 需要」，而单元把 `fullchain.pem` / `privkey.pem` 写成无条件
`LoadCredential=`。systemd 在源文件不存在时**让单元启动失败** ——
于是**一个只跑 REALITY 的节点根本装不起来**，且失败信息指向证书。

### 4.3 ExecStart 用了不存在的标志，而失败时退出码是 0

v2node 是 cobra 应用：`v2node server -c <path>`。
写成 `v2node --config <path>` 走 usage 分支并以**退出码 0** 结束，
systemd 记 `Deactivated successfully`、`Restart=on-failure` **不触发**。
现象是「服务装好了、enable 了、却安静地没在跑」。

### 4.4 `deploy-api.sh` 复现不出生产配置

`BP_ADMIN_TOTP_ENC_KEY` 线上是 Secret Manager 引用（2026-08-31 手工配的），
而脚本把它当普通环境变量传：

```
ERROR: (gcloud.run.deploy) Cannot update environment variable
[BP_ADMIN_TOTP_ENC_KEY] to string literal because it has already been set with a different type.
```

要么拒绝部署，要么（线上没配过时）把加密管理员 TOTP secret 的主密钥
**降级成明文环境变量**。

---

## 5 · 🔴 最贵的一条：REALITY target 的证书链大小是硬约束

装机、契约、单元全部修好之后，握手仍然失败。开 `realitySettings.show: true` 才看清：

```
REALITY remoteAddr: 127.0.0.1:53912  hs.c.AuthKey[:16]: [19 77 143 127 43 155 93 125 116 73 131 170 232 126 53 244]
REALITY remoteAddr: 127.0.0.1:53912  hs.c.ClientVer: [26 3 27]
REALITY remoteAddr: 127.0.0.1:53912  hs.c.ClientShortId: [193 105 188 176 0 0 0 0]
REALITY remoteAddr: 127.0.0.1:53912  len(s2cSaved): 4096   Server Hello: 1215
REALITY remoteAddr: 127.0.0.1:53912  len(s2cSaved): 2881   Change Cipher Spec: 6
REALITY remoteAddr: 127.0.0.1:53912  len(s2cSaved): 2875   Encrypted Extensions: 41
REALITY remoteAddr: 127.0.0.1:53912  len(s2cSaved): 2834   Certificate: 8273
REALITY remoteAddr: 127.0.0.1:53912  hs.c.isHandshakeComplete.Load(): false
```

客户端侧同一条连接的 `uConn.AuthKey[:16]` 与服务端 `hs.c.AuthKey[:16]` **逐字节相同** ——
**REALITY 认证是成功的**。失败在下一步：服务端把真实 `www.microsoft.com` 的 TLS 握手
转发回客户端时，**Certificate 消息 8273 字节，而可用窗口只剩 2834 字节**。

> **判据（node-provisioning §4.5 应当补上这一条）**：
> **REALITY target 的 TLS Certificate 消息必须能装进 REALITY 的缓冲窗口。**
> 实测分界线在 4.5 KB 附近：

| target | Certificate 字节 | 握手 | xray 告警 |
|---|---|---|---|
| `www.microsoft.com` | **8273** | ❌ | 有 |
| `www.yahoo.co.jp` | 6611 | ✅ | 无 |
| `www.bing.com` | 5021 | ✅ | **无** |
| `www.apple.com` | 4738 | ✅ | 有（apple/icloud） |
| `www.icloud.com` | 4737 | ✅ | 有（apple/icloud） |
| `www.digicert.com` | 4559 | ✅ | 无 |
| `www.lovelive-anime.jp` | 4322 | ✅ | 无 |
| `addons.mozilla.org` | 4133 | ✅ | 无（但 `/` 是 **301**，不满足「无跳转」） |
| `gateway.icloud.com` | 3240 | ✅ | 有 |

⚠️ **`www.microsoft.com` 正是本仓 `local-development.md` 冒烟种子里写的那个值** ——
它做**回落**目标没问题（`openssl s_client` 能正常拿到微软的证书），
做 **REALITY target** 不行。两者是不同的要求，此前文档没有区分。

**选定 `www.bing.com`**：`200 + HTTP/2 + 无跳转`、176 ms、证书 5021 B、
xray 无告警、且是少数在中国可正常访问的境外站。⚠️ 中国可达性未实测。

---

## 6 · B15 判定：mux 与 XTLS-Vision **互斥**

roadmap **B15** 自 2026-08-16 登记「未核实；若互斥，ADR 0004 §3.3 与
system-design §3.1 必须放弃一个」。**同一条订阅、同一台节点、只切 mux**：

| | mihomo（Clash Verge Rev 内核） | sing-box |
|---|---|---|
| 订阅原样（含 `smux` / `multiplex`） | ❌ 连不上 | ❌ 连不上 |
| 仅去掉 mux 块 | ✅ 出口 `35.215.158.52` | ✅ 出口 `35.215.158.52` |

🔴 **也就是说在这一条修掉之前，下发的订阅对所有客户端都是连不上的**，
而失败形态是「能导入、能显示节点、连不上」——**不报错**。

**放弃 mux 保留 Vision**：XTLS-Vision 解决的正是 mux 想解决的那个问题
（TLS-in-TLS 指纹），且它在传输层直接消除内层 TLS 记录特征，
而不是靠多路复用稀释统计特征；两者同开时 Vision 的分流逻辑与 mux 的帧封装互相破坏。

---

## 7 · 端到端实测结果

```
节点          bp-node-hk1 · asia-east2-a · Standard · 35.215.158.52 · v2node v0.4.3 · Xray 26.6.27
面板          GET /api/v2/server/config  → 200 + ETag "c-1"；带 If-None-Match → 304
              GET /api/v1/server/UniProxy/user → 200 {"users":[…1 人…]}；再请求 → 304
              错误密钥 → 401
配置热重载    改 protocol_settings + bump config_rev → 节点 60 s 内自动「重启成功」
客户端        mihomo   出口 IP = 35.215.158.52
              sing-box 出口 IP = 35.215.158.52
吞吐          3 × 5 MB 下载，2.43 – 2.78 MB/s（≈ 2745 KB/s）
流量入账      客户端实下 15,000,000 B → 面板 u+d 增量 15,039,442 B，**误差 0.3%**
在线设备      user_device_state 出现 1 行（按 IP）
节点内存      388 / 1976 MiB（≈ 20%，出口标准 5 的阈值是 < 70%）
```

---

## 代价

- **HY2 与 SS-2022 都没启用。** HY2 需要 Let's Encrypt 证书，而签发要
  `babel.plus` 的 DNS 写权限：本机 `aliyun` CLI 配的是**另一个账号**
  （`IncorrectDomainUser`），`setup-node.sh` 的 cert 步骤又写死 `--dns dns_cf`
  （那是 ADR 0010 的遗留，而 0010 已被 **ADR 0016 否决**）。**两条都要人来解。**
- **单节点、单协议、单区域**：任何一条出问题就是全线中断，没有第二条路径。
- **本次为了让运维账号能验证，直接用 SQL 给它开了套餐**（标准 / 100 GiB / 30 天），
  绕过了订单流程。这是 P1 单人验证的合理做法，但它意味着
  **「下单 → 付款 → 自动开通」这条链仍然一次都没有真的跑过**。
- **`www.bing.com` 的中国可达性未实测** —— 而 target 不可达时的失效形态是
  「回落超时」，比连不上更难诊断。

## 这次没有解决的

- [ ] 72 小时单人验证（出口标准 5）**未开始**
- [ ] 三网路由验收（出口标准 1）**判据本身作废**，见 roadmap B55
- [ ] 封禁 / 配额耗尽 / 到期 三态生效计时（出口标准 6）未做
- [ ] 节点密钥两步轮换演练（出口标准 7）未做
- [ ] HY2 证书（承上）· SS-2022 未启用
- [ ] `node-provisioning §4.5` 的 target 选型标准**未补**「证书链大小」这一条
- [ ] ADR 0004 §3.3 与 system-design §3.1 的 mux 落点**未按 docs/README §4 交代**

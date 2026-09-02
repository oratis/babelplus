# `infra/node/` · 节点建机与装机脚本

> 日期：2026-08-17 · 性质：**执行手册**（脚本的使用说明）
> 状态：**设计稿 v1，未在任何真机上跑通**（2026-08-17）——
> 四个脚本已通过 `shellcheck`（零 error / 零 warning / 零 note）与 `bash -n`，
> 并做过 `--dry-run` 全流程演练；**但没有任何一条会改动 GCP 的命令被真实执行过**。
> 事实源：[docs/04-ops/node-provisioning.md](../../docs/04-ops/node-provisioning.md)（1050 行执行手册，本目录是它的可执行形式）
> 关联：[ADR 0004](../../docs/05-adr/0004-transport-hardening.md)（BBR / mux / 证书 / 区域 / IP）、
> [ADR 0007](../../docs/05-adr/0007-node-migration.md)（新建 `bp-node-hk1`，旧节点原封不动）、
> [as-built-gcp.md §3](../../docs/02-architecture/as-built-gcp.md)（防火墙三风险）、
> [runbook-node-health.md](../../docs/04-ops/runbook-node-health.md)（建成之后的事归它管）

---

## 1 · 四个脚本，一条流水线

```
create-node.sh   →   setup-node.sh   →   （接面板，手工）   →   verify-route.sh
   GCP 侧资源          节点内装机                                🔴 唯一不可跳过的闸
                                                                      │ 不合格
                                                                      ▼
                                                                 rotate-ip.sh
                                                                （约 1 分钟一轮，
                                                                  不重建实例）
```

| 脚本 | 在哪跑 | 干什么 | 幂等 |
|---|---|---|---|
| `create-node.sh` | 本机（有 `gcloud`） | 防火墙 → 服务账号 → 静态 IP 预筛 → 实例 → 建机即刻验收 | 是（已存在则跳过 / 只报不改） |
| `setup-node.sh` | **节点上，root**，经 stdin 灌入 | 9 步装机（sysctl / 基线 / 证书 / v2node / 通路策略 / systemd / 升级 / SSH / 自检） | 是（文件内容无变化则不写、不重启） |
| `verify-route.sh` | 本机编排，**测量全在节点上跑** | J1–J6 路由验收 + 证据落盘 | 是（每次追加样本到台账） |
| `rotate-ip.sh` | 本机 | 换外网 IP + 换完的检查清单 | 否（换 IP 本身不是幂等操作） |

全部支持 `--dry-run`、`--help`、危险操作二次确认（必须**原样敲出节点名**，回车不算数）。

---

## 2 · 前置条件

**任何一项没有答案就停下，不要凭假设推进。**

### 2.1 本机

- `gcloud` 已安装并已 `gcloud auth login`
- 自己已持有 `roles/compute.osAdminLogin` 与 `roles/iap.tunnelResourceAccessor`
  —— 🔴 **必须在开 OS Login 之前授予**，顺序反了就得从 Console 救
- 本机若开着 TUN / fake-ip 代理客户端：不影响脚本（测量全在节点上跑），
  但**影响你手工复核**，见 §5

### 2.2 凭据

凭据只经环境变量传入，**不落盘、不进命令行、不进 shell history、不进实例 metadata**。

```bash
# .env 权限 600，且必须在 .gitignore 里
set -a; source ~/.secrets/bp-node-hk1.env; set +a
```

| 变量 | 谁用 | 说明 |
|---|---|---|
| `BP_PANEL_URL` | setup(v2node) | 面板地址 |
| `BP_NODE_ID` | setup(v2node) | 面板里的节点 ID |
| `BP_NODE_TOKEN` | setup(v2node) | **每节点独立密钥**（面板存哈希，节点存明文） |
| `BP_V2NODE_VERSION` | setup(v2node) | 🔴 必须钉死，脚本拒绝 `latest`，理由见 §6 |
| `BP_CERT_DOMAIN` | setup(cert) | 证书域名。**不要给它建 A 记录** |
| `BP_ACME_EMAIL` | setup(cert) | acme.sh 注册邮箱 |
| `Ali_Key` / `Ali_Secret` | setup(cert) | DNS-01 用（阿里云，ADR 0016）；值在 Secret Manager `bp-aliyun-dns-ali-key` / `-secret`；该 AK 持有 zone 的 DNS 编辑权限 |
| `BP_NODE_ID_2` / `BP_NODE_TOKEN_2` | setup(v2node)，可选 | 同机第二个面板节点（Hysteria2 那一行）。🔴 加之前先用它的 token 手工 GET `/api/v2/server/config` 看 `protocol`：v2node 对多节点是「一个错、全部不起」（2026-09-02 实测，退出码 0，REALITY 一起下线） |
| `BP_SS_PORT` | create + setup | SS-2022 端口。🔴 **脚本硬性拒绝 48882** |
| `BP_REALITY_TARGET` | setup(transport)，可选 | 设了就在节点上实测一次 target 站点 |

🔴 **不要**把 `BP_SS_PSK` / `BP_HY2_OBFS_PASSWORD` / REALITY 的 `privateKey`、`shortId`
传到节点 —— 按 [node-provisioning §4.4](../../docs/04-ops/node-provisioning.md)，
这些协议参数**全部由面板经 `GET /api/v1/server/UniProxy/config` 下发**，
节点本地配置只有面板坐标与密钥。传了只是白白扩大凭据暴露面，`setup-node.sh` 会警告。

### 2.3 四项必须先有答案的核实项（[node-provisioning §2.1](../../docs/04-ops/node-provisioning.md)）

- [ ] 现有防火墙规则的实际 priority —— `create-node.sh` 的 `audit_untagged` 会打出来
- [ ] `asia-east2` 的 `default` 子网当前有没有实例（决定开 IPv6 是否零爆炸半径）
- [ ] 🔴 **v2node 到底承载哪些协议**（决定 Hysteria2 是它管还是要单独装一套）
- [ ] 🔴 **v2node 用什么方式带节点密钥**（query string 还是 `Authorization: Bearer`）

后两项脚本回答不了，需要读 v2node 源码，输出存进 `evidence/v2node-contract-<YYYYMMDD>/`。

---

## 3 · 使用顺序

```bash
cd infra/node

# ── 0. 先看一遍将要做什么（只读查询照常执行，所以能看到真实的防火墙现状）
BP_SS_PORT=<你的端口> ./create-node.sh --dry-run

# ── 1. 建 GCP 侧资源
BP_SS_PORT=<你的端口> ./create-node.sh --candidates 5
#   分阶段跑也行：--only firewall / --only sa / --only address / --only instance / --only verify

# ── 2. 装机（凭据走 stdin，命令行里只出现变量名）
set -a; source ~/.secrets/bp-node-hk1.env; set +a
{
  for v in BP_PANEL_URL BP_NODE_ID BP_NODE_TOKEN BP_CERT_DOMAIN \
           BP_ACME_EMAIL BP_SS_PORT Ali_Key Ali_Secret BP_V2NODE_VERSION; do
    printf 'export %s=%q\n' "$v" "${!v:?缺少环境变量 $v}"
  done
  cat ./setup-node.sh
} | gcloud compute ssh bp-node-hk1 --project=oratis-491316 --zone=asia-east2-a \
      --tunnel-through-iap --command="sudo bash -s -- --all --yes"

# ── 3. 接面板（手工，node-provisioning §6）

# ── 4. 🔴 路由验收
./verify-route.sh --node bp-node-hk1
./verify-route.sh --node bp-node-hk1 --peak          # 19:00–24:00 CST 再跑一次
./verify-route.sh --node bp-node-hk1 --manual-done   # B/C 做完后才允许判「通过」

# ── 5. 不合格就换 IP，再回到第 4 步
./rotate-ip.sh --node bp-node-hk1 --reason "J1 出现跨洋绕行"
```

`${!v:?}` 是 **fail-closed**：缺任何一个变量在连上机器之前就退出，不生成半成品配置。
`printf %q` 保证含特殊字符的密码不会被 shell 二次解释。

---

## 4 · 🔴 网络标签：这一条写错就有安全后果

`oratis-491316` 的 `default` 网络里有三条**没有 target tag** 的规则
（2026-08-17 用 `create-node.sh --dry-run` 实查确认，与 as-built §3 一致）：

| 规则 | 优先级 | 对新 VM 的影响 |
|---|---|---|
| `default-allow-ssh`（`0.0.0.0/0` → tcp:22） | 65534 | **自动放通 22**。压制它的 `vpn-public-ssh-deny` 只覆盖 `vpn-node` 标签 |
| `allow-xray-443`（tcp:443） | 1000 | 自动放通 —— 方便，但这是要切断的隐式耦合 |
| `allow-hysteria-udp443`（udp:443） | 1000 | 同上 |

**所以：新 VM 必须带上一个能被某条 SSH deny 规则命中的标签，否则 22 端口对全网裸奔。**
我们的答案是 `bp-node`，被脚本建的 `bp-public-ssh-deny` 命中。

`create-node.sh` 对此的处置是三步，缺一不可：

1. 建 `bp-public-ssh-deny`（DENY tcp:22，`0.0.0.0/0`，**绑 `bp-node`**，priority 1000）
2. 建 `bp-iap-ssh-allow`（ALLOW tcp:22，`35.235.240.0/20`，**绑 `bp-node`**，priority 900）
3. **建实例之前**跑 `assert_ssh_posture()` 硬闸，核对两个不等式与 target tag，
   不通过就**拒绝建机** —— 实例从 RUNNING 到规则生效之间的窗口不允许存在

两个不等式（写错等于规则白写）：

- `bp-public-ssh-deny`(1000) < `default-allow-ssh`(65534) —— 否则公网 22 依然放通
- `bp-iap-ssh-allow`(900) < `bp-public-ssh-deny`(1000) —— 否则 IAP 隧道也被挡，
  **节点变成无法登录的砖**（GCE 没有能救 SSH 配置的带外 console）

> 缓和一点的事实（但不构成依赖它的理由）：GCE 的 Debian 官方镜像默认
> `PasswordAuthentication no`，所以裸奔的 22 不等于当场沦陷。
> **但这是「碰巧安全」，不是「设计安全」。**

**另外两条 `bp-allow-*-443` 是明知冗余仍要建的。** as-built §3 的处置建议本身就写着
要给那两条无 tag 规则**补 target tag** 做收敛 —— 这是正确的安全动作，早晚会做。
一旦执行，bp 节点**毫无预警地瞬时失去 443 入向**，而现象（443 无响应、服务端零入站连接、
进程 active）与 **IP 级封锁的三条取证特征完全吻合**，排障会走到「释放 IP 重开机器」上去。
用 40 秒建两条冗余规则，换掉一个能造成半小时误诊的跨系统耦合。

---

## 5 · 🔴 不要用 `dig` / `nc` / `curl` 判断连通性

**本机开着 TUN / fake-ip 时，`ping` / `dig` / `nslookup` / `nc` / `curl --interface`
的结果全部被客户端劫持，不能作为任何判断依据。**
Proxy_Skill 记录过一次对照实验：连 `baidu.com` 的**正对照也失败** ——
说明失败来自本机劫持，不是链路问题。

正确做法三条，按可信度排序：

1. 在**节点自己**身上测出向路径（`verify-route.sh` 做的就是这个）—— 机器是我们的，没有 TUN
2. 用一台**从未装过代理客户端**的机器测入向
3. 需要「经由节点的连通性/延迟」这类数据时，走**客户端内核的 API**
   （mihomo 的 delay 接口；Clash Verge 的 unix socket 在 `/tmp/verge/verge-mihomo.sock`）

退出代理客户端时要**退出进程**，不是切「直连」模式 ——
fake-ip 的 DNS 劫持在直连模式下依然可能生效。

`verify-route.sh` 会在本机检测 TUN 迹象并警告（不阻断，因为本机不参与测量）。
它自动化的只有数据源 A（节点侧 mtr/ping，**只测得到出向**）；
数据源 B（国内公开多点测速站，唯一能拿到**入向**数据的来源）与 C（自己的国内机器）
必须人工做，**做完加 `--manual-done` 脚本才允许输出「通过」**。
理由：中国方向非对称路由是常态，入向由中国运营商的 BGP 决策决定，我们完全无法控制 ——
**A 好看而 B 难看完全可能发生，而用户体验由 B 决定。**

---

## 6 · 🔴 版本地雷（每次建机与每次升级都要走一遍）

### 6.1 mihomo 已放弃与 Xray ≥ v26.7.11 的 REALITY 兼容

mihomo 官方原话（**已核实**）：

> "Due to xray-core's deliberately incompatible behavior, we will not consider
> compatibility with xray v26.7.11+ versions."

**而 mihomo 是 Clash Verge Rev 的内核** —— 服务端的 xray-core 版本直接决定了
一大批客户端能不能连上 REALITY，且 mihomo 明确表示不会修。

本项目的复杂之处：**我们不单独装 Xray。** v2node 是「改版 xray-core」，
xray-core 是它 vendor 进去的依赖。所以真实形态是：

> **v2node 的某个版本升级，可能在没有任何提示的情况下把 vendored xray-core
> 带过 v26.7.11，于是所有 Clash Verge Rev 用户在下一次节点重启后集体连不上 REALITY。**

处置：

1. `BP_V2NODE_VERSION` **必须钉死**（`setup-node.sh` 硬性拒绝 `latest`）；
   升级前先查 `go.mod` 里 `xtls/xray-core` 的版本
2. 注意 Xray 的 **v26.4.x–v26.7.28 均以 prerelease 发布** ——
   任何「取 latest release」的自动化都会踩坑。当前最新非预发布版是 **v26.3.27**（2026-03-27）
3. **每次升级前用真实 mihomo 客户端回归测试一次 REALITY 连通性**（无法自动化断言）

### 6.2 Xray 字段已改名，且保留静默别名

**已核实**，来自 `xtls.github.io`：

| 位置 | 旧字段名 | **新字段名** |
|---|---|---|
| `streamSettings` | `network` | **`method`** |
| `streamSettings.method` 取值 | `tcp`（+`tcpSettings`） | **`raw`**（+`rawSettings`） |
| `settings`（VLESS inbound） | `clients` | **`users`** |
| `realitySettings`（inbound） | `dest` | **`target`** |
| `realitySettings`（outbound） | `publicKey` | **`password`** |

**旧名仍作为别名被接受**（源码里有 `if c.Clients != nil { c.Users = c.Clients }` 这样的
兼容分支），所以**写错不报错，只是行为不符预期**。
这是本项目最容易产生「查不出来的 bug」的一处。

`setup-node.sh` 的自检里有 `check_field_renames()`：扫 `/etc/bp` 等目录，
命中旧名即报 FAIL。但它**只覆盖本地文件** —— 面板下发的配置在节点上的落点**待核实**，
所以扫不到不算通过，只算「无法判定」。权威做法是在生成器侧加 CI grep +
起真实 v2node 容器做契约测试。

> `publicKey` → `password` 不是美学改名：该字段确实是 x25519 公钥，
> 但在 REALITY 的设计里它是**客户端持有的秘密，持有它即可探测 REALITY 服务器**。
> 叫它 publicKey 会诱导用户随手分享。

### 6.3 其余四条

| # | 地雷 | 后果 |
|---|---|---|
| 1 | mihomo 的 `client-fingerprint`（uTLS 指纹，取值 `chrome`/`iOS`，**大小写敏感**）与 `fingerprint`（**证书 SHA-256 pin**）是完全不同的东西 | 难以排查的连接失败 |
| 2 | sing-box 的 obfs `gecko` 只在开发线 1.14 文档中存在，1.13.18 只认 `salamander` | 下发 `gecko` = 客户端加载失败 |
| 3 | Hysteria2 的 **userpass** 认证在 sing-box 里没有别名，必须输出 `"password": "user:pass"` | 订阅生成器不处理 = sing-box 用户全体认证失败 |
| 4 | 🔴 **mux 与 XTLS-Vision 能否共存，未核实** | ADR 0004 §3.3 裁定 TCP 路径启用 mux，而默认通路是 VLESS+XTLS-Vision+REALITY。若两者互斥，这两条裁决必须放弃一个。判定方法：用真实 mihomo 与 sing-box 各加载一次 |
| 5 | 面板 `tls_settings.private_key` 填 `xray x25519` 的 **`PrivateKey:`** 行 | `Password (PublicKey):` 是给客户端的；`Hash32:` 是 VLESS Encryption 用的，**与 REALITY 无关，误填即不通** |

---

## 7 · 建机检查清单

复制到工单里逐条勾。**顺序即依赖顺序，不要跳步。最后一项不可跳过。**

**前置**

- [ ] 跑清点命令存下**变更前**快照（`create-node.sh` 的 `--only firewall` 阶段自动做）
- [ ] §2.3 四项核实全部有答案，**没有一项标着「假设」**
- [ ] v2node 三条核实已跑，输出存进 `evidence/v2node-contract-<YYYYMMDD>/`
- [ ] `.env` 已备齐并 `chmod 600`，且在 `.gitignore` 内；确认凭据**不会**出现在命令行或 history
- [ ] 给自己授 `roles/compute.osAdminLogin` + `roles/iap.tunnelResourceAccessor`（**在开 OS Login 之前**）

**建机（`create-node.sh`）**

- [ ] 先跑一次 `--dry-run`，读一遍 `audit_untagged` 打出的现存无 tag 规则
- [ ] 建 4（+1 SS，+2 IPv6）条 `bp-*` 防火墙规则，**全部绑 `--target-tags=bp-node`**
- [ ] 两个不等式成立：deny(1000) < `default-allow-ssh`(65534)；IAP allow(900) < deny(1000)
- [ ] 建 `bp-node-sa`，**确认它没有任何 IAM 角色绑定**
- [ ] 批量预留 5 个静态 IP → 看网段 → 留 1 删 4（优先 `35.220.x`，避开 `34.92.x`）
      —— ⚠️ **网段预筛只是先验，不是验收**
- [ ] zone 是 `-a` 或 `-c`，**不是 `-b`**（脚本会拒绝 `-b`）
- [ ] 创建实例：`e2-small` / `debian-12` / `PREMIUM` / **`--tags=bp-node`** /
      `--service-account=bp-node-sa@...` / `--deletion-protection`
- [ ] 决定是否开 IPv6；若开，`--ipv6 --allow-subnet-mutation`，
      并**确认没有给 22 加任何 IPv6 allow**
- [ ] IAP SSH 通
- [ ] **公网 22 从境外测被拒**（`get-effective-firewalls` 与实测都要看）
- [ ] 清点 diff：`vpn-*` 与三个 Cloud Run 服务**零变化**

**装机（`setup-node.sh`）**

- [ ] 用 stdin 方式执行（凭据不进命令行）
- [ ] `sysctl net.ipv4.tcp_congestion_control` = `bbr`，`default_qdisc` = `fq`
- [ ] 时间已同步（`timedatectl show -p NTPSynchronized`）
- [ ] v2node 版本已钉死，二进制 sha256 已记录（`/etc/bp/v2node.sha256`）
- [ ] 证书 issuer 是 **Let's Encrypt**，不是 GTS
- [ ] 证书域名**没有 A 记录**；订阅里节点地址填 IP、`sni` 填证书域名
- [ ] systemd unit 含 `LoadCredential` + `DynamicUser=true` + `ProtectSystem=strict` +
      `NoNewPrivileges=true` + **`AmbientCapabilities=CAP_NET_BIND_SERVICE`**
- [ ] `unattended-upgrades` 已装且 **`Automatic-Reboot "false"`**
- [ ] SSH drop-in 生效，`sshd -t` 通过，用 `reload` 不用 `restart`
- [ ] 自检：`systemctl is-active` ✅；`ss -tulnp` 显示 tcp:443 + udp:443 + SS 端口
- [ ] `journalctl -k | grep -i 'out of memory'` 为空；`free -m` 基线已抄进 `evidence/`
- [ ] **未安装 cloudflared**，**未复制任何旧节点的 tunnel token**

**接面板（手工）**

- [ ] 节点密钥独立生成，面板存**哈希**，明文只在节点配置与 `.env`
- [ ] `private_key` 填的是 `PrivateKey:` 行**不是** `Hash32:`
- [ ] `up_mbps` / `down_mbps` **留空**（= BBR，不是 Brutal）
- [ ] 配置里使用**新字段名**（`users` / `target` / `password` / `method` / `raw`）
- [ ] 轮询验证：60 秒一次 `config` + `user`，`push` 有数据入账
- [ ] 🔴 **ETag 验证**：180 秒内出现 1×`200` + 2×`304`；若全是 `200` 则 v2node 不发
      `If-None-Match`，整套 ETag 设计一行都不生效
- [ ] 用真实 **mihomo**（Clash Verge Rev）与 **sing-box** 各加载一次订阅并实连成功

**最后一项（不可跳过）**

- [ ] 🔴 **三网路由验收通过**：`./verify-route.sh --node <node>` 的 J1–J3 硬判据全过、
      J6 证书是 Let's Encrypt，J4/J5 警告已记录，**含一次晚高峰（19:00–24:00 CST）采样**，
      数据源 B/C 已完成并加了 `--manual-done`；
      全部原始输出（**含不合格样本**）已入 `evidence/node-route-<node>-<YYYYMMDD>/`，
      README 写明证明什么、不证明什么

---

## 8 · 代价

> ⚠️ 这一选择的代价，必须留在记录里而不是被措辞掩盖：
>
> 1. **这是「脚本化的手敲」，不是 IaC。** 四个 shell 脚本把手册里的命令序列变成了
>    可重复执行的东西，但仍然**没有状态、不可 diff、不可重放** ——
>    `create-node.sh` 不知道上次建了什么，它只会「describe 一下，不在就建」。
>    这意味着**漂移检测不存在**：有人手工改了一条 `bp-*` 规则的 source-ranges，
>    脚本只核对 priority 与 target tag，改动会被漏掉。
>    IaC 属 P4 阶段，在那之前每一台节点都靠人不出错。
> 2. **四个脚本各自复制了一份守卫代码**（`assert_target_safe` / `run` / `confirm_typed`
>    在三个脚本里几乎一样）。没有抽成 `lib.sh` 的原因是 `setup-node.sh` 从 stdin
>    灌进 `sudo bash -s`，**它没有兄弟文件可以 source**，必须自包含。
>    代价是改守卫逻辑要改三处，且它们可能悄悄分叉。
> 3. **`--dry-run` 只覆盖「有副作用的命令」，只读查询照常执行。**
>    好处是预演能看到真实状态；代价是 dry-run **需要有效的 gcloud 凭据**，
>    且在权限不足时会静默降级成「查不到 → 当作不存在」，
>    从而给出与真实执行不同的计划。
> 4. **`setup-node.sh` 的证书步骤把凭据落了盘。** acme.sh 把 `Ali_Key` / `Ali_Secret` 存进
>    `~/.acme.sh/account.conf` 以便续期 —— 这是「凭据不落盘」的一个真实例外。
>    `BP_ACME_NO_PERSIST=1` 可以抹掉它，代价是**自动续期从此失效**，
>    续期时必须重新注入 token，而 Hysteria2 没有证书就完全不可用。
>    两条路都有代价，脚本选了默认保留 + 目录 700 + 明确记录。
> 5. **`curl … | sh` 装 acme.sh 是一条未钉版本的供应链依赖。**
>    照抄自手册。理想做法是钉 release tarball + 校验 sha256，但 acme.sh 的官方
>    安装路径就是这个，改掉它会偏离上游文档。**这一条应当在第一次真机执行后复审。**
> 6. **`verify-route.sh` 自动化的只有三分之一。** 数据源 A 只测得到出向；
>    B（唯一的入向来源）与 C 靠人，脚本只能用 `--manual-done` 这个**荣誉制开关**
>    来卡住结论 —— 它挡得住忘记，挡不住敷衍。
> 7. **J1–J6 六条判据全部是「设定值」，不来自任何测量。** 而 J1（跨洋绕行）
>    建立在两条 2019 / 2022 年的社区单一来源之上。
>    **若实测显示同区域 IP 段之间的路由差异并不显著，这整套流程就是纯亏。**
>    复审条件写死：**连续 5 个新 IP 的三网中位 RTT 极差都 < 20 ms，
>    则把逐 IP 全量验收降级为抽检。**
> 8. **J2 的口径与手册有一处已知偏差。** 手册写「中位 RTT」，而 `mtr -r` 报的是
>    **算术平均**。脚本改用 `ping` 的逐包值自算中位数，这是与手册文本不一致的实现 ——
>    **要回写进 node-provisioning §5.2**，否则下一个人会以为两者是同一个数。
> 9. **版本钉死 = 主动放弃上游安全修复。** 参照点：Xray 当前最新非预发布版
>    v26.3.27 发布于 2026-03-27。换来的是 mihomo 系客户端能连上 REALITY。
>    **一旦 xray-core 出现 CVE 级别修复，这个取舍必须重新裁决，而不是默认续期。**
> 10. **`setup-node.sh` 的 swap 与 `MemoryHigh` 都是手册自拟的提案，没有被任何 ADR 裁决过。**
>     它们把「瞬时全员掉线」换成「缓慢劣化」，而缓慢劣化在用户侧表现为「时快时慢」，
>     **比干脆掉线更难定位、更容易被误判成运营商 QoS**。
>     若 30 天内 swap 使用始终为 0、`MemoryHigh` 从未触发，应当撤掉而不是留着当护身符。

## 9 · 这次没有解决的

- [ ] 🔴 **四个脚本一行真实变更都没跑过。** shellcheck 零 error、`bash -n` 通过、
      `--dry-run` 全流程演练通过，但这三件事**都不能证明 `gcloud compute instances create`
      的参数组合是对的**。第一次真机执行必须逐条记录偏差并回写本文与手册。
- [ ] **`--ipv6` 整段未验证。** `--ipv6-access-type` / `--ipv6-network-tier` 参数名
      **待核实**，「实例 stack-type 能否事后变更」同样**待核实**。
      这一条卡着 ADR 0004 §3.7（论据最弱的裁决）的复审。
- [ ] **`v2node.json` 的字段名与 `NodeID` 的类型（数字还是字符串）待核实** ——
      必须以所钉 tag 的 `config.json.example` 为准。写错的表现是启动即失败或静默不上报。
- [ ] **面板下发的配置在节点上的落点未知**，所以 `check_field_renames()` 扫不到它，
      「配置里没有旧字段名」这件事在节点侧**无法被证明**。
- [ ] **`verify-route.sh` 的三个探测目标是占位示例**，归属与可达性**待核实**。
      第一次执行时要按实际选定的探测点替换，并把最终列表回写进 §5.2。
- [ ] **没有「装机后 → 接面板前」的等待/重试逻辑。** `setup-node.sh` 自检时
      `bp-node.service` 很可能因为面板里还没有这个节点而反复重启，
      脚本会报 FAIL —— 这是**预期内的假阳性**，但脚本没有区分它和真故障。
- [ ] **节点退役 / 删除脚本没写。** 本目录只覆盖建机与换 IP。
      按 ADR 0007 裁决第 7 条，退役需要另写 ADR，且旧端点必须与新端点并行存活 ≥ 7 天。
- [ ] **密钥轮换脚本没写**，因为它依赖面板侧尚不存在的两步流程
      （先下发新密钥 → 确认节点已用新密钥上报 → 再撤旧的）。
- [ ] **`rotate-ip.sh` 不会自动触发订阅重新生成与邮件广播** ——
      它只打印清单。那两件事需要 `bp-api` 侧的接口，现在还不存在。
- [ ] **「待重启节点」的巡检项不存在**（`unattended-upgrades` 关掉了自动重启，
      但没人负责补上手动重启）。`setup-node.sh` 只在装机当时看一眼
      `/var/run/reboot-required`，日常巡检没有载体。
- [ ] **没有漂移检测。** 见代价第 1 条。

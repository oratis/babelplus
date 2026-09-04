# E0 · HTTPS 入站的每用户计量验证：一次可判真伪的观察，答案只有「查得到」或「停」

> 日期：2026-09-04 · 性质：**执行手册** · 状态：**已执行一次，判定「不通过」**（2026-09-04，用户指示提前于 72 h 窗口到点执行；
> 事后核对窗口未受影响 —— 心跳最大间隔 61 s、内存峰值 24.9%）。**结果与三条出路见
> [evidence/e0-metering-20260904](../evidence/e0-metering-20260904/)。**
> 本文保留为「下一次怎么重跑」的手册；⚠️ §2.1 的做法已被实测改进：
> **Caddy 要在开发机上交叉编译后只拷二进制**，不要在节点上装 Go 与构建（那才是内存判据的真实威胁）。
> 事实基线：[client-products-spec §6.1](../03-product/client-products-spec.md) 的 E0 与 §3.7 的计量待核实项；
> [roadmap **B66**](../00-overview/roadmap.md)；v2node v0.4.3 的协议白名单（2026-09-02 真机实测）
> 关联：[`infra/node/setup-https-inbound.sh`](../../infra/node/setup-https-inbound.sh)（起入站）、
> [`web/extension/README.md` §6](../../web/extension/README.md)（这道门挡着什么）
> 读者：跑这次验证的人。**§0 与 §1 先读完再动手。**

---

## 0 · 这份文档要回答的那一个问题

> **一个真人经 HTTPS 代理入站产生的 100 MB，能不能出现在 `stat_user_server` 里？**

不能，就**停**。理由不是洁癖：扩展按配额售卖，而配额靠 `stat_user_server` 扣。
计量不通就等于「用户买了 20 GB，实际用多少我们不知道」——
那不是一个可以边跑边补的缺口，是一条**无界泄漏**。

🔴 **为什么这件事从一开始就可疑**：v2node v0.4.3 的 `GetNodeInfo` 只认
`vmess / trojan / hysteria2 / tuic / anytls / vless / shadowsocks`，**没有普通 http**
（2026-09-02 实测：给它一个不认识的 protocol，整个进程以退出码 0 退出）。
所以 HTTPS 代理只能是**另一个进程**（Caddy），而另一个进程的字节数
天然不在 v2node 的账里 —— 这正是 E0 要证伪或证实的东西。

## 1 · 跑之前必须为真的四件事

- [ ] **72 小时观察窗已到点**（roadmap §4.3 出口标准 5，终点 2026-09-05T07:05Z）。
      窗口期间在那台机器上装进程 = 用一天换重跑 72 小时。
- [ ] 手上有一个**可登录且有配额**的账号（不能用哨兵 `drill-sentinel@babel.plus`，它 `password_hash='!'`）。
- [ ] 知道要开的端口，且**不是 443**（那是 REALITY 与 HY2 的，撞了就是全线中断）。
- [ ] 防火墙那一条规则是你自己决定要不要开的 —— 脚本刻意不碰防火墙。

## 2 · 步骤

### 2.1 起入站

```bash
# 在节点上（变量走 stdin，不进 argv —— 与 setup-node.sh 同一套路）
BP_PROXY_PORT=8443 \
BP_PROXY_USER=<临时用户名> BP_PROXY_PASS=<临时密码> \
BP_CERT_DOMAIN=hk1.babel.plus \
BP_PROBE_TARGET=www.bing.com \
./setup-https-inbound.sh --apply
```

自检只证明「进程起来了、无凭据访问不返 407」。**它不证明计量**。

### 2.2 记下基线

```sql
-- 经 cloud-sql-proxy 连 bp-db（注意 zsh 的 ${P}:us-central1 写法，见 first-deploy）
SELECT server_id, u, d, u + d AS total
FROM stat_user_server
WHERE user_id = <你的 user_id>
ORDER BY server_id;
```

同时记下时间戳：`date -u +%FT%TZ`。

### 2.3 经入站产生 100 MB

```bash
curl -x "https://<用户名>:<密码>@<节点IP>:8443" --proxy-insecure \
     -o /dev/null -w '%{size_download} bytes in %{time_total}s\n' \
     https://speed.cloudflare.com/__down?bytes=104857600
```

⚠️ 走的必须是 **CONNECT 到 HTTPS 目标**，不是明文 HTTP —— 后者的字节数在代理侧的统计口径不同。

### 2.4 判定

等**两个** `/push` 周期（`base_config.push_interval` = 60 s，所以至少 2 分钟），再查一次 §2.2 的表。

| 观察 | 判定 | 下一步 |
|---|---|---|
| `u + d` 增加 ≈ 100 MB（±5%） | ✅ **E0 通过** | 开 E1：凭据派生、`getUserProxyConfig` 真实现、`probe_url` 服务 |
| 完全没变 | 🔴 **E0 不通过**（最可能的结果，理由见 §0） | **停**。要么让 HTTPS 入站的账进 UniProxy 的上报路径（需要一条独立上报通路写进 `stat_user_server`），要么放弃扩展这条传输 |
| 变了但对不上（差一个数量级 / 只算了一半） | 🔴 同样是不通过 | 先把口径查清楚再谈；一个不准的计量比没有计量更危险 —— 它会让人以为有 |

**判定必须写进 evidence 目录**（`docs/evidence/e0-metering-<日期>/`），格式照
[node-bringup-20260901](../evidence/node-bringup-20260901/)：命令原样、输出原样、结论一句话。
「跑过了、好像行」不是判定。

## 3 · 收尾

不论结果如何：

```bash
systemctl disable --now bp-httpsproxy.service   # E0 是一次观察，不是一次上线
```

🔴 **在 evidence 写下「查得到」之前，不要把这条入站写进任何下发路径**
（`getUserProxyConfig` 现在返回 501，它就是这道门的执行形式，钉在
`api/internal/handler/unimplemented_test.go`）。

## 代价

- 这次验证会在生产节点上装一个新进程、占一个端口、跑一次 100 MB 的真实流量
  （按 `c = $0.121/GiB` 算约 $0.012，可忽略；真正的代价是**在唯一一台节点上动手**）。
- Caddy 用 xcaddy 在节点上现构建，慢（几分钟）且要临时装 Go。
  换来的是「这个二进制是哪来的」有答案 —— 不从来历不明的第三方构建里拉一个代理进程。
- 临时 Basic 凭据是**手工的**，与 spec §3.7 的「由订阅 token 派生」不是一回事。
  E0 只回答计量问题，不验证凭据方案。

## 这次没有解决的

- [ ] 计量若不通，**替代方案未设计**：一条独立上报通路要写进 `stat_user_server`，
      而那意味着一个新的、与 UniProxy 并行的信任边界。
- [ ] `probe_resistance` 的回落站点用哪个域名、放什么内容 —— 未定
      （naiveproxy issue #97 记录过参考配置本身可被探测）。
- [ ] HTTPS 入站在大陆的存活期（spec 的 E5）—— 需要两周对照，本文不涉及。

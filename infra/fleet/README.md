# `infra/fleet/` · 自用机队（`vpn-*`）的工具链

> 日期：2026-09-04 · 性质：**执行手册**（脚本的使用说明）
> 状态：**部分实现**（2026-09-04）—— 见 §2 的实现台账。
> **除 `feishu-notify.sh` 的只读路径外，本目录没有任何东西在真机上跑过。**
> 事实源：[docs/04-ops/personal-fleet-runbook.md](../../docs/04-ops/personal-fleet-runbook.md)
> 关联：[ADR 0017](../../docs/05-adr/0017-personal-fleet-in-repo.md)（本目录存在的裁决）、
> [as-built-personal-fleet.md](../../docs/02-architecture/as-built-personal-fleet.md)（现状）、
> [`infra/node/`](../node/)（商用队的同类目录；本目录**复用它的守卫逻辑，不复用它的前缀**）

---

## 1 · 🔴 这个目录和 `infra/node/` 是两支机队，不要混

| | `infra/node/`（商用队） | `infra/fleet/`（自用队，本目录） |
|---|---|---|
| 资源前缀 | `bp-*` | `vpn-*` |
| 网络标签 | `bp-node` | `vpn-node` |
| 网络层级 | **硬编码 `STANDARD`**（ADR 0008 明令不给开关） | **可选**（要跑 A/B） |
| 配置下发 | v2node ← 面板 | 本地生成器 → 边缘 |
| 服务对象 | babel.plus 付费用户 | 用户本人 |

[ADR 0017](../../docs/05-adr/0017-personal-fleet-in-repo.md) 的裁决是
**「同仓不同队」：共享设计与工具，不共享任何一份 GCP 资源、订阅通路或计费。**

⚠️ **隔离靠的是命名前缀、网络标签和一个只读脚本，没有任何一层是 GCP 强制的**
（两支机队同在 `oratis-491316` 一个 project、同一个 VPC）。
一条 `--target-tags` 打错的命令就能让付费用户的流量落到自用机器上。ADR 0017 §8 代价第 1 条。

---

## 2 · 实现台账（**看这一栏再动手**）

| 文件 | 干什么 | 状态 |
|---|---|---|
| `feishu-notify.sh` + `_py/` | 飞书出口：自检 / 列会话 / 反查 open_id / 发文本 / 发卡片 | ✅ **只读路径已实测**（`--bot-info`、`--list-chats`、两条 fail-closed 分支）。**发送路径未测**：还没有收件人 |
| `fleet.example.json` | 机队清单模板（非机密拓扑） | ✅ 结构已定，`vpn-sg` / `vpn-ops` 标 `planned` |
| `healthcheck.sh` | 节点侧每日巡检（五组判据 + systemd timer） | 🔴 **未实现**。规格见 runbook §3.2 |
| `daily-report.py` | 汇总 + 组卡片 + 发飞书（`--local` 与 Worker 两条路径共用渲染） | 🔴 **未实现**。卡片格式见 runbook §3.4 |
| `gen-subscription.py` | 从 `fleet.json` + secrets 渲染四种订阅产物 | 🔴 **未实现**。约束见 runbook §2.2 |
| `publish-subscription.sh` | 把产物 PUT 进 Cloudflare KV | 🔴 **未实现** |
| `worker/` | Cloudflare Worker：订阅下发 + 巡检 ingest + 日报 cron | 🔴 **未实现**。接口见 runbook §2.3 |
| `create-vpn-node.sh` | 建自用队节点（`infra/node/create-node.sh` 的 `vpn-` 变体） | 🔴 **未实现** |

---

## 3 · 凭据

```bash
cp fleet.example.json fleet.json          # 两者都可编辑；fleet.json 已 gitignore
$EDITOR .secrets.env && chmod 600 .secrets.env
set -a; source infra/fleet/.secrets.env; set +a
```

`.secrets*.env`、`fleet.json`、`out/` 全部在 `.gitignore` 里（仓库根，ADR 0017 §3）。

| 变量 | 谁用 | 说明 |
|---|---|---|
| `FEISHU_APP_ID` / `FEISHU_APP_SECRET` | `feishu-notify.sh` | 应用「胖狗」。🔴 **不下发到任何代理节点** |
| `FEISHU_RECEIVE_ID` / `FEISHU_RECEIVE_ID_TYPE` | 同上 | 私聊用 `open_id`；或"只有你和胖狗的群"的 `chat_id` |
| `STATIC_IP` / `SS_*` / `REALITY_*` / `CDN_*` | `gen-subscription.py` | 美国侧，沿用 Proxy_Skill 的规范名 |
| `JP_*` / `SG_*` | 同上 | 日本 / 新加坡侧（`secrets_prefix` 在 `fleet.json` 里声明） |
| `CF_API_TOKEN` / `CF_ACCOUNT_ID` / `CF_KV_NAMESPACE_ID` | `publish-subscription.sh` | Cloudflare |
| `DEVICE_TOKEN_<ID>` | 两者 | **每台设备一个** —— 为的是吊销（丢一台不影响其余五台），不是计费 |

> ⚠️ Proxy_Skill 有过一次**变量名不一致**导致生成器长期无法运行的历史
> （`CDN_ADDR` / `CDN_WSPATH` / `JP_IP` vs 脚本期望的规范名）。
> 新增变量一律用 [as-built §5](../../docs/02-architecture/as-built-personal-fleet.md) 的规范名。

---

## 4 · 飞书：现在就能跑的三条

```bash
set -a; source infra/fleet/.secrets.env; set +a

./feishu-notify.sh --bot-info      # 应打出 app_name=胖狗、activate_status=2
./feishu-notify.sh --list-chats    # 拿 chat_id
./feishu-notify.sh --text "机队工具链自检"
```

**拿收件人的两条路**（🔴 目前两条都还没走通，日报没有收件人就发不出去）：

- **路 A**：`./feishu-notify.sh --whoami <你的飞书邮箱或手机号>`
  —— 需要应用有 `contact:user.id:readonly`。采集会话中这条被权限策略拦下，**未验证**。
- **路 B（推荐，零额外权限）**：在飞书里建一个**只有你和胖狗**的群，
  然后 `--list-chats` 取它的 `chat_id`。`im/v1/chats` 接口**已实测可用**。

🔴 **不要发到现有的 6 个群**（`产品小队` / `组织建设讨论` / `HakkoAI专项` / `产品+UI` /
`龙虾养殖场` / `Github权限和PR Review`）—— 里面有其他人，而日报含节点名、静态 IP、端口与流量。

🔴 **App Secret 在 2026-09-04 的一次对话中以明文出现过。**
建议在飞书后台重置一次，新值直接写进 `.secrets.env`，不要再经过任何对话。

---

## 5 · 代价

> ⚠️ 这一选择的代价，必须留在记录里而不是被措辞掩盖：
>
> 1. **本目录复制了一份 `infra/node/` 的守卫逻辑，而不是抽公共库。**
>    理由与 `infra/node/README.md` §8 代价第 2 条相同（`setup-*.sh` 从 stdin 灌进
>    `sudo bash -s`，没有兄弟文件可以 source，必须自包含），
>    代价是**改守卫逻辑要改两个目录，而它们会悄悄分叉**。
> 2. **`feishu-notify.sh` 每次调用都重新取 `tenant_access_token`**（有效期 7200s）。
>    多花一次往返，换的是 token 不落盘。日报每天一次，这个取舍是划算的；
>    **如果将来有高频调用场景，这条要重新裁决**，而不是顺手加个缓存文件。
> 3. **`_py/` 把 Python 片段拆成独立文件，而不是内联 `python3 -c`。**
>    内联版本在第一次编写时就因为**单引号 shell 串里的转义**而语法错误
>    —— 那类 bug 只在运行时暴露，且错误信息指向 `<string>`。
>    代价是目录里多了 6 个小文件，且 `feishu-notify.sh` **不再能单文件灌进 stdin**。

## 6 · 这次没有解决的

- [ ] 🔴 §2 台账里 6 个 🔴 全部未实现。
- [ ] 🔴 **收件人未取得** —— §4 的两条路都没走通。
- [ ] 🔴 **App Secret 未轮换**。
- [ ] **`verify-isolation.sh` 还没有改成读 `fleet.json`**（ADR 0017 §3），
      加第三、第四台自用节点会让它误报。**这是建 `vpn-sg` 的前置，不能并行。**
- [ ] **`feishu-notify.sh` 的发送路径（`--text` / `--card`）未跑过真实调用。**

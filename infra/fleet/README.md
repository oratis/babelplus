# `infra/fleet/` · 自用机队（`vpn-*`）的工具链

> 日期：2026-09-05 · 性质：**执行手册**（脚本的使用说明）
> 状态：**已实现并在真机上跑过**（2026-09-05）—— 见 §2 的实现台账，每一行都写了「跑没跑过、在哪跑的」。
> 事实源：[docs/04-ops/personal-fleet-runbook.md](../../docs/04-ops/personal-fleet-runbook.md)
> 关联：[ADR 0017](../../docs/05-adr/0017-personal-fleet-in-repo.md)（本目录存在的裁决，用户 2026-09-05 按修订批准）、
> [as-built-personal-fleet.md](../../docs/02-architecture/as-built-personal-fleet.md)（现状）、
> [`infra/node/`](../node/)（商用队的同类目录；本目录**复用它的守卫逻辑，不复用它的前缀**）

---

## 1 · 🔴 这个目录和 `infra/node/` 是两支机队，不要混

| | `infra/node/`（商用队） | `infra/fleet/`（自用队，本目录） |
|---|---|---|
| 资源前缀 | `bp-*` | `vpn-*` |
| 网络标签 | `bp-node` | `vpn-node` |
| 服务账号 | `bp-node-sa`（零角色） | `vpn-node-sa`（只有 `logging.logWriter` + `monitoring.metricWriter`） |
| 网络层级 | **硬编码 `STANDARD`**（ADR 0008 明令不给开关） | **必须显式 `--network-tier`**（要跑 A/B） |
| 配置下发 | v2node ← 面板 | 本地生成器 → Cloudflare Worker + KV |
| 服务对象 | babel.plus 付费用户 | 用户本人 |

[ADR 0017](../../docs/05-adr/0017-personal-fleet-in-repo.md) 的裁决是**「同仓不同队」：共享设计与工具，不共享任何一份 GCP 资源、订阅通路或计费。**
隔离由 `infra/scripts/verify-isolation.sh` 守（期望读 [`fleet.json`](fleet.json)），唯一一条 GCP 强制的隔离是 `vpn-deny-from-bp`。

---

## 2 · 实现台账（**看这一栏再动手**）

| 文件 | 干什么 | 状态（2026-09-05） |
|---|---|---|
| `fleet.json` | 机队清单：非机密拓扑 + 隔离期望 + 订阅/日报参数。**入库**（D7），是 `verify-isolation.sh` / `gen-subscription.py` / Worker `/fleet` 的唯一事实源 | ✅ 3 台（`vpn-jp` / `vpn-us` / `vpn-sg`）全部 `running` |
| `worker/` | Cloudflare Worker `fleet-sub`：订阅下发 `GET /p/<token>/<file>`、巡检 `POST /ingest/<token>`、`GET /fleet`、cron 日报（应用「胖猫」优先，webhook 备选）、`/admin/*` | ✅ 已部署 `https://fleet-sub.oratisoratisoratisoratis.workers.dev`（KV `df18867b…`）；订阅头实测正确；cron `37 0 * * *` 已挂；**首条真实日报 2026-09-05 15:53 CST 经应用私聊发送成功（code 0）** |
| `gen-subscription.py` | 从 `fleet.json` + `.secrets.env` 渲染四种产物（+ 自托管 CN CIDR） | ✅ 渲染 10 条（公告伪节点 + 9 通路，含 `vpn-sg` 的 SG-HY2 / SG-Reality）；**真机客户端未加载** |
| `publish-subscription.sh` | 产物 / 设备 token / 节点 token / fleet 副本 → KV；`--revoke`、`--refresh-cn-cidr`、`--list` | ✅ 已发布；`curl` 实测 200 + `subscription-userinfo`；未知 token 404 |
| `healthcheck.sh` | 节点侧五组巡检 → `/var/lib/fleet/latest.json` → `POST /ingest` | ✅ 三台都跑过并上报成功，互探全通 |
| `healthcheck-install.sh` | 本机执行：IAP SSH + stdin 把上一项装成 systemd timer（每小时 :30 UTC，23:30 为 daily） | ✅ 三台已装 |
| `daily-report.py` | 本机侧：`--preview` / `--send`（从 Worker 取卡片或 `--source nodes` 本地渲染兜底；发送走应用「胖猫」，复用 `feishu-notify.sh --card`） | 🔶 `--preview` 走 Worker 路径已验；`--send` 的本机路径未跑（首条是经 Worker `/admin/report/send` 发的） |
| `create-vpn-node.sh` | 建自用队节点（`infra/node/create-node.sh` 的 `vpn-` 变体，显式层级） | ✅ 第一次真实执行建成 `vpn-sg`（[evidence](../../docs/evidence/fleet-node-provision-vpn-sg-20260905/)） |
| `setup-vpn-node.sh` | 装机：xray REALITY + Hysteria2（salamander、自签、bing 伪装）、sysctl、unattended-upgrades、SSH 加固 | ✅ 在 `vpn-sg` 上跑过（结果见 as-built §2.1） |
| `feishu-notify.sh` + `_py/` | 飞书**应用**出口（只读自检 / 列会话 / 反查 open_id / 发文本 / 发卡片） | ✅ 用应用「胖猫」实测 `--bot-info`、`--list-chats`（16 个群）、`--whoami`（按手机号反查到用户 open_id；按企业邮箱查不到 user_id）；本机发送路径未跑 |
| `fleet.example.json` | ~~模板~~ | ❌ 已删（D7 之后 `fleet.json` 本身入库，模板没有存在的理由） |

---

## 3 · 凭据（`.secrets.env`，gitignored，chmod 600）

```bash
set -a; source infra/fleet/.secrets.env; set +a
```

| 变量 | 谁用 | 说明 |
|---|---|---|
| `STATIC_IP` / `SS_*` / `REALITY_*` / `CDN_*` | `gen-subscription.py` | 美国侧（无前缀），沿用 Proxy_Skill 的规范名 |
| `JP_*` / `SG_*` | 同上 | 日本 / 新加坡侧（`secrets_prefix` 在 `fleet.json` 里声明）。`JP_HY2_*` 2026-09-05 从生成产物回填（Proxy_Skill 的 `.secrets-*.env` 里本来没有） |
| `DEVICE_TOKEN_<ID>` ×6 | `publish-subscription.sh` | **每台设备一个** —— 为的是吊销（丢一台不影响其余五台），不是计费 |
| `NODE_TOKEN_<HOST>` ×3 | `publish-subscription.sh` / `healthcheck-install.sh` | 只允许 `POST /ingest` 与 `GET /fleet`；经 `LoadCredential` 进节点，不进 unit |
| `ADMIN_TOKEN` | `daily-report.py`、Worker `/admin/*` | 已 `wrangler secret put` |
| `FLEET_INGEST_URL` | `healthcheck-install.sh`、`daily-report.py` | Worker 基址。D5 定了独立域名后改这里并**重装** healthcheck env |
| `FEISHU_APP_ID` / `FEISHU_APP_SECRET` | Worker（`wrangler secret put`，已放）、`feishu-notify.sh`、`daily-report.py --send` | 应用「胖猫」，用户 2026-09-05 指定为主通道。⚠️ Secret 在对话中明文出现过，稳定后重置 |
| `FEISHU_RECEIVE_ID` / `FEISHU_RECEIVE_ID_TYPE` | Worker（已放）、`daily-report.py --send` | ✅ 用户本人 `open_id`（`--whoami` 按手机号反查）；私聊，不进任何群 |
| `FEISHU_WEBHOOK_URL` / `FEISHU_WEBHOOK_SECRET` | 备选通道 | 不配也行 |

---

## 4 · 日常操作

```bash
# 换 IP / 加节点：改 fleet.json → 隔离脚本绿 → 发布
./infra/scripts/verify-isolation.sh
./infra/fleet/publish-subscription.sh            # 客户端在 ≤15 min 内拿到（provider interval 900 s）

# 吊销一台设备 / 看 KV
./infra/fleet/publish-subscription.sh --revoke iphone
./infra/fleet/publish-subscription.sh --list

# 新节点装机 + 巡检
./infra/fleet/create-vpn-node.sh --node vpn-xx --region … --zone … --machine-type e2-small --network-tier STANDARD --auto-pick --dry-run
# …（真实执行去掉 --dry-run）… 然后 setup-vpn-node.sh（用法在文件头）
./infra/fleet/healthcheck-install.sh vpn-xx

# 日报
./infra/fleet/daily-report.py --preview          # 看 Worker 渲染好的今日卡片
./infra/fleet/daily-report.py --send             # 手动补发一次（需 FEISHU_APP_ID/SECRET + FEISHU_RECEIVE_ID）
curl -H "Authorization: Bearer $ADMIN_TOKEN" "$FLEET_INGEST_URL/admin/usage"   # 月度用量差分
```

订阅地址：`$FLEET_INGEST_URL/p/<DEVICE_TOKEN>/{mihomo-provider.yaml | clash.yaml | singbox.json | base64.txt}`。
`clash.yaml` 里的 provider URL 与 rule-provider URL 由 Worker 按请求替换成你的 token，四种产物 KV 里只存一份。

---

## 5 · 代价

> ⚠️ 这一选择的代价，必须留在记录里而不是被措辞掩盖：
>
> 1. **本目录复制了一份 `infra/node/` 的守卫逻辑，而不是抽公共库。** 理由与 `infra/node/README.md` §8 代价第 2 条相同
>    （`setup-*.sh` 从 stdin 灌进 `sudo bash -s`，必须自包含），代价是**改守卫逻辑要改两个目录，而它们会悄悄分叉**——
>    2026-09-05 已经分叉了一处：本目录的 `fw_ensure` 改成 JSON 读取 + 读不到就 die，`infra/node/` 的没改。
> 2. **workers.dev 子域名叫 `oratisoratisoratisoratis`。** 交互式注册脚本把名字重复输入了四次。它只是 D5 独立域名定下来之前的临时地址，
>    可在 Cloudflare 后台改；改了要同步 `.secrets.env` 的 `FLEET_INGEST_URL` 并重装 healthcheck env。
> 3. **日报的正式渲染在 Worker（JS）里，`daily-report.py --source nodes` 另有一份精简版（Python）。** 两份会漂移；精简版没有月度差分。
> 4. **月度用量靠节点 `tx_bytes` 差分，不是账单。** 重启会多算一个样本间隔；与计费字节不是同一口径。它是方向盘不是账本。
> 5. **停机窗口没有看门狗。** 2026-09-05 `vpn-jp` 的 stop → 换 SA → start 序列在 stop 之后被 gcloud 连接中断打断，
>    **09:44 到 15:17 无人重启，约 5.5 h 不可用**。教训：改现役节点的序列必须是一个带重试 / 回滚的脚本，不能是逐条手敲的 gcloud。
> 6. **`create-vpn-node.sh` 第一次真实执行被自己的读规则误判拦住**（网络抖动 → `denied` 读成空串 → 判「不符」）。已改成读不到就 die。

## 6 · 这次没有解决的

- [x] ~~🔴 **飞书收件会话未定**~~ ✅ 2026-09-05 15:53 CST：用户本人 `open_id` 私聊，首条真实日报发送成功（Worker `/admin/report/send`，code 0）。cron 从明天 08:37 CST 起自动发。
- [ ] 🔴 **订阅域名未定**（D5）：需要一个独立 zone；定了在 `worker/wrangler.jsonc` 加 `routes`、改 `fleet.json .subscription.hostname`、重新 publish（公告伪节点名里写域名）。
- [ ] 🔴 **真机客户端一次都没加载**：Clash Verge Rev（provider 热更新）、Shadowrocket（`base64.txt` + `profile-update-interval` 行为）。
- [ ] `vpn-us` 升 `e2-small` 后的 4 轮交叉测未做（ADR 0017 §8 代价 2 的复审判据）。
- [ ] `vpn-sg` 的入向路由与 Standard/Premium 对照未做。
- [ ] App Secret 未重置。
- [ ] Flow Logs → BigQuery sink 未建；「$ 实收 MTD」没有落点。

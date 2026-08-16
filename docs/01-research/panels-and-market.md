# 开源面板与商业市场调研

> 调研日期：2026-08-16
> 目的：为 babel.plus（面向中国大陆用户的流量中转服务：账户 + 订阅 + 支付 + 后台 + 工单）确定「直接采用 / fork / 自研」路线。
> 约定：所有数据均来自实际抓取的源码或页面（见文末「参考来源」）。凡未能核实的一律标注 **待核实**，绝不臆造端点、表名或价格。

---

## 一、开源面板横向对比

### 1.1 速览表

以下 stars / 最后提交时间通过 GitHub REST API（`gh api repos/{owner}/{repo}`）于 2026-08-16 实测。

| 项目 | 技术栈 | License | Stars | 最后提交 | 状态判断 |
|---|---|---|---|---|---|
| **v2board/v2board** | PHP 7.x + Laravel（旧版） | MIT | 5,063 | master 分支最后 commit `2023-06-03`；repo `pushed_at` 2024-03-19（dev 分支） | **事实停更**。最后 release `1.7.4`（2023-06-03） |
| **cedar2025/Xboard** | PHP 8.2+ / Laravel 12 + Octane + Horizon；Admin=React+shadcn/ui，User=Vue3+TS+NaiveUI；MySQL 5.7+ / SQLite；Redis | MIT | 4,644 | `2026-08-07` | 活跃但作者自述「light maintenance（仅修关键 bug / 合并重要 PR，新功能有限）」 |
| **Anankke/SSPanel-UIM** | PHP 8.2 + Slim + `illuminate/database`(Eloquent) + Redis；服务端渲染（Tabler 主题） | MIT | 10,411 | `2026-08-01`（master HEAD `2026-07-11`） | 活跃。**无 GitHub Release**，tag 停在 `2024.1` |
| **MHSanaei/3x-ui** | Go 1.26 + Gin + GORM；SQLite（默认）或 PostgreSQL；React 19 + Ant Design 6 + Vite | GPL-3.0 | 44,735 | `2026-08-15`（release `v3.6.0`，2026-07-30） | **非常活跃** |
| **vaxilu/x-ui**（3x-ui 上游） | JavaScript/Go | GPL-3.0 | 19,068 | `2024-08-19` | 停更，已被 3x-ui 取代 |
| **Gozargah/Marzban** | Python + FastAPI + SQLAlchemy + Alembic；SQLite（默认）或 MySQL；React 18 + Chakra UI | AGPL-3.0 | 7,258 | **master HEAD `2025-01-09`（release v0.8.4）**；`pushed_at` 2026-06-08 仅反映 `dev` 分支 | **master 已 19 个月未动**，配套 `Marzban-node` 停在 2025-03-22 |
| **hiddify/Hiddify-Manager** | Python 3.12 + APIFlask/Flask 3 + Flask-Admin(AdminLTE3) + SQLAlchemy；**MySQL/MariaDB** + Redis + Celery | 见 1.3，**不可商用** | 9,207 | `2026-07-15`（release `v12.3.3`，2026-05-29）；522 open issues | 放缓 |
| **remnawave**（实际代码在 `remnawave/backend` 等） | TypeScript + NestJS 11 + **Prisma 6 + PostgreSQL** + Redis + BullMQ；React + Mantine 9 | AGPL-3.0 | 4,859（umbrella，实为文档站） | `2026-08-16`（release `3.2.3`，2026-08-10） | **非常活跃**，五者中最新（2025-01 创建） |

节点侧 agent：

| 项目 | 技术栈 | License | Stars | 最后提交 | 状态判断 |
|---|---|---|---|---|---|
| **XrayR-project/XrayR** | Go | 无 LICENSE 文件 | 2,858 | 仓库源码**已被清空**（`contents` 返回空），描述改为「项目已废弃」；最后 release `v0.9.4`（2024-07-21） | **已废弃**，不可作为新项目基础 |
| **wyx2685/V2bX** | Go | MPL-2.0 | 1,155 | `2025-12-02`，仓库 **archived=true** | 已归档 |
| **wyx2685/v2node** | Go（改版 xray-core） | MPL-2.0 | 260 | `2026-07-13` | XrayR/V2bX 的事实继任者，当前活跃 |
| **vaxilu/soga** | 闭源二进制（GitHub 仓库仅含 `install.sh` / `soga.sh` / `soga-tool-*` 二进制） | 无 License | 652 | `2026-08-08` | 活跃但**闭源 + 商业授权** |

> **重要结论 1**：v2board 已停更三年，XrayR 已废弃且源码被删，V2bX 已归档。这条技术栈上唯一还在动的组合是 **Xboard（面板）+ v2node（节点）** 或 **Xboard + soga（闭源商业）**。

### 1.2 v2board / Xboard 深入

**Xboard 是 v2board 的重构 fork**，保留了 `v2_` 前缀表结构与 `/api/v1/server/UniProxy/*` 节点协议，因此上下游生态（XrayR/V2bX/v2node/soga）可直接对接。仓库中保留了从 v2board dev / 1.7.3 / 1.7.4 迁移的文档。

**核心能力：**

- 账户：邮箱注册/登录、Sanctum token、邀请码（`v2_invite_code`）、佣金（`v2_commission_log`）、Telegram 绑定（`telegram_id`）
- 订阅：多客户端协议适配器 `app/Protocols/`：`Clash.php` `ClashMeta.php` `SingBox.php` `Surge.php` `Surfboard.php` `QuantumultX.php` `Loon.php` `Stash.php` `Shadowrocket.php` `Shadowsocks.php` `General.php`
- 支付插件 `plugins-core/`：`AlipayF2f` `Epay` `Mgate` `Btcpay` `Coinbase` `CoinPayments`，另有 composer 依赖 `stripe/stripe-php`。v2board 原版 `app/Payments/` 还含 `WechatPayNative.php` `StripeAlipay.php` `StripeCheckout.php` `StripeCredit.php` `StripeWepay.php`
- 工单：`v2_ticket` + `v2_ticket_message`
- 运营：优惠券、礼品卡（2025-07 新增）、公告、知识库、邮件模板、插件系统、订阅模板、管理员审计日志（2026-03 用 `v2_admin_audit_log` 替换旧 `v2_log`）

**Xboard 数据模型（实测自 `database/migrations/`，表名带 `v2_` 前缀）：**

| 领域 | 表 | 关键字段 |
|---|---|---|
| 用户 | `v2_user` | `id, email(unique), password, password_algo, password_salt, uuid(36), token(char 32), group_id, plan_id, transfer_enable(bigint), u(bigint), d(bigint), t, speed_limit, device_limit, expired_at(bigint 时间戳), banned, is_admin, is_staff, balance, commission_type, commission_rate, commission_balance, invite_user_id, telegram_id, remind_expire, remind_traffic, last_login_at, last_login_ip, remarks` |
| 套餐 | `v2_plan` | 现状（2025-01 `optimize_plan_table` 之后）：`id, group_id, name, prices(json), sell(bool), transfer_enable(unsignedBigInteger, 字节), speed_limit(unsignedInteger, Mbps, 0=不限), reset_traffic_method(见下), capacity_limit(0=不限), renew, show, sort, content, tags(json)`。原来的 `month_price` / `quarter_price` / `half_year_price` / `year_price` / `two_year_price` / `three_year_price` / `onetime_price` / `reset_price` 八列**已被删除**，合并进 `prices` JSON，键为 `monthly` / `quarterly` / `half_yearly` / `yearly` / `two_yearly` / `three_yearly` / `onetime` / `reset_traffic`，**值为「元」（迁移时除以 100）**。`reset_traffic_method`：null 跟随系统 / 0 每月1号 / 1 按月重置 / 2 不重置 / 3 每年1月1日 / 4 按年重置 |
| 订单 | `v2_order` | `id, user_id, plan_id, coupon_id, payment_id, invite_user_id, type(1新购2续费3升级), period, trade_no(unique 36), callback_no, total_amount, handling_amount, discount_amount, surplus_amount, surplus_credit(原 refund_amount，2026-05 重命名), balance_amount, surplus_order_ids, status(0待支付1开通中2已取消3已完成4已折抵), commission_status, commission_balance, actual_commission_balance, paid_at` |
| 支付方式 | `v2_payment` | `id, uuid, payment, name, icon, config(text), notify_domain, handling_fee_fixed, handling_fee_percent, enable, sort` |
| 优惠券 | `v2_coupon` | `id, code, name, type, value, limit_use, limit_use_with_user, limit_plan_ids, limit_period, started_at, ended_at, show` |
| 礼品卡 | `v2_gift_card_template` / `v2_gift_card_code` / `v2_gift_card_usage` | — |
| 节点 | `v2_server`（2025-01 统一表） | `id, type, code, parent_id, machine_id, group_ids(json), route_ids(json), name, rate(decimal 8,2 倍率), tags(json), host, port, server_port, protocol_settings(json), show, enabled, sort` |
| 节点（旧） | `v2_server_vmess` `v2_server_vless` `v2_server_trojan` `v2_server_shadowsocks` `v2_server_hysteria` | 已被 `v2_server` 取代，迁移脚本原地搬运 |
| 节点分组 | `v2_server_group` | `id, name`（与 `v2_user.group_id` / `v2_plan.group_id` 对应） |
| 路由规则 | `v2_server_route` | `id, remarks, match(text), action, action_value` |
| 机器 | `v2_server_machine` | `id, name, token(64 unique), notes, is_active, last_seen_at, load_status(json)` |
| 机器负载 | `v2_server_machine_load_history` | `machine_id, cpu, mem_total, mem_used, disk_total, disk_used, recorded_at` |
| 统计 | `v2_stat` | 全局日/月：`record_at, record_type, order_count, order_total, commission_count, commission_total, paid_count, paid_total, register_count, invite_count, transfer_used_total` |
| 统计 | `v2_stat_user` | `user_id, server_rate(decimal 10), u, d, record_type, record_at`（按**倍率**分桶，unique(server_rate,user_id,record_at)） |
| 统计 | `v2_stat_server` | `server_id, server_type, u, d, record_type, record_at`（unique(server_id,server_type,record_at)） |
| 流量重置 | `v2_traffic_reset_logs` | `user_id, reset_type, reset_time, old_upload/old_download/old_total, new_upload/new_download/new_total, trigger_source, metadata(json)` |
| 工单 | `v2_ticket` | `user_id, subject, level, status(0开启1关闭), reply_status(0待回复1已回复)` |
| 工单消息 | `v2_ticket_message` | `user_id, ticket_id, message` |
| 邀请 | `v2_invite_code` | — |
| 佣金 | `v2_commission_log` | — |
| 内容 | `v2_notice` `v2_knowledge` | — |
| 邮件 | `v2_mail_log` `v2_mail_templates` | — |
| 系统 | `v2_settings` `v2_plugins` `v2_subscribe_templates` `v2_admin_audit_log` | — |

**关键设计要点（值得抄）：**

1. **流量记账在 `v2_user.u/d` 上做增量累加，不存明细流水**。明细只按天/月聚合到 `v2_stat_user` / `v2_stat_server`。这是这个业务能扛住量的核心：单用户单节点秒级流量不落库。
2. **倍率（`v2_server.rate`）在面板侧结算，节点侧只报原始字节**。`app/Jobs/TrafficFetchJob.php`：
   ```php
   User::where('id', $uid)->incrementEach([
       'u' => $v[0] * $this->server['rate'],
       'd' => $v[1] * $this->server['rate'],
   ], ['t' => time()]);
   ```
3. **可用性判定用一条 SQL 表达**（`app/Services/ServerService.php::getAvailableUsers`）：
   ```php
   User::whereIn('group_id', $groupIds)
       ->whereRaw('u + d < transfer_enable')
       ->where(fn($q) => $q->where('expired_at','>=',time())->orWhere('expired_at',NULL))
       ->where('banned', 0)
       ->select(['id','uuid','speed_limit','device_limit'])
   ```
   `expired_at = NULL` 表示永久（对应「不限时套餐」）。
4. **订单金额一律用 integer 存分**（`v2_order.total_amount` 等），不用 decimal/float。
   ⚠️ 但 2025-01 的 `optimize_plan_table` 把套餐价格改成了 `prices` JSON 且**值为元（浮点）**——这是一处**倒退**，babel.plus 不要抄这一条，套餐价格也应继续用整数分。

### 1.3 其他面板

#### 能力矩阵

| | SSPanel-UIM | 3x-ui | Hiddify | Marzban | Remnawave |
|---|---|---|---|---|---|
| **用户计费 / 订单 / 支付** | ✅ **完整** | ❌ | ❌ | ❌ | ❌（`infra_billing_*` 是**运营方自己的服务器成本台账**，不是向用户收费） |
| **工单** | ✅ | ❌ | ❌ | ❌ | ❌ |
| **多租户 / 分销** | 弱 | ❌ | ✅ **管理员树**（`parent_admin_id` 自引用） | 管理员维度（`users.admin_id` + `is_sudo`） | 仅 role 字段 |
| **终端用户登录** | ✅ | ❌（只有面板管理员） | ✅ | ❌（只有管理员） | ❌ **用户无登录，只有 `short_uuid`** |
| **可商用许可** | ✅ MIT | GPL-3.0（传染） | ❌ **不可商用** | AGPL-3.0（网络传染） | AGPL-3.0（网络传染） |

> **结论 3**：五个面板里**只有 SSPanel-UIM 具备真正的用户计费与工单**，且是 MIT。3x-ui / Hiddify / Marzban / Remnawave 全都是「纯节点与用户管理」的运维工具，把它们改造成能卖钱的服务，等于自己写掉一半系统。

#### SSPanel-UIM

- **数据模型**（`db/migrations/2023020100-init.php`，init 建 25 张表）：
  `user` `node` `product` `order` `invoice` `paylist` `user_money_log` `payback` `gift_card` `user_coupon` `user_invite_code` `ticket` `announcement` `config` `docs` `link` `login_ip` `online_log` `hourly_usage` `subscribe_log` `syslog` `email_queue` `detect_list` `detect_log` `detect_ban_log`；另有 `mfa_devices`（2025-07 迁移新增）
  - `user`（50 列）：`id, user_name, email, pass`(登录密码), `passwd`(连接密码, unique), `uuid`(unique), `u, d, transfer_today, transfer_total, transfer_enable, port, money DECIMAL(12,2), ref_by, method, node_speedlimit, node_iplimit, class, class_expire, node_group, is_banned, is_shadow_banned, auto_reset_day, auto_reset_bandwidth, api_token, ...`
  - `node`：`id, name, type, server, custom_config(JSON), traffic_rate, is_dynamic_rate, dynamic_rate_type, dynamic_rate_config, node_class, node_speedlimit, node_bandwidth, node_bandwidth_limit, bandwidthlimit_resetday, node_heartbeat, online_user, ipv4, ipv6, node_group, gfw_block, password(unique)`
  - **动态倍率**是 SSPanel 的特色：`is_dynamic_rate` + `DynamicRate::getRateByTime()`（logistic / linear 曲线），按时段自动浮动倍率——v2board 系没有
- **节点协议 `mod_mu` WebAPI**（`app/routes.php:321-331`，全部套 `NodeToken` 中间件）：
  ```
  GET  /mod_mu/nodes/{id}/info      GET  /mod_mu/func/detect_rules
  GET  /mod_mu/users                GET  /mod_mu/func/ping
  POST /mod_mu/users/traffic
  POST /mod_mu/users/aliveip
  POST /mod_mu/users/detectlog
  ```
  - **认证**（`src/Middleware/NodeToken.php`）：`?key=` query 参数等值比较 `$_ENV['muKey']`，**全局共享**；另校验 `Host` 头等于 `$_ENV['webAPIUrl']`；可选 `checkNodeIp` 校验 `REMOTE_ADDR` 落在 `node.ipv4/ipv6`。节点身份来自 `?node_id=`，**与 key 不绑定** → 持 muKey 者可冒充任意节点。与 v2board 是同一个弱点
  - **流量上报**：POST `{data: [{user_id, u, d}, ...]}`；面板按 `traffic_rate`（或动态倍率）折算后 `user.u += u*rate`、写 `hourly_usage`、更新 `node.node_bandwidth` 与 `node.online_user`
  - `GET /mod_mu/users` 返回 `[id, u, d, transfer_enable, node_speedlimit, node_iplimit, method, port, passwd, uuid]`，按 `node_class`/`node_group` 过滤，响应带 **ETag**
- **订阅**：`GET /sub/{token}/{subtype}`。token 存在独立表 **`link`**（`id, token unique, userid unique`），`bin2hex(random_bytes(...))`，长度取 `max($_ENV['sub_token_len'], 8)`；**吊销 = 删 `link` 行**。subtype 支持 `json, clash, sip008, singbox, v2rayjson, sip002, ss, v2ray, trojan`；同样发 `Subscription-Userinfo` 头；可选写 `subscribe_log`
  > **可借鉴**：把订阅 token 拆成独立表（而不是 v2board 挂在 `users.token` 上），是 SSPanel 明显优于 Xboard 的一处设计。

#### 3x-ui

- **重要更正**：3x-ui 在 v3.x **已经不是单机面板**了。有一等公民的 `nodes` 表和多面板拓扑（含链式节点）。但**它没有独立的节点 agent**——所谓「node」就是另一台完整的 3x-ui 面板，主面板通过 HTTP **轮询**它自己的 `/panel/api` 接口。
- **数据模型**（`internal/database/model/model.go` + `internal/database/db.go`）：显式 `TableName()` 的有 `clients` `client_groups` `client_inbounds` `client_hwids` `client_external_links` `inbound_fallbacks` `hosts`；由原始 SQL 直接确认的还有 `inbounds` `client_traffics` `settings`。其余（`users` `nodes` `api_tokens` `node_client_traffics` 等）按 GORM 命名约定推导，**待核实**
  - `clients`：`id, email(unique), sub_id(index), uuid, password, flow, security, wg_*, limit_ip, limit_hwid, total_gb, expiry_time, enable, tg_id, group_name, reset, ...`
  - `client_traffics`：`id, inbound_id, enable, email(unique), up, down, expiry_time, total, reset, last_online, last_sub_fetch`
  - `nodes`：`id, name, scheme, address, port, base_path, api_token, tls_verify_mode(verify|skip|pin|mtls), pinned_cert_sha256, inbound_sync_mode, status, last_heartbeat, latency_ms, cpu_pct, mem_pct, net_up, net_down, ...`
  - `api_tokens`：`id, name, token(SHA-256 哈希，明文只显示一次), enabled, scope`
- **认证**（`internal/web/controller/api.go::checkAPIAuth`）：两种——(1) **mTLS**（有已验证的客户端证书链即视为认证，scope 强制 `node-sync`）；(2) **`Authorization: Bearer <tok>`** 比对 `api_tokens` 的 SHA-256。scope 分 `admin` / `monitor` / `node-sync`，`node-sync` 有**硬编码路由白名单**。同步是**主面板拉取**，由 `node_heartbeat_job.go`（并发 32，4s 超时）和 `node_traffic_sync_job.go`（并发 8，30s 对账）驱动
  > **可借鉴**：per-node 哈希存储的 bearer token + scope 白名单 + 可选 mTLS —— 这是五个面板里**认证设计第二强**的，且实现简单，babel.plus 可直接照搬。
- **订阅**：跑在**独立端口**（默认 `subPort=2096`），三条路由 `/sub/:subid`、`/json/:subid`、`/clash/:subid`。token 是 `clients.sub_id`，**不是每客户端唯一**——一个 `subId` 聚合跨 inbound 的多个 client。拉取时间记到 `client_traffics.last_sub_fetch`，HWID 限制走 `client_hwids`

#### Hiddify

- **许可证是最大的坑**：`hiddify/hiddifypanel` 的 `LICENSE.md` 是 **CC BY-NC-SA 4.0**（明文「You may not use the material for commercial purposes」），客户端 `hiddify-app` 是「Hiddify Extended GPL v3」（GPL-3.0 文本 + 7 条 §7 附加条款，含 **NonCommercial**）。`Hiddify-Manager` 仓库同时存在 GPL-3.0 的 `LICENSE` 和 CC0-1.0 的 `LICENSE.md`，**以哪个为准待核实**。
  > **结论 4**：**Hiddify 全家桶在商业场景下不可用**，直接排除，不必再评估。
- 数据模型（无 `__tablename__`，靠 Flask-SQLAlchemy 约定）：`user` `user_detail` `admin_user` `child` `domain` `show_domain` `proxy` `str_config` `bool_config` `daily_usage` `report` `report_detail`(待核实)
  - `child` **就是节点模型**（源码注释：「the child model is node」）：`id, name, mode(virtual|remote|parent), unique_id(unique)`
  - `admin_user` 有真正的**分销商树**：`mode(super_admin|admin|agent)`, `parent_admin_id` 自引用 FK, `max_users`, `max_active_users`, `can_add_admin`
  - **无 Alembic**，用手写的整数版本迁移器（`MAX_DB_VERSION = 130`）
- 节点同步：父子面板互为 API 客户端，凭据是 **`Hiddify-API-Key` 头**，值为对端的 `child.unique_id`。⚠️ 同一个头也用于账户 API key，而**账户 API key 就是账户 UUID 原文**（源码注释 `# api_key equals uuid for now`）。用量按用户 UUID 以有符号差值对账
- **明文存密码**：`cls.query.filter(cls.username == username, cls.password == password)`，`password` 是 `String(100)` 无哈希列
- 订阅 URL：`https://<host>/<proxy_path_client>/<user_uuid>/<format>`，**token 就是用户自己的 uuid**，无独立订阅密钥

#### Marzban

- **维护风险高**：`master` HEAD 停在 2025-01-09（v0.8.4），`Marzban-node` 停在 2025-03-22（v0.5.2），只有 `dev` 分支有 2026 提交
- 数据模型（`app/db/models.py`，`__tablename__` 原文）：`admins` `admin_usage_logs` `users` `next_plans` `user_templates` `user_usage_logs` `proxies` `inbounds` `hosts` `system` `jwt` `tls` `nodes` `node_user_usages` `node_usages` `notification_reminders`；关联表 `exclude_inbounds_association`（注意变量名是 `excluded_...` 但表名少个 d）、`template_inbounds_association`
  - `users`：`id, username(34), status, used_traffic BIGINT, data_limit BIGINT, data_limit_reset_strategy, expire, admin_id, sub_revoked_at, sub_updated_at, sub_last_user_agent(512), on_hold_expire_duration, on_hold_timeout, auto_delete_in_days, online_at`
  - `nodes`：`id, name, address, port, api_port, xray_version, status, uplink BIGINT, downlink BIGINT, **usage_coefficient FLOAT**`（即倍率）
  - `jwt`（`secret_key`）与 `tls`（`key`, `certificate`）——**面板自签 CA 存在数据库里**
  - **无任何 billing / order / invoice / ticket 表**
- 详见 2.5 节的节点协议

#### Remnawave

- **仓库要认清**：`github.com/remnawave/panel`（4,859★）其实是 **Docusaurus 文档站**；真正的代码在 `remnawave/backend`（214★）、`remnawave/frontend`（137★）、`remnawave/node`（176★）、`remnawave/subscription-page`（117★）。全部 AGPL-3.0
- 数据模型（`prisma/schema.prisma`，633 行，34 张 `@@map` 表）：
  `remnawave_settings` `users` `user_traffic` `api_tokens` `admin` `passkeys` `keygen` `nodes` `nodes_user_usage_history` `nodes_usage_history` `hosts` `internal_squad_host_exclusions` `subscription_templates` `subscription_settings` `hwid_user_devices` `internal_squads` `internal_squad_members` `internal_squad_inbounds` `config_profiles` `config_profile_inbounds` `config_profile_inbounds_to_nodes` `infra_providers` `infra_billing_nodes` `infra_billing_history` `user_subscription_request_history` `hosts_to_nodes` `config_profile_snippets` `external_squads` `external_squads_templates` `subscription_page_config` `node_plugin` `user_meta` `node_meta` `torrent_blocker_reports`
  - `users`：`id BIGINT, short_uuid(unique，即订阅 token), username(unique), status(ACTIVE|DISABLED|LIMITED|EXPIRED), traffic_limit_bytes, traffic_limit_strategy(NO_RESET|DAY|WEEK|MONTH|MONTH_ROLLING), expire_at, last_traffic_reset_at, sub_revoked_at, hwid_device_limit, ...`
  - `user_traffic` 从 `users` 里**拆成 1:1 独立表**（`used_traffic_bytes, lifetime_used_traffic_bytes, online_at, last_connected_node_uuid`）——热写字段与冷读字段分离，**值得抄**
  - `nodes.consumption_multiplier BIGINT`（默认 `1e9`，即倍率用定点整数存，不用 float）——**也值得抄**
  - `keygen`（`priv_key, pub_key, ca_cert, ca_key, client_cert, client_key`）：面板即 CA
  - `admin` + `passkeys`（WebAuthn）—— **管理员支持 passkey**，五者中唯一
  - `nodes_user_usage_history` 主键 `[node_id, created_at, user_id]`，`created_at` 是 **DATE（按天分桶）**；`nodes_usage_history` 主键 `[node_uuid, created_at]`，默认 `date_trunc('hour', now())`（**按小时分桶**）
  - `user_subscription_request_history`（`user_id, request_ip, user_agent, srr_rule_name, srr_response_type, request_at`）——**每次订阅拉取都留痕**，是 v2board 系完全没有的风控能力
- 节点协议：见下方「跨面板对比」

#### 跨面板对比：节点认证强度（弱 → 强）

| 排名 | 面板 | 机制 | 问题 |
|---|---|---|---|
| 1（最弱） | **SSPanel-UIM** | 全局 `muKey` 走 query 参数 | `node_id` 与 key 不绑定，可冒充任意节点 |
| 2 | **v2board / Xboard v1** | 全局 `server_token` 走 query 参数 | 同上；Xboard 的 `machine_id`+token 是改良但仍非节点粒度 |
| 3 | **Hiddify** | `Hiddify-API-Key` 头 = 对端 `child.unique_id` | 同一个头也用于账户 API key，而账户 key = 账户 UUID 原文 |
| 4 | **3x-ui** | per-node bearer token（SHA-256 存储）+ scope 路由白名单，可选 mTLS | 较好 |
| 5 | **Marzban** | REST/RPyC over **mTLS**（`ssl_cert_reqs=CERT_REQUIRED`） | 但证书是 TOFU 固定，且显式关闭了 SAN 校验（`SANIgnoringAdaptor`） |
| 6（最强） | **Remnawave** | **mTLS TLS 1.3**（`rejectUnauthorized: true`）**+ 面板签发的非对称 JWT** 双层 | — |

#### 跨面板对比：订阅 token 方案

| 面板 | token | 可吊销性 |
|---|---|---|
| v2board / Xboard | `users.token` char(32) 明文 | 只能改 token 本身 |
| SSPanel-UIM | 独立 `link` 表，`bin2hex(random_bytes)` | ✅ 删行即吊销，与用户解耦 |
| 3x-ui | `clients.sub_id`（跨 client 共享） | 弱 |
| Hiddify | 用户 `uuid` 直接放路径 | 只能换 uuid |
| Marzban | `b64url(username,issued_at)` + 10 位截断 SHA-256 签名 | ✅ `users.sub_revoked_at` 对比签发时间，**不换标识符即可吊销** |
| Remnawave | `users.short_uuid` | `sub_revoked_at` + 重生成 short_uuid；且每次拉取写 `user_subscription_request_history` |

> **给 babel.plus 的取舍**：订阅 token 建议**取 SSPanel 的形态（独立表、随机、可多条、可单独吊销）+ Marzban 的签发时间戳语义 + Remnawave 的拉取审计表**。这三者组合起来的成本很低，收益是「泄露后可精确止血 + 能看出是谁在共享账号」。

---

## 二、面板 ↔ 节点 API 契约（核心）

### 2.1 v2board / Xboard：UniProxy v1

**路由注册**（`app/Providers/RouteServiceProvider.php` 前缀 `/api/v1`，`app/Http/Routes/V1/ServerRoute.php` 前缀 `server`）：

| 方法 | 路径 | Controller 方法 | v2board 1.7.4 | Xboard |
|---|---|---|---|---|
| GET | `/api/v1/server/UniProxy/config` | `config` | ✅ | ✅ |
| GET | `/api/v1/server/UniProxy/user` | `user` | ✅ | ✅ |
| POST | `/api/v1/server/UniProxy/push` | `push` | ✅ | ✅ |
| POST | `/api/v1/server/UniProxy/alive` | `alive` | ❌ | ✅ |
| GET | `/api/v1/server/UniProxy/alivelist` | `alivelist` | ❌ | ✅ |
| POST | `/api/v1/server/UniProxy/status` | `status` | ❌ | ✅ |

> v2board 1.7.4 的 `ServerRoute.php` 用的是动态路由 `server/{class}/{action}` → `App\Http\Controllers\Server\{Class}Controller::{action}`，其 `UniProxyController` 只实现了 `user` / `push` / `config` 三个方法。`alive` / `alivelist` / `status` 是 **Xboard 的扩展**。

另有两组历史遗留端点（Xboard 仍保留）：
`/api/v1/server/ShadowsocksTidalab/{user,submit}`、`/api/v1/server/TrojanTidalab/{config,user,submit}`。

**认证方案（`app/Http/Middleware/Server.php`）——共享静态 token，不是 per-node 密钥：**

节点把三个参数放在 **query string** 里（XrayR 用 resty 的 `SetQueryParams` 全局挂载）：

| 参数 | 说明 |
|---|---|
| `token` | 必填，全局共享，等值比较 `admin_setting('server_token')`。**所有节点共用同一个 token** |
| `node_id` | 必填，节点在 `v2_server` 中的 id |
| `node_type` | 可选，`shadowsocks` / `vmess` / `vless` / `trojan` / `hysteria` / `tuic` / `anytls` / `v2node`（`v2node` 会被规范化为 null） |

校验通过后，中间件把 `Server` 模型塞进 `$request->attributes->set('node_info', ...)`。

> **安全评价**：全局共享明文 token + query string 传递（会进 access log），任一节点失陷 = 全部节点凭证失陷，且可枚举 `node_id` 拉取全量用户 UUID。babel.plus 若自研，这里必须改成 per-node 密钥 + Header 传递。

**Xboard 新增的 machine token（`app/Http/Middleware/ServerV2.php`）**：若请求带 `machine_id`，则改用 `v2_server_machine` 表按 `(id, token)` 查找，校验 `is_active`，并检查目标 `node_id` 的 `machine_id` 归属。这是**每机器一个 token** 的改良方案，向 per-node 密钥迈了一步。

**各端点载荷：**

#### `GET /config` → 节点配置

响应带 `ETag`（`sha1(json_encode($response))`）；节点带 `If-None-Match` 时命中返回 **304**。

Xboard `ServerService::buildNodeConfig()` 输出基础字段：
```json
{
  "protocol": "vless",
  "listen_ip": "0.0.0.0",
  "server_port": 443,
  "network": "tcp",
  "networkSettings": null,
  "tls": 2,
  "flow": "xtls-rprx-vision",
  "tls_settings": { "server_name": "...", "private_key": "...", "short_id": "...", "dest": "...", "xver": "0" },
  "base_config": { "push_interval": 60, "pull_interval": 60 }
}
```
不同 `type` 分支追加字段：`shadowsocks` → `cipher` / `plugin` / `plugin_opts` / `server_key`；`trojan` → `host` / `server_name` / `multiplex`；`hysteria` → `version` / `up_mbps` / `down_mbps` / `obfs` / `obfs-password`；`tuic` → `version` / `congestion_control` / `auth_timeout` / `zero_rtt_handshake` / `heartbeat`。v2board 1.7.4 还会在 `route_id` 非空时附加 `routes`（`[{id, match[], action, action_value}]`）。

`base_config.push_interval` / `pull_interval` 默认均为 **60 秒**，由后台设置 `server_push_interval` / `server_pull_interval` 控制——**这就是流量上报节奏**。v2node 的 `BaseConfig` 还多读 `device_online_min_traffic` / `node_report_min_traffic` 两个字段。

#### `GET /user` → 用户列表

同样带 ETag / 304。响应：
```json
{ "users": [ { "id": 1, "uuid": "xxxxxxxx-....", "speed_limit": 0, "device_limit": 3 } ] }
```
> v2board 1.7.4 的 `user` 结构只有 `id` / `uuid` / `speed_limit`（XrayR 的 `newV2board/model.go` 里 `DeviceLimit` 有注释 `// todo waiting v2board send configuration`）。Xboard 的 `getAvailableUsers` 明确 `select(['id','uuid','speed_limit','device_limit'])`。
> XrayR 侧会把 `email` 合成为 `{uuid}@v2board.user`，Shadowsocks 的 `password` 直接用 `uuid`。

#### `POST /push` → 流量上报

**Body 是一个「uid → [上行字节, 下行字节]」的 map**（XrayR `ReportUserTraffic` 注释原文：`// json structure: {uid1: [u, d], uid2: [u, d], uid1: [u, d], uid3: [u, d]}`）：
```json
{ "1": [10485760, 52428800], "7": [1024, 8192] }
```
Xboard `ServerService::processTraffic()` 会过滤掉非「长度为 2 的数值数组」的项，然后：
- 缓存 `SERVER_{TYPE}_ONLINE_USER` = 条目数、`SERVER_{TYPE}_LAST_PUSH_AT` = 当前时间（TTL 3600）
- 调 `UserService::trafficFetch()` → 派发 `TrafficFetchJob`（队列 `traffic_fetch`）做倍率折算和累加

#### `POST /alive` → 在线设备（IP）上报（仅 Xboard）

Body 为 `{ "<uid>": ["1.2.3.4", "5.6.7.8"] }`，进 `DeviceStateService::setDevices(uid, nodeId, ips)`。

#### `GET /alivelist` → 拉取全网在线设备表（仅 Xboard）

只针对 `device_limit > 0` 的用户，响应 `{ "alive": { "<uid>": <count> } }`（v2node 侧结构体为 `Alive map[int]int`）。**这是多节点共享设备数限制的机制**：节点先拉全网计数，再本地决策是否放行。

#### `POST /status` → 节点负载（仅 Xboard）

严格校验：
```
cpu: required|numeric|min:0|max:100
mem.total / mem.used / swap.total / swap.used / disk.total / disk.used: required|integer|min:0
```
额外可带 `kernel_status`。写 Redis（`SERVER_{TYPE}_LOAD_STATUS`），TTL = `max(300, push_interval*3)`。

### 2.2 Xboard 的 v2 节点协议（`/api/v2/server/*`）

Xboard 在 `app/Http/Routes/V2/ServerRoute.php` 增加了一套新协议，中间件 `server.v2`：

| 方法 | 路径 | 说明 |
|---|---|---|
| GET/POST | `/api/v2/server/handshake` | 返回 `{"websocket": {"enabled": bool, "ws_url": "wss://host/ws"}}`。`node_id` 此时可空 |
| POST | `/api/v2/server/report` | **合并上报**：一次 POST 带 `traffic` / `alive` / `online` / `status` / `metrics` 五个可选字段 |
| GET | `/api/v2/server/config` | 复用 V1 的 `UniProxyController@config` |
| GET | `/api/v2/server/user` | 复用 V1 |
| POST | `/api/v2/server/push` `alive` `status`，GET `alivelist` | 复用 V1 |
| POST | `/api/v2/server/machine/nodes` | 一台机器拉取自己名下所有节点 |
| POST | `/api/v2/server/machine/status` | 机器级负载上报 |

`report` 的 body：
```json
{
  "traffic": { "<uid>": [u, d] },
  "alive":   { "<uid>": ["ip", ...] },
  "online":  { "<uid>": <conn_count> },
  "status":  { "cpu": 12.3, "mem": {"total":..,"used":..}, "swap": {...}, "disk": {...} },
  "metrics": { ... }
}
```

同时有 WebSocket 长连（`app/WebSocket/NodeWorker.php`，基于 workerman，`compose.split.sample.yaml` 里暴露在 `/api/v2/server/ws` → `:8076`），用于把配置变更推给节点，替代 60 秒轮询。

> **对 babel.plus 的直接影响**：这个 WebSocket 常驻进程 + Horizon 队列 worker 是 Xboard 上 Cloud Run 最大的架构摩擦点，详见第五节。

### 2.3 节点侧 agent 如何消费

| Agent | 消费的端点 | 备注 |
|---|---|---|
| **XrayR**（已废弃，v0.9.4） | `config` / `user` / `push` | `ReportNodeStatus` / `ReportNodeOnlineUsers` / `ReportIllegal` 在 `newV2board` 驱动里**全是空实现 `return nil`**，即 XrayR 对 v2board 不上报负载和在线用户 |
| **v2node**（现役） | `config` / `user` / `push` / `alive` / `alivelist` | query 参数固定 `node_type=v2node`；结构体同时打了 `json` 和 `msgpack` tag，说明支持 msgpack 编码 |
| **V2bX**（已归档） | 同 XrayR 系 | MPL-2.0 |
| **soga**（闭源商业） | 支持 v2board / SSPanel / ProxyPanel 等多面板 | 见 2.4 |

XrayR 的 ETag 缓存实现（`api/newV2board/v2board.go`）：客户端本地维护 `eTags["node"]` / `eTags["users"]` 两个 map，请求时带 `If-None-Match`，收到 304 就返回 `api.NodeNotModified` / `api.UserNotModified` 让上层跳过 reload。**这是省带宽和 DB 压力的关键，自研必须实现。**

### 2.4 soga（闭源）

- GitHub `vaxilu/soga` 仓库**只有安装脚本和预编译二进制**（`install.sh` / `soga.sh` / `soga-tool-amd64` / `soga-tool-arm64`），无源码、无 LICENSE → 闭源
- 自述「完全重写的独立内核，不基于任何 core」，支持 VMess / VLESS(Reality) / Trojan / SS / SSR / Hysteria2 / AnyTLS / Mieru，全协议 UDP FullCone
- **商业授权模式**：按「时长 × 用户数上限（≤2000 / ≤6000 / 无限）× 协议数（1–8 种）」定价。文档示例：单协议 / 一年 / ≤6000 用户 = **100 USDT**；全 8 协议 / 永久 / 无限用户 = **3,480 USDT**。仅收 USDT，授权码首次使用后绑定对接地址，换绑收 5 USDT
- 社区免费版用户数上限：有第三方资料称 88 用户 —— **待核实**（官方文档未直接确认）

> **结论 2**：把闭源、按 USDT 收费、授权码绑定域名的组件放进 babel.plus 的关键路径，是不可接受的供应链风险。节点侧要么用 v2node（MPL-2.0），要么自研。

### 2.5 Marzban 的节点协议

**与 UniProxy 有本质区别：Marzban 是「面板主动推 / 主动拉」，节点是被动服务端，且用 mTLS 而不是共享 token。**

`Marzban-node/main.py` 按 `SERVICE_PROTOCOL` 选协议（默认 `rest`），端口 `SERVICE_PORT` 默认 **62050**：

```python
if SERVICE_PROTOCOL == 'rpyc':
    authenticator = SSLAuthenticator(keyfile=SSL_KEY_FILE, certfile=SSL_CERT_FILE,
                                     ca_certs=SSL_CLIENT_CERT_FILE or None)
    ThreadedServer(rpyc_service.XrayService(), port=SERVICE_PORT, authenticator=authenticator)
elif SERVICE_PROTOCOL == 'rest':
    uvicorn.run(rest_service.app, ssl_keyfile=..., ssl_certfile=...,
                ssl_ca_certs=SSL_CLIENT_CERT_FILE, ssl_cert_reqs=2)   # 2 = CERT_REQUIRED
```

- **认证 = mTLS 客户端证书**。节点首次启动自签服务端证书（`certificate.py::generate_certificate`）；客户端证书来自面板 `tls` 表，粘贴进节点的 `SSL_CLIENT_CERT_FILE`。不配的话节点会打日志警告「everyone can connect to this node and this isn't secure!」
- **面板侧客户端** `app/xray/node.py`（`ReSTXRayNode` / `XRayNode`）：`session.cert = (certfile, keyfile)`，用自定义 `SANIgnoringAdaptor` **关闭 SAN 校验**，`connect()` 时用 `ssl.get_server_certificate()` 做 **TOFU 式**证书固定，不走 CA 验证
- **控制面端点（全部 POST，面板 → 节点）**：`/connect`（返回 `session_id`）、`/disconnect`、`/ping`、`/start`（body 带 `config=<xray json>`）、`/stop`、`/`（返回 `started` 与 `core_version`）
- **日志**走 WebSocket：`wss://{address}:{port}/logs?session_id={sid}&interval=0.7`
- **流量是「拉」不是「推」**：`/start` 之后面板直接对节点的 Xray gRPC API 端口（`api_port`，默认 **62051**）开通道：
  ```python
  self._api = XRayAPI(address=self.address, port=self.api_port,
                      ssl_cert=self._node_cert.encode(), ssl_target_name="Gozargah")
  ```
  然后 `app/jobs/record_usages.py::record_user_usages()` 用线程池对所有已连接节点调 `api.get_users_stats(reset=True, timeout=30)`，乘以各节点的 `usage_coefficient`（倍率），写入 `node_user_usages` / `node_usages`
- 面板侧节点管理路由（`app/routers/node.py`）：`GET /api/node/settings`、`POST /api/node`、`GET /api/node/{id}`、`WS /api/node/{id}/logs`、`GET /api/nodes`、`PUT /api/node/{id}`、`POST /api/node/{id}/reconnect`、`DELETE /api/node/{id}`、`GET /api/nodes/usage`

**Remnawave 的节点协议（更现代，值得参考）**：节点凭据是一个 **base64 JSON blob 放在 `SECRET_KEY` 环境变量**，解出 `{caCertPem, jwtPublicKey, nodeCertPem, nodeKeyPem}`（由面板 `keygen` 表签发）。传输层 mTLS TLS 1.3 + `rejectUnauthorized: true`，应用层每个 controller 再套 `@UseGuards(JwtDefaultGuard)` 验非对称 JWT。方向同样是**面板 → 节点单向**，节点是纯被动服务端，根路径 `/node`：`POST /node/handler/{add-user, remove-user, drop-users-connections, ...}`、`POST /node/stats/{get-users-stats, get-system-stats, get-users-ip-list, ...}`、`POST /node/xray/{start, stop}`。配置下发用 zstd 压缩 + `X-Config-Sha256` 完整性头。

**三种模型对比：**

| 维度 | v2board/Xboard UniProxy | Marzban | Remnawave |
|---|---|---|---|
| 方向 | **节点主动轮询面板** | 面板主动控制节点 | 面板主动控制节点 |
| 认证 | 全局共享 token（query） | mTLS 客户端证书 | mTLS TLS1.3 + 非对称 JWT |
| 用户下发 | 节点拉全量（ETag/304） | 面板逐条推 | 面板逐条推 |
| 流量 | 节点 POST `/push` | 面板 gRPC 拉 Xray stats | 节点端点被调用取 stats |
| 节点需公网入站 | ❌ **不需要** | ✅ 需要（62050/62051） | ✅ 需要（2222） |
| 生态兼容 | ✅ 大量现成 agent | 仅 Marzban-node | 仅 remnawave/node |

> **对 babel.plus 的取舍**：**采用 UniProxy 的「节点主动轮询」方向**。理由有二：一是节点不需要开放入站端口、不需要固定可达地址，运维上省掉一整类问题（节点在 NAT 后也能跑）；二是面板侧天然无状态，**这正好是 Cloud Run 想要的形态**——Marzban/Remnawave 那种「面板持有到每个节点的长连并主动推送」的模型，在会随时被回收、随时多实例的 Cloud Run 上非常难做对。
> 但**认证方式要取 Remnawave 的**：per-node 密钥，走 Header，不走 query。

---

## 三、订阅 URL 生成机制

**Xboard / v2board 的用户订阅链接由 `v2_user.token`（`char(32)`）驱动，不是 JWT，无过期、无签名。**

两条路由：

| 路由 | 定义位置 |
|---|---|
| `GET /api/v1/client/subscribe?token={token}` | `app/Http/Routes/V1/ClientRoute.php`，name `client.subscribe.legacy` |
| `GET /{subscribe_path}/{token}` | `routes/web.php`，`subscribe_path` 后台可配，**默认 `s`** → 即 `https://host/s/{token}`，name `client.subscribe` |

**鉴权（`app/Http/Middleware/Client.php`）**：`User::where('token', $token)->first()`，找不到抛 403。极简，但意味着 **token 泄露 = 订阅永久泄露**，只能靠用户手动重置。

**内容协商**（`ClientController::doSubscribe`）：
1. `UserService::isAvailable($user)` 不通过直接返回空 body + 403
2. `ServerService::getAvailableServers($user)` 按 `group_id` 取节点
3. 可选 query：`types`（按协议过滤）、`filter`（按关键词过滤节点名）、`flag`（强制指定客户端）
4. `app('protocols.manager')->matchProtocolClassName($clientInfo['flag'])` —— **按 User-Agent 匹配** `app/Protocols/*.php`，匹配不到回落 `General.php`（base64 分享链接）
5. 节点名自动加协议前缀：`[vless]` `[ss]` `[vmess]` `[trojan]` `[tuic]` `[socks]` `[anytls]`，Hysteria 按版本 `[Hy]` / `[Hy2]`
6. 用量信息通过 **`subscription-userinfo` 响应头**下发（`app/Protocols/General.php:53`，Clash / ClashMeta / SingBox / Loon / QuantumultX / Stash 均实现）：
   ```
   content-type: text/plain
   subscription-userinfo: upload={u}; download={d}; total={transfer_enable}; expire={expired_at}
   ```
   这是 Clash 系客户端显示「已用 / 总量 / 到期」的事实标准，**自研必须原样实现**。

---

## 四、商业市场调研

> 采集时间 2026-08-16。价格随地域和时间变动，标注「**一手**」的为直接抓取厂商官方页面，标注「**二手**」的为评测/聚合站（因 `nordvpn.com` / `expressvpn.com/order` 返回 403、`protonvpn.com/pricing` 价格为前端渲染，无法直接抓取）。

### 4.1 中国「机场」市场：包装与定价惯例

| 维度 | 惯例 | 实测数据 |
|---|---|---|
| 档位结构 | 3–5 档，按「月流量 GB」分级 | 飞鸟 100/200/500GB = ¥15/¥30/¥75；一云梯 100/200/400GB = ¥15/¥30/¥60；SNTP 100/200/350/500GB = ¥18/¥28/¥38/¥48（二手） |
| 主流价格锚点 | **100GB/月 ≈ ¥15–25**，即 **¥0.10–0.25/GB** | ¥15（飞鸟/一云梯）、¥18（SNTP）、¥14.9（唯兔云）、¥17（COCODUCK）；低价直连可到 ¥2/100GB（良心云）、¥1.5/100GB（一元机场，一手） |
| **倍率（rate）** | 实际用量 × 倍率 = 扣配额。1x 默认；廉价直连/普通中转 0.1x–0.5x；IPLC/IEPL 专线 1.5x–3x+ | YkkCloud 1x/0.5x/0.1x 三档；CatNet 深港 IEPL 0.3x；奈云 IPLC 3x；泰山网专线 5x；91 机场星链节点 **20x**（二手） |
| **不限时套餐** | 一次性买 GB，永久有效或 365 天，不按月扣 | 永久：魔戒 130GB ¥14.9（¥0.114/GB）、精灵学院 3600GB ¥520（≈¥0.15/GB）、CyberGuard 2160GB ¥550；365 天：Tolink/AllBlue/Mikasa 均 100GB ¥58.8（¥0.588/GB）（二手） |
| **流量重置** | 月付按账单日自动重置清零；超量后可**额外买重置包**恢复本月配额，**不延长有效期** | 一手文档原文：「每个月的流量会在下个月账单日自动重置，即已用流量清零」；「流量使用量达到 90% 以上才能重置，未达到重置按钮是隐藏的」；「重置包只在套餐有效期内生效」。**重置包具体价格官方文档只写「【亿点点】钱」→ 待核实** |
| 周期折扣 | 高度标准化：**季付 9折、半年 85折、年付 8折** | 飞鸟/一云梯 100GB：月 ¥15 / 季 ¥41 / 半年 ¥77 / 年 ¥144（正好 9/8.5/8 折）；CatNet 明示同一套折扣（二手） |
| 设备数限制 | 3–5 台最常见；廉价机场反而给 10–20 台或不限 | CatNet 3 台、白月光 5 台、一分机场 10 台（另加「日用量 ≤ 月配额 20%」）、良心云 20 台、YkkCloud/闪狐云不限（二手） |
| 限速 | 套餐限速 100–300 Mbps 常见；专线机场主打不限速 | 龙猫云「全 IPLC 专线不限速」；极速云「带宽上限 2000Mbps」（二手） |
| 订阅交付 | 付款后给一条订阅 URL，客户端自动拉节点 | 三种格式：Clash YAML / V2Ray base64 / 通用（sing-box JSON、Surge conf）。建议「优先选支持通用订阅或 Clash 订阅的机场」 |
| 订阅转换 | subconverter，`?target=clash\|singbox\|surge\|quanx\|v2ray&url={encoded}` | **公共实例会泄露订阅 token**：「第三方可以看到你的请求，包括订阅 URL 中的 token」。自建 `docker run -p 25500:25500 tindy2013/subconverter` |
| 邀请返利 | 用户级返佣普遍存在，**新机场 ~20%** 是行业口径 | 运营者原话：「佣金 20% 对于新机场而言算少的了」；同页成本结构「第三方支付再抽 10% 左右手续费」「大概还剩 30% 利润，1w 流水赚 3000」 |
| 试用政策 | **月付试水 > ¥1 试用 > 免费试用**；免费试用普遍 1–3 天 / 2–9GB | GLaDOS 3天5GB；Besnow 3天9GB；FlyBit 24h/2GB；魔戒 ¥1/2GB；奈云 1天6GB；SSRDOG 24h/3GB（二手） |
| 优惠码文化 | 每家常驻一个 8–95折 码，评测站各持专属码 | SSRDOG `KERRYNOTES` 9折；TNT `TNT85` 85折；NiceCloud `NiceCloud` 常驻95折（二手） |

**具体档位样本（二手，来源见文末）：**

| 机场 | 档位 | 月 | 季 | 半年 | 年 | 倍率 / 设备 |
|---|---|---|---|---|---|---|
| 飞鸟 | 100/200/500GB | ¥15/30/75 | ¥41/81/203 | ¥77/153/383 | ¥144/288/720 | 1x |
| 一云梯 | 100/200/400GB | ¥15/30/60 | ¥41/81/162 | ¥77/153/306 | ¥144/288/576 | 1x |
| 肥猫云 | 120/300/750GB | ¥20/40/100 | ¥54/108/270 | ¥102/204/510 | ¥192/384/960 | 1x / 不限设备 |
| 闪狐云 | 120/240/500/1000GB | ¥20/40/72/125 | ¥54/108/194/337 | ¥102/204/367/637 | ¥192/384/691/1200 | 无设备/IP 限制 |
| 良心云 | 100/500/1000GB | ¥2/4/6 | ¥6/12/18 | ¥12/24/36 | ¥24/48/72 | 全 1x / 20 设备 |
| 一元机场（一手） | 100/500/1000GB | ¥1.5/2.99/5.99 | — | ¥9/17.99/34.99 | ¥18/34.99/68.99 | — |
| CyberGuard（不限时） | 220/840/2160GB | ¥79 / ¥188 / ¥550 一次性，永久有效 | | | | ≈¥0.22–0.36/GB |

### 4.2 国际厂商对比

| 厂商 | 价格（币种按抓取所得） | 计费周期 | 设备数 | 配额模型 | 试用 / 退款 | 推荐 / 联盟 | 来源 |
|---|---|---|---|---|---|---|---|
| **Mullvad** | **€5/月，全周期同价**（另显示 $5.78 / £4.28 / SEK 55.11） | 1 月 / 1 年 / 10 年，**均为同一 €5/月，无量折** | 5 | 不限流量 | **14 天退款**（现金支付不适用，反洗钱） | **无推荐计划**；仅提及第三方 reseller | 一手 |
| **Mullvad 支付** | **加密货币支付 9折（10% off）** | 接受现金、BTC、BCH、Monero、电汇、卡、PayPal、Swish 等 | | | | | 一手 |
| **Proton VPN** | Free $0；VPN Plus $9.99 / $3.99 / $2.99；Unlimited $12.99 / $9.99 / $7.99 | 1月 / 1年 / 2年 | Free 1；付费 10 | **无流量上限、不限速（Free 也是）**；Free 限 10 国、随机分配、禁流媒体/BT | **30 天按比例退款**（只退未用部分）；Free 无需信用卡；通过推荐链接得 2 周试用 | **推荐人与被推荐人各得 $20 credit**，总上限 $1000（≈50 人）；被推荐人须完整订阅满 1 个月 | 价格二手（官网前端渲染）；设备数/退款/推荐一手 |
| **NordVPN** | Basic $14.99 / $5.49 / $3.49；Complete $29.99 / $6.99 / $4.99；Prime $25.29 / $9.49 / $7.49（月均） | 1月 / 1年 / 2年 | 最多 10 | 不限流量 | **30 天退款；无免费试用** | **1 月单新签 100%**，1年/2年 **40%**，续费 **30% 经常性**；cookie 30 天；自营联盟网络 | 二手（官网 403）；「Prime 1月 $25.29 低于 Complete $29.99」看着反常 → **待核实** |
| **ExpressVPN** | Basic $12.99 / $4.99 / $2.79；Advanced $13.99 / $5.99 / $3.59；Pro $19.99 / $8.99 / $5.99（月均） | 1月 / 1年 / 2年，**年付送 3 个月、两年付送 4 个月** | Basic 10 / Advanced 12 / Pro 14 | 不限流量 | **30 天退款**（含月付）；部分移动端有免费试用 | 推荐（一手）：「你和朋友各得 30 天免费」「推荐人数无上限」。联盟佣金率官网**刻意不披露** → **待核实** | 价格二手（官网 403）；推荐/联盟一手 |
| **Surfshark** | USD（二手）：Starter $15.45 / $2.98 / $1.78；One $17.95 / $3.38 / $2.08；One+ $20.85 / $6.98 / $4.18。官网向我方返回 **JPY**：Starter ¥388/月（总 ¥10,476）等 | 1月 / 12月 / 24月，**年付与两年付各送 3 个月**；两年付首期一次性收，之后按年续 | **不限同时在线设备**（免费试用限 3 台） | 不限流量 | **30 天退款，每客户仅一次**；iOS/Android 应用商店 **7 天免费试用**，无需先绑卡 | **新签分成 40% 起**；cookie 30 天；**$100 起提**；结清后 30 天内付款 | 结构/退款/设备一手（官网**按地域返回 JPY**）；USD 价二手 |
| **Outline / Jigsaw** | **无消费者定价——Outline 本身免费**，成本 = 你的云账单（「许多云厂商 $5/月 以内」） | 自托管 | 访问密钥数不限 | **按密钥设流量上限**是内建功能 | N/A | N/A | 一手 |

**Outline 打包方式（一手）**：三件套 Outline Manager（桌面管理端）+ Outline Client（移动/桌面）+ Outline SDK；支持 DigitalOcean / AWS / Google Cloud 或自有 Linux；治理方为独立非营利 Outline Foundation（2018 年由 Google 孵化器 Jigsaw 发起）；管理员生成唯一 access key 分发，「支持数百用户」。

### 4.3 跨市场结论（对 babel.plus 有直接产品含义）

1. **两种截然相反的配额哲学。** 国际厂商卖「不限流量 + 按设备数分档」（Surfshark 不限设备、Nord 10、Express 10/12/14、Mullvad 5、Proton 1/10）；机场卖「按 GB 计量 + 倍率 + 再加设备上限」。**倍率机制在西方完全没有对应物**——它是让同一个 GB 池同时容纳 0.1x 廉价直连和 3x–20x 专线节点的定价杠杆。babel.plus 若面向大陆用户，倍率是必须实现的，因为用户已经被教育过这套心智。
2. **折扣阶梯方向相反。** 国际厂商按期限深折（Surfshark 24 个月比自家月付便宜约 88%）然后**高价续费**；机场折扣浅且标准化（9/8.5/8 折），且**无首购-续费价差**。社区明确劝退长期：「现在一定要月付，一定要先试」「任何机场都有跑路可能」——**决定周期的是跑路风险而不是价格**。这意味着新服务想卖年付，必须先解决信任问题（公开运营主体、退款承诺、历史 uptime），否则定价再低也卖不动。
3. **返佣经济学差一倍。** 国际约 40%（Surfshark 40%、Nord 1年/2年 40% + 续费 30%），机场约 20%。而机场运营者自述的成本结构是「佣金 20% + 支付通道 10% ≈ 吃掉 30% 毛收入」——**返佣比例必须在定价模型里先扣掉，不能事后再想**。
4. **试用结构由信任模型决定。** 国际靠退款窗口（Mullvad 14 天、其余 30 天）+ 应用商店免费试用；机场靠微型试用（1–3 天 / 2–9GB 或 ¥1/2GB），因为**没有退款基础设施，试用本身就是保证**。
5. **交付方式是最大的结构差异，也是最大的安全债。** 国际厂商发签名原生 App；机场发一条 **URL**，由第三方开源客户端消费——这催生了 subconverter/Sub-Store 这一整层，也带来真实的 token 泄露面。babel.plus 的订阅 token 设计必须比 v2board 的「32 位明文永久 token」更强。
6. **Mullvad 是结构性异类**：全周期同价、无促销、无联盟、加密货币 9 折。如果 babel.plus 想走「可信、简单、隐私优先」的差异化，Mullvad 是唯一可参照的样本；但它的商业模型与大陆机场用户的「按量、多节点、可挑线路」预期完全冲突，二者只能选一。

---

## 五、功能清单（Feature Checklist）

等级定义：**必备** = 没有它服务跑不起来 / 会被投诉；**建议** = 显著影响留存或运营效率；**可选** = 规模化后再做。

### 5.1 账户体系

| # | 功能 | 等级 | 说明 |
|---|---|---|---|
| 1 | 邮箱注册 / 登录 / 找回密码 | 必备 | 参考 `v2_user.email` unique |
| 2 | 密码哈希 + salt | 必备 | Xboard 用 `password_algo` + `password_salt` 兼容多种历史算法 |
| 3 | API Token（供前端 SPA / App 用） | 必备 | Xboard 用 Laravel Sanctum |
| 4 | 邮箱验证码 / 白名单域名限制 | 建议 | 防批量注册薅试用 |
| 5 | 邀请码体系（`invite_user_id`） | 建议 | 拉新主渠道 |
| 6 | 佣金 / 返利（比例、周期型 vs 一次性） | 建议 | Xboard：`commission_type` 0系统/1周期/2一次性 + `commission_rate` |
| 7 | 账户余额（integer 存分） | 建议 | 佣金提现、余额抵扣 |
| 8 | 封禁标记（`banned`） | 必备 | 风控落点 |
| 9 | 管理员 / 客服分级（`is_admin` / `is_staff`） | 必备 | — |
| 10 | Telegram 账号绑定 | 可选 | 通知与自助查询 |
| 11 | 2FA / TOTP | 建议 | 管理员账号必须 |
| 12 | 登录 IP / 时间审计 | 建议 | `last_login_at` / `last_login_ip` |
| 13 | OAuth（Google/Apple） | 可选 | — |

### 5.2 订阅与流量

| # | 功能 | 等级 | 说明 |
|---|---|---|---|
| 1 | 订阅 URL（唯一 token） | 必备 | `/{s}/{token}` |
| 2 | 多客户端格式适配（按 UA 分发） | 必备 | Clash / ClashMeta / sing-box / Surge / Shadowrocket / QuantumultX / Loon / Stash / Surfboard / base64 |
| 3 | 订阅内嵌流量与到期信息 | 必备 | 客户端展示"已用 X / 总 Y / 到期 Z" |
| 4 | 流量配额 `transfer_enable` + `u` / `d` 累加 | 必备 | — |
| 5 | 节点倍率 `rate` 折算 | 必备 | 高成本节点（IPLC/流媒体）用倍率定价 |
| 6 | 到期时间 `expired_at`，NULL = 永久 | 必备 | 支撑「不限时套餐」 |
| 7 | 流量重置（每月 1 号 / 按月 / 不重置 / 每年 / 按年） | 必备 | `v2_plan.reset_traffic_method` 五种模式 |
| 8 | 单独购买重置包（`reset_price`） | 建议 | 中国机场标配收入项 |
| 9 | 重置审计日志 | 建议 | `v2_traffic_reset_logs` |
| 10 | 限速 `speed_limit`（Mbps） | 建议 | 低价套餐差异化 |
| 11 | 设备/IP 数限制 `device_limit` | 建议 | 需要 `alive` + `alivelist` 全网协同 |
| 12 | 订阅过滤参数（`types` / `filter` / `flag`） | 建议 | 老客户端兼容 |
| 13 | 订阅模板（自定义 Clash 规则组） | 建议 | `v2_subscribe_templates` |
| 14 | 用量日/月聚合（按倍率分桶） | 必备 | `v2_stat_user` |
| 15 | 流量/到期提醒（`remind_traffic` / `remind_expire`） | 建议 | — |
| 16 | 订阅 token 一键重置 | 建议 | 泄露后的唯一止血手段 |
| 17 | 订阅链接防盗（UA 白名单 / 频率限制 / 单 IP 限制） | 建议 | — |

### 5.3 节点管理

| # | 功能 | 等级 | 说明 |
|---|---|---|---|
| 1 | 节点 CRUD + 协议参数（JSON） | 必备 | `v2_server.protocol_settings` |
| 2 | 节点分组 `group_ids` 与套餐挂钩 | 必备 | 决定「哪个套餐能看到哪些节点」 |
| 3 | 倍率 `rate` | 必备 | — |
| 4 | 上下架 `show` / 启停 `enabled` | 必备 | — |
| 5 | 节点排序 / 标签 `tags` | 建议 | — |
| 6 | 父子节点（中转 `parent_id`） | 建议 | 中转链路的核心建模 |
| 7 | 节点拉取用户（带 ETag / 304） | 必备 | 不做 ETag 会把 DB 打爆 |
| 8 | 节点流量上报 + 队列异步入账 | 必备 | — |
| 9 | 节点在线人数 / 最后上报时间 | 必备 | 掉线告警的依据 |
| 10 | 节点负载上报（CPU/内存/磁盘） | 建议 | `/status` |
| 11 | 节点级用量统计 | 建议 | `v2_stat_server`，用于算成本 |
| 12 | 节点路由/分流规则下发 | 建议 | `v2_server_route`（match / action / action_value） |
| 13 | per-node 或 per-machine 密钥 | 必备（自研时） | Xboard 的全局 `server_token` 是已知弱点 |
| 14 | 机器（宿主）与节点分离建模 | 建议 | `v2_server_machine`，一机多节点 |
| 15 | 配置热推送（WebSocket 长连） | 可选 | 轮询 60s 已够用 |
| 16 | 节点健康检查 / 自动摘除 | 建议 | Xboard 无内建，需自建 |

### 5.4 支付与订单

| # | 功能 | 等级 | 说明 |
|---|---|---|---|
| 1 | 订单表 + 唯一 `trade_no` | 必备 | — |
| 2 | 订单状态机（待支付/开通中/已取消/已完成/已折抵） | 必备 | — |
| 3 | 订单类型：新购 / 续费 / 升级 | 必备 | `type` 1/2/3 |
| 4 | 周期定价（月/季/半年/年/两年/三年/一次性） | 必备 | `v2_plan` 七个价格字段 |
| 5 | 升级折抵（剩余价值计算） | 建议 | `surplus_amount` / `surplus_order_ids` |
| 6 | 优惠券（金额/折扣、次数、限套餐、限周期、有效期） | 建议 | `v2_coupon` |
| 7 | 余额支付 | 建议 | `balance_amount` |
| 8 | 支付渠道插件化 + 手续费（固定/百分比） | 必备 | `v2_payment.handling_fee_fixed` / `handling_fee_percent` |
| 9 | 支付回调（异步 notify + 幂等） | 必备 | `callback_no` |
| 10 | 加密货币收款（USDT / BTCPay） | 建议 | 规避传统渠道风控，本行业几乎标配 |
| 11 | 支付宝当面付 / 易支付 / 码支付 | 建议 | 大陆用户主付款方式（合规风险见 5.7） |
| 12 | 佣金计算与发放状态机 | 建议 | `commission_status` 0待确认/1发放中/2有效/3无效 |
| 13 | 礼品卡 / 兑换码 | 可选 | `v2_gift_card_*` |
| 14 | 发票 / 收据 | 可选 | — |
| 15 | 退款流程 | 建议 | Xboard 只有 `surplus_credit` 折抵额，无完整退款流 |

### 5.5 工单与通知

| # | 功能 | 等级 | 说明 |
|---|---|---|---|
| 1 | 工单 + 消息（`status` / `reply_status` / `level`） | 必备 | — |
| 2 | 工单待回复提醒（管理端红点 / TG 推送） | 必备 | — |
| 3 | 邮件发送 + 发送日志 | 必备 | `v2_mail_log` |
| 4 | 邮件模板管理 | 建议 | `v2_mail_templates` |
| 5 | 站内公告 | 必备 | `v2_notice`（含 `img_url` / `tags` / `sort`） |
| 6 | 知识库 / FAQ（含客户端使用教程） | 必备 | `v2_knowledge`。**这个行业 70% 的工单是"客户端怎么用"** |
| 7 | Telegram Bot 通知 / 自助查询 | 建议 | Xboard 有 `Telegram` 插件 |
| 8 | 到期前 / 流量将尽自动提醒 | 建议 | 续费转化关键 |
| 9 | 工单附件 / 图片 | 可选 | — |

### 5.6 后台运营

| # | 功能 | 等级 | 说明 |
|---|---|---|---|
| 1 | 用户列表：搜索、编辑配额/到期/分组、封禁 | 必备 | — |
| 2 | 后台路径可自定义（默认哈希） | 必备 | Xboard `secure_path`，默认 `hash('crc32b', app.key)` |
| 3 | 全局日报表：订单数/额、注册数、邀请数、总流量 | 必备 | `v2_stat` |
| 4 | 用户维度用量查询 | 必备 | `v2_stat_user` |
| 5 | 节点维度用量（算成本） | 建议 | `v2_stat_server` |
| 6 | 管理员操作审计日志 | 建议 | `v2_admin_audit_log` |
| 7 | 系统设置 KV 表 + 热生效 | 必备 | `v2_settings` |
| 8 | 批量操作（批量续期/批量发券/群发邮件） | 建议 | — |
| 9 | 队列监控（失败重试、积压告警） | 必备 | Xboard 用 Horizon |
| 10 | 插件/主题机制 | 可选 | 自研可省 |
| 11 | 数据导出（CSV） | 可选 | — |

### 5.7 风控与合规

| # | 功能 | 等级 | 说明 |
|---|---|---|---|
| 1 | 注册验证码（reCAPTCHA / Turnstile） | 必备 | Xboard 依赖 `google/recaptcha` |
| 2 | 登录/注册频率限制、IP 限制 | 必备 | — |
| 3 | 同账号多设备并发检测（`alive` + `alivelist`） | 建议 | 防止一号多卖 |
| 4 | 试用滥用检测（同 IP / 同邮箱域 / 同设备指纹） | 建议 | — |
| 5 | 支付欺诈 / 拒付（chargeback）处理 | 建议 | 加密货币可规避大部分 |
| 6 | 节点侧审计规则（屏蔽 BT / 违法站点） | 必备 | `v2_server_route` 的 `action` / `action_value`；XrayR 系有 `ReportIllegal` 接口但 v2board 驱动未实现 |
| 7 | 敏感目的地黑名单（钓鱼、CSAM、暗网市场） | 必备 | 出口 IP 声誉与法律风险 |
| 8 | 出口 IP 滥用投诉（abuse report）处理流程 | 必备 | 上游 VPS 商会直接停机 |
| 9 | 主域名被墙的备用域名 / 中转入口 | 必备 | 面板域名必然会被 DNS 污染 |
| 10 | `safe_mode`：非配置域名访问一律 403 | 建议 | Xboard `safe_mode_enable`，防被人套用前端做钓鱼 |
| 11 | 日志最小化（不存用户访问的目的地址） | 必备 | 既是隐私承诺也是自我保护 |
| 12 | 数据库备份 + 异地存储 | 必备 | Xboard 依赖 `spatie/db-dumper` |
| 13 | 服务条款 / 隐私政策 / 退款政策 | 必备 | — |
| 14 | 主体与收款通道的司法辖区规划 | 必备 | 待核实：具体方案需法务确认 |
| 15 | IP 归属地库（用于风控展示） | 可选 | Xboard 依赖 `zoujingli/ip2region` |

---

## 六、对 babel.plus 的建议

### 6.1 结论

> **不 fork。自研 API + Web，但严格照抄 Xboard 的数据模型与 UniProxy 节点协议，并按 6.4 的清单从 SSPanel / Marzban / Remnawave / 3x-ui 各取一处更优设计。**

用一句话概括：**Xboard 值钱的是它的领域知识（表结构、状态机、倍率结算、订阅协议适配、UniProxy 契约），不是它的代码。** 代码带着一整套与我们部署目标（API/Web 分离 + GCP Cloud Run）正面冲突的运行时假设；领域知识可以零成本搬走，而且是 MIT 许可。

### 6.2 为什么 Xboard 是唯一值得认真讨论的 fork 候选

先把其他七个排除掉，理由都很硬：

| 候选 | 排除理由 |
|---|---|
| **v2board** | master 停在 2023-06（v1.7.4），三年无维护 |
| **Hiddify** | `hiddifypanel` 是 **CC BY-NC-SA 4.0，明确禁止商业使用**；客户端也带 NonCommercial 附加条款。法律上直接出局 |
| **3x-ui** | GPL-3.0（传染）；**无计费、无订单、无工单、无终端用户登录**。它是运维工具不是生意 |
| **Marzban** | AGPL-3.0（网络传染，SaaS 也要开源）；master 停在 2025-01，Marzban-node 停在 2025-03；同样无计费无工单 |
| **Remnawave** | AGPL-3.0；**用户连登录都没有**，只有 `short_uuid`；`infra_billing_*` 是自己的服务器成本台账不是向用户收费 |
| **SSPanel-UIM** | MIT 且**是五者中唯一自带完整计费与工单的**，唯一真正的备选。但：PHP + Slim + 自研迁移体系、50 列的 `user` 表、同样的全局 `muKey` 弱认证、同样的单体架构。它的架构包袱不比 Xboard 小，而生态（客户端订阅格式适配、节点端选择）反而不如 Xboard 系丰富 |
| **XrayR / V2bX / soga** | 分别是：已废弃且源码被删 / 已归档 / 闭源商业授权 |

于是问题收敛成：**fork Xboard，还是自研 + 抄 Xboard 的模型和协议。**

### 6.3 为什么连 Xboard 也不 fork

| # | 冲突点 | 证据 |
|---|---|---|
| 1 | **Xboard 是单体，不是 API/Web 分离的。** 后台 React、用户端 Vue3 都由 Laravel 的 blade 视图渲染并从 `public/theme/` 提供静态资源；`routes/web.php` 在**请求期**执行 `File::copyDirectory($themePath, public_path('theme/'.$theme))` 把主题拷进 public 目录 | `routes/web.php` |
| 2 | **默认镜像是 supervisor 管的多进程容器**：Caddy + Octane + Horizon + Redis + WS server 全塞一个容器，`ENTRYPOINT` 是 supervisord。这是「容器里养宠物 VM」，与 Cloud Run 的单进程模型正面冲突 | `Dockerfile`、`.docker/supervisor/supervisord.conf` |
| 3 | **拆分部署仍要 4 个常驻服务 + Redis + 共享卷。** 官方 `compose.split.sample.yaml` 拓扑：caddy → (web, ws-server) + horizon + redis，且 `web/horizon/ws-server` **共享同一组 volume**（`.env`、`storage/logs`、`storage/theme`、`plugins`）。Cloud Run 没有跨服务共享可写卷（GCS FUSE 挂载不适合做 PHP 的 `plugins/` 与 opcache 目录） | `compose.split.sample.yaml` |
| 4 | **插件与主题机制依赖运行时写本地磁盘。** Cloud Run 实例文件系统是每实例内存盘、重启即失、实例间不共享——装个支付插件会在部分实例生效、部分不生效 | `plugins/` 仅含 `.gitignore`；`ThemeService` 运行时复制目录 |
| 5 | **Horizon 队列 worker 是常驻进程。** 流量入账走 `traffic_fetch` 队列。Cloud Run 上要么开 instance-based billing + `min-instances≥1`（等于常年付一台机器的钱，还随时可能被回收），要么改造成 Cloud Tasks/Pub/Sub push | `app/Jobs/TrafficFetchJob.php`；Cloud Run 计费文档：「Idle instances, including those kept warm using minimum instances, can be shut down at any time」 |
| 6 | **WebSocket 节点长连（workerman）在 Cloud Run 上是反模式。** Cloud Run 支持 WebSocket，但最长请求 60 分钟、会话亲和性只是 best-effort、有开连接的实例按 instance-based 计费。节点每小时被强制断连重连，而且面板多实例时 `NodeWorker` 的状态不共享 | Cloud Run WebSocket 文档 |
| 7 | **Fork 的维护税。** 上游自述「light maintenance」，2026 年提交频率约每月一次。Fork 后我们既拿不到快速的上游修复，又要一直手工 rebase | Xboard README + commit 记录 |
| 8 | **安全底子需要重写。** 全局共享 `server_token` 走 query string、订阅 token 是永久明文 32 位、后台路径靠 `hash('crc32b', app.key)` 混淆。这三处都要改，而它们分布在中间件、路由、订阅生成三个层面——改动量接近重写 | `Middleware/Server.php`、`Middleware/Client.php`、`routes/web.php` |
| 9 | **PHP/Laravel 与团队和 Cloud Run 的匹配度。** Cloud Run 冷启动对 PHP-FPM/Octane 不友好；Laravel 12 + Octane + Horizon + Redis 这套栈的运维复杂度，几乎全部来自「它要常驻」这个前提 | — |

### 6.4 为什么也不该「完全从零想」

Xboard/v2board 三年迭代沉淀下来的这些东西，自己想会踩一遍坑，直接抄：

1. **`u + d < transfer_enable` + `expired_at` 可空 + `banned` 三条件判可用**——一条 SQL 覆盖全部业务状态，`expired_at IS NULL` 天然支撑「不限时套餐」。
2. **倍率在面板侧结算，节点只报原始字节**——节点无状态，改倍率不用重启节点。
3. **流量不落明细流水**，只在 `users.u/d` 累加 + 按天/月聚合到 `stat_user`（按倍率分桶）/ `stat_server`。这是这个业务的性能命门。
4. **ETag + 304** 用在 `/config` 和 `/user` 上。节点每 60 秒轮询，不做 ETag 会把 DB 打爆。
5. **金额全部 integer 存分**，订单状态机 0待支付/1开通中/2已取消/3已完成/4已折抵，订单类型 1新购/2续费/3升级 + 升级折抵（`surplus_amount` + `surplus_order_ids`）。
6. **`reset_traffic_method` 五种重置模式**（跟随系统 / 每月1号 / 按月 / 不重置 / 每年1月1日 / 按年）。
7. **`subscription-userinfo` 响应头格式**与按 User-Agent 分发订阅格式——这是与 Clash/sing-box 生态的硬接口，不能自创。
8. **`/api/v1/server/UniProxy/*` 契约原样保留**——这样第一天就能用 **v2node（MPL-2.0，2026 年仍活跃）** 做节点端，不用自己写 xray 封装。这是整个方案里最大的一笔省力。

从其他面板另外抄这五条（Xboard 没有，但明显更好）：

9. **订阅 token 独立成表**（SSPanel `link` 表的思路），支持一个用户多条 token、单独命名与吊销。
10. **用 `sub_revoked_at` 语义做吊销**（Marzban）：token 内嵌签发时间，比对用户的 `sub_revoked_at` 即可一键失效全部旧链接，**不必更换标识符**。
11. **订阅拉取审计表**（Remnawave `user_subscription_request_history`：`user_id, request_ip, user_agent, request_at`）——这是唯一能看出「谁在共享账号」的数据来源，成本极低。
12. **热写字段拆表**（Remnawave 把 `used_traffic_bytes` / `online_at` 从 `users` 拆到 1:1 的 `user_traffic`）——流量累加是本系统写压力最大的地方，和用户主表分开能显著减少行锁竞争。
13. **倍率用定点整数存**（Remnawave `consumption_multiplier BIGINT`，基数 1e9），不要用 `decimal`/`float`——倍率参与金额与配额计算，浮点误差会累积。
14. **节点 token 带 scope 白名单**（3x-ui 的 `node-sync` scope + 硬编码路由允许表）：节点密钥即使泄露，也只能调那几个节点接口，碰不到管理 API。

### 6.5 建议架构（适配 API/Web 分离 + Cloud Run）

| 组件 | 形态 | 说明 |
|---|---|---|
| `babel-api` | Cloud Run service（无状态） | 用户 API + 节点 UniProxy 兼容 API + 管理 API。冷启动友好的运行时（Go / Node / Python 皆可，避免 PHP-FPM） |
| `babel-web` | 静态站，Cloud Run 或 Firebase Hosting / Cloud Storage + CDN | 用户端与后台两个 SPA，纯静态，只调 `babel-api` |
| 数据库 | Cloud SQL for PostgreSQL | 表结构照抄 Xboard（去掉 `v2_` 前缀，`created_at/updated_at` 改真正的 timestamptz，金额仍用 bigint 存分） |
| 缓存/在线态 | Memorystore for Redis（或 Cloud Run + Valkey） | 存 `alive` 设备表、节点最后上报时间、负载快照，全部带 TTL |
| 流量入账 | **Cloud Tasks 或 Pub/Sub push → Cloud Run**，不要常驻 worker | `/push` 收到后立即入队返回，push 订阅回调到同一个 Cloud Run service 的内部路由。这样彻底不需要 `min-instances` |
| 定时任务 | Cloud Scheduler → Cloud Run | 流量重置、到期处理、日/月统计聚合、订单超时取消 |
| 节点侧 | **v2node**（MPL-2.0），协议保持 UniProxy v1 兼容 | 不用 XrayR（已废弃）、不用 V2bX（已归档）、不用 soga（闭源 + USDT 授权 + 绑定域名） |
| 配置下发 | **只做 60 秒轮询 + ETag，不做 WebSocket** | Cloud Run 上 WS 长连的成本与复杂度远超收益 |

### 6.6 必须相对 Xboard 加固的地方

| # | Xboard 现状 | babel.plus 应做 | 参照对象 |
|---|---|---|---|
| 1 | 全节点共用一个明文 `server_token`，走 query string（会进 access log） | **每节点独立密钥 + scope 白名单**，走 `Authorization: Bearer`，DB 里存哈希；支持在线轮换与吊销 | 3x-ui（哈希存储 + scope）、Remnawave（面板签发非对称凭据） |
| 2 | 订阅 token 是 `users.token` char(32)，明文、永久、无签名，泄露后只能手工重置 | 独立 token 表（多条、可命名、可单独吊销）+ 内嵌签发时间 + `sub_revoked_at` 一键全撤 + 每次拉取写审计表 | SSPanel `link` + Marzban `sub_revoked_at` + Remnawave `user_subscription_request_history` |
| 3 | 后台路径靠 `hash('crc32b', app.key)` 混淆，无强制 2FA | 后台走**独立域名 + Cloud Armor/IAP 或 IP 白名单 + 强制 TOTP**，不靠路径混淆 | Remnawave 已上 WebAuthn passkey，可作为后续目标 |
| 4 | 用户主表 `v2_user` 既是身份表又是高频流量累加表 | 流量热字段拆到 1:1 的 `user_traffic` | Remnawave |

### 6.7 权衡与代价（诚实版）

- **代价 1：起步慢 2–4 周。** Fork Xboard 一周就能跑起来一个能收钱的站。自研要先把用户/套餐/订单/节点/订阅五张主表和 UniProxy 六个端点写出来。**但这笔时间会在第一次需要横向扩容、第一次要拆 API/Web、第一次上游发安全补丁时全部赚回来。**
- **代价 2：订阅格式适配是纯体力活。** Xboard 的 `app/Protocols/` 有 11 个适配器（Clash / ClashMeta / SingBox / Surge / Surfboard / QuantumultX / Loon / Stash / Shadowrocket / Shadowsocks / General）。这部分**建议按 MIT 许可直接移植逻辑**（保留版权声明），不要重新推导 YAML 结构——各客户端的字段兼容性是靠三年 issue 磨出来的。
- **代价 3：支付渠道要自己接。** Xboard 现成有 AlipayF2f / Epay / Mgate / BTCPay / Coinbase / CoinPayments / Stripe。自研需要重接，但考虑到大陆收款通道本身就要按自身主体重新申请，这部分复用价值本来就低。
- **不做 fork 的额外收益**：许可干净（不必受 MIT 归属声明约束整个产品）、可以直接上 Postgres 而不是 MySQL、可以用真正的 timestamptz 而不是 int 时间戳、可以从第一天就把审计日志和幂等键做对。

### 6.8 若坚持要 fork Xboard，最小可行改造清单

（作为 Plan B 记录，不推荐）

1. 拆容器：`ENABLE_WEB` / `ENABLE_HORIZON` / `ENABLE_WS_SERVER` 各起一个 Cloud Run service，`ENABLE_CADDY=false`，前面挂外部 HTTPS LB。
2. 关掉 WS（`server_ws_enable=0`），节点退回 60 秒轮询。
3. Horizon 服务开 instance-based billing + `min-instances=1`（接受常态成本），或改用 `queue:work --once` + Cloud Scheduler 触发。
4. Redis → Memorystore；`storage/` → GCS（session/cache 必须先切到 Redis，否则多实例串号）。
5. 插件与主题**在构建期固化进镜像**，禁用运行时安装。
6. 用户端 SPA 抽出来单独部署，Laravel 只留 API + 后台。
7. 把 `Middleware/Server.php` 的全局 token 换成 per-node 密钥（这一步会破坏与所有现成节点端的兼容，需要同步改 v2node）。

第 7 条一旦做了，「fork 换来的生态兼容性」这个最大理由就消失了——这也是 6.1 结论的最后一块拼图。

---

## 参考来源

### 开源面板与节点端（源码，均通过 GitHub REST API / raw 内容实测于 2026-08-16）

- v2board — https://github.com/v2board/v2board
  - `app/Http/Routes/ServerRoute.php`、`app/Http/Routes/ClientRoute.php`、`app/Http/Controllers/Server/UniProxyController.php`、`app/Payments/`
- Xboard — https://github.com/cedar2025/Xboard
  - `app/Providers/RouteServiceProvider.php`
  - `app/Http/Routes/V1/ServerRoute.php`、`app/Http/Routes/V1/ClientRoute.php`
  - `app/Http/Routes/V2/ServerRoute.php`
  - `app/Http/Controllers/V1/Server/UniProxyController.php`
  - `app/Http/Controllers/V2/Server/ServerController.php`
  - `app/Http/Controllers/V1/Client/ClientController.php`
  - `app/Http/Middleware/Server.php`、`ServerV2.php`、`Client.php`
  - `app/Services/ServerService.php`、`app/Jobs/TrafficFetchJob.php`
  - `app/Protocols/General.php`（`subscription-userinfo` 头）
  - `database/migrations/2023_03_19_000000_create_v2_tables.php`
  - `database/migrations/2025_01_05_131425_create_v2_server_table.php`
  - `database/migrations/2025_01_04_optimize_plan_table.php`
  - `database/migrations/2025_06_21_000002_create_traffic_reset_logs_table.php`
  - `database/migrations/2026_04_11_000001_add_machine_support.php`
  - `routes/web.php`、`Dockerfile`、`compose.split.sample.yaml`、`composer.json`、`README.md`
- XrayR（已废弃，源码取自 tag `v0.9.4`）— https://github.com/XrayR-project/XrayR
  - `api/newV2board/v2board.go`、`api/newV2board/model.go`
- V2bX（已归档）— https://github.com/wyx2685/V2bX
- v2node（现役节点端）— https://github.com/wyx2685/v2node
  - `api/v2board/panel.go`、`api/v2board/user.go`、`api/v2board/node.go`
- vaxilu/x-ui — https://github.com/vaxilu/x-ui
- 3x-ui — https://github.com/MHSanaei/3x-ui
- soga 安装脚本仓库（闭源）— https://github.com/vaxilu/soga
- soga 文档（功能介绍）— https://soga.yougotme.cc/
- soga 商业授权价格 — https://soga.yougotme.cc/future/get-license-code
- soga v2board 对接文档 — https://soga.vaxilu.com/soga-v2ray/v2board-v2ray
- XrayR 对接 v2board 文档 — https://xrayr-project.github.io/XrayR-doc/dui-jie-v2board/v2board.html
- Xboard issue #738「请考虑添加对节点端项目 v2node 的支持」— https://github.com/cedar2025/Xboard/issues/738

- SSPanel-UIM — https://github.com/Anankke/SSPanel-Uim
  - `app/routes.php`（`/mod_mu/*` 路由）、`src/Middleware/NodeToken.php`、`src/Services/Subscribe.php`
  - `db/migrations/2023020100-init.php`、`db/migrations/2025073100-refactor_mfa.php`、`composer.json`
- 3x-ui — https://github.com/MHSanaei/3x-ui
  - `internal/database/model/model.go`、`internal/database/db.go`、`internal/xray/client_traffic.go`
  - `internal/web/controller/api.go`（`checkAPIAuth` / `nodeSyncScopeAllow`）
  - `internal/web/job/node_heartbeat_job.go`、`node_traffic_sync_job.go`、`internal/web/service/setting.go`
  - `internal/sub/controller.go`、`go.mod`、`frontend/package.json`
- Hiddify-Manager — https://github.com/hiddify/Hiddify-Manager
- HiddifyPanel（实际代码，注意仓库已重建）— https://github.com/hiddify/hiddifypanel
  - `hiddifypanel/models/*.py`、`hiddifypanel/panel/init_db.py`、`hiddifypanel/auth.py`
  - `hiddifypanel/hutils/node/api_client.py`、`hiddifypanel/drivers/user_driver.py`
  - `hiddifypanel/panel/user/__init__.py`、`LICENSE.md`（CC BY-NC-SA 4.0）
- Hiddify 客户端 — https://github.com/hiddify/hiddify-app
- Marzban — https://github.com/Gozargah/Marzban
  - `app/db/models.py`、`app/xray/node.py`、`app/routers/node.py`、`app/routers/subscription.py`
  - `app/utils/jwt.py`、`app/jobs/record_usages.py`、`app/config.py`、`requirements.txt`
- Marzban-node — https://github.com/Gozargah/Marzban-node
  - `main.py`、`certificate.py`
- Remnawave（文档站 / umbrella）— https://github.com/remnawave/panel
- Remnawave backend — https://github.com/remnawave/backend
  - `prisma/schema.prisma`、`libs/contract/api/controllers/subscription.ts`、`package.json`、`LICENCE`
- Remnawave node — https://github.com/remnawave/node
  - `src/main.ts`、`src/common/utils/decode-node-payload/decode-node-payload.ts`、`libs/contract/api/routes.ts`、`.env.sample`
- Remnawave frontend — https://github.com/remnawave/frontend

### GCP Cloud Run

- WebSocket 支持与限制 — https://docs.cloud.google.com/run/docs/triggering/websockets
- 计费设置与 CPU 分配 — https://docs.cloud.google.com/run/docs/configuring/billing-settings

### 中国「机场」市场（多为评测/聚合站，非厂商一手）

- https://limbopro.com/865.html
- https://clash-blog.com/77-2/
- https://kerrynotes.com/best-vpn-pay-by-traffic/
- https://kerrynotes.com/vpn-coupon-code/
- https://tuijianvpn.com/7052
- https://jichangzhinan.org/blog/2026-jichang-tuijian/
- https://vpsknow.com/airport-recommendations
- https://jichangce.com/free-trial-cheap-proxy-services/
- https://www.jichangcha.com/blog/mianfei-shiyong-jichang/
- 倍率与参数定义 — https://blog.e.show/posts/airport-parameters/
- 订阅链接使用 — https://www.ermao.net/article/jichang-subscription-guide/
- 订阅转换与 token 泄露风险 — https://www.chonglangbiji.com/howto/subscription-convert/
- subconverter — https://subconverter.org/
- 机场运营者成本与返佣自述 — https://bulianglin.com/archives/air.html
- 一元机场官网（一手） — https://yiyuan-jichang.com/
- 流量重置机制（一手帮助文档）— https://v2ray.tawk.help/article/xufei
- 流量重置门槛（一手帮助文档）— https://doc.nicehcloud.com/zh/article/5awx6asq6yen572u5yyf5piv5lua5lmi5osp5ocd77yf-z2ete2/

### 国际厂商

- Mullvad 定价（一手）— https://mullvad.net/en/pricing
- Proton VPN 定价页（一手，价格前端渲染）— https://protonvpn.com/pricing
- Proton 推荐计划（一手）— https://proton.me/support/referral-program
- Surfshark 定价（一手，按地域返回币种）— https://surfshark.com/pricing
- Surfshark 联盟计划（一手）— https://surfshark.com/affiliate
- ExpressVPN 推荐计划（一手）— https://www.expressvpn.com/support/knowledge-hub/does-expressvpn-have-a-referral-program/
- ExpressVPN 联盟计划（一手，未披露佣金率）— https://www.expressvpn.com/affiliates
- Outline（一手）— https://getoutline.org/ 、https://getoutline.org/get-started/
- NordVPN / ExpressVPN / Surfshark / Proton 价格（**二手**，官网 403 或前端渲染）— https://www.security.org/vpn/nordvpn/ 、https://www.security.org/vpn/expressvpn/ 、https://www.security.org/vpn/surfshark/ 、https://www.security.org/vpn/protonvpn/
- NordVPN 联盟佣金（二手）— https://wecantrack.com/programs/nordvpn-affiliate-program/

---

## 附：待核实清单

| # | 事项 | 原因 |
|---|---|---|
| 1 | 机场「重置包」的具体人民币价格 | 两份一手帮助文档都只描述机制，价格写作「【亿点点】钱」 |
| 2 | soga 社区免费版的用户数上限（第三方称 88 用户） | 官方文档未直接确认 |
| 3 | NordVPN 续费价格、联盟提现门槛 | nordvpn.com 返回 403 |
| 4 | NordVPN「Prime 月付 $25.29 低于 Complete $29.99」 | 数据取自二手站，档位顺序反常，发布前需按正确地域核对 |
| 5 | ExpressVPN 联盟佣金率 | 官方联盟页刻意不披露 |
| 6 | Proton VPN 年付 VPN Plus 价格（$3.99 vs $4.99 冲突） | 官网价格为前端渲染，两个二手来源不一致 |
| 7 | Surfshark 的 USD 价格 | 官网按抓取地返回 JPY |
| 8 | babel.plus 的经营主体与收款通道所在司法辖区方案 | 需法务确认，本调研不覆盖 |
| 9 | 3x-ui 中无显式 `TableName()` 的模型的真实表名（`users` `nodes` `api_tokens` `node_client_traffics` `node_client_ips` `client_global_traffics` `outbound_subscriptions` `outbound_traffics` `inbound_client_ips` `history_of_seeders`） | 由 GORM 命名约定推导，未从原始 SQL 读到。`inbounds` `clients` `client_traffics` `hosts` `client_inbounds` `settings` **已直接确认** |
| 10 | Hiddify 的 `report_detail` 表名 | Flask-SQLAlchemy 约定推导，无原始 SQL 佐证 |
| 11 | `Hiddify-Manager` 究竟以哪个许可为准 | 仓库同时存在 GPL-3.0 的 `LICENSE` 与 CC0-1.0 的 `LICENSE.md`。注意：真正决定「不可商用」的是 `hiddify/hiddifypanel` 的 CC BY-NC-SA 4.0，这一条**无歧义** |
| 12 | Hiddify `ConfigEnum.core_type` 的完整合法取值 | 只有 `'xray'` 有直接佐证（且在注释行里） |

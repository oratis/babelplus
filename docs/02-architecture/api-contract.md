# API 契约：三面五链，只有 UniProxy 是冻结的

> 日期：2026-08-16 · 性质：**设计方案** · 状态：**设计稿 v1**（2026-08-16，未实施）
> 事实基线：UniProxy 端点、载荷与字段名来自
> [panels-and-market.md](../01-research/panels-and-market.md) §2.1（Xboard / XrayR 源码实测，2026-08-16）；
> 鉴权链、ETag 实现约束、OpenAPI 工作流来自 [ADR 0006](../05-adr/0006-api-stack.md)；
> 在线态与 `node_rev` 存储形态来自 [ADR 0005](../05-adr/0005-database-selection.md) §8；
> 端点清单的产品来源是 [page-inventory.md](../03-product/page-inventory.md) 与
> [user-journey.md](../03-product/user-journey.md)
> 关联：[system-design.md](system-design.md) §5（本文是它的展开）、
> [ADR 0007](../05-adr/0007-node-migration.md)（节点接入顺序）
> 证据口径：Xboard 源码字段=中（二手阅读，未跑）；v2node 实际行为=**需实测**；
> 客户端渲染行为=**需实测**
> ⚠️ **本文写的是设计目标。** 本文引用的所有表名与列名都是提案 ——
> `02-architecture/data-model.md` 尚不存在（见 §14）。

---

## 1 · 结论

九条裁决，先给结论。

| # | 裁决 | 一句话理由 |
|---|---|---|
| 1 | **只有 `/api/v1/server/UniProxy/*` 是冻结契约**，其余全是我们的设计，不背 v2board 的兼容包袱 | 冻结的目的是让 v2node 开箱即用，冻结面越小越好 |
| 2 | **UniProxy 面禁止使用统一响应信封**，响应是裸 JSON，形状逐字段照抄 Xboard | v2node 用 Go 结构体直接反序列化，包一层 `{"data":…}` 立刻不兼容 |
| 3 | **`node_id` 从密钥推导，请求里带的 `node_id` 一律忽略**；若不一致返 403 并告警 | 这是 Xboard/SSPanel「持 token 者可冒充任意节点」漏洞的根治点，不是缓解 |
| 4 | ~~认证凭据走 `Authorization: Bearer`；query string 是**有期限的**过渡态~~ → **✅ 2026-08-17 已读源码核实：v2node 只发 query，无 Authorization 支持，也无开关。** query 是**当前唯一可行形态**，「有期限」这个前提不成立（退出它需要给上游提 PR 或自行 fork）。每节点独立密钥 + scope 白名单仍然保留 | 证据 [v2node-contract-20260817 §3](../evidence/v2node-contract-20260817/) |
| 5 | **`/push` 在 HTTP 层不可能幂等，幂等做在队列侧**：入库拿 `batch_id` → 入队 → 任务处理器用 `claimed_at` 抢占 | Cloud Tasks 是 at-least-once，节点重试是另一回事，两者要分开治 |
| 6 | **流量累加不 bump `user_rev`，但「跨过 `transfer_enable` 阈值」必须 bump** | ADR 0006 §11.2 只说了前半句；不补后半句，配额耗尽的用户永远不会从节点列表消失 |
| 7 | **到期不是写操作，必须靠定时任务 bump `user_rev`** | 到期是时间驱动的状态变化，没有任何写操作会触发它 —— 这是 ETag 设计里最容易漏的一条 |
| 8 | **订阅下发：token 不存在或已吊销 → 404；banned / 到期 / 配额耗尽 → 200 + 空节点列表 + 伪节点** | 前者是防枚举，后者是 user-journey §11.2 的「订阅 URL 本身就是通知通道」 |
| 9 | **危险操作的二次确认在 API 层强制，不在前端** | 前端确认框对直接调 API 的人是零 —— `confirmation` 字段由服务端比对 |

三个面、五条互不共享的中间件链（照抄 [ADR 0006](../05-adr/0006-api-stack.md) §10）：

```mermaid
flowchart LR
    BASE["基座中间件<br/>RequestID · Recover<br/>结构化日志 · 超时"]
    BASE --> S["/api/v1/server/UniProxy/*<br/><b>节点面 · 冻结</b>"]
    BASE --> C["/api/v1/client/subscribe<br/><b>订阅面 · 生态硬接口</b>"]
    BASE --> U["/api/v1/*<br/><b>用户面 · 我们的设计</b>"]
    BASE --> A["/api/v1/admin/*<br/><b>管理面 · 我们的设计</b>"]
    BASE --> T["/internal/tasks/*<br/><b>内部面 · OIDC</b>"]

    S --> S1["Bearer 每节点密钥<br/>常数时间比对<br/>scope 硬编码白名单<br/>裸 JSON · 无信封"]
    C --> C1["订阅 token 查独立表<br/>比对 sub_revoked_at<br/>同步写审计<br/>YAML/JSON/base64"]
    U --> U1["access JWT + refresh<br/>CORS 限 Web 域名池<br/>统一信封"]
    A --> A1["IAP assertion + 会话<br/>强制 TOTP + step-up<br/>同事务写审计<br/>统一信封"]
    T --> T1["Google OIDC ID token<br/>校验 aud/iss/email<br/>幂等键表驱动"]

    style S fill:#2d5016,color:#fff
    style A fill:#5c1a1a,color:#fff
```

> **禁止「一个全局 auth 中间件 + 身份类型 if 分支」**（ADR 0006 §10.3）。
> 五条链共享的只有基座那四个。每多一条 if 分支，就多一条从节点密钥通往管理 API 的可能路径。

---

## 2 · 全局约定

### 2.1 域名与 base URL

| 面 | 域名 | 依据 |
|---|---|---|
| 用户面 / 订阅面 / 支付回调 | API 主域名 + 备用池（**独立主域名**，非 `web.*` 的子域） | [system-design.md](system-design.md) §4.1 |
| 节点面 | 同上（节点与客户端共用 API 域名池） | 节点故障时的降级语义见 §3.8 |
| 管理面 | **另一个独立主域名** + IAP/IP 白名单 | [page-inventory.md](../03-product/page-inventory.md) §4.1 |
| 内部面 | Cloud Run 服务默认 URL（`*.run.app`），不挂自定义域名 | 只被 Google 基础设施调用 |

> ⚠️ 已登记的既有矛盾：system-design §2 拓扑图用 `web.babel.plus` / `docs.babel.plus` 两个**子域**，
> 而同文 §4.1 要求「三者必须是独立域名，不能是同一域名的不同子域」。
> 本文按更严的 §4.1 写。这处矛盾需要一份 ADR 裁决，不属于本文范围。

### 2.2 统一响应信封（**只适用于用户面与管理面**）

成功：

```json
{
  "data": { "id": 42, "email": "a@example.com" },
  "meta": { "request_id": "01K2VQ7C9M0000000000000000" }
}
```

失败：

```json
{
  "error": {
    "code": "ORDER_STATE_CONFLICT",
    "message": "该订单已完成支付，无法取消",
    "details": [{ "field": "trade_no", "reason": "status=3" }]
  },
  "meta": { "request_id": "01K2VQ7C9M0000000000000000" }
}
```

四条硬规则：

1. **`data` 与 `error` 互斥，永不同时出现。**
2. **信封里不放 `status` 或 `success` 字段。** HTTP 状态码是唯一权威 ——
   `{"status":"success"}` 配 HTTP 500 这种两个事实源的形态是 v2board 系的反模式，不抄。
3. **`meta.request_id` 必然存在**（含 5xx），值即响应头 `X-Request-Id`。用户报障时直接贴这个串。
4. **`message` 是给人看的中文，`code` 是给程序看的英文常量。** 前端禁止匹配 `message` 做分支。

**三处例外，不套信封**：

| 例外 | 形态 | 理由 |
|---|---|---|
| `/api/v1/server/UniProxy/*` | 裸 JSON，形状照抄 Xboard | v2node 直接反序列化，见 §1 裁决 2 |
| `/api/v1/client/subscribe` | `text/yaml` / `application/json` / `text/plain` | 客户端生态硬接口，见 §4 |
| `GET /healthz` | `ok`（纯文本） | 探活不需要 JSON |

### 2.3 错误码体系

**字符串常量而非数字。** 数字码要维护一张人脑对照表，且在日志里 `grep 1042` 会命中一堆无关行。

| 前缀 | 领域 | 典型 HTTP |
|---|---|---|
| `AUTH_*` | 认证与会话 | 401 / 403 |
| `VALIDATION_*` | 请求格式与语义 | 400 / 422 |
| `RESOURCE_*` | 资源不存在或不可见 | 404 |
| `STATE_*` | 状态机冲突 | 409 |
| `QUOTA_*` | 配额、设备数、限流 | 403 / 429 |
| `PAYMENT_*` | 支付与订单 | 402 / 409 / 422 |
| `NODE_*` | 节点面专用 | 401 / 403 |
| `INTERNAL_*` | 我们的锅 | 500 / 503 |

P1 需要的完整码表（新增码必须先进 OpenAPI spec 的 enum，见 §12）：

| code | HTTP | 场景 |
|---|---|---|
| `AUTH_INVALID_CREDENTIALS` | 401 | 登录失败。**message 统一为「邮箱或密码不正确」，不区分哪个错** |
| `AUTH_TOKEN_EXPIRED` | 401 | access JWT 过期，前端应静默 refresh 一次 |
| `AUTH_TOKEN_INVALID` | 401 | 签名错 / 已登出 / refresh 已轮换 |
| `AUTH_TOTP_REQUIRED` | 403 | 管理面 step-up：该操作需要当次 TOTP |
| `AUTH_TOTP_INVALID` | 403 | TOTP 错误或已被使用过（防重放） |
| `AUTH_PERMISSION_DENIED` | 403 | 已认证但缺权限位 |
| `VALIDATION_FAILED` | 422 | 字段级校验失败，`details` 逐字段列出 |
| `VALIDATION_MALFORMED_BODY` | 400 | JSON 解析失败 |
| `RESOURCE_NOT_FOUND` | 404 | 通用不存在 |
| `STATE_CONFLICT` | 409 | 通用状态冲突 |
| `STATE_IDEMPOTENCY_MISMATCH` | 409 | 同一 `Idempotency-Key` 载荷不同 |
| `QUOTA_RATE_LIMITED` | 429 | 限流，必带 `Retry-After` |
| `QUOTA_DEVICE_LIMIT` | 403 | 设备数超限 |
| `PAYMENT_UNDERPAID` | 200 | **不是错误**，是订单状态；此处列出是为提醒它不走错误通道 |
| `PAYMENT_ORDER_EXPIRED` | 409 | 汇率锁定超时，需重新报价 |
| `PAYMENT_SIGNATURE_INVALID` | 401 | 支付回调验签失败 |
| `NODE_KEY_INVALID` | 401 | 节点密钥无效 / 已吊销 / 已过期 |
| `NODE_SCOPE_DENIED` | 403 | 密钥 scope 不含该路由 |
| `NODE_ID_MISMATCH` | 403 | 请求带的 `node_id` 与密钥绑定的不一致（§1 裁决 3） |
| `INTERNAL_ERROR` | 500 | 兜底。**message 不含任何内部细节**，只给 `request_id` |
| `INTERNAL_DEPENDENCY_DOWN` | 503 | DB / 支付网关不可达，必带 `Retry-After` |

HTTP 状态码使用约定：

| 码 | 用法 | 不要用来表示 |
|---|---|---|
| 200 | 成功（含 `underpaid` 这类业务中间态） | — |
| 201 | 创建成功，必带 `Location` | — |
| 204 | 删除 / 吊销成功，无 body | — |
| 304 | ETag 命中（**仅节点面**） | — |
| 401 | 未认证或凭据无效 | 「已登录但没权限」（那是 403） |
| 403 | 已认证但被拒 | 「资源不存在」（那是 404） |
| 404 | 不存在，**或存在但不该让你知道存在** | — |
| 409 | 状态机冲突、幂等键冲突 | 校验失败（那是 422） |
| 422 | 语义校验失败 | JSON 语法错（那是 400） |
| 429 | 限流 | — |

### 2.4 分页

**游标分页为默认**，因为几个最大的列表（订阅拉取审计、余额流水、审计日志）都是 append-only 且高基数，
offset 分页在深翻页时会做全表扫。

```
GET /api/v1/orders?limit=20&cursor=eyJpZCI6MTIzLCJhdCI6IjIwMjYtMDgtMTZUMTI6MDA6MDBaIn0
```

```json
{
  "data": [ … ],
  "meta": {
    "request_id": "01K2…",
    "next_cursor": "eyJpZCI6MTAzLCJhdCI6IjIwMjYtMDgtMTBUMDk6MTE6MDJaIn0",
    "has_more": true
  }
}
```

| 约定 | 值 |
|---|---|
| `limit` 默认 / 上限 | 20 / 100 |
| `cursor` 编码 | base64url 的 `{"id":…,"at":"…"}`，**不签名**（它只是位置不是凭据），但服务端必须校验解出的字段类型 |
| 无更多数据 | `next_cursor: null` + `has_more: false` |
| 总数 | **默认不返回**。管理面可传 `?count=true` 额外触发一次 `COUNT(*)`，用户面不提供 |

`?count=true` 单列的理由：`COUNT(*)` 在 `db-f1-micro`（ADR 0005：0.6 GiB RAM、25 连接）上是实打实的开销，
不能让每次翻页都付。后台需要「共 N 条」，用户面不需要。

### 2.5 时间格式

**ISO 8601，始终 UTC，始终带 `Z`**：`2026-08-16T12:34:56Z`。字段名一律以 `_at` 结尾。
入参若带偏移量（`+08:00`）服务端接受并规范化为 UTC。

**三处例外必须是 Unix 秒整数**，因为它们是外部生态的硬接口：

| 例外 | 形态 | 依据 |
|---|---|---|
| UniProxy 面的所有时间字段 | Unix 秒 `int64` | Xboard `v2_user.expired_at` 是 bigint 时间戳 |
| `subscription-userinfo` 响应头的 `expire=` | Unix 秒 | Clash 系客户端的事实标准 |
| 订阅内容体里的任何时间 | Unix 秒 | 同上 |

### 2.6 数值单位

| 类别 | 表示 | 字段名 | 理由 |
|---|---|---|---|
| 人民币金额 | 整数**分** | `*_amount`（另有 `currency: "CNY"`） | 照抄 Xboard `v2_order.total_amount`；**不抄** 2025-01 把套餐价改成「元」浮点的那次倒退 |
| USDT 金额 | 整数，单位 **1e-6 USDT** | `*_amount_usdt6` | TRC20 USDT 是 6 位小数；金额唯一性匹配的 `+0.0001` 递增正好是 **+100** |
| 流量 | 整数**字节** | `*_bytes` | int64 上限 9.2 EB，永远够 |
| 流量（UniProxy 面） | 整数字节 | `u` / `d` / `transfer_enable` | 照抄，不改名 |
| 倍率 | 定点整数，基数 **1e9** | `multiplier_e9` | 第一阶段不引入倍率，但字段先留；抄 Remnawave `consumption_multiplier BIGINT` |

**任何金额、流量、倍率都不得出现浮点。** 唯一允许浮点的是 `POST /status` 的 `cpu`（照抄 Xboard 的校验规则）。

### 2.7 通用请求头

| 头 | 方向 | 说明 |
|---|---|---|
| `X-Request-Id` | 请求可选 / 响应必有 | 调用方可传入自己的 ID；未传则服务端生成 ULID |
| `Idempotency-Key` | 请求 | 仅 §9 列出的端点需要 |
| `Authorization` | 请求 | `Bearer <jwt>`（用户面）/ `Bearer <节点密钥>`（节点面） |
| `X-TOTP-Code` | 请求 | 管理面 step-up 操作 |
| `Retry-After` | 响应 | 429 与 503 **必带**，单位秒 |

---

## 3 · 节点面：`/api/v1/server/UniProxy/*`（冻结契约）

### 3.1 端点总览

| 方法 | 路径 | 频率 | ETag | scope | 第一阶段 |
|---|---|---|---|---|---|
| GET | `/api/v1/server/UniProxy/config` | 60 s / 节点 | **必须** | `node:config:read` | ✅ |
| GET | `/api/v1/server/UniProxy/user` | 60 s / 节点 | **必须** | `node:users:read` | ✅ |
| POST | `/api/v1/server/UniProxy/push` | 60 s / 节点 | — | `node:traffic:write` | ✅ |
| POST | `/api/v1/server/UniProxy/alive` | 60 s / 节点 | — | `node:alive:write` | ✅ |
| GET | `/api/v1/server/UniProxy/alivelist` | 60 s / 节点 | 可选 | `node:alive:read` | ✅ |
| POST | `/api/v1/server/UniProxy/status` | 60 s / 节点 | — | `node:status:write` | ⚠️ 只写快照 |

后两个是 Xboard 的扩展（v2board 1.7.4 没有），但 v2node 会调
（panels-and-market §2.3：v2node 消费 `config` / `user` / `push` / `alive` / `alivelist`）。
`alivelist` 是**多节点共享设备数限制的唯一机制** —— 节点先拉全网计数再本地决策 —— 所以 P1 必须做。

请求量算术（依据 ADR 0006 §3.3）：每节点每 60 秒 4–5 次请求。
2 节点 = 每 7.5 秒一个请求；10 节点 = **1,728,000 请求/月**，占 Cloud Run request-based
免费额度 200 万/月的 86%；20 节点超出 173%。**ETag 不是优化，是让这笔账算得平的前提。**

**明确不实现**：`/api/v1/server/ShadowsocksTidalab/*`、`/api/v1/server/TrojanTidalab/*`
（Xboard 保留的历史端点）、`/api/v2/server/*`（Xboard 的合并上报 + WebSocket 协议）。
后者的 WebSocket 与「不做 WS，60 秒轮询」的既定裁决直接冲突。

### 3.2 鉴权：每节点独立密钥 + scope 白名单

#### 3.2.1 密钥形态

```
bpn_<key_id>_<secret>
例：bpn_7f3a2c_kQ2mXv9pL4nR8tZ1wY6bC3dF5gH7jK0sA2eU4iO6
```

| 部分 | 说明 |
|---|---|
| `bpn_` | 固定前缀，便于在日志/代码扫描里正则识别泄漏 |
| `key_id` | `node_keys` 的短标识（base32，6 字符）。**存在的唯一目的是让服务端一次主键查询定位到行**，不必对全表哈希做扫描 |
| `secret` | 32 字节 CSPRNG，base64url 无填充 |

DB 侧：

```sql
node_keys(
  id          bigserial primary key,
  node_id     integer     not null references servers(id),
  key_id      char(6)     not null unique,
  secret_hash bytea       not null,          -- sha256(secret)
  scopes      text[]      not null,
  name        text        not null,          -- 「2026-08 轮换」这类人写的备注
  created_at  timestamptz not null default now(),
  last_used_at timestamptz,
  expires_at  timestamptz,
  revoked_at  timestamptz
)
```

**`secret_hash` 用 SHA-256 而不是 argon2id/bcrypt —— 这是有意的取舍。**
慢哈希的价值是抵抗对**低熵人类密码**的离线爆破；这里是 256 位 CSPRNG 高熵密钥，
离线爆破在物理上不成立，而每 60 秒 × 节点数 × 5 端点都要付一次 argon2 的成本是纯损失。
比对用 `subtle.ConstantTimeCompare`。
**这条取舍在密钥改为人工可设时立刻失效** —— 所以 API 不提供「自定义密钥」入口。

一个节点可同时持有**多把有效密钥**。这是 D5（密钥两步轮换）能成立的前提：
先签发新密钥 → 观察到该 `key_id` 出现在 `last_used_at` → 再吊销旧的。

#### 3.2.2 scope 粒度与硬编码白名单

| scope | 授权的路由（**精确匹配，非前缀**） |
|---|---|
| `node:config:read` | `GET /api/v1/server/UniProxy/config` |
| `node:users:read` | `GET /api/v1/server/UniProxy/user` |
| `node:traffic:write` | `POST /api/v1/server/UniProxy/push` |
| `node:alive:write` | `POST /api/v1/server/UniProxy/alive` |
| `node:alive:read` | `GET /api/v1/server/UniProxy/alivelist` |
| `node:status:write` | `POST /api/v1/server/UniProxy/status` |

默认签发的 scope 集合 = 前五个。`node:status:write` 按需。

**白名单是 Go 里的常量 map（`method + 精确路径 → 所需 scope`），不从 DB 读**
（ADR 0006 §10.3 第 2 条）。从 DB 读出字符串做前缀比对的 scope 可以被路径构造绕过 ——
`node:users` 前缀匹配会意外放行任何以 `/user` 开头的未来路由。

#### 3.2.3 `node_id` 的权威来源

这是相对 Xboard / SSPanel 最重要的一处加固。

| | Xboard 现状 | babel.plus |
|---|---|---|
| 节点身份 | 请求参数 `?node_id=` | **`node_keys.node_id`，从密钥推导** |
| token 与身份的绑定 | **不绑定**（全局共享 token） | 一把密钥只对应一个节点 |
| 后果 | 持 token 者可枚举 `node_id` 拉取全量用户 UUID | 拿到一把密钥只能看到那一个节点该看的用户 |

实现规则：

1. 请求里的 `node_id` 参数**只用于日志与一致性检查，不用于查询**。
2. 若请求带了 `node_id` 且与密钥绑定的不一致 → **403 `NODE_ID_MISMATCH` + 写告警**（不是静默忽略）。
   静默忽略会让「配置写错了哪台机器」这类事故无法被发现。
3. `node_type` 参数（`v2node` / `vless` / `hysteria` …）同样只用于日志。
   节点类型的权威来源是 `servers.type`。

#### 3.2.4 🔴 与 v2node 的落地风险：它可能只会发 query string

**这是本文最大的未验证风险，优先级高于其他一切实现工作。**

已知事实：XrayR 用 resty 的 `SetQueryParams` 把 `token` / `node_id` / `node_type`
**全局挂载在 query string 上**（panels-and-market §2.1）。v2node 是 XrayR/V2bX 的同源继任者，
**大概率沿用同一形态** —— 即它很可能根本不会发 `Authorization` 头。

若如此，「Bearer 头」与「v2node 开箱即用」两件事互相冲突。三条路径：

| 路径 | 代价 | 评价 |
|---|---|---|
| **A. 给 v2node 打补丁** | MPL-2.0 是**文件级** copyleft，改动的文件必须以 MPL 开源；改动量估计约 20 行（HTTP client 构造处）。代价是从此维护一个 fork 并跟随上游 rebase | ✅ **首选** |
| **B. 接受 per-node token 走 query 作为过渡** | 加固三条里保住两条（每节点独立 + 可吊销 + scope），丢掉一条（凭据进 access log 与 Referer） | ⚠️ 过渡 |
| C. 换节点端软件 | 推翻 ADR 0007 与 system-design §3.2 | ❌ |

**裁决：A 为目标态，B 为当前唯一可行形态。**

> 🔴 **2026-08-17 修正**：原文写「B 为**有期限的**过渡态」，隐含「等实测确认后可以关掉」。
> 已读 v2node 源码核实：它用 `client.SetQueryParams({node_type, node_id, token})` 鉴权，
> **全仓没有任何一处为鉴权设置 Authorization 头，也没有配置开关**。
> 所以退出条件不成立 —— 关掉 query 需要改上游。证据见 [v2node-contract-20260817 §3](../evidence/v2node-contract-20260817/)。
> 另：`node_type` 的值是字面量 `"v2node"`，不是协议名，参数校验不能按协议名做枚举。

B 的三条硬约束（仍然全部成立）：

1. query 形态的 token **也必须是每节点独立密钥**，绝不接受全局共享 token。
2. 每次经 query 认证都写一条 `WARN` 结构化日志（带 `key_id`），使其在监控上可见、可计数。
3. **在 [ADR 0007](../05-adr/0007-node-migration.md) 阶段 5（全量切换）之前必须关闭 query 形态。**
   若届时未关闭，等于永久接受，应重开 ADR 显式承认而不是让它默认留存。

> 🔴 **动工前必须验证的三件事，起一个真实 v2node 容器就能全测**（ADR 0006 §12 的契约测试）：
> 1. v2node 是否发送 `If-None-Match`？**不发则整套 ETag 一行都不生效。**（ADR 0006 §11.4 已记为最高优先级）
> 2. v2node 能否配置 `Authorization` 头？
> 3. **v2node 收到 401/403 时怎么做？** 若它会清空本地用户列表，一次密钥配置失误 = 全体用户瞬时掉线，
>    且现象与 IP 级封锁高度相似（ADR 0007 已记录这类误导性故障形态）。
>
> ✅ **三条全部已解决，而且都是靠读源码，一个容器都没起。**
> 第 1、2 条见 [evidence/v2node-contract-20260817](../evidence/v2node-contract-20260817/)（会发 `If-None-Match`；
> 不支持 `Authorization`）。第 3 条见 [evidence/v2node-401-behavior-20260821](../evidence/v2node-401-behavior-20260821/)：
> **不会清空** —— 但**重启会失败且不自愈**，运行中则是**静默失效**（配置停更，节点侧只有一行 `log.Error`）。
> 🔴 **本文档因此多一条硬约束：401/403 的响应体不能含 `"users"` 键** ——
> v2node 的保护依赖它的 JSON token 扫描找不到该键而报错；一旦错误信封里出现这个键，
> 保护就会失效。现行 `{"error":{...}}` 信封是安全的。

### 3.3 `GET /config` — 节点自身配置

**请求**

```http
GET /api/v1/server/UniProxy/config?node_id=3&node_type=v2node HTTP/1.1
Host: <api 域名>
Authorization: Bearer bpn_7f3a2c_kQ2mXv9pL4nR8tZ1wY6bC3dF5gH7jK0sA2eU4iO6
If-None-Match: W/"3-c17-u482"
```

**响应 200 —— VLESS + XTLS-Vision + REALITY 节点**

```json
{
  "protocol": "vless",
  "listen_ip": "0.0.0.0",
  "server_port": 443,
  "network": "tcp",
  "networkSettings": null,
  "tls": 2,
  "flow": "xtls-rprx-vision",
  "tls_settings": {
    "server_name": "www.cloudflare.com",
    "private_key": "wKq3…（REALITY x25519 私钥）",
    "short_id": "6ba85179e30d4fc2",
    "dest": "www.cloudflare.com:443",
    "xver": "0"
  },
  "base_config": {
    "push_interval": 60,
    "pull_interval": 60,
    "device_online_min_traffic": 1000,
    "node_report_min_traffic": 1000
  }
}
```

**响应 200 —— Hysteria2 节点**

```json
{
  "protocol": "hysteria",
  "version": 2,
  "listen_ip": "0.0.0.0",
  "server_port": 443,
  "up_mbps": 0,
  "down_mbps": 0,
  "obfs": "salamander",
  "obfs-password": "Jc7…",
  "server_name": "hk1.example.invalid",
  "base_config": {
    "push_interval": 60,
    "pull_interval": 60
  }
}
```

**响应 304**

```http
HTTP/1.1 304 Not Modified
ETag: W/"3-c17-u482"
Cache-Control: no-cache
```

字段来源与我们的约束：

| 字段 | 来源 | 我们的约束 |
|---|---|---|
| `protocol` `listen_ip` `server_port` `network` `tls` `flow` `tls_settings` | 照抄 Xboard `ServerService::buildNodeConfig()` | 不改名、不改类型 |
| `base_config.push_interval` / `pull_interval` | 照抄，Xboard 默认均 60 | 固定 60。这就是 §3.1 请求量算术的输入 |
| `base_config.device_online_min_traffic` / `node_report_min_traffic` | v2node 的 `BaseConfig` 多读的两个字段 | 值 **需实测** 才能定 —— 单位与语义在调研中未确认 |
| **`up_mbps` / `down_mbps` = 0** | Hysteria 分支 | 🔴 **这两个正是 Brutal 拥塞控制的参数。已裁定「Hysteria2 用 BBR 不用 Brutal」，所以必须下发 0（= 不声明带宽 = 不启用 Brutal）。v2node 对 0 的处理 需实测** |
| `routes` | v2board 1.7.4 在 `route_id` 非空时附加 | **第一阶段不下发**（不做节点侧分流规则） |

**必须显式记录的缺口**：已裁定「证书必须钉 Let's Encrypt，禁用 Google Trust Services」，
但 Xboard 的 hysteria 分支**是否有证书相关字段，调研中没有记录** —— 标 **需核实**。
若契约里没有位置，证书只能在装机脚本层面固定（certbot + LE），不经 `/config` 下发。
这是 §14 的一条待办。

**新增字段的规则**：我们自己需要的字段一律加在顶层新键，且**必须做到「v2node 忽略它也能正常工作」**。
理由是 Go 的 `encoding/json` 默认忽略未知字段（除非显式 `DisallowUnknownFields`，v2node 几乎不可能这么写，
但仍需在契约测试里断言）。任何「节点必须理解才能工作」的新字段 = 破坏性变更，见 §11。

### 3.4 `GET /user` — 该节点的用户列表

**请求**：同 §3.3，`If-None-Match: W/"3-c17-u482"`

**响应 200**

```json
{
  "users": [
    { "id": 1,  "uuid": "8f3a2c1d-4b5e-4c7a-9d21-6e0f3a7b1c92", "speed_limit": 0, "device_limit": 2 },
    { "id": 7,  "uuid": "b21e9f04-7c33-4a18-8e5d-1f2a6b9c4d70", "speed_limit": 0, "device_limit": 5 },
    { "id": 19, "uuid": "c9d0117a-2e64-4f0b-b3a7-5c81d4e2f6a3", "speed_limit": 0, "device_limit": 10 }
  ]
}
```

| 字段 | 语义 | 我们的值 |
|---|---|---|
| `id` | 用户主键，**`/push` 与 `/alive` 的 key 就是它** | — |
| `uuid` | VLESS 的 UUID；SS-2022 侧 XrayR 系直接把它当 password 用 | 每用户一个，与订阅 token 无关 |
| `speed_limit` | Mbps，0 = 不限 | **第一阶段全部 0** —— 定价用设备数做杠杆，不用限速（pricing-and-plans §3.1） |
| `device_limit` | 设备数上限 | 2 / 5 / 10 三档 |

**可用用户的判定条件**（一条 SQL，照抄 Xboard `getAvailableUsers`）：

```
u + d < transfer_enable
AND (expired_at IS NULL OR expired_at > now())
AND NOT banned
AND group_id ∈ servers.group_ids
```

`expired_at IS NULL` 天然支撑「不限时套餐」。

**响应体量级**：200 用户 × 约 85 字节 ≈ 17 KB。
若无 ETag：8 节点 × 1440 次/天 × 17 KB ≈ **196 MB/天出网 + 11,520 次全量查询/天**。
有 ETag 后两者都趋近 0 —— 这就是 ETag 被称为「性能命门」的具体数字。

### 3.5 `POST /push` — 流量上报

**请求**

```http
POST /api/v1/server/UniProxy/push?node_id=3&node_type=v2node HTTP/1.1
Authorization: Bearer bpn_7f3a2c_…
Content-Type: application/json

{ "1": [10485760, 52428800], "7": [1024, 8192], "19": [0, 314572800] }
```

| 约定 | 值 |
|---|---|
| key | 用户 id 的**字符串**（JSON 对象键必然是字符串），对应 `/user` 返回的 `id` |
| value | 长度恰为 2 的非负整数数组 `[upload, download]` |
| 单位 | **字节** |
| 语义 | **增量**（自节点上次成功上报以来），不是累计值 |
| 倍率 | **不做任何折算，原样上报原始字节**。倍率在面板侧结算（第一阶段无倍率） |
| 容错 | 非「长度 2 的数值数组」的条目**静默丢弃，不让整批失败**（照抄 Xboard `processTraffic()`）。丢弃计数进 metric |

**响应 200**

```json
{ "data": true }
```

> ⚠️ Xboard 的 `/push` 响应体形状在调研中未记录，v2node 是否解析它 **需实测**。
> 若 v2node 只看状态码，本字段无意义但无害；若它解析，必须与 Xboard 逐字节一致。

**响应时间预算 < 50 ms**，因为请求路径上只做两件事：

1. `INSERT INTO traffic_batch(batch_id, node_id, payload, received_at)` —— 一次插入
2. `enqueue Cloud Tasks → /internal/tasks/traffic-batch {batch_id}`

**累加、聚合、阈值判定全部在队列侧做**（§8.2）。这不是性能洁癖 ——
`/push` 越慢，v2node 超时重试的概率越高，而节点侧重试是**无法幂等**的（§9.2）。

### 3.6 `POST /alive` — 在线设备上报

**请求**

```json
{ "1": ["203.0.113.7", "198.51.100.42"], "7": ["203.0.113.99"] }
```

写入 ADR 0005 §8 裁定的 `UNLOGGED` 表：

```sql
INSERT INTO user_alive (user_id, node_id, device_ip, seen_at)
VALUES ($1, $2, $3, now())
ON CONFLICT (user_id, node_id, device_ip) DO UPDATE SET seen_at = now();
```

量级：200 用户 × 3 设备 = 600 行，每 60 秒全量 upsert = **10 行/秒**，可忽略。
清理由 Cloud Scheduler 每 5 分钟 `DELETE … WHERE seen_at < now() - interval '5 minutes'`（§7）。

**响应 200**：`{ "data": true }`

> ⚠️ **口径必须原样承认，不要粉饰**：这是**按 IP** 计，不是按设备计。
> 同一台手机在 Wi-Fi 与蜂窝之间切换会占两个名额 —— 在设备数 = 2 的档位上，
> 一人一机一电脑就已经超限。契约层面我们只能照抄（v2node 只上报 IP），
> 因此 page-inventory §3.2.3 要求**把口径写在页面上**，并提供用户自助「踢下线」。
> ⚠️ Xboard 的 `alivelist` 究竟是按 IP 还是按连接计数，**待核实**。

### 3.7 `GET /alivelist` 与 `POST /status`

**`GET /alivelist`** —— 多节点共享设备数限制的机制。响应只含 `device_limit > 0` 的用户：

```json
{ "alive": { "1": 2, "7": 5, "19": 3 } }
```

节点先拉全网计数，再本地决策是否放行新连接。
⚠️ **拒新还是踢旧，是 v2node 的本地行为，我们无法通过契约控制**，且 user-journey §12.2
要求的是「拒新不踢旧」—— **v2node 的实际行为 待核实**。这直接决定「家里能用、公司连不上」
这个最难自诊断的现象的具体形态。

**`POST /status`** —— 节点负载。请求体与 Xboard 的校验规则一致：

```json
{
  "cpu": 12.3,
  "mem":  { "total": 2074152960, "used": 812345344 },
  "swap": { "total": 0, "used": 0 },
  "disk": { "total": 10434699264, "used": 3221225472 }
}
```

校验：`cpu` 为 `numeric` 且 `0 ≤ cpu ≤ 100`；其余六个为 `integer` 且 `≥ 0`。

**裁决：只写快照，不建历史表。** 落 `servers.load_status jsonb` + `servers.last_status_at`，
一节点一行，被覆盖。理由：8 节点 × 1440 次/天 = 11,520 行/天的负载历史对本项目规模没有任何决策价值，
而 ADR 0005 的 10 GB SSD 是要付钱的。需要趋势时接 Cloud Monitoring，不自建时序表。

### 3.8 ETag：版本号驱动，不是哈希响应体

照 ADR 0006 §11 的裁决实现，此处补齐 §11 没写的两条。

**ETag 形状**：`W/"<node_id>-c<config_rev>-u<user_rev>"`，例 `W/"3-c17-u482"`。
`/config` 只参与 `config_rev`，`/user` 只参与 `user_rev` —— 两个端点用**不同的 ETag**，
否则改配置会让用户列表也失效，反之亦然。

- `GET /config` → `W/"3-c17"`
- `GET /user` → `W/"3-u482"`

**判定路径**（一次主键查询，`< 1 ms`）：

```sql
SELECT config_rev, user_rev FROM servers WHERE id = $1;
```

> **命名冲突登记**：ADR 0006 §11.2 写的是「一张 `node_rev` 表，主键 `node_id`」，
> 但同条括注允许「或等价的列」；ADR 0005 §8 写的是「在 `servers` 上放
> `config_version` / `user_list_version` 两列」。
> **本文取 `servers.config_rev` / `servers.user_rev`**（0005 的存储形态 + 0006 的列名），
> 理由是不值得为两个 bigint 多建一张表多做一次 join。这处命名需在 `data-model.md` 落定。

**bump 规则** —— ADR 0006 只写了两条，这里必须补到四条，否则 ETag 会静默出错：

| # | 事件 | 动作 | 依据 |
|---|---|---|---|
| 1 | 改变节点可见用户集合或密钥的**写操作**：开通 / 封禁 / 解封 / 换套餐 / 改分组 / 换 uuid / 改 device_limit | bump `user_rev` | ADR 0006 §11.2 |
| 2 | 流量累加 | **不 bump** | ADR 0006 §11.2 —— 否则每 60 秒失效，ETag 归零 |
| 3 | **流量累加导致 `u+d` 跨过 `transfer_enable` 阈值** | **必须 bump** | 🔴 **本文新增。** 不补这一条，配额耗尽的用户永远不会从节点列表消失 —— 免费无限上网 |
| 4 | **`expired_at` 到点** | **必须由定时任务 bump** | 🔴 **本文新增。** 到期是时间驱动的，**没有任何写操作会触发它**。这是 ETag 设计里最容易漏的一条 |

第 3 条的实现：累加语句用 `RETURNING` 判断是否跨阈值，只在跨越的那一次 bump。

```sql
UPDATE user_traffic SET u = u + $2, d = d + $3
WHERE user_id = $1
RETURNING (u + d >= (SELECT transfer_enable FROM users WHERE id = $1)) AS exhausted_now;
```

第 4 条的实现见 §7 的 `/internal/tasks/expire-check`（每 5 分钟）。
**代价：到期后最长 5 分钟 + 60 秒轮询 = 约 6 分钟仍可上网。** 量化在 §13。

**HTTP 层细节**（ADR 0006 §11.3）：`If-None-Match` 用弱比较（RFC 9110 §8.8.3.2）；
304 必须回带 `ETag` 与 `Cache-Control: no-cache`；Go 侧手写比对，
**不用 `http.ServeContent`**（它为文件设计，会引入 `Last-Modified` 与 Range 语义）。

### 3.9 节点面错误码与降级语义

| 状态 | 何时 | **节点必须做什么** |
|---|---|---|
| 200 | 正常 | 应用新配置 |
| 304 | ETag 命中 | 复用本地配置，不做任何 reload |
| 401 `NODE_KEY_INVALID` | 密钥无效 / 已吊销 / 已过期 | **继续用最后一次成功的配置转发**，指数退避重试 |
| 403 `NODE_SCOPE_DENIED` / `NODE_ID_MISMATCH` | scope 不足 / 身份不符 | 同上 + 面板侧写告警 |
| 429 `QUOTA_RATE_LIMITED` | 超限 | 按 `Retry-After` 退避 |
| 5xx | 面板故障 | 同 401 —— **绝不能停止转发** |

> **`system-design.md` §5.3 是硬约束：控制面故障绝不能升级为数据面故障。**
> 节点在拉取失败时使用最后一次成功的配置继续服务，并本地缓冲流量数据待恢复后补报。
>
> 🔴 但这一条**我们无法通过契约强制** —— 它取决于 v2node 的实现。
> 「v2node 收到 401 是否清空用户列表」是 §3.2.4 列出的三个必测项之一，
> 也是整份契约里唯一一条「实现方不是我们、后果却由我们承担」的条款。

---

## 4 · 订阅下发：`/api/v1/client/subscribe`

这是与客户端生态的**硬接口，不能自创格式**。本节写得比其他端点细，是因为它同时承担
三件互相纠缠的事：格式协商、用量回显、以及「订阅 URL 本身就是通知通道」。

### 4.1 两条路由

| 路由 | 用途 |
|---|---|
| `GET /api/v1/client/subscribe?token={token}` | 与 Xboard 同形，方便从竞品/旧配置迁移 |
| `GET /s/{token}` | 短路径。**默认对外发这一条** —— 短、无 query、不易被聊天软件截断 |

两条走完全相同的 handler 与鉴权链。

### 4.2 token 校验逻辑

```
1. 提取 token（query ?token= 或路径段）。长度/字符集不合法 → 直接 404，不查库
2. h = sha256(token)
3. SELECT id, user_id, issued_at, revoked_at, name
     FROM sub_tokens WHERE token_hash = h        ← 存哈希不存明文
4. 未找到                      → 404
5. revoked_at IS NOT NULL      → 404             ← 单条吊销
6. issued_at < users.sub_revoked_at              → 404   ← 一键全撤（Marzban 语义）
7. 同步写审计表 subscribe_log(user_id, sub_token_id, request_ip, user_agent, request_at)
8. 判定用户可用性：
     banned / 到期 / 配额耗尽 → 200 + 空节点列表 + 伪节点（§4.6）
     可用                     → 200 + 正常节点列表
9. 按 UA 选择格式并渲染（§4.3）
10. 写 subscription-userinfo 与其余响应头（§4.4）
```

四条设计约束：

1. **失败一律 404，不是 403。** 403 会告诉攻击者「这个 token 存在但你不能用」。
   ADR 0006 §10.2 已把这一条写进鉴权链规格。
2. **token 在 DB 里存哈希不存明文。** 这是相对 Xboard（`v2_user.token` char(32) 明文）的加固之一。
   代价：面板要给用户看订阅链接，所以**明文只在签发那一刻返回一次**，之后面板显示的是
   由服务端持有的加密副本（用 Secret Manager 里的 KEK 加密存储的第二列）还是要求重新签发 ——
   **这一条尚未裁决**，见 §14。
3. **审计同步写，不异步。** ADR 0006 §10.1 的图标注为「异步写审计表」，本文修正为同步。
   理由是量级：200 用户 × 每天数次拉取 ≈ 10³ 行/天，一次 `INSERT` < 1 ms，
   而异步引入的是「审计缺失且无人知道」这类最坏的失败模式。
   **这是实现层面的修正，不推翻 ADR 0006 的任何裁决。**
4. **`banned` 不返回 404。** 被封禁的用户看到「所有节点消失」会开工单；
   看到伪节点写着原因会去申诉。这与 §1 裁决 8 一致。

### 4.3 按 `User-Agent` 分发格式

匹配**不区分大小写**，**按表内顺序**取第一个命中：

| # | UA 子串 | 格式 | `Content-Type` |
|---|---|---|---|
| 1 | `clash-verge` / `clash.meta` / `mihomo` / `clash` | **Clash/mihomo YAML** | `text/yaml; charset=utf-8` |
| 2 | `sing-box` / `SFI` / `SFA` / `SFM` / `SFT` | **sing-box JSON** | `application/json; charset=utf-8` |
| 3 | `karing` | sing-box JSON | 同上 |
| 4 | `hiddify` | sing-box JSON | 同上 |
| 5 | `v2rayn` / `v2rayng` / `shadowrocket` | **base64 分享链接** | `text/plain; charset=utf-8` |
| 6 | *（未匹配）* | **base64 分享链接** | `text/plain; charset=utf-8` |

强制覆盖：`?flag=clash|singbox|base64`（照抄 Xboard 的 `flag` 参数语义）。

> ⚠️ **UA 子串表 需实测**：各客户端的实际 UA 字符串必须逐个抓取确认，不能靠推断。
> Clash Verge Rev 与 mihomo 内核的 UA 是否一致、Karing 发什么 UA，都未验证。
> 这张表错一行，对应客户端的用户就会拿到 base64 而不是 YAML —— 现象是「导入后没有分组，
> 只有一堆裸节点」，属于 user-journey §L2「导入失败」层。

**明确不做的格式**：Surge / Surfboard / QuantumultX / Loon / Stash / SSR。
理由是 tutorials-spec 只推现役开源客户端；每多一种格式就多一条 golden file 与人工验证负担。
新增格式是产品决策不是工程决策。

### 4.4 响应头（`subscription-userinfo` 是重点）

```http
HTTP/1.1 200 OK
content-type: text/yaml; charset=utf-8
subscription-userinfo: upload=1048576; download=10485760; total=107374182400; expire=1788240000
profile-update-interval: 24
profile-web-page-url: https://<web 域名>/subscribe
content-disposition: attachment; filename*=UTF-8''babel.plus
cache-control: no-store
x-request-id: 01K2VQ7C9M0000000000000000
```

`subscription-userinfo` 的确切格式（照抄 Xboard `app/Protocols/General.php`）：

```
subscription-userinfo: upload={u}; download={d}; total={transfer_enable}; expire={expired_at}
```

| 项 | 规则 |
|---|---|
| 分隔符 | `; `（**分号 + 一个空格**） |
| 值类型 | 全部十进制整数，**无引号、无单位后缀** |
| `upload` / `download` / `total` | **字节** |
| `expire` | **Unix 秒**（不是 ISO 8601 —— §2.5 的例外之一） |
| 头名大小写 | HTTP 头不区分大小写；照抄 Xboard 用全小写 |

**不限时套餐（`expired_at IS NULL`）时 `expire` 写什么？**
Xboard 的行为是输出空值（`expire=`），**各客户端对空值的渲染 待核实** ——
已知风险是部分客户端会把空值当 0 处理并显示「1970-01-01 已过期」。

> **提案（未验证）**：不限时套餐输出一个远期时间戳 `4102444800`（2100-01-01T00:00:00Z）。
> 这是把「客户端可能渲染错」的风险换成「显示 2100 年到期」的确定行为。
> **必须实测后再定**，本条是提案不是裁决。

**不设 `ETag`。** 订阅内容内嵌当前用量数字，每次都在变；更要紧的是 ETag 会让客户端拿到
304 后继续显示旧的流量条，而流量条恰恰是用户判断「我还剩多少」的唯一入口。

### 4.5 三种格式的响应体

#### Clash / mihomo YAML

```yaml
port: 7890
socks-port: 7891
allow-lan: false
mode: rule
log-level: info
external-controller: 127.0.0.1:9090

proxies:
  - name: "HK-1 · REALITY"
    type: vless
    server: 203.0.113.10
    port: 443
    uuid: 8f3a2c1d-4b5e-4c7a-9d21-6e0f3a7b1c92
    network: tcp
    tls: true
    udp: true
    flow: xtls-rprx-vision
    servername: www.cloudflare.com
    client-fingerprint: chrome
    reality-opts:
      public-key: 7Xk1…
      short-id: "6ba85179e30d4fc2"
    smux:
      enabled: true
      protocol: h2mux
      max-connections: 4
      min-streams: 4

  - name: "HK-1 · HY2 加速"
    type: hysteria2
    server: 203.0.113.10
    port: 443
    password: Jc7…
    obfs: salamander
    obfs-password: Jc7…
    sni: hk1.example.invalid
    udp: true

proxy-groups:
  - name: "默认"
    type: fallback
    proxies: ["HK-1 · REALITY", "HK-1 · HY2 加速"]
    url: http://www.gstatic.com/generate_204
    interval: 300
  - name: "加速"
    type: select
    proxies: ["HK-1 · HY2 加速", "HK-1 · REALITY"]

rules:
  - IP-CIDR,127.0.0.0/8,DIRECT,no-resolve
  - IP-CIDR,10.0.0.0/8,DIRECT,no-resolve
  - IP-CIDR,172.16.0.0/12,DIRECT,no-resolve
  - IP-CIDR,192.168.0.0/16,DIRECT,no-resolve
  - IP-CIDR,169.254.0.0/16,DIRECT,no-resolve
  - IP-CIDR6,::1/128,DIRECT,no-resolve
  - IP-CIDR6,fc00::/7,DIRECT,no-resolve
  - IP-CIDR6,fe80::/10,DIRECT,no-resolve
  - MATCH,默认
```

> 🔴 **`rules` 这一段本文档原来漏了，实现也跟着漏了（2026-08-21 补）。**
> 配置里写着 `mode: rule`，而 mihomo 在规则全不匹配时回落到 `DIRECT` ——
> **规则表为空 = 每一条连接都走直连**。用户导入后会看到节点全在、延迟测得出来、
> 订阅流量条正常，但被墙的站点一个都打不开，且他必然把这个报成「节点坏了」。
> 这是「契约漏了 → 实现照抄契约 → 一起漏」的典型，不能靠「实现照着契约写」免责。
>
> 三条约束：
> 1. **`MATCH` 必须是最后一条**，且目标必须与 `proxy-groups` 里默认组的名字**逐字一致**
>    （指向不存在的组会让 mihomo 拒绝加载整份配置）。
> 2. 私有网段规则带 `no-resolve`，避免为了判断一条 IP 规则先做一次 DNS 解析。
> 3. 🔴 **表里刻意没有 `GEOIP,CN,DIRECT`，尽管产品上想要它。**
>    第一版写了，理由是「数据库缺失时该条匹配不上，于是落到 `MATCH`，
>    降级为全局代理，失败方向是安全的」—— **实测证明这句话是错的**：
>
>    ```
>    mihomo v1.19.30 · 全新配置目录 · 断网
>      带 GEOIP,CN → can't download MMDB …… configuration file test FAILED
>      不带        → Initial configuration complete, 7ms, test is successful
>    ```
>
>    拿不到 GeoIP 数据库时**整份配置被拒绝加载**，不是「这条规则不匹配」。
>    而那个数据库是 8.6 MB、来自 `github.com/MetaCubeX/meta-rules-dat/releases` ——
>    需要下载它的人恰恰是「人在大陆、刚装完客户端、还没有任何可用代理」的那一刻。
>    缓存过一次之后离线没问题，所以风险**特指首次加载**。
>
>    两种失败方向的代价不对称：没有 GEOIP 是「国内流量也走节点，慢一些、出口贵一些，
>    但产品能用」；有 GEOIP 却下不到是「整份订阅加载不了，产品完全不能用」。
>    首次加载必须选前者。
>
>    ⚠️ tutorials-spec 排障表里「国内网站变慢/打不开 → `GEOIP,CN` 的位置问题」
>    这一条**目前对不上实现** —— 它假定了分流规则的存在。
>    要拿回国内直连（出口成本也在等它），前置是先回答
>    「首推客户端是否自带 `geoip.metadb`」（桌面版 Clash Verge Rev 很可能自带，
>    但本仓没有一手数据）。登记为 roadmap **B46**。
>
>    实测原始输出：[evidence/client-config-validation-20260822](../evidence/client-config-validation-20260822/)。

两条与既定裁决的直接对应：

1. **TCP 路径有 `smux`，UDP 路径没有** —— 「TCP 路径启用 mux 抗 TLS-in-TLS 指纹，UDP 路径不启用」。
2. **默认组是 `fallback` 不是 `url-test`** —— system-design §3.1 的实测结论：
   各健康节点延迟同在 100–250 ms 噪声带内，吞吐却差 4–5 倍，`url-test` 会**稳定选错**。
   另给一个 `select` 类型的「加速」组，对应教程里「慢的时候切到 HY2 试试」。

> ⚠️ **字段名 需实测**：mihomo 的 vless `smux` 块字段名、`reality-opts` 的键名、
> hysteria2 的 `obfs-password` 拼写，全部必须用真实 Clash Verge Rev 加载 golden file 验证。
> ADR 0006 §12 已经把这条定为不可自动化的人工步骤：**每次改订阅格式，
> 人工用 Clash Verge Rev / sing-box 各加载一次。**
>
> ✅ **2026-08-22 部分兑现（容器里跑真实客户端做配置校验）**：
> 生成器的真实产出经 **mihomo v1.19.30 `-t`** 与 **sing-box v1.13.19 `check`** 校验，
> 两侧、含伪节点路径**全部通过** ——
> 见 [evidence/client-config-validation-20260822](../evidence/client-config-validation-20260822/)。
>
> ⚠️ **这不等于「字段名验过了」。** `-t` / `check` 只做**结构与语义校验**：
> 认得的键才校验，认不得的键**默默忽略** —— 上面那三处「需实测」写错时它们照样通过。
> 真正能证明字段名对的只有「连上去跑通流量」。**人工加载那一步仍然欠着。**

#### sing-box JSON

```json
{
  "log": { "level": "warn" },
  "outbounds": [
    {
      "type": "selector",
      "tag": "默认",
      "outbounds": ["HK-1 · REALITY", "HK-1 · HY2 加速"],
      "default": "HK-1 · REALITY"
    },
    {
      "type": "vless",
      "tag": "HK-1 · REALITY",
      "server": "203.0.113.10",
      "server_port": 443,
      "uuid": "8f3a2c1d-4b5e-4c7a-9d21-6e0f3a7b1c92",
      "flow": "xtls-rprx-vision",
      "tls": {
        "enabled": true,
        "server_name": "www.cloudflare.com",
        "utls": { "enabled": true, "fingerprint": "chrome" },
        "reality": { "enabled": true, "public_key": "7Xk1…", "short_id": "6ba85179e30d4fc2" }
      },
      "multiplex": { "enabled": true, "protocol": "h2mux", "max_connections": 4, "min_streams": 4 }
    },
    {
      "type": "hysteria2",
      "tag": "HK-1 · HY2 加速",
      "server": "203.0.113.10",
      "server_port": 443,
      "password": "Jc7…",
      "obfs": { "type": "salamander", "password": "Jc7…" },
      "tls": { "enabled": true, "server_name": "hk1.example.invalid" }
    }
  ]
}
```

> 加分做法（ADR 0006 §5.1 已记）：Go 侧直接 `import` sing-box 的配置结构体反序列化我们的输出，
> 让「生成了 sing-box 不认的 JSON」在**编译期之后的第一个测试**就暴露，而不是在用户的手机上。

#### base64 分享链接

正文是若干行分享链接的整体 base64（标准 base64，带填充，无换行）。解码后：

```
vless://8f3a2c1d-4b5e-4c7a-9d21-6e0f3a7b1c92@203.0.113.10:443?encryption=none&security=reality&sni=www.cloudflare.com&fp=chrome&pbk=7Xk1…&sid=6ba85179e30d4fc2&type=tcp&flow=xtls-rprx-vision#HK-1%20%C2%B7%20REALITY
hysteria2://Jc7…@203.0.113.10:443?sni=hk1.example.invalid&obfs=salamander&obfs-password=Jc7…#HK-1%20%C2%B7%20HY2%20%E5%8A%A0%E9%80%9F
```

### 4.6 到期 / 配额耗尽 / 封禁：空列表 + 伪节点

依据 user-journey §11.2：**订阅 URL 本身就是通知通道** ——
它是「唯一在用户邮箱收不到、Telegram 连不上、主站被封时仍然能触达的通道」。

| 状态 | 节点列表 | 伪节点名 |
|---|---|---|
| 到期 | 空 | `⚠️ 订阅已到期 · 续费 <web 域名>` |
| 配额耗尽 | 空 | `⚠️ 流量已用尽 · 购买流量包 <web 域名>` |
| 封禁 | 空 | `⚠️ 账号已停用 · 请提交工单 <web 域名>` |
| 域名广播 | 正常列表 | **列表第一位保留给广播位**（user-journey：第一个节点名保留给域名广播） |

伪节点必须是一个**语法合法但连不上**的条目（例如指向 `127.0.0.1:1`），
否则部分客户端会因为「proxies 为空」而拒绝导入整份配置，用户连这句话都看不到。

> ⚠️ **待核实**：各客户端对节点名长度与特殊字符（emoji、空格、`·`、冒号）的渲染差异。
> user-journey §16 已把这一条登记为伪节点通道的前提。

---

## 5 · 用户面：`/api/v1/*`

鉴权：短期 access JWT（15 分钟）+ refresh token（30 天，一次性轮换）。
CORS 只允许 Web 域名池。失败 401。

### 5.1 端点总览

**认证与账号**

| 方法 | 路径 | 登录 | 说明 |
|---|---|---|---|
| POST | `/api/v1/auth/register` | ❌ | body `{invite_code, email, password, email_code}`，**邀请码必填** |
| POST | `/api/v1/auth/email-code` | ❌ | body `{email, scene}`；每次发码写 `email_probe`（见下） |
| POST | `/api/v1/auth/login` | ❌ | 401 统一 `AUTH_INVALID_CREDENTIALS` |
| POST | `/api/v1/auth/refresh` | ❌ | refresh 轮换；旧 refresh 立即失效 |
| POST | `/api/v1/auth/logout` | ✅ | 吊销当前 refresh |
| POST | `/api/v1/auth/password/forgot` | ❌ | **无论邮箱是否存在都返回 204** |
| POST | `/api/v1/auth/password/reset` | ❌ | body `{token, password}` |
| GET | `/api/v1/invite/verify?code=` | ❌ | 区分「无效」与「已用尽」两种 |
| GET | `/api/v1/user/me` | ✅ | 用户信息 + 订阅摘要 |
| PUT | `/api/v1/user/password` | ✅ | 需旧密码 |
| GET/PUT | `/api/v1/user/notification-prefs` | ✅ | 见 §5.3 |
| POST | `/api/v1/user/2fa/enroll` `/verify`，DELETE `/api/v1/user/2fa` | ✅ | 用户侧 TOTP（可选，P3） |

> **`POST /auth/email-code` 有一个被重新定位的职责**：user-journey §1 把注册验证码定为
> 「失联生命线的免费持续压测」。依 ADR 0002，邮件是唯一失联恢复通道，
> 所以**收不到验证码的用户就是封锁当天必然失联的用户**。
> 因此每次发码必须写一条 `email_probe(recipient_domain, esp, bounce_code, sent_at, delivered_at)`，
> 按收件域名分组统计，直接充当 ADR 0002 §7 要求的送达率实测数据源。
> 这不是附带功能，是这个端点存在的第二个理由。

**订阅、设备、用量**

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/api/v1/user/subscription` | 订阅链接（三种格式的 URL）+ 用量 + 到期 + 设备计数 |
| GET | `/api/v1/user/subscription/tokens` | 多 token 列表（可命名、可单独吊销） |
| POST | `/api/v1/user/subscription/tokens` | 签发新 token，**明文只在此响应里出现一次** |
| DELETE | `/api/v1/user/subscription/tokens/{id}` | 吊销单条（该设备需重导） |
| POST | `/api/v1/user/subscription/revoke-all` | 置 `sub_revoked_at = now()`，**所有设备重导** |
| GET | `/api/v1/user/subscription/fetch-log` | 最近拉取审计（时间 / IP / UA），默认 10 条 |
| GET | `/api/v1/user/devices` | 在线设备（来自 `user_alive`） |
| DELETE | `/api/v1/user/devices/{id}` / `/api/v1/user/devices` | 踢单个 / 全部下线 |
| GET | `/api/v1/user/usage?range=30d` | 用量曲线（P2，依赖 30 天数据积累） |
| GET | `/api/v1/user/nodes` | 节点列表（P2，客户端里本来就有） |
| GET | `/api/v1/user/diagnose` | 账号侧四项自检（P2） |

`DELETE /api/v1/user/devices`（全部下线）的响应**必须携带生效延迟提示**：

```json
{
  "data": { "removed": 3, "effective_within_seconds": 60 },
  "meta": { "request_id": "01K2…" }
}
```

理由：配置下发是 60 秒轮询，「全部下线」不是即时的。
user-journey §12.2 明确要求告知用户这一点，否则用户会连点五次然后开工单。

`GET /api/v1/user/diagnose` 的响应形状（四项检查覆盖「能拉到订阅但连不上」的绝大多数成因，
而这四项用户在客户端里一个都看不到）：

```json
{
  "data": {
    "checks": [
      { "key": "account_active",  "ok": true,  "detail": null },
      { "key": "not_expired",     "ok": true,  "detail": { "expired_at": "2027-03-01T00:00:00Z" } },
      { "key": "traffic_left",    "ok": false, "detail": { "used_bytes": 107374182400, "total_bytes": 107374182400 } },
      { "key": "device_under_limit", "ok": true, "detail": { "current": 2, "limit": 5 } }
    ],
    "subscription_last_fetched_at": "2026-08-16T09:12:44Z",
    "traffic_last_reported_at": "2026-08-16T12:33:10Z",
    "data_delay_note": "流量数据延迟约 1 分钟"
  }
}
```

> **`data_delay_note` 不是装饰。** user-journey 记录了流量数字有三个天然不一致的口径
>（面板 / 客户端 `subscription-userinfo` / 邮件快照）。不写这一句，必然有用户拿客户端的数字质问面板的数字。

**套餐、订单、支付**

| 方法 | 路径 | 幂等 | 说明 |
|---|---|---|---|
| GET | `/api/v1/plans` | — | 周期套餐 + 流量包两类 |
| POST | `/api/v1/orders` | **`Idempotency-Key`** | 下单，锁定汇率 |
| GET | `/api/v1/orders` / `/api/v1/orders/{trade_no}` | — | 列表 / 详情 |
| POST | `/api/v1/orders/{trade_no}/cancel` | — | 仅 `status=0` 可取消 |
| POST | `/api/v1/orders/{trade_no}/pay` | **`Idempotency-Key`** | body `{method:"usdt_trc20"｜"balance"}`，返回收银台数据 |
| GET | `/api/v1/orders/{trade_no}/payment` | — | 收银台状态轮询（含 `underpaid` 差额） |
| POST | `/api/v1/orders/{trade_no}/recheck` | — | **「我已付款，帮我查一下」** —— 触发主动查单 |
| POST | `/api/v1/coupons/verify` | — | 优惠码校验（不核销） |
| POST | `/api/v1/payment/notify/{provider}` | **`(provider, external_id)`** | 支付回调，**公开端点**，验签 |

`POST /api/v1/orders` 请求与响应：

```json
// 请求
{ "plan_id": 3, "period": "yearly", "coupon_code": null, "use_balance": true }
```

```json
// 201 响应
{
  "data": {
    "trade_no": "20260816T7K2M9Q4",
    "type": "new",
    "status": "pending",
    "currency": "CNY",
    "total_amount": 43200,
    "discount_amount": 14400,
    "surplus_amount": 0,
    "balance_amount": 1200,
    "payable_amount": 42000,
    "rate_locked_at": "2026-08-16T12:34:56Z",
    "expires_at": "2026-08-16T13:04:56Z"
  }
}
```

`POST /api/v1/orders/{trade_no}/pay` 响应（USDT 收银台，对应 page-inventory §3.2.5 的六个必备元素）：

```json
{
  "data": {
    "trade_no": "20260816T7K2M9Q4",
    "chain": "TRC20",
    "address": "TXxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx",
    "amount_usdt6": 5842300,
    "amount_display": "5.8423",
    "cny_per_usdt_e4": 71930,
    "quote_expires_at": "2026-08-16T13:04:56Z",
    "confirmations_required": 1,
    "received_usdt6": 0,
    "state": "waiting",
    "note": "尾数 0.0023 是订单识别码，请按此金额一分不差地转账"
  }
}
```

三条与已裁定事实的对应：

1. **`amount_usdt6` 的末位是订单识别码。** pricing-and-plans §4.2 裁定用「小地址池 + 金额唯一性」
   （抄 EPUSDT：冲突则 `+0.0001` 递增重试，最多 100 次）。`+0.0001 USDT = +100 usdt6`。
   `note` 字段的存在是因为 user-journey 列出的七个卡点里，
   **第三个就是「四位小数尾数像诈骗」** —— 不解释这个尾数，用户会停在这一步。
2. **`state` 的取值必须含 `underpaid`**：`waiting` / `confirming` / `underpaid` / `paid` / `expired`。
   金额唯一性匹配决定了少付一定会发生（提币手续费从转出额扣是头号成因）。
   `underpaid` 时响应额外带 `shortfall_usdt6`，前端显示「已收到 X，还差 Y」，而不是笼统的「支付失败」。
3. **`confirmations_required` 必须可配置下发，不能硬编码在前端。**
   pricing-and-plans §7 已记「各链确认数策略需定稿：TRC20 等固化块（约 57 s）、ERC20 6 确认、BEP20 15 确认」。

**订单过期后的行为**（user-journey 的设计增量，必须写进契约）：

> 订单 `expires_at` 到点后状态转 `expired`，**但该收款地址必须继续监听 ≥ 24 小时**。
> 期间到账的资金**入账为余额，不直接开通订阅**。
> 因为余额仅可消费不可提现（product-brief §6），这个兜底在资金合规上是安全的。
> **不做这一条，用户第一次付款的钱就真的进黑洞** —— 这是最不可挽回的一类失败。

**工单、公告、邀请、钱包**

| 方法 | 路径 | 说明 |
|---|---|---|
| GET/POST | `/api/v1/tickets` | 列表 / 新建。新建 body 必带 `category` ∈ {`subscription`,`node-down`,`billing`,`account`} |
| GET | `/api/v1/tickets/{public_id}` | 会话。**响应必须来自固定带 `is_internal = false` 的视图** |
| POST | `/api/v1/tickets/{public_id}/messages` | 回复 |
| POST | `/api/v1/tickets/{public_id}/close` | 关闭 |
| GET | `/api/v1/notices?limit=3` | 公告（dashboard 取 3 条，`/notice` 取全部） |
| GET/POST | `/api/v1/user/invite/codes` | 邀请码列表 / 生成 |
| GET | `/api/v1/user/commissions` | 佣金记录（`确认中` / `已获得` 两段式） |
| POST | `/api/v1/user/commissions/transfer` | 佣金划转到余额（**不可提现**） |
| GET | `/api/v1/user/wallet` `/api/v1/user/wallet/transactions` | 余额与流水 |

`POST /api/v1/tickets` 请求体带自动采集的诊断快照：

```json
{
  "category": "node-down",
  "subject": "香港节点连不上",
  "message": "从今天下午开始…",
  "context": {
    "plan_name": "标准 · 年付",
    "expired_at": "2027-03-01T00:00:00Z",
    "used_bytes": 32212254720,
    "total_bytes": 107374182400,
    "device_count": 2,
    "device_limit": 5,
    "last_sub_fetch_at": "2026-08-16T09:12:44Z",
    "last_sub_fetch_ip": "203.0.113.7",
    "last_sub_fetch_ua": "clash-verge/2.4.2",
    "last_active_node": "HK-1"
  }
}
```

**`context` 由服务端在建单时重新采集并覆盖客户端提交的值。**
客户端传的那份只作为「客户端自己看到的状态」参考存进 `context.client_reported`。
理由：工单记录的是「报障当时的事实」，而客户端的自述可能已经过时或被篡改。

> ⚠️ `ticket_messages.is_internal` 是全系统最容易出安全事故的一列。
> **用户侧查询必须走固定带 `is_internal = false` 的视图或方法，不接受调用方传参决定。**
> 在 OpenAPI spec 层面：用户面的 ticket message schema **不包含** `is_internal` 字段，
> 让「泄漏内部备注」在生成的类型上就不可表达。

### 5.2 一处必须在 API 层强制的产品约束

`GET/PUT /api/v1/user/notification-prefs`：

```json
{
  "data": {
    "expire_remind": true,
    "traffic_remind": true,
    "service_broadcast": { "value": true, "locked": true,
      "reason": "服务不可用通知（新域名广播）不受开关控制" }
  }
}
```

**`service_broadcast` 是只读的 `true`。** `PUT` 若试图改它 → 422 `VALIDATION_FAILED`。
依据是 user-journey 的裁决：到期提醒 / 流量提醒两个开关抄竞品可以，
但**生命线不能被用户关掉**。把它做成一个「前端隐藏的开关」是不够的 ——
它必须在 API 层就不可写，否则总有一天会有人把它做成可写的。

---

## 6 · 管理面：`/api/v1/admin/*`

鉴权三道闸：**独立主域名 + IAP/IP 白名单 + 强制 TOTP**。失败 403。
IAP 与 TOTP 是两套独立凭据，任一泄漏不足以进入。

### 6.1 端点总览（按 page-inventory §4.2 的 17 个模块）

| 模块 | 端点 | 危险操作 |
|---|---|---|
| 1 运营看板 | `GET /admin/dashboard` | — |
| 2 用户管理 | `GET /admin/users`、`GET/PATCH /admin/users/{id}`、`POST /admin/users/{id}/ban`、`POST /admin/users/{id}/unban`、`POST /admin/users/{id}/revoke-subs`、`POST /admin/users/export` | D1 D2 D3 D14 |
| 3 订单管理 | `GET /admin/orders`、`GET /admin/orders/{trade_no}`、`POST /admin/orders/{trade_no}/mark-paid`、`POST /admin/orders/{trade_no}/refund` | **D6** D7 |
| 4 套餐管理 | `GET/POST /admin/plans`、`PATCH/DELETE /admin/plans/{id}` | D8 |
| 5 节点管理 | `GET/POST /admin/nodes`、`GET/PATCH/DELETE /admin/nodes/{id}`、`POST /admin/nodes/{id}/enable`、`/disable` | D4 D9 |
| 6 节点密钥 | `GET /admin/nodes/{id}/keys`、`POST /admin/nodes/{id}/keys`、`DELETE /admin/node-keys/{key_id}` | **D5** |
| 7 流量统计 | `GET /admin/stats`、`GET /admin/stats/export` | D14 |
| 8 工单处理 | `GET /admin/tickets`、`GET /admin/tickets/{id}`、`POST /admin/tickets/{id}/messages`（含 `is_internal`）、`PATCH /admin/tickets/{id}` | — |
| 9 邀请与返佣 | `GET/POST /admin/invites`、`POST /admin/commissions/{id}/adjust` | D11 |
| 10 审计日志 | `GET /admin/audit` | **只有 GET** |
| 11 管理员账号 | `GET/POST /admin/admins`、`DELETE /admin/admins/{id}`、`POST /admin/admins/{id}/reset-totp` | D15 D16 |
| 12 公告管理 | `GET/POST /admin/notices`、`PATCH/DELETE /admin/notices/{id}` | D12 |
| 13 优惠码 | `GET/POST /admin/coupons`、`PATCH/DELETE /admin/coupons/{id}` | D8 |
| 14 支付与对账 | `GET /admin/payments`、`PATCH /admin/payments/{id}`、`GET /admin/payments/underpaid`、`POST /admin/users/{id}/balance-adjust` | D10 D13 |
| 15 邮件与送达 | `GET /admin/mail/templates`、`PATCH /admin/mail/templates/{id}`、`GET /admin/mail/logs`、`POST /admin/mail/broadcast` | **D11b** |
| 16 系统配置 | `GET /admin/settings`、`PATCH /admin/settings` | D13 |
| 17 域名池 | `GET/POST /admin/domains`、`DELETE /admin/domains/{id}` | D13 |

**审计日志模块只有 `GET`。** 没有 `DELETE`，没有 `PATCH`，OpenAPI spec 里也不存在这两个 operation。
一个能被清理的审计日志等于没有审计日志。

### 6.2 危险操作的四层强制

page-inventory §4.4 列了 16 条危险操作与它们的要求。本节把那些要求翻译成 API 层的强制机制。

| 层 | 机制 | 适用 |
|---|---|---|
| **L1 确认串** | body 必带 `confirmation`，值必须**等于服务端指定的串**（用户邮箱 / 节点名） | 标 🔒 的：D3 D4 D6 D10 D15 D16 |
| **L2 必填原因** | body 必带 `reason`，长度 ≥ 8 字符，进审计日志 | D1 D2 D3 D6 D7 D10 D11 |
| **L3 TOTP step-up** | 请求头 `X-TOTP-Code`，且该 code **5 分钟内只能用一次**（防重放，需 `used_totp` 表） | D3 D5 D6 D10 D15 D16 |
| **L4 独立权限位** | 默认不授予，需单独开 | D6（`admin.order.mark_paid`）、D14（`admin.user.export`） |

**L1 必须在 API 层，不能在前端。** 前端的确认弹窗对一个直接 `curl` 的人是零。
形态是：服务端拿到请求后自己查出目标对象的标识串（如 `users.email`），
与 body 里的 `confirmation` 常数时间比对，不一致 → 422。

D6（手工标记订单已支付）的完整规格，因为它是**全系统最大的内部欺诈面**：

```http
POST /api/v1/admin/orders/20260816T7K2M9Q4/mark-paid
X-TOTP-Code: 481920
Content-Type: application/json

{
  "confirmation": "user@example.com",
  "reason": "链上 txid 7f3a… 已确认到账，网关回调丢失，人工补单",
  "evidence_url": "https://tronscan.org/#/transaction/7f3a…"
}
```

- `confirmation` 必须等于订单所属用户的邮箱
- `reason` ≥ 8 字符
- `X-TOTP-Code` 必须有效且未被用过
- 调用者必须持有 `admin.order.mark_paid` 权限位（**默认不授予，即使团队只有一个人**）
- 审计日志记 `before`/`after` 的完整订单状态 + `evidence_url`

> **这个权限位必须从第一天就存在，即使团队只有一个角色。**
> 一个「等有第二个人再加权限系统」的计划，在有第二个人的那天已经来不及了。

D5（节点密钥轮换）的两步强制：

| 步 | 端点 | 前置条件 |
|---|---|---|
| 1 | `POST /admin/nodes/{id}/keys` | — |
| 2 | `DELETE /admin/node-keys/{old_key_id}` | **服务端校验：该节点存在另一把 `revoked_at IS NULL` 且 `last_used_at > 新密钥签发时刻` 的密钥** |

不满足前置条件 → 409 `STATE_CONFLICT`，message 写明「新密钥尚未被节点使用过，现在吊销旧密钥会导致节点失联」。
**UI 层禁止一步完成是不够的，API 层必须自己拒绝。**

### 6.3 审计日志写入契约

```sql
admin_audit_log(
  id           bigserial primary key,
  admin_id     bigint      not null,
  request_id   text        not null,
  ip           inet        not null,
  user_agent   text,
  action       text        not null,     -- 'order.mark_paid'
  target_type  text        not null,     -- 'order'
  target_id    text        not null,
  before       jsonb,
  after        jsonb,
  reason       text,
  created_at   timestamptz not null default now()
)
```

**三条硬规则**：

1. **审计写入与业务写入在同一个事务里。** 审计写失败 → 整个操作回滚。
   这是「审计不可绕过」唯一可靠的实现方式；异步写审计等于承认审计可能缺失。
2. **`before` / `after` 存变更字段的完整快照，不存 diff。** diff 需要靠对面的数据重建，
   而对面的数据可能已经被改了三次。
3. **没有删除入口，没有编辑入口** —— API 层不存在，前端不存在。
   DB 层面应额外用只允许 `INSERT`/`SELECT` 的角色连接（第一阶段不做，登记在 §14）。

---

## 7 · 内部面：`/internal/tasks/*`

调用方是 Cloud Tasks / Pub-Sub push / Cloud Scheduler。
凭据是 Google 签发的 **OIDC ID token**，校验三项：`aud` = 本服务 URL、`iss` = `https://accounts.google.com`、
`email` = 指定的 service account。失败 403。

| 路径 | 触发 | 频率 | 幂等键 |
|---|---|---|---|
| `POST /internal/tasks/traffic-batch` | Cloud Tasks（由 `/push` 入队） | 每次 push | `traffic_batch.batch_id` |
| `POST /internal/tasks/alive-gc` | Scheduler | 5 分钟 | 天然幂等（`DELETE … WHERE seen_at < …`） |
| `POST /internal/tasks/expire-check` | Scheduler | **5 分钟** | 天然幂等 |
| `POST /internal/tasks/order-timeout` | Scheduler | 1 分钟 | 天然幂等 |
| `POST /internal/tasks/traffic-reset` | Scheduler | 每小时 | `(user_id, reset_period)` |
| `POST /internal/tasks/stat-rollup` | Scheduler | 每小时 + 每日 | `(scope, record_at)` upsert |
| `POST /internal/tasks/chain-scan` | Scheduler | 1 分钟 | `(chain, txid, log_index)` |
| `POST /internal/tasks/mail-send` | Cloud Tasks | 按需 | `mail_queue.id` |
| `POST /internal/tasks/remind-sweep` | Scheduler | 每日 | `(user_id, remind_kind, day)` |

三条约束：

1. **`/internal/tasks/*` 与公网端点在同一个 Cloud Run service** ——
   这是「不要常驻 worker，不需要 min-instances」的直接后果（ADR 0005 / 0006）。
   **它的保护是 OIDC 校验，不是路径保密。** 路径不是秘密，token 才是。
2. **拒绝把 `X-Cloud-Trace-Context` 之类可伪造的头当作判据**（ADR 0006 §10.3）。
3. **Cloud Tasks 是 at-least-once，重复投递是常态不是异常。** 每个 handler 都要有表驱动的幂等测试。

`expire-check` 的频率是 5 分钟而不是每日，理由在 §3.8 bump 规则第 4 条：
到期是时间驱动的状态变化，没有任何写操作会触发 `user_rev` 的更新。它做两件事：

```sql
-- 1. 找出上次运行以来刚过期的用户
SELECT id, group_id FROM users
WHERE expired_at > $last_run AND expired_at <= now();
-- 2. bump 这些用户所属分组能看到的所有节点
UPDATE servers SET user_rev = user_rev + 1 WHERE group_ids && $affected_groups;
```

---

## 8 · 两条关键链路

### 8.1 支付回调（USDT）

```mermaid
sequenceDiagram
    autonumber
    participant U as 用户
    participant W as bp-web
    participant A as bp-api
    participant G as 支付网关
    participant CH as 区块链
    participant Q as Cloud Tasks
    participant DB as PostgreSQL

    U->>W: 选套餐
    W->>A: POST /orders (Idempotency-Key)
    A->>DB: INSERT orders(status=pending,<br/>rate_locked_at, expires_at=T+30min)
    A-->>W: 201 {trade_no}
    W->>A: POST /orders/{trade_no}/pay {usdt_trc20}
    A->>G: 取收款地址 + 报价
    G-->>A: {address, amount_usdt6}
    A->>DB: 金额唯一性占位<br/>冲突则 +100 usdt6 重试(最多100次)
    A-->>W: 200 收银台数据
    U->>CH: 转账 USDT

    CH-->>G: 链上确认
    G->>A: POST /payment/notify/{provider}
    A->>A: 验签失败 → 401 PAYMENT_SIGNATURE_INVALID
    A->>DB: INSERT payment_event(provider, external_id)<br/>ON CONFLICT DO NOTHING

    alt 幂等键已存在（重复投递）
        A-->>G: 200（不重复入账）
    else 新事件
        A->>G: 主动查单（回调不可信）
        G-->>A: 权威金额与状态
        alt 收到金额 ≥ 应付
            A->>DB: 同一事务：status=processing →<br/>写配额/到期 → status=completed
            A->>DB: bump 相关节点 user_rev
            A->>Q: enqueue 佣金结算 + 开通邮件
            A-->>G: 200
        else 收到金额 < 应付（underpaid）
            A->>DB: 记 received_usdt6，status 保持 pending
            A-->>G: 200
            Note over W: 收银台显示「已收到 X，还差 Y」
        end
    end

    Note over A,DB: 订单 expires_at 到点 → status=expired，<br/>但该地址继续监听 ≥24h；<br/>到账入余额，不直接开通
```

三处必须写进代码而不是文档的约束：

1. **回调不可信 → 收到回调后必须反向查单。** pricing-and-plans §4.1 记录了先例：
   NewAPI 的易支付回调漏洞。`POST /orders/{trade_no}/recheck` 是同一逻辑的用户侧入口 ——
   page-inventory 称它为「用户侧的最后防线」。
2. **幂等键是 `(provider, external_id)` 上的唯一索引，不是应用层的 `SELECT … IF NOT EXISTS`。**
   后者在两个 Cloud Run 实例并发处理同一次重投时会双双通过。
3. **开通是一个事务**：写配额 + 写到期 + 改状态 + bump `user_rev`。
   少 bump 一次，用户付了钱但节点在下个 `user_rev` 变更前都看不到他。

### 8.2 流量入账

```mermaid
sequenceDiagram
    autonumber
    participant N as bp-node (v2node)
    participant A as bp-api
    participant DB as PostgreSQL
    participant Q as Cloud Tasks

    loop 每 60 秒（拉）
        N->>A: GET UniProxy/user<br/>Bearer + If-None-Match: W/"3-u482"
        A->>DB: SELECT user_rev FROM servers WHERE id=3<br/>（一次主键查询，1 ms 以内）
        alt rev 未变
            A-->>N: 304 + ETag + Cache-Control: no-cache
        else rev 已变
            A->>DB: SELECT 可用用户（u+d 未超 transfer_enable<br/>AND 未过期 AND NOT banned AND 分组匹配）
            A-->>N: 200 {"users":[…]} + 新 ETag
        end
    end

    loop 每 60 秒（推）
        N->>A: POST UniProxy/push {"1":[u,d],…}
        A->>A: scope 校验 node:traffic:write<br/>node_id 从密钥推导
        A->>DB: INSERT traffic_batch(batch_id, node_id, payload)
        A->>Q: enqueue /internal/tasks/traffic-batch
        A-->>N: 200 {"data":true}（目标 50 ms 以内）
    end

    Q->>A: POST /internal/tasks/traffic-batch (OIDC)
    A->>DB: UPDATE traffic_batch SET claimed_at=now()<br/>WHERE batch_id=$1 AND claimed_at IS NULL
    alt 抢占失败（Cloud Tasks 重投）
        A-->>Q: 200（幂等丢弃）
    else 抢占成功
        A->>DB: UPDATE user_traffic SET u=u+?, d=d+?<br/>RETURNING 是否跨过 transfer_enable
        A->>DB: UPSERT stat_user / stat_server（日聚合）
        A->>DB: UPDATE servers SET last_push_at=now()
        Note over A,DB: 累加本身不 bump user_rev
        A->>DB: 仅对「刚跨过阈值」的用户 bump user_rev
        A-->>Q: 200
    end
```

**为什么要拆成「入库 + 入队 + 异步处理」三步，而不是在 `/push` 里直接累加**：

| 理由 | 量化 |
|---|---|
| `/push` 越慢，v2node 超时重试概率越高，而节点侧重试**无法幂等**（§9.2） | 目标 < 50 ms |
| 累加要触发行锁；ADR 0005 §5 的估算 P3（8 节点 / 300 活跃）= **40 行/秒** | 对 Postgres 是噪声，真正风险是行锁竞争与行版本膨胀 |
| 不要常驻 worker，所以异步只能靠 Cloud Tasks push 回同一个 service | ADR 0005：这样彻底不需要 `min-instances` |

---

## 9 · 幂等

### 9.1 幂等总表

| 端点 | 幂等键 | 键的来源 | 冲突时 |
|---|---|---|---|
| `POST /api/v1/orders` | `Idempotency-Key` 头 | 客户端生成 UUID | 载荷相同 → 返回原结果；载荷不同 → 409 `STATE_IDEMPOTENCY_MISMATCH` |
| `POST /api/v1/orders/{n}/pay` | `Idempotency-Key` 头 | 同上 | 同上 |
| `POST /api/v1/payment/notify/{p}` | `(provider, external_id)` 唯一索引 | 网关事件 ID 或链上 `txid+log_index` | 静默返回 200，**不重复入账** |
| `POST /internal/tasks/traffic-batch` | `traffic_batch.batch_id` | **服务端**在 `/push` 时生成 | `claimed_at` 抢占失败 → 200 丢弃 |
| `POST /internal/tasks/*`（其余） | 见 §7 表 | Scheduler / 服务端 | 天然幂等或 upsert |
| `POST /api/v1/auth/email-code` | 无 | — | 靠限流而非幂等（§10） |

`Idempotency-Key` 的存储：`idempotency_keys(key, user_id, request_hash, response_status, response_body, created_at)`，
TTL 24 小时，`(key, user_id)` 唯一。`request_hash` 用于检测「同一个 key 配不同载荷」。

### 9.2 🔴 `/push` 在 HTTP 层不可能幂等 —— 这是一个必须承认的缺口

**成因**：v2node 上报的是**增量字节**且**不带任何幂等键**。
如果它在收到我们的 200 之前超时并重试，同一批流量会被累加两次。我们无法从服务端区分
「重试的同一批」与「下一个 60 秒窗口的新一批」—— 两者的载荷形状完全一样。

**能治的与不能治的，必须分开**：

| 重复来源 | 能否治 | 手段 |
|---|---|---|
| **Cloud Tasks 重投**（at-least-once） | ✅ 能 | `batch_id` + `claimed_at` 抢占（§8.2） |
| **v2node 超时重试** | ❌ **不能** | 只能降低发生概率 |

**缓解的三条**：

1. `/push` 响应时间预算 < 50 ms，且必须**远小于** v2node 的客户端超时。
   → 需实测 v2node 的超时值与重试策略。
2. 服务端对同一 `node_id` 的连续两次 `/push` 若间隔 < 5 秒，写 `WARN` 日志并计 metric。
   这不阻止重复计费，但让它**可见** —— 一个看不见的过计费会以「用户投诉流量跑得快」的形式出现，
   而那个现象有十几种其他解释。
3. 若实测发现重试频繁，退路是给 v2node 打补丁加一个单调递增的 `push_seq`
   （与 §3.2.4 的 Bearer 补丁是同一个 fork，边际成本很低）。

**量化最坏情况**：一个 60 秒窗口内的真实流量被重复计一次。
单节点单窗口在 Hysteria2 实测 ~1700 KB/s 满速下上限约 **100 MB**；
按 pricing 的锚点 ¥0.10–0.25/GB，一次约 **¥0.01–0.025**。
偶发可忽略；**若每次都重试 3 次则是 3 倍计费，那就不可忽略** ——
所以第 2 条的 metric 不是可选项。

---

## 10 · 限流

### 10.1 分面策略

| 面 | 维度 | 限额 | 超限行为 |
|---|---|---|---|
| 节点面 `/config` `/user` | per `node_id` | 30 req/min（正常 1/min，给 30 倍余量） | 429 + `Retry-After: 60` |
| 节点面 `/push` `/alive` `/alivelist` | per `node_id` | 30 req/min each | 同上 |
| 订阅面 `/s/{token}` | per token | 10 req/min | 429 |
| 订阅面 `/s/{token}` | per 源 IP | 60 req/min | 429（放宽是因为一个企业 NAT 后可能有多个用户） |
| `POST /auth/login` | per IP + per email 双维度 | 5/min 且 10/h（**指数退避**） | 429 + 解锁倒计时 |
| `POST /auth/password/forgot` | per email | **3/h** | 204（**仍返回成功文案**，防枚举） |
| `POST /auth/password/forgot` | per IP | 10/h | 429 |
| `POST /auth/email-code` | per email | 3/h；同一邮箱两次间隔 ≥ 60 s | 429 |
| 用户面其余 | per user_id | 120 req/min | 429 |
| 管理面 | 不限流（IAP 已挡） | 例外：`POST /admin/users/export` 5/h、`POST /admin/mail/broadcast` 2/h | 429 |
| 内部面 | 不限流 | — | — |

`forgot` 的限额单列的理由：它**消耗邮件配额**，而邮件是本项目的核心基础设施
（ADR 0002 把邮件从「配置项」升级为「核心基础设施」）。
AWS SES 退信率 ≥ 5% 进入审查、≥ 10% 可能暂停发信 —— 一次针对不存在邮箱的爆破就能把退信率打上去。

### 10.2 计数存储：两档，不是一档

Cloud Run 多实例（`--max-instances=8`，ADR 0005）意味着进程内计数会被放大 8 倍。
不为此买 Redis（Memorystore $35.77/月，比整个数据库还贵 3.7 倍）。

| 档 | 存储 | 适用 | 代价 |
|---|---|---|---|
| **精确档** | Postgres `UNLOGGED` 表 `rate_limit(key, window_start, count)`，`INSERT … ON CONFLICT DO UPDATE` 原子递增 | `login` / `forgot` / `email-code` / 管理面导出与群发 | 每次一条 upsert。这些端点本身低频，可接受 |
| **近似档** | 每实例进程内令牌桶 | 其余全部（含节点面与用户面通用限流） | **实际上限 = 配置值 × 实例数**，最坏 8 倍 |

**近似档 8 倍放大是被显式接受的**：这些端点的限流目的是防雪崩不是防爆破，
8 倍余量不改变「防雪崩」这个目的。
**凭据爆破与邮件配额消耗必须走精确档** —— 那里 8 倍放大是真实的安全损失。

> ⚠️ 精确档的 DB 写入会占用 Cloud SQL 的连接。ADR 0005 的约束公式是
> `max_instances × pool_max + 运维预留 ≤ max_connections − 3`（`db-f1-micro` 是 25）。
> 限流查询与业务查询共用同一个 `pgxpool`，不新开池。

---

## 11 · 版本策略

### 11.1 三个面的演进权限完全不同

| 面 | 能不能加版本 | 破坏性变更 |
|---|---|---|
| **节点面** | ❌ **永不改版。** v2node 硬编码 `/api/v1/server/UniProxy/*` | ❌ **禁止** |
| 订阅面 | ❌ 不改版（客户端里的订阅 URL 是用户手工导入的，改路径 = 所有人重导） | ❌ 禁止 |
| 用户面 / 管理面 | ✅ 可以并列跑 `/api/v2/*` | ✅ 允许，但要并行期 |

**不做 header 版本协商**（`Accept: application/vnd.babel.v2+json`）。
理由：它把版本信息藏进一个不出现在日志与浏览器地址栏里的地方，排障时永远要多问一句「你用的哪个版本」。
URL 前缀是唯一让版本在每一条 access log 里都可见的方案。

### 11.2 节点面的三条冻结不变量

以下三件事**一旦有一个已部署的节点在跑，就不能改**：

1. **路径**：`/api/v1/server/UniProxy/{config,user,push,alive,alivelist,status}`
2. **凭据的传输位置**：`Authorization` 头（或过渡期的 query，见 §3.2.4）
3. **响应的顶层形状**：`config` 是裸对象、`user` 是 `{"users":[…]}`、`push`/`alive` 是 `{"data":true}`

允许的演进只有一种：**加 optional 字段**，且必须做到 v2node 忽略它也能正常工作（§3.3）。

不允许的：删字段、改字段类型、改字段名、把 optional 变 required、改 HTTP 状态码语义。

### 11.3 万一真的需要破坏性变更

不存在「协商」这条路 —— v2node 不认我们下发的任何版本号。唯一可行的路径：

```
1. 新增一组并列的端点（如 /api/v1/server/UniProxyV2/*），旧端点原样保留
2. 逐节点升级 agent，指向新端点
3. 观察旧端点的 last_used_at 归零
4. 旧端点保留 ≥ 90 天后再下线
```

第 4 步的 90 天不是随便定的：ADR 0007 §已裁定「任何节点变更，旧端点必须并行存活 ≥ 7 天」，
而那是**同一代节点的回滚窗口**；契约级变更影响的是所有节点，量级上要更保守。
90 天是提案，**没有实测依据**。

---

## 12 · OpenAPI 维护

按 [ADR 0006](../05-adr/0006-api-stack.md) §9 的裁决：**spec-first**，三个 spec 文件。

```
openapi/
  uniproxy-v1.yaml     ← 冻结。这是「兼容目标」，不是我们的设计
  user-api.yaml
  admin-api.yaml
```

| spec | 谁在改 | CI 卡什么 |
|---|---|---|
| `uniproxy-v1.yaml` | **基本不改。** 改动需要 PR 描述里写明「为什么 v2node 仍然兼容」 | 真实 v2node 容器的契约测试 |
| `user-api.yaml` | 随功能演进 | `oapi-codegen` + `openapi-typescript` 生成物 `git diff --exit-code` |
| `admin-api.yaml` | 随后台演进 | 同上 |

工作流：改 spec → Go 侧 `oapi-codegen` 生成 `StrictServerInterface` → TS 侧 `openapi-typescript`
+ `openapi-fetch` 生成类型 → CI 跑 `git diff --exit-code` 卡漂移。

**`uniproxy-v1.yaml` 的特殊地位必须写在文件头**：

> 这份 spec 描述的不是我们设计的 API，而是我们**必须兼容的既有契约**。
> 它的正确性不由本仓库的代码定义，而由 v2node 的实际行为定义。
> 因此它的验证手段不是「代码生成 + diff」，而是 **ADR 0006 §12 的真实 v2node 容器契约测试** ——
> 那是唯一能证明「抄对了」的测试。

**本裁决最脆弱的一环**（ADR 0006 §14 已记，此处重申因为它直接落在本文上）：
契约不漂移的整条防线就是 CI 那一行 `git diff --exit-code`。
若发生过一次真实的契约漂移事故，应认真考虑改用 Huma（code-first，spec 由代码派生，构造上不会漂）。

---

## 13 · 代价

> ⚠️ 这一选择的代价，必须留在记录里而不是被措辞掩盖：
>
> 1. **冻结 UniProxy 契约 = 放弃增量下发用户列表。** `/user` 只能全量。
>    200 用户 ≈ 17 KB/次；用户数到 2000 时是 170 KB，8 节点每次变更 = 1.36 MB。
>    ETag 让稳态开销趋近 0，但**每一次配额耗尽、每一次开通都会让全部相关节点重拉全量**。
>    **这个取舍在用户数 > 2000 或节点数 > 20 时不再成立** ——
>    届时唯一出路是给 v2node 加增量游标（即维护 fork），或换 agent（即推翻 system-design §3.2）。
>
> 2. **Bearer 头很可能需要给 v2node 打补丁。** MPL-2.0 是文件级 copyleft，改动文件必须开源；
>    改动量估计约 20 行，但「维护一个 fork 并跟随上游 rebase」是长期税。
>    **若选择过渡态（query string），代价是每节点密钥会进 access log 与可能的 Referer** ——
>    加固三条只保住两条。ADR 0007 阶段 5 之前必须关闭，否则等于永久接受。
>
> 3. **60 秒轮询 + 5 分钟到期扫描 = 状态变更有确定的生效延迟。**
>    封禁最长 60 秒；配额耗尽最长 60 秒；**到期最长 5 分钟 + 60 秒 ≈ 6 分钟**；
>    「全部设备下线」最长 60 秒。
>    按 Hysteria2 实测 ~1700 KB/s 满速，6 分钟白嫖上限约 **612 MB**，
>    按 ¥0.10–0.25/GB 约 **¥0.06–0.15/人次** —— 经济上可忽略。
>    **但「封禁滥用用户要等 60 秒」不可忽略**，60 秒足够完成一次滥用行为。
>
> 4. **`/push` 无法在 HTTP 层幂等（§9.2）。** Cloud Tasks 侧的重投能治，
>    v2node 侧的超时重试治不了。最坏一次多计一个 60 秒窗口（上限约 100 MB，
>    ¥0.01–0.025）；若节点在网络抖动时每次重试 3 次则是 3 倍计费。
>    **缓解只有「让 `/push` 快」与「让重复可见」，没有根治。**
>
> 5. **近似限流被 Cloud Run 实例数放大最多 8 倍**（`--max-instances=8`）。
>    这对防雪崩无损，对防爆破有损 —— 所以凭据类端点必须走 Postgres 精确档，
>    而那会占用 `db-f1-micro` 仅 25 个连接中的份额（ADR 0005 的约束公式）。
>
> 6. **两套解包逻辑。** 节点面裸 JSON、用户/管理面统一信封、订阅面既非 JSON 也非信封 ——
>    这是兼容外部生态的直接成本，任何「统一一下吧」的重构都会破坏兼容性。
>
> 7. **三个 OpenAPI spec + 双向代码生成 = 每次改契约要跑两次生成、PR 里多出成百行生成物。**
>    换来的是「改 spec → 两侧重新生成 → CI 卡漂移」在一个 PR 里原子完成。
>    **若团队开始习惯性地 `--no-verify` 跳过这一步，这套机制的价值立刻归零。**
>
> 8. **本文引用的所有表名与列名都是提案。** `data-model.md` 尚不存在，
>    因此本文与 ADR 0005 / 0006 之间已经出现一处命名冲突（`node_rev` 表 vs `servers.config_rev` 列，见 §3.8），
>    这类冲突会随文档增多而增多。**在数据模型文档落地前，不要照本文的列名写 DDL。**

## 14 · 这次没有解决的

- [ ] 🔴 **v2node 的三项行为全部未验证**，且第一项不验证就不能动工：
      (a) 是否发送 `If-None-Match`（不发则整套 ETag 是摆设）；
      (b) 能否配置 `Authorization` 头（不能则 §3.2.4 退到过渡态）；
      (c) **收到 401/403 时是否清空用户列表**（若清空，一次密钥失误 = 全员瞬时掉线）。
      起一个真实 v2node 容器就能全测，成本远低于其影响。
- [ ] **`/config` 如何下发 Let's Encrypt 证书。** 「证书必须钉 LE、禁用 GTS」是已裁定事实，
      但 Xboard 的 hysteria 分支是否有证书字段，调研中未记录（**需核实**）。
      若契约里没有位置，证书只能固定在装机脚本层（certbot），
      那么「换证书」就不是一次配置下发而是一次运维操作 —— 两者的 runbook 完全不同。
- [ ] **`base_config.device_online_min_traffic` / `node_report_min_traffic` 的语义与单位未知。**
      v2node 会读它们，我们却不知道填什么合适。**需实测。**
- [ ] **`subscription-userinfo` 的 `expire` 在不限时套餐下取什么值。**
      §4.4 给了一个提案（远期时间戳 `4102444800`），但各客户端对空值的渲染 **未实测**。
- [ ] **Clash `smux` / sing-box `multiplex` / `reality-opts` 的确切字段名未验证。**
      只能靠人工加载 golden file，无法自动化（ADR 0006 §12 已记）。
- [ ] **各客户端的真实 `User-Agent` 字符串未抓取。** §4.3 的匹配表是推断的，
      错一行就有一类客户端拿到错误格式。
- [ ] **订阅 token 存哈希后，面板怎么给用户看明文链接？** §4.2 列了两条路
      （加密副本 vs 只在签发时返回一次），**未裁决**。这直接影响 `/subscribe` 页的可用性。
- [ ] **升级折抵算法未定。** user-journey 只定了呈现口径（三行：原套餐剩余价值 / 新套餐价 / 实付），
      算法（按剩余天数还是剩余流量）没有裁决 → `POST /orders` 的 `surplus_amount` 现在没有契约。
- [ ] **流量包与 `reset_traffic_method` 的相互作用未对齐。** user-journey 建议「周期重置时保留流量包配额」，
      与 system-design §6.7 的重置模式未对齐，属于数据模型问题不是契约问题，但会反过来改 `/plans` 的响应形状。
- [ ] **域名池与可达性探活的 API（管理面模块 17）没有设计。**
      因为「域名被封的自动检测」本身还没有机制 —— ADR 0002 §7、system-design §9、ADR 0003 §7
      三处各记了一次未解决。**在它解决前，product-brief §8 承诺的「域名失联恢复 ≤ 30 分钟」零机制支撑。**
- [ ] **`admin-api.yaml` 不应在此时冻结。** ADR 0006 已记：后台前端框架未定，
      spec 应在框架定了之后再冻结。本文的管理面端点表是清单不是契约。
- [ ] **`stat_user_server(user_id, server_id, date, u, d)` 交叉维度表未纳入端点设计。**
      page-inventory 指出 `/usage` 的「按节点分布」需要它，而现有聚合只有 `stat_user` 与 `stat_server`。
      它属于 `data-model.md` 的范围。
- [ ] **审计日志的 DB 层只读加固未做。** §6.3 只在 API 层保证 append-only；
      真正的加固是用一个只有 `INSERT`/`SELECT` 权限的 DB 角色，第一阶段不做。
- [ ] **佣金结算的状态机端点未设计**（`确认中` → `已获得` 的转换由谁触发、冷静期多长）。
- [ ] **邮件模板与 D11b 群发的请求体契约未写**（收件人筛选表达式怎么表示是个独立的设计问题）。
- [ ] **`alivelist` 的口径未核实**：Xboard 按 IP 还是按连接计数；
      v2node 拿到计数后是拒新还是踢旧（user-journey 要求拒新不踢旧）。
      这两条决定「家里能用、公司连不上」这个最难自诊断的现象的具体形态。

# v2node 实际行为核查 · 2026-08-17

> 日期：2026-08-17 · 性质：**证据型核查** · 状态：**已完成**
> 事实基线：`github.com/wyx2685/v2node` @ `2daa9dd4a114aa39294350475defa2b748d595ed`（2026-07-14）
> 采集方式：浅克隆后**通读源码**，非文档推断、非社区转述
> 关联：[roadmap B6/B16/B18](../../00-overview/roadmap.md)、
> [api-contract §14](../../02-architecture/api-contract.md)、
> [ADR 0006 §11.4](../../05-adr/0006-api-stack.md)、[node-provisioning §10](../../04-ops/node-provisioning.md)

---

## 1 · 这些证据证明什么、不证明什么

**证明**：v2node 这个特定 commit 的**代码行为** —— 它发什么头、用什么鉴权、
期望什么响应形状、如何处理错误。这些此前全被标为「需实测」，实际上读源码就能确定。

**不证明**：
- 不证明**运行时行为**。代码读出来的与真机跑出来的可能有差（配置分支、版本差异）。
  但代码是行为的上界，读代码能排除掉大量猜测。
- 不证明**其他节点端**（XrayR / V2bX / soga）的行为，它们的实现不同。
- 不证明这个 commit 之后不会变。**升级 v2node 前必须重读本文涉及的几处。**

---

## 2 · 🔴 头号发现：ETag 设计是成立的

[ADR 0006 §11.4](../../05-adr/0006-api-stack.md) 把「v2node 是否发送 `If-None-Match`」
列为**最高优先级未知**，因为整套 ETag 降载设计押在这一条上。

**答案：发。而且完整实现了条件请求。**

`api/v2board/user.go`：

```go
r, err := c.client.R().
    SetContext(ctx).
    SetHeader("If-None-Match", c.userEtag).        // ① 发送上次拿到的 ETag
    SetHeader("X-Response-Format", "msgpack").
    SetDoNotParseResponse(true).
    Get(path)
...
if r.StatusCode() == 304 {                          // ② 命中就直接返回，不解析响应体
    return nil, nil
}
...
c.userEtag = r.Header().Get("ETag")                 // ③ 保存新 ETag
```

`api/v2board/node.go:122/141` 对 `/config` 是同一套写法。

> **结论：ETag 三步（发送 → 304 短路 → 保存）全部落实。**
> 我们在 `internal/httpx/etag.go` 与 handler 里实现的协商逻辑**有实际收益**，
> 不是空转。ADR 0006 §11.4 的最高优先级未知**已解除**。

---

## 3 · 🔴 鉴权：query string 是**唯一**方式，不是可选项

`api/v2board/panel.go`：

```go
client.SetQueryParams(map[string]string{
    "node_type": "v2node",
    "node_id":   strconv.Itoa(c.NodeID),
    "token":     c.Key,
})
```

**全仓没有任何一处为鉴权设置 `Authorization` 头。**（唯一的 `SetHeader` 用于
`User-Agent`、`If-None-Match`、`X-Response-Format`。）

| 此前的说法 | 实际 |
|---|---|
| api-contract §3.2.4：「v2node 现状**很可能**只会发 query」 | ✅ **确认。而且没有开关可以改成 Bearer** |
| ADR 0006：「v2node 能否配 Authorization 头 **需实测**，最大落地风险」 | ✅ **已解除。答案是不能** |
| 我们的实现 `AllowQueryToken: true` | ✅ **正确，且这个默认值是强制的，不是保守选择** |

> **含义修正**：query token 不是「有期限的过渡态」，而是**当前唯一可行的形态**。
> 「实测确认 v2node 支持 Authorization 头之后就关掉」这个退出条件**不成立** ——
> 退出它需要给 v2node 提 PR 或自己 fork。这一点必须在 api-contract §3.2.4 里改正。

另注：`node_type` 的值是字面量 **`"v2node"`**，不是协议名（不是 `vless`/`hysteria2`）。
我们的 `NodeTypeQuery` 参数校验不能按协议名做枚举。

---

## 4 · 🟡 未记录的兼容要求：msgpack

v2node 在拉用户列表时发 **`X-Response-Format: msgpack`**，并检查响应的
`Content-Type` 是否含 `application/x-msgpack`：

```go
if strings.Contains(r.Header().Get("Content-Type"), "application/x-msgpack") {
    decoder := msgpack.NewDecoder(r.RawResponse.Body)
    ...
} else {
    // 流式 JSON 解析回退
}
```

**好消息：它优雅回退到 JSON。** 我们返回 JSON 完全可用，不必实现 msgpack。

**但 JSON 回退路径对响应形状有硬要求** —— 它是**流式**解析：

```go
for {
    tok, _ := dec.ReadToken()
    if tok.Kind() == '"' && tok.String() == "users" { break }   // 扫描直到 "users" 键
}
tok, _ := dec.ReadToken()
if tok.Kind() != '[' {                                          // 下一个必须是数组
    return nil, fmt.Errorf(`decode user list error: expected "users" array`)
}
```

> 🔴 **`{"users": null}` 会直接报错**，只有 `{"users": []}` 才行。
> 这印证了 sqlc 配置里 `emit_empty_slices: true` 是**承重的**，
> 不是风格偏好 —— 改掉它会让空用户列表的节点直接拉取失败。

**优化机会（非必需）**：实现 msgpack 响应可以显著减小用户列表体积。
在用户数上千之后值得做，当前不做。

---

## 5 · 上报端点的确切形状

### `/push` 流量上报

```go
data := make(map[int][]int64, len(userTraffic))
data[uid] = []int64{upload, download}
_, err := c.client.R().SetBody(data).ForceContentType("application/json").Post(path)
```

- 形状 **`{uid: [upload, download]}`**，确认与我们的规格一致
- 🔴 **响应体被完全忽略**（`_, err :=`）—— api-contract §14 里
  「Xboard 的 `/push` 响应体形状未记录，v2node 是否解析它 **需实测**」**已解除：不解析**。
  我们返回什么都行，只要状态码是 2xx。

### `/alive` 在线设备上报

```go
data := make(map[int][]string)
data[uid] = append(data[uid], ip)     // { UID1:["ip1","ip2"], UID2:["ip3"] }
```

- 上报的是 **IP 列表**，不是计数
- 对应 `alivelist` 返回的是 **`{"alive": {uid: count}}`**（`map[int]int`）
- 即：**节点报 IP 明细，面板回计数**。这解答了 B16 的一半 ——
  **设备计数口径是按 IP**（`user_device_state` 以 IP 为主键是对的）

### 🔴 `alivelist` 失败是静默降级

```go
if err != nil { ...; c.AliveMap.Alive = make(map[int]int); return c.AliveMap.Alive, nil }
if r == nil || r.RawResponse == nil || r.StatusCode() >= 399 {
    c.AliveMap.Alive = make(map[int]int); return c.AliveMap.Alive, nil
}
```

**我们的 `alivelist` 端点挂掉时，节点不会报错，而是当作「所有用户 0 台在线设备」继续服务。**

> 这是一个**安全相关**的行为：设备数限制会静默失效，而不是拒绝服务。
> 对可用性是好事（面板挂了不影响用户上网，符合 system-design §5.3），
> 但意味着**设备数限制不能作为计费或防滥用的强保证** ——
> 它是尽力而为的软限制。这一点必须写进 product-brief 的能力边界。

---

## 6 · 两个语义未知字段的确切含义

api-contract §14 记「`device_online_min_traffic` / `node_report_min_traffic`
值 **需实测** 才能定 —— 单位与语义在调研中未确认」。

`node/user.go` 给出了答案：

```go
reportmin = c.info.Common.BaseConfig.NodeReportMinTraffic
devicemin = c.info.Common.BaseConfig.DeviceOnlineMinTraffic

userTraffic, _ := c.server.GetUserTrafficSlice(c.tag, reportmin)   // ① 过滤上报

for _, traffic := range userTraffic {
    total := traffic.Upload + traffic.Download
    if total < int64(devicemin*1000) {                             // ② 注意 ×1000
        nocountUID[traffic.UID] = struct{}{}
    }
}
// nocountUID 里的用户不计入在线设备上报
```

| 字段 | 语义 | 单位 |
|---|---|---|
| `node_report_min_traffic` | 本轮流量低于此值的用户**不上报流量** | 直接传给 `GetUserTrafficSlice`，**单位待确认为字节**（未在本次读到该函数实现） |
| `device_online_min_traffic` | 本轮流量低于此值的用户**不计入在线设备** | **KB**（代码里 `devicemin*1000` 转成字节） |

> `device_online_min_traffic` 的作用是**过滤掉只挂着不用的连接**，避免
> 「开着客户端没上网」也占掉一个设备名额。这对我们用设备数做套餐杠杆（2/5/10）
> 是重要的 —— 设为 0 会让用户的每个空闲客户端都吃名额，客诉会很多。
>
> **建议初值：`device_online_min_traffic = 1000`（即 1 MB）**，
> 但这是推断值，**需在真实用量下调参**。

---

## 7 · 轮询与重试参数

`conf/conf.go`：

```go
const DefaultNodeRetryCount = 1
const DefaultNodeTimeout    = 15   // 秒
```

- **默认只重试 1 次，超时 15 秒**，均可在节点配置中覆盖
- 拉取与上报是两个独立的定时任务（`node/task.go` 的 `PullInterval` / `PushInterval`）
- api-contract 里「需实测 v2node 的超时值与重试策略」**已解除**

> 15 秒超时对我们的含义：**`/user` 端点的 p99 必须远低于 15 秒**。
> Cloud Run 冷启动 + 数据库查询 + 序列化上千用户，这个预算并不宽裕 ——
> 这是 ETag 之外，`/user` 必须做好的第二个理由。
> 且**只重试 1 次**，意味着一次冷启动超时就会让该节点这一轮拿不到用户列表。

---

## 8 · 对既有文档的修正清单

| 文档 | 原文 | 修正 |
|---|---|---|
| [ADR 0006 §11.4](../../05-adr/0006-api-stack.md) | v2node 是否发 `If-None-Match` = 最高优先级未知 | ✅ **发。ETag 设计成立** |
| [api-contract §3.2.4](../../02-architecture/api-contract.md) | query token 是「**有期限的**过渡态」 | ⚠️ **退出条件不成立** —— v2node 无 Authorization 支持，退出需改上游 |
| [api-contract §14](../../02-architecture/api-contract.md) | `/push` 响应体形状需实测 | ✅ **响应体被忽略，返回什么都行** |
| [api-contract §14](../../02-architecture/api-contract.md) | 两个 BaseConfig 字段语义未知 | ✅ **已确定**，`device_online_min_traffic` 单位是 KB |
| [api-contract](../../02-architecture/api-contract.md) | v2node 超时与重试需实测 | ✅ **15 秒 / 重试 1 次** |
| [roadmap B16](../../00-overview/roadmap.md) | 设备计数按 IP 还是按连接 | ✅ **按 IP**（节点报 IP 列表，面板回计数） |
| [data-model](../../02-architecture/data-model.md) | `emit_empty_slices` 的必要性 | ✅ **承重** —— `{"users":null}` 会让 v2node 解析报错 |
| 未记录 | — | 🆕 **msgpack 优先但回退 JSON**；🆕 **`alivelist` 失败静默降级为无限制** |

---

## 9 · 复现方法

```bash
gh repo clone wyx2685/v2node -- --depth=1
cd v2node && git rev-parse HEAD    # 应为 2daa9dd4a114aa39294350475defa2b748d595ed
sed -n '28,96p'  api/v2board/user.go     # ETag / msgpack / JSON 回退
sed -n '28,72p'  api/v2board/panel.go    # 鉴权装配
sed -n '1,60p'   node/user.go            # 两个 BaseConfig 字段的用法
grep -n 'DefaultNode' conf/conf.go       # 超时与重试默认值
```

---

## 10 · 代价

> ⚠️ 1. **读代码不等于跑过。** 本文所有结论都是静态阅读所得。
> 配置分支、版本差异、以及 xray-core 侧的行为都可能让实际表现不同。
> **首次接入真实节点时必须逐条回验**，尤其是 ETag 命中率与 msgpack 协商。
> 2. **结论绑死在一个 commit 上。** 升级 v2node 前必须重读 §2–§7 涉及的文件。
> 建议把本文的复现命令做成 CI 的定期检查，或至少在升级 checklist 里列出。

## 11 · 这次没有解决的

- [ ] `node_report_min_traffic` 的**单位**未确认 —— 没读 `GetUserTrafficSlice` 的实现（在 xray-core 侧）。
- [ ] `PullInterval` / `PushInterval` 的**默认值**未读到（只确认了它们存在）。
- [ ] `/config` 响应的**完整字段集**未逐字段核对（本次只确认了 ETag 行为与两个 BaseConfig 字段）。
- [ ] msgpack 编码下的用户列表体积收益**未测量**。
- [ ] v2node 对 **401/403 的处理**未读 —— 密钥被吊销时它会重试到死还是退避？
- [ ] 其他节点端（XrayR / V2bX / soga）的行为**完全未查**，本文结论不可外推。

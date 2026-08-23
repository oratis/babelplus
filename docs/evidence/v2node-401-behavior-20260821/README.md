# B7 关闭：v2node 收到 401/403 会怎样 · 2026-08-21

> 日期：2026-08-21 · 性质：**证据型核查** · 状态：**已完成**
> 事实基线：`github.com/wyx2685/v2node` @ `2daa9dd4a114aa39294350475defa2b748d595ed`（2026-07-14）
> 与 `github.com/go-resty/resty/v2` @ `v2.16.5` 的源码 —— **通读源码，未运行容器**
> 关联：[v2node-contract-20260817](../v2node-contract-20260817/)（本文补完它 §12 留下的第一条待办）、
> [roadmap B7](../../00-overview/roadmap.md)、[api-contract §14](../../02-architecture/api-contract.md)、
> [ADR 0002](../../05-adr/0002-notification-channels.md)

---

## 1 · 问的是什么

roadmap 把 B7 描述为「**这是唯一一条『实现方不是我们、后果由我们承担』的条款**」：
一次节点密钥失误，是不是等于**全员瞬时掉线**？
[v2node-contract-20260817 §12](../v2node-contract-20260817/) 把它留成待办：
「v2node 对 401/403 的处理未读 —— 密钥被吊销时它会重试到死还是退避？」

[上线审查 §5 第 4 条](../../00-overview/launch-readiness-review-20260821.md)提议起真实容器验证。
**没有必要** —— 与前一次同理，读源码就能定死。原始摘录：[source-excerpts.txt](source-excerpts.txt)。

---

## 2 · 结论：**运行中不会掉线；重启才会，而且不自愈**

登记的风险形态**是反的**。

### 2.1 运行中（`nodeInfoMonitor` 周期任务）：三重保护，一重都不会清空用户表

1. **`GetUserList` 在 401/403 上返回 error，不返回空列表。**
   它只特判 304；其余状态码一律往下走解码。我们的 401 响应体是
   `application/json` 且不含 `"users"` 键，于是 JSON 分支的 token 扫描
   （`for { tok, err := dec.ReadToken(); ... if tok.String() == "users" { break } }`）
   读到 EOF 就 `return nil, fmt.Errorf("decode user list error: %w", err)`。
   → **拿到的是 error，不是 `[]UserInfo{}`。**
2. **`nodeInfoMonitor` 在该 error 上提前 return。**
   `log.Error("Get user list failed")` 之后 `return nil`，
   **在 `compareUserList` 之前**，所以 `DelUsers` 根本不会被调用，`c.userList` 原封不动。
3. **即使我们错误地返回 200 + 空列表，也有第二道闸**：
   `if len(newU) == 0 { log.Debug("User list no change"); return nil }`。
   空列表被当作「没变化」，不是「删光」。

另外 `c.userEtag` 只在成功路径上更新，所以密钥修好之后条件请求能干净恢复。

### 2.2 重启：`Controller.Start()` 直接拒绝启动

```go
c.userList, err = c.apiClient.GetUserList(context.Background())
if err != nil {
    return fmt.Errorf("get user list error: %s", err)
}
if len(c.userList) == 0 {
    return errors.New("add users error: not have any user")
}
```

🔴 两条都是硬失败：**拉不到用户表起不来，用户数为 0 也起不来。**
密钥失误期间只要进程重启（崩溃、机器重启、换配置、重新部署），节点就起不来，
**而且不会自己重试到成功** —— 要等人把密钥修好再手动拉起。

### 2.3 不重试

`panel.New` 只调了 `client.SetRetryCount(retryCount)`（`DefaultNodeRetryCount = 1`），
**没有注册任何 `RetryCondition`，也没有 `OnAfterResponse` 状态码钩子**。
resty v2.16.5 的 `Backoff` 里默认判据是

```go
needsRetry := err != nil && err == err1   // 只对传输层错误重试
```

→ **401/403 一次都不重试**，既不会「重试到死」也谈不上退避；
它只是记一行 `log.Error`，然后等下一个 `PullInterval`（默认由面板下发）。
超时是 `DefaultNodeTimeout = 15` 秒。

---

## 3 · 顺带确证：`alivelist` 失败会静默把在线设备数归零

`GetUserAlive` 的错误处理与 `GetUserList` **相反** —— 它把失败吞掉：

```go
if r == nil || r.RawResponse == nil || r.StatusCode() >= 399 {
    c.AliveMap.Alive = make(map[int]int)
    return c.AliveMap.Alive, nil       // 返回空 map + nil error
}
```

调用方 `if newA != nil { c.limiter.AliveList = newA }` —— 空 map 是非 nil，
所以**限流器的在线列表被替换成空**。

**这确证了 [上线审查决策 #8](../../00-overview/launch-readiness-review-20260821.md) 的前提**
（「alivelist 失败时 v2node 静默降级为零在线设备 → 设备数只能是软限制」），
并把适用范围扩大了一条：**401/403 也走这个分支**，不只是网络故障。

---

## 4 · 对我们的三条要求

1. **节点密钥轮换必须是两步**（先加新密钥、确认生效、再吊销旧的）。
   这条 [page-inventory D5](../../03-product/page-inventory.md) 已经写了，
   本文给它补上了理由：一步轮换的代价不是「立刻掉线」，而是
   **进入一个静默失效窗口 —— 配置停止更新，而任何一次重启都会变成不可自愈的全员掉线**。
   静默比掉线更难发现，所以这条纪律的必要性**比原来记的更高**，不是更低。
2. **`bp_uniproxy_auth_fail` 这条日志指标是这个失效窗口的唯一可观测信号。**
   （已于 2026-08-21 建好，见 [gcp-inventory-20260821 §5.3](../gcp-inventory-20260821/)。）
   节点侧只写一行 `log.Error`，我们这边不看指标就完全不知道。
3. **401 响应体不能含 `"users"` 键。** 本文的第 1 重保护依赖这一点。
   目前的错误信封是 `{"error":{...}}`，安全；但这是一条**必须写进契约的隐性依赖**。

---

## 5 · 这些证据证明什么、不证明什么

**证明**：v2node 在**这一个 commit** 上，对非 304 响应（含 401/403）的代码路径与控制流。

**不证明**：
- 不证明**运行时行为**。与前一次同理，代码是行为的上界。
  §2.1 第 1 重保护依赖「我们的 401 响应体不含 `"users"` 键」这个前提 ——
  它由**我们**的实现保证，一旦错误信封变了就要重读本文。
- 不证明其他节点端（XrayR / V2bX / soga）的行为。
- 不证明升级之后仍成立。**升级 v2node 前必须重读本文。**
- 不证明 `PullInterval` 的实际取值 —— 它由面板下发，失效窗口的长度取决于它。

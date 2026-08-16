// Package httpx 提供跨三套 API 面复用的 HTTP 工具：ETag 协商、错误映射、请求上下文。
package httpx

import (
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
)

// ETag 协商是 UniProxy 两个高频端点的性能命门，不是可选优化。
//
// 节点每 60 秒轮询 /config 与 /user。10 个节点 × 2 端点 × 1440 次/天 × 30 天
// = 864,000 次/月/端点，两个端点合计 1,728,000 —— 占 Cloud Run 免费额度
// 200 万/月的 86%。不做 304 就意味着每次都要查库并序列化全量用户列表。
//
// 🔴 但这一切建立在一个**未经验证的前提**上：v2node 会发送 If-None-Match。
// 若它不发，本文件一行都不生效（ADR 0006 §11.4 已登记为最高优先级实测项）。
// 实测方法见 docs/04-ops/node-provisioning.md 的接入验证一节。

// StrongETag 用内容哈希生成强 ETag。
//
// 用于 /config：配置内容变化频率低，直接哈希响应体最准确。
func StrongETag(body []byte) string {
	sum := sha256.Sum256(body)
	return `"` + base64.RawURLEncoding.EncodeToString(sum[:16]) + `"`
}

// RevisionETag 用修订号生成 ETag，**不需要先序列化响应体**。
//
// 用于 /user：用户列表可能有上万行，为了算 ETag 而序列化一遍再丢弃是纯浪费。
// data-model.md 的 node_rev 表专门为此设计 —— users 表的关键列变更会由触发器
// bump user_rev，所以修订号变了才需要真正查询与序列化。
//
// 代价：修订号是**表级**的，任何一个用户变更都会让所有节点的缓存失效。
// 在我们的规模（节点数十、用户数百）这个粒度足够；若将来节点上百，
// 需要改成按 server_id 分桶的修订号。
func RevisionETag(scope string, rev int64) string {
	return `"` + scope + "-" + strconv.FormatInt(rev, 10) + `"`
}

// MatchesETag 按 RFC 9110 §8.8.3.2 做**弱比较**，判断 If-None-Match 是否命中。
//
// 弱比较意味着 W/"x" 与 "x" 视为相等 —— 中间层（Cloud Run 前端、CDN）
// 有可能把强 ETag 改写成弱 ETag，按强比较会导致 304 永远不命中，
// 表现为「ETag 配了但请求量没降」，且极难排查。
//
// 支持 If-None-Match: * 与逗号分隔的多值列表。
func MatchesETag(ifNoneMatch string, current string) bool {
	ifNoneMatch = strings.TrimSpace(ifNoneMatch)
	if ifNoneMatch == "" {
		return false
	}
	if ifNoneMatch == "*" {
		return true
	}
	cur := normalizeETag(current)
	for _, cand := range strings.Split(ifNoneMatch, ",") {
		if normalizeETag(cand) == cur {
			return true
		}
	}
	return false
}

// normalizeETag 剥掉弱标记 W/ 与两侧引号，用于弱比较。
func normalizeETag(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "W/")
	s = strings.TrimPrefix(s, "w/")
	return strings.Trim(s, `"`)
}

// CacheControlNoStore 是 UniProxy 端点的缓存策略。
//
// 节点配置与用户列表含密钥材料，绝不能被任何中间层缓存。
// ETag 用于**条件请求**（省掉响应体），不是用于共享缓存 —— 这两件事容易混淆。
const CacheControlNoStore = "no-store, no-cache, must-revalidate, private"

// ScopeUserList / ScopeConfig 是 RevisionETag 的作用域前缀，避免两个端点的
// 修订号在同一个数字上碰撞导致误命中。
const (
	ScopeUserList = "u"
	ScopeConfig   = "c"
)

// FormatSubscriptionUserInfo 生成 subscription-userinfo 响应头。
//
// 这是与 Clash / sing-box / Shadowrocket 生态的**硬接口**，格式不能自创 ——
// 客户端靠它显示流量条与到期日。字段单位是**字节**，expire 是 Unix 秒。
// expire 传 0 表示不限时（对应 users.expired_at IS NULL）。
func FormatSubscriptionUserInfo(upload, download, total, expireUnix int64) string {
	return fmt.Sprintf("upload=%d; download=%d; total=%d; expire=%d",
		upload, download, total, expireUnix)
}

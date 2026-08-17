// Package middleware 提供三套互相独立的鉴权，以及请求 ID、日志、panic 恢复。
//
// 三套鉴权刻意不共用代码路径：节点面、用户面、管理面的失败模式与审计要求都不同，
// 强行抽象只会让「哪条路径能拿到什么权限」变得难以论证。
package middleware

import (
	"context"
	"crypto/sha256"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	dbgen "github.com/oratis/babelplus/api/db/gen"
)

// ---- 节点鉴权 ----
//
// 相对 Xboard 的三处加固（docs/01-research/panels-and-market.md §6.6）：
//  1. **每节点独立密钥**，不是全节点共享一个 server_token
//  2. **DB 只存 sha256 哈希**，泄库拿不到可用密钥
//  3. **scope 白名单**，密钥泄漏也只能调它被授权的那几个端点
//
// 凭据位置：本实现优先读 `Authorization: Bearer`，回退到 `?token=`。
// 后者**不是妥协，是 v2node 的唯一形态**：
//
// ✅ 2026-08-17 已读 v2node 源码核实（evidence/v2node-contract-20260817）：
//   它用 client.SetQueryParams({node_type, node_id, token}) 鉴权，
//   **全仓没有任何一处为鉴权设置 Authorization 头，也没有开关可切换。**
//
// 所以「过渡态」这个说法要修正：query token 不是有期限的妥协，
// 而是**当前唯一可行的形态**。退出它需要给 v2node 提 PR 或自己 fork，
// 不是「等实测确认后关掉」那么简单。AllowQueryToken 的默认值 true 是强制的。
//
// 另注：node_type 的值是字面量 "v2node"，不是协议名 —— 不要按协议名做枚举校验。

type ctxKey int

const (
	ctxKeyNodeAuth ctxKey = iota
	ctxKeyRequestID
)

// NodeAuth 是通过鉴权的节点身份，注入请求上下文供 handler 使用。
type NodeAuth struct {
	KeyID      int64
	ServerID   int64
	ServerCode string
	Scopes     []string
}

// HasScope 判断是否被授权某个 scope。
func (a *NodeAuth) HasScope(want string) bool {
	for _, s := range a.Scopes {
		if s == want {
			return true
		}
	}
	return false
}

// NodeAuthenticator 是节点鉴权所需的最小数据库能力。
// 收窄成接口而不是直接吃 *store.Store，是为了单测能塞假实现。
type NodeAuthenticator interface {
	AuthenticateServerKey(ctx context.Context, keyHash []byte) (dbgen.AuthenticateServerKeyRow, error)
}

// NodeAuthConfig 是节点鉴权的配置。
type NodeAuthConfig struct {
	DB     NodeAuthenticator
	Pepper string
	Logger *slog.Logger
	// AllowQueryToken 控制是否接受 ?token=。
	//
	// **对 v2node 必须为 true** —— 它只发 query，不发 Authorization 头（已读源码核实）。
	// 保留这个开关是为了将来接入支持 Bearer 的节点端时能收紧，
	// 而不是为了「等 v2node 支持后关掉」（它不会自己支持）。
	AllowQueryToken bool
}

// AuthError 是鉴权失败的结果，带上要返回给节点的 HTTP 状态与错误码。
type AuthError struct {
	Status  int
	Code    string
	Message string
}

func (e *AuthError) Error() string { return e.Code + ": " + e.Message }

// AuthenticateNode 校验节点密钥并检查 scope。
//
// 返回值要么是 (auth, nil)，要么是 (nil, *AuthError)。
// 刻意不返回裸 error —— 调用方需要知道该回 401 还是 403，把这个判断留在这里，
// 避免每个调用点各自映射一遍状态码。
func AuthenticateNode(ctx context.Context, cfg NodeAuthConfig, r *http.Request, requiredScope string) (*NodeAuth, *AuthError) {
	raw, from := extractNodeToken(r, cfg.AllowQueryToken)
	if raw == "" {
		return nil, &AuthError{http.StatusUnauthorized, "NODE_KEY_INVALID", "缺少节点密钥"}
	}

	// 长度/字符集不合法直接拒，不查库 —— 省掉一次数据库往返，
	// 也让针对密钥表的探测拿不到时序差异。
	if !plausibleToken(raw) {
		return nil, &AuthError{http.StatusUnauthorized, "NODE_KEY_INVALID", "节点密钥格式非法"}
	}

	sum := sha256.Sum256([]byte(cfg.Pepper + raw))
	row, err := cfg.DB.AuthenticateServerKey(ctx, sum[:])
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, &AuthError{http.StatusUnauthorized, "NODE_KEY_INVALID", "节点密钥无效或已吊销"}
		}
		cfg.Logger.ErrorContext(ctx, "节点鉴权查询失败", "err", err)
		return nil, &AuthError{http.StatusInternalServerError, "INTERNAL_ERROR", "内部错误"}
	}
	if !row.ServerEnabled {
		return nil, &AuthError{http.StatusForbidden, "AUTH_PERMISSION_DENIED", "节点已停用"}
	}

	auth := &NodeAuth{
		KeyID:      row.KeyID,
		ServerID:   row.ServerID,
		ServerCode: row.ServerCode,
		Scopes:     row.Scopes,
	}
	if !auth.HasScope(requiredScope) {
		cfg.Logger.WarnContext(ctx, "节点 scope 不足",
			"server_code", row.ServerCode, "want", requiredScope, "have", row.Scopes)
		return nil, &AuthError{http.StatusForbidden, "NODE_SCOPE_DENIED", "该密钥无权访问此端点"}
	}

	if from == tokenFromQuery {
		// 记录 query 凭据的使用量。它对 v2node 是常态，不是异常 ——
		// 但凭据会进 access log 与 Referer，这个暴露面需要能被观测到，
		// 以便将来接入支持 Bearer 的节点端后确认可以收紧。
		// monitoring.md 基于这条日志建了 log-based metric。
		cfg.Logger.InfoContext(ctx, "节点使用 query string 凭据",
			"server_code", row.ServerCode, "key_prefix", row.KeyPrefix)
	}

	return auth, nil
}

// WithNodeAuth 把节点身份放进上下文。
func WithNodeAuth(ctx context.Context, a *NodeAuth) context.Context {
	return context.WithValue(ctx, ctxKeyNodeAuth, a)
}

// NodeAuthFrom 从上下文取出节点身份。handler 里用。
func NodeAuthFrom(ctx context.Context) (*NodeAuth, bool) {
	a, ok := ctx.Value(ctxKeyNodeAuth).(*NodeAuth)
	return a, ok
}

// WriteAuthError 把 AuthError 写成响应。
func WriteAuthError(w http.ResponseWriter, e *AuthError) {
	writeErrEnvelope(w, e.Status, e.Code, e.Message)
}

type tokenSource int

const (
	tokenFromNone tokenSource = iota
	tokenFromHeader
	tokenFromQuery
)

// extractNodeToken 优先取 Authorization 头，回退到 query。v2node 走后者。
func extractNodeToken(r *http.Request, allowQuery bool) (string, tokenSource) {
	if h := r.Header.Get("Authorization"); h != "" {
		if v, ok := strings.CutPrefix(h, "Bearer "); ok {
			if v = strings.TrimSpace(v); v != "" {
				return v, tokenFromHeader
			}
		}
	}
	if allowQuery {
		if v := strings.TrimSpace(r.URL.Query().Get("token")); v != "" {
			return v, tokenFromQuery
		}
	}
	return "", tokenFromNone
}

// plausibleToken 做一次廉价的形态校验。
func plausibleToken(s string) bool {
	if len(s) < 24 || len(s) > 128 {
		return false
	}
	for _, c := range s {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-', c == '_':
		default:
			return false
		}
	}
	return true
}

// TouchKeyLastUsed 异步更新密钥的最后使用时间。
//
// 刻意不在鉴权路径上同步写 —— 节点每 60 秒轮询两个端点，同步 UPDATE
// 会把一个纯读路径变成写路径，而 db-f1-micro 的连接非常紧张。
// 丢几条 last_used_at 不影响安全性，它只用于「这把密钥还活着吗」的运营判断。
func TouchKeyLastUsed(logger *slog.Logger, touch func(context.Context, int64) error, keyID int64) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := touch(ctx, keyID); err != nil {
			logger.Warn("更新节点密钥 last_used_at 失败", "key_id", keyID, "err", err)
		}
	}()
}

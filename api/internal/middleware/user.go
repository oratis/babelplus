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

// ---- 用户会话鉴权 ----
//
// 职责边界：本文件只做「**已有** token → 验明身份」。
// 签发（登录 / 注册 / refresh 轮换）不在这里，也不该在这里 ——
// 校验路径要能被独立论证，混进签发逻辑之后「这条分支能不能造出一个会话」
// 就变成了需要通读全文件才能回答的问题。
//
// 与节点鉴权（node.go）刻意**不共用代码路径**：两者的失败模式完全不同 ——
// 节点鉴权失败是运维事故（密钥配错，全体用户掉线），用户鉴权失败是日常（token 过期）。
// 共用一条链会让「收紧哪一边」这件事变成两边一起动。
//
// 存储形态（0003_accounts.up.sql）：user_sessions.refresh_hash 存 sha256，**不存明文**。
// 泄库拿到的是哈希，配合 pepper（BP_SESSION_SIGNING_KEY，只在进程内存里）
// 无法离线还原成可用 token。
//
// 🔴 与契约的一处**已知偏差**，必须记下来：
// api-contract.md §5 与 openapi.yaml 的 securitySchemes.userSession 写的是
// 「短期 access JWT（15 分钟）+ refresh token（30 天，一次性轮换）」，
// 而 DB 里只有 user_sessions（不透明 token + 哈希）这一种载体，没有任何 JWT 相关的表或密钥配置。
// 本实现校验的是**不透明会话 token**，即 user_sessions 那一份。
// TODO(P2): access JWT 落地时，本文件变成 refresh 路径的校验器，
// 另需一个独立的 JWT 验签中间件走用户面常规请求 —— 那时两者的 401 语义要能区分
// （AUTH_TOKEN_EXPIRED 触发前端静默 refresh，AUTH_TOKEN_INVALID 触发跳登录）。
// 在 JWT 落地前，本中间件对两种情况都返回 AUTH_TOKEN_INVALID，见 authFailure 的注释。

// SessionCookieName 是会话 cookie 的默认名。
//
// 加 `bp_` 前缀是为了在浏览器 devtools 与日志里一眼认出是我们的，
// 也避免与同域下其他服务（如果将来共域部署）撞名。
const SessionCookieName = "bp_session"

// 会话 token 的形态约束。
//
// 签发侧应当是 32 字节 CSPRNG 的 base64url 无填充（43 字符）。
// 这里给的是**区间**而不是等式：签发格式将来可能加前缀或改长度，
// 卡死等式会让一次无害的格式调整表现为「所有人被登出」。
const (
	minSessionTokenLen = 24
	maxSessionTokenLen = 128
)

// userAuthCtxKey 是上下文键。
//
// 用独立的空结构体类型而不是复用 node.go 的 ctxKey 枚举：
// 两套身份放进同一个键空间时，一次 iota 顺序调整就能让
// NodeAuthFrom 取到 UserAuth（类型断言会失败，但那已经是运行时才发现）。
// 不同类型的键在编译期就不可能互相取到。
type userAuthCtxKey struct{}

// UserAuth 是通过鉴权的用户身份，注入请求上下文供 handler 使用。
//
// 刻意**不带** email / plan / 流量这些业务字段：中间件每请求跑一次，
// 往里塞字段等于给每个请求加一次 join。handler 需要什么自己查。
type UserAuth struct {
	UserID    int64
	SessionID int64
	IssuedAt  time.Time
	ExpiresAt time.Time
	// FromCookie 记录凭据来源。cookie 来源的写操作需要 CSRF 防护（见 UserAuthConfig.AllowCookie），
	// handler 可据此决定是否要求额外的 CSRF 令牌。
	FromCookie bool
}

// UserSessionReader 是用户会话鉴权所需的最小数据库能力。
// 收窄成接口而不是直接吃 *store.Store，是为了单测能塞假实现。
type UserSessionReader interface {
	GetUserSessionByHash(ctx context.Context, refreshHash []byte) (dbgen.GetUserSessionByHashRow, error)
}

// UserAuthConfig 是用户会话鉴权的配置。
type UserAuthConfig struct {
	DB     UserSessionReader
	Pepper string // BP_SESSION_SIGNING_KEY
	Logger *slog.Logger

	// AllowCookie 控制是否接受 cookie 里的会话 token。
	//
	// 🔴 默认 false（零值即 fail-closed），必须显式打开。理由：
	// cookie 是**浏览器自动附带**的凭据，配合 CORS 的 Allow-Credentials: true，
	// 白名单内任意页面发起的写请求都会自动带上会话 —— 这就是 CSRF。
	// Authorization 头不会自动附带，所以头形态天然免疫。
	//
	// TODO(P2): 打开 cookie 形态之前必须先有 CSRF 防护
	// （SameSite=Lax 不够 —— 独立主域名池意味着 web 与 api 是跨站，
	//  Lax 下 cookie 根本不会被发出；所以真要用 cookie 就得 SameSite=None，
	//  而 None 完全没有 CSRF 防护，必须配双提交令牌或 Origin 头校验）。
	AllowCookie bool

	// CookieName 留空时用 SessionCookieName。
	CookieName string

	// Now 可注入以便测试时间边界。留空用 time.Now。
	Now func() time.Time
}

func (c UserAuthConfig) cookieName() string {
	if c.CookieName != "" {
		return c.CookieName
	}
	return SessionCookieName
}

func (c UserAuthConfig) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

// HashSessionToken 计算会话 token 的存库哈希。
//
// **签发侧与校验侧必须调用同一个函数** —— 两处各写一遍 sha256(pepper+raw)
// 的后果是某天有人在一侧改了拼接顺序，表现为「部分用户随机被登出」。
//
// 这里用 sha256 而不是 argon2id，与节点密钥同理（api-contract.md §3.2.1）：
// 会话 token 是 256 位 CSPRNG 高熵值，离线爆破在物理上不成立，
// 而慢哈希的成本要在**每一个已登录请求**上付。
// 用户**密码**是另一回事，那里必须用 argon2id —— 那是低熵人类输入。
func HashSessionToken(pepper, raw string) []byte {
	sum := sha256.Sum256([]byte(pepper + raw))
	return sum[:]
}

// AuthenticateUser 校验会话 token 并返回用户身份。
//
// 返回值要么是 (auth, nil)，要么是 (nil, *AuthError)。与 AuthenticateNode 同样的约定：
// 把「该回 401 还是 403」的判断留在这里，避免每个调用点各自映射一遍状态码。
func AuthenticateUser(ctx context.Context, cfg UserAuthConfig, r *http.Request) (*UserAuth, *AuthError) {
	raw, fromCookie := extractSessionToken(r, cfg.AllowCookie, cfg.cookieName())
	if raw == "" {
		return nil, authFailure("缺少会话凭据")
	}

	// 形态不合法直接拒，**不查库** —— 与节点鉴权同一纪律。
	// 省掉一次数据库往返（db-f1-micro 的连接极紧张），
	// 也让针对会话表的探测拿不到「格式对但不存在」与「格式就不对」之间的时序差异。
	if !plausibleSessionToken(raw) {
		return nil, authFailure("会话凭据格式非法")
	}

	row, err := cfg.DB.GetUserSessionByHash(ctx, HashSessionToken(cfg.Pepper, raw))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// 查询本身已经过滤了 revoked_at IS NULL AND expires_at > now()，
			// 所以「不存在 / 已吊销 / 已过期」三者在这里**无法区分** —— 这是好事：
			// 区分开就等于告诉攻击者「这个 token 曾经存在过」。
			return nil, authFailure("会话无效或已过期")
		}
		cfg.Logger.ErrorContext(ctx, "用户会话查询失败", "err", err)
		return nil, &AuthError{http.StatusInternalServerError, "INTERNAL_ERROR", "内部错误"}
	}

	// 下面两项是**纵深防御**：SQL 里已经有 revoked_at / expires_at 的过滤，
	// 这里再判一次，是为了让「有人改了 users.sql 的 WHERE 子句」这件事
	// 不会静默地变成一个鉴权绕过。多两次比较的成本可以忽略。
	now := cfg.now()
	if row.RevokedAt.Valid {
		return nil, authFailure("会话已吊销")
	}
	if !row.ExpiresAt.Valid || !row.ExpiresAt.Time.After(now) {
		return nil, authFailure("会话已过期")
	}

	// 账号已注销（AnonymizeUser 置 deleted_at + banned）。
	// 归到 401 而不是 403：这个账号在用户面已经不存在了，没有「你被拒绝」可言。
	if row.UserDeletedAt.Valid {
		return nil, authFailure("会话无效或已过期")
	}

	// 封禁归 403：与「凭据无效」必须分开，否则被封的用户会看到「登录已过期」，
	// 于是不停地重新登录 —— 客服工单里最常见的一类无效来回。
	// 前端拿到 AUTH_PERMISSION_DENIED 才能显示「账号已被封禁」。
	if row.Banned {
		return nil, &AuthError{http.StatusForbidden, "AUTH_PERMISSION_DENIED", "账号已被封禁"}
	}

	// TODO(P2): 更新 user_sessions.last_used_at。
	// 刻意不在这里同步写 —— 与 TouchKeyLastUsed 同样的理由：
	// 每个已登录请求都写一次会把纯读路径变成写路径。
	// 需要时补一条 TouchUserSession 查询，走 goroutine 异步更新。

	return &UserAuth{
		UserID:     row.UserID,
		SessionID:  row.ID,
		IssuedAt:   row.IssuedAt.Time,
		ExpiresAt:  row.ExpiresAt.Time,
		FromCookie: fromCookie,
	}, nil
}

// authFailure 统一构造 401。
//
// code 一律 AUTH_TOKEN_INVALID，**不区分「过期」与「无效」**。
// 契约里有 AUTH_TOKEN_EXPIRED（前端据此静默 refresh 一次），但那是给 access JWT 用的 ——
// JWT 能在**不查库**的情况下从签名载荷里读出 exp，所以区分是免费的。
// 这里的不透明 token 要区分就得放宽 SQL 的 expires_at 过滤，
// 等于把「这个 token 存在过」泄漏给任何持有过期 token 的人。不值得。
//
// message 用于人读，前端**禁止**匹配 message 做分支（api-contract.md §2.3）。
func authFailure(msg string) *AuthError {
	return &AuthError{http.StatusUnauthorized, "AUTH_TOKEN_INVALID", msg}
}

// WithUser 把用户身份放进上下文。
func WithUser(ctx context.Context, u *UserAuth) context.Context {
	return context.WithValue(ctx, userAuthCtxKey{}, u)
}

// UserFrom 从上下文取出用户身份。handler 里用。
//
// ok 为 false 表示这条路由**没挂**用户鉴权中间件（装配错误），
// 而不是「未登录」—— 未登录的请求根本到不了 handler。
// handler 拿到 false 时应当返回 500 而不是 401，否则装配错误会伪装成鉴权问题。
func UserFrom(ctx context.Context) (*UserAuth, bool) {
	u, ok := ctx.Value(userAuthCtxKey{}).(*UserAuth)
	return u, ok
}

// RequireUser 是 net/http 形态的中间件：鉴权失败直接写响应，成功则注入上下文。
//
// 用户面 operation 的挂载方式尚未裁定（节点面走 StrictMiddlewareFunc + operationID 表，
// 见 cmd/server/main.go 的 nodeScopeMiddleware）。提供这个 http.Handler 形态，
// 是为了让「按路由分组挂载」与「按 operationID 挂载」两种装配方式都能直接复用
// AuthenticateUser，不必各自抄一遍错误映射。
func RequireUser(cfg UserAuthConfig) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			auth, authErr := AuthenticateUser(r.Context(), cfg, r)
			if authErr != nil {
				WriteAuthError(w, r, authErr)
				return
			}
			next.ServeHTTP(w, r.WithContext(WithUser(r.Context(), auth)))
		})
	}
}

// extractSessionToken 优先取 Authorization 头，其次 cookie。
//
// 顺序不能反：前端显式设置的头代表「这次请求确实想以这个身份发出」，
// 而 cookie 是浏览器自动附带的。头优先意味着一个明确的意图能覆盖掉自动行为。
//
// **不支持 query string 形态。** 节点面接受 ?token= 是因为 v2node 只发那个（已读源码核实），
// 用户面没有这个约束 —— 而 query 里的凭据会进 access log、Referer 头与浏览器历史。
func extractSessionToken(r *http.Request, allowCookie bool, cookieName string) (string, bool) {
	if h := r.Header.Get("Authorization"); h != "" {
		if v, ok := strings.CutPrefix(h, "Bearer "); ok {
			if v = strings.TrimSpace(v); v != "" {
				return v, false
			}
		}
		// 有 Authorization 头但不是 Bearer 形态：不回退到 cookie。
		// 回退会让「客户端发了个坏头」表现为「以另一个身份成功了」。
		return "", false
	}
	if allowCookie {
		if c, err := r.Cookie(cookieName); err == nil {
			if v := strings.TrimSpace(c.Value); v != "" {
				return v, true
			}
		}
	}
	return "", false
}

// plausibleSessionToken 做一次廉价的形态校验。
//
// 与 node.go 的 plausibleToken 分开写而不是复用：两者的合法区间会各自演进
// （节点密钥有 bpn_ 前缀与 key_id 段，会话 token 没有），
// 共用一个函数会让收紧其中一边的形态检查意外收紧另一边。
func plausibleSessionToken(s string) bool {
	if len(s) < minSessionTokenLen || len(s) > maxSessionTokenLen {
		return false
	}
	// base64url 无填充的字符集。不接受 '=' —— 签发侧用的是 RawURLEncoding。
	for _, c := range s {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-', c == '_':
		default:
			return false
		}
	}
	return true
}

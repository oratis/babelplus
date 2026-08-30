package middleware

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hmac"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base32"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	dbgen "github.com/oratis/babelplus/api/db/gen"
)

// ---- 管理面鉴权 ----
//
// 这是 ADR 0006 §10 五条链里的第四条，与用户面（user.go）、节点面（node.go）
// **不共用任何代码路径**。ADR §10.3 第 1 条把「一个全局 auth 中间件 + 身份类型 if 分支」
// 列为禁止事项：那正是 Xboard 的病灶，每加一条分支都可能给节点密钥
// 开一条通往管理 API 的路。所以这里宁可把 extract / 形态校验 / 错误映射再写一遍。
//
// # 两套独立凭据
//
// api-contract §6 开头：「鉴权三道闸：独立主域名 + IAP/IP 白名单 + 强制 TOTP」，
// 且「IAP 与 TOTP 是两套独立凭据，任一泄漏不足以进入」。本文件实现其中两道：
//
//	AuthenticateAdmin  —— 每请求跑一次：IAP assertion → admin_users 查身份
//	RequireStepUp      —— **按操作**跑：危险操作（§6.2 L3）额外要一次当次 TOTP
//
// step-up 刻意不做成「每请求都要 TOTP」：那会让后台每 30 秒要求重新输一次码，
// 真实后果是运维把 TOTP 关掉。它只挡 §6.2 表里标了 L3 的那几个操作。
//
// # 失败一律 403，不是 401
//
// ADR 0006 §10.2 的表里管理面那一行写死了「失败响应 403」。理由不是美学：
// 管理面前面站着 IAP，**401 会让浏览器/IAP 认为凭据没给对而反复重新走登录流程**，
// 于是一个「你不是管理员」的确定性拒绝会表现为无限跳转。
// 403 是终态：IAP 已经证明了你是谁，是我们这一层不认你。
//
// 错误码只用 api-contract §2.3 已有的三个（AUTH_PERMISSION_DENIED /
// AUTH_TOTP_REQUIRED / AUTH_TOTP_INVALID）。**不新造码** —— §12 要求新码先进
// OpenAPI 的 enum，而本轮不允许改 openapi.yaml。
//
// # 与用户面的一处形态差异
//
// UserAuth 里刻意不带业务字段（每请求一次 join 太贵）。AdminAuth 反过来**带**
// 角色与四个权限位：管理面 QPS 是个位数，而 §6.2 L4 要求每个危险操作都能就地判权限位；
// 让 handler 自己再查一次 admin_users 才是错的 —— 那意味着「忘了查」等于「放行」。

// IAPAssertionHeader 是 Cloud IAP 注入的断言头。
//
// 🔴 **这个头在没有 IAP 的部署形态下可以被任意伪造。** `bp-api` 目前是
// `--ingress=all` 直接暴露在 `*.run.app` 上（见 clientip 相关注释），
// 任何人都能 `curl -H 'x-goog-iap-jwt-assertion: ...'` 打进来。
// 所以本文件从不「信任头的存在」，只信任**头里那个 JWT 的签名**，
// 而且在没有配置 audience 时整体拒绝 —— 见 AuthenticateAdmin 开头。
const IAPAssertionHeader = "x-goog-iap-jwt-assertion"

// TOTPCodeHeader 是 §6.2 L3 的 step-up 请求头。
const TOTPCodeHeader = "X-TOTP-Code"

// IAPIssuer 是 IAP 断言的固定签发者。
const IAPIssuer = "https://cloud.google.com/iap"

// DefaultIAPJWKSURL 是 IAP 公钥集合的地址。
//
// 注意它**不是**普通的 Google OIDC JWKS（www.googleapis.com/oauth2/v3/certs，RS256），
// IAP 断言用的是这一套独立的 ES256 密钥。拿错一套的现象是「签名永远验不过」。
const DefaultIAPJWKSURL = "https://www.gstatic.com/iap/verify/public_key-jwk"

// adminAuthCtxKey 是上下文键。
//
// 与 user.go 同一条理由：用独立的空结构体类型，而不是复用 node.go 的 ctxKey 枚举。
// 三套身份放进同一个键空间时，一次 iota 顺序调整就能让 AdminFrom 取到 NodeAuth。
// 不同类型的键在编译期就不可能互相取到。
type adminAuthCtxKey struct{}

// 管理员角色。与 0002_foundation.up.sql 的 CHECK 约束一一对应。
const (
	RoleOwner   = "owner"
	RoleAdmin   = "admin"
	RoleSupport = "support"
)

// AdminPermission 是 §6.2 L4 的独立权限位。
//
// 用类型化枚举而不是字符串：字符串权限名写错一个字母的后果是
// `HasPerm("admin.order.markpaid")` 恒为 false —— 而 false 在这里是**放行**还是**拒绝**
// 取决于调用点怎么写的，写成 `if !ok { deny }` 才安全，写反就静默放行。
// 枚举让拼错在编译期就失败。
type AdminPermission int

const (
	// PermMarkOrderPaid 对应 D6（手工标记订单已支付）—— api-contract §6.2 称之为
	// 「全系统最大的内部欺诈面」。DDL 默认 false，**即使团队只有一个人也不预授**。
	PermMarkOrderPaid AdminPermission = iota
	PermRefund                        // D7 退款
	PermAdjustBalance                 // D10 余额调整
	PermExportCSV                     // D14 导出
)

// AdminPerms 是四个危险权限位的快照，字段名与 admin_users 的列一一对应。
type AdminPerms struct {
	MarkOrderPaid bool
	Refund        bool
	AdjustBalance bool
	ExportCSV     bool
}

// AdminAuth 是通过鉴权的管理员身份，注入请求上下文供 handler 使用。
type AdminAuth struct {
	AdminID int64
	// Email 取自 **admin_users 那一份**，不是 IAP 断言里那一份。
	// 审计日志的 admin_email_snapshot 必须记这一份：断言里的 email 是身份提供方说的，
	// 而我们要留的证据是「本系统认为他是谁」。
	Email string
	Role  string
	Perms AdminPerms
	// IAPSubject 是断言的 sub（形如 `accounts.google.com:1234567890`）。
	// 留着是为了在审计与排障时能区分「同一个邮箱换了 Google 账号」。
	IAPSubject string
}

// Can 判断是否持有某个危险权限位。
func (a *AdminAuth) Can(p AdminPermission) bool {
	if a == nil {
		return false
	}
	switch p {
	case PermMarkOrderPaid:
		return a.Perms.MarkOrderPaid
	case PermRefund:
		return a.Perms.Refund
	case PermAdjustBalance:
		return a.Perms.AdjustBalance
	case PermExportCSV:
		return a.Perms.ExportCSV
	default:
		// 未知权限位一律拒绝。新增枚举而忘了在这里加分支时，
		// 现象必须是「这个操作谁都做不了」，不能是「谁都能做」。
		return false
	}
}

// AdminRecord 是 admin_users 的一行（只取鉴权需要的列）。
//
// 刻意不复用 dbgen.AdminUser：那个结构体带着 password_hash，
// 而管理面走 IAP，**根本不该有任何代码路径能读到密码哈希**。
// 收窄成本类型意味着「密码校验」这条路在管理面链上不存在。
type AdminRecord struct {
	ID            int64
	Email         string
	Role          string
	IAPSubject    string // NULL 时为空串
	TOTPSecretEnc []byte
	Disabled      bool
	Perms         AdminPerms
}

// AdminDirectory 是管理面鉴权所需的最小数据库能力。
// 收窄成接口而不是直接吃 *store.Store，是为了单测能塞假实现（与 node.go / user.go 同）。
type AdminDirectory interface {
	// LookupAdminByIAPEmail 按邮箱查管理员。找不到返回 pgx.ErrNoRows。
	// 邮箱大小写不敏感（admin_users_email_uk 建在 lower(email) 上）。
	LookupAdminByIAPEmail(ctx context.Context, email string) (AdminRecord, error)
	// LookupAdminByID 按主键查管理员。step-up 用 —— 见 RequireStepUp 的注释。
	LookupAdminByID(ctx context.Context, id int64) (AdminRecord, error)
}

// ErrTOTPCodeUsed 表示这个 code 在有效窗口内已经被用过（重放）。
var ErrTOTPCodeUsed = errors.New("TOTP code 已被使用")

// TOTPReplayGuard 记录已使用的 TOTP code。
type TOTPReplayGuard interface {
	// ClaimTOTPCode 独占一次 code。已被用过时返回 ErrTOTPCodeUsed。
	ClaimTOTPCode(ctx context.Context, adminID int64, codeHash []byte) error
}

// IAPKeyProvider 按 kid 提供 IAP 的验签公钥。
type IAPKeyProvider interface {
	PublicKey(ctx context.Context, kid string) (*ecdsa.PublicKey, error)
}

// AdminAuthConfig 是管理面鉴权的配置。
type AdminAuthConfig struct {
	// IAPAudience 是 IAP 断言的 aud，形如
	// `/projects/<PROJECT_NUMBER>/global/backendServices/<BACKEND_SERVICE_ID>`。
	//
	// 🔴 **空值 = 管理面整体拒绝**，不是「跳过校验」。理由见 AuthenticateAdmin。
	IAPAudience string

	// IAPIssuer 留空用 IAPIssuer 常量。留出字段只为测试，不要在生产改它。
	IAPIssuer string

	Keys   IAPKeyProvider
	DB     AdminDirectory
	Replay TOTPReplayGuard

	// TOTPKey 是解密 admin_users.totp_secret_enc 的 AES-256 密钥（32 字节），
	// 来自 Secret Manager（BP_ADMIN_TOTP_ENC_KEY）。空值 → step-up 一律失败。
	TOTPKey []byte

	Logger *slog.Logger

	// Now 可注入以便测试时间边界。留空用 time.Now。
	Now func() time.Time

	// ClockSkew 是 exp/iat 校验允许的时钟偏差。留空用 defaultClockSkew。
	ClockSkew time.Duration
}

const (
	// defaultClockSkew 是 JWT 时间校验的容差。
	//
	// 30 秒不是抄来的：Cloud Run 与 Google 前端都跑在 Google 的授时下，
	// 实际偏差在毫秒级。给 30 秒是为了容忍容器冷启动瞬间的时钟跳变，
	// 再大就等于延长了一个被窃断言的可用寿命（IAP 断言默认只活 5 分钟）。
	defaultClockSkew = 30 * time.Second

	// totpPeriod / totpDigits 是 RFC 6238 的标准参数，也是所有 Authenticator app 的默认值。
	// 改动它们意味着已经绑定的所有管理员要重新扫码。
	totpPeriod = 30 * time.Second
	totpDigits = 6

	// totpSkewSteps 是允许的时间步漂移（±1 步 = ±30 秒）。
	//
	// 与 0015 migration 里 used_totp 的清理窗口（10 分钟而不是 5 分钟）是同一件事的两端：
	// 那条注释写明「TOTP 校验允许 ±1 个时间步的漂移，按 5 分钟清会在边界上放过一次重放」。
	// 在这里放大 skew 就必须同步放大那边的清理窗口，否则重放窗口会重新打开。
	totpSkewSteps = 1
)

func (c AdminAuthConfig) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c AdminAuthConfig) skew() time.Duration {
	if c.ClockSkew > 0 {
		return c.ClockSkew
	}
	return defaultClockSkew
}

func (c AdminAuthConfig) issuer() string {
	if c.IAPIssuer != "" {
		return c.IAPIssuer
	}
	return IAPIssuer
}

func (c AdminAuthConfig) logger() *slog.Logger {
	if c.Logger != nil {
		return c.Logger
	}
	return slog.Default()
}

// adminDenied 统一构造管理面的 403。
//
// 全部走同一个 code（AUTH_PERMISSION_DENIED）与同一个 message
// （「无权访问管理面」），**不区分「断言坏了」「不是管理员」「已被禁用」**。
// 区分开等于给一个只拿到 IAP 通行权的人一台账号枚举机：
// 换一个邮箱重放一次，就能从错误文案上读出「这个邮箱是不是管理员」。
// 真正的原因写进服务端日志（reason 字段），那是我们能看到而调用方看不到的地方。
func adminDenied() *AuthError {
	return &AuthError{http.StatusForbidden, "AUTH_PERMISSION_DENIED", "无权访问管理面"}
}

// AuthenticateAdmin 校验 IAP 断言并解析出管理员身份。
//
// 返回值要么是 (auth, nil)，要么是 (nil, *AuthError) —— 与 AuthenticateNode /
// AuthenticateUser 同样的约定：把「该回几」的判断留在这里，
// 避免每个调用点各自映射一遍状态码。
func AuthenticateAdmin(ctx context.Context, cfg AdminAuthConfig, r *http.Request) (*AdminAuth, *AuthError) {
	log := cfg.logger()

	// 🔴 第一道闸：没有配置 audience 就**整体拒绝**，一个请求都不放。
	//
	// 这是本文件最重要的一行。IAP 断言头在没有 IAP 的部署形态下**可以被任意伪造** ——
	// 只要有一条路径能在「没配 audience」时跳过校验，那么
	// 「配置漏了」的现象就是「谁都能进管理面」，而且是**静默**的：
	// 后台照常打开、操作照常成功，没有任何症状。
	//
	// fail-closed 的理由就是这个：「配置漏了」的现象必须是
	// **「管理面进不去」**，不能是「谁都进得去」。前者五分钟内就会有人来报，
	// 后者可能几个月都没人发现 —— 而这中间任何一个人都能调用 D6（手工标记订单已支付）。
	//
	// 同理，这里不接受「dev 环境放行」这类例外：dev 的配置文件迟早会被复制到 staging。
	if cfg.IAPAudience == "" {
		// 固定文案，供 monitoring 建 log-based metric：管理面被整体关闭是运维事故，
		// 不是日常。ERROR 级别是刻意的 —— 它应该吵。
		log.ErrorContext(ctx, "bp_admin_plane_disabled 未配置 BP_ADMIN_IAP_AUDIENCE，管理面整体拒绝")
		return nil, adminDenied()
	}
	// 装配错误（忘了注入 Keys / DB）与「配置漏了」不同：它是我们自己的 bug，
	// 应该以 500 暴露而不是伪装成「你没权限」—— 后者会让人去查 IAP 配置，查错方向。
	// 500 同样是**关**的，不会放行。
	if cfg.Keys == nil || cfg.DB == nil {
		log.ErrorContext(ctx, "管理面鉴权装配不完整", "has_keys", cfg.Keys != nil, "has_db", cfg.DB != nil)
		return nil, &AuthError{http.StatusInternalServerError, "INTERNAL_ERROR", "内部错误"}
	}

	raw := strings.TrimSpace(r.Header.Get(IAPAssertionHeader))
	if raw == "" {
		log.WarnContext(ctx, "管理面请求缺少 IAP 断言", "path", RedactPath(r.URL.Path))
		return nil, adminDenied()
	}

	claims, err := verifyIAPAssertion(ctx, cfg, raw)
	if err != nil {
		// 伪造断言是**攻击信号**，不是日常故障：直连 *.run.app 的人只要试一次就会落在这里。
		// 记 WARN 并带上原因，但不把原因回给调用方（见 adminDenied）。
		log.WarnContext(ctx, "IAP 断言校验失败", "reason", err.Error(), "path", RedactPath(r.URL.Path))
		return nil, adminDenied()
	}

	email := normalizeIAPEmail(claims.Email)
	if email == "" {
		log.WarnContext(ctx, "IAP 断言缺少 email 声明", "sub", claims.Subject)
		return nil, adminDenied()
	}

	// 第二道闸：IAP 只证明「你是某个 Google 身份」。
	// 「这个 Google 身份是不是本系统的管理员」是另一个问题，答案只在 admin_users 里。
	// 少了这一步，任何被 IAP 放行的人（IAP 的访问策略可能是整个 workspace 域）
	// 都会变成管理员。
	rec, dbErr := cfg.DB.LookupAdminByIAPEmail(ctx, email)
	if dbErr != nil {
		if errors.Is(dbErr, pgx.ErrNoRows) {
			log.WarnContext(ctx, "IAP 身份不是管理员", "email", email)
			return nil, adminDenied()
		}
		log.ErrorContext(ctx, "管理员查询失败", "err", dbErr)
		return nil, &AuthError{http.StatusInternalServerError, "INTERNAL_ERROR", "内部错误"}
	}

	if authErr := checkAdminUsable(ctx, log, rec, claims.Subject); authErr != nil {
		return nil, authErr
	}

	return &AdminAuth{
		AdminID:    rec.ID,
		Email:      rec.Email,
		Role:       rec.Role,
		Perms:      rec.Perms,
		IAPSubject: claims.Subject,
	}, nil
}

// checkAdminUsable 做「这条 admin_users 记录现在还能用吗」的判断。
//
// 抽出来是因为 step-up 要**再跑一遍**（见 RequireStepUp）：
// 两处各写一遍的后果是某天有人在鉴权路径上加了一条禁用判断，
// 而 step-by-step 路径上没有 —— 于是一个刚被禁用的管理员仍然能完成危险操作。
func checkAdminUsable(ctx context.Context, log *slog.Logger, rec AdminRecord, iapSubject string) *AuthError {
	if rec.Disabled {
		// 归 403 而不是 401：被禁用是一个**终态判断**，凭据本身没问题。
		// 回 401 会让浏览器带着 IAP 反复重试，管理员看到的是无限跳转而不是「你被停用了」。
		log.WarnContext(ctx, "已禁用的管理员尝试访问", "admin_id", rec.ID, "email", rec.Email)
		return adminDenied()
	}
	// iap_subject 是可空列：第一次登录时可能还没绑。已绑的必须对得上 ——
	// 邮箱可以被回收（workspace 删号后同名重建），而 sub 不会。
	// 只在**已绑定**时校验，避免让「还没绑」变成一道进不去的门。
	if rec.IAPSubject != "" && iapSubject != "" &&
		subtle.ConstantTimeCompare([]byte(rec.IAPSubject), []byte(iapSubject)) != 1 {
		log.WarnContext(ctx, "IAP subject 与绑定值不符", "admin_id", rec.ID, "email", rec.Email)
		return adminDenied()
	}
	return nil
}

// WithAdmin 把管理员身份放进上下文。形状对齐 WithUser / WithNodeAuth。
func WithAdmin(ctx context.Context, a *AdminAuth) context.Context {
	return context.WithValue(ctx, adminAuthCtxKey{}, a)
}

// AdminFrom 从上下文取出管理员身份。handler 里用。
//
// ok 为 false 表示这条路由**没挂**管理面鉴权中间件（装配错误），
// 而不是「未登录」—— 未通过鉴权的请求根本到不了 handler。
// handler 拿到 false 时应当返回 500 而不是 403，否则装配错误会伪装成权限问题。
func AdminFrom(ctx context.Context) (*AdminAuth, bool) {
	a, ok := ctx.Value(adminAuthCtxKey{}).(*AdminAuth)
	return a, ok
}

// RequireAdmin 是 net/http 形态的中间件：鉴权失败直接写响应，成功则注入上下文。
//
// 与 RequireUser 同样的理由：让「按路由分组挂载」与「按 operationID 挂载」
// 两种装配方式都能直接复用 AuthenticateAdmin，不必各自抄一遍错误映射。
func RequireAdmin(cfg AdminAuthConfig) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			auth, authErr := AuthenticateAdmin(r.Context(), cfg, r)
			if authErr != nil {
				WriteAuthError(w, r, authErr)
				return
			}
			next.ServeHTTP(w, r.WithContext(WithAdmin(r.Context(), auth)))
		})
	}
}

// ---- TOTP step-up（api-contract §6.2 L3）----

// RequireStepUp 校验一次当次 TOTP，并把该 code 记为已用（防重放）。
//
// 用法（handler 侧）：
//
//	if authErr := cfg.RequireStepUp(ctx, r.Header.Get(mw.TOTPCodeHeader)); authErr != nil {
//	        mw.WriteAuthError(w, r, authErr)
//	        return nil, nil
//	}
//
// **为什么是 AdminAuthConfig 的方法，而不是包级的 RequireStepUp(ctx, code)**：
// 校验要解密 secret、要写 used_totp，也就是要 DB 句柄和密钥。
// 包级两参函数只能把这些东西藏进 context 传过来 —— 而往 ctx 里塞依赖
// 会让「这条路径用的是哪个库」在编译期不可见，测试也只能靠猜。
// handler 已经持有它自己的依赖结构体，多存一个 cfg 是零成本的。
//
// **为什么不做成中间件**：step-up 是**按操作**的（§6.2 只对 D3 D5 D6 D10 D15 D16 要求），
// 而且 L1（确认串）/L2（原因）/L4（权限位）都在 handler 里判 ——
// 把 L3 单独提到中间件会让四层强制散落在两处，漏一层不会有任何编译期信号。
func (c AdminAuthConfig) RequireStepUp(ctx context.Context, code string) *AuthError {
	log := c.logger()

	admin, ok := AdminFrom(ctx)
	if !ok || admin == nil {
		// 装配错误：这条路由没挂管理面鉴权，却在 handler 里要求 step-up。
		// 500 而不是 403 —— 见 AdminFrom 的注释。
		log.ErrorContext(ctx, "RequireStepUp 在没有管理员身份的上下文里被调用")
		return &AuthError{http.StatusInternalServerError, "INTERNAL_ERROR", "内部错误"}
	}
	if c.Replay == nil || len(c.TOTPKey) == 0 {
		// 与 IAPAudience 同一条纪律：缺配置的现象必须是「危险操作做不了」，
		// 不能是「危险操作不需要 TOTP」。
		log.ErrorContext(ctx, "bp_admin_stepup_unconfigured step-up 依赖缺失，危险操作一律拒绝",
			"has_replay_guard", c.Replay != nil, "totp_key_len", len(c.TOTPKey))
		return &AuthError{http.StatusForbidden, "AUTH_TOTP_REQUIRED", "该操作需要二次验证"}
	}

	code = strings.TrimSpace(code)
	if code == "" {
		// 缺头与错码要分开：前端拿到 AUTH_TOTP_REQUIRED 才知道要弹输入框，
		// 拿到 AUTH_TOTP_INVALID 是「你输错了，重来」。合并成一个码会让前端
		// 在用户还没被要求输码时就显示「验证码错误」。
		return &AuthError{http.StatusForbidden, "AUTH_TOTP_REQUIRED", "该操作需要二次验证"}
	}
	// 形态不合法直接拒，**不解密也不查库** —— 与节点/用户面同一纪律：
	// 省掉一次 AES 解密与一次数据库往返，也不给探测留时序差异。
	if !plausibleTOTPCode(code) {
		return totpInvalid()
	}

	// 重新查一次 admin_users，**不复用 ctx 里的 AdminAuth**。两个理由：
	//  1. secret 必须现取现用。把解密后的 TOTP secret 挂在 AdminAuth 上
	//     等于让它在整个请求生命周期里随手可得（日志、panic dump、
	//     任何一个打印 ctx 的地方）。
	//  2. 顺手拿到最新的 disabled 状态：一个在会话中途被禁用的管理员
	//     必须做不成危险操作，而 AdminAuth 是请求开始时的快照。
	rec, err := c.DB.LookupAdminByID(ctx, admin.AdminID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			log.WarnContext(ctx, "step-up 时管理员已不存在", "admin_id", admin.AdminID)
			return adminDenied()
		}
		log.ErrorContext(ctx, "step-up 查询管理员失败", "err", err)
		return &AuthError{http.StatusInternalServerError, "INTERNAL_ERROR", "内部错误"}
	}
	if authErr := checkAdminUsable(ctx, log, rec, admin.IAPSubject); authErr != nil {
		return authErr
	}

	secret, err := decryptTOTPSecret(c.TOTPKey, rec.TOTPSecretEnc)
	if err != nil {
		// 解不开是配置/数据事故（密钥轮换漏了、密文被截断），不是用户错误。
		// 但对调用方仍然只能是「拒绝」—— 500 会把「我们的密钥配错了」
		// 变成一次可被观察的探测信号。这里用 ERROR 日志暴露给我们自己。
		log.ErrorContext(ctx, "解密 TOTP secret 失败", "admin_id", rec.ID, "err", err)
		return totpInvalid()
	}

	if !verifyTOTPCode(secret, code, c.now()) {
		log.WarnContext(ctx, "TOTP 校验失败", "admin_id", rec.ID)
		return totpInvalid()
	}

	// 🔴 顺序不能反：**先验对，再占用**。
	//
	// 反过来（先占用再验）会让任何人拿一串随机 6 位数把 used_totp 灌满，
	// 而且能把管理员**真正**要用的那个 code 提前占掉 —— 一个免费的拒绝服务。
	//
	// 占用交给数据库的主键去拒（0015 migration 的 used_totp PK(admin_user_id, code_hash)），
	// 不是应用层 SELECT-then-INSERT：后者在并发重放下两个请求会双双通过，
	// 而并发重放正是自动化攻击的默认形态。
	//
	// 另注：这条写入**不在**业务事务里，是独立的一次写。所以业务操作失败回滚时，
	// code 仍然算用过了。这是刻意的：防重放保护的是 code 本身，
	// 「操作失败所以码还能再用一次」等于给重放开了一扇按需触发的门。
	if err := c.Replay.ClaimTOTPCode(ctx, rec.ID, c.totpCodeHash(rec.ID, code)); err != nil {
		if errors.Is(err, ErrTOTPCodeUsed) {
			log.WarnContext(ctx, "TOTP code 重放被拒", "admin_id", rec.ID)
			return totpInvalid()
		}
		// 写不进去就必须拒绝：放行等于在这一刻关闭了防重放。
		// used_totp 是普通表（不是 rate_limit 那张 UNLOGGED 表），
		// 它写不进去时数据库大概率整体不可用，业务操作本来也做不成。
		log.ErrorContext(ctx, "记录已用 TOTP code 失败，拒绝本次 step-up", "admin_id", rec.ID, "err", err)
		return &AuthError{http.StatusInternalServerError, "INTERNAL_ERROR", "内部错误"}
	}
	return nil
}

// totpInvalid 统一构造「码不对或已用过」。
//
// 两种情况必须**不可区分**：能区分就等于告诉重放者「这个码曾经是对的」，
// 而 TOTP 的取值空间只有 10^6，任何一点额外信息都值钱。
// api-contract §2.3 的 AUTH_TOTP_INVALID 描述本身就写着「TOTP 错误或已被使用过」。
func totpInvalid() *AuthError {
	return &AuthError{http.StatusForbidden, "AUTH_TOTP_INVALID", "二次验证失败"}
}

// plausibleTOTPCode 要求恰好 6 位十进制数字。
func plausibleTOTPCode(s string) bool {
	if len(s) != totpDigits {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// totpCodeHash 计算写进 used_totp.code_hash 的值。
//
// 0015 migration 的注释写明**不存明文 code**：「一份历史明文表配上时钟
// 就能反推 secret 的对齐关系」。但光做 sha256(code) 也不够 ——
// 6 位数字只有 10^6 种，一张彩虹表几毫秒就能建好，等于没哈希。
// 所以用密钥化的 HMAC，并把 admin_id 拌进去（同一个码在不同管理员名下哈希不同）。
//
// 密钥不直接用 TOTPKey，而是先派生一个子密钥：同一把密钥同时用于
// AES-GCM 解密和 HMAC 是**密钥用途混用**，虽然在这里不构成已知攻击，
// 但域分隔的成本只有一次 HMAC，没有理由省。
func (c AdminAuthConfig) totpCodeHash(adminID int64, code string) []byte {
	sub := hmac.New(sha256.New, c.TOTPKey)
	sub.Write([]byte(totpHashDomain))
	m := hmac.New(sha256.New, sub.Sum(nil))
	var idBuf [8]byte
	binary.BigEndian.PutUint64(idBuf[:], uint64(adminID))
	m.Write(idBuf[:])
	m.Write([]byte(code))
	return m.Sum(nil)
}

// totpHashDomain 是 code_hash 的域分隔串。**改它等于把所有在途的重放记录作废**
// （旧哈希再也撞不上新哈希），窗口内的 code 会全部变回可重放一次。
const totpHashDomain = "bp/used_totp/v1"

// decryptTOTPSecret 解出 base32 形态的 TOTP 密钥。
//
// 密文形态（0002_foundation.up.sql 注释「AES-256-GCM，密钥在 Secret Manager」）：
//
//	nonce(12) || ciphertext || tag(16)
//
// 明文是 **base32(RFC 4648, 大写, 无填充) 字符串**，不是原始字节 ——
// 那正是 `otpauth://` URI 里的形态，签发侧（管理员创建/reset-totp）
// 直接把展示给管理员的那串存下来即可，两侧不必各自做一次转换。
// ⚠️ 签发侧尚未实现（admin_users 目前靠人工 seed），这条约定由本函数单方面定下，
// 实现签发时必须回来对齐，否则现象是「所有管理员的码都不对」。
func decryptTOTPSecret(key, enc []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("TOTP 密钥不可用: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("构造 GCM 失败: %w", err)
	}
	if len(enc) <= gcm.NonceSize() {
		return nil, errors.New("totp_secret_enc 长度不足，密文被截断？")
	}
	plain, err := gcm.Open(nil, enc[:gcm.NonceSize()], enc[gcm.NonceSize():], nil)
	if err != nil {
		return nil, errors.New("GCM 认证失败：密钥不对或密文被改动")
	}
	return decodeBase32Secret(string(plain))
}

// decodeBase32Secret 宽容地解 base32：容忍小写、空格与 `=` 填充。
//
// 宽容是有理由的：这串东西的来源是人（从 Authenticator 或密码管理器里拷出来的），
// 而 Google Authenticator 展示 secret 时用的就是带空格的小写分组。
// 严格解析的现象是「肉眼一模一样的两串，一个能用一个不能用」。
func decodeBase32Secret(s string) ([]byte, error) {
	s = strings.ToUpper(strings.NewReplacer(" ", "", "-", "", "=", "").Replace(strings.TrimSpace(s)))
	if s == "" {
		return nil, errors.New("TOTP secret 为空")
	}
	b, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("TOTP secret 不是合法 base32: %w", err)
	}
	return b, nil
}

// verifyTOTPCode 按 RFC 6238 校验，允许 ±totpSkewSteps 个时间步。
func verifyTOTPCode(secret []byte, code string, now time.Time) bool {
	step := now.Unix() / int64(totpPeriod/time.Second)
	ok := false
	for i := -totpSkewSteps; i <= totpSkewSteps; i++ {
		want := totpAt(secret, step+int64(i))
		// 常数时间比较，且**不提前 return** —— 提前退出会把「命中的是哪个时间窗」
		// 变成一个可测量的时序差异，那等于泄漏了服务端时钟与 secret 的相对偏移。
		if subtle.ConstantTimeCompare([]byte(want), []byte(code)) == 1 {
			ok = true
		}
	}
	return ok
}

// totpAt 算出某个时间步的 6 位码（RFC 4226 动态截断）。
func totpAt(secret []byte, step int64) string {
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], uint64(step))
	// HMAC-SHA1 是 RFC 6238 的默认算法，也是所有 Authenticator app 的默认值。
	// 这里的 SHA-1 不承担抗碰撞职责（HMAC 的安全性不依赖底层哈希抗碰撞），
	// 换成 SHA-256 只会让所有已绑定的管理员全部失效。
	m := hmac.New(sha1.New, secret)
	m.Write(buf[:])
	sum := m.Sum(nil)
	off := sum[len(sum)-1] & 0x0f
	v := binary.BigEndian.Uint32(sum[off:off+4]) & 0x7fffffff
	return fmt.Sprintf("%0*d", totpDigits, v%1_000_000)
}

// ---- IAP 断言校验 ----

// iapClaims 是我们**实际使用**的声明。
//
// 只解这几个字段而不是解成 map：多余的声明（hd、google.*）不参与任何判断，
// 解出来只会诱使后来的人拿它们做鉴权决定。
type iapClaims struct {
	Issuer   string `json:"iss"`
	Audience string `json:"aud"`
	Subject  string `json:"sub"`
	Email    string `json:"email"`
	Expiry   int64  `json:"exp"`
	IssuedAt int64  `json:"iat"`
}

type jwtHeader struct {
	Alg string `json:"alg"`
	Kid string `json:"kid"`
}

// verifyIAPAssertion 验签并校验 iss / aud / exp / iat。
//
// 返回的 error 只进服务端日志，**不回给调用方**（见 adminDenied）。
func verifyIAPAssertion(ctx context.Context, cfg AdminAuthConfig, raw string) (*iapClaims, error) {
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		return nil, errors.New("不是三段式 JWT")
	}

	headerJSON, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, fmt.Errorf("header 不是 base64url: %w", err)
	}
	var hdr jwtHeader
	if err := json.Unmarshal(headerJSON, &hdr); err != nil {
		return nil, fmt.Errorf("header 不是 JSON: %w", err)
	}

	// 🔴 算法白名单：**只接受 ES256**，而且是在取密钥之前就判。
	//
	// 这一行挡的是 JWT 最经典的两个漏洞：
	//  1. `alg: none` —— 无签名 token，不判就是「任何人可自签任何身份」；
	//  2. **算法混淆** —— 攻击者把 alg 改成 HS256，让服务端拿公钥当 HMAC 密钥去验，
	//     而 IAP 的公钥是**公开的**（DefaultIAPJWKSURL 无需鉴权即可下载）。
	//
	// 「按 header 里的 alg 去选验签方式」这个写法本身就是漏洞的成因，
	// 所以这里不是「选」，是「不等于 ES256 就退出」。
	if hdr.Alg != "ES256" {
		return nil, fmt.Errorf("不接受的签名算法 %q（只接受 ES256）", hdr.Alg)
	}
	if hdr.Kid == "" {
		return nil, errors.New("header 缺少 kid")
	}

	pub, err := cfg.Keys.PublicKey(ctx, hdr.Kid)
	if err != nil {
		return nil, fmt.Errorf("取验签公钥失败 (kid=%s): %w", hdr.Kid, err)
	}

	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return nil, fmt.Errorf("签名不是 base64url: %w", err)
	}
	// ES256 的签名是 r||s，各 32 字节的定长拼接（JWS 的 P1363 形态，不是 DER）。
	// 长度不对就直接退出，免得 SetBytes 把一个 DER 编码悄悄解释成一对巨大的整数。
	if len(sig) != 64 {
		return nil, fmt.Errorf("ES256 签名长度应为 64，实为 %d", len(sig))
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	r := new(big.Int).SetBytes(sig[:32])
	s := new(big.Int).SetBytes(sig[32:])
	if !ecdsa.Verify(pub, digest[:], r, s) {
		return nil, errors.New("签名验证失败")
	}

	payloadJSON, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, fmt.Errorf("payload 不是 base64url: %w", err)
	}
	var c iapClaims
	if err := json.Unmarshal(payloadJSON, &c); err != nil {
		return nil, fmt.Errorf("payload 不是 JSON: %w", err)
	}

	// aud 用常数时间比较：它不是秘密，但比较的另一端来自调用方，
	// 而这条路径上的其它比较也都是常数时间的 —— 保持一致比逐个论证便宜。
	if subtle.ConstantTimeCompare([]byte(c.Audience), []byte(cfg.IAPAudience)) != 1 {
		// 🔴 aud 校验是**这个断言是发给我们的**的唯一证据。少了它，
		// 任何一个同样受 IAP 保护的服务（哪怕是别人项目里的）签出来的断言
		// 都能拿到这里用 —— 而那些服务的准入策略与我们无关。
		return nil, errors.New("aud 不匹配")
	}
	if c.Issuer != cfg.issuer() {
		return nil, fmt.Errorf("iss 不匹配: %q", c.Issuer)
	}

	now := cfg.now()
	skew := cfg.skew()
	if c.Expiry == 0 {
		// 没有 exp 的 token 永不过期。缺字段必须当作失败，不能当作「不限制」。
		return nil, errors.New("缺少 exp")
	}
	if !time.Unix(c.Expiry, 0).After(now.Add(-skew)) {
		return nil, errors.New("断言已过期")
	}
	// iat 在未来太多说明对方时钟不对或 token 是伪造的；容忍 skew 之内。
	if c.IssuedAt != 0 && time.Unix(c.IssuedAt, 0).After(now.Add(skew)) {
		return nil, errors.New("iat 在未来")
	}
	return &c, nil
}

// normalizeIAPEmail 把断言里的 email 归一成可以拿去查库的形态。
//
// IAP 的 email 声明**可能带身份提供方前缀**（Google 文档里的示例是
// `accounts.google.com:example@gmail.com`，与 X-Goog-Authenticated-User-Email 头一致），
// 也可能是裸邮箱。两种都要能查得到，否则会出现「换了个 IAP 配置就全员进不去」。
//
// 切在**最后一个冒号**上是安全的：邮箱本地部分不允许出现裸冒号（RFC 5321），
// 所以冒号只可能来自前缀。
func normalizeIAPEmail(raw string) string {
	e := strings.TrimSpace(raw)
	if i := strings.LastIndex(e, ":"); i >= 0 {
		e = e[i+1:]
	}
	// 统一小写：admin_users_email_uk 建在 lower(email) 上，查询也按 lower 比。
	return strings.ToLower(strings.TrimSpace(e))
}

// ---- IAP 公钥集合（JWKS）----

// IAPKeySet 缓存 IAP 的验签公钥。
//
// 为什么要缓存：每个管理面请求都去 gstatic 拉一次 JWKS 会给每个请求加上
// 一次公网往返，而且在 gstatic 抖动时整个后台不可用。
// 为什么不能只缓存：Google 会轮换密钥，永久缓存的现象是「某天所有人突然进不去」。
type IAPKeySet struct {
	// URL 留空用 DefaultIAPJWKSURL。
	URL string
	// HTTP 留空用一个带超时的默认客户端。
	HTTP *http.Client
	// TTL 留空用 defaultJWKSTTL。
	TTL time.Duration
	// Now 可注入以便测试。留空用 time.Now。
	Now func() time.Time

	mu        sync.Mutex
	keys      map[string]*ecdsa.PublicKey
	fetchedAt time.Time
	// lastMiss 记录上一次「缓存新鲜但 kid 未命中」触发的强制刷新时刻，用于限速 —— 见 PublicKey。
	lastMiss time.Time
	// lastFetch 记录上一次**尝试**拉取 JWKS 的时刻（成功与否都更新），用于限速 —— 见 PublicKey。
	//
	// 记「尝试」而不是「成功」是关键：只记成功的话，gstatic 一旦不可达，
	// fetchedAt 永远不前进 → 缓存永远算过期 → 每个请求都去拉一次，
	// 我们就成了对 gstatic 的重试风暴。与 GoogleJWKS.refreshLocked 同一条纪律。
	lastFetch time.Time
}

const (
	// defaultJWKSTTL 是公钥缓存寿命。
	//
	// 1 小时：Google 不公布轮换周期，但未命中的 kid 会立刻触发一次强制刷新
	// （见 PublicKey），所以 TTL 只影响「旧密钥被撤销后我们还认多久」，
	// 不影响新密钥的接纳速度。
	defaultJWKSTTL = time.Hour

	// jwksRefetchInterval 是两次**拉取尝试**之间的最小间隔。
	//
	// 没有这道闸的话，任何人用一个随机 kid 连发请求就能让我们
	// 对 gstatic 发起等量的出站请求 —— 一个反射式放大器，而且是我们付账。
	jwksRefetchInterval = time.Minute

	// jwksMaxBytes 是响应体上限。远端不受我们控制，不能无上限地读进内存。
	jwksMaxBytes = 1 << 20
)

// NewIAPKeySet 构造一个用默认参数的公钥集合。
func NewIAPKeySet() *IAPKeySet {
	return &IAPKeySet{}
}

func (s *IAPKeySet) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *IAPKeySet) url() string {
	if s.URL != "" {
		return s.URL
	}
	return DefaultIAPJWKSURL
}

func (s *IAPKeySet) ttl() time.Duration {
	if s.TTL > 0 {
		return s.TTL
	}
	return defaultJWKSTTL
}

func (s *IAPKeySet) client() *http.Client {
	if s.HTTP != nil {
		return s.HTTP
	}
	// 超时必须有：没有超时的出站请求会把 Cloud Run 的请求线程挂死在一个
	// 我们无法控制的远端上，表现为整个后台变慢而不是「验签失败」。
	return &http.Client{Timeout: 5 * time.Second}
}

// PublicKey 按 kid 取公钥，必要时刷新缓存。
func (s *IAPKeySet) PublicKey(ctx context.Context, kid string) (*ecdsa.PublicKey, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.now()
	fresh := s.keys != nil && now.Sub(s.fetchedAt) <= s.ttl()
	switch {
	case fresh:
		if k, ok := s.keys[kid]; ok {
			return k, nil
		}
		// 缓存新鲜但没有这个 kid：可能是刚轮换的新密钥，也可能是伪造的 kid。
		// 强制刷新一次以尽快接纳新密钥（这正是「TTL 不影响新密钥接纳速度」的来源），
		// 但要限速（见 jwksRefetchInterval）。
		if now.Sub(s.lastMiss) < jwksRefetchInterval {
			return nil, fmt.Errorf("未知 kid %q（刷新被限速）", kid)
		}
		s.lastMiss = now

	case now.Sub(s.lastFetch) < jwksRefetchInterval:
		// 🔴 缓存过期（或从未拉取成功）这条路径原本**一次节流都不过**。
		//
		// 于是 gstatic 一抖，fetchedAt 就永远不再前进，每个进来的请求都变成一次出站请求 ——
		// 与 lastMiss 挡的是同一个反射式放大器，只是触发条件从「随机 kid」
		// 换成了「远端不可达」。而管理面在公网上（--ingress=all），谁都能敲。
		if k, ok := s.keys[kid]; ok {
			// 缓存过期但 kid 命中：用略旧的公钥，不因为「该刷新了」就把请求挡掉。
			// 公钥过期 ≠ 失效（Google 是「先公布新的，旧的再挂一段时间」），
			// 而 exp 仍然在管住 token 寿命。
			return k, nil
		}
		return nil, fmt.Errorf("未知 kid %q（JWKS 刚拉取过，刷新被限速）", kid)
	}

	// 先记「尝试过」再发请求 —— 见 lastFetch 的注释。
	s.lastFetch = now

	keys, err := fetchJWKS(ctx, s.client(), s.url())
	if err != nil {
		// 拉取失败时**不回退到旧缓存的相反面**：旧缓存里有就用旧的（下面的查找），
		// 没有就报错。用旧密钥继续验签是可以接受的降级 ——
		// 它只会让「已被撤销的密钥」多活一会儿，而 exp 仍然在管住 token 寿命。
		if s.keys != nil {
			if k, ok := s.keys[kid]; ok {
				return k, nil
			}
		}
		return nil, fmt.Errorf("拉取 JWKS 失败: %w", err)
	}
	s.keys = keys
	s.fetchedAt = now

	k, ok := keys[kid]
	if !ok {
		return nil, fmt.Errorf("JWKS 里没有 kid %q", kid)
	}
	return k, nil
}

// jwk 是 JWKS 里的一把 EC 公钥。IAP 只发 EC P-256。
type jwk struct {
	Kty string `json:"kty"`
	Crv string `json:"crv"`
	Kid string `json:"kid"`
	X   string `json:"x"`
	Y   string `json:"y"`
	Alg string `json:"alg"`
}

func fetchJWKS(ctx context.Context, hc *http.Client, url string) (map[string]*ecdsa.PublicKey, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("JWKS 返回 %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, jwksMaxBytes))
	if err != nil {
		return nil, err
	}

	var set struct {
		Keys []jwk `json:"keys"`
	}
	if err := json.Unmarshal(body, &set); err != nil {
		return nil, fmt.Errorf("JWKS 不是合法 JSON: %w", err)
	}
	out := make(map[string]*ecdsa.PublicKey, len(set.Keys))
	for _, k := range set.Keys {
		// 只收 P-256 的 EC 键。混进来的其它类型（RSA / 未知曲线）直接跳过，
		// 而不是报错整包作废 —— Google 将来往同一个 JWKS 里加新类型时
		// 不应该让我们的管理面全线失败。
		if k.Kty != "EC" || k.Crv != "P-256" || k.Kid == "" {
			continue
		}
		pub, err := ecPublicKey(k.X, k.Y)
		if err != nil {
			continue
		}
		out[k.Kid] = pub
	}
	if len(out) == 0 {
		return nil, errors.New("JWKS 里没有可用的 P-256 公钥")
	}
	return out, nil
}

func ecPublicKey(xb64, yb64 string) (*ecdsa.PublicKey, error) {
	x, err := base64.RawURLEncoding.DecodeString(xb64)
	if err != nil {
		return nil, err
	}
	y, err := base64.RawURLEncoding.DecodeString(yb64)
	if err != nil {
		return nil, err
	}
	pub := &ecdsa.PublicKey{
		Curve: elliptic.P256(),
		X:     new(big.Int).SetBytes(x),
		Y:     new(big.Int).SetBytes(y),
	}
	// 不在曲线上的「公钥」必须拒绝：拿它去验签的行为在 crypto/ecdsa 里是未定义的，
	// 而这份数据来自网络。
	if !pub.Curve.IsOnCurve(pub.X, pub.Y) {
		return nil, errors.New("点不在 P-256 曲线上")
	}
	return pub, nil
}

// ---- Postgres 实现 ----

// PgAdminStore 用 dbgen.DBTX（*pgxpool.Pool 与 pgx.Tx 都满足）实现
// AdminDirectory 与 TOTPReplayGuard。
//
// **为什么是手写 SQL 而不是 sqlc**：本轮不允许改 api/db/gen/。
// admin_users 与 used_totp 目前在 db/queries/ 下没有任何查询，
// 生成侧也就没有对应的方法。
// TODO(P2): 把下面三条查询搬进 db/queries/admins.sql 并重新 `make gen-db`，
// 然后本类型改为薄封装。手写 SQL 的风险是列改名后**编译期没有信号**，
// 只有运行时报错 —— 而运行时报错的现象是「所有管理员进不去」。
type PgAdminStore struct {
	DB dbgen.DBTX
}

var (
	_ AdminDirectory  = (*PgAdminStore)(nil)
	_ TOTPReplayGuard = (*PgAdminStore)(nil)
)

// adminSelectColumns 是两条查询共用的列表达式，避免两处漂移。
const adminSelectColumns = `id, email, role, coalesce(iap_subject, ''), totp_secret_enc,
	       disabled_at IS NOT NULL,
	       perm_mark_order_paid, perm_refund, perm_adjust_balance, perm_export_csv`

func (s *PgAdminStore) LookupAdminByIAPEmail(ctx context.Context, email string) (AdminRecord, error) {
	// 按 lower(email) 比，命中 admin_users_email_uk 这个函数索引。
	// 直接写 `email = $1` 会全表扫，而且大小写不同就查不到。
	return scanAdmin(s.DB.QueryRow(ctx,
		`SELECT `+adminSelectColumns+` FROM admin_users WHERE lower(email) = lower($1)`, email))
}

func (s *PgAdminStore) LookupAdminByID(ctx context.Context, id int64) (AdminRecord, error) {
	return scanAdmin(s.DB.QueryRow(ctx,
		`SELECT `+adminSelectColumns+` FROM admin_users WHERE id = $1`, id))
}

func scanAdmin(row pgx.Row) (AdminRecord, error) {
	var a AdminRecord
	err := row.Scan(&a.ID, &a.Email, &a.Role, &a.IAPSubject, &a.TOTPSecretEnc, &a.Disabled,
		&a.Perms.MarkOrderPaid, &a.Perms.Refund, &a.Perms.AdjustBalance, &a.Perms.ExportCSV)
	if err != nil {
		return AdminRecord{}, err
	}
	return a, nil
}

// ClaimTOTPCode 把 code 写进 used_totp。撞主键即重放。
//
// **不写 ON CONFLICT DO NOTHING** —— 那样冲突会变成「影响 0 行」的成功，
// 而调用方要靠返回值区分。让它报错，然后在这里翻译成 ErrTOTPCodeUsed。
func (s *PgAdminStore) ClaimTOTPCode(ctx context.Context, adminID int64, codeHash []byte) error {
	_, err := s.DB.Exec(ctx,
		`INSERT INTO used_totp (admin_user_id, code_hash) VALUES ($1, $2)`, adminID, codeHash)
	if err != nil {
		var pgErr *pgconn.PgError
		// 23505 = unique_violation。主键 (admin_user_id, code_hash) 撞了 = 同一个码用第二次。
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return ErrTOTPCodeUsed
		}
		return err
	}
	return nil
}

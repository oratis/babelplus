package middleware

import (
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/oratis/babelplus/api/internal/httpx"
)

// ---- 内部面鉴权：/internal/tasks/* 的 Google OIDC ----
//
// 调用方只有两类：六条 Cloud Scheduler 与一条 Cloud Tasks 入账队列
// （roadmap.md §4.2）。凭据是 **Google 签发的 OIDC ID token**，
// 校验 `iss` / `aud` / `exp` / `email`（api-contract.md §7、ADR 0006 §10.2）。
//
// 🔴 为什么这条链必须独立存在，而不是「复用用户面再加个 if」：
// ADR 0006 §10.3 第 1 条。这批端点没有人类界面，路径也不出现在任何前端代码里，
// 一个无鉴权的 `POST /internal/tasks/traffic-reset` 可以被任何人用来清空全站流量计数。
// 与 node.go / user.go 一样，本文件**不与另外两套鉴权共用任何代码路径**。
//
// 🔴 **保护它的是 OIDC 校验，不是路径保密**（ADR 0006 §10.3 第 3 条）：
// `/internal/tasks/*` 与公网端点在同一个 Cloud Run service 上（「不要常驻 worker」的直接后果），
// 路径不是秘密，token 才是。同理，**禁止**把 `X-Cloud-Trace-Context`、
// `X-CloudScheduler`、`X-Appengine-*` 之类可伪造的头当作判据 —— 本文件一个都不读。
//
// 失败一律 **403**（不是 401）：openapi.yaml 给这九个 operation 声明的响应只有
// 200 / 403 / 500 三种，401 不在契约里；ADR 0006 §10.2 的表也写死了 403。

// googleJWKSURL 是 Google OIDC 的公钥集地址。
//
// 注意不是 `https://www.googleapis.com/oauth2/v1/certs`（那份是 X.509 证书形态的旧端点）。
// v3 是 JWK 形态，本文件按 JWK 解析。
const googleJWKSURL = "https://www.googleapis.com/oauth2/v3/certs"

// googleIssuers 是可接受的 `iss` 取值。
//
// api-contract.md §7 只写了 `https://accounts.google.com`，这里**多接受一个无 scheme 的形式**。
// 理由：Google 自己的文档与官方校验库长期同时承认这两个字符串，它们指的是同一个签发方、
// 同一套 JWKS —— 也就是说多接受一个不扩大信任面（签名仍然必须由同一组公钥验过）。
// 反过来只认带 scheme 的那个，则一旦 Google 换回旧形式，现象是**六条定时任务同时 403**，
// 而 expire-check 停摆意味着到期用户永远不会从节点列表消失（api-contract.md §7）。
var googleIssuers = []string{"https://accounts.google.com", "accounts.google.com"}

// 只接受 RS256。
//
// 🔴 这是 JWT 校验里最经典的一处漏洞：不校验 alg 就按 header 说的算法去验，
// 于是 `alg: none`（无签名）与 `alg: HS256`（把 RSA 公钥当 HMAC 密钥，而公钥是公开的）
// 都能伪造出「通过校验」的 token。算法必须由**我们**指定，不能由 token 自己声明。
const requiredIDTokenAlg = "RS256"

// maxIDTokenBytes 是 ID token 的长度上限。
//
// 先量长度再解析：base64 解码与 RSA 验签都要花 CPU，而这个端点在鉴权成功之前
// 对任何人开放（路径不是秘密）。Google 的 ID token 在 1 KB 量级，8 KB 有充足余量。
const maxIDTokenBytes = 8192

// internalDenyMessage 是**唯一**会返回给调用方的拒绝文案。
//
// 真实原因（aud 不符 / 不在白名单 / 签名错……）只进日志，不进响应体。
// 内部面的调用方是 Google 的基础设施，它不会读 message 做分支，
// 区分错误对它零收益；而对一个正在试探这个端点的人，
// 「你的 token 签名是好的，只是 email 不在白名单」这句话确认了一大半信息。
// 排障靠 request_id 回捞日志，不靠响应体。
const internalDenyMessage = "内部端点拒绝该调用方"

// internalCallerCtxKey 是上下文键。
//
// 与 user.go 同理：用独立的空结构体类型，而不是复用 node.go 的 ctxKey 枚举。
// 不同类型的键在编译期就不可能互相取到 —— 一次 iota 顺序调整不该让
// NodeAuthFrom 取到 InternalCaller。
type internalCallerCtxKey struct{}

// InternalCaller 是通过校验的内部调用方身份，注入请求上下文供任务 handler 写审计。
//
// 为什么要注入而不是「反正只有 Google 能进来」：九个任务端点会写库
// （bump user_rev、清流量、发信、入账），出事时第一个问题是「这次是谁触发的」。
// Scheduler 与 Tasks 用的是不同的服务账号，Email 就是那个答案。
type InternalCaller struct {
	// Email 是调用方服务账号，形如 bp-scheduler@oratis-491316.iam.gserviceaccount.com。
	Email string
	// Subject 是 Google 的稳定用户 ID（SA 的 numeric ID）。email 可以改，sub 不会。
	Subject string
	// Audience 是 token 里的 aud —— 与配置逐字节相等，记下来是为了审计时能回答
	// 「这条任务是冲着哪个 URL 发的」。
	Audience  string
	IssuedAt  time.Time
	ExpiresAt time.Time
}

// JWKSFetcher 按 kid 取签名公钥。
//
// 抽成接口是为了**测试不联网**：单测塞一把自己生成的 RSA 公钥即可，
// 不必打 googleapis.com（那会让 CI 依赖外网，且离线开发时全线红）。
// 生产用 NewGoogleJWKS。
type JWKSFetcher interface {
	KeyFor(ctx context.Context, kid string) (*rsa.PublicKey, error)
}

// InternalAuthConfig 是内部面鉴权的配置。
type InternalAuthConfig struct {
	// Audience 是 token 的 aud 必须逐字节等于的值：**Cloud Run 服务的默认 URL**
	// （`https://bp-api-xxxxxxxx.a.run.app`）。
	//
	// 🔴 不是镜像域名，也不是 API 主域名 —— roadmap.md §4.2 把这一条单独写出来是因为踩过：
	// 创建 Scheduler/Tasks 时填的 `--oidc-token-audience` 与这里配的必须完全相同，
	// 而镜像域名会被封、会轮换（ADR 0003 §5「一键新增镜像域名」）。
	// 用镜像域名当 aud 的后果是**每换一次域名就要重建六条 Scheduler 与一条队列**，
	// 而漏掉哪一条只会表现为「那个任务安静地不再运行」。
	// 服务默认 URL 是 Cloud Run 分配的、不随域名池变化。
	Audience string

	// AllowedCallers 是允许调用的服务账号 email 白名单（配置项 BP_INTERNAL_TASK_CALLERS，逗号分隔）。
	// 传进来时应当已经小写化，本文件仍会再小写一次，不依赖调用方。
	AllowedCallers []string

	// Keys 提供签名公钥。
	Keys JWKSFetcher

	Logger *slog.Logger

	// Leeway 是时间校验的容差，留空用 defaultInternalLeeway。
	Leeway time.Duration

	// Now 可注入以便测试时间边界。留空用 time.Now。
	Now func() time.Time
}

// defaultInternalLeeway 是 exp / iat 校验的时钟容差。
//
// 不留容差的后果是 NTP 抖动表现为「随机 403」—— 那是最难查的一类故障，
// 因为重试就好了。30 秒相对 ID token 一小时的寿命可以忽略，
// 换来的是「时间校验失败」这件事真的意味着 token 过期，而不是两台机器没对上表。
const defaultInternalLeeway = 30 * time.Second

func (c InternalAuthConfig) leeway() time.Duration {
	if c.Leeway > 0 {
		return c.Leeway
	}
	return defaultInternalLeeway
}

func (c InternalAuthConfig) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

// logger 兜底到 slog.Default()。
//
// 与 node.go / user.go 直接用 cfg.Logger 的写法不同：那两条链的 Logger 一定被 main 装配，
// 而本链的失败路径**每一条都要写日志**（响应体是统一文案，日志是唯一的排障通道）。
// 装配时漏传 Logger 会让每一次拒绝都 panic 成 500 —— 那时连「为什么被拒」都查不到了。
func (c InternalAuthConfig) logger() *slog.Logger {
	if c.Logger != nil {
		return c.Logger
	}
	return slog.Default()
}

// AuthenticateInternal 校验 Google OIDC ID token，返回调用方身份。
//
// 返回值要么是 (caller, nil)，要么是 (nil, *AuthError)。与 AuthenticateNode /
// AuthenticateUser 同样的约定：状态码映射留在这里，调用点不各自映射一遍。
//
// 校验顺序是刻意的：**先验签，再看 claim**。
// 签名没验过之前，token 里的一切都是攻击者可控的字符串 ——
// 先按 email 查白名单再验签，等于给一个未认证的输入开了一条查表路径。
func AuthenticateInternal(ctx context.Context, cfg InternalAuthConfig, r *http.Request) (*InternalCaller, *AuthError) {
	log := cfg.logger()

	// deny 统一构造拒绝：响应体是固定文案，真实原因只写日志。见 internalDenyMessage。
	deny := func(reason string, attrs ...any) *AuthError {
		log.WarnContext(ctx, "内部面鉴权拒绝",
			append([]any{"reason", reason, "path", RedactPath(r.URL.Path)}, attrs...)...)
		return &AuthError{http.StatusForbidden, "AUTH_PERMISSION_DENIED", internalDenyMessage}
	}

	// 🔴 fail-closed：配置缺任何一项，整条内部面**全部拒绝**。
	//
	// 与 config 包「缺必需项就拒绝启动」是同一条纪律的运行时版本。
	// 反过来（aud 为空就跳过 aud 校验、白名单为空就放行任意 SA）的后果是：
	// 一个漏配环境变量的实例会接受**任何 Google 账号**签发的 ID token ——
	// 而任何人都可以用 `gcloud auth print-identity-token --audiences=...` 拿到一个。
	// 那不是「少了一道校验」，那是把全站流量计数的清零按钮放到了公网上。
	//
	// 这里返回 403 而不是 500：500 会被任何扫到这个路径的人刷成 5xx 告警
	// （路径不是秘密），而配置缺失这件事要靠下面这行 ERROR 日志与它的 log-based metric 发现，
	// 不靠响应状态码。
	if cfg.Audience == "" || len(cfg.AllowedCallers) == 0 || cfg.Keys == nil {
		log.ErrorContext(ctx, "内部面鉴权未配置，全部拒绝",
			"has_audience", cfg.Audience != "",
			"caller_count", len(cfg.AllowedCallers),
			"has_jwks", cfg.Keys != nil,
			"path", RedactPath(r.URL.Path))
		return nil, &AuthError{http.StatusForbidden, "AUTH_PERMISSION_DENIED", internalDenyMessage}
	}

	// 只认 Authorization 头。
	//
	// 不做 query string 回退 —— 节点面接受 `?token=` 是因为 v2node 只发那个（已读源码核实），
	// 内部面没有这个约束：Cloud Scheduler / Cloud Tasks 的 OIDC 一定走 Authorization 头。
	// 而 query 里的凭据会进 access log 与 Referer。
	raw := extractInternalToken(r)
	if raw == "" {
		return nil, deny("缺少 Authorization: Bearer ID token")
	}
	if len(raw) > maxIDTokenBytes {
		return nil, deny("ID token 超长", "len", len(raw))
	}

	claims, err := verifyGoogleIDToken(ctx, cfg, raw)
	if err != nil {
		return nil, deny(err.Error())
	}

	// ---- iss ----
	if !isGoogleIssuer(claims.Iss) {
		return nil, deny("iss 非 Google", "iss", claims.Iss)
	}

	// ---- aud ----
	//
	// 精确相等，不做前缀 / 后缀 / 包含判断。
	// aud 是「这个 token 是发给谁的」，宽松匹配等于接受一个发给**别的服务**的 token：
	// 同项目里任何一个 Cloud Run 服务的合法调用方，都能把它拿到的 token 转发给我们。
	if string(claims.Aud) != cfg.Audience {
		// 不记 want，只记 got：日志的读取权限比数据库宽，而 aud 本身不是秘密 ——
		// 但把「我们期望什么」写进日志，等于替试探者省掉了猜的那一步。
		return nil, deny("aud 不匹配", "got", string(claims.Aud))
	}

	// ---- exp / iat ----
	now := cfg.now()
	leeway := cfg.leeway()
	if claims.Exp == 0 {
		return nil, deny("token 无 exp")
	}
	exp := time.Unix(claims.Exp, 0)
	if !now.Before(exp.Add(leeway)) {
		return nil, deny("token 已过期", "exp", exp.UTC().Format(time.RFC3339))
	}
	if claims.Iat != 0 {
		iat := time.Unix(claims.Iat, 0)
		if iat.After(now.Add(leeway)) {
			return nil, deny("token 的 iat 在未来", "iat", iat.UTC().Format(time.RFC3339))
		}
	}

	// ---- email_verified ----
	//
	// ⚠️ 这是 Google OIDC 最常被漏掉的一项。漏检的后果不是理论问题：
	// 只要签发方允许未验证邮箱的账号存在，`email` 就是一个**未经证实的自称**，
	// 而下面的白名单比对正是拿它去比的。缺失（nil）也一律拒 ——
	// 「claim 不存在」不等于「已验证」，把缺失当 true 是同一个漏洞的另一种写法。
	if claims.EmailVerified == nil || !*claims.EmailVerified {
		return nil, deny("email_verified 不为 true")
	}

	// ---- email 白名单 ----
	email := strings.ToLower(strings.TrimSpace(claims.Email))
	if email == "" {
		return nil, deny("token 无 email claim（调用方创建 OIDC token 时未带 email scope？）")
	}
	if !internalCallerAllowed(email, cfg.AllowedCallers) {
		return nil, deny("调用方不在白名单", "email", email)
	}

	return &InternalCaller{
		Email:     email,
		Subject:   claims.Sub,
		Audience:  string(claims.Aud),
		IssuedAt:  time.Unix(claims.Iat, 0),
		ExpiresAt: exp,
	}, nil
}

// WithInternalCaller 把内部调用方身份放进上下文。
func WithInternalCaller(ctx context.Context, c *InternalCaller) context.Context {
	return context.WithValue(ctx, internalCallerCtxKey{}, c)
}

// InternalCallerFrom 从上下文取出内部调用方身份。任务 handler 写审计时用。
//
// 与 UserFrom 同样的约定：ok 为 false 表示这条路由**没挂**内部面鉴权（装配错误），
// 而不是「未认证」—— 未认证的请求根本到不了 handler。
// handler 拿到 false 时应当返回 500 而不是 403，否则装配错误会伪装成鉴权问题。
func InternalCallerFrom(ctx context.Context) (*InternalCaller, bool) {
	c, ok := ctx.Value(internalCallerCtxKey{}).(*InternalCaller)
	return c, ok
}

// RequireInternal 是 net/http 形态的中间件：校验失败直接写 403，成功则注入上下文。
//
// 与 RequireUser 同样的理由：内部面 operation 的挂载方式可能是「按路由分组」
// 也可能是「按 operationID 分派」（authmap.go 现在是后者），
// 提供这个形态是为了两种装配都能直接复用 AuthenticateInternal，不必各自抄一遍错误映射。
func RequireInternal(cfg InternalAuthConfig) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			caller, authErr := AuthenticateInternal(r.Context(), cfg, r)
			if authErr != nil {
				WriteAuthError(w, r, authErr)
				return
			}
			next.ServeHTTP(w, r.WithContext(WithInternalCaller(r.Context(), caller)))
		})
	}
}

// extractInternalToken 取 Authorization: Bearer 的值。
//
// 与 node.go / user.go 一致只认大小写严格的 "Bearer " 前缀。
// RFC 7235 说 scheme 大小写不敏感，但三条链在这一点上保持同一行为比迁就理论更重要 ——
// 真实调用方（Google 基础设施）发的就是 "Bearer"。
func extractInternalToken(r *http.Request) string {
	v, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok {
		return ""
	}
	return strings.TrimSpace(v)
}

func isGoogleIssuer(iss string) bool {
	for _, want := range googleIssuers {
		if iss == want {
			return true
		}
	}
	return false
}

// internalCallerAllowed 判断 email 是否在白名单里。
//
// 线性扫描：白名单只有两三个元素（六条 Scheduler 与一条 Tasks 队列共用一到两个 SA），
// 建 map 的收益是负的，而 map 会让「配置顺序」这类排障信息丢失。
func internalCallerAllowed(email string, allowed []string) bool {
	for _, a := range allowed {
		if strings.ToLower(strings.TrimSpace(a)) == email {
			return true
		}
	}
	return false
}

// ---- ID token 解析与验签 ----

// idTokenAudience 是 `aud` claim 的类型。
//
// JWT 规范允许 aud 是字符串或字符串数组，但 Google 的 ID token 只发单值字符串。
// 这里**显式拒绝数组形态**而不是「数组里包含就算过」：
// 「包含」比「等于」宽松一个数量级，而我们并不需要那份宽松。
type idTokenAudience string

func (a *idTokenAudience) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return errors.New("aud 必须是单个字符串")
	}
	*a = idTokenAudience(s)
	return nil
}

type idTokenHeader struct {
	Alg string `json:"alg"`
	Kid string `json:"kid"`
	Typ string `json:"typ"`
}

// idTokenClaims 只列我们校验用得上的 claim。
//
// **不能用 DisallowUnknownFields** —— Google 会往 ID token 里加新 claim（azp、hd、at_hash……），
// 严格解码的后果是某天 Google 加一个字段，我们这边六条定时任务同时 403。
type idTokenClaims struct {
	Iss string          `json:"iss"`
	Aud idTokenAudience `json:"aud"`
	Sub string          `json:"sub"`
	// EmailVerified 用 *bool 而不是 bool：必须能区分「claim 为 false」与「claim 不存在」，
	// 两者都要拒，但零值 bool 会把两者混成同一个 false，将来有人写 `if !verified` 时
	// 看不出「缺失」这条分支从来没被单独处理过。见 AuthenticateInternal 的注释。
	EmailVerified *bool  `json:"email_verified"`
	Email         string `json:"email"`
	Exp           int64  `json:"exp"`
	Iat           int64  `json:"iat"`
}

// verifyGoogleIDToken 解析 JWS、取公钥、验签，返回**已验签**的 claim。
//
// 只做「这串东西确实由 Google 用它公布的私钥签过」这一件事。
// iss / aud / exp / email 的语义校验在 AuthenticateInternal 里 ——
// 分开是为了让「签名有没有验过」在代码里是一个可以单独回答的问题。
func verifyGoogleIDToken(ctx context.Context, cfg InternalAuthConfig, raw string) (*idTokenClaims, error) {
	h, claimsJSON, signingInput, sig, err := splitIDTokenJWS(raw)
	if err != nil {
		return nil, err
	}

	// alg 白名单，见 requiredIDTokenAlg 的注释。这一步必须在取公钥之前。
	if h.Alg != requiredIDTokenAlg {
		return nil, fmt.Errorf("alg 必须是 %s，实得 %q", requiredIDTokenAlg, h.Alg)
	}
	// typ 可缺省；给了就必须是 JWT。Google 发的是 "JWT"。
	if h.Typ != "" && !strings.EqualFold(h.Typ, "JWT") {
		return nil, fmt.Errorf("typ 非 JWT: %q", h.Typ)
	}
	if h.Kid == "" {
		return nil, errors.New("header 无 kid")
	}

	pub, err := cfg.Keys.KeyFor(ctx, h.Kid)
	if err != nil {
		return nil, fmt.Errorf("取签名公钥失败: %w", err)
	}

	sum := sha256.Sum256(signingInput)
	if err := rsa.VerifyPKCS1v15(pub, crypto.SHA256, sum[:], sig); err != nil {
		return nil, fmt.Errorf("签名校验失败: %w", err)
	}

	var claims idTokenClaims
	if err := json.Unmarshal(claimsJSON, &claims); err != nil {
		return nil, fmt.Errorf("claim 解析失败: %w", err)
	}
	return &claims, nil
}

// splitIDTokenJWS 拆出 header / payload / 签名，并返回验签用的原文（`header.payload` 的字节）。
//
// signingInput 取的是**原始子串**而不是重新拼接解码后的内容：
// base64 有多种能解出同一字节串的写法，重新编码会得到与被签名时不同的字节，
// 于是合法 token 验签失败 —— 这是手写 JWT 校验最常见的一个 bug。
func splitIDTokenJWS(raw string) (hdr idTokenHeader, claimsJSON, signingInput, sig []byte, err error) {
	first := strings.IndexByte(raw, '.')
	last := strings.LastIndexByte(raw, '.')
	if first <= 0 || last <= first || last == len(raw)-1 {
		return hdr, nil, nil, nil, errors.New("不是三段式 JWT")
	}
	if strings.IndexByte(raw[first+1:last], '.') >= 0 {
		return hdr, nil, nil, nil, errors.New("不是三段式 JWT")
	}

	// RawURLEncoding：JWS 规定 base64url **不带填充**（RFC 7515 §2）。
	// 用带填充的解码器会顺带接受非规范形态，那是签名可塑性的入口。
	hb, e := base64.RawURLEncoding.DecodeString(raw[:first])
	if e != nil {
		return hdr, nil, nil, nil, fmt.Errorf("header 不是 base64url: %w", e)
	}
	if e := json.Unmarshal(hb, &hdr); e != nil {
		return hdr, nil, nil, nil, fmt.Errorf("header 解析失败: %w", e)
	}
	claimsJSON, e = base64.RawURLEncoding.DecodeString(raw[first+1 : last])
	if e != nil {
		return hdr, nil, nil, nil, fmt.Errorf("payload 不是 base64url: %w", e)
	}
	sig, e = base64.RawURLEncoding.DecodeString(raw[last+1:])
	if e != nil {
		return hdr, nil, nil, nil, fmt.Errorf("签名不是 base64url: %w", e)
	}
	return hdr, claimsJSON, []byte(raw[:last]), sig, nil
}

// ---- Google JWKS ----

// GoogleJWKS 是 JWKSFetcher 的生产实现：按 kid 缓存 Google 公布的 RSA 公钥。
//
// 为什么要缓存：这九个端点被 Scheduler 每分钟敲一次，每次都去拉一遍 JWKS
// 等于给每个内部任务加一次跨网请求 —— 而 Google 那份文件的 Cache-Control
// 明确说了可以缓存数小时。
type GoogleJWKS struct {
	url    string
	client *http.Client
	nowFn  func() time.Time

	// minRefetch 是两次拉取之间的最小间隔。
	//
	// 🔴 没有它就有一个放大器：kid 由**请求方**控制，一串随手编的 kid
	// 会一个不落地全部 miss，于是我们对 Google 发起同样密度的请求流 ——
	// 用一个未认证端点把我们变成打 googleapis.com 的机器人，
	// 顺带在 Google 那边给自己招来限流，把真正的定时任务一起挡掉。
	minRefetch time.Duration

	mu        sync.Mutex
	keys      map[string]*rsa.PublicKey
	expiresAt time.Time // 缓存到期时刻（来自 Cache-Control）
	lastFetch time.Time // 上次**尝试**拉取的时刻，成功与否都更新
}

const (
	googleJWKSTTL        = time.Hour
	googleJWKSMinTTL     = 5 * time.Minute
	googleJWKSMaxTTL     = 24 * time.Hour
	googleJWKSMinRefetch = time.Minute
	googleJWKSMaxBytes   = 64 << 10
)

// NewGoogleJWKS 构造生产用的公钥源。client 传 nil 时用一个带超时的默认客户端。
func NewGoogleJWKS(client *http.Client) *GoogleJWKS {
	if client == nil {
		// 必须有超时：没有超时的 http.Client 在对端假死时会把 goroutine 永久挂住，
		// 而这条路径在鉴权链上 —— 挂住的是每一个内部任务请求。
		client = &http.Client{Timeout: 5 * time.Second}
	}
	return &GoogleJWKS{
		url:        googleJWKSURL,
		client:     client,
		nowFn:      time.Now,
		minRefetch: googleJWKSMinRefetch,
		keys:       map[string]*rsa.PublicKey{},
	}
}

// KeyFor 返回 kid 对应的公钥。
//
// 整个查找持有同一把互斥锁，包括那次 HTTP 拉取。这是刻意的：
// 内部面的 QPS 是「六条 Scheduler + 一条队列」的量级，串行化的代价可以忽略，
// 换来的是密钥轮换那一刻不会有 N 个并发请求同时去拉同一份 JWKS。
func (g *GoogleJWKS) KeyFor(ctx context.Context, kid string) (*rsa.PublicKey, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	now := g.nowFn()
	cached, hit := g.keys[kid]
	if hit && now.Before(g.expiresAt) {
		return cached, nil
	}

	if now.Sub(g.lastFetch) < g.minRefetch {
		if hit {
			// 缓存过期但 kid 命中：用略旧的公钥，不因为「该刷新了」就把请求挡掉。
			// 公钥过期 ≠ 失效 —— Google 的轮换是「先公布新的，旧的再挂一段时间」，
			// 这份缓存里的公钥在窗口内仍然是真的。
			return cached, nil
		}
		return nil, fmt.Errorf("kid %q 未知，且 JWKS 刚拉取过（节流中）", kid)
	}

	if err := g.refreshLocked(ctx, now); err != nil {
		if hit {
			// JWKS 暂时不可达时，宁可用过期缓存也不要让所有定时任务停摆。
			return cached, nil
		}
		return nil, err
	}
	k, ok := g.keys[kid]
	if !ok {
		return nil, fmt.Errorf("kid %q 不在 Google 的 JWKS 中", kid)
	}
	return k, nil
}

// refreshLocked 拉取并替换整份 JWKS。调用方必须已持有 g.mu。
func (g *GoogleJWKS) refreshLocked(ctx context.Context, now time.Time) error {
	// 先记「尝试过」再发请求：拉取失败时也要走节流，否则 Google 侧一出故障，
	// 我们就变成对它的重试风暴。
	g.lastFetch = now

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, g.url, nil)
	if err != nil {
		return fmt.Errorf("构造 JWKS 请求失败: %w", err)
	}
	resp, err := g.client.Do(req)
	if err != nil {
		return fmt.Errorf("拉取 JWKS 失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("拉取 JWKS 返回 %d", resp.StatusCode)
	}
	// 限长读：对端返回一份超大响应时不能把它整个读进内存。
	body, err := io.ReadAll(io.LimitReader(resp.Body, googleJWKSMaxBytes))
	if err != nil {
		return fmt.Errorf("读取 JWKS 失败: %w", err)
	}
	keys, err := parseGoogleJWKS(body)
	if err != nil {
		return err
	}

	g.keys = keys
	ttl := googleJWKSTTL
	if d, ok := googleJWKSMaxAge(resp.Header.Get("Cache-Control")); ok {
		ttl = d
	}
	// 夹住上下界：Google 给的 max-age 通常是几小时，但一个异常的极小值
	// 会让我们每次请求都去拉，极大值会让轮换后的新 kid 迟迟拿不到。
	ttl = min(max(ttl, googleJWKSMinTTL), googleJWKSMaxTTL)
	g.expiresAt = now.Add(ttl)
	return nil
}

type googleJWKSet struct {
	Keys []googleJWK `json:"keys"`
}

type googleJWK struct {
	Kty string `json:"kty"`
	Kid string `json:"kid"`
	Use string `json:"use"`
	Alg string `json:"alg"`
	N   string `json:"n"`
	E   string `json:"e"`
}

// minRSAModulusBits 是可接受的最小 RSA 模数位数。
//
// Google 发的是 2048 位。加这道检查是纵深防御：万一有人把 JWKS 地址指到别处
// （配置错误、DNS 劫持、测试残留），一把 512 位的密钥可以被当场分解，
// 而验签会「成功」。低于 2048 位的密钥在任何情况下都不该被我们接受。
const minRSAModulusBits = 2048

func parseGoogleJWKS(body []byte) (map[string]*rsa.PublicKey, error) {
	var set googleJWKSet
	if err := json.Unmarshal(body, &set); err != nil {
		return nil, fmt.Errorf("JWKS 解析失败: %w", err)
	}
	out := make(map[string]*rsa.PublicKey, len(set.Keys))
	for _, k := range set.Keys {
		// 只收「RSA + 用于验签 + RS256」的密钥。use / alg 缺省视为符合
		// （JWK 规范里两者都是可选的），但给了值就必须对得上。
		if k.Kty != "RSA" || k.Kid == "" {
			continue
		}
		if k.Use != "" && k.Use != "sig" {
			continue
		}
		if k.Alg != "" && k.Alg != requiredIDTokenAlg {
			continue
		}
		pub, err := googleJWKToRSA(k)
		if err != nil {
			// 单把密钥坏掉不该让整份 JWKS 作废 —— 轮换期间同时存在多把，
			// 拒绝整份的后果是所有内部任务一起挂。
			continue
		}
		out[k.Kid] = pub
	}
	if len(out) == 0 {
		return nil, errors.New("JWKS 里没有可用的 RS256 公钥")
	}
	return out, nil
}

func googleJWKToRSA(k googleJWK) (*rsa.PublicKey, error) {
	nb, err := base64.RawURLEncoding.DecodeString(k.N)
	if err != nil {
		return nil, fmt.Errorf("n 不是 base64url: %w", err)
	}
	eb, err := base64.RawURLEncoding.DecodeString(k.E)
	if err != nil {
		return nil, fmt.Errorf("e 不是 base64url: %w", err)
	}
	if len(eb) == 0 || len(eb) > 4 {
		return nil, errors.New("e 长度不合法")
	}
	n := new(big.Int).SetBytes(nb)
	if n.BitLen() < minRSAModulusBits {
		return nil, fmt.Errorf("模数只有 %d 位，低于 %d 位下限", n.BitLen(), minRSAModulusBits)
	}
	var e int
	for _, b := range eb {
		e = e<<8 | int(b)
	}
	// 公开指数必须是奇数且 ≥ 3：偶数或 1 的「公钥」不构成有效 RSA，
	// crypto/rsa 在验签时也会拒，这里提前拒是为了让原因出现在日志里。
	if e < 3 || e%2 == 0 {
		return nil, fmt.Errorf("e 取值不合法: %d", e)
	}
	return &rsa.PublicKey{N: n, E: e}, nil
}

// googleJWKSMaxAge 从 Cache-Control 里取 max-age 秒数。
func googleJWKSMaxAge(v string) (time.Duration, bool) {
	for _, part := range strings.Split(v, ",") {
		rest, ok := strings.CutPrefix(strings.ToLower(strings.TrimSpace(part)), "max-age=")
		if !ok {
			continue
		}
		n, err := strconv.Atoi(strings.TrimSpace(rest))
		if err != nil || n <= 0 {
			return 0, false
		}
		return time.Duration(n) * time.Second, true
	}
	return 0, false
}

// ---- 任务侧幂等 ----
//
// Cloud Tasks 是 **at-least-once**，重复投递是常态不是异常（api-contract.md §7 约束 3）。
// 每个任务 handler 都要能回答「这次是不是重复投递」。
//
// 本节**不另造一套幂等**，而是复用 httpx 的 idempotency_keys 实现 ——
// 两套幂等意味着两张表、两种过期策略、两处并发三态的判断，
// 而并发三态（首次 / 重放 / 进行中）正是最容易写错的那一块。
//
// 与用户面那一半的两处差别：
//
//  1. **键由载荷派生，不是调用方自带的。** 用户面的键来自 `Idempotency-Key` 头，
//     内部面的键来自任务参数（api-contract.md §7 的最后一列：`traffic_batch.batch_id`、
//     `(user_id, reset_period)`、`(scope, record_at)`……）。Cloud Tasks 重投时载荷逐字节相同，
//     所以同一份工作必然算出同一个键。
//  2. **不回放响应体。** 契约给内部面的成功响应是 `{"ok":true,"idempotent_skip":true}`
//     （openapi InternalTaskAck），也就是说「这次是重复投递」要如实告诉调用方，
//     而不是把重放伪装成首次执行。所以只落盘状态码，body 留空，
//     handler 在 OutcomeReplay 分支返回 `idempotent_skip: true`。
//
// ⚠️ 覆盖窗口是 24 小时（idempotency_keys.expires_at 的默认值）。
// 跨天的重复不归它管 —— stat-rollup 那类本来就是 upsert，靠业务表的唯一约束兜底。
// 另：CleanupExpiredIdempotencyKeys 必须真的被某条定时任务调起来，
// 否则过期未清理的行会卡住同名键（见 httpx.ErrIdempotencyKeyStale）。

// internalTaskEndpointPrefix 给内部面的 endpoint 值加命名空间。
//
// idempotency_keys.endpoint 是用户面与内部面共用的一列，用户面填 operationID。
// 不加前缀的话，一个叫得巧的任务名可以与某个 operationID 撞上，
// 于是两条互不相干的路径共用同一段键空间。
const internalTaskEndpointPrefix = "task:"

// ErrInternalTaskName 表示任务名不合法（装配错误，不是运行时输入问题）。
var ErrInternalTaskName = errors.New("内部任务名不合法：只允许小写字母、数字与连字符，长度 1–32")

// BeginInternalTask 用任务自己声明的幂等键抢占一次执行。
//
// task 用端点的短名（"traffic-batch" / "traffic-reset" / "stat-rollup"…），
// parts 是 api-contract.md §7 里那一列幂等键的各个分量，例如：
//
//	att, err := mw.BeginInternalTask(ctx, db, "traffic-batch", batchID)
//	att, err := mw.BeginInternalTask(ctx, db, "traffic-reset", strconv.FormatInt(uid, 10), period)
//
// 调用方按 httpx 的四种错误分别处理：
//   - ErrIdempotencyInProgress：同一份工作正在被另一次投递执行 → 让 Cloud Tasks 重试（5xx），
//     **不要**当成「已完成」返回 200 —— 那次执行可能会失败，而 200 会让这条投递被丢弃。
//   - ErrIdempotencyKeyStale：过期未清理的残留 → 同样交给重试。
//   - ErrIdempotencyMismatch：同键不同载荷。内部面的键由载荷派生，正常不可能发生；
//     真出现说明键的分量选得不足以标识这份工作，是 bug，要报出来。
//   - Outcome == OutcomeReplay：重复投递 → 直接返回 `{"ok":true,"idempotent_skip":true}`。
//
// 执行成功后**必须**调 FinishInternalTask，否则键会一直停在 in_progress，
// 24 小时内的同键重投全部拿到 ErrIdempotencyInProgress。
func BeginInternalTask(ctx context.Context, db httpx.IdempotencyStore, task string, parts ...string) (*httpx.Attempt, error) {
	if !validInternalTaskName(task) {
		return nil, fmt.Errorf("%w: %q", ErrInternalTaskName, task)
	}
	canon := canonicalTaskParts(parts)
	return httpx.BeginIdempotent(ctx, db, httpx.IdempotentRequest{
		Key: internalTaskKey(task, canon),
		// 内部任务没有用户上下文。httpx 的 sameOwner 把「两边都是 nil」视为同一归属，
		// 这正是我们要的：键的归属由 endpoint + 分量决定，不由用户决定。
		UserID:   nil,
		Endpoint: internalTaskEndpointPrefix + task,
		// Body 填与键同源的规范化字节。指纹因此与键一一对应 ——
		// 这让 httpx 的指纹比对在内部面成为一条恒真的断言，而不是一个常量占位符：
		// 万一将来有人改了键的构造却忘了改这里，指纹会立刻对不上并报 mismatch。
		Body: canon,
	})
}

// FinishInternalTask 落盘执行结果，供 24 小时内的重复投递判定为「已处理」。
//
// 不存响应体：见本节开头第 2 点。
func FinishInternalTask(ctx context.Context, db httpx.IdempotencyStore, att *httpx.Attempt) error {
	if att == nil {
		return errors.New("FinishInternalTask: attempt 为空")
	}
	return httpx.CompleteIdempotent(ctx, db, att.Key, http.StatusOK, nil)
}

// internalTaskKey 由任务名与分量的哈希拼出幂等键。
//
// 为什么哈希而不是直接把分量拼进键：分量是**任务侧的数据**
// （batch_id 由入队方生成、user_id/period 来自库里），长度与字符集都不受我们控制。
// 直接拼接的后果是 httpx.ValidateIdempotencyKey 可能在**执行之前**就把它判为非法，
// 现象是「这个任务永远跑不了」，而排查方向会完全跑偏。
// 哈希之后长度恒定（`task:` + 名字 + 64 位十六进制 ≤ 128），字符集恒定合法。
func internalTaskKey(task string, canon []byte) string {
	sum := sha256.Sum256(canon)
	return internalTaskEndpointPrefix + task + ":" + hex.EncodeToString(sum[:])
}

// canonicalTaskParts 把分量拼成无歧义的字节串。
//
// 长度前缀而不是分隔符拼接：`["ab","c"]` 与 `["a","bc"]` 用分隔符拼出来可能相同，
// 于是两份不同的工作共用一个幂等键 —— 第二份会被静默当成重复投递丢掉。
// 这与 httpx.IdempotentRequest.Fingerprint 是同一条纪律。
func canonicalTaskParts(parts []string) []byte {
	var b strings.Builder
	for _, p := range parts {
		b.WriteString(strconv.Itoa(len(p)))
		b.WriteByte(':')
		b.WriteString(p)
	}
	return []byte(b.String())
}

// validInternalTaskName 限制任务名的字符集与长度。
//
// 它会进主键与 endpoint 列，放任任意字符串进来等于把键空间交给调用方。
// 收窄到 openapi 里那九个端点的短名形态（小写 + 连字符）。
func validInternalTaskName(task string) bool {
	if task == "" || len(task) > 32 {
		return false
	}
	for i := 0; i < len(task); i++ {
		c := task[i]
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '-':
		default:
			return false
		}
	}
	return true
}

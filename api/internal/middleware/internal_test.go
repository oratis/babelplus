// 内部面鉴权（internal.go）的测试 —— /internal/tasks/* 的 Google OIDC。
//
// 这批端点没有人类界面，路径也不出现在任何前端代码里，而它们与公网端点跑在
// **同一个 Cloud Run service** 上（「不要常驻 worker」的直接后果）。
// 路径不是秘密，token 才是：一个无鉴权的 POST /internal/tasks/traffic-reset
// 可以被任何人用来清空全站流量计数。接线之前必须先证明每一道校验都真的在拒绝。
//
// # 每个用例为什么必须存在
//
//	TestAuthenticateInternalRejectsBadTokens
//	  无 token / 错 aud / 错 iss / 过期，各拒一次。
//	  aud 宽松匹配等于接受一个**发给别的服务**的 token —— 同项目里任何一个
//	  Cloud Run 服务的合法调用方，都能把它拿到的 token 转发给我们。
//
//	TestAuthenticateInternalRequiresEmailVerified
//	  ⚠️ Google OIDC 最常被漏检的一项。只要签发方允许未验证邮箱的账号存在，
//	  `email` 就是一个未经证实的自称，而白名单比对正是拿它去比的。
//	  claim **缺失（nil）也必须拒** —— 把缺失当 true 是同一个漏洞的另一种写法。
//
//	TestAuthenticateInternalRejectsCallerNotOnAllowlist
//	  签名合法只说明「这是一个 Google 账号」。任何人都能
//	  `gcloud auth print-identity-token --audiences=...` 拿到一个合法 token，
//	  白名单是「这个 Google 账号是不是我们的 Scheduler」的唯一判据。
//
//	TestAuthenticateInternalDeniesWholePlaneWhenUnconfigured
//	  🔴 白名单为空 or aud 为空 → 整条内部面拒绝，不是「跳过那项校验」。
//	  反过来的后果是：一个漏配环境变量的实例会接受**任何 Google 账号**签发的
//	  ID token —— 那不是「少了一道校验」，是把全站流量计数的清零按钮放到公网上。
//
//	TestAuthenticateInternalHappyPath
//	  正确 token 必须放行，且能取出调用方 email —— 九个任务端点都会写库，
//	  出事时第一个问题是「这次是谁触发的」，Scheduler 与 Tasks 用的是不同的 SA。
//
//	TestAuthenticateInternalRejectsForgedSignature
//	  alg:none / HS256 算法混淆 / 攻击者自签，各拒一次。算法必须由**我们**指定，
//	  不能由 token 自己声明；Google 的验签公钥是公开可下载的。
//
// # JWKS 用可注入的 fetcher，测试不联网
//
// 本文件全部用 fakeGoogleJWKS（一把测试里现生成的 RSA 公钥）。
// 唯一起 http 服务的用例打的是 httptest 的本地回环地址，不出机器。
// 让 CI 依赖 googleapis.com 的后果是「离线开发时全线红」。
package middleware

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

const (
	internalTestAudience = "https://bp-api-abcdef1234.a.run.app"
	internalTestKID      = "bp-test-google-kid"
	internalTestCaller   = "bp-scheduler@oratis-491316.iam.gserviceaccount.com"
	internalTestSubject  = "112233445566778899000"
)

var internalTestNow = time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)

// fakeGoogleJWKS 是 JWKSFetcher 的假实现。calls 用来断言「拒绝时不去取公钥」。
type fakeGoogleJWKS struct {
	keys  map[string]*rsa.PublicKey
	calls int
}

func (f *fakeGoogleJWKS) KeyFor(_ context.Context, kid string) (*rsa.PublicKey, error) {
	f.calls++
	k, ok := f.keys[kid]
	if !ok {
		return nil, fmt.Errorf("未知 kid %q", kid)
	}
	return k, nil
}

type internalFixture struct {
	priv *rsa.PrivateKey
	keys *fakeGoogleJWKS
	now  time.Time
}

func newInternalFixture(t *testing.T) *internalFixture {
	t.Helper()
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("生成 RSA 测试密钥失败: %v", err)
	}
	return &internalFixture{
		priv: priv,
		keys: &fakeGoogleJWKS{keys: map[string]*rsa.PublicKey{internalTestKID: &priv.PublicKey}},
		now:  internalTestNow,
	}
}

func (f *internalFixture) cfg() InternalAuthConfig {
	return InternalAuthConfig{
		Audience:       internalTestAudience,
		AllowedCallers: []string{internalTestCaller},
		Keys:           f.keys,
		Logger:         slog.New(slog.NewTextHandler(io.Discard, nil)),
		Now:            func() time.Time { return f.now },
	}
}

func (f *internalFixture) validClaims() map[string]any {
	return map[string]any{
		"iss":            "https://accounts.google.com",
		"aud":            internalTestAudience,
		"sub":            internalTestSubject,
		"email":          internalTestCaller,
		"email_verified": true,
		"exp":            f.now.Add(time.Hour).Unix(),
		"iat":            f.now.Add(-time.Minute).Unix(),
		// Google 会往 ID token 里加新 claim（azp / at_hash / hd…）。
		// 多一个字段不该让六条定时任务同时 403，所以这里刻意混进一个未知 claim。
		"azp": internalTestSubject,
	}
}

func (f *internalFixture) token(t *testing.T, mut func(hdr, claims map[string]any)) string {
	t.Helper()
	hdr := map[string]any{"alg": "RS256", "kid": internalTestKID, "typ": "JWT"}
	claims := f.validClaims()
	if mut != nil {
		mut(hdr, claims)
	}
	return internalTestSignRS256(t, f.priv, hdr, claims)
}

func internalTestSignRS256(t *testing.T, priv *rsa.PrivateKey, hdr, claims map[string]any) string {
	t.Helper()
	signing := internalTestJoinSegments(t, hdr, claims)
	sum := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, priv, crypto.SHA256, sum[:])
	if err != nil {
		t.Fatalf("签名失败: %v", err)
	}
	return signing + "." + base64.RawURLEncoding.EncodeToString(sig)
}

func internalTestJoinSegments(t *testing.T, hdr, claims map[string]any) string {
	t.Helper()
	hb, err := json.Marshal(hdr)
	if err != nil {
		t.Fatalf("header 序列化失败: %v", err)
	}
	cb, err := json.Marshal(claims)
	if err != nil {
		t.Fatalf("claims 序列化失败: %v", err)
	}
	return base64.RawURLEncoding.EncodeToString(hb) + "." + base64.RawURLEncoding.EncodeToString(cb)
}

func internalReq(token string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/internal/tasks/traffic-reset", nil)
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	return r
}

// internalAssertDenied 断言「403 + 统一错误码 + 统一文案」。
//
// 真实原因只进日志：区分「签名坏了」与「email 不在白名单」等于替试探者
// 确认了一大半信息。
func internalAssertDenied(t *testing.T, cfg InternalAuthConfig, r *http.Request) *AuthError {
	t.Helper()
	caller, authErr := AuthenticateInternal(context.Background(), cfg, r)
	if caller != nil {
		t.Fatalf("不该拿到调用方身份: %+v", caller)
	}
	if authErr == nil {
		t.Fatal("必须被拒")
	}
	if authErr.Status != http.StatusForbidden {
		t.Fatalf("内部面失败必须是 403（openapi 只声明了 200/403/500），实得 %d", authErr.Status)
	}
	if authErr.Code != "AUTH_PERMISSION_DENIED" {
		t.Fatalf("错误码 = %q", authErr.Code)
	}
	if authErr.Message != internalDenyMessage {
		t.Fatalf("拒绝文案必须统一（真实原因只进日志），实得 %q", authErr.Message)
	}
	return authErr
}

// ---- 无 token / 错 aud / 错 iss / 过期 ----

func TestAuthenticateInternalRejectsBadTokens(t *testing.T) {
	cases := []struct {
		name string
		mut  func(hdr, claims map[string]any)
		why  string
	}{
		{
			"aud 是同项目里另一个服务",
			func(_, c map[string]any) { c["aud"] = "https://other-service-abcdef1234.a.run.app" },
			"aud 宽松就等于接受一个发给别的服务的 token",
		},
		{
			"aud 只是前缀",
			func(_, c map[string]any) { c["aud"] = internalTestAudience[:len(internalTestAudience)-4] },
			"必须精确相等，不做前缀 / 后缀 / 包含判断",
		},
		{
			"aud 缺失",
			func(_, c map[string]any) { delete(c, "aud") },
			"缺 aud 不能被当成「不限制」",
		},
		{
			"aud 是数组形态",
			func(_, c map[string]any) { c["aud"] = []string{internalTestAudience} },
			"Google 的 ID token 只发单值字符串；接受数组等于把「等于」放宽成「包含」",
		},
		{
			"iss 不是 Google",
			func(_, c map[string]any) { c["iss"] = "https://evil.example.com" },
			"iss 不对说明这不是 Google 签的（哪怕签名在我们的假 JWKS 下能过）",
		},
		{
			"iss 缺失",
			func(_, c map[string]any) { delete(c, "iss") },
			"缺 iss 必须拒",
		},
		{
			"已过期",
			func(_, c map[string]any) { c["exp"] = internalTestNow.Add(-2 * time.Hour).Unix() },
			"过期 token 必须拒，否则一次泄漏就是永久凭据",
		},
		{
			"缺少 exp",
			func(_, c map[string]any) { delete(c, "exp") },
			"没有 exp 的 token 永不过期",
		},
		{
			"iat 在未来",
			func(_, c map[string]any) { c["iat"] = internalTestNow.Add(time.Hour).Unix() },
			"iat 在未来说明对方时钟不对或 token 是造的",
		},
		{
			"kid 缺失",
			func(h, _ map[string]any) { delete(h, "kid") },
			"没有 kid 就没法确定用哪把公钥",
		},
		{
			"kid 不在 JWKS 里",
			func(h, _ map[string]any) { h["kid"] = "kid-we-never-saw" },
			"未知 kid 必须拒，不能回退到任何一把已知公钥",
		},
		{
			"typ 不是 JWT",
			func(h, _ map[string]any) { h["typ"] = "at+jwt" },
			"给了 typ 就必须是 JWT",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newInternalFixture(t)
			internalAssertDenied(t, f.cfg(), internalReq(f.token(t, tc.mut)))
		})
	}

	t.Run("完全没有 Authorization 头", func(t *testing.T) {
		f := newInternalFixture(t)
		internalAssertDenied(t, f.cfg(), internalReq(""))
		if f.keys.calls != 0 {
			t.Fatal("没有 token 时不该去取公钥")
		}
	})

	t.Run("Authorization 不是 Bearer 形态", func(t *testing.T) {
		f := newInternalFixture(t)
		r := internalReq("")
		r.Header.Set("Authorization", "Basic dXNlcjpwYXNz")
		internalAssertDenied(t, f.cfg(), r)
	})

	t.Run("凭据放在 query 里不被接受", func(t *testing.T) {
		// 节点面接受 ?token= 是因为 v2node 只发那个；内部面没有这个约束，
		// 而 query 里的凭据会进 access log 与 Referer。
		f := newInternalFixture(t)
		r := httptest.NewRequest(http.MethodPost,
			"/internal/tasks/traffic-reset?token="+f.token(t, nil), nil)
		internalAssertDenied(t, f.cfg(), r)
	})

	t.Run("超长 token 在解析之前就被拒", func(t *testing.T) {
		f := newInternalFixture(t)
		internalAssertDenied(t, f.cfg(), internalReq(longToken(maxIDTokenBytes+1)))
		if f.keys.calls != 0 {
			t.Fatal("超长 token 不该走到验签（先量长度再解析，base64 与 RSA 都要花 CPU）")
		}
	})
}

// ---- 伪造签名 ----

// alg 必须由**我们**指定，不能由 token 自己声明 —— Google 的验签公钥是公开的。
func TestAuthenticateInternalRejectsForgedSignature(t *testing.T) {
	t.Run("alg none", func(t *testing.T) {
		f := newInternalFixture(t)
		raw := internalTestJoinSegments(t,
			map[string]any{"alg": "none", "kid": internalTestKID}, f.validClaims()) + "."
		internalAssertDenied(t, f.cfg(), internalReq(raw))
		if f.keys.calls != 0 {
			t.Fatal("alg 白名单必须在取公钥之前生效")
		}
	})

	t.Run("HS256 算法混淆", func(t *testing.T) {
		f := newInternalFixture(t)
		signing := internalTestJoinSegments(t,
			map[string]any{"alg": "HS256", "kid": internalTestKID}, f.validClaims())
		// 攻击者拿公开的 RSA 模数当 HMAC 密钥。
		raw := signing + "." + base64.RawURLEncoding.EncodeToString(f.priv.PublicKey.N.Bytes())
		internalAssertDenied(t, f.cfg(), internalReq(raw))
		if f.keys.calls != 0 {
			t.Fatal("alg 白名单必须在取公钥之前生效，否则算法混淆有机可乘")
		}
	})

	t.Run("攻击者自签的 RS256", func(t *testing.T) {
		f := newInternalFixture(t)
		evil, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			t.Fatalf("生成攻击者密钥失败: %v", err)
		}
		raw := internalTestSignRS256(t, evil,
			map[string]any{"alg": "RS256", "kid": internalTestKID, "typ": "JWT"}, f.validClaims())
		internalAssertDenied(t, f.cfg(), internalReq(raw))
	})

	t.Run("篡改 payload 后签名失配", func(t *testing.T) {
		f := newInternalFixture(t)
		raw := f.token(t, nil)
		tampered := internalTestJoinSegments(t,
			map[string]any{"alg": "RS256", "kid": internalTestKID, "typ": "JWT"},
			map[string]any{"iss": "https://accounts.google.com", "aud": internalTestAudience,
				"email": "attacker@example.com", "email_verified": true,
				"exp": f.now.Add(time.Hour).Unix()})
		// 保留原签名，换掉前两段。
		raw = tampered + raw[len(raw)-len(raw[lastDot(raw)+1:]):]
		internalAssertDenied(t, f.cfg(), internalReq(raw))
	})

	t.Run("不是三段式", func(t *testing.T) {
		f := newInternalFixture(t)
		for _, raw := range []string{"a.b", "a.b.c.d", "a.b.", ".b.c", "nodots"} {
			internalAssertDenied(t, f.cfg(), internalReq(raw))
		}
	})
}

func lastDot(s string) int {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == '.' {
			return i
		}
	}
	return -1
}

// ---- email_verified ----

// ⚠️ Google OIDC 最常被漏检的一项。三种形态都必须拒：
// false、缺失（nil）、以及把它写成字符串 "true"（那不是布尔真）。
func TestAuthenticateInternalRequiresEmailVerified(t *testing.T) {
	for _, tc := range []struct {
		name string
		mut  func(hdr, claims map[string]any)
	}{
		{"email_verified 为 false", func(_, c map[string]any) { c["email_verified"] = false }},
		{"email_verified 缺失", func(_, c map[string]any) { delete(c, "email_verified") }},
		{"email_verified 为 null", func(_, c map[string]any) { c["email_verified"] = nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newInternalFixture(t)
			// 关键：其它一切都合法，白名单也命中 —— 只有这一项不对。
			internalAssertDenied(t, f.cfg(), internalReq(f.token(t, tc.mut)))
		})
	}
}

// ---- 白名单 ----

// 签名合法只说明「这是一个 Google 账号」。任何人都能拿到一个合法的
// Google ID token，白名单是「这个账号是不是我们的 Scheduler」的唯一判据。
func TestAuthenticateInternalRejectsCallerNotOnAllowlist(t *testing.T) {
	f := newInternalFixture(t)
	raw := f.token(t, func(_, c map[string]any) { c["email"] = "someone-else@gmail.com" })
	internalAssertDenied(t, f.cfg(), internalReq(raw))
}

func TestAuthenticateInternalRejectsMissingEmailClaim(t *testing.T) {
	f := newInternalFixture(t)
	raw := f.token(t, func(_, c map[string]any) { delete(c, "email") })
	internalAssertDenied(t, f.cfg(), internalReq(raw))
}

// 白名单比对是小写全等：配置里写了大写 / 带空格不该变成一次线上排查。
func TestAuthenticateInternalAllowlistIsCaseInsensitive(t *testing.T) {
	f := newInternalFixture(t)
	cfg := f.cfg()
	cfg.AllowedCallers = []string{"  BP-Scheduler@Oratis-491316.IAM.gserviceaccount.com  "}
	raw := f.token(t, func(_, c map[string]any) { c["email"] = "BP-SCHEDULER@ORATIS-491316.iam.gserviceaccount.com" })

	if _, authErr := AuthenticateInternal(context.Background(), cfg, internalReq(raw)); authErr != nil {
		t.Fatalf("大小写不该影响白名单命中，实得 %v", authErr)
	}
}

// ---- 🔴 配置缺失 → 整条内部面拒绝 ----

// 白名单为空 or aud 为空 → 整条内部面拒绝，不是「跳过那项校验」。
//
// 这里刻意用一份**完全合法**的 token：如果实现是「aud 为空就跳过 aud 校验」
// 或「白名单为空就放行任意 SA」，这些用例会通过 —— 而那等于把
// 全站流量计数的清零按钮放到了公网上。
func TestAuthenticateInternalDeniesWholePlaneWhenUnconfigured(t *testing.T) {
	for _, tc := range []struct {
		name string
		mut  func(*InternalAuthConfig)
	}{
		{"aud 为空", func(c *InternalAuthConfig) { c.Audience = "" }},
		{"白名单为空", func(c *InternalAuthConfig) { c.AllowedCallers = nil }},
		{"白名单是空切片", func(c *InternalAuthConfig) { c.AllowedCallers = []string{} }},
		{"没有 JWKS", func(c *InternalAuthConfig) { c.Keys = nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newInternalFixture(t)
			cfg := f.cfg()
			tc.mut(&cfg)

			internalAssertDenied(t, cfg, internalReq(f.token(t, nil)))
			// 一个都不放进去：连验签都不该发生。
			if f.keys.calls != 0 {
				t.Fatal("配置缺失必须在最前面短路")
			}
		})
	}
}

// ---- happy path ----

func TestAuthenticateInternalHappyPath(t *testing.T) {
	f := newInternalFixture(t)

	caller, authErr := AuthenticateInternal(context.Background(), f.cfg(), internalReq(f.token(t, nil)))
	if authErr != nil {
		t.Fatalf("合法 token 应放行，实得 %v", authErr)
	}
	// 九个任务端点都会写库，出事时第一个问题是「这次是谁触发的」。
	if caller.Email != internalTestCaller {
		t.Fatalf("调用方 email = %q", caller.Email)
	}
	if caller.Subject != internalTestSubject {
		t.Fatalf("sub = %q（email 可以改，sub 不会）", caller.Subject)
	}
	if caller.Audience != internalTestAudience {
		t.Fatalf("aud = %q", caller.Audience)
	}
	if !caller.ExpiresAt.Equal(time.Unix(f.now.Add(time.Hour).Unix(), 0)) {
		t.Fatalf("exp = %v", caller.ExpiresAt)
	}
}

// 无 scheme 的 iss 也要认：Google 的文档与官方校验库长期同时承认两个字符串，
// 只认带 scheme 的那个，一旦 Google 换回旧形式就是六条定时任务同时 403。
func TestAuthenticateInternalAcceptsBothGoogleIssuerForms(t *testing.T) {
	for _, iss := range googleIssuers {
		f := newInternalFixture(t)
		raw := f.token(t, func(_, c map[string]any) { c["iss"] = iss })
		if _, authErr := AuthenticateInternal(context.Background(), f.cfg(), internalReq(raw)); authErr != nil {
			t.Fatalf("iss %q 应被接受，实得 %v", iss, authErr)
		}
	}
}

// exp 恰好落在容差边界上。不留容差的后果是 NTP 抖动表现为「随机 403」；
// 容差无限大则等于取消了过期。
func TestAuthenticateInternalLeewayBoundary(t *testing.T) {
	t.Run("刚过期但在容差内", func(t *testing.T) {
		f := newInternalFixture(t)
		raw := f.token(t, func(_, c map[string]any) {
			c["exp"] = f.now.Add(-defaultInternalLeeway / 2).Unix()
		})
		if _, authErr := AuthenticateInternal(context.Background(), f.cfg(), internalReq(raw)); authErr != nil {
			t.Fatalf("容差内应放行，实得 %v", authErr)
		}
	})

	t.Run("超出容差", func(t *testing.T) {
		f := newInternalFixture(t)
		raw := f.token(t, func(_, c map[string]any) {
			c["exp"] = f.now.Add(-defaultInternalLeeway - time.Minute).Unix()
		})
		internalAssertDenied(t, f.cfg(), internalReq(raw))
	})
}

// ---- 上下文与中间件形态 ----

func TestInternalCallerContextRoundTripAndIsolation(t *testing.T) {
	if _, ok := InternalCallerFrom(context.Background()); ok {
		t.Fatal("空上下文不该取到调用方")
	}
	want := &InternalCaller{Email: internalTestCaller}
	got, ok := InternalCallerFrom(WithInternalCaller(context.Background(), want))
	if !ok || got != want {
		t.Fatalf("上下文往返失败: %v %v", got, ok)
	}
	// 四套身份必须在互不相通的键空间里。
	ctx := WithInternalCaller(context.Background(), want)
	if _, ok := UserFrom(ctx); ok {
		t.Fatal("内部调用方上下文不该被 UserFrom 取到")
	}
	if _, ok := NodeAuthFrom(ctx); ok {
		t.Fatal("内部调用方上下文不该被 NodeAuthFrom 取到")
	}
	if _, ok := AdminFrom(ctx); ok {
		t.Fatal("内部调用方上下文不该被 AdminFrom 取到")
	}
	if _, ok := InternalCallerFrom(WithAdmin(context.Background(), &AdminAuth{AdminID: 1})); ok {
		t.Fatal("管理员上下文不该被 InternalCallerFrom 取到")
	}
}

func TestRequireInternalMiddleware(t *testing.T) {
	t.Run("失败时写 403 且不进 handler", func(t *testing.T) {
		f := newInternalFixture(t)
		reached := false
		h := RequireInternal(f.cfg())(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached = true }))

		w := httptest.NewRecorder()
		h.ServeHTTP(w, internalReq(""))

		if reached {
			t.Fatal("鉴权失败不该走到 handler")
		}
		if w.Code != http.StatusForbidden {
			t.Fatalf("应 403，实得 %d", w.Code)
		}
		var body struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatalf("响应不是合法 JSON: %v", err)
		}
		if body.Error.Code != "AUTH_PERMISSION_DENIED" {
			t.Fatalf("错误码 = %q", body.Error.Code)
		}
	})

	t.Run("成功时注入上下文", func(t *testing.T) {
		f := newInternalFixture(t)
		var seen *InternalCaller
		h := RequireInternal(f.cfg())(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			seen, _ = InternalCallerFrom(r.Context())
		}))

		h.ServeHTTP(httptest.NewRecorder(), internalReq(f.token(t, nil)))

		if seen == nil || seen.Email != internalTestCaller {
			t.Fatalf("handler 未拿到调用方身份: %+v", seen)
		}
	})
}

// ---- Google JWKS 解析 ----

// 低于 2048 位的 RSA 密钥在任何情况下都不该被接受：一把 512 位的密钥
// 可以被当场分解，而验签会「成功」。这是纵深防御 —— 万一 JWKS 地址被指到别处。
func TestParseGoogleJWKSRejectsWeakModulus(t *testing.T) {
	weak, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatalf("生成弱密钥失败: %v", err)
	}
	body := internalTestJWKSBody(t, "weak-kid", &weak.PublicKey)
	if _, err := parseGoogleJWKS(body); err == nil {
		t.Fatal("1024 位模数必须被拒 —— 低于 2048 位的密钥不该出现在任何 JWKS 里")
	}
}

// 轮换期间 JWKS 里同时存在多把密钥，其中一把坏掉不该让整份作废
// （那会让所有内部任务一起挂）。
func TestParseGoogleJWKSSkipsUnusableKeysButKeepsGoodOnes(t *testing.T) {
	good, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("生成密钥失败: %v", err)
	}
	body := []byte(fmt.Sprintf(`{"keys":[
		{"kty":"EC","kid":"ec-key","crv":"P-256","x":"AA","y":"BB"},
		{"kty":"RSA","kid":"enc-key","use":"enc","n":%q,"e":"AQAB"},
		{"kty":"RSA","kid":"good","use":"sig","alg":"RS256","n":%q,"e":"AQAB"}]}`,
		base64.RawURLEncoding.EncodeToString(good.PublicKey.N.Bytes()),
		base64.RawURLEncoding.EncodeToString(good.PublicKey.N.Bytes())))

	keys, err := parseGoogleJWKS(body)
	if err != nil {
		t.Fatalf("不该整份作废: %v", err)
	}
	if _, ok := keys["good"]; !ok {
		t.Fatal("可用的 RS256 公钥丢了")
	}
	if _, ok := keys["enc-key"]; ok {
		t.Fatal("use=enc 的键不该被收进验签密钥集")
	}
	if len(keys) != 1 {
		t.Fatalf("应只收 1 把，实得 %d", len(keys))
	}
}

func internalTestJWKSBody(t *testing.T, kid string, pub *rsa.PublicKey) []byte {
	t.Helper()
	e := big.NewInt(int64(pub.E)).Bytes()
	return []byte(fmt.Sprintf(`{"keys":[{"kty":"RSA","kid":%q,"use":"sig","alg":"RS256","n":%q,"e":%q}]}`,
		kid,
		base64.RawURLEncoding.EncodeToString(pub.N.Bytes()),
		base64.RawURLEncoding.EncodeToString(e)))
}

// kid 由**请求方**控制。没有节流的话，一串随手编的 kid 会让我们对
// googleapis.com 发起等量的出站请求 —— 用一个未认证端点把我们变成机器人，
// 顺带在 Google 那边招来限流，把真正的定时任务一起挡掉。
//
// 这个用例起的是 httptest 的本地回环服务，不出机器。
func TestGoogleJWKSRefetchIsThrottled(t *testing.T) {
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("生成密钥失败: %v", err)
	}
	fetches := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fetches++
		_, _ = w.Write(internalTestJWKSBody(t, internalTestKID, &priv.PublicKey))
	}))
	defer srv.Close()

	now := internalTestNow
	g := NewGoogleJWKS(srv.Client())
	g.url = srv.URL
	g.nowFn = func() time.Time { return now }

	if _, err := g.KeyFor(context.Background(), internalTestKID); err != nil {
		t.Fatalf("首次取公钥应成功: %v", err)
	}
	if fetches != 1 {
		t.Fatalf("应拉取一次，实得 %d", fetches)
	}
	for i := range 10 {
		if _, err := g.KeyFor(context.Background(), fmt.Sprintf("bogus-%d", i)); err == nil {
			t.Fatal("未知 kid 必须报错")
		}
	}
	if fetches != 1 {
		t.Fatalf("未知 kid 不该逐个触发出站请求，实得 %d 次 —— 这是一个放大器", fetches)
	}

	// 过了节流窗口之后允许再拉一次（否则密钥轮换后新 kid 永远拿不到）。
	now = now.Add(googleJWKSMinRefetch + time.Second)
	if _, err := g.KeyFor(context.Background(), "still-bogus"); err == nil {
		t.Fatal("未知 kid 必须报错")
	}
	if fetches != 2 {
		t.Fatalf("节流窗口过后应允许刷新一次，实得 %d", fetches)
	}
}

// Cache-Control 的 max-age 决定缓存寿命，但必须夹在上下界里：
// 异常的极小值会让我们每次请求都去拉，极大值会让轮换后的新 kid 迟迟拿不到。
func TestGoogleJWKSMaxAgeParsing(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want time.Duration
		ok   bool
	}{
		{"public, max-age=3600, must-revalidate", time.Hour, true},
		{"MAX-AGE=120", 2 * time.Minute, true},
		{"no-store", 0, false},
		{"max-age=abc", 0, false},
		{"max-age=0", 0, false},
		{"", 0, false},
	} {
		got, ok := googleJWKSMaxAge(tc.in)
		if ok != tc.ok || got != tc.want {
			t.Errorf("googleJWKSMaxAge(%q) = %v,%v；期望 %v,%v", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}

// ---- 任务侧幂等键 ----

// 长度前缀而不是分隔符拼接：`["ab","c"]` 与 `["a","bc"]` 用分隔符拼出来可能相同，
// 于是两份不同的工作共用一个幂等键 —— 第二份会被**静默**当成重复投递丢掉。
func TestCanonicalTaskPartsIsUnambiguous(t *testing.T) {
	a := string(canonicalTaskParts([]string{"ab", "c"}))
	b := string(canonicalTaskParts([]string{"a", "bc"}))
	if a == b {
		t.Fatalf("两组不同分量拼出了同一个键 %q —— 一份工作会被当成另一份的重复投递", a)
	}
	if string(canonicalTaskParts([]string{"x"})) == string(canonicalTaskParts([]string{"", "x"})) {
		t.Fatal("空分量必须参与区分")
	}
}

// 任务名会进主键与 endpoint 列，放任任意字符串等于把键空间交给调用方。
func TestValidInternalTaskName(t *testing.T) {
	for _, ok := range []string{"traffic-batch", "stat-rollup", "a", "task-9"} {
		if !validInternalTaskName(ok) {
			t.Errorf("%q 应被接受", ok)
		}
	}
	for _, bad := range []string{"", "Traffic-Batch", "traffic_batch", "traffic batch", "task:x",
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"} {
		if validInternalTaskName(bad) {
			t.Errorf("%q 不该被接受", bad)
		}
	}
}

// endpoint 前缀给内部面的键空间加命名空间：不加的话，一个叫得巧的任务名
// 可以与某个 operationID 撞上，于是两条互不相干的路径共用同一段键空间。
func TestInternalTaskKeyIsNamespacedAndBounded(t *testing.T) {
	k := internalTaskKey("traffic-batch", canonicalTaskParts([]string{"batch-123"}))
	if len(k) > 128 {
		t.Fatalf("键长 %d 超出 httpx 的上限", len(k))
	}
	if k[:len(internalTaskEndpointPrefix)] != internalTaskEndpointPrefix {
		t.Fatalf("键缺少命名空间前缀: %q", k)
	}
	if k == internalTaskKey("traffic-reset", canonicalTaskParts([]string{"batch-123"})) {
		t.Fatal("不同任务名必须算出不同的键")
	}
}

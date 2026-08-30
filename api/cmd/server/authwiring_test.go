// 管理面 / 内部面接线的装配测试。
//
// 背景：这 70 个 operation（61 个 admin + 9 个 internal task）此前共用一条
// 「鉴权未实现，一律 501」的分支。501 当时**兼职**充当了它们的防线 ——
// 一条把「没凭据」与「handler 没写」压成同一个响应的捷径。
// 本次接线把两件事拆开：鉴权归中间件（403），未实现归 Unimplemented（501）。
//
// # 每个用例为什么必须存在
//
//	TestAdminAndInternalOperationsRejectUnauthenticated
//	  🔴 **安全红线的直接证据。** 逐个遍历全部 70 个 operation，每一个都断言：
//	  没有凭据 / 拿伪造凭据时，**内层 handler 一次都没有被调用**，且响应是 403。
//	  这是「拆分之后没有任何一个端点变成无鉴权」的穷举证明 ——
//	  不是抽查一个代表，因为漏挂鉴权恰恰是**逐条**发生的（authmap.go 顶部那次
//	  PushUniProxyStatus 事故就是一行拼错）。
//
//	TestAdminAndInternalOperationsDenyWhenUnconfigured
//	  未配置 audience 时仍然是 403，不是放行。这一条与上一条不同：
//	  上一条证明「凭据不对会被拒」，这一条证明「配置漏了也会被拒」。
//	  两者会以完全不同的方式失效。
//
//	TestAdminAndInternalOperationsAdmitValidCredentials
//	  反向证明：拿**正确**凭据时内层 handler 确实被调用了。
//	  少了它，上面两条用一个「无条件 403」的实现也能全绿 ——
//	  那不是鉴权，那是把端点关掉。
package main

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sort"
	"testing"
	"time"

	"github.com/oratis/babelplus/api/internal/gen"
	mw "github.com/oratis/babelplus/api/internal/middleware"
)

const (
	wiringIAPAudience      = "/projects/123456789012/global/backendServices/9876543210"
	wiringIAPKID           = "wiring-iap-kid"
	wiringOIDCAudience     = "https://bp-api-abcdef1234.a.run.app"
	wiringOIDCKID          = "wiring-google-kid"
	wiringAdminEmail       = "ops@babel.plus"
	wiringAdminSubject     = "accounts.google.com:1122334455667788"
	wiringInternalCallerSA = "bp-scheduler@oratis-491316.iam.gserviceaccount.com"
)

var wiringNow = time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)

// ---- 假依赖 ----

type wiringIAPKeys struct{ pub *ecdsa.PublicKey }

func (k wiringIAPKeys) PublicKey(context.Context, string) (*ecdsa.PublicKey, error) {
	return k.pub, nil
}

type wiringGoogleKeys struct{ pub *rsa.PublicKey }

func (k wiringGoogleKeys) KeyFor(context.Context, string) (*rsa.PublicKey, error) {
	return k.pub, nil
}

// wiringAdminDir 记录被查库的次数，用来断言「未验签的凭据不会走到数据库」。
type wiringAdminDir struct{ queries int }

func (d *wiringAdminDir) LookupAdminByIAPEmail(context.Context, string) (mw.AdminRecord, error) {
	d.queries++
	return mw.AdminRecord{
		ID: 7, Email: wiringAdminEmail, Role: mw.RoleOwner, IAPSubject: wiringAdminSubject,
	}, nil
}

func (d *wiringAdminDir) LookupAdminByID(context.Context, int64) (mw.AdminRecord, error) {
	d.queries++
	return mw.AdminRecord{
		ID: 7, Email: wiringAdminEmail, Role: mw.RoleOwner, IAPSubject: wiringAdminSubject,
	}, nil
}

// ---- 装配 ----

type wiringHarness struct {
	iapPriv    *ecdsa.PrivateKey
	oidcPriv   *rsa.PrivateKey
	adminDir   *wiringAdminDir
	adminCfg   mw.AdminAuthConfig
	internCfg  mw.InternalAuthConfig
	middleware gen.StrictMiddlewareFunc
}

func newWiringHarness(t *testing.T, configured bool) *wiringHarness {
	t.Helper()
	iapPriv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("生成 ES256 密钥失败: %v", err)
	}
	oidcPriv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("生成 RSA 密钥失败: %v", err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	dir := &wiringAdminDir{}

	adminCfg := mw.AdminAuthConfig{
		Keys:   wiringIAPKeys{&iapPriv.PublicKey},
		DB:     dir,
		Logger: logger,
		Now:    func() time.Time { return wiringNow },
	}
	internCfg := mw.InternalAuthConfig{
		Keys:   wiringGoogleKeys{&oidcPriv.PublicKey},
		Logger: logger,
		Now:    func() time.Time { return wiringNow },
	}
	if configured {
		adminCfg.IAPAudience = wiringIAPAudience
		internCfg.Audience = wiringOIDCAudience
		internCfg.AllowedCallers = []string{wiringInternalCallerSA}
	}

	h := &wiringHarness{
		iapPriv: iapPriv, oidcPriv: oidcPriv, adminDir: dir,
		adminCfg: adminCfg, internCfg: internCfg,
	}
	// 节点面 / 用户面的配置在这组用例里用不到（不会走到那两个分支），
	// 但仍然逐个传进去 —— authMiddleware 的四个独立参数就是为了让
	// 「谁能拿到哪一套配置」在调用点上一眼可见。
	h.middleware = authMiddleware(mw.NodeAuthConfig{Logger: logger},
		mw.UserAuthConfig{Logger: logger}, adminCfg, internCfg)
	return h
}

// sortedOps 让遍历顺序稳定，失败信息才好读。
func sortedOps(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k, v := range m {
		if v {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// wiringSpy 是一个「被调用就说明鉴权放行了」的内层 handler。
// 它刻意返回 200 —— 真实的 admin handler 将来实现之后就是这个形状。
func wiringSpy(called *bool) gen.StrictHandlerFunc {
	return func(_ context.Context, w http.ResponseWriter, _ *http.Request, _ any) (any, error) {
		*called = true
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
		return nil, nil
	}
}

func adminRequest(header, value string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/api/v1/admin/orders/T1/mark-paid", nil)
	if header != "" {
		r.Header.Set(header, value)
	}
	return r
}

func internalRequest(header, value string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/internal/tasks/traffic-reset", nil)
	if header != "" {
		r.Header.Set(header, value)
	}
	return r
}

// ---- 🔴 安全红线 ----

// 没有正确的 IAP assertion / OIDC token 时，这 70 个端点**一个都不许**
// 走到内层 handler，响应必须是 403。
//
// 逐个遍历而不是抽查一个：漏挂鉴权是**逐条**发生的
// （authmap.go 顶部记着的那次事故就是一行 operationID 拼错）。
func TestAdminAndInternalOperationsRejectUnauthenticated(t *testing.T) {
	h := newWiringHarness(t, true)

	// 攻击者自签的凭据：kid 指向我们信任的那把公钥，但签名是别人的私钥做的。
	evilES, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("生成攻击者 ES256 密钥失败: %v", err)
	}
	evilRSA, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("生成攻击者 RSA 密钥失败: %v", err)
	}

	adminAttempts := []struct {
		name string
		req  func() *http.Request
	}{
		{"完全不带 IAP 断言头", func() *http.Request { return adminRequest("", "") }},
		{"随手编的断言", func() *http.Request { return adminRequest(mw.IAPAssertionHeader, "not-a-jwt") }},
		{"攻击者自签的断言", func() *http.Request {
			return adminRequest(mw.IAPAssertionHeader, wiringSignES256(t, evilES, validIAPClaims()))
		}},
		{"alg none 的断言", func() *http.Request {
			return adminRequest(mw.IAPAssertionHeader,
				wiringJoin(t, map[string]any{"alg": "none", "kid": wiringIAPKID}, validIAPClaims())+".")
		}},
		{"aud 是别的服务", func() *http.Request {
			c := validIAPClaims()
			c["aud"] = "/projects/999999999999/global/backendServices/1111111111"
			return adminRequest(mw.IAPAssertionHeader, wiringSignES256(t, h.iapPriv, c))
		}},
		{"已过期的断言", func() *http.Request {
			c := validIAPClaims()
			c["exp"] = wiringNow.Add(-time.Hour).Unix()
			return adminRequest(mw.IAPAssertionHeader, wiringSignES256(t, h.iapPriv, c))
		}},
		{"用户面的 Bearer token 换不到管理面", func() *http.Request {
			r := adminRequest("Authorization", "Bearer kQ2mXv9pL4nR8tZ1wY6bC3dF5gH7jK0sA2eU4iO6xYz")
			return r
		}},
	}

	for _, op := range sortedOps(adminOperations) {
		for _, attempt := range adminAttempts {
			called := false
			rec := httptest.NewRecorder()
			wrapped := h.middleware(wiringSpy(&called), op)

			if _, err := wrapped(context.Background(), rec, attempt.req(), nil); err != nil {
				t.Fatalf("%s / %s：中间件不该返回 error（响应已写完），实得 %v", op, attempt.name, err)
			}
			if called {
				t.Fatalf("🔴 %s / %s：没有合法 IAP 断言却走到了 handler —— "+
					"这个端点实现的那一刻就是一个无鉴权的管理端点", op, attempt.name)
			}
			if rec.Code != http.StatusForbidden {
				t.Fatalf("🔴 %s / %s：状态码 = %d，必须是 403；body=%s",
					op, attempt.name, rec.Code, rec.Body.String())
			}
			assertEnvelopeCode(t, rec, "AUTH_PERMISSION_DENIED")
		}
	}

	internalAttempts := []struct {
		name string
		req  func() *http.Request
	}{
		{"完全不带 Authorization", func() *http.Request { return internalRequest("", "") }},
		{"随手编的 token", func() *http.Request { return internalRequest("Authorization", "Bearer not-a-jwt") }},
		{"攻击者自签的 ID token", func() *http.Request {
			return internalRequest("Authorization", "Bearer "+wiringSignRS256(t, evilRSA, validOIDCClaims()))
		}},
		{"alg none", func() *http.Request {
			return internalRequest("Authorization", "Bearer "+
				wiringJoin(t, map[string]any{"alg": "none", "kid": wiringOIDCKID}, validOIDCClaims())+".")
		}},
		{"email_verified 不为 true", func() *http.Request {
			c := validOIDCClaims()
			c["email_verified"] = false
			return internalRequest("Authorization", "Bearer "+wiringSignRS256(t, h.oidcPriv, c))
		}},
		{"调用方不在白名单", func() *http.Request {
			c := validOIDCClaims()
			c["email"] = "someone-else@gmail.com"
			return internalRequest("Authorization", "Bearer "+wiringSignRS256(t, h.oidcPriv, c))
		}},
		{"aud 是别的服务", func() *http.Request {
			c := validOIDCClaims()
			c["aud"] = "https://other-service-abcdef1234.a.run.app"
			return internalRequest("Authorization", "Bearer "+wiringSignRS256(t, h.oidcPriv, c))
		}},
		{"已过期", func() *http.Request {
			c := validOIDCClaims()
			c["exp"] = wiringNow.Add(-2 * time.Hour).Unix()
			return internalRequest("Authorization", "Bearer "+wiringSignRS256(t, h.oidcPriv, c))
		}},
	}

	for _, op := range sortedOps(internalTaskOperations) {
		for _, attempt := range internalAttempts {
			called := false
			rec := httptest.NewRecorder()
			wrapped := h.middleware(wiringSpy(&called), op)

			if _, err := wrapped(context.Background(), rec, attempt.req(), nil); err != nil {
				t.Fatalf("%s / %s：中间件不该返回 error，实得 %v", op, attempt.name, err)
			}
			if called {
				t.Fatalf("🔴 %s / %s：没有合法 OIDC token 却走到了 handler —— "+
					"这批端点没有人类界面，路径也不是秘密，保护它们的只有 token", op, attempt.name)
			}
			if rec.Code != http.StatusForbidden {
				t.Fatalf("🔴 %s / %s：状态码 = %d，必须是 403；body=%s",
					op, attempt.name, rec.Code, rec.Body.String())
			}
			assertEnvelopeCode(t, rec, "AUTH_PERMISSION_DENIED")
		}
	}

	// 未验签的凭据一次都不该走到 admin_users 查询。
	if h.adminDir.queries != 0 {
		t.Fatalf("未验签的断言触发了 %d 次数据库查询 —— 那是给未认证输入开的一条查表路径",
			h.adminDir.queries)
	}
}

// 配置漏了（没配 audience / 白名单）时仍然必须是 403，不是放行。
//
// 与上一条的失效方式完全不同：上一条挡的是「凭据不对」，
// 这一条挡的是「一个漏配环境变量的实例把整个后台开放给公网」。
func TestAdminAndInternalOperationsDenyWhenUnconfigured(t *testing.T) {
	h := newWiringHarness(t, false)

	// 刻意用**完全合法**的凭据：如果实现是「没配就跳过校验」，这里会放行。
	adminReq := adminRequest(mw.IAPAssertionHeader, wiringSignES256(t, h.iapPriv, validIAPClaims()))
	internalReq := internalRequest("Authorization", "Bearer "+wiringSignRS256(t, h.oidcPriv, validOIDCClaims()))

	for _, tc := range []struct {
		ops map[string]bool
		req *http.Request
	}{
		{adminOperations, adminReq},
		{internalTaskOperations, internalReq},
	} {
		for _, op := range sortedOps(tc.ops) {
			called := false
			rec := httptest.NewRecorder()
			wrapped := h.middleware(wiringSpy(&called), op)
			if _, err := wrapped(context.Background(), rec, tc.req, nil); err != nil {
				t.Fatalf("%s：中间件不该返回 error，实得 %v", op, err)
			}
			if called {
				t.Fatalf("🔴 %s：未配置 audience 时放行了 —— "+
					"「配置漏了」的现象必须是「进不去」，不能是「谁都进得去」", op)
			}
			if rec.Code == http.StatusOK {
				t.Fatalf("🔴 %s：未配置时返回了 200", op)
			}
			if rec.Code != http.StatusForbidden {
				t.Fatalf("%s：状态码 = %d，期望 403", op, rec.Code)
			}
		}
	}
}

// 反向证明：拿**正确**凭据时内层 handler 确实被调用，且身份进了上下文。
//
// 少了这一条，上面两条用一个「无条件 403」的实现也能全绿 ——
// 那不是鉴权，那是把 70 个端点关掉。
func TestAdminAndInternalOperationsAdmitValidCredentials(t *testing.T) {
	h := newWiringHarness(t, true)

	t.Run("管理面", func(t *testing.T) {
		var seen *mw.AdminAuth
		wrapped := h.middleware(func(ctx context.Context, w http.ResponseWriter, _ *http.Request, _ any) (any, error) {
			seen, _ = mw.AdminFrom(ctx)
			w.WriteHeader(http.StatusOK)
			return nil, nil
		}, "GetAdminDashboard")

		rec := httptest.NewRecorder()
		req := adminRequest(mw.IAPAssertionHeader, wiringSignES256(t, h.iapPriv, validIAPClaims()))
		if _, err := wrapped(context.Background(), rec, req, nil); err != nil {
			t.Fatalf("不该返回 error: %v", err)
		}
		if rec.Code != http.StatusOK {
			t.Fatalf("合法断言应放行，实得 %d；body=%s", rec.Code, rec.Body.String())
		}
		if seen == nil || seen.AdminID != 7 {
			t.Fatalf("handler 没从上下文里拿到管理员身份: %+v", seen)
		}
		if seen.Email != wiringAdminEmail {
			t.Fatalf("Email 必须取 admin_users 那一份（审计要记它），实得 %q", seen.Email)
		}
	})

	t.Run("内部面", func(t *testing.T) {
		var seen *mw.InternalCaller
		wrapped := h.middleware(func(ctx context.Context, w http.ResponseWriter, _ *http.Request, _ any) (any, error) {
			seen, _ = mw.InternalCallerFrom(ctx)
			w.WriteHeader(http.StatusOK)
			return nil, nil
		}, "RunTrafficResetTask")

		rec := httptest.NewRecorder()
		req := internalRequest("Authorization", "Bearer "+wiringSignRS256(t, h.oidcPriv, validOIDCClaims()))
		if _, err := wrapped(context.Background(), rec, req, nil); err != nil {
			t.Fatalf("不该返回 error: %v", err)
		}
		if rec.Code != http.StatusOK {
			t.Fatalf("合法 token 应放行，实得 %d；body=%s", rec.Code, rec.Body.String())
		}
		// 九个任务端点都会写库，出事时第一个问题是「这次是谁触发的」。
		if seen == nil || seen.Email != wiringInternalCallerSA {
			t.Fatalf("handler 没从上下文里拿到调用方: %+v", seen)
		}
	})
}

// 管理面身份与内部面身份必须落在互不相通的键空间里：
// 一次 iota 顺序调整不该让内部任务端点拿到一个管理员身份。
func TestAdminAndInternalContextsStayDisjointThroughTheMiddleware(t *testing.T) {
	h := newWiringHarness(t, true)

	wrapped := h.middleware(func(ctx context.Context, w http.ResponseWriter, _ *http.Request, _ any) (any, error) {
		if _, ok := mw.AdminFrom(ctx); ok {
			t.Error("内部任务的上下文里不该有管理员身份")
		}
		if _, ok := mw.UserFrom(ctx); ok {
			t.Error("内部任务的上下文里不该有用户身份")
		}
		if _, ok := mw.NodeAuthFrom(ctx); ok {
			t.Error("内部任务的上下文里不该有节点身份")
		}
		w.WriteHeader(http.StatusOK)
		return nil, nil
	}, "RunTrafficResetTask")

	rec := httptest.NewRecorder()
	req := internalRequest("Authorization", "Bearer "+wiringSignRS256(t, h.oidcPriv, validOIDCClaims()))
	if _, err := wrapped(context.Background(), rec, req, nil); err != nil {
		t.Fatalf("不该返回 error: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("合法 token 应放行，实得 %d", rec.Code)
	}
}

// ---- 辅助 ----

func assertEnvelopeCode(t *testing.T, rec *httptest.ResponseRecorder, want string) {
	t.Helper()
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("响应不是合法 JSON: %v；body=%s", err, rec.Body.String())
	}
	if body.Error.Code != want {
		t.Fatalf("error.code = %q，期望 %q", body.Error.Code, want)
	}
}

func validIAPClaims() map[string]any {
	return map[string]any{
		"iss":   mw.IAPIssuer,
		"aud":   wiringIAPAudience,
		"sub":   wiringAdminSubject,
		"email": wiringAdminEmail,
		"exp":   wiringNow.Add(5 * time.Minute).Unix(),
		"iat":   wiringNow.Add(-time.Minute).Unix(),
	}
}

func validOIDCClaims() map[string]any {
	return map[string]any{
		"iss":            "https://accounts.google.com",
		"aud":            wiringOIDCAudience,
		"sub":            "112233445566778899000",
		"email":          wiringInternalCallerSA,
		"email_verified": true,
		"exp":            wiringNow.Add(time.Hour).Unix(),
		"iat":            wiringNow.Add(-time.Minute).Unix(),
	}
}

func wiringJoin(t *testing.T, hdr, claims map[string]any) string {
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

// wiringSignES256 按 JWS 的 P1363 形态（r||s 各 32 字节定长）签一份 IAP 断言。
func wiringSignES256(t *testing.T, priv *ecdsa.PrivateKey, claims map[string]any) string {
	t.Helper()
	signing := wiringJoin(t, map[string]any{"alg": "ES256", "kid": wiringIAPKID, "typ": "JWT"}, claims)
	digest := sha256.Sum256([]byte(signing))
	r, s, err := ecdsa.Sign(rand.Reader, priv, digest[:])
	if err != nil {
		t.Fatalf("签名失败: %v", err)
	}
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])
	return signing + "." + base64.RawURLEncoding.EncodeToString(sig)
}

func wiringSignRS256(t *testing.T, priv *rsa.PrivateKey, claims map[string]any) string {
	t.Helper()
	signing := wiringJoin(t, map[string]any{"alg": "RS256", "kid": wiringOIDCKID, "typ": "JWT"}, claims)
	sum := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, priv, crypto.SHA256, sum[:])
	if err != nil {
		t.Fatalf("签名失败: %v", err)
	}
	return signing + "." + base64.RawURLEncoding.EncodeToString(sig)
}

package middleware

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	dbgen "github.com/oratis/babelplus/api/db/gen"
)

const testPepper = "test-session-pepper"

// 43 字符的 base64url，形态与真实签发的 32 字节 CSPRNG token 一致。
const testToken = "kQ2mXv9pL4nR8tZ1wY6bC3dF5gH7jK0sA2eU4iO6xYz"

// fakeSessions 是 UserSessionReader 的假实现，按 hash 命中。
type fakeSessions struct {
	row     dbgen.GetUserSessionByHashRow
	hit     bool
	err     error
	queried int // 记录查库次数，用来断言「形态不合法不查库」
}

func (f *fakeSessions) GetUserSessionByHash(_ context.Context, _ []byte) (dbgen.GetUserSessionByHashRow, error) {
	f.queried++
	if f.err != nil {
		return dbgen.GetUserSessionByHashRow{}, f.err
	}
	if !f.hit {
		return dbgen.GetUserSessionByHashRow{}, pgx.ErrNoRows
	}
	return f.row, nil
}

func ts(t time.Time) pgtype.Timestamptz { return pgtype.Timestamptz{Time: t, Valid: true} }

func liveSession() dbgen.GetUserSessionByHashRow {
	now := time.Now()
	return dbgen.GetUserSessionByHashRow{
		ID:        77,
		UserID:    42,
		IssuedAt:  ts(now.Add(-time.Hour)),
		ExpiresAt: ts(now.Add(30 * 24 * time.Hour)),
	}
}

func testUserCfg(db UserSessionReader) UserAuthConfig {
	return UserAuthConfig{
		DB:     db,
		Pepper: testPepper,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

func bearerReq(token string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/api/v1/user/me", nil)
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	return r
}

func TestAuthenticateUserBearerHappyPath(t *testing.T) {
	db := &fakeSessions{row: liveSession(), hit: true}
	auth, authErr := AuthenticateUser(context.Background(), testUserCfg(db), bearerReq(testToken))

	if authErr != nil {
		t.Fatalf("应鉴权成功，实得 %v", authErr)
	}
	if auth.UserID != 42 || auth.SessionID != 77 {
		t.Fatalf("身份注入错误: %+v", auth)
	}
	if auth.FromCookie {
		t.Fatal("Bearer 来源不该标记为 cookie")
	}
}

// token 存的是哈希不是明文：同一 token 配不同 pepper 必须算出不同哈希。
func TestHashSessionTokenUsesPepper(t *testing.T) {
	a := HashSessionToken("pepper-a", testToken)
	b := HashSessionToken("pepper-b", testToken)
	if string(a) == string(b) {
		t.Fatal("pepper 未参与哈希 —— 泄库后可离线爆破")
	}
	if len(a) != 32 {
		t.Fatalf("应为 sha256（32 字节），实得 %d", len(a))
	}
	// 同一输入必须稳定，否则每次请求都会认不出自己的会话。
	if string(HashSessionToken("p", testToken)) != string(HashSessionToken("p", testToken)) {
		t.Fatal("哈希不稳定")
	}
}

// 形态不合法必须**不查库** —— 与节点鉴权同一纪律。
func TestAuthenticateUserMalformedTokenSkipsDB(t *testing.T) {
	cases := []struct {
		name  string
		token string
	}{
		{"太短", "abc"},
		{"太长", longToken(200)},
		{"含非法字符", "kQ2mXv9pL4nR8tZ1wY6bC3dF5gH7jK0sA2eU4iO6x.z"},
		{"含空格", "kQ2mXv9pL4nR8tZ1wY6b C3dF5gH7jK0sA2eU4iO6xy"},
		{"base64 填充符", "kQ2mXv9pL4nR8tZ1wY6bC3dF5gH7jK0sA2eU4iO6x=="},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := &fakeSessions{row: liveSession(), hit: true}
			_, authErr := AuthenticateUser(context.Background(), testUserCfg(db), bearerReq(tc.token))
			if authErr == nil {
				t.Fatal("形态非法应被拒")
			}
			if authErr.Status != http.StatusUnauthorized {
				t.Fatalf("应为 401，实得 %d", authErr.Status)
			}
			if db.queried != 0 {
				t.Fatalf("形态非法不该查库，实际查了 %d 次", db.queried)
			}
		})
	}
}

func TestAuthenticateUserMissingCredential(t *testing.T) {
	db := &fakeSessions{row: liveSession(), hit: true}
	_, authErr := AuthenticateUser(context.Background(), testUserCfg(db), bearerReq(""))

	if authErr == nil || authErr.Status != http.StatusUnauthorized {
		t.Fatalf("缺凭据应 401，实得 %v", authErr)
	}
	if authErr.Code != "AUTH_TOKEN_INVALID" {
		t.Fatalf("code = %q", authErr.Code)
	}
	if db.queried != 0 {
		t.Fatal("缺凭据不该查库")
	}
}

// 非 Bearer 的 Authorization 头不回退到 cookie。
func TestAuthenticateUserNonBearerDoesNotFallBackToCookie(t *testing.T) {
	db := &fakeSessions{row: liveSession(), hit: true}
	cfg := testUserCfg(db)
	cfg.AllowCookie = true

	r := httptest.NewRequest(http.MethodGet, "/api/v1/user/me", nil)
	r.Header.Set("Authorization", "Basic dXNlcjpwYXNz")
	r.AddCookie(&http.Cookie{Name: SessionCookieName, Value: testToken})

	if _, authErr := AuthenticateUser(context.Background(), cfg, r); authErr == nil {
		t.Fatal("坏的 Authorization 头不该被 cookie 救回来")
	}
}

func TestAuthenticateUserCookie(t *testing.T) {
	newReq := func() *http.Request {
		r := httptest.NewRequest(http.MethodGet, "/api/v1/user/me", nil)
		r.AddCookie(&http.Cookie{Name: SessionCookieName, Value: testToken})
		return r
	}

	t.Run("默认关闭 cookie 形态", func(t *testing.T) {
		db := &fakeSessions{row: liveSession(), hit: true}
		_, authErr := AuthenticateUser(context.Background(), testUserCfg(db), newReq())
		if authErr == nil {
			t.Fatal("AllowCookie 零值应为 false（fail-closed），不该认这个 cookie")
		}
		if db.queried != 0 {
			t.Fatal("cookie 未开启时不该查库")
		}
	})

	t.Run("显式开启后可用", func(t *testing.T) {
		db := &fakeSessions{row: liveSession(), hit: true}
		cfg := testUserCfg(db)
		cfg.AllowCookie = true
		auth, authErr := AuthenticateUser(context.Background(), cfg, newReq())
		if authErr != nil {
			t.Fatalf("应成功，实得 %v", authErr)
		}
		if !auth.FromCookie {
			t.Fatal("cookie 来源应被标记，CSRF 判断要用")
		}
	})
}

// query string 形态永不接受：凭据会进 access log / Referer / 浏览器历史。
func TestAuthenticateUserRejectsQueryToken(t *testing.T) {
	db := &fakeSessions{row: liveSession(), hit: true}
	r := httptest.NewRequest(http.MethodGet, "/api/v1/user/me?token="+testToken, nil)

	if _, authErr := AuthenticateUser(context.Background(), testUserCfg(db), r); authErr == nil {
		t.Fatal("用户面不接受 query token")
	}
	if db.queried != 0 {
		t.Fatal("不该查库")
	}
}

func TestAuthenticateUserSessionNotFound(t *testing.T) {
	// 查询本身带 revoked_at IS NULL AND expires_at > now()，所以
	// 不存在 / 已吊销 / 已过期在这里都表现为 ErrNoRows，且**必须不可区分**。
	db := &fakeSessions{hit: false}
	_, authErr := AuthenticateUser(context.Background(), testUserCfg(db), bearerReq(testToken))

	if authErr == nil || authErr.Status != http.StatusUnauthorized {
		t.Fatalf("应 401，实得 %v", authErr)
	}
	if authErr.Code != "AUTH_TOKEN_INVALID" {
		t.Fatalf("code = %q，不应泄漏「过期」与「不存在」的区别", authErr.Code)
	}
}

// 纵深防御：即使 SQL 的 WHERE 被改坏，Go 侧仍然要挡住已吊销 / 已过期的行。
func TestAuthenticateUserDefenseInDepth(t *testing.T) {
	t.Run("已吊销", func(t *testing.T) {
		row := liveSession()
		row.RevokedAt = ts(time.Now().Add(-time.Minute))
		db := &fakeSessions{row: row, hit: true}
		if _, authErr := AuthenticateUser(context.Background(), testUserCfg(db), bearerReq(testToken)); authErr == nil {
			t.Fatal("已吊销的会话必须被拒，不能只依赖 SQL 过滤")
		}
	})

	t.Run("已过期", func(t *testing.T) {
		row := liveSession()
		row.ExpiresAt = ts(time.Now().Add(-time.Second))
		db := &fakeSessions{row: row, hit: true}
		if _, authErr := AuthenticateUser(context.Background(), testUserCfg(db), bearerReq(testToken)); authErr == nil {
			t.Fatal("已过期的会话必须被拒")
		}
	})

	t.Run("expires_at 为 NULL", func(t *testing.T) {
		row := liveSession()
		row.ExpiresAt = pgtype.Timestamptz{}
		db := &fakeSessions{row: row, hit: true}
		if _, authErr := AuthenticateUser(context.Background(), testUserCfg(db), bearerReq(testToken)); authErr == nil {
			t.Fatal("expires_at 缺失应视为无效，不是永久有效")
		}
	})
}

// 封禁与凭据无效必须走不同的码：403 让前端能显示「账号已被封禁」，
// 401 会让被封用户不停地重新登录。
func TestAuthenticateUserBannedIs403(t *testing.T) {
	row := liveSession()
	row.Banned = true
	db := &fakeSessions{row: row, hit: true}

	_, authErr := AuthenticateUser(context.Background(), testUserCfg(db), bearerReq(testToken))
	if authErr == nil {
		t.Fatal("封禁用户应被拒")
	}
	if authErr.Status != http.StatusForbidden {
		t.Fatalf("封禁应为 403，实得 %d", authErr.Status)
	}
	if authErr.Code != "AUTH_PERMISSION_DENIED" {
		t.Fatalf("code = %q", authErr.Code)
	}
}

// 注销（AnonymizeUser 置 deleted_at + banned）归 401：账号在用户面已不存在。
func TestAuthenticateUserDeletedIs401(t *testing.T) {
	row := liveSession()
	row.Banned = true
	row.UserDeletedAt = ts(time.Now().Add(-time.Hour))
	db := &fakeSessions{row: row, hit: true}

	_, authErr := AuthenticateUser(context.Background(), testUserCfg(db), bearerReq(testToken))
	if authErr == nil || authErr.Status != http.StatusUnauthorized {
		t.Fatalf("已注销账号应 401，实得 %v", authErr)
	}
}

func TestAuthenticateUserDBErrorIs500(t *testing.T) {
	db := &fakeSessions{err: context.DeadlineExceeded}
	_, authErr := AuthenticateUser(context.Background(), testUserCfg(db), bearerReq(testToken))

	if authErr == nil || authErr.Status != http.StatusInternalServerError {
		t.Fatalf("库故障应 500（不是 401），实得 %v", authErr)
	}
	if authErr.Code != "INTERNAL_ERROR" {
		t.Fatalf("code = %q", authErr.Code)
	}
}

func TestUserContextRoundTrip(t *testing.T) {
	if _, ok := UserFrom(context.Background()); ok {
		t.Fatal("空上下文不该取到用户")
	}
	want := &UserAuth{UserID: 42, SessionID: 77}
	got, ok := UserFrom(WithUser(context.Background(), want))
	if !ok || got != want {
		t.Fatalf("上下文往返失败: %v %v", got, ok)
	}
}

// 用户身份与节点身份必须在两个键空间里，互相取不到。
func TestUserAndNodeContextsAreDisjoint(t *testing.T) {
	ctx := WithUser(context.Background(), &UserAuth{UserID: 42})
	if _, ok := NodeAuthFrom(ctx); ok {
		t.Fatal("用户上下文不该被 NodeAuthFrom 取到")
	}
	ctx = WithNodeAuth(context.Background(), &NodeAuth{ServerID: 3})
	if _, ok := UserFrom(ctx); ok {
		t.Fatal("节点上下文不该被 UserFrom 取到")
	}
}

func TestRequireUserMiddleware(t *testing.T) {
	t.Run("失败时写错误信封且不进 handler", func(t *testing.T) {
		db := &fakeSessions{hit: false}
		reached := false
		h := RequireUser(testUserCfg(db))(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			reached = true
		}))

		w := httptest.NewRecorder()
		h.ServeHTTP(w, bearerReq(testToken))

		if reached {
			t.Fatal("鉴权失败不该走到 handler")
		}
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("应 401，实得 %d", w.Code)
		}
		var body struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatalf("响应不是合法 JSON: %v", err)
		}
		if body.Error.Code != "AUTH_TOKEN_INVALID" {
			t.Fatalf("错误码 = %q", body.Error.Code)
		}
	})

	t.Run("成功时注入上下文", func(t *testing.T) {
		db := &fakeSessions{row: liveSession(), hit: true}
		var seen *UserAuth
		h := RequireUser(testUserCfg(db))(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			seen, _ = UserFrom(r.Context())
		}))

		h.ServeHTTP(httptest.NewRecorder(), bearerReq(testToken))

		if seen == nil || seen.UserID != 42 {
			t.Fatalf("handler 未拿到用户身份: %+v", seen)
		}
	})
}

func longToken(n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = 'a'
	}
	return string(b)
}

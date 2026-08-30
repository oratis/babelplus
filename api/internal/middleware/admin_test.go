// 管理面鉴权（admin.go）的测试。
//
// 这条链在被接线之前一行都没有被执行过 —— authmap.go 曾对 61 个 admin operation
// 硬返 501。接线是把「一律拒绝」换成「按凭据放行」，所以本文件必须先证明
// 「按凭据」的每一条判断都真的在拒绝，否则接线那一刻洞就上线了。
//
// # 每个用例为什么必须存在
//
//	TestAuthenticateAdminRejectsForgedAssertion
//	  🔴 本文件最重要的一条。`x-goog-iap-jwt-assertion` 在没有 IAP 的部署形态下
//	  是**任何人都能设的普通请求头**（bp-api 目前 --ingress=all 直接暴露在
//	  *.run.app 上）。这个用例钉住「信任的是头里那个 JWT 的签名，不是头的存在」：
//	  攻击者自签的 ES256、alg:none、HS256 算法混淆（IAP 公钥是公开可下载的）
//	  与纯垃圾串各拒一次，且**都不许查库** —— 查了就等于给未认证输入开了一条查表路径。
//
//	TestAuthenticateAdminDeniesWholePlaneWithoutAudience
//	  未配置 audience 时管理面**整体拒绝**，不是放行。少了这一条，
//	  「配置漏了」的现象就是「谁都能进管理面」，而且是静默的 —— 后台照常打开，
//	  没有任何症状，而这中间任何一个人都能调用 D6（手工标记订单已支付）。
//
//	TestAuthenticateAdminRejectsBadAudIssExp
//	  签名对但 aud / iss / exp 不对，各拒一次。
//	  aud 是「这个断言是发给我们的」的唯一证据：少了它，任何一个同样受 IAP 保护的
//	  服务（哪怕别人项目里的）签出来的断言都能拿到这里用。
//	  exp 缺失也必须当作失败 —— 没有 exp 的 token 永不过期。
//
//	TestAuthenticateAdminUnknownIdentityIs403 / ...DisabledAdminIs403
//	  身份合法但 admin_users 里查不到、或已被禁用 → **403 而不是 401**。
//	  管理面前面站着 IAP，401 会让浏览器认为凭据没给对而反复重走登录流程，
//	  于是一个确定性的拒绝表现为无限跳转。
//	  两者的错误码还必须与「断言坏了」不可区分，否则就是一台账号枚举机。
//
//	TestRequireStepUpRejectsWrongCode / ...ReplayWithinWindowFails
//	  §6.2 L3 的两条：错误码拒绝；**同一 code 在有效窗口内第二次使用必须失败**
//	  （used_totp 防重放）。TOTP 的取值空间只有 10^6，一个能被重放的码等于没有二次验证。
//	  另附「验错码不占用重放槽」—— 顺序反过来就是一个免费的拒绝服务：
//	  任何人拿随机 6 位数就能把管理员真正要用的码提前占掉。
//
//	TestVerifyTOTPCodeWindowBoundary / TestRequireStepUpAcceptsAdjacentSteps
//	  时间窗边界：前后各一个 step 必须收，再多一个必须拒。
//	  放大这个窗口就必须同步放大 used_totp 的清理窗口，否则重放窗口会重新打开。
package middleware

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

const (
	// 形如真实 IAP 断言的 aud：/projects/<PROJECT_NUMBER>/global/backendServices/<ID>
	adminTestAudience = "/projects/123456789012/global/backendServices/9876543210"
	adminTestKID      = "bp-test-iap-kid"
	adminTestEmail    = "ops@babel.plus"
	adminTestSubject  = "accounts.google.com:1122334455667788"
	// base32（RFC 4648，大写无填充）—— 与 decryptTOTPSecret 的明文约定一致。
	adminTestTOTPSecret = "JBSWY3DPEHPK3PXP"
)

// adminTestNow 是所有用例的固定「现在」。用固定时间而不是 time.Now：
// TOTP 与 exp 的边界判断在真实时钟下会在每 30 秒里有一小段时间翻车，
// 那种测试的失败信息与真实 bug 无法区分。
var adminTestNow = time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)

// ---- 假实现 ----

// fakeIAPKeys 是 IAPKeyProvider 的假实现。calls 用来断言「拒绝时不去取公钥」。
type fakeIAPKeys struct {
	keys  map[string]*ecdsa.PublicKey
	err   error
	calls int
}

func (f *fakeIAPKeys) PublicKey(_ context.Context, kid string) (*ecdsa.PublicKey, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	k, ok := f.keys[kid]
	if !ok {
		return nil, fmt.Errorf("未知 kid %q", kid)
	}
	return k, nil
}

// fakeAdminDir 是 AdminDirectory 的假实现，两个计数器用来断言「不该查库时没查库」。
type fakeAdminDir struct {
	rec          AdminRecord
	hit          bool
	err          error
	emailQueries int
	idQueries    int
}

func (f *fakeAdminDir) LookupAdminByIAPEmail(_ context.Context, _ string) (AdminRecord, error) {
	f.emailQueries++
	if f.err != nil {
		return AdminRecord{}, f.err
	}
	if !f.hit {
		return AdminRecord{}, pgx.ErrNoRows
	}
	return f.rec, nil
}

func (f *fakeAdminDir) LookupAdminByID(_ context.Context, _ int64) (AdminRecord, error) {
	f.idQueries++
	if f.err != nil {
		return AdminRecord{}, f.err
	}
	if !f.hit {
		return AdminRecord{}, pgx.ErrNoRows
	}
	return f.rec, nil
}

// fakeReplay 模拟 used_totp 的主键去重语义（撞了就 ErrTOTPCodeUsed）。
type fakeReplay struct {
	used  map[string]bool
	err   error
	calls int
}

func (f *fakeReplay) ClaimTOTPCode(_ context.Context, adminID int64, codeHash []byte) error {
	f.calls++
	if f.err != nil {
		return f.err
	}
	if f.used == nil {
		f.used = map[string]bool{}
	}
	k := fmt.Sprintf("%d:%x", adminID, codeHash)
	if f.used[k] {
		return ErrTOTPCodeUsed
	}
	f.used[k] = true
	return nil
}

// ---- 测试夹具 ----

type adminFixture struct {
	priv    *ecdsa.PrivateKey
	keys    *fakeIAPKeys
	dir     *fakeAdminDir
	replay  *fakeReplay
	totpKey []byte
	now     time.Time
}

func newAdminFixture(t *testing.T) *adminFixture {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("生成 ES256 测试密钥失败: %v", err)
	}
	totpKey := make([]byte, 32)
	if _, err := rand.Read(totpKey); err != nil {
		t.Fatalf("生成 TOTP 加密密钥失败: %v", err)
	}
	return &adminFixture{
		priv: priv,
		keys: &fakeIAPKeys{keys: map[string]*ecdsa.PublicKey{adminTestKID: &priv.PublicKey}},
		dir: &fakeAdminDir{
			hit: true,
			rec: AdminRecord{
				ID: 7,
				// 与断言里的 email 大小写不同，用来钉住「Email 取库里那一份」。
				Email:         "Ops@Babel.Plus",
				Role:          RoleOwner,
				IAPSubject:    adminTestSubject,
				TOTPSecretEnc: adminTestEncryptTOTP(t, totpKey, adminTestTOTPSecret),
				Perms:         AdminPerms{MarkOrderPaid: true, Refund: true},
			},
		},
		replay:  &fakeReplay{},
		totpKey: totpKey,
		now:     adminTestNow,
	}
}

func (f *adminFixture) cfg() AdminAuthConfig {
	return AdminAuthConfig{
		IAPAudience: adminTestAudience,
		Keys:        f.keys,
		DB:          f.dir,
		Replay:      f.replay,
		TOTPKey:     f.totpKey,
		Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		Now:         func() time.Time { return f.now },
	}
}

// validClaims 是一份会通过全部校验的断言载荷。各用例在其上做单点改动。
func (f *adminFixture) validClaims() map[string]any {
	return map[string]any{
		"iss":   IAPIssuer,
		"aud":   adminTestAudience,
		"sub":   adminTestSubject,
		"email": adminTestEmail,
		"exp":   f.now.Add(5 * time.Minute).Unix(),
		"iat":   f.now.Add(-time.Minute).Unix(),
	}
}

// token 用夹具里的私钥签一份断言。mut 可就地改 header / claims。
func (f *adminFixture) token(t *testing.T, mut func(hdr, claims map[string]any)) string {
	t.Helper()
	hdr := map[string]any{"alg": "ES256", "kid": adminTestKID, "typ": "JWT"}
	claims := f.validClaims()
	if mut != nil {
		mut(hdr, claims)
	}
	return adminTestSignES256(t, f.priv, hdr, claims)
}

// adminTestSignES256 按 JWS 的 P1363 形态（r||s 各 32 字节定长）签名。
func adminTestSignES256(t *testing.T, priv *ecdsa.PrivateKey, hdr, claims map[string]any) string {
	t.Helper()
	signing := adminTestJoinSegments(t, hdr, claims)
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

func adminTestJoinSegments(t *testing.T, hdr, claims map[string]any) string {
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

func adminTestEncryptTOTP(t *testing.T, key []byte, secret string) []byte {
	t.Helper()
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatalf("构造 AES 失败: %v", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatalf("构造 GCM 失败: %v", err)
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		t.Fatalf("生成 nonce 失败: %v", err)
	}
	return gcm.Seal(nonce, nonce, []byte(secret), nil)
}

func adminReq(assertion string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/api/v1/admin/orders", nil)
	if assertion != "" {
		r.Header.Set(IAPAssertionHeader, assertion)
	}
	return r
}

// adminTestCodeAt 算出某个时间步的 6 位码。
func adminTestCodeAt(t *testing.T, stepOffset int64, now time.Time) string {
	t.Helper()
	secret, err := decodeBase32Secret(adminTestTOTPSecret)
	if err != nil {
		t.Fatalf("解码测试 secret 失败: %v", err)
	}
	return totpAt(secret, now.Unix()/int64(totpPeriod/time.Second)+stepOffset)
}

// ---- 🔴 伪造断言 ----

// 伪造的 IAP 头必须被拒。这个头在没有 IAP 的部署形态下是普通请求头，
// 任何人都能 `curl -H 'x-goog-iap-jwt-assertion: ...'` 打进来。
//
// 四种伪造形态各拒一次，并且**都不许查 admin_users** —— 未验签的 email
// 是攻击者可控的字符串，拿它去查表等于给一个未认证输入开了一条数据库路径。
func TestAuthenticateAdminRejectsForgedAssertion(t *testing.T) {
	t.Run("攻击者自签的 ES256（签名验不过）", func(t *testing.T) {
		f := newAdminFixture(t)
		evil, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatalf("生成攻击者密钥失败: %v", err)
		}
		// header 里的 kid 仍然指向我们信任的那把公钥，只是签名是别人的私钥做的。
		raw := adminTestSignES256(t,
			evil,
			map[string]any{"alg": "ES256", "kid": adminTestKID, "typ": "JWT"},
			f.validClaims())

		adminAssertDenied(t, f, raw)
	})

	t.Run("alg none（无签名 token）", func(t *testing.T) {
		f := newAdminFixture(t)
		raw := adminTestJoinSegments(t,
			map[string]any{"alg": "none", "kid": adminTestKID}, f.validClaims()) + "."

		adminAssertDenied(t, f, raw)
		if f.keys.calls != 0 {
			t.Fatal("alg 白名单必须在取公钥之前生效")
		}
	})

	t.Run("HS256 算法混淆（IAP 公钥是公开的）", func(t *testing.T) {
		f := newAdminFixture(t)
		// 攻击者把 alg 改成 HS256，指望服务端拿公钥当 HMAC 密钥去验。
		signing := adminTestJoinSegments(t,
			map[string]any{"alg": "HS256", "kid": adminTestKID}, f.validClaims())
		// 「密钥」就是公钥坐标 —— 它对任何人都是公开可下载的，这正是算法混淆的要害。
		pubBytes := append(f.priv.PublicKey.X.Bytes(), f.priv.PublicKey.Y.Bytes()...)
		m := hmac.New(sha256.New, pubBytes)
		m.Write([]byte(signing))
		raw := signing + "." + base64.RawURLEncoding.EncodeToString(m.Sum(nil))

		adminAssertDenied(t, f, raw)
		if f.keys.calls != 0 {
			t.Fatal("alg 白名单必须在取公钥之前生效，否则算法混淆有机可乘")
		}
	})

	t.Run("随手编的字符串", func(t *testing.T) {
		f := newAdminFixture(t)
		adminAssertDenied(t, f, "not-a-jwt")
	})

	t.Run("完全不带这个头", func(t *testing.T) {
		f := newAdminFixture(t)
		adminAssertDenied(t, f, "")
	})
}

// adminAssertDenied 断言「403 + 统一错误码 + 没查过 admin_users」。
func adminAssertDenied(t *testing.T, f *adminFixture, raw string) {
	t.Helper()
	auth, authErr := AuthenticateAdmin(context.Background(), f.cfg(), adminReq(raw))
	if auth != nil {
		t.Fatalf("伪造凭据不该拿到身份: %+v", auth)
	}
	if authErr == nil {
		t.Fatal("伪造凭据必须被拒")
	}
	if authErr.Status != http.StatusForbidden {
		t.Fatalf("管理面失败必须是 403（401 会让浏览器带着 IAP 无限重试），实得 %d", authErr.Status)
	}
	if authErr.Code != "AUTH_PERMISSION_DENIED" {
		t.Fatalf("错误码 = %q，管理面只用 AUTH_PERMISSION_DENIED", authErr.Code)
	}
	if f.dir.emailQueries != 0 {
		t.Fatalf("未验签的断言不该触发 admin_users 查询，实际查了 %d 次", f.dir.emailQueries)
	}
}

// ---- 🔴 未配置 audience → 整体拒绝 ----

// 未配置 audience 时管理面**整体拒绝**，不是「跳过校验」。
// 这里刻意用一份**完全合法**的断言：如果实现是「没配就跳过 aud 校验」，
// 这个用例会通过鉴权 —— 而那正是「配置漏了 = 谁都能进管理面」的静默故障。
func TestAuthenticateAdminDeniesWholePlaneWithoutAudience(t *testing.T) {
	f := newAdminFixture(t)
	cfg := f.cfg()
	cfg.IAPAudience = ""

	auth, authErr := AuthenticateAdmin(context.Background(), cfg, adminReq(f.token(t, nil)))
	if auth != nil {
		t.Fatalf("未配置 audience 时不该有任何请求通过: %+v", auth)
	}
	if authErr == nil || authErr.Status != http.StatusForbidden {
		t.Fatalf("应 403，实得 %v", authErr)
	}
	// 一个都不放进去：连公钥都不该去取，更不该查库。
	if f.keys.calls != 0 || f.dir.emailQueries != 0 {
		t.Fatalf("整体拒绝必须在最前面短路，实际 keys=%d db=%d", f.keys.calls, f.dir.emailQueries)
	}
}

// 装配不全（忘了注入 Keys / DB）是我们自己的 bug，要以 500 暴露而不是伪装成 403 ——
// 后者会让人去查 IAP 配置，查错方向。但两者都必须是**关**的。
func TestAuthenticateAdminAssemblyErrorIs500(t *testing.T) {
	for _, tc := range []struct {
		name string
		mut  func(*AdminAuthConfig)
	}{
		{"缺 Keys", func(c *AdminAuthConfig) { c.Keys = nil }},
		{"缺 DB", func(c *AdminAuthConfig) { c.DB = nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newAdminFixture(t)
			cfg := f.cfg()
			tc.mut(&cfg)

			auth, authErr := AuthenticateAdmin(context.Background(), cfg, adminReq(f.token(t, nil)))
			if auth != nil {
				t.Fatal("装配不全时不该放行")
			}
			if authErr == nil || authErr.Status != http.StatusInternalServerError {
				t.Fatalf("应 500，实得 %v", authErr)
			}
		})
	}
}

// ---- 签名对，但 aud / iss / exp 不对 ----

// 签名过了不等于这份断言是发给我们的、是我们认的签发方发的、还在有效期内。
// 三项各拒一次；exp 缺失单独一条 —— 没有 exp 的 token 永不过期，
// 「缺字段」必须当作失败而不是「不限制」。
func TestAuthenticateAdminRejectsBadAudIssExp(t *testing.T) {
	cases := []struct {
		name string
		mut  func(hdr, claims map[string]any)
		why  string
	}{
		{
			"aud 是别的服务",
			func(_, c map[string]any) {
				c["aud"] = "/projects/999999999999/global/backendServices/1111111111"
			},
			"aud 是「这个断言是发给我们的」唯一证据；宽松了就等于接受别人服务的断言",
		},
		{
			"aud 为空",
			func(_, c map[string]any) { delete(c, "aud") },
			"缺 aud 不能被当成「不限制」",
		},
		{
			"aud 只是前缀",
			func(_, c map[string]any) { c["aud"] = adminTestAudience[:len(adminTestAudience)-2] },
			"必须逐字节相等，不能前缀/包含匹配",
		},
		{
			"iss 不是 IAP",
			func(_, c map[string]any) { c["iss"] = "https://accounts.google.com" },
			"普通 Google OIDC 的 iss 与 IAP 断言不是一回事，认它等于放开一整个签发方",
		},
		{
			"已过期",
			func(_, c map[string]any) { c["exp"] = adminTestNow.Add(-10 * time.Minute).Unix() },
			"过期断言必须拒，否则一次泄漏就是永久凭据",
		},
		{
			"缺少 exp",
			func(_, c map[string]any) { delete(c, "exp") },
			"没有 exp 的 token 永不过期；缺字段是失败，不是「不限制」",
		},
		{
			"iat 在未来",
			func(_, c map[string]any) { c["iat"] = adminTestNow.Add(10 * time.Minute).Unix() },
			"iat 在未来说明对方时钟不对或 token 是造的",
		},
		{
			"kid 缺失",
			func(h, _ map[string]any) { delete(h, "kid") },
			"没有 kid 就没法确定用哪把公钥，不能退化成「随便挑一把」",
		},
		{
			"kid 不在密钥集里",
			func(h, _ map[string]any) { h["kid"] = "kid-we-never-saw" },
			"未知 kid 必须拒，不能回退到任何一把已知公钥",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newAdminFixture(t)
			raw := f.token(t, tc.mut)

			auth, authErr := AuthenticateAdmin(context.Background(), f.cfg(), adminReq(raw))
			if auth != nil || authErr == nil {
				t.Fatalf("%s —— 必须被拒", tc.why)
			}
			if authErr.Status != http.StatusForbidden {
				t.Fatalf("应 403，实得 %d", authErr.Status)
			}
			if f.dir.emailQueries != 0 {
				t.Fatal("断言校验没过就不该查 admin_users")
			}
		})
	}
}

// exp 恰好落在容差边界上：skew 之内还认，之外就拒。
// 这条钉住的是「容差是有限的」—— 一个无意中被放大的 skew
// 等于延长了每一份被窃断言的可用寿命。
func TestAuthenticateAdminExpiryClockSkewBoundary(t *testing.T) {
	t.Run("刚过期但在容差内", func(t *testing.T) {
		f := newAdminFixture(t)
		raw := f.token(t, func(_, c map[string]any) {
			c["exp"] = f.now.Add(-defaultClockSkew / 2).Unix()
		})
		if _, authErr := AuthenticateAdmin(context.Background(), f.cfg(), adminReq(raw)); authErr != nil {
			t.Fatalf("容差内应放行，实得 %v", authErr)
		}
	})

	t.Run("超出容差", func(t *testing.T) {
		f := newAdminFixture(t)
		raw := f.token(t, func(_, c map[string]any) {
			c["exp"] = f.now.Add(-defaultClockSkew - time.Minute).Unix()
		})
		if _, authErr := AuthenticateAdmin(context.Background(), f.cfg(), adminReq(raw)); authErr == nil {
			t.Fatal("超出容差必须拒")
		}
	})
}

// ---- 身份合法但不是管理员 / 已被禁用 ----

// IAP 只证明「你是某个 Google 身份」。IAP 的访问策略可能是整个 workspace 域，
// 所以「这个身份是不是本系统的管理员」必须另外问一次 admin_users。
// 查不到必须 **403**：401 会让浏览器认为凭据没给对而反复重走 IAP 登录流程。
func TestAuthenticateAdminUnknownIdentityIs403(t *testing.T) {
	f := newAdminFixture(t)
	f.dir.hit = false

	auth, authErr := AuthenticateAdmin(context.Background(), f.cfg(), adminReq(f.token(t, nil)))
	if auth != nil {
		t.Fatal("不在 admin_users 里的身份不该拿到管理员上下文")
	}
	if authErr == nil {
		t.Fatal("必须被拒")
	}
	if authErr.Status == http.StatusUnauthorized {
		t.Fatal("不能是 401 —— 会让浏览器带着 IAP 无限重试，表现为无限跳转而不是「你不是管理员」")
	}
	if authErr.Status != http.StatusForbidden {
		t.Fatalf("应 403，实得 %d", authErr.Status)
	}
	if f.dir.emailQueries != 1 {
		t.Fatalf("应恰好查一次库，实得 %d", f.dir.emailQueries)
	}
}

// 被禁用的管理员 → 403，且错误码必须与「不是管理员」「断言坏了」**完全一致**。
// 能区分就等于给一个只拿到 IAP 通行权的人一台账号枚举机。
func TestAuthenticateAdminDisabledAdminIs403(t *testing.T) {
	f := newAdminFixture(t)
	f.dir.rec.Disabled = true

	_, authErr := AuthenticateAdmin(context.Background(), f.cfg(), adminReq(f.token(t, nil)))
	if authErr == nil || authErr.Status != http.StatusForbidden {
		t.Fatalf("已禁用的管理员应 403，实得 %v", authErr)
	}

	// 与「查不到」的响应逐字段相同。
	g := newAdminFixture(t)
	g.dir.hit = false
	_, missingErr := AuthenticateAdmin(context.Background(), g.cfg(), adminReq(g.token(t, nil)))
	if missingErr == nil {
		t.Fatal("查不到也必须被拒")
	}
	if *authErr != *missingErr {
		t.Fatalf("「已禁用」与「不是管理员」的响应必须不可区分：%+v vs %+v", authErr, missingErr)
	}
}

// iap_subject 已绑定时必须对得上：邮箱可以被回收（workspace 删号后同名重建），sub 不会。
func TestAuthenticateAdminSubjectMismatchIs403(t *testing.T) {
	f := newAdminFixture(t)
	f.dir.rec.IAPSubject = "accounts.google.com:0000000000000000"

	if _, authErr := AuthenticateAdmin(context.Background(), f.cfg(), adminReq(f.token(t, nil))); authErr == nil {
		t.Fatal("绑定的 sub 与断言不符必须拒")
	}
}

// 还没绑 sub 的管理员（可空列）不该被这道校验挡在门外。
func TestAuthenticateAdminUnboundSubjectPasses(t *testing.T) {
	f := newAdminFixture(t)
	f.dir.rec.IAPSubject = ""

	if _, authErr := AuthenticateAdmin(context.Background(), f.cfg(), adminReq(f.token(t, nil))); authErr != nil {
		t.Fatalf("iap_subject 未绑定不该变成一道进不去的门，实得 %v", authErr)
	}
}

// 库故障要 500，不能退化成 403 —— 后者会让一次数据库抖动看起来像「你没权限」。
func TestAuthenticateAdminDBErrorIs500(t *testing.T) {
	f := newAdminFixture(t)
	f.dir.err = context.DeadlineExceeded

	_, authErr := AuthenticateAdmin(context.Background(), f.cfg(), adminReq(f.token(t, nil)))
	if authErr == nil || authErr.Status != http.StatusInternalServerError {
		t.Fatalf("库故障应 500，实得 %v", authErr)
	}
}

// happy path：身份注入正确，且 **Email 取 admin_users 那一份**。
// 审计日志的 admin_email_snapshot 记的必须是「本系统认为他是谁」，
// 而不是身份提供方在断言里说的那一份。
func TestAuthenticateAdminHappyPath(t *testing.T) {
	f := newAdminFixture(t)

	auth, authErr := AuthenticateAdmin(context.Background(), f.cfg(), adminReq(f.token(t, nil)))
	if authErr != nil {
		t.Fatalf("合法断言应通过，实得 %v", authErr)
	}
	if auth.AdminID != 7 {
		t.Fatalf("admin_id = %d", auth.AdminID)
	}
	if auth.Email != "Ops@Babel.Plus" {
		t.Fatalf("Email 必须取 admin_users 那一份（审计要记它），实得 %q", auth.Email)
	}
	if auth.IAPSubject != adminTestSubject {
		t.Fatalf("IAPSubject = %q", auth.IAPSubject)
	}
	if !auth.Can(PermMarkOrderPaid) || !auth.Can(PermRefund) {
		t.Fatal("权限位没有从 admin_users 带过来")
	}
	if auth.Can(PermAdjustBalance) || auth.Can(PermExportCSV) {
		t.Fatal("未授予的权限位必须是 false")
	}
}

// IAP 的 email 声明可能带身份提供方前缀（accounts.google.com:a@b.com），
// 也可能是裸邮箱。两种都要能查得到，否则换一次 IAP 配置就全员进不去。
func TestAuthenticateAdminNormalizesEmail(t *testing.T) {
	for _, raw := range []string{
		adminTestEmail,
		"accounts.google.com:" + adminTestEmail,
		"  OPS@BABEL.PLUS  ",
	} {
		f := newAdminFixture(t)
		token := f.token(t, func(_, c map[string]any) { c["email"] = raw })
		if _, authErr := AuthenticateAdmin(context.Background(), f.cfg(), adminReq(token)); authErr != nil {
			t.Fatalf("email 形态 %q 应被归一化后查到，实得 %v", raw, authErr)
		}
	}
}

// 断言里没有 email 就没法查 admin_users —— 必须拒，不能拿 sub 猜。
func TestAuthenticateAdminMissingEmailClaimIsDenied(t *testing.T) {
	f := newAdminFixture(t)
	token := f.token(t, func(_, c map[string]any) { delete(c, "email") })

	if _, authErr := AuthenticateAdmin(context.Background(), f.cfg(), adminReq(token)); authErr == nil {
		t.Fatal("缺 email 声明必须拒")
	}
	if f.dir.emailQueries != 0 {
		t.Fatal("空 email 不该拿去查库")
	}
}

// Can 对未知权限位必须返回 false：新增枚举而忘了加分支时，
// 现象必须是「这个操作谁都做不了」，不能是「谁都能做」。
func TestAdminAuthCanFailsClosed(t *testing.T) {
	a := &AdminAuth{Perms: AdminPerms{MarkOrderPaid: true, Refund: true, AdjustBalance: true, ExportCSV: true}}
	if a.Can(AdminPermission(9999)) {
		t.Fatal("未知权限位必须 false")
	}
	var nilAuth *AdminAuth
	if nilAuth.Can(PermRefund) {
		t.Fatal("nil 身份必须 false")
	}
}

// ---- 上下文与中间件形态 ----

func TestAdminContextRoundTripAndIsolation(t *testing.T) {
	if _, ok := AdminFrom(context.Background()); ok {
		t.Fatal("空上下文不该取到管理员")
	}
	want := &AdminAuth{AdminID: 7}
	got, ok := AdminFrom(WithAdmin(context.Background(), want))
	if !ok || got != want {
		t.Fatalf("上下文往返失败: %v %v", got, ok)
	}
	// 三套身份必须在互不相通的键空间里 —— 一次 iota 顺序调整不该让
	// 节点密钥换来一个管理员身份。
	adminCtx := WithAdmin(context.Background(), want)
	if _, ok := UserFrom(adminCtx); ok {
		t.Fatal("管理员上下文不该被 UserFrom 取到")
	}
	if _, ok := NodeAuthFrom(adminCtx); ok {
		t.Fatal("管理员上下文不该被 NodeAuthFrom 取到")
	}
	if _, ok := AdminFrom(WithUser(context.Background(), &UserAuth{UserID: 1})); ok {
		t.Fatal("用户上下文不该被 AdminFrom 取到")
	}
}

func TestRequireAdminMiddleware(t *testing.T) {
	t.Run("失败时写信封且不进 handler", func(t *testing.T) {
		f := newAdminFixture(t)
		f.dir.hit = false
		reached := false
		h := RequireAdmin(f.cfg())(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached = true }))

		w := httptest.NewRecorder()
		h.ServeHTTP(w, adminReq(f.token(t, nil)))

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
		f := newAdminFixture(t)
		var seen *AdminAuth
		h := RequireAdmin(f.cfg())(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			seen, _ = AdminFrom(r.Context())
		}))

		h.ServeHTTP(httptest.NewRecorder(), adminReq(f.token(t, nil)))

		if seen == nil || seen.AdminID != 7 {
			t.Fatalf("handler 未拿到管理员身份: %+v", seen)
		}
	})
}

// ---- TOTP step-up ----

// stepUpCtx 造一个「已通过管理面鉴权」的上下文，供 RequireStepUp 用。
func stepUpCtx(f *adminFixture) context.Context {
	return WithAdmin(context.Background(), &AdminAuth{
		AdminID:    f.dir.rec.ID,
		Email:      f.dir.rec.Email,
		Role:       f.dir.rec.Role,
		IAPSubject: adminTestSubject,
	})
}

// 错误的 TOTP 码必须被拒，而且**不许占用重放槽**。
//
// 顺序反过来（先占用再验）会让任何人拿一串随机 6 位数把 used_totp 灌满，
// 顺带把管理员真正要用的那个码提前占掉 —— 一个免费的拒绝服务。
func TestRequireStepUpRejectsWrongCode(t *testing.T) {
	f := newAdminFixture(t)
	good := adminTestCodeAt(t, 0, f.now)
	// 找一个与真码不同的 6 位数。
	bad := "000000"
	if bad == good {
		bad = "111111"
	}

	authErr := f.cfg().RequireStepUp(stepUpCtx(f), bad)
	if authErr == nil {
		t.Fatal("错码必须被拒")
	}
	if authErr.Code != "AUTH_TOTP_INVALID" {
		t.Fatalf("错误码 = %q，应为 AUTH_TOTP_INVALID", authErr.Code)
	}
	if authErr.Status != http.StatusForbidden {
		t.Fatalf("应 403，实得 %d", authErr.Status)
	}
	if f.replay.calls != 0 {
		t.Fatal("必须先验对再占用 —— 否则随机码能把管理员真正要用的码提前占掉")
	}
	// 验证「错码没有污染重放表」：真码随后仍然可用。
	if authErr := f.cfg().RequireStepUp(stepUpCtx(f), good); authErr != nil {
		t.Fatalf("错码不该影响真码，实得 %v", authErr)
	}
}

// 🔴 同一个 code 在有效窗口内第二次使用必须失败（used_totp 防重放）。
// TOTP 的取值空间只有 10^6，一个能重放的码等于没有二次验证。
func TestRequireStepUpReplayWithinWindowFails(t *testing.T) {
	f := newAdminFixture(t)
	code := adminTestCodeAt(t, 0, f.now)

	if authErr := f.cfg().RequireStepUp(stepUpCtx(f), code); authErr != nil {
		t.Fatalf("第一次使用应通过，实得 %v", authErr)
	}
	// 时钟没有走 —— 码仍在有效窗口内，但已经被用过了。
	authErr := f.cfg().RequireStepUp(stepUpCtx(f), code)
	if authErr == nil {
		t.Fatal("同一个 code 第二次使用必须失败，否则 used_totp 防重放形同虚设")
	}
	if authErr.Code != "AUTH_TOTP_INVALID" {
		t.Fatalf("重放与错码必须不可区分（都是 AUTH_TOTP_INVALID），实得 %q", authErr.Code)
	}
}

// 时间窗边界，端到端走一遍 RequireStepUp：前后各一个 step 收，再多一个拒。
func TestRequireStepUpAcceptsAdjacentSteps(t *testing.T) {
	for _, tc := range []struct {
		name   string
		offset int64
		want   bool
	}{
		{"上一个时间步", -1, true},
		{"当前时间步", 0, true},
		{"下一个时间步", 1, true},
		{"再往前一步", -2, false},
		{"再往后一步", 2, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newAdminFixture(t)
			code := adminTestCodeAt(t, tc.offset, f.now)
			authErr := f.cfg().RequireStepUp(stepUpCtx(f), code)
			if tc.want && authErr != nil {
				t.Fatalf("±1 步内的码必须收（容忍手机与服务器的时钟差），实得 %v", authErr)
			}
			if !tc.want && authErr == nil {
				t.Fatal("窗口外的码必须拒 —— 放大窗口等于放大重放窗口")
			}
		})
	}
}

// verifyTOTPCode 的边界（不经过库与解密，直接钉住算法层）。
func TestVerifyTOTPCodeWindowBoundary(t *testing.T) {
	secret, err := decodeBase32Secret(adminTestTOTPSecret)
	if err != nil {
		t.Fatalf("解码 secret 失败: %v", err)
	}
	step := adminTestNow.Unix() / int64(totpPeriod/time.Second)

	for off := int64(-3); off <= 3; off++ {
		code := totpAt(secret, step+off)
		got := verifyTOTPCode(secret, code, adminTestNow)
		want := off >= -totpSkewSteps && off <= totpSkewSteps
		if got != want {
			t.Fatalf("偏移 %d 步：verifyTOTPCode = %v，期望 %v", off, got, want)
		}
	}
}

// RFC 6238 的官方测试向量（SHA-1、T=59s、secret "12345678901234567890"，
// 8 位码 94287082 → 取低 6 位 287082）。
//
// 这条锚点的作用：上面所有 TOTP 用例都拿 totpAt 自己生成期望值，
// 一个「实现和期望一起错」的改动不会被它们抓住。RFC 向量是外部事实源。
func TestTOTPMatchesRFC6238Vector(t *testing.T) {
	secret := []byte("12345678901234567890")
	if got := totpAt(secret, 59/30); got != "287082" {
		t.Fatalf("RFC 6238 T=59 应得 287082，实得 %s —— 实现与所有 Authenticator app 已经对不上", got)
	}
}

// step-up 依赖缺失时必须是「危险操作做不了」，不能是「危险操作不需要 TOTP」。
func TestRequireStepUpFailsClosedWithoutDeps(t *testing.T) {
	for _, tc := range []struct {
		name string
		mut  func(*AdminAuthConfig)
	}{
		{"缺重放守卫", func(c *AdminAuthConfig) { c.Replay = nil }},
		{"缺 TOTP 加密密钥", func(c *AdminAuthConfig) { c.TOTPKey = nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newAdminFixture(t)
			cfg := f.cfg()
			tc.mut(&cfg)
			code := adminTestCodeAt(t, 0, f.now)

			authErr := cfg.RequireStepUp(stepUpCtx(f), code)
			if authErr == nil {
				t.Fatal("依赖缺失必须拒绝危险操作，不能放行")
			}
			if authErr.Code != "AUTH_TOTP_REQUIRED" {
				t.Fatalf("错误码 = %q", authErr.Code)
			}
			if f.dir.idQueries != 0 {
				t.Fatal("依赖缺失应在查库之前短路")
			}
		})
	}
}

// 缺头与错码必须走不同的码：前端拿到 AUTH_TOTP_REQUIRED 才知道要弹输入框，
// 拿到 AUTH_TOTP_INVALID 是「你输错了」。合并会让用户还没被要求输码就看到「验证码错误」。
func TestRequireStepUpMissingCodeIsRequiredNotInvalid(t *testing.T) {
	f := newAdminFixture(t)

	authErr := f.cfg().RequireStepUp(stepUpCtx(f), "   ")
	if authErr == nil || authErr.Code != "AUTH_TOTP_REQUIRED" {
		t.Fatalf("空码应为 AUTH_TOTP_REQUIRED，实得 %v", authErr)
	}
	if f.dir.idQueries != 0 {
		t.Fatal("空码不该查库")
	}
}

// 形态不合法直接拒，**不解密也不查库** —— 省一次 AES 与一次数据库往返，
// 也不给探测留时序差异。
func TestRequireStepUpMalformedCodeSkipsDB(t *testing.T) {
	for _, code := range []string{"12345", "1234567", "12a456", "12 456", "abcdef"} {
		f := newAdminFixture(t)
		authErr := f.cfg().RequireStepUp(stepUpCtx(f), code)
		if authErr == nil || authErr.Code != "AUTH_TOTP_INVALID" {
			t.Fatalf("码 %q 形态非法应拒，实得 %v", code, authErr)
		}
		if f.dir.idQueries != 0 || f.replay.calls != 0 {
			t.Fatalf("码 %q 形态非法不该查库/占位", code)
		}
	}
}

// 没有管理员上下文却要求 step-up = 装配错误（这条路由没挂管理面鉴权）。
// 必须 500 而不是 403，否则装配错误会伪装成权限问题。
func TestRequireStepUpWithoutAdminContextIs500(t *testing.T) {
	f := newAdminFixture(t)
	authErr := f.cfg().RequireStepUp(context.Background(), adminTestCodeAt(t, 0, f.now))
	if authErr == nil || authErr.Status != http.StatusInternalServerError {
		t.Fatalf("装配错误应 500，实得 %v", authErr)
	}
}

// step-up 会**重新查一次** admin_users：一个在会话中途被禁用的管理员
// 必须做不成危险操作，而 AdminAuth 是请求开始时的快照。
func TestRequireStepUpRechecksDisabledMidSession(t *testing.T) {
	f := newAdminFixture(t)
	ctx := stepUpCtx(f) // 身份快照是在「还没被禁用」时取的
	f.dir.rec.Disabled = true

	authErr := f.cfg().RequireStepUp(ctx, adminTestCodeAt(t, 0, f.now))
	if authErr == nil {
		t.Fatal("会话中途被禁用的管理员必须做不成危险操作")
	}
	if authErr.Code != "AUTH_PERMISSION_DENIED" {
		t.Fatalf("错误码 = %q", authErr.Code)
	}
}

// step-up 时管理员已被删除 → 拒绝（不是放行，也不是 500）。
func TestRequireStepUpDeletedAdminIsDenied(t *testing.T) {
	f := newAdminFixture(t)
	ctx := stepUpCtx(f)
	f.dir.hit = false

	authErr := f.cfg().RequireStepUp(ctx, adminTestCodeAt(t, 0, f.now))
	if authErr == nil || authErr.Status != http.StatusForbidden {
		t.Fatalf("应 403，实得 %v", authErr)
	}
}

// used_totp 写不进去必须拒绝：放行等于在这一刻关闭了防重放。
func TestRequireStepUpClaimFailureDenies(t *testing.T) {
	f := newAdminFixture(t)
	f.replay.err = errors.New("数据库不可用")

	authErr := f.cfg().RequireStepUp(stepUpCtx(f), adminTestCodeAt(t, 0, f.now))
	if authErr == nil {
		t.Fatal("记录已用 code 失败时必须拒绝本次 step-up")
	}
	if authErr.Status != http.StatusInternalServerError {
		t.Fatalf("应 500，实得 %d", authErr.Status)
	}
}

// 密文解不开（密钥轮换漏了 / 密文被截断）对调用方只能表现为「验证失败」，
// 不能是 500 —— 那会把「我们的密钥配错了」变成一个可被观察的探测信号。
func TestRequireStepUpUndecryptableSecretLooksLikeInvalidCode(t *testing.T) {
	f := newAdminFixture(t)
	f.dir.rec.TOTPSecretEnc = []byte("truncated")

	authErr := f.cfg().RequireStepUp(stepUpCtx(f), adminTestCodeAt(t, 0, f.now))
	if authErr == nil || authErr.Code != "AUTH_TOTP_INVALID" {
		t.Fatalf("应表现为 AUTH_TOTP_INVALID，实得 %v", authErr)
	}
}

// 不同管理员的同一个码算出不同的 code_hash：否则一个管理员用过的码
// 会把另一个管理员的同码顶掉（used_totp 的主键含 admin_user_id）。
func TestTOTPCodeHashIsPerAdminAndKeyed(t *testing.T) {
	f := newAdminFixture(t)
	cfg := f.cfg()

	h1 := cfg.totpCodeHash(1, "123456")
	h2 := cfg.totpCodeHash(2, "123456")
	if string(h1) == string(h2) {
		t.Fatal("admin_id 必须拌进哈希")
	}
	if string(h1) != string(cfg.totpCodeHash(1, "123456")) {
		t.Fatal("哈希必须稳定，否则同一个码永远撞不上自己")
	}

	other := f.cfg()
	other.TOTPKey = make([]byte, 32)
	if string(h1) == string(other.totpCodeHash(1, "123456")) {
		t.Fatal("哈希必须是密钥化的 —— 6 位数字只有 10^6 种，裸 sha256 等于没哈希")
	}
	if len(h1) != sha256.Size {
		t.Fatalf("哈希长度 = %d", len(h1))
	}
}

// 人从 Authenticator / 密码管理器里拷出来的 secret 带空格、小写、填充符，
// 严格解析的现象是「肉眼一模一样的两串，一个能用一个不能用」。
func TestDecodeBase32SecretIsLenient(t *testing.T) {
	want, err := decodeBase32Secret(adminTestTOTPSecret)
	if err != nil {
		t.Fatalf("基准解码失败: %v", err)
	}
	for _, variant := range []string{
		"jbswy3dpehpk3pxp",
		"JBSW Y3DP EHPK 3PXP",
		"JBSWY3DP-EHPK3PXP",
		"  JBSWY3DPEHPK3PXP  ",
	} {
		got, err := decodeBase32Secret(variant)
		if err != nil {
			t.Fatalf("变体 %q 应能解出来: %v", variant, err)
		}
		if string(got) != string(want) {
			t.Fatalf("变体 %q 解出的 secret 与基准不同", variant)
		}
	}
	if _, err := decodeBase32Secret("  "); err == nil {
		t.Fatal("空 secret 必须报错")
	}
	if _, err := decodeBase32Secret("!!!!"); err == nil {
		t.Fatal("非法 base32 必须报错")
	}
}

// ---- JWKS ----

// 未命中的 kid 会触发强制刷新，但必须限速：kid 由请求方控制，
// 没有这道闸就是一个「我们付账」的反射式放大器。
func TestIAPKeySetRefreshOnMissIsRateLimited(t *testing.T) {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("生成密钥失败: %v", err)
	}
	fetches := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fetches++
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"keys":[{"kty":"EC","crv":"P-256","kid":%q,"alg":"ES256","x":%q,"y":%q}]}`,
			adminTestKID,
			base64.RawURLEncoding.EncodeToString(priv.PublicKey.X.FillBytes(make([]byte, 32))),
			base64.RawURLEncoding.EncodeToString(priv.PublicKey.Y.FillBytes(make([]byte, 32))))
	}))
	defer srv.Close()

	now := adminTestNow
	ks := &IAPKeySet{URL: srv.URL, HTTP: srv.Client(), Now: func() time.Time { return now }}

	if _, err := ks.PublicKey(context.Background(), adminTestKID); err != nil {
		t.Fatalf("首次取公钥应成功: %v", err)
	}
	if fetches != 1 {
		t.Fatalf("应拉取一次，实得 %d", fetches)
	}
	// 已缓存的 kid 不再拉取。
	if _, err := ks.PublicKey(context.Background(), adminTestKID); err != nil {
		t.Fatalf("缓存命中应成功: %v", err)
	}
	if fetches != 1 {
		t.Fatalf("缓存命中不该重新拉取，实得 %d 次", fetches)
	}
	// 第一次未命中触发一次强制刷新。
	if _, err := ks.PublicKey(context.Background(), "unknown-kid-1"); err == nil {
		t.Fatal("未知 kid 必须报错")
	}
	if fetches != 2 {
		t.Fatalf("首次未命中应触发一次刷新，实得 %d", fetches)
	}
	// 紧接着的未命中被限速，不再产生出站请求。
	for i := range 5 {
		if _, err := ks.PublicKey(context.Background(), fmt.Sprintf("unknown-kid-%d", i+2)); err == nil {
			t.Fatal("未知 kid 必须报错")
		}
	}
	if fetches != 2 {
		t.Fatalf("未命中刷新必须限速，实得 %d 次出站请求 —— 这是一个反射式放大器", fetches)
	}
}

// 不在曲线上的「公钥」必须被丢弃：拿它去验签在 crypto/ecdsa 里是未定义行为，
// 而这份数据来自网络。
func TestFetchJWKSRejectsOffCurvePoint(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		x := base64.RawURLEncoding.EncodeToString(make([]byte, 32)) // (0,0) 不在 P-256 上
		_, _ = fmt.Fprintf(w, `{"keys":[{"kty":"EC","crv":"P-256","kid":"bad","x":%q,"y":%q}]}`, x, x)
	}))
	defer srv.Close()

	if _, err := fetchJWKS(context.Background(), srv.Client(), srv.URL); err == nil {
		t.Fatal("整份 JWKS 里只有一把不在曲线上的键时必须报错，不能装作有可用公钥")
	}
}

// base32 解码在 Go 里对空 padding 有讲究，这里只是确认测试夹具里的
// secret 与 base32 库的约定一致（无填充、大写）。
func TestAdminFixtureSecretIsCanonicalBase32(t *testing.T) {
	if _, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(adminTestTOTPSecret); err != nil {
		t.Fatalf("测试 secret 不是合法的无填充 base32: %v", err)
	}
}

// 🔴 缓存过期 / 从未拉取成功时，节流仍然必须生效。
//
// 修复前这条路径上一次节流都不过：gstatic 一抖，每个进来的请求都会变成
// 一次出站请求。触发条件从「随机 kid」换成了「远端不可达」，但放大器是同一个
// —— 而管理面在公网上（--ingress=all），谁都能敲。
func TestIAPKeySetThrottlesWhenRemoteIsDown(t *testing.T) {
	fetches := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fetches++
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	now := adminTestNow
	ks := &IAPKeySet{URL: srv.URL, HTTP: srv.Client(), Now: func() time.Time { return now }}

	// 从未拉取成功过（keys == nil）—— 连打 20 次。
	for i := range 20 {
		if _, err := ks.PublicKey(context.Background(), fmt.Sprintf("kid-%d", i)); err == nil {
			t.Fatal("远端不可用时必须报错")
		}
	}
	if fetches != 1 {
		t.Fatalf("远端不可达时也必须节流，实得 %d 次出站请求 —— 这是一个反射式放大器", fetches)
	}

	// 节流窗口过后允许再试一次（否则远端恢复了我们也永远拉不到）。
	now = now.Add(jwksRefetchInterval + time.Second)
	if _, err := ks.PublicKey(context.Background(), adminTestKID); err == nil {
		t.Fatal("远端仍不可用，应报错")
	}
	if fetches != 2 {
		t.Fatalf("节流窗口过后应允许重试一次，实得 %d", fetches)
	}
}

// 缓存过期但 kid 命中时，宁可用略旧的公钥也不要把管理面挡掉：
// 公钥过期 ≠ 失效（Google 是「先公布新的，旧的再挂一段时间」），而 exp 仍然管着 token 寿命。
func TestIAPKeySetServesStaleKeyWhileThrottled(t *testing.T) {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("生成密钥失败: %v", err)
	}
	up := true
	fetches := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fetches++
		if !up {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = fmt.Fprintf(w, `{"keys":[{"kty":"EC","crv":"P-256","kid":%q,"alg":"ES256","x":%q,"y":%q}]}`,
			adminTestKID,
			base64.RawURLEncoding.EncodeToString(priv.PublicKey.X.FillBytes(make([]byte, 32))),
			base64.RawURLEncoding.EncodeToString(priv.PublicKey.Y.FillBytes(make([]byte, 32))))
	}))
	defer srv.Close()

	now := adminTestNow
	ks := &IAPKeySet{URL: srv.URL, HTTP: srv.Client(), TTL: time.Hour, Now: func() time.Time { return now }}
	if _, err := ks.PublicKey(context.Background(), adminTestKID); err != nil {
		t.Fatalf("首次取公钥应成功: %v", err)
	}

	// TTL 过期，远端挂了。
	up = false
	now = now.Add(2 * time.Hour)
	if _, err := ks.PublicKey(context.Background(), adminTestKID); err != nil {
		t.Fatalf("远端不可达时应降级用旧公钥，实得 %v", err)
	}
	if fetches != 2 {
		t.Fatalf("TTL 过期应触发一次拉取尝试，实得 %d", fetches)
	}
	// 紧接着的请求走节流，直接用旧公钥，不再产生出站请求。
	for range 10 {
		if _, err := ks.PublicKey(context.Background(), adminTestKID); err != nil {
			t.Fatalf("节流期内应继续用旧公钥，实得 %v", err)
		}
	}
	if fetches != 2 {
		t.Fatalf("节流期内不该再发出站请求，实得 %d", fetches)
	}
}

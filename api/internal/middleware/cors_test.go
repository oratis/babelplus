package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// allowedOrigins 是所有 CORS 用例共用的白名单。
var testOrigins = []string{"https://web.babel.plus", "http://localhost:5173"}

// newCORSHandler 组一个「CORS + 计数 handler」，返回 handler 与「是否走到过业务」的探针。
func newCORSHandler(t *testing.T, cfg CORSConfig) (http.Handler, *bool) {
	t.Helper()
	reached := false
	h := CORS(cfg)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached = true
		w.WriteHeader(http.StatusOK)
	}))
	return h, &reached
}

func do(h http.Handler, method, path string, headers map[string]string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, nil)
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestCORSAllowedOriginEchoed(t *testing.T) {
	h, reached := newCORSHandler(t, CORSConfig{AllowedOrigins: testOrigins, AllowCredentials: true})

	w := do(h, http.MethodGet, "/api/v1/user/me", map[string]string{
		"Origin": "https://web.babel.plus",
	})

	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "https://web.babel.plus" {
		t.Fatalf("Allow-Origin = %q，应回显命中的 Origin", got)
	}
	if got := w.Header().Get("Access-Control-Allow-Credentials"); got != "true" {
		t.Fatalf("Allow-Credentials = %q，应为 true", got)
	}
	if got := w.Header().Get("Access-Control-Expose-Headers"); got == "" {
		t.Fatal("实际请求应带 Expose-Headers，否则前端读不到 X-Request-Id")
	}
	if !*reached {
		t.Fatal("非预检请求必须走到业务 handler")
	}
}

func TestCORSUnknownOriginGetsNoCORSHeaders(t *testing.T) {
	h, reached := newCORSHandler(t, CORSConfig{AllowedOrigins: testOrigins, AllowCredentials: true})

	w := do(h, http.MethodGet, "/api/v1/user/me", map[string]string{
		"Origin": "https://evil.example.com",
	})

	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("未命中的 Origin 不应拿到 Allow-Origin，实得 %q", got)
	}
	if got := w.Header().Get("Access-Control-Allow-Credentials"); got != "" {
		t.Fatalf("未命中时不应有 Allow-Credentials，实得 %q", got)
	}
	// 关键：未命中不等于拒绝服务。CORS 是浏览器侧的强制，服务端照常处理请求。
	if !*reached {
		t.Fatal("未命中 CORS 白名单不应阻断请求本身（浏览器会自己拦响应）")
	}
}

// 通配符/后缀匹配是 CORS 最常见的漏洞来源，这几个都必须落空。
func TestCORSNoWildcardOrSuffixMatching(t *testing.T) {
	h, _ := newCORSHandler(t, CORSConfig{AllowedOrigins: []string{"https://babel.plus"}, AllowCredentials: true})

	for _, origin := range []string{
		"https://evil-babel.plus",         // 后缀匹配会误放
		"https://babel.plus.attacker.com", // 前缀匹配会误放
		"https://sub.babel.plus",          // 子域不在白名单里就是不在
		"http://babel.plus",               // scheme 是 origin 的一部分
		"https://babel.plus:8443",         // 端口也是
		"https://babel.plus/",             // 带尾斜杠不是合法 Origin
	} {
		w := do(h, http.MethodGet, "/api/v1/user/me", map[string]string{"Origin": origin})
		if got := w.Header().Get("Access-Control-Allow-Origin"); got != "" {
			t.Errorf("Origin %q 不应命中，却拿到 Allow-Origin=%q", origin, got)
		}
	}
}

func TestCORSOriginMatchIsCaseInsensitive(t *testing.T) {
	h, _ := newCORSHandler(t, CORSConfig{AllowedOrigins: []string{"https://web.babel.plus"}, AllowCredentials: true})

	// 浏览器发的是小写，但大小写不敏感是 scheme/host 的规范行为，匹配上之后必须**原样回显**。
	w := do(h, http.MethodGet, "/api/v1/user/me", map[string]string{"Origin": "https://WEB.Babel.Plus"})
	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "https://WEB.Babel.Plus" {
		t.Fatalf("Allow-Origin = %q，应原样回显请求里的 Origin", got)
	}
}

func TestCORSPreflightShortCircuits(t *testing.T) {
	h, reached := newCORSHandler(t, CORSConfig{
		AllowedOrigins:   testOrigins,
		AllowCredentials: true,
		MaxAge:           5 * time.Minute,
	})

	w := do(h, http.MethodOptions, "/api/v1/orders", map[string]string{
		"Origin":                         "http://localhost:5173",
		"Access-Control-Request-Method":  "POST",
		"Access-Control-Request-Headers": "authorization,content-type,idempotency-key",
	})

	if w.Code != http.StatusNoContent {
		t.Fatalf("预检应回 204，实得 %d", w.Code)
	}
	if *reached {
		t.Fatal("预检必须短路，不能走到业务 handler")
	}
	if got := w.Header().Get("Access-Control-Allow-Methods"); got == "" {
		t.Fatal("预检必须回 Allow-Methods")
	}
	h1 := w.Header().Get("Access-Control-Allow-Headers")
	for _, want := range []string{"Authorization", "Content-Type", "Idempotency-Key"} {
		if !containsHeader(h1, want) {
			t.Errorf("Allow-Headers 缺 %q，实得 %q", want, h1)
		}
	}
	if got := w.Header().Get("Access-Control-Max-Age"); got != "300" {
		t.Fatalf("Max-Age = %q，应为 300", got)
	}
	if got := w.Header().Get("Access-Control-Expose-Headers"); got != "" {
		t.Fatalf("预检响应没有业务头可暴露，不该带 Expose-Headers，实得 %q", got)
	}
}

func TestCORSPreflightFromUnknownOriginShortCircuitsWithoutHeaders(t *testing.T) {
	h, reached := newCORSHandler(t, CORSConfig{AllowedOrigins: testOrigins, AllowCredentials: true})

	w := do(h, http.MethodOptions, "/api/v1/orders", map[string]string{
		"Origin":                        "https://evil.example.com",
		"Access-Control-Request-Method": "POST",
	})

	if w.Code != http.StatusNoContent {
		t.Fatalf("预检一律 204（浏览器靠缺失的 CORS 头判失败），实得 %d", w.Code)
	}
	if *reached {
		t.Fatal("预检不该走到业务 handler，无论是否命中白名单")
	}
	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("未命中的预检不该带 Allow-Origin，实得 %q", got)
	}
	if got := w.Header().Get("Access-Control-Allow-Methods"); got != "" {
		t.Fatalf("未命中的预检不该带 Allow-Methods，实得 %q", got)
	}
}

// 只有 OPTIONS 而没有 Access-Control-Request-Method 的不是预检，应放行到 handler。
func TestCORSPlainOptionsIsNotPreflight(t *testing.T) {
	h, reached := newCORSHandler(t, CORSConfig{AllowedOrigins: testOrigins, AllowCredentials: true})

	w := do(h, http.MethodOptions, "/api/v1/user/me", map[string]string{
		"Origin": "https://web.babel.plus",
	})

	if !*reached {
		t.Fatal("非预检的 OPTIONS 应走到 handler")
	}
	if w.Code != http.StatusOK {
		t.Fatalf("应由 handler 决定状态码，实得 %d", w.Code)
	}
}

func TestCORSVaryOriginAlwaysPresent(t *testing.T) {
	h, _ := newCORSHandler(t, CORSConfig{AllowedOrigins: testOrigins, AllowCredentials: true})

	cases := []struct {
		name    string
		headers map[string]string
	}{
		{"命中白名单", map[string]string{"Origin": "https://web.babel.plus"}},
		{"未命中白名单", map[string]string{"Origin": "https://evil.example.com"}},
		{"完全没有 Origin 头", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := do(h, http.MethodGet, "/api/v1/user/me", tc.headers)
			if !containsHeader(w.Header().Get("Vary"), "Origin") {
				t.Fatalf("Vary 必须含 Origin（否则缓存会串源），实得 %q", w.Header().Get("Vary"))
			}
		})
	}
}

func TestCORSPreflightVaryIncludesRequestHeaders(t *testing.T) {
	h, _ := newCORSHandler(t, CORSConfig{AllowedOrigins: testOrigins, AllowCredentials: true})

	w := do(h, http.MethodOptions, "/api/v1/orders", map[string]string{
		"Origin":                        "https://web.babel.plus",
		"Access-Control-Request-Method": "POST",
	})

	vary := w.Header().Values("Vary")
	for _, want := range []string{"Origin", "Access-Control-Request-Method", "Access-Control-Request-Headers"} {
		found := false
		for _, v := range vary {
			if containsHeader(v, want) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("预检的 Vary 缺 %q，实得 %v", want, vary)
		}
	}
}

// 凭据组合：AllowCredentials=false 时不该冒出 Allow-Credentials 头。
func TestCORSCredentialsOff(t *testing.T) {
	h, _ := newCORSHandler(t, CORSConfig{AllowedOrigins: testOrigins, AllowCredentials: false})

	w := do(h, http.MethodGet, "/api/v1/user/me", map[string]string{"Origin": "https://web.babel.plus"})

	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "https://web.babel.plus" {
		t.Fatalf("Allow-Origin = %q", got)
	}
	if got := w.Header().Get("Access-Control-Allow-Credentials"); got != "" {
		t.Fatalf("AllowCredentials=false 时不该有该头，实得 %q", got)
	}
}

// 无论如何都不能输出 `*` —— 那与 credentials 不兼容，浏览器会直接拒。
func TestCORSNeverEmitsWildcard(t *testing.T) {
	h, _ := newCORSHandler(t, CORSConfig{
		// 即使有人把 * / null 塞进配置，中间件也要丢弃它们。
		AllowedOrigins:   []string{"*", "null", "https://web.babel.plus"},
		AllowCredentials: true,
	})

	for _, origin := range []string{"*", "null", "https://evil.example.com"} {
		w := do(h, http.MethodGet, "/api/v1/user/me", map[string]string{"Origin": origin})
		if got := w.Header().Get("Access-Control-Allow-Origin"); got != "" {
			t.Errorf("Origin %q 不应命中，实得 Allow-Origin=%q", origin, got)
		}
	}
	// 正常项仍然生效。
	w := do(h, http.MethodGet, "/api/v1/user/me", map[string]string{"Origin": "https://web.babel.plus"})
	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "https://web.babel.plus" {
		t.Fatalf("合法项应仍然命中，实得 %q", got)
	}
}

// 节点面是服务端到服务端，一个 CORS 头都不该有，预检也不短路。
func TestCORSSkipsNodeFace(t *testing.T) {
	h, reached := newCORSHandler(t, CORSConfig{AllowedOrigins: testOrigins, AllowCredentials: true})

	w := do(h, http.MethodGet, "/api/v1/server/UniProxy/config", map[string]string{
		"Origin": "https://web.babel.plus",
	})
	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("节点面不该有 Allow-Origin，实得 %q", got)
	}
	if got := w.Header().Get("Vary"); got != "" {
		t.Fatalf("节点面不该有 Vary: Origin，实得 %q", got)
	}
	if !*reached {
		t.Fatal("节点面请求必须原样放行")
	}

	*reached = false
	w = do(h, http.MethodOptions, "/api/v1/server/UniProxy/push", map[string]string{
		"Origin":                        "https://web.babel.plus",
		"Access-Control-Request-Method": "POST",
	})
	if !*reached {
		t.Fatal("节点面的 OPTIONS 不该被 CORS 短路")
	}
	if got := w.Header().Get("Access-Control-Allow-Methods"); got != "" {
		t.Fatalf("节点面预检不该拿到 Allow-Methods，实得 %q", got)
	}
}

// 空白名单 = 拒绝一切跨源（fail-closed），但不影响同源请求本身。
func TestCORSEmptyAllowlistDeniesAll(t *testing.T) {
	h, reached := newCORSHandler(t, CORSConfig{AllowCredentials: true})

	w := do(h, http.MethodGet, "/api/v1/user/me", map[string]string{"Origin": "https://web.babel.plus"})
	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("空白名单不该放行任何 Origin，实得 %q", got)
	}
	if !*reached {
		t.Fatal("同源/非浏览器请求仍应正常处理")
	}
}

func TestCORSDefaultMaxAge(t *testing.T) {
	h, _ := newCORSHandler(t, CORSConfig{AllowedOrigins: testOrigins, AllowCredentials: true})

	w := do(h, http.MethodOptions, "/api/v1/orders", map[string]string{
		"Origin":                        "https://web.babel.plus",
		"Access-Control-Request-Method": "POST",
	})
	if got := w.Header().Get("Access-Control-Max-Age"); got != "600" {
		t.Fatalf("默认 Max-Age 应为 600 秒，实得 %q", got)
	}
}

// containsHeader 判断逗号分隔的头值里是否含某一项（忽略大小写与空白）。
func containsHeader(value, want string) bool {
	for _, part := range splitAndTrim(value) {
		if equalFold(part, want) {
			return true
		}
	}
	return false
}

func splitAndTrim(s string) []string {
	var out []string
	start := 0
	for i := 0; i <= len(s); i++ {
		if i == len(s) || s[i] == ',' {
			part := s[start:i]
			for len(part) > 0 && (part[0] == ' ' || part[0] == '\t') {
				part = part[1:]
			}
			for len(part) > 0 && (part[len(part)-1] == ' ' || part[len(part)-1] == '\t') {
				part = part[:len(part)-1]
			}
			if part != "" {
				out = append(out, part)
			}
			start = i + 1
		}
	}
	return out
}

func equalFold(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		ca, cb := a[i], b[i]
		if 'A' <= ca && ca <= 'Z' {
			ca += 'a' - 'A'
		}
		if 'A' <= cb && cb <= 'Z' {
			cb += 'a' - 'A'
		}
		if ca != cb {
			return false
		}
	}
	return true
}

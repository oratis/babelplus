package middleware

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRedactPath(t *testing.T) {
	for _, c := range []struct {
		name string
		in   string
		want string
	}{
		{"订阅短链必须打码", "/s/smoketokenAAAAAAAAAAAAAAAAAAAAAA", "/s/smoketok…"},
		{"43 字符的真实形态", "/s/LbBEDimkrxmDGuVCAVzcquqQekB3hjsXte5Ih31YIAY", "/s/LbBEDimk…"},
		{"短链带后续段", "/s/abcdefghijklmnop/extra", "/s/abcdefgh…"},
		{"过短的探测保留原样（取证价值）", "/s/abc", "/s/abc"},
		{"恰好 8 位保留原样", "/s/abcdefgh", "/s/abcdefgh"},
		{"其它路径原样", "/api/v1/user/me", "/api/v1/user/me"},
		{"订单号不能被打掉：排障要按它搜日志", "/api/v1/orders/BP20260817ABCD", "/api/v1/orders/BP20260817ABCD"},
		{"前缀相似但不是短链", "/some/s/token", "/some/s/token"},
		{"根路径", "/", "/"},
		{"空", "", ""},
	} {
		if got := RedactPath(c.in); got != c.want {
			t.Errorf("%s: RedactPath(%q) = %q，期望 %q", c.name, c.in, got, c.want)
		}
	}
}

// TestAccessLogDoesNotLeakSubscriptionToken 是回归测试。
//
// 这条泄漏是真实存在过的：subscription.go 小心地不打 token，
// AccessLog 也小心地只记 r.URL.Path 而不记 query（节点密钥因此安全），
// 两个文件各自都对 —— 但订阅短链把凭据放在**路径**里，于是它照样进了日志。
// 这类缺陷只有把两边放在一起看才会暴露，所以断言必须钉在日志输出上，
// 而不只是钉在 RedactPath 的单元行为上。
func TestAccessLogDoesNotLeakSubscriptionToken(t *testing.T) {
	const token = "SECRETTOKEN0123456789abcdefghij"

	var buf strings.Builder
	logger := slog.New(slog.NewJSONHandler(&buf, nil))

	h := AccessLog(logger)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodGet, "/s/"+token, nil)
	h.ServeHTTP(httptest.NewRecorder(), req)

	out := buf.String()
	if strings.Contains(out, token) {
		t.Fatalf("访问日志里出现了订阅 token 明文：%s", out)
	}
	if !strings.Contains(out, "/s/SECRETTO…") {
		t.Errorf("期望日志里是打码后的路径，实际：%s", out)
	}
}

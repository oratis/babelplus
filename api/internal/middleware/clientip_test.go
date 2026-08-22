package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// 这组用例钉的是**取哪一段**，不是「信不信 XFF」。
//
// 曾经的实现取最左段。在 Cloud Run 这种「入口只追加、不剥离」的拓扑下，
// 调用方自带的 XFF 值原封不动留在最左边，于是 trustProxy=true 这条分支
// 返回的是纯粹的用户输入 —— 而这个值会写进 subscription_fetch_log.request_ip
// （账号共享检测的唯一数据源）与 auth 的 per-IP 限流。
func TestClientIPTakesRightmostForwardedFor(t *testing.T) {
	for _, c := range []struct {
		name       string
		xff        string
		remoteAddr string
		trustProxy bool
		want       string
	}{
		{
			name:       "伪造头在最左：必须取入口追加的最右段",
			xff:        "9.9.9.9, 203.0.113.5",
			remoteAddr: "169.254.1.1:443",
			trustProxy: true,
			want:       "203.0.113.5",
		},
		{
			name:       "多段伪造同样只认最右",
			xff:        "9.9.9.9, 8.8.8.8, 7.7.7.7, 203.0.113.5",
			remoteAddr: "169.254.1.1:443",
			trustProxy: true,
			want:       "203.0.113.5",
		},
		{
			name:       "单段（调用方没带，只有入口追加的那段）",
			xff:        "203.0.113.5",
			remoteAddr: "169.254.1.1:443",
			trustProxy: true,
			want:       "203.0.113.5",
		},
		{
			name:       "尾部空段要跳过",
			xff:        "9.9.9.9, 203.0.113.5, ",
			remoteAddr: "169.254.1.1:443",
			trustProxy: true,
			want:       "203.0.113.5",
		},
		{
			name:       "全是空段则回退到 RemoteAddr",
			xff:        " , ,",
			remoteAddr: "198.51.100.7:12345",
			trustProxy: true,
			want:       "198.51.100.7",
		},
		{
			name:       "IPv6 的入口段原样返回（不做归一化，写库前调用方自己 ParseAddr）",
			xff:        "9.9.9.9, 2001:db8::1",
			remoteAddr: "169.254.1.1:443",
			trustProxy: true,
			want:       "2001:db8::1",
		},
		{
			name:       "trustProxy=false 时 XFF 一概不看",
			xff:        "9.9.9.9, 203.0.113.5",
			remoteAddr: "198.51.100.7:12345",
			trustProxy: false,
			want:       "198.51.100.7",
		},
		{
			name:       "没有 XFF 时回退到 RemoteAddr",
			xff:        "",
			remoteAddr: "198.51.100.7:12345",
			trustProxy: true,
			want:       "198.51.100.7",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			r.RemoteAddr = c.remoteAddr
			if c.xff != "" {
				r.Header.Set("X-Forwarded-For", c.xff)
			}
			if got := ClientIP(r, c.trustProxy); got != c.want {
				t.Errorf("ClientIP() = %q，期望 %q", got, c.want)
			}
		})
	}
}

// RemoteAddr 不带端口时（httptest 之外的少数场景）原样返回，不能崩。
func TestClientIPRemoteAddrWithoutPort(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "198.51.100.7"
	if got := ClientIP(r, false); got != "198.51.100.7" {
		t.Errorf("ClientIP() = %q，期望 %q", got, "198.51.100.7")
	}
}

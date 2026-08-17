package httpx

import "testing"

// ETag 弱比较是最容易写错、错了又最难发现的一处：
// 按强比较实现的话，表现是「ETag 配了但请求量一点没降」，
// 而日志里一切正常 —— 只能靠这个测试兜住。
func TestMatchesETag(t *testing.T) {
	cases := []struct {
		name        string
		ifNoneMatch string
		current     string
		want        bool
	}{
		{"完全相同", `"u-42"`, `"u-42"`, true},
		{"修订号不同", `"u-41"`, `"u-42"`, false},
		{"客户端弱标记，服务端强", `W/"u-42"`, `"u-42"`, true},
		{"服务端弱标记，客户端强", `"u-42"`, `W/"u-42"`, true},
		{"两侧都弱", `W/"u-42"`, `W/"u-42"`, true},
		{"小写 w/ 前缀", `w/"u-42"`, `"u-42"`, true},
		{"多值列表命中其一", `"u-40", W/"u-42", "u-41"`, `"u-42"`, true},
		{"多值列表全不命中", `"u-40", "u-41"`, `"u-42"`, false},
		{"星号匹配一切", `*`, `"u-42"`, true},
		{"空头不命中", ``, `"u-42"`, false},
		{"仅空白不命中", `   `, `"u-42"`, false},
		{"作用域不同不得误命中", `"c-42"`, `"u-42"`, false},
		{"列表带多余空白", `  W/"u-42"  ,  "u-99"  `, `"u-42"`, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := MatchesETag(c.ifNoneMatch, c.current); got != c.want {
				t.Errorf("MatchesETag(%q, %q) = %v, 期望 %v", c.ifNoneMatch, c.current, got, c.want)
			}
		})
	}
}

// 两个端点的修订号可能撞上同一个数字，作用域前缀是唯一的隔离手段。
func TestRevisionETagScopeIsolation(t *testing.T) {
	u := RevisionETag(ScopeUserList, 42)
	c := RevisionETag(ScopeConfig, 42)
	if u == c {
		t.Fatalf("相同修订号的不同作用域必须产生不同 ETag：user=%s config=%s", u, c)
	}
	if MatchesETag(u, c) {
		t.Errorf("跨作用域不得命中：%s vs %s", u, c)
	}
}

func TestStrongETagStableAndDistinct(t *testing.T) {
	a := StrongETag([]byte(`{"k":1}`))
	if a != StrongETag([]byte(`{"k":1}`)) {
		t.Error("相同内容必须产生相同 ETag")
	}
	if a == StrongETag([]byte(`{"k":2}`)) {
		t.Error("不同内容必须产生不同 ETag")
	}
	if len(a) < 3 || a[0] != '"' || a[len(a)-1] != '"' {
		t.Errorf("ETag 必须带引号，得到 %q", a)
	}
}

// subscription-userinfo 是与 Clash / sing-box 生态的硬接口，格式不能自创。
func TestFormatSubscriptionUserInfo(t *testing.T) {
	got := FormatSubscriptionUserInfo(1024, 2048, 107374182400, 1767225600)
	want := "upload=1024; download=2048; total=107374182400; expire=1767225600"
	if got != want {
		t.Errorf("格式不符\n得到 %q\n期望 %q", got, want)
	}
	// expire=0 表示不限时（对应 users.expired_at IS NULL）。
	if got := FormatSubscriptionUserInfo(0, 0, 0, 0); got != "upload=0; download=0; total=0; expire=0" {
		t.Errorf("不限时套餐格式不符：%q", got)
	}
}

package config

import (
	"strings"
	"testing"
)

func TestParseAllowedOriginsHappyPath(t *testing.T) {
	got, err := parseAllowedOrigins(" https://web.babel.plus , https://admin.babel.plus ", "prod")
	if err != nil {
		t.Fatalf("实得 %v", err)
	}
	want := []string{"https://web.babel.plus", "https://admin.babel.plus"}
	if len(got) != len(want) {
		t.Fatalf("got %v，应为 %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v，应为 %v", got, want)
		}
	}
}

func TestParseAllowedOriginsNormalizesAndDedupes(t *testing.T) {
	got, err := parseAllowedOrigins("HTTPS://WEB.Babel.Plus,https://web.babel.plus", "prod")
	if err != nil {
		t.Fatalf("实得 %v", err)
	}
	if len(got) != 1 || got[0] != "https://web.babel.plus" {
		t.Fatalf("大小写不同的同一 origin 应归一并去重，实得 %v", got)
	}
}

// 非 dev 环境缺白名单 = 拒绝启动。空配置的生产实例表现为「前端全线跨域失败」，
// 那比启动失败难查得多，而且是在用户面前失败。
func TestParseAllowedOriginsFailClosed(t *testing.T) {
	for _, env := range []string{"staging", "prod"} {
		if _, err := parseAllowedOrigins("", env); err == nil {
			t.Fatalf("%s 环境缺 BP_ALLOWED_ORIGINS 应拒绝启动", env)
		}
		if _, err := parseAllowedOrigins("  ,  , ", env); err == nil {
			t.Fatalf("%s 环境全是空白项也应拒绝启动", env)
		}
	}
}

func TestParseAllowedOriginsDevDefault(t *testing.T) {
	got, err := parseAllowedOrigins("", "dev")
	if err != nil {
		t.Fatalf("dev 应有默认值，实得 %v", err)
	}
	if len(got) != 2 || got[0] != "http://localhost:5173" || got[1] != "http://localhost:5174" {
		t.Fatalf("dev 默认值应为两个 vite 端口，实得 %v", got)
	}
	// 显式配置覆盖默认值。
	got, err = parseAllowedOrigins("http://127.0.0.1:5173", "dev")
	if err != nil || len(got) != 1 {
		t.Fatalf("显式配置应覆盖默认，实得 %v %v", got, err)
	}
}

func TestParseAllowedOriginsRejectsBadForms(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		env  string
	}{
		{"通配符", "*", "prod"},
		{"null", "null", "prod"},
		{"NULL 大小写变体", "NULL", "prod"},
		{"带尾斜杠", "https://web.babel.plus/", "prod"},
		{"带路径", "https://web.babel.plus/app", "prod"},
		{"带查询串", "https://web.babel.plus?x=1", "prod"},
		{"缺 scheme", "web.babel.plus", "prod"},
		{"错误 scheme", "ftp://web.babel.plus", "prod"},
		{"file scheme", "file://", "prod"},
		{"带用户名密码", "https://u:p@web.babel.plus", "prod"},
		{"prod 下的明文 http", "http://web.babel.plus", "prod"},
		{"通配符子域", "https://*.babel.plus", "prod"},
		{"混进一个坏项", "https://web.babel.plus,https://bad.babel.plus/", "prod"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := parseAllowedOrigins(tc.raw, tc.env); err == nil {
				t.Fatalf("%q 应被拒绝", tc.raw)
			}
		})
	}
}

// 明文 http 只在非 prod 环境被接受（本地 vite、staging 的临时环境）。
func TestParseAllowedOriginsHTTPAllowedOutsideProd(t *testing.T) {
	for _, env := range []string{"dev", "staging"} {
		got, err := parseAllowedOrigins("http://localhost:5173", env)
		if err != nil {
			t.Fatalf("%s 环境应接受明文 http，实得 %v", env, err)
		}
		if len(got) != 1 {
			t.Fatalf("实得 %v", got)
		}
	}
}

// 报错信息必须点出是哪一项坏了 —— 白名单可能有十几个域名。
func TestParseAllowedOriginsErrorNamesTheOffender(t *testing.T) {
	_, err := parseAllowedOrigins("https://a.babel.plus,https://bad.babel.plus/", "prod")
	if err == nil {
		t.Fatal("应报错")
	}
	if !strings.Contains(err.Error(), "https://bad.babel.plus/") {
		t.Fatalf("报错应指出具体是哪一项：%v", err)
	}
}

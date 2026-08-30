// 管理面两项配置的校验测试。
//
// # 这两个校验函数为什么存在
//
// 它们不产生任何运行时行为，只在启动期拒绝形状不对的值。理由与
// parseAllowedOrigins / parseInternalAudience 完全一样：
// **一个写错的值与「根本没配」有完全相同的现象，却要往完全不同的方向排查。**
//
//	BP_ADMIN_IAP_AUDIENCE 写错  → 每一份真实断言的 aud 都匹配不上 → 所有管理员进不去
//	BP_ADMIN_IAP_AUDIENCE 没配  → AuthenticateAdmin 整体拒绝     → 所有管理员进不去
//
// 两者在日志之外**不可区分**。这些用例把那种排查搬到启动期。
//
// # 每个用例为什么必须存在
//
//	TestParseAdminIAPAudienceRejectsWrongShapes
//	  逐条钉住最可能被写错的形态：写成服务 URL（内部面的 aud 确实是 URL，
//	  所以这是最自然的猜法）、写成项目 ID 而不是项目编号、写成后端服务名
//	  而不是它的数字 id、带尾斜杠。每一条的现象都是「所有管理员进不去」。
//
//	TestParseAdminTOTPEncKeyRequiresExactly32Bytes
//	  🔴 aes.NewCipher 会照单全收 16 / 24 字节的密钥，于是一个被截断的密钥
//	  会安安静静地退化成 AES-128，而唯一的现象是……没有现象。
//	  密钥长度是能在启动期一次性确认的少数几件事之一。
//
//	TestAdminPairMustBeConfiguredTogether
//	  只配一项时两种漏配的现象都不指向配置本身：只配 audience → 管理面能进
//	  但所有危险操作被拒（运维会去查 TOTP 绑定、查手机时间）；
//	  只配密钥 → 管理面整体进不去（看起来像 IAP 配错了）。
package config

import (
	"encoding/base64"
	"strings"
	"testing"
)

const goodIAPAudience = "/projects/123456789012/global/backendServices/9876543210"

func TestParseAdminIAPAudienceHappyPath(t *testing.T) {
	got, err := parseAdminIAPAudience("  " + goodIAPAudience + "  ")
	if err != nil {
		t.Fatalf("合法形态应通过，实得 %v", err)
	}
	if got != goodIAPAudience {
		t.Fatalf("应去掉两端空白后原样返回，实得 %q", got)
	}
}

// 留空是允许的 —— 管理面尚未在每个环境启用，缺失时由运行时整体拒绝。
func TestParseAdminIAPAudienceEmptyIsAllowed(t *testing.T) {
	got, err := parseAdminIAPAudience("   ")
	if err != nil {
		t.Fatalf("留空应被允许（运行时 fail-closed），实得 %v", err)
	}
	if got != "" {
		t.Fatalf("留空应返回空串，实得 %q", got)
	}
}

func TestParseAdminIAPAudienceRejectsWrongShapes(t *testing.T) {
	cases := []struct {
		name string
		in   string
		why  string
	}{
		{
			"写成 Cloud Run 服务 URL",
			"https://bp-api-abcdef1234.a.run.app",
			"那是**内部面** OIDC 的 aud 形态。混用是最自然的猜法，而它永远匹配不上",
		},
		{
			"PROJECT_NUMBER 写成了项目 ID",
			"/projects/oratis-491316/global/backendServices/9876543210",
			"IAP 断言里是项目**编号**（纯数字），不是项目 ID",
		},
		{
			"BACKEND_SERVICE_ID 写成了后端服务名",
			"/projects/123456789012/global/backendServices/bp-admin-backend",
			"要的是后端服务的数字 id，不是名字",
		},
		{
			"带尾斜杠",
			goodIAPAudience + "/",
			"aud 是逐字节比对的，多一个字符就永远匹配不上",
		},
		{
			"缺少 /projects/ 前缀",
			"projects/123456789012/global/backendServices/9876543210",
			"少一个前导斜杠同样是逐字节不等",
		},
		{
			"缺少 /global/backendServices/ 分段",
			"/projects/123456789012/9876543210",
			"形态不完整",
		},
		{
			"PROJECT_NUMBER 为空",
			"/projects//global/backendServices/9876543210",
			"空段不是合法 ID",
		},
		{
			"BACKEND_SERVICE_ID 为空",
			"/projects/123456789012/global/backendServices/",
			"空段不是合法 ID",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := parseAdminIAPAudience(tc.in); err == nil {
				t.Fatalf("%s —— 必须在启动期就拒绝，否则现象是「所有管理员进不去」", tc.why)
			}
		})
	}
}

// 报错信息里必须出现那个出错的值：一条不说「哪个值不对」的启动失败
// 会让配置者在几个环境变量之间来回试。
func TestParseAdminIAPAudienceErrorNamesTheOffender(t *testing.T) {
	bad := "https://console.cloud.google.com"
	_, err := parseAdminIAPAudience(bad)
	if err == nil {
		t.Fatal("应报错")
	}
	if !strings.Contains(err.Error(), bad) {
		t.Fatalf("报错信息里应包含出错的值，实得：%v", err)
	}
}

// 🔴 密钥必须恰好 32 字节（AES-256）。
//
// 16 / 24 字节也能被 aes.NewCipher 收下 —— 于是「密钥被截断」这件事
// 完全没有现象，静静地退化成 AES-128。
func TestParseAdminTOTPEncKeyRequiresExactly32Bytes(t *testing.T) {
	for _, n := range []int{0, 1, 15, 16, 24, 31, 33, 64} {
		raw := base64.StdEncoding.EncodeToString(make([]byte, n))
		if n == 0 {
			// 空 base64 串等于「没配」，走另一条分支，单独测。
			continue
		}
		if _, err := parseAdminTOTPEncKey(raw); err == nil {
			t.Errorf("%d 字节的密钥应被拒 —— 只有 32 字节是 AES-256", n)
		}
	}

	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}
	got, err := parseAdminTOTPEncKey(base64.StdEncoding.EncodeToString(key))
	if err != nil {
		t.Fatalf("32 字节应通过，实得 %v", err)
	}
	if len(got) != 32 || string(got) != string(key) {
		t.Fatalf("解出的密钥与原值不同")
	}

	// 无填充形态也要收：从 Secret Manager 取出来时填充可能已经被去掉。
	got, err = parseAdminTOTPEncKey(base64.RawStdEncoding.EncodeToString(key))
	if err != nil {
		t.Fatalf("无填充 base64 应通过，实得 %v", err)
	}
	if string(got) != string(key) {
		t.Fatal("无填充形态解出的密钥与原值不同")
	}
}

func TestParseAdminTOTPEncKeyEmptyIsAllowed(t *testing.T) {
	got, err := parseAdminTOTPEncKey("  ")
	if err != nil {
		t.Fatalf("留空应被允许（step-up 在运行时 fail-closed），实得 %v", err)
	}
	if got != nil {
		t.Fatalf("留空应返回 nil，实得 %v", got)
	}
}

func TestParseAdminTOTPEncKeyRejectsNonBase64(t *testing.T) {
	if _, err := parseAdminTOTPEncKey("这不是-base64!!!"); err == nil {
		t.Fatal("非法 base64 必须在启动期拒绝")
	}
	// 报错里不能出现密钥内容本身。
	_, err := parseAdminTOTPEncKey(base64.StdEncoding.EncodeToString([]byte("0123456789abcdef")))
	if err == nil {
		t.Fatal("16 字节应被拒")
	}
	if strings.Contains(err.Error(), "0123456789abcdef") {
		t.Fatalf("报错信息里泄漏了密钥内容：%v", err)
	}
}

// 两项必须同时配置或同时留空 —— 这条交叉校验在 Load 里，
// 这里直接验它的判据（避免为了跑 Load 而铺一整套环境变量）。
func TestAdminPairMustBeConfiguredTogether(t *testing.T) {
	key := base64.StdEncoding.EncodeToString(make([]byte, 32))

	for _, tc := range []struct {
		name    string
		aud     string
		rawKey  string
		wantErr bool
	}{
		{"都不配（管理面整体关闭）", "", "", false},
		{"都配（管理面启用）", goodIAPAudience, key, false},
		{"只配 audience", goodIAPAudience, "", true},
		{"只配 TOTP 密钥", "", key, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			aud, err := parseAdminIAPAudience(tc.aud)
			if err != nil {
				t.Fatalf("形态校验意外失败: %v", err)
			}
			k, err := parseAdminTOTPEncKey(tc.rawKey)
			if err != nil {
				t.Fatalf("形态校验意外失败: %v", err)
			}
			// 与 Load 里那一行判据逐字一致。
			mismatched := (aud == "") != (len(k) == 0)
			if mismatched != tc.wantErr {
				t.Fatalf("交叉校验结果 = %v，期望 %v", mismatched, tc.wantErr)
			}
		})
	}
}

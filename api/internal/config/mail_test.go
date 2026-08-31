// 邮件发信整组配置的校验测试。
//
// 与 admin_test.go 同一条动机：**半配与没配的现象完全相同（信停在 queued），
// 却要往完全不同的方向排查** —— 这些用例把那种排查搬到启动期。
//
//	TestParseMailSettingsAllOrNothing
//	  半配必须被拒绝。最典型的半配是「配了 host/用户名/密码，忘了 BP_MAIL_ESP」——
//	  发信器不装配，所有信停在 queued，而配置者以为已经接上了。
//
//	TestParseMailSettingsPortWhitelist
//	  🔴 25 是明文提交口。SMTP 凭据在 25 上会裸奔，而「能连上、能发出去」
//	  这个表象与 587 完全一样 —— 唯一的差别是凭据在网络上是不是明文。
//
//	TestParseMailSettingsFromMustParse
//	  写错的 From 会在第一封信上被 ESP 拒掉，而那封信多半是某个真实用户的验证码。
package config

import (
	"strings"
	"testing"
)

func fullMail(port string) (string, string, string, string, string, string) {
	return "smtp.example.com", port, "user", "pass", "babel.plus <no-reply@babel.plus>", "ses"
}

func TestParseMailSettingsHappyPath(t *testing.T) {
	host, port, user, pass, from, esp := fullMail("")
	m, err := parseMailSettings(host, port, user, pass, from, esp)
	if err != nil {
		t.Fatalf("整组配置应通过：%v", err)
	}
	if m == (MailSettings{}) || m.Port != 587 {
		t.Fatalf("端口缺省应为 587，实得 %+v", m)
	}
	if m.Host != "smtp.example.com" || m.ESP != "ses" {
		t.Fatalf("字段没装对：%+v", m)
	}
}

func TestParseMailSettingsEmptyIsAllowed(t *testing.T) {
	m, err := parseMailSettings("", "", "", "", "", "")
	if err != nil {
		t.Fatalf("整组留空是合法状态（ESP 未选型），实得 %v", err)
	}
	if m != (MailSettings{}) {
		t.Fatalf("留空应返回零值，实得 %+v", m)
	}
}

func TestParseMailSettingsAllOrNothing(t *testing.T) {
	// 漏掉 ESP 标签是最典型的半配：发信器不装配、信停在 queued，
	// 而配置者以为已经接上了。
	if _, err := parseMailSettings("smtp.example.com", "", "user", "pass",
		"babel.plus <no-reply@babel.plus>", ""); err == nil {
		t.Fatal("半配必须在启动期被拒绝")
	}
	// 只配端口同样要拒：端口单独存在没有意义，多半是漏了其它五项。
	if _, err := parseMailSettings("", "587", "", "", "", ""); err == nil {
		t.Fatal("只配 BP_SMTP_PORT 必须被拒绝")
	}
}

func TestParseMailSettingsPortWhitelist(t *testing.T) {
	for _, bad := range []string{"25", "2525", "8025", "abc"} {
		host, _, user, pass, from, esp := fullMail("")
		if _, err := parseMailSettings(host, bad, user, pass, from, esp); err == nil {
			t.Fatalf("端口 %q 必须被拒绝（只允许 587 / 465）", bad)
		}
	}
	host, _, user, pass, from, esp := fullMail("")
	m, err := parseMailSettings(host, "465", user, pass, from, esp)
	if err != nil || m.Port != 465 {
		t.Fatalf("465（隐式 TLS）应通过，实得 %v / %+v", err, m)
	}
}

func TestParseMailSettingsFromMustParse(t *testing.T) {
	host, _, user, pass, _, esp := fullMail("")
	_, err := parseMailSettings(host, "", user, pass, "not-an-address", esp)
	if err == nil || !strings.Contains(err.Error(), "BP_MAIL_FROM") {
		t.Fatalf("非法 From 必须在启动期被拒绝并指名字段，实得 %v", err)
	}
}

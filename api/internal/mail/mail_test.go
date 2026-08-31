// 信件组装与守卫的测试。SMTP 会话本身（submit）不在进程内测 ——
// 它的正确性由「对着真 ESP 发一封实测信」验证（ADR 0002 §7 的送达率实测
// 本来就是接线后的第一件事），假 SMTP 服务器测不出任何那里测不出的东西。
package mail

import (
	"encoding/base64"
	"net/mail"
	"strings"
	"testing"
	"time"
)

func testFrom(t *testing.T) *mail.Address {
	t.Helper()
	a, err := mail.ParseAddress("babel.plus <no-reply@babel.plus>")
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestBuildMessage(t *testing.T) {
	body := "您好，\r\n验证码：123456\r\n—— babel.plus"
	raw := string(buildMessage(testFrom(t), "u@qq.com", "【babel.plus】邮箱验证码", body,
		"<abc@babel.plus>", time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)))

	head, b64, ok := strings.Cut(raw, "\r\n\r\n")
	if !ok {
		t.Fatal("信件必须有空行分隔头与体")
	}
	for _, want := range []string{
		"From: \"babel.plus\" <no-reply@babel.plus>",
		"To: <u@qq.com>",
		"MIME-Version: 1.0",
		"Content-Type: text/plain; charset=utf-8",
		"Content-Transfer-Encoding: base64",
		"Message-ID: <abc@babel.plus>",
	} {
		if !strings.Contains(head, want) {
			t.Fatalf("头部缺 %q：\n%s", want, head)
		}
	}
	// 🔴 中文主题必须走 RFC 2047 编码：裸 UTF-8 主题在部分国内 MTA 上会变问号，
	// 而「主题乱码的验证码信」在垃圾判定里是减分项。
	if strings.Contains(head, "【babel.plus】") || !strings.Contains(head, "=?utf-8?q?") {
		t.Fatalf("主题应为 Q 编码，实际：\n%s", head)
	}
	// 正文 base64 解码后必须逐字节还原。
	decoded, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(strings.TrimSpace(b64), "\r\n", ""))
	if err != nil || string(decoded) != body {
		t.Fatalf("正文没有无损往返：%v %q", err, decoded)
	}
}

func TestWrapBase64Is76Cols(t *testing.T) {
	out := wrapBase64(strings.Repeat("测试正文", 100))
	for i, line := range strings.Split(strings.TrimRight(out, "\r\n"), "\r\n") {
		if len(line) > 76 {
			t.Fatalf("第 %d 行超过 76 列（%d）：RFC 2045 的折行是部分 MTA 的硬要求", i, len(line))
		}
	}
}

// 🔴 头注入守卫：to 与 subject 今天都来自可信源（自己的库、代码常量），
// 这条钉住的是「将来有人把用户输入接进头部」的那一天。
func TestValidateHeaderValueRejectsInjection(t *testing.T) {
	for _, bad := range []string{"a@b.com\r\nBcc: x@y.com", "subject\ninjected", "", "  "} {
		if err := validateHeaderValue(bad); err == nil {
			t.Fatalf("%q 必须被拒绝", bad)
		}
	}
	if err := validateHeaderValue("u@qq.com"); err != nil {
		t.Fatalf("正常值不应被拒：%v", err)
	}
}

func TestNewMessageIDShape(t *testing.T) {
	id := newMessageID("no-reply@babel.plus")
	if !strings.HasPrefix(id, "<") || !strings.HasSuffix(id, "@babel.plus>") {
		t.Fatalf("Message-ID 形状不对：%q", id)
	}
	if id == newMessageID("no-reply@babel.plus") {
		t.Fatal("两次生成不应相同")
	}
}

func TestNewValidatesFrom(t *testing.T) {
	if _, err := New(Config{From: "not-an-address"}); err == nil {
		t.Fatal("非法 From 必须被拒（config.Load 之外的最后一道防线）")
	}
	s, err := New(Config{Host: "smtp.example.com", Port: 587, From: "a <a@b.com>", ESPName: "ses"})
	if err != nil || s.Name() != "ses" {
		t.Fatalf("合法配置应通过：%v", err)
	}
}

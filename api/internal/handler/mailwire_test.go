// ESP 装配与正文渲染的测试。
//
// 带 🔴 的两条都是静默边界：
//   - 队列里只允许「正文能从模板键完整推导」的信 —— verify_code 混进去的后果
//     是发出一封没有码的验证码信（或一封空信），用户举着它没有任何办法。
//   - 空正文拒发 —— 空信长得像钓鱼，且它只可能来自漏渲染的代码路径。
package handler

import (
	"context"
	"strings"
	"testing"
	"time"

	dbgen "github.com/oratis/babelplus/api/db/gen"
)

func TestBodyForQueuedTemplate(t *testing.T) {
	for _, tmpl := range []string{templateExpireRemind, templateTrafficRemind, broadcastTemplateDomain} {
		body, ok := bodyForQueuedTemplate(tmpl)
		if !ok || strings.TrimSpace(body) == "" {
			t.Fatalf("队列模板 %q 必须渲染出非空正文", tmpl)
		}
	}

	// 🔴 验证码类模板必须被拒：正文需要明文码，而码只在签发那一刻存在。
	// 这两个键能到队列里的唯一来路是「ESP 未配置时期的记账行」—— 那些码早已过期，
	// 渲染出一封没有码的信发出去，比不发更糟。
	for _, tmpl := range []string{emailTemplateVerifyCode, emailTemplatePasswordReset, "unknown_template"} {
		if _, ok := bodyForQueuedTemplate(tmpl); ok {
			t.Fatalf("模板 %q 不许从队列渲染", tmpl)
		}
	}
}

func TestRenderVerificationBodyCarriesCodeAndTTL(t *testing.T) {
	body := renderVerificationBody(dbgen.VerificationPurposeRegister, "482913", 10*time.Minute)
	if !strings.Contains(body, "482913") || !strings.Contains(body, "10 分钟") {
		t.Fatalf("验证码与有效期必须都在正文里：%q", body)
	}
	reset := renderVerificationBody(dbgen.VerificationPurposePasswordReset, "TOK", 30*time.Minute)
	if !strings.Contains(reset, "TOK") || !strings.Contains(reset, "30 分钟") {
		t.Fatalf("重置信同理：%q", reset)
	}
	if !strings.Contains(reset, "重置") {
		t.Fatal("重置信必须说明自己是重置密码 —— 用户没发起过就该起疑")
	}
}

// 🔴 空正文拒发：它只可能来自漏渲染的代码路径，而一封空信在用户眼里就是钓鱼。
func TestSMTPSenderRefusesEmptyBody(t *testing.T) {
	if _, err := (smtpSender{}).Send(context.Background(), MailMessage{
		To: "u@qq.com", Subject: "s", Template: templateExpireRemind, Body: "   ",
	}); err == nil {
		t.Fatal("空正文必须被拒绝")
	}
}

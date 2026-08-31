package handler

// ESP 发信的装配与正文渲染。
//
// 依赖方向：handler → internal/mail，mail 包不知道 handler 的存在 ——
// MailMessage / MailSender 属于 handler（task.go），所以适配器住在这里。
//
// 正文渲染也住在这里而不是 mail 包：正文是业务文案（验证码、提醒话术），
// mail 包只负责把「一封已经写好的信」按 SMTP 投出去。

import (
	"context"
	"fmt"
	"strings"
	"time"

	dbgen "github.com/oratis/babelplus/api/db/gen"
	"github.com/oratis/babelplus/api/internal/mail"
)

// smtpSender 把 internal/mail 的 SMTP 实现适配到 MailSender。
type smtpSender struct{ s *mail.Sender }

func (a smtpSender) Name() string     { return a.s.Name() }
func (a smtpSender) Configured() bool { return true }

func (a smtpSender) Send(ctx context.Context, msg MailMessage) (string, error) {
	if strings.TrimSpace(msg.Body) == "" {
		// 没有正文只可能是某条代码路径漏了渲染。发一封空信出去的后果是
		// 用户收到一封长得像钓鱼的空邮件 —— 拒发，让失败落在 email_log 里可见。
		return "", fmt.Errorf("模板 %q 没有渲染正文，拒绝发送", msg.Template)
	}
	return a.s.Send(ctx, msg.To, msg.Subject, msg.Body)
}

// bodyForQueuedTemplate 渲染**队列信**的正文。
//
// email_log 只有模板键没有正文列（admin_catalog.go 的 501 注释登记过这一点），
// 所以队列里只允许「正文可以从模板键完整推导」的信。三种：到期提醒、流量提醒、
// 域名广播。
//
// 🔴 verify_code / password_reset 刻意不在其中：它们的正文需要验证码明文，
// 而明文只在签发那一刻存在（库里只有哈希，10 分钟即过期）。那两类信由
// issueVerification **同步**发送；落进队列的只有 ESP 未配置时期的记账行，
// 重投时在 runMailSend 里被响亮地标 failed —— 那些码早已过期，重试也渲染不出来。
func bodyForQueuedTemplate(template string) (string, bool) {
	switch template {
	case templateExpireRemind:
		// 数字引用扫描侧的同一批常量，文案不会与入队条件漂移。
		return fmt.Sprintf("您好，\r\n\r\n"+
			"您的 babel.plus 订阅将在 %d 天内到期。为避免服务中断，请及时登录面板续费；"+
			"如已续费请忽略本邮件。\r\n\r\n—— babel.plus", remindExpireWithinDays), true
	case templateTrafficRemind:
		return fmt.Sprintf("您好，\r\n\r\n"+
			"您本周期的流量已使用超过 %d%%。用尽后连接会暂停，您可以登录面板购买流量包，"+
			"或等待下个周期自动重置。\r\n\r\n—— babel.plus", remindTrafficThresholdPct), true
	case broadcastTemplateDomain:
		// TODO(P1): 正文里带不了新域名 —— 那需要 email_log 的正文列或 mail_broadcasts
		// 父表（admin_catalog.go 的同一条 TODO）。眼下新地址只能放在管理员填的主题里。
		return "您好，\r\n\r\n" +
			"我们的面板访问地址有更新，最新地址见本邮件主题。请及时收藏新地址，" +
			"旧地址可能随时不可用。\r\n\r\n—— babel.plus", true
	}
	return "", false
}

// renderVerificationBody 渲染验证码 / 重置口令信的正文。只在签发路径
// （issueVerification）被调用 —— secret 不落任何持久层，见 bodyForQueuedTemplate。
func renderVerificationBody(purpose dbgen.VerificationPurpose, secret string, ttl time.Duration) string {
	minutes := int(ttl.Minutes())
	if purpose == dbgen.VerificationPurposePasswordReset {
		return fmt.Sprintf("您好，\r\n\r\n"+
			"您正在重置 babel.plus 的登录密码，验证码：\r\n\r\n    %s\r\n\r\n"+
			"%d 分钟内有效。若不是您本人操作，请忽略本邮件 —— 不把验证码告诉任何人，"+
			"您的账户就仍然是安全的。\r\n\r\n—— babel.plus", secret, minutes)
	}
	return fmt.Sprintf("您好，\r\n\r\n"+
		"您的 babel.plus 邮箱验证码：\r\n\r\n    %s\r\n\r\n"+
		"%d 分钟内有效。若不是您本人操作，请忽略本邮件。\r\n\r\n—— babel.plus", secret, minutes)
}

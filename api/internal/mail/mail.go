// Package mail 是 SMTP 形态的 ESP 发信实现。
//
// 为什么是 SMTP 而不是某家的 HTTP SDK：ADR 0002 §7 要求「以国内邮箱实测送达率为
// 唯一选型依据」，候选五家（Resend / SES / Postmark / Mailgun / Brevo）全部提供
// SMTP 提交端口 —— SMTP 让「换一家测」是一次配置变更，不是一次代码变更。
// 选型定稿之后要用该家的高级能力（退信回调签名、模板、批量）再换 SDK 不迟，
// 到那时 email_log 里已经有按 esp 分组的实测数据了。
//
// 只发 text/plain：验证码与提醒用纯文本就够；HTML 在国内邮箱的垃圾判定里只会更吃亏
// （ADR 0002 §5 记录的送达风险本来就高）。
//
// 本包不知道 handler 的存在 —— MailSender 接口的适配住在 handler 侧（mailwire.go），
// 依赖方向是 handler → mail，不反向。
package mail

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"mime"
	"net"
	nm "net/mail"
	"net/smtp"
	"strconv"
	"strings"
	"time"
)

const (
	// dialTimeout 单独收紧：连不上要尽快失败，Cloud Tasks 会带退避重投。
	dialTimeout = 10 * time.Second
	// sessionTimeout 盖住整个 SMTP 会话（EHLO → AUTH → DATA → QUIT 多轮往返）。
	// 没有它的话，一个挂起的 SMTP 服务器会占住 Cloud Tasks 的投递直到 Cloud Run 请求超时。
	sessionTimeout = 30 * time.Second
)

// Config 是 SMTP 提交参数。字段与 config.Load 校验过的 BP_SMTP_* / BP_MAIL_* 一一对应，
// 这里假定形状已合法（Port ∈ {587, 465}、From 可解析）—— New 只做最后一道防线。
type Config struct {
	Host     string
	Port     int
	Username string
	Password string
	From     string // RFC 5322 形态，如 "babel.plus <no-reply@babel.plus>"
	ESPName  string // email_log.esp 的标签，如 'ses' / 'resend'
}

// Sender 是一个可并发使用的 SMTP 发信器（每次 Send 建独立连接，无共享可变状态）。
type Sender struct {
	cfg  Config
	from *nm.Address
}

// New 构造 Sender。From 解析失败返回错误 —— config.Load 已校验过同一件事，
// 走到这里失败只可能是两处校验漂移，调用方应当把它当成装配错误响亮处理。
func New(cfg Config) (*Sender, error) {
	from, err := nm.ParseAddress(cfg.From)
	if err != nil {
		return nil, fmt.Errorf("发件人地址无法解析: %w", err)
	}
	return &Sender{cfg: cfg, from: from}, nil
}

// Name 返回 ESP 标签（写进 email_log.esp）。
func (s *Sender) Name() string { return s.cfg.ESPName }

// Send 投一封纯文本信，返回我们自己生成的 Message-ID。
//
// SMTP 协议不返回服务商侧的消息 ID —— 这个值落在 email_log.provider_msg_id，
// 作用是让将来的退信 / 投递回调能对回本行（回调里带的正是 Message-ID）。
func (s *Sender) Send(ctx context.Context, to, subject, body string) (string, error) {
	if err := validateHeaderValue(to); err != nil {
		return "", fmt.Errorf("收件人非法: %w", err)
	}
	if err := validateHeaderValue(subject); err != nil {
		return "", fmt.Errorf("主题非法: %w", err)
	}
	msgID := newMessageID(s.from.Address)
	raw := buildMessage(s.from, to, subject, body, msgID, time.Now())
	if err := s.submit(ctx, to, raw); err != nil {
		return "", err
	}
	return msgID, nil
}

// validateHeaderValue 是 SMTP 头注入守卫。
// to 来自我们自己的库、subject 来自代码常量 —— 这条防的是
// 「将来有人把用户输入接进头部」的那一天，而不是今天的调用方。
func validateHeaderValue(v string) error {
	if strings.ContainsAny(v, "\r\n") {
		return errors.New("含换行（SMTP 头注入形态），拒绝发送")
	}
	if strings.TrimSpace(v) == "" {
		return errors.New("为空")
	}
	return nil
}

// submit 走完整个 SMTP 会话。587 走 STARTTLS（不支持则中止，绝不明文发凭据）；
// 465 走隐式 TLS。两条路径都由 config.Load 的端口白名单保证不会落到明文口。
func (s *Sender) submit(ctx context.Context, to string, raw []byte) error {
	addr := net.JoinHostPort(s.cfg.Host, strconv.Itoa(s.cfg.Port))
	d := &net.Dialer{Timeout: dialTimeout}

	var conn net.Conn
	var err error
	if s.cfg.Port == 465 {
		conn, err = (&tls.Dialer{NetDialer: d, Config: &tls.Config{ServerName: s.cfg.Host}}).DialContext(ctx, "tcp", addr)
	} else {
		conn, err = d.DialContext(ctx, "tcp", addr)
	}
	if err != nil {
		return fmt.Errorf("连接 SMTP %s 失败: %w", addr, err)
	}
	deadline := time.Now().Add(sessionTimeout)
	if dl, ok := ctx.Deadline(); ok && dl.Before(deadline) {
		deadline = dl
	}
	_ = conn.SetDeadline(deadline)

	c, err := smtp.NewClient(conn, s.cfg.Host)
	if err != nil {
		_ = conn.Close()
		return fmt.Errorf("SMTP 握手失败: %w", err)
	}
	defer c.Close()

	if s.cfg.Port != 465 {
		// 🔴 STARTTLS 不可协商：服务器不支持就中止。
		// smtp.PlainAuth 自己也会拒绝在非 TLS 连接上发凭据，这里提前拦是为了
		// 让错误信息指向真正的原因（服务器不支持加密），而不是「认证失败」。
		if ok, _ := c.Extension("STARTTLS"); !ok {
			return errors.New("SMTP 服务器不支持 STARTTLS，拒绝在明文连接上继续")
		}
		if err := c.StartTLS(&tls.Config{ServerName: s.cfg.Host}); err != nil {
			return fmt.Errorf("STARTTLS 失败: %w", err)
		}
	}
	if err := c.Auth(smtp.PlainAuth("", s.cfg.Username, s.cfg.Password, s.cfg.Host)); err != nil {
		return fmt.Errorf("SMTP 认证失败: %w", err)
	}
	if err := c.Mail(s.from.Address); err != nil {
		return fmt.Errorf("MAIL FROM 被拒: %w", err)
	}
	if err := c.Rcpt(to); err != nil {
		return fmt.Errorf("RCPT TO 被拒: %w", err)
	}
	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("DATA 被拒: %w", err)
	}
	if _, err := w.Write(raw); err != nil {
		_ = w.Close()
		return fmt.Errorf("写入信体失败: %w", err)
	}
	if err := w.Close(); err != nil {
		// DATA 结束时的服务器判决 —— 内容被拒（垃圾判定、大小超限）走这里。
		return fmt.Errorf("信体未被接受: %w", err)
	}
	return c.Quit()
}

// buildMessage 组装 RFC 5322 信件。正文一律 base64（8 位安全，中文不经受
// 各家 MTA 对裸 8bit 的不同宽容度考验）；主题走 RFC 2047 Q 编码。
func buildMessage(from *nm.Address, to, subject, body, msgID string, now time.Time) []byte {
	var b strings.Builder
	b.WriteString("From: " + from.String() + "\r\n")
	b.WriteString("To: <" + to + ">\r\n")
	b.WriteString("Subject: " + mime.QEncoding.Encode("utf-8", subject) + "\r\n")
	b.WriteString("Date: " + now.Format(time.RFC1123Z) + "\r\n")
	b.WriteString("Message-ID: " + msgID + "\r\n")
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/plain; charset=utf-8\r\n")
	b.WriteString("Content-Transfer-Encoding: base64\r\n")
	b.WriteString("\r\n")
	b.WriteString(wrapBase64(body))
	return []byte(b.String())
}

// wrapBase64 按 RFC 2045 的 76 列折行。
func wrapBase64(s string) string {
	enc := base64.StdEncoding.EncodeToString([]byte(s))
	var b strings.Builder
	for len(enc) > 76 {
		b.WriteString(enc[:76])
		b.WriteString("\r\n")
		enc = enc[76:]
	}
	b.WriteString(enc)
	b.WriteString("\r\n")
	return b.String()
}

// newMessageID 生成 `<随机16字节hex@发件域>` 形态的 Message-ID。
func newMessageID(fromAddr string) string {
	var buf [16]byte
	// crypto/rand 在受支持平台上不会失败；真失败时退化成时间戳，
	// 唯一的代价是 Message-ID 可预测 —— 它不承担任何安全职责。
	if _, err := rand.Read(buf[:]); err != nil {
		return fmt.Sprintf("<t%d@%s>", time.Now().UnixNano(), domainOf(fromAddr))
	}
	return "<" + hex.EncodeToString(buf[:]) + "@" + domainOf(fromAddr) + ">"
}

func domainOf(addr string) string {
	if i := strings.LastIndexByte(addr, '@'); i >= 0 {
		return addr[i+1:]
	}
	return addr
}

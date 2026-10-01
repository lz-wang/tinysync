package notification

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"io"
	"mime"
	"net"
	"net/smtp"
	"strings"
	"time"
)

// smtpDialTimeout 是建立 SMTP 连接（TCP / TLS 握手）的超时。
const smtpDialTimeout = 10 * time.Second

// smtpClient 是对 *smtp.Client 的最小抽象，使发送路径可以用假客户端
// 测试 auth / STARTTLS / 收件顺序，不让单元测试访问外网。
type smtpClient interface {
	Extension(ext string) (bool, string)
	StartTLS(config *tls.Config) error
	Auth(a smtp.Auth) error
	Mail(from string) error
	Rcpt(to string) error
	Data() (io.WriteCloser, error)
	Quit() error
	Close() error
}

// smtpDialer 建立到 SMTP 服务器的客户端连接。Security 决定传输层：
// none / starttls 先明文拨号（STARTTLS 的升级在发送路径调用，可观测），
// tls 直接 TLS 拨号（465 隐式 TLS）。测试注入假实现验证选择逻辑。
type smtpDialer interface {
	Dial(ctx context.Context, host string, port int, security Security) (smtpClient, error)
}

// realDialer 是生产拨号实现。
type realDialer struct{}

// Dial 实现 smtpDialer：按 Security 选路（ADR 0006：none → net.Dial，
// starttls → net.Dial + 发送路径升级，tls → tls.Dial）。
func (realDialer) Dial(ctx context.Context, host string, port int, security Security) (smtpClient, error) {
	addr := net.JoinHostPort(host, fmt.Sprintf("%d", port))
	dialer := &net.Dialer{Timeout: smtpDialTimeout}
	switch security {
	case SecurityTLS:
		conn, err := (&tls.Dialer{
			NetDialer: dialer,
			Config:    &tls.Config{ServerName: host},
		}).DialContext(ctx, "tcp", addr)
		if err != nil {
			return nil, fmt.Errorf("dial smtp over tls: %w", err)
		}
		client, err := smtp.NewClient(conn, host)
		if err != nil {
			_ = conn.Close()
			return nil, fmt.Errorf("smtp client over tls: %w", err)
		}
		return client, nil
	default:
		// none 与 starttls 共用明文拨号；是否升级由发送路径按 Security
		// 显式调用 StartTLS。
		conn, err := dialer.DialContext(ctx, "tcp", addr)
		if err != nil {
			return nil, fmt.Errorf("dial smtp: %w", err)
		}
		client, err := smtp.NewClient(conn, host)
		if err != nil {
			_ = conn.Close()
			return nil, fmt.Errorf("smtp client: %w", err)
		}
		return client, nil
	}
}

// EmailSender 经 SMTP 发送纯文本通知（text/plain; charset=UTF-8）。
// 第一版不引入 HTML 模板体系。Password 只进入 AUTH 请求，绝不进入
// 错误消息或日志。
type EmailSender struct {
	Settings EmailSettings
	// Dialer 注入连接建立；nil 为生产拨号。
	Dialer smtpDialer
}

// Send 实现 Sender：拨号 →（starttls 模式）升级 →（配置了凭据）AUTH
// → MAIL / RCPT / DATA → Quit。连接异常路径统一 Close，重试交给下一轮
// 运行后的新通知，不在发送器内部重试。
func (s *EmailSender) Send(ctx context.Context, message Message) error {
	e := s.Settings
	dialer := s.Dialer
	if dialer == nil {
		dialer = realDialer{}
	}
	client, err := dialer.Dial(ctx, e.Host, e.Port, e.Security)
	if err != nil {
		return err
	}
	defer func() { _ = client.Close() }()

	if e.Security == SecurityStartTLS {
		if err := client.StartTLS(&tls.Config{ServerName: e.Host}); err != nil {
			return fmt.Errorf("smtp starttls: %w", err)
		}
	}
	if e.Username != "" {
		// net/smtp 的 PlainAuth 自带防护：非 TLS 且非 localhost 拒绝
		// 明文发送凭据；security=none + username 的组合在此自然报错。
		if err := client.Auth(smtp.PlainAuth("", e.Username, e.Password, e.Host)); err != nil {
			return fmt.Errorf("smtp auth: %w", err)
		}
	}
	if err := client.Mail(e.From); err != nil {
		return fmt.Errorf("smtp mail from: %w", err)
	}
	for _, recipient := range e.To {
		if err := client.Rcpt(recipient); err != nil {
			return fmt.Errorf("smtp rcpt to: %w", err)
		}
	}
	writer, err := client.Data()
	if err != nil {
		return fmt.Errorf("smtp data: %w", err)
	}
	if _, err := writer.Write(buildMail(e.From, e.To, message)); err != nil {
		_ = writer.Close()
		return fmt.Errorf("write mail body: %w", err)
	}
	if err := writer.Close(); err != nil {
		return fmt.Errorf("close mail body: %w", err)
	}
	return client.Quit()
}

// buildMail 构造 RFC 5322 邮件：中文标题经 Q 编码，正文以 UTF-8
// base64 传输，保证 8-bit 内容不依赖服务器支持 8BITMIME。
func buildMail(from string, to []string, message Message) []byte {
	var b strings.Builder
	fmt.Fprintf(&b, "From: %s\r\n", from)
	fmt.Fprintf(&b, "To: %s\r\n", strings.Join(to, ", "))
	fmt.Fprintf(&b, "Subject: %s\r\n", mime.QEncoding.Encode("UTF-8", message.Title))
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/plain; charset=UTF-8\r\n")
	b.WriteString("Content-Transfer-Encoding: base64\r\n")
	b.WriteString("\r\n")
	// 正文整体 base64 后按 ≤76 字符折行，避免单行超长被中继截断。
	encoded := base64.StdEncoding.EncodeToString([]byte(message.Body))
	for len(encoded) > 0 {
		line := min(76, len(encoded))
		b.WriteString(encoded[:line])
		b.WriteString("\r\n")
		encoded = encoded[line:]
	}
	return []byte(b.String())
}

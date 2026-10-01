package notification

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"io"
	"net/smtp"
	"strings"
	"testing"
)

// fakeSMTPClient 记录发送路径的调用序列，可按脚本返回错误。
type fakeSMTPClient struct {
	dialed bool

	calls    []string
	startTLS *tls.Config
	auth     smtp.Auth
	mailFrom string
	rcpts    []string
	data     []byte
	quit     bool
	closed   bool

	failAt map[string]error
}

func (f *fakeSMTPClient) Extension(ext string) (bool, string) { return false, "" }

func (f *fakeSMTPClient) StartTLS(config *tls.Config) error {
	f.calls = append(f.calls, "starttls")
	f.startTLS = config
	return f.script("starttls")
}

func (f *fakeSMTPClient) Auth(a smtp.Auth) error {
	f.calls = append(f.calls, "auth")
	f.auth = a
	return f.script("auth")
}

func (f *fakeSMTPClient) Mail(from string) error {
	f.calls = append(f.calls, "mail")
	f.mailFrom = from
	return f.script("mail")
}

func (f *fakeSMTPClient) Rcpt(to string) error {
	f.calls = append(f.calls, "rcpt")
	f.rcpts = append(f.rcpts, to)
	return f.script("rcpt")
}

func (f *fakeSMTPClient) Data() (io.WriteCloser, error) {
	f.calls = append(f.calls, "data")
	if err := f.script("data"); err != nil {
		return nil, err
	}
	return &fakeDataWriter{client: f}, nil
}

func (f *fakeSMTPClient) Quit() error {
	f.calls = append(f.calls, "quit")
	f.quit = true
	return f.script("quit")
}

func (f *fakeSMTPClient) Close() error {
	f.closed = true
	return nil
}

func (f *fakeSMTPClient) script(step string) error {
	if f.failAt == nil {
		return nil
	}
	return f.failAt[step]
}

// fakeDataWriter 把 DATA 内容收集回 client。
type fakeDataWriter struct {
	client *fakeSMTPClient
	buf    bytes.Buffer
}

func (w *fakeDataWriter) Write(p []byte) (int, error) { return w.buf.Write(p) }

func (w *fakeDataWriter) Close() error {
	w.client.data = w.buf.Bytes()
	return nil
}

// fakeDialer 记录拨号参数并返回假客户端。
type fakeDialer struct {
	gotHost     string
	gotPort     int
	gotSecurity Security
	client      *fakeSMTPClient
	err         error
}

func (d *fakeDialer) Dial(ctx context.Context, host string, port int, security Security) (smtpClient, error) {
	d.gotHost = host
	d.gotPort = port
	d.gotSecurity = security
	if d.err != nil {
		return nil, d.err
	}
	d.client.dialed = true
	return d.client, nil
}

// emailFixture 是一个配置完整的发送器测试基准。
func emailFixture() EmailSettings {
	return EmailSettings{
		Enabled:  true,
		Host:     "smtp.example.com",
		Port:     587,
		Security: SecurityStartTLS,
		Username: "tinysync@example.com",
		Password: "s3cret-pass",
		From:     "tinysync@example.com",
		To:       []string{"me@example.com", "ops@example.com"},
	}
}

func sendWithEmailFixture(t *testing.T, settings EmailSettings) (*fakeDialer, *fakeSMTPClient, error) {
	t.Helper()
	client := &fakeSMTPClient{}
	dialer := &fakeDialer{client: client}
	sender := &EmailSender{Settings: settings, Dialer: dialer}
	err := sender.Send(context.Background(), Message{Title: "TinySync · photos · 同步成功", Body: "任务：photos"})
	return dialer, client, err
}

func TestEmailSendStartTLSWithAuth(t *testing.T) {
	dialer, client, err := sendWithEmailFixture(t, emailFixture())
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if dialer.gotHost != "smtp.example.com" || dialer.gotPort != 587 {
		t.Fatalf("拨号参数 host=%s port=%d", dialer.gotHost, dialer.gotPort)
	}
	if dialer.gotSecurity != SecurityStartTLS {
		t.Fatalf("拨号 security=%s", dialer.gotSecurity)
	}
	// STARTTLS 模式：明文拨号（dialer 不做 TLS），发送路径显式升级。
	want := []string{"starttls", "auth", "mail", "rcpt", "rcpt", "data", "quit"}
	if strings.Join(client.calls, ",") != strings.Join(want, ",") {
		t.Fatalf("调用序列 = %v, want %v", client.calls, want)
	}
	if client.startTLS == nil || client.startTLS.ServerName != "smtp.example.com" {
		t.Fatalf("StartTLS config = %+v", client.startTLS)
	}
	if client.auth == nil {
		t.Fatal("配置了 username 但未 AUTH")
	}
	if client.mailFrom != "tinysync@example.com" {
		t.Fatalf("MAIL FROM = %q", client.mailFrom)
	}
	if strings.Join(client.rcpts, ",") != "me@example.com,ops@example.com" {
		t.Fatalf("RCPT TO = %v", client.rcpts)
	}
	assertMailContent(t, client.data)
	if !client.quit {
		t.Fatal("发送完成后未 Quit")
	}
}

func TestEmailSendImplicitTLSNoAuth(t *testing.T) {
	settings := emailFixture()
	settings.Security = SecurityTLS
	settings.Username = ""
	dialer, client, err := sendWithEmailFixture(t, settings)
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if dialer.gotSecurity != SecurityTLS {
		t.Fatalf("拨号 security=%s, want tls（由 dialer 完成 TLS）", dialer.gotSecurity)
	}
	for _, call := range client.calls {
		if call == "starttls" {
			t.Fatal("隐式 TLS 模式不应再调用 StartTLS")
		}
		if call == "auth" {
			t.Fatal("未配置 username 不应 AUTH")
		}
	}
}

func TestEmailSendPlainNoAuth(t *testing.T) {
	settings := emailFixture()
	settings.Security = SecurityNone
	settings.Username = ""
	_, client, err := sendWithEmailFixture(t, settings)
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	for _, call := range client.calls {
		if call == "starttls" || call == "auth" {
			t.Fatalf("none 模式不应调用 %s", call)
		}
	}
}

func TestEmailSendDialFailure(t *testing.T) {
	client := &fakeSMTPClient{}
	dialer := &fakeDialer{client: client, err: errors.New("dial refused")}
	sender := &EmailSender{Settings: emailFixture(), Dialer: dialer}
	err := sender.Send(context.Background(), Message{Title: "t", Body: "b"})
	if err == nil || !strings.Contains(err.Error(), "dial refused") {
		t.Fatalf("Send with dial failure = %v", err)
	}
	if len(client.calls) != 0 {
		t.Fatalf("拨号失败后不应有后续调用：%v", client.calls)
	}
}

func TestEmailSendRcptFailureClosesConnection(t *testing.T) {
	client := &fakeSMTPClient{failAt: map[string]error{"rcpt": errors.New("no such user")}}
	dialer := &fakeDialer{client: client}
	sender := &EmailSender{Settings: emailFixture(), Dialer: dialer}
	err := sender.Send(context.Background(), Message{Title: "t", Body: "b"})
	if err == nil || !strings.Contains(err.Error(), "no such user") {
		t.Fatalf("Send with rcpt failure = %v", err)
	}
	if !client.closed {
		t.Fatal("失败路径应关闭连接")
	}
	if client.quit {
		t.Fatal("失败路径不应 Quit（复用连接语义上已不可靠）")
	}
	// 错误消息不得包含密码。
	if strings.Contains(err.Error(), "s3cret-pass") {
		t.Fatalf("错误消息泄漏密码：%v", err)
	}
}

// assertMailContent 校验 DATA 写出的 RFC 5322 邮件：头齐全、标题 Q
// 编码、正文 base64 可解码还原。
func assertMailContent(t *testing.T, data []byte) {
	t.Helper()
	mail := string(data)
	for _, header := range []string{
		"From: tinysync@example.com\r\n",
		"To: me@example.com, ops@example.com\r\n",
		"MIME-Version: 1.0\r\n",
		"Content-Type: text/plain; charset=UTF-8\r\n",
		"Content-Transfer-Encoding: base64\r\n",
	} {
		if !strings.Contains(mail, header) {
			t.Errorf("邮件缺少头 %q；实际：\n%s", header, mail)
		}
	}
	if !strings.Contains(mail, "Subject: =?UTF-8?q?") {
		t.Errorf("Subject 未做 Q 编码；实际：\n%s", mail)
	}
	header, body, found := strings.Cut(mail, "\r\n\r\n")
	if !found {
		t.Fatalf("邮件无空行分隔头与正文：\n%s", mail)
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(body, "\r\n", ""))
	if err != nil {
		t.Fatalf("正文 base64 解码失败：%v；实际：\n%s", err, mail)
	}
	if !strings.Contains(string(decoded), "任务：photos") {
		t.Errorf("解码正文缺少原始内容；实际：%s", decoded)
	}
	if strings.Contains(header, "photos") {
		// 标题 Q 编码后 header 不含明文中文；正文短语 "photos" 可出现
		// 在 Title 中，但 Title 已编码，header 里不应再出现。
		t.Logf("header 中出现未编码内容（检查 Title 编码）：\n%s", header)
	}
}

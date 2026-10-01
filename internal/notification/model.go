// Package notification 承载运行完成通知领域：配置持久化（本文件与
// repository / service）、消息格式化与 Pushover / SMTP 发送（formatter /
// pushover / email），以及消费 syncjob.RunCompletion 的旁路分发
// （dispatcher）。通知是同步运行的辅助提醒：任务历史才是权威事实，
// 任何发送失败只记结构化日志，绝不影响 run 终态（ADR 0006）。
package notification

import (
	"net/mail"
	"strings"
	"time"
)

// Security 是 SMTP 连接安全模式：none 明文、starttls 显式升级
// （587 常见）、tls 隐式 TLS（465 常见）。
type Security string

// 支持的安全模式枚举。
const (
	SecurityNone     Security = "none"
	SecurityStartTLS Security = "starttls"
	SecurityTLS      Security = "tls"
)

// Valid 判断安全模式是否为受支持的枚举值。
func (s Security) Valid() bool {
	switch s {
	case SecurityNone, SecurityStartTLS, SecurityTLS:
		return true
	}
	return false
}

// PushoverSettings 是 Pushover 渠道配置。Token 与 UserKey 是 secret：
// 仅在保存路径与发送器构造时流转，API 只回显 configured 布尔。
type PushoverSettings struct {
	Enabled bool
	Token   string
	UserKey string
}

// EmailSettings 是 SMTP 邮件渠道配置。Password 是 secret；To 是收件
// 地址列表（存储层序列化为逗号分隔）。
type EmailSettings struct {
	Enabled  bool
	Host     string
	Port     int
	Security Security
	Username string
	Password string
	From     string
	To       []string
}

// Settings 是通知配置的完整快照，对应 notification_settings 的
// singleton 行。加载后由 Update 以三态语义部分修改。
type Settings struct {
	Pushover  PushoverSettings
	Email     EmailSettings
	UpdatedAt time.Time
}

// DefaultEmailPort / DefaultEmailSecurity 与 schema 默认值一致，
// PATCH 未提供时新配置以此起步。
const (
	DefaultEmailPort     = 587
	DefaultEmailSecurity = SecurityStartTLS
)

// 文本字段长度上限（按字节计）：家庭 SMTP / Pushover 配置远小于此，
// 上限只为拦截异常输入，不构成业务约束。
const maxTextFieldBytes = 1024

// PushoverConfigured 报告 Pushover 渠道是否具备启用所需的完整配置。
func (s Settings) PushoverConfigured() bool {
	return s.Pushover.Token != "" && s.Pushover.UserKey != ""
}

// EmailConfigured 报告邮件渠道是否具备启用所需的完整配置。
func (s Settings) EmailConfigured() bool {
	return s.Email.Host != "" && s.Email.From != "" && len(s.Email.To) > 0
}

// Validate 校验配置整体一致性：字段枚举、端口范围、地址格式与
// 「启用即完整」约束。校验失败的配置不落库。
func (s Settings) Validate() error {
	p := s.Pushover
	if err := checkTextField("pushover token", p.Token); err != nil {
		return err
	}
	if err := checkTextField("pushover user key", p.UserKey); err != nil {
		return err
	}
	if p.Enabled && !s.PushoverConfigured() {
		return &InvalidError{Field: "pushover", Reason: "token and user key are required when enabled"}
	}

	e := s.Email
	for field, value := range map[string]string{
		"email host":     e.Host,
		"email username": e.Username,
		"email password": e.Password,
		"email from":     e.From,
	} {
		if err := checkTextField(field, value); err != nil {
			return err
		}
	}
	if e.Port < 1 || e.Port > 65535 {
		return &InvalidError{Field: "email.port", Reason: "port must be between 1 and 65535"}
	}
	if !e.Security.Valid() {
		return &InvalidError{Field: "email.security", Reason: "security must be none, starttls or tls"}
	}
	if e.From != "" {
		if err := validateMailbox("email.from", e.From); err != nil {
			return err
		}
	}
	if len(e.To) > 16 {
		return &InvalidError{Field: "email.to", Reason: "at most 16 recipients"}
	}
	for _, addr := range e.To {
		if strings.TrimSpace(addr) == "" {
			return &InvalidError{Field: "email.to", Reason: "recipient is empty"}
		}
		if err := validateMailbox("email.to", addr); err != nil {
			return err
		}
	}
	if e.Enabled && !s.EmailConfigured() {
		return &InvalidError{Field: "email", Reason: "host, from and at least one recipient are required when enabled"}
	}
	return nil
}

// validateMailbox 只接受纯 addr-spec（user@example.com）。mail.ParseAddress
// 同时接受带 display name 的 RFC mailbox（如 `TinySync <a@b.com>`），但
// 下游三处——SQLite 逗号序列化、SMTP MAIL FROM / RCPT TO envelope、
// WebUI 逗号拆分——都按裸地址工作；display name 会破坏三者一致性
// （逗号甚至可以合法出现在带引号的 display name 里），入口即拒绝。
func validateMailbox(field, value string) error {
	parsed, err := mail.ParseAddress(value)
	if err != nil || parsed.Address != strings.TrimSpace(value) {
		return &InvalidError{Field: field, Reason: "must be a plain email address (user@example.com)"}
	}
	return nil
}

// checkTextField 校验可选文本字段的长度上限；空串合法（字段可清空）。
func checkTextField(field, value string) error {
	if len(value) > maxTextFieldBytes {
		return &InvalidError{Field: field, Reason: "value exceeds length limit"}
	}
	return nil
}

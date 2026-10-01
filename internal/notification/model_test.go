package notification

import (
	"errors"
	"strings"
	"testing"
)

// validSettings 是通过校验的基准配置，子测试按需改写单个字段。
func validSettings() Settings {
	return Settings{
		Pushover: PushoverSettings{
			Enabled: true,
			Token:   "token-1",
			UserKey: "userkey-1",
		},
		Email: EmailSettings{
			Enabled:  true,
			Host:     "smtp.example.com",
			Port:     587,
			Security: SecurityStartTLS,
			From:     "tinysync@example.com",
			To:       []string{"me@example.com"},
		},
	}
}

// invalidField 从校验错误中取出字段定位。
func invalidField(t *testing.T, err error) string {
	t.Helper()
	var invalid *InvalidError
	if !errors.As(err, &invalid) {
		t.Fatalf("Validate error = %v, want *InvalidError", err)
	}
	return invalid.Field
}

func TestSettingsValidate(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*Settings)
		wantErr bool
		field   string
	}{
		{
			name:    "valid",
			mutate:  func(s *Settings) {},
			wantErr: false,
		},
		{
			name: "pushover enabled without token",
			mutate: func(s *Settings) {
				s.Pushover.Token = ""
			},
			wantErr: true,
			field:   "pushover",
		},
		{
			name: "pushover token cleared while disabled",
			mutate: func(s *Settings) {
				s.Pushover.Enabled = false
				s.Pushover.Token = ""
				s.Pushover.UserKey = ""
			},
			wantErr: false,
		},
		{
			name: "email port zero",
			mutate: func(s *Settings) {
				s.Email.Port = 0
			},
			wantErr: true,
			field:   "email.port",
		},
		{
			name: "email port too large",
			mutate: func(s *Settings) {
				s.Email.Port = 65536
			},
			wantErr: true,
			field:   "email.port",
		},
		{
			name: "email security unsupported",
			mutate: func(s *Settings) {
				s.Email.Security = Security("ssl")
			},
			wantErr: true,
			field:   "email.security",
		},
		{
			name: "email from invalid address",
			mutate: func(s *Settings) {
				s.Email.From = "not-an-address"
			},
			wantErr: true,
			field:   "email.from",
		},
		{
			name: "email recipient invalid address",
			mutate: func(s *Settings) {
				s.Email.To = []string{"me@example.com", "bad@@example"}
			},
			wantErr: true,
			field:   "email.to",
		},
		{
			name: "email recipient empty string",
			mutate: func(s *Settings) {
				s.Email.To = []string{"  "}
			},
			wantErr: true,
			field:   "email.to",
		},
		{
			name: "email enabled without host",
			mutate: func(s *Settings) {
				s.Email.Host = ""
			},
			wantErr: true,
			field:   "email",
		},
		{
			name: "email enabled without recipients",
			mutate: func(s *Settings) {
				s.Email.To = nil
			},
			wantErr: true,
			field:   "email",
		},
		{
			name: "email incomplete but disabled",
			mutate: func(s *Settings) {
				s.Email.Enabled = false
				s.Email.Host = ""
				s.Email.To = nil
			},
			wantErr: false,
		},
		{
			name: "field exceeds length limit",
			mutate: func(s *Settings) {
				s.Email.Host = "smtp." + strings.Repeat("a", maxTextFieldBytes) + ".example.com"
			},
			wantErr: true,
			field:   "email host",
		},
		{
			name: "empty settings with schema defaults",
			mutate: func(s *Settings) {
				*s = Settings{Email: EmailSettings{
					Port:     DefaultEmailPort,
					Security: DefaultEmailSecurity,
				}}
			},
			wantErr: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			settings := validSettings()
			tt.mutate(&settings)
			err := settings.Validate()
			if !tt.wantErr {
				if err != nil {
					t.Fatalf("Validate() = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatal("Validate() = nil, want error")
			}
			if !errors.Is(err, ErrInvalid) {
				t.Fatalf("Validate() = %v, want ErrInvalid chain", err)
			}
			if got := invalidField(t, err); got != tt.field {
				t.Fatalf("Validate() field = %q, want %q", got, tt.field)
			}
		})
	}
}

package source

import (
	"errors"
	"strings"
	"testing"
)

// ValidateType 只接受 webdav，空值与其余类型拒绝。
func TestValidateType(t *testing.T) {
	if err := ValidateType(TypeWebDAV); err != nil {
		t.Errorf("webdav = %v, want nil", err)
	}
	for _, tt := range []Type{"", "s3", "sftp", "WEBDAV", "file"} {
		if err := ValidateType(tt); err == nil {
			t.Errorf("type %q = nil, want error", tt)
		} else if !errors.Is(err, ErrInvalid) {
			t.Errorf("type %q error = %v, want ErrInvalid", tt, err)
		}
	}
}

// ValidateName 拒绝空名与超长名。
func TestValidateName(t *testing.T) {
	if err := ValidateName("NAS"); err != nil {
		t.Errorf("NAS = %v, want nil", err)
	}
	if err := ValidateName(""); err == nil {
		t.Error("empty name = nil, want error")
	}
	long := strings.Repeat("长", maxNameLength+1)
	if err := ValidateName(long); err == nil {
		t.Error("over-length name = nil, want error")
	}
	if err := ValidateName(strings.Repeat("a", maxNameLength)); err != nil {
		t.Errorf("max-length name = %v, want nil", err)
	}
}

// ValidateEndpoint 按安全边界校验 endpoint。
func TestValidateEndpoint(t *testing.T) {
	valid := []string{
		"http://example.com",
		"https://example.com",
		"https://example.com/dav/",
		"https://nas.local:5006/home",
		"http://192.168.1.10:8080",
		"https://example.com/path?query=1",
	}
	for _, endpoint := range valid {
		if err := ValidateEndpoint(endpoint); err != nil {
			t.Errorf("ValidateEndpoint(%q) = %v, want nil", endpoint, err)
		}
	}

	invalid := []struct {
		endpoint string
		reason   string
	}{
		{"", "empty"},
		{"://example.com", "unparseable"},
		{"ftp://example.com", "scheme"},
		{"example.com/dav", "scheme missing"},
		{"https://", "host missing"},
		{"https://user:pass@example.com/", "embedded credentials"},
		{"https://user@example.com/", "embedded username"},
		{"https://example.com/#frag", "fragment"},
	}
	for _, tt := range invalid {
		err := ValidateEndpoint(tt.endpoint)
		if err == nil {
			t.Errorf("ValidateEndpoint(%q) = nil, want error (%s)", tt.endpoint, tt.reason)
			continue
		}
		if !errors.Is(err, ErrInvalid) {
			t.Errorf("ValidateEndpoint(%q) error = %v, want ErrInvalid", tt.endpoint, err)
		}
	}
}

// ValidateCreateInput 组合校验，任一字段非法均拒绝。
func TestValidateCreateInput(t *testing.T) {
	base := CreateInput{
		Name:     "NAS",
		Type:     TypeWebDAV,
		Endpoint: "https://example.com/dav",
	}

	if err := ValidateCreateInput(base); err != nil {
		t.Fatalf("valid input = %v, want nil", err)
	}

	badName := base
	badName.Name = " "
	if err := ValidateCreateInput(badName); err == nil {
		t.Error("blank name = nil, want error")
	}

	badType := base
	badType.Type = "s3"
	if err := ValidateCreateInput(badType); err == nil {
		t.Error("unsupported type = nil, want error")
	}

	badEndpoint := base
	badEndpoint.Endpoint = "https://user:pass@example.com/"
	if err := ValidateCreateInput(badEndpoint); err == nil {
		t.Error("endpoint with credentials = nil, want error")
	}
}

package source

import (
	"errors"
	"strings"
	"testing"
)

// TestValidateType 接受三种协议类型，空值与其余类型拒绝。
func TestValidateType(t *testing.T) {
	for _, valid := range []Type{TypeWebDAV, TypeS3, TypeSFTP} {
		if err := ValidateType(valid); err != nil {
			t.Errorf("type %q = %v, want nil", valid, err)
		}
	}
	for _, tt := range []Type{"", "WEBDAV", "file", "s3x"} {
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

// validS3Config 返回合法 S3 配置。
func validS3Config() S3Config {
	return S3Config{
		Endpoint:  "https://s3.example.com",
		Region:    "us-east-1",
		Bucket:    "backup",
		Prefix:    "tinysync",
		PathStyle: true,
		AccessKey: "AKID",
	}
}

// validSFTPConfig 返回合法 SFTP 配置。
func validSFTPConfig() SFTPConfig {
	return SFTPConfig{
		Host:               "nas.example.com",
		Port:               22,
		Username:           "user",
		RemoteRoot:         "/srv/backups",
		AuthMethod:         SFTPAuthPassword,
		HostKeyFingerprint: "SHA256:UC1Dk4I9LLQOV3B8eZ5FlrUUcbbNie4INffe2TDTz3k",
	}
}

// TestValidateConfig 按 Type 强制 config 单选一致性与协议字段约束。
func TestValidateConfig(t *testing.T) {
	valid := []struct {
		name   string
		typ    Type
		config Config
	}{
		{"webdav", TypeWebDAV, Config{WebDAV: &WebDAVConfig{Endpoint: "https://example.com/dav"}}},
		{"s3 aws default endpoint", TypeS3, Config{S3: func() *S3Config {
			c := validS3Config()
			c.Endpoint = ""
			return &c
		}()}},
		{"s3 explicit endpoint", TypeS3, Config{S3: ptrS3(validS3Config())}},
		{"sftp", TypeSFTP, Config{SFTP: ptrSFTP(validSFTPConfig())}},
		{"sftp without host key verification", TypeSFTP, Config{SFTP: func() *SFTPConfig {
			c := validSFTPConfig()
			c.HostKeyFingerprint = ""
			return &c
		}()}},
		{"sftp default port", TypeSFTP, Config{SFTP: func() *SFTPConfig {
			c := validSFTPConfig()
			c.Port = 0
			return &c
		}()}},
		{"sftp private key", TypeSFTP, Config{SFTP: func() *SFTPConfig {
			c := validSFTPConfig()
			c.AuthMethod = SFTPAuthPrivateKey
			return &c
		}()}},
	}
	for _, tt := range valid {
		if err := ValidateConfig(tt.typ, tt.config); err != nil {
			t.Errorf("%s = %v, want nil", tt.name, err)
		}
	}

	invalid := []struct {
		name   string
		typ    Type
		config Config
	}{
		{"missing config", TypeWebDAV, Config{}},
		{"type mismatch s3 with webdav config", TypeS3, Config{WebDAV: &WebDAVConfig{Endpoint: "https://x/"}}},
		{"type mismatch sftp with s3 config", TypeSFTP, Config{S3: ptrS3(validS3Config())}},
		{"extra group", TypeWebDAV, Config{
			WebDAV: &WebDAVConfig{Endpoint: "https://x/"},
			S3:     ptrS3(validS3Config()),
		}},
		{"s3 missing region", TypeS3, Config{S3: func() *S3Config {
			c := validS3Config()
			c.Region = ""
			return &c
		}()}},
		{"s3 missing bucket", TypeS3, Config{S3: func() *S3Config {
			c := validS3Config()
			c.Bucket = " "
			return &c
		}()}},
		{"s3 missing access key", TypeS3, Config{S3: func() *S3Config {
			c := validS3Config()
			c.AccessKey = ""
			return &c
		}()}},
		{"s3 bad endpoint scheme", TypeS3, Config{S3: func() *S3Config {
			c := validS3Config()
			c.Endpoint = "ftp://s3.example.com"
			return &c
		}()}},
		{"s3 absolute prefix", TypeS3, Config{S3: func() *S3Config {
			c := validS3Config()
			c.Prefix = "/tinysync"
			return &c
		}()}},
		{"sftp missing host", TypeSFTP, Config{SFTP: func() *SFTPConfig {
			c := validSFTPConfig()
			c.Host = ""
			return &c
		}()}},
		{"sftp port out of range", TypeSFTP, Config{SFTP: func() *SFTPConfig {
			c := validSFTPConfig()
			c.Port = 70000
			return &c
		}()}},
		{"sftp relative remote root", TypeSFTP, Config{SFTP: func() *SFTPConfig {
			c := validSFTPConfig()
			c.RemoteRoot = "srv/backups"
			return &c
		}()}},
		{"sftp unclean remote root", TypeSFTP, Config{SFTP: func() *SFTPConfig {
			c := validSFTPConfig()
			c.RemoteRoot = "/srv//backups"
			return &c
		}()}},
		{"sftp missing auth method", TypeSFTP, Config{SFTP: func() *SFTPConfig {
			c := validSFTPConfig()
			c.AuthMethod = ""
			return &c
		}()}},
		{"sftp unknown auth method", TypeSFTP, Config{SFTP: func() *SFTPConfig {
			c := validSFTPConfig()
			c.AuthMethod = SFTPAuthMethod("kerberos")
			return &c
		}()}},
		{"sftp fingerprint without prefix", TypeSFTP, Config{SFTP: func() *SFTPConfig {
			c := validSFTPConfig()
			c.HostKeyFingerprint = "UC1Dk4I9LLQOV3B8eZ5FlrUUcbbNie4INffe2TDTz3k"
			return &c
		}()}},
		{"sftp fingerprint not base64", TypeSFTP, Config{SFTP: func() *SFTPConfig {
			c := validSFTPConfig()
			c.HostKeyFingerprint = "SHA256:not!!base64##"
			return &c
		}()}},
		{"sftp fingerprint with padding", TypeSFTP, Config{SFTP: func() *SFTPConfig {
			c := validSFTPConfig()
			c.HostKeyFingerprint = "SHA256:UC1Dk4I9LLQOV3B8eZ5FlrUUcbbNie4INffe2TDTz3k="
			return &c
		}()}},
	}
	for _, tt := range invalid {
		err := ValidateConfig(tt.typ, tt.config)
		if err == nil {
			t.Errorf("%s = nil, want error", tt.name)
			continue
		}
		if !errors.Is(err, ErrInvalid) {
			t.Errorf("%s error = %v, want ErrInvalid", tt.name, err)
		}
	}
}

// TestValidateCredentials 校验凭据与 Type / auth_method 的匹配。
func TestValidateCredentials(t *testing.T) {
	valid := []struct {
		name   string
		typ    Type
		config Config
		creds  Credentials
	}{
		{"webdav anonymous", TypeWebDAV,
			Config{WebDAV: &WebDAVConfig{Endpoint: "https://x/"}}, Credentials{}},
		{"webdav password", TypeWebDAV,
			Config{WebDAV: &WebDAVConfig{Endpoint: "https://x/"}},
			Credentials{WebDAV: &WebDAVCredentials{Password: "secret"}}},
		{"s3", TypeS3, Config{S3: ptrS3(validS3Config())},
			Credentials{S3: &S3Credentials{SecretKey: "shhh"}}},
		{"sftp password", TypeSFTP, Config{SFTP: ptrSFTP(validSFTPConfig())},
			Credentials{SFTP: &SFTPCredentials{Password: "secret"}}},
		{"sftp private key", TypeSFTP,
			Config{SFTP: func() *SFTPConfig {
				c := validSFTPConfig()
				c.AuthMethod = SFTPAuthPrivateKey
				return &c
			}()},
			Credentials{SFTP: &SFTPCredentials{PrivateKey: "-----BEGIN", PrivateKeyPassphrase: "pp"}}},
	}
	for _, tt := range valid {
		if err := ValidateCredentials(tt.typ, tt.config, tt.creds); err != nil {
			t.Errorf("%s = %v, want nil", tt.name, err)
		}
	}

	invalid := []struct {
		name   string
		typ    Type
		config Config
		creds  Credentials
	}{
		{"s3 missing secret key", TypeS3, Config{S3: ptrS3(validS3Config())}, Credentials{}},
		{"s3 creds in wrong group", TypeS3, Config{S3: ptrS3(validS3Config())},
			Credentials{WebDAV: &WebDAVCredentials{Password: "x"}}},
		{"sftp password auth without password", TypeSFTP, Config{SFTP: ptrSFTP(validSFTPConfig())},
			Credentials{SFTP: &SFTPCredentials{}}},
		{"sftp private key auth without key", TypeSFTP,
			Config{SFTP: func() *SFTPConfig {
				c := validSFTPConfig()
				c.AuthMethod = SFTPAuthPrivateKey
				return &c
			}()},
			Credentials{SFTP: &SFTPCredentials{Password: "x"}}},
		{"sftp creds in wrong group", TypeSFTP, Config{SFTP: ptrSFTP(validSFTPConfig())},
			Credentials{S3: &S3Credentials{SecretKey: "x"}}},
	}
	for _, tt := range invalid {
		err := ValidateCredentials(tt.typ, tt.config, tt.creds)
		if err == nil {
			t.Errorf("%s = nil, want error", tt.name)
			continue
		}
		if !errors.Is(err, ErrInvalid) {
			t.Errorf("%s error = %v, want ErrInvalid", tt.name, err)
		}
	}
}

// TestValidateCredentialsUpdate 校验更新组的类型匹配。
func TestValidateCredentialsUpdate(t *testing.T) {
	pw := "new"
	if err := ValidateCredentialsUpdate(TypeWebDAV, &CredentialsUpdate{
		WebDAV: &WebDAVCredentialsUpdate{Password: &pw},
	}); err != nil {
		t.Errorf("webdav update = %v, want nil", err)
	}
	if err := ValidateCredentialsUpdate(TypeWebDAV, &CredentialsUpdate{
		S3: &S3CredentialsUpdate{},
	}); !errors.Is(err, ErrInvalid) {
		t.Errorf("webdav with s3 group = %v, want ErrInvalid", err)
	}
	if err := ValidateCredentialsUpdate(TypeSFTP, &CredentialsUpdate{
		SFTP: &SFTPCredentialsUpdate{},
	}); err != nil {
		t.Errorf("sftp empty group = %v, want nil", err)
	}
}

// TestCredentialStateOf 校验凭据状态推导。
func TestCredentialStateOf(t *testing.T) {
	if st := CredentialStateOf(TypeWebDAV, Credentials{}); st.WebDAV.PasswordSet {
		t.Error("anonymous webdav = PasswordSet, want false")
	}
	if st := CredentialStateOf(TypeS3, Credentials{S3: &S3Credentials{SecretKey: "x"}}); !st.S3.SecretKeySet {
		t.Error("s3 with secret = not set, want true")
	}
	st := CredentialStateOf(TypeSFTP, Credentials{SFTP: &SFTPCredentials{PrivateKey: "k", PrivateKeyPassphrase: "p"}})
	if st.SFTP == nil || st.SFTP.PasswordSet || !st.SFTP.PrivateKeySet || !st.SFTP.PrivateKeyPassphraseSet {
		t.Errorf("sftp state = %+v, want private key set", st.SFTP)
	}
}

// TestValidateCreateInput 组合校验，任一字段非法均拒绝。
func TestValidateCreateInput(t *testing.T) {
	base := CreateInput{
		Name:   "NAS",
		Type:   TypeWebDAV,
		Config: Config{WebDAV: &WebDAVConfig{Endpoint: "https://example.com/dav"}},
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
	badType.Type = "file"
	if err := ValidateCreateInput(badType); err == nil {
		t.Error("unsupported type = nil, want error")
	}

	badEndpoint := base
	badEndpoint.Config.WebDAV.Endpoint = "https://user:pass@example.com/"
	if err := ValidateCreateInput(badEndpoint); err == nil {
		t.Error("endpoint with credentials = nil, want error")
	}

	badCreds := base
	badCreds.Type = TypeS3
	badCreds.Config = Config{S3: ptrS3(validS3Config())}
	badCreds.Credentials = Credentials{}
	if err := ValidateCreateInput(badCreds); err == nil {
		t.Error("s3 without secret key = nil, want error")
	}
}

// TestValidateLogicalPath 校验跨协议 logical path 规则：绝对、clean、
// 无反斜杠 / NUL / 尾随分隔符；反斜杠必须拒绝以保证跨平台本地映射
// 语义一致。
func TestValidateLogicalPath(t *testing.T) {
	valid := []string{"/", "/a", "/a/b.txt", "/docs/my file 中文.txt"}
	for _, p := range valid {
		if err := ValidateLogicalPath(p); err != nil {
			t.Errorf("ValidateLogicalPath(%q) = %v, want nil", p, err)
		}
	}

	invalid := []struct {
		path   string
		reason string
	}{
		{"", "empty"},
		{"relative/path", "relative"},
		{"a", "relative"},
		{"/a/../b", "dot-dot segment"},
		{"/a/./b", "dot segment"},
		{"/a//b", "duplicate separator"},
		{"/a/", "trailing separator"},
		{`/a\b`, "backslash"},
		{"a\\b", "relative with backslash"},
		{"/a\x00b", "NUL"},
		{"/a/b/", "trailing separator on nested"},
	}
	for _, tt := range invalid {
		err := ValidateLogicalPath(tt.path)
		if err == nil {
			t.Errorf("ValidateLogicalPath(%q) = nil, want error (%s)", tt.path, tt.reason)
			continue
		}
		if !errors.Is(err, ErrInvalid) {
			t.Errorf("ValidateLogicalPath(%q) error = %v, want ErrInvalid", tt.path, err)
		}
	}
}

func ptrS3(c S3Config) *S3Config { return &c }

func ptrSFTP(c SFTPConfig) *SFTPConfig { return &c }

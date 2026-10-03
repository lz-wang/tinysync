package source

import (
	"errors"
	"strings"
	"testing"
)

// TestValidateType 接受已支持的协议类型，空值与其余类型拒绝。
func TestValidateType(t *testing.T) {
	for _, valid := range []Type{TypeWebDAV, TypeS3, TypeSFTP, TypeSMB, TypeGitHubRelease} {
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

// validSMBConfig 返回合法 SMB 配置。
func validSMBConfig() SMBConfig {
	return SMBConfig{
		Host:       "nas.example.com",
		Port:       445,
		Share:      "backup",
		RemoteRoot: "/photos",
		Username:   "tinysync",
		Domain:     "WORKGROUP",
		Signing:    SMBSigningRequired,
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
		{"smb", TypeSMB, Config{SMB: ptrSMB(validSMBConfig())}},
		{"smb default port and empty root", TypeSMB, Config{SMB: func() *SMBConfig {
			c := validSMBConfig()
			c.Port = 0
			c.RemoteRoot = ""
			return &c
		}()}},
		{"smb hidden share", TypeSMB, Config{SMB: func() *SMBConfig {
			c := validSMBConfig()
			c.Share = "backup$"
			return &c
		}()}},
		{"smb unicode share", TypeSMB, Config{SMB: func() *SMBConfig {
			c := validSMBConfig()
			c.Share = "共享"
			return &c
		}()}},
		{"smb empty domain", TypeSMB, Config{SMB: func() *SMBConfig {
			c := validSMBConfig()
			c.Domain = ""
			return &c
		}()}},
		{"smb auto signing", TypeSMB, Config{SMB: func() *SMBConfig {
			c := validSMBConfig()
			c.Signing = SMBSigningAuto
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
		{"type mismatch smb with sftp config", TypeSMB, Config{SFTP: ptrSFTP(validSFTPConfig())}},
		{"extra smb group on webdav", TypeWebDAV, Config{
			WebDAV: &WebDAVConfig{Endpoint: "https://x/"},
			SMB:    ptrSMB(validSMBConfig()),
		}},
		{"extra group", TypeWebDAV, Config{
			WebDAV: &WebDAVConfig{Endpoint: "https://x/"},
			S3:     ptrS3(validS3Config()),
		}},
		{"s3 missing endpoint", TypeS3, Config{S3: func() *S3Config {
			c := validS3Config()
			c.Endpoint = ""
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
		{"smb missing host", TypeSMB, Config{SMB: func() *SMBConfig {
			c := validSMBConfig()
			c.Host = ""
			return &c
		}()}},
		{"smb host whitespace only", TypeSMB, Config{SMB: func() *SMBConfig {
			c := validSMBConfig()
			c.Host = "  "
			return &c
		}()}},
		{"smb host with scheme", TypeSMB, Config{SMB: func() *SMBConfig {
			c := validSMBConfig()
			c.Host = "smb://nas.example.com"
			return &c
		}()}},
		{"smb host unc form", TypeSMB, Config{SMB: func() *SMBConfig {
			c := validSMBConfig()
			c.Host = `\\nas\backup`
			return &c
		}()}},
		{"smb host with trailing path", TypeSMB, Config{SMB: func() *SMBConfig {
			c := validSMBConfig()
			c.Host = "nas.example.com/photos"
			return &c
		}()}},
		{"smb port out of range", TypeSMB, Config{SMB: func() *SMBConfig {
			c := validSMBConfig()
			c.Port = 70000
			return &c
		}()}},
		{"smb missing share", TypeSMB, Config{SMB: func() *SMBConfig {
			c := validSMBConfig()
			c.Share = ""
			return &c
		}()}},
		{"smb share with slash", TypeSMB, Config{SMB: func() *SMBConfig {
			c := validSMBConfig()
			c.Share = "backup/photos"
			return &c
		}()}},
		{"smb share with backslash", TypeSMB, Config{SMB: func() *SMBConfig {
			c := validSMBConfig()
			c.Share = `backup\photos`
			return &c
		}()}},
		{"smb share dot segment", TypeSMB, Config{SMB: func() *SMBConfig {
			c := validSMBConfig()
			c.Share = ".."
			return &c
		}()}},
		{"smb missing username", TypeSMB, Config{SMB: func() *SMBConfig {
			c := validSMBConfig()
			c.Username = ""
			return &c
		}()}},
		{"smb relative remote root", TypeSMB, Config{SMB: func() *SMBConfig {
			c := validSMBConfig()
			c.RemoteRoot = "photos"
			return &c
		}()}},
		{"smb unclean remote root", TypeSMB, Config{SMB: func() *SMBConfig {
			c := validSMBConfig()
			c.RemoteRoot = "/photos//2026"
			return &c
		}()}},
		{"smb backslash remote root", TypeSMB, Config{SMB: func() *SMBConfig {
			c := validSMBConfig()
			c.RemoteRoot = `\photos`
			return &c
		}()}},
		{"smb bad signing", TypeSMB, Config{SMB: func() *SMBConfig {
			c := validSMBConfig()
			c.Signing = SMBSigningPolicy("disabled")
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
		{"smb password", TypeSMB, Config{SMB: ptrSMB(validSMBConfig())},
			Credentials{SMB: &SMBCredentials{Password: "secret"}}},
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
		{"smb missing password", TypeSMB, Config{SMB: ptrSMB(validSMBConfig())}, Credentials{}},
		{"smb empty password", TypeSMB, Config{SMB: ptrSMB(validSMBConfig())},
			Credentials{SMB: &SMBCredentials{Password: ""}}},
		{"smb creds in wrong group", TypeSMB, Config{SMB: ptrSMB(validSMBConfig())},
			Credentials{WebDAV: &WebDAVCredentials{Password: "x"}}},
		{"webdav with smb group", TypeWebDAV, Config{WebDAV: &WebDAVConfig{Endpoint: "https://x/"}},
			Credentials{SMB: &SMBCredentials{Password: "x"}}},
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
	smbPW := "next"
	if err := ValidateCredentialsUpdate(TypeSMB, &CredentialsUpdate{
		SMB: &SMBCredentialsUpdate{Password: &smbPW},
	}); err != nil {
		t.Errorf("smb update = %v, want nil", err)
	}
	if err := ValidateCredentialsUpdate(TypeSMB, &CredentialsUpdate{
		SFTP: &SFTPCredentialsUpdate{},
	}); !errors.Is(err, ErrInvalid) {
		t.Errorf("smb with sftp group = %v, want ErrInvalid", err)
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
	if st := CredentialStateOf(TypeSMB, Credentials{}); st.SMB == nil || st.SMB.PasswordSet {
		t.Errorf("smb without password = %+v, want PasswordSet false", st.SMB)
	}
	if st := CredentialStateOf(TypeSMB, Credentials{SMB: &SMBCredentials{Password: "p"}}); st.SMB == nil || !st.SMB.PasswordSet {
		t.Errorf("smb with password = %+v, want PasswordSet true", st.SMB)
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

// strictUnionSamples 是五种协议各自的合法 config + credentials 组，
// 供严格单选全矩阵测试构造「own + foreign 混组」输入。
type strictUnionSample struct {
	name  string
	typ   Type
	cfg   Config
	creds Credentials
}

func strictUnionSamples() []strictUnionSample {
	return []strictUnionSample{
		{"local", TypeLocal, Config{Local: &LocalConfig{Root: "/local"}}, Credentials{}},
		{"webdav", TypeWebDAV,
			Config{WebDAV: &WebDAVConfig{Endpoint: "https://x/"}}, Credentials{}},
		{"s3", TypeS3, Config{S3: ptrS3(validS3Config())},
			Credentials{S3: &S3Credentials{SecretKey: "shhh"}}},
		{"sftp", TypeSFTP, Config{SFTP: ptrSFTP(validSFTPConfig())},
			Credentials{SFTP: &SFTPCredentials{Password: "secret"}}},
		{"smb", TypeSMB, Config{SMB: ptrSMB(validSMBConfig())},
			Credentials{SMB: &SMBCredentials{Password: "secret"}}},
		{"github_release", TypeGitHubRelease,
			Config{GitHubRelease: &GitHubReleaseConfig{Repository: "owner/repo"}}, Credentials{}},
	}
}

// TestValidateConfigStrictUnion 严格单选全矩阵（6×5）：任何协议的
// config 混入任何其它协议的非空组都必须拒绝。SMB 落地时
// GitHubRelease 曾漏在四个既有互斥检查之外，单选不变量因此不对称。
func TestValidateConfigStrictUnion(t *testing.T) {
	for _, own := range strictUnionSamples() {
		for _, foreign := range strictUnionSamples() {
			if own.name == foreign.name {
				continue
			}
			mixed := own.cfg
			mixed.WebDAV = nonNilOf(own.cfg.WebDAV, foreign.cfg.WebDAV)
			mixed.S3 = nonNilOf(own.cfg.S3, foreign.cfg.S3)
			mixed.SFTP = nonNilOf(own.cfg.SFTP, foreign.cfg.SFTP)
			mixed.SMB = nonNilOf(own.cfg.SMB, foreign.cfg.SMB)
			mixed.GitHubRelease = nonNilOf(own.cfg.GitHubRelease, foreign.cfg.GitHubRelease)
			mixed.Local = nonNilOf(own.cfg.Local, foreign.cfg.Local)
			if err := ValidateConfig(own.typ, mixed); err == nil {
				t.Errorf("config %s + %s group = nil, want error", own.name, foreign.name)
			} else if !errors.Is(err, ErrInvalid) {
				t.Errorf("config %s + %s group error = %v, want ErrInvalid", own.name, foreign.name, err)
			}
		}
	}
}

// TestValidateCredentialsStrictUnion 严格单选全矩阵：任何协议的
// credentials 混入任何其它协议的非空组都必须拒绝。
func TestValidateCredentialsStrictUnion(t *testing.T) {
	samples := []struct {
		name  string
		typ   Type
		cfg   Config
		creds Credentials
	}{
		{"webdav", TypeWebDAV, Config{WebDAV: &WebDAVConfig{Endpoint: "https://x/"}},
			Credentials{WebDAV: &WebDAVCredentials{Password: "p"}}},
		{"s3", TypeS3, Config{S3: ptrS3(validS3Config())},
			Credentials{S3: &S3Credentials{SecretKey: "shhh"}}},
		{"sftp", TypeSFTP, Config{SFTP: ptrSFTP(validSFTPConfig())},
			Credentials{SFTP: &SFTPCredentials{Password: "secret"}}},
		{"smb", TypeSMB, Config{SMB: ptrSMB(validSMBConfig())},
			Credentials{SMB: &SMBCredentials{Password: "secret"}}},
		{"github_release", TypeGitHubRelease, Config{GitHubRelease: &GitHubReleaseConfig{Repository: "owner/repo"}},
			Credentials{GitHubRelease: &GitHubReleaseCredentials{Token: "ghp_x"}}},
	}
	for _, own := range samples {
		for _, foreign := range samples {
			if own.name == foreign.name {
				continue
			}
			mixed := own.creds
			mixed.WebDAV = nonNilOf(own.creds.WebDAV, foreign.creds.WebDAV)
			mixed.S3 = nonNilOf(own.creds.S3, foreign.creds.S3)
			mixed.SFTP = nonNilOf(own.creds.SFTP, foreign.creds.SFTP)
			mixed.SMB = nonNilOf(own.creds.SMB, foreign.creds.SMB)
			mixed.GitHubRelease = nonNilOf(own.creds.GitHubRelease, foreign.creds.GitHubRelease)
			err := ValidateCredentials(own.typ, own.cfg, mixed)
			if err == nil {
				t.Errorf("credentials %s + %s group = nil, want error", own.name, foreign.name)
			} else if !errors.Is(err, ErrInvalid) {
				t.Errorf("credentials %s + %s group error = %v, want ErrInvalid", own.name, foreign.name, err)
			}
		}
	}
}

// TestValidateCredentialsUpdateStrictUnion 严格单选全矩阵：任何协议的
// 更新输入混入任何其它协议的非空组都必须拒绝。
func TestValidateCredentialsUpdateStrictUnion(t *testing.T) {
	s := "x"
	samples := []struct {
		name string
		typ  Type
		upd  CredentialsUpdate
	}{
		{"webdav", TypeWebDAV, CredentialsUpdate{WebDAV: &WebDAVCredentialsUpdate{Password: &s}}},
		{"s3", TypeS3, CredentialsUpdate{S3: &S3CredentialsUpdate{SecretKey: &s}}},
		{"sftp", TypeSFTP, CredentialsUpdate{SFTP: &SFTPCredentialsUpdate{Password: &s}}},
		{"smb", TypeSMB, CredentialsUpdate{SMB: &SMBCredentialsUpdate{Password: &s}}},
		{"github_release", TypeGitHubRelease, CredentialsUpdate{GitHubRelease: &GitHubReleaseCredentialsUpdate{Token: &s}}},
	}
	for _, own := range samples {
		for _, foreign := range samples {
			if own.name == foreign.name {
				continue
			}
			mixed := own.upd
			mixed.WebDAV = nonNilOf(own.upd.WebDAV, foreign.upd.WebDAV)
			mixed.S3 = nonNilOf(own.upd.S3, foreign.upd.S3)
			mixed.SFTP = nonNilOf(own.upd.SFTP, foreign.upd.SFTP)
			mixed.SMB = nonNilOf(own.upd.SMB, foreign.upd.SMB)
			mixed.GitHubRelease = nonNilOf(own.upd.GitHubRelease, foreign.upd.GitHubRelease)
			err := ValidateCredentialsUpdate(own.typ, &mixed)
			if err == nil {
				t.Errorf("credentials update %s + %s group = nil, want error", own.name, foreign.name)
			} else if !errors.Is(err, ErrInvalid) {
				t.Errorf("credentials update %s + %s group error = %v, want ErrInvalid", own.name, foreign.name, err)
			}
		}
	}
}

// nonNilOf 返回 a、b 中非 nil 的那个（同为 nil 时 nil），测试装置用。
func nonNilOf[T any](a, b *T) *T {
	if a != nil {
		return a
	}
	return b
}

// TestValidateCreateInputRejectsMixedConfig 严格单选在原始形态上先行：
// Normalized 会丢弃当前协议之外的组，先归一再校验会让混组输入绕过
// 单选不变量（SMB + GitHubRelease 混组曾被归一化消掉后通过校验）。
func TestValidateCreateInputRejectsMixedConfig(t *testing.T) {
	input := CreateInput{
		Name: "mixed",
		Type: TypeSMB,
		Config: Config{
			SMB:           ptrSMB(validSMBConfig()),
			GitHubRelease: &GitHubReleaseConfig{Repository: "owner/repo"},
		},
		Credentials: Credentials{SMB: &SMBCredentials{Password: "secret"}},
	}
	if err := ValidateCreateInput(input); err == nil {
		t.Error("mixed smb + github_release config = nil, want error")
	} else if !errors.Is(err, ErrInvalid) {
		t.Errorf("mixed config error = %v, want ErrInvalid", err)
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

func ptrSMB(c SMBConfig) *SMBConfig { return &c }

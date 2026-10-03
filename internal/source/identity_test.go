package source

import (
	"testing"
)

// sftpWith 便捷构造变体。
func sftpWith(mutate func(*SFTPConfig)) SFTPConfig {
	c := validSFTPConfig()
	mutate(&c)
	return c
}

// smbWith 便捷构造变体。
func smbWith(mutate func(*SMBConfig)) SMBConfig {
	c := validSMBConfig()
	mutate(&c)
	return c
}

// RemoteIdentityEqual 按协议判定身份字段：identity 字段变更返回
// false；非身份字段（name 之外的可变项如 access_key / auth_method）
// 与 secret 状态不影响判定。
func TestRemoteIdentityEqual(t *testing.T) {
	base := Source{
		Type:   TypeSFTP,
		Config: Config{SFTP: ptrSFTP(validSFTPConfig())},
	}
	if !RemoteIdentityEqual(base, base) {
		t.Error("self != self")
	}
	// 不同协议必然不同身份。
	otherType := base
	otherType.Type = TypeWebDAV
	if RemoteIdentityEqual(base, otherType) {
		t.Error("different types equal")
	}

	// SFTP：身份字段任一变化 → false；auth_method 变化 → true。
	sftpCases := []struct {
		name string
		cfg  SFTPConfig
		want bool
	}{
		{"same", validSFTPConfig(), true},
		{"host", sftpWith(func(c *SFTPConfig) { c.Host = "other.example.com" }), false},
		{"port", sftpWith(func(c *SFTPConfig) { c.Port = 2222 }), false},
		{"username", sftpWith(func(c *SFTPConfig) { c.Username = "other" }), false},
		{"remote root", sftpWith(func(c *SFTPConfig) { c.RemoteRoot = "/srv/other" }), false},
		{"fingerprint", sftpWith(func(c *SFTPConfig) {
			c.HostKeyFingerprint = "SHA256:uE1ZbMnBqENXQLEPGP8-tQ4idimYKUNBcBtk5H3cK60"
		}), false},
		{"auth method only", sftpWith(func(c *SFTPConfig) { c.AuthMethod = SFTPAuthPrivateKey }), true},
	}
	for _, tc := range sftpCases {
		other := Source{Type: TypeSFTP, Config: Config{SFTP: ptrSFTP(tc.cfg)}}
		if got := RemoteIdentityEqual(base, other); got != tc.want {
			t.Errorf("sftp %s = %v, want %v", tc.name, got, tc.want)
		}
	}

	// SMB：身份字段（host / port / share / remote_root / username /
	// domain）任一变化 → false；signing 只影响协商强度 → true。
	smbBase := Source{Type: TypeSMB, Config: Config{SMB: ptrSMB(validSMBConfig())}}
	smbCases := []struct {
		name string
		cfg  SMBConfig
		want bool
	}{
		{"same", validSMBConfig(), true},
		{"host", smbWith(func(c *SMBConfig) { c.Host = "other-nas.example.com" }), false},
		{"port", smbWith(func(c *SMBConfig) { c.Port = 1445 }), false},
		{"share", smbWith(func(c *SMBConfig) { c.Share = "media" }), false},
		{"remote root", smbWith(func(c *SMBConfig) { c.RemoteRoot = "/media" }), false},
		{"username", smbWith(func(c *SMBConfig) { c.Username = "other" }), false},
		{"domain", smbWith(func(c *SMBConfig) { c.Domain = "OTHER" }), false},
		{"signing only", smbWith(func(c *SMBConfig) { c.Signing = SMBSigningAuto }), true},
	}
	for _, tc := range smbCases {
		other := Source{Type: TypeSMB, Config: Config{SMB: ptrSMB(tc.cfg)}}
		if got := RemoteIdentityEqual(smbBase, other); got != tc.want {
			t.Errorf("smb %s = %v, want %v", tc.name, got, tc.want)
		}
	}

	// S3：identity 五字段变化 → false；access_key 变化 → true（凭据
	// 组件，允许随 secret 轮换）。
	s3Base := Source{Type: TypeS3, Config: Config{S3: ptrS3(validS3Config())}}
	s3Cases := []struct {
		name string
		cfg  S3Config
		want bool
	}{
		{"same", validS3Config(), true},
		{"endpoint", func() S3Config { c := validS3Config(); c.Endpoint = "https://other"; return c }(), false},
		{"region", func() S3Config { c := validS3Config(); c.Region = "eu-west-1"; return c }(), false},
		{"bucket", func() S3Config { c := validS3Config(); c.Bucket = "other"; return c }(), false},
		{"prefix", func() S3Config { c := validS3Config(); c.Prefix = "other"; return c }(), false},
		{"path style", func() S3Config { c := validS3Config(); c.PathStyle = false; return c }(), false},
		{"access key only", func() S3Config { c := validS3Config(); c.AccessKey = "NEW"; return c }(), true},
	}
	for _, tc := range s3Cases {
		other := Source{Type: TypeS3, Config: Config{S3: ptrS3(tc.cfg)}}
		if got := RemoteIdentityEqual(s3Base, other); got != tc.want {
			t.Errorf("s3 %s = %v, want %v", tc.name, got, tc.want)
		}
	}

	// WebDAV：endpoint 与 username 都是身份。
	davBase := Source{Type: TypeWebDAV, Config: Config{WebDAV: &WebDAVConfig{
		Endpoint: "https://nas/dav", Username: "user",
	}}}
	for name, cfg := range map[string]WebDAVConfig{
		"same":                               {Endpoint: "https://nas/dav", Username: "user"},
		"endpoint":                           {Endpoint: "https://other/dav", Username: "user"},
		"username":                           {Endpoint: "https://nas/dav", Username: "other"},
		"username is not secret-only change": {Endpoint: "https://nas/dav", Username: "other"},
	} {
		other := Source{Type: TypeWebDAV, Config: Config{WebDAV: &cfg}}
		want := name == "same"
		if got := RemoteIdentityEqual(davBase, other); got != want {
			t.Errorf("webdav %s = %v, want %v", name, got, want)
		}
	}
}

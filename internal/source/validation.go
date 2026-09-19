package source

import (
	"encoding/base64"
	"fmt"
	"net/url"
	"path"
	"strings"
	"unicode/utf8"

	"tinysync/internal/filesafe"
)

// maxNameLength 是 Source name 的长度上限（按 Unicode 字符计）。
const maxNameLength = 128

// sftpDefaultPort 是 SFTP 的默认端口。
const sftpDefaultPort = 22

// ValidateType 校验协议类型。
func ValidateType(t Type) error {
	switch t {
	case TypeWebDAV, TypeS3, TypeSFTP:
		return nil
	case "":
		return fmt.Errorf("%w: type is required", ErrInvalid)
	default:
		return fmt.Errorf("%w: unsupported source type %q", ErrInvalid, t)
	}
}

// ValidateName 校验 name：去除首尾空白后非空，且不超过长度上限。
func ValidateName(name string) error {
	if strings.TrimSpace(name) == "" {
		return fmt.Errorf("%w: name is required", ErrInvalid)
	}
	if utf8.RuneCountInString(name) > maxNameLength {
		return fmt.Errorf("%w: name exceeds %d characters", ErrInvalid, maxNameLength)
	}
	return nil
}

// ValidateEndpoint 校验 endpoint：
//   - scheme 必须为 http / https；
//   - host 必填；
//   - 凭据必须走独立字段，拒绝 https://user:pass@host/ 形式；
//   - 不允许 fragment。
func ValidateEndpoint(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("%w: parse endpoint %q: %v", ErrInvalid, raw, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("%w: endpoint scheme must be http or https, got %q", ErrInvalid, u.Scheme)
	}
	if u.Host == "" {
		return fmt.Errorf("%w: endpoint host is required", ErrInvalid)
	}
	if u.User != nil {
		return fmt.Errorf("%w: endpoint must not embed credentials; use separate username and password", ErrInvalid)
	}
	if u.Fragment != "" {
		return fmt.Errorf("%w: endpoint must not contain a fragment", ErrInvalid)
	}
	return nil
}

// ValidateConfig 校验 config 与 Type 的一致性及各协议字段约束：
// Type=webdav → 只能存在 WebDAV config，以此类推；config 缺失同样
// 拒绝（每个 Type 必须携带自己的配置）。
func ValidateConfig(t Type, c Config) error {
	switch t {
	case TypeWebDAV:
		if c.WebDAV == nil {
			return fmt.Errorf("%w: webdav config is required", ErrInvalid)
		}
		if c.S3 != nil || c.SFTP != nil {
			return fmt.Errorf("%w: config must only contain webdav fields for type webdav", ErrInvalid)
		}
		if err := ValidateEndpoint(c.WebDAV.Endpoint); err != nil {
			return err
		}
	case TypeS3:
		if c.S3 == nil {
			return fmt.Errorf("%w: s3 config is required", ErrInvalid)
		}
		if c.WebDAV != nil || c.SFTP != nil {
			return fmt.Errorf("%w: config must only contain s3 fields for type s3", ErrInvalid)
		}
		return validateS3Config(*c.S3)
	case TypeSFTP:
		if c.SFTP == nil {
			return fmt.Errorf("%w: sftp config is required", ErrInvalid)
		}
		if c.WebDAV != nil || c.S3 != nil {
			return fmt.Errorf("%w: config must only contain sftp fields for type sftp", ErrInvalid)
		}
		return validateSFTPConfig(*c.SFTP)
	case "":
		return fmt.Errorf("%w: type is required", ErrInvalid)
	default:
		return fmt.Errorf("%w: unsupported source type %q", ErrInvalid, t)
	}
	return nil
}

// validateS3Config 校验 S3 配置：region / bucket / access_key 必填，
// endpoint 有值时必须是合法 http(s) URL，prefix 不得以 / 开头
// （prefix 是 bucket 内的对象键前缀，不是绝对路径）。
func validateS3Config(c S3Config) error {
	if strings.TrimSpace(c.Region) == "" {
		return fmt.Errorf("%w: s3 region is required", ErrInvalid)
	}
	if strings.TrimSpace(c.Bucket) == "" {
		return fmt.Errorf("%w: s3 bucket is required", ErrInvalid)
	}
	if strings.TrimSpace(c.AccessKey) == "" {
		return fmt.Errorf("%w: s3 access_key is required", ErrInvalid)
	}
	if c.Endpoint != "" {
		u, err := url.Parse(c.Endpoint)
		if err != nil {
			return fmt.Errorf("%w: parse s3 endpoint %q: %v", ErrInvalid, c.Endpoint, err)
		}
		if u.Scheme != "http" && u.Scheme != "https" {
			return fmt.Errorf("%w: s3 endpoint scheme must be http or https, got %q", ErrInvalid, u.Scheme)
		}
		if u.Host == "" {
			return fmt.Errorf("%w: s3 endpoint host is required", ErrInvalid)
		}
	}
	if strings.HasPrefix(c.Prefix, "/") {
		return fmt.Errorf("%w: s3 prefix is a bucket-relative key prefix and must not start with /", ErrInvalid)
	}
	return nil
}

// validateSFTPConfig 校验 SFTP 配置：host / username / remote_root 必填，
// remote_root 必须是绝对路径，auth_method 显式且合法；可选的
// host_key_fingerprint 提供时必须是 SHA256:<base64> 形式。
func validateSFTPConfig(c SFTPConfig) error {
	if strings.TrimSpace(c.Host) == "" {
		return fmt.Errorf("%w: sftp host is required", ErrInvalid)
	}
	if c.Port != 0 && (c.Port < 1 || c.Port > 65535) {
		return fmt.Errorf("%w: sftp port %d out of range", ErrInvalid, c.Port)
	}
	if strings.TrimSpace(c.Username) == "" {
		return fmt.Errorf("%w: sftp username is required", ErrInvalid)
	}
	if !path.IsAbs(c.RemoteRoot) {
		return fmt.Errorf("%w: sftp remote_root %q must be an absolute path", ErrInvalid, c.RemoteRoot)
	}
	if path.Clean(c.RemoteRoot) != c.RemoteRoot {
		return fmt.Errorf("%w: sftp remote_root %q must be a clean path", ErrInvalid, c.RemoteRoot)
	}
	switch c.AuthMethod {
	case SFTPAuthPassword, SFTPAuthPrivateKey:
	case "":
		return fmt.Errorf("%w: sftp auth_method is required", ErrInvalid)
	default:
		return fmt.Errorf("%w: unsupported sftp auth_method %q", ErrInvalid, c.AuthMethod)
	}
	if strings.TrimSpace(c.HostKeyFingerprint) == "" {
		return nil
	}
	return validateHostKeyFingerprint(c.HostKeyFingerprint)
}

// validateHostKeyFingerprint 校验 SHA256:<unpadded base64> 形式的
// host key fingerprint（ssh.FingerprintSHA256 的输出格式）。
func validateHostKeyFingerprint(fp string) error {
	if !strings.HasPrefix(fp, hostKeyFingerprintPrefix) {
		return fmt.Errorf("%w: sftp host_key_fingerprint must start with %q", ErrInvalid, hostKeyFingerprintPrefix)
	}
	encoded := strings.TrimPrefix(fp, hostKeyFingerprintPrefix)
	if encoded == "" {
		return fmt.Errorf("%w: sftp host_key_fingerprint is empty", ErrInvalid)
	}
	// ssh.FingerprintSHA256 输出 strip 过 padding 的 base64。
	if strings.HasSuffix(encoded, "=") {
		return fmt.Errorf("%w: sftp host_key_fingerprint must not contain base64 padding", ErrInvalid)
	}
	if _, err := base64.StdEncoding.WithPadding(base64.NoPadding).DecodeString(encoded); err != nil {
		return fmt.Errorf("%w: sftp host_key_fingerprint is not valid base64: %v", ErrInvalid, err)
	}
	return nil
}

// ValidateCredentials 校验凭据与 Type / auth_method 的匹配：
//   - WebDAV：password 可为空（匿名）；
//   - S3：secret_key 必填；
//   - SFTP：password 方式要求 password；private_key 方式要求
//     private_key（passphrase 可选）。
func ValidateCredentials(t Type, c Config, creds Credentials) error {
	switch t {
	case TypeWebDAV:
		if creds.S3 != nil || creds.SFTP != nil {
			return fmt.Errorf("%w: credentials must only contain webdav fields for type webdav", ErrInvalid)
		}
	case TypeS3:
		if creds.WebDAV != nil || creds.SFTP != nil {
			return fmt.Errorf("%w: credentials must only contain s3 fields for type s3", ErrInvalid)
		}
		if creds.S3 == nil || creds.S3.SecretKey == "" {
			return fmt.Errorf("%w: s3 secret_key is required", ErrInvalid)
		}
	case TypeSFTP:
		if creds.WebDAV != nil || creds.S3 != nil {
			return fmt.Errorf("%w: credentials must only contain sftp fields for type sftp", ErrInvalid)
		}
		if creds.SFTP == nil {
			return fmt.Errorf("%w: sftp credentials are required", ErrInvalid)
		}
		switch c.SFTP.AuthMethod {
		case SFTPAuthPassword:
			if creds.SFTP.Password == "" {
				return fmt.Errorf("%w: sftp password is required for auth_method=password", ErrInvalid)
			}
		case SFTPAuthPrivateKey:
			if creds.SFTP.PrivateKey == "" {
				return fmt.Errorf("%w: sftp private_key is required for auth_method=private_key", ErrInvalid)
			}
		default:
			return fmt.Errorf("%w: unsupported sftp auth_method %q", ErrInvalid, c.SFTP.AuthMethod)
		}
	case "":
		return fmt.Errorf("%w: type is required", ErrInvalid)
	default:
		return fmt.Errorf("%w: unsupported source type %q", ErrInvalid, t)
	}
	return nil
}

// CredentialStateOf 从凭据集合推导回显状态（按 Type 单选）。
func CredentialStateOf(t Type, creds Credentials) CredentialState {
	switch t {
	case TypeWebDAV:
		pw := false
		if creds.WebDAV != nil {
			pw = creds.WebDAV.Password != ""
		}
		return CredentialState{WebDAV: &WebDAVCredentialState{PasswordSet: pw}}
	case TypeS3:
		set := false
		if creds.S3 != nil {
			set = creds.S3.SecretKey != ""
		}
		return CredentialState{S3: &S3CredentialState{SecretKeySet: set}}
	case TypeSFTP:
		st := SFTPCredentialState{}
		if creds.SFTP != nil {
			st.PasswordSet = creds.SFTP.Password != ""
			st.PrivateKeySet = creds.SFTP.PrivateKey != ""
			st.PrivateKeyPassphraseSet = creds.SFTP.PrivateKeyPassphrase != ""
		}
		return CredentialState{SFTP: &st}
	default:
		return CredentialState{}
	}
}

// ValidateCredentialsUpdate 校验凭据更新的组选择与 Type 匹配：
// 只允许出现当前协议的更新组；组内字段允许三态（nil 保留 / 空串
// 清除 / 非空替换），不校验结果组合的完备性（例如 password 方式下
// 清空 password 合法——结果状态由调用方按业务决定）。
func ValidateCredentialsUpdate(t Type, creds *CredentialsUpdate) error {
	if creds == nil {
		return nil
	}
	switch t {
	case TypeWebDAV:
		if creds.S3 != nil || creds.SFTP != nil {
			return fmt.Errorf("%w: credentials update must only contain webdav fields for type webdav", ErrInvalid)
		}
	case TypeS3:
		if creds.WebDAV != nil || creds.SFTP != nil {
			return fmt.Errorf("%w: credentials update must only contain s3 fields for type s3", ErrInvalid)
		}
	case TypeSFTP:
		if creds.WebDAV != nil || creds.S3 != nil {
			return fmt.Errorf("%w: credentials update must only contain sftp fields for type sftp", ErrInvalid)
		}
	case "":
		return fmt.Errorf("%w: type is required", ErrInvalid)
	default:
		return fmt.Errorf("%w: unsupported source type %q", ErrInvalid, t)
	}
	return nil
}

// applyCredentialsUpdate 把三态凭据更新应用到现有回显状态上：
// nil 保留、false 清除、true 设置。
func applyCredentialsUpdate(t Type, current CredentialState, update *CredentialsUpdate) CredentialState {
	if update == nil {
		return current
	}
	switch t {
	case TypeWebDAV:
		if update.WebDAV == nil || update.WebDAV.Password == nil {
			return current
		}
		pw := *update.WebDAV.Password != ""
		if current.WebDAV == nil {
			current.WebDAV = &WebDAVCredentialState{}
		}
		c := *current.WebDAV
		c.PasswordSet = pw
		return CredentialState{WebDAV: &c}
	case TypeS3:
		if update.S3 == nil || update.S3.SecretKey == nil {
			return current
		}
		set := *update.S3.SecretKey != ""
		if current.S3 == nil {
			current.S3 = &S3CredentialState{}
		}
		c := *current.S3
		c.SecretKeySet = set
		return CredentialState{S3: &c}
	case TypeSFTP:
		if update.SFTP == nil {
			return current
		}
		if current.SFTP == nil {
			current.SFTP = &SFTPCredentialState{}
		}
		c := *current.SFTP
		if update.SFTP.Password != nil {
			c.PasswordSet = *update.SFTP.Password != ""
		}
		if update.SFTP.PrivateKey != nil {
			c.PrivateKeySet = *update.SFTP.PrivateKey != ""
		}
		if update.SFTP.PrivateKeyPassphrase != nil {
			c.PrivateKeyPassphraseSet = *update.SFTP.PrivateKeyPassphrase != ""
		}
		return CredentialState{SFTP: &c}
	default:
		return current
	}
}

// ValidateLogicalPath 校验协议无关的 Source-relative logical path：
// "/" 表示 Source root；条目必须是 "/x/y" 形式的绝对路径，目录不带
// 尾随分隔符。统一拒绝 dot segments、重复分隔符（经 clean 规则）、
// 反斜杠与 NUL——S3 object key 允许 "foo\bar.txt"，进入本地
// filepath 后在 Unix 与 Windows 上语义不同，必须在协议边界拒绝。
// 任何 adapter 返回的 FileInfo.Path 都必须通过本校验。
// 基础规则由 filesafe 提供并与本地/发布路径共享，本函数只补上
// 领域错误标记，保证调用方 errors.Is(err, ErrInvalid) 语义不变。
func ValidateLogicalPath(p string) error {
	if err := filesafe.ValidateLogicalPath(p); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	return nil
}

// ValidateCreateInput 校验创建输入的全部必填与格式约束；config 与
// credentials 基于归一化后的形态校验，与持久化值一致。
func ValidateCreateInput(input CreateInput) error {
	if err := ValidateName(input.Name); err != nil {
		return err
	}
	if err := ValidateType(input.Type); err != nil {
		return err
	}
	normalized := input.Config.Normalized(input.Type)
	if err := ValidateConfig(input.Type, normalized); err != nil {
		return err
	}
	return ValidateCredentials(input.Type, normalized, input.Credentials)
}

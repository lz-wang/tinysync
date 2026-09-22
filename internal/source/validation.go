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
	case TypeWebDAV, TypeS3, TypeSFTP, TypeGitHubRelease:
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
		if c.WebDAV.RemoteRoot != "" && (!path.IsAbs(c.WebDAV.RemoteRoot) || path.Clean(c.WebDAV.RemoteRoot) != c.WebDAV.RemoteRoot) {
			return fmt.Errorf("%w: webdav remote_root %q must be a clean absolute path", ErrInvalid, c.WebDAV.RemoteRoot)
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
	case TypeGitHubRelease:
		if c.GitHubRelease == nil {
			return fmt.Errorf("%w: github_release config is required", ErrInvalid)
		}
		if c.WebDAV != nil || c.S3 != nil || c.SFTP != nil {
			return fmt.Errorf("%w: config must only contain github_release fields for type github_release", ErrInvalid)
		}
		return validateGitHubReleaseConfig(*c.GitHubRelease)
	case "":
		return fmt.Errorf("%w: type is required", ErrInvalid)
	default:
		return fmt.Errorf("%w: unsupported source type %q", ErrInvalid, t)
	}
	return nil
}

// validateS3Config 校验 S3 配置：endpoint / bucket / access_key 必填，region 可选，
// endpoint 有值时必须是合法 http(s) URL，prefix 不得以 / 开头
// （prefix 是 bucket 内的对象键前缀，不是绝对路径）。
func validateS3Config(c S3Config) error {
	if strings.TrimSpace(c.Endpoint) == "" {
		return fmt.Errorf("%w: s3 endpoint is required", ErrInvalid)
	}
	if strings.TrimSpace(c.Bucket) == "" {
		return fmt.Errorf("%w: s3 bucket is required", ErrInvalid)
	}
	if strings.TrimSpace(c.AccessKey) == "" {
		return fmt.Errorf("%w: s3 access_key is required", ErrInvalid)
	}
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
	if strings.HasPrefix(c.Prefix, "/") {
		return fmt.Errorf("%w: s3 prefix is a bucket-relative key prefix and must not start with /", ErrInvalid)
	}
	return nil
}

// validateSFTPConfig 校验 SFTP 配置：host / username 必填；remote_root 留空时
// 由 SFTP 服务器解析为该用户 Home 目录。
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
	if c.RemoteRoot != "" && !path.IsAbs(c.RemoteRoot) {
		return fmt.Errorf("%w: sftp remote_root %q must be an absolute path", ErrInvalid, c.RemoteRoot)
	}
	if c.RemoteRoot != "" && path.Clean(c.RemoteRoot) != c.RemoteRoot {
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

// validateGitHubReleaseConfig 校验 GitHub Release 配置。只覆盖归一化
// （Normalized）无法修复的约束：repository 格式、policy 取值、tag /
// recent 模式的必填项与 verify_sha256 取值；非对应策略下的 Tag /
// RecentCount 由 Normalized 归一为零值，此处不拒绝（校验须同时接受
// 归一化前后的形态，Service.Update 先校验后归一）。
func validateGitHubReleaseConfig(c GitHubReleaseConfig) error {
	if err := ValidateGitHubRepository(c.Repository); err != nil {
		return err
	}
	switch c.ReleasePolicy {
	case "", ReleaseLatest, ReleaseTag, ReleaseRecent, ReleaseAll:
	default:
		return fmt.Errorf("%w: unsupported github_release release_policy %q", ErrInvalid, c.ReleasePolicy)
	}
	if c.ReleasePolicy == ReleaseTag && strings.TrimSpace(c.Tag) == "" {
		return fmt.Errorf("%w: github_release tag is required for release_policy=tag", ErrInvalid)
	}
	if c.ReleasePolicy == ReleaseRecent && (c.RecentCount < 1 || c.RecentCount > MaxGitHubRecentCount) {
		return fmt.Errorf("%w: github_release recent_count must be between 1 and %d", ErrInvalid, MaxGitHubRecentCount)
	}
	switch c.VerifySHA256 {
	case "", SHA256IfAvailable, SHA256Required:
	default:
		return fmt.Errorf("%w: unsupported github_release verify_sha256 %q", ErrInvalid, c.VerifySHA256)
	}
	return nil
}

// ValidateGitHubRepository 校验 repository 字段：接受 owner/repo 或
// github.com / www.github.com 的仓库 URL（可带 .git 后缀与深层 path，
// 解析由 adapter 承担）。owner 与 repo 段限定 GitHub 允许的字符集，
// 保证其可直接拼入 API path，不做运行时 escape。
func ValidateGitHubRepository(raw string) error {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return fmt.Errorf("%w: github_release repository is required", ErrInvalid)
	}
	owner, repo, err := ParseGitHubRepository(trimmed)
	if err != nil {
		return fmt.Errorf("%w: github_release repository %q must be owner/repo or a github.com repository URL", ErrInvalid, raw)
	}
	for _, seg := range [2]string{owner, repo} {
		if seg == "" || !validGitHubRepoSegment(seg) {
			return fmt.Errorf("%w: github_release repository %q has invalid segment %q", ErrInvalid, raw, seg)
		}
	}
	return nil
}

// ParseGitHubRepository 从 owner/repo 或 github.com 仓库 URL 中提取
// owner 与 repo 两段；repo 去掉 .git 后缀。形态无法识别时返回
// ErrInvalid。导出供 githubrelease factory 在客户端构造时复用同一
// 解析规则，避免校验与运行时各持一份。
func ParseGitHubRepository(raw string) (owner, repo string, err error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", "", fmt.Errorf("%w: github_release repository is required", ErrInvalid)
	}
	if !strings.Contains(trimmed, "://") {
		o, r, found := strings.Cut(trimmed, "/")
		if !found || strings.Contains(r, "/") {
			return "", "", fmt.Errorf("%w: github_release repository %q must be owner/repo or a github.com repository URL", ErrInvalid, raw)
		}
		return o, strings.TrimSuffix(r, ".git"), nil
	}
	u, err := url.Parse(trimmed)
	if err != nil {
		return "", "", fmt.Errorf("%w: parse github_release repository %q: %v", ErrInvalid, raw, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", "", fmt.Errorf("%w: github_release repository URL %q must be http(s)", ErrInvalid, raw)
	}
	if u.Host != "github.com" && u.Host != "www.github.com" {
		return "", "", fmt.Errorf("%w: github_release repository URL %q must point at github.com", ErrInvalid, raw)
	}
	segs := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(segs) < 2 || segs[0] == "" || segs[1] == "" {
		return "", "", fmt.Errorf("%w: github_release repository URL %q has no owner/repo path", ErrInvalid, raw)
	}
	return segs[0], strings.TrimSuffix(segs[1], ".git"), nil
}

// validGitHubRepoSegment 判断 owner / repo 段是否只含 GitHub 允许的
// 字符（字母、数字、.、_、-），并拒绝 . / .. 等点路径分量。
func validGitHubRepoSegment(seg string) bool {
	if seg == "." || seg == ".." {
		return false
	}
	for _, r := range seg {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '.' || r == '_' || r == '-':
		default:
			return false
		}
	}
	return true
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
	case TypeGitHubRelease:
		if creds.WebDAV != nil || creds.S3 != nil || creds.SFTP != nil {
			return fmt.Errorf("%w: credentials must only contain github_release fields for type github_release", ErrInvalid)
		}
		// Token 可为空（公开仓库匿名访问），无必填约束。
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
	case TypeGitHubRelease:
		set := false
		if creds.GitHubRelease != nil {
			set = creds.GitHubRelease.Token != ""
		}
		return CredentialState{GitHubRelease: &GitHubReleaseCredentialState{TokenSet: set}}
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
	case TypeGitHubRelease:
		if creds.WebDAV != nil || creds.S3 != nil || creds.SFTP != nil {
			return fmt.Errorf("%w: credentials update must only contain github_release fields for type github_release", ErrInvalid)
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
	case TypeGitHubRelease:
		if update.GitHubRelease == nil || update.GitHubRelease.Token == nil {
			return current
		}
		set := *update.GitHubRelease.Token != ""
		if current.GitHubRelease == nil {
			current.GitHubRelease = &GitHubReleaseCredentialState{}
		}
		c := *current.GitHubRelease
		c.TokenSet = set
		return CredentialState{GitHubRelease: &c}
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
// 基础规则由 filesafe 提供并与本地路径共享，本函数只补上
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

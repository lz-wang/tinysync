// Package source 承载 Source 领域：模型、校验、持久化接口与应用服务。
// Source 是对异构远端（WebDAV / S3 / SFTP）的统一只读抽象；
// 领域对象不携带 secret，secret 只经 Credentials 输入结构与
// Repository 凭据查询流转，从结构层避免 API 与日志意外泄漏。
package source

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Type 是 Source 的协议类型。创建后不可变：不支持原地转换协议。
type Type string

// 支持的协议类型。
const (
	TypeWebDAV        Type = "webdav"
	TypeS3            Type = "s3"
	TypeSFTP          Type = "sftp"
	TypeGitHubRelease Type = "github_release"
)

// GitHubReleasePolicy 是 GitHub Release Source 的版本选择策略。
type GitHubReleasePolicy string

// 支持的版本选择策略。语义契约见 docs/design/github-release.md §2：
// latest 遵循 GitHub 的 latest 端点选择规则（非 prerelease、非
// draft），不假定其为发布时间最近或版本号最大；recent 按完整枚举
// 后的 published_at 降序取前 N。
const (
	ReleaseLatest GitHubReleasePolicy = "latest"
	ReleaseTag    GitHubReleasePolicy = "tag"
	ReleaseRecent GitHubReleasePolicy = "recent"
	ReleaseAll    GitHubReleasePolicy = "all"
)

// GitHubSHA256Mode 是 GitHub Release Asset 的 SHA-256 校验策略。
type GitHubSHA256Mode string

// 校验策略：if_available 在 GitHub 提供 digest 时强制校验（默认），
// required 要求全部入选 Asset 均带有效 digest，否则扫描阶段失败。
const (
	SHA256IfAvailable GitHubSHA256Mode = "if_available"
	SHA256Required    GitHubSHA256Mode = "required"
)

// MaxGitHubRecentCount 是 recent 策略允许的最大版本数，与扫描规模
// 上限（1000 个 Release）一致：超过上限拒绝配置，而不是截断后进入
// Mirror 删除授权。
const MaxGitHubRecentCount = 1000

// SFTPAuthMethod 是 SFTP 的认证方式；显式声明，不根据字段非空推断。
type SFTPAuthMethod string

// SFTP 认证方式。
const (
	SFTPAuthPassword   SFTPAuthMethod = "password"
	SFTPAuthPrivateKey SFTPAuthMethod = "private_key"
)

// hostKeyFingerprintPrefix 是 SFTP host key fingerprint 的固定前缀。
const hostKeyFingerprintPrefix = "SHA256:"

// 领域哨兵错误：各实现（Repository、Service）必须以 errors.Is 判定。
var (
	// ErrNotFound 表示目标 Source 不存在。
	ErrNotFound = errors.New("source not found")
	// ErrConflict 表示 name 与现有 Source 冲突。
	ErrConflict = errors.New("source name already exists")
	// ErrInvalid 表示输入校验失败。
	ErrInvalid = errors.New("invalid source")
	// ErrUnsupportedType 表示协议类型暂未支持。
	ErrUnsupportedType = errors.New("unsupported source type")
)

// Source 是 Source 的领域对象：协议类型 + typed 配置 + 凭据状态。
// 刻意不包含 secret 明文字段：secret 只存在于 Credentials 输入结构、
// Repository 凭据查询与远端客户端构造路径中。
// Config 按 Type 严格单选：Type=webdav → 只能存在 WebDAV config，
// 以此类推；一致性由 ValidateConfig 强制。
type Source struct {
	ID              string
	Name            string
	Type            Type
	Config          Config
	CredentialState CredentialState
	Enabled         bool
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// Config 是 Source 的非敏感协议配置，按 Type 严格单选。
type Config struct {
	WebDAV        *WebDAVConfig
	S3            *S3Config
	SFTP          *SFTPConfig
	GitHubRelease *GitHubReleaseConfig
}

// WebDAVConfig 是 WebDAV Source 的非敏感配置。
type WebDAVConfig struct {
	Endpoint   string `json:"endpoint"`
	RemoteRoot string `json:"remote_root"`
	Username   string `json:"username"`
}

// S3Config 是 S3 Source 的非敏感配置。AccessKey 本身不按 secret 处理；
// Endpoint 为空串表示 AWS 默认 endpoint（自建 S3 / MinIO 填显式值）。
type S3Config struct {
	Endpoint  string `json:"endpoint"`
	Region    string `json:"region"`
	Bucket    string `json:"bucket"`
	Prefix    string `json:"prefix"`
	PathStyle bool   `json:"path_style"`
	AccessKey string `json:"access_key"`
}

// SFTPConfig 是 SFTP Source 的非敏感配置。AuthMethod 显式声明认证
// 方式；HostKeyFingerprint 可选，提供时必须为 SHA256:... 形式并用于
// 严格校验；留空时跳过主机密钥校验。CredentialID 是凭据引用（ADR
// 0005）：非空时 auth_method 必须为 private_key，且源自身不得再持有
// 内联私钥 / 口令——引用与内联互斥，生效 secret 在远端客户端构造时
// 从凭据库解析。
type SFTPConfig struct {
	Host               string         `json:"host"`
	Port               int            `json:"port"`
	Username           string         `json:"username"`
	RemoteRoot         string         `json:"remote_root"`
	AuthMethod         SFTPAuthMethod `json:"auth_method"`
	HostKeyFingerprint string         `json:"host_key_fingerprint"`
	CredentialID       string         `json:"credential_id"`
}

// GitHubReleaseConfig 是 GitHub Release Source 的非敏感配置。
// Repository 接受 owner/repo 或完整 GitHub 仓库 URL（解析规则由
// adapter 承担，持久化只做 trim）；Tag / RecentCount 仅在对应策略
// 下有意义，持久化前经 Normalized 归一为零值。
type GitHubReleaseConfig struct {
	Repository         string              `json:"repository"`
	ReleasePolicy      GitHubReleasePolicy `json:"release_policy"`
	Tag                string              `json:"tag,omitempty"`
	RecentCount        int                 `json:"recent_count,omitempty"`
	IncludePrereleases bool                `json:"include_prereleases"`
	VerifySHA256       GitHubSHA256Mode    `json:"verify_sha256"`
}

// Credentials 是一次写入或构造远端客户端的 secret 集合，按 Type
// 严格单选。仅在 CreateInput / UpdateInput / Repository 凭据查询 /
// Remote 构造路径流转，绝不进入 Source 对象与 API 响应。
type Credentials struct {
	WebDAV        *WebDAVCredentials
	S3            *S3Credentials
	SFTP          *SFTPCredentials
	GitHubRelease *GitHubReleaseCredentials
}

// GitHubReleaseCredentials 是 GitHub Release 的 secret。Token 可为空
// （公开仓库匿名访问）；私有仓库需要具有相应读取权限的 PAT。
type GitHubReleaseCredentials struct {
	Token string
}

// WebDAVCredentials 是 WebDAV 的 secret。Password 可为空（匿名访问）。
type WebDAVCredentials struct {
	Password string
}

// S3Credentials 是 S3 的 secret。
type S3Credentials struct {
	SecretKey string
}

// SFTPCredentials 是 SFTP 的 secret；按 AuthMethod 二选一。
type SFTPCredentials struct {
	// Password 用于 auth_method=password。
	Password string
	// PrivateKey 用于 auth_method=private_key（PEM 编码）。
	PrivateKey string
	// PrivateKeyPassphrase 可选，仅 private_key 方式有效。
	PrivateKeyPassphrase string
}

// CredentialState 是各 secret 是否已设置的布尔集合，协议无关地用于
// API 回显与 UI 状态展示；按 Type 严格单选，与 Config 对应。
type CredentialState struct {
	WebDAV        *WebDAVCredentialState        `json:"webdav,omitempty"`
	S3            *S3CredentialState            `json:"s3,omitempty"`
	SFTP          *SFTPCredentialState          `json:"sftp,omitempty"`
	GitHubRelease *GitHubReleaseCredentialState `json:"github_release,omitempty"`
}

// WebDAVCredentialState 是 WebDAV 的凭据状态。
type WebDAVCredentialState struct {
	PasswordSet bool `json:"password_set"`
}

// S3CredentialState 是 S3 的凭据状态。
type S3CredentialState struct {
	SecretKeySet bool `json:"secret_key_set"`
}

// SFTPCredentialState 是 SFTP 的凭据状态。
type SFTPCredentialState struct {
	PasswordSet             bool `json:"password_set"`
	PrivateKeySet           bool `json:"private_key_set"`
	PrivateKeyPassphraseSet bool `json:"private_key_passphrase_set"`
}

// GitHubReleaseCredentialState 是 GitHub Release 的凭据状态。
type GitHubReleaseCredentialState struct {
	TokenSet bool `json:"token_set"`
}

// idPrefix 是 Source ID 的固定前缀，便于在日志与 API 中一眼识别。
const idPrefix = "src_"

// newIDSize 是随机部分的字节数（128 bit）。
const newIDSize = 16

// NewID 生成 src_<128-bit random hex> 形式的唯一 ID，不引入 UUID 依赖。
func NewID() (string, error) {
	buf := make([]byte, newIDSize)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate source id: %w", err)
	}
	return idPrefix + hex.EncodeToString(buf), nil
}

// CreateInput 是创建 Source 的输入。Credentials 可为空（WebDAV 匿名）。
type CreateInput struct {
	Name        string
	Type        Type
	Config      Config
	Credentials Credentials
	Enabled     bool
}

// CredentialsUpdate 是更新 secret 的输入：指针字段区分「未提供」
// （保留现有值）与「空串」（清除）；非空替换。按 Type 单选组。
type CredentialsUpdate struct {
	WebDAV        *WebDAVCredentialsUpdate
	S3            *S3CredentialsUpdate
	SFTP          *SFTPCredentialsUpdate
	GitHubRelease *GitHubReleaseCredentialsUpdate
}

// GitHubReleaseCredentialsUpdate 是 GitHub Release secret 的更新输入。
type GitHubReleaseCredentialsUpdate struct {
	Token *string
}

// WebDAVCredentialsUpdate 是 WebDAV secret 的更新输入。
type WebDAVCredentialsUpdate struct {
	Password *string
}

// S3CredentialsUpdate 是 S3 secret 的更新输入。
type S3CredentialsUpdate struct {
	SecretKey *string
}

// SFTPCredentialsUpdate 是 SFTP secret 的更新输入；三个字段独立三态。
type SFTPCredentialsUpdate struct {
	Password             *string
	PrivateKey           *string
	PrivateKeyPassphrase *string
}

// UpdateInput 是更新 Source 的输入，指针字段区分「未提供」与「零值」：
// nil 表示保留现有值；Credentials 组内 secret 为三态（nil 保留、
// 空串清除、非空替换）。Type 不支持修改，输入结构不携带 Type。
type UpdateInput struct {
	Name        *string
	Config      *Config
	Credentials *CredentialsUpdate
	Enabled     *bool
}

// Normalized 返回按 Type 归一化后的配置副本：WebDAV endpoint 去首尾
// 空白，SFTP port 零值取默认 22，GitHub Release 的 policy 空值取
// latest、verify_sha256 空值取 if_available，非对应策略下的 Tag /
// RecentCount 归一为零值（身份比较与持久化因此形态稳定）。调用前必须
// 已通过 ValidateConfig。
func (c Config) Normalized(t Type) Config {
	switch t {
	case TypeWebDAV:
		if c.WebDAV == nil {
			return c
		}
		dav := *c.WebDAV
		dav.Endpoint = strings.TrimSpace(dav.Endpoint)
		return Config{WebDAV: &dav}
	case TypeS3:
		if c.S3 == nil {
			return c
		}
		s3 := *c.S3
		s3.Endpoint = strings.TrimSpace(s3.Endpoint)
		s3.Region = strings.TrimSpace(s3.Region)
		s3.Bucket = strings.TrimSpace(s3.Bucket)
		s3.AccessKey = strings.TrimSpace(s3.AccessKey)
		return Config{S3: &s3}
	case TypeSFTP:
		if c.SFTP == nil {
			return c
		}
		sftp := *c.SFTP
		sftp.Host = strings.TrimSpace(sftp.Host)
		sftp.Username = strings.TrimSpace(sftp.Username)
		sftp.CredentialID = strings.TrimSpace(sftp.CredentialID)
		if sftp.Port == 0 {
			sftp.Port = 22
		}
		return Config{SFTP: &sftp}
	case TypeGitHubRelease:
		if c.GitHubRelease == nil {
			return c
		}
		gh := *c.GitHubRelease
		gh.Repository = strings.TrimSpace(gh.Repository)
		if gh.ReleasePolicy == "" {
			gh.ReleasePolicy = ReleaseLatest
		}
		if gh.ReleasePolicy != ReleaseTag {
			gh.Tag = ""
		}
		if gh.ReleasePolicy != ReleaseRecent {
			gh.RecentCount = 0
		}
		if gh.VerifySHA256 == "" {
			gh.VerifySHA256 = SHA256IfAvailable
		}
		return Config{GitHubRelease: &gh}
	default:
		return c
	}
}

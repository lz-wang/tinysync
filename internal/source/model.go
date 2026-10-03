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
	"net/url"
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
	TypeSMB           Type = "smb"
	TypeGitHubRelease Type = "github_release"
	TypeLocal         Type = "local"
	TypeHTTP          Type = "http"
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

// SMBSigningPolicy 是 SMB 消息签名策略。枚举而非 bool：bool 零值无法
// 区分「字段未提供」与「用户明确关闭要求」。不提供 disabled——auto
// 已足够兼容家庭 LAN 的旧 NAS，降低客户端要求必须是主动选择。
type SMBSigningPolicy string

// SMB 消息签名策略：required 要求服务器启用签名，协商失败即失败
// （默认）；auto 跟随服务器协商结果。
const (
	SMBSigningRequired SMBSigningPolicy = "required"
	SMBSigningAuto     SMBSigningPolicy = "auto"
)

// SMBDefaultPort 是 SMB 的默认端口（445，Direct TCP）。
const SMBDefaultPort = 445

// HTTPListingMode 是 HTTP 文件源的目录索引表现形式（ADR 0009）。
// nginx / Caddy / miniserve 不是独立 Source Type，只是同一 http 类型
// 下的 listing profile；auto 按响应形态自动识别。
type HTTPListingMode string

// 支持的目录索引表现形式。
const (
	HTTPListingAuto      HTTPListingMode = "auto"
	HTTPListingNginx     HTTPListingMode = "nginx"
	HTTPListingCaddy     HTTPListingMode = "caddy"
	HTTPListingMiniserve HTTPListingMode = "miniserve"
)

// HTTPAuthMethod 是 HTTP 文件源的认证方式；显式声明，不根据字段
// 非空推断（与 SFTP 的 AuthMethod 同一风格）。
type HTTPAuthMethod string

// 支持的认证方式：none / basic（用户名密码）/ bearer（token）。
const (
	HTTPAuthNone   HTTPAuthMethod = "none"
	HTTPAuthBasic  HTTPAuthMethod = "basic"
	HTTPAuthBearer HTTPAuthMethod = "bearer"
)

// DefaultCaddyFileLimit 是 Caddy file_server browse 的默认 file_limit
// （官方默认 10000，超出后只显示前 N 个条目）。归一化把 0 补成该值；
// 单目录 caddy listing 达到该值时扫描整轮失败（fail-closed）。
const DefaultCaddyFileLimit = 10000

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
	SMB           *SMBConfig
	GitHubRelease *GitHubReleaseConfig
	Local         *LocalConfig
	HTTP          *HTTPConfig
}

// LocalConfig 是宿主机文件树的非敏感配置，Root 为 canonical native 绝对路径。
type LocalConfig struct {
	Root string `json:"root"`
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

// SMBConfig 是 SMB Source 的非敏感配置。Host 是裸主机名 / IP（不带
// smb:// 前缀、不带 share），Share 是 share 名（不含分隔符），二者
// 分离使远端身份各字段可独立校验；RemoteRoot 是 share 内的 POSIX
// 风格绝对逻辑路径（tinysync 内部不暴露 SMB 反斜杠路径，adapter
// 边界才转换为 native 分隔符）。认证只有 NTLMv2 用户名/密码，
// 不支持 guest——匿名访问需要显式 auth_method，不做字段空值推断。
type SMBConfig struct {
	Host       string           `json:"host"`
	Port       int              `json:"port"`
	Share      string           `json:"share"`
	RemoteRoot string           `json:"remote_root"`
	Username   string           `json:"username"`
	Domain     string           `json:"domain"`
	Signing    SMBSigningPolicy `json:"signing"`
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

// HTTPConfig 是 HTTP 文件源的非敏感配置（ADR 0009）。BaseURL 即
// Source 的 "/"（HTTP 文件服务的 URL path 是 namespace 的一部分，
// 不设独立 remote_root）；canonical form 为补齐尾 / 的 clean
// http(s) URL，禁止 userinfo / query / fragment。ListingMode 是
// 目录索引 profile（nginx / caddy / miniserve 只是表现形式差异，
// 不是独立 Source Type）。CaddyFileLimit 是扫描完整性参数而非远端
// 身份：与 Caddy browse.file_limit 一致，达到即整轮扫描失败。
type HTTPConfig struct {
	BaseURL     string          `json:"base_url"`
	ListingMode HTTPListingMode `json:"listing_mode"`
	AuthMethod  HTTPAuthMethod  `json:"auth_method"`
	// Username 仅 basic 方式使用；none / bearer 归一化时清空。
	Username string `json:"username,omitempty"`
	// CaddyFileLimit 0 归一为 DefaultCaddyFileLimit。
	CaddyFileLimit int `json:"caddy_file_limit,omitempty"`
}

// Credentials 是一次写入或构造远端客户端的 secret 集合，按 Type
// 严格单选。仅在 CreateInput / UpdateInput / Repository 凭据查询 /
// Remote 构造路径流转，绝不进入 Source 对象与 API 响应。
type Credentials struct {
	WebDAV        *WebDAVCredentials
	S3            *S3Credentials
	SFTP          *SFTPCredentials
	SMB           *SMBCredentials
	GitHubRelease *GitHubReleaseCredentials
	HTTP          *HTTPCredentials
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

// SMBCredentials 是 SMB 的 secret（NTLMv2 密码）。生命周期与 WebDAV
// password 完全一致：明文只存在于 credentials_json，普通 API 永不
// 回显；v1 不接入凭据库（credential library 目前只服务 SFTP SSH
// key，username/password 通用凭据是独立的设计议题）。
type SMBCredentials struct {
	Password string
}

// HTTPCredentials 是 HTTP 文件源的 secret，按 AuthMethod 二选一：
// basic 用 Password，bearer 用 BearerToken。生命周期与 WebDAV
// password 一致：明文只存在于 credentials_json，普通 API 永不回显。
type HTTPCredentials struct {
	// Password 用于 auth_method=basic。
	Password string
	// BearerToken 用于 auth_method=bearer。
	BearerToken string
}

// CredentialState 是各 secret 是否已设置的布尔集合，协议无关地用于
// API 回显与 UI 状态展示；按 Type 严格单选，与 Config 对应。
type CredentialState struct {
	WebDAV        *WebDAVCredentialState        `json:"webdav,omitempty"`
	S3            *S3CredentialState            `json:"s3,omitempty"`
	SFTP          *SFTPCredentialState          `json:"sftp,omitempty"`
	SMB           *SMBCredentialState           `json:"smb,omitempty"`
	GitHubRelease *GitHubReleaseCredentialState `json:"github_release,omitempty"`
	HTTP          *HTTPCredentialState          `json:"http,omitempty"`
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

// SMBCredentialState 是 SMB 的凭据状态。
type SMBCredentialState struct {
	PasswordSet bool `json:"password_set"`
}

// HTTPCredentialState 是 HTTP 文件源的凭据状态。
type HTTPCredentialState struct {
	PasswordSet    bool `json:"password_set"`
	BearerTokenSet bool `json:"bearer_token_set"`
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
	SMB           *SMBCredentialsUpdate
	GitHubRelease *GitHubReleaseCredentialsUpdate
	HTTP          *HTTPCredentialsUpdate
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

// SMBCredentialsUpdate 是 SMB secret 的更新输入。
type SMBCredentialsUpdate struct {
	Password *string
}

// HTTPCredentialsUpdate 是 HTTP 文件源 secret 的更新输入；两个字段
// 独立三态。
type HTTPCredentialsUpdate struct {
	Password    *string
	BearerToken *string
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
// 空白，SFTP port 零值取默认 22，SMB 的 port 零值取默认 445、
// remote_root 空串取 "/"、signing 空值取 required（持久化后 SMB
// 配置保持 canonical form），GitHub Release 的 policy 空值取
// latest、verify_sha256 空值取 if_available，非对应策略下的 Tag /
// RecentCount 归一为零值（身份比较与持久化因此形态稳定），HTTP 的
// BaseURL 归一为补齐尾 / 的 clean http(s) URL、listing_mode 空值取
// auto、auth_method 空值取 none、none / bearer 下 username 清空、
// caddy_file_limit 零值取 10000。调用前必须已通过 ValidateConfig。
func (c Config) Normalized(t Type) Config {
	switch t {
	case TypeLocal:
		if c.Local == nil {
			return c
		}
		local := *c.Local
		local.Root = strings.TrimSpace(local.Root)
		return Config{Local: &local}
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
	case TypeSMB:
		if c.SMB == nil {
			return c
		}
		smb := *c.SMB
		smb.Host = strings.TrimSpace(smb.Host)
		smb.Share = strings.TrimSpace(smb.Share)
		smb.Username = strings.TrimSpace(smb.Username)
		smb.Domain = strings.TrimSpace(smb.Domain)
		if smb.Port == 0 {
			smb.Port = SMBDefaultPort
		}
		if smb.RemoteRoot == "" {
			smb.RemoteRoot = "/"
		}
		if smb.Signing == "" {
			smb.Signing = SMBSigningRequired
		}
		return Config{SMB: &smb}
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
	case TypeHTTP:
		if c.HTTP == nil {
			return c
		}
		h := *c.HTTP
		h.BaseURL = canonicalHTTPBaseURL(h.BaseURL)
		if h.ListingMode == "" {
			h.ListingMode = HTTPListingAuto
		}
		if h.AuthMethod == "" {
			h.AuthMethod = HTTPAuthNone
		}
		if h.AuthMethod != HTTPAuthBasic {
			h.Username = ""
		}
		if h.CaddyFileLimit == 0 {
			h.CaddyFileLimit = DefaultCaddyFileLimit
		}
		return Config{HTTP: &h}
	default:
		return c
	}
}

// canonicalHTTPBaseURL 把 BaseURL 归一为 canonical form：去首尾空白、
// scheme 与 host 小写、path 补齐尾 /（dot segment 不在此清理——
// 非 clean path 由 ValidateConfig 拒绝，归一化不静默改写 namespace）。
// 不合法输入（scheme / host 缺失、query / fragment 等）原样返回，
// 由 ValidateConfig 拒绝——归一化不得先于校验吞掉错误形态。
func canonicalHTTPBaseURL(raw string) string {
	trimmed := strings.TrimSpace(raw)
	u, err := url.Parse(trimmed)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return trimmed
	}
	// %2F 等经解码改变层级的编码（RawPath 非空）同样原样返回：
	// 归一化清掉 RawPath 会把 "%2F" 静默变成真实分隔符，必须由
	// ValidateConfig 拒绝而不是重写。
	if u.RawQuery != "" || u.Fragment != "" || u.User != nil || u.RawPath != "" {
		return trimmed
	}
	u.Scheme = strings.ToLower(u.Scheme)
	u.Host = strings.ToLower(u.Host)
	u.Path = withTrailingSlash(u.Path)
	// 清空 RawPath 后 String() 按解码 path 重新转义，形态唯一。
	u.RawPath = ""
	return u.String()
}

// withTrailingSlash 返回以 / 结尾的绝对 path；空 path 视为根 "/"。
func withTrailingSlash(p string) string {
	if p == "" {
		return "/"
	}
	if strings.HasSuffix(p, "/") {
		return p
	}
	return p + "/"
}

// Package auth 承载认证领域：单一 Local Admin 的密码凭据、Web Session
// 与 scoped API Token 的模型、存储抽象与应用服务。REST / Web UI / CLI
// / 未来 MCP 共用该服务层，认证逻辑不进入 HTTP middleware。
package auth

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"
)

// Scope 是 API Token 的授权范围。语义冻结：admin ⇒ read + run；
// run ⇏ read；read ⇏ run。
type Scope string

const (
	// ScopeRead 允许查询 Source / Job / Run / File / Published
	// metadata 并下载文件。
	ScopeRead Scope = "read"
	// ScopeRun 允许手动触发 Job。
	ScopeRun Scope = "run"
	// ScopeAdmin 拥有全部权限（read + run + 配置修改 + Token 管理）。
	ScopeAdmin Scope = "admin"
)

// scopes 是全部合法 scope；新增 scope 必须先修订实现契约文档。
var scopes = map[Scope]bool{
	ScopeRead:  true,
	ScopeRun:   true,
	ScopeAdmin: true,
}

// Valid 报告 scope 是否为已定义的合法值。
func (s Scope) Valid() bool {
	return scopes[s]
}

// PrincipalKind 是 principal 的凭据来源。
type PrincipalKind string

const (
	// PrincipalKindWebSession 表示浏览器 Web Session（admin 身份）。
	PrincipalKindWebSession PrincipalKind = "web_session"
	// PrincipalKindAPIToken 表示 Bearer API Token。
	PrincipalKindAPIToken PrincipalKind = "api_token"
)

// AdminSubject 是唯一管理身份，逻辑上固定，不提供 username 配置。
const AdminSubject = "admin"

// Principal 是认证后的主体：HTTP middleware 与应用服务之间的授权
// 载体。Web Session 的 principal 即 admin（scopes 为 read + run +
// admin）；API Token 的 principal 携带 token 自身 scopes。
type Principal struct {
	Kind      PrincipalKind
	SubjectID string
	Scopes    []Scope
}

// 领域错误：存储与服务层共用；HTTP 层统一映射为 401 / 403 / 400，
// 不向客户端区分具体原因。
var (
	// ErrNotFound 表示凭据或记录不存在（admin 未初始化、session /
	// token 无效等）。
	ErrNotFound = errors.New("auth: not found")
	// ErrInvalidCredentials 表示登录密码错误。
	ErrInvalidCredentials = errors.New("auth: invalid credentials")
	// ErrInvalidInput 表示输入不满足策略（密码长度、scope、过期时刻）。
	ErrInvalidInput = errors.New("auth: invalid input")
	// ErrUnauthorized 表示凭据存在但已失效（过期或撤销）。
	ErrUnauthorized = errors.New("auth: unauthorized")
)

// AdminCredential 是管理员密码凭据的单例表示；PasswordHash 为
// Argon2id PHC 完整编码值。
type AdminCredential struct {
	PasswordHash string
	Avatar       string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// WebSession 是一次浏览器登录的会话元数据；raw session token 及其
// hash 不进入领域对象，hash 由调用方单独传递给仓库。
type WebSession struct {
	ID        string
	CreatedAt time.Time
	ExpiresAt time.Time
}

// APIToken 是 API Token 的元数据表示：token_hash 与 raw token 永远
// 不出现在该结构中——list / get 只能返回这里列出的字段。
type APIToken struct {
	ID         string
	Name       string
	Prefix     string
	Scopes     []Scope
	CreatedAt  time.Time
	ExpiresAt  *time.Time
	LastUsedAt *time.Time
	RevokedAt  *time.Time
}

// Revoked 报告 token 是否已撤销。
func (t APIToken) Revoked() bool {
	return t.RevokedAt != nil
}

// Expired 报告 token 是否已过期（无过期时刻视为永不过期）。
func (t APIToken) Expired(now time.Time) bool {
	return t.ExpiresAt != nil && !now.Before(*t.ExpiresAt)
}

// Active 报告 token 当前是否可用（未撤销且未过期）。
func (t APIToken) Active(now time.Time) bool {
	return !t.Revoked() && !t.Expired(now)
}

// MarshalScopes 把 scope 集合序列化为存储格式：排序去重后的 JSON
// 数组，保证同一集合的存储表示唯一。含未知 scope 时返回
// ErrInvalidInput。
func MarshalScopes(scopesIn []Scope) (string, error) {
	normalized, err := NormalizeScopes(scopesIn)
	if err != nil {
		return "", err
	}
	data, err := json.Marshal(normalized)
	if err != nil {
		return "", fmt.Errorf("marshal scopes: %w", err)
	}
	return string(data), nil
}

// UnmarshalScopes 从存储格式还原 scope 集合；存储值损坏或含未知
// scope 返回 ErrInvalidInput。
func UnmarshalScopes(raw string) ([]Scope, error) {
	var list []Scope
	if err := json.Unmarshal([]byte(raw), &list); err != nil {
		return nil, fmt.Errorf("%w: parse scopes %q: %v", ErrInvalidInput, raw, err)
	}
	for _, s := range list {
		if !s.Valid() {
			return nil, fmt.Errorf("%w: unknown scope %q", ErrInvalidInput, s)
		}
	}
	return list, nil
}

// NormalizeScopes 校验并规范化 scope 集合：拒绝未知与空集合，
// 排序去重。API Token 创建入口必须先经此规范化再持久化。
func NormalizeScopes(scopesIn []Scope) ([]Scope, error) {
	if len(scopesIn) == 0 {
		return nil, fmt.Errorf("%w: scopes must not be empty", ErrInvalidInput)
	}
	seen := make(map[Scope]bool, len(scopesIn))
	normalized := make([]Scope, 0, len(scopesIn))
	for _, s := range scopesIn {
		if !s.Valid() {
			return nil, fmt.Errorf("%w: unknown scope %q", ErrInvalidInput, s)
		}
		if !seen[s] {
			seen[s] = true
			normalized = append(normalized, s)
		}
	}
	slices.Sort(normalized)
	return normalized, nil
}

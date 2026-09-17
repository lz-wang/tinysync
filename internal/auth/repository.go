package auth

import (
	"context"
	"time"
)

// Repository 是认证领域的存储抽象：admin 密码单例、Web Session 与
// API Token 的持久化。实现约束：
//
//   - SetAdminPassword 必须原子：替换密码 hash 与清空全部 Web
//     Session 处于同一 transaction（密码重置立即废弃所有会话）；
//   - raw session token / raw API token 绝不落库，调用方只传 hash。
type Repository interface {
	// ---- admin ----

	// AdminConfigured 报告管理员密码是否已初始化。
	AdminConfigured(ctx context.Context) (bool, error)
	// GetAdminCredential 读取密码凭据；未初始化返回 ErrNotFound。
	GetAdminCredential(ctx context.Context) (AdminCredential, error)
	// SetAdminPassword 创建或替换管理员密码并清空全部 Web Session
	//（首次 bootstrap 与 rotation / reset 共用同一入口）。
	SetAdminPassword(ctx context.Context, passwordHash string, now time.Time) error

	// ---- web session ----

	// CreateSession 持久化会话元数据与其 hash。
	CreateSession(ctx context.Context, session WebSession, sessionHash []byte) error
	// GetSessionByHash 按 hash 读取会话；不存在返回 ErrNotFound。
	// 是否过期由服务层判定。
	GetSessionByHash(ctx context.Context, sessionHash []byte) (WebSession, error)
	// DeleteSession 按 ID 删除会话（logout）。
	DeleteSession(ctx context.Context, id string) error
	// DeleteExpiredSessions 删除全部过期会话，返回删除行数。
	DeleteExpiredSessions(ctx context.Context, now time.Time) (int64, error)

	// ---- api token ----

	// CreateAPIToken 持久化 token 元数据与其 hash；token_hash 唯一。
	CreateAPIToken(ctx context.Context, token APIToken, tokenHash []byte) error
	// GetAPIToken 按 ID 读取 token；不存在返回 ErrNotFound。
	GetAPIToken(ctx context.Context, id string) (APIToken, error)
	// GetAPITokenByHash 按 hash 读取 token（认证路径）。
	GetAPITokenByHash(ctx context.Context, tokenHash []byte) (APIToken, error)
	// ListAPITokens 返回全部 token 元数据，顺序稳定。
	ListAPITokens(ctx context.Context) ([]APIToken, error)
	// RevokeAPIToken 幂等软撤销：已撤销的 token 再次撤销仍成功且
	// 不改动既有 revoked_at。
	RevokeAPIToken(ctx context.Context, id string, now time.Time) error
	// TouchAPITokenLastUsed 节流更新 last_used_at。
	TouchAPITokenLastUsed(ctx context.Context, id string, now time.Time) error
}

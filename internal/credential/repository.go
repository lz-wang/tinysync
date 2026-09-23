package credential

import (
	"context"
	"time"
)

// Repository 是 Credential 的持久化接口，由 storage 后端（SQLite）
// 实现。普通读取路径（Get / List）不得带出 secret；secret 仅随
// Create / ReplaceSecret 写入，写入后只能整体替换（无三态）。
type Repository interface {
	// Create 存储新凭据（ID 与时间戳由领域层生成）；name 冲突返回
	// ErrConflict。被引用不拦截（新凭据尚无引用者）。
	Create(ctx context.Context, c Credential, secret Secret) error

	// Get 按 ID 读取；不存在返回 ErrNotFound。
	Get(ctx context.Context, id string) (Credential, error)

	// List 返回全部凭据，按 name 大小写不敏感排序，保证列表稳定。
	List(ctx context.Context) ([]Credential, error)

	// Rename 只更新名称与 updated_at：与 secret 派生列（fingerprint /
	// has_passphrase）互不相交，并发改名与换钥不会互相覆盖。
	// name 冲突返回 ErrConflict，ID 不存在返回 ErrNotFound。
	Rename(ctx context.Context, id string, name string, updatedAt time.Time) error

	// ReplaceSecret 整体替换 secret，并在同一事务内写回派生列
	// （fingerprint / has_passphrase）：三列原子一致，回显永不指向
	// 旧钥匙。ID 不存在返回 ErrNotFound。
	ReplaceSecret(ctx context.Context, id string, secret Secret, fingerprint string, hasPassphrase bool, updatedAt time.Time) error

	// Delete 硬删除；不存在返回 ErrNotFound。被同步源引用时在同一
	// 事务内判定并返回 ErrInUse（携带引用清单）——守卫与删除原子，
	// 并发写入的引用打不穿 fail-closed 语义。
	Delete(ctx context.Context, id string) error
}

// ReferenceIndex 回答「谁在引用凭据」：引用关系存放在 Source config
// 的 credential_id 字段，由 Source 存储实现本接口，仅供列表回显引用
// 计数；删除拦截由 Repository.Delete 的事务内守卫承担。
type ReferenceIndex interface {
	// SourcesReferencingCredential 返回引用指定凭据的同步源清单。
	SourcesReferencingCredential(ctx context.Context, credentialID string) ([]SourceRef, error)

	// AllCredentialReferences 返回全部凭据的引用清单（按凭据 ID 分组），
	// 供列表回显引用计数，避免逐凭据查询。
	AllCredentialReferences(ctx context.Context) (map[string][]SourceRef, error)
}

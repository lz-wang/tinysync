package source

import (
	"context"
)

// Repository 是 Source 的持久化接口，由 storage 后端（SQLite）实现。
// 普通读取路径（Get / List）不得带出 secret；secret 仅能经凭据查询
// 获取，且只用于构造远端客户端与持久化。
type Repository interface {
	// Create 存储新 Source（ID 与时间戳由领域层生成）；
	// name 冲突返回 ErrConflict。
	Create(ctx context.Context, s Source, creds Credentials) error

	// Get 按 ID 读取；不存在返回 ErrNotFound。
	Get(ctx context.Context, id string) (Source, error)

	// List 返回全部 Source，按 name 大小写不敏感排序，保证列表稳定。
	List(ctx context.Context) ([]Source, error)

	// Update 以传入的 Source 整体替换可变字段（name / config /
	// credential state / enabled / updated_at）；creds 非 nil 时按组内
	// 三态更新 secret（nil 保留、空串清除、非空替换）。
	// ID 不存在返回 ErrNotFound，name 冲突返回 ErrConflict。
	Update(ctx context.Context, s Source, creds *CredentialsUpdate) error

	// Delete 硬删除；不存在返回 ErrNotFound。
	Delete(ctx context.Context, id string) error

	// GetPassword 返回密码明文；不存在返回 ErrNotFound。
	// v0.5 迁移后由 GetCredentials 取代。
	GetPassword(ctx context.Context, id string) (string, error)
}

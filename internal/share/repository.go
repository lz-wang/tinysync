package share

import (
	"context"
	"errors"
)

// 领域错误：API 层映射 HTTP 状态（404 / 409）；invalid 输入统一
// 归入 source.ErrInvalid（400 语义）。
var (
	ErrNotFound = errors.New("share not found")
	ErrConflict = errors.New("slug already exists")
)

// Repository 是共享策略的持久化接口。
type Repository interface {
	// Create 插入共享；slug 冲突返回 ErrConflict；ID 与时间戳由
	// 领域层生成。
	Create(ctx context.Context, s Share) error
	// Get 按 ID 读取；不存在返回 ErrNotFound。
	Get(ctx context.Context, id string) (Share, error)
	// GetBySlug 按 slug 读取；不存在返回 ErrNotFound。公开 serving
	// 与浏览的唯一入口。
	GetBySlug(ctx context.Context, slug string) (Share, error)
	// List 返回全部共享，按 slug 字典序排序。
	List(ctx context.Context) ([]Share, error)
	// Update 整体替换 slug/name/enabled/expires_at/updated_at；
	// local_path/is_dir/created_at 不动；NotFound/ErrConflict。
	Update(ctx context.Context, s Share) error
	// Delete 硬删除；不存在返回 ErrNotFound。
	Delete(ctx context.Context, id string) error
}

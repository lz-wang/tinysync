package publish

import (
	"context"
	"errors"
)

// 领域错误：API 层映射 HTTP 状态（404 / 409）；invalid 输入统一
// 归入 source.ErrInvalid（400 语义）。
var (
	// ErrNotFound 表示 PublishedFile 不存在。
	ErrNotFound = errors.New("published file not found")
	// ErrConflict 表示 public_path 与现有 Policy 冲突。
	ErrConflict = errors.New("public path already exists")
)

// Repository 是 PublishedFile 的持久化接口，由 storage 后端（SQLite）
// 实现。
type Repository interface {
	// Create 存储新 Policy（ID 与时间戳由领域层生成）；
	// public_path 冲突返回 ErrConflict。
	Create(ctx context.Context, p PublishedFile) error

	// Get 按 ID 读取；不存在返回 ErrNotFound。
	Get(ctx context.Context, id string) (PublishedFile, error)

	// GetByPublicPath 按公开路径读取；不存在返回 ErrNotFound。
	// serving 路径的唯一入口。
	GetByPublicPath(ctx context.Context, publicPath string) (PublishedFile, error)

	// List 返回全部 Policy，按 public_path 字典序排序，保证列表稳定。
	List(ctx context.Context) ([]PublishedFile, error)

	// Update 整体替换可变字段（public_path / enabled / expires_at /
	// updated_at）；local_path 与 created_at 不被触碰。
	// ID 不存在返回 ErrNotFound，public_path 冲突返回 ErrConflict。
	Update(ctx context.Context, p PublishedFile) error

	// Delete 硬删除；不存在返回 ErrNotFound。
	Delete(ctx context.Context, id string) error
}

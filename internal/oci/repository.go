package oci

import (
	"context"
)

// Repository 是 registries 表的持久化接口。
type Repository interface {
	// Create 落库新行；name 冲突（COLLATE NOCASE）返回 ErrRegistryConflict。
	Create(ctx context.Context, r Registry) error
	// Get 按 ID 读取；不存在返回 ErrRegistryNotFound。
	Get(ctx context.Context, id string) (Registry, error)
	// List 返回全部行（按 name 大小写不敏感排序）。
	List(ctx context.Context) ([]Registry, error)
	// Update 整体替换可变字段；目标不存在返回 ErrRegistryNotFound。
	Update(ctx context.Context, r Registry) error
	// Delete 硬删除；目标不存在返回 ErrRegistryNotFound。
	Delete(ctx context.Context, id string) error
	// LatestImageStates 返回每个 (oci 任务, 源镜像) 最近一次执行的聚合
	// 状态（GET /images 的数据来源）；jobIDs 为空返回空表。
	LatestImageStates(ctx context.Context, jobIDs []string) (map[string]ImageState, error)
}

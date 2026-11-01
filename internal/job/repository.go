package job

import (
	"context"
	"database/sql"
)

// TxHook 随 Job 更新在同一事务内执行：领域执行器借它把「配置变更」与
// 领域状态重置（如 File mapping 变更清空 managed_files）做成原子操作。
type TxHook func(ctx context.Context, tx *sql.Tx) error

// Repository 是 jobs 的持久化接口。
type Repository interface {
	// Create 落库新 Job；name 冲突返回 ErrConflict。
	Create(ctx context.Context, job Job) error
	// Get 按 ID 读取；不存在时返回 ErrNotFound。
	Get(ctx context.Context, id string) (Job, error)
	// List 返回全部 Job（按 name 大小写不敏感排序）。
	List(ctx context.Context) ([]Job, error)
	// ListByKind 返回指定类型的全部 Job。
	ListByKind(ctx context.Context, kind JobKind) ([]Job, error)
	// Update 整体替换可变字段（name / enabled / config / schedule /
	// once 消费状态），hooks 在同一事务内随更新执行；目标不存在返回
	// ErrNotFound。next_run_at 随 schedule 变化重算。
	Update(ctx context.Context, job Job, hooks ...TxHook) error
	// Delete 硬删除 Job；领域状态表经 FK CASCADE 清理。
	Delete(ctx context.Context, id string) error
	// CountBySource 统计 config 引用给定 Source 的 file 任务数，供
	// Source 删除保护使用（source_id 位于 File 的 config_json 内）。
	CountBySource(ctx context.Context, sourceID string) (int, error)
}

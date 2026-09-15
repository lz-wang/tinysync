package syncjob

import (
	"context"
	"time"

	"tinysync/internal/source"
)

// ManagedState 是 managed file 的同步状态：pending 表示已登记待传输，
// synced 表示本地文件已确认落地。
type ManagedState string

// managed_files.state 的枚举值。
const (
	StatePending ManagedState = "pending"
	StateSynced  ManagedState = "synced"
)

// ManagedFile 是 managed_files 的一行：Job 对一个远端文件的本地占有记录。
// RemotePath 是 Source-relative logical path（/ 开头）；LocalRelPath 是
// 相对 Job.LocalRoot 的路径（统一 / 分隔）；绝对本地路径永远实时解析，
// 不落库。Remote 指纹用于下一轮变更判定。
type ManagedFile struct {
	JobID        string
	RemotePath   string
	LocalRelPath string
	State        ManagedState
	Remote       source.Fingerprint
	LocalSize    *int64
	LocalMtimeNs *int64
	UpdatedAt    time.Time
}

// Repository 是 Sync Job 的持久化接口。
type Repository interface {
	// Create 插入 Job；name 冲突时返回 ErrConflict。
	Create(ctx context.Context, job Job) error
	// Get 按 ID 读取；不存在时返回 ErrNotFound。
	Get(ctx context.Context, id string) (Job, error)
	// List 返回全部 Job，按 name 大小写不敏感排序。
	List(ctx context.Context) ([]Job, error)
	// Update 整体替换可变字段；不存在时返回 ErrNotFound。
	Update(ctx context.Context, job Job) error
	// Delete 硬删除 Job（managed metadata 由 FK CASCADE 清理）；
	// 不存在时返回 ErrNotFound。
	Delete(ctx context.Context, id string) error
	// CountBySource 统计引用给定 Source 的 Job 数，用于 Source 删除保护。
	CountBySource(ctx context.Context, sourceID string) (int, error)
}

// ManagedRepository 是 managed_files 的持久化接口。
// 批量操作在实现内部保持原子性，供同步引擎在单个事务边界内推进状态。
type ManagedRepository interface {
	// ListByJob 返回 Job 的全部 managed 记录，按 remote_path 排序。
	ListByJob(ctx context.Context, jobID string) ([]ManagedFile, error)
	// Upsert 按 (job_id, remote_path) 插入或更新记录。
	Upsert(ctx context.Context, files []ManagedFile) error
	// Delete 删除指定 remote_path 的记录。
	Delete(ctx context.Context, jobID string, remotePaths []string) error
	// DeleteAllForJob 删除 Job 的全部 managed 记录。
	DeleteAllForJob(ctx context.Context, jobID string) error
}

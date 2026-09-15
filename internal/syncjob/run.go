package syncjob

import (
	"context"
	"time"
)

// RunTrigger 是一轮运行的触发方式：手动或三种调度类型。
type RunTrigger string

// sync_runs.trigger_type 的枚举值。
const (
	TriggerManual   RunTrigger = "manual"
	TriggerOnce     RunTrigger = "once"
	TriggerInterval RunTrigger = "interval"
	TriggerCron     RunTrigger = "cron"
)

// Valid 判断触发方式是否为受支持的枚举值。
func (t RunTrigger) Valid() bool {
	switch t {
	case TriggerManual, TriggerOnce, TriggerInterval, TriggerCron:
		return true
	}
	return false
}

// RunItemAction 是文件级变更动作。
type RunItemAction string

// sync_run_items.action 的枚举值：create / update 为下载与覆盖，
// delete 为 Mirror 的 managed 删除，relinquish 为释放授权保留本地。
const (
	ItemCreate     RunItemAction = "create"
	ItemUpdate     RunItemAction = "update"
	ItemDelete     RunItemAction = "delete"
	ItemRelinquish RunItemAction = "relinquish"
)

// Valid 判断动作是否为受支持的枚举值。
func (a RunItemAction) Valid() bool {
	switch a {
	case ItemCreate, ItemUpdate, ItemDelete, ItemRelinquish:
		return true
	}
	return false
}

// RunItemStatus 是文件级变更结果。
type RunItemStatus string

// sync_run_items.status 的枚举值。
const (
	ItemSucceeded RunItemStatus = "succeeded"
	ItemFailed    RunItemStatus = "failed"
	ItemSkipped   RunItemStatus = "skipped"
)

// Valid 判断结果是否为受支持的枚举值。
func (s RunItemStatus) Valid() bool {
	switch s {
	case ItemSucceeded, ItemFailed, ItemSkipped:
		return true
	}
	return false
}

// RunRecord 是 sync_runs 的一行：一轮同步的持久化摘要，运行状态的唯一
// 事实来源（进程重启后仍可查询）。
type RunRecord struct {
	ID      string
	JobID   string
	Trigger RunTrigger
	// ScheduledFor 是计划触发的 occurrence 时间；手动运行为 nil。
	// 同一 (Job, Trigger, ScheduledFor) 只消费一次，是调度幂等依据。
	ScheduledFor *time.Time
	State        RunState // running | succeeded | failed | skipped
	StartedAt    time.Time
	FinishedAt   *time.Time
	Stats        RunStats
	// Error 是失败原因或 skipped 原因；成功时为空。
	Error string
}

// RunItem 是 sync_run_items 的一行：文件级变更明细。只为变化、冲突与
// 失败写行，unchanged 文件只累计 summary。Path 相对 Job.LocalRoot，
// 统一 / 分隔。
type RunItem struct {
	ID     int64
	RunID  string
	Path   string
	Action RunItemAction
	Status RunItemStatus
	Bytes  int64
	Error  string
}

// RetentionRunsPerJob 是每 Job 保留的最近 run 数（超出由 PruneRetention
// 连同 items 级联清理），避免长期运行的 SQLite 无限膨胀。
const RetentionRunsPerJob = 500

// RunFilter 是运行历史的查询过滤与分页参数。
type RunFilter struct {
	// JobID 为空表示全部 Job。
	JobID string
	// Status 为空表示全部状态。
	Status RunState
	Limit  int
	Offset int
}

// RunRepository 是 sync_runs / sync_run_items 的持久化接口。
// 实现需保证 Finalize 与 AppendItem 的失败语义：不产生半成功状态。
type RunRepository interface {
	// Insert 整体落库运行记录：正常运行带 running 状态且必须在同步
	// goroutine 启动前调用成功（保证「已开始修改本地文件却没有历史
	// run」的状态不存在）；调度跳过的记录直接携带 skipped 终态与原因。
	Insert(ctx context.Context, run RunRecord) error
	// Finalize 以 run 的 State / Stats / FinishedAt / Error 覆盖运行终态；
	// 目标 run 不存在时返回 ErrRunUnknown。
	Finalize(ctx context.Context, run RunRecord) error
	// Get 按 ID 读取；不存在时返回 ErrRunUnknown。
	Get(ctx context.Context, runID string) (RunRecord, error)
	// Latest 返回 Job 最近一条 run（按开始时间倒序）；无记录时返回
	// ErrRunUnknown。
	Latest(ctx context.Context, jobID string) (RunRecord, error)
	// List 按过滤条件分页返回 run（开始时间倒序），total 为过滤后总数。
	List(ctx context.Context, filter RunFilter) ([]RunRecord, int, error)
	// AppendItem 追加一条文件级明细。
	AppendItem(ctx context.Context, item RunItem) error
	// Items 分页返回 run 的文件明细（按写入顺序），total 为该 run 明细总数。
	Items(ctx context.Context, runID string, limit, offset int) ([]RunItem, int, error)
	// HasRunFor 判定 Job 的某触发器在指定 occurrence 是否已有 run 记录
	// （含 skipped）：存在即已消费，调度不再重复触发。
	HasRunFor(ctx context.Context, jobID string, trigger RunTrigger, scheduledFor time.Time) (bool, error)
	// FailStaleRunning 把遗留的 running 记录收敛为 failed（进程异常退出
	// 恢复），返回收敛行数。
	FailStaleRunning(ctx context.Context, finishedAt time.Time, reason string) (int64, error)
	// PruneRetention 每 Job 只保留最近 keepPerJob 条 run，更早的连同
	// items 级联删除。
	PruneRetention(ctx context.Context, keepPerJob int) error
}

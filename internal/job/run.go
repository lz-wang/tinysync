package job

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// RunTrigger 是一轮运行的触发方式：手动或三种调度类型。
type RunTrigger string

// job_runs.trigger_type 的枚举值。
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

// RunState 是运行的状态机取值。running → succeeded | failed | canceled；
// canceled 专指用户手动停止；Shutdown 等非用户意愿的中断仍记 failed。
// skipped 是调度触发但未执行（同任务重叠等）的持久化记录。idle 不是
// 持久化状态，仅表示「从未运行」的查询占位。
type RunState string

// 运行状态机取值。运行记录持久化于 job_runs，重启后仍可查询。
const (
	RunIdle      RunState = "idle"
	RunRunning   RunState = "running"
	RunSucceeded RunState = "succeeded"
	RunFailed    RunState = "failed"
	RunSkipped   RunState = "skipped"
	RunCanceled  RunState = "canceled"
)

// 运行领域哨兵错误。
var (
	// ErrRunUnknown 表示查询的运行记录不存在。
	ErrRunUnknown = errors.New("run not found")
	// ErrRunCanceled 是用户手动停止运行的取消原因（WithCancelCause）。
	// finalize 据此把终态收敛为 canceled 而非 failed；同一取值也是
	// canceled run 的 error 文案。
	ErrRunCanceled = errors.New("run canceled by user")
	// ErrRunNotActive 表示目标 run 不在进行中（不存在或已终态）。
	ErrRunNotActive = errors.New("run not active")
)

// RunItemAction 是一条运行明细的动作。公共层只把它当作领域定义的
// 字符串（file: create/update/delete/relinquish；oci: copy/skip 等），
// 不做统一枚举约束。
type RunItemAction string

// File 领域的动作取值（迁移自原 sync_run_items 语义）。
const (
	ItemCreate     RunItemAction = "create"
	ItemUpdate     RunItemAction = "update"
	ItemDelete     RunItemAction = "delete"
	ItemRelinquish RunItemAction = "relinquish"
)

// Valid 判断动作是否为受支持的枚举值（File 领域）。
func (a RunItemAction) Valid() bool {
	switch a {
	case ItemCreate, ItemUpdate, ItemDelete, ItemRelinquish:
		return true
	}
	return false
}

// RunItemStatus 是运行明细的结果。
type RunItemStatus string

// job_run_items.status 的枚举值。canceled 表示明细在传输中被用户手动
// 停止中断（Shutdown 中断仍记 failed，与 run 级状态语义一致）。
const (
	ItemSucceeded RunItemStatus = "succeeded"
	ItemFailed    RunItemStatus = "failed"
	ItemSkipped   RunItemStatus = "skipped"
	ItemCanceled  RunItemStatus = "canceled"
)

// Valid 判断结果是否为受支持的枚举值。
func (s RunItemStatus) Valid() bool {
	switch s {
	case ItemSucceeded, ItemFailed, ItemSkipped, ItemCanceled:
		return true
	}
	return false
}

// RunRecord 是 job_runs 的一行：一轮运行的持久化摘要，运行状态的唯一
// 事实来源（进程重启后仍可查询）。
type RunRecord struct {
	ID string
	// JobKind 是任务类型快照（file / oci / git），落库于 job_runs.job_kind。
	JobKind JobKind
	JobID   string
	Trigger RunTrigger
	// ScheduledFor 是计划触发的 occurrence 时间；手动运行为 nil。
	// 同一 (Job, Trigger, ScheduledFor) 只消费一次，是调度幂等依据。
	ScheduledFor *time.Time
	State        RunState // running | succeeded | failed | skipped | canceled
	StartedAt    time.Time
	FinishedAt   *time.Time
	// Result 是领域结果摘要的原始 JSON（job_runs.result_json）：
	// file 为文件统计、oci 为镜像复制摘要；running / skipped 为空。
	Result json.RawMessage
	// Error 是失败原因或 skipped 原因；成功时为空。
	Error string
}

// RunItem 是 job_run_items 的一行：一条运行明细。Target 是统一的
// 展示目标（file 为相对 LocalRoot 的 / 分隔路径，oci 为镜像引用），
// 领域扩展字段（平台、目标仓库等）放 Detail。只为变化、冲突与失败
// 写行，unchanged 项只累计进 RunRecord.Result。
type RunItem struct {
	ID     int64
	RunID  string
	Target string
	Action RunItemAction
	Status RunItemStatus
	Bytes  int64
	Error  string
	// Detail 是领域扩展字段的原始 JSON；nil 表示无扩展字段。
	Detail json.RawMessage
}

// RetentionRunsPerJob 是每 Job 保留的最近 run 数（超出由 PruneRetention
// 连同 items 级联清理），避免长期运行的 SQLite 无限膨胀。
const RetentionRunsPerJob = 500

// RunFilter 是运行历史的查询过滤与分页参数。
type RunFilter struct {
	// JobID 为空表示全部 Job。
	JobID string
	// Kind 为空表示全部任务类型。
	Kind JobKind
	// Status 为空表示全部状态。
	Status RunState
	Limit  int
	Offset int
}

// RunRepository 是 job_runs / job_run_items 的持久化接口。
// 实现需保证 Finalize 与 AppendItem 的失败语义：不产生半成功状态。
type RunRepository interface {
	// Insert 整体落库运行记录：正常运行带 running 状态且必须在运行
	// goroutine 启动前调用成功（保证「已开始修改本地文件却没有历史
	// run」的状态不存在）；调度跳过的记录直接携带 skipped 终态与原因。
	Insert(ctx context.Context, run RunRecord) error
	// PersistScheduledRun 以单个事务落库调度触发的运行记录：trigger
	// 为 once 且携带 occurrence 时，同一事务内写入 Job 的 once 消费
	// 状态——「run 存在 ⇔ occurrence 已消费」，不存在中间状态；
	// 非 once 触发只落历史，不触碰消费状态。
	PersistScheduledRun(ctx context.Context, run RunRecord) error
	// Finalize 以 run 的 State / Result / FinishedAt / Error 覆盖运行终态；
	// 目标 run 不存在时返回 ErrRunUnknown。
	Finalize(ctx context.Context, run RunRecord) error
	// Get 按 ID 读取；不存在时返回 ErrRunUnknown。
	Get(ctx context.Context, runID string) (RunRecord, error)
	// Latest 返回 Job 最近一条 run（按开始时间倒序）；无记录时返回
	// ErrRunUnknown。
	Latest(ctx context.Context, jobID string) (RunRecord, error)
	// List 按过滤条件分页返回 run（开始时间倒序），total 为过滤后总数。
	List(ctx context.Context, filter RunFilter) ([]RunRecord, int, error)
	// AppendItem 追加一条运行明细。
	AppendItem(ctx context.Context, item RunItem) error
	// Items 分页返回 run 的明细（按写入顺序），total 为该 run 明细总数。
	Items(ctx context.Context, runID string, limit, offset int) ([]RunItem, int, error)
	// HasRunFor 判定 Job 的某触发器在指定 occurrence 是否已有 run 记录
	//（含 skipped）：存在即已消费，调度不再重复触发。
	HasRunFor(ctx context.Context, jobID string, trigger RunTrigger, scheduledFor time.Time) (bool, error)
	// FailStaleRunning 把遗留的 running 记录收敛为 failed（进程异常退出
	// 恢复），返回收敛行数。
	FailStaleRunning(ctx context.Context, finishedAt time.Time, reason string) (int64, error)
	// PruneRetention 每 Job 只保留最近 keepPerJob 条 run，更早的连同
	// items 级联删除。
	PruneRetention(ctx context.Context, keepPerJob int) error
}

// MarshalResult 把领域结果摘要编码为 Result JSON；nil 返回 nil。
func MarshalResult(v any) (json.RawMessage, error) {
	if v == nil {
		return nil, nil
	}
	data, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("marshal run result: %w", err)
	}
	return json.RawMessage(data), nil
}

// Package syncjob 承载文件同步引擎领域：扫描、Selector、同步计划与
// 引擎、断点续传、Mirror 安全删除、managed metadata 与 File 任务配置
// 校验（job.ChangeAwareExecutor 的实现方）。
// 统一任务模型（jobs 表、调度、运行历史）见 internal/job；本包的
// Job 是引擎执行规格（spec），由 File 配置推导。
package syncjob

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"tinysync/internal/job"
)

// KindFile 与统一任务类型的 file 取值一致。
const KindFile = string(job.KindFile)

// Mode 是同步模式。
type Mode string

// 同步模式：Copy 只增不改删本地既有文件；Mirror 额外按 managed_files
// 授权删除远端已消失的本地文件。
const (
	ModeCopy   Mode = "copy"
	ModeMirror Mode = "mirror"
)

// Valid 判断模式是否为受支持的枚举值。
func (m Mode) Valid() bool {
	return m == ModeCopy || m == ModeMirror
}

// File 领域哨兵错误：统一 job 包承载 NotFound / Conflict / Invalid /
// KindUnsupported；以下为文件领域专属。
var (
	// ErrSourceInUse 表示 Source 仍被 File 任务引用，不能删除。
	ErrSourceInUse = errors.New("source is referenced by sync jobs")
	// ErrRootOverlap 表示 LocalRoot 与其他任务或数据目录重叠。
	ErrRootOverlap = errors.New("local root overlaps another local root")
)

// 领域错误别名：引擎与 API 层沿用本包名判定统一任务 / 运行错误。
var (
	ErrNotFound   = job.ErrNotFound
	ErrConflict   = job.ErrConflict
	ErrInvalid    = job.ErrInvalid
	ErrRunUnknown = job.ErrRunUnknown
	// ErrRunCanceled 是用户手动停止运行的取消原因（引擎与 Runner 共用）。
	ErrRunCanceled  = job.ErrRunCanceled
	ErrRunNotActive = job.ErrRunNotActive
)

// Job 是文件同步引擎的执行规格：把 Source 的 RemoteRoot 子树单向同步
// 到 LocalRoot。Include / Exclude 是相对 RemoteRoot 的 doublestar
// pattern，统一以 / 分隔。统一任务模型（internal/job）经 FileConfig
// 推导出本规格后交给引擎；Enabled / Schedule 等公共字段不属于引擎
// 语义，仅为兼容既有测试构造保留。
type Job struct {
	ID         string
	Name       string
	SourceID   string
	RemoteRoot string
	LocalRoot  string
	Mode       Mode
	Include    []string
	Exclude    []string
	Enabled    bool
	Schedule   Schedule
	// OnceConsumedFor 是 once 调度的消费状态快照（引擎不消费）。
	OnceConsumedFor *time.Time
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// Schedule 是 job.Schedule 的别名（引擎规格沿用本包名）。
type Schedule = job.Schedule

// ScheduleType 与调度类型枚举别名。
type ScheduleType = job.ScheduleType

const (
	ScheduleManual   = job.ScheduleManual
	ScheduleOnce     = job.ScheduleOnce
	ScheduleInterval = job.ScheduleInterval
	ScheduleCron     = job.ScheduleCron
)

// NewID 生成统一任务 ID（job.NewID 别名）。
var NewID = job.NewID

// FileConfig 是 kind='file' 任务在 jobs.config_json 中的领域配置形态。
type FileConfig struct {
	SourceID   string   `json:"source_id"`
	RemoteRoot string   `json:"remote_root"`
	LocalRoot  string   `json:"local_root"`
	Mode       string   `json:"mode"`
	Include    []string `json:"include"`
	Exclude    []string `json:"exclude"`
}

// MarshalFileConfig 编码 File 配置；nil pattern 归一为空数组。
func MarshalFileConfig(sourceID, remoteRoot, localRoot string, mode Mode, include, exclude []string) (json.RawMessage, error) {
	cfg := FileConfig{
		SourceID:   sourceID,
		RemoteRoot: remoteRoot,
		LocalRoot:  localRoot,
		Mode:       string(mode),
		Include:    include,
		Exclude:    exclude,
	}
	if cfg.Include == nil {
		cfg.Include = []string{}
	}
	if cfg.Exclude == nil {
		cfg.Exclude = []string{}
	}
	data, err := json.Marshal(cfg)
	if err != nil {
		return nil, fmt.Errorf("marshal file config: %w", err)
	}
	return json.RawMessage(data), nil
}

// ParseFileConfig 解码并基础校验 File 配置（pattern 集合与 mode）。
func ParseFileConfig(data []byte) (FileConfig, error) {
	var cfg FileConfig
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" {
		trimmed = "{}"
	}
	if err := json.Unmarshal([]byte(trimmed), &cfg); err != nil {
		return FileConfig{}, fmt.Errorf("%w: parse file config: %v", ErrInvalid, err)
	}
	if cfg.Include == nil {
		cfg.Include = []string{}
	}
	if cfg.Exclude == nil {
		cfg.Exclude = []string{}
	}
	if _, err := NewSelector(cfg.Include, cfg.Exclude); err != nil {
		return FileConfig{}, err
	}
	return cfg, nil
}

// ModeOf 返回配置的同步模式；缺省 copy。
func (c FileConfig) ModeOf() Mode {
	if c.Mode == "" {
		return ModeCopy
	}
	return Mode(c.Mode)
}

// Spec 由统一任务推导引擎执行规格。
func (c FileConfig) Spec(jobID string) Job {
	return Job{
		ID:         jobID,
		SourceID:   c.SourceID,
		RemoteRoot: c.RemoteRoot,
		LocalRoot:  c.LocalRoot,
		Mode:       c.ModeOf(),
		Include:    c.Include,
		Exclude:    c.Exclude,
	}
}

// 统一运行类型的别名：引擎与测试沿用本包名。
type (
	RunTrigger    = job.RunTrigger
	RunState      = job.RunState
	RunItemAction = job.RunItemAction
	RunItemStatus = job.RunItemStatus
	RunItem       = job.RunItem
	RunRecord     = job.RunRecord
	RunFilter     = job.RunFilter
)

// 触发方式与状态枚举别名。
const (
	TriggerManual   = job.TriggerManual
	TriggerOnce     = job.TriggerOnce
	TriggerInterval = job.TriggerInterval
	TriggerCron     = job.TriggerCron

	RunIdle      = job.RunIdle
	RunRunning   = job.RunRunning
	RunSucceeded = job.RunSucceeded
	RunFailed    = job.RunFailed
	RunSkipped   = job.RunSkipped
	RunCanceled  = job.RunCanceled

	ItemCreate     = job.ItemCreate
	ItemUpdate     = job.ItemUpdate
	ItemDelete     = job.ItemDelete
	ItemRelinquish = job.ItemRelinquish

	ItemSucceeded = job.ItemSucceeded
	ItemFailed    = job.ItemFailed
	ItemSkipped   = job.ItemSkipped
	ItemCanceled  = job.ItemCanceled

	RetentionRunsPerJob = job.RetentionRunsPerJob
)

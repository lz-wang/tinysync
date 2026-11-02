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

	"tinysync/internal/job"
)

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
	// ErrSourceDisabled 表示任务引用的 Source 已禁用：File 执行器的
	// 运行期准入检查（统一 Runner 不理解 Source，见已决 R7），失败
	// 收敛为一次 failed run。
	ErrSourceDisabled = errors.New("source is disabled")
)

// 领域错误别名：引擎与 API 层沿用本包名判定统一任务错误。
var (
	ErrNotFound = job.ErrNotFound
	ErrConflict = job.ErrConflict
	ErrInvalid  = job.ErrInvalid
	// ErrRunCanceled 是用户手动停止运行的取消原因（引擎与 Runner 共用）。
	ErrRunCanceled = job.ErrRunCanceled
)

// Job 是文件同步引擎的执行规格：把 Source 的 RemoteRoot 子树单向同步
// 到 LocalRoot。Include / Exclude 是相对 RemoteRoot 的 doublestar
// pattern，统一以 / 分隔。统一任务模型（internal/job）经 FileConfig
// 推导出本规格后交给引擎。
type Job struct {
	ID         string
	Name       string
	SourceID   string
	RemoteRoot string
	LocalRoot  string
	Mode       Mode
	Include    []string
	Exclude    []string
}

// Schedule 是 job.Schedule 的别名（引擎规格沿用本包名）。
type Schedule = job.Schedule

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
	RunItemAction = job.RunItemAction
	RunItem       = job.RunItem

	ProgressReporter = job.ProgressReporter
	TransferLimiter  = job.TransferLimiter
)

// NewTransferLimiter 是统一 Runner 侧构造函数的转发。
var NewTransferLimiter = job.NewTransferLimiter

// 运行阶段常量别名。
const (
	RunPhaseScanning     = job.RunPhaseScanning
	RunPhasePlanning     = job.RunPhasePlanning
	RunPhaseTransferring = job.RunPhaseTransferring
	RunPhaseFinalizing   = job.RunPhaseFinalizing
)

// 运行条目动作与状态枚举别名。
const (
	ItemCreate     = job.ItemCreate
	ItemUpdate     = job.ItemUpdate
	ItemDelete     = job.ItemDelete
	ItemRelinquish = job.ItemRelinquish

	ItemSucceeded = job.ItemSucceeded
	ItemFailed    = job.ItemFailed
	ItemSkipped   = job.ItemSkipped
	ItemCanceled  = job.ItemCanceled
)

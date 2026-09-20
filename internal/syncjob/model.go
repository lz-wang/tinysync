// Package syncjob 承载 Sync Job 领域：模型、Selector、远端扫描、
// 同步计划与引擎、LocalRoot 归属保护和手动运行状态。
// Job 把一个 Source 的远端子树单向同步到本地目录；
// 删除授权唯一来源是 managed_files。
package syncjob

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"
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

// 领域哨兵错误：各实现（Repository、Service、engine）必须以 errors.Is 判定。
var (
	// ErrNotFound 表示目标 Job 不存在。
	ErrNotFound = errors.New("sync job not found")
	// ErrConflict 表示 name 与现有 Job 冲突。
	ErrConflict = errors.New("sync job name already exists")
	// ErrInvalid 表示输入校验失败。
	ErrInvalid = errors.New("invalid sync job")
	// ErrSourceInUse 表示 Source 仍被 Job 引用，不能删除。
	ErrSourceInUse = errors.New("source is referenced by sync jobs")
	// ErrRootOverlap 表示 LocalRoot 与其他 Job 或数据目录重叠。
	ErrRootOverlap = errors.New("local root overlaps another local root")
)

// Job 是 Sync Job 的领域对象：把 Source 的 RemoteRoot 子树单向同步到
// LocalRoot。Include / Exclude 是相对 RemoteRoot 的 doublestar pattern，
// 统一以 / 分隔。Schedule 承载自动调度配置（见 schedule.go），
// manual（含零值）表示仅手动触发。
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
	// OnceConsumedFor 是 once 调度的持久化消费状态：occurrence 产生
	// run（succeeded/failed/skipped）后写入该 occurrence。once 的
	// 「只执行一次」语义靠它判定，与可被 retention 裁剪的运行历史
	// 解耦；非 once 调度、未执行的 once 恒为 nil。
	OnceConsumedFor *time.Time
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// idPrefix 是 Job ID 的固定前缀，便于在日志与 API 中一眼识别。
const idPrefix = "job_"

// newIDSize 是随机部分的字节数（128 bit）。
const newIDSize = 16

// NewID 生成 job_<128-bit random hex> 形式的唯一 ID，不引入 UUID 依赖。
func NewID() (string, error) {
	buf := make([]byte, newIDSize)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate sync job id: %w", err)
	}
	return idPrefix + hex.EncodeToString(buf), nil
}

// CreateInput 是创建 Job 的输入。Schedule 为 nil 时缺省 manual。
type CreateInput struct {
	Name       string
	SourceID   string
	RemoteRoot string
	LocalRoot  string
	Mode       Mode
	Include    []string
	Exclude    []string
	Enabled    bool
	Schedule   *Schedule
}

// UpdateInput 是更新 Job 的输入，指针字段区分「未提供」与「零值」：
// nil 表示保留现有值。Include / Exclude 提供 nil 时同样保留。
// Schedule 提供 nil 以外的值时原子替换整个调度配置。
type UpdateInput struct {
	Name       *string
	SourceID   *string
	RemoteRoot *string
	LocalRoot  *string
	Mode       *Mode
	Include    *[]string
	Exclude    *[]string
	Enabled    *bool
	Schedule   *Schedule
}

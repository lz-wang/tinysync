// Package job 承载统一任务领域：跨 File / OCI / Git 共用的任务模型、
// 调度配置、运行历史与应用服务。领域差异封装在各 Executor 的 JSON
// 配置（jobs.config_json）中，公共层不理解文件路径或 OCI Manifest。
package job

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// JobKind 是任务类型。
type JobKind string

// 任务类型：file / oci 可执行；git 仅为预留占位（创建被拒绝，
// 待统一任务管理稳定后开放）。
const (
	KindFile JobKind = "file"
	KindOCI  JobKind = "oci"
	KindGit  JobKind = "git"
)

// Valid 判断任务类型是否为受支持的枚举值。
func (k JobKind) Valid() bool {
	return k == KindFile || k == KindOCI || k == KindGit
}

// 领域哨兵错误：各实现（Repository、Service、Executor）必须以
// errors.Is 判定。
var (
	// ErrNotFound 表示目标 Job 不存在。
	ErrNotFound = errors.New("job not found")
	// ErrConflict 表示 name 与现有 Job 冲突。
	ErrConflict = errors.New("job name already exists")
	// ErrInvalid 表示输入校验失败。
	ErrInvalid = errors.New("invalid job")
	// ErrKindUnsupported 表示任务类型暂不支持创建（如 git 占位）
	// 或未注册执行器。
	ErrKindUnsupported = errors.New("job kind is not supported")
)

// Job 是统一任务的领域对象：Name / Enabled / Schedule 为公共字段，
// Config 是领域专用的 JSON 配置（由对应 Executor 校验与解释）。
// 调度字段（schedule_* / next_run_at / once_consumed_at）与
// jobs 表的独立 SQL 列一一对应。
type Job struct {
	ID      string
	Name    string
	Kind    JobKind
	Enabled bool
	Schedule
	// Config 是领域配置的原始 JSON；nil 视为空对象 {}。
	Config json.RawMessage
	// OnceConsumedAt 是 once 调度的持久化消费状态：occurrence 产生
	// run（succeeded/failed/skipped）后写入该 occurrence。once 的
	// 「只执行一次」语义靠它判定，与可被 retention 裁剪的运行历史
	// 解耦；非 once 调度、未执行的 once 恒为 nil。
	OnceConsumedAt *time.Time
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// idPrefix 是 Job ID 的固定前缀，便于在日志与 API 中一眼识别。
const idPrefix = "job_"

// newIDSize 是随机部分的字节数（128 bit）。
const newIDSize = 16

// NewID 生成 job_<128-bit random hex> 形式的唯一 ID，不引入 UUID 依赖。
func NewID() (string, error) {
	buf := make([]byte, newIDSize)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate job id: %w", err)
	}
	return idPrefix + hex.EncodeToString(buf), nil
}

// CreateInput 是创建 Job 的输入。Schedule 为 nil 时缺省 manual；
// Config 为 nil 时视为空对象。
type CreateInput struct {
	Name     string
	Kind     JobKind
	Enabled  bool
	Schedule *Schedule
	Config   json.RawMessage
}

// UpdateInput 是更新 Job 的输入，指针字段区分「未提供」与「零值」：
// nil 表示保留现有值；Config 提供时经执行器校验后整体替换。
type UpdateInput struct {
	Name     *string
	Enabled  *bool
	Schedule *Schedule
	Config   *json.RawMessage
}

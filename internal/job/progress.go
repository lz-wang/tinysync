package job

import (
	"sort"
	"sync"
	"sync/atomic"
)

// RunPhase 是一轮运行的生命周期阶段（transient，不持久化）。connecting
// 与 scanning / planning 之间由 Runner 与执行器的职责边界衔接：拨号归
// Runner，扫描之后的阶段边界全部在领域执行器内。
type RunPhase string

// 运行阶段的取值。扫描与计划阶段工作量未知，前端应以 indeterminate
// 进度表达；transferring 起 work_total 已确定。
const (
	RunPhaseConnecting   RunPhase = "connecting"
	RunPhaseScanning     RunPhase = "scanning"
	RunPhasePlanning     RunPhase = "planning"
	RunPhaseTransferring RunPhase = "transferring"
	RunPhaseFinalizing   RunPhase = "finalizing"
)

// UnitProgress 是一个在途工作单元（file 的文件传输、oci 的镜像复制）
// 的实时进度。字节计数走 atomic，写路径（io.Copy 回调）无锁；
// BytesTotal 取自下载前快照的指纹 size。
type UnitProgress struct {
	Target     string
	Action     RunItemAction
	BytesTotal int64
	done       atomic.Int64
	// resumeFrom 是最近一次 attempt 的传输起点（断点位置；0 表示
	// 全新传输），AttemptStart 对齐时写入。
	resumeFrom atomic.Int64
}

// BytesDone 返回当前已传输字节数（含 partial 已有前缀）。
func (f *UnitProgress) BytesDone() int64 { return f.done.Load() }

// ResumeFrom 返回最近一次 attempt 的断点起点（0 = 全新传输）。
func (f *UnitProgress) ResumeFrom() int64 { return f.resumeFrom.Load() }

// Add 累加本次写入的字节数。
func (f *UnitProgress) Add(n int64) { f.done.Add(n) }

// SetDone 把计数对齐 attempt 起点：done 与 resumeFrom 同步为 offset，
// 后续写入在其上累加——续传时 bytes_done 从断点开始（含已有前缀），
// 而不是从 0 重新计。
func (f *UnitProgress) SetDone(offset int64) {
	f.resumeFrom.Store(offset)
	f.done.Store(offset)
}

// UnitProgressSnapshot 是在途单元进度的只读快照（API 输出形态；
// wire 名沿用 active_files，file 语境下 target 即文件路径）。
// ResumeFrom 为 0（全新传输）时省略。
type UnitProgressSnapshot struct {
	Target     string `json:"target"`
	Action     string `json:"action"`
	BytesDone  int64  `json:"bytes_done"`
	BytesTotal int64  `json:"bytes_total"`
	ResumeFrom int64  `json:"resume_from,omitempty"`
}

// RunProgressSnapshot 是一轮运行进度的只读快照（API 输出形态）。
// 在途单元按 target 排序，输出稳定。
type RunProgressSnapshot struct {
	Phase       RunPhase               `json:"phase"`
	WorkDone    int64                  `json:"work_done"`
	WorkTotal   int64                  `json:"work_total"`
	ActiveUnits []UnitProgressSnapshot `json:"active_files"`
}

// RunProgress 是一轮运行的进程内实时进度（transient）：阶段、工作量
// 计数与在途单元。并发安全——阶段与计数在协调者 goroutine 串行更新，
// 在途单元由 worker 注册 / 注销，字节计数走 UnitProgress 的 atomic。
// 与持久化的 RunRecord.Result（历史审计事实）是两个概念：进度随运行
// 结束销毁，不写 SQLite。
type RunProgress struct {
	mu        sync.Mutex
	phase     RunPhase
	workTotal int64
	workDone  int64
	units     map[string]*UnitProgress
}

// NewRunProgress 构造处于 connecting 阶段的空进度。
func NewRunProgress() *RunProgress {
	return &RunProgress{
		phase: RunPhaseConnecting,
		units: make(map[string]*UnitProgress),
	}
}

// SetPhase 推进运行阶段。
func (p *RunProgress) SetPhase(phase RunPhase) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.phase = phase
}

// SetWorkTotal 在计划完成后设置工作量分母（整轮只设置一次）。
func (p *RunProgress) SetWorkTotal(total int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.workTotal = total
}

// AddWorkDone 在工作项真正收敛（成功 / 跳过 / 释放 / 删除完成）后
// 递增完成数。失败与取消中断的工作项不递增。
func (p *RunProgress) AddWorkDone(n int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.workDone += n
}

// BeginUnit 登记一个在途工作单元并返回其进度句柄（传输回调据此累加
// 字节）。同一 target 重复登记以最后一次为准（计划内唯一）。
func (p *RunProgress) BeginUnit(target string, action RunItemAction, totalBytes int64) *UnitProgress {
	up := &UnitProgress{Target: target, Action: action, BytesTotal: totalBytes}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.units[target] = up
	return up
}

// EndUnit 注销在途单元：无论成功、失败还是取消中断，收敛后即移除。
func (p *RunProgress) EndUnit(target string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.units, target)
}

// Snapshot 返回进度的只读快照。
func (p *RunProgress) Snapshot() RunProgressSnapshot {
	p.mu.Lock()
	defer p.mu.Unlock()
	snap := RunProgressSnapshot{
		Phase:     p.phase,
		WorkDone:  p.workDone,
		WorkTotal: p.workTotal,
	}
	if len(p.units) > 0 {
		snap.ActiveUnits = make([]UnitProgressSnapshot, 0, len(p.units))
		for _, up := range p.units {
			snap.ActiveUnits = append(snap.ActiveUnits, UnitProgressSnapshot{
				Target:     up.Target,
				Action:     string(up.Action),
				BytesDone:  up.BytesDone(),
				BytesTotal: up.BytesTotal,
				ResumeFrom: up.ResumeFrom(),
			})
		}
		sort.Slice(snap.ActiveUnits, func(i, j int) bool {
			return snap.ActiveUnits[i].Target < snap.ActiveUnits[j].Target
		})
	}
	return snap
}

// ProgressReporter 是领域执行器向 Runner 回报运行进度的事件接口：
// Runner 注入 activeRun 的 RunProgress，独立调用（测试、benchmark）
// 不注入——nil 经 NullProgress 包装为 no-op，引擎代码路径不分支。
type ProgressReporter interface {
	SetPhase(phase RunPhase)
	SetWorkTotal(total int64)
	AddWorkDone(n int64)
	// BeginUnit 登记在途单元并返回字节计数句柄；EndUnit 在传输收敛
	// 后注销。句柄可能被传输回调并发读写（atomic），不得复制。
	BeginUnit(target string, action RunItemAction, totalBytes int64) *UnitProgress
	EndUnit(target string)
}

// NullProgress 把可为 nil 的 ProgressReporter 规整为非 nil 实现。
func NullProgress(p ProgressReporter) ProgressReporter {
	if p != nil {
		return p
	}
	return nullProgress{}
}

// nullProgress 是 ProgressReporter 的 no-op 实现：BeginUnit 返回
// 无人观察的丢弃句柄，Add/SetDone 的开销为零值 atomic 操作。
type nullProgress struct{}

func (nullProgress) SetPhase(RunPhase) {}

func (nullProgress) SetWorkTotal(int64) {}

func (nullProgress) AddWorkDone(int64) {}

func (nullProgress) EndUnit(string) {}

func (nullProgress) BeginUnit(target string, action RunItemAction, totalBytes int64) *UnitProgress {
	return &UnitProgress{Target: target, Action: action, BytesTotal: totalBytes}
}

// 编译期接口断言。
var (
	_ ProgressReporter = (*RunProgress)(nil)
	_ ProgressReporter = nullProgress{}
)

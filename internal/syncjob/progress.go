package syncjob

import (
	"sort"
	"sync"
	"sync/atomic"
)

// RunPhase 是一轮运行的生命周期阶段（transient，不持久化）。connecting
// 与 scanning / planning 之间由 Runner 与引擎的职责边界衔接：拨号归
// Runner，扫描之后的阶段边界全部在引擎内。
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

// FileProgress 是一个在途文件的实时传输进度。字节计数走 atomic，
// 写路径（io.Copy 回调）无锁；BytesTotal 取自下载前快照的指纹 size。
type FileProgress struct {
	Path       string
	Action     RunItemAction
	BytesTotal int64
	done       atomic.Int64
}

// BytesDone 返回当前已传输字节数。
func (f *FileProgress) BytesDone() int64 { return f.done.Load() }

// add 累加本次写入的字节数。
func (f *FileProgress) add(n int64) { f.done.Add(n) }

// reset 归零当前 attempt 的计数（重试从头传输，不能跨 attempt 累加）。
func (f *FileProgress) reset() { f.done.Store(0) }

// FileProgressSnapshot 是在途文件进度的只读快照（API 输出形态）。
type FileProgressSnapshot struct {
	Path       string `json:"path"`
	Action     string `json:"action"`
	BytesDone  int64  `json:"bytes_done"`
	BytesTotal int64  `json:"bytes_total"`
}

// RunProgressSnapshot 是一轮运行进度的只读快照（API 输出形态）。
// ActiveFiles 按 path 排序，输出稳定。
type RunProgressSnapshot struct {
	Phase       RunPhase               `json:"phase"`
	WorkDone    int64                  `json:"work_done"`
	WorkTotal   int64                  `json:"work_total"`
	ActiveFiles []FileProgressSnapshot `json:"active_files"`
}

// RunProgress 是一轮运行的进程内实时进度（transient）：阶段、工作量
// 计数与在途文件。并发安全——阶段与计数在协调者 goroutine 串行更新，
// 在途文件由 worker 注册 / 注销，字节计数走 FileProgress 的 atomic。
// 与持久化的 RunStats（历史审计事实）是两个概念：进度随运行结束销毁，
// 不写 SQLite。
type RunProgress struct {
	mu          sync.Mutex
	phase       RunPhase
	workTotal   int64
	workDone    int64
	activeFiles map[string]*FileProgress
}

// NewRunProgress 构造处于 connecting 阶段的空进度。
func NewRunProgress() *RunProgress {
	return &RunProgress{
		phase:       RunPhaseConnecting,
		activeFiles: make(map[string]*FileProgress),
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

// BeginFile 登记一个在途文件并返回其进度句柄（下载回调据此累加字节）。
// 同一路径重复登记以最后一次为准（计划内路径唯一，正常不发生）。
func (p *RunProgress) BeginFile(path string, action RunItemAction, totalBytes int64) *FileProgress {
	fp := &FileProgress{Path: path, Action: action, BytesTotal: totalBytes}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.activeFiles[path] = fp
	return fp
}

// EndFile 注销在途文件：无论成功、失败还是取消中断，收敛后即移除。
func (p *RunProgress) EndFile(path string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.activeFiles, path)
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
	if len(p.activeFiles) > 0 {
		snap.ActiveFiles = make([]FileProgressSnapshot, 0, len(p.activeFiles))
		for _, fp := range p.activeFiles {
			snap.ActiveFiles = append(snap.ActiveFiles, FileProgressSnapshot{
				Path:       fp.Path,
				Action:     string(fp.Action),
				BytesDone:  fp.BytesDone(),
				BytesTotal: fp.BytesTotal,
			})
		}
		sort.Slice(snap.ActiveFiles, func(i, j int) bool {
			return snap.ActiveFiles[i].Path < snap.ActiveFiles[j].Path
		})
	}
	return snap
}

// ProgressReporter 是引擎向调用方回报运行进度的事件接口：Runner 注入
// activeRun 的 RunProgress，独立调用（测试、benchmark）不注入——nil
// 经 nullProgress 包装为 no-op，引擎代码路径不分支。
type ProgressReporter interface {
	SetPhase(phase RunPhase)
	SetWorkTotal(total int64)
	AddWorkDone(n int64)
	// BeginFile 登记在途文件并返回字节计数句柄；EndFile 在传输收敛
	// 后注销。句柄可能被下载回调并发读写（atomic），不得复制。
	BeginFile(path string, action RunItemAction, totalBytes int64) *FileProgress
	EndFile(path string)
}

// engineProgress 把可为 nil 的 ProgressReporter 规整为非 nil 实现。
func engineProgress(p ProgressReporter) ProgressReporter {
	if p != nil {
		return p
	}
	return nullProgress{}
}

// nullProgress 是 ProgressReporter 的 no-op 实现：BeginFile 返回
// 无人观察的丢弃句柄，add/reset 的开销为零值 atomic 操作。
type nullProgress struct{}

func (nullProgress) SetPhase(RunPhase) {}

func (nullProgress) SetWorkTotal(int64) {}

func (nullProgress) AddWorkDone(int64) {}

func (nullProgress) EndFile(string) {}

func (nullProgress) BeginFile(path string, action RunItemAction, totalBytes int64) *FileProgress {
	return &FileProgress{Path: path, Action: action, BytesTotal: totalBytes}
}

// fileProgressListener 把 FileProgress 适配为 Downloader 的
// TransferListener：attempt 重启归零计数，成功写入累加。值类型即可
// （句柄语义，fp 指针共享）。
type fileProgressListener struct {
	fp *FileProgress
}

// AttemptStart 实现 TransferListener：重试从头发送，计数归零。
func (l fileProgressListener) AttemptStart() { l.fp.reset() }

// Write 实现 TransferListener：累加本次写入字节数。
func (l fileProgressListener) Write(n int64) { l.fp.add(n) }

// 编译期接口断言。
var (
	_ ProgressReporter = (*RunProgress)(nil)
	_ ProgressReporter = nullProgress{}
	_ TransferListener = fileProgressListener{}
)

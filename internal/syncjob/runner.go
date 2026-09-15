package syncjob

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"

	"tinysync/internal/source"
)

// RunState 是手动运行的状态机取值。进程重启后回到 idle——运行记录
// 只存内存，持久化历史明确属于 v0.4。
type RunState string

// 运行状态机：idle → running → succeeded | failed。
const (
	RunIdle      RunState = "idle"
	RunRunning   RunState = "running"
	RunSucceeded RunState = "succeeded"
	RunFailed    RunState = "failed"
)

// 手动运行相关错误：API 层映射为 409 等状态码。
var (
	// ErrRunActive 表示全局已有另一个同步在运行（v0.3 固定全局单运行）。
	ErrRunActive = errors.New("another sync run is active")
	// ErrJobDisabled 表示目标 Job 已禁用。
	ErrJobDisabled = errors.New("sync job is disabled")
	// ErrSourceDisabled 表示 Job 引用的 Source 已禁用。
	ErrSourceDisabled = errors.New("source is disabled")
	// ErrRunUnknown 表示查询的运行记录不存在。
	ErrRunUnknown = errors.New("run not found")
)

// SourceCredentials 是 Runner 构造远端客户端所需的凭据查询能力。
type SourceCredentials interface {
	Get(ctx context.Context, id string) (source.Source, error)
	// GetPassword 返回密码明文；无密码时为空串。
	GetPassword(ctx context.Context, id string) (string, error)
}

// RunStatus 是一次运行的内存态快照。
type RunStatus struct {
	RunID      string
	State      RunState
	StartedAt  time.Time
	FinishedAt time.Time
	Stats      RunStats
	// Error 是失败原因的人类可读描述；成功时为空。
	Error string
}

// activeRun 是进行中的一轮同步。
type activeRun struct {
	runID  string
	jobID  string
	cancel context.CancelFunc
	done   chan struct{}
	// startedAt 在 goroutine 结束时用于填充最终状态。
	startedAt time.Time
}

// Runner 管理手动触发同步的运行时状态：全局同一时刻至多一个运行、
// 每个 Job 的最近运行结果（内存）、优雅关闭。
type Runner struct {
	repo    Repository
	managed ManagedRepository
	creds   SourceCredentials
	factory source.RemoteFactory
	// Now 返回当前时间；默认 UTC time.Now，测试可注入固定时钟。
	Now func() time.Time

	mu      sync.Mutex
	current *activeRun
	last    map[string]RunStatus // jobID → 最近一次完成的状态
	wg      sync.WaitGroup
}

// NewRunner 构造 Runner。
func NewRunner(repo Repository, managed ManagedRepository, creds SourceCredentials, factory source.RemoteFactory) *Runner {
	return &Runner{
		repo:    repo,
		managed: managed,
		creds:   creds,
		factory: factory,
		Now:     func() time.Time { return time.Now().UTC() },
		last:    make(map[string]RunStatus),
	}
}

// Start 校验并异步启动一轮同步，立即返回 run ID。运行 context 独立于
// 调用方的 HTTP request context；全局已有运行、Job 或 Source 禁用时
// 拒绝启动。
func (r *Runner) Start(ctx context.Context, jobID string) (string, error) {
	job, err := r.repo.Get(ctx, jobID)
	if err != nil {
		return "", err
	}
	if !job.Enabled {
		return "", fmt.Errorf("%w: %s", ErrJobDisabled, jobID)
	}
	src, err := r.creds.Get(ctx, job.SourceID)
	if err != nil {
		return "", err
	}
	if !src.Enabled {
		return "", fmt.Errorf("%w: %s", ErrSourceDisabled, job.SourceID)
	}
	password, err := r.creds.GetPassword(ctx, job.SourceID)
	if err != nil {
		return "", err
	}
	remote, err := r.factory.Create(src, password)
	if err != nil {
		return "", fmt.Errorf("create remote for job %s: %w", jobID, err)
	}

	r.mu.Lock()
	if r.current != nil {
		r.mu.Unlock()
		return "", fmt.Errorf("%w: job %s is running", ErrRunActive, r.current.jobID)
	}
	runID, err := newRunID()
	if err != nil {
		r.mu.Unlock()
		return "", err
	}
	runCtx, cancel := context.WithCancel(context.Background())
	run := &activeRun{
		runID:     runID,
		jobID:     jobID,
		cancel:    cancel,
		done:      make(chan struct{}),
		startedAt: r.Now(),
	}
	r.current = run
	r.mu.Unlock()

	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		stats, runErr := Run(runCtx, RunOptions{Remote: remote, Job: job, Managed: r.managed})
		finishedAt := r.Now()
		final := RunStatus{
			RunID:      runID,
			StartedAt:  run.startedAt,
			FinishedAt: finishedAt,
			Stats:      stats,
		}
		if runErr != nil {
			final.State = RunFailed
			final.Error = runErr.Error()
		} else {
			final.State = RunSucceeded
		}
		r.mu.Lock()
		r.last[jobID] = final
		r.current = nil
		r.mu.Unlock()
		cancel()
		close(run.done)
	}()
	return runID, nil
}

// GetStatus 返回 Job 的运行状态：进行中返回 running 快照，
// 否则返回最近一次完成的状态；从未运行过为 idle。
func (r *Runner) GetStatus(ctx context.Context, jobID string) (RunStatus, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.current != nil && r.current.jobID == jobID {
		return RunStatus{
			RunID:     r.current.runID,
			State:     RunRunning,
			StartedAt: r.current.startedAt,
		}, nil
	}
	if st, ok := r.last[jobID]; ok {
		return st, nil
	}
	return RunStatus{State: RunIdle}, nil
}

// Wait 等待指定 run 结束并返回最终状态；run 未知或已结束则直接查询
// 已记录状态。
func (r *Runner) Wait(ctx context.Context, runID string) (RunStatus, error) {
	r.mu.Lock()
	run := r.current
	r.mu.Unlock()

	if run == nil || run.runID != runID {
		if st, ok := r.findFinished(runID); ok {
			return st, nil
		}
		return RunStatus{}, fmt.Errorf("%w: %s", ErrRunUnknown, runID)
	}
	select {
	case <-run.done:
	case <-ctx.Done():
		return RunStatus{}, ctx.Err()
	}
	if st, ok := r.findFinished(runID); ok {
		return st, nil
	}
	return RunStatus{}, fmt.Errorf("%w: %s", ErrRunUnknown, runID)
}

// Shutdown 取消当前运行并等待其退出；ctx 超时则返回 ctx 错误。
func (r *Runner) Shutdown(ctx context.Context) error {
	r.mu.Lock()
	var cancel context.CancelFunc
	if r.current != nil {
		cancel = r.current.cancel
	}
	r.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	done := make(chan struct{})
	go func() {
		r.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// findFinished 在已完成的运行记录中查找指定 run。
func (r *Runner) findFinished(runID string) (RunStatus, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, st := range r.last {
		if st.RunID == runID {
			return st, true
		}
	}
	return RunStatus{}, false
}

// newRunID 生成 run_<128-bit random hex> 形式的运行 ID。
func newRunID() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate run id: %w", err)
	}
	return "run_" + hex.EncodeToString(buf), nil
}

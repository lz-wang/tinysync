package syncjob

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"

	"tinysync/internal/logging"
	"tinysync/internal/source"
)

// RunState 是运行的状态机取值。running → succeeded | failed；
// skipped 是调度触发但未执行（overlap / 容量不足）的持久化记录。
// idle 不是持久化状态，仅表示「从未运行」的查询占位。
type RunState string

// 运行状态机取值。运行记录持久化于 sync_runs，重启后仍可查询。
const (
	RunIdle      RunState = "idle"
	RunRunning   RunState = "running"
	RunSucceeded RunState = "succeeded"
	RunFailed    RunState = "failed"
	RunSkipped   RunState = "skipped"
)

// 手动运行相关错误：API 层映射为 409 等状态码。
var (
	// ErrRunActive 表示同一 Job 已有运行在进行（手动触发返回 409）。
	ErrRunActive = errors.New("another sync run is active")
	// ErrConcurrencyLimit 表示全局并发已达 MaxConcurrentJobs 上限
	// （手动触发返回 409）。
	ErrConcurrencyLimit = errors.New("concurrency limit reached")
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

// RunStatus 是一次运行的快照（API status 端点的数据形态）。
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
	runID        string
	jobID        string
	trigger      RunTrigger
	scheduledFor *time.Time
	cancel       context.CancelFunc
	done         chan struct{}
	// startedAt 在 goroutine 结束时用于填充最终状态。
	startedAt time.Time
}

// Runner 是同步执行协调器：并发 Job 数受控（MaxConcurrentJobs）、同一
// Job 严格串行（手动触发冲突报错，调度触发记 skipped）、运行记录以
// history 为唯一事实来源（run row 在同步 goroutine 启动前落库）。
type Runner struct {
	repo    Repository
	managed ManagedRepository
	creds   SourceCredentials
	factory source.RemoteFactory
	// history 是运行记录的持久化仓库。
	history RunRepository
	// Now 返回当前时间；默认 UTC time.Now，测试可注入固定时钟。
	Now func() time.Time

	// MaxConcurrentJobs 是全进程同时运行的 Job 数上限；必须为正整数，
	// 由应用装配从运行配置注入。
	MaxConcurrentJobs int
	// MaxConcurrentTransfers 是全进程同时进行的远端文件下载上限，
	// 由同步引擎消费；必须为正整数。
	MaxConcurrentTransfers int

	mu     sync.Mutex
	active map[string]*activeRun // jobID → 进行中的运行
	wg     sync.WaitGroup
}

// NewRunner 构造 Runner：默认并发 Job 数 1（与 v0.3 行为一致）、
// 并发传输 4，应用装配可按运行配置覆盖。
func NewRunner(repo Repository, managed ManagedRepository, creds SourceCredentials, factory source.RemoteFactory, history RunRepository) *Runner {
	return &Runner{
		repo:                   repo,
		managed:                managed,
		creds:                  creds,
		factory:                factory,
		history:                history,
		Now:                    func() time.Time { return time.Now().UTC() },
		MaxConcurrentJobs:      1,
		MaxConcurrentTransfers: 4,
		active:                 make(map[string]*activeRun),
	}
}

// Start 校验并异步启动一轮手动同步，立即返回 run ID。运行 context 独立于
// 调用方的 HTTP request context；同一 Job 已在运行或全局并发已满、Job 或
// Source 禁用时拒绝启动。
func (r *Runner) Start(ctx context.Context, jobID string) (string, error) {
	return r.start(ctx, jobID, TriggerManual, nil)
}

// StartScheduled 以调度触发启动一轮同步。occurrence 到期但同 Job 运行中
// 或全局并发已满时不排队：记录 status=skipped 的 run（error 记原因）并
// 消费该 occurrence，返回空 run ID 与 nil 错误。
func (r *Runner) StartScheduled(ctx context.Context, jobID string, trigger RunTrigger, scheduledFor time.Time) (string, error) {
	return r.start(ctx, jobID, trigger, &scheduledFor)
}

// start 是手动与调度触发的共同路径：校验 → 原子检查 overlap 与全局容量 →
// 同步落库 running 记录 → 启动 goroutine。
func (r *Runner) start(ctx context.Context, jobID string, trigger RunTrigger, scheduledFor *time.Time) (string, error) {
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

	maxConcurrent := r.MaxConcurrentJobs
	if maxConcurrent < 1 {
		maxConcurrent = 1
	}

	r.mu.Lock()
	// 同一 Job 严格串行：运行中再触发绝不并发（旧 mapping 的 metadata
	// 推进与新一轮计划交叉会产生状态竞争）。
	if _, running := r.active[jobID]; running {
		r.mu.Unlock()
		if scheduledFor != nil {
			r.recordSkipped(ctx, job, trigger, *scheduledFor, "previous run still active")
			return "", nil
		}
		return "", fmt.Errorf("%w: job %s is running", ErrRunActive, jobID)
	}
	if len(r.active) >= maxConcurrent {
		r.mu.Unlock()
		if scheduledFor != nil {
			r.recordSkipped(ctx, job, trigger, *scheduledFor, "concurrency limit reached")
			return "", nil
		}
		return "", fmt.Errorf("%w: at most %d concurrent jobs", ErrConcurrencyLimit, maxConcurrent)
	}
	runID, err := newRunID()
	if err != nil {
		r.mu.Unlock()
		return "", err
	}
	now := r.Now()
	run := &activeRun{
		runID:        runID,
		jobID:        jobID,
		trigger:      trigger,
		scheduledFor: scheduledFor,
		cancel:       nil,
		done:         make(chan struct{}),
		startedAt:    now,
	}
	r.active[jobID] = run
	r.mu.Unlock()

	// run row 必须在 goroutine 启动前同步写入成功：不允许出现已经开始
	// 修改本地文件、却没有任何历史 run ID 的状态。失败则回收 slot。
	err = r.history.Insert(ctx, RunRecord{
		ID:           runID,
		JobID:        jobID,
		Trigger:      trigger,
		ScheduledFor: scheduledFor,
		State:        RunRunning,
		StartedAt:    now,
	})
	if err != nil {
		r.mu.Lock()
		delete(r.active, jobID)
		r.mu.Unlock()
		return "", fmt.Errorf("persist run %s: %w", runID, err)
	}

	runCtx, cancel := context.WithCancel(context.Background())
	run.cancel = cancel
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		defer func() {
			r.mu.Lock()
			delete(r.active, jobID)
			r.mu.Unlock()
			cancel()
			close(run.done)
		}()
		r.execute(runCtx, job, remote, run)
	}()
	return runID, nil
}

// execute 运行同步引擎并把终态落库；落库失败只记日志，不改变本轮结果。
func (r *Runner) execute(ctx context.Context, job Job, remote source.Remote, run *activeRun) {
	stats, runErr := Run(ctx, RunOptions{
		Remote:                 remote,
		Job:                    job,
		Managed:                r.managed,
		Items:                  runItemRecorder{repo: r.history},
		RunID:                  run.runID,
		MaxConcurrentTransfers: r.MaxConcurrentTransfers,
	})
	finishedAt := r.Now()
	final := RunRecord{
		ID:           run.runID,
		JobID:        run.jobID,
		Trigger:      run.trigger,
		ScheduledFor: run.scheduledFor,
		State:        RunSucceeded,
		StartedAt:    run.startedAt,
		FinishedAt:   &finishedAt,
		Stats:        stats,
	}
	if runErr != nil {
		final.State = RunFailed
		final.Error = runErr.Error()
	}
	if err := r.history.Finalize(ctx, final); err != nil {
		logging.Errorf("finalize run %s: %v", run.runID, err)
	}
	// prune 使用独立 context：运行取消（如 Shutdown）后仍要回收历史容量。
	if err := r.history.PruneRetention(context.WithoutCancel(ctx), RetentionRunsPerJob); err != nil {
		logging.Errorf("prune run history: %v", err)
	}
}

// recordSkipped 记录调度触发的 skipped run：occurrence 已消费，
// 不排队、不执行。
func (r *Runner) recordSkipped(ctx context.Context, job Job, trigger RunTrigger, scheduledFor time.Time, reason string) {
	id, err := newRunID()
	if err != nil {
		logging.Errorf("generate skipped run id for job %s: %v", job.ID, err)
		return
	}
	now := r.Now()
	run := RunRecord{
		ID:           id,
		JobID:        job.ID,
		Trigger:      trigger,
		ScheduledFor: &scheduledFor,
		State:        RunSkipped,
		StartedAt:    now,
		FinishedAt:   &now,
		Error:        reason,
	}
	if err := r.history.Insert(ctx, run); err != nil {
		logging.Errorf("record skipped run for job %s: %v", job.ID, err)
	}
}

// IsRunning 判断指定 Job 是否正在运行，供 API 层修改/删除保护使用：
// 运行中的 Job 拒绝 PATCH / DELETE，避免旧 mapping 的 metadata Upsert
// 与配置变更交叉产生状态竞争。
func (r *Runner) IsRunning(jobID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, running := r.active[jobID]
	return running
}

// GetStatus 返回 Job 的运行状态：进行中返回 running 快照，否则返回
// 最近一条持久化 run（含 skipped）；从未运行为 idle。重启后历史仍在。
func (r *Runner) GetStatus(ctx context.Context, jobID string) (RunStatus, error) {
	r.mu.Lock()
	run := r.active[jobID]
	r.mu.Unlock()
	if run != nil {
		return RunStatus{
			RunID:     run.runID,
			State:     RunRunning,
			StartedAt: run.startedAt,
		}, nil
	}
	rec, err := r.history.Latest(ctx, jobID)
	if errors.Is(err, ErrRunUnknown) {
		return RunStatus{State: RunIdle}, nil
	}
	if err != nil {
		return RunStatus{}, err
	}
	return runRecordToStatus(rec), nil
}

// Wait 等待指定 run 结束并返回最终状态；run 未知或已结束则查询
// 持久化历史。
func (r *Runner) Wait(ctx context.Context, runID string) (RunStatus, error) {
	run := r.findActive(runID)
	if run != nil {
		select {
		case <-run.done:
		case <-ctx.Done():
			return RunStatus{}, ctx.Err()
		}
	}
	rec, err := r.history.Get(ctx, runID)
	if err != nil {
		return RunStatus{}, err
	}
	return runRecordToStatus(rec), nil
}

// Shutdown 取消全部运行并等待退出；ctx 超时则返回 ctx 错误。
func (r *Runner) Shutdown(ctx context.Context) error {
	r.mu.Lock()
	cancels := make([]context.CancelFunc, 0, len(r.active))
	for _, run := range r.active {
		cancels = append(cancels, run.cancel)
	}
	r.mu.Unlock()
	for _, cancel := range cancels {
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

// findActive 按 run ID 查找进行中的运行。
func (r *Runner) findActive(runID string) *activeRun {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, run := range r.active {
		if run.runID == runID {
			return run
		}
	}
	return nil
}

// runItemRecorder 把引擎的 ItemRecorder 适配到 RunRepository：
// 文件级明细直接追加到 sync_run_items。
type runItemRecorder struct {
	repo RunRepository
}

// RecordItem 实现 ItemRecorder。
func (a runItemRecorder) RecordItem(ctx context.Context, item RunItem) error {
	return a.repo.AppendItem(ctx, item)
}

// runRecordToStatus 把持久化 run 转为状态快照。
func runRecordToStatus(rec RunRecord) RunStatus {
	st := RunStatus{
		RunID:     rec.ID,
		State:     rec.State,
		StartedAt: rec.StartedAt,
		Stats:     rec.Stats,
		Error:     rec.Error,
	}
	if rec.FinishedAt != nil {
		st.FinishedAt = *rec.FinishedAt
	}
	return st
}

// newRunID 生成 run_<128-bit random hex> 形式的运行 ID。
func newRunID() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate run id: %w", err)
	}
	return "run_" + hex.EncodeToString(buf), nil
}

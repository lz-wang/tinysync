package job

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"
	"testing"
	"time"
)

// fixedNow 是 Runner 测试的固定时钟基准。
var fixedNow = time.Unix(1757879400, 0).UTC()

// stubExecutor 是统一 Runner / Scheduler 测试的可编程执行器：不依赖
// 任何领域实现，模拟阻塞、立即失败与结果摘要，并记录收到的 RunRequest
// （验证 Runner 注入的共享 limiter / 超时等协调输入）。
type stubExecutor struct {
	mu sync.Mutex
	// err 非 nil 时下一轮 Run 立即返回该错误（一次性生效，模拟领域
	// 执行失败；之后恢复成功）。
	err error
	// result 非 nil 时下一轮 Run 成功返回该结果摘要（一次性生效）。
	result json.RawMessage
	// blockNext 使下一轮 Run 阻塞到放行或 ctx 取消（一次性生效，
	// 模拟长运行同步）。
	blockNext bool
	// release 是阻塞轮次的放行闸门。
	release chan struct{}

	// entered 为每轮 Run 进入时缓存一条信号，测试用它等待「已在
	// 执行器内」；cancelled 在 Run 因 ctx 取消返回时关闭（断言取消
	// 真正传播到执行器）。
	entered   chan struct{}
	cancelled chan struct{}
	closeOnce sync.Once

	reqs []RunRequest
}

func newStubExecutor() *stubExecutor {
	return &stubExecutor{
		entered:   make(chan struct{}, 64),
		cancelled: make(chan struct{}),
	}
}

// Validate 实现 Executor：桩不做领域校验，配置原样返回。
func (e *stubExecutor) Validate(ctx context.Context, jobID string, cfg json.RawMessage) (json.RawMessage, error) {
	return cfg, nil
}

// Run 实现 Executor：按可编程配置阻塞 / 失败 / 返回摘要。
func (e *stubExecutor) Run(ctx context.Context, req RunRequest) (RunResult, error) {
	e.mu.Lock()
	e.reqs = append(e.reqs, req)
	err := e.err
	e.err = nil
	result := e.result
	e.result = nil
	block := e.blockNext
	e.blockNext = false
	release := e.release
	e.mu.Unlock()

	select {
	case e.entered <- struct{}{}:
	default:
	}
	if block {
		select {
		case <-release:
		case <-ctx.Done():
			e.closeOnce.Do(func() { close(e.cancelled) })
			return RunResult{}, ctx.Err()
		}
	}
	if err != nil {
		return RunResult{}, err
	}
	return RunResult{Result: result}, nil
}

// blockRun 让下一轮 Run 阻塞，返回放行闸门；需要多个并行阻塞轮时
// 在每轮 Start 前各调用一次。
func (e *stubExecutor) blockRun() chan struct{} {
	release := make(chan struct{})
	e.mu.Lock()
	e.blockNext = true
	e.release = release
	e.mu.Unlock()
	return release
}

// failNext 让下一轮 Run 立即返回错误。
func (e *stubExecutor) failNext(err error) {
	e.mu.Lock()
	e.err = err
	e.mu.Unlock()
}

// succeedWith 让下一轮 Run 成功返回指定结果摘要。
func (e *stubExecutor) succeedWith(result json.RawMessage) {
	e.mu.Lock()
	e.result = result
	e.mu.Unlock()
}

// waitEntered 等待一轮 Run 真正进入执行器（活动窗口已建立）。
func (e *stubExecutor) waitEntered(t *testing.T) {
	t.Helper()
	select {
	case <-e.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("executor was never entered")
	}
}

// cancelledRun 报告是否有 Run 因 ctx 取消返回。
func (e *stubExecutor) cancelledRun() bool {
	select {
	case <-e.cancelled:
		return true
	default:
		return false
	}
}

// requests 返回历轮 RunRequest 快照。
func (e *stubExecutor) requests() []RunRequest {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]RunRequest(nil), e.reqs...)
}

// memJobRepo 是 Repository 的内存实现（Runner / Scheduler 测试专用）。
type memJobRepo struct {
	jobs map[string]Job
}

func newMemJobRepo() *memJobRepo {
	return &memJobRepo{jobs: map[string]Job{}}
}

func (r *memJobRepo) Create(ctx context.Context, j Job) error {
	r.jobs[j.ID] = j
	return nil
}

func (r *memJobRepo) Get(ctx context.Context, id string) (Job, error) {
	j, ok := r.jobs[id]
	if !ok {
		return Job{}, fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	return j, nil
}

func (r *memJobRepo) List(ctx context.Context) ([]Job, error) {
	list := make([]Job, 0, len(r.jobs))
	for _, j := range r.jobs {
		list = append(list, j)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Name < list[j].Name })
	return list, nil
}

func (r *memJobRepo) ListByKind(ctx context.Context, kind JobKind) ([]Job, error) {
	list, err := r.List(ctx)
	if err != nil {
		return nil, err
	}
	filtered := make([]Job, 0, len(list))
	for _, j := range list {
		if j.Kind == kind {
			filtered = append(filtered, j)
		}
	}
	return filtered, nil
}

func (r *memJobRepo) Update(ctx context.Context, j Job, hooks ...TxHook) error {
	if _, ok := r.jobs[j.ID]; !ok {
		return fmt.Errorf("%w: %s", ErrNotFound, j.ID)
	}
	r.jobs[j.ID] = j
	return nil
}

func (r *memJobRepo) Delete(ctx context.Context, id string) error {
	delete(r.jobs, id)
	return nil
}

func (r *memJobRepo) CountBySource(ctx context.Context, sourceID string) (int, error) {
	return 0, nil
}

// memRunRepo 是 RunRepository 的内存实现（Runner / Scheduler 测试专用）。
type memRunRepo struct {
	mu     sync.Mutex
	runs   map[string]RunRecord
	order  []string
	prunes int
	// insertErr 非 nil 时 Insert / PersistScheduledRun 失败（模拟持久化故障）。
	insertErr error
	// finalizeErr 非 nil 时 Finalize 失败（模拟终态落库故障）。
	finalizeErr error
	// hasErr 非 nil 时 HasRunFor 失败（模拟读取故障）。
	hasErr error
	// markOnce 模拟 PersistScheduledRun 事务内的 once 消费写入
	//（runnerEnv 装配时桥接到 memJobRepo）。
	markOnce func(jobID string, at time.Time)
}

func newMemRunRepo() *memRunRepo {
	return &memRunRepo{runs: map[string]RunRecord{}}
}

func (m *memRunRepo) Insert(ctx context.Context, run RunRecord) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.insertErr != nil {
		return m.insertErr
	}
	m.runs[run.ID] = run
	m.order = append(m.order, run.ID)
	return nil
}

// PersistScheduledRun 模拟事务路径：落库 run，once 触发时同步写入
// 消费状态（消费写入失败即整体未落库，与 SQLite 事务语义一致）。
func (m *memRunRepo) PersistScheduledRun(ctx context.Context, run RunRecord) error {
	m.mu.Lock()
	if m.insertErr != nil {
		err := m.insertErr
		m.mu.Unlock()
		return err
	}
	m.runs[run.ID] = run
	m.order = append(m.order, run.ID)
	m.mu.Unlock()
	if run.Trigger == TriggerOnce && run.ScheduledFor != nil && m.markOnce != nil {
		m.markOnce(run.JobID, *run.ScheduledFor)
	}
	return nil
}

func (m *memRunRepo) Finalize(ctx context.Context, run RunRecord) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.finalizeErr != nil {
		return m.finalizeErr
	}
	stored, ok := m.runs[run.ID]
	if !ok {
		return fmt.Errorf("%w: %s", ErrRunUnknown, run.ID)
	}
	stored.State = run.State
	stored.FinishedAt = run.FinishedAt
	stored.Result = run.Result
	stored.Error = run.Error
	m.runs[run.ID] = stored
	return nil
}

func (m *memRunRepo) Get(ctx context.Context, runID string) (RunRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	run, ok := m.runs[runID]
	if !ok {
		return RunRecord{}, fmt.Errorf("%w: %s", ErrRunUnknown, runID)
	}
	return run, nil
}

func (m *memRunRepo) Latest(ctx context.Context, jobID string) (RunRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var best RunRecord
	found := false
	for _, id := range m.order {
		run := m.runs[id]
		if run.JobID != jobID {
			continue
		}
		// started_at 并列（固定时钟）时按插入序取后者。
		if !found || !run.StartedAt.Before(best.StartedAt) {
			best = run
			found = true
		}
	}
	if !found {
		return RunRecord{}, fmt.Errorf("%w: %s", ErrRunUnknown, jobID)
	}
	return best, nil
}

func (m *memRunRepo) List(ctx context.Context, filter RunFilter) ([]RunRecord, int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	// 复刻 SQLite 语义：started_at 倒序，插入序倒序兜底同毫秒并列。
	type entry struct {
		run   RunRecord
		index int
	}
	entries := make([]entry, 0, len(m.order))
	for index, id := range m.order {
		run := m.runs[id]
		if filter.JobID != "" && run.JobID != filter.JobID {
			continue
		}
		if filter.Kind != "" && run.JobKind != filter.Kind {
			continue
		}
		if filter.Status != "" && run.State != filter.Status {
			continue
		}
		entries = append(entries, entry{run: run, index: index})
	}
	sort.SliceStable(entries, func(i, j int) bool {
		if !entries[i].run.StartedAt.Equal(entries[j].run.StartedAt) {
			return entries[i].run.StartedAt.After(entries[j].run.StartedAt)
		}
		return entries[i].index > entries[j].index
	})
	list := make([]RunRecord, 0, len(entries))
	for _, e := range entries {
		list = append(list, e.run)
	}
	return list, len(list), nil
}

func (m *memRunRepo) AppendItem(ctx context.Context, item RunItem) error { return nil }

func (m *memRunRepo) Items(ctx context.Context, runID string, limit, offset int) ([]RunItem, int, error) {
	return nil, 0, nil
}

// FailedItems 桩：内存仓库不产生失败明细（完成事件的失败明细负载由
// 真 SQLite 路径的单测覆盖）。
func (m *memRunRepo) FailedItems(ctx context.Context, runID string, limit int) ([]RunItem, int, error) {
	return nil, 0, nil
}

func (m *memRunRepo) HasRunFor(ctx context.Context, jobID string, trigger RunTrigger, scheduledFor time.Time) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.hasErr != nil {
		return false, m.hasErr
	}
	for _, id := range m.order {
		run := m.runs[id]
		if run.JobID == jobID && run.Trigger == trigger &&
			run.ScheduledFor != nil && run.ScheduledFor.Equal(scheduledFor) {
			return true, nil
		}
	}
	return false, nil
}

func (m *memRunRepo) FailStaleRunning(ctx context.Context, finishedAt time.Time, reason string) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var n int64
	for id, run := range m.runs {
		if run.State == RunRunning {
			run.State = RunFailed
			run.FinishedAt = &finishedAt
			run.Error = reason
			m.runs[id] = run
			n++
		}
	}
	return n, nil
}

func (m *memRunRepo) PruneRetention(ctx context.Context, keepPerJob int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.prunes++
	return nil
}

// runnerEnv 聚合 Runner 测试环境：内存仓库 + 按注册表分发的桩执行器。
type runnerEnv struct {
	repo    *memJobRepo
	history *memRunRepo
	exec    *stubExecutor
	runner  *Runner
}

func newRunnerEnv(t *testing.T) *runnerEnv {
	t.Helper()
	env := &runnerEnv{
		repo:    newMemJobRepo(),
		history: newMemRunRepo(),
		exec:    newStubExecutor(),
	}
	// 桥接 PersistScheduledRun 的事务性 once 消费写入（生产路径在
	// SQLite 事务内写 jobs.once_consumed_at）。
	env.history.markOnce = func(jobID string, at time.Time) {
		j, ok := env.repo.jobs[jobID]
		if !ok {
			return
		}
		consumed := at
		j.OnceConsumedAt = &consumed
		env.repo.jobs[jobID] = j
	}
	env.runner = NewRunner(env.repo, env.history, map[JobKind]Executor{KindFile: env.exec})
	env.runner.Now = func() time.Time { return fixedNow }
	return env
}

// mustJob 创建启用的测试任务（配置为空对象，公共层不解释领域字段）。
func (e *runnerEnv) mustJob(t *testing.T, name string) Job {
	t.Helper()
	id, err := NewID()
	if err != nil {
		t.Fatalf("NewID: %v", err)
	}
	j := Job{
		ID:        id,
		Name:      name + "-" + id,
		Kind:      KindFile,
		Enabled:   true,
		Config:    json.RawMessage("{}"),
		CreatedAt: fixedNow,
		UpdatedAt: fixedNow,
	}
	if err := e.repo.Create(context.Background(), j); err != nil {
		t.Fatalf("create job: %v", err)
	}
	return j
}

// runCountGuard 统计 Job 的持久化 run 数（runnerEnv 的 history）。
func (e *runnerEnv) runCountGuard(t *testing.T, jobID string) int {
	t.Helper()
	_, total, err := e.history.List(context.Background(), RunFilter{JobID: jobID})
	if err != nil {
		t.Fatalf("list runs: %v", err)
	}
	return total
}

// NewRunner 默认全局并发为 2（已决 R12）：应用未显式配置时多任务可并行。
func TestRunnerDefaultConcurrency(t *testing.T) {
	r := NewRunner(newMemJobRepo(), newMemRunRepo(), map[JobKind]Executor{KindFile: newStubExecutor()})
	if r.MaxConcurrentJobs != 2 {
		t.Errorf("default MaxConcurrentJobs = %d, want 2", r.MaxConcurrentJobs)
	}
}

// 手动运行完成：状态推进到 succeeded，执行器返回的结果摘要原样落库。
func TestRunnerCompletesSynchronously(t *testing.T) {
	env := newRunnerEnv(t)
	env.exec.succeedWith(json.RawMessage(`{"files_created":1}`))
	job := env.mustJob(t, "sync")

	handle, err := env.runner.Start(context.Background(), job.ID)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if handle == "" {
		t.Fatal("Start returned empty run id")
	}
	final, err := env.runner.Wait(context.Background(), handle)
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if final.State != RunSucceeded {
		t.Errorf("state = %q, want succeeded (error %q)", final.State, final.Error)
	}
	if string(final.Result) != `{"files_created":1}` {
		t.Errorf("result = %s, want executor summary", final.Result)
	}

	// 完成后查询状态保持 succeeded（读持久化历史）。
	status, err := env.runner.GetStatus(context.Background(), job.ID)
	if err != nil {
		t.Fatalf("GetStatus: %v", err)
	}
	if status.State != RunSucceeded {
		t.Errorf("GetStatus state = %q, want succeeded", status.State)
	}
}

// 并发模型：同一 Job 严格串行（手动触发 ErrRunActive）；不同 Job 受
// MaxConcurrentJobs 限制（手动触发 ErrConcurrencyLimit）；提高上限后
// 可并行。
func TestRunnerConcurrency(t *testing.T) {
	env := newRunnerEnv(t)
	env.runner.MaxConcurrentJobs = 1
	jobA := env.mustJob(t, "a")
	jobB := env.mustJob(t, "b")
	ctx := context.Background()

	releaseA := env.exec.blockRun()
	if _, err := env.runner.Start(ctx, jobA.ID); err != nil {
		t.Fatalf("Start A: %v", err)
	}
	env.exec.waitEntered(t)
	// 同 Job 再触发。
	if _, err := env.runner.Start(ctx, jobA.ID); !errors.Is(err, ErrRunActive) {
		t.Errorf("Start same job during run = %v, want ErrRunActive", err)
	}
	// 容量 1：其他 Job 触发受全局限制。
	if _, err := env.runner.Start(ctx, jobB.ID); !errors.Is(err, ErrConcurrencyLimit) {
		t.Errorf("Start B at capacity = %v, want ErrConcurrencyLimit", err)
	}

	// 容量 2：两个 Job 并行。
	releaseB := env.exec.blockRun()
	env.runner.MaxConcurrentJobs = 2
	if _, err := env.runner.Start(ctx, jobB.ID); err != nil {
		t.Fatalf("Start B with capacity 2: %v", err)
	}
	env.exec.waitEntered(t)
	if !env.runner.IsRunning(jobA.ID) || !env.runner.IsRunning(jobB.ID) {
		t.Errorf("IsRunning = (%t, %t), want both running",
			env.runner.IsRunning(jobA.ID), env.runner.IsRunning(jobB.ID))
	}
	close(releaseA)
	close(releaseB)
	if err := env.runner.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if env.runner.IsRunning(jobA.ID) || env.runner.IsRunning(jobB.ID) {
		t.Error("jobs still running after Shutdown")
	}
}

// MaxConcurrentTransfers 的进程级共享：全部运行经 Runner 的同一
// TransferLimiter 单例协调（各领一份独立限额的叠加必须被排除），
// 运行配置的传输超时原样透传给执行器。
func TestRunnerSharesTransferLimiterAcrossRuns(t *testing.T) {
	env := newRunnerEnv(t)
	env.runner.MaxConcurrentJobs = 2
	env.runner.MaxConcurrentTransfers = 3
	env.runner.TransferTimeout = 5 * time.Second
	jobA := env.mustJob(t, "a")
	jobB := env.mustJob(t, "b")
	ctx := context.Background()

	runA, err := env.runner.Start(ctx, jobA.ID)
	if err != nil {
		t.Fatalf("Start A: %v", err)
	}
	runB, err := env.runner.Start(ctx, jobB.ID)
	if err != nil {
		t.Fatalf("Start B: %v", err)
	}
	for _, run := range []string{runA, runB} {
		if final, err := env.runner.Wait(ctx, run); err != nil || final.State != RunSucceeded {
			t.Fatalf("run %s = %+v (%v), want succeeded", run, final, err)
		}
	}
	reqs := env.exec.requests()
	if len(reqs) != 2 {
		t.Fatalf("executor requests = %d, want 2", len(reqs))
	}
	if reqs[0].Transfers == nil || reqs[0].Transfers != reqs[1].Transfers {
		t.Errorf("runs received distinct transfer limiters (%v vs %v), want one shared instance",
			reqs[0].Transfers, reqs[1].Transfers)
	}
	if got := reqs[0].Transfers.Capacity(); got != 3 {
		t.Errorf("limiter capacity = %d, want MaxConcurrentTransfers 3", got)
	}
	if reqs[0].TransferTimeout != 5*time.Second || reqs[1].TransferTimeout != 5*time.Second {
		t.Errorf("transfer timeout = (%s, %s), want 5s propagated to both runs",
			reqs[0].TransferTimeout, reqs[1].TransferTimeout)
	}
}

// Job 协调位：配置变更与执行链原子互斥。mutation 占用期间手动与
// 调度启动都报瞬时错误（occurrence 不被消费——几毫秒的配置互斥
// 不属于 overlap 策略，不该吞掉一次调度）；运行占用期间
// BeginMutation 报 ErrRunActive；各自释放后恢复。
func TestRunnerMutationGuard(t *testing.T) {
	env := newRunnerEnv(t)
	release := env.exec.blockRun()
	job := env.mustJob(t, "guarded")
	ctx := context.Background()

	if err := env.runner.BeginMutation(job.ID); err != nil {
		t.Fatalf("BeginMutation on idle job: %v", err)
	}
	if _, err := env.runner.Start(ctx, job.ID); !errors.Is(err, ErrJobMutating) {
		t.Errorf("Start during mutation = %v, want ErrJobMutating", err)
	}
	occ := fixedNow
	runID, err := env.runner.StartScheduled(ctx, job.ID, TriggerInterval, occ)
	if !errors.Is(err, ErrJobMutating) || runID != "" {
		t.Fatalf("StartScheduled during mutation = (%q, %v), want ErrJobMutating", runID, err)
	}
	// occurrence 未被消费：无任何 run 记录（含 skipped）。
	if got := env.runCountGuard(t, job.ID); got != 0 {
		t.Fatalf("runs after mutation-rejected occurrence = %d, want 0 (not consumed)", got)
	}
	env.runner.EndMutation(job.ID)

	if _, err := env.runner.Start(ctx, job.ID); err != nil {
		t.Fatalf("Start after mutation released: %v", err)
	}
	if err := env.runner.BeginMutation(job.ID); !errors.Is(err, ErrRunActive) {
		t.Errorf("BeginMutation during run = %v, want ErrRunActive", err)
	}
	close(release)
	if err := env.runner.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if err := env.runner.BeginMutation(job.ID); err != nil {
		t.Errorf("BeginMutation after run finished = %v, want success", err)
	}
	env.runner.EndMutation(job.ID)
}

// skipped run 与正常执行走同一条容量回收路径：长时间 overlap 的 Job
// 频繁产生 skipped 记录时，历史同样被 prune，不会无限堆积。
func TestRunnerSkippedRunPrunesRetention(t *testing.T) {
	env := newRunnerEnv(t)
	release := env.exec.blockRun()
	job := env.mustJob(t, "prune")
	ctx := context.Background()

	if _, err := env.runner.Start(ctx, job.ID); err != nil {
		t.Fatalf("Start: %v", err)
	}
	occ := fixedNow
	if _, err := env.runner.StartScheduled(ctx, job.ID, TriggerInterval, occ); err != nil {
		t.Fatalf("StartScheduled: %v", err)
	}
	env.history.mu.Lock()
	prunes := env.history.prunes
	env.history.mu.Unlock()
	if prunes == 0 {
		t.Error("PruneRetention was not called after skipped run")
	}
	close(release)
	if err := env.runner.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
}

// StartScheduled 的持久化失败如实返回错误：occurrence 未被消费，
// 调度器据此重试窗口而不是当作已处理。
func TestRunnerStartScheduledPersistenceError(t *testing.T) {
	env := newRunnerEnv(t)
	release := env.exec.blockRun()
	job := env.mustJob(t, "persist-fail")
	ctx := context.Background()

	if _, err := env.runner.Start(ctx, job.ID); err != nil {
		t.Fatalf("Start: %v", err)
	}
	occ := fixedNow
	env.history.insertErr = errors.New("disk I/O error")
	runID, err := env.runner.StartScheduled(ctx, job.ID, TriggerInterval, occ)
	if err == nil || runID != "" {
		t.Fatalf("StartScheduled with insert failure = (%q, %v), want error", runID, err)
	}
	consumed, err := env.history.HasRunFor(ctx, job.ID, TriggerInterval, occ)
	if err != nil || consumed {
		t.Errorf("occurrence consumed = (%t, %v), want false (not consumed)", consumed, err)
	}
	env.history.insertErr = nil
	close(release)
	if err := env.runner.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
}

// Shutdown 后拒绝启动新运行：手动与调度触发都返回 ErrShuttingDown，
// 且 Shutdown 幂等。
func TestRunnerRejectsStartAfterShutdown(t *testing.T) {
	env := newRunnerEnv(t)
	job := env.mustJob(t, "late")
	ctx := context.Background()

	if err := env.runner.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if _, err := env.runner.Start(ctx, job.ID); !errors.Is(err, ErrShuttingDown) {
		t.Errorf("Start after shutdown = %v, want ErrShuttingDown", err)
	}
	occ := fixedNow
	if _, err := env.runner.StartScheduled(ctx, job.ID, TriggerOnce, occ); !errors.Is(err, ErrShuttingDown) {
		t.Errorf("StartScheduled after shutdown = %v, want ErrShuttingDown", err)
	}
	if err := env.runner.Shutdown(ctx); err != nil {
		t.Errorf("second Shutdown = %v, want nil", err)
	}
}

// run row 落库失败时，发布时预留的 WaitGroup 名额被正确释放：
// 否则 wg 永不归零，后续任何 Shutdown 都只能靠超时返回。
func TestRunnerStartInsertFailureReleasesWaitGroup(t *testing.T) {
	env := newRunnerEnv(t)
	job := env.mustJob(t, "insert-fail")
	ctx := context.Background()

	env.history.insertErr = errors.New("disk I/O error")
	if _, err := env.runner.Start(ctx, job.ID); err == nil {
		t.Fatal("Start with insert failure = nil error, want error")
	}
	env.history.insertErr = nil

	// 协调位已释放：故障后可正常再次启动。
	runID, err := env.runner.Start(ctx, job.ID)
	if err != nil {
		t.Fatalf("Start after failed start: %v", err)
	}
	if final, err := env.runner.Wait(ctx, runID); err != nil || final.State != RunSucceeded {
		t.Errorf("retry run = %+v (%v), want succeeded", final, err)
	}

	// 泄漏回归：此时唯一的在途名额是失败 Start 未释放的残留——
	// Shutdown 必须立即返回 nil（若 wg 泄漏将阻塞到超时）。
	shutdownCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if err := env.runner.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("Shutdown after failed start = %v, want nil (immediately)", err)
	}
}

// once occurrence 产生 run 即消费（succeeded 与 skipped 都算）：消费
// 状态写入 Job 本身，与可裁剪的运行历史解耦。
func TestRunnerOnceConsumptionMarkedOnJob(t *testing.T) {
	env := newRunnerEnv(t)
	ctx := context.Background()
	occ := fixedNow.Add(-time.Hour)

	job := env.mustJob(t, "once")
	job.Schedule = Schedule{Type: ScheduleOnce, Value: occ.Format(time.RFC3339)}
	if err := env.repo.Update(ctx, job); err != nil {
		t.Fatalf("set once schedule: %v", err)
	}

	runID, err := env.runner.StartScheduled(ctx, job.ID, TriggerOnce, occ)
	if err != nil {
		t.Fatalf("StartScheduled once: %v", err)
	}
	final, err := env.runner.Wait(ctx, runID)
	if err != nil || final.State != RunSucceeded {
		t.Fatalf("once run = %+v (%v), want succeeded", final, err)
	}
	stored, err := env.repo.Get(ctx, job.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if stored.OnceConsumedAt == nil || !stored.OnceConsumedAt.Equal(occ) {
		t.Errorf("OnceConsumedAt = %v, want %v", stored.OnceConsumedAt, occ)
	}

	// skipped 的 once 同样消费 occurrence（occurrence 已产生 run）。
	occ2 := occ.Add(time.Minute)
	job2 := env.mustJob(t, "once2")
	job2.Schedule = Schedule{Type: ScheduleOnce, Value: occ2.Format(time.RFC3339)}
	if err := env.repo.Update(ctx, job2); err != nil {
		t.Fatalf("set once2 schedule: %v", err)
	}
	if err := env.runner.recordSkipped(ctx, job2, TriggerOnce, occ2, "previous run still active"); err != nil {
		t.Fatalf("recordSkipped: %v", err)
	}
	stored2, err := env.repo.Get(ctx, job2.ID)
	if err != nil {
		t.Fatalf("Get job2: %v", err)
	}
	if stored2.OnceConsumedAt == nil || !stored2.OnceConsumedAt.Equal(occ2) {
		t.Errorf("skipped OnceConsumedAt = %v, want %v", stored2.OnceConsumedAt, occ2)
	}

	// NextRunAt 对已消费的 once 不再返回触发时刻。
	job2.Schedule.Value = occ.Format(time.RFC3339)
	consumed := occ
	job2.OnceConsumedAt = &consumed
	if err := env.repo.Update(ctx, job2); err != nil {
		t.Fatalf("set job2 consumed: %v", err)
	}
	if _, ok, err := env.runner.NextRunAt(ctx, job2); err != nil || ok {
		t.Errorf("NextRunAt after consumption = (ok %t, %v), want false", ok, err)
	}
}

// 调度触发的 overlap 不排队：记录 skipped run（occurrence 已消费、
// error 记原因），返回空 run ID 与 nil 错误；全局并发已满则是瞬时
// 错误（已决 R4）——不记 skipped、occurrence 不消费，交由调度器
// 保持游标在后续周期重试。
func TestRunnerScheduledSkipped(t *testing.T) {
	env := newRunnerEnv(t)
	release := env.exec.blockRun()
	env.runner.MaxConcurrentJobs = 1
	jobA := env.mustJob(t, "a")
	jobB := env.mustJob(t, "b")
	ctx := context.Background()

	if _, err := env.runner.Start(ctx, jobA.ID); err != nil {
		t.Fatalf("Start A: %v", err)
	}

	occ := fixedNow
	runID, err := env.runner.StartScheduled(ctx, jobA.ID, TriggerInterval, occ)
	if err != nil || runID != "" {
		t.Fatalf("StartScheduled during run = (%q, %v), want empty success", runID, err)
	}
	consumed, err := env.history.HasRunFor(ctx, jobA.ID, TriggerInterval, occ)
	if err != nil || !consumed {
		t.Fatalf("occurrence consumed = (%t, %v), want true", consumed, err)
	}
	rec, err := env.history.Latest(ctx, jobA.ID)
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}
	if rec.State != RunSkipped || rec.Error != "previous run still active" ||
		rec.ScheduledFor == nil || !rec.ScheduledFor.Equal(occ) {
		t.Errorf("skipped record = %+v, want skipped with overlap reason", rec)
	}

	// 全局容量不足（容量 1，Job A 占用）：瞬时错误，occurrence 保留。
	if _, err := env.runner.StartScheduled(ctx, jobB.ID, TriggerCron, occ); !errors.Is(err, ErrConcurrencyLimit) {
		t.Fatalf("StartScheduled at capacity = %v, want ErrConcurrencyLimit", err)
	}
	if consumed, err := env.history.HasRunFor(ctx, jobB.ID, TriggerCron, occ); err != nil || consumed {
		t.Errorf("capacity occurrence consumed = (%t, %v), want false (retained)", consumed, err)
	}
	if got := env.runCountGuard(t, jobB.ID); got != 0 {
		t.Errorf("runs for throttled job = %d, want 0 (no skipped record at capacity)", got)
	}

	close(release)
	if err := env.runner.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
}

// run row 在 goroutine 启动前同步落库：Start 返回即可查到 running 记录，
// trigger 与 scheduled_for 正确；结束后的终态落库可查询。
func TestRunnerPersistsRun(t *testing.T) {
	env := newRunnerEnv(t)
	release := env.exec.blockRun()
	job := env.mustJob(t, "persist")
	ctx := context.Background()

	occ := fixedNow
	runID, err := env.runner.StartScheduled(ctx, job.ID, TriggerOnce, occ)
	if err != nil {
		t.Fatalf("StartScheduled: %v", err)
	}
	rec, err := env.history.Get(ctx, runID)
	if err != nil {
		t.Fatalf("Get right after start: %v", err)
	}
	if rec.State != RunRunning || rec.Trigger != TriggerOnce ||
		rec.ScheduledFor == nil || !rec.ScheduledFor.Equal(occ) {
		t.Errorf("persisted running record = %+v, want running once with occurrence", rec)
	}
	if rec.JobKind != KindFile {
		t.Errorf("persisted job_kind = %q, want %q", rec.JobKind, KindFile)
	}

	close(release)
	final, err := env.runner.Wait(ctx, runID)
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if final.State != RunSucceeded {
		t.Errorf("final = %+v, want succeeded", final)
	}
	persisted, err := env.history.Get(ctx, runID)
	if err != nil || persisted.State != RunSucceeded || persisted.FinishedAt == nil {
		t.Errorf("persisted final record = %+v (%v), want succeeded with finished_at", persisted, err)
	}
}

// 禁用的 Job 拒绝运行。
func TestRunnerRejectsDisabled(t *testing.T) {
	env := newRunnerEnv(t)
	j := env.mustJob(t, "disabled")
	ctx := context.Background()

	updates := j
	updates.Enabled = false
	if err := env.repo.Update(ctx, updates); err != nil {
		t.Fatalf("disable job: %v", err)
	}
	if _, err := env.runner.Start(ctx, j.ID); !errors.Is(err, ErrJobDisabled) {
		t.Errorf("disabled job = %v, want ErrJobDisabled", err)
	}
}

// 不存在的 Job 报 ErrNotFound；无运行历史的 Job 状态为 idle。
func TestRunnerNotFoundAndIdleStatus(t *testing.T) {
	env := newRunnerEnv(t)
	job := env.mustJob(t, "idle")
	ctx := context.Background()

	if _, err := env.runner.Start(ctx, "job_missing"); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing job = %v, want ErrNotFound", err)
	}
	status, err := env.runner.GetStatus(ctx, job.ID)
	if err != nil {
		t.Fatalf("GetStatus: %v", err)
	}
	if status.State != RunIdle {
		t.Errorf("initial state = %q, want idle", status.State)
	}
	// 未知 run 查询报 ErrRunUnknown。
	if _, err := env.runner.Wait(ctx, "run_missing"); !errors.Is(err, ErrRunUnknown) {
		t.Errorf("Wait missing run = %v, want ErrRunUnknown", err)
	}
}

// Shutdown 取消运行中的执行并等待退出，运行记录为 failed（终态落库）。
func TestRunnerShutdownCancels(t *testing.T) {
	env := newRunnerEnv(t)
	release := env.exec.blockRun()
	job := env.mustJob(t, "long")

	handle, err := env.runner.Start(context.Background(), job.ID)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := env.runner.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	final, err := env.runner.Wait(context.Background(), handle)
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if final.State != RunFailed {
		t.Errorf("state = %q, want failed", final.State)
	}
	close(release)
}

// 运行失败的状态与错误被记录。
func TestRunnerRecordsFailure(t *testing.T) {
	env := newRunnerEnv(t)
	env.exec.failNext(errors.New("connection reset"))
	job := env.mustJob(t, "failing")

	handle, err := env.runner.Start(context.Background(), job.ID)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	final, err := env.runner.Wait(context.Background(), handle)
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if final.State != RunFailed || final.Error == "" {
		t.Errorf("final = %+v, want failed with error", final)
	}
}

// 未注册执行器的 kind 不可运行：Start 返回 ErrKindUnsupported。
func TestRunnerRejectsUnregisteredKind(t *testing.T) {
	env := newRunnerEnv(t)
	id, err := NewID()
	if err != nil {
		t.Fatalf("NewID: %v", err)
	}
	j := Job{ID: id, Name: "oci-" + id, Kind: KindOCI, Enabled: true, Config: json.RawMessage("{}")}
	if err := env.repo.Create(context.Background(), j); err != nil {
		t.Fatalf("create oci job: %v", err)
	}
	if _, err := env.runner.Start(context.Background(), j.ID); !errors.Is(err, ErrKindUnsupported) {
		t.Errorf("unregistered kind = %v, want ErrKindUnsupported", err)
	}
}

// StartWith 的 ConfigOverride 在配置快照路径内改写本轮配置（如 oci 的
// src_images 子集触发）：执行器收到改写后的快照，落库配置不受影响，
// override 返回错误时启动失败且不留 run 记录。
func TestStartWithConfigOverride(t *testing.T) {
	env := newRunnerEnv(t)
	job := env.mustJob(t, "override")

	handle, err := env.runner.StartWith(context.Background(), job.ID, StartOptions{
		ConfigOverride: func(j Job) (json.RawMessage, error) {
			return json.RawMessage(`{"src_images":["nginx:1.27"]}`), nil
		},
	})
	if err != nil {
		t.Fatalf("StartWith: %v", err)
	}
	env.exec.waitEntered(t)
	if _, err := env.runner.Wait(context.Background(), handle); err != nil {
		t.Fatalf("Wait: %v", err)
	}

	env.exec.mu.Lock()
	reqs := append([]RunRequest(nil), env.exec.reqs...)
	env.exec.mu.Unlock()
	if len(reqs) != 1 {
		t.Fatalf("executor runs = %d, want 1", len(reqs))
	}
	if got := string(reqs[0].Job.Config); got != `{"src_images":["nginx:1.27"]}` {
		t.Errorf("overridden config = %s, want src_images subset", got)
	}
	stored, err := env.repo.Get(context.Background(), job.ID)
	if err != nil {
		t.Fatalf("get job: %v", err)
	}
	if got := string(stored.Config); got != "{}" {
		t.Errorf("stored config = %s, want unchanged {}", got)
	}

	// override 校验失败：启动被拒（400 语义），无 run 记录。
	if _, err := env.runner.StartWith(context.Background(), job.ID, StartOptions{
		ConfigOverride: func(j Job) (json.RawMessage, error) {
			return nil, fmt.Errorf("%w: bad subset", ErrInvalid)
		},
	}); !errors.Is(err, ErrInvalid) {
		t.Errorf("override error = %v, want ErrInvalid", err)
	}
	if got := env.runCountGuard(t, job.ID); got != 1 {
		t.Errorf("run count after failed override = %d, want 1", got)
	}
}

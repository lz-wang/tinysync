package syncjob

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"tinysync/internal/source"
)

// blockingRemote 的 List 阻塞直到 ctx 取消或测试放行，模拟长运行同步。
type blockingRemote struct {
	release chan struct{}
}

func (b *blockingRemote) Stat(ctx context.Context, path string) (source.FileInfo, error) {
	return source.FileInfo{}, nil
}

func (b *blockingRemote) List(ctx context.Context, path string) ([]source.FileInfo, error) {
	select {
	case <-b.release:
		return nil, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (b *blockingRemote) Open(ctx context.Context, path string) (io.ReadCloser, error) {
	return nil, nil
}

func (b *blockingRemote) Close() error {
	return nil
}

// memCreds 是 SourceGateway 的内存实现。
type memCreds struct {
	source source.Source
	remote source.Remote
}

func (m *memCreds) Get(ctx context.Context, id string) (source.Source, error) {
	if m.source.ID != id {
		return source.Source{}, source.ErrNotFound
	}
	return m.source, nil
}

func (m *memCreds) OpenRemote(ctx context.Context, id string) (source.Source, source.Remote, error) {
	if m.source.ID != id {
		return source.Source{}, nil, source.ErrNotFound
	}
	return m.source, m.remote, nil
}

// memJobRepo 是 Repository 的内存实现（Runner 测试专用）。
type memJobRepo struct {
	jobs map[string]Job
}

func newMemJobRepo() *memJobRepo {
	return &memJobRepo{jobs: map[string]Job{}}
}

func (r *memJobRepo) Create(ctx context.Context, job Job) error {
	r.jobs[job.ID] = job
	return nil
}

func (r *memJobRepo) Get(ctx context.Context, id string) (Job, error) {
	job, ok := r.jobs[id]
	if !ok {
		return Job{}, fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	return job, nil
}

func (r *memJobRepo) List(ctx context.Context) ([]Job, error) {
	list := make([]Job, 0, len(r.jobs))
	for _, j := range r.jobs {
		list = append(list, j)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Name < list[j].Name })
	return list, nil
}

func (r *memJobRepo) Update(ctx context.Context, job Job) error {
	if _, ok := r.jobs[job.ID]; !ok {
		return fmt.Errorf("%w: %s", ErrNotFound, job.ID)
	}
	r.jobs[job.ID] = job
	return nil
}

func (r *memJobRepo) UpdateAndResetManaged(ctx context.Context, job Job) error {
	return r.Update(ctx, job)
}

func (r *memJobRepo) Delete(ctx context.Context, id string) error {
	delete(r.jobs, id)
	return nil
}

func (r *memJobRepo) CountBySource(ctx context.Context, sourceID string) (int, error) {
	return 0, nil
}

// memRunRepo 是 RunRepository 的内存实现（Runner 测试专用）。
type memRunRepo struct {
	mu     sync.Mutex
	runs   map[string]RunRecord
	order  []string
	prunes int
	// insertErr 非 nil 时 Insert / PersistScheduledRun 失败（模拟持久化故障）。
	insertErr error
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
	stored, ok := m.runs[run.ID]
	if !ok {
		return fmt.Errorf("%w: %s", ErrRunUnknown, run.ID)
	}
	stored.State = run.State
	stored.FinishedAt = run.FinishedAt
	stored.Stats = run.Stats
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

// runnerEnv 聚合 Runner 测试环境。
type runnerEnv struct {
	repo    *memJobRepo
	managed *inMemoryManaged
	creds   *memCreds
	history *memRunRepo
	runner  *Runner
	root    string
}

func newRunnerEnv(t *testing.T, remote source.Remote) *runnerEnv {
	t.Helper()
	env := &runnerEnv{
		repo:    newMemJobRepo(),
		managed: newInMemoryManaged(),
		creds: &memCreds{
			source: source.Source{
				ID: "src_a", Name: "nas", Type: source.TypeWebDAV,
				Config: source.Config{WebDAV: &source.WebDAVConfig{
					Endpoint: "https://dav.example.com/",
				}},
				Enabled: true,
			},
			remote: remote,
		},
		history: newMemRunRepo(),
		root:    t.TempDir(),
	}
	// 桥接 PersistScheduledRun 的事务性 once 消费写入（生产路径在
	// SQLite 事务内写 sync_jobs.once_consumed_for）。
	env.history.markOnce = func(jobID string, at time.Time) {
		job, ok := env.repo.jobs[jobID]
		if !ok {
			return
		}
		consumed := at
		job.OnceConsumedFor = &consumed
		env.repo.jobs[jobID] = job
	}
	env.runner = NewRunner(env.repo, env.managed, env.creds, env.history)
	env.runner.Now = func() time.Time { return time.Unix(1757879400, 0).UTC() }
	return env
}

// mustJob 创建启用的测试 Job（默认 LocalRoot）。
func (e *runnerEnv) mustJob(t *testing.T, name string) Job {
	t.Helper()
	return e.mustJobIn(t, name, e.root)
}

// mustJobIn 创建启用且指定 LocalRoot 的测试 Job。
func (e *runnerEnv) mustJobIn(t *testing.T, name, localRoot string) Job {
	t.Helper()
	id, err := NewID()
	if err != nil {
		t.Fatalf("NewID: %v", err)
	}
	now := time.Unix(1757879400, 0).UTC()
	job := Job{
		ID:         id,
		Name:       name + "-" + id,
		SourceID:   "src_a",
		RemoteRoot: "/",
		LocalRoot:  localRoot,
		Mode:       ModeCopy,
		Enabled:    true,
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	if err := e.repo.Create(context.Background(), job); err != nil {
		t.Fatalf("create job: %v", err)
	}
	return job
}

// closeTrackingRemote 包装 engineRemote 并记录 Close 调用次数。
type closeTrackingRemote struct {
	*engineRemote
	mu    sync.Mutex
	times int
}

func (c *closeTrackingRemote) Close() error {
	c.mu.Lock()
	c.times++
	c.mu.Unlock()
	return nil
}

func (c *closeTrackingRemote) closeCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.times
}

// 运行结束后 Remote 必须被释放：有连接生命周期的协议（如 SFTP）
// 不遗留会话。
func TestRunnerClosesRemoteAfterRun(t *testing.T) {
	remote := &closeTrackingRemote{
		engineRemote: buildRemote(map[string]string{"/a.txt": "v1"}, nil),
	}
	env := newRunnerEnv(t, remote)
	job := env.mustJob(t, "sync")

	runID, err := env.runner.Start(context.Background(), job.ID)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	status, err := env.runner.Wait(context.Background(), runID)
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if status.State != RunSucceeded {
		t.Fatalf("run state = %s, want succeeded", status.State)
	}
	if n := remote.closeCount(); n != 1 {
		t.Fatalf("remote close count = %d, want 1", n)
	}
}

// 手动运行完成：状态推进到 succeeded，统计与本地文件正确。
func TestRunnerCompletesSynchronously(t *testing.T) {
	remote := buildRemote(map[string]string{"/a.txt": "v1"}, nil)
	env := newRunnerEnv(t, remote)
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
	if final.Stats.FilesCreated != 1 {
		t.Errorf("stats = %+v, want 1 created", final.Stats)
	}
	data, err := os.ReadFile(filepath.Join(env.root, "a.txt"))
	if err != nil || string(data) != "v1" {
		t.Errorf("downloaded content = %q (%v), want v1", data, err)
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
	release := make(chan struct{})
	env := newRunnerEnv(t, &blockingRemote{release: release})
	jobA := env.mustJob(t, "a")
	jobB := env.mustJob(t, "b")
	ctx := context.Background()

	if _, err := env.runner.Start(ctx, jobA.ID); err != nil {
		t.Fatalf("Start A: %v", err)
	}
	// 同 Job 再触发。
	if _, err := env.runner.Start(ctx, jobA.ID); !errors.Is(err, ErrRunActive) {
		t.Errorf("Start same job during run = %v, want ErrRunActive", err)
	}
	// 默认容量 1：其他 Job 触发受全局限制。
	if _, err := env.runner.Start(ctx, jobB.ID); !errors.Is(err, ErrConcurrencyLimit) {
		t.Errorf("Start B at capacity = %v, want ErrConcurrencyLimit", err)
	}

	// 容量 2：两个 Job 并行。
	env.runner.MaxConcurrentJobs = 2
	if _, err := env.runner.Start(ctx, jobB.ID); err != nil {
		t.Fatalf("Start B with capacity 2: %v", err)
	}
	if !env.runner.IsRunning(jobA.ID) || !env.runner.IsRunning(jobB.ID) {
		t.Errorf("IsRunning = (%t, %t), want both running",
			env.runner.IsRunning(jobA.ID), env.runner.IsRunning(jobB.ID))
	}
	close(release)
	if err := env.runner.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if env.runner.IsRunning(jobA.ID) || env.runner.IsRunning(jobB.ID) {
		t.Error("jobs still running after Shutdown")
	}
}

// MaxConcurrentTransfers 是全进程上限：多个 Job 并行时，全部下载的
// 并发峰值不超过共享 TransferLimiter 容量——每个 Job 各持一份限额的
// 叠加（MaxConcurrentJobs × 上限）必须被排除。
func TestRunnerTransferLimitIsProcessWide(t *testing.T) {
	files := map[string]string{}
	for _, name := range []string{"/a1", "/a2", "/a3", "/a4", "/a5", "/b1", "/b2", "/b3", "/b4", "/b5"} {
		files[name] = "content-of" + name
	}
	probe := &probeRemote{
		engineRemote: buildRemote(files, nil),
		delay:        40 * time.Millisecond,
	}
	env := newRunnerEnv(t, probe)
	env.runner.MaxConcurrentJobs = 2
	env.runner.MaxConcurrentTransfers = 3

	jobA := env.mustJobIn(t, "a", t.TempDir())
	jobB := env.mustJobIn(t, "b", t.TempDir())
	ctx := context.Background()

	runA, err := env.runner.Start(ctx, jobA.ID)
	if err != nil {
		t.Fatalf("Start A: %v", err)
	}
	runB, err := env.runner.Start(ctx, jobB.ID)
	if err != nil {
		t.Fatalf("Start B: %v", err)
	}
	finalA, err := env.runner.Wait(ctx, runA)
	if err != nil {
		t.Fatalf("Wait A: %v", err)
	}
	if finalA.State != RunSucceeded {
		t.Errorf("run A state = %q (%s), want succeeded", finalA.State, finalA.Error)
	}
	finalB, err := env.runner.Wait(ctx, runB)
	if err != nil {
		t.Fatalf("Wait B: %v", err)
	}
	if finalB.State != RunSucceeded {
		t.Errorf("run B state = %q (%s), want succeeded", finalB.State, finalB.Error)
	}
	if probe.maxSeen > 3 {
		t.Errorf("process-wide concurrent Open peak = %d, want <= 3", probe.maxSeen)
	}
	if probe.maxSeen < 2 {
		t.Errorf("concurrent Open peak = %d, want cross-job overlap observed (>= 2)", probe.maxSeen)
	}
}

// Job 协调位：配置变更与执行链原子互斥。mutation 占用期间手动与
// 调度启动都报瞬时错误（occurrence 不被消费——几毫秒的配置互斥
// 不属于 overlap 策略，不该吞掉一次调度）；运行占用期间
// BeginMutation 报 ErrRunActive；各自释放后恢复。
func TestRunnerMutationGuard(t *testing.T) {
	release := make(chan struct{})
	env := newRunnerEnv(t, &blockingRemote{release: release})
	job := env.mustJob(t, "guarded")
	ctx := context.Background()

	if err := env.runner.BeginMutation(job.ID); err != nil {
		t.Fatalf("BeginMutation on idle job: %v", err)
	}
	if _, err := env.runner.Start(ctx, job.ID); !errors.Is(err, ErrJobMutating) {
		t.Errorf("Start during mutation = %v, want ErrJobMutating", err)
	}
	occ := time.Unix(1757879400, 0).UTC()
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
	release := make(chan struct{})
	env := newRunnerEnv(t, &blockingRemote{release: release})
	job := env.mustJob(t, "prune")
	ctx := context.Background()

	if _, err := env.runner.Start(ctx, job.ID); err != nil {
		t.Fatalf("Start: %v", err)
	}
	occ := time.Unix(1757879400, 0).UTC()
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
	release := make(chan struct{})
	env := newRunnerEnv(t, &blockingRemote{release: release})
	job := env.mustJob(t, "persist-fail")
	ctx := context.Background()

	if _, err := env.runner.Start(ctx, job.ID); err != nil {
		t.Fatalf("Start: %v", err)
	}
	occ := time.Unix(1757879400, 0).UTC()
	env.history.insertErr = errorsNew("disk I/O error")
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

// runCountGuard 统计 Job 的持久化 run 数（runnerEnv 的 history）。
func (e *runnerEnv) runCountGuard(t *testing.T, jobID string) int {
	t.Helper()
	_, total, err := e.history.List(context.Background(), RunFilter{JobID: jobID})
	if err != nil {
		t.Fatalf("list runs: %v", err)
	}
	return total
}

// Shutdown 后拒绝启动新运行：手动与调度触发都返回 ErrShuttingDown，
// 且 Shutdown 幂等。
func TestRunnerRejectsStartAfterShutdown(t *testing.T) {
	env := newRunnerEnv(t, buildRemote(map[string]string{"/a.txt": "v1"}, nil))
	job := env.mustJob(t, "late")
	ctx := context.Background()

	if err := env.runner.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if _, err := env.runner.Start(ctx, job.ID); !errors.Is(err, ErrShuttingDown) {
		t.Errorf("Start after shutdown = %v, want ErrShuttingDown", err)
	}
	occ := time.Unix(1757879400, 0).UTC()
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
	env := newRunnerEnv(t, buildRemote(map[string]string{"/a.txt": "v1"}, nil))
	job := env.mustJob(t, "insert-fail")
	ctx := context.Background()

	env.history.insertErr = errorsNew("disk I/O error")
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
	env := newRunnerEnv(t, buildRemote(map[string]string{"/a.txt": "v1"}, nil))
	ctx := context.Background()
	occ := time.Unix(1757879400, 0).UTC().Add(-time.Hour)

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
	if stored.OnceConsumedFor == nil || !stored.OnceConsumedFor.Equal(occ) {
		t.Errorf("OnceConsumedFor = %v, want %v", stored.OnceConsumedFor, occ)
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
	if stored2.OnceConsumedFor == nil || !stored2.OnceConsumedFor.Equal(occ2) {
		t.Errorf("skipped OnceConsumedFor = %v, want %v", stored2.OnceConsumedFor, occ2)
	}

	// NextRunAt 对已消费的 once 不再返回触发时刻。
	job2.Schedule.Value = occ.Format(time.RFC3339)
	job2.OnceConsumedFor = &occ
	if err := env.repo.Update(ctx, job2); err != nil {
		t.Fatalf("set job2 consumed: %v", err)
	}
	if _, ok, err := env.runner.NextRunAt(ctx, job2); err != nil || ok {
		t.Errorf("NextRunAt after consumption = (ok %t, %v), want false", ok, err)
	}
}

// 调度触发的 overlap 与容量不足不排队：记录 skipped run（occurrence
// 已消费、error 记原因），返回空 run ID 与 nil 错误。
func TestRunnerScheduledSkipped(t *testing.T) {
	release := make(chan struct{})
	env := newRunnerEnv(t, &blockingRemote{release: release})
	jobA := env.mustJob(t, "a")
	jobB := env.mustJob(t, "b")
	ctx := context.Background()

	if _, err := env.runner.Start(ctx, jobA.ID); err != nil {
		t.Fatalf("Start A: %v", err)
	}

	occ := time.Unix(1757879400, 0).UTC()
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

	// 全局容量不足（默认 1，Job A 占用）：其他 Job 的 occurrence 同样 skipped。
	runID, err = env.runner.StartScheduled(ctx, jobB.ID, TriggerCron, occ)
	if err != nil || runID != "" {
		t.Fatalf("StartScheduled at capacity = (%q, %v), want empty success", runID, err)
	}
	rec, err = env.history.Latest(ctx, jobB.ID)
	if err != nil {
		t.Fatalf("Latest B: %v", err)
	}
	if rec.State != RunSkipped || rec.Error != "concurrency limit reached" {
		t.Errorf("capacity skipped record = %+v, want skipped with concurrency reason", rec)
	}

	close(release)
	if err := env.runner.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
}

// run row 在 goroutine 启动前同步落库：Start 返回即可查到 running 记录，
// trigger 与 scheduled_for 正确；结束后的终态落库可查询。
func TestRunnerPersistsRun(t *testing.T) {
	release := make(chan struct{})
	env := newRunnerEnv(t, &blockingRemote{release: release})
	job := env.mustJob(t, "persist")
	ctx := context.Background()

	occ := time.Unix(1757879400, 0).UTC()
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

// 禁用的 Job 或 Source 拒绝运行。
func TestRunnerRejectsDisabled(t *testing.T) {
	remote := buildRemote(nil, nil)
	env := newRunnerEnv(t, remote)
	job := env.mustJob(t, "disabled")
	ctx := context.Background()

	updates := job
	updates.Enabled = false
	if err := env.repo.Update(ctx, updates); err != nil {
		t.Fatalf("disable job: %v", err)
	}
	if _, err := env.runner.Start(ctx, job.ID); !errors.Is(err, ErrJobDisabled) {
		t.Errorf("disabled job = %v, want ErrJobDisabled", err)
	}

	updates.Enabled = true
	if err := env.repo.Update(ctx, updates); err != nil {
		t.Fatalf("enable job: %v", err)
	}
	env.creds.source.Enabled = false
	if _, err := env.runner.Start(ctx, job.ID); !errors.Is(err, ErrSourceDisabled) {
		t.Errorf("disabled source = %v, want ErrSourceDisabled", err)
	}
}

// 不存在的 Job 报 ErrNotFound；无运行历史的 Job 状态为 idle。
func TestRunnerNotFoundAndIdleStatus(t *testing.T) {
	remote := buildRemote(nil, nil)
	env := newRunnerEnv(t, remote)
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

// Shutdown 取消运行中的同步并等待退出，运行记录为 failed（终态落库）。
func TestRunnerShutdownCancels(t *testing.T) {
	release := make(chan struct{})
	env := newRunnerEnv(t, &blockingRemote{release: release})
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
	// 远端 List 失败 → 扫描中止 → 本轮 failed。
	remote := buildRemote(nil, nil)
	remote.listErr = errors.New("connection reset")
	env := newRunnerEnv(t, remote)
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

// hangReader 的 Read 阻塞到连接关闭：不感知 ctx——模拟 *sftp.File.Read
// 这类不接受 context 的底层 I/O，只有连接被关闭才能让读取返回。
type hangReader struct {
	closed <-chan struct{}
}

func (r *hangReader) Read([]byte) (int, error) {
	<-r.closed
	return 0, errors.New("connection closed during read")
}

func (r *hangReader) Close() error { return nil }

// hangRemote 模拟有连接生命周期协议（SFTP）的传输模型：Open 返回阻塞
// reader，Close 关闭连接使阻塞中的读取返回。
type hangRemote struct {
	opened chan struct{} // Open 已被调用（读取已进入阻塞）
	closed chan struct{} // 连接已关闭
}

func (h *hangRemote) Stat(ctx context.Context, path string) (source.FileInfo, error) {
	return source.FileInfo{}, nil
}

func (h *hangRemote) List(ctx context.Context, path string) ([]source.FileInfo, error) {
	return []source.FileInfo{{
		Path:        "/big.bin",
		Fingerprint: source.Fingerprint{Size: 1 << 20},
	}}, nil
}

func (h *hangRemote) Open(ctx context.Context, path string) (io.ReadCloser, error) {
	select {
	case <-h.opened:
	default:
		close(h.opened)
	}
	return &hangReader{closed: h.closed}, nil
}

func (h *hangRemote) Close() error {
	select {
	case <-h.closed:
	default:
		close(h.closed)
	}
	return nil
}

// closingGateway 是感知 run 生命周期的 SourceGateway：OpenRemote 记录
// 收到的 ctx，并可阻塞模拟拨号；ctx 取消时触发连接关闭——与 SFTP
// Factory 内「ctx 取消关闭连接」的守护行为一致。
type closingGateway struct {
	src    source.Source
	remote *hangRemote
	// dial 非 nil 时 OpenRemote 阻塞直到放行或 ctx 取消（模拟拨号）。
	dial    <-chan struct{}
	mu      sync.Mutex
	openCtx context.Context
}

func (g *closingGateway) Get(ctx context.Context, id string) (source.Source, error) {
	return g.src, nil
}

func (g *closingGateway) OpenRemote(ctx context.Context, id string) (source.Source, source.Remote, error) {
	g.mu.Lock()
	g.openCtx = ctx
	g.mu.Unlock()
	if g.dial != nil {
		select {
		case <-g.dial:
		case <-ctx.Done():
			return source.Source{}, nil, ctx.Err()
		}
	}
	// 模拟 Factory 的关闭守护：创建用的 ctx 取消即关闭连接。
	go func() {
		<-ctx.Done()
		_ = g.remote.Close()
	}()
	return g.src, g.remote, nil
}

// hangRunnerEnv 在标准环境上用感知生命周期的 gateway 重建 Runner
// （固定时钟与 newRunnerEnv 保持一致）。
func hangRunnerEnv(t *testing.T, gw *closingGateway) *runnerEnv {
	t.Helper()
	env := newRunnerEnv(t, nil)
	env.runner = NewRunner(env.repo, env.managed, gw, env.history)
	env.runner.Now = func() time.Time { return time.Unix(1757879400, 0).UTC() }
	return env
}

// 回归：Shutdown 必须能打断阻塞中的远端读取。runCtx 是 Remote 创建、
// 关闭守护与引擎传输的唯一取消根——context 取消只能自父向子传播，
// 若 Remote 创建挂在 run 之外的独立（父）context 上，取消 runCtx 传
// 播不到连接，Shutdown 只能等传输自然结束（本测试超时失败）。
func TestRunnerShutdownInterruptsBlockingTransfer(t *testing.T) {
	remote := &hangRemote{opened: make(chan struct{}), closed: make(chan struct{})}
	gw := &closingGateway{src: source.Source{
		ID: "src_a", Name: "nas", Type: source.TypeWebDAV, Enabled: true,
	}, remote: remote}
	env := hangRunnerEnv(t, gw)
	job := env.mustJob(t, "hang")

	runID, err := env.runner.Start(context.Background(), job.ID)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	// 确认引擎已进入阻塞读取，Shutdown 面对的是真实的传输中连接。
	select {
	case <-remote.opened:
	case <-time.After(5 * time.Second):
		t.Fatal("engine never started reading the remote file")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := env.runner.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("Shutdown did not interrupt the blocking transfer: %v", err)
	}

	// 取消传播到 Remote 创建（关闭守护）用的 context 本身。
	gw.mu.Lock()
	openCtx := gw.openCtx
	gw.mu.Unlock()
	if openCtx == nil || openCtx.Err() == nil {
		t.Error("OpenRemote ctx was not cancelled by Shutdown")
	}

	status, err := env.runner.Wait(context.Background(), runID)
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if status.State != RunFailed {
		t.Errorf("run state after shutdown = %s (error %q), want failed", status.State, status.Error)
	}
}

// 回归：拨号中的启动阶段也必须可被 Shutdown 终止。Remote 创建在 run
// 发布（active / WaitGroup / running 记录）之后进行，启动窗口不再游离
// 于 shutdown 生命周期之外；创建失败记为 failed run 而不是启动接口的
// 同步错误。
func TestRunnerShutdownInterruptsPendingOpenRemote(t *testing.T) {
	remote := &hangRemote{closed: make(chan struct{})}
	gw := &closingGateway{
		src: source.Source{
			ID: "src_a", Name: "nas", Type: source.TypeWebDAV, Enabled: true,
		},
		remote: remote,
		dial:   make(chan struct{}),
	}
	env := hangRunnerEnv(t, gw)
	job := env.mustJob(t, "dial")

	runID, err := env.runner.Start(context.Background(), job.ID)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := env.runner.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("Shutdown did not interrupt pending OpenRemote: %v", err)
	}
	status, err := env.runner.Wait(context.Background(), runID)
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if status.State != RunFailed || !strings.Contains(status.Error, "create remote") {
		t.Errorf("run after shutdown = %s/%q, want failed with create remote error", status.State, status.Error)
	}
}

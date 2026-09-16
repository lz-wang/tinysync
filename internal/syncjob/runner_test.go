package syncjob

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
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

// stubFactory 是 source.RemoteFactory 的桩：返回构造时给定的 Remote。
type stubFactory struct {
	remote source.Remote
}

func (f stubFactory) Create(s source.Source, password string) (source.Remote, error) {
	return f.remote, nil
}

// memCreds 是 SourceCredentials 的内存实现。
type memCreds struct {
	source    source.Source
	passwords map[string]string
}

func (m *memCreds) Get(ctx context.Context, id string) (source.Source, error) {
	if m.source.ID != id {
		return source.Source{}, source.ErrNotFound
	}
	return m.source, nil
}

func (m *memCreds) GetPassword(ctx context.Context, id string) (string, error) {
	return m.passwords[id], nil
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
	mu    sync.Mutex
	runs  map[string]RunRecord
	order []string
}

func newMemRunRepo() *memRunRepo {
	return &memRunRepo{runs: map[string]RunRecord{}}
}

func (m *memRunRepo) Insert(ctx context.Context, run RunRecord) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.runs[run.ID] = run
	m.order = append(m.order, run.ID)
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

func (m *memRunRepo) PruneRetention(ctx context.Context, keepPerJob int) error { return nil }

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
				Endpoint: "https://dav.example.com/", Enabled: true,
			},
			passwords: map[string]string{"src_a": "secret"},
		},
		history: newMemRunRepo(),
		root:    t.TempDir(),
	}
	env.runner = NewRunner(env.repo, env.managed, env.creds, stubFactory{remote: remote}, env.history)
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

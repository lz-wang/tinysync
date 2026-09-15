package syncjob

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
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

func (r *memJobRepo) List(ctx context.Context) ([]Job, error) { return nil, nil }

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

// runnerEnv 聚合 Runner 测试环境。
type runnerEnv struct {
	repo    *memJobRepo
	managed *inMemoryManaged
	creds   *memCreds
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
		root: t.TempDir(),
	}
	env.runner = NewRunner(env.repo, env.managed, env.creds, stubFactory{remote: remote})
	env.runner.Now = func() time.Time { return time.Unix(1757879400, 0).UTC() }
	return env
}

// mustJob 创建启用的测试 Job。
func (e *runnerEnv) mustJob(t *testing.T, name string) Job {
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
		LocalRoot:  e.root,
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

	// 完成后查询状态保持 succeeded。
	status, err := env.runner.GetStatus(context.Background(), job.ID)
	if err != nil {
		t.Fatalf("GetStatus: %v", err)
	}
	if status.State != RunSucceeded {
		t.Errorf("GetStatus state = %q, want succeeded", status.State)
	}
}

// 全局同一时刻只允许一个同步运行：运行中触发另一个 Job 返回 ErrRunActive。
func TestRunnerEnforcesSingleRun(t *testing.T) {
	release := make(chan struct{})
	env := newRunnerEnv(t, &blockingRemote{release: release})
	jobA := env.mustJob(t, "a")
	jobB := env.mustJob(t, "b")

	if _, err := env.runner.Start(context.Background(), jobA.ID); err != nil {
		t.Fatalf("Start A: %v", err)
	}
	if _, err := env.runner.Start(context.Background(), jobB.ID); !errors.Is(err, ErrRunActive) {
		t.Errorf("Start B during A = %v, want ErrRunActive", err)
	}
	close(release)
	if err := env.runner.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
}

// 禁用的 Job 或 Source 拒绝运行。
func TestRunnerRejectsDisabled(t *testing.T) {
	remote := buildRemote(nil, nil)
	env := newRunnerEnv(t, remote)
	job := env.mustJob(t, "disabled")

	updates := job
	updates.Enabled = false
	if err := env.repo.Update(context.Background(), updates); err != nil {
		t.Fatalf("disable job: %v", err)
	}
	if _, err := env.runner.Start(context.Background(), job.ID); !errors.Is(err, ErrJobDisabled) {
		t.Errorf("disabled job = %v, want ErrJobDisabled", err)
	}

	updates.Enabled = true
	if err := env.repo.Update(context.Background(), updates); err != nil {
		t.Fatalf("enable job: %v", err)
	}
	env.creds.source.Enabled = false
	if _, err := env.runner.Start(context.Background(), job.ID); !errors.Is(err, ErrSourceDisabled) {
		t.Errorf("disabled source = %v, want ErrSourceDisabled", err)
	}
}

// 不存在的 Job 报 ErrNotFound；无运行历史的 Job 状态为 idle。
func TestRunnerNotFoundAndIdleStatus(t *testing.T) {
	remote := buildRemote(nil, nil)
	env := newRunnerEnv(t, remote)
	job := env.mustJob(t, "idle")

	if _, err := env.runner.Start(context.Background(), "job_missing"); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing job = %v, want ErrNotFound", err)
	}
	status, err := env.runner.GetStatus(context.Background(), job.ID)
	if err != nil {
		t.Fatalf("GetStatus: %v", err)
	}
	if status.State != RunIdle {
		t.Errorf("initial state = %q, want idle", status.State)
	}
}

// Shutdown 取消运行中的同步并等待退出，运行记录为 failed。
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

// 优雅关闭的终态持久化：真实 SQLite 下验证 Shutdown 取消运行后，
// failed 终态在本进程内立即落库——不依赖「下次启动的 stale 恢复」
// 才能完成终态收敛。Finalize 若使用已取消的运行 context，真实
// SQLite 会直接失败并遗留 running 行，本测试捕捉该回归。
package syncjob_test

import (
	"context"
	"io"
	"testing"
	"time"

	"tinysync/internal/source"
	"tinysync/internal/storage"
	"tinysync/internal/syncjob"
	jobsqlite "tinysync/internal/syncjob/sqlite"
)

// gateRemote 的 List 阻塞直到放行或 ctx 取消，模拟长运行同步。
type gateRemote struct{ gate chan struct{} }

func (b *gateRemote) Stat(ctx context.Context, path string) (source.FileInfo, error) {
	return source.FileInfo{}, nil
}

func (b *gateRemote) List(ctx context.Context, path string) ([]source.FileInfo, error) {
	select {
	case <-b.gate:
		return nil, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (b *gateRemote) Open(ctx context.Context, path string) (io.ReadCloser, error) {
	return nil, nil
}

func (b *gateRemote) Close() error {
	return nil
}

// staticCreds / staticFactory 提供固定的 Source 与远端。
type staticCreds struct{}

func (staticCreds) Get(ctx context.Context, id string) (source.Source, error) {
	return source.Source{ID: id, Name: "nas", Type: source.TypeWebDAV, Enabled: true}, nil
}

func (staticCreds) GetPassword(ctx context.Context, id string) (string, error) {
	return "secret", nil
}

type staticFactory struct{ remote source.Remote }

func (f staticFactory) Create(ctx context.Context, s source.Source, password string) (source.Remote, error) {
	return f.remote, nil
}

func TestRunnerShutdownPersistsTerminalStateWithSQLite(t *testing.T) {
	dataDir := t.TempDir()
	db, err := storage.Open(dataDir)
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := storage.Migrate(context.Background(), db, dataDir); err != nil {
		t.Fatalf("storage.Migrate: %v", err)
	}
	// 种子 Source（Job 落库的 FK 依赖；Runner 用 stub 凭据不查库）。
	if _, err := db.Exec(`INSERT INTO sources
		(id, name, type, endpoint, username, password, enabled, created_at, updated_at)
		VALUES ('src_x', 'nas', 'webdav', 'https://dav.example.com/', '', '', 1, 1, 1)`); err != nil {
		t.Fatalf("seed source: %v", err)
	}

	jobRepo := jobsqlite.NewRepository(db)
	managedRepo := jobsqlite.NewManagedRepository(db)
	runRepo := jobsqlite.NewRunRepository(db)

	gate := make(chan struct{})
	runner := syncjob.NewRunner(jobRepo, managedRepo, staticCreds{}, staticFactory{remote: &gateRemote{gate: gate}}, runRepo)

	now := time.Now().UTC().Add(-time.Minute)
	job := syncjob.Job{
		ID: "job_shutdown", Name: "shutdown", SourceID: "src_x",
		RemoteRoot: "/", LocalRoot: t.TempDir(), Mode: syncjob.ModeCopy,
		Enabled: true, Schedule: syncjob.Schedule{Type: syncjob.ScheduleManual},
		CreatedAt: now, UpdatedAt: now,
	}
	if err := jobRepo.Create(context.Background(), job); err != nil {
		t.Fatalf("create job: %v", err)
	}

	runID, err := runner.Start(context.Background(), job.ID)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	// 运行卡在远端 List；确认 running 记录已落库。
	deadline := time.Now().Add(5 * time.Second)
	for {
		rec, getErr := runRepo.Get(context.Background(), runID)
		if getErr == nil && rec.State == syncjob.RunRunning {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("run never reached running state: %+v (%v)", rec, getErr)
		}
		time.Sleep(10 * time.Millisecond)
	}

	// 优雅关闭：取消运行并等待退出。
	if err := runner.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}

	// 终态在本次进程内已落库：failed + finished_at（不遗留 running）。
	rec, err := runRepo.Get(context.Background(), runID)
	if err != nil {
		t.Fatalf("get run after shutdown: %v", err)
	}
	if rec.State != syncjob.RunFailed {
		t.Errorf("run state after shutdown = %q (error %q), want failed", rec.State, rec.Error)
	}
	if rec.FinishedAt == nil {
		t.Error("run after shutdown has no finished_at")
	}
}

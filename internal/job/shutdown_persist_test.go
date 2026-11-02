// 优雅关闭的终态持久化：真实 SQLite 下验证 Shutdown 取消运行后，
// failed 终态在本进程内立即落库——不依赖「下次启动的 stale 恢复」
// 才能完成终态收敛。Finalize 若使用已取消的运行 context，真实
// SQLite 会直接失败并遗留 running 行，本测试捕捉该回归。
package job_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"tinysync/internal/job"
	jobsqlite "tinysync/internal/job/sqlite"
	"tinysync/internal/storage"
)

// gateExecutor 的 Run 阻塞到 ctx 取消或放行，模拟长运行同步；取消
// 沿 runCtx 传播进来（Runner 的唯一取消根），返回 ctx 错误。
type gateExecutor struct {
	gate chan struct{}
}

// Validate 实现 Executor：桩不做领域校验，配置原样返回。
func (e *gateExecutor) Validate(ctx context.Context, jobID string, cfg json.RawMessage) (json.RawMessage, error) {
	return cfg, nil
}

func (e *gateExecutor) Run(ctx context.Context, req job.RunRequest) (job.RunResult, error) {
	select {
	case <-e.gate:
		return job.RunResult{}, nil
	case <-ctx.Done():
		return job.RunResult{}, ctx.Err()
	}
}

func TestRunnerShutdownPersistsTerminalStateWithSQLite(t *testing.T) {
	dataDir := t.TempDir()
	db, err := storage.Open(dataDir)
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := storage.Ensure(context.Background(), db); err != nil {
		t.Fatalf("storage.Ensure: %v", err)
	}

	jobRepo := jobsqlite.NewRepository(db)
	runRepo := jobsqlite.NewRunRepository(db)
	runner := job.NewRunner(jobRepo, runRepo, map[job.JobKind]job.Executor{
		job.KindFile: &gateExecutor{gate: make(chan struct{})},
	})

	now := time.Now().UTC().Add(-time.Minute)
	j := job.Job{
		ID: "job_shutdown", Name: "shutdown", Kind: job.KindFile,
		Enabled: true, Config: []byte("{}"),
		CreatedAt: now, UpdatedAt: now,
	}
	if err := jobRepo.Create(context.Background(), j); err != nil {
		t.Fatalf("create job: %v", err)
	}

	runID, err := runner.Start(context.Background(), j.ID)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	// 运行卡在执行器内；确认 running 记录已落库。
	deadline := time.Now().Add(5 * time.Second)
	for {
		rec, getErr := runRepo.Get(context.Background(), runID)
		if getErr == nil && rec.State == job.RunRunning {
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
	if rec.State != job.RunFailed {
		t.Errorf("run state after shutdown = %q (error %q), want failed", rec.State, rec.Error)
	}
	if rec.FinishedAt == nil {
		t.Error("run after shutdown has no finished_at")
	}
}

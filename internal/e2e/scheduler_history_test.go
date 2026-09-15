// 调度与同步历史的端到端覆盖：真实 WebDAV 服务端 + 真实 SQLite +
// 真实 Scheduler / Runner，覆盖自动同步、once 补执行、容量跳过、
// 跨重启历史与 stale 恢复、retention 级联。
package e2e

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"tinysync/internal/source"
	sourcesqlite "tinysync/internal/source/sqlite"
	"tinysync/internal/source/webdav"
	"tinysync/internal/syncjob"
	jobsqlite "tinysync/internal/syncjob/sqlite"
)

// setInterval 把 Job 调度改为 interval，anchor 回退到过去
// （now - (every - firesIn)），使首个边界在 firesIn 后到期。
func (e *env) setInterval(t *testing.T, jobID string, every time.Duration, firesIn time.Duration) {
	t.Helper()
	ctx := context.Background()
	job, err := e.jobRepo.Get(ctx, jobID)
	if err != nil {
		t.Fatalf("get job: %v", err)
	}
	anchor := time.Now().UTC().Add(firesIn - every)
	job.Schedule = syncjob.Schedule{
		Type:     syncjob.ScheduleInterval,
		Value:    every.String(),
		AnchorAt: &anchor,
	}
	if err := e.jobRepo.Update(ctx, job); err != nil {
		t.Fatalf("set interval schedule: %v", err)
	}
}

// setOnce 把 Job 调度改为 once（at 可为过去时刻，验证离线补执行）。
func (e *env) setOnce(t *testing.T, jobID string, at time.Time) {
	t.Helper()
	ctx := context.Background()
	job, err := e.jobRepo.Get(ctx, jobID)
	if err != nil {
		t.Fatalf("get job: %v", err)
	}
	job.Schedule = syncjob.Schedule{
		Type:  syncjob.ScheduleOnce,
		Value: at.UTC().Format(time.RFC3339),
	}
	if err := e.jobRepo.Update(ctx, job); err != nil {
		t.Fatalf("set once schedule: %v", err)
	}
}

// newRunRepo 返回当前数据库的运行历史仓库。
func (e *env) newRunRepo() *jobsqlite.RunRepository {
	return jobsqlite.NewRunRepository(e.db)
}

// waitFor 轮询条件直到满足或超时。
func waitFor(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// 自动同步：interval 边界到期后无人工介入完成同步，运行历史记录
// trigger=interval 与 occurrence，未到期的下一个边界不重复触发。
func TestSchedulerAutoSyncEndToEnd(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.dav.writeFile(t, "/a.txt", "v1")
	sourceID := e.createSource(t)
	job := e.createJob(t, "Auto Sync", sourceID, string(syncjob.ModeCopy))
	e.setInterval(t, job.ID, 30*time.Minute, 3*time.Second)

	runRepo := e.newRunRepo()
	scheduler := syncjob.NewScheduler(e.jobRepo, e.runner, runRepo)
	scheduler.Start(ctx)
	defer scheduler.Stop()

	local := filepath.Join(job.LocalRoot, "a.txt")
	waitFor(t, 15*time.Second, "scheduled download", func() bool {
		_, err := os.Stat(local)
		return err == nil
	})
	waitFor(t, 15*time.Second, "run finalize", func() bool {
		rec, err := runRepo.Latest(ctx, job.ID)
		return err == nil && rec.State != syncjob.RunRunning
	})
	rec, err := runRepo.Latest(ctx, job.ID)
	if err != nil {
		t.Fatalf("latest run: %v", err)
	}
	if rec.Trigger != syncjob.TriggerInterval || rec.State != syncjob.RunSucceeded ||
		rec.ScheduledFor == nil {
		t.Fatalf("scheduled run = %+v, want succeeded interval with occurrence", rec)
	}
	if got := readLocal(t, local); got != "v1" {
		t.Errorf("auto synced content = %q, want v1", got)
	}

	// 下一个边界在 30m 之后：短时间内不得新增 run（不排队不重放）。
	time.Sleep(1500 * time.Millisecond)
	if _, total, err := runRepo.List(ctx, syncjob.RunFilter{JobID: job.ID}); err != nil || total != 1 {
		t.Errorf("runs after idle window = %d (%v), want 1", total, err)
	}
}

// once 到期触发；调度器重启后同一 occurrence 已消费不再触发。
func TestSchedulerOnceCatchUpEndToEnd(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.dav.writeFile(t, "/a.txt", "v1")
	sourceID := e.createSource(t)
	job := e.createJob(t, "Once Sync", sourceID, string(syncjob.ModeMirror))
	// once 时刻定在过去：模拟离线期间错过的调度，恢复后补执行一次。
	e.setOnce(t, job.ID, time.Now().UTC().Add(-time.Hour))

	runRepo := e.newRunRepo()
	scheduler := syncjob.NewScheduler(e.jobRepo, e.runner, runRepo)
	scheduler.Start(ctx)

	local := filepath.Join(job.LocalRoot, "a.txt")
	waitFor(t, 15*time.Second, "catch-up download", func() bool {
		_, err := os.Stat(local)
		return err == nil
	})
	waitFor(t, 15*time.Second, "once run finalize", func() bool {
		rec, err := runRepo.Latest(ctx, job.ID)
		return err == nil && rec.State == syncjob.RunSucceeded
	})
	rec, _ := runRepo.Latest(ctx, job.ID)
	if rec.Trigger != syncjob.TriggerOnce || rec.ScheduledFor == nil {
		t.Fatalf("once run = %+v, want trigger once with occurrence", rec)
	}
	scheduler.Stop()

	// 调度器重启（新游标）：once 已消费，不再重复执行。
	scheduler2 := syncjob.NewScheduler(e.jobRepo, e.runner, runRepo)
	scheduler2.Start(ctx)
	time.Sleep(2 * time.Second)
	scheduler2.Stop()
	if _, total, err := runRepo.List(ctx, syncjob.RunFilter{JobID: job.ID}); err != nil || total != 1 {
		t.Errorf("runs after scheduler restart = %d (%v), want 1 (consumed)", total, err)
	}
}

// 全局容量：两个 Job 的 once 同时到期，容量 1 时后到者记 skipped
// （occurrence 已消费），先到者正常完成。
func TestSchedulerCapacitySkipsEndToEnd(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.dav.writeFile(t, "/a.txt", "v1")
	sourceID := e.createSource(t)
	jobA := e.createJob(t, "Capacity A", sourceID, string(syncjob.ModeCopy))
	jobB := e.createJob(t, "Capacity B", sourceID, string(syncjob.ModeCopy))
	at := time.Now().UTC().Add(2 * time.Second)
	e.setOnce(t, jobA.ID, at)
	e.setOnce(t, jobB.ID, at)

	runRepo := e.newRunRepo()
	scheduler := syncjob.NewScheduler(e.jobRepo, e.runner, runRepo)
	scheduler.Start(ctx)
	defer scheduler.Stop()

	waitFor(t, 15*time.Second, "capacity skip record", func() bool {
		rec, err := runRepo.Latest(ctx, jobB.ID)
		return err == nil && rec.State == syncjob.RunSkipped
	})
	recB, _ := runRepo.Latest(ctx, jobB.ID)
	if recB.Trigger != syncjob.TriggerOnce || recB.Error != "concurrency limit reached" {
		t.Errorf("skipped run = %+v, want once skipped by concurrency limit", recB)
	}
	if recB.ScheduledFor == nil || !recB.ScheduledFor.Equal(at.Truncate(time.Second)) && !recB.ScheduledFor.Equal(at) {
		t.Errorf("skipped occurrence = %v, want %v", recB.ScheduledFor, at)
	}

	localA := filepath.Join(jobA.LocalRoot, "a.txt")
	waitFor(t, 15*time.Second, "job A download", func() bool {
		_, err := os.Stat(localA)
		return err == nil
	})
	waitFor(t, 15*time.Second, "job A finalize", func() bool {
		rec, err := runRepo.Latest(ctx, jobA.ID)
		return err == nil && rec.State == syncjob.RunSucceeded
	})
}

// 跨重启：completed run 与文件明细仍可查询；遗留 running 收敛为 failed
// （与 app 启动恢复一致）；重启后的 Runner 继续正常同步。
func TestRunHistoryAcrossRestartEndToEnd(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.dav.writeFile(t, "/a.txt", "v1")
	sourceID := e.createSource(t)
	job := e.createJob(t, "Restart History", sourceID, string(syncjob.ModeMirror))
	first := e.runAndWait(t, job.ID)
	requireSucceeded(t, "first run", first)

	// 模拟进程崩溃遗留的 running 记录。
	runRepo := e.newRunRepo()
	staleAt := time.Now().UTC().Add(-time.Hour)
	if err := runRepo.Insert(ctx, syncjob.RunRecord{
		ID:        "run_stale_e2e",
		JobID:     job.ID,
		Trigger:   syncjob.TriggerManual,
		State:     syncjob.RunRunning,
		StartedAt: staleAt,
	}); err != nil {
		t.Fatalf("insert stale run: %v", err)
	}

	if err := e.closeDB(t); err != nil {
		t.Fatalf("close db: %v", err)
	}

	db := openDB(t, e.dataDir)
	t.Cleanup(func() { _ = db.Close() })
	sources2 := source.NewService(sourcesqlite.New(db), webdav.NewFactory())
	jobRepo2 := jobsqlite.NewRepository(db)
	managedRepo2 := jobsqlite.NewManagedRepository(db)
	runRepo2 := jobsqlite.NewRunRepository(db)
	runner2 := syncjob.NewRunner(jobRepo2, managedRepo2, sources2, webdav.NewFactory(), runRepo2)

	// 启动恢复（与 app.Run 相同语义）：遗留 running 收敛为 failed。
	recovered, err := runRepo2.FailStaleRunning(ctx, time.Now().UTC(), "previous process interrupted")
	if err != nil {
		t.Fatalf("recover stale runs: %v", err)
	}
	if recovered != 1 {
		t.Fatalf("recovered = %d, want 1", recovered)
	}

	// completed run 与明细仍可查询（List 为 started_at 倒序）。
	runs, total, err := runner2.ListRuns(ctx, syncjob.RunFilter{JobID: job.ID})
	if err != nil || total != 2 {
		t.Fatalf("runs after restart = %d (%v), want 2", total, err)
	}
	if runs[0].ID != first.RunID || runs[0].State != syncjob.RunSucceeded {
		t.Fatalf("latest run = %+v, want succeeded %s", runs[0], first.RunID)
	}
	if runs[1].ID != "run_stale_e2e" || runs[1].State != syncjob.RunFailed ||
		runs[1].Error != "previous process interrupted" {
		t.Fatalf("stale run = %+v, want converged to failed with reason", runs[1])
	}
	items, itemTotal, err := runner2.ListRunItems(ctx, first.RunID, 50, 0)
	if err != nil || itemTotal != 1 || len(items) != 1 {
		t.Fatalf("items after restart = (%d rows, total %d, %v), want 1", len(items), itemTotal, err)
	}
	if items[0].Path != "a.txt" || items[0].Action != syncjob.ItemCreate {
		t.Errorf("item = %+v, want create a.txt", items[0])
	}

	// 重启后的 Runner 继续收敛：远端 v2 → update 成功。
	e.dav.writeFile(t, "/a.txt", "v2")
	runID, err := runner2.Start(ctx, job.ID)
	if err != nil {
		t.Fatalf("start run after restart: %v", err)
	}
	runCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	final, err := runner2.Wait(runCtx, runID)
	if err != nil {
		t.Fatalf("wait run after restart: %v", err)
	}
	requireSucceeded(t, "post-restart run", final)
	if final.Stats.FilesUpdated != 1 {
		t.Errorf("post-restart stats = %+v, want updated=1", final.Stats)
	}
}

// retention：每 Job 只保留最近 N 条 run，被裁剪 run 的明细级联删除，
// 真实本地文件不受影响。
func TestRunRetentionEndToEnd(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	sourceID := e.createSource(t)
	job := e.createJob(t, "Retention", sourceID, string(syncjob.ModeMirror))

	for _, version := range []string{"v1", "v2", "v3"} {
		e.dav.writeFile(t, "/a.txt", version)
		requireSucceeded(t, "run "+version, e.runAndWait(t, job.ID))
	}

	runRepo := e.newRunRepo()
	runs, total, err := runRepo.List(ctx, syncjob.RunFilter{JobID: job.ID})
	if err != nil || total != 3 {
		t.Fatalf("runs before prune = %d (%v), want 3", total, err)
	}
	oldest := runs[2] // List 为 started_at 倒序，末位最旧

	if err := runRepo.PruneRetention(ctx, 2); err != nil {
		t.Fatalf("prune retention: %v", err)
	}
	runs, total, err = runRepo.List(ctx, syncjob.RunFilter{JobID: job.ID})
	if err != nil || total != 2 {
		t.Fatalf("runs after prune = %d (%v), want 2", total, err)
	}
	for _, run := range runs {
		if run.ID == oldest.ID {
			t.Errorf("oldest run %s survived prune", oldest.ID)
		}
	}
	if _, itemTotal, err := runRepo.Items(ctx, oldest.ID, 10, 0); err != nil || itemTotal != 0 {
		t.Errorf("items of pruned run = (total %d, %v), want 0 (cascade)", itemTotal, err)
	}
	keptItems, itemTotal, err := runRepo.Items(ctx, runs[0].ID, 10, 0)
	if err != nil || itemTotal != 1 || len(keptItems) != 1 {
		t.Errorf("items of kept run = (%d rows, total %d, %v), want 1", len(keptItems), itemTotal, err)
	}

	// 真实本地文件保留在最新版本。
	local := filepath.Join(job.LocalRoot, "a.txt")
	if got := readLocal(t, local); got != "v3" {
		t.Errorf("local after prune = %q, want v3", got)
	}
}

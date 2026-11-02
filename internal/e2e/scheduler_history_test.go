// 调度与同步历史的端到端覆盖：真实 WebDAV 服务端 + 真实 SQLite +
// 真实 Scheduler / Runner，覆盖自动同步、once 补执行、容量跳过、
// 跨重启历史与 stale 恢复、retention 级联。
package e2e

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	job2 "tinysync/internal/job"
	jobsqlite "tinysync/internal/job/sqlite"
	"tinysync/internal/source"
	sourcesqlite "tinysync/internal/source/sqlite"
	"tinysync/internal/source/webdav"
	"tinysync/internal/syncjob"
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
		Type:     job2.ScheduleInterval,
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
		Type:  job2.ScheduleOnce,
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
	scheduler := job2.NewScheduler(e.jobRepo, e.runner, runRepo)
	scheduler.Start(ctx)
	defer scheduler.Stop()

	local := filepath.Join(job.LocalRoot, "a.txt")
	waitFor(t, 15*time.Second, "scheduled download", func() bool {
		_, err := os.Stat(local)
		return err == nil
	})
	waitFor(t, 15*time.Second, "run finalize", func() bool {
		rec, err := runRepo.Latest(ctx, job.ID)
		return err == nil && rec.State != job2.RunRunning
	})
	rec, err := runRepo.Latest(ctx, job.ID)
	if err != nil {
		t.Fatalf("latest run: %v", err)
	}
	if rec.Trigger != job2.TriggerInterval || rec.State != job2.RunSucceeded ||
		rec.ScheduledFor == nil {
		t.Fatalf("scheduled run = %+v, want succeeded interval with occurrence", rec)
	}
	if got := readLocal(t, local); got != "v1" {
		t.Errorf("auto synced content = %q, want v1", got)
	}

	// 下一个边界在 30m 之后：短时间内不得新增 run（不排队不重放）。
	time.Sleep(1500 * time.Millisecond)
	if _, total, err := runRepo.List(ctx, job2.RunFilter{JobID: job.ID}); err != nil || total != 1 {
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
	scheduler := job2.NewScheduler(e.jobRepo, e.runner, runRepo)
	scheduler.Start(ctx)

	local := filepath.Join(job.LocalRoot, "a.txt")
	waitFor(t, 15*time.Second, "catch-up download", func() bool {
		_, err := os.Stat(local)
		return err == nil
	})
	waitFor(t, 15*time.Second, "once run finalize", func() bool {
		rec, err := runRepo.Latest(ctx, job.ID)
		return err == nil && rec.State == job2.RunSucceeded
	})
	rec, _ := runRepo.Latest(ctx, job.ID)
	if rec.Trigger != job2.TriggerOnce || rec.ScheduledFor == nil {
		t.Fatalf("once run = %+v, want trigger once with occurrence", rec)
	}
	scheduler.Stop()

	// 调度器重启（新游标）：once 已消费，不再重复执行。
	scheduler2 := job2.NewScheduler(e.jobRepo, e.runner, runRepo)
	scheduler2.Start(ctx)
	time.Sleep(2 * time.Second)
	scheduler2.Stop()
	if _, total, err := runRepo.List(ctx, job2.RunFilter{JobID: job.ID}); err != nil || total != 1 {
		t.Errorf("runs after scheduler restart = %d (%v), want 1 (consumed)", total, err)
	}
}

// once 消费状态独立于可裁剪的运行历史：once 执行后，即使该 run 连同
// 整个历史被 retention 裁剪，调度器也不重放 once——「只执行一次」
// 靠 jobs.once_consumed_at 维持，不靠 job_runs 行存活。
func TestOnceConsumedSurvivesRetentionEndToEnd(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.dav.writeFile(t, "/a.txt", "v1")
	sourceID := e.createSource(t)
	job := e.createJob(t, "Once Retention", sourceID, string(syncjob.ModeMirror))
	e.setOnce(t, job.ID, time.Now().UTC().Add(-time.Hour))

	runRepo := e.newRunRepo()
	scheduler := job2.NewScheduler(e.jobRepo, e.runner, runRepo)
	scheduler.Start(ctx)

	local := filepath.Join(job.LocalRoot, "a.txt")
	waitFor(t, 15*time.Second, "once catch-up", func() bool {
		_, err := os.Stat(local)
		return err == nil
	})
	waitFor(t, 15*time.Second, "once finalize", func() bool {
		rec, err := runRepo.Latest(ctx, job.ID)
		return err == nil && rec.State == job2.RunSucceeded
	})
	onceRun, err := runRepo.Latest(ctx, job.ID)
	if err != nil {
		t.Fatalf("latest once run: %v", err)
	}
	scheduler.Stop()

	// 消费状态已写入 Job。
	fresh, err := e.jobRepo.Get(ctx, job.ID)
	if err != nil {
		t.Fatalf("get job: %v", err)
	}
	if fresh.OnceConsumedAt == nil || !fresh.OnceConsumedAt.Equal(*onceRun.ScheduledFor) {
		t.Fatalf("OnceConsumedAt = %v, want occurrence %v", fresh.OnceConsumedAt, onceRun.ScheduledFor)
	}

	// 填充超过 retention 上限的运行并裁剪：once run 被裁剪出历史。
	for i := 0; i < job2.RetentionRunsPerJob+1; i++ {
		if err := runRepo.Insert(ctx, job2.RunRecord{
			ID:        fmt.Sprintf("run_fill_%04d", i),
			JobKind:   job2.KindFile,
			JobID:     job.ID,
			Trigger:   job2.TriggerManual,
			State:     job2.RunSucceeded,
			StartedAt: onceRun.StartedAt.Add(time.Duration(i+1) * time.Millisecond),
		}); err != nil {
			t.Fatalf("insert filler run: %v", err)
		}
	}
	if err := runRepo.PruneRetention(ctx, job2.RetentionRunsPerJob); err != nil {
		t.Fatalf("prune retention: %v", err)
	}
	if _, err := runRepo.Get(ctx, onceRun.ID); err == nil {
		t.Fatalf("once run %s survived prune, want pruned", onceRun.ID)
	}

	// 调度器继续运行：once 不重放（若重放会出现第 501 条之外的 run）。
	scheduler2 := job2.NewScheduler(e.jobRepo, e.runner, runRepo)
	scheduler2.Start(ctx)
	time.Sleep(3 * time.Second)
	scheduler2.Stop()
	_, total, err := runRepo.List(ctx, job2.RunFilter{JobID: job.ID})
	if err != nil || total != job2.RetentionRunsPerJob {
		t.Errorf("runs after retention + idle window = %d (%v), want %d (no once replay)", total, err, job2.RetentionRunsPerJob)
	}
}

// 全局容量（已决 R4）：两个 Job 的 once 同时到期，容量 1 时后到者被
// 瞬时拒绝——occurrence 不消费、不记 skipped，调度器保持游标在后续
// 周期重试；先到者完成后，后到者重试并正常完成。
func TestSchedulerCapacityThrottleRetriesEndToEnd(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.dav.writeFile(t, "/a.txt", "v1")
	sourceID := e.createSource(t)
	jobA := e.createJob(t, "Capacity A", sourceID, string(syncjob.ModeCopy))
	jobB := e.createJob(t, "Capacity B", sourceID, string(syncjob.ModeCopy))
	e.runner.MaxConcurrentJobs = 1
	at := time.Now().UTC().Add(2 * time.Second)
	e.setOnce(t, jobA.ID, at)
	e.setOnce(t, jobB.ID, at)

	runRepo := e.newRunRepo()
	scheduler := job2.NewScheduler(e.jobRepo, e.runner, runRepo)
	scheduler.Start(ctx)
	defer scheduler.Stop()

	localA := filepath.Join(jobA.LocalRoot, "a.txt")
	waitFor(t, 15*time.Second, "job A download", func() bool {
		_, err := os.Stat(localA)
		return err == nil
	})
	waitFor(t, 15*time.Second, "job A finalize", func() bool {
		rec, err := runRepo.Latest(ctx, jobA.ID)
		return err == nil && rec.State == job2.RunSucceeded
	})

	// A 收敛释放容量后，B 的被拒 occurrence 在后续 tick 重试成功。
	localB := filepath.Join(jobB.LocalRoot, "a.txt")
	waitFor(t, 15*time.Second, "job B retry after capacity released", func() bool {
		rec, err := runRepo.Latest(ctx, jobB.ID)
		return err == nil && rec.State == job2.RunSucceeded
	})
	if _, err := os.Stat(localB); err != nil {
		t.Errorf("job B download after retry: %v", err)
	}
	// 容量满只拒绝不消费：B 的历史里没有 skipped 记录。
	runs, total, err := runRepo.List(ctx, job2.RunFilter{JobID: jobB.ID})
	if err != nil {
		t.Fatalf("list job B runs: %v", err)
	}
	if total != 1 || runs[0].State != job2.RunSucceeded || runs[0].Trigger != job2.TriggerOnce {
		t.Errorf("job B runs = %+v (total %d), want exactly one succeeded once", runs, total)
	}
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
	if err := runRepo.Insert(ctx, job2.RunRecord{
		ID:        "run_stale_e2e",
		JobKind:   job2.KindFile,
		JobID:     job.ID,
		Trigger:   job2.TriggerManual,
		State:     job2.RunRunning,
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
	stack2 := newFileJobStack(db, sources2, e.dataDir)
	runRepo2 := stack2.Runs
	runner2 := stack2.Runner

	// 启动恢复（与 app.Run 相同语义）：遗留 running 收敛为 failed。
	recovered, err := runRepo2.FailStaleRunning(ctx, time.Now().UTC(), "previous process interrupted")
	if err != nil {
		t.Fatalf("recover stale runs: %v", err)
	}
	if recovered != 1 {
		t.Fatalf("recovered = %d, want 1", recovered)
	}

	// completed run 与明细仍可查询（List 为 started_at 倒序）。
	runs, total, err := runner2.ListRuns(ctx, job2.RunFilter{JobID: job.ID})
	if err != nil || total != 2 {
		t.Fatalf("runs after restart = %d (%v), want 2", total, err)
	}
	if runs[0].ID != first.RunID || runs[0].State != job2.RunSucceeded {
		t.Fatalf("latest run = %+v, want succeeded %s", runs[0], first.RunID)
	}
	if runs[1].ID != "run_stale_e2e" || runs[1].State != job2.RunFailed ||
		runs[1].Error != "previous process interrupted" {
		t.Fatalf("stale run = %+v, want converged to failed with reason", runs[1])
	}
	items, itemTotal, err := runner2.ListRunItems(ctx, first.RunID, 50, 0)
	if err != nil || itemTotal != 1 || len(items) != 1 {
		t.Fatalf("items after restart = (%d rows, total %d, %v), want 1", len(items), itemTotal, err)
	}
	if items[0].Target != "a.txt" || items[0].Action != syncjob.ItemCreate {
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
	if fileStatsOf(t, final).FilesUpdated != 1 {
		t.Errorf("post-restart stats = %+v, want updated=1", fileStatsOf(t, final))
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
	runs, total, err := runRepo.List(ctx, job2.RunFilter{JobID: job.ID})
	if err != nil || total != 3 {
		t.Fatalf("runs before prune = %d (%v), want 3", total, err)
	}
	oldest := runs[2] // List 为 started_at 倒序，末位最旧

	if err := runRepo.PruneRetention(ctx, 2); err != nil {
		t.Fatalf("prune retention: %v", err)
	}
	runs, total, err = runRepo.List(ctx, job2.RunFilter{JobID: job.ID})
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

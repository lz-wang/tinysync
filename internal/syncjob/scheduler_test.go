package syncjob

import (
	"context"
	"testing"
	"time"

	"tinysync/internal/source"
)

// schedulerBase 是调度测试的固定基准时间。
var schedulerBase = time.Unix(1757879400, 0).UTC()

// schedulerEnv 在 runnerEnv 之上构造被测调度器：固定时钟、手动 tick。
type schedulerEnv struct {
	*runnerEnv
	scheduler *Scheduler
	now       time.Time
}

func newSchedulerEnv(t *testing.T, remote source.Remote) *schedulerEnv {
	t.Helper()
	env := &schedulerEnv{
		runnerEnv: newRunnerEnv(t, remote),
		now:       schedulerBase,
	}
	env.scheduler = NewScheduler(env.repo, env.runner, env.history)
	env.scheduler.Now = func() time.Time { return env.now }
	// tick 可脱离 Start 直接测试：游标初始化与 Start 保持一致。
	env.scheduler.mu.Lock()
	env.scheduler.cursor = schedulerBase
	env.scheduler.mu.Unlock()
	return env
}

// tickTo 推进固定时钟并执行一次 tick。
func (e *schedulerEnv) tickTo(t *testing.T, at time.Time) {
	t.Helper()
	if at.Before(e.now) {
		t.Fatalf("clock moved backwards: %v -> %v", e.now, at)
	}
	e.now = at
	e.scheduler.tick(context.Background())
}

// restartAt 模拟进程重启：时钟与游标同时跳到 at（窗口之外不回看）。
func (e *schedulerEnv) restartAt(at time.Time) {
	e.now = at
	e.scheduler.mu.Lock()
	e.scheduler.cursor = at
	e.scheduler.mu.Unlock()
}

// mustScheduledJob 为测试 Job 设置 schedule。
func (e *schedulerEnv) mustScheduledJob(t *testing.T, name string, schedule Schedule) Job {
	t.Helper()
	job := e.mustJob(t, name)
	job.Schedule = schedule
	if err := e.repo.Update(context.Background(), job); err != nil {
		t.Fatalf("set schedule: %v", err)
	}
	return job
}

// runCount 统计 Job 的持久化 run 数。
func (e *schedulerEnv) runCount(t *testing.T, jobID string) int {
	t.Helper()
	runs, total, err := e.history.List(context.Background(), RunFilter{JobID: jobID})
	if err != nil {
		t.Fatalf("list runs: %v", err)
	}
	if len(runs) != total {
		t.Fatalf("runs = %d (total %d), inconsistent", len(runs), total)
	}
	return total
}

// interval 按持久化 anchor 相位调度：边界到达才触发，occurrence 为
// 边界本身；重启后相位不变、错过的周期不补跑。
func TestSchedulerIntervalPhase(t *testing.T) {
	release := make(chan struct{})
	env := newSchedulerEnv(t, &blockingRemote{release: release})
	anchor := schedulerBase
	job := env.mustScheduledJob(t, "interval", Schedule{
		Type:     ScheduleInterval,
		Value:    "30m",
		AnchorAt: &anchor,
	})
	ctx := context.Background()

	// 边界未到：不触发。
	env.tickTo(t, anchor.Add(time.Second))
	if got := env.runCount(t, job.ID); got != 0 {
		t.Fatalf("runs before boundary = %d, want 0", got)
	}

	// 边界后首个 tick：触发，occurrence 为边界本身。
	env.tickTo(t, anchor.Add(30*time.Minute+time.Second))
	runs, _, err := env.history.List(ctx, RunFilter{JobID: job.ID})
	if err != nil || len(runs) != 1 {
		t.Fatalf("runs after boundary = %d (%v), want 1", len(runs), err)
	}
	if runs[0].Trigger != TriggerInterval || runs[0].ScheduledFor == nil ||
		!runs[0].ScheduledFor.Equal(anchor.Add(30*time.Minute)) {
		t.Errorf("run = %+v, want interval occurrence %v", runs[0], anchor.Add(30*time.Minute))
	}
	// 窗口内重复 tick 不重复触发（run 仍在进行）。
	env.tickTo(t, anchor.Add(30*time.Minute+2*time.Second))
	if got := env.runCount(t, job.ID); got != 1 {
		t.Fatalf("runs after repeat tick = %d, want 1", got)
	}
	close(release)
	if err := env.runner.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}

	// 重启模拟：游标重置为当前时刻，错过的边界（1h）不补跑，相位保持。
	env.restartAt(anchor.Add(time.Hour))
	env.tickTo(t, anchor.Add(time.Hour+time.Second))
	if got := env.runCount(t, job.ID); got != 1 {
		t.Fatalf("runs after restart tick = %d, want 1 (no catch-up)", got)
	}
	// 下一个边界按 anchor 相位计算：1h30m。
	env.tickTo(t, anchor.Add(90*time.Minute+time.Second))
	runs, _, err = env.history.List(ctx, RunFilter{JobID: job.ID})
	if err != nil || len(runs) != 2 {
		t.Fatalf("runs at next boundary = %d (%v), want 2", len(runs), err)
	}
	if runs[0].ScheduledFor == nil || !runs[0].ScheduledFor.Equal(anchor.Add(90*time.Minute)) {
		t.Errorf("second occurrence = %v, want %v (anchor phase)", runs[0].ScheduledFor, anchor.Add(90*time.Minute))
	}
	if err := env.runner.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
}

// once 到期触发且只触发一次；离线期间错过的 once 恢复后补执行。
func TestSchedulerOnceFiresOnceAndCatchesUp(t *testing.T) {
	env := newSchedulerEnv(t, buildRemote(map[string]string{"/a.txt": "v1"}, nil))
	at := schedulerBase.Add(time.Hour)
	job := env.mustScheduledJob(t, "once", Schedule{
		Type:  ScheduleOnce,
		Value: at.Format(time.RFC3339),
	})
	ctx := context.Background()

	// 到期前不触发。
	env.tickTo(t, schedulerBase.Add(time.Second))
	if got := env.runCount(t, job.ID); got != 0 {
		t.Fatalf("runs before once = %d, want 0", got)
	}

	// 到期触发，occurrence 为配置时刻。
	env.tickTo(t, at.Add(time.Second))
	runs, _, err := env.history.List(ctx, RunFilter{JobID: job.ID})
	if err != nil || len(runs) != 1 {
		t.Fatalf("runs after once = %d (%v), want 1", len(runs), err)
	}
	if runs[0].Trigger != TriggerOnce || runs[0].ScheduledFor == nil || !runs[0].ScheduledFor.Equal(at) {
		t.Errorf("run = %+v, want once occurrence %v", runs[0], at)
	}

	// 重复 tick 已消费，不重复触发。
	env.tickTo(t, at.Add(2*time.Second))
	if got := env.runCount(t, job.ID); got != 1 {
		t.Fatalf("runs after repeat tick = %d, want 1 (consumed)", got)
	}

	// 离线期间错过的 once：重启后仍未消费则补执行一次。
	job2 := env.mustScheduledJob(t, "once-late", Schedule{
		Type:  ScheduleOnce,
		Value: at.Format(time.RFC3339),
	})
	env.restartAt(at.Add(2 * time.Hour))
	env.tickTo(t, at.Add(2*time.Hour+time.Second))
	lateRuns, _, err := env.history.List(ctx, RunFilter{JobID: job2.ID})
	if err != nil || len(lateRuns) != 1 {
		t.Fatalf("late once runs = %d (%v), want 1 (catch-up)", len(lateRuns), err)
	}
	env.tickTo(t, at.Add(2*time.Hour+2*time.Second))
	if got := env.runCount(t, job2.ID); got != 1 {
		t.Fatalf("late once after repeat tick = %d, want 1", got)
	}
	// 等待在途运行退出，避免与临时目录清理竞争。
	if err := env.runner.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
}

// cron 按 IANA timezone 触发：03:30 Asia/Singapore 即 19:30 UTC。
func TestSchedulerCronTimezone(t *testing.T) {
	env := newSchedulerEnv(t, buildRemote(nil, nil))
	job := env.mustScheduledJob(t, "cron", Schedule{
		Type:     ScheduleCron,
		Value:    "30 3 * * *",
		Timezone: "Asia/Singapore",
	})
	ctx := context.Background()

	day := time.Date(2026, 9, 19, 0, 0, 0, 0, time.UTC)
	env.restartAt(day)
	env.tickTo(t, day.Add(time.Second))
	if got := env.runCount(t, job.ID); got != 0 {
		t.Fatalf("runs before cron time = %d, want 0", got)
	}

	// 03:30 SGT = 19:30 UTC，边界后首个 tick 触发。
	env.tickTo(t, day.Add(19*time.Hour+30*time.Minute+time.Second))
	runs, _, err := env.history.List(ctx, RunFilter{JobID: job.ID})
	if err != nil || len(runs) != 1 {
		t.Fatalf("runs after cron time = %d (%v), want 1", len(runs), err)
	}
	if runs[0].Trigger != TriggerCron || runs[0].ScheduledFor == nil ||
		!runs[0].ScheduledFor.Equal(day.Add(19*time.Hour+30*time.Minute)) {
		t.Errorf("run = %+v, want cron occurrence %v", runs[0], day.Add(19*time.Hour+30*time.Minute))
	}
	if err := env.runner.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
}

// 调度触发的 overlap：运行中到期记 skipped（occurrence 已消费，不排队）。
func TestSchedulerOverlapRecordsSkipped(t *testing.T) {
	release := make(chan struct{})
	env := newSchedulerEnv(t, &blockingRemote{release: release})
	anchor := schedulerBase
	job := env.mustScheduledJob(t, "busy", Schedule{
		Type:     ScheduleInterval,
		Value:    "30m",
		AnchorAt: &anchor,
	})
	ctx := context.Background()

	// 手动运行占用 Job。
	if _, err := env.runner.Start(ctx, job.ID); err != nil {
		t.Fatalf("Start: %v", err)
	}
	env.tickTo(t, anchor.Add(30*time.Minute+time.Second))
	runs, _, err := env.history.List(ctx, RunFilter{JobID: job.ID})
	if err != nil || len(runs) != 2 {
		t.Fatalf("runs = %d (%v), want 2 (manual + skipped)", len(runs), err)
	}
	if runs[0].State != RunSkipped || runs[0].Error != "previous run still active" ||
		runs[0].ScheduledFor == nil || !runs[0].ScheduledFor.Equal(anchor.Add(30*time.Minute)) {
		t.Errorf("latest run = %+v, want skipped with overlap reason", runs[0])
	}
	close(release)
	if err := env.runner.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
}

// manual 与禁用 Job 不被调度。
func TestSchedulerIgnoresManualAndDisabled(t *testing.T) {
	env := newSchedulerEnv(t, buildRemote(nil, nil))
	manual := env.mustJob(t, "manual")
	disabled := env.mustScheduledJob(t, "disabled", Schedule{
		Type:  ScheduleInterval,
		Value: "30m",
	})
	disabled.Enabled = false
	if err := env.repo.Update(context.Background(), disabled); err != nil {
		t.Fatalf("disable job: %v", err)
	}

	env.tickTo(t, schedulerBase.Add(time.Hour))
	if got := env.runCount(t, manual.ID) + env.runCount(t, disabled.ID); got != 0 {
		t.Errorf("scheduled runs = %d, want 0", got)
	}
}

// Start / Stop 生命周期：Stop 后调度循环退出，不泄漏 goroutine。
func TestSchedulerStartStop(t *testing.T) {
	env := newSchedulerEnv(t, buildRemote(nil, nil))
	ctx, cancel := context.WithCancel(context.Background())
	env.scheduler.Start(ctx)
	env.scheduler.Stop()
	cancel()
	// 二次 Stop 安全。
	env.scheduler.Stop()
}

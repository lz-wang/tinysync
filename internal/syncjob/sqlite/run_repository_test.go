// RunRepository 的测试直接使用真实临时 SQLite 数据库，覆盖持久化
// 语义（occurrence 消费、stale 恢复、retention 级联）。
package sqlite

import (
	"context"
	"errors"
	"testing"
	"time"

	"tinysync/internal/syncjob"
)

// baseTime 是测试用固定基准时间（整毫秒，避免毫秒截断歧义）。
var baseTime = time.Unix(1757879400, 0).UTC()

// seedRunSource 插入 Source 与两个 Job，返回运行仓库。
func seedRunSource(t *testing.T) (*RunRepository, []string) {
	t.Helper()
	db, _, _ := openRepos(t)
	mustSeedSource(t, db, "src_a")
	ids := []string{"job_a", "job_b"}
	for i, id := range ids {
		if _, err := db.Exec(`INSERT INTO sync_jobs
			(id, name, source_id, remote_root, local_root, mode,
			 include_patterns, exclude_patterns, enabled, created_at, updated_at)
			VALUES (?, ?, 'src_a', '/', '/tmp/x', 'copy', '[]', '[]', 1, 1, 1)`,
			id, "job-"+string(rune('a'+i))); err != nil {
			t.Fatalf("seed job %s: %v", id, err)
		}
	}
	return NewRunRepository(db), ids
}

// runningRun 构造 running 状态的测试运行记录。
func runningRun(id, jobID string, trigger syncjob.RunTrigger, scheduledFor *time.Time, offset time.Duration) syncjob.RunRecord {
	return syncjob.RunRecord{
		ID:           id,
		JobID:        jobID,
		Trigger:      trigger,
		ScheduledFor: scheduledFor,
		State:        syncjob.RunRunning,
		StartedAt:    baseTime.Add(offset),
	}
}

// 手动运行 Insert/Get/Finalize 往返：scheduled_for / finished_at 可空、
// 统计与错误覆盖生效；未知 run 报 ErrRunUnknown。
func TestRunInsertGetFinalize(t *testing.T) {
	repo, jobs := seedRunSource(t)
	ctx := context.Background()

	run := runningRun("run_a", jobs[0], syncjob.TriggerManual, nil, 0)
	if err := repo.Insert(ctx, run); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	got, err := repo.Get(ctx, "run_a")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Trigger != syncjob.TriggerManual || got.ScheduledFor != nil ||
		got.State != syncjob.RunRunning || got.FinishedAt != nil {
		t.Errorf("running run = %+v, want manual running with nil times", got)
	}

	finished := baseTime.Add(time.Second)
	state := syncjob.RunSucceeded
	final := syncjob.RunRecord{
		ID:         "run_a",
		JobID:      jobs[0],
		Trigger:    syncjob.TriggerManual,
		State:      state,
		StartedAt:  run.StartedAt,
		FinishedAt: &finished,
		Stats: syncjob.RunStats{
			FilesTotal: 3, FilesCreated: 2, FilesSkipped: 1, BytesTransferred: 128,
		},
	}
	if err := repo.Finalize(ctx, final); err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	got, err = repo.Get(ctx, "run_a")
	if err != nil {
		t.Fatalf("Get after finalize: %v", err)
	}
	if got.State != syncjob.RunSucceeded || got.FinishedAt == nil ||
		!got.FinishedAt.Equal(finished) || got.Stats != final.Stats {
		t.Errorf("finalized run = %+v, want succeeded with stats %+v", got, final.Stats)
	}

	// Finalize 未知 run 报 ErrRunUnknown。
	missing := final
	missing.ID = "run_missing"
	if err := repo.Finalize(ctx, missing); !errors.Is(err, syncjob.ErrRunUnknown) {
		t.Errorf("Finalize missing = %v, want ErrRunUnknown", err)
	}
	if _, err := repo.Get(ctx, "run_missing"); !errors.Is(err, syncjob.ErrRunUnknown) {
		t.Errorf("Get missing = %v, want ErrRunUnknown", err)
	}
}

// 计划运行携带 occurrence：往返毫秒精度相等。
func TestRunScheduledForRoundTrip(t *testing.T) {
	repo, jobs := seedRunSource(t)
	ctx := context.Background()

	occ := baseTime.Add(30 * time.Minute)
	run := runningRun("run_cron", jobs[0], syncjob.TriggerCron, &occ, time.Minute)
	if err := repo.Insert(ctx, run); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	got, err := repo.Get(ctx, "run_cron")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.ScheduledFor == nil || !got.ScheduledFor.Equal(occ) {
		t.Errorf("scheduled_for = %v, want %v", got.ScheduledFor, occ)
	}
}

// 引用不存在的 Job 插入运行被外键拒绝。
func TestRunInsertMissingJobRejected(t *testing.T) {
	repo, _ := seedRunSource(t)
	err := repo.Insert(context.Background(), runningRun("run_x", "job_missing", syncjob.TriggerManual, nil, 0))
	if !errors.Is(err, syncjob.ErrNotFound) {
		t.Errorf("Insert with missing job = %v, want ErrNotFound", err)
	}
}

// Latest 返回 Job 最近一条（含 skipped）；无记录报 ErrRunUnknown。
func TestRunLatest(t *testing.T) {
	repo, jobs := seedRunSource(t)
	ctx := context.Background()

	if _, err := repo.Latest(ctx, jobs[0]); !errors.Is(err, syncjob.ErrRunUnknown) {
		t.Fatalf("Latest on empty = %v, want ErrRunUnknown", err)
	}

	if err := repo.Insert(ctx, runningRun("run_1", jobs[0], syncjob.TriggerManual, nil, 0)); err != nil {
		t.Fatalf("Insert 1: %v", err)
	}
	// 同毫秒并列时按插入序取后者。
	if err := repo.Insert(ctx, runningRun("run_2", jobs[0], syncjob.TriggerManual, nil, 0)); err != nil {
		t.Fatalf("Insert 2: %v", err)
	}
	olderJobB := runningRun("run_b", jobs[1], syncjob.TriggerManual, nil, -time.Hour)
	if err := repo.Insert(ctx, olderJobB); err != nil {
		t.Fatalf("Insert b: %v", err)
	}

	latest, err := repo.Latest(ctx, jobs[0])
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}
	if latest.ID != "run_2" {
		t.Errorf("Latest = %s, want run_2", latest.ID)
	}
}

// List 支持 job / status 过滤、started_at 倒序、分页与总数。
func TestRunListFilterPagination(t *testing.T) {
	repo, jobs := seedRunSource(t)
	ctx := context.Background()

	insert := func(id, jobID string, offset time.Duration, state syncjob.RunState) {
		t.Helper()
		run := runningRun(id, jobID, syncjob.TriggerManual, nil, offset)
		if err := repo.Insert(ctx, run); err != nil {
			t.Fatalf("Insert %s: %v", id, err)
		}
		if state == syncjob.RunRunning {
			return
		}
		fin := baseTime.Add(offset + time.Second)
		final := run
		final.State = state
		final.FinishedAt = &fin
		if err := repo.Finalize(ctx, final); err != nil {
			t.Fatalf("Finalize %s: %v", id, err)
		}
	}
	insert("run_1", jobs[0], time.Hour, syncjob.RunSucceeded)
	insert("run_2", jobs[0], 2*time.Hour, syncjob.RunFailed)
	insert("run_3", jobs[1], 3*time.Hour, syncjob.RunSucceeded)
	insert("run_4", jobs[0], 4*time.Hour, syncjob.RunRunning)

	// 全部倒序。
	runs, total, err := repo.List(ctx, syncjob.RunFilter{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if total != 4 {
		t.Fatalf("total = %d, want 4", total)
	}
	wantOrder := []string{"run_4", "run_3", "run_2", "run_1"}
	for i, id := range wantOrder {
		if runs[i].ID != id {
			t.Errorf("runs[%d] = %s, want %s", i, runs[i].ID, id)
		}
	}

	// job 过滤。
	runs, total, err = repo.List(ctx, syncjob.RunFilter{JobID: jobs[0]})
	if err != nil || total != 3 {
		t.Fatalf("List by job = (total %d, %v), want 3", total, err)
	}
	if runs[0].ID != "run_4" || runs[2].ID != "run_1" {
		t.Errorf("job-filtered order = [%s %s %s], want run_4..run_1", runs[0].ID, runs[1].ID, runs[2].ID)
	}

	// status 过滤。
	runs, total, err = repo.List(ctx, syncjob.RunFilter{Status: syncjob.RunSucceeded})
	if err != nil || total != 2 {
		t.Fatalf("List by status = (total %d, %v), want 2", total, err)
	}

	// 分页。
	runs, total, err = repo.List(ctx, syncjob.RunFilter{Limit: 2, Offset: 1})
	if err != nil || total != 4 {
		t.Fatalf("List page = (total %d, %v), want 4", total, err)
	}
	if len(runs) != 2 || runs[0].ID != "run_3" || runs[1].ID != "run_2" {
		t.Errorf("page = [%s %s], want [run_3 run_2]", runs[0].ID, runs[1].ID)
	}
}

// AppendItem / Items：按写入顺序返回，分页与总数正确。
func TestRunItems(t *testing.T) {
	repo, jobs := seedRunSource(t)
	ctx := context.Background()

	if err := repo.Insert(ctx, runningRun("run_a", jobs[0], syncjob.TriggerManual, nil, 0)); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	items := []syncjob.RunItem{
		{RunID: "run_a", Path: "a.jpg", Action: syncjob.ItemCreate, Status: syncjob.ItemSucceeded, Bytes: 10},
		{RunID: "run_a", Path: "b.jpg", Action: syncjob.ItemUpdate, Status: syncjob.ItemFailed, Error: "boom"},
		{RunID: "run_a", Path: "c.jpg", Action: syncjob.ItemCreate, Status: syncjob.ItemSkipped, Error: "unknown local file"},
	}
	for _, item := range items {
		if err := repo.AppendItem(ctx, item); err != nil {
			t.Fatalf("AppendItem %s: %v", item.Path, err)
		}
	}

	got, total, err := repo.Items(ctx, "run_a", 10, 0)
	if err != nil || total != 3 {
		t.Fatalf("Items = (total %d, %v), want 3", total, err)
	}
	for i, want := range items {
		if got[i].Path != want.Path || got[i].Action != want.Action ||
			got[i].Status != want.Status || got[i].Bytes != want.Bytes || got[i].Error != want.Error {
			t.Errorf("items[%d] = %+v, want %+v", i, got[i], want)
		}
		if got[i].ID == 0 {
			t.Errorf("items[%d] has zero autoincrement id", i)
		}
	}

	page, total, err := repo.Items(ctx, "run_a", 1, 1)
	if err != nil || total != 3 || len(page) != 1 || page[0].Path != "b.jpg" {
		t.Errorf("page = %+v (total %d, %v), want [b.jpg] total 3", page, total, err)
	}
}

// HasRunFor 实现 occurrence 消费判定：同 Job 同触发器同 occurrence
// 命中，其余不命中。
func TestRunHasRunFor(t *testing.T) {
	repo, jobs := seedRunSource(t)
	ctx := context.Background()

	occ := baseTime.Add(30 * time.Minute)
	if err := repo.Insert(ctx, runningRun("run_a", jobs[0], syncjob.TriggerInterval, &occ, 0)); err != nil {
		t.Fatalf("Insert: %v", err)
	}

	consumed, err := repo.HasRunFor(ctx, jobs[0], syncjob.TriggerInterval, occ)
	if err != nil || !consumed {
		t.Errorf("HasRunFor same occurrence = (%v, %v), want true", consumed, err)
	}
	for name, check := range map[string]struct {
		jobID   string
		trigger syncjob.RunTrigger
		at      time.Time
	}{
		"other job":     {jobs[1], syncjob.TriggerInterval, occ},
		"other trigger": {jobs[0], syncjob.TriggerCron, occ},
		"other time":    {jobs[0], syncjob.TriggerInterval, occ.Add(time.Millisecond)},
	} {
		consumed, err := repo.HasRunFor(ctx, check.jobID, check.trigger, check.at)
		if err != nil || consumed {
			t.Errorf("HasRunFor %s = (%v, %v), want false", name, consumed, err)
		}
	}
}

// FailStaleRunning 只收敛 running；已终结记录不受影响。
func TestRunFailStaleRunning(t *testing.T) {
	repo, jobs := seedRunSource(t)
	ctx := context.Background()

	if err := repo.Insert(ctx, runningRun("run_stale", jobs[0], syncjob.TriggerManual, nil, 0)); err != nil {
		t.Fatalf("Insert stale: %v", err)
	}
	done := runningRun("run_done", jobs[0], syncjob.TriggerManual, nil, time.Minute)
	if err := repo.Insert(ctx, done); err != nil {
		t.Fatalf("Insert done: %v", err)
	}
	fin := baseTime.Add(2 * time.Minute)
	if err := repo.Finalize(ctx, syncjob.RunRecord{
		ID: "run_done", State: syncjob.RunSucceeded, FinishedAt: &fin,
	}); err != nil {
		t.Fatalf("Finalize done: %v", err)
	}

	recovered, err := repo.FailStaleRunning(ctx, baseTime.Add(time.Hour), "previous process interrupted")
	if err != nil {
		t.Fatalf("FailStaleRunning: %v", err)
	}
	if recovered != 1 {
		t.Fatalf("recovered = %d, want 1", recovered)
	}

	stale, err := repo.Get(ctx, "run_stale")
	if err != nil {
		t.Fatalf("Get stale: %v", err)
	}
	if stale.State != syncjob.RunFailed || stale.Error != "previous process interrupted" ||
		stale.FinishedAt == nil || !stale.FinishedAt.Equal(baseTime.Add(time.Hour)) {
		t.Errorf("stale run = %+v, want failed with reason and finished_at", stale)
	}

	kept, err := repo.Get(ctx, "run_done")
	if err != nil || kept.State != syncjob.RunSucceeded {
		t.Errorf("done run after recovery = %+v (%v), want succeeded kept", kept, err)
	}

	// 二次恢复无遗留：0 行。
	recovered, err = repo.FailStaleRunning(ctx, baseTime.Add(time.Hour), "x")
	if err != nil || recovered != 0 {
		t.Errorf("second recovery = (%d, %v), want 0", recovered, err)
	}
}

// PruneRetention 每 Job 保留最近 N 条，更早的 run 连同 items 级联删除。
func TestRunPruneRetention(t *testing.T) {
	repo, jobs := seedRunSource(t)
	ctx := context.Background()

	for i := range 5 {
		run := runningRun("run_a"+string(rune('0'+i)), jobs[0], syncjob.TriggerManual, nil, time.Duration(i)*time.Minute)
		if err := repo.Insert(ctx, run); err != nil {
			t.Fatalf("Insert a%d: %v", i, err)
		}
		if err := repo.AppendItem(ctx, syncjob.RunItem{
			RunID: run.ID, Path: "x.jpg", Action: syncjob.ItemCreate,
			Status: syncjob.ItemSucceeded, Bytes: 1,
		}); err != nil {
			t.Fatalf("AppendItem a%d: %v", i, err)
		}
	}
	for i := range 3 {
		run := runningRun("run_b"+string(rune('0'+i)), jobs[1], syncjob.TriggerManual, nil, time.Duration(i)*time.Minute)
		if err := repo.Insert(ctx, run); err != nil {
			t.Fatalf("Insert b%d: %v", i, err)
		}
	}

	if err := repo.PruneRetention(ctx, 2); err != nil {
		t.Fatalf("PruneRetention: %v", err)
	}

	runs, total, err := repo.List(ctx, syncjob.RunFilter{})
	if err != nil || total != 4 {
		t.Fatalf("after prune total = %d (%v), want 4", total, err)
	}
	// 每 Job 保留 started_at 最新的 2 条。
	byJob := map[string]int{jobs[0]: 0, jobs[1]: 0}
	wantKeep := map[string]bool{
		"run_a3": true, "run_a4": true,
		"run_b1": true, "run_b2": true,
	}
	for _, run := range runs {
		byJob[run.JobID]++
		if !wantKeep[run.ID] {
			t.Errorf("unexpected kept run %s", run.ID)
		}
	}
	for jobID, n := range byJob {
		if n != 2 {
			t.Errorf("job %s kept %d runs, want 2", jobID, n)
		}
	}

	// 被裁剪 run 的 items 级联删除；保留 run 的 items 不受影响。
	_, itemCount, err := repo.Items(ctx, "run_a2", 10, 0)
	if err != nil || itemCount != 0 {
		t.Errorf("items of pruned run = (total %d, %v), want 0 (cascade)", itemCount, err)
	}
	keptItems, itemCount, err := repo.Items(ctx, "run_a4", 10, 0)
	if err != nil || itemCount != 1 || len(keptItems) != 1 {
		t.Errorf("items of kept run = (%d rows, total %d, %v), want 1", len(keptItems), itemCount, err)
	}
}

// PersistScheduledRun 在单事务内落库 run 并写入 once 消费状态：
// 「run 存在 ⇔ occurrence 已消费」；非 once 触发只落历史，不触碰
// 消费状态；目标 Job 不存在时整体失败（事务回滚，run 未落库）。
func TestPersistScheduledRunConsumesOnceAtomically(t *testing.T) {
	db, jobRepo, _ := openRepos(t)
	mustSeedSource(t, db, "src_a")
	runRepo := NewRunRepository(db)
	ctx := context.Background()

	mustInsertOnceJob := func(id, name string) {
		t.Helper()
		job := newJob(id, name, "src_a")
		job.Schedule = syncjob.Schedule{
			Type:  syncjob.ScheduleOnce,
			Value: baseTime.Add(time.Hour).Format(time.RFC3339),
		}
		if err := jobRepo.Create(ctx, job); err != nil {
			t.Fatalf("create job %s: %v", id, err)
		}
	}
	mustInsertOnceJob("job_a", "once")
	mustInsertOnceJob("job_b", "once-b")

	// once：run 落库 + 消费状态同事务写入。
	at := baseTime.Add(time.Minute)
	run := runningRun("run_once", "job_a", syncjob.TriggerOnce, &at, 0)
	if err := runRepo.PersistScheduledRun(ctx, run); err != nil {
		t.Fatalf("PersistScheduledRun once: %v", err)
	}
	if _, err := runRepo.Get(ctx, "run_once"); err != nil {
		t.Fatalf("get persisted run: %v", err)
	}
	got, err := jobRepo.Get(ctx, "job_a")
	if err != nil {
		t.Fatalf("get job: %v", err)
	}
	if got.OnceConsumedFor == nil || !got.OnceConsumedFor.Equal(at) {
		t.Errorf("consumed_for = %v, want %v", got.OnceConsumedFor, at)
	}

	// interval：只落历史，消费状态保持不变。
	iv := runningRun("run_iv", "job_a", syncjob.TriggerInterval, &at, 0)
	if err := runRepo.PersistScheduledRun(ctx, iv); err != nil {
		t.Fatalf("PersistScheduledRun interval: %v", err)
	}
	got, err = jobRepo.Get(ctx, "job_a")
	if err != nil {
		t.Fatalf("get job after interval run: %v", err)
	}
	if got.OnceConsumedFor == nil || !got.OnceConsumedFor.Equal(at) {
		t.Errorf("consumed_for after interval run = %v, want unchanged %v", got.OnceConsumedFor, at)
	}

	// 不存在的 Job：整体失败（事务回滚），run 未落库。
	missing := runningRun("run_missing", "job_missing", syncjob.TriggerOnce, &at, 0)
	if err := runRepo.PersistScheduledRun(ctx, missing); !errors.Is(err, syncjob.ErrNotFound) {
		t.Fatalf("PersistScheduledRun missing job = %v, want ErrNotFound", err)
	}
	if _, err := runRepo.Get(ctx, "run_missing"); err == nil {
		t.Error("run row of missing job survived, want rollback")
	}
}

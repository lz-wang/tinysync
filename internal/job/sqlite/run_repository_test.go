// RunRepository 的测试直接使用真实临时 SQLite 数据库，覆盖持久化
// 语义（occurrence 消费、stale 恢复、retention 级联）。
package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"tinysync/internal/job"
	"tinysync/internal/storage"
)

// baseTime 是测试用固定基准时间（整毫秒，避免毫秒截断歧义）。
var baseTime = time.Unix(1757879400, 0).UTC()

// openTestDB 打开并初始化一个临时数据库，登记到 testDB 供断言查询。
func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := storage.Open(t.TempDir())
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := storage.Ensure(context.Background(), db); err != nil {
		t.Fatalf("storage.Ensure: %v", err)
	}
	return db
}

// seedJobs 初始化数据库并插入两个 file 任务，返回任务仓库、连接与任务 ID。
func seedJobs(t *testing.T) (*Repository, *sql.DB, []string) {
	t.Helper()
	db := openTestDB(t)
	ids := []string{"job_a", "job_b"}
	for i, id := range ids {
		if _, err := db.Exec(`INSERT INTO jobs
			(id, name, kind, enabled, config_json, created_at, updated_at)
			VALUES (?, ?, 'file', 1, '{}', 1, 1)`,
			id, "job-"+string(rune('a'+i))); err != nil {
			t.Fatalf("seed job %s: %v", id, err)
		}
	}
	return NewRepository(db), db, ids
}

// seedRunRepo 在 seedJobs 之上返回运行仓库。
func seedRunRepo(t *testing.T) (*RunRepository, []string) {
	t.Helper()
	_, db, ids := seedJobs(t)
	return NewRunRepository(db), ids
}

// nextRunAtOf 读取 jobs.next_run_at 原始列值（NULL → nil）。
func nextRunAtOf(t *testing.T, db *sql.DB, jobID string) *int64 {
	t.Helper()
	var null sql.NullInt64
	if err := db.QueryRow("SELECT next_run_at FROM jobs WHERE id = ?", jobID).Scan(&null); err != nil {
		t.Fatalf("query next_run_at of %s: %v", jobID, err)
	}
	if !null.Valid {
		return nil
	}
	return &null.Int64
}

// runningRun 构造 running 状态的测试运行记录。
func runningRun(id, jobID string, trigger job.RunTrigger, scheduledFor *time.Time, offset time.Duration) job.RunRecord {
	return job.RunRecord{
		ID:           id,
		JobKind:      job.KindFile,
		JobID:        jobID,
		Trigger:      trigger,
		ScheduledFor: scheduledFor,
		State:        job.RunRunning,
		StartedAt:    baseTime.Add(offset),
	}
}

// 手动运行 Insert/Get/Finalize 往返：scheduled_for / finished_at 可空、
// 结果摘要与错误覆盖生效；未知 run 报 ErrRunUnknown。
func TestRunInsertGetFinalize(t *testing.T) {
	repo, jobs := seedRunRepo(t)
	ctx := context.Background()

	run := runningRun("run_a", jobs[0], job.TriggerManual, nil, 0)
	if err := repo.Insert(ctx, run); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	got, err := repo.Get(ctx, "run_a")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Trigger != job.TriggerManual || got.ScheduledFor != nil ||
		got.State != job.RunRunning || got.FinishedAt != nil || got.JobKind != job.KindFile {
		t.Errorf("running run = %+v, want manual running with nil times", got)
	}

	finished := baseTime.Add(time.Second)
	finalResult := []byte(`{"files_total":3,"files_created":2,"files_skipped":1,"bytes_transferred":128}`)
	final := job.RunRecord{
		ID:         "run_a",
		JobKind:    job.KindFile,
		JobID:      jobs[0],
		Trigger:    job.TriggerManual,
		State:      job.RunSucceeded,
		StartedAt:  run.StartedAt,
		FinishedAt: &finished,
		Result:     finalResult,
	}
	if err := repo.Finalize(ctx, final); err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	got, err = repo.Get(ctx, "run_a")
	if err != nil {
		t.Fatalf("Get after finalize: %v", err)
	}
	if got.State != job.RunSucceeded || got.FinishedAt == nil ||
		!got.FinishedAt.Equal(finished) || string(got.Result) != string(final.Result) {
		t.Errorf("finalized run = %+v, want succeeded with result %s", got, final.Result)
	}

	// Finalize 不存在的 run：ErrRunUnknown，不产生新行。
	missing := final
	missing.ID = "run_missing"
	if err := repo.Finalize(ctx, missing); !errors.Is(err, job.ErrRunUnknown) {
		t.Fatalf("Finalize missing run = %v, want ErrRunUnknown", err)
	}
	if _, err := repo.Get(ctx, "run_missing"); !errors.Is(err, job.ErrRunUnknown) {
		t.Fatalf("Get missing run = %v, want ErrRunUnknown", err)
	}
}

// scheduled_for 毫秒精度往返不漂移。
func TestRunScheduledForRoundTrip(t *testing.T) {
	repo, jobs := seedRunRepo(t)
	ctx := context.Background()

	occ := baseTime.Truncate(time.Millisecond).Add(90 * time.Minute)
	run := runningRun("run_cron", jobs[0], job.TriggerCron, &occ, time.Minute)
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

// 外键：run 引用不存在的任务被拒绝并映射为 ErrNotFound。
func TestRunInsertMissingJobRejected(t *testing.T) {
	repo, _ := seedRunRepo(t)
	err := repo.Insert(context.Background(), runningRun("run_x", "job_missing", job.TriggerManual, nil, 0))
	if !errors.Is(err, job.ErrNotFound) {
		t.Fatalf("Insert missing job = %v, want ErrNotFound", err)
	}
}

// Latest 返回 started_at 倒序首条；无记录返回 ErrRunUnknown。
func TestRunLatest(t *testing.T) {
	repo, jobs := seedRunRepo(t)
	ctx := context.Background()

	if _, err := repo.Latest(ctx, jobs[0]); !errors.Is(err, job.ErrRunUnknown) {
		t.Fatalf("Latest empty = %v, want ErrRunUnknown", err)
	}
	if err := repo.Insert(ctx, runningRun("run_1", jobs[0], job.TriggerManual, nil, 0)); err != nil {
		t.Fatalf("Insert 1: %v", err)
	}
	if err := repo.Insert(ctx, runningRun("run_2", jobs[0], job.TriggerManual, nil, time.Hour)); err != nil {
		t.Fatalf("Insert 2: %v", err)
	}
	// 另一任务的 run 不影响 Latest。
	olderJobB := runningRun("run_b", jobs[1], job.TriggerManual, nil, -time.Hour)
	if err := repo.Insert(ctx, olderJobB); err != nil {
		t.Fatalf("Insert jobB: %v", err)
	}
	got, err := repo.Latest(ctx, jobs[0])
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}
	if got.ID != "run_2" {
		t.Errorf("Latest = %s, want run_2", got.ID)
	}
}

// List：job / kind / status 过滤与 limit / offset 分页，total 为过滤后总数。
func TestRunListFilterPagination(t *testing.T) {
	repo, jobs := seedRunRepo(t)
	ctx := context.Background()

	insert := func(id, jobID string, offset time.Duration, state job.RunState) {
		t.Helper()
		run := runningRun(id, jobID, job.TriggerManual, nil, offset)
		run.State = state
		if state != job.RunRunning {
			fin := run.StartedAt.Add(time.Second)
			run.FinishedAt = &fin
		}
		if err := repo.Insert(ctx, run); err != nil {
			t.Fatalf("Insert %s: %v", id, err)
		}
	}
	insert("run_1", jobs[0], time.Hour, job.RunSucceeded)
	insert("run_2", jobs[0], 2*time.Hour, job.RunFailed)
	insert("run_3", jobs[1], 3*time.Hour, job.RunSucceeded)
	insert("run_4", jobs[0], 4*time.Hour, job.RunRunning)

	runs, total, err := repo.List(ctx, job.RunFilter{})
	if err != nil || total != 4 || len(runs) != 4 {
		t.Fatalf("List all = (%d rows, total %d, %v), want 4/4", len(runs), total, err)
	}
	// started_at 倒序。
	wantOrder := []string{"run_4", "run_3", "run_2", "run_1"}
	for i, want := range wantOrder {
		if runs[i].ID != want {
			t.Fatalf("order[%d] = %s, want %s", i, runs[i].ID, want)
		}
	}

	runs, total, err = repo.List(ctx, job.RunFilter{JobID: jobs[0]})
	if err != nil || total != 3 || len(runs) != 3 {
		t.Fatalf("List by job = (%d rows, total %d, %v), want 3/3", len(runs), total, err)
	}

	runs, total, err = repo.List(ctx, job.RunFilter{Status: job.RunSucceeded})
	if err != nil || total != 2 || len(runs) != 2 {
		t.Fatalf("List by status = (%d rows, total %d, %v), want 2/2", len(runs), total, err)
	}

	runs, total, err = repo.List(ctx, job.RunFilter{Kind: job.KindFile})
	if err != nil || total != 4 {
		t.Fatalf("List by kind = (%d rows, total %d, %v), want total 4", len(runs), total, err)
	}

	// 分页：limit=2 offset=1 → run_3 / run_2，total 仍是过滤后总数。
	runs, total, err = repo.List(ctx, job.RunFilter{Limit: 2, Offset: 1})
	if err != nil || total != 4 || len(runs) != 2 {
		t.Fatalf("List page = (%d rows, total %d, %v), want 2/4", len(runs), total, err)
	}
	if runs[0].ID != "run_3" || runs[1].ID != "run_2" {
		t.Errorf("page = [%s, %s], want [run_3, run_2]", runs[0].ID, runs[1].ID)
	}
}

// AppendItem / Items：target 写入 detail_json 并按写入顺序分页返回。
func TestRunItems(t *testing.T) {
	repo, jobs := seedRunRepo(t)
	ctx := context.Background()
	if err := repo.Insert(ctx, runningRun("run_a", jobs[0], job.TriggerManual, nil, 0)); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	items := []job.RunItem{
		{RunID: "run_a", Target: "a.jpg", Action: job.ItemCreate, Status: job.ItemSucceeded, Bytes: 10},
		{RunID: "run_a", Target: "b.jpg", Action: job.ItemUpdate, Status: job.ItemFailed, Error: "boom"},
		{RunID: "run_a", Target: "c.jpg", Action: job.ItemCreate, Status: job.ItemSkipped, Error: "conflict"},
	}
	for _, item := range items {
		if err := repo.AppendItem(ctx, item); err != nil {
			t.Fatalf("AppendItem %s: %v", item.Target, err)
		}
	}
	got, total, err := repo.Items(ctx, "run_a", 10, 0)
	if err != nil || total != 3 || len(got) != 3 {
		t.Fatalf("Items = (%d rows, total %d, %v), want 3/3", len(got), total, err)
	}
	for i, want := range items {
		if got[i].Target != want.Target || got[i].Action != want.Action ||
			got[i].Status != want.Status || got[i].Bytes != want.Bytes || got[i].Error != want.Error {
			t.Errorf("item[%d] = %+v, want %+v", i, got[i], want)
		}
	}
	// 分页。
	page, total, err := repo.Items(ctx, "run_a", 1, 1)
	if err != nil || total != 3 || len(page) != 1 || page[0].Target != "b.jpg" {
		t.Fatalf("Items page = (%d rows, total %d, %v), want b.jpg", len(page), total, err)
	}
	// 未知 run：Items 直接空，语义区分由调用方（Get 先行）负责。
	if _, total, err := repo.Items(ctx, "run_missing", 10, 0); err != nil || total != 0 {
		t.Fatalf("Items missing run = (total %d, %v), want 0", total, err)
	}
}

// FailedItems：只返回 status=failed 的明细（写入序、limit 截断），
// total 为失败明细总数（不受 limit 截断），领域扩展字段保留在 Detail。
func TestRunFailedItems(t *testing.T) {
	repo, jobs := seedRunRepo(t)
	ctx := context.Background()
	if err := repo.Insert(ctx, runningRun("run_a", jobs[0], job.TriggerManual, nil, 0)); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	items := []job.RunItem{
		{RunID: "run_a", Target: "a.jpg", Action: job.ItemCreate, Status: job.ItemSucceeded, Bytes: 10},
		{RunID: "run_a", Target: "b.jpg", Action: job.ItemUpdate, Status: job.ItemFailed, Error: "boom",
			Detail: json.RawMessage(`{"src":"docker.io/a:1","platform_results":[{"platform":"linux/amd64","status":"failed","error":"boom"}]}`)},
		{RunID: "run_a", Target: "c.jpg", Action: job.ItemCreate, Status: job.ItemSkipped},
		{RunID: "run_a", Target: "d.jpg", Action: job.ItemUpdate, Status: job.ItemFailed, Error: "late"},
	}
	for _, item := range items {
		if err := repo.AppendItem(ctx, item); err != nil {
			t.Fatalf("AppendItem %s: %v", item.Target, err)
		}
	}
	got, total, err := repo.FailedItems(ctx, "run_a", job.FailedItemLimit)
	if err != nil || total != 2 || len(got) != 2 {
		t.Fatalf("FailedItems = (%d rows, total %d, %v), want 2/2", len(got), total, err)
	}
	// 只含失败行，写入序；领域扩展字段原样保留。
	if got[0].Target != "b.jpg" || got[0].Error != "boom" {
		t.Errorf("failed item[0] = (%s, %q), want (b.jpg, boom)", got[0].Target, got[0].Error)
	}
	if got[1].Target != "d.jpg" {
		t.Errorf("failed item[1] target = %s, want d.jpg (write order)", got[1].Target)
	}
	var detail struct {
		Src             string `json:"src"`
		PlatformResults []struct {
			Platform string `json:"platform"`
			Status   string `json:"status"`
		} `json:"platform_results"`
	}
	if err := json.Unmarshal(got[0].Detail, &detail); err != nil {
		t.Fatalf("unmarshal detail: %v", err)
	}
	if detail.Src != "docker.io/a:1" || len(detail.PlatformResults) != 1 ||
		detail.PlatformResults[0].Platform != "linux/amd64" {
		t.Errorf("detail = %+v, want src + one failed platform preserved", detail)
	}
	// 公共 target 键已拆出，不再混入 Detail。
	if strings.Contains(string(got[0].Detail), `"target"`) {
		t.Errorf("detail still contains target key: %s", got[0].Detail)
	}

	// limit 截断：取最早的 N 条，total 仍为失败明细总数。
	page, total, err := repo.FailedItems(ctx, "run_a", 1)
	if err != nil || total != 2 || len(page) != 1 || page[0].Target != "b.jpg" {
		t.Fatalf("FailedItems page = (%d rows, total %d, %v), want earliest b.jpg", len(page), total, err)
	}
	// 无失败明细的 run：空列表 + total 0。
	if err := repo.Insert(ctx, runningRun("run_b", jobs[1], job.TriggerManual, nil, 0)); err != nil {
		t.Fatalf("Insert run_b: %v", err)
	}
	if got, total, err := repo.FailedItems(ctx, "run_b", job.FailedItemLimit); err != nil || total != 0 || len(got) != 0 {
		t.Fatalf("FailedItems empty run = (%d rows, total %d, %v), want 0/0", len(got), total, err)
	}
}

// HasRunFor：occurrence 消费判定按 (job, trigger, occurrence) 精确匹配。
func TestRunHasRunFor(t *testing.T) {
	repo, jobs := seedRunRepo(t)
	ctx := context.Background()

	occ := baseTime.Add(time.Hour)
	if err := repo.Insert(ctx, runningRun("run_a", jobs[0], job.TriggerInterval, &occ, 0)); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	consumed, err := repo.HasRunFor(ctx, jobs[0], job.TriggerInterval, occ)
	if err != nil || !consumed {
		t.Fatalf("HasRunFor exact = (%v, %v), want true", consumed, err)
	}
	// 其他任务 / 触发器 / 时刻一律未消费。
	if consumed, _ := repo.HasRunFor(ctx, jobs[1], job.TriggerInterval, occ); consumed {
		t.Error("HasRunFor other job = true, want false")
	}
	if consumed, _ := repo.HasRunFor(ctx, jobs[0], job.TriggerCron, occ); consumed {
		t.Error("HasRunFor other trigger = true, want false")
	}
	if consumed, _ := repo.HasRunFor(ctx, jobs[0], job.TriggerInterval, occ.Add(time.Millisecond)); consumed {
		t.Error("HasRunFor other time = true, want false")
	}
}

// FailStaleRunning：启动恢复把遗留 running 收敛为 failed，终态 run 不受影响。
func TestRunFailStaleRunning(t *testing.T) {
	repo, jobs := seedRunRepo(t)
	ctx := context.Background()

	if err := repo.Insert(ctx, runningRun("run_stale", jobs[0], job.TriggerManual, nil, 0)); err != nil {
		t.Fatalf("Insert stale: %v", err)
	}
	done := runningRun("run_done", jobs[0], job.TriggerManual, nil, time.Minute)
	fin := done.StartedAt.Add(time.Second)
	done.FinishedAt = &fin
	done.State = job.RunSucceeded
	if err := repo.Insert(ctx, done); err != nil {
		t.Fatalf("Insert done: %v", err)
	}
	if err := repo.Finalize(ctx, done); err != nil {
		t.Fatalf("Finalize done: %v", err)
	}

	reason := "previous process interrupted"
	at := baseTime.Add(2 * time.Hour)
	n, err := repo.FailStaleRunning(ctx, at, reason)
	if err != nil || n != 1 {
		t.Fatalf("FailStaleRunning = (%d, %v), want 1", n, err)
	}
	stale, err := repo.Get(ctx, "run_stale")
	if err != nil {
		t.Fatalf("Get stale: %v", err)
	}
	if stale.State != job.RunFailed || stale.Error != reason || stale.FinishedAt == nil {
		t.Errorf("stale after recovery = %+v, want failed with reason & finished_at", stale)
	}
	kept, err := repo.Get(ctx, "run_done")
	if err != nil || kept.State != job.RunSucceeded {
		t.Errorf("terminal run after recovery = (%+v, %v), want succeeded untouched", kept, err)
	}
}

// PruneRetention：每任务保留最近 N 条（含 running），更早的连同 items
// 级联删除；不同任务互不影响。
func TestRunPruneRetention(t *testing.T) {
	repo, jobs := seedRunRepo(t)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		run := runningRun("run_a"+string(rune('0'+i)), jobs[0], job.TriggerManual, nil, time.Duration(i)*time.Minute)
		if err := repo.Insert(ctx, run); err != nil {
			t.Fatalf("Insert %s: %v", run.ID, err)
		}
		if err := repo.AppendItem(ctx, job.RunItem{
			RunID: run.ID, Target: "x.jpg", Action: job.ItemCreate,
			Status: job.ItemSucceeded, Bytes: 1,
		}); err != nil {
			t.Fatalf("AppendItem %s: %v", run.ID, err)
		}
	}
	for i := 0; i < 3; i++ {
		run := runningRun("run_b"+string(rune('0'+i)), jobs[1], job.TriggerManual, nil, time.Duration(i)*time.Minute)
		if err := repo.Insert(ctx, run); err != nil {
			t.Fatalf("Insert %s: %v", run.ID, err)
		}
	}

	if err := repo.PruneRetention(ctx, 2); err != nil {
		t.Fatalf("PruneRetention: %v", err)
	}
	runs, total, err := repo.List(ctx, job.RunFilter{})
	if err != nil || total != 4 {
		t.Fatalf("List after prune = (total %d, %v), want 4", total, err)
	}
	byJob := map[string]int{}
	for _, run := range runs {
		byJob[run.JobID]++
	}
	for jobID, n := range byJob {
		if n != 2 {
			t.Errorf("job %s kept %d runs, want 2", jobID, n)
		}
	}

	// 被裁剪 run 的 items 级联删除；保留 run 的 items 不受影响。
	_, itemCount, err := repo.Items(ctx, "run_a0", 10, 0)
	if err != nil || itemCount != 0 {
		t.Errorf("items of pruned run = (total %d, %v), want 0 (cascade)", itemCount, err)
	}
	keptItems, itemCount, err := repo.Items(ctx, "run_a2", 10, 0)
	if err != nil || itemCount != 1 || len(keptItems) != 1 {
		t.Errorf("items of kept run = (%d rows, total %d, %v), want 1", len(keptItems), itemCount, err)
	}
}

// PersistScheduledRun 在单事务内落库 run 并写入 once 消费状态：
// 「run 存在 ⇔ occurrence 已消费」；非 once 触发只落历史，不触碰
// 消费状态；目标任务不存在时整体失败（事务回滚，run 未落库）。
func TestPersistScheduledRunConsumesOnceAtomically(t *testing.T) {
	jobRepo, db, _ := seedJobs(t)
	runRepo := NewRunRepository(db)
	ctx := context.Background()

	mustInsertOnceJob := func(id, name string) {
		t.Helper()
		at := baseTime.Add(time.Hour)
		if err := jobRepo.Create(ctx, job.Job{
			ID:        id,
			Name:      name,
			Kind:      job.KindFile,
			Enabled:   true,
			Schedule:  job.Schedule{Type: job.ScheduleOnce, Value: at.Format(time.RFC3339)},
			Config:    []byte(`{}`),
			CreatedAt: baseTime,
			UpdatedAt: baseTime,
		}); err != nil {
			t.Fatalf("create job %s: %v", id, err)
		}
	}
	mustInsertOnceJob("job_once_a", "once")
	mustInsertOnceJob("job_once_b", "once-b")

	// once：run 落库 + 消费状态同事务写入，next_run_at 随消费清空。
	at := baseTime.Add(time.Minute)
	run := runningRun("run_once", "job_once_a", job.TriggerOnce, &at, 0)
	if err := runRepo.PersistScheduledRun(ctx, run); err != nil {
		t.Fatalf("PersistScheduledRun once: %v", err)
	}
	if _, err := runRepo.Get(ctx, "run_once"); err != nil {
		t.Fatalf("get persisted run: %v", err)
	}
	got, err := jobRepo.Get(ctx, "job_once_a")
	if err != nil {
		t.Fatalf("get job: %v", err)
	}
	if got.OnceConsumedAt == nil || !got.OnceConsumedAt.Equal(at) {
		t.Errorf("once_consumed_at = %v, want %v", got.OnceConsumedAt, at)
	}
	if nr := nextRunAtOf(t, db, "job_once_a"); nr != nil {
		t.Errorf("next_run_at after once consumption = %d, want NULL", *nr)
	}

	// interval：只落历史，消费状态保持不变。
	iv := runningRun("run_iv", "job_once_a", job.TriggerInterval, &at, 0)
	if err := runRepo.PersistScheduledRun(ctx, iv); err != nil {
		t.Fatalf("PersistScheduledRun interval: %v", err)
	}
	got, err = jobRepo.Get(ctx, "job_once_a")
	if err != nil {
		t.Fatalf("get job after interval run: %v", err)
	}
	if got.OnceConsumedAt == nil || !got.OnceConsumedAt.Equal(at) {
		t.Errorf("once_consumed_at after interval run = %v, want unchanged %v", got.OnceConsumedAt, at)
	}

	// 不存在的任务：整体失败（事务回滚），run 未落库。
	missing := runningRun("run_missing", "job_missing", job.TriggerOnce, &at, 0)
	if err := runRepo.PersistScheduledRun(ctx, missing); !errors.Is(err, job.ErrNotFound) {
		t.Fatalf("PersistScheduledRun missing job = %v, want ErrNotFound", err)
	}
	if _, err := runRepo.Get(ctx, "run_missing"); err == nil {
		t.Error("run row of missing job survived, want rollback")
	}
}

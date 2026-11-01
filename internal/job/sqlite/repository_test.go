// Repository 的测试直接使用真实临时 SQLite 数据库，不 mock SQL。
package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"testing"
	"time"

	"tinysync/internal/job"
)

// newJob 构造测试用统一任务，时间戳固定以便断言；schedule 显式 manual，
// 与落库后的取值一致（省略 Schedule 的零值任务由仓库归一为 manual）。
func newJob(id, name string) job.Job {
	now := time.Unix(1757879400, 0).UTC()
	return job.Job{
		ID:        id,
		Name:      name,
		Kind:      job.KindFile,
		Enabled:   true,
		Schedule:  job.Schedule{Type: job.ScheduleManual},
		Config:    []byte(`{"source_id":"src_a","remote_root":"/photos","local_root":"/tmp/backup","mode":"mirror"}`),
		CreatedAt: now,
		UpdatedAt: now,
	}
}

// 创建后 Get 往返全部字段（config 原样、schedule 与时间）。
func TestJobCreateAndGetRoundTrip(t *testing.T) {
	repo, _, _ := seedJobs(t)

	j := newJob("job_rt", "photos")
	if err := repo.Create(context.Background(), j); err != nil {
		t.Fatalf("Create: %v", err)
	}
	got, err := repo.Get(context.Background(), "job_rt")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !reflect.DeepEqual(got, j) {
		t.Errorf("Get = %+v, want %+v", got, j)
	}
}

// List 按 name 大小写不敏感排序；ListByKind 只返回指定类型。
func TestJobListSortedNocaseAndByKind(t *testing.T) {
	repo, _, _ := seedJobs(t)
	oci := newJob("job_oci", "Zeta")
	oci.Kind = job.KindOCI
	for _, j := range []job.Job{
		oci,
		newJob("job_alpha", "alpha"),
		newJob("job_mid", "Midway"),
	} {
		if err := repo.Create(context.Background(), j); err != nil {
			t.Fatalf("Create %s: %v", j.ID, err)
		}
	}
	jobs, err := repo.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	var names []string
	for _, j := range jobs {
		names = append(names, j.Name)
	}
	// seedJobs 预置的 job-a / job-b 也在列表中，按 NOCASE 全局排序。
	want := []string{"alpha", "job-a", "job-b", "Midway", "Zeta"}
	if len(names) != len(want) {
		t.Fatalf("names = %v, want %v", names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Errorf("names[%d] = %q, want %q", i, names[i], want[i])
		}
	}

	onlyOCI, err := repo.ListByKind(context.Background(), job.KindOCI)
	if err != nil || len(onlyOCI) != 1 || onlyOCI[0].Kind != job.KindOCI {
		t.Fatalf("ListByKind = (%d rows, %v), want 1 oci", len(onlyOCI), err)
	}
}

// name 大小写不敏感唯一：仅大小写不同也算冲突。
func TestJobCreateNameConflictNocase(t *testing.T) {
	repo, _, _ := seedJobs(t)
	if err := repo.Create(context.Background(), newJob("job_x", "photos")); err != nil {
		t.Fatalf("Create: %v", err)
	}
	err := repo.Create(context.Background(), newJob("job_y", "PHOTOS"))
	if !errors.Is(err, job.ErrConflict) {
		t.Fatalf("Create duplicate name = %v, want ErrConflict", err)
	}
}

// Update 整体替换可变字段；目标不存在报 ErrNotFound；改到冲突 name 报
// ErrConflict；领域钩子与更新同事务（成功执行 / 失败回滚）。
func TestJobUpdateWithHooksAtomic(t *testing.T) {
	repo, _, _ := seedJobs(t)
	ctx := context.Background()
	if err := repo.Create(ctx, newJob("job_x", "photos")); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := repo.Create(ctx, newJob("job_y", "docs")); err != nil {
		t.Fatalf("Create b: %v", err)
	}

	// 成功路径：钩子随更新执行。
	ran := false
	updated := newJob("job_x", "vacation")
	updated.Config = []byte(`{"source_id":"src_a","mode":"copy"}`)
	if err := repo.Update(ctx, updated, func(ctx context.Context, tx *sql.Tx) error {
		ran = true
		return nil
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	got, err := repo.Get(ctx, "job_x")
	if err != nil || got.Name != "vacation" || string(got.Config) != string(updated.Config) {
		t.Fatalf("Get after update = %+v (%v), want vacation with new config", got, err)
	}
	if !ran {
		t.Error("update hook was not executed")
	}

	// 失败路径：钩子返回错误 → 整体回滚，更新不生效。
	updated2 := newJob("job_x", "rolled-back")
	hookErr := errors.New("hook failed")
	if err := repo.Update(ctx, updated2, func(ctx context.Context, tx *sql.Tx) error {
		return hookErr
	}); !errors.Is(err, hookErr) {
		t.Fatalf("Update with failing hook = %v, want hook error", err)
	}
	kept, err := repo.Get(ctx, "job_x")
	if err != nil || kept.Name != "vacation" {
		t.Fatalf("Get after rollback = %+v (%v), want vacation intact", kept, err)
	}

	if err := repo.Update(ctx, newJob("job_z", "x")); !errors.Is(err, job.ErrNotFound) {
		t.Errorf("Update missing = %v, want ErrNotFound", err)
	}
	if err := repo.Update(ctx, newJob("job_x", "docs")); !errors.Is(err, job.ErrConflict) {
		t.Errorf("Update to conflicting name = %v, want ErrConflict", err)
	}
}

// schedule 字段往返：四种类型（含 anchor 有无）Create/Get/Update 深度相等；
// 省略 Schedule 的零值任务按 manual 落库（CHECK 约束不接受空类型）；
// next_run_at 按 schedule 推导落库，manual / 已消费 once 为 NULL。
func TestJobScheduleRoundTripAndNextRunAt(t *testing.T) {
	repo, db, _ := seedJobs(t)
	ctx := context.Background()
	repo.Now = func() time.Time { return time.Unix(1757879400, 0).UTC() }

	anchor := time.Unix(1757879400, 0).UTC()
	onceAt := anchor.Add(24 * time.Hour)
	schedules := []job.Schedule{
		{Type: job.ScheduleManual},
		{Type: job.ScheduleOnce, Value: onceAt.Format(time.RFC3339)},
		{Type: job.ScheduleInterval, Value: "30m", AnchorAt: &anchor},
		{Type: job.ScheduleCron, Value: "0 3 * * *", Timezone: "Asia/Singapore"},
	}
	for i, schedule := range schedules {
		id := "job_s" + string(rune('0'+i))
		j := newJob(id, "sched-"+string(rune('0'+i)))
		j.Schedule = schedule
		if err := repo.Create(ctx, j); err != nil {
			t.Fatalf("Create %s: %v", id, err)
		}
		got, err := repo.Get(ctx, id)
		if err != nil {
			t.Fatalf("Get %s: %v", id, err)
		}
		if !reflect.DeepEqual(got, j) {
			t.Errorf("Get %s = %+v, want %+v", id, got, j)
		}
		if schedule.AnchorAt != nil {
			if got.Schedule.AnchorAt == nil || !got.Schedule.AnchorAt.Equal(*schedule.AnchorAt) {
				t.Errorf("anchor roundtrip = %v, want %v", got.Schedule.AnchorAt, *schedule.AnchorAt)
			}
		}
		// next_run_at：manual 为 NULL；其余为按 schedule 推导的下一触发。
		next := nextRunAtOf(t, db, id)
		if schedule.Type == job.ScheduleManual {
			if next != nil {
				t.Errorf("next_run_at of manual %s = %d, want NULL", id, *next)
			}
			continue
		}
		expected, ok := schedule.NextRun(anchor)
		if !ok || next == nil || *next != expected.UnixMilli() {
			t.Errorf("next_run_at of %s = %v, want %v", id, next, expected)
		}
	}

	// once 已消费后 next_run_at 归 NULL。
	consumed := onceAt
	j := newJob("job_consumed", "consumed")
	j.Schedule = schedules[1]
	j.OnceConsumedAt = &consumed
	if err := repo.Create(ctx, j); err != nil {
		t.Fatalf("Create consumed: %v", err)
	}
	if next := nextRunAtOf(t, db, "job_consumed"); next != nil {
		t.Errorf("next_run_at of consumed once = %d, want NULL", *next)
	}

	// 零值 schedule 落库为 manual。
	raw := job.Job{
		ID: "job_raw", Name: "raw", Kind: job.KindFile, Enabled: true,
		Config:    []byte(`{}`),
		CreatedAt: anchor, UpdatedAt: anchor,
	}
	if err := repo.Create(ctx, raw); err != nil {
		t.Fatalf("Create raw: %v", err)
	}
	got, err := repo.Get(ctx, "job_raw")
	if err != nil {
		t.Fatalf("Get raw: %v", err)
	}
	if got.Schedule != (job.Schedule{Type: job.ScheduleManual}) {
		t.Errorf("raw schedule = %+v, want manual", got.Schedule)
	}
}

// Delete 硬删除；二次删除报 ErrNotFound。CountBySource 统计 config
// 引用给定 Source 的 file 任务数。
func TestJobDeleteAndCountBySource(t *testing.T) {
	repo, _, _ := seedJobs(t)
	ctx := context.Background()
	for _, id := range []string{"job_x", "job_y"} {
		if err := repo.Create(ctx, newJob(id, "name-"+id)); err != nil {
			t.Fatalf("Create %s: %v", id, err)
		}
	}

	count, err := repo.CountBySource(ctx, "src_a")
	if err != nil {
		t.Fatalf("CountBySource: %v", err)
	}
	if count != 2 {
		t.Errorf("CountBySource = %d, want 2", count)
	}
	// 不同 source 引用不计数。
	if count, _ := repo.CountBySource(ctx, "src_other"); count != 0 {
		t.Errorf("CountBySource other = %d, want 0", count)
	}

	if err := repo.Delete(ctx, "job_x"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if err := repo.Delete(ctx, "job_x"); !errors.Is(err, job.ErrNotFound) {
		t.Errorf("second Delete = %v, want ErrNotFound", err)
	}
	if _, err := repo.Get(ctx, "job_x"); !errors.Is(err, job.ErrNotFound) {
		t.Errorf("Get after delete = %v, want ErrNotFound", err)
	}
}

// once 消费时间戳经 Update / Get 往返保持（毫秒精度持久化
// correctness state，独立于运行历史）。
func TestOnceConsumedAtRoundTrip(t *testing.T) {
	repo, _, _ := seedJobs(t)
	ctx := context.Background()

	j := newJob("job_once", "photos")
	j.Schedule = job.Schedule{Type: job.ScheduleOnce, Value: "2026-09-20T03:00:00Z"}
	if err := repo.Create(ctx, j); err != nil {
		t.Fatalf("Create: %v", err)
	}
	got, err := repo.Get(ctx, "job_once")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.OnceConsumedAt != nil {
		t.Fatalf("fresh once job consumed_at = %v, want nil", got.OnceConsumedAt)
	}

	at := time.Unix(1757879400, 0).UTC().Add(333 * time.Millisecond)
	consumed := at
	j.OnceConsumedAt = &consumed
	if err := repo.Update(ctx, j); err != nil {
		t.Fatalf("Update: %v", err)
	}
	got, err = repo.Get(ctx, "job_once")
	if err != nil {
		t.Fatalf("Get after update: %v", err)
	}
	if got.OnceConsumedAt == nil || !got.OnceConsumedAt.Equal(at) {
		t.Errorf("consumed_at = %v, want %v (ms precision)", got.OnceConsumedAt, at)
	}
	listed, err := repo.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	found := false
	for _, lj := range listed {
		if lj.ID == "job_once" {
			found = lj.OnceConsumedAt != nil
		}
	}
	if !found {
		t.Error("List did not keep consumed_at")
	}
}

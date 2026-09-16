// Package sqlite 的测试直接使用真实临时 SQLite 数据库，不 mock SQL。
package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"strconv"
	"testing"
	"time"

	"tinysync/internal/source"
	"tinysync/internal/storage"
	"tinysync/internal/syncjob"
)

// openRepos 打开临时数据库并完成 migration，返回 Job 与 managed 仓库。
func openRepos(t *testing.T) (*sql.DB, *Repository, *ManagedRepository) {
	t.Helper()
	dataDir := t.TempDir()
	db, err := storage.Open(dataDir)
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := storage.Migrate(context.Background(), db, dataDir); err != nil {
		t.Fatalf("storage.Migrate: %v", err)
	}
	return db, NewRepository(db), NewManagedRepository(db)
}

// mustSeedSource 在库中插入一个测试 Source，返回其 ID。
func mustSeedSource(t *testing.T, db *sql.DB, id string) {
	t.Helper()
	_, err := db.Exec(`INSERT INTO sources
		(id, name, type, endpoint, username, password, enabled, created_at, updated_at)
		VALUES (?, 'seed', 'webdav', 'https://dav.example.com/', '', '', 1, 1, 1)`, id)
	if err != nil {
		t.Fatalf("seed source: %v", err)
	}
}

// newJob 构造测试用领域对象，时间戳固定以便断言；schedule 显式 manual，
// 与落库后的取值一致（省略 Schedule 的零值 Job 由仓库归一为 manual）。
func newJob(id, name, sourceID string) syncjob.Job {
	now := time.Unix(1757879400, 0).UTC()
	return syncjob.Job{
		ID:         id,
		Name:       name,
		SourceID:   sourceID,
		RemoteRoot: "/photos",
		LocalRoot:  "/tmp/backup",
		Mode:       syncjob.ModeMirror,
		Include:    []string{"**/*.jpg"},
		Exclude:    []string{"tmp/**"},
		Enabled:    true,
		Schedule:   syncjob.Schedule{Type: syncjob.ScheduleManual},
		CreatedAt:  now,
		UpdatedAt:  now,
	}
}

// 创建后 Get 往返全部字段（含 patterns 与时间）。
func TestJobCreateAndGetRoundTrip(t *testing.T) {
	db, repo, _ := openRepos(t)
	mustSeedSource(t, db, "src_a")

	job := newJob("job_a", "photos", "src_a")
	if err := repo.Create(context.Background(), job); err != nil {
		t.Fatalf("Create: %v", err)
	}
	got, err := repo.Get(context.Background(), "job_a")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !reflect.DeepEqual(got, job) {
		t.Errorf("Get = %+v, want %+v", got, job)
	}
}

// List 按 name 大小写不敏感排序。
func TestJobListSortedNocase(t *testing.T) {
	db, repo, _ := openRepos(t)
	mustSeedSource(t, db, "src_a")
	for _, j := range []syncjob.Job{
		newJob("job_c", "Zeta", "src_a"),
		newJob("job_a", "alpha", "src_a"),
		newJob("job_b", "Midway", "src_a"),
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
	want := []string{"alpha", "Midway", "Zeta"}
	if len(names) != len(want) {
		t.Fatalf("names = %v, want %v", names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Errorf("names[%d] = %q, want %q", i, names[i], want[i])
		}
	}
}

// name 大小写不敏感唯一：仅大小写不同也算冲突。
func TestJobCreateNameConflictNocase(t *testing.T) {
	db, repo, _ := openRepos(t)
	mustSeedSource(t, db, "src_a")
	if err := repo.Create(context.Background(), newJob("job_a", "photos", "src_a")); err != nil {
		t.Fatalf("Create: %v", err)
	}
	err := repo.Create(context.Background(), newJob("job_b", "PHOTOS", "src_a"))
	if !errors.Is(err, syncjob.ErrConflict) {
		t.Fatalf("Create duplicate name = %v, want ErrConflict", err)
	}
}

// 引用不存在的 Source 创建 Job 被外键拒绝并映射为 ErrInvalid。
func TestJobCreateMissingSourceRejected(t *testing.T) {
	_, repo, _ := openRepos(t)
	err := repo.Create(context.Background(), newJob("job_a", "photos", "src_missing"))
	if !errors.Is(err, syncjob.ErrInvalid) {
		t.Fatalf("Create with missing source = %v, want ErrInvalid", err)
	}
}

// Update 整体替换可变字段；目标不存在报 ErrNotFound；改到冲突 name 报 ErrConflict。
func TestJobUpdate(t *testing.T) {
	db, repo, _ := openRepos(t)
	mustSeedSource(t, db, "src_a")
	if err := repo.Create(context.Background(), newJob("job_a", "photos", "src_a")); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := repo.Create(context.Background(), newJob("job_b", "docs", "src_a")); err != nil {
		t.Fatalf("Create b: %v", err)
	}

	updated := newJob("job_a", "vacation", "src_a")
	updated.Mode = syncjob.ModeCopy
	// 存储契约：patterns 往返后为非 nil 切片（nil 归一为空数组）。
	updated.Include = []string{}
	updated.Exclude = []string{}
	updated.Enabled = false
	if err := repo.Update(context.Background(), updated); err != nil {
		t.Fatalf("Update: %v", err)
	}
	got, err := repo.Get(context.Background(), "job_a")
	if err != nil {
		t.Fatalf("Get after update: %v", err)
	}
	if !reflect.DeepEqual(got, updated) {
		t.Errorf("Get = %+v, want %+v", got, updated)
	}

	if err := repo.Update(context.Background(), newJob("job_z", "x", "src_a")); !errors.Is(err, syncjob.ErrNotFound) {
		t.Errorf("Update missing = %v, want ErrNotFound", err)
	}
	if err := repo.Update(context.Background(), newJob("job_a", "docs", "src_a")); !errors.Is(err, syncjob.ErrConflict) {
		t.Errorf("Update to conflicting name = %v, want ErrConflict", err)
	}
}

// schedule 字段往返：四种类型（含 anchor 有无）Create/Get/Update 深度相等；
// 省略 Schedule 的零值 Job 按 manual 落库（CHECK 约束不接受空类型）；
// UpdateAndResetManaged 不丢失 schedule。
func TestJobScheduleRoundTrip(t *testing.T) {
	db, repo, managed := openRepos(t)
	mustSeedSource(t, db, "src_a")
	ctx := context.Background()

	anchor := time.Unix(1757879400, 0).UTC()
	onceAt := anchor.Add(24 * time.Hour)
	schedules := []syncjob.Schedule{
		{Type: syncjob.ScheduleManual},
		{Type: syncjob.ScheduleOnce, Value: onceAt.Format(time.RFC3339)},
		{Type: syncjob.ScheduleInterval, Value: "30m", AnchorAt: &anchor},
		{Type: syncjob.ScheduleCron, Value: "0 3 * * *", Timezone: "Asia/Singapore"},
	}
	for i, schedule := range schedules {
		id := "job_s" + strconv.Itoa(i)
		job := newJob(id, "sched-"+strconv.Itoa(i), "src_a")
		job.Schedule = schedule
		if err := repo.Create(ctx, job); err != nil {
			t.Fatalf("Create %s: %v", id, err)
		}
		got, err := repo.Get(ctx, id)
		if err != nil {
			t.Fatalf("Get %s: %v", id, err)
		}
		if !reflect.DeepEqual(got, job) {
			t.Errorf("Get %s = %+v, want %+v", id, got, job)
		}
		// interval anchor 往返后保持 UTC 时刻相等。
		if schedule.AnchorAt != nil {
			if got.Schedule.AnchorAt == nil || !got.Schedule.AnchorAt.Equal(*schedule.AnchorAt) {
				t.Errorf("anchor roundtrip = %v, want %v", got.Schedule.AnchorAt, *schedule.AnchorAt)
			}
		}
	}

	// 零值 schedule 落库为 manual。
	raw := syncjob.Job{
		ID: "job_raw", Name: "raw", SourceID: "src_a",
		RemoteRoot: "/", LocalRoot: "/tmp/raw", Mode: syncjob.ModeCopy, Enabled: true,
		CreatedAt: anchor, UpdatedAt: anchor,
	}
	if err := repo.Create(ctx, raw); err != nil {
		t.Fatalf("Create raw: %v", err)
	}
	got, err := repo.Get(ctx, "job_raw")
	if err != nil {
		t.Fatalf("Get raw: %v", err)
	}
	if got.Schedule != (syncjob.Schedule{Type: syncjob.ScheduleManual}) {
		t.Errorf("raw schedule = %+v, want manual", got.Schedule)
	}

	// UpdateAndResetManaged：mapping 变更时 schedule 完整保留。
	job := newJob("job_reset", "reset", "src_a")
	job.Schedule = schedules[3]
	if err := repo.Create(ctx, job); err != nil {
		t.Fatalf("Create reset: %v", err)
	}
	seedManagedRows(t, managed, "job_reset")
	mappingChange := job
	mappingChange.Name = "reset-2"
	mappingChange.RemoteRoot = "/elsewhere"
	if err := repo.UpdateAndResetManaged(ctx, mappingChange); err != nil {
		t.Fatalf("UpdateAndResetManaged: %v", err)
	}
	kept, err := repo.Get(ctx, "job_reset")
	if err != nil {
		t.Fatalf("Get after reset: %v", err)
	}
	if kept.Schedule != job.Schedule {
		t.Errorf("schedule after reset = %+v, want %+v", kept.Schedule, job.Schedule)
	}
	if rows := countManaged(t, managed, "job_reset"); rows != 0 {
		t.Errorf("managed after reset = %d, want 0", rows)
	}
}

// Delete 硬删除；二次删除报 ErrNotFound。CountBySource 统计引用数。
func TestJobDeleteAndCountBySource(t *testing.T) {
	db, repo, _ := openRepos(t)
	mustSeedSource(t, db, "src_a")
	if err := repo.Create(context.Background(), newJob("job_a", "photos", "src_a")); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := repo.Create(context.Background(), newJob("job_b", "docs", "src_a")); err != nil {
		t.Fatalf("Create b: %v", err)
	}

	count, err := repo.CountBySource(context.Background(), "src_a")
	if err != nil {
		t.Fatalf("CountBySource: %v", err)
	}
	if count != 2 {
		t.Errorf("CountBySource = %d, want 2", count)
	}

	if err := repo.Delete(context.Background(), "job_a"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if err := repo.Delete(context.Background(), "job_a"); !errors.Is(err, syncjob.ErrNotFound) {
		t.Errorf("second Delete = %v, want ErrNotFound", err)
	}
	if _, err := repo.Get(context.Background(), "job_a"); !errors.Is(err, syncjob.ErrNotFound) {
		t.Errorf("Get after delete = %v, want ErrNotFound", err)
	}
}

// UpdateAndResetManaged 原子性：成功时配置替换且 managed 清空；
// 更新失败（name 唯一冲突）时整体回滚，managed 记录完整保留。
func TestJobUpdateAndResetManagedAtomic(t *testing.T) {
	db, repo, managed := openRepos(t)
	mustSeedSource(t, db, "src_a")
	if err := repo.Create(context.Background(), newJob("job_a", "photos", "src_a")); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := repo.Create(context.Background(), newJob("job_b", "docs", "src_a")); err != nil {
		t.Fatalf("Create b: %v", err)
	}
	seedManagedRows(t, managed, "job_a")

	// 成功路径：更新生效，managed 清空。
	updated := newJob("job_a", "vacation", "src_a")
	if err := repo.UpdateAndResetManaged(context.Background(), updated); err != nil {
		t.Fatalf("UpdateAndResetManaged: %v", err)
	}
	got, err := repo.Get(context.Background(), "job_a")
	if err != nil || got.Name != "vacation" {
		t.Fatalf("Get after reset = %+v (%v), want name vacation", got, err)
	}
	if rows := countManaged(t, managed, "job_a"); rows != 0 {
		t.Errorf("managed after reset = %d, want 0", rows)
	}

	// 失败路径：mapping 改动 + name 撞唯一约束 → 回滚，managed 保留。
	seedManagedRows(t, managed, "job_a")
	conflicting := newJob("job_a", "docs", "src_a")
	conflicting.RemoteRoot = "/elsewhere"
	if err := repo.UpdateAndResetManaged(context.Background(), conflicting); !errors.Is(err, syncjob.ErrConflict) {
		t.Fatalf("conflicting reset = %v, want ErrConflict", err)
	}
	if rows := countManaged(t, managed, "job_a"); rows != 1 {
		t.Errorf("managed after rollback = %d, want 1 (metadata must survive)", rows)
	}
	kept, err := repo.Get(context.Background(), "job_a")
	if err != nil {
		t.Fatalf("Get after rollback: %v", err)
	}
	if kept.Name != "vacation" || kept.RemoteRoot != "/photos" {
		t.Errorf("job after rollback = %+v, want successful update state intact", kept)
	}
}

// seedManagedRows 向 managed 仓库写入一条测试记录。
func seedManagedRows(t *testing.T, managed *ManagedRepository, jobID string) {
	t.Helper()
	size := int64(10)
	err := managed.Upsert(context.Background(), []syncjob.ManagedFile{{
		JobID:        jobID,
		RemotePath:   "/photos/a.jpg",
		LocalRelPath: "photos/a.jpg",
		State:        syncjob.StateSynced,
		Remote:       source.Fingerprint{Size: 10, ETag: `"a"`},
		LocalSize:    &size,
		UpdatedAt:    time.Unix(1757879400, 0).UTC(),
	}})
	if err != nil {
		t.Fatalf("seed managed rows: %v", err)
	}
}

// countManaged 统计 Job 的 managed 记录数。
func countManaged(t *testing.T, managed *ManagedRepository, jobID string) int {
	t.Helper()
	list, err := managed.ListByJob(context.Background(), jobID)
	if err != nil {
		t.Fatalf("list managed: %v", err)
	}
	return len(list)
}

// managed 记录 Upsert（同 key 更新）、ListByJob 排序、批量 Delete、
// 全量 DeleteAllForJob，以及 local_rel_path 唯一冲突。
func TestManagedFileCRUD(t *testing.T) {
	db, repo, managed := openRepos(t)
	mustSeedSource(t, db, "src_a")
	if err := repo.Create(context.Background(), newJob("job_a", "photos", "src_a")); err != nil {
		t.Fatalf("Create job: %v", err)
	}

	mtime := int64(1757879400000000000)
	localSize := int64(128)
	file := func(remotePath, relPath string, state syncjob.ManagedState) syncjob.ManagedFile {
		return syncjob.ManagedFile{
			JobID:        "job_a",
			RemotePath:   remotePath,
			LocalRelPath: relPath,
			State:        state,
			Remote: source.Fingerprint{
				Size:       128,
				ModifiedAt: time.Unix(1757879400, 0).UTC(),
				ETag:       "\"abc\"",
			},
			LocalSize:    &localSize,
			LocalMtimeNs: &mtime,
			UpdatedAt:    time.Unix(1757879400, 0).UTC(),
		}
	}

	// 同 remote_path 二次 Upsert 更新而非插入。
	batch := []syncjob.ManagedFile{
		file("/photos/b.jpg", "photos/b.jpg", syncjob.StatePending),
		file("/photos/a.jpg", "photos/a.jpg", syncjob.StatePending),
	}
	if err := managed.Upsert(context.Background(), batch); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	synced := file("/photos/b.jpg", "photos/b.jpg", syncjob.StateSynced)
	synced.Remote.ETag = "\"def\""
	if err := managed.Upsert(context.Background(), []syncjob.ManagedFile{synced}); err != nil {
		t.Fatalf("Upsert update: %v", err)
	}

	list, err := managed.ListByJob(context.Background(), "job_a")
	if err != nil {
		t.Fatalf("ListByJob: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("managed rows = %d, want 2", len(list))
	}
	if list[0].RemotePath != "/photos/a.jpg" || list[1].RemotePath != "/photos/b.jpg" {
		t.Errorf("order = [%s, %s], want a.jpg first", list[0].RemotePath, list[1].RemotePath)
	}
	if !reflect.DeepEqual(list[1], synced) {
		t.Errorf("row b = %+v, want %+v", list[1], synced)
	}

	// 同 Job 内 local_rel_path 唯一。
	dup := file("/photos/c.jpg", "photos/a.jpg", syncjob.StatePending)
	if err := managed.Upsert(context.Background(), []syncjob.ManagedFile{dup}); !errors.Is(err, syncjob.ErrConflict) {
		t.Errorf("Upsert duplicate local_rel_path = %v, want ErrConflict", err)
	}

	// 批量删除指定 remote_path，再全量清理。
	if err := managed.Delete(context.Background(), "job_a", []string{"/photos/a.jpg"}); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	list, err = managed.ListByJob(context.Background(), "job_a")
	if err != nil {
		t.Fatalf("ListByJob after delete: %v", err)
	}
	if len(list) != 1 || list[0].RemotePath != "/photos/b.jpg" {
		t.Fatalf("rows after delete = %+v, want only b.jpg", list)
	}
	if err := managed.DeleteAllForJob(context.Background(), "job_a"); err != nil {
		t.Fatalf("DeleteAllForJob: %v", err)
	}
	if list, err = managed.ListByJob(context.Background(), "job_a"); err != nil || len(list) != 0 {
		t.Fatalf("rows after DeleteAllForJob = %+v (%v), want empty", list, err)
	}
}

// 零值 ModifiedAt 存 NULL（不写 UnixNano 伪时间戳），读回零值，
// 读写语义对称。
func TestManagedZeroModifiedAtStoredAsNull(t *testing.T) {
	db, repo, managed := openRepos(t)
	mustSeedSource(t, db, "src_a")
	if err := repo.Create(context.Background(), newJob("job_a", "photos", "src_a")); err != nil {
		t.Fatalf("Create job: %v", err)
	}

	err := managed.Upsert(context.Background(), []syncjob.ManagedFile{{
		JobID:        "job_a",
		RemotePath:   "/no-mtime.txt",
		LocalRelPath: "no-mtime.txt",
		State:        syncjob.StatePending,
		Remote:       source.Fingerprint{Size: 5, ETag: `"x"`},
		UpdatedAt:    time.Unix(1757879400, 0).UTC(),
	}})
	if err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	var mtimeNS sql.NullInt64
	if err := db.QueryRow(
		"SELECT remote_mtime_ns FROM managed_files WHERE job_id = 'job_a'",
	).Scan(&mtimeNS); err != nil {
		t.Fatalf("query mtime: %v", err)
	}
	if mtimeNS.Valid {
		t.Errorf("remote_mtime_ns = %d, want NULL for zero ModifiedAt", mtimeNS.Int64)
	}
	list, err := managed.ListByJob(context.Background(), "job_a")
	if err != nil || len(list) != 1 {
		t.Fatalf("ListByJob = %d entries (%v), want 1", len(list), err)
	}
	if !list[0].Remote.ModifiedAt.IsZero() {
		t.Errorf("ModifiedAt after roundtrip = %v, want zero", list[0].Remote.ModifiedAt)
	}
}

// Job 删除后 managed 记录经 FK CASCADE 清理。
func TestManagedCascadeOnJobDelete(t *testing.T) {
	db, repo, managed := openRepos(t)
	mustSeedSource(t, db, "src_a")
	if err := repo.Create(context.Background(), newJob("job_a", "photos", "src_a")); err != nil {
		t.Fatalf("Create job: %v", err)
	}
	file := syncjob.ManagedFile{
		JobID:        "job_a",
		RemotePath:   "/photos/a.jpg",
		LocalRelPath: "photos/a.jpg",
		State:        syncjob.StateSynced,
		UpdatedAt:    time.Unix(1757879400, 0).UTC(),
	}
	if err := managed.Upsert(context.Background(), []syncjob.ManagedFile{file}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	if err := repo.Delete(context.Background(), "job_a"); err != nil {
		t.Fatalf("Delete job: %v", err)
	}
	list, err := managed.ListByJob(context.Background(), "job_a")
	if err != nil {
		t.Fatalf("ListByJob: %v", err)
	}
	if len(list) != 0 {
		t.Errorf("managed rows after job delete = %d, want 0 (CASCADE)", len(list))
	}
}

// once 消费时间戳经 Update / Get 往返保持（毫秒精度持久化
// correctness state，独立于运行历史）。
func TestOnceConsumedForRoundTrip(t *testing.T) {
	db, repo, _ := openRepos(t)
	mustSeedSource(t, db, "src_a")
	ctx := context.Background()

	job := newJob("job_a", "photos", "src_a")
	job.Schedule = syncjob.Schedule{Type: syncjob.ScheduleOnce, Value: "2026-09-20T03:00:00Z"}
	if err := repo.Create(ctx, job); err != nil {
		t.Fatalf("Create: %v", err)
	}
	got, err := repo.Get(ctx, "job_a")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.OnceConsumedFor != nil {
		t.Fatalf("fresh once job consumed_for = %v, want nil", got.OnceConsumedFor)
	}

	at := time.Unix(1757879400, 0).UTC().Add(333 * time.Millisecond)
	consumed := at
	job.OnceConsumedFor = &consumed
	if err := repo.Update(ctx, job); err != nil {
		t.Fatalf("Update: %v", err)
	}
	got, err = repo.Get(ctx, "job_a")
	if err != nil {
		t.Fatalf("Get after update: %v", err)
	}
	if got.OnceConsumedFor == nil || !got.OnceConsumedFor.Equal(at) {
		t.Errorf("consumed_for = %v, want %v (ms precision)", got.OnceConsumedFor, at)
	}
	listed, err := repo.List(ctx)
	if err != nil || len(listed) != 1 || listed[0].OnceConsumedFor == nil {
		t.Errorf("List = %+v (%v), want consumed_for kept", listed, err)
	}
}

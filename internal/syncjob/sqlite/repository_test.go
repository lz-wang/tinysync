// Package sqlite 的测试直接使用真实临时 SQLite 数据库，不 mock SQL。
package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
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

// newJob 构造测试用领域对象，时间戳固定以便断言。
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

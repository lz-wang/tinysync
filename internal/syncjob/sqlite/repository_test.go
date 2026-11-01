// managed_files 仓储的测试直接使用真实临时 SQLite 数据库，不 mock SQL。
// 统一任务（jobs / job_runs）仓储的测试见 internal/job/sqlite。
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

// openManaged 打开临时数据库并完成初始化，返回连接与 managed 仓库。
func openManaged(t *testing.T) (*sql.DB, *ManagedRepository) {
	t.Helper()
	db, err := storage.Open(t.TempDir())
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := storage.Ensure(context.Background(), db); err != nil {
		t.Fatalf("storage.Ensure: %v", err)
	}
	return db, NewManagedRepository(db)
}

// seedJobRow 插入一条最小 file 任务行，满足 managed_files 的外键。
func seedJobRow(t *testing.T, db *sql.DB, id string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO jobs
		(id, name, kind, enabled, config_json, created_at, updated_at)
		VALUES (?, ?, 'file', 1, '{}', 1, 1)`, id, "job-"+id); err != nil {
		t.Fatalf("seed job %s: %v", id, err)
	}
}

// managed 记录 Upsert（同 key 更新）、ListByJob 排序、批量 Delete、
// 全量 DeleteAllForJob，以及 local_rel_path 唯一冲突。
func TestManagedFileCRUD(t *testing.T) {
	db, managed := openManaged(t)
	seedJobRow(t, db, "job_a")

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
	db, managed := openManaged(t)
	seedJobRow(t, db, "job_a")

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

// 任务删除后 managed 记录经 FK CASCADE 清理。
func TestManagedCascadeOnJobDelete(t *testing.T) {
	db, managed := openManaged(t)
	seedJobRow(t, db, "job_a")
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

	if _, err := db.Exec("DELETE FROM jobs WHERE id = 'job_a'"); err != nil {
		t.Fatalf("delete job: %v", err)
	}
	list, err := managed.ListByJob(context.Background(), "job_a")
	if err != nil {
		t.Fatalf("ListByJob: %v", err)
	}
	if len(list) != 0 {
		t.Errorf("managed rows after job delete = %d, want 0 (CASCADE)", len(list))
	}
}

package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"tinysync/internal/storage"
	"tinysync/internal/syncjob"
)

// seedManaged100K 向 managed_files 批量插入 100k 行（benchmark
// 装置：直接 SQL 插入，不经被测仓库）。
func seedManaged100K(b *testing.B, db *sql.DB, jobID string) {
	b.Helper()
	tx, err := db.Begin()
	if err != nil {
		b.Fatalf("begin: %v", err)
	}
	stmt, err := tx.Prepare(`INSERT INTO managed_files
		(job_id, remote_path, local_rel_path, state, remote_size, updated_at)
		VALUES (?, ?, ?, 'synced', 64, ?)`)
	if err != nil {
		b.Fatalf("prepare: %v", err)
	}
	now := time.Now().UnixMilli()
	for i := 0; i < 100_000; i++ {
		rel := fmt.Sprintf("bulk/f%06d.txt", i)
		if _, err := stmt.Exec(jobID, "/"+rel, rel, now); err != nil {
			b.Fatalf("insert %d: %v", i, err)
		}
	}
	if err := stmt.Close(); err != nil {
		b.Fatalf("close stmt: %v", err)
	}
	if err := tx.Commit(); err != nil {
		b.Fatalf("commit: %v", err)
	}
}

// BenchmarkSQLiteManaged100K：100k managed 行下的 ListByJob 全量
// 读取成本（MCP 搜索与引擎 planning 的底层查询）。基线证据：
// 只有实测不可接受才追加查询优化。
func BenchmarkSQLiteManaged100K(b *testing.B) {
	datadir := b.TempDir()
	db, err := storage.Open(datadir)
	if err != nil {
		b.Fatalf("open: %v", err)
	}
	b.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	if err := storage.Migrate(ctx, db, datadir); err != nil {
		b.Fatalf("migrate: %v", err)
	}

	jobRepo := NewRepository(db)
	managed := NewManagedRepository(db)

	jobID := "job_bench100k"
	// FK 依赖：先插入 Source 行。
	if _, err := db.Exec(`INSERT INTO sources
		(id, name, type, endpoint, username, password, enabled, created_at, updated_at)
		VALUES ('src_bench', 'bench', 'webdav', 'https://bench.example.com', '', '', 1, 1, 1)`); err != nil {
		b.Fatalf("seed source: %v", err)
	}
	if err := jobRepo.Create(ctx, syncjob.Job{
		ID:         jobID,
		Name:       "bench",
		SourceID:   "src_bench",
		RemoteRoot: "/",
		LocalRoot:  filepath.Join(datadir, "local"),
		Mode:       syncjob.ModeCopy,
		Enabled:    true,
	}); err != nil {
		b.Fatalf("create job: %v", err)
	}
	seedManaged100K(b, db, jobID)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		files, err := managed.ListByJob(ctx, jobID)
		if err != nil {
			b.Fatalf("list: %v", err)
		}
		if len(files) != 100_000 {
			b.Fatalf("rows = %d, want 100000", len(files))
		}
	}
}

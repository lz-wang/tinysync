package browser

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"tinysync/internal/storage"
	"tinysync/internal/source"
	sourcesqlite "tinysync/internal/source/sqlite"
	"tinysync/internal/source/webdav"
	"tinysync/internal/syncjob"
	jobsqlite "tinysync/internal/syncjob/sqlite"
)

// BenchmarkManagedSearch100K：100k managed 行上的 MCP 文件搜索
//（SearchManaged：全量行过滤 + 前 limit+1 命中的实时 stat）。
// 基线证据：只有实测不可接受才追加 repository 内查询优化。
func BenchmarkManagedSearch100K(b *testing.B) {
	datadir := filepath.Join(b.TempDir(), "data")
	localRoot := filepath.Join(b.TempDir(), "local")
	db, err := storage.Open(datadir)
	if err != nil {
		b.Fatalf("open: %v", err)
	}
	b.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()

	if err := storage.Migrate(ctx, db, datadir); err != nil {
		b.Fatalf("migrate: %v", err)
	}
	remotes, err := source.NewRemoteRegistry(webdav.NewFactory())
	if err != nil {
		b.Fatalf("registry: %v", err)
	}
	sources := source.NewService(sourcesqlite.New(db), remotes)
	jobRepo := jobsqlite.NewRepository(db)
	managed := jobsqlite.NewManagedRepository(db)
	jobs := syncjob.NewService(jobRepo, sources, datadir)
	local := NewLocalService(jobs, managed)

	if err := os.MkdirAll(localRoot, 0o755); err != nil {
		b.Fatalf("mkdir: %v", err)
	}

	job, err := func() (syncjob.Job, error) {
		src, err := sources.Create(ctx, source.CreateInput{
			Name: "bench-source",
			Type: source.TypeWebDAV,
			Config: source.Config{WebDAV: &source.WebDAVConfig{
				Endpoint: "https://bench.example.com/dav",
			}},
			Enabled: true,
		})
		if err != nil {
			return syncjob.Job{}, err
		}
		return jobs.Create(ctx, syncjob.CreateInput{
			Name:       "bench",
			SourceID:   src.ID,
			RemoteRoot: "/",
			LocalRoot:  localRoot,
			Mode:       syncjob.ModeCopy,
			Enabled:    true,
		})
	}()
	if err != nil {
		b.Fatalf("create job: %v", err)
	}

	seed100kManaged(b, db, job.ID)
	// 预置 60 个真实文件：搜索在前 51 次实时 stat 内命中 limit+1
	// 即停止，模拟「命中后截断」的真实路径。
	if err := os.MkdirAll(filepath.Join(localRoot, "bulk"), 0o755); err != nil {
		b.Fatalf("mkdir bulk: %v", err)
	}
	for i := 0; i < 60; i++ {
		if err := os.WriteFile(filepath.Join(localRoot, fmt.Sprintf("bulk/f%06d.txt", i)), []byte("x"), 0o644); err != nil {
			b.Fatalf("seed file: %v", err)
		}
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		result, err := local.SearchManaged(ctx, job.ID, SearchOptions{Query: "f000", Limit: 50})
		if err != nil {
			b.Fatalf("search: %v", err)
		}
		if len(result.Entries) == 0 {
			b.Fatal("search returned no entries")
		}
	}
}

// seed100kManaged 批量插入 100k managed 行（装置直写 SQL）。
func seed100kManaged(b *testing.B, db *sql.DB, jobID string) {
	b.Helper()
	tx, err := db.Begin()
	if err != nil {
		b.Fatalf("begin: %v", err)
	}
	stmt, err := tx.Prepare(`INSERT INTO managed_files
		(job_id, remote_path, local_rel_path, state, remote_size, updated_at)
		VALUES (?, ?, ?, 'synced', 1, 1)`)
	if err != nil {
		b.Fatalf("prepare: %v", err)
	}
	for i := 0; i < 100_000; i++ {
		rel := fmt.Sprintf("bulk/f%06d.txt", i)
		if _, err := stmt.Exec(jobID, "/"+rel, rel); err != nil {
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

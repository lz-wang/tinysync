// Package sqlite 的测试直接使用真实临时 SQLite 数据库，不 mock SQL。
package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"tinysync/internal/oci"
	"tinysync/internal/oci/registry"
	"tinysync/internal/storage"
)

// openRepository 打开临时数据库并完成 schema，返回仓库与清理函数。
func openRepository(t *testing.T) (*sql.DB, *Repository) {
	t.Helper()
	dataDir := t.TempDir()
	db, err := storage.Open(dataDir)
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := storage.Ensure(context.Background(), db); err != nil {
		t.Fatalf("storage.Ensure: %v", err)
	}
	return db, New(db)
}

// newRow 构造测试用领域对象，时间戳固定以便断言。
func newRow(id, name string, kind oci.RegistryKind, host string) oci.Registry {
	now := time.Unix(1757879400, 0).UTC()
	return oci.Registry{
		ID:        id,
		Name:      name,
		Kind:      kind,
		Host:      host,
		CreatedAt: now,
		UpdatedAt: now,
	}
}

// CRUD 全字段往返：mirror 行的 mirrors_json、primary 行的
// strip_registries_json / path_transform_json 均无损；name 冲突（大小写
// 不敏感）与不存在更新返回领域哨兵。
func TestRegistryCRUDRoundtrip(t *testing.T) {
	_, repo := openRepository(t)
	ctx := context.Background()

	primary := newRow("reg_1", "cnb", oci.KindPrimary, "docker.cnb.cool/lzwang/mirrors")
	primary.Username = "u"
	primary.Password = "p"
	primary.InsecureSkipVerify = true
	primary.PlainHTTP = true
	primary.StripRegistries = []string{"docker.io", "docker.io/library"}
	primary.PathTransform = &registry.PathTransformConfig{Separator: "---", Mode: "encode"}
	if err := repo.Create(ctx, primary); err != nil {
		t.Fatalf("create primary: %v", err)
	}
	mirror := newRow("reg_2", "hub-mirror", oci.KindMirror, "docker.io")
	mirror.Mirrors = []string{"docker.1ms.run", "docker.xuanyuan.me"}
	if err := repo.Create(ctx, mirror); err != nil {
		t.Fatalf("create mirror: %v", err)
	}

	if err := repo.Create(ctx, newRow("reg_3", "CNB", oci.KindPrimary, "other.example.com")); err != oci.ErrRegistryConflict {
		t.Fatalf("case-insensitive name conflict: err = %v, want ErrRegistryConflict", err)
	}

	got, err := repo.Get(ctx, "reg_1")
	if err != nil {
		t.Fatalf("get primary: %v", err)
	}
	if got.Hostname() != "docker.cnb.cool" || got.Namespace() != "lzwang/mirrors" {
		t.Fatalf("host split = %q / %q", got.Hostname(), got.Namespace())
	}
	if got.Username != "u" || got.Password != "p" || !got.InsecureSkipVerify || !got.PlainHTTP {
		t.Fatalf("auth fields = %+v", got)
	}
	if len(got.StripRegistries) != 2 || got.StripRegistries[1] != "docker.io/library" {
		t.Fatalf("strip_registries = %v", got.StripRegistries)
	}
	if got.PathTransform == nil || got.PathTransform.Separator != "---" || got.PathTransform.Mode != "encode" {
		t.Fatalf("path_transform = %+v", got.PathTransform)
	}

	gotMirror, err := repo.Get(ctx, "reg_2")
	if err != nil {
		t.Fatalf("get mirror: %v", err)
	}
	if len(gotMirror.Mirrors) != 2 || gotMirror.Mirrors[1] != "docker.xuanyuan.me" {
		t.Fatalf("mirrors = %v", gotMirror.Mirrors)
	}

	// 更新整体替换（含清空 path_transform），不存在返回哨兵。
	got.PathTransform = nil
	got.Host = "zot.example.com"
	if err := repo.Update(ctx, got); err != nil {
		t.Fatalf("update primary: %v", err)
	}
	updated, err := repo.Get(ctx, "reg_1")
	if err != nil {
		t.Fatalf("get updated: %v", err)
	}
	if updated.Host != "zot.example.com" || updated.PathTransform != nil {
		t.Fatalf("updated = %+v", updated)
	}
	missing := newRow("reg_x", "x", oci.KindPrimary, "x.example.com")
	if err := repo.Update(ctx, missing); !errors.Is(err, oci.ErrRegistryNotFound) {
		t.Fatalf("update missing: err = %v, want ErrRegistryNotFound", err)
	}

	list, err := repo.List(ctx)
	if err != nil || len(list) != 2 {
		t.Fatalf("list = %v / %v", list, err)
	}

	if err := repo.Delete(ctx, "reg_2"); err != nil {
		t.Fatalf("delete mirror: %v", err)
	}
	if err := repo.Delete(ctx, "reg_2"); !errors.Is(err, oci.ErrRegistryNotFound) {
		t.Fatalf("delete again: err = %v, want ErrRegistryNotFound", err)
	}
}

// LatestImageStates 按 (job, detail.src) 聚合最近一次运行的逐对结果，
// platform_skip 并入 skipped 后取最新 run。
func TestLatestImageStates(t *testing.T) {
	db, repo := openRepository(t)
	ctx := context.Background()

	for _, jobID := range []string{"job_a", "job_b"} {
		if _, err := db.ExecContext(ctx, `INSERT INTO jobs (id, name, kind, enabled, created_at, updated_at)
			VALUES (?, ?, 'oci', 1, 0, 0)`, jobID, jobID); err != nil {
			t.Fatalf("insert job %s: %v", jobID, err)
		}
	}
	insertRun := func(id, jobID string, startedAt int64) {
		t.Helper()
		_, err := db.ExecContext(ctx, `INSERT INTO job_runs (id, job_id, job_kind, trigger_type, state, started_at)
			VALUES (?, ?, 'oci', 'manual', 'succeeded', ?)`, id, jobID, startedAt)
		if err != nil {
			t.Fatalf("insert run %s: %v", id, err)
		}
	}
	insertItem := func(runID, action, status, src string) {
		t.Helper()
		_, err := db.ExecContext(ctx, `INSERT INTO job_run_items (run_id, action, status, bytes, error, detail_json, created_at)
			VALUES (?, ?, ?, 0, '', json_object('src', ?), ?)`, runID, action, status, src, time.Now().UnixMilli())
		if err != nil {
			t.Fatalf("insert item: %v", err)
		}
	}

	insertRun("run_1", "job_a", 1000)
	insertItem("run_1", "copy", "succeeded", "library/nginx:1.27")
	insertItem("run_1", "copy", "skipped", "library/alpine:3.20")
	insertRun("run_2", "job_a", 2000)
	insertItem("run_2", "skip", "skipped", "library/nginx:1.27")
	insertRun("run_3", "job_b", 3000)
	insertItem("run_3", "copy", "failed", "library/redis:7")

	states, err := repo.LatestImageStates(ctx, []string{"job_a", "job_b"})
	if err != nil {
		t.Fatalf("LatestImageStates: %v", err)
	}
	if len(states) != 3 {
		t.Fatalf("states = %v, want 3 entries", states)
	}
	nginx := states["job_a\x00library/nginx:1.27"]
	if nginx.RunID != "run_2" || nginx.Status != "skipped" {
		t.Fatalf("latest nginx state = %+v, want run_2 / skipped", nginx)
	}
	alpine := states["job_a\x00library/alpine:3.20"]
	if alpine.RunID != "run_1" || alpine.Status != "skipped" {
		t.Fatalf("alpine state = %+v", alpine)
	}
	redis := states["job_b\x00library/redis:7"]
	if redis.Status != "failed" {
		t.Fatalf("redis state = %+v", redis)
	}

	if empty, err := repo.LatestImageStates(ctx, nil); err != nil || len(empty) != 0 {
		t.Fatalf("empty job list: %v / %v", empty, err)
	}
}

package sqlite

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"tinysync/internal/publish"
	"tinysync/internal/storage"
)

// newRepo 构造已完成 migration 的临时数据库仓库。
func newRepo(t *testing.T) *Repository {
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
	return NewRepository(db)
}

func samplePolicy(id, publicPath string) publish.PublishedFile {
	expiry := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	return publish.PublishedFile{
		ID:         id,
		LocalPath:  filepath.Join(string(filepath.Separator), "srv", "a.txt"),
		PublicPath: publicPath,
		Enabled:    true,
		ExpiresAt:  &expiry,
		CreatedAt:  time.Date(2026, 9, 17, 0, 0, 0, 0, time.UTC),
		UpdatedAt:  time.Date(2026, 9, 17, 0, 0, 0, 0, time.UTC),
	}
}

// CRUD 往返：字段（含可空过期与时间戳）逐项一致；public_path 冲突
// 与 not found 语义正确。
func TestRepositoryCRUD(t *testing.T) {
	repo := newRepo(t)
	ctx := context.Background()

	p := samplePolicy("pub_1", "/a.txt")
	if err := repo.Create(ctx, p); err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, err := repo.Get(ctx, "pub_1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.LocalPath != p.LocalPath || got.PublicPath != "/a.txt" || !got.Enabled {
		t.Errorf("got = %+v, want %+v", got, p)
	}
	if got.ExpiresAt == nil || !got.ExpiresAt.Equal(*p.ExpiresAt) {
		t.Errorf("expires_at = %v, want %v", got.ExpiresAt, p.ExpiresAt)
	}
	if !got.CreatedAt.Equal(p.CreatedAt) || !got.UpdatedAt.Equal(p.UpdatedAt) {
		t.Errorf("timestamps = %v/%v, want %v/%v", got.CreatedAt, got.UpdatedAt, p.CreatedAt, p.UpdatedAt)
	}

	byPath, err := repo.GetByPublicPath(ctx, "/a.txt")
	if err != nil || byPath.ID != "pub_1" {
		t.Errorf("GetByPublicPath = %+v, %v; want pub_1", byPath, err)
	}

	// public_path 冲突。
	dup := samplePolicy("pub_2", "/a.txt")
	if err := repo.Create(ctx, dup); !errors.Is(err, publish.ErrConflict) {
		t.Errorf("duplicate public path = %v, want ErrConflict", err)
	}
	// 换名后可创建；Update 撞既有 public_path 同样冲突。
	dup.PublicPath = "/b.txt"
	dup.ExpiresAt = nil
	if err := repo.Create(ctx, dup); err != nil {
		t.Fatalf("Create dup: %v", err)
	}
	conflicting := dup
	conflicting.PublicPath = "/a.txt"
	if err := repo.Update(ctx, conflicting); !errors.Is(err, publish.ErrConflict) {
		t.Errorf("Update to conflicting path = %v, want ErrConflict", err)
	}

	// 无过期的更新与列表顺序。
	p2, err := repo.Get(ctx, "pub_2")
	if err != nil {
		t.Fatalf("Get pub_2: %v", err)
	}
	p2.Enabled = false
	if err := repo.Update(ctx, p2); err != nil {
		t.Fatalf("Update: %v", err)
	}
	list, err := repo.List(ctx)
	if err != nil || len(list) != 2 {
		t.Fatalf("List = %v, %v; want 2 policies", list, err)
	}
	if list[0].PublicPath != "/a.txt" || list[1].PublicPath != "/b.txt" {
		t.Errorf("list order = [%s, %s], want a.txt, b.txt", list[0].PublicPath, list[1].PublicPath)
	}
	if list[1].ExpiresAt != nil || list[1].Enabled {
		t.Errorf("pub_2 after update = %+v, want disabled without expiry", list[1])
	}

	// 删除与 not found。
	if err := repo.Delete(ctx, "pub_1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := repo.Get(ctx, "pub_1"); !errors.Is(err, publish.ErrNotFound) {
		t.Errorf("Get after delete = %v, want ErrNotFound", err)
	}
	if err := repo.Delete(ctx, "pub_1"); !errors.Is(err, publish.ErrNotFound) {
		t.Errorf("Delete missing = %v, want ErrNotFound", err)
	}
	if _, err := repo.GetByPublicPath(ctx, "/missing"); !errors.Is(err, publish.ErrNotFound) {
		t.Errorf("GetByPublicPath missing = %v, want ErrNotFound", err)
	}
}

// 编译期断言。
var _ publish.Repository = (*Repository)(nil)

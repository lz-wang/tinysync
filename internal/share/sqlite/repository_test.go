package sqlite

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"tinysync/internal/share"
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

func sampleShare(id, slug string) share.Share {
	expiry := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	name := "album"
	return share.Share{
		ID:        id,
		LocalPath: filepath.Join(string(filepath.Separator), "srv", "photos"),
		Slug:      slug,
		Name:      &name,
		IsDir:     true,
		Enabled:   true,
		ExpiresAt: &expiry,
		CreatedAt: time.Date(2026, 9, 20, 0, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, 9, 20, 0, 0, 0, 0, time.UTC),
	}
}

// CRUD 往返：字段（含可空名称与过期、时间戳）逐项一致；slug 冲突
// 与 not found 语义正确。
func TestRepositoryCRUD(t *testing.T) {
	repo := newRepo(t)
	ctx := context.Background()

	s := sampleShare("shr_1", "album")
	if err := repo.Create(ctx, s); err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, err := repo.Get(ctx, "shr_1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.LocalPath != s.LocalPath || got.Slug != "album" || !got.Enabled || !got.IsDir {
		t.Errorf("got = %+v, want %+v", got, s)
	}
	if got.Name == nil || *got.Name != "album" {
		t.Errorf("name = %+v, want album", got.Name)
	}
	if got.ExpiresAt == nil || !got.ExpiresAt.Equal(*s.ExpiresAt) {
		t.Errorf("expires_at = %v, want %v", got.ExpiresAt, s.ExpiresAt)
	}
	if !got.CreatedAt.Equal(s.CreatedAt) || !got.UpdatedAt.Equal(s.UpdatedAt) {
		t.Errorf("timestamps = %v/%v, want %v/%v", got.CreatedAt, got.UpdatedAt, s.CreatedAt, s.UpdatedAt)
	}

	bySlug, err := repo.GetBySlug(ctx, "album")
	if err != nil || bySlug.ID != "shr_1" {
		t.Errorf("GetBySlug = %+v, %v; want shr_1", bySlug, err)
	}

	// slug 冲突。
	dup := sampleShare("shr_2", "album")
	if err := repo.Create(ctx, dup); !errors.Is(err, share.ErrConflict) {
		t.Errorf("duplicate slug = %v, want ErrConflict", err)
	}
	// 换 slug 后可创建；Update 撞既有 slug 同样冲突。
	dup.Slug = "backup"
	dup.ExpiresAt = nil
	dup.Name = nil
	if err := repo.Create(ctx, dup); err != nil {
		t.Fatalf("Create dup: %v", err)
	}
	conflicting := dup
	conflicting.Slug = "album"
	if err := repo.Update(ctx, conflicting); !errors.Is(err, share.ErrConflict) {
		t.Errorf("Update to conflicting slug = %v, want ErrConflict", err)
	}

	// 无名称无过期的更新与列表顺序。
	s2, err := repo.Get(ctx, "shr_2")
	if err != nil {
		t.Fatalf("Get shr_2: %v", err)
	}
	s2.Enabled = false
	if err := repo.Update(ctx, s2); err != nil {
		t.Fatalf("Update: %v", err)
	}
	list, err := repo.List(ctx)
	if err != nil || len(list) != 2 {
		t.Fatalf("List = %v, %v; want 2 shares", list, err)
	}
	if list[0].Slug != "album" || list[1].Slug != "backup" {
		t.Errorf("list order = [%s, %s], want album, backup", list[0].Slug, list[1].Slug)
	}
	if list[1].Name != nil || list[1].ExpiresAt != nil || list[1].Enabled {
		t.Errorf("shr_2 after update = %+v, want unnamed disabled without expiry", list[1])
	}

	// 删除与 not found。
	if err := repo.Delete(ctx, "shr_1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := repo.Get(ctx, "shr_1"); !errors.Is(err, share.ErrNotFound) {
		t.Errorf("Get after delete = %v, want ErrNotFound", err)
	}
	if err := repo.Delete(ctx, "shr_1"); !errors.Is(err, share.ErrNotFound) {
		t.Errorf("Delete missing = %v, want ErrNotFound", err)
	}
	if _, err := repo.GetBySlug(ctx, "missing"); !errors.Is(err, share.ErrNotFound) {
		t.Errorf("GetBySlug missing = %v, want ErrNotFound", err)
	}
}

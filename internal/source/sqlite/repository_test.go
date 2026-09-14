// Package sqlite 的测试直接使用真实临时 SQLite 数据库，不 mock SQL。
package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"tinysync/internal/source"
	"tinysync/internal/storage"
)

// openRepository 打开临时数据库并完成 migration，返回仓库与清理函数。
func openRepository(t *testing.T) (*sql.DB, *Repository) {
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
	return db, New(db)
}

// newSource 构造测试用领域对象，时间戳固定以便断言。
func newSource(id, name string) source.Source {
	now := time.Unix(1757879400, 0).UTC()
	return source.Source{
		ID:        id,
		Name:      name,
		Type:      source.TypeWebDAV,
		Endpoint:  "https://dav.example.com/files",
		Username:  "user",
		Enabled:   true,
		CreatedAt: now,
		UpdatedAt: now,
	}
}

// mustCreate 创建测试 Source，失败即终止。
func mustCreate(t *testing.T, repo *Repository, s source.Source, password string) {
	t.Helper()
	if err := repo.Create(context.Background(), s, password); err != nil {
		t.Fatalf("create source %s: %v", s.ID, err)
	}
}

// 创建后 Get 返回全部字段，PasswordSet 正确，且不携带密码明文。
func TestCreateAndGet(t *testing.T) {
	_, repo := openRepository(t)
	ctx := context.Background()

	s := newSource("src_a", "NAS")
	s.PasswordSet = false
	mustCreate(t, repo, s, "secret")

	got, err := repo.Get(ctx, "src_a")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Name != "NAS" || got.Type != source.TypeWebDAV ||
		got.Endpoint != s.Endpoint || got.Username != "user" || !got.Enabled {
		t.Errorf("Get = %+v, want fields of %+v", got, s)
	}
	if !got.CreatedAt.Equal(s.CreatedAt) || !got.UpdatedAt.Equal(s.UpdatedAt) {
		t.Errorf("timestamps = %v/%v, want %v/%v",
			got.CreatedAt, got.UpdatedAt, s.CreatedAt, s.UpdatedAt)
	}
	if !got.PasswordSet {
		t.Error("PasswordSet = false, want true")
	}
	if got.PasswordSet && got.Username == "secret" {
		t.Error("password leaked into username")
	}

	// 匿名 Source：PasswordSet 为 false。
	anon := newSource("src_anon", "Anon")
	anon.Username = ""
	mustCreate(t, repo, anon, "")
	gotAnon, err := repo.Get(ctx, "src_anon")
	if err != nil {
		t.Fatalf("Get anon: %v", err)
	}
	if gotAnon.PasswordSet {
		t.Error("anon PasswordSet = true, want false")
	}
}

// 未知 ID 返回 ErrNotFound。
func TestGetUnknownID(t *testing.T) {
	_, repo := openRepository(t)
	if _, err := repo.Get(context.Background(), "src_missing"); !errors.Is(err, source.ErrNotFound) {
		t.Fatalf("Get unknown = %v, want ErrNotFound", err)
	}
}

// List 按 name 大小写不敏感排序。
func TestListOrdering(t *testing.T) {
	_, repo := openRepository(t)
	for id, name := range map[string]string{
		"src_c": "zeta",
		"src_a": "Alpha",
		"src_b": "beta",
	} {
		mustCreate(t, repo, newSource(id, name), "")
	}

	list, err := repo.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 3 {
		t.Fatalf("List length = %d, want 3", len(list))
	}
	wantOrder := []string{"Alpha", "beta", "zeta"}
	for i, want := range wantOrder {
		if list[i].Name != want {
			t.Errorf("list[%d].Name = %q, want %q", i, list[i].Name, want)
		}
	}
}

// 同名（含大小写差异）创建返回 ErrConflict。
func TestCreateDuplicateNameConflict(t *testing.T) {
	_, repo := openRepository(t)
	mustCreate(t, repo, newSource("src_a", "NAS"), "")

	dup := newSource("src_b", "nas")
	if err := repo.Create(context.Background(), dup, ""); !errors.Is(err, source.ErrConflict) {
		t.Fatalf("Create duplicate = %v, want ErrConflict", err)
	}
}

// 更新字段生效且 created_at 不变；password 语义完整覆盖。
func TestUpdateFieldsAndPassword(t *testing.T) {
	_, repo := openRepository(t)
	ctx := context.Background()
	s := newSource("src_a", "NAS")
	mustCreate(t, repo, s, "old-secret")

	// 1. password 为 nil：字段更新，密码保留。
	updated := s
	updated.Name = "NAS Renamed"
	updated.Endpoint = "https://dav.example.com/other"
	updated.Username = "user2"
	updated.Enabled = false
	updated.UpdatedAt = s.UpdatedAt.Add(time.Minute)
	if err := repo.Update(ctx, updated, nil); err != nil {
		t.Fatalf("Update (keep password): %v", err)
	}
	got, err := repo.Get(ctx, "src_a")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Name != "NAS Renamed" || got.Endpoint != updated.Endpoint ||
		got.Username != "user2" || got.Enabled {
		t.Errorf("after update = %+v", got)
	}
	if !got.CreatedAt.Equal(s.CreatedAt) || !got.UpdatedAt.Equal(updated.UpdatedAt) {
		t.Errorf("timestamps = %v/%v, want created %v updated %v",
			got.CreatedAt, got.UpdatedAt, s.CreatedAt, updated.UpdatedAt)
	}
	if !got.PasswordSet {
		t.Error("PasswordSet = false after nil-password update, want true")
	}
	if pw, err := repo.GetPassword(ctx, "src_a"); err != nil || pw != "old-secret" {
		t.Errorf("GetPassword after nil update = %q, %v; want old-secret", pw, err)
	}

	// 2. password 指向空串：清除密码。
	cleared := ""
	if err := repo.Update(ctx, updated, &cleared); err != nil {
		t.Fatalf("Update (clear password): %v", err)
	}
	got, err = repo.Get(ctx, "src_a")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.PasswordSet {
		t.Error("PasswordSet = true after clearing, want false")
	}
	if pw, err := repo.GetPassword(ctx, "src_a"); err != nil || pw != "" {
		t.Errorf("GetPassword after clearing = %q, %v; want empty", pw, err)
	}

	// 3. password 指向新值：替换密码。
	newPw := "new-secret"
	if err := repo.Update(ctx, updated, &newPw); err != nil {
		t.Fatalf("Update (replace password): %v", err)
	}
	if pw, err := repo.GetPassword(ctx, "src_a"); err != nil || pw != "new-secret" {
		t.Errorf("GetPassword after replace = %q, %v; want new-secret", pw, err)
	}
}

// 更新为与其他 Source 冲突的 name 返回 ErrConflict。
func TestUpdateNameConflict(t *testing.T) {
	_, repo := openRepository(t)
	mustCreate(t, repo, newSource("src_a", "first"), "")
	mustCreate(t, repo, newSource("src_b", "second"), "")

	updated := newSource("src_b", "FIRST")
	if err := repo.Update(context.Background(), updated, nil); !errors.Is(err, source.ErrConflict) {
		t.Fatalf("Update to conflicting name = %v, want ErrConflict", err)
	}
}

// 更新不存在的 ID 返回 ErrNotFound。
func TestUpdateUnknownID(t *testing.T) {
	_, repo := openRepository(t)
	if err := repo.Update(context.Background(), newSource("src_x", "x"), nil); !errors.Is(err, source.ErrNotFound) {
		t.Fatalf("Update unknown = %v, want ErrNotFound", err)
	}
}

// Delete 硬删除；未知 ID 返回 ErrNotFound；重开数据库后数据保持。
func TestDeleteAndPersistence(t *testing.T) {
	dataDir := t.TempDir()
	ctx := context.Background()

	db, err := storage.Open(dataDir)
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	if err := storage.Migrate(ctx, db, dataDir); err != nil {
		t.Fatalf("storage.Migrate: %v", err)
	}
	repo := New(db)
	mustCreate(t, repo, newSource("src_a", "NAS"), "secret")
	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// 关闭后重开：数据仍在，删除生效。
	db2, err := storage.Open(dataDir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer func() { _ = db2.Close() }()
	if err := storage.Migrate(ctx, db2, dataDir); err != nil {
		t.Fatalf("migrate after reopen: %v", err)
	}
	repo2 := New(db2)

	got, err := repo2.Get(ctx, "src_a")
	if err != nil {
		t.Fatalf("Get after reopen: %v", err)
	}
	if !got.PasswordSet {
		t.Error("PasswordSet = false after reopen, want true")
	}
	if pw, err := repo2.GetPassword(ctx, "src_a"); err != nil || pw != "secret" {
		t.Errorf("GetPassword after reopen = %q, %v; want secret", pw, err)
	}

	if err := repo2.Delete(ctx, "src_a"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := repo2.Get(ctx, "src_a"); !errors.Is(err, source.ErrNotFound) {
		t.Fatalf("Get after delete = %v, want ErrNotFound", err)
	}
	if err := repo2.Delete(ctx, "src_a"); !errors.Is(err, source.ErrNotFound) {
		t.Fatalf("Delete unknown = %v, want ErrNotFound", err)
	}
}

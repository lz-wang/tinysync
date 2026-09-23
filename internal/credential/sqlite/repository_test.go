// Package sqlite 的测试直接使用真实临时 SQLite 数据库，不 mock SQL。
package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"tinysync/internal/credential"
	"tinysync/internal/credential/keytest"
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

// testCredential 构造测试用领域对象，时间戳固定以便断言。
func testCredential(id, name string) credential.Credential {
	now := time.Unix(1760000000, 0).UTC()
	return credential.Credential{
		ID:        id,
		Name:      name,
		Type:      credential.TypeSSHKey,
		CreatedAt: now,
		UpdatedAt: now,
	}
}

// mustCreateKey 创建带私钥的测试凭据，失败即终止。
func mustCreateKey(t *testing.T, repo *Repository, c credential.Credential, secret credential.Secret) {
	t.Helper()
	if err := repo.Create(context.Background(), c, secret); err != nil {
		t.Fatalf("create credential %s: %v", c.ID, err)
	}
}

func testKey(t *testing.T) string {
	t.Helper()
	pem, err := keytest.UnencryptedEd25519()
	if err != nil {
		t.Fatalf("keytest.UnencryptedEd25519: %v", err)
	}
	return pem
}

// secretJSONOf 读取指定凭据的 secret_json 原文。
func secretJSONOf(t *testing.T, db *sql.DB, id string) string {
	t.Helper()
	var raw string
	if err := db.QueryRow("SELECT secret_json FROM credentials WHERE id = ?", id).Scan(&raw); err != nil {
		t.Fatalf("read secret_json of %s: %v", id, err)
	}
	return raw
}

// 创建后 Get 返回全部字段；secret 不在领域对象中流转。
func TestCreateAndGet(t *testing.T) {
	db, repo := openRepository(t)
	ctx := context.Background()
	key := testKey(t)

	c := testCredential("crd_a", "NAS 钥匙")
	c.Fingerprint = "SHA256:abc"
	// HasPassphrase 由 Service 从 secret 派生后传入；仓储按对象原样落库。
	c.HasPassphrase = true
	mustCreateKey(t, repo, c, credential.Secret{PrivateKey: key, PrivateKeyPassphrase: "p"})

	got, err := repo.Get(ctx, "crd_a")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Name != "NAS 钥匙" || got.Type != credential.TypeSSHKey ||
		got.Fingerprint != "SHA256:abc" || !got.HasPassphrase {
		t.Errorf("Get = %+v, want fields of %+v", got, c)
	}
	if !got.CreatedAt.Equal(c.CreatedAt) || !got.UpdatedAt.Equal(c.UpdatedAt) {
		t.Errorf("timestamps = %v / %v, want %v / %v", got.CreatedAt, got.UpdatedAt, c.CreatedAt, c.UpdatedAt)
	}

	// 无口令凭据 HasPassphrase = false。
	c2 := testCredential("crd_b", "no-pass")
	c2.Fingerprint = "SHA256:def"
	mustCreateKey(t, repo, c2, credential.Secret{PrivateKey: key})
	got2, err := repo.Get(ctx, "crd_b")
	if err != nil {
		t.Fatalf("Get crd_b: %v", err)
	}
	if got2.HasPassphrase {
		t.Errorf("HasPassphrase = true, want false")
	}

	// secret_json 落库形态：空口令不写键。
	var decoded secretJSON
	if err := json.Unmarshal([]byte(secretJSONOf(t, db, "crd_b")), &decoded); err != nil {
		t.Fatalf("decode secret_json: %v", err)
	}
	if decoded.PrivateKey == nil || *decoded.PrivateKey != key || decoded.PrivateKeyPassphrase != nil {
		t.Errorf("secret_json = %s, want only private_key key", secretJSONOf(t, db, "crd_b"))
	}
}

// name 大小写不敏感冲突。
func TestNameConflict(t *testing.T) {
	_, repo := openRepository(t)
	ctx := context.Background()
	key := testKey(t)

	mustCreateKey(t, repo, testCredential("crd_a", "NAS"), credential.Secret{PrivateKey: key})
	err := repo.Create(ctx, testCredential("crd_b", "nas"), credential.Secret{PrivateKey: key})
	if !errors.Is(err, credential.ErrConflict) {
		t.Errorf("err = %v, want ErrConflict", err)
	}
}

// List 按 name 大小写不敏感排序。
func TestListOrdering(t *testing.T) {
	_, repo := openRepository(t)
	ctx := context.Background()
	key := testKey(t)

	mustCreateKey(t, repo, testCredential("crd_c", "beta"), credential.Secret{PrivateKey: key})
	mustCreateKey(t, repo, testCredential("crd_a", "Alpha"), credential.Secret{PrivateKey: key})
	mustCreateKey(t, repo, testCredential("crd_b", "alpha2"), credential.Secret{PrivateKey: key})

	list, err := repo.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 3 {
		t.Fatalf("List len = %d, want 3", len(list))
	}
	want := []string{"Alpha", "alpha2", "beta"}
	for i, w := range want {
		if list[i].Name != w {
			t.Errorf("list[%d].Name = %q, want %q", i, list[i].Name, w)
		}
	}
}

// Rename 只更新名称：secret_json 逐字节不变（守卫断言，防止 rename
// 路径误触碰 secret）；名称冲突 409。
func TestRenamePreservesSecret(t *testing.T) {
	db, repo := openRepository(t)
	ctx := context.Background()
	key := testKey(t)

	c := testCredential("crd_a", "old")
	c.Fingerprint = "SHA256:abc"
	c.HasPassphrase = true
	mustCreateKey(t, repo, c, credential.Secret{PrivateKey: key, PrivateKeyPassphrase: "p"})

	before := secretJSONOf(t, db, "crd_a")
	if err := repo.Rename(ctx, "crd_a", "new", c.UpdatedAt.Add(time.Minute)); err != nil {
		t.Fatalf("Rename: %v", err)
	}
	after := secretJSONOf(t, db, "crd_a")
	if before != after {
		t.Errorf("secret_json changed by rename:\nbefore = %s\nafter  = %s", before, after)
	}

	got, err := repo.Get(ctx, "crd_a")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Name != "new" || got.Fingerprint != "SHA256:abc" || !got.HasPassphrase {
		t.Errorf("Get = %+v, want renamed with secret state preserved", got)
	}

	// 冲突与不存在。
	mustCreateKey(t, repo, testCredential("crd_b", "other"), credential.Secret{PrivateKey: key})
	if err := repo.Rename(ctx, "crd_a", "OTHER", time.Now()); !errors.Is(err, credential.ErrConflict) {
		t.Errorf("conflict err = %v, want ErrConflict", err)
	}
	if err := repo.Rename(ctx, "crd_missing", "x", time.Now()); !errors.Is(err, credential.ErrNotFound) {
		t.Errorf("missing err = %v, want ErrNotFound", err)
	}
}

// ReplaceSecret：secret_json 与派生列（指纹 / 口令状态）原子更新。
func TestReplaceSecret(t *testing.T) {
	db, repo := openRepository(t)
	ctx := context.Background()
	key := testKey(t)

	c := testCredential("crd_a", "a")
	c.Fingerprint = "SHA256:old"
	mustCreateKey(t, repo, c, credential.Secret{PrivateKey: key})

	newKey := testKey(t)
	if err := repo.ReplaceSecret(ctx, "crd_a", credential.Secret{PrivateKey: newKey, PrivateKeyPassphrase: "np"},
		"SHA256:new", true, c.UpdatedAt.Add(time.Minute)); err != nil {
		t.Fatalf("ReplaceSecret: %v", err)
	}

	got, err := repo.Get(ctx, "crd_a")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Fingerprint != "SHA256:new" || !got.HasPassphrase {
		t.Errorf("Get = %+v, want new fingerprint with passphrase", got)
	}

	var decoded secretJSON
	if err := json.Unmarshal([]byte(secretJSONOf(t, db, "crd_a")), &decoded); err != nil {
		t.Fatalf("decode secret_json: %v", err)
	}
	if decoded.PrivateKey == nil || *decoded.PrivateKey != newKey ||
		decoded.PrivateKeyPassphrase == nil || *decoded.PrivateKeyPassphrase != "np" {
		t.Errorf("secret_json not replaced: %s", secretJSONOf(t, db, "crd_a"))
	}

	if err := repo.ReplaceSecret(ctx, "crd_missing", credential.Secret{PrivateKey: key}, "SHA256:x", false, time.Now()); !errors.Is(err, credential.ErrNotFound) {
		t.Errorf("missing err = %v, want ErrNotFound", err)
	}
}

// 删除守卫：被引用（config 携带 credential_id 的 sources 行）时在
// 同一事务内拒绝并回显引用清单；引用清空后删除成功。
func TestDeleteGuard(t *testing.T) {
	db, repo := openRepository(t)
	ctx := context.Background()
	key := testKey(t)

	mustCreateKey(t, repo, testCredential("crd_a", "a"), credential.Secret{PrivateKey: key})
	now := time.Now().UnixMilli()
	insertRef := func(sourceID, name, credentialID string) {
		t.Helper()
		if _, err := db.ExecContext(ctx,
			`INSERT INTO sources (id, name, type, endpoint, username, password, config_json, credentials_json, enabled, created_at, updated_at)
			 VALUES (?, ?, 'sftp', '', '', '', json_object('credential_id', ?), '{}', 1, ?, ?)`,
			sourceID, name, credentialID, now, now,
		); err != nil {
			t.Fatalf("insert source %s: %v", sourceID, err)
		}
	}
	insertRef("src_1", "NAS 一", "crd_a")
	insertRef("src_2", "NAS 二", "crd_a")

	err := repo.Delete(ctx, "crd_a")
	var inUse *credential.ErrInUse
	if !errors.As(err, &inUse) {
		t.Fatalf("err = %v, want ErrInUse", err)
	}
	if len(inUse.Sources) != 2 || inUse.Sources[0].ID != "src_1" || inUse.Sources[1].Name != "NAS 二" {
		t.Errorf("Sources = %+v, want both refs", inUse.Sources)
	}
	if _, err := repo.Get(ctx, "crd_a"); err != nil {
		t.Fatalf("Get after refused delete: %v", err)
	}

	// 引用清空后删除成功。
	if _, err := db.ExecContext(ctx, "DELETE FROM sources"); err != nil {
		t.Fatalf("clear sources: %v", err)
	}
	if err := repo.Delete(ctx, "crd_a"); err != nil {
		t.Fatalf("Delete after unbind: %v", err)
	}
	if _, err := repo.Get(ctx, "crd_a"); !errors.Is(err, credential.ErrNotFound) {
		t.Errorf("Get after delete err = %v, want ErrNotFound", err)
	}
	if err := repo.Delete(ctx, "crd_a"); !errors.Is(err, credential.ErrNotFound) {
		t.Errorf("double delete err = %v, want ErrNotFound", err)
	}
}

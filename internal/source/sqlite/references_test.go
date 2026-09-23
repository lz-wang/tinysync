package sqlite

import (
	"context"
	"testing"
	"time"

	"tinysync/internal/source"
)

// 引用索引：只有 config 携带 credential_id 的行产生条目，按凭据 ID
// 正确分组与过滤；删除守卫据此拒绝或放行。
func TestCredentialReferences(t *testing.T) {
	db, repo := openRepository(t)
	ctx := context.Background()
	now := time.Now().UnixMilli()

	insert := func(id, name, credentialID string) {
		t.Helper()
		configJSON := "{}"
		if credentialID != "" {
			configJSON = `{"credential_id":"` + credentialID + `"}`
		}
		if _, err := db.ExecContext(ctx,
			`INSERT INTO sources (id, name, type, endpoint, username, password, config_json, credentials_json, enabled, created_at, updated_at)
			 VALUES (?, ?, 'sftp', '', '', '', ?, '{}', 1, ?, ?)`,
			id, name, configJSON, now, now,
		); err != nil {
			t.Fatalf("insert source %s: %v", id, err)
		}
	}

	// 两条 sftp 引用 crd_a；一条 sftp 引用 crd_b；一条 sftp 不引用；
	// 一条 webdav 不引用。
	insert("src_a1", "NAS 一", "crd_a")
	insert("src_a2", "NAS 二", "crd_a")
	insert("src_b1", "VPS", "crd_b")
	insert("src_c1", "无引用", "")
	if err := repo.Create(ctx, source.Source{
		ID:   "src_d1",
		Name: "WebDAV",
		Type: source.TypeWebDAV,
		Config: source.Config{WebDAV: &source.WebDAVConfig{
			Endpoint: "https://dav.example.com",
		}},
		Enabled:   true,
		CreatedAt: time.UnixMilli(now).UTC(),
		UpdatedAt: time.UnixMilli(now).UTC(),
	}, source.Credentials{}); err != nil {
		t.Fatalf("create webdav source: %v", err)
	}

	all, err := repo.AllCredentialReferences(ctx)
	if err != nil {
		t.Fatalf("AllCredentialReferences: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("groups = %d (%+v), want 2", len(all), all)
	}
	if len(all["crd_a"]) != 2 || all["crd_a"][0].ID != "src_a1" || all["crd_a"][1].Name != "NAS 二" {
		t.Errorf("crd_a refs = %+v, want src_a1 + src_a2", all["crd_a"])
	}
	if len(all["crd_b"]) != 1 || all["crd_b"][0].ID != "src_b1" {
		t.Errorf("crd_b refs = %+v, want src_b1", all["crd_b"])
	}
	if _, has := all["crd_missing"]; has {
		t.Errorf("unexpected refs for unknown credential")
	}

	refs, err := repo.SourcesReferencingCredential(ctx, "crd_a")
	if err != nil {
		t.Fatalf("SourcesReferencingCredential: %v", err)
	}
	if len(refs) != 2 {
		t.Errorf("refs = %+v, want 2 entries", refs)
	}

	empty, err := repo.SourcesReferencingCredential(ctx, "crd_none")
	if err != nil {
		t.Fatalf("SourcesReferencingCredential(none): %v", err)
	}
	if len(empty) != 0 {
		t.Errorf("refs = %+v, want empty", empty)
	}
}

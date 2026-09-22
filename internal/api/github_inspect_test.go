package api

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"tinysync/internal/source"
	"tinysync/internal/source/sqlite"
	"tinysync/internal/storage"
)

// fakeInspectRemote 在 fakeRemote 之上实现 ReleaseInspector：返回
// 固定预览结果。
type fakeInspectRemote struct {
	fakeRemote
	inspection *source.GitHubInspection
	inspectErr error
}

func (r fakeInspectRemote) Inspect(ctx context.Context, assetDetail int) (*source.GitHubInspection, error) {
	return r.inspection, r.inspectErr
}

// fakeInspectFactory 构造实现 ReleaseInspector 的 fake Remote，并
// 记录最近一次 Create 收到的配置与凭据，供组合形态断言。
type fakeInspectFactory struct {
	remote    source.Remote
	lastSrc   source.Source
	lastCreds source.Credentials
}

func (f *fakeInspectFactory) Type() source.Type {
	return source.TypeWebDAV
}

func (f *fakeInspectFactory) Create(ctx context.Context, s source.Source, credentials source.Credentials) (source.Remote, error) {
	f.lastSrc = s
	f.lastCreds = credentials
	return f.remote, nil
}

func newInspectRouter(t *testing.T) (testRouter, *fakeInspectFactory) {
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
	factory := &fakeInspectFactory{remote: fakeInspectRemote{
		inspection: &source.GitHubInspection{
			Repository: "gitea/gitea",
			Releases: []source.InspectedRelease{{
				Tag:         "v1.2.0",
				Name:        "Release 1.2",
				PublishedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
				Assets: []source.InspectedAsset{
					{Name: "app.tar.gz", Size: 100, DigestAvailable: true},
					{Name: "SHA256SUMS", Size: 64, DigestAvailable: false},
				},
			}},
		},
	}}
	svc := source.NewService(sqlite.New(db), factory)
	return newTestAuth(t, db, Dependencies{Sources: svc}), factory
}

// TestInspectAPIConfigForm config + token 形态：预览结果回显，token
// 明文绝不出现在响应中。
func TestInspectAPIConfigForm(t *testing.T) {
	router, _ := newInspectRouter(t)
	rec := doJSON(t, router, "POST", "/api/v1/sources/inspect", `{
		"type": "github_release",
		"config": {"repository": "gitea/gitea", "release_policy": "recent", "recent_count": 3},
		"credentials": {"token": "INSPECT_TOKEN_SECRET"}
	}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("inspect status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "INSPECT_TOKEN_SECRET") {
		t.Errorf("response leaks token: %s", rec.Body.String())
	}
	body := decodeJSON(t, rec)
	if body["ok"] != true || body["repository"] != "gitea/gitea" {
		t.Fatalf("inspect result = %v", body)
	}
	releases, _ := body["releases"].([]any)
	if len(releases) != 1 {
		t.Fatalf("releases = %v, want 1", releases)
	}
	first, _ := releases[0].(map[string]any)
	if first["tag"] != "v1.2.0" {
		t.Errorf("tag = %v, want v1.2.0", first["tag"])
	}
	assets, _ := first["assets"].([]any)
	if len(assets) != 2 {
		t.Fatalf("assets = %v, want 2", assets)
	}
}

// TestInspectAPISourceIDForm source_id 形态：服务端读取已存配置与
// 凭据（请求体不带任何 secret）。
func TestInspectAPISourceIDForm(t *testing.T) {
	router, _ := newInspectRouter(t)
	// 先创建一个 github_release Source（fakeFactory 不校验类型）。
	rec := doJSON(t, router, "POST", "/api/v1/sources", `{
		"name": "Gitea",
		"type": "github_release",
		"config": {"repository": "gitea/gitea", "release_policy": "all"},
		"credentials": {"token": "SAVED_TOKEN_SECRET"},
		"enabled": true
	}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create status = %d, body = %s", rec.Code, rec.Body.String())
	}
	created := decodeJSON(t, rec)
	id, _ := created["id"].(string)

	rec = doJSON(t, router, "POST", "/api/v1/sources/inspect", `{"source_id": "`+id+`"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("inspect status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "SAVED_TOKEN_SECRET") {
		t.Errorf("response leaks saved token: %s", rec.Body.String())
	}
	body := decodeJSON(t, rec)
	if body["ok"] != true || body["repository"] != "gitea/gitea" {
		t.Fatalf("inspect result = %v", body)
	}
}

// TestInspectAPIValidation 非法形态在 400 暴露。
func TestInspectAPIValidation(t *testing.T) {
	router, _ := newInspectRouter(t)
	rec := doJSON(t, router, "POST", "/api/v1/sources/inspect", `{"type": "webdav"}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("webdav inspect status = %d, want 400", rec.Code)
	}
	rec = doJSON(t, router, "POST", "/api/v1/sources/inspect", `{"type": "github_release"}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("missing config status = %d, want 400", rec.Code)
	}
	rec = doJSON(t, router, "POST", "/api/v1/sources/inspect", `{"source_id": "src_missing"}`)
	if rec.Code != http.StatusNotFound {
		t.Errorf("missing source status = %d, want 404", rec.Code)
	}
}

// TestInspectAPISourceIDWithConfigOverride 编辑态组合形态（验收：私有
// Source 微调表单后测试预览，沿用已存 Token 成功）：source_id + 提案
// config → 服务端用已存凭据 + 提案配置；credentials.token 同传时临
// 时覆盖已存 Token；Token 明文绝不回显。
func TestInspectAPISourceIDWithConfigOverride(t *testing.T) {
	router, factory := newInspectRouter(t)
	rec := doJSON(t, router, "POST", "/api/v1/sources", `{
		"name": "Gitea",
		"type": "github_release",
		"config": {"repository": "gitea/gitea", "release_policy": "all"},
		"credentials": {"token": "SAVED_TOKEN_SECRET"},
		"enabled": true
	}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create status = %d, body = %s", rec.Code, rec.Body.String())
	}
	created := decodeJSON(t, rec)
	id, _ := created["id"].(string)

	// 只微调 verify_sha256、不传 token：已存 Token + 提案配置生效。
	rec = doJSON(t, router, "POST", "/api/v1/sources/inspect", `{
		"source_id": "`+id+`",
		"config": {"repository": "gitea/gitea", "release_policy": "all", "verify_sha256": "required"}
	}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("inspect status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if got := factory.lastCreds.GitHubRelease; got == nil || got.Token != "SAVED_TOKEN_SECRET" {
		t.Errorf("inspect credentials = %+v, want saved token", factory.lastCreds)
	}
	if gh := factory.lastSrc.Config.GitHubRelease; gh == nil || gh.VerifySHA256 != source.SHA256Required {
		t.Errorf("proposed config not applied: %+v", factory.lastSrc.Config)
	}
	if strings.Contains(rec.Body.String(), "SAVED_TOKEN_SECRET") {
		t.Errorf("response leaks saved token: %s", rec.Body.String())
	}
	body := decodeJSON(t, rec)
	if body["ok"] != true || body["repository"] != "gitea/gitea" {
		t.Fatalf("inspect result = %v", body)
	}

	// credentials.token 同传：提案 Token 临时覆盖已存 Token。
	rec = doJSON(t, router, "POST", "/api/v1/sources/inspect", `{
		"source_id": "`+id+`",
		"config": {"repository": "gitea/gitea", "release_policy": "all"},
		"credentials": {"token": "OVERRIDE_TOKEN_SECRET"}
	}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("inspect with override status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if got := factory.lastCreds.GitHubRelease; got == nil || got.Token != "OVERRIDE_TOKEN_SECRET" {
		t.Errorf("token override not applied: %+v", factory.lastCreds)
	}
}

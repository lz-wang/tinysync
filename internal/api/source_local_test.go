package api

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"tinysync/internal/browser"
	"tinysync/internal/source"
	localadapter "tinysync/internal/source/local"
	sourcesqlite "tinysync/internal/source/sqlite"
	"tinysync/internal/storage"
	"tinysync/internal/syncjob"
	jobsqlite "tinysync/internal/syncjob/sqlite"
)

func newLocalSourceRouter(t *testing.T) testRouter {
	t.Helper()
	ctx := context.Background()
	dataDir := t.TempDir()
	db, err := storage.Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := storage.Migrate(ctx, db, dataDir); err != nil {
		t.Fatal(err)
	}
	svc := source.NewService(sourcesqlite.New(db), localadapter.NewFactory())
	jobs := syncjob.NewService(jobsqlite.NewRepository(db), svc, dataDir)
	return newTestAuth(t, db, Dependencies{Sources: svc, Jobs: jobs, Browser: browser.NewRemoteService(svc)})
}

func jsonBody(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestLocalSourceAPI(t *testing.T) {
	router := newLocalSourceRouter(t)
	root := t.TempDir()
	req := map[string]any{"name": "local", "type": "local", "config": map[string]string{"root": root}}
	w := doJSON(t, router, http.MethodPost, "/api/v1/sources", jsonBody(t, req))
	if w.Code != http.StatusCreated {
		t.Fatalf("create %d %s", w.Code, w.Body)
	}
	resp := decodeJSON(t, w)
	id := resp["id"].(string)
	endpoint := "/api/v1/sources/" + id
	want, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	if resp["config"].(map[string]any)["root"] != want || len(resp["credential_state"].(map[string]any)) != 0 {
		t.Fatalf("response=%v", resp)
	}
	for _, method := range []string{http.MethodGet, http.MethodPatch} {
		body := ""
		if method == http.MethodPatch {
			body = `{"name":"renamed"}`
		}
		if w := doJSON(t, router, method, endpoint, body); w.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", method, w.Code, w.Body)
		}
	}
	if w := doJSON(t, router, http.MethodPost, endpoint+"/test", ""); w.Code != http.StatusOK || decodeJSON(t, w)["ok"] != true {
		t.Fatalf("test: %d %s", w.Code, w.Body)
	}
	for _, creds := range []any{map[string]string{"password": "secret"}, map[string]string{}, nil} {
		req["name"] = "bad"
		req["credentials"] = creds
		if w := doJSON(t, router, http.MethodPost, "/api/v1/sources", jsonBody(t, req)); w.Code != http.StatusBadRequest {
			t.Fatalf("create creds: %d %s", w.Code, w.Body)
		}
		if w := doJSON(t, router, http.MethodPatch, endpoint, jsonBody(t, map[string]any{"credentials": creds})); w.Code != http.StatusBadRequest {
			t.Fatalf("patch creds: %d %s", w.Code, w.Body)
		}
	}
	for _, body := range []string{`{"type":"smb"}`, `{"config":{"root":"/","unknown":true}}`} {
		if w := doJSON(t, router, http.MethodPatch, endpoint, body); w.Code != http.StatusBadRequest {
			t.Fatalf("invalid: %d %s", w.Code, w.Body)
		}
	}
	jobReq := map[string]any{"name": "job", "source_id": id, "remote_root": "/", "local_root": t.TempDir(), "mode": "mirror"}
	if w := doJSON(t, router, http.MethodPost, "/api/v1/jobs", jsonBody(t, jobReq)); w.Code != http.StatusCreated {
		t.Fatalf("job: %d %s", w.Code, w.Body)
	}
	alias := filepath.Join(root, "alias")
	if err := os.Symlink(root, alias); err != nil {
		t.Skip(err)
	}
	if w := doJSON(t, router, http.MethodPatch, endpoint, jsonBody(t, map[string]any{"config": map[string]string{"root": alias}})); w.Code != http.StatusOK {
		t.Fatalf("same identity: %d %s", w.Code, w.Body)
	}
	if err := os.Remove(alias); err != nil {
		t.Fatal(err)
	}
	if w := doJSON(t, router, http.MethodPatch, endpoint, jsonBody(t, map[string]any{"config": map[string]string{"root": t.TempDir()}})); w.Code != http.StatusConflict {
		t.Fatalf("identity change: %d %s", w.Code, w.Body)
	}
	if w := doJSON(t, router, http.MethodDelete, endpoint, ""); w.Code != http.StatusConflict {
		t.Fatalf("referenced delete: %d %s", w.Code, w.Body)
	}
	if err := os.Remove(root); err != nil {
		t.Fatal(err)
	}
	if w := doJSON(t, router, http.MethodPost, endpoint+"/test", ""); w.Code != http.StatusOK || decodeJSON(t, w)["ok"] != false {
		t.Fatalf("missing root: %d %s", w.Code, w.Body)
	}
}

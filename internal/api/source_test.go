package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"tinysync/internal/source"
	"tinysync/internal/source/sqlite"
	"tinysync/internal/storage"
)

// fakeRemote 是可控的 Remote：Stat 按预设成功或失败。
type fakeRemote struct {
	statErr error
}

func (r fakeRemote) Stat(ctx context.Context, path string) (source.FileInfo, error) {
	if r.statErr != nil {
		return source.FileInfo{}, r.statErr
	}
	return source.FileInfo{Path: path, IsDir: true}, nil
}

func (r fakeRemote) List(ctx context.Context, path string) ([]source.FileInfo, error) {
	return nil, errors.New("not implemented")
}

func (r fakeRemote) Open(ctx context.Context, path string) (io.ReadCloser, error) {
	return nil, errors.New("not implemented")
}

func (r fakeRemote) Close() error {
	return nil
}

// fakeFactory 返回预设 Remote。
type fakeFactory struct {
	remote source.Remote
}

func (f fakeFactory) Type() source.Type {
	return source.TypeWebDAV
}

func (f fakeFactory) Create(ctx context.Context, s source.Source, credentials source.Credentials) (source.Remote, error) {
	return f.remote, nil
}

// newSourceRouter 构造挂载真实 Source 服务的路由。
func newSourceRouter(t *testing.T, remote source.Remote) *gin.Engine {
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
	svc := source.NewService(sqlite.New(db), fakeFactory{remote: remote})
	return NewRouter(testWebFS(), Dependencies{Sources: svc})
}

// doJSON 执行 JSON 请求并返回 recorder。
func doJSON(t *testing.T, router *gin.Engine, method, path string, body string) *httptest.ResponseRecorder {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, reader)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

// decodeJSON 解码响应体为对象。
func decodeJSON(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	return body
}

// assertNoPassword 断言响应不含密码明文，也没有 password 明文字段。
func assertNoPassword(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	if strings.Contains(rec.Body.String(), "SUPER_SECRET") {
		t.Errorf("response leaks password: %s", rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err == nil {
		if _, has := body["password"]; has {
			t.Errorf("response has password field: %s", rec.Body.String())
		}
	}
}

// 完整生命周期：创建（含密码）→ 读取 → 列表 → 更新 → 删除，
// 全程密码明文不出现在任何响应中。
func TestSourceLifecycleAPI(t *testing.T) {
	router := newSourceRouter(t, fakeRemote{})

	// POST 创建。
	rec := doJSON(t, router, "POST", "/api/v1/sources", `{
		"name": "NAS WebDAV",
		"type": "webdav",
		"endpoint": "https://dav.example.com/files",
		"username": "user",
		"password": "SUPER_SECRET",
		"enabled": true
	}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST status = %d, body = %s", rec.Code, rec.Body.String())
	}
	assertNoPassword(t, rec)
	created := decodeJSON(t, rec)
	id, _ := created["id"].(string)
	if id == "" {
		t.Fatalf("created source has no id: %s", rec.Body.String())
	}
	if created["password_set"] != true {
		t.Errorf("password_set = %v, want true", created["password_set"])
	}
	if created["created_at"] == "" || created["updated_at"] == "" {
		t.Errorf("missing timestamps: %s", rec.Body.String())
	}

	// GET 单个。
	rec = doJSON(t, router, "GET", "/api/v1/sources/"+id, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET status = %d", rec.Code)
	}
	assertNoPassword(t, rec)

	// GET 列表。
	rec = doJSON(t, router, "GET", "/api/v1/sources", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET list status = %d", rec.Code)
	}
	assertNoPassword(t, rec)
	list := decodeJSON(t, rec)
	sources, _ := list["sources"].([]any)
	if len(sources) != 1 {
		t.Fatalf("list length = %d, want 1", len(sources))
	}

	// PATCH：改名并替换密码。
	rec = doJSON(t, router, "PATCH", "/api/v1/sources/"+id,
		`{"name": "Renamed", "password": "NEW_SECRET"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("PATCH status = %d, body = %s", rec.Code, rec.Body.String())
	}
	assertNoPassword(t, rec)
	updated := decodeJSON(t, rec)
	if updated["name"] != "Renamed" {
		t.Errorf("patched name = %v, want Renamed", updated["name"])
	}
	if updated["password_set"] != true {
		t.Error("password_set = false after replace, want true")
	}

	// PATCH：清除密码。
	rec = doJSON(t, router, "PATCH", "/api/v1/sources/"+id, `{"password": ""}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("PATCH clear status = %d", rec.Code)
	}
	if decodeJSON(t, rec)["password_set"] != false {
		t.Error("password_set = true after clearing, want false")
	}

	// DELETE 后 GET 404。
	rec = doJSON(t, router, "DELETE", "/api/v1/sources/"+id, "")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("DELETE status = %d, want 204", rec.Code)
	}
	rec = doJSON(t, router, "GET", "/api/v1/sources/"+id, "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("GET after delete status = %d, want 404", rec.Code)
	}
}

// 非法输入返回 400 并带错误描述。
func TestSourceCreateValidationAPI(t *testing.T) {
	router := newSourceRouter(t, fakeRemote{})

	for name, body := range map[string]string{
		"bad type":     `{"name": "x", "type": "s3", "endpoint": "https://e.com"}`,
		"bad endpoint": `{"name": "x", "type": "webdav", "endpoint": "https://u:p@e.com"}`,
		"blank name":   `{"name": "  ", "type": "webdav", "endpoint": "https://e.com"}`,
		"bad json":     `{`,
	} {
		rec := doJSON(t, router, "POST", "/api/v1/sources", body)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", name, rec.Code)
		}
		if !strings.Contains(rec.Body.String(), "error") {
			t.Errorf("%s: body has no error field: %s", name, rec.Body.String())
		}
	}
}

// 重名创建返回 409。
func TestSourceDuplicateNameAPI(t *testing.T) {
	router := newSourceRouter(t, fakeRemote{})
	body := `{"name": "NAS", "type": "webdav", "endpoint": "https://e.com"}`
	if rec := doJSON(t, router, "POST", "/api/v1/sources", body); rec.Code != http.StatusCreated {
		t.Fatalf("first POST status = %d", rec.Code)
	}
	rec := doJSON(t, router, "POST", "/api/v1/sources", body)
	if rec.Code != http.StatusConflict {
		t.Fatalf("duplicate POST status = %d, want 409", rec.Code)
	}
}

// 未知 ID 的读取 / 更新 / 删除 / 测试均 404。
func TestSourceNotFoundAPI(t *testing.T) {
	router := newSourceRouter(t, fakeRemote{})
	for _, tc := range []struct {
		method, path string
	}{
		{"GET", "/api/v1/sources/src_missing"},
		{"PATCH", "/api/v1/sources/src_missing"},
		{"DELETE", "/api/v1/sources/src_missing"},
		{"POST", "/api/v1/sources/src_missing/test"},
	} {
		rec := doJSON(t, router, tc.method, tc.path, "{}")
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s %s status = %d, want 404", tc.method, tc.path, rec.Code)
		}
	}
}

// Connection Test 成功与失败都返回 200 与结构化结果。
func TestSourceTestAPI(t *testing.T) {
	okRouter := newSourceRouter(t, fakeRemote{})
	failRouter := newSourceRouter(t, fakeRemote{statErr: errors.New("authentication failed")})

	for _, tc := range []struct {
		name   string
		router *gin.Engine
		wantOK bool
	}{
		{"success", okRouter, true},
		{"failure", failRouter, false},
	} {
		rec := doJSON(t, tc.router, "POST", "/api/v1/sources", `{
			"name": "NAS", "type": "webdav", "endpoint": "https://e.com"
		}`)
		id, _ := decodeJSON(t, rec)["id"].(string)

		rec = doJSON(t, tc.router, "POST", "/api/v1/sources/"+id+"/test", "")
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: test status = %d, want 200", tc.name, rec.Code)
		}
		body := decodeJSON(t, rec)
		if body["ok"] != tc.wantOK {
			t.Errorf("%s: ok = %v, want %v", tc.name, body["ok"], tc.wantOK)
		}
		if _, has := body["latency_ms"]; !has {
			t.Errorf("%s: missing latency_ms", tc.name)
		}
		if !tc.wantOK && body["error"] == nil {
			t.Errorf("%s: missing error message", tc.name)
		}
		if tc.wantOK && body["error"] != nil {
			t.Errorf("%s: unexpected error %v", tc.name, body["error"])
		}
	}
}

// SPA fallback 不得吞掉未实现的 sources 子路径；错误方法 405。
func TestSourceRoutingBoundaries(t *testing.T) {
	router := newSourceRouter(t, fakeRemote{})

	rec := doJSON(t, router, "GET", "/api/v1/sources/src_x/unknown", "")
	if rec.Code != http.StatusNotFound {
		t.Errorf("unknown subpath status = %d, want 404", rec.Code)
	}

	rec = doJSON(t, router, "PATCH", "/api/v1/sources", "{}")
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("PATCH collection status = %d, want 405", rec.Code)
	}

	// 深链接仍回退 SPA。
	rec = doJSON(t, router, "GET", "/sources", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "<title>TinySync</title>") {
		t.Errorf("GET /sources should serve SPA, got %d", rec.Code)
	}
}

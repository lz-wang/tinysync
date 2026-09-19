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

func (r fakeRemote) List(ctx context.Context, path string, opts source.ListOptions) (source.FilePage, error) {
	return source.FilePage{}, errors.New("not implemented")
}

func (r fakeRemote) Open(ctx context.Context, path string) (io.ReadCloser, error) {
	return nil, errors.New("not implemented")
}

func (r fakeRemote) Close() error {
	return nil
}

// fakeFactory 返回预设 Remote；createErr 非 nil 时 Create 失败（模拟
// dial / 认证 / host key 等创建阶段故障）。
type fakeFactory struct {
	remote    source.Remote
	createErr error
}

func (f fakeFactory) Type() source.Type {
	return source.TypeWebDAV
}

func (f fakeFactory) Create(ctx context.Context, s source.Source, credentials source.Credentials) (source.Remote, error) {
	if f.createErr != nil {
		return nil, f.createErr
	}
	return f.remote, nil
}

// newSourceRouter 构造挂载真实 Source 服务的路由。
func newSourceRouter(t *testing.T, remote source.Remote) testRouter {
	t.Helper()
	return newSourceRouterWithFactory(t, fakeFactory{remote: remote})
}

// newSourceRouterWithFactory 用指定 factory 构造路由。
func newSourceRouterWithFactory(t *testing.T, factory fakeFactory) testRouter {
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
	svc := source.NewService(sqlite.New(db), factory)
	return newTestAuth(t, db, Dependencies{Sources: svc})
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

// assertNoSecret 断言响应不含 secret 明文，也没有 credentials 明文字段：
// 凭据状态只允许以 credential_state 布尔回显。
func assertNoSecret(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	for _, secret := range []string{"SUPER_SECRET", "NEW_SECRET", "TOP_SECRET_KEY", "PRIVATE_KEY_BODY"} {
		if strings.Contains(rec.Body.String(), secret) {
			t.Errorf("response leaks secret: %s", rec.Body.String())
		}
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err == nil {
		if _, has := body["credentials"]; has {
			t.Errorf("response has credentials field: %s", rec.Body.String())
		}
		if _, has := body["password"]; has {
			t.Errorf("response has password field: %s", rec.Body.String())
		}
	}
}

// 完整生命周期：创建（含密码）→ 读取 → 列表 → 更新 → 删除，
// 全程 secret 明文不出现在任何响应中。
func TestSourceLifecycleAPI(t *testing.T) {
	router := newSourceRouter(t, fakeRemote{})

	// POST 创建。
	rec := doJSON(t, router, "POST", "/api/v1/sources", `{
		"name": "NAS WebDAV",
		"type": "webdav",
		"config": {"endpoint": "https://dav.example.com/files", "username": "user"},
		"credentials": {"password": "SUPER_SECRET"},
		"enabled": true
	}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST status = %d, body = %s", rec.Code, rec.Body.String())
	}
	assertNoSecret(t, rec)
	created := decodeJSON(t, rec)
	id, _ := created["id"].(string)
	if id == "" {
		t.Fatalf("created source has no id: %s", rec.Body.String())
	}
	state, _ := created["credential_state"].(map[string]any)
	davState, _ := state["webdav"].(map[string]any)
	if davState == nil || davState["password_set"] != true {
		t.Errorf("credential_state = %v, want webdav password_set true", created["credential_state"])
	}
	config, _ := created["config"].(map[string]any)
	if config["endpoint"] != "https://dav.example.com/files" {
		t.Errorf("config = %v, want endpoint echo", config)
	}
	if created["created_at"] == "" || created["updated_at"] == "" {
		t.Errorf("missing timestamps: %s", rec.Body.String())
	}

	// GET 单个。
	rec = doJSON(t, router, "GET", "/api/v1/sources/"+id, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET status = %d", rec.Code)
	}
	assertNoSecret(t, rec)

	// GET 列表。
	rec = doJSON(t, router, "GET", "/api/v1/sources", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET list status = %d", rec.Code)
	}
	assertNoSecret(t, rec)
	list := decodeJSON(t, rec)
	sources, _ := list["sources"].([]any)
	if len(sources) != 1 {
		t.Fatalf("list length = %d, want 1", len(sources))
	}

	// PATCH：改名并替换密码（credentials 三态：非空替换）。
	rec = doJSON(t, router, "PATCH", "/api/v1/sources/"+id,
		`{"name": "Renamed", "credentials": {"password": "NEW_SECRET"}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("PATCH status = %d, body = %s", rec.Code, rec.Body.String())
	}
	assertNoSecret(t, rec)
	updated := decodeJSON(t, rec)
	if updated["name"] != "Renamed" {
		t.Errorf("patched name = %v, want Renamed", updated["name"])
	}
	state, _ = updated["credential_state"].(map[string]any)
	davState, _ = state["webdav"].(map[string]any)
	if davState == nil || davState["password_set"] != true {
		t.Error("password_set = false after replace, want true")
	}

	// PATCH：清除密码（credentials 三态：空串清除）。
	rec = doJSON(t, router, "PATCH", "/api/v1/sources/"+id,
		`{"credentials": {"password": ""}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("PATCH clear status = %d", rec.Code)
	}
	state, _ = decodeJSON(t, rec)["credential_state"].(map[string]any)
	davState, _ = state["webdav"].(map[string]any)
	if davState == nil || davState["password_set"] != false {
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

// 三协议 payload 均可创建并按各自 config / credential_state 回显。
func TestSourceCreateThreeProtocolsAPI(t *testing.T) {
	router := newSourceRouter(t, fakeRemote{})

	cases := []struct {
		name       string
		body       string
		wantState  string
		wantKey    string
		wantConfig string
	}{
		{
			name: "webdav",
			body: `{"name": "dav", "type": "webdav",
				"config": {"endpoint": "https://dav.example.com", "username": "u"},
				"credentials": {"password": "SUPER_SECRET"}}`,
			wantState:  "webdav",
			wantKey:    "password_set",
			wantConfig: "endpoint",
		},
		{
			name: "s3",
			body: `{"name": "s3", "type": "s3",
				"config": {"endpoint": "https://s3.example.com", "region": "us-east-1", "bucket": "backup", "path_style": true,
					"access_key": "AKID", "prefix": "tinysync"},
				"credentials": {"secret_key": "TOP_SECRET_KEY"}}`,
			wantState:  "s3",
			wantKey:    "secret_key_set",
			wantConfig: "bucket",
		},
		{
			name: "sftp",
			body: `{"name": "sftp", "type": "sftp",
				"config": {"host": "nas.example.com", "port": 22, "username": "u",
					"remote_root": "/srv/backups", "auth_method": "private_key",
					"host_key_fingerprint": "SHA256:UC1Dk4I9LLQOV3B8eZ5FlrUUcbbNie4INffe2TDTz3k"},
				"credentials": {"private_key": "PRIVATE_KEY_BODY", "private_key_passphrase": "pp"}}`,
			wantState:  "sftp",
			wantKey:    "private_key_set",
			wantConfig: "remote_root",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := doJSON(t, router, "POST", "/api/v1/sources", tc.body)
			if rec.Code != http.StatusCreated {
				t.Fatalf("POST status = %d, body = %s", rec.Code, rec.Body.String())
			}
			assertNoSecret(t, rec)
			created := decodeJSON(t, rec)
			state, _ := created["credential_state"].(map[string]any)
			group, _ := state[tc.wantState].(map[string]any)
			if group == nil || group[tc.wantKey] != true {
				t.Errorf("credential_state = %v, want %s.%s true", state, tc.wantState, tc.wantKey)
			}
			config, _ := created["config"].(map[string]any)
			if _, has := config[tc.wantConfig]; !has {
				t.Errorf("config = %v, want %s echo", config, tc.wantConfig)
			}
		})
	}
}

// 非法输入返回 400 并带错误描述：type/config 不匹配、未知字段、
// type 不可修改等契约错误在入口直接拒绝。
func TestSourceCreateValidationAPI(t *testing.T) {
	router := newSourceRouter(t, fakeRemote{})

	for name, body := range map[string]string{
		"unsupported type":     `{"name": "x", "type": "file", "config": {}}`,
		"webdav with s3 creds": `{"name": "x", "type": "webdav", "config": {"endpoint": "https://e.com"}, "credentials": {"secret_key": "v"}}`,
		"bad endpoint":         `{"name": "x", "type": "webdav", "config": {"endpoint": "https://u:p@e.com"}}`,
		"blank name":           `{"name": "  ", "type": "webdav", "config": {"endpoint": "https://e.com"}}`,
		"unknown config field": `{"name": "x", "type": "webdav", "config": {"endpoint": "https://e.com", "region": "x"}}`,
		"unknown top field":    `{"name": "x", "type": "webdav", "config": {"endpoint": "https://e.com"}, "password": "v"}`,
		"s3 missing secret":    `{"name": "x", "type": "s3", "config": {"region": "r", "bucket": "b", "access_key": "a"}}`,
		"bad json":             `{`,
	} {
		rec := doJSON(t, router, "POST", "/api/v1/sources", body)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400, body = %s", name, rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), "error") {
			t.Errorf("%s: body has no error field: %s", name, rec.Body.String())
		}
	}
}

// PATCH 契约：type 不可修改（携带不同 type 400）、config 整组替换
// 保留未提供字段、缺省保留、unknown field 400。
func TestSourcePatchContractAPI(t *testing.T) {
	router := newSourceRouter(t, fakeRemote{})

	created := doJSON(t, router, "POST", "/api/v1/sources", `{
		"name": "dav", "type": "webdav",
		"config": {"endpoint": "https://dav.example.com", "username": "user"},
		"credentials": {"password": "SUPER_SECRET"}
	}`)
	id, _ := decodeJSON(t, created)["id"].(string)

	// 携带不同 type → 400。
	rec := doJSON(t, router, "PATCH", "/api/v1/sources/"+id, `{"type": "s3"}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("patch type = %d, want 400", rec.Code)
	}
	// 携带相同 type → 允许（客户端回显）。
	rec = doJSON(t, router, "PATCH", "/api/v1/sources/"+id, `{"type": "webdav"}`)
	if rec.Code != http.StatusOK {
		t.Errorf("patch same type = %d, want 200", rec.Code)
	}

	// config 整组替换：endpoint 缺省会丢吗？——契约是「出现即替换」，
	// 未提供 endpoint 将校验失败（400），不是静默保留。
	rec = doJSON(t, router, "PATCH", "/api/v1/sources/"+id, `{"config": {"username": "u2"}}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("patch config replacing endpoint with blank = %d, want 400", rec.Code)
	}

	// config 整组替换：完整提供时生效。
	rec = doJSON(t, router, "PATCH", "/api/v1/sources/"+id,
		`{"config": {"endpoint": "https://other.example.com/dav", "username": "u2"}}`)
	if rec.Code != http.StatusOK {
		t.Errorf("patch full config = %d, body = %s", rec.Code, rec.Body.String())
	}
	config := decodeJSON(t, rec)["config"].(map[string]any)
	if config["endpoint"] != "https://other.example.com/dav" || config["username"] != "u2" {
		t.Errorf("config after patch = %v", config)
	}

	// credentials 缺省 → 保留。
	rec = doJSON(t, router, "PATCH", "/api/v1/sources/"+id, `{"name": "dav2"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("patch name only = %d", rec.Code)
	}
	state := decodeJSON(t, rec)["credential_state"].(map[string]any)
	if state["webdav"].(map[string]any)["password_set"] != true {
		t.Error("password_set lost by name-only patch, want preserved")
	}

	// unknown credentials field → 400。
	rec = doJSON(t, router, "PATCH", "/api/v1/sources/"+id,
		`{"credentials": {"secret_key": "v"}}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("patch credentials wrong group = %d, want 400", rec.Code)
	}
}

// 重名创建返回 409。
func TestSourceDuplicateNameAPI(t *testing.T) {
	router := newSourceRouter(t, fakeRemote{})
	body := `{"name": "NAS", "type": "webdav", "config": {"endpoint": "https://e.com"}}`
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
		router testRouter
		wantOK bool
	}{
		{"success", okRouter, true},
		{"failure", failRouter, false},
	} {
		rec := doJSON(t, tc.router, "POST", "/api/v1/sources", `{
			"name": "NAS", "type": "webdav", "config": {"endpoint": "https://e.com"}
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

// Factory 创建阶段失败（dial / 认证 / host key / 超时）沿用既有 REST
// 契约：200 + ok=false，而不是落 handleSourceError 默认分支的 500。
func TestSourceTestFactoryFailureAPI(t *testing.T) {
	router := newSourceRouterWithFactory(t, fakeFactory{
		createErr: errors.New("sftp dial 127.0.0.1:22: connection refused"),
	})

	rec := doJSON(t, router, "POST", "/api/v1/sources", `{
		"name": "NAS", "type": "webdav", "config": {"endpoint": "https://e.com"}
	}`)
	id, _ := decodeJSON(t, rec)["id"].(string)

	rec = doJSON(t, router, "POST", "/api/v1/sources/"+id+"/test", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("test status = %d, want 200", rec.Code)
	}
	body := decodeJSON(t, rec)
	if body["ok"] != false {
		t.Errorf("ok = %v, want false", body["ok"])
	}
	if body["error"] == nil {
		t.Error("missing error message")
	}
}

// 严格解码收口：请求体与 config 子对象都必须是单一 JSON 值，首个
// 值解码成功后的尾随数据不再被忽略（400）。
func TestSourceStrictDecodeTrailingDataAPI(t *testing.T) {
	router := newSourceRouter(t, fakeRemote{})

	rec := doJSON(t, router, "POST", "/api/v1/sources",
		`{"name": "NAS", "type": "webdav", "config": {"endpoint": "https://e.com"}} {"unexpected": true}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("trailing data after body = %d, want 400", rec.Code)
	}

	rec = doJSON(t, router, "POST", "/api/v1/sources",
		`{"name": "NAS", "type": "webdav", "config": {"endpoint": "https://e.com"} {"trailing": 1}}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("trailing data inside config = %d, want 400", rec.Code)
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

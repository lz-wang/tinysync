package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"tinysync/internal/source"
	sourcesqlite "tinysync/internal/source/sqlite"
	"tinysync/internal/storage"
	"tinysync/internal/syncjob"
	jobsqlite "tinysync/internal/syncjob/sqlite"
)

// scopeTestEnv 携带 session（admin）、裸引擎与 db：scope 矩阵与
// raw-token 泄漏检查共用。
type scopeTestEnv struct {
	router testRouter
	db     *sql.DB
}

// doBearer 以 Bearer token 发送请求（不附带 cookie）。
func (e *scopeTestEnv) doBearer(method, path, rawToken, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if rawToken != "" {
		req.Header.Set("Authorization", "Bearer "+rawToken)
	}
	rec := httptest.NewRecorder()
	e.router.Engine.ServeHTTP(rec, req)
	return rec
}

// newScopeTestEnv 构造带 Sources / Jobs / Runner 的认证环境。
func newScopeTestEnv(t *testing.T) *scopeTestEnv {
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
	sourceSvc := source.NewService(sourcesqlite.New(db), fakeFactory{remote: fakeRemote{}})
	jobRepo := jobsqlite.NewRepository(db)
	managedRepo := jobsqlite.NewManagedRepository(db)
	jobSvc := syncjob.NewService(jobRepo, sourceSvc, dataDir)
	runner := syncjob.NewRunner(jobRepo, managedRepo, sourceSvc, jobsqlite.NewRunRepository(db))
	router := newTestAuth(t, db, Dependencies{Sources: sourceSvc, Jobs: jobSvc, Runner: runner})
	return &scopeTestEnv{router: router, db: db}
}

// createTokenViaAPI 用 admin 会话创建 token 并返回 raw 与元数据。
func createTokenViaAPI(t *testing.T, router testRouter, body string) (string, map[string]any) {
	t.Helper()
	rec := doJSON(t, router, http.MethodPost, "/api/v1/api-tokens", body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create token status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		APIToken map[string]any `json:"api_token"`
		RawToken string         `json:"raw_token"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode create token body: %v", err)
	}
	if resp.RawToken == "" {
		t.Fatalf("create token response missing raw_token: %s", rec.Body.String())
	}
	return resp.RawToken, resp.APIToken
}

// listTokensViaAPI 返回 list 响应的 token 元数据数组。
func listTokensViaAPI(t *testing.T, router testRouter) []map[string]any {
	t.Helper()
	rec := doJSON(t, router, http.MethodGet, "/api/v1/api-tokens", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list tokens status = %d", rec.Code)
	}
	var list struct {
		APITokens []map[string]any `json:"api_tokens"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	return list.APITokens
}

// Token CRUD：create（raw 唯一一次）→ list（无 raw）→ revoke 幂等
// → 输入校验 → api-tokens 端点自身 admin-only。
func TestAPITokenLifecycleAPI(t *testing.T) {
	router := newScopeTestEnv(t).router

	raw, dto := createTokenViaAPI(t, router,
		`{"name": "automation", "scopes": ["read", "run"], "expires_at": "2026-12-31T00:00:00Z"}`)
	if dto["prefix"] != raw[:len("ts_")+8] {
		t.Errorf("prefix = %v, want %q", dto["prefix"], raw[:len("ts_")+8])
	}
	if id, _ := dto["id"].(string); id == "" {
		t.Fatalf("created token has no id: %v", dto)
	}

	// list：含元数据，绝不含 raw。
	tokens := listTokensViaAPI(t, router)
	if len(tokens) != 1 {
		t.Fatalf("list length = %d, want 1", len(tokens))
	}
	if _, has := tokens[0]["raw_token"]; has {
		t.Error("list items must not contain raw_token field")
	}
	if tokens[0]["revoked_at"] != "" {
		t.Errorf("revoked_at = %v, want empty", tokens[0]["revoked_at"])
	}
	if tokens[0]["last_used_at"] != "" {
		t.Errorf("last_used_at = %v, want empty before first use", tokens[0]["last_used_at"])
	}

	// revoke 幂等：两次都 204。
	id, _ := dto["id"].(string)
	for i := 0; i < 2; i++ {
		rec := doJSON(t, router, http.MethodPost, "/api/v1/api-tokens/"+id+"/revoke", "")
		if rec.Code != http.StatusNoContent {
			t.Fatalf("revoke #%d status = %d, want 204", i+1, rec.Code)
		}
	}
	tokens = listTokensViaAPI(t, router)
	if tokens[0]["revoked_at"] == "" {
		t.Error("list after revoke missing revoked_at")
	}

	// 未知 ID revoke → 404。
	if rec := doJSON(t, router, http.MethodPost, "/api/v1/api-tokens/tok_missing/revoke", ""); rec.Code != http.StatusNotFound {
		t.Errorf("revoke missing status = %d, want 404", rec.Code)
	}

	// 输入校验：未知 scope / 空 name / 空 scopes / 非法 expires_at → 400。
	for _, body := range []string{
		`{"name": "x", "scopes": ["sudo"]}`,
		`{"name": " ", "scopes": ["read"]}`,
		`{"name": "x", "scopes": []}`,
		`{"name": "x", "scopes": ["read"], "expires_at": "not-a-time"}`,
	} {
		if rec := doJSON(t, router, http.MethodPost, "/api/v1/api-tokens", body); rec.Code != http.StatusBadRequest {
			t.Errorf("create %s status = %d, want 400", body, rec.Code)
		}
	}

	// api-tokens 端点要求 admin：read token → 403。
	readRaw, _ := createTokenViaAPI(t, router, `{"name": "reader", "scopes": ["read"]}`)
	if rec := doJSON(t, router, http.MethodGet, "/api/v1/api-tokens", ""); rec.Code != http.StatusOK {
		t.Fatalf("admin session list tokens = %d", rec.Code)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/api-tokens", nil)
	req.Header.Set("Authorization", "Bearer "+readRaw)
	rec2 := httptest.NewRecorder()
	router.Engine.ServeHTTP(rec2, req)
	if rec2.Code != http.StatusForbidden {
		t.Errorf("read token on api-tokens = %d, want 403", rec2.Code)
	}
}

// scope 语义矩阵：read / run / admin 边界、Bearer 优先、过期与
// 撤销立即生效。
func TestTokenScopeEnforcement(t *testing.T) {
	env := newScopeTestEnv(t)
	router := env.router

	readRaw, _ := createTokenViaAPI(t, router, `{"name": "reader", "scopes": ["read"]}`)
	runRaw, _ := createTokenViaAPI(t, router, `{"name": "runner", "scopes": ["run"]}`)
	adminRaw, _ := createTokenViaAPI(t, router, `{"name": "chief", "scopes": ["admin"]}`)

	// read：GET 允许；POST run / POST admin 资源 403。
	if rec := env.doBearer(http.MethodGet, "/api/v1/sources", readRaw, ""); rec.Code != http.StatusOK {
		t.Errorf("read token GET sources = %d, want 200", rec.Code)
	}
	if rec := env.doBearer(http.MethodPost, "/api/v1/jobs/some/run", readRaw, ""); rec.Code != http.StatusForbidden {
		t.Errorf("read token run = %d, want 403", rec.Code)
	}
	if rec := env.doBearer(http.MethodPost, "/api/v1/sources", readRaw, `{"name":"x","type":"webdav","config":{"endpoint":"https://dav"}}`); rec.Code != http.StatusForbidden {
		t.Errorf("read token create source = %d, want 403", rec.Code)
	}

	// run：run 到达 handler（job 缺失为 404 而非 401/403）；read 403。
	if rec := env.doBearer(http.MethodPost, "/api/v1/jobs/some/run", runRaw, ""); rec.Code != http.StatusNotFound {
		t.Errorf("run token run = %d, want handler-level 404", rec.Code)
	}
	if rec := env.doBearer(http.MethodGet, "/api/v1/sources", runRaw, ""); rec.Code != http.StatusForbidden {
		t.Errorf("run token GET sources = %d, want 403", rec.Code)
	}

	// admin：全部允许（admin ⇒ read + run）。
	if rec := env.doBearer(http.MethodGet, "/api/v1/sources", adminRaw, ""); rec.Code != http.StatusOK {
		t.Errorf("admin token GET sources = %d, want 200", rec.Code)
	}
	if rec := env.doBearer(http.MethodPost, "/api/v1/jobs/some/run", adminRaw, ""); rec.Code != http.StatusNotFound {
		t.Errorf("admin token run = %d, want handler-level 404", rec.Code)
	}
	if rec := env.doBearer(http.MethodGet, "/api/v1/api-tokens", adminRaw, ""); rec.Code != http.StatusOK {
		t.Errorf("admin token list tokens = %d, want 200", rec.Code)
	}

	// Bearer 优先：有效 bearer + 有效 cookie 时以 bearer 为准，
	// session 端点拒绝 token principal。
	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/session", nil)
	req.AddCookie(router.session)
	req.Header.Set("Authorization", "Bearer "+readRaw)
	rec2 := httptest.NewRecorder()
	router.Engine.ServeHTTP(rec2, req)
	if rec2.Code != http.StatusUnauthorized {
		t.Errorf("bearer on session endpoint = %d, want 401", rec2.Code)
	}

	// 撤销后立即失效。
	tokens := listTokensViaAPI(t, router)
	readerID := ""
	for _, tok := range tokens {
		if tok["name"] == "reader" {
			readerID, _ = tok["id"].(string)
		}
	}
	if readerID == "" {
		t.Fatal("reader token not found in list")
	}
	if rec := doJSON(t, router, http.MethodPost, "/api/v1/api-tokens/"+readerID+"/revoke", ""); rec.Code != http.StatusNoContent {
		t.Fatalf("revoke reader = %d", rec.Code)
	}
	if rec := env.doBearer(http.MethodGet, "/api/v1/sources", readRaw, ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("revoked token = %d, want 401", rec.Code)
	}

	// 过期 token：创建约 1 秒后过期，认证 401。
	soon := time.Now().UTC().Add(1100 * time.Millisecond).Format(time.RFC3339)
	expiredRaw, _ := createTokenViaAPI(t, router,
		fmt.Sprintf(`{"name": "short", "scopes": ["read"], "expires_at": %q}`, soon))
	time.Sleep(1200 * time.Millisecond)
	if rec := env.doBearer(http.MethodGet, "/api/v1/sources", expiredRaw, ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("expired token = %d, want 401", rec.Code)
	}
}

// Bearer API Token 不做 CSRF Origin 校验：显式凭据不受浏览器 cookie
// 环境影响，跨源 Origin 的 Bearer 变更请求放行到 handler；Web
// Session 的跨源变更仍被拒绝（契约：CSRF 只约束 cookie 凭据）。
func TestBearerTokenSkipsCSRFCheck(t *testing.T) {
	env := newScopeTestEnv(t)
	runRaw, _ := createTokenViaAPI(t, env.router, `{"name": "runner", "scopes": ["run"]}`)

	// run token + 跨源 Origin + POST：到达 handler（job 缺失 404），
	// 绝不 403 CSRF。
	req := httptest.NewRequest(http.MethodPost, "/api/v1/jobs/some/run", nil)
	req.Header.Set("Authorization", "Bearer "+runRaw)
	req.Header.Set("Origin", "http://evil.example.com")
	rec := httptest.NewRecorder()
	env.router.Engine.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("bearer POST with cross-origin origin = %d, want handler-level 404 (no CSRF check)", rec.Code)
	}

	// 同源 Bearer 同样放行（对照）。
	req = httptest.NewRequest(http.MethodPost, "/api/v1/jobs/some/run", nil)
	req.Header.Set("Authorization", "Bearer "+runRaw)
	req.Header.Set("Origin", "http://example.com")
	rec = httptest.NewRecorder()
	env.router.Engine.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("bearer POST with same-origin = %d, want handler-level 404", rec.Code)
	}

	// Web Session + 跨源 Origin 的变更请求仍被 CSRF 校验拒绝。
	req = httptest.NewRequest(http.MethodPost, "/api/v1/sources", strings.NewReader("{}"))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(env.router.session)
	req.Header.Set("Origin", "http://evil.example.com")
	rec = httptest.NewRecorder()
	env.router.Engine.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("session POST with cross-origin origin = %d, want 403 (CSRF)", rec.Code)
	}
}

// raw token 不落入数据库任何文本列，也无法从 list API 取回。
func TestRawTokenAbsentFromDatabase(t *testing.T) {
	env := newScopeTestEnv(t)
	raw, _ := createTokenViaAPI(t, env.router, `{"name": "leak-check", "scopes": ["read"]}`)
	secret := strings.TrimPrefix(raw, "ts_")

	// list API 无法取回 raw。
	tokens := listTokensViaAPI(t, env.router)
	for _, tok := range tokens {
		for key, value := range tok {
			if s, ok := value.(string); ok && strings.Contains(s, secret) {
				t.Errorf("list field %s leaks raw secret", key)
			}
		}
	}

	// 数据库文本列不含 raw secret（token_hash 为 SHA-256 摘要，
	// 结构上不可能还原 raw）。
	rows, err := env.db.QueryContext(context.Background(),
		"SELECT name, prefix, scopes_json FROM api_tokens")
	if err != nil {
		t.Fatalf("query api_tokens: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var name, prefix, scopes string
		if err := rows.Scan(&name, &prefix, &scopes); err != nil {
			t.Fatalf("scan api token: %v", err)
		}
		for _, col := range []string{name, prefix, scopes} {
			if strings.Contains(col, secret) {
				t.Errorf("column %q leaks raw secret", col)
			}
		}
	}
}

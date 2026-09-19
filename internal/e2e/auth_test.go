package e2e

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/gin-gonic/gin"

	"tinysync/internal/api"
	"tinysync/internal/auth"
	authsqlite "tinysync/internal/auth/sqlite"
	"tinysync/internal/syncjob"
)

// 认证边界端到端：完整 HTTP 栈（Gin router + 真实 SQLite + 真实
// 同步 Runner）验证 v0.7 安全契约——default-deny、session 生命周期、
// Bearer Token scope 边界、/published 公开语义与重启持久化。

// e2eAdminPassword 是 E2E 环境的管理员密码（与 browserEnv 一致）。
const e2eAdminPassword = "e2e-admin-password"

// doRaw 不带任何凭据直接请求 router（用于验证公开 / 拒绝语义）。
func doRaw(t *testing.T, router http.Handler, method, path, body string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	var reader *strings.Reader
	if body != "" {
		reader = strings.NewReader(body)
	} else {
		reader = strings.NewReader("")
	}
	req := httptest.NewRequest(method, path, reader)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

// loginViaAPI 用登录端点建立会话，返回 cookie。
func loginViaAPI(t *testing.T, router http.Handler, password string) *http.Cookie {
	t.Helper()
	rec := doRaw(t, router, http.MethodPost, "/api/v1/auth/login",
		`{"password": "`+password+`"}`, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("login status = %d, body = %s", rec.Code, rec.Body.String())
	}
	cookies := rec.Result().Cookies()
	for _, c := range cookies {
		if c.Name == "tinysync_session" && c.Value != "" {
			return c
		}
	}
	t.Fatal("login response missing session cookie")
	return nil
}

// 未认证客户端无法访问任何管理 API；公开端点与公开文件保持开放。
func TestAuthE2EDefaultDeny(t *testing.T) {
	remote := protocolFixtures()[0].fixture(t)
	e := newBrowserEnv(t, remote)

	// 管理端点全部 401。
	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/sources"},
		{http.MethodGet, "/api/v1/jobs"},
		{http.MethodGet, "/api/v1/runs"},
		{http.MethodGet, "/api/v1/published-files"},
		{http.MethodGet, "/api/v1/api-tokens"},
		{http.MethodGet, "/api/v1/auth/session"},
		{http.MethodPost, "/api/v1/sources"},
	} {
		rec := doRaw(t, e.router, tc.method, tc.path, "{}", nil)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s without auth = %d, want 401", tc.method, tc.path, rec.Code)
		}
	}

	// 公开端点保持 200 / 非 401。
	if rec := doRaw(t, e.router, http.MethodGet, "/api/v1/health", "", nil); rec.Code != http.StatusOK {
		t.Errorf("health = %d, want 200", rec.Code)
	}
	if rec := doRaw(t, e.router, http.MethodGet, "/api/v1/version", "", nil); rec.Code != http.StatusOK {
		t.Errorf("version = %d, want 200", rec.Code)
	}

	// 登录：错误密码统一 401；正确密码 200 + HttpOnly cookie。
	rec := doRaw(t, e.router, http.MethodPost, "/api/v1/auth/login",
		`{"password": "totally-wrong-pass"}`, nil)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("wrong password = %d, want 401", rec.Code)
	}
	cookie := loginViaAPI(t, e.router, e2eAdminPassword)
	if !cookie.HttpOnly {
		t.Error("session cookie must be HttpOnly")
	}

	// URL 携带凭据 → 400；无效 Bearer（带有效 cookie）→ 401 不 fallback。
	rec = doRaw(t, e.router, http.MethodGet, "/api/v1/sources?token=ts_x", "", nil)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("query token = %d, want 400", rec.Code)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/sources", nil)
	req.AddCookie(e.session)
	req.Header.Set("Authorization", "Bearer ts_forged_token")
	rec = httptest.NewRecorder()
	e.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("invalid bearer with valid session = %d, want 401", rec.Code)
	}

	// 跨源 logout → 403（CSRF 纵深防御）。
	req = httptest.NewRequest(http.MethodPost, "/api/v1/auth/logout", nil)
	req.AddCookie(e.session)
	req.Header.Set("Origin", "http://evil.example.com")
	rec = httptest.NewRecorder()
	e.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("cross-origin logout = %d, want 403", rec.Code)
	}
}

// 会话生命周期：logout 立即失效；密码重置废弃全部既有会话。
func TestAuthE2ESessionLifecycle(t *testing.T) {
	dataDir := t.TempDir()
	db := openDB(t, dataDir)
	authSvc := auth.NewService(authsqlite.NewRepository(db))
	if err := authSvc.SetAdminPassword(context.Background(), e2eAdminPassword); err != nil {
		t.Fatalf("bootstrap admin: %v", err)
	}
	router := api.NewRouter(fstest.MapFS{}, api.Dependencies{Auth: authSvc})

	sessionA := loginViaAPI(t, router, e2eAdminPassword)
	sessionB := loginViaAPI(t, router, e2eAdminPassword)

	// logout A 只影响 A。
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/logout", nil)
	req.AddCookie(sessionA)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("logout = %d, want 204", rec.Code)
	}
	req = httptest.NewRequest(http.MethodGet, "/api/v1/auth/session", nil)
	req.AddCookie(sessionA)
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("session A after logout = %d, want 401", rec.Code)
	}
	req = httptest.NewRequest(http.MethodGet, "/api/v1/auth/session", nil)
	req.AddCookie(sessionB)
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("session B after logout of A = %d, want 200", rec.Code)
	}

	// 密码重置：全部会话立即失效。
	if err := authSvc.SetAdminPassword(context.Background(), "rotated-e2e-password"); err != nil {
		t.Fatalf("reset password: %v", err)
	}
	for name, session := range map[string]*http.Cookie{"A": sessionA, "B": sessionB} {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/session", nil)
		req.AddCookie(session)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("session %s after reset = %d, want 401", name, rec.Code)
		}
	}
	// 新密码可登录，旧密码拒绝。
	if rec := doRaw(t, router, http.MethodPost, "/api/v1/auth/login",
		`{"password": "`+e2eAdminPassword+`"}`, nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("old password after reset = %d, want 401", rec.Code)
	}
	loginViaAPI(t, router, "rotated-e2e-password")
}

// scope 边界：read / run / admin token 对真实 Runner 的权限矩阵，
// 撤销与过期立即生效，raw token 不落库。
func TestAuthE2ETokenScopes(t *testing.T) {
	remote := protocolFixtures()[0].fixture(t)
	e := newBrowserEnv(t, remote)
	e.createJob(syncjob.ModeCopy)

	createToken := func(t *testing.T, name, scopes string) string {
		t.Helper()
		rec := e.doJSON(http.MethodPost, "/api/v1/api-tokens",
			`{"name": "`+name+`", "scopes": `+scopes+`}`)
		if rec.Code != http.StatusCreated {
			t.Fatalf("create token %s = %d, body = %s", name, rec.Code, rec.Body.String())
		}
		var body struct {
			RawToken string `json:"raw_token"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode create token: %v", err)
		}
		return body.RawToken
	}
	bearer := func(raw string) map[string]string {
		return map[string]string{"Authorization": "Bearer " + raw}
	}

	readRaw := createToken(t, "reader", `["read"]`)
	runRaw := createToken(t, "runner", `["run"]`)
	adminRaw := createToken(t, "chief", `["admin"]`)

	// read：查询允许；run / 管理动作 403。
	if rec := doRaw(t, e.router, http.MethodGet, "/api/v1/sources", "", bearer(readRaw)); rec.Code != http.StatusOK {
		t.Errorf("read GET sources = %d, want 200", rec.Code)
	}
	if rec := doRaw(t, e.router, http.MethodPost, "/api/v1/jobs/"+e.job.ID+"/run", "", bearer(readRaw)); rec.Code != http.StatusForbidden {
		t.Errorf("read run = %d, want 403", rec.Code)
	}
	if rec := doRaw(t, e.router, http.MethodPost, "/api/v1/published-files", "{}", bearer(readRaw)); rec.Code != http.StatusForbidden {
		t.Errorf("read publish = %d, want 403", rec.Code)
	}

	// run：真实触发允许（202）；read 403。
	if rec := doRaw(t, e.router, http.MethodPost, "/api/v1/jobs/"+e.job.ID+"/run", "", bearer(runRaw)); rec.Code != http.StatusAccepted {
		t.Errorf("run token run = %d, want 202", rec.Code)
	}
	if rec := doRaw(t, e.router, http.MethodGet, "/api/v1/sources", "", bearer(runRaw)); rec.Code != http.StatusForbidden {
		t.Errorf("run GET sources = %d, want 403", rec.Code)
	}
	// HEAD 下载与 GET 同为 read scope：run-only token 不得借 HEAD
	// 探测文件元信息（回归：HEAD 曾遗漏 requireScope）。
	if rec := doRaw(t, e.router, http.MethodHead, "/api/v1/jobs/"+e.job.ID+"/files/download?path=/", "", bearer(runRaw)); rec.Code != http.StatusForbidden {
		t.Errorf("run token HEAD download = %d, want 403", rec.Code)
	}

	// admin：read + run + token 管理。
	if rec := doRaw(t, e.router, http.MethodGet, "/api/v1/sources", "", bearer(adminRaw)); rec.Code != http.StatusOK {
		t.Errorf("admin GET sources = %d, want 200", rec.Code)
	}
	if rec := doRaw(t, e.router, http.MethodGet, "/api/v1/api-tokens", "", bearer(adminRaw)); rec.Code != http.StatusOK {
		t.Errorf("admin GET tokens = %d, want 200", rec.Code)
	}

	// 撤销立即失效。
	rec := e.doJSON(http.MethodPost, "/api/v1/api-tokens/tok_missing/revoke", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("revoke missing sanity = %d, want 404", rec.Code)
	}
	var list struct {
		APITokens []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"api_tokens"`
	}
	rec = e.doJSON(http.MethodGet, "/api/v1/api-tokens", "")
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode tokens list: %v", err)
	}
	runTokenID := ""
	for _, token := range list.APITokens {
		if token.Name == "runner" {
			runTokenID = token.ID
		}
	}
	if rec = e.doJSON(http.MethodPost, "/api/v1/api-tokens/"+runTokenID+"/revoke", ""); rec.Code != http.StatusNoContent {
		t.Fatalf("revoke runner = %d, want 204", rec.Code)
	}
	if rec = doRaw(t, e.router, http.MethodPost, "/api/v1/jobs/"+e.job.ID+"/run", "", bearer(runRaw)); rec.Code != http.StatusUnauthorized {
		t.Errorf("revoked token run = %d, want 401", rec.Code)
	}

	// 过期立即失效。
	soon := time.Now().UTC().Add(1100 * time.Millisecond).Format(time.RFC3339)
	rec = e.doJSON(http.MethodPost, "/api/v1/api-tokens",
		`{"name": "short", "scopes": ["read"], "expires_at": "`+soon+`"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create short token = %d", rec.Code)
	}
	var shortBody struct {
		RawToken string `json:"raw_token"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &shortBody); err != nil {
		t.Fatalf("decode short token: %v", err)
	}
	time.Sleep(1200 * time.Millisecond)
	if rec = doRaw(t, e.router, http.MethodGet, "/api/v1/sources", "", bearer(shortBody.RawToken)); rec.Code != http.StatusUnauthorized {
		t.Errorf("expired token = %d, want 401", rec.Code)
	}

	// raw token 不出现在 list 响应与数据库文本列。
	rec = e.doJSON(http.MethodGet, "/api/v1/api-tokens", "")
	if strings.Contains(rec.Body.String(), strings.TrimPrefix(runRaw, "ts_")) {
		t.Error("token list leaks raw secret")
	}
	rows, err := e.db.QueryContext(context.Background(),
		"SELECT name, prefix, scopes_json FROM api_tokens")
	if err != nil {
		t.Fatalf("query api_tokens: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var name, prefix, scopes string
		if err := rows.Scan(&name, &prefix, &scopes); err != nil {
			t.Fatalf("scan token row: %v", err)
		}
		for _, col := range []string{name, prefix, scopes} {
			if strings.Contains(col, strings.TrimPrefix(runRaw, "ts_")) {
				t.Errorf("db column %q leaks raw secret", col)
			}
		}
	}
}

// /published/*path 保持显式公开：无凭据可访问，管理端点仍 401。
func TestAuthE2EPublishedStaysPublic(t *testing.T) {
	remote := protocolFixtures()[0].fixture(t)
	e := newBrowserEnv(t, remote)
	remote.put(t, "/managed.txt", "public-content")
	e.createJob(syncjob.ModeCopy)
	e.sync()

	rec := e.doJSON(http.MethodPost, "/api/v1/published-files",
		`{"job_id": "`+e.job.ID+`", "path": "/managed.txt", "public_path": "/authcheck.txt"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create publish policy = %d, body = %s", rec.Code, rec.Body.String())
	}

	// 公开 URL 无凭据 200；管理端点无凭据 401。
	if rec = doRaw(t, e.router, http.MethodGet, "/published/authcheck.txt", "", nil); rec.Code != http.StatusOK {
		t.Errorf("published without auth = %d, want 200", rec.Code)
	}
	if rec = doRaw(t, e.router, http.MethodGet, "/api/v1/published-files", "", nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("published-files management without auth = %d, want 401", rec.Code)
	}
}

// 重启后 admin / 会话 / token / 撤销状态正确恢复。
func TestAuthE2ERestartPersistence(t *testing.T) {
	dataDir := t.TempDir()
	db := openDB(t, dataDir)
	authSvc := auth.NewService(authsqlite.NewRepository(db))
	if err := authSvc.SetAdminPassword(context.Background(), e2eAdminPassword); err != nil {
		t.Fatalf("bootstrap admin: %v", err)
	}
	router := api.NewRouter(fstest.MapFS{}, api.Dependencies{Auth: authSvc})
	session := loginViaAPI(t, router, e2eAdminPassword)

	// 创建并撤销一个 token；保留一个未撤销 token。
	createToken := func(name string) (string, string) {
		rec := doRaw(t, router, http.MethodPost, "/api/v1/api-tokens",
			`{"name": "`+name+`", "scopes": ["read"]}`,
			map[string]string{"Cookie": session.Name + "=" + session.Value})
		if rec.Code != http.StatusCreated {
			t.Fatalf("create token %s = %d, body = %s", name, rec.Code, rec.Body.String())
		}
		var body struct {
			APIToken struct {
				ID string `json:"id"`
			} `json:"api_token"`
			RawToken string `json:"raw_token"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode token response: %v", err)
		}
		return body.APIToken.ID, body.RawToken
	}
	revokedID, revokedRaw := createToken("revoked")
	_, liveRaw := createToken("live")
	rec := doRaw(t, router, http.MethodPost, "/api/v1/api-tokens/"+revokedID+"/revoke", "",
		map[string]string{"Cookie": session.Name + "=" + session.Value})
	if rec.Code != http.StatusNoContent {
		t.Fatalf("revoke = %d, want 204", rec.Code)
	}

	// 「重启」：关闭并重新打开数据库，重建认证栈。
	if err := db.Close(); err != nil {
		t.Fatalf("close db: %v", err)
	}
	reopened := openDB(t, dataDir)
	t.Cleanup(func() { _ = reopened.Close() })
	restartedAuth := auth.NewService(authsqlite.NewRepository(reopened))
	restartedRouter := api.NewRouter(fstest.MapFS{}, api.Dependencies{Auth: restartedAuth})

	// admin 已初始化；会话跨重启仍有效。
	configured, err := restartedAuth.AdminConfigured(context.Background())
	if err != nil || !configured {
		t.Fatalf("admin after restart = (%v, %v), want (true, nil)", configured, err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/session", nil)
	req.AddCookie(session)
	rec = httptest.NewRecorder()
	restartedRouter.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("session after restart = %d, want 200", rec.Code)
	}

	// token 状态恢复：用始终注册的 /api/v1/api-tokens（admin 端点）
	// 区分——撤销的 token 认证失败 401；未撤销的通过认证、因 scope
	// 不足（read < admin）403。
	for name, raw := range map[string]string{"revoked": revokedRaw, "live": liveRaw} {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/api-tokens", nil)
		req.Header.Set("Authorization", "Bearer "+raw)
		rec := httptest.NewRecorder()
		restartedRouter.ServeHTTP(rec, req)
		if name == "revoked" && rec.Code != http.StatusUnauthorized {
			t.Errorf("revoked token after restart = %d, want 401", rec.Code)
		}
		if name == "live" && rec.Code != http.StatusForbidden {
			t.Errorf("live token after restart = %d, want 403 (authenticated, scope limited)", rec.Code)
		}
	}
}

// 受保护路由 scope 矩阵：对全部 /api/v1 受保护 method/path 逐一断言
// 匿名 401、read / run / admin 的 403 边界与 Web-Session-only 路由对
// Bearer 的拒绝；矩阵与 Gin 注册表双向核对——新受保护路由漏配 scope
// 或平行路由（如 GET ↔ HEAD）漂移时会直接失败。
func TestAuthE2EProtectedScopeMatrix(t *testing.T) {
	remote := protocolFixtures()[0].fixture(t)
	e := newBrowserEnv(t, remote)
	e.createJob(syncjob.ModeCopy)

	engine, ok := e.router.(*gin.Engine)
	if !ok {
		t.Fatal("e2e router is not *gin.Engine")
	}

	// matrixRoute 描述一个受保护端点：scope 为 "" 表示仅接受 Web
	// Session（auth/session|logout，Bearer 一律 401）。
	type matrixRoute struct {
		method string
		path   string // 注册模板，:id 以不存在的 ID 发请求
		scope  auth.Scope
		body   string
	}
	const missingID = "matrix_missing"
	routes := []matrixRoute{
		{http.MethodGet, "/api/v1/sources", auth.ScopeRead, ""},
		{http.MethodPost, "/api/v1/sources", auth.ScopeAdmin, "{}"},
		{http.MethodGet, "/api/v1/sources/:id", auth.ScopeRead, ""},
		{http.MethodPatch, "/api/v1/sources/:id", auth.ScopeAdmin, "{}"},
		{http.MethodDelete, "/api/v1/sources/:id", auth.ScopeAdmin, ""},
		{http.MethodPost, "/api/v1/sources/:id/test", auth.ScopeAdmin, "{}"},
		{http.MethodGet, "/api/v1/sources/:id/files", auth.ScopeRead, ""},
		{http.MethodGet, "/api/v1/sources/:id/files/stat", auth.ScopeRead, ""},
		{http.MethodGet, "/api/v1/sources/:id/files/download", auth.ScopeRead, ""},
		{http.MethodPost, "/api/v1/sources/:id/directories", auth.ScopeAdmin, "{}"},
		{http.MethodGet, "/api/v1/jobs", auth.ScopeRead, ""},
		{http.MethodGet, "/api/v1/jobs/local-directories", auth.ScopeAdmin, ""},
		{http.MethodPost, "/api/v1/jobs/local-directories", auth.ScopeAdmin, "{}"},
		{http.MethodPost, "/api/v1/jobs", auth.ScopeAdmin, "{}"},
		{http.MethodGet, "/api/v1/jobs/:id", auth.ScopeRead, ""},
		{http.MethodPatch, "/api/v1/jobs/:id", auth.ScopeAdmin, "{}"},
		{http.MethodDelete, "/api/v1/jobs/:id", auth.ScopeAdmin, ""},
		{http.MethodPost, "/api/v1/jobs/:id/run", auth.ScopeRun, ""},
		{http.MethodGet, "/api/v1/jobs/:id/status", auth.ScopeRead, ""},
		{http.MethodGet, "/api/v1/runs", auth.ScopeRead, ""},
		{http.MethodGet, "/api/v1/runs/:id", auth.ScopeRead, ""},
		{http.MethodGet, "/api/v1/runs/:id/items", auth.ScopeRead, ""},
		{http.MethodGet, "/api/v1/jobs/:id/files", auth.ScopeRead, ""},
		{http.MethodGet, "/api/v1/jobs/:id/files/stat", auth.ScopeRead, ""},
		{http.MethodGet, "/api/v1/jobs/:id/files/download", auth.ScopeRead, ""},
		// HEAD 与 GET download 同为 read：响应头即文件元信息。
		{http.MethodHead, "/api/v1/jobs/:id/files/download", auth.ScopeRead, ""},
		{http.MethodGet, "/api/v1/published-files", auth.ScopeRead, ""},
		{http.MethodPost, "/api/v1/published-files", auth.ScopeAdmin, "{}"},
		{http.MethodPatch, "/api/v1/published-files/:id", auth.ScopeAdmin, "{}"},
		{http.MethodDelete, "/api/v1/published-files/:id", auth.ScopeAdmin, ""},
		{http.MethodGet, "/api/v1/api-tokens", auth.ScopeAdmin, ""},
		{http.MethodPost, "/api/v1/api-tokens", auth.ScopeAdmin, "{}"},
		{http.MethodPost, "/api/v1/api-tokens/:id/revoke", auth.ScopeAdmin, ""},
		{http.MethodGet, "/api/v1/auth/session", "", ""},
		{http.MethodPost, "/api/v1/auth/logout", "", ""},
		{http.MethodGet, "/api/v1/auth/profile", "", ""},
		{http.MethodPatch, "/api/v1/auth/profile", "", "{}"},
	}

	// 双向核对 1：矩阵每行都必须已注册（矩阵本身不可漂移）。
	registered := map[string]bool{}
	for _, r := range engine.Routes() {
		if strings.HasPrefix(r.Path, "/api/v1/") {
			registered[r.Method+" "+r.Path] = true
		}
	}
	publicRoutes := map[string]bool{
		http.MethodGet + " /api/v1/health":      true,
		http.MethodGet + " /api/v1/version":     true,
		http.MethodPost + " /api/v1/auth/login": true,
	}
	matrixKeys := map[string]bool{}
	for _, row := range routes {
		key := row.method + " " + row.path
		matrixKeys[key] = true
		if !registered[key] {
			t.Errorf("matrix row %s is not registered", key)
		}
	}
	// 双向核对 2：每个 /api/v1 注册路由必须进矩阵或显式公开——
	// 新增受保护端点必须同时声明 scope，防止 GET/HEAD 平行漂移。
	for key := range registered {
		if publicRoutes[key] || matrixKeys[key] {
			continue
		}
		t.Errorf("registered route %s missing from scope matrix (declare required scope)", key)
	}

	createToken := func(t *testing.T, name, scopes string) string {
		t.Helper()
		rec := e.doJSON(http.MethodPost, "/api/v1/api-tokens",
			`{"name": "`+name+`", "scopes": `+scopes+`}`)
		if rec.Code != http.StatusCreated {
			t.Fatalf("create token %s = %d, body = %s", name, rec.Code, rec.Body.String())
		}
		var body struct {
			RawToken string `json:"raw_token"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode create token: %v", err)
		}
		return body.RawToken
	}
	readRaw := createToken(t, "matrix-reader", `["read"]`)
	runRaw := createToken(t, "matrix-runner", `["run"]`)
	adminRaw := createToken(t, "matrix-chief", `["admin"]`)

	type principal struct {
		name   string
		rawTok string
	}
	principals := []principal{
		{"anonymous", ""},
		{"read", readRaw},
		{"run", runRaw},
		{"admin", adminRaw},
	}

	for _, row := range routes {
		requestPath := strings.ReplaceAll(row.path, ":id", missingID)
		for _, p := range principals {
			headers := map[string]string{}
			if p.rawTok != "" {
				headers["Authorization"] = "Bearer " + p.rawTok
			}
			rec := doRaw(t, e.router, row.method, requestPath, row.body, headers)

			switch {
			case p.rawTok == "":
				// 匿名访问受保护端点一律 401。
				if rec.Code != http.StatusUnauthorized {
					t.Errorf("%s %s as anonymous = %d, want 401", row.method, requestPath, rec.Code)
				}
			case row.scope == "":
				// Web-Session-only 路由拒绝一切 Bearer。
				if rec.Code != http.StatusUnauthorized {
					t.Errorf("%s %s as %s bearer = %d, want 401", row.method, requestPath, p.name, rec.Code)
				}
			case p.name == "admin" || p.name == string(row.scope):
				// scope 匹配（admin 蕴含一切）：到达 handler，任何
				// handler 产生状态码都可，唯独不允许认证/授权拦截。
				if rec.Code == http.StatusUnauthorized || rec.Code == http.StatusForbidden {
					t.Errorf("%s %s as %s = %d, want handler reached (not 401/403)", row.method, requestPath, p.name, rec.Code)
				}
			default:
				// scope 不匹配：403。
				if rec.Code != http.StatusForbidden {
					t.Errorf("%s %s as %s = %d, want 403", row.method, requestPath, p.name, rec.Code)
				}
			}
		}
	}
}

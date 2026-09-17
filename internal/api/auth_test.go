package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"tinysync/internal/auth"
	authsqlite "tinysync/internal/auth/sqlite"
	"tinysync/internal/source"
	"tinysync/internal/source/sqlite"
	"tinysync/internal/storage"
)

// authTestEnv 是认证 API 测试环境：router（自动带 session cookie）、
// bare（不带凭据的原始引擎）与认证服务。
type authTestEnv struct {
	router  testRouter
	bare    *gin.Engine
	authSvc *auth.Service
	db      *sql.DB
}

// doBare 用原始引擎发送请求（不带任何凭据）。
func (e *authTestEnv) doBare(method, path, body string) *httptest.ResponseRecorder {
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, reader)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	e.bare.ServeHTTP(rec, req)
	return rec
}

// newAuthTestEnv 构造认证测试环境。
func newAuthTestEnv(t *testing.T) *authTestEnv {
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
	svc := auth.NewService(authsqlite.NewRepository(db))
	ctx := context.Background()
	if err := svc.SetAdminPassword(ctx, testAdminPassword); err != nil {
		t.Fatalf("set admin password: %v", err)
	}
	// Sources 服务用于让业务端点真正注册（否则 404 语义属于路由层
	// 而非认证层）；fakeFactory 不会被实际拨号。
	deps := Dependencies{
		Auth:    svc,
		Sources: source.NewService(sqlite.New(db), fakeFactory{}),
	}
	engine := NewRouter(testWebFS(), deps)
	router := testRouter{Engine: engine}
	_, raw, err := svc.Login(ctx, testAdminPassword)
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	router.session = &http.Cookie{Name: sessionCookieName, Value: raw}
	return &authTestEnv{router: router, bare: engine, authSvc: svc, db: db}
}

// 未认证客户端访问受保护 API 一律 401；公开端点与 SPA 保持 200。
func TestUnauthenticatedAccessDenied(t *testing.T) {
	env := newAuthTestEnv(t)

	for _, path := range []string{
		"/api/v1/sources",
		"/api/v1/sources/src_x",
		"/api/v1/auth/session",
	} {
		if rec := env.doBare(http.MethodGet, path, ""); rec.Code != http.StatusUnauthorized {
			t.Errorf("GET %s without auth = %d, want 401", path, rec.Code)
		}
	}
	// state-changing 未认证同样 401。
	if rec := env.doBare(http.MethodPost, "/api/v1/sources", "{}"); rec.Code != http.StatusUnauthorized {
		t.Errorf("POST /api/v1/sources without auth = %d, want 401", rec.Code)
	}
	// 公开端点保持公开。
	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/health"},
		{http.MethodGet, "/api/v1/version"},
		{http.MethodGet, "/"},
		{http.MethodGet, "/api/v1/auth/login"}, // 路径存在性：GET 405 而非 404
	} {
		rec := env.doBare(tc.method, tc.path, "")
		if rec.Code == http.StatusUnauthorized {
			t.Errorf("%s %s should not require auth, got 401", tc.method, tc.path)
		}
	}
}

// 登录成功：200 + Set-Cookie（HttpOnly / SameSite=Strict / Path=/）+
// expires_at；凭据错误统一 401。
func TestLoginEndpoint(t *testing.T) {
	env := newAuthTestEnv(t)

	rec := env.doBare(http.MethodPost, "/api/v1/auth/login",
		`{"password": "`+testAdminPassword+`"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("login status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if cache := rec.Header().Get("Cache-Control"); cache != "no-store" {
		t.Errorf("login cache-control = %q, want no-store", cache)
	}
	var body struct {
		ExpiresAt string `json:"expires_at"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode login body: %v", err)
	}
	expiresAt, err := time.Parse(time.RFC3339, body.ExpiresAt)
	if err != nil {
		t.Fatalf("expires_at not RFC3339: %v", err)
	}
	if d := time.Until(expiresAt); d <= 0 || d > auth.SessionTTL+time.Minute {
		t.Errorf("expires_at = %v, want about %v ahead", expiresAt, auth.SessionTTL)
	}
	cookie := rec.Header().Get("Set-Cookie")
	for _, want := range []string{
		sessionCookieName + "=",
		"HttpOnly",
		"SameSite=Strict",
		"Path=/",
	} {
		if !strings.Contains(cookie, want) {
			t.Errorf("Set-Cookie missing %q: %s", want, cookie)
		}
	}
	if strings.Contains(cookie, "Secure") {
		t.Errorf("plain-HTTP login must not set Secure: %s", cookie)
	}
	if strings.Contains(rec.Body.String(), sessionCookieName) || len(rec.Body.String()) > 200 {
		t.Errorf("login response must not carry credentials: %s", rec.Body.String())
	}

	// 密码错误：统一 401 invalid credentials。
	rec = env.doBare(http.MethodPost, "/api/v1/auth/login", `{"password": "wrong-password-1"}`)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong password status = %d, want 401", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "invalid credentials") {
		t.Errorf("wrong password body = %s, want unified error", rec.Body.String())
	}

	// 空密码与错误密码同形 401（统一凭据错误语义）。
	if rec := env.doBare(http.MethodPost, "/api/v1/auth/login", `{}`); rec.Code != http.StatusUnauthorized {
		t.Errorf("empty password status = %d, want 401", rec.Code)
	}
	// 请求体经严格解码：非法 JSON 400。
	if rec := env.doBare(http.MethodPost, "/api/v1/auth/login", `not-json`); rec.Code != http.StatusBadRequest {
		t.Errorf("malformed body status = %d, want 400 (strict bind)", rec.Code)
	}
}

// 登录入口两级输入限制：超限请求体 400；超过 1024 字节的密码在
// 服务层拒绝，统一 401 invalid credentials，不暴露策略细节。
func TestLoginInputLimits(t *testing.T) {
	env := newAuthTestEnv(t)

	// 请求体超过 4 KiB：HTTP 层拒绝，与非法 JSON 同形 400。
	huge := `{"password": "` + strings.Repeat("x", 5000) + `"}`
	if rec := env.doBare(http.MethodPost, "/api/v1/auth/login", huge); rec.Code != http.StatusBadRequest {
		t.Errorf("oversized login body status = %d, want 400", rec.Code)
	}

	// 密码超 1024 字节：统一 401 invalid credentials。
	tooLong := `{"password": "` + strings.Repeat("x", 1025) + `"}`
	rec := env.doBare(http.MethodPost, "/api/v1/auth/login", tooLong)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("oversized password status = %d, want 401", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "invalid credentials") {
		t.Errorf("oversized password body = %s, want unified error", rec.Body.String())
	}

	// 登录端点公开匿名，超大输入不得触发 Argon2id：语义上以统一
	// 401 收敛即可，不做策略区分。
}

// 登录端点同样执行 Origin 校验。
func TestLoginOriginValidation(t *testing.T) {
	env := newAuthTestEnv(t)
	body := `{"password": "` + testAdminPassword + `"}`

	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "http://evil.example.com")
	rec := httptest.NewRecorder()
	env.bare.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("cross-origin login = %d, want 403", rec.Code)
	}

	req = httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "http://example.com") // httptest 默认 Host
	rec = httptest.NewRecorder()
	env.bare.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("same-origin login = %d, want 200", rec.Code)
	}
}

// 会话查询与登出：logout 后会话立即失效。
func TestSessionQueryAndLogout(t *testing.T) {
	env := newAuthTestEnv(t)

	rec := doJSON(t, env.router, http.MethodGet, "/api/v1/auth/session", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("session status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Authenticated bool   `json:"authenticated"`
		Subject       string `json:"subject"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode session body: %v", err)
	}
	if !body.Authenticated || body.Subject != auth.AdminSubject {
		t.Errorf("session body = %+v, want authenticated admin", body)
	}

	// logout 返回 204 并清除 cookie。
	rec = doJSON(t, env.router, http.MethodPost, "/api/v1/auth/logout", "")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("logout status = %d, want 204", rec.Code)
	}
	setCookie := rec.Header().Get("Set-Cookie")
	if !strings.Contains(setCookie, "Max-Age=0") {
		t.Errorf("logout Set-Cookie = %q, want expired cookie", setCookie)
	}
	// 同一 cookie 再次访问 → 401。
	rec = doJSON(t, env.router, http.MethodGet, "/api/v1/auth/session", "")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("session after logout = %d, want 401", rec.Code)
	}
}

// 过期会话 401 且被删除；不存在 / 伪造 token 同形 401。
func TestExpiredSessionRejected(t *testing.T) {
	env := newAuthTestEnv(t)
	ctx := context.Background()

	// 直接向数据库写入一个已过期会话。
	repo := authsqlite.NewRepository(env.db)
	past := time.Now().UTC().Add(-time.Hour)
	expired := auth.WebSession{ID: "ses_expired", CreatedAt: past.Add(-time.Minute), ExpiresAt: past}
	if err := repo.CreateSession(ctx, expired, auth.HashSessionToken("expired-raw")); err != nil {
		t.Fatalf("seed expired session: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/session", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "expired-raw"})
	rec := httptest.NewRecorder()
	env.bare.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expired session = %d, want 401", rec.Code)
	}

	// 会话已被删除：再次使用同 token 仍 401（非恢复）。
	if _, err := repo.GetSessionByHash(ctx, auth.HashSessionToken("expired-raw")); err == nil {
		t.Error("expired session not purged after authentication attempt")
	}

	// 伪造 token 同形 401。
	req = httptest.NewRequest(http.MethodGet, "/api/v1/auth/session", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "forged"})
	rec = httptest.NewRecorder()
	env.bare.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("forged session = %d, want 401", rec.Code)
	}
}

// 显式提供无效 Bearer Token 时绝不 fallback 到有效 Web Session。
func TestInvalidBearerNoFallback(t *testing.T) {
	env := newAuthTestEnv(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/sources", nil)
	req.AddCookie(env.router.session)
	req.Header.Set("Authorization", "Bearer ts_invalid")
	rec := httptest.NewRecorder()
	env.bare.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("invalid bearer with valid cookie = %d, want 401 (no fallback)", rec.Code)
	}

	// 非 Bearer scheme 同样 401。
	req = httptest.NewRequest(http.MethodGet, "/api/v1/sources", nil)
	req.AddCookie(env.router.session)
	req.Header.Set("Authorization", "Basic dXNlcjpwYXNz")
	rec = httptest.NewRecorder()
	env.bare.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("basic auth with valid cookie = %d, want 401", rec.Code)
	}
}

// URL 携带凭据参数一律 400：认证信息不放 URL。
func TestCredentialInQueryRejected(t *testing.T) {
	env := newAuthTestEnv(t)
	for _, key := range []string{"token", "api_key", "access_token"} {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/session?"+key+"=ts_x", nil)
		req.AddCookie(env.router.session)
		rec := httptest.NewRecorder()
		env.bare.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("query %s = %d, want 400", key, rec.Code)
		}
	}
}

// 跨源 state-changing 请求（携带会话）被 CSRF 校验拒绝。
func TestCrossOriginMutationRejected(t *testing.T) {
	env := newAuthTestEnv(t)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/logout", nil)
	req.AddCookie(env.router.session)
	req.Header.Set("Origin", "http://evil.example.com")
	rec := httptest.NewRecorder()
	env.bare.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("cross-origin mutation = %d, want 403", rec.Code)
	}

	// 同源 mutation 放行。
	req = httptest.NewRequest(http.MethodPost, "/api/v1/auth/logout", nil)
	req.AddCookie(env.router.session)
	req.Header.Set("Origin", "http://example.com")
	rec = httptest.NewRecorder()
	env.bare.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("same-origin mutation = %d, want 204: %s", rec.Code, rec.Body.String())
	}
}

// HTTPS 到达（反代头）时登录 cookie 带 Secure。
func TestSessionCookieSecureOverHTTPS(t *testing.T) {
	env := newAuthTestEnv(t)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login",
		strings.NewReader(`{"password": "`+testAdminPassword+`"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Forwarded-Proto", "https")
	rec := httptest.NewRecorder()
	env.bare.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("login over https proxy = %d", rec.Code)
	}
	if cookie := rec.Header().Get("Set-Cookie"); !strings.Contains(cookie, "Secure") {
		t.Errorf("Set-Cookie over https = %q, want Secure", cookie)
	}
}

// Auth 依赖缺失必须 fail closed：受保护端点 500、登录 500，
// 绝不退化为匿名管理 API。
func TestAuthDependencyFailClosed(t *testing.T) {
	engine := NewRouter(testWebFS(), Dependencies{})
	do := func(method, path, body string) *httptest.ResponseRecorder {
		var reader io.Reader
		if body != "" {
			reader = strings.NewReader(body)
		}
		req := httptest.NewRequest(method, path, reader)
		rec := httptest.NewRecorder()
		engine.ServeHTTP(rec, req)
		return rec
	}
	if rec := do(http.MethodGet, "/api/v1/auth/session", ""); rec.Code != http.StatusInternalServerError {
		t.Errorf("protected endpoint with nil auth = %d, want 500", rec.Code)
	}
	if rec := do(http.MethodPost, "/api/v1/auth/login", `{"password":"x"}`); rec.Code != http.StatusInternalServerError {
		t.Errorf("login with nil auth = %d, want 500", rec.Code)
	}
}

package api

import (
	"context"
	"database/sql"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"tinysync/internal/auth"
	authsqlite "tinysync/internal/auth/sqlite"
)

// testAdminPassword 是测试环境的管理员密码（满足最小策略）。
const testAdminPassword = "test-admin-password"

// testRouter 携带认证后的 router 与 session cookie：嵌入 *gin.Engine
// 保持 ServeHTTP 用法不变，覆写自动附带 cookie，受保护端点即登录态。
// session 为 nil 时保持未认证（用于 401 语义测试）。
type testRouter struct {
	*gin.Engine
	session *http.Cookie
}

func (r testRouter) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	if r.session != nil {
		req.AddCookie(r.session)
	}
	r.Engine.ServeHTTP(w, req)
}

// newTestAuth 为 deps 装配认证服务：设置管理员密码并登录，返回携带
// session cookie 的路由包装。
func newTestAuth(t *testing.T, db *sql.DB, deps Dependencies) testRouter {
	t.Helper()
	svc := auth.NewService(authsqlite.NewRepository(db))
	ctx := context.Background()
	if err := svc.SetAdminPassword(ctx, testAdminPassword); err != nil {
		t.Fatalf("set admin password: %v", err)
	}
	deps.Auth = svc
	router := NewRouter(testWebFS(), deps)
	_, raw, err := svc.Login(ctx, testAdminPassword)
	if err != nil {
		t.Fatalf("login test admin: %v", err)
	}
	return testRouter{Engine: router, session: &http.Cookie{Name: sessionCookieName, Value: raw}}
}

// doJSON 执行 JSON 请求并返回 recorder（cookie 由 testRouter 注入）。
func doJSON(t *testing.T, router testRouter, method, path string, body string) *httptest.ResponseRecorder {
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

package mcp

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"tinysync/internal/auth"
	authsqlite "tinysync/internal/auth/sqlite"
	"tinysync/internal/storage"
)

// transport 认证契约：/mcp 只接受有效 Bearer API Token；匿名、无效、
// 已撤销、已过期与 Web Session cookie 一律 401。协议版本与请求体上限
// 在同一传输层锁定。

// mcpEnv 是 transport 测试环境：真实 SQLite + 真实 auth.Service。
type mcpEnv struct {
	t      *testing.T
	server *httptest.Server
	svc    *auth.Service
}

func newMCPEnv(t *testing.T) *mcpEnv {
	t.Helper()
	dataDir := t.TempDir()
	db, err := storage.Open(dataDir)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := storage.Migrate(context.Background(), db, dataDir); err != nil {
		t.Fatalf("migrate database: %v", err)
	}
	svc := auth.NewService(authsqlite.NewRepository(db))
	if err := svc.SetAdminPassword(context.Background(), "mcp-admin-password"); err != nil {
		t.Fatalf("set admin password: %v", err)
	}
	handler := New(Deps{Auth: svc})
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return &mcpEnv{t: t, server: server, svc: svc}
}

// createToken 用真实服务创建 API Token，返回 ID 与 raw（raw 仅此一次
// 可见）。
func (e *mcpEnv) createToken(name string, scopes []auth.Scope, expires *time.Time) (string, string) {
	e.t.Helper()
	token, raw, err := e.svc.CreateAPIToken(context.Background(), auth.CreateAPITokenInput{
		Name:      name,
		Scopes:    scopes,
		ExpiresAt: expires,
	})
	if err != nil {
		e.t.Fatalf("create api token %s: %v", name, err)
	}
	return token.ID, raw
}

// postMCP 向 /mcp 发送 JSON-RPC POST，返回响应（body 由调用方关闭）。
func (e *mcpEnv) postMCP(headers map[string]string, body string) *http.Response {
	e.t.Helper()
	req, err := http.NewRequest(http.MethodPost, e.server.URL+"/mcp", strings.NewReader(body))
	if err != nil {
		e.t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := e.server.Client().Do(req)
	if err != nil {
		e.t.Fatalf("POST /mcp: %v", err)
	}
	e.t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

// initializeRequest 是新协议 initialize 的 JSON-RPC 请求体。
const initializeRequest = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"` + ProtocolVersion + `","capabilities":{},"clientInfo":{"name":"mcp-server-test","version":"0"}}}`

// createExpiredToken 在推进服务时钟后返回一个已过期的 raw token：
// 创建时刻合法（过期时刻在未来），验证时刻已过过期线。
func createExpiredToken(t *testing.T, svc *auth.Service) string {
	t.Helper()
	base := svc.Now
	now := base().UTC()
	svc.Now = func() time.Time { return now }
	expires := now.Add(time.Hour)
	_, raw, err := svc.CreateAPIToken(context.Background(), auth.CreateAPITokenInput{
		Name:      "expired",
		Scopes:    []auth.Scope{auth.ScopeRead},
		ExpiresAt: &expires,
	})
	if err != nil {
		t.Fatalf("create expired api token: %v", err)
	}
	svc.Now = func() time.Time { return now.Add(2 * time.Hour) }
	return raw
}

func TestMCPTransportAuthentication(t *testing.T) {
	e := newMCPEnv(t)
	revokedID, revokedRaw := e.createToken("revoked", []auth.Scope{auth.ScopeRead}, nil)
	if err := e.svc.RevokeAPIToken(context.Background(), revokedID); err != nil {
		t.Fatalf("revoke token: %v", err)
	}

	t.Run("no auth is unauthorized", func(t *testing.T) {
		resp := e.postMCP(nil, initializeRequest)
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401", resp.StatusCode)
		}
	})
	t.Run("invalid token is unauthorized", func(t *testing.T) {
		resp := e.postMCP(map[string]string{"Authorization": "Bearer ts_invalid"}, initializeRequest)
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401", resp.StatusCode)
		}
	})
	t.Run("revoked token is unauthorized", func(t *testing.T) {
		resp := e.postMCP(map[string]string{"Authorization": "Bearer " + revokedRaw}, initializeRequest)
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401", resp.StatusCode)
		}
	})
	t.Run("expired token is unauthorized", func(t *testing.T) {
		expiredRaw := createExpiredToken(t, e.svc)
		resp := e.postMCP(map[string]string{"Authorization": "Bearer " + expiredRaw}, initializeRequest)
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401", resp.StatusCode)
		}
	})
	t.Run("web session cookie only is unauthorized", func(t *testing.T) {
		_, rawSession, err := e.svc.Login(context.Background(), "mcp-admin-password")
		if err != nil {
			t.Fatalf("login: %v", err)
		}
		resp := e.postMCP(map[string]string{"Cookie": "tinysync_session=" + rawSession}, initializeRequest)
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401", resp.StatusCode)
		}
	})
	t.Run("non bearer authorization is unauthorized", func(t *testing.T) {
		_, raw := e.createToken("basic", []auth.Scope{auth.ScopeRead}, nil)
		resp := e.postMCP(map[string]string{"Authorization": "Basic " + raw}, initializeRequest)
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401", resp.StatusCode)
		}
	})
	t.Run("valid api token is accepted", func(t *testing.T) {
		_, valid := e.createToken("valid", []auth.Scope{auth.ScopeRead, auth.ScopeRun}, nil)
		resp := e.postMCP(map[string]string{"Authorization": "Bearer " + valid}, initializeRequest)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200", resp.StatusCode)
		}
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		if !strings.Contains(string(body), `"result"`) {
			t.Fatalf("initialize response missing JSON-RPC result, body = %s", body)
		}
	})
}

func TestMCPTransportUnsupportedProtocol(t *testing.T) {
	e := newMCPEnv(t)
	_, raw := e.createToken("read", []auth.Scope{auth.ScopeRead}, nil)
	old := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"mcp-server-test","version":"0"}}}`
	resp := e.postMCP(map[string]string{"Authorization": "Bearer " + raw}, old)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (error is reported in JSON-RPC body)", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if strings.Contains(string(body), `"2025-06-18"`) {
		t.Fatalf("server must not negotiate old protocol version, body = %s", body)
	}
}

func TestMCPTransportRequestTooLarge(t *testing.T) {
	e := newMCPEnv(t)
	_, raw := e.createToken("read", []auth.Scope{auth.ScopeRead}, nil)
	// 请求体超过 1 MiB 上限：与 initialize 无关的任意 JSON 大负载。
	huge := `{"jsonrpc":"2.0","id":1,"method":"ping","params":{"pad":"` + strings.Repeat("a", MaxRequestBodyBytes) + `"}}`
	resp := e.postMCP(map[string]string{"Authorization": "Bearer " + raw}, huge)
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", resp.StatusCode)
	}
}

func TestMCPTransportGetNotAllowed(t *testing.T) {
	e := newMCPEnv(t)
	_, raw := e.createToken("read", []auth.Scope{auth.ScopeRead}, nil)
	req, err := http.NewRequest(http.MethodGet, e.server.URL+"/mcp", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+raw)
	resp, err := e.server.Client().Do(req)
	if err != nil {
		t.Fatalf("GET /mcp: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", resp.StatusCode)
	}
}

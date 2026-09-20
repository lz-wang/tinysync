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

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"tinysync/internal/api"
	"tinysync/internal/auth"
	authsqlite "tinysync/internal/auth/sqlite"
	"tinysync/internal/browser"
	"tinysync/internal/mcp"
	"tinysync/internal/share"
	sharesqlite "tinysync/internal/share/sqlite"
)

// MCP 集成端到端：完整 HTTP 栈（Gin router + /mcp + 真实 SQLite +
// 真实 Runner + 真实 WebDAV 远端）+ 官方 MCP client，验证 v0.8 契约——
// 认证矩阵、per-tool 授权、run 生命周期、文件发现、resource 读取与
// 大文件 HTTP 下载。

// mcpE2E 是 MCP E2E 环境：在真实 env（dav + SQLite + services +
// Runner）之上装配完整 router 与认证。
type mcpE2E struct {
	*env
	router      http.Handler
	authService *auth.Service
	session     *http.Cookie
	jobID       string
}

func newMCPE2E(t *testing.T) *mcpE2E {
	t.Helper()
	e := newEnv(t)
	authService := auth.NewService(authsqlite.NewRepository(e.db))
	if err := authService.SetAdminPassword(context.Background(), e2eAdminPassword); err != nil {
		t.Fatalf("set admin password: %v", err)
	}
	_, rawSession, err := authService.Login(context.Background(), e2eAdminPassword)
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	localFiles := browser.NewLocalService(e.jobs, e.managedRepo)
	shares := share.NewService(sharesqlite.NewRepository(e.db), e.jobs)
	router := api.NewRouter(fstest.MapFS{}, api.Dependencies{
		Auth:       authService,
		Sources:    e.sources,
		Jobs:       e.jobs,
		Runner:     e.runner,
		LocalFiles: localFiles,
		Share:      shares,
		MCP:        mcp.New(mcp.Deps{Auth: authService, Sources: e.sources, Jobs: e.jobs, Runner: e.runner, LocalFiles: localFiles}),
	})
	return &mcpE2E{
		env:         e,
		router:      router,
		authService: authService,
		session:     &http.Cookie{Name: "tinysync_session", Value: rawSession},
	}
}

// mcpToken 是一种 scope 组合的 API Token。
type mcpToken struct {
	name string
	raw  string
}

// createToken 经真实 auth 服务创建 API Token。
func (m *mcpE2E) createToken(t *testing.T, name string, scopes []auth.Scope) mcpToken {
	t.Helper()
	_, raw, err := m.authService.CreateAPIToken(context.Background(), auth.CreateAPITokenInput{Name: name, Scopes: scopes})
	if err != nil {
		t.Fatalf("create token %s: %v", name, err)
	}
	return mcpToken{name: name, raw: raw}
}

// postMCP 直接对 router 发送 JSON-RPC POST（transport 矩阵用）。
func postMCP(t *testing.T, router http.Handler, headers map[string]string, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

// connectClient 以给定 token 建立官方 MCP client 会话（进程内
// transport，全部请求直达 router）。
func connectClient(t *testing.T, router http.Handler, raw string) *mcpsdk.ClientSession {
	t.Helper()
	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "mcp-e2e", Version: "0"}, nil)
	var base http.RoundTripper = routerTransport{router: router}
	if raw != "" {
		base = bearerRoundTripper{base: base, token: raw}
	}
	transport := &mcpsdk.StreamableClientTransport{
		Endpoint:   "http://e2e.invalid/mcp",
		HTTPClient: &http.Client{Transport: base},
		MaxRetries: -1,
	}
	session, err := client.Connect(context.Background(), transport, nil)
	if err != nil {
		t.Fatalf("mcp connect: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

// routerTransport 把 HTTP 请求直接交给 router。
type routerTransport struct {
	router http.Handler
}

func (r routerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	rec := httptest.NewRecorder()
	r.router.ServeHTTP(rec, req)
	return rec.Result(), nil
}

type bearerRoundTripper struct {
	base  http.RoundTripper
	token string
}

func (b bearerRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.Header.Set("Authorization", "Bearer "+b.token)
	return b.base.RoundTrip(req)
}

// authorizedJSON 经 router 执行带 cookie 的 JSON 请求，返回响应体
// （创建类端点 201，其余 200）。
func authorizedJSON(t *testing.T, m *mcpE2E, method, path, body string) string {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(m.session)
	rec := httptest.NewRecorder()
	m.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK && rec.Code != http.StatusCreated {
		t.Fatalf("%s %s status = %d body = %s", method, path, rec.Code, rec.Body.String())
	}
	return rec.Body.String()
}

// setupSourceAndJob 经 REST API（Web Session cookie）创建真实 Source
// 与 Job：证明 MCP 与 REST 共用同一应用服务层。
func (m *mcpE2E) setupSourceAndJob(t *testing.T) {
	t.Helper()
	sourceBody := authorizedJSON(t, m, http.MethodPost, "/api/v1/sources",
		`{"name":"MCP E2E WebDAV","type":"webdav","config":{"endpoint":"`+m.dav.srv.URL+`","username":"e2e"},"credentials":{"password":"e2e-dav-secret"}}`)
	sourceID := extractJSONField(t, sourceBody, "id")

	localRoot := t.TempDir()
	jobBody := authorizedJSON(t, m, http.MethodPost, "/api/v1/jobs",
		`{"name":"mcp e2e job","source_id":"`+sourceID+`","remote_root":"/","local_root":"`+localRoot+`","mode":"copy","enabled":true}`)
	m.jobID = extractJSONField(t, jobBody, "id")
}

// extractJSONField 提取 JSON 顶层字符串字段。
func extractJSONField(t *testing.T, body, field string) string {
	t.Helper()
	marker := `"` + field + `":"`
	idx := strings.Index(body, marker)
	if idx < 0 {
		t.Fatalf("field %q not found in %s", field, body)
	}
	rest := body[idx+len(marker):]
	end := strings.Index(rest, `"`)
	return rest[:end]
}

// mcpCall 调用工具：返回 structured content 原始 JSON、is_error 与
// client 错误。tool error 文案从 content 提取。
func mcpCall(t *testing.T, session *mcpsdk.ClientSession, name string, args map[string]any) (string, string, bool, error) {
	t.Helper()
	res, err := session.CallTool(context.Background(), &mcpsdk.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		return "", err.Error(), false, err
	}
	raw, _ := json.Marshal(res.StructuredContent)
	text := ""
	for _, c := range res.Content {
		if tc, ok := c.(*mcpsdk.TextContent); ok {
			text = tc.Text
		}
	}
	return string(raw), text, res.IsError, nil
}

// requireToolText 断言工具调用返回 is_error 并给出文案。
func requireToolText(t *testing.T, raw, text string, isErr bool, err error) string {
	t.Helper()
	if err != nil {
		return err.Error()
	}
	if !isErr {
		t.Fatalf("expected tool error, got success: %s", raw)
	}
	return text
}

func TestMCPE2EFullWorkflow(t *testing.T) {
	m := newMCPE2E(t)
	m.setupSourceAndJob(t)

	// 远端真实文件：一个 UTF-8 文本 + 一个 binary（MemFS 需先建目录）。
	for _, dir := range []string{"/notes", "/data"} {
		if err := m.dav.fs.Mkdir(context.Background(), dir, 0o755); err != nil {
			t.Fatalf("dav mkdir %s: %v", dir, err)
		}
	}
	m.dav.writeFile(t, "/notes/2026-report.txt", "quarterly report body")
	m.dav.writeFile(t, "/data/blob.bin", string([]byte{0x00, 0x01, 0xFF, 0xFE}))

	readToken := m.createToken(t, "e2e-read", []auth.Scope{auth.ScopeRead})
	runToken := m.createToken(t, "e2e-run", []auth.Scope{auth.ScopeRun})
	adminToken := m.createToken(t, "e2e-admin", []auth.Scope{auth.ScopeAdmin})

	t.Run("discovery via read token", func(t *testing.T) {
		session := connectClient(t, m.router, readToken.raw)
		res, err := session.ListTools(context.Background(), nil)
		if err != nil {
			t.Fatalf("list tools: %v", err)
		}
		if len(res.Tools) != 7 {
			t.Fatalf("tools = %d, want 7", len(res.Tools))
		}
		raw, text, isErr, err := mcpCall(t, session, "list_sources", map[string]any{})
		if err != nil || isErr {
			t.Fatalf("list_sources: err=%v isErr=%v text=%s raw=%s", err, isErr, text, raw)
		}
		// secret 不进入 MCP 返回。
		if strings.Contains(raw, "e2e-dav-secret") {
			t.Fatal("list_sources leaked credential secret")
		}
		raw, text, isErr, err = mcpCall(t, session, "list_jobs", map[string]any{})
		if err != nil || isErr || !strings.Contains(raw, m.jobID) {
			t.Fatalf("list_jobs: err=%v isErr=%v text=%s raw=%s want job %s", err, isErr, text, raw, m.jobID)
		}
	})

	t.Run("run token starts sync, run survives session close, read token polls", func(t *testing.T) {
		runSession := connectClient(t, m.router, runToken.raw)
		raw, text, isErr, err := mcpCall(t, runSession, "run_sync", map[string]any{"job_id": m.jobID})
		if err != nil || isErr {
			t.Fatalf("run_sync: err=%v isErr=%v text=%s raw=%s", err, isErr, text, raw)
		}
		runID := extractJSONField(t, raw, "run_id")
		// MCP HTTP 请求（与该 session 关联的连接）结束不取消已启动的
		// run：立即关闭 run session，run 仍应收敛。
		_ = runSession.Close()

		readSession := connectClient(t, m.router, readToken.raw)
		var state string
		deadline := time.Now().Add(30 * time.Second)
		for {
			raw, text, isErr, err := mcpCall(t, readSession, "get_sync_run", map[string]any{"run_id": runID})
			if err != nil || isErr {
				t.Fatalf("get_sync_run: err=%v isErr=%v text=%s raw=%s", err, isErr, text, raw)
			}
			state = extractJSONField(t, raw, "state")
			if state == "succeeded" || state == "failed" {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("run %s did not converge, state = %s", runID, state)
			}
			time.Sleep(50 * time.Millisecond)
		}
		if state != "succeeded" {
			t.Fatalf("state = %s, want succeeded", state)
		}
	})

	t.Run("search and file info via read token", func(t *testing.T) {
		session := connectClient(t, m.router, readToken.raw)
		raw, text, isErr, err := mcpCall(t, session, "search_files", map[string]any{"job_id": m.jobID, "query": "report"})
		if err != nil || isErr {
			t.Fatalf("search_files: err=%v isErr=%v text=%s raw=%s", err, isErr, text, raw)
		}
		if !strings.Contains(raw, "/notes/2026-report.txt") {
			t.Fatalf("search result missing synced file: %s", raw)
		}
		if strings.Contains(raw, readToken.raw) {
			t.Fatal("search result leaked raw token")
		}

		raw, text, isErr, err = mcpCall(t, session, "get_file_info", map[string]any{"job_id": m.jobID, "path": "/notes/2026-report.txt"})
		if err != nil || isErr {
			t.Fatalf("get_file_info: err=%v isErr=%v text=%s raw=%s", err, isErr, text, raw)
		}
		resourceURI := extractJSONField(t, raw, "resource_uri")
		if resourceURI != "tinysync://jobs/"+m.jobID+"/files/notes/2026-report.txt" {
			t.Fatalf("resource_uri = %q", resourceURI)
		}
		downloadURL := extractJSONField(t, raw, "download_url")
		if !strings.HasPrefix(downloadURL, "/api/v1/jobs/") {
			t.Fatalf("download_url = %q, want relative REST URL", downloadURL)
		}
	})

	t.Run("resource read via read token", func(t *testing.T) {
		session := connectClient(t, m.router, readToken.raw)
		res, err := session.ReadResource(context.Background(), &mcpsdk.ReadResourceParams{
			URI: "tinysync://jobs/" + m.jobID + "/files/notes/2026-report.txt",
		})
		if err != nil {
			t.Fatalf("read resource: %v", err)
		}
		if len(res.Contents) != 1 || res.Contents[0].Text != "quarterly report body" {
			t.Fatalf("contents = %+v", res.Contents)
		}
		// binary 经 MCP 读取被拒绝。
		_, err = session.ReadResource(context.Background(), &mcpsdk.ReadResourceParams{
			URI: "tinysync://jobs/" + m.jobID + "/files/data/blob.bin",
		})
		if err == nil || !strings.Contains(err.Error(), "UTF-8") {
			t.Fatalf("binary resource err = %v, want UTF-8 rejection", err)
		}
	})

	t.Run("large file downloads over HTTP with bearer and range", func(t *testing.T) {
		downloadPath := "/api/v1/jobs/" + m.jobID + "/files/download?path=%2Fnotes%2F2026-report.txt"
		req := httptest.NewRequest(http.MethodGet, downloadPath, nil)
		req.Header.Set("Authorization", "Bearer "+readToken.raw)
		rec := httptest.NewRecorder()
		m.router.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("download status = %d", rec.Code)
		}
		if got := rec.Body.String(); got != "quarterly report body" {
			t.Fatalf("body = %q", got)
		}

		// Range 请求 → 206 部分内容。
		req = httptest.NewRequest(http.MethodGet, downloadPath, nil)
		req.Header.Set("Authorization", "Bearer "+readToken.raw)
		req.Header.Set("Range", "bytes=0-8")
		rec = httptest.NewRecorder()
		m.router.ServeHTTP(rec, req)
		if rec.Code != http.StatusPartialContent {
			t.Fatalf("range status = %d, want 206", rec.Code)
		}
		if got := rec.Body.String(); got != "quarterly" {
			t.Fatalf("range body = %q, want quarterly", got)
		}

		// 匿名下载被拒绝：download_url 不是匿名临时链接。
		req = httptest.NewRequest(http.MethodGet, downloadPath, nil)
		rec = httptest.NewRecorder()
		m.router.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("anonymous download status = %d, want 401", rec.Code)
		}
	})

	t.Run("admin reaches run tools", func(t *testing.T) {
		session := connectClient(t, m.router, adminToken.raw)
		_, text, isErr, err := mcpCall(t, session, "get_job", map[string]any{"job_id": m.jobID})
		if err != nil || isErr {
			t.Fatalf("admin get_job: err=%v isErr=%v text=%s", err, isErr, text)
		}
	})
}

func TestMCPE2ESecurityMatrix(t *testing.T) {
	m := newMCPE2E(t)
	initialize := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"` + mcp.ProtocolVersion + `","capabilities":{},"clientInfo":{"name":"e2e","version":"0"}}}`

	t.Run("transport rejects all non-bearer credentials", func(t *testing.T) {
		cases := []struct {
			name    string
			headers map[string]string
		}{
			{"anonymous", nil},
			{"cookie only", map[string]string{"Cookie": m.session.Name + "=" + m.session.Value}},
			{"invalid bearer", map[string]string{"Authorization": "Bearer ts_invalid"}},
			{"non-bearer scheme", map[string]string{"Authorization": "Basic abc"}},
		}
		for _, tc := range cases {
			rec := postMCP(t, m.router, tc.headers, initialize)
			if rec.Code != http.StatusUnauthorized {
				t.Errorf("%s: status = %d, want 401", tc.name, rec.Code)
			}
		}
	})

	t.Run("revoked and expired tokens rejected", func(t *testing.T) {
		revoked := m.createToken(t, "e2e-revoked", []auth.Scope{auth.ScopeRead})
		tokens, err := m.authService.ListAPITokens(context.Background())
		if err != nil {
			t.Fatalf("list tokens: %v", err)
		}
		for _, tok := range tokens {
			if tok.Name == "e2e-revoked" {
				if err := m.authService.RevokeAPIToken(context.Background(), tok.ID); err != nil {
					t.Fatalf("revoke: %v", err)
				}
			}
		}
		rec := postMCP(t, m.router, map[string]string{"Authorization": "Bearer " + revoked.raw}, initialize)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("revoked: status = %d, want 401", rec.Code)
		}

		// 过期：创建合法 token 后推进服务时钟。
		base := m.authService.Now
		now := base().UTC()
		m.authService.Now = func() time.Time { return now }
		expires := now.Add(time.Hour)
		_, expiredRaw, err := m.authService.CreateAPIToken(context.Background(), auth.CreateAPITokenInput{
			Name: "e2e-expired", Scopes: []auth.Scope{auth.ScopeRead}, ExpiresAt: &expires,
		})
		if err != nil {
			t.Fatalf("create expired token: %v", err)
		}
		m.authService.Now = func() time.Time { return now.Add(2 * time.Hour) }
		rec = postMCP(t, m.router, map[string]string{"Authorization": "Bearer " + expiredRaw}, initialize)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("expired: status = %d, want 401", rec.Code)
		}
		m.authService.Now = base
	})

	t.Run("scope isolation across sessions", func(t *testing.T) {
		readToken := m.createToken(t, "e2e-matrix-read", []auth.Scope{auth.ScopeRead})
		runToken := m.createToken(t, "e2e-matrix-run", []auth.Scope{auth.ScopeRun})
		adminToken := m.createToken(t, "e2e-matrix-admin", []auth.Scope{auth.ScopeAdmin})

		readSession := connectClient(t, m.router, readToken.raw)
		runSession := connectClient(t, m.router, runToken.raw)
		adminSession := connectClient(t, m.router, adminToken.raw)

		// run-only：run_sync 之外全部 permission denied。run_sync 对
		// 不存在的 job 得到 not found，说明授权已通过、进入业务层。
		_, text, isErr, err := mcpCall(t, runSession, "run_sync", map[string]any{"job_id": "job_none"})
		if got := requireToolText(t, "", text, isErr, err); !strings.Contains(got, "job not found") {
			t.Fatalf("run token run_sync: %q, want job not found", got)
		}
		for _, tc := range []struct {
			name string
			args map[string]any
		}{
			{"list_sources", map[string]any{}},
			{"list_jobs", map[string]any{}},
			{"get_job", map[string]any{"job_id": "job_none"}},
			{"search_files", map[string]any{"job_id": "job_none", "query": "x"}},
			{"get_file_info", map[string]any{"job_id": "job_none", "path": "/x"}},
			{"get_sync_run", map[string]any{"run_id": "run_none"}},
		} {
			_, text, isErr, err := mcpCall(t, runSession, tc.name, tc.args)
			if got := requireToolText(t, "", text, isErr, err); !strings.Contains(got, "permission denied") {
				t.Errorf("run token %s: %q, want permission denied", tc.name, got)
			}
		}

		// read-only：read tools 可用（业务错误也是授权通过的证明），
		// run_sync 拒绝。
		_, text, isErr, err = mcpCall(t, readSession, "get_job", map[string]any{"job_id": "job_none"})
		if got := requireToolText(t, "", text, isErr, err); !strings.Contains(got, "job not found") {
			t.Fatalf("read token get_job: %q, want job not found", got)
		}
		_, text, isErr, err = mcpCall(t, readSession, "run_sync", map[string]any{"job_id": "job_none"})
		if got := requireToolText(t, "", text, isErr, err); !strings.Contains(got, "permission denied") {
			t.Fatalf("read token run_sync: %q, want permission denied", got)
		}

		// admin：工具全部可达。
		raw, text, isErr, err := mcpCall(t, adminSession, "list_sources", map[string]any{})
		if err != nil || isErr {
			t.Fatalf("admin list_sources: err=%v isErr=%v text=%s raw=%s", err, isErr, text, raw)
		}
		_, text, isErr, err = mcpCall(t, adminSession, "get_job", map[string]any{"job_id": "job_none"})
		if got := requireToolText(t, "", text, isErr, err); !strings.Contains(got, "job not found") {
			t.Fatalf("admin get_job: %q, want job not found", got)
		}
	})
}

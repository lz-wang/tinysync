package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"tinysync/internal/auth"
	authsqlite "tinysync/internal/auth/sqlite"
	"tinysync/internal/source"
	sourcesqlite "tinysync/internal/source/sqlite"
	"tinysync/internal/storage"
	"tinysync/internal/syncjob"
	jobsqlite "tinysync/internal/syncjob/sqlite"
)

// errFactoryUnavailable 是 nilFactory 拒绝构造远端的占位错误。
var errFactoryUnavailable = errors.New("remote factory unavailable in tool tests")

// toolsEnv 是 tool 层测试环境：真实应用服务 + 官方 MCP client，
// 用于锁定 wire contract 与 per-tool 授权矩阵。
type toolsEnv struct {
	t         *testing.T
	serverURL string
	svc       *auth.Service
}

func newToolsEnv(t *testing.T, sources []source.Source, jobs []syncjob.Job) *toolsEnv {
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

	now := time.Now().UTC()
	srcRepo := sourcesqlite.New(db)
	for _, s := range sources {
		clone := s
		clone.CreatedAt = now
		clone.UpdatedAt = now
		creds := seedCredentialsFor(clone)
		if err := srcRepo.Create(context.Background(), clone, creds); err != nil {
			t.Fatalf("create source %s: %v", s.ID, err)
		}
	}
	jobRepo := jobsqlite.NewRepository(db)
	sourcesSvc := source.NewService(srcRepo, nilFactory{})
	jobsSvc := syncjob.NewService(jobRepo, sourcesSvc, dataDir)
	for _, j := range jobs {
		clone := j
		clone.CreatedAt = now
		clone.UpdatedAt = now
		if err := jobRepo.Create(context.Background(), clone); err != nil {
			t.Fatalf("create job %s: %v", j.ID, err)
		}
	}

	handler := New(Deps{
		Auth:    svc,
		Sources: sourcesSvc,
		Jobs:    jobsSvc,
	})
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return &toolsEnv{t: t, serverURL: server.URL, svc: svc}
}

// connect 以给定 scope 集合的 API Token 建立 MCP client 会话。
func (e *toolsEnv) connect(scopes []auth.Scope) *mcp.ClientSession {
	e.t.Helper()
	_, raw, err := e.svc.CreateAPIToken(context.Background(), auth.CreateAPITokenInput{
		Name:   "tools-test-token",
		Scopes: scopes,
	})
	if err != nil {
		e.t.Fatalf("create api token: %v", err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "mcp-tools-test", Version: "0"}, nil)
	transport := &mcp.StreamableClientTransport{
		Endpoint: e.serverURL + "/mcp",
		HTTPClient: &http.Client{
			Transport: bearerTransport{base: http.DefaultTransport, token: raw},
		},
		MaxRetries: -1,
	}
	session, err := client.Connect(context.Background(), transport, nil)
	if err != nil {
		e.t.Fatalf("mcp connect: %v", err)
	}
	e.t.Cleanup(func() { _ = session.Close() })
	return session
}

// bearerTransport 为全部请求注入 Bearer token，模拟真实 Agent。
type bearerTransport struct {
	base  http.RoundTripper
	token string
}

func (b bearerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.Header.Set("Authorization", "Bearer "+b.token)
	return b.base.RoundTrip(req)
}

// seedCredentialsFor 按 Source 类型给出与 credential_state 匹配的
// 种子凭据（存储层从凭据推导布尔集合，明文绝不回显）。
func seedCredentialsFor(s source.Source) source.Credentials {
	switch s.Type {
	case source.TypeWebDAV:
		return source.Credentials{WebDAV: &source.WebDAVCredentials{Password: "mcp-password"}}
	case source.TypeSFTP:
		return source.Credentials{SFTP: &source.SFTPCredentials{Password: "mcp-password"}}
	default:
		return source.Credentials{}
	}
}

// nilFactory 是不参与测试的 RemoteFactory 占位（工具测试不触远端）。
type nilFactory struct{}

func (nilFactory) Type() source.Type {
	return "nil"
}

func (nilFactory) Create(context.Context, source.Source, source.Credentials) (source.Remote, error) {
	return nil, errFactoryUnavailable
}

// discoveryFixture 构造两个 Source 与两个 Job（name 乱序插入以验证
// 排序稳定性）。
func discoveryFixture(t *testing.T) (*toolsEnv, source.Source, source.Source) {
	t.Helper()
	webdav := source.Source{
		ID:   "src_mcp_webdav",
		Name: "MCP WebDAV",
		Type: source.TypeWebDAV,
		Config: source.Config{
			WebDAV: &source.WebDAVConfig{Endpoint: "https://mcp.invalid/dav", Username: "mcp-user"},
		},
		CredentialState: source.CredentialState{WebDAV: &source.WebDAVCredentialState{PasswordSet: true}},
		Enabled:         true,
	}
	sftp := source.Source{
		ID:   "src_mcp_sftp",
		Name: "MCP SFTP",
		Type: source.TypeSFTP,
		Config: source.Config{
			SFTP: &source.SFTPConfig{
				Host: "mcp.invalid", Port: 22, Username: "mcp",
				RemoteRoot: "/srv", AuthMethod: source.SFTPAuthPassword,
				HostKeyFingerprint: "SHA256:UC1Dk4I9LLQOV3B8eZ5FlrUUcbbNie4INffe2TDTz3k",
			},
		},
		CredentialState: source.CredentialState{SFTP: &source.SFTPCredentialState{PasswordSet: true}},
		Enabled:         false,
	}
	jobs := []syncjob.Job{
		{ID: "job_mcp_b", Name: "zeta job", SourceID: webdav.ID, RemoteRoot: "/", LocalRoot: t.TempDir(), Mode: syncjob.ModeCopy, Enabled: true,
			Schedule: syncjob.Schedule{Type: syncjob.ScheduleManual}},
		{ID: "job_mcp_a", Name: "alpha job", SourceID: sftp.ID, RemoteRoot: "/srv", LocalRoot: t.TempDir(), Mode: syncjob.ModeMirror, Enabled: false,
			Schedule: syncjob.Schedule{Type: syncjob.ScheduleManual}},
	}
	env := newToolsEnv(t, []source.Source{webdav, sftp}, jobs)
	return env, webdav, sftp
}

// requireToolError 断言工具调用返回 is_error 结果并返回错误文案。
func requireToolError(t *testing.T, res *mcp.CallToolResult, err error) string {
	t.Helper()
	if err == nil && !res.IsError {
		t.Fatalf("expected tool error, got success: %+v", res.StructuredContent)
	}
	text := ""
	if err != nil {
		text = err.Error()
	}
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			text = tc.Text
		}
	}
	return text
}

func TestMCPListSources(t *testing.T) {
	env, webdav, sftp := discoveryFixture(t)
	session := env.connect([]auth.Scope{auth.ScopeRead})

	res, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	got := map[string]bool{}
	for _, tool := range res.Tools {
		got[tool.Name] = true
	}
	for _, want := range []string{"list_sources", "list_jobs", "get_job"} {
		if !got[want] {
			t.Fatalf("tools/list missing %q: %v", want, got)
		}
	}

	var result listSourcesResult
	raw := mustCallTool(t, session, "list_sources", ListInput{})
	if err := remarshal(raw, &result); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if result.Total != 2 || len(result.Sources) != 2 {
		t.Fatalf("sources = %+v, want 2 entries", result)
	}
	byID := map[string]sourceSummary{}
	for _, s := range result.Sources {
		byID[s.ID] = s
	}
	ws := byID[webdav.ID]
	configJSON, err := json.Marshal(ws.Config)
	if err != nil || !strings.Contains(string(configJSON), "https://mcp.invalid/dav") {
		t.Fatalf("webdav config missing endpoint: %v (%v)", ws.Config, err)
	}
	if ws.CredentialState.WebDAV == nil || !ws.CredentialState.WebDAV.PasswordSet {
		t.Fatalf("webdav credential_state = %+v, want password_set", ws.CredentialState)
	}
	ss := byID[sftp.ID]
	if ss.Enabled {
		t.Fatal("sftp should stay disabled")
	}
	// secret 永不出现在返回中（直接检查 wire JSON）。
	for _, secret := range []string{"mcp-password", "S3cret"} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("list_sources leaked %q: %s", secret, raw)
		}
	}
}

func TestMCPListJobsOrderAndPaging(t *testing.T) {
	env, _, _ := discoveryFixture(t)
	session := env.connect([]auth.Scope{auth.ScopeRead})

	var result listJobsResult
	call := mustCallTool(t, session, "list_jobs", ListInput{})
	if err := remarshal(call, &result); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if result.Total != 2 {
		t.Fatalf("total = %d, want 2", result.Total)
	}
	// Job 列表按 name 大小写不敏感排序：alpha 在 zeta 之前。
	if len(result.Jobs) != 2 || result.Jobs[0].Name != "alpha job" || result.Jobs[1].Name != "zeta job" {
		t.Fatalf("job order = %+v, want alpha before zeta", result.Jobs)
	}

	// 分页：limit=1 offset=1 只返回第二条。
	var paged listJobsResult
	call = mustCallTool(t, session, "list_jobs", ListInput{Limit: 1, Offset: 1})
	if err := remarshal(call, &paged); err != nil {
		t.Fatalf("decode paged result: %v", err)
	}
	if len(paged.Jobs) != 1 || paged.Jobs[0].Name != "zeta job" {
		t.Fatalf("paged jobs = %+v, want only zeta", paged.Jobs)
	}

	// 越界 limit 报 tool error，不静默截断。
	if _, res, err := callTool(session, "list_jobs", ListInput{Limit: maxListLimit + 1}); err == nil && !res.IsError {
		t.Fatal("limit over max should fail")
	}
}

func TestMCPGetJob(t *testing.T) {
	env, _, _ := discoveryFixture(t)
	session := env.connect([]auth.Scope{auth.ScopeRead})

	var detail jobDetail
	call := mustCallTool(t, session, "get_job", JobIDInput{JobID: "job_mcp_b"})
	if err := remarshal(call, &detail); err != nil {
		t.Fatalf("decode detail: %v", err)
	}
	if detail.ID != "job_mcp_b" || detail.Mode != string(syncjob.ModeCopy) || detail.RemoteRoot != "/" {
		t.Fatalf("detail = %+v", detail)
	}
	if detail.Schedule == nil || detail.Schedule.Type != string(syncjob.ScheduleManual) {
		t.Fatalf("schedule = %+v, want manual", detail.Schedule)
	}

	// 未知 ID：确定性 tool error。
	_, res, err := callTool(session, "get_job", JobIDInput{JobID: "job_missing"})
	text := requireToolError(t, res, err)
	if !strings.Contains(text, "job not found") {
		t.Fatalf("error text = %q, want job not found", text)
	}
}

func TestMCPDiscoveryAuthorization(t *testing.T) {
	env, _, _ := discoveryFixture(t)

	t.Run("run-only token is denied read tools", func(t *testing.T) {
		session := env.connect([]auth.Scope{auth.ScopeRun})
		for _, name := range []string{"list_sources", "list_jobs"} {
			_, res, err := callTool(session, name, ListInput{})
			text := requireToolError(t, res, err)
			if !strings.Contains(text, "permission denied") {
				t.Fatalf("%s error text = %q, want permission denied", name, text)
			}
		}
		_, res, err := callTool(session, "get_job", JobIDInput{JobID: "job_mcp_b"})
		if text := requireToolError(t, res, err); !strings.Contains(text, "permission denied") {
			t.Fatalf("get_job error text = %q, want permission denied", text)
		}
	})
	t.Run("read token can read", func(t *testing.T) {
		session := env.connect([]auth.Scope{auth.ScopeRead})
		if _, res, err := callTool(session, "list_sources", ListInput{}); err != nil || res.IsError {
			t.Fatalf("read token list_sources failed: %v", err)
		}
	})
	t.Run("admin can read", func(t *testing.T) {
		session := env.connect([]auth.Scope{auth.ScopeAdmin})
		if _, res, err := callTool(session, "list_jobs", ListInput{}); err != nil || res.IsError {
			t.Fatalf("admin token list_jobs failed: %v", err)
		}
	})
}

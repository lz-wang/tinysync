package mcp

import (
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"tinysync/internal/auth"
	"tinysync/internal/source"
	"tinysync/internal/syncjob"
)

// fileFixture 构造一个 Job（LocalRoot 为测试可见的临时目录）、实际
// 写入的本地文件与 synced managed 记录，覆盖 URL 转义场景。
func fileFixture(t *testing.T) (*toolsEnv, string) {
	t.Helper()
	root := t.TempDir()
	job := syncjob.Job{
		ID: "job_mcp_files", Name: "files job", SourceID: "src_mcp_files",
		RemoteRoot: "/", LocalRoot: root, Mode: syncjob.ModeCopy,
		Enabled:  true,
		Schedule: syncjob.Schedule{Type: syncjob.ScheduleManual},
	}
	webdav := source.Source{
		ID:   "src_mcp_files",
		Name: "Files WebDAV",
		Type: source.TypeWebDAV,
		Config: source.Config{
			WebDAV: &source.WebDAVConfig{Endpoint: "https://files.invalid/dav", Username: "mcp"},
		},
		Enabled: true,
	}
	env := newToolsEnv(t, []source.Source{webdav}, []syncjob.Job{job})
	writeRel := func(rel, content string) {
		t.Helper()
		abs := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", abs, err)
		}
		if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", abs, err)
		}
	}
	writeRel("docs/report.md", "hello report")
	writeRel("docs/空间 #1 100%.md", "escaped")
	env.upsertSynced(job.ID, "docs/report.md", "docs/空间 #1 100%.md")
	return env, root
}

func TestMCPSearchFiles(t *testing.T) {
	env, _ := fileFixture(t)
	session := env.connect([]auth.Scope{auth.ScopeRead})

	var result searchFilesResult
	raw := mustCallTool(t, session, "search_files", SearchFilesInput{JobID: "job_mcp_files", Query: "report"})
	if err := remarshal(raw, &result); err != nil {
		t.Fatalf("decode search result: %v", err)
	}
	if len(result.Entries) != 1 || result.Truncated {
		t.Fatalf("result = %+v, want 1 entry not truncated", result)
	}
	entry := result.Entries[0]
	if entry.Path != "/docs/report.md" || entry.Kind != "file" || entry.Size != int64(len("hello report")) {
		t.Fatalf("entry = %+v", entry)
	}
	if !strings.HasPrefix(entry.DownloadURL, "/api/v1/jobs/job_mcp_files/files/download?path=") {
		t.Fatalf("download_url = %q, want relative REST URL", entry.DownloadURL)
	}
	if entry.ResourceURI != "tinysync://jobs/job_mcp_files/files/docs/report.md" {
		t.Fatalf("resource_uri = %q", entry.ResourceURI)
	}

	// 未匹配：空列表不截断。
	var empty searchFilesResult
	raw = mustCallTool(t, session, "search_files", SearchFilesInput{JobID: "job_mcp_files", Query: "missing"})
	if err := remarshal(raw, &empty); err != nil {
		t.Fatalf("decode empty result: %v", err)
	}
	if len(empty.Entries) != 0 || empty.Truncated {
		t.Fatalf("empty result = %+v", empty)
	}

	// 非法输入：空 query 报 tool error。
	_, res, err := callTool(session, "search_files", SearchFilesInput{JobID: "job_mcp_files", Query: ""})
	if text := requireToolError(t, res, err); !strings.Contains(text, "invalid input") {
		t.Fatalf("error = %q, want invalid input", text)
	}
	// 未知 Job。
	_, res, err = callTool(session, "search_files", SearchFilesInput{JobID: "job_missing", Query: "x"})
	if text := requireToolError(t, res, err); !strings.Contains(text, "job not found") {
		t.Fatalf("error = %q, want job not found", text)
	}
}

func TestMCPGetFileInfo(t *testing.T) {
	env, _ := fileFixture(t)
	session := env.connect([]auth.Scope{auth.ScopeRead})

	t.Run("regular file", func(t *testing.T) {
		var detail fileInfoDetail
		raw := mustCallTool(t, session, "get_file_info", GetFileInfoInput{JobID: "job_mcp_files", Path: "/docs/report.md"})
		if err := remarshal(raw, &detail); err != nil {
			t.Fatalf("decode detail: %v", err)
		}
		if detail.Kind != "file" || detail.Size != int64(len("hello report")) || detail.Managed == nil || !*detail.Managed {
			t.Fatalf("detail = %+v", detail)
		}
		if detail.ResourceURI == "" || detail.DownloadURL == "" {
			t.Fatalf("file detail missing resource_uri / download_url: %+v", detail)
		}
	})
	t.Run("directory has no download or resource", func(t *testing.T) {
		var detail fileInfoDetail
		raw := mustCallTool(t, session, "get_file_info", GetFileInfoInput{JobID: "job_mcp_files", Path: "/docs"})
		if err := remarshal(raw, &detail); err != nil {
			t.Fatalf("decode dir detail: %v", err)
		}
		if detail.Kind != "directory" || detail.ResourceURI != "" || detail.DownloadURL != "" {
			t.Fatalf("dir detail = %+v", detail)
		}
	})
	t.Run("missing file", func(t *testing.T) {
		_, res, err := callTool(session, "get_file_info", GetFileInfoInput{JobID: "job_mcp_files", Path: "/docs/none.md"})
		if text := requireToolError(t, res, err); !strings.Contains(text, "file not found") {
			t.Fatalf("error = %q, want file not found", text)
		}
	})
	t.Run("traversal rejected", func(t *testing.T) {
		_, res, err := callTool(session, "get_file_info", GetFileInfoInput{JobID: "job_mcp_files", Path: "/../escape.txt"})
		if text := requireToolError(t, res, err); !strings.Contains(text, "invalid input") {
			t.Fatalf("error = %q, want invalid input", text)
		}
	})
}

func TestMCPFileURLEscaping(t *testing.T) {
	env, _ := fileFixture(t)
	session := env.connect([]auth.Scope{auth.ScopeRead})

	var detail fileInfoDetail
	raw := mustCallTool(t, session, "get_file_info", GetFileInfoInput{JobID: "job_mcp_files", Path: "/docs/空间 #1 100%.md"})
	if err := remarshal(raw, &detail); err != nil {
		t.Fatalf("decode escaped detail: %v", err)
	}
	// resource_uri：路径特殊字符逐段 percent-encode，分隔符保留，
	// 不出现空格 / # / ? 等裸字符。
	wantResource := "tinysync://jobs/job_mcp_files/files/docs/" + url.PathEscape("空间 #1 100%.md")
	if detail.ResourceURI != wantResource {
		t.Fatalf("resource_uri = %q, want %q", detail.ResourceURI, wantResource)
	}
	if strings.ContainsAny(detail.ResourceURI, " #?") {
		t.Fatalf("resource_uri contains raw unsafe characters: %q", detail.ResourceURI)
	}
	// download_url：path 查询参数整体转义，且可无损还原。
	wantDownload := "/api/v1/jobs/job_mcp_files/files/download?path=" + url.QueryEscape("/docs/空间 #1 100%.md")
	if detail.DownloadURL != wantDownload {
		t.Fatalf("download_url = %q, want %q", detail.DownloadURL, wantDownload)
	}
	parsed, err := url.Parse(detail.DownloadURL)
	if err != nil {
		t.Fatalf("parse download_url: %v", err)
	}
	if got := parsed.Query().Get("path"); got != "/docs/空间 #1 100%.md" {
		t.Fatalf("decoded path = %q", got)
	}
}

func TestMCPFileToolsAuthorization(t *testing.T) {
	env, _ := fileFixture(t)
	session := env.connect([]auth.Scope{auth.ScopeRun})
	_, res, err := callTool(session, "search_files", SearchFilesInput{JobID: "job_mcp_files", Query: "report"})
	if text := requireToolError(t, res, err); !strings.Contains(text, "permission denied") {
		t.Fatalf("search_files error = %q, want permission denied", text)
	}
	_, res, err = callTool(session, "get_file_info", GetFileInfoInput{JobID: "job_mcp_files", Path: "/docs/report.md"})
	if text := requireToolError(t, res, err); !strings.Contains(text, "permission denied") {
		t.Fatalf("get_file_info error = %q, want permission denied", text)
	}
	// 工具输出不出现凭据形态字段（raw token 泄露检查由 E2E 走完整
	// wire 承担）。
	readSession := env.connect([]auth.Scope{auth.ScopeRead})
	rawOut := mustCallTool(t, readSession, "get_file_info", GetFileInfoInput{JobID: "job_mcp_files", Path: "/docs/report.md"})
	for _, secret := range []string{"token", "credential", "bearer"} {
		if strings.Contains(strings.ToLower(string(rawOut)), secret) {
			t.Fatalf("get_file_info output mentions %q: %s", secret, rawOut)
		}
	}
}

package mcp

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"tinysync/internal/auth"
	"tinysync/internal/source"
	"tinysync/internal/syncjob"
)

// resourceFixture 构造一个 Job 与若干本地文件（含边界场景），返回
// env 与 LocalRoot。
func resourceFixture(t *testing.T) (*toolsEnv, string) {
	t.Helper()
	root := t.TempDir()
	job := syncjob.Job{
		ID: "job_mcp_res", Name: "resource job", SourceID: "src_mcp_res",
		RemoteRoot: "/", LocalRoot: root, Mode: syncjob.ModeCopy,
		Enabled:  true,
		Schedule: syncjob.Schedule{Type: syncjob.ScheduleManual},
	}
	webdav := source.Source{
		ID:   "src_mcp_res",
		Name: "Resource WebDAV",
		Type: source.TypeWebDAV,
		Config: source.Config{
			WebDAV: &source.WebDAVConfig{Endpoint: "https://res.invalid/dav", Username: "mcp"},
		},
		Enabled: true,
	}
	env := newToolsEnv(t, []source.Source{webdav}, []syncjob.Job{job})
	writeRel := func(rel string, content []byte) {
		t.Helper()
		abs := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", abs, err)
		}
		if err := os.WriteFile(abs, content, 0o644); err != nil {
			t.Fatalf("write %s: %v", abs, err)
		}
	}
	writeRel("readme.md", []byte("# hello tinysync\n"))
	writeRel("docs/note.txt", []byte("note"))
	writeRel("empty.txt", []byte(""))
	writeRel("binary.bin", []byte{0x00, 0xFF, 0xFE, 0x00, 0x01})
	writeRel("boundary.txt", []byte(strings.Repeat("a", maxResourceSize)))
	writeRel("over.txt", []byte(strings.Repeat("a", maxResourceSize+1)))
	if err := os.Symlink("../outside-secret.txt", filepath.Join(root, "link.txt")); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	return env, root
}

// readResource 以指定 scope 的 token 读取资源 URI，返回文本与错误。
func readResource(t *testing.T, env *toolsEnv, scopes []auth.Scope, uri string) (string, error) {
	t.Helper()
	session := env.connect(scopes)
	res, err := session.ReadResource(context.Background(), &mcp.ReadResourceParams{URI: uri})
	if err != nil {
		return "", err
	}
	if len(res.Contents) != 1 {
		t.Fatalf("contents = %d, want 1", len(res.Contents))
	}
	return res.Contents[0].Text, nil
}

func TestMCPResourceRead(t *testing.T) {
	env, _ := resourceFixture(t)
	read := []auth.Scope{auth.ScopeRead}

	t.Run("small utf-8 text", func(t *testing.T) {
		text, err := readResource(t, env, read, "tinysync://jobs/job_mcp_res/files/readme.md")
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		if text != "# hello tinysync\n" {
			t.Fatalf("text = %q", text)
		}
	})
	t.Run("empty file", func(t *testing.T) {
		text, err := readResource(t, env, read, "tinysync://jobs/job_mcp_res/files/empty.txt")
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		if text != "" {
			t.Fatalf("text = %q, want empty", text)
		}
	})
	t.Run("binary rejected", func(t *testing.T) {
		if _, err := readResource(t, env, read, "tinysync://jobs/job_mcp_res/files/binary.bin"); err == nil ||
			!strings.Contains(err.Error(), "UTF-8") {
			t.Fatalf("err = %v, want UTF-8 rejection", err)
		}
	})
	t.Run("256 KiB boundary accepted", func(t *testing.T) {
		text, err := readResource(t, env, read, "tinysync://jobs/job_mcp_res/files/boundary.txt")
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		if len(text) != maxResourceSize {
			t.Fatalf("len = %d, want %d", len(text), maxResourceSize)
		}
	})
	t.Run("256 KiB + 1 rejected", func(t *testing.T) {
		_, err := readResource(t, env, read, "tinysync://jobs/job_mcp_res/files/over.txt")
		if err == nil || !strings.Contains(err.Error(), "too large") {
			t.Fatalf("err = %v, want too large", err)
		}
	})
	t.Run("directory rejected", func(t *testing.T) {
		_, err := readResource(t, env, read, "tinysync://jobs/job_mcp_res/files")
		if err == nil {
			t.Fatal("reading files/ directory should fail (malformed uri)")
		}
		if _, err := readResource(t, env, read, "tinysync://jobs/job_mcp_res/files/docs"); err == nil ||
			!strings.Contains(err.Error(), "regular file") {
			t.Fatalf("err = %v, want regular-file rejection", err)
		}
	})
	t.Run("symlink rejected", func(t *testing.T) {
		_, err := readResource(t, env, read, "tinysync://jobs/job_mcp_res/files/link.txt")
		if err == nil || !strings.Contains(err.Error(), "regular file") {
			t.Fatalf("err = %v, want regular-file rejection", err)
		}
	})
	t.Run("traversal rejected", func(t *testing.T) {
		_, err := readResource(t, env, read, "tinysync://jobs/job_mcp_res/files/../secret.txt")
		if err == nil || !strings.Contains(err.Error(), "invalid") {
			t.Fatalf("err = %v, want invalid path", err)
		}
	})
	t.Run("missing file", func(t *testing.T) {
		_, err := readResource(t, env, read, "tinysync://jobs/job_mcp_res/files/none.txt")
		if err == nil || !strings.Contains(err.Error(), "not found") {
			t.Fatalf("err = %v, want not found", err)
		}
	})
	t.Run("unknown scheme is not found", func(t *testing.T) {
		// 非 tinysync URI 不匹配任何模板：SDK 统一 Resource not found。
		_, err := readResource(t, env, read, "file:///etc/passwd")
		if err == nil || !strings.Contains(err.Error(), "not found") {
			t.Fatalf("err = %v, want not found", err)
		}
	})
	t.Run("run-only token denied", func(t *testing.T) {
		_, err := readResource(t, env, []auth.Scope{auth.ScopeRun}, "tinysync://jobs/job_mcp_res/files/readme.md")
		if err == nil || !strings.Contains(err.Error(), "permission denied") {
			t.Fatalf("err = %v, want permission denied", err)
		}
	})
}

func TestMCPResourceLimitReaderGuardsReplacement(t *testing.T) {
	// LimitReader(max+1) 的独立行为：即使 Stat 通道被绕过（直接给
	// 读取函数一个超大文件），读到 max+1 字节即拒绝，不截断返回。
	f, err := os.CreateTemp(t.TempDir(), "limit")
	if err != nil {
		t.Fatalf("temp file: %v", err)
	}
	defer func() { _ = f.Close() }()
	if _, err := f.WriteString(strings.Repeat("a", maxResourceSize+1)); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := f.Seek(0, 0); err != nil {
		t.Fatalf("seek: %v", err)
	}
	if _, err := readLimitedText(f); err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("err = %v, want too large from read path", err)
	}
}

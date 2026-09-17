package filesafe

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// TestServeFileContent：MIME 按扩展名推断（未知回退
// application/octet-stream）、Range / HEAD 语义由 http.ServeContent
// 统一——Local 下载与 Published serving 共用本出口，行为不得分叉。
func TestServeFileContent(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) *os.File {
		t.Helper()
		abs := filepath.Join(dir, name)
		if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
		f, err := os.Open(abs)
		if err != nil {
			t.Fatalf("open %s: %v", name, err)
		}
		return f
	}
	serve := func(t *testing.T, f *os.File, name, method string, rangeHeader string) *httptest.ResponseRecorder {
		t.Helper()
		info, err := f.Stat()
		if err != nil {
			t.Fatalf("stat: %v", err)
		}
		if _, err := f.Seek(0, 0); err != nil {
			t.Fatalf("seek: %v", err)
		}
		req := httptest.NewRequest(method, "/served", nil)
		if rangeHeader != "" {
			req.Header.Set("Range", rangeHeader)
		}
		w := httptest.NewRecorder()
		ServeFileContent(w, req, f, info, name)
		return w
	}

	t.Run("known extension mime", func(t *testing.T) {
		f := write("a.txt", "hello")
		defer func() { _ = f.Close() }()
		w := serve(t, f, "a.txt", http.MethodGet, "")
		if w.Code != http.StatusOK || w.Body.String() != "hello" {
			t.Errorf("serve = %d %q, want 200 hello", w.Code, w.Body.String())
		}
		if got := w.Header().Get("Content-Type"); got != "text/plain; charset=utf-8" {
			t.Errorf("content-type = %q, want text/plain; charset=utf-8", got)
		}
	})

	t.Run("unknown extension falls back to octet stream", func(t *testing.T) {
		f := write("b.unknownext", "raw")
		defer func() { _ = f.Close() }()
		w := serve(t, f, "b.unknownext", http.MethodGet, "")
		if got := w.Header().Get("Content-Type"); got != "application/octet-stream" {
			t.Errorf("content-type = %q, want application/octet-stream", got)
		}
	})

	t.Run("range request", func(t *testing.T) {
		f := write("c.txt", "hello")
		defer func() { _ = f.Close() }()
		w := serve(t, f, "c.txt", http.MethodGet, "bytes=1-3")
		if w.Code != http.StatusPartialContent || w.Body.String() != "ell" {
			t.Errorf("range = %d %q, want 206 ell", w.Code, w.Body.String())
		}
	})

	t.Run("head has no body", func(t *testing.T) {
		f := write("d.txt", "hello")
		defer func() { _ = f.Close() }()
		w := serve(t, f, "d.txt", http.MethodHead, "")
		if w.Code != http.StatusOK || w.Body.Len() != 0 {
			t.Errorf("head = %d body %d, want 200 with empty body", w.Code, w.Body.Len())
		}
	})
}

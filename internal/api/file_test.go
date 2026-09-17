package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"tinysync/internal/browser"
	"tinysync/internal/source"
	"tinysync/internal/source/sqlite"
	"tinysync/internal/storage"
)

// fileRemote 是文件浏览 API 测试的可控 Remote。
type fileRemote struct {
	entries map[string][]source.FileInfo
	dirs    map[string]bool
	exists  map[string]bool
	content string
	listErr error
	openErr error
	closeN  int
}

func (r *fileRemote) Stat(ctx context.Context, path string) (source.FileInfo, error) {
	if r.dirs[path] {
		return source.FileInfo{Path: path, IsDir: true}, nil
	}
	if r.exists[path] {
		return source.FileInfo{
			Path: path,
			Fingerprint: source.Fingerprint{
				Size:       int64(len(r.content)),
				ModifiedAt: time.Unix(1757879400, 0).UTC(),
			},
		}, nil
	}
	return source.FileInfo{}, fmt.Errorf("%w: %s not found", fs.ErrNotExist, path)
}

func (r *fileRemote) List(ctx context.Context, path string, opts source.ListOptions) (source.FilePage, error) {
	if r.listErr != nil {
		return source.FilePage{}, r.listErr
	}
	return source.FilePage{Entries: r.entries[path]}, nil
}

func (r *fileRemote) Open(ctx context.Context, path string) (io.ReadCloser, error) {
	if r.openErr != nil {
		return nil, r.openErr
	}
	return io.NopCloser(strings.NewReader(r.content)), nil
}

func (r *fileRemote) Close() error {
	r.closeN++
	return nil
}

// newFileRouter 构造带 Source 服务与 Remote Browser 的路由，返回
// router 与已存在的 Source ID。
func newFileRouter(t *testing.T, remote *fileRemote) (*gin.Engine, string) {
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
	svc := source.NewService(sqlite.New(db), fakeFactory{remote: remote})
	src, err := svc.Create(context.Background(), source.CreateInput{
		Name:    "files-it",
		Type:    source.TypeWebDAV,
		Enabled: true,
		Config:  source.Config{WebDAV: &source.WebDAVConfig{Endpoint: "http://127.0.0.1:1/dav"}},
	})
	if err != nil {
		t.Fatalf("create source: %v", err)
	}
	files := browser.NewRemoteService(svc)
	return NewRouter(testWebFS(), Dependencies{Sources: svc, Browser: files}), src.ID
}

// 目录列表返回 path / entries / next_cursor 结构。
func TestRemoteFilesList(t *testing.T) {
	remote := &fileRemote{
		entries: map[string][]source.FileInfo{
			"/": {
				{Path: "/docs", IsDir: true},
				{Path: "/a.txt", Fingerprint: source.Fingerprint{Size: 3, ModifiedAt: time.Unix(1757879400, 0).UTC()}},
			},
		},
		dirs: map[string]bool{"/docs": true},
	}
	router, id := newFileRouter(t, remote)

	w := doJSON(t, router, http.MethodGet, "/api/v1/sources/"+id+"/files?path=/", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	var resp struct {
		Path    string `json:"path"`
		Entries []struct {
			Path    string `json:"path"`
			Name    string `json:"name"`
			Kind    string `json:"kind"`
			Size    int64  `json:"size"`
			Managed *bool  `json:"managed"`
		} `json:"entries"`
		NextCursor string `json:"next_cursor"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Path != "/" || resp.NextCursor != "" || len(resp.Entries) != 2 {
		t.Fatalf("response = %+v", resp)
	}
	if resp.Entries[0].Path != "/docs" || resp.Entries[0].Kind != "directory" {
		t.Errorf("first entry = %+v, want /docs directory", resp.Entries[0])
	}
	if resp.Entries[0].Managed != nil {
		t.Errorf("remote entry must not carry managed, got %v", *resp.Entries[0].Managed)
	}
	if resp.Entries[1].Path != "/a.txt" || resp.Entries[1].Kind != "file" || resp.Entries[1].Size != 3 {
		t.Errorf("second entry = %+v, want /a.txt file size 3", resp.Entries[1])
	}
}

// 非法 query 参数与路径 → 400。
func TestRemoteFilesListInvalidInput(t *testing.T) {
	remote := &fileRemote{entries: map[string][]source.FileInfo{"/": {}}}
	router, id := newFileRouter(t, remote)

	for _, tc := range []struct {
		name string
		qs   string
	}{
		{"limit zero", "?path=/&limit=0"},
		{"limit over max", "?path=/&limit=501"},
		{"limit not a number", "?path=/&limit=abc"},
		{"traversal path", "?path=/../x"},
		{"duplicate slash", "?path=//a"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := doJSON(t, router, http.MethodGet, "/api/v1/sources/"+id+"/files"+tc.qs, "")
			if w.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400, body = %s", w.Code, w.Body.String())
			}
		})
	}
}

// Source 不存在 → 404；远端列表故障 → 502。
func TestRemoteFilesErrorMapping(t *testing.T) {
	remote := &fileRemote{listErr: errors.New("connection reset")}
	router, id := newFileRouter(t, remote)

	w := doJSON(t, router, http.MethodGet, "/api/v1/sources/no-such/files?path=/", "")
	if w.Code != http.StatusNotFound {
		t.Errorf("missing source status = %d, want 404", w.Code)
	}

	w = doJSON(t, router, http.MethodGet, "/api/v1/sources/"+id+"/files?path=/", "")
	if w.Code != http.StatusBadGateway {
		t.Errorf("remote failure status = %d, want 502", w.Code)
	}
}

// stat 命中返回条目；文件不存在 → 404。
func TestRemoteFilesStat(t *testing.T) {
	remote := &fileRemote{
		exists:  map[string]bool{"/a.txt": true},
		content: "abc",
	}
	router, id := newFileRouter(t, remote)

	w := doJSON(t, router, http.MethodGet, "/api/v1/sources/"+id+"/files/stat?path=/a.txt", "")
	if w.Code != http.StatusOK {
		t.Fatalf("stat status = %d, body = %s", w.Code, w.Body.String())
	}
	var entry struct {
		Kind string `json:"kind"`
		Size int64  `json:"size"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &entry); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if entry.Kind != "file" || entry.Size != 3 {
		t.Errorf("entry = %+v, want file size 3", entry)
	}

	w = doJSON(t, router, http.MethodGet, "/api/v1/sources/"+id+"/files/stat?path=/missing.txt", "")
	if w.Code != http.StatusNotFound {
		t.Errorf("missing file status = %d, want 404", w.Code)
	}
}

// download 流式返回：attachment 处置、Content-Length、Last-Modified、
// MIME 推断与完整内容；Remote 用毕释放。
func TestRemoteFilesDownload(t *testing.T) {
	remote := &fileRemote{
		exists:  map[string]bool{"/docs/report.txt": true},
		content: "report-body",
	}
	router, id := newFileRouter(t, remote)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/sources/"+id+"/files/download?path=/docs/report.txt", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("download status = %d, body = %s", w.Code, w.Body.String())
	}
	if got := w.Header().Get("Content-Type"); got != "text/plain; charset=utf-8" {
		t.Errorf("content-type = %q, want text/plain", got)
	}
	if got := w.Header().Get("Content-Disposition"); !strings.Contains(got, "attachment") || !strings.Contains(got, "report.txt") {
		t.Errorf("content-disposition = %q, want attachment with filename", got)
	}
	if got := w.Header().Get("Content-Length"); got != "11" {
		t.Errorf("content-length = %q, want 11", got)
	}
	if got := w.Header().Get("Last-Modified"); got == "" {
		t.Error("last-modified missing, want stat-derived header")
	}
	if got := w.Body.String(); got != "report-body" {
		t.Errorf("body = %q, want report-body", got)
	}
	if remote.closeN != 1 {
		t.Errorf("close count = %d, want 1", remote.closeN)
	}

	// 目录不可下载。
	remote.dirs = map[string]bool{"/docs": true}
	w = httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/sources/"+id+"/files/download?path=/docs", nil))
	if w.Code != http.StatusBadRequest {
		t.Errorf("directory download status = %d, want 400", w.Code)
	}
}

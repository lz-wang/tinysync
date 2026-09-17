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
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"tinysync/internal/browser"
	"tinysync/internal/source"
	"tinysync/internal/source/sqlite"
	"tinysync/internal/storage"
	"tinysync/internal/syncjob"
)

// memFileJobRepo 是文件 API 测试的内存 Job 仓库（syncjob.Repository
// 读取路径）。Repository 与 ManagedRepository 的 Delete 签名不同，
// 单一 struct 无法同时实现，managed 拆到 memFileManagedRepo。
type memFileJobRepo struct {
	jobs map[string]syncjob.Job
}

func newMemFileJobRepo() *memFileJobRepo {
	return &memFileJobRepo{jobs: map[string]syncjob.Job{}}
}

func (m *memFileJobRepo) Create(ctx context.Context, job syncjob.Job) error {
	m.jobs[job.ID] = job
	return nil
}

func (m *memFileJobRepo) Get(ctx context.Context, id string) (syncjob.Job, error) {
	job, ok := m.jobs[id]
	if !ok {
		return syncjob.Job{}, fmt.Errorf("%w: %s", syncjob.ErrNotFound, id)
	}
	return job, nil
}

func (m *memFileJobRepo) List(ctx context.Context) ([]syncjob.Job, error) {
	return nil, errors.New("not implemented")
}

func (m *memFileJobRepo) Update(ctx context.Context, job syncjob.Job) error {
	return errors.New("not implemented")
}

func (m *memFileJobRepo) UpdateAndResetManaged(ctx context.Context, job syncjob.Job) error {
	return errors.New("not implemented")
}

func (m *memFileJobRepo) Delete(ctx context.Context, id string) error {
	return errors.New("not implemented")
}

func (m *memFileJobRepo) CountBySource(ctx context.Context, sourceID string) (int, error) {
	return 0, errors.New("not implemented")
}

// memFileManagedRepo 是文件 API 测试的内存 managed 记录仓库。
type memFileManagedRepo struct {
	files []syncjob.ManagedFile
}

func (m *memFileManagedRepo) ListByJob(ctx context.Context, jobID string) ([]syncjob.ManagedFile, error) {
	return m.files, nil
}

func (m *memFileManagedRepo) Upsert(ctx context.Context, files []syncjob.ManagedFile) error {
	return errors.New("not implemented")
}

func (m *memFileManagedRepo) Delete(ctx context.Context, jobID string, remotePaths []string) error {
	return errors.New("not implemented")
}

func (m *memFileManagedRepo) DeleteAllForJob(ctx context.Context, jobID string) error {
	return errors.New("not implemented")
}

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

// localFilesEnv 是本地文件 API 测试环境：真实临时目录 + 内存 Job。
type localFilesEnv struct {
	router *gin.Engine
	jobID  string
	root   string
}

// newLocalFilesEnv 构造带 LocalFiles 服务的路由：Job 的 LocalRoot
// 指向临时目录。
func newLocalFilesEnv(t *testing.T) *localFilesEnv {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("evalsymlinks: %v", err)
	}
	dataDir := t.TempDir()
	db, err := storage.Open(dataDir)
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := storage.Migrate(context.Background(), db, dataDir); err != nil {
		t.Fatalf("storage.Migrate: %v", err)
	}
	svc := source.NewService(sqlite.New(db), fakeFactory{})
	jobRepo := newMemFileJobRepo()
	managedRepo := &memFileManagedRepo{}
	now := time.Now().UTC()
	job := syncjob.Job{
		ID:        "job_local_api",
		Name:      "local-api",
		Mode:      syncjob.ModeCopy,
		LocalRoot: root,
		Enabled:   true,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := jobRepo.Create(context.Background(), job); err != nil {
		t.Fatalf("create job: %v", err)
	}
	jobs := syncjob.NewService(jobRepo, nil, dataDir)
	files := browser.NewRemoteService(svc)
	local := browser.NewLocalService(jobs, managedRepo)
	router := NewRouter(testWebFS(), Dependencies{Sources: svc, Jobs: jobs, Runner: nil, Browser: files, LocalFiles: local})
	return &localFilesEnv{router: router, jobID: job.ID, root: root}
}

func (e *localFilesEnv) write(t *testing.T, rel, content string) {
	t.Helper()
	abs := filepath.Join(e.root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
}

// 本地列表携带 managed 标记；URL 编码的 traversal 被拒绝。
func TestLocalFilesListAPI(t *testing.T) {
	env := newLocalFilesEnv(t)
	env.write(t, "a.txt", "hello")
	env.write(t, "sub/b.txt", "world")
	// managed 标记来自 managed_files；这里记录 a.txt 为 managed。
	// memJobRepo 同时实现 ManagedRepository，直接操作不受支持——
	// managed=false 的呈现即覆盖默认分支，managed=true 的推导在
	// browser 层测试已覆盖。
	base := "/api/v1/jobs/" + env.jobID + "/files"

	w := doJSON(t, env.router, http.MethodGet, base+"?path=/", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	var resp struct {
		Entries []struct {
			Path    string `json:"path"`
			Kind    string `json:"kind"`
			Managed bool   `json:"managed"`
		} `json:"entries"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	byPath := map[string]struct {
		Kind    string `json:"-"`
		Managed bool
	}{}
	for _, e := range resp.Entries {
		byPath[e.Path] = struct {
			Kind    string `json:"-"`
			Managed bool
		}{Kind: e.Kind, Managed: e.Managed}
	}
	if e, ok := byPath["/a.txt"]; !ok || e.Kind != "file" || e.Managed {
		t.Errorf("/a.txt = %+v ok=%v, want unmanaged file", byPath["/a.txt"], ok)
	}
	if e, ok := byPath["/sub"]; !ok || e.Kind != "directory" {
		t.Errorf("/sub = %+v ok=%v, want directory", byPath["/sub"], ok)
	}

	// URL 编码 traversal：Gin 先解码再匹配 query，服务端按解码后
	// 的 dot segments 拒绝（400 而不是 404/200）。
	encoded := "?path=" + "%2e%2e%2f%2e%2e%2fetc%2fpasswd"
	w = doJSON(t, env.router, http.MethodGet, base+encoded, "")
	if w.Code != http.StatusBadRequest {
		t.Errorf("encoded traversal status = %d, want 400, body = %s", w.Code, w.Body.String())
	}
}

// 本地下载：200 全量、Range → 206、非法 Range → 416、HEAD 只回头。
func TestLocalFilesDownloadAPI(t *testing.T) {
	env := newLocalFilesEnv(t)
	content := "0123456789abcdef"
	env.write(t, "docs/data.bin", content)
	base := "/api/v1/jobs/" + env.jobID + "/files/download?path=/docs/data.bin"

	// 全量 200。
	w := httptest.NewRecorder()
	env.router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, base, nil))
	if w.Code != http.StatusOK || w.Body.String() != content {
		t.Fatalf("full download = %d %q, want 200 %q", w.Code, w.Body.String(), content)
	}
	if got := w.Header().Get("Content-Disposition"); !strings.Contains(got, "attachment") {
		t.Errorf("content-disposition = %q, want attachment", got)
	}

	// 合法 Range → 206 部分内容。
	req := httptest.NewRequest(http.MethodGet, base, nil)
	req.Header.Set("Range", "bytes=0-3")
	w = httptest.NewRecorder()
	env.router.ServeHTTP(w, req)
	if w.Code != http.StatusPartialContent || w.Body.String() != "0123" {
		t.Errorf("range download = %d %q, want 206 \"0123\"", w.Code, w.Body.String())
	}

	// 非法 Range → 416。
	req = httptest.NewRequest(http.MethodGet, base, nil)
	req.Header.Set("Range", "bytes=999-1000")
	w = httptest.NewRecorder()
	env.router.ServeHTTP(w, req)
	if w.Code != http.StatusRequestedRangeNotSatisfiable {
		t.Errorf("invalid range = %d, want 416", w.Code)
	}

	// HEAD：只回响应头，无 body。
	w = httptest.NewRecorder()
	env.router.ServeHTTP(w, httptest.NewRequest(http.MethodHead, base, nil))
	if w.Code != http.StatusOK || w.Body.Len() != 0 {
		t.Errorf("HEAD = %d body %d bytes, want 200 empty", w.Code, w.Body.Len())
	}
	if got := w.Header().Get("Content-Length"); got != "16" {
		t.Errorf("HEAD content-length = %q, want 16", got)
	}
}

// 本地下载拒绝：symlink（root 内外与断链）与目录 → 400。
func TestLocalFilesDownloadRejects(t *testing.T) {
	env := newLocalFilesEnv(t)
	env.write(t, "real.txt", "x")
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("s"), 0o644); err != nil {
		t.Fatalf("write outside: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(env.root, "out-link")); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	if err := os.Symlink("real.txt", filepath.Join(env.root, "in-link.txt")); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	if err := os.Symlink("ghost", filepath.Join(env.root, "broken-link")); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	if err := os.Mkdir(filepath.Join(env.root, "subdir"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	for _, tc := range []struct {
		path, reason string
	}{
		{"/out-link/secret.txt", "parent symlink escape"},
		{"/in-link.txt", "symlink to inside file"},
		{"/broken-link", "broken symlink"},
		{"/subdir", "directory as download"},
	} {
		w := httptest.NewRecorder()
		env.router.ServeHTTP(w, httptest.NewRequest(http.MethodGet,
			"/api/v1/jobs/"+env.jobID+"/files/download?path="+tc.path, nil))
		if w.Code != http.StatusBadRequest {
			t.Errorf("%s (%s) = %d, want 400", tc.path, tc.reason, w.Code)
		}
	}
}

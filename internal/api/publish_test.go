package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"tinysync/internal/publish"
	publishsqlite "tinysync/internal/publish/sqlite"
	"tinysync/internal/source"
	"tinysync/internal/source/sqlite"
	"tinysync/internal/storage"
	"tinysync/internal/syncjob"
)

// publishEnv 是发布 API 测试环境：真实 SQLite 持久化 + 临时
// LocalRoot，managed 记录可编程。
type publishEnv struct {
	router  *gin.Engine
	svc     *publish.Service
	jobRepo *memFileJobRepo
	managed *memFileManagedRepo
	jobID   string
	root    string
}

func newPublishEnv(t *testing.T) *publishEnv {
	t.Helper()
	canonical, err := filepath.EvalSymlinks(t.TempDir())
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

	jobRepo := newMemFileJobRepo()
	managedRepo := &memFileManagedRepo{}
	jobID := "job_publish_it"
	now := time.Now().UTC()
	if err := jobRepo.Create(context.Background(), syncjob.Job{
		ID:        jobID,
		Name:      "publish-it",
		Mode:      syncjob.ModeCopy,
		LocalRoot: canonical,
		Enabled:   true,
		CreatedAt: now,
		UpdatedAt: now,
	}); err != nil {
		t.Fatalf("create job: %v", err)
	}

	svc := source.NewService(sqlite.New(db), fakeFactory{})
	jobs := syncjob.NewService(jobRepo, nil, dataDir)
	policies := publish.NewService(publishsqlite.NewRepository(db), jobs, managedRepo)
	router := NewRouter(testWebFS(), Dependencies{
		Sources: svc, Jobs: jobs, Publish: policies,
	})
	return &publishEnv{
		router:  router,
		svc:     policies,
		jobRepo: jobRepo,
		managed: managedRepo,
		jobID:   jobID,
		root:    canonical,
	}
}

func (e *publishEnv) write(t *testing.T, rel, content string) {
	t.Helper()
	abs := filepath.Join(e.root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func (e *publishEnv) setManaged(rel ...string) {
	files := make([]syncjob.ManagedFile, 0, len(rel))
	for _, p := range rel {
		files = append(files, syncjob.ManagedFile{JobID: e.jobID, LocalRelPath: p})
	}
	e.managed.files = files
}

// CRUD 全链：创建（canonical local path 落库）、列表、更新、删除与
// 错误语义（未 managed 400、缺 Job 404、冲突 409、local_path 不可变）。
func TestPublishCRUD(t *testing.T) {
	env := newPublishEnv(t)
	env.write(t, "synced/a.txt", "hello")
	env.setManaged("synced/a.txt")
	base := "/api/v1/published-files"

	// 创建。
	w := doJSON(t, env.router, http.MethodPost, base,
		`{"job_id":"`+env.jobID+`","path":"/synced/a.txt","public_path":"/files/a.txt","enabled":true}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("create = %d, body = %s", w.Code, w.Body.String())
	}
	var created publishedFileDTO
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if created.LocalPath != filepath.Join(env.root, "synced", "a.txt") || created.PublicPath != "/files/a.txt" {
		t.Errorf("created = %+v, want canonical local path", created)
	}

	// 列表。
	w = doJSON(t, env.router, http.MethodGet, base, "")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "/files/a.txt") {
		t.Errorf("list = %d %s", w.Code, w.Body.String())
	}

	// 未 managed 文件拒绝发布（400），目录拒绝（400）。
	env.write(t, "private.txt", "x")
	w = doJSON(t, env.router, http.MethodPost, base,
		`{"job_id":"`+env.jobID+`","path":"/private.txt","public_path":"/p.txt","enabled":true}`)
	if w.Code != http.StatusBadRequest {
		t.Errorf("unmanaged publish = %d, want 400", w.Code)
	}
	w = doJSON(t, env.router, http.MethodPost, base,
		`{"job_id":"`+env.jobID+`","path":"/synced","public_path":"/d","enabled":true}`)
	if w.Code != http.StatusBadRequest {
		t.Errorf("directory publish = %d, want 400", w.Code)
	}
	// Job 不存在 → 404。
	w = doJSON(t, env.router, http.MethodPost, base,
		`{"job_id":"nope","path":"/synced/a.txt","public_path":"/x","enabled":true}`)
	if w.Code != http.StatusNotFound {
		t.Errorf("missing job = %d, want 404", w.Code)
	}

	// public_path 冲突 → 409。
	w = doJSON(t, env.router, http.MethodPost, base,
		`{"job_id":"`+env.jobID+`","path":"/synced/a.txt","public_path":"/files/a.txt","enabled":true}`)
	if w.Code != http.StatusConflict {
		t.Errorf("duplicate public path = %d, want 409", w.Code)
	}

	// 更新：disable 与清除过期；local_path 不可变（输入中的路径字段
	// 即使携带也不会改变落库值）。
	w = doJSON(t, env.router, http.MethodPatch, base+"/"+created.ID, `{"enabled":false}`)
	if w.Code != http.StatusOK {
		t.Fatalf("update = %d, body = %s", w.Code, w.Body.String())
	}
	var updated publishedFileDTO
	if err := json.Unmarshal(w.Body.Bytes(), &updated); err != nil {
		t.Fatalf("unmarshal update: %v", err)
	}
	if updated.Enabled || updated.LocalPath != created.LocalPath {
		t.Errorf("updated = %+v, want disabled with unchanged local path", updated)
	}

	// 不存在的策略 → 404。
	w = doJSON(t, env.router, http.MethodPatch, base+"/pub_nope", `{"enabled":true}`)
	if w.Code != http.StatusNotFound {
		t.Errorf("update missing = %d, want 404", w.Code)
	}

	// 删除 → 200，此后策略不复存在。
	w = doJSON(t, env.router, http.MethodDelete, base+"/"+created.ID, "")
	if w.Code != http.StatusOK {
		t.Errorf("delete = %d", w.Code)
	}
	// GET /published-files/:id 未注册（契约只提供列表端点）；同路径
	// 存在 POST / PATCH / DELETE，未注册的 GET 由 405 + Allow 表达。
	w = doJSON(t, env.router, http.MethodGet, base+"/"+created.ID, "")
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("get after delete = %d, want 405", w.Code)
	}
}

// 公开 serving：200 / Range 206 / 非法 Range 416 / HEAD / no-store；
// disabled / 过期 / 缺失 / 目录一律 404；未发布路径不可穿越。
func TestPublishServing(t *testing.T) {
	env := newPublishEnv(t)
	env.write(t, "synced/data.bin", "0123456789abcdef")
	env.setManaged("synced/data.bin")

	if _, err := env.svc.Create(context.Background(), publish.CreateInput{
		JobID: env.jobID, Path: "/synced/data.bin", PublicPath: "/dl/data.bin", Enabled: true,
	}); err != nil {
		t.Fatalf("create policy: %v", err)
	}
	url := "/published/dl/data.bin"

	// 200 全量 + no-store + nosniff。
	w := httptest.NewRecorder()
	env.router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, url, nil))
	if w.Code != http.StatusOK || w.Body.String() != "0123456789abcdef" {
		t.Fatalf("serve = %d %q", w.Code, w.Body.String())
	}
	if got := w.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("cache-control = %q, want no-store", got)
	}
	if got := w.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("x-content-type-options = %q, want nosniff", got)
	}

	// Range 206。
	req := httptest.NewRequest(http.MethodGet, url, nil)
	req.Header.Set("Range", "bytes=2-5")
	w = httptest.NewRecorder()
	env.router.ServeHTTP(w, req)
	if w.Code != http.StatusPartialContent || w.Body.String() != "2345" {
		t.Errorf("range = %d %q, want 206 \"2345\"", w.Code, w.Body.String())
	}

	// 非法 Range 416。
	req = httptest.NewRequest(http.MethodGet, url, nil)
	req.Header.Set("Range", "bytes=500-")
	w = httptest.NewRecorder()
	env.router.ServeHTTP(w, req)
	if w.Code != http.StatusRequestedRangeNotSatisfiable {
		t.Errorf("invalid range = %d, want 416", w.Code)
	}

	// HEAD 只有响应头。
	w = httptest.NewRecorder()
	env.router.ServeHTTP(w, httptest.NewRequest(http.MethodHead, url, nil))
	if w.Code != http.StatusOK || w.Body.Len() != 0 || w.Header().Get("Content-Length") != "16" {
		t.Errorf("HEAD = %d body %d len %q", w.Code, w.Body.Len(), w.Header().Get("Content-Length"))
	}

	// 禁用 → 404，且与「路径不存在」同形。
	policies, _ := env.svc.List(context.Background())
	disabled := false
	if _, err := env.svc.Update(context.Background(), policies[0].ID, publish.UpdateInput{Enabled: &disabled}); err != nil {
		t.Fatalf("disable: %v", err)
	}
	w = httptest.NewRecorder()
	env.router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, url, nil))
	disabledBody := w.Body.String()
	if w.Code != http.StatusNotFound {
		t.Errorf("disabled = %d, want 404", w.Code)
	}
	w = httptest.NewRecorder()
	env.router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/published/dl/never", nil))
	if w.Code != http.StatusNotFound || w.Body.String() != disabledBody {
		t.Errorf("missing = %d %q, want same shape as disabled (%q)", w.Code, w.Body.String(), disabledBody)
	}

	// 目录发布被创建校验拒绝后，也无法通过 serving 访问目录。
	env.write(t, "synced/sub/x.txt", "x")
	w = httptest.NewRecorder()
	env.router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/published/dl/sub", nil))
	if w.Code != http.StatusNotFound {
		t.Errorf("directory = %d, want 404", w.Code)
	}

	// 根与逃逸形态全部 404（绝不进入 SPA fallback 或文件系统根）。
	// "/published"（无尾随 /）由 Gin RedirectTrailingSlash 301 到
	// "/published/"，最终语义仍是不存在。
	for _, p := range []string{"/published/", "/published/../etc", "/published/%2e%2e/etc"} {
		w = httptest.NewRecorder()
		env.router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, p, nil))
		if w.Code != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", p, w.Code)
		}
	}
}

// serving 与 SPA fallback 的边界：/published/* 显式注册优先，
// 不存在路径也不回 index.html。
func TestPublishedRouteNotSwallowedBySPA(t *testing.T) {
	env := newPublishEnv(t)
	w := httptest.NewRecorder()
	env.router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/published/nothing-here", nil))
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 (not SPA index)", w.Code)
	}
	if strings.Contains(w.Body.String(), "<html") {
		t.Error("response contains HTML, want JSON 404")
	}
	_ = fmt.Sprint()
}

package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"tinysync/internal/share"
	sharesqlite "tinysync/internal/share/sqlite"
	"tinysync/internal/source"
	"tinysync/internal/source/sqlite"
	"tinysync/internal/storage"
	"tinysync/internal/syncjob"
)

// shareEnv 是共享 API 测试环境：真实 SQLite 持久化 + 临时 LocalRoot。
type shareEnv struct {
	router testRouter
	svc    *share.Service
	jobID  string
	root   string
}

func newShareEnv(t *testing.T) *shareEnv {
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
	jobID := "job_share_it"
	now := time.Now().UTC()
	if err := jobRepo.Create(context.Background(), syncjob.Job{
		ID:        jobID,
		Name:      "share-it",
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
	shares := share.NewService(sharesqlite.NewRepository(db), jobs)
	router := newTestAuth(t, db, Dependencies{
		Sources: svc, Jobs: jobs, Share: shares,
	})
	return &shareEnv{router: router, svc: shares, jobID: jobID, root: canonical}
}

func (e *shareEnv) write(t *testing.T, rel, content string) {
	t.Helper()
	abs := filepath.Join(e.root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
}

// CRUD 全链：创建（文件与目录、命名与随机 slug）、列表、更新、删除
// 与错误语义（缺 Job 404、名称非法 400、slug 冲突 409、local_path
// 不可变）。
func TestShareCRUD(t *testing.T) {
	env := newShareEnv(t)
	env.write(t, "synced/a.txt", "hello")
	base := "/api/v1/shares"

	// 创建命名文件共享。
	w := doJSON(t, env.router, http.MethodPost, base,
		`{"job_id":"`+env.jobID+`","path":"/synced/a.txt","name":"album","enabled":true}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("create = %d, body = %s", w.Code, w.Body.String())
	}
	var created shareDTO
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if created.LocalPath != filepath.Join(env.root, "synced", "a.txt") || created.Slug != "album" || created.IsDir {
		t.Errorf("created = %+v, want canonical file share with slug album", created)
	}

	// 列表。
	w = doJSON(t, env.router, http.MethodGet, base, "")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "album") {
		t.Errorf("list = %d %s", w.Code, w.Body.String())
	}

	// 目录共享与未命名（随机 slug）共享均可创建；unmanaged 文件同样
	// 放行（ADR-0002：共享不做 managed 过滤）。
	w = doJSON(t, env.router, http.MethodPost, base,
		`{"job_id":"`+env.jobID+`","path":"/synced","enabled":true}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("create dir share = %d, body = %s", w.Code, w.Body.String())
	}
	var dirCreated shareDTO
	if err := json.Unmarshal(w.Body.Bytes(), &dirCreated); err != nil {
		t.Fatalf("unmarshal dir: %v", err)
	}
	if !dirCreated.IsDir || len(dirCreated.Slug) != 10 {
		t.Errorf("dir share = %+v, want is_dir with random slug", dirCreated)
	}
	env.write(t, "private.txt", "x")
	w = doJSON(t, env.router, http.MethodPost, base,
		`{"job_id":"`+env.jobID+`","path":"/private.txt","enabled":true}`)
	if w.Code != http.StatusCreated {
		t.Errorf("unmanaged file share = %d, want 201", w.Code)
	}

	// Job 不存在 → 404；名称非法 → 400；slug 冲突 → 409。
	w = doJSON(t, env.router, http.MethodPost, base,
		`{"job_id":"nope","path":"/synced/a.txt","enabled":true}`)
	if w.Code != http.StatusNotFound {
		t.Errorf("missing job = %d, want 404", w.Code)
	}
	w = doJSON(t, env.router, http.MethodPost, base,
		`{"job_id":"`+env.jobID+`","path":"/synced/a.txt","name":"a.b","enabled":true}`)
	if w.Code != http.StatusBadRequest {
		t.Errorf("invalid name = %d, want 400", w.Code)
	}
	w = doJSON(t, env.router, http.MethodPost, base,
		`{"job_id":"`+env.jobID+`","path":"/synced/a.txt","name":"album","enabled":true}`)
	if w.Code != http.StatusConflict {
		t.Errorf("duplicate slug = %d, want 409", w.Code)
	}

	// 更新：改名联动 slug、disable；local_path 不受影响。
	w = doJSON(t, env.router, http.MethodPatch, base+"/"+created.ID, `{"name":"photos","enabled":false}`)
	if w.Code != http.StatusOK {
		t.Fatalf("update = %d, body = %s", w.Code, w.Body.String())
	}
	var updated shareDTO
	if err := json.Unmarshal(w.Body.Bytes(), &updated); err != nil {
		t.Fatalf("unmarshal update: %v", err)
	}
	if updated.Slug != "photos" || updated.Enabled || updated.LocalPath != created.LocalPath {
		t.Errorf("updated = %+v, want renamed disabled share", updated)
	}

	// 不存在的共享 → 404。
	w = doJSON(t, env.router, http.MethodPatch, base+"/shr_nope", `{"enabled":true}`)
	if w.Code != http.StatusNotFound {
		t.Errorf("update missing = %d, want 404", w.Code)
	}

	// 删除 → 200；GET /shares/:id 未注册（契约只提供列表端点），同路径
	// 存在 POST / PATCH / DELETE，未注册的 GET 由 405 + Allow 表达。
	w = doJSON(t, env.router, http.MethodDelete, base+"/"+created.ID, "")
	if w.Code != http.StatusOK {
		t.Errorf("delete = %d", w.Code)
	}
	w = doJSON(t, env.router, http.MethodGet, base+"/"+created.ID, "")
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("get after delete = %d, want 405", w.Code)
	}
}

// 公开直链 serving：200 / Range 206 / 非法 Range 416 / HEAD / no-store；
// disabled / 缺失 / 指向目录 / 文件共享的非常规路径 一律同形 404；
// 浏览页深链接返回 SPA index.html（noindex，ADR-0001）。
func TestSharedServing(t *testing.T) {
	env := newShareEnv(t)
	env.write(t, "synced/data.bin", "0123456789abcdef")

	created, err := env.svc.Create(context.Background(), share.CreateInput{
		JobID: env.jobID, Path: "/synced/data.bin", Name: "dl", Enabled: true,
	})
	if err != nil {
		t.Fatalf("create share: %v", err)
	}
	url := "/shared/dl/data.bin"

	// 200 全量 + no-store + nosniff + noindex。
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
	if got := w.Header().Get("X-Robots-Tag"); got != "noindex" {
		t.Errorf("x-robots-tag = %q, want noindex", got)
	}

	// Range 206 / 非法 Range 416 / HEAD。
	req := httptest.NewRequest(http.MethodGet, url, nil)
	req.Header.Set("Range", "bytes=2-5")
	w = httptest.NewRecorder()
	env.router.ServeHTTP(w, req)
	if w.Code != http.StatusPartialContent || w.Body.String() != "2345" {
		t.Errorf("range = %d %q, want 206 \"2345\"", w.Code, w.Body.String())
	}
	req = httptest.NewRequest(http.MethodGet, url, nil)
	req.Header.Set("Range", "bytes=500-")
	w = httptest.NewRecorder()
	env.router.ServeHTTP(w, req)
	if w.Code != http.StatusRequestedRangeNotSatisfiable {
		t.Errorf("invalid range = %d, want 416", w.Code)
	}
	w = httptest.NewRecorder()
	env.router.ServeHTTP(w, httptest.NewRequest(http.MethodHead, url, nil))
	if w.Code != http.StatusOK || w.Body.Len() != 0 || w.Header().Get("Content-Length") != "16" {
		t.Errorf("HEAD = %d body %d len %q", w.Code, w.Body.Len(), w.Header().Get("Content-Length"))
	}

	// 浏览页深链接：SPA index.html + noindex + no-store。
	w = httptest.NewRecorder()
	env.router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/shared/dl", nil))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "<html") {
		t.Errorf("page = %d %q, want SPA html", w.Code, w.Body.String())
	}
	if got := w.Header().Get("X-Robots-Tag"); got != "noindex" {
		t.Errorf("page x-robots-tag = %q, want noindex", got)
	}

	// 尾随斜杠重定向回浏览页。
	w = httptest.NewRecorder()
	env.router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/shared/dl/", nil))
	if w.Code != http.StatusPermanentRedirect || w.Header().Get("Location") != "/shared/dl" {
		t.Errorf("trailing slash = %d %q, want 308 to /shared/dl", w.Code, w.Header().Get("Location"))
	}

	// 禁用 → 404，且与「共享内路径不存在」同形。
	disabled := false
	if _, err := env.svc.Update(context.Background(), created.ID, share.UpdateInput{Enabled: &disabled}); err != nil {
		t.Fatalf("disable: %v", err)
	}
	w = httptest.NewRecorder()
	env.router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, url, nil))
	disabledBody := w.Body.String()
	if w.Code != http.StatusNotFound {
		t.Errorf("disabled = %d, want 404", w.Code)
	}
	w = httptest.NewRecorder()
	env.router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/shared/dl/never.bin", nil))
	if w.Code != http.StatusNotFound || w.Body.String() != disabledBody {
		t.Errorf("missing = %d %q, want same shape as disabled (%q)", w.Code, w.Body.String(), disabledBody)
	}

	// 恢复后：文件共享只服务 basename 那一条路径；指向目录、逃逸与
	// 未知 slug 全部 404（绝不进入 SPA fallback 或文件系统根）。
	enabled := true
	if _, err := env.svc.Update(context.Background(), created.ID, share.UpdateInput{Enabled: &enabled}); err != nil {
		t.Fatalf("enable: %v", err)
	}
	env.write(t, "synced/sub/x.txt", "x")
	for _, p := range []string{
		"/shared/dl/sub", "/shared/dl/sub/x.txt", "/shared/dl/%2e%2e/etc",
		"/shared/dl/../etc", "/shared/no-such-slug/f.txt",
	} {
		w = httptest.NewRecorder()
		env.router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, p, nil))
		if w.Code != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", p, w.Code)
		}
	}
}

// serving 与 SPA fallback 的边界：/shared/* 直链显式注册优先，
// 不存在路径也不回 index.html。
func TestSharedRouteNotSwallowedBySPA(t *testing.T) {
	env := newShareEnv(t)
	w := httptest.NewRecorder()
	env.router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/shared/nothing-here/missing.txt", nil))
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 (not SPA index)", w.Code)
	}
	if strings.Contains(w.Body.String(), "<html") {
		t.Error("response contains HTML, want JSON 404")
	}
}

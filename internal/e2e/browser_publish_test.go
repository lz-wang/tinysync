package e2e

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"tinysync/internal/api"
	"tinysync/internal/browser"
	"tinysync/internal/publish"
	publishsqlite "tinysync/internal/publish/sqlite"
	"tinysync/internal/source"
	sourcesqlite "tinysync/internal/source/sqlite"
	"tinysync/internal/syncjob"
	jobsqlite "tinysync/internal/syncjob/sqlite"
)

// 文件浏览与发布的端到端覆盖：完整 HTTP 栈（Gin router + 真实
// SQLite + 真实同步 Runner + 协议 fixture）验证「远端浏览 → 同步 →
// 本地浏览 → 发布 → 公开 serving」链条与 confinement 语义。

// bridgedFactory 把协议 fixture 接到 source.Service 的 factory dispatch：
// OpenRemote 的统一入口经它获得 fixture 提供的真实协议栈 Remote。
type bridgedFactory struct{ fixture matrixRemote }

func (f bridgedFactory) Type() source.Type { return source.TypeWebDAV }

func (f bridgedFactory) Create(ctx context.Context, s source.Source, credentials source.Credentials) (source.Remote, error) {
	return f.fixture.openRemote()
}

// browserEnv 是完整 HTTP 浏览/发布环境。
type browserEnv struct {
	t         *testing.T
	router    http.Handler
	db        *sql.DB
	dataDir   string
	jobRepo   *jobsqlite.Repository
	managed   *jobsqlite.ManagedRepository
	runner    *syncjob.Runner
	job       syncjob.Job
	localRoot string
	remote    matrixRemote
}

func newBrowserEnv(t *testing.T, fixture matrixRemote) *browserEnv {
	t.Helper()
	dataDir := t.TempDir()
	db := openDB(t, dataDir)
	t.Cleanup(func() { _ = db.Close() })

	now := time.Unix(1757879400, 0).UTC()
	srcRepo := sourcesqlite.New(db)
	if err := srcRepo.Create(context.Background(), source.Source{
		ID:              "src_browser",
		Name:            "browser",
		Type:            source.TypeWebDAV,
		Config:          source.Config{WebDAV: &source.WebDAVConfig{Endpoint: "https://browser.invalid/dav"}},
		CredentialState: source.CredentialState{WebDAV: &source.WebDAVCredentialState{}},
		Enabled:         true,
		CreatedAt:       now,
		UpdatedAt:       now,
	}, source.Credentials{}); err != nil {
		t.Fatalf("create source row: %v", err)
	}

	jobRepo := jobsqlite.NewRepository(db)
	managed := jobsqlite.NewManagedRepository(db)
	sources := source.NewService(srcRepo, bridgedFactory{fixture: fixture})
	jobs := syncjob.NewService(jobRepo, sources, dataDir)
	runner := syncjob.NewRunner(jobRepo, managed, matrixGateway{
		src:        source.Source{ID: "src_browser", Name: "browser", Type: source.TypeWebDAV, Enabled: true},
		openRemote: fixture.openRemote,
	}, jobsqlite.NewRunRepository(db))

	files := browser.NewRemoteService(sources)
	localFiles := browser.NewLocalService(jobs, managed)
	policies := publish.NewService(publishsqlite.NewRepository(db), jobs, managed)

	router := api.NewRouter(fstest.MapFS{}, api.Dependencies{
		Sources:    sources,
		Jobs:       jobs,
		Runner:     runner,
		Browser:    files,
		LocalFiles: localFiles,
		Publish:    policies,
	})
	return &browserEnv{
		t:         t,
		router:    router,
		db:        db,
		dataDir:   dataDir,
		jobRepo:   jobRepo,
		managed:   managed,
		runner:    runner,
		localRoot: t.TempDir(),
		remote:    fixture,
	}
}

func (e *browserEnv) createJob(mode syncjob.Mode) {
	e.t.Helper()
	id, err := syncjob.NewID()
	if err != nil {
		e.t.Fatalf("new job id: %v", err)
	}
	// LocalRoot 按领域规则 canonicalize（abs + EvalSymlinks）。
	canonical, err := filepath.EvalSymlinks(e.localRoot)
	if err != nil {
		e.t.Fatalf("evalsymlinks local root: %v", err)
	}
	e.localRoot = canonical
	now := time.Unix(1757879400, 0).UTC()
	e.job = syncjob.Job{
		ID: id, Name: "job-" + id, SourceID: "src_browser",
		RemoteRoot: "/", LocalRoot: e.localRoot,
		Mode: mode, Enabled: true,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := e.jobRepo.Create(context.Background(), e.job); err != nil {
		e.t.Fatalf("create job: %v", err)
	}
}

func (e *browserEnv) sync() {
	e.t.Helper()
	runID, err := e.runner.Start(context.Background(), e.job.ID)
	if err != nil {
		e.t.Fatalf("Start: %v", err)
	}
	status, err := e.runner.Wait(context.Background(), runID)
	if err != nil {
		e.t.Fatalf("Wait: %v", err)
	}
	if status.State != syncjob.RunSucceeded {
		e.t.Fatalf("run state = %s (%s), want succeeded", status.State, status.Error)
	}
}

// get 执行 GET 请求并返回 recorder。
func (e *browserEnv) get(path string) *httptest.ResponseRecorder {
	e.t.Helper()
	w := httptest.NewRecorder()
	e.router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
	return w
}

// doJSON 执行带 JSON body 的请求。
func (e *browserEnv) doJSON(method, path, body string) *httptest.ResponseRecorder {
	e.t.Helper()
	w := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	e.router.ServeHTTP(w, req)
	return w
}

// TestRemoteBrowserAcrossProtocols：三协议统一执行 browse root /
// browse child / stat / download / pagination / invalid logical path。
func TestRemoteBrowserAcrossProtocols(t *testing.T) {
	for _, tc := range protocolFixtures() {
		t.Run(tc.name, func(t *testing.T) {
			remote := tc.fixture(t)
			e := newBrowserEnv(t, remote)

			// 远端数据：根层 3 个条目与 /docs 子目录（目录由 fixture
			// 预建）；/docs 内 6 个条目用于分页（>limit）。
			remote.put(t, "/root.txt", "root-content")
			remote.put(t, "/robots.txt", "user-agent: *")
			remote.put(t, "/docs/report.txt", "report-content")
			for i := 0; i < 5; i++ {
				remote.put(t, fmt.Sprintf("/docs/file-%02d.txt", i), fmt.Sprintf("content-%d", i))
			}

			// browse root：limit=2 分页拉取（3 条目 → 2 页），验证
			// cursor 链聚合完整、无重复无缺失。
			seen := map[string]bool{}
			cursor := ""
			pages := 0
			for {
				w := e.get("/api/v1/sources/src_browser/files?path=/&limit=2" + func() string {
					if cursor == "" {
						return ""
					}
					return "&cursor=" + cursor
				}())
				if w.Code != http.StatusOK {
					t.Fatalf("browse root page %d = %d, body %s", pages, w.Code, w.Body.String())
				}
				var page struct {
					Path       string `json:"path"`
					Entries    []struct {
						Path string `json:"path"`
						Kind string `json:"kind"`
					} `json:"entries"`
					NextCursor string `json:"next_cursor"`
				}
				if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
					t.Fatalf("unmarshal page: %v", err)
				}
				if page.Path != "/" {
					t.Errorf("page path = %q, want /", page.Path)
				}
				if len(page.Entries) > 2 {
					t.Errorf("page %d entries = %d, want <= limit 2", pages, len(page.Entries))
				}
				for _, entry := range page.Entries {
					if seen[entry.Path] {
						t.Errorf("duplicated entry %s across pages", entry.Path)
					}
					seen[entry.Path] = true
				}
				pages++
				if page.NextCursor == "" {
					break
				}
				cursor = page.NextCursor
			}
			if pages != 2 {
				t.Errorf("root pages = %d, want 2 (3 entries, limit 2)", pages)
			}
			for _, want := range []string{"/root.txt", "/robots.txt", "/docs"} {
				if !seen[want] {
					t.Errorf("browse root missing %s, got %v", want, seen)
				}
			}

			// browse child + 多页分页：/docs 有 6 个条目，limit=2 →
			// 3 页，无重复无缺失。
			childSeen := map[string]bool{}
			cursor = ""
			pages = 0
			for {
				w := e.get("/api/v1/sources/src_browser/files?path=/docs&limit=2" + func() string {
					if cursor == "" {
						return ""
					}
					return "&cursor=" + cursor
				}())
				if w.Code != http.StatusOK {
					t.Fatalf("browse child page %d = %d", pages, w.Code)
				}
				var page struct {
					Entries    []struct {
						Path string `json:"path"`
					} `json:"entries"`
					NextCursor string `json:"next_cursor"`
				}
				if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
					t.Fatalf("unmarshal child page: %v", err)
				}
				for _, entry := range page.Entries {
					if childSeen[entry.Path] {
						t.Errorf("duplicated child entry %s", entry.Path)
					}
					childSeen[entry.Path] = true
				}
				pages++
				if page.NextCursor == "" {
					break
				}
				cursor = page.NextCursor
			}
			if pages != 3 {
				t.Errorf("child pages = %d, want 3 (6 entries, limit 2)", pages)
			}
			if len(childSeen) != 6 {
				t.Errorf("child entries = %d, want 6 with no duplicates", len(childSeen))
			}
			for i := 0; i < 5; i++ {
				if !childSeen[fmt.Sprintf("/docs/file-%02d.txt", i)] {
					t.Errorf("missing /docs/file-%02d.txt across pages", i)
				}
			}

			// stat。
			w := e.get("/api/v1/sources/src_browser/files/stat?path=/docs/report.txt")
			if w.Code != http.StatusOK {
				t.Fatalf("stat = %d, body %s", w.Code, w.Body.String())
			}
			var stat struct {
				Kind string `json:"kind"`
				Size int64  `json:"size"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &stat); err != nil {
				t.Fatalf("unmarshal stat: %v", err)
			}
			if stat.Kind != "file" || stat.Size != int64(len("report-content")) {
				t.Errorf("stat = %+v, want file size %d", stat, len("report-content"))
			}

			// download：内容一致且带 attachment 处置。
			w = e.get("/api/v1/sources/src_browser/files/download?path=/docs/report.txt")
			if w.Code != http.StatusOK || w.Body.String() != "report-content" {
				t.Errorf("download = %d %q", w.Code, w.Body.String())
			}
			if disposition := w.Header().Get("Content-Disposition"); !strings.Contains(disposition, "attachment") {
				t.Errorf("content-disposition = %q, want attachment", disposition)
			}

			// invalid logical path → 400。
			w = e.get("/api/v1/sources/src_browser/files?path=/a/../b")
			if w.Code != http.StatusBadRequest {
				t.Errorf("invalid path = %d, want 400", w.Code)
			}

			// source not found → 404。
			w = e.get("/api/v1/sources/src_missing/files?path=/")
			if w.Code != http.StatusNotFound {
				t.Errorf("missing source = %d, want 404", w.Code)
			}
		})
	}
}

// TestLocalBrowserConfinement：managed / unmanaged 标记、download
// （Range / HEAD / MIME / disposition）、traversal 与 symlink 拒绝、
// 目录下载拒绝。
func TestLocalBrowserConfinement(t *testing.T) {
	remote := newWebDAVFixture(t)
	e := newBrowserEnv(t, remote)

	remote.put(t, "/managed.txt", "managed-content")
	remote.put(t, "/docs/inner.txt", "inner")
	e.createJob(syncjob.ModeCopy)
	e.sync()

	// unmanaged 文件（目录中先于 TinySync 存在）。
	unmanaged := filepath.Join(e.localRoot, "unmanaged.txt")
	if err := os.WriteFile(unmanaged, []byte("bystander"), 0o644); err != nil {
		t.Fatalf("write unmanaged: %v", err)
	}
	// symlink：root 内与 root 外。
	if err := os.Symlink(filepath.Join(e.localRoot, "managed.txt"), filepath.Join(e.localRoot, "inside-link")); err != nil {
		t.Fatalf("symlink inside: %v", err)
	}
	outside := t.TempDir()
	secretPath := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(secretPath, []byte("secret"), 0o644); err != nil {
		t.Fatalf("write outside secret: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(e.localRoot, "outside-link")); err != nil {
		t.Fatalf("symlink outside: %v", err)
	}

	// list：managed / unmanaged 标记与 symlink 呈现。
	w := e.get("/api/v1/jobs/" + e.job.ID + "/files?path=/")
	if w.Code != http.StatusOK {
		t.Fatalf("local list = %d, body %s", w.Code, w.Body.String())
	}
	var page struct {
		Entries []struct {
			Path    string `json:"path"`
			Kind    string `json:"kind"`
			Managed bool   `json:"managed"`
		} `json:"entries"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	byPath := map[string]struct {
		kind    string
		managed bool
	}{}
	for _, entry := range page.Entries {
		byPath[entry.Path] = struct {
			kind    string
			managed bool
		}{entry.Kind, entry.Managed}
	}
	if entry, ok := byPath["/managed.txt"]; !ok || entry.kind != "file" || !entry.managed {
		t.Errorf("/managed.txt = %+v ok %v, want managed file", byPath["/managed.txt"], ok)
	}
	if entry, ok := byPath["/unmanaged.txt"]; !ok || entry.managed {
		t.Errorf("/unmanaged.txt = %+v ok %v, want unmanaged", byPath["/unmanaged.txt"], ok)
	}
	if entry, ok := byPath["/docs"]; !ok || entry.kind != "directory" {
		t.Errorf("/nested = %+v ok %v, want directory", byPath["/docs"], ok)
	}
	if entry, ok := byPath["/inside-link"]; !ok || entry.kind != "symlink" {
		t.Errorf("/inside-link = %+v ok %v, want symlink", byPath["/inside-link"], ok)
	}

	base := "/api/v1/jobs/" + e.job.ID

	// download：Range / HEAD / MIME / disposition。
	w = e.get(base + "/files/download?path=/managed.txt")
	if w.Code != http.StatusOK || w.Body.String() != "managed-content" {
		t.Fatalf("download = %d %q", w.Code, w.Body.String())
	}
	if got := w.Header().Get("Content-Type"); got != "text/plain; charset=utf-8" {
		t.Errorf("content-type = %q", got)
	}
	if got := w.Header().Get("Content-Disposition"); !strings.Contains(got, "attachment") {
		t.Errorf("disposition = %q", got)
	}
	req := httptest.NewRequest(http.MethodGet, base+"/files/download?path=/managed.txt", nil)
	req.Header.Set("Range", "bytes=0-6")
	w = httptest.NewRecorder()
	e.router.ServeHTTP(w, req)
	if w.Code != http.StatusPartialContent || w.Body.String() != "managed" {
		t.Errorf("range = %d %q, want 206 \"managed\"", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	e.router.ServeHTTP(w, httptest.NewRequest(http.MethodHead, base+"/files/download?path=/managed.txt", nil))
	if w.Code != http.StatusOK || w.Body.Len() != 0 {
		t.Errorf("HEAD = %d body %d", w.Code, w.Body.Len())
	}

	// 拒绝矩阵：traversal（含 URL 编码形态）、symlink、目录下载、
	// 不存在的 job。
	for _, tc := range []struct {
		path string
		want int
	}{
		{"/files?path=/../x", http.StatusBadRequest},
		{"/files?path=%2f%2e%2e%2f%2e%2e%2fetc%2fpasswd", http.StatusBadRequest},
		{"/files/download?path=/../managed.txt", http.StatusBadRequest},
		{"/files/download?path=/inside-link", http.StatusBadRequest},
		{"/files/download?path=/outside-link/secret.txt", http.StatusBadRequest},
		{"/files/download?path=/outside-link/ghost", http.StatusNotFound},
		{"/files/download?path=/docs", http.StatusBadRequest},
		{"/files?path=/", http.StatusNotFound},
	} {
		target := base
		if tc.want == http.StatusNotFound {
			target = "/api/v1/jobs/job_missing"
		}
		w = e.get(target + tc.path)
		if w.Code != tc.want {
			t.Errorf("GET %s%s = %d, want %d", target, tc.path, w.Code, tc.want)
		}
	}
}

// TestPublishLifecycle：完整链 remote → sync → managed local file →
// create publish policy → GET published URL → Range / HEAD；disable /
// expire / Mirror delete → 404；冲突 409；unmanaged / traversal /
// symlink escape 拒绝；重启后策略持久。
func TestPublishLifecycle(t *testing.T) {
	remote := newWebDAVFixture(t)
	e := newBrowserEnv(t, remote)

	remote.put(t, "/docs/published.txt", "public-content")
	e.createJob(syncjob.ModeMirror)
	e.sync()

	// 创建策略：经 REST，目标为 managed 文件。
	createBody := `{"job_id":"` + e.job.ID + `","path":"/docs/published.txt","public_path":"/share/published.txt","enabled":true}`
	w := e.doJSON(http.MethodPost, "/api/v1/published-files", createBody)
	if w.Code != http.StatusCreated {
		t.Fatalf("create policy = %d, body %s", w.Code, w.Body.String())
	}
	var policy struct {
		ID        string `json:"id"`
		LocalPath string `json:"local_path"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &policy); err != nil {
		t.Fatalf("unmarshal policy: %v", err)
	}
	// local_path 是 canonical 形态；macOS 的临时目录在 /var（/private/var
	// 的 symlink）之下，比较前对期望值做同一归一。
	canonicalRoot, err := filepath.EvalSymlinks(e.localRoot)
	if err != nil {
		t.Fatalf("evalsymlinks local root: %v", err)
	}
	if policy.LocalPath != filepath.Join(canonicalRoot, "docs", "published.txt") {
		t.Errorf("local_path = %q, want canonical path under local root", policy.LocalPath)
	}

	// 公开 serving：200 内容一致 + no-store；Range 206；HEAD 仅响应头。
	url := "/published/share/published.txt"
	w = e.get(url)
	if w.Code != http.StatusOK || w.Body.String() != "public-content" {
		t.Fatalf("published = %d %q", w.Code, w.Body.String())
	}
	if got := w.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("cache-control = %q", got)
	}
	req := httptest.NewRequest(http.MethodGet, url, nil)
	req.Header.Set("Range", "bytes=7-")
	w = httptest.NewRecorder()
	e.router.ServeHTTP(w, req)
	if w.Code != http.StatusPartialContent || w.Body.String() != "content" {
		t.Errorf("range = %d %q, want 206 \"content\"", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	e.router.ServeHTTP(w, httptest.NewRequest(http.MethodHead, url, nil))
	if w.Code != http.StatusOK || w.Body.Len() != 0 {
		t.Errorf("HEAD = %d body %d", w.Code, w.Body.Len())
	}

	// duplicate public_path → 409。
	w = e.doJSON(http.MethodPost, "/api/v1/published-files", createBody)
	if w.Code != http.StatusConflict {
		t.Errorf("duplicate = %d, want 409", w.Code)
	}

	// unmanaged / traversal / symlink escape 拒绝创建。
	unmanaged := filepath.Join(e.localRoot, "stranger.txt")
	if err := os.WriteFile(unmanaged, []byte("x"), 0o644); err != nil {
		t.Fatalf("write stranger: %v", err)
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(e.localRoot, "escape-link")); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	for _, tc := range []struct {
		name string
		body string
	}{
		{"unmanaged", `{"job_id":"` + e.job.ID + `","path":"/stranger.txt","public_path":"/s.txt","enabled":true}`},
		{"traversal", `{"job_id":"` + e.job.ID + `","path":"/../outside.txt","public_path":"/t.txt","enabled":true}`},
		{"symlink escape", `{"job_id":"` + e.job.ID + `","path":"/escape-link/secret.txt","public_path":"/e.txt","enabled":true}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := e.doJSON(http.MethodPost, "/api/v1/published-files", tc.body)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("create = %d, want 400, body %s", w.Code, w.Body.String())
			}
		})
	}

	// disable → 404 且与「路径不存在」响应同形。
	w = e.doJSON(http.MethodPatch, "/api/v1/published-files/"+policy.ID, `{"enabled":false}`)
	if w.Code != http.StatusOK {
		t.Fatalf("disable = %d, body %s", w.Code, w.Body.String())
	}
	disabled := e.get(url)
	if disabled.Code != http.StatusNotFound {
		t.Errorf("disabled = %d, want 404", disabled.Code)
	}
	missing := e.get("/published/share/never-existed")
	if missing.Code != http.StatusNotFound || missing.Body.String() != disabled.Body.String() {
		t.Errorf("missing = %d %q, want same shape as disabled", missing.Code, missing.Body.String())
	}

	// expire → 404：设置一个即将到来的过期时刻（RFC3339 秒级精度，
	// 留足余量避免四舍五入后不在未来）后等待越过。
	expiry := time.Now().Add(3 * time.Second).UTC().Format(time.RFC3339)
	w = e.doJSON(http.MethodPatch, "/api/v1/published-files/"+policy.ID,
		`{"enabled":true,"expires_at":"`+expiry+`"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("set expiry = %d, body %s", w.Code, w.Body.String())
	}
	time.Sleep(3500 * time.Millisecond)
	if w := e.get(url); w.Code != http.StatusNotFound {
		t.Errorf("expired = %d, want 404", w.Code)
	}

	// 恢复有效（清除过期）→ 200。
	w = e.doJSON(http.MethodPatch, "/api/v1/published-files/"+policy.ID, `{"expires_at":null}`)
	if w.Code != http.StatusOK {
		t.Fatalf("clear expiry = %d", w.Code)
	}
	if w := e.get(url); w.Code != http.StatusOK {
		t.Errorf("after clear expiry = %d, want 200", w.Code)
	}

	// Mirror delete → 文件被同步删除 → published URL 自然 404。
	remote.remove(t, "/docs/published.txt")
	e.sync()
	if _, err := os.Stat(policy.LocalPath); !os.IsNotExist(err) {
		t.Fatalf("local file stat = %v, want removed by mirror", err)
	}
	w = e.get(url)
	if w.Code != http.StatusNotFound {
		t.Errorf("after mirror delete = %d, want 404", w.Code)
	}

	// 重启持久：关闭并重新打开数据库，策略记录仍然存在。
	if err := e.db.Close(); err != nil {
		t.Fatalf("close db: %v", err)
	}
	db, err := sql.Open("sqlite", "file:"+filepath.Join(e.dataDir, "tinysync.db"))
	if err != nil {
		t.Fatalf("reopen db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Ping(); err != nil {
		t.Fatalf("ping reopened db: %v", err)
	}
	var count int
	if err := db.QueryRow("SELECT count(*) FROM published_files WHERE id = ?", policy.ID).Scan(&count); err != nil {
		t.Fatalf("query policy after restart: %v", err)
	}
	if count != 1 {
		t.Fatalf("policy rows after restart = %d, want 1", count)
	}
}

package e2e

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	xnetdav "golang.org/x/net/webdav"

	"tinysync/internal/app"
	"tinysync/internal/auth"
	authsqlite "tinysync/internal/auth/sqlite"
	"tinysync/internal/config"
	"tinysync/internal/source"
	sourcesqlite "tinysync/internal/source/sqlite"
	"tinysync/internal/source/webdav"
	"tinysync/internal/storage"
	"tinysync/internal/syncjob"
	jobsqlite "tinysync/internal/syncjob/sqlite"
)

// hardeningEnabled 报告重负载场景是否启用：10k 目录与 32 MiB 大传输
// 由 `make hardening`（TINYSYNC_HARDENING=1）运行，避免常规
// make test 超预算。
func hardeningEnabled() bool {
	return os.Getenv("TINYSYNC_HARDENING") != ""
}

// faultDAV 在真实 WebDAV 协议栈之外提供传输阶段故障注入：按路径
// 计数的 503、恒定 401 与 body 中途断连。
type faultDAV struct {
	fs  xnetdav.FileSystem
	srv *httptest.Server

	mu       sync.Mutex
	pending  map[string]int  // 路径 → 剩余 503 次数
	drop     map[string]int  // 路径 → 剩余中途断连次数
	authFail map[string]bool // 路径 → 恒定 401
	hits     map[string]int  // 路径 → GET 命中次数
}

// startFaultDAV 启动带故障注入的 WebDAV 服务。
func startFaultDAV(t *testing.T) *faultDAV {
	f := &faultDAV{
		fs:       xnetdav.NewMemFS(),
		pending:  map[string]int{},
		drop:     map[string]int{},
		authFail: map[string]bool{},
		hits:     map[string]int{},
	}
	inner := &xnetdav.Handler{FileSystem: f.fs, LockSystem: xnetdav.NewMemLS()}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			f.mu.Lock()
			f.hits[r.URL.Path]++
			switch {
			case f.pending[r.URL.Path] > 0:
				f.pending[r.URL.Path]--
				f.mu.Unlock()
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			case f.drop[r.URL.Path] > 0:
				f.drop[r.URL.Path]--
				f.mu.Unlock()
				// 声明大 Content-Length 后只写少量字节即断连：
				// 客户端在 body 中途收到 unexpected EOF。
				w.Header().Set("Content-Length", "65536")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write(make([]byte, 64))
				panic(http.ErrAbortHandler)
			case f.authFail[r.URL.Path]:
				f.mu.Unlock()
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			f.mu.Unlock()
		}
		inner.ServeHTTP(w, r)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *faultDAV) failNext(path string, n int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pending[path] = n
}

func (f *faultDAV) dropNext(path string, n int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.drop[path] = n
}

func (f *faultDAV) alwaysUnauthorized(path string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.authFail[path] = true
}

func (f *faultDAV) getHits(path string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.hits[path]
}

// writeSeed 经 MemFS 写入种子文件（测试装置不经被测 adapter）。
func (f *faultDAV) writeSeed(t *testing.T, logical, content string) {
	t.Helper()
	ctx := context.Background()
	mkdirAllMemFS(t, f.fs, parentOfLogical(logical))
	file, err := f.fs.OpenFile(ctx, logical, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		t.Fatalf("seed %s: %v", logical, err)
	}
	if _, err := file.Write([]byte(content)); err != nil {
		t.Fatalf("seed %s: %v", logical, err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("seed close %s: %v", logical, err)
	}
}

// parentOfLogical 返回逻辑路径的父目录（根返回 "/"）。
func parentOfLogical(logical string) string {
	i := strings.LastIndexByte(logical, '/')
	if i <= 0 {
		return "/"
	}
	return logical[:i]
}

// relEnv 是可靠性场景的端到端环境：真实 WebDAV 服务 + SQLite +
// Source / Job 服务 + Runner。
type relEnv struct {
	davURL    string
	dataDir   string
	localRoot string
	db        *sql.DB
	sources   *source.Service
	runner    *syncjob.Runner
	managed   *jobsqlite.ManagedRepository
	history   *jobsqlite.RunRepository
	jobID     string
}

// newReliabilityEnv 在指定 dav endpoint、datadir 与 localRoot 上
// 装配环境（datadir / localRoot 可跨「重启」复用）。
func newReliabilityEnv(t *testing.T, davURL, dataDir, localRoot, mode string) *relEnv {
	t.Helper()
	if err := os.MkdirAll(localRoot, 0o755); err != nil {
		t.Fatalf("mkdir local root: %v", err)
	}
	db := openDB(t, dataDir)
	e := &relEnv{
		davURL:    davURL,
		dataDir:   dataDir,
		localRoot: localRoot,
		db:        db,
	}
	e.sources = source.NewService(sourcesqlite.New(db), webdav.NewFactory())
	jobRepo := jobsqlite.NewRepository(db)
	e.managed = jobsqlite.NewManagedRepository(db)
	e.history = jobsqlite.NewRunRepository(db)
	jobs := syncjob.NewService(jobRepo, e.sources, dataDir)
	e.runner = syncjob.NewRunner(jobRepo, e.managed, e.sources, e.history)

	if jobID := e.existingJobID(t); jobID != "" {
		e.jobID = jobID
		return e
	}
	src, err := e.sources.Create(context.Background(), source.CreateInput{
		Name: "Reliability WebDAV",
		Type: source.TypeWebDAV,
		Config: source.Config{WebDAV: &source.WebDAVConfig{
			Endpoint: davURL,
		}},
		Enabled: true,
	})
	if err != nil {
		t.Fatalf("create source: %v", err)
	}
	job, err := jobs.Create(context.Background(), syncjob.CreateInput{
		Name:       "reliability-job",
		SourceID:   src.ID,
		RemoteRoot: "/",
		LocalRoot:  localRoot,
		Mode:       syncjob.Mode(mode),
		Enabled:    true,
	})
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	e.jobID = job.ID
	return e
}

// existingJobID 返回已存在的 Job ID（跨重启复用同一 Job）。
func (e *relEnv) existingJobID(t *testing.T) string {
	t.Helper()
	const query = "SELECT id FROM sync_jobs LIMIT 1"
	var id string
	if err := e.db.QueryRow(query).Scan(&id); err != nil {
		if err == sql.ErrNoRows {
			return ""
		}
		t.Fatalf("query job: %v", err)
	}
	return id
}

// runAndWait 触发一轮同步并等待终态。
func (e *relEnv) runAndWait(t *testing.T, timeout time.Duration) syncjob.RunStatus {
	t.Helper()
	runID, err := e.runner.Start(context.Background(), e.jobID)
	if err != nil {
		t.Fatalf("start run: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	status, err := e.runner.Wait(ctx, runID)
	if err != nil {
		t.Fatalf("wait run: %v", err)
	}
	return status
}

// setAdminPassword 经真实服务初始化管理员凭据。
func (e *relEnv) setAdminPassword(t *testing.T, password string) {
	t.Helper()
	svc := auth.NewService(authsqlite.NewRepository(e.db))
	if err := svc.SetAdminPassword(context.Background(), password); err != nil {
		t.Fatalf("set admin password: %v", err)
	}
}

// close 关闭数据库连接，模拟进程退出。
func (e *relEnv) close(t *testing.T) {
	t.Helper()
	if err := e.db.Close(); err != nil {
		t.Fatalf("close db: %v", err)
	}
}

// 首次 503 → 重试成功：传输阶段的瞬时故障在单轮 run 内收敛。
func TestTransientTransferFailureRetries(t *testing.T) {
	f := startFaultDAV(t)
	f.writeSeed(t, "/docs/retry.txt", "retry-content")

	e := newReliabilityEnv(t, f.srv.URL, t.TempDir(), filepath.Join(t.TempDir(), "local"), string(syncjob.ModeCopy))
	f.failNext("/docs/retry.txt", 1)

	st := e.runAndWait(t, 30*time.Second)
	requireSucceeded(t, "transient retry", st)
	if st.Stats.FilesCreated != 1 || st.Stats.BytesTransferred != int64(len("retry-content")) {
		t.Errorf("stats = %+v, want 1 created with full bytes", st.Stats)
	}
	if got := f.getHits("/docs/retry.txt"); got != 2 {
		t.Errorf("GET hits = %d, want 2 (503 + successful retry)", got)
	}
	if content := readLocal(t, filepath.Join(e.localRoot, "docs", "retry.txt")); content != "retry-content" {
		t.Errorf("content = %q, want retry-content", content)
	}
}

// body 中途断连 → 重试成功：stream 中断按瞬时处理，本轮收敛。
func TestConnectionDropMidBodyRetries(t *testing.T) {
	f := startFaultDAV(t)
	f.writeSeed(t, "/drop.bin", strings.Repeat("x", 4096))

	e := newReliabilityEnv(t, f.srv.URL, t.TempDir(), filepath.Join(t.TempDir(), "local"), string(syncjob.ModeCopy))
	f.dropNext("/drop.bin", 1)

	st := e.runAndWait(t, 30*time.Second)
	requireSucceeded(t, "mid-body drop retry", st)
	if got := f.getHits("/drop.bin"); got != 2 {
		t.Errorf("GET hits = %d, want 2", got)
	}
	info, err := os.Stat(filepath.Join(e.localRoot, "drop.bin"))
	if err != nil || info.Size() != 4096 {
		t.Errorf("drop.bin size = %d (err=%v), want 4096", info.Size(), err)
	}
}

// 恒定 401：确定性失败只尝试一次，run 失败且零本地变更。
func TestPermanentFailureDoesNotRetry(t *testing.T) {
	f := startFaultDAV(t)
	f.writeSeed(t, "/docs/secret.txt", "never-arrives")
	f.alwaysUnauthorized("/docs/secret.txt")

	e := newReliabilityEnv(t, f.srv.URL, t.TempDir(), filepath.Join(t.TempDir(), "local"), string(syncjob.ModeCopy))

	st := e.runAndWait(t, 30*time.Second)
	if st.State != syncjob.RunFailed {
		t.Fatalf("state = %s (%s), want failed", st.State, st.Error)
	}
	if got := f.getHits("/docs/secret.txt"); got != 1 {
		t.Errorf("GET hits = %d, want 1 (401 must not retry)", got)
	}
	// 零文件级变更：目录骨架（下载前创建的父目录）允许存在，
	// 但不落地任何文件内容。
	var files int
	err := filepath.WalkDir(e.localRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type().IsRegular() {
			files++
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk local root: %v", err)
	}
	if files != 0 {
		t.Errorf("local root has %d file(s) after failed run, want 0", files)
	}
}

// 同一 remote snapshot 连续运行收敛且幂等：run2 / run3 零变更
// （Copy 与 Mirror 都覆盖；Mirror 追加远端删除后同样收敛）。
func TestRepeatedRunsIdempotent(t *testing.T) {
	for _, mode := range []string{string(syncjob.ModeCopy), string(syncjob.ModeMirror)} {
		t.Run(mode, func(t *testing.T) {
			dav := startDAV(t)
			dav.writeFile(t, "/a.txt", "alpha")
			dav.writeFile(t, "/b.txt", "beta")
			writeMemFSBytes(t, dav, "/dir/c.txt", []byte("gamma"))

			e := newReliabilityEnv(t, dav.srv.URL, t.TempDir(), filepath.Join(t.TempDir(), "local"), mode)

			st1 := e.runAndWait(t, 30*time.Second)
			requireSucceeded(t, "run1", st1)
			if st1.Stats.FilesCreated != 3 {
				t.Fatalf("run1 stats = %+v, want 3 created", st1.Stats)
			}

			st2 := e.runAndWait(t, 30*time.Second)
			requireSucceeded(t, "run2", st2)
			assertOnlySkips(t, "run2", st2.Stats)

			st3 := e.runAndWait(t, 30*time.Second)
			requireSucceeded(t, "run3", st3)
			assertOnlySkips(t, "run3", st3.Stats)

			if mode == string(syncjob.ModeMirror) {
				dav.remove(t, "/b.txt")
				st4 := e.runAndWait(t, 30*time.Second)
				requireSucceeded(t, "run4 mirror delete", st4)
				if st4.Stats.FilesDeleted != 1 {
					t.Fatalf("run4 stats = %+v, want 1 deleted", st4.Stats)
				}
				if _, err := os.Stat(filepath.Join(e.localRoot, "b.txt")); !os.IsNotExist(err) {
					t.Fatal("b.txt survived mirror delete")
				}
				st5 := e.runAndWait(t, 30*time.Second)
				requireSucceeded(t, "run5", st5)
				assertOnlySkips(t, "run5", st5.Stats)
			}
		})
	}
}

// assertOnlySkips 断言统计只剩 skip（幂等：无任何实际变更）。
func assertOnlySkips(t *testing.T, phase string, st syncjob.RunStats) {
	t.Helper()
	if st.FilesCreated != 0 || st.FilesUpdated != 0 || st.FilesDeleted != 0 || st.BytesTransferred != 0 {
		t.Errorf("%s stats = %+v, want zero mutations", phase, st)
	}
}

// crash 遗留（running 记录 + pending metadata + 临时文件）经真实
// app.Run 重启后收敛：running → failed、临时文件清理、经 REST API
// 触发的下一轮 run 正常同步。
func TestCrashRecoveryConvergesAfterRestart(t *testing.T) {
	dav := startDAV(t)
	dav.writeFile(t, "/recover.txt", "recovered-content")

	dataDir := t.TempDir()
	localRoot := filepath.Join(t.TempDir(), "local")
	e := newReliabilityEnv(t, dav.srv.URL, dataDir, localRoot, string(syncjob.ModeCopy))
	adminPassword := "crash-recovery-pass-123"
	e.setAdminPassword(t, adminPassword)

	// 注入 crash 遗留状态：遗留临时文件 + pending managed 记录 +
	// running 运行记录（进程在运行中死亡）。
	tempPath := filepath.Join(localRoot, ".tinysync-part-deadbeef")
	if err := os.WriteFile(tempPath, []byte("half-written"), 0o644); err != nil {
		t.Fatalf("write temp: %v", err)
	}
	ctx := context.Background()
	if err := e.managed.Upsert(ctx, []syncjob.ManagedFile{{
		JobID:        e.jobID,
		RemotePath:   "/ghost.txt",
		LocalRelPath: "ghost.txt",
		State:        syncjob.StatePending,
		UpdatedAt:    time.Now().UTC(),
	}}); err != nil {
		t.Fatalf("seed pending: %v", err)
	}
	staleID := fmt.Sprintf("run_%016x", time.Now().UnixNano())
	if err := e.history.Insert(ctx, syncjob.RunRecord{
		ID:        staleID,
		JobID:     e.jobID,
		Trigger:   syncjob.TriggerManual,
		State:     syncjob.RunRunning,
		StartedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed stale running: %v", err)
	}
	e.close(t)

	// 真实 app.Run 重启：datadir 锁 → stale 收敛 → 临时文件清理 →
	// HTTP 就绪。
	port := freePort(t)
	cfg := config.Default()
	cfg.DataDir = dataDir
	cfg.Port = port
	appCtx, cancelApp := context.WithCancel(context.Background())
	defer cancelApp()
	appDone := make(chan error, 1)
	go func() {
		appDone <- appRunForTest(appCtx, cfg)
	}()
	waitHealthy(t, port)

	// stale running 已收敛为 failed。
	if state := runStateFromDB(t, dataDir, staleID); state != string(syncjob.RunFailed) {
		t.Fatalf("stale run state = %s, want failed", state)
	}
	// crash 临时文件已清理。
	if _, err := os.Stat(tempPath); !os.IsNotExist(err) {
		t.Fatalf("crash temp file survived restart: %v", err)
	}

	// 经真实 REST API 触发下一轮 run：正常收敛。
	client := loginClient(t, port, adminPassword)
	runID := triggerRun(t, client, port, e.jobID)
	if state := waitRunStateByAPI(t, client, port, runID, 30*time.Second); state != "succeeded" {
		t.Fatalf("post-restart run state = %s, want succeeded", state)
	}
	if content := readLocal(t, filepath.Join(localRoot, "recover.txt")); content != "recovered-content" {
		t.Errorf("content = %q, want recovered-content", content)
	}

	cancelApp()
	select {
	case err := <-appDone:
		if err != nil {
			t.Fatalf("app.Run: %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("app.Run did not return after cancel")
	}
}

// 大目录场景（hardening 门控）：10,000 个远端文件的完整 scan /
// plan / 同步收敛，随后一轮零变更。
func TestLargeDirectory10K(t *testing.T) {
	if !hardeningEnabled() {
		t.Skip("set TINYSYNC_HARDENING=1 to run the 10k directory scenario")
	}
	dav := startDAV(t)
	const total = 10000
	for i := 0; i < total; i++ {
		writeMemFSBytes(t, dav, fmt.Sprintf("/bulk/f%05d.txt", i), []byte(fmt.Sprintf("content-%05d", i)))
	}

	e := newReliabilityEnv(t, dav.srv.URL, t.TempDir(), filepath.Join(t.TempDir(), "local"), string(syncjob.ModeCopy))
	st1 := e.runAndWait(t, 10*time.Minute)
	requireSucceeded(t, "bulk run1", st1)
	if st1.Stats.FilesCreated != total {
		t.Fatalf("run1 created = %d, want %d", st1.Stats.FilesCreated, total)
	}
	st2 := e.runAndWait(t, 10*time.Minute)
	requireSucceeded(t, "bulk run2", st2)
	if st2.Stats.FilesSkipped != total || st2.Stats.BytesTransferred != 0 {
		t.Errorf("run2 stats = %+v, want %d skips and zero bytes", st2.Stats, total)
	}
}

// 大文件场景（hardening 门控）：32 MiB 生成流验证 streaming 传输
// 内容一致。
func TestLargeTransfer32MiB(t *testing.T) {
	if !hardeningEnabled() {
		t.Skip("set TINYSYNC_HARDENING=1 to run the 32MiB transfer scenario")
	}
	dav := startDAV(t)
	payload := deterministicBytes(32 << 20)
	writeMemFSBytes(t, dav, "/big-transfer.bin", payload)

	e := newReliabilityEnv(t, dav.srv.URL, t.TempDir(), filepath.Join(t.TempDir(), "local"), string(syncjob.ModeCopy))
	st := e.runAndWait(t, 10*time.Minute)
	requireSucceeded(t, "large transfer", st)
	if st.Stats.BytesTransferred != int64(len(payload)) {
		t.Errorf("bytes = %d, want %d", st.Stats.BytesTransferred, len(payload))
	}
	data, err := os.ReadFile(filepath.Join(e.localRoot, "big-transfer.bin"))
	if err != nil {
		t.Fatalf("read local: %v", err)
	}
	if sha256.Sum256(data) != sha256.Sum256(payload) {
		t.Error("large transfer content mismatch")
	}
}

// ---- crash E2E 的 HTTP / app 装置 ----

// appRunForTest 以 fallback 前端资源运行真实 app.Run。
func appRunForTest(ctx context.Context, cfg *config.Config) error {
	return app.Run(ctx, cfg, mustFallbackFS())
}

// freePort 申请一个可用的本机端口。
func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// waitHealthy 轮询公开 health 端点直至就绪。
func waitHealthy(t *testing.T, port int) {
	t.Helper()
	url := fmt.Sprintf("http://127.0.0.1:%d/api/v1/health", port)
	for i := 0; i < 60; i++ {
		resp, err := http.Get(url)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatal("app did not become healthy")
}

// loginClient 用管理员密码登录并返回携带会话 cookie 的 client。
func loginClient(t *testing.T, port int, password string) *http.Client {
	t.Helper()
	body := strings.NewReader(fmt.Sprintf(`{"password":%q}`, password))
	resp, err := http.Post(fmt.Sprintf("http://127.0.0.1:%d/api/v1/auth/login", port),
		"application/json", body)
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("login status = %d, want 200", resp.StatusCode)
	}
	cookies := resp.Cookies()
	if len(cookies) == 0 {
		t.Fatal("login returned no session cookie")
	}
	return &http.Client{Transport: cookieTransport{cookies: cookies}}
}

// cookieTransport 为每个请求附加会话 cookie。
type cookieTransport struct {
	cookies []*http.Cookie
}

func (t cookieTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	for _, c := range t.cookies {
		req.AddCookie(c)
	}
	return http.DefaultTransport.RoundTrip(req)
}

// triggerRun 经 REST API 触发同步并返回 run ID。
func triggerRun(t *testing.T, client *http.Client, port int, jobID string) string {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost,
		fmt.Sprintf("http://127.0.0.1:%d/api/v1/jobs/%s/run", port, jobID), nil)
	if err != nil {
		t.Fatalf("build run request: %v", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("trigger run: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("run status = %d, want 202", resp.StatusCode)
	}
	var out struct {
		RunID string `json:"run_id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil || out.RunID == "" {
		t.Fatalf("decode run response: %v (%+v)", err, out)
	}
	return out.RunID
}

// waitRunStateByAPI 轮询 run 终态。
func waitRunStateByAPI(t *testing.T, client *http.Client, port int, runID string, timeout time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/api/v1/runs/%s", port, runID))
		if err == nil {
			var out struct {
				Status string `json:"status"`
			}
			err = json.NewDecoder(resp.Body).Decode(&out)
			_ = resp.Body.Close()
			if err == nil {
				switch out.Status {
				case "succeeded", "failed":
					return out.Status
				}
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatal("run did not converge via API")
	return ""
}

// runStateFromDB 直接查询数据库中的运行状态。
func runStateFromDB(t *testing.T, dataDir, runID string) string {
	t.Helper()
	db, err := storage.Open(dataDir)
	if err != nil {
		t.Fatalf("open db for query: %v", err)
	}
	defer db.Close()
	var state string
	if err := db.QueryRow("SELECT status FROM sync_runs WHERE id = ?", runID).Scan(&state); err != nil {
		t.Fatalf("query run %s: %v", runID, err)
	}
	return state
}

// mustFallbackFS 返回测试用的空前端资源（app.Run 仅用于承载路由，
// 测试不访问 Web 静态资源）。
func mustFallbackFS() fstest.MapFS {
	return fstest.MapFS{}
}

// deterministicBytes 生成确定性伪随机内容（可重复校验）。
func deterministicBytes(n int) []byte {
	buf := make([]byte, n)
	x := uint32(0x12345679)
	for i := range buf {
		x = x*1664525 + 1013904223
		buf[i] = byte(x >> 24)
	}
	return buf
}

// writeMemFSBytes 把字节内容写入 dav MemFS。
func writeMemFSBytes(t *testing.T, dav *davServer, logical string, content []byte) {
	t.Helper()
	ctx := context.Background()
	mkdirAllMemFS(t, dav.fs, parentOfLogical(logical))
	file, err := dav.fs.OpenFile(ctx, logical, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		t.Fatalf("open %s: %v", logical, err)
	}
	if _, err := file.Write(content); err != nil {
		t.Fatalf("write %s: %v", logical, err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close %s: %v", logical, err)
	}
}

// mkdirAllMemFS 逐级创建 MemFS 目录（已存在视为成功：失败交由
// 后续 OpenFile 暴露）。
func mkdirAllMemFS(t *testing.T, fs xnetdav.FileSystem, dir string) {
	t.Helper()
	if dir == "" || dir == "/" {
		return
	}
	ctx := context.Background()
	for i := 1; i <= len(dir); i++ {
		if i == len(dir) || dir[i] == '/' {
			_ = fs.Mkdir(ctx, dir[:i], 0o755)
		}
	}
}

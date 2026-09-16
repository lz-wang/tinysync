package e2e

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	xnetdav "golang.org/x/net/webdav"

	"tinysync/internal/source"
	sourcesqlite "tinysync/internal/source/sqlite"
	"tinysync/internal/source/webdav"
	"tinysync/internal/storage"
	"tinysync/internal/syncjob"
	jobsqlite "tinysync/internal/syncjob/sqlite"
)

// davServer 是带 PROPFIND 故障开关的真实 WebDAV 服务端：
// 内部使用 x/net/webdav MemFS，测试可直接改写远端内容。
type davServer struct {
	fs   xnetdav.FileSystem
	srv  *httptest.Server
	fail atomic.Bool
}

// startDAV 启动空根目录的 WebDAV 服务并注册清理。
func startDAV(t *testing.T) *davServer {
	t.Helper()
	dav := &davServer{fs: xnetdav.NewMemFS()}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// fail 开关只对 PROPFIND 生效：模拟扫描阶段故障，
		// Open/GET 不受影响以便覆盖传输阶段行为。
		if dav.fail.Load() && r.Method == "PROPFIND" {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		inner := &xnetdav.Handler{FileSystem: dav.fs, LockSystem: xnetdav.NewMemLS()}
		inner.ServeHTTP(w, r)
	})
	dav.srv = httptest.NewServer(handler)
	t.Cleanup(dav.srv.Close)
	return dav
}

// writeFile 写入或覆盖远端文件。
func (d *davServer) writeFile(t *testing.T, name, content string) {
	t.Helper()
	ctx := context.Background()
	f, err := d.fs.OpenFile(ctx, name, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		t.Fatalf("dav open %s: %v", name, err)
	}
	if _, err := f.Write([]byte(content)); err != nil {
		t.Fatalf("dav write %s: %v", name, err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("dav close %s: %v", name, err)
	}
}

// remove 删除远端文件。
func (d *davServer) remove(t *testing.T, name string) {
	t.Helper()
	if err := d.fs.RemoveAll(context.Background(), name); err != nil {
		t.Fatalf("dav remove %s: %v", name, err)
	}
}

// env 是一套真实的端到端环境：Dav 服务端 + 临时 datadir 的 SQLite +
// 真实 Factory 构造的 Source/Job 服务与 Runner。
type env struct {
	dav         *davServer
	dataDir     string
	db          *sql.DB
	sources     *source.Service
	jobs        *syncjob.Service
	runner      *syncjob.Runner
	jobRepo     *jobsqlite.Repository
	managedRepo *jobsqlite.ManagedRepository
}

// newEnv 装配端到端环境；db 生命周期由测试注册的 cleanup 管理。
func newEnv(t *testing.T) *env {
	t.Helper()
	e := &env{dav: startDAV(t), dataDir: t.TempDir()}
	e.db = openDB(t, e.dataDir)
	e.sources = source.NewService(sourcesqlite.New(e.db), webdav.NewFactory())
	e.jobRepo = jobsqlite.NewRepository(e.db)
	e.managedRepo = jobsqlite.NewManagedRepository(e.db)
	e.jobs = syncjob.NewService(e.jobRepo, e.sources, e.dataDir)
	e.runner = syncjob.NewRunner(e.jobRepo, e.managedRepo, e.sources, jobsqlite.NewRunRepository(e.db))
	t.Cleanup(func() { _ = e.db.Close() })
	return e
}

// closeDB 关闭当前数据库连接，模拟进程退出。
func (e *env) closeDB(t *testing.T) error {
	t.Helper()
	return e.db.Close()
}

// openDB 打开并迁移临时 datadir 的 SQLite。
func openDB(t *testing.T, dataDir string) *sql.DB {
	t.Helper()
	db, err := storage.Open(dataDir)
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	if err := storage.Migrate(context.Background(), db, dataDir); err != nil {
		t.Fatalf("storage.Migrate: %v", err)
	}
	return db
}

// createSource 经真实服务创建指向 Dav 服务端的 Source，返回 ID。
func (e *env) createSource(t *testing.T) string {
	t.Helper()
	src, err := e.sources.Create(context.Background(), source.CreateInput{
		Name:     "E2E WebDAV",
		Type:     source.TypeWebDAV,
		Endpoint: e.dav.srv.URL,
		Enabled:  true,
	})
	if err != nil {
		t.Fatalf("create source: %v", err)
	}
	return src.ID
}

// createJob 经真实服务创建 Job（include all，远端根），返回领域对象。
func (e *env) createJob(t *testing.T, name, sourceID, mode string) syncjob.Job {
	t.Helper()
	localRoot := filepath.Join(t.TempDir(), "local")
	if err := os.MkdirAll(localRoot, 0o755); err != nil {
		t.Fatalf("mkdir local root: %v", err)
	}
	job, err := e.jobs.Create(context.Background(), syncjob.CreateInput{
		Name:       name,
		SourceID:   sourceID,
		RemoteRoot: "/",
		LocalRoot:  localRoot,
		Mode:       syncjob.Mode(mode),
		Enabled:    true,
	})
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	return job
}

// runAndWait 触发一轮同步并等待终态。
func (e *env) runAndWait(t *testing.T, jobID string) syncjob.RunStatus {
	t.Helper()
	runID, err := e.runner.Start(context.Background(), jobID)
	if err != nil {
		t.Fatalf("start run: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	status, err := e.runner.Wait(ctx, runID)
	if err != nil {
		t.Fatalf("wait run: %v", err)
	}
	return status
}

// requireSucceeded 断言一轮同步成功。
func requireSucceeded(t *testing.T, phase string, st syncjob.RunStatus) {
	t.Helper()
	if st.State != syncjob.RunSucceeded {
		t.Fatalf("%s: state = %s (%s), want succeeded", phase, st.State, st.Error)
	}
}

// readLocal 读取本地同步文件内容。
func readLocal(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

// 端到端主链路：v1 下载 → 二轮 skip → v2 更新 → 本地缺失修复 →
// 扫描失败零变更 → 远端删除后 Copy 保留 + Mirror 删除，
// 手工创建的未知本地文件永不触碰。
func TestWebDAVPullEndToEnd(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	sourceID := e.createSource(t)
	copyJob := e.createJob(t, "Copy Job", sourceID, string(syncjob.ModeCopy))
	mirrorJob := e.createJob(t, "Mirror Job", sourceID, string(syncjob.ModeMirror))
	copyLocal := filepath.Join(copyJob.LocalRoot, "a.txt")
	mirrorLocal := filepath.Join(mirrorJob.LocalRoot, "a.txt")

	// 1. remote a.txt = v1 → 双 Job 下载。
	e.dav.writeFile(t, "/a.txt", "v1")
	requireSucceeded(t, "copy v1", e.runAndWait(t, copyJob.ID))
	requireSucceeded(t, "mirror v1", e.runAndWait(t, mirrorJob.ID))
	if got := readLocal(t, copyLocal); got != "v1" {
		t.Fatalf("copy local = %q, want v1", got)
	}
	if got := readLocal(t, mirrorLocal); got != "v1" {
		t.Fatalf("mirror local = %q, want v1", got)
	}

	// 2. 远端无变化 → skip，零传输。
	st := e.runAndWait(t, copyJob.ID)
	requireSucceeded(t, "copy skip", st)
	if st.Stats.FilesSkipped != 1 || st.Stats.FilesCreated != 0 || st.Stats.BytesTransferred != 0 {
		t.Fatalf("copy skip stats = %+v, want skipped=1 no transfer", st.Stats)
	}

	// 3. remote a.txt = v2 → 更新。
	e.dav.writeFile(t, "/a.txt", "v2")
	st = e.runAndWait(t, copyJob.ID)
	requireSucceeded(t, "copy update", st)
	if st.Stats.FilesUpdated != 1 {
		t.Fatalf("copy update stats = %+v, want updated=1", st.Stats)
	}
	requireSucceeded(t, "mirror update", e.runAndWait(t, mirrorJob.ID))
	if got := readLocal(t, copyLocal); got != "v2" {
		t.Fatalf("copy local = %q, want v2", got)
	}

	// 4. 本地 managed 文件缺失 → 修复下载。
	if err := os.Remove(copyLocal); err != nil {
		t.Fatalf("remove local: %v", err)
	}
	st = e.runAndWait(t, copyJob.ID)
	requireSucceeded(t, "copy repair", st)
	if st.Stats.FilesCreated != 1 {
		t.Fatalf("copy repair stats = %+v, want created=1", st.Stats)
	}
	if got := readLocal(t, copyLocal); got != "v2" {
		t.Fatalf("copy repaired local = %q, want v2", got)
	}

	// 5. 扫描失败（PROPFIND 500）→ 整体失败且零本地变更：本地文件在位、
	//    managed metadata 完整保留，下一轮继续收敛。
	e.dav.fail.Store(true)
	st = e.runAndWait(t, copyJob.ID)
	if st.State != syncjob.RunFailed {
		t.Fatalf("scan failure: state = %s, want failed", st.State)
	}
	if got := readLocal(t, copyLocal); got != "v2" {
		t.Fatalf("scan failure modified local: %q", got)
	}
	managed, err := e.managedRepo.ListByJob(ctx, copyJob.ID)
	if err != nil || len(managed) != 1 {
		t.Fatalf("scan failure managed = %d entries (%v), want 1", len(managed), err)
	}
	e.dav.fail.Store(false)

	// 6. 手工创建未知本地文件，随后确认任何模式都不触碰它。
	unknown := filepath.Join(mirrorJob.LocalRoot, "unknown.txt")
	if err := os.WriteFile(unknown, []byte("mine"), 0o644); err != nil {
		t.Fatalf("write unknown: %v", err)
	}

	// 7. remote 删除 a.txt：
	//    Copy → 本地保留 + managed 释放；
	//    Mirror → 本地 managed 文件删除 + managed 清理；未知文件保留。
	e.dav.remove(t, "/a.txt")
	st = e.runAndWait(t, copyJob.ID)
	requireSucceeded(t, "copy remote-delete", st)
	if st.Stats.FilesDeleted != 0 {
		t.Fatalf("copy remote-delete stats = %+v, want deleted=0", st.Stats)
	}
	if got := readLocal(t, copyLocal); got != "v2" {
		t.Fatalf("copy kept local = %q, want v2", got)
	}
	if managed, _ := e.managedRepo.ListByJob(ctx, copyJob.ID); len(managed) != 0 {
		t.Fatalf("copy managed after relinquish = %d entries, want 0", len(managed))
	}

	st = e.runAndWait(t, mirrorJob.ID)
	requireSucceeded(t, "mirror remote-delete", st)
	if st.Stats.FilesDeleted != 1 {
		t.Fatalf("mirror remote-delete stats = %+v, want deleted=1", st.Stats)
	}
	if _, err := os.Stat(mirrorLocal); !os.IsNotExist(err) {
		t.Fatalf("mirror local still present (stat err = %v), want removed", err)
	}
	if got := readLocal(t, unknown); got != "mine" {
		t.Fatalf("unknown local = %q, want mine", got)
	}
	if managed, _ := e.managedRepo.ListByJob(ctx, mirrorJob.ID); len(managed) != 0 {
		t.Fatalf("mirror managed after delete = %d entries, want 0", len(managed))
	}
}

// 跨重启持久化：同 datadir 重开数据库后 Source / Job / managed 完整
// 保留，运行状态回到 idle（内存态），重启后的进程继续收敛变更。
func TestJobPersistsAcrossRestart(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.dav.writeFile(t, "/a.txt", "v1")
	sourceID := e.createSource(t)
	job := e.createJob(t, "Restart Job", sourceID, string(syncjob.ModeMirror))
	requireSucceeded(t, "first run", e.runAndWait(t, job.ID))

	// 模拟进程退出：关闭数据库。
	if err := e.closeDB(t); err != nil {
		t.Fatalf("close db: %v", err)
	}

	// 重启：同 datadir 重开，全部状态可恢复。
	db := openDB(t, e.dataDir)
	t.Cleanup(func() { _ = db.Close() })
	sources2 := source.NewService(sourcesqlite.New(db), webdav.NewFactory())
	jobRepo2 := jobsqlite.NewRepository(db)
	managedRepo2 := jobsqlite.NewManagedRepository(db)
	jobs2 := syncjob.NewService(jobRepo2, sources2, e.dataDir)
	runner2 := syncjob.NewRunner(jobRepo2, managedRepo2, sources2, jobsqlite.NewRunRepository(db))

	// Source 完整保留。
	src, err := sources2.Get(ctx, sourceID)
	if err != nil {
		t.Fatalf("source after restart: %v", err)
	}
	if src.Endpoint != e.dav.srv.URL || !src.Enabled {
		t.Fatalf("source after restart = %+v, want endpoint/enabled preserved", src)
	}

	// Job 配置完整保留。
	got, err := jobs2.Get(ctx, job.ID)
	if err != nil {
		t.Fatalf("job after restart: %v", err)
	}
	if got.Name != job.Name || got.Mode != job.Mode || got.RemoteRoot != job.RemoteRoot ||
		got.LocalRoot != job.LocalRoot || got.SourceID != job.SourceID || !got.Enabled {
		t.Fatalf("job after restart = %+v, want %+v", got, job)
	}

	// managed synced 记录保留（Mirror 删除授权不因重启丢失）。
	managed, err := managedRepo2.ListByJob(ctx, job.ID)
	if err != nil || len(managed) != 1 || managed[0].State != syncjob.StateSynced {
		t.Fatalf("managed after restart = %d entries (%v), want 1 synced", len(managed), err)
	}

	// 运行状态持久化：重启后最近一次 completed run 仍可查询，不再回 idle。
	st, err := runner2.GetStatus(ctx, job.ID)
	if err != nil || st.State != syncjob.RunSucceeded {
		t.Fatalf("status after restart = %s (%v), want persisted succeeded", st.State, err)
	}

	// 重启后的进程继续收敛：远端 v2 → 更新。
	e.dav.writeFile(t, "/a.txt", "v2")
	runID, err := runner2.Start(ctx, job.ID)
	if err != nil {
		t.Fatalf("start run after restart: %v", err)
	}
	runCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	final, err := runner2.Wait(runCtx, runID)
	if err != nil {
		t.Fatalf("wait run after restart: %v", err)
	}
	if final.State != syncjob.RunSucceeded || final.Stats.FilesUpdated != 1 {
		t.Fatalf("run after restart = %s %+v, want succeeded updated=1", final.State, final.Stats)
	}
	data, err := os.ReadFile(filepath.Join(job.LocalRoot, "a.txt"))
	if err != nil || string(data) != "v2" {
		t.Fatalf("local after restart run = %q (%v), want v2", data, err)
	}
}

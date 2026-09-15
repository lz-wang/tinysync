package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"tinysync/internal/source"
	sourcesqlite "tinysync/internal/source/sqlite"
	"tinysync/internal/storage"
	"tinysync/internal/syncjob"
	jobsqlite "tinysync/internal/syncjob/sqlite"
)

// fakeJobRemote 是可控的 Remote：List 按预设返回文件或错误。
type fakeJobRemote struct {
	files []source.FileInfo
	err   error
}

func (r fakeJobRemote) Stat(ctx context.Context, path string) (source.FileInfo, error) {
	return source.FileInfo{Path: path, IsDir: true}, nil
}

func (r fakeJobRemote) List(ctx context.Context, path string) ([]source.FileInfo, error) {
	return r.files, r.err
}

func (r fakeJobRemote) Open(ctx context.Context, path string) (io.ReadCloser, error) {
	return nil, errors.New("not implemented")
}

// gateRemote 的 List 阻塞在 gate 上，用于构造确定性的「运行中」窗口；
// 释放后按预设返回结果。
type gateRemote struct {
	gate chan struct{}
	err  error
}

func (r *gateRemote) Stat(ctx context.Context, path string) (source.FileInfo, error) {
	return source.FileInfo{Path: path, IsDir: true}, nil
}

func (r *gateRemote) List(ctx context.Context, path string) ([]source.FileInfo, error) {
	select {
	case <-r.gate:
		return nil, r.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (r *gateRemote) Open(ctx context.Context, path string) (io.ReadCloser, error) {
	return nil, errors.New("not implemented")
}

// newJobRouter 构造挂载真实 Source + Job 服务与 Runner 的路由，
// 全部共享同一临时 SQLite。
func newJobRouter(t *testing.T, remote source.Remote) *gin.Engine {
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
	sourceSvc := source.NewService(sourcesqlite.New(db), fakeFactory{remote: remote})
	jobRepo := jobsqlite.NewRepository(db)
	managedRepo := jobsqlite.NewManagedRepository(db)
	jobSvc := syncjob.NewService(jobRepo, sourceSvc, dataDir)
	runner := syncjob.NewRunner(jobRepo, managedRepo, sourceSvc, fakeFactory{remote: remote})
	return NewRouter(testWebFS(), Dependencies{Sources: sourceSvc, Jobs: jobSvc, Runner: runner})
}

// createSourceViaAPI 用 API 创建 Source 并返回 ID。
func createSourceViaAPI(t *testing.T, router *gin.Engine, name string, enabled bool) string {
	t.Helper()
	body := fmt.Sprintf(`{"name": %q, "type": "webdav", "endpoint": "https://dav.example.com", "enabled": %t}`,
		name, enabled)
	rec := doJSON(t, router, "POST", "/api/v1/sources", body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create source status = %d, body = %s", rec.Code, rec.Body.String())
	}
	id, _ := decodeJSON(t, rec)["id"].(string)
	if id == "" {
		t.Fatalf("created source has no id: %s", rec.Body.String())
	}
	return id
}

// jobPayload 构造创建 Job 的请求体，localRoot 指向新临时目录。
func jobPayload(t *testing.T, name, sourceID, mode string, enabled bool) (string, string) {
	t.Helper()
	localRoot := filepath.ToSlash(filepath.Join(t.TempDir(), "root"))
	if err := os.MkdirAll(localRoot, 0o755); err != nil {
		t.Fatalf("mkdir local root: %v", err)
	}
	body := fmt.Sprintf(`{
		"name": %q, "source_id": %q, "remote_root": "/photos",
		"local_root": %q, "mode": %q, "include": ["**/*.jpg"], "exclude": [],
		"enabled": %t
	}`, name, sourceID, localRoot, mode, enabled)
	return body, localRoot
}

// waitForRunState 轮询 status 直到进入期望状态，返回最终响应。
func waitForRunState(t *testing.T, router *gin.Engine, jobID string, want ...syncjob.RunState) map[string]any {
	t.Helper()
	wantStr := make([]string, 0, len(want))
	for _, w := range want {
		wantStr = append(wantStr, string(w))
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		rec := doJSON(t, router, "GET", "/api/v1/jobs/"+jobID+"/status", "")
		if rec.Code == http.StatusOK {
			body := decodeJSON(t, rec)
			state, _ := body["state"].(string)
			if slices.Contains(wantStr, state) {
				return body
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("job %s did not reach state %v in time", jobID, wantStr)
	return nil
}

// 完整生命周期：创建 → 读取 → 列表 → 部分更新 → 删除；
// Job 从未运行时 status 为 idle。
func TestJobLifecycleAPI(t *testing.T) {
	router := newJobRouter(t, fakeJobRemote{})
	sourceID := createSourceViaAPI(t, router, "NAS WebDAV", true)
	payload, _ := jobPayload(t, "Photos", sourceID, "copy", true)

	rec := doJSON(t, router, "POST", "/api/v1/jobs", payload)
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST status = %d, body = %s", rec.Code, rec.Body.String())
	}
	created := decodeJSON(t, rec)
	id, _ := created["id"].(string)
	if id == "" {
		t.Fatalf("created job has no id: %s", rec.Body.String())
	}
	if created["mode"] != "copy" || created["enabled"] != true {
		t.Errorf("create defaults wrong: %s", rec.Body.String())
	}
	if created["remote_root"] != "/photos" {
		t.Errorf("remote_root = %v, want /photos", created["remote_root"])
	}
	include, _ := created["include"].([]any)
	if len(include) != 1 || include[0] != "**/*.jpg" {
		t.Errorf("include = %v, want [**/*.jpg]", created["include"])
	}
	if created["created_at"] == "" || created["updated_at"] == "" {
		t.Errorf("missing timestamps: %s", rec.Body.String())
	}

	// GET 单个与列表。
	rec = doJSON(t, router, "GET", "/api/v1/jobs/"+id, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET status = %d", rec.Code)
	}
	rec = doJSON(t, router, "GET", "/api/v1/jobs", "")
	list := decodeJSON(t, rec)
	jobs, _ := list["jobs"].([]any)
	if len(jobs) != 1 {
		t.Fatalf("list length = %d, want 1", len(jobs))
	}

	// 从未运行：status 为 idle。
	rec = doJSON(t, router, "GET", "/api/v1/jobs/"+id+"/status", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status code = %d", rec.Code)
	}
	status := decodeJSON(t, rec)
	if status["state"] != string(syncjob.RunIdle) {
		t.Errorf("initial status = %v, want idle", status["state"])
	}

	// PATCH：改 mode / 禁用 / 替换 include，其余保留。
	rec = doJSON(t, router, "PATCH", "/api/v1/jobs/"+id,
		`{"mode": "mirror", "enabled": false, "include": ["docs/**"]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("PATCH status = %d, body = %s", rec.Code, rec.Body.String())
	}
	updated := decodeJSON(t, rec)
	if updated["mode"] != "mirror" || updated["enabled"] != false {
		t.Errorf("patched fields wrong: %s", rec.Body.String())
	}
	if updated["name"] != "Photos" {
		t.Errorf("patched name changed unexpectedly: %s", rec.Body.String())
	}
	patchedInclude, _ := updated["include"].([]any)
	if len(patchedInclude) != 1 || patchedInclude[0] != "docs/**" {
		t.Errorf("patched include = %v, want [docs/**]", updated["include"])
	}

	// DELETE 后 GET 与 status 均 404。
	rec = doJSON(t, router, "DELETE", "/api/v1/jobs/"+id, "")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("DELETE status = %d, want 204", rec.Code)
	}
	if rec := doJSON(t, router, "GET", "/api/v1/jobs/"+id, ""); rec.Code != http.StatusNotFound {
		t.Errorf("GET after delete status = %d, want 404", rec.Code)
	}
	if rec := doJSON(t, router, "GET", "/api/v1/jobs/"+id+"/status", ""); rec.Code != http.StatusNotFound {
		t.Errorf("status after delete = %d, want 404", rec.Code)
	}
}

// 非法输入返回 400：空白名、未知 mode、不存在的 Source、
// 不存在的 LocalRoot、坏 JSON。
func TestJobCreateValidationAPI(t *testing.T) {
	router := newJobRouter(t, fakeJobRemote{})
	root := filepath.ToSlash(t.TempDir())

	for name, body := range map[string]string{
		"blank name":       fmt.Sprintf(`{"name": " ", "source_id": "src_x", "local_root": %q, "mode": "copy"}`, root),
		"bad mode":         fmt.Sprintf(`{"name": "x", "source_id": "src_x", "local_root": %q, "mode": "sync"}`, root),
		"missing source":   fmt.Sprintf(`{"name": "x", "source_id": "src_missing", "local_root": %q, "mode": "copy"}`, root),
		"missing location": `{"name": "x", "source_id": "src_x", "local_root": "/nonexistent/dir", "mode": "copy"}`,
		"bad json":         `{`,
	} {
		rec := doJSON(t, router, "POST", "/api/v1/jobs", body)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400, body = %s", name, rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), "error") {
			t.Errorf("%s: body has no error field: %s", name, rec.Body.String())
		}
	}
}

// name 大小写不敏感冲突返回 409。
func TestJobDuplicateNameAPI(t *testing.T) {
	router := newJobRouter(t, fakeJobRemote{})
	sourceID := createSourceViaAPI(t, router, "NAS", true)
	payload, _ := jobPayload(t, "Backup", sourceID, "copy", true)
	if rec := doJSON(t, router, "POST", "/api/v1/jobs", payload); rec.Code != http.StatusCreated {
		t.Fatalf("first POST status = %d, body = %s", rec.Code, rec.Body.String())
	}
	dup, _ := jobPayload(t, "backup", sourceID, "copy", true)
	rec := doJSON(t, router, "POST", "/api/v1/jobs", dup)
	if rec.Code != http.StatusConflict {
		t.Fatalf("duplicate POST status = %d, want 409, body = %s", rec.Code, rec.Body.String())
	}
}

// 未知 ID 的 CRUD 与 run / status 均 404。
func TestJobNotFoundAPI(t *testing.T) {
	router := newJobRouter(t, fakeJobRemote{})
	for _, tc := range []struct {
		method, path string
	}{
		{"GET", "/api/v1/jobs/job_missing"},
		{"PATCH", "/api/v1/jobs/job_missing"},
		{"DELETE", "/api/v1/jobs/job_missing"},
		{"POST", "/api/v1/jobs/job_missing/run"},
		{"GET", "/api/v1/jobs/job_missing/status"},
	} {
		rec := doJSON(t, router, tc.method, tc.path, "{}")
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s %s status = %d, want 404", tc.method, tc.path, rec.Code)
		}
	}
}

// LocalRoot 与现有 Job 重叠（相等或父子嵌套）返回 409。
func TestJobRootOverlapAPI(t *testing.T) {
	router := newJobRouter(t, fakeJobRemote{})
	sourceID := createSourceViaAPI(t, router, "NAS", true)
	first, root := jobPayload(t, "A", sourceID, "copy", true)
	if rec := doJSON(t, router, "POST", "/api/v1/jobs", first); rec.Code != http.StatusCreated {
		t.Fatalf("first POST status = %d, body = %s", rec.Code, rec.Body.String())
	}

	// 相同 root。
	same := fmt.Sprintf(`{"name": "B", "source_id": %q, "local_root": %q, "mode": "copy"}`, sourceID, root)
	if rec := doJSON(t, router, "POST", "/api/v1/jobs", same); rec.Code != http.StatusConflict {
		t.Errorf("same root status = %d, want 409, body = %s", rec.Code, rec.Body.String())
	}

	// 子目录。
	nested := filepath.ToSlash(filepath.Join(root, "sub"))
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("mkdir nested: %v", err)
	}
	nestedBody := fmt.Sprintf(`{"name": "C", "source_id": %q, "local_root": %q, "mode": "copy"}`, sourceID, nested)
	if rec := doJSON(t, router, "POST", "/api/v1/jobs", nestedBody); rec.Code != http.StatusConflict {
		t.Errorf("nested root status = %d, want 409", rec.Code)
	}
}

// 手动运行：202 + run_id，状态收敛为 succeeded，统计字段齐备。
func TestJobRunSuccessAPI(t *testing.T) {
	router := newJobRouter(t, fakeJobRemote{})
	sourceID := createSourceViaAPI(t, router, "NAS", true)
	payload, _ := jobPayload(t, "Empty", sourceID, "copy", true)
	rec := doJSON(t, router, "POST", "/api/v1/jobs", payload)
	jobID, _ := decodeJSON(t, rec)["id"].(string)

	rec = doJSON(t, router, "POST", "/api/v1/jobs/"+jobID+"/run", "")
	if rec.Code != http.StatusAccepted {
		t.Fatalf("run status = %d, want 202, body = %s", rec.Code, rec.Body.String())
	}
	runBody := decodeJSON(t, rec)
	runID, _ := runBody["run_id"].(string)
	if runID == "" {
		t.Fatalf("run response missing run_id: %s", rec.Body.String())
	}
	if runBody["state"] != string(syncjob.RunRunning) {
		t.Errorf("run state = %v, want running", runBody["state"])
	}

	final := waitForRunState(t, router, jobID, syncjob.RunSucceeded, syncjob.RunFailed)
	if final["state"] != string(syncjob.RunSucceeded) {
		t.Fatalf("final state = %v (%v), want succeeded", final["state"], final["error"])
	}
	if final["run_id"] != runID {
		t.Errorf("status run_id = %v, want %v", final["run_id"], runID)
	}
	stats, ok := final["stats"].(map[string]any)
	if !ok {
		t.Fatalf("status missing stats object: %v", final)
	}
	for _, field := range []string{
		"files_total", "files_created", "files_updated", "files_deleted", "files_skipped", "bytes_transferred",
	} {
		if _, has := stats[field]; !has {
			t.Errorf("stats missing %s: %v", field, stats)
		}
	}
	if finishedAt, _ := final["finished_at"].(string); finishedAt == "" {
		t.Error("finished run missing finished_at")
	}
}

// Job 禁用与 Source 禁用时 Run 均 409。
func TestJobRunRejectedWhenDisabledAPI(t *testing.T) {
	router := newJobRouter(t, fakeJobRemote{})

	enabledSource := createSourceViaAPI(t, router, "Live", true)
	disabledSource := createSourceViaAPI(t, router, "Sleeping", false)

	payload, _ := jobPayload(t, "DisabledJob", enabledSource, "copy", false)
	rec := doJSON(t, router, "POST", "/api/v1/jobs", payload)
	disabledJobID, _ := decodeJSON(t, rec)["id"].(string)

	payload, _ = jobPayload(t, "OnDisabledSource", disabledSource, "copy", true)
	rec = doJSON(t, router, "POST", "/api/v1/jobs", payload)
	jobOnDisabledSourceID, _ := decodeJSON(t, rec)["id"].(string)

	rec = doJSON(t, router, "POST", "/api/v1/jobs/"+disabledJobID+"/run", "")
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "disabled") {
		t.Errorf("disabled job run = %d %s, want 409 disabled", rec.Code, rec.Body.String())
	}

	rec = doJSON(t, router, "POST", "/api/v1/jobs/"+jobOnDisabledSourceID+"/run", "")
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "disabled") {
		t.Errorf("disabled source run = %d %s, want 409 disabled", rec.Code, rec.Body.String())
	}
}

// 全局单运行：占用中再次触发返回 409；本轮失败时状态为 failed 并带错误。
func TestJobRunConflictAndFailureAPI(t *testing.T) {
	gate := make(chan struct{})
	remote := &gateRemote{gate: gate, err: errors.New("remote exploded")}
	router := newJobRouter(t, remote)
	sourceID := createSourceViaAPI(t, router, "NAS", true)
	payload, _ := jobPayload(t, "Blocked", sourceID, "copy", true)
	rec := doJSON(t, router, "POST", "/api/v1/jobs", payload)
	jobID, _ := decodeJSON(t, rec)["id"].(string)

	// 第一轮启动并停在扫描阶段。
	rec = doJSON(t, router, "POST", "/api/v1/jobs/"+jobID+"/run", "")
	if rec.Code != http.StatusAccepted {
		t.Fatalf("first run status = %d, want 202", rec.Code)
	}
	// 确认进入 running 后再触发第二轮。
	waitForRunState(t, router, jobID, syncjob.RunRunning)

	rec = doJSON(t, router, "POST", "/api/v1/jobs/"+jobID+"/run", "")
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "active") {
		t.Errorf("second run = %d %s, want 409 active", rec.Code, rec.Body.String())
	}

	// 释放 gate：扫描失败 → 本轮 failed，错误可见。
	close(gate)
	final := waitForRunState(t, router, jobID, syncjob.RunSucceeded, syncjob.RunFailed)
	if final["state"] != string(syncjob.RunFailed) {
		t.Fatalf("final state = %v, want failed", final["state"])
	}
	if errMsg, _ := final["error"].(string); !strings.Contains(errMsg, "remote exploded") {
		t.Errorf("error = %q, want containing remote exploded", errMsg)
	}
}

// 运行中的 Job 拒绝修改与删除（409）：旧 mapping 的传输可能仍在
// 推进 metadata，与配置变更/删除交叉会产生状态竞争；运行结束后恢复。
func TestJobRunningRejectsUpdateAndDeleteAPI(t *testing.T) {
	gate := make(chan struct{})
	router := newJobRouter(t, &gateRemote{gate: gate})
	sourceID := createSourceViaAPI(t, router, "NAS", true)
	payload, _ := jobPayload(t, "InFlight", sourceID, "copy", true)
	rec := doJSON(t, router, "POST", "/api/v1/jobs", payload)
	jobID, _ := decodeJSON(t, rec)["id"].(string)

	if rec := doJSON(t, router, "POST", "/api/v1/jobs/"+jobID+"/run", ""); rec.Code != http.StatusAccepted {
		t.Fatalf("run status = %d, want 202", rec.Code)
	}
	waitForRunState(t, router, jobID, syncjob.RunRunning)

	if rec := doJSON(t, router, "PATCH", "/api/v1/jobs/"+jobID, `{"name": "Renamed"}`); rec.Code != http.StatusConflict {
		t.Errorf("PATCH running job = %d %s, want 409", rec.Code, rec.Body.String())
	}
	if rec := doJSON(t, router, "DELETE", "/api/v1/jobs/"+jobID, ""); rec.Code != http.StatusConflict {
		t.Errorf("DELETE running job = %d %s, want 409", rec.Code, rec.Body.String())
	}

	// 运行结束（gate 释放 → 扫描失败）后恢复可改可删。
	close(gate)
	waitForRunState(t, router, jobID, syncjob.RunSucceeded, syncjob.RunFailed)
	if rec := doJSON(t, router, "PATCH", "/api/v1/jobs/"+jobID, `{"name": "Renamed"}`); rec.Code != http.StatusOK {
		t.Errorf("PATCH after run = %d %s, want 200", rec.Code, rec.Body.String())
	}
	if rec := doJSON(t, router, "DELETE", "/api/v1/jobs/"+jobID, ""); rec.Code != http.StatusNoContent {
		t.Errorf("DELETE after run = %d, want 204", rec.Code)
	}
}

// 被 Job 引用的 Source 删除返回 409；Job 删除后可正常删除 Source。
func TestSourceDeleteBlockedByJobAPI(t *testing.T) {
	router := newJobRouter(t, fakeJobRemote{})
	sourceID := createSourceViaAPI(t, router, "InUse", true)
	payload, _ := jobPayload(t, "Holder", sourceID, "copy", true)
	rec := doJSON(t, router, "POST", "/api/v1/jobs", payload)
	jobID, _ := decodeJSON(t, rec)["id"].(string)

	rec = doJSON(t, router, "DELETE", "/api/v1/sources/"+sourceID, "")
	if rec.Code != http.StatusConflict {
		t.Fatalf("delete referenced source = %d, want 409, body = %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "sync job") {
		t.Errorf("conflict body should mention sync jobs: %s", rec.Body.String())
	}

	if rec := doJSON(t, router, "DELETE", "/api/v1/jobs/"+jobID, ""); rec.Code != http.StatusNoContent {
		t.Fatalf("delete job status = %d, want 204", rec.Code)
	}
	if rec := doJSON(t, router, "DELETE", "/api/v1/sources/"+sourceID, ""); rec.Code != http.StatusNoContent {
		t.Errorf("delete unreferenced source = %d, want 204", rec.Code)
	}
}

// 完成事件的失败明细负载（R31）：真实 SQLite 下验证 Runner 在 finalize
// 时按 status=failed / 写入序 / FailedItemLimit 查询失败明细并随完成
// 事件发布——通知模块因此零存储依赖。公共层不做领域解释：Detail 的
// 领域扩展字段原样透传。
package job_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"tinysync/internal/job"
	jobsqlite "tinysync/internal/job/sqlite"
	"tinysync/internal/storage"
)

// fixedNow 是失败明细测试的固定时钟基准。
var fixedNow = time.Unix(1757879400, 0).UTC()

// itemRecorderExecutor 在 Run 内写运行明细后按配置失败或成功，模拟
// 领域执行器的真实行为（明细先于终态落库）。
type itemRecorderExecutor struct {
	// items 是每轮 Run 要写入的明细（一次性弹出队首）。
	itemsMu sync.Mutex
	items   [][]job.RunItem
	// fail 非 nil 时本轮 Run 以该错误失败。
	failMu sync.Mutex
	fail   error
}

func (e *itemRecorderExecutor) Validate(ctx context.Context, jobID string, cfg json.RawMessage) (json.RawMessage, error) {
	return cfg, nil
}

func (e *itemRecorderExecutor) Run(ctx context.Context, req job.RunRequest) (job.RunResult, error) {
	e.itemsMu.Lock()
	var items []job.RunItem
	if len(e.items) > 0 {
		items = e.items[0]
		e.items = e.items[1:]
	}
	e.itemsMu.Unlock()
	for _, item := range items {
		// 与真实领域执行器一致：明细标记本轮 run ID（job_run_items
		// 对 job_runs 有外键）。
		item.RunID = req.RunID
		if err := req.Items.RecordItem(ctx, item); err != nil {
			return job.RunResult{}, fmt.Errorf("record item: %w", err)
		}
	}
	e.failMu.Lock()
	fail := e.fail
	e.failMu.Unlock()
	return job.RunResult{}, fail
}

func (e *itemRecorderExecutor) enqueue(items []job.RunItem, fail error) {
	e.itemsMu.Lock()
	e.items = append(e.items, items)
	e.itemsMu.Unlock()
	e.failMu.Lock()
	e.fail = fail
	e.failMu.Unlock()
}

// captureEvents 记录完成事件。
type captureEvents struct {
	mu     sync.Mutex
	events []job.RunCompletion
}

func (h *captureEvents) OnRunCompleted(run job.RunCompletion) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.events = append(h.events, run)
}

func (h *captureEvents) snapshot() []job.RunCompletion {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]job.RunCompletion(nil), h.events...)
}

func newFailedItemsEnv(t *testing.T) (*job.Runner, *itemRecorderExecutor, *captureEvents, string) {
	t.Helper()
	db, err := storage.Open(t.TempDir())
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := storage.Ensure(context.Background(), db); err != nil {
		t.Fatalf("storage.Ensure: %v", err)
	}
	jobRepo := jobsqlite.NewRepository(db)
	runRepo := jobsqlite.NewRunRepository(db)
	exec := &itemRecorderExecutor{}
	runner := job.NewRunner(jobRepo, runRepo, map[job.JobKind]job.Executor{
		job.KindOCI: exec,
	})
	hook := &captureEvents{}
	runner.CompletionHook = hook

	now := fixedNow
	j := job.Job{
		ID: "job_oci", Name: "cache", Kind: job.KindOCI,
		Enabled: true, Config: []byte("{}"),
		CreatedAt: now, UpdatedAt: now,
	}
	if err := jobRepo.Create(context.Background(), j); err != nil {
		t.Fatalf("create job: %v", err)
	}
	return runner, exec, hook, j.ID
}

// 失败 run 的完成事件携带失败明细：只含 status=failed 行（写入序）、
// total 为失败总数、Detail 领域扩展字段原样保留；成功 run 不携带。
func TestRunnerPublishesFailedItemsInCompletion(t *testing.T) {
	runner, exec, hook, jobID := newFailedItemsEnv(t)
	ctx := context.Background()

	exec.enqueue([]job.RunItem{
		{RunID: "", Target: "mirror.local/a:1", Action: "copy", Status: job.ItemSucceeded, Bytes: 10},
		{RunID: "", Target: "mirror.local/b:1", Action: "copy", Status: job.ItemFailed, Error: "write failed",
			Detail: json.RawMessage(`{"src":"docker.io/b:1","status":"failed"}`)},
		{RunID: "", Target: "mirror.local/c:1", Action: "skip", Status: job.ItemSkipped},
		{RunID: "", Target: "mirror.local/d:1", Action: "copy", Status: job.ItemFailed, Error: "denied",
			Detail: json.RawMessage(`{"src":"docker.io/d:1","platform_results":[{"platform":"linux/amd64","status":"failed","error":"denied"}]}`)},
	}, errors.New("2 of 4 image copies failed"))

	runID, err := runner.Start(ctx, jobID)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if _, err := runner.Wait(ctx, runID); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	events := hook.snapshot()
	if len(events) != 1 {
		t.Fatalf("completion events = %d, want 1", len(events))
	}
	event := events[0]
	if event.State != job.RunFailed {
		t.Fatalf("event state = %s, want failed", event.State)
	}
	if len(event.FailedItems) != 2 || event.FailedItemsTotal != 2 {
		t.Fatalf("failed items = (%d carried, total %d), want 2/2", len(event.FailedItems), event.FailedItemsTotal)
	}
	// 写入序，公共字段与领域 Detail 各就其位。
	first, second := event.FailedItems[0], event.FailedItems[1]
	if first.Target != "mirror.local/b:1" || first.Error != "write failed" {
		t.Errorf("first failed item = (%s, %q), want (mirror.local/b:1, write failed)", first.Target, first.Error)
	}
	if second.Target != "mirror.local/d:1" || second.Error != "denied" {
		t.Errorf("second failed item = (%s, %q), want (mirror.local/d:1, denied)", second.Target, second.Error)
	}
	var detail struct {
		Src             string `json:"src"`
		PlatformResults []struct {
			Platform string `json:"platform"`
			Status   string `json:"status"`
		} `json:"platform_results"`
	}
	if err := json.Unmarshal(second.Detail, &detail); err != nil {
		t.Fatalf("unmarshal detail: %v", err)
	}
	if detail.Src != "docker.io/d:1" || len(detail.PlatformResults) != 1 ||
		detail.PlatformResults[0].Platform != "linux/amd64" {
		t.Errorf("detail = %+v, want domain extension fields preserved", detail)
	}

	// 成功 run：无失败明细（failed items 为空、total 0）。
	exec.enqueue([]job.RunItem{
		{RunID: "", Target: "mirror.local/e:1", Action: "copy", Status: job.ItemSucceeded, Bytes: 5},
	}, nil)
	runID, err = runner.Start(ctx, jobID)
	if err != nil {
		t.Fatalf("Start second run: %v", err)
	}
	if _, err := runner.Wait(ctx, runID); err != nil {
		t.Fatalf("Wait second run: %v", err)
	}
	events = hook.snapshot()
	if len(events) != 2 {
		t.Fatalf("completion events = %d, want 2", len(events))
	}
	if event := events[1]; event.State != job.RunSucceeded ||
		len(event.FailedItems) != 0 || event.FailedItemsTotal != 0 {
		t.Errorf("succeeded event = (state %s, %d items, total %d), want succeeded with no failed items",
			event.State, len(event.FailedItems), event.FailedItemsTotal)
	}
}

// 失败明细超过 FailedItemLimit 时完成事件只携带最早 20 条，total 仍为
// 精确总数——通知正文据此渲染截断标注。
func TestRunnerFailedItemsCappedAtLimit(t *testing.T) {
	runner, exec, hook, jobID := newFailedItemsEnv(t)
	ctx := context.Background()

	items := make([]job.RunItem, 0, job.FailedItemLimit+5)
	for i := 0; i < job.FailedItemLimit+5; i++ {
		items = append(items, job.RunItem{
			RunID:  "",
			Target: fmt.Sprintf("mirror.local/img%02d:1", i),
			Action: "copy",
			Status: job.ItemFailed,
			Error:  "boom",
		})
	}
	exec.enqueue(items, errors.New("25 of 25 image copies failed"))

	runID, err := runner.Start(ctx, jobID)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if _, err := runner.Wait(ctx, runID); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	events := hook.snapshot()
	if len(events) != 1 {
		t.Fatalf("completion events = %d, want 1", len(events))
	}
	event := events[0]
	if len(event.FailedItems) != job.FailedItemLimit {
		t.Fatalf("carried failed items = %d, want %d", len(event.FailedItems), job.FailedItemLimit)
	}
	if event.FailedItemsTotal != job.FailedItemLimit+5 {
		t.Errorf("failed items total = %d, want %d", event.FailedItemsTotal, job.FailedItemLimit+5)
	}
	// 写入序的前 20 条。
	if event.FailedItems[0].Target != "mirror.local/img00:1" ||
		event.FailedItems[job.FailedItemLimit-1].Target != fmt.Sprintf("mirror.local/img%02d:1", job.FailedItemLimit-1) {
		t.Errorf("carried items not the earliest %d in write order (first %s, last %s)",
			job.FailedItemLimit, event.FailedItems[0].Target, event.FailedItems[job.FailedItemLimit-1].Target)
	}
}

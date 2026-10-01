package syncjob

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// captureHook 记录收到的完成事件，供断言「恰好一次」与负载内容。
type captureHook struct {
	mu     sync.Mutex
	events []RunCompletion
}

func (h *captureHook) OnRunCompleted(run RunCompletion) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.events = append(h.events, run)
}

func (h *captureHook) snapshot() []RunCompletion {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]RunCompletion(nil), h.events...)
}

// 成功的 run 在终态落库后恰好发布一次完成事件，负载自带 JobName /
// SourceID 快照。
func TestRunnerPublishesCompletionOnSuccess(t *testing.T) {
	env := newRunnerEnv(t, buildRemote(map[string]string{"/a.txt": "v1"}, nil))
	hook := &captureHook{}
	env.runner.CompletionHook = hook
	job := env.mustJob(t, "photos")

	runID, err := env.runner.Start(context.Background(), job.ID)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitForRunState(t, env, runID, RunSucceeded)

	events := hook.snapshot()
	if len(events) != 1 {
		t.Fatalf("completion events = %d, want 1", len(events))
	}
	event := events[0]
	if event.RunID != runID || event.JobID != job.ID {
		t.Fatalf("event ids = %s/%s, want %s/%s", event.RunID, event.JobID, runID, job.ID)
	}
	if event.JobName != job.Name || event.SourceID != job.SourceID {
		t.Fatalf("event job name/source = %s/%s, want %s/%s", event.JobName, event.SourceID, job.Name, job.SourceID)
	}
	if event.State != RunSucceeded || event.Error != "" {
		t.Fatalf("event state = %s error = %q, want succeeded/empty", event.State, event.Error)
	}
	if event.Stats.FilesCreated != 1 {
		t.Fatalf("event stats files_created = %d, want 1", event.Stats.FilesCreated)
	}
	if event.FinishedAt.Before(event.StartedAt) {
		t.Fatalf("event finished_at %s before started_at %s", event.FinishedAt, event.StartedAt)
	}
}

// OpenRemote 失败也是一次真实执行过的 failed run：同样发布完成事件。
func TestRunnerPublishesCompletionOnOpenRemoteFailure(t *testing.T) {
	env := newRunnerEnv(t, buildRemote(map[string]string{}, nil))
	env.creds.openErr = errors.New("dial timeout")
	hook := &captureHook{}
	env.runner.CompletionHook = hook
	job := env.mustJob(t, "photos")

	runID, err := env.runner.Start(context.Background(), job.ID)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitForRunState(t, env, runID, RunFailed)

	events := hook.snapshot()
	if len(events) != 1 {
		t.Fatalf("completion events = %d, want 1", len(events))
	}
	if events[0].State != RunFailed {
		t.Fatalf("event state = %s, want failed", events[0].State)
	}
	if events[0].Error == "" {
		t.Fatal("failed event should carry error")
	}
}

// 终态落库失败不发布完成事件：数据库仍显示 running 时发出完成通知
// 会造成两边事实不一致（ADR 0006）。
func TestRunnerDoesNotPublishWhenFinalizeFails(t *testing.T) {
	env := newRunnerEnv(t, buildRemote(map[string]string{"/a.txt": "v1"}, nil))
	hook := &captureHook{}
	env.runner.CompletionHook = hook
	job := env.mustJob(t, "photos")

	runID, err := env.runner.Start(context.Background(), job.ID)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	env.history.finalizeErr = errors.New("disk full")
	if _, err := env.runner.Wait(context.Background(), runID); err != nil {
		t.Fatalf("Wait: %v", err)
	}

	if events := hook.snapshot(); len(events) != 0 {
		t.Fatalf("completion events = %d, want 0 when finalize fails", len(events))
	}
}

// skipped（调度未执行）不产生完成事件：它不是「任务完成」。
func TestRunnerSkippedRunDoesNotPublishCompletion(t *testing.T) {
	release := make(chan struct{})
	env := newRunnerEnv(t, &blockingRemote{release: release})
	hook := &captureHook{}
	env.runner.CompletionHook = hook
	job := env.mustJob(t, "photos")

	first, err := env.runner.Start(context.Background(), job.ID)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	// 等第一轮真正占用 active 后再触发调度，确保记 skipped。
	waitForRunState(t, env, first, RunRunning)
	occurrence := time.Now().UTC()
	if _, err := env.runner.StartScheduled(context.Background(), job.ID, TriggerCron, occurrence); err != nil {
		t.Fatalf("StartScheduled: %v", err)
	}
	close(release)
	waitForRunState(t, env, first, RunSucceeded)

	// 找到 skipped run 并确认无事件。
	runs, _, err := env.history.List(context.Background(), RunFilter{JobID: job.ID})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	skipped := 0
	for _, rec := range runs {
		if rec.State == RunSkipped {
			skipped++
		}
	}
	if skipped != 1 {
		t.Fatalf("skipped runs = %d, want 1", skipped)
	}
	// 成功那轮发布一次；skipped 不追加。
	events := hook.snapshot()
	if len(events) != 1 || events[0].State != RunSucceeded {
		t.Fatalf("completion events = %+v, want exactly one succeeded", events)
	}
}

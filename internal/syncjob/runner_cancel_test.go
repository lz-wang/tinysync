package syncjob

import (
	"context"
	"errors"
	"sort"
	"testing"
	"time"
)

// waitForRunState 轮询等待 run 达到指定终态（memRunRepo 即时可见，轮询
// 只为跨 goroutine 的可见时序留出余量）。
func waitForRunState(t *testing.T, env *runnerEnv, runID string, want RunState) RunRecord {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		rec, err := env.history.Get(context.Background(), runID)
		if err == nil {
			if rec.State == want {
				return rec
			}
			// 终态互斥：到达其他终态即不再等待。
			switch rec.State {
			case RunSucceeded, RunFailed, RunSkipped, RunCanceled:
				t.Fatalf("run %s reached %s, want %s (error=%q)", runID, rec.State, want, rec.Error)
			}
		} else if !errors.Is(err, ErrRunUnknown) {
			t.Fatalf("get run %s: %v", runID, err)
		}
		if time.Now().After(deadline) {
			t.Fatalf("run %s did not reach %s within deadline", runID, want)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// waitForPending 轮询等待指定远端文件被派发（managed 登记 pending）。
func waitForPending(t *testing.T, m *inMemoryManaged, remotePath string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		m.mu.Lock()
		_, ok := m.files[remotePath]
		m.mu.Unlock()
		if ok {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("file %s was never dispatched (no pending metadata)", remotePath)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// 手动停止的完整链路：Cancel 以 ErrRunCanceled 触发取消 → 在途传输中断
// → 终态收敛为 canceled（而非 failed）；active 期间重复取消幂等；run
// 结束后 Cancel 返回 ErrRunNotActive；取消不破坏 Job 协调位，下一轮
// 正常运行。
func TestRunnerCancelRecordsCanceled(t *testing.T) {
	remote := buildRemote(map[string]string{"/a.txt": "v1", "/b.bin": "big"}, nil)
	remote.overrides["/b.bin"] = &cancelAwareReader{rel: make(chan struct{}), cancelled: make(chan struct{})}
	env := newRunnerEnv(t, remote)
	job := env.mustJob(t, "stop-me")

	runID, err := env.runner.Start(context.Background(), job.ID)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	// 等 b.bin 真正进入传输（pending 已登记），取消面对的是在途下载。
	waitForPending(t, env.managed, "/b.bin")

	// 幂等：active 期间重复取消返回 nil。
	for range 3 {
		if err := env.runner.Cancel(runID); err != nil {
			t.Fatalf("Cancel: %v", err)
		}
	}

	rec := waitForRunState(t, env, runID, RunCanceled)
	if rec.Error != ErrRunCanceled.Error() {
		t.Errorf("canceled run error = %q, want %q", rec.Error, ErrRunCanceled.Error())
	}
	if rec.FinishedAt == nil {
		t.Error("canceled run FinishedAt = nil, want set")
	}
	// run 结束后：不在 active，返回 ErrRunNotActive。
	if err := env.runner.Cancel(runID); !errors.Is(err, ErrRunNotActive) {
		t.Errorf("Cancel after finish = %v, want ErrRunNotActive", err)
	}
	// 未知 run 同样 ErrRunNotActive（API 层结合持久化历史区分 404 / 409）。
	if err := env.runner.Cancel("run_nope"); !errors.Is(err, ErrRunNotActive) {
		t.Errorf("Cancel unknown = %v, want ErrRunNotActive", err)
	}

	// 取消只针对单个 run：Job 协调位已释放，下一轮正常运行（b.bin 的
	// override 一次性生效已消费，本轮从 contents 正常下载）。
	runID2, err := env.runner.Start(context.Background(), job.ID)
	if err != nil {
		t.Fatalf("restart after cancel: %v", err)
	}
	waitForRunState(t, env, runID2, RunSucceeded)
}

// Shutdown 引发的取消不是用户行为：终态保持 failed（cancel(nil)，cause
// 退化为 context.Canceled），与 canceled 的「用户主动停止」语义区分。
func TestRunnerShutdownCancelStaysFailed(t *testing.T) {
	remote := buildRemote(map[string]string{"/a.txt": "v1", "/b.bin": "big"}, nil)
	remote.overrides["/b.bin"] = &cancelAwareReader{rel: make(chan struct{}), cancelled: make(chan struct{})}
	env := newRunnerEnv(t, remote)
	job := env.mustJob(t, "shutdown-me")

	runID, err := env.runner.Start(context.Background(), job.ID)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitForPending(t, env.managed, "/b.bin")

	if err := env.runner.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	rec := waitForRunState(t, env, runID, RunFailed)
	if rec.Error == ErrRunCanceled.Error() {
		t.Errorf("shutdown-interrupted run error = %q, want failure text", rec.Error)
	}
}

// 用户手动取消时，被中断的在途下载在明细中如实记 canceled；取消前已
// 收敛的文件保持 succeeded；未派发的文件不留明细（append-only 审计）。
func TestRunRecordsCanceledItemOnUserCancel(t *testing.T) {
	f := newEngineFixture(t, ModeCopy)
	runCtx, cancelRun := context.WithCancelCause(context.Background())
	defer cancelRun(nil)

	remote := buildRemote(map[string]string{"/a.txt": "v1", "/b.bin": "big", "/c.txt": "v1"}, nil)
	remote.overrides["/b.bin"] = &cancelAwareReader{rel: make(chan struct{}), cancelled: make(chan struct{})}
	// buildRemote 以 map 迭代构造 entries，顺序不定；显式排序固定派发
	// 顺序：a.txt（成功收敛）→ b.bin（阻塞在途）→ c.txt（永不派发）。
	entries := remote.entries["/"]
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	items := &memItems{}
	done := make(chan error, 1)
	go func() {
		_, err := Run(runCtx, RunOptions{
			Remote:  remote,
			Job:     f.job,
			Managed: f.managed,
			Items:   items,
			RunID:   "run_cx",
			// 串行传输：a.txt 收敛后 b.bin 才派发，保证断言确定性。
			Transfers: NewTransferLimiter(1),
		})
		done <- err
	}()
	// b.bin 已派发（blocking read 生效中）→ 模拟用户手动停止。
	waitForPending(t, f.managed, "/b.bin")
	cancelRun(ErrRunCanceled)

	if err := <-done; err == nil {
		t.Fatal("Run after user cancel = nil, want cancellation error")
	}
	got := items.all()
	var succeededA, canceledB int
	for _, item := range got {
		switch item.Path {
		case "a.txt":
			if item.Status != ItemSucceeded {
				t.Errorf("a.txt item = %+v, want succeeded (already converged)", item)
			}
			succeededA++
		case "b.bin":
			if item.Status != ItemCanceled {
				t.Errorf("b.bin item = %+v, want canceled", item)
			}
			canceledB++
		case "c.txt":
			t.Errorf("c.txt should have no item (never dispatched), got %+v", item)
		}
	}
	if succeededA != 1 || canceledB != 1 {
		t.Errorf("items = (%d a.txt, %d b.bin), want (1, 1); all=%+v", succeededA, canceledB, got)
	}
}

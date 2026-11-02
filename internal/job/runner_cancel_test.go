package job

import (
	"context"
	"errors"
	"strings"
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

// Cancel 中断的是 run 自身的取消根：取消沿 runCtx 传播到执行器（拨号、
// 传输等全部挂在该链上），终态收敛为 canceled 而非 failed。
func TestRunnerCancelReachesExecutor(t *testing.T) {
	env := newRunnerEnv(t)
	release := env.exec.blockRun()
	job := env.mustJob(t, "cancel-me")
	ctx := context.Background()

	runID, err := env.runner.Start(ctx, job.ID)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	env.exec.waitEntered(t)
	if err := env.runner.Cancel(runID); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	final, err := env.runner.Wait(ctx, runID)
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if final.State != RunCanceled || final.Error != ErrRunCanceled.Error() {
		t.Errorf("final = %s/%q, want canceled with user-stop reason", final.State, final.Error)
	}
	if !env.exec.cancelledRun() {
		t.Error("cancellation did not propagate into the executor ctx")
	}
	close(release)
	if err := env.runner.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
}

// 手动停止的完整链路：active 期间重复取消幂等；run 结束后 Cancel
// 返回 ErrRunNotActive；取消不破坏 Job 协调位，下一轮正常运行。
func TestRunnerCancelRecordsCanceled(t *testing.T) {
	env := newRunnerEnv(t)
	release := env.exec.blockRun()
	job := env.mustJob(t, "stop-me")
	ctx := context.Background()

	runID, err := env.runner.Start(ctx, job.ID)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	env.exec.waitEntered(t)

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

	// 取消只针对单个 run：Job 协调位已释放，下一轮正常运行（阻塞
	// 一次性生效已消费，本轮立即收敛）。
	runID2, err := env.runner.Start(ctx, job.ID)
	if err != nil {
		t.Fatalf("restart after cancel: %v", err)
	}
	waitForRunState(t, env, runID2, RunSucceeded)
	close(release)
	if err := env.runner.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
}

// Shutdown 引发的取消不是用户行为：终态保持 failed（cancel(nil)，cause
// 退化为 context.Canceled），与 canceled 的「用户主动停止」语义区分。
func TestRunnerShutdownCancelStaysFailed(t *testing.T) {
	env := newRunnerEnv(t)
	release := env.exec.blockRun()
	job := env.mustJob(t, "shutdown-me")
	ctx := context.Background()

	runID, err := env.runner.Start(ctx, job.ID)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	env.exec.waitEntered(t)

	if err := env.runner.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	rec := waitForRunState(t, env, runID, RunFailed)
	if rec.Error == ErrRunCanceled.Error() {
		t.Errorf("shutdown-interrupted run error = %q, want failure text", rec.Error)
	}
	if !strings.Contains(rec.Error, context.Canceled.Error()) {
		t.Errorf("shutdown-interrupted run error = %q, want context canceled cause", rec.Error)
	}
	close(release)
}

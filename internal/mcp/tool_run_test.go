package mcp

import (
	"strings"
	"testing"
	"time"

	"tinysync/internal/auth"
	"tinysync/internal/source"
	"tinysync/internal/syncjob"
)

// runFixture 构造启用的 Copy Job（远端不可达：运行会收敛为 failed，
// 正好用于状态查询断言）与一个禁用 Job。
func runFixture(t *testing.T) *toolsEnv {
	t.Helper()
	webdav := source.Source{
		ID:   "src_mcp_run",
		Name: "Run WebDAV",
		Type: source.TypeWebDAV,
		Config: source.Config{
			WebDAV: &source.WebDAVConfig{Endpoint: "https://run.invalid/dav", Username: "mcp"},
		},
		Enabled: true,
	}
	job := syncjob.Job{
		ID: "job_mcp_run", Name: "run job", SourceID: webdav.ID,
		RemoteRoot: "/", LocalRoot: t.TempDir(), Mode: syncjob.ModeCopy,
		Enabled:  true,
		Schedule: syncjob.Schedule{Type: syncjob.ScheduleManual},
	}
	disabled := syncjob.Job{
		ID: "job_mcp_disabled", Name: "disabled job", SourceID: webdav.ID,
		RemoteRoot: "/", LocalRoot: t.TempDir(), Mode: syncjob.ModeCopy,
		Enabled:  false,
		Schedule: syncjob.Schedule{Type: syncjob.ScheduleManual},
	}
	return newToolsEnv(t, []source.Source{webdav}, []syncjob.Job{job, disabled})
}

func TestMCPRunSyncAuthorization(t *testing.T) {
	env := runFixture(t)

	t.Run("run-only token can run_sync but not get_sync_run", func(t *testing.T) {
		session := env.connect([]auth.Scope{auth.ScopeRun})
		var result runSyncResult
		raw := mustCallTool(t, session, "run_sync", runSyncInput{JobID: "job_mcp_run"})
		if err := remarshal(raw, &result); err != nil {
			t.Fatalf("decode run result: %v", err)
		}
		if !strings.HasPrefix(result.RunID, "run_") {
			t.Fatalf("run_id = %q, want run_ prefix", result.RunID)
		}
		_, res, err := callTool(session, "get_sync_run", RunIDInput{RunID: result.RunID})
		if text := requireToolError(t, res, err); !strings.Contains(text, "permission denied") {
			t.Fatalf("get_sync_run error = %q, want permission denied", text)
		}
	})
	t.Run("read token can get_sync_run but not run_sync", func(t *testing.T) {
		session := env.connect([]auth.Scope{auth.ScopeRead})
		_, res, err := callTool(session, "run_sync", runSyncInput{JobID: "job_mcp_run"})
		if text := requireToolError(t, res, err); !strings.Contains(text, "permission denied") {
			t.Fatalf("run_sync error = %q, want permission denied", text)
		}
	})
	t.Run("admin can do both", func(t *testing.T) {
		session := env.connect([]auth.Scope{auth.ScopeAdmin})
		raw := mustCallTool(t, session, "run_sync", runSyncInput{JobID: "job_mcp_run"})
		var result runSyncResult
		if err := remarshal(raw, &result); err != nil {
			t.Fatalf("decode run result: %v", err)
		}
		// 运行是异步的：start 立即返回，等待终态后查询。
		runID := result.RunID
		var detail runDetail
		deadline := time.Now().Add(10 * time.Second)
		for {
			raw := mustCallTool(t, session, "get_sync_run", RunIDInput{RunID: runID})
			if err := remarshal(raw, &detail); err != nil {
				t.Fatalf("decode run detail: %v", err)
			}
			if detail.State == string(syncjob.RunSucceeded) || detail.State == string(syncjob.RunFailed) {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("run %s did not converge, state = %s", runID, detail.State)
			}
			time.Sleep(50 * time.Millisecond)
		}
		// 远端不可达收敛为 failed，error 文案非空。
		if detail.State != string(syncjob.RunFailed) || detail.Error == "" {
			t.Fatalf("state = %s error = %q, want failed with error", detail.State, detail.Error)
		}
		if detail.JobID != "job_mcp_run" || detail.FinishedAt == "" {
			t.Fatalf("run detail = %+v, want job_id and finished_at", detail)
		}
	})
}

func TestMCPRunSyncErrors(t *testing.T) {
	env := runFixture(t)
	session := env.connect([]auth.Scope{auth.ScopeRun, auth.ScopeRead})

	t.Run("unknown job", func(t *testing.T) {
		_, res, err := callTool(session, "run_sync", runSyncInput{JobID: "job_missing"})
		if text := requireToolError(t, res, err); !strings.Contains(text, "job not found") {
			t.Fatalf("error = %q, want job not found", text)
		}
	})
	t.Run("disabled job", func(t *testing.T) {
		_, res, err := callTool(session, "run_sync", runSyncInput{JobID: "job_mcp_disabled"})
		if text := requireToolError(t, res, err); !strings.Contains(text, "job is disabled") {
			t.Fatalf("error = %q, want disabled", text)
		}
	})
	t.Run("empty job_id is invalid input", func(t *testing.T) {
		_, res, err := callTool(session, "run_sync", runSyncInput{JobID: ""})
		if text := requireToolError(t, res, err); !strings.Contains(text, "invalid input") {
			t.Fatalf("error = %q, want invalid input", text)
		}
	})
	t.Run("unknown run id", func(t *testing.T) {
		_, res, err := callTool(session, "get_sync_run", RunIDInput{RunID: "run_missing"})
		if text := requireToolError(t, res, err); !strings.Contains(text, "run not found") {
			t.Fatalf("error = %q, want run not found", text)
		}
	})
}

package mcp

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"tinysync/internal/auth"
	"tinysync/internal/syncjob"
)

// runSyncInput 是 run_sync 的唯一输入：只接受现有 job_id，不允许
// 调用方覆盖任何同步配置。
type runSyncInput struct {
	JobID string `json:"job_id"`
}

// runSyncResult 是 run_sync 的输出：异步启动，立即返回 run_id；
// 后续经 get_sync_run 轮询状态。
type runSyncResult struct {
	RunID string `json:"run_id"`
}

// runStatsDetail 是一轮运行的统计摘要。
type runStatsDetail struct {
	FilesTotal       int   `json:"files_total"`
	FilesCreated     int   `json:"files_created"`
	FilesUpdated     int   `json:"files_updated"`
	FilesDeleted     int   `json:"files_deleted"`
	FilesSkipped     int   `json:"files_skipped"`
	BytesTransferred int64 `json:"bytes_transferred"`
}

// runDetail 是 get_sync_run 的输出。
type runDetail struct {
	RunID      string         `json:"run_id"`
	JobID      string         `json:"job_id"`
	State      string         `json:"state"`
	Trigger    string         `json:"trigger,omitempty"`
	StartedAt  string         `json:"started_at"`
	FinishedAt string         `json:"finished_at,omitempty"`
	Stats      runStatsDetail `json:"stats"`
	Error      string         `json:"error,omitempty"`
}

// registerRunTools 注册同步执行工具：run_sync 用 run scope，
// get_sync_run 用 read scope（run ⇏ read 保持成立）。
func registerRunTools(server *mcp.Server, deps Deps) {
	if deps.Runner == nil {
		return
	}
	mcp.AddTool(server, &mcp.Tool{
		Name: "run_sync",
		Description: "Start an already-configured TinySync sync job and return its run_id immediately. " +
			"Side-effect / destructive-capable: mirror jobs may delete local files that this job manages and that no longer exist on the remote. " +
			"Poll get_sync_run with the returned run_id until it reaches succeeded or failed.",
		Annotations: destructiveAnnotations(),
	}, func(ctx context.Context, req *mcp.CallToolRequest, input runSyncInput) (*mcp.CallToolResult, runSyncResult, error) {
		if input.JobID == "" {
			return nil, runSyncResult{}, errors.New("invalid input: job_id is required")
		}
		if err := authorize(ctx, auth.ScopeRun); err != nil {
			return nil, runSyncResult{}, err
		}
		runID, err := deps.Runner.Start(ctx, input.JobID)
		if err != nil {
			return nil, runSyncResult{}, mapRunStartError(err, input.JobID)
		}
		return nil, runSyncResult{RunID: runID}, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "get_sync_run",
		Description: "Get the state, stats and failure reason of one sync run by run_id (states: running / succeeded / failed / skipped).",
		Annotations: readOnlyAnnotations(),
	}, func(ctx context.Context, req *mcp.CallToolRequest, input RunIDInput) (*mcp.CallToolResult, runDetail, error) {
		if input.RunID == "" {
			return nil, runDetail{}, errors.New("invalid input: run_id is required")
		}
		if err := authorize(ctx, auth.ScopeRead); err != nil {
			return nil, runDetail{}, err
		}
		rec, err := deps.Runner.GetRun(ctx, input.RunID)
		if err != nil {
			if errors.Is(err, syncjob.ErrRunUnknown) {
				return nil, runDetail{}, fmt.Errorf("run not found: %s", input.RunID)
			}
			return nil, runDetail{}, errors.New("internal error")
		}
		return nil, toRunDetail(rec), nil
	})
}

// mapRunStartError 把 Runner 的启动错误转换为稳定、可供 Agent 判断
// 的 tool error 文案：确定性失败（不存在 / 禁用）与瞬时失败（已在
// 运行 / 并发满 / 变更互斥）可区分，不统一映射为 internal error。
func mapRunStartError(err error, jobID string) error {
	switch {
	case errors.Is(err, syncjob.ErrNotFound):
		return fmt.Errorf("job not found: %s", jobID)
	case errors.Is(err, syncjob.ErrJobDisabled):
		return fmt.Errorf("job is disabled: %s", jobID)
	case errors.Is(err, syncjob.ErrSourceDisabled):
		return fmt.Errorf("job is disabled: its source is disabled (%s)", jobID)
	case errors.Is(err, syncjob.ErrRunActive):
		return fmt.Errorf("job is already running: %s (retry after it finishes)", jobID)
	case errors.Is(err, syncjob.ErrConcurrencyLimit):
		return errors.New("concurrency limit reached (retry later)")
	case errors.Is(err, syncjob.ErrJobMutating):
		return fmt.Errorf("job is being modified: %s (retry shortly)", jobID)
	case errors.Is(err, syncjob.ErrShuttingDown):
		return errors.New("server is shutting down")
	default:
		return errors.New("internal error")
	}
}

// toRunDetail 转换运行记录；时间输出 RFC3339。
func toRunDetail(rec syncjob.RunRecord) runDetail {
	detail := runDetail{
		RunID:     rec.ID,
		JobID:     rec.JobID,
		State:     string(rec.State),
		Trigger:   string(rec.Trigger),
		StartedAt: rec.StartedAt.Format(time.RFC3339),
		Stats: runStatsDetail{
			FilesTotal:       rec.Stats.FilesTotal,
			FilesCreated:     rec.Stats.FilesCreated,
			FilesUpdated:     rec.Stats.FilesUpdated,
			FilesDeleted:     rec.Stats.FilesDeleted,
			FilesSkipped:     rec.Stats.FilesSkipped,
			BytesTransferred: rec.Stats.BytesTransferred,
		},
		Error: rec.Error,
	}
	if rec.FinishedAt != nil {
		detail.FinishedAt = rec.FinishedAt.Format(time.RFC3339)
	}
	return detail
}

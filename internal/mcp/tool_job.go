package mcp

import (
	"context"
	"errors"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"tinysync/internal/auth"
	"tinysync/internal/syncjob"
)

// JobIDInput 是按 Job 定位的工具输入。
type JobIDInput struct {
	JobID string `json:"job_id"`
}

// RunIDInput 是按运行定位的工具输入。
type RunIDInput struct {
	RunID string `json:"run_id"`
}

// registerJobTools 注册只读 Job 发现工具（run_sync / get_sync_run
// 见 tool_run.go）。
func registerJobTools(server *mcp.Server, deps Deps) {
	if deps.Jobs == nil {
		return
	}
	mcp.AddTool(server, &mcp.Tool{
		Name:        "list_jobs",
		Description: "List TinySync sync jobs (id, name, source, mode, enabled). Use get_job for the full read-only configuration.",
		Annotations: readOnlyAnnotations(),
	}, func(ctx context.Context, req *mcp.CallToolRequest, input ListInput) (*mcp.CallToolResult, listJobsResult, error) {
		limit, offset, err := input.normalize()
		if err != nil {
			return nil, listJobsResult{}, fmt.Errorf("invalid input: %w", err)
		}
		if err := authorize(ctx, auth.ScopeRead); err != nil {
			return nil, listJobsResult{}, err
		}
		jobs, err := deps.Jobs.List(ctx)
		if err != nil {
			return nil, listJobsResult{}, errors.New("internal error")
		}
		result := listJobsResult{Total: len(jobs), Offset: offset}
		end := min(offset+limit, len(jobs))
		for _, j := range jobs[min(offset, len(jobs)):end] {
			result.Jobs = append(result.Jobs, toJobSummary(j))
		}
		return nil, result, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "get_job",
		Description: "Get the full read-only configuration of one sync job by job_id (roots, mode copy/mirror, include/exclude filters, schedule).",
		Annotations: readOnlyAnnotations(),
	}, func(ctx context.Context, req *mcp.CallToolRequest, input JobIDInput) (*mcp.CallToolResult, jobDetail, error) {
		if input.JobID == "" {
			return nil, jobDetail{}, errors.New("invalid input: job_id is required")
		}
		if err := authorize(ctx, auth.ScopeRead); err != nil {
			return nil, jobDetail{}, err
		}
		job, err := deps.Jobs.Get(ctx, input.JobID)
		if err != nil {
			if errors.Is(err, syncjob.ErrNotFound) {
				return nil, jobDetail{}, fmt.Errorf("job not found: %s", input.JobID)
			}
			return nil, jobDetail{}, errors.New("internal error")
		}
		return nil, toJobDetail(job), nil
	})
}

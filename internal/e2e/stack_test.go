// e2e 测试共享的统一任务装配：Repository / File 执行器 / Service /
// Runner / Scheduler 一站式构造，供各协议 e2e 环境复用。
package e2e

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"testing"

	"tinysync/internal/job"
	jobsqlite "tinysync/internal/job/sqlite"
	"tinysync/internal/source"
	"tinysync/internal/syncjob"
	managedsqlite "tinysync/internal/syncjob/sqlite"
)

// fileJobStack 是统一任务的 e2e 装配栈。
type fileJobStack struct {
	Repo      *jobsqlite.Repository
	Runs      *jobsqlite.RunRepository
	Managed   *managedsqlite.ManagedRepository
	Exec      *syncjob.FileExecutor
	Jobs      *job.Service
	Runner    *syncjob.Runner
	Scheduler *syncjob.Scheduler

	seq int // 同名任务的递增序号（name 唯一约束）
}

// newFileJobStack 按生产装配形态构造统一任务栈。
func newFileJobStack(db *sql.DB, sources *source.Service, dataDir string) *fileJobStack {
	repo := jobsqlite.NewRepository(db)
	runs := jobsqlite.NewRunRepository(db)
	managed := managedsqlite.NewManagedRepository(db)
	exec := syncjob.NewFileExecutor(sources, repo, managed, dataDir)
	executors := map[job.JobKind]job.Executor{job.KindFile: exec}
	jobs := job.NewService(repo, executors)
	runner := job.NewRunner(repo, runs, executors)
	return &fileJobStack{
		Repo:      repo,
		Runs:      runs,
		Managed:   managed,
		Exec:      exec,
		Jobs:      jobs,
		Runner:    runner,
		Scheduler: job.NewScheduler(repo, runner, runs),
	}
}

// createFileJob 创建启用的 file 任务（include/exclude 可为空）；
// 同一栈内多次创建同名任务时自动追加序号。
func (s *fileJobStack) createFileJob(ctx context.Context, t *testing.T, name, sourceID, remoteRoot, localRoot string, mode syncjob.Mode, include, exclude []string) job.Job {
	t.Helper()
	s.seq++
	suffixed := fmt.Sprintf("%s-%d", name, s.seq)
	cfg, err := syncjob.MarshalFileConfig(sourceID, remoteRoot, localRoot, mode, include, exclude)
	if err != nil {
		t.Fatalf("marshal file config: %v", err)
	}
	created, err := s.Jobs.Create(ctx, job.CreateInput{
		Name: suffixed, Kind: job.KindFile, Enabled: true, Config: cfg,
	})
	if err != nil {
		t.Fatalf("create file job %q: %v", suffixed, err)
	}
	return created
}

// fileStatsOf 解析运行状态的领域结果摘要（file 统计）。
func fileStatsOf(t *testing.T, st syncjob.RunStatus) syncjob.RunStats {
	t.Helper()
	var stats syncjob.RunStats
	if len(st.Result) > 0 {
		if err := json.Unmarshal(st.Result, &stats); err != nil {
			t.Fatalf("parse run result: %v", err)
		}
	}
	return stats
}

// fileStatsFromRecord 解析持久化运行记录的结果摘要。
func fileStatsFromRecord(t *testing.T, rec job.RunRecord) syncjob.RunStats {
	t.Helper()
	var stats syncjob.RunStats
	if len(rec.Result) > 0 {
		if err := json.Unmarshal(rec.Result, &stats); err != nil {
			t.Fatalf("parse run result: %v", err)
		}
	}
	return stats
}

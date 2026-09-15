package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"

	"tinysync/internal/syncjob"
)

// runColumns 是 sync_runs 的读取列清单（与 scanRun 的扫描顺序一致）。
const runColumns = "id, job_id, trigger_type, scheduled_for, status, " +
	"started_at, finished_at, files_total, files_created, files_updated, " +
	"files_deleted, files_skipped, bytes_transferred, error"

// runItemColumns 是 sync_run_items 的读取列清单。
const runItemColumns = "id, run_id, path, action, status, bytes, error"

// RunRepository 是 syncjob.RunRepository 的 SQLite 实现。
type RunRepository struct {
	db *sql.DB
}

// NewRunRepository 构造 RunRepository；db 需已完成 schema migration。
func NewRunRepository(db *sql.DB) *RunRepository {
	return &RunRepository{db: db}
}

// Insert 实现 syncjob.RunRepository。
func (r *RunRepository) Insert(ctx context.Context, run syncjob.RunRecord) error {
	_, err := r.db.ExecContext(ctx, `INSERT INTO sync_runs
		(id, job_id, trigger_type, scheduled_for, status, started_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		run.ID, run.JobID, string(run.Trigger), nullMillis(run.ScheduledFor),
		string(run.State), run.StartedAt.UnixMilli(),
	)
	return mapRunError("insert run", run.ID, err)
}

// Finalize 实现 syncjob.RunRepository：整体覆盖终态与统计。
func (r *RunRepository) Finalize(ctx context.Context, run syncjob.RunRecord) error {
	res, err := r.db.ExecContext(ctx, `UPDATE sync_runs SET
		status = ?, finished_at = ?, files_total = ?, files_created = ?,
		files_updated = ?, files_deleted = ?, files_skipped = ?,
		bytes_transferred = ?, error = ?
		WHERE id = ?`,
		string(run.State), nullMillis(run.FinishedAt),
		run.Stats.FilesTotal, run.Stats.FilesCreated, run.Stats.FilesUpdated,
		run.Stats.FilesDeleted, run.Stats.FilesSkipped, run.Stats.BytesTransferred,
		run.Error, run.ID,
	)
	if err != nil {
		return fmt.Errorf("finalize run %s: %w", run.ID, err)
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return fmt.Errorf("%w: %s", syncjob.ErrRunUnknown, run.ID)
	}
	return nil
}

// Get 实现 syncjob.RunRepository。
func (r *RunRepository) Get(ctx context.Context, runID string) (syncjob.RunRecord, error) {
	row := r.db.QueryRowContext(ctx,
		"SELECT "+runColumns+" FROM sync_runs WHERE id = ?", runID)
	run, err := scanRun(row)
	return run, mapRunGetError("get run", runID, err)
}

// Latest 实现 syncjob.RunRepository：started_at 倒序首条（rowid 兜底
// 同毫秒并列）。
func (r *RunRepository) Latest(ctx context.Context, jobID string) (syncjob.RunRecord, error) {
	row := r.db.QueryRowContext(ctx,
		"SELECT "+runColumns+" FROM sync_runs WHERE job_id = ? "+
			"ORDER BY started_at DESC, rowid DESC LIMIT 1", jobID)
	run, err := scanRun(row)
	return run, mapRunGetError("latest run of job", jobID, err)
}

// List 实现 syncjob.RunRepository：job / status 过滤 + limit/offset 分页，
// 返回过滤后总数。
func (r *RunRepository) List(ctx context.Context, filter syncjob.RunFilter) ([]syncjob.RunRecord, int, error) {
	where, args := runFilterClause(filter)
	var total int
	if err := r.db.QueryRowContext(ctx,
		"SELECT count(*) FROM sync_runs"+where, args...,
	).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count runs: %w", err)
	}

	// SQLite 语义：LIMIT 负数 = 不限制。Limit<=0 表示调用方未设分页上限
	// （由 API 层收敛默认值），仓库层不截断。
	limit := filter.Limit
	if limit <= 0 {
		limit = -1
	}
	query := "SELECT " + runColumns + " FROM sync_runs" + where +
		" ORDER BY started_at DESC, rowid DESC LIMIT ? OFFSET ?"
	rows, err := r.db.QueryContext(ctx, query,
		append(args, limit, filter.Offset)...)
	if err != nil {
		return nil, 0, fmt.Errorf("list runs: %w", err)
	}
	defer rows.Close()

	var list []syncjob.RunRecord
	for rows.Next() {
		run, err := scanRun(rows)
		if err != nil {
			return nil, 0, fmt.Errorf("scan run: %w", err)
		}
		list = append(list, run)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("iterate runs: %w", err)
	}
	return list, total, nil
}

// runFilterClause 构造 List 的 WHERE 子句与参数（无过滤时为空串）。
func runFilterClause(filter syncjob.RunFilter) (string, []any) {
	var (
		clauses []string
		args    []any
	)
	if filter.JobID != "" {
		clauses = append(clauses, "job_id = ?")
		args = append(args, filter.JobID)
	}
	if filter.Status != "" {
		clauses = append(clauses, "status = ?")
		args = append(args, string(filter.Status))
	}
	if len(clauses) == 0 {
		return "", nil
	}
	where := " WHERE "
	for i, c := range clauses {
		if i > 0 {
			where += " AND "
		}
		where += c
	}
	return where, args
}

// AppendItem 实现 syncjob.RunRepository。
func (r *RunRepository) AppendItem(ctx context.Context, item syncjob.RunItem) error {
	_, err := r.db.ExecContext(ctx, `INSERT INTO sync_run_items
		(run_id, path, action, status, bytes, error)
		VALUES (?, ?, ?, ?, ?, ?)`,
		item.RunID, item.Path, string(item.Action), string(item.Status),
		item.Bytes, item.Error,
	)
	if err != nil {
		return fmt.Errorf("append run item %s: %w", item.Path, err)
	}
	return nil
}

// Items 实现 syncjob.RunRepository：按写入顺序分页，返回该 run 明细总数。
// limit<=0 视为不限制。
func (r *RunRepository) Items(ctx context.Context, runID string, limit, offset int) ([]syncjob.RunItem, int, error) {
	var total int
	if err := r.db.QueryRowContext(ctx,
		"SELECT count(*) FROM sync_run_items WHERE run_id = ?", runID,
	).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count items of run %s: %w", runID, err)
	}

	if limit <= 0 {
		limit = -1
	}
	rows, err := r.db.QueryContext(ctx,
		"SELECT "+runItemColumns+" FROM sync_run_items WHERE run_id = ? "+
			"ORDER BY id ASC LIMIT ? OFFSET ?", runID, limit, offset)
	if err != nil {
		return nil, 0, fmt.Errorf("list items of run %s: %w", runID, err)
	}
	defer rows.Close()

	var list []syncjob.RunItem
	for rows.Next() {
		var (
			item   syncjob.RunItem
			action string
			status string
		)
		if err := rows.Scan(&item.ID, &item.RunID, &item.Path, &action, &status,
			&item.Bytes, &item.Error); err != nil {
			return nil, 0, fmt.Errorf("scan run item: %w", err)
		}
		item.Action = syncjob.RunItemAction(action)
		item.Status = syncjob.RunItemStatus(status)
		list = append(list, item)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("iterate run items: %w", err)
	}
	return list, total, nil
}

// HasRunFor 实现 syncjob.RunRepository：occurrence 消费判定，
// 命中 (job_id, trigger_type, scheduled_for) 索引。
func (r *RunRepository) HasRunFor(ctx context.Context, jobID string, trigger syncjob.RunTrigger, scheduledFor time.Time) (bool, error) {
	var exists bool
	if err := r.db.QueryRowContext(ctx,
		"SELECT EXISTS(SELECT 1 FROM sync_runs WHERE job_id = ? AND trigger_type = ? AND scheduled_for = ?)",
		jobID, string(trigger), scheduledFor.UnixMilli(),
	).Scan(&exists); err != nil {
		return false, fmt.Errorf("check consumed occurrence %s %s: %w", jobID, trigger, err)
	}
	return exists, nil
}

// FailStaleRunning 实现 syncjob.RunRepository：启动恢复把遗留 running
// 收敛为 failed。
func (r *RunRepository) FailStaleRunning(ctx context.Context, finishedAt time.Time, reason string) (int64, error) {
	res, err := r.db.ExecContext(ctx,
		"UPDATE sync_runs SET status = 'failed', finished_at = ?, error = ? "+
			"WHERE status = 'running'",
		finishedAt.UnixMilli(), reason,
	)
	if err != nil {
		return 0, fmt.Errorf("fail stale running: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("fail stale running: %w", err)
	}
	return n, nil
}

// PruneRetention 实现 syncjob.RunRepository：每 Job 保留最近 keepPerJob
// 条 run（started_at 倒序），其余连同 items 级联删除。
func (r *RunRepository) PruneRetention(ctx context.Context, keepPerJob int) error {
	if keepPerJob <= 0 {
		return fmt.Errorf("prune retention: keepPerJob must be positive, got %d", keepPerJob)
	}
	_, err := r.db.ExecContext(ctx, `DELETE FROM sync_runs WHERE id IN (
		SELECT id FROM (
			SELECT id, ROW_NUMBER() OVER (
				PARTITION BY job_id ORDER BY started_at DESC, rowid DESC
			) AS rn
			FROM sync_runs
		) WHERE rn > ?
	)`, keepPerJob)
	if err != nil {
		return fmt.Errorf("prune runs retention %d: %w", keepPerJob, err)
	}
	return nil
}

// scanRun 把一行结果转为领域对象；时间为 UTC。
func scanRun(row rowScanner) (syncjob.RunRecord, error) {
	var (
		run              syncjob.RunRecord
		trigger          string
		scheduledFor     sql.NullInt64
		status           string
		startedAtMillis  int64
		finishedAtMillis sql.NullInt64
	)
	if err := row.Scan(&run.ID, &run.JobID, &trigger, &scheduledFor, &status,
		&startedAtMillis, &finishedAtMillis,
		&run.Stats.FilesTotal, &run.Stats.FilesCreated, &run.Stats.FilesUpdated,
		&run.Stats.FilesDeleted, &run.Stats.FilesSkipped, &run.Stats.BytesTransferred,
		&run.Error); err != nil {
		return syncjob.RunRecord{}, err
	}
	run.Trigger = syncjob.RunTrigger(trigger)
	run.ScheduledFor = timeFromMillis(scheduledFor)
	run.State = syncjob.RunState(status)
	run.StartedAt = time.UnixMilli(startedAtMillis).UTC()
	run.FinishedAt = timeFromMillis(finishedAtMillis)
	return run, nil
}

// mapRunError 包装写操作错误；外键拒绝（Job 不存在）映射为 ErrNotFound。
func mapRunError(op, runID string, err error) error {
	if err == nil {
		return nil
	}
	var se *sqlite.Error
	if errors.As(err, &se) && se.Code() == sqlite3.SQLITE_CONSTRAINT_FOREIGNKEY {
		return fmt.Errorf("%s %s: %w (job does not exist)", op, runID, syncjob.ErrNotFound)
	}
	return fmt.Errorf("%s %s: %w", op, runID, err)
}

// mapRunGetError 统一 no-rows 到 ErrRunUnknown。
func mapRunGetError(op, id string, err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: %s", syncjob.ErrRunUnknown, id)
	}
	if err != nil {
		return fmt.Errorf("%s %s: %w", op, id, err)
	}
	return nil
}

// 编译期接口断言。
var _ syncjob.RunRepository = (*RunRepository)(nil)

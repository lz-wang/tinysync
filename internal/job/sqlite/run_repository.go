package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"

	"tinysync/internal/job"
	"tinysync/internal/storage"
)

// runColumns 是 job_runs 的读取列清单（与 scanRun 的扫描顺序一致）。
const runColumns = "id, job_id, job_kind, trigger_type, scheduled_for, state, " +
	"started_at, finished_at, result_json, error"

// runItemColumns 是 job_run_items 的读取列清单。
const runItemColumns = "id, run_id, action, status, bytes, error, detail_json"

// RunRepository 是 job.RunRepository 的 SQLite 实现。
type RunRepository struct {
	db *sql.DB
}

// NewRunRepository 构造 RunRepository；db 需已完成 schema 初始化。
func NewRunRepository(db *sql.DB) *RunRepository {
	return &RunRepository{db: db}
}

// insertRunSQL 是 job_runs 的插入语句，Insert 与 PersistScheduledRun 共用。
// 运行结果摘要（result_json）由 Finalize 写入，插入时留默认值。
const insertRunSQL = `INSERT INTO job_runs
	(id, job_id, job_kind, trigger_type, scheduled_for, state, started_at, finished_at, error)
	VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`

// insertRunArgs 按插入语句的参数顺序展开运行记录。
func insertRunArgs(run job.RunRecord) []any {
	return []any{
		run.ID, run.JobID, string(run.JobKind), string(run.Trigger), nullMillis(run.ScheduledFor),
		string(run.State), run.StartedAt.UnixMilli(),
		nullMillis(run.FinishedAt), run.Error,
	}
}

// Insert 实现 job.RunRepository：整体落库运行记录。正常运行带
// running 状态（goroutine 启动前写入）；调度跳过的记录直接携带
// skipped 终态、原因与结束时间。
func (r *RunRepository) Insert(ctx context.Context, run job.RunRecord) error {
	_, err := r.db.ExecContext(ctx, insertRunSQL, insertRunArgs(run)...)
	return mapRunError("insert run", run.ID, err)
}

// PersistScheduledRun 实现 job.RunRepository：单个事务内落库运行
// 记录；once 触发时同步写入 jobs.once_consumed_at 并清空 next_run_at
// （once 已消费，无下一次触发），使「run 存在 ⇔ occurrence 已消费」
// 原子成立。非 once 触发只落历史。
func (r *RunRepository) PersistScheduledRun(ctx context.Context, run job.RunRecord) error {
	return storage.WithTx(ctx, r.db, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, insertRunSQL, insertRunArgs(run)...); err != nil {
			return mapRunError("insert run", run.ID, err)
		}
		if run.Trigger == job.TriggerOnce && run.ScheduledFor != nil {
			res, err := tx.ExecContext(ctx,
				"UPDATE jobs SET once_consumed_at = ?, next_run_at = NULL WHERE id = ?",
				run.ScheduledFor.UnixMilli(), run.JobID)
			if err != nil {
				return fmt.Errorf("mark once consumed %s: %w", run.JobID, err)
			}
			if n, err := res.RowsAffected(); err == nil && n == 0 {
				return fmt.Errorf("%w: %s", job.ErrNotFound, run.JobID)
			}
		}
		return nil
	})
}

// Finalize 实现 job.RunRepository：整体覆盖终态与结果摘要。
func (r *RunRepository) Finalize(ctx context.Context, run job.RunRecord) error {
	res, err := r.db.ExecContext(ctx, `UPDATE job_runs SET
		state = ?, finished_at = ?, result_json = ?, error = ?
		WHERE id = ?`,
		string(run.State), nullMillis(run.FinishedAt), resultString(run.Result),
		run.Error, run.ID,
	)
	if err != nil {
		return fmt.Errorf("finalize run %s: %w", run.ID, err)
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return fmt.Errorf("%w: %s", job.ErrRunUnknown, run.ID)
	}
	return nil
}

// Get 实现 job.RunRepository。
func (r *RunRepository) Get(ctx context.Context, runID string) (job.RunRecord, error) {
	row := r.db.QueryRowContext(ctx,
		"SELECT "+runColumns+" FROM job_runs WHERE id = ?", runID)
	run, err := scanRun(row)
	return run, mapRunGetError("get run", runID, err)
}

// Latest 实现 job.RunRepository：started_at 倒序首条（rowid 兜底
// 同毫秒并列）。
func (r *RunRepository) Latest(ctx context.Context, jobID string) (job.RunRecord, error) {
	row := r.db.QueryRowContext(ctx,
		"SELECT "+runColumns+" FROM job_runs WHERE job_id = ? "+
			"ORDER BY started_at DESC, rowid DESC LIMIT 1", jobID)
	run, err := scanRun(row)
	return run, mapRunGetError("latest run of job", jobID, err)
}

// List 实现 job.RunRepository：job / kind / status 过滤 + limit/offset
// 分页，返回过滤后总数。
func (r *RunRepository) List(ctx context.Context, filter job.RunFilter) ([]job.RunRecord, int, error) {
	where, args := runFilterClause(filter)
	var total int
	if err := r.db.QueryRowContext(ctx,
		"SELECT count(*) FROM job_runs"+where, args...,
	).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count runs: %w", err)
	}

	// SQLite 语义：LIMIT 负数 = 不限制。Limit<=0 表示调用方未设分页上限
	//（由 API 层收敛默认值），仓库层不截断。
	limit := filter.Limit
	if limit <= 0 {
		limit = -1
	}
	query := "SELECT " + runColumns + " FROM job_runs" + where +
		" ORDER BY started_at DESC, rowid DESC LIMIT ? OFFSET ?"
	rows, err := r.db.QueryContext(ctx, query,
		append(args, limit, filter.Offset)...)
	if err != nil {
		return nil, 0, fmt.Errorf("list runs: %w", err)
	}
	defer rows.Close()

	var list []job.RunRecord
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
func runFilterClause(filter job.RunFilter) (string, []any) {
	var (
		clauses []string
		args    []any
	)
	if filter.JobID != "" {
		clauses = append(clauses, "job_id = ?")
		args = append(args, filter.JobID)
	}
	if filter.Kind != "" {
		clauses = append(clauses, "job_kind = ?")
		args = append(args, string(filter.Kind))
	}
	if filter.Status != "" {
		clauses = append(clauses, "state = ?")
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

// itemDetail 是 job_run_items.detail_json 的形态：target 为统一展示
// 目标（文件路径 / 镜像引用），其余键为领域扩展字段。
type itemDetail struct {
	Target string `json:"target"`
}

// AppendItem 实现 job.RunRepository：target 与领域扩展字段写入
// detail_json。
func (r *RunRepository) AppendItem(ctx context.Context, item job.RunItem) error {
	detail, err := mergeItemDetail(item.Target, item.Detail)
	if err != nil {
		return err
	}
	_, err = r.db.ExecContext(ctx, `INSERT INTO job_run_items
		(run_id, action, status, bytes, error, detail_json, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		item.RunID, string(item.Action), string(item.Status),
		item.Bytes, item.Error, detail, time.Now().UTC().UnixMilli(),
	)
	if err != nil {
		return fmt.Errorf("append run item %s: %w", item.Target, err)
	}
	return nil
}

// mergeItemDetail 把 target 与领域扩展字段合并为 detail_json。
func mergeItemDetail(target string, extra json.RawMessage) (string, error) {
	merged := map[string]any{"target": target}
	if len(extra) > 0 {
		var fields map[string]any
		if err := json.Unmarshal(extra, &fields); err != nil {
			return "", fmt.Errorf("parse run item detail: %w", err)
		}
		for k, v := range fields {
			if k != "target" {
				merged[k] = v
			}
		}
	}
	data, err := json.Marshal(merged)
	if err != nil {
		return "", fmt.Errorf("marshal run item detail: %w", err)
	}
	return string(data), nil
}

// Items 实现 job.RunRepository：按写入顺序分页，返回该 run 明细总数。
// limit<=0 视为不限制。
func (r *RunRepository) Items(ctx context.Context, runID string, limit, offset int) ([]job.RunItem, int, error) {
	var total int
	if err := r.db.QueryRowContext(ctx,
		"SELECT count(*) FROM job_run_items WHERE run_id = ?", runID,
	).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count items of run %s: %w", runID, err)
	}

	if limit <= 0 {
		limit = -1
	}
	rows, err := r.db.QueryContext(ctx,
		"SELECT "+runItemColumns+" FROM job_run_items WHERE run_id = ? "+
			"ORDER BY id ASC LIMIT ? OFFSET ?", runID, limit, offset)
	if err != nil {
		return nil, 0, fmt.Errorf("list items of run %s: %w", runID, err)
	}
	defer rows.Close()

	var list []job.RunItem
	for rows.Next() {
		var (
			item       job.RunItem
			action     string
			status     string
			detailJSON string
		)
		if err := rows.Scan(&item.ID, &item.RunID, &action, &status,
			&item.Bytes, &item.Error, &detailJSON); err != nil {
			return nil, 0, fmt.Errorf("scan run item: %w", err)
		}
		item.Target, item.Detail = splitItemDetail(detailJSON)
		item.Action = job.RunItemAction(action)
		item.Status = job.RunItemStatus(status)
		list = append(list, item)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("iterate run items: %w", err)
	}
	return list, total, nil
}

// splitItemDetail 把 detail_json 拆回 target 与扩展字段。
func splitItemDetail(data string) (string, json.RawMessage) {
	if data == "" || data == "{}" {
		return "", nil
	}
	var detail itemDetail
	if err := json.Unmarshal([]byte(data), &detail); err != nil {
		return "", nil
	}
	return detail.Target, nil
}

// HasRunFor 实现 job.RunRepository：occurrence 消费判定，
// 命中 (job_id, trigger_type, scheduled_for) 索引。
func (r *RunRepository) HasRunFor(ctx context.Context, jobID string, trigger job.RunTrigger, scheduledFor time.Time) (bool, error) {
	var exists bool
	if err := r.db.QueryRowContext(ctx,
		"SELECT EXISTS(SELECT 1 FROM job_runs WHERE job_id = ? AND trigger_type = ? AND scheduled_for = ?)",
		jobID, string(trigger), scheduledFor.UnixMilli(),
	).Scan(&exists); err != nil {
		return false, fmt.Errorf("check consumed occurrence %s %s: %w", jobID, trigger, err)
	}
	return exists, nil
}

// FailStaleRunning 实现 job.RunRepository：启动恢复把遗留 running
// 收敛为 failed。
func (r *RunRepository) FailStaleRunning(ctx context.Context, finishedAt time.Time, reason string) (int64, error) {
	res, err := r.db.ExecContext(ctx,
		"UPDATE job_runs SET state = 'failed', finished_at = ?, error = ? "+
			"WHERE state = 'running'",
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

// PruneRetention 实现 job.RunRepository：每 Job 保留最近 keepPerJob
// 条 run（started_at 倒序），其余连同 items 级联删除。
func (r *RunRepository) PruneRetention(ctx context.Context, keepPerJob int) error {
	if keepPerJob <= 0 {
		return fmt.Errorf("prune retention: keepPerJob must be positive, got %d", keepPerJob)
	}
	_, err := r.db.ExecContext(ctx, `DELETE FROM job_runs WHERE id IN (
		SELECT id FROM (
			SELECT id, ROW_NUMBER() OVER (
				PARTITION BY job_id ORDER BY started_at DESC, rowid DESC
			) AS rn
			FROM job_runs
		) WHERE rn > ?
	)`, keepPerJob)
	if err != nil {
		return fmt.Errorf("prune runs retention %d: %w", keepPerJob, err)
	}
	return nil
}

// resultString 把结果 JSON 转为落库字符串；nil 归一为 {}。
func resultString(result json.RawMessage) string {
	if len(result) == 0 {
		return "{}"
	}
	return string(result)
}

// scanRun 把一行结果转为领域对象；时间为 UTC。
func scanRun(row rowScanner) (job.RunRecord, error) {
	var (
		run              job.RunRecord
		kind             string
		trigger          string
		scheduledFor     sql.NullInt64
		state            string
		startedAtMillis  int64
		finishedAtMillis sql.NullInt64
		resultJSON       string
	)
	if err := row.Scan(&run.ID, &run.JobID, &kind, &trigger, &scheduledFor, &state,
		&startedAtMillis, &finishedAtMillis, &resultJSON,
		&run.Error); err != nil {
		return job.RunRecord{}, err
	}
	run.JobKind = job.JobKind(kind)
	run.Trigger = job.RunTrigger(trigger)
	run.ScheduledFor = timeFromMillis(scheduledFor)
	run.State = job.RunState(state)
	run.StartedAt = time.UnixMilli(startedAtMillis).UTC()
	run.FinishedAt = timeFromMillis(finishedAtMillis)
	if resultJSON != "" && resultJSON != "{}" {
		run.Result = json.RawMessage(resultJSON)
	}
	return run, nil
}

// mapRunError 包装写操作错误；外键拒绝（Job 不存在）映射为 ErrNotFound。
func mapRunError(op, runID string, err error) error {
	if err == nil {
		return nil
	}
	var se *sqlite.Error
	if errors.As(err, &se) && se.Code() == sqlite3.SQLITE_CONSTRAINT_FOREIGNKEY {
		return fmt.Errorf("%s %s: %w (job does not exist)", op, runID, job.ErrNotFound)
	}
	return fmt.Errorf("%s %s: %w", op, runID, err)
}

// mapRunGetError 统一 no-rows 到 ErrRunUnknown。
func mapRunGetError(op, id string, err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: %s", job.ErrRunUnknown, id)
	}
	if err != nil {
		return fmt.Errorf("%s %s: %w", op, id, err)
	}
	return nil
}

// 编译期接口断言。
var _ job.RunRepository = (*RunRepository)(nil)

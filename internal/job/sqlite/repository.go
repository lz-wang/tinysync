// Package sqlite 实现 job 的 SQLite 持久化：Repository（jobs）与
// RunRepository（job_runs / job_run_items）。name 大小写不敏感唯一；
// 领域配置经 config_json 原样存取（公共层不解释）。
package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"

	"tinysync/internal/job"
	"tinysync/internal/storage"
)

// jobColumns 是 jobs 的读取列清单（与 scanJob 的扫描顺序一致）。
const jobColumns = "id, name, kind, enabled, config_json, " +
	"schedule_type, schedule_value, schedule_timezone, schedule_anchor_at, " +
	"next_run_at, once_consumed_at, created_at, updated_at"

// Repository 是 job.Repository 的 SQLite 实现。
type Repository struct {
	db *sql.DB
	// Now 返回当前时间，用于维护 jobs.next_run_at；测试可注入固定时钟。
	Now func() time.Time
}

// NewRepository 构造 Repository；db 需已完成 schema 初始化。
func NewRepository(db *sql.DB) *Repository {
	return &Repository{
		db:  db,
		Now: func() time.Time { return time.Now().UTC() },
	}
}

// Create 实现 job.Repository。
func (r *Repository) Create(ctx context.Context, j job.Job) error {
	schedule := persistedSchedule(j.Schedule)
	_, err := r.db.ExecContext(ctx, `INSERT INTO jobs
		(id, name, kind, enabled, config_json,
		 schedule_type, schedule_value, schedule_timezone, schedule_anchor_at,
		 next_run_at, once_consumed_at, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		j.ID, j.Name, string(j.Kind), boolToInt(j.Enabled), configString(j.Config),
		string(schedule.Type), schedule.Value, schedule.Timezone, nullMillis(schedule.AnchorAt),
		r.nextRunMillis(j), nullMillis(j.OnceConsumedAt),
		j.CreatedAt.UnixMilli(), j.UpdatedAt.UnixMilli(),
	)
	return mapJobError("create job", j.ID, err)
}

// Get 实现 job.Repository。
func (r *Repository) Get(ctx context.Context, id string) (job.Job, error) {
	row := r.db.QueryRowContext(ctx,
		"SELECT "+jobColumns+" FROM jobs WHERE id = ?", id)
	j, err := scanJob(row)
	return j, mapGetError("get job", id, err)
}

// List 实现 job.Repository，按 name 大小写不敏感排序。
func (r *Repository) List(ctx context.Context) ([]job.Job, error) {
	return r.listWhere(ctx, "")
}

// ListByKind 实现 job.Repository。
func (r *Repository) ListByKind(ctx context.Context, kind job.JobKind) ([]job.Job, error) {
	return r.listWhere(ctx, " WHERE kind = "+quoteString(string(kind)))
}

// listWhere 按 where 子句（含 " WHERE ..." 前缀）列出 Job。
func (r *Repository) listWhere(ctx context.Context, where string) ([]job.Job, error) {
	rows, err := r.db.QueryContext(ctx,
		"SELECT "+jobColumns+" FROM jobs"+where+" ORDER BY name COLLATE NOCASE ASC, id ASC")
	if err != nil {
		return nil, fmt.Errorf("list jobs: %w", err)
	}
	defer rows.Close()

	var list []job.Job
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			return nil, fmt.Errorf("scan job: %w", err)
		}
		list = append(list, j)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate jobs: %w", err)
	}
	return list, nil
}

// Update 实现 job.Repository：单事务内整体替换可变字段并执行领域钩子
// （同成功同失败，不产生半成功状态）。
func (r *Repository) Update(ctx context.Context, j job.Job, hooks ...job.TxHook) error {
	schedule := persistedSchedule(j.Schedule)
	return storage.WithTx(ctx, r.db, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE jobs SET
			name = ?, enabled = ?, config_json = ?,
			schedule_type = ?, schedule_value = ?, schedule_timezone = ?,
			schedule_anchor_at = ?, next_run_at = ?, once_consumed_at = ?,
			updated_at = ?
			WHERE id = ?`,
			j.Name, boolToInt(j.Enabled), configString(j.Config),
			string(schedule.Type), schedule.Value, schedule.Timezone,
			nullMillis(schedule.AnchorAt), r.nextRunMillis(j), nullMillis(j.OnceConsumedAt),
			j.UpdatedAt.UnixMilli(), j.ID)
		if err != nil {
			return mapJobError("update job", j.ID, err)
		}
		if n, err := res.RowsAffected(); err == nil && n == 0 {
			return fmt.Errorf("%w: %s", job.ErrNotFound, j.ID)
		}
		for _, hook := range hooks {
			if err := hook(ctx, tx); err != nil {
				return fmt.Errorf("job %s update hook: %w", j.ID, err)
			}
		}
		return nil
	})
}

// Delete 实现 job.Repository（硬删除；领域状态表经 FK CASCADE 清理）。
func (r *Repository) Delete(ctx context.Context, id string) error {
	res, err := r.db.ExecContext(ctx, "DELETE FROM jobs WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("delete job %s: %w", id, err)
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return fmt.Errorf("%w: %s", job.ErrNotFound, id)
	}
	return nil
}

// CountBySource 实现 job.Repository：统计引用给定 Source 的 file 任务数
// （source_id 位于 File 的 config_json 内）。
func (r *Repository) CountBySource(ctx context.Context, sourceID string) (int, error) {
	var count int
	if err := r.db.QueryRowContext(ctx,
		"SELECT count(*) FROM jobs WHERE kind = ? AND json_extract(config_json, '$.source_id') = ?",
		string(job.KindFile), sourceID,
	).Scan(&count); err != nil {
		return 0, fmt.Errorf("count jobs by source %s: %w", sourceID, err)
	}
	return count, nil
}

// rowScanner 兼容 *sql.Row 与 *sql.Rows 的 Scan。
type rowScanner interface {
	Scan(dest ...any) error
}

// scanJob 把一行结果转为领域对象；时间为 UTC。
func scanJob(row rowScanner) (job.Job, error) {
	var (
		j                job.Job
		kind             string
		configJSON       string
		enabled          int
		scheduleType     string
		scheduleValue    string
		scheduleTimezone string
		scheduleAnchor   sql.NullInt64
		nextRunAt        sql.NullInt64
		onceConsumedAt   sql.NullInt64
		createdAtMillis  int64
		updatedAtMillis  int64
	)
	if err := row.Scan(&j.ID, &j.Name, &kind, &enabled, &configJSON,
		&scheduleType, &scheduleValue, &scheduleTimezone, &scheduleAnchor,
		&nextRunAt, &onceConsumedAt,
		&createdAtMillis, &updatedAtMillis); err != nil {
		return job.Job{}, err
	}
	j.Kind = job.JobKind(kind)
	j.Config = jsonOrNull(configJSON)
	j.Enabled = enabled == 1
	j.Schedule = job.Schedule{
		Type:     job.ScheduleType(scheduleType),
		Value:    scheduleValue,
		Timezone: scheduleTimezone,
		AnchorAt: timeFromMillis(scheduleAnchor),
	}
	j.OnceConsumedAt = timeFromMillis(onceConsumedAt)
	j.CreatedAt = time.UnixMilli(createdAtMillis).UTC()
	j.UpdatedAt = time.UnixMilli(updatedAtMillis).UTC()
	return j, nil
}

// persistedSchedule 归一待写入的 schedule：类型为零值或未知时按 manual
// 落库，与 schema CHECK 约束一致（构造方省略 Schedule 视为手动）。
func persistedSchedule(s job.Schedule) job.Schedule {
	if !s.Type.Valid() {
		return job.Schedule{Type: job.ScheduleManual}
	}
	return s
}

// nextRunMillis 计算落库的 next_run_at：manual 无；once 已消费后无；
// 其余按 schedule 与当前时刻推导。
func (r *Repository) nextRunMillis(j job.Job) sql.NullInt64 {
	schedule := persistedSchedule(j.Schedule)
	if schedule.Type == job.ScheduleManual {
		return sql.NullInt64{}
	}
	if schedule.Type == job.ScheduleOnce && job.OnceConsumed(j) {
		return sql.NullInt64{}
	}
	next, ok := schedule.NextRun(r.Now())
	if !ok {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: next.UnixMilli(), Valid: true}
}

// configString 把配置 JSON 转为落库字符串；nil 归一为 {}。
func configString(cfg []byte) string {
	if len(cfg) == 0 {
		return "{}"
	}
	return string(cfg)
}

// jsonOrNull 把落库字符串读回原始 JSON；空串归一为 nil（由调用方按
// 空对象解释）。
func jsonOrNull(s string) []byte {
	if s == "" || s == "{}" {
		return nil
	}
	return []byte(s)
}

// quoteString 生成 SQL 字符串字面量（ListByKind 的 kind 来自受控枚举，
// 仍按 SQL 规则转义单引号）。
func quoteString(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

// nullMillis 把时间指针转为可空列：nil 存 NULL。
func nullMillis(t *time.Time) sql.NullInt64 {
	if t == nil {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: t.UnixMilli(), Valid: true}
}

// timeFromMillis 把可空毫秒列读回 UTC 时间指针。
func timeFromMillis(n sql.NullInt64) *time.Time {
	if !n.Valid {
		return nil
	}
	t := time.UnixMilli(n.Int64).UTC()
	return &t
}

// mapJobError 把 jobs 约束错误映射为领域错误：name 唯一冲突 →
// ErrConflict；其余透传。
func mapJobError(op, id string, err error) error {
	if err == nil {
		return nil
	}
	var se *sqlite.Error
	if errors.As(err, &se) && se.Code() == sqlite3.SQLITE_CONSTRAINT_UNIQUE {
		return fmt.Errorf("%s %s: %w", op, id, job.ErrConflict)
	}
	return fmt.Errorf("%s %s: %w", op, id, err)
}

// mapGetError 统一 no-rows 到 ErrNotFound。
func mapGetError(op, id string, err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: %s", job.ErrNotFound, id)
	}
	if err != nil {
		return fmt.Errorf("%s %s: %w", op, id, err)
	}
	return nil
}

// boolToInt 把布尔值映射为 schema 的 CHECK (enabled IN (0, 1))。
func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// 编译期接口断言。
var _ job.Repository = (*Repository)(nil)

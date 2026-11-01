// Package sqlite 实现 syncjob 的 SQLite 持久化：
// Repository（jobs，kind='file'）与 ManagedRepository（managed_files）。
// 删除授权语义的数据库侧兜底是 job_id FK CASCADE；name 大小写不敏感
// 唯一。File 领域配置经 config_json 编解码（统一任务表，见
// storage/schema.sql）。
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

	"tinysync/internal/storage"
	"tinysync/internal/syncjob"
)

// fileConfig 是 kind='file' 任务在 jobs.config_json 中的领域配置形态。
type fileConfig struct {
	SourceID   string   `json:"source_id"`
	RemoteRoot string   `json:"remote_root"`
	LocalRoot  string   `json:"local_root"`
	Mode       string   `json:"mode"`
	Include    []string `json:"include"`
	Exclude    []string `json:"exclude"`
}

// configFile 把 Job 的领域字段编码为 config_json。
func configFile(job syncjob.Job) (string, error) {
	cfg := fileConfig{
		SourceID:   job.SourceID,
		RemoteRoot: job.RemoteRoot,
		LocalRoot:  job.LocalRoot,
		Mode:       string(job.Mode),
		Include:    job.Include,
		Exclude:    job.Exclude,
	}
	if cfg.Include == nil {
		cfg.Include = []string{}
	}
	if cfg.Exclude == nil {
		cfg.Exclude = []string{}
	}
	data, err := json.Marshal(cfg)
	if err != nil {
		return "", fmt.Errorf("marshal file config: %w", err)
	}
	return string(data), nil
}

// parseFileConfig 把 config_json 解码回领域字段。
func parseFileConfig(data string) (fileConfig, error) {
	var cfg fileConfig
	if err := json.Unmarshal([]byte(data), &cfg); err != nil {
		return fileConfig{}, fmt.Errorf("parse file config: %w", err)
	}
	if cfg.Include == nil {
		cfg.Include = []string{}
	}
	if cfg.Exclude == nil {
		cfg.Exclude = []string{}
	}
	return cfg, nil
}

// jobColumns 是 jobs 的普通读取列清单。
const jobColumns = "id, name, kind, enabled, config_json, " +
	"schedule_type, schedule_value, schedule_timezone, schedule_anchor_at, " +
	"next_run_at, once_consumed_at, created_at, updated_at"

// managedColumns 是 managed_files 的读取列清单。
const managedColumns = "job_id, remote_path, local_rel_path, state, " +
	"remote_size, remote_mtime_ns, remote_etag, remote_checksum, remote_version, " +
	"local_size, local_mtime_ns, updated_at"

// Repository 是 syncjob.Repository 的 SQLite 实现。
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

// ManagedRepository 是 syncjob.ManagedRepository 的 SQLite 实现。
type ManagedRepository struct {
	db *sql.DB
}

// NewManagedRepository 构造 ManagedRepository；db 需已完成 schema 初始化。
func NewManagedRepository(db *sql.DB) *ManagedRepository {
	return &ManagedRepository{db: db}
}

// Create 实现 syncjob.Repository。
func (r *Repository) Create(ctx context.Context, job syncjob.Job) error {
	config, err := configFile(job)
	if err != nil {
		return err
	}
	schedule := persistedSchedule(job.Schedule)
	_, err = r.db.ExecContext(ctx, `INSERT INTO jobs
		(id, name, kind, enabled, config_json,
		 schedule_type, schedule_value, schedule_timezone, schedule_anchor_at,
		 next_run_at, once_consumed_at, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		job.ID, job.Name, syncjob.KindFile, boolToInt(job.Enabled), config,
		string(schedule.Type), schedule.Value, schedule.Timezone, nullMillis(schedule.AnchorAt),
		r.nextRunMillis(job), nullMillis(job.OnceConsumedFor),
		job.CreatedAt.UnixMilli(), job.UpdatedAt.UnixMilli(),
	)
	return mapJobError("create job", job.ID, err)
}

// Get 实现 syncjob.Repository。
func (r *Repository) Get(ctx context.Context, id string) (syncjob.Job, error) {
	row := r.db.QueryRowContext(ctx,
		"SELECT "+jobColumns+" FROM jobs WHERE id = ?", id)
	job, err := scanJob(row)
	return job, mapGetError("get job", id, err)
}

// List 实现 syncjob.Repository，按 name 大小写不敏感排序。
func (r *Repository) List(ctx context.Context) ([]syncjob.Job, error) {
	rows, err := r.db.QueryContext(ctx,
		"SELECT "+jobColumns+" FROM jobs ORDER BY name COLLATE NOCASE ASC, id ASC")
	if err != nil {
		return nil, fmt.Errorf("list jobs: %w", err)
	}
	defer rows.Close()

	var list []syncjob.Job
	for rows.Next() {
		job, err := scanJob(rows)
		if err != nil {
			return nil, fmt.Errorf("scan job: %w", err)
		}
		list = append(list, job)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate jobs: %w", err)
	}
	return list, nil
}

// Update 实现 syncjob.Repository：整体替换可变字段。
func (r *Repository) Update(ctx context.Context, job syncjob.Job) error {
	res, err := r.updateExec(ctx, r.db, job)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return fmt.Errorf("%w: %s", syncjob.ErrNotFound, job.ID)
	}
	return nil
}

// UpdateAndResetManaged 实现 syncjob.Repository：单事务内整体替换
// 可变字段并删除该 Job 的全部 managed 记录。mapping 变更与 metadata
// 释放同成功同失败——先删后更或先更后删的分开执行都存在半成功状态
// （如更新撞 name 唯一冲突时 metadata 已被清空，Job 失去管理关系）。
func (r *Repository) UpdateAndResetManaged(ctx context.Context, job syncjob.Job) error {
	return storage.WithTx(ctx, r.db, func(tx *sql.Tx) error {
		res, err := r.updateExec(ctx, tx, job)
		if err != nil {
			return err
		}
		if n, err := res.RowsAffected(); err == nil && n == 0 {
			return fmt.Errorf("%w: %s", syncjob.ErrNotFound, job.ID)
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM managed_files WHERE job_id = ?", job.ID); err != nil {
			return fmt.Errorf("delete managed files of job %s: %w", job.ID, err)
		}
		return nil
	})
}

// updateExec 执行 jobs 的整体更新语句，execer 兼容 *sql.DB 与
// *sql.Tx；约束错误映射为领域错误。
func (r *Repository) updateExec(ctx context.Context, exec interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}, job syncjob.Job) (sql.Result, error) {
	config, err := configFile(job)
	if err != nil {
		return nil, err
	}
	schedule := persistedSchedule(job.Schedule)
	res, err := exec.ExecContext(ctx, `UPDATE jobs SET
		name = ?, enabled = ?, config_json = ?,
		schedule_type = ?, schedule_value = ?, schedule_timezone = ?,
		schedule_anchor_at = ?, next_run_at = ?, once_consumed_at = ?,
		updated_at = ?
		WHERE id = ? AND kind = ?`,
		job.Name, boolToInt(job.Enabled), config,
		string(schedule.Type), schedule.Value, schedule.Timezone,
		nullMillis(schedule.AnchorAt), r.nextRunMillis(job), nullMillis(job.OnceConsumedFor),
		job.UpdatedAt.UnixMilli(), job.ID, syncjob.KindFile,
	)
	if err != nil {
		return nil, mapJobError("update job", job.ID, err)
	}
	return res, nil
}

// Delete 实现 syncjob.Repository（硬删除；managed_files 经 CASCADE 清理）。
func (r *Repository) Delete(ctx context.Context, id string) error {
	res, err := r.db.ExecContext(ctx, "DELETE FROM jobs WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("delete job %s: %w", id, err)
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return fmt.Errorf("%w: %s", syncjob.ErrNotFound, id)
	}
	return nil
}

// CountBySource 实现 syncjob.Repository：统计引用给定 Source 的
// File 任务数（source_id 位于 config_json 内）。
func (r *Repository) CountBySource(ctx context.Context, sourceID string) (int, error) {
	var count int
	if err := r.db.QueryRowContext(ctx,
		"SELECT count(*) FROM jobs WHERE kind = ? AND json_extract(config_json, '$.source_id') = ?",
		syncjob.KindFile, sourceID,
	).Scan(&count); err != nil {
		return 0, fmt.Errorf("count jobs by source %s: %w", sourceID, err)
	}
	return count, nil
}

// ListByJob 实现 syncjob.ManagedRepository，按 remote_path 排序。
func (r *ManagedRepository) ListByJob(ctx context.Context, jobID string) ([]syncjob.ManagedFile, error) {
	rows, err := r.db.QueryContext(ctx,
		"SELECT "+managedColumns+" FROM managed_files WHERE job_id = ? ORDER BY remote_path ASC", jobID)
	if err != nil {
		return nil, fmt.Errorf("list managed files %s: %w", jobID, err)
	}
	defer rows.Close()

	var list []syncjob.ManagedFile
	for rows.Next() {
		f, err := scanManaged(rows)
		if err != nil {
			return nil, fmt.Errorf("scan managed file: %w", err)
		}
		list = append(list, f)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate managed files: %w", err)
	}
	return list, nil
}

// Upsert 实现 syncjob.ManagedRepository：按 (job_id, remote_path) 插入或更新，
// 单事务原子；local_rel_path 唯一冲突映射为 ErrConflict。
func (r *ManagedRepository) Upsert(ctx context.Context, files []syncjob.ManagedFile) error {
	if len(files) == 0 {
		return nil
	}
	return storage.WithTx(ctx, r.db, func(tx *sql.Tx) error {
		for _, f := range files {
			_, err := tx.ExecContext(ctx, `INSERT INTO managed_files
				(job_id, remote_path, local_rel_path, state,
				 remote_size, remote_mtime_ns, remote_etag, remote_checksum, remote_version,
				 local_size, local_mtime_ns, updated_at)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
				ON CONFLICT (job_id, remote_path) DO UPDATE SET
					local_rel_path = excluded.local_rel_path,
					state = excluded.state,
					remote_size = excluded.remote_size,
					remote_mtime_ns = excluded.remote_mtime_ns,
					remote_etag = excluded.remote_etag,
					remote_checksum = excluded.remote_checksum,
					remote_version = excluded.remote_version,
					local_size = excluded.local_size,
					local_mtime_ns = excluded.local_mtime_ns,
					updated_at = excluded.updated_at`,
				f.JobID, f.RemotePath, f.LocalRelPath, string(f.State),
				f.Remote.Size, nullTime(f.Remote.ModifiedAt), f.Remote.ETag,
				f.Remote.Checksum, f.Remote.Version,
				f.LocalSize, f.LocalMtimeNs, f.UpdatedAt.UnixMilli(),
			)
			if err != nil {
				return mapManagedError("upsert managed file", f.RemotePath, err)
			}
		}
		return nil
	})
}

// Delete 实现 syncjob.ManagedRepository（批量）。
func (r *ManagedRepository) Delete(ctx context.Context, jobID string, remotePaths []string) error {
	if len(remotePaths) == 0 {
		return nil
	}
	return storage.WithTx(ctx, r.db, func(tx *sql.Tx) error {
		for _, p := range remotePaths {
			if _, err := tx.ExecContext(ctx,
				"DELETE FROM managed_files WHERE job_id = ? AND remote_path = ?", jobID, p,
			); err != nil {
				return fmt.Errorf("delete managed file %s: %w", p, err)
			}
		}
		return nil
	})
}

// DeleteAllForJob 实现 syncjob.ManagedRepository。
func (r *ManagedRepository) DeleteAllForJob(ctx context.Context, jobID string) error {
	if _, err := r.db.ExecContext(ctx, "DELETE FROM managed_files WHERE job_id = ?", jobID); err != nil {
		return fmt.Errorf("delete managed files of job %s: %w", jobID, err)
	}
	return nil
}

// rowScanner 兼容 *sql.Row 与 *sql.Rows 的 Scan。
type rowScanner interface {
	Scan(dest ...any) error
}

// scanJob 把一行结果转为领域对象；时间为 UTC。kind 非 file 的行
// 属于其他领域任务，由各自仓储读取，这里返回错误防串域。
func scanJob(row rowScanner) (syncjob.Job, error) {
	var (
		job              syncjob.Job
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
	if err := row.Scan(&job.ID, &job.Name, &kind, &enabled, &configJSON,
		&scheduleType, &scheduleValue, &scheduleTimezone, &scheduleAnchor,
		&nextRunAt, &onceConsumedAt,
		&createdAtMillis, &updatedAtMillis); err != nil {
		return syncjob.Job{}, err
	}
	if kind != syncjob.KindFile {
		return syncjob.Job{}, fmt.Errorf("job %s has kind %q, not a file job", job.ID, kind)
	}
	cfg, err := parseFileConfig(configJSON)
	if err != nil {
		return syncjob.Job{}, fmt.Errorf("job %s: %w", job.ID, err)
	}
	job.SourceID = cfg.SourceID
	job.RemoteRoot = cfg.RemoteRoot
	job.LocalRoot = cfg.LocalRoot
	job.Mode = syncjob.Mode(cfg.Mode)
	job.Include = cfg.Include
	job.Exclude = cfg.Exclude
	job.Enabled = enabled == 1
	job.Schedule = syncjob.Schedule{
		Type:     syncjob.ScheduleType(scheduleType),
		Value:    scheduleValue,
		Timezone: scheduleTimezone,
		AnchorAt: timeFromMillis(scheduleAnchor),
	}
	job.OnceConsumedFor = timeFromMillis(onceConsumedAt)
	job.CreatedAt = time.UnixMilli(createdAtMillis).UTC()
	job.UpdatedAt = time.UnixMilli(updatedAtMillis).UTC()
	return job, nil
}

// persistedSchedule 归一待写入的 schedule：类型为零值或未知时按 manual
// 落库，与 schema CHECK 约束一致（Job 构造方省略 Schedule 视为手动）。
func persistedSchedule(s syncjob.Schedule) syncjob.Schedule {
	if !s.Type.Valid() {
		return syncjob.Schedule{Type: syncjob.ScheduleManual}
	}
	return s
}

// nextRunMillis 计算落库的 next_run_at：manual 无；once 已消费后无；
// 其余按 schedule 与当前时刻推导。
func (r *Repository) nextRunMillis(job syncjob.Job) sql.NullInt64 {
	schedule := persistedSchedule(job.Schedule)
	switch schedule.Type {
	case syncjob.ScheduleOnce:
		if at, err := schedule.OnceAt(); err == nil &&
			job.OnceConsumedFor != nil &&
			job.OnceConsumedFor.UnixMilli() == at.UnixMilli() {
			return sql.NullInt64{}
		}
		next, ok := schedule.NextRun(r.Now())
		if !ok {
			return sql.NullInt64{}
		}
		return sql.NullInt64{Int64: next.UnixMilli(), Valid: true}
	case syncjob.ScheduleInterval, syncjob.ScheduleCron:
		next, ok := schedule.NextRun(r.Now())
		if !ok {
			return sql.NullInt64{}
		}
		return sql.NullInt64{Int64: next.UnixMilli(), Valid: true}
	default:
		return sql.NullInt64{}
	}
}

// nullMillis 把时间指针转为可空列：nil 存 NULL（interval anchor、
// scheduled_for / finished_at 同构）。
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

// scanManaged 把一行结果转为领域对象；时间为 UTC。
func scanManaged(row rowScanner) (syncjob.ManagedFile, error) {
	var (
		f               syncjob.ManagedFile
		state           string
		remoteSize      int64
		remoteMtimeNs   sql.NullInt64
		localSize       sql.NullInt64
		localMtimeNs    sql.NullInt64
		updatedAtMillis int64
	)
	if err := row.Scan(&f.JobID, &f.RemotePath, &f.LocalRelPath, &state,
		&remoteSize, &remoteMtimeNs, &f.Remote.ETag, &f.Remote.Checksum, &f.Remote.Version,
		&localSize, &localMtimeNs, &updatedAtMillis); err != nil {
		return syncjob.ManagedFile{}, err
	}
	f.State = syncjob.ManagedState(state)
	f.Remote.Size = remoteSize
	if remoteMtimeNs.Valid {
		f.Remote.ModifiedAt = time.Unix(0, remoteMtimeNs.Int64).UTC()
	}
	f.LocalSize = nullableInt64(localSize)
	f.LocalMtimeNs = nullableInt64(localMtimeNs)
	f.UpdatedAt = time.UnixMilli(updatedAtMillis).UTC()
	return f, nil
}

// mapJobError 把 jobs 约束错误映射为领域错误：
// name 唯一冲突 → ErrConflict；其余透传（source 存在性由应用层校验）。
func mapJobError(op, id string, err error) error {
	if err == nil {
		return nil
	}
	var se *sqlite.Error
	if errors.As(err, &se) {
		switch se.Code() {
		case sqlite3.SQLITE_CONSTRAINT_UNIQUE:
			return fmt.Errorf("%s %s: %w", op, id, syncjob.ErrConflict)
		}
	}
	return fmt.Errorf("%s %s: %w", op, id, err)
}

// mapManagedError 把 managed_files 约束错误映射为领域错误：
// local_rel_path 唯一冲突 → ErrConflict；job 外键拒绝 → ErrNotFound。
func mapManagedError(op, remotePath string, err error) error {
	if err == nil {
		return nil
	}
	var se *sqlite.Error
	if errors.As(err, &se) {
		switch se.Code() {
		case sqlite3.SQLITE_CONSTRAINT_UNIQUE:
			return fmt.Errorf("%s %s: %w", op, remotePath, syncjob.ErrConflict)
		case sqlite3.SQLITE_CONSTRAINT_FOREIGNKEY:
			return fmt.Errorf("%s %s: %w (job does not exist)", op, remotePath, syncjob.ErrNotFound)
		}
	}
	return fmt.Errorf("%s %w", op, err)
}

// mapGetError 统一 no-rows 到 ErrNotFound。
func mapGetError(op, id string, err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: %s", syncjob.ErrNotFound, id)
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

// nullTime 把时间转为可空列：零值时间存 NULL，避免把 time.Time{}
// 的 UnixNano（负的巨大伪时间戳）写进 remote_mtime_ns；读回时
// NULL 归一为零值时间，语义对称。
func nullTime(t time.Time) sql.NullInt64 {
	if t.IsZero() {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: t.UnixNano(), Valid: true}
}

// nullableInt64 把 NULL 转为 nil 指针。
func nullableInt64(n sql.NullInt64) *int64 {
	if !n.Valid {
		return nil
	}
	v := n.Int64
	return &v
}

// 编译期接口断言。
var (
	_ syncjob.Repository        = (*Repository)(nil)
	_ syncjob.ManagedRepository = (*ManagedRepository)(nil)
)

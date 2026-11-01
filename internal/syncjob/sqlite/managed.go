// Package sqlite 实现 syncjob 的 managed_files 持久化。
// 统一任务（jobs / job_runs / job_run_items）的持久化见 internal/job/sqlite。
package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"

	"tinysync/internal/storage"
	"tinysync/internal/syncjob"
)

// managedColumns 是 managed_files 的读取列清单。
const managedColumns = "job_id, remote_path, local_rel_path, state, " +
	"remote_size, remote_mtime_ns, remote_etag, remote_checksum, remote_version, " +
	"local_size, local_mtime_ns, updated_at"

// ManagedRepository 是 syncjob.ManagedRepository 的 SQLite 实现。
type ManagedRepository struct {
	db *sql.DB
}

// NewManagedRepository 构造 ManagedRepository；db 需已完成 schema 初始化。
func NewManagedRepository(db *sql.DB) *ManagedRepository {
	return &ManagedRepository{db: db}
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
var _ syncjob.ManagedRepository = (*ManagedRepository)(nil)

// Package sqlite 实现 publish.Repository 的 SQLite 持久化：表结构
// 见 migration 0006_published_files.sql，时间列为 Unix 毫秒，
// public_path 全局唯一（数据库 UNIQUE 约束兜底）。
package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"tinysync/internal/publish"
)

// publishedColumns 是 published_files 的读取列清单。
const publishedColumns = "id, local_path, public_path, enabled, expires_at, created_at, updated_at"

// Repository 是 publish.Repository 的 SQLite 实现。
type Repository struct {
	db *sql.DB
}

// NewRepository 构造 Repository；db 需已完成 schema migration。
func NewRepository(db *sql.DB) *Repository {
	return &Repository{db: db}
}

// Create 实现 publish.Repository。
func (r *Repository) Create(ctx context.Context, p publish.PublishedFile) error {
	_, err := r.db.ExecContext(ctx, `INSERT INTO published_files
		(id, local_path, public_path, enabled, expires_at, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		p.ID, p.LocalPath, p.PublicPath, boolToInt(p.Enabled),
		nullMillis(p.ExpiresAt), p.CreatedAt.UnixMilli(), p.UpdatedAt.UnixMilli(),
	)
	return mapError("create published file", p.ID, err)
}

// Get 实现 publish.Repository。
func (r *Repository) Get(ctx context.Context, id string) (publish.PublishedFile, error) {
	row := r.db.QueryRowContext(ctx,
		"SELECT "+publishedColumns+" FROM published_files WHERE id = ?", id)
	p, err := scan(row)
	return p, mapGetError("get published file", id, err)
}

// GetByPublicPath 实现 publish.Repository。
func (r *Repository) GetByPublicPath(ctx context.Context, publicPath string) (publish.PublishedFile, error) {
	row := r.db.QueryRowContext(ctx,
		"SELECT "+publishedColumns+" FROM published_files WHERE public_path = ?", publicPath)
	p, err := scan(row)
	return p, mapGetError("get published file by public path", publicPath, err)
}

// List 实现 publish.Repository，按 public_path 字典序排序。
func (r *Repository) List(ctx context.Context) ([]publish.PublishedFile, error) {
	rows, err := r.db.QueryContext(ctx,
		"SELECT "+publishedColumns+" FROM published_files ORDER BY public_path ASC")
	if err != nil {
		return nil, fmt.Errorf("list published files: %w", err)
	}
	defer rows.Close()

	var list []publish.PublishedFile
	for rows.Next() {
		p, err := scan(rows)
		if err != nil {
			return nil, fmt.Errorf("scan published file: %w", err)
		}
		list = append(list, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate published files: %w", err)
	}
	return list, nil
}

// Update 实现 publish.Repository：整体替换可变字段，local_path 与
// created_at 不被触碰。
func (r *Repository) Update(ctx context.Context, p publish.PublishedFile) error {
	res, err := r.db.ExecContext(ctx, `UPDATE published_files SET
		public_path = ?, enabled = ?, expires_at = ?, updated_at = ?
		WHERE id = ?`,
		p.PublicPath, boolToInt(p.Enabled), nullMillis(p.ExpiresAt),
		p.UpdatedAt.UnixMilli(), p.ID,
	)
	if err != nil {
		return mapError("update published file", p.ID, err)
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return fmt.Errorf("%w: %s", publish.ErrNotFound, p.ID)
	}
	return nil
}

// Delete 实现 publish.Repository（硬删除；不触及本地文件）。
func (r *Repository) Delete(ctx context.Context, id string) error {
	res, err := r.db.ExecContext(ctx, "DELETE FROM published_files WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("delete published file %s: %w", id, err)
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return fmt.Errorf("%w: %s", publish.ErrNotFound, id)
	}
	return nil
}

// rowScanner 兼容 *sql.Row 与 *sql.Rows 的 Scan。
type rowScanner interface {
	Scan(dest ...any) error
}

// scan 把一行结果转为领域对象；时间为 UTC。
func scan(row rowScanner) (publish.PublishedFile, error) {
	var (
		p           publish.PublishedFile
		enabled     int
		expiresAt   sql.NullInt64
		createdAtMs int64
		updatedAtMs int64
	)
	if err := row.Scan(&p.ID, &p.LocalPath, &p.PublicPath, &enabled, &expiresAt,
		&createdAtMs, &updatedAtMs); err != nil {
		return publish.PublishedFile{}, err
	}
	p.Enabled = enabled == 1
	if expiresAt.Valid {
		t := time.UnixMilli(expiresAt.Int64).UTC()
		p.ExpiresAt = &t
	}
	p.CreatedAt = time.UnixMilli(createdAtMs).UTC()
	p.UpdatedAt = time.UnixMilli(updatedAtMs).UTC()
	return p, nil
}

// mapGetError 区分「不存在」与扫描错误。
func mapGetError(op, key string, err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: %s", publish.ErrNotFound, key)
	}
	if err != nil {
		return fmt.Errorf("%s %s: %w", op, key, err)
	}
	return nil
}

// mapError 把约束冲突（UNIQUE public_path）映射为 ErrConflict。
func mapError(op, id string, err error) error {
	if err == nil {
		return nil
	}
	if strings.Contains(err.Error(), "UNIQUE constraint failed: published_files.public_path") {
		return fmt.Errorf("%w: %s", publish.ErrConflict, op)
	}
	return fmt.Errorf("%s %s: %w", op, id, err)
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// nullMillis 把时间指针转为可空列：nil 存 NULL。
func nullMillis(t *time.Time) sql.NullInt64 {
	if t == nil {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: t.UnixMilli(), Valid: true}
}

// 编译期断言：实现完整接口。
var _ publish.Repository = (*Repository)(nil)

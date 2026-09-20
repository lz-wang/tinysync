// Package sqlite 实现 share.Repository 的 SQLite 持久化：表结构见
// migration 0009_shares.sql，时间列为 Unix 毫秒，slug 全局唯一
// （数据库 UNIQUE 约束兜底），name 为可空列（NULL 表示未命名）。
package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"tinysync/internal/share"
)

// shareColumns 是 shares 的读取列清单。
const shareColumns = "id, local_path, slug, name, is_dir, enabled, expires_at, created_at, updated_at"

// Repository 是 share.Repository 的 SQLite 实现。
type Repository struct {
	db *sql.DB
}

// NewRepository 构造 Repository；db 需已完成 schema migration。
func NewRepository(db *sql.DB) *Repository {
	return &Repository{db: db}
}

// Create 实现 share.Repository。
func (r *Repository) Create(ctx context.Context, s share.Share) error {
	_, err := r.db.ExecContext(ctx, `INSERT INTO shares
		(id, local_path, slug, name, is_dir, enabled, expires_at, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		s.ID, s.LocalPath, s.Slug, nullString(s.Name), boolToInt(s.IsDir),
		boolToInt(s.Enabled), nullMillis(s.ExpiresAt), s.CreatedAt.UnixMilli(), s.UpdatedAt.UnixMilli(),
	)
	return mapError("create share", s.ID, err)
}

// Get 实现 share.Repository。
func (r *Repository) Get(ctx context.Context, id string) (share.Share, error) {
	row := r.db.QueryRowContext(ctx,
		"SELECT "+shareColumns+" FROM shares WHERE id = ?", id)
	s, err := scan(row)
	return s, mapGetError("get share", id, err)
}

// GetBySlug 实现 share.Repository。
func (r *Repository) GetBySlug(ctx context.Context, slug string) (share.Share, error) {
	row := r.db.QueryRowContext(ctx,
		"SELECT "+shareColumns+" FROM shares WHERE slug = ?", slug)
	s, err := scan(row)
	return s, mapGetError("get share by slug", slug, err)
}

// List 实现 share.Repository，按 slug 字典序排序。
func (r *Repository) List(ctx context.Context) ([]share.Share, error) {
	rows, err := r.db.QueryContext(ctx,
		"SELECT "+shareColumns+" FROM shares ORDER BY slug ASC")
	if err != nil {
		return nil, fmt.Errorf("list shares: %w", err)
	}
	defer rows.Close()

	var list []share.Share
	for rows.Next() {
		s, err := scan(rows)
		if err != nil {
			return nil, fmt.Errorf("scan share: %w", err)
		}
		list = append(list, s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate shares: %w", err)
	}
	return list, nil
}

// Update 实现 share.Repository：整体替换可变字段，local_path/is_dir
// 与 created_at 不被触碰。
func (r *Repository) Update(ctx context.Context, s share.Share) error {
	res, err := r.db.ExecContext(ctx, `UPDATE shares SET
		slug = ?, name = ?, enabled = ?, expires_at = ?, updated_at = ?
		WHERE id = ?`,
		s.Slug, nullString(s.Name), boolToInt(s.Enabled), nullMillis(s.ExpiresAt),
		s.UpdatedAt.UnixMilli(), s.ID,
	)
	if err != nil {
		return mapError("update share", s.ID, err)
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return fmt.Errorf("%w: %s", share.ErrNotFound, s.ID)
	}
	return nil
}

// Delete 实现 share.Repository（硬删除；不触及本地文件）。
func (r *Repository) Delete(ctx context.Context, id string) error {
	res, err := r.db.ExecContext(ctx, "DELETE FROM shares WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("delete share %s: %w", id, err)
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return fmt.Errorf("%w: %s", share.ErrNotFound, id)
	}
	return nil
}

// rowScanner 兼容 *sql.Row 与 *sql.Rows 的 Scan。
type rowScanner interface {
	Scan(dest ...any) error
}

// scan 把一行结果转为领域对象；时间为 UTC。
func scan(row rowScanner) (share.Share, error) {
	var (
		s           share.Share
		name        sql.NullString
		isDir       int
		enabled     int
		expiresAt   sql.NullInt64
		createdAtMs int64
		updatedAtMs int64
	)
	if err := row.Scan(&s.ID, &s.LocalPath, &s.Slug, &name, &isDir, &enabled, &expiresAt,
		&createdAtMs, &updatedAtMs); err != nil {
		return share.Share{}, err
	}
	if name.Valid {
		v := name.String
		s.Name = &v
	}
	s.IsDir = isDir == 1
	s.Enabled = enabled == 1
	if expiresAt.Valid {
		t := time.UnixMilli(expiresAt.Int64).UTC()
		s.ExpiresAt = &t
	}
	s.CreatedAt = time.UnixMilli(createdAtMs).UTC()
	s.UpdatedAt = time.UnixMilli(updatedAtMs).UTC()
	return s, nil
}

// mapGetError 区分「不存在」与扫描错误。
func mapGetError(op, key string, err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: %s", share.ErrNotFound, key)
	}
	if err != nil {
		return fmt.Errorf("%s %s: %w", op, key, err)
	}
	return nil
}

// mapError 把约束冲突（UNIQUE slug）映射为 ErrConflict。
func mapError(op, id string, err error) error {
	if err == nil {
		return nil
	}
	if strings.Contains(err.Error(), "UNIQUE constraint failed: shares.slug") {
		return fmt.Errorf("%w: %s", share.ErrConflict, op)
	}
	return fmt.Errorf("%s %s: %w", op, id, err)
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// nullString 把名称指针转为可空列：nil 存 NULL。
func nullString(s *string) sql.NullString {
	if s == nil {
		return sql.NullString{}
	}
	return sql.NullString{String: *s, Valid: true}
}

// nullMillis 把时间指针转为可空列：nil 存 NULL。
func nullMillis(t *time.Time) sql.NullInt64 {
	if t == nil {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: t.UnixMilli(), Valid: true}
}

// 编译期断言：实现完整接口。
var _ share.Repository = (*Repository)(nil)

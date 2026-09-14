// Package sqlite 实现 source.Repository 的 SQLite 后端。
// 普通读取路径不读取 password 列，仅以 (password != ”) 形式给出
// PasswordSet 标志；密码明文只能经 GetPassword 获取。
package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"tinysync/internal/source"
	"tinysync/internal/storage"
)

// Repository 是 source.Repository 的 SQLite 实现。
type Repository struct {
	db *sql.DB
}

// New 构造 Repository；db 需已完成 schema migration（含 sources 表）。
func New(db *sql.DB) *Repository {
	return &Repository{db: db}
}

// sourceColumns 是普通读取路径的列清单：不含 password 明文，
// 只含 password_set 布尔表达式。
const sourceColumns = "id, name, type, endpoint, username, (password != '') AS password_set, enabled, created_at, updated_at"

// Create 实现 source.Repository。
func (r *Repository) Create(ctx context.Context, s source.Source, password string) error {
	return storage.WithTx(ctx, r.db, func(tx *sql.Tx) error {
		var conflicts int
		if err := tx.QueryRowContext(ctx,
			"SELECT count(*) FROM sources WHERE name = ?", s.Name,
		).Scan(&conflicts); err != nil {
			return fmt.Errorf("check name conflict: %w", err)
		}
		if conflicts > 0 {
			return source.ErrConflict
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO sources
			(id, name, type, endpoint, username, password, enabled, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			s.ID, s.Name, string(s.Type), s.Endpoint, s.Username, password, boolToInt(s.Enabled),
			s.CreatedAt.UnixMilli(), s.UpdatedAt.UnixMilli(),
		)
		if err != nil {
			return fmt.Errorf("insert source %s: %w", s.ID, err)
		}
		return nil
	})
}

// Get 实现 source.Repository。
func (r *Repository) Get(ctx context.Context, id string) (source.Source, error) {
	row := r.db.QueryRowContext(ctx,
		"SELECT "+sourceColumns+" FROM sources WHERE id = ?", id)
	s, err := scanSource(row)
	if errors.Is(err, sql.ErrNoRows) {
		return source.Source{}, fmt.Errorf("%w: %s", source.ErrNotFound, id)
	}
	if err != nil {
		return source.Source{}, fmt.Errorf("get source %s: %w", id, err)
	}
	return s, nil
}

// List 实现 source.Repository，按 name 大小写不敏感排序。
func (r *Repository) List(ctx context.Context) ([]source.Source, error) {
	rows, err := r.db.QueryContext(ctx,
		"SELECT "+sourceColumns+" FROM sources ORDER BY name COLLATE NOCASE ASC, id ASC")
	if err != nil {
		return nil, fmt.Errorf("list sources: %w", err)
	}
	defer rows.Close()

	var list []source.Source
	for rows.Next() {
		s, err := scanSource(rows)
		if err != nil {
			return nil, fmt.Errorf("scan source: %w", err)
		}
		list = append(list, s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate sources: %w", err)
	}
	return list, nil
}

// Update 实现 source.Repository：整体替换可变字段；password 非 nil 时替换。
func (r *Repository) Update(ctx context.Context, s source.Source, password *string) error {
	return storage.WithTx(ctx, r.db, func(tx *sql.Tx) error {
		var exists int
		if err := tx.QueryRowContext(ctx,
			"SELECT count(*) FROM sources WHERE id = ?", s.ID,
		).Scan(&exists); err != nil {
			return fmt.Errorf("check source existence: %w", err)
		}
		if exists == 0 {
			return fmt.Errorf("%w: %s", source.ErrNotFound, s.ID)
		}
		var conflicts int
		if err := tx.QueryRowContext(ctx,
			"SELECT count(*) FROM sources WHERE name = ? AND id != ?", s.Name, s.ID,
		).Scan(&conflicts); err != nil {
			return fmt.Errorf("check name conflict: %w", err)
		}
		if conflicts > 0 {
			return source.ErrConflict
		}

		var err error
		if password != nil {
			_, err = tx.ExecContext(ctx, `UPDATE sources SET
				name = ?, type = ?, endpoint = ?, username = ?, password = ?,
				enabled = ?, updated_at = ?
				WHERE id = ?`,
				s.Name, string(s.Type), s.Endpoint, s.Username, *password,
				boolToInt(s.Enabled), s.UpdatedAt.UnixMilli(), s.ID,
			)
		} else {
			_, err = tx.ExecContext(ctx, `UPDATE sources SET
				name = ?, type = ?, endpoint = ?, username = ?,
				enabled = ?, updated_at = ?
				WHERE id = ?`,
				s.Name, string(s.Type), s.Endpoint, s.Username,
				boolToInt(s.Enabled), s.UpdatedAt.UnixMilli(), s.ID,
			)
		}
		if err != nil {
			return fmt.Errorf("update source %s: %w", s.ID, err)
		}
		return nil
	})
}

// Delete 实现 source.Repository（硬删除）。
func (r *Repository) Delete(ctx context.Context, id string) error {
	res, err := r.db.ExecContext(ctx, "DELETE FROM sources WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("delete source %s: %w", id, err)
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return fmt.Errorf("%w: %s", source.ErrNotFound, id)
	}
	return nil
}

// GetPassword 实现 source.Repository：唯一允许读取 password 列的路径。
func (r *Repository) GetPassword(ctx context.Context, id string) (string, error) {
	var password string
	err := r.db.QueryRowContext(ctx,
		"SELECT password FROM sources WHERE id = ?", id).Scan(&password)
	if errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("%w: %s", source.ErrNotFound, id)
	}
	if err != nil {
		return "", fmt.Errorf("get source password %s: %w", id, err)
	}
	return password, nil
}

// rowScanner 兼容 *sql.Row 与 *sql.Rows 的 Scan。
type rowScanner interface {
	Scan(dest ...any) error
}

// scanSource 把一行普通读取路径的结果转为领域对象；时间为 UTC。
func scanSource(row rowScanner) (source.Source, error) {
	var (
		s               source.Source
		typ             string
		enabled         int
		createdAtMillis int64
		updatedAtMillis int64
	)
	if err := row.Scan(&s.ID, &s.Name, &typ, &s.Endpoint, &s.Username,
		&s.PasswordSet, &enabled, &createdAtMillis, &updatedAtMillis); err != nil {
		return source.Source{}, err
	}
	s.Type = source.Type(typ)
	s.Enabled = enabled == 1
	s.CreatedAt = time.UnixMilli(createdAtMillis).UTC()
	s.UpdatedAt = time.UnixMilli(updatedAtMillis).UTC()
	return s, nil
}

// boolToInt 把布尔值映射为 schema 的 CHECK (enabled IN (0, 1))。
func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

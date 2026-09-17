// Package sqlite 实现 auth.Repository 的 SQLite 持久化：表结构见
// migration 0007_authentication.sql，时间列为 Unix 毫秒。raw 凭据
// 绝不落库，调用方只传 SHA-256 hash（BLOB）。
package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"tinysync/internal/auth"
	"tinysync/internal/storage"
)

// Repository 是 auth.Repository 的 SQLite 实现。
type Repository struct {
	db *sql.DB
}

// NewRepository 构造 Repository；db 需已完成 schema migration
// （含 0007 认证三表）。
func NewRepository(db *sql.DB) *Repository {
	return &Repository{db: db}
}

// AdminConfigured 实现 auth.Repository。
func (r *Repository) AdminConfigured(ctx context.Context) (bool, error) {
	var count int
	if err := r.db.QueryRowContext(ctx,
		"SELECT count(*) FROM admin_credentials WHERE singleton = 1",
	).Scan(&count); err != nil {
		return false, fmt.Errorf("check admin credential: %w", err)
	}
	return count > 0, nil
}

// GetAdminCredential 实现 auth.Repository。
func (r *Repository) GetAdminCredential(ctx context.Context) (auth.AdminCredential, error) {
	var (
		cred        auth.AdminCredential
		createdAtMs int64
		updatedAtMs int64
	)
	if err := r.db.QueryRowContext(ctx,
		"SELECT password_hash, created_at, updated_at FROM admin_credentials WHERE singleton = 1",
	).Scan(&cred.PasswordHash, &createdAtMs, &updatedAtMs); err != nil {
		return auth.AdminCredential{}, mapGetError("get admin credential", "singleton", err)
	}
	cred.CreatedAt = time.UnixMilli(createdAtMs).UTC()
	cred.UpdatedAt = time.UnixMilli(updatedAtMs).UTC()
	return cred, nil
}

// SetAdminPassword 实现 auth.Repository：替换密码与清空全部 Web
// Session 处于同一 transaction，密码重置立即废弃所有会话。
func (r *Repository) SetAdminPassword(ctx context.Context, passwordHash string, now time.Time) error {
	nowMs := now.UnixMilli()
	return storage.WithTx(ctx, r.db, func(tx *sql.Tx) error {
		// singleton 列恒为 1：存在即替换，不存在即首建（同一 upsert）。
		if _, err := tx.ExecContext(ctx, `INSERT INTO admin_credentials
			(singleton, password_hash, created_at, updated_at)
			VALUES (1, ?, ?, ?)
			ON CONFLICT (singleton) DO UPDATE SET
			 password_hash = excluded.password_hash,
			 updated_at = excluded.updated_at`,
			passwordHash, nowMs, nowMs); err != nil {
			return fmt.Errorf("upsert admin credential: %w", err)
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM web_sessions"); err != nil {
			return fmt.Errorf("clear web sessions: %w", err)
		}
		return nil
	})
}

// CreateSession 实现 auth.Repository。
func (r *Repository) CreateSession(ctx context.Context, session auth.WebSession, sessionHash []byte) error {
	_, err := r.db.ExecContext(ctx, `INSERT INTO web_sessions
		(id, session_hash, created_at, expires_at)
		VALUES (?, ?, ?, ?)`,
		session.ID, sessionHash, session.CreatedAt.UnixMilli(), session.ExpiresAt.UnixMilli(),
	)
	return mapError("create web session", session.ID, err)
}

// GetSessionByHash 实现 auth.Repository。
func (r *Repository) GetSessionByHash(ctx context.Context, sessionHash []byte) (auth.WebSession, error) {
	var (
		session     auth.WebSession
		createdAtMs int64
		expiresAtMs int64
	)
	if err := r.db.QueryRowContext(ctx,
		"SELECT id, created_at, expires_at FROM web_sessions WHERE session_hash = ?",
		sessionHash,
	).Scan(&session.ID, &createdAtMs, &expiresAtMs); err != nil {
		return auth.WebSession{}, mapGetError("get web session by hash", "<hash>", err)
	}
	session.CreatedAt = time.UnixMilli(createdAtMs).UTC()
	session.ExpiresAt = time.UnixMilli(expiresAtMs).UTC()
	return session, nil
}

// DeleteSession 实现 auth.Repository：会话不存在时视为已删除。
func (r *Repository) DeleteSession(ctx context.Context, id string) error {
	if _, err := r.db.ExecContext(ctx, "DELETE FROM web_sessions WHERE id = ?", id); err != nil {
		return fmt.Errorf("delete web session %s: %w", id, err)
	}
	return nil
}

// DeleteExpiredSessions 实现 auth.Repository。
func (r *Repository) DeleteExpiredSessions(ctx context.Context, now time.Time) (int64, error) {
	res, err := r.db.ExecContext(ctx,
		"DELETE FROM web_sessions WHERE expires_at <= ?", now.UnixMilli())
	if err != nil {
		return 0, fmt.Errorf("delete expired web sessions: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("count expired web sessions: %w", err)
	}
	return n, nil
}

// tokenColumns 是 api_tokens 的读取列清单。
const tokenColumns = "id, name, prefix, scopes_json, created_at, expires_at, last_used_at, revoked_at"

// CreateAPIToken 实现 auth.Repository。
func (r *Repository) CreateAPIToken(ctx context.Context, token auth.APIToken, tokenHash []byte) error {
	scopesJSON, err := json.Marshal(token.Scopes)
	if err != nil {
		return fmt.Errorf("marshal scopes of api token %s: %w", token.ID, err)
	}
	_, err = r.db.ExecContext(ctx, `INSERT INTO api_tokens
		(id, name, prefix, token_hash, scopes_json, created_at, expires_at, last_used_at, revoked_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, NULL, NULL)`,
		token.ID, token.Name, token.Prefix, tokenHash, string(scopesJSON),
		token.CreatedAt.UnixMilli(), nullMillis(token.ExpiresAt),
	)
	return mapError("create api token", token.ID, err)
}

// GetAPIToken 实现 auth.Repository。
func (r *Repository) GetAPIToken(ctx context.Context, id string) (auth.APIToken, error) {
	row := r.db.QueryRowContext(ctx,
		"SELECT "+tokenColumns+" FROM api_tokens WHERE id = ?", id)
	token, err := scanToken(row)
	return token, mapGetError("get api token", id, err)
}

// GetAPITokenByHash 实现 auth.Repository。
func (r *Repository) GetAPITokenByHash(ctx context.Context, tokenHash []byte) (auth.APIToken, error) {
	row := r.db.QueryRowContext(ctx,
		"SELECT "+tokenColumns+" FROM api_tokens WHERE token_hash = ?", tokenHash)
	token, err := scanToken(row)
	return token, mapGetError("get api token by hash", "<hash>", err)
}

// ListAPITokens 实现 auth.Repository，按 created_at 升序排序。
func (r *Repository) ListAPITokens(ctx context.Context) ([]auth.APIToken, error) {
	rows, err := r.db.QueryContext(ctx,
		"SELECT "+tokenColumns+" FROM api_tokens ORDER BY created_at ASC, id ASC")
	if err != nil {
		return nil, fmt.Errorf("list api tokens: %w", err)
	}
	defer rows.Close()

	var list []auth.APIToken
	for rows.Next() {
		token, err := scanToken(rows)
		if err != nil {
			return nil, fmt.Errorf("scan api token: %w", err)
		}
		list = append(list, token)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate api tokens: %w", err)
	}
	return list, nil
}

// RevokeAPIToken 实现 auth.Repository：幂等软撤销，已撤销的 token
// 保持既有 revoked_at 不变。
func (r *Repository) RevokeAPIToken(ctx context.Context, id string, now time.Time) error {
	res, err := r.db.ExecContext(ctx,
		"UPDATE api_tokens SET revoked_at = ? WHERE id = ? AND revoked_at IS NULL",
		now.UnixMilli(), id)
	if err != nil {
		return fmt.Errorf("revoke api token %s: %w", id, err)
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		// 无行更新：要么 token 不存在，要么已撤销（幂等成功）。
		var count int
		if err := r.db.QueryRowContext(ctx,
			"SELECT count(*) FROM api_tokens WHERE id = ?", id).Scan(&count); err != nil {
			return fmt.Errorf("check api token %s: %w", id, err)
		}
		if count == 0 {
			return fmt.Errorf("%w: api token %s", auth.ErrNotFound, id)
		}
	}
	return nil
}

// TouchAPITokenLastUsed 实现 auth.Repository：节流写入，不存在的
// token 静默忽略（认证路径不因元数据维护失败而失败）。
func (r *Repository) TouchAPITokenLastUsed(ctx context.Context, id string, now time.Time) error {
	thresholdMs := now.Add(-auth.LastUsedThrottle).UnixMilli()
	if _, err := r.db.ExecContext(ctx, `UPDATE api_tokens
		SET last_used_at = ?
		WHERE id = ? AND (last_used_at IS NULL OR last_used_at < ?)`,
		now.UnixMilli(), id, thresholdMs); err != nil {
		return fmt.Errorf("touch api token %s last_used_at: %w", id, err)
	}
	return nil
}

// rowScanner 兼容 *sql.Row 与 *sql.Rows 的 Scan。
type rowScanner interface {
	Scan(dest ...any) error
}

// scanToken 把一行结果转为领域对象；时间为 UTC，可空列还原为指针。
func scanToken(row rowScanner) (auth.APIToken, error) {
	var (
		token       auth.APIToken
		scopesJSON  string
		createdAtMs int64
		expiresAt   sql.NullInt64
		lastUsedAt  sql.NullInt64
		revokedAt   sql.NullInt64
	)
	if err := row.Scan(&token.ID, &token.Name, &token.Prefix, &scopesJSON,
		&createdAtMs, &expiresAt, &lastUsedAt, &revokedAt); err != nil {
		return auth.APIToken{}, err
	}
	// 复用领域校验还原 scope：存储损坏或含未知 scope 时 fail closed，
	// 不让残缺授权集合进入 principal。
	scopes, err := auth.UnmarshalScopes(scopesJSON)
	if err != nil {
		return auth.APIToken{}, fmt.Errorf("unmarshal scopes of api token %s: %w", token.ID, err)
	}
	token.Scopes = scopes
	token.CreatedAt = time.UnixMilli(createdAtMs).UTC()
	token.ExpiresAt = millisPointer(expiresAt)
	token.LastUsedAt = millisPointer(lastUsedAt)
	token.RevokedAt = millisPointer(revokedAt)
	return token, nil
}

// mapGetError 区分「不存在」与扫描错误。
func mapGetError(op, key string, err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: %s", auth.ErrNotFound, key)
	}
	if err != nil {
		return fmt.Errorf("%s %s: %w", op, key, err)
	}
	return nil
}

// mapError 包装写路径错误。token_hash / session_hash 的 UNIQUE 冲突
// 在 crypto/rand 高熵凭据下实际不可达，不映射领域错误，保留驱动
// 原始信息即可。
func mapError(op, id string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s %s: %w", op, id, err)
}

// nullMillis 把时间指针转为可空列：nil 存 NULL。
func nullMillis(t *time.Time) sql.NullInt64 {
	if t == nil {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: t.UnixMilli(), Valid: true}
}

// millisPointer 把可空毫秒列还原为 UTC 时间指针。
func millisPointer(v sql.NullInt64) *time.Time {
	if !v.Valid {
		return nil
	}
	t := time.UnixMilli(v.Int64).UTC()
	return &t
}

// 编译期断言：实现完整接口。
var _ auth.Repository = (*Repository)(nil)

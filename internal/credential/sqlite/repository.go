// Package sqlite 实现 credential.Repository 的 SQLite 持久化。
// 普通读取路径只取状态与指纹列，永不取回 secret_json 明文；secret
// 仅随 Create / ReplaceSecret 写入（整体替换，无三态）。
package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"tinysync/internal/credential"
	sourcesqlite "tinysync/internal/source/sqlite"
	"tinysync/internal/storage"
)

// Repository 是 credential.Repository 的 SQLite 实现。
type Repository struct {
	db *sql.DB
}

// New 构造 Repository；db 需已完成 schema migration（含 credentials 表）。
func New(db *sql.DB) *Repository {
	return &Repository{db: db}
}

// credentialColumns 是普通读取路径的列清单：不含 secret_json。
const credentialColumns = "id, name, type, fingerprint, has_passphrase, created_at, updated_at"

// Create 实现 credential.Repository。
func (r *Repository) Create(ctx context.Context, c credential.Credential, secret credential.Secret) error {
	secretJSON, err := encodeSecret(secret)
	if err != nil {
		return err
	}
	return storage.WithTx(ctx, r.db, func(tx *sql.Tx) error {
		var conflicts int
		if err := tx.QueryRowContext(ctx,
			"SELECT count(*) FROM credentials WHERE name = ?", c.Name,
		).Scan(&conflicts); err != nil {
			return fmt.Errorf("check name conflict: %w", err)
		}
		if conflicts > 0 {
			return credential.ErrConflict
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO credentials
			(id, name, type, secret_json, fingerprint, has_passphrase, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			c.ID, c.Name, string(c.Type), secretJSON, c.Fingerprint,
			boolToInt(c.HasPassphrase), c.CreatedAt.UnixMilli(), c.UpdatedAt.UnixMilli(),
		)
		if err != nil {
			return fmt.Errorf("insert credential %s: %w", c.ID, err)
		}
		return nil
	})
}

// Get 实现 credential.Repository。
func (r *Repository) Get(ctx context.Context, id string) (credential.Credential, error) {
	row := r.db.QueryRowContext(ctx,
		"SELECT "+credentialColumns+" FROM credentials WHERE id = ?", id)
	c, err := scanCredential(row)
	if errors.Is(err, sql.ErrNoRows) {
		return credential.Credential{}, fmt.Errorf("%w: %s", credential.ErrNotFound, id)
	}
	if err != nil {
		return credential.Credential{}, fmt.Errorf("get credential %s: %w", id, err)
	}
	return c, nil
}

// List 实现 credential.Repository，按 name 大小写不敏感排序。
func (r *Repository) List(ctx context.Context) ([]credential.Credential, error) {
	rows, err := r.db.QueryContext(ctx,
		"SELECT "+credentialColumns+" FROM credentials ORDER BY name COLLATE NOCASE ASC, id ASC")
	if err != nil {
		return nil, fmt.Errorf("list credentials: %w", err)
	}
	defer rows.Close()

	var list []credential.Credential
	for rows.Next() {
		c, err := scanCredential(rows)
		if err != nil {
			return nil, fmt.Errorf("scan credential: %w", err)
		}
		list = append(list, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate credentials: %w", err)
	}
	return list, nil
}

// Rename 实现 credential.Repository：只更新名称与 updated_at，不触碰
// secret 派生列——并发换钥与改名写在互不相交的列集上，不存在互相覆盖。
func (r *Repository) Rename(ctx context.Context, id string, name string, updatedAt time.Time) error {
	return storage.WithTx(ctx, r.db, func(tx *sql.Tx) error {
		var conflicts int
		if err := tx.QueryRowContext(ctx,
			"SELECT count(*) FROM credentials WHERE name = ? AND id != ?", name, id,
		).Scan(&conflicts); err != nil {
			return fmt.Errorf("check name conflict: %w", err)
		}
		if conflicts > 0 {
			return credential.ErrConflict
		}
		res, err := tx.ExecContext(ctx,
			"UPDATE credentials SET name = ?, updated_at = ? WHERE id = ?",
			name, updatedAt.UnixMilli(), id,
		)
		if err != nil {
			return fmt.Errorf("rename credential %s: %w", id, err)
		}
		return requireAffected(res, id)
	})
}

// ReplaceSecret 实现 credential.Repository：secret_json 与派生列
// （fingerprint / has_passphrase）在同一事务内整体替换，回显永不指向
// 旧钥匙。
func (r *Repository) ReplaceSecret(ctx context.Context, id string, secret credential.Secret, fingerprint string, hasPassphrase bool, updatedAt time.Time) error {
	secretJSON, err := encodeSecret(secret)
	if err != nil {
		return err
	}
	res, err := r.db.ExecContext(ctx, `UPDATE credentials SET
		secret_json = ?, fingerprint = ?, has_passphrase = ?, updated_at = ?
		WHERE id = ?`,
		secretJSON, fingerprint, boolToInt(hasPassphrase), updatedAt.UnixMilli(), id,
	)
	if err != nil {
		return fmt.Errorf("replace secret of credential %s: %w", id, err)
	}
	return requireAffected(res, id)
}

// Delete 实现 credential.Repository（硬删除）。引用守卫与删除在同一
// 事务内：并发把某源 config 的 credential_id 指向本凭据的写入要么
// 完成于守卫之前（守卫看到引用，返回 ErrInUse），要么在删除提交后
// 才能通过存在性校验写入——fail-closed 语义没有可打穿的窗口。
func (r *Repository) Delete(ctx context.Context, id string) error {
	return storage.WithTx(ctx, r.db, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx,
			"SELECT id, name FROM sources WHERE "+sourcesqlite.CredentialIDExpr+" = ?", id)
		if err != nil {
			return fmt.Errorf("check credential references of %s: %w", id, err)
		}
		var refs []credential.SourceRef
		for rows.Next() {
			var ref credential.SourceRef
			if err := rows.Scan(&ref.ID, &ref.Name); err != nil {
				_ = rows.Close()
				return fmt.Errorf("scan credential reference of %s: %w", id, err)
			}
			refs = append(refs, ref)
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return fmt.Errorf("iterate credential references of %s: %w", id, err)
		}
		_ = rows.Close()
		if len(refs) > 0 {
			return &credential.ErrInUse{Sources: refs}
		}
		res, err := tx.ExecContext(ctx, "DELETE FROM credentials WHERE id = ?", id)
		if err != nil {
			return fmt.Errorf("delete credential %s: %w", id, err)
		}
		return requireAffected(res, id)
	})
}

// requireAffected 把 0 行受影响映射为 ErrNotFound。
func requireAffected(res sql.Result, id string) error {
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("rows affected of credential %s: %w", id, err)
	}
	if n == 0 {
		return fmt.Errorf("%w: %s", credential.ErrNotFound, id)
	}
	return nil
}

// secretJSON 是 secret 的持久化中间形态：空口令不写入 JSON 键
// （与 sources 的匿名语义一致）。
type secretJSON struct {
	PrivateKey           *string `json:"private_key,omitempty"`
	PrivateKeyPassphrase *string `json:"private_key_passphrase,omitempty"`
}

// encodeSecret 序列化 secret；空私钥（理论上被领域校验拦截）返回错误。
func encodeSecret(s credential.Secret) (string, error) {
	if s.PrivateKey == "" {
		return "", fmt.Errorf("%w: encode empty private key", credential.ErrInvalid)
	}
	raw := secretJSON{
		PrivateKey:           &s.PrivateKey,
		PrivateKeyPassphrase: strPtr(s.PrivateKeyPassphrase),
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return "", fmt.Errorf("encode credential secret: %w", err)
	}
	return string(b), nil
}

// strPtr 返回指向副本的指针；空串返回 nil，使空 secret 字段不写入
// JSON 键。
func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// rowScanner 兼容 *sql.Row 与 *sql.Rows 的 Scan。
type rowScanner interface {
	Scan(dest ...any) error
}

// scanCredential 把一行普通读取路径的结果转为领域对象；时间为 UTC。
func scanCredential(row rowScanner) (credential.Credential, error) {
	var (
		c               credential.Credential
		typ             string
		hasPassphrase   int
		createdAtMillis int64
		updatedAtMillis int64
	)
	if err := row.Scan(&c.ID, &c.Name, &typ, &c.Fingerprint,
		&hasPassphrase, &createdAtMillis, &updatedAtMillis); err != nil {
		return credential.Credential{}, err
	}
	c.Type = credential.Type(typ)
	c.HasPassphrase = hasPassphrase == 1
	c.CreatedAt = time.UnixMilli(createdAtMillis).UTC()
	c.UpdatedAt = time.UnixMilli(updatedAtMillis).UTC()
	return c, nil
}

// boolToInt 把布尔值映射为 schema 的 CHECK (has_passphrase IN (0, 1))。
func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// 编译期接口断言。
var _ credential.Repository = (*Repository)(nil)

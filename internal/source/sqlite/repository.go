package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
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

// credentialStateExpr 在 SQL 中从 credentials_json 推导各 secret 的
// 回显状态：普通读取路径只取状态布尔，永不取回 secret 明文。
// SQLite 比较结果为整数，json('true') / json('false') 保证输出
// JSON 布尔。
const credentialStateExpr = `CASE type
	WHEN 'webdav' THEN json_object('webdav', json_object(
		'password_set', CASE WHEN COALESCE(json_extract(credentials_json, '$.password'), '') != '' THEN json('true') ELSE json('false') END))
	WHEN 's3' THEN json_object('s3', json_object(
		'secret_key_set', CASE WHEN COALESCE(json_extract(credentials_json, '$.secret_key'), '') != '' THEN json('true') ELSE json('false') END))
	WHEN 'sftp' THEN json_object('sftp', json_object(
		'password_set', CASE WHEN COALESCE(json_extract(credentials_json, '$.password'), '') != '' THEN json('true') ELSE json('false') END,
		'private_key_set', CASE WHEN COALESCE(json_extract(credentials_json, '$.private_key'), '') != '' THEN json('true') ELSE json('false') END,
		'private_key_passphrase_set', CASE WHEN COALESCE(json_extract(credentials_json, '$.private_key_passphrase'), '') != '' THEN json('true') ELSE json('false') END))
	ELSE '{}'
END`

// sourceColumns 是普通读取路径的列清单：config_json 为非敏感配置，
// credential_state_json 由 SQL 表达式推导，不含任何 secret 明文。
const sourceColumns = "id, name, type, config_json, " + credentialStateExpr + " AS credential_state_json, enabled, created_at, updated_at"

// Create 实现 source.Repository。
func (r *Repository) Create(ctx context.Context, s source.Source, creds source.Credentials) error {
	configJSON, err := encodeConfig(s.Type, s.Config)
	if err != nil {
		return err
	}
	credsJSON, err := encodeCredentials(s.Type, creds)
	if err != nil {
		return err
	}
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
		// legacy 列（endpoint / username / password）自 0005 起清空为
		// schema tombstone，保持 NOT NULL 约束成立，不再是事实来源。
		_, err := tx.ExecContext(ctx, `INSERT INTO sources
			(id, name, type, endpoint, username, password, config_json, credentials_json, enabled, created_at, updated_at)
			VALUES (?, ?, ?, '', '', '', ?, ?, ?, ?, ?)`,
			s.ID, s.Name, string(s.Type), configJSON, credsJSON, boolToInt(s.Enabled),
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

// Update 实现 source.Repository：整体替换可变字段；creds 非 nil 时按
// 组内三态更新 secret（nil 保留、空串清除、非空替换）。secret 合并
// 在事务内读取旧值完成——更新方（service 层）不持有旧 secret 明文。
func (r *Repository) Update(ctx context.Context, s source.Source, creds *source.CredentialsUpdate) error {
	configJSON, err := encodeConfig(s.Type, s.Config)
	if err != nil {
		return err
	}
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

		credsJSON := ""
		if creds != nil {
			var oldRaw string
			if err := tx.QueryRowContext(ctx,
				"SELECT credentials_json FROM sources WHERE id = ?", s.ID,
			).Scan(&oldRaw); err != nil {
				return fmt.Errorf("read credentials of source %s: %w", s.ID, err)
			}
			merged, err := mergeCredentials(s.Type, oldRaw, creds)
			if err != nil {
				return err
			}
			if credsJSON, err = encodeCredentials(s.Type, merged); err != nil {
				return err
			}
		}

		var err error
		if creds != nil {
			_, err = tx.ExecContext(ctx, `UPDATE sources SET
				name = ?, type = ?, config_json = ?, credentials_json = ?,
				enabled = ?, updated_at = ?
				WHERE id = ?`,
				s.Name, string(s.Type), configJSON, credsJSON,
				boolToInt(s.Enabled), s.UpdatedAt.UnixMilli(), s.ID,
			)
		} else {
			_, err = tx.ExecContext(ctx, `UPDATE sources SET
				name = ?, type = ?, config_json = ?,
				enabled = ?, updated_at = ?
				WHERE id = ?`,
				s.Name, string(s.Type), configJSON,
				boolToInt(s.Enabled), s.UpdatedAt.UnixMilli(), s.ID,
			)
		}
		if err != nil {
			return fmt.Errorf("update source %s: %w", s.ID, err)
		}
		return nil
	})
}

// mergeCredentials 把三态更新应用到旧凭据上：nil 保留、空串清除、
// 非空替换。
func mergeCredentials(t source.Type, oldRaw string, update *source.CredentialsUpdate) (source.Credentials, error) {
	old, err := decodeCredentials(t, oldRaw)
	if err != nil {
		return source.Credentials{}, fmt.Errorf("decode existing credentials: %w", err)
	}
	switch t {
	case source.TypeWebDAV:
		if update.WebDAV == nil || update.WebDAV.Password == nil {
			return old, nil
		}
		old.WebDAV.Password = *update.WebDAV.Password
		return old, nil
	case source.TypeS3:
		if update.S3 == nil || update.S3.SecretKey == nil {
			return old, nil
		}
		old.S3.SecretKey = *update.S3.SecretKey
		return old, nil
	case source.TypeSFTP:
		if update.SFTP == nil {
			return old, nil
		}
		if update.SFTP.Password != nil {
			old.SFTP.Password = *update.SFTP.Password
		}
		if update.SFTP.PrivateKey != nil {
			old.SFTP.PrivateKey = *update.SFTP.PrivateKey
		}
		if update.SFTP.PrivateKeyPassphrase != nil {
			old.SFTP.PrivateKeyPassphrase = *update.SFTP.PrivateKeyPassphrase
		}
		return old, nil
	default:
		return source.Credentials{}, fmt.Errorf("%w: %q", source.ErrUnsupportedType, t)
	}
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

// GetCredentials 实现 source.Repository：唯一允许读取 secret 明文的
// 路径，仅用于构造远端客户端与凭据合并。
func (r *Repository) GetCredentials(ctx context.Context, id string) (source.Credentials, error) {
	var typ, credsJSON string
	err := r.db.QueryRowContext(ctx,
		"SELECT type, credentials_json FROM sources WHERE id = ?", id,
	).Scan(&typ, &credsJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return source.Credentials{}, fmt.Errorf("%w: %s", source.ErrNotFound, id)
	}
	if err != nil {
		return source.Credentials{}, fmt.Errorf("get source credentials %s: %w", id, err)
	}
	creds, err := decodeCredentials(source.Type(typ), credsJSON)
	if err != nil {
		return source.Credentials{}, fmt.Errorf("decode credentials of source %s: %w", id, err)
	}
	return creds, nil
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
		configJSON      string
		stateJSON       string
		enabled         int
		createdAtMillis int64
		updatedAtMillis int64
	)
	if err := row.Scan(&s.ID, &s.Name, &typ, &configJSON, &stateJSON,
		&enabled, &createdAtMillis, &updatedAtMillis); err != nil {
		return source.Source{}, err
	}
	s.Type = source.Type(typ)
	s.Enabled = enabled == 1
	s.CreatedAt = time.UnixMilli(createdAtMillis).UTC()
	s.UpdatedAt = time.UnixMilli(updatedAtMillis).UTC()

	config, err := decodeConfig(s.Type, configJSON)
	if err != nil {
		return source.Source{}, fmt.Errorf("source %s: %w", s.ID, err)
	}
	s.Config = config
	if err := json.Unmarshal([]byte(stateJSON), &s.CredentialState); err != nil {
		return source.Source{}, fmt.Errorf("source %s: decode credential state: %w", s.ID, err)
	}
	return s, nil
}

// boolToInt 把布尔值映射为 schema 的 CHECK (enabled IN (0, 1))。
func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// 编译期接口断言。
var _ source.Repository = (*Repository)(nil)

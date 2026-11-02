// Package sqlite 是 OCI Registry 配置（registries 表）的 SQLite 持久化。
package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"tinysync/internal/oci"
	"tinysync/internal/oci/registry"
	"tinysync/internal/storage"
)

// Repository 是 oci.Repository 的 SQLite 实现。
type Repository struct {
	db *sql.DB
}

// New 构造 Repository；db 需已完成 schema 初始化（含 registries 表）。
func New(db *sql.DB) *Repository {
	return &Repository{db: db}
}

// registryColumns 是读取路径的列清单（含全部领域 JSON 列）。
const registryColumns = "id, name, kind, host, username, password, insecure_skip_verify, plain_http, strip_registries_json, path_transform_json, mirrors_json, created_at, updated_at"

// Create 实现 oci.Repository。
func (r *Repository) Create(ctx context.Context, row oci.Registry) error {
	stripJSON, err := marshalStrings(row.StripRegistries)
	if err != nil {
		return err
	}
	transformJSON, err := marshalPathTransform(row.PathTransform)
	if err != nil {
		return err
	}
	mirrorsJSON, err := marshalStrings(row.Mirrors)
	if err != nil {
		return err
	}
	return storage.WithTx(ctx, r.db, func(tx *sql.Tx) error {
		var conflicts int
		if err := tx.QueryRowContext(ctx,
			"SELECT count(*) FROM registries WHERE name = ? COLLATE NOCASE", row.Name,
		).Scan(&conflicts); err != nil {
			return fmt.Errorf("check registry name conflict: %w", err)
		}
		if conflicts > 0 {
			return oci.ErrRegistryConflict
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO registries
			(id, name, kind, host, username, password, insecure_skip_verify, plain_http,
			 strip_registries_json, path_transform_json, mirrors_json, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			row.ID, row.Name, string(row.Kind), row.Host, row.Username, row.Password,
			boolToInt(row.InsecureSkipVerify), boolToInt(row.PlainHTTP),
			stripJSON, transformJSON, mirrorsJSON,
			row.CreatedAt.UnixMilli(), row.UpdatedAt.UnixMilli(),
		)
		if err != nil {
			return fmt.Errorf("insert registry %s: %w", row.ID, err)
		}
		return nil
	})
}

// Get 实现 oci.Repository。
func (r *Repository) Get(ctx context.Context, id string) (oci.Registry, error) {
	row := r.db.QueryRowContext(ctx,
		"SELECT "+registryColumns+" FROM registries WHERE id = ?", id)
	reg, err := scanRegistry(row)
	if errors.Is(err, sql.ErrNoRows) {
		return oci.Registry{}, fmt.Errorf("%w: %s", oci.ErrRegistryNotFound, id)
	}
	if err != nil {
		return oci.Registry{}, fmt.Errorf("get registry %s: %w", id, err)
	}
	return reg, nil
}

// List 实现 oci.Repository，按 name 大小写不敏感排序。
func (r *Repository) List(ctx context.Context) ([]oci.Registry, error) {
	rows, err := r.db.QueryContext(ctx,
		"SELECT "+registryColumns+" FROM registries ORDER BY name COLLATE NOCASE ASC, id ASC")
	if err != nil {
		return nil, fmt.Errorf("list registries: %w", err)
	}
	defer rows.Close()

	var list []oci.Registry
	for rows.Next() {
		reg, err := scanRegistry(rows)
		if err != nil {
			return nil, fmt.Errorf("scan registry: %w", err)
		}
		list = append(list, reg)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate registries: %w", err)
	}
	return list, nil
}

// Update 实现 oci.Repository：整体替换可变字段（kind 不可变更由 service
// 层保证——请求不携带 kind）。
func (r *Repository) Update(ctx context.Context, row oci.Registry) error {
	stripJSON, err := marshalStrings(row.StripRegistries)
	if err != nil {
		return err
	}
	transformJSON, err := marshalPathTransform(row.PathTransform)
	if err != nil {
		return err
	}
	mirrorsJSON, err := marshalStrings(row.Mirrors)
	if err != nil {
		return err
	}
	return storage.WithTx(ctx, r.db, func(tx *sql.Tx) error {
		var exists int
		if err := tx.QueryRowContext(ctx,
			"SELECT count(*) FROM registries WHERE id = ?", row.ID,
		).Scan(&exists); err != nil {
			return fmt.Errorf("check registry existence: %w", err)
		}
		if exists == 0 {
			return fmt.Errorf("%w: %s", oci.ErrRegistryNotFound, row.ID)
		}
		var conflicts int
		if err := tx.QueryRowContext(ctx,
			"SELECT count(*) FROM registries WHERE name = ? COLLATE NOCASE AND id != ?", row.Name, row.ID,
		).Scan(&conflicts); err != nil {
			return fmt.Errorf("check registry name conflict: %w", err)
		}
		if conflicts > 0 {
			return oci.ErrRegistryConflict
		}
		_, err := tx.ExecContext(ctx, `UPDATE registries SET
			name = ?, host = ?, username = ?, password = ?, insecure_skip_verify = ?, plain_http = ?,
			strip_registries_json = ?, path_transform_json = ?, mirrors_json = ?, updated_at = ?
			WHERE id = ?`,
			row.Name, row.Host, row.Username, row.Password,
			boolToInt(row.InsecureSkipVerify), boolToInt(row.PlainHTTP),
			stripJSON, transformJSON, mirrorsJSON,
			row.UpdatedAt.UnixMilli(), row.ID,
		)
		if err != nil {
			return fmt.Errorf("update registry %s: %w", row.ID, err)
		}
		return nil
	})
}

// Delete 实现 oci.Repository（硬删除）。
func (r *Repository) Delete(ctx context.Context, id string) error {
	res, err := r.db.ExecContext(ctx, "DELETE FROM registries WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("delete registry %s: %w", id, err)
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return fmt.Errorf("%w: %s", oci.ErrRegistryNotFound, id)
	}
	return nil
}

// LatestImageStates 实现 oci.Repository：一条 SQL 聚合每个
// （oci 任务, 源镜像）最近一次运行的逐对结果（oci-sync
// GetLatestImageExecutionStates 的移植；源镜像取自明细 detail_json.src）。
func (r *Repository) LatestImageStates(ctx context.Context, jobIDs []string) (map[string]oci.ImageState, error) {
	result := make(map[string]oci.ImageState)
	if len(jobIDs) == 0 {
		return result, nil
	}
	args := make([]any, len(jobIDs))
	for i, id := range jobIDs {
		args[i] = id
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(jobIDs)), ",")
	rows, err := r.db.QueryContext(ctx, `WITH outcomes AS (
		SELECT run.job_id, json_extract(item.detail_json, '$.src') AS src,
		       run.id AS run_id, run.started_at,
		       CASE WHEN SUM(item.status = 'failed') > 0 THEN 'failed'
		            WHEN SUM(item.status = 'succeeded') > 0 THEN 'success'
		            WHEN SUM(item.status = 'skipped') > 0 THEN 'skipped'
		            ELSE MAX(item.status) END AS status,
		       ROW_NUMBER() OVER (PARTITION BY run.job_id, json_extract(item.detail_json, '$.src')
		                          ORDER BY run.started_at DESC, item.id DESC) AS position
		FROM job_run_items item JOIN job_runs run ON run.id = item.run_id
		WHERE run.job_kind = 'oci' AND run.job_id IN (`+placeholders+`)
		GROUP BY run.job_id, json_extract(item.detail_json, '$.src'), run.id
	) SELECT job_id, src, run_id, started_at, status FROM outcomes WHERE position = 1`, args...)
	if err != nil {
		return nil, fmt.Errorf("query latest image states: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var jobID, src, runID, status string
		var startedAt int64
		if err := rows.Scan(&jobID, &src, &runID, &startedAt, &status); err != nil {
			return nil, fmt.Errorf("scan image state: %w", err)
		}
		result[jobID+"\x00"+src] = oci.ImageState{
			JobID:    jobID,
			Source:   src,
			RunID:    runID,
			Status:   status,
			SyncedAt: time.UnixMilli(startedAt).UTC(),
		}
	}
	return result, rows.Err()
}

// rowScanner 兼容 *sql.Row 与 *sql.Rows 的 Scan。
type rowScanner interface {
	Scan(dest ...any) error
}

// scanRegistry 把一行结果转为领域对象；时间为 UTC。
func scanRegistry(row rowScanner) (oci.Registry, error) {
	var (
		reg           oci.Registry
		kind          string
		stripJSON     string
		transformJSON string
		mirrorsJSON   string
		insecure      int
		plainHTTP     int
		createdMilli  int64
		updatedMilli  int64
	)
	if err := row.Scan(&reg.ID, &reg.Name, &kind, &reg.Host, &reg.Username, &reg.Password,
		&insecure, &plainHTTP, &stripJSON, &transformJSON, &mirrorsJSON,
		&createdMilli, &updatedMilli); err != nil {
		return oci.Registry{}, err
	}
	reg.Kind = oci.RegistryKind(kind)
	reg.InsecureSkipVerify = insecure == 1
	reg.PlainHTTP = plainHTTP == 1
	reg.CreatedAt = time.UnixMilli(createdMilli).UTC()
	reg.UpdatedAt = time.UnixMilli(updatedMilli).UTC()

	var err error
	if reg.StripRegistries, err = unmarshalStrings(stripJSON); err != nil {
		return oci.Registry{}, fmt.Errorf("registry %s: decode strip_registries: %w", reg.ID, err)
	}
	if reg.PathTransform, err = unmarshalPathTransform(transformJSON); err != nil {
		return oci.Registry{}, fmt.Errorf("registry %s: decode path_transform: %w", reg.ID, err)
	}
	if reg.Mirrors, err = unmarshalStrings(mirrorsJSON); err != nil {
		return oci.Registry{}, fmt.Errorf("registry %s: decode mirrors: %w", reg.ID, err)
	}
	return reg, nil
}

// marshalStrings 编码字符串列表列；nil / 空归一为 '[]'。
func marshalStrings(list []string) (string, error) {
	if len(list) == 0 {
		return "[]", nil
	}
	data, err := json.Marshal(list)
	if err != nil {
		return "", fmt.Errorf("marshal string list: %w", err)
	}
	return string(data), nil
}

// unmarshalStrings 解码字符串列表列；空对象 / 空数组归一为 nil。
func unmarshalStrings(raw string) ([]string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || trimmed == "{}" || trimmed == "[]" {
		return nil, nil
	}
	var list []string
	if err := json.Unmarshal([]byte(trimmed), &list); err != nil {
		return nil, err
	}
	if len(list) == 0 {
		return nil, nil
	}
	return list, nil
}

// marshalPathTransform 编码路径转换列；nil 归一为 '{}'。
func marshalPathTransform(pt *registry.PathTransformConfig) (string, error) {
	if pt == nil {
		return "{}", nil
	}
	data, err := json.Marshal(pt)
	if err != nil {
		return "", fmt.Errorf("marshal path transform: %w", err)
	}
	return string(data), nil
}

// unmarshalPathTransform 解码路径转换列；空对象归一为 nil（未配置）。
func unmarshalPathTransform(raw string) (*registry.PathTransformConfig, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || trimmed == "{}" || trimmed == "null" {
		return nil, nil
	}
	var pt registry.PathTransformConfig
	if err := json.Unmarshal([]byte(trimmed), &pt); err != nil {
		return nil, err
	}
	if pt.Separator == "" && pt.Mode == "" {
		return nil, nil
	}
	return &pt, nil
}

// boolToInt 把布尔值映射为 schema 的 CHECK 列。
func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// 编译期接口断言。
var _ oci.Repository = (*Repository)(nil)

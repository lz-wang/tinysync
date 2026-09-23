package sqlite

import (
	"context"
	"fmt"

	"tinysync/internal/credential"
)

// 引用索引：凭据引用关系存放在 sources.config_json 的 credential_id
// 字段（当前只有 sftp config 携带），由 Source 存储回答「谁在引用」，
// 供回显计数使用；凭据删除守卫在 credentials 侧的同一事务内复用
// 同一判定表达式，两侧语义不会漂移。

// CredentialIDExpr 是 sources 行中 credential_id 引用字段的 SQL 表达
// 式：引用判定（回显索引与删除守卫）的唯一事实来源。
const CredentialIDExpr = "json_extract(config_json, '$.credential_id')"

// SourcesReferencingCredential 实现 credential.ReferenceIndex：返回
// config 中 credential_id 指向该凭据的同步源清单。
func (r *Repository) SourcesReferencingCredential(ctx context.Context, credentialID string) ([]credential.SourceRef, error) {
	refs, err := r.AllCredentialReferences(ctx)
	if err != nil {
		return nil, err
	}
	return refs[credentialID], nil
}

// AllCredentialReferences 实现 credential.ReferenceIndex：一次查询
// 返回全部凭据的引用清单（按凭据 ID 分组）。config 不携带 credential_id
// 的行（其他协议、未引用的 sftp）不产生条目。
func (r *Repository) AllCredentialReferences(ctx context.Context) (map[string][]credential.SourceRef, error) {
	rows, err := r.db.QueryContext(ctx,
		"SELECT id, name, "+CredentialIDExpr+" FROM sources"+
			" WHERE "+CredentialIDExpr+" IS NOT NULL")
	if err != nil {
		return nil, fmt.Errorf("list credential references: %w", err)
	}
	defer rows.Close()

	refs := make(map[string][]credential.SourceRef)
	for rows.Next() {
		var credentialID string
		var ref credential.SourceRef
		if err := rows.Scan(&ref.ID, &ref.Name, &credentialID); err != nil {
			return nil, fmt.Errorf("scan credential reference: %w", err)
		}
		refs[credentialID] = append(refs[credentialID], ref)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate credential references: %w", err)
	}
	return refs, nil
}

// 编译期接口断言。
var _ credential.ReferenceIndex = (*Repository)(nil)

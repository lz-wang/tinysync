// Package storage 的数据库初始化：全新建库（schema.sql 一次性生效），
// 不提供旧数据迁移——本轮为破坏性变更，旧版数据库明确报错。
package storage

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
)

// schemaFS 内嵌唯一的完整 schema（无迁移序列）。
//
//go:embed schema.sql
var schemaFS embed.FS

// SchemaVersion 是本二进制创建 / 接受的数据库 schema 版本
// （PRAGMA user_version）。版本取 14：紧接旧迁移序列的终点（13），
// 使「旧库（≤13）」与「未来二进制创建的库（>14）」可按大小判方向。
const SchemaVersion = 14

// ErrSchemaIncompatible 表示数据库与本二进制的 schema 不兼容：
// 可能是旧版 TinySync 数据库、未来版本创建的库或外来 SQLite 文件。
// 正确动作是换用新的数据目录（或升级二进制），程序不自动删除或改写。
var ErrSchemaIncompatible = errors.New("database schema incompatible with this build")

// CurrentVersion 返回数据库当前 schema 版本（PRAGMA user_version），
// 全新数据库为 0。
func CurrentVersion(ctx context.Context, db *sql.DB) (int, error) {
	var version int
	if err := db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return 0, fmt.Errorf("read user_version: %w", err)
	}
	return version, nil
}

// Ensure 保证数据库完成初始化：
//   - 全新数据库（version 0 且无业务表）：单事务执行 schema.sql 并写入
//     user_version，全部业务表与版本号原子生效；
//   - 已是本版本 schema：幂等返回；
//   - 任何其它情形（旧序列版本、未来版本、外来文件）：ErrSchemaIncompatible，
//     不自动删除数据、不做降级 / 迁移。
func Ensure(ctx context.Context, db *sql.DB) error {
	version, err := CurrentVersion(ctx, db)
	if err != nil {
		return err
	}
	if version == SchemaVersion {
		return nil
	}
	if version != 0 {
		return fmt.Errorf("%w: schema version %d, want %d; start with a fresh data directory",
			ErrSchemaIncompatible, version, SchemaVersion)
	}
	hasTables, err := hasUserTables(ctx, db)
	if err != nil {
		return err
	}
	if hasTables {
		return fmt.Errorf("%w: version 0 database already has tables", ErrSchemaIncompatible)
	}
	schema, err := schemaFS.ReadFile("schema.sql")
	if err != nil {
		return fmt.Errorf("read embedded schema: %w", err)
	}
	return WithTx(ctx, db, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, string(schema)); err != nil {
			return fmt.Errorf("apply schema: %w", err)
		}
		// PRAGMA 不支持参数绑定；SchemaVersion 是内部常量，拼接无注入风险。
		if _, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", SchemaVersion)); err != nil {
			return fmt.Errorf("set schema version: %w", err)
		}
		return nil
	})
}

// hasUserTables 判断库中是否已有业务表（排除 SQLite 内部表）。
func hasUserTables(ctx context.Context, db *sql.DB) (bool, error) {
	var count int
	err := db.QueryRowContext(ctx,
		"SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%'",
	).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("inspect sqlite_master: %w", err)
	}
	return count > 0, nil
}

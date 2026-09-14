package storage

import (
	"context"
	"database/sql"
	"fmt"
)

// WithTx 在单个事务中执行 fn：fn 返回 nil 则提交，否则回滚并透传错误。
// storage 层统一的 transaction helper。
func WithTx(ctx context.Context, db *sql.DB, fn func(tx *sql.Tx) error) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit tx: %w", err)
	}
	return nil
}

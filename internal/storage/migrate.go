package storage

import (
	"context"
	"crypto/rand"
	"database/sql"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"
)

// randomHex 返回 n 字节 cryptographically 随机数据的 hex 编码，
// 用于构造不冲突的备份文件名后缀。
func randomHex(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

//go:embed migrations/*.sql
var migrationFS embed.FS

// backupsDirName 是数据目录下存放迁移前备份的子目录名。
const backupsDirName = "backups"

// migration 是单个 schema 版本：目标版本号与其 SQL 内容。
type migration struct {
	version int
	sql     string
}

// CurrentVersion 返回数据库当前 schema 版本（PRAGMA user_version），
// 全新数据库为 0。
func CurrentVersion(ctx context.Context, db *sql.DB) (int, error) {
	var version int
	if err := db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return 0, fmt.Errorf("read user_version: %w", err)
	}
	return version, nil
}

// Migrate 将数据库 schema 推进到内嵌 migration 的最新版本。
// 旧库向前迁移，迁移前用 VACUUM INTO 生成一致性备份；
// 更高版本的数据库拒绝迁移（提示升级二进制）；已是最新版本时幂等返回。
// dataDir 用于放置备份文件。
func Migrate(ctx context.Context, db *sql.DB, dataDir string) error {
	return migrate(ctx, db, dataDir, migrationFS)
}

// migrate 是 Migrate 的可注入实现，测试用它替换 migration 文件来源。
func migrate(ctx context.Context, db *sql.DB, dataDir string, fsys fs.FS) error {
	migrations, err := loadMigrations(fsys)
	if err != nil {
		return err
	}
	if len(migrations) == 0 {
		return errors.New("no migrations embedded")
	}
	current, err := CurrentVersion(ctx, db)
	if err != nil {
		return err
	}
	latest := migrations[len(migrations)-1].version
	if current > latest {
		return fmt.Errorf("database schema version %d is newer than supported %d; upgrade tinysync first", current, latest)
	}
	if current == latest {
		return nil
	}

	// 已有数据的库升级前先做完整性检查与一致性备份；全新库（无业务表）
	// 跳过，不为空升级生成空备份。corruption 守卫在备份之前：已损坏的
	// 数据库不再继续 migration，避免在坏数据上继续写版本号。
	hasTables, err := hasUserTables(ctx, db)
	if err != nil {
		return err
	}
	if hasTables {
		if err := verifyIntegrity(ctx, db); err != nil {
			return err
		}
		if err := backupDatabase(ctx, db, dataDir, current); err != nil {
			return err
		}
	}

	for _, m := range migrations {
		if m.version <= current {
			continue
		}
		if err := applyMigration(ctx, db, m); err != nil {
			return fmt.Errorf("apply migration %04d: %w", m.version, err)
		}
	}
	return nil
}

// loadMigrations 解析 migrations 目录下的 SQL 文件并按版本号排序。
// 文件名必须为 NNNN_<名称>.sql，NNNN 即目标 schema 版本且不可重复。
func loadMigrations(fsys fs.FS) ([]migration, error) {
	entries, err := fs.ReadDir(fsys, "migrations")
	if err != nil {
		return nil, fmt.Errorf("read migrations dir: %w", err)
	}
	var list []migration
	seen := make(map[int]bool)
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".sql") {
			continue
		}
		prefix, _, found := strings.Cut(strings.TrimSuffix(name, ".sql"), "_")
		if !found {
			return nil, fmt.Errorf("migration %s: name must be NNNN_<desc>.sql", name)
		}
		version, err := strconv.Atoi(prefix)
		if err != nil || version <= 0 {
			return nil, fmt.Errorf("migration %s: invalid version %q", name, prefix)
		}
		if seen[version] {
			return nil, fmt.Errorf("migration %s: duplicate version %d", name, version)
		}
		seen[version] = true
		content, err := fs.ReadFile(fsys, "migrations/"+name)
		if err != nil {
			return nil, fmt.Errorf("read migration %s: %w", name, err)
		}
		list = append(list, migration{version: version, sql: string(content)})
	}
	sort.Slice(list, func(i, j int) bool { return list[i].version < list[j].version })
	return list, nil
}

// applyMigration 在单个事务中执行一个版本的 migration：
// SQL 与 user_version 原子生效，失败整体回滚到迁移前状态。
func applyMigration(ctx context.Context, db *sql.DB, m migration) error {
	return WithTx(ctx, db, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, m.sql); err != nil {
			return err
		}
		// PRAGMA 不支持参数绑定；version 是内部 int，拼接无注入风险。
		if _, err := tx.ExecContext(ctx, "PRAGMA user_version = "+strconv.Itoa(m.version)); err != nil {
			return err
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

// backupDatabase 用 VACUUM INTO 生成迁移前的一致性备份：
// <dataDir>/backups/tinysync-v<fromVersion>-<时间戳>-<随机后缀>.db。
// 时间戳仅秒级精度，随机后缀保证同秒内的失败重试不会因目标已存在而冲突。
func backupDatabase(ctx context.Context, db *sql.DB, dataDir string, fromVersion int) error {
	target, err := backupPath(dataDir, fmt.Sprintf("v%d", fromVersion))
	if err != nil {
		return err
	}
	return Backup(ctx, db, target)
}

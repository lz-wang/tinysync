package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// IntegrityReport 是一次数据库完整性检查的结果汇总。
type IntegrityReport struct {
	// QuickCheck 是 PRAGMA quick_check 的结果（健康时为 "ok"）。
	QuickCheck string
	// ForeignKeyViolations 是 PRAGMA foreign_key_check 返回的违规行数。
	ForeignKeyViolations int
	// UserVersion 是数据库当前 schema 版本。
	UserVersion int
	// LatestVersion 是本二进制内嵌 migration 的最新 schema 版本。
	LatestVersion int
}

// inspectDSNQuery 是 Inspect 使用的只读 DSN 基线：不启用
// journal_mode 等 PRAGMA——只读打开不应把被检查的文件（可能是备份）
// 转换为 WAL 或留下 -wal / -shm 副产物。modernc.org/sqlite 支持
// SQLite URI 的 mode=ro。
const inspectDSNQuery = "?mode=ro&_pragma=busy_timeout(5000)"

// dsnForReadonly 返回指向 dbPath 的只读 SQLite file: URI。
func dsnForReadonly(dbPath string) string {
	return encodeSQLiteURI(dbPath) + inspectDSNQuery
}

// Check 对已打开的数据库执行完整性检查：quick_check、外键一致性
// 与 schema 版本边界。契约：
//   - quick_check != ok → 失败；
//   - 存在外键违规 → 失败；
//   - schema 版本高于本二进制支持 → 失败（提示升级二进制）；
//   - 不自动「修复」数据库，不静默忽略 corruption。
func Check(ctx context.Context, db *sql.DB) (IntegrityReport, error) {
	report := IntegrityReport{}
	latest, err := latestEmbeddedVersion()
	if err != nil {
		return report, err
	}
	report.LatestVersion = latest

	quick, err := quickCheck(ctx, db)
	if err != nil {
		return report, err
	}
	report.QuickCheck = quick
	if quick != "ok" {
		return report, fmt.Errorf("integrity check failed: quick_check reports %q", quick)
	}

	violations, err := foreignKeyViolations(ctx, db)
	if err != nil {
		return report, err
	}
	report.ForeignKeyViolations = violations
	if violations > 0 {
		return report, fmt.Errorf("integrity check failed: %d foreign key violation(s)", violations)
	}

	version, err := CurrentVersion(ctx, db)
	if err != nil {
		return report, err
	}
	report.UserVersion = version
	if version > latest {
		return report, fmt.Errorf("database schema version %d is newer than supported %d; upgrade tinysync first", version, latest)
	}
	return report, nil
}

// Inspect 以只读方式打开指定路径的 SQLite 数据库并执行 Check，用毕
// 关闭连接。被检查的文件不会被修改（不转换 journal mode、不产生
// -wal / -shm 副产物）。文件不存在或不是合法 SQLite 数据库时返回错误。
func Inspect(ctx context.Context, path string) (IntegrityReport, error) {
	db, err := sql.Open("sqlite", dsnForReadonly(path))
	if err != nil {
		return IntegrityReport{}, fmt.Errorf("open sqlite %s: %w", path, err)
	}
	defer func() { _ = db.Close() }()
	// 只读检查无需写连接池。
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if err := db.PingContext(ctx); err != nil {
		return IntegrityReport{}, fmt.Errorf("open sqlite %s: %w", path, err)
	}
	return Check(ctx, db)
}

// Backup 用 VACUUM INTO 把数据库内容生成为 target 指定路径的一致性
// 快照（WAL 模式下裸复制主数据库文件不可靠，统一走本函数）。target
// 的父目录必须已存在；POSIX 下文件权限收敛为 0600（备份含密码明文）。
func Backup(ctx context.Context, db *sql.DB, target string) error {
	// 路径中的单引号按 SQL 字符串规则转义；斜杠统一为 /（Windows 可接受）。
	quoted := strings.ReplaceAll(filepath.ToSlash(target), "'", "''")
	if _, err := db.ExecContext(ctx, "VACUUM INTO '"+quoted+"'"); err != nil {
		return fmt.Errorf("vacuum into %s: %w", target, err)
	}
	// 备份含密码明文，与主库同样收敛权限（POSIX 生效）。
	if runtime.GOOS != "windows" {
		if err := os.Chmod(target, 0o600); err != nil {
			return fmt.Errorf("chmod %s: %w", target, err)
		}
	}
	return nil
}

// verifyIntegrity 是 migration 前的完整性守卫：已损坏的数据库不再
// 继续 migration（版本边界由 migrate 自身的 current > latest 检查负责）。
func verifyIntegrity(ctx context.Context, db *sql.DB) error {
	quick, err := quickCheck(ctx, db)
	if err != nil {
		return err
	}
	if quick != "ok" {
		return fmt.Errorf("integrity check failed before migration: quick_check reports %q", quick)
	}
	violations, err := foreignKeyViolations(ctx, db)
	if err != nil {
		return err
	}
	if violations > 0 {
		return fmt.Errorf("integrity check failed before migration: %d foreign key violation(s)", violations)
	}
	return nil
}

// ManualBackupPath 为手动备份生成目标路径
// <dataDir>/backups/tinysync-manual-<时间戳>-<随机后缀>.db，并确保
// backups 子目录存在（0700）。时间戳仅秒级精度，随机后缀保证同秒内
// 的重试不会因目标已存在而冲突。
func ManualBackupPath(dataDir string) (string, error) {
	return backupPath(dataDir, "manual")
}

// PreRestoreBackupPath 为 restore 前的安全备份生成目标路径。
func PreRestoreBackupPath(dataDir string) (string, error) {
	return backupPath(dataDir, "prerestore")
}

// backupPath 生成 <dataDir>/backups/tinysync-<kind>-<时间戳>-<随机>.db。
func backupPath(dataDir, kind string) (string, error) {
	backupDir := filepath.Join(dataDir, backupsDirName)
	if err := os.MkdirAll(backupDir, 0o700); err != nil {
		return "", fmt.Errorf("create backup dir %s: %w", backupDir, err)
	}
	suffix, err := randomHex(4)
	if err != nil {
		return "", fmt.Errorf("generate backup suffix: %w", err)
	}
	name := fmt.Sprintf("tinysync-%s-%s-%s.db", kind, time.Now().Format("20060102T150405"), suffix)
	return filepath.Join(backupDir, name), nil
}

// quickCheck 执行 PRAGMA quick_check：健康时返回单行 "ok"；损坏时
// 可能返回多行问题描述，这里取首行拼接，让错误信息可定位。
func quickCheck(ctx context.Context, db *sql.DB) (string, error) {
	rows, err := db.QueryContext(ctx, "PRAGMA quick_check")
	if err != nil {
		return "", fmt.Errorf("quick_check: %w", err)
	}
	defer rows.Close()
	var first string
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			return "", fmt.Errorf("scan quick_check: %w", err)
		}
		if first == "" {
			first = line
		} else if first != "ok" {
			// 已有问题，追加细节帮助定位（封顶避免超长）。
			if len(first) < 256 {
				first += "; " + line
			}
		}
	}
	if err := rows.Err(); err != nil {
		return "", fmt.Errorf("quick_check: %w", err)
	}
	if first == "" {
		return "", errors.New("quick_check returned no rows")
	}
	return first, nil
}

// foreignKeyViolations 统计 PRAGMA foreign_key_check 的违规行数。
func foreignKeyViolations(ctx context.Context, db *sql.DB) (int, error) {
	rows, err := db.QueryContext(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		return 0, fmt.Errorf("foreign_key_check: %w", err)
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		count++
	}
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("foreign_key_check: %w", err)
	}
	return count, nil
}

// latestEmbeddedVersion 返回内嵌 migration 的最新 schema 版本。
func latestEmbeddedVersion() (int, error) {
	migrations, err := loadMigrations(migrationFS)
	if err != nil {
		return 0, err
	}
	if len(migrations) == 0 {
		return 0, errors.New("no migrations embedded")
	}
	return migrations[len(migrations)-1].version, nil
}

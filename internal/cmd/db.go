// db 子命令：数据库维护入口（check / backup / restore）。全部复用
// datadir 单实例锁——与 serve 互斥执行，保证维护期间没有并发写入者；
// restore 是破坏性操作，必须 --force 显式确认，且恢复前自动生成当前
// 数据库的 safety backup。
package cmd

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"

	"tinysync/internal/config"
	"tinysync/internal/instance"
	"tinysync/internal/storage"
)

// dbInput 是 db 子命令的解析后输入。
type dbInput struct {
	DataDir string
	// From 是 restore 的备份来源路径。
	From string
	// Force 是 restore 的破坏性操作确认。
	Force bool
}

// execDBCheck 执行 tinysync db check：持有 datadir lock 打开数据库，
// 输出完整性报告（quick_check / 外键违规 / schema 版本）。
func execDBCheck(ctx context.Context, in dbInput, stdout, stderr io.Writer) error {
	cfg, err := config.Load(config.Options{DataDir: in.DataDir})
	if err != nil {
		return err
	}
	lock, err := instance.Acquire(cfg.DataDir)
	if err != nil {
		return err
	}
	defer func() { _ = lock.Release() }()

	db, err := storage.Open(cfg.DataDir)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer func() { _ = db.Close() }()
	report, err := storage.Check(ctx, db)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "quick_check=%s foreign_key_violations=%d schema=v%d (latest v%d)\n",
		report.QuickCheck, report.ForeignKeyViolations, report.UserVersion, report.LatestVersion)
	return nil
}

// execDBBackup 执行 tinysync db backup：生成
// <datadir>/backups/tinysync-manual-<时间戳>-<随机>.db 一致性快照
// （VACUUM INTO，不是裸复制主库文件——WAL 模式下裸复制不可靠）。
func execDBBackup(ctx context.Context, in dbInput, stdout, stderr io.Writer) error {
	cfg, err := config.Load(config.Options{DataDir: in.DataDir})
	if err != nil {
		return err
	}
	lock, err := instance.Acquire(cfg.DataDir)
	if err != nil {
		return err
	}
	defer func() { _ = lock.Release() }()

	db, err := storage.Open(cfg.DataDir)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer func() { _ = db.Close() }()
	target, err := storage.ManualBackupPath(cfg.DataDir)
	if err != nil {
		return err
	}
	if err := storage.Backup(ctx, db, target); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "backup written to %s\n", target)
	return nil
}

// execDBRestore 执行 tinysync db restore：validate source backup →
// 备份当前 DB（safety backup；「健康」= 可打开且通过完整性检查，
// 此时备份必须成功，失败即在任何替换发生前中止——磁盘空间不足、
// 权限错误或 backup 目录故障时继续覆盖健康库不可接受；打不开或
// 可打开但真实损坏（ErrIntegrity）的库告警后跳过备份继续恢复——
// restore 是救灾入口；Check 失败不默认等价于损坏：schema 比本
// 二进制新（ErrNewerSchema）与 context / I/O 等运维失败一律在任何
// 替换前中止，旧 binary 覆盖健康的更高版本库是 downgrade
// data-loss 而非救灾）→ 同目录 staging 文件 + fsync → 替换数据库 →
// 清理遗留 -wal / -shm → reopen + integrity check。schema 较旧的
// 备份恢复成功，下次 serve 正常向前迁移；schema 较新的备份拒绝
// 恢复（不做 downgrade migration）。
func execDBRestore(ctx context.Context, in dbInput, stdout, stderr io.Writer) error {
	if !in.Force {
		return fmt.Errorf("restore replaces the live database; re-run with --force to confirm")
	}
	cfg, err := config.Load(config.Options{DataDir: in.DataDir})
	if err != nil {
		return err
	}
	lock, err := instance.Acquire(cfg.DataDir)
	if err != nil {
		return err
	}
	defer func() { _ = lock.Release() }()

	// 1. validate source backup：非法 SQLite / 损坏 / newer schema
	//    一律在改动任何文件之前拒绝。
	report, err := storage.Inspect(ctx, in.From)
	if err != nil {
		return fmt.Errorf("validate backup %s: %w", in.From, err)
	}

	// 2. pre-restore safety backup：「健康」的判定是可打开且通过完整
	//    性检查——两者都满足时备份必须成功，路径或 VACUUM 失败即在
	//    任何替换发生前中止（健康的库不能在丢失最后快照的情况下被
	//    覆盖）；可打开但 Check 判定为真实损坏（ErrIntegrity：页级
	//    损坏 / 外键违规）的库，告警后跳过备份继续恢复——restore 是
	//    救灾入口，不能被损坏库自身的 VACUUM 失败阻断。Check 失败
	//    不默认等价于损坏：schema 比本二进制新（ErrNewerSchema，正确
	//    动作是升级 binary 而非降级覆盖）与 context 取消 / I/O 等
	//    运维失败一律在任何替换发生前中止。
	db, err := storage.Open(cfg.DataDir)
	if err != nil {
		fmt.Fprintf(stderr, "warning: current database unavailable, skipping pre-restore backup: %v\n", err)
	} else if _, cerr := storage.Check(ctx, db); cerr != nil {
		_ = db.Close()
		switch {
		case errors.Is(cerr, storage.ErrNewerSchema):
			return fmt.Errorf("current database schema is newer than this binary supports; upgrade tinysync instead of restoring an older backup: %w", cerr)
		case errors.Is(cerr, storage.ErrIntegrity):
			fmt.Fprintf(stderr, "warning: current database is corrupted, skipping pre-restore backup: %v\n", cerr)
		default:
			return fmt.Errorf("check current database failed; restore aborted before replacing it: %w", cerr)
		}
	} else {
		target, perr := storage.PreRestoreBackupPath(cfg.DataDir)
		if perr != nil {
			_ = db.Close()
			return fmt.Errorf("pre-restore safety backup failed; restore aborted before replacing the healthy database: %w", perr)
		}
		if berr := storage.Backup(ctx, db, target); berr != nil {
			_ = db.Close()
			return fmt.Errorf("pre-restore safety backup failed; restore aborted before replacing the healthy database: %w", berr)
		}
		fmt.Fprintf(stdout, "pre-restore safety backup: %s\n", target)
		_ = db.Close()
	}

	// 3. staging：来源复制到与目标同目录的临时文件并 fsync，保证
	//    rename 生效时数据已落盘（同目录保证同一文件系统）。
	dbPath := filepath.Join(cfg.DataDir, storage.DatabaseFileName)
	suffix, err := randomSuffix()
	if err != nil {
		return err
	}
	staging := dbPath + ".restore-" + suffix
	if err := copyFileSync(in.From, staging); err != nil {
		return err
	}
	defer func() { _ = os.Remove(staging) }() // rename 成功后为无害空操作

	// 4-5. 替换数据库并清理旧库遗留的 WAL sidecar：stale WAL 与新库
	//      内容不匹配，绝不能留给 reopen 时的 recovery。
	if err := os.Rename(staging, dbPath); err != nil {
		return fmt.Errorf("replace database: %w", err)
	}
	for _, sidecar := range []string{dbPath + "-wal", dbPath + "-shm"} {
		if err := os.Remove(sidecar); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove stale sidecar %s: %w", sidecar, err)
		}
	}
	if err := syncDir(cfg.DataDir); err != nil {
		fmt.Fprintf(stderr, "warning: fsync datadir failed: %v\n", err)
	}

	// 6. reopen + integrity check：恢复后的库必须立即可用。
	reopened, err := storage.Open(cfg.DataDir)
	if err != nil {
		return fmt.Errorf("reopen restored database: %w", err)
	}
	defer func() { _ = reopened.Close() }()
	if _, err := storage.Check(ctx, reopened); err != nil {
		return fmt.Errorf("integrity check after restore: %w", err)
	}

	if report.UserVersion < report.LatestVersion {
		fmt.Fprintf(stdout, "database restored from %s (schema v%d, older than supported v%d); next serve migrates forward\n",
			in.From, report.UserVersion, report.LatestVersion)
	} else {
		fmt.Fprintf(stdout, "database restored from %s (schema v%d)\n", in.From, report.UserVersion)
	}
	return nil
}

// copyFileSync 复制单个文件并在关闭前 fsync 目标。
func copyFileSync(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("open %s: %w", src, err)
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create staging %s: %w", dst, err)
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return fmt.Errorf("copy %s -> %s: %w", src, dst, err)
	}
	if err := out.Sync(); err != nil {
		_ = out.Close()
		return fmt.Errorf("fsync %s: %w", dst, err)
	}
	return out.Close()
}

// syncDir 对目录执行 fsync，保证 rename / unlink 的目录项变更落盘
// （POSIX 生效；Windows 目录句柄不支持 Sync，直接跳过）。
func syncDir(path string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	d, err := os.Open(path)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

// randomSuffix 生成 8 字节随机 hex，用于 staging 临时文件命名。
func randomSuffix() (string, error) {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate random suffix: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

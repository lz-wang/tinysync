package cmd

import (
	"bytes"
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"tinysync/internal/auth"
	authsqlite "tinysync/internal/auth/sqlite"
	"tinysync/internal/instance"
	"tinysync/internal/storage"
)

// bootstrapDBDataDir 迁移一个临时 datadir 并写入可回读的业务数据：
// 管理员密码、带密码的 Source 与引用它的 Job——restore 往返据此验证
// secret / 凭据 / Job 全部恢复。
func bootstrapDBDataDir(t *testing.T, dataDir string) *sql.DB {
	t.Helper()
	ctx := context.Background()
	db, err := storage.Open(dataDir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := storage.Migrate(ctx, db, dataDir); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	authSvc := auth.NewService(authsqlite.NewRepository(db))
	if err := authSvc.SetAdminPassword(ctx, "restore-roundtrip-pass-123"); err != nil {
		t.Fatalf("SetAdminPassword: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO sources
		(id, name, type, endpoint, username, password, enabled, created_at, updated_at)
		VALUES ('src_rt', 'roundtrip', 'webdav', 'https://example.com', 'user', 'S3cret-Roundtrip', 1, 1, 1)`); err != nil {
		t.Fatalf("seed source: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO sync_jobs
		(id, name, source_id, remote_root, local_root, mode, enabled, created_at, updated_at)
		VALUES ('job_rt', 'roundtrip-job', 'src_rt', '/', '/tmp/rt', 'mirror', 1, 1, 1)`); err != nil {
		t.Fatalf("seed job: %v", err)
	}
	return db
}

// runDB 执行一个 db 子命令并返回 stdout / stderr 与错误。
func runDB(t *testing.T, in dbInput) (string, string, error) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	err := execDBRestore(context.Background(), in, &stdout, &stderr)
	return stdout.String(), stderr.String(), err
}

// backupHealthyDB 做一份手动备份并返回其路径。
func backupHealthyDB(t *testing.T, dataDir string) string {
	t.Helper()
	var stdout, stderr bytes.Buffer
	if err := execDBBackup(context.Background(), dbInput{DataDir: dataDir}, &stdout, &stderr); err != nil {
		t.Fatalf("db backup: %v", err)
	}
	backupPath := strings.TrimSpace(strings.TrimPrefix(stdout.String(), "backup written to "))
	if backupPath == "" || !strings.HasSuffix(backupPath, ".db") {
		t.Fatalf("cannot parse backup path from %q", stdout.String())
	}
	return backupPath
}

// 当前库健康时，pre-restore safety backup 失败必须在任何替换发生前
// 中止 restore（fail-closed）：健康库在丢失最后快照的情况下继续覆盖
// 不可接受。backups 路径被同名普通文件占住即可注入 backup 失败。
func TestDBRestoreAbortsWhenSafetyBackupFailsOnHealthyDB(t *testing.T) {
	dataDir := t.TempDir()
	bootstrapDBDataDir(t, dataDir)
	backupPath := backupHealthyDB(t, dataDir)

	// 备份之后向当前库写入标记：restore 若被替换则标记随旧库消失。
	raw, err := sql.Open("sqlite", "file:"+filepath.Join(dataDir, storage.DatabaseFileName))
	if err != nil {
		t.Fatalf("open for marker: %v", err)
	}
	if _, err := raw.Exec("CREATE TABLE restore_marker (id INTEGER)"); err != nil {
		t.Fatalf("create marker: %v", err)
	}
	if _, err := raw.Exec("INSERT INTO restore_marker VALUES (1)"); err != nil {
		t.Fatalf("insert marker: %v", err)
	}
	_ = raw.Close()

	// 注入 backup 失败：来源备份先挪出 backups 目录，再把 backups
	// 目录位置用同名文件占住（PreRestoreBackupPath 的 MkdirAll 失败）。
	movedBackup := filepath.Join(dataDir, "source-backup.db")
	if err := os.Rename(backupPath, movedBackup); err != nil {
		t.Fatalf("move source backup: %v", err)
	}
	if err := os.RemoveAll(filepath.Join(dataDir, "backups")); err != nil {
		t.Fatalf("remove backups dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "backups"), []byte("not a dir"), 0o600); err != nil {
		t.Fatalf("write backups decoy: %v", err)
	}

	_, _, err = runDB(t, dbInput{DataDir: dataDir, From: movedBackup, Force: true})
	if err == nil {
		t.Fatal("restore with failing safety backup on healthy db = nil, want abort")
	}
	if !strings.Contains(err.Error(), "pre-restore safety backup") {
		t.Errorf("restore error %v, want safety backup failure", err)
	}

	// 当前库未被替换：标记仍在。
	db, err := sql.Open("sqlite", "file:"+filepath.Join(dataDir, storage.DatabaseFileName))
	if err != nil {
		t.Fatalf("reopen current db: %v", err)
	}
	defer db.Close()
	var marked int
	if err := db.QueryRow("SELECT count(*) FROM restore_marker").Scan(&marked); err != nil || marked != 1 {
		t.Errorf("marker query = (%d, %v), want healthy db left untouched", marked, err)
	}
}

// corruptMiddlePage 覆盖数据库文件第二个数据页（offset = 默认页大小
// 4096 起）为 0xFF：保留 header 的页级损坏——storage.Open 仍可打开，
// quick_check 失败。与 storage 包测试同一手法。
func corruptMiddlePage(t *testing.T, dbPath string) {
	t.Helper()
	f, err := os.OpenFile(dbPath, os.O_RDWR, 0o600)
	if err != nil {
		t.Fatalf("open db for corruption: %v", err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		t.Fatalf("stat db: %v", err)
	}
	if info.Size() < 8192 {
		t.Fatal("database smaller than two pages; cannot corrupt middle page")
	}
	if _, err := f.WriteAt(make([]byte, 512), 4096); err != nil {
		t.Fatalf("corrupt middle page: %v", err)
	}
	if err := f.Sync(); err != nil {
		t.Fatalf("sync corrupted db: %v", err)
	}
}

// 「可 Open 但已损坏」的库必须允许恢复：Open 成功 + integrity check
// 失败 → corrupted → 告警并继续 restore。损坏库的 VACUUM INTO safety
// backup 大概率失败，若据此中止恢复，最需要救灾的场景反而被损坏库
// 自身阻断。
func TestDBRestoreProceedsWhenOpenableDBCorrupted(t *testing.T) {
	dataDir := t.TempDir()
	db := bootstrapDBDataDir(t, dataDir)
	backupPath := backupHealthyDB(t, dataDir)

	// 关闭连接、清掉 sidecar 后制造页级损坏（遗留 WAL 可能把内容写回）。
	if err := db.Close(); err != nil {
		t.Fatalf("close bootstrap db: %v", err)
	}
	for _, sidecar := range []string{"-wal", "-shm"} {
		if err := os.Remove(filepath.Join(dataDir, storage.DatabaseFileName+sidecar)); err != nil && !os.IsNotExist(err) {
			t.Fatalf("remove sidecar: %v", err)
		}
	}
	corruptMiddlePage(t, filepath.Join(dataDir, storage.DatabaseFileName))

	// 前置状态确认：Open 成功、Check 失败——本测试针对的真实形态。
	ctx := context.Background()
	opened, err := storage.Open(dataDir)
	if err != nil {
		t.Fatalf("precondition: open corrupted db should succeed: %v", err)
	}
	if _, cerr := storage.Check(ctx, opened); cerr == nil {
		opened.Close()
		t.Fatal("precondition: Check on corrupted db should fail")
	}
	_ = opened.Close()

	stdout, stderr, err := runDB(t, dbInput{DataDir: dataDir, From: backupPath, Force: true})
	if err != nil {
		t.Fatalf("restore on openable-corrupted db = %v, want success", err)
	}
	_ = stdout
	if !strings.Contains(stderr, "corrupted") {
		t.Errorf("stderr %q missing corrupted-db warning", stderr)
	}

	// 恢复后的库健康且业务数据回来。
	restored, err := storage.Open(dataDir)
	if err != nil {
		t.Fatalf("reopen restored db: %v", err)
	}
	defer restored.Close()
	if _, err := storage.Check(ctx, restored); err != nil {
		t.Errorf("integrity check after restore: %v", err)
	}
	var count int
	if err := restored.QueryRow("SELECT count(*) FROM sync_jobs WHERE id = 'job_rt'").Scan(&count); err != nil || count != 1 {
		t.Errorf("job after corrupted-db restore = (%d, %v), want restored", count, err)
	}
}

// 当前库不可打开（损坏）时恢复语义不变：跳过 safety backup、告警并
// 继续恢复——restore 本来就是救灾入口。
func TestDBRestoreProceedsWhenCurrentDBDamaged(t *testing.T) {
	dataDir := t.TempDir()
	bootstrapDBDataDir(t, dataDir)
	backupPath := backupHealthyDB(t, dataDir)

	// 当前库整体替换为非 SQLite 垃圾字节并清掉 sidecar（遗留 WAL 会
	// 把内容恢复回来）：库不可打开。
	if err := os.WriteFile(filepath.Join(dataDir, storage.DatabaseFileName), []byte("definitely not a sqlite database"), 0o600); err != nil {
		t.Fatalf("corrupt db: %v", err)
	}
	for _, sidecar := range []string{"-wal", "-shm"} {
		if err := os.Remove(filepath.Join(dataDir, storage.DatabaseFileName+sidecar)); err != nil && !os.IsNotExist(err) {
			t.Fatalf("remove sidecar: %v", err)
		}
	}

	_, restoreStderr, err := runDB(t, dbInput{DataDir: dataDir, From: backupPath, Force: true})
	if err != nil {
		t.Fatalf("restore on damaged db: %v", err)
	}
	if !strings.Contains(restoreStderr, "current database unavailable") {
		t.Errorf("stderr %q missing damaged-db warning", restoreStderr)
	}

	// 恢复后的库健康且业务数据回来。
	restored, err := storage.Open(dataDir)
	if err != nil {
		t.Fatalf("reopen restored db: %v", err)
	}
	defer restored.Close()
	var count int
	if err := restored.QueryRow("SELECT count(*) FROM sync_jobs WHERE id = 'job_rt'").Scan(&count); err != nil || count != 1 {
		t.Errorf("job after damaged-db restore = (%d, %v), want restored", count, err)
	}
}

// 备份 → 破坏 → restore → 原数据回来：secret / 管理员凭据 / Job 全部
// 恢复，restore 自身产生 pre-restore safety backup。
func TestDBBackupRestoreRoundTrip(t *testing.T) {
	dataDir := t.TempDir()
	bootstrapDBDataDir(t, dataDir)
	ctx := context.Background()

	// backup。
	var stdout, stderr bytes.Buffer
	if err := execDBBackup(ctx, dbInput{DataDir: dataDir}, &stdout, &stderr); err != nil {
		t.Fatalf("db backup: %v", err)
	}
	backupPath := strings.TrimSpace(strings.TrimPrefix(stdout.String(), "backup written to "))
	if backupPath == "" || !strings.HasSuffix(backupPath, ".db") {
		t.Fatalf("cannot parse backup path from %q", stdout.String())
	}
	if _, err := os.Stat(backupPath); err != nil {
		t.Fatalf("backup file missing: %v", err)
	}
	// 备份权限收敛 0600（POSIX）。
	if info, err := os.Stat(backupPath); err == nil && !isWindows() {
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Errorf("backup perm = %o, want 600", perm)
		}
	}

	// 破坏当前库：删除业务数据（模拟误操作 / 损坏后的状态）。
	if _, err := storage.Open(dataDir); err == nil {
		// 已有 Cleanup 管理连接；这里仅确认可打开。
	}
	raw, err := sql.Open("sqlite", "file:"+filepath.Join(dataDir, storage.DatabaseFileName))
	if err != nil {
		t.Fatalf("open for mutation: %v", err)
	}
	if _, err := raw.Exec("DELETE FROM sync_jobs"); err != nil {
		t.Fatalf("delete jobs: %v", err)
	}
	if _, err := raw.Exec("DELETE FROM sources"); err != nil {
		t.Fatalf("delete sources: %v", err)
	}
	_ = raw.Close()

	// restore。
	out, _, err := runDB(t, dbInput{DataDir: dataDir, From: backupPath, Force: true})
	if err != nil {
		t.Fatalf("db restore: %v", err)
	}
	if !strings.Contains(out, "pre-restore safety backup") {
		t.Errorf("restore output %q missing safety backup line", out)
	}

	// 原数据回来。
	restored, err := storage.Open(dataDir)
	if err != nil {
		t.Fatalf("reopen restored db: %v", err)
	}
	defer restored.Close()
	var password string
	if err := restored.QueryRow("SELECT password FROM sources WHERE id = 'src_rt'").Scan(&password); err != nil {
		t.Fatalf("source not restored: %v", err)
	}
	if password != "S3cret-Roundtrip" {
		t.Errorf("restored source password = %q, want secret intact", password)
	}
	var count int
	if err := restored.QueryRow("SELECT count(*) FROM sync_jobs WHERE id = 'job_rt'").Scan(&count); err != nil || count != 1 {
		t.Fatalf("job not restored (count=%d err=%v)", count, err)
	}
	authSvc := auth.NewService(authsqlite.NewRepository(restored))
	if _, _, err := authSvc.Login(ctx, "restore-roundtrip-pass-123"); err != nil {
		t.Errorf("admin credential not restored: login failed: %v", err)
	}

	// pre-restore safety backup 存在且可检查。
	matches, err := filepath.Glob(filepath.Join(dataDir, "backups", "tinysync-prerestore-*.db"))
	if err != nil || len(matches) != 1 {
		t.Fatalf("prerestore backup glob = %v (%v), want exactly one", matches, err)
	}
	if _, err := storage.Inspect(ctx, matches[0]); err != nil {
		t.Errorf("prerestore backup integrity: %v", err)
	}
}

// 非法 SQLite 文件拒绝恢复：任何文件改动之前失败。
func TestDBRestoreRejectsInvalidSQLite(t *testing.T) {
	dataDir := t.TempDir()
	bootstrapDBDataDir(t, dataDir)

	garbage := filepath.Join(t.TempDir(), "garbage.db")
	if err := os.WriteFile(garbage, []byte("definitely not sqlite"), 0o600); err != nil {
		t.Fatalf("write garbage: %v", err)
	}
	_, _, err := runDB(t, dbInput{DataDir: dataDir, From: garbage, Force: true})
	if err == nil {
		t.Fatal("restore garbage = nil, want error")
	}
	// 原库未被改动。
	db, err := storage.Open(dataDir)
	if err != nil {
		t.Fatalf("open live db: %v", err)
	}
	defer db.Close()
	var count int
	if err := db.QueryRow("SELECT count(*) FROM sources WHERE id = 'src_rt'").Scan(&count); err != nil || count != 1 {
		t.Errorf("live db was modified by rejected restore (count=%d err=%v)", count, err)
	}
}

// 损坏的 SQLite 备份拒绝恢复（quick_check 失败），原库保持原样。
func TestDBRestoreRejectsCorruptedSQLite(t *testing.T) {
	dataDir := t.TempDir()
	bootstrapDBDataDir(t, dataDir)
	ctx := context.Background()

	corrupt := filepath.Join(t.TempDir(), "corrupt.db")
	db, err := storage.Open(dataDir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := storage.Backup(ctx, db, corrupt); err != nil {
		t.Fatalf("seed corrupt candidate: %v", err)
	}
	_ = db.Close()
	if err := corruptDBFile(corrupt); err != nil {
		t.Fatalf("corrupt file: %v", err)
	}

	_, _, err = runDB(t, dbInput{DataDir: dataDir, From: corrupt, Force: true})
	if err == nil {
		t.Fatal("restore corrupted backup = nil, want error")
	}
}

// schema 比 binary 新的备份拒绝恢复：不做 downgrade migration。
func TestDBRestoreRejectsNewerSchema(t *testing.T) {
	dataDir := t.TempDir()
	db := bootstrapDBDataDir(t, dataDir)
	ctx := context.Background()

	newer := filepath.Join(t.TempDir(), "newer.db")
	if _, err := db.Exec("PRAGMA user_version = 9999"); err != nil {
		t.Fatalf("bump version: %v", err)
	}
	if err := storage.Backup(ctx, db, newer); err != nil {
		t.Fatalf("backup newer: %v", err)
	}

	_, _, err := runDB(t, dbInput{DataDir: dataDir, From: newer, Force: true})
	if err == nil {
		t.Fatal("restore newer schema = nil, want error")
	}
	if !strings.Contains(err.Error(), "newer than supported") {
		t.Errorf("error %v does not mention version boundary", err)
	}
}

// schema 较旧的备份恢复成功：恢复后的库保持旧版本内容，消息说明
// 下次 serve 向前迁移（真实向前迁移由 storage 的升级测试链覆盖）。
func TestDBRestoreAcceptsOlderSchema(t *testing.T) {
	dataDir := t.TempDir()
	db := bootstrapDBDataDir(t, dataDir)
	ctx := context.Background()

	latest, err := storage.Check(ctx, db)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	older := filepath.Join(t.TempDir(), "older.db")
	if _, err := db.Exec("PRAGMA user_version = 1"); err != nil {
		t.Fatalf("downgrade version marker: %v", err)
	}
	if err := storage.Backup(ctx, db, older); err != nil {
		t.Fatalf("backup older: %v", err)
	}

	out, _, err := runDB(t, dbInput{DataDir: dataDir, From: older, Force: true})
	if err != nil {
		t.Fatalf("restore older schema: %v", err)
	}
	if !strings.Contains(out, "older than supported") || !strings.Contains(out, "migrates forward") {
		t.Errorf("restore output %q missing forward-migration notice", out)
	}
	// 数据仍然恢复。
	restored, err := storage.Open(dataDir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer restored.Close()
	var count int
	if err := restored.QueryRow("SELECT count(*) FROM sources WHERE id = 'src_rt'").Scan(&count); err != nil || count != 1 {
		t.Errorf("source not restored (count=%d err=%v)", count, err)
	}
	_ = latest
}

// 缺少 --force 时拒绝执行：破坏性操作必须显式确认。
func TestDBRestoreRequiresForce(t *testing.T) {
	dataDir := t.TempDir()
	bootstrapDBDataDir(t, dataDir)

	if _, _, err := runDB(t, dbInput{DataDir: dataDir, From: "whatever.db"}); err == nil {
		t.Fatal("restore without --force = nil, want error")
	}
}

// serve 持有 datadir lock 时 restore 被拒绝：单实例约束覆盖维护命令。
func TestDBRestoreRefusesWhenDatadirLocked(t *testing.T) {
	dataDir := t.TempDir()
	bootstrapDBDataDir(t, dataDir)

	backup := filepath.Join(t.TempDir(), "b.db")
	ctx := context.Background()
	db, err := storage.Open(dataDir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := storage.Backup(ctx, db, backup); err != nil {
		t.Fatalf("backup: %v", err)
	}
	_ = db.Close()

	// 模拟正在运行的 serve：持有锁。
	lock, err := instance.Acquire(dataDir)
	if err != nil {
		t.Fatalf("acquire lock: %v", err)
	}
	defer func() { _ = lock.Release() }()

	_, _, err = runDB(t, dbInput{DataDir: dataDir, From: backup, Force: true})
	if err == nil {
		t.Fatal("restore while locked = nil, want error")
	}
	if !strings.Contains(err.Error(), "owned by another tinysync process") {
		t.Errorf("error %v does not mention datadir ownership", err)
	}
}

// db check 在健康库上成功并输出报告。
func TestDBCheckHealthy(t *testing.T) {
	dataDir := t.TempDir()
	bootstrapDBDataDir(t, dataDir)

	var stdout, stderr bytes.Buffer
	if err := execDBCheck(context.Background(), dbInput{DataDir: dataDir}, &stdout, &stderr); err != nil {
		t.Fatalf("db check: %v", err)
	}
	if !strings.Contains(stdout.String(), "quick_check=ok") {
		t.Errorf("check output %q missing quick_check=ok", stdout.String())
	}
	if !strings.Contains(stdout.String(), "foreign_key_violations=0") {
		t.Errorf("check output %q missing fk count", stdout.String())
	}
}

func isWindows() bool {
	return os.PathSeparator == '\\'
}

// corruptDBFile 覆盖文件第二个数据页（默认页大小 4096 起）为 0xFF，
// 制造保留 header 的页级损坏（与 storage 包测试同一手法）。
func corruptDBFile(dbPath string) error {
	f, err := os.OpenFile(dbPath, os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if info.Size() < 8192 {
		return errTooSmallForCorruption
	}
	if _, err := f.WriteAt(make([]byte, 512), 4096); err != nil {
		return err
	}
	return f.Sync()
}

var errTooSmallForCorruption = errFixed("database smaller than two pages")

type errFixed string

func (e errFixed) Error() string { return string(e) }

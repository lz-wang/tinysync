package storage

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"testing/fstest"
)

// openMigrated 在临时目录打开并迁移一个数据库，返回连接。
func openMigrated(t *testing.T, dataDir string) *sql.DB {
	t.Helper()
	db, err := Open(dataDir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := Migrate(context.Background(), db, dataDir); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	return db
}

// probeMigrations 是测试用的两步 migration：v1 建表，v2 加列。
func probeMigrations() fstest.MapFS {
	return fstest.MapFS{
		"migrations/0001_base.sql": &fstest.MapFile{
			Data: []byte("CREATE TABLE probe (id INTEGER PRIMARY KEY, val TEXT);"),
		},
		"migrations/0002_more.sql": &fstest.MapFile{
			Data: []byte("ALTER TABLE probe ADD COLUMN extra TEXT;"),
		},
	}
}

// probeMigrationsV1 只含 v1：把数据库停在旧版本，供升级路径测试。
func probeMigrationsV1() fstest.MapFS {
	return fstest.MapFS{
		"migrations/0001_base.sql": &fstest.MapFile{
			Data: []byte("CREATE TABLE probe (id INTEGER PRIMARY KEY, val TEXT);"),
		},
	}
}

// 健康数据库通过 Check：quick_check ok、无外键违规、版本与内嵌一致。
func TestCheckHealthyDatabase(t *testing.T) {
	db := openMigrated(t, t.TempDir())

	report, err := Check(context.Background(), db)
	if err != nil {
		t.Fatalf("Check healthy db: %v", err)
	}
	if report.QuickCheck != "ok" {
		t.Errorf("quick_check = %q, want ok", report.QuickCheck)
	}
	if report.ForeignKeyViolations != 0 {
		t.Errorf("fk violations = %d, want 0", report.ForeignKeyViolations)
	}
	if report.UserVersion != report.LatestVersion {
		t.Errorf("user_version = %d, latest = %d, want equal", report.UserVersion, report.LatestVersion)
	}
}

// 存在外键违规时 Check 失败：不静默忽略参照完整性破坏。
func TestCheckFailsOnForeignKeyViolation(t *testing.T) {
	dataDir := t.TempDir()
	db := openMigrated(t, dataDir)

	// 用独立连接（不带 foreign_keys PRAGMA，SQLite 默认关闭）注入
	// 孤儿 Job，模拟外部破坏或历史缺陷留下的违规数据。
	raw, err := sql.Open("sqlite", "file:"+filepath.Join(dataDir, DatabaseFileName))
	if err != nil {
		t.Fatalf("open raw: %v", err)
	}
	defer raw.Close()
	if _, err := raw.Exec(`INSERT INTO sync_jobs (id, name, source_id, remote_root, local_root, mode, enabled, created_at, updated_at)
		VALUES ('job_x', 'orphan', 'src_missing', '/', '/tmp/x', 'copy', 1, 1, 1)`); err != nil {
		t.Fatalf("insert orphan job: %v", err)
	}

	_, err = Check(context.Background(), db)
	if err == nil {
		t.Fatal("Check with fk violation = nil, want error")
	}
	if !strings.Contains(err.Error(), "foreign key violation") {
		t.Errorf("error %v does not mention foreign key violation", err)
	}
}

// schema 版本高于本二进制支持时 Check 失败：提示升级二进制，不做
// downgrade 假设。
func TestCheckFailsOnNewerSchema(t *testing.T) {
	db := openMigrated(t, t.TempDir())

	if _, err := db.Exec("PRAGMA user_version = 9999"); err != nil {
		t.Fatalf("bump user_version: %v", err)
	}
	_, err := Check(context.Background(), db)
	if err == nil {
		t.Fatal("Check newer schema = nil, want error")
	}
	if !strings.Contains(err.Error(), "newer than supported") {
		t.Errorf("error %v does not mention version boundary", err)
	}
}

// Inspect 以只读方式检查数据库文件：健康库通过；垃圾 / 缺失文件失败；
// 检查不产生 -wal / -shm 副产物（只读打开不转换 journal mode）。
func TestInspect(t *testing.T) {
	dataDir := t.TempDir()
	dbPath := filepath.Join(dataDir, DatabaseFileName)
	db := openMigrated(t, dataDir)

	// 用 Backup 生成的单文件快照（无 WAL sidecar）做 Inspect 副本，
	// 与 WAL 模式打开的主库 sidecar 隔离。
	standalone := filepath.Join(t.TempDir(), "standalone.db")
	if err := Backup(context.Background(), db, standalone); err != nil {
		t.Fatalf("Backup standalone copy: %v", err)
	}

	report, err := Inspect(context.Background(), standalone)
	if err != nil {
		t.Fatalf("Inspect standalone db: %v", err)
	}
	if report.QuickCheck != "ok" {
		t.Errorf("quick_check = %q, want ok", report.QuickCheck)
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		if _, err := os.Stat(standalone + suffix); err == nil {
			t.Errorf("Inspect left sidecar %s%s behind", standalone, suffix)
		}
	}

	// Inspect 也能直接检查运行中的主库文件（WAL sidecar 属正常状态）。
	if _, err := Inspect(context.Background(), dbPath); err != nil {
		t.Fatalf("Inspect live db file: %v", err)
	}

	garbage := filepath.Join(t.TempDir(), "garbage.db")
	if err := os.WriteFile(garbage, []byte("this is not a sqlite database at all"), 0o600); err != nil {
		t.Fatalf("write garbage: %v", err)
	}
	if _, err := Inspect(context.Background(), garbage); err == nil {
		t.Fatal("Inspect garbage file = nil, want error")
	}

	if _, err := Inspect(context.Background(), filepath.Join(t.TempDir(), "missing.db")); err == nil {
		t.Fatal("Inspect missing file = nil, want error")
	}
}

// Backup 生成可打开、内容一致的一致性快照；POSIX 下权限 0600。
func TestBackupRoundTrip(t *testing.T) {
	dataDir := t.TempDir()
	db := openMigrated(t, dataDir)
	ctx := context.Background()
	if _, err := db.Exec(`INSERT INTO sources
		(id, name, type, endpoint, username, password, enabled, created_at, updated_at)
		VALUES ('src_backup', 'backup-probe', 'webdav', 'https://example.com', '', 'secret', 1, 1, 1)`); err != nil {
		t.Fatalf("seed source: %v", err)
	}

	target := filepath.Join(t.TempDir(), "manual-backup.db")
	if err := Backup(ctx, db, target); err != nil {
		t.Fatalf("Backup: %v", err)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(target)
		if err != nil {
			t.Fatalf("stat backup: %v", err)
		}
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Errorf("backup perm = %o, want 600", perm)
		}
	}

	report, err := Inspect(ctx, target)
	if err != nil {
		t.Fatalf("Inspect backup: %v", err)
	}
	if report.QuickCheck != "ok" || report.ForeignKeyViolations != 0 {
		t.Fatalf("backup integrity = %+v, want clean", report)
	}
	// 备份内容与源一致：seed 的行随快照存在。
	restored, err := sql.Open("sqlite", BuildDSN(target))
	if err != nil {
		t.Fatalf("open backup: %v", err)
	}
	defer restored.Close()
	var name string
	if err := restored.QueryRow("SELECT name FROM sources WHERE id = 'src_backup'").Scan(&name); err != nil {
		t.Fatalf("query backup content: %v", err)
	}
	if name != "backup-probe" {
		t.Errorf("backup content name = %q, want backup-probe", name)
	}
}

// corruption 守卫：migration 前检测到损坏的数据库必须失败，不得
// 继续迁移（不在坏数据上继续写 schema 版本）。
func TestMigrateRefusesCorruptedDatabase(t *testing.T) {
	dataDir := t.TempDir()

	db, err := Open(dataDir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := migrate(context.Background(), db, dataDir, probeMigrationsV1()); err != nil {
		t.Fatalf("migrate to v1: %v", err)
	}
	assertVersion(t, db, 1)

	// 关闭连接后破坏文件中部的数据页（保留 header，保证仍可打开）。
	if err := db.Close(); err != nil {
		t.Fatalf("close before corruption: %v", err)
	}
	dbPath := filepath.Join(dataDir, DatabaseFileName)
	if err := corruptMiddlePage(dbPath); err != nil {
		t.Fatalf("corrupt db: %v", err)
	}

	reopened, err := Open(dataDir)
	if err != nil {
		// 打开即失败同样满足「不继续迁移」。
		return
	}
	defer reopened.Close()
	if err := migrate(context.Background(), reopened, dataDir, probeMigrations()); err == nil {
		t.Fatal("migrate corrupted db = nil, want integrity failure before migration")
	}
	// 版本必须保持 v1：损坏的库没有继续前进。
	assertVersion(t, reopened, 1)
}

// corruptMiddlePage 覆盖文件第二个数据页（offset = 默认页大小 4096
// 起）为 0xFF，制造保留 header 的页级损坏。文件不足两页时报错。
func corruptMiddlePage(dbPath string) error {
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
		return errDatabaseTooSmall
	}
	if _, err := f.WriteAt(make([]byte, 512), 4096); err != nil {
		return err
	}
	return f.Sync()
}

// errDatabaseTooSmall 标记文件小于两页、无法安全构造页级损坏。
var errDatabaseTooSmall = errorString("database smaller than two pages")

type errorString string

func (e errorString) Error() string { return string(e) }

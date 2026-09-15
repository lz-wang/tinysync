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

// assertVersion 断言数据库当前 schema 版本。
func assertVersion(t *testing.T, db *sql.DB, want int) {
	t.Helper()
	got, err := CurrentVersion(context.Background(), db)
	if err != nil {
		t.Fatalf("read version: %v", err)
	}
	if got != want {
		t.Fatalf("schema version = %d, want %d", got, want)
	}
}

// sourceCount 统计 sources 表行数（表不存在时报错）。
func sourceCount(t *testing.T, db *sql.DB) int {
	t.Helper()
	var count int
	if err := db.QueryRow("SELECT count(*) FROM sources").Scan(&count); err != nil {
		t.Fatalf("query sources: %v", err)
	}
	return count
}

// 全新数据库自动初始化到最新版本，sources 表可用且约束生效。
func TestMigrateFreshDatabase(t *testing.T) {
	dataDir := t.TempDir()
	db, err := Open(dataDir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	ctx := context.Background()
	if err := Migrate(ctx, db, dataDir); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	assertVersion(t, db, 1)

	insert := func(id, name string) error {
		_, err := db.Exec(`INSERT INTO sources
			(id, name, type, endpoint, username, password, enabled, created_at, updated_at)
			VALUES (?, ?, 'webdav', 'https://example.com', '', '', 1, 1, 1)`, id, name)
		return err
	}
	if err := insert("src_a", "nas"); err != nil {
		t.Fatalf("insert source: %v", err)
	}
	// name COLLATE NOCASE UNIQUE：仅大小写不同也算重复。
	if err := insert("src_b", "NAS"); err == nil {
		t.Fatal("insert duplicate name (case-insensitive) = nil, want error")
	}
	if _, err := db.Exec(`INSERT INTO sources
		(id, name, type, endpoint, username, password, enabled, created_at, updated_at)
		VALUES ('src_c', 'other', 'webdav', 'https://example.com', '', '', 7, 1, 1)`); err == nil {
		t.Fatal("insert enabled=7 = nil, want CHECK violation")
	}

	// 全新库升级不产生空备份。
	if _, err := os.Stat(filepath.Join(dataDir, backupsDirName)); !os.IsNotExist(err) {
		t.Errorf("backups dir should not exist for fresh database, stat err = %v", err)
	}
}

// 重复启动幂等：第二次 Migrate 不重复执行、不产生备份。
func TestMigrateIdempotent(t *testing.T) {
	dataDir := t.TempDir()
	db, err := Open(dataDir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	ctx := context.Background()
	if err := Migrate(ctx, db, dataDir); err != nil {
		t.Fatalf("first Migrate: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO sources
		(id, name, type, endpoint, username, password, enabled, created_at, updated_at)
		VALUES ('src_a', 'nas', 'webdav', 'https://example.com', '', '', 1, 1, 1)`); err != nil {
		t.Fatalf("insert source: %v", err)
	}

	if err := Migrate(ctx, db, dataDir); err != nil {
		t.Fatalf("second Migrate: %v", err)
	}
	assertVersion(t, db, 1)
	if got := sourceCount(t, db); got != 1 {
		t.Errorf("sources rows = %d, want 1", got)
	}
	if _, err := os.Stat(filepath.Join(dataDir, backupsDirName)); !os.IsNotExist(err) {
		t.Errorf("backups dir should not exist without upgrade, stat err = %v", err)
	}
}

// 旧库向前迁移：既有业务表保留，升级前生成一致性备份且备份可打开。
func TestMigrateLegacyDatabaseCreatesBackup(t *testing.T) {
	dataDir := t.TempDir()
	db, err := Open(dataDir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	ctx := context.Background()
	// 模拟版本化之前的旧库：已有业务数据，user_version 保持 0。
	if _, err := db.Exec("CREATE TABLE legacy_data (value TEXT NOT NULL)"); err != nil {
		t.Fatalf("create legacy table: %v", err)
	}
	if _, err := db.Exec("INSERT INTO legacy_data (value) VALUES ('old')"); err != nil {
		t.Fatalf("insert legacy row: %v", err)
	}

	if err := Migrate(ctx, db, dataDir); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	assertVersion(t, db, 1)

	var legacy string
	if err := db.QueryRow("SELECT value FROM legacy_data").Scan(&legacy); err != nil {
		t.Fatalf("query legacy data after migrate: %v", err)
	}
	if legacy != "old" {
		t.Errorf("legacy value = %q, want old", legacy)
	}
	if got := sourceCount(t, db); got != 0 {
		t.Errorf("sources rows = %d, want 0", got)
	}

	// 备份文件存在、权限收敛、可独立打开且包含升级前数据。
	entries, err := os.ReadDir(filepath.Join(dataDir, backupsDirName))
	if err != nil {
		t.Fatalf("read backups dir: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("backup files = %d, want 1", len(entries))
	}
	backupPath := filepath.Join(dataDir, backupsDirName, entries[0].Name())
	if !strings.HasPrefix(entries[0].Name(), "tinysync-v0-") {
		t.Errorf("backup name = %q, want tinysync-v0- prefix", entries[0].Name())
	}
	if info, err := os.Stat(backupPath); err != nil {
		t.Fatalf("stat backup: %v", err)
	} else if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Errorf("backup perm = %o, want 600", info.Mode().Perm())
	}

	backupDB, err := sql.Open("sqlite", backupPath)
	if err != nil {
		t.Fatalf("open backup: %v", err)
	}
	defer backupDB.Close()
	var value string
	if err := backupDB.QueryRow("SELECT value FROM legacy_data").Scan(&value); err != nil {
		t.Fatalf("query backup: %v", err)
	}
	if value != "old" {
		t.Errorf("backup value = %q, want old", value)
	}
}

// 更高版本的数据库拒绝启动，且不改动任何数据。
func TestMigrateRejectsNewerDatabase(t *testing.T) {
	dataDir := t.TempDir()
	db, err := Open(dataDir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	if _, err := db.Exec("PRAGMA user_version = 99"); err != nil {
		t.Fatalf("set user_version: %v", err)
	}

	err = Migrate(context.Background(), db, dataDir)
	if err == nil {
		t.Fatal("Migrate on newer database = nil, want error")
	}
	if !strings.Contains(err.Error(), "newer") {
		t.Errorf("error = %v, want mention newer schema", err)
	}
	assertVersion(t, db, 99)
}

// migration 逐版本原子：前一个成功版本保持提交，坏版本回滚。
func TestMigrateRollsBackBrokenMigration(t *testing.T) {
	fsys := fstest.MapFS{
		"migrations/0001_good.sql": &fstest.MapFile{Data: []byte("CREATE TABLE good (v INTEGER NOT NULL);")},
		"migrations/0002_bad.sql":  &fstest.MapFile{Data: []byte("CREATE TABLE bad (v INTEGER NOT NULL); THIS IS NOT SQL;")},
	}
	dataDir := t.TempDir()
	db, err := Open(dataDir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	if err := migrate(context.Background(), db, dataDir, fsys); err == nil {
		t.Fatal("migrate with broken SQL = nil, want error")
	}
	// 0001 已原子提交，0002 回滚。
	assertVersion(t, db, 1)
	if _, err := db.Exec("SELECT count(*) FROM good"); err != nil {
		t.Errorf("table from committed migration missing: %v", err)
	}
	if _, err := db.Exec("SELECT count(*) FROM bad"); err == nil {
		t.Error("table from rolled-back migration exists")
	}
}

// 首个 migration 自身失败时整体回滚到版本 0。
func TestMigrateRollsBackFirstBrokenMigration(t *testing.T) {
	fsys := fstest.MapFS{
		"migrations/0001_bad.sql": &fstest.MapFile{Data: []byte("THIS IS NOT SQL;")},
	}
	dataDir := t.TempDir()
	db, err := Open(dataDir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	if err := migrate(context.Background(), db, dataDir, fsys); err == nil {
		t.Fatal("migrate with broken first migration = nil, want error")
	}
	assertVersion(t, db, 0)
	var count int
	if err := db.QueryRow(
		"SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%'",
	).Scan(&count); err != nil {
		t.Fatalf("inspect sqlite_master: %v", err)
	}
	if count != 0 {
		t.Errorf("user tables after rollback = %d, want 0", count)
	}
}

// 同一秒内连续两次迁移备份必须生成不同文件，避免失败重试时
// VACUUM INTO 因目标已存在而冲突。
func TestBackupDatabaseUniqueNamesSameSecond(t *testing.T) {
	dataDir := t.TempDir()
	db, err := Open(dataDir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	if _, err := db.Exec("CREATE TABLE legacy_data (value TEXT NOT NULL)"); err != nil {
		t.Fatalf("create table: %v", err)
	}

	if err := backupDatabase(context.Background(), db, dataDir, 1); err != nil {
		t.Fatalf("first backup: %v", err)
	}
	if err := backupDatabase(context.Background(), db, dataDir, 1); err != nil {
		t.Fatalf("second backup (same second): %v", err)
	}

	entries, err := os.ReadDir(filepath.Join(dataDir, backupsDirName))
	if err != nil {
		t.Fatalf("read backups dir: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("backup files = %d, want 2", len(entries))
	}
	if entries[0].Name() == entries[1].Name() {
		t.Errorf("backup names collide: %q", entries[0].Name())
	}
}

// 文件名不符合 NNNN_<desc>.sql 约定时直接报错，不猜测执行顺序。
func TestLoadMigrationsRejectsBadNames(t *testing.T) {
	fsys := fstest.MapFS{
		"migrations/noversion.sql": &fstest.MapFile{Data: []byte("SELECT 1;")},
	}
	if _, err := loadMigrations(fsys); err == nil {
		t.Fatal("loadMigrations with bad name = nil, want error")
	}

	dup := fstest.MapFS{
		"migrations/0001_a.sql": &fstest.MapFile{Data: []byte("SELECT 1;")},
		"migrations/0001_b.sql": &fstest.MapFile{Data: []byte("SELECT 2;")},
	}
	if _, err := loadMigrations(dup); err == nil {
		t.Fatal("loadMigrations with duplicate version = nil, want error")
	}
}

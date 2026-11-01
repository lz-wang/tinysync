package storage

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// expectedTables 是全新初始化必须创建的业务表全集。
var expectedTables = []string{
	"sources",
	"credentials",
	"jobs",
	"job_runs",
	"job_run_items",
	"managed_files",
	"registries",
	"admin_credentials",
	"web_sessions",
	"api_tokens",
	"shares",
	"notification_settings",
}

// openFresh 打开一个全新数据目录的数据库（未初始化）。
func openFresh(t *testing.T, dataDir string) *sql.DB {
	t.Helper()
	db, err := Open(dataDir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// tableExists 判断业务表是否已创建。
func tableExists(t *testing.T, db *sql.DB, name string) bool {
	t.Helper()
	var count int
	if err := db.QueryRow(
		"SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = ?", name,
	).Scan(&count); err != nil {
		t.Fatalf("query sqlite_master for %s: %v", name, err)
	}
	return count == 1
}

// 全新数据目录首次启动即完成数据库初始化：schema.sql 全部业务表与
// user_version 原子生效，幂等可重复调用。
func TestEnsureInitializesFreshDatabase(t *testing.T) {
	db := openFresh(t, t.TempDir())

	if err := Ensure(context.Background(), db); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	assertVersion(t, db, SchemaVersion)
	for _, name := range expectedTables {
		if !tableExists(t, db, name) {
			t.Errorf("table %s missing after Ensure", name)
		}
	}
	// 幂等：已初始化的库再次 Ensure 直接成功，不重复执行 schema。
	if err := Ensure(context.Background(), db); err != nil {
		t.Fatalf("Ensure twice: %v", err)
	}
	assertVersion(t, db, SchemaVersion)
}

// 空文件（零字节、无业务表、version 0）视同全新库，完成初始化。
func TestEnsureInitializesEmptyFile(t *testing.T) {
	dataDir := t.TempDir()
	dbPath := filepath.Join(dataDir, DatabaseFileName)
	if err := writeEmptyFile(dbPath); err != nil {
		t.Fatalf("create empty file: %v", err)
	}
	db := openFresh(t, dataDir)
	if err := Ensure(context.Background(), db); err != nil {
		t.Fatalf("Ensure on empty file: %v", err)
	}
	if !tableExists(t, db, "jobs") {
		t.Error("jobs table missing after Ensure on empty file")
	}
}

// 旧版（破坏性变更前）数据库必须明确报错，不自动删除、不尝试迁移。
func TestEnsureRejectsLegacyDatabase(t *testing.T) {
	dataDir := t.TempDir()
	db := openFresh(t, dataDir)
	// 构造一个旧序列版本（< SchemaVersion）的库：有业务表、版本号落后。
	if _, err := db.Exec("CREATE TABLE legacy_probe (id TEXT PRIMARY KEY)"); err != nil {
		t.Fatalf("seed legacy table: %v", err)
	}
	if _, err := db.Exec("PRAGMA user_version = 13"); err != nil {
		t.Fatalf("set legacy version: %v", err)
	}
	err := Ensure(context.Background(), db)
	if !errors.Is(err, ErrSchemaIncompatible) {
		t.Fatalf("Ensure on legacy db err = %v, want ErrSchemaIncompatible", err)
	}
	assertVersion(t, db, 13)
}

// 更高版本（未来二进制创建）同样拒绝，方向语义留给 ErrNewerSchema。
func TestEnsureRejectsNewerDatabase(t *testing.T) {
	db := openFresh(t, t.TempDir())
	if _, err := db.Exec("PRAGMA user_version = 15"); err != nil {
		t.Fatalf("set newer version: %v", err)
	}
	err := Ensure(context.Background(), db)
	if !errors.Is(err, ErrSchemaIncompatible) {
		t.Fatalf("Ensure on newer db err = %v, want ErrSchemaIncompatible", err)
	}
}

// version 0 但已有业务表（外来 SQLite 文件）拒绝初始化，不覆盖。
func TestEnsureRejectsForeignDatabase(t *testing.T) {
	db := openFresh(t, t.TempDir())
	if _, err := db.Exec("CREATE TABLE foreign_probe (id TEXT PRIMARY KEY)"); err != nil {
		t.Fatalf("seed foreign table: %v", err)
	}
	err := Ensure(context.Background(), db)
	if !errors.Is(err, ErrSchemaIncompatible) {
		t.Fatalf("Ensure on foreign db err = %v, want ErrSchemaIncompatible", err)
	}
	if !tableExists(t, db, "foreign_probe") {
		t.Error("foreign table must not be dropped by Ensure")
	}
}

// writeEmptyFile 创建一个零字节文件，模拟 Open 前已存在的空数据库文件。
func writeEmptyFile(path string) error {
	return os.WriteFile(path, []byte{}, 0o600)
}

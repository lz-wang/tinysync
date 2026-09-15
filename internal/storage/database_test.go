package storage

import (
	"database/sql"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// assertPragmaInt 断言整型 PRAGMA 当前值。
func assertPragmaInt(t *testing.T, db *sql.DB, pragma string, want int) {
	t.Helper()
	var got int
	if err := db.QueryRow(pragma).Scan(&got); err != nil {
		t.Fatalf("query %s: %v", pragma, err)
	}
	if got != want {
		t.Errorf("%s = %d, want %d", pragma, got, want)
	}
}

// Open 在临时目录创建数据库，DSN PRAGMA 基线全部生效。
func TestOpenCreatesDatabaseWithPragmas(t *testing.T) {
	db, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	assertPragmaInt(t, db, "PRAGMA foreign_keys", 1)
	assertPragmaInt(t, db, "PRAGMA busy_timeout", busyTimeoutMS)
	assertPragmaInt(t, db, "PRAGMA synchronous", 1) // NORMAL

	var journalMode string
	if err := db.QueryRow("PRAGMA journal_mode").Scan(&journalMode); err != nil {
		t.Fatalf("query journal_mode: %v", err)
	}
	if journalMode != "wal" {
		t.Errorf("journal_mode = %q, want wal", journalMode)
	}
}

// Open 收敛数据库文件权限为 0600（仅 POSIX 平台可校验），
// 并配置单连接池。
func TestOpenFilePermissionsAndPool(t *testing.T) {
	dataDir := t.TempDir()
	db, err := Open(dataDir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	if got := db.Stats().MaxOpenConnections; got != 1 {
		t.Errorf("MaxOpenConnections = %d, want 1", got)
	}
	if runtime.GOOS == "windows" {
		return
	}
	info, err := os.Stat(filepath.Join(dataDir, DatabaseFileName))
	if err != nil {
		t.Fatalf("stat database file: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("database file perm = %o, want 600", perm)
	}
}

// Open 对不存在的数据目录逐级自动创建。
func TestOpenCreatesMissingDataDir(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "nested", "data")
	db, err := Open(dataDir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	if info, err := os.Stat(dataDir); err != nil || !info.IsDir() {
		t.Fatalf("datadir %s not created: %v", dataDir, err)
	}
}

// Open 对已有数据库可重复打开，先前写入的数据保持可读。
func TestOpenReusesExistingDatabase(t *testing.T) {
	dataDir := t.TempDir()

	db, err := Open(dataDir)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	if _, err := db.Exec("CREATE TABLE reopen_marker (value TEXT NOT NULL)"); err != nil {
		t.Fatalf("create table: %v", err)
	}
	if _, err := db.Exec("INSERT INTO reopen_marker (value) VALUES ('kept')"); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	db2, err := Open(dataDir)
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	defer db2.Close()

	var value string
	if err := db2.QueryRow("SELECT value FROM reopen_marker").Scan(&value); err != nil {
		t.Fatalf("query after reopen: %v", err)
	}
	if value != "kept" {
		t.Errorf("value after reopen = %q, want kept", value)
	}
}

// BuildDSN 输出 file: URI 并附带完整 PRAGMA 基线。
func TestBuildDSNAppendsPragmas(t *testing.T) {
	dsn := BuildDSN(filepath.Join("data", DatabaseFileName))
	want := "file:data/" + DatabaseFileName + "?" + pragmaQuery
	if dsn != want {
		t.Errorf("BuildDSN = %q, want %q", dsn, want)
	}
	abs := BuildDSN(filepath.Join(string(filepath.Separator), "tmp", "data", DatabaseFileName))
	if abs != "file:///tmp/data/"+DatabaseFileName+"?"+pragmaQuery {
		t.Errorf("BuildDSN absolute = %q, want file:/// URI", abs)
	}
}

// 数据目录包含 ?、#、% 与空格等特殊字符时，DSN 必须仍能打开并读写。
// ? 在 Windows 文件名中非法，该平台跳过。
func TestOpenHandlesSpecialCharacterDataDir(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("? is not a legal filename character on windows")
	}
	dataDir := filepath.Join(t.TempDir(), "weird ?#% dir")
	db, err := Open(dataDir)
	if err != nil {
		t.Fatalf("Open special-char datadir: %v", err)
	}
	defer db.Close()

	if _, err := db.Exec("CREATE TABLE marker (value TEXT NOT NULL)"); err != nil {
		t.Fatalf("create table: %v", err)
	}
	if _, err := db.Exec("INSERT INTO marker (value) VALUES ('ok')"); err != nil {
		t.Fatalf("insert: %v", err)
	}
	var value string
	if err := db.QueryRow("SELECT value FROM marker").Scan(&value); err != nil {
		t.Fatalf("query: %v", err)
	}
	if value != "ok" {
		t.Errorf("value = %q, want ok", value)
	}
}

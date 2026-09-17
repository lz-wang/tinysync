package storage

import (
	"context"
	"database/sql"
	"io/fs"
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

// embeddedLatestVersion 返回内嵌 migration 的最新版本号，
// 供「升级到最新版本」类断言使用，避免随新 migration 硬编码失效。
func embeddedLatestVersion(t *testing.T) int {
	t.Helper()
	migrations, err := loadMigrations(migrationFS)
	if err != nil {
		t.Fatalf("load migrations: %v", err)
	}
	return migrations[len(migrations)-1].version
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
	assertVersion(t, db, embeddedLatestVersion(t))

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
	assertVersion(t, db, embeddedLatestVersion(t))
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
	assertVersion(t, db, embeddedLatestVersion(t))

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

// 真实 v1 起步升级回归：v0.2.0 数据库（含 Source 与密码）迁移到最新版本后
// 数据完整保留、被引用 Source 禁删、备份可重开且停留在 v1、sync_jobs 可用。
func TestMigrateFromV1PreservesSources(t *testing.T) {
	dataDir := t.TempDir()
	db, err := Open(dataDir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	ctx := context.Background()
	// 用真实的 0001 schema 先构造 v0.2.0 形态的库。
	v1SQL, err := fs.ReadFile(migrationFS, "migrations/0001_sources.sql")
	if err != nil {
		t.Fatalf("read embedded v1 schema: %v", err)
	}
	v1FS := fstest.MapFS{
		"migrations/0001_sources.sql": &fstest.MapFile{Data: v1SQL},
	}
	if err := migrate(ctx, db, dataDir, v1FS); err != nil {
		t.Fatalf("build v1 database: %v", err)
	}
	assertVersion(t, db, 1)
	if _, err := db.Exec(`INSERT INTO sources
		(id, name, type, endpoint, username, password, enabled, created_at, updated_at)
		VALUES ('src_a', 'nas', 'webdav', 'https://example.com/dav/', 'user', 'secret', 1, 1, 1)`); err != nil {
		t.Fatalf("insert source: %v", err)
	}

	// 真实迁移到最新版本。
	if err := Migrate(ctx, db, dataDir); err != nil {
		t.Fatalf("Migrate from v1: %v", err)
	}
	assertVersion(t, db, embeddedLatestVersion(t))

	// Source 完整保留：密码随 0005 backfill 移入 credentials_json，
	// legacy password 列清空为 tombstone。
	var name, password, credsJSON string
	if err := db.QueryRow(
		"SELECT name, password, credentials_json FROM sources WHERE id = 'src_a'",
	).Scan(&name, &password, &credsJSON); err != nil {
		t.Fatalf("query source after migrate: %v", err)
	}
	if name != "nas" {
		t.Errorf("source name after migrate = %q, want nas", name)
	}
	if credsJSON != `{"password":"secret"}` {
		t.Errorf("credentials_json after migrate = %s, want backfilled secret", credsJSON)
	}
	if password != "" {
		t.Errorf("legacy password after migrate = %q, want cleared", password)
	}

	// sync_jobs 可创建，且受 FK RESTRICT 保护：被引用的 Source 禁删。
	if _, err := db.Exec(`INSERT INTO sync_jobs
		(id, name, source_id, remote_root, local_root, mode,
		 include_patterns, exclude_patterns, enabled, created_at, updated_at)
		VALUES ('job_a', 'photos', 'src_a', '/photos', '/tmp/backup', 'mirror',
		 '[]', '[]', 1, 1, 1)`); err != nil {
		t.Fatalf("insert sync job: %v", err)
	}
	if _, err := db.Exec("DELETE FROM sources WHERE id = 'src_a'"); err == nil {
		t.Fatal("delete referenced source = nil, want FK RESTRICT error")
	}

	// managed_files 可创建，受 UNIQUE(job_id, local_rel_path) 与 CASCADE 保护。
	insertManaged := func(remotePath, relPath string) error {
		_, err := db.Exec(`INSERT INTO managed_files
			(job_id, remote_path, local_rel_path, state, remote_size,
			 remote_mtime_ns, remote_etag, remote_checksum, remote_version,
			 local_size, local_mtime_ns, updated_at)
			VALUES ('job_a', ?, ?, 'synced', 10, 1, '', '', '', 10, 1, 1)`,
			remotePath, relPath)
		return err
	}
	if err := insertManaged("/photos/a.jpg", "photos/a.jpg"); err != nil {
		t.Fatalf("insert managed file: %v", err)
	}
	if err := insertManaged("/photos/b.jpg", "photos/a.jpg"); err == nil {
		t.Fatal("insert duplicate local_rel_path = nil, want UNIQUE violation")
	}
	if _, err := db.Exec("DELETE FROM sync_jobs WHERE id = 'job_a'"); err != nil {
		t.Fatalf("delete sync job: %v", err)
	}
	var managed int
	if err := db.QueryRow("SELECT count(*) FROM managed_files").Scan(&managed); err != nil {
		t.Fatalf("count managed files: %v", err)
	}
	if managed != 0 {
		t.Errorf("managed rows after job delete = %d, want 0 (CASCADE)", managed)
	}

	// 升级备份存在、可重开、停留在 v1 且含迁移前数据。
	entries, err := os.ReadDir(filepath.Join(dataDir, backupsDirName))
	if err != nil {
		t.Fatalf("read backups dir: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("backup files = %d, want 1", len(entries))
	}
	if !strings.HasPrefix(entries[0].Name(), "tinysync-v1-") {
		t.Errorf("backup name = %q, want tinysync-v1- prefix", entries[0].Name())
	}
	backupDB, err := sql.Open("sqlite", filepath.Join(dataDir, backupsDirName, entries[0].Name()))
	if err != nil {
		t.Fatalf("open backup: %v", err)
	}
	defer backupDB.Close()
	assertVersion(t, backupDB, 1)
	if got := sourceCount(t, backupDB); got != 1 {
		t.Errorf("backup sources rows = %d, want 1", got)
	}
	if _, err := backupDB.Query("SELECT count(*) FROM sync_jobs"); err == nil {
		t.Error("backup should not contain sync_jobs table")
	}
}

// 真实 v2→v3 升级回归：v0.3.0 数据库（Source + Job + managed）迁移后数据
// 完整保留、既有 Job 自动 manual、备份可重开且停留在 v2；sync_runs /
// sync_run_items 可用，CHECK 约束生效，删除 Job 级联清理运行历史。
func TestMigrateV2ToV3AddsScheduleAndRuns(t *testing.T) {
	dataDir := t.TempDir()
	db, err := Open(dataDir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	ctx := context.Background()
	// 用真实的 0001 + 0002 schema 构造 v0.3.0 形态的库。
	v1SQL, err := fs.ReadFile(migrationFS, "migrations/0001_sources.sql")
	if err != nil {
		t.Fatalf("read embedded v1 schema: %v", err)
	}
	v2SQL, err := fs.ReadFile(migrationFS, "migrations/0002_sync_jobs.sql")
	if err != nil {
		t.Fatalf("read embedded v2 schema: %v", err)
	}
	v2FS := fstest.MapFS{
		"migrations/0001_sources.sql":   &fstest.MapFile{Data: v1SQL},
		"migrations/0002_sync_jobs.sql": &fstest.MapFile{Data: v2SQL},
	}
	if err := migrate(ctx, db, dataDir, v2FS); err != nil {
		t.Fatalf("build v2 database: %v", err)
	}
	assertVersion(t, db, 2)
	if _, err := db.Exec(`INSERT INTO sources
		(id, name, type, endpoint, username, password, enabled, created_at, updated_at)
		VALUES ('src_a', 'nas', 'webdav', 'https://example.com/dav/', 'user', 'secret', 1, 1, 1)`); err != nil {
		t.Fatalf("insert source: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO sync_jobs
		(id, name, source_id, remote_root, local_root, mode,
		 include_patterns, exclude_patterns, enabled, created_at, updated_at)
		VALUES ('job_a', 'photos', 'src_a', '/photos', '/tmp/backup', 'mirror',
		 '[]', '[]', 1, 1, 1)`); err != nil {
		t.Fatalf("insert sync job: %v", err)
	}

	if err := Migrate(ctx, db, dataDir); err != nil {
		t.Fatalf("Migrate v2->v3: %v", err)
	}
	assertVersion(t, db, embeddedLatestVersion(t))

	// 既有 Job 数据完整保留，schedule 列为 manual 缺省，无自动调度行为。
	var scheduleType, scheduleValue, scheduleTimezone string
	var anchor sql.NullInt64
	if err := db.QueryRow(`SELECT schedule_type, schedule_value, schedule_timezone,
		schedule_anchor_at FROM sync_jobs WHERE id = 'job_a'`,
	).Scan(&scheduleType, &scheduleValue, &scheduleTimezone, &anchor); err != nil {
		t.Fatalf("query job schedule columns: %v", err)
	}
	if scheduleType != "manual" || scheduleValue != "" || scheduleTimezone != "" || anchor.Valid {
		t.Errorf("upgraded schedule = (%q, %q, %q, %v), want (manual, '', '', NULL)",
			scheduleType, scheduleValue, scheduleTimezone, anchor)
	}

	// sync_runs 可用：CHECK 约束拒绝非法枚举。
	insertRun := func(id, trigger, status string) error {
		_, err := db.Exec(`INSERT INTO sync_runs
			(id, job_id, trigger_type, scheduled_for, status, started_at)
			VALUES (?, 'job_a', ?, NULL, ?, 1)`, id, trigger, status)
		return err
	}
	if err := insertRun("run_a", "manual", "succeeded"); err != nil {
		t.Fatalf("insert run: %v", err)
	}
	if err := insertRun("run_bad", "hourly", "succeeded"); err == nil {
		t.Fatal("insert trigger=hourly = nil, want CHECK violation")
	}
	if err := insertRun("run_bad", "manual", "queued"); err == nil {
		t.Fatal("insert status=queued = nil, want CHECK violation")
	}
	// schedule_type CHECK 生效。
	if _, err := db.Exec(`UPDATE sync_jobs SET schedule_type = 'yearly' WHERE id = 'job_a'`); err == nil {
		t.Fatal("update schedule_type=yearly = nil, want CHECK violation")
	}

	// sync_run_items 可用且随 run 级联。
	if _, err := db.Exec(`INSERT INTO sync_run_items
		(run_id, path, action, status, bytes, error)
		VALUES ('run_a', 'a.jpg', 'create', 'succeeded', 10, '')`); err != nil {
		t.Fatalf("insert run item: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO sync_run_items
		(run_id, path, action, status, bytes, error)
		VALUES ('run_a', 'a.jpg', 'purge', 'succeeded', 0, '')`); err == nil {
		t.Fatal("insert action=purge = nil, want CHECK violation")
	}

	// 删除 Job 级联清理 runs 与 items（真实本地文件由调用方负责，本层无感知）。
	if _, err := db.Exec("DELETE FROM sync_jobs WHERE id = 'job_a'"); err != nil {
		t.Fatalf("delete sync job: %v", err)
	}
	var runs, items int
	if err := db.QueryRow("SELECT count(*) FROM sync_runs").Scan(&runs); err != nil {
		t.Fatalf("count runs: %v", err)
	}
	if err := db.QueryRow("SELECT count(*) FROM sync_run_items").Scan(&items); err != nil {
		t.Fatalf("count run items: %v", err)
	}
	if runs != 0 || items != 0 {
		t.Errorf("rows after job delete = (runs %d, items %d), want (0, 0)", runs, items)
	}

	// 升级备份存在、可重开、停留在 v2 且无 v3 表。
	entries, err := os.ReadDir(filepath.Join(dataDir, backupsDirName))
	if err != nil {
		t.Fatalf("read backups dir: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("backup files = %d, want 1", len(entries))
	}
	if !strings.HasPrefix(entries[0].Name(), "tinysync-v2-") {
		t.Errorf("backup name = %q, want tinysync-v2- prefix", entries[0].Name())
	}
	backupDB, err := sql.Open("sqlite", filepath.Join(dataDir, backupsDirName, entries[0].Name()))
	if err != nil {
		t.Fatalf("open backup: %v", err)
	}
	defer backupDB.Close()
	assertVersion(t, backupDB, 2)
	if _, err := backupDB.Query("SELECT count(*) FROM sync_runs"); err == nil {
		t.Error("backup should not contain sync_runs table")
	}
}

// v3 → v4 升级：once_consumed_for 从存量 once run 回填（取最近一次
// occurrence），interval / manual Job 不回填；升级备份停留在 v3。
// migration 一旦发布即不可变接口，0004 的核心价值（backfill）必须
// 有专项回归。
func TestMigrateV3ToV4BackfillsOnceConsumption(t *testing.T) {
	dataDir := t.TempDir()
	db, err := Open(dataDir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	ctx := context.Background()
	// 用真实的 0001-0003 schema 构造 v3 形态的库。
	v3FS := fstest.MapFS{}
	for _, name := range []string{"0001_sources.sql", "0002_sync_jobs.sql", "0003_scheduler_history.sql"} {
		data, err := fs.ReadFile(migrationFS, "migrations/"+name)
		if err != nil {
			t.Fatalf("read embedded %s: %v", name, err)
		}
		v3FS["migrations/"+name] = &fstest.MapFile{Data: data}
	}
	if err := migrate(ctx, db, dataDir, v3FS); err != nil {
		t.Fatalf("build v3 database: %v", err)
	}
	assertVersion(t, db, 3)

	if _, err := db.Exec(`INSERT INTO sources
		(id, name, type, endpoint, username, password, enabled, created_at, updated_at)
		VALUES ('src_a', 'nas', 'webdav', 'https://example.com/dav/', 'user', 'secret', 1, 1, 1)`); err != nil {
		t.Fatalf("insert source: %v", err)
	}
	insertJob := func(id, name, scheduleType, scheduleValue, anchor string) {
		t.Helper()
		if _, err := db.Exec(`INSERT INTO sync_jobs
			(id, name, source_id, remote_root, local_root, mode,
			 include_patterns, exclude_patterns, enabled,
			 schedule_type, schedule_value, schedule_timezone, schedule_anchor_at,
			 created_at, updated_at)
			VALUES (?, ?, 'src_a', '/', '/tmp/x', 'copy', '[]', '[]', 1, ?, ?, '', `+anchor+`, 1, 1)`,
			id, name, scheduleType, scheduleValue); err != nil {
			t.Fatalf("insert job %s: %v", id, err)
		}
	}
	// once（执行过）+ interval + manual（v0.3 遗留缺省）三类 Job。
	insertJob("job_once", "once", "once", "2026-09-20T03:00:00Z", "NULL")
	insertJob("job_iv", "interval", "interval", "30m", "1000")
	insertJob("job_manual", "manual", "manual", "", "NULL")

	// once Job 有两条历史 occurrence（10:00 失败、10:30 失败——失败
	// 同样算消费）；interval run 不应参与回填。
	if _, err := db.Exec(`INSERT INTO sync_runs
		(id, job_id, trigger_type, scheduled_for, status, started_at)
		VALUES
		('run_old', 'job_once', 'once', 1000, 'failed', 1000),
		('run_new', 'job_once', 'once', 2000, 'failed', 2000),
		('run_iv', 'job_iv', 'interval', 1500, 'succeeded', 1500)`); err != nil {
		t.Fatalf("insert runs: %v", err)
	}

	if err := Migrate(ctx, db, dataDir); err != nil {
		t.Fatalf("Migrate v3->v4: %v", err)
	}
	assertVersion(t, db, embeddedLatestVersion(t))

	consumedFor := func(jobID string) any {
		t.Helper()
		var got any
		if err := db.QueryRow(
			"SELECT once_consumed_for FROM sync_jobs WHERE id = ?", jobID,
		).Scan(&got); err != nil {
			t.Fatalf("query consumed_for of %s: %v", jobID, err)
		}
		return got
	}
	// once：回填为最近一次 occurrence（started_at 倒序取 run_new）。
	if got := consumedFor("job_once"); got != int64(2000) {
		t.Errorf("once job consumed_for = %v, want 2000 (latest occurrence)", got)
	}
	// interval / manual：保持 NULL。
	if got := consumedFor("job_iv"); got != nil {
		t.Errorf("interval job consumed_for = %v, want NULL", got)
	}
	if got := consumedFor("job_manual"); got != nil {
		t.Errorf("manual job consumed_for = %v, want NULL", got)
	}

	// 升级备份存在且停留在 v3。
	entries, err := os.ReadDir(filepath.Join(dataDir, backupsDirName))
	if err != nil {
		t.Fatalf("read backups dir: %v", err)
	}
	if len(entries) != 1 || !strings.HasPrefix(entries[0].Name(), "tinysync-v3-") {
		t.Fatalf("backup files = %v, want one tinysync-v3-* entry", entries)
	}
	backupDB, err := sql.Open("sqlite", filepath.Join(dataDir, backupsDirName, entries[0].Name()))
	if err != nil {
		t.Fatalf("open backup: %v", err)
	}
	defer backupDB.Close()
	assertVersion(t, backupDB, 3)
	if _, err := backupDB.Query("SELECT once_consumed_for FROM sync_jobs"); err == nil {
		t.Error("backup should not contain once_consumed_for column")
	}
}

// v4 → v5 升级：WebDAV 扁平列一次性 backfill 到 config_json /
// credentials_json 后清空 legacy 列；匿名 Source 的 credentials_json
// 保持空对象。migration 一旦发布即不可变接口，0005 的核心价值
// （存量 WebDAV 无损迁移）必须有专项回归。
func TestMigrateV4ToV5BackfillsSourceConfigs(t *testing.T) {
	dataDir := t.TempDir()
	db, err := Open(dataDir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	ctx := context.Background()
	// 用真实的 0001-0004 schema 构造 v4 形态的库。
	v4FS := fstest.MapFS{}
	for _, name := range []string{
		"0001_sources.sql", "0002_sync_jobs.sql",
		"0003_scheduler_history.sql", "0004_once_consumption.sql",
	} {
		data, err := fs.ReadFile(migrationFS, "migrations/"+name)
		if err != nil {
			t.Fatalf("read embedded %s: %v", name, err)
		}
		v4FS["migrations/"+name] = &fstest.MapFile{Data: data}
	}
	if err := migrate(ctx, db, dataDir, v4FS); err != nil {
		t.Fatalf("build v4 database: %v", err)
	}
	assertVersion(t, db, 4)

	// 带密码与匿名（含特殊字符密码）两个存量 WebDAV Source。
	if _, err := db.Exec(`INSERT INTO sources
		(id, name, type, endpoint, username, password, enabled, created_at, updated_at)
		VALUES
		('src_a', 'nas', 'webdav', 'https://example.com/dav/', 'user', 'se"cret''x', 1, 1, 1),
		('src_anon', 'anon', 'webdav', 'https://anon.example.com/dav/', '', '', 1, 1, 1)`); err != nil {
		t.Fatalf("insert sources: %v", err)
	}

	if err := Migrate(ctx, db, dataDir); err != nil {
		t.Fatalf("Migrate v4->v5: %v", err)
	}
	assertVersion(t, db, embeddedLatestVersion(t))

	row := func(id string) (configJSON, credsJSON, endpoint, username, password string) {
		t.Helper()
		if err := db.QueryRow(
			"SELECT config_json, credentials_json, endpoint, username, password FROM sources WHERE id = ?", id,
		).Scan(&configJSON, &credsJSON, &endpoint, &username, &password); err != nil {
			t.Fatalf("query source %s: %v", id, err)
		}
		return
	}

	// 带密码行：config / credentials backfill 完整，legacy 列清空。
	configJSON, credsJSON, endpoint, username, password := row("src_a")
	if configJSON != `{"endpoint":"https://example.com/dav/","username":"user"}` {
		t.Errorf("config_json = %s, want backfilled webdav config", configJSON)
	}
	if credsJSON != `{"password":"se\"cret'x"}` {
		t.Errorf("credentials_json = %s, want backfilled password with escaping", credsJSON)
	}
	if endpoint != "" || username != "" || password != "" {
		t.Errorf("legacy columns = %q/%q/%q, want all cleared", endpoint, username, password)
	}

	// 匿名行：credentials_json 为空对象，不写入空串键。
	configJSON, credsJSON, endpoint, username, password = row("src_anon")
	if configJSON != `{"endpoint":"https://anon.example.com/dav/","username":""}` {
		t.Errorf("anon config_json = %s", configJSON)
	}
	if credsJSON != `{}` {
		t.Errorf("anon credentials_json = %s, want empty object", credsJSON)
	}
	if endpoint != "" || username != "" || password != "" {
		t.Errorf("anon legacy columns = %q/%q/%q, want all cleared", endpoint, username, password)
	}

	// 升级备份存在且停留在 v4。
	entries, err := os.ReadDir(filepath.Join(dataDir, backupsDirName))
	if err != nil {
		t.Fatalf("read backups dir: %v", err)
	}
	if len(entries) != 1 || !strings.HasPrefix(entries[0].Name(), "tinysync-v4-") {
		t.Fatalf("backup files = %v, want one tinysync-v4-* entry", entries)
	}
	backupDB, err := sql.Open("sqlite", filepath.Join(dataDir, backupsDirName, entries[0].Name()))
	if err != nil {
		t.Fatalf("open backup: %v", err)
	}
	defer backupDB.Close()
	assertVersion(t, backupDB, 4)
	if _, err := backupDB.Query("SELECT config_json FROM sources"); err == nil {
		t.Error("backup should not contain config_json column")
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

// TestMigrateV5ToV6AddsPublishedFiles：v5 库升级到 v6 后新增
// published_files 表，既有 sources / jobs / runs 数据完整保留，
// 备份停留在 v5。
func TestMigrateV5ToV6AddsPublishedFiles(t *testing.T) {
	dataDir := t.TempDir()
	db, err := Open(dataDir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	ctx := context.Background()
	// 用真实的 0001-0005 schema 构造 v5 形态的库。
	v5FS := fstest.MapFS{}
	for _, name := range []string{
		"0001_sources.sql", "0002_sync_jobs.sql",
		"0003_scheduler_history.sql", "0004_once_consumption.sql",
		"0005_source_configs.sql",
	} {
		data, err := fs.ReadFile(migrationFS, "migrations/"+name)
		if err != nil {
			t.Fatalf("read embedded %s: %v", name, err)
		}
		v5FS["migrations/"+name] = &fstest.MapFile{Data: data}
	}
	if err := migrate(ctx, db, dataDir, v5FS); err != nil {
		t.Fatalf("build v5 database: %v", err)
	}
	assertVersion(t, db, 5)

	// 存量数据：Source、Job 与一轮运行历史。
	if _, err := db.Exec(`INSERT INTO sources
		(id, name, type, endpoint, username, password,
		 config_json, credentials_json, enabled, created_at, updated_at)
		VALUES ('src_a', 'nas', 'webdav', '', '', '',
		 '{"endpoint":"https://example.com/dav/","username":"user"}', '{}', 1, 1, 1)`); err != nil {
		t.Fatalf("insert source: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO sync_jobs
		(id, name, source_id, remote_root, local_root, mode,
		 include_patterns, exclude_patterns, enabled,
		 schedule_type, schedule_value, schedule_timezone,
		 once_consumed_for, created_at, updated_at)
		VALUES ('job_a', 'photos', 'src_a', '/photos', '/tmp/backup', 'copy',
		 '[]', '[]', 1, 'manual', '', '', NULL, 1, 1)`); err != nil {
		t.Fatalf("insert job: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO sync_runs
		(id, job_id, trigger_type, scheduled_for, status, started_at)
		VALUES ('run_a', 'job_a', 'manual', NULL, 'succeeded', 1)`); err != nil {
		t.Fatalf("insert run: %v", err)
	}

	if err := Migrate(ctx, db, dataDir); err != nil {
		t.Fatalf("Migrate v5->v6: %v", err)
	}
	assertVersion(t, db, embeddedLatestVersion(t))

	// 既有数据完整保留。
	var jobName, sourceID string
	if err := db.QueryRow("SELECT name, source_id FROM sync_jobs WHERE id = 'job_a'").Scan(&jobName, &sourceID); err != nil {
		t.Fatalf("query job after upgrade: %v", err)
	}
	if jobName != "photos" || sourceID != "src_a" {
		t.Errorf("job after upgrade = (%q, %q), want (photos, src_a)", jobName, sourceID)
	}
	var runStatus string
	if err := db.QueryRow("SELECT status FROM sync_runs WHERE id = 'run_a'").Scan(&runStatus); err != nil {
		t.Fatalf("query run after upgrade: %v", err)
	}
	if runStatus != "succeeded" {
		t.Errorf("run status = %q, want succeeded", runStatus)
	}

	// published_files 可用：约束生效。
	if _, err := db.Exec(`INSERT INTO published_files
		(id, local_path, public_path, enabled, expires_at, created_at, updated_at)
		VALUES ('pub_a', '/tmp/a.txt', '/a.txt', 1, NULL, 1, 1)`); err != nil {
		t.Fatalf("insert published file: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO published_files
		(id, local_path, public_path, enabled, expires_at, created_at, updated_at)
		VALUES ('pub_b', '/tmp/b.txt', '/a.txt', 1, NULL, 1, 1)`); err == nil {
		t.Fatal("insert duplicate public_path = nil, want UNIQUE violation")
	}
	if _, err := db.Exec(`INSERT INTO published_files
		(id, local_path, public_path, enabled, expires_at, created_at, updated_at)
		VALUES ('pub_c', '/tmp/c.txt', '/c.txt', 2, NULL, 1, 1)`); err == nil {
		t.Fatal("insert enabled=2 = nil, want CHECK violation")
	}

	// 升级备份存在且停留在 v5。
	entries, err := os.ReadDir(filepath.Join(dataDir, backupsDirName))
	if err != nil {
		t.Fatalf("read backups dir: %v", err)
	}
	if len(entries) != 1 || !strings.HasPrefix(entries[0].Name(), "tinysync-v5-") {
		t.Fatalf("backup files = %v, want one tinysync-v5-* entry", entries)
	}
}

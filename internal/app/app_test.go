package app

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"
	"time"

	"tinysync/internal/auth"
	authsqlite "tinysync/internal/auth/sqlite"
	"tinysync/internal/config"
	"tinysync/internal/storage"
)

// bootstrapAdmin 在数据目录完成 migration 并初始化管理员密码：
// v0.7 起 serve 在 admin 未初始化时拒绝启动，测试环境用它满足
// 启动前置条件。
func bootstrapAdmin(t *testing.T, dataDir string) {
	t.Helper()
	ctx := context.Background()
	db, err := storage.Open(dataDir)
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := storage.Migrate(ctx, db, dataDir); err != nil {
		t.Fatalf("storage.Migrate: %v", err)
	}
	svc := auth.NewService(authsqlite.NewRepository(db))
	if err := svc.SetAdminPassword(ctx, "bootstrap-password-123"); err != nil {
		t.Fatalf("bootstrap admin password: %v", err)
	}
}

// Run 在 ctx 已取消时应立即返回 nil（优雅关闭视为成功）。
// 数据库在临时目录打开，不触碰真实同步数据。
func TestRunReturnsOnCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	cfg := config.Default()
	cfg.DataDir = t.TempDir()
	bootstrapAdmin(t, cfg.DataDir)

	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, cfg, fstest.MapFS{})
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run on canceled context = %v, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after context cancellation")
	}
}

// 首次启动自动初始化管理员密码；不需要 operator 先运行 CLI。
func TestRunBootstrapsAdminWhenAuthNotInitialized(t *testing.T) {
	cfg := config.Default()
	cfg.DataDir = t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := Run(ctx, cfg, fstest.MapFS{}); err != nil {
		t.Fatalf("Run first startup = %v, want nil", err)
	}
	db, err := storage.Open(cfg.DataDir)
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	defer db.Close()
	configured, err := auth.NewService(authsqlite.NewRepository(db)).AdminConfigured(context.Background())
	if err != nil || !configured {
		t.Fatalf("admin after first startup = (%v, %v), want (true, nil)", configured, err)
	}
}

// 数据库不可用时 Run 直接失败，不启动 HTTP 服务。
func TestRunFailsWhenDatabaseUnavailable(t *testing.T) {
	notADir := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(notADir, []byte("x"), 0o600); err != nil {
		t.Fatalf("prepare blocking file: %v", err)
	}

	cfg := config.Default()
	cfg.DataDir = notADir

	if err := Run(context.Background(), cfg, fstest.MapFS{}); err == nil {
		t.Fatal("Run with unusable datadir = nil, want error")
	}
}

// 优雅关闭收口：Run 正常返回后 WAL 被 checkpoint TRUNCATE 截断为零
// （或不存在），且已提交数据经重新打开完好可读。
func TestRunTruncatesWALOnGracefulShutdown(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cfg := config.Default()
	cfg.DataDir = t.TempDir()
	bootstrapAdmin(t, cfg.DataDir)

	// 预写一行业务数据制造 WAL 内容；连接在断言期间保持打开，
	// 保证 -wal / -shm 文件存在可断言。
	extra, err := storage.Open(cfg.DataDir)
	if err != nil {
		t.Fatalf("open extra connection: %v", err)
	}
	defer extra.Close()
	if _, err := extra.Exec(`INSERT INTO sources
		(id, name, type, endpoint, username, password, enabled, created_at, updated_at)
		VALUES ('src_wal', 'wal-shutdown', 'webdav', 'https://example.com', '', 'pw', 1, 1, 1)`); err != nil {
		t.Fatalf("seed source: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, cfg, fstest.MapFS{})
	}()
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run graceful = %v, want nil", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return after context cancellation")
	}

	walPath := filepath.Join(cfg.DataDir, "tinysync.db-wal")
	info, err := os.Stat(walPath)
	switch {
	case os.IsNotExist(err):
		// 最后一连接未落 WAL 时文件不存在，与零长度等价。
	case err != nil:
		t.Fatalf("stat wal: %v", err)
	case info.Size() != 0:
		t.Errorf("wal size after graceful shutdown = %d, want 0", info.Size())
	}

	// checkpoint 后已提交数据完好。
	var count int
	if err := extra.QueryRow("SELECT count(*) FROM sources WHERE id = 'src_wal'").Scan(&count); err != nil || count != 1 {
		t.Errorf("data after shutdown checkpoint (count=%d err=%v)", count, err)
	}
}

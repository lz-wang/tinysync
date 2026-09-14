package app

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"
	"time"

	"tinysync/internal/config"
)

// Run 在 ctx 已取消时应立即返回 nil（优雅关闭视为成功）。
// 数据库在临时目录打开，不触碰真实同步数据。
func TestRunReturnsOnCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	cfg := config.Default()
	cfg.DataDir = t.TempDir()

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

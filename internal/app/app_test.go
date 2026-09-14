package app

import (
	"context"
	"testing"
	"testing/fstest"
	"time"

	"tinysync/internal/config"
)

// Run 在 ctx 已取消时应立即返回 nil（优雅关闭视为成功）。
func TestRunReturnsOnCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, config.Default(), fstest.MapFS{})
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

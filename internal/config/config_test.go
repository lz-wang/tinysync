package config

import (
	"path/filepath"
	"testing"
)

func TestDefaults(t *testing.T) {
	cfg := Default()
	if cfg.DataDir != DefaultDataDir {
		t.Fatalf("expected default datadir %q, got %q", DefaultDataDir, cfg.DataDir)
	}
	if cfg.Port != DefaultPort {
		t.Fatalf("expected default port %d, got %d", DefaultPort, cfg.Port)
	}
	if cfg.ListenAddr() != ":9466" {
		t.Fatalf("expected listen addr :9466, got %q", cfg.ListenAddr())
	}
	if cfg.MaxConcurrentJobs != DefaultMaxConcurrentJobs {
		t.Fatalf("expected default max-concurrent-jobs %d, got %d", DefaultMaxConcurrentJobs, cfg.MaxConcurrentJobs)
	}
	if cfg.MaxConcurrentTransfers != DefaultMaxConcurrentTransfers {
		t.Fatalf("expected default max-concurrent-transfers %d, got %d", DefaultMaxConcurrentTransfers, cfg.MaxConcurrentTransfers)
	}
}

func TestLoadAppliesOptions(t *testing.T) {
	tempDir := t.TempDir() // 已是绝对路径
	cfg, err := Load(Options{DataDir: tempDir, Port: 19466, MaxConcurrentJobs: 3, MaxConcurrentTransfers: 8})
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.DataDir != tempDir {
		t.Fatalf("expected datadir %s, got %s", tempDir, cfg.DataDir)
	}
	if cfg.Port != 19466 {
		t.Fatalf("expected port 19466, got %d", cfg.Port)
	}
	if cfg.ListenAddr() != ":19466" {
		t.Fatalf("expected listen addr :19466, got %q", cfg.ListenAddr())
	}
	if cfg.MaxConcurrentJobs != 3 || cfg.MaxConcurrentTransfers != 8 {
		t.Fatalf("expected concurrency (3, 8), got (%d, %d)", cfg.MaxConcurrentJobs, cfg.MaxConcurrentTransfers)
	}
	if err := cfg.EnsureDataDir(); err != nil {
		t.Fatalf("ensure data dir: %v", err)
	}
}

func TestLoadFallsBackToDefaults(t *testing.T) {
	cfg, err := Load(Options{})
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	// 空 Options：回退默认值，DataDir 解析为绝对路径。
	if cfg.Port != DefaultPort {
		t.Fatalf("expected default port %d, got %d", DefaultPort, cfg.Port)
	}
	if cfg.MaxConcurrentJobs != DefaultMaxConcurrentJobs || cfg.MaxConcurrentTransfers != DefaultMaxConcurrentTransfers {
		t.Fatalf("expected default concurrency (%d, %d), got (%d, %d)",
			DefaultMaxConcurrentJobs, DefaultMaxConcurrentTransfers, cfg.MaxConcurrentJobs, cfg.MaxConcurrentTransfers)
	}
	if !filepath.IsAbs(cfg.DataDir) {
		t.Fatalf("expected absolute datadir, got %q", cfg.DataDir)
	}
}

func TestLoadRejectsInvalidPort(t *testing.T) {
	for _, port := range []int{-1, 65536, 70000} {
		if _, err := Load(Options{Port: port}); err == nil {
			t.Errorf("expected error for port %d, got nil", port)
		}
	}
	// 0 表示未提供，回退默认值而非报错。
	if cfg, err := Load(Options{Port: 0}); err != nil {
		t.Errorf("port 0 should fall back to default, got error: %v", err)
	} else if cfg.Port != DefaultPort {
		t.Errorf("port 0 should fall back to default %d, got %d", DefaultPort, cfg.Port)
	}
}

// 并发参数必须为正整数；0 表示未提供（回退默认值），负数直接拒绝。
func TestLoadValidatesConcurrency(t *testing.T) {
	if _, err := Load(Options{MaxConcurrentJobs: -1}); err == nil {
		t.Error("expected error for max-concurrent-jobs -1, got nil")
	}
	if _, err := Load(Options{MaxConcurrentTransfers: -4}); err == nil {
		t.Error("expected error for max-concurrent-transfers -4, got nil")
	}
	cfg, err := Load(Options{MaxConcurrentJobs: 0, MaxConcurrentTransfers: 0})
	if err != nil {
		t.Fatalf("zero concurrency should fall back to default: %v", err)
	}
	if cfg.MaxConcurrentJobs != DefaultMaxConcurrentJobs || cfg.MaxConcurrentTransfers != DefaultMaxConcurrentTransfers {
		t.Fatalf("expected default concurrency (%d, %d), got (%d, %d)",
			DefaultMaxConcurrentJobs, DefaultMaxConcurrentTransfers, cfg.MaxConcurrentJobs, cfg.MaxConcurrentTransfers)
	}
}

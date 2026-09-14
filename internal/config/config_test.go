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
}

func TestLoadAppliesOptions(t *testing.T) {
	tempDir := t.TempDir() // 已是绝对路径
	cfg, err := Load(Options{DataDir: tempDir, Port: 19466})
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

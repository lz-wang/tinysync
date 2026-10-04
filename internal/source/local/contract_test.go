package local

import (
	"os"
	"path/filepath"
	"testing"

	"tinysync/internal/source"
	"tinysync/internal/source/remotetest"
)

type localContractHarness struct{ root string }

func (h *localContractHarness) NewRemote(t *testing.T) source.Remote {
	h.root = t.TempDir()
	return newTestRemote(t, h.root)
}

// Write 经真实文件系统直接写入（测试装置不经被测 adapter）。
func (h *localContractHarness) Write(t *testing.T, logical, content string) {
	t.Helper()
	abs := filepath.Join(h.root, filepath.FromSlash(logical))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", abs, err)
	}
	if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", abs, err)
	}
}

// Mkdir 经真实文件系统创建目录。
func (h *localContractHarness) Mkdir(t *testing.T, logical string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(h.root, filepath.FromSlash(logical)), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", logical, err)
	}
}

// TestRemoteContractSuite 以统一契约套件验证 Local adapter。
func TestRemoteContractSuite(t *testing.T) {
	remotetest.RunSuite(t, &localContractHarness{})
}

// TestRemoteResumeContractSuite 以断点续传契约套件验证 OpenFrom。
func TestRemoteResumeContractSuite(t *testing.T) {
	remotetest.RunResumeSuite(t, &localContractHarness{})
}

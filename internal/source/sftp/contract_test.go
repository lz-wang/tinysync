package sftp

import (
	"os"
	"path/filepath"
	"testing"

	"tinysync/internal/source"
	"tinysync/internal/source/remotetest"
)

// sftpContractHarness 把进程内 SSH/SFTP 服务（真实握手 + pkg/sftp
// server + 真实临时目录）接到 remotetest 套件。
type sftpContractHarness struct {
	root string
}

// NewRemote 启动全新服务与空目录并经生产 Factory 构造 Remote。
func (h *sftpContractHarness) NewRemote(t *testing.T) source.Remote {
	h.root = t.TempDir()
	ts := startTestServer(t)
	cfg := sftpSourceConfig(ts, h.root, source.SFTPAuthPassword)
	return newSFTPFactoryRemote(t, ts, cfg, source.Credentials{SFTP: &source.SFTPCredentials{
		Password: testPassword,
	}})
}

// Write 经真实文件系统直接写入（测试装置不经被测 adapter）。
func (h *sftpContractHarness) Write(t *testing.T, logical, content string) {
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
func (h *sftpContractHarness) Mkdir(t *testing.T, logical string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(h.root, filepath.FromSlash(logical)), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", logical, err)
	}
}

// TestRemoteContractSuite 以统一契约套件验证 SFTP adapter。
func TestRemoteContractSuite(t *testing.T) {
	remotetest.RunSuite(t, &sftpContractHarness{})
}

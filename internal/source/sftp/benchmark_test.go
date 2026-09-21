package sftp

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"tinysync/internal/source"
	"tinysync/internal/syncjob"
)

// benchmark 皆对应同步扫描链路（syncjob.ScanRemote）：复用进程内
// SSH/SFTP 测试服务（真实握手 + pkg/sftp + 临时目录），setup 全部在
// 计时之外完成。ReadDir 调用次数的确定性断言在 walkDirectory 的
// 单元测试里（见 walk_test.go），benchmark 只记录 wall-clock /
// allocation。

// newBenchRemote 启动测试服务并把 remote_root 指向临时目录，返回
// 已连接的 Remote 与目录；数据集由调用方在计时外铺设。
func newBenchRemote(b *testing.B) (source.Remote, string) {
	b.Helper()
	root := b.TempDir()
	ts := startTestServer(b)
	cfg := sftpSourceConfig(ts, root, source.SFTPAuthPassword)
	remote := newSFTPFactoryRemote(b, ts, cfg, source.Credentials{SFTP: &source.SFTPCredentials{
		Password: testPassword,
	}})
	return remote, root
}

// seedBulk 在 root/bulk 下铺设 flat（dirs=1）或 nested（dirs 个子目录，
// 各 perDir 个文件）数据集。
func seedBulk(b *testing.B, root string, dirs, perDir int) {
	b.Helper()
	bulk := filepath.Join(root, "bulk")
	if err := os.Mkdir(bulk, 0o755); err != nil {
		b.Fatalf("mkdir bulk: %v", err)
	}
	for d := 0; d < dirs; d++ {
		dir := bulk
		if dirs > 1 {
			dir = filepath.Join(bulk, fmt.Sprintf("d%03d", d))
			if err := os.Mkdir(dir, 0o755); err != nil {
				b.Fatalf("mkdir %s: %v", dir, err)
			}
		}
		for i := 0; i < perDir; i++ {
			path := filepath.Join(dir, fmt.Sprintf("f%06d.txt", i))
			if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
				b.Fatalf("write %s: %v", path, err)
			}
		}
	}
}

// BenchmarkScanRemoteSFTP10KFlat：同步扫描 10,000 文件单目录。当前
// 实现按 DefaultListLimit(100) 伪分页对同一目录重复 ReadDir；修复
// 目标是每目录一次 ReadDir（次数断言见 walk_test.go）。
func BenchmarkScanRemoteSFTP10KFlat(b *testing.B) {
	remote, root := newBenchRemote(b)
	seedBulk(b, root, 1, 10000)
	ctx := context.Background()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		files, err := syncjob.ScanRemote(ctx, remote, "/")
		if err != nil {
			b.Fatalf("scan: %v", err)
		}
		if len(files) != 10000 {
			b.Fatalf("scanned %d files, want 10000", len(files))
		}
	}
}

// BenchmarkScanRemoteSFTP10KNested：同步扫描 100 目录 × 100 文件。
func BenchmarkScanRemoteSFTP10KNested(b *testing.B) {
	const dirs = 100
	const perDir = 100
	remote, root := newBenchRemote(b)
	seedBulk(b, root, dirs, perDir)
	ctx := context.Background()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		files, err := syncjob.ScanRemote(ctx, remote, "/")
		if err != nil {
			b.Fatalf("scan: %v", err)
		}
		if len(files) != dirs*perDir {
			b.Fatalf("scanned %d files, want %d", len(files), dirs*perDir)
		}
	}
}

package smb

import (
	"context"
	"fmt"
	"io"
	"testing"

	"tinysync/internal/source"
)

// SMB benchmark 皆走 adapter 真实代码路径（remotePath 映射、
// toFileInfo 转换、walkDirectory 枚举、PageSlice 切页），数据面为
// 内存 fake conn——SMB 无进程内 server 可用（真实 Samba 属
// env-gated 集成测试），网络往返不在这些基准的度量范围；相对数值
// 用于对比 adapter 层的优化，wall-clock 不作为 CI 门禁。每目录一次
// ReadDir 的确定性断言在 walk_test.go。

// newBenchRemote 构造接 fake conn 的 remote；数据集由调用方在计时外
// 铺设（seedBenchDirs）。
func newBenchRemote(b *testing.B) (*remote, *fakeConn) {
	b.Helper()
	c := newFakeConn()
	r := newFakeRemote(b, func(ctx context.Context) (conn, error) { return c, nil })
	return r, c
}

// seedBenchDirs 铺设 dirs 个子目录 × perDir 个文件；dirs=1 时全部
// 文件落在 root 单层。
func seedBenchDirs(b *testing.B, c *fakeConn, dirs, perDir int) {
	b.Helper()
	if dirs == 1 {
		entries := make([]fakeEntry, 0, perDir)
		for i := 0; i < perDir; i++ {
			entries = append(entries, fakeEntry{name: fmt.Sprintf("f%06d.txt", i), size: 1})
		}
		c.seed("", entries...)
		return
	}
	for d := 0; d < dirs; d++ {
		entries := make([]fakeEntry, 0, perDir)
		for i := 0; i < perDir; i++ {
			entries = append(entries, fakeEntry{name: fmt.Sprintf("f%06d.txt", i), size: 1})
		}
		c.seed(fmt.Sprintf("d%03d", d), entries...)
	}
	rootEntries := make([]fakeEntry, 0, dirs)
	for d := 0; d < dirs; d++ {
		rootEntries = append(rootEntries, fakeEntry{name: fmt.Sprintf("d%03d", d), isDir: true})
	}
	c.seed("", rootEntries...)
}

// BenchmarkSMBScanTree1K：100 目录 × 10 文件的全树扫描。
func BenchmarkSMBScanTree1K(b *testing.B) {
	r, c := newBenchRemote(b)
	seedBenchDirs(b, c, 100, 10)
	ctx := context.Background()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		n := 0
		err := r.ScanTree(ctx, "/", func(source.FileInfo) error { n++; return nil })
		if err != nil {
			b.Fatalf("scan: %v", err)
		}
		if n != 1100 {
			b.Fatalf("visited %d entries, want 1100 (1000 files + 100 dirs)", n)
		}
	}
}

// BenchmarkSMBScanTree10KFlat：10,000 文件单目录的全树扫描（每目录
// 一次 ReadDir 的契约下不产生重复枚举）。
func BenchmarkSMBScanTree10KFlat(b *testing.B) {
	r, c := newBenchRemote(b)
	seedBenchDirs(b, c, 1, 10000)
	ctx := context.Background()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		n := 0
		err := r.ScanTree(ctx, "/", func(source.FileInfo) error { n++; return nil })
		if err != nil {
			b.Fatalf("scan: %v", err)
		}
		if n != 10000 {
			b.Fatalf("visited %d entries, want 10000", n)
		}
	}
}

// BenchmarkSMBList1000：1000 条目单层的 List 分页链路（单层完整枚举
// + MaxListLimit 截断切页，跟随 cursor 到 EOF）。
func BenchmarkSMBList1000(b *testing.B) {
	r, c := newBenchRemote(b)
	seedBenchDirs(b, c, 1, 1000)
	ctx := context.Background()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		seen := 0
		cursor := ""
		for {
			page, err := r.List(ctx, "/", source.ListOptions{Limit: source.MaxListLimit, Cursor: cursor})
			if err != nil {
				b.Fatalf("list: %v", err)
			}
			seen += len(page.Entries)
			if page.NextCursor == "" {
				break
			}
			cursor = page.NextCursor
		}
		if seen != 1000 {
			b.Fatalf("listed %d entries, want 1000", seen)
		}
	}
}

// BenchmarkSMBPathMapping：remotePath（logical → native 映射 + root
// confinement 防御）微基准，同步扫描每个条目都要经过。
func BenchmarkSMBPathMapping(b *testing.B) {
	const root = "/photos"
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		native, err := remotePath(root, "/2026/10/a.jpg")
		if err != nil || native != `photos\2026\10\a.jpg` {
			b.Fatalf("remotePath = %q, %v", native, err)
		}
	}
}

// BenchmarkSMBOpen1MiB：1 MiB 文件 Open + 完整读取（adapter 的
// ReadCloser 包装开销；fake 数据面为内存流，网络吞吐由真实 Samba
// 集成测试覆盖）。
func BenchmarkSMBOpen1MiB(b *testing.B) {
	r, c := newBenchRemote(b)
	content := make([]byte, 1<<20)
	for i := range content {
		content[i] = byte(i)
	}
	c.seed("", fakeEntry{name: "big.bin", size: int64(len(content))})
	c.writeFile(`big.bin`, string(content))
	ctx := context.Background()

	b.ReportAllocs()
	b.SetBytes(int64(len(content)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rc, err := r.Open(ctx, "/big.bin")
		if err != nil {
			b.Fatalf("open: %v", err)
		}
		n, err := io.Copy(io.Discard, rc)
		_ = rc.Close()
		if err != nil || n != int64(len(content)) {
			b.Fatalf("read = %d bytes, %v; want %d", n, err, len(content))
		}
	}
}

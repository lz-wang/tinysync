package syncjob

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"tinysync/internal/source"
)

// seededTarget 建立带既有目标文件的目录，返回 root 与目标路径。
func seededTarget(t *testing.T) (string, string) {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "docs"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	target := filepath.Join(root, "docs", "a.txt")
	if err := os.WriteFile(target, []byte("old-content"), 0o644); err != nil {
		t.Fatalf("seed target: %v", err)
	}
	return root, target
}

// fsRemote 返回内容固定的最小 Remote（带 Open 计数）。
func fsRemote(content string) *countingRemote {
	return &countingRemote{contents: map[string]io.ReadCloser{
		"/docs/a.txt": io.NopCloser(strings.NewReader(content)),
	}}
}

// assertTargetPreserved 断言既有目标内容未变。
func assertTargetPreserved(t *testing.T, target string) {
	t.Helper()
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read target: %v", err)
	}
	if string(data) != "old-content" {
		t.Errorf("target = %q, want old-content preserved", data)
	}
}

// create temp 失败（permission denied 语义）：目标完好、无临时文件、
// 错误可判定为确定性失败（不重试）。
func TestFSFailureCreateTemp(t *testing.T) {
	root, target := seededTarget(t)
	remote := fsRemote("new-content")
	d := newTestDownloader(remote)
	d.hooks = &fileHooks{
		createTemp: func(path string) (*os.File, error) {
			return nil, syscall.EACCES
		},
	}

	err := d.Download(context.Background(), "/docs/a.txt", root, "docs/a.txt", source.Fingerprint{Size: 11})
	if err == nil {
		t.Fatal("Download with create-temp failure = nil, want error")
	}
	if got := remote.opens.Load(); got != 1 {
		// EACCES → fs.ErrPermission → permanent：单次尝试。
		t.Errorf("open attempts = %d, want 1 (permission errors must not retry)", got)
	}
	assertTargetPreserved(t, target)
	assertNoTempFiles(t, root)
}

// 写入中途 ENOSPC：目标完好、临时文件清理、确定性失败不重试。
func TestFSFailureENOSPCMidWrite(t *testing.T) {
	root, target := seededTarget(t)
	remote := fsRemote("content-that-exceeds-the-budget")
	d := newTestDownloader(remote)
	d.hooks = &fileHooks{
		wrapWriter: func(f *os.File) io.Writer {
			return &budgetWriter{f: f, budget: 4}
		},
	}

	err := d.Download(context.Background(), "/docs/a.txt", root, "docs/a.txt", source.Fingerprint{Size: 31})
	if err == nil {
		t.Fatal("Download with ENOSPC = nil, want error")
	}
	if !errors.Is(err, syscall.ENOSPC) {
		t.Errorf("error = %v, want ENOSPC", err)
	}
	assertTargetPreserved(t, target)
	assertNoTempFiles(t, root)
}

// fsync 失败：数据未确认落盘前不得替换目标；临时文件清理。
func TestFSFailureSync(t *testing.T) {
	root, target := seededTarget(t)
	remote := fsRemote("new-content")
	d := newTestDownloader(remote)
	d.hooks = &fileHooks{
		syncFile: func(*os.File) error { return errors.New("fsync: I/O error") },
	}

	err := d.Download(context.Background(), "/docs/a.txt", root, "docs/a.txt", source.Fingerprint{Size: 11})
	if err == nil {
		t.Fatal("Download with fsync failure = nil, want error")
	}
	assertTargetPreserved(t, target)
	assertNoTempFiles(t, root)
}

// rename 失败：旧目标完好（替换失败发生在新文件生效之前）、临时
// 文件清理。
func TestFSFailureRename(t *testing.T) {
	root, target := seededTarget(t)
	remote := fsRemote("new-content")
	d := newTestDownloader(remote)
	d.hooks = &fileHooks{
		renameFile: func(old, new string) error { return errors.New("rename: device busy") },
	}

	err := d.Download(context.Background(), "/docs/a.txt", root, "docs/a.txt", source.Fingerprint{Size: 11})
	if err == nil {
		t.Fatal("Download with rename failure = nil, want error")
	}
	assertTargetPreserved(t, target)
	assertNoTempFiles(t, root)
}

// budgetWriter 写满预算后返回 ENOSPC，模拟磁盘写满。
type budgetWriter struct {
	f      *os.File
	budget int
}

func (w *budgetWriter) Write(p []byte) (int, error) {
	if w.budget <= 0 {
		return 0, syscall.ENOSPC
	}
	if len(p) <= w.budget {
		n, err := w.f.Write(p)
		w.budget -= n
		return n, err
	}
	// 预算不足：写满剩余预算后带 ENOSPC 返回（短写必须携带错误）。
	n, _ := w.f.Write(p[:w.budget])
	w.budget = 0
	return n, syscall.ENOSPC
}

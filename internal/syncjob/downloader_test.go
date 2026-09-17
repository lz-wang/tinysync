package syncjob

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"tinysync/internal/source"
)

// downloadRemote 可编程 Open 的 Remote：按路径返回内容 reader 或错误序列。
type downloadRemote struct {
	contents map[string]io.ReadCloser
	errSeq   map[string][]error
}

func (d *downloadRemote) Stat(ctx context.Context, path string) (source.FileInfo, error) {
	return source.FileInfo{}, errors.New("not implemented")
}

func (d *downloadRemote) List(ctx context.Context, path string, opts source.ListOptions) (source.FilePage, error) {
	return source.FilePage{}, errors.New("not implemented")
}

func (d *downloadRemote) Open(ctx context.Context, path string) (io.ReadCloser, error) {
	if errs := d.errSeq[path]; len(errs) > 0 {
		d.errSeq[path] = errs[1:]
		return nil, errs[0]
	}
	if rc, ok := d.contents[path]; ok {
		return rc, nil
	}
	return nil, fmt.Errorf("no such remote file %s", path)
}

func (d *downloadRemote) Close() error {
	return nil
}

// errReader 读取到第 n 字节后报错，模拟传输中途断开。
type errReader struct {
	data   []byte
	pos    int
	failAt int
}

func (e *errReader) Read(p []byte) (int, error) {
	if e.pos >= e.failAt {
		return 0, errors.New("connection reset mid-transfer")
	}
	n := copy(p, e.data[e.pos:])
	e.pos += n
	return n, nil
}

func (e *errReader) Close() error { return nil }

// newTestDownloader 构造重试零延迟的下载器，避免测试拖慢。
func newTestDownloader(remote source.Remote) *Downloader {
	return &Downloader{
		remote:      remote,
		maxAttempts: 3,
		backoff:     func(int) time.Duration { return 0 },
	}
}

// 正常下载：内容落地、无临时文件残留。
func TestDownloadCreatesFile(t *testing.T) {
	root := t.TempDir()
	remote := &downloadRemote{contents: map[string]io.ReadCloser{
		"/docs/a.txt": io.NopCloser(strings.NewReader("hello tinysync")),
	}}
	d := newTestDownloader(remote)

	err := d.Download(context.Background(), "/docs/a.txt", root, "docs/a.txt", source.Fingerprint{Size: 14})
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(root, "docs", "a.txt"))
	if err != nil {
		t.Fatalf("read target: %v", err)
	}
	if string(data) != "hello tinysync" {
		t.Errorf("content = %q, want hello tinysync", data)
	}
	assertNoTempFiles(t, root)
}

// 大小校验失败：删除临时文件，目标不落盘。
func TestDownloadVerifiesSize(t *testing.T) {
	root := t.TempDir()
	remote := &downloadRemote{contents: map[string]io.ReadCloser{
		"/docs/a.txt": io.NopCloser(strings.NewReader("short")),
	}}
	d := newTestDownloader(remote)

	err := d.Download(context.Background(), "/docs/a.txt", root, "docs/a.txt", source.Fingerprint{Size: 100})
	if err == nil {
		t.Fatal("Download with size mismatch = nil, want error")
	}
	if _, err := os.Stat(filepath.Join(root, "docs", "a.txt")); !os.IsNotExist(err) {
		t.Errorf("target exists after failed download, stat err = %v", err)
	}
	assertNoTempFiles(t, root)
}

// 零字节文件严格校验：声明 0 字节时 body 必须为空；声明 0 但收到
// 内容视为传输损坏，不落地。
func TestDownloadVerifiesZeroSize(t *testing.T) {
	t.Run("empty body ok", func(t *testing.T) {
		root := t.TempDir()
		remote := &downloadRemote{contents: map[string]io.ReadCloser{
			"/empty.txt": io.NopCloser(strings.NewReader("")),
		}}
		d := newTestDownloader(remote)

		if err := d.Download(context.Background(), "/empty.txt", root, "empty.txt", source.Fingerprint{Size: 0}); err != nil {
			t.Fatalf("Download zero-size: %v", err)
		}
		data, err := os.ReadFile(filepath.Join(root, "empty.txt"))
		if err != nil || len(data) != 0 {
			t.Errorf("zero-size file = %q (%v), want empty", data, err)
		}
		assertNoTempFiles(t, root)
	})
	t.Run("non-empty body rejected", func(t *testing.T) {
		root := t.TempDir()
		remote := &downloadRemote{contents: map[string]io.ReadCloser{
			"/empty.txt": io.NopCloser(strings.NewReader("unexpected")),
		}}
		// 单次尝试：fake 的 reader 复用会在重试时耗尽内容，
		// 干扰「声明 0 但 body 非空」的判定。
		d := &Downloader{remote: remote, maxAttempts: 1, backoff: func(int) time.Duration { return 0 }}

		if err := d.Download(context.Background(), "/empty.txt", root, "empty.txt", source.Fingerprint{Size: 0}); err == nil {
			t.Fatal("Download with declared 0 but non-empty body = nil, want error")
		}
		if _, err := os.Stat(filepath.Join(root, "empty.txt")); !os.IsNotExist(err) {
			t.Errorf("target exists after failed download, stat err = %v", err)
		}
		assertNoTempFiles(t, root)
	})
}

// 传输中途失败：已存在的旧目标保持原内容，临时文件清理。
func TestDownloadPreservesTargetOnMidTransferFailure(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "docs"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	target := filepath.Join(root, "docs", "a.txt")
	if err := os.WriteFile(target, []byte("old-content"), 0o644); err != nil {
		t.Fatalf("seed target: %v", err)
	}

	remote := &downloadRemote{contents: map[string]io.ReadCloser{
		"/docs/a.txt": &errReader{data: []byte("new-content-longer"), failAt: 4},
	}}
	d := &Downloader{remote: remote, maxAttempts: 1, backoff: func(int) time.Duration { return 0 }}

	err := d.Download(context.Background(), "/docs/a.txt", root, "docs/a.txt", source.Fingerprint{Size: 18})
	if err == nil {
		t.Fatal("Download with mid-transfer failure = nil, want error")
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read target: %v", err)
	}
	if string(data) != "old-content" {
		t.Errorf("target = %q, want old-content preserved", data)
	}
	assertNoTempFiles(t, root)
}

// 基础重试：首次 Open 失败、重试成功，最终完成。
func TestDownloadRetriesTransientFailure(t *testing.T) {
	root := t.TempDir()
	remote := &downloadRemote{
		contents: map[string]io.ReadCloser{
			"/docs/a.txt": io.NopCloser(strings.NewReader("ok")),
		},
		errSeq: map[string][]error{
			"/docs/a.txt": {errors.New("503 service unavailable")},
		},
	}
	d := newTestDownloader(remote)

	if err := d.Download(context.Background(), "/docs/a.txt", root, "docs/a.txt", source.Fingerprint{Size: 2}); err != nil {
		t.Fatalf("Download after retry: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(root, "docs", "a.txt"))
	if err != nil {
		t.Fatalf("read target: %v", err)
	}
	if string(data) != "ok" {
		t.Errorf("content = %q, want ok", data)
	}
}

// context 取消后不再重试，错误可判定为 Canceled。
func TestDownloadStopsOnContextCancel(t *testing.T) {
	root := t.TempDir()
	remote := &downloadRemote{
		errSeq: map[string][]error{
			"/docs/a.txt": {errors.New("e1"), errors.New("e2"), errors.New("e3"), errors.New("e4")},
		},
	}
	d := newTestDownloader(remote)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := d.Download(ctx, "/docs/a.txt", root, "docs/a.txt", source.Fingerprint{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Download with canceled ctx = %v, want context.Canceled", err)
	}
}

// 目标是既有 symlink 时拒绝，不覆盖。
func TestDownloadRejectsSymlinkTarget(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "docs"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "docs", "a.txt")); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	remote := &downloadRemote{contents: map[string]io.ReadCloser{
		"/docs/a.txt": io.NopCloser(strings.NewReader("x")),
	}}
	d := newTestDownloader(remote)

	if err := d.Download(context.Background(), "/docs/a.txt", root, "docs/a.txt", source.Fingerprint{}); err == nil {
		t.Fatal("Download onto symlink = nil, want error")
	}
	entries, err := os.ReadDir(filepath.Join(root, "docs"))
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	if len(entries) != 1 {
		t.Errorf("docs entries = %d, want only the symlink (no temp files)", len(entries))
	}
}

// assertNoTempFiles 断言目录树中没有遗留的 .tinysync-part- 临时文件。
func assertNoTempFiles(t *testing.T, root string) {
	t.Helper()
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if strings.Contains(d.Name(), ".tinysync-part-") {
			t.Errorf("temp file left behind: %s", path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
}

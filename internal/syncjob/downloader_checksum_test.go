package syncjob

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"tinysync/internal/source"
)

// stubChecksumRemote 提供固定内容的只读远端。
type stubChecksumRemote struct {
	content string
}

func (r stubChecksumRemote) Stat(ctx context.Context, path string) (source.FileInfo, error) {
	return source.FileInfo{}, errors.New("not implemented")
}

func (r stubChecksumRemote) List(ctx context.Context, path string, opts source.ListOptions) (source.FilePage, error) {
	return source.FilePage{}, errors.New("not implemented")
}

func (r stubChecksumRemote) Open(ctx context.Context, path string) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader(r.content)), nil
}

func (r stubChecksumRemote) Close() error { return nil }

// sha256Hex 计算内容的 sha256 十六进制。
func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// TestNewChecksumVerifier 校验器构造规则：空串免校验、合法 sha256
// 接受、坏长度 / 非 hex / 未知算法 fail-closed。
func TestNewChecksumVerifier(t *testing.T) {
	if v, err := newChecksumVerifier(""); err != nil || v != nil {
		t.Errorf("empty checksum = (%v, %v), want (nil, nil)", v, err)
	}
	if v, err := newChecksumVerifier("sha256:" + strings.Repeat("a", 64)); err != nil || v == nil {
		t.Errorf("valid sha256 = (%v, %v), want verifier", v, err)
	}
	for _, bad := range []string{
		"sha256:" + strings.Repeat("a", 63),
		"sha256:" + strings.Repeat("g", 64),
		"md5:abc",
		"barehex",
	} {
		if _, err := newChecksumVerifier(bad); err == nil {
			t.Errorf("newChecksumVerifier(%q) = nil error, want error", bad)
		}
	}
}

// TestDownloadChecksumSuccess 摘要匹配时文件正常落地（原子写入完成）。
func TestDownloadChecksumSuccess(t *testing.T) {
	content := "release-asset-bytes"
	root := t.TempDir()
	target := filepath.Join(root, "app.tar.gz")
	if err := os.WriteFile(target, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	d := NewDownloader(stubChecksumRemote{content: content})
	fp := source.Fingerprint{Size: int64(len(content)), Checksum: "sha256:" + sha256Hex(content)}
	if err := d.Download(t.Context(), "/a", root, "app.tar.gz", fp); err != nil {
		t.Fatalf("Download: %v", err)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != content {
		t.Errorf("target = %q, want %q", string(got), content)
	}
}

// TestDownloadChecksumMismatch 摘要不匹配属确定性失败：不重试、
// 已有目标文件不被覆盖、无临时文件残留（验收：SHA-256 不匹配时不
// 覆盖原文件）。
func TestDownloadChecksumMismatch(t *testing.T) {
	content := "release-asset-bytes"
	root := t.TempDir()
	target := filepath.Join(root, "app.tar.gz")
	if err := os.WriteFile(target, []byte("previous-version"), 0o644); err != nil {
		t.Fatal(err)
	}
	d := NewDownloader(stubChecksumRemote{content: content})
	fp := source.Fingerprint{Size: int64(len(content)), Checksum: "sha256:" + strings.Repeat("0", 64)}
	err := d.Download(t.Context(), "/a", root, "app.tar.gz", fp)
	if err == nil {
		t.Fatal("Download succeeded, want checksum mismatch error")
	}
	if source.IsRetryable(err) {
		t.Errorf("mismatch should be permanent: %v", err)
	}
	got, readErr := os.ReadFile(target)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(got) != "previous-version" {
		t.Errorf("target overwritten: %q, want original content preserved", string(got))
	}
	entries, _ := os.ReadDir(root)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), tempPrefix) {
			t.Errorf("temp file leaked: %s", e.Name())
		}
	}
}

// TestDownloadChecksumRejectsUnsupportedFormat 指纹声明了不支持的
// 摘要格式时下载确定性失败，绝不静默跳过校验。
func TestDownloadChecksumRejectsUnsupportedFormat(t *testing.T) {
	root := t.TempDir()
	d := NewDownloader(stubChecksumRemote{content: "x"})
	fp := source.Fingerprint{Size: 1, Checksum: "md5:d41d8cd98f00b204e9800998ecf8427e"}
	if err := d.Download(t.Context(), "/a", root, "new.bin", fp); err == nil {
		t.Fatal("Download succeeded with unsupported checksum, want error")
	} else if source.IsRetryable(err) {
		t.Errorf("unsupported format should be permanent: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "new.bin")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("target exists after rejected download: %v", err)
	}
}

// TestDownloadChecksumSizeWins 尺寸校验先于摘要：声明尺寸不符时
// 短路失败，摘要仍不落地。
func TestDownloadChecksumSizeWins(t *testing.T) {
	content := "release-asset-bytes"
	root := t.TempDir()
	d := NewDownloader(stubChecksumRemote{content: content})
	fp := source.Fingerprint{Size: 3, Checksum: "sha256:" + sha256Hex(content)}
	err := d.Download(t.Context(), "/a", root, "app.bin", fp)
	if err == nil || !strings.Contains(err.Error(), "size mismatch") {
		t.Errorf("Download = %v, want size mismatch", err)
	}
	if _, err := os.Stat(filepath.Join(root, "app.bin")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("target exists after size mismatch: %v", err)
	}
}

// TestDownloadChecksumWithProgress 摘要校验与字节进度并存：喂 hash
// 不改变 bytes_done 语义（进度只计成功写入字节）。
func TestDownloadChecksumWithProgress(t *testing.T) {
	content := "progress-and-hash"
	root := t.TempDir()
	d := NewDownloader(stubChecksumRemote{content: content})
	fp := source.Fingerprint{Size: int64(len(content)), Checksum: "sha256:" + sha256Hex(content)}
	listener := &countingListener{}
	if err := d.download(t.Context(), "/a", root, "app.bin", fp, listener); err != nil {
		t.Fatalf("download: %v", err)
	}
	if listener.written != int64(len(content)) {
		t.Errorf("listener.written = %d, want %d", listener.written, len(content))
	}
}

// countingListener 是累计字节的进度回调（与既有测试 helper 命名区分）。
type countingListener struct {
	written int64
}

func (l *countingListener) AttemptStart() { l.written = 0 }

func (l *countingListener) Write(n int64) { l.written += n }

// TestDownloadChecksumEmptyContent 零字节内容 + 零字节声明摘要的
// sha256（空串摘要）同样成立。
func TestDownloadChecksumEmptyContent(t *testing.T) {
	root := t.TempDir()
	d := NewDownloader(stubChecksumRemote{content: ""})
	fp := source.Fingerprint{Size: 0, Checksum: "sha256:" + sha256Hex("")}
	if err := d.Download(t.Context(), "/a", root, "empty.bin", fp); err != nil {
		t.Fatalf("Download: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(root, "empty.bin"))
	if err != nil || len(got) != 0 {
		t.Errorf("empty file = %q, %v", string(got), err)
	}
}

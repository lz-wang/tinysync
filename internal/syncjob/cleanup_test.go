package syncjob

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// 只删除与 Downloader 实际生成的临时文件名（.tinysync-part- + 12 位
// hex）严格匹配的普通文件；其余同名前缀变体（裸前缀、6 位 hex、非
// hex、带扩展名）可能是合法用户文件，一概保留。symlink、目录（即使
// 同名前缀）与无关隐藏文件同样不动；嵌套子目录内的遗留同样清理；
// LocalRoot 不存在静默跳过。
func TestRemoveStaleTempFiles(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "videos", "movies")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(root, "docs"), 0o755); err != nil {
		t.Fatalf("mkdir docs: %v", err)
	}

	write := func(rel, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, rel), []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}
	// 真实 Downloader 形态（tempPrefix + 6 字节 hex）。
	write("docs/.tinysync-part-0123456789ab", "stale temp")
	write(filepath.Join("videos", "movies", ".tinysync-part-fedcba987654"), "stale temp nested")
	// 伪装成临时文件的合法用户文件：一律保留。
	write("docs/.tinysync-part-", "bare prefix")
	write("docs/.tinysync-part-abc123", "user file with only 6 hex chars")
	write("docs/.tinysync-part-notes", "user file with non-hex suffix")
	write("docs/.tinysync-part-0123456789ag", "user file with non-hex char")
	write("docs/.tinysync-part-0123456789ab.txt", "user file with extra suffix")
	write("docs/.tinysync-other", "unrelated hidden file")
	write("docs/keep.tinysync-part-x", "regular file whose name merely contains the prefix")
	write("docs/normal.txt", "real content")
	if err := os.Symlink("normal.txt", filepath.Join(root, "docs", ".tinysync-part-link")); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	if err := os.Mkdir(filepath.Join(root, ".tinysync-part-dir"), 0o755); err != nil {
		t.Fatalf("mkdir decoy: %v", err)
	}

	removed, err := RemoveStaleTempFiles(context.Background(), []string{root})
	if err != nil {
		t.Fatalf("RemoveStaleTempFiles: %v", err)
	}
	if removed != 2 {
		t.Errorf("removed = %d, want 2", removed)
	}
	if _, err := os.Stat(filepath.Join(root, "docs", ".tinysync-part-0123456789ab")); !os.IsNotExist(err) {
		t.Errorf("stale temp file survived: %v", err)
	}
	if _, err := os.Stat(filepath.Join(nested, ".tinysync-part-fedcba987654")); !os.IsNotExist(err) {
		t.Errorf("stale nested temp file survived: %v", err)
	}
	for _, keep := range []string{
		"docs/.tinysync-part-",
		"docs/.tinysync-part-abc123",
		"docs/.tinysync-part-notes",
		"docs/.tinysync-part-0123456789ag",
		"docs/.tinysync-part-0123456789ab.txt",
		"docs/.tinysync-other",
		"docs/keep.tinysync-part-x",
		"docs/normal.txt",
		"docs/.tinysync-part-link",
		".tinysync-part-dir",
	} {
		if _, err := os.Stat(filepath.Join(root, keep)); err != nil {
			t.Errorf("decoy %s was removed: %v", keep, err)
		}
	}
}

// isTransferTempName 严格匹配 Downloader 真实生成的临时文件名形态。
func TestIsTransferTempName(t *testing.T) {
	cases := []struct {
		name string
		want bool
	}{
		{".tinysync-part-0123456789ab", true},
		{".tinysync-part-fedcba987654", true},
		{".tinysync-part-", false},                 // 裸前缀
		{".tinysync-part-abc123", false},           // 只有 6 位 hex
		{".tinysync-part-0123456789", false},       // 只有 10 位 hex
		{".tinysync-part-0123456789abc", false},    // 13 位
		{".tinysync-part-0123456789ag", false},     // 含非 hex 字符
		{".tinysync-part-0123456789ab.txt", false}, // 带扩展名
		{".tinysync-part-notes", false},            // 用户文件
		{".tinysync-other", false},                 // 其它隐藏文件
		{"normal.txt", false},                      // 无前缀
		{"", false},                                // 空名
	}
	for _, tc := range cases {
		if got := isTransferTempName(tc.name); got != tc.want {
			t.Errorf("isTransferTempName(%q) = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// LocalRoot 不存在（Job 尚未运行过）静默跳过；非目录 root 跳过。
func TestRemoveStaleTempFilesSkipsMissingRoots(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "not-created-yet")
	notADir := filepath.Join(t.TempDir(), "file.txt")
	if err := os.WriteFile(notADir, []byte("x"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	removed, err := RemoveStaleTempFiles(context.Background(), []string{missing, notADir})
	if err != nil {
		t.Fatalf("RemoveStaleTempFiles: %v", err)
	}
	if removed != 0 {
		t.Errorf("removed = %d, want 0", removed)
	}
}

// ctx 取消后停止清理。
func TestRemoveStaleTempFilesHonorsContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	removed, err := RemoveStaleTempFiles(ctx, []string{t.TempDir()})
	if !(-removed == 0) || err == nil {
		t.Fatalf("RemoveStaleTempFiles with canceled ctx = (%d, %v), want error", removed, err)
	}
}

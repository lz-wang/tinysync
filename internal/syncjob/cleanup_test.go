package syncjob

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// 只删除与内部临时前缀匹配的普通文件：其它隐藏文件、symlink、
// 目录（即使同名前缀）与合法文件一概不动；嵌套子目录内的遗留
// 同样清理；LocalRoot 不存在静默跳过。
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
	write("docs/.tinysync-part-abc123", "stale temp")
	write(filepath.Join("videos", "movies", ".tinysync-part-ff0099"), "stale temp nested")
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
	if _, err := os.Stat(filepath.Join(root, "docs", ".tinysync-part-abc123")); !os.IsNotExist(err) {
		t.Errorf("stale temp file survived: %v", err)
	}
	if _, err := os.Stat(filepath.Join(nested, ".tinysync-part-ff0099")); !os.IsNotExist(err) {
		t.Errorf("stale nested temp file survived: %v", err)
	}
	for _, keep := range []string{
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

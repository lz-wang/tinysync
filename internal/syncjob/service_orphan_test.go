package syncjob_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"tinysync/internal/syncjob"
)

// 写入 legacy 随机临时文件与 v1 断点文件两种形态的中间文件，外加
// 断点形态的 symlink 与用户文件。
func seedTransferTemps(t *testing.T, root string) {
	t.Helper()
	legacy := filepath.Join(root, ".tinysync-part-0123456789ab")
	if err := os.WriteFile(legacy, []byte("legacy"), 0o644); err != nil {
		t.Fatal(err)
	}
	partial := filepath.Join(root, ".tinysync-part-v1-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
	if err := os.WriteFile(partial, []byte("partial"), 0o644); err != nil {
		t.Fatal(err)
	}
	// 断点形态的 symlink：旧 root 退出配置面后不会再有 Downloader
	// 访问，fail-closed 验证没有意义——清理时一并 unlink（不跟随
	// 目标，链接指向的用户文件不受影响）。
	if err := os.Symlink(legacy, filepath.Join(root, ".tinysync-part-v1-cccccccccccccccccccccccccccccccc-dddddddddddddddddddddddddddddddd")); err != nil {
		t.Fatal(err)
	}
	normal := filepath.Join(root, "user-file.txt")
	if err := os.WriteFile(normal, []byte("keep me"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// 删除 Job：旧 LocalRoot 退出配置面后，其中的传输中间文件（legacy
// 临时文件、v1 断点文件、断点形态 symlink）立即回收——启动期清理
// 不再枚举这个 root，partialRetention 对此类 mapping 变更孤儿实际
// 无效。用户文件与 symlink 指向的目标保留。
func TestDeleteJobCleansOrphanedTransferTemps(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	input := env.validInput(t, "cleanup")
	job, err := env.service.Create(ctx, input)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	seedTransferTemps(t, job.LocalRoot)

	if err := env.service.Delete(ctx, job.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	entries, err := os.ReadDir(job.LocalRoot)
	if err != nil {
		t.Fatal(err)
	}
	names := make(map[string]bool, len(entries))
	for _, e := range entries {
		names[e.Name()] = true
	}
	if len(names) != 1 || !names["user-file.txt"] {
		t.Fatalf("root entries after delete = %v, want only user file", names)
	}
	// symlink 只摘链接本身：链接指向的用户文件原样保留。
	if _, err := os.Stat(filepath.Join(job.LocalRoot, ".tinysync-part-0123456789ab")); !os.IsNotExist(err) {
		t.Errorf("legacy temp survived orphan cleanup (stat err=%v)", err)
	}
}

// RemoveTransferTemps 的形态矩阵：regular / symlink / 空目录形态的
// 断点文件回收，非空目录、非断点 symlink、用户文件保留；symlink
// unlink 不跟随目标。
func TestRemoveTransferTempsEntryShapes(t *testing.T) {
	root := t.TempDir()
	victim := filepath.Join(root, "victim.txt")
	if err := os.WriteFile(victim, []byte("target content"), 0o644); err != nil {
		t.Fatal(err)
	}
	shapes := []struct {
		name  string
		build func(string) error
		keep  bool
	}{
		{"user-file.txt", func(p string) error { return os.WriteFile(p, []byte("x"), 0o644) }, true},
		{".tinysync-part-0123456789ab", func(p string) error { return os.WriteFile(p, []byte("x"), 0o644) }, false},
		{".tinysync-part-v1-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", func(p string) error { return os.WriteFile(p, []byte("x"), 0o644) }, false},
		{".tinysync-part-v1-cccccccccccccccccccccccccccccccc-dddddddddddddddddddddddddddddddd", func(p string) error { return os.Symlink(victim, p) }, false},
		{"other-link.txt", func(p string) error { return os.Symlink(victim, p) }, true},
		{".tinysync-part-v1-eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee-ffffffffffffffffffffffffffffffff", func(p string) error { return os.Mkdir(p, 0o755) }, false},
		{".tinysync-part-v1-99999999999999999999999999999999-88888888888888888888888888888888", func(p string) error { return os.MkdirAll(filepath.Join(p, "nested"), 0o755) }, true},
	}
	for _, s := range shapes {
		if err := s.build(filepath.Join(root, s.name)); err != nil {
			t.Fatalf("seed %s: %v", s.name, err)
		}
	}
	removed, err := syncjob.RemoveTransferTemps(root)
	if err != nil {
		t.Fatalf("RemoveTransferTemps: %v", err)
	}
	if removed != 4 {
		t.Errorf("removed = %d, want 4 (legacy + partial + partial symlink + empty partial dir)", removed)
	}
	for _, s := range shapes {
		_, statErr := os.Lstat(filepath.Join(root, s.name))
		if s.keep && statErr != nil {
			t.Errorf("entry %s was removed, want kept", s.name)
		}
		if !s.keep && !os.IsNotExist(statErr) {
			t.Errorf("entry %s survived, want removed (stat err=%v)", s.name, statErr)
		}
	}
	// symlink 指向的目标不受影响。
	if _, err := os.Stat(victim); err != nil {
		t.Errorf("symlink target was affected: %v", err)
	}
}

// LocalRoot 变更：旧 root 的传输中间文件立即回收，新 root 与用户文件
// 不受影响。managed metadata 已随 mapping 变更重置（既有语义）。
func TestUpdateLocalRootCleansOldRootTransferTemps(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	input := env.validInput(t, "rebase")
	job, err := env.service.Create(ctx, input)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	seedTransferTemps(t, job.LocalRoot)
	newRoot := t.TempDir()
	// canonical LocalRoot 经 EvalSymlinks（macOS /var → /private/var）。
	resolved, err := filepath.EvalSymlinks(newRoot)
	if err != nil {
		t.Fatal(err)
	}
	newRoot = resolved

	newLocalRoot := newRoot
	updated, err := env.service.Update(ctx, job.ID, syncjob.UpdateInput{LocalRoot: &newLocalRoot})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if updated.LocalRoot != newRoot {
		t.Fatalf("updated LocalRoot = %q, want %q", updated.LocalRoot, newRoot)
	}
	entries, err := os.ReadDir(job.LocalRoot)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if syncjob.IsPartialName(e.Name()) && (e.Type().IsRegular() || e.Type()&os.ModeSymlink != 0) {
			t.Errorf("orphaned partial survived in old root: %s", e.Name())
		}
		if e.Name() == ".tinysync-part-0123456789ab" {
			t.Errorf("legacy temp survived in old root")
		}
	}
	if _, err := os.Stat(filepath.Join(newRoot, "user-file.txt")); !os.IsNotExist(err) {
		t.Errorf("new root unexpectedly populated (stat err=%v)", err)
	}
}

// 非 mapping 变更的 Update 不触碰 LocalRoot 中的中间文件。
func TestUpdateNonMappingKeepsTransferTemps(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	input := env.validInput(t, "keep")
	job, err := env.service.Create(ctx, input)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	seedTransferTemps(t, job.LocalRoot)

	enabled := false
	if _, err := env.service.Update(ctx, job.ID, syncjob.UpdateInput{Enabled: &enabled}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if _, err := os.Stat(filepath.Join(job.LocalRoot, ".tinysync-part-0123456789ab")); err != nil {
		t.Errorf("legacy temp removed by non-mapping update: %v", err)
	}
}

// RemoveTransferTemps 对不存在的 root 静默跳过（Job 从未运行）。
func TestRemoveTransferTempsMissingRoot(t *testing.T) {
	removed, err := syncjob.RemoveTransferTemps(filepath.Join(t.TempDir(), "missing"))
	if err != nil {
		t.Fatalf("RemoveTransferTemps on missing root = %v, want nil", err)
	}
	if removed != 0 {
		t.Errorf("removed = %d, want 0", removed)
	}
}

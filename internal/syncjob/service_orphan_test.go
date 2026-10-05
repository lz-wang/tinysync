package syncjob_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"tinysync/internal/syncjob"
)

// 写入 legacy 随机临时文件与 v1 断点文件两种形态的中间文件。
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
	// 断点形态的 symlink 条目不是 regular file：清理不触碰（留给
	// Downloader 的 validatePartial fail-closed）。
	if err := os.Symlink(legacy, filepath.Join(root, ".tinysync-part-v1-cccccccccccccccccccccccccccccccc-dddddddddddddddddddddddddddddddddd")); err != nil {
		t.Fatal(err)
	}
	normal := filepath.Join(root, "user-file.txt")
	if err := os.WriteFile(normal, []byte("keep me"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// 删除 Job：旧 LocalRoot 退出配置面后，其中的传输中间文件（legacy
// 临时文件与 v1 断点文件）立即回收——启动期清理不再枚举这个 root，
// partialRetention 对此类 mapping 变更孤儿实际无效。用户文件保留，
// 非 regular 的断点形态条目（symlink）不触碰。
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
	if len(names) != 2 {
		t.Fatalf("root entries after delete = %v, want only user file + symlink", names)
	}
	if !names["user-file.txt"] {
		t.Errorf("user file was removed by orphan cleanup")
	}
	if !names[".tinysync-part-v1-cccccccccccccccccccccccccccccccc-dddddddddddddddddddddddddddddddddd"] {
		t.Errorf("non-regular partial entry was removed by orphan cleanup")
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
		if syncjob.IsPartialName(e.Name()) && e.Type().IsRegular() {
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

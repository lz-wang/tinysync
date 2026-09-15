package syncjob

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"tinysync/internal/source"
)

// fp 构造指纹 helper。
func fp(size int64, etag string) source.Fingerprint {
	return source.Fingerprint{Size: size, ModifiedAt: time.Unix(1757879400, 0).UTC(), ETag: etag}
}

// remoteFile 构造远端文件条目。
func remoteFile(rel string, size int64, etag string) source.FileInfo {
	return source.FileInfo{Path: "/" + rel, Fingerprint: fp(size, etag)}
}

// managedEntry 构造 managed 记录。
func managedEntry(remotePath string, size int64, etag string) ManagedFile {
	return ManagedFile{
		JobID:      "job_a",
		RemotePath: remotePath,
		State:      StateSynced,
		Remote:     fp(size, etag),
	}
}

// 新文件下载；指纹未变跳过；ETag 或 Size 变化更新。
func TestBuildPlanCreateUpdateSkip(t *testing.T) {
	remoteFiles := []source.FileInfo{
		remoteFile("new.txt", 10, "\"a\""),
		remoteFile("same.txt", 10, "\"b\""),
		remoteFile("changed.txt", 20, "\"c\""),
		remoteFile("etag-flip.txt", 10, "\"d2\""),
	}
	managed := []ManagedFile{
		managedEntry("/same.txt", 10, "\"b\""),
		managedEntry("/changed.txt", 10, "\"c\""),
		managedEntry("/etag-flip.txt", 10, "\"d1\""),
	}
	plan := BuildPlan(ModeMirror, "/", remoteFiles, setOf("new.txt", "same.txt", "changed.txt", "etag-flip.txt"), managed)

	if got := rels(plan.Downloads); len(got) != 1 || got[0] != "new.txt" {
		t.Errorf("Downloads = %v, want [new.txt]", got)
	}
	if got := rels(plan.Updates); len(got) != 2 || got[0] != "changed.txt" || got[1] != "etag-flip.txt" {
		t.Errorf("Updates = %v, want [changed.txt etag-flip.txt]", got)
	}
	if got := rels(plan.Skips); len(got) != 1 || got[0] != "same.txt" {
		t.Errorf("Skips = %v, want [same.txt]", got)
	}
	if len(plan.Relinquish) != 0 || len(plan.Deletes) != 0 {
		t.Errorf("Relinquish/Deletes = %v/%v, want empty", plan.Relinquish, plan.Deletes)
	}
}

// 远端消失：Copy 释放 metadata 保留本地；Mirror 删除 managed 本地文件。
func TestBuildPlanRemoteDeleted(t *testing.T) {
	managed := []ManagedFile{
		managedEntry("/gone.txt", 10, "\"a\""),
	}
	// RemoteRoot 下已无任何文件。
	plan := BuildPlan(ModeCopy, "/", nil, setOf(), managed)
	if len(plan.Relinquish) != 1 || plan.Relinquish[0] != "/gone.txt" {
		t.Errorf("Copy Relinquish = %v, want [/gone.txt]", plan.Relinquish)
	}
	if len(plan.Deletes) != 0 {
		t.Errorf("Copy Deletes = %v, want empty", plan.Deletes)
	}

	plan = BuildPlan(ModeMirror, "/", nil, setOf(), managed)
	if len(plan.Deletes) != 1 || plan.Deletes[0] != "/gone.txt" {
		t.Errorf("Mirror Deletes = %v, want [/gone.txt]", plan.Deletes)
	}
	if len(plan.Relinquish) != 0 {
		t.Errorf("Mirror Relinquish = %v, want empty", plan.Relinquish)
	}
}

// selector 排除仍存在的远端文件：释放 metadata、保留本地，
// Copy 与 Mirror 行为一致——文件还在远端，不构成删除理由。
func TestBuildPlanSelectorExcluded(t *testing.T) {
	remoteFiles := []source.FileInfo{remoteFile("dropped.txt", 10, "\"a\"")}
	managed := []ManagedFile{managedEntry("/dropped.txt", 10, "\"a\"")}

	plan := BuildPlan(ModeMirror, "/", remoteFiles, setOf(), managed)
	if len(plan.Relinquish) != 1 || plan.Relinquish[0] != "/dropped.txt" {
		t.Errorf("Relinquish = %v, want [/dropped.txt]", plan.Relinquish)
	}
	if len(plan.Deletes) != 0 {
		t.Errorf("Deletes = %v, want empty (file still exists remotely)", plan.Deletes)
	}

	// 未 managed 的排除文件：无任何动作。
	plan = BuildPlan(ModeMirror, "/", remoteFiles, setOf(), nil)
	if plan.HasWork() {
		t.Errorf("unmanaged excluded file produced plan %+v, want no-op", plan)
	}
}

// 计划输出按 rel path 确定性排序，不依赖输入顺序。
func TestBuildPlanDeterministicOrder(t *testing.T) {
	remoteFiles := []source.FileInfo{
		remoteFile("c.txt", 1, "\"a\""),
		remoteFile("a.txt", 1, "\"a\""),
		remoteFile("b.txt", 1, "\"a\""),
	}
	plan := BuildPlan(ModeCopy, "/", remoteFiles, setOf("a.txt", "b.txt", "c.txt"), nil)
	got := rels(plan.Downloads)
	if len(got) != 3 || got[0] != "a.txt" || got[1] != "b.txt" || got[2] != "c.txt" {
		t.Errorf("Downloads = %v, want sorted [a.txt b.txt c.txt]", got)
	}
}

// 指纹判定优先级：Version > Checksum > ETag+Size > Size+ModifiedAt；
// ETag 是 opaque token，只比较相等性。
func TestFingerprintChanged(t *testing.T) {
	base := fp(10, "\"a\"")
	baseVersion := base
	baseVersion.Version = "v1"

	cases := []struct {
		name  string
		now   source.Fingerprint
		known source.Fingerprint
		want  bool
	}{
		{name: "identical", now: base, known: base, want: false},
		{name: "version differs", now: base, known: baseVersion, want: true},
		{name: "checksum differs", now: base, known: fp2(base, "sum1", ""), want: true},
		{name: "etag differs", now: fp(10, "\"z\""), known: base, want: true},
		{name: "size differs", now: fp(11, "\"a\""), known: base, want: true},
		{name: "mtime differs no etag", now: source.Fingerprint{Size: 10, ModifiedAt: time.Unix(1, 0)}, known: source.Fingerprint{Size: 10, ModifiedAt: time.Unix(2, 0)}, want: true},
		{name: "etag only on one side", now: fp(10, "\"a\""), known: source.Fingerprint{Size: 10}, want: true},
		{name: "no comparable info", now: source.Fingerprint{}, known: source.Fingerprint{}, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := fingerprintChanged(tc.now, tc.known); got != tc.want {
				t.Errorf("fingerprintChanged = %v, want %v", got, tc.want)
			}
		})
	}
}

// fp2 在指纹基础上设置 checksum / version。
func fp2(base source.Fingerprint, checksum, version string) source.Fingerprint {
	out := base
	out.Checksum = checksum
	out.Version = version
	return out
}

// preflight 把 download 目标被未知本地文件、目录或 symlink 占用的情况
// 全部标记为冲突；空闲目标无冲突。
func TestPreflightLocal(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "occupied-dir"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "unknown.txt"), []byte("keep"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.Symlink(filepath.Join(root, "unknown.txt"), filepath.Join(root, "link.txt")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	plan := Plan{
		Downloads: []planEntry{
			{relPath: "unknown.txt"},
			{relPath: "occupied-dir"},
			{relPath: "link.txt"},
			{relPath: "free/new.txt"},
		},
	}
	conflicts, err := PreflightLocal(root, plan)
	if err != nil {
		t.Fatalf("PreflightLocal: %v", err)
	}
	if len(conflicts) != 3 {
		t.Fatalf("conflicts = %v, want 3 entries", conflicts)
	}
	for _, rel := range []string{"unknown.txt", "occupied-dir", "link.txt"} {
		if _, ok := conflicts[rel]; !ok {
			t.Errorf("conflict %s missing, got %v", rel, conflicts)
		}
	}
	if _, ok := conflicts["free/new.txt"]; ok {
		t.Error("free target wrongly flagged as conflict")
	}
}

// rels 提取计划条目的路径列表。
func rels(entries []planEntry) []string {
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.relPath)
	}
	return out
}

// setOf 构造 selector 命中集合。
func setOf(rels ...string) map[string]bool {
	m := make(map[string]bool, len(rels))
	for _, r := range rels {
		m[r] = true
	}
	return m
}

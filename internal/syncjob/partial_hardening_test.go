package syncjob

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"tinysync/internal/source"
)

// —— deterministic partial 的文件系统 hardening（ADR 0010）：可预测的
// partial 路径在验证与打开之间存在被替换成 symlink 的竞争窗口，打开
// 顺序必须保证任何截断 / 写入都发生在「句柄 = 刚验证的 regular
// partial」确认之后。

// truncate 打开拒绝 symlink：竞争窗口内路径被替换成 symlink 时，
// O_EXCL 失败进入「打开已有条目」分支，verifyPartialHandle 必须在
// Truncate 之前拒绝——symlink 目标文件绝不被截断。
func TestOpenPartialForAppendTruncateRefusesSymlink(t *testing.T) {
	root := t.TempDir()
	victim := filepath.Join(root, "victim.txt")
	if err := os.WriteFile(victim, []byte("precious service data"), 0o644); err != nil {
		t.Fatal(err)
	}
	partial := filepath.Join(root, partialName("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"))
	if err := os.Symlink(victim, partial); err != nil {
		t.Fatal(err)
	}
	if _, err := openPartialForAppend(partial, true); err == nil {
		t.Fatal("openPartialForAppend on symlink = nil error, want refusal")
	}
	data, err := os.ReadFile(victim)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "precious service data" {
		t.Fatalf("symlink target was truncated: %q", data)
	}
}

// 续传打开同样拒绝 symlink：O_WRONLY 会跟随 symlink 打开目标，
// append 到外部文件同样是静默内容错误。
func TestOpenPartialForAppendResumeRefusesSymlink(t *testing.T) {
	root := t.TempDir()
	victim := filepath.Join(root, "victim.txt")
	if err := os.WriteFile(victim, []byte("precious service data"), 0o644); err != nil {
		t.Fatal(err)
	}
	partial := filepath.Join(root, partialName("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"))
	if err := os.Symlink(victim, partial); err != nil {
		t.Fatal(err)
	}
	if _, err := openPartialForAppend(partial, false); err == nil {
		t.Fatal("resume openPartialForAppend on symlink = nil error, want refusal")
	}
	data, err := os.ReadFile(victim)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "precious service data" {
		t.Fatalf("symlink target was appended: %q", data)
	}
}

// 正常路径：新建 exclusive 创建空文件；已有 regular partial 打开后
// 截断为空（truncate）或原样打开（续传）。
func TestOpenPartialForAppendHappyPaths(t *testing.T) {
	root := t.TempDir()
	partial := filepath.Join(root, partialName("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"))

	f, err := openPartialForAppend(partial, true)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(partial, []byte("stale prefix"), 0o644); err != nil {
		t.Fatal(err)
	}

	// truncate：已有文件身份确认后截断为空。
	f, err = openPartialForAppend(partial, true)
	if err != nil {
		t.Fatalf("recreate over existing: %v", err)
	}
	info, err := f.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() != 0 {
		t.Fatalf("truncated partial size = %d, want 0", info.Size())
	}
	_ = f.Close()

	// 续传：原样打开，内容保持。
	f, err = openPartialForAppend(partial, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("kept prefix"); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	f, err = openPartialForAppend(partial, false)
	if err != nil {
		t.Fatalf("resume open: %v", err)
	}
	info, err = f.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() != int64(len("kept prefix")) {
		t.Fatalf("resume partial size = %d, want %d", info.Size(), len("kept prefix"))
	}
	_ = f.Close()
}

// alignTo 检测 Stat → hash 之间的截短：文件实际字节数小于调用方
// Stat 到的 size 时报错，hash 状态保持旧值——绝不假装 covered ==
// size 留下前缀 hole（无 checksum 协议上的静默拼接）。
func TestPartialHasherAlignToDetectsShrink(t *testing.T) {
	root := t.TempDir()
	partial := filepath.Join(root, "p.bin")
	content := strings.Repeat("x", 100)
	if err := os.WriteFile(partial, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	verifier, err := newChecksumVerifier("")
	if err != nil {
		t.Fatal(err)
	}
	p := &partialHasher{path: partial, verifier: verifier}

	// 先推进到 50（正常），再模拟 Stat 与 hash 之间被截短到 30：
	// alignTo(100) 读取时只能拿到 30 字节。
	if err := p.alignTo(50); err != nil {
		t.Fatalf("alignTo(50): %v", err)
	}
	if err := os.Truncate(partial, 30); err != nil {
		t.Fatal(err)
	}
	err = p.alignTo(100)
	if err == nil {
		t.Fatal("alignTo past shrunken partial = nil, want error")
	}
	if !strings.Contains(err.Error(), "shrank") {
		t.Fatalf("error = %v, want shrink detection", err)
	}
	if p.covered != 50 {
		t.Fatalf("covered = %d after failed align, want unchanged 50", p.covered)
	}
	// 状态未被污染：重新对齐到真实长度后 hash 仍可用。
	if err := p.alignTo(30); err != nil {
		t.Fatalf("re-align to 30: %v", err)
	}
	if p.covered != 30 {
		t.Fatalf("covered = %d, want 30", p.covered)
	}
}

// 远端条目落在内部断点文件命名空间：计划即拒绝，记 skipped 明细，
// 本地不落地——否则同步产物会进入启动期 orphan GC 的删除范围。
func TestRunSkipsReservedPartialNamespace(t *testing.T) {
	f := newEngineFixture(t, ModeMirror)
	name := partialName("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
	remote := buildRemote(map[string]string{
		"/docs/a.txt":   "v1",
		"/docs/" + name: "namespace squatter",
	}, []string{"/docs"})

	items := &memItems{}
	stats, err := f.runWith(remote, func(o *RunOptions) { o.Items = items })
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(f.root, "docs", name)); !os.IsNotExist(statErr) {
		t.Fatalf("reserved namespace entry landed on disk (stat err=%v)", statErr)
	}
	if stats.FilesCreated != 1 {
		t.Errorf("FilesCreated = %d, want 1 (only a.txt)", stats.FilesCreated)
	}
	if stats.FilesSkipped != 1 {
		t.Errorf("FilesSkipped = %d, want 1 (reserved namespace)", stats.FilesSkipped)
	}
	var found bool
	for _, item := range items.all() {
		if item.Path == "docs/"+name && item.Status == ItemSkipped {
			found = true
		}
	}
	if !found {
		t.Errorf("no skipped item recorded for reserved namespace entry: %+v", items.all())
	}
}

// 已在 managed 中的 namespace 条目（历史遗留）本地缺失时同样拒绝
// 修复下载：pending 重传路径与全新下载路径都被计划拒绝。
func TestRunSkipsReservedPartialNamespaceOnRepair(t *testing.T) {
	f := newEngineFixture(t, ModeMirror)
	name := partialName("cccccccccccccccccccccccccccccccc", "dddddddddddddddddddddddddddddddd")
	remotePath := "/docs/" + name
	remote := buildRemote(map[string]string{"/docs/" + name: "legacy squatter"}, []string{"/docs"})

	// 预置 synced managed 记录 + 删除本地文件 → 修复下载路径。
	mtime := int64(1757879400000000000)
	size := int64(len("legacy squatter"))
	if err := f.managed.Upsert(context.Background(), []ManagedFile{{
		JobID:        f.job.ID,
		RemotePath:   remotePath,
		LocalRelPath: "docs/" + name,
		State:        StateSynced,
		Remote:       source.Fingerprint{Size: size, ModifiedAt: time.Unix(1757879400, 0).UTC(), ETag: etagOf("legacy squatter")},
		LocalSize:    &size,
		LocalMtimeNs: &mtime,
	}}); err != nil {
		t.Fatal(err)
	}

	items := &memItems{}
	stats, err := f.runWith(remote, func(o *RunOptions) { o.Items = items })
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(f.root, "docs", name)); !os.IsNotExist(statErr) {
		t.Fatalf("reserved namespace repair landed on disk (stat err=%v)", statErr)
	}
	if stats.FilesCreated != 0 {
		t.Errorf("FilesCreated = %d, want 0", stats.FilesCreated)
	}
}

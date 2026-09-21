package s3

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"tinysync/internal/source"
	"tinysync/internal/syncjob"
)

// collectTree 扫描 root 并收集 visit 的全部条目（path → IsDir）。
func collectTree(t *testing.T, r source.Remote, root string) (map[string]bool, error) {
	t.Helper()
	visited := map[string]bool{}
	err := r.(source.TreeScanner).ScanTree(context.Background(), root, func(fi source.FileInfo) error {
		if visited[fi.Path] {
			t.Errorf("entry %s visited twice", fi.Path)
		}
		visited[fi.Path] = fi.IsDir
		return nil
	})
	return visited, err
}

// ScanTree flat 10k：分页完整，请求量只随对象页数增长（10000 /
// 1000 = 10 次 ListObjectsV2）。
func TestS3ScanTreeFlatPaginationComplete(t *testing.T) {
	if testing.Short() {
		t.Skip("10k dataset is expensive for -short")
	}
	mod := time.Unix(1757879400, 0).UTC()
	fk := newFakeS3(1000)
	const total = 10000
	for i := 0; i < total; i++ {
		fk.put(fmt.Sprintf("base/f%06d", i), []byte("x"), mod, "")
	}
	r := newTestRemote(fk, "base")

	visited, err := collectTree(t, r, "/")
	if err != nil {
		t.Fatalf("ScanTree: %v", err)
	}
	files := 0
	for path, isDir := range visited {
		if !isDir {
			files++
		} else if path != "/base" {
			t.Errorf("unexpected directory %s", path)
		}
	}
	if files != total {
		t.Fatalf("files = %d, want %d (no duplicates, no gaps)", files, total)
	}
	if got := fk.listRequests.Load(); got != 10 {
		t.Errorf("ListObjectsV2 requests = %d, want 10 (page-count dominated)", got)
	}
}

// ScanTree 1000 目录 × 10 对象：请求量与页数线性相关，与目录数无关。
func TestS3ScanTreeDeepRequestCount(t *testing.T) {
	mod := time.Unix(1757879400, 0).UTC()
	fk := newFakeS3(1000)
	const dirs = 1000
	const perDir = 10
	for d := 0; d < dirs; d++ {
		for i := 0; i < perDir; i++ {
			fk.put(fmt.Sprintf("base/d%04d/f%02d", d, i), []byte("x"), mod, "")
		}
	}
	r := newTestRemote(fk, "base")

	visited, err := collectTree(t, r, "/")
	if err != nil {
		t.Fatalf("ScanTree: %v", err)
	}
	files := 0
	dirsSeen := 0
	for path, isDir := range visited {
		if isDir {
			dirsSeen++
		} else {
			files++
		}
		_ = path
	}
	if files != dirs*perDir {
		t.Errorf("files = %d, want %d", files, dirs*perDir)
	}
	// prefix "base/" 剥离后，logical 目录即 1000 个 /dNNNN 子目录
	// （base 前缀不出现在 logical 中）。
	if dirsSeen != dirs {
		t.Errorf("dirs = %d, want %d", dirsSeen, dirs)
	}
	// 10000 对象 / 1000 每页 = 10 页；远小于目录数主导的 ~1010 次。
	if got := fk.listRequests.Load(); got != 10 {
		t.Errorf("ListObjectsV2 requests = %d, want 10 (not directory-count dominated)", got)
	}
}

// source prefix 边界：prefix 之外的对象绝不泄漏。
func TestS3ScanTreePrefixBoundary(t *testing.T) {
	mod := time.Unix(1757879400, 0).UTC()
	fk := newFakeS3(0)
	fk.put("base/a.txt", []byte("x"), mod, "")
	fk.put("base/sub/inner.txt", []byte("x"), mod, "")
	fk.put("outside/secret.txt", []byte("x"), mod, "")
	fk.put("baseother/x", []byte("x"), mod, "") // prefix 歧义对象
	r := newTestRemote(fk, "base")

	visited, err := collectTree(t, r, "/")
	if err != nil {
		t.Fatalf("ScanTree: %v", err)
	}
	for path := range visited {
		if strings.HasPrefix(path, "/outside") || strings.HasPrefix(path, "/baseother") {
			t.Errorf("entry %s escaped source prefix", path)
		}
	}
	for _, file := range []string{"/a.txt", "/sub/inner.txt"} {
		if isDir, ok := visited[file]; !ok {
			t.Errorf("file %s missing, entries = %v", file, visited)
		} else if isDir {
			t.Errorf("file %s visited as directory", file)
		}
	}
	if _, ok := visited["/sub"]; !ok {
		t.Error("directory /sub not derived")
	}
}

// Job RemoteRoot：ScanTree 只返回子树，root 自身不 visit。
func TestS3ScanTreeRemoteRootSubtree(t *testing.T) {
	mod := time.Unix(1757879400, 0).UTC()
	fk := newFakeS3(0)
	fk.put("base/sub/a.txt", []byte("x"), mod, "")
	fk.put("base/sub/deep/b.txt", []byte("x"), mod, "")
	fk.put("base/other.txt", []byte("x"), mod, "")
	r := newTestRemote(fk, "base")

	visited, err := collectTree(t, r, "/sub")
	if err != nil {
		t.Fatalf("ScanTree: %v", err)
	}
	if visited["/sub"] || visited["/"] {
		t.Error("root itself must not be visited")
	}
	// 文件与目录条目都要出现且类型正确。
	for _, file := range []string{"/sub/a.txt", "/sub/deep/b.txt"} {
		if isDir, ok := visited[file]; !ok {
			t.Errorf("file %s missing", file)
		} else if isDir {
			t.Errorf("file %s visited as directory", file)
		}
	}
	if isDir, ok := visited["/sub/deep"]; !ok || !isDir {
		t.Errorf("directory /sub/deep = %v,%v; want present dir", isDir, ok)
	}
	if _, leak := visited["/other.txt"]; leak {
		t.Error("entry outside RemoteRoot leaked")
	}
}

// RemoteRoot 自身的 folder marker（Mkdir 写入的 root/ 占位对象会
// 落进 root prefix 的 flat 扫描结果）：ScanTree 跳过 root 自身，不
// 把 marker 推导成 "/root/"（会被 ValidateLogicalPath 拒绝、令整个
// Job 失败）；子树文件照常完整返回。
func TestS3ScanTreeSkipsRemoteRootMarker(t *testing.T) {
	mod := time.Unix(1757879400, 0).UTC()
	fk := newFakeS3(0)
	fk.put("base/sub/", nil, mod, "")
	fk.put("base/sub/a.txt", []byte("x"), mod, "")
	fk.put("base/sub/deep/b.txt", []byte("x"), mod, "")
	fk.put("base/other.txt", []byte("x"), mod, "")
	r := newTestRemote(fk, "base")

	visited, err := collectTree(t, r, "/sub")
	if err != nil {
		t.Fatalf("ScanTree: %v", err)
	}
	if visited["/sub"] || visited["/sub/"] {
		t.Error("root marker itself must not be visited")
	}
	for _, file := range []string{"/sub/a.txt", "/sub/deep/b.txt"} {
		if isDir, ok := visited[file]; !ok {
			t.Errorf("file %s missing, entries = %v", file, visited)
		} else if isDir {
			t.Errorf("file %s visited as directory", file)
		}
	}
	if isDir, ok := visited["/sub/deep"]; !ok || !isDir {
		t.Errorf("directory /sub/deep = %v,%v; want present dir", isDir, ok)
	}
	if _, leak := visited["/other.txt"]; leak {
		t.Error("entry outside RemoteRoot leaked")
	}

	// ScanRemote（fast path 与 ScanTree 共用同一 collector）：
	// marker root 不再令 Job 失败，快照恰好 2 个文件。
	files, err := syncjob.ScanRemote(context.Background(), r, "/sub")
	if err != nil {
		t.Fatalf("ScanRemote: %v", err)
	}
	if len(files) != 2 {
		t.Fatalf("ScanRemote files = %v, want 2", files)
	}
}

// folder marker：目录而非文件；空目录 marker 不产生文件条目。
func TestS3ScanTreeFolderMarkers(t *testing.T) {
	mod := time.Unix(1757879400, 0).UTC()
	fk := newFakeS3(0)
	fk.put("base/empty/", nil, mod, "")
	fk.put("base/photos/2026/", nil, mod, "")
	fk.put("base/photos/2026/a.jpg", []byte("jpeg"), mod, "")
	r := newTestRemote(fk, "base")

	visited, err := collectTree(t, r, "/")
	if err != nil {
		t.Fatalf("ScanTree: %v", err)
	}
	for _, dir := range []string{"/empty", "/photos", "/photos/2026"} {
		if isDir, ok := visited[dir]; !ok || !isDir {
			t.Errorf("directory %s = %v,%v; want present dir", dir, isDir, ok)
		}
	}
	if isDir, ok := visited["/photos/2026/a.jpg"]; !ok {
		t.Error("file under marker directories missing")
	} else if isDir {
		t.Error("file visited as directory")
	}
	for path, isDir := range visited {
		if strings.HasSuffix(path, "/") {
			t.Errorf("marker path %s leaked with trailing slash", path)
		}
		_ = isDir
	}
	if visited["/photos/2026/a.jpg/"] {
		t.Error("file path corrupted")
	}
}

// file/dir collision（foo 与 foo/bar.txt 共存）：虚拟目录 visit 让
// collector 在本地 mutation 前 fail-fast。
func TestS3ScanTreeCollisionFailFast(t *testing.T) {
	mod := time.Unix(1757879400, 0).UTC()
	fk := newFakeS3(0)
	fk.put("base/foo", []byte("file"), mod, "")
	fk.put("base/foo/bar.txt", []byte("x"), mod, "")
	r := newTestRemote(fk, "base")

	// collision 由扫描 collector 检出（syncjob 的 ErrInvalid 语义）。
	if _, err := syncjob.ScanRemote(context.Background(), r, "/"); !errors.Is(err, syncjob.ErrInvalid) {
		t.Fatalf("collision scan error = %v, want syncjob.ErrInvalid", err)
	}
}

// folder marker 场景下整体扫描成功（Mirror 不会把 marker 当文件
// 下载）；空目录在快照中零文件。
func TestS3ScanTreeEmptyDirYieldsNoFiles(t *testing.T) {
	mod := time.Unix(1757879400, 0).UTC()
	fk := newFakeS3(0)
	fk.put("base/empty/", nil, mod, "")
	r := newTestRemote(fk, "base")

	files, err := syncjob.ScanRemote(context.Background(), r, "/")
	if err != nil {
		t.Fatalf("ScanRemote: %v", err)
	}
	if len(files) != 0 {
		t.Errorf("files = %v, want none for empty dir marker", files)
	}
}

// ContinuationToken 分页：无重复、无遗漏。
func TestS3ScanTreeContinuationNoDupNoGap(t *testing.T) {
	mod := time.Unix(1757879400, 0).UTC()
	fk := newFakeS3(3) // 每页 3 条
	const total = 10
	for i := 0; i < total; i++ {
		fk.put(fmt.Sprintf("base/f%02d", i), []byte("x"), mod, "")
	}
	r := newTestRemote(fk, "base")

	visited, err := collectTree(t, r, "/")
	if err != nil {
		t.Fatalf("ScanTree: %v", err)
	}
	var got []string
	for path, isDir := range visited {
		if !isDir {
			got = append(got, path)
		}
	}
	if len(got) != total {
		t.Fatalf("files = %d, want %d", len(got), total)
	}
	sort.Strings(got)
	for i := 0; i < total; i++ {
		want := fmt.Sprintf("/f%02d", i)
		if got[i] != want {
			t.Fatalf("entry %d = %s, want %s (ordered, no dups/gaps)", i, got[i], want)
		}
	}
}

// ETag opaque 原样保留；Size / LastModified 保留。
func TestS3ScanTreeFingerprintPreserved(t *testing.T) {
	mod := time.Unix(1757879400, 0).UTC()
	fk := newFakeS3(0)
	fk.put("base/data.bin", []byte("0123456789"), mod, `"opaque-etag-xyz"`)
	r := newTestRemote(fk, "base")

	var fi source.FileInfo
	err := r.(source.TreeScanner).ScanTree(context.Background(), "/", func(entry source.FileInfo) error {
		if !entry.IsDir {
			fi = entry
		}
		return nil
	})
	if err != nil {
		t.Fatalf("ScanTree: %v", err)
	}
	if fi.Path != "/data.bin" {
		t.Fatalf("path = %s, want /data.bin", fi.Path)
	}
	if fi.Fingerprint.ETag != `"opaque-etag-xyz"` {
		t.Errorf("etag = %q, want opaque echo", fi.Fingerprint.ETag)
	}
	if fi.Fingerprint.Size != 10 {
		t.Errorf("size = %d, want 10", fi.Fingerprint.Size)
	}
	if !fi.Fingerprint.ModifiedAt.Equal(mod) {
		t.Errorf("mtime = %v, want %v", fi.Fingerprint.ModifiedAt, mod)
	}
}

// cancellation 立即停止；callback 错误原样透传并中止。
func TestS3ScanTreeAbortSemantics(t *testing.T) {
	mod := time.Unix(1757879400, 0).UTC()
	fk := newFakeS3(0)
	fk.put("base/a.txt", []byte("x"), mod, "")
	r := newTestRemote(fk, "base")

	t.Run("ContextCanceled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		err := r.(source.TreeScanner).ScanTree(ctx, "/", func(fi source.FileInfo) error { return nil })
		if !errors.Is(err, context.Canceled) {
			t.Errorf("ScanTree canceled = %v, want context.Canceled", err)
		}
	})

	t.Run("CallbackError", func(t *testing.T) {
		sentinel := errors.New("stop scan")
		calls := 0
		err := r.(source.TreeScanner).ScanTree(context.Background(), "/", func(fi source.FileInfo) error {
			calls++
			return sentinel
		})
		if !errors.Is(err, sentinel) {
			t.Errorf("ScanTree callback error = %v, want sentinel", err)
		}
		if calls != 1 {
			t.Errorf("visit called %d times, want 1 (abort immediately)", calls)
		}
	})

	// visit 中途取消：条目循环逐条检查 ctx，同页剩余对象不再处理，
	// 取消不等下一页请求边界。
	t.Run("CancelDuringVisit", func(t *testing.T) {
		mod := time.Unix(1757879400, 0).UTC()
		fk := newFakeS3(0)
		const total = 100
		for i := 0; i < total; i++ {
			fk.put(fmt.Sprintf("base/f%03d", i), []byte("x"), mod, "")
		}
		r := newTestRemote(fk, "base")

		ctx, cancel := context.WithCancel(context.Background())
		visits := 0
		err := r.(source.TreeScanner).ScanTree(ctx, "/", func(fi source.FileInfo) error {
			visits++
			cancel()
			return nil
		})
		if !errors.Is(err, context.Canceled) {
			t.Errorf("ScanTree cancel-during-visit = %v, want context.Canceled", err)
		}
		if visits >= total {
			t.Errorf("visit called %d times, want < %d (stop within the entry loop)", visits, total)
		}
	})
}

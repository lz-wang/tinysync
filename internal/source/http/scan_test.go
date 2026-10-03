package http

import (
	"context"
	"errors"
	"io/fs"
	"sort"
	"strings"
	"testing"

	"tinysync/internal/source"
)

// seedTree 写入固定树：/a/（x.txt、y.txt）+ /b/z.txt + /top.txt。
func seedTree(f *fakeCaddyServer) {
	f.mkdir("/a")
	f.mkdir("/b")
	f.write("/a/x.txt", "x")
	f.write("/a/y.txt", "y")
	f.write("/b/z.txt", "z")
	f.write("/top.txt", "top")
}

// ScanTree 全量 visit 文件与目录（root 自身除外），每个目录恰好一次
// listing 请求。
func TestScanTreeCaddy(t *testing.T) {
	f := newFakeCaddyServer(t)
	seedTree(f)
	r, err := f.openRemote(source.HTTPListingCaddy)
	if err != nil {
		t.Fatalf("openRemote: %v", err)
	}
	defer func() { _ = r.Close() }()

	var paths []string
	err = r.(source.TreeScanner).ScanTree(context.Background(), "/", func(fi source.FileInfo) error {
		paths = append(paths, fi.Path)
		if fi.IsDir {
			return nil
		}
		if fi.Fingerprint.Size == 0 {
			t.Errorf("file %q fingerprint size = 0, want exact", fi.Path)
		}
		if fi.Fingerprint.ETag != "" {
			t.Errorf("json listing 不应为 ETag 发 HEAD：%q has etag", fi.Path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("ScanTree: %v", err)
	}
	sort.Strings(paths)
	want := []string{"/a", "/a/x.txt", "/a/y.txt", "/b", "/b/z.txt", "/top.txt"}
	if len(paths) != len(want) {
		t.Fatalf("visited = %v, want %v", paths, want)
	}
	for i := range want {
		if paths[i] != want[i] {
			t.Errorf("visited[%d] = %q, want %q", i, paths[i], want[i])
		}
	}
	for dir, n := range f.listings {
		if n != 1 {
			t.Errorf("directory %s listed %d times, want exactly 1", dir, n)
		}
	}
	if len(f.listings) != 3 {
		t.Errorf("listed dirs = %v, want root + /a + /b", f.listings)
	}
}

// 子树扫描：只 visit root 之下的条目。
func TestScanTreeSubtree(t *testing.T) {
	f := newFakeCaddyServer(t)
	seedTree(f)
	r, err := f.openRemote(source.HTTPListingCaddy)
	if err != nil {
		t.Fatalf("openRemote: %v", err)
	}
	defer func() { _ = r.Close() }()

	var paths []string
	err = r.(source.TreeScanner).ScanTree(context.Background(), "/a", func(fi source.FileInfo) error {
		paths = append(paths, fi.Path)
		return nil
	})
	if err != nil {
		t.Fatalf("ScanTree: %v", err)
	}
	sort.Strings(paths)
	if len(paths) != 2 || paths[0] != "/a/x.txt" || paths[1] != "/a/y.txt" {
		t.Errorf("visited = %v, want /a/x.txt /a/y.txt", paths)
	}
}

// Caddy file_limit 达到上限：ScanTree 整体失败（permanent），绝不
// 返回截断快照（Mirror 的删除授权依赖完整快照）。
func TestScanTreeCaddyFileLimitFailClosed(t *testing.T) {
	f := newFakeCaddyServer(t)
	seedTree(f)
	// 根目录恰好 3 个条目（a/ b/ top.txt）：limit=3 即触发 fail-closed。
	remote, err := NewFactory().Create(context.Background(), source.Source{
		Name: "limit",
		Type: source.TypeHTTP,
		Config: source.Config{HTTP: &source.HTTPConfig{
			BaseURL:        f.srv.URL + "/",
			ListingMode:    source.HTTPListingCaddy,
			CaddyFileLimit: 3,
		}},
	}, source.Credentials{})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	defer func() { _ = remote.Close() }()

	var visited int
	err = remote.(source.TreeScanner).ScanTree(context.Background(), "/", func(fi source.FileInfo) error {
		visited++
		return nil
	})
	if err == nil {
		t.Fatal("ScanTree with reached file_limit = nil, want failure")
	}
	if source.IsRetryable(err) {
		t.Errorf("file_limit error should be permanent, got %v", err)
	}
	if want := "file_limit=3"; !strings.Contains(err.Error(), want) {
		t.Errorf("error = %v, want containing %q", err, want)
	}
	if visited != 0 {
		t.Errorf("visited %d entries before failing, want 0", visited)
	}
	// limit=4 不触发（3 < 4）。
	remote4, err := NewFactory().Create(context.Background(), source.Source{
		Name: "limit4",
		Type: source.TypeHTTP,
		Config: source.Config{HTTP: &source.HTTPConfig{
			BaseURL:        f.srv.URL + "/",
			ListingMode:    source.HTTPListingCaddy,
			CaddyFileLimit: 4,
		}},
	}, source.Credentials{})
	if err != nil {
		t.Fatalf("Create limit4: %v", err)
	}
	defer func() { _ = remote4.Close() }()
	err = remote4.(source.TreeScanner).ScanTree(context.Background(), "/", func(source.FileInfo) error { return nil })
	if err != nil {
		t.Errorf("ScanTree with limit=4: %v, want success", err)
	}
}

// 部分失败等于整体失败：子目录 listing 500 时 ScanTree 返回 transient
// 错误，绝不返回部分结果。
func TestScanTreePartialFailureFailsWhole(t *testing.T) {
	f := newFakeCaddyServer(t)
	seedTree(f)
	f.mu.Lock()
	f.failDir = "/b"
	f.mu.Unlock()
	r, err := f.openRemote(source.HTTPListingCaddy)
	if err != nil {
		t.Fatalf("openRemote: %v", err)
	}
	defer func() { _ = r.Close() }()

	var visited []string
	err = r.(source.TreeScanner).ScanTree(context.Background(), "/", func(fi source.FileInfo) error {
		visited = append(visited, fi.Path)
		return nil
	})
	if err == nil {
		t.Fatal("ScanTree with failing subdir = nil, want failure")
	}
	if !source.IsRetryable(err) {
		t.Errorf("500 listing should be transient, got %v", err)
	}
}

// visit 错误原样透传并终止扫描。
func TestScanTreeVisitErrorPropagates(t *testing.T) {
	f := newFakeCaddyServer(t)
	seedTree(f)
	r, err := f.openRemote(source.HTTPListingCaddy)
	if err != nil {
		t.Fatalf("openRemote: %v", err)
	}
	defer func() { _ = r.Close() }()

	sentinel := errors.New("stop")
	err = r.(source.TreeScanner).ScanTree(context.Background(), "/", func(fi source.FileInfo) error {
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Errorf("ScanTree error = %v, want sentinel", err)
	}
}

// ctx 取消及时终止扫描。
func TestScanTreeContextCancel(t *testing.T) {
	f := newFakeCaddyServer(t)
	seedTree(f)
	r, err := f.openRemote(source.HTTPListingCaddy)
	if err != nil {
		t.Fatalf("openRemote: %v", err)
	}
	defer func() { _ = r.Close() }()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err = r.(source.TreeScanner).ScanTree(ctx, "/", func(source.FileInfo) error { return nil })
	if !errors.Is(err, context.Canceled) {
		t.Errorf("ScanTree canceled = %v, want context.Canceled", err)
	}
}

// auto 模式探测 JSON 字段集（caddy）并缓存：第二次 listing 不再依赖
// 探测路径。
func TestAutoDetectCaddy(t *testing.T) {
	f := newFakeCaddyServer(t)
	seedTree(f)
	r, err := f.openRemote(source.HTTPListingAuto)
	if err != nil {
		t.Fatalf("openRemote: %v", err)
	}
	defer func() { _ = r.Close() }()

	c := r.(*Client)
	if _, err := c.Stat(context.Background(), "/top.txt"); err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if c.cachedKind() != listingCaddyJSON {
		t.Errorf("cached kind = %s, want caddy-json", c.cachedKind())
	}
	// 缺失文件：not-exist 且 permanent。
	_, err = c.Stat(context.Background(), "/missing.txt")
	if !errors.Is(err, fs.ErrNotExist) || source.IsRetryable(err) {
		t.Errorf("Stat missing = %v, want permanent fs.ErrNotExist", err)
	}
}

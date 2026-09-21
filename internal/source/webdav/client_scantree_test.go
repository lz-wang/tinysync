package webdav

import (
	"context"
	"errors"
	"net/http/httptest"
	"os"
	"testing"

	xnetdav "golang.org/x/net/webdav"

	"tinysync/internal/source"
)

// ScanTree 测试与 benchmark 共用装置（见 benchmark_test.go）：
// countingHandler 统计 PROPFIND，断言「每个目录恰好一次枚举」这一
// 确定性调用次数，而不是只看 wall-clock。

// writeMemFS 经 MemFS 直接写入（测试装置不经被测 adapter），逐级
// 创建父目录。
func writeMemFS(tb testing.TB, fs xnetdav.FileSystem, logical, content string) {
	tb.Helper()
	ctx := context.Background()
	for i := 1; i < len(logical); i++ {
		if logical[i] == '/' {
			_ = fs.Mkdir(ctx, logical[:i], 0o755) // 已存在视为成功
		}
	}
	f, err := fs.OpenFile(ctx, logical, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		tb.Fatalf("dav write %s: %v", logical, err)
	}
	if _, err := f.Write([]byte(content)); err != nil {
		tb.Fatalf("dav write %s: %v", logical, err)
	}
	if err := f.Close(); err != nil {
		tb.Fatalf("dav close %s: %v", logical, err)
	}
}

// newCountingServer 把协议 handler 包上 PROPFIND 计数并启动 httptest。
func newCountingServer(tb testing.TB, next *xnetdav.Handler) (*countingHandler, *httptest.Server) {
	tb.Helper()
	handler := &countingHandler{next: next}
	srv := httptest.NewServer(handler)
	tb.Cleanup(srv.Close)
	return handler, srv
}

// newRootedFactoryRemote 经生产 Factory 构造带 RemoteRoot 的 Remote。
func newRootedFactoryRemote(tb testing.TB, endpoint, remoteRoot string) source.Remote {
	tb.Helper()
	f := NewFactory()
	remote, err := f.Create(context.Background(), source.Source{
		Type: source.TypeWebDAV,
		Config: source.Config{
			WebDAV: &source.WebDAVConfig{Endpoint: endpoint, RemoteRoot: remoteRoot},
		},
	}, source.Credentials{})
	if err != nil {
		tb.Fatalf("create remote: %v", err)
	}
	tb.Cleanup(func() { _ = remote.Close() })
	return remote
}

// TestScanTreeFlatDirectorySinglePropfind：10,000 文件单目录的同步
// 扫描必须恰好一次 PROPFIND——这是伪分页修复的核心断言（修复前
// ≈100 次）。
func TestScanTreeFlatDirectorySinglePropfind(t *testing.T) {
	if testing.Short() {
		t.Skip("10k dataset is expensive for -short")
	}
	handler := newBulkWebDAVHandler(t, 1, 10000)
	remote := newBenchWebDAVRemote(t, context.Background(), handler)

	var files []source.FileInfo
	err := remote.(source.TreeScanner).ScanTree(context.Background(), "/bulk", func(fi source.FileInfo) error {
		files = append(files, fi)
		return nil
	})
	if err != nil {
		t.Fatalf("ScanTree: %v", err)
	}
	if len(files) != 10000 {
		t.Fatalf("visited %d entries, want 10000", len(files))
	}
	for _, fi := range files {
		if fi.IsDir {
			t.Fatalf("visited unexpected directory %s", fi.Path)
		}
	}
	if got := handler.propfind.Load(); got != 1 {
		t.Errorf("PROPFIND calls = %d, want 1", got)
	}
}

// TestScanTreeNestedDirectoryVisitsOnce：嵌套树中每个实际目录恰好
// 一次 PROPFIND；目录条目全部 visit（collision / 深度安全语义依赖），
// root 自身不在结果中。
func TestScanTreeNestedDirectoryVisitsOnce(t *testing.T) {
	fs := xnetdav.NewMemFS()
	writeMemFS(t, fs, "/bulk/a/f.txt", "a")
	writeMemFS(t, fs, "/bulk/b/f.txt", "b")
	writeMemFS(t, fs, "/bulk/c/d/f.txt", "c")
	handler, srv := newCountingServer(t, &xnetdav.Handler{FileSystem: fs, LockSystem: xnetdav.NewMemLS()})
	remote := newRootedFactoryRemote(t, srv.URL, "")

	visited := map[string]bool{}
	err := remote.(source.TreeScanner).ScanTree(context.Background(), "/", func(fi source.FileInfo) error {
		if visited[fi.Path] {
			t.Errorf("entry %s visited twice", fi.Path)
		}
		visited[fi.Path] = fi.IsDir
		return nil
	})
	if err != nil {
		t.Fatalf("ScanTree: %v", err)
	}
	// 目录条目：全部 visit；root 自身不 visit。
	for _, dir := range []string{"/bulk", "/bulk/a", "/bulk/b", "/bulk/c", "/bulk/c/d"} {
		if !visited[dir] {
			t.Errorf("directory %s not visited", dir)
		}
	}
	if visited["/"] {
		t.Error("root itself must not be visited")
	}
	// 文件条目：类型正确。
	for _, file := range []string{"/bulk/a/f.txt", "/bulk/b/f.txt", "/bulk/c/d/f.txt"} {
		if isDir, ok := visited[file]; !ok {
			t.Errorf("file %s not visited", file)
		} else if isDir {
			t.Errorf("file %s visited as directory", file)
		}
	}
	if len(visited) != 8 {
		t.Errorf("visited %d entries, want 5 dirs + 3 files", len(visited))
	}
	// 6 个被扫目录（root + 5 个子目录）→ 恰好 6 次 PROPFIND，每个
	// 目录一次，绝不因分页重复。
	if got := handler.propfind.Load(); got != 6 {
		t.Errorf("PROPFIND calls = %d, want 6 (one per scanned directory)", got)
	}
}

// TestScanTreeContracts 覆盖其余契约：取消、非法路径、callback
// 错误、RemoteRoot 映射与特殊文件名。
func TestScanTreeContracts(t *testing.T) {
	t.Run("ContextCanceled", func(t *testing.T) {
		handler := newBulkWebDAVHandler(t, 1, 1)
		remote := newBenchWebDAVRemote(t, context.Background(), handler)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		err := remote.(source.TreeScanner).ScanTree(ctx, "/bulk", func(fi source.FileInfo) error { return nil })
		if !errors.Is(err, context.Canceled) {
			t.Errorf("ScanTree canceled = %v, want context.Canceled", err)
		}
	})

	t.Run("InvalidLogicalPath", func(t *testing.T) {
		handler := newBulkWebDAVHandler(t, 1, 1)
		remote := newBenchWebDAVRemote(t, context.Background(), handler)
		for _, root := range []string{"not-absolute", "/a/../b", "/a//b"} {
			err := remote.(source.TreeScanner).ScanTree(context.Background(), root, func(fi source.FileInfo) error { return nil })
			if !errors.Is(err, source.ErrInvalid) {
				t.Errorf("ScanTree %q = %v, want ErrInvalid", root, err)
			}
		}
	})

	t.Run("CallbackErrorAborts", func(t *testing.T) {
		handler := newBulkWebDAVHandler(t, 1, 1)
		remote := newBenchWebDAVRemote(t, context.Background(), handler)
		sentinel := errors.New("stop scan")
		calls := 0
		err := remote.(source.TreeScanner).ScanTree(context.Background(), "/bulk", func(fi source.FileInfo) error {
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

	// visit 中途取消：Depth:1 响应一次带回整层条目，entry loop 逐条
	// 检查 ctx，同层剩余条目不再处理（契约「及时响应 ctx 取消」）。
	t.Run("CancelDuringVisit", func(t *testing.T) {
		handler := newBulkWebDAVHandler(t, 1, 100)
		remote := newBenchWebDAVRemote(t, context.Background(), handler)
		ctx, cancel := context.WithCancel(context.Background())
		visits := 0
		err := remote.(source.TreeScanner).ScanTree(ctx, "/bulk", func(fi source.FileInfo) error {
			if fi.IsDir {
				return nil
			}
			visits++
			cancel()
			return nil
		})
		if !errors.Is(err, context.Canceled) {
			t.Errorf("ScanTree cancel-during-visit = %v, want context.Canceled", err)
		}
		if visits >= 100 {
			t.Errorf("visit called %d times, want < 100 (stop within the entry loop)", visits)
		}
	})

	t.Run("RemoteRootMapping", func(t *testing.T) {
		// RemoteRoot=/bulk：逻辑根映射到 /bulk，ScanTree("/") 只返回
		// 子树内容，logical path 以 / 开头且不携带 root 前缀。
		fs := xnetdav.NewMemFS()
		seedBulkMemFS(t, fs, 1, 3)
		_, srv := newCountingServer(t, &xnetdav.Handler{FileSystem: fs, LockSystem: xnetdav.NewMemLS()})
		remote := newRootedFactoryRemote(t, srv.URL, "/bulk")

		var paths []string
		err := remote.(source.TreeScanner).ScanTree(context.Background(), "/", func(fi source.FileInfo) error {
			paths = append(paths, fi.Path)
			return nil
		})
		if err != nil {
			t.Fatalf("ScanTree: %v", err)
		}
		if len(paths) != 3 {
			t.Fatalf("paths = %v, want 3 files under root", paths)
		}
		for _, p := range paths {
			if len(p) == 0 || p[0] != '/' {
				t.Errorf("path %q is not absolute logical form", p)
			}
		}
	})

	t.Run("SpecialFilename", func(t *testing.T) {
		fs := xnetdav.NewMemFS()
		writeMemFS(t, fs, "/bulk/special 名字 +1.txt", "special")
		_, srv := newCountingServer(t, &xnetdav.Handler{FileSystem: fs, LockSystem: xnetdav.NewMemLS()})
		remote := newRootedFactoryRemote(t, srv.URL, "")

		var names []string
		err := remote.(source.TreeScanner).ScanTree(context.Background(), "/bulk", func(fi source.FileInfo) error {
			names = append(names, fi.Path)
			return nil
		})
		if err != nil {
			t.Fatalf("ScanTree: %v", err)
		}
		if len(names) != 1 || names[0] != "/bulk/special 名字 +1.txt" {
			t.Fatalf("names = %v, want special filename preserved", names)
		}
	})
}

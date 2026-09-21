package webdav

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"

	xnetdav "golang.org/x/net/webdav"

	"tinysync/internal/source"
	"tinysync/internal/syncjob"
)

// countingHandler 在协议 handler 外统计 PROPFIND 请求数：扫描链路的
// 请求规模是确定性算法指标（一个目录应恰好对应一次 Depth:1
// PROPFIND），benchmark 与 ScanTree 调用次数测试共用。
type countingHandler struct {
	next     http.Handler
	propfind atomic.Int64
}

func (h *countingHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == "PROPFIND" {
		h.propfind.Add(1)
	}
	h.next.ServeHTTP(w, r)
}

// BenchmarkRemotePagination10K：10,000 条目目录上的全量分页枚举
// （limit 500，10000/500 = 20 页游标流转）。基线证据：切片分页策略
// 在 Browser / List 分页路径的成本记录。
func BenchmarkRemotePagination10K(b *testing.B) {
	ctx := context.Background()
	handler := newBulkWebDAVHandler(b, 1, 10000)
	remote := newBenchWebDAVRemote(b, ctx, handler)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		cursor := ""
		count := 0
		for {
			page, err := remote.List(ctx, "/bulk", source.ListOptions{Limit: source.MaxListLimit, Cursor: cursor})
			if err != nil {
				b.Fatalf("list: %v", err)
			}
			count += len(page.Entries)
			if page.NextCursor == "" {
				break
			}
			cursor = page.NextCursor
		}
		if count != 10000 {
			b.Fatalf("listed %d entries, want %d", count, 10000)
		}
	}
}

// BenchmarkScanRemoteWebDAV10KFlat：同步扫描 10,000 文件单目录。
// 当前实现按 DefaultListLimit(100) 伪分页重复 PROPFIND 同一目录
// （≈100 次/op）；ScanTree 修复目标是 1 次/op。
func BenchmarkScanRemoteWebDAV10KFlat(b *testing.B) {
	ctx := context.Background()
	handler := newBulkWebDAVHandler(b, 1, 10000)
	remote := newBenchWebDAVRemote(b, ctx, handler)

	b.ReportAllocs()
	b.ResetTimer()
	before := handler.propfind.Load()
	for i := 0; i < b.N; i++ {
		files, err := syncjob.ScanRemote(ctx, remote, "/bulk")
		if err != nil {
			b.Fatalf("scan: %v", err)
		}
		if len(files) != 10000 {
			b.Fatalf("scanned %d files, want 10000", len(files))
		}
	}
	b.ReportMetric(float64(handler.propfind.Load()-before)/float64(b.N), "propfind/op")
}

// BenchmarkScanRemoteWebDAV10KNested：同步扫描 100 目录 × 100 文件。
// 每个实际目录应恰好枚举一次（101 个目录 → 101 次 PROPFIND/op）。
func BenchmarkScanRemoteWebDAV10KNested(b *testing.B) {
	ctx := context.Background()
	const dirs = 100
	const perDir = 100
	handler := newBulkWebDAVHandler(b, dirs, perDir)
	remote := newBenchWebDAVRemote(b, ctx, handler)

	b.ReportAllocs()
	b.ResetTimer()
	before := handler.propfind.Load()
	for i := 0; i < b.N; i++ {
		files, err := syncjob.ScanRemote(ctx, remote, "/bulk")
		if err != nil {
			b.Fatalf("scan: %v", err)
		}
		if len(files) != dirs*perDir {
			b.Fatalf("scanned %d files, want %d", len(files), dirs*perDir)
		}
	}
	b.ReportMetric(float64(handler.propfind.Load()-before)/float64(b.N), "propfind/op")
}

// newBulkWebDAVHandler 构造 MemFS（/bulk 下 dirs×perDir 个文件）并包上
// PROPFIND 计数 handler 的 httptest 服务。
func newBulkWebDAVHandler(tb testing.TB, dirs, perDir int) *countingHandler {
	tb.Helper()
	fs := xnetdav.NewMemFS()
	seedBulkMemFS(tb, fs, dirs, perDir)
	return &countingHandler{
		next: &xnetdav.Handler{FileSystem: fs, LockSystem: xnetdav.NewMemLS()},
	}
}

// seedBulkMemFS 在 MemFS 的 /bulk 下创建 flat（dirs=1）或 nested
// （dirs 个子目录，各 perDir 个文件）数据集。
func seedBulkMemFS(tb testing.TB, fs xnetdav.FileSystem, dirs, perDir int) {
	tb.Helper()
	ctx := context.Background()
	if err := fs.Mkdir(ctx, "/bulk", 0o755); err != nil {
		tb.Fatalf("mkdir: %v", err)
	}
	for d := 0; d < dirs; d++ {
		dir := "/bulk"
		if dirs > 1 {
			dir = fmt.Sprintf("/bulk/d%03d", d)
			if err := fs.Mkdir(ctx, dir, 0o755); err != nil {
				tb.Fatalf("mkdir %s: %v", dir, err)
			}
		}
		for i := 0; i < perDir; i++ {
			logical := fmt.Sprintf("%s/f%06d.txt", dir, i)
			f, err := fs.OpenFile(ctx, logical, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o644)
			if err != nil {
				tb.Fatalf("create %s: %v", logical, err)
			}
			if _, err := f.Write([]byte("x")); err != nil {
				tb.Fatalf("write %s: %v", logical, err)
			}
			if err := f.Close(); err != nil {
				tb.Fatalf("close %s: %v", logical, err)
			}
		}
	}
}

// newBenchWebDAVRemote 经生产 Factory 把计数 handler 后面的服务接成
// Remote；生命周期挂到 b.Cleanup。
func newBenchWebDAVRemote(tb testing.TB, ctx context.Context, handler *countingHandler) source.Remote {
	tb.Helper()
	srv := httptest.NewServer(handler)
	tb.Cleanup(srv.Close)
	factory := NewFactory()
	remote, err := factory.Create(ctx, source.Source{
		Type: source.TypeWebDAV,
		Config: source.Config{
			WebDAV: &source.WebDAVConfig{Endpoint: srv.URL},
		},
	}, source.Credentials{})
	if err != nil {
		tb.Fatalf("create remote: %v", err)
	}
	tb.Cleanup(func() { _ = remote.Close() })
	return remote
}

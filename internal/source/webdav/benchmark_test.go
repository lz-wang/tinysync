package webdav

import (
	"context"
	"fmt"
	"net/http/httptest"
	"os"
	"testing"

	xnetdav "golang.org/x/net/webdav"

	"tinysync/internal/source"
)

// BenchmarkRemotePagination10K：10,000 条目目录上的全量分页枚举
// （limit 500，21 页游标流转）。基线证据：切片分页策略的成本记录。
func BenchmarkRemotePagination10K(b *testing.B) {
	fs := xnetdav.NewMemFS()
	ctx := context.Background()
	if err := fs.Mkdir(ctx, "/bulk", 0o755); err != nil {
		b.Fatalf("mkdir: %v", err)
	}
	const total = 10000
	for i := 0; i < total; i++ {
		logical := fmt.Sprintf("/bulk/f%06d.txt", i)
		f, err := fs.OpenFile(ctx, logical, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o644)
		if err != nil {
			b.Fatalf("create %s: %v", logical, err)
		}
		if _, err := f.Write([]byte("x")); err != nil {
			b.Fatalf("write %s: %v", logical, err)
		}
		if err := f.Close(); err != nil {
			b.Fatalf("close %s: %v", logical, err)
		}
	}

	handler := &xnetdav.Handler{FileSystem: fs, LockSystem: xnetdav.NewMemLS()}
	srv := httptest.NewServer(handler)
	b.Cleanup(srv.Close)

	factory := NewFactory()
	remote, err := factory.Create(ctx, source.Source{
		Type: source.TypeWebDAV,
		Config: source.Config{
			WebDAV: &source.WebDAVConfig{Endpoint: srv.URL},
		},
	}, source.Credentials{})
	if err != nil {
		b.Fatalf("create remote: %v", err)
	}
	b.Cleanup(func() { _ = remote.Close() })

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
		if count != total {
			b.Fatalf("listed %d entries, want %d", count, total)
		}
	}
}

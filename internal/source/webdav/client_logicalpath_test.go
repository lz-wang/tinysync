package webdav

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	xnetdav "golang.org/x/net/webdav"

	"tinysync/internal/source"
)

// hrefToLogical 把服务器返回的 href path（percent-decode 后）转换为
// Source-relative logical path，并拒绝越出 endpoint root。
func TestHrefToLogical(t *testing.T) {
	tests := []struct {
		name    string
		prefix  string // 归一化后的 endpoint 前缀（无尾斜杠，root 为空串）
		href    string // 服务器 href 的 decoded path
		want    string
		wantErr bool
	}{
		{name: "root with trailing slash", prefix: "/dav/user", href: "/dav/user/", want: "/"},
		{name: "root without trailing slash", prefix: "/dav/user", href: "/dav/user", want: "/"},
		{name: "bare root endpoint", prefix: "", href: "/", want: "/"},
		{name: "bare root endpoint file", prefix: "", href: "/docs/report.txt", want: "/docs/report.txt"},
		{name: "directory", prefix: "/dav/user", href: "/dav/user/docs/", want: "/docs"},
		{name: "file", prefix: "/dav/user", href: "/dav/user/docs/report.txt", want: "/docs/report.txt"},
		{name: "space and unicode", prefix: "/dav/user", href: "/dav/user/docs/my file 中文.txt", want: "/docs/my file 中文.txt"},
		{name: "outside root", prefix: "/dav/user", href: "/dav/other/file", wantErr: true},
		{name: "prefix segment ambiguity", prefix: "/dav/user", href: "/dav/users/x", wantErr: true},
		{name: "prefix without separator", prefix: "/dav/user", href: "/dav/users", wantErr: true},
		{name: "not absolute", prefix: "/dav/user", href: "dav/user/docs", wantErr: true},
		{name: "empty href", prefix: "/dav/user", href: "", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := hrefToLogical(tt.prefix, tt.href)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("hrefToLogical(%q, %q) = %q, want error", tt.prefix, tt.href, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("hrefToLogical(%q, %q): %v", tt.prefix, tt.href, err)
			}
			if got != tt.want {
				t.Errorf("hrefToLogical(%q, %q) = %q, want %q", tt.prefix, tt.href, got, tt.want)
			}
		})
	}
}

// unicodeFS 构造含 ASCII、空格与中文文件名的内存文件系统。
func unicodeFS(t *testing.T) xnetdav.FileSystem {
	t.Helper()
	fs := xnetdav.NewMemFS()
	ctx := context.Background()
	if err := fs.Mkdir(ctx, "/docs", 0o755); err != nil {
		t.Fatalf("mkdir /docs: %v", err)
	}
	for _, name := range []string{"/docs/report.txt", "/docs/my file 中文.txt"} {
		f, err := fs.OpenFile(ctx, name, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o644)
		if err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
		if _, err := f.Write([]byte("data")); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
		if err := f.Close(); err != nil {
			t.Fatalf("close %s: %v", name, err)
		}
	}
	return fs
}

// startLogicalServer 启动挂在指定挂载点上的 WebDAV 服务并注册清理；
// 挂载点为空串表示直接挂在服务器根上。
func startLogicalServer(t *testing.T, fs xnetdav.FileSystem, mountPrefix string) *httptest.Server {
	t.Helper()
	dav := &xnetdav.Handler{FileSystem: fs, LockSystem: xnetdav.NewMemLS(), Prefix: mountPrefix}
	mux := http.NewServeMux()
	if mountPrefix == "" {
		mux.Handle("/", dav)
	} else {
		mux.Handle(mountPrefix, dav)
		mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			http.NotFound(w, r)
		})
	}
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// newLogicalRemote 构造指向 endpointPath 的 Remote。
func newLogicalRemote(t *testing.T, srv *httptest.Server, endpointPath string) source.Remote {
	t.Helper()
	factory := NewFactory()
	r, err := factory.Create(source.Source{
		Name:     "test",
		Type:     source.TypeWebDAV,
		Endpoint: srv.URL + endpointPath,
	}, "")
	if err != nil {
		t.Fatalf("Factory.Create: %v", err)
	}
	return r
}

// Stat 与 List 返回的 Path 必须是 Source-relative logical path：
// 服务器 href（含 percent-encoded 空格与中文）剥离 endpoint 前缀，
// 目录不带尾斜杠，List 不含目录自身；带前缀与裸根两种 endpoint 等价。
func TestLogicalPathConversion(t *testing.T) {
	for _, tc := range []struct {
		name         string
		mountPrefix  string
		endpointPath string
	}{
		{name: "prefixed endpoint", mountPrefix: "/dav/user/", endpointPath: "/dav/user/"},
		{name: "bare root endpoint", mountPrefix: "", endpointPath: "/"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := startLogicalServer(t, unicodeFS(t), tc.mountPrefix)
			r := newLogicalRemote(t, srv, tc.endpointPath)
			ctx := context.Background()

			root, err := r.Stat(ctx, "/")
			if err != nil {
				t.Fatalf("Stat /: %v", err)
			}
			if root.Path != "/" || !root.IsDir {
				t.Errorf("root = %+v, want Path / and IsDir", root)
			}

			docs, err := r.Stat(ctx, "/docs")
			if err != nil {
				t.Fatalf("Stat /docs: %v", err)
			}
			if docs.Path != "/docs" || !docs.IsDir {
				t.Errorf("docs = %+v, want Path /docs and IsDir", docs)
			}

			rootList, err := r.List(ctx, "/")
			if err != nil {
				t.Fatalf("List /: %v", err)
			}
			if len(rootList) != 1 || rootList[0].Path != "/docs" {
				t.Errorf("root list = %+v, want only /docs (self omitted)", rootList)
			}

			entries, err := r.List(ctx, "/docs")
			if err != nil {
				t.Fatalf("List /docs: %v", err)
			}
			got := map[string]bool{}
			for _, e := range entries {
				if e.IsDir || e.Fingerprint.Size == 0 {
					t.Errorf("entry %s: IsDir=%v Size=%d, want regular file with size", e.Path, e.IsDir, e.Fingerprint.Size)
				}
				got[e.Path] = true
			}
			if !got["/docs/report.txt"] || !got["/docs/my file 中文.txt"] {
				t.Errorf("entries = %v, want report.txt and percent-encoded unicode name", got)
			}
		})
	}
}

// 服务器 href 越出 endpoint root 时 List 整体报错，不返回部分结果。
func TestListRejectsEscapingHref(t *testing.T) {
	// 服务器把 collection 挂在 /dav/user/，客户端 endpoint 是 /dav/other/：
	// 所有 href 都不在 endpoint 前缀之内。
	srv := startLogicalServer(t, unicodeFS(t), "/dav/user/")
	r := newLogicalRemote(t, srv, "/dav/other/")

	if _, err := r.Stat(context.Background(), "/"); err == nil {
		t.Fatal("Stat / with foreign href = nil, want escape error")
	}
	if _, err := r.List(context.Background(), "/"); err == nil {
		t.Fatal("List / with foreign href = nil, want escape error")
	}
}

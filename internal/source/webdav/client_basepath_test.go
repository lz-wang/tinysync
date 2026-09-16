package webdav

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	xnetdav "golang.org/x/net/webdav"

	"tinysync/internal/source"
)

// basePathServer 是一个只在 /dav/user/ 子树下提供 WebDAV 的测试服务器，
// 并记录收到的全部请求路径，用于断言请求不逃逸 Source root。
type basePathServer struct {
	server *httptest.Server

	mu       sync.Mutex
	seenPath []string
}

// newBasePathServer 启动带路径前缀的 WebDAV 测试服务。
func newBasePathServer(t *testing.T) *basePathServer {
	t.Helper()
	bs := &basePathServer{}
	dav := &xnetdav.Handler{
		FileSystem: testFS(t),
		LockSystem: xnetdav.NewMemLS(),
		Prefix:     "/dav/user/",
	}
	mux := http.NewServeMux()
	mux.Handle("/dav/user/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bs.record(r.URL.Path)
		dav.ServeHTTP(w, r)
	}))
	// 前缀之外的请求全部记录并 404：修复前 Stat("/") 会错误地打到这里。
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		bs.record(r.URL.Path)
		http.NotFound(w, r)
	})
	bs.server = httptest.NewServer(mux)
	t.Cleanup(bs.server.Close)
	return bs
}

func (bs *basePathServer) record(path string) {
	bs.mu.Lock()
	defer bs.mu.Unlock()
	bs.seenPath = append(bs.seenPath, path)
}

func (bs *basePathServer) paths() []string {
	bs.mu.Lock()
	defer bs.mu.Unlock()
	return append([]string(nil), bs.seenPath...)
}

// endpoint 返回带路径前缀的 endpoint URL。
func (bs *basePathServer) endpoint() string {
	return bs.server.URL + "/dav/user/"
}

// newBasePathRemote 构造指向带前缀 endpoint 的 Remote。
func newBasePathRemote(t *testing.T, bs *basePathServer) source.Remote {
	factory := NewFactory()
	r, err := factory.Create(context.Background(), source.Source{
		Name:     "test",
		Type:     source.TypeWebDAV,
		Endpoint: bs.endpoint(),
	}, "")
	if err != nil {
		t.Fatalf("Factory.Create: %v", err)
	}
	return r
}

// assertRequestsStayInRoot 断言全部请求都落在 /dav/user 前缀之下，
// 即 Source root 不可逃逸。
func assertRequestsStayInRoot(t *testing.T, bs *basePathServer) {
	t.Helper()
	for _, p := range bs.paths() {
		if !strings.HasPrefix(p, "/dav/user") {
			t.Errorf("request escaped source root: %s", p)
		}
		if strings.Contains(p, "..") {
			t.Errorf("request path contains ..: %s", p)
		}
	}
}

// endpoint 带路径前缀时，Stat("/") 必须访问 endpoint 本身
// （/dav/user/），而不是服务器根（/）。
func TestEndpointBasePathStat(t *testing.T) {
	bs := newBasePathServer(t)
	r := newBasePathRemote(t, bs)

	info, err := r.Stat(t.Context(), "/")
	if err != nil {
		t.Fatalf("Stat / on %s: %v", bs.endpoint(), err)
	}
	if !info.IsDir {
		t.Error("root IsDir = false, want true")
	}
	assertRequestsStayInRoot(t, bs)
}

// endpoint 带路径前缀时，List 同样保持在 Source root 之下。
func TestEndpointBasePathList(t *testing.T) {
	bs := newBasePathServer(t)
	r := newBasePathRemote(t, bs)

	entries, err := r.List(t.Context(), "/docs")
	if err != nil {
		t.Fatalf("List /docs on %s: %v", bs.endpoint(), err)
	}
	found := false
	for _, e := range entries {
		if e.Path == "/docs/report.txt" && !e.IsDir && e.Fingerprint.Size > 0 {
			found = true
		}
	}
	if !found {
		t.Errorf("entries = %+v, want /docs/report.txt", entries)
	}
	assertRequestsStayInRoot(t, bs)
}

// endpoint 带路径前缀时，Open 按相对路径解析到前缀之下的文件。
func TestEndpointBasePathOpen(t *testing.T) {
	bs := newBasePathServer(t)
	r := newBasePathRemote(t, bs)

	rc, err := r.Open(t.Context(), "/docs/report.txt")
	if err != nil {
		t.Fatalf("Open on %s: %v", bs.endpoint(), err)
	}
	defer rc.Close()
	data, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(data) != "hello tinysync" {
		t.Errorf("content = %q, want hello tinysync", data)
	}
	assertRequestsStayInRoot(t, bs)
}

// 包含 .. 的逻辑路径经归一化后不得逃逸 Source root：
// /../escape 等价于 root 下的 /escape（本测试服务器上不存在，应报错，
// 但请求必须停留在前缀之内）。
func TestDotDotStaysInsideRoot(t *testing.T) {
	bs := newBasePathServer(t)
	r := newBasePathRemote(t, bs)

	if _, err := r.Stat(t.Context(), "/../escape"); err == nil {
		t.Fatal("Stat /../escape = nil, want error (path does not exist)")
	}
	assertRequestsStayInRoot(t, bs)
}

// trailingSlashServer 模拟要求 collection URL 以 / 结尾、且不自动重定向的
// WebDAV 服务：只有 /dav/user/ 提供正常服务，/dav/user 直接 405，
// 不帮客户端修正 URL。
type trailingSlashServer struct {
	server *httptest.Server

	mu       sync.Mutex
	seenPath []string
}

// newTrailingSlashServer 启动严格 trailing slash 测试服务。
// 刻意不使用 http.ServeMux：它会把 /dav/user 自动 301 到 /dav/user/，
// 从而掩盖 trailing slash 丢失问题。
func newTrailingSlashServer(t *testing.T) *trailingSlashServer {
	t.Helper()
	ts := &trailingSlashServer{}
	dav := &xnetdav.Handler{
		FileSystem: testFS(t),
		LockSystem: xnetdav.NewMemLS(),
		Prefix:     "/dav/user/",
	}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ts.record(r.URL.Path)
		switch r.URL.Path {
		case "/dav/user/":
			dav.ServeHTTP(w, r)
		case "/dav/user":
			http.Error(w, "missing trailing slash", http.StatusMethodNotAllowed)
		default:
			http.NotFound(w, r)
		}
	})
	ts.server = startServer(t, handler)
	return ts
}

func (ts *trailingSlashServer) record(path string) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	ts.seenPath = append(ts.seenPath, path)
}

func (ts *trailingSlashServer) paths() []string {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	return append([]string(nil), ts.seenPath...)
}

// endpoint 以 / 结尾时，Stat("/") 的第一个请求必须就是 /dav/user/，
// 不能依赖服务器把 /dav/user 重定向回来。go-webdav v0.7.0 的
// ResolveHref 会丢掉 endpoint 的 trailing slash，导致这类服务直接
// 拒绝 Source root 请求。
func TestStatRootPreservesTrailingSlash(t *testing.T) {
	ts := newTrailingSlashServer(t)
	factory := NewFactory()
	r, err := factory.Create(context.Background(), source.Source{
		Name:     "test",
		Type:     source.TypeWebDAV,
		Endpoint: ts.server.URL + "/dav/user/",
	}, "")
	if err != nil {
		t.Fatalf("Factory.Create: %v", err)
	}

	if _, err := r.Stat(t.Context(), "/"); err != nil {
		t.Fatalf("Stat / on %s: %v", ts.server.URL+"/dav/user/", err)
	}
	got := ts.paths()
	if len(got) == 0 {
		t.Fatal("no request reached the server")
	}
	if got[0] != "/dav/user/" {
		t.Errorf("first request = %q, want /dav/user/ without redirect", got[0])
	}
	for _, p := range got {
		if !strings.HasPrefix(p, "/dav/user/") {
			t.Errorf("request escaped collection: %s", p)
		}
	}
}

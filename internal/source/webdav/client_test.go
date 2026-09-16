package webdav

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-webdav"
	xnetdav "golang.org/x/net/webdav"

	"tinysync/internal/source"
)

// testFS 构造含 /docs/report.txt 的内存文件系统。
func testFS(t *testing.T) xnetdav.FileSystem {
	t.Helper()
	fs := xnetdav.NewMemFS()
	ctx := context.Background()
	if err := fs.Mkdir(ctx, "/docs", 0o755); err != nil {
		t.Fatalf("mkdir /docs: %v", err)
	}
	f, err := fs.OpenFile(ctx, "/docs/report.txt", os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		t.Fatalf("create test file: %v", err)
	}
	if _, err := f.Write([]byte("hello tinysync")); err != nil {
		t.Fatalf("write test file: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close test file: %v", err)
	}
	return fs
}

// davHandler 构造含 /docs/report.txt 的真实 WebDAV 服务端处理器。
func davHandler(t *testing.T) http.Handler {
	t.Helper()
	return &xnetdav.Handler{FileSystem: testFS(t), LockSystem: xnetdav.NewMemLS()}
}

// requireBasicAuth 给 handler 加 Basic Auth 校验。
func requireBasicAuth(next http.Handler, user, pass string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUser, gotPass, ok := r.BasicAuth()
		if !ok || gotUser != user || gotPass != pass {
			w.Header().Set("WWW-Authenticate", `Basic realm="restricted"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// startServer 启动测试 HTTP 服务并注册清理。
func startServer(t *testing.T, handler http.Handler) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srv
}

// newRemote 用 Factory 构造指向 srv 的 Remote。
func newRemote(t *testing.T, srv *httptest.Server, username, password string) source.Remote {
	t.Helper()
	factory := NewFactory()
	r, err := factory.Create(context.Background(), source.Source{
		Name: "test",
		Type: source.TypeWebDAV,
		Config: source.Config{WebDAV: &source.WebDAVConfig{
			Endpoint: srv.URL,
			Username: username,
		}},
	}, password)
	if err != nil {
		t.Fatalf("Factory.Create: %v", err)
	}
	return r
}

// 匿名访问：不发送 Authorization 头，Stat 可用。
func TestAnonymousAccess(t *testing.T) {
	var gotAuth string
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		davHandler(t).ServeHTTP(w, r)
	})
	srv := startServer(t, handler)

	r := newRemote(t, srv, "", "")
	if _, err := r.Stat(context.Background(), "/"); err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if gotAuth != "" {
		t.Errorf("anonymous request sent Authorization: %q", gotAuth)
	}
}

// Basic Auth 正确时可用；密码错误报错。
func TestBasicAuth(t *testing.T) {
	srv := startServer(t, requireBasicAuth(davHandler(t), "user", "pass"))

	r := newRemote(t, srv, "user", "pass")
	if _, err := r.Stat(context.Background(), "/"); err != nil {
		t.Fatalf("Stat with valid auth: %v", err)
	}

	bad := newRemote(t, srv, "user", "wrong")
	if _, err := bad.Stat(context.Background(), "/"); err == nil {
		t.Fatal("Stat with invalid password = nil, want error")
	}
}

// Stat 根路径返回目录信息。
func TestStatRoot(t *testing.T) {
	srv := startServer(t, davHandler(t))
	r := newRemote(t, srv, "", "")

	info, err := r.Stat(context.Background(), "/")
	if err != nil {
		t.Fatalf("Stat /: %v", err)
	}
	if !info.IsDir {
		t.Errorf("root IsDir = false, want true")
	}
}

// List 列出目录内容。
func TestList(t *testing.T) {
	srv := startServer(t, davHandler(t))
	r := newRemote(t, srv, "", "")

	entries, err := r.List(context.Background(), "/docs")
	if err != nil {
		t.Fatalf("List /docs: %v", err)
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
}

// Open 读回文件内容。
func TestOpen(t *testing.T) {
	srv := startServer(t, davHandler(t))
	r := newRemote(t, srv, "", "")

	rc, err := r.Open(context.Background(), "/docs/report.txt")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer rc.Close()
	data, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(data) != "hello tinysync" {
		t.Errorf("content = %q, want hello tinysync", data)
	}
}

// HTTP 客户端整体超时生效。
func TestTimeout(t *testing.T) {
	srv := startServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(300 * time.Millisecond)
		w.WriteHeader(http.StatusMultiStatus)
	}))
	httpClient := &http.Client{Timeout: 50 * time.Millisecond, CheckRedirect: redirectPolicy}
	auth := webdav.HTTPClientWithBasicAuth(httpClient, "", "")
	client, err := webdav.NewClient(auth, srv.URL)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	r := &remote{client: client}

	_, err = r.Stat(context.Background(), "/")
	if err == nil {
		t.Fatal("Stat on slow server = nil, want timeout error")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("error = %v, want DeadlineExceeded", err)
	}
}

// context 取消立即生效。
func TestContextCancellation(t *testing.T) {
	srv := startServer(t, davHandler(t))
	httpClient := &http.Client{CheckRedirect: redirectPolicy}
	auth := webdav.HTTPClientWithBasicAuth(httpClient, "", "")
	client, err := webdav.NewClient(auth, srv.URL)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	r := &remote{client: client}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := r.Stat(ctx, "/"); !errors.Is(err, context.Canceled) {
		t.Errorf("Stat on canceled ctx = %v, want context.Canceled", err)
	}
}

// 跨 host 重定向被拒绝，凭据不外流。
func TestCrossHostRedirectRejected(t *testing.T) {
	target := startServer(t, davHandler(t))
	redirector := startServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/docs", http.StatusFound)
	}))

	r := newRemote(t, redirector, "user", "pass")
	_, err := r.Stat(context.Background(), "/")
	if err == nil {
		t.Fatal("Stat with cross-host redirect = nil, want error")
	}
	if !strings.Contains(err.Error(), "cross-host") {
		t.Errorf("error = %v, want cross-host rejection", err)
	}
}

// redirectPolicy 单元测试：跨 host 拒绝。
func TestRedirectPolicyCrossHostRejected(t *testing.T) {
	via := []*http.Request{{URL: mustURL(t, "https://nas.example.com/dav/")}}
	req := &http.Request{URL: mustURL(t, "https://other.example.com/dav/")}
	err := redirectPolicy(req, via)
	if err == nil || !strings.Contains(err.Error(), "cross-host") {
		t.Errorf("redirectPolicy cross-host = %v, want cross-host rejection", err)
	}
}

// redirectPolicy 单元测试：HTTPS 到 HTTP 降级拒绝（同 host）。
func TestRedirectPolicyDowngradeRejected(t *testing.T) {
	via := []*http.Request{{URL: mustURL(t, "https://nas.example.com/dav/")}}
	req := &http.Request{URL: mustURL(t, "http://nas.example.com/dav/")}
	err := redirectPolicy(req, via)
	if err == nil || !strings.Contains(err.Error(), "downgrade") {
		t.Errorf("redirectPolicy downgrade = %v, want downgrade rejection", err)
	}
}

// redirectPolicy 单元测试：同 host 同协议放行。
func TestRedirectPolicyAllowsSameHost(t *testing.T) {
	via := []*http.Request{{URL: mustURL(t, "https://nas.example.com/dav/")}}
	req := &http.Request{URL: mustURL(t, "https://nas.example.com/dav/sub/")}
	if err := redirectPolicy(req, via); err != nil {
		t.Errorf("redirectPolicy same-host = %v, want nil", err)
	}
}

// mustURL 解析测试 URL，失败即终止。
func mustURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse %q: %v", raw, err)
	}
	return u
}

// 端到端：TLS 服务的重定向到 HTTP 目标被重定向策略拒绝
// （测试服务的端口互不相同，先命中跨 host 检查；降级分支由
// redirectPolicy 单元测试覆盖）。
func TestHTTPSDowngradeRedirectRejected(t *testing.T) {
	httpTarget := startServer(t, davHandler(t))
	tlsRedirector := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, httpTarget.URL+"/docs", http.StatusFound)
	}))
	t.Cleanup(tlsRedirector.Close)

	// 测试专用 client：跳过自签证书校验，但保留重定向安全策略。
	httpClient := &http.Client{
		Transport:     &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}},
		CheckRedirect: redirectPolicy,
	}
	auth := webdav.HTTPClientWithBasicAuth(httpClient, "", "")
	client, err := webdav.NewClient(auth, tlsRedirector.URL)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	r := &remote{client: client}

	_, err = r.Stat(context.Background(), "/")
	if err == nil {
		t.Fatal("Stat with downgrade redirect = nil, want error")
	}
	if !strings.Contains(err.Error(), "not allowed") {
		t.Errorf("error = %v, want redirect rejection", err)
	}
}

// Factory 拒绝不支持的协议类型。
func TestFactoryUnsupportedType(t *testing.T) {
	factory := NewFactory()
	_, err := factory.Create(context.Background(), source.Source{Type: source.Type("s3")}, "")
	if !errors.Is(err, source.ErrUnsupportedType) {
		t.Fatalf("Create s3 = %v, want ErrUnsupportedType", err)
	}
}

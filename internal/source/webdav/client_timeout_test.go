package webdav

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/emersion/go-webdav"
)

// newTestHTTPClient 用指定 response header timeout 构造与 Factory 相同
// 形态的客户端，供超时行为测试注入短时限。
func newTestHTTPClient(t *testing.T, headerTimeout time.Duration) *webdav.Client {
	t.Helper()
	httpClient := &http.Client{
		Transport:     newHTTPTransport(headerTimeout),
		CheckRedirect: redirectPolicy,
	}
	client, err := webdav.NewClient(webdav.HTTPClientWithBasicAuth(httpClient, "", ""), "http://unused.example.com/")
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return client
}

// 服务器迟迟不发响应头时，Transport 的 ResponseHeaderTimeout 必须快速
// 切断请求，而不是等待整个 body 超时周期。
func TestResponseHeaderTimeoutCutsStalledServer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(1500 * time.Millisecond)
		w.WriteHeader(http.StatusMultiStatus)
	}))
	t.Cleanup(srv.Close)

	remote := &remote{client: newTestHTTPClient(t, 100*time.Millisecond)}
	start := time.Now()
	_, err := remote.Stat(context.Background(), "/")
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("Stat on stalled server = nil, want header timeout error")
	}
	if elapsed > time.Second {
		t.Errorf("Stat returned after %v, want fast header timeout (~100ms)", elapsed)
	}
}

// 响应头到达后，慢速 body 传输不受 header timeout 约束：
// 大文件下载的生命周期由调用方的 Run context 控制。
func TestBodyTransferNotLimitedByHeaderTimeout(t *testing.T) {
	payload := []byte("0123456789abcdef")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		for _, b := range payload {
			_, _ = w.Write([]byte{b})
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			time.Sleep(60 * time.Millisecond)
		}
	}))
	t.Cleanup(srv.Close)

	httpClient := &http.Client{
		Transport:     newHTTPTransport(100 * time.Millisecond),
		CheckRedirect: redirectPolicy,
	}
	start := time.Now()
	resp, err := httpClient.Get(srv.URL)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("read slow body: %v", err)
	}
	if string(data) != string(payload) {
		t.Errorf("body = %q, want full payload", data)
	}
	if elapsed < 500*time.Millisecond {
		t.Errorf("body finished in %v, test expected slow transfer", elapsed)
	}
}

// Factory 构造的客户端不得设置整体 Client.Timeout（它会覆盖整个
// response body 生命周期、砍断大文件下载）；分段超时全部就位。
// Transport 为分类包装层（错误响应 → 协议错误分类），底层仍是
// 分段超时的 *http.Transport。
func TestHTTPClientContract(t *testing.T) {
	c := newHTTPClient()
	if c.Timeout != 0 {
		t.Errorf("Client.Timeout = %v, want 0 (whole-request timeout must not cut downloads)", c.Timeout)
	}
	classifier, ok := c.Transport.(*classifyingTransport)
	if !ok {
		t.Fatalf("Transport = %T, want *classifyingTransport", c.Transport)
	}
	tr, ok := classifier.base.(*http.Transport)
	if !ok {
		t.Fatalf("inner transport = %T, want *http.Transport", classifier.base)
	}
	if tr.ResponseHeaderTimeout <= 0 {
		t.Errorf("ResponseHeaderTimeout = %v, want positive", tr.ResponseHeaderTimeout)
	}
	if tr.TLSHandshakeTimeout <= 0 {
		t.Errorf("TLSHandshakeTimeout = %v, want positive", tr.TLSHandshakeTimeout)
	}
	if tr.IdleConnTimeout <= 0 {
		t.Errorf("IdleConnTimeout = %v, want positive", tr.IdleConnTimeout)
	}
	if tr.DialContext == nil {
		t.Error("DialContext = nil, want dialer with timeout")
	}
}

// wrapOp 保持把 context 截止判定为 DeadlineExceeded（Connection Test
// 的 context 超时路径不受 Transport 改造影响）。
func TestWrapOpPreservesDeadlineExceeded(t *testing.T) {
	err := wrapOp("stat", "/", context.DeadlineExceeded)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("wrapOp = %v, want errors.Is DeadlineExceeded", err)
	}
}

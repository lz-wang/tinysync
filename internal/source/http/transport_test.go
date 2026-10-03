package http

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"tinysync/internal/source"
)

// newTestRequester 构造指向 ts.URL 根的 requester。
func newTestRequester(t *testing.T, ts *httptest.Server, auth authConfig) *requester {
	t.Helper()
	m, err := newMapper(ts.URL + "/files/")
	if err != nil {
		t.Fatalf("newMapper: %v", err)
	}
	return newRequester(m, auth)
}

// 同源 canonical redirect（子树内跳转）可用；跨源 / 降级 redirect 被
// 拒；Authorization 只发往同源服务器。
func TestRedirectConfinement(t *testing.T) {
	var sawAuth string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/files/hop" {
			// 同子树内跳一次。
			http.Redirect(w, r, "/files/target.txt", http.StatusFound)
			return
		}
		if r.Header.Get("Authorization") != "" {
			sawAuth = r.Header.Get("Authorization")
		}
		if r.URL.Path == "/files/target.txt" {
			_, _ = w.Write([]byte("ok"))
		}
	}))
	defer ts.Close()

	r := newTestRequester(t, ts, authConfig{method: source.HTTPAuthBasic, username: "u", password: "p"})

	// 子树内 redirect：成功。
	req, err := r.newRequest(context.Background(), http.MethodGet, ts.URL+"/files/hop", "")
	if err != nil {
		t.Fatalf("newRequest: %v", err)
	}
	resp, err := r.do(req)
	if err != nil {
		t.Fatalf("same-origin redirect: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if sawAuth == "" {
		t.Error("basic auth header missing on same-origin request")
	}

	// 子树内请求被服务器 redirect 到外源：CheckRedirect 必须拒绝，
	// credential 不得泄露。
	ts2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://evil.example.com/leak", http.StatusFound)
	}))
	defer ts2.Close()
	r2 := newTestRequester(t, ts2, authConfig{method: source.HTTPAuthBearer, token: "tok"})
	req2, err := r2.newRequest(context.Background(), http.MethodGet, ts2.URL+"/files/x", "")
	if err != nil {
		t.Fatalf("newRequest: %v", err)
	}
	_, err = r2.do(req2)
	if err == nil {
		t.Fatal("cross-origin redirect accepted, want rejection")
	}
	if !strings.Contains(err.Error(), "redirect escapes") {
		t.Errorf("error = %v, want redirect escapes", err)
	}
	if source.IsRetryable(err) {
		t.Error("cross-origin redirect should be permanent (not retryable)")
	}

	// redirect 链超限：5 跳后终止。
	hops := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/files/hop", http.StatusFound)
	}))
	defer hops.Close()
	r3 := newTestRequester(t, hops, authConfig{method: source.HTTPAuthNone})
	req3, err := r3.newRequest(context.Background(), http.MethodGet, hops.URL+"/files/x", "")
	if err != nil {
		t.Fatalf("newRequest: %v", err)
	}
	_, err = r3.do(req3)
	if err == nil || !strings.Contains(err.Error(), "too many redirects") {
		t.Errorf("infinite redirect error = %v, want too many redirects", err)
	}
}

// redirect path 的 canonical 编码约束：encoded dot segment（大小写
// 两种十六进制）、编码分隔符（%2f / %5c）与明文 dot segment 都会被
// 部分 Web Server / 反向代理规范化到 BaseURL 之外，一律拒绝
// （permanent，credential 不跟随）。明文空段（//）与明文 dot segment
// 在 Go 的 URL 解析阶段即被折叠，但编码形态会完整保留到
// CheckRedirect——这是 confinement 的真正缺口。canonical 编码
// （如 %20 空格）的同子树 redirect 不受影响。
func TestRedirectEncodedPathConfinement(t *testing.T) {
	locations := []string{
		"/files/%2e%2e/secret",
		"/files/%2E%2E/secret",
		"/files/a%2fb",
		"/files/a%5cb",
		"/files/../secret",
	}
	for _, loc := range locations {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, loc, http.StatusFound)
		}))
		r := newTestRequester(t, ts, authConfig{method: source.HTTPAuthBearer, token: "tok"})
		req, err := r.newRequest(context.Background(), http.MethodGet, ts.URL+"/files/hop", "")
		if err != nil {
			t.Fatalf("newRequest: %v", err)
		}
		_, err = r.do(req)
		if err == nil {
			t.Errorf("redirect to %q accepted, want rejection", loc)
			ts.Close()
			continue
		}
		if !strings.Contains(err.Error(), "redirect escapes") {
			t.Errorf("redirect to %q error = %v, want redirect escapes", loc, err)
		}
		if source.IsRetryable(err) {
			t.Errorf("redirect to %q should be permanent, got %v", loc, err)
		}
		ts.Close()
	}

	// 对照：canonical 编码（%20）的同子树 redirect 正常跟随。
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/files/hop" {
			http.Redirect(w, r, "/files/a%20b.txt", http.StatusFound)
			return
		}
		_, _ = w.Write([]byte("ok"))
	}))
	defer ok.Close()
	r := newTestRequester(t, ok, authConfig{method: source.HTTPAuthNone})
	req, err := r.newRequest(context.Background(), http.MethodGet, ok.URL+"/files/hop", "")
	if err != nil {
		t.Fatalf("newRequest: %v", err)
	}
	resp, err := r.do(req)
	if err != nil {
		t.Fatalf("canonical-encoded redirect rejected: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
}

// 压缩 representation fail-closed：请求显式 identity；服务器仍返回
// gzip 编码时整体失败（permanent）。
func TestCompressionFailClosed(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Accept-Encoding") != "identity" {
			w.Header().Set("X-Seen-Encoding", r.Header.Get("Accept-Encoding"))
		}
		w.Header().Set("Content-Encoding", "gzip")
		_, _ = w.Write([]byte("compressed"))
	}))
	defer ts.Close()

	r := newTestRequester(t, ts, authConfig{method: source.HTTPAuthNone})
	req, err := r.newRequest(context.Background(), http.MethodGet, ts.URL+"/files/a.txt", "")
	if err != nil {
		t.Fatalf("newRequest: %v", err)
	}
	_, err = r.do(req)
	if err == nil {
		t.Fatal("gzip content-encoding accepted, want fail-closed")
	}
	if !strings.Contains(err.Error(), "content-encoding") {
		t.Errorf("error = %v, want content-encoding detail", err)
	}
	if source.IsRetryable(err) {
		t.Error("compression mismatch should be permanent")
	}
}

// 认证头：basic 与 bearer 按配置注入，none 不携带。
func TestAuthHeaders(t *testing.T) {
	var got string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	cases := []struct {
		name string
		auth authConfig
		want string
	}{
		{"none", authConfig{method: source.HTTPAuthNone}, ""},
		{"basic", authConfig{method: source.HTTPAuthBasic, username: "u", password: "p"}, "Basic dTpw"},
		{"bearer", authConfig{method: source.HTTPAuthBearer, token: "tok"}, "Bearer tok"},
	}
	for _, tc := range cases {
		r := newTestRequester(t, ts, tc.auth)
		req, err := r.newRequest(context.Background(), http.MethodGet, ts.URL+"/files/", "")
		if err != nil {
			t.Fatalf("%s newRequest: %v", tc.name, err)
		}
		resp, err := r.do(req)
		if err != nil {
			t.Fatalf("%s do: %v", tc.name, err)
		}
		_ = resp.Body.Close()
		if got != tc.want {
			t.Errorf("%s Authorization = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// metadata 兜底矩阵：HEAD 精确值、HEAD 405 → Range 206 total、HEAD
// 无 Content-Length → Range 200、Range total 未知 → fail-closed。
func TestStatFileMetadataFallbacks(t *testing.T) {
	ctx := context.Background()
	const headUnsupported = "head-unsupported"
	const noHeadLength = "no-head-length"
	const unknownRange = "unknown-range"
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/files/")
		switch name {
		case "plain.bin":
			if r.Method == http.MethodHead {
				w.Header().Set("Content-Length", "123")
				w.Header().Set("Last-Modified", "Wed, 21 Oct 2026 07:28:00 GMT")
				w.Header().Set("ETag", `"abc"`)
				w.WriteHeader(http.StatusOK)
				return
			}
			w.WriteHeader(http.StatusOK)
		case headUnsupported:
			if r.Method == http.MethodHead {
				w.WriteHeader(http.StatusMethodNotAllowed)
				return
			}
			// Range fallback：206 + total。
			w.Header().Set("Content-Range", "bytes 0-0/987654321")
			w.WriteHeader(http.StatusPartialContent)
		case noHeadLength:
			if r.Method == http.MethodHead {
				// 200 但不写 Content-Length（Go 会写 0？——显式
				// chunked：写 body 前 Flush）。
				w.WriteHeader(http.StatusOK)
				return
			}
			// Range：服务器忽略 Range 返回 200 + 完整长度。
			w.Header().Set("Content-Length", "42")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(make([]byte, 42))
		case unknownRange:
			if r.Method == http.MethodHead {
				w.WriteHeader(http.StatusMethodNotAllowed)
				return
			}
			w.Header().Set("Content-Range", "bytes 0-0/*")
			w.WriteHeader(http.StatusPartialContent)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer ts.Close()

	r := newTestRequester(t, ts, authConfig{method: source.HTTPAuthNone})

	meta, err := r.statFileMetadata(ctx, "/plain.bin")
	if err != nil {
		t.Fatalf("plain: %v", err)
	}
	if meta.Size != 123 || meta.ETag != `"abc"` || meta.ModifiedAt.IsZero() {
		t.Errorf("plain meta = %+v", meta)
	}

	meta, err = r.statFileMetadata(ctx, "/"+headUnsupported)
	if err != nil {
		t.Fatalf("head-unsupported: %v", err)
	}
	if meta.Size != 987654321 {
		t.Errorf("range total size = %d, want 987654321", meta.Size)
	}

	meta, err = r.statFileMetadata(ctx, "/"+noHeadLength)
	if err != nil {
		t.Fatalf("no-head-length: %v", err)
	}
	if meta.Size != 42 {
		t.Errorf("ignored-range size = %d, want 42", meta.Size)
	}

	_, err = r.statFileMetadata(ctx, "/"+unknownRange)
	if err == nil {
		t.Fatal("unknown range total accepted, want fail-closed")
	}
	if source.IsRetryable(err) {
		t.Error("unknown size should be permanent")
	}

	_, err = r.statFileMetadata(ctx, "/missing.bin")
	if err == nil || source.IsRetryable(err) {
		t.Errorf("404 should be permanent, got %v", err)
	}
}

// HTTP 状态分类矩阵（ADR 0009）：4xx permanent（408/429 除外）、
// 5xx transient。
func TestClassifyResponseError(t *testing.T) {
	cases := []struct {
		status int
		retry  bool
	}{
		{http.StatusBadRequest, false},
		{http.StatusUnauthorized, false},
		{http.StatusForbidden, false},
		{http.StatusNotFound, false},
		{http.StatusConflict, false},
		{http.StatusGone, false},
		{http.StatusRequestTimeout, true},
		{http.StatusTooManyRequests, true},
		{http.StatusInternalServerError, true},
		{http.StatusBadGateway, true},
		{http.StatusServiceUnavailable, true},
		{http.StatusGatewayTimeout, true},
	}
	for _, tc := range cases {
		resp := &http.Response{
			StatusCode: tc.status,
			Status:     http.StatusText(tc.status),
			Request:    mustNewRequest(t),
		}
		err := classifyResponseError("get", resp)
		if got := source.IsRetryable(err); got != tc.retry {
			t.Errorf("status %d retryable = %v, want %v", tc.status, got, tc.retry)
		}
	}
}

// mustNewRequest 构造分类测试用的最小请求对象。
func mustNewRequest(t *testing.T) *http.Request {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, "https://mirror.example.com/files/x", nil)
	if err != nil {
		t.Fatal(err)
	}
	return req
}

// ctx 取消经传输层原样透传（errors.Is 判定）。
func TestTransportContextCancellation(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer ts.Close()

	ctx, cancel := context.WithCancel(context.Background())
	r := newTestRequester(t, ts, authConfig{method: source.HTTPAuthNone})
	req, err := r.newRequest(ctx, http.MethodGet, ts.URL+"/files/slow.txt", "")
	if err != nil {
		t.Fatalf("newRequest: %v", err)
	}
	go func() { cancel() }()
	_, err = r.do(req)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
}

package webdav

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"tinysync/internal/source"
)

// adapter boundary 的错误响应分类：408 / 429 / 5xx transient，
// 其余全部 4xx（400 / 401 / 403 / 404 / 405 / 409 / 410 / 412 /
// 418 等）permanent——客户端错误重连不会改变结果；transport 错误
// 走通用规则。
func TestHTTPStatusClassification(t *testing.T) {
	cases := []struct {
		name      string
		status    int
		retryable bool
	}{
		{"401 unauthorized", http.StatusUnauthorized, false},
		{"403 forbidden", http.StatusForbidden, false},
		{"404 not found", http.StatusNotFound, false},
		{"408 request timeout", http.StatusRequestTimeout, true},
		{"429 too many requests", http.StatusTooManyRequests, true},
		{"500 internal", http.StatusInternalServerError, true},
		{"503 service unavailable", http.StatusServiceUnavailable, true},
		{"400 bad request", http.StatusBadRequest, false},
		{"405 method not allowed", http.StatusMethodNotAllowed, false},
		{"409 conflict", http.StatusConflict, false},
		{"410 gone", http.StatusGone, false},
		{"412 precondition failed", http.StatusPreconditionFailed, false},
		{"418 teapot", http.StatusTeapot, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
			}))
			defer srv.Close()

			remote, err := newFactoryRemote(t, srv.URL)
			if err != nil {
				t.Fatalf("create remote: %v", err)
			}
			_, err = remote.Stat(context.Background(), "/missing.txt")
			if err == nil {
				t.Fatalf("Stat on %d response = nil error, want failure", tc.status)
			}
			if got := source.IsRetryable(err); got != tc.retryable {
				t.Errorf("IsRetryable(%v) = %v, want %v", err, got, tc.retryable)
			}
		})
	}
}

// transport 层错误（连接被掐断）不带状态码：走通用传输层规则，
// 分类结果为可重试。
func TestTransportErrorIsRetryable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic(http.ErrAbortHandler)
	}))
	defer srv.Close()

	remote, err := newFactoryRemote(t, srv.URL)
	if err != nil {
		t.Fatalf("create remote: %v", err)
	}
	_, statErr := remote.Stat(context.Background(), "/x")
	if statErr == nil {
		t.Fatal("Stat on aborted handler = nil error, want failure")
	}
	// ErrAbortHandler 会以连接中断形式浮现；无论具体形态，都不应被
	// 判为确定性失败。
	if !source.IsRetryable(statErr) && !errors.Is(statErr, context.DeadlineExceeded) {
		t.Errorf("transport error %v unexpectedly permanent", statErr)
	}
}

// newFactoryRemote 用给定 endpoint 构造 adapter 的 Remote（走真实
// Factory 入口，保证生产构造路径与分类 transport 一致）。
func newFactoryRemote(t *testing.T, endpoint string) (source.Remote, error) {
	t.Helper()
	f := NewFactory()
	return f.Create(context.Background(), source.Source{
		Type: source.TypeWebDAV,
		Config: source.Config{
			WebDAV: &source.WebDAVConfig{Endpoint: endpoint},
		},
	}, source.Credentials{})
}

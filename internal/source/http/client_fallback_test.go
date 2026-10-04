package http

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"

	"tinysync/internal/source"
)

// raw 探测的临时错误保留可重试语义；永久失败仍报告 unsupported listing。
func TestAutoMiniserveFallbackResponseErrors(t *testing.T) {
	for _, tc := range []struct {
		status int
		retry  bool
	}{
		{http.StatusRequestTimeout, true},
		{http.StatusTooManyRequests, true},
		{http.StatusServiceUnavailable, true},
		{http.StatusForbidden, false},
		{http.StatusNotFound, false},
		{http.StatusOK, false}, // raw 也返回不可识别的普通页面。
	} {
		t.Run(strconv.Itoa(tc.status), func(t *testing.T) {
			var rawRequests atomic.Int32
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Query().Get("raw") == "true" {
					rawRequests.Add(1)
					w.WriteHeader(tc.status)
				}
				_, _ = w.Write([]byte(`<html><body>unrecognized UI</body></html>`))
			}))
			t.Cleanup(ts.Close)
			remote := openRemoteAt(t, ts, source.HTTPListingAuto)
			page, err := remote.List(context.Background(), "/", source.ListOptions{})
			if err == nil || len(page.Entries) != 0 || source.IsRetryable(err) != tc.retry {
				t.Fatalf("fallback: page = %+v, err = %v, want retryable=%v", page, err, tc.retry)
			}
			if rawRequests.Load() != 1 {
				t.Errorf("raw requests = %d, want 1", rawRequests.Load())
			}
			want := "no supported directory listing"
			if tc.retry {
				want = strconv.Itoa(tc.status)
			}
			if !strings.Contains(err.Error(), want) {
				t.Errorf("err = %v, want %q", err, want)
			}
		})
	}
}

// 用传输边界注入错误，避免依赖计时触发取消 / 超时 / 连接重置。
func TestAutoMiniserveFallbackTransportErrors(t *testing.T) {
	for _, wantErr := range []error{context.Canceled, context.DeadlineExceeded, syscall.ECONNRESET} {
		t.Run(wantErr.Error(), func(t *testing.T) {
			remote, err := NewFactory().Create(context.Background(), source.Source{
				Name: "fallback",
				Type: source.TypeHTTP,
				Config: source.Config{HTTP: &source.HTTPConfig{
					BaseURL: "http://example.test/", ListingMode: source.HTTPListingAuto,
				}},
			}, source.Credentials{})
			if err != nil {
				t.Fatalf("Create: %v", err)
			}
			t.Cleanup(func() { _ = remote.Close() })
			rawRequests := 0
			remote.(*Client).req.client.Transport = fallbackRoundTripperFunc(func(req *http.Request) (*http.Response, error) {
				if req.URL.Query().Get("raw") == "true" {
					rawRequests++
					return nil, wantErr
				}
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     make(http.Header),
					Body:       io.NopCloser(strings.NewReader(`<html><body>unrecognized UI</body></html>`)),
					Request:    req,
				}, nil
			})
			_, err = remote.Stat(context.Background(), "/")
			if !errors.Is(err, wantErr) {
				t.Fatalf("fallback error = %v, want %v", err, wantErr)
			}
			if source.IsRetryable(err) != !errors.Is(wantErr, context.Canceled) {
				t.Errorf("fallback retryability changed: %v", err)
			}
			if rawRequests != 1 {
				t.Errorf("raw requests = %d, want 1", rawRequests)
			}
		})
	}
}

type fallbackRoundTripperFunc func(*http.Request) (*http.Response, error)

func (f fallbackRoundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

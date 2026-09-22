package githubrelease

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"tinysync/internal/source"
)

// redirectTransport 是可控的 RoundTripper：按请求 host 分发响应，
// 不真实联网。用于在进程内验证「302 → 跨 host CDN → Token 剥除」
// 的完整下载链路（重定向白名单与剥除逻辑由 http.Client 执行）。
type redirectTransport struct {
	serve func(r *http.Request) *http.Response
}

func (t redirectTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	return t.serve(r), nil
}

// newResponse 构造最小响应。
func newResponse(status int, header map[string]string, body string) *http.Response {
	h := http.Header{}
	for k, v := range header {
		h.Set(k, v)
	}
	return &http.Response{
		StatusCode: status,
		Header:     h,
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

// TestOpenAssetRedirectStripsToken 资产请求 302 至 CDN host：跟随
// 重定向（host 在白名单）、第二个请求不携带 Authorization、最终
// 200 body 原样返回（验收：302 下载重定向不泄露 Token）。
func TestOpenAssetRedirectStripsToken(t *testing.T) {
	var cdnAuth string
	c := newClient("ghp_secret", "gitea", "gitea")
	c.httpClient = &http.Client{
		Transport: redirectTransport{serve: func(r *http.Request) *http.Response {
			switch r.URL.Host {
			case "api.github.com":
				if got := r.Header.Get("Authorization"); got != "Bearer ghp_secret" {
					t.Errorf("api request Authorization = %q, want bearer token", got)
				}
				if got := r.Header.Get("Accept"); got != "application/octet-stream" {
					t.Errorf("api request Accept = %q, want octet-stream", got)
				}
				return newResponse(http.StatusFound, map[string]string{
					"Location": "https://objects.githubusercontent.com/asset/1?sig=x",
				}, "")
			case "objects.githubusercontent.com":
				cdnAuth = r.Header.Get("Authorization")
				return newResponse(http.StatusOK, nil, "asset-bytes")
			default:
				t.Errorf("unexpected request to %s", r.URL.Host)
				return newResponse(http.StatusBadGateway, nil, "")
			}
		}},
		CheckRedirect: redirectPolicy("api.github.com"),
	}
	rc, err := c.openAsset(t.Context(), 42)
	if err != nil {
		t.Fatalf("openAsset: %v", err)
	}
	defer rc.Close()
	body, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("read asset: %v", err)
	}
	if string(body) != "asset-bytes" {
		t.Errorf("asset body = %q", string(body))
	}
	if cdnAuth != "" {
		t.Errorf("CDN request leaked Authorization: %q", cdnAuth)
	}
}

// TestOpenAssetRedirectRejectedHost 重定向目标不在白名单时整体失败。
func TestOpenAssetRedirectRejectedHost(t *testing.T) {
	c := newClient("ghp_secret", "gitea", "gitea")
	c.httpClient = &http.Client{
		Transport: redirectTransport{serve: func(r *http.Request) *http.Response {
			return newResponse(http.StatusFound, map[string]string{
				"Location": "https://evil.example.com/asset/1",
			}, "")
		}},
		CheckRedirect: redirectPolicy("api.github.com"),
	}
	if _, err := c.openAsset(t.Context(), 42); err == nil {
		t.Fatal("openAsset followed disallowed redirect, want error")
	}
}

// TestOpenAssetNotFound 扫描后 Asset 被删除 → 404 permanent，调用方
// 保留已有本地文件与管理状态（错误分类契约）。
func TestOpenAssetNotFound(t *testing.T) {
	c := newClient("", "gitea", "gitea")
	c.httpClient = &http.Client{
		Transport: redirectTransport{serve: func(r *http.Request) *http.Response {
			return newResponse(http.StatusNotFound, nil, `{"message": "Not Found"}`)
		}},
		CheckRedirect: redirectPolicy("api.github.com"),
	}
	_, err := c.openAsset(t.Context(), 42)
	if err == nil {
		t.Fatal("openAsset succeeded for deleted asset, want error")
	}
	if source.IsRetryable(err) {
		t.Errorf("deleted asset should be permanent: %v", err)
	}
}

// TestOpenAssetCancel 运行取消可中断下载请求：ctx 取消后 openAsset
// 返回 context 错误（验收：运行中取消可中断下载）。
func TestOpenAssetCancel(t *testing.T) {
	started := make(chan struct{})
	c := newClient("", "gitea", "gitea")
	c.httpClient = &http.Client{
		Transport: redirectTransport{serve: func(r *http.Request) *http.Response {
			close(started)
			<-r.Context().Done()
			return nil
		}},
		CheckRedirect: redirectPolicy("api.github.com"),
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		_, err := c.openAsset(ctx, 42)
		done <- err
	}()
	<-started
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("openAsset succeeded after cancel, want context error")
		}
		if !strings.Contains(err.Error(), context.Canceled.Error()) {
			t.Errorf("error = %v, want context canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("openAsset not canceled within 5s")
	}
}

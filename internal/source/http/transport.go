package http

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"tinysync/internal/source"
)

// maxRedirects 是允许的最大重定向跳数。redirect 收敛策略（ADR 0009）：
// same-origin only（scheme + host[:port] 全等，https→http 因此被拒）、
// 最终 path 仍须位于 BaseURL 子树——Basic / Bearer credential 绝不被
// 重定向到其它服务器。
const maxRedirects = 5

// transport 单例：DisableCompression 保证客户端不主动协商压缩
// representation（Downloader 的 written == Fingerprint.Size 校验依赖
// 字节数语义）；Accept-Encoding: identity 由每个请求显式携带。
var sharedTransport = &http.Transport{
	DisableCompression: true,
	Proxy:              http.ProxyFromEnvironment,
	// 单请求超时不设全局值：大文件传输由调用方 ctx 控制。
	TLSHandshakeTimeout:   10 * time.Second,
	ResponseHeaderTimeout: 30 * time.Second,
	MaxIdleConns:          16,
	MaxIdleConnsPerHost:   8,
	IdleConnTimeout:       90 * time.Second,
}

// authConfig 是请求认证配置（Factory 边界冻结的值副本）。
type authConfig struct {
	method   source.HTTPAuthMethod
	username string
	password string
	token    string
}

// requester 承载 listing / metadata / 下载共用的传输约束：URL 映射、
// 认证、identity 编码与 redirect 收敛。
type requester struct {
	base   *mapper
	client *http.Client
	auth   authConfig
}

// newRequester 构造 requester；redirect 收敛经 CheckRedirect 挂在
// client 上，对 listing / HEAD / GET 全部生效。
func newRequester(base *mapper, auth authConfig) *requester {
	r := &requester{base: base, auth: auth}
	r.client = &http.Client{
		Transport: sharedTransport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= maxRedirects {
				return &redirectError{fmt.Errorf("too many redirects (max %d)", maxRedirects)}
			}
			if !r.base.contains(req.URL) {
				return &redirectError{fmt.Errorf("redirect escapes source base URL: %s", req.URL.Redacted())}
			}
			return nil
		},
	}
	return r
}

// newRequest 构造带完整传输约束的请求：identity 编码、认证头。
// accept 是内容协商头（listing 请求传 JSON 优先，metadata / 下载传空）。
func (r *requester) newRequest(ctx context.Context, method, rawURL, accept string) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, rawURL, nil)
	if err != nil {
		return nil, source.MarkPermanent(fmt.Errorf("build %s request: %w", method, err))
	}
	// 压缩 representation 会破坏字节数校验语义：显式 identity，
	// 服务端仍返回非 identity 编码时在响应边界 fail-closed。
	req.Header.Set("Accept-Encoding", "identity")
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	switch r.auth.method {
	case source.HTTPAuthBasic:
		req.SetBasicAuth(r.auth.username, r.auth.password)
	case source.HTTPAuthBearer:
		req.Header.Set("Authorization", "Bearer "+r.auth.token)
	}
	return req, nil
}

// do 执行请求并统一校验响应边界：非 identity 的 Content-Encoding 一律
// 拒绝（Caddy precompressed sidecar 等场景会返回压缩 representation）。
func (r *requester) do(req *http.Request) (*http.Response, error) {
	resp, err := r.client.Do(req)
	if err != nil {
		return nil, classifyTransportError(req.Method, normalizeHTTPErr(err))
	}
	if enc := resp.Header.Get("Content-Encoding"); enc != "" && enc != "identity" && enc != "gzip;q=0" {
		_ = resp.Body.Close()
		return nil, source.MarkPermanent(fmt.Errorf(
			"http %s %s: content-encoding %q is not identity; compressed representations would break size verification",
			req.Method, req.URL.Redacted(), enc))
	}
	return resp, nil
}

// directoryURL / fileURL 转发到 mapper。
func (r *requester) directoryURL(logical string) string { return r.base.directoryURL(logical) }

func (r *requester) fileURL(logical string) string { return r.base.fileURL(logical) }

// CloseIdleConnections 释放 transport 上的空闲连接（Remote.Close 调用）。
func (r *requester) CloseIdleConnections() {
	r.client.CloseIdleConnections()
}

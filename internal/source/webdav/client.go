// Package webdav 实现 source.Remote 的 WebDAV 只读 adapter。
// 底层库虽提供写操作，本包刻意只暴露 Stat / List / Open：
// v1 的 Source interface 仅含同步所需的 read-only 能力。
package webdav

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/emersion/go-webdav"

	"tinysync/internal/source"
)

// HTTP 客户端硬化参数。
const (
	// clientTimeout 是单次 HTTP 请求的整体超时上限。
	clientTimeout = 15 * time.Second
	// maxRedirects 是单次请求允许的最多重定向次数。
	maxRedirects = 5
)

// Factory 实现 source.RemoteFactory，按 Source 配置构造 WebDAV 客户端。
type Factory struct{}

// NewFactory 构造 WebDAV RemoteFactory。
func NewFactory() *Factory {
	return &Factory{}
}

// Create 实现 source.RemoteFactory；匿名访问不发送 Authorization 头。
func (f *Factory) Create(s source.Source, password string) (source.Remote, error) {
	if s.Type != source.TypeWebDAV {
		return nil, fmt.Errorf("%w: %q", source.ErrUnsupportedType, s.Type)
	}
	endpoint, err := url.Parse(s.Endpoint)
	if err != nil {
		return nil, fmt.Errorf("parse webdav endpoint %s: %w", s.Endpoint, err)
	}
	httpClient := newHTTPClient()
	var auth webdav.HTTPClient = httpClient
	if s.Username != "" || password != "" {
		auth = webdav.HTTPClientWithBasicAuth(httpClient, s.Username, password)
	}
	client, err := webdav.NewClient(auth, s.Endpoint)
	if err != nil {
		return nil, fmt.Errorf("create webdav client for %s: %w", s.Endpoint, err)
	}
	return &remote{client: client, hrefPrefix: normalizeHrefPrefix(endpoint.Path)}, nil
}

// normalizeHrefPrefix 把 endpoint 路径归一为 href 前缀匹配形式：
// 去掉尾斜杠（root 为空串），供 hrefToLogical 按 path segment 对齐。
func normalizeHrefPrefix(endpointPath string) string {
	return strings.TrimSuffix(path.Clean("/"+endpointPath), "/")
}

// newHTTPClient 构造带安全边界的 HTTP 客户端：
// 整体超时、最多 5 次重定向、拒绝跨 host 重定向（避免凭据外流）、
// 拒绝 HTTPS 到 HTTP 的降级重定向。
func newHTTPClient() *http.Client {
	return &http.Client{
		Timeout:       clientTimeout,
		CheckRedirect: redirectPolicy,
	}
}

// redirectPolicy 是 http.Client 的重定向安全策略。
func redirectPolicy(req *http.Request, via []*http.Request) error {
	if len(via) >= maxRedirects {
		return fmt.Errorf("stopped after %d redirects", maxRedirects)
	}
	if req.URL.Host != via[0].URL.Host {
		return errors.New("webdav: cross-host redirect is not allowed")
	}
	if via[0].URL.Scheme == "https" && req.URL.Scheme != "https" {
		return errors.New("webdav: https to http downgrade redirect is not allowed")
	}
	return nil
}

// remote 是 source.Remote 的 WebDAV 实现。
type remote struct {
	client *webdav.Client
	// hrefPrefix 是 endpoint 路径的归一化前缀（无尾斜杠，root 为空串），
	// 用于把服务器 href 转换回 Source-relative logical path。
	hrefPrefix string
}

// resolveRelative 把 Source-relative logical path（统一以 / 开头）转换为
// endpoint-relative path（"/" → ""、"/foo" → "foo"）。go-webdav 只对相对
// 路径执行 path.Join(endpoint.Path, rel)，因此以 / 开头的路径会丢失
// endpoint 的路径前缀、直接变成服务器绝对路径（详见其 ResolveHref）。
// 归一化同时消除 .. 序列，保证请求不逃逸 Source root。
func resolveRelative(logicalPath string) string {
	cleaned := path.Clean("/" + logicalPath)
	return strings.TrimPrefix(cleaned, "/")
}

// logicalPath 把请求路径归一为 logical path 形式（/ 开头、无尾斜杠、
// root 为 "/"），与 resolveRelative 的归一化规则一致。
func logicalPath(p string) string {
	return path.Clean("/" + p)
}

// Stat 实现 source.Remote。
func (r *remote) Stat(ctx context.Context, path string) (source.FileInfo, error) {
	info, err := r.client.Stat(ctx, resolveRelative(path))
	if err != nil {
		return source.FileInfo{}, wrapOp("stat", path, err)
	}
	fi, err := r.toFileInfo(*info)
	if err != nil {
		return source.FileInfo{}, wrapOp("stat", path, err)
	}
	return fi, nil
}

// List 实现 source.Remote（非递归列目录，不包含目录自身条目）。
func (r *remote) List(ctx context.Context, path string) ([]source.FileInfo, error) {
	logical := logicalPath(path)
	entries, err := r.client.ReadDir(ctx, resolveRelative(path), false)
	if err != nil {
		return nil, wrapOp("list", path, err)
	}
	list := make([]source.FileInfo, 0, len(entries))
	for _, entry := range entries {
		fi, err := r.toFileInfo(entry)
		if err != nil {
			return nil, wrapOp("list", path, err)
		}
		// Depth:1 PROPFIND 的响应包含目录自身，对调用方不可见。
		if fi.Path == logical {
			continue
		}
		list = append(list, fi)
	}
	return list, nil
}

// Open 实现 source.Remote。
func (r *remote) Open(ctx context.Context, path string) (io.ReadCloser, error) {
	rc, err := r.client.Open(ctx, resolveRelative(path))
	if err != nil {
		return nil, wrapOp("open", path, err)
	}
	return rc, nil
}

// hrefToLogical 把服务器 href 的 decoded path 转换为 Source-relative
// logical path。href 必须落在 endpoint 前缀之内（按 path segment 对齐，
// 拒绝 /dav/users 之类的前缀歧义），目录尾斜杠被归一去除。
func hrefToLogical(prefix, href string) (string, error) {
	if !strings.HasPrefix(href, "/") {
		return "", fmt.Errorf("webdav href %q is not an absolute path", href)
	}
	if prefix != "" {
		if href != prefix && !strings.HasPrefix(href, prefix+"/") {
			return "", fmt.Errorf("webdav href %q escapes endpoint root %q", href, prefix)
		}
		href = strings.TrimPrefix(href, prefix)
	}
	logical := strings.TrimSuffix(href, "/")
	if logical == "" {
		logical = "/"
	}
	return logical, nil
}

// toFileInfo 转换为协议无关的 FileInfo：href 剥离 endpoint 前缀得到
// logical path，指纹填充 Size / ModifiedAt / ETag（Checksum 与 Version 留空）。
func (r *remote) toFileInfo(info webdav.FileInfo) (source.FileInfo, error) {
	logical, err := hrefToLogical(r.hrefPrefix, info.Path)
	if err != nil {
		return source.FileInfo{}, err
	}
	return source.FileInfo{
		Path:  logical,
		IsDir: info.IsDir,
		Fingerprint: source.Fingerprint{
			Size:       info.Size,
			ModifiedAt: info.ModTime,
			ETag:       info.ETag,
		},
	}, nil
}

// wrapOp 为底层错误补充操作与路径上下文；ctx 超时/取消经 %w 保持可判定。
func wrapOp(op, path string, err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return fmt.Errorf("webdav %s %s: %w", op, path, err)
}

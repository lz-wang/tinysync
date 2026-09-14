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
	httpClient := newHTTPClient()
	var auth webdav.HTTPClient = httpClient
	if s.Username != "" || password != "" {
		auth = webdav.HTTPClientWithBasicAuth(httpClient, s.Username, password)
	}
	client, err := webdav.NewClient(auth, s.Endpoint)
	if err != nil {
		return nil, fmt.Errorf("create webdav client for %s: %w", s.Endpoint, err)
	}
	return &remote{client: client}, nil
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
}

// Stat 实现 source.Remote。
func (r *remote) Stat(ctx context.Context, path string) (source.FileInfo, error) {
	info, err := r.client.Stat(ctx, normalizePath(path))
	if err != nil {
		return source.FileInfo{}, wrapOp("stat", path, err)
	}
	return toFileInfo(*info), nil
}

// List 实现 source.Remote（非递归列目录）。
func (r *remote) List(ctx context.Context, path string) ([]source.FileInfo, error) {
	entries, err := r.client.ReadDir(ctx, normalizePath(path), false)
	if err != nil {
		return nil, wrapOp("list", path, err)
	}
	list := make([]source.FileInfo, 0, len(entries))
	for _, entry := range entries {
		list = append(list, toFileInfo(entry))
	}
	return list, nil
}

// Open 实现 source.Remote。
func (r *remote) Open(ctx context.Context, path string) (io.ReadCloser, error) {
	rc, err := r.client.Open(ctx, normalizePath(path))
	if err != nil {
		return nil, wrapOp("open", path, err)
	}
	return rc, nil
}

// normalizePath 统一远端逻辑路径为以 / 开头的形式。
func normalizePath(path string) string {
	if path == "" {
		return "/"
	}
	if !strings.HasPrefix(path, "/") {
		return "/" + path
	}
	return path
}

// toFileInfo 转换为协议无关的 FileInfo。
func toFileInfo(info webdav.FileInfo) source.FileInfo {
	return source.FileInfo{
		Path:    info.Path,
		Size:    info.Size,
		IsDir:   info.IsDir,
		ModTime: info.ModTime,
	}
}

// wrapOp 为底层错误补充操作与路径上下文；ctx 超时/取消经 %w 保持可判定。
func wrapOp(op, path string, err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return fmt.Errorf("webdav %s %s: %w", op, path, err)
}

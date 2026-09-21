// Package webdav 实现 source.Remote 的 WebDAV adapter。同步主接口保持
// 只读；目录选择器经可选 DirectoryCreator 能力创建目录。
package webdav

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/emersion/go-webdav"

	"tinysync/internal/source"
)

// HTTP 客户端硬化参数。刻意不设置 http.Client.Timeout：它会覆盖整个
// response body 生命周期，把正常的大文件下载拦腰砍断；连接与响应头
// 阶段由 Transport 分段超时兜底，body 传输由调用方的 Run context 控制。
const (
	// dialTimeout 是建立 TCP 连接的最长时间。
	dialTimeout = 10 * time.Second
	// tlsHandshakeTimeout 是 TLS 握手的最长时间。
	tlsHandshakeTimeout = 10 * time.Second
	// responseHeaderTimeout 是发出请求到收到响应头的最长时间，
	// 不约束 body 传输。
	responseHeaderTimeout = 30 * time.Second
	// idleConnTimeout 是空闲连接在池中的存活时间。
	idleConnTimeout = 90 * time.Second
	// maxRedirects 是单次请求允许的最多重定向次数。
	maxRedirects = 5
)

// Factory 实现 source.RemoteFactory，按 Source 配置构造 WebDAV 客户端。
type Factory struct{}

// NewFactory 构造 WebDAV RemoteFactory。
func NewFactory() *Factory {
	return &Factory{}
}

// Type 实现 source.RemoteFactory：本 factory 服务 WebDAV 类型。
func (f *Factory) Type() source.Type {
	return source.TypeWebDAV
}

// Create 实现 source.RemoteFactory；匿名访问不发送 Authorization 头。
// WebDAV 基于 HTTP 无持久会话，ctx 当前仅用于接口一致性（连接建立
// 无独立网络操作）。
func (f *Factory) Create(ctx context.Context, s source.Source, credentials source.Credentials) (source.Remote, error) {
	if s.Type != source.TypeWebDAV || s.Config.WebDAV == nil {
		return nil, fmt.Errorf("%w: %q", source.ErrUnsupportedType, s.Type)
	}
	password := ""
	if credentials.WebDAV != nil {
		password = credentials.WebDAV.Password
	}
	cfg := *s.Config.WebDAV
	endpoint, err := url.Parse(cfg.Endpoint)
	if err != nil {
		return nil, fmt.Errorf("parse webdav endpoint %s: %w", cfg.Endpoint, err)
	}
	httpClient := newHTTPClient()
	var auth webdav.HTTPClient = httpClient
	if cfg.Username != "" || password != "" {
		auth = webdav.HTTPClientWithBasicAuth(httpClient, cfg.Username, password)
	}
	client, err := webdav.NewClient(auth, cfg.Endpoint)
	if err != nil {
		return nil, fmt.Errorf("create webdav client for %s: %w", cfg.Endpoint, err)
	}
	root := path.Clean("/" + cfg.RemoteRoot)
	return &remote{
		client:     client,
		root:       root,
		hrefPrefix: normalizeHrefPrefix(path.Join(endpoint.Path, root)),
	}, nil
}

// normalizeHrefPrefix 把 endpoint 路径归一为 href 前缀匹配形式：
// 去掉尾斜杠（root 为空串），供 hrefToLogical 按 path segment 对齐。
func normalizeHrefPrefix(endpointPath string) string {
	return strings.TrimSuffix(path.Clean("/"+endpointPath), "/")
}

// newHTTPClient 构造带安全边界的 HTTP 客户端：
// Transport 分段超时、最多 5 次重定向、拒绝跨 host 重定向（避免凭据外流）、
// 拒绝 HTTPS 到 HTTP 的降级重定向；错误响应在 transport 层转为带
// 分类标记的协议错误（见 classifyingTransport）。
func newHTTPClient() *http.Client {
	return &http.Client{
		Transport:     &classifyingTransport{base: newHTTPTransport(responseHeaderTimeout)},
		CheckRedirect: redirectPolicy,
	}
}

// newHTTPTransport 构造分段超时的 Transport：dial / TLS 握手 / 响应头
// 各自限时，body 传输不限时；headerTimeout 由调用方按场景注入。
func newHTTPTransport(headerTimeout time.Duration) *http.Transport {
	return &http.Transport{
		DialContext: (&net.Dialer{
			Timeout:   dialTimeout,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		TLSHandshakeTimeout:   tlsHandshakeTimeout,
		ResponseHeaderTimeout: headerTimeout,
		IdleConnTimeout:       idleConnTimeout,
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
	// root 是配置的 WebDAV 子目录；Source 逻辑根目录始终映射到这里。
	root string
	// hrefPrefix 是 endpoint 路径的归一化前缀（无尾斜杠，root 为空串），
	// 用于把服务器 href 转换回 Source-relative logical path。
	hrefPrefix string
}

// resolveRelative 把 Source-relative logical path（统一以 / 开头）转换为
// endpoint-relative path（"/" → ""、"/foo" → "foo"）。go-webdav 只对相对
// 路径执行 path.Join(endpoint.Path, rel)，因此以 / 开头的路径会丢失
// endpoint 的路径前缀、直接变成服务器绝对路径（详见其 ResolveHref）。
// 归一化同时消除 .. 序列，保证请求不逃逸 Source root。
func (r *remote) resolveRelative(logicalPath string) string {
	cleaned := path.Join(r.root, logicalPath)
	return strings.TrimPrefix(cleaned, "/")
}

// logicalPath 把请求路径归一为 logical path 形式（/ 开头、无尾斜杠、
// root 为 "/"），与 resolveRelative 的归一化规则一致。
func logicalPath(p string) string {
	return path.Clean("/" + p)
}

// Stat 实现 source.Remote。入口统一校验 logical path：非法路径
// fail-fast，不依赖归一化把无效输入悄悄变成另一个请求。
func (r *remote) Stat(ctx context.Context, path string) (source.FileInfo, error) {
	if err := source.ValidateLogicalPath(path); err != nil {
		return source.FileInfo{}, err
	}
	info, err := r.client.Stat(ctx, r.resolveRelative(path))
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
// WebDAV 的 Depth:1 PROPFIND 没有服务端分页游标：单层完整枚举后在
// adapter 边界切片分页，cursor 为 opaque offset token；分页约束的是
// 返回条目数，协议层单次请求仍是整层目录（v0.6 契约已记录该限制）。
func (r *remote) List(ctx context.Context, path string, opts source.ListOptions) (source.FilePage, error) {
	if err := source.ValidateLogicalPath(path); err != nil {
		return source.FilePage{}, err
	}
	logical := logicalPath(path)
	entries, err := r.client.ReadDir(ctx, r.resolveRelative(path), false)
	if err != nil {
		return source.FilePage{}, wrapOp("list", path, err)
	}
	all := make([]source.FileInfo, 0, len(entries))
	for _, entry := range entries {
		fi, err := r.toFileInfo(entry)
		if err != nil {
			return source.FilePage{}, wrapOp("list", path, err)
		}
		// Depth:1 PROPFIND 的响应包含目录自身，对调用方不可见。
		if fi.Path == logical {
			continue
		}
		all = append(all, fi)
	}
	// 切片分页要求单层枚举顺序跨请求稳定：WebDAV 协议不保证服务器
	// 排序，按 logical path 排序后分页，避免页间重复 / 缺失。
	sort.Slice(all, func(i, j int) bool { return all[i].Path < all[j].Path })
	return source.PageSlice(all, opts)
}

// Open 实现 source.Remote。入口统一校验 logical path。
func (r *remote) Open(ctx context.Context, path string) (io.ReadCloser, error) {
	if err := source.ValidateLogicalPath(path); err != nil {
		return nil, err
	}
	rc, err := r.client.Open(ctx, r.resolveRelative(path))
	if err != nil {
		return nil, wrapOp("open", path, err)
	}
	return rc, nil
}

// Mkdir 实现 source.DirectoryCreator，在逻辑路径对应的 WebDAV 目录
// 建立直接子目录。
func (r *remote) Mkdir(ctx context.Context, path string) error {
	if err := source.ValidateLogicalPath(path); err != nil || path == "/" {
		if err != nil {
			return err
		}
		return fmt.Errorf("%w: cannot create remote root", source.ErrInvalid)
	}
	if err := r.client.Mkdir(ctx, r.resolveRelative(path)); err != nil {
		return wrapOp("mkdir", path, err)
	}
	return nil
}

// Close 实现 source.Remote：WebDAV 基于 HTTP、无持久会话，
// 连接复用由 http.Transport 管理，显式关闭为空操作。
func (r *remote) Close() error {
	return nil
}

// 编译期断言：ScanTree 可选能力。
var _ source.TreeScanner = (*remote)(nil)

// ScanTree 实现 source.TreeScanner：全树扫描 root 子树，文件与目录
// 都 visit（root 自身除外）。每个目录恰好一次 Depth:1 PROPFIND——
// 同步扫描不再经 List 的切片分页把同一目录重复枚举上百次；分页
// 契约仍由 List 独立承担（Files / API 路径不受影响）。visit 错误
// 原样透传，任何一层失败即整体失败（契约见 source.TreeScanner）。
func (r *remote) ScanTree(ctx context.Context, root string, visit func(source.FileInfo) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := source.ValidateLogicalPath(root); err != nil {
		return err
	}
	return r.scanDir(ctx, logicalPath(root), visit)
}

// scanDir 递归枚举一个目录：一次 ReadDir（协议层一次 Depth:1
// PROPFIND）取回整层条目，文件与目录 visit 后对子目录递归。
func (r *remote) scanDir(ctx context.Context, dir string, visit func(source.FileInfo) error) error {
	entries, err := r.client.ReadDir(ctx, r.resolveRelative(dir), false)
	if err != nil {
		return wrapOp("scan", dir, err)
	}
	for _, entry := range entries {
		fi, err := r.toFileInfo(entry)
		if err != nil {
			return wrapOp("scan", dir, err)
		}
		// Depth:1 PROPFIND 的响应包含目录自身，对调用方不可见。
		if fi.Path == dir {
			continue
		}
		if err := visit(fi); err != nil {
			return err
		}
		if fi.IsDir {
			if err := r.scanDir(ctx, fi.Path, visit); err != nil {
				return err
			}
		}
	}
	return nil
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
// logical path（统一经 ValidateLogicalPath 校验），指纹填充
// Size / ModifiedAt / ETag（Checksum 与 Version 留空）。
func (r *remote) toFileInfo(info webdav.FileInfo) (source.FileInfo, error) {
	logical, err := hrefToLogical(r.hrefPrefix, info.Path)
	if err != nil {
		return source.FileInfo{}, err
	}
	if err := source.ValidateLogicalPath(logical); err != nil {
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

// webdavStatusError 是 transport 层捕获的 HTTP 错误响应：go-webdav
// 的错误类型位于 internal 包、外部无法按类型判定状态码，因此本
// adapter 在自己的 RoundTripper 边界把 ≥400 的响应转为本类型，协议
// 错误分类据此完成（adapter boundary 之外只见 source 错误语义）。
type webdavStatusError struct {
	code int
}

func (e *webdavStatusError) Error() string {
	return fmt.Sprintf("webdav: http status %d %s", e.code, http.StatusText(e.code))
}

// classifyWebDAVStatus 按状态码分类并标记：408 / 429 / 5xx（请求
// 超时、限流、服务端故障）为 transient；其余全部 4xx（400 / 401 /
// 403 / 404 / 405 / 409 / 410 / 412 等，客户端错误重连不会改变
// 结果）为 permanent。transport 层错误不经此函数，交给
// source.IsRetryable 的通用传输层规则。
func classifyWebDAVStatus(code int) error {
	err := &webdavStatusError{code: code}
	switch {
	case code == http.StatusRequestTimeout || code == http.StatusTooManyRequests || code >= 500:
		return source.MarkTransient(err)
	case code >= http.StatusBadRequest:
		return source.MarkPermanent(err)
	}
	return err
}

// classifyingTransport 在 transport 层把 ≥400 的 HTTP 响应转换为带
// 分类标记的错误。对 read-only 用途（PROPFIND / GET）等价：原本
// go-webdav 也会把这些状态转为操作失败，只是错误类型不可外部判定。
type classifyingTransport struct {
	base http.RoundTripper
}

// RoundTrip 实现 http.RoundTripper：正常响应原样返回，错误响应在
// 排空并关闭 body（让连接可复用）后转为分类错误；PROPFIND 207 响应
// 先经 ETag 净化（见 sanitizeETags）再交给 go-webdav 解析。
func (t *classifyingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.base.RoundTrip(req)
	if err != nil {
		// transport 层错误（连接重置、超时等）不带状态码，分类交给
		// source.IsRetryable 的通用传输层规则。
		return nil, err
	}
	if resp.StatusCode >= http.StatusBadRequest {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		_ = resp.Body.Close()
		return nil, classifyWebDAVStatus(resp.StatusCode)
	}
	if req.Method == "PROPFIND" && resp.StatusCode == http.StatusMultiStatus {
		return rewriteMultiStatusBody(resp)
	}
	return resp, nil
}

// maxRewriteBytes 是允许读入内存做 ETag 净化的 207 响应体上限；
// 超过则放弃净化原样透传（防御病态服务器，正常单层 PROPFIND 远小于此）。
const maxRewriteBytes = 64 << 20

// rewriteMultiStatusBody 读出 PROPFIND 207 响应体，执行 ETag 净化后
// 重组为可重复读的 body。
func rewriteMultiStatusBody(resp *http.Response) (*http.Response, error) {
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxRewriteBytes+1))
	if err != nil {
		_ = resp.Body.Close()
		return nil, err
	}
	if len(body) > maxRewriteBytes {
		// 已读部分与未读流拼回，行为退回库的严格解析。
		resp.Body = struct {
			io.Reader
			io.Closer
		}{io.MultiReader(bytes.NewReader(body), resp.Body), resp.Body}
		resp.ContentLength = -1
		return resp, nil
	}
	_ = resp.Body.Close()
	sanitized := sanitizeETags(body)
	resp.Body = io.NopCloser(bytes.NewReader(sanitized))
	resp.ContentLength = int64(len(sanitized))
	return resp, nil
}

// etagElementRegex 匹配 PROPFIND 响应中的 getetag 属性元素（允许任意
// XML 前缀与属性，如 <D:getetag>、<d:getetag>、<getetag xmlns="DAV:">）。
// XML 文本节点不含 '<'，用 [^<]* 即可圈定原始文本值。
var etagElementRegex = regexp.MustCompile(`(<(?:[A-Za-z_][\w.-]*:)?getetag(?:\s[^>]*)?>)([^<]*)(</(?:[A-Za-z_][\w.-]*:)?getetag\s*>)`)

// xmlTextReplacer 解码 XML 文本节点的五个预定义实体；&amp; 必须最后
// 替换，避免 &amp;lt; 之类被二次解码。
var xmlTextReplacer = strings.NewReplacer(
	"&lt;", "<",
	"&gt;", ">",
	"&quot;", `"`,
	"&apos;", "'",
	"&amp;", "&",
)

// sanitizeETags 净化 PROPFIND 响应体中的 getetag 值：go-webdav 用
// strconv.Unquote 严格解析（期望 RFC 4918 的带引号形式），但 Synology
// DSM、lighttpd 等服务器返回裸值或弱 ETag（W/"..."），一条不合规值
// 就让整个 ReadDir / Stat 失败。上游明确拒绝宽松解析（PR #69 / #174
// 未合并、#205 被关闭），因此在 transport 边界给不合规值补引号：
// 净化后的值经库去引号还原为原始 opaque 值，语义等价。判定基于「库
// 视角」——encoding/xml 解码实体后的 chardata：库本就能解析的值（含
// 实体编码形式）一律原样保留，只有 Unquote 确实会失败的才重写；已
// 带引号仅混入空白的规范化为去空白形式，避免二次加引号。
func sanitizeETags(body []byte) []byte {
	return etagElementRegex.ReplaceAllFunc(body, func(m []byte) []byte {
		groups := etagElementRegex.FindSubmatch(m)
		openTag, rawText, closeTag := groups[1], groups[2], groups[3]
		libraryView := xmlTextReplacer.Replace(string(rawText))
		if _, err := strconv.Unquote(libraryView); err == nil {
			return m
		}
		trimmed := strings.TrimSpace(libraryView)
		if trimmed == "" {
			return m
		}
		rewritten := strconv.Quote(trimmed)
		if _, err := strconv.Unquote(trimmed); err == nil {
			rewritten = trimmed
		}
		var out bytes.Buffer
		out.Write(openTag)
		_ = xml.EscapeText(&out, []byte(rewritten))
		out.Write(closeTag)
		return out.Bytes()
	})
}

// Package githubrelease 实现 GitHub Release 的 source.Remote adapter：
// 把一个仓库的 Releases 转换为统一的只读远端目录树，交给现有同步
// 引擎执行扫描、过滤、下载与校验。设计契约见 docs/design/github-release.md：
// 版本选择、路径编码、指纹与删除语义均在该文档冻结。
//
// adapter 内部分层：client 统一负责 HTTP、Link 分页、错误分类与
// ETag 条件请求缓存；releases / assets 负责版本与制品发现；remote /
// scanner 把发现结果映射为逻辑路径；download 只负责打开二进制流。
package githubrelease

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"tinysync/internal/source"
)

const (
	// defaultAPIBaseURL 是第一版固定的 GitHub API Host：Token 只发送
	// 到该 Host（下载重定向除外，重定向策略见 newHTTPClient）。GitHub
	// Enterprise Server 的自定义 Host 属后续受控扩展，第一版不开放。
	defaultAPIBaseURL = "https://api.github.com"

	// pageSize 是列表接口的 per_page；GitHub 上限 100，取满以减少
	// 分页请求数（完整分页是 Mirror 删除授权的前提）。
	pageSize = 100

	// dialTimeout / tlsHandshakeTimeout / responseHeaderTimeout 是
	// HTTP 传输的分段超时；响应体传输不限时（大 Asset 可合法传输很久，
	// 取消依赖 ctx 贯穿）。
	dialTimeout           = 15 * time.Second
	tlsHandshakeTimeout   = 15 * time.Second
	responseHeaderTimeout = 30 * time.Second

	// maxRedirects 限制下载重定向链长度。
	maxRedirects = 5

	// downloadHostSuffixes 是下载重定向目标的 host 后缀白名单：GitHub
	// 与其对象 CDN。按 DNS label 边界匹配，见 githubHostAllowed。
	githubHostSuffixes = "github.com"
	githubCDNHostSufix = "githubusercontent.com"
)

// apiError 是携带 HTTP 状态码与响应片段的 API 错误：adapter 边界
// 错误分类的依据（classifyStatus），上层只问 source.IsRetryable。
type apiError struct {
	code int
	body string
	// rateLimited 标记该 403 是主速率限制耗尽（而非权限不足）。
	rateLimited bool
}

func (e *apiError) Error() string {
	if e.rateLimited {
		return fmt.Sprintf("github: api status %d (rate limited): %s", e.code, e.body)
	}
	return fmt.Sprintf("github: api status %d: %s", e.code, e.body)
}

// client 是 GitHub REST API 的最小只读客户端。统一负责 HTTP、Link
// 分页、错误分类与 ETag 条件请求缓存；元数据请求（Release / Asset
// 枚举）在该 client 内串行执行，对速率限制友好；Asset 下载流不经
// 此串行约束，复用上层全局传输并发控制。
type client struct {
	httpClient *http.Client
	// baseURL 生产固定 api.github.com；测试注入 httptest server。
	baseURL string
	// token 为空串表示匿名访问（公开仓库）。
	token string
	owner string
	repo  string
	cache *etagCache
	// metaMu 串行化全部元数据 GET：同一 client（即同一 Remote 实例）
	// 内的 Release / Asset 枚举不并发打 API。
	metaMu sync.Mutex
}

// newClient 构造默认配置的 GitHub API 客户端。
func newClient(token, owner, repo string) *client {
	return newClientAtDefault(defaultAPIBaseURL, token, owner, repo)
}

// newClientAt 构造指向指定 API base 的客户端：生产 base 固定
// api.github.com，测试经 Factory.newClientAt 注入 httptest 假服务器。
func newClientAtDefault(baseURL, token, owner, repo string) *client {
	return &client{
		httpClient: newHTTPClient(defaultAPIHost(baseURL)),
		baseURL:    baseURL,
		token:      token,
		owner:      owner,
		repo:       repo,
		cache:      newETagCache(),
	}
}

// defaultAPIHost 提取 base URL 的 host，作为重定向策略的起始锚点。
func defaultAPIHost(baseURL string) string {
	u, err := url.Parse(baseURL)
	if err != nil {
		return "api.github.com"
	}
	return u.Host
}

// newHTTPClient 构造带安全边界的 HTTP 客户端：Transport 分段超时、
// 响应体传输不限时；重定向策略见 redirectPolicy。originHost 是首个
// 请求的 host（生产为 api.github.com，测试为 httptest server），
// 重定向允许回落到它（测试服务器的自重定向）。
func newHTTPClient(originHost string) *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			DialContext: (&net.Dialer{
				Timeout:   dialTimeout,
				KeepAlive: 30 * time.Second,
			}).DialContext,
			TLSHandshakeTimeout:   tlsHandshakeTimeout,
			ResponseHeaderTimeout: responseHeaderTimeout,
			IdleConnTimeout:       90 * time.Second,
		},
		CheckRedirect: redirectPolicy(originHost),
	}
}

// redirectPolicy 是重定向安全策略（契约见设计文档 §7）：
//   - 仅允许 https（GitHub CDN 不提供 http，拒绝降级）；
//   - 目标 host 必须是起始 host 或以 github.com / githubusercontent.com
//     结尾（DNS label 边界匹配），拒绝意外下载目标；
//   - 跨 host 重定向显式剥除 Authorization：Token 只属于 API Host。
//     （Go 标准库对跨域重定向本就不复制该头，这里显式执行以免依赖
//     隐式行为。）
func redirectPolicy(originHost string) func(*http.Request, []*http.Request) error {
	return func(req *http.Request, via []*http.Request) error {
		if len(via) >= maxRedirects {
			return fmt.Errorf("github: stopped after %d redirects", maxRedirects)
		}
		if req.URL.Scheme != "https" {
			return fmt.Errorf("github: redirect to %q is not https", req.URL)
		}
		if req.URL.Host != originHost && !githubHostAllowed(req.URL.Host) {
			return fmt.Errorf("github: redirect host %q is not an allowed download target", req.URL.Host)
		}
		if req.URL.Host != via[0].URL.Host {
			req.Header.Del("Authorization")
		}
		return nil
	}
}

// githubHostAllowed 判断 host 是否为 GitHub 下载 CDN：按 DNS label
// 边界做后缀匹配（evil-github.com 不匹配 github.com）。
func githubHostAllowed(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	for _, suffix := range [2]string{githubHostSuffixes, githubCDNHostSufix} {
		if host == suffix || strings.HasSuffix(host, "."+suffix) {
			return true
		}
	}
	return false
}

// classifyStatus 按状态码分类并标记（adapter boundary 契约，与其余
// 协议 adapter 的分类对齐）：
//   - 429、5xx → transient（限流、服务端故障）；
//   - 403 区分速率限制：主速率限制耗尽的 403（X-RateLimit-Remaining
//     为 0）属瞬时——等待配额窗口后同一请求可恢复；其余 403 是权限
//     不足，permanent；
//   - 其余 4xx（401 / 404 / 422 等）→ permanent，重试不改变结果。
func classifyStatus(resp *http.Response, body string) error {
	e := &apiError{code: resp.StatusCode, body: body}
	switch {
	case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
		return source.MarkTransient(e)
	case resp.StatusCode == http.StatusForbidden:
		if resp.Header.Get("X-RateLimit-Remaining") == "0" || resp.Header.Get("Retry-After") != "" {
			e.rateLimited = true
			return source.MarkTransient(e)
		}
		return source.MarkPermanent(e)
	case resp.StatusCode >= http.StatusBadRequest:
		return source.MarkPermanent(e)
	}
	return e
}

// etagCacheEntry 是条件请求缓存的条目：上次响应的 ETag、body 与
// Link 分页游标。next 必须随 body 一起缓存：GitHub 的 304 响应不
// 携带 Link header，若分页续拉依赖 304 响应头解析 next，列表会被
// 错误截断为首页——对 Mirror 而言等于把「远端消失」误判到被截断
// 的页上，违反「不完整扫描不得产生删除授权」的不变量。
type etagCacheEntry struct {
	etag string
	body []byte
	next string
}

// etagCache 是按 URL 键控的条件请求缓存：GET 携带 If-None-Match，
// 304 命中复用缓存 body。GitHub 对返回 304 的条件请求不计入主速率
// 限制。缓存条目只含非敏感元数据；任一请求失败由调用方整体报错，
// 不存在「用不完整缓存凑快照」的路径（fail-closed）。
type etagCache struct {
	mu      sync.Mutex
	entries map[string]etagCacheEntry
}

func newETagCache() *etagCache {
	return &etagCache{entries: make(map[string]etagCacheEntry)}
}

// get 返回 URL 的缓存条目（无则 ok=false）。
func (c *etagCache) get(url string) (etagCacheEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[url]
	return e, ok
}

// put 写入 URL 的缓存条目。
func (c *etagCache) put(url string, e etagCacheEntry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[url] = e
}

// getJSON 执行一次 GET：带 ETag 条件请求，2xx 解码 JSON，304 复用
// 缓存，≥400 按状态码分类。入参接受以 / 开头的相对 API path（内部
// 拼 baseURL）或完整 URL（分页 next）。返回响应 Link header 中的
// rel="next" URL（无则空串），供调用方继续分页。元数据请求全程持有
// metaMu，同一 client 内串行执行。
func (c *client) getJSON(ctx context.Context, pathOrURL string, out any) (string, error) {
	c.metaMu.Lock()
	defer c.metaMu.Unlock()
	return c.getJSONLocked(ctx, pathOrURL, out)
}

// getJSONLocked 是 getJSON 的无锁实现，调用方必须已持有 metaMu
// （分页循环为减少锁开销直接复用）。
func (c *client) getJSONLocked(ctx context.Context, pathOrURL string, out any) (string, error) {
	rawURL := pathOrURL
	if strings.HasPrefix(pathOrURL, "/") {
		rawURL = c.baseURL + pathOrURL
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", fmt.Errorf("github: build request %s: %w", rawURL, err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	if entry, ok := c.cache.get(rawURL); ok {
		req.Header.Set("If-None-Match", entry.etag)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		// ctx 取消优先：上层 Runner 据此把用户停止收敛为 canceled。
		if ctxErr := ctx.Err(); ctxErr != nil {
			return "", ctxErr
		}
		return "", fmt.Errorf("github: get %s: %w", rawURL, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusNotModified {
		entry, ok := c.cache.get(rawURL)
		if !ok {
			return "", fmt.Errorf("github: 304 without cached entry for %s", rawURL)
		}
		if err := json.Unmarshal(entry.body, out); err != nil {
			return "", fmt.Errorf("github: decode cached %s: %w", rawURL, err)
		}
		// 304 响应不携带 Link header：分页游标复用 200 时的缓存值，
		// 绝不把「无 Link」当「已到末页」（fail-closed，见 etagCacheEntry）。
		return entry.next, nil
	}
	if resp.StatusCode >= http.StatusBadRequest {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return "", classifyStatus(resp, strings.TrimSpace(string(body)))
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxMetadataBodyBytes))
	if err != nil {
		return "", fmt.Errorf("github: read %s: %w", rawURL, err)
	}
	if err := json.Unmarshal(body, out); err != nil {
		return "", fmt.Errorf("github: decode %s: %w", rawURL, err)
	}
	next, err := c.nextLink(resp)
	if err != nil {
		return "", err
	}
	// 仅缓存携带 ETag 的响应；无 ETag 时退化为直连请求（正确性不受
	// 影响，只是少了条件请求优惠）。
	if etag := resp.Header.Get("ETag"); etag != "" {
		c.cache.put(rawURL, etagCacheEntry{etag: etag, body: body, next: next})
	}
	return next, nil
}

// maxMetadataBodyBytes 是单个元数据响应的读取上限：releases 列表每页
// 100 条含完整 asset 数组，正常不超过数 MB；上限防病态响应撑爆内存。
const maxMetadataBodyBytes = 16 << 20

// nextLink 从 Link header 解析 rel="next" 的 URL；没有 next 返回空串
// （EOF）。next URL 必须落在 baseURL 的 origin 内（scheme + host 一致）：
// 分页游标是服务器回传的，origin 校验防把请求打到未知主机。
func (c *client) nextLink(resp *http.Response) (string, error) {
	header := resp.Header.Get("Link")
	if header == "" {
		return "", nil
	}
	base, err := url.Parse(c.baseURL)
	if err != nil {
		return "", fmt.Errorf("github: parse api base: %w", err)
	}
	for _, field := range strings.Split(header, ",") {
		parts := strings.Split(field, ";")
		if len(parts) < 2 {
			continue
		}
		target := strings.Trim(strings.TrimSpace(parts[0]), "<>")
		var rel string
		for _, param := range parts[1:] {
			name, value, found := strings.Cut(strings.TrimSpace(param), "=")
			if found && strings.TrimSpace(name) == "rel" {
				rel = strings.Trim(strings.TrimSpace(value), `"`)
			}
		}
		if rel != "next" {
			continue
		}
		u, err := url.Parse(target)
		if err != nil {
			return "", fmt.Errorf("github: parse next link %q: %w", target, err)
		}
		if u.Scheme != base.Scheme || u.Host != base.Host {
			return "", fmt.Errorf("github: next link %q escapes api origin", target)
		}
		return u.String(), nil
	}
	return "", nil
}

// listAll 逐页枚举一个列表端点：首页为 firstPath（相对 API root，
// 自带 per_page），其后按 Link next 续拉直到 EOF。任何一页失败都
// 整体失败并返回 nil——完整分页是 Mirror 删除授权的前提，绝不返回
// 部分结果。整个枚举全程持有 metaMu：一次完整的列表发现对速率限制
// 表现为连续串行的请求序列。
func listAll[T any](ctx context.Context, c *client, firstPath string) ([]T, error) {
	c.metaMu.Lock()
	defer c.metaMu.Unlock()
	var all []T
	pageURL := c.baseURL + firstPath
	for {
		var page []T
		next, err := c.getJSONLocked(ctx, pageURL, &page)
		if err != nil {
			return nil, err
		}
		all = append(all, page...)
		if next == "" {
			return all, nil
		}
		pageURL = next
	}
}

// apiPath 构造 /repos/{owner}/{repo} 前缀的 API path：owner 与 repo
// 已在配置校验时限定字符集，直接拼接。
func (c *client) apiPath(suffix string) string {
	return "/repos/" + c.owner + "/" + c.repo + suffix
}

// verifyRepo 验证仓库可访问（工厂创建阶段的权限预检）：GET
// /repos/{owner}/{repo}，返回 GitHub 侧全名（owner/repo）。仓库不
// 存在与无权限 GitHub 都返回 404（不泄露私有仓库存在性），经分类为
// permanent 由上层透出。
func (c *client) verifyRepo(ctx context.Context) (string, error) {
	var repo struct {
		FullName string `json:"full_name"`
	}
	if _, err := c.getJSON(ctx, c.apiPath(""), &repo); err != nil {
		return "", err
	}
	if repo.FullName == "" {
		// 防御异常响应：full_name 缺失时退回本地解析值。
		return c.owner + "/" + c.repo, nil
	}
	return repo.FullName, nil
}

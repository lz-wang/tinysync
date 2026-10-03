// Client 是 source.Remote 的 HTTP 实现。无持久会话：每个操作是一次
// 独立 HTTP 请求（listing / HEAD / GET），Close 只释放空闲连接。
// listing profile 的确定：显式 mode 按配置约束响应形态；auto 在首个
// 成功 listing 上探测并缓存（同一服务器的形态一致）。Caddy
// file_limit 完整性检查在 listing 边界 fail-closed（ADR 0009）。
package http

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"path"
	"sort"
	"sync"

	"tinysync/internal/source"
)

// metadataConcurrency 是 HTML listing 模式批量补全精确 metadata 的
// 有界并发上限（one file = one HEAD，禁止无界 goroutine）。
const metadataConcurrency = 8

// Client 实现 source.Remote 与 source.TreeScanner。
type Client struct {
	cfg  source.HTTPConfig
	req  *requester
	mode source.HTTPListingMode

	mu   sync.Mutex
	kind listingKind
}

// 编译期契约断言：只读 Remote + TreeScanner；不实现 DirectoryCreator。
var (
	_ source.Remote      = (*Client)(nil)
	_ source.TreeScanner = (*Client)(nil)
)

// newClient 构造 Client（Factory 的生产路径；测试直接复用）。
func newClient(cfg source.HTTPConfig, req *requester) *Client {
	mode := cfg.ListingMode
	if mode == "" {
		mode = source.HTTPListingAuto
	}
	limit := cfg.CaddyFileLimit
	if limit == 0 {
		limit = source.DefaultCaddyFileLimit
	}
	cfg.ListingMode = mode
	cfg.CaddyFileLimit = limit
	return &Client{cfg: cfg, req: req, mode: mode}
}

// Stat 实现 source.Remote。"/" 直接请求根目录 listing（触发 profile
// 探测与结构校验——TestConnection 因此能识别「端点可达但不是目录
// 索引」的形态）；其余路径经父目录 listing 精确定位，避免依赖服务器
// 对文件 URL 的目录式响应。
func (c *Client) Stat(ctx context.Context, logicalPath string) (source.FileInfo, error) {
	if err := ctx.Err(); err != nil {
		return source.FileInfo{}, err
	}
	if err := source.ValidateLogicalPath(logicalPath); err != nil {
		return source.FileInfo{}, err
	}
	if logicalPath == "/" {
		if _, err := c.fetchDir(ctx, "/"); err != nil {
			return source.FileInfo{}, err
		}
		return source.FileInfo{Path: "/", IsDir: true}, nil
	}
	parent := path.Dir(logicalPath)
	name := path.Base(logicalPath)
	entries, err := c.fetchDir(ctx, parent)
	if err != nil {
		return source.FileInfo{}, err
	}
	for _, e := range entries {
		if e.Name != name {
			continue
		}
		infos, err := c.hydrate(ctx, parent, []rawEntry{e})
		if err != nil {
			return source.FileInfo{}, err
		}
		return infos[0], nil
	}
	return source.FileInfo{}, source.MarkPermanent(fmt.Errorf("http stat %s: %w", logicalPath, fs.ErrNotExist))
}

// List 实现 source.Remote：单层完整枚举后切片分页（HTTP 目录索引
// 无游标语义）；HTML 模式先补全精确 metadata，排序保证分页稳定。
func (c *Client) List(ctx context.Context, logicalDir string, opts source.ListOptions) (source.FilePage, error) {
	if err := ctx.Err(); err != nil {
		return source.FilePage{}, err
	}
	if err := source.ValidateLogicalPath(logicalDir); err != nil {
		return source.FilePage{}, err
	}
	entries, err := c.fetchDir(ctx, logicalDir)
	if err != nil {
		return source.FilePage{}, err
	}
	infos, err := c.hydrate(ctx, logicalDir, entries)
	if err != nil {
		return source.FilePage{}, err
	}
	sort.Slice(infos, func(i, j int) bool { return infos[i].Path < infos[j].Path })
	return source.PageSlice(infos, opts)
}

// Open 实现 source.Remote：GET 文件 URL（identity representation，
// 非 identity 编码在响应边界 fail-closed）；字节数一致性由 Downloader
// 的 written == Fingerprint.Size 校验兜底。
func (c *Client) Open(ctx context.Context, logicalPath string) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := source.ValidateLogicalPath(logicalPath); err != nil {
		return nil, err
	}
	if logicalPath == "/" {
		return nil, fmt.Errorf("%w: cannot open directory root", source.ErrInvalid)
	}
	req, err := c.req.newRequest(ctx, http.MethodGet, c.req.fileURL(logicalPath), "")
	if err != nil {
		return nil, err
	}
	resp, err := c.req.do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != 200 {
		_ = resp.Body.Close()
		return nil, classifyResponseError("open", resp)
	}
	return resp.Body, nil
}

// Close 实现 source.Remote：无持久会话，释放空闲连接，幂等。
func (c *Client) Close() error {
	c.req.CloseIdleConnections()
	return nil
}

// fetchDir 获取并解析目录 listing。auto 模式在首个成功响应上探测
// profile（含一次 ?raw=true 的 miniserve 探测兜底）并缓存；显式
// mode 约束响应形态必须匹配。caddy listing 条目数达到配置的
// file_limit 时 fail-closed：截断快照会授权 Mirror 误删除，宁可
// 整轮失败。
func (c *Client) fetchDir(ctx context.Context, dir string) ([]rawEntry, error) {
	raw := false
	switch {
	case c.mode == source.HTTPListingMiniserve:
		raw = true
	default:
		if kind := c.cachedKind(); kind == listingMiniserveHTML {
			raw = true
		}
	}
	body, err := c.getListingBody(ctx, dir, raw)
	if err != nil {
		return nil, err
	}
	kind := detectListing(body)
	if kind == listingUnknown && !raw && c.mode == source.HTTPListingAuto {
		// 未识别的 HTML 可能是 miniserve 完整 UI 页：以 ?raw=true
		// 再探测一次（仍失败则明确 unsupported）。
		if rawBody, rawErr := c.getListingBody(ctx, dir, true); rawErr == nil {
			if rawKind := detectListing(rawBody); rawKind != listingUnknown {
				body, kind = rawBody, rawKind
			}
		}
	}
	if kind == listingUnknown {
		return nil, unsupportedListingError("")
	}
	kind, err = c.resolveKind(kind, body)
	if err != nil {
		return nil, err
	}
	entries, err := parseListing(kind, c.req.base, dir, body)
	if err != nil {
		return nil, err
	}
	if c.mode == source.HTTPListingAuto {
		c.cacheKind(kind)
	}
	if kind == listingCaddyJSON && len(entries) >= c.cfg.CaddyFileLimit {
		return nil, source.MarkPermanent(fmt.Errorf(
			"caddy directory listing of %s reached file_limit=%d; snapshot completeness cannot be guaranteed",
			dir, c.cfg.CaddyFileLimit))
	}
	return entries, nil
}

// resolveKind 把探测到的形态与配置的 listing_mode 对齐：显式 profile
// 只接受自己（nginx 含 JSON / HTML 两种子形态）的形态；空数组对
// nginx / caddy 语义等价（零条目），按配置形态解释。
func (c *Client) resolveKind(detected listingKind, body []byte) (listingKind, error) {
	if c.mode == source.HTTPListingAuto {
		return detected, nil
	}
	accept := map[source.HTTPListingMode][]listingKind{
		source.HTTPListingNginx:     {listingNginxJSON, listingNginxHTML},
		source.HTTPListingCaddy:     {listingCaddyJSON},
		source.HTTPListingMiniserve: {listingMiniserveHTML},
	}
	for _, k := range accept[c.mode] {
		if detected == k {
			return detected, nil
		}
	}
	if detected == listingNginxJSON && isEmptyJSONArray(body) &&
		(c.mode == source.HTTPListingCaddy || c.mode == source.HTTPListingNginx) {
		// 空数组：按配置形态解释（parser 对空形态语义一致）。
		return kindOfMode(c.mode), nil
	}
	return listingUnknown, source.MarkPermanent(fmt.Errorf(
		"detected %s listing does not match configured listing_mode=%s", detected, c.mode))
}

// kindOfMode 返回 mode 的首选形态（空数组重解释用）。
func kindOfMode(mode source.HTTPListingMode) listingKind {
	switch mode {
	case source.HTTPListingCaddy:
		return listingCaddyJSON
	case source.HTTPListingMiniserve:
		return listingMiniserveHTML
	default:
		return listingNginxJSON
	}
}

// cachedKind 返回 auto 模式缓存的形态（无缓存 / 其它 mode 为 unknown）。
func (c *Client) cachedKind() listingKind {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.mode != source.HTTPListingAuto {
		return listingUnknown
	}
	return c.kind
}

// cacheKind 缓存 auto 探测结果。
func (c *Client) cacheKind(kind listingKind) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.kind = kind
}

// getListingBody 请求目录 listing 并返回 body；非 200 分类为协议错误。
func (c *Client) getListingBody(ctx context.Context, dir string, raw bool) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	urlStr := c.req.directoryURL(dir)
	if raw {
		urlStr = miniserveRawURL(urlStr)
	}
	req, err := c.req.newRequest(ctx, http.MethodGet, urlStr, "application/json, text/html;q=0.9")
	if err != nil {
		return nil, err
	}
	resp, err := c.req.do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 200 {
		return nil, classifyResponseError("list", resp)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, classifyTransportError("list", err)
	}
	return body, nil
}

// hydrate 把 rawEntry 转换为 FileInfo：JSON listing 自带精确
// size / mtime（不为 ETag 额外发 HEAD——Size + ModifiedAt 已满足
// Planner 契约）；HTML listing 的文件条目经 HEAD（Range 兜底）补全
// 精确 metadata + ETag，批量有界并发。任何补全失败都让整个操作
// 失败（部分结果不返回）。
func (c *Client) hydrate(ctx context.Context, dir string, entries []rawEntry) ([]source.FileInfo, error) {
	if len(entries) == 0 {
		return []source.FileInfo{}, nil
	}
	metas := make([]fileMetadata, len(entries))
	idx := make([]int, 0, len(entries))
	for i, e := range entries {
		if e.IsDir || e.SizeKnown {
			continue
		}
		idx = append(idx, i)
	}
	if len(idx) > 0 {
		if err := c.hydrationPool(ctx, dir, entries, idx, metas); err != nil {
			return nil, err
		}
	}
	infos := make([]source.FileInfo, len(entries))
	for i, e := range entries {
		logical := joinLogical(dir, e.Name)
		fi := source.FileInfo{Path: logical, IsDir: e.IsDir}
		switch {
		case e.IsDir:
			fi.Fingerprint.ModifiedAt = e.ModifiedAt
		case e.SizeKnown:
			fi.Fingerprint.Size = e.Size
			fi.Fingerprint.ModifiedAt = e.ModifiedAt
		default:
			fi.Fingerprint.Size = metas[i].Size
			fi.Fingerprint.ModifiedAt = metas[i].ModifiedAt
			fi.Fingerprint.ETag = metas[i].ETag
		}
		infos[i] = fi
	}
	return infos, nil
}

// hydrationPool 以有界并发补全 idx 列出的文件条目 metadata。首个
// 错误决定整体失败（其余在途请求随 ctx / 自然完成收尾）。
func (c *Client) hydrationPool(ctx context.Context, dir string, entries []rawEntry, idx []int, metas []fileMetadata) error {
	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		firstErr error
	)
	setErr := func(err error) {
		mu.Lock()
		defer mu.Unlock()
		if firstErr == nil {
			firstErr = err
		}
	}
	sem := make(chan struct{}, metadataConcurrency)
	for _, i := range idx {
		if ctx.Err() != nil {
			break
		}
		sem <- struct{}{}
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			defer func() { <-sem }()
			meta, err := c.req.statFileMetadata(ctx, joinLogical(dir, entries[i].Name))
			if err != nil {
				setErr(err)
				return
			}
			metas[i] = meta
		}(i)
	}
	wg.Wait()
	if firstErr != nil {
		return firstErr
	}
	return ctx.Err()
}

// isEmptyJSONArray 报告 body 是否为空 JSON 数组（空目录的形态等价）。
func isEmptyJSONArray(body []byte) bool {
	for _, b := range body {
		switch b {
		case ' ', '\t', '\r', '\n':
			continue
		case '[':
			return true
		default:
			return false
		}
	}
	return false
}

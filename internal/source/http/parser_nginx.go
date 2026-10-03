package http

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"golang.org/x/net/html"
)

// nginxJSONEntry 是 nginx autoindex_format json 的条目形态：
// 目录名带尾 "/"，目录无 size；mtime 为 HTTP 日期格式。size 是字节
// 精确值（autoindex_exact_size 只影响 HTML 格式）。
type nginxJSONEntry struct {
	Name  string `json:"name"`
	Type  string `json:"type"`
	Mtime string `json:"mtime"`
	Size  int64  `json:"size"`
}

// parseNginxJSON 解析 nginx JSON autoindex（推荐形态：autoindex on +
// autoindex_format json）。
func parseNginxJSON(m *mapper, dir string, body []byte) ([]rawEntry, error) {
	var entries []nginxJSONEntry
	if err := json.Unmarshal(body, &entries); err != nil {
		return nil, malformedListing(dir, fmt.Sprintf("invalid nginx JSON: %v", err))
	}
	raw := make([]rawEntry, 0, len(entries))
	for _, e := range entries {
		if e.Type != "directory" && e.Type != "file" {
			return nil, malformedListing(dir, fmt.Sprintf("nginx entry %q has unknown type %q", e.Name, e.Type))
		}
		isDir := e.Type == "directory"
		if !isDir && e.Size < 0 {
			// 负 size 是畸形 listing：不得进入 Fingerprint（Planner 的
			// 大小比较与 Downloader 的字节数校验都依赖非负精确值）。
			return nil, malformedListing(dir, fmt.Sprintf("nginx entry %q has negative size %d", e.Name, e.Size))
		}
		entry := rawEntry{Name: strings.TrimSuffix(e.Name, "/"), IsDir: isDir}
		if !isDir {
			// 文件条目携带精确字节大小。
			entry.SizeKnown = true
			entry.Size = e.Size
		}
		if t, err := http.ParseTime(e.Mtime); err == nil {
			entry.ModifiedAt = t
		}
		raw = append(raw, entry)
	}
	return collect(dir, raw)
}

// parseNginxHTML 解析 nginx 默认 HTML autoindex：<pre> 内的 <a href>
// 列表，目录 href 带尾 "/"。href 只作为发现条目的输入——经
// entryFromHref 校验后，条目名以 logical path 重建，最终请求 URL 不
// 使用 href。页面显示的大小 / 日期不解析（人类可读近似，不可进入
// Fingerprint），metadata 由 HEAD 补全。任何 href 形态不可信都让整
// 个 listing 失败（部分 listing 等于不完整快照）。
func parseNginxHTML(m *mapper, dir string, body []byte) ([]rawEntry, error) {
	doc, err := html.Parse(strings.NewReader(string(body)))
	if err != nil {
		return nil, malformedListing(dir, fmt.Sprintf("invalid nginx HTML: %v", err))
	}
	pre := findFirst(doc, "pre")
	if pre == nil {
		return nil, malformedListing(dir, "nginx HTML has no <pre> listing")
	}
	var (
		raw      []rawEntry
		parseErr error
	)
	visitAnchors(pre, func(href string) {
		// 导航链接（父目录）跳过；条目 href 严格解析。
		if href == "../" || href == "./" || href == "/" {
			return
		}
		entry, err := m.entryFromHref(dir, href)
		if err != nil {
			if parseErr == nil {
				parseErr = err
			}
			return
		}
		raw = append(raw, rawEntry{Name: entry.Name, IsDir: entry.IsDir})
	})
	if parseErr != nil {
		return nil, malformedListing(dir, parseErr.Error())
	}
	return collect(dir, raw)
}

// findFirst 返回首个指定 tag 的元素。
func findFirst(n *html.Node, tag string) *html.Node {
	if n.Type == html.ElementNode && n.Data == tag {
		return n
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if found := findFirst(c, tag); found != nil {
			return found
		}
	}
	return nil
}

// visitAnchors 遍历子树内的 <a href>。
func visitAnchors(n *html.Node, fn func(href string)) {
	if n.Type == html.ElementNode && n.Data == "a" {
		for _, attr := range n.Attr {
			if attr.Key == "href" {
				fn(attr.Val)
				break
			}
		}
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		visitAnchors(c, fn)
	}
}

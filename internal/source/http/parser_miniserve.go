package http

import (
	"fmt"
	"strings"

	"golang.org/x/net/html"
)

// parseMiniserveHTML 解析 miniserve ?raw=true 的简化 HTML 表：
// 行标记 <tr class="entry-type-directory|file">，条目锚点
// <a class="directory|file" href>。只从 DOM 取 name / isDir——
// miniserve 默认 --size-display human，页面大小是人类可读近似，
// 绝不解析；精确 metadata 由 HEAD（Range 兜底）补全。父目录导航行
// （href "../"）跳过。
func parseMiniserveHTML(m *mapper, dir string, body []byte) ([]rawEntry, error) {
	doc, err := html.Parse(strings.NewReader(string(body)))
	if err != nil {
		return nil, malformedListing(dir, fmt.Sprintf("invalid miniserve HTML: %v", err))
	}
	var (
		raw      []rawEntry
		sawRow   bool
		parseErr error
	)
	visitRows(doc, func(rowClass, href, anchorClass string) {
		// 父目录导航行跳过。
		if href == "../" || href == "./" || href == "/" || href == ".." {
			return
		}
		isDir := strings.Contains(rowClass, "entry-type-directory") ||
			strings.Contains(anchorClass, "directory")
		// 无 entry-type-* 行标记也无 anchor class 的行不是条目
		//（表头 / 隐藏行）；带 entry-type-file 但缺锚点的行同样
		// 形态不可信，交给 href 解析统一拒绝。
		sawRow = true
		entry, err := m.entryFromHref(dir, href)
		if err != nil {
			if parseErr == nil {
				parseErr = err
			}
			return
		}
		// 行级 class 优先于尾斜杠推断目录形态。
		if strings.Contains(rowClass, "entry-type-") {
			raw = append(raw, rawEntry{Name: entry.Name, IsDir: isDir})
			return
		}
		raw = append(raw, rawEntry{Name: entry.Name, IsDir: entry.IsDir})
	})
	if parseErr != nil {
		return nil, malformedListing(dir, parseErr.Error())
	}
	if !sawRow {
		return nil, malformedListing(dir, "miniserve HTML has no entry rows")
	}
	return collect(dir, raw)
}

// visitRows 遍历 <tr>：解析行 class、行内首个 <a href> 与锚点 class。
func visitRows(n *html.Node, fn func(rowClass, href, anchorClass string)) {
	if n.Type == html.ElementNode && n.Data == "tr" {
		rowClass := attrOf(n, "class")
		href, anchorClass := firstAnchor(n)
		if href != "" {
			fn(rowClass, href, anchorClass)
		}
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		visitRows(c, fn)
	}
}

// firstAnchor 返回子树内首个 <a> 的 href 与 class。
func firstAnchor(n *html.Node) (href, class string) {
	if n.Type == html.ElementNode && n.Data == "a" {
		return attrOf(n, "href"), attrOf(n, "class")
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if h, cl := firstAnchor(c); h != "" {
			return h, cl
		}
	}
	return "", ""
}

// attrOf 返回元素属性值。
func attrOf(n *html.Node, key string) string {
	for _, attr := range n.Attr {
		if attr.Key == key {
			return attr.Val
		}
	}
	return ""
}

// miniserveRawURL 为目录 listing URL 附加 ?raw=true（listing query 由
// adapter 自己控制，BaseURL 不携带 query）。
func miniserveRawURL(dirURL string) string {
	return dirURL + "?raw=true"
}

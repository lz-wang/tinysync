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
		parseErr error
	)
	visitRows(doc, func(rowClass, href, anchorClass string) {
		// 只有 entry-type-* 行标记或 file/directory 锚点 class 的行是
		// 条目行；表头（含 ?sort=... 排序控件）、隐藏行与导航行跳过
		//（真实 miniserve raw 页的表头锚点是纯 query href）。
		isEntryRow := strings.Contains(rowClass, "entry-type-") ||
			hasClass(anchorClass, "directory") || hasClass(anchorClass, "file")
		if !isEntryRow {
			return
		}
		// 父目录导航行跳过。
		if href == "../" || href == "./" || href == "/" || href == ".." {
			return
		}
		isDir := strings.Contains(rowClass, "entry-type-directory") ||
			hasClass(anchorClass, "directory")
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
	// 零条目行是合法形态（空目录：detection 以表头结构标记兜底，
	// 此处不再要求至少一行）。
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

// hasClass 报告空白分隔的 class 列表是否含指定 token（精确词匹配，
// 避免 "file" 误匹配 "profile"）。
func hasClass(class, want string) bool {
	for _, token := range strings.Fields(class) {
		if token == want {
			return true
		}
	}
	return false
}

// miniserveRawURL 为目录 listing URL 附加 ?raw=true（listing query 由
// adapter 自己控制，BaseURL 不携带 query）。
func miniserveRawURL(dirURL string) string {
	return dirURL + "?raw=true"
}

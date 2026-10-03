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
// （href "../"）跳过。symlink 条目（行标记 entry-type-symlink /
// 锚点 class=symlink，真实 miniserve 对指向目录与文件的链接均使用
// 该标记）fail-closed 拒绝（与 Caddy is_symlink 同一风格）：服务器
// 端推荐 --no-symlinks 只是测试配置，客户端必须自带防线。
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
		if parseErr != nil {
			return
		}
		rowType, err := miniserveEntryType(rowClass, "entry-type-")
		if err != nil {
			parseErr = err
			return
		}
		anchorType, err := miniserveEntryType(anchorClass, "")
		if err != nil {
			parseErr = err
			return
		}
		if rowType == "symlink" || anchorType == "symlink" {
			parseErr = fmt.Errorf("miniserve entry %q is a symlink; symlinks are not supported (fail-closed)", href)
			return
		}
		if rowType == "" && anchorType == "" {
			// 表头排序控件和非条目行不参与快照。
			return
		}
		if anchorType == "" || (rowType != "" && rowType != anchorType) {
			parseErr = fmt.Errorf("miniserve entry %q has conflicting or missing row/anchor type", href)
			return
		}
		// 仅确认是目录的父目录导航可以跳过。
		if anchorType == "directory" && (href == "../" || href == "./" || href == "/" || href == "..") {
			return
		}
		entry, err := m.entryFromHref(dir, href)
		if err != nil {
			parseErr = err
			return
		}
		if entry.IsDir != (anchorType == "directory") {
			parseErr = fmt.Errorf("miniserve entry %q has conflicting href/type", href)
			return
		}
		raw = append(raw, rawEntry{Name: entry.Name, IsDir: entry.IsDir})
	})
	if parseErr != nil {
		return nil, malformedListing(dir, parseErr.Error())
	}
	// parser 独立验证结构，不能仅信任 detector；零条目必须有完整表头。
	if !isMiniserveDOM(doc) {
		return nil, malformedListing(dir, "missing miniserve listing structure")
	}
	return collect(dir, raw)
}

// miniserveEntryType 按 class token enum 解析行或锚点类型。未知
// entry-type-* 与多个相冲突的类型一律失败，不能默认解释为文件。
func miniserveEntryType(class, prefix string) (string, error) {
	kind := ""
	for _, token := range strings.Fields(class) {
		if prefix != "" && !strings.HasPrefix(token, prefix) {
			continue
		}
		typ := strings.TrimPrefix(token, prefix)
		switch typ {
		case "file", "directory", "symlink":
		default:
			if prefix == "" {
				continue
			}
			return "", fmt.Errorf("unknown miniserve entry type %q", token)
		}
		if kind != "" && kind != typ {
			return "", fmt.Errorf("conflicting miniserve entry types in %q", class)
		}
		kind = typ
	}
	return kind, nil
}

func detectMiniserveHTML(body []byte) bool {
	doc, err := html.Parse(strings.NewReader(string(body)))
	return err == nil && isMiniserveDOM(doc)
}

// isMiniserveDOM 要求真实条目行与带类型的锚点，或同一 table 内
// thead 的 name/size/date 三列表头与 tbody。脚本、注释和文本中的
// class 字符串不构成目录索引证据。
func isMiniserveDOM(doc *html.Node) bool {
	if doc.Type == html.ElementNode && doc.Data == "table" {
		recognizedRow := false
		visitRows(doc, func(rowClass, href, anchorClass string) {
			// 导航行不能证明有条目；零条目只能由完整表头确认。
			if href == "../" || href == "./" || href == "/" || href == ".." {
				return
			}
			if href != "" && (hasClass(rowClass, "entry-type-file") ||
				hasClass(rowClass, "entry-type-directory") || hasClass(rowClass, "entry-type-symlink")) &&
				(hasClass(anchorClass, "file") || hasClass(anchorClass, "directory") || hasClass(anchorClass, "symlink")) {
				recognizedRow = true
			}
		})
		if recognizedRow || hasMiniserveHeader(doc) {
			return true
		}
	}
	for child := doc.FirstChild; child != nil; child = child.NextSibling {
		if isMiniserveDOM(child) {
			return true
		}
	}
	return false
}

func hasMiniserveHeader(table *html.Node) bool {
	var head, body *html.Node
	for child := table.FirstChild; child != nil; child = child.NextSibling {
		if child.Type != html.ElementNode {
			continue
		}
		switch child.Data {
		case "thead":
			head = child
		case "tbody":
			body = child
		}
	}
	return head != nil && body != nil && hasHeaderClass(head, "name") &&
		hasHeaderClass(head, "size") && hasHeaderClass(head, "date")
}

func hasHeaderClass(node *html.Node, class string) bool {
	if node.Type == html.ElementNode && node.Data == "th" && hasClass(attrOf(node, "class"), class) {
		return true
	}
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		if hasHeaderClass(child, class) {
			return true
		}
	}
	return false
}

// visitRows 遍历 <tr>：解析行 class、行内首个 <a href> 与锚点 class。
func visitRows(n *html.Node, fn func(rowClass, href, anchorClass string)) {
	if n.Type == html.ElementNode && n.Data == "tr" {
		rowClass := attrOf(n, "class")
		href, anchorClass := firstAnchor(n)
		fn(rowClass, href, anchorClass)
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

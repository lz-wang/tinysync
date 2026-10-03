// Package http 实现 source 的 HTTP Web Server 只读 Remote（ADR 0009）：
// 以 nginx autoindex / Caddy file_server browse / miniserve 这类 HTTP
// 目录索引为同步源。三种 Web Server 的差异只停留在 listing parser /
// detector 一层，transport / 认证 / 重定向 / URL confinement / metadata
// / 下载全部共用。HTTP 文件服务在 TinySync 中只视为只读源：不实现
// DirectoryCreator。
package http

import (
	"fmt"
	"net/url"
	"strings"

	"tinysync/internal/source"
)

// mapper 把 Source logical path 映射到 BaseURL 子树内的请求 URL，并
// 校验外部提供的 URL（redirect 目标 / listing href）没有越出该子树。
// 请求 URL 一律由 logical path 逐 segment PathEscape 重建——listing
// 里的 <a href> 只作为发现 entry 的输入，绝不直接作为下载 URL。
type mapper struct {
	scheme string
	// host 是 host[:port]，redirect 同源判定（scheme + host 全等）用。
	host string
	// basePath 是解码后的绝对 path，恒以 / 结尾；Source "/" 即该子树。
	basePath string
	// baseRaw 是重建的 canonical BaseURL 前缀（scheme://host/<escaped
	// basePath>，以 / 结尾），拼接 segment 即得请求 URL。
	baseRaw string
}

// newMapper 解析 canonical BaseURL（PrepareConfig 已保证形态；此处
// 防御性复检，绕过 Service 直连 Factory 的路径同样 fail-closed）。
func newMapper(baseURL string) (*mapper, error) {
	u, err := url.Parse(baseURL)
	if err != nil {
		return nil, source.MarkPermanent(fmt.Errorf("parse http base_url %q: %w", baseURL, err))
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, source.MarkPermanent(fmt.Errorf("http base_url scheme must be http or https, got %q", u.Scheme))
	}
	if u.Host == "" {
		return nil, source.MarkPermanent(fmt.Errorf("http base_url host is required"))
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.RawPath != "" {
		return nil, source.MarkPermanent(fmt.Errorf("http base_url %q is not in canonical form", baseURL))
	}
	m := &mapper{
		scheme:   u.Scheme,
		host:     stripDefaultPort(u.Scheme, u.Host),
		basePath: trailingSlash(u.Path),
	}
	// basePath 逐 segment 重建 escaped 前缀（与 logical path 同一转义
	// 规则，保证前缀在 escaped 与 decoded 两个层面一致）。
	escaped := escapeSegments(m.basePath)
	if escaped == "" {
		m.baseRaw = fmt.Sprintf("%s://%s/", m.scheme, m.host)
	} else {
		m.baseRaw = fmt.Sprintf("%s://%s/%s/", m.scheme, m.host, escaped)
	}
	return m, nil
}

// trailingSlash 返回以 / 结尾的绝对 path；空 path 视为根 "/"。
func trailingSlash(p string) string {
	if p == "" {
		return "/"
	}
	if strings.HasSuffix(p, "/") {
		return p
	}
	return p + "/"
}

// directoryURL 返回目录 listing 的请求 URL（目录恒以 / 结尾，避免
// 依赖服务器 canonical redirect 才能拿到 listing）。
func (m *mapper) directoryURL(logical string) string {
	if logical == "/" {
		return m.baseRaw
	}
	return m.baseRaw + escapeSegments(logical) + "/"
}

// fileURL 返回文件的请求 URL（不带尾斜杠）。
func (m *mapper) fileURL(logical string) string {
	if logical == "/" {
		return m.baseRaw
	}
	return m.baseRaw + escapeSegments(logical)
}

// escapeSegments 把 validated logical path 的每个 segment 独立转义后
// 用 / 连接；"?"、"#"、空格、非 ASCII 等只在 segment 内合法的字符
// 全部被编码，不产生额外层级。根 path 返回空串。
func escapeSegments(logical string) string {
	trimmed := strings.Trim(logical, "/")
	if trimmed == "" {
		return ""
	}
	segs := strings.Split(trimmed, "/")
	escaped := make([]string, len(segs))
	for i, seg := range segs {
		escaped[i] = url.PathEscape(seg)
	}
	return strings.Join(escaped, "/")
}

// contains 判断 target 是否位于 BaseURL 子树内且同源（scheme 与
// host[:port] 全等——https→http、跨主机、跨端口一律视为越界，防止
// Basic / Bearer credential 被重定向到其它服务器；默认端口与省略
// 形式语义等价）。
func (m *mapper) contains(target *url.URL) bool {
	if target.Scheme != m.scheme || stripDefaultPort(target.Scheme, target.Host) != m.host {
		return false
	}
	return strings.HasPrefix(trailingSlash(target.Path), m.basePath)
}

// stripDefaultPort 归一 origin 比较：http 的 :80 与 https 的 :443 和
// 省略形式等价。
func stripDefaultPort(scheme, host string) string {
	switch scheme {
	case "http":
		return strings.TrimSuffix(host, ":80")
	case "https":
		return strings.TrimSuffix(host, ":443")
	}
	return host
}

// hrefEntry 是 listing href 解析出的单个条目。
type hrefEntry struct {
	// Name 是解码后的单一 segment 文件名（不含 /）。
	Name string
	// IsDir 来自 href 的尾斜杠（HTML listing 的目录约定）。
	IsDir bool
}

// entryFromHref 把 listing 中的 <a href> 解析为 dir 下的单个条目：
// 相对引用按目录 URL 解析，解析结果必须仍位于 BaseURL 子树、恰好是
// dir 下的一段 clean segment——"../"、跨层级、越子树、编码分隔符
// （%2F 解码后变成多段）与反斜杠 / NUL 一律拒绝。href 的 query /
// fragment 不参与条目身份（如 miniserve 的 ?raw=true），比较前剥离。
// 调用方先跳过 "../" 等导航链接；本函数返回的错误意味着 listing
// 形态不可信（malformed → permanent）。
func (m *mapper) entryFromHref(dir string, href string) (hrefEntry, error) {
	ref, err := url.Parse(href)
	if err != nil {
		return hrefEntry{}, fmt.Errorf("parse listing href %q: %w", href, err)
	}
	// 相对引用按目录 URL 解析；绝对 URL（ref.IsAbs()）由
	// ResolveReference 原样保留，随后经 contains 做同源判定。
	resolved := (&url.URL{Scheme: m.scheme, Host: m.host, Path: m.dirPath(dir)}).ResolveReference(ref)
	resolved.RawQuery = ""
	resolved.Fragment = ""
	if !m.contains(resolved) {
		return hrefEntry{}, fmt.Errorf("listing href %q escapes source base URL", href)
	}
	rel := strings.TrimSuffix(strings.TrimPrefix(resolved.Path, m.basePath), "/")
	if dir != "/" {
		parent := strings.Trim(dir, "/")
		if !strings.HasPrefix(rel+"/", parent+"/") {
			return hrefEntry{}, fmt.Errorf("listing href %q is not a child of %q", href, dir)
		}
		rel = strings.TrimPrefix(rel, parent+"/")
	}
	if rel == "" || rel == "." || rel == ".." || strings.ContainsAny(rel, `/\`+"\x00") {
		return hrefEntry{}, fmt.Errorf("listing href %q is not a single clean path segment", href)
	}
	return hrefEntry{Name: rel, IsDir: strings.HasSuffix(ref.Path, "/")}, nil
}

// dirPath 返回目录 logical path 对应的解码 URL path（basePath +
// 子路径，以 / 结尾）。
func (m *mapper) dirPath(dir string) string {
	if dir == "/" {
		return m.basePath
	}
	return trailingSlash(m.basePath + strings.TrimPrefix(dir, "/"))
}

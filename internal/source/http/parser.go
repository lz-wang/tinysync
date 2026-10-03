package http

import (
	"fmt"
	"strings"
	"time"

	"tinysync/internal/source"
)

// listingKind 标识目录索引的具体形态（ADR 0009：nginx / Caddy /
// miniserve 只是同一 http 类型下的 listing profile）。
type listingKind int

const (
	listingUnknown listingKind = iota
	listingNginxJSON
	listingNginxHTML
	listingCaddyJSON
	listingMiniserveHTML
)

// String 供错误消息与测试断言使用。
func (k listingKind) String() string {
	switch k {
	case listingNginxJSON:
		return "nginx-json"
	case listingNginxHTML:
		return "nginx-html"
	case listingCaddyJSON:
		return "caddy-json"
	case listingMiniserveHTML:
		return "miniserve-html"
	default:
		return "unknown"
	}
}

// rawEntry 是 parser 从 listing 提取的单个条目。JSON listing 自带
// 精确 size / mtime；HTML listing 只有名字与目录形态，精确 metadata
// 由调用方经 HEAD（Range 兜底）补全——页面显示值是人类可读近似，
// 绝不能进入 Fingerprint。
type rawEntry struct {
	Name  string
	IsDir bool
	// SizeKnown 为 true 时 Size 是精确值（JSON listing）。
	SizeKnown bool
	Size      int64
	// ModifiedAt 可能为零值（listing 不提供且 HEAD 未补全时）。
	ModifiedAt time.Time
}

// parser 把一种 listing 形态解析为 dir 的直接子条目。
type parser func(m *mapper, dir string, body []byte) ([]rawEntry, error)

// parsers 是形态 → parser 的注册表。
var parsers = map[listingKind]parser{
	listingNginxJSON:     parseNginxJSON,
	listingNginxHTML:     parseNginxHTML,
	listingCaddyJSON:     parseCaddyJSON,
	listingMiniserveHTML: parseMiniserveHTML,
}

// parseListing 按形态分派解析。
func parseListing(kind listingKind, m *mapper, dir string, body []byte) ([]rawEntry, error) {
	p, ok := parsers[kind]
	if !ok {
		return nil, unsupportedListingError(fmt.Sprintf(" (listing kind %s)", kind))
	}
	return p(m, dir, body)
}

// collect 校验条目名并收集（去重 + 单 clean segment + file/dir 冲突
// 拒绝）：重复名意味着 listing 形态不可信（malformed → permanent）。
func collect(dir string, entries []rawEntry) ([]rawEntry, error) {
	seen := make(map[string]bool, len(entries))
	out := make([]rawEntry, 0, len(entries))
	for _, e := range entries {
		name := e.Name
		if name == "" || name == "." || name == ".." || strings.ContainsAny(name, `/\`+"\x00") {
			return nil, malformedListing(dir, fmt.Sprintf("entry name %q is not a single clean path segment", e.Name))
		}
		if seen[name] {
			return nil, malformedListing(dir, fmt.Sprintf("duplicate entry %q", name))
		}
		seen[name] = true
		out = append(out, e)
	}
	return out, nil
}

// malformedListing 返回 permanent 错误：listing 解析失败是确定性失败。
func malformedListing(dir, detail string) error {
	return source.MarkPermanent(fmt.Errorf("malformed directory listing at %s: %s", dir, detail))
}

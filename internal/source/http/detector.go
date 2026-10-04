package http

import (
	"bytes"
	"encoding/json"
)

// detectListing 按响应形态识别 listing profile（ADR 0009）：不依赖
// Server header（reverse proxy / CDN / 隐藏版本都会让它失效），JSON
// 按字段集区分（is_dir + is_symlink + mod_time → caddy；type + mtime
// → nginx），HTML 按结构标记区分（miniserve row class / nginx
// autoindex DOM）。无法识别的形态返回 listingUnknown——调用方明确
// 失败（unsupported HTTP directory listing），绝不静默当作空目录或
// 加入 generic HTML crawler。
func detectListing(body []byte) listingKind {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) > 0 && (trimmed[0] == '[' || trimmed[0] == '{') {
		return detectJSONListing(trimmed)
	}
	if detectMiniserveHTML(trimmed) {
		return listingMiniserveHTML
	}
	if detectNginxHTML(trimmed) {
		return listingNginxHTML
	}
	return listingUnknown
}

// detectJSONListing 区分 nginx JSON（顶层数组，条目含 type/mtime/size）
// 与 Caddy JSON（Accept: application/json 的 browse 输出，顶层数组，
// 条目含 is_dir/is_symlink/mod_time）。只接受正式的顶层数组：顶层
// JSON object 一律 unknown——`{}`、`{"status":"ok"}` 这类响应无法与
// 「字段全部缺失的包装形态」区分，把字段缺失当作空数组会授权 Mirror
// 删除全部 managed files（fail-closed）。空数组无法证明 profile，只
// 能由显式配置解释；非空数组的每个条目都必须满足同一 profile。
func detectJSONListing(b []byte) listingKind {
	var arr []map[string]json.RawMessage
	if err := json.Unmarshal(b, &arr); err != nil || arr == nil {
		return listingUnknown
	}
	if len(arr) == 0 {
		return listingEmptyJSON
	}
	kind := sniffEntryFields(arr[0])
	for _, entry := range arr[1:] {
		if sniffEntryFields(entry) != kind {
			return listingUnknown
		}
	}
	return kind
}

// sniffEntryFields 要求必填字段存在且非 null，避免缺失 bool 被解释为 false。
func sniffEntryFields(entry map[string]json.RawMessage) listingKind {
	caddy := hasRequiredFields(entry, caddyRequiredFields...)
	nginx := hasRequiredFields(entry, "name", "type", "mtime")
	if nginx && !bytes.Equal(bytes.TrimSpace(entry["type"]), []byte(`"directory"`)) {
		nginx = hasRequiredFields(entry, "size")
	}
	if caddy && !nginx {
		return listingCaddyJSON
	}
	if nginx && !caddy {
		return listingNginxJSON
	}
	return listingUnknown
}

func hasRequiredFields(entry map[string]json.RawMessage, fields ...string) bool {
	for _, field := range fields {
		value, ok := entry[field]
		if !ok || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return false
		}
	}
	return true
}

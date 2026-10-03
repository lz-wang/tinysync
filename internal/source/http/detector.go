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
	if bytes.Contains(trimmed, []byte("entry-type-directory")) ||
		bytes.Contains(trimmed, []byte("entry-type-file")) {
		return listingMiniserveHTML
	}
	// 空目录的 miniserve raw 页没有条目行，只剩表头结构标记
	//（真实输出：<thead><th class="name">Name</th>…）。
	if bytes.Contains(trimmed, []byte(`<th class="name">`)) {
		return listingMiniserveHTML
	}
	// nginx autoindex HTML 的稳定结构标记：标题「Index of /」。
	if bytes.Contains(trimmed, []byte("<title>Index of /")) {
		return listingNginxHTML
	}
	return listingUnknown
}

// detectJSONListing 区分 nginx JSON（顶层数组，条目含 type/mtime/size）
// 与 Caddy JSON（Accept: application/json 的 browse 输出，条目含
// is_dir/is_symlink/mod_time；兼容顶层对象 + items/files 数组的包装
// 形态）。空数组按 nginx 形态处理（空目录对两种 parser 语义等价）。
func detectJSONListing(b []byte) listingKind {
	var arr []map[string]json.RawMessage
	if err := json.Unmarshal(b, &arr); err == nil {
		if len(arr) == 0 {
			return listingNginxJSON
		}
		return sniffEntryFields(arr[0])
	}
	var obj struct {
		Items []map[string]json.RawMessage `json:"items"`
		Files []map[string]json.RawMessage `json:"files"`
	}
	if err := json.Unmarshal(b, &obj); err == nil {
		entries := obj.Items
		if len(entries) == 0 {
			entries = obj.Files
		}
		if len(entries) == 0 {
			return listingCaddyJSON
		}
		return sniffEntryFields(entries[0])
	}
	return listingUnknown
}

// sniffEntryFields 按首条目的字段集判定形态。
func sniffEntryFields(entry map[string]json.RawMessage) listingKind {
	_, isDir := entry["is_dir"]
	_, isSymlink := entry["is_symlink"]
	_, modTime := entry["mod_time"]
	_, typ := entry["type"]
	_, mtime := entry["mtime"]
	if isDir && isSymlink && modTime {
		return listingCaddyJSON
	}
	if typ && mtime {
		return listingNginxJSON
	}
	return listingUnknown
}

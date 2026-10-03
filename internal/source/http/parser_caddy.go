package http

import (
	"encoding/json"
	"fmt"
	"time"

	"tinysync/internal/source"
)

// caddyEntry 是 Caddy file_server browse 在 Accept: application/json
// 下返回的条目：name / size / url / mod_time / mode / is_dir /
// is_symlink。兼容顶层数组与 {items|files: [...]} 两种包装形态。
type caddyEntry struct {
	Name      string    `json:"name"`
	Size      int64     `json:"size"`
	URL       string    `json:"url"`
	ModTime   time.Time `json:"mod_time"`
	Mode      uint32    `json:"mode"`
	IsDir     bool      `json:"is_dir"`
	IsSymlink bool      `json:"is_symlink"`
}

// parseCaddyJSON 解析 Caddy JSON listing。可识别的 symlink 一律拒绝
// （fail-closed，与 Local / SMB 同一风格）：Caddy 官方明确 file
// server root 不是文件系统 sandbox，root 内 symlink 仍可能指向
// root 外；跟随破坏 root confinement，静默跳过破坏 Mirror 删除
// 安全语义。size 为精确字节值。
func parseCaddyJSON(m *mapper, dir string, body []byte) ([]rawEntry, error) {
	entries, err := decodeCaddyEntries(body)
	if err != nil {
		return nil, malformedListing(dir, fmt.Sprintf("invalid caddy JSON: %v", err))
	}
	raw := make([]rawEntry, 0, len(entries))
	for _, e := range entries {
		if e.IsSymlink {
			return nil, source.MarkPermanent(fmt.Errorf(
				"caddy entry %q is a symlink; symlinks are not supported (fail-closed)", joinLogical(dir, e.Name)))
		}
		entry := rawEntry{
			Name:       e.Name,
			IsDir:      e.IsDir,
			SizeKnown:  true,
			Size:       e.Size,
			ModifiedAt: e.ModTime,
		}
		if e.IsDir {
			entry.SizeKnown = false
			entry.Size = 0
		}
		raw = append(raw, entry)
	}
	return collect(dir, raw)
}

// decodeCaddyEntries 兼容顶层数组与 {items|files: [...]} 包装。
func decodeCaddyEntries(body []byte) ([]caddyEntry, error) {
	var arr []caddyEntry
	if err := json.Unmarshal(body, &arr); err == nil {
		return arr, nil
	}
	var obj struct {
		Items []caddyEntry `json:"items"`
		Files []caddyEntry `json:"files"`
	}
	if err := json.Unmarshal(body, &obj); err != nil {
		return nil, err
	}
	if len(obj.Items) > 0 {
		return obj.Items, nil
	}
	return obj.Files, nil
}

// joinLogical 拼接 dir 与 name 的 logical path（"/" 根特殊处理）。
func joinLogical(dir, name string) string {
	if dir == "/" {
		return "/" + name
	}
	return dir + "/" + name
}

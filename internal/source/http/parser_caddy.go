package http

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"tinysync/internal/source"
)

// caddyEntry 是 Caddy file_server browse 在 Accept: application/json
// 下返回的条目：name / size / url / mod_time / mode / is_dir /
// is_symlink。listing 主体是顶层 JSON 数组（与 detector 的识别口径
// 一致；不支持 {items|files} 包装形态——字段缺失与空数组不可区分，
// 见 detectJSONListing）。
type caddyEntry struct {
	Name      string    `json:"name"`
	Size      int64     `json:"size"`
	URL       string    `json:"url"`
	ModTime   time.Time `json:"mod_time"`
	Mode      uint32    `json:"mode"`
	IsDir     bool      `json:"is_dir"`
	IsSymlink bool      `json:"is_symlink"`
}

var caddyRequiredFields = []string{"name", "size", "url", "mod_time", "is_dir", "is_symlink"}

// UnmarshalJSON 逐条验证 wire schema；缺失 / null 字段不能落入 Go
// 零值，尤其 is_dir 缺失会漏扫 managed descendants。
func (e *caddyEntry) UnmarshalJSON(body []byte) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil {
		return err
	}
	for _, field := range caddyRequiredFields {
		if !hasRequiredFields(fields, field) {
			return fmt.Errorf("caddy entry missing or null required field %q", field)
		}
	}
	type wireEntry caddyEntry
	return json.Unmarshal(body, (*wireEntry)(e))
}

// parseCaddyJSON 解析 Caddy JSON listing（顶层数组）。可识别的
// symlink 一律拒绝（fail-closed，与 Local / SMB 同一风格）：Caddy
// 官方明确 file server root 不是文件系统 sandbox，root 内 symlink
// 仍可能指向 root 外；跟随破坏 root confinement，静默跳过破坏
// Mirror 删除安全语义。size 为精确字节值。
func parseCaddyJSON(m *mapper, dir string, body []byte) ([]rawEntry, error) {
	var entries []caddyEntry
	if err := json.Unmarshal(body, &entries); err != nil {
		return nil, malformedListing(dir, fmt.Sprintf("invalid caddy JSON: %v", err))
	}
	if entries == nil {
		return nil, malformedListing(dir, "invalid caddy JSON: expected an array, got null")
	}
	raw := make([]rawEntry, 0, len(entries))
	for _, e := range entries {
		if e.IsSymlink {
			return nil, source.MarkPermanent(fmt.Errorf(
				"caddy entry %q is a symlink; symlinks are not supported (fail-closed)", joinLogical(dir, e.Name)))
		}
		// 负 size 是畸形 listing：不得进入 Fingerprint（Planner 的大小
		// 比较与 Downloader 的字节数校验都依赖非负精确值）。
		if !e.IsDir && e.Size < 0 {
			return nil, malformedListing(dir, fmt.Sprintf("caddy entry %q has negative size %d", e.Name, e.Size))
		}
		// 真实 Caddy 的目录名带尾 "/"（与 nginx JSON 同一约定），统一剥掉。
		entry := rawEntry{
			Name:       strings.TrimSuffix(e.Name, "/"),
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

// joinLogical 拼接 dir 与 name 的 logical path（"/" 根特殊处理）。
func joinLogical(dir, name string) string {
	if dir == "/" {
		return "/" + name
	}
	return dir + "/" + name
}

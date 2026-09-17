// Package browser 是文件浏览的应用服务：Remote 浏览经
// source.Service.OpenRemote 统一入口复用协议抽象，Local 浏览以
// Job.LocalRoot 为唯一 namespace。REST API、Web UI 与未来 MCP
// 共用本层，不各自接触凭据、factory 或本地路径代数。
package browser

import (
	"errors"
	"path"
	"time"

	"tinysync/internal/source"
)

// 条目类型枚举（Kind 字段取值）。
const (
	KindFile      = "file"
	KindDirectory = "directory"
	KindSymlink   = "symlink"
	KindOther     = "other"
)

// 领域错误：API 层映射 HTTP 状态（400 / 404 / 502）。
var (
	// ErrInvalid 表示请求参数非法（路径、limit、cursor）。
	ErrInvalid = source.ErrInvalid
	// ErrNotFound 表示 Source / Job 或目标文件不存在。
	ErrNotFound = errors.New("not found")
	// ErrRemote 表示远端操作失败（连接、协议、超时以外的远端错误）。
	ErrRemote = errors.New("remote failure")
)

// Entry 是浏览器条目的协议无关展示模型。Managed 仅 Local 条目携带
// （nil = 不适用；Remote 条目永不为非 nil）。
type Entry struct {
	// Path 是以 / 分隔的逻辑路径（Remote：Source-relative；
	// Local：LocalRoot-relative），目录不带尾斜杠。
	Path string `json:"path"`
	// Name 是路径最后一段；根目录为 "/"。
	Name       string     `json:"name"`
	Kind       string     `json:"kind"`
	Size       int64      `json:"size"`
	ModifiedAt *time.Time `json:"modified_at"`
	// Managed 为 nil 表示不适用（Remote 条目）；Local 条目指向布尔。
	Managed *bool `json:"managed,omitempty"`
}

// NewEntryFromRemote 把远端 FileInfo 转换为展示条目。
func NewEntryFromRemote(fi source.FileInfo) Entry {
	return Entry{
		Path:       fi.Path,
		Name:       entryName(fi.Path),
		Kind:       kindOf(fi.IsDir),
		Size:       fi.Fingerprint.Size,
		ModifiedAt: optionalTime(fi.Fingerprint.ModifiedAt),
	}
}

// kindOf 由目录标志映射条目类型；Remote 侧不出现 symlink / other
// （SFTP adapter 对 symlink fail-fast，WebDAV / S3 无该语义）。
func kindOf(isDir bool) string {
	if isDir {
		return KindDirectory
	}
	return KindFile
}

// entryName 取路径最后一段；根目录返回 "/"。
func entryName(p string) string {
	if p == "/" {
		return "/"
	}
	return path.Base(p)
}

// optionalTime 把零值时间归一为 nil（避免输出 0001-01-01）。
func optionalTime(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	utc := t.UTC()
	return &utc
}

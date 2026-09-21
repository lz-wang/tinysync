package source

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"strconv"
	"time"
)

// Fingerprint 是协议无关的远端文件指纹，供同步引擎判定内容是否变化。
// 各协议只填充自己能提供的字段，其余保持零值。
type Fingerprint struct {
	Size       int64
	ModifiedAt time.Time
	// ETag 是 opaque token，绝不假定其格式（如 MD5）。
	ETag string
	// Checksum 与 Version 为未来协议预留（如 S3 的版本 ID）。
	Checksum string
	Version  string
}

// FileInfo 是远端文件 / 目录的协议无关描述。
type FileInfo struct {
	// Path 是以 / 分隔的 Source-relative 绝对逻辑路径，
	// "/" 表示 Source root 而不是服务器 root；目录不带尾斜杠。
	Path        string
	IsDir       bool
	Fingerprint Fingerprint
}

// List 分页语义的常量：DefaultListLimit 是未指定 limit 时的每页
// 条目数，MaxListLimit 是单页上限。REST 查询参数沿用同一规则，
// 越界在 API 层拒绝；adapter 内部只做归一。
const (
	DefaultListLimit = 100
	MaxListLimit     = 500
)

// ListOptions 是 List 的分页参数。
type ListOptions struct {
	// Limit 是本页最大条目数；<=0 取 DefaultListLimit，超过
	// MaxListLimit 截断为 MaxListLimit。
	Limit int
	// Cursor 是上一页返回的 NextCursor，原样回传；空串表示从头
	// 开始。cursor 是 adapter-owned opaque token：调用方不解析、
	// 不改写、不假设其内部结构。
	Cursor string
}

// FilePage 是 List 的一页结果。Entries 为空时 NextCursor 必为空
// （adapter 不允许空页死循环）；NextCursor 为空串表示枚举结束
// （EOF）。
type FilePage struct {
	Entries    []FileInfo
	NextCursor string
}

// NormalizeListLimit 归一分页上限：<=0 取默认值，超过上限截断。
// adapter 与 REST 层共用，保证全工程单一分页语义。
func NormalizeListLimit(limit int) int {
	switch {
	case limit <= 0:
		return DefaultListLimit
	case limit > MaxListLimit:
		return MaxListLimit
	default:
		return limit
	}
}

// EncodeListOffset 把切片分页的 offset 编码为 opaque cursor token，
// 供采用「单层完整枚举 + 切片」策略的 adapter（WebDAV / SFTP）共用。
// token 对调用方保持不透明：内部是 offset 并不构成对外契约。
func EncodeListOffset(offset int) string {
	return base64.RawURLEncoding.EncodeToString([]byte(strconv.Itoa(offset)))
}

// DecodeListOffset 解码 EncodeListOffset 生成的 cursor；空串按从头
// 开始处理。非法 token 返回 ErrInvalid——伪造或跨 adapter 的 cursor
// 在边界整体失败，而不是静默从头开始得到重复页。
func DecodeListOffset(cursor string) (int, error) {
	if cursor == "" {
		return 0, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return 0, fmt.Errorf("%w: malformed list cursor", ErrInvalid)
	}
	offset, err := strconv.Atoi(string(raw))
	if err != nil || offset < 0 {
		return 0, fmt.Errorf("%w: malformed list cursor", ErrInvalid)
	}
	return offset, nil
}

// PageSlice 对已完整枚举的单层条目执行切片分页，供 WebDAV / SFTP
// 共用：cursor 解码为 offset，返回 entries[offset:offset+limit]。
// offset 越界按 EOF 处理（空 Entries + 空 NextCursor），limit 经
// NormalizeListLimit 归一。返回切片与 entries 共享底层数组，调用方
// 不得就地修改。
func PageSlice(entries []FileInfo, opts ListOptions) (FilePage, error) {
	offset, err := DecodeListOffset(opts.Cursor)
	if err != nil {
		return FilePage{}, err
	}
	limit := NormalizeListLimit(opts.Limit)
	if offset >= len(entries) {
		return FilePage{Entries: []FileInfo{}}, nil
	}
	end := offset + limit
	if end > len(entries) {
		end = len(entries)
	}
	next := ""
	if end < len(entries) {
		next = EncodeListOffset(end)
	}
	return FilePage{Entries: entries[offset:end], NextCursor: next}, nil
}

// Remote 是 Source 的只读远端访问接口，协议无关：
// 远端逻辑路径统一使用 /；不暴露协议特定对象给上层；
// 只包含同步所需能力，不提供 Upload / Delete / Rename / Move。
// List 分页返回：cursor 为 adapter-owned opaque token（S3 映射
// ContinuationToken；WebDAV / SFTP 为单层枚举后的切片 offset），
// 空.NextCursor 表示 EOF。
type Remote interface {
	Stat(ctx context.Context, path string) (FileInfo, error)
	List(ctx context.Context, path string, opts ListOptions) (FilePage, error)
	Open(ctx context.Context, path string) (io.ReadCloser, error)
	// Close 释放 Remote 持有的连接与会话。无持久会话的协议（如
	// WebDAV）为显式空操作；有连接生命周期的协议（如 SFTP）必须
	// 关闭底层连接，使阻塞中的读取随 ctx 取消或 Close 退出。
	Close() error
}

// DirectoryCreator 是 Remote 可选的目录创建能力。同步读取契约仍由
// Remote 保持最小化；仅管理员在 Web UI 中浏览远端根目录时按需断言
// 此能力，避免把写入操作强加给第三方 Remote 实现。
type DirectoryCreator interface {
	Mkdir(ctx context.Context, path string) error
}

// TreeScanner 是 Remote 可选的全树扫描能力：面向同步引擎的批量枚举，
// 把「浏览分页」（List，供 Files / API 使用）与「同步扫描」（ScanTree）
// 两个访问模式分离。WebDAV / SFTP 的 List 是伪分页（协议层每次请求
// 都枚举整层目录）；S3 的 Delimiter 逐目录递归则把请求量放大到与
// 目录数线性相关。ScanTree 让 adapter 用协议原生的最高效方式一次
// 枚举整个子树：WebDAV / SFTP 每目录一次枚举、S3 flat prefix 扫描。
//
// Remote 实现方可选择性提供；同步引擎优先断言此能力，未实现时回退
// 既有 List 递归——测试 fake 与第三方 adapter 无需为此付出成本。
//
// ScanTree 契约：
//   - root 是 Source-relative logical path（"/" 或以 / 开头）；root
//     自身不 visit，其全部后代全量遍历。
//   - 文件与目录都必须 visit：目录条目用于维持 file/dir collision、
//     max depth 等安全语义（S3 等无目录协议从 key 推导虚拟目录）。
//   - 遍历顺序不作为契约，消费方不得依赖。
//   - 部分扫描等于失败：任何一层枚举错误都整体失败，绝不返回部分
//     结果（Mirror 的删除授权依赖完整快照）。
//   - 必须及时响应 ctx 取消。
//   - visit 返回错误时立即终止且原样透传（含调用方安全策略的拒绝）。
//   - 扫描只读：禁止对远端做任何变更。
type TreeScanner interface {
	ScanTree(ctx context.Context, root string, visit func(FileInfo) error) error
}

// RemoteFactory 按 Source 配置构造远端客户端。每个协议 adapter 实现
// 一份，经 RemoteRegistry 按类型 dispatch。
type RemoteFactory interface {
	// Type 声明本 factory 服务的协议类型；Registry 据此建立 dispatch。
	Type() Type
	// Create 用 Source 配置与凭据集合构造 Remote；ctx 用于可取消的
	// 连接建立（如 SFTP dial）。协议类型不支持时返回 ErrUnsupportedType。
	Create(ctx context.Context, s Source, credentials Credentials) (Remote, error)
}

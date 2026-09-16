package source

import (
	"context"
	"io"
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

// Remote 是 Source 的只读远端访问接口，协议无关：
// 远端逻辑路径统一使用 /；不暴露协议特定对象给上层；
// 只包含同步所需能力，不提供 Upload / Delete / Rename / Move。
// v0.2.0 仅 Connection Test 经 Stat 使用该接口；
// List / Open 为 v0.3.0 同步引擎预留。
type Remote interface {
	Stat(ctx context.Context, path string) (FileInfo, error)
	List(ctx context.Context, path string) ([]FileInfo, error)
	Open(ctx context.Context, path string) (io.ReadCloser, error)
	// Close 释放 Remote 持有的连接与会话。无持久会话的协议（如
	// WebDAV）为显式空操作；有连接生命周期的协议（如 SFTP）必须
	// 关闭底层连接，使阻塞中的读取随 ctx 取消或 Close 退出。
	Close() error
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

package source

import (
	"context"
	"io"
	"time"
)

// FileInfo 是远端文件 / 目录的协议无关描述。
type FileInfo struct {
	// Path 是以 / 分隔的绝对逻辑路径。
	Path string
	Size int64
	// IsDir 表示是否为目录。
	IsDir bool
	// ModTime 是修改时间；协议不提供时为零值。
	ModTime time.Time
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
}

// RemoteFactory 按 Source 配置构造远端客户端。
type RemoteFactory interface {
	// Create 用 Source 配置与密码明文构造 Remote；
	// 协议类型不支持时返回 ErrUnsupportedType。
	Create(s Source, password string) (Remote, error)
}

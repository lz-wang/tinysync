package browser

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"time"

	"tinysync/internal/source"
)

// RemoteService 是远端文件浏览的应用服务：list / stat / download
// 全部经 source.Service.OpenRemote 完成——凭据查询与协议 dispatch
// 不在 browser 出现第二入口。Remote 生命周期由本层管理（用毕必关），
// 调用方不接触 source.Remote。
type RemoteService struct {
	sources *source.Service
}

// NewRemoteService 构造远端浏览服务。
func NewRemoteService(sources *source.Service) *RemoteService {
	return &RemoteService{sources: sources}
}

// List 列出 Source 的一层目录（分页参数透传给协议 adapter）。
// limit 与 cursor 语义与 Remote.List 契约一致。返回条目与下一页
// cursor（空串 = EOF）。
func (s *RemoteService) List(ctx context.Context, sourceID, logicalDir string, opts source.ListOptions) ([]Entry, string, error) {
	if err := source.ValidateLogicalPath(logicalDir); err != nil {
		return nil, "", err
	}
	src, remote, err := s.sources.OpenRemote(ctx, sourceID)
	if err != nil {
		return nil, "", mapOpenError(err)
	}
	_ = src
	page, listErr := remote.List(ctx, logicalDir, opts)
	closeErr := remote.Close()
	if listErr != nil {
		return nil, "", mapRemoteError("list", logicalDir, listErr)
	}
	if closeErr != nil {
		return nil, "", mapRemoteError("close", logicalDir, closeErr)
	}
	entries := make([]Entry, 0, len(page.Entries))
	for _, fi := range page.Entries {
		entries = append(entries, NewEntryFromRemote(fi))
	}
	return entries, page.NextCursor, nil
}

// Stat 读取单个远端路径的元信息。
func (s *RemoteService) Stat(ctx context.Context, sourceID, logicalPath string) (Entry, error) {
	if err := source.ValidateLogicalPath(logicalPath); err != nil {
		return Entry{}, err
	}
	src, remote, err := s.sources.OpenRemote(ctx, sourceID)
	if err != nil {
		return Entry{}, mapOpenError(err)
	}
	_ = src
	fi, statErr := remote.Stat(ctx, logicalPath)
	closeErr := remote.Close()
	if statErr != nil {
		return Entry{}, mapRemoteError("stat", logicalPath, statErr)
	}
	if closeErr != nil {
		return Entry{}, mapRemoteError("close", logicalPath, closeErr)
	}
	return NewEntryFromRemote(fi), nil
}

// DownloadMeta 是下载响应所需的元信息（Stat 可得时填充）。SizeKnown
// 区分「Stat 不可得、长度未知」与「真实长度为 0」：后者也要输出
// Content-Length: 0，不能用 0 值同时表达两种语义。
type DownloadMeta struct {
	Size       int64
	SizeKnown  bool
	ModifiedAt *time.Time
}

// Open 打开远端文件读取流。返回的 read 与 release 成对出现：release
// 关闭文件流与底层 Remote（SFTP 等有连接协议），请求结束时必须调用。
// Stat 仅用于元信息，失败不阻塞下载；目录在下载前显式拒绝。
func (s *RemoteService) Open(ctx context.Context, sourceID, logicalPath string) (DownloadMeta, io.ReadCloser, func() error, error) {
	if err := source.ValidateLogicalPath(logicalPath); err != nil {
		return DownloadMeta{}, nil, nil, err
	}
	_, remote, oerr := s.sources.OpenRemote(ctx, sourceID)
	if oerr != nil {
		return DownloadMeta{}, nil, nil, mapOpenError(oerr)
	}
	fi, statErr := remote.Stat(ctx, logicalPath)
	if statErr == nil && fi.IsDir {
		_ = remote.Close()
		return DownloadMeta{}, nil, nil, fmt.Errorf("%w: %s is a directory", ErrInvalid, logicalPath)
	}
	body, openErr := remote.Open(ctx, logicalPath)
	if openErr != nil {
		_ = remote.Close()
		return DownloadMeta{}, nil, nil, mapRemoteError("open", logicalPath, openErr)
	}
	release := func() error {
		closeErr := body.Close()
		if rErr := remote.Close(); rErr != nil && closeErr == nil {
			closeErr = rErr
		}
		return closeErr
	}
	var meta DownloadMeta
	if statErr == nil {
		meta = DownloadMeta{
			Size:       fi.Fingerprint.Size,
			SizeKnown:  true,
			ModifiedAt: optionalTime(fi.Fingerprint.ModifiedAt),
		}
	}
	return meta, body, release, nil
}

// mapOpenError 把 OpenRemote 的错误映射为领域错误：Source 不存在是
// 404，其余（凭据读取、协议 factory 失败——如 SFTP dial / 认证 /
// host key 校验）按远端失败处理。
func mapOpenError(err error) error {
	if errors.Is(err, source.ErrNotFound) {
		return fmt.Errorf("%w: source not found", ErrNotFound)
	}
	return fmt.Errorf("%w: %v", ErrRemote, err)
}

// mapRemoteError 把远端操作错误映射为领域错误：目标不存在（SFTP /
// WebDAV 经底层 fs.ErrNotExist 判定）→ ErrNotFound；取消与超时原样
// 传递（请求终止）；其余归入 ErrRemote。
func mapRemoteError(op, path string, err error) error {
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return fmt.Errorf("%w: %s not found", ErrNotFound, path)
	case errors.Is(err, source.ErrNotFound):
		return fmt.Errorf("%w: %s not found", ErrNotFound, path)
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return err
	case errors.Is(err, source.ErrInvalid):
		return fmt.Errorf("%w: %v", ErrInvalid, err)
	default:
		return fmt.Errorf("%w: %s %s: %v", ErrRemote, op, path, err)
	}
}

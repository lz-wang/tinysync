package mcp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"strings"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"tinysync/internal/auth"
	"tinysync/internal/browser"
	"tinysync/internal/filesafe"
)

// maxResourceSize 是 MCP inline resource 的内容上限（256 KiB）：
// 只承载小型文本，大文件经 get_file_info 的 download_url 走现有 HTTP。
const maxResourceSize = 256 << 10

// resourceMIMEType 是文本资源统一返回的 MIME。
const resourceMIMEType = "text/plain; charset=utf-8"

// registerResources 注册 resource template：
//
//	tinysync://jobs/{job_id}/files/{path}
//
// 显式携带 Job namespace，对应 Job → LocalRoot → logical path 模型。
// 读取经 LocalService.Open（filesafe 边界：拒绝目录、symlink 与父
// 目录逃逸）。缓存策略由 ServerOptions.SetCacheable 全局设定
// （cacheScope=private、ttlMs=0）。
func registerResources(server *mcp.Server, deps Deps) {
	if deps.LocalFiles == nil {
		return
	}
	server.AddResourceTemplate(
		&mcp.ResourceTemplate{
			URITemplate: "tinysync://jobs/{job_id}/files/{+path}",
			Name:        "job-file",
			MIMEType:    resourceMIMEType,
			Description: "Small UTF-8 text file synced by a TinySync job (<= 256 KiB, regular files only). Larger or binary files: use get_file_info and download via HTTP.",
		},
		func(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
			jobID, logicalPath, err := parseResourceURI(req.Params.URI)
			if err != nil {
				return nil, err
			}
			if err := authorize(ctx, auth.ScopeRead); err != nil {
				return nil, err
			}
			text, err := readTextResource(ctx, deps.LocalFiles, jobID, logicalPath)
			if err != nil {
				return nil, err
			}
			return &mcp.ReadResourceResult{
				Contents: []*mcp.ResourceContents{{
					URI:      req.Params.URI,
					MIMEType: resourceMIMEType,
					Text:     text,
				}},
			}, nil
		},
	)
}

// parseResourceURI 解析 tinysync://jobs/{job_id}/files/{path}：
// path 段保留 /，percent-encoding 解码后还原逻辑路径。
func parseResourceURI(raw string) (jobID, logicalPath string, err error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", "", fmt.Errorf("invalid resource uri")
	}
	if u.Scheme != "tinysync" || u.Host != "jobs" {
		return "", "", fmt.Errorf("unsupported resource uri scheme: only tinysync://jobs/{job_id}/files/{path} is provided")
	}
	segments := strings.SplitN(strings.TrimPrefix(u.Path, "/"), "/", 3)
	if len(segments) != 3 || segments[1] != "files" || segments[0] == "" || segments[2] == "" {
		return "", "", fmt.Errorf("malformed resource uri: want tinysync://jobs/{job_id}/files/{path}")
	}
	jobID, err = url.PathUnescape(segments[0])
	if err != nil {
		return "", "", fmt.Errorf("malformed resource uri: job id encoding")
	}
	logicalPath = "/" + segments[2]
	return jobID, logicalPath, nil
}

// readTextResource 打开普通文件并读取小型 UTF-8 文本：fast path 以
// Stat 尺寸拒绝明显超限的文件；读取经 LimitReader(max+1) 兜底
// stat/read 之间文件被替换超限的情况；内容必须为合法 UTF-8。
func readTextResource(ctx context.Context, files *browser.LocalService, jobID, logicalPath string) (string, error) {
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	_, f, info, err := files.Open(ctx, jobID, logicalPath)
	if err != nil {
		return "", mapResourceOpenError(err, logicalPath)
	}
	defer func() { _ = f.Close() }()
	if info.Size() > maxResourceSize {
		return "", fmt.Errorf("resource too large: %d bytes (max %d); use get_file_info and download via HTTP", info.Size(), maxResourceSize)
	}
	return readLimitedText(f)
}

// readLimitedText 从已打开的普通文件读取不超过 maxResourceSize 的
// UTF-8 文本：LimitReader 读 max+1 字节，读到 max+1 即说明实际内容
// 超限（stat/read 之间被替换），拒绝而非截断。
func readLimitedText(f *os.File) (string, error) {
	data, err := io.ReadAll(io.LimitReader(f, maxResourceSize+1))
	if err != nil {
		return "", errors.New("internal error")
	}
	if len(data) > maxResourceSize {
		return "", fmt.Errorf("resource too large: exceeds %d bytes on read; use get_file_info and download via HTTP", maxResourceSize)
	}
	if !utf8.Valid(data) {
		return "", fmt.Errorf("resource is not valid UTF-8 text; binary content must be downloaded via HTTP")
	}
	return string(data), nil
}

// mapResourceOpenError 把 LocalService.Open 的错误映射为稳定文案。
func mapResourceOpenError(err error, path string) error {
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return err
	case errors.Is(err, filesafe.ErrEscape), errors.Is(err, filesafe.ErrNotRegularFile):
		return fmt.Errorf("not a readable regular file: %s (directories and symlinks are not provided as resources)", path)
	case errors.Is(err, browser.ErrInvalid):
		return fmt.Errorf("invalid resource path: %v", err)
	case errors.Is(err, browser.ErrNotFound), errors.Is(err, fs.ErrNotExist):
		return fmt.Errorf("file not found: %s", path)
	default:
		return errors.New("internal error")
	}
}

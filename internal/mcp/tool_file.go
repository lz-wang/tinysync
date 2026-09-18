package mcp

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"tinysync/internal/auth"
	"tinysync/internal/browser"
	"tinysync/internal/filesafe"
)

// SearchFilesInput 是 search_files 的输入。
type SearchFilesInput struct {
	JobID string `json:"job_id"`
	Query string `json:"query"`
	Limit int    `json:"limit,omitempty"`
}

// GetFileInfoInput 是 get_file_info 的输入：path 为 LocalRoot 内的
// 逻辑路径（/ 开头）。
type GetFileInfoInput struct {
	JobID string `json:"job_id"`
	Path  string `json:"path"`
}

// fileInfoDetail 是文件条目的 MCP 输出：Entry 语义与 REST 一致，
// 并附 MCP 专属的 resource_uri 与 download_url。大文件不经 MCP
// 搬运，client 用 download_url 走现有 HTTP（同一 Bearer token）。
type fileInfoDetail struct {
	Path        string     `json:"path"`
	Name        string     `json:"name"`
	Kind        string     `json:"kind"`
	Size        int64      `json:"size"`
	ModifiedAt  *time.Time `json:"modified_at"`
	Managed     *bool      `json:"managed,omitempty"`
	ResourceURI string     `json:"resource_uri,omitempty"`
	DownloadURL string     `json:"download_url,omitempty"`
}

// searchFilesResult 是 search_files 的输出。
type searchFilesResult struct {
	JobID     string           `json:"job_id"`
	Query     string           `json:"query"`
	Entries   []fileInfoDetail `json:"entries"`
	Truncated bool             `json:"truncated"`
}

// resourceURI 构造 tinysync://jobs/{job_id}/files/{path} 资源 URI：
// 逐段 percent-encode（保留 / 分隔），空格、中文、#、?、% 等字符
// 在 URI 中安全表达，read 端按段解码还原。
func resourceURI(jobID, logicalPath string) string {
	segments := strings.Split(strings.TrimPrefix(logicalPath, "/"), "/")
	for i, seg := range segments {
		segments[i] = url.PathEscape(seg)
	}
	return "tinysync://jobs/" + url.PathEscape(jobID) + "/files/" + strings.Join(segments, "/")
}

// downloadURL 返回现有本地下载端点的 relative URL：client 相对
// MCP endpoint origin 解析，下载时携带同一 Bearer token。不产生
// absolute URL 或匿名临时链接。
func downloadURL(jobID, logicalPath string) string {
	return "/api/v1/jobs/" + url.PathEscape(jobID) + "/files/download?path=" + url.QueryEscape(logicalPath)
}

// toFileInfoDetail 转换 Entry：普通文件补充 resource_uri 与
// download_url；目录 / symlink / other 不提供（读取走 filesafe
// 边界、下载走本地文件语义，两者都只对普通文件有意义）。
func toFileInfoDetail(jobID string, e browser.Entry) fileInfoDetail {
	detail := fileInfoDetail{
		Path:       e.Path,
		Name:       e.Name,
		Kind:       e.Kind,
		Size:       e.Size,
		ModifiedAt: e.ModifiedAt,
		Managed:    e.Managed,
	}
	if e.Kind == browser.KindFile {
		detail.ResourceURI = resourceURI(jobID, e.Path)
		detail.DownloadURL = downloadURL(jobID, e.Path)
	}
	return detail
}

// registerFileTools 注册本地同步文件发现工具（read scope）。
func registerFileTools(server *mcp.Server, deps Deps) {
	if deps.LocalFiles == nil {
		return
	}
	mcp.AddTool(server, &mcp.Tool{
		Name: "search_files",
		Description: "Search files synced by one TinySync job by case-insensitive substring of the local relative path. " +
			"Only managed files whose local copy currently exists are returned; results are capped (truncated flag when more matches exist).",
		Annotations: readOnlyAnnotations(),
	}, func(ctx context.Context, req *mcp.CallToolRequest, input SearchFilesInput) (*mcp.CallToolResult, searchFilesResult, error) {
		if input.JobID == "" {
			return nil, searchFilesResult{}, errors.New("invalid input: job_id is required")
		}
		if err := authorize(ctx, auth.ScopeRead); err != nil {
			return nil, searchFilesResult{}, err
		}
		res, err := deps.LocalFiles.SearchManaged(ctx, input.JobID, browser.SearchOptions{
			Query: input.Query,
			Limit: input.Limit,
		})
		if err != nil {
			return nil, searchFilesResult{}, mapSearchError(err)
		}
		result := searchFilesResult{
			JobID:     input.JobID,
			Query:     input.Query,
			Entries:   make([]fileInfoDetail, 0, len(res.Entries)),
			Truncated: res.Truncated,
		}
		for _, e := range res.Entries {
			result.Entries = append(result.Entries, toFileInfoDetail(input.JobID, e))
		}
		return nil, result, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name: "get_file_info",
		Description: "Get metadata of one local file synced by a TinySync job (size, kind, managed flag), plus a resource URI for small UTF-8 text " +
			"and a relative download URL for large files (use the same bearer token; HTTP Range supported).",
		Annotations: readOnlyAnnotations(),
	}, func(ctx context.Context, req *mcp.CallToolRequest, input GetFileInfoInput) (*mcp.CallToolResult, fileInfoDetail, error) {
		if input.JobID == "" {
			return nil, fileInfoDetail{}, errors.New("invalid input: job_id is required")
		}
		if input.Path == "" {
			return nil, fileInfoDetail{}, errors.New("invalid input: path is required")
		}
		if err := authorize(ctx, auth.ScopeRead); err != nil {
			return nil, fileInfoDetail{}, err
		}
		entry, err := deps.LocalFiles.Stat(ctx, input.JobID, input.Path)
		if err != nil {
			return nil, fileInfoDetail{}, mapLookupError(err, input.Path)
		}
		return nil, toFileInfoDetail(input.JobID, entry), nil
	})
}

// mapSearchError 把搜索错误映射为稳定 tool error 文案。
func mapSearchError(err error) error {
	switch {
	case errors.Is(err, browser.ErrInvalid):
		return fmt.Errorf("invalid input: %v", err)
	case errors.Is(err, browser.ErrNotFound):
		return errors.New("job not found")
	default:
		return errors.New("internal error")
	}
}

// mapLookupError 把 Stat 错误映射为稳定 tool error 文案：目标
// 不存在（含 job 不存在）与路径非法可区分，其余不泄露内部细节。
func mapLookupError(err error, path string) error {
	switch {
	case errors.Is(err, browser.ErrInvalid), errors.Is(err, filesafe.ErrEscape), errors.Is(err, filesafe.ErrNotRegularFile):
		return fmt.Errorf("invalid input: %v", err)
	case errors.Is(err, browser.ErrNotFound):
		return errors.New("job not found")
	case errors.Is(err, fs.ErrNotExist), errors.Is(err, browser.ErrRemote):
		return fmt.Errorf("file not found: %s", path)
	default:
		return errors.New("internal error")
	}
}

package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"path"
	"strconv"

	"github.com/gin-gonic/gin"

	"tinysync/internal/browser"
	"tinysync/internal/source"
)

// registerRemoteFileRoutes 注册远端文件浏览端点。Browser 为 nil 时
// 跳过注册（依赖缺失时由未知路径 404 兜底）。
// 浏览复用 source.Service.OpenRemote 的统一入口，不在 API 层接触
// 凭据或协议分支。
func registerRemoteFileRoutes(group *gin.RouterGroup, svc *browser.RemoteService) {
	if svc == nil {
		return
	}
	h := &fileHandlers{remote: svc}
	group.GET("/sources/:id/files", h.remoteList)
	group.GET("/sources/:id/files/stat", h.remoteStat)
	group.GET("/sources/:id/files/download", h.remoteDownload)
}

// fileHandlers 是文件浏览端点的 handler 集合。
type fileHandlers struct {
	remote *browser.RemoteService
}

// remoteListResponse 是远端目录列表响应。
type remoteListResponse struct {
	Path       string          `json:"path"`
	Entries    []browser.Entry `json:"entries"`
	NextCursor string          `json:"next_cursor"`
}

// remoteList 列出 Source 的一层目录：
// GET /api/v1/sources/:id/files?path=/&limit=100&cursor=...
func (h *fileHandlers) remoteList(c *gin.Context) {
	p := queryPath(c)
	opts, err := listOptionsFromQuery(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	entries, next, err := h.remote.List(c.Request.Context(), c.Param("id"), p, opts)
	if err != nil {
		handleFileError(c, err)
		return
	}
	c.JSON(http.StatusOK, remoteListResponse{
		Path:       p,
		Entries:    entries,
		NextCursor: next,
	})
}

// remoteStat 读取远端路径元信息：
// GET /api/v1/sources/:id/files/stat?path=/foo.txt
func (h *fileHandlers) remoteStat(c *gin.Context) {
	entry, err := h.remote.Stat(c.Request.Context(), c.Param("id"), queryPath(c))
	if err != nil {
		handleFileError(c, err)
		return
	}
	c.JSON(http.StatusOK, entry)
}

// remoteDownload 流式下载远端文件：
// GET /api/v1/sources/:id/files/download?path=/foo.txt
// Remote.Open 是顺序只读流：不支持 Range，响应恒为 200 全量；
// Content-Length / Last-Modified 在 Stat 可用时填充。
func (h *fileHandlers) remoteDownload(c *gin.Context) {
	p := queryPath(c)
	meta, body, release, err := h.remote.Open(c.Request.Context(), c.Param("id"), p)
	if err != nil {
		handleFileError(c, err)
		return
	}
	defer func() { _ = release() }()

	setRemoteDownloadHeaders(c.Writer, path.Base(p), meta)
	c.Status(http.StatusOK)
	// 头部已提交，传输中断（客户端取消 / 远端断开）只能表现为响应
	// 截断；不向已提交的响应写错误体。
	_, _ = io.Copy(c.Writer, body)
}

// queryPath 读取 path 查询参数，缺省为根目录。
func queryPath(c *gin.Context) string {
	if p := c.Query("path"); p != "" {
		return p
	}
	return "/"
}

// listOptionsFromQuery 解析 limit / cursor：limit 存在时必须在
// 1..MaxListLimit（越界 400，不做静默截断）；cursor 原样透传。
func listOptionsFromQuery(c *gin.Context) (source.ListOptions, error) {
	opts := source.ListOptions{Limit: source.DefaultListLimit}
	if raw := c.Query("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > source.MaxListLimit {
			return source.ListOptions{}, fmt.Errorf("%w: limit must be an integer in 1..%d", source.ErrInvalid, source.MaxListLimit)
		}
		opts.Limit = n
	}
	opts.Cursor = c.Query("cursor")
	return opts, nil
}

// handleFileError 把 browser 领域错误映射为 HTTP 响应：invalid 400、
// not found 404、远端失败 502；请求取消是客户端断开，直接终止响应。
func handleFileError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		c.Abort()
	case errors.Is(err, browser.ErrInvalid):
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
	case errors.Is(err, browser.ErrNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
	case errors.Is(err, browser.ErrRemote):
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
	default:
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
	}
}

// setRemoteDownloadHeaders 设置下载响应头：按扩展名推断 MIME（未知
// 为 application/octet-stream）、attachment 处置（非 ASCII 文件名按
// RFC 5987 编码）、Stat 可得时带 Content-Length 与 Last-Modified。
func setRemoteDownloadHeaders(w http.ResponseWriter, name string, meta browser.DownloadMeta) {
	contentType := mime.TypeByExtension(path.Ext(name))
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	w.Header().Set("Content-Type", contentType)
	if disposition := mime.FormatMediaType("attachment", map[string]string{"filename": name}); disposition != "" {
		w.Header().Set("Content-Disposition", disposition)
	}
	if meta.Size > 0 {
		w.Header().Set("Content-Length", strconv.FormatInt(meta.Size, 10))
	}
	if meta.ModifiedAt != nil {
		w.Header().Set("Last-Modified", meta.ModifiedAt.UTC().Format(http.TimeFormat))
	}
}

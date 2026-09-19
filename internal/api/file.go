package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"tinysync/internal/auth"
	"tinysync/internal/browser"
	"tinysync/internal/filesafe"
	"tinysync/internal/source"
)

// registerRemoteFileRoutes 注册远端文件浏览端点。svc 为 nil 时跳过
// 注册（依赖缺失时由未知路径 404 兜底）。浏览复用
// source.Service.OpenRemote 的统一入口，不在 API 层接触凭据或协议
// 分支。
func registerRemoteFileRoutes(group *gin.RouterGroup, svc *browser.RemoteService) {
	if svc == nil {
		return
	}
	h := &fileHandlers{remote: svc}
	// 文件浏览与下载为 read scope。
	group.GET("/sources/:id/files", requireScope(auth.ScopeRead), h.remoteList)
	group.GET("/sources/:id/files/stat", requireScope(auth.ScopeRead), h.remoteStat)
	group.GET("/sources/:id/files/download", requireScope(auth.ScopeRead), h.remoteDownload)
	group.POST("/sources/:id/directories", requireScope(auth.ScopeAdmin), h.remoteMkdir)
}

type createRemoteDirectoryRequest struct {
	Path string `json:"path"`
	Name string `json:"name"`
}

// registerLocalFileRoutes 注册本地文件浏览端点（以 Job 为 namespace）。
// local 为 nil 时跳过注册。
func registerLocalFileRoutes(group *gin.RouterGroup, local *browser.LocalService) {
	if local == nil {
		return
	}
	h := &fileHandlers{local: local}
	group.GET("/jobs/:id/files", requireScope(auth.ScopeRead), h.localList)
	group.GET("/jobs/:id/files/stat", requireScope(auth.ScopeRead), h.localStat)
	group.GET("/jobs/:id/files/download", requireScope(auth.ScopeRead), h.localDownload)
	// 本地文件经 *os.File + ServeContent 服务，HEAD 只回响应头。
	// HEAD 与 GET 同为 read scope：响应头暴露存在性 / 大小 / 类型等
	// 文件元信息，run-only token 不得借 HEAD 探测。
	group.HEAD("/jobs/:id/files/download", requireScope(auth.ScopeRead), h.localDownload)
}

// fileHandlers 是文件浏览端点的 handler 集合。
type fileHandlers struct {
	remote *browser.RemoteService
	local  *browser.LocalService
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
	showHidden, err := strconv.ParseBool(c.DefaultQuery("hidden", "false"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "hidden must be a boolean"})
		return
	}
	entries, next, err := h.remote.List(c.Request.Context(), c.Param("id"), p, opts)
	if err != nil {
		handleFileError(c, err)
		return
	}
	if !showHidden {
		visible := entries[:0]
		for _, entry := range entries {
			if !strings.HasPrefix(entry.Name, ".") {
				visible = append(visible, entry)
			}
		}
		entries = visible
	}
	c.JSON(http.StatusOK, remoteListResponse{
		Path:       p,
		Entries:    entries,
		NextCursor: next,
	})
}

// remoteMkdir 在已浏览的远端目录内创建一个直接子目录。路径与名称分别
// 校验，禁止利用 name 的分隔符或 dot segment 跳出当前目录。
func (h *fileHandlers) remoteMkdir(c *gin.Context) {
	var req createRemoteDirectoryRequest
	if !strictBind(c, &req) {
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" || name == "." || name == ".." || path.Base(name) != name || strings.ContainsAny(name, `/\\`) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "folder name must be a single non-empty path segment"})
		return
	}
	if err := source.ValidateLogicalPath(req.Path); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	created := path.Join(req.Path, name)
	if !strings.HasPrefix(created, "/") {
		created = "/" + created
	}
	if err := h.remote.Mkdir(c.Request.Context(), c.Param("id"), created); err != nil {
		handleFileError(c, err)
		return
	}
	c.JSON(http.StatusCreated, gin.H{"path": created})
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

// localList 列出 Job.LocalRoot 下的一层目录：
// GET /api/v1/jobs/:id/files?path=/&limit=100&cursor=...
func (h *fileHandlers) localList(c *gin.Context) {
	p := queryPath(c)
	opts, err := listOptionsFromQuery(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	showHidden, err := strconv.ParseBool(c.DefaultQuery("hidden", "false"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "hidden must be a boolean"})
		return
	}
	entries, next, err := h.local.List(c.Request.Context(), c.Param("id"), p, opts)
	if err != nil {
		handleFileError(c, err)
		return
	}
	if !showHidden {
		visible := entries[:0]
		for _, entry := range entries {
			if !strings.HasPrefix(entry.Name, ".") {
				visible = append(visible, entry)
			}
		}
		entries = visible
	}
	c.JSON(http.StatusOK, localListResponse{
		Path:       p,
		Entries:    entries,
		NextCursor: next,
	})
}

// localListResponse 是本地目录列表响应（条目携带 managed 标记）。
type localListResponse struct {
	Path       string          `json:"path"`
	Entries    []browser.Entry `json:"entries"`
	NextCursor string          `json:"next_cursor"`
}

// localStat 读取本地路径元信息：
// GET /api/v1/jobs/:id/files/stat?path=/foo.txt
func (h *fileHandlers) localStat(c *gin.Context) {
	entry, err := h.local.Stat(c.Request.Context(), c.Param("id"), queryPath(c))
	if err != nil {
		handleFileError(c, err)
		return
	}
	c.JSON(http.StatusOK, entry)
}

// localDownload 下载本地文件：
// GET / HEAD /api/v1/jobs/:id/files/download?path=/foo.txt
// 经 http.ServeContent 服务：Range → 206 / 非法 Range → 416，
// HEAD 只回响应头；symlink 与目录在 Open 阶段拒绝。
func (h *fileHandlers) localDownload(c *gin.Context) {
	p := queryPath(c)
	name, f, info, err := h.local.Open(c.Request.Context(), c.Param("id"), p)
	if err != nil {
		handleFileError(c, err)
		return
	}
	defer func() { _ = f.Close() }()

	if disposition := mime.FormatMediaType("attachment", map[string]string{"filename": name}); disposition != "" {
		c.Header("Content-Disposition", disposition)
	}
	// 未命中 Range 时显式回 200；ServeContent 对合法 Range 回 206。
	if !rangeRequested(c.Request) {
		c.Status(http.StatusOK)
	}
	filesafe.ServeFileContent(c.Writer, c.Request, f, info, name)
}

// rangeRequested 判断客户端是否携带 Range 头（ServeContent 负责校验
// 与 206/416 判定，这里只用于避免预置 200 与 206 冲突）。
func rangeRequested(r *http.Request) bool {
	return r.Header.Get("Range") != ""
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

// handleFileError 把 browser 领域错误映射为 HTTP 响应：invalid（含
// symlink / 目录不可下载 / root 逃逸）400、not found 404、远端失败
// 502；请求取消是客户端断开，直接终止响应。
func handleFileError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		c.Abort()
	case errors.Is(err, fs.ErrNotExist):
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
	case errors.Is(err, browser.ErrInvalid), errors.Is(err, filesafe.ErrEscape), errors.Is(err, filesafe.ErrNotRegularFile):
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
// RFC 5987 编码）、Stat 可得时带 Content-Length（含 0 字节文件）与
// Last-Modified。
func setRemoteDownloadHeaders(w http.ResponseWriter, name string, meta browser.DownloadMeta) {
	contentType := mime.TypeByExtension(path.Ext(name))
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	w.Header().Set("Content-Type", contentType)
	if disposition := mime.FormatMediaType("attachment", map[string]string{"filename": name}); disposition != "" {
		w.Header().Set("Content-Disposition", disposition)
	}
	if meta.SizeKnown {
		w.Header().Set("Content-Length", strconv.FormatInt(meta.Size, 10))
	}
	if meta.ModifiedAt != nil {
		w.Header().Set("Last-Modified", meta.ModifiedAt.UTC().Format(http.TimeFormat))
	}
}

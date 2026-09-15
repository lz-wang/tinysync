package api

import (
	"bytes"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"tinysync/internal/buildinfo"
	"tinysync/internal/source"
	"tinysync/internal/syncjob"
)

// NewRouter 构建全部路由：
//   - /api/v1/* 只注册真实端点，未知 API 路径 404，不被 SPA fallback 吞掉；
//   - 其余路径由 NoRoute 承接：命中嵌入文件按静态资源服务
//     （assets 带 immutable 缓存），未命中回退 index.html（前端路由深链接）；
//     带扩展名的资源路径缺失时 404，不误回 index.html。
//
// 引擎用 gin.New() 而非 gin.Default()：项目已有 zap 日志，不再安装 Gin Logger
// 造成第二份 access log 事实来源；只保留 Recovery 兜底 panic。
func NewRouter(webFS fs.FS, deps Dependencies) *gin.Engine {
	gin.SetMode(gin.ReleaseMode)
	router := gin.New()
	router.Use(gin.Recovery())
	router.HandleMethodNotAllowed = true

	api := router.Group("/api/v1")
	{
		api.GET("/health", handleHealth)
		api.GET("/version", handleVersion)
		registerSourceRoutes(api, deps.Sources, deps.Jobs)
		registerJobRoutes(api, deps.Jobs, deps.Runner)
	}
	router.NoRoute(handleWeb(webFS))
	return router
}

// Dependencies 是 API 层依赖的应用服务集合。
type Dependencies struct {
	// Sources 是 Source 应用服务（REST / Web UI / MCP 共用）。
	Sources *source.Service
	// Jobs 是 Sync Job 应用服务；为 nil 时不注册 Job 端点，
	// 也不启用 Source 的 Job 引用删除保护。
	Jobs *syncjob.Service
	// Runner 是手动运行的运行时状态；为 nil 时 run / status 端点不注册。
	Runner *syncjob.Runner
}

// handleHealth 报告服务健康状态。
func handleHealth(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// handleVersion 返回构建期注入的版本号（buildinfo.Version）。
func handleVersion(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"version": buildinfo.Version})
}

// handleWeb 返回 SPA 静态资源与 fallback 处理器。
// 非 GET 注册方法的 API 请求由 Gin HandleMethodNotAllowed 返回 405（携带
// Allow 头）；其余未匹配请求全部进入本处理器。
func handleWeb(webFS fs.FS) gin.HandlerFunc {
	return func(c *gin.Context) {
		// API 前缀必须 404：不能让 SPA fallback 吞掉未知 API 路径。
		if strings.HasPrefix(c.Request.URL.Path, "/api/") {
			c.AbortWithStatusJSON(http.StatusNotFound, gin.H{"error": "not found"})
			return
		}
		filePath := path.Clean(strings.TrimPrefix(c.Request.URL.Path, "/"))
		if filePath == "" || filePath == "." {
			filePath = "index.html"
		}
		data, err := fs.ReadFile(webFS, filePath)
		if err != nil {
			if isStaticAssetRequest(c.Request.URL.Path) {
				c.Status(http.StatusNotFound)
				return
			}
			// 前端路由深链接回退到 index.html。注意：进入 NoRoute 时 Gin 已把
			// 状态预置为 404，这里必须显式改回 200，否则响应以 404 提交。
			data, err = fs.ReadFile(webFS, "index.html")
			if err != nil {
				_ = c.Error(err)
				c.String(http.StatusInternalServerError, "embedded web assets missing")
				return
			}
			c.Header("Cache-Control", "no-store")
			c.Data(http.StatusOK, "text/html; charset=utf-8", data)
			return
		}
		applyStaticCacheHeaders(c.Writer, filePath)
		http.ServeContent(c.Writer, c.Request, filePath, time.Now(), bytes.NewReader(data))
	}
}

// isStaticAssetRequest 判断请求是否为静态资源（带扩展名或 /assets/ 前缀）：
// 这类路径缺失时应 404，而不是回退 index.html。
func isStaticAssetRequest(requestPath string) bool {
	cleanPath := path.Clean(strings.TrimSpace(requestPath))
	if strings.HasPrefix(cleanPath, "/assets/") {
		return true
	}
	base := path.Base(cleanPath)
	if base == "." || base == "/" || base == "" {
		return false
	}
	return path.Ext(base) != ""
}

// applyStaticCacheHeaders 按文件类型设置缓存策略：
// index.html 不缓存（保证发版即生效），assets 带内容 hash 可永久缓存。
func applyStaticCacheHeaders(w http.ResponseWriter, filePath string) {
	switch {
	case filePath == "index.html":
		w.Header().Set("Cache-Control", "no-store")
	case strings.HasPrefix(filePath, "assets/"):
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	}
}

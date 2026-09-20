package api

import (
	"bytes"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"tinysync/internal/auth"
	"tinysync/internal/browser"
	"tinysync/internal/buildinfo"
	"tinysync/internal/share"
	"tinysync/internal/source"
	"tinysync/internal/syncjob"
)

// NewRouter 构建全部路由：
//   - /api/v1/* 分两层：public（health / version / login）与
//     protected（default-deny，经 authMiddleware 认证）；
//   - 仅 /shared/:slug（浏览页）与 /shared/:slug/*path（直链）显式注册；
//     裸 /shared（含尾随斜杠）一律 404；
//     /mcp 显式注册，其余 /mcp/* 路径 404，不落入 SPA fallback；
//   - 其余路径由 NoRoute 承接：命中嵌入文件按静态资源服务
//     （assets 带 immutable 缓存），未命中回退 index.html（前端路由深链接）；
//     带扩展名的资源路径缺失时 404，不误回 index.html。
//
// 引擎用 gin.New() 而非 gin.Default()：项目已有 zap 日志，不再安装 Gin Logger
// 造成第二份 access log 事实来源。全局中间件链：RequestID（服务器
// 生成的请求标识）→ AccessLog（唯一 access log，defer 保证 panic
// 也留痕）→ Recovery（兜底 panic 为 500，位于链内侧使 access log
// 能记录恢复后的 500 状态）。
func NewRouter(webFS fs.FS, deps Dependencies) *gin.Engine {
	gin.SetMode(gin.ReleaseMode)
	router := gin.New()
	router.Use(requestIDMiddleware())
	router.Use(accessLogMiddleware())
	router.Use(gin.Recovery())
	router.HandleMethodNotAllowed = true

	api := router.Group("/api/v1")
	{
		api.GET("/health", handleHealth)
		api.GET("/version", handleVersion)
		registerAuthRoutes(api, deps.Auth)
		registerPublicShareRoutes(api, deps.Share)
	}
	// 受保护 API：default-deny。Auth 为 nil 时中间件 fail closed（500）。
	protected := router.Group("/api/v1", authMiddleware(deps.Auth))
	{
		registerSessionRoutes(protected, deps.Auth)
		registerSourceRoutes(protected, deps.Sources, deps.Jobs)
		registerJobRoutes(protected, deps.Jobs, deps.Runner)
		registerRemoteFileRoutes(protected, deps.Browser)
		registerLocalFileRoutes(protected, deps.LocalFiles)
		registerShareRoutes(protected, deps.Share)
		registerAPITokenRoutes(protected, deps.Auth)
	}
	// 仅 /shared/:slug（浏览页）与 /shared/:slug/*path（直链）显式注册：
	// 公开服务不落入 SPA fallback，裸 /shared 则由 NoRoute 拒绝。
	registerSharedServingRoutes(router, webFS, deps.Share)
	// /mcp 显式注册：MCP adapter 自带认证与跨源防护链（auth 只认
	// Bearer API Token），不走 Gin 中间件。nil 时路径不存在（fail
	// closed：不会出现无认证的 MCP 端点）。Any 而非 POST：GET / DELETE
	// 等方法由 MCP handler 自己回应（stateless 下 GET/DELETE 为 405）。
	if deps.MCP != nil {
		router.Any("/mcp", gin.WrapH(deps.MCP))
	}
	router.NoRoute(handleWeb(webFS))
	return router
}

// Dependencies 是 API 层依赖的应用服务集合。
type Dependencies struct {
	// Auth 是认证应用服务；为 nil 时受保护端点 fail closed（500），
	// 绝不退化为匿名访问。
	Auth *auth.Service
	// Sources 是 Source 应用服务（REST / Web UI / MCP 共用）。
	Sources *source.Service
	// Jobs 是 Sync Job 应用服务；为 nil 时不注册 Job 端点，
	// 也不启用 Source 的 Job 引用删除保护。
	Jobs *syncjob.Service
	// Runner 是手动运行的运行时状态；为 nil 时 run / status 端点不注册。
	Runner *syncjob.Runner
	// Browser 是文件浏览应用服务；为 nil 时不注册文件端点。
	Browser *browser.RemoteService
	// LocalFiles 是本地文件浏览应用服务；为 nil 时不注册本地文件端点。
	LocalFiles *browser.LocalService
	// Share 是共享策略应用服务；为 nil 时不注册共享端点与公开
	// /shared 路由。
	Share *share.Service
	// MCP 是已装配完成的 MCP endpoint handler（自带 Bearer 认证与
	// 跨源防护）。api 层只认识 http.Handler，不接触 MCP SDK。为 nil
	// 时不注册 /mcp（fail closed）。
	MCP http.Handler
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
		// API、MCP 与裸 /shared namespace 必须 404：不能让 SPA fallback
		// 吞掉未知 adapter 路径或公开共享索引。精确 /mcp 与带 slug 的
		// /shared 路径均由显式路由处理。
		if strings.HasPrefix(c.Request.URL.Path, "/api/") || c.Request.URL.Path == "/mcp" || strings.HasPrefix(c.Request.URL.Path, "/mcp/") || c.Request.URL.Path == "/shared" || c.Request.URL.Path == "/shared/" {
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

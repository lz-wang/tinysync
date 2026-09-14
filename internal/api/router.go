package api

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"time"

	"tinysync/internal/buildinfo"
)

// NewRouter 构建全部路由：
//   - /api/v1/* 只注册真实端点，未知 API 路径 404，不被 SPA fallback 吞掉；
//   - 其余路径由 SPA fallback 承接：命中嵌入文件按静态资源服务
//     （assets 带 immutable 缓存），未命中回退 index.html（前端路由深链接）；
//     带扩展名的资源路径缺失时 404，不误回 index.html。
func NewRouter(webFS fs.FS) http.Handler {
	mux := http.NewServeMux()
	// API 路由不用 "GET " 前缀注册：避免错误方法被 "/" 兜底吞成 404，
	// 而是经 methodGET 返回 405（精确路径优先级高于 "/"）。
	mux.HandleFunc("/api/v1/health", methodGET(handleHealth))
	mux.HandleFunc("/api/v1/version", methodGET(handleVersion))
	mux.HandleFunc("/", handleWeb(webFS))
	return mux
}

// methodGET 包装 handler：仅放行 GET（含 HEAD 语义由调用方决定，骨架仅 GET），
// 其余方法返回 405 并携带 Allow 头。
func methodGET(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", "GET")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		next(w, r)
	}
}

// handleHealth 报告服务健康状态。
func handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleVersion 返回构建期注入的版本号（buildinfo.Version）。
func handleVersion(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"version": buildinfo.Version})
}

// handleWeb 返回 SPA 静态资源与 fallback 处理器。
func handleWeb(webFS fs.FS) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// API 前缀必须 404：不能让 SPA fallback 吞掉未知 API 路径。
		if strings.HasPrefix(r.URL.Path, "/api/") {
			http.NotFound(w, r)
			return
		}
		filePath := path.Clean(strings.TrimPrefix(r.URL.Path, "/"))
		if filePath == "" || filePath == "." {
			filePath = "index.html"
		}
		data, err := fs.ReadFile(webFS, filePath)
		if err != nil {
			if isStaticAssetRequest(r.URL.Path) {
				http.NotFound(w, r)
				return
			}
			// 前端路由深链接回退到 index.html。
			data, err = fs.ReadFile(webFS, "index.html")
			if err != nil {
				http.Error(w, "embedded web assets missing", http.StatusInternalServerError)
				return
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Cache-Control", "no-store")
			_, _ = w.Write(data)
			return
		}
		applyStaticCacheHeaders(w, filePath)
		http.ServeContent(w, r, filePath, time.Now(), bytes.NewReader(data))
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

// writeJSON 写出 JSON 响应并设置 Content-Type。
func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	// 头部已写出，编码失败对客户端不可恢复。
	_ = json.NewEncoder(w).Encode(payload)
}

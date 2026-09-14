package api

import (
	"encoding/json"
	"net/http"

	"tinysync/internal/buildinfo"
)

// NewRouter 构建全部路由。未知 /api/v1/* 路径返回 404，
// 不会被 SPA fallback 吞掉（fallback 在 WebUI 嵌入后接入）。
func NewRouter() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/health", handleHealth)
	mux.HandleFunc("GET /api/v1/version", handleVersion)
	return mux
}

// handleHealth 报告服务健康状态。
func handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleVersion 返回构建期注入的版本号（buildinfo.Version）。
func handleVersion(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"version": buildinfo.Version})
}

// writeJSON 写出 JSON 响应并设置 Content-Type。
func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	// 头部已写出，编码失败对客户端不可恢复。
	_ = json.NewEncoder(w).Encode(payload)
}

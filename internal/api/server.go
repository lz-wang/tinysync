// Package api 承载 HTTP 服务：路由定义、handler 与进程生命周期。
// 骨架阶段只暴露 health 与 version 两个只读端点，用于打通
// Git -> Make -> ldflags -> buildinfo -> HTTP API 的版本链路。
package api

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"time"

	"tinysync/internal/config"
)

// shutdownTimeout 是优雅关闭时等待存量请求完成的最长时间。
const shutdownTimeout = 10 * time.Second

// Server 是 HTTP 服务实例：持有 *http.Server 并管理其生命周期。
type Server struct {
	httpServer *http.Server
}

// NewServer 用给定配置构造 HTTP 服务（不监听，调用 Start 开始服务）。
// webFS 为嵌入的前端静态资源（web/dist 或 fallback），由 SPA 路由服务。
func NewServer(cfg *config.Config, webFS fs.FS) *Server {
	return &Server{
		httpServer: &http.Server{
			Addr:    cfg.ListenAddr(),
			Handler: NewRouter(webFS),
			// 硬化常驻服务：防止慢速头部与闲置连接无限占用。
			ReadHeaderTimeout: 5 * time.Second,
			IdleTimeout:       60 * time.Second,
		},
	}
}

// Start 开始监听并服务，阻塞直至 Shutdown 被调用（返回 ErrServerClosed）
// 或发生监听错误。
func (s *Server) Start() error {
	err := s.httpServer.ListenAndServe()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("http serve: %w", err)
	}
	return nil
}

// Shutdown 优雅关闭：停止接收新连接，等待存量请求完成（最多
// shutdownTimeout）。多次调用安全。
func (s *Server) Shutdown(ctx context.Context) error {
	shutdownCtx, cancel := context.WithTimeout(ctx, shutdownTimeout)
	defer cancel()
	if err := s.httpServer.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("http shutdown: %w", err)
	}
	return nil
}

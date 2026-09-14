// Package app 是应用生命周期与装配根（composition root）：
// 从 config/logging/api 等低层包构造服务并驱动其生命周期。
package app

import (
	"context"
	"errors"
	"net/http"

	"tinysync/internal/api"
	"tinysync/internal/config"
	"tinysync/internal/logging"
)

// Run 启动 HTTP 服务并阻塞直至 ctx 取消（SIGINT/SIGTERM）。
// ctx 取消后执行优雅关闭：停止接收新请求，等待存量请求完成。
// 返回 nil 表示正常退出（含优雅关闭），非 nil 表示启动或运行失败。
func Run(ctx context.Context, cfg *config.Config) error {
	server := api.NewServer(cfg)

	serveErr := make(chan error, 1)
	go func() {
		if err := server.Start(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr <- err
		}
	}()
	logging.Infof("tinysync ready, listening on %s", cfg.ListenAddr())

	select {
	case <-ctx.Done():
	case err := <-serveErr:
		return err
	}

	logging.Infof("shutting down")
	if err := server.Shutdown(context.Background()); err != nil {
		return err
	}
	logging.Infof("bye")
	return nil
}

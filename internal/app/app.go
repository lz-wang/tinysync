// Package app 是应用生命周期与装配根（composition root）：
// 从 config/logging/storage/api 等低层包构造服务并驱动其生命周期。
package app

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/http"

	"tinysync/internal/api"
	"tinysync/internal/config"
	"tinysync/internal/logging"
	"tinysync/internal/storage"
)

// Run 建立持久化并启动 HTTP 服务，阻塞直至 ctx 取消（SIGINT/SIGTERM）。
// webFS 为嵌入的前端静态资源。数据库不可用时不启动 HTTP 服务；
// ctx 取消后执行优雅关闭：停止接收新请求，等待存量请求完成，最后关闭 DB。
// 返回 nil 表示正常退出（含优雅关闭），非 nil 表示启动或运行失败。
func Run(ctx context.Context, cfg *config.Config, webFS fs.FS) error {
	db, err := storage.Open(cfg.DataDir)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer func() {
		if cerr := db.Close(); cerr != nil {
			logging.Errorf("close database: %v", cerr)
		}
	}()

	server := api.NewServer(cfg, webFS)

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

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
	"tinysync/internal/source"
	"tinysync/internal/source/sqlite"
	"tinysync/internal/source/webdav"
	"tinysync/internal/storage"
	"tinysync/internal/syncjob"
	jobsqlite "tinysync/internal/syncjob/sqlite"
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
	// migration 是启动关键路径：使用独立 context，不因启动即收到的
	// 取消信号中断，保证退出行为与数据库状态确定。
	if err := storage.Migrate(context.Background(), db, cfg.DataDir); err != nil {
		return fmt.Errorf("migrate database: %w", err)
	}

	// 装配 Source 领域：SQLite 仓库 + WebDAV factory + 应用服务。
	// REST / Web UI / MCP 共用该服务层。
	sources := source.NewService(sqlite.New(db), webdav.NewFactory())

	// 装配 Sync Job 领域：仓库共享同一 DB（FK RESTRICT / CASCADE 生效），
	// 应用服务带 LocalRoot 归属保护，Runner 提供手动运行与内存状态。
	jobRepo := jobsqlite.NewRepository(db)
	managedRepo := jobsqlite.NewManagedRepository(db)
	jobs := syncjob.NewService(jobRepo, sources, cfg.DataDir)
	runner := syncjob.NewRunner(jobRepo, managedRepo, sources, webdav.NewFactory())

	server := api.NewServer(cfg, webFS, api.Dependencies{
		Sources: sources,
		Jobs:    jobs,
		Runner:  runner,
	})

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
	// 存量请求结束后取消仍在进行的同步运行并等待退出，
	// 保证退出时没有遗留的传输 goroutine。
	if err := runner.Shutdown(context.Background()); err != nil {
		return fmt.Errorf("shutdown sync runner: %w", err)
	}
	logging.Infof("bye")
	return nil
}

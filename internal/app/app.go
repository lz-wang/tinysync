// Package app 是应用生命周期与装配根（composition root）：
// 从 config/logging/storage/api 等低层包构造服务并驱动其生命周期。
package app

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"time"

	"tinysync/internal/api"
	"tinysync/internal/auth"
	authsqlite "tinysync/internal/auth/sqlite"
	"tinysync/internal/browser"
	"tinysync/internal/config"
	"tinysync/internal/instance"
	"tinysync/internal/logging"
	"tinysync/internal/mcp"
	"tinysync/internal/publish"
	publishsqlite "tinysync/internal/publish/sqlite"
	"tinysync/internal/source"
	s3adapter "tinysync/internal/source/s3"
	sftpadapter "tinysync/internal/source/sftp"
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
//
// datadir 单实例约束：整个进程生命周期持有 <datadir>/tinysync.lock
// 独占锁（one datadir = one process），第二个实例启动即失败；锁在
// DB 关闭后释放。
func Run(ctx context.Context, cfg *config.Config, webFS fs.FS) error {
	lock, err := instance.Acquire(cfg.DataDir)
	if err != nil {
		return err
	}
	defer func() {
		if lerr := lock.Release(); lerr != nil {
			logging.Errorf("release datadir lock: %v", lerr)
		}
	}()

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

	// 认证边界：serve 强制要求已初始化管理员密码，不提供绕过开关；
	// 未初始化时拒绝启动，由 operator 经 CLI 完成 bootstrap。
	authService := auth.NewService(authsqlite.NewRepository(db))
	configured, err := authService.AdminConfigured(context.Background())
	if err != nil {
		return fmt.Errorf("check admin credential: %w", err)
	}
	if !configured {
		return fmt.Errorf("authentication is not initialized; run `tinysync auth set-password --datadir %s`", cfg.DataDir)
	}

	// 启动恢复：进程异常退出遗留的 running 记录收敛为 failed，
	// 保证持久化历史与「没有进程在运行」的现实一致。
	runs := jobsqlite.NewRunRepository(db)
	recovered, err := runs.FailStaleRunning(context.Background(), time.Now().UTC(), "previous process interrupted")
	if err != nil {
		return fmt.Errorf("recover stale sync runs: %w", err)
	}
	if recovered > 0 {
		logging.Infof("recovered %d stale running sync run(s) as failed", recovered)
	}

	// 装配 Source 领域：SQLite 仓库 + 协议注册表 + 应用服务。协议
	// dispatch 只发生在 registry 一处，业务层不出现协议分支；
	// REST / Web UI / MCP 共用该服务层。
	remotes, err := source.NewRemoteRegistry(webdav.NewFactory(), s3adapter.NewFactory(), sftpadapter.NewFactory())
	if err != nil {
		return fmt.Errorf("assemble remote registry: %w", err)
	}
	sources := source.NewService(sqlite.New(db), remotes)

	// 装配 Sync Job 领域：仓库共享同一 DB（FK RESTRICT / CASCADE 生效），
	// 应用服务带 LocalRoot 归属保护，Runner 提供手动运行并以持久化
	// 历史为运行状态事实来源；并发上限来自运行配置。
	jobRepo := jobsqlite.NewRepository(db)
	managedRepo := jobsqlite.NewManagedRepository(db)
	jobs := syncjob.NewService(jobRepo, sources, cfg.DataDir)
	runner := syncjob.NewRunner(jobRepo, managedRepo, sources, runs)
	runner.MaxConcurrentJobs = cfg.MaxConcurrentJobs
	runner.MaxConcurrentTransfers = cfg.MaxConcurrentTransfers
	scheduler := syncjob.NewScheduler(jobRepo, runner, runs)

	// 装配文件浏览：Remote 浏览复用 Source 服务的统一远端入口，
	// 本地浏览以 Job.LocalRoot 为唯一 namespace，managed 标记来自
	// managed_files 记录。
	files := browser.NewRemoteService(sources)
	localFiles := browser.NewLocalService(jobs, managedRepo)

	// 装配发布策略：canonical local path 校验依赖 Job 与 managed
	// 记录，serving 与 CRUD 共用同一服务层。
	policies := publish.NewService(publishsqlite.NewRepository(db), jobs, managedRepo)

	// 装配 MCP adapter：复用同一批应用服务，自带 Bearer 认证与跨源
	// 防护；REST / Web UI / MCP 至此共用一个 composition root。
	mcpHandler := mcp.New(mcp.Deps{
		Auth:       authService,
		Sources:    sources,
		Jobs:       jobs,
		Runner:     runner,
		LocalFiles: localFiles,
	})

	server := api.NewServer(cfg, webFS, api.Dependencies{
		Auth:       authService,
		Sources:    sources,
		Jobs:       jobs,
		Runner:     runner,
		Browser:    files,
		LocalFiles: localFiles,
		Publish:    policies,
		MCP:        mcpHandler,
	})

	serveErr := make(chan error, 1)
	go func() {
		if err := server.Start(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr <- err
		}
	}()
	logging.Infof("tinysync ready, listening on %s", cfg.ListenAddr())
	// 调度器在 HTTP 服务就绪后启动：生产触发，Runner 执行，历史持久化。
	scheduler.Start(ctx)

	select {
	case <-ctx.Done():
	case err := <-serveErr:
		return err
	}

	logging.Infof("shutting down")
	// 优雅关闭顺序（v0.9 冻结契约）：停触发来源 → 取消在途运行并等待
	// 终态落库 → 排空存量 HTTP 请求 → WAL checkpoint 收口 → 关闭 DB →
	// 释放 datadir lock（后两步由 defer 完成）。异常退出不依赖
	// checkpoint 保正确性，仍由 SQLite WAL recovery 保证。
	scheduler.Stop()
	if err := runner.Shutdown(context.Background()); err != nil {
		return fmt.Errorf("shutdown sync runner: %w", err)
	}
	if err := server.Shutdown(context.Background()); err != nil {
		return err
	}
	// 运行终态已全部落库后截断 WAL：干净退出不留膨胀的 WAL 文件。
	// 失败记录并使关闭以非零结果结束，但不尝试删除 WAL 文件。
	if err := storage.CheckpointWal(context.Background(), db); err != nil {
		logging.Errorf("wal checkpoint on shutdown: %v", err)
		return fmt.Errorf("wal checkpoint: %w", err)
	}
	logging.Infof("bye")
	return nil
}

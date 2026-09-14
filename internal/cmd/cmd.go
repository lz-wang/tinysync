// Package cmd 承载命令行解析：根命令定义与 serve 子命令。
// main.go 仅做 signal context，命令分发交给 NewCommand。
package cmd

import (
	"context"
	"fmt"
	"io/fs"

	"github.com/urfave/cli/v3"

	"tinysync/internal/app"
	"tinysync/internal/buildinfo"
	"tinysync/internal/config"
	"tinysync/internal/logging"
)

// NewCommand 构建 CLI 根命令（urfave/cli/v3）。webFS 为嵌入的前端静态资源，
// 由 serve 子命令透传给应用。
//
// 最终只保留：
//
//	./tinysync serve --port 9466    # 启动服务
//	./tinysync --version            # 打印版本
//	./tinysync version              # 同 --version
func NewCommand(webFS fs.FS) *cli.Command {
	// 让 --version 输出纯版本号（默认带 "app version" 前缀）。
	cli.VersionPrinter = func(c *cli.Command) {
		fmt.Println(c.Root().Version)
	}

	return &cli.Command{
		Name:    "tinysync",
		Usage:   "HomeLab 文件同步服务",
		Version: buildinfo.Version,
		Commands: []*cli.Command{
			{
				Name:  "serve",
				Usage: "启动 TinySync 服务（Web UI + REST API）",
				Flags: serveFlags(),
				Action: func(ctx context.Context, c *cli.Command) error {
					return runServer(ctx, c.String("datadir"), c.Int("port"), webFS)
				},
			},
			{
				Name:  "version",
				Usage: "打印版本号（同 --version）",
				Action: func(ctx context.Context, c *cli.Command) error {
					fmt.Println(c.Root().Version)
					return nil
				},
			},
		},
	}
}

// serveFlags 是 serve 子命令的启动参数：datadir 与 port。
// 环境变量作为默认值来源，命令行参数优先。
func serveFlags() []cli.Flag {
	return []cli.Flag{
		&cli.StringFlag{
			Name:    "datadir",
			Usage:   "运行时数据根目录（日志在 <datadir>/logs/）",
			Value:   config.DefaultDataDir,
			Sources: cli.EnvVars("TINYSYNC_DATADIR"),
		},
		&cli.IntFlag{
			Name:    "port",
			Usage:   "HTTP 监听端口",
			Value:   config.DefaultPort,
			Sources: cli.EnvVars("TINYSYNC_PORT"),
		},
	}
}

// runServer 用 CLI/env 传入的 datadir/port 加载配置、初始化日志，
// 交给 app.Run 启动服务并阻塞至 ctx 取消。
func runServer(ctx context.Context, dataDir string, port int, webFS fs.FS) error {
	cfg, err := config.Load(config.Options{DataDir: dataDir, Port: port})
	if err != nil {
		return err
	}
	if err := cfg.EnsureDataDir(); err != nil {
		return err
	}
	logging.Init(cfg.DataDir)
	defer func() { _ = logging.Sync() }()
	logging.Infof("starting tinysync %s (datadir=%s, addr=%s)", buildinfo.Version, cfg.DataDir, cfg.ListenAddr())
	return app.Run(ctx, cfg, webFS)
}

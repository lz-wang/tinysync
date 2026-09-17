// Package cmd 承载命令行解析：根命令定义与 serve / auth 子命令。
// main.go 仅做 signal context，命令分发交给 NewCommand。
package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strings"

	"github.com/urfave/cli/v3"
	"golang.org/x/term"

	"tinysync/internal/app"
	"tinysync/internal/auth"
	authsqlite "tinysync/internal/auth/sqlite"
	"tinysync/internal/buildinfo"
	"tinysync/internal/config"
	"tinysync/internal/logging"
	"tinysync/internal/storage"
)

// NewCommand 构建 CLI 根命令（urfave/cli/v3）。webFS 为嵌入的前端静态资源，
// 由 serve 子命令透传给应用。
//
// 命令一览：
//
//	./tinysync serve --port 9466    # 启动服务
//	./tinysync auth set-password    # 初始化 / 重置管理员密码
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
					return runServer(ctx, config.Options{
						DataDir:                c.String("datadir"),
						Port:                   c.Int("port"),
						MaxConcurrentJobs:      c.Int("max-concurrent-jobs"),
						MaxConcurrentTransfers: c.Int("max-concurrent-transfers"),
					}, webFS)
				},
			},
			{
				Name:  "auth",
				Usage: "管理员凭据管理",
				Commands: []*cli.Command{
					{
						Name:  "set-password",
						Usage: "初始化或重置管理员密码（重置会立即废弃全部 Web Session）",
						Description: `首次部署的 bootstrap、遗忘密码后的 operator reset 与
密码 rotation 共用此命令。serve 在管理员密码未初始化时拒绝启动。

自动化场景用 --password-stdin 从管道读取：

  printf '%s\n' "$PASSWORD" | tinysync auth set-password --datadir /data --password-stdin

不提供 --password 明文参数，避免密码进入 shell history 与进程 argv。`,
						Flags: []cli.Flag{
							&cli.StringFlag{
								Name:    "datadir",
								Usage:   "运行时数据根目录",
								Value:   config.DefaultDataDir,
								Sources: cli.EnvVars("TINYSYNC_DATADIR"),
							},
							&cli.BoolFlag{
								Name:  "password-stdin",
								Usage: "从 stdin 读取密码（自动化场景）；缺省时交互式输入并要求确认",
							},
						},
						Action: func(ctx context.Context, c *cli.Command) error {
							return execSetPassword(ctx, setPasswordInput{
								DataDir:       c.String("datadir"),
								PasswordStdin: c.Bool("password-stdin"),
							}, os.Stdin, os.Stdout, os.Stderr)
						},
					},
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

// setPasswordInput 是 auth set-password 的解析后输入。
type setPasswordInput struct {
	DataDir       string
	PasswordStdin bool
}

// execSetPassword 执行 auth set-password：打开数据目录数据库、完成
// migration、按输入方式读取密码并创建 / 替换管理员密码。stdin /
// stdout / stderr 可注入以便测试。
func execSetPassword(ctx context.Context, in setPasswordInput, stdin io.Reader, stdout, stderr io.Writer) error {
	cfg, err := config.Load(config.Options{DataDir: in.DataDir})
	if err != nil {
		return err
	}
	password, err := readPassword(in.PasswordStdin, stdin, stderr)
	if err != nil {
		return err
	}

	db, err := storage.Open(cfg.DataDir)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer func() { _ = db.Close() }()
	if err := storage.Migrate(ctx, db, cfg.DataDir); err != nil {
		return fmt.Errorf("migrate database: %w", err)
	}

	svc := auth.NewService(authsqlite.NewRepository(db))
	configured, err := svc.AdminConfigured(ctx)
	if err != nil {
		return fmt.Errorf("check admin credential: %w", err)
	}
	if err := svc.SetAdminPassword(ctx, password); err != nil {
		return fmt.Errorf("set admin password: %w", err)
	}
	if configured {
		fmt.Fprintln(stdout, "管理员密码已更新，全部已有 Web Session 已废弃。")
	} else {
		fmt.Fprintln(stdout, "管理员密码已初始化，现在可以启动 tinysync serve。")
	}
	return nil
}

// readPassword 按输入方式读取管理员密码：--password-stdin 从 stdin
// 读取并去掉末尾单个换行；交互模式要求 TTY、不回显并二次确认。
func readPassword(fromStdin bool, stdin io.Reader, stderr io.Writer) (string, error) {
	if fromStdin {
		data, err := io.ReadAll(stdin)
		if err != nil {
			return "", fmt.Errorf("read password from stdin: %w", err)
		}
		password := strings.TrimSuffix(string(data), "\n")
		password = strings.TrimSuffix(password, "\r")
		if password == "" {
			return "", errors.New("stdin 未提供密码")
		}
		return password, nil
	}
	file, ok := stdin.(*os.File)
	if !ok || !term.IsTerminal(int(file.Fd())) {
		return "", errors.New("stdin 不是终端；自动化场景请使用 --password-stdin")
	}
	fmt.Fprint(stderr, "输入新的管理员密码: ")
	first, err := term.ReadPassword(int(file.Fd()))
	fmt.Fprintln(stderr)
	if err != nil {
		return "", fmt.Errorf("read password: %w", err)
	}
	fmt.Fprint(stderr, "再次输入以确认: ")
	confirm, err := term.ReadPassword(int(file.Fd()))
	fmt.Fprintln(stderr)
	if err != nil {
		return "", fmt.Errorf("read password confirmation: %w", err)
	}
	if string(first) != string(confirm) {
		return "", errors.New("两次输入的密码不一致")
	}
	return string(first), nil
}

// serveFlags 是 serve 子命令的启动参数：datadir、port 与并发上限。
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
		&cli.IntFlag{
			Name:    "max-concurrent-jobs",
			Usage:   "同时运行的同步 Job 数上限",
			Value:   config.DefaultMaxConcurrentJobs,
			Sources: cli.EnvVars("TINYSYNC_MAX_CONCURRENT_JOBS"),
		},
		&cli.IntFlag{
			Name:    "max-concurrent-transfers",
			Usage:   "同时进行的远端文件下载上限",
			Value:   config.DefaultMaxConcurrentTransfers,
			Sources: cli.EnvVars("TINYSYNC_MAX_CONCURRENT_TRANSFERS"),
		},
	}
}

// runServer 用 CLI/env 传入的配置加载 Config、初始化日志，
// 交给 app.Run 启动服务并阻塞至 ctx 取消。
func runServer(ctx context.Context, opts config.Options, webFS fs.FS) error {
	cfg, err := config.Load(opts)
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

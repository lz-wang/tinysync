// Package config 承载运行时配置：底层参数（数据目录、端口、并发上限）
// 由 CLI/env 提供，其余沿用内置默认值。
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

// DefaultPort 是 tinysync serve 的默认监听端口。
const DefaultPort = 9466

// DefaultDataDir 是默认运行时数据根目录。
const DefaultDataDir = "./data"

// 并发默认值：Job 数保持 v0.3 的全局单运行行为，远端下载并发给到
// 适度吞吐（SQLite 状态推进始终串行，不受此值影响）。
const (
	DefaultMaxConcurrentJobs      = 1
	DefaultMaxConcurrentTransfers = 4
)

// Config 是进程运行所需的全部配置。
type Config struct {
	// DataDir 是运行时数据根目录（日志在 <datadir>/logs/）。
	DataDir string
	// Port 是 HTTP 监听端口。
	Port int
	// MaxConcurrentJobs 是全进程同时运行的同步 Job 数上限。
	MaxConcurrentJobs int
	// MaxConcurrentTransfers 是全进程同时进行的远端文件下载上限。
	MaxConcurrentTransfers int
	// TransferTimeout 是单文件单次传输 attempt 的超时；0 表示不启用
	//（HomeLab 大文件可能合法传输很久，默认保持既有行为）。
	TransferTimeout time.Duration
}

// Default 返回内置默认配置。
func Default() *Config {
	return &Config{
		DataDir:                DefaultDataDir,
		Port:                   DefaultPort,
		MaxConcurrentJobs:      DefaultMaxConcurrentJobs,
		MaxConcurrentTransfers: DefaultMaxConcurrentTransfers,
	}
}

// Options 是启动配置来源：由 CLI flag 或环境变量解析而来。
type Options struct {
	DataDir string
	Port    int
	// 并发参数为 0 表示未提供（回退默认值）。
	MaxConcurrentJobs      int
	MaxConcurrentTransfers int
	// TransferTimeout 为 0 表示未提供 / 不启用；负值非法。
	TransferTimeout time.Duration
}

// Load 用传入参数装配 Config：空缺项回退默认值，DataDir 解析为绝对路径，
// Port 校验合法范围，并发参数必须为正整数。
func Load(opts Options) (*Config, error) {
	cfg := Default()
	if opts.DataDir != "" {
		cfg.DataDir = opts.DataDir
	}
	// Port 为 0 表示未提供（回退默认值）；其余值必须落在合法端口范围。
	if opts.Port != 0 {
		cfg.Port = opts.Port
	}
	if cfg.Port < 1 || cfg.Port > 65535 {
		return nil, fmt.Errorf("port %d out of range [1, 65535]", cfg.Port)
	}
	if opts.MaxConcurrentJobs != 0 {
		cfg.MaxConcurrentJobs = opts.MaxConcurrentJobs
	}
	if opts.MaxConcurrentTransfers != 0 {
		cfg.MaxConcurrentTransfers = opts.MaxConcurrentTransfers
	}
	if cfg.MaxConcurrentJobs < 1 {
		return nil, fmt.Errorf("max-concurrent-jobs %d must be a positive integer", cfg.MaxConcurrentJobs)
	}
	if cfg.MaxConcurrentTransfers < 1 {
		return nil, fmt.Errorf("max-concurrent-transfers %d must be a positive integer", cfg.MaxConcurrentTransfers)
	}
	if opts.TransferTimeout != 0 {
		cfg.TransferTimeout = opts.TransferTimeout
	}
	if cfg.TransferTimeout < 0 {
		return nil, fmt.Errorf("transfer-timeout %s must not be negative (0 disables)", cfg.TransferTimeout)
	}
	if !filepath.IsAbs(cfg.DataDir) {
		abs, err := filepath.Abs(cfg.DataDir)
		if err != nil {
			return nil, fmt.Errorf("resolve datadir %q: %w", cfg.DataDir, err)
		}
		cfg.DataDir = abs
	}
	return cfg, nil
}

// ListenAddr 返回 HTTP 监听地址（":<port>"，绑定全部网卡）。
func (c *Config) ListenAddr() string {
	return ":" + strconv.Itoa(c.Port)
}

// EnsureDataDir 创建数据目录与日志目录，避免 logger 初始化写文件失败。
func (c *Config) EnsureDataDir() error {
	if err := os.MkdirAll(c.DataDir, 0o755); err != nil {
		return fmt.Errorf("create datadir %s: %w", c.DataDir, err)
	}
	return os.MkdirAll(filepath.Join(c.DataDir, "logs"), 0o755)
}

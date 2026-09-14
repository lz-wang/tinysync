// Package app 是应用生命周期与装配根（composition root）：
// 从 config/logging 等低层包构造服务并驱动其生命周期。
// 骨架阶段先建立生命周期骨架，HTTP 服务在后续提交接入。
package app

import (
	"context"

	"tinysync/internal/config"
	"tinysync/internal/logging"
)

// Run 启动应用并阻塞直至 ctx 取消（SIGINT/SIGTERM）。
// 返回 nil 表示正常退出（含优雅关闭），非 nil 表示启动或运行失败。
func Run(ctx context.Context, cfg *config.Config) error {
	logging.Infof("tinysync ready (addr=%s)", cfg.ListenAddr())
	<-ctx.Done()
	logging.Infof("shutting down")
	return nil
}

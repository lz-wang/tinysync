// TinySync 是一个 HomeLab 文件同步服务。
//
// main 负责 signal context、嵌入前端静态资源与命令分发：CLI 解析在
// internal/cmd，应用生命周期在 internal/app（composition root）。
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"tinysync/internal/cmd"
	webui "tinysync/web"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	// 嵌入的前端静态资源（-tags webui 时为 web/dist，否则为 fallback 页面）。
	if err := cmd.NewCommand(webui.Content()).Run(ctx, os.Args); err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}
}

//go:build !webui

// Package webui 暴露构建期嵌入的 WebUI 静态资源。
// 不带 webui tag 时（普通 go build / go test / go vet）嵌入 fallback 页面，
// 保证无 Node 环境（未构建 web/dist）时 Go 代码仍可完整编译与测试。
package webui

import (
	"embed"
	"io/fs"
)

//go:embed fallback
var embeddedFallback embed.FS

// Content 返回源码树内的 fallback 页面（已剥去 fallback 前缀）。
func Content() fs.FS {
	content, err := fs.Sub(embeddedFallback, "fallback")
	if err != nil {
		panic(err)
	}
	return content
}

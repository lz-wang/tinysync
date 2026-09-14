//go:build webui

// Package webui 暴露构建期嵌入的 WebUI 静态资源。
// 使用 -tags webui 构建时嵌入 Vite 产物 web/dist；普通 go build / go test
// 不带该 tag，编译 fallback（见 embed_fallback.go），保证无 Node 环境
// 也能完整编译与测试 Go 代码。
package webui

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var embeddedDistribution embed.FS

// Content 返回嵌入的 Vite 生产构建产物（已剥去 dist 前缀）。
func Content() fs.FS {
	content, err := fs.Sub(embeddedDistribution, "dist")
	if err != nil {
		panic(err)
	}
	return content
}

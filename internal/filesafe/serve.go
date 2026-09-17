package filesafe

import (
	"mime"
	"net/http"
	"os"
	"path/filepath"
)

// ServeFileContent 以共享的 serving 语义输出已打开的本地普通文件，
// 是 Local 下载与 Published HTTP serving 共用的唯一出口：MIME 按
// name 扩展名推断（未知回退 application/octet-stream）并显式落
// Content-Type，Range / 206 / 416 / HEAD 统一交给 http.ServeContent
// 处理。缓存控制、下载处置等端点策略头由调用方自行补充。
func ServeFileContent(w http.ResponseWriter, r *http.Request, f *os.File, info os.FileInfo, name string) {
	contentType := mime.TypeByExtension(filepath.Ext(name))
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	w.Header().Set("Content-Type", contentType)
	http.ServeContent(w, r, name, info.ModTime(), f)
}

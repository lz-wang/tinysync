package webui

import (
	"io/fs"
	"strings"
	"testing"
)

// Content() 在两种构建变体（webui / fallback）下都必须提供可读的 index.html，
// 且页面标题为 TinySync（smoke 测试依赖该标题）。
func TestContentProvidesIndex(t *testing.T) {
	data, err := fs.ReadFile(Content(), "index.html")
	if err != nil {
		t.Fatalf("read index.html: %v", err)
	}
	if !strings.Contains(string(data), "<title>TinySync</title>") {
		t.Fatalf("index.html has no <title>TinySync</title>: %s", data)
	}
}

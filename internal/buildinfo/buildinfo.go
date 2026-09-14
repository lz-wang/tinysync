// Package buildinfo 是版本号的唯一运行时载体。
//
// Version 的真实来源是 Git：Makefile 解析版本后通过
//
//	-ldflags "-X tinysync/internal/buildinfo.Version=..."
//
// 在构建期注入。未经注入时保持 "dev"，用于 `go run` 等开发场景。
// 不引入 VERSION 文件、package version、config version 等第二事实来源。
package buildinfo

var Version = "dev"

// TinySync 是一个 HomeLab 文件同步服务。
//
// 当前处于工程骨架阶段：main 只打印版本号，应用运行时
// （cmd → app → config/logging → api）在后续提交中逐步建立。
package main

import (
	"fmt"
	"os"

	"tinysync/internal/buildinfo"
)

func main() {
	fmt.Println(buildinfo.Version)
	os.Exit(0)
}

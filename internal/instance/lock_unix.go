//go:build unix

package instance

import (
	"os"
	"syscall"
)

// lockFileExclusive 对文件整体加非阻塞独占锁（flock 语义）：已被
// 其他进程持有时立即返回错误。锁随文件描述符存活，进程退出（含
// crash）后由内核自动释放。
func lockFileExclusive(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
}

// unlockFile 释放 flock。
func unlockFile(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
}

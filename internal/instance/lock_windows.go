//go:build windows

package instance

import (
	"os"

	"golang.org/x/sys/windows"
)

// lockFileExclusive 对文件开头 1 字节区间加非阻塞独占锁
// （LockFileEx，LOCKFILE_EXCLUSIVE_LOCK | LOCKFILE_FAIL_IMMEDIATELY）：
// 已被其他进程持有时立即返回错误。句柄关闭（含进程 crash）后锁由
// 系统自动释放。stdlib syscall 不导出该 API，使用 golang.org/x/sys/windows。
func lockFileExclusive(f *os.File) error {
	var overlapped windows.Overlapped
	return windows.LockFileEx(
		windows.Handle(f.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
		0, 1, 0, &overlapped,
	)
}

// unlockFile 释放 LockFileEx 锁定的区间。
func unlockFile(f *os.File) error {
	var overlapped windows.Overlapped
	return windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, &overlapped)
}

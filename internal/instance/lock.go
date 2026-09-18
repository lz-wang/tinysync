// Package instance 承载进程级实例约束：同一 datadir 同一时刻只允许
// 一个 TinySync 进程持有（one datadir = one process）。Runner /
// Scheduler 不是多进程协调系统，两个实例并发运行会破坏调度触发、
// 传输互斥与 Mirror ownership 假设；数据库锁无法表达这些语义。
//
// 锁通过 OS 级 file lock 实现（POSIX flock / Windows LockFileEx）：
// 进程 crash 后由 OS 自动释放，不依赖清理逻辑，也不以「锁文件存在」
// 判定归属——锁文件本身在进程退出后保留无害。
package instance

import (
	"fmt"
	"os"
	"path/filepath"
)

// LockFileName 是数据目录下的锁文件名。
const LockFileName = "tinysync.lock"

// Lock 是已获取的 datadir 独占锁。Release 释放 OS 锁并关闭文件句柄，
// 多次调用安全。锁文件本身不删除。
type Lock struct {
	file *os.File
}

// Acquire 以独占、非阻塞的方式获取 datadir 的进程所有权：另一个
// TinySync 进程已持有锁时立即失败（fail-fast），错误信息包含 datadir
// 路径、不包含任何 secret。目录不存在时自动创建。
func Acquire(dataDir string) (*Lock, error) {
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, fmt.Errorf("create datadir %s: %w", dataDir, err)
	}
	path := filepath.Join(dataDir, LockFileName)
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open lock file %s: %w", path, err)
	}
	if err := lockFileExclusive(f); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("datadir %s is owned by another tinysync process: %w", dataDir, err)
	}
	return &Lock{file: f}, nil
}

// Release 释放锁；多次调用安全。
func (l *Lock) Release() error {
	if l == nil || l.file == nil {
		return nil
	}
	err := unlockFile(l.file)
	if cerr := l.file.Close(); err == nil {
		err = cerr
	}
	l.file = nil
	return err
}

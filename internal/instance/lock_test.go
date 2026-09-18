package instance

import (
	"path/filepath"
	"strings"
	"testing"
)

// 首次获取成功；同一 datadir 的第二次获取 fail-fast 失败，错误信息
// 包含 datadir 路径（不包含 secret——本包不接触任何凭据）。
func TestAcquireExclusive(t *testing.T) {
	dir := t.TempDir()

	first, err := Acquire(dir)
	if err != nil {
		t.Fatalf("first Acquire: %v", err)
	}
	defer func() { _ = first.Release() }()

	second, err := Acquire(dir)
	if err == nil {
		_ = second.Release()
		t.Fatal("second Acquire on locked datadir = nil error, want failure")
	}
	if !strings.Contains(err.Error(), dir) {
		t.Errorf("error %q does not mention datadir %s", err, dir)
	}
	if !strings.Contains(err.Error(), "another tinysync process") {
		t.Errorf("error %q does not explain exclusive ownership", err)
	}
}

// Release 后可重新获取：锁与文件句柄生命周期一致，不依赖锁文件消失。
func TestReleaseThenReacquire(t *testing.T) {
	dir := t.TempDir()

	first, err := Acquire(dir)
	if err != nil {
		t.Fatalf("first Acquire: %v", err)
	}
	if err := first.Release(); err != nil {
		t.Fatalf("first Release: %v", err)
	}
	// 多次 Release 安全。
	if err := first.Release(); err != nil {
		t.Fatalf("second Release: %v", err)
	}

	second, err := Acquire(dir)
	if err != nil {
		t.Fatalf("reacquire after release: %v", err)
	}
	_ = second.Release()
}

// 不同 datadir 可同时持有各自的锁（多实例并存于不同数据目录）。
func TestIndependentDataDirs(t *testing.T) {
	dirA := filepath.Join(t.TempDir(), "a")
	dirB := filepath.Join(t.TempDir(), "b")

	lockA, err := Acquire(dirA)
	if err != nil {
		t.Fatalf("acquire A: %v", err)
	}
	defer func() { _ = lockA.Release() }()

	lockB, err := Acquire(dirB)
	if err != nil {
		t.Fatalf("acquire B: %v", err)
	}
	defer func() { _ = lockB.Release() }()
}

// nil Lock 的 Release 安全（防御性：生命周期边界外调用不 panic）。
func TestReleaseNilLock(t *testing.T) {
	var l *Lock
	if err := l.Release(); err != nil {
		t.Fatalf("Release on nil lock: %v", err)
	}
}

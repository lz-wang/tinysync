//go:build !windows

package local

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"

	"tinysync/internal/filesafe"
	"tinysync/internal/source"
)

func TestSpecialFilesFailClosed(t *testing.T) {
	for _, kind := range []string{"fifo", "socket"} {
		t.Run(kind, func(t *testing.T) {
			root, err := os.MkdirTemp("", "ts-local-")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.RemoveAll(root) })
			r := newTestRemote(t, root)
			native := filepath.Join(root, "special")
			switch kind {
			case "fifo":
				if err := unix.Mkfifo(native, 0o600); err != nil {
					t.Fatal(err)
				}
			case "socket":
				listener, err := net.Listen("unix", native)
				if err != nil {
					t.Fatal(err)
				}
				defer listener.Close()
			}
			ctx := context.Background()
			if _, err := r.Stat(ctx, "/special"); !errors.Is(err, filesafe.ErrNotRegularFile) {
				t.Fatalf("stat=%v", err)
			}
			if _, err := r.Open(ctx, "/special"); !errors.Is(err, filesafe.ErrNotRegularFile) {
				t.Fatalf("open=%v", err)
			}
			if _, err := r.List(ctx, "/", source.ListOptions{}); !errors.Is(err, filesafe.ErrNotRegularFile) {
				t.Fatalf("list=%v", err)
			}
			if err := r.ScanTree(ctx, "/", func(source.FileInfo) error { return nil }); !errors.Is(err, filesafe.ErrNotRegularFile) {
				t.Fatalf("scan=%v", err)
			}
			if err := r.Mkdir(ctx, "/special/child"); err == nil {
				t.Fatal("mkdir under special accepted")
			}
		})
	}
	// 设备文件使用系统已有节点，测试不创建 privileged device。
	if _, err := os.Stat("/dev/null"); err == nil {
		r := newTestRemote(t, "/")
		if _, err := r.Stat(context.Background(), "/dev/null"); !errors.Is(err, filesafe.ErrNotRegularFile) {
			t.Fatalf("device=%v", err)
		}
	}
}

func TestPermissionDeniedFailsScan(t *testing.T) {
	root := t.TempDir()
	r := newTestRemote(t, root)
	dir := filepath.Join(root, "blocked")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(dir, 0o700)
	if _, err := os.ReadDir(dir); err == nil {
		t.Skip("运行用户可绕过目录权限")
	}
	ctx := context.Background()
	if _, err := r.List(ctx, "/blocked", source.ListOptions{}); !errors.Is(err, os.ErrPermission) {
		t.Fatalf("list permission=%v", err)
	}
	if err := r.ScanTree(ctx, "/", func(source.FileInfo) error { return nil }); !errors.Is(err, os.ErrPermission) {
		t.Fatalf("scan permission=%v", err)
	}
}

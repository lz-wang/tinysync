//go:build !windows

package e2e

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"

	"tinysync/internal/syncjob"
)

func TestLocalMirrorRejectsIncompleteSnapshots(t *testing.T) {
	for _, failure := range []string{"symlink", "fifo", "socket", "permission"} {
		t.Run(failure, func(t *testing.T) {
			// Unix socket 使用短临时路径，避免系统 socket path 长度上限。
			root, err := os.MkdirTemp("", "ts-e2e-")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.RemoveAll(root) })
			fixture := newLocalFixtureAt(t, root)
			e := newMatrixEnv(t, fixture)
			ctx := context.Background()
			fixture.put(t, "/keep.txt", "keep")
			target := t.TempDir()
			job := newMatrixJob(t, e, target, "mirror")
			runAndWait(t, e, job.ID)
			fixture.remove(t, "/keep.txt")
			special := filepath.Join(root, "invalid")
			switch failure {
			case "symlink":
				if err := os.Symlink(t.TempDir(), special); err != nil {
					t.Fatal(err)
				}
			case "fifo":
				if err := unix.Mkfifo(special, 0o600); err != nil {
					t.Fatal(err)
				}
			case "socket":
				listener, err := net.Listen("unix", special)
				if err != nil {
					t.Fatal(err)
				}
				defer listener.Close()
			case "permission":
				if err := os.Mkdir(special, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(special, 0); err != nil {
					t.Fatal(err)
				}
				defer os.Chmod(special, 0o700)
				if _, err := os.ReadDir(special); err == nil {
					t.Skip("运行用户可绕过目录权限")
				}
			}
			id, err := e.runner.Start(ctx, job.ID)
			if err != nil {
				t.Fatal(err)
			}
			status, err := e.runner.Wait(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			if status.State != syncjob.RunFailed {
				t.Fatalf("status=%+v", status)
			}
			assertLocalFile(t, target, "keep.txt", "keep")
			managed, err := e.managed.ListByJob(ctx, job.ID)
			if err != nil || len(managed) != 1 {
				t.Fatalf("managed=%v %v", managed, err)
			}
		})
	}
}

package local

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"tinysync/internal/source"
)

func TestScanTreeContracts(t *testing.T) {
	root := t.TempDir()
	r := newTestRemote(t, root)
	ctx := context.Background()
	if err := r.Mkdir(ctx, "/dir"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "dir", "file"), []byte("content"), 0o600); err != nil {
		t.Fatal(err)
	}
	var paths []string
	if err := r.ScanTree(ctx, "/dir", func(fi source.FileInfo) error { paths = append(paths, fi.Path); return nil }); err != nil {
		t.Fatal(err)
	}
	if len(paths) != 1 || paths[0] != "/dir/file" {
		t.Fatalf("subtree=%v", paths)
	}
	stop := errors.New("visitor failed")
	if err := r.ScanTree(ctx, "/", func(source.FileInfo) error { return stop }); !errors.Is(err, stop) {
		t.Fatalf("visitor=%v", err)
	}
	cancelCtx, cancel := context.WithCancel(ctx)
	if err := r.ScanTree(cancelCtx, "/", func(source.FileInfo) error { cancel(); return nil }); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel=%v", err)
	}
	if err := r.ScanTree(ctx, "/", func(fi source.FileInfo) error {
		if fi.IsDir {
			return os.RemoveAll(filepath.Join(root, "dir"))
		}
		return nil
	}); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("partial=%v", err)
	}
}

func TestSymlinksFailClosed(t *testing.T) {
	root := t.TempDir()
	r := newTestRemote(t, root)
	ctx := context.Background()
	if err := r.Mkdir(ctx, "/dir"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "dir", "file"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "dir"), filepath.Join(root, "link")); err != nil {
		t.Skip(err)
	}
	for _, p := range []string{"/link", "/link/file"} {
		if _, err := r.Stat(ctx, p); err == nil {
			t.Fatal("stat followed link")
		}
		if _, err := r.Open(ctx, p); err == nil {
			t.Fatal("open followed link")
		}
		if _, err := r.List(ctx, p, source.ListOptions{}); err == nil {
			t.Fatal("list followed link")
		}
	}
	if err := r.Mkdir(ctx, "/link/new"); err == nil {
		t.Fatal("mkdir followed link")
	}
	if err := r.ScanTree(ctx, "/", func(source.FileInfo) error { return nil }); err == nil {
		t.Fatal("scan ignored link")
	}
	if _, err := r.List(ctx, "/", source.ListOptions{}); err == nil {
		t.Fatal("list ignored link")
	}
}

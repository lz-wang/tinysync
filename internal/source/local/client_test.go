package local

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"tinysync/internal/source"
)

func newTestRemote(t *testing.T, root string) *Remote {
	t.Helper()
	cfg, err := source.PrepareConfig(source.TypeLocal, source.Config{Local: &source.LocalConfig{Root: root}})
	if err != nil {
		t.Fatal(err)
	}
	remote, err := NewFactory().Create(context.Background(), source.Source{Type: source.TypeLocal, Config: cfg}, source.Credentials{})
	if err != nil {
		t.Fatal(err)
	}
	return remote.(*Remote)
}

func TestFactoryAndMkdir(t *testing.T) {
	ctx := context.Background()
	if _, err := NewFactory().Create(ctx, source.Source{}, source.Credentials{}); err == nil {
		t.Fatal("accepted foreign type")
	}
	if _, err := NewFactory().Create(ctx, source.Source{Type: source.TypeLocal}, source.Credentials{}); err == nil {
		t.Fatal("accepted missing config")
	}
	root := t.TempDir()
	r := newTestRemote(t, root)
	if err := r.Mkdir(ctx, "/dir"); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"/", "/dir", "/missing/child", "/../outside"} {
		if err := r.Mkdir(ctx, p); err == nil {
			t.Fatalf("mkdir accepted %q", p)
		}
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Stat(ctx, "/"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("deleted root: %v", err)
	}
}

func TestOpenCancellation(t *testing.T) {
	root := t.TempDir()
	r := newTestRemote(t, root)
	if err := os.WriteFile(filepath.Join(root, "large"), make([]byte, 1<<20), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f, err := r.Open(ctx, "/large")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.Read(make([]byte, 4096)); err != nil {
		t.Fatal(err)
	}
	cancel()
	if _, err := f.Read(make([]byte, 4096)); !errors.Is(err, context.Canceled) {
		t.Fatalf("read after cancel=%v", err)
	}
}

func TestFactoryRejectsCredentials(t *testing.T) {
	cfg, err := source.PrepareConfig(source.TypeLocal, source.Config{Local: &source.LocalConfig{Root: t.TempDir()}})
	if err != nil {
		t.Fatal(err)
	}
	src := source.Source{Type: source.TypeLocal, Config: cfg}
	for _, tc := range []struct {
		name  string
		creds source.Credentials
	}{
		{"webdav", source.Credentials{WebDAV: &source.WebDAVCredentials{}}},
		{"s3", source.Credentials{S3: &source.S3Credentials{}}},
		{"sftp", source.Credentials{SFTP: &source.SFTPCredentials{}}},
		{"smb", source.Credentials{SMB: &source.SMBCredentials{}}},
		{"github_release", source.Credentials{GitHubRelease: &source.GitHubReleaseCredentials{}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			remote, err := NewFactory().Create(context.Background(), src, tc.creds)
			if remote != nil {
				_ = remote.Close()
				t.Fatal("returned a remote for credentials-bearing local source")
			}
			if !errors.Is(err, source.ErrInvalid) {
				t.Fatalf("Create = %v; want ErrInvalid", err)
			}
		})
	}
}

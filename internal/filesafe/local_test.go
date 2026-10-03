package filesafe

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestPathsOverlap(t *testing.T) {
	base := filepath.VolumeName(t.TempDir()) + string(filepath.Separator)
	for _, tc := range []struct {
		a, b string
		want bool
	}{
		{base, base, true}, {base, filepath.Join(base, "home/user"), true},
		{filepath.Join(base, "home/user"), filepath.Join(base, "home/user/data"), true},
		{filepath.Join(base, "home/user"), filepath.Join(base, "home/users"), false},
		{filepath.Join(base, "a/b"), filepath.Join(base, "a/c"), false},
	} {
		if got := PathsOverlap(tc.a, tc.b); got != tc.want {
			t.Errorf("PathsOverlap(%q,%q)=%v", tc.a, tc.b, got)
		}
		if got := PathsOverlap(tc.b, tc.a); got != tc.want {
			t.Errorf("reverse PathsOverlap=%v", got)
		}
	}
}

func TestCanonicalExistingDir(t *testing.T) {
	root := t.TempDir()
	want, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	got, err := CanonicalExistingDir("  " + root + "  ")
	if err != nil || got != want {
		t.Fatalf("got %q, %v", got, err)
	}
	file := filepath.Join(root, "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"", filepath.Join(root, "missing"), file} {
		if _, err := CanonicalExistingDir(p); err == nil {
			t.Errorf("accepted %q", p)
		}
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(root, link); err != nil {
		t.Skip(err)
	}
	if got, err := CanonicalExistingDir(link); err != nil || got != want {
		t.Fatalf("link: %q, %v", got, err)
	}
}

func TestResolveNoSymlink(t *testing.T) {
	root, err := CanonicalExistingDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "dir", "file"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ResolveNoSymlink(root, "/dir/file"); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"/../outside", "/dir\\file", "/dir/file/child"} {
		if _, _, err := ResolveNoSymlink(root, p); err == nil {
			t.Errorf("accepted %q", p)
		}
	}
	if err := os.Symlink(filepath.Join(root, "dir"), filepath.Join(root, "link")); err != nil {
		t.Skip(err)
	}
	for _, p := range []string{"/link", "/link/file"} {
		if _, _, err := ResolveNoSymlink(root, p); !errors.Is(err, ErrNotRegularFile) {
			t.Errorf("%s: %v", p, err)
		}
	}
	if _, _, err := ResolveNoSymlink(filepath.Join(root, "link"), "/file"); !errors.Is(err, ErrNotRegularFile) {
		t.Errorf("root: %v", err)
	}
}

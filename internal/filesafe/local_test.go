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

func TestExistingDirectoriesOverlap(t *testing.T) {
	base := t.TempDir()
	for _, dir := range []string{"data/sub", "database", "backup"} {
		if err := os.MkdirAll(filepath.Join(base, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		a, b string
		want bool
	}{
		{"data", "data", true},
		{"data", "data/sub", true},
		{"data", "database", false},
		{"data/sub", "backup", false},
	} {
		t.Run(tc.a+"-"+tc.b, func(t *testing.T) {
			a, b := filepath.Join(base, tc.a), filepath.Join(base, tc.b)
			for _, pair := range [][2]string{{a, b}, {b, a}} {
				got, err := ExistingDirectoriesOverlap(pair[0], pair[1])
				if err != nil || got != tc.want {
					t.Fatalf("overlap(%q, %q) = %v, %v; want %v", pair[0], pair[1], got, err, tc.want)
				}
			}
		})
	}
	file := filepath.Join(base, "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []string{filepath.Join(base, "missing"), file} {
		for _, pair := range [][2]string{{base, invalid}, {invalid, base}} {
			if _, err := ExistingDirectoriesOverlap(pair[0], pair[1]); err == nil {
				t.Fatalf("accepted invalid directory %q", invalid)
			}
		}
	}
}

func TestExistingDirectoriesOverlapSymlinkAlias(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "data")
	if err := os.MkdirAll(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(base, "alias")
	if err := os.Symlink(dir, alias); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	for _, target := range []string{alias, filepath.Join(alias, "sub")} {
		got, err := ExistingDirectoriesOverlap(dir, target)
		if err != nil || !got {
			t.Fatalf("alias overlap = %v, %v", got, err)
		}
	}
}

func TestExistingDirectoriesOverlapCaseInsensitive(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "Data")
	if err := os.MkdirAll(filepath.Join(dir, "Sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(base, "data")
	info, err := os.Stat(alias)
	if os.IsNotExist(err) {
		t.Skip("test volume is case-sensitive")
	}
	if err != nil {
		t.Fatal(err)
	}
	original, err := os.Stat(dir)
	if err != nil || !os.SameFile(original, info) {
		t.Fatalf("case alias is not the same directory: %v", err)
	}
	for _, target := range []string{alias, filepath.Join(alias, "sub")} {
		for _, pair := range [][2]string{{dir, target}, {target, dir}} {
			got, err := ExistingDirectoriesOverlap(pair[0], pair[1])
			if err != nil || !got {
				t.Fatalf("case overlap(%q, %q) = %v, %v", pair[0], pair[1], got, err)
			}
		}
	}
}

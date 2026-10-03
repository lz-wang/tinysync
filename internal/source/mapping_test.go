package source

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"tinysync/internal/filesafe"
)

func TestLocalMappingMatrix(t *testing.T) {
	base, err := filesafe.CanonicalExistingDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"data/a/out", "data/a/sub", "data/b", "backup"} {
		if err := os.MkdirAll(filepath.Join(base, p), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		root, remote, target string
		reject               bool
	}{
		{"data/a", "/", "data/a", true}, {"data/a", "/", "data/a/out", true},
		{"data/a/sub", "/", "data/a", true}, {"data", "/a", "data/a/out", true},
		{"data", "/a", "data/b", false}, {"data", "/a", "backup", false},
		{"data", "/", "backup", false},
	} {
		t.Run(tc.root+tc.remote+"-"+tc.target, func(t *testing.T) {
			src := Source{Type: TypeLocal, Config: Config{Local: &LocalConfig{Root: filepath.Join(base, tc.root)}}}
			err := ValidateJobMapping(src, tc.remote, filepath.Join(base, tc.target))
			if (err != nil) != tc.reject {
				t.Fatalf("mapping: %v, reject=%v", err, tc.reject)
			}
		})
	}
}

func TestLocalMappingRejectsCaseInsensitiveOverlap(t *testing.T) {
	base, err := filesafe.CanonicalExistingDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(base, "Data")
	if err := os.MkdirAll(filepath.Join(root, "Sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(base, "data")
	if _, err := os.Stat(alias); os.IsNotExist(err) {
		t.Skip("test volume is case-sensitive")
	} else if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ root, target string }{
		{root, alias}, {root, filepath.Join(alias, "sub")},
		{filepath.Join(root, "Sub"), alias},
	} {
		src := Source{Type: TypeLocal, Config: Config{Local: &LocalConfig{Root: tc.root}}}
		if err := ValidateJobMapping(src, "/", tc.target); !errors.Is(err, ErrInvalid) {
			t.Fatalf("accepted overlapping mapping %q -> %q: %v", tc.root, tc.target, err)
		}
	}
}

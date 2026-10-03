package local

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"tinysync/internal/filesafe"
	"tinysync/internal/source"
)

func BenchmarkScanTree(b *testing.B) {
	root, err := filesafe.CanonicalExistingDir(b.TempDir())
	if err != nil {
		b.Fatal(err)
	}
	for i := 0; i < 1000; i++ {
		if err := os.WriteFile(filepath.Join(root, fmt.Sprintf("file-%04d", i)), nil, 0o600); err != nil {
			b.Fatal(err)
		}
	}
	r := &Remote{root: root}
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		count := 0
		if err := r.ScanTree(ctx, "/", func(source.FileInfo) error { count++; return nil }); err != nil {
			b.Fatal(err)
		}
		if count != 1000 {
			b.Fatalf("visited=%d", count)
		}
	}
}

package syncjob

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"tinysync/internal/source"
)

// benchmark 皆为 v1.0 前的性能基线证据（make benchmark）：只记录
// ns/op、B/op、allocs/op 作为优化依据，不作为 CI 门禁。

// b10kRemote 构造 total 个文件的远端快照。
func b10kRemote(total int) []source.FileInfo {
	files := make([]source.FileInfo, 0, total)
	for i := 0; i < total; i++ {
		files = append(files, source.FileInfo{
			Path:  fmt.Sprintf("/bulk/f%06d.txt", i),
			IsDir: false,
			Fingerprint: source.Fingerprint{
				Size:       64,
				ModifiedAt: time.Unix(1757879400, 0).UTC(),
				ETag:       fmt.Sprintf(`"etag-%06d"`, i),
			},
		})
	}
	return files
}

// BenchmarkPlan10K：10k 远端 vs 10k managed（10% 变更）的 planner 成本。
func BenchmarkPlan10K(b *testing.B) {
	const total = 10000
	remote := b10kRemote(total)
	managed := make([]ManagedFile, 0, total)
	selected := make(map[string]bool, total)
	for i, fi := range remote {
		selected[fmt.Sprintf("bulk/f%06d.txt", i)] = true
		rec := ManagedFile{
			JobID:        "job_bench",
			RemotePath:   fi.Path,
			LocalRelPath: strings.TrimPrefix(fi.Path, "/"),
			State:        StateSynced,
			Remote:       fi.Fingerprint,
		}
		if i%10 == 0 {
			// 10% 指纹变化：走 update 分支。
			rec.Remote.ETag = `"changed"`
		}
		managed = append(managed, rec)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		plan := BuildPlan(ModeMirror, "/", remote, selected, managed)
		if len(plan.Updates) != total/10 {
			b.Fatalf("updates = %d, want %d", len(plan.Updates), total/10)
		}
	}
}

// BenchmarkSmallFileSync：100 个小文件的完整引擎同步（真实磁盘 IO）。
func BenchmarkSmallFileSync(b *testing.B) {
	const total = 100
	localRoot := b.TempDir()
	content := strings.Repeat("x", 64)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		remote := &benchRemote{files: make(map[string]string, total)}
		for j := 0; j < total; j++ {
			remote.files[fmt.Sprintf("/f%03d.txt", j)] = content
		}
		managed := newInMemoryManaged()
		b.StartTimer()

		if _, err := Run(context.Background(), RunOptions{
			Remote:  remote,
			Job:     Job{ID: "job_bench", RemoteRoot: "/", LocalRoot: localRoot, Mode: ModeCopy, Enabled: true},
			Managed: managed,
		}); err != nil {
			b.Fatalf("run: %v", err)
		}
	}
}

// BenchmarkLargeFileTransfer：32 MiB 单文件原子下载（含磁盘写入、
// fsync 与 rename）。
func BenchmarkLargeFileTransfer(b *testing.B) {
	const size = 32 << 20
	payload := strings.Repeat("a", size)
	localRoot := b.TempDir()

	b.SetBytes(size)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		remote := &benchRemote{files: map[string]string{"/big.bin": payload}}
		d := NewDownloader(remote)
		if err := d.Download(context.Background(), "/big.bin", localRoot, fmt.Sprintf("big-%d.bin", i), source.Fingerprint{Size: size}); err != nil {
			b.Fatalf("download: %v", err)
		}
		_ = os.Remove(filepath.Join(localRoot, fmt.Sprintf("big-%d.bin", i)))
	}
}

// benchRemote 是 benchmark 用的最小 Remote。
type benchRemote struct {
	files map[string]string
}

func (r *benchRemote) Stat(ctx context.Context, path string) (source.FileInfo, error) {
	content, ok := r.files[path]
	if !ok {
		return source.FileInfo{}, os.ErrNotExist
	}
	return source.FileInfo{Path: path, Fingerprint: source.Fingerprint{Size: int64(len(content))}}, nil
}

func (r *benchRemote) List(ctx context.Context, path string, opts source.ListOptions) (source.FilePage, error) {
	return source.FilePage{}, nil
}

func (r *benchRemote) Open(ctx context.Context, path string) (io.ReadCloser, error) {
	content, ok := r.files[path]
	if !ok {
		return nil, os.ErrNotExist
	}
	return io.NopCloser(strings.NewReader(content)), nil
}

func (r *benchRemote) Close() error { return nil }

package githubrelease

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"tinysync/internal/source"
	"tinysync/internal/syncjob"
)

// startBenchRemote 启动假 GitHub（numReleases 个版本、每版本
// assetsPerRelease 个 Asset）并构造指向它的 Remote：进程内假服务端，
// 基准不含真实网络延迟，度量的是枚举协议量与 adapter 转换开销。
func startBenchRemote(b *testing.B, numReleases, assetsPerRelease int) source.Remote {
	b.Helper()
	manifest := make([]string, 0, numReleases)
	for i := 0; i < numReleases; i++ {
		id := int64(1000 + i)
		tag := fmt.Sprintf("v1.%d.0", i)
		published := fmt.Sprintf("2026-01-01T%02d:%02d:%02dZ", i/3600%24, i/60%60, i%60)
		manifest = append(manifest,
			fmt.Sprintf(`{"id": %d, "tag_name": %q, "name": %q, "draft": false, "prerelease": false, "published_at": %q}`,
				id, tag, tag, published))
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/gitea/gitea":
			_, _ = w.Write([]byte(`{"full_name": "gitea/gitea"}`))
		case r.URL.Path == "/repos/gitea/gitea/releases":
			// per_page 上限 100：完整枚举需要多页，按 Link header 翻页。
			page := 0
			if p := r.URL.Query().Get("page"); p != "" {
				page, _ = strconv.Atoi(p)
			}
			start, end := page*100, page*100+100
			if start >= len(manifest) {
				_, _ = w.Write([]byte("[]"))
				return
			}
			if end > len(manifest) {
				end = len(manifest)
			}
			if end < len(manifest) {
				w.Header().Set("Link",
					fmt.Sprintf(`<http://%s%s?page=%d>; rel="next"`, r.Host, r.URL.Path, page+1))
			}
			_, _ = w.Write([]byte("[" + joinJSON(manifest[start:end]) + "]"))
		default:
			var releaseID int64
			if _, err := fmt.Sscanf(r.URL.Path, "/repos/gitea/gitea/releases/%d/assets", &releaseID); err == nil {
				assets := make([]string, 0, assetsPerRelease)
				for a := 0; a < assetsPerRelease; a++ {
					assets = append(assets,
						fmt.Sprintf(`{"id": %d, "name": "app-%02d.tar.gz", "size": %d, "updated_at": "2026-01-01T00:00:00Z", "digest": "", "state": "uploaded"}`,
							releaseID*100+int64(a), a, 1024*1024))
				}
				_, _ = w.Write([]byte("[" + joinJSON(assets) + "]"))
				return
			}
			http.NotFound(w, r)
		}
	}))
	b.Cleanup(srv.Close)

	remote, err := NewFactoryWithAPIBase(srv.URL).Create(context.Background(),
		source.Source{
			Type: source.TypeGitHubRelease,
			Config: source.Config{GitHubRelease: &source.GitHubReleaseConfig{
				Repository:    "gitea/gitea",
				ReleasePolicy: source.ReleaseAll,
			}},
		},
		source.Credentials{})
	if err != nil {
		b.Fatalf("create remote: %v", err)
	}
	b.Cleanup(func() { _ = remote.Close() })
	return remote
}

// BenchmarkScanRemoteGitHub 全树扫描：完整枚举 releases 与每版本
// assets，走 TreeScanner fast path（与同步引擎路径一致）。
func BenchmarkScanRemoteGitHub(b *testing.B) {
	remote := startBenchRemote(b, 100, 10)
	ctx := context.Background()
	b.ResetTimer()
	for b.Loop() {
		files, err := syncjob.ScanRemote(ctx, remote, "/")
		if err != nil {
			b.Fatalf("scan: %v", err)
		}
		if len(files) != 100*10 {
			b.Fatalf("files = %d, want %d", len(files), 100*10)
		}
	}
}

// BenchmarkListRootGitHub 浏览路径：root 单层枚举 + 切片分页。
func BenchmarkListRootGitHub(b *testing.B) {
	remote := startBenchRemote(b, 100, 10)
	ctx := context.Background()
	b.ResetTimer()
	for b.Loop() {
		page, err := remote.List(ctx, "/", source.ListOptions{})
		if err != nil {
			b.Fatalf("list: %v", err)
		}
		if len(page.Entries) != 100 {
			b.Fatalf("entries = %d, want 100", len(page.Entries))
		}
	}
}

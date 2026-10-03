package e2e

import (
	"context"
	"path/filepath"
	"testing"

	"tinysync/internal/source"
	httpadapter "tinysync/internal/source/http"
	"tinysync/internal/syncjob"
)

// Mirror 的 fail-closed 同步级反例：先同步 managed files，再注入
// 无法证明完整性的 200 响应；运行必须失败，根文件与目录后代均不删除。
func TestHTTPMirrorUnprovableSnapshotFailClosed(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
	}{
		{"status object", `{"status":"error"}`},
		{"ambiguous empty array", `[]`},
		{"ordinary html marker", `<html><body><script>const className = "entry-type-file";</script></body></html>`},
		{"malformed later caddy directory", `[{"name":"a.txt","size":2,"url":"a.txt","mod_time":"2026-10-01T00:00:00Z","is_dir":false,"is_symlink":false},{"name":"docs","size":4096,"mod_time":"2026-10-01T00:00:00Z"}]`},
		{"unknown miniserve row", `<table><tr class="entry-type-file"><td><a class="file" href="a.txt">a</a></td></tr><tr class="entry-type-special"><td><a class="directory" href="docs/">docs</a></td></tr></table>`},
		{"conflicting miniserve directory", `<table><tr class="entry-type-file"><td><a class="file" href="a.txt">a</a></td></tr><tr class="entry-type-file"><td><a class="directory" href="docs/">docs</a></td></tr></table>`},
	} {
		t.Run(tc.name, func(t *testing.T) { testHTTPMirrorUnprovableSnapshot(t, tc.body) })
	}
}

func testHTTPMirrorUnprovableSnapshot(t *testing.T, body string) {
	t.Helper()
	srv := newHTTPMatrixServer(t)
	srv.put("/a.txt", "v1")
	srv.put("/docs/b.txt", "b1")
	srv.put("/docs/photo.jpg", "jpeg")

	factory := httpadapter.NewFactory()
	src := source.Source{
		Name: "matrix",
		Type: source.TypeHTTP,
		Config: source.Config{HTTP: &source.HTTPConfig{
			BaseURL:     srv.srv.URL + "/",
			ListingMode: source.HTTPListingAuto,
		}},
	}
	remote := matrixRemote{
		name: "http_degraded",
		src:  src,
		put:  func(t *testing.T, logical, content string) { srv.put(logical, content) },
		remove: func(t *testing.T, logical string) {
			srv.remove(logical)
		},
		openRemote: func() (source.Remote, error) {
			return factory.Create(context.Background(), src, source.Credentials{})
		},
	}

	e := newMatrixEnv(t, remote)
	localRoot := t.TempDir()
	job := newMatrixJob(t, e, localRoot, "mirror")

	// 1. 初始 Mirror：三个 managed files 落地。
	stats := runAndWait(t, e, job.ID)
	if stats.FilesCreated != 3 {
		t.Fatalf("initial stats = %+v, want 3 created", stats)
	}
	assertLocalFile(t, localRoot, "a.txt", "v1")
	assertLocalFile(t, localRoot, filepath.Join("docs", "b.txt"), "b1")
	assertLocalFile(t, localRoot, filepath.Join("docs", "photo.jpg"), "jpeg")

	// 2. listing 变成无法证明完整性的响应：本轮运行必须失败。
	srv.degrade(body)
	runID, err := e.runner.Start(context.Background(), job.ID)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	status, err := e.runner.Wait(context.Background(), runID)
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if status.State != syncjob.RunFailed {
		t.Fatalf("run state = %s (%s), want failed", status.State, status.Error)
	}
	if status.Stats.FilesDeleted != 0 {
		t.Errorf("deleted = %d on unprovable snapshot, want 0", status.Stats.FilesDeleted)
	}

	// 3. managed files 一个都不删除。
	assertLocalFile(t, localRoot, "a.txt", "v1")
	assertLocalFile(t, localRoot, filepath.Join("docs", "b.txt"), "b1")
	assertLocalFile(t, localRoot, filepath.Join("docs", "photo.jpg"), "jpeg")

	// 4. 服务恢复后下一轮照常成功（失败不破坏状态）。
	srv.degrade("")
	stats = runAndWait(t, e, job.ID)
	if stats.FilesCreated != 0 || stats.FilesUpdated != 0 || stats.FilesDeleted != 0 {
		t.Fatalf("recovered stats = %+v, want zero changes", stats)
	}
}

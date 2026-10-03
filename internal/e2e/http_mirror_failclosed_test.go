package e2e

import (
	"context"
	"path/filepath"
	"testing"

	"tinysync/internal/source"
	httpadapter "tinysync/internal/source/http"
	"tinysync/internal/syncjob"
)

// Mirror 的 fail-closed 同步级反例（HTTP）：先完成一次 Mirror，然后让
// 服务器从合法 Caddy listing 切换为 `200 {"status":"error"}`（反向代理
// 把文件服务异常吞成 JSON 状态页的形态）。这类响应无法证明快照完整性，
// 扫描必须整轮失败，本地 managed files 一个都不删除——否则「任意
// JSON object 被解释成空 Caddy 目录」会让 Planner 认为全部远端文件
// 已消失并授权 Mirror 删除。
func TestHTTPMirrorUnprovableSnapshotFailClosed(t *testing.T) {
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

	// 2. listing 变成 `200 {"status":"error"}`：本轮运行必须失败。
	srv.degrade(`{"status":"error"}`)
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

package e2e

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"

	"tinysync/internal/source"
	"tinysync/internal/source/githubrelease"
	sourcesqlite "tinysync/internal/source/sqlite"
	"tinysync/internal/syncjob"
	jobsqlite "tinysync/internal/syncjob/sqlite"
)

// fakeReleaseState 是假 GitHub 的可变状态：release 列表与 per-release
// asset 内容。digest 为空表示 GitHub 未提供摘要；测试按需改写状态后
// 触发下一轮同步。
type fakeReleaseState struct {
	mu       sync.Mutex
	repo     string
	releases []fakeRelease
}

type fakeRelease struct {
	id      int64
	tag     string
	publish string // RFC3339；"" 表示无
	draft   bool
	pre     bool
	// assets 是 asset name → 内容；digestKey 声明与内容对应的摘要。
	assets    map[string]string
	digestKey map[string]string
}

func (s *fakeReleaseState) snapshot() ([]fakeRelease, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]fakeRelease, len(s.releases))
	copy(out, s.releases)
	return out, s.repo
}

// assetID 从 release id 与 asset name 派生稳定的假 asset id。
func assetID(releaseID int64, name string) int64 {
	return releaseID*100 + int64(len(name))
}

// serve 实现假 GitHub 的 HTTP 分发：/repos/o/r、/releases、
// /releases/:id/assets、/releases/assets/:id（302 → 同 host CDN 路径
// 再 200，进程内覆盖重定向跟随与 Accept 透传）。
func (s *fakeReleaseState) serve(w http.ResponseWriter, r *http.Request) {
	releases, repoName := s.snapshot()
	writeJSON := func(body string) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}
	switch {
	case r.URL.Path == "/repos/gitea/gitea":
		writeJSON(`{"full_name": "` + repoName + `"}`)
		return
	case r.URL.Path == "/repos/gitea/gitea/releases":
		items := ""
		for i, rel := range releases {
			if i > 0 {
				items += ","
			}
			published := "null"
			if rel.publish != "" {
				published = strconv.Quote(rel.publish)
			}
			items += fmt.Sprintf(`{"id": %d, "tag_name": %q, "name": %q, "draft": %t, "prerelease": %t, "published_at": %s}`,
				rel.id, rel.tag, rel.tag, rel.draft, rel.pre, published)
		}
		writeJSON("[" + items + "]")
		return
	}
	for _, rel := range releases {
		if r.URL.Path == fmt.Sprintf("/repos/gitea/gitea/releases/%d/assets", rel.id) {
			items := ""
			i := 0
			for name, content := range rel.assets {
				if i > 0 {
					items += ","
				}
				i++
				digest := rel.digestKey[name]
				items += fmt.Sprintf(`{"id": %d, "name": %q, "size": %d, "updated_at": %q, "digest": %q, "state": "uploaded"}`,
					assetID(rel.id, name), name, len(content), rel.publish, digest)
			}
			writeJSON("[" + items + "]")
			return
		}
	}
	for _, rel := range releases {
		for name, content := range rel.assets {
			want := fmt.Sprintf("/repos/gitea/gitea/releases/assets/%d", assetID(rel.id, name))
			if r.URL.Path == want {
				if r.Header.Get("Accept") != "application/octet-stream" {
					w.WriteHeader(http.StatusNotAcceptable)
					return
				}
				// 下载直出 200：302 重定向链的跟随、https 白名单与
				// Token 剥除由 adapter 的 redirectTransport 单测覆盖
				//（httptest 为 http，无法在 e2e 内重定向到合法 https
				// 目标——策略拒绝 http 重定向正是其安全语义）。
				_, _ = w.Write([]byte(content))
				return
			}
		}
	}
	http.NotFound(w, r)
}

// sha256Digest 计算内容的 sha256 摘要（GitHub digest 形态）。
func sha256Digest(content string) string {
	sum := sha256.Sum256([]byte(content))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// githubE2E 装配 GitHub Release 端到端环境：假 GitHub + 真实
// factory / Source / Job 服务与 Runner。
type githubE2E struct {
	state   *fakeReleaseState
	sources *source.Service
	jobs    *syncjob.Service
	runner  *syncjob.Runner
}

// startGitHubE2E 启动假 GitHub 并完成装配。
func startGitHubE2E(t *testing.T) *githubE2E {
	t.Helper()
	state := &fakeReleaseState{repo: "gitea/gitea"}
	srv := httptest.NewServer(http.HandlerFunc(state.serve))
	t.Cleanup(srv.Close)

	db := openDB(t, t.TempDir())
	t.Cleanup(func() { _ = db.Close() })
	sources := source.NewService(sourcesqlite.New(db), githubrelease.NewFactoryWithAPIBase(srv.URL))
	jobRepo := jobsqlite.NewRepository(db)
	runner := syncjob.NewRunner(jobRepo, jobsqlite.NewManagedRepository(db), sources, jobsqlite.NewRunRepository(db))
	return &githubE2E{
		state:   state,
		sources: sources,
		jobs:    syncjob.NewService(jobRepo, sources, t.TempDir()),
		runner:  runner,
	}
}

// createGitHubSource 经真实服务创建 github_release Source（recent=2）。
func (g *githubE2E) createGitHubSource(t *testing.T) string {
	t.Helper()
	src, err := g.sources.Create(context.Background(), source.CreateInput{
		Name: "E2E GitHub",
		Type: source.TypeGitHubRelease,
		Config: source.Config{GitHubRelease: &source.GitHubReleaseConfig{
			Repository:    "gitea/gitea",
			ReleasePolicy: source.ReleaseRecent,
			RecentCount:   2,
		}},
		Enabled: true,
	})
	if err != nil {
		t.Fatalf("create github source: %v", err)
	}
	return src.ID
}

// createGitHubJob 创建 remote_root=/ 的 Job。
func (g *githubE2E) createGitHubJob(t *testing.T, name, sourceID, mode string) syncjob.Job {
	t.Helper()
	localRoot := t.TempDir() + "/local"
	if err := os.MkdirAll(localRoot, 0o755); err != nil {
		t.Fatalf("mkdir local root: %v", err)
	}
	job, err := g.jobs.Create(context.Background(), syncjob.CreateInput{
		Name:       name,
		SourceID:   sourceID,
		RemoteRoot: "/",
		LocalRoot:  localRoot,
		Mode:       syncjob.Mode(mode),
		Enabled:    true,
	})
	if err != nil {
		t.Fatalf("create github job: %v", err)
	}
	return job
}

// runAndWait 触发一轮同步并等待终态。
func (g *githubE2E) runAndWait(t *testing.T, jobID string) syncjob.RunStatus {
	t.Helper()
	runID, err := g.runner.Start(context.Background(), jobID)
	if err != nil {
		t.Fatalf("start run: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	st, err := g.runner.Wait(ctx, runID)
	if err != nil {
		t.Fatalf("wait run: %v", err)
	}
	return st
}

// TestGitHubReleasePullEndToEnd 端到端主链路：版本目录结构下载 →
// 二轮 skip → 版本轮换时 Mirror 删除退出版本、Copy 保留 → 新版本
// 继续跟随。
func TestGitHubReleasePullEndToEnd(t *testing.T) {
	g := startGitHubE2E(t)
	g.state.mu.Lock()
	g.state.releases = []fakeRelease{
		{id: 101, tag: "v1.0", publish: "2026-01-01T00:00:00Z",
			assets:    map[string]string{"app-linux.tar.gz": "v1-linux", "SHA256SUMS": "v1-sums"},
			digestKey: map[string]string{}},
		{id: 102, tag: "v1.1", publish: "2026-01-02T00:00:00Z",
			assets:    map[string]string{"app-linux.tar.gz": "v11-linux"},
			digestKey: map[string]string{}},
	}
	g.state.mu.Unlock()

	sourceID := g.createGitHubSource(t)
	copyJob := g.createGitHubJob(t, "Copy", sourceID, string(syncjob.ModeCopy))
	mirrorJob := g.createGitHubJob(t, "Mirror", sourceID, string(syncjob.ModeMirror))

	// 1. 初始下载：两个版本目录各自落地（recent=2 选中 v1.1 与 v1.0）。
	requireSucceeded(t, "copy initial", g.runAndWait(t, copyJob.ID))
	requireSucceeded(t, "mirror initial", g.runAndWait(t, mirrorJob.ID))
	if got := readLocal(t, copyJob.LocalRoot+"/v1.0__101/app-linux.tar.gz"); got != "v1-linux" {
		t.Fatalf("v1.0 copy = %q, want v1-linux", got)
	}
	if got := readLocal(t, copyJob.LocalRoot+"/v1.1__102/app-linux.tar.gz"); got != "v11-linux" {
		t.Fatalf("v1.1 copy = %q, want v11-linux", got)
	}
	// SHA256SUMS 仅存在于 v1.0：v1.1 目录不应出现该文件。
	if _, err := os.Stat(mirrorJob.LocalRoot + "/v1.1__102/SHA256SUMS"); !os.IsNotExist(err) {
		t.Errorf("v1.1 should not contain SHA256SUMS (belongs to v1.0), err = %v", err)
	}

	// 2. 无变化 → skip，零传输（3 个文件：v1.0 两个 + v1.1 一个）。
	st := g.runAndWait(t, copyJob.ID)
	requireSucceeded(t, "copy skip", st)
	if st.Stats.FilesSkipped != 3 || st.Stats.BytesTransferred != 0 {
		t.Fatalf("skip stats = %+v, want skipped=3 no transfer", st.Stats)
	}

	// 3. 版本轮换：v1.2 发布使 v1.0 退出 recent=2 范围。
	g.state.mu.Lock()
	g.state.releases = append(g.state.releases, fakeRelease{
		id: 103, tag: "v1.2", publish: "2026-01-03T00:00:00Z",
		assets:    map[string]string{"app-linux.tar.gz": "v12-linux"},
		digestKey: map[string]string{},
	})
	g.state.mu.Unlock()

	requireSucceeded(t, "mirror rotation", g.runAndWait(t, mirrorJob.ID))
	// Mirror：退出版本的目录被删除；Copy：保留。
	if _, err := os.Stat(mirrorJob.LocalRoot + "/v1.0__101/app-linux.tar.gz"); !os.IsNotExist(err) {
		t.Errorf("mirror still keeps rotated-out version dir, want deleted (err = %v)", err)
	}
	if got := readLocal(t, copyJob.LocalRoot+"/v1.0__101/app-linux.tar.gz"); got != "v1-linux" {
		t.Errorf("copy should keep rotated-out version: %q", got)
	}
	// 新版本继续跟随。
	if got := readLocal(t, mirrorJob.LocalRoot+"/v1.2__103/app-linux.tar.gz"); got != "v12-linux" {
		t.Errorf("mirror v1.2 = %q, want v12-linux", got)
	}
}

// TestGitHubReleasePullChecksumMismatch 同名 Asset 重传（updated_at
// 变化触发 update）后内容与声明 digest 不一致：整轮失败且已有本地
// 文件不被覆盖（验收：SHA-256 不匹配时不覆盖原文件）。
func TestGitHubReleasePullChecksumMismatch(t *testing.T) {
	g := startGitHubE2E(t)
	good := "original-content"
	g.state.mu.Lock()
	g.state.releases = []fakeRelease{
		{id: 201, tag: "v2.0", publish: "2026-02-01T00:00:00Z",
			assets:    map[string]string{"app.zip": good},
			digestKey: map[string]string{"app.zip": sha256Digest(good)}},
	}
	g.state.mu.Unlock()

	sourceID := g.createGitHubSource(t)
	job := g.createGitHubJob(t, "Checksum", sourceID, string(syncjob.ModeCopy))
	requireSucceeded(t, "initial", g.runAndWait(t, job.ID))
	if got := readLocal(t, job.LocalRoot+"/v2.0__201/app.zip"); got != good {
		t.Fatalf("initial content = %q, want %q", got, good)
	}

	// 重传：等长但内容不同，digest 声明未随内容更新（size 校验通过，
	// 摘要校验必须拦截）。updated_at 变化使 Version 变化触发 update。
	g.state.mu.Lock()
	g.state.releases[0].publish = "2026-02-02T00:00:00Z"
	g.state.releases[0].assets["app.zip"] = "tampered-content"
	g.state.mu.Unlock()

	st := g.runAndWait(t, job.ID)
	if st.State != syncjob.RunFailed {
		t.Fatalf("state = %s (%s), want failed on checksum mismatch", st.State, st.Error)
	}
	if got := readLocal(t, job.LocalRoot+"/v2.0__201/app.zip"); got != good {
		t.Errorf("local file overwritten on checksum mismatch: %q", got)
	}
}

// TestIntegrationGitHubReleaseReal 真实公开仓库的可选集成测试：设置
// TINYSYNC_IT_GITHUB_REPO 后运行（如 make integration
// TINYSYNC_IT_GITHUB_REPO=gitea/gitea；私有仓库可另附
// TINYSYNC_IT_GITHUB_TOKEN）；未设置时跳过，不影响退出码。
func TestIntegrationGitHubReleaseReal(t *testing.T) {
	repoInput := os.Getenv("TINYSYNC_IT_GITHUB_REPO")
	if repoInput == "" {
		t.Skip("TINYSYNC_IT_GITHUB_REPO not set; skipping real GitHub integration")
	}
	cfg := source.GitHubReleaseConfig{
		Repository:    repoInput,
		ReleasePolicy: source.ReleaseRecent,
		RecentCount:   1,
	}
	token := os.Getenv("TINYSYNC_IT_GITHUB_TOKEN")
	src := source.Source{
		Type:   source.TypeGitHubRelease,
		Config: source.Config{GitHubRelease: &cfg},
	}
	creds := source.Credentials{GitHubRelease: &source.GitHubReleaseCredentials{Token: token}}

	remote, err := githubrelease.NewFactory().Create(context.Background(), src, creds)
	if err != nil {
		t.Fatalf("create remote for %s: %v", repoInput, err)
	}
	defer func() { _ = remote.Close() }()
	inspector, ok := remote.(source.ReleaseInspector)
	if !ok {
		t.Fatal("github_release remote does not implement ReleaseInspector")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	inspection, err := inspector.Inspect(ctx, source.InspectAssetDetailLimit)
	if err != nil {
		t.Fatalf("inspect %s: %v", repoInput, err)
	}
	if inspection.Repository == "" || len(inspection.Releases) == 0 {
		t.Fatalf("inspection = %+v, want repository and releases", inspection)
	}
	t.Logf("repository=%s releases=%d first=%s", inspection.Repository,
		len(inspection.Releases), inspection.Releases[0].Tag)
}

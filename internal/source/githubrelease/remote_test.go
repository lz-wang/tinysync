package githubrelease

import (
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"tinysync/internal/source"
)

// remoteFixture 是 remote 测试的固定数据：仓库 3 个 Release（均非
// draft 非 prerelease），policy=recent(2) 按 published_at 降序选中
// id 103（v3）与 102（v2/beta，tag 含斜杠），id 101（v1）在选中范围
// 之外；各 Release 带固定 Asset。
type remoteFixture struct {
	f *fakeGitHub
	r *remote
}

// newRemoteFixture 启动假 GitHub 并构造 remote（绕过 factory 的
// verifyRepo 以便各测试自选断言重点；factory 路径由独立测试覆盖）。
func newRemoteFixture(t *testing.T, policy source.GitHubReleasePolicy) *remoteFixture {
	t.Helper()
	f := newFakeGitHub(t, "ghp_token", func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/gitea/gitea":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"full_name": "gitea/gitea"}`))
		case r.URL.Path == "/repos/gitea/gitea/releases":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte("[" + joinJSON([]string{
				releaseJSON(103, "v3", "2026-01-03T00:00:00Z", false, false),
				releaseJSON(102, "v2/beta", "2026-01-02T00:00:00Z", false, false),
				releaseJSON(101, "v1", "2026-01-01T00:00:00Z", false, false),
			}) + "]"))
		case r.URL.Path == "/repos/gitea/gitea/releases/103/assets":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte("[" + joinJSON([]string{
				assetJSON(1001, "app-linux-amd64.tar.gz", 100, "sha256:aaaa", "uploaded"),
				assetJSON(1002, "SHA256SUMS", 64, "", "uploaded"),
				assetJSON(1003, "app-draft.zip", 10, "", "uploading"),
			}) + "]"))
		case r.URL.Path == "/repos/gitea/gitea/releases/102/assets":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte("[" + assetJSON(1004, "app.zip", 200, "", "uploaded") + "]"))
		case r.URL.Path == "/repos/gitea/gitea/releases/assets/1001":
			if got := r.Header.Get("Accept"); got != "application/octet-stream" {
				t.Errorf("Accept = %q, want octet-stream", got)
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("asset-bytes"))
		default:
			http.NotFound(w, r)
		}
	})
	r := &remote{
		client: f.c,
		cfg:    source.GitHubReleaseConfig{ReleasePolicy: policy, RecentCount: 2},
	}
	return &remoteFixture{f: f, r: r}
}

// mustStat Stat 并断言成功。
func (fx *remoteFixture) mustStat(t *testing.T, p string) source.FileInfo {
	t.Helper()
	fi, err := fx.r.Stat(t.Context(), p)
	if err != nil {
		t.Fatalf("Stat(%s): %v", p, err)
	}
	return fi
}

// TestStatRoot 验证 root 探测为目录。
func TestStatRoot(t *testing.T) {
	fx := newRemoteFixture(t, source.ReleaseRecent)
	fi := fx.mustStat(t, "/")
	if !fi.IsDir || fi.Path != "/" {
		t.Errorf("stat / = %+v, want dir", fi)
	}
}

// TestStatVersionDirAndAsset 验证版本目录与 Asset 的 Stat：指纹字段
// 按 §4 映射（Size / ModifiedAt / Checksum / Version 格式）。
func TestStatVersionDirAndAsset(t *testing.T) {
	fx := newRemoteFixture(t, source.ReleaseRecent)
	dir := fx.mustStat(t, "/v3__103")
	if !dir.IsDir || dir.Path != "/v3__103" {
		t.Errorf("stat /v3__103 = %+v, want dir", dir)
	}

	// 选中范围外的 v1（id 101）不可见。
	if _, err := fx.r.Stat(t.Context(), "/v1__101"); err == nil {
		t.Error("stat out-of-policy version dir succeeded, want not found")
	}
	// 不存在的目录形态。
	if _, err := fx.r.Stat(t.Context(), "/unknown-dir"); err == nil {
		t.Error("stat malformed version dir succeeded, want not found")
	}

	fi := fx.mustStat(t, "/v3__103/app-linux-amd64.tar.gz")
	wantUpdated := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC) // assetJSON 固定 updated_at
	if fi.IsDir {
		t.Fatalf("stat asset = dir: %+v", fi)
	}
	if fi.Fingerprint.Size != 100 || fi.Fingerprint.Checksum != "sha256:aaaa" {
		t.Errorf("fingerprint = %+v, want size 100 checksum sha256:aaaa", fi.Fingerprint)
	}
	if !fi.Fingerprint.ModifiedAt.Equal(wantUpdated) {
		t.Errorf("ModifiedAt = %v, want %v", fi.Fingerprint.ModifiedAt, wantUpdated)
	}
	if fi.Fingerprint.Version != "1001:2026-01-01T00:00:00Z:sha256:aaaa" {
		t.Errorf("Version = %q, want asset id/updated/digest combo", fi.Fingerprint.Version)
	}
	// path 是拼接后的绝对逻辑路径。
	if fi.Path != "/v3__103/app-linux-amd64.tar.gz" {
		t.Errorf("Path = %q", fi.Path)
	}
}

// TestListRoot 验证 root 列表输出全部选中版本目录（含 tag 斜杠编码），
// 不含选中范围外的版本；切片分页可用。
func TestListRoot(t *testing.T) {
	fx := newRemoteFixture(t, source.ReleaseRecent)
	page, err := fx.r.List(t.Context(), "/", source.ListOptions{})
	if err != nil {
		t.Fatalf("List /: %v", err)
	}
	if page.NextCursor != "" {
		t.Errorf("NextCursor = %q, want empty (2 entries < default limit)", page.NextCursor)
	}
	var paths []string
	for _, e := range page.Entries {
		paths = append(paths, e.Path)
		if !e.IsDir {
			t.Errorf("entry %s is not a dir", e.Path)
		}
	}
	want := []string{"/v2%2Fbeta__102", "/v3__103"}
	if len(paths) != len(want) {
		t.Fatalf("root entries = %v, want %v", paths, want)
	}
	for i := range want {
		if paths[i] != want[i] {
			t.Errorf("root entries[%d] = %s, want %s", i, paths[i], want[i])
		}
	}

	// 切片分页：limit=1 + cursor。
	page1, err := fx.r.List(t.Context(), "/", source.ListOptions{Limit: 1})
	if err != nil {
		t.Fatalf("List / limit=1: %v", err)
	}
	if len(page1.Entries) != 1 || page1.NextCursor == "" {
		t.Fatalf("page1 = %+v, want 1 entry with cursor", page1)
	}
	page2, err := fx.r.List(t.Context(), "/", source.ListOptions{Limit: 1, Cursor: page1.NextCursor})
	if err != nil {
		t.Fatalf("List / page2: %v", err)
	}
	if len(page2.Entries) != 1 || page2.NextCursor != "" {
		t.Fatalf("page2 = %+v, want last entry without cursor", page2)
	}
}

// TestListVersionAssets 验证版本目录列表输出 Asset 文件（uploaded
// 过滤 + 指纹一致）。
func TestListVersionAssets(t *testing.T) {
	fx := newRemoteFixture(t, source.ReleaseRecent)
	page, err := fx.r.List(t.Context(), "/v3__103", source.ListOptions{})
	if err != nil {
		t.Fatalf("List /v3__103: %v", err)
	}
	if len(page.Entries) != 2 {
		t.Fatalf("entries = %d, want 2 (uploaded only)", len(page.Entries))
	}
	for _, e := range page.Entries {
		if e.IsDir {
			t.Errorf("entry %s should be a file", e.Path)
		}
		// 浏览与扫描同源：此处指纹必须与 Stat 单文件完全一致。
		stat, err := fx.r.Stat(t.Context(), e.Path)
		if err != nil {
			t.Fatalf("Stat(%s): %v", e.Path, err)
		}
		if stat.Fingerprint.Version != e.Fingerprint.Version {
			t.Errorf("fingerprint mismatch between List and Stat for %s: %q vs %q",
				e.Path, e.Fingerprint.Version, stat.Fingerprint.Version)
		}
	}
}

// TestListRejectsInvalidPaths 非法路径 fail-fast：非法逻辑路径、
// 超深度路径、对文件列目录。
func TestListRejectsInvalidPaths(t *testing.T) {
	fx := newRemoteFixture(t, source.ReleaseRecent)
	for _, p := range []string{"/a//b", "/../etc", "/v3__103/app.zip/deeper"} {
		if _, err := fx.r.List(t.Context(), p, source.ListOptions{}); err == nil {
			t.Errorf("List(%s) succeeded, want error", p)
		}
	}
}

// TestOpenAsset 验证下载流：按 asset id 请求 octet-stream，body 原样
// 透传；非文件路径拒绝。
func TestOpenAsset(t *testing.T) {
	fx := newRemoteFixture(t, source.ReleaseRecent)
	rc, err := fx.r.Open(t.Context(), "/v3__103/app-linux-amd64.tar.gz")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer rc.Close()
	body, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("read asset: %v", err)
	}
	if string(body) != "asset-bytes" {
		t.Errorf("asset body = %q", string(body))
	}

	for _, p := range []string{"/", "/v3__103", "/v1__101/app.zip"} {
		if rc, err := fx.r.Open(t.Context(), p); err == nil {
			_ = rc.Close()
			t.Errorf("Open(%s) succeeded, want error", p)
		}
	}
}

// TestScanTreeConsistentWithBrowse 验收核心：扫描与浏览得到一致的
// 文件身份——ScanTree(root=/) 输出的每个文件条目与 Stat/List 的
// Path、Fingerprint 完全一致；目录先于其文件 visit。
func TestScanTreeConsistentWithBrowse(t *testing.T) {
	fx := newRemoteFixture(t, source.ReleaseRecent)

	var got []source.FileInfo
	err := fx.r.ScanTree(t.Context(), "/", func(fi source.FileInfo) error {
		got = append(got, fi)
		return nil
	})
	if err != nil {
		t.Fatalf("ScanTree /: %v", err)
	}
	if len(got) != 5 { // 2 目录 + 3 文件（uploading asset 被过滤）
		t.Fatalf("scan entries = %d, want 5", len(got))
	}
	// 深度优先：目录先于其文件 visit；目录顺序为编码名字典序。
	wantOrder := []struct {
		path  string
		isDir bool
	}{
		{"/v2%2Fbeta__102", true},
		{"/v2%2Fbeta__102/app.zip", false},
		{"/v3__103", true},
		{"/v3__103/app-linux-amd64.tar.gz", false},
		{"/v3__103/SHA256SUMS", false},
	}
	for i, want := range wantOrder {
		if got[i].Path != want.path || got[i].IsDir != want.isDir {
			t.Errorf("scan[%d] = %s (dir=%v), want %s (dir=%v)",
				i, got[i].Path, got[i].IsDir, want.path, want.isDir)
		}
	}

	// 每个文件与 Stat 身份一致。
	for _, fi := range got {
		if fi.IsDir {
			continue
		}
		stat := fx.mustStat(t, fi.Path)
		if stat.Fingerprint.Version != fi.Fingerprint.Version ||
			stat.Fingerprint.Size != fi.Fingerprint.Size ||
			stat.Fingerprint.Checksum != fi.Fingerprint.Checksum {
			t.Errorf("identity mismatch for %s: scan %+v stat %+v", fi.Path, fi.Fingerprint, stat.Fingerprint)
		}
	}
}

// TestScanTreeVersionSubtree 验证 root=版本目录：只输出该版本的
// Asset 文件，不含其它版本。
func TestScanTreeVersionSubtree(t *testing.T) {
	fx := newRemoteFixture(t, source.ReleaseRecent)
	var paths []string
	err := fx.r.ScanTree(t.Context(), "/v3__103", func(fi source.FileInfo) error {
		paths = append(paths, fi.Path)
		return nil
	})
	if err != nil {
		t.Fatalf("ScanTree /v3__103: %v", err)
	}
	if len(paths) != 2 {
		t.Fatalf("entries = %v, want 2 files of v3", paths)
	}
	for _, p := range paths {
		if !strings.HasPrefix(p, "/v3__103/") {
			t.Errorf("entry %s escapes version dir", p)
		}
	}
}

// TestScanTreeFailsOnAssetPageError 任一 Asset 枚举失败 → 整体失败，
// 绝不返回部分结果。
func TestScanTreeFailsOnAssetPageError(t *testing.T) {
	f := newFakeGitHub(t, "", func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/gitea/gitea/releases":
			_, _ = w.Write([]byte("[" + joinJSON([]string{
				releaseJSON(103, "v3", "2026-01-03T00:00:00Z", false, false),
				releaseJSON(102, "v2", "2026-01-02T00:00:00Z", false, false),
			}) + "]"))
		case "/repos/gitea/gitea/releases/103/assets":
			_, _ = w.Write([]byte("[" + assetJSON(1001, "a.zip", 1, "", "uploaded") + "]"))
		case "/repos/gitea/gitea/releases/102/assets":
			http.Error(w, `{"message": "rate limited"}`, http.StatusTooManyRequests)
		default:
			http.NotFound(w, r)
		}
	})
	r := &remote{client: f.c, cfg: source.GitHubReleaseConfig{ReleasePolicy: source.ReleaseAll}}
	var visited int
	err := r.ScanTree(t.Context(), "/", func(fi source.FileInfo) error {
		visited++
		return nil
	})
	if err == nil {
		t.Fatal("ScanTree succeeded, want error on second release assets")
	}
	if !source.IsRetryable(err) {
		t.Errorf("429 should be retryable: %v", err)
	}
}

// TestScanTreeInvalidRoot 非法 root 拒绝。
func TestScanTreeInvalidRoot(t *testing.T) {
	fx := newRemoteFixture(t, source.ReleaseRecent)
	for _, root := range []string{"/../x", "/v3__103/a.zip/deep"} {
		err := fx.r.ScanTree(t.Context(), root, func(fi source.FileInfo) error { return nil })
		if err == nil {
			t.Errorf("ScanTree(%s) succeeded, want error", root)
		}
	}
}

// TestAssetNameValidation 非法 asset 名（反斜杠）在转换层 fail-fast，
// 不进入快照。
func TestAssetNameValidation(t *testing.T) {
	f := newFakeGitHub(t, "", func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/gitea/gitea/releases":
			_, _ = w.Write([]byte("[" + releaseJSON(103, "v3", "2026-01-03T00:00:00Z", false, false) + "]"))
		case "/repos/gitea/gitea/releases/103/assets":
			_, _ = w.Write([]byte("[" + assetJSON(1001, "bad\\name.zip", 1, "", "uploaded") + "]"))
		default:
			http.NotFound(w, r)
		}
	})
	r := &remote{client: f.c, cfg: source.GitHubReleaseConfig{ReleasePolicy: source.ReleaseAll}}
	err := r.ScanTree(t.Context(), "/", func(fi source.FileInfo) error { return nil })
	if err == nil {
		t.Fatal("ScanTree succeeded with backslash asset name, want error")
	}
}

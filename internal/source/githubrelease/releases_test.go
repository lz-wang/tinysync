package githubrelease

import (
	"errors"
	"net/http"
	"strconv"
	"testing"
	"time"

	"tinysync/internal/source"
)

// TestReleaseSelectable 校验入选条件：draft 一律排除、prerelease 按
// 开关、无 published_at 防御性排除。
func TestReleaseSelectable(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	cases := []struct {
		name       string
		r          Release
		prerelease bool
		want       bool
	}{
		{"stable", Release{PublishedAt: &now}, false, true},
		{"stable with prerelease flag on", Release{PublishedAt: &now}, true, true},
		{"draft", Release{Draft: true, PublishedAt: &now}, true, false},
		{"prerelease excluded", Release{Prerelease: true, PublishedAt: &now}, false, false},
		{"prerelease included", Release{Prerelease: true, PublishedAt: &now}, true, true},
		{"no published_at", Release{}, true, false},
	}
	for _, tc := range cases {
		if got := tc.r.selectable(tc.prerelease); got != tc.want {
			t.Errorf("%s = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestSelectReleasesLatest 验证 latest 策略：使用 latest 端点、不做
// 本地过滤（GitHub 保证结果非 prerelease 非 draft）。
func TestSelectReleasesLatest(t *testing.T) {
	f := newFakeGitHub(t, "", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/gitea/gitea/releases/latest" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(releaseJSON(7, "v1.2.0", "2026-01-07T00:00:00Z", false, false)))
	})
	got, err := f.c.selectReleases(t.Context(), source.GitHubReleaseConfig{ReleasePolicy: source.ReleaseLatest})
	if err != nil {
		t.Fatalf("selectReleases latest: %v", err)
	}
	if len(got) != 1 || got[0].ID != 7 || got[0].TagName != "v1.2.0" {
		t.Errorf("latest = %+v, want single release id 7", got)
	}
}

// TestSelectReleasesLatestEmpty 仓库无合格 Release 时 404 permanent。
func TestSelectReleasesLatestEmpty(t *testing.T) {
	f := newFakeGitHub(t, "", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"message": "Not Found"}`, http.StatusNotFound)
	})
	_, err := f.c.selectReleases(t.Context(), source.GitHubReleaseConfig{ReleasePolicy: source.ReleaseLatest})
	if err == nil {
		t.Fatal("want error for empty repository")
	}
	if source.IsRetryable(err) {
		t.Errorf("404 should be permanent: %v", err)
	}
}

// TestSelectReleasesByTag 验证 tag 策略：精确命中、特殊字符 tag 经
// path escape 进入 API path。
func TestSelectReleasesByTag(t *testing.T) {
	var gotPath string
	f := newFakeGitHub(t, "", func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.EscapedPath()
		if gotPath != "/repos/gitea/gitea/releases/tags/v1.0%2Fbeta" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(releaseJSON(9, "v1.0/beta", "2026-01-09T00:00:00Z", false, false)))
	})
	got, err := f.c.selectReleases(t.Context(), source.GitHubReleaseConfig{ReleasePolicy: source.ReleaseTag, Tag: "v1.0/beta"})
	if err != nil {
		t.Fatalf("selectReleases by tag: %v", err)
	}
	if gotPath != "/repos/gitea/gitea/releases/tags/v1.0%2Fbeta" {
		t.Errorf("request path = %s, want escaped tag", gotPath)
	}
	if len(got) != 1 || got[0].TagName != "v1.0/beta" {
		t.Errorf("by tag = %+v, want v1.0/beta", got)
	}
}

// TestSelectReleasesByTagMissing 指定 Tag 无 Release 时 404 permanent。
func TestSelectReleasesByTagMissing(t *testing.T) {
	f := newFakeGitHub(t, "", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"message": "Not Found"}`, http.StatusNotFound)
	})
	_, err := f.c.selectReleases(t.Context(), source.GitHubReleaseConfig{ReleasePolicy: source.ReleaseTag, Tag: "nope"})
	if err == nil {
		t.Fatal("want error for missing tag")
	}
	if source.IsRetryable(err) {
		t.Errorf("missing tag should be permanent: %v", err)
	}
}

// TestSelectReleasesRecent 验证 recent 策略：完整枚举后按 published_at
// 降序排序取前 N、draft 一律排除、prerelease 按开关（不依赖 API 返回
// 顺序）。
func TestSelectReleasesRecent(t *testing.T) {
	// 服务端按 created 乱序返回：id 越大发布越早，并混入 draft 与
	// prerelease。
	pages := [][]string{
		{
			releaseJSON(1, "v1", "2026-01-01T00:00:00Z", false, false),
			releaseJSON(2, "v2-draft", "2026-01-08T00:00:00Z", true, false),
			releaseJSON(3, "v3-rc", "2026-01-07T00:00:00Z", false, true),
			releaseJSON(4, "v4", "2026-01-04T00:00:00Z", false, false),
		},
		{
			releaseJSON(5, "v5", "2026-01-03T00:00:00Z", false, false),
			releaseJSON(6, "v6", "2026-01-02T00:00:00Z", false, false),
			releaseJSON(7, "v7", "2026-01-06T00:00:00Z", false, false),
		},
	}
	f := newFakeGitHub(t, "", pagedHandler(pages))

	// 排除 prerelease：入选 5 个稳定版（v5,v6,v4,v7,v1 → 按时间降序
	// v7,v4,v5,v6,v1），recent=3 取前 3。
	got, err := f.c.selectReleases(t.Context(), source.GitHubReleaseConfig{
		ReleasePolicy: source.ReleaseRecent, RecentCount: 3,
	})
	if err != nil {
		t.Fatalf("selectReleases recent: %v", err)
	}
	wantIDs := []int64{7, 4, 5}
	if len(got) != len(wantIDs) {
		t.Fatalf("got %d releases (%+v), want %d", len(got), got, len(wantIDs))
	}
	for i, want := range wantIDs {
		if got[i].ID != want {
			t.Errorf("recent[%d].ID = %d, want %d", i, got[i].ID, want)
		}
	}

	// 包含 prerelease：v3-rc（01-07）按时间降序排最前，recent=3 取
	// v3-rc、v7（01-06）、v4（01-04）。
	got, err = f.c.selectReleases(t.Context(), source.GitHubReleaseConfig{
		ReleasePolicy: source.ReleaseRecent, RecentCount: 3, IncludePrereleases: true,
	})
	if err != nil {
		t.Fatalf("selectReleases recent with prereleases: %v", err)
	}
	wantIDs = []int64{3, 7, 4}
	if len(got) != len(wantIDs) {
		t.Fatalf("got %d releases (%+v), want %d", len(got), got, len(wantIDs))
	}
	for i, want := range wantIDs {
		if got[i].ID != want {
			t.Errorf("recent+pre[%d].ID = %d, want %d", i, got[i].ID, want)
		}
	}
}

// TestSelectReleasesAll 验证 all 策略：全部合格版本按时间降序。
func TestSelectReleasesAll(t *testing.T) {
	pages := [][]string{
		{releaseJSON(1, "v1", "2026-01-01T00:00:00Z", false, false)},
		{releaseJSON(2, "v2", "2026-01-02T00:00:00Z", false, false)},
	}
	f := newFakeGitHub(t, "", pagedHandler(pages))
	got, err := f.c.selectReleases(t.Context(), source.GitHubReleaseConfig{
		ReleasePolicy: source.ReleaseAll, IncludePrereleases: true,
	})
	if err != nil {
		t.Fatalf("selectReleases all: %v", err)
	}
	if len(got) != 2 || got[0].ID != 2 || got[1].ID != 1 {
		t.Errorf("all = %+v, want [2 1] by published_at desc", got)
	}
}

// TestSelectReleasesOverLimit 验证扫描规模上限：超过 1000 个 Release
// 整体失败，绝不截断（验收：不允许截断后进入 Mirror）。
func TestSelectReleasesOverLimit(t *testing.T) {
	// 构造 1001 个合格 Release。
	releases := make([]string, 0, maxReleases+1)
	for i := int64(1); i <= int64(maxReleases+1); i++ {
		releases = append(releases, releaseJSON(i, "v"+strconv.FormatInt(i, 10), "2026-01-01T00:00:00Z", false, false))
	}
	// published_at 相同也无所谓：上限检查发生在过滤前（按原始枚举数）。
	f := newFakeGitHub(t, "", pagedHandler([][]string{releases}))
	got, err := f.c.selectReleases(t.Context(), source.GitHubReleaseConfig{ReleasePolicy: source.ReleaseAll})
	if err == nil {
		t.Fatalf("selectReleases returned %d releases, want over-limit error", len(got))
	}
}

// TestSelectReleasesUnsupportedPolicy 非法策略在 adapter 边界拒绝。
func TestSelectReleasesUnsupportedPolicy(t *testing.T) {
	f := newFakeGitHub(t, "", func(w http.ResponseWriter, r *http.Request) {
		t.Error("unexpected request")
	})
	if _, err := f.c.selectReleases(t.Context(), source.GitHubReleaseConfig{ReleasePolicy: "weekly"}); err == nil {
		t.Fatal("want error for unsupported policy")
	}
}

// TestListAssetsFiltersAndConflicts 验证 Asset 枚举：仅保留 uploaded、
// 同名大小写折叠冲突整体失败。
func TestListAssetsFiltersAndConflicts(t *testing.T) {
	f := newFakeGitHub(t, "", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/gitea/gitea/releases/123/assets" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("[" + joinJSON([]string{
			assetJSON(1, "app-linux.tar.gz", 100, "sha256:aaaa", "uploaded"),
			assetJSON(2, "app-draft.zip", 10, "", "uploading"),
			assetJSON(3, "SHA256SUMS", 64, "", "uploaded"),
		}) + "]"))
	})
	got, err := f.c.listAssets(t.Context(), 123)
	if err != nil {
		t.Fatalf("listAssets: %v", err)
	}
	if len(got) != 2 || got[0].Name != "app-linux.tar.gz" || got[1].Name != "SHA256SUMS" {
		t.Errorf("assets = %+v, want uploaded-only entries", got)
	}
}

// TestListAssetsCaseConflict 同一 Release 内 asset name 大小写折叠
// 冲突时整体失败（本地大小写不敏感文件系统的路径歧义防御）。
func TestListAssetsCaseConflict(t *testing.T) {
	f := newFakeGitHub(t, "", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("[" + joinJSON([]string{
			assetJSON(1, "App.tar.gz", 100, "", "uploaded"),
			assetJSON(2, "app.TAR.GZ", 200, "", "uploaded"),
		}) + "]"))
	})
	got, err := f.c.listAssets(t.Context(), 123)
	if err == nil {
		t.Fatalf("listAssets = %+v, want case-conflict error", got)
	}
	if !errors.Is(err, source.ErrInvalid) {
		t.Errorf("case conflict should be ErrInvalid-marked: %v", err)
	}
}

// pagedHandler 构造按 page=N 分页的 releases 列表 handler（每页固定
// 2 条由 pages 内容决定，页码超出即 404 终止）。
func pagedHandler(pages [][]string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/gitea/gitea/releases" {
			http.NotFound(w, r)
			return
		}
		page := 1
		if p := r.URL.Query().Get("page"); p != "" {
			page, _ = strconv.Atoi(p)
		}
		if page < 1 || page > len(pages) {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if page < len(pages) {
			w.Header().Set("Link", nextLinkHeader(r, page+1))
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("[" + joinJSON(pages[page-1]) + "]"))
	}
}

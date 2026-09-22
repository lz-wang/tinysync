package githubrelease

import (
	"net/http"
	"testing"

	"tinysync/internal/source"
)

// TestInspectPreview 展开明细：full_name 回显、Asset 明细与 digest
// 可用性标记。
func TestInspectPreview(t *testing.T) {
	f := newFakeGitHub(t, "", func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/gitea/gitea":
			_, _ = w.Write([]byte(`{"full_name": "gitea/gitea"}`))
		case "/repos/gitea/gitea/releases":
			_, _ = w.Write([]byte("[" + joinJSON([]string{
				releaseJSON(2, "v2", "2026-01-02T00:00:00Z", false, false),
				releaseJSON(1, "v1", "2026-01-01T00:00:00Z", false, false),
			}) + "]"))
		case "/repos/gitea/gitea/releases/2/assets":
			_, _ = w.Write([]byte("[" + joinJSON([]string{
				assetJSON(11, "a.zip", 10, "sha256:beef", "uploaded"),
				assetJSON(12, "b.sig", 1, "", "uploaded"),
			}) + "]"))
		case "/repos/gitea/gitea/releases/1/assets":
			_, _ = w.Write([]byte("[" + assetJSON(13, "old.zip", 5, "", "uploaded") + "]"))
		default:
			http.NotFound(w, r)
		}
	})
	r := &remote{client: f.c, cfg: source.GitHubReleaseConfig{ReleasePolicy: source.ReleaseAll}}
	out, err := r.Inspect(t.Context(), source.InspectAssetDetailLimit)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if out.Repository != "gitea/gitea" {
		t.Errorf("Repository = %q", out.Repository)
	}
	if len(out.Releases) != 2 {
		t.Fatalf("Releases = %d, want 2", len(out.Releases))
	}
	// 顺序与选中策略一致（published_at 降序）。
	if out.Releases[0].Tag != "v2" || out.Releases[1].Tag != "v1" {
		t.Errorf("release order = %s, %s; want v2, v1", out.Releases[0].Tag, out.Releases[1].Tag)
	}
	assets := out.Releases[0].Assets
	if len(assets) != 2 || !assets[0].DigestAvailable || assets[1].DigestAvailable {
		t.Errorf("assets = %+v, want digest flag only on first", assets)
	}
	if out.Releases[1].Assets == nil || len(out.Releases[1].Assets) != 1 {
		t.Errorf("v1 assets = %+v, want expanded", out.Releases[1].Assets)
	}
}

// TestInspectDetailLimitDetailZero assetDetail=0 展开全部版本明细。
func TestInspectDetailLimitDetailZero(t *testing.T) {
	f := newFakeGitHub(t, "", func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/gitea/gitea":
			_, _ = w.Write([]byte(`{"full_name": "gitea/gitea"}`))
		case "/repos/gitea/gitea/releases":
			_, _ = w.Write([]byte("[" + joinJSON([]string{
				releaseJSON(2, "v2", "2026-01-02T00:00:00Z", false, false),
				releaseJSON(1, "v1", "2026-01-01T00:00:00Z", false, false),
			}) + "]"))
		case "/repos/gitea/gitea/releases/2/assets", "/repos/gitea/gitea/releases/1/assets":
			_, _ = w.Write([]byte("[" + assetJSON(11, "a.zip", 10, "", "uploaded") + "]"))
		default:
			http.NotFound(w, r)
		}
	})
	r := &remote{client: f.c, cfg: source.GitHubReleaseConfig{ReleasePolicy: source.ReleaseAll}}
	out, err := r.Inspect(t.Context(), 0)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	for _, rel := range out.Releases {
		if rel.Assets == nil {
			t.Errorf("release %s assets not expanded with detail=0", rel.Tag)
		}
	}
}

// TestInspectDetailLimitTruncates assetDetail 限制展开数量：更早版本
// 只有版本级概览（Assets 为 nil）。
func TestInspectDetailLimitTruncates(t *testing.T) {
	f := newFakeGitHub(t, "", func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/gitea/gitea":
			_, _ = w.Write([]byte(`{"full_name": "gitea/gitea"}`))
		case "/repos/gitea/gitea/releases":
			_, _ = w.Write([]byte("[" + joinJSON([]string{
				releaseJSON(3, "v3", "2026-01-03T00:00:00Z", false, false),
				releaseJSON(2, "v2", "2026-01-02T00:00:00Z", false, false),
				releaseJSON(1, "v1", "2026-01-01T00:00:00Z", false, false),
			}) + "]"))
		case "/repos/gitea/gitea/releases/3/assets", "/repos/gitea/gitea/releases/2/assets":
			_, _ = w.Write([]byte("[" + assetJSON(11, "a.zip", 10, "", "uploaded") + "]"))
		default:
			http.NotFound(w, r)
		}
	})
	r := &remote{client: f.c, cfg: source.GitHubReleaseConfig{ReleasePolicy: source.ReleaseAll}}
	out, err := r.Inspect(t.Context(), 2)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if out.Releases[0].Assets == nil || out.Releases[1].Assets == nil {
		t.Error("first two releases should be expanded")
	}
	if out.Releases[2].Assets != nil {
		t.Errorf("oldest release expanded beyond detail limit: %+v", out.Releases[2].Assets)
	}
}

// TestInspectFailsOnAssetError Asset 明细枚举失败 → 预览整体失败，
// 不产出部分结果。
func TestInspectFailsOnAssetError(t *testing.T) {
	f := newFakeGitHub(t, "", func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/gitea/gitea":
			_, _ = w.Write([]byte(`{"full_name": "gitea/gitea"}`))
		case "/repos/gitea/gitea/releases":
			_, _ = w.Write([]byte("[" + releaseJSON(1, "v1", "2026-01-01T00:00:00Z", false, false) + "]"))
		case "/repos/gitea/gitea/releases/1/assets":
			http.Error(w, `{"message": "boom"}`, http.StatusInternalServerError)
		default:
			http.NotFound(w, r)
		}
	})
	r := &remote{client: f.c, cfg: source.GitHubReleaseConfig{ReleasePolicy: source.ReleaseAll}}
	if _, err := r.Inspect(t.Context(), 1); err == nil {
		t.Fatal("Inspect succeeded with asset failure, want error")
	}
}

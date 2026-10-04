package githubrelease

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"tinysync/internal/source"
)

// resumeFixture 假 GitHub：asset 1001 的下载端点经 http.ServeContent
// 服务（标准 Range / 206 语义），asset 1002 忽略 Range 返回 200
// （降级路径），asset 1004 返回 416。
type resumeFixture struct {
	r *remote
}

func newResumeFixture(t *testing.T) *resumeFixture {
	t.Helper()
	assetContent := strings.Repeat("0123456789", 10) // 100 bytes，与 asset JSON 声明一致
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
			}) + "]"))
		case r.URL.Path == "/repos/gitea/gitea/releases/103/assets":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte("[" + joinJSON([]string{
				assetJSON(1001, "app.tar.gz", int64(len(assetContent)), "sha256:aaaa", "uploaded"),
				assetJSON(1002, "legacy.zip", 80, "", "uploaded"),
				assetJSON(1004, "small.zip", 10, "", "uploaded"),
			}) + "]"))
		case r.URL.Path == "/repos/gitea/gitea/releases/assets/1001":
			http.ServeContent(w, r, "app.tar.gz", time.Unix(1700000000, 0), strings.NewReader(assetContent))
		case r.URL.Path == "/repos/gitea/gitea/releases/assets/1002":
			// 模拟忽略 Range 的服务：剥头返回 200 全量。
			r2 := r.Clone(r.Context())
			r2.Header.Del("Range")
			http.ServeContent(w, r2, "legacy.zip", time.Unix(1700000000, 0), strings.NewReader(strings.Repeat("z", 80)))
		case r.URL.Path == "/repos/gitea/gitea/releases/assets/1004":
			http.Error(w, "range not satisfiable", http.StatusRequestedRangeNotSatisfiable)
		default:
			http.NotFound(w, r)
		}
	})
	return &resumeFixture{r: &remote{
		client: f.c,
		cfg:    source.GitHubReleaseConfig{ReleasePolicy: source.ReleaseRecent, RecentCount: 2},
	}}
}

// remote.OpenFrom 全链路：定位 asset → Range 请求 → 206 后缀正确，
// 指纹取自 Stat（快照身份）。
func TestRemoteOpenFromResume(t *testing.T) {
	fx := newResumeFixture(t)
	fi, err := fx.r.Stat(t.Context(), "/v3__103/app.tar.gz")
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	rc, err := fx.r.OpenFrom(t.Context(), "/v3__103/app.tar.gz", 30, fi.Fingerprint)
	if err != nil {
		t.Fatalf("OpenFrom(30): %v", err)
	}
	body, err := io.ReadAll(rc)
	_ = rc.Close()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(body) != 70 {
		t.Fatalf("suffix = %d bytes, want 70", len(body))
	}
	if !strings.HasPrefix(string(body), "0123456789") {
		t.Fatalf("suffix content = %q, want content[30:]", string(body)[:10])
	}
}

// offset=0 复用无 Range 的 openAsset（200 全量）。
func TestRemoteOpenFromZeroOffset(t *testing.T) {
	fx := newResumeFixture(t)
	fi, err := fx.r.Stat(t.Context(), "/v3__103/app.tar.gz")
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	rc, err := fx.r.OpenFrom(t.Context(), "/v3__103/app.tar.gz", 0, fi.Fingerprint)
	if err != nil {
		t.Fatalf("OpenFrom(0): %v", err)
	}
	body, _ := io.ReadAll(rc)
	_ = rc.Close()
	if len(body) != 100 {
		t.Fatalf("full read = %d bytes, want 100", len(body))
	}
}

// 服务器忽略 Range（200）：降级 ErrResumeUnsupported。
func TestRemoteOpenFromRangeIgnored(t *testing.T) {
	fx := newResumeFixture(t)
	fi, err := fx.r.Stat(t.Context(), "/v3__103/legacy.zip")
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	_, err = fx.r.OpenFrom(t.Context(), "/v3__103/legacy.zip", 10, fi.Fingerprint)
	if !errors.Is(err, source.ErrResumeUnsupported) {
		t.Fatalf("OpenFrom with ignored Range = %v, want ErrResumeUnsupported", err)
	}
}

// 416：offset 越过资源末尾 → ErrRemoteChanged。
func TestRemoteOpenFromRangeNotSatisfiable(t *testing.T) {
	fx := newResumeFixture(t)
	fi, err := fx.r.Stat(t.Context(), "/v3__103/small.zip")
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	_, err = fx.r.OpenFrom(t.Context(), "/v3__103/small.zip", 50, fi.Fingerprint)
	if !errors.Is(err, source.ErrRemoteChanged) {
		t.Fatalf("OpenFrom beyond end = %v, want ErrRemoteChanged", err)
	}
}

// 入口校验：负 offset、目录路径、非文件。
func TestRemoteOpenFromInvalidInput(t *testing.T) {
	fx := newResumeFixture(t)
	if _, err := fx.r.OpenFrom(context.Background(), "/v3__103/app.tar.gz", -1, source.Fingerprint{}); !errors.Is(err, source.ErrInvalid) {
		t.Fatalf("OpenFrom(-1) = %v, want ErrInvalid", err)
	}
	for _, p := range []string{"/", "/v3__103", "/missing__999/app.zip"} {
		if _, err := fx.r.OpenFrom(context.Background(), p, 0, source.Fingerprint{}); err == nil {
			t.Errorf("OpenFrom(%s) = nil, want error", p)
		}
	}
}

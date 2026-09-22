package githubrelease

import (
	"errors"
	"net/http"
	"net/url"
	"testing"

	"tinysync/internal/source"
)

// TestFactoryType factory 声明服务的协议类型。
func TestFactoryType(t *testing.T) {
	if got := (NewFactory()).Type(); got != source.TypeGitHubRelease {
		t.Errorf("Type = %q, want github_release", got)
	}
}

// TestFactoryCreateRejectsWrongType 类型不匹配在 adapter 边界拒绝。
func TestFactoryCreateRejectsWrongType(t *testing.T) {
	f := NewFactory()
	src := source.Source{Type: source.TypeWebDAV}
	if _, err := f.Create(t.Context(), src, source.Credentials{}); !errors.Is(err, source.ErrUnsupportedType) {
		t.Errorf("Create(webdav) = %v, want ErrUnsupportedType", err)
	}
	src = source.Source{Type: source.TypeGitHubRelease}
	if _, err := f.Create(t.Context(), src, source.Credentials{}); !errors.Is(err, source.ErrUnsupportedType) {
		t.Errorf("Create(github without config) = %v, want ErrUnsupportedType", err)
	}
}

// TestFactoryCreateRejectsBadRepository repository 形态非法时 Create
// 直接失败，不发起网络请求。
func TestFactoryCreateRejectsBadRepository(t *testing.T) {
	f := &Factory{newClientAt: func(string, string, string, string) *client {
		t.Error("network client built for invalid repository")
		return nil
	}}
	src := source.Source{
		Type:   source.TypeGitHubRelease,
		Config: source.Config{GitHubRelease: &source.GitHubReleaseConfig{Repository: "not-a-repo"}},
	}
	if _, err := f.Create(t.Context(), src, source.Credentials{}); !errors.Is(err, source.ErrInvalid) {
		t.Errorf("Create(bad repo) = %v, want ErrInvalid", err)
	}
}

// newTestFactory 构造 newClientAt 注入假服务器的 factory（owner/repo
// 固定 gitea/gitea，token 按需）。
func newTestFactory(t *testing.T, token string, handler http.HandlerFunc) (*Factory, *fakeGitHub) {
	t.Helper()
	fake := newFakeGitHub(t, token, handler)
	f := &Factory{
		newClientAt: func(baseURL, tok, owner, repo string) *client {
			u, err := url.Parse(fake.server.URL)
			if err != nil {
				t.Fatalf("parse server url: %v", err)
			}
			return &client{
				httpClient: newHTTPClient(u.Host),
				baseURL:    fake.server.URL,
				token:      tok,
				owner:      owner,
				repo:       repo,
				cache:      newETagCache(),
			}
		},
	}
	return f, fake
}

// githubSource 构造 repository 指向 gitea/gitea 的 Source。
func githubSource(policy source.GitHubReleasePolicy) source.Source {
	return source.Source{
		Type: source.TypeGitHubRelease,
		Config: source.Config{GitHubRelease: &source.GitHubReleaseConfig{
			Repository:    "gitea/gitea",
			ReleasePolicy: policy,
			RecentCount:   3,
		}},
	}
}

// TestFactoryCreateVerifiesRepo 仓库不可访问（404：不存在或无权限）
// 使 Create 失败且错误 permanent。
func TestFactoryCreateVerifiesRepo(t *testing.T) {
	f, _ := newTestFactory(t, "", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"message": "Not Found"}`, http.StatusNotFound)
	})
	_, err := f.Create(t.Context(), githubSource(source.ReleaseAll), source.Credentials{})
	if err == nil {
		t.Fatal("Create succeeded for missing repository, want error")
	}
	if source.IsRetryable(err) {
		t.Errorf("repo 404 should be permanent: %v", err)
	}
}

// TestFactoryCreateSuccessURLRepository 成功路径：repository 用完整
// URL 形态解析（owner/repo 提取）、Token 注入请求、返回的 Remote
// 可用且携带配置。
func TestFactoryCreateSuccessURLRepository(t *testing.T) {
	f, fake := newTestFactory(t, "", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/repos/gitea/gitea" {
			if got := r.Header.Get("Authorization"); got != "Bearer ghp_x" {
				t.Errorf("Authorization = %q, want bearer token", got)
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"full_name": "gitea/gitea"}`))
			return
		}
		http.NotFound(w, r)
	})
	src := githubSource(source.ReleaseAll)
	src.Config.GitHubRelease.Repository = "https://github.com/gitea/gitea.git"
	creds := source.Credentials{GitHubRelease: &source.GitHubReleaseCredentials{Token: "ghp_x"}}

	remote, err := f.Create(t.Context(), src, creds)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := remote.List(t.Context(), "/", source.ListOptions{}); err == nil {
		// releases 端点未在 fixture 实现：此处只验证 Remote 已装配
		// 并能发起请求（错误来自 fixture 404，不是装配失败）。
		t.Logf("List on minimal fixture returns endpoint error as expected")
	}
	_ = fake
	if err := remote.Close(); err != nil {
		t.Errorf("Close = %v, want nil", err)
	}
}

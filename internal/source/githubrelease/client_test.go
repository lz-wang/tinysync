package githubrelease

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"sync"
	"testing"

	"tinysync/internal/source"
)

// fakeGitHub 是 httptest 实现的假 GitHub API：handler 按 path 分发，
// 记录全部请求 path 供分页断言。
type fakeGitHub struct {
	server   *httptest.Server
	c        *client
	mu       sync.Mutex
	requests []string
}

// newFakeGitHub 启动假服务器并构造指向它的 client（owner/repo 固定
// gitea/gitea，token 按需）。
func newFakeGitHub(t *testing.T, token string, handler http.HandlerFunc) *fakeGitHub {
	t.Helper()
	f := &fakeGitHub{}
	recording := func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.requests = append(f.requests, r.URL.Path+"?"+r.URL.RawQuery)
		f.mu.Unlock()
		handler(w, r)
	}
	f.server = httptest.NewServer(http.HandlerFunc(recording))
	t.Cleanup(f.server.Close)
	u, err := url.Parse(f.server.URL)
	if err != nil {
		t.Fatalf("parse server url: %v", err)
	}
	f.c = &client{
		httpClient: newHTTPClient(u.Host),
		baseURL:    f.server.URL,
		token:      token,
		owner:      "gitea",
		repo:       "gitea",
		cache:      newETagCache(),
	}
	return f
}

// requestCount 返回累计请求数。
func (f *fakeGitHub) requestCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.requests)
}

// releaseJSON 构造一条 Release 的 JSON 对象。
func releaseJSON(id int64, tag, published string, draft, prerelease bool) string {
	publishedJSON := "null"
	if published != "" {
		publishedJSON = strconv.Quote(published)
	}
	return fmt.Sprintf(`{"id": %d, "tag_name": %q, "name": %q, "draft": %t, "prerelease": %t, "published_at": %s}`,
		id, tag, tag, draft, prerelease, publishedJSON)
}

// assetJSON 构造一条 Asset 的 JSON 对象。
func assetJSON(id int64, name string, size int64, digest, state string) string {
	digestJSON := `""`
	if digest != "" {
		digestJSON = strconv.Quote(digest)
	}
	return fmt.Sprintf(`{"id": %d, "name": %q, "size": %d, "updated_at": "2026-01-01T00:00:00Z", "digest": %s, "state": %q}`,
		id, name, size, digestJSON, state)
}

// nextLinkHeader 用请求自身的 host 构造下一页的完整 URL（httptest
// server 是 http，r.Host 即 host:port），避免闭包依赖 server 实例。
func nextLinkHeader(r *http.Request, page int) string {
	return fmt.Sprintf("<http://%s%s?page=%d>; rel=\"next\"", r.Host, r.URL.Path, page)
}

// joinJSON 以逗号拼接 JSON 对象片段。
func joinJSON(items []string) string {
	out := ""
	for i, item := range items {
		if i > 0 {
			out += ","
		}
		out += item
	}
	return out
}

// TestListAllPagination 验证 Link 分页完整枚举：逐页续拉、不漏条目
// （验收：超过 100 条 Release 时完整分页）。
func TestListAllPagination(t *testing.T) {
	pages := [][]string{
		{releaseJSON(1, "v1", "2026-01-01T00:00:00Z", false, false), releaseJSON(2, "v2", "2026-01-02T00:00:00Z", false, false)},
		{releaseJSON(3, "v3", "2026-01-03T00:00:00Z", false, false)},
		{releaseJSON(4, "v4", "2026-01-04T00:00:00Z", false, false), releaseJSON(5, "v5", "2026-01-05T00:00:00Z", false, false)},
	}
	f := newFakeGitHub(t, "", func(w http.ResponseWriter, r *http.Request) {
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
	})

	got, err := listAllReleases(t.Context(), f.c)
	if err != nil {
		t.Fatalf("listAll: %v", err)
	}
	if len(got) != 5 {
		t.Fatalf("got %d releases, want 5", len(got))
	}
	if f.requestCount() != 3 {
		t.Errorf("requests = %d, want 3 pages", f.requestCount())
	}
	for i, want := range []int64{1, 2, 3, 4, 5} {
		if got[i].ID != want {
			t.Errorf("releases[%d].ID = %d, want %d", i, got[i].ID, want)
		}
	}
}

// TestListAllStopsOnEOF 验证无 Link header 时单页结束，不产生多余请求。
func TestListAllStopsOnEOF(t *testing.T) {
	f := newFakeGitHub(t, "", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("[" + releaseJSON(1, "v1", "2026-01-01T00:00:00Z", false, false) + "]"))
	})
	got, err := listAllReleases(t.Context(), f.c)
	if err != nil {
		t.Fatalf("listAll: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d releases, want 1", len(got))
	}
	if f.requestCount() != 1 {
		t.Errorf("requests = %d, want 1", f.requestCount())
	}
}

// TestListAllFailsOnPageError 验证任何一页失败都整体失败并返回 nil，
// 绝不返回部分结果（验收：分页失败不触发 Mirror 删除）。
func TestListAllFailsOnPageError(t *testing.T) {
	f := newFakeGitHub(t, "", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("page") == "" {
			w.Header().Set("Link", nextLinkHeader(r, 2))
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("[" + releaseJSON(1, "v1", "2026-01-01T00:00:00Z", false, false) + "]"))
			return
		}
		http.Error(w, `{"message": "boom"}`, http.StatusInternalServerError)
	})
	got, err := listAllReleases(t.Context(), f.c)
	if err == nil {
		t.Fatal("listAll succeeded, want error on second page")
	}
	if !source.IsRetryable(err) {
		t.Errorf("error %v should be retryable (5xx)", err)
	}
	if got != nil {
		t.Errorf("partial result %v leaked on page failure", got)
	}
}

// TestNextLinkEscapesOrigin 验证 next link 指向未知主机时整体失败，
// 不把后续请求打到服务器回传的任意 URL。
func TestNextLinkEscapesOrigin(t *testing.T) {
	f := newFakeGitHub(t, "", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Link", `<https://evil.example.com/repos/gitea/gitea/releases?page=2>; rel="next"`)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("[]"))
	})
	got, err := listAllReleases(t.Context(), f.c)
	if err == nil {
		t.Fatal("listAll accepted cross-origin next link, want error")
	}
	if got != nil {
		t.Errorf("partial result %v leaked", got)
	}
}

// TestETagConditionalRequest 验证条件请求缓存：第二次请求携带
// If-None-Match，304 命中复用缓存内容。
func TestETagConditionalRequest(t *testing.T) {
	var seenIfNoneMatch []string
	f := newFakeGitHub(t, "", func(w http.ResponseWriter, r *http.Request) {
		if inm := r.Header.Get("If-None-Match"); inm != "" {
			seenIfNoneMatch = append(seenIfNoneMatch, inm)
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", `"abc123"`)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"full_name": "gitea/gitea"}`))
	})
	var first, second struct {
		FullName string `json:"full_name"`
	}
	if _, err := f.c.getJSON(t.Context(), f.c.apiPath(""), &first); err != nil {
		t.Fatalf("first getJSON: %v", err)
	}
	if _, err := f.c.getJSON(t.Context(), f.c.apiPath(""), &second); err != nil {
		t.Fatalf("second getJSON: %v", err)
	}
	if len(seenIfNoneMatch) != 1 || seenIfNoneMatch[0] != `"abc123"` {
		t.Errorf("If-None-Match seen = %v, want one request with %q", seenIfNoneMatch, `"abc123"`)
	}
	if first.FullName != second.FullName {
		t.Errorf("cached decode mismatch: first %+v second %+v", first, second)
	}
}

// TestClassifyStatusViaClient 验证状态码分类经过真实 HTTP 路径生效
// （adapter boundary 契约：403/429 限流可重试，其余 4xx permanent）。
func TestClassifyStatusViaClient(t *testing.T) {
	cases := []struct {
		name      string
		status    int
		headers   map[string]string
		retryable bool
	}{
		{"401 permanent", http.StatusUnauthorized, nil, false},
		{"404 permanent", http.StatusNotFound, nil, false},
		{"403 permission permanent", http.StatusForbidden, nil, false},
		{"403 rate limited transient", http.StatusForbidden, map[string]string{"X-RateLimit-Remaining": "0"}, true},
		{"403 retry-after transient", http.StatusForbidden, map[string]string{"Retry-After": "60"}, true},
		{"429 transient", http.StatusTooManyRequests, nil, true},
		{"500 transient", http.StatusInternalServerError, nil, true},
		{"503 transient", http.StatusServiceUnavailable, nil, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeGitHub(t, "", func(w http.ResponseWriter, r *http.Request) {
				for k, v := range tc.headers {
					w.Header().Set(k, v)
				}
				http.Error(w, `{"message": "x"}`, tc.status)
			})
			var out []Release
			if _, err := f.c.getJSON(t.Context(), f.c.apiPath("/releases?per_page=100"), &out); err == nil {
				t.Fatal("want error")
			} else if got := source.IsRetryable(err); got != tc.retryable {
				t.Errorf("IsRetryable = %v, want %v (err = %v)", got, tc.retryable, err)
			}
		})
	}
}

// TestTokenSentOnlyToAPIHost 验证 Token 注入 API 请求头；下载重定向
// 的剥除行为由 TestRedirectPolicy 覆盖。
func TestTokenSentOnlyToAPIHost(t *testing.T) {
	f := newFakeGitHub(t, "ghp_secret", func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer ghp_secret" {
			t.Errorf("Authorization = %q, want bearer token", got)
		}
		if got := r.Header.Get("Accept"); got != "application/vnd.github+json" {
			t.Errorf("Accept = %q", got)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"full_name": "gitea/gitea"}`))
	})
	if _, err := f.c.verifyRepo(t.Context()); err != nil {
		t.Fatalf("verifyRepo: %v", err)
	}
}

// TestAnonymousNoAuthHeader 匿名访问不发送 Authorization。
func TestAnonymousNoAuthHeader(t *testing.T) {
	f := newFakeGitHub(t, "", func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "" {
			t.Errorf("Authorization = %q, want empty for anonymous", got)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"full_name": "gitea/gitea"}`))
	})
	if _, err := f.c.verifyRepo(t.Context()); err != nil {
		t.Fatalf("verifyRepo: %v", err)
	}
}

// TestVerifyRepoErrorClass 仓库不可访问（404：不存在或无权限）经
// 分类为 permanent 透出。
func TestVerifyRepoErrorClass(t *testing.T) {
	f := newFakeGitHub(t, "", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"message": "Not Found"}`, http.StatusNotFound)
	})
	if _, err := f.c.verifyRepo(t.Context()); err == nil {
		t.Fatal("verifyRepo succeeded, want error")
	} else if source.IsRetryable(err) {
		t.Errorf("404 should be permanent, got retryable: %v", err)
	}
}

// TestRedirectPolicy 直接断言重定向安全策略：仅 https、目标 host
// 白名单、跨 host 剥 Authorization（验收：302 下载重定向不泄露 Token）。
func TestRedirectPolicy(t *testing.T) {
	policy := redirectPolicy("api.github.com")
	newReq := func(rawURL string, auth string) *http.Request {
		u, err := url.Parse(rawURL)
		if err != nil {
			t.Fatalf("parse %s: %v", rawURL, err)
		}
		req := &http.Request{URL: u, Header: http.Header{}}
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		return req
	}
	via := []*http.Request{newReq("https://api.github.com/repos/gitea/gitea/releases/assets/1", "")}

	cases := []struct {
		name    string
		target  string
		auth    string
		wantErr bool
	}{
		{"cdn allowed", "https://objects.githubusercontent.com/x/y?z=1", "Bearer ghp_secret", false},
		{"bare domain allowed", "https://github.com/x", "", false},
		{"subdomain suffix allowed", "https://release-assets.githubusercontent.com/x", "Bearer ghp_secret", false},
		{"evil suffix rejected", "https://evil-github.com/x", "Bearer ghp_secret", true},
		{"unknown host rejected", "https://example.com/x", "Bearer ghp_secret", true},
		{"http downgrade rejected", "http://objects.githubusercontent.com/x", "Bearer ghp_secret", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := newReq(tc.target, tc.auth)
			err := policy(req, via)
			if (err != nil) != tc.wantErr {
				t.Errorf("redirectPolicy(%s) err = %v, wantErr %v", tc.target, err, tc.wantErr)
			}
			if !tc.wantErr && req.URL.Host != via[0].URL.Host && req.Header.Get("Authorization") != "" {
				t.Error("cross-host redirect should strip Authorization")
			}
		})
	}
	// 同 host 重定向保留 Authorization。
	same := newReq("https://api.github.com/other", "Bearer ghp_secret")
	if err := policy(same, via); err != nil {
		t.Errorf("same-host redirect: %v", err)
	}
	if same.Header.Get("Authorization") != "Bearer ghp_secret" {
		t.Error("same-host redirect should keep Authorization")
	}
	// 重定向链上限。
	chain := []*http.Request{via[0], via[0], via[0], via[0], via[0]}
	if err := policy(newReq("https://github.com/x", ""), chain); err == nil {
		t.Error("expected redirect limit error")
	}
}

// listAllReleases 是 releases 列表分页的测试辅助（等价于 releases.go
// 内部对 listAll 的调用形态）。
func listAllReleases(ctx context.Context, c *client) ([]Release, error) {
	return listAll[Release](ctx, c, c.apiPath("/releases?per_page=100"))
}

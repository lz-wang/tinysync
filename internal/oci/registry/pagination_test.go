package registry

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"tinysync/internal/oci/image"
)

func registryFixture(t *testing.T, items []string, linkStyle string, status int) (*Client, string, *image.RegistryInfo, *atomic.Int32) {
	t.Helper()
	requests := new(atomic.Int32)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v2/" {
			w.WriteHeader(http.StatusOK)
			return
		}
		requests.Add(1)
		if status != 0 {
			w.WriteHeader(status)
			return
		}
		last := r.URL.Query().Get("last")
		start := 0
		for i, s := range items {
			if s == last {
				start = i + 1
			}
		}
		n := 1
		_, _ = fmt.Sscan(r.URL.Query().Get("n"), &n)
		end := min(start+n, len(items))
		if end < len(items) {
			path := r.URL.Path + "?n=" + fmt.Sprint(n) + "&last=" + url.QueryEscape(items[end-1])
			if linkStyle == "absolute" {
				path = "http://" + r.Host + path
			}
			if linkStyle == "evil" {
				path = "https://127.0.0.1:1" + path
			}
			if linkStyle == "scheme" {
				path = "ftp://" + r.Host + path
			}
			w.Header().Set("Link", "<"+path+">; rel=\"next\"")
		}
		key := "tags"
		if r.URL.Path == "/v2/_catalog" {
			key = "repositories"
		}
		_ = json.NewEncoder(w).Encode(map[string]any{key: items[start:end]})
	}))
	t.Cleanup(server.Close)
	return NewClient(nopLogger{}), strings.TrimPrefix(server.URL, "http://"), &image.RegistryInfo{PlainHTTP: true}, requests
}

// A malicious Link must be rejected before any request reaches the victim,
// rather than merely failing when an unreachable external host is contacted.
func TestDiscoveryDoesNotContactCrossHostLink(t *testing.T) {
	var victimCalls atomic.Int32
	victim := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		victimCalls.Add(1)
		_, _ = w.Write([]byte(`{"repositories":["stolen"]}`))
	}))
	t.Cleanup(victim.Close)
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v2/" {
			w.WriteHeader(200)
			return
		}
		w.Header().Set("Link", "<"+victim.URL+"/v2/_catalog?last=a>; rel=\"next\"")
		_, _ = w.Write([]byte(`{"repositories":["a"]}`))
	}))
	t.Cleanup(source.Close)
	client := NewClient(nopLogger{})
	_, err := client.SearchRepositories(context.Background(), strings.TrimPrefix(source.URL, "http://"), &image.RegistryInfo{PlainHTTP: true}, SearchInput{PageSize: 1})
	if err == nil || victimCalls.Load() != 0 {
		t.Fatalf("unsafe Link followed: error=%v calls=%d", err, victimCalls.Load())
	}
}

func TestDiscoveryCursorPathBoundary(t *testing.T) {
	c, host, info, calls := registryFixture(t, []string{"a"}, "relative", 0)
	for _, path := range []string{"//evil.example/v2/_catalog", "/v2/_catalog#fragment", "/v2/%5fcatalog", "/v2/other/tags/list", "/v2/_catalog/..", "https://user:password@" + host + "/v2/_catalog"} {
		cursor := encodeRemoteCursor(RemoteCursor{Version: 1, Registry: host, NextPath: path})
		if _, err := c.SearchRepositories(context.Background(), host, info, SearchInput{Cursor: cursor}); err == nil {
			t.Fatalf("unsafe path accepted: %s", path)
		}
	}
	if calls.Load() != 0 {
		t.Fatal("invalid cursor made a registry request")
	}
}

func TestDiscoveryCancelsInFlightRequest(t *testing.T) {
	started := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v2/" {
			w.WriteHeader(200)
			return
		}
		close(started)
		<-r.Context().Done()
	}))
	t.Cleanup(server.Close)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := NewClient(nopLogger{}).SearchRepositories(ctx, strings.TrimPrefix(server.URL, "http://"), &image.RegistryInfo{PlainHTTP: true}, SearchInput{})
		done <- err
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("request did not start")
	}
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancellation ignored")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancelled discovery remained blocked")
	}
}

type discoveryRoundTripper func(*http.Request) (*http.Response, error)

func (f discoveryRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type discoveryBody struct {
	io.Reader
	closed bool
}

func (b *discoveryBody) Close() error { b.closed = true; return nil }

func TestDiscoveryClosesOversizedResponse(t *testing.T) {
	body := &discoveryBody{Reader: bytes.NewReader(bytes.Repeat([]byte("a"), (4<<20)+1))}
	transport := &pageTransport{host: "registry.example", endpoint: "/v2/_catalog", size: 1, base: discoveryRoundTripper(func(*http.Request) (*http.Response, error) { return &http.Response{StatusCode: 200, Body: body}, nil })}
	req, _ := http.NewRequest("GET", "https://registry.example/v2/_catalog", nil)
	if _, err := transport.RoundTrip(req); err == nil || !body.closed {
		t.Fatalf("large response not bounded/closed: %v %v", err, body.closed)
	}
}

func TestEmptyDiscoveryPageCanContinue(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v2/" {
			w.WriteHeader(200)
			return
		}
		if r.URL.Query().Get("last") == "marker" {
			_, _ = w.Write([]byte(`{"repositories":["match"]}`))
			return
		}
		w.Header().Set("Link", `</v2/_catalog?last=marker>; rel="next"`)
		_, _ = w.Write([]byte(`{"repositories":[]}`))
	}))
	t.Cleanup(server.Close)
	c := NewClient(nopLogger{})
	host := strings.TrimPrefix(server.URL, "http://")
	info := &image.RegistryInfo{PlainHTTP: true}
	page, err := c.SearchRepositories(context.Background(), host, info, SearchInput{PageSize: 1})
	if err != nil || !page.HasMore || len(page.Items) != 0 || page.Scanned != 0 {
		t.Fatalf("empty continuation: %+v %v", page, err)
	}
	page, err = c.SearchRepositories(context.Background(), host, info, SearchInput{PageSize: 1, Cursor: page.NextCursor})
	if err != nil || page.HasMore || len(page.Items) != 1 || page.Items[0] != "match" {
		t.Fatalf("continuation: %+v %v", page, err)
	}
	missing, missingHost, missingInfo, _ := registryFixture(t, nil, "relative", 404)
	_, err = missing.ListTagsPage(context.Background(), missingHost+"/missing", missingInfo, "", 1)
	if de, ok := err.(*DiscoveryError); !ok || de.Code != "registry_not_found" {
		t.Fatalf("missing tag error: %v", err)
	}
}

func TestDiscoveryRejectsNonAdvancingCursor(t *testing.T) {
	for _, body := range []string{`{"repositories":[]}`, `{"repositories":["a"]}`} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/v2/" {
				w.WriteHeader(200)
				return
			}
			w.Header().Set("Link", `</v2/_catalog?last=a>; rel="next"`)
			_, _ = w.Write([]byte(body))
		}))
		host := strings.TrimPrefix(server.URL, "http://")
		cursor := encodeRemoteCursor(RemoteCursor{Version: 1, Registry: host, NextPath: "/v2/_catalog?last=a"})
		_, err := NewClient(nopLogger{}).SearchRepositories(context.Background(), host, &image.RegistryInfo{PlainHTTP: true}, SearchInput{PageSize: 1, Cursor: cursor})
		server.Close()
		if err == nil {
			t.Fatal("non-advancing pagination cursor accepted")
		}
	}
}

func TestRegistryDiscoveryPagination(t *testing.T) {
	for _, style := range []string{"relative", "absolute"} {
		t.Run(style, func(t *testing.T) {
			client, host, info, _ := registryFixture(t, []string{"team/a", "team/nginx", "other/nginx", "team/nginx-two"}, style, 0)
			first, err := client.SearchRepositories(context.Background(), host, info, SearchInput{Query: "nginx", Namespace: "team", PageSize: 1, ScanLimit: 2})
			if err != nil || !reflect.DeepEqual(first.Items, []string{"team/nginx"}) || !first.HasMore || first.Scanned != 2 {
				t.Fatalf("first: %+v %v", first, err)
			}
			second, err := client.SearchRepositories(context.Background(), host, info, SearchInput{Query: "nginx", Namespace: "team", PageSize: 1, Cursor: first.NextCursor})
			if err != nil || !reflect.DeepEqual(second.Items, []string{"team/nginx-two"}) || second.HasMore {
				t.Fatalf("second: %+v %v", second, err)
			}
			if _, err := client.SearchRepositories(context.Background(), host, info, SearchInput{Query: "different", Namespace: "team", Cursor: first.NextCursor}); err == nil {
				t.Fatal("filter-mismatched cursor accepted")
			}
			tags, err := client.ListTagsPage(context.Background(), host+"/foo", info, "", 2)
			if err != nil || len(tags.Items) != 2 || !tags.HasMore {
				t.Fatalf("tags: %+v %v", tags, err)
			}
			next, err := client.ListTagsPage(context.Background(), host+"/foo", info, tags.NextCursor, 2)
			if err != nil || len(next.Items) != 2 || next.HasMore {
				t.Fatalf("next: %+v %v", next, err)
			}
			if _, err := client.ListTagsPage(context.Background(), host+"/other", info, tags.NextCursor, 2); err == nil {
				t.Fatal("repository-mismatched cursor accepted")
			}
		})
	}
}

func TestRegistryDiscoveryBoundaries(t *testing.T) {
	for _, status := range []int{401, 403, 404, 429, 500} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			c, host, info, _ := registryFixture(t, nil, "relative", status)
			_, err := c.SearchRepositories(context.Background(), host, info, SearchInput{})
			want := map[int]string{401: "registry_unauthorized", 403: "registry_forbidden", 404: "registry_catalog_unsupported", 429: "registry_rate_limited", 500: "registry_unavailable"}[status]
			got, ok := err.(*DiscoveryError)
			if !ok || got.Code != want {
				t.Fatalf("got %v want %s", err, want)
			}
		})
	}
	for _, style := range []string{"evil", "scheme"} {
		t.Run(style, func(t *testing.T) {
			c, host, info, requests := registryFixture(t, []string{"a", "b"}, style, 0)
			if _, err := c.SearchRepositories(context.Background(), host, info, SearchInput{PageSize: 1}); err == nil {
				t.Fatal("malicious link accepted")
			}
			if requests.Load() != 1 {
				t.Fatal("malicious link was followed")
			}
		})
	}
	c, host, info, requests := registryFixture(t, []string{"a", "b", "c", "d"}, "relative", 0)
	page, err := c.SearchRepositories(context.Background(), host, info, SearchInput{Query: "missing", PageSize: 1, ScanLimit: 2})
	if err != nil || len(page.Items) != 0 || !page.ScanLimitReached || page.Scanned != 2 || requests.Load() != 2 {
		t.Fatalf("scan bound: %+v %v", page, err)
	}
	for _, cursor := range []string{"broken", encodeRemoteCursor(RemoteCursor{Version: 1, Registry: host, NextPath: "https://127.0.0.1:1/v2/_catalog"}), encodeRemoteCursor(RemoteCursor{Version: 1, Registry: "wrong", NextPath: "/v2/_catalog"}), encodeRemoteCursor(RemoteCursor{Version: 1, Registry: host, NextPath: "/v2/../settings"})} {
		if _, err := c.SearchRepositories(context.Background(), host, info, SearchInput{Cursor: cursor}); err == nil {
			t.Fatalf("invalid cursor accepted: %s", cursor)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.SearchRepositories(ctx, host, info, SearchInput{}); err == nil {
		t.Fatal("cancel ignored")
	}
	empty, emptyHost, emptyInfo, _ := registryFixture(t, nil, "relative", 0)
	page, err = empty.SearchRepositories(context.Background(), emptyHost, emptyInfo, SearchInput{})
	if err != nil || page.HasMore || len(page.Items) != 0 {
		t.Fatalf("empty: %+v %v", page, err)
	}
}

func TestUnsupportedRegistryOperations(t *testing.T) {
	for _, status := range []int{http.StatusMethodNotAllowed, http.StatusNotImplemented} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			c, host, info, _ := registryFixture(t, nil, "relative", status)
			_, err := c.SearchRepositories(context.Background(), host, info, SearchInput{})
			catalog, ok := err.(*DiscoveryError)
			if !ok || catalog.Code != "registry_catalog_unsupported" || catalog.Retryable {
				t.Fatalf("catalog: %v", err)
			}
			_, err = c.SearchTags(context.Background(), host+"/team/app", info, SearchInput{})
			tags, ok := err.(*DiscoveryError)
			if !ok || tags.Code != "registry_operation_unsupported" || tags.Retryable {
				t.Fatalf("tags: %v", err)
			}
		})
	}
}

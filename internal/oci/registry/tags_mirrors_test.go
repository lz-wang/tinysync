package registry

import (
	"context"
	"reflect"
	"testing"
)

func TestTagMirrorPaginationSourceBoundary(t *testing.T) {
	c, mirror, info, calls := registryFixture(t, []string{"a", "b"}, "relative", 0)
	cfg := newStaticResolver(map[string][]string{"docker.io": {mirror}})
	page, err := c.SearchTagsWithMirrors(context.Background(), "docker.io/library/nginx", info, cfg, SearchInput{PageSize: 1})
	if err != nil || !page.HasMore || !reflect.DeepEqual(page.Items, []string{"a"}) {
		t.Fatalf("first: %+v %v", page, err)
	}
	cursor := page.NextCursor
	for _, test := range []struct {
		name, repository string
		cfg              Resolver
		input            SearchInput
	}{
		{"different source", "docker.io/library/other", cfg, SearchInput{Cursor: cursor}},
		{"different query", "docker.io/library/nginx", cfg, SearchInput{Query: "b", Cursor: cursor}},
		{"removed mirror", "docker.io/library/nginx", newStaticResolver(nil), SearchInput{Cursor: cursor}},
		{"malformed cursor", "docker.io/library/nginx", cfg, SearchInput{Cursor: "broken"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			before := calls.Load()
			_, err := c.SearchTagsWithMirrors(context.Background(), test.repository, info, test.cfg, test.input)
			if err == nil || calls.Load() != before {
				t.Fatalf("invalid continuation made a request: %v", err)
			}
		})
	}
	page, err = c.SearchTagsWithMirrors(context.Background(), "docker.io/library/nginx", info, cfg, SearchInput{PageSize: 1, Cursor: cursor})
	if err != nil || page.HasMore || !reflect.DeepEqual(page.Items, []string{"b"}) {
		t.Fatalf("next: %+v %v", page, err)
	}
}

func TestTagMirrorFirstPageFallback(t *testing.T) {
	c, primary, info, primaryCalls := registryFixture(t, nil, "relative", 503)
	_, failedMirror, _, failedCalls := registryFixture(t, nil, "relative", 503)
	_, mirror, _, mirrorCalls := registryFixture(t, []string{"a"}, "relative", 0)
	cfg := newStaticResolver(map[string][]string{primary: {failedMirror, mirror}})
	page, err := c.SearchTagsWithMirrors(context.Background(), primary+"/team/app", info, cfg, SearchInput{})
	if err != nil || !reflect.DeepEqual(page.Items, []string{"a"}) || primaryCalls.Load() != 1 || failedCalls.Load() != 1 || mirrorCalls.Load() != 1 {
		t.Fatalf("fallback: %+v %v", page, err)
	}
}

package registry

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/remote/transport"

	"tinysync/internal/oci/image"
)

// DiscoveryError is a stable error category, safe to return to remote clients.
type DiscoveryError struct {
	Code      string
	Retryable bool
	Cause     error
}

func (e *DiscoveryError) Error() string { return e.Code }

func (e *DiscoveryError) Unwrap() error { return e.Cause }

// Page bounds registry discovery output.
type Page[T any] struct {
	Items      []T    `json:"items"`
	HasMore    bool   `json:"has_more"`
	NextCursor string `json:"next_cursor,omitempty"`
}

// SearchPage reports how much of the catalog was scanned, without a fabricated total.
type SearchPage[T any] struct {
	Page[T]
	Scanned          int  `json:"scanned"`
	ScanLimitReached bool `json:"scan_limit_reached"`
}

// SearchInput contains filtering and bounded discovery options.
type SearchInput struct {
	RegistryConfig string `json:"-"`
	Query          string `json:"query,omitempty"`
	Namespace      string `json:"-"`
	PageSize       int    `json:"page_size,omitempty"`
	ScanLimit      int    `json:"scan_limit,omitempty"`
	Cursor         string `json:"cursor,omitempty"`
}

// RemoteCursor never contains a remote URL and is revalidated on every request.
type RemoteCursor struct {
	RegistryConfig   string `json:"registry_config,omitempty"`
	Version          int    `json:"v"`
	Registry         string `json:"registry"`
	Repo             string `json:"repo,omitempty"`
	NextPath         string `json:"next_path"`
	Query            string `json:"query,omitempty"`
	Namespace        string `json:"namespace,omitempty"`
	SourceRepository string `json:"source_repository,omitempty"`
}

func discoveryError(err error, catalog bool) error {
	var de *DiscoveryError
	if errors.As(err, &de) {
		return err
	}
	code := "registry_unavailable"
	retryable := true
	var te *transport.Error
	if errors.As(err, &te) {
		switch te.StatusCode {
		case 401:
			code = "registry_unauthorized"
			retryable = false
		case 403:
			code = "registry_forbidden"
			retryable = false
		case 404:
			code = "registry_not_found"
			retryable = false
			if catalog {
				code = "registry_catalog_unsupported"
			}
		case 405, 501:
			code = "registry_operation_unsupported"
			if catalog {
				code = "registry_catalog_unsupported"
			}
			retryable = false
		case 429:
			code = "registry_rate_limited"
		}
	}
	return &DiscoveryError{Code: code, Retryable: retryable, Cause: err}
}

// ClassifyRemoteError applies the same safe status mapping to OCI inspection.
func ClassifyRemoteError(err error) error { return discoveryError(err, false) }

func cursorError(err error) error { return &DiscoveryError{Code: "invalid_cursor", Cause: err} }

func encodeRemoteCursor(c RemoteCursor) string {
	data, _ := json.Marshal(c)
	return base64.RawURLEncoding.EncodeToString(data)
}

func validateNextPath(value, endpoint string) (*url.URL, error) {
	u, err := url.Parse(value)
	if err != nil || u == nil || u.Scheme != "" || u.Host != "" || u.User != nil || u.Opaque != "" || u.Fragment != "" || u.Path != endpoint || u.RawPath != "" || !strings.HasPrefix(value, "/v2/") {
		return nil, cursorError(fmt.Errorf("invalid pagination path"))
	}
	return u, nil
}

func decodeRemoteCursor(value, host, repo, endpoint string, input SearchInput) (string, error) {
	if value == "" {
		return "", nil
	}
	if len(value) > 8192 {
		return "", cursorError(fmt.Errorf("cursor too long"))
	}
	data, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return "", cursorError(err)
	}
	var c RemoteCursor
	if err := json.Unmarshal(data, &c); err != nil {
		return "", cursorError(err)
	}
	if c.Version != 1 || c.Registry != host || c.Repo != repo || c.Query != input.Query || c.Namespace != input.Namespace || c.RegistryConfig != input.RegistryConfig {
		return "", cursorError(fmt.Errorf("cursor does not match request"))
	}
	if _, err := validateNextPath(c.NextPath, endpoint); err != nil {
		return "", err
	}
	return c.NextPath, nil
}

// pageTransport resumes only a validated endpoint. The registry SDK still owns
// authentication and Link resolution/host checks. Response bytes are bounded
// and closed here, including SDK error paths.
type pageTransport struct {
	base                     http.RoundTripper
	host, endpoint, nextPath string
	size                     int
}

func (t *pageTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Host != t.host || req.URL.Path != t.endpoint {
		return t.base.RoundTrip(req)
	}
	clone := req.Clone(req.Context())
	u := *req.URL
	if t.nextPath != "" {
		next, err := validateNextPath(t.nextPath, t.endpoint)
		if err != nil {
			return nil, err
		}
		u.Path = next.Path
		u.RawQuery = next.RawQuery
	}
	q := u.Query()
	q.Set("n", fmt.Sprint(t.size))
	u.RawQuery = q.Encode()
	clone.URL = &u
	resp, err := t.base.RoundTrip(clone)
	if err != nil {
		return nil, err
	}
	data, readErr := io.ReadAll(io.LimitReader(resp.Body, 4<<20+1))
	closeErr := resp.Body.Close()
	if readErr != nil {
		return nil, readErr
	}
	if closeErr != nil {
		return nil, closeErr
	}
	if len(data) > 4<<20 {
		return nil, fmt.Errorf("registry discovery response too large")
	}
	resp.Body = io.NopCloser(bytes.NewReader(data))
	resp.Request = clone
	return resp, nil
}

func (c *Client) discoveryPage(ctx context.Context, host, repo, endpoint, path string, size int, info *image.RegistryInfo) ([]string, string, error) {
	opts := c.createRemoteOptions(ctx, info)
	opts = append(opts, remote.WithPageSize(size), remote.WithTransport(&pageTransport{base: c.createTransport(info), host: host, endpoint: endpoint, nextPath: path, size: size}), remote.WithRetryPredicate(func(error) bool { return false }))
	puller, err := remote.NewPuller(opts...)
	if err != nil {
		return nil, "", err
	}
	if repo == "" {
		reg, err := name.NewRegistry(host, createNameOptions(info)...)
		if err != nil {
			return nil, "", err
		}
		iter, err := puller.Catalogger(ctx, reg)
		if err != nil {
			return nil, "", err
		}
		page, err := iter.Next(ctx)
		if err != nil {
			return nil, "", err
		}
		return page.Repos, page.Next, nil
	}
	ref, err := name.NewRepository(host+"/"+repo, createNameOptions(info)...)
	if err != nil {
		return nil, "", err
	}
	iter, err := puller.Lister(ctx, ref)
	if err != nil {
		return nil, "", err
	}
	page, err := iter.Next(ctx)
	if err != nil {
		return nil, "", err
	}
	return page.Tags, page.Next, nil
}

func (c *Client) discover(ctx context.Context, host, repo string, info *image.RegistryInfo, input SearchInput) (*SearchPage[string], error) {
	if input.PageSize == 0 {
		input.PageSize = 50
	}
	if input.ScanLimit == 0 {
		input.ScanLimit = 1000
	}
	if input.PageSize < 1 || input.PageSize > 100 || input.ScanLimit < 1 || input.ScanLimit > 5000 {
		return nil, &DiscoveryError{Code: "invalid_input", Cause: fmt.Errorf("page_size must be 1–100 and scan_limit 1–5000")}
	}
	endpoint := "/v2/_catalog"
	if repo != "" {
		endpoint = "/v2/" + repo + "/tags/list"
	}
	path, err := decodeRemoteCursor(input.Cursor, host, repo, endpoint, input)
	if err != nil {
		return nil, err
	}
	result := &SearchPage[string]{Page: Page[string]{Items: []string{}}}
	seen := make(map[string]bool)
	for result.Scanned < input.ScanLimit && len(result.Items) < input.PageSize {
		if err := ctx.Err(); err != nil {
			return nil, discoveryError(err, repo == "")
		}
		if seen[path] {
			return nil, discoveryError(fmt.Errorf("registry pagination cycle"), repo == "")
		}
		seen[path] = true
		size := min(input.PageSize-len(result.Items), input.ScanLimit-result.Scanned)
		items, next, err := c.discoveryPage(ctx, host, repo, endpoint, path, size, info)
		if err != nil {
			return nil, discoveryError(err, repo == "")
		}
		if len(items) > size {
			return nil, discoveryError(fmt.Errorf("registry did not honor page size"), repo == "")
		}
		result.Scanned += len(items)
		for _, item := range items {
			if input.Namespace != "" && !strings.HasPrefix(item, strings.TrimSuffix(input.Namespace, "/")+"/") {
				continue
			}
			if strings.Contains(strings.ToLower(item), strings.ToLower(input.Query)) {
				result.Items = append(result.Items, item)
			}
		}
		if next == "" {
			result.HasMore = false
			result.NextCursor = ""
			return result, nil
		}
		u, err := url.Parse(next)
		if err != nil {
			return nil, cursorError(err)
		}
		reg, err := name.NewRegistry(host, createNameOptions(info)...)
		if err != nil {
			return nil, discoveryError(err, repo == "")
		}
		if u.Host != reg.RegistryStr() || u.Scheme != reg.Scheme() || u.User != nil || u.Fragment != "" {
			return nil, cursorError(fmt.Errorf("unsafe registry pagination link"))
		}
		path = u.RequestURI()
		if _, err := validateNextPath(path, endpoint); err != nil {
			return nil, err
		}
		if seen[path] {
			return nil, discoveryError(fmt.Errorf("registry pagination cycle"), repo == "")
		}
		result.HasMore = true
		result.NextCursor = encodeRemoteCursor(RemoteCursor{Version: 1, Registry: host, Repo: repo, NextPath: path, Query: input.Query, Namespace: input.Namespace, RegistryConfig: input.RegistryConfig})
		// Empty pages can still advertise continuation. Bound remote requests too.
		if len(items) == 0 {
			break
		}
	}
	result.ScanLimitReached = result.HasMore && result.Scanned >= input.ScanLimit
	return result, nil
}

// SearchRepositories performs bounded catalog scanning and local filtering.
func (c *Client) SearchRepositories(ctx context.Context, host string, info *image.RegistryInfo, input SearchInput) (*SearchPage[string], error) {
	reg, err := name.NewRegistry(host, createNameOptions(info)...)
	if err != nil {
		return nil, &DiscoveryError{Code: "invalid_input", Cause: err}
	}
	return c.discover(ctx, reg.RegistryStr(), "", info, input)
}

// SearchTags performs bounded tag scanning without inspecting manifests.
func (c *Client) SearchTags(ctx context.Context, repository string, info *image.RegistryInfo, input SearchInput) (*SearchPage[string], error) {
	repo, err := name.NewRepository(repository, createNameOptions(info)...)
	if err != nil {
		return nil, &DiscoveryError{Code: "invalid_input", Cause: err}
	}
	return c.discover(ctx, repo.RegistryStr(), repo.RepositoryStr(), info, input)
}

// ListTagsPage exposes one bounded page of repository tags.
func (c *Client) ListTagsPage(ctx context.Context, repo string, info *image.RegistryInfo, cursor string, pageSize int) (*Page[string], error) {
	page, err := c.SearchTags(ctx, repo, info, SearchInput{Cursor: cursor, PageSize: pageSize})
	if err != nil {
		return nil, err
	}
	return &page.Page, nil
}

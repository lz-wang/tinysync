package registry

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"tinysync/internal/oci/image"
)

// SearchTagsWithMirrors keeps pagination on the selected source. A failed first
// page may fall back; a continuation must never switch to another tag collection.
func (c *Client) SearchTagsWithMirrors(ctx context.Context, repository string, info *image.RegistryInfo, res Resolver, input SearchInput) (*SearchPage[string], error) {
	type source struct {
		repository string
		info       *image.RegistryInfo
	}
	host := strings.SplitN(repository, "/", 2)[0]
	policy := res.SourcePolicy(host)
	primary := source{repository, info}
	sources := make([]source, 0)
	if !policy.SkipPrimary {
		sources = append(sources, primary)
	}
	for _, addr := range policy.Mirrors {
		sources = append(sources, source{constructMirrorRepoName(repository, addr), anonymousMirrorInfo})
	}
	if policy.SkipPrimary {
		sources = append(sources, primary)
	}
	var cursor RemoteCursor
	if input.Cursor != "" {
		if len(input.Cursor) > 8192 {
			return nil, cursorError(fmt.Errorf("cursor too long"))
		}
		data, err := base64.RawURLEncoding.DecodeString(input.Cursor)
		if err != nil {
			return nil, cursorError(err)
		}
		if err := json.Unmarshal(data, &cursor); err != nil || cursor.SourceRepository != repository {
			return nil, cursorError(fmt.Errorf("cursor does not match source repository"))
		}
	}
	var lastErr error
	for _, src := range sources {
		if err := ctx.Err(); err != nil {
			return nil, discoveryError(err, false)
		}
		if input.Cursor != "" {
			ref, err := parseRepository(src.repository, src.info)
			if err != nil || ref.RegistryStr() != cursor.Registry || ref.RepositoryStr() != cursor.Repo {
				continue
			}
		}
		page, err := c.SearchTags(ctx, src.repository, src.info, input)
		if err != nil {
			var de *DiscoveryError
			if input.Cursor != "" || errors.As(err, &de) && (de.Code == "invalid_cursor" || de.Code == "invalid_input") {
				return nil, err
			}
			lastErr = err
			continue
		}
		if page.NextCursor != "" {
			data, err := base64.RawURLEncoding.DecodeString(page.NextCursor)
			if err != nil {
				return nil, cursorError(err)
			}
			var next RemoteCursor
			if err := json.Unmarshal(data, &next); err != nil {
				return nil, cursorError(err)
			}
			next.SourceRepository = repository
			page.NextCursor = encodeRemoteCursor(next)
		}
		return page, nil
	}
	if input.Cursor != "" {
		return nil, cursorError(fmt.Errorf("pagination source is no longer configured"))
	}
	return nil, lastErr
}

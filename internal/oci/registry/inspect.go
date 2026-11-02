package registry

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/remote"

	"tinysync/internal/oci/image"
)

func (c *Client) buildIndexInspectResult(desc *remote.Descriptor, ref name.Reference, tag string) (*image.RemoteInspectResult, error) {
	index, err := desc.ImageIndex()
	if err != nil {
		return nil, fmt.Errorf("failed to get image index: %w", err)
	}

	manifest, err := index.IndexManifest()
	if err != nil {
		return nil, fmt.Errorf("failed to get index manifest: %w", err)
	}

	c.logger.Infof("detected manifest index (multi-arch): manifest_count=%d", len(manifest.Manifests))

	manifestJSON, err := json.Marshal(manifest)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal index manifest: %w", err)
	}

	return &image.RemoteInspectResult{
		Ref:       ref.String(),
		Tag:       tag,
		Digest:    desc.Digest.String(),
		MediaType: string(desc.MediaType),
		IsIndex:   true,
		Manifest:  json.RawMessage(manifestJSON),
		Platforms: indexPlatforms(manifest),
	}, nil
}

func calculateManifestImageSize(manifest []byte) (int64, error) {
	var manifestData v1.Manifest
	if err := json.Unmarshal(manifest, &manifestData); err != nil {
		return 0, fmt.Errorf("failed to unmarshal manifest: %w", err)
	}

	size := manifestData.Config.Size
	for _, layer := range manifestData.Layers {
		size += layer.Size
	}

	return size, nil
}

func (c *Client) buildSingleInspectResult(desc *remote.Descriptor, ref name.Reference, tag string) (*image.RemoteInspectResult, error) {
	img, err := desc.Image()
	if err != nil {
		return nil, fmt.Errorf("failed to get image: %w", err)
	}

	manifest, err := img.RawManifest()
	if err != nil {
		return nil, fmt.Errorf("failed to get manifest: %w", err)
	}

	configFile, err := img.ConfigFile()
	if err != nil {
		return nil, fmt.Errorf("failed to get config file: %w", err)
	}

	configJSON, err := img.RawConfigFile()
	if err != nil {
		return nil, fmt.Errorf("failed to get raw config file: %w", err)
	}

	size, err := calculateManifestImageSize(manifest)
	if err != nil {
		return nil, err
	}

	c.logger.Debugf("detected single-arch manifest")

	return &image.RemoteInspectResult{
		Ref:       ref.String(),
		Tag:       tag,
		Digest:    desc.Digest.String(),
		MediaType: string(desc.MediaType),
		Size:      size,
		IsIndex:   false,
		Manifest:  json.RawMessage(manifest),
		Config:    json.RawMessage(configJSON),
		Platforms: singlePlatform(configFile),
	}, nil
}

func (c *Client) InspectImage(ctx context.Context, src *image.SrcImage, res Resolver) (*image.RemoteInspectResult, error) {
	repoRef := strings.Split(src.RepoName, "/")[0]
	policy := res.SourcePolicy(repoRef)

	attemptedRegistries := []string{}
	var lastErr error

	if !policy.SkipPrimary {
		c.logger.Infof("attempting to inspect image from primary registry: registry=%s image=%s",
			repoRef, src.RepoName+":"+src.Tag)
		attemptedRegistries = append(attemptedRegistries, repoRef)

		result, err := c.inspectImageWithRepo(ctx, src.RepoName, src.Tag, src.Registry)
		if err == nil {
			c.logger.Infof("successfully inspected image from primary registry: registry=%s", repoRef)
			result.Mirror = ""
			return result, nil
		}
		lastErr = err
		c.logger.Warnf("failed to inspect from primary registry: registry=%s error=%v", repoRef, err)
	} else {
		c.logger.Infof("skipping primary registry for %s, will use mirrors directly", repoRef)
	}

	// Try mirrors (anonymous access — mirrors never inherit source credentials)
	if len(policy.Mirrors) > 0 {
		for i, mirrorAddr := range policy.Mirrors {
			attemptedRegistries = append(attemptedRegistries, mirrorAddr)
			mirrorRepoName := constructMirrorRepoName(src.RepoName, mirrorAddr)

			c.logger.Infof("attempting to inspect image from mirror: mirror_index=%d mirror=%s image=%s",
				i+1, mirrorAddr, mirrorRepoName+":"+src.Tag)

			result, err := c.inspectImageWithRepo(ctx, mirrorRepoName, src.Tag, anonymousMirrorInfo)
			if err == nil {
				c.logger.Infof("successfully inspected image from mirror: mirror_index=%d mirror=%s", i+1, mirrorAddr)
				result.Mirror = mirrorAddr
				return result, nil
			}
			lastErr = err
			c.logger.Warnf("failed to inspect from mirror: mirror_index=%d mirror=%s error=%v", i+1, mirrorAddr, err)
		}
	}

	// If SkipPrimary is true but no mirrors available, fallback to primary registry
	if policy.SkipPrimary && len(attemptedRegistries) == 0 {
		c.logger.Infof("no mirrors configured for %s, falling back to primary registry", repoRef)
		attemptedRegistries = append(attemptedRegistries, repoRef)
		result, err := c.inspectImageWithRepo(ctx, src.RepoName, src.Tag, src.Registry)
		if err == nil {
			c.logger.Infof("successfully inspected image from primary registry (fallback): registry=%s", repoRef)
			result.Mirror = ""
			return result, nil
		}
		lastErr = err
		c.logger.Warnf("failed to inspect from primary registry (fallback): registry=%s error=%v", repoRef, err)
	}

	return nil, fmt.Errorf("failed to inspect image after trying all registries/mirrors (tried: %v): %w", attemptedRegistries, lastErr)
}

func (c *Client) inspectImageWithRepo(ctx context.Context, repoName, tag string, regInfo *image.RegistryInfo) (*image.RemoteInspectResult, error) {
	ref, err := parseReference(repoName, tag, regInfo)
	if err != nil {
		return nil, fmt.Errorf("failed to parse reference: %w", err)
	}

	opts := c.createRemoteOptions(ctx, regInfo)

	desc, err := remote.Get(ref, opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to get image descriptor: %w", err)
	}

	// Check if it's an index (multi-arch)
	if desc.MediaType.IsIndex() {
		return c.buildIndexInspectResult(desc, ref, tag)
	}

	return c.buildSingleInspectResult(desc, ref, tag)
}

func (c *Client) InspectImagePlatform(ctx context.Context, src *image.SrcImage, platform image.PlatformInfo, res Resolver) (*image.RemoteInspectResult, error) {
	repoRef := strings.Split(src.RepoName, "/")[0]
	policy := res.SourcePolicy(repoRef)

	attemptedRegistries := []string{}
	var lastErr error

	if !policy.SkipPrimary {
		attemptedRegistries = append(attemptedRegistries, repoRef)
		result, err := c.inspectPlatformImageWithRepo(ctx, src.RepoName, src.Tag, src.Registry, platform)
		if err == nil {
			result.Mirror = ""
			return result, nil
		}
		lastErr = err
	}

	if len(policy.Mirrors) > 0 {
		for _, mirrorAddr := range policy.Mirrors {
			attemptedRegistries = append(attemptedRegistries, mirrorAddr)
			mirrorRepoName := constructMirrorRepoName(src.RepoName, mirrorAddr)

			result, err := c.inspectPlatformImageWithRepo(ctx, mirrorRepoName, src.Tag, anonymousMirrorInfo, platform)
			if err == nil {
				result.Mirror = mirrorAddr
				return result, nil
			}
			lastErr = err
		}
	}

	if policy.SkipPrimary && len(attemptedRegistries) == 0 {
		attemptedRegistries = append(attemptedRegistries, repoRef)
		result, err := c.inspectPlatformImageWithRepo(ctx, src.RepoName, src.Tag, src.Registry, platform)
		if err == nil {
			result.Mirror = ""
			return result, nil
		}
		lastErr = err
	}

	return nil, fmt.Errorf("failed to inspect platform image after trying all registries/mirrors (tried: %v): %w", attemptedRegistries, lastErr)
}

func (c *Client) inspectPlatformImageWithRepo(ctx context.Context, repoName, tag string, regInfo *image.RegistryInfo, platform image.PlatformInfo) (*image.RemoteInspectResult, error) {
	ref, err := parseReference(repoName, tag, regInfo)
	if err != nil {
		return nil, fmt.Errorf("failed to parse reference: %w", err)
	}

	opts := c.createRemoteOptions(ctx, regInfo)
	desc, err := remote.Get(ref, opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to get image descriptor: %w", err)
	}

	if !desc.MediaType.IsIndex() {
		return nil, fmt.Errorf("image %s is not a multi-platform manifest index", ref.String())
	}

	index, err := desc.ImageIndex()
	if err != nil {
		return nil, fmt.Errorf("failed to get image index: %w", err)
	}

	manifest, err := index.IndexManifest()
	if err != nil {
		return nil, fmt.Errorf("failed to get index manifest: %w", err)
	}

	platformManifest := findPlatformManifest(manifest, platform)
	if platformManifest == nil {
		normalizedPlatform := normalizePlatformInfo(platform)
		return nil, fmt.Errorf("%w: %s/%s/%s", ErrPlatformNotFound, normalizedPlatform.OS, normalizedPlatform.Architecture, normalizedPlatform.Variant)
	}

	platformRef, err := parseDigestReference(repoName, platformManifest.Digest.String(), regInfo)
	if err != nil {
		return nil, fmt.Errorf("failed to parse platform reference: %w", err)
	}

	platformDesc, err := remote.Get(platformRef, opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to get platform descriptor: %w", err)
	}

	return c.buildSingleInspectResult(platformDesc, platformRef, tag)
}

func (c *Client) ListRepositories(ctx context.Context, registry, username, password string) ([]string, error) {
	var repos []string
	cursor := ""
	for {
		page, err := c.SearchRepositories(ctx, registry, &image.RegistryInfo{Username: username, Password: password}, SearchInput{PageSize: 100, Cursor: cursor})
		if err != nil {
			return nil, err
		}
		repos = append(repos, page.Items...)
		if !page.HasMore {
			return repos, nil
		}
		cursor = page.NextCursor
	}
}

func (c *Client) ListTags(ctx context.Context, repoName string, regInfo *image.RegistryInfo, res Resolver) ([]string, error) {
	repoRef := strings.Split(repoName, "/")[0]
	policy := res.SourcePolicy(repoRef)

	attemptedRegistries := []string{}
	var lastErr error

	if !policy.SkipPrimary {
		attemptedRegistries = append(attemptedRegistries, repoRef)
		tags, err := c.listTagsWithRepo(ctx, repoName, regInfo)
		if err == nil {
			return tags, nil
		}
		lastErr = err
		c.logger.Warnf("failed to list tags from primary registry: registry=%s error=%v", repoRef, err)
	} else {
		c.logger.Infof("skipping primary registry for %s tags listing, will use mirrors directly", repoRef)
	}

	if len(policy.Mirrors) > 0 {
		for i, mirrorAddr := range policy.Mirrors {
			attemptedRegistries = append(attemptedRegistries, mirrorAddr)
			mirrorRepoName := constructMirrorRepoName(repoName, mirrorAddr)
			tags, err := c.listTagsWithRepo(ctx, mirrorRepoName, anonymousMirrorInfo)
			if err == nil {
				c.logger.Infof("successfully listed tags from mirror: mirror_index=%d mirror=%s", i+1, mirrorAddr)
				return tags, nil
			}
			lastErr = err
			c.logger.Warnf("failed to list tags from mirror: mirror_index=%d mirror=%s error=%v", i+1, mirrorAddr, err)
		}
	}

	if policy.SkipPrimary && len(attemptedRegistries) == 0 {
		attemptedRegistries = append(attemptedRegistries, repoRef)
		tags, err := c.listTagsWithRepo(ctx, repoName, regInfo)
		if err == nil {
			return tags, nil
		}
		lastErr = err
		c.logger.Warnf("failed to list tags from primary registry (fallback): registry=%s error=%v", repoRef, err)
	}

	return nil, fmt.Errorf("failed to list tags after trying all registries/mirrors (tried: %v): %w", attemptedRegistries, lastErr)
}

func (c *Client) listTagsWithRepo(ctx context.Context, repoName string, regInfo *image.RegistryInfo) ([]string, error) {
	repo, err := parseRepository(repoName, regInfo)
	if err != nil {
		return nil, fmt.Errorf("failed to parse repository: %w", err)
	}

	tags, err := remote.List(repo, c.createRemoteOptions(ctx, regInfo)...)
	if err != nil {
		return nil, fmt.Errorf("failed to list tags: %w", err)
	}

	return tags, nil
}

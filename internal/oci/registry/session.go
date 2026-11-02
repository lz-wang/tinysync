package registry

import (
	"context"
	"sync"

	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"golang.org/x/sync/singleflight"

	"tinysync/internal/oci/image"
)

type sourceDescriptorCacheEntry struct {
	descriptor *remote.Descriptor
	usedMirror string
}

type platformCopyTaskResult struct {
	index          int
	manifest       v1.Descriptor
	platform       string
	platformResult image.PlatformCopyResult
	copied         bool
}

// CopySession caches source-side metadata within a single task execution.
type CopySession struct {
	descriptorMu       sync.RWMutex
	descriptorCache    map[string]sourceDescriptorCacheEntry
	descriptorGroup    singleflight.Group
	indexManifestMu    sync.RWMutex
	indexManifestCache map[string]*v1.IndexManifest
	indexManifestGroup singleflight.Group
}

// NewCopySession creates a per-task metadata cache for copy operations.
func sourceDescriptorCacheKey(src *image.SrcImage) string {
	if src == nil {
		return ""
	}
	return src.RepoName + ":" + src.Tag
}

func (s *CopySession) getSourceDescriptor(cacheKey string) (*remote.Descriptor, string, bool) {
	if s == nil || cacheKey == "" {
		return nil, "", false
	}
	s.descriptorMu.RLock()
	defer s.descriptorMu.RUnlock()
	entry, ok := s.descriptorCache[cacheKey]
	if !ok {
		return nil, "", false
	}
	return entry.descriptor, entry.usedMirror, true
}

func (s *CopySession) storeSourceDescriptor(cacheKey string, desc *remote.Descriptor, usedMirror string) {
	if s == nil || cacheKey == "" || desc == nil {
		return
	}
	s.descriptorMu.Lock()
	defer s.descriptorMu.Unlock()
	s.descriptorCache[cacheKey] = sourceDescriptorCacheEntry{
		descriptor: desc,
		usedMirror: usedMirror,
	}
}

func (s *CopySession) getIndexManifest(cacheKey string) (*v1.IndexManifest, bool) {
	if s == nil || cacheKey == "" {
		return nil, false
	}
	s.indexManifestMu.RLock()
	defer s.indexManifestMu.RUnlock()
	manifest, ok := s.indexManifestCache[cacheKey]
	return manifest, ok
}

func (s *CopySession) storeIndexManifest(cacheKey string, manifest *v1.IndexManifest) {
	if s == nil || cacheKey == "" || manifest == nil {
		return
	}
	s.indexManifestMu.Lock()
	defer s.indexManifestMu.Unlock()
	s.indexManifestCache[cacheKey] = manifest
}

func (c *Client) getDescriptorWithMirrorCached(
	ctx context.Context,
	src *image.SrcImage,
	res Resolver,
	repoRef string,
	session *CopySession,
) (*remote.Descriptor, string, error) {
	cacheKey := sourceDescriptorCacheKey(src)
	if desc, usedMirror, ok := session.getSourceDescriptor(cacheKey); ok {
		return desc, usedMirror, nil
	}

	policy := res.SourcePolicy(repoRef)
	if session == nil || cacheKey == "" {
		return c.getDescriptorWithMirror(ctx, src.RepoName, src.Tag, src.Registry, policy, repoRef)
	}

	value, err, _ := session.descriptorGroup.Do(cacheKey, func() (interface{}, error) {
		if desc, usedMirror, ok := session.getSourceDescriptor(cacheKey); ok {
			return sourceDescriptorCacheEntry{descriptor: desc, usedMirror: usedMirror}, nil
		}

		desc, usedMirror, err := c.getDescriptorWithMirror(ctx, src.RepoName, src.Tag, src.Registry, policy, repoRef)
		if err != nil {
			return nil, err
		}
		session.storeSourceDescriptor(cacheKey, desc, usedMirror)
		return sourceDescriptorCacheEntry{descriptor: desc, usedMirror: usedMirror}, nil
	})
	if err != nil {
		return nil, "", err
	}

	entry := value.(sourceDescriptorCacheEntry)
	return entry.descriptor, entry.usedMirror, nil
}

func (c *Client) getIndexManifestCached(cacheKey string, srcDesc *remote.Descriptor, session *CopySession) (*v1.IndexManifest, error) {
	if manifest, ok := session.getIndexManifest(cacheKey); ok {
		return manifest, nil
	}

	if session == nil || cacheKey == "" {
		srcIndex, err := srcDesc.ImageIndex()
		if err != nil {
			return nil, err
		}
		return srcIndex.IndexManifest()
	}

	value, err, _ := session.indexManifestGroup.Do(cacheKey, func() (interface{}, error) {
		if manifest, ok := session.getIndexManifest(cacheKey); ok {
			return manifest, nil
		}

		srcIndex, err := srcDesc.ImageIndex()
		if err != nil {
			return nil, err
		}
		manifest, err := srcIndex.IndexManifest()
		if err != nil {
			return nil, err
		}
		session.storeIndexManifest(cacheKey, manifest)
		return manifest, nil
	})
	if err != nil {
		return nil, err
	}

	return value.(*v1.IndexManifest), nil
}

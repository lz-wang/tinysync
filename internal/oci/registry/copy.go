package registry

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/remote/transport"
	"github.com/google/go-containerregistry/pkg/v1/types"

	"tinysync/internal/oci/image"
)

// CopyImage 复制镜像。源端：普通 registry 走 primary → mirrors；
// docker.io 跳过 primary，mirrors 优先且全部失败后回退 primary（mirror 不是硬依赖）。
// 目标端只使用配置的 destination；重试固定在 registry copy 内部按镜像/平台粒度进行。
func (c *Client) CopyImage(ctx context.Context, src *image.SrcImage, dst *image.DstImage, res Resolver, taskPlatforms []PlatformConfig, session *CopySession) (*image.CopyResult, error) {
	srcRepoRef := strings.Split(src.RepoName, "/")[0]

	c.logger.Infof("starting image copy: source=%s source_registry=%s destination=%s destination_registry=%s",
		src.RepoName+":"+src.Tag, srcRepoRef, dst.RepoName+":"+dst.Tag, strings.Split(dst.RepoName, "/")[0])

	// Try to get source descriptor with mirror fallback
	srcDesc, srcMirrorUsed, err := c.getDescriptorWithMirrorCached(ctx, src, res, srcRepoRef, session)
	if err != nil {
		return c.copyResultError(
			src.RepoName+":"+src.Tag,
			dst.RepoName+":"+dst.Tag,
			fmt.Sprintf("failed to access source registry: %v", err),
			srcMirrorUsed,
		), nil
	}

	// Resolve copy platforms: task-level only（未配置则同步全部平台）
	platforms := resolveCopyPlatforms(taskPlatforms)
	sourceCacheKey := sourceDescriptorCacheKey(src)

	// No platforms configured: for multi-arch images, copy all source platforms so
	// retries can target only failed platforms. Single-arch images still use the full path.
	if len(platforms) == 0 && srcDesc.MediaType.IsIndex() {
		srcManifest, err := c.getIndexManifestCached(sourceCacheKey, srcDesc, session)
		if err == nil {
			platforms = collectPlatformsFromIndexManifest(srcManifest)
		}
	}

	if len(platforms) == 0 {
		return c.copyFullImage(ctx, src, dst, srcDesc, srcMirrorUsed)
	}

	// Multi-platform copy
	return c.copyMultiPlatformImage(ctx, src, dst, srcDesc, platforms, srcMirrorUsed, session, sourceCacheKey)
}

func (c *Client) copyFullImage(ctx context.Context, src *image.SrcImage, dst *image.DstImage, srcDesc *remote.Descriptor, srcMirrorUsed string) (*image.CopyResult, error) {
	c.logger.Infof("copying full image (all platforms): source=%s destination=%s",
		src.RepoName+":"+src.Tag, dst.RepoName+":"+dst.Tag)

	dstRef, err := c.getDestinationRef(dst)
	if err != nil {
		return c.copyResultError(
			src.RepoName+":"+src.Tag,
			dst.RepoName+":"+dst.Tag,
			fmt.Sprintf("failed to access destination registry: %v", err),
			srcMirrorUsed,
		), nil
	}

	// Check if destination already exists with same content
	if c.checkImageExists(ctx, srcDesc, dstRef, dst.Registry) {
		c.logger.Infof("destination image already exists with identical content, skipping copy: source=%s destination=%s",
			src.RepoName+":"+src.Tag, dst.RepoName+":"+dst.Tag)

		return &image.CopyResult{
			SrcRef:     src.RepoName + ":" + src.Tag,
			DstRef:     dst.RepoName + ":" + dst.Tag,
			Success:    true,
			Skipped:    true,
			Message:    "Image already exists with identical content, skipped",
			UsedMirror: srcMirrorUsed,
		}, nil
	}

	// Perform the copy
	dstMeter := newTrafficMeter()
	dstOpts := c.createRemoteOptionsWithMeter(ctx, dst.Registry, dstMeter)
	finalize := func(result *image.CopyResult) *image.CopyResult {
		if result != nil {
			result.TransferredBytes = dstMeter.bytesTransferred()
		}
		return result
	}

	if srcDesc.MediaType.IsIndex() {
		// Multi-arch image: copy the entire index
		index, err := srcDesc.ImageIndex()
		if err != nil {
			return finalize(&image.CopyResult{
				SrcRef:     src.RepoName + ":" + src.Tag,
				DstRef:     dst.RepoName + ":" + dst.Tag,
				Success:    false,
				Message:    fmt.Sprintf("failed to get image index: %v", err),
				UsedMirror: srcMirrorUsed,
			}), nil
		}

		if err := c.writeIndexWithRetry(dstRef, index, dstOpts); err != nil {
			return finalize(&image.CopyResult{
				SrcRef:     src.RepoName + ":" + src.Tag,
				DstRef:     dst.RepoName + ":" + dst.Tag,
				Success:    false,
				Message:    fmt.Sprintf("failed to write image index: %v", err),
				UsedMirror: srcMirrorUsed,
			}), nil
		}

		digest, _ := index.Digest()
		c.logger.Infof("successfully copied full image index: digest=%s", digest.String())

		return finalize(&image.CopyResult{
			SrcRef:     src.RepoName + ":" + src.Tag,
			DstRef:     dst.RepoName + ":" + dst.Tag,
			Digest:     digest.String(),
			Success:    true,
			Message:    "Full image copied successfully (all platforms)",
			UsedMirror: srcMirrorUsed,
			Platforms:  []string{"all"},
		}), nil
	}

	// Single-arch image
	img, err := srcDesc.Image()
	if err != nil {
		return finalize(&image.CopyResult{
			SrcRef:     src.RepoName + ":" + src.Tag,
			DstRef:     dst.RepoName + ":" + dst.Tag,
			Success:    false,
			Message:    fmt.Sprintf("failed to get image: %v", err),
			UsedMirror: srcMirrorUsed,
		}), nil
	}

	if err := c.writeImageWithRetry(dstRef, img, dstOpts); err != nil {
		return finalize(&image.CopyResult{
			SrcRef:     src.RepoName + ":" + src.Tag,
			DstRef:     dst.RepoName + ":" + dst.Tag,
			Success:    false,
			Message:    fmt.Sprintf("failed to write image: %v", err),
			UsedMirror: srcMirrorUsed,
		}), nil
	}

	digest, _ := img.Digest()
	c.logger.Infof("successfully copied single-arch image: digest=%s", digest.String())

	return finalize(&image.CopyResult{
		SrcRef:     src.RepoName + ":" + src.Tag,
		DstRef:     dst.RepoName + ":" + dst.Tag,
		Digest:     digest.String(),
		Success:    true,
		Message:    "Image copied successfully",
		UsedMirror: srcMirrorUsed,
		Platforms:  []string{"single"},
	}), nil
}

func (c *Client) copyMultiPlatformImage(ctx context.Context, src *image.SrcImage, dst *image.DstImage, srcDesc *remote.Descriptor, platforms []PlatformConfig, srcMirrorUsed string, session *CopySession, sourceCacheKey string) (*image.CopyResult, error) {
	requestedPlatformStrs := make([]string, len(platforms))
	for i, p := range platforms {
		requestedPlatformStrs[i] = platformToString(p)
	}

	c.logger.Infof("copying multi-platform image: source=%s destination=%s platforms=%v",
		src.RepoName+":"+src.Tag, dst.RepoName+":"+dst.Tag, requestedPlatformStrs)

	// Get source image index
	srcIndex, err := srcDesc.ImageIndex()
	if err != nil {
		// Source is not an index, check if it matches any configured platform
		return c.copySingleImageWithPlatformCheck(ctx, src, dst, srcDesc, platforms, srcMirrorUsed)
	}

	srcManifest, err := c.getIndexManifestCached(sourceCacheKey, srcDesc, session)
	if err != nil {
		return &image.CopyResult{
			SrcRef:     src.RepoName + ":" + src.Tag,
			DstRef:     dst.RepoName + ":" + dst.Tag,
			Success:    false,
			Message:    fmt.Sprintf("failed to get index manifest: %v", err),
			UsedMirror: srcMirrorUsed,
		}, nil
	}

	dstRef, err := c.getDestinationRef(dst)
	if err != nil {
		return c.copyResultError(
			src.RepoName+":"+src.Tag,
			dst.RepoName+":"+dst.Tag,
			fmt.Sprintf("failed to access destination registry: %v", err),
			srcMirrorUsed,
		), nil
	}

	dstMeter := newTrafficMeter()
	dstOpts := c.createRemoteOptionsWithMeter(ctx, dst.Registry, dstMeter)
	var platformTransferredBytes int64
	finalize := func(result *image.CopyResult) *image.CopyResult {
		if result != nil {
			result.TransferredBytes = dstMeter.bytesTransferred() + platformTransferredBytes
		}
		return result
	}

	matchingManifests, matchingPlatforms := collectMatchingPlatformManifests(srcManifest, platforms)
	if len(matchingManifests) == 0 {
		platformResults := make([]image.PlatformCopyResult, 0, len(platforms))
		for _, platform := range platformStrs(platforms) {
			platformResults = append(platformResults, image.PlatformCopyResult{
				Platform: platform,
				Status:   "platform_skip",
				Error:    "No matching platforms found in source image",
			})
		}
		return finalize(&image.CopyResult{
			SrcRef:          src.RepoName + ":" + src.Tag,
			DstRef:          dst.RepoName + ":" + dst.Tag,
			Success:         true,
			PlatformSkip:    true,
			Message:         "No matching platforms found in source image",
			UsedMirror:      srcMirrorUsed,
			PlatformResults: platformResults,
		}), nil
	}

	c.logger.Infof("collected matching platform manifests: matched_manifest_count=%d matched_platform_count=%d",
		len(matchingManifests), len(matchingPlatforms))

	dedupedManifests, dedupedPlatforms, duplicateCount := dedupePlatformManifests(matchingManifests, matchingPlatforms)
	if duplicateCount > 0 {
		c.logger.Warnf("deduplicated duplicate platform manifests before index write: duplicate_count=%d matched_manifest_count=%d deduped_manifest_count=%d",
			duplicateCount, len(matchingManifests), len(dedupedManifests))
	}
	matchingManifests = dedupedManifests
	matchingPlatforms = dedupedPlatforms

	_, _, expectedIndexDigest, err := buildImageIndex(matchingManifests)
	if err != nil {
		return finalize(&image.CopyResult{
			SrcRef:     src.RepoName + ":" + src.Tag,
			DstRef:     dst.RepoName + ":" + dst.Tag,
			Success:    false,
			Message:    fmt.Sprintf("failed to build expected image index: %v", err),
			UsedMirror: srcMirrorUsed,
		}), nil
	}

	// Skip only when the destination tag already points to the exact filtered index.
	if matches, platformResults := c.destinationMatchesExpectedIndex(expectedIndexDigest, matchingManifests, matchingPlatforms, dstRef, dstOpts); matches {
		c.logger.Infof("all configured platforms already exist in destination, skipping copy: source=%s destination=%s",
			src.RepoName+":"+src.Tag, dst.RepoName+":"+dst.Tag)

		return finalize(&image.CopyResult{
			SrcRef:          src.RepoName + ":" + src.Tag,
			DstRef:          dst.RepoName + ":" + dst.Tag,
			Success:         true,
			Skipped:         true,
			Message:         "All configured platforms already exist in destination",
			UsedMirror:      srcMirrorUsed,
			Platforms:       requestedPlatformStrs,
			PlatformResults: platformResults,
		}), nil
	}

	platformTaskResults := c.copyPlatformImagesConcurrently(ctx, srcIndex, dstRef, dst.Registry, matchingManifests, matchingPlatforms)
	copiedManifests := make([]v1.Descriptor, 0, len(platformTaskResults))
	copiedPlatforms := make([]string, 0, len(platformTaskResults))
	platformResults := make([]image.PlatformCopyResult, 0, len(platformTaskResults))
	for _, taskResult := range platformTaskResults {
		platformResults = append(platformResults, taskResult.platformResult)
		platformTransferredBytes += taskResult.platformResult.TransferredBytes
		if !taskResult.copied {
			continue
		}
		copiedManifests = append(copiedManifests, taskResult.manifest)
		copiedPlatforms = append(copiedPlatforms, taskResult.platform)
	}

	if len(copiedManifests) == 0 {
		return finalize(&image.CopyResult{
			SrcRef:          src.RepoName + ":" + src.Tag,
			DstRef:          dst.RepoName + ":" + dst.Tag,
			Success:         false,
			Message:         "failed to copy any matching platform image",
			UsedMirror:      srcMirrorUsed,
			PlatformResults: platformResults,
		}), nil
	}

	newIndex, indexBytes, indexDigest, err := buildImageIndex(copiedManifests)
	if err != nil {
		return finalize(&image.CopyResult{
			SrcRef:     src.RepoName + ":" + src.Tag,
			DstRef:     dst.RepoName + ":" + dst.Tag,
			Success:    false,
			Message:    fmt.Sprintf("failed to marshal new index: %v", err),
			UsedMirror: srcMirrorUsed,
		}), nil
	}

	// Write the index with the tag
	indexWritable := &taggableIndex{bytes: indexBytes, manifest: newIndex, mediaType: types.OCIImageIndex}
	if err := c.writeMultiPlatformIndexWithCompatibility(ctx, dstRef, indexWritable, indexDigest, dstOpts); err != nil {
		return finalize(&image.CopyResult{
			SrcRef:          src.RepoName + ":" + src.Tag,
			DstRef:          dst.RepoName + ":" + dst.Tag,
			Success:         false,
			Message:         fmt.Sprintf("failed to tag image index: %v", err),
			UsedMirror:      srcMirrorUsed,
			PlatformResults: platformResults,
		}), nil
	}

	c.logger.Infof("created multi-arch image index: tag=%s platforms=%d digest=%s",
		dstRef.String(), len(copiedManifests), indexDigest.String())

	return finalize(&image.CopyResult{
		SrcRef:          src.RepoName + ":" + src.Tag,
		DstRef:          dst.RepoName + ":" + dst.Tag,
		Digest:          indexDigest.String(),
		Success:         true,
		Message:         fmt.Sprintf("Multi-arch image copied with %d platforms", len(copiedManifests)),
		UsedMirror:      srcMirrorUsed,
		Platforms:       copiedPlatforms,
		PlatformResults: platformResults,
	}), nil
}

func (c *Client) copyPlatformImagesConcurrently(ctx context.Context, srcIndex v1.ImageIndex, dstRef name.Reference, dstRegInfo *image.RegistryInfo, manifests []v1.Descriptor, platforms []string) []platformCopyTaskResult {
	if len(manifests) == 0 {
		return nil
	}

	concurrency := resolvePlatformCopyConcurrency(len(manifests))
	if concurrency <= 1 {
		results := make([]platformCopyTaskResult, len(manifests))
		for i := range manifests {
			results[i] = c.copySinglePlatform(ctx, srcIndex, dstRef, dstRegInfo, manifests[i], platforms[i], i)
		}
		return results
	}

	c.logger.Infof("copying platform images concurrently: platforms=%d concurrency=%d", len(manifests), concurrency)

	type platformTask struct {
		index int
	}

	taskCh := make(chan platformTask, len(manifests))
	resultCh := make(chan platformCopyTaskResult, len(manifests))
	var wg sync.WaitGroup

	for worker := 0; worker < concurrency; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for task := range taskCh {
				resultCh <- c.copySinglePlatform(ctx, srcIndex, dstRef, dstRegInfo, manifests[task.index], platforms[task.index], task.index)
			}
		}()
	}

	for i := range manifests {
		taskCh <- platformTask{index: i}
	}
	close(taskCh)

	go func() {
		wg.Wait()
		close(resultCh)
	}()

	results := make([]platformCopyTaskResult, len(manifests))
	for result := range resultCh {
		results[result.index] = result
	}

	return results
}

func (c *Client) copySinglePlatform(ctx context.Context, srcIndex v1.ImageIndex, dstRef name.Reference, dstRegInfo *image.RegistryInfo, manifest v1.Descriptor, platform string, index int) platformCopyTaskResult {
	result := platformCopyTaskResult{
		index:    index,
		manifest: manifest,
		platform: platform,
	}

	platformStartTime := time.Now()
	platformMeter := newTrafficMeter()
	dstOpts := c.createRemoteOptionsWithMeter(ctx, dstRegInfo, platformMeter)
	finalize := func(result platformCopyTaskResult) platformCopyTaskResult {
		result.platformResult.TransferredBytes = platformMeter.bytesTransferred()
		return result
	}
	maxAttempts := copyRetryAttempts + 1
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		// Check for context cancellation before each attempt
		if err := ctx.Err(); err != nil {
			c.logger.Infof("platform copy cancelled: platform=%s digest=%s error=%v", platform, manifest.Digest.String(), err)
			result.platformResult = platformCopyResult(
				platform,
				"failed",
				platformStartTime,
				fmt.Sprintf("cancelled: %v", err),
			)
			return finalize(result)
		}

		if attempt > 1 {
			delay := retryBackoffDelay(attempt - 1)
			c.logger.Warnf("retrying failed platform copy: platform=%s digest=%s attempt=%d max_attempts=%d backoff=%s",
				platform, manifest.Digest.String(), attempt, maxAttempts, delay)
			select {
			case <-time.After(delay):
			case <-ctx.Done():
				result.platformResult = platformCopyResult(
					platform,
					"failed",
					platformStartTime,
					fmt.Sprintf("cancelled during retry backoff: %v", ctx.Err()),
				)
				return finalize(result)
			}
		}

		img, err := srcIndex.Image(manifest.Digest)
		if err != nil {
			if attempt == maxAttempts {
				c.logger.Warnf("failed to get image for platform: platform=%s digest=%s error=%v",
					platform, manifest.Digest.String(), err)
				result.platformResult = platformCopyResult(
					platform,
					"failed",
					platformStartTime,
					fmt.Sprintf("failed to get image for platform: %v", err),
				)
				return finalize(result)
			}
			continue
		}

		imgDigestRef := dstRef.Context().Digest(manifest.Digest.String())
		if err := remote.Write(imgDigestRef, img, dstOpts...); err != nil {
			if attempt == maxAttempts {
				c.logger.Warnf("failed to write platform image: platform=%s digest=%s error=%v",
					platform, manifest.Digest.String(), err)
				result.platformResult = platformCopyResult(
					platform,
					"failed",
					platformStartTime,
					fmt.Sprintf("failed to write platform image: %v", err),
				)
				return finalize(result)
			}
			continue
		}

		c.logger.Infof("copied platform image: platform=%s digest=%s attempt=%d",
			platform, manifest.Digest.String(), attempt)
		result.platformResult = platformCopyResult(platform, "success", platformStartTime, "")
		result.copied = true
		return finalize(result)
	}
	return finalize(result)
}

func (c *Client) copySingleImageWithPlatformCheck(ctx context.Context, src *image.SrcImage, dst *image.DstImage, srcDesc *remote.Descriptor, platforms []PlatformConfig, srcMirrorUsed string) (*image.CopyResult, error) {
	// Get source image config to check platform
	img, err := srcDesc.Image()
	if err != nil {
		return &image.CopyResult{
			SrcRef:     src.RepoName + ":" + src.Tag,
			DstRef:     dst.RepoName + ":" + dst.Tag,
			Success:    false,
			Message:    fmt.Sprintf("failed to get image: %v", err),
			UsedMirror: srcMirrorUsed,
		}, nil
	}

	config, err := img.ConfigFile()
	if err != nil {
		c.logger.Warnf("could not get image config, proceeding with copy anyway: %v", err)
	} else {
		// Check if the image platform matches any configured platform
		imgPlatform := v1.Platform{
			OS:           config.OS,
			Architecture: config.Architecture,
			Variant:      config.Variant,
		}

		matched := matchesAnyPlatform(imgPlatform, platforms)
		if matched == nil {
			c.logger.Infof("single-arch image does not match any configured platform, skipping: os=%s arch=%s",
				config.OS, config.Architecture)

			return &image.CopyResult{
				SrcRef:       src.RepoName + ":" + src.Tag,
				DstRef:       dst.RepoName + ":" + dst.Tag,
				Success:      true,
				PlatformSkip: true,
				Message:      "Image platform does not match configured platforms",
				UsedMirror:   srcMirrorUsed,
			}, nil
		}
	}

	// Copy the image
	dstRef, err := c.getDestinationRef(dst)
	if err != nil {
		return c.copyResultError(
			src.RepoName+":"+src.Tag,
			dst.RepoName+":"+dst.Tag,
			fmt.Sprintf("failed to access destination registry: %v", err),
			srcMirrorUsed,
		), nil
	}

	dstMeter := newTrafficMeter()
	dstOpts := c.createRemoteOptionsWithMeter(ctx, dst.Registry, dstMeter)
	finalize := func(result *image.CopyResult) *image.CopyResult {
		if result != nil {
			result.TransferredBytes = dstMeter.bytesTransferred()
		}
		return result
	}
	if err := c.writeImageWithRetry(dstRef, img, dstOpts); err != nil {
		return finalize(&image.CopyResult{
			SrcRef:     src.RepoName + ":" + src.Tag,
			DstRef:     dst.RepoName + ":" + dst.Tag,
			Success:    false,
			Message:    fmt.Sprintf("failed to write image: %v", err),
			UsedMirror: srcMirrorUsed,
		}), nil
	}

	digest, _ := img.Digest()
	c.logger.Infof("successfully copied single-arch image: digest=%s", digest.String())

	return finalize(&image.CopyResult{
		SrcRef:     src.RepoName + ":" + src.Tag,
		DstRef:     dst.RepoName + ":" + dst.Tag,
		Digest:     digest.String(),
		Success:    true,
		Message:    "Image copied successfully",
		UsedMirror: srcMirrorUsed,
		Platforms:  platformStrs(platforms),
	}), nil
}

func (c *Client) writeImageWithRetry(dstRef name.Reference, img v1.Image, dstOpts []remote.Option) error {
	maxAttempts := copyRetryAttempts + 1
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if err := remote.Write(dstRef, img, dstOpts...); err != nil {
			if attempt == maxAttempts {
				return err
			}
			delay := retryBackoffDelay(attempt)
			c.logger.Warnf("retrying failed image write: reference=%s attempt=%d max_attempts=%d backoff=%s error=%v",
				dstRef.String(), attempt+1, maxAttempts, delay, err)
			time.Sleep(delay)
			continue
		}
		return nil
	}
	return nil
}

func (c *Client) writeIndexWithRetry(dstRef name.Reference, index v1.ImageIndex, dstOpts []remote.Option) error {
	maxAttempts := copyRetryAttempts + 1
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if err := remote.WriteIndex(dstRef, index, dstOpts...); err != nil {
			if attempt == maxAttempts {
				return err
			}
			delay := retryBackoffDelay(attempt)
			c.logger.Warnf("retrying failed image index write: reference=%s attempt=%d max_attempts=%d backoff=%s error=%v",
				dstRef.String(), attempt+1, maxAttempts, delay, err)
			time.Sleep(delay)
			continue
		}
		return nil
	}
	return nil
}

func (c *Client) tagIndexWithRetry(dstRef name.Tag, index remote.Taggable, dstOpts []remote.Option) error {
	maxAttempts := copyRetryAttempts + 1
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if err := remote.Tag(dstRef, index, dstOpts...); err != nil {
			if attempt == maxAttempts {
				return err
			}
			delay := retryBackoffDelay(attempt)
			c.logger.Warnf("retrying failed image index tag: reference=%s attempt=%d max_attempts=%d backoff=%s error=%v",
				dstRef.String(), attempt+1, maxAttempts, delay, err)
			time.Sleep(delay)
			continue
		}
		return nil
	}
	return nil
}

func (c *Client) checkImageExists(ctx context.Context, srcDesc *remote.Descriptor, dstRef name.Reference, dstRegInfo *image.RegistryInfo) bool {
	dstOpts := c.createRemoteOptions(ctx, dstRegInfo)

	dstDigest, err := c.getDigestForDescriptorCheck(dstRef, dstOpts)
	if err != nil {
		return false
	}

	return srcDesc.Digest == dstDigest
}

func collectMatchingPlatformManifests(srcManifest *v1.IndexManifest, platforms []PlatformConfig) ([]v1.Descriptor, []string) {
	if srcManifest == nil {
		return nil, nil
	}

	var manifests []v1.Descriptor
	var platformNames []string
	for _, manifest := range srcManifest.Manifests {
		if manifest.Platform == nil {
			continue
		}

		matchedCfg := matchesAnyPlatform(*manifest.Platform, platforms)
		if matchedCfg == nil {
			continue
		}

		manifests = append(manifests, manifest)
		platformNames = append(platformNames, platformToString(*matchedCfg))
	}

	return manifests, platformNames
}

func dedupePlatformManifests(manifests []v1.Descriptor, platformNames []string) ([]v1.Descriptor, []string, int) {
	if len(manifests) == 0 || len(platformNames) == 0 {
		return manifests, platformNames, 0
	}

	seen := make(map[string]struct{}, len(manifests))
	dedupedManifests := make([]v1.Descriptor, 0, len(manifests))
	dedupedPlatformNames := make([]string, 0, len(platformNames))
	duplicateCount := 0

	for i, manifest := range manifests {
		platformName := ""
		if i < len(platformNames) {
			platformName = platformNames[i]
		}
		key := platformName + "|" + manifest.Digest.String()
		if _, ok := seen[key]; ok {
			duplicateCount++
			continue
		}
		seen[key] = struct{}{}
		dedupedManifests = append(dedupedManifests, manifest)
		dedupedPlatformNames = append(dedupedPlatformNames, platformName)
	}

	return dedupedManifests, dedupedPlatformNames, duplicateCount
}

func buildImageIndex(manifests []v1.Descriptor) (*v1.IndexManifest, []byte, v1.Hash, error) {
	return buildImageIndexWithMediaType(manifests, types.OCIImageIndex)
}

func buildImageIndexWithMediaType(manifests []v1.Descriptor, mediaType types.MediaType) (*v1.IndexManifest, []byte, v1.Hash, error) {
	if mediaType == "" {
		mediaType = types.OCIImageIndex
	}

	index := &v1.IndexManifest{
		SchemaVersion: 2,
		MediaType:     mediaType,
		Manifests:     append([]v1.Descriptor(nil), manifests...),
	}

	indexBytes, err := json.Marshal(index)
	if err != nil {
		return nil, nil, v1.Hash{}, err
	}

	indexDigest, _, err := v1.SHA256(bytes.NewReader(indexBytes))
	if err != nil {
		return nil, nil, v1.Hash{}, err
	}

	return index, indexBytes, indexDigest, nil
}

func (c *Client) writeMultiPlatformIndexWithCompatibility(ctx context.Context, dstRef name.Tag, index *taggableIndex, indexDigest v1.Hash, dstOpts []remote.Option) error {
	if err := c.tagIndexWithRetry(dstRef, index, dstOpts); err == nil {
		return nil
	} else if !shouldRetryMultiPlatformIndexWithCompatibility(err) {
		return err
	}

	c.logger.Warnf("falling back to compatibility flow for multi-platform index write: reference=%s registry=%s index_digest=%s original_media_type=%s fallback_media_type=%s",
		dstRef.String(), dstRef.RegistryStr(), indexDigest.String(), string(index.mediaType), string(types.DockerManifestList))

	if err := c.writeAndTagIndexByDigest(ctx, dstRef, indexDigest, index, dstOpts); err == nil {
		return nil
	}

	fallbackIndex, fallbackDigest, err := buildTaggableIndexWithMediaType(index.manifest.Manifests, types.DockerManifestList)
	if err != nil {
		return err
	}

	c.logger.Warnf("retrying multi-platform index write with Docker manifest list media type: reference=%s registry=%s index_digest=%s original_media_type=%s fallback_media_type=%s",
		dstRef.String(), dstRef.RegistryStr(), fallbackDigest.String(), string(index.mediaType), string(fallbackIndex.mediaType))

	return c.writeAndTagIndexByDigest(ctx, dstRef, fallbackDigest, fallbackIndex, dstOpts)
}

func buildTaggableIndexWithMediaType(manifests []v1.Descriptor, mediaType types.MediaType) (*taggableIndex, v1.Hash, error) {
	indexManifest, indexBytes, indexDigest, err := buildImageIndexWithMediaType(manifests, mediaType)
	if err != nil {
		return nil, v1.Hash{}, err
	}
	return &taggableIndex{bytes: indexBytes, manifest: indexManifest, mediaType: mediaType}, indexDigest, nil
}

func (c *Client) writeAndTagIndexByDigest(ctx context.Context, dstRef name.Tag, indexDigest v1.Hash, index *taggableIndex, dstOpts []remote.Option) error {
	digestRef := dstRef.Digest(indexDigest.String())
	if err := c.putIndexWithRetry(digestRef, index, dstOpts); err != nil {
		return err
	}
	if err := c.waitForIndexVisibility(ctx, digestRef, indexDigest, dstOpts); err != nil {
		return err
	}
	return c.tagIndexWithRetry(dstRef, index, dstOpts)
}

func (c *Client) putIndexWithRetry(dstRef name.Reference, index remote.Taggable, dstOpts []remote.Option) error {
	maxAttempts := copyRetryAttempts + 1
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if err := remote.Put(dstRef, index, dstOpts...); err != nil {
			if attempt == maxAttempts {
				return err
			}
			delay := retryBackoffDelay(attempt)
			c.logger.Warnf("retrying failed image index manifest write: reference=%s attempt=%d max_attempts=%d backoff=%s error=%v",
				dstRef.String(), attempt+1, maxAttempts, delay, err)
			time.Sleep(delay)
			continue
		}
		return nil
	}
	return nil
}

func (c *Client) waitForIndexVisibility(ctx context.Context, ref name.Reference, expectedDigest v1.Hash, dstOpts []remote.Option) error {
	maxAttempts := copyRetryAttempts + 2
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}

		gotDigest, err := c.getDigestForDescriptorCheck(ref, dstOpts)
		if err == nil && gotDigest == expectedDigest {
			return nil
		}

		if attempt == maxAttempts {
			if err != nil {
				return fmt.Errorf("index digest %s not visible after write: %w", expectedDigest.String(), err)
			}
			return fmt.Errorf("index digest %s not visible after write: got %s", expectedDigest.String(), gotDigest.String())
		}

		delay := retryBackoffDelay(attempt)
		c.logger.Warnf("waiting for image index visibility in destination registry: reference=%s expected_digest=%s attempt=%d max_attempts=%d error=%v",
			ref.String(), expectedDigest.String(), attempt+1, maxAttempts, err)

		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

func shouldRetryMultiPlatformIndexWithCompatibility(err error) bool {
	if err == nil {
		return false
	}
	if te, ok := err.(*transport.Error); ok {
		if te.StatusCode == http.StatusUnauthorized || te.StatusCode == http.StatusForbidden {
			return false
		}
	}

	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "artifact") && strings.Contains(msg, "not found") {
		return true
	}
	if strings.Contains(msg, "manifest unknown") {
		return true
	}
	return strings.Contains(msg, "tag") && strings.Contains(msg, "not found")
}

func (c *Client) destinationMatchesExpectedIndex(expectedDigest v1.Hash, manifests []v1.Descriptor, platformNames []string, dstRef name.Reference, dstOpts []remote.Option) (bool, []image.PlatformCopyResult) {
	dstDigest, err := c.getDigestForDescriptorCheck(dstRef, dstOpts)
	if err != nil {
		return false, nil
	}

	if dstDigest != expectedDigest {
		c.logger.Debugf("destination tag points to a different digest: expected=%s actual=%s",
			expectedDigest.String(), dstDigest.String())
		return false, nil
	}

	platformResults := make([]image.PlatformCopyResult, 0, len(manifests))
	for i, manifest := range manifests {
		imgDigestRef := dstRef.Context().Digest(manifest.Digest.String())
		startTime := time.Now()
		if _, err := remote.Head(imgDigestRef, dstOpts...); err != nil {
			c.logger.Debugf("expected platform manifest not found in destination: digest=%s error=%v",
				manifest.Digest.String(), err)
			return false, nil
		}
		platformResults = append(platformResults, platformCopyResultWithTimes(
			platformNames[i],
			"skipped",
			startTime,
			time.Now(),
			"",
		))
	}

	return true, platformResults
}

func (c *Client) getDigestForDescriptorCheck(ref name.Reference, opts []remote.Option) (v1.Hash, error) {
	desc, err := remote.Head(ref, opts...)
	if !shouldFallbackToGetAfterHeadError(err) {
		if err != nil {
			return v1.Hash{}, err
		}
		return desc.Digest, nil
	}

	c.logger.Debugf("HEAD check failed, falling back to GET for descriptor lookup: reference=%s error=%v",
		ref.String(), err)
	fullDesc, err := remote.Get(ref, opts...)
	if err != nil {
		return v1.Hash{}, err
	}
	return fullDesc.Digest, nil
}

func (t *taggableIndex) RawManifest() ([]byte, error) {
	return t.bytes, nil
}

func (t *taggableIndex) MediaType() (types.MediaType, error) {
	if t.mediaType != "" {
		return t.mediaType, nil
	}
	return types.OCIImageIndex, nil
}

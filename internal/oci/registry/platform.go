package registry

import (
	"fmt"
	"sort"
	"strings"
	"time"

	v1 "github.com/google/go-containerregistry/pkg/v1"

	"tinysync/internal/oci/image"
)

func platformCopyResult(platform, status string, startTime time.Time, errMsg string) image.PlatformCopyResult {
	endTime := time.Now()
	return image.PlatformCopyResult{
		Platform:   platform,
		Status:     status,
		DurationMs: endTime.Sub(startTime).Milliseconds(),
		StartTime:  startTime.Format(time.RFC3339),
		EndTime:    endTime.Format(time.RFC3339),
		Error:      errMsg,
	}
}

func platformCopyResultWithTimes(platform, status string, startTime, endTime time.Time, errMsg string) image.PlatformCopyResult {
	return image.PlatformCopyResult{
		Platform:   platform,
		Status:     status,
		DurationMs: endTime.Sub(startTime).Milliseconds(),
		StartTime:  startTime.Format(time.RFC3339),
		EndTime:    endTime.Format(time.RFC3339),
		Error:      errMsg,
	}
}

func uniquePlatforms(platforms []image.PlatformInfo) []image.PlatformInfo {
	seen := make(map[string]struct{}, len(platforms))
	result := make([]image.PlatformInfo, 0, len(platforms))
	for _, platform := range platforms {
		key := platform.OS + "/" + platform.Architecture + "/" + platform.Variant
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, platform)
	}
	return result
}

func sortPlatforms(platforms []image.PlatformInfo) {
	sort.Slice(platforms, func(i, j int) bool {
		if platforms[i].OS != platforms[j].OS {
			return platforms[i].OS < platforms[j].OS
		}
		if platforms[i].Architecture != platforms[j].Architecture {
			return platforms[i].Architecture < platforms[j].Architecture
		}
		return platforms[i].Variant < platforms[j].Variant
	})
}

func singlePlatform(configFile *v1.ConfigFile) []image.PlatformInfo {
	if configFile == nil {
		return nil
	}
	platforms := []image.PlatformInfo{{
		OS:           configFile.OS,
		Architecture: configFile.Architecture,
		Variant:      configFile.Variant,
	}}
	sortPlatforms(platforms)
	return platforms
}

func indexPlatforms(manifest *v1.IndexManifest) []image.PlatformInfo {
	if manifest == nil {
		return nil
	}

	platforms := make([]image.PlatformInfo, 0, len(manifest.Manifests))
	for _, entry := range manifest.Manifests {
		if entry.Platform == nil {
			continue
		}
		platforms = append(platforms, image.PlatformInfo{
			OS:           entry.Platform.OS,
			Architecture: entry.Platform.Architecture,
			Variant:      entry.Platform.Variant,
		})
	}

	platforms = uniquePlatforms(platforms)
	sortPlatforms(platforms)
	return platforms
}

func normalizePlatformValue(value string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	if value == "unknown" {
		return ""
	}
	return value
}

func normalizePlatformInfo(platform image.PlatformInfo) image.PlatformInfo {
	return image.PlatformInfo{
		OS:           normalizePlatformValue(platform.OS),
		Architecture: normalizePlatformValue(platform.Architecture),
		Variant:      normalizePlatformValue(platform.Variant),
	}
}

func normalizePlatformConfigs(platforms []PlatformConfig) []PlatformConfig {
	if len(platforms) == 0 {
		return nil
	}

	result := make([]PlatformConfig, len(platforms))
	for i, platform := range platforms {
		result[i] = PlatformConfig{
			OS:           normalizePlatformValue(platform.OS),
			Architecture: normalizePlatformValue(platform.Architecture),
			Variant:      normalizePlatformValue(platform.Variant),
		}
		if result[i].OS == "" {
			result[i].OS = "linux"
		}
	}

	return result
}

// resolveCopyPlatforms 只使用任务级平台配置（Task 是同步行为的完整定义，
// 不再隐式继承全局配置）；未配置则同步全部平台。
func resolveCopyPlatforms(taskPlatforms []PlatformConfig) []PlatformConfig {
	return normalizePlatformConfigs(taskPlatforms)
}

func resolvePlatformCopyConcurrency(platformCount int) int {
	if platformCount <= 1 {
		return platformCount
	}
	if platformCount < defaultPlatformCopyConcurrency {
		return platformCount
	}
	return defaultPlatformCopyConcurrency
}

func collectPlatformsFromIndexManifest(srcManifest *v1.IndexManifest) []PlatformConfig {
	if srcManifest == nil {
		return nil
	}

	platforms := make([]PlatformConfig, 0, len(srcManifest.Manifests))
	seen := make(map[string]struct{}, len(srcManifest.Manifests))
	for _, manifest := range srcManifest.Manifests {
		if manifest.Platform == nil {
			continue
		}

		platform := PlatformConfig{
			OS:           normalizePlatformValue(manifest.Platform.OS),
			Architecture: normalizePlatformValue(manifest.Platform.Architecture),
			Variant:      normalizePlatformValue(manifest.Platform.Variant),
		}
		if platform.OS == "" {
			platform.OS = "linux"
		}

		key := platformToString(platform)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		platforms = append(platforms, platform)
	}

	return platforms
}

func platformMatches(platform *v1.Platform, target image.PlatformInfo) bool {
	if platform == nil {
		return false
	}

	normalizedPlatform := normalizePlatformInfo(image.PlatformInfo{
		OS:           platform.OS,
		Architecture: platform.Architecture,
		Variant:      platform.Variant,
	})

	return normalizedPlatform.OS == target.OS &&
		normalizedPlatform.Architecture == target.Architecture &&
		normalizedPlatform.Variant == target.Variant
}

func findPlatformManifest(manifest *v1.IndexManifest, target image.PlatformInfo) *v1.Descriptor {
	if manifest == nil {
		return nil
	}

	normalizedTarget := normalizePlatformInfo(target)
	for _, entry := range manifest.Manifests {
		if platformMatches(entry.Platform, normalizedTarget) {
			descriptor := entry
			return &descriptor
		}
	}

	return nil
}

func matchesAnyPlatform(p v1.Platform, platforms []PlatformConfig) *PlatformConfig {
	for _, cfg := range platforms {
		if p.OS == cfg.OS && p.Architecture == cfg.Architecture {
			// Check variant if configured
			if cfg.Variant != "" && p.Variant != cfg.Variant {
				continue
			}
			return &cfg
		}
	}
	return nil
}

func platformToString(p PlatformConfig) string {
	s := fmt.Sprintf("%s/%s", p.OS, p.Architecture)
	if p.Variant != "" {
		s += "/" + p.Variant
	}
	return s
}

func platformStrs(platforms []PlatformConfig) []string {
	result := make([]string, len(platforms))
	for i, p := range platforms {
		result[i] = platformToString(p)
	}
	return result
}

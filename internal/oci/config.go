package oci

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/go-containerregistry/pkg/name"

	"tinysync/internal/job"
	"tinysync/internal/oci/image"
	"tinysync/internal/oci/registry"
	"tinysync/internal/oci/task"
)

// 校验常量（oci-sync control/validation.go 移植）。
const (
	MaxSrcImageLength  = 512
	MaxDstServerLength = 128
)

// ParseTaskConfig 解析 oci 任务的 config_json；nil / 空串视为空对象。
// 只做形状解码，取值合法性由 ValidateSrcImages / NormalizePlatforms 与
// 执行器的 Validate 入口校验。
func ParseTaskConfig(raw json.RawMessage) (task.TaskConfig, error) {
	var cfg task.TaskConfig
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return cfg, nil
	}
	if err := json.Unmarshal([]byte(trimmed), &cfg); err != nil {
		return task.TaskConfig{}, fmt.Errorf("%w: oci config is not valid JSON: %v", job.ErrInvalid, err)
	}
	return cfg, nil
}

// MarshalTaskConfig 序列化归一后的落库配置。
func MarshalTaskConfig(cfg task.TaskConfig) (json.RawMessage, error) {
	data, err := json.Marshal(cfg)
	if err != nil {
		return nil, fmt.Errorf("marshal oci config: %w", err)
	}
	return json.RawMessage(data), nil
}

// ValidateSrcImages 校验源镜像列表的每一项格式（oci-sync
// control/validation.go 的 validateSrcImages 移植）：非空、长度上限、
// 非 URL、逗号分隔的标签段无空项、可展开为合法 tag 引用且无重复。
func ValidateSrcImages(srcImages []string) error {
	seen := make(map[string]bool)
	for i, img := range srcImages {
		if strings.TrimSpace(img) == "" {
			return fmt.Errorf("%w: src_images[%d] is empty", job.ErrInvalid, i)
		}
		if len(img) > MaxSrcImageLength {
			return fmt.Errorf("%w: src_images[%d] exceeds maximum length of %d characters", job.ErrInvalid, i, MaxSrcImageLength)
		}
		if strings.Contains(img, "://") {
			return fmt.Errorf("%w: src_images[%d] must be an image reference, not a URL", job.ErrInvalid, i)
		}
		// Validate the first raw reference too: expansion defaults missing tags
		// to latest and skips empty comma entries, which must not hide typos.
		parts := strings.Split(img, ",")
		for _, part := range parts {
			if strings.TrimSpace(part) == "" {
				return fmt.Errorf("%w: src_images[%d] has an empty comma-separated tag", job.ErrInvalid, i)
			}
		}
		if _, err := name.NewTag(strings.TrimSpace(parts[0])); err != nil {
			return fmt.Errorf("%w: src_images[%d] has invalid image format: %v", job.ErrInvalid, i, err)
		}
		for _, ref := range image.ExpandSrcImages([]string{img}) {
			parsed, err := name.NewTag(ref.Repo + ":" + ref.Tag)
			if err != nil {
				return fmt.Errorf("%w: src_images[%d] has invalid image format: %v", job.ErrInvalid, i, err)
			}
			if seen[parsed.Name()] {
				return fmt.Errorf("%w: src_images[%d] duplicates source image %q", job.ErrInvalid, i, parsed.Name())
			}
			seen[parsed.Name()] = true
		}
	}
	return nil
}

// ExpandSrcImages 展开逗号分隔的标签并校验每个展开结果（task 执行器
// parseSrcImages 的镜像；返回归一化后的 repo:tag 列表）。
func ExpandSrcImages(srcImages []string) ([]string, error) {
	refs := image.ExpandSrcImages(srcImages)
	images := make([]string, 0, len(refs))
	for _, ref := range refs {
		value := ref.Repo + ":" + ref.Tag
		if _, err := name.NewTag(value); err != nil {
			return nil, fmt.Errorf("%w: invalid source image reference %q", job.ErrInvalid, value)
		}
		images = append(images, value)
	}
	return images, nil
}

// NormalizePlatforms 校验并规范化平台过滤配置（oci-sync
// control/validation.go 的 normalizePlatforms 移植）：platforms 非空时
// 逐条要求 architecture 必填（否则配置错误要到执行时才以 platform_skip
// 暴露）；os 为空时补全为 linux；三字段统一 TrimSpace。传空数组表示
// 清除平台过滤（同步全部平台），返回 nil。
func NormalizePlatforms(platforms []registry.PlatformConfig) ([]registry.PlatformConfig, error) {
	if len(platforms) == 0 {
		return nil, nil
	}
	normalized := make([]registry.PlatformConfig, 0, len(platforms))
	for i, p := range platforms {
		architecture := strings.TrimSpace(p.Architecture)
		if architecture == "" {
			return nil, fmt.Errorf("%w: platforms[%d]: architecture is required when platforms is set", job.ErrInvalid, i)
		}
		osName := strings.TrimSpace(p.OS)
		if osName == "" {
			osName = "linux"
		}
		normalized = append(normalized, registry.PlatformConfig{
			OS:           osName,
			Architecture: architecture,
			Variant:      strings.TrimSpace(p.Variant),
		})
	}
	return normalized, nil
}

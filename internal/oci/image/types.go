package image

import "strings"

// RemoteInspectResult contains the inspection results of a remote image
type RemoteInspectResult struct {
	Ref             string          `json:"ref"`                        // 实际 inspect 的镜像引用
	Tag             string          `json:"tag"`                        // 当前 inspect 的 tag
	Mirror          string          `json:"mirror"`                     // 命中的 mirror 地址
	Digest          string          `json:"digest,omitempty"`           // 远程描述符摘要
	MediaType       string          `json:"media_type,omitempty"`       // OCI media type
	Size            int64           `json:"size,omitempty"`             // 镜像大小（字节）
	IsIndex         bool            `json:"is_index"`                   // 是否为多架构镜像索引
	Manifest        interface{}     `json:"manifest,omitempty"`         // manifest 内容（JSON）
	Config          interface{}     `json:"config,omitempty"`           // 配置信息
	Platforms       []PlatformInfo  `json:"platforms,omitempty"`        // 平台摘要
	PlatformsStatus map[string]bool `json:"platforms_status,omitempty"` // 平台存在状态
}

// PlatformInfo 表示镜像支持的平台摘要
type PlatformInfo struct {
	OS           string `json:"os,omitempty"`           // 操作系统
	Architecture string `json:"architecture,omitempty"` // 架构
	Variant      string `json:"variant,omitempty"`      // 变体
}

// SrcImage represents the source image to inspect
type SrcImage struct {
	Registry  *RegistryInfo `json:"registry,omitempty"`
	Namespace string        `json:"namespace,omitempty"`
	RepoName  string        `json:"repo_name"`
	Tag       string        `json:"tag"`
}

// DstImage represents the destination image to copy to
type DstImage struct {
	Registry  *RegistryInfo `json:"registry,omitempty"`
	Namespace string        `json:"namespace,omitempty"`
	RepoName  string        `json:"repo_name"`
	Tag       string        `json:"tag"`
}

// CopyResult contains the results of an image copy operation
type CopyResult struct {
	SrcRef           string               `json:"src_ref"`
	DstRef           string               `json:"dst_ref"`
	Digest           string               `json:"digest"`
	Size             int64                `json:"size"`
	TransferredBytes int64                `json:"transferred_bytes,omitempty"`
	Success          bool                 `json:"success"`
	Skipped          bool                 `json:"skipped"`
	PlatformSkip     bool                 `json:"platform_skip,omitempty"` // 因 platform 不存在而跳过
	Message          string               `json:"message,omitempty"`
	UsedMirror       string               `json:"used_mirror,omitempty"`
	Platforms        []string             `json:"platforms,omitempty"`        // 复制的平台列表
	PlatformResults  []PlatformCopyResult `json:"platform_results,omitempty"` // 平台级结果
}

// PlatformCopyResult contains the result of a single platform within a copy.
type PlatformCopyResult struct {
	Platform         string `json:"platform"`
	Status           string `json:"status"` // success, failed, skipped, platform_skip
	DurationMs       int64  `json:"duration_ms"`
	TransferredBytes int64  `json:"transferred_bytes,omitempty"`
	StartTime        string `json:"start_time,omitempty"`
	EndTime          string `json:"end_time,omitempty"`
	Error            string `json:"error,omitempty"`
}

// RegistryInfo contains registry authentication information
type RegistryInfo struct {
	Username           string `json:"username"`
	Password           string `json:"password"`
	PlainHTTP          bool   `json:"plain_http"`           // 是否使用纯 HTTP（无 TLS）
	InsecureSkipVerify bool   `json:"insecure_skip_verify"` // 是否跳过 TLS 证书验证（默认验证）
}

// ImageRef 表示解析后的镜像引用
type ImageRef struct {
	Repo string // 仓库路径（不含 registry），如 library/nginx, lscr.io/linuxserver/onlyoffice
	Tag  string // 标签
}

// ExpandSrcImages 解析源镜像列表，展开逗号分隔的标签
// 输入: ["nginx:1.24,1.25", "alpine:3.18"]
// 输出: [{Repo: "library/nginx", Tag: "1.24"}, {Repo: "library/nginx", Tag: "1.25"}, ...]
func ExpandSrcImages(srcImages []string) []ImageRef {
	var refs []ImageRef

	for _, imageEntry := range srcImages {
		imageEntry = strings.TrimSpace(imageEntry)
		if imageEntry == "" {
			continue
		}

		// 解析仓库名和标签
		var baseName, tagsPart string
		if idx := strings.LastIndex(imageEntry, ":"); idx > 0 {
			beforeColon := imageEntry[:idx]
			afterColon := imageEntry[idx+1:]
			if strings.Contains(beforeColon, "/") {
				// : 前有 /，: 一定是标签分隔符（如 valkey/valkey:8）
				baseName = beforeColon
				tagsPart = afterColon
			} else if strings.Contains(afterColon, "/") {
				// : 前无 / 但 : 后有 /，是 registry:port/path 格式（如 localhost:5000/myimage）
				baseName = imageEntry
				tagsPart = ""
			} else {
				// : 前后均无 /，是简单的 image:tag 格式（如 alpine:3.23.3）
				baseName = beforeColon
				tagsPart = afterColon
			}
		} else {
			baseName = imageEntry
		}

		// 如果没有指定标签，使用 latest
		if tagsPart == "" {
			refs = append(refs, ImageRef{Repo: normalizeRepo(baseName), Tag: "latest"})
			continue
		}

		// 展开逗号分隔的标签
		tags := strings.Split(tagsPart, ",")
		for _, tag := range tags {
			tag = strings.TrimSpace(tag)
			if tag != "" {
				refs = append(refs, ImageRef{Repo: normalizeRepo(baseName), Tag: tag})
			}
		}
	}

	return refs
}

// normalizeRepo 规范化仓库名，为没有命名空间的镜像添加 library/ 前缀
func normalizeRepo(repo string) string {
	// 如果已经包含 /，说明有命名空间或 registry，直接返回
	if strings.Contains(repo, "/") {
		return repo
	}
	// docker.io 官方镜像需要添加 library/ 前缀
	return "library/" + repo
}

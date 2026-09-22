package source

import (
	"context"
	"time"
)

// InspectAssetDetailLimit 是预览请求展开 Asset 明细的版本数上限：
// 预览面向「配置是否过宽」的判断，全部版本的明细对 all 策略会放大
// 为上千次 API 请求；只有最近版本（发布时间降序前 N 个）展开明细，
// 其余版本仅给出版本级概览。20 覆盖 recent 策略的合理上限。
const InspectAssetDetailLimit = 20

// InspectedRelease 是预览结果的版本概览条目。
type InspectedRelease struct {
	// Tag 是 Release Tag（展示用；逻辑路径以编码目录名为准）。
	Tag string `json:"tag"`
	// Name 是 Release 的显示名（GitHub 侧可为空）。
	Name string `json:"name"`
	// Prerelease 标记预发布（已按 Source 配置决定是否入选）。
	Prerelease bool `json:"prerelease"`
	// PublishedAt 是发布时间（UTC 零值表示不可选）。
	PublishedAt time.Time `json:"published_at"`
	// Assets 是 Asset 明细；仅发布时间最近的 InspectAssetDetailLimit
	// 个版本展开（nil 表示未展开，非空即该版本全部已上传 Asset）。
	Assets []InspectedAsset `json:"assets,omitempty"`
}

// InspectedAsset 是预览结果的单个 Asset 概览。
type InspectedAsset struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
	// DigestAvailable 标记 GitHub 是否提供该 Asset 的内容摘要
	// （决定 SHA-256 校验策略的实际效力）。
	DigestAvailable bool `json:"digest_available"`
}

// GitHubInspection 是 GitHub Release Source 的预览结果：仓库名称与
// 选中版本概览（协议命名保留于领域包，与 GitHubReleaseConfig 同构）。
type GitHubInspection struct {
	// Repository 是 GitHub 侧的全名（owner/repo）。
	Repository string             `json:"repository"`
	Releases   []InspectedRelease `json:"releases"`
}

// ReleaseInspector 是 Remote 可选的「创建前预览」能力：在不持久化
// 配置的前提下执行一次版本发现与 Asset 概览，供 WebUI 测试并预览。
// 由 Service.Inspect 按需断言，未实现的协议自然返回不支持。
type ReleaseInspector interface {
	// Inspect 执行预览发现。assetDetail 指定展开 Asset 明细的版本数
	// 上限（从发布时间最近的版本起；0 展开全部）。任何发现失败都
	// 整体失败——预览不产出部分结果。
	Inspect(ctx context.Context, assetDetail int) (*GitHubInspection, error)
}

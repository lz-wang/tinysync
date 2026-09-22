package githubrelease

import (
	"context"
	"fmt"

	"tinysync/internal/source"
)

// 编译期断言：预览能力。
var _ source.ReleaseInspector = (*remote)(nil)

// Inspect 实现 source.ReleaseInspector：执行一次版本发现与 Asset
// 概览（不持久化，供「测试并预览」）。返回仓库名称与全部选中版本；
// Asset 明细只展开发布时间最近的 assetDetail 个版本，其余版本仅给
// 版本级概览，控制预览请求量。任何发现失败整体失败——预览不产出
// 部分结果。
func (r *remote) Inspect(ctx context.Context, assetDetail int) (*source.GitHubInspection, error) {
	fullName, err := r.client.verifyRepo(ctx)
	if err != nil {
		return nil, err
	}
	releases, err := r.client.selectReleases(ctx, r.cfg)
	if err != nil {
		return nil, err
	}
	out := &source.GitHubInspection{
		Repository: fullName,
		Releases:   make([]source.InspectedRelease, 0, len(releases)),
	}
	for i, rel := range releases {
		item := source.InspectedRelease{
			Tag:         rel.TagName,
			Name:        rel.Name,
			Prerelease:  rel.Prerelease,
			PublishedAt: rel.publishedAt().UTC(),
		}
		if assetDetail <= 0 || i < assetDetail {
			assets, err := r.releaseAssets(ctx, rel.ID)
			if err != nil {
				return nil, fmt.Errorf("github: inspect assets of release %d: %w", rel.ID, err)
			}
			item.Assets = make([]source.InspectedAsset, 0, len(assets))
			for _, a := range assets {
				item.Assets = append(item.Assets, source.InspectedAsset{
					Name:            a.Name,
					Size:            a.Size,
					DigestAvailable: a.Digest != "",
				})
			}
		}
		out.Releases = append(out.Releases, item)
	}
	return out, nil
}

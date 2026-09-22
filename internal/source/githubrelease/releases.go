package githubrelease

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"time"

	"tinysync/internal/source"
)

// maxReleases 是完整枚举的 Release 数量上限：超过即整体失败，绝不
// 截断后进入 Mirror 删除授权（契约见设计文档 §2）。
const maxReleases = source.MaxGitHubRecentCount

// Release 是 GitHub Release 的元数据子集。
type Release struct {
	ID      int64  `json:"id"`
	TagName string `json:"tag_name"`
	Name    string `json:"name"`
	Draft   bool   `json:"draft"`
	// Prerelease 标记 GitHub 侧的预发布状态；纳入与否由 Source 配置
	// 的 include_prereleases 决定（latest 策略不受其影响）。
	Prerelease bool `json:"prerelease"`
	// PublishedAt 是发布时间；recent 策略的排序依据。nil（防御性，
	// 理论上只出现在 draft 上）表示不可选。
	PublishedAt *time.Time `json:"published_at"`
}

// publishedAt 归一 Release 的发布时间；缺失视为零值（不可选）。
func (r Release) publishedAt() time.Time {
	if r.PublishedAt == nil {
		return time.Time{}
	}
	return *r.PublishedAt
}

// selectable 判断 Release 是否符合入选条件：草稿一律排除（对匿名
// 请求不可见且随时可能消失）；预发布按配置开关；无发布时间防御性
// 排除。
func (r Release) selectable(includePrereleases bool) bool {
	if r.Draft || r.publishedAt().IsZero() {
		return false
	}
	if r.Prerelease && !includePrereleases {
		return false
	}
	return true
}

// selectReleases 按 Source 配置的版本选择策略发现 Release。Asset 不
// 在此阶段枚举（快照阶段按 Release ID 单独分页拉取）。任何枚举失败
// 都整体失败。
func (c *client) selectReleases(ctx context.Context, cfg source.GitHubReleaseConfig) ([]Release, error) {
	switch cfg.ReleasePolicy {
	case source.ReleaseLatest:
		return c.latestRelease(ctx)
	case source.ReleaseTag:
		return c.releaseByTag(ctx, cfg.Tag, cfg.IncludePrereleases)
	case source.ReleaseRecent:
		return c.recentReleases(ctx, cfg.RecentCount, cfg.IncludePrereleases)
	case source.ReleaseAll:
		return c.allReleases(ctx, cfg.IncludePrereleases)
	default:
		return nil, fmt.Errorf("%w: unsupported github_release release_policy %q", source.ErrInvalid, cfg.ReleasePolicy)
	}
}

// latestRelease 使用 GitHub 的 latest 端点：按 GitHub 自身规则选择
// 非 prerelease、非 draft 的 Release，不假定其为发布时间最近或版本
// 号最大（契约见设计文档 §2）。仓库尚无合格 Release 时 404（permanent）。
func (c *client) latestRelease(ctx context.Context) ([]Release, error) {
	var r Release
	if _, err := c.getJSON(ctx, c.apiPath("/releases/latest"), &r); err != nil {
		return nil, fmt.Errorf("github: latest release: %w", err)
	}
	return []Release{r}, nil
}

// releaseByTag 精确匹配一个 Release Tag；404 表示该 Tag 无 Release，
// 属确定性失败（permanent）。命中结果同样执行入选过滤：draft 一律
// 拒绝、prerelease 按配置开关、无 published_at 防御性拒绝——显式
// 指定的 Tag 不豁免契约（「草稿一律不参与同步；预发布默认排除」，
// 设计文档 §2）。Tag 含斜杠等特殊字符时经 path escape 进入 API path。
func (c *client) releaseByTag(ctx context.Context, tag string, includePrereleases bool) ([]Release, error) {
	var r Release
	path := c.apiPath("/releases/tags/" + url.PathEscape(tag))
	if _, err := c.getJSON(ctx, path, &r); err != nil {
		return nil, fmt.Errorf("github: release by tag %q: %w", tag, err)
	}
	if !r.selectable(includePrereleases) {
		return nil, fmt.Errorf("%w: release tag %q is not selectable (draft, prerelease without include_prereleases, or missing published_at)", source.ErrInvalid, tag)
	}
	return []Release{r}, nil
}

// recentReleases 完整分页枚举候选 Release 后按 published_at 降序排序
// 取前 N：不依赖 API 返回顺序（GitHub 的列表排序不是发布时间的契约），
// 也不截断首页（契约见设计文档 §2）。
func (c *client) recentReleases(ctx context.Context, count int, includePrereleases bool) ([]Release, error) {
	all, err := c.listSelectableReleases(ctx, includePrereleases)
	if err != nil {
		return nil, err
	}
	if count > len(all) {
		count = len(all)
	}
	return all[:count], nil
}

// allReleases 枚举全部符合条件的已发布 Release。
func (c *client) allReleases(ctx context.Context, includePrereleases bool) ([]Release, error) {
	return c.listSelectableReleases(ctx, includePrereleases)
}

// listSelectableReleases 完整分页枚举并过滤入选 Release，按
// published_at 降序稳定排序；枚举总量超上限即整体失败。
func (c *client) listSelectableReleases(ctx context.Context, includePrereleases bool) ([]Release, error) {
	all, err := listAll[Release](ctx, c, c.apiPath(fmt.Sprintf("/releases?per_page=%d", pageSize)))
	if err != nil {
		return nil, fmt.Errorf("github: list releases: %w", err)
	}
	if len(all) > maxReleases {
		return nil, fmt.Errorf("%w: repository %s/%s has %d releases, exceeding scan limit %d; refusing to truncate",
			source.ErrInvalid, c.owner, c.repo, len(all), maxReleases)
	}
	selected := make([]Release, 0, len(all))
	for _, r := range all {
		if r.selectable(includePrereleases) {
			selected = append(selected, r)
		}
	}
	sort.Slice(selected, func(i, j int) bool {
		pi, pj := selected[i].publishedAt(), selected[j].publishedAt()
		if !pi.Equal(pj) {
			return pi.After(pj)
		}
		// 同秒发布的稳定性兜底：按 Release ID 降序。
		return selected[i].ID > selected[j].ID
	})
	return selected, nil
}

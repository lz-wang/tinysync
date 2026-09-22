package githubrelease

import (
	"context"
	"fmt"

	"tinysync/internal/source"
)

// Factory 实现 source.RemoteFactory，按 Source 配置构造 GitHub
// Release 客户端，经 RemoteRegistry 注册至统一协议分发。
type Factory struct {
	// newClientAt 构造指向指定 API base 的客户端；nil 用生产实现
	// （固定 api.github.com）。测试经它注入 httptest 假 GitHub，与
	// S3 adapter 的 API 注入口同构。
	newClientAt func(baseURL, token, owner, repo string) *client
}

// NewFactory 构造 GitHub Release RemoteFactory。
func NewFactory() *Factory {
	return &Factory{}
}

// Type 实现 source.RemoteFactory：本 factory 服务 github_release 类型。
func (f *Factory) Type() source.Type {
	return source.TypeGitHubRelease
}

// Create 实现 source.RemoteFactory：解析 repository（owner/repo 或
// github.com 仓库 URL）、以 Token（可匿名）构造客户端并验证仓库访问
// 权限——权限不足 / 仓库不存在（GitHub 均返回 404）在创建阶段即暴露，
// 连接测试与同步扫描共用此路径，不会把失败推迟到第一轮下载。
func (f *Factory) Create(ctx context.Context, s source.Source, credentials source.Credentials) (source.Remote, error) {
	if s.Type != source.TypeGitHubRelease || s.Config.GitHubRelease == nil {
		return nil, fmt.Errorf("%w: %q", source.ErrUnsupportedType, s.Type)
	}
	cfg := *s.Config.GitHubRelease
	owner, repo, err := source.ParseGitHubRepository(cfg.Repository)
	if err != nil {
		return nil, err
	}
	token := ""
	if credentials.GitHubRelease != nil {
		token = credentials.GitHubRelease.Token
	}
	newClientAt := f.newClientAt
	if newClientAt == nil {
		newClientAt = newClientAtDefault
	}
	c := newClientAt(defaultAPIBaseURL, token, owner, repo)
	if err := c.verifyRepo(ctx); err != nil {
		return nil, fmt.Errorf("verify github repository %s/%s: %w", owner, repo, err)
	}
	return &remote{client: c, cfg: cfg}, nil
}

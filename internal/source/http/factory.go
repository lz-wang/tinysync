package http

import (
	"context"
	"fmt"

	"tinysync/internal/source"
)

// Factory 实现 source.RemoteFactory。HTTP 无持久会话：Create 只冻结
// 配置与认证的值副本并构造 Client（连接身份自创建起不可变），首个
// 操作才发起网络请求。
type Factory struct{}

// NewFactory 构造 HTTP RemoteFactory。
func NewFactory() *Factory {
	return &Factory{}
}

// Type 实现 source.RemoteFactory：本 factory 服务 http 类型。
func (f *Factory) Type() source.Type {
	return source.TypeHTTP
}

// Create 实现 source.RemoteFactory：类型 / 配置 / 凭据与认证方式的
// 一致性在边界复检（绕过 Service 直连 Factory 的路径同样 fail-closed），
// 配置与 secret 冻结为值副本。
func (f *Factory) Create(ctx context.Context, s source.Source, credentials source.Credentials) (source.Remote, error) {
	if s.Type != source.TypeHTTP || s.Config.HTTP == nil {
		return nil, fmt.Errorf("%w: %q", source.ErrUnsupportedType, s.Type)
	}
	cfg := *s.Config.HTTP
	// 防御性归一（持久化层已保证 canonical form；直连路径同样稳定）。
	if cfg.ListingMode == "" {
		cfg.ListingMode = source.HTTPListingAuto
	}
	if cfg.AuthMethod == "" {
		cfg.AuthMethod = source.HTTPAuthNone
	}
	if cfg.CaddyFileLimit == 0 {
		cfg.CaddyFileLimit = source.DefaultCaddyFileLimit
	}
	if err := source.ValidateCredentials(source.TypeHTTP, source.Config{HTTP: &cfg}, credentials); err != nil {
		return nil, err
	}
	auth := authConfig{method: cfg.AuthMethod, username: cfg.Username}
	if credentials.HTTP != nil {
		auth.password = credentials.HTTP.Password
		auth.token = credentials.HTTP.BearerToken
	}
	m, err := newMapper(cfg.BaseURL)
	if err != nil {
		return nil, err
	}
	return newClient(cfg, newRequester(m, auth)), nil
}

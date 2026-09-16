package source

import (
	"context"
	"fmt"
)

// RemoteRegistry 把协议类型 dispatch 到对应 factory：协议边界只在此
// 做一次类型分发，业务层（Runner / API / 未来 MCP）不允许出现
// switch source.Type。Registry 自身实现 RemoteFactory，可整体注入
// Service。
type RemoteRegistry struct {
	factories map[Type]RemoteFactory
}

// 编译期断言：Registry 可作为 RemoteFactory 使用。
var _ RemoteFactory = (*RemoteRegistry)(nil)

// NewRemoteRegistry 按 factory 声明的协议类型建立 dispatch 表；
// 同一类型重复注册视为装配错误。
func NewRemoteRegistry(factories ...RemoteFactory) (*RemoteRegistry, error) {
	m := make(map[Type]RemoteFactory, len(factories))
	for _, f := range factories {
		t := f.Type()
		if _, dup := m[t]; dup {
			return nil, fmt.Errorf("%w: duplicate remote factory for type %q", ErrInvalid, t)
		}
		m[t] = f
	}
	return &RemoteRegistry{factories: m}, nil
}

// Type 实现 RemoteFactory：返回空串以示 Registry 是聚合入口而非
// 单一协议 factory（Registry 不会注册进另一个 Registry）。
func (r *RemoteRegistry) Type() Type {
	return ""
}

// Create 实现 RemoteFactory：按 Source 类型 dispatch；未注册的类型
// 返回 ErrUnsupportedType。
func (r *RemoteRegistry) Create(ctx context.Context, s Source, credentials Credentials) (Remote, error) {
	f, ok := r.factories[s.Type]
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrUnsupportedType, s.Type)
	}
	return f.Create(ctx, s, credentials)
}

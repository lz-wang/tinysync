package source

import (
	"context"
	"strings"
	"time"
)

// testTimeout 是 Connection Test 的最长时间。
const testTimeout = 10 * time.Second

// Service 是 Source 的应用服务：REST API、Web UI 与 MCP 共用的业务入口。
// 统一处理校验、ID 与时间戳生成、凭据三态更新语义、远端客户端构造与
// 连接测试超时；调用方不接触存储细节。
type Service struct {
	repo    Repository
	factory RemoteFactory
	// Now 返回当前时间；默认 UTC time.Now，测试可注入固定时钟。
	Now func() time.Time
}

// NewService 构造应用服务。
func NewService(repo Repository, factory RemoteFactory) *Service {
	return &Service{
		repo:    repo,
		factory: factory,
		Now:     func() time.Time { return time.Now().UTC() },
	}
}

// TestResult 是 Connection Test 的结果。连接失败也是一次成功完成的
// 测试操作：以 OK=false 与 Error 描述返回，不代表测试操作本身失败。
type TestResult struct {
	OK        bool
	LatencyMS int64
	// Error 是失败原因的人类可读描述；成功时为空。
	Error string
}

// Create 校验并创建 Source。Type 决定 Config / Credentials 的单选组；
// 校验失败返回 ErrInvalid。
func (s *Service) Create(ctx context.Context, input CreateInput) (Source, error) {
	if err := ValidateCreateInput(input); err != nil {
		return Source{}, err
	}

	id, err := NewID()
	if err != nil {
		return Source{}, err
	}
	now := s.Now()
	src := Source{
		ID:              id,
		Name:            strings.TrimSpace(input.Name),
		Type:            input.Type,
		Config:          input.Config.Normalized(input.Type),
		CredentialState: CredentialStateOf(input.Type, input.Credentials),
		Enabled:         input.Enabled,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	if err := s.repo.Create(ctx, src, input.Credentials); err != nil {
		return Source{}, err
	}
	return src, nil
}

// Get 按 ID 读取 Source。
func (s *Service) Get(ctx context.Context, id string) (Source, error) {
	return s.repo.Get(ctx, id)
}

// List 返回全部 Source，顺序稳定。
func (s *Service) List(ctx context.Context) ([]Source, error) {
	return s.repo.List(ctx)
}

// Update 部分更新 Source：nil 字段保留现有值。Type 不可变（输入结构
// 不携带 Type）。Credentials 按协议组更新，组内 secret 为三态语义：
// nil 保留、空串清除、非空替换。
func (s *Service) Update(ctx context.Context, id string, input UpdateInput) (Source, error) {
	current, err := s.repo.Get(ctx, id)
	if err != nil {
		return Source{}, err
	}

	updated := current
	if input.Name != nil {
		name := strings.TrimSpace(*input.Name)
		if err := ValidateName(name); err != nil {
			return Source{}, err
		}
		updated.Name = name
	}
	if input.Config != nil {
		if err := ValidateConfig(updated.Type, *input.Config); err != nil {
			return Source{}, err
		}
		updated.Config = input.Config.Normalized(updated.Type)
	}
	if input.Credentials != nil {
		if err := ValidateCredentialsUpdate(updated.Type, input.Credentials); err != nil {
			return Source{}, err
		}
		updated.CredentialState = applyCredentialsUpdate(updated.Type, updated.CredentialState, input.Credentials)
	}
	if input.Enabled != nil {
		updated.Enabled = *input.Enabled
	}
	updated.UpdatedAt = s.Now()

	if err := s.repo.Update(ctx, updated, input.Credentials); err != nil {
		return Source{}, err
	}
	return updated, nil
}

// Delete 按 ID 硬删除 Source。只删除本地 Source 配置，
// 不触及远端文件。
func (s *Service) Delete(ctx context.Context, id string) error {
	return s.repo.Delete(ctx, id)
}

// OpenRemote 按 ID 读取 Source 并构造其远端客户端：凭据查询与协议
// dispatch 集中在此一条链路，Runner、Connection Test 与未来 MCP /
// browser 复用同一入口，不各自接触凭据或 factory。调用方负责在用毕
// 后 Close 返回的 Remote。返回 Source 供调用方执行 Enabled 等策略
// 检查；凭据明文不经过该入口以外的任何路径。
func (s *Service) OpenRemote(ctx context.Context, id string) (Source, Remote, error) {
	src, err := s.repo.Get(ctx, id)
	if err != nil {
		return Source{}, nil, err
	}
	creds, err := s.repo.GetCredentials(ctx, id)
	if err != nil {
		return Source{}, nil, err
	}
	remote, err := s.factory.Create(ctx, src, creds)
	if err != nil {
		return Source{}, nil, err
	}
	return src, remote, nil
}

// TestConnection 真正连接远端验证配置：对根路径执行 Stat（WebDAV 为
// PROPFIND）。连接失败返回 OK=false 的结果与 nil 错误；Source 不存在
// 或存储故障才返回错误。Remote 的创建纳入同一超时窗口，结束后始终
// 释放连接（有连接生命周期的协议不遗留会话）。
func (s *Service) TestConnection(ctx context.Context, id string) (TestResult, error) {
	start := s.Now()
	testCtx, cancel := context.WithTimeout(ctx, testTimeout)
	defer cancel()
	_, remote, err := s.OpenRemote(testCtx, id)
	if err != nil {
		return TestResult{}, err
	}
	// 关闭失败不影响测试结论；探测结果只由 Stat 决定。
	defer func() { _ = remote.Close() }()
	_, statErr := remote.Stat(testCtx, "/")
	latency := s.Now().Sub(start).Milliseconds()
	if statErr != nil {
		return TestResult{OK: false, LatencyMS: latency, Error: statErr.Error()}, nil
	}
	return TestResult{OK: true, LatencyMS: latency}, nil
}

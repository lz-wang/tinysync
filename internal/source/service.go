package source

import (
	"context"
	"strings"
	"time"
)

// testTimeout 是 Connection Test 的最长时间。
const testTimeout = 10 * time.Second

// Service 是 Source 的应用服务：REST API、Web UI 与 MCP 共用的业务入口。
// 统一处理校验、ID 与时间戳生成、password 更新语义、远端客户端构造与
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

// Create 校验并创建 Source。
func (s *Service) Create(ctx context.Context, input CreateInput) (Source, error) {
	name := strings.TrimSpace(input.Name)
	if err := ValidateName(name); err != nil {
		return Source{}, err
	}
	if err := ValidateType(input.Type); err != nil {
		return Source{}, err
	}
	endpoint := strings.TrimSpace(input.Endpoint)
	if err := ValidateEndpoint(endpoint); err != nil {
		return Source{}, err
	}

	id, err := NewID()
	if err != nil {
		return Source{}, err
	}
	now := s.Now()
	src := Source{
		ID:          id,
		Name:        name,
		Type:        input.Type,
		Endpoint:    endpoint,
		Username:    input.Username,
		PasswordSet: input.Password != "",
		Enabled:     input.Enabled,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if err := s.repo.Create(ctx, src, input.Password); err != nil {
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

// Update 部分更新 Source：nil 字段保留现有值；password 语义为
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
	if input.Endpoint != nil {
		endpoint := strings.TrimSpace(*input.Endpoint)
		if err := ValidateEndpoint(endpoint); err != nil {
			return Source{}, err
		}
		updated.Endpoint = endpoint
	}
	if input.Username != nil {
		updated.Username = *input.Username
	}
	if input.Enabled != nil {
		updated.Enabled = *input.Enabled
	}
	updated.UpdatedAt = s.Now()

	var password *string
	if input.Password != nil {
		pw := *input.Password
		password = &pw
		updated.PasswordSet = pw != ""
	}
	if err := s.repo.Update(ctx, updated, password); err != nil {
		return Source{}, err
	}
	return updated, nil
}

// Delete 按 ID 硬删除 Source。只删除本地 Source 配置，
// 不触及远端文件。
func (s *Service) Delete(ctx context.Context, id string) error {
	return s.repo.Delete(ctx, id)
}

// TestConnection 真正连接远端验证配置：对根路径执行 Stat（WebDAV 为
// PROPFIND）。连接失败返回 OK=false 的结果与 nil 错误；Source 不存在
// 或存储故障才返回错误。
func (s *Service) TestConnection(ctx context.Context, id string) (TestResult, error) {
	src, err := s.repo.Get(ctx, id)
	if err != nil {
		return TestResult{}, err
	}
	password, err := s.repo.GetPassword(ctx, id)
	if err != nil {
		return TestResult{}, err
	}
	remote, err := s.factory.Create(src, password)
	if err != nil {
		return TestResult{}, err
	}

	start := s.Now()
	testCtx, cancel := context.WithTimeout(ctx, testTimeout)
	defer cancel()
	_, statErr := remote.Stat(testCtx, "/")
	latency := s.Now().Sub(start).Milliseconds()
	if statErr != nil {
		return TestResult{OK: false, LatencyMS: latency, Error: statErr.Error()}, nil
	}
	return TestResult{OK: true, LatencyMS: latency}, nil
}

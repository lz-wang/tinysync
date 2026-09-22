package source

import (
	"context"
	"fmt"
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
// dispatch 集中在此一条链路，Runner 与未来 MCP / browser 复用同一
// 入口，不各自接触凭据或 factory。调用方负责在用毕后 Close 返回的
// Remote。返回 Source 供调用方执行 Enabled 等策略检查；凭据明文不
// 经过该入口以外的任何路径。
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
// 或存储故障才返回错误。两类失败以 Remote 创建为界区分：Repository
// 读取失败是存储故障（返回错误）；factory 失败是测试的结论——SFTP
// 等协议的 dial、认证、host key 校验全部发生在 Create 内，与 Stat
// 失败同样归入 OK=false，不作为操作失败传播。Remote 的创建与探测
// 纳入同一超时窗口，结束后始终释放连接（有连接生命周期的协议不
// 遗留会话）。
func (s *Service) TestConnection(ctx context.Context, id string) (TestResult, error) {
	start := s.Now()
	testCtx, cancel := context.WithTimeout(ctx, testTimeout)
	defer cancel()
	src, err := s.repo.Get(ctx, id)
	if err != nil {
		return TestResult{}, err
	}
	creds, err := s.repo.GetCredentials(ctx, id)
	if err != nil {
		return TestResult{}, err
	}
	remote, err := s.factory.Create(testCtx, src, creds)
	if err != nil {
		return TestResult{OK: false, LatencyMS: s.Now().Sub(start).Milliseconds(), Error: err.Error()}, nil
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

// InspectInput 是预览请求的输入，语义按字段组合解释（不持久化任何
// 值）：
//   - 仅 SourceID：预览已保存 Source（已存配置 + 已存凭据）；
//   - SourceID + GitHubConfig：提案配置 + 已存凭据（编辑表单微调后
//     预览，无需重新索取 Token）；
//   - SourceID + GitHubConfig + Token：提案配置 + 提案 Token；
//   - 仅 GitHubConfig（+ 可选 Token）：提案配置 + 匿名/提案 Token
//     （创建表单）。
type InspectInput struct {
	SourceID     string
	GitHubConfig *GitHubReleaseConfig
	Token        string
}

// InspectResult 是预览的结果。发现失败也是一次成功完成的预览操作：
// 以 OK=false 与 Error 描述返回；只有请求本身异常才作为错误传播。
type InspectResult struct {
	OK         bool
	LatencyMS  int64
	Error      string
	Inspection *GitHubInspection
}

// Inspect 执行「创建前测试并预览」：经协议 factory 构造临时客户端
// 并断言 ReleaseInspector 能力（当前仅 github_release 支持）。凭据
// 明文只在 Service 内部流转（与 OpenRemote 同一边界）；Remote 在
// 预览窗口结束後始终释放。
func (s *Service) Inspect(ctx context.Context, input InspectInput) (InspectResult, error) {
	start := s.Now()
	inspectCtx, cancel := context.WithTimeout(ctx, testTimeout)
	defer cancel()

	var src Source
	var creds Credentials
	if input.SourceID != "" {
		var err error
		src, err = s.repo.Get(ctx, input.SourceID)
		if err != nil {
			return InspectResult{}, err
		}
		creds, err = s.repo.GetCredentials(ctx, input.SourceID)
		if err != nil {
			return InspectResult{}, err
		}
		// 编辑态预览：提案配置 / 提案 Token 只作用于本次预览的临时
		// 实例（src / creds 是仓库返回值的拷贝，不写回持久层）；
		// 未提供的部分沿用已存值。覆盖项仅对 github_release 有意义，
		// 其余类型拒绝以免静默忽略造成「以为用新配置预览了」的错位。
		if input.GitHubConfig != nil || input.Token != "" {
			if src.Type != TypeGitHubRelease {
				return InspectResult{}, fmt.Errorf("%w: inspect overrides require github_release source, got %q", ErrInvalid, src.Type)
			}
		}
		if input.GitHubConfig != nil {
			if err := ValidateConfig(TypeGitHubRelease, Config{GitHubRelease: input.GitHubConfig}); err != nil {
				return InspectResult{}, err
			}
			src.Config = Config{GitHubRelease: input.GitHubConfig}.Normalized(TypeGitHubRelease)
		}
		if input.Token != "" {
			creds.GitHubRelease = &GitHubReleaseCredentials{Token: input.Token}
		}
	} else {
		if input.GitHubConfig == nil {
			return InspectResult{}, fmt.Errorf("%w: inspect requires source_id or github_release config", ErrInvalid)
		}
		if err := ValidateConfig(TypeGitHubRelease, Config{GitHubRelease: input.GitHubConfig}); err != nil {
			return InspectResult{}, err
		}
		normalized := Config{GitHubRelease: input.GitHubConfig}.Normalized(TypeGitHubRelease)
		src = Source{Type: TypeGitHubRelease, Config: normalized}
		creds = Credentials{GitHubRelease: &GitHubReleaseCredentials{Token: input.Token}}
	}

	remote, err := s.factory.Create(inspectCtx, src, creds)
	if err != nil {
		return InspectResult{OK: false, LatencyMS: s.Now().Sub(start).Milliseconds(), Error: err.Error()}, nil
	}
	defer func() { _ = remote.Close() }()
	inspector, ok := remote.(ReleaseInspector)
	if !ok {
		return InspectResult{}, fmt.Errorf("%w: %q does not support inspect", ErrInvalid, src.Type)
	}
	inspection, err := inspector.Inspect(inspectCtx, InspectAssetDetailLimit)
	if err != nil {
		return InspectResult{OK: false, LatencyMS: s.Now().Sub(start).Milliseconds(), Error: err.Error()}, nil
	}
	return InspectResult{OK: true, LatencyMS: s.Now().Sub(start).Milliseconds(), Inspection: inspection}, nil
}

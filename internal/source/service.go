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
	// Credentials 是凭据引用的校验与解析入口；nil 时跳过引用存在性
	// 校验（测试装配），生产装配必须提供。
	Credentials CredentialResolver
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
	prepared, err := prepareCreateInput(input)
	if err != nil {
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
		Config:          prepared,
		CredentialState: CredentialStateOf(input.Type, input.Credentials),
		Enabled:         input.Enabled,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	// 凭据引用存在性校验：以归一化后的 config 为准。
	if err := s.validateCredentialReference(ctx, src.Config); err != nil {
		return Source{}, err
	}
	if err := s.repo.Create(ctx, src, input.Credentials); err != nil {
		return Source{}, err
	}
	// 写后重读：CredentialState 以存储推导（含引用态跟随凭据）为
	// 单一事实来源，避免服务层推导与持久化脱节。
	return s.repo.Get(ctx, id)
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

	// 先验证调用方的原始组选择，再进行协议专属的凭据归一化；否则
	// HTTP 全量更新会覆盖原输入，静默吞掉 SMB 等外协议凭据组。
	if err := ValidateCredentialsUpdate(current.Type, input.Credentials); err != nil {
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
		normalized, err := PrepareConfig(updated.Type, *input.Config)
		if err != nil {
			return Source{}, err
		}
		// 凭据引用存在性校验：config 整体替换语义下，credential_id
		// 缺省即解绑，出现即改绑（指向不存在的凭据在入口拒绝）。
		if err := s.validateCredentialReference(ctx, normalized); err != nil {
			return Source{}, err
		}
		updated.Config = normalized
	}

	// HTTP auth_method ↔ secret 存储不变量：auth_method 变更时清除
	// 非当前认证方式的存量 secret（none → 全清、basic → 清 token、
	// bearer → 清 password），并对「存量 + 本次更新」合并后的最终
	// 有效凭据整体校验后一次性写入。否则旧方式的 secret 沉睡在存储
	// 里——轻则形成 dormant credential，重则（如 basic → none 不清
	// password）让 ValidateCredentials 在下次 Factory.Create 时拒绝，
	// Source 保存成功却无法打开 Remote。不变量由后端保证，WebUI 的
	// 联动清除只是显示层优化。
	if updated.Type == TypeHTTP {
		authChanged := current.Config.HTTP == nil || updated.Config.HTTP == nil ||
			current.Config.HTTP.AuthMethod != updated.Config.HTTP.AuthMethod
		secretChanged := input.Credentials != nil && input.Credentials.HTTP != nil &&
			(input.Credentials.HTTP.Password != nil || input.Credentials.HTTP.BearerToken != nil)
		if authChanged || secretChanged {
			effective, err := s.effectiveHTTPCredentials(ctx, id, updated.Config, input.Credentials)
			if err != nil {
				return Source{}, err
			}
			input.Credentials = effective
		} else {
			// 无关更新和空 patch 不读写 secret，避免以旧快照覆盖并发轮换。
			input.Credentials = nil
		}
	}

	// 引用态源不接受内联 secret 写入：无论本次是否变更 config，只要
	// 生效配置处于引用态，就强制清除同请求携带（或合并进存储）的内联
	// secret——否则「引用 + 沉睡内联钥」会在后续解绑时静默复活旧钥，
	// 互斥不变量在存储层被打破。
	if updated.Type == TypeSFTP && updated.Config.SFTP != nil && updated.Config.SFTP.CredentialID != "" {
		input.Credentials = clearInlineSFTPUpdate(input.Credentials)
	}
	if input.Credentials != nil {
		updated.CredentialState = applyCredentialsUpdate(updated.Type, updated.CredentialState, input.Credentials)
	}
	if input.Enabled != nil {
		updated.Enabled = *input.Enabled
	}
	updated.UpdatedAt = s.Now()

	if err := s.repo.Update(ctx, updated, input.Credentials); err != nil {
		return Source{}, err
	}
	// 写后重读：与 Create 同理，回显以存储推导的 CredentialState 为准。
	return s.repo.Get(ctx, id)
}

// Delete 按 ID 硬删除 Source。只删除本地 Source 配置，
// 不触及远端文件。
func (s *Service) Delete(ctx context.Context, id string) error {
	return s.repo.Delete(ctx, id)
}

// BindCredential 把源的凭据引用指向 credentialID 并强制清除内联
// secret：以调用时点的最新配置为基础改写，返回更新后的源。与源配置
// 的整体替换语义一致，并发编辑遵循 last-write-wins；提升编排（api
// 层）在紧邻调用前创建凭据，窗口已尽量收窄。
func (s *Service) BindCredential(ctx context.Context, id string, credentialID string) (Source, error) {
	src, err := s.repo.Get(ctx, id)
	if err != nil {
		return Source{}, err
	}
	if src.Type != TypeSFTP || src.Config.SFTP == nil ||
		src.Config.SFTP.AuthMethod != SFTPAuthPrivateKey {
		return Source{}, fmt.Errorf("%w: source is not eligible for credential promotion", ErrInvalid)
	}
	cfg := *src.Config.SFTP
	cfg.CredentialID = credentialID
	normalized := (Config{SFTP: &cfg}).Normalized(src.Type)
	if err := s.validateCredentialReference(ctx, normalized); err != nil {
		return Source{}, err
	}
	if err := s.repo.Update(ctx, sourceForUpdate(src, normalized), clearInlineSFTPUpdate(nil)); err != nil {
		return Source{}, err
	}
	// 写后重读：回显以存储推导的 CredentialState 为准。
	return s.repo.Get(ctx, id)
}

// sourceForUpdate 以现有源为基底套用新配置（name / enabled 等其余
// 可变字段保持不变）。
func sourceForUpdate(src Source, cfg Config) Source {
	updated := src
	updated.Config = cfg
	return updated
}

// InlineCredentialsForPromote 返回源存储的内联凭据明文：仅供提升迁移
// 转存进凭据库使用，私钥不经手前端。非 SFTP / 引用态源返回 ErrInvalid。
func (s *Service) InlineCredentialsForPromote(ctx context.Context, id string) (Credentials, error) {
	src, err := s.repo.Get(ctx, id)
	if err != nil {
		return Credentials{}, err
	}
	if src.Type != TypeSFTP || src.Config.SFTP == nil ||
		src.Config.SFTP.CredentialID != "" ||
		src.Config.SFTP.AuthMethod != SFTPAuthPrivateKey {
		return Credentials{}, fmt.Errorf("%w: source is not eligible for credential promotion", ErrInvalid)
	}
	return s.repo.GetCredentials(ctx, id)
}

// clearInlineSFTPUpdate 返回强制清除全部内联 SFTP secret 的更新输入：
// 三个字段均为空串（三态清除），显式覆盖调用方同请求携带的任何值。
func clearInlineSFTPUpdate(creds *CredentialsUpdate) *CredentialsUpdate {
	empty := ""
	clear := &CredentialsUpdate{SFTP: &SFTPCredentialsUpdate{
		Password:             &empty,
		PrivateKey:           &empty,
		PrivateKeyPassphrase: &empty,
	}}
	if creds == nil {
		return clear
	}
	creds.SFTP = clear.SFTP
	return creds
}

// effectiveHTTPCredentials 计算 HTTP Source 更新后的最终有效凭据，
// 返回等价的整组更新输入（两个字段全量写入，不再依赖逐字段三态）：
// 读取存量 secret、套用本次 patch、按生效 auth_method 清除非当前
// 认证方式的字段，再以 ValidateCredentials 校验最终组合——basic 缺
// password / bearer 缺 token 的更新在入口拒绝，而不是保存一个下次
// Factory.Create 会拒绝、无法打开 Remote 的 Source。
func (s *Service) effectiveHTTPCredentials(ctx context.Context, id string, cfg Config, patch *CredentialsUpdate) (*CredentialsUpdate, error) {
	if cfg.HTTP == nil {
		return nil, fmt.Errorf("%w: http config is required", ErrInvalid)
	}
	stored, err := s.repo.GetCredentials(ctx, id)
	if err != nil {
		return nil, err
	}
	var password, token string
	if stored.HTTP != nil {
		password, token = stored.HTTP.Password, stored.HTTP.BearerToken
	}
	if patch != nil && patch.HTTP != nil {
		if patch.HTTP.Password != nil {
			password = *patch.HTTP.Password
		}
		if patch.HTTP.BearerToken != nil {
			token = *patch.HTTP.BearerToken
		}
	}
	switch cfg.HTTP.AuthMethod {
	case HTTPAuthNone:
		password, token = "", ""
	case HTTPAuthBasic:
		token = ""
	case HTTPAuthBearer:
		password = ""
	}
	if err := ValidateCredentials(TypeHTTP, cfg, Credentials{HTTP: &HTTPCredentials{
		Password:    password,
		BearerToken: token,
	}}); err != nil {
		return nil, err
	}
	return &CredentialsUpdate{HTTP: &HTTPCredentialsUpdate{
		Password:    &password,
		BearerToken: &token,
	}}, nil
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
	creds, err := s.credentialsFor(ctx, src)
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
	creds, err := s.credentialsFor(ctx, src)
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

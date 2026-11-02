package oci

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/go-containerregistry/pkg/v1/remote/transport"

	"tinysync/internal/job"
	"tinysync/internal/oci/image"
	"tinysync/internal/oci/registry"
)

// JobLister 提供统一任务配置读取（Registry 删除守卫与镜像状态列表）；
// 由 *job.Service 实现。
type JobLister interface {
	List(ctx context.Context) ([]job.Job, error)
}

// Service 是 OCI 领域的应用服务：Registry 配置 CRUD（primary 目标仓库 +
// mirror 加速器）、引擎 resolver 快照、镜像管理查询（tags / inspect /
// remote discovery）与 oci 任务的源镜像状态列表。
type Service struct {
	repo Repository
	// Jobs 提供任务配置读取（Registry 删除守卫与镜像状态列表）；nil 时
	// 删除不做引用拦截、镜像列表为空。装配顺序上晚于构造回填
	//（job.Service 的构造需要 OCI 执行器，OCI 执行器又需要本服务）。
	Jobs JobLister
	// logger 是引擎日志接口（zap → tinysync logging 适配由 NewService
	// 注入默认实现）。
	logger registry.Logger
	// client 复用 transport 缓存的 registry 客户端（查询端点共用）。
	client *registry.Client
	// Now 返回当前时间；默认 UTC time.Now，测试可注入固定时钟。
	Now func() time.Time
}

// NewService 构造应用服务；logger 为 nil 时用静默实现（测试）。
func NewService(repo Repository, logger registry.Logger) *Service {
	if logger == nil {
		logger = nopLogger{}
	}
	return &Service{
		repo:   repo,
		logger: logger,
		client: registry.NewClient(logger),
		Now:    func() time.Time { return time.Now().UTC() },
	}
}

// nopLogger 是测试用的静默日志实现。
type nopLogger struct{}

func (nopLogger) Debugf(string, ...any) {}

func (nopLogger) Infof(string, ...any) {}

func (nopLogger) Warnf(string, ...any) {}

func (nopLogger) Errorf(string, ...any) {}

// Resolver 返回当前 registries 表的快照 resolver：一轮运行 / 一次查询
// 内配置一致，不受并发变更影响。
func (s *Service) Resolver(ctx context.Context) (registry.Resolver, error) {
	rows, err := s.repo.List(ctx)
	if err != nil {
		return nil, err
	}
	return newResolver(rows), nil
}

// ---------------------------------------------------------------------------
// Registry CRUD
// ---------------------------------------------------------------------------

// Get 按 ID 读取 Registry 配置。
func (s *Service) Get(ctx context.Context, id string) (Registry, error) {
	return s.repo.Get(ctx, id)
}

// List 返回全部 Registry 配置（按 name 排序）。
func (s *Service) List(ctx context.Context) ([]Registry, error) {
	return s.repo.List(ctx)
}

// Create 校验并落库新 Registry 配置。校验沿用 oci-sync：name 必填唯一、
// host 必填；mirror 行上游唯一且至少一个非空加速器；path_transform
// 配置时 separator 必填、mode 受限。
func (s *Service) Create(ctx context.Context, input CreateInput) (Registry, error) {
	kind := input.Kind
	if kind == "" {
		kind = KindPrimary
	}
	if !kind.Valid() {
		return Registry{}, fmt.Errorf("%w: kind must be primary or mirror", ErrRegistryInvalid)
	}
	row := Registry{
		Name:               strings.TrimSpace(input.Name),
		Kind:               kind,
		Host:               strings.TrimSpace(input.Host),
		Username:           input.Username,
		Password:           input.Password,
		InsecureSkipVerify: input.InsecureSkipVerify,
		PlainHTTP:          input.PlainHTTP,
		StripRegistries:    input.StripRegistries,
		PathTransform:      input.PathTransform,
		Mirrors:            input.Mirrors,
	}
	if err := s.validate(ctx, row, nil); err != nil {
		return Registry{}, err
	}
	id, err := NewID()
	if err != nil {
		return Registry{}, err
	}
	now := s.Now()
	row.ID = id
	row.CreatedAt = now
	row.UpdatedAt = now
	if err := s.repo.Create(ctx, row); err != nil {
		return Registry{}, err
	}
	return row, nil
}

// Update 按 ID 更新 Registry 配置：nil 字段保留现有值，Password 三态
// （nil 保留、空串清除、非空替换）。Kind 不可变更；mirror 上游变更重新
// 校验唯一性。
func (s *Service) Update(ctx context.Context, id string, input UpdateInput) (Registry, error) {
	current, err := s.repo.Get(ctx, id)
	if err != nil {
		return Registry{}, err
	}
	updated := current
	if input.Name != nil {
		updated.Name = strings.TrimSpace(*input.Name)
	}
	if input.Host != nil {
		updated.Host = strings.TrimSpace(*input.Host)
	}
	if input.Username != nil {
		updated.Username = *input.Username
	}
	if input.Password != nil {
		updated.Password = *input.Password
	}
	if input.InsecureSkipVerify != nil {
		updated.InsecureSkipVerify = *input.InsecureSkipVerify
	}
	if input.PlainHTTP != nil {
		updated.PlainHTTP = *input.PlainHTTP
	}
	if input.StripRegistries != nil {
		updated.StripRegistries = *input.StripRegistries
	}
	if input.PathTransformSet {
		updated.PathTransform = input.PathTransform
	}
	if input.Mirrors != nil {
		updated.Mirrors = *input.Mirrors
	}
	if err := s.validate(ctx, updated, &current); err != nil {
		return Registry{}, err
	}
	updated.UpdatedAt = s.Now()
	if err := s.repo.Update(ctx, updated); err != nil {
		return Registry{}, err
	}
	return updated, nil
}

// validate 校验一行 Registry 配置（创建与更新共用）。skip 是更新前的
// 现值（唯一性检查排除自身）。
func (s *Service) validate(ctx context.Context, row Registry, skip *Registry) error {
	if row.Name == "" {
		return fmt.Errorf("%w: name is required", ErrRegistryInvalid)
	}
	if row.Host == "" {
		return fmt.Errorf("%w: host is required", ErrRegistryInvalid)
	}
	if row.Hostname() == "" {
		return fmt.Errorf("%w: host must start with the registry hostname", ErrRegistryInvalid)
	}
	if err := validatePathTransform(row.PathTransform); err != nil {
		return err
	}
	for i, entry := range row.StripRegistries {
		if strings.TrimSpace(entry) == "" {
			return fmt.Errorf("%w: strip_registries[%d] cannot be empty", ErrRegistryInvalid, i)
		}
	}
	if row.Kind == KindMirror {
		// mirror 行：上游主机唯一；至少一个非空加速器。
		if len(row.Mirrors) == 0 {
			return fmt.Errorf("%w: mirror %s: at least one mirror address is required", ErrRegistryInvalid, row.Name)
		}
		for i, addr := range row.Mirrors {
			if strings.TrimSpace(addr) == "" {
				return fmt.Errorf("%w: mirror %s: mirror address %d cannot be empty", ErrRegistryInvalid, row.Name, i)
			}
		}
		existing, err := s.repo.List(ctx)
		if err != nil {
			return err
		}
		for _, other := range existing {
			if skip != nil && other.ID == skip.ID {
				continue
			}
			if other.Kind == KindMirror && other.Host == row.Host {
				return fmt.Errorf("%w: duplicate mirror upstream %q", ErrMirrorConflict, row.Host)
			}
		}
	} else if len(row.Mirrors) > 0 {
		return fmt.Errorf("%w: mirrors is only valid for mirror rows", ErrRegistryInvalid)
	}
	return nil
}

// validatePathTransform 校验路径转换配置：separator 必填，mode 取值受限
// （空为 both，oci-sync 默认语义）。
func validatePathTransform(pt *registry.PathTransformConfig) error {
	if pt == nil {
		return nil
	}
	if strings.TrimSpace(pt.Separator) == "" {
		return fmt.Errorf("%w: path_transform.separator is required when path_transform is set", ErrRegistryInvalid)
	}
	switch pt.Mode {
	case "", "encode", "decode", "both":
		return nil
	default:
		return fmt.Errorf("%w: path_transform.mode must be encode, decode or both", ErrRegistryInvalid)
	}
}

// Delete 删除 Registry 配置。仍被 oci 任务 dst_servers 引用时拒绝
// （应用层守卫，引用位于任务 config_json 内）。
func (s *Service) Delete(ctx context.Context, id string) error {
	current, err := s.repo.Get(ctx, id)
	if err != nil {
		return err
	}
	if s.Jobs != nil {
		jobs, err := s.Jobs.List(ctx)
		if err != nil {
			return err
		}
		for _, j := range jobs {
			if j.Kind != job.KindOCI {
				continue
			}
			cfg, err := ParseTaskConfig(j.Config)
			if err != nil {
				continue
			}
			for _, dst := range cfg.DstServers {
				if dst == current.Name {
					return fmt.Errorf("%w: %s is referenced by job %q", ErrRegistryInUse, current.Name, j.Name)
				}
			}
		}
	}
	return s.repo.Delete(ctx, id)
}

// OverrideSrcImages 构造「立即运行带子集」的配置快照：校验子集合法性
// （与 Validate 同一规则）后替换 src_images（展开归一），其余字段保持
// 任务落库配置。仅用于本次运行，不落库。
func (s *Service) OverrideSrcImages(ctx context.Context, cfg json.RawMessage, images []string) (json.RawMessage, error) {
	taskCfg, err := ParseTaskConfig(cfg)
	if err != nil {
		return nil, err
	}
	if err := ValidateSrcImages(images); err != nil {
		return nil, err
	}
	expanded, err := ExpandSrcImages(images)
	if err != nil {
		return nil, err
	}
	if len(expanded) == 0 {
		return nil, fmt.Errorf("%w: src_images cannot be empty", job.ErrInvalid)
	}
	taskCfg.SrcImages = expanded
	return MarshalTaskConfig(taskCfg)
}

// ---------------------------------------------------------------------------
// 镜像状态列表（GET /images）
// ---------------------------------------------------------------------------

// ImageListItem 是一个源镜像在所属 oci 任务下的配置与最近执行状态。
type ImageListItem struct {
	Source        string   `json:"source"`
	JobID         string   `json:"job_id"`
	JobName       string   `json:"job_name"`
	JobEnabled    bool     `json:"job_enabled"`
	DstRegistries []string `json:"dst_registries"`
	LastStatus    string   `json:"last_status,omitempty"`
	LastSyncTime  string   `json:"last_sync_time,omitempty"`
}

// ListImages 合并 oci 任务定义与各源镜像的最近执行状态
// （oci-sync control ListImages 的移植；执行状态读自统一 job_run_items）。
func (s *Service) ListImages(ctx context.Context) ([]ImageListItem, error) {
	if s.Jobs == nil {
		return nil, nil
	}
	jobs, err := s.Jobs.List(ctx)
	if err != nil {
		return nil, err
	}
	ociJobs := make([]job.Job, 0, len(jobs))
	for _, j := range jobs {
		if j.Kind == job.KindOCI {
			ociJobs = append(ociJobs, j)
		}
	}
	ids := make([]string, 0, len(ociJobs))
	for _, j := range ociJobs {
		ids = append(ids, j.ID)
	}
	latest, err := s.repo.LatestImageStates(ctx, ids)
	if err != nil {
		return nil, err
	}
	items := make([]ImageListItem, 0)
	for _, j := range ociJobs {
		cfg, err := ParseTaskConfig(j.Config)
		if err != nil {
			continue
		}
		for _, ref := range image.ExpandSrcImages(cfg.SrcImages) {
			source := ref.Repo + ":" + ref.Tag
			item := ImageListItem{
				Source:        source,
				JobID:         j.ID,
				JobName:       j.Name,
				JobEnabled:    j.Enabled,
				DstRegistries: cfg.DstServers,
			}
			if state, ok := latest[j.ID+"\x00"+source]; ok {
				item.LastStatus = state.Status
				item.LastSyncTime = state.SyncedAt.UTC().Format(time.RFC3339)
			}
			items = append(items, item)
		}
	}
	return items, nil
}

// ---------------------------------------------------------------------------
// 镜像管理查询（tags / inspect / remote discovery）
// ---------------------------------------------------------------------------

// configuredRegistry 按配置名或主机名查 primary Registry（GET
// /remote/repositories 的 registry 参数解析；同名主机多配置时要求给出
// 配置名）。oci-sync control configuredRegistry 移植：先按名精确匹配，
// 再按主机匹配（多命中为歧义）。
func configuredRegistry(rows []Registry, input string) (Registry, error) {
	for _, row := range rows {
		if row.Kind == KindPrimary && row.Name == input {
			return row, nil
		}
	}
	var match *Registry
	for i := range rows {
		row := &rows[i]
		if row.Kind != KindPrimary || row.Hostname() != input {
			continue
		}
		if match != nil {
			return Registry{}, ambiguousRegistry()
		}
		match = row
	}
	if match == nil {
		return Registry{}, &Error{Code: "registry_not_configured", Message: "registry is not configured"}
	}
	return *match, nil
}

func ambiguousRegistry() *Error {
	return &Error{Code: "registry_ambiguous", Message: "multiple registry configurations match; specify the registry configuration name"}
}

// repositoryRegistry 解析镜像引用所属的 primary Registry（oci-sync
// control repositoryRegistry 移植，公共访问策略：无匹配时回退匿名访问）。
// selected 非空时必须精确命中该配置且 namespace 覆盖仓库路径；否则按
// 最长 namespace 前缀选择，同长度多匹配视为歧义。
func repositoryRegistry(rows []Registry, ref *image.ImageReference, selected string) (Registry, error) {
	path := strings.TrimPrefix(ref.FullRepoName(), ref.Registry+"/")
	if selected != "" {
		for _, row := range rows {
			if row.Kind == KindPrimary && row.Name == selected {
				if row.Hostname() != ref.Registry || !namespaceAllowed(row, path) {
					return Registry{}, &Error{Code: "registry_forbidden", Message: "repository is outside the selected registry or namespace"}
				}
				return row, nil
			}
		}
		return Registry{}, &Error{Code: "registry_not_configured", Message: "registry configuration name is not configured"}
	}
	var best *Registry
	ambiguous := false
	for i := range rows {
		row := &rows[i]
		if row.Kind != KindPrimary || row.Hostname() != ref.Registry {
			continue
		}
		if !namespaceAllowed(*row, path) {
			continue
		}
		if best == nil || len(row.Namespace()) > len(best.Namespace()) {
			best, ambiguous = row, false
		} else if len(row.Namespace()) == len(best.Namespace()) {
			ambiguous = true
		}
	}
	if ambiguous {
		return Registry{}, ambiguousRegistry()
	}
	if best != nil {
		return *best, nil
	}
	// 公共访问策略：未配置的主机允许匿名查询。
	return Registry{Name: ref.Registry, Host: ref.Registry}, nil
}

func infoForRegistry(row Registry) *image.RegistryInfo {
	return &image.RegistryInfo{
		Username:           row.Username,
		Password:           row.Password,
		PlainHTTP:          row.PlainHTTP,
		InsecureSkipVerify: row.InsecureSkipVerify,
	}
}

func namespaceAllowed(row Registry, repository string) bool {
	ns := row.Namespace()
	return ns == "" || strings.HasPrefix(repository, strings.TrimSuffix(ns, "/")+"/")
}

// SearchRemoteRepositories 在配置的 Registry 内做有界 catalog 扫描
// （oci-sync control SearchRemoteRepositories 移植）。
func (s *Service) SearchRemoteRepositories(ctx context.Context, input string, options registry.SearchInput) (*registry.SearchPage[string], error) {
	rows, err := s.repo.List(ctx)
	if err != nil {
		return nil, err
	}
	row, err := configuredRegistry(rows, strings.TrimSpace(input))
	if err != nil {
		return nil, err
	}
	options.Namespace = row.Namespace()
	options.RegistryConfig = row.Name
	page, err := s.client.SearchRepositories(ctx, row.Hostname(), infoForRegistry(row), options)
	if err != nil {
		return nil, AsError(err)
	}
	return page, nil
}

// ListRemoteTags 分页列出仓库 tag，docker.io 走 mirror 优先（oci-sync
// control ListRemoteTags + PublicAllowed 移植）。
func (s *Service) ListRemoteTags(ctx context.Context, repository string, options registry.SearchInput, selected string) (*registry.SearchPage[string], error) {
	ref := image.ParseRepository(strings.TrimSpace(repository))
	if ref == nil {
		return nil, invalidInput(fmt.Errorf("invalid repository"))
	}
	rows, err := s.repo.List(ctx)
	if err != nil {
		return nil, err
	}
	row, err := repositoryRegistry(rows, ref, selected)
	if err != nil {
		return nil, err
	}
	options.Namespace = ""
	options.RegistryConfig = row.Name
	res := newResolver(rows)
	page, err := s.client.SearchTagsWithMirrors(ctx, ref.FullRepoName(), infoForRegistry(row), res, options)
	if err != nil {
		return nil, AsError(err)
	}
	return page, nil
}

// ListTags 是无分页的标签列表（GET /images/tags；oci-sync
// ListTagsLegacy 移植）：按字典序倒序，默认 tag 优先 latest。
func (s *Service) ListTags(ctx context.Context, input string) (*ImageTagsResult, error) {
	input = strings.TrimSpace(input)
	ref := image.ParseReference(input)
	var repo *image.ImageReference
	if ref != nil {
		repo = &image.ImageReference{Registry: ref.Registry, Namespace: ref.Namespace, RepoName: ref.RepoName}
	} else {
		repo = image.ParseRepository(input)
	}
	if repo == nil {
		return nil, invalidInput(fmt.Errorf("invalid repository: %s", input))
	}
	res, err := s.Resolver(ctx)
	if err != nil {
		return nil, err
	}
	cfg := res.RegistryByHost(repo.Registry)
	tags, err := s.client.ListTags(ctx, repo.FullRepoName(), infoForRegistryRow(cfg), res)
	if err != nil {
		return nil, AsError(err)
	}
	sort.Slice(tags, func(i, j int) bool { return tags[i] > tags[j] })
	defaultTag := ""
	if len(tags) > 0 {
		defaultTag = tags[0]
	}
	for _, tag := range tags {
		if tag == "latest" {
			defaultTag = tag
			break
		}
	}
	return &ImageTagsResult{Repo: repo.FullRepoName(), Tags: tags, DefaultTag: defaultTag}, nil
}

// infoForRegistryRow 把 resolver 给出的运行时配置转为 RegistryInfo。
func infoForRegistryRow(cfg *registry.RegistryConfig) *image.RegistryInfo {
	if cfg == nil {
		return &image.RegistryInfo{}
	}
	return &image.RegistryInfo{
		Username:           cfg.Username,
		Password:           cfg.Password,
		PlainHTTP:          cfg.PlainHTTP,
		InsecureSkipVerify: cfg.InsecureSkipVerify,
	}
}

// ImageTagsResult 是 GET /images/tags 的响应（oci-sync model 移植）。
type ImageTagsResult struct {
	Repo       string   `json:"repo"`
	Tags       []string `json:"tags"`
	DefaultTag string   `json:"default_tag"`
}

// InspectImage 查询远端镜像元数据；platform 非空时定位具体平台
// （oci-sync control InspectImage + PublicAllowed 移植）。selected 是
// registry 配置名（多配置同主机时消歧）。
func (s *Service) InspectImage(ctx context.Context, input string, platform *image.PlatformInfo, selected string) (*image.RemoteInspectResult, error) {
	ref := image.ParseReference(strings.TrimSpace(input))
	if ref == nil {
		return nil, invalidInput(fmt.Errorf("invalid image reference: %s", input))
	}
	rows, err := s.repo.List(ctx)
	if err != nil {
		return nil, err
	}
	row, err := repositoryRegistry(rows, ref, selected)
	if err != nil {
		return nil, err
	}
	src := &image.SrcImage{
		RepoName:  ref.FullRepoName(),
		Namespace: ref.Namespace,
		Tag:       ref.Tag,
		Registry:  infoForRegistry(row),
	}
	res := newResolver(rows)
	var result *image.RemoteInspectResult
	if platform != nil {
		result, err = s.client.InspectImagePlatform(ctx, src, *platform, res)
	} else {
		result, err = s.client.InspectImage(ctx, src, res)
	}
	if err != nil {
		return nil, AsError(err)
	}
	result.Ref = ref.FullName()
	return result, nil
}

// ---------------------------------------------------------------------------
// 错误分类（DiscoveryError → 稳定 code → REST 映射）
// ---------------------------------------------------------------------------

// Error 是镜像管理端点的业务错误（oci-sync control.Error 移植）：code
// 稳定可映射 HTTP 状态，message 不含远端凭据与 transport URL。
type Error struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
	Cause     error  `json:"-"`
}

func (e *Error) Error() string { return e.Message }

func (e *Error) Unwrap() error { return e.Cause }

func invalidInput(err error) error {
	return &Error{Code: "invalid_input", Message: err.Error(), Cause: err}
}

// AsError 把领域与远端错误归一为 *Error（oci-sync control AsError 移植）：
// DiscoveryError 透传其 code；transport 错误走远端分类；平台未找到映射
// platform_not_found。
func AsError(err error) *Error {
	var result *Error
	if errors.As(err, &result) {
		return result
	}
	var discovery *registry.DiscoveryError
	if errors.As(err, &discovery) {
		return &Error{Code: discovery.Code, Message: strings.ReplaceAll(discovery.Code, "_", " "), Retryable: discovery.Retryable, Cause: err}
	}
	var remoteErr *transport.Error
	if errors.As(err, &remoteErr) {
		return AsError(registry.ClassifyRemoteError(err))
	}
	if errors.Is(err, registry.ErrPlatformNotFound) {
		return &Error{Code: "platform_not_found", Message: err.Error(), Cause: err}
	}
	return &Error{Code: "internal_error", Message: "operation failed", Cause: err}
}

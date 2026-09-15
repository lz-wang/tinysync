package syncjob

import (
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"tinysync/internal/source"
)

// SourceService 是 Job 依赖的 Source 查询能力，解耦具体实现。
type SourceService interface {
	// Get 按 ID 读取 Source；不存在时返回 source.ErrNotFound。
	Get(ctx context.Context, id string) (source.Source, error)
}

// Service 是 Sync Job 的应用服务：校验、LocalRoot 归属保护、ID 与
// 时间戳生成、mapping 变更时的 metadata 安全释放；调用方不接触存储细节。
type Service struct {
	repo    Repository
	managed ManagedRepository
	sources SourceService
	// dataDir 是 TinySync 数据目录，同步 LocalRoot 不得与其重叠。
	dataDir string
	// Now 返回当前时间；默认 UTC time.Now，测试可注入固定时钟。
	Now func() time.Time
}

// NewService 构造应用服务；dataDir 用于归属保护检查。
func NewService(repo Repository, managed ManagedRepository, sources SourceService, dataDir string) *Service {
	return &Service{
		repo:    repo,
		managed: managed,
		sources: sources,
		dataDir: dataDir,
		Now:     func() time.Time { return time.Now().UTC() },
	}
}

// Create 校验并创建 Job。
func (s *Service) Create(ctx context.Context, input CreateInput) (Job, error) {
	name := strings.TrimSpace(input.Name)
	if name == "" {
		return Job{}, fmt.Errorf("%w: name is required", ErrInvalid)
	}
	if !input.Mode.Valid() {
		return Job{}, fmt.Errorf("%w: mode %q must be copy or mirror", ErrInvalid, input.Mode)
	}
	if _, err := s.sources.Get(ctx, input.SourceID); err != nil {
		return Job{}, err
	}
	remoteRoot, err := normalizeRemoteRoot(input.RemoteRoot)
	if err != nil {
		return Job{}, err
	}
	localRoot, err := s.canonicalLocalRoot(input.LocalRoot)
	if err != nil {
		return Job{}, err
	}
	if err := s.checkRootAvailable(ctx, localRoot, ""); err != nil {
		return Job{}, err
	}

	id, err := NewID()
	if err != nil {
		return Job{}, err
	}
	now := s.Now()
	job := Job{
		ID:         id,
		Name:       name,
		SourceID:   input.SourceID,
		RemoteRoot: remoteRoot,
		LocalRoot:  localRoot,
		Mode:       input.Mode,
		Include:    input.Include,
		Exclude:    input.Exclude,
		Enabled:    input.Enabled,
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	if err := s.repo.Create(ctx, job); err != nil {
		return Job{}, err
	}
	return job, nil
}

// Get 按 ID 读取 Job。
func (s *Service) Get(ctx context.Context, id string) (Job, error) {
	return s.repo.Get(ctx, id)
}

// List 返回全部 Job。
func (s *Service) List(ctx context.Context) ([]Job, error) {
	return s.repo.List(ctx)
}

// Update 按 ID 更新 Job，nil 字段保留现有值。mapping 字段（source /
// remoteRoot / localRoot）变更时安全释放 managed metadata——先释放再更新：
// 释放后更新失败时旧 mapping 下次 Run 会自然重建 metadata，而反向顺序
// 若更新成功、释放失败，残留记录会被 Mirror 误判为远端消失。
func (s *Service) Update(ctx context.Context, id string, input UpdateInput) (Job, error) {
	current, err := s.repo.Get(ctx, id)
	if err != nil {
		return Job{}, err
	}

	updated := current
	if input.Name != nil {
		name := strings.TrimSpace(*input.Name)
		if name == "" {
			return Job{}, fmt.Errorf("%w: name is required", ErrInvalid)
		}
		updated.Name = name
	}
	if input.Mode != nil {
		if !input.Mode.Valid() {
			return Job{}, fmt.Errorf("%w: mode %q must be copy or mirror", ErrInvalid, *input.Mode)
		}
		updated.Mode = *input.Mode
	}
	if input.SourceID != nil {
		if _, err := s.sources.Get(ctx, *input.SourceID); err != nil {
			return Job{}, err
		}
		updated.SourceID = *input.SourceID
	}
	if input.RemoteRoot != nil {
		remoteRoot, err := normalizeRemoteRoot(*input.RemoteRoot)
		if err != nil {
			return Job{}, err
		}
		updated.RemoteRoot = remoteRoot
	}
	if input.LocalRoot != nil {
		localRoot, err := s.canonicalLocalRoot(*input.LocalRoot)
		if err != nil {
			return Job{}, err
		}
		if err := s.checkRootAvailable(ctx, localRoot, id); err != nil {
			return Job{}, err
		}
		updated.LocalRoot = localRoot
	}
	if input.Include != nil {
		updated.Include = *input.Include
	}
	if input.Exclude != nil {
		updated.Exclude = *input.Exclude
	}
	if input.Enabled != nil {
		updated.Enabled = *input.Enabled
	}

	mappingChanged := updated.SourceID != current.SourceID ||
		updated.RemoteRoot != current.RemoteRoot ||
		updated.LocalRoot != current.LocalRoot
	if mappingChanged {
		if err := s.managed.DeleteAllForJob(ctx, id); err != nil {
			return Job{}, err
		}
	}
	updated.UpdatedAt = s.Now()
	if err := s.repo.Update(ctx, updated); err != nil {
		return Job{}, err
	}
	return updated, nil
}

// Delete 删除 Job；managed metadata 经 FK CASCADE 清理，真实本地文件不受影响。
func (s *Service) Delete(ctx context.Context, id string) error {
	return s.repo.Delete(ctx, id)
}

// normalizeRemoteRoot 把 RemoteRoot 归一为 canonical logical path：
// "/" 或空串为 root，其余必须落在 root 之下且不带尾斜杠。
// 归一化规则与 WebDAV adapter 的 logicalPath 一致（path.Clean、消除 ..）。
func normalizeRemoteRoot(root string) (string, error) {
	cleaned := strings.TrimSuffix(path.Clean("/"+strings.TrimSpace(root)), "/")
	if cleaned == "" {
		return "/", nil
	}
	return cleaned, nil
}

// canonicalLocalRoot 校验 LocalRoot：必须存在、是目录，并归一为
// abs + clean + EvalSymlinks 的 canonical 绝对路径。
func (s *Service) canonicalLocalRoot(root string) (string, error) {
	trimmed := strings.TrimSpace(root)
	if trimmed == "" {
		return "", fmt.Errorf("%w: local root is required", ErrInvalid)
	}
	abs, err := filepath.Abs(trimmed)
	if err != nil {
		return "", fmt.Errorf("%w: resolve local root %s: %w", ErrInvalid, root, err)
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", fmt.Errorf("%w: local root %s must exist: %w", ErrInvalid, root, err)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", fmt.Errorf("%w: stat local root %s: %w", ErrInvalid, root, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%w: local root %s is not a directory", ErrInvalid, root)
	}
	return resolved, nil
}

// checkRootAvailable 断言 canonical localRoot 不与任何其他 Job（skipID
// 除外）或 dataDir 重叠：相等、父子包含一律拒绝。
func (s *Service) checkRootAvailable(ctx context.Context, localRoot, skipID string) error {
	jobs, err := s.repo.List(ctx)
	if err != nil {
		return err
	}
	dataDir, err := s.canonicalDataDir()
	if err != nil {
		return err
	}
	if rootsOverlap(localRoot, dataDir) {
		return fmt.Errorf("%w: %s overlaps tinysync data dir %s", ErrRootOverlap, localRoot, dataDir)
	}
	for _, job := range jobs {
		if job.ID == skipID {
			continue
		}
		if rootsOverlap(localRoot, job.LocalRoot) {
			return fmt.Errorf("%w: %s overlaps %q local root %s", ErrRootOverlap, localRoot, job.Name, job.LocalRoot)
		}
	}
	return nil
}

// canonicalDataDir 返回归一化的数据目录绝对路径。
func (s *Service) canonicalDataDir() (string, error) {
	abs, err := filepath.Abs(s.dataDir)
	if err != nil {
		return "", fmt.Errorf("resolve data dir %s: %w", s.dataDir, err)
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		return resolved, nil
	}
	return abs, nil
}

// rootsOverlap 判断两个 canonical 绝对路径是否相等或存在父子包含；
// Windows 路径大小写不敏感。
func rootsOverlap(a, b string) bool {
	if runtime.GOOS == "windows" {
		a = strings.ToLower(a)
		b = strings.ToLower(b)
	}
	return sameOrUnder(a, b) || sameOrUnder(b, a)
}

// sameOrUnder 判断 child 是否等于 parent 或位于 parent 之下（按分隔符对齐）。
func sameOrUnder(parent, child string) bool {
	if parent == child {
		return true
	}
	return strings.HasPrefix(child, parent+string(filepath.Separator))
}

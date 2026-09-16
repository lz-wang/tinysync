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
// 时间戳生成、mapping 变更时的原子 metadata 释放；调用方不接触存储细节。
type Service struct {
	repo    Repository
	sources SourceService
	// dataDir 是 TinySync 数据目录，同步 LocalRoot 不得与其重叠。
	dataDir string
	// Now 返回当前时间；默认 UTC time.Now，测试可注入固定时钟。
	Now func() time.Time
}

// NewService 构造应用服务；dataDir 用于归属保护检查。
func NewService(repo Repository, sources SourceService, dataDir string) *Service {
	return &Service{
		repo:    repo,
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
	// pattern 在配置入口即时校验：非法 pattern 直接拒绝（400），
	// 不留到首次 Run 才静默失败。
	if _, err := NewSelector(input.Include, input.Exclude); err != nil {
		return Job{}, err
	}
	// schedule 同样在配置入口校验；缺省 manual 保证与 v0.3 行为一致。
	schedule := Schedule{Type: ScheduleManual}
	if input.Schedule != nil {
		if err := input.Schedule.Validate(); err != nil {
			return Job{}, err
		}
		schedule = input.Schedule.Normalized()
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
	if schedule.Type == ScheduleInterval {
		// interval 相位从创建时刻起算（anchor），重启后按持久化值恢复。
		anchor := now
		schedule.AnchorAt = &anchor
	}
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
		Schedule:   schedule,
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
// remoteRoot / localRoot）变更时在单个事务中原子替换配置并释放 managed
// metadata——分开执行存在半成功状态（如更新撞 name 冲突而 metadata
// 已清空，Job 将永久失去对本地文件的管理关系）。
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
	if input.Schedule != nil {
		// schedule 原子替换：整体校验归一后覆盖。语义变化时 interval
		// 的相位基准随变更重设（anchor = now），once 的消费状态随之
		// 清空（新 occurrence 未执行）；语义未变的同值更新两者都保留，
		// 不被无关的 PATCH 重置。其他类型不携带 anchor 与消费状态。
		if err := input.Schedule.Validate(); err != nil {
			return Job{}, err
		}
		normalized := input.Schedule.Normalized()
		if current.Schedule.IntentEqual(normalized) {
			normalized.AnchorAt = current.Schedule.AnchorAt
			updated.OnceConsumedFor = current.OnceConsumedFor
		} else {
			if normalized.Type == ScheduleInterval {
				anchor := s.Now()
				normalized.AnchorAt = &anchor
			} else {
				normalized.AnchorAt = nil
			}
			updated.OnceConsumedFor = nil
		}
		updated.Schedule = normalized
	}
	// 校验合并后的最终 pattern 集合：与 Create 一致，配置入口即拒绝
	// 非法 pattern（mapping 未变时不触碰 managed metadata）。
	if input.Include != nil || input.Exclude != nil {
		if _, err := NewSelector(updated.Include, updated.Exclude); err != nil {
			return Job{}, err
		}
	}

	mappingChanged := updated.SourceID != current.SourceID ||
		updated.RemoteRoot != current.RemoteRoot ||
		updated.LocalRoot != current.LocalRoot
	updated.UpdatedAt = s.Now()
	if mappingChanged {
		if err := s.repo.UpdateAndResetManaged(ctx, updated); err != nil {
			return Job{}, err
		}
		return updated, nil
	}
	if err := s.repo.Update(ctx, updated); err != nil {
		return Job{}, err
	}
	return updated, nil
}

// Delete 删除 Job；managed metadata 经 FK CASCADE 清理，真实本地文件不受影响。
func (s *Service) Delete(ctx context.Context, id string) error {
	return s.repo.Delete(ctx, id)
}

// CountBySource 统计引用给定 Source 的 Job 数，供 Source 删除保护使用。
func (s *Service) CountBySource(ctx context.Context, sourceID string) (int, error) {
	return s.repo.CountBySource(ctx, sourceID)
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

// sameOrUnder 判断 child 是否等于 parent 或位于 parent 之下。
// 用 filepath.Rel 做 containment 判定，对 "/" 与 Windows 卷根等
// 自带 trailing separator 的根路径同样正确（前缀拼接会把 parent+"/"
// 变成 "//" 而漏判）。
func sameOrUnder(parent, child string) bool {
	rel, err := filepath.Rel(parent, child)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

package publish

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"time"

	"tinysync/internal/filesafe"
	"tinysync/internal/source"
	"tinysync/internal/syncjob"
)

// newIDSize 是随机部分的字节数（128 bit）。
const newIDSize = 16

// NewID 生成 pub_<128-bit random hex> 形式的唯一 ID，不引入 UUID 依赖。
func NewID() (string, error) {
	buf := make([]byte, newIDSize)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate published file id: %w", err)
	}
	return idPrefix + hex.EncodeToString(buf), nil
}

// JobLookup 提供 Job 读取；*syncjob.Service 天然满足。
type JobLookup interface {
	Get(ctx context.Context, id string) (syncjob.Job, error)
}

// Service 是发布策略的应用服务：REST API / Web UI / serving 共用的
// 业务入口。local_path 只能经 Create 的校验链产生（Job + LocalRoot
// 内逻辑路径 → 存在 / 普通文件 / managed 检查 → canonical 绝对
// 路径），API 无法借 Publish 浏览任意主机文件。
type Service struct {
	repo    Repository
	jobs    JobLookup
	managed syncjob.ManagedRepository
	// Now 返回当前时间；默认 UTC time.Now，测试可注入固定时钟。
	Now func() time.Time
}

// NewService 构造应用服务。
func NewService(repo Repository, jobs JobLookup, managed syncjob.ManagedRepository) *Service {
	return &Service{
		repo:    repo,
		jobs:    jobs,
		managed: managed,
		Now:     func() time.Time { return time.Now().UTC() },
	}
}

// Create 校验并创建发布策略：
//
//	Job.LocalRoot + logical path
//	  ↓ safeResolve（root confinement + symlink 拒绝）
//	  ↓ 必须是普通文件
//	  ↓ managed == true（不发布 Job 目录里的未知私人文件）
//	  ↓ canonical local_path
//	  ↓ published_files
//
// 目标不存在 / 非普通文件 / 未 managed / public_path 冲突都以 400 /
// 409 语义失败（ErrInvalid / ErrConflict / ErrNotFound）。
func (s *Service) Create(ctx context.Context, input CreateInput) (PublishedFile, error) {
	if err := ValidateCreateInput(input); err != nil {
		return PublishedFile{}, err
	}
	job, err := s.jobs.Get(ctx, input.JobID)
	if err != nil {
		if errors.Is(err, syncjob.ErrNotFound) {
			return PublishedFile{}, fmt.Errorf("%w: job not found", ErrNotFound)
		}
		return PublishedFile{}, err
	}
	resolved, _, err := filesafe.ResolveRegularFile(job.LocalRoot, input.Path)
	if err != nil {
		switch {
		case errors.Is(err, fs.ErrNotExist):
			return PublishedFile{}, fmt.Errorf("%w: publish target %s does not exist", source.ErrInvalid, input.Path)
		case errors.Is(err, filesafe.ErrNotRegularFile), errors.Is(err, filesafe.ErrEscape):
			return PublishedFile{}, fmt.Errorf("%w: %v", source.ErrInvalid, err)
		default:
			return PublishedFile{}, err
		}
	}
	managed, err := s.managed.ListByJob(ctx, input.JobID)
	if err != nil {
		return PublishedFile{}, err
	}
	if !isManaged(managed, input.Path) {
		return PublishedFile{}, fmt.Errorf(
			"%w: %s is not managed by job %s; only synced files can be published",
			source.ErrInvalid, input.Path, input.JobID)
	}

	publicPath, err := filesafe.NormalizePublicPath(input.PublicPath)
	if err != nil {
		return PublishedFile{}, fmt.Errorf("%w: %v", source.ErrInvalid, err)
	}
	if input.ExpiresAt != nil && !input.ExpiresAt.After(s.Now()) {
		return PublishedFile{}, fmt.Errorf("%w: expires_at must be in the future", source.ErrInvalid)
	}
	id, err := NewID()
	if err != nil {
		return PublishedFile{}, err
	}
	now := s.Now()
	policy := PublishedFile{
		ID:         id,
		LocalPath:  resolved,
		PublicPath: publicPath,
		Enabled:    input.Enabled,
		ExpiresAt:  cloneTime(input.ExpiresAt),
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	if err := s.repo.Create(ctx, policy); err != nil {
		return PublishedFile{}, err
	}
	return policy, nil
}

// Get 按 ID 读取策略。
func (s *Service) Get(ctx context.Context, id string) (PublishedFile, error) {
	return s.repo.Get(ctx, id)
}

// List 返回全部策略，顺序稳定。
func (s *Service) List(ctx context.Context) ([]PublishedFile, error) {
	return s.repo.List(ctx)
}

// Update 部分更新策略：public_path / enabled / expires_at 可变；
// local_path 不可变。public_path 冲突返回 ErrConflict。
func (s *Service) Update(ctx context.Context, id string, input UpdateInput) (PublishedFile, error) {
	if err := ValidateUpdateInput(input); err != nil {
		return PublishedFile{}, err
	}
	current, err := s.repo.Get(ctx, id)
	if err != nil {
		return PublishedFile{}, err
	}
	if input.ExpiresAt != nil && !input.ExpiresAt.After(s.Now()) {
		return PublishedFile{}, fmt.Errorf("%w: expires_at must be in the future", source.ErrInvalid)
	}
	if input.PublicPath != nil {
		normalized, normErr := filesafe.NormalizePublicPath(*input.PublicPath)
		if normErr != nil {
			return PublishedFile{}, fmt.Errorf("%w: %v", source.ErrInvalid, normErr)
		}
		current.PublicPath = normalized
	}
	if input.Enabled != nil {
		current.Enabled = *input.Enabled
	}
	switch {
	case input.ClearExpires:
		current.ExpiresAt = nil
	case input.ExpiresAt != nil:
		current.ExpiresAt = cloneTime(input.ExpiresAt)
	}
	current.UpdatedAt = s.Now()
	if err := s.repo.Update(ctx, current); err != nil {
		return PublishedFile{}, err
	}
	return current, nil
}

// Delete 按 ID 删除策略；只移除本地策略记录，不触及文件。
func (s *Service) Delete(ctx context.Context, id string) error {
	return s.repo.Delete(ctx, id)
}

// ResolveForRequest 把公开路径解析为当前可服务的本地文件：策略存在、
// enabled、未过期、文件存在且仍是普通文件。任何不满足都返回
// ErrNotFound——不区分「存在但禁止」与「不存在」，减少资源信息
// 泄露。返回 canonical 路径与文件信息供 serving 打开。
func (s *Service) ResolveForRequest(ctx context.Context, publicPath string) (PublishedFile, os.FileInfo, error) {
	policy, err := s.repo.GetByPublicPath(ctx, publicPath)
	if err != nil {
		return PublishedFile{}, nil, fmt.Errorf("%w: %s", ErrNotFound, publicPath)
	}
	if !policy.Enabled || policy.Expired(s.Now()) {
		return PublishedFile{}, nil, fmt.Errorf("%w: %s", ErrNotFound, publicPath)
	}
	// local_path 是 canonical 绝对路径；serving 时重新校验——文件
	// 可能已被删除或替换为 symlink / 目录。
	info, err := os.Stat(policy.LocalPath)
	if err != nil || !info.Mode().IsRegular() {
		return PublishedFile{}, nil, fmt.Errorf("%w: %s", ErrNotFound, publicPath)
	}
	return policy, info, nil
}

// isManaged 判断逻辑路径（LocalRoot 内相对 slash 形式）是否在该 Job
// 的 managed 记录中。
func isManaged(files []syncjob.ManagedFile, logicalPath string) bool {
	rel := logicalPath
	if len(rel) > 0 && rel[0] == '/' {
		rel = rel[1:]
	}
	for _, f := range files {
		if f.LocalRelPath == rel {
			return true
		}
	}
	return false
}

func cloneTime(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	utc := t.UTC()
	return &utc
}

package share

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"time"

	"tinysync/internal/filesafe"
	"tinysync/internal/source"
	"tinysync/internal/syncjob"
)

// newIDSize 是随机部分的字节数（128 bit）。
const newIDSize = 16

// NewID 生成 shr_<128-bit random hex> 形式的唯一 ID，不引入 UUID 依赖。
func NewID() (string, error) {
	buf := make([]byte, newIDSize)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate share id: %w", err)
	}
	return idPrefix + hex.EncodeToString(buf), nil
}

// JobLookup 提供 Job 读取；*syncjob.Service 天然满足。
type JobLookup interface {
	Get(ctx context.Context, id string) (syncjob.Job, error)
}

// Service 是共享策略的应用服务：REST API / Web UI / 公开 serving
// 共用的业务入口。local_path 只能经 Create 的校验链产生（Job +
// LocalRoot 内逻辑路径 → 存在 / 普通文件或目录 / 非 symlink →
// canonical 绝对路径），API 无法借共享浏览任意主机文件。目录共享
// 不做 managed 过滤（ADR-0002：共享目录即整棵公开）。
type Service struct {
	repo Repository
	jobs JobLookup
	// Now 返回当前时间；默认 UTC time.Now，测试可注入固定时钟。
	Now func() time.Time
}

// NewService 构造应用服务。
func NewService(repo Repository, jobs JobLookup) *Service {
	return &Service{
		repo: repo,
		jobs: jobs,
		Now:  func() time.Time { return time.Now().UTC() },
	}
}

// Create 校验并创建共享策略：
//
//	Job.LocalRoot + logical path
//	  ↓ ResolveCanonicalTarget（root confinement + symlink 拒绝，
//	    目标可以是文件 / 目录 / 本地根本身）
//	  ↓ canonical local_path
//	  ↓ shares
//
// 目标不存在 / 非普通文件或目录 / 名称非法 / slug 冲突都以 400 /
// 409 / 404 语义失败（ErrInvalid / ErrConflict / ErrNotFound）。
func (s *Service) Create(ctx context.Context, input CreateInput) (Share, error) {
	if err := ValidateCreateInput(input); err != nil {
		return Share{}, err
	}
	job, err := s.jobs.Get(ctx, input.JobID)
	if err != nil {
		if errors.Is(err, syncjob.ErrNotFound) {
			return Share{}, fmt.Errorf("%w: job not found", ErrNotFound)
		}
		return Share{}, err
	}
	resolved, info, err := filesafe.ResolveCanonicalTarget(job.LocalRoot, input.Path)
	if err != nil {
		switch {
		case errors.Is(err, fs.ErrNotExist):
			return Share{}, fmt.Errorf("%w: share target %s does not exist", source.ErrInvalid, input.Path)
		case errors.Is(err, filesafe.ErrNotRegularFile), errors.Is(err, filesafe.ErrEscape):
			return Share{}, fmt.Errorf("%w: %v", source.ErrInvalid, err)
		default:
			return Share{}, err
		}
	}
	slug := input.Name
	if slug == "" {
		slug, err = RandomSlug()
		if err != nil {
			return Share{}, err
		}
	}
	if input.ExpiresAt != nil && !input.ExpiresAt.After(s.Now()) {
		return Share{}, fmt.Errorf("%w: expires_at must be in the future", source.ErrInvalid)
	}
	id, err := NewID()
	if err != nil {
		return Share{}, err
	}
	now := s.Now()
	created := Share{
		ID:        id,
		LocalPath: resolved,
		Slug:      slug,
		IsDir:     info.IsDir(),
		Enabled:   input.Enabled,
		ExpiresAt: cloneTime(input.ExpiresAt),
		CreatedAt: now,
		UpdatedAt: now,
	}
	if input.Name != "" {
		name := input.Name
		created.Name = &name
	}
	if err := s.repo.Create(ctx, created); err != nil {
		return Share{}, err
	}
	return created, nil
}

// Get 按 ID 读取共享。
func (s *Service) Get(ctx context.Context, id string) (Share, error) {
	return s.repo.Get(ctx, id)
}

// List 返回全部共享，顺序稳定（slug 字典序）。
func (s *Service) List(ctx context.Context) ([]Share, error) {
	return s.repo.List(ctx)
}

// Update 部分更新共享：name（联动 slug）/ enabled / expires_at 可变；
// local_path 与 is_dir 不可变。slug 冲突返回 ErrConflict。
func (s *Service) Update(ctx context.Context, id string, input UpdateInput) (Share, error) {
	if err := ValidateUpdateInput(input); err != nil {
		return Share{}, err
	}
	current, err := s.repo.Get(ctx, id)
	if err != nil {
		return Share{}, err
	}
	if input.ExpiresAt != nil && !input.ExpiresAt.After(s.Now()) {
		return Share{}, fmt.Errorf("%w: expires_at must be in the future", source.ErrInvalid)
	}
	if input.Name != nil {
		if *input.Name == "" {
			// 清除自定义名称：展示回落 basename，slug 保持不变。
			current.Name = nil
		} else {
			name := *input.Name
			current.Name = &name
			current.Slug = name
		}
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
		return Share{}, err
	}
	return current, nil
}

// Delete 按 ID 删除共享；只移除本地策略记录，不触及文件。
func (s *Service) Delete(ctx context.Context, id string) error {
	return s.repo.Delete(ctx, id)
}

// ResolveForRequest 把 slug 解析为当前可服务的共享：共享存在、
// enabled、未过期。任何不满足都返回裸 ErrNotFound——不区分「存在
// 但禁止」与「不存在」，响应同形，减少资源信息泄露（文件系统层面
// 的身份复验由公开 serving / 浏览在访问时完成——路径代数校验
// （创建时）与真实访问共用 filesafe 原语）。
func (s *Service) ResolveForRequest(ctx context.Context, slug string) (Share, error) {
	found, err := s.repo.GetBySlug(ctx, slug)
	if err != nil {
		return Share{}, ErrNotFound
	}
	if !found.Enabled || found.Expired(s.Now()) {
		return Share{}, ErrNotFound
	}
	return found, nil
}

// OpenFile 打开 slug 对应共享内逻辑路径指向的普通文件，供公开直链
// serving：
//   - 文件共享：唯一可服务路径是 "/"+basename(LocalPath)，经
//     filesafe.OpenCanonicalRegularFile 复验 canonical 身份；
//   - 目录共享：先复验共享根身份（最终组件非 symlink、全链解析
//     仍等于持久化 canonical 路径），再按 ResolveRegularFile 做
//     root confinement 与 symlink 拒绝。
//
// 共享不可服务（禁用/过期/不存在）或路径不可用（缺失、指向目录、
// 逃逸、身份偏离）一律映射 ErrNotFound 语义——serving 侧同形 404，
// 不泄露共享与文件系统的当前状态。返回下载文件名（逻辑路径的
// 最后一段）与打开的文件。
func (s *Service) OpenFile(ctx context.Context, slug, logicalPath string) (string, *os.File, os.FileInfo, error) {
	found, err := s.ResolveForRequest(ctx, slug)
	if err != nil {
		return "", nil, nil, err
	}
	if !found.IsDir {
		if logicalPath != "/"+filepath.Base(found.LocalPath) {
			return "", nil, nil, fmt.Errorf("%w: %s is not served by this share", ErrNotFound, logicalPath)
		}
		f, info, err := filesafe.OpenCanonicalRegularFile(found.LocalPath)
		if err != nil {
			return "", nil, nil, fmt.Errorf("%w: %v", ErrNotFound, err)
		}
		return path.Base(logicalPath), f, info, nil
	}
	if err := verifyDirRoot(found.LocalPath); err != nil {
		return "", nil, nil, fmt.Errorf("%w: %v", ErrNotFound, err)
	}
	resolved, info, err := filesafe.ResolveRegularFile(found.LocalPath, logicalPath)
	if err != nil {
		return "", nil, nil, fmt.Errorf("%w: %v", ErrNotFound, err)
	}
	f, err := os.Open(resolved)
	if err != nil {
		return "", nil, nil, fmt.Errorf("%w: %v", ErrNotFound, err)
	}
	return path.Base(logicalPath), f, info, nil
}

// verifyDirRoot 复验目录共享根的身份：最终组件非 symlink、是目录、
// 全链解析仍等于持久化 canonical 路径。创建后根被删除或替换为
// symlink 时，公开访问按不存在处理（与文件共享的
// OpenCanonicalRegularFile 复验同一语义）。
func verifyDirRoot(localPath string) error {
	info, err := os.Lstat(localPath)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%w: %s is a symlink", filesafe.ErrNotRegularFile, localPath)
	}
	if !info.IsDir() {
		return fmt.Errorf("%w: %s is not a directory", filesafe.ErrNotRegularFile, localPath)
	}
	resolved, err := filepath.EvalSymlinks(localPath)
	if err != nil {
		return err
	}
	if resolved != localPath {
		return fmt.Errorf("%w: %s now resolves to %s via symlink", filesafe.ErrEscape, localPath, resolved)
	}
	return nil
}

func cloneTime(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	utc := t.UTC()
	return &utc
}

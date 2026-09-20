package share

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
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

func cloneTime(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	utc := t.UTC()
	return &utc
}

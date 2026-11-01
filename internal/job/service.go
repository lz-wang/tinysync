package job

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Service 是统一任务的应用服务：公共字段（name / enabled / schedule）
// 的校验与合并、经 Executor 的领域配置校验、变更事务钩子与善后回调的
// 编排；调用方不接触存储细节。
type Service struct {
	repo Repository
	// executors 是按类型静态注册的执行器表；未注册的类型不可创建。
	executors map[JobKind]Executor
	// Now 返回当前时间；默认 UTC time.Now，测试可注入固定时钟。
	Now func() time.Time
}

// NewService 构造应用服务。executors 为 nil 时任何类型都不可创建
// （测试装配可按需注入子集）。
func NewService(repo Repository, executors map[JobKind]Executor) *Service {
	return &Service{
		repo:      repo,
		executors: executors,
		Now:       func() time.Time { return time.Now().UTC() },
	}
}

// executor 返回类型的执行器；未注册返回 ErrKindUnsupported。
func (s *Service) executor(kind JobKind) (Executor, error) {
	exec, ok := s.executors[kind]
	if !ok {
		return nil, fmt.Errorf("%w: kind %q has no executor", ErrKindUnsupported, kind)
	}
	return exec, nil
}

// normalizedConfig 归一配置 JSON：nil / 空串归一为 {}。
func normalizedConfig(cfg json.RawMessage) (json.RawMessage, error) {
	trimmed := strings.TrimSpace(string(cfg))
	if trimmed == "" {
		return json.RawMessage("{}"), nil
	}
	if !json.Valid([]byte(trimmed)) {
		return nil, fmt.Errorf("%w: config is not valid JSON", ErrInvalid)
	}
	return json.RawMessage(trimmed), nil
}

// Create 校验并创建 Job：公共字段校验 → 执行器领域校验 → 落库。
func (s *Service) Create(ctx context.Context, input CreateInput) (Job, error) {
	name := strings.TrimSpace(input.Name)
	if name == "" {
		return Job{}, fmt.Errorf("%w: name is required", ErrInvalid)
	}
	if !input.Kind.Valid() {
		return Job{}, fmt.Errorf("%w: unknown kind %q", ErrInvalid, input.Kind)
	}
	exec, err := s.executor(input.Kind)
	if err != nil {
		return Job{}, err
	}
	config, err := normalizedConfig(input.Config)
	if err != nil {
		return Job{}, err
	}
	// schedule 在配置入口即时校验：非法表达式直接拒绝，不留到首次
	// Run 才静默失败。缺省 manual。
	schedule := Schedule{Type: ScheduleManual}
	if input.Schedule != nil {
		if err := input.Schedule.Validate(); err != nil {
			return Job{}, err
		}
		schedule = input.Schedule.Normalized()
	}
	if schedule.Type == ScheduleInterval {
		// interval 相位从创建时刻起算（anchor），重启后按持久化值恢复。
		anchor := s.Now()
		schedule.AnchorAt = &anchor
	}
	normalized, err := exec.Validate(ctx, "", config)
	if err != nil {
		return Job{}, err
	}
	if normalized != nil {
		config = normalized
	}
	id, err := NewID()
	if err != nil {
		return Job{}, err
	}
	now := s.Now()
	created := Job{
		ID:        id,
		Name:      name,
		Kind:      input.Kind,
		Enabled:   input.Enabled,
		Schedule:  schedule,
		Config:    config,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := s.repo.Create(ctx, created); err != nil {
		return Job{}, err
	}
	return created, nil
}

// Get 按 ID 读取 Job。
func (s *Service) Get(ctx context.Context, id string) (Job, error) {
	return s.repo.Get(ctx, id)
}

// List 返回全部 Job。
func (s *Service) List(ctx context.Context) ([]Job, error) {
	return s.repo.List(ctx)
}

// Update 按 ID 更新 Job，nil 字段保留现有值。kind 不可变更。schedule
// 原子替换：语义变化时 interval 的相位基准随变更重设（anchor = now）、
// once 的消费状态清空；语义未变的同值更新两者都保留。Config 提供时经
// 执行器校验后整体替换，领域状态重置钩子随更新同事务执行。
func (s *Service) Update(ctx context.Context, id string, input UpdateInput) (Job, error) {
	current, err := s.repo.Get(ctx, id)
	if err != nil {
		return Job{}, err
	}
	exec, err := s.executor(current.Kind)
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
	if input.Enabled != nil {
		updated.Enabled = *input.Enabled
	}
	configChanged := false
	if input.Config != nil {
		config, cerr := normalizedConfig(*input.Config)
		if cerr != nil {
			return Job{}, cerr
		}
		updated.Config = config
		configChanged = true
	}
	if input.Schedule != nil {
		if err := input.Schedule.Validate(); err != nil {
			return Job{}, err
		}
		normalized := input.Schedule.Normalized()
		if current.Schedule.IntentEqual(normalized) {
			normalized.AnchorAt = current.Schedule.AnchorAt
			updated.OnceConsumedAt = current.OnceConsumedAt
		} else {
			if normalized.Type == ScheduleInterval {
				anchor := s.Now()
				normalized.AnchorAt = &anchor
			} else {
				normalized.AnchorAt = nil
			}
			updated.OnceConsumedAt = nil
		}
		updated.Schedule = normalized
	}
	if configChanged {
		normalized, verr := exec.Validate(ctx, id, updated.Config)
		if verr != nil {
			return Job{}, verr
		}
		if normalized != nil {
			updated.Config = normalized
		}
	}
	updated.UpdatedAt = s.Now()

	var hooks []TxHook
	if aware, ok := exec.(ChangeAwareExecutor); ok {
		hooks, err = aware.UpdateHooks(ctx, current, updated)
		if err != nil {
			return Job{}, err
		}
	}
	if err := s.repo.Update(ctx, updated, hooks...); err != nil {
		return Job{}, err
	}
	if aware, ok := exec.(ChangeAwareExecutor); ok {
		aware.AfterUpdate(ctx, current, updated)
	}
	return updated, nil
}

// Delete 删除 Job；领域状态表经 FK CASCADE 清理，执行器的善后回调
// （如 File 的孤儿 partial 回收）在删除提交后尽力而为执行。
func (s *Service) Delete(ctx context.Context, id string) error {
	current, err := s.repo.Get(ctx, id)
	if err != nil {
		return err
	}
	exec, err := s.executor(current.Kind)
	if err != nil {
		return err
	}
	if err := s.repo.Delete(ctx, id); err != nil {
		return err
	}
	if aware, ok := exec.(ChangeAwareExecutor); ok {
		aware.AfterDelete(ctx, current)
	}
	return nil
}

// CountBySource 统计引用给定 Source 的 file 任务数，供 Source 删除保护
// 使用（领域校验由 File 执行器负责，这里只透传计数）。
func (s *Service) CountBySource(ctx context.Context, sourceID string) (int, error) {
	return s.repo.CountBySource(ctx, sourceID)
}

package notification

import (
	"context"
	"fmt"
	"time"
)

// Service 是通知配置的应用服务：REST / Web UI 共用的读写入口。
// 配置每次通知时热读取（发送器不缓存），WebUI 保存即生效。
type Service struct {
	repo Repository
	// Now 返回当前时间；默认 UTC time.Now，测试可注入固定时钟。
	Now func() time.Time
}

// NewService 构造应用服务。
func NewService(repo Repository) *Service {
	return &Service{
		repo: repo,
		Now:  func() time.Time { return time.Now().UTC() },
	}
}

// Settings 返回当前配置快照（含 secret，仅供发送路径与保存合并；
// API 层不得直接回显）。
func (s *Service) Settings(ctx context.Context) (Settings, error) {
	settings, err := s.repo.Load(ctx)
	if err != nil {
		return Settings{}, fmt.Errorf("load notification settings: %w", err)
	}
	return settings, nil
}

// PatchInput 是配置的部分更新输入。nil 指针 / nil 切片一律「保留现有
// 值」；secret 字段（Token / UserKey / Password）额外支持 Clear*
// 删除——空字符串不承担「清除」语义，避免一个值同时解释为保留与
// 删除（ADR 0006 三态契约）。
type PatchInput struct {
	Pushover *PushoverPatch
	Email    *EmailPatch
}

// PushoverPatch 是 Pushover 渠道的部分更新。
type PushoverPatch struct {
	Enabled *bool
	// Token 为 nil 保留；非 nil 替换；ClearToken 删除（优先于 Token）。
	Token      *string
	ClearToken bool
	// UserKey 语义同 Token。
	UserKey      *string
	ClearUserKey bool
}

// EmailPatch 是邮件渠道的部分更新。非 secret 字段 nil 保留、非 nil
// 替换（空串即清空）；To 为 nil 保留、非 nil 整体替换（含空切片）。
type EmailPatch struct {
	Enabled  *bool
	Host     *string
	Port     *int
	Security *Security
	Username *string
	From     *string
	To       []string
	// Password 为 nil 保留；非 nil 替换；ClearPassword 删除。
	Password      *string
	ClearPassword bool
}

// Update 应用部分更新：Load 现有配置 → 合并三态字段 → 归一化默认值
// → 校验 → Save，返回更新后的完整配置。校验失败不落库。
func (s *Service) Update(ctx context.Context, input PatchInput) (Settings, error) {
	current, err := s.repo.Load(ctx)
	if err != nil {
		return Settings{}, fmt.Errorf("load notification settings: %w", err)
	}
	next := current
	if input.Pushover != nil {
		if input.Pushover.Enabled != nil {
			next.Pushover.Enabled = *input.Pushover.Enabled
		}
		if input.Pushover.ClearToken {
			next.Pushover.Token = ""
		} else if input.Pushover.Token != nil {
			next.Pushover.Token = *input.Pushover.Token
		}
		if input.Pushover.ClearUserKey {
			next.Pushover.UserKey = ""
		} else if input.Pushover.UserKey != nil {
			next.Pushover.UserKey = *input.Pushover.UserKey
		}
	}
	if input.Email != nil {
		if input.Email.Enabled != nil {
			next.Email.Enabled = *input.Email.Enabled
		}
		if input.Email.Host != nil {
			next.Email.Host = *input.Email.Host
		}
		if input.Email.Port != nil {
			next.Email.Port = *input.Email.Port
		}
		if input.Email.Security != nil {
			next.Email.Security = *input.Email.Security
		}
		if input.Email.Username != nil {
			next.Email.Username = *input.Email.Username
		}
		if input.Email.From != nil {
			next.Email.From = *input.Email.From
		}
		if input.Email.To != nil {
			next.Email.To = input.Email.To
		}
		if input.Email.ClearPassword {
			next.Email.Password = ""
		} else if input.Email.Password != nil {
			next.Email.Password = *input.Email.Password
		}
	}
	// 归一化默认值：Port / Security 的零值表示「未设置」，落库前收敛到
	// schema 默认；空收件列表统一为 nil（存储与回显语义一致）。
	if next.Email.Port == 0 {
		next.Email.Port = DefaultEmailPort
	}
	if next.Email.Security == "" {
		next.Email.Security = DefaultEmailSecurity
	}
	if len(next.Email.To) == 0 {
		next.Email.To = nil
	}
	if err := next.Validate(); err != nil {
		return Settings{}, err
	}
	updatedAt := s.Now()
	next.UpdatedAt = updatedAt
	if err := s.repo.Save(ctx, next, updatedAt); err != nil {
		return Settings{}, fmt.Errorf("save notification settings: %w", err)
	}
	return next, nil
}

package credential

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// Service 是 Credential 的应用服务：REST API 与 Web UI 共用的业务
// 入口。统一处理校验、ID 与时间戳生成、私钥解析与指纹派生；调用方
// 不接触存储细节。
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

// Create 校验并创建凭据：私钥必须可解析，公钥指纹随解析派生。类型
// 只支持 ssh_key；校验失败返回 ErrInvalid / ErrUnsupportedType。
func (s *Service) Create(ctx context.Context, input CreateInput) (Credential, error) {
	if input.Type != TypeSSHKey {
		return Credential{}, fmt.Errorf("%w: %q", ErrUnsupportedType, input.Type)
	}
	name := strings.TrimSpace(input.Name)
	if err := ValidateName(name); err != nil {
		return Credential{}, err
	}
	fingerprint, err := ParseSecret(input.Secret)
	if err != nil {
		return Credential{}, err
	}
	id, err := NewID()
	if err != nil {
		return Credential{}, err
	}
	now := s.Now()
	c := Credential{
		ID:            id,
		Name:          name,
		Type:          input.Type,
		Fingerprint:   fingerprint,
		HasPassphrase: input.Secret.PrivateKeyPassphrase != "",
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	if err := s.repo.Create(ctx, c, input.Secret); err != nil {
		return Credential{}, err
	}
	return c, nil
}

// Get 按 ID 读取凭据。
func (s *Service) Get(ctx context.Context, id string) (Credential, error) {
	return s.repo.Get(ctx, id)
}

// List 返回全部凭据，顺序稳定。
func (s *Service) List(ctx context.Context) ([]Credential, error) {
	return s.repo.List(ctx)
}

// Update 部分更新凭据。Name 为 nil 保留；Secret 为 nil 保留现有
// secret，非 nil 整体替换并重新解析派生指纹。两个可变轴（名称 /
// secret 三列）落在存储层不相交的列集上，并发互不覆盖；输入全空时
// 不触碰存储直接返回。所有校验先于任何写入完成。
func (s *Service) Update(ctx context.Context, id string, input UpdateInput) (Credential, error) {
	current, err := s.repo.Get(ctx, id)
	if err != nil {
		return Credential{}, err
	}

	updated := current
	now := s.Now()
	var (
		newName   string
		rename    bool
		newSecret *Secret
	)
	if input.Name != nil {
		newName = strings.TrimSpace(*input.Name)
		if err := ValidateName(newName); err != nil {
			return Credential{}, err
		}
		rename = true
	}
	if input.Secret != nil {
		fingerprint, err := ParseSecret(*input.Secret)
		if err != nil {
			return Credential{}, err
		}
		newSecret = input.Secret
		updated.Fingerprint = fingerprint
		updated.HasPassphrase = input.Secret.PrivateKeyPassphrase != ""
	}
	if !rename && newSecret == nil {
		return current, nil
	}

	if newSecret != nil {
		if err := s.repo.ReplaceSecret(ctx, id, *newSecret, updated.Fingerprint, updated.HasPassphrase, now); err != nil {
			return Credential{}, err
		}
	}
	if rename {
		if err := s.repo.Rename(ctx, id, newName, now); err != nil {
			return Credential{}, err
		}
	}
	updated.Name = newName
	updated.UpdatedAt = now
	return updated, nil
}

// Delete 按 ID 硬删除凭据。被同步源引用时返回 ErrInUse（携带引用
// 清单，由存储层在同一事务内判定）：引用源解绑或改绑之前，删除
// fail-closed。
func (s *Service) Delete(ctx context.Context, id string) error {
	return s.repo.Delete(ctx, id)
}

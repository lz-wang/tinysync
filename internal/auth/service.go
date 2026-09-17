package auth

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Service 是认证应用服务：REST / Web UI / CLI / 未来 MCP 共用的业务
// 入口。密码策略、session 生命周期与 scope 授权语义都在此层判定，
// 不进入 HTTP middleware。
type Service struct {
	repo Repository
	// Now 返回当前时间；默认 UTC time.Now，测试可注入固定时钟。
	Now func() time.Time
}

// NewService 构造认证应用服务。
func NewService(repo Repository) *Service {
	return &Service{
		repo: repo,
		Now:  func() time.Time { return time.Now().UTC() },
	}
}

// SetAdminPassword 创建或替换管理员密码（首次 bootstrap、遗忘后
// reset 与 rotation 共用入口），并原子废弃全部 Web Session。
func (s *Service) SetAdminPassword(ctx context.Context, password string) error {
	hash, err := HashPassword(password)
	if err != nil {
		return err
	}
	if err := s.repo.SetAdminPassword(ctx, hash, s.Now()); err != nil {
		return fmt.Errorf("set admin password: %w", err)
	}
	return nil
}

// AdminConfigured 报告管理员密码是否已初始化。
func (s *Service) AdminConfigured(ctx context.Context) (bool, error) {
	return s.repo.AdminConfigured(ctx)
}

// Login 校验密码并创建 Web Session：返回会话元数据与 raw session
// token——raw 只在此返回一次，之后无法取回。admin 未初始化、密码
// 错误与 hash 损坏统一返回 ErrInvalidCredentials，不区分具体原因。
func (s *Service) Login(ctx context.Context, password string) (WebSession, string, error) {
	// 顺手清理过期会话；清理失败不阻断登录（下次登录再清）。
	_, _ = s.repo.DeleteExpiredSessions(ctx, s.Now())

	cred, err := s.repo.GetAdminCredential(ctx)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return WebSession{}, "", ErrInvalidCredentials
		}
		return WebSession{}, "", err
	}
	ok, err := VerifyPassword(password, cred.PasswordHash)
	if err != nil {
		// 存量 hash 损坏按凭据错误处理，不向调用方暴露内部格式。
		return WebSession{}, "", ErrInvalidCredentials
	}
	if !ok {
		return WebSession{}, "", ErrInvalidCredentials
	}

	raw, hash, err := newSessionToken()
	if err != nil {
		return WebSession{}, "", err
	}
	id, err := newSessionID()
	if err != nil {
		return WebSession{}, "", err
	}
	now := s.Now()
	session := WebSession{ID: id, CreatedAt: now, ExpiresAt: now.Add(SessionTTL)}
	if err := s.repo.CreateSession(ctx, session, hash); err != nil {
		return WebSession{}, "", fmt.Errorf("create web session: %w", err)
	}
	return session, raw, nil
}

// Logout 删除当前会话（会话已不存在时幂等成功）。
func (s *Service) Logout(ctx context.Context, sessionID string) error {
	return s.repo.DeleteSession(ctx, sessionID)
}

// AuthenticateSession 校验 raw session token 并返回 admin principal。
// token 不存在与已过期统一返回 ErrUnauthorized；过期会话同时删除。
func (s *Service) AuthenticateSession(ctx context.Context, rawToken string) (Principal, WebSession, error) {
	session, err := s.repo.GetSessionByHash(ctx, HashSessionToken(rawToken))
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return Principal{}, WebSession{}, ErrUnauthorized
		}
		return Principal{}, WebSession{}, err
	}
	if !s.Now().Before(session.ExpiresAt) {
		_ = s.repo.DeleteSession(ctx, session.ID)
		return Principal{}, WebSession{}, ErrUnauthorized
	}
	return SessionPrincipal(), session, nil
}

// SessionPrincipal 返回 Web Session 的 principal：唯一 admin 身份，
// 持有完整授权集合（read + run + admin）。
func SessionPrincipal() Principal {
	return Principal{
		Kind:      PrincipalKindWebSession,
		SubjectID: AdminSubject,
		Scopes:    []Scope{ScopeAdmin, ScopeRead, ScopeRun},
	}
}

// Authorize 判定 principal 是否具备所需 scope：admin ⇒ read + run；
// run ⇏ read；read ⇏ run。
func Authorize(p Principal, required Scope) bool {
	for _, scope := range p.Scopes {
		if scope == ScopeAdmin || scope == required {
			return true
		}
	}
	return false
}

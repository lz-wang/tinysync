package auth

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// fakeRepo 是内存 Repository：service 单测不重复覆盖 SQLite 细节
// （仓库行为由 sqlite 包专项测试覆盖）。
type fakeRepo struct {
	credHash    *string
	avatar      string
	sessions    map[string]WebSession // key: string(sessionHash)
	tokenByID   map[string]APIToken
	tokenHashID map[string]string
}

func newFakeRepo() *fakeRepo {
	return &fakeRepo{
		sessions:    map[string]WebSession{},
		tokenByID:   map[string]APIToken{},
		tokenHashID: map[string]string{},
	}
}

func (f *fakeRepo) AdminConfigured(context.Context) (bool, error) {
	return f.credHash != nil, nil
}

func (f *fakeRepo) GetAdminCredential(context.Context) (AdminCredential, error) {
	if f.credHash == nil {
		return AdminCredential{}, ErrNotFound
	}
	return AdminCredential{PasswordHash: *f.credHash, Avatar: f.avatar}, nil
}

func (f *fakeRepo) SetAdminPassword(_ context.Context, hash string, _ time.Time) error {
	f.credHash = &hash
	f.sessions = map[string]WebSession{}
	return nil
}

func (f *fakeRepo) SetAdminAvatar(_ context.Context, avatar string, _ time.Time) error {
	if f.credHash == nil {
		return ErrNotFound
	}
	f.avatar = avatar
	return nil
}

func (f *fakeRepo) CreateSession(_ context.Context, s WebSession, hash []byte) error {
	f.sessions[string(hash)] = s
	return nil
}

func (f *fakeRepo) GetSessionByHash(_ context.Context, hash []byte) (WebSession, error) {
	s, ok := f.sessions[string(hash)]
	if !ok {
		return WebSession{}, ErrNotFound
	}
	return s, nil
}

func (f *fakeRepo) DeleteSession(_ context.Context, id string) error {
	for key, s := range f.sessions {
		if s.ID == id {
			delete(f.sessions, key)
		}
	}
	return nil
}

func (f *fakeRepo) DeleteExpiredSessions(_ context.Context, now time.Time) (int64, error) {
	var n int64
	for key, s := range f.sessions {
		if !s.ExpiresAt.After(now) {
			delete(f.sessions, key)
			n++
		}
	}
	return n, nil
}

func (f *fakeRepo) CreateAPIToken(_ context.Context, token APIToken, hash []byte) error {
	f.tokenByID[token.ID] = token
	f.tokenHashID[string(hash)] = token.ID
	return nil
}

func (f *fakeRepo) GetAPIToken(_ context.Context, id string) (APIToken, error) {
	token, ok := f.tokenByID[id]
	if !ok {
		return APIToken{}, ErrNotFound
	}
	return token, nil
}

func (f *fakeRepo) GetAPITokenByHash(_ context.Context, hash []byte) (APIToken, error) {
	id, ok := f.tokenHashID[string(hash)]
	if !ok {
		return APIToken{}, ErrNotFound
	}
	return f.tokenByID[id], nil
}

func (f *fakeRepo) ListAPITokens(context.Context) ([]APIToken, error) {
	list := make([]APIToken, 0, len(f.tokenByID))
	for _, token := range f.tokenByID {
		list = append(list, token)
	}
	return list, nil
}

func (f *fakeRepo) RevokeAPIToken(_ context.Context, id string, now time.Time) error {
	token, ok := f.tokenByID[id]
	if !ok {
		return ErrNotFound
	}
	if token.RevokedAt == nil {
		token.RevokedAt = &now
		f.tokenByID[id] = token
	}
	return nil
}

// TouchAPITokenLastUsed 与 sqlite 实现同语义：1 分钟阈值内不写入。
func (f *fakeRepo) TouchAPITokenLastUsed(_ context.Context, id string, now time.Time) error {
	token, ok := f.tokenByID[id]
	if !ok {
		return nil
	}
	threshold := now.Add(-LastUsedThrottle)
	if token.LastUsedAt == nil || token.LastUsedAt.Before(threshold) {
		token.LastUsedAt = &now
		f.tokenByID[id] = token
	}
	return nil
}

// validPassword 满足最小策略的测试密码。
const validPassword = "correct-horse-battery"

// fixedClock 构造注入固定时钟的 service。
func fixedClock(now time.Time) func() time.Time {
	return func() time.Time { return now }
}

func TestSetAdminPasswordAndLogin(t *testing.T) {
	repo := newFakeRepo()
	now := time.Unix(1757879400, 0).UTC()
	svc := NewService(repo)
	svc.Now = fixedClock(now)
	ctx := context.Background()

	configured, err := svc.AdminConfigured(ctx)
	if err != nil || configured {
		t.Fatalf("AdminConfigured before bootstrap = (%v, %v), want (false, nil)", configured, err)
	}
	if err := svc.SetAdminPassword(ctx, validPassword); err != nil {
		t.Fatalf("SetAdminPassword: %v", err)
	}

	session, raw, err := svc.Login(ctx, validPassword)
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if raw == "" || len(raw) < 32 {
		t.Fatalf("raw session token too short: %q", raw)
	}
	if want := now.Add(SessionTTL); !session.ExpiresAt.Equal(want) {
		t.Errorf("expires_at = %v, want %v (7d TTL)", session.ExpiresAt, want)
	}
	if !strings.HasPrefix(session.ID, "ses_") {
		t.Errorf("session id = %q, want ses_ prefix", session.ID)
	}

	principal, got, err := svc.AuthenticateSession(ctx, raw)
	if err != nil {
		t.Fatalf("AuthenticateSession: %v", err)
	}
	if got.ID != session.ID {
		t.Errorf("session id = %q, want %q", got.ID, session.ID)
	}
	if principal.Kind != PrincipalKindWebSession || principal.SubjectID != AdminSubject {
		t.Errorf("principal = %+v, want web_session/admin", principal)
	}
	for _, scope := range []Scope{ScopeRead, ScopeRun, ScopeAdmin} {
		if !Authorize(principal, scope) {
			t.Errorf("session principal lacks scope %s", scope)
		}
	}
}

func TestLoginErrors(t *testing.T) {
	repo := newFakeRepo()
	svc := NewService(repo)
	ctx := context.Background()

	// admin 未初始化：与密码错误同形。
	if _, _, err := svc.Login(ctx, validPassword); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("Login unconfigured = %v, want ErrInvalidCredentials", err)
	}

	if err := svc.SetAdminPassword(ctx, validPassword); err != nil {
		t.Fatalf("SetAdminPassword: %v", err)
	}
	if _, _, err := svc.Login(ctx, "wrong-password-123"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("Login wrong password = %v, want ErrInvalidCredentials", err)
	}

	// 存量 hash 损坏：按凭据错误处理，不 panic。
	broken := "$argon2id$v=19$m=19456,t=2,p=1$!!!broken"
	repo.credHash = &broken
	if _, _, err := svc.Login(ctx, validPassword); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("Login malformed hash = %v, want ErrInvalidCredentials", err)
	}
}

// 登录密码输入上限：恰好 1024 字节可正常登录，超过 1024 字节在
// KDF 之前拒绝，统一凭据错误语义（与设置密码同策略）。
func TestLoginPasswordSizeLimit(t *testing.T) {
	repo := newFakeRepo()
	svc := NewService(repo)
	ctx := context.Background()

	maxBytes := strings.Repeat("a", maxPasswordBytes)
	if err := svc.SetAdminPassword(ctx, maxBytes); err != nil {
		t.Fatalf("SetAdminPassword (1024 bytes): %v", err)
	}
	if _, _, err := svc.Login(ctx, maxBytes); err != nil {
		t.Fatalf("Login with 1024-byte password = %v, want success", err)
	}

	tooLong := strings.Repeat("a", maxPasswordBytes+1)
	if _, _, err := svc.Login(ctx, tooLong); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("Login with 1025-byte password = %v, want ErrInvalidCredentials", err)
	}
}

// 每次 login 产生唯一 session token；logout 后立即失效。
func TestSessionLifecycle(t *testing.T) {
	repo := newFakeRepo()
	svc := NewService(repo)
	ctx := context.Background()
	if err := svc.SetAdminPassword(ctx, validPassword); err != nil {
		t.Fatalf("SetAdminPassword: %v", err)
	}

	_, rawA, err := svc.Login(ctx, validPassword)
	if err != nil {
		t.Fatalf("Login a: %v", err)
	}
	_, rawB, err := svc.Login(ctx, validPassword)
	if err != nil {
		t.Fatalf("Login b: %v", err)
	}
	if rawA == rawB {
		t.Fatal("two logins share one session token, want uniqueness")
	}
	if _, _, err := svc.AuthenticateSession(ctx, rawA); err != nil {
		t.Fatalf("AuthenticateSession a: %v", err)
	}
	if _, _, err := svc.AuthenticateSession(ctx, rawB); err != nil {
		t.Fatalf("AuthenticateSession b: %v", err)
	}

	if _, _, err := svc.AuthenticateSession(ctx, "forged-token"); !errors.Is(err, ErrUnauthorized) {
		t.Errorf("AuthenticateSession(forged) = %v, want ErrUnauthorized", err)
	}

	// logout 只删除当前会话。
	if err := svc.Logout(ctx, "ses_nonexistent"); err != nil {
		t.Errorf("Logout(unknown) = %v, want nil (idempotent)", err)
	}
	// 通过 AuthenticateSession 取会话 ID 再 logout。
	_, session, err := svc.AuthenticateSession(ctx, rawA)
	if err != nil {
		t.Fatalf("AuthenticateSession a: %v", err)
	}
	if err := svc.Logout(ctx, session.ID); err != nil {
		t.Fatalf("Logout: %v", err)
	}
	if _, _, err := svc.AuthenticateSession(ctx, rawA); !errors.Is(err, ErrUnauthorized) {
		t.Errorf("session a after logout = %v, want ErrUnauthorized", err)
	}
	if _, _, err := svc.AuthenticateSession(ctx, rawB); err != nil {
		t.Errorf("session b after logout of a = %v, want nil", err)
	}
}

// 过期会话认证 401 并被删除。
func TestExpiredSessionRejectedAndPurged(t *testing.T) {
	repo := newFakeRepo()
	now := time.Unix(1757879400, 0).UTC()
	svc := NewService(repo)
	svc.Now = fixedClock(now)
	ctx := context.Background()
	if err := svc.SetAdminPassword(ctx, validPassword); err != nil {
		t.Fatalf("SetAdminPassword: %v", err)
	}
	_, raw, err := svc.Login(ctx, validPassword)
	if err != nil {
		t.Fatalf("Login: %v", err)
	}

	svc.Now = fixedClock(now.Add(SessionTTL + time.Minute))
	if _, _, err := svc.AuthenticateSession(ctx, raw); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("expired session = %v, want ErrUnauthorized", err)
	}
	// 会话已被删除（fake repo 直接观察存储）。
	if len(repo.sessions) != 0 {
		t.Errorf("sessions after expiry = %d, want 0 (purged)", len(repo.sessions))
	}
}

// 密码重置立即废弃全部既有 Web Session。
func TestPasswordResetInvalidatesSessions(t *testing.T) {
	repo := newFakeRepo()
	svc := NewService(repo)
	ctx := context.Background()
	if err := svc.SetAdminPassword(ctx, validPassword); err != nil {
		t.Fatalf("SetAdminPassword: %v", err)
	}
	rawA, err := firstRaw(svc, ctx)
	if err != nil {
		t.Fatal(err)
	}
	rawB, err := firstRaw(svc, ctx)
	if err != nil {
		t.Fatal(err)
	}

	newPassword := "rotated-password-42"
	if err := svc.SetAdminPassword(ctx, newPassword); err != nil {
		t.Fatalf("SetAdminPassword (reset): %v", err)
	}
	for _, raw := range []string{rawA, rawB} {
		if _, _, err := svc.AuthenticateSession(ctx, raw); !errors.Is(err, ErrUnauthorized) {
			t.Errorf("old session after reset = %v, want ErrUnauthorized", err)
		}
	}
	// 新密码可登录，旧密码不可。
	if _, _, err := svc.Login(ctx, validPassword); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("Login old password after reset = %v, want ErrInvalidCredentials", err)
	}
	if _, _, err := svc.Login(ctx, newPassword); err != nil {
		t.Errorf("Login new password after reset = %v, want nil", err)
	}
}

func firstRaw(svc *Service, ctx context.Context) (string, error) {
	_, raw, err := svc.Login(ctx, validPassword)
	return raw, err
}

func TestValidatePasswordPolicy(t *testing.T) {
	cases := []struct {
		name     string
		password string
		wantErr  bool
	}{
		{"12 chars ok", "abcdefghijkl", false},
		{"11 chars rejected", "abcdefghijk", true},
		{"empty rejected", "", true},
		{"1024 bytes ok", strings.Repeat("a", 1024), false},
		{"1025 bytes rejected", strings.Repeat("a", 1025), true},
	}
	for _, tc := range cases {
		err := ValidatePassword(tc.password)
		if (err != nil) != tc.wantErr {
			t.Errorf("%s: err = %v, wantErr %v", tc.name, err, tc.wantErr)
		}
	}
	// 策略错误是 ErrInvalidInput。
	if err := ValidatePassword("short"); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("short password err = %v, want ErrInvalidInput", err)
	}
}

func TestPasswordHashRoundTrip(t *testing.T) {
	hash, err := HashPassword(validPassword)
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if !strings.HasPrefix(hash, "$argon2id$v=19$m=19456,t=2,p=1$") {
		t.Fatalf("hash = %q, want argon2id PHC with frozen params", hash)
	}
	ok, err := VerifyPassword(validPassword, hash)
	if err != nil || !ok {
		t.Fatalf("VerifyPassword(correct) = (%v, %v), want (true, nil)", ok, err)
	}
	ok, err = VerifyPassword("wrong-password-123", hash)
	if err != nil || ok {
		t.Fatalf("VerifyPassword(wrong) = (%v, %v), want (false, nil)", ok, err)
	}
	// 每次哈希 salt 不同。
	other, err := HashPassword(validPassword)
	if err != nil {
		t.Fatalf("HashPassword again: %v", err)
	}
	if other == hash {
		t.Fatal("two hashes equal, want distinct salts")
	}

	for _, broken := range []string{
		"",
		"not-a-phc",
		"$argon2i$v=19$m=19456,t=2,p=1$c2FsdA$aGFzaA",  // 算法不符
		"$argon2id$v=16$m=19456,t=2,p=1$c2FsdA$aGFzaA", // 版本不符
		"$argon2id$v=19$m=0,t=2,p=1$c2FsdA$aGFzaA",     // 参数非法
		"$argon2id$v=19$m=19456,t=2,p=1$!!!$aGFzaA",    // salt 非 base64
	} {
		if _, err := VerifyPassword(validPassword, broken); !errors.Is(err, ErrPasswordHash) {
			t.Errorf("VerifyPassword(%q) err = %v, want ErrPasswordHash", broken, err)
		}
	}
}

// Authorize 语义矩阵：admin ⇒ read + run；run ⇏ read；read ⇏ run。
func TestAuthorizeScopeSemantics(t *testing.T) {
	cases := []struct {
		name     string
		scopes   []Scope
		required Scope
		want     bool
	}{
		{"admin implies read", []Scope{ScopeAdmin}, ScopeRead, true},
		{"admin implies run", []Scope{ScopeAdmin}, ScopeRun, true},
		{"admin implies admin", []Scope{ScopeAdmin}, ScopeAdmin, true},
		{"read allows read", []Scope{ScopeRead}, ScopeRead, true},
		{"read denies run", []Scope{ScopeRead}, ScopeRun, false},
		{"read denies admin", []Scope{ScopeRead}, ScopeAdmin, false},
		{"run allows run", []Scope{ScopeRun}, ScopeRun, true},
		{"run denies read", []Scope{ScopeRun}, ScopeRead, false},
		{"run denies admin", []Scope{ScopeRun}, ScopeAdmin, false},
		{"empty denies all", nil, ScopeRead, false},
	}
	for _, tc := range cases {
		got := Authorize(Principal{Scopes: tc.scopes}, tc.required)
		if got != tc.want {
			t.Errorf("%s: Authorize = %v, want %v", tc.name, got, tc.want)
		}
	}
}

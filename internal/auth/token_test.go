package auth

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// createToken 构造已注入时钟的 service 并创建 token 的快捷方式。
func createToken(t *testing.T, svc *Service, input CreateAPITokenInput) (APIToken, string) {
	t.Helper()
	token, raw, err := svc.CreateAPIToken(context.Background(), input)
	if err != nil {
		t.Fatalf("CreateAPIToken: %v", err)
	}
	return token, raw
}

func TestCreateAPIToken(t *testing.T) {
	repo := newFakeRepo()
	now := time.Unix(1757879400, 0).UTC()
	svc := NewService(repo)
	svc.Now = fixedClock(now)

	expires := now.Add(30 * 24 * time.Hour)
	token, raw := createToken(t, svc, CreateAPITokenInput{
		Name:      "automation",
		Scopes:    []Scope{ScopeRun, ScopeRead, ScopeRun}, // 乱序 + 重复
		ExpiresAt: &expires,
	})

	// raw 只出现一次，格式为 ts_<base64url>，熵充足。
	if !strings.HasPrefix(raw, "ts_") || len(raw) < 40 {
		t.Fatalf("raw token = %q, want ts_<secret> with 256-bit entropy", raw)
	}
	if strings.Contains(raw, "=") || strings.Contains(raw, "+") || strings.Contains(raw, "/") {
		t.Fatalf("raw token = %q, want base64url alphabet", raw)
	}
	// 展示前缀只暴露 ts_ + 8 字符。
	if token.Prefix != raw[:tokenDisplayLen] {
		t.Errorf("prefix = %q, want %q", token.Prefix, raw[:tokenDisplayLen])
	}
	if token.ID == "" || !strings.HasPrefix(token.ID, "tok_") {
		t.Errorf("token id = %q, want tok_ prefix", token.ID)
	}
	// scope 规范化：排序去重。
	if len(token.Scopes) != 2 || token.Scopes[0] != ScopeRead || token.Scopes[1] != ScopeRun {
		t.Errorf("scopes = %v, want normalized [read run]", token.Scopes)
	}
	if token.ExpiresAt == nil || !token.ExpiresAt.Equal(expires) {
		t.Errorf("expires_at = %v, want %v", token.ExpiresAt, expires)
	}
	if !token.CreatedAt.Equal(now) {
		t.Errorf("created_at = %v, want %v", token.CreatedAt, now)
	}

	// List 返回元数据但绝不含 raw。
	list, err := svc.ListAPITokens(context.Background())
	if err != nil {
		t.Fatalf("ListAPITokens: %v", err)
	}
	if len(list) != 1 || list[0].ID != token.ID {
		t.Fatalf("list = %+v, want the created token", list)
	}
}

func TestCreateAPITokenValidation(t *testing.T) {
	svc := NewService(newFakeRepo())
	ctx := context.Background()
	past := time.Now().UTC().Add(-time.Hour)

	cases := []struct {
		name   string
		input  CreateAPITokenInput
		wantEq error
	}{
		{"empty name", CreateAPITokenInput{Name: "  ", Scopes: []Scope{ScopeRead}}, ErrInvalidInput},
		{"long name", CreateAPITokenInput{Name: strings.Repeat("名", 101), Scopes: []Scope{ScopeRead}}, ErrInvalidInput},
		{"empty scopes", CreateAPITokenInput{Name: "x"}, ErrInvalidInput},
		{"unknown scope", CreateAPITokenInput{Name: "x", Scopes: []Scope{"sudo"}}, ErrInvalidInput},
		{"past expiry", CreateAPITokenInput{Name: "x", Scopes: []Scope{ScopeRead}, ExpiresAt: &past}, ErrInvalidInput},
	}
	for _, tc := range cases {
		if _, _, err := svc.CreateAPIToken(ctx, tc.input); !errors.Is(err, tc.wantEq) {
			t.Errorf("%s: err = %v, want %v", tc.name, err, tc.wantEq)
		}
	}
	// 合法：永不过期。
	if _, _, err := svc.CreateAPIToken(ctx, CreateAPITokenInput{Name: "x", Scopes: []Scope{ScopeRead}}); err != nil {
		t.Errorf("create without expiry = %v, want nil", err)
	}
}

// 认证路径：有效 token → principal；未知 / 过期 / 撤销统一
// ErrUnauthorized；last_used_at 节流更新。
func TestAuthenticateAPIToken(t *testing.T) {
	repo := newFakeRepo()
	now := time.Unix(1757879400, 0).UTC()
	svc := NewService(repo)
	svc.Now = fixedClock(now)

	expires := now.Add(time.Hour)
	token, raw := createToken(t, svc, CreateAPITokenInput{
		Name:      "automation",
		Scopes:    []Scope{ScopeRead, ScopeRun},
		ExpiresAt: &expires,
	})

	// 有效：principal 携带 token 自身 scopes。
	principal, got, err := svc.AuthenticateAPIToken(context.Background(), raw)
	if err != nil {
		t.Fatalf("AuthenticateAPIToken: %v", err)
	}
	if principal.Kind != PrincipalKindAPIToken || principal.SubjectID != token.ID {
		t.Errorf("principal = %+v, want api_token/%s", principal, token.ID)
	}
	if len(principal.Scopes) != 2 {
		t.Errorf("principal scopes = %v, want token scopes", principal.Scopes)
	}
	if got.ID != token.ID {
		t.Errorf("token id = %q, want %q", got.ID, token.ID)
	}
	// 认证节流更新 last_used_at（从仓库读取，认证返回值为 touch 前快照）。
	stored, err := svc.ListAPITokens(context.Background())
	if err != nil {
		t.Fatalf("ListAPITokens: %v", err)
	}
	if stored[0].LastUsedAt == nil || !stored[0].LastUsedAt.Equal(now) {
		t.Errorf("last_used_at = %v, want %v", stored[0].LastUsedAt, now)
	}

	// 未知 raw 同形 401。
	if _, _, err := svc.AuthenticateAPIToken(context.Background(), "ts_unknown"); !errors.Is(err, ErrUnauthorized) {
		t.Errorf("unknown token = %v, want ErrUnauthorized", err)
	}

	// 过期。
	svc.Now = fixedClock(expires.Add(time.Minute))
	if _, _, err := svc.AuthenticateAPIToken(context.Background(), raw); !errors.Is(err, ErrUnauthorized) {
		t.Errorf("expired token = %v, want ErrUnauthorized", err)
	}
	svc.Now = fixedClock(now)

	// 撤销后立即失效。
	if err := svc.RevokeAPIToken(context.Background(), token.ID); err != nil {
		t.Fatalf("RevokeAPIToken: %v", err)
	}
	if _, _, err := svc.AuthenticateAPIToken(context.Background(), raw); !errors.Is(err, ErrUnauthorized) {
		t.Errorf("revoked token = %v, want ErrUnauthorized", err)
	}

	// 撤销不存在的 token：ErrNotFound。
	if err := svc.RevokeAPIToken(context.Background(), "tok_missing"); !errors.Is(err, ErrNotFound) {
		t.Errorf("revoke missing = %v, want ErrNotFound", err)
	}
}

// admin token 的 principal 通过 Authorize 获得全部权限。
func TestAdminTokenImpliesAllScopes(t *testing.T) {
	svc := NewService(newFakeRepo())
	_, raw := createToken(t, svc, CreateAPITokenInput{
		Name:   "admin",
		Scopes: []Scope{ScopeAdmin},
	})
	principal, _, err := svc.AuthenticateAPIToken(context.Background(), raw)
	if err != nil {
		t.Fatalf("AuthenticateAPIToken: %v", err)
	}
	for _, required := range []Scope{ScopeRead, ScopeRun, ScopeAdmin} {
		if !Authorize(principal, required) {
			t.Errorf("admin token lacks scope %s", required)
		}
	}
	// run-only token 不具备 read（scope 语义回归）。
	_, runRaw := createToken(t, svc, CreateAPITokenInput{Name: "runner", Scopes: []Scope{ScopeRun}})
	runPrincipal, _, err := svc.AuthenticateAPIToken(context.Background(), runRaw)
	if err != nil {
		t.Fatalf("AuthenticateAPIToken: %v", err)
	}
	if Authorize(runPrincipal, ScopeRead) {
		t.Error("run token granted read, want deny")
	}
}

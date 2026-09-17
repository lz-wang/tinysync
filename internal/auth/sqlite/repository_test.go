package sqlite

import (
	"context"
	"errors"
	"testing"
	"time"

	"tinysync/internal/auth"
	"tinysync/internal/storage"
)

// testRepo 构造已完成 migration 的仓库并注册清理。
func testRepo(t *testing.T) (*Repository, string) {
	t.Helper()
	dataDir := t.TempDir()
	db, err := storage.Open(dataDir)
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := storage.Migrate(context.Background(), db, dataDir); err != nil {
		t.Fatalf("storage.Migrate: %v", err)
	}
	return NewRepository(db), dataDir
}

// reopenRepo 用同一数据目录重新打开数据库，模拟进程重启。
func reopenRepo(t *testing.T, dataDir string) *Repository {
	t.Helper()
	db, err := storage.Open(dataDir)
	if err != nil {
		t.Fatalf("storage.Open (reopen): %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return NewRepository(db)
}

func TestAdminCredentialLifecycle(t *testing.T) {
	repo, _ := testRepo(t)
	ctx := context.Background()

	// 未初始化：AdminConfigured=false，读取返回 ErrNotFound。
	configured, err := repo.AdminConfigured(ctx)
	if err != nil {
		t.Fatalf("AdminConfigured: %v", err)
	}
	if configured {
		t.Fatal("fresh database reports admin configured, want false")
	}
	if _, err := repo.GetAdminCredential(ctx); !errors.Is(err, auth.ErrNotFound) {
		t.Fatalf("GetAdminCredential on fresh database = %v, want ErrNotFound", err)
	}

	now := time.Unix(1757879400, 0).UTC()
	if err := repo.SetAdminPassword(ctx, "$argon2id$v=19$m=19456,t=2,p=1$salt$hash", now); err != nil {
		t.Fatalf("SetAdminPassword: %v", err)
	}
	cred, err := repo.GetAdminCredential(ctx)
	if err != nil {
		t.Fatalf("GetAdminCredential: %v", err)
	}
	if cred.PasswordHash != "$argon2id$v=19$m=19456,t=2,p=1$salt$hash" {
		t.Errorf("password_hash = %q, want stored hash", cred.PasswordHash)
	}
	if !cred.CreatedAt.Equal(now) || !cred.UpdatedAt.Equal(now) {
		t.Errorf("timestamps = (%v, %v), want %v", cred.CreatedAt, cred.UpdatedAt, now)
	}

	// rotation：替换 hash，更新 updated_at，created_at 保持不变。
	later := now.Add(time.Hour)
	if err := repo.SetAdminPassword(ctx, "$argon2id$rotated", later); err != nil {
		t.Fatalf("SetAdminPassword (rotation): %v", err)
	}
	cred, err = repo.GetAdminCredential(ctx)
	if err != nil {
		t.Fatalf("GetAdminCredential after rotation: %v", err)
	}
	if cred.PasswordHash != "$argon2id$rotated" {
		t.Errorf("password_hash after rotation = %q, want rotated", cred.PasswordHash)
	}
	if !cred.CreatedAt.Equal(now) {
		t.Errorf("created_at after rotation = %v, want unchanged %v", cred.CreatedAt, now)
	}
	if !cred.UpdatedAt.Equal(later) {
		t.Errorf("updated_at after rotation = %v, want %v", cred.UpdatedAt, later)
	}
}

// SetAdminPassword 与清空 session 原子生效：重置密码后全部旧
// session 立即失效。
func TestSetAdminPasswordClearsSessions(t *testing.T) {
	repo, _ := testRepo(t)
	ctx := context.Background()
	now := time.Unix(1757879400, 0).UTC()
	if err := repo.SetAdminPassword(ctx, "hash-a", now); err != nil {
		t.Fatalf("SetAdminPassword: %v", err)
	}

	for _, id := range []string{"ses_a", "ses_b"} {
		session := auth.WebSession{ID: id, CreatedAt: now, ExpiresAt: now.Add(7 * 24 * time.Hour)}
		if err := repo.CreateSession(ctx, session, []byte("hash-of-"+id)); err != nil {
			t.Fatalf("CreateSession %s: %v", id, err)
		}
	}

	if err := repo.SetAdminPassword(ctx, "hash-b", now.Add(time.Minute)); err != nil {
		t.Fatalf("SetAdminPassword (reset): %v", err)
	}
	for _, id := range []string{"hash-of-ses_a", "hash-of-ses_b"} {
		if _, err := repo.GetSessionByHash(ctx, []byte(id)); !errors.Is(err, auth.ErrNotFound) {
			t.Errorf("session %s after password reset = %v, want ErrNotFound", id, err)
		}
	}
}

func TestSessionRoundTrip(t *testing.T) {
	repo, _ := testRepo(t)
	ctx := context.Background()
	now := time.Unix(1757879400, 0).UTC()
	session := auth.WebSession{ID: "ses_a", CreatedAt: now, ExpiresAt: now.Add(7 * 24 * time.Hour)}

	if err := repo.CreateSession(ctx, session, []byte("session-hash")); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	got, err := repo.GetSessionByHash(ctx, []byte("session-hash"))
	if err != nil {
		t.Fatalf("GetSessionByHash: %v", err)
	}
	if got.ID != session.ID || !got.ExpiresAt.Equal(session.ExpiresAt) || !got.CreatedAt.Equal(session.CreatedAt) {
		t.Errorf("session = %+v, want %+v", got, session)
	}

	// 未知 hash 不存在。
	if _, err := repo.GetSessionByHash(ctx, []byte("unknown")); !errors.Is(err, auth.ErrNotFound) {
		t.Errorf("GetSessionByHash(unknown) = %v, want ErrNotFound", err)
	}

	// logout 后不再存在。
	if err := repo.DeleteSession(ctx, session.ID); err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}
	if _, err := repo.GetSessionByHash(ctx, []byte("session-hash")); !errors.Is(err, auth.ErrNotFound) {
		t.Errorf("session after delete = %v, want ErrNotFound", err)
	}
	// 重复删除幂等。
	if err := repo.DeleteSession(ctx, session.ID); err != nil {
		t.Errorf("DeleteSession (again) = %v, want nil", err)
	}
}

// DeleteExpiredSessions 只清理过期会话，未过期会话保留。
func TestDeleteExpiredSessions(t *testing.T) {
	repo, _ := testRepo(t)
	ctx := context.Background()
	now := time.Unix(1757879400, 0).UTC()

	mkSession := func(id string, expires time.Time) {
		t.Helper()
		if err := repo.CreateSession(ctx,
			auth.WebSession{ID: id, CreatedAt: now, ExpiresAt: expires}, []byte(id)); err != nil {
			t.Fatalf("CreateSession %s: %v", id, err)
		}
	}
	mkSession("ses_expired", now.Add(-time.Minute))
	mkSession("ses_live", now.Add(time.Hour))

	deleted, err := repo.DeleteExpiredSessions(ctx, now)
	if err != nil {
		t.Fatalf("DeleteExpiredSessions: %v", err)
	}
	if deleted != 1 {
		t.Fatalf("deleted rows = %d, want 1", deleted)
	}
	if _, err := repo.GetSessionByHash(ctx, []byte("ses_live")); err != nil {
		t.Errorf("live session after purge = %v, want nil", err)
	}
	if _, err := repo.GetSessionByHash(ctx, []byte("ses_expired")); !errors.Is(err, auth.ErrNotFound) {
		t.Errorf("expired session after purge = %v, want ErrNotFound", err)
	}
}

// tokenFixture 构造带固定 hash 的 token 元数据。
func tokenFixture(id string, now time.Time, expires *time.Time) (auth.APIToken, []byte) {
	token := auth.APIToken{
		ID:        id,
		Name:      "automation-" + id,
		Prefix:    "ts_abcd1234",
		Scopes:    []auth.Scope{auth.ScopeRead, auth.ScopeRun},
		CreatedAt: now,
		ExpiresAt: expires,
	}
	return token, []byte("sha256-of-" + id)
}

func TestAPITokenRoundTrip(t *testing.T) {
	repo, _ := testRepo(t)
	ctx := context.Background()
	now := time.Unix(1757879400, 0).UTC()
	expires := now.Add(30 * 24 * time.Hour)

	token, hash := tokenFixture("tok_a", now, &expires)
	if err := repo.CreateAPIToken(ctx, token, hash); err != nil {
		t.Fatalf("CreateAPIToken: %v", err)
	}

	byID, err := repo.GetAPIToken(ctx, token.ID)
	if err != nil {
		t.Fatalf("GetAPIToken: %v", err)
	}
	byHash, err := repo.GetAPITokenByHash(ctx, hash)
	if err != nil {
		t.Fatalf("GetAPITokenByHash: %v", err)
	}
	if byID.ID != token.ID || byHash.ID != token.ID {
		t.Fatalf("tokens = (%+v, %+v), want id %s", byID, byHash, token.ID)
	}
	for _, got := range []auth.APIToken{byID, byHash} {
		if got.Name != token.Name || got.Prefix != token.Prefix {
			t.Errorf("token metadata = %+v, want %+v", got, token)
		}
		if len(got.Scopes) != 2 || got.Scopes[0] != auth.ScopeRead || got.Scopes[1] != auth.ScopeRun {
			t.Errorf("scopes = %v, want [read run]", got.Scopes)
		}
		if got.ExpiresAt == nil || !got.ExpiresAt.Equal(expires) {
			t.Errorf("expires_at = %v, want %v", got.ExpiresAt, expires)
		}
		if got.LastUsedAt != nil || got.RevokedAt != nil {
			t.Errorf("nullable fields = (%v, %v), want (nil, nil)", got.LastUsedAt, got.RevokedAt)
		}
		if got.Active(now.Add(time.Second)) != true {
			t.Error("fresh token should be active")
		}
	}

	// 未知 ID / hash 不存在。
	if _, err := repo.GetAPIToken(ctx, "tok_missing"); !errors.Is(err, auth.ErrNotFound) {
		t.Errorf("GetAPIToken(missing) = %v, want ErrNotFound", err)
	}
	if _, err := repo.GetAPITokenByHash(ctx, []byte("unknown")); !errors.Is(err, auth.ErrNotFound) {
		t.Errorf("GetAPITokenByHash(unknown) = %v, want ErrNotFound", err)
	}
}

// 可空 expires_at：无过期时刻的 token 永不过期。
func TestAPITokenNullableExpiresAt(t *testing.T) {
	repo, _ := testRepo(t)
	ctx := context.Background()
	now := time.Unix(1757879400, 0).UTC()

	token, hash := tokenFixture("tok_never", now, nil)
	if err := repo.CreateAPIToken(ctx, token, hash); err != nil {
		t.Fatalf("CreateAPIToken: %v", err)
	}
	got, err := repo.GetAPIToken(ctx, token.ID)
	if err != nil {
		t.Fatalf("GetAPIToken: %v", err)
	}
	if got.ExpiresAt != nil {
		t.Errorf("expires_at = %v, want nil", got.ExpiresAt)
	}
	if got.Expired(now.Add(100 * 24 * time.Hour)) {
		t.Error("token without expires_at reported expired")
	}
}

// token_hash 唯一约束生效。
func TestAPITokenHashUnique(t *testing.T) {
	repo, _ := testRepo(t)
	ctx := context.Background()
	now := time.Unix(1757879400, 0).UTC()

	tokenA, _ := tokenFixture("tok_a", now, nil)
	tokenB, hashB := tokenFixture("tok_b", now, nil)
	if err := repo.CreateAPIToken(ctx, tokenA, hashB); err != nil {
		t.Fatalf("CreateAPIToken a: %v", err)
	}
	if err := repo.CreateAPIToken(ctx, tokenB, hashB); err == nil {
		t.Fatal("CreateAPIToken with duplicate hash = nil, want UNIQUE violation")
	}
}

// scopes_json 损坏或含未知 scope 时读取 fail closed：复用领域层
// UnmarshalScopes 校验，残缺授权集合绝不进入 principal。
func TestScanTokenRejectsCorruptedScopes(t *testing.T) {
	repo, dataDir := testRepo(t)
	ctx := context.Background()
	now := time.Unix(1757879400, 0).UTC()

	// 绕过领域层直接写入损坏行（模拟存储损坏）。
	db, err := storage.Open(dataDir)
	if err != nil {
		t.Fatalf("open raw db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	for id, scopesJSON := range map[string]string{
		"tok_notjson": `not-json`,
		"tok_unknown": `["sudo"]`,
	} {
		if _, err := db.ExecContext(ctx,
			"INSERT INTO api_tokens (id, name, prefix, token_hash, scopes_json, created_at) VALUES (?, ?, ?, ?, ?, ?)",
			id, "corrupted", "ts_corrupt", []byte("sha256-of-"+id), scopesJSON, now.UnixMilli()); err != nil {
			t.Fatalf("seed corrupted token %s: %v", id, err)
		}
	}

	for _, id := range []string{"tok_notjson", "tok_unknown"} {
		if _, err := repo.GetAPIToken(ctx, id); !errors.Is(err, auth.ErrInvalidInput) {
			t.Errorf("GetAPIToken(%s) = %v, want ErrInvalidInput", id, err)
		}
	}
	if _, err := repo.ListAPITokens(ctx); !errors.Is(err, auth.ErrInvalidInput) {
		t.Errorf("ListAPITokens with corrupted rows = %v, want ErrInvalidInput", err)
	}
}

func TestAPITokenRevokeIdempotent(t *testing.T) {
	repo, _ := testRepo(t)
	ctx := context.Background()
	now := time.Unix(1757879400, 0).UTC()

	token, hash := tokenFixture("tok_a", now, nil)
	if err := repo.CreateAPIToken(ctx, token, hash); err != nil {
		t.Fatalf("CreateAPIToken: %v", err)
	}

	revokeAt := now.Add(time.Hour)
	if err := repo.RevokeAPIToken(ctx, token.ID, revokeAt); err != nil {
		t.Fatalf("RevokeAPIToken: %v", err)
	}
	revoked, err := repo.GetAPIToken(ctx, token.ID)
	if err != nil {
		t.Fatalf("GetAPIToken after revoke: %v", err)
	}
	if revoked.RevokedAt == nil || !revoked.RevokedAt.Equal(revokeAt) {
		t.Fatalf("revoked_at = %v, want %v", revoked.RevokedAt, revokeAt)
	}
	if revoked.Active(revokeAt) {
		t.Error("revoked token reported active")
	}

	// 幂等：再次撤销成功且 revoked_at 不变。
	if err := repo.RevokeAPIToken(ctx, token.ID, revokeAt.Add(time.Hour)); err != nil {
		t.Fatalf("RevokeAPIToken (again) = %v, want nil", err)
	}
	revoked, err = repo.GetAPIToken(ctx, token.ID)
	if err != nil {
		t.Fatalf("GetAPIToken after re-revoke: %v", err)
	}
	if !revoked.RevokedAt.Equal(revokeAt) {
		t.Errorf("revoked_at after re-revoke = %v, want unchanged %v", revoked.RevokedAt, revokeAt)
	}

	// 撤销不存在的 token 返回 ErrNotFound（与幂等撤销已存在 token 区分）。
	if err := repo.RevokeAPIToken(ctx, "tok_missing", now); !errors.Is(err, auth.ErrNotFound) {
		t.Errorf("RevokeAPIToken(missing) = %v, want ErrNotFound", err)
	}
}

// TouchAPITokenLastUsed 节流：阈值内的重复调用不写入。
func TestTouchAPITokenLastUsedThrottle(t *testing.T) {
	repo, _ := testRepo(t)
	ctx := context.Background()
	now := time.Unix(1757879400, 0).UTC()

	token, hash := tokenFixture("tok_a", now, nil)
	if err := repo.CreateAPIToken(ctx, token, hash); err != nil {
		t.Fatalf("CreateAPIToken: %v", err)
	}

	// 首次使用立即写入。
	firstUse := now.Add(10 * time.Second)
	if err := repo.TouchAPITokenLastUsed(ctx, token.ID, firstUse); err != nil {
		t.Fatalf("TouchAPITokenLastUsed: %v", err)
	}
	got, err := repo.GetAPIToken(ctx, token.ID)
	if err != nil {
		t.Fatalf("GetAPIToken: %v", err)
	}
	if got.LastUsedAt == nil || !got.LastUsedAt.Equal(firstUse) {
		t.Fatalf("last_used_at = %v, want %v", got.LastUsedAt, firstUse)
	}

	// 30 秒后再用：在 1 分钟阈值内，不写入。
	if err := repo.TouchAPITokenLastUsed(ctx, token.ID, firstUse.Add(30*time.Second)); err != nil {
		t.Fatalf("TouchAPITokenLastUsed (throttled): %v", err)
	}
	got, err = repo.GetAPIToken(ctx, token.ID)
	if err != nil {
		t.Fatalf("GetAPIToken: %v", err)
	}
	if !got.LastUsedAt.Equal(firstUse) {
		t.Errorf("last_used_at after throttled touch = %v, want unchanged %v", got.LastUsedAt, firstUse)
	}

	// 超过阈值后写入生效。
	later := firstUse.Add(2 * time.Minute)
	if err := repo.TouchAPITokenLastUsed(ctx, token.ID, later); err != nil {
		t.Fatalf("TouchAPITokenLastUsed (after threshold): %v", err)
	}
	got, err = repo.GetAPIToken(ctx, token.ID)
	if err != nil {
		t.Fatalf("GetAPIToken: %v", err)
	}
	if !got.LastUsedAt.Equal(later) {
		t.Errorf("last_used_at after threshold touch = %v, want %v", got.LastUsedAt, later)
	}
}

func TestListAPITokens(t *testing.T) {
	repo, _ := testRepo(t)
	ctx := context.Background()
	now := time.Unix(1757879400, 0).UTC()

	// 空库返回 nil，前端可归一为空数组语义由 API 层处理。
	list, err := repo.ListAPITokens(ctx)
	if err != nil {
		t.Fatalf("ListAPITokens (empty): %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("empty list = %v, want empty", list)
	}

	tokA, hashA := tokenFixture("tok_a", now, nil)
	tokB, hashB := tokenFixture("tok_b", now.Add(time.Second), nil)
	tokB.Scopes = []auth.Scope{auth.ScopeAdmin}
	if err := repo.CreateAPIToken(ctx, tokB, hashB); err != nil {
		t.Fatalf("CreateAPIToken b: %v", err)
	}
	if err := repo.CreateAPIToken(ctx, tokA, hashA); err != nil {
		t.Fatalf("CreateAPIToken a: %v", err)
	}

	list, err = repo.ListAPITokens(ctx)
	if err != nil {
		t.Fatalf("ListAPITokens: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("list length = %d, want 2", len(list))
	}
	if list[0].ID != "tok_a" || list[1].ID != "tok_b" {
		t.Errorf("list order = (%s, %s), want created_at ASC (tok_a, tok_b)", list[0].ID, list[1].ID)
	}
	if len(list[1].Scopes) != 1 || list[1].Scopes[0] != auth.ScopeAdmin {
		t.Errorf("tok_b scopes = %v, want [admin]", list[1].Scopes)
	}
}

// 重启后 admin / session / token 状态完整恢复。
func TestPersistenceAcrossRestart(t *testing.T) {
	repo, dataDir := testRepo(t)
	ctx := context.Background()
	now := time.Unix(1757879400, 0).UTC()

	if err := repo.SetAdminPassword(ctx, "argon2-hash", now); err != nil {
		t.Fatalf("SetAdminPassword: %v", err)
	}
	session := auth.WebSession{ID: "ses_a", CreatedAt: now, ExpiresAt: now.Add(7 * 24 * time.Hour)}
	if err := repo.CreateSession(ctx, session, []byte("session-hash")); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	token, tokenHash := tokenFixture("tok_a", now, nil)
	if err := repo.CreateAPIToken(ctx, token, tokenHash); err != nil {
		t.Fatalf("CreateAPIToken: %v", err)
	}
	if err := repo.RevokeAPIToken(ctx, token.ID, now.Add(time.Minute)); err != nil {
		t.Fatalf("RevokeAPIToken: %v", err)
	}

	reopened := reopenRepo(t, dataDir)
	configured, err := reopened.AdminConfigured(ctx)
	if err != nil || !configured {
		t.Fatalf("AdminConfigured after restart = (%v, %v), want (true, nil)", configured, err)
	}
	if _, err := reopened.GetSessionByHash(ctx, []byte("session-hash")); err != nil {
		t.Errorf("session after restart = %v, want nil", err)
	}
	got, err := reopened.GetAPIToken(ctx, token.ID)
	if err != nil {
		t.Fatalf("token after restart: %v", err)
	}
	if got.RevokedAt == nil {
		t.Error("revocation lost after restart")
	}
}

// scope 存储格式：排序去重后的 JSON 数组，可无损往返；未知 scope
// 拒绝。
func TestScopeMarshaling(t *testing.T) {
	raw, err := auth.MarshalScopes([]auth.Scope{auth.ScopeRun, auth.ScopeRead, auth.ScopeRun})
	if err != nil {
		t.Fatalf("MarshalScopes: %v", err)
	}
	if raw != `["read","run"]` {
		t.Fatalf("marshaled scopes = %s, want [\"read\",\"run\"]", raw)
	}
	scopes, err := auth.UnmarshalScopes(raw)
	if err != nil {
		t.Fatalf("UnmarshalScopes: %v", err)
	}
	if len(scopes) != 2 || scopes[0] != auth.ScopeRead || scopes[1] != auth.ScopeRun {
		t.Errorf("round-trip scopes = %v, want [read run]", scopes)
	}

	if _, err := auth.MarshalScopes(nil); !errors.Is(err, auth.ErrInvalidInput) {
		t.Errorf("MarshalScopes(nil) = %v, want ErrInvalidInput", err)
	}
	if _, err := auth.MarshalScopes([]auth.Scope{"root"}); !errors.Is(err, auth.ErrInvalidInput) {
		t.Errorf("MarshalScopes(unknown) = %v, want ErrInvalidInput", err)
	}
	if _, err := auth.UnmarshalScopes(`["sudo"]`); !errors.Is(err, auth.ErrInvalidInput) {
		t.Errorf("UnmarshalScopes(unknown) = %v, want ErrInvalidInput", err)
	}
}

// HTTP Source 的 auth_method ↔ secret 存储不变量测试：Update 必须
// 维护「认证方式 ↔ secret」的最终一致（外部测试包：复用 stubFactory
// 与真实 SQLite 仓库）。
package source_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"tinysync/internal/source"
	"tinysync/internal/source/sqlite"
	"tinysync/internal/storage"
)

// newHTTPTestService 构造带真实 SQLite 仓库的 Service 并返回仓库
// （GetCredentials 明文断言用）。
func newHTTPTestService(t *testing.T) (*source.Service, source.Repository) {
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
	repo := sqlite.New(db)
	svc := source.NewService(repo, &stubFactory{remote: &stubRemote{}})
	svc.Now = func() time.Time { return time.Unix(1757879400, 0).UTC() }
	return svc, repo
}

// httpPw / httpTok 便捷构造 secret 指针。
func httpPw(v string) *string { return &v }

// createHTTPSource 按指定认证方式创建 HTTP Source（credentials 为该
// 方式的合法 secret）。
func createHTTPSource(t *testing.T, svc *source.Service, auth source.HTTPAuthMethod, password, token string) source.Source {
	t.Helper()
	cfg := source.HTTPConfig{
		BaseURL:        "https://mirror.example.com/releases/",
		ListingMode:    source.HTTPListingAuto,
		AuthMethod:     auth,
		CaddyFileLimit: source.DefaultCaddyFileLimit,
	}
	if auth == source.HTTPAuthBasic {
		cfg.Username = "tinysync"
	}
	creds := source.Credentials{}
	if password != "" || token != "" {
		creds.HTTP = &source.HTTPCredentials{Password: password, BearerToken: token}
	}
	src, err := svc.Create(context.Background(), source.CreateInput{
		Name:        "mirror",
		Type:        source.TypeHTTP,
		Config:      source.Config{HTTP: &cfg},
		Credentials: creds,
		Enabled:     true,
	})
	if err != nil {
		t.Fatalf("Create (%s): %v", auth, err)
	}
	return src
}

// httpConfigWith 返回修改 auth_method 后的配置（basic 补 username）。
func httpConfigWith(base source.Source, auth source.HTTPAuthMethod) *source.Config {
	cfg := *base.Config.HTTP
	cfg.AuthMethod = auth
	cfg.Username = ""
	if auth == source.HTTPAuthBasic {
		cfg.Username = "tinysync"
	}
	return &source.Config{HTTP: &cfg}
}

// assertHTTPSecrets 断言存储中的最终 secret 形态。
func assertHTTPSecrets(t *testing.T, repo source.Repository, id, wantPassword, wantToken string) {
	t.Helper()
	creds, err := repo.GetCredentials(context.Background(), id)
	if err != nil {
		t.Fatalf("GetCredentials: %v", err)
	}
	if creds.HTTP == nil {
		creds.HTTP = &source.HTTPCredentials{}
	}
	if creds.HTTP.Password != wantPassword || creds.HTTP.BearerToken != wantToken {
		t.Errorf("stored secrets = (password %q, token %q), want (%q, %q)",
			creds.HTTP.Password, creds.HTTP.BearerToken, wantPassword, wantToken)
	}
}

// auth_method 六种转换的最终有效凭据：旧方式的存量 secret 被清除，
// 新方式缺 secret 的更新在入口拒绝。
func TestServiceUpdateHTTPAuthTransition(t *testing.T) {
	cases := []struct {
		name        string
		from        source.HTTPAuthMethod
		fromSecret  string // 旧方式的存量 secret
		to          source.HTTPAuthMethod
		newPassword string // 同请求携带的新 secret（按需）
		newToken    string
		wantErr     string // 非空表示更新被拒绝
		wantPass    string // 期望的最终 secret（更新成功时）
		wantToken   string
	}{
		{"none to basic", source.HTTPAuthNone, "", source.HTTPAuthBasic, "pw", "", "", "pw", ""},
		{"none to bearer", source.HTTPAuthNone, "", source.HTTPAuthBearer, "", "tok", "", "", "tok"},
		{"basic to none", source.HTTPAuthBasic, "old_pw", source.HTTPAuthNone, "", "", "", "", ""},
		{"basic to bearer", source.HTTPAuthBasic, "old_pw", source.HTTPAuthBearer, "", "tok", "", "", "tok"},
		{"bearer to none", source.HTTPAuthBearer, "old_tok", source.HTTPAuthNone, "", "", "", "", ""},
		{"bearer to basic", source.HTTPAuthBearer, "old_tok", source.HTTPAuthBasic, "pw", "", "", "pw", ""},
		// 新方式缺 secret：拒绝，而不是保存一个打不开 Remote 的源。
		{"none to basic without password", source.HTTPAuthNone, "", source.HTTPAuthBasic, "", "", "password is required", "", ""},
		{"none to bearer without token", source.HTTPAuthNone, "", source.HTTPAuthBearer, "", "", "bearer token is required", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc, repo := newHTTPTestService(t)
			ctx := context.Background()
			fromPw, fromTok := "", ""
			if tc.from == source.HTTPAuthBasic {
				fromPw = tc.fromSecret
			}
			if tc.from == source.HTTPAuthBearer {
				fromTok = tc.fromSecret
			}
			src := createHTTPSource(t, svc, tc.from, fromPw, fromTok)

			input := source.UpdateInput{Config: httpConfigWith(src, tc.to)}
			if tc.newPassword != "" || tc.newToken != "" {
				input.Credentials = &source.CredentialsUpdate{HTTP: &source.HTTPCredentialsUpdate{
					Password:    httpPw(tc.newPassword),
					BearerToken: httpPw(tc.newToken),
				}}
			}
			_, err := svc.Update(ctx, src.ID, input)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("Update error = %v, want containing %q", err, tc.wantErr)
				}
				// 拒绝时存量保持不变。
				assertHTTPSecrets(t, repo, src.ID, fromPw, fromTok)
				return
			}
			if err != nil {
				t.Fatalf("Update: %v", err)
			}
			assertHTTPSecrets(t, repo, src.ID, tc.wantPass, tc.wantToken)
			got, err := svc.Get(ctx, src.ID)
			if err != nil {
				t.Fatalf("Get: %v", err)
			}
			if got.CredentialState.HTTP == nil {
				t.Fatal("credential state missing http group")
			}
			if want := tc.wantPass != ""; got.CredentialState.HTTP.PasswordSet != want {
				t.Errorf("password_set = %v, want %v", got.CredentialState.HTTP.PasswordSet, want)
			}
			if want := tc.wantToken != ""; got.CredentialState.HTTP.BearerTokenSet != want {
				t.Errorf("bearer_token_set = %v, want %v", got.CredentialState.HTTP.BearerTokenSet, want)
			}
		})
	}
}

// 同请求同时携带两种 secret（恶意 / 异常客户端）：最终存储只保留
// 当前认证方式的 secret，绝不持久化双活跃形态。
func TestServiceUpdateHTTPRejectsDormantDualSecrets(t *testing.T) {
	cases := []struct {
		name     string
		auth     source.HTTPAuthMethod
		password string
		token    string
		wantPass string
		wantTok  string
	}{
		{"basic keeps password only", source.HTTPAuthBasic, "pw", "evil-tok", "pw", ""},
		{"bearer keeps token only", source.HTTPAuthBearer, "evil-pw", "tok", "", "tok"},
		{"none clears both", source.HTTPAuthNone, "evil-pw", "evil-tok", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc, repo := newHTTPTestService(t)
			ctx := context.Background()
			// 存量：与目标方式匹配的合法 secret。
			fromPw, fromTok := "", ""
			if tc.auth == source.HTTPAuthBasic {
				fromPw = "old_pw"
			}
			if tc.auth == source.HTTPAuthBearer {
				fromTok = "old_tok"
			}
			src := createHTTPSource(t, svc, tc.auth, fromPw, fromTok)

			if _, err := svc.Update(ctx, src.ID, source.UpdateInput{
				Credentials: &source.CredentialsUpdate{HTTP: &source.HTTPCredentialsUpdate{
					Password:    httpPw(tc.password),
					BearerToken: httpPw(tc.token),
				}},
			}); err != nil {
				t.Fatalf("Update: %v", err)
			}
			assertHTTPSecrets(t, repo, src.ID, tc.wantPass, tc.wantTok)
		})
	}
}

// Create 边界的严格匹配：basic 拒绝携带 token、bearer 拒绝携带
// password（ValidateCredentials 不变量，防 Create 路径写入沉睡 secret）。
func TestServiceCreateHTTPStrictSecretMatch(t *testing.T) {
	svc, repo := newHTTPTestService(t)
	ctx := context.Background()

	bad := createInputFor(source.HTTPAuthBasic, "u")
	bad.Credentials = source.Credentials{HTTP: &source.HTTPCredentials{Password: "pw", BearerToken: "tok"}}
	if _, err := svc.Create(ctx, bad); err == nil || !strings.Contains(err.Error(), "does not accept a bearer token") {
		t.Errorf("basic + token error = %v, want rejection", err)
	}

	bad2 := createInputFor(source.HTTPAuthBearer, "")
	bad2.Credentials = source.Credentials{HTTP: &source.HTTPCredentials{Password: "pw", BearerToken: "tok"}}
	if _, err := svc.Create(ctx, bad2); err == nil || !strings.Contains(err.Error(), "does not accept a password") {
		t.Errorf("bearer + password error = %v, want rejection", err)
	}

	// 合法创建不受影响。
	ok := createInputFor(source.HTTPAuthBasic, "u")
	ok.Credentials = source.Credentials{HTTP: &source.HTTPCredentials{Password: "pw"}}
	src, err := svc.Create(ctx, ok)
	if err != nil {
		t.Fatalf("Create basic: %v", err)
	}
	assertHTTPSecrets(t, repo, src.ID, "pw", "")
}

// createInputFor 构造 HTTP Create 输入。
func createInputFor(auth source.HTTPAuthMethod, username string) source.CreateInput {
	cfg := source.HTTPConfig{
		BaseURL:        "https://mirror.example.com/releases/",
		ListingMode:    source.HTTPListingAuto,
		AuthMethod:     auth,
		Username:       username,
		CaddyFileLimit: source.DefaultCaddyFileLimit,
	}
	return source.CreateInput{
		Name:    "mirror",
		Type:    source.TypeHTTP,
		Config:  source.Config{HTTP: &cfg},
		Enabled: true,
	}
}

// 与认证无关的更新（改名）也会顺手修复存量里的不变量缺口（遗留
// dormant secret 被清除），且不改变当前方式的 secret。
func TestServiceUpdateHTTPPurgesLegacyDormantSecret(t *testing.T) {
	svc, repo := newHTTPTestService(t)
	ctx := context.Background()
	src := createHTTPSource(t, svc, source.HTTPAuthBasic, "keep_me", "")
	// 直接经仓库写入遗留 dormant token（绕过 Service，模拟旧版本
	// 保存的形态）。
	if err := repo.Update(ctx, src, &source.CredentialsUpdate{HTTP: &source.HTTPCredentialsUpdate{
		BearerToken: httpPw("legacy_tok"),
	}}); err != nil {
		t.Fatalf("repo.Update legacy: %v", err)
	}

	name := "renamed"
	if _, err := svc.Update(ctx, src.ID, source.UpdateInput{Name: &name}); err != nil {
		t.Fatalf("Update name: %v", err)
	}
	assertHTTPSecrets(t, repo, src.ID, "keep_me", "")
}

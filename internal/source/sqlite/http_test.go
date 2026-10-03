package sqlite

import (
	"context"
	"testing"
	"time"

	"tinysync/internal/source"
)

// newHTTPSource 构造测试用 HTTP Source（canonical form 配置）。
func newHTTPSource(id, name string) source.Source {
	now := time.Unix(1758900000, 0).UTC()
	return source.Source{
		ID:   id,
		Name: name,
		Type: source.TypeHTTP,
		Config: source.Config{HTTP: &source.HTTPConfig{
			BaseURL:        "https://mirror.example.com/releases/",
			ListingMode:    source.HTTPListingAuto,
			AuthMethod:     source.HTTPAuthBasic,
			Username:       "tinysync",
			CaddyFileLimit: source.DefaultCaddyFileLimit,
		}},
		Enabled:   true,
		CreatedAt: now,
		UpdatedAt: now,
	}
}

// httpCredentialState 便捷读取 HTTP 凭据状态。
func httpCredentialState(s source.Source) (passwordSet, tokenSet bool) {
	if s.CredentialState.HTTP == nil {
		return false, false
	}
	return s.CredentialState.HTTP.PasswordSet, s.CredentialState.HTTP.BearerTokenSet
}

// HTTP Source 的完整持久化回路：config 全字段 roundtrip、SQL 推导的
// password_set / bearer_token_set 状态、GetCredentials 读回两个 secret
// 明文（唯一明文路径）。
func TestHTTPCreateAndGet(t *testing.T) {
	_, repo := openRepository(t)
	ctx := context.Background()

	s := newHTTPSource("src_http1", "Mirror HTTP")
	s.CredentialState = source.CredentialState{
		HTTP: &source.HTTPCredentialState{PasswordSet: true},
	}
	mustCreate(t, repo, s, source.Credentials{HTTP: &source.HTTPCredentials{Password: "http_secret"}})

	got, err := repo.Get(ctx, "src_http1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Config.HTTP == nil {
		t.Fatal("http config missing")
	}
	want := *s.Config.HTTP
	if *got.Config.HTTP != want {
		t.Errorf("config = %+v, want roundtrip of %+v", *got.Config.HTTP, want)
	}
	passwordSet, tokenSet := httpCredentialState(got)
	if !passwordSet || tokenSet {
		t.Errorf("credential state = (password %v, token %v), want (true, false)", passwordSet, tokenSet)
	}

	creds, err := repo.GetCredentials(ctx, "src_http1")
	if err != nil {
		t.Fatalf("GetCredentials: %v", err)
	}
	if creds.HTTP == nil || creds.HTTP.Password != "http_secret" || creds.HTTP.BearerToken != "" {
		t.Errorf("GetCredentials = %+v, want password roundtrip", creds.HTTP)
	}

	// bearer 源：token_set 为 true，password 为空。
	bearer := newHTTPSource("src_http2", "Mirror Bearer")
	bearer.Config.HTTP.AuthMethod = source.HTTPAuthBearer
	bearer.Config.HTTP.Username = ""
	bearer.CredentialState = source.CredentialState{
		HTTP: &source.HTTPCredentialState{BearerTokenSet: true},
	}
	mustCreate(t, repo, bearer, source.Credentials{HTTP: &source.HTTPCredentials{BearerToken: "tok"}})
	gotBearer, err := repo.Get(ctx, "src_http2")
	if err != nil {
		t.Fatalf("Get bearer: %v", err)
	}
	passwordSet, tokenSet = httpCredentialState(gotBearer)
	if passwordSet || !tokenSet {
		t.Errorf("bearer state = (password %v, token %v), want (false, true)", passwordSet, tokenSet)
	}
}

// HTTP 的三态 secret 更新：nil 保留、空串清除、非空替换；password 与
// bearer_token 相互独立。
func TestHTTPUpdateSecrets(t *testing.T) {
	_, repo := openRepository(t)
	ctx := context.Background()

	s := newHTTPSource("src_http3", "Mirror HTTP")
	s.CredentialState = source.CredentialState{
		HTTP: &source.HTTPCredentialState{PasswordSet: true, BearerTokenSet: true},
	}
	mustCreate(t, repo, s, source.Credentials{HTTP: &source.HTTPCredentials{
		Password:    "old_pass",
		BearerToken: "old_tok",
	}})

	// nil 两者：保留。
	if err := repo.Update(ctx, s, &source.CredentialsUpdate{
		HTTP: &source.HTTPCredentialsUpdate{},
	}); err != nil {
		t.Fatalf("Update keep: %v", err)
	}
	creds, err := repo.GetCredentials(ctx, "src_http3")
	if err != nil {
		t.Fatalf("GetCredentials after keep: %v", err)
	}
	if creds.HTTP.Password != "old_pass" || creds.HTTP.BearerToken != "old_tok" {
		t.Errorf("secrets = (%q, %q), want kept", creds.HTTP.Password, creds.HTTP.BearerToken)
	}

	// 清除 token、替换 password：单字段独立三态。
	if err := repo.Update(ctx, s, &source.CredentialsUpdate{
		HTTP: &source.HTTPCredentialsUpdate{
			Password:    ptrStr("new_pass"),
			BearerToken: ptrStr(""),
		},
	}); err != nil {
		t.Fatalf("Update partial: %v", err)
	}
	creds, err = repo.GetCredentials(ctx, "src_http3")
	if err != nil {
		t.Fatalf("GetCredentials after partial: %v", err)
	}
	if creds.HTTP.Password != "new_pass" || creds.HTTP.BearerToken != "" {
		t.Errorf("secrets = (%q, %q), want (new_pass, cleared)", creds.HTTP.Password, creds.HTTP.BearerToken)
	}
	got, err := repo.Get(ctx, "src_http3")
	if err != nil {
		t.Fatalf("Get after partial: %v", err)
	}
	passwordSet, tokenSet := httpCredentialState(got)
	if !passwordSet || tokenSet {
		t.Errorf("state = (password %v, token %v), want (true, false)", passwordSet, tokenSet)
	}
}

// config 更新（base_url / listing_mode 变更）roundtrip，credential
// state 不受影响。
func TestHTTPUpdateConfig(t *testing.T) {
	_, repo := openRepository(t)
	ctx := context.Background()

	s := newHTTPSource("src_http4", "Mirror HTTP")
	s.CredentialState = source.CredentialState{
		HTTP: &source.HTTPCredentialState{PasswordSet: true},
	}
	mustCreate(t, repo, s, source.Credentials{HTTP: &source.HTTPCredentials{Password: "keep_me"}})

	updated := s
	updated.Config = source.Config{HTTP: &source.HTTPConfig{
		BaseURL:        "https://mirror2.example.com/dist/",
		ListingMode:    source.HTTPListingCaddy,
		AuthMethod:     source.HTTPAuthNone,
		CaddyFileLimit: 100000,
	}}
	if err := repo.Update(ctx, updated, nil); err != nil {
		t.Fatalf("Update config: %v", err)
	}
	got, err := repo.Get(ctx, "src_http4")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	cfg := got.Config.HTTP
	if cfg == nil ||
		cfg.BaseURL != "https://mirror2.example.com/dist/" ||
		cfg.ListingMode != source.HTTPListingCaddy ||
		cfg.AuthMethod != source.HTTPAuthNone ||
		cfg.Username != "" ||
		cfg.CaddyFileLimit != 100000 {
		t.Errorf("config = %+v, want updated values", cfg)
	}
	passwordSet, _ := httpCredentialState(got)
	if !passwordSet {
		t.Error("password_set = false after config-only update, want true")
	}
}

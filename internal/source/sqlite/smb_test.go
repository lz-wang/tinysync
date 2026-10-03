package sqlite

import (
	"context"
	"testing"
	"time"

	"tinysync/internal/source"
)

// newSMBSource 构造测试用 SMB Source（canonical form 配置）。
func newSMBSource(id, name string) source.Source {
	now := time.Unix(1758900000, 0).UTC()
	return source.Source{
		ID:   id,
		Name: name,
		Type: source.TypeSMB,
		Config: source.Config{SMB: &source.SMBConfig{
			Host:       "nas.example.com",
			Port:       445,
			Share:      "backup",
			RemoteRoot: "/photos",
			Username:   "tinysync",
			Domain:     "WORKGROUP",
			Signing:    source.SMBSigningRequired,
		}},
		Enabled:   true,
		CreatedAt: now,
		UpdatedAt: now,
	}
}

// smbPasswordSet 便捷读取 SMB 凭据状态。
func smbPasswordSet(s source.Source) bool {
	return s.CredentialState.SMB != nil && s.CredentialState.SMB.PasswordSet
}

// smbConfigOf 便捷读取 SMB 配置，缺失即失败。
func smbConfigOf(t *testing.T, s source.Source) source.SMBConfig {
	t.Helper()
	if s.Config.SMB == nil {
		t.Fatalf("source %s has no smb config", s.ID)
	}
	return *s.Config.SMB
}

// SMB Source 的完整持久化回路：config 全字段 roundtrip、SQL 推导的
// password_set 状态、GetCredentials 读回 password 明文（唯一明文路径）。
func TestSMBCreateAndGet(t *testing.T) {
	_, repo := openRepository(t)
	ctx := context.Background()

	s := newSMBSource("src_smb1", "NAS SMB")
	s.CredentialState = source.CredentialState{
		SMB: &source.SMBCredentialState{PasswordSet: true},
	}
	mustCreate(t, repo, s, source.Credentials{SMB: &source.SMBCredentials{Password: "smb_secret"}})

	got, err := repo.Get(ctx, "src_smb1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	cfg := smbConfigOf(t, got)
	if cfg.Host != "nas.example.com" || cfg.Port != 445 || cfg.Share != "backup" ||
		cfg.RemoteRoot != "/photos" || cfg.Username != "tinysync" ||
		cfg.Domain != "WORKGROUP" || cfg.Signing != source.SMBSigningRequired {
		t.Errorf("config = %+v, want roundtrip of %+v", cfg, s.Config.SMB)
	}
	if !smbPasswordSet(got) {
		t.Error("password_set = false, want true")
	}

	creds, err := repo.GetCredentials(ctx, "src_smb1")
	if err != nil {
		t.Fatalf("GetCredentials: %v", err)
	}
	if creds.SMB == nil || creds.SMB.Password != "smb_secret" {
		t.Errorf("GetCredentials = %+v, want password roundtrip", creds)
	}

	// 空密码 Source：password_set 为 false，GetCredentials 返回空密码。
	empty := newSMBSource("src_smb2", "NAS SMB Empty")
	mustCreate(t, repo, empty, source.Credentials{SMB: &source.SMBCredentials{}})
	gotEmpty, err := repo.Get(ctx, "src_smb2")
	if err != nil {
		t.Fatalf("Get empty: %v", err)
	}
	if smbPasswordSet(gotEmpty) {
		t.Error("empty password_set = true, want false")
	}
}

// SMB 的三态 password 更新：nil 保留、空串清除、非空替换；全程不
// 触碰 config。
func TestSMBUpdatePassword(t *testing.T) {
	_, repo := openRepository(t)
	ctx := context.Background()

	s := newSMBSource("src_smb3", "NAS SMB")
	s.CredentialState = source.CredentialState{
		SMB: &source.SMBCredentialState{PasswordSet: true},
	}
	mustCreate(t, repo, s, source.Credentials{SMB: &source.SMBCredentials{Password: "old_pass"}})

	// nil password：保留。
	if err := repo.Update(ctx, s, &source.CredentialsUpdate{
		SMB: &source.SMBCredentialsUpdate{},
	}); err != nil {
		t.Fatalf("Update keep: %v", err)
	}
	creds, err := repo.GetCredentials(ctx, "src_smb3")
	if err != nil {
		t.Fatalf("GetCredentials after keep: %v", err)
	}
	if creds.SMB.Password != "old_pass" {
		t.Errorf("password = %q, want kept", creds.SMB.Password)
	}

	// 空串：清除。
	if err := repo.Update(ctx, s, &source.CredentialsUpdate{
		SMB: &source.SMBCredentialsUpdate{Password: ptrStr("")},
	}); err != nil {
		t.Fatalf("Update clear: %v", err)
	}
	creds, err = repo.GetCredentials(ctx, "src_smb3")
	if err != nil {
		t.Fatalf("GetCredentials after clear: %v", err)
	}
	if creds.SMB.Password != "" {
		t.Errorf("password = %q, want cleared", creds.SMB.Password)
	}
	got, err := repo.Get(ctx, "src_smb3")
	if err != nil {
		t.Fatalf("Get after clear: %v", err)
	}
	if smbPasswordSet(got) {
		t.Error("password_set = true after clear, want false")
	}

	// 非空：替换。
	if err := repo.Update(ctx, s, &source.CredentialsUpdate{
		SMB: &source.SMBCredentialsUpdate{Password: ptrStr("new_pass")},
	}); err != nil {
		t.Fatalf("Update replace: %v", err)
	}
	creds, err = repo.GetCredentials(ctx, "src_smb3")
	if err != nil {
		t.Fatalf("GetCredentials after replace: %v", err)
	}
	if creds.SMB.Password != "new_pass" {
		t.Errorf("password = %q, want replaced", creds.SMB.Password)
	}
}

// config 更新（remote_root 变更）roundtrip，credential state 不受影响。
func TestSMBUpdateConfig(t *testing.T) {
	_, repo := openRepository(t)
	ctx := context.Background()

	s := newSMBSource("src_smb4", "NAS SMB")
	s.CredentialState = source.CredentialState{
		SMB: &source.SMBCredentialState{PasswordSet: true},
	}
	mustCreate(t, repo, s, source.Credentials{SMB: &source.SMBCredentials{Password: "keep_me"}})

	updated := s
	updated.Config = source.Config{SMB: &source.SMBConfig{
		Host:       "nas2.example.com",
		Port:       1445,
		Share:      "media$",
		RemoteRoot: "/",
		Username:   "tinysync",
		Domain:     "",
		Signing:    source.SMBSigningAuto,
	}}
	if err := repo.Update(ctx, updated, nil); err != nil {
		t.Fatalf("Update config: %v", err)
	}
	got, err := repo.Get(ctx, "src_smb4")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	cfg := smbConfigOf(t, got)
	if cfg.Host != "nas2.example.com" || cfg.Port != 1445 || cfg.Share != "media$" ||
		cfg.RemoteRoot != "/" || cfg.Domain != "" || cfg.Signing != source.SMBSigningAuto {
		t.Errorf("config = %+v, want updated values", cfg)
	}
	if !smbPasswordSet(got) {
		t.Error("password_set = false after config-only update, want true")
	}
}

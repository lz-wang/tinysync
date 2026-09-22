package sqlite

import (
	"context"
	"testing"
	"time"

	"tinysync/internal/source"
)

// newGitHubSource 构造测试用 GitHub Release Source。
func newGitHubSource(id, name string) source.Source {
	now := time.Unix(1758900000, 0).UTC()
	return source.Source{
		ID:   id,
		Name: name,
		Type: source.TypeGitHubRelease,
		Config: source.Config{GitHubRelease: &source.GitHubReleaseConfig{
			Repository:         "gitea/gitea",
			ReleasePolicy:      source.ReleaseRecent,
			RecentCount:        3,
			IncludePrereleases: false,
			VerifySHA256:       source.SHA256IfAvailable,
		}},
		Enabled:   true,
		CreatedAt: now,
		UpdatedAt: now,
	}
}

// githubTokenSet 便捷读取 GitHub Release 凭据状态。
func githubTokenSet(s source.Source) bool {
	return s.CredentialState.GitHubRelease != nil && s.CredentialState.GitHubRelease.TokenSet
}

// githubConfigOf 便捷读取 GitHub Release 配置，缺失即失败。
func githubConfigOf(t *testing.T, s source.Source) source.GitHubReleaseConfig {
	t.Helper()
	if s.Config.GitHubRelease == nil {
		t.Fatalf("source %s has no github_release config", s.ID)
	}
	return *s.Config.GitHubRelease
}

// GitHub Release Source 的完整持久化回路：config 全字段 roundtrip、
// token 有无两种凭据状态、GetCredentials 读回 token 明文。
func TestGitHubReleaseCreateAndGet(t *testing.T) {
	_, repo := openRepository(t)
	ctx := context.Background()

	s := newGitHubSource("src_gh1", "Gitea")
	s.CredentialState = source.CredentialState{
		GitHubRelease: &source.GitHubReleaseCredentialState{TokenSet: true},
	}
	mustCreate(t, repo, s, source.Credentials{GitHubRelease: &source.GitHubReleaseCredentials{Token: "ghp_secret"}})

	got, err := repo.Get(ctx, "src_gh1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	gh := githubConfigOf(t, got)
	if gh.Repository != "gitea/gitea" || gh.ReleasePolicy != source.ReleaseRecent ||
		gh.RecentCount != 3 || gh.IncludePrereleases || gh.VerifySHA256 != source.SHA256IfAvailable {
		t.Errorf("config = %+v, want roundtrip of %+v", gh, s.Config.GitHubRelease)
	}
	if !githubTokenSet(got) {
		t.Error("token_set = false, want true")
	}

	creds, err := repo.GetCredentials(ctx, "src_gh1")
	if err != nil {
		t.Fatalf("GetCredentials: %v", err)
	}
	if creds.GitHubRelease == nil || creds.GitHubRelease.Token != "ghp_secret" {
		t.Errorf("GetCredentials = %+v, want token roundtrip", creds)
	}

	// 匿名 Source：token_set 为 false，GetCredentials 返回空 token。
	anon := newGitHubSource("src_gh2", "Gitea Anon")
	mustCreate(t, repo, anon, source.Credentials{GitHubRelease: &source.GitHubReleaseCredentials{}})
	gotAnon, err := repo.Get(ctx, "src_gh2")
	if err != nil {
		t.Fatalf("Get anon: %v", err)
	}
	if githubTokenSet(gotAnon) {
		t.Error("anon token_set = true, want false")
	}
}

// GitHub Release 的三态 token 更新：nil 保留、空串清除、非空替换；
// 全程不触碰 config。
func TestGitHubReleaseUpdateToken(t *testing.T) {
	_, repo := openRepository(t)
	ctx := context.Background()

	s := newGitHubSource("src_gh3", "Gitea")
	s.CredentialState = source.CredentialState{
		GitHubRelease: &source.GitHubReleaseCredentialState{TokenSet: true},
	}
	mustCreate(t, repo, s, source.Credentials{GitHubRelease: &source.GitHubReleaseCredentials{Token: "old_token"}})

	// nil token：保留。
	if err := repo.Update(ctx, s, &source.CredentialsUpdate{
		GitHubRelease: &source.GitHubReleaseCredentialsUpdate{},
	}); err != nil {
		t.Fatalf("Update keep: %v", err)
	}
	creds, err := repo.GetCredentials(ctx, "src_gh3")
	if err != nil {
		t.Fatalf("GetCredentials after keep: %v", err)
	}
	if creds.GitHubRelease.Token != "old_token" {
		t.Errorf("token = %q, want kept", creds.GitHubRelease.Token)
	}

	// 空串：清除。
	if err := repo.Update(ctx, s, &source.CredentialsUpdate{
		GitHubRelease: &source.GitHubReleaseCredentialsUpdate{Token: ptrStr("")},
	}); err != nil {
		t.Fatalf("Update clear: %v", err)
	}
	creds, err = repo.GetCredentials(ctx, "src_gh3")
	if err != nil {
		t.Fatalf("GetCredentials after clear: %v", err)
	}
	if creds.GitHubRelease.Token != "" {
		t.Errorf("token = %q, want cleared", creds.GitHubRelease.Token)
	}
	got, err := repo.Get(ctx, "src_gh3")
	if err != nil {
		t.Fatalf("Get after clear: %v", err)
	}
	if githubTokenSet(got) {
		t.Error("token_set = true after clear, want false")
	}

	// 非空：替换。
	if err := repo.Update(ctx, s, &source.CredentialsUpdate{
		GitHubRelease: &source.GitHubReleaseCredentialsUpdate{Token: ptrStr("new_token")},
	}); err != nil {
		t.Fatalf("Update replace: %v", err)
	}
	creds, err = repo.GetCredentials(ctx, "src_gh3")
	if err != nil {
		t.Fatalf("GetCredentials after replace: %v", err)
	}
	if creds.GitHubRelease.Token != "new_token" {
		t.Errorf("token = %q, want replaced", creds.GitHubRelease.Token)
	}
}

// ptrStr 返回字符串指针（sqlite 包测试 helper）。
func ptrStr(s string) *string {
	return &s
}

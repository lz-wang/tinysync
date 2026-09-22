package source

import (
	"testing"
)

// validGitHubReleaseConfig 返回合法的 GitHub Release 配置基准。
func validGitHubReleaseConfig() GitHubReleaseConfig {
	return GitHubReleaseConfig{
		Repository:         "gitea/gitea",
		ReleasePolicy:      ReleaseLatest,
		IncludePrereleases: false,
		VerifySHA256:       SHA256IfAvailable,
	}
}

// ptrGH 返回 GitHub Release 配置指针。
func ptrGH(c GitHubReleaseConfig) *GitHubReleaseConfig {
	return &c
}

// ghWith 便捷构造变体。
func ghWith(mutate func(*GitHubReleaseConfig)) GitHubReleaseConfig {
	c := validGitHubReleaseConfig()
	mutate(&c)
	return c
}

// TestValidateGitHubRepository 校验 repository 字段：owner/repo 与
// github.com 仓库 URL（含 .git 后缀与深层 path）均合法；其余形态拒绝。
func TestValidateGitHubRepository(t *testing.T) {
	valid := []string{
		"gitea/gitea",
		"gitea/gitea.git",
		"lz-wang/tinysync",
		"Owner.Name/repo_name-1",
		"https://github.com/gitea/gitea",
		"https://github.com/gitea/gitea.git",
		"https://github.com/gitea/gitea/releases",
		"http://github.com/gitea/gitea",
		"https://www.github.com/gitea/gitea",
		"  gitea/gitea  ",
	}
	for _, repo := range valid {
		if err := ValidateGitHubRepository(repo); err != nil {
			t.Errorf("ValidateGitHubRepository(%q) = %v, want nil", repo, err)
		}
	}
	invalid := []string{
		"",
		"   ",
		"gitea",
		"gitea/gitea/extra",
		"https://gitlab.com/gitea/gitea",
		"ftp://github.com/gitea/gitea",
		"https://github.com/gitea",
		"https://github.com//gitea",
		"gitea/..",
		"gitea/re po",
		"gitea/re@po",
		"./gitea",
	}
	for _, repo := range invalid {
		if err := ValidateGitHubRepository(repo); err == nil {
			t.Errorf("ValidateGitHubRepository(%q) = nil, want error", repo)
		}
	}
}

// TestValidateConfigGitHubRelease 校验 github_release 配置分支：归一化
// 无法修复的约束（repository 格式、policy 取值、tag/recent 必填、
// verify_sha256 取值）按 ErrInvalid 拒绝。
func TestValidateConfigGitHubRelease(t *testing.T) {
	valid := []struct {
		name   string
		config Config
	}{
		{"latest default", Config{GitHubRelease: ptrGH(validGitHubReleaseConfig())}},
		{"policy empty before normalize", Config{GitHubRelease: ptrGH(ghWith(func(c *GitHubReleaseConfig) {
			c.ReleasePolicy = ""
		}))}},
		{"tag mode", Config{GitHubRelease: ptrGH(ghWith(func(c *GitHubReleaseConfig) {
			c.ReleasePolicy = ReleaseTag
			c.Tag = "v1.2.3"
		}))}},
		{"recent mode", Config{GitHubRelease: ptrGH(ghWith(func(c *GitHubReleaseConfig) {
			c.ReleasePolicy = ReleaseRecent
			c.RecentCount = 3
		}))}},
		{"recent max", Config{GitHubRelease: ptrGH(ghWith(func(c *GitHubReleaseConfig) {
			c.ReleasePolicy = ReleaseRecent
			c.RecentCount = MaxGitHubRecentCount
		}))}},
		{"all mode with prereleases", Config{GitHubRelease: ptrGH(ghWith(func(c *GitHubReleaseConfig) {
			c.ReleasePolicy = ReleaseAll
			c.IncludePrereleases = true
		}))}},
		{"sha256 required", Config{GitHubRelease: ptrGH(ghWith(func(c *GitHubReleaseConfig) {
			c.VerifySHA256 = SHA256Required
		}))}},
	}
	for _, tt := range valid {
		if err := ValidateConfig(TypeGitHubRelease, tt.config); err != nil {
			t.Errorf("%s = %v, want nil", tt.name, err)
		}
	}

	invalid := []struct {
		name   string
		config Config
	}{
		{"missing config", Config{}},
		{"type mismatch with webdav config", Config{
			GitHubRelease: ptrGH(validGitHubReleaseConfig()),
			WebDAV:        &WebDAVConfig{Endpoint: "https://x/"},
		}},
		{"empty repository", Config{GitHubRelease: ptrGH(ghWith(func(c *GitHubReleaseConfig) {
			c.Repository = ""
		}))}},
		{"bad repository", Config{GitHubRelease: ptrGH(ghWith(func(c *GitHubReleaseConfig) {
			c.Repository = "not-a-repo"
		}))}},
		{"unsupported policy", Config{GitHubRelease: ptrGH(ghWith(func(c *GitHubReleaseConfig) {
			c.ReleasePolicy = GitHubReleasePolicy("weekly")
		}))}},
		{"tag mode without tag", Config{GitHubRelease: ptrGH(ghWith(func(c *GitHubReleaseConfig) {
			c.ReleasePolicy = ReleaseTag
		}))}},
		{"tag mode with blank tag", Config{GitHubRelease: ptrGH(ghWith(func(c *GitHubReleaseConfig) {
			c.ReleasePolicy = ReleaseTag
			c.Tag = "   "
		}))}},
		{"recent count zero", Config{GitHubRelease: ptrGH(ghWith(func(c *GitHubReleaseConfig) {
			c.ReleasePolicy = ReleaseRecent
			c.RecentCount = 0
		}))}},
		{"recent count negative", Config{GitHubRelease: ptrGH(ghWith(func(c *GitHubReleaseConfig) {
			c.ReleasePolicy = ReleaseRecent
			c.RecentCount = -1
		}))}},
		{"recent count over max", Config{GitHubRelease: ptrGH(ghWith(func(c *GitHubReleaseConfig) {
			c.ReleasePolicy = ReleaseRecent
			c.RecentCount = MaxGitHubRecentCount + 1
		}))}},
		{"unsupported verify_sha256", Config{GitHubRelease: ptrGH(ghWith(func(c *GitHubReleaseConfig) {
			c.VerifySHA256 = GitHubSHA256Mode("strict")
		}))}},
	}
	for _, tt := range invalid {
		if err := ValidateConfig(TypeGitHubRelease, tt.config); err == nil {
			t.Errorf("%s = nil, want error", tt.name)
		}
	}
}

// ReleasePolicy / GitHubSHA256Mode 的测试用别名，避免测试内直接构造
// 未导出形态。
type (
	// ReleasePolicy 别名仅测试可见。
	ReleasePolicy = GitHubReleasePolicy
)

// TestNormalizedGitHubRelease 验证归一化契约：policy / verify_sha256
// 空值取默认，非对应策略下的 Tag / RecentCount 归一为零值，repository
// trim。
func TestNormalizedGitHubRelease(t *testing.T) {
	got := Config{GitHubRelease: ptrGH(ghWith(func(c *GitHubReleaseConfig) {
		c.Repository = "  gitea/gitea  "
		c.ReleasePolicy = ""
		c.Tag = "leftover"
		c.RecentCount = 9
		c.VerifySHA256 = ""
	}))}.Normalized(TypeGitHubRelease)
	gh := got.GitHubRelease
	if gh == nil {
		t.Fatal("normalized config is nil")
	}
	if gh.Repository != "gitea/gitea" {
		t.Errorf("repository = %q, want trimmed", gh.Repository)
	}
	if gh.ReleasePolicy != ReleaseLatest {
		t.Errorf("policy = %q, want latest default", gh.ReleasePolicy)
	}
	if gh.Tag != "" {
		t.Errorf("tag = %q, want cleared for non-tag policy", gh.Tag)
	}
	if gh.RecentCount != 0 {
		t.Errorf("recent_count = %d, want cleared for non-recent policy", gh.RecentCount)
	}
	if gh.VerifySHA256 != SHA256IfAvailable {
		t.Errorf("verify_sha256 = %q, want if_available default", gh.VerifySHA256)
	}

	// tag / recent 模式保留对应字段。
	tagged := Config{GitHubRelease: ptrGH(ghWith(func(c *GitHubReleaseConfig) {
		c.ReleasePolicy = ReleaseTag
		c.Tag = "v1.2.3"
	}))}.Normalized(TypeGitHubRelease)
	if tagged.GitHubRelease.Tag != "v1.2.3" {
		t.Errorf("tag mode tag = %q, want preserved", tagged.GitHubRelease.Tag)
	}
	recent := Config{GitHubRelease: ptrGH(ghWith(func(c *GitHubReleaseConfig) {
		c.ReleasePolicy = ReleaseRecent
		c.RecentCount = 3
	}))}.Normalized(TypeGitHubRelease)
	if recent.GitHubRelease.RecentCount != 3 {
		t.Errorf("recent mode count = %d, want preserved", recent.GitHubRelease.RecentCount)
	}
}

// TestValidateCredentialsGitHubRelease 校验 github_release 凭据分支：
// 仅接受 github_release 组；token 可为空（匿名访问）。
func TestValidateCredentialsGitHubRelease(t *testing.T) {
	cfg := Config{GitHubRelease: ptrGH(validGitHubReleaseConfig())}

	valid := []Credentials{
		{},
		{GitHubRelease: &GitHubReleaseCredentials{Token: ""}},
		{GitHubRelease: &GitHubReleaseCredentials{Token: "ghp_token"}},
	}
	for _, creds := range valid {
		if err := ValidateCredentials(TypeGitHubRelease, cfg, creds); err != nil {
			t.Errorf("ValidateCredentials(%+v) = %v, want nil", creds, err)
		}
	}
	invalid := []Credentials{
		{WebDAV: &WebDAVCredentials{Password: "x"}},
		{S3: &S3Credentials{SecretKey: "x"}},
		{SFTP: &SFTPCredentials{Password: "x"}},
	}
	for _, creds := range invalid {
		if err := ValidateCredentials(TypeGitHubRelease, cfg, creds); err == nil {
			t.Errorf("ValidateCredentials(%+v) = nil, want error", creds)
		}
	}
}

// TestGitHubReleaseCredentialState 校验凭据状态推导与三态更新。
func TestGitHubReleaseCredentialState(t *testing.T) {
	if state := CredentialStateOf(TypeGitHubRelease, Credentials{}); state.GitHubRelease != nil && state.GitHubRelease.TokenSet {
		t.Error("anonymous credentials should report token_set=false")
	}
	set := CredentialStateOf(TypeGitHubRelease, Credentials{GitHubRelease: &GitHubReleaseCredentials{Token: "t"}})
	if set.GitHubRelease == nil || !set.GitHubRelease.TokenSet {
		t.Error("token credentials should report token_set=true")
	}

	current := set
	current = applyCredentialsUpdate(TypeGitHubRelease, current, &CredentialsUpdate{
		GitHubRelease: &GitHubReleaseCredentialsUpdate{Token: strPtrVal("")},
	})
	if current.GitHubRelease.TokenSet {
		t.Error("empty token update should clear token_set")
	}
	current = applyCredentialsUpdate(TypeGitHubRelease, current, &CredentialsUpdate{
		GitHubRelease: &GitHubReleaseCredentialsUpdate{Token: strPtrVal("new")},
	})
	if !current.GitHubRelease.TokenSet {
		t.Error("non-empty token update should set token_set")
	}
	kept := applyCredentialsUpdate(TypeGitHubRelease, current, &CredentialsUpdate{
		GitHubRelease: &GitHubReleaseCredentialsUpdate{},
	})
	if !kept.GitHubRelease.TokenSet {
		t.Error("nil token update should keep token_set")
	}
	// 非 github_release 组的更新不影响状态。
	kept = applyCredentialsUpdate(TypeGitHubRelease, kept, &CredentialsUpdate{
		WebDAV: &WebDAVCredentialsUpdate{Password: strPtrVal("x")},
	})
	if !kept.GitHubRelease.TokenSet {
		t.Error("webdav-group update should not touch github_release state")
	}
}

// TestValidateCredentialsUpdateGitHubRelease 校验更新组选择：仅允许
// github_release 组。
func TestValidateCredentialsUpdateGitHubRelease(t *testing.T) {
	if err := ValidateCredentialsUpdate(TypeGitHubRelease, &CredentialsUpdate{
		GitHubRelease: &GitHubReleaseCredentialsUpdate{},
	}); err != nil {
		t.Errorf("github_release group = %v, want nil", err)
	}
	for name, update := range map[string]CredentialsUpdate{
		"webdav group": {WebDAV: &WebDAVCredentialsUpdate{}},
		"s3 group":     {S3: &S3CredentialsUpdate{}},
		"sftp group":   {SFTP: &SFTPCredentialsUpdate{}},
	} {
		if err := ValidateCredentialsUpdate(TypeGitHubRelease, &update); err == nil {
			t.Errorf("%s = nil, want error", name)
		}
	}
}

// TestRemoteIdentityEqualGitHubRelease 校验身份字段：版本选择范围
// （repository / policy / tag / recent_count / prereleases）是身份，
// verify_sha256 与 token 不是。
func TestRemoteIdentityEqualGitHubRelease(t *testing.T) {
	base := Source{Type: TypeGitHubRelease, Config: Config{GitHubRelease: ptrGH(validGitHubReleaseConfig())}}
	if !RemoteIdentityEqual(base, base) {
		t.Error("self != self")
	}
	cases := []struct {
		name string
		cfg  GitHubReleaseConfig
		want bool
	}{
		{"same", validGitHubReleaseConfig(), true},
		{"repository", ghWith(func(c *GitHubReleaseConfig) { c.Repository = "other/other" }), false},
		{"policy", ghWith(func(c *GitHubReleaseConfig) { c.ReleasePolicy = ReleaseAll }), false},
		{"tag", ghWith(func(c *GitHubReleaseConfig) { c.ReleasePolicy = ReleaseTag; c.Tag = "v2" }), false},
		{"recent count", ghWith(func(c *GitHubReleaseConfig) { c.ReleasePolicy = ReleaseRecent; c.RecentCount = 5 }), false},
		{"prereleases", ghWith(func(c *GitHubReleaseConfig) { c.IncludePrereleases = true }), false},
		// verify_sha256 只影响校验强度，不改变远端身份与删除范围。
		{"verify_sha256 only", ghWith(func(c *GitHubReleaseConfig) { c.VerifySHA256 = SHA256Required }), true},
	}
	for _, tc := range cases {
		other := Source{Type: TypeGitHubRelease, Config: Config{GitHubRelease: ptrGH(tc.cfg)}}
		if got := RemoteIdentityEqual(base, other); got != tc.want {
			t.Errorf("github_release %s = %v, want %v", tc.name, got, tc.want)
		}
	}
	// verify_sha256 不属于身份，但要保证两种取值持久化形态都被接受。
	if err := ValidateConfig(TypeGitHubRelease, Config{GitHubRelease: ptrGH(ghWith(func(c *GitHubReleaseConfig) {
		c.VerifySHA256 = SHA256Required
	}))}); err != nil {
		t.Errorf("required verify_sha256 = %v, want nil", err)
	}
}

// strPtrVal 返回字符串指针。
func strPtrVal(s string) *string {
	return &s
}

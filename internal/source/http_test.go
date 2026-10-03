package source

import (
	"strings"
	"testing"
)

// validHTTPConfig 构造 canonical form 的 HTTP 配置。
func validHTTPConfig() HTTPConfig {
	return HTTPConfig{
		BaseURL:        "https://mirror.example.com/releases/",
		ListingMode:    HTTPListingAuto,
		AuthMethod:     HTTPAuthNone,
		CaddyFileLimit: DefaultCaddyFileLimit,
	}
}

// httpWith 便捷构造变体。
func httpWith(mutate func(*HTTPConfig)) HTTPConfig {
	c := validHTTPConfig()
	mutate(&c)
	return c
}

// Normalized 把 BaseURL 归一为补齐尾 / 的 clean http(s) URL（scheme /
// host 小写），listing_mode / auth_method / caddy_file_limit 取默认，
// none / bearer 下 username 清空；原始输入不被修改，已归一形态保持
// 稳定。
func TestConfigNormalizedHTTP(t *testing.T) {
	raw := HTTPConfig{
		BaseURL:     "  HTTPS://Mirror.Example.com/Releases  ",
		ListingMode: "",
		AuthMethod:  "",
		Username:    "stale",
	}
	got := Config{HTTP: &raw}.Normalized(TypeHTTP).HTTP
	if got == nil {
		t.Fatal("normalized http config = nil")
	}
	want := HTTPConfig{
		BaseURL:        "https://mirror.example.com/Releases/",
		ListingMode:    HTTPListingAuto,
		AuthMethod:     HTTPAuthNone,
		CaddyFileLimit: DefaultCaddyFileLimit,
	}
	if *got != want {
		t.Errorf("normalized http = %+v, want %+v", *got, want)
	}
	if raw.ListingMode != "" || raw.AuthMethod != "" || raw.Username != "stale" {
		t.Errorf("original config mutated: %+v", raw)
	}

	// 根路径两种写法归一为 /；basic 保留 username。
	root := Config{HTTP: &HTTPConfig{BaseURL: "http://mirror.example.com", AuthMethod: HTTPAuthBasic, Username: "tinysync"}}.Normalized(TypeHTTP).HTTP
	if root.BaseURL != "http://mirror.example.com/" {
		t.Errorf("root BaseURL = %q, want trailing slash", root.BaseURL)
	}
	if root.Username != "tinysync" {
		t.Errorf("basic username = %q, want kept", root.Username)
	}
	// bearer 下 username 清空。
	bearer := Config{HTTP: &HTTPConfig{BaseURL: "http://mirror.example.com/", AuthMethod: HTTPAuthBearer, Username: "stale"}}.Normalized(TypeHTTP).HTTP
	if bearer.Username != "" {
		t.Errorf("bearer username = %q, want cleared", bearer.Username)
	}

	// 不合法形态原样返回（校验层拒绝），不被归一化吞掉。
	bad := Config{HTTP: &HTTPConfig{BaseURL: "https://mirror.example.com/files/?token=x"}}.Normalized(TypeHTTP).HTTP
	if bad.BaseURL != "https://mirror.example.com/files/?token=x" {
		t.Errorf("query base_url normalized to %q, want untouched", bad.BaseURL)
	}

	// 已归一形态保持不变；显式 caddy_file_limit 不被覆盖回默认。
	canonical := validHTTPConfig()
	canonical.AuthMethod = HTTPAuthBasic
	canonical.Username = "tinysync"
	canonical.CaddyFileLimit = 100000
	if got := (Config{HTTP: &canonical}).Normalized(TypeHTTP).HTTP; *got != canonical {
		t.Errorf("canonical form changed by Normalized: %+v", *got)
	}
}

// validateHTTPConfig 覆盖 ADR 0009 canonical form 表：scheme / host /
// userinfo / query / fragment / percent-encoded separator / clean path /
// 枚举取值 / basic 的 username 必填 / 非负 file_limit。
func TestValidateHTTPConfig(t *testing.T) {
	valid := []struct {
		name string
		cfg  HTTPConfig
	}{
		{"canonical", validHTTPConfig()},
		{"no trailing slash", httpWith(func(c *HTTPConfig) { c.BaseURL = "https://mirror.example.com/releases" })},
		{"root only", httpWith(func(c *HTTPConfig) { c.BaseURL = "https://mirror.example.com/" })},
		{"http scheme", httpWith(func(c *HTTPConfig) { c.BaseURL = "http://mirror.example.com/" })},
		{"port", httpWith(func(c *HTTPConfig) { c.BaseURL = "https://mirror.example.com:8443/releases/" })},
		{"explicit profiles", httpWith(func(c *HTTPConfig) { c.ListingMode = HTTPListingNginx })},
		{"basic with username", httpWith(func(c *HTTPConfig) {
			c.AuthMethod = HTTPAuthBasic
			c.Username = "tinysync"
		})},
		{"bearer", httpWith(func(c *HTTPConfig) { c.AuthMethod = HTTPAuthBearer })},
		{"empty enums (pre-normalized)", httpWith(func(c *HTTPConfig) { c.ListingMode = ""; c.AuthMethod = "" })},
	}
	for _, tc := range valid {
		if err := validateHTTPConfig(tc.cfg); err != nil {
			t.Errorf("%s: unexpected error: %v", tc.name, err)
		}
	}

	invalid := []struct {
		name string
		cfg  HTTPConfig
		want string
	}{
		{"empty base_url", httpWith(func(c *HTTPConfig) { c.BaseURL = "" }), "scheme"},
		{"ftp scheme", httpWith(func(c *HTTPConfig) { c.BaseURL = "ftp://mirror.example.com/" }), "scheme"},
		{"no host", httpWith(func(c *HTTPConfig) { c.BaseURL = "https:///releases/" }), "host is required"},
		{"userinfo", httpWith(func(c *HTTPConfig) { c.BaseURL = "https://user:pass@mirror.example.com/" }), "must not embed credentials"},
		{"query", httpWith(func(c *HTTPConfig) { c.BaseURL = "https://mirror.example.com/files/?token=x" }), "query"},
		{"sort query", httpWith(func(c *HTTPConfig) { c.BaseURL = "https://mirror.example.com/files/?sort=name" }), "query"},
		{"fragment", httpWith(func(c *HTTPConfig) { c.BaseURL = "https://mirror.example.com/files/#top" }), "fragment"},
		{"encoded slash", httpWith(func(c *HTTPConfig) { c.BaseURL = "https://mirror.example.com/a%2Fb/" }), "standard URL encoding"},
		{"encoded backslash", httpWith(func(c *HTTPConfig) { c.BaseURL = "https://mirror.example.com/a%5Cb/" }), "path separators or NUL"},
		{"dot segment", httpWith(func(c *HTTPConfig) { c.BaseURL = "https://mirror.example.com/a/../b/" }), "clean absolute path"},
		{"double slash", httpWith(func(c *HTTPConfig) { c.BaseURL = "https://mirror.example.com/a//b/" }), "clean absolute path"},
		{"bad listing mode", httpWith(func(c *HTTPConfig) { c.ListingMode = "apache" }), "listing_mode"},
		{"bad auth method", httpWith(func(c *HTTPConfig) { c.AuthMethod = "digest" }), "auth_method"},
		{"basic without username", httpWith(func(c *HTTPConfig) {
			c.AuthMethod = HTTPAuthBasic
			c.Username = ""
		}), "username is required for auth_method=basic"},
		{"negative file limit", httpWith(func(c *HTTPConfig) { c.CaddyFileLimit = -1 }), "caddy_file_limit"},
	}
	for _, tc := range invalid {
		err := validateHTTPConfig(tc.cfg)
		if err == nil {
			t.Errorf("%s: expected error", tc.name)
			continue
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: error = %v, want containing %q", tc.name, err, tc.want)
		}
	}
}

// ValidateCredentials 与显式 AuthMethod 严格匹配：none 不接受任何
// secret、basic 要求 password、bearer 要求 bearer token。
func TestValidateCredentialsHTTP(t *testing.T) {
	cases := []struct {
		name    string
		auth    HTTPAuthMethod
		creds   Credentials
		wantErr string
	}{
		{"none empty", HTTPAuthNone, Credentials{}, ""},
		{"none nil group", HTTPAuthNone, Credentials{HTTP: &HTTPCredentials{}}, ""},
		{"none with password", HTTPAuthNone, Credentials{HTTP: &HTTPCredentials{Password: "x"}}, "does not accept credentials"},
		{"none with token", HTTPAuthNone, Credentials{HTTP: &HTTPCredentials{BearerToken: "x"}}, "does not accept credentials"},
		{"basic ok", HTTPAuthBasic, Credentials{HTTP: &HTTPCredentials{Password: "pw"}}, ""},
		{"basic missing", HTTPAuthBasic, Credentials{}, "password is required"},
		{"basic empty", HTTPAuthBasic, Credentials{HTTP: &HTTPCredentials{}}, "password is required"},
		{"bearer ok", HTTPAuthBearer, Credentials{HTTP: &HTTPCredentials{BearerToken: "tok"}}, ""},
		{"bearer missing", HTTPAuthBearer, Credentials{}, "bearer token is required"},
		{"foreign group", HTTPAuthNone, Credentials{SMB: &SMBCredentials{Password: "x"}}, "must only contain http fields"},
	}
	for _, tc := range cases {
		cfg := Config{HTTP: &HTTPConfig{
			BaseURL:     "https://mirror.example.com/",
			AuthMethod:  tc.auth,
			ListingMode: HTTPListingAuto,
		}}
		if tc.auth == HTTPAuthBasic {
			cfg.HTTP.Username = "tinysync"
		}
		err := ValidateCredentials(TypeHTTP, cfg, tc.creds)
		if tc.wantErr == "" {
			if err != nil {
				t.Errorf("%s: unexpected error: %v", tc.name, err)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
			t.Errorf("%s: error = %v, want containing %q", tc.name, err, tc.wantErr)
		}
	}
}

// CredentialStateOf 按 password / bearer_token 推导回显状态。
func TestCredentialStateOfHTTP(t *testing.T) {
	empty := CredentialStateOf(TypeHTTP, Credentials{})
	if empty.HTTP == nil || empty.HTTP.PasswordSet || empty.HTTP.BearerTokenSet {
		t.Errorf("empty state = %+v, want all false", empty.HTTP)
	}
	both := CredentialStateOf(TypeHTTP, Credentials{HTTP: &HTTPCredentials{Password: "pw", BearerToken: "tok"}})
	if both.HTTP == nil || !both.HTTP.PasswordSet || !both.HTTP.BearerTokenSet {
		t.Errorf("state = %+v, want both set", both.HTTP)
	}
}

// ValidateCreateInput 拒绝 HTTP 与其它协议组的混入（严格单选）。
func TestValidateCreateInputRejectsMixedHTTPConfig(t *testing.T) {
	err := ValidateCreateInput(CreateInput{
		Name: "mixed",
		Type: TypeHTTP,
		Config: Config{
			HTTP:   &HTTPConfig{BaseURL: "https://mirror.example.com/"},
			WebDAV: &WebDAVConfig{Endpoint: "https://example.com/dav"},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "must only contain") {
		t.Errorf("mixed config error = %v, want union rejection", err)
	}
	// 缺失自身组同样拒绝。
	err = ValidateCreateInput(CreateInput{
		Name:   "missing",
		Type:   TypeHTTP,
		Config: Config{},
	})
	if err == nil || !strings.Contains(err.Error(), "http config is required") {
		t.Errorf("missing config error = %v, want required rejection", err)
	}
}

// PrepareConfig 产出 canonical form（含 BaseURL 归一），持久化前形态
// 与身份比较稳定。
func TestPrepareConfigHTTP(t *testing.T) {
	got, err := PrepareConfig(TypeHTTP, Config{HTTP: &HTTPConfig{
		BaseURL: "  HTTPS://Mirror.Example.com/Releases ",
	}})
	if err != nil {
		t.Fatalf("PrepareConfig: %v", err)
	}
	want := HTTPConfig{
		BaseURL:        "https://mirror.example.com/Releases/",
		ListingMode:    HTTPListingAuto,
		AuthMethod:     HTTPAuthNone,
		CaddyFileLimit: DefaultCaddyFileLimit,
	}
	if *got.HTTP != want {
		t.Errorf("prepared = %+v, want %+v", *got.HTTP, want)
	}
}

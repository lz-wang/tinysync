package mcp

import (
	"encoding/json"
	"strings"
	"testing"

	"tinysync/internal/source"
)

// toSourceSummary 对 HTTP Source 的转换：config 走 HTTP 组（非敏感
// 字段序列化），credential_state 回显 password_set / bearer_token_set；
// secret 明文绝不进入 summary（结构体本身不承载，这里锁定 wire JSON）。
func TestToSourceSummaryHTTP(t *testing.T) {
	s := source.Source{
		ID:   "src_mcp_http",
		Name: "MCP HTTP",
		Type: source.TypeHTTP,
		Config: source.Config{HTTP: &source.HTTPConfig{
			BaseURL:        "https://mirror.example.com/releases/",
			ListingMode:    source.HTTPListingCaddy,
			AuthMethod:     source.HTTPAuthBasic,
			Username:       "tinysync",
			CaddyFileLimit: source.DefaultCaddyFileLimit,
		}},
		CredentialState: source.CredentialState{
			HTTP: &source.HTTPCredentialState{PasswordSet: true, BearerTokenSet: false},
		},
		Enabled: true,
	}
	summary, err := toSourceSummary(s)
	if err != nil {
		t.Fatalf("toSourceSummary: %v", err)
	}
	if summary.Type != "http" {
		t.Errorf("type = %q, want http", summary.Type)
	}
	configJSON, err := json.Marshal(summary.Config)
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}
	for _, want := range []string{
		"https://mirror.example.com/releases/", `"caddy"`, `"basic"`, "tinysync",
	} {
		if !strings.Contains(string(configJSON), want) {
			t.Errorf("http config JSON %s missing %s", configJSON, want)
		}
	}
	if strings.Contains(string(configJSON), "password") || strings.Contains(string(configJSON), "bearer_token") {
		t.Errorf("http config JSON leaks secret keys: %s", configJSON)
	}
	if summary.CredentialState.HTTP == nil || !summary.CredentialState.HTTP.PasswordSet {
		t.Errorf("credential_state = %+v, want http password_set", summary.CredentialState)
	}
}

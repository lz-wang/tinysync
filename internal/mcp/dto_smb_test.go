package mcp

import (
	"encoding/json"
	"strings"
	"testing"

	"tinysync/internal/source"
)

// toSourceSummary 对 SMB Source 的转换：config 走 SMB 组（非敏感字段
// 序列化），credential_state 回显 password_set；secret 明文绝不进入
// summary（结构体本身不承载 password，这里锁定 wire JSON 形态）。
func TestToSourceSummarySMB(t *testing.T) {
	s := source.Source{
		ID:   "src_mcp_smb",
		Name: "MCP SMB",
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
		CredentialState: source.CredentialState{
			SMB: &source.SMBCredentialState{PasswordSet: true},
		},
		Enabled: true,
	}
	summary, err := toSourceSummary(s)
	if err != nil {
		t.Fatalf("toSourceSummary: %v", err)
	}
	if summary.Type != "smb" {
		t.Errorf("type = %q, want smb", summary.Type)
	}
	configJSON, err := json.Marshal(summary.Config)
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}
	for _, want := range []string{"nas.example.com", `"backup"`, "/photos", "WORKGROUP", `"required"`} {
		if !strings.Contains(string(configJSON), want) {
			t.Errorf("smb config JSON %s missing %s", configJSON, want)
		}
	}
	if strings.Contains(string(configJSON), "password") {
		t.Errorf("smb config JSON leaks password key: %s", configJSON)
	}
	if summary.CredentialState.SMB == nil || !summary.CredentialState.SMB.PasswordSet {
		t.Errorf("credential_state = %+v, want smb password_set", summary.CredentialState)
	}
}

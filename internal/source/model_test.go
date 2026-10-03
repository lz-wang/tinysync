package source

import (
	"strings"
	"testing"
)

// NewID 生成 src_ 前缀、128-bit hex 的唯一 ID。
func TestNewID(t *testing.T) {
	const iterations = 1000
	seen := make(map[string]bool, iterations)
	for range iterations {
		id, err := NewID()
		if err != nil {
			t.Fatalf("NewID: %v", err)
		}
		if !strings.HasPrefix(id, idPrefix) {
			t.Fatalf("id %q missing prefix %q", id, idPrefix)
		}
		hexPart := strings.TrimPrefix(id, idPrefix)
		if len(hexPart) != newIDSize*2 {
			t.Fatalf("id %q hex length = %d, want %d", id, len(hexPart), newIDSize*2)
		}
		if seen[id] {
			t.Fatalf("duplicate id %q in %d iterations", id, iterations)
		}
		seen[id] = true
	}
}

// SMB 配置归一化为 canonical form：port 零值取 445、remote_root
// 空串取 "/"、signing 空值取 required、host / share / username /
// domain 去首尾空白；原配置不被修改（返回副本）。
func TestConfigNormalizedSMB(t *testing.T) {
	raw := SMBConfig{
		Host:       "  nas.example.com  ",
		Port:       0,
		Share:      " backup ",
		RemoteRoot: "",
		Username:   " tinysync ",
		Domain:     " WORKGROUP ",
		Signing:    "",
	}
	got := Config{SMB: &raw}.Normalized(TypeSMB).SMB
	if got == nil {
		t.Fatal("normalized SMB config = nil")
	}
	want := SMBConfig{
		Host:       "nas.example.com",
		Port:       445,
		Share:      "backup",
		RemoteRoot: "/",
		Username:   "tinysync",
		Domain:     "WORKGROUP",
		Signing:    SMBSigningRequired,
	}
	if *got != want {
		t.Errorf("normalized SMB = %+v, want %+v", *got, want)
	}
	if raw.Port != 0 || raw.RemoteRoot != "" || raw.Signing != "" {
		t.Errorf("original config mutated: %+v", raw)
	}

	// 已归一形态保持不变；显式 auto signing 不被覆盖回 required。
	canonical := want
	canonical.Signing = SMBSigningAuto
	if got := (Config{SMB: &canonical}).Normalized(TypeSMB).SMB; *got != canonical {
		t.Errorf("canonical form changed by Normalized: %+v", *got)
	}
}

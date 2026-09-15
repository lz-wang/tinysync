package syncjob

import (
	"strings"
	"testing"
)

// Mode 只接受 copy / mirror 枚举值。
func TestModeValid(t *testing.T) {
	if !ModeCopy.Valid() || !ModeMirror.Valid() {
		t.Error("copy/mirror should be valid modes")
	}
	for _, invalid := range []Mode{"", "sync", "COPY", "mirror "} {
		if invalid.Valid() {
			t.Errorf("mode %q should be invalid", invalid)
		}
	}
}

// NewID 生成 job_ 前缀、128-bit hex、不重复的 ID。
func TestNewID(t *testing.T) {
	seen := make(map[string]bool)
	for range 100 {
		id, err := NewID()
		if err != nil {
			t.Fatalf("NewID: %v", err)
		}
		if !strings.HasPrefix(id, "job_") {
			t.Fatalf("id = %q, want job_ prefix", id)
		}
		if len(id) != len("job_")+2*newIDSize {
			t.Fatalf("id = %q, want %d hex chars after prefix", id, 2*newIDSize)
		}
		if seen[id] {
			t.Fatalf("duplicate id %q", id)
		}
		seen[id] = true
	}
}

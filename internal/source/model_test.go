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

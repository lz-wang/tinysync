package mcp

import (
	"encoding/json"
	"testing"

	"tinysync/internal/source"
)

func TestToSourceSummaryLocal(t *testing.T) {
	summary, err := toSourceSummary(source.Source{ID: "src_local", Type: source.TypeLocal, Config: source.Config{Local: &source.LocalConfig{Root: "/data/photos"}}, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(summary)
	if err != nil {
		t.Fatal(err)
	}
	var wire struct {
		Type   string             `json:"type"`
		Config source.LocalConfig `json:"config"`
		State  map[string]bool    `json:"credential_state"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatal(err)
	}
	if wire.Type != "local" || wire.Config.Root != "/data/photos" || len(wire.State) != 0 {
		t.Fatalf("summary=%s", raw)
	}
}

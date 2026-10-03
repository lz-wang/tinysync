package http

import (
	"strings"
	"testing"

	"tinysync/internal/source"
)

// caddyJSONFixture 是 Caddy file_server browse 在
// Accept: application/json 下的输出形态。
const caddyJSONFixture = `[
{"name":"linux","size":4096,"url":"/releases/linux/","mod_time":"2026-10-01T12:00:00Z","mode":2147484141,"is_dir":true,"is_symlink":false},
{"name":"foo.tar.gz","size":123456789,"url":"foo.tar.gz","mod_time":"2026-10-02T08:30:00Z","mode":420,"is_dir":false,"is_symlink":false},
{"name":"empty.txt","size":0,"url":"empty.txt","mod_time":"2026-10-02T08:30:00Z","mode":420,"is_dir":false,"is_symlink":false}
]`

// Caddy JSON：精确 size / RFC3339 mod_time / 目录形态；目录不携带
// 精确 size（Fingerprint 只对文件有意义）。
func TestParseCaddyJSON(t *testing.T) {
	m := newRootMapper(t)
	entries, err := parseCaddyJSON(m, "/", []byte(caddyJSONFixture))
	if err != nil {
		t.Fatalf("parseCaddyJSON: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("entries = %d, want 3", len(entries))
	}
	if entries[0].Name != "linux" || !entries[0].IsDir || entries[0].SizeKnown {
		t.Errorf("entry 0 = %+v, want dir linux without exact size", entries[0])
	}
	if entries[1].Name != "foo.tar.gz" || entries[1].IsDir || !entries[1].SizeKnown || entries[1].Size != 123456789 {
		t.Errorf("entry 1 = %+v, want file size 123456789", entries[1])
	}
	if entries[1].ModifiedAt.Year() != 2026 {
		t.Errorf("mod_time = %v, want parsed RFC3339", entries[1].ModifiedAt)
	}
	if entries[2].Size != 0 || !entries[2].SizeKnown {
		t.Errorf("entry 2 = %+v, want zero-byte with known size", entries[2])
	}
}

// 顶层 JSON object（含 {items|files} 包装形态）不是合法的 Caddy
// listing：字段缺失与空数组不可区分，绝不解释成零条目快照。
func TestParseCaddyJSONRejectsWrappedObject(t *testing.T) {
	m := newRootMapper(t)
	for _, body := range []string{
		`{}`,
		`{"status":"ok"}`,
		`{"items":null}`,
		`{"files":null}`,
		`{"items":[]}`,
		`{"items":"bad"}`,
		`{"items":[{"name":"a","size":1,"url":"a","mod_time":"2026-10-01T12:00:00Z","is_dir":false,"is_symlink":false}]}`,
	} {
		_, err := parseCaddyJSON(m, "/", []byte(body))
		if err == nil {
			t.Errorf("wrapped object %s accepted, want invalid caddy JSON", body)
			continue
		}
		if !strings.Contains(err.Error(), "invalid caddy JSON") {
			t.Errorf("wrapped object %s error = %v, want invalid caddy JSON", body, err)
		}
	}
}

// 可识别 symlink 一律 fail-closed（permanent）：跟随破坏 root
// confinement，跳过破坏 Mirror 删除安全。
func TestParseCaddyJSONSymlinkFailClosed(t *testing.T) {
	m := newRootMapper(t)
	body := `[{"name":"link","size":3,"url":"link","mod_time":"2026-10-01T12:00:00Z","is_dir":false,"is_symlink":true}]`
	_, err := parseCaddyJSON(m, "/", []byte(body))
	if err == nil {
		t.Fatal("symlink accepted, want fail-closed")
	}
	if !strings.Contains(err.Error(), "symlink") {
		t.Errorf("error = %v, want symlink detail", err)
	}
	if source.IsRetryable(err) {
		t.Error("symlink rejection should be permanent")
	}
}

// Caddy JSON 的非法形态：非 JSON、重复条目、跨层级名。
func TestParseCaddyJSONMalformed(t *testing.T) {
	m := newRootMapper(t)
	cases := []struct {
		name string
		body string
		want string
	}{
		{"invalid json", `[{"name":`, "invalid caddy JSON"},
		{"duplicate", `[{"name":"a","is_dir":false,"is_symlink":false},{"name":"a","is_dir":true,"is_symlink":false}]`, "duplicate entry"},
		{"nested name", `[{"name":"a/b","is_dir":false,"is_symlink":false}]`, "not a single clean path segment"},
		{"negative size", `[{"name":"a","size":-1,"url":"a","mod_time":"2026-10-01T12:00:00Z","is_dir":false,"is_symlink":false}]`, "negative size"},
	}
	for _, tc := range cases {
		_, err := parseCaddyJSON(m, "/", []byte(tc.body))
		if err == nil {
			t.Errorf("%s: expected error", tc.name)
			continue
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: error = %v, want containing %q", tc.name, err, tc.want)
		}
	}
}

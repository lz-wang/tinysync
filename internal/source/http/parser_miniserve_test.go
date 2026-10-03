package http

import (
	"strings"
	"testing"
)

// newRootMapper 构造指向固定 BaseURL 的 mapper（parser 测试用）。
func newRootMapper(t *testing.T) *mapper {
	t.Helper()
	m, err := newMapper("https://mirror.example.com/releases/")
	if err != nil {
		t.Fatalf("newMapper: %v", err)
	}
	return m
}

// miniserveHTMLFixture 是 miniserve ?raw=true 的简化 HTML 表。
const miniserveHTMLFixture = `<!DOCTYPE html>
<html>
<head><meta charset="utf-8"><title>Directory: /releases/</title></head>
<body>
<table>
<thead><tr><th>Name</th><th>Size</th><th>Modified</th></tr></thead>
<tbody>
<tr class="entry-type-directory"><td><a class="directory" href="../">../</a></td><td>-</td><td>2026-10-01 12:00</td></tr>
<tr class="entry-type-directory"><td><a class="directory" href="linux/">linux/</a></td><td>-</td><td>2026-10-01 12:00</td></tr>
<tr class="entry-type-file"><td><a class="file" href="foo.tar.gz">foo.tar.gz</a></td><td>118 MB</td><td>2026-10-02 08:30</td></tr>
<tr class="entry-type-file"><td><a class="file" href="empty.txt">empty.txt</a></td><td>0 B</td><td>2026-10-02 08:30</td></tr>
<tr class="entry-type-file"><td><a class="file" href="name%20with%20space.txt">name with space.txt</a></td><td>4 B</td><td>2026-10-02 08:30</td></tr>
</tbody>
</table>
</body>
</html>`

// miniserve：只从 DOM 取 name / isDir；页面大小（人类可读）不进入
// metadata。
func TestParseMiniserveHTML(t *testing.T) {
	m := newRootMapper(t)
	entries, err := parseMiniserveHTML(m, "/", []byte(miniserveHTMLFixture))
	if err != nil {
		t.Fatalf("parseMiniserveHTML: %v", err)
	}
	want := map[string]bool{
		"linux":               true,
		"foo.tar.gz":          false,
		"empty.txt":           false,
		"name with space.txt": false,
	}
	if len(entries) != len(want) {
		t.Fatalf("entries = %+v, want %d entries", entries, len(want))
	}
	for _, e := range entries {
		isDir, ok := want[e.Name]
		if !ok {
			t.Errorf("unexpected entry %q", e.Name)
			continue
		}
		if e.IsDir != isDir {
			t.Errorf("entry %q IsDir = %v, want %v", e.Name, e.IsDir, isDir)
		}
		if e.SizeKnown {
			t.Errorf("entry %q must not claim exact size from HTML page", e.Name)
		}
	}
}

// 无 entry-type-* 行标记时按锚点 class / 尾斜杠推断（兼容 miniserve
// 版本差异）。
func TestParseMiniserveHTMLFallbackMarkers(t *testing.T) {
	m := newRootMapper(t)
	body := `<html><body><table>
<tr><td><a class="directory" href="d/">d/</a></td></tr>
<tr><td><a class="file" href="f.txt">f.txt</a></td></tr>
</table></body></html>`
	entries, err := parseMiniserveHTML(m, "/", []byte(body))
	if err != nil {
		t.Fatalf("parseMiniserveHTML fallback: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("entries = %+v, want 2", entries)
	}
	if !entries[0].IsDir || entries[1].IsDir {
		t.Errorf("dir detection = %+v", entries)
	}
}

// miniserve 安全矩阵：越子树、编码分隔符、外部源、无条目行。
func TestParseMiniserveHTMLSecurity(t *testing.T) {
	m := newRootMapper(t)
	cases := []struct {
		name string
		body string
		want string
	}{
		{"path escape", `<html><body><table><tr class="entry-type-file"><td><a href="../../x">x</a></td></tr></table></body></html>`, "malformed directory listing"},
		{"external absolute", `<html><body><table><tr class="entry-type-file"><td><a href="https://evil.example.com/x">x</a></td></tr></table></body></html>`, "malformed directory listing"},
		{"encoded slash", `<html><body><table><tr class="entry-type-file"><td><a href="a%2Fb">a/b</a></td></tr></table></body></html>`, "malformed directory listing"},
		{"no rows", `<html><body><table><thead><tr><th>Name</th></tr></thead></table></body></html>`, "no entry rows"},
		{"duplicate", `<html><body><table><tr class="entry-type-file"><td><a href="a">a</a></td></tr><tr class="entry-type-file"><td><a href="a">a</a></td></tr></table></body></html>`, "duplicate entry"},
	}
	for _, tc := range cases {
		_, err := parseMiniserveHTML(m, "/", []byte(tc.body))
		if err == nil {
			t.Errorf("%s: expected error", tc.name)
			continue
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: error = %v, want containing %q", tc.name, err, tc.want)
		}
	}
}

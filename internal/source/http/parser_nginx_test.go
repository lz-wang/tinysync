package http

import (
	"strings"
	"testing"

	"tinysync/internal/source"
)

// nginxJSONFixture 是 nginx autoindex_format json 的真实输出形态。
const nginxJSONFixture = `[
{"name":"linux/","type":"directory","mtime":"Wed, 21 Oct 2026 07:28:00 GMT"},
{"name":"foo.tar.gz","type":"file","mtime":"Wed, 21 Oct 2026 07:28:00 GMT","size":123456789},
{"name":"readme.txt","type":"file","mtime":"Wed, 30 Sep 2026 00:11:22 GMT","size":0}
]`

// nginxHTMLFixture 是 nginx 默认 HTML autoindex 的真实输出形态。
const nginxHTMLFixture = `<html>
<head><title>Index of /releases/</title></head>
<body>
<h1>Index of /releases/</h1><hr><pre><a href="../">../</a>
<a href="linux/">linux/</a>                                                  21-Oct-2026 07:28       -
<a href="foo.tar.gz">foo.tar.gz</a>                                  21-Oct-2026 07:28     118M
<a href="empty.txt">empty.txt</a>                                    21-Oct-2026 07:28       0
<a href="name%20with%20space.txt">name with space.txt</a>                  21-Oct-2026 07:28       4
</pre><hr></body>
</html>`

// nginx JSON：条目名剥尾斜杠、目录 / 文件区分、size 精确、mtime 解析。
func TestParseNginxJSON(t *testing.T) {
	m := newRootMapper(t)
	entries, err := parseNginxJSON(m, "/", []byte(nginxJSONFixture))
	if err != nil {
		t.Fatalf("parseNginxJSON: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("entries = %d, want 3", len(entries))
	}
	if entries[0].Name != "linux" || !entries[0].IsDir {
		t.Errorf("entry 0 = %+v, want dir linux", entries[0])
	}
	if entries[0].SizeKnown {
		t.Error("directory entry should not carry exact size")
	}
	if entries[1].Name != "foo.tar.gz" || entries[1].IsDir || !entries[1].SizeKnown || entries[1].Size != 123456789 {
		t.Errorf("entry 1 = %+v, want file foo.tar.gz size 123456789", entries[1])
	}
	if entries[2].Size != 0 || !entries[2].SizeKnown {
		t.Errorf("entry 2 = %+v, want zero-byte file with known size", entries[2])
	}
	if entries[1].ModifiedAt.IsZero() {
		t.Error("mtime not parsed")
	}
}

// nginx JSON 的非法形态：非 JSON、未知 type、重复条目、跨层级名。
func TestParseNginxJSONMalformed(t *testing.T) {
	m := newRootMapper(t)
	cases := []struct {
		name string
		body string
		want string
	}{
		{"invalid json", `{`, "invalid nginx JSON"},
		{"unknown type", `[{"name":"x","type":"fifo","mtime":"Wed, 21 Oct 2026 07:28:00 GMT"}]`, "unknown type"},
		{"duplicate", `[{"name":"a","type":"file","mtime":"Wed, 21 Oct 2026 07:28:00 GMT","size":1},{"name":"a","type":"file","mtime":"Wed, 21 Oct 2026 07:28:00 GMT","size":2}]`, "duplicate entry"},
		{"file dir collision", `[{"name":"a","type":"file","mtime":"Wed, 21 Oct 2026 07:28:00 GMT","size":1},{"name":"a/","type":"directory","mtime":"Wed, 21 Oct 2026 07:28:00 GMT"}]`, "duplicate entry"},
		{"nested name", `[{"name":"a/b","type":"file","mtime":"Wed, 21 Oct 2026 07:28:00 GMT","size":1}]`, "not a single clean path segment"},
		{"dot segment", `[{"name":"..","type":"directory","mtime":"Wed, 21 Oct 2026 07:28:00 GMT"}]`, "not a single clean path segment"},
		{"negative size", `[{"name":"x","type":"file","mtime":"Wed, 21 Oct 2026 07:28:00 GMT","size":-1}]`, "negative size"},
	}
	for _, tc := range cases {
		_, err := parseNginxJSON(m, "/", []byte(tc.body))
		if err == nil {
			t.Errorf("%s: expected error", tc.name)
			continue
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: error = %v, want containing %q", tc.name, err, tc.want)
		}
		if source.IsRetryable(err) {
			t.Errorf("%s: malformed listing should be permanent", tc.name)
		}
	}
}

// nginx HTML：href 解析条目（尾斜杠 = 目录），显示的大小 / 日期不
// 进入 metadata（SizeKnown 恒 false，由 HEAD 补全）。
func TestParseNginxHTML(t *testing.T) {
	m := newRootMapper(t)
	entries, err := parseNginxHTML(m, "/", []byte(nginxHTMLFixture))
	if err != nil {
		t.Fatalf("parseNginxHTML: %v", err)
	}
	want := map[string]bool{
		"linux":               true,
		"foo.tar.gz":          false,
		"empty.txt":           false,
		"name with space.txt": false,
	}
	if len(entries) != len(want) {
		t.Fatalf("entries = %+v, want %d", entries, len(want))
	}
	seen := map[string]bool{}
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
		seen[e.Name] = true
	}
	for name := range want {
		if !seen[name] {
			t.Errorf("entry %q missing", name)
		}
	}
}

// nginx HTML 的安全矩阵：越子树 / 跨源 / 编码分隔符 / 无 <pre>。
func TestParseNginxHTMLSecurity(t *testing.T) {
	m := newRootMapper(t)
	cases := []struct {
		name string
		body string
		want string
	}{
		{"parent at root", `<html><head><title>Index of /releases/</title></head><body><pre><a href="../">../</a><a href="d/">d/</a></pre></body></html>`, ""},
		{"path escape", `<html><head><title>Index of /</title></head><body><pre><a href="../../etc/passwd">passwd</a></pre></body></html>`, "malformed directory listing"},
		{"absolute external", `<html><head><title>Index of /</title></head><body><pre><a href="https://evil.example.com/x">x</a></pre></body></html>`, "malformed directory listing"},
		{"encoded slash", `<html><head><title>Index of /</title></head><body><pre><a href="a%2Fb.txt">a/b</a></pre></body></html>`, "malformed directory listing"},
		{"no pre", `<html><head><title>Index of /</title></head><body><p>nothing</p></body></html>`, "no <pre> listing"},
		{"duplicate", `<html><head><title>Index of /</title></head><body><pre><a href="a">a</a><a href="a">a</a></pre></body></html>`, "duplicate entry"},
	}
	for _, tc := range cases {
		_, err := parseNginxHTML(m, "/", []byte(tc.body))
		if tc.want == "" {
			if err != nil {
				t.Errorf("%s: unexpected error %v", tc.name, err)
			}
			continue
		}
		if err == nil {
			t.Errorf("%s: expected error", tc.name)
			continue
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: error = %v, want containing %q", tc.name, err, tc.want)
		}
	}
}

// 子目录 listing：href 相对父目录解析。
func TestParseNginxHTMLSubdir(t *testing.T) {
	m := newRootMapper(t)
	body := `<html><head><title>Index of /releases/linux/</title></head><body><pre><a href="../">../</a>
<a href="x86_64/">x86_64/</a>
<a href="SHA256SUMS">SHA256SUMS</a>
</pre></body></html>`
	entries, err := parseNginxHTML(m, "/linux", []byte(body))
	if err != nil {
		t.Fatalf("parseNginxHTML subdir: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("entries = %+v, want 2", entries)
	}
	for _, e := range entries {
		if e.Name == "x86_64" && !e.IsDir {
			t.Error("x86_64 should be dir")
		}
		if e.Name == "SHA256SUMS" && e.IsDir {
			t.Error("SHA256SUMS should be file")
		}
	}
}

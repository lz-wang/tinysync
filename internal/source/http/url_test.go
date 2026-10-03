package http

import (
	"net/url"
	"strings"
	"testing"
)

// mustParseURL 是测试便捷函数。
func mustParseURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse %q: %v", raw, err)
	}
	return u
}

// BaseURL → logical path 映射：目录恒以 / 结尾、segment 逐个转义、
// 特殊字符（空格 / 非 ASCII / ? # %）不产生额外层级。
func TestMapperURLFor(t *testing.T) {
	m, err := newMapper("https://mirror.example.com/releases/")
	if err != nil {
		t.Fatalf("newMapper: %v", err)
	}
	cases := []struct {
		logical string
		want    string
	}{
		{"/", "https://mirror.example.com/releases/"},
		{"/dir", "https://mirror.example.com/releases/dir/"},
		{"/dir/", "https://mirror.example.com/releases/dir/"},
		{"/dir/sub", "https://mirror.example.com/releases/dir/sub/"},
	}
	for _, tc := range cases {
		if got := m.directoryURL(tc.logical); got != tc.want {
			t.Errorf("directoryURL(%q) = %q, want %q", tc.logical, got, tc.want)
		}
	}
	// 文件 URL 不带尾斜杠；segment 逐个转义，特殊字符不产生额外层级。
	files := []struct {
		logical string
		want    string
	}{
		{"/dir/file.tar.gz", "https://mirror.example.com/releases/dir/file.tar.gz"},
		{"/a b.txt", "https://mirror.example.com/releases/a%20b.txt"},
		{"/名字 +1.txt", "https://mirror.example.com/releases/%E5%90%8D%E5%AD%97%20+1.txt"},
		{"/what?query.txt", "https://mirror.example.com/releases/what%3Fquery.txt"},
		{"/hash#tag.txt", "https://mirror.example.com/releases/hash%23tag.txt"},
		{"/100%.txt", "https://mirror.example.com/releases/100%25.txt"},
	}
	for _, tc := range files {
		if got := m.fileURL(tc.logical); got != tc.want {
			t.Errorf("fileURL(%q) = %q, want %q", tc.logical, got, tc.want)
		}
	}

	// 根 BaseURL（无 path）。
	root, err := newMapper("http://mirror.example.com/")
	if err != nil {
		t.Fatalf("newMapper root: %v", err)
	}
	if got := root.directoryURL("/a/b"); got != "http://mirror.example.com/a/b/" {
		t.Errorf("root directoryURL = %q", got)
	}

	// BaseURL path 本身含特殊字符（canonical 编码形态）时转义后仍是
	// 子树前缀。
	sp, err := newMapper("https://mirror.example.com/my%20files/")
	if err != nil {
		t.Fatalf("newMapper space: %v", err)
	}
	if got := sp.fileURL("/a.txt"); got != "https://mirror.example.com/my%20files/a.txt" {
		t.Errorf("space base fileURL = %q", got)
	}
}

// newMapper 拒绝非 canonical 形态（防御性复检，正常路径已被
// PrepareConfig 拦截）。
func TestNewMapperRejectsNonCanonical(t *testing.T) {
	for _, raw := range []string{
		"ftp://mirror.example.com/",
		"https://user:pass@mirror.example.com/",
		"https://mirror.example.com/files/?token=x",
		"https://mirror.example.com/files/#frag",
		"https://mirror.example.com/a%2Fb/",
		"",
	} {
		if _, err := newMapper(raw); err == nil {
			t.Errorf("newMapper(%q) = nil error, want rejection", raw)
		}
	}
}

// listing href 安全矩阵：合法子树内引用解析为单段条目；../、越子树、
// 编码分隔符、跨层级、绝对外源、反斜杠全部拒绝。
func TestEntryFromHref(t *testing.T) {
	m, err := newMapper("https://mirror.example.com/releases/")
	if err != nil {
		t.Fatalf("newMapper: %v", err)
	}
	valid := []struct {
		dir      string
		href     string
		wantName string
		wantDir  bool
	}{
		{"/", "linux/", "linux", true},
		{"/", "foo.tar.gz", "foo.tar.gz", false},
		{"/", "./foo.tar.gz", "foo.tar.gz", false},
		{"/", "name%20with%20space.txt", "name with space.txt", false},
		{"/", "https://mirror.example.com/releases/linux/", "linux", true},
		// 默认端口的同源绝对 URL（与省略形式语义等价）。
		{"/", "https://mirror.example.com:443/releases/linux/", "linux", true},
	}
	for _, tc := range valid {
		entry, err := m.entryFromHref(tc.dir, tc.href)
		if err != nil {
			t.Errorf("entryFromHref(%q, %q): %v", tc.dir, tc.href, err)
			continue
		}
		if entry.Name != tc.wantName || entry.IsDir != tc.wantDir {
			t.Errorf("entryFromHref(%q, %q) = %+v, want (%q, %v)", tc.dir, tc.href, entry, tc.wantName, tc.wantDir)
		}
	}

	invalid := []struct {
		dir  string
		href string
		want string
	}{
		// "../" 是导航链接：根目录下越出子树，子目录下不是本目录的
		// 单段条目——两种形态都拒绝（解析器会先跳过 "../"）。
		{"/", "../", "escapes source base URL"},
		{"/dir", "../sibling/", "not a child of"},
		// 越子树的绝对 path。
		{"/", "/etc/passwd", "escapes source base URL"},
		{"/dir", "/releases/other/x", "not a child of"},
		// 跨主机 / 降级 scheme / 非默认端口。
		{"/", "https://evil.example.com/releases/linux/", "escapes source base URL"},
		{"/", "http://mirror.example.com/releases/linux/", "escapes source base URL"},
		{"/", "https://mirror.example.com:8443/releases/linux/", "escapes source base URL"},
		// 编码分隔符 / 编码 dot-segment / 反斜杠：canonical path 校验
		// （contains 内完成）即拒绝——这类形态经服务器或反向代理规范
		// 化后可能越出子树。
		{"/", "a%2Fb%2Fc.txt", "escapes source base URL"},
		{"/", "%2e%2e/", "escapes source base URL"},
		{"/", "a\\b.txt", "escapes source base URL"},
		// 自引用。
		{"/", "./", "not a single clean path segment"},
	}
	for _, tc := range invalid {
		_, err := m.entryFromHref(tc.dir, tc.href)
		if err == nil {
			t.Errorf("entryFromHref(%q, %q) = nil error, want rejection", tc.dir, tc.href)
			continue
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("entryFromHref(%q, %q) error = %v, want containing %q", tc.dir, tc.href, err, tc.want)
		}
	}
}

// contains 判定同源 + 子树前缀：scheme / host / port 任一不同即越界。
func TestMapperContains(t *testing.T) {
	m, err := newMapper("https://mirror.example.com:8443/releases/")
	if err != nil {
		t.Fatalf("newMapper: %v", err)
	}
	same := []string{
		"https://mirror.example.com:8443/releases/",
		"https://mirror.example.com:8443/releases/a/b.txt",
	}
	escaped := []string{
		"https://mirror.example.com:8443/",
		"https://mirror.example.com:8443/releases-other/",
		"https://mirror.example.com/releases/",
		"https://evil.example.com:8443/releases/",
		"http://mirror.example.com:8443/releases/",
		// 编码 dot segment（大小写两种十六进制）/ 编码分隔符 / 明文
		// dot segment / 空段：RawPath 非空或 path.Clean 不等价——Go
		// 不折叠编码 dot segment，仅做解码前缀比较会放行，随后被
		// 服务器 / 反向代理规范化到子树之外。
		"https://mirror.example.com:8443/releases/%2e%2e/secret",
		"https://mirror.example.com:8443/releases/%2E%2E/secret",
		"https://mirror.example.com:8443/releases/a%2fb",
		"https://mirror.example.com:8443/releases/a%5cb",
		"https://mirror.example.com:8443/releases/../secret",
		"https://mirror.example.com:8443/releases//x",
	}
	for _, raw := range same {
		u := mustParseURL(t, raw)
		if !m.contains(u) {
			t.Errorf("contains(%q) = false, want true", raw)
		}
	}
	for _, raw := range escaped {
		u := mustParseURL(t, raw)
		if m.contains(u) {
			t.Errorf("contains(%q) = true, want false", raw)
		}
	}
}

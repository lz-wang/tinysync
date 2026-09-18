package filesafe

import (
	"path"
	"path/filepath"
	"strings"
	"testing"
)

// FuzzValidateLogicalPath：被接受的路径必须满足全部基础不变量——
// 绝对、无反斜杠、无 NUL、clean（traversal 不得被归一成合法路径）、
// 除 root 外无尾斜杠；被拒绝的路径只要求不 panic。
func FuzzValidateLogicalPath(f *testing.F) {
	for _, seed := range []string{
		"/", "/a", "/a/b/c.txt", "..", "/..", "/a/../..", "/a/./b",
		"\\", "/a\\b", "/a\x00b", "/a/", "a", "/./a", "//a", "/a//b",
		"/。。/a", "/日本語/ファイル.txt",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, p string) {
		err := ValidateLogicalPath(p)
		if err != nil {
			return
		}
		if strings.ContainsRune(p, '\x00') {
			t.Fatalf("NUL accepted: %q", p)
		}
		if strings.ContainsRune(p, '\\') {
			t.Fatalf("backslash accepted: %q", p)
		}
		if !strings.HasPrefix(p, "/") {
			t.Fatalf("relative path accepted: %q", p)
		}
		if p != "/" && strings.HasSuffix(p, "/") {
			t.Fatalf("trailing slash accepted: %q", p)
		}
		if p != "/" && path.Clean(p) != p {
			t.Fatalf("unclean path accepted: %q", p)
		}
	})
}

// FuzzResolveWithinRoot：函数返回成功时，逻辑路径必然已通过校验、
// 解析结果必然落在 root 之内（Join + Rel 双向验证不可被任何输入绕过）。
func FuzzResolveWithinRoot(f *testing.F) {
	for _, root := range []string{"/srv/data", "/tmp/fz", "/a"} {
		for _, logical := range []string{"/", "/a", "/a/b", "..", "/..", "/a/../../x", "/a\x00"} {
			f.Add(root, logical)
		}
	}
	f.Fuzz(func(t *testing.T, root, logical string) {
		// root 语义上是已 canonicalize 的绝对目录；非绝对输入与
		// 含 NUL 的 root 跳过（不属于本函数契约）。
		if !filepath.IsAbs(root) {
			return
		}
		if strings.ContainsRune(root, '\x00') {
			return
		}
		target, err := ResolveWithinRoot(root, logical)
		if err != nil {
			return
		}
		if err := ValidateLogicalPath(logical); err != nil {
			t.Fatalf("ResolveWithinRoot accepted invalid logical path %q", logical)
		}
		if logical == "/" {
			if target != root {
				t.Fatalf("root logical path resolved to %q, want %q", target, root)
			}
			return
		}
		rel, relErr := filepath.Rel(root, target)
		if relErr != nil {
			t.Fatalf("Rel(%q, %q): %v", root, target, relErr)
		}
		if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			t.Fatalf("logical %q escaped root %q via %q", logical, root, target)
		}
	})
}

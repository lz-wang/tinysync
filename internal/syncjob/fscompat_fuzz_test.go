package syncjob

import (
	"path/filepath"
	"strings"
	"testing"
)

// FuzzLocalMapping：resolveLocalTarget 对任意输入不得 panic，拒绝
// 「..」组件、绝对路径与空路径；返回成功时结果必须落在 localRoot
// 之内（本地映射的 root confinement 不变量）。
func FuzzLocalMapping(f *testing.F) {
	for _, root := range []string{"/srv/tinysync", "/tmp/fz"} {
		for _, rel := range []string{"a.txt", "docs/a.txt", "..", "../escape", "/abs", "", ".", "a/../b", "a/../../b"} {
			f.Add(root, rel)
		}
	}
	f.Fuzz(func(t *testing.T, localRoot, rel string) {
		if !filepath.IsAbs(localRoot) {
			return
		}
		if strings.ContainsRune(localRoot, '\x00') {
			return
		}
		target, err := resolveLocalTarget(localRoot, rel)
		if err != nil {
			// 被拒绝的输入只要求不 panic 且错误可读。
			return
		}
		// 本函数是纯路径代数：接受即代表输入为非空相对路径且无
		// 「..」组件。
		if rel == "" || rel == "." || rel == ".." || strings.HasPrefix(rel, "/") {
			t.Fatalf("invalid rel %q accepted", rel)
		}
		for _, seg := range strings.Split(rel, "/") {
			if seg == ".." {
				t.Fatalf("rel %q with .. component accepted", rel)
			}
		}
		relBack, relErr := filepath.Rel(localRoot, target)
		if relErr != nil {
			t.Fatalf("Rel(%q, %q): %v", localRoot, target, relErr)
		}
		if relBack == ".." || strings.HasPrefix(relBack, ".."+string(filepath.Separator)) {
			t.Fatalf("rel %q escaped local root %q via %q", rel, localRoot, target)
		}
	})
}

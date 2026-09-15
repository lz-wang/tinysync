package syncjob

import (
	"errors"
	"testing"
)

// include 为空等价于 include all；全部路径都被选择。
func TestSelectorIncludeAllByDefault(t *testing.T) {
	sel, err := NewSelector(nil, nil)
	if err != nil {
		t.Fatalf("NewSelector: %v", err)
	}
	for _, p := range []string{"a.txt", "docs/report.pdf", "photos/2026/a.jpg"} {
		if !sel.Match(p) {
			t.Errorf("Match(%q) = false, want true (include all)", p)
		}
	}
}

// exclude 永远优先于 include：两者同时命中时排除。
func TestSelectorExcludeWins(t *testing.T) {
	sel, err := NewSelector(
		[]string{"**/*.jpg"},
		[]string{"private/**", "**/thumb.jpg"},
	)
	if err != nil {
		t.Fatalf("NewSelector: %v", err)
	}
	cases := map[string]bool{
		"photos/a.jpg":      true,
		"photos/2026/b.jpg": true,
		"private/a.jpg":     false,
		"photos/thumb.jpg":  false,
		"docs/report.pdf":   false, // 不满足 include
		"photos/keep.png":   false,
	}
	for path, want := range cases {
		if got := sel.Match(path); got != want {
			t.Errorf("Match(%q) = %v, want %v", path, got, want)
		}
	}
}

// glob 语法覆盖：* 单段、** 跨段、? 单字符、[] 字符类。
func TestSelectorGlobSyntax(t *testing.T) {
	sel, err := NewSelector(
		[]string{"docs/*.txt", "photos/**", "log-?.txt", "data/[ab].bin"},
		nil,
	)
	if err != nil {
		t.Fatalf("NewSelector: %v", err)
	}
	cases := map[string]bool{
		"docs/a.txt":        true,
		"docs/sub/a.txt":    false, // * 不跨段
		"photos/2026/a.jpg": true,
		"photos/a.jpg":      true,
		"log-1.txt":         true,
		"log-12.txt":        false, // ? 只匹配单字符
		"data/a.bin":        true,
		"data/c.bin":        false,
		"docs":              false,
	}
	for path, want := range cases {
		if got := sel.Match(path); got != want {
			t.Errorf("Match(%q) = %v, want %v", path, got, want)
		}
	}
}

// 精确路径等价于精确 include。
func TestSelectorExactPath(t *testing.T) {
	sel, err := NewSelector([]string{"docs/report.pdf"}, nil)
	if err != nil {
		t.Fatalf("NewSelector: %v", err)
	}
	if !sel.Match("docs/report.pdf") {
		t.Error("Match(exact) = false, want true")
	}
	if sel.Match("docs/other.pdf") {
		t.Error("Match(other) = true, want false")
	}
}

// 非法 pattern（未闭合的字符类）在构造时报错，不在运行时静默不匹配。
func TestSelectorRejectsInvalidPattern(t *testing.T) {
	if _, err := NewSelector([]string{"docs/[a-"}, nil); err == nil {
		t.Error("NewSelector with invalid include pattern = nil, want error")
	}
	if _, err := NewSelector(nil, []string{"**/["}); err == nil {
		t.Error("NewSelector with invalid exclude pattern = nil, want error")
	}
	// 哨兵错误可判定。
	_, err := NewSelector([]string{"docs/[a-"}, nil)
	if !errors.Is(err, ErrInvalid) {
		t.Errorf("error = %v, want ErrInvalid", err)
	}
}

// Pattern 只作用于相对 RemoteRoot 的 / 分隔路径；前导 / 不影响匹配语义
// 的一致性（引擎传入的 input 始终无前导 /）。
func TestSelectorRelativePathsOnly(t *testing.T) {
	sel, err := NewSelector([]string{"**/*.jpg"}, nil)
	if err != nil {
		t.Fatalf("NewSelector: %v", err)
	}
	// 引擎契约：input 是相对路径（不含 RemoteRoot 前缀、无前导 /）。
	if !sel.Match("2026/a.jpg") {
		t.Error("Match(relative) = false, want true")
	}
}

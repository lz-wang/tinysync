package filesafe

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestValidateLogicalPath 覆盖基础规则：root、普通路径、dot segments、
// 重复分隔符、尾随分隔符、反斜杠（含 Windows separator 形态）、NUL
// 与相对路径。
func TestValidateLogicalPath(t *testing.T) {
	valid := []string{"/", "/a", "/a/b.txt", "/docs/my file 中文.txt"}
	for _, p := range valid {
		if err := ValidateLogicalPath(p); err != nil {
			t.Errorf("ValidateLogicalPath(%q) = %v, want nil", p, err)
		}
	}

	invalid := []struct {
		path   string
		reason string
	}{
		{"", "empty"},
		{"../", "dot-dot relative"},
		{"/../x", "leading dot-dot"},
		{"/a/../../x", "nested dot-dot"},
		{"/a/./b", "dot segment"},
		{"/a//b", "duplicate separator"},
		{"//a", "leading duplicate separator"},
		{"/a/", "trailing separator"},
		{`/a\b`, "backslash"},
		{"a\\b", "relative with backslash"},
		{"\\", "bare backslash"},
		{`C:\x`, "windows drive path"},
		{"/a\x00b", "NUL"},
		{"relative/path", "relative"},
	}
	for _, tt := range invalid {
		if err := ValidateLogicalPath(tt.path); err == nil {
			t.Errorf("ValidateLogicalPath(%q) = nil, want error (%s)", tt.path, tt.reason)
		}
	}
}

// TestNormalizePublicPath 覆盖 public path 规则：与逻辑路径共用基础
// 规则，额外拒绝 root 与空白输入。
func TestNormalizePublicPath(t *testing.T) {
	valid := map[string]string{
		"/a.jpg":          "/a.jpg",
		"/photos/a.jpg":   "/photos/a.jpg",
		"/docs/my 1.txt":  "/docs/my 1.txt",
		"  /photos/b.png": "/photos/b.png",
	}
	for in, want := range valid {
		got, err := NormalizePublicPath(in)
		if err != nil {
			t.Errorf("NormalizePublicPath(%q) = %v, want %q", in, err, want)
			continue
		}
		if got != want {
			t.Errorf("NormalizePublicPath(%q) = %q, want %q", in, got, want)
		}
	}

	invalid := []string{
		"",
		"   ",
		"a.jpg",
		"/",
		"/a/../b",
		"/a/./b",
		"//a",
		"/a/",
		`/a\b`,
		"/a\x00b",
	}
	for _, p := range invalid {
		if _, err := NormalizePublicPath(p); err == nil {
			t.Errorf("NormalizePublicPath(%q) = nil error, want error", p)
		}
	}
}

// TestResolveWithinRoot 验证纯路径代数：root 映射自身、逻辑路径拼接、
// 逃逸与非法输入拒绝；不做文件系统检查。
func TestResolveWithinRoot(t *testing.T) {
	root := t.TempDir()

	got, err := ResolveWithinRoot(root, "/")
	if err != nil {
		t.Fatalf("ResolveWithinRoot(root, \"/\") = %v, want nil", err)
	}
	if got != root {
		t.Errorf("root logical path resolved to %q, want %q", got, root)
	}

	got, err = ResolveWithinRoot(root, "/a/b.txt")
	if err != nil {
		t.Fatalf("ResolveWithinRoot(root, \"/a/b.txt\") = %v, want nil", err)
	}
	if want := filepath.Join(root, "a", "b.txt"); got != want {
		t.Errorf("resolved = %q, want %q", got, want)
	}

	for _, p := range []string{"", "relative", "/../x", "/a//b", "/a/", `C:\x`} {
		if _, err := ResolveWithinRoot(root, p); err == nil {
			t.Errorf("ResolveWithinRoot(root, %q) = nil error, want error", p)
		}
	}
}

// TestResolveRegularFile 覆盖真实文件系统下的解析语义：普通文件、
// 目录、root 自身、root 内 / 外 / 断链 symlink 与父目录 symlink
// 逃逸。symlink 一律拒绝；父目录 symlink 逃出 root 必须被发现。
func TestResolveRegularFile(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()

	write := func(rel, content string) string {
		t.Helper()
		abs := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", abs, err)
		}
		if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", abs, err)
		}
		return abs
	}
	plain := write("docs/a.txt", "hello")
	write("other/b.txt", "world")
	if err := os.Mkdir(filepath.Join(root, "subdir"), 0o755); err != nil {
		t.Fatalf("mkdir subdir: %v", err)
	}
	outsideFile := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(outsideFile, []byte("secret"), 0o644); err != nil {
		t.Fatalf("write outside file: %v", err)
	}

	// symlink 集合：指向 root 内、指向 root 外、断链。
	link := func(link, target string) {
		t.Helper()
		if err := os.Symlink(target, filepath.Join(root, filepath.FromSlash(link))); err != nil {
			t.Fatalf("symlink %s -> %s: %v", link, target, err)
		}
	}
	link("inside-link.txt", "docs/a.txt")
	link("outside-link.txt", outsideFile)
	link("broken-link.txt", "no-such-target.txt")
	link("dir-link", "subdir")
	link("escape", outside)

	t.Run("regular file", func(t *testing.T) {
		resolved, info, err := ResolveRegularFile(root, "/docs/a.txt")
		if err != nil {
			t.Fatalf("ResolveRegularFile regular file: %v", err)
		}
		// macOS 的临时目录位于 /var（/private/var 的 symlink）；
		// 返回值是 canonical 形态，与 plain 逐字节比较前先归一。
		canonicalPlain, err := filepath.EvalSymlinks(plain)
		if err != nil {
			t.Fatalf("evalsymlinks plain: %v", err)
		}
		if resolved != canonicalPlain {
			t.Errorf("resolved = %q, want %q", resolved, canonicalPlain)
		}
		if !info.Mode().IsRegular() || info.Size() != 5 {
			t.Errorf("info = %v, want regular file size 5", info)
		}
	})

	t.Run("directory rejected", func(t *testing.T) {
		_, _, err := ResolveRegularFile(root, "/subdir")
		if !errors.Is(err, ErrNotRegularFile) {
			t.Errorf("directory error = %v, want ErrNotRegularFile", err)
		}
	})

	t.Run("root itself rejected", func(t *testing.T) {
		_, _, err := ResolveRegularFile(root, "/")
		if !errors.Is(err, ErrNotRegularFile) {
			t.Errorf("root error = %v, want ErrNotRegularFile", err)
		}
	})

	for _, tt := range []struct {
		path   string
		reason string
	}{
		{"/inside-link.txt", "symlink to file inside root"},
		{"/outside-link.txt", "symlink to file outside root"},
		{"/broken-link.txt", "broken symlink"},
		{"/dir-link", "symlink to directory"},
	} {
		_, _, err := ResolveRegularFile(root, tt.path)
		if !errors.Is(err, ErrNotRegularFile) {
			t.Errorf("ResolveRegularFile(%s) error = %v, want ErrNotRegularFile (%s)", tt.path, err, tt.reason)
		}
	}

	t.Run("parent symlink escape rejected", func(t *testing.T) {
		// /escape 指向 root 外目录；其下的 secret.txt 经 Lstat 可见、
		// 是普通文件，但整链解析后越出 root，必须拒绝。
		_, _, err := ResolveRegularFile(root, "/escape/secret.txt")
		if err == nil {
			t.Fatal("parent symlink escape = nil error, want error")
		}
		if errors.Is(err, ErrNotRegularFile) {
			t.Errorf("escape error = %v, want escape error instead of ErrNotRegularFile", err)
		}
	})

	t.Run("missing file", func(t *testing.T) {
		_, _, err := ResolveRegularFile(root, "/docs/nope.txt")
		if !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("missing file error = %v, want fs.ErrNotExist", err)
		}
	})
}

// TestResolveWithinRootRejectsParentEscape 单独验证 Rel 兜底：即使
// 上游校验被绕过，Join + Rel 双向验证也不产生 root 之外的路径。
func TestResolveWithinRootRejectsParentEscape(t *testing.T) {
	root := t.TempDir()
	// 构造逻辑上不可能通过校验的输入，直接驱动内部路径代数：
	// 只有 ValidateLogicalPath 放行的路径才会走到 Join。
	for _, p := range []string{"/a/../../x", "/../x"} {
		_, err := ResolveWithinRoot(root, p)
		if err == nil || !strings.Contains(err.Error(), "not clean") {
			t.Errorf("ResolveWithinRoot(%q) error = %v, want not-clean rejection", p, err)
		}
	}
}

// TestLstatWithinRoot：最终组件保留 Lstat 语义（symlink 可见），父
// 目录组件中的 symlink 逃逸必须拒绝——父目录指向 root 外时，目标
// 的存在性 / 类型 / size / mtime 都不得泄露。
func TestLstatWithinRoot(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()

	write := func(rel, content string) {
		t.Helper()
		abs := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", abs, err)
		}
		if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", abs, err)
		}
	}
	write("docs/a.txt", "hello")
	outsideFile := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(outsideFile, []byte("secret"), 0o644); err != nil {
		t.Fatalf("write outside file: %v", err)
	}
	link := func(link, target string) {
		t.Helper()
		if err := os.Symlink(target, filepath.Join(root, filepath.FromSlash(link))); err != nil {
			t.Fatalf("symlink %s -> %s: %v", link, target, err)
		}
	}
	link("inside-link.txt", "docs/a.txt")
	link("escape", outside)

	t.Run("regular file", func(t *testing.T) {
		_, info, err := LstatWithinRoot(root, "/docs/a.txt")
		if err != nil {
			t.Fatalf("LstatWithinRoot regular file: %v", err)
		}
		if !info.Mode().IsRegular() || info.Size() != 5 {
			t.Errorf("info = %v, want regular file size 5", info)
		}
	})

	t.Run("root itself", func(t *testing.T) {
		_, info, err := LstatWithinRoot(root, "/")
		if err != nil || !info.IsDir() {
			t.Errorf("LstatWithinRoot(/) = %v, %v; want directory", info, err)
		}
	})

	t.Run("final symlink stays visible", func(t *testing.T) {
		_, info, err := LstatWithinRoot(root, "/inside-link.txt")
		if err != nil {
			t.Fatalf("LstatWithinRoot symlink: %v", err)
		}
		if info.Mode()&os.ModeSymlink == 0 {
			t.Errorf("info mode = %v, want symlink preserved", info.Mode())
		}
	})

	t.Run("parent symlink escape rejected", func(t *testing.T) {
		_, info, err := LstatWithinRoot(root, "/escape/secret.txt")
		if info != nil || !errors.Is(err, ErrEscape) {
			t.Errorf("parent escape = %v, %v; want ErrEscape", info, err)
		}
	})

	t.Run("missing target", func(t *testing.T) {
		_, _, err := LstatWithinRoot(root, "/docs/nope.txt")
		if !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("missing target error = %v, want fs.ErrNotExist", err)
		}
	})

	t.Run("missing parent", func(t *testing.T) {
		_, _, err := LstatWithinRoot(root, "/no-such-dir/a.txt")
		if !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("missing parent error = %v, want fs.ErrNotExist", err)
		}
	})
}

// TestOpenCanonicalRegularFile：canonical 路径在持久化之后被替换为
// symlink（最终组件或任意父组件）时必须拒绝；普通文件经句柄二次
// 确认后打开。
func TestOpenCanonicalRegularFile(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()

	file := filepath.Join(root, "a.txt")
	if err := os.WriteFile(file, []byte("hello"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	canonical, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatalf("evalsymlinks root: %v", err)
	}
	target := filepath.Join(canonical, "a.txt")

	t.Run("regular file opens", func(t *testing.T) {
		f, info, err := OpenCanonicalRegularFile(target)
		if err != nil {
			t.Fatalf("OpenCanonicalRegularFile: %v", err)
		}
		defer func() { _ = f.Close() }()
		if !info.Mode().IsRegular() || info.Size() != 5 {
			t.Errorf("info = %v, want regular file size 5", info)
		}
	})

	t.Run("missing file", func(t *testing.T) {
		_, _, err := OpenCanonicalRegularFile(filepath.Join(canonical, "nope.txt"))
		if !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("missing file error = %v, want fs.ErrNotExist", err)
		}
	})

	t.Run("final component replaced by symlink", func(t *testing.T) {
		outsideFile := filepath.Join(outside, "secret.txt")
		if err := os.WriteFile(outsideFile, []byte("secret"), 0o644); err != nil {
			t.Fatalf("write outside: %v", err)
		}
		victim := filepath.Join(canonical, "swap.txt")
		if err := os.WriteFile(victim, []byte("x"), 0o644); err != nil {
			t.Fatalf("write victim: %v", err)
		}
		if err := os.Remove(victim); err != nil {
			t.Fatalf("remove victim: %v", err)
		}
		if err := os.Symlink(outsideFile, victim); err != nil {
			t.Fatalf("symlink: %v", err)
		}
		_, _, err := OpenCanonicalRegularFile(victim)
		if !errors.Is(err, ErrNotRegularFile) {
			t.Errorf("final symlink error = %v, want ErrNotRegularFile", err)
		}
	})

	t.Run("parent directory replaced by symlink", func(t *testing.T) {
		// docs -> outside：持久化路径的中间组件成为 symlink，全链
		// 解析结果偏离持久化形态，必须拒绝。symlink 目标下放置同名
		// 普通文件，保证拒绝来自父组件逃逸而非最终组件缺失。
		outsideFile := filepath.Join(outside, "inner.txt")
		if err := os.WriteFile(outsideFile, []byte("s"), 0o644); err != nil {
			t.Fatalf("write outside inner: %v", err)
		}
		dir := filepath.Join(canonical, "docs")
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatalf("mkdir docs: %v", err)
		}
		inner := filepath.Join(dir, "inner.txt")
		if err := os.WriteFile(inner, []byte("x"), 0o644); err != nil {
			t.Fatalf("write inner: %v", err)
		}
		if err := os.Remove(inner); err != nil {
			t.Fatalf("remove inner: %v", err)
		}
		if err := os.Remove(dir); err != nil {
			t.Fatalf("remove docs: %v", err)
		}
		if err := os.Symlink(outside, dir); err != nil {
			t.Fatalf("symlink docs: %v", err)
		}
		_, _, err := OpenCanonicalRegularFile(inner)
		if !errors.Is(err, ErrEscape) {
			t.Errorf("parent symlink error = %v, want ErrEscape", err)
		}
	})
}

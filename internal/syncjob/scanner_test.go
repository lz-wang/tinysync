package syncjob

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"tinysync/internal/source"
)

// remoteRelPath 计算远端 logical path 相对 RemoteRoot 的相对路径。
func TestRemoteRelPath(t *testing.T) {
	cases := []struct {
		name       string
		remoteRoot string
		logical    string
		want       string
		wantErr    bool
	}{
		{name: "under root", remoteRoot: "/photos", logical: "/photos/2026/a.jpg", want: "2026/a.jpg"},
		{name: "direct child", remoteRoot: "/photos", logical: "/photos/a.jpg", want: "a.jpg"},
		{name: "bare root", remoteRoot: "/", logical: "/docs/report.pdf", want: "docs/report.pdf"},
		{name: "root itself", remoteRoot: "/", logical: "/", want: "."},
		// 非根 RemoteRoot 之下的 Source 根一律越界：防御异常服务器把
		// "/"（或空串）混入子树列表，导致扫描越出 Job 边界。
		{name: "source root under non-root", remoteRoot: "/photos", logical: "/", wantErr: true},
		{name: "empty under non-root", remoteRoot: "/photos", logical: "", wantErr: true},
		{name: "escape parent", remoteRoot: "/photos", logical: "/etc/passwd", wantErr: true},
		{name: "prefix ambiguity", remoteRoot: "/photos", logical: "/photosmith/a.jpg", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := remoteRelPath(tc.remoteRoot, tc.logical)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("remoteRelPath(%q, %q) = %q, want error", tc.remoteRoot, tc.logical, got)
				}
				if !errors.Is(err, ErrInvalid) {
					t.Errorf("error = %v, want ErrInvalid", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("remoteRelPath(%q, %q): %v", tc.remoteRoot, tc.logical, err)
			}
			if got != tc.want {
				t.Errorf("remoteRelPath(%q, %q) = %q, want %q", tc.remoteRoot, tc.logical, got, tc.want)
			}
		})
	}
}

// containment 判定对根路径同样正确："/" 包含一切绝对路径，
// 不因前缀拼接产生 "//" 而漏判；兄弟目录前缀歧义不误判。
// Windows 卷根（自带 trailing separator）走同一 filepath.Rel 路径。
func TestSameOrUnderRootContainment(t *testing.T) {
	if !sameOrUnder("/", "/") {
		t.Error(`sameOrUnder("/", "/") = false, want true`)
	}
	if !sameOrUnder("/", "/home/user/tinysync") {
		t.Error(`sameOrUnder("/", "/home/user/tinysync") = false, want true`)
	}
	if !sameOrUnder("/home/user", "/home/user/tinysync") {
		t.Error("direct child should be contained")
	}
	if sameOrUnder("/home/user", "/homeusers") {
		t.Error("prefix ambiguity must not count as containment")
	}
	if sameOrUnder("/a/b", "/a/c") {
		t.Error("sibling directory must not count as containment")
	}
}

// fakeRemote 可编程的 source.Remote：按目录返回条目或错误。
type fakeRemote struct {
	entries map[string][]source.FileInfo
	errs    map[string]error
}

func (f *fakeRemote) Stat(ctx context.Context, path string) (source.FileInfo, error) {
	return source.FileInfo{}, errors.New("not implemented")
}

func (f *fakeRemote) List(ctx context.Context, path string) ([]source.FileInfo, error) {
	if err := f.errs[path]; err != nil {
		return nil, err
	}
	return f.entries[path], nil
}

func (f *fakeRemote) Open(ctx context.Context, path string) (io.ReadCloser, error) {
	return nil, errors.New("not implemented")
}

func (f *fakeRemote) Close() error {
	return nil
}

// 递归扫描返回子树内全部文件（不含目录），嵌套层级完整。
func TestScanRemoteRecursive(t *testing.T) {
	remote := &fakeRemote{entries: map[string][]source.FileInfo{
		"/photos": {
			{Path: "/photos/docs", IsDir: true},
			{Path: "/photos/a.jpg"},
		},
		"/photos/docs": {
			{Path: "/photos/docs/report.txt"},
			{Path: "/photos/docs/sub", IsDir: true},
		},
		"/photos/docs/sub": {
			{Path: "/photos/docs/sub/deep.bin"},
		},
	}}
	files, err := ScanRemote(context.Background(), remote, "/photos")
	if err != nil {
		t.Fatalf("ScanRemote: %v", err)
	}
	var got []string
	for _, f := range files {
		if f.IsDir {
			t.Errorf("scan returned directory %s", f.Path)
		}
		got = append(got, f.Path)
	}
	for _, want := range []string{"/photos/a.jpg", "/photos/docs/report.txt", "/photos/docs/sub/deep.bin"} {
		if !slices.Contains(got, want) {
			t.Errorf("scan missing %s, got %v", want, got)
		}
	}
}

// 任一层 List 失败都整体失败：Mirror 的删除授权依赖完整快照，
// 绝不返回部分结果。
func TestScanRemoteFailsIncomplete(t *testing.T) {
	remote := &fakeRemote{
		entries: map[string][]source.FileInfo{
			"/photos": {
				{Path: "/photos/a.jpg"},
				{Path: "/photos/docs", IsDir: true},
			},
		},
		errs: map[string]error{
			"/photos/docs": errors.New("connection reset"),
		},
	}
	files, err := ScanRemote(context.Background(), remote, "/photos")
	if err == nil {
		t.Fatalf("ScanRemote = %v files, want error on partial scan", files)
	}
	if !strings.Contains(err.Error(), "connection reset") {
		t.Errorf("error = %v, want underlying cause", err)
	}
}

// resolveLocalTarget 把 / 分隔的相对路径安全解析到 LocalRoot 之下，
// 拒绝 .. 逃逸与绝对注入。
func TestResolveLocalTarget(t *testing.T) {
	root := string(filepath.Separator) + "srv" + string(filepath.Separator) + "backup"
	cases := []struct {
		name    string
		rel     string
		want    string
		wantErr bool
	}{
		{name: "plain", rel: "docs/report.pdf", want: filepath.Join(root, "docs", "report.pdf")},
		{name: "nested", rel: "a/b/c.txt", want: filepath.Join(root, "a", "b", "c.txt")},
		{name: "dot escape", rel: "a/../../escape", wantErr: true},
		{name: "leading dot dot", rel: "../escape", wantErr: true},
		{name: "absolute injection", rel: "/etc/passwd", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveLocalTarget(root, tc.rel)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("resolveLocalTarget(%q) = %q, want error", tc.rel, got)
				}
				if !errors.Is(err, ErrInvalid) {
					t.Errorf("error = %v, want ErrInvalid", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveLocalTarget(%q): %v", tc.rel, err)
			}
			if got != tc.want {
				t.Errorf("resolveLocalTarget(%q) = %q, want %q", tc.rel, got, tc.want)
			}
		})
	}
}

// 本地路径组件检查：既有路径中的 symlink 一律拒绝，普通树放行，
// 尚不存在的目标放行（由下载时的创建策略负责）。
func TestRejectSymlinkComponents(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "docs"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "docs", "ok.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	// 正常目标：组件均为真实目录/文件。
	target := filepath.Join(root, "docs", "new.txt")
	if err := rejectSymlinkComponents(root, target); err != nil {
		t.Fatalf("rejectSymlinkComponents(normal) = %v, want nil", err)
	}

	// 中间组件是 symlink（即使指向 root 内部）也拒绝。
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "docs", "link")); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	if err := rejectSymlinkComponents(root, filepath.Join(root, "docs", "link", "f.txt")); err == nil {
		t.Error("symlinked intermediate component accepted, want error")
	}

	// 目标自身是已有 symlink 拒绝（不得覆盖）。
	if err := os.Symlink(outside, filepath.Join(root, "docs", "alias.txt")); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	if err := rejectSymlinkComponents(root, filepath.Join(root, "docs", "alias.txt")); err == nil {
		t.Error("symlink target accepted, want error")
	}

	// 指向 root 外部与否无关紧要：v0.3 一律从严拒绝。
}

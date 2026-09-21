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

func (f *fakeRemote) List(ctx context.Context, path string, opts source.ListOptions) (source.FilePage, error) {
	if err := f.errs[path]; err != nil {
		return source.FilePage{}, err
	}
	return source.FilePage{Entries: f.entries[path]}, nil
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

// ScanRemote 对 adapter 返回的条目做跨协议 logical path 二次校验：
// 反斜杠、dot segments、重复分隔符等不可移植路径整体失败扫描，
// 不进入本地 filepath 映射（Mirror 在完整扫描失败时不会删除）。
func TestScanRemoteRejectsInvalidLogicalPaths(t *testing.T) {
	for name, bad := range map[string]string{
		"backslash":       `/photos\file.txt`,
		"dot-dot":         "/photos/../file.txt",
		"dot segment":     "/photos/./file.txt",
		"duplicate slash": "/photos//file.txt",
		"trailing slash":  "/photos/file.txt/",
		"relative entry":  "photos/file.txt",
	} {
		t.Run(name, func(t *testing.T) {
			remote := &fakeRemote{entries: map[string][]source.FileInfo{
				"/photos": {{Path: bad}},
			}}
			files, err := ScanRemote(context.Background(), remote, "/photos")
			if err == nil {
				t.Fatalf("ScanRemote with %q = %v files, want error", bad, files)
			}
			if !errors.Is(err, source.ErrInvalid) {
				t.Errorf("error = %v, want ErrInvalid", err)
			}
		})
	}

	// logical path 合法但越出 RemoteRoot：由既有边界检查拒绝。
	remote := &fakeRemote{entries: map[string][]source.FileInfo{
		"/photos": {{Path: "/other/file.txt"}},
	}}
	if _, err := ScanRemote(context.Background(), remote, "/photos"); !errors.Is(err, ErrInvalid) {
		t.Errorf("ScanRemote escaping entry = %v, want ErrInvalid", err)
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

// pagerRemote 按预定义页序列返回：对同一目录的 List 依序返回下一
// 页并记录 cursor 消费，验证 scanner 的分页循环。
type pagerRemote struct {
	pages map[string][]source.FilePage
	calls map[string]int
}

func newPagerRemote(pages map[string][]source.FilePage) *pagerRemote {
	return &pagerRemote{pages: pages, calls: map[string]int{}}
}

func (p *pagerRemote) Stat(ctx context.Context, path string) (source.FileInfo, error) {
	return source.FileInfo{}, errors.New("not implemented")
}

func (p *pagerRemote) List(ctx context.Context, path string, opts source.ListOptions) (source.FilePage, error) {
	seq := p.pages[path]
	n := p.calls[path]
	p.calls[path] = n + 1
	if n >= len(seq) {
		return source.FilePage{}, errors.New("unexpected extra List call")
	}
	page := seq[n]
	// cursor 语义：首轮必须空串，续页必须回传上一页的 NextCursor。
	want := ""
	if n > 0 {
		want = seq[n-1].NextCursor
	}
	if opts.Cursor != want {
		return source.FilePage{}, errors.New("cursor not propagated")
	}
	return page, nil
}

func (p *pagerRemote) Open(ctx context.Context, path string) (io.ReadCloser, error) {
	return nil, errors.New("not implemented")
}

func (p *pagerRemote) Close() error {
	return nil
}

// scanner 按分页契约逐页消费：cursor 原样回传、EOF 以空 NextCursor
// 表达，跨页聚合全部文件。
func TestScanRemoteConsumesPages(t *testing.T) {
	remote := newPagerRemote(map[string][]source.FilePage{
		"/photos": {
			{Entries: []source.FileInfo{
				{Path: "/photos/a.jpg"},
				{Path: "/photos/docs", IsDir: true},
			}, NextCursor: "cursor-1"},
			{Entries: []source.FileInfo{
				{Path: "/photos/b.jpg"},
			}},
		},
		"/photos/docs": {
			{Entries: []source.FileInfo{
				{Path: "/photos/docs/report.txt"},
			}, NextCursor: "deep-cursor"},
			{Entries: []source.FileInfo{
				{Path: "/photos/docs/second.txt"},
			}},
		},
	})
	files, err := ScanRemote(context.Background(), remote, "/photos")
	if err != nil {
		t.Fatalf("ScanRemote: %v", err)
	}
	var got []string
	for _, f := range files {
		got = append(got, f.Path)
	}
	for _, want := range []string{"/photos/a.jpg", "/photos/b.jpg", "/photos/docs/report.txt", "/photos/docs/second.txt"} {
		if !slices.Contains(got, want) {
			t.Errorf("scan missing %s across pages, got %v", want, got)
		}
	}
	if remote.calls["/photos"] != 2 || remote.calls["/photos/docs"] != 2 {
		t.Errorf("calls = %v, want 2 pages per directory", remote.calls)
	}
}

// 同一 path 跨页以不同类型出现（如 S3 一页报文件、另一页报目录）：
// 整体失败，保持 file/dir collision 的 fail-fast 快照语义。
func TestScanRemoteRejectsCrossPageCollision(t *testing.T) {
	remote := newPagerRemote(map[string][]source.FilePage{
		"/photos": {
			{Entries: []source.FileInfo{
				{Path: "/photos/a.jpg"},
				{Path: "/photos/docs", IsDir: true},
			}, NextCursor: "cursor-1"},
			{Entries: []source.FileInfo{
				{Path: "/photos/docs"},
			}},
		},
		// 第二页的目录条目会触发对该层的递归；提供空末页让递归正常
		// 结束，collision 检测发生在 /photos 层的 seen 判定。
		"/photos/docs": {
			{},
		},
	})
	if _, err := ScanRemote(context.Background(), remote, "/photos"); !errors.Is(err, ErrInvalid) {
		t.Errorf("cross-page collision error = %v, want ErrInvalid", err)
	}
}

// 本地路径组件检查：既有路径中的 symlink 一律拒绝，普通树放行，
// 尚不存在的目标放行（由下载时的创建策略负责）。
func TestRejectSymlinkComponents(t *testing.T) {	root := t.TempDir()
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

// treeRemote 是实现 TreeScanner 的 fake：List 恒定失败，ScanRemote
// 若仍调 List 即失败——以此证明 fast path 确实生效。
type treeRemote struct {
	entries []source.FileInfo
	calls   int
}

func (t *treeRemote) Stat(ctx context.Context, path string) (source.FileInfo, error) {
	return source.FileInfo{}, errors.New("not implemented")
}

func (t *treeRemote) List(ctx context.Context, path string, opts source.ListOptions) (source.FilePage, error) {
	t.calls++
	return source.FilePage{}, errors.New("List must not be called when TreeScanner is available")
}

func (t *treeRemote) ScanTree(ctx context.Context, root string, visit func(source.FileInfo) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	for _, entry := range t.entries {
		if err := visit(entry); err != nil {
			return err
		}
	}
	return nil
}

func (t *treeRemote) Open(ctx context.Context, path string) (io.ReadCloser, error) {
	return nil, errors.New("not implemented")
}

func (t *treeRemote) Close() error {
	return nil
}

// ScanRemote 优先走 TreeScanner fast path：全部条目（含目录）经
// collector 校验后文件进入快照，List 不被调用。
func TestScanRemotePrefersTreeScanner(t *testing.T) {
	remote := &treeRemote{entries: []source.FileInfo{
		{Path: "/photos/docs", IsDir: true},
		{Path: "/photos/a.jpg"},
		{Path: "/photos/docs/report.txt"},
	}}
	files, err := ScanRemote(context.Background(), remote, "/photos")
	if err != nil {
		t.Fatalf("ScanRemote: %v", err)
	}
	if remote.calls != 0 {
		t.Errorf("List called %d times on TreeScanner fast path, want 0", remote.calls)
	}
	if len(files) != 2 {
		t.Fatalf("files = %v, want [/photos/a.jpg /photos/docs/report.txt]", files)
	}
	for _, f := range files {
		if f.IsDir {
			t.Errorf("scan returned directory %s", f.Path)
		}
	}
}

// fast path 同样维持 file/dir collision fail-fast：目录 visit 让
// S3 式「同 path 既是文件又是目录」在本地 mutation 前整体失败。
func TestScanRemoteTreeScannerRejectsCollision(t *testing.T) {
	remote := &treeRemote{entries: []source.FileInfo{
		{Path: "/photos/foo"},
		{Path: "/photos/foo/bar.txt"},
		{Path: "/photos/foo", IsDir: true},
	}}
	if _, err := ScanRemote(context.Background(), remote, "/photos"); !errors.Is(err, ErrInvalid) {
		t.Errorf("tree scan collision error = %v, want ErrInvalid", err)
	}
}

// fast path 复用同一 collector：非法 logical path 与越出 RemoteRoot
// 的条目照常拒绝（前者是 source.ErrInvalid，后者是扫描边界语义的
// ErrInvalid）。
func TestScanRemoteTreeScannerRejectsInvalidAndEscape(t *testing.T) {
	cases := map[string]struct {
		entries []source.FileInfo
		want    error
	}{
		"invalid path": {entries: []source.FileInfo{{Path: `/photos\file.txt`}}, want: source.ErrInvalid},
		"dot segments": {entries: []source.FileInfo{{Path: "/photos/../file.txt"}}, want: source.ErrInvalid},
		"escape root":  {entries: []source.FileInfo{{Path: "/other/file.txt"}}, want: ErrInvalid},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			remote := &treeRemote{entries: tc.entries}
			if _, err := ScanRemote(context.Background(), remote, "/photos"); !errors.Is(err, tc.want) {
				t.Errorf("tree scan error = %v, want %v", err, tc.want)
			}
		})
	}
}

// fast path 维持 max depth：超出 maxScanDepth 的目录条目整体失败，
// 不依赖 adapter 自我约束。
func TestScanRemoteTreeScannerDepthLimit(t *testing.T) {
	entries := []source.FileInfo{}
	prefix := ""
	for i := 0; i <= maxScanDepth; i++ {
		prefix += "/d"
		entries = append(entries, source.FileInfo{Path: prefix, IsDir: true})
	}
	remote := &treeRemote{entries: entries}
	_, err := ScanRemote(context.Background(), remote, "/")
	if err == nil {
		t.Fatal("tree scan beyond max depth = nil, want error")
	}
	if !strings.Contains(err.Error(), "max depth") {
		t.Errorf("error = %v, want max depth message", err)
	}
}

// fast path 及时响应 ctx 取消（契约由 adapter 保证，collector 透传
// 错误即可）。
func TestScanRemoteTreeScannerContextCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	remote := &treeRemote{entries: []source.FileInfo{{Path: "/photos/a.jpg"}}}
	if _, err := ScanRemote(ctx, remote, "/photos"); !errors.Is(err, context.Canceled) {
		t.Errorf("tree scan canceled error = %v, want context.Canceled", err)
	}
}

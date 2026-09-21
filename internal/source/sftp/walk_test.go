package sftp

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"testing"
	"time"

	"tinysync/internal/source"
	"tinysync/internal/syncjob"
)

// countingReadDir 是 walkDirectory 注入用的工作目录读取 fake：记录
// 每层目录的 ReadDir 调用次数，把「每个目录恰好一次枚举」变成
// 确定性断言。
type countingReadDir struct {
	calls map[string]int
	dirs  map[string][]fs.FileInfo
	errs  map[string]error
}

func newCountingReadDir() *countingReadDir {
	return &countingReadDir{
		calls: map[string]int{},
		dirs:  map[string][]fs.FileInfo{},
		errs:  map[string]error{},
	}
}

func (c *countingReadDir) ReadDir(ctx context.Context, logical string) ([]fs.FileInfo, error) {
	c.calls[logical]++
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := c.errs[logical]; err != nil {
		return nil, err
	}
	return c.dirs[logical], nil
}

// fakeEntry 构造最小 fs.FileInfo。
type fakeEntry struct {
	name  string
	dir   bool
	link  bool
	value string
}

func (e fakeEntry) Name() string { return e.name }

func (e fakeEntry) Size() int64 { return int64(len(e.value)) }

func (e fakeEntry) Mode() fs.FileMode {
	switch {
	case e.link:
		return fs.ModeSymlink | 0o777
	case e.dir:
		return fs.ModeDir | 0o755
	default:
		return 0o644
	}
}

func (e fakeEntry) ModTime() time.Time { return time.Unix(1757879400, 0).UTC() }

func (e fakeEntry) IsDir() bool { return e.dir }

func (e fakeEntry) Sys() any { return nil }

// flat：10k 文件单目录 → ReadDir 恰好 1 次。
func TestWalkDirectoryFlatSingleReadDir(t *testing.T) {
	if testing.Short() {
		t.Skip("10k dataset is expensive for -short")
	}
	const total = 10000
	rd := newCountingReadDir()
	entries := make([]fs.FileInfo, 0, total)
	for i := 0; i < total; i++ {
		entries = append(entries, fakeEntry{name: fmt.Sprintf("f%06d.txt", i), value: "x"})
	}
	rd.dirs["/bulk"] = entries

	var files []source.FileInfo
	err := walkDirectory(context.Background(), "/bulk", rd.ReadDir, func(fi source.FileInfo) error {
		files = append(files, fi)
		return nil
	})
	if err != nil {
		t.Fatalf("walkDirectory: %v", err)
	}
	if len(files) != total {
		t.Fatalf("visited %d entries, want %d", len(files), total)
	}
	if got := rd.calls["/bulk"]; got != 1 {
		t.Errorf("ReadDir calls on 10k flat directory = %d, want 1", got)
	}
}

// nested：100 子目录 + root → ReadDir 恰好 101 次。
func TestWalkDirectoryNestedOneReadDirPerDirectory(t *testing.T) {
	const dirs = 100
	const perDir = 100
	rd := newCountingReadDir()
	rootEntries := []fs.FileInfo{}
	for d := 0; d < dirs; d++ {
		name := fmt.Sprintf("d%03d", d)
		rootEntries = append(rootEntries, fakeEntry{name: name, dir: true})
		children := make([]fs.FileInfo, 0, perDir)
		for i := 0; i < perDir; i++ {
			children = append(children, fakeEntry{name: fmt.Sprintf("f%02d.txt", i), value: "x"})
		}
		rd.dirs["/bulk/"+name] = children
	}
	rd.dirs["/bulk"] = rootEntries

	var files int
	err := walkDirectory(context.Background(), "/bulk", rd.ReadDir, func(fi source.FileInfo) error {
		if !fi.IsDir {
			files++
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walkDirectory: %v", err)
	}
	if files != dirs*perDir {
		t.Fatalf("files = %d, want %d", files, dirs*perDir)
	}
	if got := len(rd.calls); got != dirs+1 {
		t.Errorf("distinct ReadDir targets = %d, want %d (root + one per directory)", got, dirs+1)
	}
	for dir, n := range rd.calls {
		if n != 1 {
			t.Errorf("directory %s read %d times, want exactly 1", dir, n)
		}
	}
}

// callback 错误立即终止，不继续枚举子目录。
func TestWalkDirectoryCallbackErrorAborts(t *testing.T) {
	rd := newCountingReadDir()
	rd.dirs["/bulk"] = []fs.FileInfo{
		fakeEntry{name: "a.txt", value: "x"},
		fakeEntry{name: "sub", dir: true},
	}
	rd.dirs["/bulk/sub"] = []fs.FileInfo{fakeEntry{name: "deep.txt", value: "x"}}

	sentinel := errors.New("stop walk")
	calls := 0
	err := walkDirectory(context.Background(), "/bulk", rd.ReadDir, func(fi source.FileInfo) error {
		calls++
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Errorf("walkDirectory callback error = %v, want sentinel", err)
	}
	if calls != 1 {
		t.Errorf("visit called %d times, want 1 (abort immediately)", calls)
	}
	if rd.calls["/bulk/sub"] != 0 {
		t.Error("subdirectory enumerated after callback error")
	}
}

// symlink 由 toFileInfo 拒绝，策略与 List 一致。
func TestWalkDirectoryRejectsSymlink(t *testing.T) {
	rd := newCountingReadDir()
	rd.dirs["/bulk"] = []fs.FileInfo{fakeEntry{name: "link.txt", link: true}}
	err := walkDirectory(context.Background(), "/bulk", rd.ReadDir, func(fi source.FileInfo) error { return nil })
	if !errors.Is(err, source.ErrInvalid) {
		t.Errorf("symlink walk error = %v, want ErrInvalid", err)
	}
}

// ctx 取消立即停止。
func TestWalkDirectoryContextCanceled(t *testing.T) {
	rd := newCountingReadDir()
	rd.dirs["/bulk"] = []fs.FileInfo{fakeEntry{name: "a.txt", value: "x"}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := walkDirectory(ctx, "/bulk", rd.ReadDir, func(fi source.FileInfo) error { return nil })
	if !errors.Is(err, context.Canceled) {
		t.Errorf("walkDirectory canceled = %v, want context.Canceled", err)
	}
}

// visit 中途取消：单层 ReadDir 一次返回全部条目，entry loop 逐条
// 检查 ctx，同层剩余条目不再处理（契约「及时响应 ctx 取消」）。
func TestWalkDirectoryCancelDuringVisit(t *testing.T) {
	rd := newCountingReadDir()
	const total = 100
	entries := make([]fs.FileInfo, 0, total)
	for i := 0; i < total; i++ {
		entries = append(entries, fakeEntry{name: fmt.Sprintf("f%03d.txt", i), value: "x"})
	}
	rd.dirs["/bulk"] = entries

	ctx, cancel := context.WithCancel(context.Background())
	visits := 0
	err := walkDirectory(ctx, "/bulk", rd.ReadDir, func(fi source.FileInfo) error {
		visits++
		cancel()
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("walkDirectory cancel-during-visit = %v, want context.Canceled", err)
	}
	if visits >= total {
		t.Errorf("visit called %d times, want < %d (stop within the entry loop)", visits, total)
	}
}

// readDir 错误透传。
func TestWalkDirectoryReadDirError(t *testing.T) {
	rd := newCountingReadDir()
	rd.errs["/bulk"] = errors.New("connection reset")
	_, err := walkCollect(t, rd, "/bulk")
	if err == nil || !strings.Contains(err.Error(), "connection reset") {
		t.Errorf("walkDirectory error = %v, want underlying cause", err)
	}
}

// walkCollect 便利封装：收集全部条目。
func walkCollect(t *testing.T, rd *countingReadDir, root string) ([]source.FileInfo, error) {
	t.Helper()
	var out []source.FileInfo
	err := walkDirectory(context.Background(), root, rd.ReadDir, func(fi source.FileInfo) error {
		out = append(out, fi)
		return nil
	})
	return out, err
}

// 真实 SSH/SFTP 服务端到端：ScanTree 扫完整子树，文件/目录区分
// 正确，RemoteRoot 边界成立。
func TestScanTreeEndToEnd(t *testing.T) {
	root := t.TempDir()
	seedFile(t, root, "bulk/a/f.txt", "a")
	seedFile(t, root, "bulk/b/c/deep.txt", "deep")
	seedFile(t, root, "bulk/top.txt", "top")
	seedFile(t, root, "outside/secret.txt", "no")
	ts := startTestServer(t)

	cfg := sftpSourceConfig(ts, root, source.SFTPAuthPassword)
	r := newSFTPFactoryRemote(t, ts, cfg, source.Credentials{SFTP: &source.SFTPCredentials{
		Password: testPassword,
	}})
	scanner, ok := r.(source.TreeScanner)
	if !ok {
		t.Fatal("sftp remote does not implement source.TreeScanner")
	}

	visited := map[string]bool{}
	err := scanner.ScanTree(context.Background(), "/bulk", func(fi source.FileInfo) error {
		visited[fi.Path] = fi.IsDir
		return nil
	})
	if err != nil {
		t.Fatalf("ScanTree: %v", err)
	}
	for _, dir := range []string{"/bulk/a", "/bulk/b", "/bulk/b/c"} {
		if !visited[dir] {
			t.Errorf("directory %s not visited", dir)
		}
	}
	for _, file := range []string{"/bulk/top.txt", "/bulk/a/f.txt", "/bulk/b/c/deep.txt"} {
		if isDir, seen := visited[file]; !seen || isDir {
			t.Errorf("file %s visited = %v,%v; want file", file, seen, isDir)
		}
	}
	if visited["/outside"] || visited["/outside/secret.txt"] {
		t.Error("scan escaped remote root")
	}
	if visited["/"] || visited["/bulk"] {
		t.Error("root itself must not be visited")
	}

	// ScanRemote 断言 TreeScanner 能力后走 fast path：端到端等价。
	files, err := syncjob.ScanRemote(context.Background(), r, "/bulk")
	if err != nil {
		t.Fatalf("ScanRemote: %v", err)
	}
	if len(files) != 3 {
		t.Fatalf("ScanRemote files = %d, want 3", len(files))
	}
}

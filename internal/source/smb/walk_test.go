package smb

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"sync/atomic"
	"testing"

	"tinysync/internal/source"
)

// countReadDir 包装 readDir 并按 native 路径计数调用次数：把「每个
// 目录恰好一次 ReadDir」变成确定性断言（不靠 wall-clock）。
type countReadDir struct {
	inner  func(context.Context, string) ([]os.FileInfo, error)
	counts map[string]*atomic.Int64
}

func (c *countReadDir) readDir(ctx context.Context, logical string) ([]os.FileInfo, error) {
	n, ok := c.counts[logical]
	if !ok {
		n = &atomic.Int64{}
		c.counts[logical] = n
	}
	n.Add(1)
	return c.inner(ctx, logical)
}

// walkFixture 构造三层树：
//
//	root → a.txt、docs/、photos/
//	docs → b.txt、inner/
//	docs/inner → deep.txt
//	photos →（空目录）
func walkFixture() (map[string][]fakeEntry, map[string]string) {
	dirs := map[string][]fakeEntry{
		"": {
			{name: "a.txt", size: 2},
			{name: "docs", isDir: true},
			{name: "photos", isDir: true},
		},
		`docs`: {
			{name: "b.txt", size: 2},
			{name: "inner", isDir: true},
		},
		`docs\inner`: {
			{name: "deep.txt", size: 4},
		},
		`photos`: {},
	}
	files := map[string]string{
		`a.txt`: "v1", `docs\b.txt`: "b1", `docs\inner\deep.txt`: "deep",
	}
	return dirs, files
}

// walkReadDirOf 把 fake 目录树适配为 walkDirectory 的 readDir 注入
// （logical → native 由 remotePath 完成，root 固定 "/"）。
func walkReadDirOf(c *fakeConn) func(context.Context, string) ([]os.FileInfo, error) {
	return func(ctx context.Context, logical string) ([]os.FileInfo, error) {
		native, err := remotePath("/", logical)
		if err != nil {
			return nil, err
		}
		return c.readDir(ctx, native)
	}
}

// ScanTree 全量遍历：文件与目录都 visit（root 除外），顺序不作为契约。
func TestScanTreeVisitsFilesAndDirs(t *testing.T) {
	dirs, _ := walkFixture()
	c := newFakeConn()
	for dir, entries := range dirs {
		c.seed(dir, entries...)
	}
	scanned := map[string]bool{}
	err := walkDirectory(context.Background(), "/", walkReadDirOf(c), func(fi source.FileInfo) error {
		scanned[fi.Path] = true
		if fi.Path == "/docs" || fi.Path == "/docs/inner" || fi.Path == "/photos" {
			if !fi.IsDir {
				t.Errorf("%s IsDir = false", fi.Path)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walkDirectory: %v", err)
	}
	want := map[string]bool{
		"/a.txt": true, "/docs": true, "/photos": true,
		"/docs/b.txt": true, "/docs/inner": true, "/docs/inner/deep.txt": true,
	}
	for p := range want {
		if !scanned[p] {
			t.Errorf("entry %q not visited (got %v)", p, scanned)
		}
	}
	for p := range scanned {
		if !want[p] {
			t.Errorf("unexpected entry %q", p)
		}
	}
}

// 每个目录恰好一次 ReadDir：同步扫描不对同一目录重复枚举。
func TestScanTreeOneReadDirPerDirectory(t *testing.T) {
	dirs, _ := walkFixture()
	c := newFakeConn()
	for dir, entries := range dirs {
		c.seed(dir, entries...)
	}
	counter := &countReadDir{inner: walkReadDirOf(c), counts: map[string]*atomic.Int64{}}
	err := walkDirectory(context.Background(), "/", counter.readDir, func(source.FileInfo) error { return nil })
	if err != nil {
		t.Fatalf("walkDirectory: %v", err)
	}
	for _, dir := range []string{"/", "/docs", "/docs/inner", "/photos"} {
		if n := counter.counts[dir].Load(); n != 1 {
			t.Errorf("readDir %q called %d times, want 1", dir, n)
		}
	}
}

// 空目录：正常 visit 子树中的空目录，不产生多余请求。
func TestScanTreeEmptyDirectory(t *testing.T) {
	c := newFakeConn()
	c.seed("", fakeEntry{name: "empty-dir", isDir: true})
	c.dirs[`empty-dir`] = []fakeEntry{}
	var visited []string
	err := walkDirectory(context.Background(), "/", walkReadDirOf(c), func(fi source.FileInfo) error {
		visited = append(visited, fi.Path)
		return nil
	})
	if err != nil {
		t.Fatalf("walkDirectory: %v", err)
	}
	if len(visited) != 1 || visited[0] != "/empty-dir" {
		t.Errorf("visited = %v, want [/empty-dir]", visited)
	}
}

// 部分失败 = 整体失败：任一子目录枚举失败时绝不返回部分结果
// （Mirror 删除安全语义的关键断言）。
func TestScanTreePartialFailureFailsWhole(t *testing.T) {
	dirs, _ := walkFixture()
	c := newFakeConn()
	for dir, entries := range dirs {
		c.seed(dir, entries...)
	}
	c.mu.Lock()
	// 生产路径中 go-smb2 已把 STATUS_ACCESS_DENIED 映射为 fs.ErrPermission，
	// fake 直接注入哨兵错误以贴近 wire 形态。
	c.readDirErrs[`docs\inner`] = fs.ErrPermission
	c.mu.Unlock()

	var visited []string
	err := walkDirectory(context.Background(), "/", walkReadDirOf(c), func(fi source.FileInfo) error {
		visited = append(visited, fi.Path)
		return nil
	})
	if err == nil {
		t.Fatal("walkDirectory with failing subdir = nil, want error")
	}
	// 调用方契约：失败时 visit 到的条目不得当作快照使用；这里只确认
	// 错误正确传播（深层文件未被访问也于事无补——错误本身就是结论）。
	if !errors.Is(err, fs.ErrPermission) {
		t.Errorf("error = %v, want wrapped fs.ErrPermission", err)
	}
}

// visit 错误原样透传（含调用方安全策略的拒绝）。
func TestScanTreeVisitErrorPropagates(t *testing.T) {
	dirs, _ := walkFixture()
	c := newFakeConn()
	for dir, entries := range dirs {
		c.seed(dir, entries...)
	}
	sentinel := errors.New("visitor rejected")
	err := walkDirectory(context.Background(), "/", walkReadDirOf(c), func(fi source.FileInfo) error {
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Errorf("error = %v, want visitor sentinel", err)
	}
}

// reparse point 出现在树中任意位置：ScanTree fail-closed。
func TestScanTreeReparseFailsClosed(t *testing.T) {
	c := newFakeConn()
	c.seed("", fakeEntry{name: "a.txt", size: 1})
	c.seed("", fakeEntry{name: "junction", isDir: true, reparse: true})
	err := walkDirectory(context.Background(), "/", walkReadDirOf(c), func(source.FileInfo) error { return nil })
	if !errors.Is(err, source.ErrInvalid) {
		t.Errorf("error = %v, want ErrInvalid (reparse rejected)", err)
	}
}

// ctx 取消：遍历立即终止并返回 ctx 错误（不继续展开后续目录）。
func TestScanTreeContextCancellation(t *testing.T) {
	dirs, _ := walkFixture()
	c := newFakeConn()
	for dir, entries := range dirs {
		c.seed(dir, entries...)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := walkDirectory(ctx, "/", walkReadDirOf(c), func(source.FileInfo) error { return nil })
	if !errors.Is(err, context.Canceled) {
		t.Errorf("error = %v, want context.Canceled", err)
	}
}

// 条目级取消：readDir 返回后、下一条目处理前取消同样立即生效
// （单层可能一次带回数万条目）。
func TestScanTreeCancellationBetweenEntries(t *testing.T) {
	c := newFakeConn()
	entries := make([]fakeEntry, 0, 64)
	for i := range 64 {
		entries = append(entries, fakeEntry{name: fmt.Sprintf("f%02d.txt", i), size: 1})
	}
	c.seed("", entries...)
	ctx, cancel := context.WithCancel(context.Background())
	var visited int
	err := walkDirectory(ctx, "/", walkReadDirOf(c), func(source.FileInfo) error {
		visited++
		if visited == 3 {
			cancel()
		}
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("error = %v, want context.Canceled", err)
	}
	if visited >= 64 {
		t.Errorf("visited %d entries after cancel, want prompt termination", visited)
	}
}

// remote 层的 ScanTree：transport 故障拆除连接代际并返回 transient
// （Downloader 的重试据此建立新连接）。
func TestScanTreeTransportFailureTearsDown(t *testing.T) {
	dirs, _ := walkFixture()
	c := newFakeConn()
	for dir, entries := range dirs {
		c.seed(dir, entries...)
	}
	r := newFakeRemote(t, func(ctx context.Context) (conn, error) { return c, nil })

	c.kill()
	err := r.ScanTree(context.Background(), "/", func(source.FileInfo) error { return nil })
	if err == nil {
		t.Fatal("ScanTree on dead connection = nil, want error")
	}
	if !source.IsRetryable(err) {
		t.Errorf("error %v not retryable, want transient", err)
	}
	if c.closeCount != 1 {
		t.Errorf("teardown closeCount = %d, want 1", c.closeCount)
	}
	// 连接已拆空：下一次 session 重连。
	c.mu.Lock()
	dead := c.dead
	c.mu.Unlock()
	if !dead {
		t.Fatal("fake conn not dead; test premise broken")
	}
}

// os.FileInfo 缺失（条目消失）在 readDir 边界原样传播。
func TestScanTreeMissingDirectory(t *testing.T) {
	c := newFakeConn()
	c.seed("", fakeEntry{name: "docs", isDir: true})
	// `docs` 目录条目存在但从未登记内容：readDir 返回 not exist。
	err := walkDirectory(context.Background(), "/", walkReadDirOf(c), func(source.FileInfo) error { return nil })
	if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("error = %v, want os.ErrNotExist", err)
	}
}

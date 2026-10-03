package smb

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	smb2 "github.com/cloudsoda/go-smb2"

	"tinysync/internal/source"
)

// —— fake conn：内存目录树 + 可注入故障，确定性验证 adapter 的
// 协议无关语义（Stat / List / Open / Mkdir / 错误分类）。条目形态
// 直接构造 *smb2.FileStat，与 go-smb2 从服务器响应解码出的类型一致，
// toFileInfo 的 reparse point 断言因此走真实类型路径。

// FILE_ATTRIBUTE_DIRECTORY / FILE_ATTRIBUTE_REPARSE_POINT 的 MS
// 固定数值（go-smb2 的常量在 internal 包，外部不可引用）。
const (
	faDirectory      uint32 = 0x10
	faReparsePoint   uint32 = 0x400
	ioReparseSymlink uint32 = 0xA000000C
)

// fakeEntry 是内存目录树的一个条目。
type fakeEntry struct {
	name    string
	isDir   bool
	size    int64
	reparse bool
}

// stat 构造与生产路径同形的 *smb2.FileStat。
func (e fakeEntry) stat() *smb2.FileStat {
	st := &smb2.FileStat{
		FileName:      e.name,
		EndOfFile:     e.size,
		LastWriteTime: time.Unix(1757879400, 0).UTC(),
	}
	if e.isDir {
		st.FileAttributes |= faDirectory
	}
	if e.reparse {
		st.FileAttributes |= faReparsePoint
		st.ReparsePointTag = ioReparseSymlink
	}
	return st
}

// fakeConn 是 conn 的内存实现。dead 模拟连接已被对端关闭：所有操作
// 返回 TransportError（拆除回声）。closeCount 记录 close 调用。
type fakeConn struct {
	mu         sync.Mutex
	dirs       map[string][]fakeEntry // native dir path（"" = root）→ 条目
	files      map[string]string      // native file path → 内容
	mkdirLog   []string
	dead       bool
	closeCount int
	// readDirErrs 按 native path 注入一次性的 ReadDir 故障（消费后清除）。
	readDirErrs map[string]error
}

func newFakeConn() *fakeConn {
	return &fakeConn{
		dirs:        map[string][]fakeEntry{},
		files:       map[string]string{},
		readDirErrs: map[string]error{},
	}
}

// seed 在 native dir 下登记条目。
func (c *fakeConn) seed(dir string, entries ...fakeEntry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.dirs[dir] = append(c.dirs[dir], entries...)
}

func (c *fakeConn) writeFile(native, content string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.files[native] = content
}

func (c *fakeConn) kill() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.dead = true
}

func (c *fakeConn) stat(native string) (os.FileInfo, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.dead {
		return nil, &smb2.TransportError{Err: errors.New("connection reset by peer")}
	}
	if _, ok := c.dirs[native]; ok {
		return (&fakeEntry{name: lastSegment(native), isDir: true}).stat(), nil
	}
	if native == "" {
		// root 未登记（fake 未 seed root）等价于不存在。
		return nil, os.ErrNotExist
	}
	// 条目：在父目录中查找（文件与目录都按登记形态返回，reparse
	// 标记随条目——与真实服务器对已登记路径的 Lstat 语义一致）。
	parent, name := splitNative(native)
	for _, e := range c.dirs[parent] {
		if e.name == name {
			return e.stat(), nil
		}
	}
	return nil, os.ErrNotExist
}

func (c *fakeConn) lstat(ctx context.Context, native string) (os.FileInfo, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return c.stat(native)
}

func (c *fakeConn) readDir(ctx context.Context, native string) ([]os.FileInfo, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.dead {
		return nil, &smb2.TransportError{Err: errors.New("connection reset by peer")}
	}
	if err := c.readDirErrs[native]; err != nil {
		delete(c.readDirErrs, native)
		return nil, err
	}
	entries, ok := c.dirs[native]
	if !ok {
		return nil, os.ErrNotExist
	}
	sorted := make([]fakeEntry, len(entries))
	copy(sorted, entries)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].name < sorted[j].name })
	out := make([]os.FileInfo, 0, len(sorted))
	for _, e := range sorted {
		out = append(out, e.stat())
	}
	return out, nil
}

func (c *fakeConn) open(ctx context.Context, native string) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.dead {
		return nil, &smb2.TransportError{Err: errors.New("connection reset by peer")}
	}
	content, ok := c.files[native]
	if !ok {
		return nil, os.ErrNotExist
	}
	return io.NopCloser(bytes.NewReader([]byte(content))), nil
}

func (c *fakeConn) mkdir(ctx context.Context, native string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.dead {
		return &smb2.TransportError{Err: errors.New("connection reset by peer")}
	}
	if _, exists := c.dirs[native]; exists {
		return os.ErrExist
	}
	c.mkdirLog = append(c.mkdirLog, native)
	c.dirs[native] = nil
	parent, name := splitNative(native)
	if parent != native {
		c.dirs[parent] = append(c.dirs[parent], fakeEntry{name: name, isDir: true})
	}
	return nil
}

func (c *fakeConn) close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closeCount++
	return nil
}

// splitNative 拆分 native 路径为父目录与名字（"" 为 root）。
func splitNative(native string) (parent, name string) {
	if native == "" {
		return "", ""
	}
	idx := strings.LastIndexByte(native, '\\')
	if idx < 0 {
		return "", native
	}
	return native[:idx], native[idx+1:]
}

// lastSegment 取 native 路径最后一段作为 stat 名。
func lastSegment(native string) string {
	_, name := splitNative(native)
	if name == "" {
		return "/"
	}
	return name
}

// newFakeRemote 用注入的 connect 构造 remote 并完成首连。
func newFakeRemote(t *testing.T, connect func(ctx context.Context) (conn, error)) *remote {
	t.Helper()
	r := &remote{
		cfg: source.SMBConfig{
			Host: "nas.example.com", Port: 445, Share: "backup",
			RemoteRoot: "/", Username: "tinysync", Signing: source.SMBSigningRequired,
		},
		password: "secret",
		connect:  connect,
	}
	return r
}

// seededRemote 返回接在单个 fakeConn 上的 remote 与该 conn，root 下
// 预置契约数据集。
func seededRemote(t *testing.T) (*remote, *fakeConn) {
	t.Helper()
	c := newFakeConn()
	c.seed("", fakeEntry{name: "hello.txt", size: int64(len("hello smb"))})
	c.seed("", fakeEntry{name: "empty.txt"})
	c.writeFile(`hello.txt`, "hello smb")
	c.writeFile(`empty.txt`, "")
	c.seed("", fakeEntry{name: "docs", isDir: true})
	c.seed(`docs`, fakeEntry{name: "inner.txt", size: 5})
	c.writeFile(`docs\inner.txt`, "inner")
	r := newFakeRemote(t, func(ctx context.Context) (conn, error) { return c, nil })
	return r, c
}

// Factory 的类型与凭据守卫：错误类型 / 缺 config 在 dial 之前拒绝。
func TestFactoryGuards(t *testing.T) {
	f := NewFactory()
	if f.Type() != source.TypeSMB {
		t.Errorf("Type = %q, want smb", f.Type())
	}
	// wrong type / missing config。
	if _, err := f.Create(context.Background(), source.Source{
		Type:   source.TypeSFTP,
		Config: source.Config{SFTP: &source.SFTPConfig{}},
	}, source.Credentials{SMB: &source.SMBCredentials{Password: "p"}}); !errors.Is(err, source.ErrUnsupportedType) {
		t.Errorf("wrong type error = %v, want ErrUnsupportedType", err)
	}
	if _, err := f.Create(context.Background(), source.Source{
		Type: source.TypeSMB,
	}, source.Credentials{SMB: &source.SMBCredentials{Password: "p"}}); !errors.Is(err, source.ErrUnsupportedType) {
		t.Errorf("missing config error = %v, want ErrUnsupportedType", err)
	}
	// guest 不支持：password 缺失在 dial 之前拒绝。
	for name, creds := range map[string]source.Credentials{
		"nil group":    {},
		"empty secret": {SMB: &source.SMBCredentials{}},
	} {
		_, err := f.Create(context.Background(), source.Source{
			Type:   source.TypeSMB,
			Config: source.Config{SMB: &source.SMBConfig{Host: "nas", Share: "s", Username: "u"}},
		}, creds)
		if !errors.Is(err, source.ErrInvalid) {
			t.Errorf("credentials %s error = %v, want ErrInvalid", name, err)
		}
	}
}

// Stat：root / 文件 / 目录 / 缺失与 root confinement 映射。
func TestStat(t *testing.T) {
	r, _ := seededRemote(t)
	ctx := context.Background()

	fi, err := r.Stat(ctx, "/")
	if err != nil {
		t.Fatalf("Stat root: %v", err)
	}
	if !fi.IsDir || fi.Path != "/" {
		t.Errorf("root = %+v, want dir /", fi)
	}

	fi, err = r.Stat(ctx, "/hello.txt")
	if err != nil {
		t.Fatalf("Stat file: %v", err)
	}
	if fi.IsDir || fi.Fingerprint.Size != int64(len("hello smb")) {
		t.Errorf("file = %+v, want size %d", fi, len("hello smb"))
	}

	if _, err := r.Stat(ctx, "/missing.txt"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Stat missing = %v, want fs.ErrNotExist", err)
	}
}

// remote_root 非根时：logical path 映射到 root 子树，root 外的路径
// 无法表达（logical 是 Source-relative）。
func TestStatRemoteRootMapping(t *testing.T) {
	c := newFakeConn()
	c.seed(`photos\2026`, fakeEntry{name: "a.jpg", size: 3})
	c.writeFile(`photos\2026\a.jpg`, "jpg")
	r := newFakeRemote(t, func(ctx context.Context) (conn, error) { return c, nil })
	r.cfg.RemoteRoot = "/photos"

	fi, err := r.Stat(context.Background(), "/2026/a.jpg")
	if err != nil {
		t.Fatalf("Stat /2026/a.jpg via root /photos: %v", err)
	}
	if fi.Path != "/2026/a.jpg" || fi.Fingerprint.Size != 3 {
		t.Errorf("stat = %+v, want /2026/a.jpg size 3", fi)
	}
}

// List：单层枚举 + PageSlice 切页；条目类型正确。
func TestList(t *testing.T) {
	r, _ := seededRemote(t)
	ctx := context.Background()

	page, err := r.List(ctx, "/", source.ListOptions{Limit: source.MaxListLimit})
	if err != nil {
		t.Fatalf("List root: %v", err)
	}
	if page.NextCursor != "" {
		t.Errorf("full page cursor = %q, want EOF", page.NextCursor)
	}
	want := map[string]bool{"/hello.txt": true, "/empty.txt": true, "/docs": true}
	if len(page.Entries) != len(want) {
		t.Fatalf("root entries = %v, want %d", page.Entries, len(want))
	}
	for _, fi := range page.Entries {
		if !want[fi.Path] {
			t.Errorf("unexpected entry %q", fi.Path)
		}
		if fi.Path == "/docs" && !fi.IsDir {
			t.Error("/docs IsDir = false")
		}
	}

	// 小页分页：跟随 cursor 到 EOF，不重复不丢失。
	seen := map[string]int{}
	cursor := ""
	pages := 0
	for {
		page, err := r.List(ctx, "/", source.ListOptions{Limit: 1, Cursor: cursor})
		if err != nil {
			t.Fatalf("List page %d: %v", pages, err)
		}
		pages++
		for _, fi := range page.Entries {
			seen[fi.Path]++
		}
		if page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
	}
	if len(seen) != len(want) {
		t.Errorf("paged entries = %v, want %d distinct", seen, len(want))
	}
	for p, n := range seen {
		if n != 1 {
			t.Errorf("entry %q seen %d times", p, n)
		}
	}

	// 子目录。
	page, err = r.List(ctx, "/docs", source.ListOptions{})
	if err != nil {
		t.Fatalf("List /docs: %v", err)
	}
	if len(page.Entries) != 1 || page.Entries[0].Path != "/docs/inner.txt" {
		t.Errorf("subdir = %+v, want /docs/inner.txt", page.Entries)
	}
}

// Open：内容 roundtrip、空文件、缺失。
func TestOpen(t *testing.T) {
	r, _ := seededRemote(t)
	ctx := context.Background()

	rc, err := r.Open(ctx, "/hello.txt")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	data, err := io.ReadAll(rc)
	_ = rc.Close()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(data) != "hello smb" {
		t.Errorf("content = %q, want hello smb", data)
	}

	rc, err = r.Open(ctx, "/empty.txt")
	if err != nil {
		t.Fatalf("Open empty: %v", err)
	}
	data, err = io.ReadAll(rc)
	_ = rc.Close()
	if err != nil || len(data) != 0 {
		t.Errorf("empty read = %q, %v; want empty", data, err)
	}

	if _, err := r.Open(ctx, "/missing.txt"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Open missing = %v, want fs.ErrNotExist", err)
	}
}

// Mkdir：root 拒绝、正常创建落到 native 路径。
func TestMkdir(t *testing.T) {
	r, c := seededRemote(t)
	ctx := context.Background()

	if err := r.Mkdir(ctx, "/"); !errors.Is(err, source.ErrInvalid) {
		t.Errorf("Mkdir / = %v, want ErrInvalid", err)
	}
	if err := r.Mkdir(ctx, "/docs/newdir"); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	if len(c.mkdirLog) != 1 || c.mkdirLog[0] != `docs\newdir` {
		t.Errorf("mkdir log = %v, want [docs\\newdir]", c.mkdirLog)
	}
	// 新目录可被 List 发现。
	page, err := r.List(ctx, "/docs", source.ListOptions{})
	if err != nil {
		t.Fatalf("List after mkdir: %v", err)
	}
	found := false
	for _, fi := range page.Entries {
		if fi.Path == "/docs/newdir" && fi.IsDir {
			found = true
		}
	}
	if !found {
		t.Errorf("newdir missing from listing: %+v", page.Entries)
	}
}

// 非法 logical path 在任何操作入口统一拒绝（ErrInvalid fail-fast）。
func TestInvalidLogicalPathsRejected(t *testing.T) {
	r, _ := seededRemote(t)
	ctx := context.Background()
	for _, p := range []string{"not-absolute", "/a/../b", "a\\b", "/a//b", "/a/", ""} {
		if _, err := r.Stat(ctx, p); !errors.Is(err, source.ErrInvalid) {
			t.Errorf("Stat %q = %v, want ErrInvalid", p, err)
		}
		if _, err := r.List(ctx, p, source.ListOptions{}); !errors.Is(err, source.ErrInvalid) {
			t.Errorf("List %q = %v, want ErrInvalid", p, err)
		}
		if _, err := r.Open(ctx, p); !errors.Is(err, source.ErrInvalid) {
			t.Errorf("Open %q = %v, want ErrInvalid", p, err)
		}
	}
}

// reparse point 拒绝：Stat 与 List 对 reparse 条目 fail-closed，
// 绝不把 junction / symlink 当普通条目返回。
func TestReparsePointRejected(t *testing.T) {
	c := newFakeConn()
	c.seed("", fakeEntry{name: "junction", isDir: true, reparse: true})
	c.seed("", fakeEntry{name: "link.txt", size: 4, reparse: true})
	r := newFakeRemote(t, func(ctx context.Context) (conn, error) { return c, nil })
	ctx := context.Background()

	if _, err := r.Stat(ctx, "/junction"); !errors.Is(err, source.ErrInvalid) {
		t.Errorf("Stat junction = %v, want ErrInvalid", err)
	}
	if _, err := r.Stat(ctx, "/link.txt"); !errors.Is(err, source.ErrInvalid) {
		t.Errorf("Stat symlink = %v, want ErrInvalid", err)
	}
	if _, err := r.List(ctx, "/", source.ListOptions{}); !errors.Is(err, source.ErrInvalid) {
		t.Errorf("List with reparse entry = %v, want ErrInvalid", err)
	}
}

// ctx 取消：入口归一为 context.Canceled（go-smb2 的请求级取消之外，
// adapter 自身的入口与 fake conn 的 ctx 检查同步生效）。
func TestContextCancellation(t *testing.T) {
	r, _ := seededRemote(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := r.Stat(ctx, "/hello.txt"); !errors.Is(err, context.Canceled) {
		t.Errorf("Stat canceled = %v, want context.Canceled", err)
	}
	if _, err := r.List(ctx, "/", source.ListOptions{}); !errors.Is(err, context.Canceled) {
		t.Errorf("List canceled = %v, want context.Canceled", err)
	}
	if _, err := r.Open(ctx, "/hello.txt"); !errors.Is(err, context.Canceled) {
		t.Errorf("Open canceled = %v, want context.Canceled", err)
	}
}

// 错误分类：认证 / share / 权限 → permanent；会话删除与 transport →
// transient 且 isSessionLost；fs 哨兵与未知 NTSTATUS 原样。
func TestErrorClassification(t *testing.T) {
	cases := []struct {
		name        string
		err         error
		retryable   bool
		sessionLost bool
	}{
		{"logon failure", &smb2.ResponseError{Code: ntStatusLogonFailure}, false, false},
		{"bad network name", &smb2.ResponseError{Code: ntStatusBadNetworkName}, false, false},
		{"access denied", &smb2.ResponseError{Code: ntStatusAccessDenied}, false, false},
		{"user session deleted", &smb2.ResponseError{Code: ntStatusUserSessionDeleted}, true, true},
		{"network name deleted", &smb2.ResponseError{Code: ntStatusNetworkNameDeleted}, true, true},
		{"transport", &smb2.TransportError{Err: errors.New("reset")}, true, true},
		{"wrapped transport", fmt.Errorf("smb stat /a: %w", &smb2.TransportError{Err: errors.New("EOF")}), true, true},
		{"eof echo", io.EOF, true, true},
		{"not exist", os.ErrNotExist, false, false},
		{"unknown status", &smb2.ResponseError{Code: 0xC0000001}, true, false},
	}
	for _, tc := range cases {
		classified := classifyError(tc.err)
		if got := source.IsRetryable(classified); got != tc.retryable {
			t.Errorf("%s retryable = %v, want %v", tc.name, got, tc.retryable)
		}
		if got := isSessionLost(tc.err); got != tc.sessionLost {
			t.Errorf("%s sessionLost = %v, want %v", tc.name, got, tc.sessionLost)
		}
	}
}

// normalizeCtxErr：transport 拆除回声在 ctx 已取消时归一为 ctx 错误。
func TestNormalizeCtxErr(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := &smb2.TransportError{Err: errors.New("use of closed network connection")}
	if got := normalizeCtxErr(ctx, err); !errors.Is(got, context.Canceled) {
		t.Errorf("normalizeCtxErr = %v, want context.Canceled", got)
	}
	if got := normalizeCtxErr(context.Background(), err); !errors.Is(got, err) {
		t.Errorf("normalizeCtxErr with live ctx = %v, want original", got)
	}
}

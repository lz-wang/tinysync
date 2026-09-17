package browser

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"strings"
	"testing"
	"time"

	"tinysync/internal/source"
	"tinysync/internal/source/sqlite"
	"tinysync/internal/storage"
)

// fakeRemote 是 browser 测试的可控 Remote：list / stat / open 按预设
// 返回，并记录 Close 次数。
type fakeRemote struct {
	entries  map[string][]source.FileInfo
	dirs     map[string]bool
	listErr  error
	listPage *source.FilePage
	statErr  error
	openErr  error
	content  string
	closeErr error
	closeN   int
}

func (r *fakeRemote) Stat(ctx context.Context, path string) (source.FileInfo, error) {
	if r.statErr != nil {
		return source.FileInfo{}, r.statErr
	}
	if r.dirs[path] {
		return source.FileInfo{Path: path, IsDir: true}, nil
	}
	if _, ok := r.entries[path]; ok {
		return source.FileInfo{
			Path: path,
			Fingerprint: source.Fingerprint{
				Size:       int64(len(r.content)),
				ModifiedAt: time.Unix(1757879400, 0).UTC(),
			},
		}, nil
	}
	return source.FileInfo{}, fmt.Errorf("%w: %s not found", fs.ErrNotExist, path)
}

func (r *fakeRemote) List(ctx context.Context, path string, opts source.ListOptions) (source.FilePage, error) {
	if r.listErr != nil {
		return source.FilePage{}, r.listErr
	}
	if r.listPage != nil {
		return *r.listPage, nil
	}
	return source.FilePage{Entries: r.entries[path]}, nil
}

func (r *fakeRemote) Open(ctx context.Context, path string) (io.ReadCloser, error) {
	if r.openErr != nil {
		return nil, r.openErr
	}
	return io.NopCloser(strings.NewReader(r.content)), nil
}

func (r *fakeRemote) Close() error {
	r.closeN++
	return r.closeErr
}

// fakeFactory 返回共享的 fakeRemote 实例。
type fakeFactory struct {
	remote    *fakeRemote
	createErr error
}

func (f fakeFactory) Type() source.Type { return source.TypeWebDAV }

func (f fakeFactory) Create(ctx context.Context, s source.Source, credentials source.Credentials) (source.Remote, error) {
	if f.createErr != nil {
		return nil, f.createErr
	}
	return f.remote, nil
}

// newRemoteService 构造挂接 fake factory 的 browser 服务与已存在的
// Source ID。
func newRemoteService(t *testing.T, remote *fakeRemote, createErr error) (*RemoteService, string) {
	t.Helper()
	dataDir := t.TempDir()
	db, err := storage.Open(dataDir)
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := storage.Migrate(context.Background(), db, dataDir); err != nil {
		t.Fatalf("storage.Migrate: %v", err)
	}
	svc := source.NewService(sqlite.New(db), fakeFactory{remote: remote, createErr: createErr})
	src, err := svc.Create(context.Background(), source.CreateInput{
		Name:    "it",
		Type:    source.TypeWebDAV,
		Enabled: true,
		Config:  source.Config{WebDAV: &source.WebDAVConfig{Endpoint: "http://127.0.0.1:1/dav"}},
	})
	if err != nil {
		t.Fatalf("create source: %v", err)
	}
	return NewRemoteService(svc), src.ID
}

// List 经 OpenRemote 列目录并转换条目；Remote 用毕关闭。
func TestRemoteServiceList(t *testing.T) {
	mod := time.Unix(1757879400, 0).UTC()
	remote := &fakeRemote{
		entries: map[string][]source.FileInfo{
			"/": {
				{Path: "/docs", IsDir: true},
				{Path: "/a.txt", Fingerprint: source.Fingerprint{Size: 3, ModifiedAt: mod}},
			},
		},
		dirs: map[string]bool{"/docs": true},
	}
	svc, id := newRemoteService(t, remote, nil)

	entries, next, err := svc.List(context.Background(), id, "/", source.ListOptions{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if next != "" {
		t.Errorf("next cursor = %q, want empty", next)
	}
	if len(entries) != 2 {
		t.Fatalf("entries = %+v, want 2", entries)
	}
	byPath := map[string]Entry{}
	for _, e := range entries {
		byPath[e.Path] = e
	}
	if e := byPath["/docs"]; e.Kind != KindDirectory || e.Name != "docs" {
		t.Errorf("/docs entry = %+v, want directory named docs", e)
	}
	if e := byPath["/a.txt"]; e.Kind != KindFile || e.Size != 3 || e.ModifiedAt == nil || !e.ModifiedAt.Equal(mod) {
		t.Errorf("/a.txt entry = %+v, want file size 3 with mtime", e)
	}
	if remote.closeN != 1 {
		t.Errorf("Close count = %d, want 1 (remote released after list)", remote.closeN)
	}
}

// 非法逻辑路径在进入远端之前整体拒绝。
func TestRemoteServiceListRejectsInvalidPath(t *testing.T) {
	svc, id := newRemoteService(t, &fakeRemote{}, nil)
	for _, p := range []string{"../", "/a//b", "/a/../b", "relative"} {
		if _, _, err := svc.List(context.Background(), id, p, source.ListOptions{}); !errors.Is(err, ErrInvalid) {
			t.Errorf("List(%q) error = %v, want ErrInvalid", p, err)
		}
	}
}

// Stat 单条目转换；目标不存在映射为 ErrNotFound。
func TestRemoteServiceStat(t *testing.T) {
	remote := &fakeRemote{
		entries: map[string][]source.FileInfo{"/a.txt": {}},
		content: "abc",
	}
	svc, id := newRemoteService(t, remote, nil)

	entry, err := svc.Stat(context.Background(), id, "/a.txt")
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if entry.Kind != KindFile || entry.Size != 3 || entry.Name != "a.txt" {
		t.Errorf("entry = %+v, want file a.txt size 3", entry)
	}

	if _, err := svc.Stat(context.Background(), id, "/missing.txt"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Stat missing error = %v, want ErrNotFound", err)
	}
	if remote.closeN != 2 {
		t.Errorf("Close count = %d, want 2 (both stats release remote)", remote.closeN)
	}
}

// Open 返回流与元信息；release 关闭流与 Remote；目录拒绝下载。
func TestRemoteServiceOpen(t *testing.T) {
	remote := &fakeRemote{
		entries: map[string][]source.FileInfo{"/a.txt": {}},
		dirs:    map[string]bool{"/docs": true},
		content: "hello",
	}
	svc, id := newRemoteService(t, remote, nil)

	meta, body, release, err := svc.Open(context.Background(), id, "/a.txt")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if meta.Size != 5 || meta.ModifiedAt == nil {
		t.Errorf("meta = %+v, want size 5 and mtime", meta)
	}
	data, err := io.ReadAll(body)
	if err != nil || string(data) != "hello" {
		t.Fatalf("read = %q, %v; want hello", data, err)
	}
	if err := release(); err != nil {
		t.Fatalf("release: %v", err)
	}
	if remote.closeN != 1 {
		t.Errorf("Close count = %d, want 1", remote.closeN)
	}

	if _, _, _, err := svc.Open(context.Background(), id, "/docs"); !errors.Is(err, ErrInvalid) {
		t.Errorf("Open directory error = %v, want ErrInvalid", err)
	}
}

// Source 不存在 → ErrNotFound；factory 创建失败（dial / 认证）→
// ErrRemote。
func TestRemoteServiceErrorMapping(t *testing.T) {
	svc, _ := newRemoteService(t, &fakeRemote{}, nil)
	if _, _, err := svc.List(context.Background(), "no-such-source", "/", source.ListOptions{}); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing source error = %v, want ErrNotFound", err)
	}

	svc2, id2 := newRemoteService(t, &fakeRemote{}, errors.New("ssh: handshake failed"))
	if _, _, err := svc2.List(context.Background(), id2, "/", source.ListOptions{}); !errors.Is(err, ErrRemote) {
		t.Errorf("create failure error = %v, want ErrRemote", err)
	}

	// 远端列表故障 → ErrRemote。
	remote := &fakeRemote{listErr: errors.New("connection reset")}
	svc3, id3 := newRemoteService(t, remote, nil)
	if _, _, err := svc3.List(context.Background(), id3, "/", source.ListOptions{}); !errors.Is(err, ErrRemote) {
		t.Errorf("list failure error = %v, want ErrRemote", err)
	}
	if remote.closeN != 1 {
		t.Errorf("Close count = %d, want 1 (failure path still releases)", remote.closeN)
	}
}

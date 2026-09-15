package syncjob

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"tinysync/internal/source"
)

// engineRemote 同时支持 List 与 Open 的测试 Remote。
// contents 存原始内容，每次 Open 生成新 reader（模拟可重复读的远端）；
// overrides 允许注入特殊 reader（如挂起，一次性生效）；
// openErrs 让 Open 持续报错（跨重试，模拟持续性传输故障）。
type engineRemote struct {
	entries   map[string][]source.FileInfo
	contents  map[string]string
	overrides map[string]io.ReadCloser
	openErrs  map[string]error
	listErr   error
}

func (e *engineRemote) Stat(ctx context.Context, path string) (source.FileInfo, error) {
	return source.FileInfo{}, errorsNew("not implemented")
}

func (e *engineRemote) List(ctx context.Context, path string) ([]source.FileInfo, error) {
	if e.listErr != nil {
		return nil, e.listErr
	}
	return e.entries[path], nil
}

func (e *engineRemote) Open(ctx context.Context, path string) (io.ReadCloser, error) {
	if err := e.openErrs[path]; err != nil {
		return nil, err
	}
	// override 一次性生效：删除 key 而非置 nil，避免后续查到
	// (nil, true) 返回空 reader。
	if rc, ok := e.overrides[path]; ok {
		delete(e.overrides, path)
		return rc, nil
	}
	if content, ok := e.contents[path]; ok {
		return io.NopCloser(strings.NewReader(content)), nil
	}
	return nil, errorsNew("no such remote file " + path)
}

// errorsNew 是 errors.New 的短别名，保持测试表格紧凑。
func errorsNew(msg string) error { return &errString{msg} }

type errString struct{ msg string }

func (e *errString) Error() string { return e.msg }

// inMemoryManaged 是 ManagedRepository 的内存实现。
type inMemoryManaged struct {
	files map[string]ManagedFile // key: remotePath（单 Job 测试）
}

func newInMemoryManaged() *inMemoryManaged {
	return &inMemoryManaged{files: map[string]ManagedFile{}}
}

func (m *inMemoryManaged) ListByJob(ctx context.Context, jobID string) ([]ManagedFile, error) {
	var list []ManagedFile
	for _, f := range m.files {
		list = append(list, f)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].RemotePath < list[j].RemotePath })
	return list, nil
}

func (m *inMemoryManaged) Upsert(ctx context.Context, files []ManagedFile) error {
	for _, f := range files {
		f.UpdatedAt = time.Now().UTC()
		m.files[f.RemotePath] = f
	}
	return nil
}

func (m *inMemoryManaged) Delete(ctx context.Context, jobID string, remotePaths []string) error {
	for _, p := range remotePaths {
		delete(m.files, p)
	}
	return nil
}

func (m *inMemoryManaged) DeleteAllForJob(ctx context.Context, jobID string) error {
	m.files = map[string]ManagedFile{}
	return nil
}

// engineFixture 聚合引擎测试环境。
type engineFixture struct {
	t       *testing.T
	root    string // LocalRoot
	managed *inMemoryManaged
	job     Job
}

func newEngineFixture(t *testing.T, mode Mode) *engineFixture {
	t.Helper()
	root := t.TempDir()
	return &engineFixture{
		t:       t,
		root:    root,
		managed: newInMemoryManaged(),
		job: Job{
			ID:         "job_a",
			Name:       "test",
			RemoteRoot: "/",
			LocalRoot:  root,
			Mode:       mode,
			Enabled:    true,
		},
	}
}

// remote 构造带单文件内容的远端：目录映射 entries。
func buildRemote(files map[string]string, dirs []string) *engineRemote {
	r := &engineRemote{
		entries:   map[string][]source.FileInfo{"/": {}},
		contents:  map[string]string{},
		overrides: map[string]io.ReadCloser{},
		openErrs:  map[string]error{},
	}
	for _, d := range dirs {
		r.entries["/"] = append(r.entries["/"], source.FileInfo{Path: d, IsDir: true})
		r.entries[d] = nil
	}
	for path, content := range files {
		r.entries["/"] = append(r.entries["/"], source.FileInfo{
			Path:        path,
			Fingerprint: source.Fingerprint{Size: int64(len(content)), ModifiedAt: time.Unix(1757879400, 0).UTC(), ETag: etagOf(content)},
		})
		r.contents[path] = content
	}
	return r
}

// etagOf 用内容生成稳定假 ETag。
func etagOf(content string) string {
	return `"` + strings.ReplaceAll(content, " ", "_") + `"`
}

// run 执行一轮同步。
func (f *engineFixture) run(remote source.Remote) (RunStats, error) {
	f.t.Helper()
	return Run(context.Background(), RunOptions{Remote: remote, Job: f.job, Managed: f.managed})
}

// mustFile 断言本地文件内容。
func (f *engineFixture) mustFile(rel, want string) {
	f.t.Helper()
	data, err := os.ReadFile(filepath.Join(f.root, filepath.FromSlash(rel)))
	if err != nil {
		f.t.Fatalf("read %s: %v", rel, err)
	}
	if string(data) != want {
		f.t.Errorf("content of %s = %q, want %q", rel, data, want)
	}
}

// remote new → download，managed 登记 synced；Copy 与 Mirror 一致。
func TestRunDownloadsNewFiles(t *testing.T) {
	for _, mode := range []Mode{ModeCopy, ModeMirror} {
		t.Run(string(mode), func(t *testing.T) {
			f := newEngineFixture(t, mode)
			remote := buildRemote(map[string]string{"/docs/a.txt": "v1"}, []string{"/docs"})

			stats, err := f.run(remote)
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			f.mustFile("docs/a.txt", "v1")
			if stats.FilesCreated != 1 || stats.BytesTransferred != 2 {
				t.Errorf("stats = %+v, want 1 created / 2 bytes", stats)
			}
			m, ok := f.managed.files["/docs/a.txt"]
			if !ok || m.State != StateSynced {
				t.Errorf("managed = %+v, want synced entry", m)
			}
		})
	}
}

// 远端未变 → skip 不传输；远端变化 → update 覆盖。
func TestRunSkipsUnchangedAndUpdatesChanged(t *testing.T) {
	f := newEngineFixture(t, ModeMirror)
	remote := buildRemote(map[string]string{"/a.txt": "v1"}, nil)
	if _, err := f.run(remote); err != nil {
		t.Fatalf("first Run: %v", err)
	}

	stats, err := f.run(remote)
	if err != nil {
		t.Fatalf("second Run: %v", err)
	}
	if stats.FilesSkipped != 1 || stats.BytesTransferred != 0 {
		t.Errorf("second Run stats = %+v, want 1 skipped / 0 bytes", stats)
	}

	remote.contents["/a.txt"] = "v2-long"
	remote.entries["/"][0].Fingerprint = source.Fingerprint{
		Size:       int64(len("v2-long")),
		ModifiedAt: time.Unix(1757879401, 0).UTC(),
		ETag:       etagOf("v2-long"),
	}
	stats, err = f.run(remote)
	if err != nil {
		t.Fatalf("third Run: %v", err)
	}
	if stats.FilesUpdated != 1 {
		t.Errorf("third Run stats = %+v, want 1 updated", stats)
	}
	f.mustFile("a.txt", "v2-long")
}

// 本地 managed 文件丢失 → repair 重新下载。
func TestRunRepairsMissingLocalFile(t *testing.T) {
	f := newEngineFixture(t, ModeMirror)
	remote := buildRemote(map[string]string{"/a.txt": "v1"}, nil)
	if _, err := f.run(remote); err != nil {
		t.Fatalf("first Run: %v", err)
	}
	if err := os.Remove(filepath.Join(f.root, "a.txt")); err != nil {
		t.Fatalf("remove local: %v", err)
	}

	stats, err := f.run(remote)
	if err != nil {
		t.Fatalf("second Run: %v", err)
	}
	if stats.FilesCreated != 1 {
		t.Errorf("stats = %+v, want 1 created (repair)", stats)
	}
	f.mustFile("a.txt", "v1")
}

// 远端删除：Copy 保留本地并解除管理；Mirror 删除 managed 本地文件，
// 未知的本地文件永远保留。
func TestRunRemoteDeleteCopyVsMirror(t *testing.T) {
	t.Run("copy keeps local", func(t *testing.T) {
		f := newEngineFixture(t, ModeCopy)
		if _, err := f.run(buildRemote(map[string]string{"/a.txt": "v1"}, nil)); err != nil {
			t.Fatalf("first Run: %v", err)
		}
		if _, err := f.run(buildRemote(nil, nil)); err != nil {
			t.Fatalf("second Run: %v", err)
		}
		f.mustFile("a.txt", "v1")
		if len(f.managed.files) != 0 {
			t.Errorf("managed = %d entries, want 0 (unmanaged)", len(f.managed.files))
		}
	})
	t.Run("mirror deletes managed only", func(t *testing.T) {
		f := newEngineFixture(t, ModeMirror)
		if _, err := f.run(buildRemote(map[string]string{"/a.txt": "v1"}, nil)); err != nil {
			t.Fatalf("first Run: %v", err)
		}
		// 手动创建的未知文件。
		if err := os.WriteFile(filepath.Join(f.root, "unknown.txt"), []byte("mine"), 0o644); err != nil {
			t.Fatalf("seed unknown: %v", err)
		}
		if _, err := f.run(buildRemote(nil, nil)); err != nil {
			t.Fatalf("second Run: %v", err)
		}
		if _, err := os.Stat(filepath.Join(f.root, "a.txt")); !os.IsNotExist(err) {
			t.Errorf("managed local a.txt still exists, stat err = %v", err)
		}
		data, err := os.ReadFile(filepath.Join(f.root, "unknown.txt"))
		if err != nil || string(data) != "mine" {
			t.Errorf("unknown.txt = %q (%v), want preserved", data, err)
		}
		if len(f.managed.files) != 0 {
			t.Errorf("managed = %d entries, want 0", len(f.managed.files))
		}
	})
}

// selector 排除仍存在的远端文件：释放管理、保留本地（Mirror 亦然）。
func TestRunSelectorExcludedReleases(t *testing.T) {
	f := newEngineFixture(t, ModeMirror)
	remote := buildRemote(map[string]string{"/a.txt": "v1", "/b.txt": "keep"}, nil)
	f.job.Include = []string{"*.txt"}
	if _, err := f.run(remote); err != nil {
		t.Fatalf("first Run: %v", err)
	}

	f.job.Exclude = []string{"a.txt"}
	if _, err := f.run(remote); err != nil {
		t.Fatalf("second Run: %v", err)
	}
	f.mustFile("a.txt", "v1") // 本地保留
	f.mustFile("b.txt", "keep")
	if _, managed := f.managed.files["/a.txt"]; managed {
		t.Error("a.txt still managed after exclusion, want released")
	}
	if _, managed := f.managed.files["/b.txt"]; !managed {
		t.Error("b.txt should remain managed")
	}
}

// download 目标被未知本地文件占用：跳过且不覆盖。
func TestRunNeverOverwritesUnknownLocal(t *testing.T) {
	f := newEngineFixture(t, ModeMirror)
	if err := os.WriteFile(filepath.Join(f.root, "a.txt"), []byte("mine"), 0o644); err != nil {
		t.Fatalf("seed unknown: %v", err)
	}
	remote := buildRemote(map[string]string{"/a.txt": "remote-content"}, nil)

	stats, err := f.run(remote)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	f.mustFile("a.txt", "mine")
	if stats.FilesSkipped != 1 || stats.FilesCreated != 0 {
		t.Errorf("stats = %+v, want 1 skipped / 0 created", stats)
	}
}

// 远端扫描失败：Mirror 直接中止，零删除——本地文件与 metadata 原样。
func TestRunScanFailureAbortsZeroDelete(t *testing.T) {
	f := newEngineFixture(t, ModeMirror)
	if _, err := f.run(buildRemote(map[string]string{"/a.txt": "v1"}, nil)); err != nil {
		t.Fatalf("first Run: %v", err)
	}

	broken := buildRemote(nil, nil)
	broken.listErr = errorsNew("connection reset")
	_, err := f.run(broken)
	if err == nil {
		t.Fatal("Run with scan failure = nil, want error")
	}
	f.mustFile("a.txt", "v1")
	if len(f.managed.files) != 1 {
		t.Errorf("managed entries = %d, want 1 (untouched)", len(f.managed.files))
	}
}

// 传输失败：本轮不执行后续 Mirror 删除，保持可恢复状态。
func TestRunTransferFailureForbidsDelete(t *testing.T) {
	f := newEngineFixture(t, ModeMirror)
	if _, err := f.run(buildRemote(map[string]string{"/a.txt": "v1", "/b.txt": "old"}, nil)); err != nil {
		t.Fatalf("first Run: %v", err)
	}

	// b.txt 远端更新但内容不可读（Open 失败）；a.txt 远端消失。
	broken := &engineRemote{
		entries:   map[string][]source.FileInfo{"/": {}},
		contents:  map[string]string{},
		overrides: map[string]io.ReadCloser{},
	}
	// 重建与上一轮相同路径但更新的条目，Open 时报错。
	broken.entries["/"] = []source.FileInfo{{
		Path:        "/b.txt",
		Fingerprint: source.Fingerprint{Size: 9, ModifiedAt: time.Unix(1757879402, 0).UTC(), ETag: `"changed"`},
	}}

	_, err := f.run(broken)
	if err == nil {
		t.Fatal("Run with transfer failure = nil, want error")
	}
	f.mustFile("a.txt", "v1")  // remote delete 未执行
	f.mustFile("b.txt", "old") // update 失败不破坏旧内容
	if len(f.managed.files) != 2 {
		t.Errorf("managed entries = %d, want 2 (untouched)", len(f.managed.files))
	}
}

// context 取消：中止且不产生删除。
func TestRunContextCancel(t *testing.T) {
	f := newEngineFixture(t, ModeMirror)
	if _, err := f.run(buildRemote(map[string]string{"/a.txt": "v1"}, nil)); err != nil {
		t.Fatalf("first Run: %v", err)
	}
	remote := buildRemote(map[string]string{"/a.txt": "v2"}, nil)
	remote.overrides["/a.txt"] = hangingReader{}

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	if _, err := Run(ctx, RunOptions{Remote: remote, Job: f.job, Managed: f.managed}); err == nil {
		t.Fatal("Run with canceled ctx = nil, want error")
	}
	f.mustFile("a.txt", "v1")
}

// 传输中断后的下一轮必须继续收敛：失败那轮已把 managed 记为
// pending(v2)，本地仍是 v1；恢复后 pending 绝不能被 skip，
// 必须重新下载 v2 并推进 synced。
func TestRunRecoversAfterInterruptedUpdate(t *testing.T) {
	f := newEngineFixture(t, ModeMirror)
	if _, err := f.run(buildRemote(map[string]string{"/a.txt": "v1"}, nil)); err != nil {
		t.Fatalf("first Run: %v", err)
	}

	// remote → v2，Open 持续报错（跨重试）：下载失败。
	remote := buildRemote(map[string]string{"/a.txt": "v2"}, nil)
	remote.openErrs["/a.txt"] = errorsNew("read failed mid-transfer")
	if _, err := f.run(remote); err == nil {
		t.Fatal("Run with broken transfer = nil, want error")
	}
	f.mustFile("a.txt", "v1") // 原子下载保证旧内容完好
	m, ok := f.managed.files["/a.txt"]
	if !ok || m.State != StatePending {
		t.Fatalf("managed after failure = %+v, want pending", m)
	}
	if m.Remote.ETag != etagOf("v2") {
		t.Errorf("managed remote etag = %q, want v2 etag (pending 登记)", m.Remote.ETag)
	}

	// 恢复：远端可读。pending 强制重传，绝不能 skip。
	stats, err := f.run(buildRemote(map[string]string{"/a.txt": "v2"}, nil))
	if err != nil {
		t.Fatalf("recovery Run: %v", err)
	}
	if stats.FilesUpdated != 1 || stats.FilesSkipped != 0 {
		t.Errorf("recovery stats = %+v, want 1 updated / 0 skipped", stats)
	}
	f.mustFile("a.txt", "v2")
	m = f.managed.files["/a.txt"]
	if m.State != StateSynced {
		t.Errorf("managed after recovery = %s, want synced", m.State)
	}
}

// Mirror 删除前校验既有路径组件：managed 文件的父目录被替换为
// symlink 时必须拒绝执行——lexical 路径落在 LocalRoot 内不代表
// 解析后的真实目标也在内；失败保留 metadata，LocalRoot 外文件完好。
func TestRunMirrorDeleteRejectsParentSymlink(t *testing.T) {
	f := newEngineFixture(t, ModeMirror)
	// 预置 managed：/link/a.txt → 本地 link/a.txt（synced）。
	f.managed.files["/link/a.txt"] = ManagedFile{
		JobID:        f.job.ID,
		RemotePath:   "/link/a.txt",
		LocalRelPath: "link/a.txt",
		State:        StateSynced,
		Remote:       source.Fingerprint{Size: 8, ETag: `"seeded"`},
	}

	// 本地：root/link 指向 root 之外的目录，真实文件在那里。
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatalf("mkdir outside: %v", err)
	}
	if err := os.WriteFile(filepath.Join(outside, "a.txt"), []byte("precious"), 0o644); err != nil {
		t.Fatalf("seed outside file: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(f.root, "link")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	// 远端已无 /link/a.txt：Mirror 计划删除，但父目录是 symlink → 拒绝。
	if _, err := f.run(buildRemote(nil, nil)); err == nil {
		t.Fatal("Run with parent symlink on delete path = nil, want error")
	}
	data, err := os.ReadFile(filepath.Join(outside, "a.txt"))
	if err != nil || string(data) != "precious" {
		t.Fatalf("outside file = %q (%v), want untouched precious", data, err)
	}
	if _, ok := f.managed.files["/link/a.txt"]; !ok {
		t.Error("managed metadata removed despite rejected delete, want preserved")
	}
}

// hangingReader 阻塞读取直到 Close，用于模拟慢传输。
type hangingReader struct{}

func (hangingReader) Read(p []byte) (int, error) {
	time.Sleep(10 * time.Millisecond)
	return 0, context.DeadlineExceeded
}

func (hangingReader) Close() error { return nil }

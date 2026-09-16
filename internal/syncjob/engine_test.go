package syncjob

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
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
		// 模拟 response body 绑定 request context：reader 在 Open 时
		// 拿到本次下载的 ctx，取消沿 ctx 传播到读取。
		if binder, ok := rc.(ctxAware); ok {
			binder.bindCtx(ctx)
		}
		return rc, nil
	}
	if content, ok := e.contents[path]; ok {
		return io.NopCloser(strings.NewReader(content)), nil
	}
	return nil, errorsNew("no such remote file " + path)
}

func (e *engineRemote) Close() error {
	return nil
}

// errorsNew 是 errors.New 的短别名，保持测试表格紧凑。
func errorsNew(msg string) error { return &errString{msg} }

type errString struct{ msg string }

func (e *errString) Error() string { return e.msg }

// inMemoryManaged 是 ManagedRepository 的内存实现；多 Job 并行运行时
// 会被并发读写（生产路径由 SQLite 串行化），用锁保证测试桩线程安全。
type inMemoryManaged struct {
	mu    sync.Mutex
	files map[string]ManagedFile // key: remotePath（单 Job 测试）
}

func newInMemoryManaged() *inMemoryManaged {
	return &inMemoryManaged{files: map[string]ManagedFile{}}
}

func (m *inMemoryManaged) ListByJob(ctx context.Context, jobID string) ([]ManagedFile, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var list []ManagedFile
	for _, f := range m.files {
		list = append(list, f)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].RemotePath < list[j].RemotePath })
	return list, nil
}

func (m *inMemoryManaged) Upsert(ctx context.Context, files []ManagedFile) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, f := range files {
		f.UpdatedAt = time.Now().UTC()
		m.files[f.RemotePath] = f
	}
	return nil
}

func (m *inMemoryManaged) Delete(ctx context.Context, jobID string, remotePaths []string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, p := range remotePaths {
		delete(m.files, p)
	}
	return nil
}

func (m *inMemoryManaged) DeleteAllForJob(ctx context.Context, jobID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
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

// memItems 是 ItemRecorder 的内存实现。
type memItems struct {
	mu    sync.Mutex
	items []RunItem
}

func (m *memItems) RecordItem(ctx context.Context, item RunItem) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.items = append(m.items, item)
	return nil
}

func (m *memItems) all() []RunItem {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]RunItem(nil), m.items...)
}

// runWith 用自定义 RunOptions 执行一轮同步。
func (f *engineFixture) runWith(remote source.Remote, adjust func(*RunOptions)) (RunStats, error) {
	f.t.Helper()
	options := RunOptions{Remote: remote, Job: f.job, Managed: f.managed}
	if adjust != nil {
		adjust(&options)
	}
	return Run(context.Background(), options)
}

// 文件级明细：变化文件写 create/update/delete/relinquish 条目，
// unchanged 文件不产生明细。
func TestRunRecordsItemsOnlyForChanges(t *testing.T) {
	f := newEngineFixture(t, ModeMirror)
	items := &memItems{}

	// 首轮：a.txt / b.txt 下载。
	remote := buildRemote(map[string]string{"/a.txt": "v1", "/b.txt": "keep"}, nil)
	if _, err := f.runWith(remote, func(o *RunOptions) { o.Items = items; o.RunID = "run_1" }); err != nil {
		t.Fatalf("first Run: %v", err)
	}
	created := items.all()
	if len(created) != 2 {
		t.Fatalf("first run items = %+v, want 2 create entries", created)
	}
	for _, item := range created {
		if item.RunID != "run_1" || item.Action != ItemCreate || item.Status != ItemSucceeded || item.Bytes == 0 {
			t.Errorf("item = %+v, want succeeded create with run_1", item)
		}
	}

	// 第二轮全部 unchanged：零明细。
	if _, err := f.runWith(remote, func(o *RunOptions) { o.Items = items; o.RunID = "run_2" }); err != nil {
		t.Fatalf("unchanged Run: %v", err)
	}
	if got := len(items.all()); got != 2 {
		t.Fatalf("items after unchanged run = %d, want 2 (no entries for unchanged)", got)
	}

	// 第三轮：a.txt 更新；排除 b.txt（relinquish）。
	remote.contents["/a.txt"] = "v2-long"
	setFingerprint(remote, "/a.txt", "v2-long", 1757879401)
	f.job.Exclude = []string{"b.txt"}
	if _, err := f.runWith(remote, func(o *RunOptions) { o.Items = items; o.RunID = "run_3" }); err != nil {
		t.Fatalf("third Run: %v", err)
	}
	var updated, relinquished int
	for _, item := range items.all() {
		if item.RunID != "run_3" {
			continue
		}
		switch item.Action {
		case ItemUpdate:
			updated++
			if item.Path != "a.txt" || item.Status != ItemSucceeded {
				t.Errorf("update item = %+v, want a.txt succeeded", item)
			}
		case ItemRelinquish:
			relinquished++
			if item.Path != "b.txt" || item.Status != ItemSucceeded {
				t.Errorf("relinquish item = %+v, want b.txt succeeded", item)
			}
		}
	}
	if updated != 1 || relinquished != 1 {
		t.Errorf("third run items = (%d updated, %d relinquished), want (1, 1)", updated, relinquished)
	}

	// 第四轮：远端只保留 b.txt（继续排除），a.txt 远端消失且仍受管
	// → Mirror 删除产生 delete 明细。
	f.job.Exclude = []string{"b.txt"}
	remote.entries["/"] = []source.FileInfo{fileEntryOf("/b.txt", "keep", 1757879400)}
	if _, err := f.runWith(remote, func(o *RunOptions) { o.Items = items; o.RunID = "run_4" }); err != nil {
		t.Fatalf("fourth Run: %v", err)
	}
	var deleted int
	for _, item := range items.all() {
		if item.RunID == "run_4" && item.Action == ItemDelete {
			deleted++
			if item.Status != ItemSucceeded {
				t.Errorf("delete item = %+v, want succeeded", item)
			}
		}
	}
	if deleted != 1 {
		t.Errorf("delete items = %d, want 1", deleted)
	}
}

// download 目标冲突记 skipped 明细：文件不覆盖且可追溯。
func TestRunRecordsConflictItem(t *testing.T) {
	f := newEngineFixture(t, ModeMirror)
	if err := os.WriteFile(filepath.Join(f.root, "a.txt"), []byte("mine"), 0o644); err != nil {
		t.Fatalf("seed unknown: %v", err)
	}
	remote := buildRemote(map[string]string{"/a.txt": "remote"}, nil)
	items := &memItems{}

	if _, err := f.runWith(remote, func(o *RunOptions) { o.Items = items }); err != nil {
		t.Fatalf("Run: %v", err)
	}
	got := items.all()
	if len(got) != 1 || got[0].Action != ItemCreate || got[0].Status != ItemSkipped || got[0].Error == "" {
		t.Fatalf("items = %+v, want one skipped create with reason", got)
	}
}

// 传输失败：失败文件记 failed 明细；未派发文件不登记 pending；
// relinquish 与 Mirror delete 不执行。
func TestRunTransferFailureItemsAndSafety(t *testing.T) {
	f := newEngineFixture(t, ModeMirror)
	if _, err := f.run(buildRemote(map[string]string{"/a.txt": "v1", "/b.txt": "old"}, nil)); err != nil {
		t.Fatalf("first Run: %v", err)
	}

	// b.txt 更新但 Open 失败；a.txt 远端消失（Mirror 待删除）。
	broken := &engineRemote{
		entries:   map[string][]source.FileInfo{"/": {}},
		contents:  map[string]string{},
		overrides: map[string]io.ReadCloser{},
		openErrs:  map[string]error{},
	}
	broken.entries["/"] = []source.FileInfo{{
		Path:        "/b.txt",
		Fingerprint: source.Fingerprint{Size: 9, ModifiedAt: time.Unix(1757879402, 0).UTC(), ETag: `"changed"`},
	}}
	items := &memItems{}

	_, err := f.runWith(broken, func(o *RunOptions) {
		o.Items = items
		o.Transfers = NewTransferLimiter(1)
	})
	if err == nil {
		t.Fatal("Run with transfer failure = nil, want error")
	}
	got := items.all()
	if len(got) != 1 || got[0].Action != ItemUpdate || got[0].Status != ItemFailed || got[0].Error == "" {
		t.Fatalf("items = %+v, want one failed update entry", got)
	}
	f.mustFile("a.txt", "v1")  // remote delete 未执行
	f.mustFile("b.txt", "old") // update 失败不破坏旧内容
	if len(f.managed.files) != 2 {
		t.Errorf("managed entries = %d, want 2 (untouched)", len(f.managed.files))
	}
}

// 并发上限：单轮 Run 同时进行的远端下载不超过注入的 TransferLimiter
// 容量，全部文件仍传输成功。
func TestRunTransferConcurrencyCapped(t *testing.T) {
	f := newEngineFixture(t, ModeCopy)
	files := map[string]string{}
	for _, name := range []string{"/a", "/b", "/c", "/d", "/e"} {
		files[name] = "content-of" + name
	}
	probe := &probeRemote{
		engineRemote: buildRemote(files, nil),
		delay:        40 * time.Millisecond,
	}

	stats, err := f.runWith(probe, func(o *RunOptions) { o.Transfers = NewTransferLimiter(2) })
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if stats.FilesCreated != 5 {
		t.Errorf("stats = %+v, want 5 created", stats)
	}
	if probe.maxSeen > 2 {
		t.Errorf("concurrent Open peak = %d, want <= 2", probe.maxSeen)
	}
	if probe.maxSeen < 2 {
		t.Errorf("concurrent Open peak = %d, want overlap observed (>= 2)", probe.maxSeen)
	}
}

// setFingerprint 按路径更新远端条目的指纹（entries 来自 map 遍历，顺序随机）。
func setFingerprint(r *engineRemote, path, content string, mtime int64) {
	for i := range r.entries["/"] {
		if r.entries["/"][i].Path == path {
			r.entries["/"][i].Fingerprint = source.Fingerprint{
				Size:       int64(len(content)),
				ModifiedAt: time.Unix(mtime, 0).UTC(),
				ETag:       etagOf(content),
			}
			return
		}
	}
	panic("setFingerprint: missing entry " + path)
}

// fileEntryOf 构造单个远端文件条目。
func fileEntryOf(path, content string, mtime int64) source.FileInfo {
	return source.FileInfo{
		Path: path,
		Fingerprint: source.Fingerprint{
			Size:       int64(len(content)),
			ModifiedAt: time.Unix(mtime, 0).UTC(),
			ETag:       etagOf(content),
		},
	}
}

// probeRemote 包装 engineRemote 并测量 Open 的并发峰值。
type probeRemote struct {
	*engineRemote
	mu      sync.Mutex
	current int
	maxSeen int
	delay   time.Duration
}

func (p *probeRemote) Open(ctx context.Context, path string) (io.ReadCloser, error) {
	p.mu.Lock()
	p.current++
	if p.current > p.maxSeen {
		p.maxSeen = p.current
	}
	p.mu.Unlock()
	time.Sleep(p.delay)
	p.mu.Lock()
	p.current--
	p.mu.Unlock()
	return p.engineRemote.Open(ctx, path)
}

// hangingReader 阻塞读取直到 Close，用于模拟慢传输。
type hangingReader struct{}

func (hangingReader) Read(p []byte) (int, error) {
	time.Sleep(10 * time.Millisecond)
	return 0, context.DeadlineExceeded
}

func (hangingReader) Close() error { return nil }

// ctxAware 是测试 reader 的可选能力：engineRemote.Open 时注入本次
// 下载的 ctx，模拟 response body 绑定 request context 的取消传播。
type ctxAware interface {
	bindCtx(context.Context)
}

// cancelAwareReader 模拟绑定 request context 的远端响应体：Read 阻塞
// 直到放行或 ctx 取消；被 ctx 取消中断时关闭 cancelled 信号（供测试
// 断言传输真正被取消，而不是默默继续下载）。
type cancelAwareReader struct {
	ctx       context.Context // Open 时注入
	rel       chan struct{}
	cancelled chan struct{}
}

func (r *cancelAwareReader) bindCtx(ctx context.Context) { r.ctx = ctx }

func (r *cancelAwareReader) Read(p []byte) (int, error) {
	select {
	case <-r.rel:
		return 0, io.EOF
	case <-r.ctx.Done():
		close(r.cancelled)
		return 0, r.ctx.Err()
	}
}

func (r *cancelAwareReader) Close() error { return nil }

// 首次传输失败必须真正取消在途下载：失败后不再派发新任务，在途
// worker 经传输 context 中断（不再继续下载完整文件），整体失败，
// 被取消的文件不落地。
func TestRunTransferCancellationAfterFirstFailure(t *testing.T) {
	f := newEngineFixture(t, ModeCopy)
	runCtx, cancelRun := context.WithCancel(context.Background())
	defer cancelRun()

	remote := buildRemote(map[string]string{"/a.txt": "v1", "/b.txt": "v2"}, nil)
	remote.openErrs["/a.txt"] = errorsNew("read reset by peer")
	b := &cancelAwareReader{rel: make(chan struct{}), cancelled: make(chan struct{})}
	// 直接放 reader 本体（不可包一层 NopCloser，否则丢失 ctxAware）。
	remote.overrides["/b.txt"] = b

	_, err := Run(runCtx, RunOptions{
		Remote:    remote,
		Job:       f.job,
		Managed:   f.managed,
		Transfers: NewTransferLimiter(2),
	})
	if err == nil || !strings.Contains(err.Error(), "a.txt") {
		t.Fatalf("Run err = %v, want transfer failure mentioning a.txt", err)
	}
	select {
	case <-b.cancelled:
	default:
		t.Fatal("in-flight download of b.txt was not cancelled after a.txt failed")
	}
	if _, statErr := os.Lstat(filepath.Join(f.root, "b.txt")); !os.IsNotExist(statErr) {
		t.Errorf("b.txt exists after cancelled transfer (%v), want absent", statErr)
	}
}

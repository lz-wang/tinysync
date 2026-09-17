package browser

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	"tinysync/internal/filesafe"
	"tinysync/internal/source"
	"tinysync/internal/syncjob"
)

// localFixture 是本地浏览测试环境：Job 的 LocalRoot 指向临时目录，
// managed 记录可编程。
type localFixture struct {
	root    string
	svc     *LocalService
	jobID   string
	managed *memManagedRepo
}

// memJobRepo 是最小化的内存 syncjob.Repository：只支持 Create / Get。
type memJobRepo struct {
	jobs map[string]syncjob.Job
}

func newMemJobRepo(jobs ...syncjob.Job) *memJobRepo {
	m := &memJobRepo{jobs: map[string]syncjob.Job{}}
	for _, j := range jobs {
		m.jobs[j.ID] = j
	}
	return m
}

func (m *memJobRepo) Create(ctx context.Context, job syncjob.Job) error {
	m.jobs[job.ID] = job
	return nil
}

func (m *memJobRepo) Get(ctx context.Context, id string) (syncjob.Job, error) {
	job, ok := m.jobs[id]
	if !ok {
		return syncjob.Job{}, fmt.Errorf("%w: %s", syncjob.ErrNotFound, id)
	}
	return job, nil
}

func (m *memJobRepo) List(ctx context.Context) ([]syncjob.Job, error) {
	return nil, errors.New("not implemented")
}

func (m *memJobRepo) Update(ctx context.Context, job syncjob.Job) error {
	return errors.New("not implemented")
}

func (m *memJobRepo) UpdateAndResetManaged(ctx context.Context, job syncjob.Job) error {
	return errors.New("not implemented")
}

func (m *memJobRepo) Delete(ctx context.Context, id string) error {
	return errors.New("not implemented")
}

func (m *memJobRepo) CountBySource(ctx context.Context, sourceID string) (int, error) {
	return 0, errors.New("not implemented")
}

// memManagedRepo 是内存 ManagedRepository，只支持 ListByJob。
type memManagedRepo struct {
	files []syncjob.ManagedFile
}

func (m *memManagedRepo) ListByJob(ctx context.Context, jobID string) ([]syncjob.ManagedFile, error) {
	return m.files, nil
}

func (m *memManagedRepo) Upsert(ctx context.Context, files []syncjob.ManagedFile) error {
	return errors.New("not implemented")
}

func (m *memManagedRepo) Delete(ctx context.Context, jobID string, remotePaths []string) error {
	return errors.New("not implemented")
}

func (m *memManagedRepo) DeleteAllForJob(ctx context.Context, jobID string) error {
	return errors.New("not implemented")
}

// newLocalFixture 构造 Job：LocalRoot 指向临时目录。Job 经内存
// Repository 直接落库（Service.Create 会校验 Source 存在，浏览测试
// 不涉及该依赖），LocalRoot 传入 canonical 形态与领域规则一致。
func newLocalFixture(t *testing.T) *localFixture {
	t.Helper()
	f := &localFixture{root: t.TempDir()}
	// LocalRoot 与 Job 创建路径保持同一 canonicalize 规则。
	canonical, err := filepath.EvalSymlinks(f.root)
	if err != nil {
		t.Fatalf("evalsymlinks root: %v", err)
	}
	f.root = canonical

	f.managed = &memManagedRepo{}
	now := time.Now().UTC()
	job := syncjob.Job{
		ID:        "job_browse_it",
		Name:      "browse-it",
		Mode:      syncjob.ModeCopy,
		LocalRoot: f.root,
		Enabled:   true,
		CreatedAt: now,
		UpdatedAt: now,
	}
	repo := newMemJobRepo(job)
	jobs := syncjob.NewService(repo, nil, t.TempDir())
	f.jobID = job.ID
	f.svc = NewLocalService(jobs, f.managed)
	return f
}

func (f *localFixture) write(t *testing.T, rel, content string) {
	t.Helper()
	abs := filepath.Join(f.root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", abs, err)
	}
	if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", abs, err)
	}
}

func (f *localFixture) setManaged(t *testing.T, relPaths ...string) {
	t.Helper()
	files := make([]syncjob.ManagedFile, 0, len(relPaths))
	for _, p := range relPaths {
		files = append(files, syncjob.ManagedFile{JobID: f.jobID, LocalRelPath: p})
	}
	f.managed.files = files
}

// 列目录返回 kind 分类与 managed 标记；unmanaged 条目正确呈现。
func TestLocalServiceList(t *testing.T) {
	fx := newLocalFixture(t)
	fx.write(t, "managed.txt", "m")
	fx.write(t, "unmanaged.txt", "u")
	fx.write(t, "sub/inner.txt", "i")
	fx.setManaged(t, "managed.txt")

	entries, next, err := fx.svc.List(context.Background(), fx.jobID, "/", source.ListOptions{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if next != "" {
		t.Errorf("next = %q, want empty", next)
	}
	byName := map[string]Entry{}
	for _, e := range entries {
		byName[e.Path] = e
	}
	m := byName["/managed.txt"]
	if m.Kind != KindFile || m.Managed == nil || !*m.Managed {
		t.Errorf("managed.txt = %+v, want file managed=true", m)
	}
	u := byName["/unmanaged.txt"]
	if u.Kind != KindFile || u.Managed == nil || *u.Managed {
		t.Errorf("unmanaged.txt = %+v, want file managed=false", u)
	}
	if d := byName["/sub"]; d.Kind != KindDirectory {
		t.Errorf("sub = %+v, want directory", d)
	}
}

// 分页：limit 切片、cursor 续页、EOF 语义。
func TestLocalServiceListPagination(t *testing.T) {
	fx := newLocalFixture(t)
	for _, name := range []string{"a", "b", "c", "d", "e"} {
		fx.write(t, name+".txt", name)
	}
	var got []string
	cursor := ""
	pages := 0
	for {
		entries, next, err := fx.svc.List(context.Background(), fx.jobID, "/", source.ListOptions{Limit: 2, Cursor: cursor})
		if err != nil {
			t.Fatalf("List page %d: %v", pages, err)
		}
		if len(entries) > 2 {
			t.Fatalf("page %d entries = %d, want <= 2", pages, len(entries))
		}
		for _, e := range entries {
			got = append(got, e.Path)
		}
		pages++
		if next == "" {
			break
		}
		cursor = next
	}
	if pages != 3 || len(got) != 5 {
		t.Fatalf("pages = %d entries = %v, want 3 pages 5 entries", pages, got)
	}
	seen := map[string]bool{}
	for _, p := range got {
		if seen[p] {
			t.Errorf("duplicated entry %s", p)
		}
		seen[p] = true
	}
}

// 专项安全场景：traversal 拒绝、symlink 不跟随、兄弟前缀不越界。
func TestLocalServiceConfinement(t *testing.T) {
	fx := newLocalFixture(t)
	fx.write(t, "inside.txt", "ok")
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("s"), 0o644); err != nil {
		t.Fatalf("write outside: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(fx.root, "outside-link")); err != nil {
		t.Fatalf("symlink outside: %v", err)
	}
	if err := os.Symlink("inside.txt", filepath.Join(fx.root, "inside-link.txt")); err != nil {
		t.Fatalf("symlink inside: %v", err)
	}
	if err := os.Symlink(filepath.Join(fx.root, "no-such"), filepath.Join(fx.root, "broken-link")); err != nil {
		t.Fatalf("symlink broken: %v", err)
	}

	// traversal：dot segments 在逻辑路径校验层拒绝。
	for _, p := range []string{"/../../../etc/passwd", "/../outside/secret.txt", "/a/../b", "/a//b", "relative"} {
		if _, err := fx.svc.Stat(context.Background(), fx.jobID, p); err == nil || errors.Is(err, fs.ErrNotExist) {
			t.Errorf("Stat(%q) = %v, want rejection (not missing-file)", p, err)
		}
	}

	// symlink：可见（kind=symlink）但不可 Open / 不可当作目录进入。
	for _, link := range []string{"/outside-link", "/inside-link.txt", "/broken-link"} {
		entry, err := fx.svc.Stat(context.Background(), fx.jobID, link)
		if err != nil {
			t.Fatalf("Stat(%s): %v", link, err)
		}
		if entry.Kind != KindSymlink {
			t.Errorf("Stat(%s) kind = %s, want symlink", link, entry.Kind)
		}
		if _, _, _, err := fx.svc.Open(context.Background(), fx.jobID, link); !errors.Is(err, filesafe.ErrNotRegularFile) {
			t.Errorf("Open(%s) error = %v, want ErrNotRegularFile", link, err)
		}
	}
	// symlink 目录：List 穿过它不会发生——List 只列目录一层，进入
	// symlink 目录时 ResolveWithinRoot 的目标本身可以列吗？不行：
	// symlink 指向目录在 ReadDir 层面有效，但 confinement 语义拒绝。
	entries, _, err := fx.svc.List(context.Background(), fx.jobID, "/outside-link", source.ListOptions{})
	if err == nil {
		t.Errorf("List through symlinked dir = %d entries, want rejection", len(entries))
	}

	// 父目录 symlink 逃逸（Open 路径）：outside-link/secret.txt 经
	// EvalSymlinks 解析后越出 root，必须拒绝（且不是「文件不存在」）。
	if _, _, _, err := fx.svc.Open(context.Background(), fx.jobID, "/outside-link/secret.txt"); err == nil {
		t.Error("Open through parent symlink escape = nil error, want escape rejection")
	} else if errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Open escape = %v, want confinement error rather than missing-file", err)
	}

	// 目录请求下载 → ErrNotRegularFile。
	fx.write(t, "sub/x.txt", "x")
	if _, _, _, err := fx.svc.Open(context.Background(), fx.jobID, "/sub"); !errors.Is(err, filesafe.ErrNotRegularFile) {
		t.Errorf("Open directory error = %v, want ErrNotRegularFile", err)
	}
}

// Open 返回真实文件内容与元信息；Stat 不存在 → fs.ErrNotExist（404）。
func TestLocalServiceOpenAndStat(t *testing.T) {
	fx := newLocalFixture(t)
	fx.write(t, "docs/a.txt", "hello-local")
	fx.setManaged(t, "docs/a.txt")

	name, file, info, err := fx.svc.Open(context.Background(), fx.jobID, "/docs/a.txt")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = file.Close() }()
	if name != "a.txt" || info.Size() != 11 {
		t.Errorf("open = %q size %d, want a.txt size 11", name, info.Size())
	}
	data, err := io.ReadAll(file)
	if err != nil || string(data) != "hello-local" {
		t.Errorf("read = %q, %v; want hello-local", data, err)
	}

	entry, err := fx.svc.Stat(context.Background(), fx.jobID, "/docs/a.txt")
	if err != nil || entry.Kind != KindFile || entry.Size != 11 || !*entry.Managed {
		t.Errorf("stat = %+v, %v; want managed file size 11", entry, err)
	}

	if _, err := fx.svc.Stat(context.Background(), fx.jobID, "/docs/missing.txt"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("stat missing = %v, want fs.ErrNotExist", err)
	}
}

// Job 不存在 → ErrNotFound；非法路径 → ErrInvalid。
func TestLocalServiceInputErrors(t *testing.T) {
	fx := newLocalFixture(t)
	if _, err := fx.svc.Stat(context.Background(), "no-such-job", "/"); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing job error = %v, want ErrNotFound", err)
	}
	if _, err := fx.svc.Stat(context.Background(), fx.jobID, "/../x"); !errors.Is(err, ErrInvalid) {
		t.Errorf("invalid path error = %v, want ErrInvalid", err)
	}
	if _, _, err := fx.svc.List(context.Background(), "no-such-job", "/", source.ListOptions{}); !errors.Is(err, ErrNotFound) {
		t.Errorf("list missing job error = %v, want ErrNotFound", err)
	}
}

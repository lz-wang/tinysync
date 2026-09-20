package share

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"tinysync/internal/source"
	"tinysync/internal/syncjob"
)

// memRepo 是内存 Repository：行为覆盖 Create/Update 冲突与 Get/List。
type memRepo struct {
	shares map[string]Share
}

func newMemRepo() *memRepo {
	return &memRepo{shares: map[string]Share{}}
}

func (m *memRepo) Create(ctx context.Context, s Share) error {
	for _, existing := range m.shares {
		if existing.Slug == s.Slug {
			return fmt.Errorf("%w: %s", ErrConflict, s.Slug)
		}
	}
	m.shares[s.ID] = s
	return nil
}

func (m *memRepo) Get(ctx context.Context, id string) (Share, error) {
	s, ok := m.shares[id]
	if !ok {
		return Share{}, fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	return s, nil
}

func (m *memRepo) GetBySlug(ctx context.Context, slug string) (Share, error) {
	for _, s := range m.shares {
		if s.Slug == slug {
			return s, nil
		}
	}
	return Share{}, fmt.Errorf("%w: %s", ErrNotFound, slug)
}

func (m *memRepo) List(ctx context.Context) ([]Share, error) {
	var list []Share
	for _, s := range m.shares {
		list = append(list, s)
	}
	return list, nil
}

func (m *memRepo) Update(ctx context.Context, s Share) error {
	if _, ok := m.shares[s.ID]; !ok {
		return fmt.Errorf("%w: %s", ErrNotFound, s.ID)
	}
	for id, existing := range m.shares {
		if id != s.ID && existing.Slug == s.Slug {
			return fmt.Errorf("%w: %s", ErrConflict, s.Slug)
		}
	}
	m.shares[s.ID] = s
	return nil
}

func (m *memRepo) Delete(ctx context.Context, id string) error {
	if _, ok := m.shares[id]; !ok {
		return fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	delete(m.shares, id)
	return nil
}

// memJobs 是内存 Job 读取桩。
type memJobs struct {
	job syncjob.Job
}

func (m *memJobs) Get(ctx context.Context, id string) (syncjob.Job, error) {
	if m.job.ID != id {
		return syncjob.Job{}, fmt.Errorf("%w: %s", syncjob.ErrNotFound, id)
	}
	return m.job, nil
}

// serviceEnv 组装 Service 与受控环境。
type serviceEnv struct {
	svc   *Service
	repo  *memRepo
	jobs  *memJobs
	root  string
	jobID string
	now   time.Time
}

func newServiceEnv(t *testing.T) *serviceEnv {
	t.Helper()
	env := &serviceEnv{
		repo: newMemRepo(),
		now:  time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC),
	}
	canonical, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("evalsymlinks: %v", err)
	}
	env.root = canonical
	env.jobID = "job_share"
	env.jobs = &memJobs{job: syncjob.Job{
		ID:        env.jobID,
		LocalRoot: canonical,
		Mode:      syncjob.ModeCopy,
	}}
	env.svc = NewService(env.repo, env.jobs)
	env.svc.Now = func() time.Time { return env.now }
	return env
}

func (e *serviceEnv) write(t *testing.T, rel, content string) {
	t.Helper()
	abs := filepath.Join(e.root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
}

// Create 完整校验链：文件 / 目录 / 本地根均放行并 canonical 落库；
// unmanaged 文件同样放行（ADR-0002）。
func TestCreateSharesFileDirectoryAndRoot(t *testing.T) {
	env := newServiceEnv(t)
	env.write(t, "photos/a.jpg", "jpeg")
	env.write(t, "private.txt", "secret")

	named, err := env.svc.Create(context.Background(), CreateInput{
		JobID: env.jobID, Path: "/photos/a.jpg", Name: "album", Enabled: true,
	})
	if err != nil {
		t.Fatalf("Create file: %v", err)
	}
	if named.ID == "" || !strings.HasPrefix(named.ID, "shr_") {
		t.Errorf("id = %q, want shr_ prefix", named.ID)
	}
	if named.LocalPath != filepath.Join(env.root, "photos", "a.jpg") || named.IsDir || named.Slug != "album" {
		t.Errorf("file share = %+v, want canonical file with slug album", named)
	}
	if named.Name == nil || *named.Name != "album" {
		t.Errorf("name = %+v, want album", named.Name)
	}

	dirShare, err := env.svc.Create(context.Background(), CreateInput{
		JobID: env.jobID, Path: "/photos", Enabled: true,
	})
	if err != nil || !dirShare.IsDir || dirShare.LocalPath != filepath.Join(env.root, "photos") {
		t.Fatalf("Create dir = %+v, %v; want canonical dir", dirShare, err)
	}
	if dirShare.Name != nil {
		t.Errorf("dir name = %+v, want nil", dirShare.Name)
	}
	if len(dirShare.Slug) != 10 {
		t.Errorf("random slug = %q, want 10 chars", dirShare.Slug)
	}

	rootShare, err := env.svc.Create(context.Background(), CreateInput{
		JobID: env.jobID, Path: "/", Enabled: true,
	})
	if err != nil || !rootShare.IsDir || rootShare.LocalPath != env.root {
		t.Fatalf("Create root = %+v, %v; want canonical root dir", rootShare, err)
	}

	// unmanaged 文件同样可共享。
	if _, err := env.svc.Create(context.Background(), CreateInput{
		JobID: env.jobID, Path: "/private.txt", Enabled: true,
	}); err != nil {
		t.Errorf("Create unmanaged file: %v", err)
	}
	if named.CreatedAt != env.now || named.UpdatedAt != env.now {
		t.Errorf("timestamps = %v/%v, want injected now", named.CreatedAt, named.UpdatedAt)
	}
}

// 不安全目标拒绝：逃逸 / 缺失 / symlink；名称非法与过去过期拒绝；
// Job 不存在 404 语义。
func TestCreateRejectsUnsafeTargets(t *testing.T) {
	env := newServiceEnv(t)
	env.write(t, "synced.txt", "data")
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("s"), 0o644); err != nil {
		t.Fatalf("write outside: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(env.root, "out-link")); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	if err := os.Symlink("synced.txt", filepath.Join(env.root, "alias.txt")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	cases := []struct {
		name  string
		input CreateInput
		byErr error
	}{
		{"traversal", CreateInput{JobID: env.jobID, Path: "/../x", Enabled: true}, source.ErrInvalid},
		{"missing target", CreateInput{JobID: env.jobID, Path: "/nope.txt", Enabled: true}, source.ErrInvalid},
		{"symlink inside root", CreateInput{JobID: env.jobID, Path: "/alias.txt", Enabled: true}, source.ErrInvalid},
		{"parent symlink escape", CreateInput{JobID: env.jobID, Path: "/out-link/secret.txt", Enabled: true}, source.ErrInvalid},
		{"dot in name", CreateInput{JobID: env.jobID, Path: "/synced.txt", Name: "a.b", Enabled: true}, source.ErrInvalid},
		{"leading dash name", CreateInput{JobID: env.jobID, Path: "/synced.txt", Name: "-abc", Enabled: true}, source.ErrInvalid},
		{"slash in name", CreateInput{JobID: env.jobID, Path: "/synced.txt", Name: "a/b", Enabled: true}, source.ErrInvalid},
		{"overlong name", CreateInput{JobID: env.jobID, Path: "/synced.txt", Name: strings.Repeat("a", 65), Enabled: true}, source.ErrInvalid},
		{"missing job", CreateInput{JobID: "no-such", Path: "/synced.txt", Enabled: true}, ErrNotFound},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := env.svc.Create(context.Background(), tc.input); !errors.Is(err, tc.byErr) {
				t.Errorf("Create error = %v, want %v", err, tc.byErr)
			}
		})
	}

	// 过期时刻在过去 → 400 语义。
	past := env.now.Add(-time.Hour)
	if _, err := env.svc.Create(context.Background(), CreateInput{
		JobID: env.jobID, Path: "/synced.txt", Enabled: true, ExpiresAt: &past,
	}); !errors.Is(err, source.ErrInvalid) {
		t.Errorf("past expiry error = %v, want ErrInvalid", err)
	}
}

// RandomSlug：长度、字符集与两次调用不重叠。
func TestRandomSlug(t *testing.T) {
	first, err := RandomSlug()
	if err != nil {
		t.Fatalf("RandomSlug: %v", err)
	}
	second, err := RandomSlug()
	if err != nil {
		t.Fatalf("RandomSlug: %v", err)
	}
	if len(first) != 10 || len(second) != 10 {
		t.Errorf("slug lengths = %d/%d, want 10", len(first), len(second))
	}
	if first == second {
		t.Errorf("two random slugs equal: %q", first)
	}
	for _, c := range first {
		if !strings.ContainsRune(slugAlphabet, c) {
			t.Errorf("slug %q contains non-base62 rune %q", first, c)
		}
	}
}

// Update 只允许 name（联动 slug）/ enabled / expires_at；local_path
// 与 is_dir 不可变；清名称不清 slug；slug 冲突 409。
func TestUpdateMutatesOnlyMutableFields(t *testing.T) {
	env := newServiceEnv(t)
	env.write(t, "photos/a.jpg", "jpeg")
	p, err := env.svc.Create(context.Background(), CreateInput{
		JobID: env.jobID, Path: "/photos/a.jpg", Enabled: true,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// 改名：slug 随之变化（旧链接失效）。
	rename := "album"
	updated, err := env.svc.Update(context.Background(), p.ID, UpdateInput{Name: &rename})
	if err != nil {
		t.Fatalf("Update name: %v", err)
	}
	if updated.Slug != "album" || updated.Name == nil || *updated.Name != "album" {
		t.Errorf("updated = %+v, want slug/name album", updated)
	}
	if updated.LocalPath != p.LocalPath || updated.IsDir != p.IsDir || !updated.CreatedAt.Equal(p.CreatedAt) {
		t.Errorf("immutable fields changed: %v -> %v", p, updated)
	}

	// 清除名称后 slug 保持不变。
	empty := ""
	updated, err = env.svc.Update(context.Background(), p.ID, UpdateInput{Name: &empty})
	if err != nil || updated.Name != nil {
		t.Fatalf("clear name = %+v, %v; want nil name", updated, err)
	}
	if updated.Slug != "album" {
		t.Errorf("after clear = %+v, want slug album", updated)
	}

	// 启停与过期。
	disabled := false
	if _, err := env.svc.Update(context.Background(), p.ID, UpdateInput{Enabled: &disabled}); err != nil {
		t.Fatalf("disable: %v", err)
	}
	expiry := env.now.Add(time.Hour)
	updated, err = env.svc.Update(context.Background(), p.ID, UpdateInput{ExpiresAt: &expiry})
	if err != nil || updated.ExpiresAt == nil || !updated.ExpiresAt.Equal(expiry) {
		t.Fatalf("set expiry = %+v, %v", updated, err)
	}
	updated, err = env.svc.Update(context.Background(), p.ID, UpdateInput{ClearExpires: true})
	if err != nil || updated.ExpiresAt != nil {
		t.Errorf("clear expiry = %+v, %v; want nil expires", updated, err)
	}
	if updated.Enabled {
		t.Error("enabled = true, want still disabled")
	}

	// slug 冲突 → 409。
	second, err := env.svc.Create(context.Background(), CreateInput{
		JobID: env.jobID, Path: "/photos/a.jpg", Name: "second", Enabled: true,
	})
	if err != nil {
		t.Fatalf("Create second: %v", err)
	}
	conflict := "album"
	if _, err := env.svc.Update(context.Background(), second.ID, UpdateInput{Name: &conflict}); !errors.Is(err, ErrConflict) {
		t.Errorf("conflict error = %v, want ErrConflict", err)
	}
}

// ResolveForRequest：禁用 / 过期一律 ErrNotFound，不区分原因。文件
// 系统状态（缺失、symlink 替换）不归本层判定，由公开访问时的
// filesafe 原语复验并同形映射 404。
func TestResolveForRequestHidesReasons(t *testing.T) {
	env := newServiceEnv(t)
	env.write(t, "synced.txt", "data")
	p, err := env.svc.Create(context.Background(), CreateInput{
		JobID: env.jobID, Path: "/synced.txt", Name: "a", Enabled: true,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if _, err := env.svc.ResolveForRequest(context.Background(), "a"); err != nil {
		t.Fatalf("resolve enabled = %v, want nil", err)
	}

	// 禁用。
	disabled := false
	if _, err := env.svc.Update(context.Background(), p.ID, UpdateInput{Enabled: &disabled}); err != nil {
		t.Fatalf("disable: %v", err)
	}
	if _, err := env.svc.ResolveForRequest(context.Background(), "a"); !errors.Is(err, ErrNotFound) {
		t.Errorf("resolve disabled = %v, want ErrNotFound", err)
	}

	// 过期（时钟推进）。
	enabled := true
	expiry := env.now.Add(time.Hour)
	if _, err := env.svc.Update(context.Background(), p.ID, UpdateInput{Enabled: &enabled, ExpiresAt: &expiry}); err != nil {
		t.Fatalf("set expiry: %v", err)
	}
	env.now = env.now.Add(2 * time.Hour)
	if _, err := env.svc.ResolveForRequest(context.Background(), "a"); !errors.Is(err, ErrNotFound) {
		t.Errorf("resolve expired = %v, want ErrNotFound", err)
	}

	// 未知 slug。
	if _, err := env.svc.ResolveForRequest(context.Background(), "never"); !errors.Is(err, ErrNotFound) {
		t.Errorf("resolve unknown = %v, want ErrNotFound", err)
	}
}

// Delete 只移除策略记录，不触及本地文件。
func TestDeleteKeepsLocalFile(t *testing.T) {
	env := newServiceEnv(t)
	env.write(t, "synced.txt", "data")
	p, err := env.svc.Create(context.Background(), CreateInput{
		JobID: env.jobID, Path: "/synced.txt", Enabled: true,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := env.svc.Delete(context.Background(), p.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := os.Stat(filepath.Join(env.root, "synced.txt")); err != nil {
		t.Errorf("local file stat = %v, want still present", err)
	}
	if _, err := env.svc.Get(context.Background(), p.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("get after delete = %v, want ErrNotFound", err)
	}
}

// Expired 的边界：到期时刻即视为过期。
func TestExpiredBoundary(t *testing.T) {
	at := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	s := Share{ExpiresAt: &at}
	if s.Expired(at.Add(-time.Second)) {
		t.Error("one second before expiry = expired, want active")
	}
	if !s.Expired(at) {
		t.Error("at expiry = active, want expired")
	}
	if (Share{}).Expired(at.Add(time.Hour)) {
		t.Error("nil expiry = expired, want never")
	}
}

// ValidateCreateInput 的格式校验（不涉及文件系统）：本地根 "/" 是
// 合法目标（与发布域的关键差异）。
func TestValidateCreateInput(t *testing.T) {
	if err := ValidateCreateInput(CreateInput{Path: "/a"}); !errors.Is(err, source.ErrInvalid) {
		t.Errorf("missing job id error = %v, want ErrInvalid", err)
	}
	if err := ValidateCreateInput(CreateInput{JobID: "job", Path: "/a", Name: "bad name"}); !errors.Is(err, source.ErrInvalid) {
		t.Errorf("invalid name error = %v, want ErrInvalid", err)
	}
	if err := ValidateCreateInput(CreateInput{JobID: "job", Path: "/"}); err != nil {
		t.Errorf("root target error = %v, want nil（本地根允许共享）", err)
	}
}

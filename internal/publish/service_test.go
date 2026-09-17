package publish

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"tinysync/internal/source"
	"tinysync/internal/syncjob"
)

// memRepo 是内存 Repository：行为覆盖 Create 冲突与 Get/List。
type memRepo struct {
	files map[string]PublishedFile
}

func newMemRepo() *memRepo {
	return &memRepo{files: map[string]PublishedFile{}}
}

func (m *memRepo) Create(ctx context.Context, p PublishedFile) error {
	for _, existing := range m.files {
		if existing.PublicPath == p.PublicPath {
			return fmt.Errorf("%w: %s", ErrConflict, p.PublicPath)
		}
	}
	m.files[p.ID] = p
	return nil
}

func (m *memRepo) Get(ctx context.Context, id string) (PublishedFile, error) {
	p, ok := m.files[id]
	if !ok {
		return PublishedFile{}, fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	return p, nil
}

func (m *memRepo) GetByPublicPath(ctx context.Context, publicPath string) (PublishedFile, error) {
	for _, p := range m.files {
		if p.PublicPath == publicPath {
			return p, nil
		}
	}
	return PublishedFile{}, fmt.Errorf("%w: %s", ErrNotFound, publicPath)
}

func (m *memRepo) List(ctx context.Context) ([]PublishedFile, error) {
	var list []PublishedFile
	for _, p := range m.files {
		list = append(list, p)
	}
	return list, nil
}

func (m *memRepo) Update(ctx context.Context, p PublishedFile) error {
	if _, ok := m.files[p.ID]; !ok {
		return fmt.Errorf("%w: %s", ErrNotFound, p.ID)
	}
	for id, existing := range m.files {
		if id != p.ID && existing.PublicPath == p.PublicPath {
			return fmt.Errorf("%w: %s", ErrConflict, p.PublicPath)
		}
	}
	m.files[p.ID] = p
	return nil
}

func (m *memRepo) Delete(ctx context.Context, id string) error {
	if _, ok := m.files[id]; !ok {
		return fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	delete(m.files, id)
	return nil
}

// memJobs 是内存 Job 仓库（Get）与 managed 记录仓库的合体视图：
// Repository 与 ManagedRepository 的 Delete 签名不同，Get-only 的
// Job 侧单独实现。
type memJobs struct {
	job     syncjob.Job
	managed []syncjob.ManagedFile
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
		now:  time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC),
	}
	canonical, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("evalsymlinks: %v", err)
	}
	env.root = canonical
	env.jobID = "job_pub"
	env.jobs = &memJobs{job: syncjob.Job{
		ID:        env.jobID,
		LocalRoot: canonical,
		Mode:      syncjob.ModeCopy,
	}}
	env.svc = NewService(env.repo, &jobsStub{jobs: env.jobs}, &managedStub{jobs: env.jobs})
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

func (e *serviceEnv) setManaged(rel ...string) {
	files := make([]syncjob.ManagedFile, 0, len(rel))
	for _, p := range rel {
		files = append(files, syncjob.ManagedFile{JobID: e.jobID, LocalRelPath: p})
	}
	e.jobs.managed = files
}

// jobsStub 以内存数据实现 Job 读取（避免依赖 syncjob.Service 的
// 持久化仓库）。
type jobsStub struct{ jobs *memJobs }

func (s *jobsStub) Get(ctx context.Context, id string) (syncjob.Job, error) {
	return s.jobs.Get(ctx, id)
}

type managedStub struct{ jobs *memJobs }

func (s *managedStub) ListByJob(ctx context.Context, jobID string) ([]syncjob.ManagedFile, error) {
	return s.jobs.managed, nil
}

func (s *managedStub) Upsert(ctx context.Context, files []syncjob.ManagedFile) error {
	return errors.New("not implemented")
}

func (s *managedStub) Delete(ctx context.Context, jobID string, remotePaths []string) error {
	return errors.New("not implemented")
}

func (s *managedStub) DeleteAllForJob(ctx context.Context, jobID string) error {
	return errors.New("not implemented")
}

// Create 完整校验链：canonical local path 落库、managed 要求生效。
func TestCreatePublishesManagedRegularFile(t *testing.T) {
	env := newServiceEnv(t)
	env.write(t, "photos/a.jpg", "jpeg")
	env.setManaged("photos/a.jpg")

	p, err := env.svc.Create(context.Background(), CreateInput{
		JobID:      env.jobID,
		Path:       "/photos/a.jpg",
		PublicPath: "/photos/a.jpg",
		Enabled:    true,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if p.ID == "" || p.LocalPath != filepath.Join(env.root, "photos", "a.jpg") {
		t.Errorf("policy = %+v, want canonical local path under root", p)
	}
	if p.CreatedAt != env.now || p.UpdatedAt != env.now {
		t.Errorf("timestamps = %v/%v, want injected now", p.CreatedAt, p.UpdatedAt)
	}
}

// 未 managed 的文件拒绝发布；目录与 symlink 拒绝；root 外逃逸拒绝。
func TestCreateRejectsUnmanagedAndUnsafeTargets(t *testing.T) {
	env := newServiceEnv(t)
	env.write(t, "private.txt", "secret")
	env.write(t, "synced.txt", "data")
	env.write(t, "sub/x.txt", "x")
	env.setManaged("synced.txt")
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
		path  string
		byErr error
	}{
		{"unmanaged file", "/private.txt", source.ErrInvalid},
		{"directory", "/sub", source.ErrInvalid},
		{"root itself", "/", source.ErrInvalid},
		{"traversal", "/../x", source.ErrInvalid},
		{"missing file", "/nope.txt", source.ErrInvalid},
		{"symlink inside root", "/alias.txt", source.ErrInvalid},
		{"parent symlink escape", "/out-link/secret.txt", source.ErrInvalid},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := env.svc.Create(context.Background(), CreateInput{
				JobID:      env.jobID,
				Path:       tc.path,
				PublicPath: "/pub-" + tc.name,
				Enabled:    true,
			})
			if !errors.Is(err, tc.byErr) {
				t.Errorf("Create(%s) error = %v, want %v", tc.path, err, tc.byErr)
			}
		})
	}

	// public_path 非法 → 400。
	if _, err := env.svc.Create(context.Background(), CreateInput{
		JobID: env.jobID, Path: "/synced.txt", PublicPath: "relative", Enabled: true,
	}); !errors.Is(err, source.ErrInvalid) {
		t.Errorf("invalid public path error = %v, want ErrInvalid", err)
	}
	// 过期时刻在过去 → 400。
	past := env.now.Add(-time.Hour)
	if _, err := env.svc.Create(context.Background(), CreateInput{
		JobID: env.jobID, Path: "/synced.txt", PublicPath: "/expired", Enabled: true, ExpiresAt: &past,
	}); !errors.Is(err, source.ErrInvalid) {
		t.Errorf("past expiry error = %v, want ErrInvalid", err)
	}
	// Job 不存在 → 404。
	if _, err := env.svc.Create(context.Background(), CreateInput{
		JobID: "no-such", Path: "/synced.txt", PublicPath: "/x", Enabled: true,
	}); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing job error = %v, want ErrNotFound", err)
	}
}

// Update 只允许 public_path / enabled / expires_at；local_path 不可变。
func TestUpdateMutatesOnlyMutableFields(t *testing.T) {
	env := newServiceEnv(t)
	env.write(t, "synced.txt", "data")
	env.setManaged("synced.txt")
	p, err := env.svc.Create(context.Background(), CreateInput{
		JobID: env.jobID, Path: "/synced.txt", PublicPath: "/old", Enabled: true,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	newPath := "/new"
	enabled := false
	updated, err := env.svc.Update(context.Background(), p.ID, UpdateInput{
		PublicPath: &newPath,
		Enabled:    &enabled,
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if updated.PublicPath != "/new" || updated.Enabled {
		t.Errorf("updated = %+v, want /new disabled", updated)
	}
	if updated.LocalPath != p.LocalPath || !updated.CreatedAt.Equal(p.CreatedAt) {
		t.Errorf("immutable fields changed: %v -> %v", p, updated)
	}

	// 清除过期。
	expiry := env.now.Add(time.Hour)
	if _, err := env.svc.Update(context.Background(), p.ID, UpdateInput{ExpiresAt: &expiry}); err != nil {
		t.Fatalf("set expiry: %v", err)
	}
	updated, err = env.svc.Update(context.Background(), p.ID, UpdateInput{ClearExpires: true})
	if err != nil || updated.ExpiresAt != nil {
		t.Errorf("clear expiry = %+v, %v; want nil expires", updated, err)
	}

	// public_path 冲突 → 409。
	second, err := env.svc.Create(context.Background(), CreateInput{
		JobID: env.jobID, Path: "/synced.txt", PublicPath: "/second", Enabled: true,
	})
	if err != nil {
		t.Fatalf("Create second: %v", err)
	}
	if _, err := env.svc.Update(context.Background(), second.ID, UpdateInput{PublicPath: &newPath}); !errors.Is(err, ErrConflict) {
		t.Errorf("conflict error = %v, want ErrConflict", err)
	}
}

// ResolveForRequest：禁用 / 过期一律 ErrNotFound，不区分原因。文件
// 系统状态（缺失、symlink 替换）不归本层判定，由 serving 时的
// filesafe.OpenCanonicalRegularFile 复验并同形映射 404。
func TestResolveForRequestHidesReasons(t *testing.T) {
	env := newServiceEnv(t)
	env.write(t, "synced.txt", "data")
	env.setManaged("synced.txt")
	p, err := env.svc.Create(context.Background(), CreateInput{
		JobID: env.jobID, Path: "/synced.txt", PublicPath: "/a.txt", Enabled: true,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	_, err = env.svc.ResolveForRequest(context.Background(), "/a.txt")
	if err != nil {
		t.Fatalf("resolve enabled = %v, want nil", err)
	}

	// 禁用。
	disabled := false
	if _, err := env.svc.Update(context.Background(), p.ID, UpdateInput{Enabled: &disabled}); err != nil {
		t.Fatalf("disable: %v", err)
	}
	if _, err := env.svc.ResolveForRequest(context.Background(), "/a.txt"); !errors.Is(err, ErrNotFound) {
		t.Errorf("resolve disabled = %v, want ErrNotFound", err)
	}

	// 过期（时钟推进）。
	enabled := true
	expiry := env.now.Add(time.Hour)
	if _, err := env.svc.Update(context.Background(), p.ID, UpdateInput{Enabled: &enabled, ExpiresAt: &expiry}); err != nil {
		t.Fatalf("set expiry: %v", err)
	}
	env.now = env.now.Add(2 * time.Hour)
	if _, err := env.svc.ResolveForRequest(context.Background(), "/a.txt"); !errors.Is(err, ErrNotFound) {
		t.Errorf("resolve expired = %v, want ErrNotFound", err)
	}

	// 文件缺失。
	env.now = time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	if err := os.Remove(filepath.Join(env.root, "synced.txt")); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if _, err := env.svc.ResolveForRequest(context.Background(), "/a.txt"); err != nil {
		t.Errorf("resolve with missing file = %v, want policy still resolved", err)
	}

	// 未知公开路径。
	if _, err := env.svc.ResolveForRequest(context.Background(), "/never"); !errors.Is(err, ErrNotFound) {
		t.Errorf("resolve unknown = %v, want ErrNotFound", err)
	}
}

// Delete 只移除策略记录，不触及本地文件。
func TestDeleteKeepsLocalFile(t *testing.T) {
	env := newServiceEnv(t)
	env.write(t, "synced.txt", "data")
	env.setManaged("synced.txt")
	p, err := env.svc.Create(context.Background(), CreateInput{
		JobID: env.jobID, Path: "/synced.txt", PublicPath: "/a.txt", Enabled: true,
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
	at := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	p := PublishedFile{ExpiresAt: &at}
	if p.Expired(at.Add(-time.Second)) {
		t.Error("one second before expiry = expired, want active")
	}
	if !p.Expired(at) {
		t.Error("at expiry = active, want expired")
	}
	if (PublishedFile{}).Expired(at.Add(time.Hour)) {
		t.Error("nil expiry = expired, want never")
	}
}

// ValidateCreateInput 的格式校验（不涉及文件系统）。
func TestValidateCreateInput(t *testing.T) {
	if err := ValidateCreateInput(CreateInput{Path: "/a", PublicPath: "/a"}); !errors.Is(err, source.ErrInvalid) {
		t.Errorf("missing job id error = %v, want ErrInvalid", err)
	}
	if err := ValidateCreateInput(CreateInput{JobID: "job", Path: "/", PublicPath: "/a"}); !errors.Is(err, source.ErrInvalid) {
		t.Errorf("root target error = %v, want ErrInvalid", err)
	}
	if err := ValidateCreateInput(CreateInput{JobID: "job", Path: "/a", PublicPath: "/"}); !errors.Is(err, source.ErrInvalid) {
		t.Errorf("root public path error = %v, want ErrInvalid", err)
	}
	if err := ValidateCreateInput(CreateInput{JobID: "job", Path: "/a", PublicPath: "/ok"}); err != nil {
		t.Errorf("valid input error = %v, want nil", err)
	}
}

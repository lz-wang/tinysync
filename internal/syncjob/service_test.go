package syncjob_test

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"tinysync/internal/source"
	sqliterepo "tinysync/internal/source/sqlite"
	"tinysync/internal/source/webdav"
	"tinysync/internal/storage"
	"tinysync/internal/syncjob"
	sqlite "tinysync/internal/syncjob/sqlite"
)

// testEnv 聚合 syncjob.Service 依赖：真实 SQLite、真实 Source 服务与固定时钟。
type testEnv struct {
	db        *sql.DB
	managed   *sqlite.ManagedRepository
	service   *syncjob.Service
	sourceSvc *source.Service
	dataDir   string
	now       time.Time
}

// newTestEnv 构造测试环境：dataDir 与被测服务分离，时钟固定可断言。
func newTestEnv(t *testing.T) *testEnv {
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

	sourceSvc := source.NewService(sqliterepo.New(db), webdav.NewFactory())
	env := &testEnv{
		db:        db,
		managed:   sqlite.NewManagedRepository(db),
		service:   syncjob.NewService(sqlite.NewRepository(db), sourceSvc, dataDir),
		sourceSvc: sourceSvc,
		dataDir:   dataDir,
		now:       time.Unix(1757879400, 0).UTC(),
	}
	env.service.Now = func() time.Time { return env.now }
	return env
}

// mustSource 创建一个启用的测试 Source 并返回 ID。
func (e *testEnv) mustSource(t *testing.T) string {
	t.Helper()
	src, err := e.sourceSvc.Create(context.Background(), source.CreateInput{
		Name:     "nas-" + newTestName(),
		Type:     source.TypeWebDAV,
		Endpoint: "https://dav.example.com/dav/user/",
		Enabled:  true,
	})
	if err != nil {
		t.Fatalf("create source: %v", err)
	}
	return src.ID
}

// newTestName 生成唯一的测试名（Source name 大小写不敏感唯一）。
func newTestName() string {
	id, err := syncjob.NewID()
	if err != nil {
		panic(err)
	}
	return id
}

// validInput 返回指向独立临时目录的合法创建输入。
func (e *testEnv) validInput(t *testing.T, name string) syncjob.CreateInput {
	t.Helper()
	return syncjob.CreateInput{
		Name:       name + "-" + newTestName(),
		SourceID:   e.mustSource(t),
		RemoteRoot: "/photos",
		LocalRoot:  t.TempDir(),
		Mode:       syncjob.ModeMirror,
		Include:    []string{"**/*.jpg"},
		Enabled:    true,
	}
}

// 创建成功：ID 生成、时间来自时钟、LocalRoot 以 canonical 形式存储。
func TestCreateCanonicalizesLocalRoot(t *testing.T) {
	env := newTestEnv(t)
	input := env.validInput(t, "photos")

	job, err := env.service.Create(context.Background(), input)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if len(job.ID) <= len("job_") {
		t.Errorf("Create returned ID %q, want job_ prefixed id", job.ID)
	}
	if !job.CreatedAt.Equal(env.now) || !job.UpdatedAt.Equal(env.now) {
		t.Errorf("CreatedAt/UpdatedAt = %v/%v, want fixed clock %v", job.CreatedAt, job.UpdatedAt, env.now)
	}
	canonical, err := filepath.EvalSymlinks(input.LocalRoot)
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	if job.LocalRoot != canonical {
		t.Errorf("LocalRoot = %q, want canonical %q", job.LocalRoot, canonical)
	}
}

// RemoteRoot 归一为 canonical logical path：去尾斜杠、消除 ..，root 为 /。
func TestCreateNormalizesRemoteRoot(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()

	input := env.validInput(t, "photos")
	input.RemoteRoot = "/photos/2024/../"
	job, err := env.service.Create(ctx, input)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if job.RemoteRoot != "/photos" {
		t.Errorf("RemoteRoot = %q, want /photos", job.RemoteRoot)
	}

	input2 := env.validInput(t, "root")
	input2.RemoteRoot = "/"
	job2, err := env.service.Create(ctx, input2)
	if err != nil {
		t.Fatalf("Create root job: %v", err)
	}
	if job2.RemoteRoot != "/" {
		t.Errorf("RemoteRoot = %q, want /", job2.RemoteRoot)
	}
}

// 校验失败用例：Source 不存在、非法 syncjob.Mode、LocalRoot 不存在或不是目录、空 name。
func TestCreateValidation(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()

	missing := env.validInput(t, "missing-source")
	missing.SourceID = "src_missing"
	if _, err := env.service.Create(ctx, missing); !errors.Is(err, source.ErrNotFound) {
		t.Errorf("missing source = %v, want source.ErrNotFound", err)
	}

	badMode := env.validInput(t, "bad-mode")
	badMode.Mode = syncjob.Mode("rsync")
	if _, err := env.service.Create(ctx, badMode); !errors.Is(err, syncjob.ErrInvalid) {
		t.Errorf("bad mode = %v, want syncjob.ErrInvalid", err)
	}

	notExist := env.validInput(t, "no-local")
	notExist.LocalRoot = filepath.Join(t.TempDir(), "gone")
	if _, err := env.service.Create(ctx, notExist); !errors.Is(err, syncjob.ErrInvalid) {
		t.Errorf("missing local root = %v, want syncjob.ErrInvalid", err)
	}

	fileRoot := filepath.Join(t.TempDir(), "file.txt")
	if err := os.WriteFile(fileRoot, []byte("x"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	notDir := env.validInput(t, "file-local")
	notDir.LocalRoot = fileRoot
	if _, err := env.service.Create(ctx, notDir); !errors.Is(err, syncjob.ErrInvalid) {
		t.Errorf("file local root = %v, want syncjob.ErrInvalid", err)
	}

	blank := env.validInput(t, "blank")
	blank.Name = "   "
	if _, err := env.service.Create(ctx, blank); !errors.Is(err, syncjob.ErrInvalid) {
		t.Errorf("blank name = %v, want syncjob.ErrInvalid", err)
	}
}

// LocalRoot 与已有 Job 重叠（相等/父子）拒绝。
func TestCreateRejectsJobRootOverlap(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()

	base := t.TempDir()
	first := env.validInput(t, "first")
	first.LocalRoot = base
	if _, err := env.service.Create(ctx, first); err != nil {
		t.Fatalf("Create first: %v", err)
	}

	for name, root := range map[string]string{
		"equal":      base,
		"child":      filepath.Join(base, "sub"),
		"grandchild": filepath.Join(base, "sub", "deep"),
		"parent":     filepath.Dir(base),
	} {
		// canonical 校验要求 root 存在；先创建再验证归属保护。
		if err := os.MkdirAll(root, 0o755); err != nil {
			t.Fatalf("prepare %s root: %v", name, err)
		}
		input := env.validInput(t, "overlap")
		input.LocalRoot = root
		if _, err := env.service.Create(ctx, input); !errors.Is(err, syncjob.ErrRootOverlap) {
			t.Errorf("%s root %s = %v, want syncjob.ErrRootOverlap", name, root, err)
		}
	}
}

// LocalRoot 与 TinySync dataDir 互相包含时拒绝，避免同步树吞掉数据库文件。
func TestCreateRejectsDataDirOverlap(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()

	for name, root := range map[string]string{
		"equal to datadir":  env.dataDir,
		"inside datadir":    filepath.Join(env.dataDir, "sync"),
		"parent of datadir": filepath.Dir(env.dataDir),
	} {
		input := env.validInput(t, "datadir")
		input.LocalRoot = root
		if err := os.MkdirAll(root, 0o755); err != nil {
			t.Fatalf("prepare root %s: %v", root, err)
		}
		if _, err := env.service.Create(ctx, input); !errors.Is(err, syncjob.ErrRootOverlap) {
			t.Errorf("%s (%s) = %v, want syncjob.ErrRootOverlap", name, root, err)
		}
	}
}

// Update 修改 mapping 字段（source/remoteRoot/localRoot 任一）时安全释放
// managed metadata；仅改名不释放。
func TestUpdateReleasesMetadataOnMappingChange(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	sourceID := env.mustSource(t)

	job, err := env.service.Create(ctx, syncjob.CreateInput{
		Name:       "photos-" + newTestName(),
		SourceID:   sourceID,
		RemoteRoot: "/photos",
		LocalRoot:  t.TempDir(),
		Mode:       syncjob.ModeMirror,
		Enabled:    true,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	seedManaged(t, env, job.ID)

	// 仅改名：metadata 保留。
	name := "renamed-" + newTestName()
	if _, err := env.service.Update(ctx, job.ID, syncjob.UpdateInput{Name: &name}); err != nil {
		t.Fatalf("Update name: %v", err)
	}
	if got := managedCount(t, env, job.ID); got != 1 {
		t.Fatalf("after rename managed = %d, want 1", got)
	}

	// 修改 remoteRoot：metadata 释放；本地文件保留是 Copy/relinquish 语义。
	root := "/vacation"
	if _, err := env.service.Update(ctx, job.ID, syncjob.UpdateInput{RemoteRoot: &root}); err != nil {
		t.Fatalf("Update remoteRoot: %v", err)
	}
	if got := managedCount(t, env, job.ID); got != 0 {
		t.Errorf("after remoteRoot change managed = %d, want 0", got)
	}
}

// Create / Update 即时校验 include/exclude pattern：非法 pattern
// 直接拒绝（ErrInvalid），不留到首次 Run 才静默失败。
func TestCreateAndUpdateValidatePatterns(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	sourceID := env.mustSource(t)

	job, err := env.service.Create(ctx, syncjob.CreateInput{
		Name:       "photos-" + newTestName(),
		SourceID:   sourceID,
		RemoteRoot: "/photos",
		LocalRoot:  t.TempDir(),
		Mode:       syncjob.ModeCopy,
		Include:    []string{"**/*.jpg"},
		Exclude:    []string{"[invalid"},
		Enabled:    true,
	})
	if !errors.Is(err, syncjob.ErrInvalid) {
		t.Fatalf("Create with invalid exclude = %v, want ErrInvalid", err)
	}

	job, err = env.service.Create(ctx, syncjob.CreateInput{
		Name:       "photos-" + newTestName(),
		SourceID:   sourceID,
		RemoteRoot: "/photos",
		LocalRoot:  t.TempDir(),
		Mode:       syncjob.ModeCopy,
		Enabled:    true,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	bad := []string{"[invalid"}
	if _, err := env.service.Update(ctx, job.ID, syncjob.UpdateInput{Include: &bad}); !errors.Is(err, syncjob.ErrInvalid) {
		t.Errorf("Update with invalid include = %v, want ErrInvalid", err)
	}
	// 合法 pattern 正常更新。
	good := []string{"docs/**", "**/*.pdf"}
	if _, err := env.service.Update(ctx, job.ID, syncjob.UpdateInput{Include: &good}); err != nil {
		t.Errorf("Update with valid include = %v, want nil", err)
	}
}

// mapping 变更与配置替换必须原子：更新撞 name 唯一冲突时 metadata
// 不丢失——先删后更的旧实现会在半成功状态下让 Job 永久失去对本地
// 文件的管理关系（下轮 planner 把远端文件当 unknown local 全部跳过）。
func TestUpdateMappingConflictKeepsManaged(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	sourceID := env.mustSource(t)

	job, err := env.service.Create(ctx, syncjob.CreateInput{
		Name:       "photos-" + newTestName(),
		SourceID:   sourceID,
		RemoteRoot: "/photos",
		LocalRoot:  t.TempDir(),
		Mode:       syncjob.ModeMirror,
		Enabled:    true,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	other, err := env.service.Create(ctx, syncjob.CreateInput{
		Name:       "taken-" + newTestName(),
		SourceID:   sourceID,
		RemoteRoot: "/docs",
		LocalRoot:  t.TempDir(),
		Mode:       syncjob.ModeCopy,
		Enabled:    true,
	})
	if err != nil {
		t.Fatalf("Create other: %v", err)
	}
	seedManaged(t, env, job.ID)

	// 一次 PATCH 同时改 remoteRoot（mapping 变更）与 name（撞唯一约束）。
	root := "/elsewhere"
	name := other.Name
	_, err = env.service.Update(ctx, job.ID, syncjob.UpdateInput{RemoteRoot: &root, Name: &name})
	if !errors.Is(err, syncjob.ErrConflict) {
		t.Fatalf("Update with mapping + name conflict = %v, want ErrConflict", err)
	}
	if got := managedCount(t, env, job.ID); got != 1 {
		t.Errorf("managed after failed mapping update = %d, want 1 (metadata must survive)", got)
	}
}

// Update 校验新 mapping：重叠 LocalRoot、不存在的 Source 拒绝且不释放。
func TestUpdateValidatesMapping(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	sourceID := env.mustSource(t)

	job, err := env.service.Create(ctx, syncjob.CreateInput{
		Name:       "photos-" + newTestName(),
		SourceID:   sourceID,
		RemoteRoot: "/photos",
		LocalRoot:  t.TempDir(),
		Mode:       syncjob.ModeCopy,
		Enabled:    true,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	seedManaged(t, env, job.ID)

	otherRoot := t.TempDir()
	if _, err := env.service.Create(ctx, syncjob.CreateInput{
		Name:       "other-" + newTestName(),
		SourceID:   sourceID,
		RemoteRoot: "/other",
		LocalRoot:  otherRoot,
		Mode:       syncjob.ModeCopy,
		Enabled:    true,
	}); err != nil {
		t.Fatalf("Create other: %v", err)
	}

	overlap := filepath.Join(otherRoot, "sub")
	if err := os.MkdirAll(overlap, 0o755); err != nil {
		t.Fatalf("prepare overlap root: %v", err)
	}
	if _, err := env.service.Update(ctx, job.ID, syncjob.UpdateInput{LocalRoot: &overlap}); !errors.Is(err, syncjob.ErrRootOverlap) {
		t.Errorf("update to overlapping root = %v, want syncjob.ErrRootOverlap", err)
	}
	if got := managedCount(t, env, job.ID); got != 1 {
		t.Errorf("managed after failed update = %d, want 1 (unchanged)", got)
	}

	missing := "src_missing"
	if _, err := env.service.Update(ctx, job.ID, syncjob.UpdateInput{SourceID: &missing}); !errors.Is(err, source.ErrNotFound) {
		t.Errorf("update to missing source = %v, want source.ErrNotFound", err)
	}
	if got := managedCount(t, env, job.ID); got != 1 {
		t.Errorf("managed after failed source update = %d, want 1", got)
	}

	if _, err := env.service.Update(ctx, "job_missing", syncjob.UpdateInput{}); !errors.Is(err, syncjob.ErrNotFound) {
		t.Errorf("update missing job = %v, want syncjob.ErrNotFound", err)
	}
}

// Delete 移除 Job 并清空 managed metadata（CASCADE），本地文件不被触碰。
func TestDeleteJob(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	job, err := env.service.Create(ctx, syncjob.CreateInput{
		Name:       "photos-" + newTestName(),
		SourceID:   env.mustSource(t),
		RemoteRoot: "/photos",
		LocalRoot:  t.TempDir(),
		Mode:       syncjob.ModeMirror,
		Enabled:    true,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	seedManaged(t, env, job.ID)

	if err := env.service.Delete(ctx, job.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := env.service.Get(ctx, job.ID); !errors.Is(err, syncjob.ErrNotFound) {
		t.Errorf("Get after delete = %v, want syncjob.ErrNotFound", err)
	}
	if got := managedCount(t, env, job.ID); got != 0 {
		t.Errorf("managed after delete = %d, want 0", got)
	}
	if err := env.service.Delete(ctx, job.ID); !errors.Is(err, syncjob.ErrNotFound) {
		t.Errorf("second Delete = %v, want syncjob.ErrNotFound", err)
	}
}

// Create / Update 即时校验 schedule；interval 创建时设置相位 anchor，
// schedule 变更重设 anchor 且不触碰 managed metadata（非 mapping 字段）。
func TestCreateAndUpdateSchedule(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()

	bad := env.validInput(t, "bad-schedule")
	bad.Schedule = &syncjob.Schedule{Type: syncjob.ScheduleInterval, Value: "30s"}
	if _, err := env.service.Create(ctx, bad); !errors.Is(err, syncjob.ErrInvalid) {
		t.Errorf("Create with interval 30s = %v, want ErrInvalid", err)
	}

	// 创建 interval Job：value 归一、anchor 来自固定时钟。
	input := env.validInput(t, "scheduled")
	input.Schedule = &syncjob.Schedule{Type: syncjob.ScheduleInterval, Value: " 30m "}
	job, err := env.service.Create(ctx, input)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if job.Schedule.Type != syncjob.ScheduleInterval || job.Schedule.Value != "30m" {
		t.Errorf("created schedule = %+v, want interval 30m", job.Schedule)
	}
	if job.Schedule.AnchorAt == nil || !job.Schedule.AnchorAt.Equal(env.now) {
		t.Errorf("interval anchor = %v, want fixed clock %v", job.Schedule.AnchorAt, env.now)
	}

	// 缺省 schedule 为 manual，与 v0.3 行为一致。
	plain := env.validInput(t, "plain")
	plainJob, err := env.service.Create(ctx, plain)
	if err != nil {
		t.Fatalf("Create plain: %v", err)
	}
	if plainJob.Schedule.Type != syncjob.ScheduleManual {
		t.Errorf("default schedule type = %q, want manual", plainJob.Schedule.Type)
	}

	// 更新为 cron：原子替换整个 schedule，anchor 清空，managed 不释放。
	seedManaged(t, env, job.ID)
	cronSchedule := syncjob.Schedule{
		Type:     syncjob.ScheduleCron,
		Value:    "0 3 * * *",
		Timezone: "Asia/Singapore",
	}
	updated, err := env.service.Update(ctx, job.ID, syncjob.UpdateInput{Schedule: &cronSchedule})
	if err != nil {
		t.Fatalf("Update schedule: %v", err)
	}
	if updated.Schedule.Type != syncjob.ScheduleCron ||
		updated.Schedule.Value != "0 3 * * *" ||
		updated.Schedule.Timezone != "Asia/Singapore" {
		t.Errorf("updated schedule = %+v, want cron 0 3 * * * Asia/Singapore", updated.Schedule)
	}
	if updated.Schedule.AnchorAt != nil {
		t.Errorf("cron schedule anchor = %v, want nil", updated.Schedule.AnchorAt)
	}
	if got := managedCount(t, env, job.ID); got != 1 {
		t.Errorf("managed after schedule change = %d, want 1 (not a mapping change)", got)
	}

	// 非法 schedule 拒绝更新（持久化回读断言随 schedule 字段落库一并覆盖）。
	worse := syncjob.Schedule{Type: syncjob.ScheduleOnce, Value: "not-a-time"}
	if _, err := env.service.Update(ctx, job.ID, syncjob.UpdateInput{Schedule: &worse}); !errors.Is(err, syncjob.ErrInvalid) {
		t.Errorf("Update with bad once = %v, want ErrInvalid", err)
	}
}

// seedManaged 直接向 managed repo 写一条记录，供释放语义断言使用。
func seedManaged(t *testing.T, env *testEnv, jobID string) {
	t.Helper()
	size := int64(10)
	err := env.managed.Upsert(context.Background(), []syncjob.ManagedFile{{
		JobID:        jobID,
		RemotePath:   "/photos/a.jpg",
		LocalRelPath: "photos/a.jpg",
		State:        syncjob.StateSynced,
		Remote:       source.Fingerprint{Size: 10},
		LocalSize:    &size,
		UpdatedAt:    env.now,
	}})
	if err != nil {
		t.Fatalf("seed managed: %v", err)
	}
}

// managedCount 统计 Job 的 managed 记录数。
func managedCount(t *testing.T, env *testEnv, jobID string) int {
	t.Helper()
	list, err := env.managed.ListByJob(context.Background(), jobID)
	if err != nil {
		t.Fatalf("list managed: %v", err)
	}
	return len(list)
}

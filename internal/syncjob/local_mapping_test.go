package syncjob_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"tinysync/internal/job"
	jobsqlite "tinysync/internal/job/sqlite"
	"tinysync/internal/source"
	"tinysync/internal/syncjob"
)

func TestLocalJobCreateAndUpdateMapping(t *testing.T) {
	for _, field := range []string{"source_id", "remote_root", "local_root"} {
		t.Run(field, func(t *testing.T) {
			env := newTestEnv(t)
			ctx := context.Background()
			base := t.TempDir()
			for _, p := range []string{"a", "b"} {
				if err := os.Mkdir(filepath.Join(base, p), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			src, err := env.sourceSvc.Create(ctx, source.CreateInput{Name: "local", Type: source.TypeLocal, Config: source.Config{Local: &source.LocalConfig{Root: base}}, Enabled: true})
			if err != nil {
				t.Fatal(err)
			}
			input := fileJobInput{Name: "local job", SourceID: src.ID, RemoteRoot: "/a", LocalRoot: filepath.Join(base, "b"), Mode: syncjob.ModeMirror, Enabled: true}
			unsafe := input
			unsafe.LocalRoot = filepath.Join(base, "a")
			if _, err := env.createFileJob(ctx, unsafe); !errors.Is(err, syncjob.ErrInvalid) {
				t.Fatalf("create overlap=%v", err)
			}
			j, err := env.createFileJob(ctx, input)
			if err != nil {
				t.Fatal(err)
			}
			seedManaged(t, env, j.ID)
			var upd filePatch
			switch field {
			case "remote_root":
				value := "/"
				upd.RemoteRoot = &value
			case "local_root":
				value := filepath.Join(base, "a")
				upd.LocalRoot = &value
			case "source_id":
				if err := os.Mkdir(filepath.Join(base, "b", "a"), 0o755); err != nil {
					t.Fatal(err)
				}
				other, err := env.sourceSvc.Create(ctx, source.CreateInput{Name: "other", Type: source.TypeLocal, Config: source.Config{Local: &source.LocalConfig{Root: filepath.Join(base, "b")}}, Enabled: true})
				if err != nil {
					t.Fatal(err)
				}
				upd.SourceID = &other.ID
			}
			if _, err := env.updateFileJob(ctx, j.ID, upd); !errors.Is(err, syncjob.ErrInvalid) {
				t.Fatalf("update=%v", err)
			}
			if managedCount(t, env, j.ID) != 1 {
				t.Fatal("rejected update reset metadata")
			}
			got, err := env.service.Get(ctx, j.ID)
			if err != nil {
				t.Fatalf("get job: %v", err)
			}
			gotCfg := fileConfigOf(t, got)
			wantCfg := fileConfigOf(t, j)
			if gotCfg.SourceID != wantCfg.SourceID || gotCfg.RemoteRoot != wantCfg.RemoteRoot || gotCfg.LocalRoot != wantCfg.LocalRoot {
				t.Fatalf("job changed: %+v", gotCfg)
			}
		})
	}
}

func TestRejectedLocalJobCreateDoesNotCreateDestination(t *testing.T) {
	for _, tc := range []struct{ remote, target string }{
		{"/", "source/new/a/b"},
		{"/sub", "source/sub/new/a/b"},
		{"/missing", "backup/new/a/b"},
	} {
		t.Run(tc.remote+"-"+tc.target, func(t *testing.T) {
			env := newTestEnv(t)
			ctx := context.Background()
			base := t.TempDir()
			root := filepath.Join(base, "source")
			if err := os.MkdirAll(filepath.Join(root, "sub"), 0o755); err != nil {
				t.Fatal(err)
			}
			src, err := env.sourceSvc.Create(ctx, source.CreateInput{
				Name: "local", Type: source.TypeLocal,
				Config: source.Config{Local: &source.LocalConfig{Root: root}}, Enabled: true,
			})
			if err != nil {
				t.Fatal(err)
			}
			destination := filepath.Join(base, tc.target)
			_, err = env.createFileJob(ctx, fileJobInput{
				Name: "rejected", SourceID: src.ID, RemoteRoot: tc.remote,
				LocalRoot: destination, Mode: syncjob.ModeMirror,
			})
			if !errors.Is(err, syncjob.ErrInvalid) {
				t.Fatalf("Create = %v; want ErrInvalid", err)
			}
			// 连最外层缺失组件也不得出现，不仅检查最终目录。
			for current := destination; current != base; current = filepath.Dir(current) {
				if current == root || current == filepath.Join(root, "sub") {
					break
				}
				if _, err := os.Lstat(current); !os.IsNotExist(err) {
					t.Fatalf("rejected create changed %q: %v", current, err)
				}
			}
			jobs, err := env.service.List(ctx)
			if err != nil || len(jobs) != 0 {
				t.Fatalf("rejected create persisted jobs: %+v, %v", jobs, err)
			}
		})
	}
}

func TestLocalJobCreateAllowsMissingSiblingDestination(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	base := t.TempDir()
	root := filepath.Join(base, "source")
	if err := os.MkdirAll(filepath.Join(root, "input"), 0o755); err != nil {
		t.Fatal(err)
	}
	src, err := env.sourceSvc.Create(ctx, source.CreateInput{
		Name: "local", Type: source.TypeLocal,
		Config: source.Config{Local: &source.LocalConfig{Root: root}}, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(root, "output", "nested")
	j, err := env.createFileJob(ctx, fileJobInput{
		Name: "siblings", SourceID: src.ID, RemoteRoot: "/input",
		LocalRoot: destination, Mode: syncjob.ModeCopy,
	})
	if err != nil {
		t.Fatal(err)
	}
	rootCreated := fileConfigOf(t, j).LocalRoot
	if info, err := os.Stat(rootCreated); err != nil || !info.IsDir() {
		t.Fatalf("valid destination not created: %v", err)
	}
}

// recheckingRepository 注入预检与创建后复验之间的变化，无并发计时假设。
type recheckingRepository struct {
	job.Repository
	lists     int
	onRecheck func() error
}

func (r *recheckingRepository) List(ctx context.Context) ([]job.Job, error) {
	r.lists++
	if r.lists == 2 {
		if err := r.onRecheck(); err != nil {
			return nil, err
		}
	}
	return r.Repository.List(ctx)
}

func TestLocalJobCreateRevalidatesAfterMkdir(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	base := t.TempDir()
	root := filepath.Join(base, "source")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	// Windows 等平台无法创建 symlink 时仅跳过此变化注入场景。
	probe := filepath.Join(base, "probe")
	if err := os.Symlink(root, probe); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := os.Remove(probe); err != nil {
		t.Fatal(err)
	}
	src, err := env.sourceSvc.Create(ctx, source.CreateInput{
		Name: "local", Type: source.TypeLocal,
		Config: source.Config{Local: &source.LocalConfig{Root: root}}, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(base, "backup", "nested")
	repo := &recheckingRepository{Repository: jobsqlite.NewRepository(env.db)}
	repo.onRecheck = func() error {
		if _, err := os.Stat(destination); err != nil {
			return fmt.Errorf("destination was not created before recheck: %w", err)
		}
		// 创建后源目录被外部替换为链接：只做预检会错误放行。
		canonical := src.Config.Local.Root
		if err := os.Rename(canonical, canonical+"-before"); err != nil {
			return err
		}
		return os.Symlink(destination, canonical)
	}
	svc := job.NewService(repo, map[job.JobKind]job.Executor{
		job.KindFile: syncjob.NewFileExecutor(env.sourceSvc, repo, env.managed, env.dataDir),
	})
	cfg, err := syncjob.MarshalFileConfig(src.ID, "/", destination, syncjob.ModeMirror, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = svc.Create(ctx, job.CreateInput{
		Name: "changed mapping", Kind: job.KindFile, Config: cfg, Enabled: true,
	})
	if !errors.Is(err, syncjob.ErrInvalid) || repo.lists != 2 {
		t.Fatalf("Create = %v; root checks = %d, want ErrInvalid after recheck", err, repo.lists)
	}
	jobs, err := env.service.List(ctx)
	if err != nil || len(jobs) != 0 {
		t.Fatalf("unsafe mapping persisted: %+v, %v", jobs, err)
	}
}

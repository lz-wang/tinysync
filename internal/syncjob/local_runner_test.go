package syncjob

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"tinysync/internal/filesafe"
	"tinysync/internal/source"
)

func TestRunnerRejectsChangedLocalMappingBeforeOpen(t *testing.T) {
	for _, change := range []string{"overlap", "source_symlink", "target_symlink", "missing"} {
		t.Run(change, func(t *testing.T) {
			env := newRunnerEnv(t, nil)
			ctx := context.Background()
			root, err := filesafe.CanonicalExistingDir(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			target, err := filesafe.CanonicalExistingDir(env.root)
			if err != nil {
				t.Fatal(err)
			}
			env.creds.source.Type = source.TypeLocal
			env.creds.source.Config = source.Config{Local: &source.LocalConfig{Root: root}}
			env.creds.openErr = errorsNew("OpenRemote should not be reached")
			job := env.mustJobIn(t, "local", target)
			job.Mode = ModeMirror
			if err := source.ValidateJobMapping(env.creds.source, "/", target); err != nil {
				t.Fatal(err)
			}
			switch change {
			case "overlap":
				env.creds.source.Config.Local.Root = target
			case "source_symlink":
				if err := os.Remove(root); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, root); err != nil {
					t.Skip(err)
				}
			case "target_symlink":
				if err := os.Remove(target); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(root, target); err != nil {
					t.Skip(err)
				}
			case "missing":
				if err := os.Remove(root); err != nil {
					t.Fatal(err)
				}
			}
			if err := env.repo.Update(ctx, job); err != nil {
				t.Fatal(err)
			}
			survivor := filepath.Join(target, "keep.txt")
			if err := os.WriteFile(survivor, []byte("keep"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := env.managed.Upsert(ctx, []ManagedFile{{JobID: job.ID, RemotePath: "/keep.txt", LocalRelPath: "keep.txt", State: StateSynced}}); err != nil {
				t.Fatal(err)
			}
			runID, err := env.runner.Start(ctx, job.ID)
			if err != nil {
				t.Fatal(err)
			}
			status, err := env.runner.Wait(ctx, runID)
			if err != nil {
				t.Fatal(err)
			}
			if status.State != RunFailed || !strings.Contains(status.Error, "validate job mapping") {
				t.Fatalf("status=%+v", status)
			}
			if _, err := os.Stat(survivor); err != nil {
				t.Fatalf("mirror deleted survivor: %v", err)
			}
			managed, err := env.managed.ListByJob(ctx, job.ID)
			if err != nil || len(managed) != 1 {
				t.Fatalf("metadata changed: %v %v", managed, err)
			}
		})
	}
}

package syncjob_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

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
			input := syncjob.CreateInput{Name: "local job", SourceID: src.ID, RemoteRoot: "/a", LocalRoot: filepath.Join(base, "b"), Mode: syncjob.ModeMirror, Enabled: true}
			unsafe := input
			unsafe.LocalRoot = filepath.Join(base, "a")
			if _, err := env.service.Create(ctx, unsafe); !errors.Is(err, syncjob.ErrInvalid) {
				t.Fatalf("create overlap=%v", err)
			}
			job, err := env.service.Create(ctx, input)
			if err != nil {
				t.Fatal(err)
			}
			seedManaged(t, env, job.ID)
			var upd syncjob.UpdateInput
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
			if _, err := env.service.Update(ctx, job.ID, upd); !errors.Is(err, syncjob.ErrInvalid) {
				t.Fatalf("update=%v", err)
			}
			if managedCount(t, env, job.ID) != 1 {
				t.Fatal("rejected update reset metadata")
			}
			got, err := env.service.Get(ctx, job.ID)
			if err != nil || got.SourceID != job.SourceID || got.RemoteRoot != job.RemoteRoot || got.LocalRoot != job.LocalRoot {
				t.Fatalf("job changed: %+v %v", got, err)
			}
		})
	}
}

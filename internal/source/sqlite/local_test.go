package sqlite

import (
	"context"
	"path/filepath"
	"testing"

	"tinysync/internal/source"
)

func TestLocalServiceRoundtrip(t *testing.T) {
	db, repo := openRepository(t)
	svc := source.NewService(repo, nil)
	ctx := context.Background()
	src, err := svc.Create(ctx, source.CreateInput{Name: "local", Type: source.TypeLocal, Config: source.Config{Local: &source.LocalConfig{Root: t.TempDir()}}, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	var raw string
	if err := db.QueryRow("SELECT credentials_json FROM sources WHERE id = ?", src.ID).Scan(&raw); err != nil || raw != "{}" {
		t.Fatalf("credentials=%q err=%v", raw, err)
	}
	creds, err := repo.GetCredentials(ctx, src.ID)
	if err != nil || creds != (source.Credentials{}) {
		t.Fatalf("credentials=%+v err=%v", creds, err)
	}
	if src.CredentialState != (source.CredentialState{}) {
		t.Fatalf("state=%+v", src.CredentialState)
	}
	next := source.Config{Local: &source.LocalConfig{Root: t.TempDir()}}
	want, err := filepath.EvalSymlinks(next.Local.Root)
	if err != nil {
		t.Fatal(err)
	}
	updated, err := svc.Update(ctx, src.ID, source.UpdateInput{Config: &next})
	if err != nil || updated.Config.Local.Root != want {
		t.Fatalf("update=%+v err=%v", updated, err)
	}
	all, err := svc.List(ctx)
	if err != nil || len(all) != 1 || all[0].Type != source.TypeLocal {
		t.Fatalf("list=%+v err=%v", all, err)
	}
}

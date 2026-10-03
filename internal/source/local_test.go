package source

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestPrepareLocalConfig(t *testing.T) {
	root := t.TempDir()
	canonical, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	cfg := Config{Local: &LocalConfig{Root: "  " + root + "  "}}
	got, err := PrepareConfig(TypeLocal, cfg)
	if err != nil || got.Local.Root != canonical {
		t.Fatalf("config=%+v err=%v", got, err)
	}
	if cfg.Local.Root == canonical {
		t.Fatal("Prepare mutated input")
	}
	mixed := cfg
	mixed.WebDAV = &WebDAVConfig{Endpoint: "https://example.com"}
	if _, err := PrepareConfig(TypeLocal, mixed); !errors.Is(err, ErrInvalid) {
		t.Fatalf("union: %v", err)
	}
	if _, err := PrepareConfig(TypeWebDAV, mixed); !errors.Is(err, ErrInvalid) {
		t.Fatalf("reverse union: %v", err)
	}
	for _, root := range []string{"", filepath.Join(root, "missing")} {
		if _, err := PrepareConfig(TypeLocal, Config{Local: &LocalConfig{Root: root}}); !errors.Is(err, ErrInvalid) {
			t.Fatalf("invalid: %v", err)
		}
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(root, link); err != nil {
		t.Skip(err)
	}
	alias, err := PrepareConfig(TypeLocal, Config{Local: &LocalConfig{Root: link}})
	if err != nil {
		t.Fatal(err)
	}
	if !RemoteIdentityEqual(Source{Type: TypeLocal, Config: got}, Source{Type: TypeLocal, Config: alias}) {
		t.Fatal("canonical identity differs")
	}
}

func TestLocalRejectsAllCredentials(t *testing.T) {
	for _, creds := range []Credentials{{WebDAV: &WebDAVCredentials{}}, {S3: &S3Credentials{}}, {SFTP: &SFTPCredentials{}}, {SMB: &SMBCredentials{}}, {GitHubRelease: &GitHubReleaseCredentials{}}} {
		if err := ValidateCredentials(TypeLocal, Config{}, creds); !errors.Is(err, ErrInvalid) {
			t.Fatalf("credentials: %v", err)
		}
	}
	if err := ValidateCredentials(TypeLocal, Config{}, Credentials{}); err != nil {
		t.Fatal(err)
	}
	if err := ValidateCredentialsUpdate(TypeLocal, &CredentialsUpdate{}); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	if state := CredentialStateOf(TypeLocal, Credentials{}); state != (CredentialState{}) {
		t.Fatalf("state=%+v", state)
	}
}

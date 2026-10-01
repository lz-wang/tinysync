// Package sqlite 的测试直接使用真实临时 SQLite 数据库，不 mock SQL。
package sqlite

import (
	"context"
	"reflect"
	"testing"
	"time"

	"tinysync/internal/notification"
	"tinysync/internal/storage"
)

// openRepository 打开临时数据库并完成 migration，返回仓库与清理函数。
func openRepository(t *testing.T) *Repository {
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
	return New(db)
}

func TestLoadDefaults(t *testing.T) {
	repo := openRepository(t)
	settings, err := repo.Load(context.Background())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := notification.Settings{
		Email: notification.EmailSettings{
			Port:     notification.DefaultEmailPort,
			Security: notification.DefaultEmailSecurity,
		},
	}
	if !reflect.DeepEqual(settings, want) {
		t.Fatalf("Load() = %+v, want %+v", settings, want)
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	repo := openRepository(t)
	now := time.Unix(1760000000, 0).UTC()
	saved := notification.Settings{
		Pushover: notification.PushoverSettings{
			Enabled: true,
			Token:   "azGDORePK8g0C1uRgZoRnqbsDFpBbB",
			UserKey: "uQiRzpo4DXghDmr9QxxfgoQ87UnVZ",
		},
		Email: notification.EmailSettings{
			Enabled:  true,
			Host:     "smtp.example.com",
			Port:     465,
			Security: notification.SecurityTLS,
			Username: "tinysync@example.com",
			Password: "s3cret-pass",
			From:     "tinysync@example.com",
			To:       []string{"me@example.com", "ops@example.com"},
		},
	}
	if err := saved.Validate(); err != nil {
		t.Fatalf("fixture Validate: %v", err)
	}
	if err := repo.Save(context.Background(), saved, now); err != nil {
		t.Fatalf("Save: %v", err)
	}

	loaded, err := repo.Load(context.Background())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := saved
	want.UpdatedAt = now
	if !reflect.DeepEqual(loaded, want) {
		t.Fatalf("Load() = %+v, want %+v", loaded, want)
	}
}

func TestSaveClearsRecipients(t *testing.T) {
	repo := openRepository(t)
	now := time.Unix(1760000000, 0).UTC()
	first := notification.Settings{
		Email: notification.EmailSettings{
			Port:     notification.DefaultEmailPort,
			Security: notification.DefaultEmailSecurity,
			From:     "tinysync@example.com",
			To:       []string{"me@example.com"},
		},
	}
	if err := repo.Save(context.Background(), first, now); err != nil {
		t.Fatalf("Save: %v", err)
	}
	cleared := first
	cleared.Email.To = nil
	if err := repo.Save(context.Background(), cleared, now.Add(time.Second)); err != nil {
		t.Fatalf("Save: %v", err)
	}
	loaded, err := repo.Load(context.Background())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.Email.To != nil {
		t.Fatalf("Email.To = %v, want nil", loaded.Email.To)
	}
}

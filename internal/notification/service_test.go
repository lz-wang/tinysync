package notification

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

// fakeRepo 是内存 Repository：记录 Save 调用以断言落库内容与顺序。
type fakeRepo struct {
	settings Settings
	saved    []Settings
	loadErr  error
}

func (f *fakeRepo) Load(ctx context.Context) (Settings, error) {
	if f.loadErr != nil {
		return Settings{}, f.loadErr
	}
	return f.settings, nil
}

func (f *fakeRepo) Save(ctx context.Context, settings Settings, updatedAt time.Time) error {
	f.saved = append(f.saved, settings)
	f.settings = settings
	return nil
}

func boolPtr(b bool) *bool        { return &b }
func strPtr(s string) *string     { return &s }
func intPtr(i int) *int           { return &i }
func secPtr(s Security) *Security { return &s }

func TestServiceUpdatePatchSemantics(t *testing.T) {
	now := time.Unix(1760000000, 0).UTC()
	tests := []struct {
		name    string
		initial Settings
		patch   PatchInput
		want    Settings
	}{
		{
			name:    "empty patch keeps everything",
			initial: validSettings(),
			patch:   PatchInput{},
			want:    validSettings(),
		},
		{
			name:    "nil channel patch keeps everything",
			initial: validSettings(),
			patch:   PatchInput{Pushover: nil, Email: nil},
			want:    validSettings(),
		},
		{
			name:    "replace secret by string",
			initial: validSettings(),
			patch: PatchInput{Pushover: &PushoverPatch{
				Token:   strPtr("new-token"),
				UserKey: strPtr("new-key"),
			}},
			want: func() Settings {
				s := validSettings()
				s.Pushover.Token = "new-token"
				s.Pushover.UserKey = "new-key"
				return s
			}(),
		},
		{
			name:    "empty string replaces (not clear)",
			initial: validSettings(),
			patch: PatchInput{Pushover: &PushoverPatch{
				Enabled: boolPtr(false),
				Token:   strPtr(""),
			}},
			want: func() Settings {
				s := validSettings()
				s.Pushover.Enabled = false
				s.Pushover.Token = ""
				return s
			}(),
		},
		{
			name:    "clear deletes secret",
			initial: validSettings(),
			patch: PatchInput{Pushover: &PushoverPatch{
				Enabled:    boolPtr(false),
				ClearToken: true,
			}},
			want: func() Settings {
				s := validSettings()
				s.Pushover.Enabled = false
				s.Pushover.Token = ""
				return s
			}(),
		},
		{
			name:    "clear takes precedence over replace",
			initial: validSettings(),
			patch: PatchInput{Email: &EmailPatch{
				Enabled:       boolPtr(false),
				Password:      strPtr("ignored"),
				ClearPassword: true,
			}},
			want: func() Settings {
				s := validSettings()
				s.Email.Enabled = false
				s.Email.Password = ""
				return s
			}(),
		},
		{
			name:    "email scalar fields replace",
			initial: validSettings(),
			patch: PatchInput{Email: &EmailPatch{
				Host:     strPtr("smtp2.example.com"),
				Port:     intPtr(465),
				Security: secPtr(SecurityTLS),
				Username: strPtr("tinysync"),
				From:     strPtr("tinysync@new.example.com"),
			}},
			want: func() Settings {
				s := validSettings()
				s.Email.Host = "smtp2.example.com"
				s.Email.Port = 465
				s.Email.Security = SecurityTLS
				s.Email.Username = "tinysync"
				s.Email.From = "tinysync@new.example.com"
				return s
			}(),
		},
		{
			name:    "to replaced as whole list",
			initial: validSettings(),
			patch: PatchInput{Email: &EmailPatch{
				To: []string{"a@example.com", "b@example.com"},
			}},
			want: func() Settings {
				s := validSettings()
				s.Email.To = []string{"a@example.com", "b@example.com"}
				return s
			}(),
		},
		{
			name:    "to empty slice clears recipients",
			initial: validSettings(),
			patch: PatchInput{Email: &EmailPatch{
				Enabled: boolPtr(false),
				To:      []string{},
			}},
			want: func() Settings {
				s := validSettings()
				s.Email.Enabled = false
				s.Email.To = nil
				return s
			}(),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := &fakeRepo{settings: tt.initial}
			svc := NewService(repo)
			svc.Now = func() time.Time { return now }
			got, err := svc.Update(context.Background(), tt.patch)
			if err != nil {
				t.Fatalf("Update() = %v, want nil", err)
			}
			want := tt.want
			want.UpdatedAt = now
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("Update() = %+v, want %+v", got, want)
			}
			if len(repo.saved) != 1 {
				t.Fatalf("Save called %d times, want 1", len(repo.saved))
			}
		})
	}
}

func TestServiceUpdateInvalidDoesNotSave(t *testing.T) {
	repo := &fakeRepo{settings: validSettings()}
	svc := NewService(repo)
	// 启用但清掉 token：合并后校验失败。
	_, err := svc.Update(context.Background(), PatchInput{Pushover: &PushoverPatch{
		ClearToken: true,
	}})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("Update() = %v, want ErrInvalid", err)
	}
	if len(repo.saved) != 0 {
		t.Fatalf("Save called %d times after invalid input, want 0", len(repo.saved))
	}
}

func TestServiceSettings(t *testing.T) {
	repo := &fakeRepo{settings: validSettings()}
	svc := NewService(repo)
	got, err := svc.Settings(context.Background())
	if err != nil {
		t.Fatalf("Settings() = %v, want nil", err)
	}
	if !reflect.DeepEqual(got, validSettings()) {
		t.Fatalf("Settings() = %+v, want %+v", got, validSettings())
	}

	repo.loadErr = errors.New("boom")
	if _, err := svc.Settings(context.Background()); err == nil {
		t.Fatal("Settings() with load error = nil, want error")
	}
}

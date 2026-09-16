// Service 的测试：真实 SQLite 仓库 + 受控 stub factory。
// 外部测试包：需引用 sqlite 仓库实现，避免测试 import 环。
package source_test

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"tinysync/internal/source"
	"tinysync/internal/source/sqlite"
	"tinysync/internal/storage"
)

// stubRemote 是可控的 Remote：Stat 返回预设结果。
type stubRemote struct {
	statErr error
	lastCtx context.Context
}

func (r *stubRemote) Stat(ctx context.Context, path string) (source.FileInfo, error) {
	r.lastCtx = ctx
	if r.statErr != nil {
		return source.FileInfo{}, r.statErr
	}
	return source.FileInfo{Path: path, IsDir: true}, nil
}

func (r *stubRemote) List(ctx context.Context, path string) ([]source.FileInfo, error) {
	return nil, errors.New("not implemented")
}

func (r *stubRemote) Open(ctx context.Context, path string) (io.ReadCloser, error) {
	return nil, errors.New("not implemented")
}

func (r *stubRemote) Close() error {
	return nil
}

// stubFactory 返回预设 Remote 并记录最近一次构造参数。
type stubFactory struct {
	remote       source.Remote
	lastSource   source.Source
	lastPassword string
}

func (f *stubFactory) Type() source.Type {
	return source.TypeWebDAV
}

func (f *stubFactory) Create(ctx context.Context, s source.Source, password string) (source.Remote, error) {
	f.lastSource = s
	f.lastPassword = password
	return f.remote, nil
}

// newTestService 用真实临时 SQLite 仓库与 stub factory 构造 Service，
// 并固定时钟便于断言。
func newTestService(t *testing.T, remote source.Remote) (*source.Service, *stubFactory) {
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
	factory := &stubFactory{remote: remote}
	svc := source.NewService(sqlite.New(db), factory)
	base := time.Unix(1757879400, 0).UTC()
	svc.Now = func() time.Time { return base }
	return svc, factory
}

// validInput 返回合法创建输入。
func validInput() source.CreateInput {
	return source.CreateInput{
		Name: "  NAS  ",
		Type: source.TypeWebDAV,
		Config: source.Config{WebDAV: &source.WebDAVConfig{
			Endpoint: " https://dav.example.com/files ",
			Username: "user",
		}},
		Credentials: source.Credentials{WebDAV: &source.WebDAVCredentials{
			Password: "secret",
		}},
		Enabled: true,
	}
}

// Create 完成 trim、校验、ID 与时间戳生成并持久化。
func TestServiceCreate(t *testing.T) {
	svc, _ := newTestService(t, &stubRemote{})

	got, err := svc.Create(context.Background(), validInput())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if got.Name != "NAS" || got.Config.WebDAV.Endpoint != "https://dav.example.com/files" {
		t.Errorf("Create trimmed fields = %+v", got)
	}
	if len(got.ID) < 4 || got.ID[:4] != "src_" {
		t.Errorf("ID = %q, want src_ prefix", got.ID)
	}
	if !got.CredentialState.WebDAV.PasswordSet || !got.Enabled {
		t.Errorf("flags = %+v", got)
	}
	if !got.CreatedAt.Equal(got.UpdatedAt) {
		t.Error("CreatedAt != UpdatedAt on create")
	}

	reread, err := svc.Get(context.Background(), got.ID)
	if err != nil {
		t.Fatalf("Get after create: %v", err)
	}
	if reread.Name != "NAS" {
		t.Errorf("persisted name = %q", reread.Name)
	}
}

// Create 校验失败返回 ErrInvalid，不落库。
func TestServiceCreateValidation(t *testing.T) {
	svc, _ := newTestService(t, &stubRemote{})
	ctx := context.Background()

	bad := validInput()
	bad.Name = "   "
	if _, err := svc.Create(ctx, bad); !errors.Is(err, source.ErrInvalid) {
		t.Errorf("blank name = %v, want ErrInvalid", err)
	}
	// type 与 config 不一致：声明 s3 却携带 webdav config。
	bad = validInput()
	bad.Type = source.TypeS3
	if _, err := svc.Create(ctx, bad); !errors.Is(err, source.ErrInvalid) {
		t.Errorf("type/config mismatch = %v, want ErrInvalid", err)
	}
	bad = validInput()
	bad.Config.WebDAV.Endpoint = "https://user:pass@host/"
	if _, err := svc.Create(ctx, bad); !errors.Is(err, source.ErrInvalid) {
		t.Errorf("bad endpoint = %v, want ErrInvalid", err)
	}
}

// 重名创建透传 ErrConflict。
func TestServiceCreateDuplicate(t *testing.T) {
	svc, _ := newTestService(t, &stubRemote{})
	ctx := context.Background()

	if _, err := svc.Create(ctx, validInput()); err != nil {
		t.Fatalf("first Create: %v", err)
	}
	dup := validInput()
	dup.Name = "nas"
	if _, err := svc.Create(ctx, dup); !errors.Is(err, source.ErrConflict) {
		t.Errorf("duplicate = %v, want ErrConflict", err)
	}
}

// Update 部分更新：未提供的字段与密码保持不变。
func TestServiceUpdatePartial(t *testing.T) {
	svc, _ := newTestService(t, &stubRemote{})
	// 每次读取时间递增一分钟，保证 UpdatedAt 可观测地前进。
	clock := time.Unix(1757879400, 0).UTC()
	svc.Now = func() time.Time {
		clock = clock.Add(time.Minute)
		return clock
	}
	ctx := context.Background()

	created, err := svc.Create(ctx, validInput())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	newName := "Renamed"
	updated, err := svc.Update(ctx, created.ID, source.UpdateInput{Name: &newName})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if updated.Name != "Renamed" {
		t.Errorf("name = %q, want Renamed", updated.Name)
	}
	if updated.Config.WebDAV.Endpoint != created.Config.WebDAV.Endpoint ||
		updated.Config.WebDAV.Username != created.Config.WebDAV.Username || !updated.Enabled {
		t.Errorf("untouched fields changed: %+v", updated)
	}
	if !updated.CredentialState.WebDAV.PasswordSet {
		t.Error("PasswordSet = false, want unchanged true")
	}
	if !updated.UpdatedAt.After(created.UpdatedAt) {
		t.Errorf("UpdatedAt %v not advanced after %v", updated.UpdatedAt, created.UpdatedAt)
	}

	// 密码清除后 PasswordSet 同步为 false。
	empty := ""
	updated, err = svc.Update(ctx, created.ID, source.UpdateInput{
		Credentials: &source.CredentialsUpdate{
			WebDAV: &source.WebDAVCredentialsUpdate{Password: &empty},
		},
	})
	if err != nil {
		t.Fatalf("Update clear password: %v", err)
	}
	if updated.CredentialState.WebDAV.PasswordSet {
		t.Error("PasswordSet = true after clearing, want false")
	}
}

// Update 校验失败与未知 ID。
func TestServiceUpdateErrors(t *testing.T) {
	svc, _ := newTestService(t, &stubRemote{})
	ctx := context.Background()

	if _, err := svc.Update(ctx, "src_missing", source.UpdateInput{}); !errors.Is(err, source.ErrNotFound) {
		t.Errorf("update unknown = %v, want ErrNotFound", err)
	}

	created, err := svc.Create(ctx, validInput())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	badConfig := source.Config{WebDAV: &source.WebDAVConfig{Endpoint: "ftp://x/"}}
	if _, err := svc.Update(ctx, created.ID, source.UpdateInput{Config: &badConfig}); !errors.Is(err, source.ErrInvalid) {
		t.Errorf("update bad endpoint = %v, want ErrInvalid", err)
	}
}

// Delete 透传仓库行为。
func TestServiceDelete(t *testing.T) {
	svc, _ := newTestService(t, &stubRemote{})
	ctx := context.Background()

	created, err := svc.Create(ctx, validInput())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := svc.Delete(ctx, created.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if err := svc.Delete(ctx, created.ID); !errors.Is(err, source.ErrNotFound) {
		t.Errorf("delete twice = %v, want ErrNotFound", err)
	}
}

// TestConnection 成功：Stat 收到根路径与 10s 超时 context。
func TestServiceTestConnectionSuccess(t *testing.T) {
	remote := &stubRemote{}
	svc, factory := newTestService(t, remote)
	ctx := context.Background()

	created, err := svc.Create(ctx, validInput())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	result, err := svc.TestConnection(ctx, created.ID)
	if err != nil {
		t.Fatalf("TestConnection: %v", err)
	}
	if !result.OK {
		t.Errorf("result = %+v, want OK", result)
	}
	if result.LatencyMS < 0 {
		t.Errorf("latency = %d, want >= 0", result.LatencyMS)
	}
	if factory.lastPassword != "secret" {
		t.Errorf("factory password = %q, want secret", factory.lastPassword)
	}
	if remote.lastCtx == nil {
		t.Fatal("Stat was not called")
	}
	if _, ok := remote.lastCtx.Deadline(); !ok {
		t.Error("Stat ctx has no deadline, want 10s timeout")
	}
}

// TestConnection 失败也是一次成功完成的测试操作：返回结果而非错误。
func TestServiceTestConnectionFailure(t *testing.T) {
	remote := &stubRemote{statErr: errors.New("authentication failed")}
	svc, _ := newTestService(t, remote)
	ctx := context.Background()

	created, err := svc.Create(ctx, validInput())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	result, err := svc.TestConnection(ctx, created.ID)
	if err != nil {
		t.Fatalf("TestConnection returned error: %v", err)
	}
	if result.OK {
		t.Error("result.OK = true, want false")
	}
	if result.Error == "" {
		t.Error("result.Error empty, want failure reason")
	}
}

// 未知 ID 的连接测试返回 ErrNotFound。
func TestServiceTestConnectionUnknownID(t *testing.T) {
	svc, _ := newTestService(t, &stubRemote{})
	if _, err := svc.TestConnection(context.Background(), "src_missing"); !errors.Is(err, source.ErrNotFound) {
		t.Errorf("TestConnection unknown = %v, want ErrNotFound", err)
	}
}

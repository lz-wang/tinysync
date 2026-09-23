// 凭据引用（ADR 0005）的领域验证：互斥不变量、存在性校验、引用态
// secret 解析与换钥传播。使用真实 SQLite 仓库 + 凭据服务，factory
// 为捕获桩。
package source_test

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"strings"
	"testing"

	"tinysync/internal/credential"
	"tinysync/internal/credential/keytest"
	credentialsqlite "tinysync/internal/credential/sqlite"
	"tinysync/internal/source"
	"tinysync/internal/source/sqlite"
	"tinysync/internal/storage"
)

// captureRemote 是无操作 Remote 桩。
type captureRemote struct{}

func (captureRemote) Stat(context.Context, string) (source.FileInfo, error) {
	return source.FileInfo{}, errors.New("not implemented")
}

func (captureRemote) List(context.Context, string, source.ListOptions) (source.FilePage, error) {
	return source.FilePage{}, errors.New("not implemented")
}

func (captureRemote) Open(context.Context, string) (io.ReadCloser, error) {
	return nil, errors.New("not implemented")
}

func (captureRemote) Close() error { return nil }

// captureFactory 捕获 Create 收到的凭据：断言引用解析的合成结果。
type captureFactory struct {
	creds source.Credentials
}

func (f *captureFactory) Type() source.Type { return source.TypeSFTP }

func (f *captureFactory) Create(_ context.Context, _ source.Source, c source.Credentials) (source.Remote, error) {
	f.creds = c
	return captureRemote{}, nil
}

// referenceEnv 持有真实装配的服务、数据库与捕获 factory。
type referenceEnv struct {
	db          *sql.DB
	sources     *source.Service
	credentials *credential.Service
	factory     *captureFactory
}

// newReferenceEnv 构造真实装配：source 服务带凭据 resolver，双仓库
// 共享同一临时数据库。
func newReferenceEnv(t *testing.T) *referenceEnv {
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
	credentials := credential.NewService(credentialsqlite.New(db))
	factory := &captureFactory{}
	sources := source.NewService(sqlite.New(db), factory)
	sources.Credentials = credentials
	return &referenceEnv{db: db, sources: sources, credentials: credentials, factory: factory}
}

// newKeyCredential 创建一把未加密 ed25519 钥匙凭据。
func newKeyCredential(t *testing.T, env *referenceEnv, name string) credential.Credential {
	t.Helper()
	pem, err := keytest.UnencryptedEd25519()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	c, err := env.credentials.Create(context.Background(), credential.CreateInput{
		Name:   name,
		Type:   credential.TypeSSHKey,
		Secret: credential.Secret{PrivateKey: pem},
	})
	if err != nil {
		t.Fatalf("create credential: %v", err)
	}
	return c
}

// mustNewKeyPEM 生成新钥匙 PEM。
func mustNewKeyPEM(t *testing.T) string {
	t.Helper()
	pem, err := keytest.UnencryptedEd25519()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	return pem
}

// resolvedCreds 经 OpenRemote 捕获 factory 实际收到的凭据。
func resolvedCreds(t *testing.T, env *referenceEnv, id string) source.Credentials {
	t.Helper()
	env.factory.creds = source.Credentials{}
	if _, _, err := env.sources.OpenRemote(context.Background(), id); err != nil {
		t.Fatalf("OpenRemote: %v", err)
	}
	return env.factory.creds
}

// 引用态生命周期：存在性校验、互斥拒绝、OpenRemote 合成引用 secret、
// 换钥传播、解绑回内联、引用失效 fail-closed。
func TestCredentialReferenceLifecycle(t *testing.T) {
	ctx := context.Background()
	env := newReferenceEnv(t)
	key1 := newKeyCredential(t, env, "NAS 钥匙")

	// 引用态创建：正常。
	refCfg := source.SFTPConfig{
		Host: "nas.example.com", Port: 22, Username: "tinysync",
		AuthMethod: source.SFTPAuthPrivateKey, CredentialID: key1.ID,
	}
	src, err := env.sources.Create(ctx, source.CreateInput{
		Name:   "NAS",
		Type:   source.TypeSFTP,
		Config: source.Config{SFTP: &refCfg},
	})
	if err != nil {
		t.Fatalf("create referencing source: %v", err)
	}

	// 引用不存在的凭据：入口拒绝。
	badCfg := refCfg
	badCfg.CredentialID = "crd_missing"
	if _, err := env.sources.Create(ctx, source.CreateInput{
		Name: "bad", Type: source.TypeSFTP, Config: source.Config{SFTP: &badCfg},
	}); !errors.Is(err, source.ErrInvalid) || !strings.Contains(err.Error(), "does not exist") {
		t.Errorf("missing reference err = %v, want ErrInvalid does not exist", err)
	}

	// 互斥：引用态携带内联私钥 → 拒绝。
	mutexCfg := refCfg
	if _, err := env.sources.Create(ctx, source.CreateInput{
		Name:        "mutex",
		Type:        source.TypeSFTP,
		Config:      source.Config{SFTP: &mutexCfg},
		Credentials: source.Credentials{SFTP: &source.SFTPCredentials{PrivateKey: "junk"}},
	}); !errors.Is(err, source.ErrInvalid) || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Errorf("inline+reference err = %v, want ErrInvalid mutually exclusive", err)
	}

	// 互斥：credential_id 配 password 方式 → 拒绝。
	pwCfg := refCfg
	pwCfg.AuthMethod = source.SFTPAuthPassword
	if _, err := env.sources.Create(ctx, source.CreateInput{
		Name: "pw", Type: source.TypeSFTP, Config: source.Config{SFTP: &pwCfg},
		Credentials: source.Credentials{SFTP: &source.SFTPCredentials{Password: "x"}},
	}); !errors.Is(err, source.ErrInvalid) || !strings.Contains(err.Error(), "requires auth_method=private_key") {
		t.Errorf("reference+password err = %v, want ErrInvalid", err)
	}

	// OpenRemote：引用态合成凭据库中的私钥，源自身 secret 为空。
	got := resolvedCreds(t, env, src.ID)
	if got.SFTP == nil || got.SFTP.PrivateKey == "" || got.SFTP.PrivateKeyPassphrase != "" {
		t.Errorf("resolved creds = %+v, want referenced private key", got.SFTP)
	}

	// 换钥传播：凭据换钥后下一次构造即用新钥。
	newPEM := mustNewKeyPEM(t)
	if _, err := env.credentials.Update(ctx, key1.ID, credential.UpdateInput{
		Secret: &credential.Secret{PrivateKey: newPEM},
	}); err != nil {
		t.Fatalf("rekey credential: %v", err)
	}
	got = resolvedCreds(t, env, src.ID)
	if got.SFTP == nil || got.SFTP.PrivateKey != newPEM {
		t.Errorf("resolved creds after rekey = %+v, want new key", got.SFTP)
	}

	// 解绑改内联：PATCH config（credential_id 置空）+ 写入内联私钥。
	inlineKey := mustNewKeyPEM(t)
	newCfg := refCfg
	newCfg.CredentialID = ""
	if _, err := env.sources.Update(ctx, src.ID, source.UpdateInput{
		Config: &source.Config{SFTP: &newCfg},
		Credentials: &source.CredentialsUpdate{SFTP: &source.SFTPCredentialsUpdate{
			PrivateKey: &inlineKey,
		}},
	}); err != nil {
		t.Fatalf("unbind update: %v", err)
	}
	got = resolvedCreds(t, env, src.ID)
	if got.SFTP == nil || got.SFTP.PrivateKey != inlineKey {
		t.Errorf("inline creds after unbind = %+v, want inline key", got.SFTP)
	}

	// 引用失效：重新绑定后，绕过删除守卫直接移除凭据行（模拟外部
	// 破坏）→ OpenRemote 确定性失败，绝不静默。
	if _, err := env.sources.Update(ctx, src.ID, source.UpdateInput{
		Config: &source.Config{SFTP: &refCfg},
		Credentials: &source.CredentialsUpdate{SFTP: &source.SFTPCredentialsUpdate{
			PrivateKey: new(string),
		}},
	}); err != nil {
		t.Fatalf("rebind update: %v", err)
	}

	// 只带 credentials 的 PATCH（无 config）打在引用态源上：内联钥
	// 被强制清除，存储不出现「引用 + 沉睡内联钥」。
	staleKey := mustNewKeyPEM(t)
	if _, err := env.sources.Update(ctx, src.ID, source.UpdateInput{
		Credentials: &source.CredentialsUpdate{SFTP: &source.SFTPCredentialsUpdate{
			PrivateKey: &staleKey,
		}},
	}); err != nil {
		t.Fatalf("credentials-only update on referenced source: %v", err)
	}
	got = resolvedCreds(t, env, src.ID)
	if got.SFTP == nil || got.SFTP.PrivateKey == staleKey {
		t.Errorf("resolved creds = inline pasted key, want referenced credential key")
	}
	if _, err := env.db.ExecContext(ctx, "DELETE FROM credentials"); err != nil {
		t.Fatalf("raw delete credential: %v", err)
	}
	if _, _, err := env.sources.OpenRemote(ctx, src.ID); err == nil {
		t.Fatalf("OpenRemote with dangling reference: want error, got nil")
	} else if source.IsRetryable(err) {
		t.Errorf("dangling reference err = %v, want permanent (non-retryable)", err)
	}
}

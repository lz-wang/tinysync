package credential

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// fakeRepo 是内存 Repository：记录调用并模拟 name 冲突与 NotFound。
type fakeRepo struct {
	creds map[string]Credential
	// renamed / replacedSecrets / deleted 记录各写路径的调用。
	renamed         []string
	replacedSecrets []*Secret
	deleted         []string
	// deleteErr 非 nil 时 Delete 返回该错误。
	deleteErr error
	// secrets 记录 Create / ReplaceSecret 写入的 secret（GetSecret 读回）。
	secrets map[string]Secret
}

func newFakeRepo() *fakeRepo {
	return &fakeRepo{creds: map[string]Credential{}, secrets: map[string]Secret{}}
}

func (r *fakeRepo) Create(_ context.Context, c Credential, secret Secret) error {
	for _, existing := range r.creds {
		if strings.EqualFold(existing.Name, c.Name) {
			return ErrConflict
		}
	}
	r.creds[c.ID] = c
	r.secrets[c.ID] = secret
	return nil
}

func (r *fakeRepo) GetSecret(_ context.Context, id string) (Secret, error) {
	secret, ok := r.secrets[id]
	if !ok {
		return Secret{}, errNotFound(id)
	}
	return secret, nil
}

func (r *fakeRepo) Get(_ context.Context, id string) (Credential, error) {
	c, ok := r.creds[id]
	if !ok {
		return Credential{}, errNotFound(id)
	}
	return c, nil
}

func (r *fakeRepo) List(_ context.Context) ([]Credential, error) {
	list := make([]Credential, 0, len(r.creds))
	for _, c := range r.creds {
		list = append(list, c)
	}
	return list, nil
}

func (r *fakeRepo) Rename(_ context.Context, id string, name string, _ time.Time) error {
	c, ok := r.creds[id]
	if !ok {
		return errNotFound(id)
	}
	for _, existing := range r.creds {
		if existing.ID != id && strings.EqualFold(existing.Name, name) {
			return ErrConflict
		}
	}
	c.Name = name
	r.creds[id] = c
	r.renamed = append(r.renamed, id)
	return nil
}

func (r *fakeRepo) ReplaceSecret(_ context.Context, id string, secret Secret, fingerprint string, hasPassphrase bool, _ time.Time) error {
	c, ok := r.creds[id]
	if !ok {
		return errNotFound(id)
	}
	c.Fingerprint = fingerprint
	c.HasPassphrase = hasPassphrase
	r.creds[id] = c
	r.secrets[id] = secret
	r.replacedSecrets = append(r.replacedSecrets, &secret)
	return nil
}

func (r *fakeRepo) Delete(_ context.Context, id string) error {
	if r.deleteErr != nil {
		return r.deleteErr
	}
	if _, ok := r.creds[id]; !ok {
		return errNotFound(id)
	}
	delete(r.creds, id)
	r.deleted = append(r.deleted, id)
	return nil
}

func errNotFound(id string) error {
	return errors.New("not found: " + id)
}

// newTestService 构造固定时钟的服务。
func newTestService(repo Repository) *Service {
	svc := NewService(repo)
	now := time.Unix(1760000000, 0).UTC()
	svc.Now = func() time.Time { return now }
	return svc
}

// 创建：指纹随解析派生，口令状态正确，名称 trim，ID 带前缀。
func TestServiceCreate(t *testing.T) {
	repo := newFakeRepo()
	svc := newTestService(repo)

	c, err := svc.Create(context.Background(), CreateInput{
		Name:   "  NAS 钥匙  ",
		Type:   TypeSSHKey,
		Secret: Secret{PrivateKey: testKeyUnencrypted},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if !strings.HasPrefix(c.ID, "crd_") {
		t.Errorf("ID = %q, want crd_ prefix", c.ID)
	}
	if c.Name != "NAS 钥匙" {
		t.Errorf("Name = %q, want trimmed", c.Name)
	}
	if c.Fingerprint != testKeyFingerprint {
		t.Errorf("Fingerprint = %q, want %q", c.Fingerprint, testKeyFingerprint)
	}
	if c.HasPassphrase {
		t.Errorf("HasPassphrase = true, want false")
	}
	if !c.CreatedAt.Equal(c.UpdatedAt) {
		t.Errorf("timestamps differ: %v / %v", c.CreatedAt, c.UpdatedAt)
	}
}

// 创建带口令钥匙：HasPassphrase 回显为真。
func TestServiceCreateWithPassphrase(t *testing.T) {
	svc := newTestService(newFakeRepo())

	c, err := svc.Create(context.Background(), CreateInput{
		Name:   "enc",
		Type:   TypeSSHKey,
		Secret: Secret{PrivateKey: testKeyEncrypted, PrivateKeyPassphrase: testKeyPassphrase},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if !c.HasPassphrase {
		t.Errorf("HasPassphrase = false, want true")
	}
}

// 创建校验失败：未知类型、坏钥、空名。
func TestServiceCreateValidation(t *testing.T) {
	cases := []struct {
		name  string
		input CreateInput
		want  string
	}{
		{"unknown type", CreateInput{Name: "x", Type: "webdav"}, "unsupported credential type"},
		{"bad key", CreateInput{Name: "x", Type: TypeSSHKey, Secret: Secret{PrivateKey: "junk"}}, "invalid credential"},
		{"empty name", CreateInput{Name: "  ", Type: TypeSSHKey, Secret: Secret{PrivateKey: testKeyUnencrypted}}, "name is required"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := newTestService(newFakeRepo())
			_, err := svc.Create(context.Background(), tc.input)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want containing %q", err, tc.want)
			}
		})
	}
}

// name 冲突从 Repository 透传。
func TestServiceCreateConflict(t *testing.T) {
	repo := newFakeRepo()
	svc := newTestService(repo)
	if _, err := svc.Create(context.Background(), CreateInput{Name: "a", Type: TypeSSHKey, Secret: Secret{PrivateKey: testKeyUnencrypted}}); err != nil {
		t.Fatalf("first Create: %v", err)
	}
	if _, err := svc.Create(context.Background(), CreateInput{Name: "A", Type: TypeSSHKey, Secret: Secret{PrivateKey: testKeyUnencrypted}}); !errors.Is(err, ErrConflict) {
		t.Errorf("err = %v, want ErrConflict", err)
	}
}

// 只改名：走 Rename 路径，不触碰 secret 三列，指纹与口令状态不变。
func TestServiceUpdateNameOnly(t *testing.T) {
	repo := newFakeRepo()
	svc := newTestService(repo)
	created, err := svc.Create(context.Background(), CreateInput{Name: "old", Type: TypeSSHKey, Secret: Secret{PrivateKey: testKeyEncrypted, PrivateKeyPassphrase: testKeyPassphrase}})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	svc.Now = func() time.Time { return created.UpdatedAt.Add(time.Minute) }

	newName := "new"
	updated, err := svc.Update(context.Background(), created.ID, UpdateInput{Name: &newName})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if updated.Name != "new" || updated.Fingerprint != testKeyFingerprint || !updated.HasPassphrase {
		t.Errorf("updated = %+v, want renamed with secret state preserved", updated)
	}
	if !updated.UpdatedAt.After(created.UpdatedAt) {
		t.Errorf("UpdatedAt = %v, want advanced", updated.UpdatedAt)
	}
	if len(repo.renamed) != 1 || repo.renamed[0] != created.ID || len(repo.replacedSecrets) != 0 {
		t.Errorf("calls renamed=%v replaced=%v, want rename only", repo.renamed, repo.replacedSecrets)
	}
}

// 更换 secret：走 ReplaceSecret 路径并派生新指纹与口令状态。
func TestServiceUpdateSecret(t *testing.T) {
	repo := newFakeRepo()
	svc := newTestService(repo)
	created, err := svc.Create(context.Background(), CreateInput{Name: "a", Type: TypeSSHKey, Secret: Secret{PrivateKey: testKeyUnencrypted}})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	updated, err := svc.Update(context.Background(), created.ID, UpdateInput{
		Secret: &Secret{PrivateKey: testKeyEncrypted, PrivateKeyPassphrase: testKeyPassphrase},
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if updated.Fingerprint != testKeyFingerprint {
		t.Errorf("Fingerprint = %q, want %q (same key re-encrypted)", updated.Fingerprint, testKeyFingerprint)
	}
	if !updated.HasPassphrase {
		t.Errorf("HasPassphrase = false, want true")
	}
	if len(repo.replacedSecrets) != 1 || len(repo.renamed) != 0 {
		t.Errorf("calls renamed=%v replaced=%v, want replace only", repo.renamed, repo.replacedSecrets)
	}
}

// 更换 secret 解析失败：拒绝且不落库。
func TestServiceUpdateInvalidSecret(t *testing.T) {
	repo := newFakeRepo()
	svc := newTestService(repo)
	created, err := svc.Create(context.Background(), CreateInput{Name: "a", Type: TypeSSHKey, Secret: Secret{PrivateKey: testKeyUnencrypted}})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if _, err := svc.Update(context.Background(), created.ID, UpdateInput{Secret: &Secret{PrivateKey: "junk"}}); !errors.Is(err, ErrInvalid) {
		t.Errorf("err = %v, want ErrInvalid", err)
	}
	got, err := svc.Get(context.Background(), created.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Fingerprint != testKeyFingerprint {
		t.Errorf("Fingerprint = %q, want unchanged", got.Fingerprint)
	}
	if len(repo.replacedSecrets)+len(repo.renamed) != 0 {
		t.Errorf("storage writes = %d/%d, want none", len(repo.renamed), len(repo.replacedSecrets))
	}
}

// 新名非法：在任何写入之前拒绝，secret 更改不会被半提交。
func TestServiceUpdateInvalidNameRejectedBeforeWrite(t *testing.T) {
	repo := newFakeRepo()
	svc := newTestService(repo)
	created, err := svc.Create(context.Background(), CreateInput{Name: "a", Type: TypeSSHKey, Secret: Secret{PrivateKey: testKeyUnencrypted}})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	empty := "   "
	if _, err := svc.Update(context.Background(), created.ID, UpdateInput{
		Name:   &empty,
		Secret: &Secret{PrivateKey: testKeyEncrypted, PrivateKeyPassphrase: testKeyPassphrase},
	}); err == nil {
		t.Fatalf("Update with invalid name: want error, got nil")
	}
	if len(repo.replacedSecrets)+len(repo.renamed) != 0 {
		t.Errorf("storage writes = %d/%d, want none", len(repo.renamed), len(repo.replacedSecrets))
	}
}

// 空 PATCH：不触碰存储，直接返回现状。
func TestServiceUpdateEmpty(t *testing.T) {
	repo := newFakeRepo()
	svc := newTestService(repo)
	created, err := svc.Create(context.Background(), CreateInput{Name: "a", Type: TypeSSHKey, Secret: Secret{PrivateKey: testKeyUnencrypted}})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	updated, err := svc.Update(context.Background(), created.ID, UpdateInput{})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if updated != created {
		t.Errorf("updated = %+v, want unchanged", updated)
	}
	if len(repo.renamed)+len(repo.replacedSecrets)+len(repo.deleted) != 0 {
		t.Errorf("storage writes occurred, want none")
	}
}

// 删除：ErrInUse（含清单）与 ErrNotFound 均从存储层透传。
func TestServiceDelete(t *testing.T) {
	repo := newFakeRepo()
	svc := newTestService(repo)
	created, err := svc.Create(context.Background(), CreateInput{Name: "a", Type: TypeSSHKey, Secret: Secret{PrivateKey: testKeyUnencrypted}})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := svc.Delete(context.Background(), created.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := svc.Get(context.Background(), created.ID); err == nil {
		t.Errorf("Get after delete: want error, got nil")
	}

	inUse := &ErrInUse{Sources: []SourceRef{{ID: "src_1", Name: "NAS"}}}
	repo.deleteErr = inUse
	err = svc.Delete(context.Background(), "crd_any")
	if !errors.As(err, new(*ErrInUse)) || len(err.(*ErrInUse).Sources) != 1 {
		t.Errorf("err = %v, want ErrInUse passthrough", err)
	}
}

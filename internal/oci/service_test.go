package oci

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"tinysync/internal/job"
	"tinysync/internal/oci/image"
	"tinysync/internal/oci/registry"
)

// fixedNow 是服务测试的固定时钟基准。
var fixedNow = time.Unix(1757879400, 0).UTC()

// registryRef 解析镜像引用，失败即终止（选择逻辑测试辅助）。
func registryRef(t *testing.T, ref string) *image.ImageReference {
	t.Helper()
	parsed := image.ParseReference(ref)
	if parsed == nil {
		t.Fatalf("parse reference %q failed", ref)
	}
	return parsed
}

// fakeRepo 是内存版 Repository（服务层单元测试用，不触及 SQLite）。
type fakeRepo struct {
	rows    []Registry
	nextID  int
	listErr error
}

func (r *fakeRepo) Create(_ context.Context, row Registry) error {
	for _, existing := range r.rows {
		if strings.EqualFold(existing.Name, row.Name) {
			return ErrRegistryConflict
		}
	}
	r.nextID++
	row.ID = "reg_test"
	r.rows = append(r.rows, row)
	return nil
}

func (r *fakeRepo) Get(_ context.Context, id string) (Registry, error) {
	for _, row := range r.rows {
		if row.ID == id {
			return row, nil
		}
	}
	return Registry{}, ErrRegistryNotFound
}

func (r *fakeRepo) List(_ context.Context) ([]Registry, error) {
	if r.listErr != nil {
		return nil, r.listErr
	}
	return append([]Registry(nil), r.rows...), nil
}

func (r *fakeRepo) Update(_ context.Context, row Registry) error {
	for i := range r.rows {
		if r.rows[i].ID == row.ID {
			r.rows[i] = row
			return nil
		}
	}
	return ErrRegistryNotFound
}

func (r *fakeRepo) Delete(_ context.Context, id string) error {
	for i := range r.rows {
		if r.rows[i].ID == id {
			r.rows = append(r.rows[:i], r.rows[i+1:]...)
			return nil
		}
	}
	return ErrRegistryNotFound
}

func (r *fakeRepo) LatestImageStates(_ context.Context, jobIDs []string) (map[string]ImageState, error) {
	return nil, nil
}

// fakeJobs 是内存版 JobLister。
type fakeJobs struct {
	jobs []job.Job
}

func (f *fakeJobs) List(context.Context) ([]job.Job, error) { return f.jobs, nil }

// newTestService 构造带固定时钟与内存仓库的服务。
func newTestService(rows []Registry, jobs []job.Job) *Service {
	repo := &fakeRepo{rows: rows}
	svc := NewService(repo, nil)
	svc.Now = func() time.Time { return fixedNow }
	svc.Jobs = &fakeJobs{jobs: jobs}
	return svc
}

// Create 校验 mirror 行：上游唯一且至少一个非空加速器（oci-sync 语义）。
func TestServiceCreateMirrorValidation(t *testing.T) {
	svc := newTestService(nil, nil)

	if _, err := svc.Create(context.Background(), CreateInput{
		Name: "hub-mirror", Kind: KindMirror, Host: "docker.io",
	}); err == nil || !errors.Is(err, ErrRegistryInvalid) {
		t.Fatalf("empty mirrors: err = %v, want ErrRegistryInvalid", err)
	}
	if _, err := svc.Create(context.Background(), CreateInput{
		Name: "hub-mirror", Kind: KindMirror, Host: "docker.io",
		Mirrors: []string{"docker.1ms.run", " "},
	}); err == nil || !errors.Is(err, ErrRegistryInvalid) {
		t.Fatalf("blank mirror entry: err = %v, want ErrRegistryInvalid", err)
	}
	created, err := svc.Create(context.Background(), CreateInput{
		Name: "hub-mirror", Kind: KindMirror, Host: "docker.io",
		Mirrors: []string{"docker.1ms.run"},
	})
	if err != nil {
		t.Fatalf("create mirror: %v", err)
	}
	if created.Namespace() != "" || created.Hostname() != "docker.io" {
		t.Fatalf("mirror host split = %q / %q", created.Hostname(), created.Namespace())
	}
	// 同上游第二份 mirror 列表被拒绝。
	if _, err := svc.Create(context.Background(), CreateInput{
		Name: "hub-mirror-2", Kind: KindMirror, Host: "docker.io",
		Mirrors: []string{"mirror.example.com"},
	}); !errors.Is(err, ErrMirrorConflict) {
		t.Fatalf("duplicate upstream: err = %v, want ErrMirrorConflict", err)
	}
}

// primary 行不允许携带 mirrors；path_transform 非法值被拒绝。
func TestServiceCreatePrimaryValidation(t *testing.T) {
	svc := newTestService(nil, nil)

	if _, err := svc.Create(context.Background(), CreateInput{
		Name: "zot", Kind: KindPrimary, Host: "zot.example.com",
		Mirrors: []string{"docker.1ms.run"},
	}); err == nil || !errors.Is(err, ErrRegistryInvalid) {
		t.Fatalf("primary with mirrors: err = %v, want ErrRegistryInvalid", err)
	}
	if _, err := svc.Create(context.Background(), CreateInput{
		Name: "zot", Kind: KindPrimary, Host: "zot.example.com",
		PathTransform: &registry.PathTransformConfig{Mode: "both"},
	}); err == nil || !errors.Is(err, ErrRegistryInvalid) {
		t.Fatalf("missing separator: err = %v, want ErrRegistryInvalid", err)
	}
	if _, err := svc.Create(context.Background(), CreateInput{
		Name: "zot", Kind: KindPrimary, Host: "zot.example.com",
		PathTransform: &registry.PathTransformConfig{Separator: "---", Mode: "roundtrip"},
	}); err == nil || !errors.Is(err, ErrRegistryInvalid) {
		t.Fatalf("bad mode: err = %v, want ErrRegistryInvalid", err)
	}
	if _, err := svc.Create(context.Background(), CreateInput{
		Name: "", Kind: KindPrimary, Host: "zot.example.com",
	}); err == nil || !errors.Is(err, ErrRegistryInvalid) {
		t.Fatalf("empty name: err = %v, want ErrRegistryInvalid", err)
	}
	if _, err := svc.Create(context.Background(), CreateInput{
		Name: "zot", Kind: "proxy", Host: "zot.example.com",
	}); err == nil || !errors.Is(err, ErrRegistryInvalid) {
		t.Fatalf("bad kind: err = %v, want ErrRegistryInvalid", err)
	}
	// host 可携带 namespace，推导出 Registry / Namespace。
	created, err := svc.Create(context.Background(), CreateInput{
		Name: "cnb", Kind: KindPrimary, Host: "docker.cnb.cool/lzwang/mirrors",
		StripRegistries: []string{"docker.io"},
		PathTransform:   &registry.PathTransformConfig{Separator: "---", Mode: "encode"},
	})
	if err != nil {
		t.Fatalf("create primary with namespace: %v", err)
	}
	cfg := created.RegistryConfig()
	if cfg.Registry != "docker.cnb.cool" || cfg.Namespace != "lzwang/mirrors" {
		t.Fatalf("RegistryConfig = %q / %q", cfg.Registry, cfg.Namespace)
	}
}

// 仍被 oci 任务 dst_servers 引用的 Registry 拒绝删除。
func TestServiceDeleteReferencedByJob(t *testing.T) {
	svc := newTestService([]Registry{{
		ID: "reg_1", Name: "zot", Kind: KindPrimary, Host: "zot.example.com",
	}}, []job.Job{{
		ID: "job_1", Name: "sync", Kind: job.KindOCI, Enabled: true,
		Config: json.RawMessage(`{"src_images":["nginx:1.27"],"dst_servers":["zot"]}`),
	}})

	if err := svc.Delete(context.Background(), "reg_1"); !errors.Is(err, ErrRegistryInUse) {
		t.Fatalf("delete referenced registry: err = %v, want ErrRegistryInUse", err)
	}
	unrelated := newTestService([]Registry{{ID: "reg_1", Name: "zot", Kind: KindPrimary, Host: "zot.example.com"}}, nil)
	if err := unrelated.Delete(context.Background(), "reg_1"); err != nil {
		t.Fatalf("delete unreferenced registry: %v", err)
	}
}

// 快照 resolver：按名 / 按主机查 primary，未配置主机返回匿名零值配置，
// mirror 映射与 docker.io 跳过 primary 特判随 resolver 走（oci-sync
// FindRegistryByHost / GetMirror / shouldSkipPrimarySourceRegistry 语义）。
func TestSnapshotResolver(t *testing.T) {
	res := newResolver([]Registry{
		{ID: "1", Name: "zot", Kind: KindPrimary, Host: "zot.example.com", Username: "u", Password: "p"},
		{ID: "2", Name: "cnb", Kind: KindPrimary, Host: "docker.cnb.cool/lzwang/mirrors"},
		{ID: "3", Name: "hub", Kind: KindMirror, Host: "docker.io", Mirrors: []string{"docker.1ms.run"}},
	})

	if got := res.RegistryByName("zot"); got == nil || got.Username != "u" {
		t.Fatalf("RegistryByName(zot) = %+v", got)
	}
	if got := res.RegistryByName("hub"); got != nil {
		t.Fatalf("mirror row is not a sync target: %+v", got)
	}
	if got := res.RegistryByName("missing"); got != nil {
		t.Fatalf("unknown name = %+v, want nil", got)
	}

	if got := res.RegistryByHost("zot.example.com"); got == nil || got.Name != "zot" {
		t.Fatalf("RegistryByHost exact = %+v", got)
	}
	if got := res.RegistryByHost("evilzot.example.com"); got == nil || got.Username != "" || got.Registry != "evilzot.example.com" {
		t.Fatalf("unconfigured host should fall back to anonymous zero config: %+v", got)
	}

	if got := res.MirrorsFor("docker.io"); len(got) != 1 || got[0] != "docker.1ms.run" {
		t.Fatalf("MirrorsFor(docker.io) = %v", got)
	}
	if got := res.MirrorsFor("ghcr.io"); got != nil {
		t.Fatalf("MirrorsFor(ghcr.io) = %v, want nil", got)
	}

	if !res.SourcePolicy("docker.io").SkipPrimary {
		t.Fatal("docker.io should skip primary (mirror first)")
	}
	if res.SourcePolicy("docker.io").Mirrors[0] != "docker.1ms.run" {
		t.Fatal("SourcePolicy should carry mirrors")
	}
	if res.SourcePolicy("ghcr.io").SkipPrimary {
		t.Fatal("non docker.io should hit primary first")
	}
}

// OverrideSrcImages：子集校验 + 展开归一 + 其余字段保持。
func TestServiceOverrideSrcImages(t *testing.T) {
	svc := newTestService(nil, nil)
	base := json.RawMessage(`{"src_images":["nginx:1.24,1.25","alpine"],"dst_servers":["zot"],"platforms":[{"os":"linux","architecture":"arm64"}]}`)

	overridden, err := svc.OverrideSrcImages(context.Background(), base, []string{"nginx:1.25", "redis"})
	if err != nil {
		t.Fatalf("OverrideSrcImages: %v", err)
	}
	var cfg struct {
		SrcImages  []string `json:"src_images"`
		DstServers []string `json:"dst_servers"`
		Platforms  []struct {
			OS           string `json:"os"`
			Architecture string `json:"architecture"`
		} `json:"platforms"`
	}
	if err := json.Unmarshal(overridden, &cfg); err != nil {
		t.Fatalf("unmarshal overridden: %v", err)
	}
	if len(cfg.SrcImages) != 2 || cfg.SrcImages[0] != "library/nginx:1.25" || cfg.SrcImages[1] != "library/redis:latest" {
		t.Fatalf("overridden src_images = %v", cfg.SrcImages)
	}
	if len(cfg.DstServers) != 1 || cfg.DstServers[0] != "zot" {
		t.Fatalf("dst_servers must be preserved: %v", cfg.DstServers)
	}
	if len(cfg.Platforms) != 1 || cfg.Platforms[0].Architecture != "arm64" {
		t.Fatalf("platforms must be preserved: %v", cfg.Platforms)
	}

	if _, err := svc.OverrideSrcImages(context.Background(), base, []string{"nginx:1.25", "nginx:1.25"}); err == nil {
		t.Fatal("duplicate subset entries should be rejected")
	}
	if _, err := svc.OverrideSrcImages(context.Background(), base, []string{"ht tp://bad"}); err == nil {
		t.Fatal("invalid subset entries should be rejected")
	}
	if _, err := svc.OverrideSrcImages(context.Background(), base, nil); err == nil {
		t.Fatal("empty subset should be rejected")
	}
}

// configuredRegistry / repositoryRegistry 选择：最长 namespace 前缀 +
// 歧义检测 + 公共回退（oci-sync control remote.go 语义）。
func TestRegistrySelection(t *testing.T) {
	host := "registry.example.com"
	rows := []Registry{
		{ID: "1", Name: "a", Kind: KindPrimary, Host: host + "/team-a", Username: "team-a"},
		{ID: "2", Name: "b", Kind: KindPrimary, Host: host + "/team-b", Username: "team-b"},
		{ID: "3", Name: "root", Kind: KindPrimary, Host: host},
	}

	// 按主机多配置 → 歧义；给出配置名消歧。
	if _, err := configuredRegistry(rows, host); err == nil || err.(*Error).Code != "registry_ambiguous" {
		t.Fatalf("configuredRegistry by host: %v, want ambiguous", err)
	}
	if reg, err := configuredRegistry(rows, "b"); err != nil || reg.Name != "b" {
		t.Fatalf("configuredRegistry by name: %v / %+v", err, reg)
	}

	// repositoryRegistry：team-b 仓库选中 team-b 配置（最长前缀）。
	refTeamB := registryRef(t, host+"/team-b/app:latest")
	reg, err := repositoryRegistry(rows, refTeamB, "")
	if err != nil || reg.Username != "team-b" {
		t.Fatalf("longest prefix selection: %v / %+v", err, reg)
	}
	// 根路径仓库选中无 namespace 的 root 配置。
	reg, err = repositoryRegistry(rows, registryRef(t, host+"/app:latest"), "")
	if err != nil || reg.Name != "root" {
		t.Fatalf("root selection: %v / %+v", err, reg)
	}
	// selected 指向的配置 namespace 不覆盖仓库 → forbidden。
	if _, err := repositoryRegistry(rows, refTeamB, "a"); err == nil || err.(*Error).Code != "registry_forbidden" {
		t.Fatalf("namespace boundary: %v, want registry_forbidden", err)
	}
	// selected 不存在 → not configured。
	if _, err := repositoryRegistry(rows, refTeamB, "unknown"); err == nil || err.(*Error).Code != "registry_not_configured" {
		t.Fatalf("unknown selection: %v, want registry_not_configured", err)
	}
	// 同主机同 namespace 双配置 → 歧义。
	dup := append(append([]Registry(nil), rows...), Registry{ID: "4", Name: "b-duplicate", Kind: KindPrimary, Host: host + "/team-b"})
	if _, err := repositoryRegistry(dup, refTeamB, ""); err == nil || err.(*Error).Code != "registry_ambiguous" {
		t.Fatalf("ambiguous selection: %v, want registry_ambiguous", err)
	}
	// 未配置主机 → 匿名回退（公共访问策略）。
	reg, err = repositoryRegistry(rows, registryRef(t, "ghcr.io/xxx/yyy:latest"), "")
	if err != nil || reg.Name != "ghcr.io" || reg.Username != "" {
		t.Fatalf("anonymous fallback: %v / %+v", err, reg)
	}
}

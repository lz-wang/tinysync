package oci

import (
	"strings"

	"tinysync/internal/oci/registry"
)

// snapshotResolver 是 registry.Resolver 的 registries 表快照实现：
// 每轮运行 / 每次查询前由 Service 从存储读出全部行构建，运行期间配置
// 变更不影响进行中的快照（与统一 Runner 的配置快照路径同构）。
//
// docker.io 特判（跳过 primary、mirror 优先）在这里随 resolver 走，
// 引擎（registry.Client）内不出现任何主机名特判；匿名 mirror 与
// 目标永不走 mirror 由引擎的镜像读取 / 写入路径保证。
type snapshotResolver struct {
	primariesByName map[string]registry.RegistryConfig
	primariesByHost map[string][]registry.RegistryConfig
	mirrors         map[string][]string
}

// newResolver 把 registries 行集合构建为快照 resolver（oci-sync
// parseRegistryFields 语义：Host 拆分出 namespace 后参与匹配）。
func newResolver(rows []Registry) *snapshotResolver {
	res := &snapshotResolver{
		primariesByName: make(map[string]registry.RegistryConfig),
		primariesByHost: make(map[string][]registry.RegistryConfig),
		mirrors:         make(map[string][]string),
	}
	for _, row := range rows {
		switch row.Kind {
		case KindPrimary:
			cfg := row.RegistryConfig()
			res.primariesByName[cfg.Name] = cfg
			res.primariesByHost[cfg.Registry] = append(res.primariesByHost[cfg.Registry], cfg)
		case KindMirror:
			res.mirrors[row.Host] = row.Mirrors
		}
	}
	return res
}

// RegistryByName 实现 registry.Resolver：按配置名查 primary Registry
// （mirror 行不是真实 Registry，不能作为同步目标）；不存在返回 nil。
func (r *snapshotResolver) RegistryByName(name string) *registry.RegistryConfig {
	cfg, ok := r.primariesByName[name]
	if !ok {
		return nil
	}
	return &cfg
}

// RegistryByHost 实现 registry.Resolver：按纯主机名精确匹配 primary
// Registry（"ghcr.io" 不会匹配 "evilghcr.io"，凭据不落到错误 Registry，
// 多行同主机时取第一行——与 oci-sync FindRegistryByHost 语义一致）；
// 未配置的主机返回匿名零值配置（匿名访问、验证 TLS——零值即安全默认）。
func (r *snapshotResolver) RegistryByHost(host string) *registry.RegistryConfig {
	if cfgs := r.primariesByHost[host]; len(cfgs) > 0 {
		cfg := cfgs[0]
		return &cfg
	}
	return &registry.RegistryConfig{
		Registry: host,
		Name:     host,
	}
}

// MirrorsFor 实现 registry.Resolver：返回上游主机配置的加速器列表；
// 未配置返回 nil。
func (r *snapshotResolver) MirrorsFor(upstreamHost string) []string {
	return r.mirrors[upstreamHost]
}

// SourcePolicy 实现 registry.Resolver：docker.io 跳过 primary（mirror
// 优先、全部失败后回退 primary）；其余主机 primary 优先。
func (r *snapshotResolver) SourcePolicy(upstreamHost string) registry.SourcePolicy {
	return registry.SourcePolicy{
		SkipPrimary: shouldSkipPrimarySourceRegistry(upstreamHost),
		Mirrors:     r.mirrors[upstreamHost],
	}
}

// shouldSkipPrimarySourceRegistry 对 docker.io 直接跳过 primary，
// 走配置的 mirror（mirror 是 source pull 加速器，配置了就使用）。
// docker.io 特判随 resolver 走（oci-sync 同名逻辑移植），引擎内不出现
// 主机名特判。
func shouldSkipPrimarySourceRegistry(repoRef string) bool {
	return repoRef == "docker.io"
}

// anonymousPrimary 是未配置主机的匿名零值配置（与 RegistryByHost 的
// 缺省返回一致，供 /images 系列端点的公共访问路径使用）。
func anonymousPrimary(host string) registry.RegistryConfig {
	return registry.RegistryConfig{Registry: host, Name: host}
}

// hostOfRepoName 返回完整仓库路径的 registry 主机部分（首段）。
func hostOfRepoName(repoName string) string {
	return strings.SplitN(repoName, "/", 2)[0]
}

// 编译期接口断言。
var _ registry.Resolver = (*snapshotResolver)(nil)

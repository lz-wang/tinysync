package registry

// nopLogger 是测试用的静默日志实现（zap.NewNop 的移植形态）。
type nopLogger struct{}

func (nopLogger) Debugf(format string, args ...any) {}

func (nopLogger) Infof(format string, args ...any) {}

func (nopLogger) Warnf(format string, args ...any) {}

func (nopLogger) Errorf(format string, args ...any) {}

// staticResolver 是测试用的静态 resolver：只携带 mirror 映射，其余
// 查询返回匿名零值配置（oci-sync 测试中 *config.Config 的等价物）。
type staticResolver struct {
	mirrors map[string][]string
}

func newStaticResolver(mirrors map[string][]string) *staticResolver {
	return &staticResolver{mirrors: mirrors}
}

func (s *staticResolver) RegistryByName(name string) *RegistryConfig {
	return nil
}

func (s *staticResolver) RegistryByHost(host string) *RegistryConfig {
	return &RegistryConfig{Registry: host, Name: host}
}

func (s *staticResolver) MirrorsFor(upstreamHost string) []string {
	return s.mirrors[upstreamHost]
}

func (s *staticResolver) SourcePolicy(upstreamHost string) SourcePolicy {
	return SourcePolicy{
		SkipPrimary: upstreamHost == "docker.io",
		Mirrors:     s.mirrors[upstreamHost],
	}
}

// 编译期接口断言。
var _ Resolver = (*staticResolver)(nil)

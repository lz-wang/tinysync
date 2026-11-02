package registry

// 本文件承载引擎的窄配置边界（自 oci-sync 移植时的一次性手术产物）：
// 引擎不再直穿 oci-sync 的全局 *config.Config，全部 Registry / mirror
// 决策经 Resolver 进入；docker.io 特判（跳过 primary）同样随 resolver
// 走，引擎内不出现任何主机名特判。值类型（RegistryConfig /
// PathTransformConfig / PlatformConfig）与引擎同包定义，供 task 执行器
// 与 internal/oci 的 resolver 实现共用。

// RegistryConfig 是一个 Registry 的运行时配置（自 oci-sync config 包
// 移植的值形态）。Host 字段可携带 namespace（如
// "docker.cnb.cool/lzwang/mirrors"），Namespace 由首个 "/" 前缀推导。
type RegistryConfig struct {
	Name               string               `json:"name"`
	Registry           string               `json:"registry"`
	Username           string               `json:"username"`
	Password           string               `json:"password"`
	InsecureSkipVerify bool                 `json:"insecure_skip_verify"` // 仅在显式配置为 true 时跳过 TLS 证书验证，默认验证
	StripRegistries    []string             `json:"strip_registries"`     // 作为目标时，移除匹配的源 registry 前缀（含 namespace）
	Namespace          string               `json:"-"`                    // 从 registry 字段自动解析，不从 JSON 读取
	PathTransform      *PathTransformConfig `json:"path_transform"`       // 路径转换配置
	PlainHTTP          bool                 `json:"plain_http"`           // 是否使用纯 HTTP（无 TLS）
}

// PathTransformConfig 镜像路径转换配置。
type PathTransformConfig struct {
	Separator string `json:"separator"` // 分隔符，如 "---"
	Mode      string `json:"mode"`      // encode, decode, both
}

// PlatformConfig 表示镜像复制的平台过滤配置（oci-sync 同名类型移植）。
type PlatformConfig struct {
	OS           string `json:"os"`           // 操作系统，默认 linux
	Architecture string `json:"architecture"` // 架构，如 amd64, arm64
	Variant      string `json:"variant"`      // 变体，如 v8 (可选)
}

// SourcePolicy 是引擎访问某上游主机源端的完整决策：是否跳过 primary、
// 可用的 mirror 加速器列表。docker.io 特判（跳过 primary、mirror 优先）
// 由 Resolver 实现给出，引擎只按策略执行回退次序。
type SourcePolicy struct {
	// SkipPrimary 为 true 时先尝试 mirrors、全部失败后回退 primary
	// （mirror 是加速器而不是硬依赖）。
	SkipPrimary bool
	// Mirrors 是按序尝试的匿名 mirror 地址列表。
	Mirrors []string
}

// Resolver 是引擎访问 Registry 配置的窄接口（tinysync registries 表
// 驱动其实现）。引擎的镜像读取（docker.io 跳过 primary、mirror 优先、
// 匿名 mirror）与任务执行器的目标查找全部经它完成；目标端永不走
// mirror（getDestinationRef 只使用配置的 destination）。
type Resolver interface {
	// RegistryByName 按配置名查 primary Registry；不存在返回 nil。
	RegistryByName(name string) *RegistryConfig
	// RegistryByHost 按纯主机名精确匹配 primary Registry
	//（"ghcr.io" 不会匹配 "evilghcr.io"，凭据不落到错误 Registry）；
	// 未配置的主机返回匿名零值配置（匿名访问、验证 TLS——零值即安全默认）。
	RegistryByHost(host string) *RegistryConfig
	// MirrorsFor 返回某上游主机配置的 mirror 加速器列表；未配置返回 nil。
	MirrorsFor(upstreamHost string) []string
	// SourcePolicy 返回某上游主机的源端访问决策（docker.io → 跳过
	// primary、mirror 优先；其余 → primary 优先）。
	SourcePolicy(upstreamHost string) SourcePolicy
}

// Logger 是引擎的最小日志接口（zap → tinysync logging 的手术产物）：
// 引擎内不再注入 *zap.Logger，结构化字段改为 format 参数，由调用方
// 注入 logging 适配。
type Logger interface {
	Debugf(format string, args ...any)
	Infof(format string, args ...any)
	Warnf(format string, args ...any)
	Errorf(format string, args ...any)
}

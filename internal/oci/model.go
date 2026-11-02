// Package oci 承载 OCI 镜像同步领域（自 oci-sync 移植）：镜像复制引擎
// （internal/oci/registry）、扇出执行器（internal/oci/task）、Registry 配置
// 存储（registries 表：primary 目标仓库 + mirror 加速器）、镜像管理查询
// 与统一任务模型的 kind=oci 执行器适配。
//
// 领域边界：本包不感知 jobs 表的公共字段（schedule / enabled 等），
// 领域差异全部封装在 config_json（task.TaskConfig）的校验与执行内。
package oci

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"tinysync/internal/oci/registry"
)

// RegistryKind 是 registries 行的类型：primary 为真实 Registry 配置
// （同步目标与凭据来源），mirror 是上游主机的加速器列表。
type RegistryKind string

// registries.kind 的枚举值。
const (
	KindPrimary RegistryKind = "primary"
	KindMirror  RegistryKind = "mirror"
)

// Valid 判断类型是否为受支持的枚举值。
func (k RegistryKind) Valid() bool {
	return k == KindPrimary || k == KindMirror
}

// 领域哨兵错误：API 层映射为 REST 状态码。
var (
	// ErrRegistryNotFound 表示目标 Registry 配置不存在。
	ErrRegistryNotFound = errors.New("registry not found")
	// ErrRegistryConflict 表示 name 与现有配置冲突（COLLATE NOCASE）。
	ErrRegistryConflict = errors.New("registry name already exists")
	// ErrMirrorConflict 表示 mirror 行的上游主机与现有配置冲突
	//（一条上游主机只允许一份加速器列表）。
	ErrMirrorConflict = errors.New("mirror upstream already exists")
	// ErrRegistryInvalid 表示输入校验失败。
	ErrRegistryInvalid = errors.New("invalid registry")
	// ErrRegistryInUse 表示 Registry 配置仍被 oci 任务的 dst_servers 引用，
	// 删除被拒绝。
	ErrRegistryInUse = errors.New("registry is referenced by oci jobs")
)

// Registry 是 registries 表的一行。primary 行的 Host 可携带 namespace
// （"docker.cnb.cool/lzwang/mirrors"，oci-sync parseRegistryNamespace 语义）；
// mirror 行的 Host 是上游主机（如 docker.io），Mirrors 是加速器列表。
type Registry struct {
	ID       string
	Name     string
	Kind     RegistryKind
	Host     string
	Username string
	Password string
	// InsecureSkipVerify 仅在显式配置为 true 时跳过 TLS 证书验证。
	InsecureSkipVerify bool
	PlainHTTP          bool
	// StripRegistries 是作为目标时移除匹配源 registry 前缀的列表
	// （strip_registries_json）。
	StripRegistries []string
	// PathTransform 是镜像路径转换配置（path_transform_json）；
	// nil 表示未配置。
	PathTransform *registry.PathTransformConfig
	// Mirrors 是 mirror 行的加速器地址列表（mirrors_json）；
	// primary 行恒为空。
	Mirrors   []string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Hostname 返回 Host 的纯主机部分（首个 "/" 之前）。
func (r Registry) Hostname() string {
	host, _ := r.splitHost()
	return host
}

// Namespace 返回 Host 携带的 namespace（首个 "/" 之后；无则空）。
func (r Registry) Namespace() string {
	_, ns := r.splitHost()
	return ns
}

// splitHost 按 oci-sync parseRegistryNamespace 语义拆分 Host：
// "docker.cnb.cool/lzwang/mirrors" -> ("docker.cnb.cool", "lzwang/mirrors")。
func (r Registry) splitHost() (registryHost, namespace string) {
	parts := strings.SplitN(r.Host, "/", 2)
	if len(parts) == 1 {
		return parts[0], ""
	}
	return parts[0], parts[1]
}

// RegistryConfig 把行转换为引擎的运行时配置形态（host 拆分出 namespace）。
func (r Registry) RegistryConfig() registry.RegistryConfig {
	return registry.RegistryConfig{
		Name:               r.Name,
		Registry:           r.Hostname(),
		Username:           r.Username,
		Password:           r.Password,
		InsecureSkipVerify: r.InsecureSkipVerify,
		StripRegistries:    r.StripRegistries,
		Namespace:          r.Namespace(),
		PathTransform:      r.PathTransform,
		PlainHTTP:          r.PlainHTTP,
	}
}

// idPrefix 是 Registry ID 的固定前缀。
const idPrefix = "reg_"

// NewID 生成 reg_<128-bit random hex> 形式的唯一 ID。
func NewID() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate registry id: %w", err)
	}
	return idPrefix + hex.EncodeToString(buf), nil
}

// ImageState 是一个 (oci 任务, 源镜像) 的最近执行状态（GET /images 的行）。
type ImageState struct {
	JobID    string
	Source   string // 源镜像引用（repo:tag）
	RunID    string
	Status   string // success | skipped | failed
	SyncedAt time.Time
}

// CreateInput 是创建 Registry 的输入。Kind 决定行语义：primary 的
// Host 是 registry（可含 namespace）；mirror 的 Host 是上游主机、
// Mirrors 必填。
type CreateInput struct {
	Name               string
	Kind               RegistryKind
	Host               string
	Username           string
	Password           string
	InsecureSkipVerify bool
	PlainHTTP          bool
	StripRegistries    []string
	PathTransform      *registry.PathTransformConfig
	Mirrors            []string
}

// UpdateInput 是更新 Registry 的输入：nil 字段保留现有值。Password 为
// 三态（nil 保留、空串清除、非空替换）。PathTransformSet 区分「未提供
// path_transform」与「显式置 null（清除）」。Kind 不可变更。
type UpdateInput struct {
	Name               *string
	Host               *string
	Username           *string
	Password           *string
	InsecureSkipVerify *bool
	PlainHTTP          *bool
	StripRegistries    *[]string
	PathTransformSet   bool
	PathTransform      *registry.PathTransformConfig
	Mirrors            *[]string
}

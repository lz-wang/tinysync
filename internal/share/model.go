// Package share 是 HTTP 共享策略的应用领域：把同步后的本地文件或
// 目录（含 Job 本地根）显式暴露为受控公开 URL。Share 持有创建时
// canonicalize 的本地绝对路径，与 Job 生命周期解耦；slug 全局唯一，
// 自定义共享名称即 slug，未命名时系统生成随机 slug（name 为 NULL，
// 展示回落 basename）；禁用 / 过期 / 目标缺失在公开侧一律按不存在
// 处理（同形 404，见 CONTEXT.md 与 ADR-0002）。
package share

import (
	"time"
)

// idPrefix 是 Share ID 的固定前缀，便于在日志与 API 中一眼识别。
const idPrefix = "shr_"

// Share 是一条显式共享策略。
type Share struct {
	ID string
	// LocalPath 是创建时 canonicalize（abs + EvalSymlinks）的本地目标
	// 绝对路径，创建后不可变。指向的目标被删除后公开 URL 自然 404；
	// Job 修改 LocalRoot 不会隐式 retarget。
	LocalPath string
	// Slug 是 /shared 之下的唯一 URL 标识段：自定义名称或随机生成。
	Slug string
	// Name 为 nil 表示未命名（随机 slug）；非 nil 时等于自定义名称
	// （此时 Slug 与之相等），仅用于管理侧辨识。
	Name *string
	// IsDir 记录创建时目标是目录（含 Job 本地根）还是普通文件。
	// 文件共享的公开浏览根是恰含自身一条目的虚拟目录。
	IsDir   bool
	Enabled bool
	// ExpiresAt 为 nil 表示永不过期。
	ExpiresAt *time.Time
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Expired 判断共享在给定时刻是否已过期。
func (s Share) Expired(now time.Time) bool {
	return s.ExpiresAt != nil && !now.Before(*s.ExpiresAt)
}

// CreateInput 是创建输入：目标以 Job + LocalRoot 内逻辑路径表达，
// API 不接受任意 local_path；只有 Service 能把它转换为 canonical
// 绝对路径。Name 空串表示未命名（生成随机 slug）。
type CreateInput struct {
	JobID string
	// Path 是 LocalRoot 内逻辑路径（/ 分隔、绝对；"/" 即整个本地根）。
	Path string
	// Name 是可选的自定义共享名称（即 URL slug）。
	Name      string
	Enabled   bool
	ExpiresAt *time.Time
}

// UpdateInput 是部分更新输入：nil 字段保留现有值。local_path 与
// 创建时的目标类型不可变——要换目标就新建共享，避免隐式 retarget。
// Name 指向空串表示清除自定义名称（slug 保持不变）；指向非空名称
// 时同时改写 slug（旧公开链接立即失效）。ExpiresAt 与 ClearExpires
// 互斥使用：前者设置新过期时刻，后者清除过期（永不过期）。
type UpdateInput struct {
	Name         *string
	Enabled      *bool
	ExpiresAt    *time.Time
	ClearExpires bool
}

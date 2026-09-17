// Package publish 是 HTTP 发布策略的应用领域：把同步后的本地文件
// 显式暴露为受控公开 URL。Policy 持有创建时 canonicalize 的本地
// 绝对路径，与 Job 生命周期解耦；公开路径全局唯一，禁用 / 过期 /
// 文件缺失在 serving 侧一律按不存在处理。
package publish

import (
	"time"
)

// idPrefix 是 PublishedFile ID 的固定前缀，便于在日志与 API 中一眼
// 识别。
const idPrefix = "pub_"

// PublishedFile 是一条显式发布策略。
type PublishedFile struct {
	ID string
	// LocalPath 是创建 Policy 时 canonicalize（abs + EvalSymlinks）
	// 的本地文件绝对路径，创建后不可变。指向的文件被删除后公开
	// URL 自然 404；Job 修改 LocalRoot 不会隐式 retarget。
	LocalPath string
	// PublicPath 是 /published 之下的唯一公开路径（/ 开头、clean、
	// 非 root）。
	PublicPath string
	Enabled    bool
	// ExpiresAt 为 nil 表示永不过期。
	ExpiresAt *time.Time
	CreatedAt time.Time
	UpdatedAt time.Time
}

// CreateInput 是创建输入：目标文件以 Job + LocalRoot 内逻辑路径
// 表达，API 不接受任意 local_path；只有 Service 能把它转换为
// canonical 绝对路径。
type CreateInput struct {
	JobID string
	// Path 是 LocalRoot 内逻辑路径（/ 分隔、绝对）。
	Path       string
	PublicPath string
	Enabled    bool
	ExpiresAt  *time.Time
}

// UpdateInput 是部分更新输入：nil 字段保留现有值。local_path 与
// 创建时的 Job 关系不可变——要换文件就新建 Policy，避免隐式
// retarget。ExpiresAt 与 ClearExpires 互斥使用：前者设置新过期
// 时刻，后者清除过期（永不过期）。
type UpdateInput struct {
	PublicPath   *string
	Enabled      *bool
	ExpiresAt    *time.Time
	ClearExpires bool
}

// Expired 判断策略在给定时刻是否已过期。
func (p PublishedFile) Expired(now time.Time) bool {
	return p.ExpiresAt != nil && !now.Before(*p.ExpiresAt)
}

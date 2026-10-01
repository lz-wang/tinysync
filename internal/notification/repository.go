package notification

import (
	"context"
	"time"
)

// Repository 是通知配置的持久化接口，由 storage 后端（SQLite）实现。
// 配置是 singleton 形态：migration 已预置 id=1 的默认行，这里只有
// 整行 Load / Save，没有 Insert / Delete。secret（token / user key /
// password）随整行存取，读取路径只在发送与保存合并时发生——与凭据域
// 不同，这里没有独立的「普通读取不取 secret」路径，API 层负责不回显。
type Repository interface {
	// Load 返回 singleton 配置行（secret 在内）。
	Load(ctx context.Context) (Settings, error)

	// Save 整行覆盖配置并写入 updated_at。
	Save(ctx context.Context, settings Settings, updatedAt time.Time) error
}

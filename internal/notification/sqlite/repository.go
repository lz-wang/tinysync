// Package sqlite 实现 notification.Repository 的 SQLite 持久化。
// 配置是 singleton 行（id=1，migration 预置），只 Load / Save 整行；
// email_to 以逗号分隔序列化收件地址列表。
package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"tinysync/internal/notification"
)

// Repository 是 notification.Repository 的 SQLite 实现。
type Repository struct {
	db *sql.DB
}

// New 构造 Repository；db 需已完成 schema migration
// （含 notification_settings 表）。
func New(db *sql.DB) *Repository {
	return &Repository{db: db}
}

// Load 实现 notification.Repository，返回 singleton 配置行。
// 迁移预置行的 security / port 与领域默认值一致；updated_at 为 0 的
// 预置行返回零时刻，表示从未配置。
func (r *Repository) Load(ctx context.Context) (notification.Settings, error) {
	var (
		settings      notification.Settings
		pushEnabled   int
		emailEnabled  int
		emailSecurity string
		emailToJoined string
		updatedAt     int64
	)
	err := r.db.QueryRowContext(ctx, `SELECT
		pushover_enabled, pushover_token, pushover_user_key,
		email_enabled, email_host, email_port, email_security,
		email_username, email_password, email_from, email_to, updated_at
		FROM notification_settings WHERE id = 1`).
		Scan(&pushEnabled, &settings.Pushover.Token, &settings.Pushover.UserKey,
			&emailEnabled, &settings.Email.Host, &settings.Email.Port, &emailSecurity,
			&settings.Email.Username, &settings.Email.Password, &settings.Email.From,
			&emailToJoined, &updatedAt)
	if err != nil {
		return notification.Settings{}, fmt.Errorf("load notification settings: %w", err)
	}
	settings.Pushover.Enabled = pushEnabled == 1
	settings.Email.Enabled = emailEnabled == 1
	settings.Email.Security = notification.Security(emailSecurity)
	settings.Email.To = splitRecipients(emailToJoined)
	// migration 预置行的 updated_at 为 0（从未配置）：显式映射为 Go 零
	// 时刻，与「未设置」语义一致。
	if updatedAt == 0 {
		settings.UpdatedAt = time.Time{}
	} else {
		settings.UpdatedAt = time.UnixMilli(updatedAt).UTC()
	}
	return settings, nil
}

// Save 实现 notification.Repository，整行覆盖 singleton 配置。
func (r *Repository) Save(ctx context.Context, settings notification.Settings, updatedAt time.Time) error {
	res, err := r.db.ExecContext(ctx, `UPDATE notification_settings SET
		pushover_enabled = ?, pushover_token = ?, pushover_user_key = ?,
		email_enabled = ?, email_host = ?, email_port = ?, email_security = ?,
		email_username = ?, email_password = ?, email_from = ?, email_to = ?,
		updated_at = ?
		WHERE id = 1`,
		boolToInt(settings.Pushover.Enabled), settings.Pushover.Token, settings.Pushover.UserKey,
		boolToInt(settings.Email.Enabled), settings.Email.Host, settings.Email.Port, string(settings.Email.Security),
		settings.Email.Username, settings.Email.Password, settings.Email.From,
		joinRecipients(settings.Email.To), updatedAt.UnixMilli(),
	)
	if err != nil {
		return fmt.Errorf("save notification settings: %w", err)
	}
	if n, err := res.RowsAffected(); err != nil {
		return fmt.Errorf("rows affected of notification settings: %w", err)
	} else if n == 0 {
		return fmt.Errorf("save notification settings: singleton row missing")
	}
	return nil
}

// recipientsSeparator 是 email_to 的分隔符。逗号不会出现在合法的
// 收件地址内（RFC 5322 未引用 local-part 不允许逗号），分隔无损。
const recipientsSeparator = ","

// joinRecipients 序列化收件地址列表。
func joinRecipients(to []string) string {
	return strings.Join(to, recipientsSeparator)
}

// splitRecipients 反序列化收件地址列表；空串返回 nil。
func splitRecipients(joined string) []string {
	if joined == "" {
		return nil
	}
	parts := strings.Split(joined, recipientsSeparator)
	recipients := make([]string, 0, len(parts))
	for _, part := range parts {
		recipients = append(recipients, strings.TrimSpace(part))
	}
	return recipients
}

// boolToInt 把布尔值映射为 schema 的 CHECK (IN (0, 1))。
func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// 编译期接口断言。
var _ notification.Repository = (*Repository)(nil)

package notification

import "errors"

// 领域哨兵错误：各实现（Repository、Service、Sender）以 errors.Is 判定。
var (
	// ErrInvalid 表示输入校验失败。具体字段与原因见 InvalidError。
	ErrInvalid = errors.New("invalid notification settings")
	// ErrNotConfigured 表示渠道已启用但缺少发送所需的完整配置
	//（或未启用）。测试发送端点据此返回 400。
	ErrNotConfigured = errors.New("notification channel is not configured")
)

// InvalidError 是携带字段定位的校验失败，API 以 400 回显 field 与
// reason，便于 WebUI 精确标注表单项。
type InvalidError struct {
	Field  string
	Reason string
}

// Error 实现 error。
func (e *InvalidError) Error() string {
	return "invalid notification settings: " + e.Field + ": " + e.Reason
}

// Unwrap 使 errors.Is(err, ErrInvalid) 成立。
func (e *InvalidError) Unwrap() error {
	return ErrInvalid
}

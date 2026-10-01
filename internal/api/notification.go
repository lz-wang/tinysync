package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"tinysync/internal/auth"
	"tinysync/internal/notification"
)

// registerNotificationRoutes 注册通知配置端点（ADR 0006）。通知配置是
// 管理面资源：读取与写入都要求 admin——渠道参数与 secret 状态属于
// 敏感管理信息，read / run scope token 一律不可见。svc 为 nil 时跳过
// 注册（依赖缺失时由未知路径 404 兜底）。
func registerNotificationRoutes(group *gin.RouterGroup, svc *notification.Service, dispatcher *notification.Dispatcher) {
	if svc == nil {
		return
	}
	h := &notificationHandlers{svc: svc, dispatcher: dispatcher}
	group.GET("/notifications/settings", requireScope(auth.ScopeAdmin), h.get)
	group.PATCH("/notifications/settings", requireScope(auth.ScopeAdmin), h.update)
	group.POST("/notifications/test/pushover", requireScope(auth.ScopeAdmin), h.testPushover)
	group.POST("/notifications/test/email", requireScope(auth.ScopeAdmin), h.testEmail)
}

// notificationHandlers 是通知端点的 handler 集合。
type notificationHandlers struct {
	svc        *notification.Service
	dispatcher *notification.Dispatcher
}

// pushoverSettingsDTO 是 Pushover 渠道的回显：secret 只以 configured
// 布尔出现，任何响应都不回显 token / user key 明文。
type pushoverSettingsDTO struct {
	Enabled           bool `json:"enabled"`
	TokenConfigured   bool `json:"token_configured"`
	UserKeyConfigured bool `json:"user_key_configured"`
}

// emailSettingsDTO 是邮件渠道的回显；password 只以 configured 布尔
// 出现。
type emailSettingsDTO struct {
	Enabled            bool     `json:"enabled"`
	Host               string   `json:"host"`
	Port               int      `json:"port"`
	Security           string   `json:"security"`
	Username           string   `json:"username"`
	PasswordConfigured bool     `json:"password_configured"`
	From               string   `json:"from"`
	To                 []string `json:"to"`
}

// notificationSettingsDTO 是 GET / PATCH 的响应体。
type notificationSettingsDTO struct {
	Pushover  pushoverSettingsDTO `json:"pushover"`
	Email     emailSettingsDTO    `json:"email"`
	UpdatedAt string              `json:"updated_at,omitempty"`
}

// toNotificationSettingsDTO 转换领域配置；secret 全部折叠为 configured
// 布尔。to 为 nil 时输出空数组，保持响应结构稳定。
func toNotificationSettingsDTO(s notification.Settings) notificationSettingsDTO {
	dto := notificationSettingsDTO{
		Pushover: pushoverSettingsDTO{
			Enabled:           s.Pushover.Enabled,
			TokenConfigured:   s.Pushover.Token != "",
			UserKeyConfigured: s.Pushover.UserKey != "",
		},
		Email: emailSettingsDTO{
			Enabled:            s.Email.Enabled,
			Host:               s.Email.Host,
			Port:               s.Email.Port,
			Security:           string(s.Email.Security),
			Username:           s.Email.Username,
			PasswordConfigured: s.Email.Password != "",
			From:               s.Email.From,
			To:                 s.Email.To,
		},
	}
	if dto.Email.To == nil {
		dto.Email.To = []string{}
	}
	if !s.UpdatedAt.IsZero() {
		dto.UpdatedAt = s.UpdatedAt.Format(time.RFC3339)
	}
	return dto
}

// pushoverPatchPayload 是 Pushover 渠道的 PATCH 载荷：secret 三态
// （nil 保留 / 字符串替换 / clear 删除）。
type pushoverPatchPayload struct {
	Enabled      *bool   `json:"enabled"`
	Token        *string `json:"token"`
	ClearToken   bool    `json:"clear_token"`
	UserKey      *string `json:"user_key"`
	ClearUserKey bool    `json:"clear_user_key"`
}

// emailPatchPayload 是邮件渠道的 PATCH 载荷；非 secret 字段 nil 保留、
// 非 nil 替换，To nil 保留、非 nil 整体替换，password 三态。
type emailPatchPayload struct {
	Enabled       *bool    `json:"enabled"`
	Host          *string  `json:"host"`
	Port          *int     `json:"port"`
	Security      *string  `json:"security"`
	Username      *string  `json:"username"`
	From          *string  `json:"from"`
	To            []string `json:"to"`
	Password      *string  `json:"password"`
	ClearPassword bool     `json:"clear_password"`
}

// notificationPatchPayload 是 PATCH /notifications/settings 请求体。
type notificationPatchPayload struct {
	Pushover *pushoverPatchPayload `json:"pushover"`
	Email    *emailPatchPayload    `json:"email"`
}

// get GET /api/v1/notifications/settings。
func (h *notificationHandlers) get(c *gin.Context) {
	settings, err := h.svc.Settings(c.Request.Context())
	if err != nil {
		handleNotificationError(c, err)
		return
	}
	c.JSON(http.StatusOK, toNotificationSettingsDTO(settings))
}

// update PATCH /api/v1/notifications/settings。字段不存在保留、提供
// 即替换、clear_* 删除；校验失败 400 并回显 field / reason。
func (h *notificationHandlers) update(c *gin.Context) {
	var req notificationPatchPayload
	if !strictBind(c, &req) {
		return
	}
	input := notification.PatchInput{}
	if req.Pushover != nil {
		input.Pushover = &notification.PushoverPatch{
			Enabled:      req.Pushover.Enabled,
			Token:        req.Pushover.Token,
			ClearToken:   req.Pushover.ClearToken,
			UserKey:      req.Pushover.UserKey,
			ClearUserKey: req.Pushover.ClearUserKey,
		}
	}
	if req.Email != nil {
		email := &notification.EmailPatch{
			Enabled:       req.Email.Enabled,
			Host:          req.Email.Host,
			Port:          req.Email.Port,
			Username:      req.Email.Username,
			From:          req.Email.From,
			To:            req.Email.To,
			Password:      req.Email.Password,
			ClearPassword: req.Email.ClearPassword,
		}
		if req.Email.Security != nil {
			security := notification.Security(*req.Email.Security)
			email.Security = &security
		}
		input.Email = email
	}
	updated, err := h.svc.Update(c.Request.Context(), input)
	if err != nil {
		handleNotificationError(c, err)
		return
	}
	c.JSON(http.StatusOK, toNotificationSettingsDTO(updated))
}

// testPushover POST /api/v1/notifications/test/pushover：用已保存的
// 配置发送测试通知（浏览器不重传 secret）。
func (h *notificationHandlers) testPushover(c *gin.Context) {
	h.sendTest(c, "pushover")
}

// testEmail POST /api/v1/notifications/test/email。
func (h *notificationHandlers) testEmail(c *gin.Context) {
	h.sendTest(c, "email")
}

// testSendTimeout 是测试发送端点的固定超时：HTTP request ctx 本身
// 没有 deadline，不设上限时一个失联 SMTP server 会把请求无限挂住。
// 与 dispatcher 的 sendTimeout 同量级。
const testSendTimeout = 10 * time.Second

// sendTest 经 dispatcher 用已保存配置发送测试消息。发送失败也是一次
// 成功完成的操作：200 携带 ok=false 与错误摘要，不代表测试操作本身
// 失败——与 Source 连接测试同一交互语义。
func (h *notificationHandlers) sendTest(c *gin.Context, channel string) {
	if h.dispatcher == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), testSendTimeout)
	defer cancel()
	if err := h.dispatcher.SendTest(ctx, channel); err != nil {
		if errors.Is(err, notification.ErrNotConfigured) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "channel is not enabled or not configured"})
			return
		}
		// 发送错误本身不区分渠道细节，避免把远端报文原样透传给前端。
		c.JSON(http.StatusOK, gin.H{"ok": false, "error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// handleNotificationError 把通知领域错误映射为 HTTP 响应：校验失败
// 400（携带 field / reason）、未知 500。
func handleNotificationError(c *gin.Context, err error) {
	var invalid *notification.InvalidError
	switch {
	case errors.As(err, &invalid):
		c.JSON(http.StatusBadRequest, gin.H{
			"error":  invalid.Error(),
			"field":  invalid.Field,
			"reason": invalid.Reason,
		})
	default:
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
	}
}

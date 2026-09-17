package api

import (
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"tinysync/internal/auth"
)

// sessionCookieName 是 Web Session cookie 名。
const sessionCookieName = "tinysync_session"

// authHandlers 是认证端点的 handler 集合。
type authHandlers struct {
	svc *auth.Service
}

// registerAuthRoutes 注册公开的登录端点（Origin 校验在 handler 内）。
func registerAuthRoutes(group *gin.RouterGroup, svc *auth.Service) {
	h := &authHandlers{svc: svc}
	group.POST("/auth/login", h.login)
}

// registerSessionRoutes 注册受保护的会话查询与登出端点：仅接受
// Web Session 凭据（Bearer API Token 一律 401）。
func registerSessionRoutes(group *gin.RouterGroup, svc *auth.Service) {
	h := &authHandlers{svc: svc}
	group.GET("/auth/session", requireWebSession(), h.session)
	group.POST("/auth/logout", requireWebSession(), h.logout)
}

// loginRequest 是登录请求体。
type loginRequest struct {
	Password string `json:"password"`
}

// loginResponse 是登录成功响应：expires_at 告知会话绝对过期时刻；
// session 经 Set-Cookie 下发，响应体不含任何凭据。
type loginResponse struct {
	ExpiresAt string `json:"expires_at"`
}

// login 校验密码并创建 Web Session。失败统一 401 invalid
// credentials，不区分 admin 未初始化 / 密码错误 / 凭据缺失。
func (h *authHandlers) login(c *gin.Context) {
	if h.svc == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "authentication is not configured"})
		return
	}
	// 登录端点同样做 CSRF Origin 校验。
	if !sameOrigin(c.Request) {
		c.JSON(http.StatusForbidden, gin.H{"error": "cross-origin request rejected"})
		return
	}
	var req loginRequest
	if !strictBind(c, &req) {
		return
	}
	session, raw, err := h.svc.Login(c.Request.Context(), req.Password)
	if err != nil {
		c.Header("Cache-Control", "no-store")
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid credentials"})
		return
	}
	c.Header("Cache-Control", "no-store")
	http.SetCookie(c.Writer, newSessionCookie(c.Request, raw, session.ExpiresAt))
	c.JSON(http.StatusOK, loginResponse{ExpiresAt: session.ExpiresAt.Format(time.RFC3339)})
}

// session 返回当前会话信息（middleware 已保证有效会话）。
func (h *authHandlers) session(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	sess, ok := sessionOf(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"authenticated": true,
		"subject":       auth.AdminSubject,
		"expires_at":    sess.ExpiresAt.Format(time.RFC3339),
	})
}

// logout 删除当前会话并立即清除浏览器 cookie；幂等。
func (h *authHandlers) logout(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	if sess, ok := sessionOf(c); ok {
		_ = h.svc.Logout(c.Request.Context(), sess.ID)
	}
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		Secure:   requestIsHTTPS(c.Request),
	})
	c.Status(http.StatusNoContent)
}

// newSessionCookie 构造会话 cookie：HttpOnly + SameSite=Strict +
// Path=/；HTTPS 请求（直接 TLS 或反代头）附带 Secure。
func newSessionCookie(r *http.Request, value string, expires time.Time) *http.Cookie {
	return &http.Cookie{
		Name:     sessionCookieName,
		Value:    value,
		Path:     "/",
		Expires:  expires,
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		Secure:   requestIsHTTPS(r),
	}
}

// forwardedProtoTrusted 是受信任的反代协议头取值。
const forwardedProtoHTTPS = "https"

// requestIsHTTPS 判断请求是否经 HTTPS 到达：直接 TLS 连接或反向
// 代理的 X-Forwarded-Proto 头。
func requestIsHTTPS(r *http.Request) bool {
	return r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), forwardedProtoHTTPS)
}

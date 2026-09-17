package api

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/gin-gonic/gin"

	"tinysync/internal/auth"
)

// requireScope 返回授权中间件：要求已认证 principal 具备所需
// scope（admin ⇒ read + run 由 auth.Authorize 判定）。
func requireScope(required auth.Scope) gin.HandlerFunc {
	return func(c *gin.Context) {
		principal, ok := principalOf(c)
		if !ok {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
			return
		}
		if !auth.Authorize(principal, required) {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "insufficient scope"})
			return
		}
		c.Next()
	}
}

// requireWebSession 返回中间件：要求凭据来自 Web Session（auth
// session 端点不接受 Bearer API Token）。
func requireWebSession() gin.HandlerFunc {
	return func(c *gin.Context) {
		if _, ok := sessionOf(c); !ok {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
			return
		}
		c.Next()
	}
}

// gin context 键：principal 与当前 Web Session。
const (
	ctxKeyPrincipal = "auth.principal"
	ctxKeySession   = "auth.session"
)

// queryCredentialKeys 是被禁止的 URL 凭据参数：认证信息绝不放 URL，
// machine client 只认 Authorization: Bearer。
var queryCredentialKeys = []string{"token", "api_key", "access_token"}

// stateChangingMethods 是需要 CSRF Origin 校验的 HTTP 方法。
var stateChangingMethods = map[string]bool{
	http.MethodPost:   true,
	http.MethodPut:    true,
	http.MethodPatch:  true,
	http.MethodDelete: true,
}

// authMiddleware 返回认证中间件：Authorization 头优先且绝不 fallback
// 到 Web Session；deps.Auth 缺失时 fail closed（500），绝不解释为
// 「关闭认证」的匿名模式。
func authMiddleware(svc *auth.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		if svc == nil {
			c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "authentication is not configured"})
			return
		}
		// 认证信息不放 URL：查询串携带凭据参数一律 400。
		for _, key := range queryCredentialKeys {
			if c.Query(key) != "" {
				c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "credentials must not be provided in the URL"})
				return
			}
		}
		// CSRF 纵深防御：携带跨源 Origin 的 state-changing 请求拒绝
		//（主防线是 SameSite=Strict cookie；Bearer 客户端不带 Origin）。
		if stateChangingMethods[c.Request.Method] && !sameOrigin(c.Request) {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "cross-origin request rejected"})
			return
		}
		if header := c.GetHeader("Authorization"); header != "" {
			// Bearer API Token：唯一 machine credential 入口。显式
			// 提供无效凭据绝不 fallback 到 Web Session。
			const bearerPrefix = "Bearer "
			if !strings.HasPrefix(header, bearerPrefix) {
				c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
				return
			}
			raw := strings.TrimSpace(strings.TrimPrefix(header, bearerPrefix))
			if raw == "" {
				c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
				return
			}
			principal, _, err := svc.AuthenticateAPIToken(c.Request.Context(), raw)
			if err != nil {
				c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
				return
			}
			c.Set(ctxKeyPrincipal, principal)
			c.Next()
			return
		}
		rawToken, err := c.Cookie(sessionCookieName)
		if err != nil || rawToken == "" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
			return
		}
		principal, session, err := svc.AuthenticateSession(c.Request.Context(), rawToken)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
			return
		}
		c.Set(ctxKeyPrincipal, principal)
		c.Set(ctxKeySession, session)
		c.Next()
	}
}

// principalOf 返回当前请求的 principal（middleware 认证后注入）。
func principalOf(c *gin.Context) (auth.Principal, bool) {
	v, ok := c.Get(ctxKeyPrincipal)
	if !ok {
		return auth.Principal{}, false
	}
	principal, ok := v.(auth.Principal)
	return principal, ok
}

// sessionOf 返回当前请求的 Web Session（cookie 凭据时存在）。
func sessionOf(c *gin.Context) (auth.WebSession, bool) {
	v, ok := c.Get(ctxKeySession)
	if !ok {
		return auth.WebSession{}, false
	}
	session, ok := v.(auth.WebSession)
	return session, ok
}

// sameOrigin 校验 Origin 头与请求 Host 一致（CSRF 校验）。Origin
// 缺失时放行——非浏览器客户端不存在 CSRF；解析失败或 Host 不一致
// 返回 false。
func sameOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" {
		return false
	}
	return u.Host == r.Host
}

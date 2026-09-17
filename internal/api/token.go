package api

import (
	"errors"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"tinysync/internal/auth"
)

// registerAPITokenRoutes 注册 API Token 管理端点：全部要求 admin
// scope。
func registerAPITokenRoutes(group *gin.RouterGroup, svc *auth.Service) {
	h := &tokenHandlers{svc: svc}
	g := group.Group("/api-tokens", requireScope(auth.ScopeAdmin))
	{
		g.GET("", h.list)
		g.POST("", h.create)
		g.POST("/:id/revoke", h.revoke)
	}
}

// tokenHandlers 是 API Token 端点的 handler 集合。
type tokenHandlers struct {
	svc *auth.Service
}

// apiTokenDTO 是 API Token 的 API 表示：只有元数据——raw token 与
// token_hash 绝不出现在任何响应中。可空时刻输出空串（与既有
// expires_at 契约风格一致）。
type apiTokenDTO struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	Prefix     string   `json:"prefix"`
	Scopes     []string `json:"scopes"`
	CreatedAt  string   `json:"created_at"`
	ExpiresAt  string   `json:"expires_at"`
	LastUsedAt string   `json:"last_used_at"`
	RevokedAt  string   `json:"revoked_at"`
}

func toAPITokenDTO(t auth.APIToken) apiTokenDTO {
	scopes := make([]string, 0, len(t.Scopes))
	for _, s := range t.Scopes {
		scopes = append(scopes, string(s))
	}
	return apiTokenDTO{
		ID:         t.ID,
		Name:       t.Name,
		Prefix:     t.Prefix,
		Scopes:     scopes,
		CreatedAt:  t.CreatedAt.Format(time.RFC3339),
		ExpiresAt:  formatOptionalTime(t.ExpiresAt),
		LastUsedAt: formatOptionalTime(t.LastUsedAt),
		RevokedAt:  formatOptionalTime(t.RevokedAt),
	}
}

// formatOptionalTime 输出 RFC3339 或空串（nil）。
func formatOptionalTime(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.Format(time.RFC3339)
}

// createTokenRequest 是创建请求体：expires_at 为可选 RFC3339。
type createTokenRequest struct {
	Name      string   `json:"name"`
	Scopes    []string `json:"scopes"`
	ExpiresAt string   `json:"expires_at"`
}

// createTokenResponse 是创建响应：raw_token 唯一一次出现。
type createTokenResponse struct {
	APIToken apiTokenDTO `json:"api_token"`
	RawToken string      `json:"raw_token"`
}

// list 返回全部 token 元数据。
func (h *tokenHandlers) list(c *gin.Context) {
	tokens, err := h.svc.ListAPITokens(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}
	dtos := make([]apiTokenDTO, 0, len(tokens))
	for _, t := range tokens {
		dtos = append(dtos, toAPITokenDTO(t))
	}
	c.JSON(http.StatusOK, gin.H{"api_tokens": dtos})
}

// create 校验并创建 token；raw token 只在此响应中出现一次。
func (h *tokenHandlers) create(c *gin.Context) {
	var req createTokenRequest
	if !strictBind(c, &req) {
		return
	}
	input := auth.CreateAPITokenInput{Name: req.Name}
	for _, s := range req.Scopes {
		input.Scopes = append(input.Scopes, auth.Scope(s))
	}
	if req.ExpiresAt != "" {
		expiry, err := parseRFC3339(req.ExpiresAt)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "expires_at must be RFC3339"})
			return
		}
		input.ExpiresAt = &expiry
	}
	token, raw, err := h.svc.CreateAPIToken(c.Request.Context(), input)
	if err != nil {
		if errors.Is(err, auth.ErrInvalidInput) {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}
	c.JSON(http.StatusCreated, createTokenResponse{APIToken: toAPITokenDTO(token), RawToken: raw})
}

// revoke 幂等软撤销：active → revoked、revoked → revoked 均 204。
func (h *tokenHandlers) revoke(c *gin.Context) {
	err := h.svc.RevokeAPIToken(c.Request.Context(), c.Param("id"))
	if err != nil {
		if errors.Is(err, auth.ErrNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "api token not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}
	c.Status(http.StatusNoContent)
}

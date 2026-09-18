package mcp

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	mcpauth "github.com/modelcontextprotocol/go-sdk/auth"

	"tinysync/internal/auth"
)

// verifyToken 构造 MCP SDK 的 TokenVerifier：唯一校验入口是
// auth.Service.AuthenticateAPIToken，MCP 不引入第二套凭据语义。
// 不存在 / 已撤销 / 已过期统一映射为 SDK ErrInvalidToken（→ 401）；
// 存储故障原样返回（→ 500），不伪装成凭据错误。
func verifyToken(svc *auth.Service) mcpauth.TokenVerifier {
	return func(ctx context.Context, raw string, _ *http.Request) (*mcpauth.TokenInfo, error) {
		principal, token, err := svc.AuthenticateAPIToken(ctx, raw)
		if err != nil {
			if errors.Is(err, auth.ErrUnauthorized) || errors.Is(err, auth.ErrNotFound) {
				return nil, fmt.Errorf("%w: invalid api token", mcpauth.ErrInvalidToken)
			}
			return nil, fmt.Errorf("verify api token: %w", err)
		}
		scopes := make([]string, 0, len(principal.Scopes))
		for _, s := range principal.Scopes {
			scopes = append(scopes, string(s))
		}
		info := &mcpauth.TokenInfo{
			Scopes: scopes,
			UserID: principal.SubjectID,
		}
		// 过期判定在 AuthenticateAPIToken 内完成；这里回填真实过期
		// 时刻，让 SDK 中间件对临期 token 的拒绝与服务层一致。
		if token.ExpiresAt != nil {
			info.Expiration = *token.ExpiresAt
		}
		return info, nil
	}
}

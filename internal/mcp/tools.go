package mcp

import (
	"context"
	"errors"
	"fmt"

	mcpauth "github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"tinysync/internal/auth"
)

// 分页参数边界：避免 MCP 一次返回无界数组。越界报 tool error，
// 不做静默截断。
const (
	defaultListLimit = 100
	maxListLimit     = 500
)

// ListInput 是列表类工具的公共输入。
type ListInput struct {
	Limit  int `json:"limit,omitempty"`
	Offset int `json:"offset,omitempty"`
}

// normalize 校验并归一分页参数：limit 0 取默认值，负值与超上限报错。
func (in ListInput) normalize() (limit, offset int, err error) {
	limit = in.Limit
	if limit == 0 {
		limit = defaultListLimit
	}
	if limit < 0 || limit > maxListLimit {
		return 0, 0, fmt.Errorf("limit must be in 1..%d", maxListLimit)
	}
	if in.Offset < 0 {
		return 0, 0, errors.New("offset must not be negative")
	}
	return limit, in.Offset, nil
}

// pageBounds 返回安全的半开分页区间。offset 超过总数时返回空页；先从
// total 减去 offset，再相加，避免合法的大 offset 与 limit 相加溢出。
func pageBounds(total, offset, limit int) (start, end int) {
	if offset >= total {
		return total, total
	}
	pageLen := min(limit, total-offset)
	return offset, offset + pageLen
}

// principalFromContext 还原认证中间件注入的 principal：Bearer 中间件
// 已完成 authentication，这里只取回 scopes 供 per-tool authorization。
func principalFromContext(ctx context.Context) (auth.Principal, bool) {
	info := mcpauth.TokenInfoFromContext(ctx)
	if info == nil {
		return auth.Principal{}, false
	}
	scopes := make([]auth.Scope, 0, len(info.Scopes))
	for _, s := range info.Scopes {
		scopes = append(scopes, auth.Scope(s))
	}
	return auth.Principal{
		Kind:      auth.PrincipalKindAPIToken,
		SubjectID: info.UserID,
		Scopes:    scopes,
	}, true
}

// authorize 校验当前请求具备所需 scope（admin ⇒ read + run 由
// auth.Authorize 判定）。失败返回稳定的 permission denied tool error，
// 与「资源不存在」可区分。
func authorize(ctx context.Context, required auth.Scope) error {
	principal, ok := principalFromContext(ctx)
	if !ok {
		return errors.New("unauthenticated: valid bearer api token required")
	}
	if !auth.Authorize(principal, required) {
		return fmt.Errorf("permission denied: %q scope is required for this tool", required)
	}
	return nil
}

// toolAnnotations 返回只读工具的注解（对 Agent 标注副作用语义）。
func readOnlyAnnotations() *mcp.ToolAnnotations {
	return &mcp.ToolAnnotations{
		ReadOnlyHint:  true,
		OpenWorldHint: boolPtr(false),
	}
}

// destructiveAnnotations 标注 side-effect / destructive-capable 语义：
// mirror Job 运行可能删除「已由该 Job 管理、但远端已不存在」的本地
// 文件。
func destructiveAnnotations() *mcp.ToolAnnotations {
	return &mcp.ToolAnnotations{
		ReadOnlyHint:    false,
		IdempotentHint:  false,
		DestructiveHint: boolPtr(true),
		OpenWorldHint:   boolPtr(true),
	}
}

func boolPtr(b bool) *bool {
	return &b
}

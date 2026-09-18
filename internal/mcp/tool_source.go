package mcp

import (
	"context"
	"errors"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"tinysync/internal/auth"
)

// registerSourceTools 注册只读 Source 发现工具。
func registerSourceTools(server *mcp.Server, deps Deps) {
	if deps.Sources == nil {
		return
	}
	mcp.AddTool(server, &mcp.Tool{
		Name:        "list_sources",
		Description: "List TinySync remote sources (WebDAV / S3 / SFTP) with their non-sensitive config and credential state. Secrets are never included.",
		Annotations: readOnlyAnnotations(),
	}, func(ctx context.Context, req *mcp.CallToolRequest, input ListInput) (*mcp.CallToolResult, listSourcesResult, error) {
		limit, offset, err := input.normalize()
		if err != nil {
			return nil, listSourcesResult{}, fmt.Errorf("invalid input: %w", err)
		}
		if err := authorize(ctx, auth.ScopeRead); err != nil {
			return nil, listSourcesResult{}, err
		}
		sources, err := deps.Sources.List(ctx)
		if err != nil {
			return nil, listSourcesResult{}, errors.New("internal error")
		}
		result := listSourcesResult{Total: len(sources), Offset: offset}
		start, end := pageBounds(len(sources), offset, limit)
		for _, s := range sources[start:end] {
			summary, err := toSourceSummary(s)
			if err != nil {
				return nil, listSourcesResult{}, errors.New("internal error")
			}
			result.Sources = append(result.Sources, summary)
		}
		return nil, result, nil
	})
}

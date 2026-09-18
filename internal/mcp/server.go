// Package mcp 是 MCP（Model Context Protocol）adapter：为现有应用
// 服务提供受 API Token 保护的 Streamable HTTP 入口。MCP 是 transport，
// 不建立第二套业务实现：tools / resources 全部委托应用服务层，认证
// 完全复用 auth.Service 与 read / run / admin scope。
//
// SDK 依赖只出现在本包；internal/api 仅通过 http.Handler 接入。
package mcp

import (
	"context"
	"net/http"

	mcpauth "github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"tinysync/internal/auth"
	"tinysync/internal/browser"
	"tinysync/internal/buildinfo"
	"tinysync/internal/source"
	"tinysync/internal/syncjob"
)

// ProtocolVersion 是 v0.8 固定支持的 MCP 协议版本；旧版本协商一律
// 失败，不引入兼容层。
const ProtocolVersion = "2026-07-28"

// MaxRequestBodyBytes 是 /mcp 请求体上限（1 MiB）：MCP 契约只承载
// 查询与小参数请求，超限 413。
const MaxRequestBodyBytes = 1 << 20

// Deps 是 MCP adapter 的应用服务依赖集合。Auth 必须非 nil（由装配
// 根保证：serve 强制要求已初始化认证）。
type Deps struct {
	Auth       *auth.Service
	Sources    *source.Service
	Jobs       *syncjob.Service
	Runner     *syncjob.Runner
	LocalFiles *browser.LocalService
}

// New 构造完整的 /mcp handler 链：
//
//	CrossOriginProtection → RequireBearerToken → StreamableHTTPHandler
//
// Cross-origin protection 显式启用而不依赖 SDK 默认值；SDK 的
// localhost DNS-rebinding protection 保持默认开启。Bearer 中间件只做
// authentication（不设全局 scope，run-only token 也必须能进入 /mcp），
// authorization 由各 tool / resource handler 按 scope 判定。
func New(deps Deps) http.Handler {
	server := mcp.NewServer(
		&mcp.Implementation{Name: "tinysync", Version: buildinfo.Version},
		&mcp.ServerOptions{
			SupportedProtocolVersions: []string{ProtocolVersion},
			// TinySync 的结果是认证后的私有数据：cacheScope=private、
			// ttlMs=0，第一版不做客户端缓存优化。
			SetCacheable: func(_ context.Context, _ mcp.Request, c *mcp.Cacheable) {
				c.CacheScope = "private"
				c.TTLMs = 0
			},
		},
	)
	registerTools(server, deps)
	registerResources(server, deps)

	streamable := mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return server },
		&mcp.StreamableHTTPOptions{
			Stateless:                    true,
			JSONResponse:                 true,
			PropagateRequestCancellation: true,
			MaxRequestBodyBytes:          MaxRequestBodyBytes,
		},
	)
	protected := mcpauth.RequireBearerToken(verifyToken(deps.Auth), &mcpauth.RequireBearerTokenOptions{
		// TinySync API Token 允许永不过期，过期判定在 verifier 内
		// 完成并回填 Expiration。
		AllowMissingExpiration: true,
	})
	return http.NewCrossOriginProtection().Handler(protected(streamable))
}

// registerTools 注册 v0.8 tools；实现分别落在 tool_source.go、
// tool_job.go、tool_run.go 与 tool_file.go。
func registerTools(server *mcp.Server, deps Deps) {
	registerSourceTools(server, deps)
	registerJobTools(server, deps)
	registerRunTools(server, deps)
	registerFileTools(server, deps)
}

package mcp

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// callTool 经官方 MCP client 调用工具：返回 structured content 的
// 再序列化 JSON、完整结果与 client 侧错误。IsError 结果以 res.IsError
// 表达（client 不转为 error）。
func callTool(session *mcp.ClientSession, name string, args any) (json.RawMessage, *mcp.CallToolResult, error) {
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      name,
		Arguments: args,
	})
	if err != nil {
		return nil, res, err
	}
	raw, err := json.Marshal(res.StructuredContent)
	if err != nil {
		return nil, res, err
	}
	return raw, res, nil
}

// mustCallTool 调用工具并断言成功（非 error、非 is_error）。
func mustCallTool(t *testing.T, session *mcp.ClientSession, name string, args any) json.RawMessage {
	t.Helper()
	raw, res, err := callTool(session, name, args)
	if err != nil {
		t.Fatalf("call %s: %v", name, err)
	}
	if res.IsError {
		t.Fatalf("call %s returned tool error: %s", name, raw)
	}
	return raw
}

// remarshal 把 structured content JSON 解码到强类型输出。
func remarshal(raw json.RawMessage, out any) error {
	return json.Unmarshal(raw, out)
}

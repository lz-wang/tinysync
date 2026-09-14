# v0.8.0 — MCP Integration

总体进度与当前优先级见 [ROADMAP.md](../../ROADMAP.md)。以下均为规划；模型、接口和路由示例用于设计讨论，不代表当前可用契约。

目标：让 Agent / LLM 可以通过 MCP 管理 TinySync 和读取同步文件信息。

原则：

> MCP 是现有 application service 的 adapter，不建立第二套业务实现。

使用官方 Go MCP SDK，并采用 Streamable HTTP。

## Tools

建议：

```text
list_sources
list_jobs
get_job
run_sync
get_sync_run
search_files
get_file_info
```

涉及修改配置的工具应谨慎开放。

## Resources

适合小型文本内容或元数据：

```text
tinysync://files/...
```

大型 binary 不通过 MCP base64 搬运。

正确方式：

```text
MCP
 → metadata / download URL
 → HTTP Range download
```

## Authorization

MCP 复用 API Token / scope 模型。

## 完成标准

> Agent 可以发现同步源、查询任务、触发同步、检索本地文件信息，并通过 HTTP 获取大文件。
